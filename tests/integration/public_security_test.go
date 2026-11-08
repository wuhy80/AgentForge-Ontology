package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	data "github.com/wuhy80/AgentForge-Ontology/services/data"
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

const publicSecurityOrigin = "https://security.test:30011"

type publicSecurityHTTP struct {
	t       *testing.T
	client  *http.Client
	cookies []*http.Cookie
	csrf    string
}

// Never print response bodies: successful identity responses contain CSRF and
// failed security assertions must not turn test logs into credential stores.
func (h *publicSecurityHTTP) request(method, endpoint string, body any, headers map[string]string, want int, out any) http.Header {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal("encode public security fixture")
		}
		reader = bytes.NewReader(encoded)
	}
	return h.rawRequest(method, endpoint, reader, headers, want, out)
}

func (h *publicSecurityHTTP) rawRequest(method, endpoint string, reader io.Reader, headers map[string]string, want int, out any) http.Header {
	h.t.Helper()
	r, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		h.t.Fatal("construct public security request")
	}
	r.Host = "security.test:30011"
	r.Header.Set("Origin", publicSecurityOrigin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", h.csrf)
	for _, cookie := range h.cookies {
		r.AddCookie(cookie)
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	response, err := h.client.Do(r)
	if err != nil {
		h.t.Fatal("public security transport failed")
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(encoded) > 2<<20 {
		h.t.Fatal("public security response exceeded fixture budget")
	}
	if response.StatusCode != want {
		var failure c.Error
		_ = json.Unmarshal(encoded, &failure)
		h.t.Fatalf("%s %s: got %d want %d (code=%s; body redacted)", method, r.URL.Path, response.StatusCode, want, failure.Code)
	}
	if !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
		h.t.Fatal("public response allowed shared caching")
	}
	if out != nil && json.Unmarshal(encoded, out) != nil {
		h.t.Fatal("invalid public response (body redacted)")
	}
	return response.Header
}

type publicSecuritySchema struct {
	id, category             string
	code, department, secret string
	count, amount, immutable string
}

type publicSecurityTenant struct {
	id     string
	admin  *publicSecurityHTTP
	reader *publicSecurityHTTP
	user   c.User
	role   c.Role
	models []publicSecuritySchema
	hidden publicSecuritySchema
}

// Real TLS HTTP, real MySQL, real OPA and the same public Cookie middleware and
// private session/authorization RPC used by the services. No signed test user
// adapter, Docker runner, production account or existing tenant is involved.
func TestPublicSecuritySessionsAndOPAEndToEnd(t *testing.T) {
	mdsn, ddsn, opaURL := os.Getenv("ONTOLOGY_TEST_METADATA_DSN"), os.Getenv("ONTOLOGY_TEST_DATA_DSN"), os.Getenv("ONTOLOGY_TEST_OPA_URL")
	if mdsn == "" || ddsn == "" || opaURL == "" {
		t.Skip("set two ONTOLOGY_TEST_*_DSN values and ONTOLOGY_TEST_OPA_URL for public Cookie security integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	open := func(dsn string) *storage.Engine {
		engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 10, MaxIdle: 3})
		if err != nil {
			t.Fatal("open isolated public security database")
		}
		t.Cleanup(func() { _ = engine.Close() })
		return engine
	}
	me, de := open(mdsn), open(ddsn)
	if err := storage.Migrate(ctx, me.DB(), "metadata", append(append([]storage.Migration{}, metadata.Migrations...), identity.Migrations...)); err != nil {
		t.Fatal("explicit metadata migration failed")
	}
	if err := storage.Migrate(ctx, de.DB(), "data", data.Migrations); err != nil {
		t.Fatal("explicit data migration failed")
	}
	tenants := []*publicSecurityTenant{{id: "public-a-" + c.NewID()}, {id: "public-b-" + c.NewID()}}
	t.Cleanup(func() {
		for _, tenant := range tenants {
			cleanupOntologyTenant(t, me, de, tenant.id)
			for _, table := range []string{"af_identity_user_group_members", "af_identity_user_groups", "af_identity_sessions", "af_identity_audit", "af_identity_users", "af_identity_roles", "af_identity_tenants"} {
				if _, err := me.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant.id); err != nil {
					t.Error("cleanup isolated public security identities")
				}
			}
		}
	})
	accounts := identity.NewStore(me.DB(), &platform.OPAAuthorizer{URL: opaURL}, publicSecurityOrigin)
	const password = "PublicSecurityFixture!2026"
	for _, tenant := range tenants {
		if err := accounts.Bootstrap(ctx, tenant.id, "admin", password); err != nil {
			t.Fatal("bootstrap isolated public security administrator")
		}
	}
	metaMux := http.NewServeMux()
	metaServer := httptest.NewTLSServer(platform.ProtectSessionRoutes(serviceToken, true, accounts, metaMux))
	t.Cleanup(metaServer.Close)
	checker := &platform.RemoteSessionChecker{URL: metaServer.URL, ServiceToken: serviceToken, Client: metaServer.Client()}
	authorizer := &platform.RemoteAuthorizer{URL: metaServer.URL, ServiceToken: serviceToken, Client: metaServer.Client()}
	dataApp := data.NewHandler(data.Config{Engine: de, Authorizer: authorizer, SessionChecker: checker, MetadataURL: metaServer.URL, ServiceToken: serviceToken, CursorSecret: cursorSecret, HTTPClient: metaServer.Client()})
	dataServer := httptest.NewTLSServer(platform.ProtectSessionRoutes(serviceToken, false, checker, dataApp))
	t.Cleanup(dataServer.Close)
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
	metaMux.Handle("/", metadata.NewHandler(metadata.Config{Engine: me, Authorizer: accounts, SessionChecker: accounts, DataURL: dataServer.URL, ServiceToken: serviceToken, HTTPClient: dataServer.Client()}))
	roots := x509.NewCertPool()
	roots.AddCert(metaServer.Certificate())
	roots.AddCert(dataServer.Certificate())
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login := func(tenantID, username string) *publicSecurityHTTP {
		browser := &publicSecurityHTTP{t: t, client: client}
		var session c.Session
		headers := browser.request("POST", metaServer.URL+"/metadata/auth/login", c.LoginRequest{Tenant: tenantID, Username: username, Password: password}, nil, 200, &session)
		response := http.Response{Header: headers}
		browser.cookies, browser.csrf = response.Cookies(), session.CSRFToken
		if len(browser.cookies) != 1 || !browser.cookies[0].Secure || !browser.cookies[0].HttpOnly || browser.cookies[0].SameSite != http.SameSiteStrictMode || browser.cookies[0].Domain != "" || browser.cookies[0].Path != "/" || len(session.User.ID) > 200 || len(session.User.TenantID) > 200 || len(browser.csrf) == 0 || len(browser.csrf) > 200 {
			t.Fatal("public HTTPS session/Cookie contract failed")
		}
		browser.request("GET", metaServer.URL+"/metadata/auth/session", nil, nil, 200, nil)
		return browser
	}
	publish := func(tenant *publicSecurityTenant, category, group, name string) publicSecuritySchema {
		model := publicSecuritySchema{category: category, code: c.NewID(), department: c.NewID(), secret: c.NewID(), count: c.NewID(), amount: c.NewID(), immutable: c.NewID()}
		definition := c.Definition{CategoryID: category, GroupID: group, Kind: c.KindSchema, Name: name, Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{
			{ID: model.code, Name: "公开编码", DataType: "string", Required: true},
			{ID: model.department, Name: "所属区域", DataType: "string", Required: true},
			{ID: model.secret, Name: "隐藏机密字段", DataType: "string", Nullable: true},
			{ID: model.count, Name: "精确整数", DataType: "int64", Required: true},
			{ID: model.amount, Name: "精确金额", DataType: "decimal", Precision: 24, Scale: 3, Required: true},
			{ID: model.immutable, Name: "只读标记", DataType: "string", ReadOnly: true, Nullable: true},
		}}}
		var draft c.CurrentDefinition
		headers := tenant.admin.request("POST", metaServer.URL+"/metadata/definitions", definition, nil, 201, &draft)
		model.id = draft.Definition.ID
		tenant.admin.request("POST", metaServer.URL+"/metadata/definitions/"+model.id+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
		return model
	}
	savePolicy := func(tenant *publicSecurityTenant, change func(*c.PermissionPolicy)) {
		var policy c.PolicyRecord
		tenant.admin.request("GET", metaServer.URL+"/metadata/admin/policy", nil, nil, 200, &policy)
		change(&policy.Current)
		headers := tenant.admin.request("PUT", metaServer.URL+"/metadata/admin/policy", policy.Current, map[string]string{"If-Match": `"` + policy.ETag + `"`}, 200, nil)
		tenant.admin.request("POST", metaServer.URL+"/metadata/admin/policy/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	}
	for _, tenant := range tenants {
		tenant.admin = login(tenant.id, "admin")
		var category, group c.Organization
		tenant.admin.request("POST", metaServer.URL+"/metadata/categories", map[string]string{"name": "公网权限隔离"}, nil, 201, &category)
		tenant.admin.request("POST", metaServer.URL+"/metadata/groups", map[string]string{"name": "隔离模型", "categoryId": category.ID}, nil, 201, &group)
		tenant.models = []publicSecuritySchema{publish(tenant, category.ID, group.ID, "允许模型甲"), publish(tenant, category.ID, group.ID, "允许模型乙")}
		tenant.hidden = publish(tenant, category.ID, group.ID, "禁止发现模型")
		tenant.admin.request("POST", metaServer.URL+"/metadata/admin/roles", c.RoleInput{Name: "受限业务用户"}, nil, 201, &tenant.role)
		tenant.admin.request("POST", metaServer.URL+"/metadata/admin/users", c.UserInput{Username: "limited", Password: password, DisplayName: "受限测试用户", Enabled: true, RoleIDs: []string{tenant.role.ID}}, nil, 201, &tenant.user)
		savePolicy(tenant, func(policy *c.PermissionPolicy) {
			for _, model := range tenant.models {
				read := []string{model.code, model.department, model.count, model.amount, model.immutable}
				write := []string{model.code, model.department, model.count, model.amount}
				for _, operation := range []string{"instance.query", "instance.read", "instance.create", "instance.patch"} {
					rule := c.PermissionRule{ID: c.NewID(), RoleID: tenant.role.ID, Effect: "allow", Operation: operation, ResourceID: model.id, ReadFields: read, WriteFields: write}
					condition := &c.Expression{Op: "eq", Field: model.department, Value: raw("allowed")}
					if operation == "instance.create" {
						rule.NewRowFilter = condition
					} else {
						rule.RowFilter = condition
					}
					if operation == "instance.patch" {
						rule.NewRowFilter = condition
					}
					policy.Rules = append(policy.Rules, rule)
				}
			}
		})
		tenant.reader = login(tenant.id, "limited")
	}
	values := func(model publicSecuritySchema, code, area string) map[string]json.RawMessage {
		return map[string]json.RawMessage{model.code: raw(code), model.department: raw(area), model.count: raw("9007199254740993"), model.amount: raw("123456789012345.678")}
	}
	create := func(browser *publicSecurityHTTP, model publicSecuritySchema, code, area, key string, want int) c.Instance {
		var result c.Instance
		browser.request("POST", dataServer.URL+"/data/schemas/"+model.id+"/instances", c.WriteRequest{Values: values(model, code, area), IdempotencyKey: key}, nil, want, &result)
		return result
	}
	var hiddenRow c.Instance
	for _, tenant := range tenants {
		model := tenant.models[0]
		create(tenant.admin, model, "visible-1", "allowed", c.NewID(), 201)
		create(tenant.admin, model, "visible-2", "allowed", c.NewID(), 201)
		row := create(tenant.admin, model, "hidden-row", "forbidden", c.NewID(), 201)
		if tenant == tenants[0] {
			hiddenRow = row
		}
		var directory c.QueryContext
		tenant.reader.request("GET", dataServer.URL+"/data/query/context?limit=50", nil, nil, 200, &directory)
		if len(directory.Models) != 2 {
			t.Fatal("business-only discovery did not isolate permitted models")
		}
		for _, discovered := range directory.Models {
			if len(discovered.Fields) != 5 || discovered.SchemaID == tenant.hidden.id {
				t.Fatal("directory leaked forbidden model or fields")
			}
			for _, field := range discovered.Fields {
				if field.Name == "隐藏机密字段" {
					t.Fatal("directory leaked hidden field metadata")
				}
			}
		}
		var form c.FormDefinition
		tenant.reader.request("GET", dataServer.URL+"/data/schemas/"+model.id+"/form", nil, nil, 200, &form)
		if !form.CreateAllowed || len(form.Definition.Schema.Properties) != 5 {
			t.Fatal("form-safe discovery did not preserve permitted field contract")
		}
		for _, field := range form.Definition.Schema.Properties {
			if field.ID == model.secret || field.ID == model.immutable && !field.ReadOnly {
				t.Fatal("form discovery exposed hidden or writable immutable field")
			}
		}
		tenant.reader.request("GET", metaServer.URL+"/metadata/definitions/"+model.id, nil, nil, 404, nil)
		tenant.reader.request("GET", dataServer.URL+"/data/schemas/"+tenant.hidden.id+"/form", nil, nil, 403, nil)
	}
	a, b := tenants[0], tenants[1]
	model := a.models[0]
	query := c.QuerySpec{From: c.Source{SchemaID: model.id}, Select: []string{model.code, model.count, model.amount}, OrderBy: []c.Order{{Field: model.code, Direction: "asc"}}, Page: c.Page{Limit: 1}}
	var page c.QueryResult
	a.reader.request("POST", dataServer.URL+"/data/query", query, map[string]string{"X-Tenant-ID": b.id, "X-User-ID": b.user.ID, "Authorization": "Bearer invented-service-identity"}, 200, &page)
	if len(page.Data) != 1 || !page.HasMore || page.NextCursor == "" || string(page.Data[0].Values[model.count]) != `"9007199254740993"` || string(page.Data[0].Values[model.amount]) != `"123456789012345.678"` {
		t.Fatal("authorized query, tenant identity or exact numeric round trip failed")
	}
	dataCursor := page.NextCursor
	query.Page.Cursor = dataCursor
	a.reader.request("POST", dataServer.URL+"/data/query", query, nil, 200, &page)
	if len(page.Data) != 1 || page.HasMore {
		t.Fatal("row filter or pagination skipped/added a business row")
	}
	a.admin.request("POST", dataServer.URL+"/data/query", query, nil, 422, nil)
	crossTenant := query
	crossTenant.From.SchemaID = b.models[0].id
	crossTenant.Select = []string{b.models[0].code}
	crossTenant.OrderBy = nil
	b.reader.request("POST", dataServer.URL+"/data/query", crossTenant, nil, 422, nil)
	query.Page.Cursor += "tampered"
	a.reader.request("POST", dataServer.URL+"/data/query", query, nil, 422, nil)
	query.Page.Cursor = ""
	query.Select = []string{model.secret}
	a.reader.request("POST", dataServer.URL+"/data/query", query, nil, 403, nil)
	query.Select = []string{model.code}
	query.Where = &c.Expression{Op: "eq", Field: model.secret, Value: raw("probe")}
	a.reader.request("POST", dataServer.URL+"/data/query", query, nil, 403, nil)
	query.Where = nil
	query.OrderBy = []c.Order{{Field: model.secret, Direction: "asc"}}
	a.reader.request("POST", dataServer.URL+"/data/query", query, nil, 403, nil)
	query.OrderBy = nil
	query.From.SchemaID = a.hidden.id
	a.reader.request("POST", dataServer.URL+"/data/query", query, nil, 403, nil)
	query.From.SchemaID = model.id
	b.admin.request("POST", dataServer.URL+"/data/query", query, nil, 404, nil)
	itemURL := dataServer.URL + "/data/schemas/" + model.id + "/instances/" + hiddenRow.ID
	a.reader.request("GET", itemURL, nil, nil, 404, nil)
	b.admin.request("GET", itemURL, nil, nil, 404, nil)
	var directoryPage c.QueryContext
	a.reader.request("GET", dataServer.URL+"/data/query/context?limit=1", nil, nil, 200, &directoryPage)
	if len(directoryPage.Models) != 1 || directoryPage.NextCursor == "" {
		t.Fatal("directory did not provide signed continuation")
	}
	directoryCursor := directoryPage.NextCursor
	continuation := dataServer.URL + "/data/query/context?limit=1&cursor=" + url.QueryEscape(directoryCursor)
	directoryPage = c.QueryContext{}
	a.reader.request("GET", continuation, nil, nil, 200, &directoryPage)
	if len(directoryPage.Models) != 1 || directoryPage.NextCursor != "" {
		t.Fatalf("directory continuation models=%d hasCursor=%v", len(directoryPage.Models), directoryPage.NextCursor != "")
	}
	a.admin.request("GET", continuation, nil, nil, 422, nil)
	b.reader.request("GET", continuation, nil, nil, 422, nil)
	a.reader.request("GET", continuation+"x", nil, nil, 422, nil)
	a.reader.request("GET", continuation+"&search=changed", nil, nil, 422, nil)
	intent := c.NewID()
	created := create(a.reader, model, "limited-create", "allowed", intent, 201)
	replay := create(a.reader, model, "limited-create", "allowed", intent, 200)
	if created.ID != replay.ID || len(replay.Values) != 5 {
		t.Fatal("idempotent replay lost identity or field projection")
	}
	adminCreated := create(a.admin, model, "limited-create", "allowed", intent, 201)
	otherCreated := create(b.reader, b.models[0], "limited-create", "allowed", intent, 201)
	if created.ID == adminCreated.ID || created.ID == otherCreated.ID {
		t.Fatal("idempotent key crossed user or tenant boundary")
	}
	a.reader.request("POST", dataServer.URL+"/data/schemas/"+model.id+"/instances", c.WriteRequest{Values: values(model, "changed-intent", "allowed"), IdempotencyKey: intent}, nil, 409, nil)
	for field, value := range map[string]json.RawMessage{model.secret: raw("private"), model.immutable: raw("injected")} {
		body := values(model, "forbidden-field", "allowed")
		body[field] = value
		a.reader.request("POST", dataServer.URL+"/data/schemas/"+model.id+"/instances", c.WriteRequest{Values: body, IdempotencyKey: c.NewID()}, nil, 403, nil)
	}
	for field, value := range map[string]json.RawMessage{model.count: json.RawMessage(`9007199254740993`), model.amount: json.RawMessage(`1.234`)} {
		body := values(model, "invalid-number", "allowed")
		body[field] = value
		a.reader.request("POST", dataServer.URL+"/data/schemas/"+model.id+"/instances", c.WriteRequest{Values: body, IdempotencyKey: c.NewID()}, nil, 422, nil)
	}
	a.reader.request("POST", dataServer.URL+"/data/schemas/"+model.id+"/instances", c.WriteRequest{Values: values(model, "bad-row", "forbidden"), IdempotencyKey: c.NewID()}, nil, 403, nil)
	a.reader.request("POST", dataServer.URL+"/data/schemas/"+a.hidden.id+"/instances", c.WriteRequest{Values: values(a.hidden, "bad-model", "allowed"), IdempotencyKey: c.NewID()}, nil, 403, nil)
	a.reader.request("POST", dataServer.URL+"/data/schemas/"+model.id+"/instances", map[string]any{"values": values(model, "injected-tenant", "allowed"), "idempotencyKey": c.NewID(), "tenantId": b.id}, nil, 400, nil)
	for _, origin := range []string{"", "null", "https://attacker.test", "https://security.test"} {
		a.reader.request("POST", dataServer.URL+"/data/query", query, map[string]string{"Origin": origin}, 403, nil)
		(&publicSecurityHTTP{t: t, client: client}).request("POST", metaServer.URL+"/metadata/auth/login", c.LoginRequest{Tenant: a.id, Username: "admin", Password: password}, map[string]string{"Origin": origin}, 403, nil)
	}
	for _, csrf := range []string{"", strings.Repeat("x", 64)} {
		a.reader.request("POST", dataServer.URL+"/data/query", query, map[string]string{"X-CSRF-Token": csrf}, 403, nil)
	}
	for _, cookie := range []string{"ontology_session=", "ontology_session=" + a.reader.cookies[0].Value + "; ontology_session=" + a.reader.cookies[0].Value, "ontology_session=" + a.reader.cookies[0].Value + `; ontology_session="unclosed`} {
		a.reader.request("GET", dataServer.URL+"/data/query/context", nil, map[string]string{"Cookie": cookie}, 401, nil)
		a.reader.request("GET", metaServer.URL+"/metadata/auth/session", nil, map[string]string{"Cookie": cookie}, 401, nil)
	}
	a.reader.request("GET", dataServer.URL+"/data/query/context?limit=51", nil, nil, 400, nil)
	query.Page.Limit = 101
	a.reader.request("POST", dataServer.URL+"/data/query", query, nil, 422, nil)
	query.Page.Limit = 1
	// An io.Reader wrapper hides Len; net/http sends this body with chunked
	// transfer encoding rather than a Content-Length declaration.
	a.reader.rawRequest("POST", dataServer.URL+"/data/query", io.LimitReader(strings.NewReader(strings.Repeat(" ", c.MaxDataRequestBytes+1)), int64(c.MaxDataRequestBytes+1)), nil, 413, nil)
	// Exercise all eight built-in writes through the public authority.
	{
		for _, tenant := range tenants {
			var base c.CurrentDefinition
			tenant.admin.request("GET", metaServer.URL+"/metadata/definitions/"+tenant.models[0].id, nil, nil, 200, &base)
			definition := base.Definition
			definition.ID, definition.Name = "", "八写接口隔离验收"
			definition.Schema.Indexes = []c.Index{{ID: c.NewID(), Name: "编码唯一", Unique: true, FieldIDs: []string{tenant.models[0].code}}}
			var draft c.CurrentDefinition
			headers := tenant.admin.request("POST", metaServer.URL+"/metadata/definitions", definition, nil, 201, &draft)
			writeModel := tenant.models[0]
			writeModel.id = draft.Definition.ID
			tenant.admin.request("POST", metaServer.URL+"/metadata/definitions/"+writeModel.id+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
			baseURL := dataServer.URL + "/data/schemas/" + writeModel.id
			var capabilities c.WriteCapabilities
			tenant.admin.request("GET", baseURL+"/write-capabilities", nil, nil, 200, &capabilities)
			if len(capabilities.Operations) != 8 || !capabilities.OverwriteAllowed || capabilities.MaxBulkItems != 100 {
				t.Fatal("administrator eight-write discovery is incomplete")
			}
			savePolicy(tenant, func(policy *c.PermissionPolicy) {
				for _, operation := range []string{"instance.read", "instance.create", "instance.patch", "instance.delete", "instance.upsert"} {
					condition := &c.Expression{Op: "eq", Field: writeModel.department, Value: raw("allowed")}
					rule := c.PermissionRule{ID: c.NewID(), RoleID: tenant.role.ID, Effect: "allow", Operation: operation, ResourceID: writeModel.id,
						ReadFields:  []string{writeModel.code, writeModel.department, writeModel.count, writeModel.amount},
						WriteFields: []string{writeModel.code, writeModel.department, writeModel.count, writeModel.amount}, RowFilter: condition}
					if operation == "instance.create" || operation == "instance.patch" || operation == "instance.upsert" {
						rule.NewRowFilter = condition
					}
					policy.Rules = append(policy.Rules, rule)
				}
			})
			tenant.reader.request("GET", baseURL+"/write-capabilities", nil, nil, 200, &capabilities)
			if capabilities.OverwriteAllowed {
				t.Fatal("ordinary upsert authority granted overwrite")
			}
			for _, field := range capabilities.Fields {
				if field.ID == writeModel.secret {
					t.Fatal("write capability discovery leaked hidden field metadata")
				}
			}
			created := create(tenant.reader, writeModel, "single", "allowed", c.NewID(), 201)
			var updated c.Instance
			tenant.reader.request("PATCH", baseURL+"/instances/"+created.ID, c.WriteRequest{Values: map[string]json.RawMessage{writeModel.count: raw("9007199254740994")}}, map[string]string{"If-Match": `"` + created.ETag + `"`}, 200, &updated)
			tenant.reader.request("DELETE", baseURL+"/instances/"+created.ID, nil, map[string]string{"If-Match": `"` + updated.ETag + `"`}, 204, nil)
			upsert := c.UpsertRequest{Match: c.WriteMatch{IndexID: definition.Schema.Indexes[0].ID, Values: map[string]json.RawMessage{writeModel.code: raw("upsert")}}, Values: values(writeModel, "upsert", "allowed"), IdempotencyKey: c.NewID()}
			var receipt c.WriteResult
			tenant.reader.request("POST", baseURL+"/instances/upsert", upsert, nil, 201, &receipt)
			if receipt.Outcome != "created" || receipt.ID == "" {
				t.Fatal("upsert create receipt lost its outcome or identity")
			}
			upsert.IdempotencyKey = c.NewID()
			tenant.reader.request("POST", baseURL+"/instances/upsert", upsert, nil, 428, nil)
			upsert.Values = map[string]json.RawMessage{writeModel.count: raw("9007199254740995")}
			tenant.reader.request("POST", baseURL+"/instances/upsert", upsert, map[string]string{"If-Match": `"` + receipt.ETag + `"`}, 200, &receipt)
			upsert.ConflictPolicy, upsert.IdempotencyKey = "overwrite", c.NewID()
			tenant.reader.request("POST", baseURL+"/instances/upsert", upsert, nil, 403, nil)
			tenant.admin.request("POST", baseURL+"/instances/upsert", upsert, nil, 200, &receipt)
			bulk := c.BulkWriteRequest{IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{
				{ClientKey: "first", Values: values(writeModel, "bulk-1", "allowed")},
				{ClientKey: "second", Values: values(writeModel, "bulk-2", "allowed")},
			}}
			var batch c.BulkWriteResult
			tenant.reader.request("POST", baseURL+"/instances/bulk-create", bulk, nil, 200, &batch)
			if !batch.Atomic || !batch.Committed || len(batch.Results) != 2 {
				t.Fatal("bulk create receipt lost atomic commit or items")
			}
			tenant.reader.request("POST", baseURL+"/instances/bulk-create", bulk, nil, 200, &batch)
			bulk.IdempotencyKey = c.NewID()
			bulk.Items = []c.BulkWriteItem{}
			for _, item := range batch.Results {
				bulk.Items = append(bulk.Items, c.BulkWriteItem{ClientKey: item.ClientKey, ID: item.ID, IfMatch: `"` + item.ETag + `"`, Values: map[string]json.RawMessage{writeModel.count: raw("9007199254740996")}})
			}
			tenant.reader.request("POST", baseURL+"/instances/bulk-update", bulk, nil, 200, &batch)
			bulk.IdempotencyKey, bulk.Items = c.NewID(), []c.BulkWriteItem{}
			for _, item := range batch.Results {
				bulk.Items = append(bulk.Items, c.BulkWriteItem{ClientKey: item.ClientKey, ID: item.ID, IfMatch: `"` + item.ETag + `"`})
			}
			tenant.reader.request("POST", baseURL+"/instances/bulk-delete", bulk, nil, 200, &batch)
			bulk.IdempotencyKey, bulk.ConflictPolicy = c.NewID(), "check"
			bulk.Items = []c.BulkWriteItem{{ClientKey: "upsert", Match: &c.WriteMatch{ID: c.NewID()}, Values: values(writeModel, "bulk-upsert", "allowed")}}
			tenant.reader.request("POST", baseURL+"/instances/bulk-upsert", bulk, nil, 200, &batch)
			if batch.Results[0].Outcome != "created" || string(batch.Results[0].Values[writeModel.count]) != `"9007199254740993"` {
				t.Fatal("bulk upsert lost outcome or integer precision")
			}
			bulk.IdempotencyKey, bulk.ConflictPolicy = c.NewID(), ""
			bulk.Items = []c.BulkWriteItem{
				{ClientKey: "would-write", Values: values(writeModel, "rolled-back", "allowed")},
				{ClientKey: "forbidden", Values: values(writeModel, "blocked", "forbidden")},
			}
			tenant.reader.request("POST", baseURL+"/instances/bulk-create", bulk, nil, 403, nil)
			// The first item must have rolled back, including its unique key.
			create(tenant.reader, writeModel, "rolled-back", "allowed", c.NewID(), 201)
			tenant.reader.request("POST", baseURL+"/instances/bulk-create", bulk, map[string]string{"X-CSRF-Token": ""}, 403, nil)
			tenant.reader.request("POST", baseURL+"/instances/bulk-create", bulk, map[string]string{"Origin": "https://attacker.test"}, 403, nil)
			other := tenants[0]
			if tenant == other {
				other = tenants[1]
			}
			other.reader.request("GET", baseURL+"/write-capabilities", nil, nil, 404, nil)
		}
	}
	// Policy publication changes permissions immediately for an existing Cookie;
	// session revocation itself is exercised separately through user role edits.
	savePolicy(a, func(policy *c.PermissionPolicy) {
		for i := range policy.Rules {
			rule := &policy.Rules[i]
			if rule.RoleID == a.role.ID {
				filtered := []string{}
				for _, id := range rule.ReadFields {
					if id != model.count {
						filtered = append(filtered, id)
					}
				}
				rule.ReadFields = filtered
			}
		}
	})
	query.Select = []string{model.count}
	a.reader.request("POST", dataServer.URL+"/data/query", query, nil, 403, nil)
	a.reader.request("GET", continuation, nil, nil, 422, nil)
	expiring := login(a.id, "limited")
	sum := sha256.Sum256([]byte(expiring.cookies[0].Value))
	if _, err := me.DB().ExecContext(ctx, "UPDATE af_identity_sessions SET expires_at=? WHERE tenant_id=? AND token_hash=?", time.Now().Add(-time.Second).Unix(), a.id, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal("expire only the isolated test session")
	}
	expiring.request("GET", dataServer.URL+"/data/query/context", nil, nil, 401, nil)
	updateUser := func(enabled bool, roles []string) {
		a.admin.request("PUT", metaServer.URL+"/metadata/admin/users/"+a.user.ID, c.UserInput{Username: a.user.Username, DisplayName: a.user.DisplayName, Enabled: enabled, RoleIDs: roles}, map[string]string{"If-Match": `"` + a.user.ETag + `"`}, 200, &a.user)
	}
	updateUser(true, []string{})
	a.reader.request("GET", metaServer.URL+"/metadata/auth/session", nil, nil, 401, nil)
	a.reader.request("POST", dataServer.URL+"/data/query", query, nil, 401, nil)
	updateUser(true, []string{a.role.ID})
	a.reader = login(a.id, "limited")
	updateUser(false, []string{a.role.ID})
	a.reader.request("GET", dataServer.URL+"/data/query/context", nil, nil, 401, nil)
	b.reader.request("POST", metaServer.URL+"/metadata/auth/logout", nil, nil, 204, nil)
	b.reader.request("GET", metaServer.URL+"/metadata/auth/session", nil, nil, 401, nil)
	b.reader.request("POST", dataServer.URL+"/data/schemas/"+b.models[0].id+"/instances", c.WriteRequest{Values: values(b.models[0], "after-logout", "allowed"), IdempotencyKey: c.NewID()}, nil, 401, nil)
	t.Log("two tenants, four users, real TLS/Cookie/CSRF + MySQL/OPA: discovery, safe forms, row/field/write boundaries, precise numbers, idempotency, cursor context, input budgets and current revocation verified")
}
