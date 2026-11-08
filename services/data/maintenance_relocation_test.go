package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func testRelocationItem() c.StorageRelocation {
	before := testDefinition()
	before.ID, before.TenantID = c.NewID(), "relocation-unit"
	raw, _ := json.Marshal(before)
	var after c.Definition
	_ = json.Unmarshal(raw, &after)
	source := c.NewID()
	after.Schema.StorageBinding = &c.StorageBinding{DataSourceID: source, Namespace: "knowledge", Table: "notes", IDColumn: "id"}
	for i := range after.Schema.Properties {
		after.Schema.Properties[i].Mapping = &c.PropertyMapping{DataSourceID: source, Namespace: "knowledge", Table: "notes", Column: columnName(after.Schema.Properties[i].ID)}
	}
	return c.StorageRelocation{OperationID: c.NewID(), SourceETag: "configured-source", Previous: c.CurrentDefinition{Definition: before, ETag: digest(before), Status: "active"}, Definition: after, Digest: digest(after)}
}

func TestRelocationManifestRejectsLogicalChangesAndUnboundedScope(t *testing.T) {
	item := testRelocationItem()
	if err := validateRelocationManifest([]c.StorageRelocation{item}); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name string
		edit func(*c.StorageRelocation)
	}{
		{"digest", func(item *c.StorageRelocation) { item.Digest = "wrong" }},
		{"source revision", func(item *c.StorageRelocation) { item.SourceETag = "" }},
		{"rename", func(item *c.StorageRelocation) { item.Definition.Name = "other"; item.Digest = digest(item.Definition) }},
		{"direction", func(item *c.StorageRelocation) {
			item.Definition.Schema.Mode = "db_2_schema"
			item.Digest = digest(item.Definition)
		}},
		{"property id", func(item *c.StorageRelocation) {
			item.Definition.Schema.Properties[0].ID = c.NewID()
			item.Digest = digest(item.Definition)
		}},
		{"cross table", func(item *c.StorageRelocation) {
			item.Definition.Schema.Properties[0].Mapping.Table = "other"
			item.Digest = digest(item.Definition)
		}},
		{"tenant column", func(item *c.StorageRelocation) {
			item.Definition.Schema.StorageBinding.TenantColumn = "tenant"
			item.Digest = digest(item.Definition)
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			raw, _ := json.Marshal(item)
			var invalid c.StorageRelocation
			_ = json.Unmarshal(raw, &invalid)
			scenario.edit(&invalid)
			if validateRelocationManifest([]c.StorageRelocation{invalid}) == nil {
				t.Fatal("invalid relocation manifest was accepted")
			}
		})
	}
	if validateRelocationManifest(nil) == nil || validateRelocationManifest([]c.StorageRelocation{item, item}) == nil || validateRelocationManifest(make([]c.StorageRelocation, 17)) == nil {
		t.Fatal("empty, duplicate or oversized relocation was accepted")
	}
}

type relocationFixture struct {
	t      *testing.T
	ctx    context.Context
	cfg    Config
	source c.DataSource
	tables []string
}

