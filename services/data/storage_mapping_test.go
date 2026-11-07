package data

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestStorageMappingRequiresSpecificDefinitionPermission(t *testing.T) {
	path := "/data/schemas/" + testDefinition().ID + "/storage-mapping"
	for _, tc := range []struct {
		name     string
		decision c.Decision
		status   int
	}{
		{"denied even for administrator", c.Decision{Allow: false, State: "current"}, 403},
		{"conditional rows", c.Decision{Allow: true, State: "current", RowFilter: &c.Expression{Op: "eq", Field: testFieldCode, Value: raw("A")}}, 422},
		{"conditional new rows", c.Decision{Allow: true, State: "current", NewRowFilter: &c.Expression{Op: "eq", Field: testFieldCode, Value: raw("A")}}, 422},
		{"restricted fields", c.Decision{Allow: true, State: "current", ReadFields: []string{testFieldCode}}, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No Engine or Metadata means an authorization bypass cannot silently
			// pass by returning an empty mapping.
			h := NewHandler(Config{Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, request c.AuthorizationRequest) (c.Decision, error) {
				if request.Operation != "definition.edit" || request.ResourceID != testDefinition().ID || request.Kind != c.KindSchema {
					t.Fatalf("mapping authorization is not specific to the schema: %+v", request)
				}
				return tc.decision, nil
			})})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, sourceRequest("GET", path, true))
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("mapping response=%d want=%d: %s", w.Code, tc.status, w.Body)
			}
		})
	}
	w := httptest.NewRecorder()
	NewHandler(Config{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != 401 {
		t.Fatalf("anonymous mapping access accepted: %d", w.Code)
	}
}
