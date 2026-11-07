package data

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const rowSQLWorkID = "1b74bd75-135a-4f2f-8f10-7ac08918ef01"
const rowSQLLineID = "1b74bd75-135a-4f2f-8f10-7ac08918ef02"
const rowSQLLinkA = "1b74bd75-135a-4f2f-8f10-7ac08918ef03"
const rowSQLLinkB = "1b74bd75-135a-4f2f-8f10-7ac08918ef04"
const rowSQLWorkField = "1b74bd75-135a-4f2f-8f10-7ac08918ef21"
const rowSQLLineField = "1b74bd75-135a-4f2f-8f10-7ac08918ef31"

func rowSQLDefinitions() []c.Definition {
	root := testDefinition()
	root.CategoryID = "category-a"
	work := c.Definition{ID: rowSQLWorkID, TenantID: root.TenantID, CategoryID: root.CategoryID, Kind: c.KindSchema, Name: "工单", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{ID: rowSQLWorkField, Name: "编号", DataType: "string"}, {ID: testFieldPrice, Name: "金额", DataType: "decimal", Precision: 18, Scale: 3, Nullable: true}}}}
	line := c.Definition{ID: rowSQLLineID, TenantID: root.TenantID, CategoryID: root.CategoryID, Kind: c.KindSchema, Name: "产线", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{ID: rowSQLLineField, Name: "编号", DataType: "string"}, {ID: testFieldPrice, Name: "计数", DataType: "int64"}}}}
	link := func(id, name, from, to, forward, reverse string) c.Definition {
		return c.Definition{ID: id, TenantID: root.TenantID, CategoryID: root.CategoryID, Kind: c.KindLink, Name: name, Link: &c.Link{SourceSchemaID: from, TargetSchemaID: to, ForwardName: forward, ReverseName: reverse, Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	}
	return []c.Definition{root, work, line, link(rowSQLLinkA, "设备工单", root.ID, work.ID, "工单", "设备"), link(rowSQLLinkB, "工单产线", work.ID, line.ID, "产线", "工单")}
}

func rowSQLTestHandler(t *testing.T, definitions []c.Definition, deny string) http.Handler {
	t.Helper()
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata/categories" {
			writeJSON(w, 200, map[string]any{"data": []c.Organization{{ID: "category-a", TenantID: "tenant-a", Name: "生产管理"}}, "hasMore": false})
			return
		}
		if r.URL.Path == "/metadata/definitions" {
			if r.Header.Get("Authorization") != "Bearer caller-token" {
				t.Error("link directory lost caller identity")
			}
			list := []c.CurrentDefinition{}
			for _, d := range definitions {
				if d.Kind == c.KindLink {
					list = append(list, c.CurrentDefinition{Definition: d, ETag: "active", Status: "active"})
				}
			}
			writeJSON(w, 200, map[string]any{"data": list, "hasMore": false})
			return
		}
		for _, d := range definitions {
			if r.URL.Path == "/metadata/definitions/"+d.ID || r.URL.Path == "/metadata/resolve" && nameKey(r.URL.Query().Get("name")) == nameKey(d.Name) {
				writeJSON(w, 200, c.CurrentDefinition{Definition: d, ETag: "active", Status: "active"})
				return
			}
		}
		writeJSON(w, 404, c.Error{Code: "not_found", Message: "not found"})
	}))
	t.Cleanup(metadata.Close)
	return NewHandler(Config{MetadataURL: metadata.URL, ServiceToken: "service", CursorSecret: strings.Repeat("x", 32), Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		if req.ResourceID == deny {
			return c.Decision{Allow: false}, nil
		}
		if req.Operation == "definition.read" {
			return c.Decision{Allow: true, State: "static"}, nil
		}
		return c.Decision{Allow: true, State: "static", ReadFields: []string{"*"}}, nil
	})})
}

