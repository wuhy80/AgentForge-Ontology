package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func relocationTestManifest(count int) []c.StorageRelocation {
	tenant, category, group, source := "relocation-test-"+c.NewID(), c.NewID(), c.NewID(), c.NewID()
	items := []c.StorageRelocation{}
	for n := 0; n < count; n++ {
		old := c.Definition{ID: c.NewID(), TenantID: tenant, CategoryID: category, GroupID: group, Kind: c.KindSchema, Name: fmt.Sprintf("迁移对象%d", n+1), Description: "保持业务定义及稳定身份", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{
			{ID: c.NewID(), Name: "来源键", DataType: "string", Required: true},
			{ID: c.NewID(), Name: "正文", DataType: "longtext", Nullable: true},
			{ID: c.NewID(), Name: "金额", DataType: "decimal", Precision: 24, Scale: 6, Nullable: true},
		}}}
		old.Schema.Indexes = []c.Index{{ID: c.NewID(), Name: "来源键唯一索引", Unique: true, FieldIDs: []string{old.Schema.Properties[0].ID}}}
		next := copyMapped(old)
		next.Schema.StorageBinding = &c.StorageBinding{DataSourceID: source, Namespace: "knowledge", Table: fmt.Sprintf("kb_entities_%d", n), IDColumn: "id"}
		for i := range next.Schema.Properties {
			next.Schema.Properties[i].Mapping = &c.PropertyMapping{DataSourceID: source, Namespace: "knowledge", Table: next.Schema.StorageBinding.Table, Column: fmt.Sprintf("field_%d", i), KeyColumn: "id"}
		}
		items = append(items, c.StorageRelocation{OperationID: c.NewID(), SourceETag: strings.Repeat("a", 64), Previous: c.CurrentDefinition{Definition: old, ETag: digestDefinition(old), Status: "active"}, Definition: next, Digest: digestDefinition(next)})
	}
	return items
}

func cloneRelocationItems(items []c.StorageRelocation) []c.StorageRelocation {
	raw, _ := json.Marshal(items)
	var out []c.StorageRelocation
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestDefaultStorageRelocationManifestKeepsLogicalIdentity(t *testing.T) {
	items := relocationTestManifest(2)
	before := cloneRelocationItems(items)
	if _, err := canonicalStorageRelocation(items); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(items, before) {
		t.Fatal("reviewed manifest was mutated")
	}
	if compatibleUpdate(items[0].Previous.Definition, items[0].Definition) == nil {
		t.Fatal("ordinary publication unexpectedly allowed a storage rebind")
	}
	if _, err := canonicalStorageRelocation(nil); err == nil {
		t.Fatal("empty batch accepted")
	}
	if _, err := canonicalStorageRelocation(relocationTestManifest(17)); err == nil {
		t.Fatal("batch beyond sixteen models accepted")
	}
	mutations := map[string]func([]c.StorageRelocation){
		"changed name":      func(v []c.StorageRelocation) { v[0].Definition.Name = "其他名称" },
		"changed stable ID": func(v []c.StorageRelocation) { v[0].Definition.ID = c.NewID() },
		"changed property":  func(v []c.StorageRelocation) { v[0].Definition.Schema.Properties[0].Name = "变更来源键" },
		"changed type":      func(v []c.StorageRelocation) { v[0].Definition.Schema.Properties[0].DataType = "uuid" },
		"changed default":   func(v []c.StorageRelocation) { v[0].Definition.Schema.Properties[0].Default = json.RawMessage(`"new"`) },
		"changed index":     func(v []c.StorageRelocation) { v[0].Definition.Schema.Indexes[0].Unique = false },
		"changed group":     func(v []c.StorageRelocation) { v[0].Definition.GroupID = c.NewID() },
		"changed tenant":    func(v []c.StorageRelocation) { v[0].Definition.TenantID = "foreign" },
		"other category":    func(v []c.StorageRelocation) { v[0].Definition.CategoryID = c.NewID() },
		"reverse model":     func(v []c.StorageRelocation) { v[0].Definition.Schema.Mode = "db_2_schema" },
		"old mapped model": func(v []c.StorageRelocation) {
			v[0].Previous.Definition.Schema.StorageBinding = v[0].Definition.Schema.StorageBinding
		},
		"draft old model":    func(v []c.StorageRelocation) { v[0].Previous.Status = "draft" },
		"shared tenant":      func(v []c.StorageRelocation) { v[0].Definition.Schema.StorageBinding.TenantColumn = "tenant" },
		"different key":      func(v []c.StorageRelocation) { v[0].Definition.Schema.StorageBinding.IDColumn = "other_id" },
		"missing namespace":  func(v []c.StorageRelocation) { v[0].Definition.Schema.StorageBinding.Namespace = "" },
		"other field table":  func(v []c.StorageRelocation) { v[0].Definition.Schema.Properties[0].Mapping.Table = "other" },
		"other field source": func(v []c.StorageRelocation) { v[0].Definition.Schema.Properties[0].Mapping.DataSourceID = c.NewID() },
		"duplicate operation": func(v []c.StorageRelocation) {
			v[1].OperationID = v[0].OperationID
		},
		"duplicate target": func(v []c.StorageRelocation) {
			v[1].Definition.Schema.StorageBinding.Table = v[0].Definition.Schema.StorageBinding.Table
			for i := range v[1].Definition.Schema.Properties {
				v[1].Definition.Schema.Properties[i].Mapping.Table = v[0].Definition.Schema.StorageBinding.Table
			}
		},
		"source config changed": func(v []c.StorageRelocation) { v[1].SourceETag = strings.Repeat("b", 64) },
		"missing source tag":    func(v []c.StorageRelocation) { v[0].SourceETag = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := cloneRelocationItems(items)
			mutate(candidate)
			for i := range candidate {
				candidate[i].Digest = digestDefinition(candidate[i].Definition)
				candidate[i].Previous.ETag = digestDefinition(candidate[i].Previous.Definition)
			}
			if _, err := canonicalStorageRelocation(candidate); err == nil {
				t.Fatal("unsafe relocation manifest accepted")
			}
		})
	}
	for _, old := range []bool{false, true} {
		bad := cloneRelocationItems(items)
		if old {
			bad[0].Previous.ETag = strings.Repeat("0", 64)
		} else {
			bad[0].Digest = strings.Repeat("0", 64)
		}
		if _, err := canonicalStorageRelocation(bad); err == nil {
			t.Fatal("incorrect definition digest accepted")
		}
	}
}

