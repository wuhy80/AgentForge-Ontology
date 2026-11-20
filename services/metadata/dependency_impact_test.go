package metadata

import (
	"reflect"
	"sort"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestDefinitionReferenceReasonsMatchPublication(t *testing.T) {
	for _, d := range []c.Definition{
		{Schema: &c.Schema{Properties: []c.Property{{EnumID: "enum"}, {EnumID: "enum"}}}},
		{Link: &c.Link{SourceSchemaID: "self", TargetSchemaID: "self"}},
		{Action: &c.Callable{Input: []c.Property{{EnumID: "enum"}}, Output: []c.Property{{EnumID: "other"}}, TargetSchemaIDs: []string{"schema"}, FunctionID: "fn", DataWrite: []c.DataWriteCapability{{SchemaID: "schema"}}}},
		{Function: &c.Callable{Input: []c.Property{{EnumID: "enum"}}, DataWrite: []c.DataWriteCapability{{SchemaID: "schema"}}}},
	} {
		refs := definitionReferenceReasons(d)
		ids := []string{}
		for id := range refs {
			ids = append(ids, id)
		}
		want := dependencyIDs(d)
		sort.Strings(ids)
		sort.Strings(want)
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("impact and publication references disagree: %v / %v", ids, want)
		}
	}
	self := definitionReferenceReasons(c.Definition{Link: &c.Link{SourceSchemaID: "self", TargetSchemaID: "self"}})
	if !reflect.DeepEqual(self["self"], []string{"link-source", "link-target"}) {
		t.Fatalf("self link reasons lost: %v", self)
	}
}
