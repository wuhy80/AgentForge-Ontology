package data

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestSavedQuerySQLCompileInnerRowsKeepsScopedASTParameters(t *testing.T) {
	base := rowSQLTestHandler(t, rowSQLDefinitions(), "") // No business Engine.
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base.ServeHTTP(w, r.WithContext(platform.WithSessionUser(r.Context(), c.User{TenantID: "tenant-a", ID: "reader", Enabled: true})))
	})
	text := `SELECT "e"."设备编码", "w"."金额", "l"."计数" FROM "生产管理"."设备" AS "e" JOIN LINK "e"."工单" AS "w" JOIN LINK "w"."产线" AS "l" WHERE "w"."金额" >= :下限 AND "w"."编号" = '固定文本' ORDER BY "l"."计数" DESC LIMIT 1;`
	w := callOntologySQL(t, app, "POST", "/data/saved-queries/compile", c.SavedQueryCompileRequest{Text: text})
	var out c.SavedQueryTemplate
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("row SQL template: %d %s", w.Code, w.Body)
	}
	if out.Query.Rows == nil || len(out.Query.Rows.Joins) != 2 || len(out.Parameters) != 1 || out.Parameters[0].DataType != "decimal" || out.Query.Where.Args[0].Field != "n1."+testFieldPrice || out.Query.Where.Args[0].Parameter != "下限" || len(out.Query.Where.Args[0].Value) != 0 || string(out.Query.Where.Args[1].Value) != `"固定文本"` || out.Query.OrderBy[0].Field != "n2."+testFieldPrice {
		t.Fatalf("row AST parameter identity/type lost: %+v", out)
	}
	for _, invalid := range []string{
		`SELECT "e"."设备编码", COUNT(*) FROM "生产管理"."设备" AS "e" JOIN LINK "e"."工单" AS "w" GROUP BY "e"."设备编码" LIMIT 1`,
		strings.Replace(text, `"工单" AS "w"`, `"不存在" AS "w"`, 1),
		strings.Replace(text, `"w"."编号" = '固定文本'`, `"l"."计数" = :下限`, 1),
		strings.Replace(text, `"w"."金额" >= :下限`, `"w"."金额" IN (:下限)`, 1),
	} {
		w := callOntologySQL(t, app, "POST", "/data/saved-queries/compile", c.SavedQueryCompileRequest{Text: invalid})
		if w.Code != 422 || strings.Contains(w.Body.String(), `"query"`) {
			t.Fatalf("invalid row SQL template accepted: %d %s", w.Code, w.Body)
		}
	}
}

func TestSavedQuerySQLCompileKeepsASTParametersAndSupportsAggregate(t *testing.T) {
	definition := ontologySQLTestDefinition()
	definition.ID, definition.TenantID = c.NewID(), "saved-query-compiler"
	for i := range definition.Schema.Properties {
		definition.Schema.Properties[i].ID = c.NewID()
	}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.CurrentDefinition{Definition: definition, ETag: digest(definition), Status: "active"})
	}))
	defer metadata.Close()
	// Engine is deliberately nil: compiling templates must not scan business
	// records or rely on any physical mapping/database connection.
	app := NewHandler(Config{MetadataURL: metadata.URL, HTTPClient: metadata.Client(), CursorSecret: strings.Repeat("c", 32), Authorizer: authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		return c.Decision{Allow: true, State: "current", ReadFields: []string{"*"}}, nil
	})})
	compile := func(text string, want int) c.SavedQueryTemplate {
		t.Helper()
		encoded, _ := json.Marshal(c.SavedQueryCompileRequest{Text: text})
		r := httptest.NewRequest("POST", "/data/saved-queries/compile", bytes.NewReader(encoded))
		ctx := platform.WithPrincipal(r.Context(), c.Principal{TenantID: definition.TenantID, Subject: "owner"})
		r = r.WithContext(platform.WithSessionUser(ctx, c.User{TenantID: definition.TenantID, ID: "owner", Enabled: true}))
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("compile HTTP%d want%d: %s", w.Code, want, w.Body)
		}
		var out c.SavedQueryTemplate
		if want == 200 && json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("invalid compile template")
		}
		return out
	}
	template := compile(`SELECT "标题" FROM "分类"."笔记" WHERE "标题" = :文本 AND "标题" = '固定文本' LIMIT 10`, 200)
	if template.Query.From.SchemaID != definition.ID || len(template.Parameters) != 1 || template.Parameters[0].Name != "文本" || template.Query.Where.Args[0].Parameter != "文本" || len(template.Query.Where.Args[0].Value) != 0 || string(template.Query.Where.Args[1].Value) != `"固定文本"` {
		t.Fatal("AST parameter identity or literal was lost")
	}
	aggregate := compile(`SELECT "编码", SUM("金额") AS "合计" FROM "分类"."笔记" WHERE "金额" >= :下限 GROUP BY "编码" ORDER BY "合计" DESC LIMIT 5`, 200)
	if aggregate.Query.Aggregate == nil || aggregate.Query.Where.Parameter != "下限" || len(aggregate.Query.Where.Value) != 0 || aggregate.Parameters[0].DataType != "decimal" || aggregate.Query.OrderBy[0].Field != aggregate.Query.Aggregate.Measures[0].ID {
		t.Fatal("aggregate template did not preserve stable outputs and parameter")
	}
	for _, text := range []string{
		`SELECT "标题" FROM "分类"."笔记" WHERE "标题" IN (:文本)`,
		`SELECT "标题" FROM "分类"."笔记" WHERE "标题" IN (:甲,:乙,'固定值')`,
		`SELECT "标题" FROM "分类"."笔记" WHERE "标题" = :相同 AND "计数" = :相同`,
	} {
		compile(text, 422)
	}
}

