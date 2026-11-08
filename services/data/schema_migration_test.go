package data

import (
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestPlatformAdditionPlanUsesBoundDefaults(t *testing.T) {
	old := testDefinition()
	next := old
	s := *old.Schema
	next.Schema = &s
	s.Properties = append([]c.Property(nil), s.Properties...)
	value := "初始'); DROP TABLE secrets; --"
	s.Properties = append(s.Properties, c.Property{ID: c.NewID(), Name: "新属性", DataType: "string", Required: true, Default: raw(value)})
	addition, statements, err := platformAdditionPlan(old, next)
	if err != nil || len(addition.Properties) != 1 || len(statements) != 3 {
		t.Fatalf("invalid additive plan: %v %v", statements, err)
	}
	joined := strings.Join(statements, "\n")
	if strings.Contains(joined, value) || !strings.Contains(joined, "=?") || !strings.Contains(joined, "`etag`=UUID()") || !strings.Contains(joined, "MODIFY COLUMN") {
		t.Fatalf("defaults were interpolated or concurrency was not invalidated: %s", joined)
	}
	s.Properties[len(s.Properties)-1].Default = raw(nil)
	if _, _, err = platformAdditionPlan(old, next); err == nil {
		t.Fatal("nonnullable NULL backfill accepted")
	}
}
