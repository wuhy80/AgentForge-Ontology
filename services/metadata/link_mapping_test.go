package metadata

import (
	"encoding/json"
	"reflect"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func validLinkMappingDefinition() c.Definition {
	return c.Definition{ID: "link-1", TenantID: "tenant-1", CategoryID: "category-1", GroupID: "group-1", Kind: c.KindLink, Name: "笔记标记标签", Link: &c.Link{SourceSchemaID: "source", TargetSchemaID: "target", ForwardName: "标记标签", ReverseName: "标记笔记", Cardinality: "N:M", OnDelete: "restrict"}}
}

func cloneLinkDefinition(d c.Definition) c.Definition {
	raw, _ := json.Marshal(d)
	var result c.Definition
	_ = json.Unmarshal(raw, &result)
	return result
}

func TestLinkStorageMappingsRejectUnsupportedOrContradictoryForms(t *testing.T) {
	for _, tc := range []struct {
		name        string
		binding     c.LinkStorageBinding
		cardinality string
		valid       bool
	}{
		{"managed", c.LinkStorageBinding{Mode: "managed"}, "N:M", true},
		{"junction", c.LinkStorageBinding{Mode: "junction", DataSourceID: "db", Table: "笔记标签", SourceColumn: "笔记ID", TargetColumn: "标签ID", TenantColumn: "租户"}, "N:M", true},
		{"source foreign key", c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: "db", ForeignKeySide: "source", ForeignKeyColumn: "category_id"}, "N:1", true},
		{"target foreign key", c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: "db", ForeignKeySide: "target", ForeignKeyColumn: "note_id"}, "1:N", true},
		{"unknown mode", c.LinkStorageBinding{Mode: "sql"}, "N:M", false},
		{"mixed managed fields", c.LinkStorageBinding{Mode: "managed", Table: "existing"}, "N:M", false},
		{"same junction columns", c.LinkStorageBinding{Mode: "junction", DataSourceID: "db", Table: "edges", SourceColumn: "id", TargetColumn: "id"}, "N:M", false},
		{"junction tenant collision", c.LinkStorageBinding{Mode: "junction", DataSourceID: "db", Table: "edges", SourceColumn: "source", TargetColumn: "target", TenantColumn: "target"}, "N:M", false},
		{"junction no source", c.LinkStorageBinding{Mode: "junction", Table: "edges", SourceColumn: "source", TargetColumn: "target"}, "N:M", false},
		{"junction control", c.LinkStorageBinding{Mode: "junction", DataSourceID: "db", Table: "edges\x00", SourceColumn: "source", TargetColumn: "target"}, "N:M", false},
		{"source key incompatible cardinality", c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: "db", ForeignKeySide: "source", ForeignKeyColumn: "fk"}, "1:N", false},
		{"target key incompatible cardinality", c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: "db", ForeignKeySide: "target", ForeignKeyColumn: "fk"}, "N:1", false},
		{"many to many key", c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: "db", ForeignKeySide: "source", ForeignKeyColumn: "fk"}, "N:M", false},
		{"key missing side", c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: "db", ForeignKeyColumn: "fk"}, "1:1", false},
		{"key mixed table", c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: "db", Table: "edges", ForeignKeySide: "source", ForeignKeyColumn: "fk"}, "N:1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := validLinkMappingDefinition()
			d.Link.Cardinality, d.Link.StorageBinding = tc.cardinality, &tc.binding
			if err := canonicalDefinition(&d, false); (err == nil) != tc.valid {
				t.Fatalf("valid=%t, err=%v", tc.valid, err)
			}
		})
	}
	d := validLinkMappingDefinition()
	if err := canonicalDefinition(&d, false); err != nil || d.Link.StorageBinding != nil {
		t.Fatal("definition-only link must stay unbound", err)
	}
}

func TestLinkMappingEndpointScopeAndStorageAreChecked(t *testing.T) {
	source, target := validSchema(), validSchema()
	source.ID, target.ID = "source", "target"
	l := validLinkMappingDefinition().Link
	l.StorageBinding = &c.LinkStorageBinding{Mode: "managed"}
	if err := validateLinkEndpoints(l, source, target, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*c.Definition){
		func(d *c.Definition) { d.ID = "another" },
		func(d *c.Definition) { d.TenantID = "foreign" },
		func(d *c.Definition) { d.Kind = c.KindEnum },
		func(d *c.Definition) { d.Schema = nil },
	} {
		copy := cloneLinkDefinition(source)
		modify(&copy)
		if err := validateLinkEndpoints(l, copy, target, "tenant-1"); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
	source.Schema.StorageBinding = &c.StorageBinding{DataSourceID: "db", Table: "notes", IDColumn: "id", TenantColumn: "tenant"}
	target.Schema.StorageBinding = &c.StorageBinding{DataSourceID: "db", Table: "tags", IDColumn: "id", TenantColumn: "tenant"}
	if err := validateLinkEndpoints(l, source, target, "tenant-1"); err == nil {
		t.Fatal("managed link accepted external endpoints")
	}
	l.StorageBinding = &c.LinkStorageBinding{Mode: "junction", DataSourceID: "db", Table: "edges", SourceColumn: "source", TargetColumn: "target"}
	if err := validateLinkEndpoints(l, source, target, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	target.Schema.StorageBinding.DataSourceID = "other"
	if err := validateLinkEndpoints(l, source, target, "tenant-1"); err == nil {
		t.Fatal("cross-source relation accepted")
	}
	target.Schema.StorageBinding.DataSourceID = "db"
	l.StorageBinding = &c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: "db", ForeignKeySide: "source", ForeignKeyColumn: "tenant"}
	if err := validateLinkEndpoints(l, source, target, "tenant-1"); err == nil {
		t.Fatal("tenant column accepted as foreign key")
	}
}

func TestActiveLinkBindingCanOnlyBeAddedOnceWithoutChangingStructure(t *testing.T) {
	old := validLinkMappingDefinition()
	next := cloneLinkDefinition(old)
	next.Name, next.Link.ForwardName = "笔记标签关联", "拥有标签"
	next.Link.StorageBinding = &c.LinkStorageBinding{Mode: "managed"}
	before := cloneLinkDefinition(next)
	if err := compatibleUpdate(old, next); err != nil {
		t.Fatal("explicit first binding rejected", err)
	}
	if old.Link.StorageBinding != nil || !reflect.DeepEqual(next, before) {
		t.Fatal("compatibility check mutated its inputs")
	}
	for _, mutate := range []func(*c.Definition){
		func(d *c.Definition) { d.Link.SourceSchemaID = "another" },
		func(d *c.Definition) { d.Link.TargetSchemaID = "another" },
		func(d *c.Definition) { d.Link.Cardinality = "1:N" },
		func(d *c.Definition) { d.Link.OnDelete = "cascade" },
	} {
		changed := cloneLinkDefinition(next)
		mutate(&changed)
		if err := compatibleUpdate(old, changed); err == nil {
			t.Fatal("structure change hidden behind initial storage binding")
		}
	}
	if err := compatibleUpdate(next, old); err == nil {
		t.Fatal("active mapping removal accepted")
	}
	changed := cloneLinkDefinition(next)
	changed.Link.StorageBinding.Mode = "junction"
	if err := compatibleUpdate(next, changed); err == nil {
		t.Fatal("active mapping replacement accepted")
	}
}
