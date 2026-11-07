package platform

import (
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestCallablePoliciesRequireSeparateWholeResourceGrants(t *testing.T) {
	for operation, kind := range map[string]c.Kind{"function.invoke": c.KindFunction, "action.invoke": c.KindAction} {
		t.Run(operation, func(t *testing.T) {
			grant := rule("call", operation)
			grant.ReadFields, grant.WriteFields = nil, nil
			request := c.AuthorizationRequest{Operation: operation, ResourceID: "schema", Kind: kind}
			other := grant
			other.ID, other.RoleID, other.ReadFields = "other", "other", []string{"*"}
			decision, err := mergeRuntime(runtime(grant, other), request)
			if err != nil || !decision.Allow {
				t.Fatal("equivalent whole-resource grants failed", err)
			}
			wildcard := grant
			wildcard.ResourceID = "*"
			decision, err = mergeRuntime(runtime(wildcard), request)
			if err != nil || !decision.Allow {
				t.Fatal("tenant-wide callable permission failed", err)
			}
			for _, invalidKind := range []c.Kind{"", c.KindSchema, c.KindLink} {
				invalid := request
				invalid.Kind = invalidKind
				if _, err := mergeRuntime(runtime(grant), invalid); err == nil {
					t.Fatal("invocation accepted missing/wrong resource kind")
				}
			}
			for _, id := range []string{"", "*", " padded"} {
				invalid := request
				invalid.ResourceID = id
				if _, err := mergeRuntime(runtime(grant), invalid); err == nil {
					t.Fatal("invocation accepted a non-concrete resource")
				}
			}
			for _, mutate := range []func(*c.PermissionRule){
				func(r *c.PermissionRule) { r.ReadFields = []string{"output"} },
				func(r *c.PermissionRule) { r.WriteFields = []string{"input"} },
				func(r *c.PermissionRule) { r.RowFilter = filter("input", "restricted") },
				func(r *c.PermissionRule) { r.NewRowFilter = filter("output", "restricted") },
			} {
				bad := grant
				mutate(&bad)
				if ValidatePermissionPolicy(c.PermissionPolicy{Rules: []c.PermissionRule{bad}}) == nil {
					t.Fatal("partial invocation permission silently accepted")
				}
			}
			deny := grant
			deny.ID, deny.Effect = "deny", "deny"
			decision, err = mergeRuntime(runtime(grant, deny), request)
			if err != nil || decision.Allow {
				t.Fatal("explicit deny did not dominate", err)
			}
			unrelated := grant
			unrelated.Operation = "definition.read"
			decision, err = mergeRuntime(runtime(unrelated), request)
			if err != nil || decision.Allow {
				t.Fatal("definition visibility granted invocation", err)
			}
			if operation == "function.invoke" {
				unrelated.Operation = "action.invoke"
				decision, err = mergeRuntime(runtime(unrelated), request)
				if err != nil || decision.Allow {
					t.Fatal("Action grant leaked direct Function permission", err)
				}
			}
		})
	}
}
