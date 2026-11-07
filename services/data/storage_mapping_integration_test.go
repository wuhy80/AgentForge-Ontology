package data

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func readStorageMapping(t *testing.T, f *mappedIntegrationFixture, d c.Definition) c.SchemaStorageMapping {
	t.Helper()
	w := f.request("GET", "/data/schemas/"+d.ID+"/storage-mapping", nil, false)
	if w.Code != 200 {
		t.Fatalf("read mapping failed: %d %s", w.Code, w.Body)
	}
	var result c.SchemaStorageMapping
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"host":`, `"username":`, `"password":`, `"passwordSet":`, `"values":`} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("mapping exposes connection or instance data: %s", forbidden)
		}
	}
	if result.SchemaID != d.ID || result.DefinitionETag != digest(d) || len(result.Properties) != len(d.Schema.Properties) {
		t.Fatalf("mapping identity does not match the active definition: %+v", result)
	}
	return result
}

func TestStorageMappingDefaultCatalogAndIsolation(t *testing.T) {
	f := newMappedIntegrationFixture(t, "mysql", os.Getenv("MYSQL_TEST_DSN"))
	d := testDefinition()
	d.ID, d.TenantID = c.NewID(), f.tenant
	f.dropOnCleanup(0, tableName(d.ID))
	operation := c.NewID()
	if response := f.prepare(d, "", operation); response.Code != 200 {
		t.Fatalf("prepare default schema: %d %s", response.Code, response.Body)
	}
	f.activate(d, operation)
	result := readStorageMapping(t, f, d)
	if !result.Managed || result.Mode != "schema_2_db" || result.Primary.DataSourceID != "" || result.Primary.DataSourceName != "平台默认存储" || result.Primary.Driver != "mysql" || result.Primary.Namespace != f.sources[0].Database || result.Primary.Table != tableName(d.ID) || result.Primary.KeyColumn != "id" || result.Primary.TenantColumn != "tenant_id" {
		t.Fatalf("default database mapping was not resolved correctly: %+v", result.Primary)
	}
	for index, property := range result.Properties {
		if property.PropertyID != d.Schema.Properties[index].ID || property.PropertyName != d.Schema.Properties[index].Name || property.Column != columnName(property.PropertyID) || property.Table != result.Primary.Table || property.DatabaseType == "" || !property.Exists {
			t.Fatalf("actual default property mapping missing: %+v", property)
		}
	}
	if result.Properties[1].DatabaseType != "decimal(18,3)" {
		t.Fatalf("catalog type was guessed instead of observed: %+v", result.Properties[1])
	}
	// A vanished physical column must be reported as absent rather than being
	// presented as an existing, verified mapping derived from stable IDs.
	f.exec(0, "ALTER TABLE "+f.qualified(0, tableName(d.ID))+" DROP COLUMN "+quote(columnName(testFieldPrice)))
	result = readStorageMapping(t, f, d)
	if result.Properties[1].Exists || result.Properties[1].DatabaseType != "" {
		t.Fatal("missing physical column was reported as existing")
	}
	path := "/data/schemas/" + d.ID + "/storage-mapping"
	if _, err := f.engine.DB().ExecContext(f.ctx, "UPDATE af_data_gates SET status='paused' WHERE tenant_id=? AND definition_id=?", f.tenant, d.ID); err != nil {
		t.Fatal(err)
	}
	if response := f.request("GET", path, nil, false); response.Code != 409 {
		t.Fatalf("paused mapping exposed: %d %s", response.Code, response.Body)
	}
	if _, err := f.engine.DB().ExecContext(f.ctx, "UPDATE af_data_gates SET status='ready' WHERE tenant_id=? AND definition_id=?", f.tenant, d.ID); err != nil {
		t.Fatal(err)
	}
	// A source administrator without business permission still cannot read a
	// schema catalog. The fixture is an administrator on every request.
	f.authorize = func(c.AuthorizationRequest) c.Decision { return c.Decision{State: "denied"} }
	if response := f.request("GET", path, nil, false); response.Code != 403 {
		t.Fatalf("administrator bypassed definition authorization: %d", response.Code)
	}
	checks := 0
	f.authorize = func(c.AuthorizationRequest) c.Decision {
		checks++
		decision := f.decision
		if checks > 1 {
			decision.State = "changed"
		}
		return decision
	}
	if response := f.request("GET", path, nil, false); response.Code != 409 {
		t.Fatalf("changed authorization returned mapping: %d %s", response.Code, response.Body)
	}
	f.authorize = nil
	f.mu.Lock()
	current := f.current[d.ID]
	current.Definition.TenantID = "other-tenant"
	f.current[d.ID] = current
	f.mu.Unlock()
	if response := f.request("GET", path, nil, false); response.Code != http.StatusNotFound {
		t.Fatalf("other tenant definition mapping exposed: %d %s", response.Code, response.Body)
	}
	f.mu.Lock()
	current.Definition.TenantID, current.Status = f.tenant, "draft"
	f.current[d.ID] = current
	f.mu.Unlock()
	if response := f.request("GET", path, nil, false); response.Code != 409 {
		t.Fatalf("draft exposed as active mapping: %d %s", response.Code, response.Body)
	}
}

func TestStorageMappingExternalCatalogRealDrivers(t *testing.T) {
	for _, backend := range []struct{ driver, env string }{{"mysql", "MYSQL_TEST_DSN"}, {"postgresql", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			f := newMappedIntegrationFixture(t, backend.driver, sourceIntegrationDSN(backend.driver, backend.env))
			suffix := strings.ReplaceAll(c.NewID(), "-", "")[:12]
			primary, secondary := "mapping_view_primary_"+suffix, "mapping_view_secondary_"+suffix
			f.createTable(0, primary, "tenant VARCHAR(128) NOT NULL,id BIGINT NOT NULL,code VARCHAR(255) NOT NULL,department VARCHAR(255) NOT NULL,PRIMARY KEY(tenant,id)")
			f.createTable(1, secondary, "tenant VARCHAR(128) NOT NULL,equipment_id BIGINT NOT NULL,amount DECIMAL(20,4) NOT NULL,PRIMARY KEY(tenant,equipment_id)")
			d := f.definition(primary, secondary, "db_2_schema")
			operation := c.NewID()
			if response := f.prepare(d, "", operation); response.Code != 200 {
				t.Fatalf("prepare external schema: %d %s", response.Code, response.Body)
			}
			f.activate(d, operation)
			before := []string{}
			for index, name := range []string{primary, secondary} {
				catalog, err := readSourceTable(f.ctx, f.dbs[index], f.sources[index], "", name)
				if err != nil {
					t.Fatal(err)
				}
				before = append(before, catalog.Signature)
			}
			result := readStorageMapping(t, f, d)
			if result.Managed || result.Primary.DataSourceID != f.sources[0].ID || result.Primary.Table != primary || result.Primary.KeyColumn != "id" || result.Primary.TenantColumn != "tenant" {
				t.Fatalf("external primary mapping mismatch: %+v", result.Primary)
			}
			for index, property := range result.Properties {
				mappedSource := 0
				table, key := primary, "id"
				if index == 2 {
					mappedSource, table, key = 1, secondary, "equipment_id"
				}
				if property.DataSourceID != f.sources[mappedSource].ID || property.Driver != f.sources[mappedSource].Driver || property.DataSourceName != f.sources[mappedSource].Name || property.Table != table || property.KeyColumn != key || property.Column != d.Schema.Properties[index].Mapping.Column || !property.Exists || property.DatabaseType == "" {
					t.Fatalf("cross-source property mapping incomplete: %+v", property)
				}
			}
			for index, name := range []string{primary, secondary} {
				catalog, err := readSourceTable(f.ctx, f.dbs[index], f.sources[index], "", name)
				if err != nil || catalog.Signature != before[index] {
					t.Fatal("reading mapping changed external table structure")
				}
			}
			column, _ := sourceQuote(backend.driver, "amount")
			f.exec(1, "ALTER TABLE "+f.qualified(1, secondary)+" DROP COLUMN "+column)
			result = readStorageMapping(t, f, d)
			if result.Properties[2].Exists || result.Properties[2].DatabaseType != "" {
				t.Fatal("missing external column was reported as existing")
			}
		})
	}
}
