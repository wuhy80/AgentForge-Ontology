package integration_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	data "github.com/wuhy80/AgentForge-Ontology/services/data"
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

// Public TLS/Cookie/CSRF requests exercise the definition coordinator and actual
// typed MySQL tables; direct SQL is confined to fixture setup and cleanup.
func TestModelEvolutionSessionsAndOPAEndToEnd(t *testing.T) {
	mdsn, ddsn, opa := os.Getenv("ONTOLOGY_TEST_METADATA_DSN"), os.Getenv("ONTOLOGY_TEST_DATA_DSN"), os.Getenv("ONTOLOGY_TEST_OPA_URL")
	if mdsn == "" || ddsn == "" || opa == "" {
		t.Skip("set isolated ONTOLOGY_TEST_METADATA_DSN, ONTOLOGY_TEST_DATA_DSN and ONTOLOGY_TEST_OPA_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	open := func(dsn string) *storage.Engine {
		engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 12, MaxIdle: 3})
		if err != nil {
			t.Fatal("open isolated model evolution database")
		}
		t.Cleanup(func() { _ = engine.Close() })
		return engine
	}
	me, de := open(mdsn), open(ddsn)
	if err := storage.Migrate(ctx, me.DB(), "metadata", append(append([]storage.Migration{}, metadata.Migrations...), identity.Migrations...)); err != nil {
		t.Fatal("explicit metadata fixture migration")
	}
	if err := storage.Migrate(ctx, de.DB(), "data", data.Migrations); err != nil {
		t.Fatal("explicit data fixture migration")
	}
	tenant, foreign := "model-evolution-"+c.NewID(), "model-evolution-foreign-"+c.NewID()
	t.Cleanup(func() {
		for _, id := range []string{tenant, foreign} {
			cleanupOntologyTenant(t, me, de, id)
			for _, table := range []string{"af_identity_user_group_members", "af_identity_user_groups", "af_identity_sessions", "af_identity_audit", "af_identity_users", "af_identity_roles", "af_identity_tenants"} {
				if _, err := me.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=?", id); err != nil {
					t.Error("clean isolated model evolution identity")
				}
			}
		}
	})
	const password = "ModelEvolutionFixture!2026"
	accounts := identity.NewStore(me.DB(), &platform.OPAAuthorizer{URL: opa}, publicSecurityOrigin)
	for _, id := range []string{tenant, foreign} {
		if err := accounts.Bootstrap(ctx, id, "admin", password); err != nil {
			t.Fatal("bootstrap isolated model evolution tenant")
		}
	}
	metaMux := http.NewServeMux()
	metaServer := httptest.NewTLSServer(platform.ProtectSessionRoutes(serviceToken, true, accounts, metaMux))
	t.Cleanup(metaServer.Close)
	checker := &platform.RemoteSessionChecker{URL: metaServer.URL, ServiceToken: serviceToken, Client: metaServer.Client()}
	authorizer := &platform.RemoteAuthorizer{URL: metaServer.URL, ServiceToken: serviceToken, Client: metaServer.Client()}
	dataServer := httptest.NewTLSServer(platform.ProtectSessionRoutes(serviceToken, false, checker, data.NewHandler(data.Config{Engine: de, Authorizer: authorizer, SessionChecker: checker, MetadataURL: metaServer.URL, ServiceToken: serviceToken, CursorSecret: cursorSecret, HTTPClient: metaServer.Client()})))
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
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	login := func(id string) *publicSecurityHTTP {
		browser := &publicSecurityHTTP{t: t, client: client}
		var session c.Session
		headers := browser.request("POST", metaServer.URL+"/metadata/auth/login", c.LoginRequest{Tenant: id, Username: "admin", Password: password}, nil, 200, &session)
		response := http.Response{Header: headers}
		browser.cookies, browser.csrf = response.Cookies(), session.CSRFToken
		return browser
	}
	admin, outsider := login(tenant), login(foreign)
	var category, group c.Organization
	admin.request("POST", metaServer.URL+"/metadata/categories", map[string]string{"name": "模型演进"}, nil, 201, &category)
	admin.request("POST", metaServer.URL+"/metadata/groups", map[string]string{"name": "隔离验收", "categoryId": category.ID}, nil, 201, &group)
	definitionURL := func(id string) string { return metaServer.URL + "/metadata/definitions/" + id }
	publish := func(d c.Definition) c.CurrentDefinition {
		d.CategoryID, d.GroupID = category.ID, group.ID
		var draft, current c.CurrentDefinition
		headers := admin.request("POST", metaServer.URL+"/metadata/definitions", d, nil, 201, &draft)
		admin.request("POST", definitionURL(draft.Definition.ID)+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
		admin.request("GET", definitionURL(draft.Definition.ID), nil, nil, 200, &current)
		return current
	}
	definitionInput := func(d c.Definition) c.Definition {
		encoded, err := json.Marshal(d)
		if err != nil {
			t.Fatal("copy definition input")
		}
		var copy c.Definition
		if json.Unmarshal(encoded, &copy) != nil {
			t.Fatal("decode definition input")
		}
		for _, property := range c.DefinitionProperties(&copy) {
			property.EnumValues, property.EnumETag = nil, ""
		}
		return copy
	}
	update := func(d c.CurrentDefinition, candidate c.Definition, publishStatus int) c.CurrentDefinition {
		var draft c.CurrentDefinition
		headers := admin.request("PUT", definitionURL(d.Definition.ID)+"/draft", definitionInput(candidate), map[string]string{"If-Match": `"` + d.ETag + `"`}, 200, &draft)
		admin.request("POST", definitionURL(d.Definition.ID)+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, publishStatus, nil)
		if publishStatus == 200 {
			admin.request("GET", definitionURL(d.Definition.ID), nil, nil, 200, &draft)
		}
		return draft
	}
	states := publish(c.Definition{Kind: c.KindEnum, Name: "工单状态", Enum: &c.Enum{Values: []c.EnumValue{{Code: "ready", Label: "待办"}, {Code: "legacy", Label: "旧状态"}}}})
	code, state, note := c.NewID(), c.NewID(), c.NewID()
	schema := publish(c.Definition{Kind: c.KindSchema, Name: "工单", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{ID: code, Name: "代码", DataType: "string", Required: true}, {ID: state, Name: "状态", DataType: "string", Required: true, EnumID: states.Definition.ID}, {ID: note, Name: "备注", DataType: "string", Nullable: true}}}})
	baseURL := dataServer.URL + "/data/schemas/" + schema.Definition.ID + "/instances"
	create := func(label, value string, want int) c.Instance {
		var item c.Instance
		admin.request("POST", baseURL, c.WriteRequest{IdempotencyKey: c.NewID(), Values: map[string]json.RawMessage{code: raw(label), state: raw(value)}}, nil, want, &item)
		return item
	}
	legacy := create("old", "legacy", 201)
	create("new", "ready", 201)
	create("invalid", "unknown", 422)
	outsider.request("GET", baseURL+"/"+legacy.ID, nil, nil, 404, nil)
	var form c.FormDefinition
	admin.request("GET", dataServer.URL+"/data/schemas/"+schema.Definition.ID+"/form", nil, nil, 200, &form)
	if len(form.Definition.Schema.Properties) != 3 || len(form.Definition.Schema.Properties[1].EnumValues) != 2 || form.Definition.Schema.Properties[1].EnumValues[0].Label != "待办" {
		t.Fatal("minimal form omitted current authorized enum options")
	}
	forged := definitionInput(schema.Definition)
	forged.ID, forged.Name = "", "伪造枚举上下文"
	forged.Schema.Properties[1].EnumValues = []c.EnumValue{{Code: "attacker", Label: "客户端伪造"}}
	forged.Schema.Properties[1].EnumETag = "client-controlled"
	admin.request("POST", metaServer.URL+"/metadata/definitions", forged, nil, 400, nil)
	query := c.QuerySpec{From: c.Source{SchemaID: schema.Definition.ID}, Select: []string{code, state}, Page: c.Page{Limit: 1}}
	var page c.QueryResult
	admin.request("POST", dataServer.URL+"/data/query", query, nil, 200, &page)
	if !page.HasMore || page.NextCursor == "" {
		t.Fatal("enum pagination did not produce a continuation")
	}
	oldCursor := page.NextCursor
	// An enum edit changes dependent query semantics even when Schema IDs and
	// Schema content remain unchanged.
	nextStates := states.Definition
	nextStates.Enum = &c.Enum{Values: []c.EnumValue{{Code: "ready", Label: "待处理"}, {Code: "legacy", Label: "旧状态", Deprecated: true}, {Code: "done", Label: "已完成"}}}
	states = update(states, nextStates, 200)
	query.Page.Cursor = oldCursor
	admin.request("POST", dataServer.URL+"/data/query", query, nil, 422, nil)
	var kept c.Instance
	admin.request("GET", baseURL+"/"+legacy.ID, nil, nil, 200, &kept)
	if string(kept.Values[state]) != `"legacy"` {
		t.Fatal("deprecated enum code lost its stored identity")
	}
	admin.request("PATCH", baseURL+"/"+legacy.ID, c.WriteRequest{Values: map[string]json.RawMessage{note: raw("保留旧状态")}}, map[string]string{"If-Match": `"` + kept.ETag + `"`}, 200, &kept)
	create("deprecated", "legacy", 422)
	create("completed", "done", 201)
	// Field append and default backfill must preserve object identity and make
	// previously fetched concurrency tokens stale.
	priority := c.NewID()
	candidate := definitionInput(schema.Definition)
	copiedSchema := *candidate.Schema
	copiedSchema.Properties = append(append([]c.Property{}, copiedSchema.Properties...), c.Property{ID: priority, Name: "优先级", DataType: "int64", Required: true, Default: raw("9007199254740993")})
	copiedSchema.Indexes = []c.Index{{ID: c.NewID(), Name: "代码唯一", Unique: true, FieldIDs: []string{code}}}
	candidate.Schema = &copiedSchema
	var draft c.CurrentDefinition
	headers := admin.request("PUT", definitionURL(schema.Definition.ID)+"/draft", candidate, map[string]string{"If-Match": `"` + schema.ETag + `"`}, 200, &draft)
	var preview c.PrepareResult
	admin.request("POST", definitionURL(schema.Definition.ID)+"/preview", nil, nil, 200, &preview)
	if len(preview.Statements) == 0 {
		t.Fatal("structure preview omitted migration statements")
	}
	var before c.Instance
	admin.request("GET", baseURL+"/"+legacy.ID, nil, nil, 200, &before)
	if _, exists := before.Values[priority]; exists || before.ETag != kept.ETag {
		t.Fatal("read-only preview altered an existing object")
	}
	admin.request("POST", definitionURL(schema.Definition.ID)+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	admin.request("GET", baseURL+"/"+legacy.ID, nil, nil, 200, &kept)
	if kept.ID != legacy.ID || string(kept.Values[priority]) != `"9007199254740993"` || kept.ETag == before.ETag {
		t.Fatal("migration lost identity, precise default or concurrency invalidation")
	}
	admin.request("PATCH", baseURL+"/"+legacy.ID, c.WriteRequest{Values: map[string]json.RawMessage{note: raw("过期编辑")}}, map[string]string{"If-Match": `"` + before.ETag + `"`}, 412, nil)
	create("new", "ready", 409)
	// Removed codes and destructive schema edits remain explicit rejections.
	removed := states.Definition
	removed.Enum = &c.Enum{Values: []c.EnumValue{{Code: "ready", Label: "待处理"}, {Code: "done", Label: "已完成"}}}
	update(states, removed, 422)
	admin.request("GET", definitionURL(schema.Definition.ID), nil, nil, 200, &schema)
	destructive := schema.Definition
	changedSchema := *destructive.Schema
	changedSchema.Properties = append([]c.Property{}, changedSchema.Properties...)
	changedSchema.Properties[0].DataType = "boolean"
	destructive.Schema = &changedSchema
	update(schema, destructive, 422)
	admin.request("GET", baseURL+"/"+legacy.ID, nil, nil, 200, nil)
}
