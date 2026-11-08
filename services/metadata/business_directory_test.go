package metadata

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

type businessSessionCheckerFunc func(context.Context, c.SessionCheck) (c.Session, error)

func (f businessSessionCheckerFunc) CheckSession(ctx context.Context, check c.SessionCheck) (c.Session, error) {
	return f(ctx, check)
}

func TestBusinessDirectoryRejectsUntrustedAndUnsupportedRequests(t *testing.T) {
	secret := strings.Repeat("s", 32)
	h := &handler{cfg: Config{ServiceToken: secret}}
	valid := c.BusinessDirectoryRequest{Check: c.SessionCheck{Token: strings.Repeat("c", 64), Method: "GET", Path: "/data/query/context"}, Limit: 20}
	for _, test := range []struct {
		name    string
		service string
		mutate  func(*c.BusinessDirectoryRequest)
		status  int
	}{
		{"no service credential", "", nil, 401},
		{"wrong service credential", "wrong", nil, 401},
		{"arbitrary source", secret, func(in *c.BusinessDirectoryRequest) { in.Check.Path = "/metadata/admin/users" }, 400},
		{"ambiguous credentials", secret, func(in *c.BusinessDirectoryRequest) { in.Check.AccessToken = "mcp_" + strings.Repeat("x", 32) }, 400},
		{"oversized limit", secret, func(in *c.BusinessDirectoryRequest) { in.Limit = 21 }, 400},
		{"missing authority", secret, nil, 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			if test.mutate != nil {
				test.mutate(&input)
			}
			encoded, _ := json.Marshal(input)
			r := httptest.NewRequest("POST", "/internal/metadata/business-directory", strings.NewReader(string(encoded)))
			r.Header.Set("Authorization", "Bearer "+test.service)
			r.Header.Set("X-Tenant-ID", "attacker")
			w := httptest.NewRecorder()
			h.businessDirectoryHTTP(w, r)
			if w.Code != test.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
}

func TestMySQLBusinessDirectoryUsesCurrentBusinessIdentity(t *testing.T) {
	dsn := os.Getenv("METADATA_TEST_DSN")
	if dsn == "" {
		t.Skip("set METADATA_TEST_DSN for the isolated business discovery contract")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 1, MaxIdle: 1})
	if err != nil {
		t.Fatal("open test DB")
	}
	t.Cleanup(func() { _ = engine.Close() })
	if err = storage.Migrate(ctx, engine.DB(), "metadata", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant, other := "business-directory-"+c.NewID(), "business-directory-other-"+c.NewID()
	category, otherCategory := c.NewID(), c.NewID()
	for _, org := range []struct{ id, tenant string }{{category, tenant}, {otherCategory, other}} {
		_, err = engine.DB().ExecContext(ctx, "INSERT INTO af_meta_organizations (id,tenant_id,kind,parent_id,name,name_key) VALUES (?,?,'category','','Business',?)", org.id, org.tenant, []byte("business"))
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, id := range []string{tenant, other} {
			_, _ = engine.DB().Exec("DELETE FROM af_meta_definitions WHERE tenant_id=?", id)
			_, _ = engine.DB().Exec("DELETE FROM af_meta_organizations WHERE tenant_id=?", id)
		}
	})
	visible, hidden, foreign := c.NewID(), c.NewID(), c.NewID()
	for _, item := range []struct{ id, tenant, category string }{{visible, tenant, category}, {hidden, tenant, category}, {foreign, other, otherCategory}} {
		d := c.Definition{ID: item.id, TenantID: item.tenant, CategoryID: item.category, Kind: c.KindSchema, Name: item.id, Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{ID: c.NewID(), Name: "Public", DataType: "string", Nullable: true}}}}
		raw, _ := json.Marshal(d)
		_, err = engine.DB().ExecContext(ctx, "INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,current_json,current_digest,status) VALUES (?,?,?,'','schema',?,?,?,?,'active')", item.id, item.tenant, item.category, item.id, []byte(item.id), string(raw), strings.Repeat("d", 64))
		if err != nil {
			t.Fatal(err)
		}
	}
	revoked := false
	calls := 0
	checker := businessSessionCheckerFunc(func(_ context.Context, check c.SessionCheck) (c.Session, error) {
		calls++
		if check.Token != strings.Repeat("c", 64) || revoked && calls%2 == 0 {
			return c.Session{}, &platform.AccessError{Status: 401, Code: "unauthenticated", Message: "test session revoked"}
		}
		return c.Session{User: c.User{ID: "reader", TenantID: tenant, Enabled: true, ETag: "user-state"}, ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
	})
	authorizer := authorizerFunc(func(ctx context.Context, p c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		// A one-connection pool catches SQL cursors held across authorization.
		var one int
		if err := engine.DB().QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
			return c.Decision{}, err
		}
		if p.Subject != "reader" || p.TenantID != tenant || req.Operation != "instance.query" {
			t.Error("discovery substituted identity or requested whole-definition permission")
		}
		if req.ResourceID != visible {
			return c.Decision{}, nil
		}
		return c.Decision{Allow: true, State: "policy-state", ReadFields: []string{"*"}}, nil
	})
	app := NewHandler(Config{Engine: engine, Authorizer: authorizer, SessionChecker: checker, ServiceToken: strings.Repeat("s", 32)})
	request := func() *httptest.ResponseRecorder {
		input := c.BusinessDirectoryRequest{Check: c.SessionCheck{Token: strings.Repeat("c", 64), Method: "GET", Path: "/data/query/context"}, Limit: 20}
		encoded, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", "/internal/metadata/business-directory", strings.NewReader(string(encoded))).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
		r.Header.Set("X-Tenant-ID", other)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	w := request()
	var out c.BusinessDirectoryPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Entries) != 1 || out.Entries[0].Current.Definition.ID != visible || out.HasMore || out.Scanned != 2 {
		t.Fatalf("business directory status=%d", w.Code)
	}
	if strings.Contains(w.Body.String(), hidden) || strings.Contains(w.Body.String(), foreign) {
		t.Fatal("unauthorized model leaked")
	}
	revoked = true
	w = request()
	if w.Code != 401 {
		t.Fatalf("revocation before response accepted: %d", w.Code)
	}
}