func TestOntologySQLRowParserMultipleLinksAndDirections(t *testing.T) {
	text := `SELECT "设备"."设备编码", "工单"."编号", "产线"."编号" FROM "生产管理"."设备" AS "设备"
JOIN LINK "设备"."工单" AS "工单"
JOIN LINK "工单" VIA "生产管理"."工单产线" DIRECTION OUT AS "产线"
WHERE "工单"."金额" >= :下限 ORDER BY "产线"."编号" ASC LIMIT 7;`
	stmt, diagnostics := parseOntologySQL(text)
	if len(diagnostics) != 0 || stmt == nil || len(stmt.Joins) != 2 {
		t.Fatalf("parse: %#v", diagnostics)
	}
	if stmt.Joins[0].Entry != "工单" || stmt.Joins[1].Category != "生产管理" || stmt.Joins[1].Name != "工单产线" || stmt.Joins[1].Direction != "out" {
		t.Fatalf("joins: %#v", stmt.Joins)
	}
	formatted, diagnostics := formatOntologySQL(text)
	if len(diagnostics) != 0 || strings.Count(formatted, "\nJOIN LINK") != 2 {
		t.Fatalf("format: %s %#v", formatted, diagnostics)
	}
	if _, diagnostics = parseOntologySQL(formatted); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	for _, invalid := range []string{
		`SELECT "设备编码" FROM "设备" LEFT JOIN LINK "设备"."工单" AS "工单";`,
		`SELECT "设备编码" FROM "设备" JOIN LINK "设备" VIA "生产管理"."设备工单" DIRECTION BOTH AS "工单";`,
		`SELECT "设备编码" FROM "设备" JOIN LINK "设备"."工单" AS "工单" ON TRUE;`,
		`SELECT "设备编码" FROM "设备"` + strings.Repeat(` JOIN LINK "设备"."工单" AS "工单"`, 5),
	} {
		if _, diagnostics = parseOntologySQL(invalid); len(diagnostics) == 0 {
			t.Fatalf("invalid accepted: %s", invalid)
		}
	}
}

func TestOntologySQLRowAnalysisStableBindingsAndMissingParameterScope(t *testing.T) {
	app := rowSQLTestHandler(t, rowSQLDefinitions(), "")
	text := `SELECT "e"."设备编码", "w"."编号", "l"."编号" FROM "生产管理"."设备" AS "e" JOIN LINK "e"."工单" AS "w" JOIN LINK "w"."产线" AS "l" WHERE "w"."金额" >= :金额 LIMIT 5;`
	for _, provided := range []bool{false, true} {
		req := c.OntologySQLRequest{Text: text, DefaultCategoryID: "category-a", RequestID: "rows"}
		if provided {
			req.Parameters = map[string]json.RawMessage{"金额": raw("9007199254740.123")}
		}
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", req)
		var out c.OntologySQLAnalysis
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || len(out.Diagnostics) != 0 || out.Ready != provided || out.Scope == nil || len(out.Scope.Nodes) != 3 {
			t.Fatalf("analysis: %d %s", w.Code, w.Body.String())
		}
		if !provided && out.Query != nil {
			t.Fatal("missing parameter presented as executable plan")
		}
		if provided && (out.Query.Rows == nil || out.Query.Rows.Joins[1].From != "n1" || out.Query.Where.Field != "n1."+testFieldPrice || string(out.Query.Where.Value) != string(raw("9007199254740.123"))) {
			t.Fatalf("wrong binding: %#v", out.Query)
		}
		if out.Fields[1].ID != "n1."+rowSQLWorkField || out.Fields[1].Name != "w.编号" || out.Fields[2].ID != "n2."+rowSQLLineField || strings.Contains(w.Body.String(), "mapping") {
			t.Fatalf("output metadata: %s", w.Body.String())
		}
	}
}

