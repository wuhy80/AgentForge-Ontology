package metadata

import (
	"encoding/json"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func mappedDefinition() c.Definition {
	d := validSchema()
	d.Schema.StorageBinding = &c.StorageBinding{DataSourceID: "source-a", Namespace: "business", Table: "devices", TenantColumn: "tenant_id"}
	d.Schema.Properties[0].Mapping = &c.PropertyMapping{DataSourceID: "source-a", Column: "name"}
	d.Schema.Properties = append(d.Schema.Properties, c.Property{ID: "field-2", Name: "外部状态", DataType: "string", Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: "source-b", Namespace: "public", Table: "device_status", Column: "status", KeyColumn: "device_id", TenantColumn: "tenant_id"}})
	return d
}

func copyMapped(d c.Definition) c.Definition {
	raw, _ := json.Marshal(d)
	var out c.Definition
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestMappingCanonicalizationPreservesSourceIdentity(t *testing.T) {
	d := mappedDefinition()
	d.Schema.Mode = "db_to_schema"
	if err := canonicalDefinition(&d, false); err != nil {
		t.Fatal(err)
	}
	if d.Schema.Mode != "db_2_schema" || d.Schema.StorageBinding.IDColumn != "id" {
		t.Fatal("mode and primary identity not normalized")
	}
	m := d.Schema.Properties[0].Mapping
	if m.Table != "devices" || m.Namespace != "business" || m.KeyColumn != "id" || m.TenantColumn != "tenant_id" {
		t.Fatalf("primary inheritance lost: %+v", m)
	}
	before := digestDefinition(d)
	if err := canonicalDefinition(&d, false); err != nil || before != digestDefinition(d) {
		t.Fatal("canonical mapping changed identity on retry")
	}
}

func TestMappingRejectsAmbiguousOrUnsafeAssociations(t *testing.T) {
	cases := map[string]func(*c.Definition){
		"missing field source":          func(d *c.Definition) { d.Schema.Properties[0].Mapping = nil },
		"missing associated table":      func(d *c.Definition) { d.Schema.Properties[1].Mapping.Table = "" },
		"missing unique key":            func(d *c.Definition) { d.Schema.Properties[1].Mapping.KeyColumn = "" },
		"missing row cannot be nonnull": func(d *c.Definition) { d.Schema.Properties[1].Nullable = false },
		"primary key conflict":          func(d *c.Definition) { d.Schema.Properties[0].Mapping.KeyColumn = "another_id" },
		"control identifier":            func(d *c.Definition) { d.Schema.Properties[1].Mapping.Column = "status\x00" },
		"repeated physical column": func(d *c.Definition) {
			p := d.Schema.Properties[1]
			p.ID = "field-3"
			p.Name = "重复状态"
			d.Schema.Properties = append(d.Schema.Properties, p)
		},
		"inconsistent association": func(d *c.Definition) {
			p := copyMapped(*d).Schema.Properties[1]
			p.ID = "field-3"
			p.Name = "附加信息"
			p.Mapping.Column = "extra"
			p.Mapping.KeyColumn = "other_id"
			d.Schema.Properties = append(d.Schema.Properties, p)
		},
		"cross-source index": func(d *c.Definition) {
			d.Schema.Indexes = []c.Index{{ID: "index-1", Name: "跨源索引", FieldIDs: []string{"field-1", "field-2"}}}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			d := mappedDefinition()
			change(&d)
			if canonicalDefinition(&d, false) == nil {
				t.Fatal("invalid mapping accepted")
			}
		})
	}
}

func TestMappedAdditionRequiresImmutableBindingsAndNullableNewFields(t *testing.T) {
	old := mappedDefinition()
	if err := canonicalDefinition(&old, false); err != nil {
		t.Fatal(err)
	}
	next := copyMapped(old)
	next.Schema.Properties = append(next.Schema.Properties, c.Property{ID: "field-3", Name: "备注", DataType: "string", Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: "source-a", Column: "remark"}})
	if err := canonicalDefinition(&next, false); err != nil {
		t.Fatal(err)
	}
	if err := compatibleUpdate(old, next); err != nil {
		t.Fatalf("safe mapped addition rejected: %v", err)
	}
	cases := map[string]func(*c.Definition){
		"nonnullable addition":   func(d *c.Definition) { d.Schema.Properties[2].Nullable = false },
		"retarget field":         func(d *c.Definition) { d.Schema.Properties[0].Mapping.Column = "other_name" },
		"retarget source":        func(d *c.Definition) { d.Schema.StorageBinding.DataSourceID = "another-source" },
		"drop field":             func(d *c.Definition) { d.Schema.Properties = d.Schema.Properties[1:] },
		"change mode":            func(d *c.Definition) { d.Schema.Mode = "db_2_schema" },
		"default needs backfill": func(d *c.Definition) { d.Schema.Properties[2].Default = json.RawMessage(`"filled"`) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := copyMapped(next)
			change(&candidate)
			if compatibleUpdate(old, candidate) == nil {
				t.Fatal("unsafe migration accepted")
			}
		})
	}
}
