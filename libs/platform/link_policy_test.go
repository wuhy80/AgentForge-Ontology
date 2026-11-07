package platform

import (
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestLinkPoliciesRequireSeparateStaticRelationGrants(t *testing.T) {
	for _, operation := range []string{"link.query", "link.create", "link.delete"} {
		r := rule("relation", operation)
		r.ReadFields, r.WriteFields = []string{"*"}, nil
		policy := c.PermissionPolicy{Rules: []c.PermissionRule{r}}
		if err := ValidatePermissionPolicy(policy); err != nil {
			t.Fatal(operation, err)
		}
		request := c.AuthorizationRequest{Operation: operation, ResourceID: "schema", Kind: c.KindLink}
		decision, err := mergeRuntime(runtime(r), request)
		if err != nil || !decision.Allow {
			t.Fatalf("explicit relation permission not usable: %s err=%v", operation, err)
		}
		other := r
		other.ID, other.RoleID, other.ReadFields = "other-role", "other-role", nil
		decision, err = mergeRuntime(runtime(r, other), request)
		if err != nil || !decision.Allow {
			t.Fatal("equivalent whole-relation grants conflicted across roles", err)
		}
		request.Kind = c.KindSchema
		if _, err := mergeRuntime(runtime(r), request); err == nil {
			t.Fatal("relation operation accepted an explicit non-Link resource kind")
		}
		request.Kind = c.KindLink
		request.Operation = "instance.query"
		decision, err = mergeRuntime(runtime(r), request)
		if err != nil || decision.Allow {
			t.Fatal("relation grant leaked to Schema query", err)
		}
		for _, mutate := range []func(*c.PermissionRule){
			func(r *c.PermissionRule) { r.ReadFields = []string{"sourceId"} },
			func(r *c.PermissionRule) { r.WriteFields = []string{"targetId"} },
			func(r *c.PermissionRule) { r.RowFilter = filter("sourceId", "hidden") },
			func(r *c.PermissionRule) { r.NewRowFilter = filter("targetId", "hidden") },
		} {
			bad := r
			mutate(&bad)
			if err := ValidatePermissionPolicy(c.PermissionPolicy{Rules: []c.PermissionRule{bad}}); err == nil {
				t.Fatal("unsupported relation field/row restriction accepted")
			}
		}
		denied := r
		denied.ID, denied.Effect, denied.ReadFields = "deny", "deny", nil
		decision, err = mergeRuntime(runtime(r, denied), c.AuthorizationRequest{Operation: operation, ResourceID: "schema", Kind: c.KindLink})
		if err != nil || decision.Allow {
			t.Fatal("explicit relation deny did not dominate", err)
		}
	}
	if policyOperation("link.patch") || policyOperation("link.traverse") {
		t.Fatal("unsupported relation operations advertised")
	}
}