func TestOntologySQLRowRejectsAmbiguousHiddenAndDuplicateScopes(t *testing.T) {
	definitions := rowSQLDefinitions()
	app := rowSQLTestHandler(t, definitions, "")
	for _, tc := range []struct{ text, code string }{
		{`SELECT "编号" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w" JOIN LINK "w"."产线" AS "l";`, "ambiguous_field"},
		{`SELECT "w"."不存在" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w";`, "field_unavailable"},
		{`SELECT "e"."设备编码" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "e";`, "duplicate_alias"},
		{`SELECT "e"."设备编码" FROM "设备" AS "e" JOIN LINK "尚未声明"."工单" AS "w";`, "unknown_qualifier"},
		{`SELECT "e"."设备编码" FROM "设备" AS "e" JOIN LINK "e"."无此关系" AS "w";`, "link_unavailable"},
		{`SELECT "w"."金额" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w" JOIN LINK "w"."产线" AS "l" WHERE "w"."金额" = :值 AND "l"."计数" = :值;`, "parameter_type_conflict"},
	} {
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", c.OntologySQLRequest{Text: tc.text, DefaultCategoryID: "category-a"})
		var out c.OntologySQLAnalysis
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || out.Ready || len(out.Diagnostics) == 0 || out.Diagnostics[0].Code != tc.code {
			t.Fatalf("want %s got %d %s", tc.code, w.Code, w.Body.String())
		}
	}
	for _, denied := range []string{rowSQLLinkA, rowSQLWorkID} {
		app := rowSQLTestHandler(t, definitions, denied)
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", c.OntologySQLRequest{Text: `SELECT "e"."设备编码" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w";`, DefaultCategoryID: "category-a"})
		var out c.OntologySQLAnalysis
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || out.Ready || len(out.Diagnostics) == 0 || out.Diagnostics[0].Code != "link_unavailable" {
			t.Fatalf("hidden: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestOntologySQLRowReverseSelfAndBranchAliases(t *testing.T) {
	definitions := rowSQLDefinitions()
	self := definitions[3]
	self.ID, self.Name = "1b74bd75-135a-4f2f-8f10-7ac08918ef05", "设备上下级"
	copy := *self.Link
	copy.SourceSchemaID, copy.TargetSchemaID, copy.ForwardName, copy.ReverseName = definitions[0].ID, definitions[0].ID, "下级", "上级"
	self.Link = &copy
	definitions = append(definitions, self)
	app := rowSQLTestHandler(t, definitions, "")
	text := `SELECT "w"."编号", "e"."设备编码", "l"."编号", "上级"."设备编码" FROM "工单" AS "w" JOIN LINK "w"."设备" AS "e" JOIN LINK "w"."产线" AS "l" JOIN LINK "e"."上级" AS "上级" LIMIT 5;`
	w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", c.OntologySQLRequest{Text: text, DefaultCategoryID: "category-a"})
	var out c.OntologySQLAnalysis
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || !out.Ready || len(out.Query.Rows.Nodes) != 4 {
		t.Fatalf("reverse/self/branch: %d %s", w.Code, w.Body.String())
	}
	joins := out.Query.Rows.Joins
	if joins[0].Direction != "in" || joins[1].From != "n0" || joins[2].From != "n1" || joins[2].Direction != "in" {
		t.Fatalf("incorrect row topology: %+v", joins)
	}
}

func TestOntologySQLRowFieldInsertionUsesExplicitAliasAndPreservesParameters(t *testing.T) {
	app := rowSQLTestHandler(t, rowSQLDefinitions(), "")
	text := `-- 😀` + "\r\n" + `SELECT "e"."设备编码" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w" WHERE "w"."金额" > :金额 LIMIT 5;`
	req := c.QueryFieldInsertionRequest{OntologySQLRequest: c.OntologySQLRequest{Text: text, DefaultCategoryID: "category-a", RequestID: "insert"}, SchemaID: rowSQLWorkID, FieldID: rowSQLWorkField, Alias: "w", SelectionFrom: lenUTF16(text), SelectionTo: lenUTF16(text)}
	w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/insert-field", req)
	var out c.QueryFieldInsertionResult
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || len(out.Diagnostics) > 0 || !strings.Contains(out.Text, `SELECT "e"."设备编码", "w"."编号"`) || !strings.Contains(out.Text, `WHERE "w"."金额" > :金额`) {
		t.Fatalf("insert: %d %s", w.Code, w.Body.String())
	}
	from, _ := sqlCursorByte(out.Text, out.From)
	to, _ := sqlCursorByte(out.Text, out.To)
	if out.Text[from:to] != `"w"."编号"` || out.TextHash != ontologySQLTextHash(text) {
		t.Fatalf("range/hash: %#v", out)
	}
	req.Text = out.Text
	req.SelectionFrom, req.SelectionTo = lenUTF16(req.Text), lenUTF16(req.Text)
	w = callOntologySQL(t, app, "POST", "/data/query/ontology-sql/insert-field", req)
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Text != req.Text {
		t.Fatal("duplicate field was reinserted")
	}
	parameterStart := strings.Index(req.Text, ":金额")
	req.SelectionFrom = lenUTF16(req.Text[:parameterStart+len(":金")])
	req.SelectionTo = req.SelectionFrom
	w = callOntologySQL(t, app, "POST", "/data/query/ontology-sql/insert-field", req)
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || out.Text != req.Text || len(out.Diagnostics) == 0 || out.Diagnostics[0].Code != "unsafe_edit_position" {
		t.Fatal("unsafe insertion changed original")
	}
}

func TestOntologySQLRowFieldInsertionKeepsSameNamedFieldsInDifferentRoles(t *testing.T) {
	app := rowSQLTestHandler(t, rowSQLDefinitions(), "")
	text := `SELECT "w"."编号" FROM "工单" AS "w" JOIN LINK "w"."产线" AS "l" WHERE "w"."金额" > :金额 LIMIT 5;`
	req := c.QueryFieldInsertionRequest{OntologySQLRequest: c.OntologySQLRequest{Text: text, DefaultCategoryID: "category-a", Parameters: map[string]json.RawMessage{"金额": raw("1.23")}}, SchemaID: rowSQLLineID, FieldID: rowSQLLineField, Alias: "l"}
	for attempt := 0; attempt < 5; attempt++ {
		req.SelectionFrom, req.SelectionTo = lenUTF16(req.Text), lenUTF16(req.Text)
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/insert-field", req)
		var out c.QueryFieldInsertionResult
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Diagnostics) != 0 {
			t.Fatalf("insert: %d %s", w.Code, w.Body.String())
		}
		if !strings.Contains(out.Text, `SELECT "w"."编号", "l"."编号"`) || !strings.Contains(out.Text, `WHERE "w"."金额" > :金额`) {
			t.Fatalf("lost independent field or condition: %s", out.Text)
		}
		if attempt > 0 && out.Text != req.Text {
			t.Fatalf("duplicate operation changed projection: %s", out.Text)
		}
		req.Text = out.Text
	}
}

