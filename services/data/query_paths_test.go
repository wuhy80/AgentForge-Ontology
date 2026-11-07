package data

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func callQueryPaths(t *testing.T, app http.Handler, text, target string, params map[string]json.RawMessage) c.QueryPathsResult {
	t.Helper()
	w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/paths", c.QueryPathsRequest{OntologySQLRequest: c.OntologySQLRequest{Text: text, Parameters: params, DefaultCategoryID: "category-a", RequestID: "paths"}, TargetSchemaID: target})
	var out c.QueryPathsResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("paths: %d %s", w.Code, w.Body.String())
	}
	if out.TextHash != ontologySQLTextHash(text) || out.RequestID != "paths" {
		t.Fatalf("lost source identity: %#v", out)
	}
	return out
}

func assertPathBinds(t *testing.T, app http.Handler, candidate c.QueryPathCandidate, params map[string]json.RawMessage, ready bool) c.OntologySQLAnalysis {
	t.Helper()
	w := callOntologySQL(t, app, "POST", "/data/query/ontology-sql/analyze", c.OntologySQLRequest{Text: candidate.Text, Parameters: params, DefaultCategoryID: "category-a"})
	var analysis c.OntologySQLAnalysis
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &analysis) != nil || len(analysis.Diagnostics) != 0 || analysis.Ready != ready {
		t.Fatalf("candidate cannot bind: %s\n%d %s", candidate.Text, w.Code, w.Body.String())
	}
	return analysis
}

func TestQueryPathsGeneratesExecutableTwoLinkCandidateWithoutBusinessEngine(t *testing.T) {
	app := rowSQLTestHandler(t, rowSQLDefinitions(), "")
	text := "-- 😀 原稿\r\n" + `SELECT "e"."设备编码" /* 投影 */ FROM "生产管理"."设备" AS "e"` + "\r\n" + `-- 原条件` + "\r\n" + `WHERE "e"."设备编码" = :编码 ORDER BY "e"."设备编码" DESC LIMIT 7; -- 保留末尾`
	params := map[string]json.RawMessage{"编码": raw("P-01")}
	out := callQueryPaths(t, app, text, rowSQLLineID, params)
	if out.Truncated || len(out.Diagnostics) != 0 || len(out.Candidates) != 1 || len(out.Candidates[0].Steps) != 2 {
		t.Fatalf("expected unique two-link path: %#v", out)
	}
	candidate := out.Candidates[0]
	if candidate.FromAlias != "e" || candidate.Steps[0].LinkID != rowSQLLinkA || candidate.Steps[1].LinkID != rowSQLLinkB || candidate.Steps[1].FromAlias != candidate.Steps[0].ToAlias {
		t.Fatalf("wrong path: %#v", candidate)
	}
	for _, retained := range []string{"-- 😀 原稿\r\n", "/* 投影 */", "-- 原条件", `WHERE "e"."设备编码" = :编码`, `ORDER BY "e"."设备编码" DESC LIMIT 7; -- 保留末尾`} {
		if !strings.Contains(candidate.Text, retained) {
			t.Fatalf("lost original content %s: %s", retained, candidate.Text)
		}
	}
	analysis := assertPathBinds(t, app, candidate, params, true)
	if len(analysis.Query.Rows.Joins) != 2 || analysis.Query.Page.Limit != 7 || len(analysis.Query.Select) != 1 || analysis.Query.Select[0] != "n0."+testFieldCode {
		t.Fatalf("wrong bound plan: %#v", analysis.Query)
	}
	// Metadata-only operations must work with Engine=nil; no physical tables or
	// business samples are needed to suggest, inspect or bind a path.
	missing := callQueryPaths(t, app, text, rowSQLLineID, nil)
	if len(missing.Candidates) != 1 {
		t.Fatalf("missing parameters blocked editing: %#v", missing)
	}
	assertPathBinds(t, app, missing.Candidates[0], nil, false)
}