func newRelocationFixture(t *testing.T) *relocationFixture {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not configured; real relocation verification not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 6, MaxIdle: 3})
	if err != nil {
		t.Fatal("cannot connect isolated relocation test store")
	}
	if err = storage.Migrate(ctx, engine.DB(), "data", Migrations); err != nil {
		engine.Close()
		t.Fatal(err)
	}
	input := mappedTestSourceInput(t, "mysql", dsn, "迁移隔离源")
	source := c.DataSource{ID: c.NewID(), TenantID: "relocation-" + c.NewID(), Name: input.Name, Driver: input.Driver, Host: input.Host, Port: input.Port, Database: input.Database, Username: input.Username, TLSMode: input.TLSMode, Enabled: true, PasswordSet: true, TenantIsolation: "dedicated", ETag: digest(c.NewID())}
	f := &relocationFixture{t: t, ctx: ctx, cfg: Config{SourceNetworkPolicy: testSourceNetworkPolicy(t), Engine: engine, DataSourceKey: strings.Repeat("relocation-test-key", 3)}, source: source}
	t.Cleanup(func() {
		for _, table := range f.tables {
			if _, err := engine.DB().Exec("DROP TABLE IF EXISTS " + quote(table)); err != nil {
				t.Errorf("cannot remove isolated relocation table: %v", err)
			}
		}
		for _, table := range []string{"af_data_mapped_steps", "af_data_mapped_targets", "af_data_gates", "af_data_sources"} {
			if _, err := engine.DB().Exec("DELETE FROM "+quote(table)+" WHERE tenant_id=?", source.TenantID); err != nil {
				t.Errorf("cannot remove isolated relocation control row: %v", err)
			}
		}
		engine.Close()
	})
	h := &handler{cfg: f.cfg}
	secret, err := h.encryptSourceSecret(source.TenantID, source.ID, *input.Password)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(source)
	if _, err = engine.DB().ExecContext(ctx, "INSERT INTO af_data_sources (tenant_id,id,name,name_key,config_json,secret_cipher,endpoint_key,tenant_isolation,etag) VALUES (?,?,?,?,?,?,?,?,?)", source.TenantID, source.ID, source.Name, []byte(source.Name), string(raw), secret, endpointKey(source), source.TenantIsolation, source.ETag); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *relocationFixture) item() c.StorageRelocation {
	f.t.Helper()
	before := c.Definition{ID: c.NewID(), TenantID: f.source.TenantID, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: "知识正文", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{
		{ID: c.NewID(), Name: "来源键", DataType: "string"},
		{ID: c.NewID(), Name: "原始正文", DataType: "longtext"},
		{ID: c.NewID(), Name: "可空文本", DataType: "text", Nullable: true},
		{ID: c.NewID(), Name: "整数", DataType: "int64"},
		{ID: c.NewID(), Name: "精确数值", DataType: "decimal", Precision: 38, Scale: 8},
		{ID: c.NewID(), Name: "可用", DataType: "boolean"},
		{ID: c.NewID(), Name: "时间", DataType: "timestamp", Nullable: true},
	}}}
	before.Schema.Indexes = []c.Index{{ID: c.NewID(), Name: "来源键唯一", Unique: true, FieldIDs: []string{before.Schema.Properties[0].ID}}}
	statement, err := ddl(before)
	if err != nil {
		f.t.Fatal(err)
	}
	f.tables = append(f.tables, tableName(before.ID))
	if _, err = f.cfg.Engine.DB().ExecContext(f.ctx, statement); err != nil {
		f.t.Fatal(err)
	}
	columns := []string{"tenant_id", "id", "etag"}
	for _, property := range before.Schema.Properties {
		columns = append(columns, columnName(property.ID))
	}
	for i := 0; i < 2; i++ {
		quoted := []string{}
		for _, column := range columns {
			quoted = append(quoted, quote(column))
		}
		var nullableText, instant any
		if i == 1 {
			nullableText = ""
			instant = time.Date(2026, 11, 7, 12, 13, 14, 123456000, time.UTC)
		}
		args := []any{before.TenantID, c.NewID(), c.NewID(), c.NewID(), "\ufeff---\r\n" + strings.Repeat("知识正文\n\r\n", 2048), nullableText, "9223372036854775807", "9007199254740993.12345678", i == 1, instant}
		if _, err = f.cfg.Engine.DB().ExecContext(f.ctx, "INSERT INTO "+quote(tableName(before.ID))+" ("+strings.Join(quoted, ",")+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")+")", args...); err != nil {
			f.t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(before)
	if _, err = f.cfg.Engine.DB().ExecContext(f.ctx, "INSERT INTO af_data_gates (tenant_id,definition_id,digest,previous_digest,operation_id,status,definition,storage_signature) VALUES (?,?,?,'',?,'ready',?,?)", before.TenantID, before.ID, digest(before), c.NewID(), string(raw), storageSignature(before)); err != nil {
		f.t.Fatal(err)
	}
	var after c.Definition
	_ = json.Unmarshal(raw, &after)
	target := "relocate_" + strings.ReplaceAll(c.NewID(), "-", "")
	f.tables = append(f.tables, target)
	after.Schema.StorageBinding = &c.StorageBinding{DataSourceID: f.source.ID, Namespace: f.source.Database, Table: target, IDColumn: "id"}
	for i := range after.Schema.Properties {
		after.Schema.Properties[i].Mapping = &c.PropertyMapping{DataSourceID: f.source.ID, Namespace: f.source.Database, Table: target, Column: columnName(after.Schema.Properties[i].ID)}
	}
	return c.StorageRelocation{OperationID: c.NewID(), SourceETag: f.source.ETag, Previous: c.CurrentDefinition{Definition: before, Status: "active", ETag: digest(before)}, Definition: after, Digest: digest(after)}
}

