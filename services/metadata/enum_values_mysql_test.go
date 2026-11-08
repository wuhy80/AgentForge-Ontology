package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func TestMySQLEnumDefaultsDependentsAndDetachedMetadata(t *testing.T) {
	dsn := os.Getenv("METADATA_TEST_DSN")
	if dsn == "" {
		t.Skip("set METADATA_TEST_DSN for isolated enum dependency validation")
	}
	ctx := context.Background()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := storage.Migrate(ctx, engine.DB(), "metadata", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant := "enum-metadata-" + c.NewID()
	defer func() {
		for _, table := range []string{"af_meta_dependencies", "af_meta_definitions"} {
			if _, err := engine.DB().Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant); err != nil {
				t.Error(err)
			}
		}
	}()
	enum := c.Definition{ID: c.NewID(), TenantID: tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindEnum, Name: "状态", Enum: &c.Enum{Values: []c.EnumValue{{Code: "A", Label: "可用"}, {Code: "B", Label: "历史", Deprecated: true}}}}
	schema := c.Definition{ID: c.NewID(), TenantID: tenant, CategoryID: enum.CategoryID, GroupID: enum.GroupID, Kind: c.KindSchema, Name: "默认状态对象", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{ID: c.NewID(), Name: "状态", DataType: "string", EnumID: enum.ID, Default: json.RawMessage(`"A"`)}}}}
	for _, definition := range []c.Definition{enum, schema} {
		encoded, _ := json.Marshal(definition)
		if _, err := engine.DB().Exec("INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,current_json,current_digest,status) VALUES (?,?,?,?,?,?,?,?,?,'active')", definition.ID, tenant, definition.CategoryID, definition.GroupID, definition.Kind, definition.Name, definition.Name, string(encoded), digestDefinition(definition)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := engine.DB().Exec("INSERT INTO af_meta_dependencies (tenant_id,source_id,target_id) VALUES (?,?,?)", tenant, schema.ID, enum.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := engine.DB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolved, err := resolveEnumReferences(ctx, tx, schema)
	if err != nil || len(resolved.Schema.Properties[0].EnumValues) != 2 || resolved.Schema.Properties[0].EnumETag != digestDefinition(enum) {
		t.Fatalf("current enum metadata not resolved: %v", err)
	}
	if schema.Schema.Properties[0].EnumValues != nil || schema.Schema.Properties[0].EnumETag != "" {
		t.Fatal("response-only options contaminated the saved definition")
	}
	var persisted string
	if err := tx.QueryRowContext(ctx, "SELECT current_json FROM af_meta_definitions WHERE tenant_id=? AND id=?", tenant, schema.ID).Scan(&persisted); err != nil || strings.Contains(persisted, "enumValues") || strings.Contains(persisted, "enumEtag") {
		t.Fatal("derived metadata persisted")
	}
	if err := validateEnumReferences(ctx, tx, schema); err != nil {
		t.Fatal("valid current default rejected", err)
	}
	schema.Schema.Properties[0].Default = json.RawMessage(`"B"`)
	if err := validateEnumReferences(ctx, tx, schema); err == nil {
		t.Fatal("deprecated default accepted")
	}
	schema.TenantID = "another-tenant"
	if err := validateEnumReferences(ctx, tx, schema); err == nil {
		t.Fatal("cross-tenant enum reference accepted")
	}
	enum.Enum.Values[0].Label = "已改标签"
	if err := validateEnumDependents(ctx, tx, enum); err != nil {
		t.Fatal("label update broke stable defaults", err)
	}
	enum.Enum.Values[0].Deprecated = true
	var failure *apiError
	if err := validateEnumDependents(ctx, tx, enum); !errors.As(err, &failure) || failure.code != "enum_default_in_use" {
		t.Fatalf("active default did not protect code: %v", err)
	}
}
