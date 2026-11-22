package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func TestMySQLDefinitionDependencyImpact(t *testing.T) {
	dsn := os.Getenv("METADATA_TEST_DSN")
	if dsn == "" {
		t.Skip("set METADATA_TEST_DSN for isolated dependency impact tests")
	}
	ctx := context.Background()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	if err := storage.Migrate(ctx, engine.DB(), "metadata", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant, foreign := "impact-"+c.NewID(), "impact-"+c.NewID()
	t.Cleanup(func() {
		if _, err := engine.DB().Exec("DELETE FROM af_meta_definitions WHERE tenant_id IN (?,?)", tenant, foreign); err != nil {
			t.Error(err)
		}
	})
	denied := map[string]bool{}
	checks := map[string]int{}
	var failure error
	revokeID := ""
	var onAuthorize func(c.AuthorizationRequest)
	api := NewHandler(Config{Engine: engine, ServiceToken: strings.Repeat("s", 32), Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		if failure != nil {
			return c.Decision{}, failure
		}
		key := req.Operation + ":" + req.ResourceID
		checks[key]++
		if onAuthorize != nil {
			onAuthorize(req)
		}
		return c.Decision{Allow: !denied[key] && !(req.ResourceID == revokeID && checks[key] > 1)}, nil
	})})
	definition := func(kind c.Kind, name string) c.Definition {
		return c.Definition{ID: c.NewID(), TenantID: tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: kind, Name: name}
	}
	store := func(current, draft *c.Definition) {
		t.Helper()
		d := current
		if d == nil {
			d = draft
		}
		var currentJSON, draftJSON any
		if current != nil {
			raw, _ := json.Marshal(current)
			currentJSON = string(raw)
		}
		if draft != nil {
			raw, _ := json.Marshal(draft)
			draftJSON = string(raw)
		}
		_, err := engine.DB().Exec(`INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,current_json,current_digest,draft_json,draft_digest,status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
 ON DUPLICATE KEY UPDATE current_json=VALUES(current_json),current_digest=VALUES(current_digest),draft_json=VALUES(draft_json),draft_digest=VALUES(draft_digest)`, d.ID, d.TenantID, d.CategoryID, d.GroupID, d.Kind, d.Name, []byte(d.Name), currentJSON, "current-etag", draftJSON, "draft-etag", "active")
		if err != nil {
			t.Fatal(err)
		}
	}
	request := func(path, tenantID string, want int) c.DefinitionDependencyImpact {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{Subject: "impact-reader", TenantID: tenantID}))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("HTTP %d want %d: %s", w.Code, want, w.Body.String())
		}
		var result c.DefinitionDependencyImpact
		if want == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("report must not be cached")
			}
		}
		if strings.Contains(w.Body.String(), "PRIVATE_SOURCE") {
			t.Fatal("source leaked")
		}
		return result
	}
	enum := definition(c.KindEnum, "状态")
	enum.Enum = &c.Enum{}
	schema := definition(c.KindSchema, "工单")
	schema.Schema = &c.Schema{Properties: []c.Property{{EnumID: enum.ID}}}
	link := definition(c.KindLink, "自关联")
	link.Link = &c.Link{SourceSchemaID: schema.ID, TargetSchemaID: schema.ID}
	fn := definition(c.KindFunction, "写入函数")
	fn.Function = &c.Callable{DataWrite: []c.DataWriteCapability{{SchemaID: schema.ID}}, ArtifactRef: "PRIVATE_SOURCE"}
	action := definition(c.KindAction, "动作当前")
	action.Action = &c.Callable{FunctionID: fn.ID, TargetSchemaIDs: []string{schema.ID}}
	draft := action
	draft.Name = "动作草稿"
	draft.Action = &c.Callable{Input: []c.Property{{EnumID: enum.ID}}}
	unpublished := definition(c.KindFunction, "未发布函数")
	unpublished.Function = &c.Callable{DataWrite: []c.DataWriteCapability{{SchemaID: schema.ID}}}
	for _, d := range []*c.Definition{&enum, &schema, &link, &fn} {
		store(d, nil)
	}
	store(&action, &draft)
	store(nil, &unpublished)
	url := "/metadata/definitions/" + schema.ID + "/dependencies"
	result := request(url, tenant, 200)
	if result.Scope != "direct-definition-references" || result.Definition.ID != schema.ID || len(result.Dependencies) != 1 || result.Dependencies[0].Definition.ID != enum.ID || len(result.Dependents) != 4 {
		t.Fatalf("wrong direct report: %+v", result)
	}
	for _, edge := range result.Dependents {
		if edge.Definition.ID == link.ID && len(edge.Reasons) != 2 {
			t.Fatal("self link duplicated/lost direction")
		}
	}
	result = request("/metadata/definitions/"+enum.ID+"/dependencies", tenant, 200)
	if len(result.Dependents) != 2 {
		t.Fatalf("saved draft reference missing: %+v", result)
	}
	result = request("/metadata/definitions/"+action.ID+"/dependencies?view=draft", tenant, 200)
	if result.Definition.Name != draft.Name || result.Definition.ETag != "draft-etag" || len(result.Dependencies) != 1 || result.Dependencies[0].Definition.ID != enum.ID {
		t.Fatalf("draft/current mixed: %+v", result)
	}
	request("/metadata/definitions/"+unpublished.ID+"/dependencies", tenant, 404)
	request("/metadata/definitions/"+unpublished.ID+"/dependencies?view=draft", tenant, 200)
	request(url, foreign, 404)
	denied["definition.read:"+schema.ID] = true
	request(url, tenant, 404)
	delete(denied, "definition.read:"+schema.ID)
	denied["definition.read:"+enum.ID] = true
	denied["definition.read:"+link.ID] = true
	denied["definition.edit:"+unpublished.ID] = true
	result = request(url, tenant, 200)
	if len(result.Dependencies) != 0 || len(result.Dependents) != 2 {
		t.Fatalf("unauthorized identities/counts leaked: %+v", result)
	}
	denied = map[string]bool{"definition.edit:" + action.ID: true}
	request("/metadata/definitions/"+action.ID+"/dependencies?view=draft", tenant, 404)
	result = request("/metadata/definitions/"+enum.ID+"/dependencies", tenant, 200)
	if len(result.Dependents) != 1 {
		t.Fatal("draft edit grant was ignored")
	}
	denied = map[string]bool{}
	checks = map[string]int{}
	revokeID = link.ID
	request(url, tenant, 409)
	revokeID = ""
	for _, tc := range []struct {
		id     string
		hidden bool
		want   int
	}{{schema.ID, false, 409}, {link.ID, false, 409}, {link.ID, true, 200}} {
		checks = map[string]int{}
		denied = map[string]bool{"definition.read:" + tc.id: tc.hidden}
		onAuthorize = func(req c.AuthorizationRequest) {
			if req.ResourceID != fn.ID || checks["definition.read:"+fn.ID] != 2 {
				return
			}
			if _, err := engine.DB().Exec("UPDATE af_meta_definitions SET current_digest=? WHERE tenant_id=? AND id=?", c.NewID(), tenant, tc.id); err != nil {
				t.Fatal(err)
			}
		}
		request(url, tenant, tc.want)
	}
	onAuthorize = nil
	denied = map[string]bool{}
	failure = errors.New("OPA unavailable")
	request(url, tenant, 503)
	failure = nil
	for _, suffix := range []string{"?view=all", "?view=current&view=draft", "?after=hidden", "?version=old", "?view=%ZZ", "?view=current;after=hidden"} {
		request(url+suffix, tenant, 400)
	}
	r := httptest.NewRequest("GET", url, nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	r.Header.Set("X-Tenant-ID", tenant)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("service identity must not access impact reports")
	}
	// A saved draft changes immediately without touching the active dependency index.
	draft.Action = &c.Callable{}
	store(&action, &draft)
	result = request("/metadata/definitions/"+enum.ID+"/dependencies", tenant, 200)
	if len(result.Dependents) != 1 {
		t.Fatal("draft reference removal not reflected")
	}
	// All failure budgets reject the whole response, never a misleading prefix.
	for _, budget := range []string{"definitions", "bytes", "single-json", "edges", "output"} {
		t.Run(budget, func(t *testing.T) {
			if _, err := engine.DB().Exec("DELETE FROM af_meta_definitions WHERE tenant_id=?", foreign); err != nil {
				t.Fatal(err)
			}
			root := definition(c.KindSchema, "预算根")
			root.TenantID = foreign
			root.Schema = &c.Schema{}
			store(&root, nil)
			n := 1
			switch budget {
			case "definitions":
				n = 1000
			case "bytes":
				n = 9
			case "edges":
				n = 9
			case "output":
				n = 201
			}
			for i := 0; i < n; i++ {
				d := definition(c.KindAction, c.NewID())
				d.TenantID = foreign
				d.Action = &c.Callable{TargetSchemaIDs: []string{root.ID}}
				switch budget {
				case "bytes":
					d.Description = strings.Repeat("x", (1<<20)-1024)
				case "single-json":
					d.Description = strings.Repeat("x", (1<<20)+1)
				case "edges":
					for j := 0; j < 500; j++ {
						d.Action.TargetSchemaIDs = append(d.Action.TargetSchemaIDs, c.NewID())
					}
				}
				store(&d, nil)
			}
			request("/metadata/definitions/"+root.ID+"/dependencies", foreign, 422)
		})
	}
	if _, err := engine.DB().Exec(`INSERT INTO af_meta_definitions
 (id,tenant_id,category_id,group_id,kind,name,name_key,status)
 WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<?)
 SELECT UUID(),?,?,?,'action',CONCAT('deleted-',n),CONCAT('deleted-',n),'deleted' FROM seq`,
		impactDefinitionBudget, tenant, c.NewID(), c.NewID()); err != nil {
		t.Fatal(err)
	}
	request(url, tenant, 200)
	// The reader never created a publication guard or wrote a dependency index.
	var count int
	if err := engine.DB().QueryRow("SELECT COUNT(*) FROM af_meta_tenant_guards WHERE tenant_id=?", tenant).Scan(&count); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("read mutated tenant guard")
	}
}
