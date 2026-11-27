package integration_test

import (
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func verifyMappedLinkLifecycle(t *testing.T, de *storage.Engine, admin, outsider *publicSecurityHTTP,
	dataURL string, definitionURL func(string) string, publish func(c.Definition) c.CurrentDefinition,
	tenant string, input c.DataSourceInput) {
	t.Helper()
	t.Log("mapped Link lifecycle over public Metadata/Data HTTP")
	var source c.DataSource
	admin.request("POST", dataURL+"/data/sources", input, nil, 201, &source)
	t.Cleanup(func() {
		for _, table := range []string{"af_data_mapped_steps", "af_data_mapped_targets", "af_data_sources"} {
			if _, err := de.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant); err != nil {
				t.Error(err)
			}
		}
	})
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := de.DB().Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	suffix := strings.ReplaceAll(c.NewID(), "-", "")
	endpoints, edges := "lc_endpoints_"+suffix, "lc_edges_"+suffix
	exec("CREATE TABLE " + endpoints + " (tenant VARCHAR(128) NOT NULL, id BIGINT NOT NULL, name VARCHAR(40) NOT NULL, fk BIGINT NULL, PRIMARY KEY(tenant,id))")
	t.Cleanup(func() { exec("DROP TABLE " + endpoints) })
	exec("CREATE TABLE " + edges + " (tenant VARCHAR(128) NOT NULL, source_id BIGINT NOT NULL, target_id BIGINT NOT NULL, PRIMARY KEY(tenant,source_id,target_id))")
	t.Cleanup(func() { exec("DROP TABLE " + edges) })
	exec("INSERT INTO "+endpoints+" (tenant,id,name) VALUES (?,1,'source'),(?,2,'target')", tenant, tenant)
	exec("INSERT INTO "+edges+" VALUES (?,1,2)", tenant+"-other")
	endpoint := publish(c.Definition{Kind: c.KindSchema, Name: "映射关系验收对象", Schema: &c.Schema{
		Mode: "db_2_schema", StorageLayout: "single",
		StorageBinding: &c.StorageBinding{DataSourceID: source.ID, Table: endpoints, IDColumn: "id", TenantColumn: "tenant"},
		Properties: []c.Property{{Name: "名称", DataType: "string", Required: true,
			Mapping: &c.PropertyMapping{DataSourceID: source.ID, Column: "name"}}},
	}})
	for _, fixture := range []struct {
		name, cardinality string
		binding           c.LinkStorageBinding
		add, remove       string
	}{
		{"关联表验收关系", "N:M", c.LinkStorageBinding{Mode: "junction", DataSourceID: source.ID, Table: edges,
			TenantColumn: "tenant", SourceColumn: "source_id", TargetColumn: "target_id"},
			"INSERT INTO " + edges + " VALUES (?,1,2)", "DELETE FROM " + edges + " WHERE tenant=?"},
		{"外键验收关系", "N:1", c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: source.ID,
			ForeignKeySide: "source", ForeignKeyColumn: "fk"},
			"UPDATE " + endpoints + " SET fk=2 WHERE tenant=? AND id=1", "UPDATE " + endpoints + " SET fk=NULL WHERE tenant=? AND id=1"},
	} {
		link := publish(c.Definition{Kind: c.KindLink, Name: fixture.name, Link: &c.Link{
			SourceSchemaID: endpoint.Definition.ID, TargetSchemaID: endpoint.Definition.ID,
			ForwardName: "关联", ReverseName: "被关联", Cardinality: fixture.cardinality, OnDelete: "restrict", StorageBinding: &fixture.binding,
		}})
		path := definitionURL(link.Definition.ID)
		match := map[string]string{"If-Match": `"` + link.ETag + `"`}
		query := c.QuerySpec{
			From:   c.Source{SchemaID: endpoint.Definition.ID},
			Via:    &c.LinkTraversal{LinkID: link.Definition.ID, Direction: "out", InstanceID: "1"},
			Select: []string{endpoint.Definition.Schema.Properties[0].ID}, Page: c.Page{Limit: 10},
		}
		admin.request("GET", path+"/delete-preview", nil, match, 409, nil)
		exec(fixture.add, tenant)
		admin.request("POST", dataURL+"/data/query", query, nil, 200, nil)
		var blocked c.Error
		admin.request("GET", path+"/disable-preview", nil, match, 409, &blocked)
		if blocked.Code != "definition_data_in_use" {
			t.Fatalf("%s: disable preview returned %q for existing data", fixture.name, blocked.Code)
		}
		exec(fixture.remove, tenant)
		admin.request("GET", path+"/disable-preview", nil, match, 200, nil)
		admin.request("POST", path+"/disable", nil, match, 200, nil)
		admin.request("POST", dataURL+"/data/query", query, nil, 409, nil)
		admin.request("GET", path+"/restore-preview", nil, match, 200, nil)
		admin.request("POST", path+"/restore", nil, match, 200, nil)
		admin.request("POST", dataURL+"/data/query", query, nil, 200, nil)
		admin.request("POST", path+"/disable", nil, match, 200, nil)
		outsider.request("GET", path+"/delete-preview", nil, match, 404, nil)
		admin.request("GET", path+"/delete-preview", nil, match, 204, nil)
		exec(fixture.add, tenant)
		admin.request("DELETE", path, nil, match, 409, &blocked)
		if blocked.Code != "definition_data_in_use" {
			t.Fatalf("%s: delete returned %q for data added after preview", fixture.name, blocked.Code)
		}
		exec(fixture.remove, tenant)
		admin.request("DELETE", path, nil, map[string]string{"If-Match": `"stale"`}, 412, nil)
		admin.request("DELETE", path, nil, match, 204, nil)
		admin.request("DELETE", path, nil, match, 204, nil)
		admin.request("POST", path+"/restore", nil, match, 404, nil)
	}
	var count int
	if err := de.DB().QueryRow("SELECT COUNT(*) FROM "+edges+" WHERE tenant=?", tenant+"-other").Scan(&count); err != nil || count != 1 {
		t.Fatalf("mapped Link deletion affected another tenant's relation: count=%d, err=%v", count, err)
	}
}
