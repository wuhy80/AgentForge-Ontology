package metadata

import (
	"encoding/json"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func executableFunction() c.Definition {
	d := validSchema()
	d.Kind, d.Schema = c.KindFunction, nil
	d.Function = &c.Callable{SideEffects: "none", Input: []c.Property{{ID: "input", Name: "文本", DataType: "string", Required: true}}, Output: []c.Property{{ID: "output", Name: "结果", DataType: "string", Required: true}}, Runtime: &c.ScriptRuntime{Language: "python", Source: "def main(inputs):\n    return {'结果': inputs['文本']}\n", Entrypoint: "main", TimeoutSeconds: 5}}
	return d
}

func TestInlinePythonRequiresPureFunctionAndUnambiguousImplementation(t *testing.T) {
	valid := executableFunction()
	if err := canonicalDefinition(&valid, false); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*c.Definition){
		"action script":         func(d *c.Definition) { d.Kind, d.Action, d.Function = c.KindAction, d.Function, nil },
		"external side effects": func(d *c.Definition) { d.Function.SideEffects = "externalWrite" },
		"data side effects":     func(d *c.Definition) { d.Function.SideEffects = "dataWrite" },
		"ambiguous artifact":    func(d *c.Definition) { d.Function.ArtifactRef = "some-image" },
		"nested function":       func(d *c.Definition) { d.Function.FunctionID = "another-function" },
		"unsupported language":  func(d *c.Definition) { d.Function.Runtime.Language = "js" },
		"enum parameter":        func(d *c.Definition) { d.Function.Input[0].EnumID = "enum-id" },
	} {
		t.Run(name, func(t *testing.T) {
			d := executableFunction()
			mutate(&d)
			if err := canonicalDefinition(&d, false); err == nil {
				t.Fatal("unsupported implementation accepted")
			}
		})
	}
	valid.Function.Runtime = nil
	if err := canonicalDefinition(&valid, false); err != nil {
		t.Fatal("metadata-only legacy definition rejected", err)
	}
}

func TestPureFunctionRuntimeUpdatePreservesSignatureAndSnapshot(t *testing.T) {
	old := executableFunction()
	raw, _ := json.Marshal(old)
	var next c.Definition
	_ = json.Unmarshal(raw, &next)
	next.Function.Runtime.Source = "def main(inputs):\n    return {'结果': inputs['文本'].upper()}\n"
	next.Function.Runtime.TimeoutSeconds = 10
	if compatibleUpdate(old, next) != nil || old.Function.Runtime.TimeoutSeconds != 5 {
		t.Fatal("runtime update rejected or mutated current implementation")
	}
	next.Function.Output[0].DataType = "text"
	if compatibleUpdate(old, next) == nil {
		t.Fatal("runtime update bypassed output signature lock")
	}
	next.Function.Output[0].DataType = "string"
	next.Function.SideEffects = "dataWrite"
	if compatibleUpdate(old, next) == nil {
		t.Fatal("runtime update bypassed side effect lock")
	}
	next.Function.SideEffects = "none"
	old.Function.Runtime = nil
	if compatibleUpdate(old, next) != nil {
		t.Fatal("pure metadata-only function cannot acquire an implementation")
	}
}

func TestActionBindingChecksFullContractAndTenant(t *testing.T) {
	function := executableFunction()
	action := function
	action.ID, action.Kind, action.Function = "action", c.KindAction, nil
	raw, _ := json.Marshal(function.Function)
	_ = json.Unmarshal(raw, &action.Action)
	action.Action.Runtime, action.Action.FunctionID = nil, function.ID
	action.Action.Input[0].ID, action.Action.Output[0].ID = "action-input", "action-output"
	if err := validateCallableBinding(action, function); err != nil {
		t.Fatal("matching stable-ID-independent signature rejected", err)
	}
	for _, mutate := range []func(*c.Definition){
		func(d *c.Definition) { d.TenantID = "other" },
		func(d *c.Definition) { d.Kind = c.KindSchema },
		func(d *c.Definition) { d.Function.SideEffects = "dataWrite" },
		func(d *c.Definition) { d.Function.Input[0].Nullable = true },
		func(d *c.Definition) { d.Function.Input[0].Name = "其他文本" },
	} {
		raw, _ := json.Marshal(function)
		var candidate c.Definition
		_ = json.Unmarshal(raw, &candidate)
		mutate(&candidate)
		if validateCallableBinding(action, candidate) == nil {
			t.Fatal("mismatched function binding accepted")
		}
	}
}
