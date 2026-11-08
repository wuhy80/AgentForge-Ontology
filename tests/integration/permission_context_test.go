package integration_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	data "github.com/wuhy80/AgentForge-Ontology/services/data"
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

// Real users, TLS/Cookie/CSRF, authoritative identity, OPA and two MySQL stores.
// It exercises Data -> Metadata -> Data callbacks while a public Data request
// holds business admission. No test identity or static OPA grant supplies access.
func TestPermissionContextSessionsAndOPAEndToEnd(t *testing.T) {
	mdsn, ddsn, opa := os.Getenv("ONTOLOGY_TEST_METADATA_DSN"), os.Getenv("ONTOLOGY_TEST_DATA_DSN"), os.Getenv("ONTOLOGY_TEST_OPA_URL")
	if mdsn == "" || ddsn == "" || opa == "" {
		t.Skip("set two ONTOLOGY_TEST_*_DSN values and ONTOLOGY_TEST_OPA_URL for real permission context integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	open := func(dsn string) *storage.Engine {
		engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 12, MaxIdle: 3})
		if err != nil {
			t.Fatal("open isolated permission context database")
		}
		t.Cleanup(func() { _ = engine.Close() })
		return engine
	}
	me, de := open(mdsn), open(ddsn)
	if err := storage.Migrate(ctx, me.DB(), "metadata", append(append([]storage.Migration{}, metadata.Migrations...), identity.Migrations...)); err != nil {
		t.Fatal("explicit identity migration")
	}
	if err := storage.Migrate(ctx, de.DB(), "data", data.Migrations); err != nil {
		t.Fatal("explicit data migration")
	}
	tenant, foreign := "permission-context-"+c.NewID(), "permission-context-other-"+c.NewID()
	t.Cleanup(func() {
		for _, id := range []string{tenant, foreign} {
			cleanupOntologyTenant(t, me, de, id)
			for _, table := range []string{"af_identity_user_group_members", "af_identity_user_groups", "af_identity_sessions", "af_identity_audit", "af_identity_users", "af_identity_roles", "af_identity_tenants"} {
				if _, err := me.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=?", id); err != nil {
					t.Error("clean isolated permission context identities")
				}
			}
		}
	})
	const password = "PermissionContextFixture!2026"
	accounts := identity.NewStore(me.DB(), &platform.OPAAuthorizer{URL: opa}, publicSecurityOrigin)
	for _, id := range []string{tenant, foreign} {
		if err := accounts.Bootstrap(ctx, id, "admin", password); err != nil {
			t.Fatal("bootstrap isolated permission context tenant")
		}
	}
	metaMux := http.NewServeMux()
	metaServer := httptest.NewTLSServer(platform.ProtectSessionRoutes(serviceToken, true, accounts, metaMux))
	t.Cleanup(metaServer.Close)
	checker := &platform.RemoteSessionChecker{URL: metaServer.URL, ServiceToken: serviceToken, Client: metaServer.Client()}
	authorizer := &platform.RemoteAuthorizer{URL: metaServer.URL, ServiceToken: serviceToken, Client: metaServer.Client()}
	dataServer := httptest.NewTLSServer(platform.ProtectSessionRoutes(serviceToken, false, checker, data.NewHandler(data.Config{Engine: de, Authorizer: authorizer, SessionChecker: checker, MetadataURL: metaServer.URL, ServiceToken: serviceToken, CursorSecret: cursorSecret, HTTPClient: metaServer.Client()})))
	t.Cleanup(dataServer.Close)
	reader := &platform.RemoteObjectContext{URL: dataServer.URL, ServiceToken: serviceToken, Client: dataServer.Client()}
	accounts.ObjectContext = reader.Read
	metaMux.Handle("/metadata/auth/", accounts.NewHandler())
	metaMux.Handle("/metadata/admin/", accounts.NewHandler())
	metaMux.Handle("POST /internal/identity/session", executionInternal(serviceToken, func(w http.ResponseWriter, r *http.Request) {
		var input c.SessionCheck
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			platform.WriteError(w, 400, "invalid_json", "无效请求")
			return
		}
		result, err := accounts.CheckSession(r.Context(), input)
		if err != nil {
			identity.WriteError(w, err)
			return
		}
		platform.WriteJSON(w, 200, result)
	}))
	metaMux.Handle("POST /internal/authorization/decision", executionInternal(serviceToken, func(w http.ResponseWriter, r *http.Request) {
		var input c.AuthorizationCheck
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			platform.WriteError(w, 400, "invalid_json", "无效请求")
			return
		}
		result, err := accounts.Authorize(r.Context(), input.Principal, input.Request)
		if err != nil {
			identity.WriteError(w, err)
			return
		}
		platform.WriteJSON(w, 200, result)
	}))
	metaApp := metadata.NewHandler(metadata.Config{Engine: me, Authorizer: accounts, SessionChecker: accounts, DataURL: dataServer.URL, ServiceToken: serviceToken, HTTPClient: dataServer.Client()})
	metaMux.Handle("POST /internal/metadata/query-directory", metadata.NewQueryDirectoryHandler(serviceToken, accounts, metaApp))
	metaMux.Handle("/", metaApp)
	roots := x509.NewCertPool()
	roots.AddCert(metaServer.Certificate())
	roots.AddCert(dataServer.Certificate())
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	login := func(id, username string) *publicSecurityHTTP {
		browser := &publicSecurityHTTP{t: t, client: client}
		var session c.Session
		headers := browser.request("POST", metaServer.URL+"/metadata/auth/login", c.LoginRequest{Tenant: id, Username: username, Password: password}, nil, 200, &session)
		response := http.Response{Header: headers}
		browser.cookies, browser.csrf = response.Cookies(), session.CSRFToken
		return browser
	}
	admin, outsider := login(tenant, "admin"), login(foreign, "admin")
	var category, group c.Organization
	admin.request("POST", metaServer.URL+"/metadata/categories", map[string]string{"name": "权限上下文测试"}, nil, 201, &category)
	admin.request("POST", metaServer.URL+"/metadata/groups", map[string]string{"name": "隔离模型", "categoryId": category.ID}, nil, 201, &group)
	publish := func(name string, properties []c.Property, indexes []c.Index) c.Definition {
		var draft c.CurrentDefinition
		headers := admin.request("POST", metaServer.URL+"/metadata/definitions", c.Definition{Kind: c.KindSchema, CategoryID: category.ID, GroupID: group.ID, Name: name, Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: properties, Indexes: indexes}}, nil, 201, &draft)
		admin.request("POST", metaServer.URL+"/metadata/definitions/"+draft.Definition.ID+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
		return draft.Definition
	}
	profileUser, profileName, profileDepartment := c.NewID(), c.NewID(), c.NewID()
	profiles := publish("成员资料", []c.Property{{ID: profileUser, Name: "账号ID", DataType: "uuid", Required: true}, {ID: profileName, Name: "账号名称", DataType: "string", Required: true}, {ID: profileDepartment, Name: "授权部门", DataType: "string", Required: true}}, []c.Index{{ID: c.NewID(), Name: "账号ID唯一", Unique: true, FieldIDs: []string{profileUser}}, {ID: c.NewID(), Name: "账号名称唯一", Unique: true, FieldIDs: []string{profileName}}})
	code, owner, department, secret := c.NewID(), c.NewID(), c.NewID(), c.NewID()
	documents := publish("业务记录", []c.Property{{ID: code, Name: "代码", DataType: "string", Required: true}, {ID: owner, Name: "负责人", DataType: "uuid", Required: true}, {ID: department, Name: "所属部门", DataType: "string", Required: true}, {ID: secret, Name: "隐藏内容", DataType: "string", Nullable: true}}, nil)
	var role c.Role
	admin.request("POST", metaServer.URL+"/metadata/admin/roles", c.RoleInput{Name: "上下文限定角色"}, nil, 201, &role)
	users := make([]c.User, 2)
	for i, name := range []string{"alice", "bob"} {
		admin.request("POST", metaServer.URL+"/metadata/admin/users", c.UserInput{Username: name, Password: password, DisplayName: name, Enabled: true, RoleIDs: []string{role.ID}}, nil, 201, &users[i])
	}
	const alpha, beta = "private-authority-department-alpha", "private-authority-department-beta"
	profileRows := make([]c.Instance, 2)
	for i, name := range []string{alpha, beta} {
		admin.request("POST", dataServer.URL+"/data/schemas/"+profiles.ID+"/instances", c.WriteRequest{Values: map[string]json.RawMessage{profileUser: raw(users[i].ID), profileName: raw(users[i].Username), profileDepartment: raw(name)}, IdempotencyKey: c.NewID()}, nil, 201, &profileRows[i])
	}
	values := func(label, user, dept string) map[string]json.RawMessage {
		return map[string]json.RawMessage{code: raw(label), owner: raw(user), department: raw(dept)}
	}
	create := func(browser *publicSecurityHTTP, label, user, dept, key string, want int) c.Instance {
		var item c.Instance
		browser.request("POST", dataServer.URL+"/data/schemas/"+documents.ID+"/instances", c.WriteRequest{Values: values(label, user, dept), IdempotencyKey: key}, nil, want, &item)
		return item
	}
	first := create(admin, "a-1", users[0].ID, alpha, c.NewID(), 201)
	create(admin, "a-2", users[0].ID, alpha, c.NewID(), 201)
	otherDepartment := create(admin, "a-other", users[0].ID, beta, c.NewID(), 201)
	bobRow := create(admin, "b-1", users[1].ID, beta, c.NewID(), 201)
	condition := func(byUsername bool) *c.Expression {
		ref := &c.PermissionValueRef{Source: "object", SchemaID: profiles.ID, FieldID: profileDepartment, MatchFieldID: profileUser, MatchUserAttribute: "id"}
		if byUsername {
			ref.MatchFieldID, ref.MatchUserAttribute = profileName, "username"
		}
		return &c.Expression{Op: "and", Args: []c.Expression{{Op: "eq", Field: owner, ValueRef: &c.PermissionValueRef{Source: "user", Attribute: "id"}}, {Op: "eq", Field: department, ValueRef: ref}}}
	}
	var policy c.PolicyRecord
	admin.request("GET", metaServer.URL+"/metadata/admin/policy", nil, nil, 200, &policy)
	// The current text compiler separately requires whole target definition
	// discovery. This does not grant access to the protected profile definition.
	policy.Current.Rules = append(policy.Current.Rules, c.PermissionRule{ID: c.NewID(), RoleID: role.ID, Effect: "allow", Operation: "definition.read", ResourceID: documents.ID})
	for _, op := range []string{"instance.query", "instance.read", "instance.create", "instance.patch"} {
		rule := c.PermissionRule{ID: c.NewID(), RoleID: role.ID, Effect: "allow", Operation: op, ResourceID: documents.ID, ReadFields: []string{code, owner}, WriteFields: []string{code, owner, department}}
		if op == "instance.create" {
			rule.NewRowFilter = condition(false)
		} else {
			rule.RowFilter = condition(op == "instance.read")
		}
		if op == "instance.patch" {
			rule.NewRowFilter = condition(false)
		}
		policy.Current.Rules = append(policy.Current.Rules, rule)
	}
	headers := admin.request("PUT", metaServer.URL+"/metadata/admin/policy", policy.Current, map[string]string{"If-Match": `"` + policy.ETag + `"`}, 200, &policy)
	admin.request("POST", metaServer.URL+"/metadata/admin/policy/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	alice, bob := login(tenant, "alice"), login(tenant, "bob")
	query := c.QuerySpec{From: c.Source{SchemaID: documents.ID}, Select: []string{code, owner}, OrderBy: []c.Order{{Field: code, Direction: "asc"}}, Page: c.Page{Limit: 1}}
	var page c.QueryResult
	alice.request("POST", dataServer.URL+"/data/query", query, map[string]string{"X-User-ID": users[1].ID, "X-Tenant-ID": foreign}, 200, &page)
	if len(page.Data) != 1 || page.Data[0].ID != first.ID || len(page.Data[0].Values) != 2 || page.NextCursor == "" {
		t.Fatal("dynamic user/object filter or hidden-column projection failed")
	}
	oldCursor := page.NextCursor
	query.Page.Cursor = oldCursor
	alice.request("POST", dataServer.URL+"/data/query", query, nil, 200, &page)
	if len(page.Data) != 1 || page.HasMore {
		t.Fatal("dynamic condition was applied after pagination")
	}
	query.Page.Cursor = ""
	bob.request("POST", dataServer.URL+"/data/query", query, nil, 200, &page)
	if len(page.Data) != 1 || page.Data[0].ID != bobRow.ID || page.HasMore {
		t.Fatal("context leaked across users sharing the same role")
	}
	alice.request("GET", dataServer.URL+"/data/schemas/"+documents.ID+"/instances/"+first.ID, nil, nil, 200, nil)
	alice.request("GET", dataServer.URL+"/data/schemas/"+documents.ID+"/instances/"+otherDepartment.ID, nil, nil, 404, nil)
	alice.request("GET", dataServer.URL+"/data/schemas/"+profiles.ID+"/instances/"+profileRows[0].ID, nil, nil, 403, nil)
	outsider.request("POST", dataServer.URL+"/data/query", query, nil, 404, nil)
	alice.request("POST", dataServer.URL+"/internal/authorization/object-value", c.ObjectContextRequest{SchemaID: profiles.ID, FieldID: profileDepartment, InstanceID: profileRows[0].ID}, nil, 401, nil)
	var sqlResult c.OntologySQLResult
	sqlRequest := c.OntologySQLRequest{Text: `SELECT "代码", "负责人" FROM "权限上下文测试"."业务记录" ORDER BY "代码" LIMIT 10`}
	var analysis c.OntologySQLAnalysis
	alice.request("POST", dataServer.URL+"/data/query/ontology-sql/analyze", sqlRequest, nil, 200, &analysis)
	if !analysis.Ready {
		t.Fatalf("context SQL did not bind: %+v", analysis.Diagnostics)
	}
	alice.request("POST", dataServer.URL+"/data/query/ontology-sql", sqlRequest, nil, 200, &sqlResult)
	if len(sqlResult.Data) != 2 {
		t.Fatal("OntologySQL bypassed or lost context filtering")
	}
	var simulation json.RawMessage
	admin.request("POST", metaServer.URL+"/metadata/admin/simulate", c.SimulationRequest{UserID: users[0].ID, Request: c.AuthorizationRequest{Operation: "instance.query", ResourceID: documents.ID, Kind: c.KindSchema}}, nil, 200, &simulation)
	var simulated c.SimulationResult
	if json.Unmarshal(simulation, &simulated) != nil || !simulated.Decision.Allow || !simulated.ContextProtected || simulated.Decision.RowFilter != nil || simulated.Decision.ContextDigest != "" || strings.Contains(string(simulation), alpha) {
		t.Fatal("simulation exposed resolved protected attribute values")
	}
	query.Where = &c.Expression{Op: "eq", Field: owner, ValueRef: &c.PermissionValueRef{Source: "user", Attribute: "id"}}
	alice.request("POST", dataServer.URL+"/data/query", query, nil, 422, nil)
	query.Where = nil
	alice.request("POST", dataServer.URL+"/data/query", map[string]any{"from": query.From, "select": query.Select, "page": query.Page, "context": map[string]string{"department": beta}}, nil, 400, nil)
	key := c.NewID()
	created := create(alice, "own-create", users[0].ID, alpha, key, 201)
	create(alice, "wrong-owner", users[1].ID, alpha, c.NewID(), 403)
	create(alice, "wrong-dept", users[0].ID, beta, c.NewID(), 403)
	itemURL := dataServer.URL + "/data/schemas/" + documents.ID + "/instances/" + created.ID
	alice.request("PATCH", itemURL, c.WriteRequest{Values: map[string]json.RawMessage{department: raw(beta)}}, map[string]string{"If-Match": `"` + created.ETag + `"`}, 403, nil)
	alice.request("PATCH", itemURL, c.WriteRequest{Values: map[string]json.RawMessage{owner: raw(users[1].ID)}}, map[string]string{"If-Match": `"` + created.ETag + `"`}, 403, nil)
	bulk := c.BulkWriteRequest{IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{{ClientKey: "first", Values: values("bulk-rolled-back", users[0].ID, alpha)}, {ClientKey: "forbidden", Values: values("bulk-denied", users[0].ID, beta)}}}
	alice.request("POST", dataServer.URL+"/data/schemas/"+documents.ID+"/instances/bulk-create", bulk, nil, 403, nil)
	probe := c.QuerySpec{From: c.Source{SchemaID: documents.ID}, Select: []string{code}, Where: &c.Expression{Op: "eq", Field: code, Value: raw("bulk-rolled-back")}, Page: c.Page{Limit: 10}}
	admin.request("POST", dataServer.URL+"/data/query", probe, nil, 200, &page)
	if len(page.Data) != 0 {
		t.Fatal("bulk context denial left a partial write")
	}
	// Change only the isolated user's authority object, through the public API.
	profileURL := dataServer.URL + "/data/schemas/" + profiles.ID + "/instances/" + profileRows[0].ID
	admin.request("PATCH", profileURL, c.WriteRequest{Values: map[string]json.RawMessage{profileDepartment: raw(beta)}}, map[string]string{"If-Match": `"` + profileRows[0].ETag + `"`}, 200, &profileRows[0])
	query.Page.Cursor = oldCursor
	alice.request("POST", dataServer.URL+"/data/query", query, nil, 422, nil)
	query.Page.Cursor = ""
	alice.request("POST", dataServer.URL+"/data/query", query, nil, 200, &page)
	if len(page.Data) != 1 || page.Data[0].ID != otherDepartment.ID {
		t.Fatal("object attribute change did not change current access")
	}
	create(alice, "own-create", users[0].ID, alpha, key, 403)
	alice.request("GET", itemURL, nil, nil, 404, nil)
	// Inject a concurrent committed change in this random test row during
	// collection, then after its final consistency read. Both stages must fail
	// closed. This is test scheduling, never an authority shortcut.
	var changed atomic.Bool
	var contextReads atomic.Int32
	var changeAfter atomic.Int32
	changeAfter.Store(1)
	accounts.ObjectContext = func(ctx context.Context, tenantID string, input c.ObjectContextRequest) (c.ObjectContextResult, error) {
		result, err := reader.Read(ctx, tenantID, input)
		if err == nil && tenantID == tenant && input.SchemaID == profiles.ID && contextReads.Add(1) == changeAfter.Load() && changed.CompareAndSwap(false, true) {
			tableHash := sha256.Sum256(raw(profiles.ID))
			fieldHash := sha256.Sum256(raw(profileDepartment))
			_, err = de.DB().ExecContext(ctx, "UPDATE `af_s_"+hex.EncodeToString(tableHash[:])[:32]+"` SET `f_"+hex.EncodeToString(fieldHash[:])[:32]+"`=?,etag=? WHERE tenant_id=? AND id=?", alpha, c.NewID(), tenant, profileRows[0].ID)
		}
		return result, err
	}
	alice.request("POST", dataServer.URL+"/data/query", query, nil, 403, nil)
	if !changed.Load() {
		t.Fatal("context collection race was not injected")
	}
	changed.Store(false)
	contextReads.Store(0)
	changeAfter.Store(2)
	alice.request("POST", dataServer.URL+"/data/query", query, nil, 409, nil)
	if !changed.Load() {
		t.Fatal("context dependency race was not injected")
	}
	accounts.ObjectContext = reader.Read
	admin.request("GET", profileURL, nil, nil, 200, &profileRows[0])
	// Configure explicit source maintenance solely to prove that the mixed
	// transaction guard survives the real authorization RPC, even when each
	// operation is individually granted. Nothing in the rejected batch commits.
	admin.request("GET", metaServer.URL+"/metadata/admin/policy", nil, nil, 200, &policy)
	policy.Current.Rules = append(policy.Current.Rules,
		c.PermissionRule{ID: c.NewID(), RoleID: role.ID, Effect: "allow", Operation: "instance.patch", ResourceID: profiles.ID, WriteFields: []string{profileDepartment}, RowFilter: &c.Expression{Op: "eq", Field: profileUser, ValueRef: &c.PermissionValueRef{Source: "user", Attribute: "id"}}},
		c.PermissionRule{ID: c.NewID(), RoleID: role.ID, Effect: "allow", Operation: "instance.delete", ResourceID: documents.ID, ReadFields: []string{code, owner}, RowFilter: &c.Expression{Op: "and", Args: []c.Expression{{Op: "eq", Field: owner, ValueRef: &c.PermissionValueRef{Source: "user", Attribute: "id"}}, {Op: "eq", Field: department, ValueRef: &c.PermissionValueRef{Source: "object", SchemaID: profiles.ID, FieldID: profileDepartment, InstanceID: profileRows[0].ID}}}}})
	headers = admin.request("PUT", metaServer.URL+"/metadata/admin/policy", policy.Current, map[string]string{"If-Match": `"` + policy.ETag + `"`}, 200, nil)
	admin.request("POST", metaServer.URL+"/metadata/admin/policy/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	var transaction c.WriteTransactionResult
	alice.request("POST", dataServer.URL+"/data/transactions", c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{
		{ClientKey: "source", SchemaID: profiles.ID, Operation: "update", ID: profileRows[0].ID, IfMatch: `"` + profileRows[0].ETag + `"`, Values: map[string]json.RawMessage{profileDepartment: raw(beta)}},
		{ClientKey: "dependent", SchemaID: documents.ID, Operation: "create", Values: values("source-mutating-batch", users[0].ID, alpha)},
	}}, nil, 200, &transaction)
	if transaction.Committed || transaction.Error == nil || transaction.Error.Code != "context_source_mutation" {
		t.Fatal("mixed transaction changed its authorization source")
	}
	admin.request("GET", profileURL, nil, nil, 200, &profileRows[0])
	if string(profileRows[0].Values[profileDepartment]) != string(raw(alpha)) {
		t.Fatal("rejected mixed transaction changed the source")
	}
	alice.request("DELETE", itemURL, nil, map[string]string{"If-Match": `"` + created.ETag + `"`}, 204, nil)
	admin.request("DELETE", profileURL, nil, map[string]string{"If-Match": `"` + profileRows[0].ETag + `"`}, 204, nil)
	alice.request("POST", dataServer.URL+"/data/query", query, nil, 403, nil)
	t.Log("real MySQL/OPA/TLS/Cookie: subject and object contexts, ID/username binding, private attribute dependency, SQL/read/write/bulk, current changes, stale cursors/replays, denied injection and in-flight dependency recheck verified")
}
