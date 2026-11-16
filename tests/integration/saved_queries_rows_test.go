package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func verifySavedInnerRowsHTTP(t *testing.T, admin, foreign *publicSecurityHTTP, sdk *dataSetSDKProcess, metadataURL, dataURL string, category, group c.Organization, schemaID, code, priority string) func() {
	t.Helper()
	var objects c.QueryResult
	admin.request("POST", dataURL+"/data/query", c.QuerySpec{From: c.Source{SchemaID: schemaID}, Select: []string{code}, Page: c.Page{Limit: 10}}, nil, 200, &objects)
	if len(objects.Data) != 3 {
		t.Fatal("inner rows fixture requires three objects")
	}
	definition := c.Definition{CategoryID: category.ID, GroupID: group.ID, Kind: c.KindLink, Name: "保存关联验收", Link: &c.Link{SourceSchemaID: schemaID, TargetSchemaID: schemaID, ForwardName: "后续工单", ReverseName: "前序工单", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	var draft c.CurrentDefinition
	headers := admin.request("POST", metadataURL+"/metadata/definitions", definition, nil, 201, &draft)
	linkID := draft.Definition.ID
	admin.request("POST", metadataURL+"/metadata/definitions/"+linkID+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	for _, child := range objects.Data[1:] {
		admin.request("POST", dataURL+"/data/links/"+linkID+"/instances", c.LinkWriteRequest{SourceID: objects.Data[0].ID, TargetID: child.ID}, nil, 201, nil)
	}
	text := `SELECT "父"."代码", "子"."优先级" FROM "模型演进"."工单" AS "父" JOIN LINK "父"."后续工单" AS "子" WHERE "子"."优先级" >= :下限 LIMIT 1;`
	var template c.SavedQueryTemplate
	admin.request("POST", dataURL+"/data/saved-queries/compile", c.SavedQueryCompileRequest{Text: text, DefaultCategoryID: category.ID}, nil, 200, &template)
	if template.Query.Rows == nil || template.Query.Rows.Joins[0].LinkID != linkID || template.Query.Where.Field != "n1."+priority || template.Query.Where.Parameter != "下限" || len(template.Query.Where.Value) != 0 || len(template.Parameters) != 1 || template.Parameters[0].DataType != "int64" {
		t.Fatal("public row template lost stable scope or parameter identity")
	}
	input := c.SavedQueryInput{Name: "关联行保存验收", Source: c.SavedQuerySource{Kind: "ontology-sql", Text: text, DefaultCategoryID: category.ID}, Query: template.Query, Parameters: template.Parameters}
	var saved c.SavedQuery
	admin.request("POST", dataURL+"/data/saved-queries", input, nil, 201, &saved)
	path := dataURL + "/data/saved-queries/" + saved.ID
	foreign.request("GET", path, nil, nil, 404, nil)
	params := map[string]json.RawMessage{"下限": raw("9007199254740993")}
	cursor := ""
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		var result c.QueryResult
		admin.request("POST", path+"/execute", c.SavedQueryExecuteRequest{Parameters: params, Cursor: cursor}, nil, 200, &result)
		if !result.RowMode || len(result.Data) != 0 || len(result.Rows) != 1 || len(result.Rows[0].Entities) != 2 || seen[result.Rows[0].ID] || string(result.Rows[0].Values["n1."+priority]) != `"9007199254740993"` {
			t.Fatal("public saved row lost tuple, origins or exact int64")
		}
		seen[result.Rows[0].ID] = true
		cursor = result.NextCursor
		if result.HasMore != (i == 0) {
			t.Fatal("public saved row continuation invalid")
		}
	}
	var reloaded c.SavedQuery
	admin.request("GET", path, nil, nil, 200, &reloaded)
	encoded, _ := json.Marshal(reloaded)
	if strings.Contains(string(encoded), "9007199254740993") {
		t.Fatal("runtime parameter entered public template")
	}
	sdk.stage("rows", map[string]any{"linkId": linkID})
	return func() {
		t.Helper()
		admin.request("GET", path, nil, nil, 200, &reloaded)
		var result c.QueryResult
		admin.request("POST", path+"/execute", c.SavedQueryExecuteRequest{Parameters: params}, nil, 200, &result)
		if !result.RowMode || len(result.Rows) != 1 || result.Rows[0].Entities[0].InstanceID != objects.Data[0].ID || result.Rows[0].Entities[1].SchemaID != schemaID || string(result.Rows[0].Values["n1."+priority]) != `"9007199254740993"` {
			t.Fatal("saved row rebound renamed/replaced object")
		}
		admin.request("DELETE", path, nil, map[string]string{"If-Match": `"` + saved.ETag + `"`}, 204, nil)
	}
}
