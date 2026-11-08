package identity_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestMySQLPermissionContextValidationResolutionAndRedaction(t *testing.T) {
	f := newGroupFixture(t)
	t.Cleanup(func() {
		for _, table := range []string{"af_identity_user_group_members", "af_identity_user_groups", "af_identity_sessions", "af_identity_audit", "af_identity_users", "af_identity_roles", "af_identity_tenants", "af_meta_definitions"} {
			if _, err := f.db.Exec("DELETE FROM "+table+" WHERE tenant_id=?", f.tenant); err != nil {
				t.Error("cleanup permission context tenant")
			}
		}
	})
	roleID := f.seedRoles(1)[0]
	worker := f.seedUsers(1)[0]
	roleJSON, _ := json.Marshal([]string{roleID})
	if _, err := f.db.Exec("UPDATE af_identity_users SET role_ids=? WHERE tenant_id=? AND id=?", roleJSON, f.tenant, worker.ID); err != nil {
		t.Fatal("assign fixture role")
	}
	seed := func(properties []c.Property, indexes []c.Index) c.Definition {
		d := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: "权限上下文-" + c.NewID(), Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: properties, Indexes: indexes}}
		raw, _ := json.Marshal(d)
		if _, err := f.db.Exec("INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,current_json,current_digest,status) VALUES (?,?,?,?,?,?,?,?,?,'active')", d.ID, f.tenant, d.CategoryID, d.GroupID, d.Kind, d.Name, []byte(d.Name), string(raw), strings.Repeat("a", 64)); err != nil {
			t.Fatal("seed context schema")
		}
		return d
	}
	owner, dept, flag := c.NewID(), c.NewID(), c.NewID()
	target := seed([]c.Property{{ID: owner, Name: "负责人", DataType: "uuid"}, {ID: dept, Name: "部门", DataType: "string"}, {ID: flag, Name: "状态", DataType: "boolean"}}, nil)
	key, sourceField, sourceFlag := c.NewID(), c.NewID(), c.NewID()
	source := seed([]c.Property{{ID: key, Name: "用户", DataType: "uuid"}, {ID: sourceField, Name: "部门", DataType: "string"}, {ID: sourceFlag, Name: "状态", DataType: "boolean"}}, []c.Index{{ID: c.NewID(), Name: "唯一用户", Unique: true, FieldIDs: []string{key}}})
	base := f.policy().Current.Rules
	ref := c.PermissionValueRef{Source: "object", SchemaID: source.ID, FieldID: sourceField, MatchFieldID: key, MatchUserAttribute: "id"}
	makeRule := func(e *c.Expression, op string) c.PermissionRule {
		return c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "allow", Operation: op, ResourceID: target.ID, ReadFields: []string{owner}, WriteFields: []string{dept}, RowFilter: e}
	}
	publish := func(rules ...c.PermissionRule) {
		policy := f.policy()
		all := append(append([]c.PermissionRule{}, base...), rules...)
		w := f.request("PUT", "/metadata/admin/policy", &f.admin, policy.ETag, c.PermissionPolicy{Rules: all})
		f.check(w, 200)
		draft := groupDecode[c.PolicyRecord](t, w)
		f.check(f.request("POST", "/metadata/admin/policy/publish", &f.admin, draft.ETag, nil), 200)
	}
	// Saving validates source binding, target type, uniqueness and unsupported
	// operands before any attribute read can occur.
	for _, invalid := range []c.PermissionValueRef{
		{Source: "user", Attribute: "id", SchemaID: source.ID},
		{Source: "object", SchemaID: c.NewID(), FieldID: sourceField, InstanceID: c.NewID()},
		{Source: "object", SchemaID: source.ID, FieldID: sourceFlag, InstanceID: c.NewID()},
		{Source: "object", SchemaID: source.ID, FieldID: sourceField, MatchFieldID: sourceField, MatchUserAttribute: "username"},
		{Source: "object", SchemaID: source.ID, FieldID: sourceField, InstanceID: "not-an-id"},
	} {
		r := makeRule(&c.Expression{Op: "eq", Field: dept, ValueRef: &invalid}, "instance.query")
		f.check(f.request("PUT", "/metadata/admin/policy", &f.admin, f.policy().ETag, c.PermissionPolicy{Rules: append(append([]c.PermissionRule{}, base...), r)}), 422)
	}
	boolRef := c.PermissionValueRef{Source: "object", SchemaID: source.ID, FieldID: sourceFlag, InstanceID: c.NewID()}
	badBool := makeRule(&c.Expression{Op: "gt", Field: flag, ValueRef: &boolRef}, "instance.query")
	f.check(f.request("PUT", "/metadata/admin/policy", &f.admin, f.policy().ETag, c.PermissionPolicy{Rules: append(append([]c.PermissionRule{}, base...), badBool)}), 422)
	secret := "hidden-context-" + c.NewID()
	value, stamp := secret, strings.Repeat("b", 64)
	calls := 0
	f.store.ObjectContext = func(_ context.Context, tenant string, input c.ObjectContextRequest) (c.ObjectContextResult, error) {
		calls++
		match, _ := json.Marshal(worker.ID)
		if tenant != f.tenant || input.SchemaID != source.ID || input.FieldID != sourceField || input.MatchFieldID != key || string(input.MatchValue) != string(match) {
			t.Fatal("context reader escaped policy binding")
		}
		raw, _ := json.Marshal(value)
		return c.ObjectContextResult{Value: raw, Digest: stamp}, nil
	}
	r := makeRule(&c.Expression{Op: "eq", Field: dept, ValueRef: &ref}, "instance.patch")
	r.NewRowFilter = &c.Expression{Op: "eq", Field: dept, ValueRef: &ref}
	publish(r)
	request := c.AuthorizationRequest{Operation: "instance.patch", ResourceID: target.ID, Kind: c.KindSchema}
	p := c.Principal{TenantID: f.tenant, Subject: worker.ID}
	decision, err := f.store.Authorize(context.Background(), p, request)
	if err != nil || !decision.Allow || decision.RowFilter.ValueRef != nil || decision.NewRowFilter.ValueRef != nil || calls != 2 || len(decision.ContextDigest) != 64 || len(decision.ContextSchemaIDs) != 1 || decision.ContextSchemaIDs[0] != source.ID {
		t.Fatal("context was not resolved and rechecked with dependency evidence", err)
	}
	w := f.request("POST", "/metadata/admin/simulate", &f.admin, "", c.SimulationRequest{UserID: worker.ID, Request: request})
	f.check(w, 200)
	simulation := groupDecode[c.SimulationResult](t, w)
	if !simulation.ContextProtected || !simulation.Decision.Allow || simulation.Decision.RowFilter != nil || simulation.Decision.NewRowFilter != nil || simulation.Decision.ContextDigest != "" || strings.Contains(w.Body.String(), secret) {
		t.Fatal("simulation disclosed protected context")
	}
	before := decision.ContextDigest
	stamp = strings.Repeat("c", 64)
	decision, err = f.store.Authorize(context.Background(), p, request)
	if err != nil || decision.ContextDigest == before {
		t.Fatal("source evidence change was not reflected")
	}
	value = ""
	f.store.ObjectContext = func(context.Context, string, c.ObjectContextRequest) (c.ObjectContextResult, error) {
		calls++
		return c.ObjectContextResult{}, errors.New("unavailable")
	}
	if _, err = f.store.Authorize(context.Background(), p, request); err == nil {
		t.Fatal("attribute failure allowed request")
	}
	deny := r
	deny.ID = c.NewID()
	deny.Effect = "deny"
	deny.ReadFields = nil
	deny.WriteFields = nil
	deny.RowFilter = nil
	deny.NewRowFilter = nil
	publish(r, deny)
	beforeCalls := calls
	decision, err = f.store.Authorize(context.Background(), p, request)
	if err != nil || decision.Allow || calls != beforeCalls {
		t.Fatal("explicit deny attempted unnecessary object reads")
	}
	// User and group providers use the same authoritative role/membership state.
	for _, attribute := range []string{"id", "roleIds", "userGroupIds"} {
		if attribute == "userGroupIds" {
			emptyRef := c.PermissionValueRef{Source: "user", Attribute: attribute}
			publish(makeRule(&c.Expression{Op: "not", Args: []c.Expression{{Op: "in", Field: owner, ValueRef: &emptyRef}}}, "instance.query"))
			if _, err := f.store.Authorize(context.Background(), p, c.AuthorizationRequest{Operation: "instance.query", ResourceID: target.ID, Kind: c.KindSchema}); err == nil {
				t.Fatal("NOT turned a missing context collection into an allow")
			}
			f.createGroup(c.UserGroupInput{Name: "上下文组", Enabled: true, RoleIDs: []string{roleID}, UserIDs: []string{worker.ID}})
		}
		userRef := c.PermissionValueRef{Source: "user", Attribute: attribute}
		op := "eq"
		if attribute != "id" {
			op = "in"
		}
		publish(makeRule(&c.Expression{Op: op, Field: owner, ValueRef: &userRef}, "instance.query"))
		query := c.AuthorizationRequest{Operation: "instance.query", ResourceID: target.ID, Kind: c.KindSchema}
		decision, err = f.store.Authorize(context.Background(), p, query)
		if err != nil || !decision.Allow || decision.RowFilter.ValueRef != nil || len(decision.RowFilter.Value) == 0 {
			t.Fatal("user provider failed", attribute, err)
		}
	}
	// Validation must preserve existing fixed collection operands too. A seeded
	// RawMessage destination used to overwrite the complete array while checking
	// its first element, corrupting both policy persistence and resolved sets.
	fixed, _ := json.Marshal([]string{worker.ID})
	publish(makeRule(&c.Expression{Op: "in", Field: owner, Value: fixed}, "instance.query"))
	saved := f.policy().Current.Rules
	if string(saved[len(saved)-1].RowFilter.Value) != string(fixed) || !json.Valid(fixed) {
		t.Fatal("fixed collection validation mutated policy input")
	}
	if _, err := f.store.Authorize(context.Background(), p, c.AuthorizationRequest{Operation: "instance.query", ResourceID: target.ID, Kind: c.KindSchema}); err != nil {
		t.Fatal("fixed collection grant regressed", err)
	}
}
