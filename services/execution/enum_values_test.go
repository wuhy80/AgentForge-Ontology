package execution

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func enumExecutionFixture() (*fixture, c.Definition) {
	f := newFixture()
	enum := c.Definition{ID: "enum", TenantID: "tenant", Kind: c.KindEnum, Name: "状态", Enum: &c.Enum{Values: []c.EnumValue{{Code: "A", Label: "可用"}, {Code: "B", Label: "历史", Deprecated: true}}}}
	f.meta.set(enum)
	f.fn.Function.Input = []c.Property{{ID: "fn-in", Name: "输入", DataType: "string", Required: true, EnumID: enum.ID}}
	f.fn.Function.Output = []c.Property{{ID: "fn-out", Name: "结果", DataType: "string", Required: true, EnumID: enum.ID}}
	f.meta.set(f.fn)
	f.runner.run = func(_ context.Context, _ *c.ScriptRuntime, input map[string]json.RawMessage, _ string) (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{"结果": input["输入"]}, nil
	}
	return f, enum
}

func TestEnumInvocationUsesCurrentCodesAndRevokesHistoricalResults(t *testing.T) {
	f, enum := enumExecutionFixture()
	body := `{"definitionId":"function","input":{"fn-in":"A"}}`
	got := invocation(t, request(f.h, "POST", "/execution/invocations", "enum", body), 201)
	if got.Status != "succeeded" || string(got.Output["fn-out"]) != `"A"` {
		t.Fatal("valid enum computation failed")
	}
	for i, value := range []string{"B", "可用", "unknown"} {
		invalid := strings.Replace(body, `"A"`, `"`+value+`"`, 1)
		if w := request(f.h, "POST", "/execution/invocations", "invalid-"+strconv.Itoa(i), invalid); w.Code != 422 {
			t.Fatalf("invalid enum input accepted: %d", w.Code)
		}
	}
	if f.runner.calls.Load() != 1 {
		t.Fatal("invalid enum input ran code")
	}
	enum.Enum.Values[0].Label = "已改名"
	f.meta.set(enum)
	if w := request(f.h, "GET", "/execution/invocations/"+got.ID, "", ""); w.Code != 409 {
		t.Fatalf("enum change exposed old result: %d", w.Code)
	}
	if w := request(f.h, "POST", "/execution/invocations", "enum", body); w.Code != 409 || f.runner.calls.Load() != 1 {
		t.Fatalf("enum change reran old key: %d", w.Code)
	}
	// Changes during execution also prevent delivery, without treating a stale
	// snapshot as authority for an output or a future replay.
	f.runner.run = func(_ context.Context, _ *c.ScriptRuntime, input map[string]json.RawMessage, _ string) (map[string]json.RawMessage, error) {
		enum.Enum.Values[0].Label = "再次改名"
		f.meta.set(enum)
		return map[string]json.RawMessage{"结果": input["输入"]}, nil
	}
	if w := request(f.h, "POST", "/execution/invocations", "changed-during-run", body); w.Code != 409 {
		t.Fatalf("changed enum delivered stale result: %d", w.Code)
	}
}

func TestEnumInvocationRejectsInvalidOutputAndSupportsDraftBinding(t *testing.T) {
	f, _ := enumExecutionFixture()
	f.runner.run = func(context.Context, *c.ScriptRuntime, map[string]json.RawMessage, string) (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{"结果": json.RawMessage(`"B"`)}, nil
	}
	got := invocation(t, request(f.h, "POST", "/execution/invocations", "bad-output", `{"definitionId":"function","input":{"fn-in":"A"}}`), 201)
	if got.Status != "failed" || got.Error == nil || got.Error.Code != "invalid_output" {
		t.Fatal("deprecated enum output accepted")
	}
	f, _ = enumExecutionFixture()
	f.meta.setDraft(f.fn)
	body := mustJSON(c.InvocationRequest{DefinitionID: f.fn.ID, DraftETag: digest(f.fn), Input: map[string]json.RawMessage{"fn-in": json.RawMessage(`"A"`)}})
	if got := invocation(t, request(f.h, "POST", "/execution/invocations", "draft-enum", body), 201); got.Mode != "draft" || got.Status != "succeeded" {
		t.Fatal("enum draft did not execute")
	}
}