func TestQueryPathsConsecutiveSelectionsKeepOriginalProjectionAndConditions(t *testing.T) {
	app := rowSQLTestHandler(t, rowSQLDefinitions(), "")
	text := "-- 😀 原稿\r\n" + `SELECT "设备编码" /* 保留返回字段 */ FROM "设备" AS "e" WHERE "设备编码" = :编码 ORDER BY "设备编码" DESC LIMIT 7;`
	params := map[string]json.RawMessage{"编码": raw("P-01")}
	for index, target := range []string{rowSQLWorkID, rowSQLLineID} {
		out := callQueryPaths(t, app, text, target, params)
		if len(out.Diagnostics) != 0 || len(out.Candidates) != 1 {
			t.Fatalf("step %d: %#v", index, out)
		}
		candidate := out.Candidates[0]
		stmt, diagnostics := parseOntologySQL(candidate.Text)
		if len(diagnostics) != 0 || len(stmt.Projection) != 1 || len(stmt.Joins) != index+1 || stmt.Projection[0].Qualifier != "e" || stmt.Projection[0].Name != "设备编码" {
			t.Fatalf("path changed chosen fields: %s %#v", candidate.Text, diagnostics)
		}
		for _, retained := range []string{"-- 😀 原稿\r\n", "/* 保留返回字段 */", `WHERE "e"."设备编码" = :编码 ORDER BY "e"."设备编码" DESC LIMIT 7;`} {
			if !strings.Contains(candidate.Text, retained) {
				t.Fatalf("lost original content %s: %s", retained, candidate.Text)
			}
		}
		analysis := assertPathBinds(t, app, candidate, params, true)
		if len(analysis.Query.Select) != 1 || analysis.Query.Select[0] != "n0."+testFieldCode || string(analysis.Query.Where.Value) != string(params["编码"]) {
			t.Fatalf("path changed output or parameter binding: %#v", analysis.Query)
		}
		text = candidate.Text
	}
}

func TestQueryPathsPreservesSameNamedFieldsFromDifferentRoles(t *testing.T) {
	definitions := rowSQLDefinitions()
	definitions[1].Schema.Properties[0].Name = "来源键"
	definitions[2].Schema.Properties[0].Name = "来源键"
	app := rowSQLTestHandler(t, definitions, "")
	text := `SELECT "w"."来源键", "l"."来源键" FROM "工单" AS "w" JOIN LINK "w"."产线" AS "l" WHERE "w"."来源键" = :键 ORDER BY "l"."来源键";`
	params := map[string]json.RawMessage{"键": raw("source-01")}
	out := callQueryPaths(t, app, text, definitions[0].ID, params)
	if len(out.Diagnostics) != 0 || len(out.Candidates) != 1 {
		t.Fatalf("missing reverse path: %#v", out)
	}
	candidate := out.Candidates[0]
	if !strings.Contains(candidate.Text, `SELECT "w"."来源键", "l"."来源键"`) {
		t.Fatalf("different roles were collapsed or renamed: %s", candidate.Text)
	}
	analysis := assertPathBinds(t, app, candidate, params, true)
	if len(analysis.Query.Select) != 2 || analysis.Query.Select[0] != "n0."+rowSQLWorkField || analysis.Query.Select[1] != "n1."+rowSQLLineField || analysis.Fields[0].Name != "w.来源键" || analysis.Fields[1].Name != "l.来源键" {
		t.Fatalf("different role projections changed: %#v", analysis)
	}
}

