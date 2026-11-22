package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

func TestMySQLPythonPublicationAndActionBinding(t *testing.T) {
	dsn := os.Getenv("METADATA_TEST_DSN")
	if dsn == "" {
		t.Skip("set METADATA_TEST_DSN for isolated Python publication lifecycle")
	}
	engine, err := storage.Open(context.Background(), storage.Config{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := storage.Migrate(context.Background(), engine.DB(), "metadata", append(append([]storage.Migration{}, Migrations...), identity.Migrations...)); err != nil {
		t.Fatal(err)
	}
	tenant := "python-publication-" + c.NewID()
	defer func() {
		for _, table := range []string{"af_meta_dependencies", "af_meta_outbox", "af_meta_publications", "af_meta_definitions", "af_meta_organizations", "af_meta_tenant_guards", "af_identity_tenants"} {
			if _, err := engine.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant); err != nil {
				t.Error(err)
			}
		}
	}()
	var savedEnumReference, impactOverBudget atomic.Bool
	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if impactOverBudget.Load() && (strings.HasSuffix(r.URL.Path, "/managed-storage-lifecycle") || strings.HasSuffix(r.URL.Path, "/saved-query-impact")) {
			writeJSON(w, 422, c.Error{Code: "saved_query_impact_budget", Message: "internal details"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/managed-storage-lifecycle") {
			w.WriteHeader(204)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/saved-query-impact") {
			writeJSON(w, 200, c.SavedQueryDefinitionImpact{Referenced: savedEnumReference.Load()})
			return
		}
		if r.URL.Path == "/internal/publications/prepare" {
			var input c.PrepareRequest
			_ = json.NewDecoder(r.Body).Decode(&input)
			writeJSON(w, 200, c.PrepareResult{OperationID: input.OperationID, Status: "prepared"})
		} else {
			writeJSON(w, 200, map[string]string{"status": "ready"})
		}
	}))
	defer data.Close()
	api := NewHandler(Config{Engine: engine, DataURL: data.URL, ServiceToken: strings.Repeat("p", 32), CallablePolicyReferences: identity.NewStore(engine.DB(), nil, ""), Authorizer: authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		return c.Decision{Allow: true}, nil
	})})
	request := func(method, path, etag string, body any, status int, out any) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{TenantID: tenant, Subject: "author"}))
		if etag != "" {
			r.Header.Set("If-Match", etagHeader(etag))
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: HTTP %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var category, group c.Organization
	request("POST", "/metadata/categories", "", map[string]string{"name": "Python验收"}, 201, &category)
	request("POST", "/metadata/groups", "", map[string]string{"name": "计算", "categoryId": category.ID}, 201, &group)
	create := func(definition c.Definition) c.CurrentDefinition {
		t.Helper()
		definition.ID, definition.TenantID, definition.CategoryID, definition.GroupID = "", "", category.ID, group.ID
		var record c.CurrentDefinition
		request("POST", "/metadata/definitions", "", definition, 201, &record)
		return record
	}
	publish := func(record c.CurrentDefinition, status int) {
		t.Helper()
		request("POST", "/metadata/definitions/"+record.Definition.ID+"/publish", record.ETag, nil, status, nil)
	}
	function := create(executableFunction())
	publish(function, 200)
	functionPath := "/metadata/definitions/" + function.Definition.ID
	lifecycleDefinition := validSchema()
	lifecycleDefinition.Name = "生命周期验收对象"
	lifecycleSchema := create(lifecycleDefinition)
	publish(lifecycleSchema, 200)
	schemaPath := "/metadata/definitions/" + lifecycleSchema.Definition.ID
	request("GET", schemaPath+"/disable-preview", lifecycleSchema.ETag, nil, 200, nil)
	var schemaState c.CurrentDefinition
	request("POST", schemaPath+"/disable", lifecycleSchema.ETag, nil, 200, &schemaState)
	if schemaState.Status != "disabled" || schemaState.Definition.ID != lifecycleSchema.Definition.ID {
		t.Fatalf("unexpected disabled Schema: %+v", schemaState)
	}
	request("POST", schemaPath+"/restore", lifecycleSchema.ETag, nil, 200, &schemaState)
	if schemaState.Status != "active" {
		t.Fatalf("unexpected restored Schema: %+v", schemaState)
	}
	enum := create(c.Definition{Kind: c.KindEnum, Name: "验收枚举", Enum: &c.Enum{Values: []c.EnumValue{{Code: "in", Label: "有效"}}}})
	publish(enum, 200)
	enumPath := "/metadata/definitions/" + enum.Definition.ID
	request("GET", enumPath+"/disable-preview", enum.ETag, nil, 200, nil)
	var blocked c.Error
	impactOverBudget.Store(true)
	for _, target := range []struct{ path, etag string }{{schemaPath, lifecycleSchema.ETag}, {enumPath, enum.ETag}} {
		request("GET", target.path+"/disable-preview", target.etag, nil, 422, &blocked)
		if blocked.Code != "saved_query_impact_budget" || blocked.Message == "internal details" {
			t.Fatalf("unexpected preview failure: %+v", blocked)
		}
		request("POST", target.path+"/disable", target.etag, nil, 422, &blocked)
		if blocked.Code != "saved_query_impact_budget" {
			t.Fatalf("unexpected disable failure: %+v", blocked)
		}
		var state c.CurrentDefinition
		request("GET", target.path, "", nil, 200, &state)
		if state.Status != "active" {
			t.Fatalf("failed disable changed state: %+v", state)
		}
	}
	impactOverBudget.Store(false)
	savedEnumReference.Store(true)
	request("POST", enumPath+"/disable", enum.ETag, nil, 409, &blocked)
	if blocked.Code != "definition_saved_query_in_use" {
		t.Fatalf("saved query did not block enum disable: %+v", blocked)
	}
	savedEnumReference.Store(false)
	var enumState c.CurrentDefinition
	request("POST", enumPath+"/disable", enum.ETag, nil, 200, &enumState)
	if enumState.Status != disabledDefinition || enumState.Definition.ID != enum.Definition.ID {
		t.Fatalf("enum did not retain identity: %+v", enumState)
	}
	enumUser := executableFunction()
	enumUser.ID, enumUser.TenantID = "", ""
	enumUser.Name = "草稿引用枚举"
	enumUser.Function.Input = []c.Property{{Name: "状态", DataType: "string", EnumID: enum.Definition.ID}}
	enumUser.CategoryID, enumUser.GroupID = category.ID, group.ID
	request("POST", "/metadata/definitions", "", enumUser, 422, &blocked)
	if blocked.Code != "invalid_enum_reference" {
		t.Fatalf("disabled enum accepted as a new reference: %+v", blocked)
	}
	request("POST", enumPath+"/restore", enum.ETag, nil, 200, &enumState)
	if enumState.Status != "active" {
		t.Fatalf("enum did not restore: %+v", enumState)
	}
	enumUserDraft := create(enumUser)
	request("POST", enumPath+"/disable", enum.ETag, nil, 409, &blocked)
	if blocked.Code != "definition_in_use" {
		t.Fatalf("draft did not block enum disable: %+v", blocked)
	}
	publish(enumUserDraft, 200)
	request("GET", enumPath+"/disable-preview", enum.ETag, nil, 409, &blocked)
	if blocked.Code != "definition_in_use" {
		t.Fatalf("current definition did not block enum disable: %+v", blocked)
	}
	request("GET", functionPath+"/disable-preview", function.ETag, nil, 200, nil)
	actionDefinition := executableFunction()
	actionDefinition.Kind, actionDefinition.Name, actionDefinition.Action, actionDefinition.Function = c.KindAction, "调用Python", actionDefinition.Function, nil
	actionDefinition.Action.Runtime, actionDefinition.Action.FunctionID = nil, function.Definition.ID
	actionDefinition.Action.Input[0].ID, actionDefinition.Action.Output[0].ID = "action-in", "action-out"
	action := create(actionDefinition)
	request("POST", functionPath+"/disable", function.ETag, nil, 409, nil)
	request("GET", functionPath+"/disable-preview", function.ETag, nil, 409, nil)
	publish(action, 200)
	request("GET", functionPath+"/disable-preview", function.ETag, nil, 409, nil)
	request("POST", functionPath+"/disable", function.ETag, nil, 409, nil)
	inlineDefinition := executableAction()
	inlineDefinition.Name = "独立Python动作"
	inline := create(inlineDefinition)
	publish(inline, 200)
	inlinePath := "/metadata/definitions/" + inline.Definition.ID
	request("POST", inlinePath+"/disable", "", nil, 428, nil)
	request("POST", inlinePath+"/disable", "stale", nil, 412, nil)
	var lifecycle c.CurrentDefinition
	var preview c.CallableLifecyclePreview
	request("GET", inlinePath+"/disable-preview", "stale", nil, 412, nil)
	request("GET", inlinePath+"/disable-preview", inline.ETag, nil, 200, &preview)
	request("GET", inlinePath, "", nil, 200, &lifecycle)
	if preview.DefinitionID != inline.Definition.ID || preview.TargetStatus != disabledDefinition || lifecycle.Status != "active" {
		t.Fatalf("disable preview changed current state: preview=%+v state=%s", preview, lifecycle.Status)
	}
	policy, _ := json.Marshal(c.PermissionPolicy{Rules: []c.PermissionRule{{Operation: "action.invoke", ResourceID: inline.Definition.ID}}})
	setPolicy := func(current string, draft any) {
		t.Helper()
		_, err := engine.DB().Exec(`INSERT INTO af_identity_tenants (tenant_id,authorization_state,current_policy,draft_policy,policy_etag)
 VALUES (?,'state',?,?,'etag') ON DUPLICATE KEY UPDATE current_policy=VALUES(current_policy),draft_policy=VALUES(draft_policy)`, tenant, current, draft)
		if err != nil {
			t.Fatal(err)
		}
	}
	const emptyPolicy = `{"rules":[]}`
	for _, policies := range []struct {
		current string
		draft   any
	}{{string(policy), nil}, {emptyPolicy, string(policy)}} {
		setPolicy(policies.current, policies.draft)
		var failure c.Error
		request("GET", inlinePath+"/disable-preview", inline.ETag, nil, 409, &failure)
		if failure.Code != "definition_policy_in_use" {
			t.Fatalf("unexpected blocker: %+v", failure)
		}
		request("POST", inlinePath+"/disable", inline.ETag, nil, 409, nil)
	}
	setPolicy(emptyPolicy, nil)
	request("POST", inlinePath+"/disable", inline.ETag, nil, 200, &lifecycle)
	if lifecycle.Status != disabledDefinition || lifecycle.Definition.ID != inline.Definition.ID {
		t.Fatalf("disabled callable was not retained: %+v", lifecycle)
	}
	request("POST", inlinePath+"/disable", inline.ETag, nil, 200, nil)
	request("GET", inlinePath, "", nil, 200, &lifecycle)
	if lifecycle.Status != disabledDefinition {
		t.Fatal("current definition did not expose paused state")
	}
	request("PUT", inlinePath+"/draft", inline.ETag, inline.Definition, 409, nil)
	request("POST", inlinePath+"/publish", inline.ETag, nil, 409, nil)
	request("GET", inlinePath+"/restore-preview", inline.ETag, nil, 200, &preview)
	request("GET", inlinePath, "", nil, 200, &lifecycle)
	if preview.TargetStatus != "active" || lifecycle.Status != disabledDefinition {
		t.Fatal("restore preview changed current state")
	}
	request("POST", inlinePath+"/restore", inline.ETag, nil, 200, &lifecycle)
	if lifecycle.Status != "active" || lifecycle.Definition.ID != inline.Definition.ID {
		t.Fatalf("restored callable lost its identity: %+v", lifecycle)
	}
	originalSource := inline.Definition.Action.Runtime.Source
	inline.Definition.Action.TestCases = []c.CallableTestCase{{ID: "uppercase-sample", Name: "大写样例", Input: map[string]json.RawMessage{inline.Definition.Action.Input[0].ID: json.RawMessage(`"hello"`)}}}
	inline.Definition.Action.Runtime.Source = "def main(inputs):\n    return {'结果': inputs['文本'].upper()}\n"
	var inlineUpdate, inlineCurrent c.CurrentDefinition
	request("PUT", "/metadata/definitions/"+inline.Definition.ID+"/draft", inline.ETag, inline.Definition, 200, &inlineUpdate)
	request("GET", "/metadata/definitions/"+inline.Definition.ID, "", nil, 200, &inlineCurrent)
	if inlineCurrent.Definition.Action.Runtime.Source != originalSource {
		t.Fatal("unpublished Action code replaced current source")
	}
	publish(inlineUpdate, 200)
	request("GET", "/metadata/definitions/"+inline.Definition.ID, "", nil, 200, &inlineCurrent)
	if inlineCurrent.Definition.Action.Runtime.Source != inlineUpdate.Definition.Action.Runtime.Source {
		t.Fatal("published Action code did not become current")
	}
	if len(inlineCurrent.Definition.Action.TestCases) != 1 || string(inlineCurrent.Definition.Action.TestCases[0].Input[inlineCurrent.Definition.Action.Input[0].ID]) != `"hello"` {
		t.Fatal("saved/published sample lost stable parameter binding")
	}
	// Samples can change through the regular draft flow without a structural migration.
	inlineCurrent.Definition.Action.TestCases[0].Name = "改名样例"
	request("PUT", "/metadata/definitions/"+inline.Definition.ID+"/draft", inlineCurrent.ETag, inlineCurrent.Definition, 200, &inlineUpdate)
	publish(inlineUpdate, 200)
	request("GET", "/metadata/definitions/"+inline.Definition.ID, "", nil, 200, &inlineCurrent)
	if inlineCurrent.Definition.Action.TestCases[0].Name != "改名样例" {
		t.Fatal("sample metadata update did not publish")
	}
	inlineCurrent.Definition.Action.Runtime, inlineCurrent.Definition.Action.FunctionID = nil, function.Definition.ID
	request("PUT", "/metadata/definitions/"+inline.Definition.ID+"/draft", inlineCurrent.ETag, inlineCurrent.Definition, 200, &inlineUpdate)
	publish(inlineUpdate, 422)
	// Signature mismatches are rejected before a publication operation starts.
	bad := actionDefinition
	bad.Name = "错误签名"
	bad.Action = &c.Callable{FunctionID: function.Definition.ID, SideEffects: "none"}
	badAction := create(bad)
	publish(badAction, 422)
	var update c.CurrentDefinition
	function.Definition.Function.Runtime.Source = "def main(inputs):\n    return {'结果': inputs['文本'].upper()}\n"
	request("PUT", "/metadata/definitions/"+function.Definition.ID+"/draft", function.ETag, function.Definition, 200, &update)
	publish(update, 200)
	var current c.CurrentDefinition
	request("GET", "/metadata/definitions/"+function.Definition.ID, "", nil, 200, &current)
	if current.Definition.Function.Runtime.Source != update.Definition.Function.Runtime.Source {
		t.Fatal("new code did not become current")
	}
	// Parameter display changes cannot invalidate an already published Action.
	current.Definition.Function.Input[0].Name = "改名后的参数"
	request("PUT", "/metadata/definitions/"+current.Definition.ID+"/draft", current.ETag, current.Definition, 200, &update)
	publish(update, 422)
	request("GET", "/metadata/definitions/"+function.Definition.ID, "", nil, 200, &current)
	if current.Definition.Function.Input[0].Name != "文本" {
		t.Fatal("rejected binding change replaced current definition")
	}
	// A same-ID reference cannot borrow the other tenant's implementation.
	foreign := actionDefinition
	foreign.Name = "不存在的函数"
	foreign.Action = &c.Callable{FunctionID: c.NewID(), SideEffects: "none"}
	foreignAction := create(foreign)
	publish(foreignAction, 422)
	// Declared writes must resolve a current same-tenant Schema even in drafts.
	schemaDefinition := validSchema()
	schemaDefinition.Name = "事务写入对象"
	schema := create(schemaDefinition)
	publish(schema, 200)
	writerDefinition := writingFunction()
	writerDefinition.Name = "事务写入函数"
	writerDefinition.Function.DataWrite = []c.DataWriteCapability{{SchemaID: schema.Definition.ID, Operations: []string{"update", "create"}}}
	writer := create(writerDefinition)
	publish(writer, 200)
	writerActionDefinition := writer.Definition
	writerActionDefinition.ID, writerActionDefinition.TenantID = "", ""
	writerActionBody := *writer.Definition.Function
	writerActionDefinition.Kind, writerActionDefinition.Action, writerActionDefinition.Function = c.KindAction, &writerActionBody, nil
	writerActionDefinition.Name = "事务绑定动作"
	writerActionDefinition.Action.FunctionID, writerActionDefinition.Action.Runtime = writer.Definition.ID, nil
	writerAction := create(writerActionDefinition)
	publish(writerAction, 200)
	writer.Definition.Function.Runtime.Source += "\n# new write implementation"
	var writerDraft c.CurrentDefinition
	request("PUT", "/metadata/definitions/"+writer.Definition.ID+"/draft", writer.ETag, writer.Definition, 200, &writerDraft)
	publish(writerDraft, 200)
	for _, ref := range []string{function.Definition.ID, c.NewID()} {
		candidate := writingFunction()
		candidate.ID, candidate.TenantID, candidate.CategoryID, candidate.GroupID, candidate.Name = "", "", category.ID, group.ID, "非法写入引用"
		candidate.Function.DataWrite = []c.DataWriteCapability{{SchemaID: ref, Operations: []string{"create"}}}
		request("POST", "/metadata/definitions", "", candidate, 422, nil)
	}
}
