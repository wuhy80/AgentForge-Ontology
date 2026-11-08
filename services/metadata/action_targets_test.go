package metadata

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

func validAction() contracts.Definition {
	d := validSchema()
	d.ID, d.Kind, d.Name, d.Schema = "action-1", contracts.KindAction, "设备维护", nil
	d.Action = &contracts.Callable{SideEffects: "dataWrite", DataWrite: []contracts.DataWriteCapability{{SchemaID: "schema-a", Operations: []string{"update"}}}, TargetSchemaIDs: []string{"schema-b", "schema-a"}}
	return d
}

func TestActionTargetsAreCanonicalDependencies(t *testing.T) {
	d := validAction()
	if err := canonicalDefinition(&d, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.Action.TargetSchemaIDs, []string{"schema-a", "schema-b"}) {
		t.Fatalf("targets did not canonicalize as a set: %+v", d.Action.TargetSchemaIDs)
	}
	first := digestDefinition(d)
	d.Action.TargetSchemaIDs = []string{"schema-b", "schema-a"}
	if err := canonicalDefinition(&d, false); err != nil || digestDefinition(d) != first {
		t.Fatal("reordering target selection changed definition identity")
	}
	d.Action.FunctionID = "function-id"
	d.Action.Input = []contracts.Property{{ID: "enum-field", Name: "状态", DataType: "string", EnumID: "enum-id"}}
	ids := dependencyIDs(d)
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"enum-id", "function-id", "schema-a", "schema-b"}) {
		t.Fatalf("action dependencies omitted targets or callable references: %+v", ids)
	}
}

func TestActionTargetValidationAndBudget(t *testing.T) {
	for name, targets := range map[string][]string{
		"empty ID": {""}, "blank ID": {" "}, "duplicate": {"schema-a", "schema-a"},
		"case duplicate": {"schema-a", "SCHEMA-A"}, "padded": {" schema-a"},
		"control": {"schema\x00a"}, "long": {strings.Repeat("x", 129)},
	} {
		t.Run(name, func(t *testing.T) {
			d := validAction()
			d.Action.TargetSchemaIDs = targets
			if err := canonicalDefinition(&d, false); err == nil {
				t.Fatal("invalid Action target accepted")
			}
		})
	}
	d := validAction()
	d.Action.TargetSchemaIDs = nil
	if err := canonicalDefinition(&d, false); err != nil {
		t.Fatalf("legacy unbound action rejected: %v", err)
	}
	for i := 0; i < 32; i++ {
		d.Action.TargetSchemaIDs = append(d.Action.TargetSchemaIDs, contracts.NewID())
	}
	if err := canonicalDefinition(&d, false); err != nil {
		t.Fatalf("valid target limit rejected: %v", err)
	}
	d.Action.TargetSchemaIDs = append(d.Action.TargetSchemaIDs, contracts.NewID())
	if err := canonicalDefinition(&d, false); err == nil {
		t.Fatal("Action target budget was not enforced")
	}
	d = validAction()
	d.Kind, d.Function, d.Action = contracts.KindFunction, d.Action, nil
	if err := canonicalDefinition(&d, false); err == nil {
		t.Fatal("Function silently accepted Action-only target metadata")
	}
}

func TestActionTargetUpdatesPreserveCallableBoundaries(t *testing.T) {
	old := validAction()
	raw, _ := json.Marshal(old)
	var next contracts.Definition
	_ = json.Unmarshal(raw, &next)
	next.Action.TargetSchemaIDs = []string{"schema-c"}
	if err := compatibleUpdate(old, next); err != nil {
		t.Fatalf("target metadata update rejected: %v", err)
	}
	if !reflect.DeepEqual(old.Action.TargetSchemaIDs, []string{"schema-b", "schema-a"}) {
		t.Fatal("compatibility comparison mutated current targets")
	}
	next.Action.ArtifactRef = "new-implementation"
	if err := compatibleUpdate(old, next); err == nil {
		t.Fatal("target changes bypassed implementation lock")
	}
	next.Action.ArtifactRef = ""
	next.Action.SideEffects = "externalWrite"
	if err := compatibleUpdate(old, next); err == nil {
		t.Fatal("target changes bypassed side-effect lock")
	}
}
