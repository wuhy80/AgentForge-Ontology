package contracts

import (
	"encoding/json"
	"testing"
)

func TestSchemaAdditionPreservesExistingIdentitiesAndConstraints(t *testing.T) {
	old := Definition{ID: "schema", TenantID: "tenant", CategoryID: "category", GroupID: "group", Kind: KindSchema, Schema: &Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []Property{{ID: "old", Name: "原属性", DataType: "string", Required: true}}, Indexes: []Index{{ID: "old-index", Name: "原索引", FieldIDs: []string{"old"}}}}}
	clone := func() Definition {
		b, _ := json.Marshal(old)
		var next Definition
		_ = json.Unmarshal(b, &next)
		return next
	}
	for _, tc := range []struct {
		name  string
		edit  func(*Definition)
		valid bool
	}{
		{"nullable", func(d *Definition) {
			d.Schema.Properties = append(d.Schema.Properties, Property{ID: "new", DataType: "string", Nullable: true})
		}, true},
		{"required-default", func(d *Definition) {
			d.Schema.Properties = append(d.Schema.Properties, Property{ID: "new", DataType: "int64", Default: json.RawMessage(`"9007199254740993"`)})
		}, true},
		{"index-only", func(d *Definition) {
			d.Schema.Indexes = append(d.Schema.Indexes, Index{ID: "new-index", Unique: true, FieldIDs: []string{"old"}})
		}, true},
		{"required-no-default", func(d *Definition) {
			d.Schema.Properties = append(d.Schema.Properties, Property{ID: "new", DataType: "string"})
		}, false},
		{"required-null-default", func(d *Definition) {
			d.Schema.Properties = append(d.Schema.Properties, Property{ID: "new", DataType: "string", Default: json.RawMessage(` null `)})
		}, false},
		{"wrong-default", func(d *Definition) {
			d.Schema.Properties = append(d.Schema.Properties, Property{ID: "new", DataType: "int64", Default: json.RawMessage(`9007199254740993`)})
		}, false},
		{"delete-field", func(d *Definition) { d.Schema.Properties = nil }, false},
		{"retype-field", func(d *Definition) { d.Schema.Properties[0].DataType = "uuid" }, false},
		{"rebind-enum", func(d *Definition) { d.Schema.Properties[0].EnumID = "enum" }, false},
		{"change-nullability", func(d *Definition) { d.Schema.Properties[0].Nullable = true }, false},
		{"change-default", func(d *Definition) { d.Schema.Properties[0].Default = json.RawMessage(`"changed"`) }, false},
		{"delete-index", func(d *Definition) { d.Schema.Indexes = nil }, false},
		{"change-index", func(d *Definition) { d.Schema.Indexes[0].Unique = true }, false},
		{"move", func(d *Definition) { d.GroupID = "other" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := clone()
			tc.edit(&next)
			_, err := ValidateSchemaAddition(old, next)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}