func TestQueryPathsAllowsExistingHundredFieldProjection(t *testing.T) {
	definitions := rowSQLDefinitions()
	fields := []string{}
	definitions[0].Schema.Properties = nil
	for index := 0; index < 100; index++ {
		name := fmt.Sprintf("字段%03d", index+1)
		definitions[0].Schema.Properties = append(definitions[0].Schema.Properties, c.Property{ID: fmt.Sprintf("1b74bd75-135a-4f2f-8f10-%012d", 1000+index), Name: name, DataType: "int64"})
		fields = append(fields, sqlQuotedIdentifier(name))
	}
	app := rowSQLTestHandler(t, definitions, "")
	text := "SELECT " + strings.Join(fields, ", ") + ` FROM "设备" AS "e" LIMIT 5;`
	out := callQueryPaths(t, app, text, rowSQLLineID, nil)
	if len(out.Diagnostics) != 0 || len(out.Candidates) != 1 || len(out.Candidates[0].Steps) != 2 {
		t.Fatalf("projection budget incorrectly blocked relationship scope: %#v", out)
	}
	analysis := assertPathBinds(t, app, out.Candidates[0], nil, true)
	if len(analysis.Query.Select) != 100 || analysis.Query.Select[0] != "n0."+definitions[0].Schema.Properties[0].ID || analysis.Query.Select[99] != "n0."+definitions[0].Schema.Properties[99].ID {
		t.Fatalf("existing projection changed: %#v", analysis.Query)
	}
}

func TestQueryPathsDoesNotRequireTargetDefaultProjection(t *testing.T) {
	definitions := rowSQLDefinitions()
	definitions[2].Schema.Properties = []c.Property{{ID: rowSQLLineField, Name: "正文", DataType: "longtext", Nullable: true}}
	app := rowSQLTestHandler(t, definitions, "")
	out := callQueryPaths(t, app, `SELECT "设备编码" FROM "设备" AS "e";`, rowSQLLineID, nil)
	if len(out.Diagnostics) != 0 || len(out.Candidates) != 1 {
		t.Fatalf("target fields incorrectly blocked relationship scope: %#v", out)
	}
	analysis := assertPathBinds(t, app, out.Candidates[0], nil, true)
	if len(analysis.Query.Select) != 1 || analysis.Query.Select[0] != "n0."+testFieldCode || strings.Contains(out.Candidates[0].Text, `"正文"`) {
		t.Fatalf("target field was implicitly projected: %#v", analysis)
	}
}

func TestQueryPathsReturnsEveryShortestAmbiguousAlternative(t *testing.T) {
	definitions := rowSQLDefinitions()
	alternative := definitions[3]
	alternative.ID, alternative.Name = "1b74bd75-135a-4f2f-8f10-7ac08918ef06", "设备历史工单"
	definitions = append(definitions, alternative)
	app := rowSQLTestHandler(t, definitions, "")
	out := callQueryPaths(t, app, `SELECT "设备编码" FROM "设备" AS "e" LIMIT 5;`, rowSQLLineID, nil)
	if len(out.Candidates) != 2 || out.Candidates[0].ID == out.Candidates[1].ID {
		t.Fatalf("ambiguity guessed or collapsed: %#v", out)
	}
	for _, candidate := range out.Candidates {
		if len(candidate.Steps) != 2 || !strings.Contains(candidate.Text, " VIA ") {
			t.Fatalf("ambiguous short entry generated: %#v", candidate)
		}
		assertPathBinds(t, app, candidate, nil, true)
	}
}

func TestQueryPathsUsesExistingAliasReverseAndSelfRoles(t *testing.T) {
	definitions := rowSQLDefinitions()
	self := definitions[3]
	self.ID, self.Name = "1b74bd75-135a-4f2f-8f10-7ac08918ef05", "设备上下级"
	link := *self.Link
	link.TargetSchemaID, link.ForwardName, link.ReverseName = definitions[0].ID, "下级", "上级"
	self.Link = &link
	definitions = append(definitions, self)
	app := rowSQLTestHandler(t, definitions, "")
	branch := callQueryPaths(t, app, `SELECT "e"."设备编码" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w";`, rowSQLLineID, nil)
	if len(branch.Candidates) != 1 || branch.Candidates[0].FromAlias != "w" || len(branch.Candidates[0].Steps) != 1 {
		t.Fatalf("did not use nearest existing role: %#v", branch)
	}
	assertPathBinds(t, app, branch.Candidates[0], nil, true)
	reverse := callQueryPaths(t, app, `SELECT "编号" FROM "产线" AS "l";`, definitions[0].ID, nil)
	if len(reverse.Candidates) != 1 || reverse.Candidates[0].Steps[0].Direction != "in" || reverse.Candidates[0].Steps[1].Direction != "in" {
		t.Fatalf("wrong reverse path: %#v", reverse)
	}
	assertPathBinds(t, app, reverse.Candidates[0], nil, true)
	selfPaths := callQueryPaths(t, app, `SELECT "设备编码" FROM "设备" AS "设备";`, definitions[0].ID, nil)
	if len(selfPaths.Candidates) != 2 {
		t.Fatalf("self directions not explicit: %#v", selfPaths)
	}
	for _, candidate := range selfPaths.Candidates {
		if candidate.Steps[0].ToAlias != "设备_2" {
			t.Fatalf("self alias collided: %#v", candidate)
		}
		assertPathBinds(t, app, candidate, nil, true)
	}
}

