package identity_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestMySQLLinkPoliciesBindCurrentTenantAndStaySeparateFromEndpoints(t *testing.T) {
	f := newGroupFixture(t)
	defer func() {
		for _, table := range []string{"af_identity_user_group_members", "af_identity_user_groups", "af_identity_sessions", "af_identity_audit", "af_identity_users", "af_identity_roles", "af_identity_tenants", "af_meta_definitions"} {
			if _, err := f.db.Exec("DELETE FROM "+table+" WHERE tenant_id=?", f.tenant); err != nil {
				t.Errorf("clean isolated Link policy tenant: %v", err)
			}
		}
	}()
	roleID := f.seedRoles(1)[0]
	worker := f.seedUsers(1)[0]
	roles, _ := json.Marshal([]string{roleID})
	if _, err := f.db.Exec("UPDATE af_identity_users SET role_ids=? WHERE tenant_id=? AND id=?", string(roles), f.tenant, worker.ID); err != nil {
		t.Fatal(err)
	}
	worker.RoleIDs = []string{roleID}
	seed := func(kind c.Kind, tenant, status string) c.Definition {
		d := c.Definition{ID: c.NewID(), TenantID: tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: kind, Name: "关系权限-" + c.NewID()}
		if kind == c.KindLink {
			d.Link = &c.Link{SourceSchemaID: c.NewID(), TargetSchemaID: c.NewID(), ForwardName: "关联", ReverseName: "被关联", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}
		} else {
			d.Schema = &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{ID: c.NewID(), Name: "名称", DataType: "string"}}}
		}
		raw, _ := json.Marshal(d)
		if _, err := f.db.Exec("INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,current_json,current_digest,status) VALUES (?,?,?,?,?,?,?,?,?,?)", d.ID, tenant, d.CategoryID, d.GroupID, kind, d.Name, []byte(d.Name), string(raw), strings.Repeat("a", 64), status); err != nil {
			t.Fatal("seed isolated Link policy definition")
		}
		return d
	}
	link := seed(c.KindLink, f.tenant, "active")
	schema := seed(c.KindSchema, f.tenant, "active")
	draftLink := seed(c.KindLink, f.tenant, "draft")
	foreign := seed(c.KindLink, "foreign-"+c.NewID(), "active")
	defer f.db.Exec("DELETE FROM af_meta_definitions WHERE tenant_id=? AND id=?", foreign.TenantID, foreign.ID)
	policy := f.policy()
	for _, operation := range []string{"link.query", "link.create", "link.delete"} {
		query := c.AuthorizationRequest{Operation: operation, ResourceID: link.ID, Kind: c.KindLink}
		f.expectDecision(worker, query, false)
		for _, id := range []string{schema.ID, draftLink.ID, foreign.ID, c.NewID()} {
			candidate := c.PermissionPolicy{Rules: append([]c.PermissionRule{}, policy.Current.Rules...)}
			candidate.Rules = append(candidate.Rules, c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "allow", Operation: operation, ResourceID: id})
			f.check(f.request("PUT", "/metadata/admin/policy", &f.admin, policy.ETag, candidate), 422)
		}
	}
	candidate := c.PermissionPolicy{Rules: append([]c.PermissionRule{}, policy.Current.Rules...)}
	for _, operation := range []string{"link.query", "link.create", "link.delete"} {
		candidate.Rules = append(candidate.Rules, c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "allow", Operation: operation, ResourceID: link.ID, ReadFields: []string{"*"}})
	}
	w := f.request("PUT", "/metadata/admin/policy", &f.admin, policy.ETag, candidate)
	f.check(w, 200)
	draft := groupDecode[c.PolicyRecord](t, w)
	if _, err := f.db.Exec("UPDATE af_meta_definitions SET status='draft' WHERE tenant_id=? AND id=?", f.tenant, link.ID); err != nil {
		t.Fatal(err)
	}
	f.check(f.request("POST", "/metadata/admin/policy/publish", &f.admin, draft.ETag, nil), 422)
	if _, err := f.db.Exec("UPDATE af_meta_definitions SET status='active' WHERE tenant_id=? AND id=?", f.tenant, link.ID); err != nil {
		t.Fatal(err)
	}
	f.check(f.request("POST", "/metadata/admin/policy/publish", &f.admin, draft.ETag, nil), 200)
	for _, operation := range []string{"link.query", "link.create", "link.delete"} {
		f.expectDecision(worker, c.AuthorizationRequest{Operation: operation, ResourceID: link.ID, Kind: c.KindLink}, true)
	}
	f.expectDecision(worker, c.AuthorizationRequest{Operation: "instance.query", ResourceID: schema.ID, Kind: c.KindSchema}, false)
	// Re-running bootstrap may not add or reset permissions on existing tenants.
	before := f.policy()
	if err := f.store.Bootstrap(context.Background(), f.tenant, "admin", groupTestPassword); err != nil {
		t.Fatal(err)
	}
	after := f.policy()
	if after.ETag != before.ETag || after.State != before.State || len(after.Current.Rules) != len(before.Current.Rules) {
		t.Fatal("bootstrap modified an existing tenant policy")
	}
}
