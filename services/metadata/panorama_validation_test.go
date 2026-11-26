package metadata

import (
	"reflect"
	"testing"
)

func TestSchemaPanoramaMetadata(t *testing.T) {
	definition := validSchema()
	definition.Schema.Tags = []string{"  Production  ", "Cafe\u0301"}
	definition.Schema.PanoramaLayer = "L2"
	if err := canonicalDefinition(&definition, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(definition.Schema.Tags, []string{"Production", "Café"}) {
		t.Fatalf("unexpected canonical tags: %v", definition.Schema.Tags)
	}
	definition.Schema.Tags = append(definition.Schema.Tags, "production")
	if err := canonicalDefinition(&definition, true); err == nil {
		t.Fatal("duplicate label accepted")
	}
	definition.Schema.Tags = nil
	definition.Schema.PanoramaLayer = "M0"
	if err := canonicalDefinition(&definition, true); err == nil {
		t.Fatal("reserved layer accepted")
	}
}

func TestMappedSchemaClassificationUpdate(t *testing.T) {
	old, next := mappedDefinition(), mappedDefinition()
	next.Schema.Tags, next.Schema.PanoramaLayer = []string{"知识组织"}, "L2"
	if err := compatibleUpdate(old, next); err != nil {
		t.Fatal(err)
	}
	next.Schema.StorageBinding.Table += "_other"
	if err := compatibleUpdate(old, next); err == nil {
		t.Fatal("changed table accepted")
	}
}
