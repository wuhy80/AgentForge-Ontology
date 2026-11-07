package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestBusinessResponsePreservesAuthorityErrors(t *testing.T) {
	definition := testDefinition()
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(c.CurrentDefinition{Definition: definition, ETag: "current", Status: "active"})
	}))
	defer metadata.Close()
	queryBody, err := json.Marshal(baseQuery())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unsupported combination", &platform.AccessError{Status: 422, Code: "unsupported_policy_combination", Message: "权限组合无法安全执行"}, 422, "unsupported_policy_combination"},
		{"disabled identity", &platform.AccessError{Status: 401, Code: "unauthenticated", Message: "账户已停用"}, 401, "unauthenticated"},
		{"forbidden", &platform.AccessError{Status: 403, Code: "forbidden", Message: "不允许操作"}, 403, "forbidden"},
		{"authority unavailable", &platform.AccessError{Status: 503, Code: "identity_unavailable", Message: "权限服务不可用"}, 503, "identity_unavailable"},
		{"unknown failure", errors.New("private-connection-detail"), 503, "authorization_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, route := range []struct{ method, path, body, operation string }{
				{"GET", "/data/schemas/" + definition.ID + "/instances/00000000-0000-0000-0000-000000000001", "", "instance.read"},
				{"POST", "/data/query", string(queryBody), "instance.query"},
			} {
				t.Run(route.operation, func(t *testing.T) {
					calls := 0
					app := NewHandler(Config{MetadataURL: metadata.URL, Authorizer: authorizerFunc(func(_ context.Context, p c.Principal, request c.AuthorizationRequest) (c.Decision, error) {
						calls++
						if p.Subject != "user" || request.Operation != route.operation || request.ResourceID != definition.ID {
							t.Error("incorrect authorization context")
						}
						return c.Decision{Allow: true, State: "old-allow"}, fmt.Errorf("authority failure: %w", test.err)
					})})
					r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
					r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{Subject: "user", TenantID: definition.TenantID}))
					w := httptest.NewRecorder()
					app.ServeHTTP(w, r)
					var response c.Error
					if json.Unmarshal(w.Body.Bytes(), &response) != nil || w.Code != test.status || response.Code != test.code || calls != 1 {
						t.Fatalf("status=%d body=%s calls=%d", w.Code, w.Body.String(), calls)
					}
					if strings.Contains(w.Body.String(), "private-connection-detail") {
						t.Fatal("private authority failure leaked")
					}
					if access, ok := test.err.(*platform.AccessError); ok && response.Message != access.Message {
						t.Fatalf("public authority message lost: %s", response.Message)
					}
				})
			}
		})
	}
}
