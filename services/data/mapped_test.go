package data

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const mappedPrimarySource = "39cc89d0-4d68-4f79-8b28-bd37f562ed16"
const mappedSecondarySource = "2f995764-94d4-4c30-830c-8175ef27b527"

func mappedFixture() (c.Definition, []*mappedTable) {
	d := testDefinition()
	d.Schema.Mode = "db_2_schema"
	d.Schema.StorageBinding = &c.StorageBinding{DataSourceID: mappedPrimarySource, Namespace: "business", Table: "equipment", IDColumn: "entity_id", TenantColumn: "tenant"}
	d.Schema.Properties[0].Mapping = &c.PropertyMapping{DataSourceID: mappedPrimarySource, Column: "code"}
	d.Schema.Properties[1].Mapping = &c.PropertyMapping{DataSourceID: mappedSecondarySource, Namespace: "public", Table: "prices", Column: "amount", KeyColumn: "equipment_id", TenantColumn: "tenant"}
	d.Schema.Properties[2].Mapping = &c.PropertyMapping{DataSourceID: mappedPrimarySource, Column: "department"}
	primary := &mappedTable{source: c.DataSource{ID: mappedPrimarySource, TenantID: d.TenantID, Driver: "mysql", Database: "business", TenantIsolation: "column", ETag: "primary-etag"}, namespace: "business", name: "equipment", keyColumn: "entity_id", tenantColumn: "tenant", primary: true, exists: true, properties: []c.Property{d.Schema.Properties[0], d.Schema.Properties[2]}, catalog: c.SourceTable{
		Columns: []c.SourceColumn{{Name: "entity_id", DataType: "string", DatabaseType: "char(36)", MaxLength: 36}, {Name: "tenant", DataType: "string", MaxLength: 128}, {Name: "code", DataType: "string", MaxLength: 255}, {Name: "department", DataType: "string", MaxLength: 255}},
		Indexes: []c.SourceIndex{{Name: "PRIMARY", Primary: true, Unique: true, Columns: []string{"tenant", "entity_id"}}}, Signature: "primary-structure",
	}}
	secondary := &mappedTable{source: c.DataSource{ID: mappedSecondarySource, TenantID: d.TenantID, Driver: "postgresql", Database: "external", TenantIsolation: "column", ETag: "secondary-etag"}, namespace: "public", name: "prices", keyColumn: "equipment_id", tenantColumn: "tenant", exists: true, properties: []c.Property{d.Schema.Properties[1]}, catalog: c.SourceTable{
		Columns: []c.SourceColumn{{Name: "equipment_id", DataType: "uuid", DatabaseType: "uuid"}, {Name: "tenant", DataType: "string", MaxLength: 128}, {Name: "amount", DataType: "decimal", Precision: 18, Scale: 3}},
		Indexes: []c.SourceIndex{{Name: "price_key", Unique: true, Columns: []string{"tenant", "equipment_id"}}}, Signature: "secondary-structure",
	}}
	return d, []*mappedTable{primary, secondary}
}

func TestMappedQueryKeepsAuthorizationAndTenantInPrimarySQL(t *testing.T) {
	d, tables := mappedFixture()
	if err := validateMappedDefinition(d); err != nil {
		t.Fatal(err)
	}
	if err := validateMappedCatalog(tables); err != nil {
		t.Fatal(err)
	}
	decision := allowed()
	decision.ReadFields = []string{"*"}
	decision.RowFilter = &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("D1")}
	query := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Select: []string{testFieldPrice, testFieldCode}, Where: &c.Expression{Op: "startsWith", Field: testFieldCode, Value: raw("P_%' OR 1=1")}, Page: c.Page{Limit: 2}}
	compiled, err := compileMappedQuery(d, tables, "current", c.Principal{TenantID: d.TenantID, Subject: "reader"}, decision, query, strings.Repeat("s", 32), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled.SQL, "prices") || strings.Contains(compiled.SQL, "OR 1=1") || !strings.Contains(compiled.SQL, "BINARY `tenant` = BINARY ?") || !strings.Contains(compiled.SQL, "BINARY `department` = ?") {
		t.Fatalf("primary SQL leaked or lost constraints: %s", compiled.SQL)
	}
	if len(compiled.Args) != 4 || compiled.Args[0] != "P!_!%' OR 1=1%" || compiled.Args[1] != "D1" || compiled.Args[2] != d.TenantID || len(compiled.Projection) != 2 || len(compiled.ScanFields) != 1 {
		t.Fatalf("incorrect mapped projection/parameters: %#v", compiled)
	}
	for _, filter := range []*c.Expression{{Op: "eq", Field: testFieldPrice, Value: raw("1.000")}, {Op: "and", Args: []c.Expression{{Op: "eq", Field: testFieldCode, Value: raw("one")}, {Op: "eq", Field: testFieldPrice, Value: raw("1.000")}}}} {
		query.Where = filter
		if _, err = compileMappedQuery(d, tables, "current", c.Principal{TenantID: d.TenantID, Subject: "reader"}, decision, query, strings.Repeat("s", 32), ""); err == nil {
			t.Fatal("cross-source condition was silently weakened")
		}
	}
	query.Where = nil
	query.OrderBy = []c.Order{{Field: testFieldPrice, Direction: "asc"}}
	if _, err = compileMappedQuery(d, tables, "current", c.Principal{TenantID: d.TenantID, Subject: "reader"}, decision, query, strings.Repeat("s", 32), ""); err == nil {
		t.Fatal("cross-source ordering was accepted")
	}
	query.OrderBy = nil
	decision.RowFilter = &c.Expression{Op: "eq", Field: testFieldPrice, Value: raw("1.000")}
	if _, err = compileMappedQuery(d, tables, "current", c.Principal{TenantID: d.TenantID, Subject: "reader"}, decision, query, strings.Repeat("s", 32), ""); err == nil {
		t.Fatal("cross-source authorization filter was ignored")
	}
}

