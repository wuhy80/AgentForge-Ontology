package data

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

// Keep the platform control store independent of the external MySQL release
// under certification. Existing local runs retain their original fallback.
func sourceIntegrationDSN(driver, fallbackEnv string) string {
	if driver == "mysql" {
		if dsn := os.Getenv("MYSQL_SOURCE_TEST_DSN"); dsn != "" {
			return dsn
		}
	}
	return os.Getenv(fallbackEnv)
}

// A release used only as the secondary projection source does not exercise its
// own query compiler, primary-table DDL or index migration. Keep those paths in
// the certification suite, independently of the platform control database.
func TestSourceVersionPrimaryPublicationIndexesAndTypedQuery(t *testing.T) {
	for _, backend := range []struct{ driver, env string }{{"mysql", "MYSQL_TEST_DSN"}, {"postgresql", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			f := newMappedIntegrationFixture(t, backend.driver, sourceIntegrationDSN(backend.driver, backend.env))
			source := f.sources[1]
			version, err := storage.InspectDatabaseVersion(f.ctx, f.dbs[1], backend.driver, "auto", "")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("candidate source: product=%s server=%s profile=%s", version.Product, version.ServerVersion, version.CapabilityProfile)
			namespace, _ := sourceNamespace(source, "")
			table := "version_primary_" + strings.ReplaceAll(c.NewID(), "-", "")[:12]
			f.dropOnCleanup(1, table)
			d := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: "版本主表验证", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", StorageBinding: &c.StorageBinding{DataSourceID: source.ID, Namespace: namespace, Table: table, IDColumn: "id", TenantColumn: "tenant"}}}
			for _, property := range []c.Property{
				{Name: "编码", DataType: "string", Required: true, Mapping: &c.PropertyMapping{DataSourceID: source.ID, Column: "code"}},
				{Name: "金额", DataType: "decimal", Precision: 20, Scale: 4, Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: source.ID, Column: "amount"}},
				{Name: "时间", DataType: "timestamp", Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: source.ID, Column: "observed_at"}},
				{Name: "启用", DataType: "boolean", Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: source.ID, Column: "enabled"}},
			} {
				property.ID = c.NewID()
				d.Schema.Properties = append(d.Schema.Properties, property)
			}
			d.Schema.Indexes = []c.Index{{ID: c.NewID(), Name: "编码唯一", Unique: true, FieldIDs: []string{d.Schema.Properties[0].ID}}}
			preview := f.request("POST", "/internal/publications/preview", c.PrepareRequest{Definition: d}, true)
			if preview.Code != 200 || !strings.Contains(preview.Body.String(), "CREATE TABLE") {
				t.Fatalf("primary publication preview failed: %d %s", preview.Code, preview.Body)
			}
			var exists int
			if err := f.dbs[1].QueryRowContext(f.ctx, sourceBind(backend.driver, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=? AND table_name=?"), namespace, table).Scan(&exists); err != nil || exists != 0 {
				t.Fatal("primary preview modified the candidate database")
			}
			operation := c.NewID()
			if response := f.prepare(d, "", operation); response.Code != 200 {
				t.Fatalf("primary forward prepare: %d %s", response.Code, response.Body)
			}
			f.activate(d, operation)
			catalog, err := readSourceTable(f.ctx, f.dbs[1], source, namespace, table)
			if err != nil {
				t.Fatal(err)
			}
			hasCodeIndex := false
			for _, index := range catalog.Indexes {
				if index.Unique && len(index.Columns) == 2 && index.Columns[0] == "tenant" && index.Columns[1] == "code" {
					hasCodeIndex = true
				}
			}
			if !hasCodeIndex {
				t.Fatal("candidate database lost the tenant-scoped unique index")
			}
			instant := time.Date(2026, 11, 7, 13, 14, 15, 123456000, time.UTC)
			ids := []string{c.NewID(), c.NewID()}
			for index, id := range ids {
				f.exec(1, "INSERT INTO "+f.qualified(1, table)+" (tenant,id,code,amount,observed_at,enabled) VALUES (?,?,?,?,?,?)", f.tenant, id, "C"+string(rune('1'+index)), "9007199254740993.0000", instant, true)
			}
			// Adjacent integers above 2^53 must not compare equal through DOUBLE.
			f.exec(1, "INSERT INTO "+f.qualified(1, table)+" (tenant,id,code,amount,observed_at,enabled) VALUES (?,?,?,?,?,?)", f.tenant, c.NewID(), "C3", "9007199254740992.0000", instant, true)
			if _, err := f.dbs[1].ExecContext(f.ctx, sourceBind(backend.driver, "INSERT INTO "+f.qualified(1, table)+" (tenant,id,code) VALUES (?,?,?)"), f.tenant, c.NewID(), "C1"); err == nil {
				t.Fatal("candidate unique index accepted a duplicate code in one tenant")
			}
			selectFields := []string{}
			for _, property := range d.Schema.Properties {
				selectFields = append(selectFields, property.ID)
			}
			query := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Select: selectFields, Where: &c.Expression{Op: "eq", Field: d.Schema.Properties[1].ID, Value: raw("9007199254740993.0000")}, Page: c.Page{Limit: 1}}
			first := f.query(query, 200)
			if len(first.Data) != 1 || !first.HasMore || first.NextCursor == "" || string(first.Data[0].Values[d.Schema.Properties[1].ID]) != `"9007199254740993.0000"` || string(first.Data[0].Values[d.Schema.Properties[2].ID]) != `"2026-11-07T13:14:15.123456Z"` || string(first.Data[0].Values[d.Schema.Properties[3].ID]) != "true" {
				t.Fatalf("candidate typed primary query lost precision or cursor: %+v", first)
			}
			query.Page.Cursor = first.NextCursor
			second := f.query(query, 200)
			if len(second.Data) != 1 || second.HasMore || second.Data[0].ID == first.Data[0].ID {
				t.Fatal("candidate primary cursor did not select the next exact row")
			}
			next := d
			next.Schema = &c.Schema{Mode: d.Schema.Mode, StorageLayout: d.Schema.StorageLayout, StorageBinding: d.Schema.StorageBinding, Properties: append([]c.Property(nil), d.Schema.Properties...), Indexes: append([]c.Index(nil), d.Schema.Indexes...)}
			note := c.Property{ID: c.NewID(), Name: "说明", DataType: "string", Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: source.ID, Column: "note"}}
			next.Schema.Properties = append(next.Schema.Properties, note)
			next.Schema.Indexes = append(next.Schema.Indexes, c.Index{ID: c.NewID(), Name: "说明索引", FieldIDs: []string{note.ID}})
			operation = c.NewID()
			if response := f.prepare(next, digest(d), operation); response.Code != 200 {
				t.Fatalf("candidate additive column/index prepare: %d %s", response.Code, response.Body)
			}
			f.activate(next, operation)
			catalog, err = readSourceTable(f.ctx, f.dbs[1], source, namespace, table)
			if err != nil {
				t.Fatal(err)
			}
			hasNoteColumn, hasNoteIndex := false, false
			for _, column := range catalog.Columns {
				hasNoteColumn = hasNoteColumn || column.Name == "note" && column.Nullable
			}
			for _, index := range catalog.Indexes {
				hasNoteIndex = hasNoteIndex || !index.Unique && len(index.Columns) == 2 && index.Columns[0] == "tenant" && index.Columns[1] == "note"
			}
			if !hasNoteColumn || !hasNoteIndex {
				t.Fatal("candidate database lost the additive column or index")
			}
			query.Page.Cursor = ""
			query.Select = append(query.Select, note.ID)
			result := f.query(query, 200)
			if len(result.Data) != 1 || !json.Valid(result.Data[0].Values[note.ID]) || string(result.Data[0].Values[note.ID]) != "null" {
				t.Fatal("additive candidate migration altered existing row values")
			}
		})
	}
}
