package platform

import (
	"net/http"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestUpsertPoliciesKeepOldAndNewRoleConditionsCoupled(t *testing.T) {
	for _, operation := range []string{"instance.upsert", "instance.upsert.overwrite"} {
		t.Run(operation, func(t *testing.T) {
			a, b := rule("a", operation), rule("b", operation)
			a.RowFilter, a.NewRowFilter = filter("department", "A"), filter("department", "B")
			b.RowFilter, b.NewRowFilter = filter("department", "C"), filter("department", "D")
			request := c.AuthorizationRequest{Operation: operation, ResourceID: "schema", Kind: c.KindSchema}
			if _, err := mergeRuntime(runtime(a, b), request); err == nil {
				t.Fatal("upsert lost the coupling between a role's old and new row constraints")
			}
			b.NewRowFilter = a.NewRowFilter
			decision, err := mergeRuntime(runtime(a, b), request)
			if err != nil || !decision.Allow || expressionKey(decision.NewRowFilter) != expressionKey(a.NewRowFilter) {
				t.Fatal("safe upsert policy merge failed")
			}
			other := "instance.upsert"
			if operation == other {
				other = "instance.upsert.overwrite"
			}
			request.Operation = other
			decision, err = mergeRuntime(runtime(a, b), request)
			if err != nil || decision.Allow {
				t.Fatal("one upsert operation granted the other implicitly")
			}
		})
	}
}

func TestBulkPermissionsAndMCPCredentialsDoNotInventWriteAuthority(t *testing.T) {
	for _, operation := range []string{"instance.bulk_create", "instance.bulk_update", "instance.bulk_delete", "instance.bulk_upsert"} {
		if err := ValidatePermissionPolicy(c.PermissionPolicy{Rules: []c.PermissionRule{rule("a", operation)}}); err == nil {
			t.Fatalf("bulk operation %s introduced a second permission scheme", operation)
		}
	}
	for _, suffix := range []string{"", "/upsert", "/bulk-create", "/bulk-update", "/bulk-delete", "/bulk-upsert"} {
		if _, allowed := AccessRoute(http.MethodPost, "/data/schemas/schema/instances"+suffix); allowed {
			t.Fatal("existing external read/invoke scopes gained mutation access")
		}
	}
}

func TestWriteAuditOperationsUseFixedLabels(t *testing.T) {
	for suffix, want := range map[string]string{
		"/write-capabilities":    "write.capabilities",
		"/instances/upsert":      "instance.upsert",
		"/instances/bulk-create": "instance.bulk_create",
		"/instances/bulk-update": "instance.bulk_update",
		"/instances/bulk-delete": "instance.bulk_delete",
		"/instances/bulk-upsert": "instance.bulk_upsert",
	} {
		method := http.MethodPost
		if suffix == "/write-capabilities" {
			method = http.MethodGet
		}
		if got := auditOperation(method, "/data/schemas/SENSITIVE_ID"+suffix); got != want {
			t.Fatalf("write audit got %q want %q", got, want)
		}
	}
}
