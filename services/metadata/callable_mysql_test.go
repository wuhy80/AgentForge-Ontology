package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
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
	if err := storage.Migrate(context.Background(), engine.DB(), "metadata", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant := "python-publication-" + c.NewID()
	defer func() {
		for _, table := range []string{"af_meta_dependencies", "af_meta_outbox", "af_meta_publications", "af_meta_definitions", "af_meta_organizations", "af_meta_tenant_guards"} {
			if _, err := engine.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant); err != nil {
				t.Error(err)
			}
		}
	}()
	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/publications/prepare" {
			var input c.PrepareRequest
			_ = json.NewDecoder(r.Body).Decode(&input)
			writeJSON(w, 200, c.PrepareResult{OperationID: input.OperationID, Status: "prepared"})
		} else {
			writeJSON(w, 200, map[string]string{"status": "ready"})
		}
	}))
	defer data.Close()
	api := NewHandler(Config{Engine: engine, DataURL: data.URL, ServiceToken: strings.Repeat("p", 32), Authorizer: authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
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
	actionDefinition := executableFunction()
	actionDefinition.Kind, actionDefinition.Name, actionDefinition.Action, actionDefinition.Function = c.KindAction, "调用Python", actionDefinition.Function, nil
	actionDefinition.Action.Runtime, actionDefinition.Action.FunctionID = nil, function.Definition.ID
	actionDefinition.Action.Input[0].ID, actionDefinition.Action.Output[0].ID = "action-in", "action-out"
	action := create(actionDefinition)
	publish(action, 200)
	inlineDefinition := executableAction()
	inlineDefinition.Name = "独立Python动作"
	inline := create(inlineDefinition)
	publish(inline, 200)
	originalSource := inline.Definition.Action.Runtime.Source
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
}
