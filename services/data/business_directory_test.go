package data

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func serveBusinessDirectoryFixture(t *testing.T, w http.ResponseWriter, r *http.Request, definitions []c.Definition, decision func(c.Definition) c.Decision) bool {
	t.Helper()
	if r.URL.Path != "/internal/metadata/business-directory" {
		return false
	}
	var input c.BusinessDirectoryRequest
	if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&input) != nil || input.Check.Token != strings.Repeat("s", 64) || input.Check.Path == "" || r.Header.Get("X-Tenant-ID") != "" {
		t.Error("business directory lost authoritative caller check")
		w.WriteHeader(400)
		return true
	}
	ordered := append([]c.Definition{}, definitions...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	out := c.BusinessDirectoryPage{Entries: []c.BusinessDirectoryEntry{}}
	for _, d := range ordered {
		if d.Kind != c.KindSchema || d.ID <= input.After || input.SchemaID != "" && d.ID != input.SchemaID || input.CategoryID != "" && d.CategoryID != input.CategoryID {
			continue
		}
		out.Scanned++
		grant := decision(d)
		if !grant.Allow {
			continue
		}
		if len(out.Entries) == input.Limit {
			out.HasMore = true
			out.NextCursor = out.Entries[len(out.Entries)-1].Current.Definition.ID
			break
		}
		out.Entries = append(out.Entries, c.BusinessDirectoryEntry{Current: c.CurrentDefinition{Definition: d, ETag: "active", Status: "active"}, Category: "生产管理", Decision: grant})
	}
	writeJSON(w, 200, out)
	return true
}

func TestBusinessDirectoryBytePagingBindsIdentityAndQuery(t *testing.T) {
	definitions := []c.Definition{}
	for i := 0; i < 50; i++ {
		d := testDefinition()
		d.ID = fmt.Sprintf("%08x-0000-0000-0000-000000000001", i+1)
		d.CategoryID = "category-a"
		d.Name = fmt.Sprintf("模型%02d", i)
		d.Schema.Properties = nil
		d.Schema.Indexes = nil
		for j := 0; j < 100; j++ {
			d.Schema.Properties = append(d.Schema.Properties, c.Property{ID: fmt.Sprintf("%08x-0000-0000-0001-%012x", i+1, j+1), Name: strings.Repeat("字", 125) + fmt.Sprintf("%03d", j), DataType: "text", Nullable: true})
		}
		if e := validateSchema(d); e != nil {
			t.Fatal(e)
		}
		definitions = append(definitions, d)
	}
	state := "state-1"
	grant := func(c.Definition) c.Decision { return c.Decision{Allow: true, State: state, ReadFields: []string{"*"}} }
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveBusinessDirectoryFixture(t, w, r, definitions, grant) {
			return
		}
		for _, d := range definitions {
			if r.URL.Path == "/metadata/definitions/"+d.ID {
				writeJSON(w, 200, c.CurrentDefinition{Definition: d, ETag: "active", Status: "active"})
				return
			}
		}
		w.WriteHeader(404)
	}))
	defer metadata.Close()
	app := NewHandler(Config{MetadataURL: metadata.URL, ServiceToken: strings.Repeat("x", 32), CursorSecret: strings.Repeat("c", 32), Authorizer: authorizerFunc(func(_ context.Context, p c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		if req.Operation != "instance.query" {
			t.Error("business discovery requested whole-definition permission")
		}
		return grant(c.Definition{}), nil
	})})
	cursor, firstCursor := "", ""
	seen := map[string]bool{}
	pages := 0
	pageBytes := []int{}
	for {
		w := callOntologySQL(t, app, "GET", "/data/query/context?limit=50&cursor="+url.QueryEscape(cursor), nil)
		var out c.QueryContext
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Body.Len() > queryDirectoryMaxBytes || len(out.Models) == 0 {
			t.Fatalf("directory page status=%d bytes=%d", w.Code, w.Body.Len())
		}
		pages++
		pageBytes = append(pageBytes, w.Body.Len())
		for _, m := range out.Models {
			if seen[m.SchemaID] || len(m.Fields) != 100 {
				t.Fatal("directory lost, duplicated or truncated model fields")
			}
			seen[m.SchemaID] = true
		}
		if out.NextCursor == "" {
			break
		}
		if len(out.NextCursor) > 4096 || uuidPattern.MatchString(out.NextCursor) {
			t.Fatal("directory cursor is not bounded and opaque")
		}
		if firstCursor == "" {
			firstCursor = out.NextCursor
		}
		cursor = out.NextCursor
		if pages > 10 {
			t.Fatal("directory did not terminate")
		}
	}
	if len(seen) != 50 || pages < 2 {
		t.Fatalf("wrong byte paging: pages=%d models=%d", pages, len(seen))
	}
	t.Logf("50 models / 5000 complete fields returned in %d pages; JSON bytes per page=%v", pages, pageBytes)
	for _, change := range []string{"tamper", "tenant", "subject", "category", "search", "limit", "state"} {
		t.Run(change, func(t *testing.T) {
			query := url.Values{"limit": {"50"}, "cursor": {firstCursor}}
			principal := c.Principal{TenantID: "tenant-a", Subject: "reader"}
			switch change {
			case "tamper":
				query.Set("cursor", firstCursor+"x")
			case "tenant":
				principal.TenantID = "other"
			case "subject":
				principal.Subject = "other"
			case "category":
				query.Set("categoryId", "other")
			case "search":
				query.Set("search", "other")
			case "limit":
				query.Set("limit", "49")
			case "state":
				state = "state-2"
				defer func() { state = "state-1" }()
			}
			r := httptest.NewRequest("GET", "/data/query/context?"+query.Encode(), nil)
			r.Header.Set("Cookie", "ontology_session="+strings.Repeat("s", 64))
			r = r.WithContext(platform.WithPrincipal(r.Context(), principal))
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != 422 {
				t.Fatalf("unbound cursor accepted: %d", w.Code)
			}
		})
	}
}

