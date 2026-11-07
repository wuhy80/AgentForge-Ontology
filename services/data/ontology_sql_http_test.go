package data

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf16"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func ontologySQLTestHandler(t *testing.T, definition c.Definition, queryDecision c.Decision) http.Handler {
	t.Helper()
	definition.CategoryID = "category-a"
	hidden := definition
	hidden.ID, hidden.Name = "ffffffff-ffff-ffff-ffff-ffffffffffff", "不可见模型"
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metadata/categories", "/metadata/definitions":
			if r.Header.Get("Authorization") != "Bearer caller-token" || r.Header.Get("Cookie") != "ontology_session=caller-session" || r.Header.Get("X-Tenant-ID") != "" {
				t.Error("directory did not retain caller credentials exclusively")
			}
			if r.URL.Path == "/metadata/categories" {
				writeJSON(w, 200, map[string]any{"data": []c.Organization{{ID: "category-a", TenantID: definition.TenantID, Name: "生产管理"}}, "hasMore": false})
			} else {
				writeJSON(w, 200, map[string]any{"data": []c.CurrentDefinition{{Definition: definition, ETag: "current", Status: "active"}, {Definition: hidden, ETag: "current", Status: "active"}}, "hasMore": false})
			}
		case "/metadata/resolve":
			if r.Header.Get("Authorization") != "Bearer service-only" || r.Header.Get("X-Tenant-ID") != definition.TenantID {
				t.Error("current definition did not use trusted service context")
			}
			source := definition
			if r.URL.Query().Get("name") == "已停用隐藏模型" {
				writeJSON(w, 200, c.CurrentDefinition{Definition: hidden, ETag: "current", Status: "disabled"})
				return
			} else if r.URL.Query().Get("name") == hidden.Name {
				source = hidden
			} else if nameKey(r.URL.Query().Get("name")) != nameKey(definition.Name) {
				writeJSON(w, 404, c.Error{Code: "not_found", Message: "not found"})
				return
			}
			writeJSON(w, 200, c.CurrentDefinition{Definition: source, ETag: "current", Status: "active"})
		case "/metadata/definitions/" + definition.ID, "/metadata/definitions/" + hidden.ID:
			if r.Header.Get("Authorization") != "Bearer service-only" || r.Header.Get("X-Tenant-ID") != definition.TenantID {
				t.Error("single model lookup did not use trusted context")
			}
			source := definition
			if strings.HasSuffix(r.URL.Path, hidden.ID) {
				source = hidden
			}
			writeJSON(w, 200, c.CurrentDefinition{Definition: source, ETag: "current", Status: "active"})
		default:
			t.Errorf("unexpected metadata request: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(metadata.Close)
	// Engine stays nil: context, analysis, formatting and completion must never
	// reach a transaction, business query, or physical source catalog.
	return NewHandler(Config{MetadataURL: metadata.URL, ServiceToken: "service-only", CursorSecret: strings.Repeat("c", 32), Authorizer: authorizerFunc(func(_ context.Context, p c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		if p.TenantID != definition.TenantID || p.Subject != "reader" {
			t.Error("untrusted principal")
		}
		if req.ResourceID == hidden.ID {
			return c.Decision{Allow: false}, nil
		}
		if req.Operation == "definition.read" {
			return c.Decision{Allow: true, State: "policy"}, nil
		}
		if req.Operation != "instance.query" {
			t.Errorf("unexpected operation: %s", req.Operation)
		}
		return queryDecision, nil
	})})
}

func callOntologySQL(t *testing.T, app http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(encoded)))
	r.Header.Set("Authorization", "Bearer caller-token")
	r.Header.Set("Cookie", "ontology_session=caller-session")
	r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{TenantID: "tenant-a", Subject: "reader"}))
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("query metadata response must be no-store")
	}
	return w
}