func TestOntologySQLRowLinkAmbiguityRequiresExplicitIdentity(t *testing.T) {
	definitions := rowSQLDefinitions()
	alternative := definitions[3]
	alternative.ID, alternative.Name = "1b74bd75-135a-4f2f-8f10-7ac08918ef06", "设备历史工单"
	copy := *alternative.Link
	alternative.Link = &copy
	definitions = append(definitions, alternative)
	app := rowSQLTestHandler(t, definitions, "")
	for _, tc := range []struct{ text, code string }{
		{`SELECT "e"."设备编码" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w";`, "ambiguous_link"},
		{`SELECT "e"."设备编码" FROM "设备" AS "e" JOIN LINK "e" VIA "生产管理"."设备工单" DIRECTION OUT AS "w";`, ""},
	} {
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", c.OntologySQLRequest{Text: tc.text, DefaultCategoryID: "category-a"})
		var out c.OntologySQLAnalysis
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 {
			t.Fatalf("analysis: %d %s", w.Code, w.Body.String())
		}
		if tc.code == "" {
			if !out.Ready || out.Query.Rows.Joins[0].LinkID != rowSQLLinkA {
				t.Fatalf("explicit link not bound: %s", w.Body.String())
			}
		} else if out.Ready || len(out.Diagnostics) == 0 || out.Diagnostics[0].Code != tc.code {
			t.Fatalf("ambiguous entrance selected first: %s", w.Body.String())
		}
	}
}