func (f *relocationFixture) status(item c.StorageRelocation) string {
	f.t.Helper()
	var status string
	if err := f.cfg.Engine.DB().QueryRowContext(f.ctx, "SELECT status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", item.Definition.TenantID, item.Definition.ID).Scan(&status); err != nil {
		f.t.Fatal(err)
	}
	return status
}

func TestMySQLDefaultStorageRelocationPreservesEveryValueAndResumes(t *testing.T) {
	f := newRelocationFixture(t)
	items := []c.StorageRelocation{f.item(), f.item()}
	plan, err := PlanDefaultStorageRelocation(f.ctx, f.cfg, items)
	if err != nil || len(plan) != 2 || plan[0].Rows != 2 || len(plan[0].Statements) != 2 {
		t.Fatalf("invalid relocation plan: %+v %v", plan, err)
	}
	var targetCount, claimCount int
	if err = f.cfg.Engine.DB().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", items[0].Definition.Schema.StorageBinding.Table).Scan(&targetCount); err != nil {
		t.Fatal(err)
	}
	if err = f.cfg.Engine.DB().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM af_data_mapped_targets WHERE tenant_id=?", f.source.TenantID).Scan(&claimCount); err != nil {
		t.Fatal(err)
	}
	if targetCount != 0 || claimCount != 0 || f.status(items[0]) != "ready" || f.status(items[1]) != "ready" {
		t.Fatal("plan mutated target, ownership or admission")
	}
	prepared, err := PrepareDefaultStorageRelocation(f.ctx, f.cfg, items)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range items {
		if prepared[i].ContentDigest != plan[i].ContentDigest || f.status(item) != "prepared" {
			t.Fatal("relocation lost source values or opened an unactivated gate")
		}
		rows, err := readDefaultRelocationRows(f.ctx, f.cfg.Engine.DB(), item.Previous.Definition)
		if err != nil || digest(rows) != plan[i].ContentDigest {
			t.Fatal("relocation changed its original source")
		}
	}
	if _, err = PrepareDefaultStorageRelocation(f.ctx, f.cfg, items); err != nil {
		t.Fatalf("identical preparation did not resume: %v", err)
	}
	if err = ActivateDefaultStorageRelocation(f.ctx, f.cfg, items[:1]); err == nil || f.status(items[0]) != "prepared" || f.status(items[1]) != "prepared" {
		t.Fatal("a shortened manifest activated part of the prepared batch")
	}
	if _, err = PrepareDefaultStorageRelocation(f.ctx, f.cfg, items[:1]); err == nil {
		t.Fatal("a shortened preparation manifest was accepted")
	}
	// A partially prepared batch must not activate even its completed members.
	if _, err = f.cfg.Engine.DB().ExecContext(f.ctx, "UPDATE af_data_gates SET status='preparing' WHERE tenant_id=? AND definition_id=?", f.source.TenantID, items[1].Definition.ID); err != nil {
		t.Fatal(err)
	}
	if ActivateDefaultStorageRelocation(f.ctx, f.cfg, items) == nil || f.status(items[0]) != "prepared" {
		t.Fatal("activation opened part of an incomplete batch")
	}
	if _, err = PrepareDefaultStorageRelocation(f.ctx, f.cfg, items); err != nil {
		t.Fatal(err)
	}
	if err = ActivateDefaultStorageRelocation(f.ctx, f.cfg, items); err != nil {
		t.Fatal(err)
	}
	if f.status(items[0]) != "ready" || f.status(items[1]) != "ready" {
		t.Fatal("activation did not open the entire prepared batch")
	}
	if _, err = PrepareDefaultStorageRelocation(f.ctx, f.cfg, items); err != nil {
		t.Fatalf("completed relocation was not idempotent: %v", err)
	}
}

