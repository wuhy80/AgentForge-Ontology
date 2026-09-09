package runtime

import (
	"context"
	"github.com/agentforge/ontology/internal/datasource"
	"github.com/agentforge/ontology/internal/ontology"
	"github.com/agentforge/ontology/internal/publish"
	"testing"
	"time"
)

func testRuntime(t *testing.T) *Service {
	t.Helper()
	driver := &datasource.MemoryDriver{Resources: map[string][]map[string]any{"pumps": {{"pump_id": "P1", "temp": 86.2, "motor_id": "M1"}, {"pump_id": "P2", "temp": 60.0, "motor_id": "M2"}}, "motors": {{"motor_id": "M1", "name": "Main Motor"}, {"motor_id": "M2", "name": "Backup Motor"}}}}
	registry := datasource.NewRegistry()
	if err := registry.Register(driver); err != nil {
		t.Fatal(err)
	}
	binding := func(resource string, fields map[string]ontology.FieldBinding) *ontology.DataBinding {
		return &ontology.DataBinding{Source: "factory", Resource: resource, PrimaryKey: []string{"id"}, Fields: fields}
	}
	draft := ontology.Draft{Workspace: ontology.Workspace{ID: "yard"}, DataSources: []ontology.DataSource{{ID: "factory", Driver: "memory"}}, Vendors: []ontology.Vendor{{ID: "yard", Domains: []ontology.Domain{{ID: "equipment", Objects: []ontology.ObjectType{{Name: "Motor", DisplayName: "电机", Properties: []ontology.Property{{Name: "id", Type: ontology.TypeString}, {Name: "name", Type: ontology.TypeString}}, Binding: binding("motors", map[string]ontology.FieldBinding{"id": {Field: "motor_id"}, "name": {Field: "name"}})}, {Name: "Pump", DisplayName: "冷却泵", Aliases: []string{"主冷却水泵"}, Properties: []ontology.Property{{Name: "id", Type: ontology.TypeString}, {Name: "temperature", Type: ontology.TypeFloat}, {Name: "motorId", Type: ontology.TypeString}}, Links: []ontology.Link{{Name: "drivenBy", Target: ontology.ObjectRef{Vendor: "yard", Domain: "equipment", Object: "Motor"}, ReverseName: "drives", Cardinality: ontology.ManyToOne, Resolver: ontology.LinkResolver{Type: "field", SourceField: "motorId", TargetField: "id"}}}, Binding: binding("pumps", map[string]ontology.FieldBinding{"id": {Field: "pump_id"}, "temperature": {Field: "temp"}, "motorId": {Field: "motor_id"}})}}}}}}}
	snap, errs := publish.Compile(draft, 1, time.Now())
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	rt := New(registry)
	rt.Activate(snap)
	return rt
}
func TestSemanticQueryPushesFiltersAndMapsFields(t *testing.T) {
	rt := testRuntime(t)
	result, err := rt.Query(context.Background(), ObjectQuery{Workspace: "yard", Object: "yard.equipment.Pump", Fields: []string{"id", "temperature"}, Filters: []SemanticFilter{{Property: "temperature", Operator: datasource.OpGreater, Value: 80.0}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0]["id"] != "P1" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
func TestFieldLinkResolverLoadsRelatedObject(t *testing.T) {
	rt := testRuntime(t)
	result, err := rt.Related(context.Background(), RelatedQuery{Workspace: "yard", Object: "yard.equipment.Pump", ID: "P1", Link: "drivenBy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0]["name"] != "Main Motor" {
		t.Fatalf("unexpected relation: %#v", result)
	}
}
func TestSearchUsesAliases(t *testing.T) {
	rt := testRuntime(t)
	hits, err := rt.Search(context.Background(), "yard", "主冷却水泵", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Key != "yard.equipment.Pump" {
		t.Fatalf("unexpected hits: %#v", hits)
	}
}
