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
)

// The real Metadata DB exercises one-time binding, dependency validation and
// immutable publication. Data is a request-bound stub; no Link DDL is claimed.
func TestMySQLLinkMappingLifecycleAndIsolation(t *testing.T) {
	dsn := os.Getenv("METADATA_TEST_DSN")
	if dsn == "" {
		t.Skip("set METADATA_TEST_DSN for isolated Link mapping lifecycle")
	}
	ctx := context.Background()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal("open isolated Metadata database")
	}
	defer engine.Close()
	if err := storage.Migrate(ctx, engine.DB(), "metadata", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant, otherTenant := "link-mapping-"+c.NewID(), "link-mapping-"+c.NewID()
	defer func() {
		for _, table := range []string{"af_meta_dependencies", "af_meta_outbox", "af_meta_publications", "af_meta_definitions", "af_meta_organizations", "af_meta_tenant_guards"} {
			if _, err := engine.DB().Exec("DELETE FROM "+table+" WHERE tenant_id IN (?,?)", tenant, otherTenant); err != nil {
				t.Errorf("clean isolated Link tenant: %v", err)
			}
		}
	}()
	var mappedPrepares, mappedActivations atomic.Int32
	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/publications/prepare" {
			var req c.PrepareRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Definition.Link != nil && req.Definition.Link.StorageBinding != nil {
				mappedPrepares.Add(1)
				if req.PreviousDigest == "" || req.Definition.Link.StorageBinding.Mode != "managed" {
					t.Error("one-time binding lost previous identity")
				}
			}
			writeJSON(w, 200, c.PrepareResult{OperationID: req.OperationID, Status: "prepared"})
			return
		}
		if r.URL.Path == "/internal/publications/activate" {
			var req c.ActivateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			var raw string
			if err := engine.DB().QueryRow("SELECT current_json FROM af_meta_definitions WHERE tenant_id=? AND id=?", r.Header.Get("X-Tenant-ID"), req.DefinitionID).Scan(&raw); err != nil {
				t.Error(err)
			}
			var current c.Definition
			_ = json.Unmarshal([]byte(raw), &current)
			if current.Link != nil && current.Link.StorageBinding != nil {
				mappedActivations.Add(1)
			}
			writeJSON(w, 200, map[string]string{"status": "ready"})
			return
		}
		w.WriteHeader(404)
	}))
	defer data.Close()
	deniedID := ""
	api := NewHandler(Config{Engine: engine, DataURL: data.URL, ServiceToken: strings.Repeat("x", 32), Authorizer: authorizerFunc(func(_ context.Context, principal c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		return c.Decision{Allow: principal.Subject != "restricted" || req.ResourceID != deniedID}, nil
	})})
	request := func(method, path, tenantID, etag string, body any) *httptest.ResponseRecorder {
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{Subject: "link-author", TenantID: tenantID}))
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
			t.Fatalf("HTTP %d want %d: %s", w.Code, status, w.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	organizations := func(tenantID string) (c.Organization, c.Organization) {
		var category, group c.Organization
		decode(request("POST", "/metadata/categories", tenantID, "", map[string]string{"name": "关系映射"}), 201, &category)
		decode(request("POST", "/metadata/groups", tenantID, "", map[string]string{"name": "关系测试", "categoryId": category.ID}), 201, &group)
		return category, group
	}
	category, group := organizations(tenant)
	otherCategory, otherGroup := organizations(otherTenant)
	publish := func(tenantID string, record c.CurrentDefinition) {
		t.Helper()
		var pub c.Publication
		decode(request("POST", "/metadata/definitions/"+record.Definition.ID+"/publish", tenantID, record.ETag, nil), 200, &pub)
		if pub.Status != "succeeded" {
			t.Fatal("publication incomplete")
		}
	}
	schema := func(tenantID, categoryID, groupID, name string, active bool) c.CurrentDefinition {
		d := validSchema()
		d.ID, d.TenantID, d.CategoryID, d.GroupID, d.Name = "", "", categoryID, groupID, name
		var record c.CurrentDefinition
		decode(request("POST", "/metadata/definitions", tenantID, "", d), 201, &record)
		if active {
			publish(tenantID, record)
		}
		return record
	}
	source := schema(tenant, category.ID, group.ID, "笔记", true)
	target := schema(tenant, category.ID, group.ID, "标签", true)
	draftEndpoint := schema(tenant, category.ID, group.ID, "未发布", false)
	foreign := schema(otherTenant, otherCategory.ID, otherGroup.ID, "外租户", true)
	d := validLinkMappingDefinition()
	d.ID, d.TenantID, d.CategoryID, d.GroupID = "", "", category.ID, group.ID
	d.Link.SourceSchemaID, d.Link.TargetSchemaID = source.Definition.ID, target.Definition.ID
	d.Link.Cardinality = "1:N"
	for _, id := range []string{draftEndpoint.Definition.ID, foreign.Definition.ID, c.NewID()} {
		invalid := cloneLinkDefinition(d)
		invalid.Link.TargetSchemaID = id
		decode(request("POST", "/metadata/definitions", tenant, "", invalid), 422, nil)
	}
	var current c.CurrentDefinition
	decode(request("POST", "/metadata/definitions", tenant, "", d), 201, &current)
	publish(tenant, current)
	// Exercise the actual MySQL direction-discovery route: DISTINCT combined
	// with ORDER BY an unselected ID must not turn the directory into HTTP 500.
	createLink := func(tenantID, categoryID, groupID, name, sourceID, targetID string, active bool) c.CurrentDefinition {
		t.Helper()
		definition := validLinkMappingDefinition()
		definition.ID, definition.TenantID, definition.CategoryID, definition.GroupID, definition.Name = "", "", categoryID, groupID, name
		definition.Link.SourceSchemaID, definition.Link.TargetSchemaID = sourceID, targetID
		definition.Link.ForwardName, definition.Link.ReverseName = name+"正向", name+"反向"
		var record c.CurrentDefinition
		decode(request("POST", "/metadata/definitions", tenantID, "", definition), 201, &record)
		if active {
			publish(tenantID, record)
		}
		return record
	}
	self := createLink(tenant, category.ID, group.ID, "自身目录关系", source.Definition.ID, source.Definition.ID, true)
	createLink(tenant, category.ID, group.ID, "未生效目录关系", target.Definition.ID, source.Definition.ID, false)
	createLink(tenant, category.ID, group.ID, "无关目录关系", target.Definition.ID, target.Definition.ID, true)
	foreignLink := createLink(otherTenant, otherCategory.ID, otherGroup.ID, "外租户目录关系", foreign.Definition.ID, foreign.Definition.ID, true)
	type navigation struct {
		LinkRef struct {
			ID string `json:"id"`
		} `json:"linkRef"`
		Direction      string `json:"direction"`
		Name           string `json:"name"`
		SourceSchemaID string `json:"sourceSchemaId"`
		TargetSchemaID string `json:"targetSchemaId"`
		Cardinality    string `json:"cardinality"`
	}
	directory := func(schemaID, tenantID, subject string, status int) []navigation {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/metadata/schemas/"+schemaID+"/links", nil)
		r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{Subject: subject, TenantID: tenantID}))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if status != http.StatusOK {
			decode(w, status, nil)
			return nil
		}
		var result struct {
			Data []navigation `json:"data"`
		}
		decode(w, status, &result)
		return result.Data
	}
	items := directory(source.Definition.ID, tenant, "link-author", http.StatusOK)
	if len(items) != 3 {
		t.Fatalf("source directory must contain one outgoing and both self directions, got %+v", items)
	}
	seen := map[string]bool{}
	for i, item := range items {
		key := item.LinkRef.ID + ":" + item.Direction
		if seen[key] || (i > 0 && items[i-1].LinkRef.ID > item.LinkRef.ID) {
			t.Fatalf("directory duplicated or reordered a Link: %+v", items)
		}
		seen[key] = true
		if item.SourceSchemaID != source.Definition.ID {
			t.Fatalf("navigation origin differs from requested Schema: %+v", item)
		}
		if item.LinkRef.ID == current.Definition.ID {
			if item.Direction != "out" || item.Name != current.Definition.Link.ForwardName || item.TargetSchemaID != target.Definition.ID || item.Cardinality != "1:N" {
				t.Fatalf("invalid outgoing navigation: %+v", item)
			}
		} else if item.LinkRef.ID != self.Definition.ID || item.TargetSchemaID != source.Definition.ID {
			t.Fatalf("draft, unrelated or foreign Link leaked into directory: %+v", item)
		}
	}
	if !seen[self.Definition.ID+":out"] || !seen[self.Definition.ID+":in"] {
		t.Fatal("self relation lost a direction")
	}
	foundReverse := false
	for _, item := range directory(target.Definition.ID, tenant, "link-author", http.StatusOK) {
		if item.LinkRef.ID == current.Definition.ID {
			foundReverse = true
			if item.Direction != "in" || item.Name != current.Definition.Link.ReverseName || item.TargetSchemaID != source.Definition.ID || item.Cardinality != "N:1" {
				t.Fatalf("invalid reverse navigation: %+v", item)
			}
		}
	}
	if !foundReverse {
		t.Fatal("target directory lost its incoming relation")
	}
	for _, hidden := range []string{current.Definition.ID, target.Definition.ID} {
		deniedID = hidden
		if items := directory(source.Definition.ID, tenant, "restricted", http.StatusOK); len(items) != 2 || items[0].LinkRef.ID != self.Definition.ID || items[1].LinkRef.ID != self.Definition.ID {
			t.Fatalf("unreadable Link or opposite endpoint leaked: %+v", items)
		}
	}
	deniedID = source.Definition.ID
	directory(source.Definition.ID, tenant, "restricted", http.StatusForbidden)
	directory(source.Definition.ID, otherTenant, "link-author", http.StatusNotFound)
	directory(foreign.Definition.ID, tenant, "link-author", http.StatusNotFound)
	directory(draftEndpoint.Definition.ID, tenant, "link-author", http.StatusNotFound)
	if items := directory(foreign.Definition.ID, otherTenant, "link-author", http.StatusOK); len(items) != 2 || items[0].LinkRef.ID != foreignLink.Definition.ID || items[1].LinkRef.ID != foreignLink.Definition.ID {
		t.Fatalf("foreign tenant directory was not isolated: %+v", items)
	}
	bound := cloneLinkDefinition(current.Definition)
	bound.Link.StorageBinding = &c.LinkStorageBinding{Mode: "managed"}
	var draft c.CurrentDefinition
	decode(request("PUT", "/metadata/definitions/"+bound.ID+"/draft", tenant, current.ETag, bound), 200, &draft)
	decode(request("PUT", "/metadata/definitions/"+bound.ID+"/draft", tenant, current.ETag, bound), 412, nil)
	var unchanged c.CurrentDefinition
	decode(request("GET", "/metadata/definitions/"+bound.ID, tenant, "", nil), 200, &unchanged)
	if unchanged.Definition.Link.StorageBinding != nil {
		t.Fatal("draft binding leaked into current")
	}
	if _, err := engine.DB().Exec("UPDATE af_meta_definitions SET status='draft' WHERE tenant_id=? AND id=?", tenant, target.Definition.ID); err != nil {
		t.Fatal(err)
	}
	decode(request("POST", "/metadata/definitions/"+bound.ID+"/publish", tenant, draft.ETag, nil), 422, nil)
	if _, err := engine.DB().Exec("UPDATE af_meta_definitions SET status='active' WHERE tenant_id=? AND id=?", tenant, target.Definition.ID); err != nil {
		t.Fatal(err)
	}
	publish(tenant, draft)
	if mappedPrepares.Load() != 1 || mappedActivations.Load() != 1 {
		t.Fatalf("bound publication skipped Data coordination: %d/%d", mappedPrepares.Load(), mappedActivations.Load())
	}
	decode(request("GET", "/metadata/definitions/"+bound.ID, tenant, "", nil), 200, &current)
	changed := cloneLinkDefinition(current.Definition)
	changed.Link.StorageBinding = nil
	decode(request("PUT", "/metadata/definitions/"+bound.ID+"/draft", tenant, current.ETag, changed), 200, &draft)
	decode(request("POST", "/metadata/definitions/"+bound.ID+"/preview", tenant, draft.ETag, nil), 422, nil)
	decode(request("POST", "/metadata/definitions/"+bound.ID+"/publish", tenant, draft.ETag, nil), 422, nil)
	if mappedPrepares.Load() != 1 {
		t.Fatal("unsupported binding removal reached Data")
	}
}