func TestMySQLDefaultStorageRelocationRefusesAdoptionDriftAndManagedLinks(t *testing.T) {
	f := newRelocationFixture(t)
	item := f.item()
	items := []c.StorageRelocation{item}
	if _, err := f.cfg.Engine.DB().ExecContext(f.ctx, "UPDATE af_data_sources SET etag=? WHERE tenant_id=? AND id=?", "changed", f.source.TenantID, f.source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanDefaultStorageRelocation(f.ctx, f.cfg, items); err == nil || f.status(item) != "ready" {
		t.Fatal("source credential/config drift was accepted or paused the source")
	}
	if _, err := f.cfg.Engine.DB().ExecContext(f.ctx, "UPDATE af_data_sources SET etag=? WHERE tenant_id=? AND id=?", f.source.ETag, f.source.TenantID, f.source.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanDefaultStorageRelocation(f.ctx, f.cfg, items)
	if err != nil {
		t.Fatal(err)
	}
	// Even a table with a forged correct owner marker must not be adopted before
	// this exact manifest has reserved it in Data's durable control catalog.
	for _, statement := range plan[0].Statements {
		if _, err := f.cfg.Engine.DB().ExecContext(f.ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PrepareDefaultStorageRelocation(f.ctx, f.cfg, items); err == nil || f.status(item) != "ready" {
		t.Fatal("an existing unrelated target was adopted")
	}
	if _, err := f.cfg.Engine.DB().ExecContext(f.ctx, "DROP TABLE "+quote(item.Definition.Schema.StorageBinding.Table)); err != nil {
		t.Fatal(err)
	}
	link := c.Definition{ID: c.NewID(), TenantID: f.source.TenantID, Kind: c.KindLink, Link: &c.Link{SourceSchemaID: item.Definition.ID, TargetSchemaID: c.NewID(), StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	raw, _ := json.Marshal(link)
	if _, err := f.cfg.Engine.DB().ExecContext(f.ctx, "INSERT INTO af_data_gates (tenant_id,definition_id,digest,previous_digest,operation_id,status,definition,storage_signature) VALUES (?,?,?,'',?,'ready',?,?)", link.TenantID, link.ID, digest(link), c.NewID(), string(raw), digest(link)); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDefaultStorageRelocation(f.ctx, f.cfg, items); err == nil || f.status(item) != "ready" {
		t.Fatal("an endpoint with a default managed Link was relocated")
	}
}

func TestMySQLDefaultStorageRelocationInterruptedPreparationNeverOverwrites(t *testing.T) {
	f := newRelocationFixture(t)
	item := f.item()
	plans, err := planDefaultRelocations(f.ctx, f.cfg, []c.StorageRelocation{item})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRelocationPlans(plans)
	if err = closeRelocationGates(f.ctx, f.cfg, plans); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after target DDL but before copying any rows.
	for _, statement := range plans[0].statements {
		if _, err = f.cfg.Engine.DB().ExecContext(f.ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = PrepareDefaultStorageRelocation(f.ctx, f.cfg, []c.StorageRelocation{item}); err != nil {
		t.Fatalf("interrupted target creation could not resume: %v", err)
	}
	property := item.Definition.Schema.Properties[1]
	if _, err = f.cfg.Engine.DB().ExecContext(f.ctx, "UPDATE "+quote(item.Definition.Schema.StorageBinding.Table)+" SET "+quote(property.Mapping.Column)+"=? LIMIT 1", "independent target change"); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareDefaultStorageRelocation(f.ctx, f.cfg, []c.StorageRelocation{item}); err == nil || f.status(item) != "prepared" {
		t.Fatal("target drift was overwritten or an unverified target was activated")
	}
	if err = ActivateDefaultStorageRelocation(f.ctx, f.cfg, []c.StorageRelocation{item}); err == nil || f.status(item) != "prepared" {
		t.Fatal("activation accepted changed target contents")
	}
	var actual string
	if err = f.cfg.Engine.DB().QueryRowContext(f.ctx, "SELECT "+quote(property.Mapping.Column)+" FROM "+quote(item.Definition.Schema.StorageBinding.Table)+" WHERE "+quote(property.Mapping.Column)+"=?", "independent target change").Scan(&actual); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if actual != "independent target change" {
		t.Fatal("target drift was silently repaired or erased")
	}
}
