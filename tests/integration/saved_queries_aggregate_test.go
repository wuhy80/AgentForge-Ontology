package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// This extends the real TLS/Cookie/CSRF/OPA/MySQL model-evolution fixture. It
// verifies stable bindings, private ownership and authorization at execution,
// rather than substituting an in-process grant for the public request chain.
func verifySavedQueriesAndAggregatesHTTP(t *testing.T, admin, foreign *publicSecurityHTTP, client *http.Client, metadataURL, dataURL, tenant string, category, group c.Organization, schemaID, code, state, priority string) {
	t.Helper()
	queryURL, savedURL := dataURL+"/data/query", dataURL+"/data/saved-queries"
	definitionURL := metadataURL + "/metadata/definitions/" + schemaID
	query := c.QuerySpec{From: c.Source{SchemaID: schemaID}, Select: []string{}, Aggregate: &c.AggregateQuery{Measures: []c.AggregateMeasure{{ID: "mCount", Op: "count", Name: "数量"}, {ID: "mSum", Op: "sum", Field: priority, Name: "合计"}, {ID: "mAvg", Op: "avg", Field: priority, Name: "平均"}}}, Page: c.Page{Limit: 10}}
	var result c.QueryResult
	admin.request("POST", queryURL, query, nil, 200, &result)
	if !result.AggregateMode || len(result.Data) != 0 || len(result.AggregateRows) != 1 || string(result.AggregateRows[0].Values["mCount"]) != `"3"` || string(result.AggregateRows[0].Values["mSum"]) != `"27021597764222979"` || string(result.AggregateRows[0].Values["mAvg"]) != `"9007199254740993.0000"` {
		t.Fatal("public aggregate lost result grain, complete input or exact numeric values")
	}
	for _, field := range result.Fields {
		if !field.ReadOnly {
			t.Fatal("aggregate exposed a writable result field")
		}
	}
	grouped := query
	grouped.Aggregate = &c.AggregateQuery{GroupBy: []string{state}, Measures: []c.AggregateMeasure{{ID: "mCount", Op: "count"}}}
	grouped.Page.Limit = 1
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		result = c.QueryResult{}
		admin.request("POST", queryURL, grouped, nil, 200, &result)
		if len(result.AggregateRows) != 1 || string(result.AggregateRows[0].Values["mCount"]) != `"1"` {
			t.Fatal("grouped aggregation counted a page instead of the authorized input")
		}
		key := string(result.AggregateRows[0].Values[state])
		if seen[key] {
			t.Fatal("aggregate continuation repeated a group")
		}
		seen[key] = true
		if i < 2 && (!result.HasMore || result.NextCursor == "") || i == 2 && result.HasMore {
			t.Fatal("aggregate continuation did not preserve group boundaries")
		}
		grouped.Page.Cursor = result.NextCursor
	}
	text := `SELECT "状态", COUNT(*) AS "数量", SUM("优先级") AS "合计" FROM "模型演进"."工单" GROUP BY "状态" LIMIT 10`
	var sqlResult c.OntologySQLResult
	admin.request("POST", queryURL+"/ontology-sql", c.OntologySQLRequest{Text: text}, nil, 200, &sqlResult)
	if !sqlResult.AggregateMode || sqlResult.Query.Aggregate == nil || len(sqlResult.AggregateRows) != 3 || len(sqlResult.Data) != 0 {
		t.Fatal("OntologySQL did not lower aggregate into the shared query result")
	}
	var compiled c.SavedQueryTemplate
	source := c.SavedQuerySource{Kind: "ontology-sql", Text: `SELECT "代码", "优先级" FROM "模型演进"."工单" WHERE "代码" = :编号 LIMIT 10`, DefaultCategoryID: category.ID}
	admin.request("POST", savedURL+"/compile", c.SavedQueryCompileRequest{Text: source.Text, DefaultCategoryID: category.ID}, nil, 200, &compiled)
	if compiled.Query.From.SchemaID != schemaID || compiled.Query.Where == nil || compiled.Query.Where.Parameter != "编号" || len(compiled.Query.Where.Value) != 0 || len(compiled.Parameters) != 1 {
		t.Fatal("SQL template did not preserve an unbound typed parameter and stable IDs")
	}
	admin.request("POST", queryURL, compiled.Query, nil, 422, nil)
	input := c.SavedQueryInput{Name: "按编号查询", Source: source, Query: compiled.Query, Parameters: compiled.Parameters}
	var saved c.SavedQuery
	headers := admin.request("POST", savedURL, input, nil, 201, &saved)
	if saved.ID == "" || saved.ETag == "" || headers.Get("ETag") != `"`+saved.ETag+`"` {
		t.Fatal("saved query omitted its persistent identity or concurrency token")
	}
	itemURL := savedURL + "/" + saved.ID
	var summary json.RawMessage
	admin.request("GET", savedURL, nil, nil, 200, &summary)
	for _, privateKey := range []string{`"query":`, `"source":`, `"parameters":`, `"values":`} {
		if strings.Contains(string(summary), privateKey) {
			t.Fatal("saved-query list exposed template or result contents")
		}
	}
	arguments := c.SavedQueryExecuteRequest{Parameters: map[string]json.RawMessage{"编号": raw("new")}}
	result = c.QueryResult{}
	admin.request("POST", itemURL+"/execute", arguments, nil, 200, &result)
	if len(result.Data) != 1 || string(result.Data[0].Values[priority]) != `"9007199254740993"` {
		t.Fatal("saved query failed to bind execution arguments")
	}
	originalID := result.Data[0].ID
	admin.request("POST", itemURL+"/execute", c.SavedQueryExecuteRequest{}, nil, 422, nil)
	admin.request("POST", itemURL+"/execute", c.SavedQueryExecuteRequest{Parameters: map[string]json.RawMessage{"编号": raw("new"), "额外": raw("ignored")}}, nil, 422, nil)
	foreign.request("GET", itemURL, nil, nil, 404, nil)
	foreign.request("POST", itemURL+"/execute", arguments, nil, 404, nil)
	var reloaded c.SavedQuery
	admin.request("GET", itemURL, nil, nil, 200, &reloaded)
	if reloaded.Query.Where.Parameter != "编号" || len(reloaded.Query.Where.Value) != 0 || len(reloaded.Parameters) != 1 {
		t.Fatal("execution mutated the persisted parameter template")
	}
	// Rename the bound schema and fields, then publish a different schema under
	// their old names. Replaying source text would now target different data.
	var current, draft c.CurrentDefinition
	admin.request("GET", definitionURL, nil, nil, 200, &current)
	rawDefinition, _ := json.Marshal(current.Definition)
	var renamed c.Definition
	if json.Unmarshal(rawDefinition, &renamed) != nil {
		t.Fatal("copy rename fixture")
	}
	renamed.Name = "工单已改名"
	for _, field := range c.DefinitionProperties(&renamed) {
		field.EnumValues, field.EnumETag = nil, ""
		if field.ID == code {
			field.Name = "编号"
		}
		if field.ID == priority {
			field.Name = "精确分值"
		}
	}
	headers = admin.request("PUT", definitionURL+"/draft", renamed, map[string]string{"If-Match": `"` + current.ETag + `"`}, 200, &draft)
	admin.request("POST", definitionURL+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	otherCode, otherPriority := c.NewID(), c.NewID()
	replacement := c.Definition{CategoryID: category.ID, GroupID: group.ID, Kind: c.KindSchema, Name: "工单", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{ID: otherCode, Name: "代码", DataType: "string", Required: true}, {ID: otherPriority, Name: "优先级", DataType: "int64", Required: true}}}}
	headers = admin.request("POST", metadataURL+"/metadata/definitions", replacement, nil, 201, &draft)
	admin.request("POST", metadataURL+"/metadata/definitions/"+draft.Definition.ID+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	admin.request("POST", dataURL+"/data/schemas/"+draft.Definition.ID+"/instances", c.WriteRequest{IdempotencyKey: c.NewID(), Values: map[string]json.RawMessage{otherCode: raw("new"), otherPriority: raw("1")}}, nil, 201, nil)
	result = c.QueryResult{}
	admin.request("POST", itemURL+"/execute", arguments, nil, 200, &result)
	if len(result.Data) != 1 || result.Data[0].ID != originalID || string(result.Data[0].Values[priority]) != `"9007199254740993"` {
		t.Fatal("saved query rebound an old Chinese name to a different schema")
	}
	input.Name = "改名后的查询"
	admin.request("PUT", itemURL, input, map[string]string{"If-Match": `"` + saved.ETag + `"`}, 200, &reloaded)
	admin.request("PUT", itemURL, input, map[string]string{"If-Match": `"` + saved.ETag + `"`}, 412, nil)
	admin.request("DELETE", itemURL, nil, map[string]string{"If-Match": `"` + saved.ETag + `"`}, 412, nil)
	// A second user in the same tenant sees only their own templates. Row and
	// column rules still constrain saved execution and aggregate input.
	var role c.Role
	var user c.User
	admin.request("POST", metadataURL+"/metadata/admin/roles", c.RoleInput{Name: "保存查询验收"}, nil, 201, &role)
	const password = "SavedQueryFixture!2026"
	admin.request("POST", metadataURL+"/metadata/admin/users", c.UserInput{Username: "query-reader", Password: password, DisplayName: "查询用户", Enabled: true, RoleIDs: []string{role.ID}}, nil, 201, &user)
	var policy c.PolicyRecord
	admin.request("GET", metadataURL+"/metadata/admin/policy", nil, nil, 200, &policy)
	queryRule := c.PermissionRule{ID: c.NewID(), RoleID: role.ID, Effect: "allow", Operation: "instance.query", ResourceID: schemaID, ReadFields: []string{code}, RowFilter: &c.Expression{Op: "eq", Field: code, Value: raw("new")}}
	policy.Current.Rules = append(policy.Current.Rules, queryRule, c.PermissionRule{ID: c.NewID(), RoleID: role.ID, Effect: "allow", Operation: "definition.read", ResourceID: schemaID})
	headers = admin.request("PUT", metadataURL+"/metadata/admin/policy", policy.Current, map[string]string{"If-Match": `"` + policy.ETag + `"`}, 200, &policy)
	admin.request("POST", metadataURL+"/metadata/admin/policy/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	reader := &publicSecurityHTTP{t: t, client: client}
	var session c.Session
	headers = reader.request("POST", metadataURL+"/metadata/auth/login", c.LoginRequest{Tenant: tenant, Username: user.Username, Password: password}, nil, 200, &session)
	response := http.Response{Header: headers}
	reader.cookies, reader.csrf = response.Cookies(), session.CSRFToken
	reader.request("GET", itemURL, nil, nil, 404, nil)
	var empty c.SavedQueryPage
	reader.request("GET", savedURL, nil, nil, 200, &empty)
	if len(empty.Data) != 0 {
		t.Fatal("private query directory leaked another user templates")
	}
	limitedInput := c.SavedQueryInput{Name: "本人查询", Source: c.SavedQuerySource{Kind: "visual"}, Query: c.QuerySpec{From: c.Source{SchemaID: schemaID}, Select: []string{code}, Page: c.Page{Limit: 10}}, Parameters: []c.SavedQueryParameter{}}
	var limited c.SavedQuery
	reader.request("POST", savedURL, limitedInput, nil, 201, &limited)
	admin.request("GET", savedURL+"/"+limited.ID, nil, nil, 404, nil)
	result = c.QueryResult{}
	reader.request("POST", savedURL+"/"+limited.ID+"/execute", c.SavedQueryExecuteRequest{}, nil, 200, &result)
	if len(result.Data) != 1 || result.Data[0].ID != originalID || len(result.Data[0].Values) != 1 {
		t.Fatal("saved execution bypassed current row or column rules")
	}
	limitedAggregate := c.QuerySpec{From: c.Source{SchemaID: schemaID}, Select: []string{}, Aggregate: &c.AggregateQuery{Measures: []c.AggregateMeasure{{ID: "mCount", Op: "count"}}}, Page: c.Page{Limit: 10}}
	result = c.QueryResult{}
	reader.request("POST", queryURL, limitedAggregate, nil, 200, &result)
	if len(result.AggregateRows) != 1 || string(result.AggregateRows[0].Values["mCount"]) != `"1"` {
		t.Fatal("aggregate count bypassed the current user row filter")
	}
	limitedAggregate.Aggregate.Measures = []c.AggregateMeasure{{ID: "mSecret", Op: "sum", Field: priority}}
	reader.request("POST", queryURL, limitedAggregate, nil, 403, nil)
	// Policy activation changes access immediately without editing the template
	// or destroying its owner identity. Clearing an invalid template stays safe.
	admin.request("GET", metadataURL+"/metadata/admin/policy", nil, nil, 200, &policy)
	var rules []c.PermissionRule
	for _, rule := range policy.Current.Rules {
		if rule.ID != queryRule.ID {
			rules = append(rules, rule)
		}
	}
	policy.Current.Rules = rules
	headers = admin.request("PUT", metadataURL+"/metadata/admin/policy", policy.Current, map[string]string{"If-Match": `"` + policy.ETag + `"`}, 200, &policy)
	admin.request("POST", metadataURL+"/metadata/admin/policy/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	reader.request("GET", savedURL+"/"+limited.ID, nil, nil, 403, nil)
	reader.request("POST", savedURL+"/"+limited.ID+"/execute", c.SavedQueryExecuteRequest{}, nil, 403, nil)
	reader.request("GET", savedURL, nil, nil, 200, &empty)
	if len(empty.Data) != 1 || empty.Data[0].ID != limited.ID {
		t.Fatal("owner could not discover a revoked template for cleanup")
	}
	reader.request("DELETE", savedURL+"/"+limited.ID, nil, map[string]string{"If-Match": `"` + limited.ETag + `"`}, 204, nil)
	admin.request("DELETE", itemURL, nil, map[string]string{"If-Match": `"` + reloaded.ETag + `"`}, 204, nil)
	admin.request("GET", itemURL, nil, nil, 404, nil)
}
