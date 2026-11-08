package metadata

import (
	"encoding/json"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestPublicationAdmitsSafePlatformAdditions(t *testing.T) {
	old := validSchema()
	clone := func() c.Definition {
		b, _ := json.Marshal(old)
		var next c.Definition
		_ = json.Unmarshal(b, &next)
		return next
	}
	next := clone()
	next.Schema.Properties = append(next.Schema.Properties, c.Property{ID: "new", Name: "负责人", DataType: "string", Nullable: true})
	next.Schema.Indexes = append(next.Schema.Indexes, c.Index{ID: "new-index", Name: "负责人索引", FieldIDs: []string{"new"}})
	if err := compatibleUpdate(old, next); err != nil {
		t.Fatalf("safe field/index addition rejected: %v", err)
	}
	next.Schema.Properties[0].Required = false
	if compatibleUpdate(old, next) == nil {
		t.Fatal("addition hid an old constraint change")
	}
	next = clone()
	next.Schema.Properties = append(next.Schema.Properties, c.Property{ID: "new", Name: "必填属性", DataType: "string", Default: json.RawMessage(`"初始值"`)})
	if err := compatibleUpdate(old, next); err != nil {
		t.Fatalf("valid backfill default rejected: %v", err)
	}
	next.Schema.Properties[1].Default = nil
	if compatibleUpdate(old, next) == nil {
		t.Fatal("nonnullable field without backfill accepted")
	}
}
