package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

// This opt-in test uses fresh tenant and object identities; it never deletes
// existing records or takes ownership of another test's definitions.
func TestMySQLDraftDirectoryViewsAndEditAuthorization(t *testing.T) {
	dsn := os.Getenv("METADATA_TEST_DSN")
	if dsn == "" {
		t.Skip("set METADATA_TEST_DSN to verify draft discovery and authorization on real MySQL")
	}
	engine, err := storage.Open(context.Background(), storage.Config{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := storage.Migrate(context.Background(), engine.DB(), "metadata", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant := "draft-directory-" + contracts.NewID()
	hiddenID := ""
	authorizer := authorizerFunc(func(_ context.Context, principal contracts.Principal, req contracts.AuthorizationRequest) (contracts.Decision, error) {
		allow := true
		if principal.Subject == "reader" && req.Operation == "definition.edit" || principal.Subject == "editor" && req.Operation == "definition.read" {
			allow = false
		}
		if principal.Subject == "restricted" && req.ResourceID == hiddenID {
			allow = false
		}
		return contracts.Decision{Allow: allow}, nil
	})
	api := NewHandler(Config{Engine: engine, Authorizer: authorizer})
	request := func(method, path, subject, tenantID, etag string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r = r.WithContext(platform.WithPrincipal(r.Context(), contracts.Principal{Subject: subject, TenantID: tenantID}))
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
	var category, group contracts.Organization
	decode(request("POST", "/metadata/categories", "admin", tenant, "", map[string]string{"name": "草稿目录分类"}), 201, &category)
	decode(request("POST", "/metadata/groups", "admin", tenant, "", map[string]string{"name": "模型分组", "categoryId": category.ID}), 201, &group)
	create := func(name string) contracts.CurrentDefinition {
		t.Helper()
		d := validSchema()
		d.ID, d.TenantID, d.CategoryID, d.GroupID, d.Name = "", "", category.ID, group.ID, name
		var out contracts.CurrentDefinition
		decode(request("POST", "/metadata/definitions", "admin", tenant, "", d), 201, &out)
		return out
	}
	active := create("当前名称")
	// Seed only this test's current state; the production publish protocol is
	// independently covered by lifecycle and cross-service integration tests.
	raw, _ := json.Marshal(active.Definition)
	if _, err := engine.DB().Exec("UPDATE af_meta_definitions SET current_json=?,current_digest=?,draft_json=NULL,draft_digest='',status='active' WHERE tenant_id=? AND id=?", string(raw), active.ETag, tenant, active.Definition.ID); err != nil {
		t.Fatal(err)
	}
	active.Definition.Name = "未生效改名"
	var revised contracts.CurrentDefinition
	decode(request("PUT", "/metadata/definitions/"+active.Definition.ID+"/draft", "admin", tenant, active.ETag, active.Definition), 200, &revised)
	unpublished := create("仅草稿模型")
	type directory struct {
		Data       []contracts.CurrentDefinition `json:"data"`
		HasMore    bool                          `json:"hasMore"`
		NextCursor string                        `json:"nextCursor"`
	}
	var current, drafts directory
	decode(request("GET", "/metadata/definitions", "admin", tenant, "", nil), 200, &current)
	if len(current.Data) != 1 || current.Data[0].Definition.Name != "当前名称" || current.Data[0].ETag != active.ETag || current.Data[0].Status != "active" {
		t.Fatalf("current directory mixed in draft contents: %+v", current)
	}
	decode(request("GET", "/metadata/definitions?view=draft", "editor", tenant, "", nil), 200, &drafts)
	if len(drafts.Data) != 2 {
		t.Fatalf("draft directory omitted unpublished or edited definition: %+v", drafts)
	}
	for _, d := range drafts.Data {
		if d.Status != "draft" || d.Definition.Name == "当前名称" {
			t.Fatal("draft directory returned a current record")
		}
		if d.Definition.ID == active.Definition.ID && (d.Definition.Name != "未生效改名" || d.ETag != revised.ETag) {
			t.Fatal("draft directory used the current ETag or content")
		}
	}
	decode(request("GET", "/metadata/definitions?view=draft", "reader", tenant, "", nil), 403, nil)
	decode(request("GET", "/metadata/definitions?view=current", "editor", tenant, "", nil), 403, nil)
	decode(request("GET", "/metadata/definitions?view=current", "reader", tenant, "", nil), 200, &current)
	hiddenID = active.Definition.ID
	var visible directory
	w := request("GET", "/metadata/definitions?view=draft&limit=1", "restricted", tenant, "", nil)
	decode(w, 200, &visible)
	if len(visible.Data) != 1 || visible.Data[0].Definition.ID != unpublished.Definition.ID || visible.HasMore || visible.NextCursor != "" || strings.Contains(w.Body.String(), hiddenID) {
		t.Fatalf("hidden draft leaked through directory or continuation: %s", w.Body.String())
	}
	var other directory
	decode(request("GET", "/metadata/definitions?view=draft", "admin", tenant+"-other", "", nil), 200, &other)
	if len(other.Data) != 0 || other.NextCursor != "" || other.HasMore {
		t.Fatal("draft directory crossed tenant boundary")
	}
	decode(request("GET", "/metadata/definitions?view=all", "admin", tenant, "", nil), 400, nil)
	decode(request("GET", "/metadata/definitions?view=draft&categoryId="+category.ID+"&kind=enum", "admin", tenant, "", nil), 200, &other)
	if len(other.Data) != 0 {
		t.Fatal("draft directory ignored category/kind filters")
	}
}