func TestOntologySQLRowBinderEnforcesFieldAuthorizationAndPrimaryProjection(t *testing.T) {
	definitions := rowSQLDefinitions()
	scope := &ontologySQLRowScope{Nodes: []ontologySQLScopeNode{{ID: "n0", Alias: "e", Current: c.CurrentDefinition{Definition: definitions[0]}, Decision: allowed()}, {ID: "n1", Alias: "w", Current: c.CurrentDefinition{Definition: definitions[1]}, Decision: c.Decision{Allow: true, ReadFields: []string{rowSQLWorkField}}}}, Plan: c.RowQuery{Nodes: []c.RowQueryNode{{ID: "n0", Alias: "e", SchemaID: definitions[0].ID}, {ID: "n1", Alias: "w", SchemaID: rowSQLWorkID}}, Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: rowSQLLinkA, Direction: "out"}}}}
	stmt, _ := parseOntologySQL(`SELECT "w"."金额" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w";`)
	out := bindRowOntologySQL(stmt, scope, nil)
	if out.Ready || len(out.Diagnostics) == 0 || out.Diagnostics[0].Code != "field_unavailable" {
		t.Fatalf("hidden field exposed: %#v", out)
	}
	mapped := definitions[1]
	schema := *mapped.Schema
	schema.Properties = append([]c.Property{}, mapped.Schema.Properties...)
	schema.StorageBinding = &c.StorageBinding{DataSourceID: "source", Table: "orders", Namespace: "data"}
	schema.Properties[0].Mapping = &c.PropertyMapping{DataSourceID: "other-source", Table: "details", Column: "code"}
	mapped.Schema = &schema
	scope.Nodes[1].Current.Definition = mapped
	stmt, _ = parseOntologySQL(`SELECT "w"."编号" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w";`)
	out = bindRowOntologySQL(stmt, scope, nil)
	if out.Ready || len(out.Diagnostics) == 0 || out.Diagnostics[0].Code != "row_projection_unsupported" {
		t.Fatalf("unsupported mapping projected: %#v", out)
	}
}

func TestOntologySQLRowCompletionIncludesJoinedAliasesOnlyWhenAuthorized(t *testing.T) {
	text := `SELECT "w". FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w" LIMIT 5;`
	for _, deny := range []string{"", rowSQLLinkA} {
		app := rowSQLTestHandler(t, rowSQLDefinitions(), deny)
		w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/complete", c.OntologySQLRequest{Text: text, DefaultCategoryID: "category-a", CursorOffset: lenUTF16(`SELECT "w".`)})
		var out c.OntologySQLCompletions
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 {
			t.Fatalf("complete: %d %s", w.Code, w.Body.String())
		}
		if deny == "" && len(out.Items) != 2 || deny != "" && len(out.Items) != 0 {
			t.Fatalf("wrong qualified completions: %s", w.Body.String())
		}
		for _, item := range out.Items {
			if item.InsertText != `"编号"` && item.InsertText != `"金额"` {
				t.Fatalf("root candidate leaked: %#v", item)
			}
		}
	}
}