func TestOntologySQLContextExcludesHiddenModelsAndFields(t *testing.T) {
	d := testDefinition()
	d.Schema.Properties[2].DataType = "longtext"
	app := ontologySQLTestHandler(t, d, allowed())
	w := callOntologySQL(t, app, "GET", "/data/query/context?categoryId=category-a", nil)
	var out c.QueryContext
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || len(out.Models) != 1 {
		t.Fatalf("context: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "不可见模型") || strings.Contains(w.Body.String(), "采购价") || strings.Contains(w.Body.String(), "mapping") {
		t.Fatal("hidden or physical metadata escaped")
	}
	fields := out.Models[0].Fields
	if len(fields) != 2 || !fields[0].Filterable || !fields[0].Sortable || fields[1].Sortable {
		t.Fatalf("wrong capabilities: %+v", fields)
	}
	for _, schema := range []string{d.ID, "ffffffff-ffff-ffff-ffff-ffffffffffff"} {
		w := callOntologySQL(t, app, "GET", "/data/query/context?schemaId="+schema, nil)
		var out c.QueryContext
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || schema == d.ID && len(out.Models) != 1 || schema != d.ID && len(out.Models) != 0 {
			t.Fatalf("single model context: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestOntologySQLAnalysisBindsWithoutBusinessQueries(t *testing.T) {
	app := ontologySQLTestHandler(t, testDefinition(), allowed())
	text := `SELECT "设备编码" FROM "设备" WHERE STARTS_WITH("设备编码", :前缀) LIMIT 5;`
	for _, provided := range []bool{false, true} {
		req := c.OntologySQLRequest{Text: text, DefaultCategoryID: "category-a", RequestID: "request-1"}
		if provided {
			req.Parameters = map[string]json.RawMessage{"前缀": raw("_%'")}
		}
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", req)
		var out c.OntologySQLAnalysis
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || out.Ready != provided || len(out.Diagnostics) != 0 || len(out.Parameters) != 1 || out.Parameters[0].Provided != provided {
			t.Fatalf("analysis: %d %s", w.Code, w.Body.String())
		}
		if out.RequestID != "request-1" || out.TextHash != ontologySQLTextHash(text) {
			t.Fatal("stale-response identity missing")
		}
		if provided && (out.Query == nil || out.Query.From.SchemaID != testDefinition().ID || out.Query.Where.Field != testFieldCode || string(out.Query.Where.Value) != string(raw("_%'"))) {
			t.Fatalf("not bound precisely: %+v", out.Query)
		}
	}
}

func TestOntologySQLContextSearchUsesAuthorizedFieldsOnly(t *testing.T) {
	d := testDefinition()
	app := ontologySQLTestHandler(t, d, allowed())
	for _, schemaID := range []string{"", d.ID} {
		for _, test := range []struct {
			search string
			count  int
		}{
			{"编码", 1}, {"部门", 1}, {"设备", 1}, {"生产管理", 1},
			{"采购价", 0}, {"不存在字段", 0},
		} {
			t.Run(schemaID+"/"+test.search, func(t *testing.T) {
				values := url.Values{"search": {test.search}}
				if schemaID != "" {
					values.Set("schemaId", schemaID)
				}
				w := callOntologySQL(t, app, "GET", "/data/query/context?"+values.Encode(), nil)
				var out c.QueryContext
				if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || len(out.Models) != test.count {
					t.Fatalf("search %s: %d %s", test.search, w.Code, w.Body.String())
				}
				if strings.Contains(w.Body.String(), "采购价") {
					t.Fatal("hidden field leaked through search")
				}
			})
		}
	}
}

func TestOntologySQLUnavailableNamesHaveUniformDiagnostics(t *testing.T) {
	app := ontologySQLTestHandler(t, testDefinition(), allowed())
	var hiddenSource, missingSource, inactiveSource c.OntologySQLAnalysis
	for _, test := range []struct {
		name, text string
		out        *c.OntologySQLAnalysis
	}{
		{"hidden", `SELECT "设备编码" FROM "生产管理"."不可见模型"`, &hiddenSource},
		{"missing", `SELECT "设备编码" FROM "生产管理"."不存在"`, &missingSource},
		{"inactive", `SELECT "设备编码" FROM "生产管理"."已停用隐藏模型"`, &inactiveSource},
	} {
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", c.OntologySQLRequest{Text: test.text})
		if json.Unmarshal(w.Body.Bytes(), test.out) != nil || w.Code != 200 || test.out.Ready || test.out.Query != nil || len(test.out.Diagnostics) != 1 {
			t.Fatalf("%s: %d %s", test.name, w.Code, w.Body.String())
		}
	}
	if hiddenSource.Diagnostics[0].Code != missingSource.Diagnostics[0].Code || hiddenSource.Diagnostics[0].Message != missingSource.Diagnostics[0].Message {
		t.Fatal("hidden and absent names distinguishable")
	}
	if inactiveSource.Diagnostics[0].Code != missingSource.Diagnostics[0].Code || inactiveSource.Diagnostics[0].Message != missingSource.Diagnostics[0].Message {
		t.Fatal("inactive hidden name existence distinguishable")
	}
	for _, field := range []string{"采购价", "不存在"} {
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", c.OntologySQLRequest{Text: fmt.Sprintf(`SELECT "%s" FROM "生产管理"."设备"`, field)})
		var out c.OntologySQLAnalysis
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || out.Ready || out.Query != nil || len(out.Fields) != 0 || len(out.Diagnostics) != 1 || out.Diagnostics[0].Code != "field_unavailable" {
			t.Fatalf("field diagnostic: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestOntologySQLCompletionUsesFutureFromAndRespectsClauseCapabilities(t *testing.T) {
	d := testDefinition()
	d.Schema.Properties[2].DataType = "longtext"
	app := ontologySQLTestHandler(t, d, allowed())
	for _, test := range []struct {
		text     string
		cursor   int
		expected int
	}{
		{`SELECT "设备". FROM "生产管理"."设备"`, len(utf16.Encode([]rune(`SELECT "设备".`))), 2},
		{`SELECT "设备编码" FROM "生产管理"."设备" ORDER BY "设备".`, len(utf16.Encode([]rune(`SELECT "设备编码" FROM "生产管理"."设备" ORDER BY "设备".`))), 1},
	} {
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/complete", c.OntologySQLRequest{Text: test.text, CursorOffset: test.cursor})
		var out c.OntologySQLCompletions
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || len(out.Items) != test.expected {
			t.Fatalf("completion: %d %s", w.Code, w.Body.String())
		}
		for _, item := range out.Items {
			if item.Kind != "field" || item.Label == "采购价" || item.Range.From != test.cursor || item.Range.To != test.cursor {
				t.Fatalf("invalid completion: %+v", item)
			}
		}
	}
	for _, text := range []string{"SELECT -- 不要补全", "SELECT /* 不要补全", "SELECT '不要补全"} {
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/complete", c.OntologySQLRequest{Text: text, CursorOffset: len(utf16.Encode([]rune(text)))})
		var out c.OntologySQLCompletions
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || len(out.Items) != 0 {
			t.Fatalf("completion in comment/string: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestOntologySQLMissingParametersDoNotHideCrossSourceCapabilities(t *testing.T) {
	d := testDefinition()
	source := "11111111-1111-4111-8111-111111111111"
	d.Schema.StorageBinding = &c.StorageBinding{DataSourceID: source, Table: "devices", IDColumn: "id"}
	for i := range d.Schema.Properties {
		d.Schema.Properties[i].Mapping = &c.PropertyMapping{DataSourceID: source, Column: fmt.Sprintf("field_%d", i)}
	}
	d.Schema.Properties[2].Nullable = true
	d.Schema.Properties[2].Required = false
	d.Schema.Properties[2].Mapping.Table = "departments"
	d.Schema.Properties[2].Mapping.KeyColumn = "device_id"
	app := ontologySQLTestHandler(t, d, allowed())
	w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", c.OntologySQLRequest{Text: `SELECT "所属部门" FROM "生产管理"."设备" WHERE "所属部门" = :部门`})
	var out c.OntologySQLAnalysis
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || out.Ready || out.Query != nil || len(out.Parameters) != 1 || len(out.Diagnostics) != 1 || out.Diagnostics[0].Code != "cross_source_condition_unsupported" {
		t.Fatalf("missing parameter capability: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "devices") || strings.Contains(w.Body.String(), "departments") {
		t.Fatal("physical names escaped")
	}
}

func TestOntologySQLRejectsInvalidExecutionAndCursorOffsetsBeforeStorage(t *testing.T) {
	app := ontologySQLTestHandler(t, testDefinition(), allowed())
	for _, text := range []string{`DELETE FROM "生产管理"."设备"`, `SELECT * FROM "生产管理"."设备"`, `SELECT "设备编码" FROM "生产管理"."设备" WHERE "设备编码"=:缺失`} {
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql", c.OntologySQLRequest{Text: text})
		if w.Code != 422 {
			t.Fatalf("invalid execute: %d %s", w.Code, w.Body.String())
		}
	}
	for _, offset := range []int{-1, 1, 3} {
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/complete", c.OntologySQLRequest{Text: "😀", CursorOffset: offset})
		if w.Code != 400 {
			t.Fatalf("invalid UTF16 cursor accepted: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestOntologySQLCompletionKeepsClauseGrammarAndBooleanOperators(t *testing.T) {
	d := testDefinition()
	d.Schema.Properties[2].DataType = "boolean"
	app := ontologySQLTestHandler(t, d, allowed())
	for _, test := range []struct {
		text         string
		want, absent string
	}{
		{`SELECT "设备编码" FROM "生产管理"."设备" `, "AS", "field"},
		{`SELECT "设备编码" FROM "生产管理"."设备" ORDER `, "BY", "field"},
		{`SELECT "设备编码" FROM "生产管理"."设备" WHERE "所属部门" `, "=", ">"},
	} {
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/complete", c.OntologySQLRequest{Text: test.text, CursorOffset: len(utf16.Encode([]rune(test.text)))})
		var out c.OntologySQLCompletions
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 {
			t.Fatalf("completion: %d %s", w.Code, w.Body.String())
		}
		found := false
		for _, item := range out.Items {
			if item.Label == test.want {
				found = true
			}
			if item.Label == test.absent || item.Kind == test.absent {
				t.Fatalf("invalid candidate: %+v", item)
			}
		}
		if !found {
			t.Fatalf("missing %s: %s", test.want, w.Body.String())
		}
	}
}

func TestOntologySQLContextRejectsUnsupportedAuthorizationConditions(t *testing.T) {
	d := testDefinition()
	d.Schema.StorageBinding = &c.StorageBinding{DataSourceID: "11111111-1111-4111-8111-111111111111", Table: "devices", IDColumn: "id"}
	for i := range d.Schema.Properties {
		d.Schema.Properties[i].Mapping = &c.PropertyMapping{DataSourceID: d.Schema.StorageBinding.DataSourceID, Column: fmt.Sprintf("field_%d", i)}
	}
	d.Schema.Properties[2].Nullable = true
	d.Schema.Properties[2].Required = false
	d.Schema.Properties[2].Mapping.Table = "departments"
	d.Schema.Properties[2].Mapping.KeyColumn = "device_id"
	decision := allowed()
	decision.RowFilter = &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("D1")}
	app := ontologySQLTestHandler(t, d, decision)
	w := callOntologySQL(t, app, "GET", "/data/query/context", nil)
	if w.Code != 422 || strings.Contains(w.Body.String(), "departments") {
		t.Fatalf("unsupported grant directory: %d %s", w.Code, w.Body.String())
	}
	w = callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", c.OntologySQLRequest{Text: `SELECT "设备编码" FROM "生产管理"."设备" WHERE "设备编码" = :编码`})
	var out c.OntologySQLAnalysis
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || out.Ready || out.Query != nil || len(out.Parameters) != 1 || len(out.Diagnostics) != 1 || out.Diagnostics[0].Code != "cross_source_condition_unsupported" {
		t.Fatalf("unsupported grant analysis: %d %s", w.Code, w.Body.String())
	}
}