func TestMySQLDefaultStorageRelocationAtomicityAndEvidence(t *testing.T) {
	dsn := os.Getenv("METADATA_TEST_DSN")
	if dsn == "" {
		t.Skip("set METADATA_TEST_DSN for isolated offline relocation verification")
	}
	ctx := context.Background()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal("open isolated Metadata database")
	}
	defer engine.Close()
	if err := storage.Migrate(ctx, engine.DB(), "metadata", Migrations); err != nil {
		t.Fatal(err)
	}
	items := relocationTestManifest(2)
	tenant, category, group := items[0].Definition.TenantID, items[0].Definition.CategoryID, items[0].Definition.GroupID
	defer func() {
		for _, table := range []string{"af_meta_dependencies", "af_meta_outbox", "af_meta_publications", "af_meta_definitions", "af_meta_organizations", "af_meta_tenant_guards"} {
			if _, err := engine.DB().ExecContext(ctx, "DELETE FROM "+table+" WHERE tenant_id=?", tenant); err != nil {
				t.Errorf("clean isolated relocation records: %v", err)
			}
		}
	}()
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := engine.DB().ExecContext(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO af_meta_tenant_guards (tenant_id) VALUES (?)", tenant)
	exec("INSERT INTO af_meta_organizations (id,tenant_id,kind,parent_id,name,name_key) VALUES (?,?,'category','','迁移分类',?)", category, tenant, []byte("迁移分类"))
	exec("INSERT INTO af_meta_organizations (id,tenant_id,kind,parent_id,name,name_key) VALUES (?,?,'group',?,'迁移分组',?)", group, tenant, category, []byte("迁移分组"))
	seed := func(d c.Definition) {
		t.Helper()
		raw, _ := json.Marshal(d)
		_, name, _ := normalizeName(d.Name)
		exec("INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,current_json,current_digest,status) VALUES (?,?,?,?,?,?,?,?,?,'active')", d.ID, d.TenantID, d.CategoryID, d.GroupID, d.Kind, d.Name, []byte(name), string(raw), digestDefinition(d))
	}
	for _, item := range items {
		seed(item.Previous.Definition)
	}
	assertOld := func() {
		t.Helper()
		for _, item := range items {
			var actual string
			if err := engine.DB().QueryRowContext(ctx, "SELECT current_digest FROM af_meta_definitions WHERE tenant_id=? AND id=?", tenant, item.Definition.ID).Scan(&actual); err != nil || actual != item.Previous.ETag {
				t.Fatalf("failed relocation changed an earlier batch item: digest=%s err=%v", actual, err)
			}
		}
		for _, table := range []string{"af_meta_publications", "af_meta_outbox"} {
			var count int
			if err := engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE tenant_id=?", tenant).Scan(&count); err != nil || count != 0 {
				t.Fatalf("validation wrote %s: count=%d err=%v", table, count, err)
			}
		}
	}
	if err := ValidateDefaultStorageRelocation(ctx, engine, items); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDefaultStorageRelocation(ctx, engine, items); err == nil {
		t.Fatal("verification accepted uncommitted models")
	}
	assertOld()
	for _, status := range []string{"preparing", "prepare_failed", "committed", "activation_pending"} {
		exec("INSERT INTO af_meta_publications (id,tenant_id,definition_id,candidate_json,candidate_digest,previous_digest,status,error_text) VALUES (?,?,?,'{}','','',?,'')", c.NewID(), tenant, items[0].Definition.ID, status)
		if err := CommitDefaultStorageRelocation(ctx, engine, items); err == nil {
			t.Fatalf("unfinished %s publication accepted", status)
		}
		exec("DELETE FROM af_meta_publications WHERE tenant_id=?", tenant)
		assertOld()
	}
	exec("UPDATE af_meta_tenant_guards SET publication_id=? WHERE tenant_id=?", c.NewID(), tenant)
	if err := CommitDefaultStorageRelocation(ctx, engine, items); err == nil {
		t.Fatal("busy tenant guard accepted")
	}
	exec("UPDATE af_meta_tenant_guards SET publication_id='' WHERE tenant_id=?", tenant)
	exec("UPDATE af_meta_definitions SET draft_json=current_json,draft_digest=current_digest WHERE tenant_id=? AND id=?", tenant, items[1].Definition.ID)
	if err := CommitDefaultStorageRelocation(ctx, engine, items); err == nil {
		t.Fatal("existing draft accepted")
	}
	exec("UPDATE af_meta_definitions SET draft_json=NULL,draft_digest='' WHERE tenant_id=?", tenant)
	assertOld()
	link := c.Definition{ID: c.NewID(), TenantID: tenant, CategoryID: category, GroupID: group, Kind: c.KindLink, Name: "迁移关联", Link: &c.Link{SourceSchemaID: items[0].Definition.ID, TargetSchemaID: items[1].Definition.ID, ForwardName: "关联目标", ReverseName: "关联来源", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	seed(link)
	if err := CommitDefaultStorageRelocation(ctx, engine, items); err == nil {
		t.Fatal("already bound dependency accepted")
	}
	exec("DELETE FROM af_meta_definitions WHERE tenant_id=? AND id=?", tenant, link.ID)
	assertOld()
	stale := cloneRelocationItems(items)
	stale[1].Previous.Definition.Description = "过期快照"
	stale[1].Previous.ETag = digestDefinition(stale[1].Previous.Definition)
	stale[1].Definition.Description = "过期快照"
	stale[1].Digest = digestDefinition(stale[1].Definition)
	if err := CommitDefaultStorageRelocation(ctx, engine, stale); err == nil {
		t.Fatal("stale current snapshot accepted")
	}
	assertOld()
	// Cross-tenant Enum references cannot be introduced even by an old snapshot.
	foreign := cloneRelocationItems(items)
	foreign[0].Previous.Definition.Schema.Properties[0].EnumID = c.NewID()
	foreign[0].Definition.Schema.Properties[0].EnumID = foreign[0].Previous.Definition.Schema.Properties[0].EnumID
	foreign[0].Previous.ETag = digestDefinition(foreign[0].Previous.Definition)
	foreign[0].Digest = digestDefinition(foreign[0].Definition)
	raw, _ := json.Marshal(foreign[0].Previous.Definition)
	exec("UPDATE af_meta_definitions SET current_json=?,current_digest=? WHERE tenant_id=? AND id=?", string(raw), foreign[0].Previous.ETag, tenant, items[0].Definition.ID)
	if err := CommitDefaultStorageRelocation(ctx, engine, foreign); err == nil {
		t.Fatal("missing or foreign reference accepted")
	}
	raw, _ = json.Marshal(items[0].Previous.Definition)
	exec("UPDATE af_meta_definitions SET current_json=?,current_digest=? WHERE tenant_id=? AND id=?", string(raw), items[0].Previous.ETag, tenant, items[0].Definition.ID)
	assertOld()
	if err := CommitDefaultStorageRelocation(ctx, engine, items); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ValidateDefaultStorageRelocation(ctx, engine, items); err != nil {
			t.Fatal(err)
		}
		if err := CommitDefaultStorageRelocation(ctx, engine, items); err != nil {
			t.Fatal(err)
		}
		if err := VerifyDefaultStorageRelocation(ctx, engine, items); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"af_meta_publications", "af_meta_outbox"} {
		var count int
		if err := engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE tenant_id=?", tenant).Scan(&count); err != nil || count != len(items) {
			t.Fatalf("idempotent batch duplicated %s: count=%d err=%v", table, count, err)
		}
	}
	changedSource := cloneRelocationItems(items)
	for i := range changedSource {
		changedSource[i].SourceETag = strings.Repeat("b", 64)
	}
	if err := CommitDefaultStorageRelocation(ctx, engine, changedSource); err == nil {
		t.Fatal("changed source configuration accepted as the original manifest")
	}
	changedOperation := cloneRelocationItems(items)
	changedOperation[0].OperationID = c.NewID()
	if err := VerifyDefaultStorageRelocation(ctx, engine, changedOperation); err == nil {
		t.Fatal("changed operation identity accepted as the original manifest")
	}
	// A manually corrupted mixed state is rejected rather than completed by
	// updating only the old half; Metadata commits the entire batch atomically.
	first := items[0]
	raw, _ = json.Marshal(first.Previous.Definition)
	exec("UPDATE af_meta_definitions SET current_json=?,current_digest=? WHERE tenant_id=? AND id=?", string(raw), first.Previous.ETag, tenant, first.Definition.ID)
	exec("DELETE FROM af_meta_publications WHERE tenant_id=? AND id=?", tenant, first.OperationID)
	exec("DELETE FROM af_meta_outbox WHERE tenant_id=? AND id=?", tenant, first.OperationID)
	if err := CommitDefaultStorageRelocation(ctx, engine, items); err == nil {
		t.Fatal("mixed old and new batch was accepted")
	}
	var actual string
	if err := engine.DB().QueryRowContext(ctx, "SELECT current_digest FROM af_meta_definitions WHERE tenant_id=? AND id=?", tenant, first.Definition.ID).Scan(&actual); err != nil || actual != first.Previous.ETag {
		t.Fatal("mixed-state rejection still modified the old half")
	}
	raw, _ = json.Marshal(first.Definition)
	exec("UPDATE af_meta_definitions SET current_json=?,current_digest=? WHERE tenant_id=? AND id=?", string(raw), first.Digest, tenant, first.Definition.ID)
	exec("INSERT INTO af_meta_publications (id,tenant_id,definition_id,candidate_json,candidate_digest,previous_digest,status,error_text) VALUES (?,?,?,?,?,?,'succeeded','')", first.OperationID, tenant, first.Definition.ID, string(raw), first.Digest, first.Previous.ETag)
	raw, _ = json.Marshal(first)
	exec("INSERT INTO af_meta_outbox (id,tenant_id,event_type,object_id,payload) VALUES (?,?,'definition.storage_relocated',?,?)", first.OperationID, tenant, first.Definition.ID, string(raw))
	// A later valid relation binding must not invalidate historical verification.
	link.Link.StorageBinding = &c.LinkStorageBinding{Mode: "junction", DataSourceID: items[0].Definition.Schema.StorageBinding.DataSourceID, Namespace: "knowledge", Table: "kb_relations", SourceColumn: "source_id", TargetColumn: "target_id"}
	seed(link)
	if err := VerifyDefaultStorageRelocation(ctx, engine, items); err != nil {
		t.Fatal("post-migration Link configuration blocked evidence verification", err)
	}
	exec("UPDATE af_meta_publications SET previous_digest=? WHERE tenant_id=? AND id=?", strings.Repeat("0", 64), tenant, items[0].OperationID)
	if err := VerifyDefaultStorageRelocation(ctx, engine, items); err == nil {
		t.Fatal("tampered publication evidence accepted")
	}
	exec("UPDATE af_meta_publications SET previous_digest=? WHERE tenant_id=? AND id=?", items[0].Previous.ETag, tenant, items[0].OperationID)
	exec("UPDATE af_meta_outbox SET payload='{}' WHERE tenant_id=? AND id=?", tenant, items[0].OperationID)
	if err := CommitDefaultStorageRelocation(ctx, engine, items); err == nil {
		t.Fatal("tampered event evidence accepted as a successful retry")
	}
}