func TestMappedKeysRejectPartialNullableAndNonUniqueAssociations(t *testing.T) {
	for _, scenario := range []string{"non_unique", "partial", "nullable", "wrong_type", "tenant_missing", "unknown_column"} {
		t.Run(scenario, func(t *testing.T) {
			_, tables := mappedFixture()
			secondary := tables[1]
			switch scenario {
			case "non_unique":
				secondary.catalog.Indexes[0].Unique = false
			case "partial":
				secondary.catalog.Indexes[0].Predicate = "amount IS NOT NULL"
			case "nullable":
				secondary.catalog.Columns[0].Nullable = true
			case "wrong_type":
				secondary.catalog.Columns[0].DataType = "int64"
			case "tenant_missing":
				secondary.tenantColumn = "missing_tenant"
			case "unknown_column":
				secondary.properties[0].Mapping.Column = "missing_amount"
			}
			if err := validateMappedCatalog(tables); err == nil {
				t.Fatal("unsafe association accepted")
			}
		})
	}
}

func TestMappedPostgreSQLBindingsAndIntegerCursor(t *testing.T) {
	d, tables := mappedFixture()
	primary := tables[0]
	primary.source.Driver, primary.namespace = "postgresql", "public"
	primary.catalog.Columns[0].DataType, primary.catalog.Columns[0].DatabaseType = "int64", "bigint"
	query := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Select: []string{testFieldCode}, Page: c.Page{Limit: 1}}
	decision := allowed()
	principal := c.Principal{TenantID: d.TenantID, Subject: "reader"}
	secret := strings.Repeat("s", 32)
	compiled, err := compileMappedQuery(d, tables[:1], "current", principal, decision, query, secret, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled.SQL, "?") || !strings.Contains(compiled.SQL, `convert_to(CAST("tenant" AS text),'UTF8') = convert_to(CAST($1 AS text),'UTF8')`) {
		t.Fatalf("PostgreSQL placeholders not rebound: %s", compiled.SQL)
	}
	token, err := signCursor(secret, pageCursor{TenantID: d.TenantID, Subject: principal.Subject, SchemaID: d.ID, Digest: "current", Authorization: digest(decision), Fingerprint: compiled.Fingerprint, LastID: "9007199254740993", Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	query.Page.Cursor = token
	continued, err := compileMappedQuery(d, tables[:1], "current", principal, decision, query, secret, "")
	if err != nil {
		t.Fatal(err)
	}
	if continued.Args[1] != int64(9007199254740993) {
		t.Fatalf("integer cursor lost precision: %#v", continued.Args)
	}
	if _, err = parseCursor(secret, token); err == nil {
		t.Fatal("generic mapped key weakened legacy UUID cursor validation")
	}
	primary.catalog.Signature = "changed"
	if _, err = compileMappedQuery(d, tables[:1], "current", principal, decision, query, secret, ""); err == nil {
		t.Fatal("cursor survived source structure drift")
	}
	if bound := sourceBind("postgresql", `SELECT "a?b",'literal ? and ''quoted ?''' FROM "t" WHERE "x"=? AND "y"=?`); bound != `SELECT "a?b",'literal ? and ''quoted ?''' FROM "t" WHERE "x"=$1 AND "y"=$2` {
		t.Fatalf("quoted catalog/literals altered: %s", bound)
	}
}

func TestMappedMigrationAllowsOnlyAppendWithStablePhysicalBinding(t *testing.T) {
	d, _ := mappedFixture()
	d.Schema.Mode = "schema_2_db"
	rawDefinition, _ := json.Marshal(d)
	var next c.Definition
	_ = json.Unmarshal(rawDefinition, &next)
	next.Schema.Properties = append(next.Schema.Properties, c.Property{ID: c.NewID(), Name: "说明", DataType: "string", Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: mappedPrimarySource, Column: "description"}})
	if err := mappedCompatibleUpdate(d, next); err != nil {
		t.Fatal(err)
	}
	next.Schema.Properties[3].Nullable = false
	if err := mappedCompatibleUpdate(d, next); err == nil {
		t.Fatal("non-null addition skipped required backfill")
	}
	next.Schema.Properties[3].Nullable = true
	next.Schema.Properties[0].Mapping.Column = "other_code"
	if err := mappedCompatibleUpdate(d, next); err == nil {
		t.Fatal("existing property rebound to another physical column")
	}
}
