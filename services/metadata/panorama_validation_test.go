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
