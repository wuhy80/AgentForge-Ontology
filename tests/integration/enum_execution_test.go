package integration_test

import (
	"encoding/json"
	"fmt"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Runs in the real three-service TLS/Cookie/OPA/container fixture, after its
// independent per-language and transactional SDK checks.
func verifyEnumRuntimeHTTP(t *testing.T, admin, other *executionHTTP, execURL, metaURL, dataURL string, publish func(c.Definition) c.CurrentDefinition) {
	t.Helper()
	states := publish(c.Definition{Kind: c.KindEnum, Name: "执行参数状态", Enum: &c.Enum{Values: []c.EnumValue{{Code: "ready", Label: "待办"}, {Code: "legacy", Label: "旧值"}}}})
	property := func(name string) c.Property {
		return c.Property{Name: name, DataType: "string", Required: true, EnumID: states.Definition.ID}
	}
	sources := []struct{ language, entrypoint, source string }{
		{"python", "main", "def main(inputs):\n    return {'结果': inputs['状态']}\n"},
		{"javascript", "main", "function main(inputs) { return { '结果': inputs['状态'] }; }"},
		{"go", "Main", "package main\nfunc Main(inputs map[string]any) (map[string]any,error) { return map[string]any{\"结果\":inputs[\"状态\"]},nil }\n"},
	}
	var first c.Invocation
	var firstIntent c.InvocationRequest
	var firstKey string
	for _, source := range sources {
		fn := publish(c.Definition{Kind: c.KindFunction, Name: source.language + "枚举回传", Function: &c.Callable{SideEffects: "none", Input: []c.Property{property("状态")}, Output: []c.Property{property("结果")}, Runtime: &c.ScriptRuntime{Language: source.language, Entrypoint: source.entrypoint, Source: source.source, TimeoutSeconds: 30}}})
		bound := publish(c.Definition{Kind: c.KindAction, Name: source.language + "枚举绑定", Action: &c.Callable{SideEffects: "none", FunctionID: fn.Definition.ID, Input: []c.Property{property("状态")}, Output: []c.Property{property("结果")}}})
		inline := publish(c.Definition{Kind: c.KindAction, Name: source.language + "枚举源码", Action: &c.Callable{SideEffects: "none", Input: []c.Property{property("状态")}, Output: []c.Property{property("结果")}, Runtime: &c.ScriptRuntime{Language: source.language, Entrypoint: source.entrypoint, Source: source.source, TimeoutSeconds: 30}}})
		for _, definition := range []c.Definition{fn.Definition, bound.Definition, inline.Definition} {
			callable := definition.Function
			if callable == nil {
				callable = definition.Action
			}
			intent := c.InvocationRequest{DefinitionID: definition.ID, Input: map[string]json.RawMessage{callable.Input[0].ID: raw("ready")}}
			key := c.NewID()
			var result c.Invocation
			admin.request("POST", execURL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": key}, 201, &result)
			if result.Status != "succeeded" || string(result.Output[callable.Output[0].ID]) != `"ready"` {
				t.Fatalf("%s enum invocation did not return a stable code", source.language)
			}
			other.request("GET", execURL+"/execution/invocations/"+result.ID, nil, nil, 404, nil)
			if first.ID == "" {
				first, firstIntent, firstKey = result, intent, key
			}
			intent.Input[callable.Input[0].ID] = raw("待办")
			admin.request("POST", execURL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": c.NewID()}, 422, nil)
		}
	}
	// An enum parameter crosses the Execution -> Data transaction bridge as
	// resolved dependency evidence while preserving the caller's business rights.
	schema := publish(c.Definition{Kind: c.KindSchema, Name: "枚举写计划对象", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{property("状态")}}})
	field := schema.Definition.Schema.Properties[0].ID
	write := publish(c.Definition{Kind: c.KindFunction, Name: "枚举事务写入", Function: &c.Callable{SideEffects: "dataWrite", DataWrite: []c.DataWriteCapability{{SchemaID: schema.Definition.ID, Operations: []string{"create"}}}, Input: []c.Property{property("状态")}, Output: []c.Property{property("结果")}, Runtime: &c.ScriptRuntime{Language: "python", Entrypoint: "main", TimeoutSeconds: 10, Source: fmt.Sprintf("def main(inputs,ctx):\n    ctx.data.create(%q,{%q:inputs['状态']},'created')\n    return {'结果':inputs['状态']}\n", schema.Definition.ID, field)}}})
	writeInput := write.Definition.Function.Input[0].ID
	var written c.Invocation
	admin.request("POST", execURL+"/execution/invocations", c.InvocationRequest{DefinitionID: write.Definition.ID, Input: map[string]json.RawMessage{writeInput: raw("ready")}}, map[string]string{"Idempotency-Key": c.NewID()}, 201, &written)
	if written.Status != "succeeded" || written.Transaction == nil || !written.Transaction.Committed {
		t.Fatal("enum dependency evidence did not cross the write bridge")
	}
	var objects c.QueryResult
	admin.request("POST", dataURL+"/data/query", c.QuerySpec{From: c.Source{SchemaID: schema.Definition.ID}, Select: []string{field}, Page: c.Page{Limit: 10}}, nil, 200, &objects)
	if len(objects.Data) != 1 || string(objects.Data[0].Values[field]) != `"ready"` {
		t.Fatal("enum transaction did not commit the stable business code")
	}
	// Changed labels and deprecation invalidate old execution snapshots, rather
	// than serving cached output with an outdated dependency interpretation.
	states.Definition.Enum.Values[0].Label = "待处理"
	states.Definition.Enum.Values[1].Deprecated = true
	var draft c.CurrentDefinition
	headers := admin.request("PUT", metaURL+"/metadata/definitions/"+states.Definition.ID+"/draft", states.Definition, map[string]string{"If-Match": `"` + states.ETag + `"`}, 200, &draft)
	admin.request("POST", metaURL+"/metadata/definitions/"+states.Definition.ID+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	admin.request("GET", execURL+"/execution/invocations/"+first.ID, nil, nil, 409, nil)
	admin.request("POST", execURL+"/execution/invocations", firstIntent, map[string]string{"Idempotency-Key": firstKey}, 409, nil)
	for id := range firstIntent.Input {
		firstIntent.Input[id] = raw("legacy")
	}
	admin.request("POST", execURL+"/execution/invocations", firstIntent, map[string]string{"Idempotency-Key": c.NewID()}, 422, nil)
}
