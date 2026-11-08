package identity_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestMySQLCallablePoliciesBindTypeTenantAndRecheckRevocation(t *testing.T) {
	f := newGroupFixture(t)
	defer func() {
		for _, table := range []string{"af_identity_user_group_members", "af_identity_user_groups", "af_identity_sessions", "af_identity_audit", "af_identity_users", "af_identity_roles", "af_identity_tenants", "af_meta_definitions"} {
			if _, err := f.db.Exec("DELETE FROM "+table+" WHERE tenant_id=?", f.tenant); err != nil {
				t.Error(err)
			}
		}
	}()
	roleID := f.seedRoles(1)[0]
	worker := f.seedUsers(1)[0]
	roles, _ := json.Marshal([]string{roleID})
	if _, err := f.db.Exec("UPDATE af_identity_users SET role_ids=? WHERE tenant_id=? AND id=?", string(roles), f.tenant, worker.ID); err != nil {
		t.Fatal(err)
	}
	seed := func(kind c.Kind, tenant, status string) c.Definition {
		d := c.Definition{ID: c.NewID(), TenantID: tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: kind, Name: "调用权限-" + c.NewID()}
		if kind == c.KindFunction {
			d.Function = &c.Callable{SideEffects: "none"}
		} else {
			d.Action = &c.Callable{SideEffects: "none"}
		}
		raw, _ := json.Marshal(d)
		if _, err := f.db.Exec("INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,current_json,current_digest,status) VALUES (?,?,?,?,?,?,?,?,?,?)", d.ID, tenant, d.CategoryID, d.GroupID, kind, d.Name, []byte(d.Name), string(raw), strings.Repeat("a", 64), status); err != nil {
			t.Fatal(err)
		}
		return d
	}
	function := seed(c.KindFunction, f.tenant, "active")
	action := seed(c.KindAction, f.tenant, "active")
	draftFunction := seed(c.KindFunction, f.tenant, "draft")
	validDraft := seed(c.KindFunction, f.tenant, "draft")
	if _, err := f.db.Exec("UPDATE af_meta_definitions SET draft_json=current_json,draft_digest=current_digest,current_json=NULL,current_digest='' WHERE tenant_id=? AND id=?", f.tenant, validDraft.ID); err != nil {
		t.Fatal(err)
	}
	foreign := seed(c.KindFunction, "foreign-"+c.NewID(), "active")
	defer f.db.Exec("DELETE FROM af_meta_definitions WHERE tenant_id=? AND id=?", foreign.TenantID, foreign.ID)
	functionRequest := c.AuthorizationRequest{Operation: "function.invoke", ResourceID: function.ID, Kind: c.KindFunction}
	actionRequest := c.AuthorizationRequest{Operation: "action.invoke", ResourceID: action.ID, Kind: c.KindAction}
	f.expectDecision(worker, functionRequest, false)
	f.expectDecision(worker, actionRequest, false)
	policy := f.policy()
	for operation, ids := range map[string][]string{"function.invoke": {action.ID, draftFunction.ID, foreign.ID, c.NewID()}, "action.invoke": {function.ID}} {
		for _, id := range ids {
			candidate := c.PermissionPolicy{Rules: append([]c.PermissionRule{}, policy.Current.Rules...)}
			candidate.Rules = append(candidate.Rules, c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "allow", Operation: operation, ResourceID: id})
			f.check(f.request("PUT", "/metadata/admin/policy", &f.admin, policy.ETag, candidate), 422)
		}
	}
	grant := c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "allow", Operation: "function.invoke", ResourceID: function.ID}
	draftGrant := c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "allow", Operation: "function.invoke", ResourceID: validDraft.ID}
	candidate := c.PermissionPolicy{Rules: append(append([]c.PermissionRule{}, policy.Current.Rules...), grant, draftGrant)}
	w := f.request("PUT", "/metadata/admin/policy", &f.admin, policy.ETag, candidate)
	f.check(w, 200)
	draft := groupDecode[c.PolicyRecord](t, w)
	if _, err := f.db.Exec("UPDATE af_meta_definitions SET status='draft' WHERE tenant_id=? AND id=?", f.tenant, function.ID); err != nil {
		t.Fatal(err)
	}
	f.check(f.request("POST", "/metadata/admin/policy/publish", &f.admin, draft.ETag, nil), 422)
	if _, err := f.db.Exec("UPDATE af_meta_definitions SET status='active' WHERE tenant_id=? AND id=?", f.tenant, function.ID); err != nil {
		t.Fatal(err)
	}
	f.check(f.request("POST", "/metadata/admin/policy/publish", &f.admin, draft.ETag, nil), 200)
	f.expectDecision(worker, functionRequest, true)
	f.expectDecision(worker, c.AuthorizationRequest{Operation: "function.invoke", ResourceID: validDraft.ID, Kind: c.KindFunction}, true)
	f.expectDecision(worker, actionRequest, false)
	// Caller-supplied kind cannot turn a Function grant into Action execution.
	invalid := functionRequest
	invalid.Kind = c.KindAction
	if _, err := f.store.Authorize(context.Background(), c.Principal{TenantID: f.tenant, Subject: worker.ID}, invalid); err == nil {
		t.Fatal("wrong callable kind accepted")
	}
	// Publishing the prior policy revokes the call without an allow cache.
	current := f.policy()
	w = f.request("PUT", "/metadata/admin/policy", &f.admin, current.ETag, policy.Current)
	f.check(w, 200)
	draft = groupDecode[c.PolicyRecord](t, w)
	f.check(f.request("POST", "/metadata/admin/policy/publish", &f.admin, draft.ETag, nil), 200)
	f.expectDecision(worker, functionRequest, false)
	before := f.policy()
	if err := f.store.Bootstrap(context.Background(), f.tenant, "admin", groupTestPassword); err != nil {
		t.Fatal(err)
	}
	after := f.policy()
	if before.ETag != after.ETag || before.State != after.State || len(before.Current.Rules) != len(after.Current.Rules) {
		t.Fatal("bootstrap widened existing tenant permissions")
	}
}
