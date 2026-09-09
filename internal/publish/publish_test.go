package publish

import (
	"github.com/agentforge/ontology/internal/ontology"
	"testing"
	"time"
)

func TestCompileCreatesImmutableSnapshot(t *testing.T) {
	draft := ontology.Draft{Workspace: ontology.Workspace{ID: "yard"}, DataSources: []ontology.DataSource{{ID: "source", Driver: "memory"}}, Vendors: []ontology.Vendor{{ID: "acme", Domains: []ontology.Domain{{ID: "equipment", Objects: []ontology.ObjectType{{Name: "Pump", Properties: []ontology.Property{{Name: "id", Type: ontology.TypeString}}, Binding: &ontology.DataBinding{Source: "source", Resource: "pumps", PrimaryKey: []string{"id"}, Fields: map[string]ontology.FieldBinding{"id": {Field: "id"}}}}}}}}}}
	snapshot, errs := Compile(draft, 1, time.Unix(1, 0))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	draft.Vendors[0].Domains[0].Objects[0].Properties[0].Name = "changed"
	if _, ok := snapshot.Objects["acme.equipment.Pump"].Properties["id"]; !ok {
		t.Fatal("snapshot changed with mutable draft")
	}
	if snapshot.Revision != 1 || snapshot.PublicationID == "" || len(snapshot.Hash) != 64 {
		t.Fatalf("invalid publication metadata: %#v", snapshot)
	}
}
