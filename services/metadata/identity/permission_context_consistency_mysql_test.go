package identity_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestMySQLPermissionContextRechecksCollectedValuesAndSourceBindings(t *testing.T) {
	f := newGroupFixture(t)
	t.Cleanup(func() {
		for _, table := range []string{"af_identity_user_group_members", "af_identity_user_groups", "af_identity_sessions", "af_identity_audit", "af_identity_users", "af_identity_roles", "af_identity_tenants", "af_meta_definitions"} {
			if _, err := f.db.Exec("DELETE FROM "+table+" WHERE tenant_id=?", f.tenant); err != nil {
				t.Error("cleanup context consistency tenant")
			}
		}
	})
	roleID := f.seedRoles(1)[0]
	worker := f.seedUsers(1)[0]
	roleJSON, _ := json.Marshal([]string{roleID})
	if _, err := f.db.Exec("UPDATE af_identity_users SET role_ids=? WHERE tenant_id=? AND id=?", roleJSON, f.tenant, worker.ID); err != nil {
		t.Fatal("assign context consistency role")
	}
	seed := func(properties []c.Property, indexes []c.Index) c.Definition {
		d := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: "上下文一致性-" + c.NewID(), Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: properties, Indexes: indexes}}
		raw, _ := json.Marshal(d)
		if _, err := f.db.Exec("INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,current_json,current_digest,status) VALUES (?,?,?,?,?,?,?,?,?,'active')", d.ID, f.tenant, d.CategoryID, d.GroupID, d.Kind, d.Name, []byte(d.Name), string(raw), strings.Repeat("a", 64)); err != nil {
			t.Fatal("seed context consistency schema")
		}
		return d
	}
	targetDept, targetLimit := c.NewID(), c.NewID()
	target := seed([]c.Property{{ID: targetDept, Name: "部门", DataType: "string"}, {ID: targetLimit, Name: "额度", DataType: "int64"}}, nil)
	userKey, sourceDept, sourceLimit := c.NewID(), c.NewID(), c.NewID()
	source := seed([]c.Property{{ID: userKey, Name: "用户", DataType: "uuid"}, {ID: sourceDept, Name: "部门", DataType: "string"}, {ID: sourceLimit, Name: "额度", DataType: "int64"}}, []c.Index{{ID: c.NewID(), Name: "唯一用户", Unique: true, FieldIDs: []string{userKey}}})
	sourceRaw, _ := json.Marshal(source)
	setSource := func(raw []byte) {
		if _, err := f.db.Exec("UPDATE af_meta_definitions SET current_json=? WHERE tenant_id=? AND id=?", string(raw), f.tenant, source.ID); err != nil {
			t.Fatal("update context source definition")
		}
	}
	base := f.policy().Current.Rules
	publish := func(operation string, row, newRow *c.Expression) {
		policy := f.policy()
		rule := c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "allow", Operation: operation, ResourceID: target.ID, ReadFields: []string{"*"}, WriteFields: []string{"*"}, RowFilter: row, NewRowFilter: newRow}
		w := f.request("PUT", "/metadata/admin/policy", &f.admin, policy.ETag, c.PermissionPolicy{Rules: append(append([]c.PermissionRule{}, base...), rule)})
		f.check(w, 200)
		draft := groupDecode[c.PolicyRecord](t, w)
		f.check(f.request("POST", "/metadata/admin/policy/publish", &f.admin, draft.ETag, nil), 200)
	}
	p := c.Principal{TenantID: f.tenant, Subject: worker.ID}
	authorize := func(operation string) (c.Decision, error) {
		return f.store.Authorize(context.Background(), p, c.AuthorizationRequest{Operation: operation, ResourceID: target.ID, Kind: c.KindSchema})
	}
	instance := c.NewID()
	deptRef := c.PermissionValueRef{Source: "object", SchemaID: source.ID, FieldID: sourceDept, InstanceID: instance}
	limitRef := c.PermissionValueRef{Source: "object", SchemaID: source.ID, FieldID: sourceLimit, InstanceID: instance}
	deptCondition := func(ref c.PermissionValueRef) *c.Expression {
		return &c.Expression{Op: "eq", Field: targetDept, ValueRef: &ref}
	}
	privateValue := "context-private-" + c.NewID()
	privateRaw, _ := json.Marshal(privateValue)
	assertUnavailable := func(t *testing.T, decision c.Decision, err error) {
		t.Helper()
		var access *platform.AccessError
		if decision.Allow || !errors.As(err, &access) || access.Status != 403 || access.Code != "permission_context_unavailable" || strings.Contains(err.Error(), privateValue) {
			t.Fatal("changed context was not rejected without protected values")
		}
	}

	t.Run("equivalent scalar references share both reads without changing saved policy", func(t *testing.T) {
		explicit := deptRef
		explicit.Mode = "scalar"
		publish("instance.patch", deptCondition(deptRef), deptCondition(explicit))
		calls := 0
		var deadline time.Time
		f.store.ObjectContext = func(ctx context.Context, tenant string, input c.ObjectContextRequest) (c.ObjectContextResult, error) {
			calls++
			currentDeadline, bounded := ctx.Deadline()
			if !bounded || time.Until(currentDeadline) > 4*time.Second || calls > 1 && !currentDeadline.Equal(deadline) {
				t.Fatal("object recheck extended the decision's shared context deadline")
			}
			deadline = currentDeadline
			if tenant != f.tenant || input.Mode != "scalar" || input.FieldID != sourceDept || input.InstanceID != instance {
				t.Fatal("scalar normalization changed the configured selector")
			}
			return c.ObjectContextResult{Value: privateRaw, Digest: strings.Repeat("b", 64)}, nil
		}
		decision, err := authorize("instance.patch")
		if err != nil || !decision.Allow || calls != 2 || string(decision.RowFilter.Value) != string(privateRaw) || string(decision.NewRowFilter.Value) != string(privateRaw) {
			t.Fatal("equivalent operands were not collected and rechecked together")
		}
		saved := f.policy().Current.Rules
		last := saved[len(saved)-1]
		if last.RowFilter.ValueRef == nil || last.RowFilter.ValueRef.Mode != "" || last.NewRowFilter.ValueRef == nil || last.NewRowFilter.ValueRef.Mode != "scalar" || len(last.RowFilter.Value) != 0 || len(last.NewRowFilter.Value) != 0 {
			t.Fatal("runtime normalization or resolution mutated saved policy")
		}
	})

	for _, changed := range []string{"digest only", "value only", "lookup fails"} {
		t.Run(changed, func(t *testing.T) {
			publish("instance.query", deptCondition(deptRef), nil)
			calls := 0
			f.store.ObjectContext = func(context.Context, string, c.ObjectContextRequest) (c.ObjectContextResult, error) {
				calls++
				result := c.ObjectContextResult{Value: privateRaw, Digest: strings.Repeat("b", 64)}
				if calls == 2 {
					switch changed {
					case "digest only":
						result.Digest = strings.Repeat("c", 64)
					case "value only":
						result.Value = json.RawMessage(`"other"`)
					case "lookup fails":
						return c.ObjectContextResult{}, errors.New(privateValue)
					}
				}
				return result, nil
			}
			decision, err := authorize("instance.query")
			assertUnavailable(t, decision, err)
			if calls != 2 {
				t.Fatal("object operand was not rechecked")
			}
		})
	}

	t.Run("different fields cannot combine old and new row evidence", func(t *testing.T) {
		row := &c.Expression{Op: "and", Args: []c.Expression{*deptCondition(deptRef), {Op: "lte", Field: targetLimit, ValueRef: &limitRef}}}
		publish("instance.query", row, nil)
		var fields []string
		f.store.ObjectContext = func(_ context.Context, _ string, input c.ObjectContextRequest) (c.ObjectContextResult, error) {
			fields = append(fields, input.FieldID)
			if input.FieldID == sourceLimit {
				return c.ObjectContextResult{Value: json.RawMessage(`"1000"`), Digest: strings.Repeat("d", 64)}, nil
			}
			if len(fields) == 1 {
				return c.ObjectContextResult{Value: privateRaw, Digest: strings.Repeat("b", 64)}, nil
			}
			return c.ObjectContextResult{Value: json.RawMessage(`"new department"`), Digest: strings.Repeat("c", 64)}, nil
		}
		decision, err := authorize("instance.query")
		assertUnavailable(t, decision, err)
		if len(fields) != 3 || fields[0] != sourceDept || fields[1] != sourceLimit || fields[2] != sourceDept {
			t.Fatal("recheck began before all distinct operands were collected")
		}
	})

	t.Run("last collected operand is also rechecked", func(t *testing.T) {
		row := &c.Expression{Op: "and", Args: []c.Expression{*deptCondition(deptRef), {Op: "lte", Field: targetLimit, ValueRef: &limitRef}}}
		publish("instance.query", row, nil)
		var fields []string
		f.store.ObjectContext = func(_ context.Context, _ string, input c.ObjectContextRequest) (c.ObjectContextResult, error) {
			fields = append(fields, input.FieldID)
			if input.FieldID == sourceDept {
				return c.ObjectContextResult{Value: privateRaw, Digest: strings.Repeat("b", 64)}, nil
			}
			stamp := strings.Repeat("c", 64)
			if len(fields) == 4 {
				stamp = strings.Repeat("d", 64)
			}
			return c.ObjectContextResult{Value: json.RawMessage(`"1000"`), Digest: stamp}, nil
		}
		decision, err := authorize("instance.query")
		assertUnavailable(t, decision, err)
		if len(fields) != 4 || fields[0] != sourceDept || fields[1] != sourceLimit || fields[2] != sourceDept || fields[3] != sourceLimit {
			t.Fatal("not all distinct object operands were rechecked")
		}
	})

	for _, changed := range []string{"source type", "unique selector"} {
		t.Run(changed, func(t *testing.T) {
			setSource(sourceRaw)
			t.Cleanup(func() { setSource(sourceRaw) })
			ref := deptRef
			ref.InstanceID = ""
			ref.MatchFieldID, ref.MatchUserAttribute = userKey, "id"
			publish("instance.query", deptCondition(ref), nil)
			calls := 0
			f.store.ObjectContext = func(_ context.Context, _ string, input c.ObjectContextRequest) (c.ObjectContextResult, error) {
				calls++
				if calls == 2 {
					var changedSource c.Definition
					if json.Unmarshal(sourceRaw, &changedSource) != nil {
						t.Fatal("clone source definition")
					}
					if changed == "source type" {
						changedSource.Schema.Properties[1].DataType = "text"
					} else {
						changedSource.Schema.Indexes = nil
					}
					raw, _ := json.Marshal(changedSource)
					setSource(raw)
				}
				return c.ObjectContextResult{Value: privateRaw, Digest: strings.Repeat("b", 64)}, nil
			}
			decision, err := authorize("instance.query")
			assertUnavailable(t, decision, err)
			if calls != 2 {
				t.Fatal("source binding race was not checked after reads")
			}
		})
	}
}
