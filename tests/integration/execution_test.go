package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	data "github.com/wuhy80/AgentForge-Ontology/services/data"
	execution "github.com/wuhy80/AgentForge-Ontology/services/execution"
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

const executionOrigin = "https://127.0.0.1:5173"

type executionHTTP struct {
	t       *testing.T
	cookies []*http.Cookie
	csrf    string
	client  *http.Client
}

func (h *executionHTTP) request(method, endpoint string, body any, headers map[string]string, want int, out any) http.Header {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	r, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	r.Host = "127.0.0.1:5173"
	r.Header.Set("Origin", executionOrigin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", h.csrf)
	for _, cookie := range h.cookies {
		r.AddCookie(cookie)
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	client := h.client
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	res, err := client.Do(r)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		h.t.Fatal(err)
	}
	if res.StatusCode != want {
		h.t.Fatalf("%s %s: got %d want %d: %s", method, r.URL.Path, res.StatusCode, want, encoded)
	}
	if out != nil && json.Unmarshal(encoded, out) != nil {
		h.t.Fatalf("invalid response: %s", encoded)
	}
	return res.Header
}

func executionInternal(service string, action func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !platform.EqualSecret(platform.Bearer(r), service) {
			platform.WriteError(w, 401, "service_unauthenticated", "需要服务身份")
			return
		}
		action(w, r)
	})
}

