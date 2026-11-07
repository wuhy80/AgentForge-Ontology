package identity_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

const groupTestPassword = "UserGroupsFixture!2026"

type groupBrowser struct {
	cookie  *http.Cookie
	session contracts.Session
}

type groupFixture struct {
	t      *testing.T
	db     *sql.DB
	store  *identity.Store
	api    http.Handler
	tenant string
	admin  groupBrowser
}

func newGroupFixture(t *testing.T) *groupFixture {
	t.Helper()
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_TEST_DSN for isolated MySQL user-group contracts")
	}
	engine, err := storage.Open(context.Background(), storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 8, MaxIdle: 2})
	if err != nil {
		t.Fatal("open user-group test database")
	}
	t.Cleanup(func() { engine.Close() })
	migrations := append(append([]storage.Migration{}, metadata.Migrations...), identity.Migrations...)
	if err := storage.Migrate(context.Background(), engine.DB(), "metadata", migrations); err != nil {
		t.Fatal(err)
	}
	endpoint := os.Getenv("IDENTITY_TEST_OPA_URL")
	if endpoint == "" {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Input struct {
					Runtime struct {
						Grant contracts.Decision `json:"grant"`
					} `json:"runtime"`
				} `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error("invalid OPA runtime request")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": request.Input.Runtime.Grant})
		}))
		t.Cleanup(server.Close)
		endpoint = server.URL
	}
	f := &groupFixture{t: t, db: engine.DB(), tenant: "groups-" + contracts.NewID()}
	f.store = identity.NewStore(f.db, &platform.OPAAuthorizer{URL: endpoint}, "http://localhost:5173")
	f.api = f.store.NewHandler()
	if err := f.store.Bootstrap(context.Background(), f.tenant, "admin", groupTestPassword); err != nil {
		t.Fatal(err)
	}
	f.admin = f.login("admin")
	return f
}

func (f *groupFixture) request(method, path string, browser *groupBrowser, etag string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Origin", "http://localhost:5173")
	r.RemoteAddr = "127.0.0.1:32001"
	if browser != nil {
		r.AddCookie(browser.cookie)
		r.Header.Set("X-CSRF-Token", browser.session.CSRFToken)
	}
	if etag != "" {
		r.Header.Set("If-Match", `"`+etag+`"`)
	}
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	return w
}

func (f *groupFixture) check(w *httptest.ResponseRecorder, status int) {
	f.t.Helper()
	if w.Code != status {
		f.t.Fatalf("HTTP %d, want %d: %s", w.Code, status, w.Body.String())
	}
}

func groupDecode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal("invalid user-group response")
	}
	return result
}

func (f *groupFixture) login(username string) groupBrowser {
	f.t.Helper()
	w := f.request("POST", "/metadata/auth/login", nil, "", contracts.LoginRequest{Tenant: f.tenant, Username: username, Password: groupTestPassword})
	f.check(w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		f.t.Fatal("missing user-group test session")
	}
	return groupBrowser{cookie: cookies[0], session: groupDecode[contracts.Session](f.t, w)}
}

func (f *groupFixture) createGroup(input contracts.UserGroupInput) contracts.UserGroup {
	f.t.Helper()
	w := f.request("POST", "/metadata/admin/user-groups", &f.admin, "", input)
	f.check(w, 201)
	group := groupDecode[contracts.UserGroup](f.t, w)
	if w.Header().Get("ETag") != `"`+group.ETag+`"` {
		f.t.Fatal("group ETag header mismatch")
	}
	return group
}

func (f *groupFixture) updateGroup(group contracts.UserGroup, input contracts.UserGroupInput) contracts.UserGroup {
	f.t.Helper()
	w := f.request("PUT", "/metadata/admin/user-groups/"+group.ID, &f.admin, group.ETag, input)
	f.check(w, 200)
	return groupDecode[contracts.UserGroup](f.t, w)
}

func groupInput(group contracts.UserGroup) contracts.UserGroupInput {
	return contracts.UserGroupInput{Name: group.Name, Description: group.Description, Enabled: group.Enabled, RoleIDs: append([]string{}, group.RoleIDs...), UserIDs: append([]string{}, group.UserIDs...)}
}

func (f *groupFixture) snapshot() string {
	f.t.Helper()
	var state string
	var groups, members, sessions, audits int
	if err := f.db.QueryRow("SELECT authorization_state FROM af_identity_tenants WHERE tenant_id=?", f.tenant).Scan(&state); err != nil {
		f.t.Fatal(err)
	}
	for table, count := range map[string]*int{"af_identity_user_groups": &groups, "af_identity_user_group_members": &members, "af_identity_sessions": &sessions, "af_identity_audit": &audits} {
		if err := f.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE tenant_id=?", f.tenant).Scan(count); err != nil {
			f.t.Fatal(err)
		}
	}
	return fmt.Sprintf("%s/%d/%d/%d/%d", state, groups, members, sessions, audits)
}

// Large boundary fixtures use direct inserts in this random tenant, avoiding
// thousands of unrelated bcrypt/API operations. Assertions still use public APIs.
func (f *groupFixture) seedRoles(count int) []string {
	f.t.Helper()
	ids := make([]string, count)
	for i := range ids {
		ids[i] = contracts.NewID()
		name := "角色-" + ids[i]
		if _, err := f.db.Exec("INSERT INTO af_identity_roles (id,tenant_id,name,name_key,description,etag) VALUES (?,?,?,?,?,?)", ids[i], f.tenant, name, []byte(name), "", strings.Repeat("a", 64)); err != nil {
			f.t.Fatal("seed isolated group roles")
		}
	}
	return ids
}

func (f *groupFixture) seedUsers(count int) []contracts.User {
	f.t.Helper()
	var hash string
	if err := f.db.QueryRow("SELECT password_hash FROM af_identity_users WHERE tenant_id=? AND id=?", f.tenant, f.admin.session.User.ID).Scan(&hash); err != nil {
		f.t.Fatal(err)
	}
	users := make([]contracts.User, count)
	for i := range users {
		id := contracts.NewID()
		name := "user-" + id
		users[i] = contracts.User{ID: id, TenantID: f.tenant, Username: name, DisplayName: name, Enabled: true, RoleIDs: []string{}, ETag: strings.Repeat("a", 64)}
		if _, err := f.db.Exec("INSERT INTO af_identity_users (id,tenant_id,username,username_key,display_name,password_hash,enabled,administrator,role_ids,etag) VALUES (?,?,?,?,?,?,true,false,?,?)", id, f.tenant, name, []byte(name), name, hash, "[]", users[i].ETag); err != nil {
			f.t.Fatal("seed isolated group users")
		}
	}
	return users
}

func (f *groupFixture) policy() contracts.PolicyRecord {
	f.t.Helper()
	w := f.request("GET", "/metadata/admin/policy", &f.admin, "", nil)
	f.check(w, 200)
	return groupDecode[contracts.PolicyRecord](f.t, w)
}

func (f *groupFixture) expectDecision(user contracts.User, query contracts.AuthorizationRequest, allow bool) {
	f.t.Helper()
	decision, err := f.store.Authorize(context.Background(), contracts.Principal{TenantID: f.tenant, Subject: user.ID}, query)
	if err != nil || decision.Allow != allow {
		f.t.Fatalf("group authorization allow=%t, want %t; err=%v", decision.Allow, allow, err)
	}
	w := f.request("POST", "/metadata/admin/simulate", &f.admin, "", contracts.SimulationRequest{UserID: user.ID, Request: query})
	f.check(w, 200)
	if result := groupDecode[contracts.SimulationResult](f.t, w); result.Decision.Allow != allow {
		f.t.Fatal("simulation disagrees with effective group roles")
	}
}

func TestMySQLUserGroupLifecycle(t *testing.T) {
	f := newGroupFixture(t)
	other := newGroupFixture(t)
	roles := f.seedRoles(4)
	users := f.seedUsers(3)
	worker, disabled, replacement := users[0], users[1], users[2]
	if _, err := f.db.Exec("UPDATE af_identity_users SET enabled=false WHERE tenant_id=? AND id=?", f.tenant, disabled.ID); err != nil {
		t.Fatal(err)
	}
	workerBrowser := f.login(worker.Username)
	workerOtherSession := f.login(worker.Username)
	replacementBrowser := f.login(replacement.Username)
	schemaID, firstField, secondField := contracts.NewID(), contracts.NewID(), contracts.NewID()
	definition := contracts.Definition{ID: schemaID, TenantID: f.tenant, CategoryID: contracts.NewID(), GroupID: contracts.NewID(), Kind: contracts.KindSchema, Name: "组权限模型", Schema: &contracts.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []contracts.Property{{ID: firstField, Name: "字段一", DataType: "string"}, {ID: secondField, Name: "字段二", DataType: "string"}}}}
	raw, _ := json.Marshal(definition)
	if _, err := f.db.Exec("INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,current_json,current_digest,status) VALUES (?,?,?,?,?,?,?,?,?,'active')", schemaID, f.tenant, definition.CategoryID, definition.GroupID, "schema", definition.Name, []byte(definition.Name), string(raw), strings.Repeat("a", 64)); err != nil {
		t.Fatal("seed isolated group permission schema")
	}
	policy := f.policy()
	candidate := policy.Current
	candidate.Rules = append(candidate.Rules,
		contracts.PermissionRule{ID: contracts.NewID(), RoleID: roles[0], Effect: "allow", Operation: "instance.read", ResourceID: schemaID, ReadFields: []string{firstField}},
		contracts.PermissionRule{ID: contracts.NewID(), RoleID: roles[1], Effect: "deny", Operation: "instance.read", ResourceID: schemaID},
		contracts.PermissionRule{ID: contracts.NewID(), RoleID: roles[2], Effect: "allow", Operation: "instance.read", ResourceID: schemaID, ReadFields: []string{secondField}})
	w := f.request("PUT", "/metadata/admin/policy", &f.admin, policy.ETag, candidate)
	f.check(w, 200)
	draft := groupDecode[contracts.PolicyRecord](t, w)
	f.check(f.request("POST", "/metadata/admin/policy/publish", &f.admin, draft.ETag, nil), 200)
	query := contracts.AuthorizationRequest{Operation: "instance.read", ResourceID: schemaID}
	f.expectDecision(worker, query, false)
	input := contracts.UserGroupInput{Name: " Café ", Description: "成员可包含停用账号", Enabled: true, RoleIDs: []string{roles[3], roles[0]}, UserIDs: []string{worker.ID, disabled.ID}}
	group := f.createGroup(input)
	if group.Name != "Café" || len(group.UserIDs) != 2 || len(group.RoleIDs) != 2 {
		t.Fatal("group input not normalized")
	}
	f.check(f.request("GET", "/metadata/auth/session", &workerBrowser, "", nil), 401)
	f.check(f.request("GET", "/metadata/auth/session", &workerOtherSession, "", nil), 401)
	workerBrowser = f.login(worker.Username)
	if len(workerBrowser.session.User.RoleIDs) != 0 || workerBrowser.session.User.Administrator {
		t.Fatal("group roles leaked into direct roles or administrator flag")
	}
	f.expectDecision(worker, query, true)
	if _, err := f.store.Authorize(context.Background(), contracts.Principal{TenantID: f.tenant, Subject: disabled.ID}, query); err == nil {
		t.Fatal("disabled group member received authorization")
	}
	w = f.request("POST", "/metadata/admin/simulate", &f.admin, "", contracts.SimulationRequest{UserID: disabled.ID, Request: query})
	f.check(w, 200)
	if groupDecode[contracts.SimulationResult](t, w).Decision.Allow {
		t.Fatal("disabled group member received simulated authorization")
	}
	f.check(f.request("GET", "/metadata/admin/user-groups", &workerBrowser, "", nil), 403)
	f.check(f.request("POST", "/metadata/admin/user-groups", &workerBrowser, "", input), 403)
	w = f.request("GET", "/metadata/admin/user-groups/"+group.ID, &f.admin, "", nil)
	f.check(w, 200)
	if fetched := groupDecode[contracts.UserGroup](t, w); fetched.ETag != group.ETag || len(fetched.UserIDs) != 2 || w.Header().Get("ETag") != `"`+group.ETag+`"` {
		t.Fatal("get group disagrees with create")
	}
	w = f.request("GET", "/metadata/admin/user-groups", &f.admin, "", nil)
	f.check(w, 200)
	if listed := groupDecode[struct {
		Data []contracts.UserGroup `json:"data"`
	}](t, w); len(listed.Data) != 1 || len(listed.Data[0].UserIDs) != 2 {
		t.Fatal("group list lost members")
	}
	// Group uniqueness uses the same Unicode normalization and case folding as
	// users/roles, while its namespace remains independent from those records.
	duplicate := input
	duplicate.Name = "CAFE\u0301"
	f.check(f.request("POST", "/metadata/admin/user-groups", &f.admin, "", duplicate), 409)
	f.check(f.request("POST", "/metadata/admin/roles", &f.admin, "", contracts.RoleInput{Name: group.Name}), 201)
	f.check(f.request("POST", "/metadata/admin/users", &f.admin, "", contracts.UserInput{Username: group.Name, DisplayName: group.Name, Password: groupTestPassword, Enabled: true}), 201)
	f.check(other.request("GET", "/metadata/admin/user-groups/"+group.ID, &other.admin, "", nil), 404)
	f.check(other.request("PUT", "/metadata/admin/user-groups/"+group.ID, &other.admin, group.ETag, input), 404)
	foreign := input
	foreign.Name = "跨租户成员"
	foreign.UserIDs = []string{other.admin.session.User.ID}
	f.check(f.request("POST", "/metadata/admin/user-groups", &f.admin, "", foreign), 422)
	foreign.UserIDs = []string{}
	foreign.RoleIDs = other.admin.session.User.RoleIDs
	f.check(f.request("POST", "/metadata/admin/user-groups", &f.admin, "", foreign), 422)
	f.check(f.request("PUT", "/metadata/admin/user-groups/"+group.ID, &f.admin, "", input), 428)
	f.check(f.request("PUT", "/metadata/admin/user-groups/"+group.ID, &f.admin, "stale", input), 412)
	f.check(f.request("POST", "/metadata/admin/user-groups", &f.admin, "", map[string]any{"name": "未知字段", "nestedGroupIds": []string{group.ID}}), 400)
	duplicate = input
	duplicate.Name = "重复成员"
	duplicate.UserIDs = []string{worker.ID, worker.ID}
	f.check(f.request("POST", "/metadata/admin/user-groups", &f.admin, "", duplicate), 422)
	stateBefore := f.policy().State
	input = groupInput(group)
	input.Name = "设备维护组"
	input.Description = "只修改名称和说明"
	input.UserIDs[0], input.UserIDs[1] = input.UserIDs[1], input.UserIDs[0]
	input.RoleIDs[0], input.RoleIDs[1] = input.RoleIDs[1], input.RoleIDs[0]
	previousETag := group.ETag
	group = f.updateGroup(group, input)
	if group.ETag == previousETag || f.policy().State != stateBefore {
		t.Fatal("descriptive edit changed authorization state or failed to change ETag")
	}
	f.check(f.request("GET", "/metadata/auth/session", &workerBrowser, "", nil), 200)
	denyGroup := f.createGroup(contracts.UserGroupInput{Name: "显式拒绝", Enabled: true, RoleIDs: []string{roles[1]}, UserIDs: []string{worker.ID}})
	f.check(f.request("GET", "/metadata/auth/session", &workerBrowser, "", nil), 401)
	f.expectDecision(worker, query, false)
	workerBrowser = f.login(worker.Username)
	denyInput := groupInput(denyGroup)
	denyInput.Enabled = false
	denyGroup = f.updateGroup(denyGroup, denyInput)
	f.check(f.request("GET", "/metadata/auth/session", &workerBrowser, "", nil), 401)
	f.expectDecision(worker, query, true)
	workerBrowser = f.login(worker.Username)
	denyInput.Enabled = true
	denyInput.RoleIDs = []string{roles[2]}
	denyGroup = f.updateGroup(denyGroup, denyInput)
	f.check(f.request("GET", "/metadata/auth/session", &workerBrowser, "", nil), 401)
	if _, err := f.store.Authorize(context.Background(), contracts.Principal{TenantID: f.tenant, Subject: worker.ID}, query); err == nil {
		t.Fatal("incompatible field grants from groups silently expanded access")
	} else if access, ok := err.(*platform.AccessError); !ok || access.Status != 422 {
		t.Fatal("incompatible group grants did not return explicit 422")
	}
	f.check(f.request("POST", "/metadata/admin/simulate", &f.admin, "", contracts.SimulationRequest{UserID: worker.ID, Request: query}), 422)
	workerBrowser = f.login(worker.Username)
	denyInput.UserIDs = []string{}
	f.updateGroup(denyGroup, denyInput)
	f.check(f.request("GET", "/metadata/auth/session", &workerBrowser, "", nil), 401)
	f.expectDecision(worker, query, true)
	workerBrowser = f.login(worker.Username)
	input = groupInput(group)
	input.Enabled = false
	group = f.updateGroup(group, input)
	f.check(f.request("GET", "/metadata/auth/session", &workerBrowser, "", nil), 401)
	f.expectDecision(worker, query, false)
	workerBrowser = f.login(worker.Username)
	input.Enabled = true
	input.UserIDs = []string{replacement.ID}
	f.updateGroup(group, input)
	f.check(f.request("GET", "/metadata/auth/session", &workerBrowser, "", nil), 401)
	f.check(f.request("GET", "/metadata/auth/session", &replacementBrowser, "", nil), 401)
	f.expectDecision(worker, query, false)
	f.expectDecision(replacement, query, true)
	// The creator can be a member: commit succeeds, then its old session is gone.
	self := f.createGroup(contracts.UserGroupInput{Name: "管理员本人", Enabled: true, UserIDs: []string{f.admin.session.User.ID}})
	f.check(f.request("GET", "/metadata/auth/session", &f.admin, "", nil), 401)
	f.admin = f.login("admin")
	w = f.request("GET", "/metadata/admin/audit", &f.admin, "", nil)
	f.check(w, 200)
	var found bool
	for _, entry := range groupDecode[struct {
		Data []contracts.AuditRecord `json:"data"`
	}](t, w).Data {
		if entry.Operation == "user_group.create" && entry.TargetID == self.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("committed self-membership creation missing audit")
	}
}

func TestMySQLUserGroupBudgets(t *testing.T) {
	f := newGroupFixture(t)
	roles := f.seedRoles(65)
	users := f.seedUsers(257)
	ids := make([]string, len(users))
	for i := range users {
		ids[i] = users[i].ID
	}
	before := f.snapshot()
	f.check(f.request("POST", "/metadata/admin/user-groups", &f.admin, "", contracts.UserGroupInput{Name: "超成员预算", Enabled: true, UserIDs: ids}), 422)
	f.check(f.request("POST", "/metadata/admin/user-groups", &f.admin, "", contracts.UserGroupInput{Name: "超角色预算", Enabled: true, RoleIDs: roles}), 422)
	if f.snapshot() != before {
		t.Fatal("rejected input changed group directory, state, sessions or audit")
	}
	f.createGroup(contracts.UserGroupInput{Name: "256成员", Enabled: true, UserIDs: ids[:256]})
	user := users[0]
	userInput := contracts.UserInput{Username: user.Username, DisplayName: user.DisplayName, Enabled: true, RoleIDs: []string{roles[0]}}
	w := f.request("PUT", "/metadata/admin/users/"+user.ID, &f.admin, user.ETag, userInput)
	f.check(w, 200)
	user = groupDecode[contracts.User](t, w)
	// A direct role duplicated by a group consumes one effective role slot.
	group := f.createGroup(contracts.UserGroupInput{Name: "有效64角色", Enabled: true, RoleIDs: roles[:64], UserIDs: []string{user.ID}})
	current := f.login(user.Username)
	decision, err := f.store.Authorize(context.Background(), contracts.Principal{TenantID: f.tenant, Subject: user.ID}, contracts.AuthorizationRequest{Operation: "instance.read", ResourceID: "anything"})
	if err != nil || decision.Allow {
		t.Fatalf("64 distinct effective roles failed or granted unconfigured access: %v", err)
	}
	before = f.snapshot()
	input := groupInput(group)
	input.RoleIDs = roles[1:]
	f.check(f.request("PUT", "/metadata/admin/user-groups/"+group.ID, &f.admin, group.ETag, input), 422)
	userInput.RoleIDs = []string{roles[64]}
	f.check(f.request("PUT", "/metadata/admin/users/"+user.ID, &f.admin, user.ETag, userInput), 422)
	if f.snapshot() != before {
		t.Fatal("effective-role rejection changed directory, state, sessions or audit")
	}
	f.check(f.request("GET", "/metadata/auth/session", &current, "", nil), 200)
	w = f.request("GET", "/metadata/admin/user-groups/"+group.ID, &f.admin, "", nil)
	f.check(w, 200)
	if persisted := groupDecode[contracts.UserGroup](t, w); persisted.ETag != group.ETag {
		t.Fatal("rejected role update persisted its ETag")
	}
	// Disabled memberships still count, preventing unbounded dormant groups.
	limited := users[256]
	for i := 0; i < 64; i++ {
		f.createGroup(contracts.UserGroupInput{Name: fmt.Sprintf("停用组-%02d", i), Enabled: false, UserIDs: []string{limited.ID}})
	}
	current = f.login(limited.Username)
	before = f.snapshot()
	f.check(f.request("POST", "/metadata/admin/user-groups", &f.admin, "", contracts.UserGroupInput{Name: "第65组", Enabled: false, UserIDs: []string{limited.ID}}), 422)
	if f.snapshot() != before {
		t.Fatal("65th membership rejection had transaction side effects")
	}
	f.check(f.request("GET", "/metadata/auth/session", &current, "", nil), 200)
}

func TestMySQLUserGroupConcurrentUpdates(t *testing.T) {
	f := newGroupFixture(t)
	user := f.seedUsers(1)[0]
	roles := f.seedRoles(65)
	group := f.createGroup(contracts.UserGroupInput{Name: "并发ETag", Enabled: true, UserIDs: []string{user.ID}})
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := groupInput(group)
			input.Description = fmt.Sprintf("并发-%d", i)
			codes <- f.request("PUT", "/metadata/admin/user-groups/"+group.ID, &f.admin, group.ETag, input).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	results := []int{}
	for code := range codes {
		results = append(results, code)
	}
	sort.Ints(results)
	if len(results) != 2 || results[0] != 200 || results[1] != 412 {
		t.Fatalf("same group ETag race returned %v", results)
	}
	input := contracts.UserInput{Username: user.Username, DisplayName: user.DisplayName, Enabled: true, RoleIDs: roles[:63]}
	w := f.request("PUT", "/metadata/admin/users/"+user.ID, &f.admin, user.ETag, input)
	f.check(w, 200)
	first := f.createGroup(contracts.UserGroupInput{Name: "并发角色预算A", Enabled: true, UserIDs: []string{user.ID}})
	second := f.createGroup(contracts.UserGroupInput{Name: "并发角色预算B", Enabled: true, UserIDs: []string{user.ID}})
	var auditsBefore int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM af_identity_audit WHERE tenant_id=?", f.tenant).Scan(&auditsBefore); err != nil {
		t.Fatal(err)
	}
	codes = make(chan int, 2)
	for i, next := range []contracts.UserGroup{first, second} {
		wg.Add(1)
		go func(i int, next contracts.UserGroup) {
			defer wg.Done()
			input := groupInput(next)
			input.RoleIDs = []string{roles[63+i]}
			codes <- f.request("PUT", "/metadata/admin/user-groups/"+next.ID, &f.admin, next.ETag, input).Code
		}(i, next)
	}
	wg.Wait()
	close(codes)
	results = []int{}
	for code := range codes {
		results = append(results, code)
	}
	sort.Ints(results)
	if len(results) != 2 || results[0] != 200 || results[1] != 422 {
		t.Fatalf("effective role race returned %v", results)
	}
	var auditsAfter int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM af_identity_audit WHERE tenant_id=?", f.tenant).Scan(&auditsAfter); err != nil || auditsAfter != auditsBefore+1 {
		t.Fatal("concurrent budget rejection committed audit")
	}
	if _, err := f.store.Authorize(context.Background(), contracts.Principal{TenantID: f.tenant, Subject: user.ID}, contracts.AuthorizationRequest{Operation: "instance.read", ResourceID: "anything"}); err != nil {
		t.Fatalf("concurrent groups exceeded effective role budget: %v", err)
	}
	limited := f.seedUsers(1)[0]
	for i := 0; i < 63; i++ {
		id := contracts.NewID()
		name := "并发成员预算-" + id
		if _, err := f.db.Exec("INSERT INTO af_identity_user_groups (id,tenant_id,name,name_key,description,enabled,role_ids,etag) VALUES (?,?,?,?,?,false,?,?)", id, f.tenant, name, []byte(name), "", "[]", strings.Repeat("a", 64)); err != nil {
			t.Fatal("seed isolated membership race group")
		}
		if _, err := f.db.Exec("INSERT INTO af_identity_user_group_members (tenant_id,group_id,user_id) VALUES (?,?,?)", f.tenant, id, limited.ID); err != nil {
			t.Fatal("seed isolated membership race member")
		}
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM af_identity_audit WHERE tenant_id=?", f.tenant).Scan(&auditsBefore); err != nil {
		t.Fatal(err)
	}
	codes = make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := contracts.UserGroupInput{Name: fmt.Sprintf("并发第64组-%d", i), Enabled: false, UserIDs: []string{limited.ID}}
			codes <- f.request("POST", "/metadata/admin/user-groups", &f.admin, "", input).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	results = []int{}
	for code := range codes {
		results = append(results, code)
	}
	sort.Ints(results)
	if len(results) != 2 || results[0] != 201 || results[1] != 422 {
		t.Fatalf("membership race returned %v", results)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM af_identity_audit WHERE tenant_id=?", f.tenant).Scan(&auditsAfter); err != nil || auditsAfter != auditsBefore+1 {
		t.Fatal("concurrent membership rejection committed audit")
	}
	var memberships int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM af_identity_user_group_members WHERE tenant_id=? AND user_id=?", f.tenant, limited.ID).Scan(&memberships); err != nil || memberships != 64 {
		t.Fatal("concurrent groups exceeded membership budget")
	}
}

func TestMySQLUserGroupDirectoryBudgets(t *testing.T) {
	t.Run("groups", func(t *testing.T) {
		f := newGroupFixture(t)
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		for i := 0; i < 1999; i++ {
			id := contracts.NewID()
			name := "目录组-" + id
			if _, err := tx.Exec("INSERT INTO af_identity_user_groups (id,tenant_id,name,name_key,description,enabled,role_ids,etag) VALUES (?,?,?,?,?,false,?,?)", id, f.tenant, name, []byte(name), "", "[]", strings.Repeat("a", 64)); err != nil {
				t.Fatal("seed isolated group directory")
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		f.createGroup(contracts.UserGroupInput{Name: "第2000组"})
		before := f.snapshot()
		f.check(f.request("POST", "/metadata/admin/user-groups", &f.admin, "", contracts.UserGroupInput{Name: "第2001组"}), 422)
		if f.snapshot() != before {
			t.Fatal("group directory write budget failure committed changes")
		}
		w := f.request("GET", "/metadata/admin/user-groups", &f.admin, "", nil)
		f.check(w, 200)
		if list := groupDecode[struct {
			Data []contracts.UserGroup `json:"data"`
		}](t, w); len(list.Data) != 2000 {
			t.Fatal("group directory boundary silently truncated")
		}
		id := contracts.NewID()
		if _, err := f.db.Exec("INSERT INTO af_identity_user_groups (id,tenant_id,name,name_key,description,enabled,role_ids,etag) VALUES (?,?,?,?,?,false,?,?)", id, f.tenant, "读取超预算", []byte("读取超预算"), "", "[]", strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
		f.check(f.request("GET", "/metadata/admin/user-groups", &f.admin, "", nil), 422)
	})
	t.Run("memberships", func(t *testing.T) {
		f := newGroupFixture(t)
		users := f.seedUsers(320)
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var first contracts.UserGroup
		for i := 0; i < 80; i++ {
			id := contracts.NewID()
			name := "成员目录组-" + id
			if _, err := tx.Exec("INSERT INTO af_identity_user_groups (id,tenant_id,name,name_key,description,enabled,role_ids,etag) VALUES (?,?,?,?,?,false,?,?)", id, f.tenant, name, []byte(name), "", "[]", strings.Repeat("a", 64)); err != nil {
				t.Fatal("seed isolated membership directory")
			}
			group := contracts.UserGroup{ID: id, Name: name, RoleIDs: []string{}, UserIDs: []string{}, ETag: strings.Repeat("a", 64)}
			args := []any{}
			values := []string{}
			for j := 0; j < 250; j++ {
				member := users[(i*250+j)%len(users)].ID
				group.UserIDs = append(group.UserIDs, member)
				values = append(values, "(?,?,?)")
				args = append(args, f.tenant, id, member)
			}
			if _, err := tx.Exec("INSERT INTO af_identity_user_group_members (tenant_id,group_id,user_id) VALUES "+strings.Join(values, ","), args...); err != nil {
				t.Fatal("seed isolated directory members")
			}
			if i == 0 {
				first = group
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		before := f.snapshot()
		input := groupInput(first)
		input.UserIDs = append(input.UserIDs, users[250].ID)
		f.check(f.request("PUT", "/metadata/admin/user-groups/"+first.ID, &f.admin, first.ETag, input), 422)
		if f.snapshot() != before {
			t.Fatal("directory membership write budget failure committed changes")
		}
		w := f.request("GET", "/metadata/admin/user-groups", &f.admin, "", nil)
		f.check(w, 200)
		var count int
		for _, group := range groupDecode[struct {
			Data []contracts.UserGroup `json:"data"`
		}](t, w).Data {
			count += len(group.UserIDs)
		}
		if count != 20000 {
			t.Fatal("directory membership boundary silently truncated")
		}
		if _, err := f.db.Exec("INSERT INTO af_identity_user_group_members (tenant_id,group_id,user_id) VALUES (?,?,?)", f.tenant, first.ID, users[250].ID); err != nil {
			t.Fatal(err)
		}
		f.check(f.request("GET", "/metadata/admin/user-groups", &f.admin, "", nil), 422)
	})
}
