package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCallableTestCasesUseStableInputsAndExactScalars(t *testing.T) {
	callable := &Callable{Input: []Property{{ID: "n", Name: "数量", DataType: "int64", Required: true}, {ID: "d", Name: "金额", DataType: "decimal", Precision: 25, Scale: 2}, {ID: "b", Name: "确认", DataType: "boolean"}, {ID: "s", Name: "说明", DataType: "string", Nullable: true}}, TestCases: []CallableTestCase{{ID: "sample", Name: "大数边界", Input: map[string]json.RawMessage{"n": json.RawMessage(`"9223372036854775807"`), "d": json.RawMessage(`"9007199254740993.10"`), "b": json.RawMessage(`true`), "s": json.RawMessage(`null`)}}}}
	if err := ValidateCallableTestCases(callable); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ key, raw string }{{"n", `9223372036854775807`}, {"n", `"9223372036854775808"`}, {"d", `9007199254740993.10`}, {"d", `"0.001"`}, {"b", `"true"`}, {"s", `{}`}, {"n", `null`}, {"数量", `"1"`}} {
		copy := *callable
		copy.TestCases = []CallableTestCase{{ID: "sample", Name: "类型检查", Input: map[string]json.RawMessage{test.key: json.RawMessage(test.raw)}}}
		if err := ValidateCallableTestCases(&copy); err == nil {
			t.Errorf("accepted %s=%s", test.key, test.raw)
		}
	}
	callable.TestCases[0].Input = map[string]json.RawMessage{}
	if err := ValidateCallableTestCases(callable); err != nil {
		t.Fatal("partial sample should allow required inputs to be filled before running", err)
	}
}

func TestCallableTestCaseBudgetsAndIdentity(t *testing.T) {
	base := &Callable{Input: []Property{{ID: "text", Name: "内容", DataType: "longtext"}}, TestCases: []CallableTestCase{{ID: "sample", Name: "样例", Input: map[string]json.RawMessage{}}}}
	for name, modify := range map[string]func(*Callable){
		"name":      func(c *Callable) { c.TestCases[0].Name = strings.Repeat("例", 81) },
		"control":   func(c *Callable) { c.TestCases[0].Name = "例\n" },
		"id":        func(c *Callable) { c.TestCases[0].ID = "../test" },
		"null":      func(c *Callable) { c.TestCases[0].Input = nil },
		"duplicate": func(c *Callable) { c.TestCases = append(c.TestCases, c.TestCases[0]) },
		"count":     func(c *Callable) { c.TestCases = make([]CallableTestCase, 11) },
		"size": func(c *Callable) {
			raw, _ := json.Marshal(strings.Repeat("x", MaxCallableTestCaseBytes))
			c.TestCases[0].Input["text"] = raw
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var copy Callable
			_ = json.Unmarshal(raw, &copy)
			modify(&copy)
			if err := ValidateCallableTestCases(&copy); err == nil {
				t.Fatal("accepted invalid sample")
			}
		})
	}
}
