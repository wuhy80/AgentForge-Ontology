package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

// Tests must leave no business records in the shared development databases.
// The generated tenant and generated physical names constrain every cleanup.
func cleanupOntologyTenant(t *testing.T, me, de *storage.Engine, tenant string) {
	t.Helper()
	rows, err := de.DB().Query("SELECT definition FROM af_data_gates WHERE tenant_id=?", tenant)
	if err != nil {
		t.Error(err)
		return
	}
	var schemas, links []string
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Error(err)
			continue
		}
		var d c.Definition
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Error(err)
			continue
		}
		if d.TenantID != tenant {
			t.Error("cleanup gate tenant mismatch")
			continue
		}
		encoded, _ := json.Marshal(d.ID)
		sum := sha256.Sum256(encoded)
		if d.Kind == c.KindSchema && d.Schema != nil && d.Schema.StorageBinding == nil {
			schemas = append(schemas, "af_s_"+hex.EncodeToString(sum[:])[:32])
		}
		if d.Kind == c.KindLink && d.Link != nil && d.Link.StorageBinding != nil && d.Link.StorageBinding.Mode == "managed" {
			links = append(links, "af_l_"+hex.EncodeToString(sum[:])[:32])
		}
	}
	if err := rows.Err(); err != nil {
		t.Error(err)
	}
	_ = rows.Close()
	for _, table := range append(links, schemas...) {
		if _, err := de.DB().Exec("DROP TABLE IF EXISTS `" + table + "`"); err != nil {
			t.Error(err)
		}
	}
	for _, table := range []string{"af_data_saved_queries", "af_data_saved_query_owners", "af_data_schema_migrations", "af_data_outbox", "af_data_idempotency", "af_data_write_receipts", "af_data_transaction_receipts", "af_data_gates"} {
		if _, err := de.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant); err != nil {
			t.Error(err)
		}
	}
	for _, table := range []string{"af_meta_dependencies", "af_meta_outbox", "af_meta_publications", "af_meta_definitions", "af_meta_organizations", "af_meta_tenant_guards"} {
		if _, err := me.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant); err != nil {
			t.Error(err)
		}
	}
}