func TestQueryPathsHiddenRelationshipsAndModelsAreAbsent(t *testing.T) {
	definitions := rowSQLDefinitions()
	for _, denied := range []string{rowSQLLinkA, rowSQLWorkID, rowSQLLineID} {
		app := rowSQLTestHandler(t, definitions, denied)
		out := callQueryPaths(t, app, `SELECT "设备编码" FROM "设备";`, rowSQLLineID, nil)
		if len(out.Candidates) != 0 || len(out.Diagnostics) != 1 {
			t.Fatalf("hidden path suggested: %#v", out)
		}
		encoded, _ := json.Marshal(out)
		if strings.Contains(string(encoded), "工单") || strings.Contains(string(encoded), rowSQLLinkA) || strings.Contains(string(encoded), rowSQLLineID) {
			t.Fatalf("hidden metadata leaked: %s", encoded)
		}
	}
}

func TestQueryPathsStopsAtCandidateBudgetWithoutPartialSuggestions(t *testing.T) {
	definitions := rowSQLDefinitions()
	for i := 0; i < maxQueryPathCandidates; i++ {
		alternative := definitions[3]
		alternative.ID = fmt.Sprintf("1b74bd75-135a-4f2f-8f10-%012d", 500+i)
		alternative.Name = fmt.Sprintf("设备工单候选%d", i)
		definitions = append(definitions, alternative)
	}
	out := callQueryPaths(t, rowSQLTestHandler(t, definitions, ""), `SELECT "设备编码" FROM "设备";`, rowSQLWorkID, nil)
	if !out.Truncated || len(out.Candidates) != 0 || len(out.Diagnostics) != 1 || out.Diagnostics[0].Code != "path_budget" {
		t.Fatalf("returned arbitrary partial path set: %#v", out)
	}
}

func TestQueryPathsInvalidDraftAndMaxDepthRemainUnchanged(t *testing.T) {
	app := rowSQLTestHandler(t, rowSQLDefinitions(), "")
	for _, text := range []string{`SELECT FROM`, `SELECT "不存在" FROM "设备";`, `SELECT "设备编码" FROM "设备" WHERE "设备编码" = :值;`} {
		params := map[string]json.RawMessage(nil)
		if strings.Contains(text, ":值") {
			params = map[string]json.RawMessage{"值": raw(true)}
		}
		out := callQueryPaths(t, app, text, rowSQLLineID, params)
		if len(out.Candidates) != 0 || len(out.Diagnostics) == 0 {
			t.Fatalf("invalid draft replaced: %#v", out)
		}
	}
	text := `SELECT "e"."设备编码" FROM "设备" AS "e" JOIN LINK "e"."工单" AS "w" JOIN LINK "w"."设备" AS "e2" JOIN LINK "e2"."工单" AS "w2" JOIN LINK "w2"."产线" AS "l";`
	out := callQueryPaths(t, app, text, rowSQLLineID, nil)
	if len(out.Candidates) != 0 || len(out.Diagnostics) != 1 || out.Diagnostics[0].Code != "budget_exceeded" {
		t.Fatalf("depth budget ignored: %#v", out)
	}
}
