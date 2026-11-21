package data

import (
	"encoding/json"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestFilePropertyRequiresDedicatedWrite(t *testing.T) {
	d := testDefinition()
	p := c.Property{ID: c.NewID(), Name: "主图", DataType: "longtext", Nullable: true, FileKind: c.FilePropertyImage}
	d.Schema.Properties = []c.Property{p}
	d.Schema.Indexes = nil
	if err := validateSchema(d); err != nil {
		t.Fatal(err)
	}
	if _, err := propertyFileField(d, p.Name); err == nil {
		t.Fatal("file endpoint accepted a display name as field ID")
	}
	if _, err := propertyFileField(d, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := buildValues(d.Schema, map[string]json.RawMessage{p.ID: raw("forged summary")}, nil, []string{"*"}, false); err == nil {
		t.Fatal("generic mutation accepted file contents")
	}
	values, err := buildValues(d.Schema, nil, nil, []string{"*"}, true)
	if err != nil || string(values[p.ID]) != "null" {
		t.Fatalf("create should initialize optional file to null: %v", err)
	}
	d.Schema.Properties[0].Nullable = false
	if err := validateSchema(d); err == nil {
		t.Fatal("accepted non-nullable file property")
	}
}
