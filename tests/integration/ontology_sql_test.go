package integration_test

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Reuses the isolated tenant, published model and real OPA decisions. No records
// are added by text compilation, completion, formatting or execution.
func verifyOntologySQL(t *testing.T, h harness, dataURL, opaURL, tenant, admin, reader, categoryID, schemaID, codeID, nameID, priceID, countID string, first, second c.Instance, readerOps map[string]any) {
	t.Helper()
	t.Log("OntologySQL shares typed QuerySpec, row/field authorization and cursor execution")
	grant := map[string]any{"allow": true}
	withDirectory := map[string]any{}
	for key, value := range readerOps {
		withDirectory[key] = value
	}
	withDirectory["definition.read"] = map[string]any{"*": grant}
	withDirectory["organization.read"] = map[string]any{"*": grant}
	policyURL := opaURL + "/v1/data/grants/" + url.PathEscape(tenant) + "/reader"
	h.request("PUT", policyURL, "", "", withDirectory, 204, nil)
	defer h.request("PUT", policyURL, "", "", readerOps, 204, nil)
	base := dataURL + "/data/query/ontology-sql"
	var context struct {
		Models []struct {
			SchemaID string `json:"schemaId"`
			Fields   []struct {
				ID string `json:"id"`
			} `json:"fields"`
		} `json:"models"`
	}
	h.request("GET", dataURL+"/data/query/context?categoryId="+url.QueryEscape(categoryID), "", "", nil, 401, nil)
	headers := h.request("GET", dataURL+"/data/query/context?categoryId="+url.QueryEscape(categoryID), reader, "", nil, 200, &context)
	if headers.Get("Cache-Control") != "no-store" || len(context.Models) != 1 || context.Models[0].SchemaID != schemaID || len(context.Models[0].Fields) != 2 {
		t.Fatal("query context must expose only authorized model and query fields without caching")
	}
	for _, field := range context.Models[0].Fields {
		if field.ID != codeID && field.ID != nameID {
			t.Fatal("hidden field leaked through query context")
		}
	}
	text := `SELECT "设"."设备编码", "设"."设备名称" FROM "生产管理"."设备" AS "设" WHERE STARTS_WITH("设"."设备编码", :前缀) ORDER BY "设"."设备编码" ASC LIMIT 1;`
	request := c.OntologySQLRequest{Text: text, DefaultCategoryID: categoryID, RequestID: "integration-text"}
	var analysis c.OntologySQLAnalysis
	h.request("POST", base+"/analyze", reader, "", request, 200, &analysis)
	if analysis.Ready || len(analysis.Parameters) != 1 || analysis.Parameters[0].Name != "前缀" || analysis.Parameters[0].Provided {
		t.Fatal("analysis must infer missing typed parameters without executing")
	}
	request.Parameters = map[string]json.RawMessage{"前缀": raw("PUMP-")}
	h.request("POST", base+"/analyze", reader, "", request, 200, &analysis)
	if !analysis.Ready || analysis.Query == nil || analysis.Query.From.SchemaID != schemaID || analysis.Query.Select[0] != codeID || analysis.RequestID != request.RequestID || analysis.TextHash == "" {
		t.Fatalf("text did not bind to the stable authorized QuerySpec: %+v", analysis)
	}
	var result c.OntologySQLResult
	h.request("POST", base, reader, "", request, 200, &result)
	if len(result.Data) != 1 || result.Data[0].ID != first.ID || !result.HasMore || result.NextCursor == "" {
		t.Fatal("text execution must apply row authorization before LIMIT and return the shared cursor")
	}
	var jsonResult c.QueryResult
	h.request("POST", dataURL+"/data/query", reader, "", result.Query, 200, &jsonResult)
	if !reflect.DeepEqual(result.Data, jsonResult.Data) || !reflect.DeepEqual(result.Fields, jsonResult.Fields) || result.HasMore != jsonResult.HasMore {
		t.Fatal("text and bound JSON query results differ")
	}
	result.Query.Page.Cursor = result.NextCursor
	h.request("POST", dataURL+"/data/query", reader, "", result.Query, 200, &jsonResult)
	if len(jsonResult.Data) != 1 || jsonResult.Data[0].ID != second.ID || jsonResult.HasMore {
		t.Fatal("text execution context did not continue through the shared JSON entry")
	}

	var hidden c.OntologySQLAnalysis
	h.request("POST", base+"/analyze", reader, "", c.OntologySQLRequest{Text: `SELECT "采购价" FROM "生产管理"."设备"`}, 200, &hidden)
	if hidden.Ready || hidden.Query != nil || len(hidden.Fields) != 0 || len(hidden.Diagnostics) == 0 {
		t.Fatal("analysis of a hidden property must fail without returning hidden bindings")
	}
	completionText := `SELECT "设备". FROM "生产管理"."设备"`
	var complete c.OntologySQLCompletions
	h.request("POST", base+"/complete", reader, "", c.OntologySQLRequest{Text: completionText, CursorOffset: len([]rune(`SELECT "设备".`))}, 200, &complete)
	encoded, _ := json.Marshal(complete)
	if !strings.Contains(string(encoded), "设备编码") || strings.Contains(string(encoded), "采购价") {
		t.Fatal("incomplete text completion must expose authorized fields only")
	}

	for _, unsupported := range []string{`SELECT * FROM "生产管理"."设备"`, text + ` SELECT "设备编码" FROM "生产管理"."设备"`, `DELETE FROM "生产管理"."设备"`} {
		h.request("POST", base, admin, "", c.OntologySQLRequest{Text: unsupported, Parameters: request.Parameters}, 422, nil)
	}
	var exact c.OntologySQLResult
	h.request("POST", base, admin, "", c.OntologySQLRequest{Text: `SELECT "采购价", "累计计数" FROM "生产管理"."设备" WHERE "累计计数" = 9007199254740993 AND "采购价" = :价格`, Parameters: map[string]json.RawMessage{"价格": raw("123456789012345.678")}}, 200, &exact)
	if len(exact.Data) != 1 || exact.Data[0].ID != first.ID || string(exact.Data[0].Values[countID]) != `"9007199254740993"` || string(exact.Data[0].Values[priceID]) != `"123456789012345.678"` {
		t.Fatal("text literals and typed parameters must retain int64 and decimal precision in real MySQL")
	}
	var formatted c.OntologySQLFormat
	h.request("POST", base+"/format", reader, "", request, 200, &formatted)
	if formatted.Text == "" || len(formatted.Diagnostics) != 0 {
		t.Fatal("valid text must format without dropping query content")
	}
	request.Text = formatted.Text
	h.request("POST", base+"/analyze", reader, "", request, 200, &analysis)
	if !analysis.Ready {
		t.Fatal("formatted text must remain valid and retain parameter binding")
	}
}
