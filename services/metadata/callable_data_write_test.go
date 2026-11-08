package metadata

import (
	"encoding/json"
	"reflect"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func writingFunction() c.Definition {
	d := executableFunction()
	d.Function.SideEffects = "dataWrite"
	d.Function.DataWrite = []c.DataWriteCapability{{SchemaID: "schema-b", Operations: []string{"update", "create"}}, {SchemaID: "schema-a", Operations: []string{"upsert"}}}
	d.Function.Runtime.Source = "def main(inputs, ctx):\n    return {'结果': inputs['文本']}\n"
	return d
}

func TestDataWriteDeclarationsCanonicalizeAndTrackDependencies(t *testing.T) {
	d := writingFunction()
	if err := canonicalDefinition(&d, false); err != nil {
		t.Fatal(err)
	}
	first := digestDefinition(d)
	if d.Function.DataWrite[0].SchemaID != "schema-a" || !reflect.DeepEqual(d.Function.DataWrite[1].Operations, []string{"create", "update"}) {
		t.Fatal("declarations are not canonical")
	}
	d.Function.DataWrite[0], d.Function.DataWrite[1] = d.Function.DataWrite[1], d.Function.DataWrite[0]
	d.Function.DataWrite[0].Operations = []string{"update", "create"}
	if err := canonicalDefinition(&d, false); err != nil || digestDefinition(d) != first {
		t.Fatal("set reorder changed identity", err)
	}
	deps := dependencyIDs(d)
	if len(deps) != 2 {
		t.Fatal("write schemas absent from dependencies", deps)
	}
	for _, language := range []string{"python", "javascript", "go"} {
		d.Function.Runtime.Language = language
		d.Function.Runtime.Entrypoint = "main"
		if language == "go" {
			d.Function.Runtime.Entrypoint = "Main"
		}
		if err := canonicalDefinition(&d, false); err != nil {
			t.Fatalf("%s data writer rejected: %v", language, err)
		}
	}
	if inlineCallableAvailable(d.Function) {
		t.Fatal("data writer exposed through MCP callable discovery")
	}
}

func TestInvalidDataWriteDeclarationsFailClosed(t *testing.T) {
	for name, mutate := range map[string]func(*c.Callable){
		"missing":                 func(body *c.Callable) { body.DataWrite = nil },
		"none carries writes":     func(body *c.Callable) { body.SideEffects = "none" },
		"external carries writes": func(body *c.Callable) { body.SideEffects = "externalWrite" },
		"duplicate schema":        func(body *c.Callable) { body.DataWrite = append(body.DataWrite, body.DataWrite[0]) },
		"empty schema":            func(body *c.Callable) { body.DataWrite[0].SchemaID = "" },
		"padded schema":           func(body *c.Callable) { body.DataWrite[0].SchemaID = " schema" },
		"control schema":          func(body *c.Callable) { body.DataWrite[0].SchemaID = "schema\n" },
		"missing operations":      func(body *c.Callable) { body.DataWrite[0].Operations = nil },
		"duplicate operation":     func(body *c.Callable) { body.DataWrite[0].Operations = []string{"create", "create"} },
		"bulk declaration":        func(body *c.Callable) { body.DataWrite[0].Operations = []string{"bulk_create"} },
		"unknown operation":       func(body *c.Callable) { body.DataWrite[0].Operations = []string{"query"} },
		"schema budget": func(body *c.Callable) {
			for i := 0; i < 33; i++ {
				body.DataWrite = append(body.DataWrite, c.DataWriteCapability{SchemaID: c.NewID(), Operations: []string{"create"}})
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := writingFunction()
			mutate(d.Function)
			if canonicalDefinition(&d, false) == nil {
				t.Fatal("invalid write declaration accepted")
			}
		})
	}
}

func TestDataWriteBindingAndActiveUpdatesPreserveCapability(t *testing.T) {
	function := writingFunction()
	action := function
	raw, _ := json.Marshal(function.Function)
	var body c.Callable
	_ = json.Unmarshal(raw, &body)
	action.Kind, action.Function, action.Action = c.KindAction, nil, &body
	action.Action.FunctionID, action.Action.Runtime = function.ID, nil
	action.Action.DataWrite[0], action.Action.DataWrite[1] = action.Action.DataWrite[1], action.Action.DataWrite[0]
	if err := validateCallableBinding(action, function); err != nil {
		t.Fatal("equivalent binding rejected", err)
	}
	action.Action.DataWrite[0].Operations = []string{"delete"}
	if validateCallableBinding(action, function) == nil {
		t.Fatal("different write range accepted")
	}
	for _, creator := range []func() c.Definition{writingFunction, func() c.Definition {
		d := writingFunction()
		d.Kind, d.Action, d.Function = c.KindAction, d.Function, nil
		return d
	}} {
		old := creator()
		raw, _ := json.Marshal(old)
		var next c.Definition
		_ = json.Unmarshal(raw, &next)
		body := next.Function
		if body == nil {
			body = next.Action
		}
		body.Runtime.Source += "\n# update"
		if err := compatibleUpdate(old, next); err != nil {
			t.Fatal("safe source update rejected", err)
		}
		body.DataWrite[0].Operations = []string{"delete"}
		if compatibleUpdate(old, next) == nil {
			t.Fatal("source update bypassed write capability lock")
		}
	}
}

func TestWriteSchemaReferencesRequireSameTenantAndSchema(t *testing.T) {
	d := writingFunction()
	target := validSchema()
	target.ID = "schema-a"
	target.TenantID = d.TenantID
	if err := validateDataWriteSchema(d, target, target.ID); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*c.Definition){func(ref *c.Definition) { ref.TenantID = "foreign" }, func(ref *c.Definition) { ref.Kind = c.KindFunction }, func(ref *c.Definition) { ref.Schema = nil }, func(ref *c.Definition) { ref.ID = "other" }} {
		ref := target
		mutate(&ref)
		if validateDataWriteSchema(d, ref, target.ID) == nil {
			t.Fatal("invalid write schema accepted")
		}
	}
}
