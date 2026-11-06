package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type authorizerFunc func(context.Context, contracts.Principal, contracts.AuthorizationRequest) (contracts.Decision, error)

func (f authorizerFunc) Authorize(ctx context.Context, p contracts.Principal, r contracts.AuthorizationRequest) (contracts.Decision, error) {
	return f(ctx, p, r)
}

func TestAuthorizationFailsClosedAndServicePrincipalNameCannotBypassPolicy(t *testing.T) {
	h := &handler{}
	p := contracts.Principal{Subject: "__metadata_service__", TenantID: "tenant"}
	if err := h.authorize(context.Background(), p, "definition.read", "id", contracts.KindSchema); err == nil {
		t.Fatal("a reserved-looking subject bypassed absent authorization")
	}
	h.cfg.Authorizer = authorizerFunc(func(context.Context, contracts.Principal, contracts.AuthorizationRequest) (contracts.Decision, error) {
		return contracts.Decision{Allow: true}, fmt.Errorf("OPA unavailable")
	})
	if err := h.authorize(context.Background(), p, "definition.read", "id", contracts.KindSchema); err == nil {
		t.Fatal("an authorizer failure was treated as allow")
	}
	h.cfg.Authorizer = authorizerFunc(func(context.Context, contracts.Principal, contracts.AuthorizationRequest) (contracts.Decision, error) {
		return contracts.Decision{Allow: true, RowFilter: &contracts.Expression{Op: "eq", Field: "owner", Value: json.RawMessage(`"self"`)}}, nil
	})
	if err := h.authorize(context.Background(), p, "definition.read", "id", contracts.KindSchema); err == nil {
		t.Fatal("an unsupported metadata condition was ignored")
	}
}

func TestInternalCredentialIsReadOnlyAndRequiresTenant(t *testing.T) {
	secret := strings.Repeat("s", 32)
	h := &handler{cfg: Config{ServiceToken: secret}}
	called := false
	endpoint := h.wrap(func(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
		called = true
		return h.authorize(r.Context(), p, "definition.read", "schema", contracts.KindSchema)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metadata/definitions/{id}", endpoint)
	mux.HandleFunc("GET /metadata/definitions/{id}/draft", endpoint)
	mux.HandleFunc("POST /metadata/definitions/{id}/publish", endpoint)
	for _, tc := range []struct {
		method, path, tenant string
		status               int
		reaches              bool
	}{
		{"GET", "/metadata/definitions/schema", "tenant", 200, true},
		{"GET", "/metadata/definitions/schema", "", 401, false},
		{"GET", "/metadata/definitions/schema/draft", "tenant", 403, false},
		{"POST", "/metadata/definitions/schema/publish", "tenant", 403, false},
	} {
		called = false
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("X-Tenant-ID", tc.tenant)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.status || called != tc.reaches {
			t.Errorf("%s %s: status %d, reached %v", tc.method, tc.path, w.Code, called)
		}
	}
}

func TestDataPrepareAcknowledgementMustBindOperation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"valid", `{"operationId":"operation-1","status":"prepared"}`, 200, false},
		{"wrong operation", `{"operationId":"other","status":"prepared"}`, 200, true},
		{"already activated replay", `{"operationId":"operation-1","status":"ready"}`, 200, false},
		{"not prepared", `{"operationId":"operation-1","status":"preparing"}`, 200, true},
		{"invalid JSON", `{`, 200, true},
		{"server failure", `{}`, 500, true},
		{"preflight reject", `{}`, 422, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Tenant-ID") != "tenant" || r.Header.Get("Authorization") != "Bearer internal" {
					t.Error("coordination identity missing")
				}
				var request contracts.PrepareRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.OperationID != "operation-1" {
					t.Error("wrong request operation")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			h := &handler{cfg: Config{DataURL: srv.URL, ServiceToken: "internal"}, client: srv.Client()}
			err := h.dataCall(context.Background(), "tenant", "/internal/publications/prepare", contracts.PrepareRequest{OperationID: "operation-1"})
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestPublicTenantHeaderDoesNotOverrideAuthenticatedTenant(t *testing.T) {
	h := &handler{}
	var got string
	ep := h.wrap(func(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
		got = p.TenantID
		return nil
	})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Tenant-ID", "other-tenant")
	r = r.WithContext(platform.WithPrincipal(r.Context(), contracts.Principal{Subject: "user", TenantID: "trusted-tenant"}))
	w := httptest.NewRecorder()
	ep(w, r)
	if got != "trusted-tenant" {
		t.Fatalf("tenant was overwritten: %q", got)
	}
}
