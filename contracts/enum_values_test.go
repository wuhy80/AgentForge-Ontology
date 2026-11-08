package contracts

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestEnumResolutionAndStableCodeValidation(t *testing.T) {
	enum := CurrentDefinition{Definition: Definition{ID: "enum", TenantID: "tenant", Kind: KindEnum, Enum: &Enum{Values: []EnumValue{{Code: "A", Label: "可用"}, {Code: "B", Label: "历史", Deprecated: true}}}}, ETag: "current", Status: "active"}
	definition := Definition{TenantID: "tenant", Schema: &Schema{Properties: []Property{{ID: "one", DataType: "string", EnumID: "enum", EnumValues: []EnumValue{{Code: "FORGED", Label: "伪造"}}, EnumETag: "forged"}, {ID: "two", DataType: "string", EnumID: "enum"}}}}
	lookups := 0
	lookup := func(string) (CurrentDefinition, error) { lookups++; return enum, nil }
	if err := ResolveDefinitionEnums(&definition, lookup); err != nil || lookups != 1 {
		t.Fatalf("resolution failed or repeated dependency read: %v / %d", err, lookups)
	}
	property := definition.Schema.Properties[0]
	for _, tc := range []struct {
		raw         string
		read, valid bool
	}{{`"A"`, false, true}, {`"B"`, false, false}, {`"B"`, true, true}, {`"可用"`, true, false}, {`"FORGED"`, false, false}, {`null`, false, true}} {
		if err := ValidateEnumValue(property, json.RawMessage(tc.raw), tc.read); (err == nil) != tc.valid {
			t.Errorf("code %s read=%v: %v", tc.raw, tc.read, err)
		}
	}
	before := EnumContextDigest(definition)
	enum.ETag = "new"
	if err := ResolveDefinitionEnums(&definition, lookup); err != nil {
		t.Fatal(err)
	}
	if before == EnumContextDigest(definition) {
		t.Fatal("current enum change did not alter dependency evidence")
	}
	enum.Definition.TenantID = "other"
	if ResolveDefinitionEnums(&definition, lookup) == nil {
		t.Fatal("cross tenant enum accepted")
	}
}

func TestEnumResolvedMetadataBudget(t *testing.T) {
	definition := Definition{TenantID: "tenant", Schema: &Schema{Properties: []Property{{DataType: "string", EnumID: "enum"}}}}
	values := make([]EnumValue, 1024)
	for i := range values {
		values[i] = EnumValue{Code: strconv.Itoa(i), Label: strings.Repeat("字", 128)}
	}
	// Oversized code metadata is rejected before it reaches a public response.
	if ResolveDefinitionEnums(&definition, func(string) (CurrentDefinition, error) {
		return CurrentDefinition{Definition: Definition{ID: "enum", TenantID: "tenant", Kind: KindEnum, Enum: &Enum{Values: values}}, Status: "active", ETag: "v"}, nil
	}) == nil {
		t.Fatal("oversized enum metadata accepted")
	}
}
