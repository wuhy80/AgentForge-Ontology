package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPythonRuntimeAcceptsOnlyBoundedDeclaredImplementation(t *testing.T) {
	valid := ScriptRuntime{Language: "python", Source: "def main(inputs):\n    return {}\n", Entrypoint: "main", TimeoutSeconds: 5}
	if err := ValidateScriptRuntime(&valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ScriptRuntime){
		"unknown language":      func(r *ScriptRuntime) { r.Language = "ruby" },
		"empty source":          func(r *ScriptRuntime) { r.Source = "  \n" },
		"oversize source":       func(r *ScriptRuntime) { r.Source = strings.Repeat("x", MaxScriptSourceBytes+1) },
		"invalid utf8":          func(r *ScriptRuntime) { r.Source = string([]byte{0xff}) },
		"nul":                   func(r *ScriptRuntime) { r.Source += "\x00" },
		"expression entrypoint": func(r *ScriptRuntime) { r.Entrypoint = "module.main()" },
		"dunder":                func(r *ScriptRuntime) { r.Entrypoint = "__import__" },
		"keyword":               func(r *ScriptRuntime) { r.Entrypoint = "class" },
		"zero timeout":          func(r *ScriptRuntime) { r.TimeoutSeconds = 0 },
		"unbounded timeout":     func(r *ScriptRuntime) { r.TimeoutSeconds = 31 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if ValidateScriptRuntime(&candidate) == nil {
				t.Fatal("unsupported runtime silently accepted")
			}
		})
	}
	if ValidateScriptRuntime(nil) == nil {
		t.Fatal("missing implementation accepted")
	}
}

func TestScriptRuntimeLanguagesAndReservedEntrypoints(t *testing.T) {
	for language, entrypoint := range map[string]string{"python": "main", "javascript": "main", "go": "Main"} {
		if err := ValidateScriptRuntime(&ScriptRuntime{Language: language, Source: "source validated at execution", Entrypoint: entrypoint, TimeoutSeconds: 30}); err != nil {
			t.Fatalf("%s: %v", language, err)
		}
	}
	for language, words := range map[string][]string{"python": {"class", "return"}, "javascript": {"import", "await", "eval"}, "go": {"main", "init", "_", "func", "__ontologyMain"}} {
		for _, entrypoint := range words {
			if ValidateScriptRuntime(&ScriptRuntime{Language: language, Source: "source", Entrypoint: entrypoint, TimeoutSeconds: 1}) == nil {
				t.Fatalf("%s accepted reserved %s", language, entrypoint)
			}
		}
	}
}

func TestCallableSignaturesBindNamesAndValueSemanticsAcrossIDs(t *testing.T) {
	left := &Callable{Input: []Property{{ID: "action-a", Name: "金额", DataType: "decimal", Precision: 18, Scale: 2, Required: true}, {ID: "action-b", Name: "备注", DataType: "string", Nullable: true, Default: json.RawMessage(`""`)}}, Output: []Property{{ID: "action-result", Name: "结果", DataType: "string"}}}
	right := &Callable{Input: []Property{left.Input[1], left.Input[0]}, Output: append([]Property{}, left.Output...)}
	right.Input[0].ID, right.Input[1].ID, right.Output[0].ID = "function-a", "function-b", "function-result"
	if !CallableSignaturesEqual(left, right) {
		t.Fatal("equivalent signatures with independent IDs and order rejected")
	}
	for _, mutate := range []func(*Callable){
		func(c *Callable) { c.Input[0].Name = "说明" },
		func(c *Callable) { c.Input[0].Nullable = false },
		func(c *Callable) { c.Input[0].Default = nil },
		func(c *Callable) { c.Input[1].Scale = 3 },
		func(c *Callable) { c.Output[0].DataType = "text" },
	} {
		raw, _ := json.Marshal(right)
		var candidate Callable
		_ = json.Unmarshal(raw, &candidate)
		mutate(&candidate)
		if CallableSignaturesEqual(left, &candidate) {
			t.Fatal("different value contract accepted")
		}
	}
	if left.Input[0].ID != "action-a" || right.Input[0].ID != "function-a" {
		t.Fatal("signature comparison mutated stable identifiers")
	}
}