func TestQualifyRowSQLReferencesKeepsOriginalBindingAndSourceText(t *testing.T) {
	d := ontologySQLTestDefinition()
	alias := `笔"记.😀`
	scope := &ontologySQLRowScope{Nodes: []ontologySQLScopeNode{{ID: "n0", Alias: alias, Current: c.CurrentDefinition{Definition: d}, Decision: c.Decision{Allow: true, ReadFields: []string{"*"}}}}}
	text := "-- 字段 \"标题\" 😀\r\n" + `SELECT "标题", "计数", "笔""记.😀"."编码"` + "\r\n" + `FROM "笔记" AS "笔""记.😀"` + "\r\n" + `WHERE STARTS_WITH("标题", :前缀) AND ("计数" > 3 OR "编码" = '原文 "标题" 😀') /* "计数" */` + "\r\n" + `ORDER BY "计数" DESC LIMIT 5;`
	stmt, diagnostics := parseOntologySQL(text)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	qualified, err := qualifyRowSQLReferences(text, stmt, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"标题", "计数", "编码"} {
		if !strings.Contains(qualified, sqlIdentifierReference(alias, field)) {
			t.Fatalf("field not qualified: %s", qualified)
		}
	}
	if !strings.HasPrefix(qualified, "-- 字段 \"标题\" 😀\r\n") || !strings.Contains(qualified, `'原文 "标题" 😀'`) || !strings.Contains(qualified, `/* "计数" */`) || !strings.Contains(qualified, ":前缀") || strings.Contains(qualified, `"笔""记.😀"."笔""记.😀"`) {
		t.Fatalf("non-field source text changed: %s", qualified)
	}
	newStmt, diagnostics := parseOntologySQL(qualified)
	if len(diagnostics) != 0 {
		t.Fatalf("qualified SQL is invalid: %s %#v", qualified, diagnostics)
	}
	for _, field := range newStmt.Projection {
		if field.Qualifier != alias {
			t.Fatalf("lost original alias: %+v", field)
		}
	}
	again, err := qualifyRowSQLReferences(qualified, newStmt, scope)
	if err != nil || again != qualified {
		t.Fatal("qualifying fields was not idempotent")
	}
}

func TestQualifyRowSQLReferencesUsesEachOriginalNodeAndRejectsAmbiguity(t *testing.T) {
	definitions := rowSQLDefinitions()
	scope := &ontologySQLRowScope{Nodes: []ontologySQLScopeNode{{ID: "n0", Alias: "e", Current: c.CurrentDefinition{Definition: definitions[0]}, Decision: c.Decision{ReadFields: []string{"*"}}}, {ID: "n1", Alias: "w", Current: c.CurrentDefinition{Definition: definitions[1]}, Decision: c.Decision{ReadFields: []string{"*"}}}}}
	text := `SELECT "设备编码", "编号" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w" WHERE "金额" > :金额 ORDER BY "编号";`
	stmt, _ := parseOntologySQL(text)
	qualified, err := qualifyRowSQLReferences(text, stmt, scope)
	if err != nil || !strings.Contains(qualified, `SELECT "e"."设备编码", "w"."编号"`) || !strings.Contains(qualified, `WHERE "w"."金额" > :金额 ORDER BY "w"."编号"`) {
		t.Fatalf("wrong aliases: %s %v", qualified, err)
	}
	scope.Nodes = append(scope.Nodes, ontologySQLScopeNode{ID: "n2", Alias: "l", Current: c.CurrentDefinition{Definition: definitions[2]}, Decision: c.Decision{ReadFields: []string{"*"}}})
	unchanged, err := qualifyRowSQLReferences(text, stmt, scope)
	if err == nil || unchanged != text {
		t.Fatal("ambiguous field was silently rebound")
	}
	scope.Nodes[1].Decision.ReadFields = nil
	stmt, _ = parseOntologySQL(`SELECT "金额" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w";`)
	if _, err := qualifyRowSQLReferences(`SELECT "金额" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w";`, stmt, scope); err == nil {
		t.Fatal("hidden field was qualified")
	}
}
