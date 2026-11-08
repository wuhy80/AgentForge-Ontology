package identity_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

// Explicit opt-in, isolated tenants, and explicit migrations. Credentials and
// database connection strings are never written to test output.
func TestMySQLExternalConnectionsAndOAuth(t *testing.T) {
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_TEST_DSN for the external authentication lifecycle")
	}
	ctx := context.Background()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 8, MaxIdle: 2})
	if err != nil {
		t.Fatal("open isolated test database")
	}
	defer engine.Close()
	migrations := append(append([]storage.Migration{}, metadata.Migrations...), identity.Migrations...)
	if err := storage.Migrate(ctx, engine.DB(), "metadata", migrations); err != nil {
		t.Fatal(err)
	}
	const origin, resource = "http://localhost:5173", "http://127.0.0.1:18084/mcp"
	store := identity.NewStore(engine.DB(), nil, origin)
	if err := store.ConfigureExternal("http://127.0.0.1:18084", resource); err != nil {
		t.Fatal(err)
	}
	app := platform.ProtectSessionRoutes(strings.Repeat("s", 32), true, store, store.NewHandler())
	tenant, otherTenant := "external-"+c.NewID(), "external-"+c.NewID()
	for _, id := range []string{tenant, otherTenant} {
		if err := store.Bootstrap(ctx, id, "admin", "ExternalTest!2026"); err != nil {
			t.Fatal(err)
		}
	}
	type browser struct {
		cookie  *http.Cookie
		session c.Session
	}
	request := func(method, path string, b *browser, body any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		contentType := "application/json"
		if form, ok := body.(url.Values); ok {
			raw, contentType = []byte(form.Encode()), "application/x-www-form-urlencoded"
		} else if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, origin+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", contentType)
		if b != nil {
			r.Header.Set("Origin", origin)
			r.AddCookie(b.cookie)
			r.Header.Set("X-CSRF-Token", b.session.CSRFToken)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int, out any) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("unexpected HTTP status %d, want %d", w.Code, status)
		}
		if out != nil && json.Unmarshal(w.Body.Bytes(), out) != nil {
			t.Fatal("invalid response JSON")
		}
	}
	login := func(id string) browser {
		t.Helper()
		// Login origin is required independently of any existing session.
		raw, _ := json.Marshal(c.LoginRequest{Tenant: id, Username: "admin", Password: "ExternalTest!2026"})
		r := httptest.NewRequest("POST", origin+"/metadata/auth/login", bytes.NewReader(raw))
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		var b browser
		expect(w, 200, &b.session)
		if len(w.Result().Cookies()) != 1 {
			t.Fatal("missing browser session")
		}
		b.cookie = w.Result().Cookies()[0]
		return b
	}
	admin, other := login(tenant), login(otherTenant)
	create := func(scopes ...string) c.AccessConnectionCreated {
		t.Helper()
		var out c.AccessConnectionCreated
		expect(request("POST", "/metadata/auth/connections", &admin, c.AccessConnectionInput{Name: "test", Scopes: scopes, ExpiresInSeconds: 3600}), 201, &out)
		if !platform.ValidAccessToken(out.AccessToken) {
			t.Fatal("invalid access credential format")
		}
		return out
	}
	checkToken := func(token, method, path, audience string, want int) {
		t.Helper()
		session, err := store.CheckSession(ctx, c.SessionCheck{AccessToken: token, Method: method, Path: path, Resource: audience})
		if want == 200 {
			if err != nil || session.User.ID != admin.session.User.ID || session.User.TenantID != tenant {
				t.Fatal("external identity was not preserved")
			}
			return
		}
		failure, ok := err.(*platform.AccessError)
		if !ok || failure.Status != want {
			t.Fatalf("expected access status %d", want)
		}
	}
	discover := create(platform.ScopeDiscover)
	checkToken(discover.AccessToken, "GET", "/data/query/context", "", 200)
	checkToken(discover.AccessToken, "POST", "/data/query", "", 403)
	checkToken(discover.AccessToken, "GET", "/metadata/definitions/secret", "", 401)
	checkToken(discover.AccessToken, "POST", "/mcp", resource+"-wrong", 401)
	listed := request("GET", "/metadata/auth/connections", &admin, nil)
	expect(listed, 200, nil)
	if strings.Contains(listed.Body.String(), discover.AccessToken) || strings.Contains(listed.Body.String(), "token_hash") {
		t.Fatal("connection list disclosed a credential")
	}
	expect(request("DELETE", "/metadata/auth/connections/"+discover.Connection.ID, &other, nil), 404, nil)
	checkToken(discover.AccessToken, "POST", "/mcp", resource, 200)
	expect(request("DELETE", "/metadata/auth/connections/"+discover.Connection.ID, &admin, nil), 204, nil)
	checkToken(discover.AccessToken, "POST", "/mcp", resource, 401)

	const callback = "http://127.0.0.1:49173/callback"
	var client struct {
		ID string `json:"client_id"`
	}
	expect(request("POST", "/oauth/register", nil, map[string]any{"client_name": "integration", "redirect_uris": []string{callback}, "token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code"}, "response_types": []string{"code"}}), 201, &client)
	expect(request("GET", "/.well-known/oauth-authorization-server", nil, nil), 200, nil)
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	input := c.OAuthAuthorizationInput{ClientID: client.ID, RedirectURI: callback, ResponseType: "code", CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:]), CodeChallengeMethod: "S256", Scope: platform.ScopeDiscover + " " + platform.ScopeQuery, State: "client-state", Resource: resource, Approve: true}
	q := url.Values{"client_id": {client.ID}, "redirect_uri": {callback}, "response_type": {"code"}, "code_challenge": {input.CodeChallenge}, "code_challenge_method": {"S256"}, "scope": {input.Scope}}
	authorize := request("GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	expect(authorize, 302, nil)
	if !strings.Contains(authorize.Header().Get("Location"), url.QueryEscape(resource)) {
		t.Fatal("default OAuth resource missing in consent redirect")
	}
	expect(request("POST", "/metadata/auth/oauth/preview", nil, input), 401, nil)
	expect(request("POST", "/metadata/auth/oauth/preview", &admin, input), 200, nil)
	issueCode := func(in c.OAuthAuthorizationInput) string {
		t.Helper()
		var out struct {
			RedirectURI string `json:"redirectUri"`
		}
		expect(request("POST", "/metadata/auth/oauth/authorize", &admin, in), 200, &out)
		u, err := url.Parse(out.RedirectURI)
		if err != nil || u.Query().Get("state") != input.State {
			t.Fatal("OAuth state was not preserved")
		}
		return u.Query().Get("code")
	}
	denied := input
	denied.Approve = false
	if issueCode(denied) != "" {
		t.Fatal("denied consent issued a code")
	}
	wrongRedirect := input
	wrongRedirect.RedirectURI = callback + "/wrong"
	expect(request("POST", "/metadata/auth/oauth/authorize", &admin, wrongRedirect), 400, nil)
	code := issueCode(input)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {client.ID}, "redirect_uri": {callback}, "resource": {resource}, "code_verifier": {verifier}, "code": {code}}
	form.Set("code_verifier", strings.Repeat("x", 64))
	expect(request("POST", "/oauth/token", nil, form), 400, nil)
	form.Set("code_verifier", verifier)
	form.Set("resource", resource+"-wrong")
	expect(request("POST", "/oauth/token", nil, form), 400, nil)
	form.Set("resource", resource)
	var token struct {
		Value string `json:"access_token"`
		Scope string `json:"scope"`
	}
	expect(request("POST", "/oauth/token", nil, form), 200, &token)
	if token.Scope != input.Scope {
		t.Fatal("OAuth scope is not space-delimited")
	}
	checkToken(token.Value, "POST", "/data/query", "", 200)
	expect(request("POST", "/oauth/token", nil, form), 400, nil)
	expect(request("POST", "/oauth/revoke", nil, url.Values{"token": {token.Value}}), 200, nil)
	checkToken(token.Value, "POST", "/mcp", resource, 401)
	form.Set("code", issueCode(input))
	hash := sha256.Sum256([]byte(form.Get("code")))
	if _, err := engine.DB().Exec("UPDATE af_identity_oauth_codes SET expires_at=1 WHERE code_hash=?", hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	expect(request("POST", "/oauth/token", nil, form), 400, nil)
	// Role hash changes are detected even when the user ETag is unchanged.
	roleToken := create(platform.ScopeDiscover)
	if _, err := engine.DB().Exec("UPDATE af_identity_users SET role_ids='[]' WHERE tenant_id=? AND id=?", tenant, admin.session.User.ID); err != nil {
		t.Fatal(err)
	}
	checkToken(roleToken.AccessToken, "POST", "/mcp", resource, 401)
	admin = login(tenant)
	changed := create(platform.ScopeDiscover)
	if _, err := engine.DB().Exec("UPDATE af_identity_users SET enabled=0 WHERE tenant_id=? AND id=?", tenant, admin.session.User.ID); err != nil {
		t.Fatal(err)
	}
	checkToken(changed.AccessToken, "POST", "/mcp", resource, 401)
}