func TestSavedQueryMySQLPostCommitAuthorizationChangeHidesTemplate(t *testing.T) {
	f := newSavedQueriesFixture(t)
	var checks atomic.Int32
	f.authorize = func(request c.AuthorizationRequest) c.Decision {
		decision := f.decision
		if request.Operation == "instance.query" && checks.Add(1) >= 3 {
			decision.Allow = false
		}
		return decision
	}
	w := f.own("POST", "/data/saved-queries", "", f.input, 403)
	if strings.Contains(w.Body.String(), "query") && strings.Contains(w.Body.String(), f.definition.Schema.Properties[0].ID) {
		t.Fatal("revocation during save exposed template fields")
	}
	page := mutationDecode[c.SavedQueryPage](t, f.own("GET", "/data/saved-queries", "", nil, 200))
	if len(page.Data) != 1 {
		t.Fatal("unknown save result cannot be identified from safe summaries")
	}
	f.own("DELETE", "/data/saved-queries/"+page.Data[0].ID, page.Data[0].ETag, nil, 204)
}

func TestSavedQueryMySQLAggregateTemplateCanonicalizesAndArrayExecutes(t *testing.T) {
	f := newSavedQueriesFixture(t)
	status := f.definition.Schema.Properties[0]
	f.input.Query.Select = []string{}
	f.input.Query.Aggregate = &c.AggregateQuery{GroupBy: []string{status.ID}, Measures: []c.AggregateMeasure{{ID: "mCount", Op: "count"}}}
	f.input.Query.OrderBy = []c.Order{{Field: "mCount", Direction: "DESC"}}
	f.input.Query.Where = &c.Expression{Op: "in", Field: status.ID, Parameter: "states"}
	f.input.Parameters = []c.SavedQueryParameter{declarationForProperty("states", status, true)}
	saved := mutationDecode[c.SavedQuery](t, f.own("POST", "/data/saved-queries", "", f.input, 201))
	if saved.Query.Aggregate.Measures[0].Name == "" || saved.Query.OrderBy[0].Direction != "desc" || saved.Query.Where.Parameter != "states" {
		t.Fatal("canonical aggregate changed or lost template binding")
	}
	f.own("GET", "/data/saved-queries/"+saved.ID, "", nil, 200)
	for i := 0; i < 2; i++ {
		if w := f.request("POST", "/data/schemas/"+f.definition.ID+"/instances", c.WriteRequest{IdempotencyKey: c.NewID(), Values: map[string]json.RawMessage{status.ID: raw("A")}}, false); w.Code != 201 {
			t.Fatalf("create aggregate object: %d %s", w.Code, w.Body)
		}
	}
	result := mutationDecode[c.QueryResult](t, f.own("POST", "/data/saved-queries/"+saved.ID+"/execute", "", c.SavedQueryExecuteRequest{Parameters: map[string]json.RawMessage{"states": json.RawMessage(`["A","B"]`)}}, 200))
	if !result.AggregateMode || len(result.AggregateRows) != 1 || string(result.AggregateRows[0].Values["mCount"]) != `"2"` {
		t.Fatal("array parameter or aggregate execution failed")
	}
	f.input.Query.Aggregate.GroupBy[0] = status.Name
	f.input.Name = "错误名称绑定"
	f.own("POST", "/data/saved-queries", "", f.input, 422)
}
