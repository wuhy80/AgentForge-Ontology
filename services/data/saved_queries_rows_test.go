package data

import (
	"encoding/json"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestSavedQueryMySQLInnerRowsParametersPaginationAndRevocation(t *testing.T) {
	f := newSavedQueriesFixture(t)
	amount := c.Property{ID: c.NewID(), Name: "金额", DataType: "decimal", Precision: 24, Scale: 4, Required: true}
	target := c.Definition{ID: c.NewID(), TenantID: f.tenant, Kind: c.KindSchema, Name: "关联明细", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{amount}}}
	publishRowDefinition(t, f.mappedIntegrationFixture, target)
	t.Cleanup(func() { _, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + quote(tableName(target.ID))) })
	link := c.Definition{ID: c.NewID(), TenantID: f.tenant, Kind: c.KindLink, Name: "对象明细", Link: &c.Link{SourceSchemaID: f.definition.ID, TargetSchemaID: target.ID, ForwardName: "明细", ReverseName: "对象", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	publishRowDefinition(t, f.mappedIntegrationFixture, link)
	t.Cleanup(func() { _, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + quote(linkTableName(link.ID))) })
	create := func(schema, field, value string) c.Instance {
		w := f.request("POST", "/data/schemas/"+schema+"/instances", c.WriteRequest{IdempotencyKey: c.NewID(), Values: map[string]json.RawMessage{field: raw(value)}}, false)
		if w.Code != 201 {
			t.Fatalf("create row query object: %d %s", w.Code, w.Body)
		}
		return mutationDecode[c.Instance](t, w)
	}
	root := create(f.definition.ID, f.definition.Schema.Properties[0].ID, "A")
	for i := 0; i < 2; i++ {
		child := create(target.ID, amount.ID, "9007199254740993.0001")
		w := f.request("POST", "/data/links/"+link.ID+"/instances", c.LinkWriteRequest{SourceID: root.ID, TargetID: child.ID}, false)
		if w.Code != 201 {
			t.Fatalf("create saved row link: %d %s", w.Code, w.Body)
		}
	}
	f.input.Query = c.QuerySpec{From: c.Source{SchemaID: f.definition.ID}, Rows: &c.RowQuery{
		Nodes: []c.RowQueryNode{{ID: "n0", SchemaID: f.definition.ID, Alias: "对象"}, {ID: "n1", SchemaID: target.ID, Alias: "明细一"}, {ID: "n2", SchemaID: target.ID, Alias: "明细二"}},
		Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: link.ID, Direction: "out"}, {From: "n0", To: "n2", LinkID: link.ID, Direction: "out"}},
	}, Select: []string{"n0." + f.definition.Schema.Properties[0].ID, "n1." + amount.ID, "n2." + amount.ID}, Where: &c.Expression{Op: "gte", Field: "n1." + amount.ID, Parameter: "最低金额"}, OrderBy: []c.Order{{Field: "n2." + amount.ID, Direction: "DESC"}}, Page: c.Page{Limit: 1}}
	f.input.Parameters = []c.SavedQueryParameter{declarationForProperty("最低金额", amount, false)}
	saved := mutationDecode[c.SavedQuery](t, f.own("POST", "/data/saved-queries", "", f.input, 201))
	path := "/data/saved-queries/" + saved.ID
	f.own("GET", path, "", nil, 200)
	for _, id := range []string{target.ID, link.ID, amount.ID} {
		listed := f.own("GET", "/data/saved-queries?definitionId="+id, "", nil, 200)
		page := mutationDecode[c.SavedQueryPage](t, listed)
		if (len(page.Data) == 1) != (id != amount.ID) {
			t.Fatalf("unexpected binding matches for %s: %+v", id, page)
		}
	}
	bound := decodeQueryBinding(t, f.request("POST", "/data/query/bind", c.QuerySpec{From: saved.Query.From, Rows: saved.Query.Rows, Select: saved.Query.Select, Page: saved.Query.Page}, false))
	if len(bound.Fields) != 3 || bound.Query.Rows == nil {
		t.Fatal("bound row scope missing")
	}
	parameters := map[string]json.RawMessage{"最低金额": raw("9007199254740993.0001")}
	cursor, firstCursor := "", ""
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		result := mutationDecode[c.QueryResult](t, f.own("POST", path+"/execute", "", c.SavedQueryExecuteRequest{Parameters: parameters, Cursor: cursor}, 200))
		if !result.RowMode || len(result.Rows) != 1 || len(result.Data) != 0 || len(result.Rows[0].Entities) != 3 || string(result.Rows[0].Values["n1."+amount.ID]) != `"9007199254740993.0001"` || seen[result.Rows[0].ID] {
			t.Fatalf("saved row pagination lost precision or tuple: %+v", result)
		}
		seen[result.Rows[0].ID] = true
		if result.HasMore != (i < 3) {
			t.Fatal("incorrect saved row continuation")
		}
		cursor = result.NextCursor
		if i == 0 {
			firstCursor = cursor
		}
	}
	f.own("POST", path+"/execute", "", c.SavedQueryExecuteRequest{Parameters: map[string]json.RawMessage{"最低金额": raw("0.0000")}, Cursor: firstCursor}, 422)
	var persisted string
	if err := f.engine.DB().QueryRow("SELECT template_json FROM af_data_saved_queries WHERE tenant_id=? AND user_id=? AND id=?", f.tenant, "saved-owner", saved.ID).Scan(&persisted); err != nil || strings.Contains(persisted, "9007199254740993") || strings.Contains(persisted, `"value"`) {
		t.Fatal("runtime parameter persisted", err)
	}
	f.authorize = func(request c.AuthorizationRequest) c.Decision {
		d := f.decision
		if request.ResourceID == target.ID && request.Operation == "instance.query" {
			d.Allow = false
		}
		return d
	}
	f.own("GET", path, "", nil, 403)
	f.own("POST", path+"/execute", "", c.SavedQueryExecuteRequest{Parameters: parameters, Cursor: firstCursor}, 403)
	f.authorize = func(request c.AuthorizationRequest) c.Decision {
		d := f.decision
		if request.ResourceID == link.ID && request.Operation == "link.query" {
			d.Allow = false
		}
		return d
	}
	f.own("GET", path, "", nil, 403)
	f.own("POST", path+"/execute", "", c.SavedQueryExecuteRequest{Parameters: parameters}, 403)
	f.own("DELETE", path, saved.ETag, nil, 204)
	f.authorize = nil
	f.input.Query = c.QuerySpec{
		From:     c.Source{SchemaID: f.definition.ID},
		Relation: &c.RelationFilter{LinkID: link.ID, Direction: "out", Operator: "exists"},
		Select:   []string{f.definition.Schema.Properties[0].ID},
		Page:     c.Page{Limit: 1},
	}
	f.input.Parameters = nil
	filtered := mutationDecode[c.SavedQuery](t, f.own("POST", "/data/saved-queries", "", f.input, 201))
	listed := mutationDecode[c.SavedQueryPage](t, f.own("GET", "/data/saved-queries?definitionId="+link.ID, "", nil, 200))
	if len(listed.Data) != 1 || listed.Data[0].ID != filtered.ID {
		t.Fatalf("missing exists filter Link binding: %+v", listed)
	}
}
