package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestBusinessResponsePreservesAuthorityErrors(t *testing.T) {
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
			calls := 0
			h := &handler{cfg: Config{Authorizer: authorizerFunc(func(_ context.Context, p contracts.Principal, request contracts.AuthorizationRequest) (contracts.Decision, error) {
				calls++
				if p.Subject != "user" || request.Operation != "organization.edit" {
					t.Error("incorrect authorization context")
				}
				return contracts.Decision{Allow: true}, fmt.Errorf("authority failure: %w", test.err)
			})}}
			r := httptest.NewRequest("POST", "/metadata/categories", strings.NewReader(`{"name":"分类"}`))
			r = r.WithContext(platform.WithPrincipal(r.Context(), contracts.Principal{Subject: "user", TenantID: "tenant"}))
			w := httptest.NewRecorder()
			h.wrap(h.createCategory)(w, r)
			var response contracts.Error
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
}
