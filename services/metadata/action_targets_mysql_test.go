package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

// Uses fresh tenant/object identities and cleans only those tenants. The real
// MySQL Metadata lifecycle is exercised over HTTP; Data acknowledgements are
// isolated stubs because an Action target change never executes source DDL.
func TestMySQLActionTargetsLifecycleAndIsolation(t *testing.T) {
	dsn := os.Getenv("METADATA_TEST_DSN")
	if dsn == "" {
		t.Skip("set METADATA_TEST_DSN for Action target lifecycle on real MySQL")
	}
	ctx := context.Background()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := storage.Migrate(ctx, engine.DB(), "metadata", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant, otherTenant := "action-targets-"+contracts.NewID(), "action-targets-"+contracts.NewID()
	defer func() {
		for _, table := range []string{"af_meta_dependencies", "af_meta_outbox", "af_meta_publications", "af_meta_definitions", "af_meta_organizations", "af_meta_tenant_guards"} {
			if _, err := engine.DB().Exec("DELETE FROM "+table+" WHERE tenant_id IN (?,?)", tenant, otherTenant); err != nil {
				t.Errorf("clean isolated test tenant: %v", err)
			}
		}
	}()
	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/publications/prepare":
			var req contracts.PrepareRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			writeJSON(w, 200, contracts.PrepareResult{OperationID: req.OperationID, Status: "prepared"})
		case "/internal/publications/activate":
			writeJSON(w, 200, map[string]string{"status": "ready"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer data.Close()
	api := NewHandler(Config{Engine: engine, Authorizer: authorizerFunc(func(context.Context, contracts.Principal, contracts.AuthorizationRequest) (contracts.Decision, error) {
		return contracts.Decision{Allow: true}, nil
	}), DataURL: data.URL, ServiceToken: strings.Repeat("x", 32)})
	request := func(method, path, tenantID, etag string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r = r.WithContext(platform.WithPrincipal(r.Context(), contracts.Principal{Subject: "action-author", TenantID: tenantID}))
		if etag != "" {
			r.Header.Set("If-Match", etagHeader(etag))
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, status int, out any) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("HTTP %d, want %d: %s", w.Code, status, w.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	organizations := func(tenantID string) (contracts.Organization, contracts.Organization) {
		t.Helper()
		var category, group contracts.Organization
		decode(request("POST", "/metadata/categories", tenantID, "", map[string]string{"name": "动作目标验收"}), 201, &category)
		decode(request("POST", "/metadata/groups", tenantID, "", map[string]string{"name": "动作测试", "categoryId": category.ID}), 201, &group)
		return category, group
	}
	category, group := organizations(tenant)
	otherCategory, otherGroup := organizations(otherTenant)
	publish := func(tenantID string, record contracts.CurrentDefinition) {
		t.Helper()
		var pub contracts.Publication
		decode(request("POST", "/metadata/definitions/"+record.Definition.ID+"/publish", tenantID, record.ETag, nil), 200, &pub)
		if pub.Status != "succeeded" {
			t.Fatalf("publication incomplete: %+v", pub)
		}
	}
	createSchema := func(tenantID, categoryID, groupID, name string, active bool) contracts.CurrentDefinition {
		t.Helper()
		d := validSchema()
		d.ID, d.TenantID, d.CategoryID, d.GroupID, d.Name = "", "", categoryID, groupID, name
		var out contracts.CurrentDefinition
		decode(request("POST", "/metadata/definitions", tenantID, "", d), 201, &out)
		if active {
			publish(tenantID, out)
		}
		return out
	}
	first := createSchema(tenant, category.ID, group.ID, "生产设备", true)
	second := createSchema(tenant, category.ID, group.ID, "设备档案", true)
	unpublished := createSchema(tenant, category.ID, group.ID, "尚未生效对象", false)
	foreign := createSchema(otherTenant, otherCategory.ID, otherGroup.ID, "其他租户设备", true)
	makeAction := func(targets ...string) contracts.Definition {
		d := validAction()
		d.ID, d.TenantID, d.CategoryID, d.GroupID = "", "", category.ID, group.ID
		d.Action.TargetSchemaIDs = targets
		return d
	}
	for _, id := range []string{unpublished.Definition.ID, foreign.Definition.ID, contracts.NewID(), strings.ToUpper(first.Definition.ID)} {
		decode(request("POST", "/metadata/definitions", tenant, "", makeAction(id)), 422, nil)
	}
	var current contracts.CurrentDefinition
	decode(request("POST", "/metadata/definitions", tenant, "", makeAction(first.Definition.ID)), 201, &current)
	publish(tenant, current)
	var dependencyCount int
	if err := engine.DB().QueryRow("SELECT COUNT(*) FROM af_meta_dependencies WHERE tenant_id=? AND source_id=? AND target_id=?", tenant, current.Definition.ID, first.Definition.ID).Scan(&dependencyCount); err != nil || dependencyCount != 1 {
		t.Fatalf("published target dependency not persisted: count=%d error=%v", dependencyCount, err)
	}
	// A different same-tenant definition kind must not become an Action target.
	decode(request("POST", "/metadata/definitions", tenant, "", makeAction(current.Definition.ID)), 422, nil)
	modified := current.Definition
	modified.Action = &contracts.Callable{SideEffects: "dataWrite", TargetSchemaIDs: []string{second.Definition.ID}}
	var draft contracts.CurrentDefinition
	decode(request("PUT", "/metadata/definitions/"+current.Definition.ID+"/draft", tenant, current.ETag, modified), 200, &draft)
	decode(request("PUT", "/metadata/definitions/"+current.Definition.ID+"/draft", tenant, current.ETag, modified), 412, nil)
	var stillCurrent contracts.CurrentDefinition
	decode(request("GET", "/metadata/definitions/"+current.Definition.ID, tenant, "", nil), 200, &stillCurrent)
	if !reflect.DeepEqual(stillCurrent.Definition.Action.TargetSchemaIDs, []string{first.Definition.ID}) {
		t.Fatal("draft targets leaked into current definition")
	}
	// A target becoming unavailable after draft save must be rechecked before
	// publication creates a durable intent or changes the current dependency.
	if _, err := engine.DB().Exec("UPDATE af_meta_definitions SET status='draft' WHERE tenant_id=? AND id=?", tenant, second.Definition.ID); err != nil {
		t.Fatal(err)
	}
	decode(request("POST", "/metadata/definitions/"+draft.Definition.ID+"/publish", tenant, draft.ETag, nil), 422, nil)
	if _, err := engine.DB().Exec("UPDATE af_meta_definitions SET status='active' WHERE tenant_id=? AND id=?", tenant, second.Definition.ID); err != nil {
		t.Fatal(err)
	}
	publish(tenant, draft)
	decode(request("GET", "/metadata/definitions/"+current.Definition.ID, tenant, "", nil), 200, &stillCurrent)
	if !reflect.DeepEqual(stillCurrent.Definition.Action.TargetSchemaIDs, []string{second.Definition.ID}) {
		t.Fatal("target-only Action update did not publish")
	}
	if err := engine.DB().QueryRow("SELECT COUNT(*) FROM af_meta_dependencies WHERE tenant_id=? AND source_id=? AND target_id=?", tenant, current.Definition.ID, first.Definition.ID).Scan(&dependencyCount); err != nil || dependencyCount != 0 {
		t.Fatal("superseded Action target dependency remained active")
	}
	// Unbinding is also an explicit draft/publication change, not an implicit
	// association with every schema or an invocation authorization grant.
	modified.Action.TargetSchemaIDs = nil
	decode(request("PUT", "/metadata/definitions/"+current.Definition.ID+"/draft", tenant, stillCurrent.ETag, modified), 200, &draft)
	publish(tenant, draft)
	stillCurrent = contracts.CurrentDefinition{}
	decode(request("GET", "/metadata/definitions/"+current.Definition.ID, tenant, "", nil), 200, &stillCurrent)
	if len(stillCurrent.Definition.Action.TargetSchemaIDs) != 0 {
		t.Fatal("explicit Action target removal was lost")
	}
}
