package identity_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

// This opt-in integration uses isolated tenants and explicit migrations, never
// deletes existing users or policies, and never prints credentials or a DSN.
func TestMySQLIdentityLifecycle(t *testing.T) {
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_TEST_DSN for the isolated MySQL identity contract")
	}
	ctx := context.Background()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 8, MaxIdle: 2})
	if err != nil {
		t.Fatal("open test database")
	}
	defer engine.Close()
	migrations := append(append([]storage.Migration{}, metadata.Migrations...), identity.Migrations...)
	if err := storage.Migrate(ctx, engine.DB(), "metadata", migrations); err != nil {
		t.Fatal(err)
	}
	var opa *platform.OPAAuthorizer
	if endpoint := os.Getenv("IDENTITY_TEST_OPA_URL"); endpoint != "" {
		opa = &platform.OPAAuthorizer{URL: endpoint}
	} else {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Input struct {
					Runtime struct {
						Grant contracts.Decision `json:"grant"`
					} `json:"runtime"`
				} `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Error("invalid runtime request")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": request.Input.Runtime.Grant})
		}))
		defer server.Close()
		opa = &platform.OPAAuthorizer{URL: server.URL}
	}
	store := identity.NewStore(engine.DB(), opa, "http://localhost:5173")
	api := store.NewHandler()
	tenant, otherTenant := "identity-"+contracts.NewID(), "identity-"+contracts.NewID()
	const originalPassword = "123456"
	if err := store.Bootstrap(ctx, tenant, "admin", originalPassword); err != nil {
		t.Fatal(err)
	}
	if err := store.Bootstrap(ctx, tenant, "different-admin", "DoNotReplace!2026"); err != nil {
		t.Fatal(err)
	}
	if err := store.Bootstrap(ctx, tenant, "admin", "DoNotReplace!2026"); err != nil {
		t.Fatal(err)
	}
	if err := store.Bootstrap(ctx, otherTenant, "admin", originalPassword); err != nil {
		t.Fatal(err)
	}
	type browser struct {
		cookie  *http.Cookie
		session contracts.Session
	}
	request := func(method, path string, b *browser, etag string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Origin", "http://localhost:5173")
		r.RemoteAddr = "127.0.0.1:32000"
		if b != nil {
			r.AddCookie(b.cookie)
			r.Header.Set("X-CSRF-Token", b.session.CSRFToken)
		}
		if etag != "" {
			r.Header.Set("If-Match", `"`+etag+`"`)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("HTTP %d, want %d: %s", w.Code, status, w.Body.String())
		}
	}
	decode := func(w *httptest.ResponseRecorder, target any) {
		t.Helper()
		if json.Unmarshal(w.Body.Bytes(), target) != nil {
			t.Fatal("invalid JSON response")
		}
	}
	login := func(tenant, username, password string) browser {
		t.Helper()
		w := request("POST", "/metadata/auth/login", nil, "", contracts.LoginRequest{Tenant: tenant, Username: username, Password: password})
		check(w, 200)
		var b browser
		decode(w, &b.session)
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatal("missing session cookie")
		}
		b.cookie = cookies[0]
		if !b.cookie.HttpOnly || b.cookie.SameSite != http.SameSiteStrictMode || b.cookie.Path != "/" || b.cookie.Secure {
			t.Fatal("invalid local session cookie attributes")
		}
		return b
	}
	admin := login(tenant, "ADMIN", originalPassword)
	other := login(otherTenant, "admin", originalPassword)
	check(request("POST", "/metadata/auth/login", nil, "", contracts.LoginRequest{Tenant: tenant, Username: "different-admin", Password: "DoNotReplace!2026"}), 401)
	check(request("POST", "/metadata/auth/login", nil, "", contracts.LoginRequest{Tenant: tenant, Username: "admin", Password: "DoNotReplace!2026"}), 401)
	check(request("GET", "/metadata/auth/session", &admin, "", nil), 200)
	if _, err := store.CheckSession(ctx, contracts.SessionCheck{Token: admin.cookie.Value, Method: "POST", CSRFToken: "bad"}); err == nil {
		t.Fatal("missing CSRF accepted")
	}
	if _, err := store.CheckSession(ctx, contracts.SessionCheck{Token: admin.cookie.Value, Method: "POST", CSRFToken: admin.session.CSRFToken, Origin: "http://attacker.test"}); err == nil {
		t.Fatal("foreign Origin accepted")
	}
	if _, err := store.CheckSession(ctx, contracts.SessionCheck{Token: admin.cookie.Value, Method: "POST", CSRFToken: admin.session.CSRFToken}); err != nil {
		t.Fatal("trusted CLI check requires CSRF but can omit browser Origin")
	}
	previewCheck := contracts.SessionCheck{Token: admin.cookie.Value, Method: "POST", CSRFToken: admin.session.CSRFToken, Origin: "http://localhost:4173"}
	if _, err := store.CheckSession(ctx, previewCheck); err == nil {
		t.Fatal("unconfigured preview origin accepted")
	}
	if err := store.ConfigureAdditionalOrigins(previewCheck.Origin); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckSession(ctx, previewCheck); err != nil {
		t.Fatal("configured preview origin rejected the existing browser session")
	}
	previewCheck.CSRFToken = strings.Repeat("0", 64)
	if _, err := store.CheckSession(ctx, previewCheck); err == nil {
		t.Fatal("preview origin bypassed CSRF")
	}
	previewCheck.CSRFToken, previewCheck.Origin = admin.session.CSRFToken, "http://attacker.test"
	if _, err := store.CheckSession(ctx, previewCheck); err == nil {
		t.Fatal("preview alias allowed an unrelated origin")
	}
	sum := sha256.Sum256([]byte(admin.cookie.Value))
	var storedHash string
	if err := engine.DB().QueryRow("SELECT token_hash FROM af_identity_sessions WHERE tenant_id=? AND user_id=?", tenant, admin.session.User.ID).Scan(&storedHash); err != nil || storedHash != hex.EncodeToString(sum[:]) || storedHash == admin.cookie.Value {
		t.Fatal("session is not stored as an opaque digest")
	}
	// A tenant administrator with no roles can manage identities but receives
	// no implicit business grant from that administrator flag.
	create := contracts.UserInput{Username: "manager", DisplayName: "权限管理员", Password: "12345", Enabled: true, Administrator: true, RoleIDs: []string{}}
	check(request("POST", "/metadata/admin/users", &admin, "", create), 422)
	create.Password = "654321"
	w := request("POST", "/metadata/admin/users", &admin, "", create)
	check(w, 201)
	var manager contracts.User
	decode(w, &manager)
	managerBrowser := login(tenant, create.Username, create.Password)
	decision, err := store.Authorize(ctx, contracts.Principal{TenantID: tenant, Subject: manager.ID}, contracts.AuthorizationRequest{Operation: "instance.read", ResourceID: "anything"})
	if err != nil || decision.Allow {
		t.Fatal("administrator flag granted business data")
	}
	check(request("GET", "/metadata/admin/users/"+admin.session.User.ID, &other, "", nil), 404)
	check(request("PUT", "/metadata/admin/users/"+admin.session.User.ID, &admin, admin.session.User.ETag, contracts.UserInput{Username: "admin", DisplayName: "管理员", Enabled: false, Administrator: true, RoleIDs: admin.session.User.RoleIDs}), 422)
	w = request("POST", "/metadata/admin/roles", &admin, "", contracts.RoleInput{Name: "维修员", Description: "有限业务访问"})
	check(w, 201)
	var role contracts.Role
	decode(w, &role)
	check(request("PUT", "/metadata/admin/roles/"+role.ID, &admin, "stale", contracts.RoleInput{Name: "维修员新名称"}), 412)
	check(request("PUT", "/metadata/admin/roles/"+role.ID, &admin, role.ETag, contracts.RoleInput{Name: "设备维修员"}), 200)
	create = contracts.UserInput{Username: "worker", DisplayName: "设备维修员", Password: "WorkerIdentity!2026", Enabled: true, RoleIDs: []string{role.ID}}
	w = request("POST", "/metadata/admin/users", &admin, "", create)
	check(w, 201)
	var worker contracts.User
	decode(w, &worker)
	workerBrowser := login(tenant, create.Username, create.Password)
	check(request("GET", "/metadata/admin/users", &workerBrowser, "", nil), 403)
	check(request("POST", "/metadata/admin/users", &other, "", create), 422)
	// Stable schema and property references are validated at draft save and
	// again when a policy is made current.
	schemaID, fieldID := contracts.NewID(), contracts.NewID()
	definition := contracts.Definition{ID: schemaID, TenantID: tenant, CategoryID: contracts.NewID(), GroupID: contracts.NewID(), Kind: contracts.KindSchema, Name: "设备", Schema: &contracts.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []contracts.Property{{ID: fieldID, Name: "设备数量", DataType: "int64"}}}}
	raw, _ := json.Marshal(definition)
	if _, err := engine.DB().Exec("INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,current_json,current_digest,status) VALUES (?,?,?,?,?,?,?,?,?,'active')", schemaID, tenant, definition.CategoryID, definition.GroupID, "schema", "设备", []byte("设备"), string(raw), strings.Repeat("a", 64)); err != nil {
		t.Fatal("create isolated schema fixture")
	}
	w = request("GET", "/metadata/admin/policy", &admin, "", nil)
	check(w, 200)
	var policy contracts.PolicyRecord
	decode(w, &policy)
	candidate := policy.Current
	candidate.Rules = append(append([]contracts.PermissionRule{}, candidate.Rules...), contracts.PermissionRule{ID: contracts.NewID(), RoleID: role.ID, Effect: "allow", Operation: "instance.read", ResourceID: schemaID, ReadFields: []string{fieldID}, WriteFields: []string{}, RowFilter: &contracts.Expression{Op: "gt", Field: fieldID, Value: json.RawMessage(`"0"`)}})
	invalid := candidate
	invalid.Rules = append([]contracts.PermissionRule{}, candidate.Rules...)
	invalid.Rules[len(invalid.Rules)-1].ReadFields = []string{"设备数量"}
	check(request("PUT", "/metadata/admin/policy", &admin, policy.ETag, invalid), 422)
	invalid.Rules[len(invalid.Rules)-1].ReadFields = []string{fieldID}
	invalid.Rules[len(invalid.Rules)-1].RowFilter = &contracts.Expression{Op: "gt", Field: fieldID, Value: json.RawMessage(`1`)}
	check(request("PUT", "/metadata/admin/policy", &admin, policy.ETag, invalid), 422)
	w = request("PUT", "/metadata/admin/policy", &admin, policy.ETag, candidate)
	check(w, 200)
	var draft contracts.PolicyRecord
	decode(w, &draft)
	if draft.Draft == nil || draft.State != policy.State || len(draft.Current.Rules) != len(policy.Current.Rules) {
		t.Fatal("draft prematurely changed current policy")
	}
	principal := contracts.Principal{TenantID: tenant, Subject: worker.ID}
	query := contracts.AuthorizationRequest{Operation: "instance.read", ResourceID: schemaID}
	decision, err = store.Authorize(ctx, principal, query)
	if err != nil || decision.Allow {
		t.Fatal("draft authorized current access")
	}
	w = request("POST", "/metadata/admin/simulate", &admin, "", contracts.SimulationRequest{UserID: worker.ID, Request: query, Draft: true})
	check(w, 200)
	var simulation contracts.SimulationResult
	decode(w, &simulation)
	if !simulation.Decision.Allow {
		t.Fatal("draft simulation did not use candidate roles")
	}
	check(request("POST", "/metadata/admin/policy/publish", &admin, policy.ETag, nil), 412)
	w = request("POST", "/metadata/admin/policy/publish", &admin, draft.ETag, nil)
	check(w, 200)
	var published contracts.PolicyRecord
	decode(w, &published)
	if published.Draft != nil || published.State == policy.State {
		t.Fatal("publication did not replace current authorization state")
	}
	decision, err = store.Authorize(ctx, principal, query)
	if err != nil || !decision.Allow || len(decision.ReadFields) != 1 || decision.RowFilter == nil {
		t.Fatalf("current authorization not enforced: allow=%v err=%v", decision.Allow, err)
	}
	// A role change revokes every existing session; disabling is immediate.
	create.RoleIDs = []string{}
	w = request("PUT", "/metadata/admin/users/"+worker.ID, &admin, worker.ETag, create)
	check(w, 200)
	decode(w, &worker)
	check(request("GET", "/metadata/auth/session", &workerBrowser, "", nil), 401)
	decision, err = store.Authorize(ctx, principal, query)
	if err != nil || decision.Allow {
		t.Fatal("old role membership continued to authorize")
	}
	workerBrowser = login(tenant, create.Username, create.Password)
	create.Enabled = false
	w = request("PUT", "/metadata/admin/users/"+worker.ID, &admin, worker.ETag, create)
	check(w, 200)
	decode(w, &worker)
	check(request("GET", "/metadata/auth/session", &workerBrowser, "", nil), 401)
	if _, err := store.Authorize(ctx, principal, query); err == nil {
		t.Fatal("disabled principal authorized")
	}
	// Self-service password change keeps only the newly rotated session.
	secondManager := login(tenant, "manager", "654321")
	check(request("PUT", "/metadata/auth/password", &managerBrowser, "", map[string]string{"currentPassword": "654321", "password": "12345"}), 422)
	check(request("GET", "/metadata/auth/session", &managerBrowser, "", nil), 200)
	w = request("PUT", "/metadata/auth/password", &managerBrowser, "", map[string]string{"currentPassword": "654321", "password": "234567"})
	check(w, 200)
	var changed contracts.Session
	decode(w, &changed)
	if changed.CSRFToken == managerBrowser.session.CSRFToken {
		t.Fatal("password change reused CSRF")
	}
	check(request("GET", "/metadata/auth/session", &secondManager, "", nil), 401)
	check(request("GET", "/metadata/auth/session", &managerBrowser, "", nil), 401)
	managerBrowser = browser{cookie: w.Result().Cookies()[0], session: changed}
	check(request("GET", "/metadata/auth/session", &managerBrowser, "", nil), 200)
	check(request("POST", "/metadata/auth/logout", &managerBrowser, "", nil), 204)
	check(request("GET", "/metadata/auth/session", &managerBrowser, "", nil), 401)
	// Admin password reset revokes every old session without disclosing the hash.
	managerBrowser = login(tenant, "manager", "234567")
	w = request("GET", "/metadata/admin/users/"+manager.ID, &admin, "", nil)
	check(w, 200)
	decode(w, &manager)
	reset := contracts.UserInput{Username: "manager", DisplayName: "权限管理员", Password: "12345", Enabled: true, Administrator: true, RoleIDs: []string{}}
	check(request("PUT", "/metadata/admin/users/"+manager.ID, &admin, manager.ETag, reset), 422)
	check(request("GET", "/metadata/auth/session", &managerBrowser, "", nil), 200)
	reset.Password = "345678"
	w = request("PUT", "/metadata/admin/users/"+manager.ID, &admin, manager.ETag, reset)
	check(w, 200)
	check(request("GET", "/metadata/auth/session", &managerBrowser, "", nil), 401)
	_ = login(tenant, "manager", "345678")
	w = request("GET", "/metadata/admin/audit", &admin, "", nil)
	check(w, 200)
	if !strings.Contains(w.Body.String(), "policy.publish") || !strings.Contains(w.Body.String(), "auth.password") || strings.Contains(w.Body.String(), originalPassword) || strings.Contains(w.Body.String(), admin.cookie.Value) || strings.Contains(w.Body.String(), "password_hash") {
		t.Fatal("audit missing required events or contains credentials")
	}
	// Audit is an explicitly recent view and stays available past its page size.
	for i := 0; i < 205; i++ {
		if _, err := engine.DB().Exec("INSERT INTO af_identity_audit (id,tenant_id,actor_id,operation,target_id) VALUES (?,?,?,'test.fixture','fixture')", contracts.NewID(), tenant, admin.session.User.ID); err != nil {
			t.Fatal("insert owned audit fixture")
		}
	}
	w = request("GET", "/metadata/admin/audit", &admin, "", nil)
	check(w, 200)
	var recent struct {
		Data    []contracts.AuditRecord `json:"data"`
		HasMore bool                    `json:"hasMore"`
	}
	decode(w, &recent)
	if len(recent.Data) != 200 || !recent.HasMore {
		t.Fatal("recent audit failed to expose bounded history")
	}
	for attempt := 0; attempt < 5; attempt++ {
		check(request("POST", "/metadata/auth/login", nil, "", contracts.LoginRequest{Tenant: tenant, Username: "missing-account", Password: "WrongPassword!2026"}), 401)
	}
	check(request("POST", "/metadata/auth/login", nil, "", contracts.LoginRequest{Tenant: tenant, Username: "missing-account", Password: "WrongPassword!2026"}), 429)
	t.Run("concurrent administrator removal retains one effective administrator", func(t *testing.T) {
		adminTenant := "identity-admin-" + contracts.NewID()
		if err := store.Bootstrap(ctx, adminTenant, "first", originalPassword); err != nil {
			t.Fatal(err)
		}
		first := login(adminTenant, "first", originalPassword)
		secondInput := contracts.UserInput{Username: "second", DisplayName: "第二管理员", Password: originalPassword, Enabled: true, Administrator: true, RoleIDs: []string{}}
		w := request("POST", "/metadata/admin/users", &first, "", secondInput)
		check(w, 201)
		var secondUser contracts.User
		decode(w, &secondUser)
		second := login(adminTenant, "second", originalPassword)
		firstInput := contracts.UserInput{Username: "first", DisplayName: "首个管理员", Enabled: false, Administrator: true, RoleIDs: first.session.User.RoleIDs}
		secondInput.Password = ""
		secondInput.Enabled = false
		results := make(chan *httptest.ResponseRecorder, 2)
		start := make(chan struct{})
		var ready sync.WaitGroup
		ready.Add(2)
		go func() {
			ready.Done()
			<-start
			results <- request("PUT", "/metadata/admin/users/"+secondUser.ID, &first, secondUser.ETag, secondInput)
		}()
		go func() {
			ready.Done()
			<-start
			results <- request("PUT", "/metadata/admin/users/"+first.session.User.ID, &second, first.session.User.ETag, firstInput)
		}()
		ready.Wait()
		close(start)
		codes := map[int]int{}
		for i := 0; i < 2; i++ {
			response := <-results
			codes[response.Code]++
		}
		if codes[200] != 1 || codes[401] != 1 {
			t.Fatalf("concurrent removals should serialize, got %v", codes)
		}
		var remaining int
		if err := engine.DB().QueryRow("SELECT COUNT(*) FROM af_identity_users WHERE tenant_id=? AND enabled=true AND administrator=true", adminTenant).Scan(&remaining); err != nil || remaining != 1 {
			t.Fatal("concurrent removal lost the final administrator")
		}
	})
}