// Uses the real account store, current OPA policy, public cookie/CSRF routes,
// three independent MySQL databases and actual restricted language containers.
func TestCallableExecutionSessionsAndOPAEndToEnd(t *testing.T) {
	mdsn, ddsn, edsn := os.Getenv("ONTOLOGY_TEST_METADATA_DSN"), os.Getenv("ONTOLOGY_TEST_DATA_DSN"), os.Getenv("ONTOLOGY_TEST_EXECUTION_DSN")
	opaURL := os.Getenv("ONTOLOGY_TEST_OPA_URL")
	if mdsn == "" || ddsn == "" || edsn == "" || opaURL == "" || os.Getenv("EXECUTION_TEST_DOCKER") != "1" {
		t.Skip("set three ONTOLOGY_TEST_*_DSN values, ONTOLOGY_TEST_OPA_URL and EXECUTION_TEST_DOCKER=1 for real callable/session/OPA integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	open := func(dsn string) *storage.Engine {
		e, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 6, MaxIdle: 2})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.Close() })
		return e
	}
	me, de, ee := open(mdsn), open(ddsn), open(edsn)
	for _, set := range []struct {
		engine     *storage.Engine
		name       string
		migrations []storage.Migration
	}{
		{me, "metadata", append(append([]storage.Migration{}, metadata.Migrations...), identity.Migrations...)},
		{de, "data", data.Migrations}, {ee, "execution", execution.Migrations},
	} {
		if err := storage.Migrate(ctx, set.engine.DB(), set.name, set.migrations); err != nil {
			t.Fatal(err)
		}
	}
	tenant, otherTenant := "python-e2e-"+c.NewID(), "python-other-"+c.NewID()
	t.Cleanup(func() {
		for _, tenantID := range []string{tenant, otherTenant} {
			if _, err := ee.DB().Exec("DELETE FROM af_execution_invocations WHERE tenant_id=?", tenantID); err != nil {
				t.Error(err)
			}
			cleanupOntologyTenant(t, me, de, tenantID)
			for _, table := range []string{"af_identity_user_group_members", "af_identity_user_groups", "af_identity_sessions", "af_identity_audit", "af_identity_users", "af_identity_roles", "af_identity_tenants"} {
				if _, err := me.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenantID); err != nil {
					t.Error(err)
				}
			}
		}
	})
	opa := &platform.OPAAuthorizer{URL: opaURL}
	accounts := identity.NewStore(me.DB(), opa, executionOrigin)
	const password = "integration-python-password-123"
	for _, tenantID := range []string{tenant, otherTenant} {
		if err := accounts.Bootstrap(ctx, tenantID, "admin", password); err != nil {
			t.Fatal(err)
		}
	}
	metaMux := http.NewServeMux()
	metaServer := httptest.NewTLSServer(platform.ProtectSessionRoutes(serviceToken, true, accounts, metaMux))
	defer metaServer.Close()
	dataServer := httptest.NewTLSServer(platform.ProtectSessionRoutes(serviceToken, false, accounts, data.NewHandler(data.Config{Engine: de, Authorizer: accounts, SessionChecker: accounts, MetadataURL: metaServer.URL, ServiceToken: serviceToken, CursorSecret: cursorSecret, HTTPClient: metaServer.Client()})))
	defer dataServer.Close()
	metaMux.Handle("/metadata/auth/", accounts.NewHandler())
	metaMux.Handle("/metadata/admin/", accounts.NewHandler())
	metaMux.Handle("POST /internal/identity/session", executionInternal(serviceToken, func(w http.ResponseWriter, r *http.Request) {
		var check c.SessionCheck
		if json.NewDecoder(r.Body).Decode(&check) != nil {
			platform.WriteError(w, 400, "invalid_json", "无效请求")
			return
		}
		result, err := accounts.CheckSession(r.Context(), check)
		if err != nil {
			identity.WriteError(w, err)
			return
		}
		platform.WriteJSON(w, 200, result)
	}))
	metaMux.Handle("POST /internal/authorization/decision", executionInternal(serviceToken, func(w http.ResponseWriter, r *http.Request) {
		var check c.AuthorizationCheck
		if json.NewDecoder(r.Body).Decode(&check) != nil {
			platform.WriteError(w, 400, "invalid_json", "无效请求")
			return
		}
		result, err := accounts.Authorize(r.Context(), check.Principal, check.Request)
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
	browserClient := &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login := func(tenantID, username string) (*executionHTTP, c.Session) {
		h := &executionHTTP{t: t, client: browserClient}
		var session c.Session
		headers := h.request("POST", metaServer.URL+"/metadata/auth/login", c.LoginRequest{Tenant: tenantID, Username: username, Password: password}, nil, 200, &session)
		response := http.Response{Header: headers}
		h.cookies, h.csrf = response.Cookies(), session.CSRFToken
		return h, session
	}
	admin, session := login(tenant, "admin")
	other, _ := login(otherTenant, "admin")
	checker := &platform.RemoteSessionChecker{URL: metaServer.URL, ServiceToken: serviceToken, Client: metaServer.Client()}
	image := os.Getenv("ONTOLOGY_PYTHON_IMAGE")
	if image == "" {
		image = "python:3.12-alpine"
	}
	runner := &execution.DockerRunner{Isolation: "trusted", Image: image, JavaScriptImage: os.Getenv("ONTOLOGY_JAVASCRIPT_IMAGE"), GoImage: os.Getenv("ONTOLOGY_GO_IMAGE"), Binary: os.Getenv("ONTOLOGY_DOCKER_BINARY")}
	if _, err := runner.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	execConfig := execution.Config{Store: &execution.SQLStore{DB: ee.DB()}, Authorizer: &platform.RemoteAuthorizer{URL: metaServer.URL, ServiceToken: serviceToken, Client: metaServer.Client()}, SessionChecker: checker, Metadata: &execution.RemoteMetadata{URL: metaServer.URL, ServiceToken: serviceToken, Client: metaServer.Client()}, Runner: runner, DataTransactions: &execution.RemoteDataTransactions{URL: dataServer.URL, ServiceToken: serviceToken, Client: dataServer.Client()}}
	execApp := execution.NewHandler(execConfig)
	execServer := httptest.NewTLSServer(platform.ProtectSessionRoutes(serviceToken, false, checker, execApp))
	defer execServer.Close()
	roots.AddCert(execServer.Certificate())
	var category, group c.Organization
	admin.request("POST", metaServer.URL+"/metadata/categories", map[string]string{"name": "Python执行验收"}, nil, 201, &category)
	admin.request("POST", metaServer.URL+"/metadata/groups", map[string]string{"name": "计算", "categoryId": category.ID}, nil, 201, &group)
	publish := func(def c.Definition) c.CurrentDefinition {
		def.CategoryID, def.GroupID = category.ID, group.ID
		var draft c.CurrentDefinition
		headers := admin.request("POST", metaServer.URL+"/metadata/definitions", def, nil, 201, &draft)
		admin.request("POST", metaServer.URL+"/metadata/definitions/"+draft.Definition.ID+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
		var current c.CurrentDefinition
		admin.request("GET", metaServer.URL+"/metadata/definitions/"+draft.Definition.ID, nil, nil, 200, &current)
		return current
	}
	fn := publish(c.Definition{Kind: c.KindFunction, Name: "计算字符数", Function: &c.Callable{
		Input: []c.Property{{Name: "正文", DataType: "string", Required: true}}, Output: []c.Property{{Name: "字符数", DataType: "int64", Required: true}}, SideEffects: "none",
		Runtime: &c.ScriptRuntime{Language: "python", Entrypoint: "main", TimeoutSeconds: 5, Source: "def main(inputs):\n    print('private diagnostic must not escape')\n    return {'字符数': str(len(inputs['正文']))}\n"},
	}})
	action := publish(c.Definition{Kind: c.KindAction, Name: "统计笔记", Action: &c.Callable{FunctionID: fn.Definition.ID, SideEffects: "none", Input: []c.Property{{Name: "正文", DataType: "string", Required: true}}, Output: []c.Property{{Name: "字符数", DataType: "int64", Required: true}}}})
	inline := publish(c.Definition{Kind: c.KindAction, Name: "独立源码统计笔记", Action: &c.Callable{
		Input: []c.Property{{Name: "正文", DataType: "string", Required: true}}, Output: []c.Property{{Name: "字符数", DataType: "int64", Required: true}}, SideEffects: "none",
		Runtime: &c.ScriptRuntime{Language: "python", Entrypoint: "main", TimeoutSeconds: 5, Source: "def main(inputs):\n    return {'字符数': str(len(inputs['正文']) + 10)}\n"},
	}})
	if fn.Definition.Function.Input[0].ID == action.Definition.Action.Input[0].ID {
		t.Fatal("independent signatures must keep their own parameter identity")
	}
	// Explicitly grant this fixture's role; existing tenants are never upgraded.
	var policy c.PolicyRecord
	admin.request("GET", metaServer.URL+"/metadata/admin/policy", nil, nil, 200, &policy)
	roleID := session.User.RoleIDs[0]
	for _, op := range []string{"function.invoke", "action.invoke"} {
		policy.Current.Rules = append(policy.Current.Rules, c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "allow", Operation: op, ResourceID: "*"})
	}
	savePolicy := func(rules []c.PermissionRule) {
		var latest c.PolicyRecord
		admin.request("GET", metaServer.URL+"/metadata/admin/policy", nil, nil, 200, &latest)
		headers := admin.request("PUT", metaServer.URL+"/metadata/admin/policy", c.PermissionPolicy{Rules: rules}, map[string]string{"If-Match": `"` + latest.ETag + `"`}, 200, nil)
		admin.request("POST", metaServer.URL+"/metadata/admin/policy/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	}
	savePolicy(policy.Current.Rules)
	inputID, outputID := fn.Definition.Function.Input[0].ID, fn.Definition.Function.Output[0].ID
	intent := c.InvocationRequest{DefinitionID: fn.Definition.ID, Input: map[string]json.RawMessage{inputID: raw("Hello知识")}}
	key := c.NewID()
	var result c.Invocation
	admin.request("POST", execServer.URL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": key}, 201, &result)
	if result.Status != "succeeded" || string(result.Output[outputID]) != `"7"` || result.CodeDigest == "" {
		t.Fatalf("unexpected Python result: %+v", result)
	}
	var replay c.Invocation
	admin.request("POST", execServer.URL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": key}, 200, &replay)
	if replay.ID != result.ID {
		t.Fatal("replay executed a second task")
	}
	changed := c.InvocationRequest{DefinitionID: fn.Definition.ID, Input: map[string]json.RawMessage{inputID: raw("different intent")}}
	admin.request("POST", execServer.URL+"/execution/invocations", changed, map[string]string{"Idempotency-Key": key}, 409, nil)
	admin.request("POST", execServer.URL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": c.NewID(), "X-CSRF-Token": ""}, 403, nil)
	resultURL := execServer.URL + "/execution/invocations/" + result.ID
	other.request("GET", resultURL, nil, nil, 404, nil)
	var peer c.User
	admin.request("POST", metaServer.URL+"/metadata/admin/users", c.UserInput{Username: "peer", DisplayName: "同租户其他用户", Password: password, Enabled: true, RoleIDs: []string{roleID}}, nil, 201, &peer)
	peerClient, _ := login(tenant, "peer")
	peerClient.request("GET", resultURL, nil, nil, 404, nil)
	admin.request("GET", resultURL, nil, nil, 200, &replay)
	actionIntent := c.InvocationRequest{DefinitionID: action.Definition.ID, Input: map[string]json.RawMessage{action.Definition.Action.Input[0].ID: raw("Hello知识")}}
	var actionResult c.Invocation
	admin.request("POST", execServer.URL+"/execution/invocations", actionIntent, map[string]string{"Idempotency-Key": c.NewID()}, 201, &actionResult)
	if actionResult.Status != "succeeded" || string(actionResult.Output[action.Definition.Action.Output[0].ID]) != `"7"` {
		t.Fatalf("Action did not adapt independent parameter IDs: %+v", actionResult)
	}
	denied := append(append([]c.PermissionRule{}, policy.Current.Rules...), c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "deny", Operation: "function.invoke", ResourceID: fn.Definition.ID})
	savePolicy(denied)
	admin.request("GET", resultURL, nil, nil, 403, nil)
	admin.request("POST", execServer.URL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": key}, 403, nil)
	admin.request("POST", execServer.URL+"/execution/invocations", actionIntent, map[string]string{"Idempotency-Key": c.NewID()}, 403, nil)
	// An Action with its own source requires only its own invoke grant, even
	// when the referenced Function used by another Action is explicitly denied.
	inlineIntent := c.InvocationRequest{DefinitionID: inline.Definition.ID, Input: map[string]json.RawMessage{inline.Definition.Action.Input[0].ID: raw("Hello知识")}}
	inlineKey := c.NewID()
	var inlineResult c.Invocation
	admin.request("POST", execServer.URL+"/execution/invocations", inlineIntent, map[string]string{"Idempotency-Key": inlineKey}, 201, &inlineResult)
	inlineOutputID := inline.Definition.Action.Output[0].ID
	if inlineResult.Kind != c.KindAction || inlineResult.Status != "succeeded" || string(inlineResult.Output[inlineOutputID]) != `"17"` {
		t.Fatalf("native Action did not execute its own source: %+v", inlineResult)
	}
	inlineURL := execServer.URL + "/execution/invocations/" + inlineResult.ID
	admin.request("POST", execServer.URL+"/execution/invocations", inlineIntent, map[string]string{"Idempotency-Key": inlineKey}, 200, &replay)
	if replay.ID != inlineResult.ID {
		t.Fatal("native Action replay executed again")
	}
	other.request("GET", inlineURL, nil, nil, 404, nil)
	peerClient.request("GET", inlineURL, nil, nil, 404, nil)
	inlineDenied := append(append([]c.PermissionRule{}, denied...), c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "deny", Operation: "action.invoke", ResourceID: inline.Definition.ID})
	savePolicy(inlineDenied)
	admin.request("GET", inlineURL, nil, nil, 403, nil)
	admin.request("POST", execServer.URL+"/execution/invocations", inlineIntent, map[string]string{"Idempotency-Key": inlineKey}, 403, nil)
	savePolicy(denied)
	// Saving source is separate from publication. Existing calls keep the
	// current source until publication; afterwards old results are rejected.
	inline.Definition.Action.Runtime.Source = "def main(inputs):\n    return {'字符数': str(len(inputs['正文']) + 20)}\n"
	inline.Definition.Action.TestCases = []c.CallableTestCase{{ID: "usual-note", Name: "常用笔记样例", Input: map[string]json.RawMessage{inline.Definition.Action.Input[0].ID: raw("Hello知识")}}}
	var inlineDraft c.CurrentDefinition
	inlineHeaders := admin.request("PUT", metaServer.URL+"/metadata/definitions/"+inline.Definition.ID+"/draft", inline.Definition, map[string]string{"If-Match": `"` + inline.ETag + `"`}, 200, &inlineDraft)
	admin.request("GET", inlineURL, nil, nil, 200, &replay)
	admin.request("POST", execServer.URL+"/execution/invocations", inlineIntent, map[string]string{"Idempotency-Key": c.NewID()}, 201, &replay)
	if string(replay.Output[inlineOutputID]) != `"17"` {
		t.Fatal("unpublished native Action source was executed")
	}
	var loadedDraft c.CurrentDefinition
	admin.request("GET", metaServer.URL+"/metadata/definitions/"+inline.Definition.ID+"/draft", nil, nil, 200, &loadedDraft)
	if len(loadedDraft.Definition.Action.TestCases) != 1 || string(loadedDraft.Definition.Action.TestCases[0].Input[inline.Definition.Action.Input[0].ID]) != `"Hello知识"` {
		t.Fatal("saved test case did not round trip through metadata")
	}
	draftIntent := inlineIntent
	draftIntent.DraftETag = inlineDraft.ETag
	var draftResult c.Invocation
	draftKey := c.NewID()
	admin.request("POST", execServer.URL+"/execution/invocations", draftIntent, map[string]string{"Idempotency-Key": draftKey}, 201, &draftResult)
	if draftResult.Mode != "draft" || draftResult.DraftETag != inlineDraft.ETag || string(draftResult.Output[inlineOutputID]) != `"27"` {
		t.Fatalf("saved draft did not run independently: %+v", draftResult)
	}
	draftResultURL := execServer.URL + "/execution/invocations/" + draftResult.ID
	other.request("GET", draftResultURL, nil, nil, 404, nil)
	peerClient.request("GET", draftResultURL, nil, nil, 404, nil)
	editDenied := append(append([]c.PermissionRule{}, denied...), c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "deny", Operation: "definition.edit", ResourceID: inline.Definition.ID})
	savePolicy(editDenied)
	admin.request("GET", draftResultURL, nil, nil, 403, nil)
	admin.request("POST", execServer.URL+"/execution/invocations", draftIntent, map[string]string{"Idempotency-Key": draftKey}, 403, nil)
	// Current business calls continue with invoke permission, without edit.
	admin.request("POST", execServer.URL+"/execution/invocations", inlineIntent, map[string]string{"Idempotency-Key": c.NewID()}, 201, &replay)
	if replay.Mode != "current" || string(replay.Output[inlineOutputID]) != `"17"` {
		t.Fatal("draft permission affected current business call")
	}
	savePolicy(denied)
	correctSource := inlineDraft.Definition.Action.Runtime.Source
	inlineDraft.Definition.Action.Runtime.Source = "def main(inputs):\n    结果 = (1 + )\n    return {}\n"
	inlineHeaders = admin.request("PUT", metaServer.URL+"/metadata/definitions/"+inline.Definition.ID+"/draft", inlineDraft.Definition, map[string]string{"If-Match": inlineHeaders.Get("ETag")}, 200, &inlineDraft)
	admin.request("GET", draftResultURL, nil, nil, 409, nil)
	draftIntent.DraftETag = inlineDraft.ETag
	admin.request("POST", execServer.URL+"/execution/invocations", draftIntent, map[string]string{"Idempotency-Key": c.NewID()}, 201, &replay)
	if replay.Status != "failed" || len(replay.Diagnostics) != 1 || replay.Diagnostics[0].Code != "syntax_error" || replay.Diagnostics[0].Line != 2 {
		t.Fatalf("draft syntax location missing: %+v", replay)
	}
	inlineDraft.Definition.Action.Runtime.Source = correctSource
	inlineHeaders = admin.request("PUT", metaServer.URL+"/metadata/definitions/"+inline.Definition.ID+"/draft", inlineDraft.Definition, map[string]string{"If-Match": inlineHeaders.Get("ETag")}, 200, &inlineDraft)
	admin.request("POST", metaServer.URL+"/metadata/definitions/"+inline.Definition.ID+"/publish", nil, map[string]string{"If-Match": inlineHeaders.Get("ETag")}, 200, nil)
	admin.request("GET", draftResultURL, nil, nil, 404, nil)
	admin.request("GET", inlineURL, nil, nil, 409, nil)
	admin.request("POST", execServer.URL+"/execution/invocations", inlineIntent, map[string]string{"Idempotency-Key": inlineKey}, 409, nil)
	admin.request("POST", execServer.URL+"/execution/invocations", inlineIntent, map[string]string{"Idempotency-Key": c.NewID()}, 201, &replay)
	if string(replay.Output[inlineOutputID]) != `"27"` || replay.CodeDigest == inlineResult.CodeDigest {
		t.Fatal("new native Action source did not become effective")
	}
	savePolicy(policy.Current.Rules)
	// New code uses a new snapshot; old results cannot be misinterpreted.
	fn.Definition.Function.Runtime.Source = "def main(inputs):\n    return {'字符数': str(len(inputs['正文']) + 1)}\n"
	var draft c.CurrentDefinition
	headers := admin.request("PUT", metaServer.URL+"/metadata/definitions/"+fn.Definition.ID+"/draft", fn.Definition, map[string]string{"If-Match": `"` + fn.ETag + `"`}, 200, &draft)
	functionDraftIntent := intent
	functionDraftIntent.DraftETag = draft.ETag
	admin.request("POST", execServer.URL+"/execution/invocations", functionDraftIntent, map[string]string{"Idempotency-Key": c.NewID()}, 201, &replay)
	if replay.Mode != "draft" || string(replay.Output[outputID]) != `"8"` {
		t.Fatal("Function draft did not use draft implementation")
	}
	admin.request("POST", metaServer.URL+"/metadata/definitions/"+fn.Definition.ID+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	admin.request("GET", resultURL, nil, nil, 409, nil)
	admin.request("POST", execServer.URL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": c.NewID()}, 201, &replay)
	if string(replay.Output[outputID]) != `"8"` {
		t.Fatal("new invocation did not use the current source")
	}
	var unpublished c.CurrentDefinition
	admin.request("POST", metaServer.URL+"/metadata/definitions", c.Definition{CategoryID: category.ID, GroupID: group.ID, Kind: c.KindFunction, Name: "未发布草稿试算", Function: &c.Callable{SideEffects: "none", Runtime: &c.ScriptRuntime{Language: "python", Entrypoint: "main", TimeoutSeconds: 5, Source: "def main(inputs):\n    return {}\n"}}}, nil, 201, &unpublished)
	admin.request("POST", execServer.URL+"/execution/invocations", c.InvocationRequest{DefinitionID: unpublished.Definition.ID, DraftETag: unpublished.ETag, Input: map[string]json.RawMessage{}}, map[string]string{"Idempotency-Key": c.NewID()}, 201, &replay)
	if replay.Mode != "draft" || replay.Status != "succeeded" {
		t.Fatalf("unpublished Function draft did not run: %+v", replay)
	}
	admin.request("GET", metaServer.URL+"/metadata/definitions/"+unpublished.Definition.ID, nil, nil, 404, nil)
	slow := publish(c.Definition{Kind: c.KindFunction, Name: "超时计算", Function: &c.Callable{SideEffects: "none", Runtime: &c.ScriptRuntime{Language: "python", Entrypoint: "main", TimeoutSeconds: 1, Source: "def main(inputs):\n    while True:\n        pass\n"}}})
	admin.request("POST", execServer.URL+"/execution/invocations", c.InvocationRequest{DefinitionID: slow.Definition.ID, Input: map[string]json.RawMessage{}}, map[string]string{"Idempotency-Key": c.NewID()}, 201, &replay)
	if replay.Status != "timed_out" {
		t.Fatalf("expected bounded timeout: %+v", replay)
	}
	encoded, _ := json.Marshal(replay)
	if strings.Contains(string(encoded), "while True") || strings.Contains(string(encoded), "Traceback") {
		t.Fatal("execution disclosed code or traceback")
	}
	verifyMultiLanguageHTTP(t, admin, other, peerClient, execServer.URL, metaServer.URL, publish, savePolicy, policy.Current.Rules, roleID)
	verifyDataWriteTransactionsHTTP(t, admin, other, execServer.URL, dataServer.URL, publish)
	verifyTransactionRecoveryHTTP(t, admin, execConfig, roots, dataServer.URL, publish)
	// Authoritative OPA failure closes access even for a persisted success.
	originalURL := opa.URL
	opa.URL = "http://127.0.0.1:1"
	admin.request("GET", execServer.URL+"/execution/invocations/"+actionResult.ID, nil, nil, 503, nil)
	opa.URL = originalURL
	admin.request("POST", metaServer.URL+"/metadata/auth/logout", nil, nil, 204, nil)
	admin.request("GET", resultURL, nil, nil, 401, nil)
}

func verifyMultiLanguageHTTP(t *testing.T, admin, other, peer *executionHTTP, execURL, metaURL string, publish func(c.Definition) c.CurrentDefinition, savePolicy func([]c.PermissionRule), baseRules []c.PermissionRule, roleID string) {
	t.Helper()
	var capabilities struct {
		Runtimes, SourceTools       []string
		Invocation, DraftInvocation bool
	}
	admin.request("GET", execURL+"/execution/capabilities", nil, nil, 200, &capabilities)
	if !capabilities.Invocation || !capabilities.DraftInvocation || strings.Join(capabilities.Runtimes, ",") != "python,javascript,go" || strings.Join(capabilities.SourceTools, ",") != "go" {
		t.Fatalf("real capabilities not available: %+v", capabilities)
	}
	for _, language := range []string{"javascript", "go"} {
		entrypoint, sourceTemplate, badSource := "main", "function main(inputs) { console.log('private'); return {结果: '__VALUE__'}; }", "function main(inputs) {\n const 中文 = ;\n}"
		if language == "go" {
			entrypoint = "Main"
			sourceTemplate = "package main\nimport \"fmt\"\nfunc Main(inputs map[string]any)(map[string]any,error){fmt.Println(\"private\");return map[string]any{\"结果\":\"__VALUE__\"},nil}"
			badSource = "package main\nfunc Main(inputs map[string]any)(map[string]any,error){\n中文 := ;return nil,nil\n}"
		}
		source := func(value string) string { return strings.ReplaceAll(sourceTemplate, "__VALUE__", value) }
		createCallable := func() *c.Callable {
			return &c.Callable{Input: []c.Property{{Name: "金额", DataType: "decimal", Precision: 25, Scale: 2, Required: true}}, Output: []c.Property{{Name: "结果", DataType: "decimal", Precision: 25, Scale: 2, Required: true}}, SideEffects: "none", Runtime: &c.ScriptRuntime{Language: language, Entrypoint: entrypoint, Source: source("9007199254740993.10"), TimeoutSeconds: 30}}
		}
		function := publish(c.Definition{Kind: c.KindFunction, Name: language + "精确计算", Function: createCallable()})
		inline := publish(c.Definition{Kind: c.KindAction, Name: language + "原生动作", Action: createCallable()})
		boundCallable := createCallable()
		boundCallable.Runtime = nil
		boundCallable.FunctionID = function.Definition.ID
		bound := publish(c.Definition{Kind: c.KindAction, Name: language + "绑定动作", Action: boundCallable})
		callable := func(d c.Definition) *c.Callable {
			if d.Kind == c.KindAction {
				return d.Action
			}
			return d.Function
		}
		intent := func(d c.Definition) c.InvocationRequest {
			return c.InvocationRequest{DefinitionID: d.ID, Input: map[string]json.RawMessage{callable(d).Input[0].ID: raw("9007199254740993.10")}}
		}
		invoke := func(request c.InvocationRequest, key string, status int) c.Invocation {
			var result c.Invocation
			admin.request("POST", execURL+"/execution/invocations", request, map[string]string{"Idempotency-Key": key}, status, &result)
			return result
		}
		assertValue := func(result c.Invocation, d c.Definition, value string) {
			t.Helper()
			if result.Status != "succeeded" || string(result.Output[callable(d).Output[0].ID]) != `"`+value+`"` {
				t.Fatalf("%s %s wrong value: %+v", language, d.Kind, result)
			}
		}
		for _, current := range []c.CurrentDefinition{function, inline} {
			request, key := intent(current.Definition), c.NewID()
			result := invoke(request, key, 201)
			assertValue(result, current.Definition, "9007199254740993.10")
			if replay := invoke(request, key, 200); replay.ID != result.ID {
				t.Fatal("multilanguage idempotency executed twice")
			}
			resultURL := execURL + "/execution/invocations/" + result.ID
			other.request("GET", resultURL, nil, nil, 404, nil)
			peer.request("GET", resultURL, nil, nil, 404, nil)
			callable(current.Definition).Runtime.Source = source("9007199254740993.20")
			var draft c.CurrentDefinition
			headers := admin.request("PUT", metaURL+"/metadata/definitions/"+current.Definition.ID+"/draft", current.Definition, map[string]string{"If-Match": `"` + current.ETag + `"`}, 200, &draft)
			assertValue(invoke(request, c.NewID(), 201), current.Definition, "9007199254740993.10")
			draftRequest := request
			draftRequest.DraftETag = draft.ETag
			draftResult := invoke(draftRequest, c.NewID(), 201)
			assertValue(draftResult, draft.Definition, "9007199254740993.20")
			if draftResult.Mode != "draft" || draftResult.DraftETag != draft.ETag {
				t.Fatal("multilanguage draft evidence missing")
			}
			wrong := draftRequest
			wrong.DraftETag = strings.Repeat("0", 64)
			invoke(wrong, c.NewID(), 409)
			callable(draft.Definition).Runtime.Source = badSource
			headers = admin.request("PUT", metaURL+"/metadata/definitions/"+draft.Definition.ID+"/draft", draft.Definition, map[string]string{"If-Match": headers.Get("ETag")}, 200, &draft)
			admin.request("GET", execURL+"/execution/invocations/"+draftResult.ID, nil, nil, 409, nil)
			draftRequest.DraftETag = draft.ETag
			failed := invoke(draftRequest, c.NewID(), 201)
			wantLine := 2
			if language == "go" {
				wantLine = 3
			}
			if failed.Status != "failed" || len(failed.Diagnostics) != 1 || failed.Diagnostics[0].Code != "syntax_error" || failed.Diagnostics[0].Line != wantLine || strings.Contains(string(mustMarshal(failed)), "private") {
				t.Fatalf("%s unsafe syntax diagnostic: %+v", language, failed)
			}
			callable(draft.Definition).Runtime.Source = source("9007199254740993.20")
			headers = admin.request("PUT", metaURL+"/metadata/definitions/"+draft.Definition.ID+"/draft", draft.Definition, map[string]string{"If-Match": headers.Get("ETag")}, 200, &draft)
			admin.request("POST", metaURL+"/metadata/definitions/"+draft.Definition.ID+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
			admin.request("GET", resultURL, nil, nil, 409, nil)
			admin.request("GET", execURL+"/execution/invocations/"+draftResult.ID, nil, nil, 404, nil)
			assertValue(invoke(request, c.NewID(), 201), current.Definition, "9007199254740993.20")
		}
		assertValue(invoke(intent(bound.Definition), c.NewID(), 201), bound.Definition, "9007199254740993.20")
		denyInvoke := append(append([]c.PermissionRule{}, baseRules...), c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "deny", Operation: "function.invoke", ResourceID: function.Definition.ID})
		savePolicy(denyInvoke)
		admin.request("POST", execURL+"/execution/invocations", intent(function.Definition), map[string]string{"Idempotency-Key": c.NewID()}, 403, nil)
		admin.request("POST", execURL+"/execution/invocations", intent(bound.Definition), map[string]string{"Idempotency-Key": c.NewID()}, 403, nil)
		assertValue(invoke(intent(inline.Definition), c.NewID(), 201), inline.Definition, "9007199254740993.20")
		if language == "go" {
			request := c.SourceToolRequest{DefinitionID: function.Definition.ID, Operation: "format", Runtime: createCallable().Runtime}
			var formatted c.SourceToolResult
			admin.request("POST", execURL+"/execution/source-tools", request, nil, 200, &formatted)
			if len(formatted.Diagnostics) != 0 || !strings.Contains(formatted.Source, "func Main(inputs map[string]any) (map[string]any, error)") {
				t.Fatalf("real Go format: %+v", formatted)
			}
			other.request("POST", execURL+"/execution/source-tools", request, nil, 404, nil)
			request.Runtime.Source = badSource
			request.Operation = "check"
			admin.request("POST", execURL+"/execution/source-tools", request, nil, 200, &formatted)
			if len(formatted.Diagnostics) == 0 || formatted.Diagnostics[0].Line != 3 {
				t.Fatalf("real Go check: %+v", formatted)
			}
			request.Runtime.Source = source("9007199254740993.10")
			savePolicy(append(append([]c.PermissionRule{}, baseRules...), c.PermissionRule{ID: c.NewID(), RoleID: roleID, Effect: "deny", Operation: "definition.edit", ResourceID: function.Definition.ID}))
			admin.request("POST", execURL+"/execution/source-tools", request, nil, 403, nil)
			assertValue(invoke(intent(function.Definition), c.NewID(), 201), function.Definition, "9007199254740993.20")
		}
		savePolicy(baseRules)
	}
}

func mustMarshal(value any) []byte { encoded, _ := json.Marshal(value); return encoded }