// Shares the real Metadata/Data/OPA harness, including publication coordination.
// These checks exercise SQL relation filtering, rather than a mocked JOIN result.
func verifyManagedLinks(t *testing.T, h harness, ctx context.Context, me, de *storage.Engine,
	metadataURL, dataURL, opaURL, tenant, admin, reader, other, categoryID, groupID, schemaID, codeID, nameID, deptID string,
	first, second, hidden c.Instance, readerOps map[string]any) {
	t.Helper()
	t.Log("managed Link publication, SQL navigation, independent relation/endpoint authorization")
	definition := c.Definition{CategoryID: categoryID, GroupID: groupID, Kind: c.KindLink, Name: "设备关联设备", Link: &c.Link{
		SourceSchemaID: schemaID, TargetSchemaID: schemaID, ForwardName: "关联设备", ReverseName: "被设备关联", Cardinality: "N:M", OnDelete: "restrict",
	}}
	var draft c.CurrentDefinition
	headers := h.request("POST", metadataURL+"/metadata/definitions", admin, "", definition, 201, &draft)
	id := draft.Definition.ID
	definitionURL := metadataURL + "/metadata/definitions/" + id
	edgesURL := dataURL + "/data/links/" + id + "/instances"
	var publication c.Publication
	h.request("POST", definitionURL+"/publish", admin, headers.Get("ETag"), nil, 200, &publication)
	t.Cleanup(func() {
		// Only remove this helper's newly-created definition/table and its events.
		body, _ := json.Marshal(id)
		sum := sha256.Sum256(body)
		table := "af_l_" + hex.EncodeToString(sum[:])[:32]
		if _, err := de.DB().Exec("DROP TABLE IF EXISTS `" + table + "`"); err != nil {
			t.Errorf("clean isolated Link table: %v", err)
		}
		for _, table := range []string{"af_data_outbox", "af_data_gates"} {
			column := "definition_id"
			if table == "af_data_outbox" {
				column = "schema_id"
			}
			if _, err := de.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=? AND "+column+"=?", tenant, id); err != nil {
				t.Error(err)
			}
		}
		for _, table := range []string{"af_meta_dependencies", "af_meta_outbox", "af_meta_publications", "af_meta_definitions"} {
			column := "definition_id"
			switch table {
			case "af_meta_dependencies":
				column = "source_id"
			case "af_meta_outbox":
				column = "object_id"
			case "af_meta_definitions":
				column = "id"
			}
			if _, err := me.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=? AND "+column+"=?", tenant, id); err != nil {
				t.Error(err)
			}
		}
	})
	query := c.QuerySpec{From: c.Source{SchemaID: schemaID}, Via: &c.LinkTraversal{LinkID: id, Direction: "out", InstanceID: first.ID}, Select: []string{codeID, nameID}, OrderBy: []c.Order{{Field: codeID, Direction: "asc"}}, Page: c.Page{Limit: 1}}
	h.request("POST", dataURL+"/data/query", admin, "", query, 422, nil)
	// Existing metadata-only definitions can acquire their first binding.
	var current c.CurrentDefinition
	h.request("GET", definitionURL, admin, "", nil, 200, &current)
	current.Definition.Link.StorageBinding = &c.LinkStorageBinding{Mode: "managed"}
	headers = h.request("PUT", definitionURL+"/draft", admin, `"`+current.ETag+`"`, current.Definition, 200, &draft)
	h.request("POST", definitionURL+"/publish", admin, headers.Get("ETag"), nil, 200, &publication)
	var page c.QueryResult
	h.request("POST", dataURL+"/data/query", admin, "", query, 200, &page)
	if len(page.Data) != 0 || page.HasMore {
		t.Fatal("newly-bound Link must have no synthetic relation facts")
	}
	var edge, hiddenEdge c.LinkInstance
	edgeHeaders := h.request("POST", edgesURL, admin, "", c.LinkWriteRequest{SourceID: first.ID, TargetID: second.ID}, 201, &edge)
	h.request("POST", edgesURL, admin, "", c.LinkWriteRequest{SourceID: first.ID, TargetID: hidden.ID}, 201, &hiddenEdge)
	h.request("POST", edgesURL, admin, "", c.LinkWriteRequest{SourceID: first.ID, TargetID: second.ID}, 409, nil)
	h.request("POST", dataURL+"/data/query", other, "", query, 404, nil)
	// Having instance permissions alone must not reveal relation facts.
	h.request("POST", dataURL+"/data/query", reader, "", query, 403, nil)
	readerOps["link.query"] = map[string]any{id: map[string]any{"allow": true, "readFields": []string{"*"}}}
	h.request("PUT", opaURL+"/v1/data/grants/"+url.PathEscape(tenant)+"/reader", "", "", readerOps, 204, nil)
	h.request("POST", dataURL+"/data/query", reader, "", query, 200, &page)
	if len(page.Data) != 1 || page.Data[0].ID != second.ID || page.HasMore || len(page.Data[0].Values) != 2 {
		t.Fatalf("target row/field filtering must precede pagination: %+v", page)
	}
	query.Select = []string{deptID}
	h.request("POST", dataURL+"/data/query", reader, "", query, 403, nil)
	query.Select = []string{codeID, nameID}
	query.Via.InstanceID = hidden.ID
	h.request("POST", dataURL+"/data/query", reader, "", query, 404, nil)
	query.Via.InstanceID = first.ID
	h.request("POST", edgesURL, reader, "", c.LinkWriteRequest{SourceID: second.ID, TargetID: first.ID}, 403, nil)
	var edges struct {
		Data    []c.LinkInstance `json:"data"`
		HasMore bool             `json:"hasMore"`
	}
	h.request("GET", edgesURL+"?direction=out&instanceId="+first.ID+"&limit=1", reader, "", nil, 200, &edges)
	if len(edges.Data) != 1 || edges.Data[0].ID != edge.ID || edges.HasMore {
		t.Fatalf("edge listing leaked hidden endpoint: %+v", edges)
	}
	h.request("POST", dataURL+"/data/query", admin, "", query, 200, &page)
	if len(page.Data) != 1 || page.Data[0].ID != second.ID || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("Link first page: %+v", page)
	}
	cursor := page.NextCursor
	query.Page.Cursor = cursor
	h.request("POST", dataURL+"/data/query", admin, "", query, 200, &page)
	if len(page.Data) != 1 || page.Data[0].ID != hidden.ID || page.HasMore {
		t.Fatalf("Link second page: %+v", page)
	}
	// Changing only the Link's current display definition still invalidates its
	// old query cursor, without changing the persisted relation identities.
	h.request("GET", definitionURL, admin, "", nil, 200, &current)
	current.Definition.Link.ForwardName = "关联的设备"
	headers = h.request("PUT", definitionURL+"/draft", admin, `"`+current.ETag+`"`, current.Definition, 200, &draft)
	h.request("POST", definitionURL+"/publish", admin, headers.Get("ETag"), nil, 200, &publication)
	query.Page.Cursor = cursor
	h.request("POST", dataURL+"/data/query", admin, "", query, 422, nil)
	query.Via.Direction, query.Via.InstanceID = "in", second.ID
	h.request("POST", dataURL+"/data/query", admin, "", query, 422, nil)
	query.Page.Cursor = ""
	h.request("POST", dataURL+"/data/query", reader, "", query, 200, &page)
	if len(page.Data) != 1 || page.Data[0].ID != first.ID {
		t.Fatalf("reverse Link did not use the same relation fact: %+v", page)
	}
	var endpoint c.Instance
	endpointHeaders := h.request("GET", dataURL+"/data/schemas/"+schemaID+"/instances/"+first.ID, admin, "", nil, 200, &endpoint)
	h.request("DELETE", dataURL+"/data/schemas/"+schemaID+"/instances/"+first.ID, admin, endpointHeaders.Get("ETag"), nil, 409, nil)
	h.request("DELETE", edgesURL+"/"+edge.ID, admin, "", nil, 428, nil)
	h.request("DELETE", edgesURL+"/"+edge.ID, admin, `"`+c.NewID()+`"`, nil, 412, nil)
	h.request("DELETE", edgesURL+"/"+edge.ID, admin, edgeHeaders.Get("ETag"), nil, 204, nil)
	h.request("POST", dataURL+"/data/query", reader, "", query, 200, &page)
	if len(page.Data) != 0 {
		t.Fatal("deleted relation remains visible in reverse")
	}
	var events int
	if err := de.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM af_data_outbox WHERE tenant_id=? AND schema_id=?", tenant, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 3 {
		t.Fatalf("two creates and one delete must produce exactly three relation events: %d", events)
	}
}