func TestFormDiscoveryUsesBusinessFieldsAndWriteOperation(t *testing.T) {
	d := testDefinition()
	d.CategoryID = "category-a"
	d.Schema.Properties[0].Default = raw("SENSITIVE_DEFAULT")
	d.Schema.Properties[0].Mapping = &c.PropertyMapping{Column: "SENSITIVE_COLUMN"}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.CurrentDefinition{Definition: d, ETag: "active", Status: "active"})
	}))
	defer metadata.Close()
	query := c.Decision{Allow: true, State: "state", ReadFields: []string{testFieldCode, testFieldDept}}
	create := c.Decision{Allow: true, State: "state", WriteFields: []string{testFieldCode}}
	patch := c.Decision{Allow: true, State: "state", WriteFields: []string{testFieldDept}}
	app := NewHandler(Config{MetadataURL: metadata.URL, ServiceToken: strings.Repeat("s", 32), Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		switch req.Operation {
		case "instance.query":
			return query, nil
		case "instance.create":
			return create, nil
		case "instance.patch":
			return patch, nil
		default:
			t.Error("form requested definition or unrelated permission")
			return c.Decision{}, nil
		}
	})})
	for _, operation := range []string{"create", "patch"} {
		w := callOntologySQL(t, app, "GET", "/data/schemas/"+d.ID+"/form?operation="+operation, nil)
		var out c.FormDefinition
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Definition.Schema.Properties) != 2 {
			t.Fatalf("form status=%d", w.Code)
		}
		if strings.Contains(w.Body.String(), "SENSITIVE") || strings.Contains(w.Body.String(), "采购价") || strings.Contains(w.Body.String(), "mapping") || strings.Contains(w.Body.String(), "default") {
			t.Fatal("private definition material escaped form")
		}
		fields := out.Definition.Schema.Properties
		if !out.CreateAllowed || !out.PatchAllowed || fields[0].ReadOnly != (operation == "patch") || fields[1].ReadOnly != (operation == "create") {
			t.Fatal("wrong operation-specific form fields")
		}
	}
	create, patch = c.Decision{}, c.Decision{}
	w := callOntologySQL(t, app, "GET", "/data/schemas/"+d.ID+"/form", nil)
	var out c.FormDefinition
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.CreateAllowed || out.PatchAllowed {
		t.Fatal("read-only form discovery failed")
	}
	for _, field := range out.Definition.Schema.Properties {
		if !field.ReadOnly {
			t.Fatal("denied write permission appeared writable")
		}
	}
	// A mapped schema advertises only the read-only marker, never its binding.
	d.Schema.Properties[0].Default = nil
	d.Schema.StorageBinding = &c.StorageBinding{DataSourceID: "11111111-1111-4111-8111-111111111111", Table: "PRIVATE_TABLE", IDColumn: "PRIVATE_ID"}
	for i := range d.Schema.Properties {
		d.Schema.Properties[i].Mapping = &c.PropertyMapping{DataSourceID: d.Schema.StorageBinding.DataSourceID, Column: fmt.Sprintf("PRIVATE_FIELD_%d", i)}
	}
	create, patch = c.Decision{Allow: true, State: "state", WriteFields: []string{"*"}}, c.Decision{Allow: true, State: "state", WriteFields: []string{"*"}}
	w = callOntologySQL(t, app, "GET", "/data/schemas/"+d.ID+"/form", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Definition.Schema.StorageBinding || out.CreateAllowed || out.PatchAllowed || strings.Contains(w.Body.String(), "PRIVATE") {
		t.Fatalf("mapped form status=%d", w.Code)
	}
	for _, field := range out.Definition.Schema.Properties {
		if !field.ReadOnly {
			t.Fatal("mapped form advertised writable field")
		}
	}
}

func TestFormDiscoveryRejectsAuthorizationChangeBeforeResponse(t *testing.T) {
	d := testDefinition()
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.CurrentDefinition{Definition: d, ETag: "active", Status: "active"})
	}))
	defer metadata.Close()
	checks := 0
	app := NewHandler(Config{MetadataURL: metadata.URL, ServiceToken: strings.Repeat("s", 32), Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		if req.Operation == "instance.query" {
			checks++
			if checks > 1 {
				return c.Decision{}, nil
			}
			return c.Decision{Allow: true, State: "state", ReadFields: []string{"*"}}, nil
		}
		return c.Decision{}, nil
	})})
	w := callOntologySQL(t, app, "GET", "/data/schemas/"+d.ID+"/form", nil)
	if w.Code != 409 || strings.Contains(w.Body.String(), "设备编码") {
		t.Fatalf("form disclosed fields after authorization changed: %d", w.Code)
	}
}
