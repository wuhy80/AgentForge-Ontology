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

func executableAction() c.Definition {
	d := executableFunction()
	d.Kind, d.Action, d.Function = c.KindAction, d.Function, nil
	return d
}

func TestInlinePythonRequiresPureCallableAndUnambiguousImplementation(t *testing.T) {
	valid := executableFunction()
	if err := canonicalDefinition(&valid, false); err != nil {
		t.Fatal(err)
	}
	for _, create := range []func() c.Definition{executableFunction, executableAction} {
		d := create()
		if d.Action != nil {
			d.Action.TargetSchemaIDs = []string{"associated-schema"}
		}
		if err := canonicalDefinition(&d, false); err != nil {
			t.Fatal("pure inline callable rejected", err)
		}
		for name, mutate := range map[string]func(*c.Callable){
			"external side effects": func(c *c.Callable) { c.SideEffects = "externalWrite" },
			"data side effects":     func(c *c.Callable) { c.SideEffects = "dataWrite" },
			"ambiguous artifact":    func(c *c.Callable) { c.ArtifactRef = "some-image" },
			"function and artifact": func(c *c.Callable) { c.Runtime, c.FunctionID, c.ArtifactRef = nil, "function-id", "some-image" },
			"nested function":       func(c *c.Callable) { c.FunctionID = "another-function" },
			"unsupported language":  func(c *c.Callable) { c.Runtime.Language = "js" },
			"non-string enum":       func(c *c.Callable) { c.Input[0].EnumID, c.Input[0].DataType = "enum-id", "int64" },
		} {
			t.Run(string(d.Kind)+"/"+name, func(t *testing.T) {
				candidate := create()
				callable := candidate.Function
				if candidate.Action != nil {
					callable = candidate.Action
				}
				mutate(callable)
				if err := canonicalDefinition(&candidate, false); err == nil {
					t.Fatal("unsupported implementation accepted")
				}
			})
		}
	}
	valid.Function.Runtime = nil
	if err := canonicalDefinition(&valid, false); err != nil {
		t.Fatal("metadata-only legacy definition rejected", err)
	}
}

func TestInlineActionRuntimeUpdatePreservesModeAndSignature(t *testing.T) {
	old := executableAction()
	copyDefinition := func(in c.Definition) c.Definition {
		raw, _ := json.Marshal(in)
		var out c.Definition
		_ = json.Unmarshal(raw, &out)
		return out
	}
	next := copyDefinition(old)
	next.Action.Runtime.Source += "\n# new source"
	next.Action.Runtime.Entrypoint, next.Action.Runtime.TimeoutSeconds = "calculate", 10
	next.Action.TargetSchemaIDs = []string{"associated-schema"}
	if err := compatibleUpdate(old, next); err != nil || old.Action.Runtime.TimeoutSeconds != 5 {
		t.Fatal("inline Action update rejected or changed current snapshot", err)
	}
	for name, mutate := range map[string]func(*c.Callable){
		"remove source":  func(callable *c.Callable) { callable.Runtime = nil },
		"bind function":  func(callable *c.Callable) { callable.Runtime, callable.FunctionID = nil, "function-id" },
		"bind artifact":  func(callable *c.Callable) { callable.Runtime, callable.ArtifactRef = nil, "artifact" },
		"output type":    func(callable *c.Callable) { callable.Output[0].DataType = "text" },
		"side effects":   func(callable *c.Callable) { callable.SideEffects = "dataWrite" },
		"input required": func(callable *c.Callable) { callable.Input[0].Required = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := copyDefinition(old)
			mutate(candidate.Action)
			if compatibleUpdate(old, candidate) == nil {
				t.Fatal("active Action mode or signature changed")
			}
		})
	}
	legacy := copyDefinition(old)
	legacy.Action.Runtime = nil
	if compatibleUpdate(legacy, next) != nil {
		t.Fatal("pure metadata-only Action cannot acquire source")
	}
	legacy.Action.FunctionID = "function-id"
	if compatibleUpdate(legacy, next) == nil {
		t.Fatal("active bound Action switched to inline source")
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

func TestCallableLanguagesCanChangeWithoutChangingActiveSignature(t *testing.T) {
	for _, create := range []func() c.Definition{executableFunction, executableAction} {
		old := create()
		for _, language := range []string{"javascript", "go"} {
			raw, _ := json.Marshal(old)
			var next c.Definition
			_ = json.Unmarshal(raw, &next)
			callable := next.Function
			if next.Kind == c.KindAction {
				callable = next.Action
			}
			callable.Runtime.Language = language
			callable.Runtime.Entrypoint = "Main"
			callable.Runtime.Source = "saved source is parsed only by the selected runtime"
			if err := canonicalDefinition(&next, false); err != nil {
				t.Fatalf("%s %s source rejected: %v", next.Kind, language, err)
			}
			if err := compatibleUpdate(old, next); err != nil {
				t.Fatalf("%s language update rejected: %v", next.Kind, err)
			}
			callable.Output[0].DataType = "text"
			if compatibleUpdate(old, next) == nil {
				t.Fatal("language change bypassed active signature lock")
			}
		}
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
