package metadata

import (
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestInlineCallableAcceptsStringEnumParameters(t *testing.T) {
	for _, create := range []func() c.Definition{executableFunction, executableAction} {
		definition := create()
		for _, property := range c.DefinitionProperties(&definition) {
			property.EnumID = "current-enum"
		}
		if err := canonicalDefinition(&definition, false); err != nil {
			t.Fatalf("%s string enum parameters rejected: %v", definition.Kind, err)
		}
	}
}

func TestEnumEvolutionPreservesStableCodesAndRejectsClientMetadata(t *testing.T) {
	old := c.Definition{ID: "enum", TenantID: "tenant", CategoryID: "category", GroupID: "group", Kind: c.KindEnum, Name: "状态", Enum: &c.Enum{Values: []c.EnumValue{{Code: "A", Label: "可用"}}}}
	next := old
	next.Enum = &c.Enum{Values: []c.EnumValue{{Code: "A", Label: "历史", Deprecated: true}, {Code: "B", Label: "新值"}}}
	if err := compatibleUpdate(old, next); err != nil {
		t.Fatalf("add/deprecate enum rejected: %v", err)
	}
	next.Enum = &c.Enum{Values: []c.EnumValue{{Code: "B", Label: "替换"}}}
	if compatibleUpdate(old, next) == nil {
		t.Fatal("used stable code could be deleted or reused")
	}
	for _, derived := range []bool{false, true} {
		definition := validSchema()
		definition.Schema.Properties[0].EnumID = "enum"
		if derived {
			definition.Schema.Properties[0].EnumETag = "forged"
		} else {
			definition.Schema.Properties[0].EnumValues = []c.EnumValue{}
		}
		if canonicalDefinition(&definition, false) == nil {
			t.Fatal("client controlled derived enum metadata accepted")
		}
	}
}
