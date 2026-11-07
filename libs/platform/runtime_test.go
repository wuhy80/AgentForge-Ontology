package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func filter(field, value string) *c.Expression {
	raw, _ := json.Marshal(value)
	return &c.Expression{Op: "eq", Field: field, Value: raw}
}

func rule(id, operation string) c.PermissionRule {
	return c.PermissionRule{ID: id, RoleID: "role-" + id, Effect: "allow", Operation: operation, ResourceID: "schema", ReadFields: []string{"field-b", "field-a"}, WriteFields: []string{"field-a"}}
}

func runtime(rules ...c.PermissionRule) *c.RuntimePolicy {
	return &c.RuntimePolicy{State: "current-state", Rules: rules}
}

func TestRuntimeFieldAndRowCoupling(t *testing.T) {
	a, b := rule("a", "instance.read"), rule("b", "instance.read")
	a.RowFilter, b.RowFilter = filter("department", "A"), filter("department", "B")
	request := c.AuthorizationRequest{Operation: "instance.read", ResourceID: "schema"}
	decision, err := mergeRuntime(runtime(a, b), request)
	if err != nil || !decision.Allow || decision.RowFilter.Op != "or" || len(decision.RowFilter.Args) != 2 || strings.Join(decision.ReadFields, ",") != "field-a,field-b" {
		t.Fatalf("merged decision=%#v error=%v", decision, err)
	}
	b.ReadFields = []string{"field-a", "field-b", "field-a"}
	if _, err := mergeRuntime(runtime(a, b), request); err != nil {
		t.Fatal("equivalent field sets rejected", err)
	}
	b.ReadFields = []string{"field-a"}
	if _, err := mergeRuntime(runtime(a, b), request); err == nil {
		t.Fatal("different field scopes were independently unioned")
	}
	b.ReadFields, b.WriteFields = a.ReadFields, []string{"*"}
	if _, err := mergeRuntime(runtime(a, b), request); err == nil {
		t.Fatal("different write field scopes accepted")
	}
	b.WriteFields, b.RowFilter = a.WriteFields, nil
	decision, err = mergeRuntime(runtime(a, b), request)
	if err != nil || !decision.Allow || decision.RowFilter != nil || decision.NewRowFilter != nil {
		t.Fatal("unconditional row allow did not cover all rows", err)
	}
	a.ReadFields, b.ReadFields = []string{"*"}, []string{"*", "field-a"}
	if _, err := mergeRuntime(runtime(a, b), request); err != nil {
		t.Fatal("wildcard field sets rejected", err)
	}
}

func TestRuntimeDenyAndNoMatchingRules(t *testing.T) {
	a, deny := rule("allow", "instance.read"), rule("deny", "instance.read")
	deny.Effect, deny.ReadFields, deny.WriteFields = "deny", nil, nil
	for _, rules := range [][]c.PermissionRule{{a, deny}, {deny, a}, nil} {
		decision, err := mergeRuntime(runtime(rules...), c.AuthorizationRequest{Operation: "instance.read", ResourceID: "schema"})
		if err != nil || decision.Allow {
			t.Fatalf("deny failed: %#v %v", decision, err)
		}
	}
	deny.ResourceID = "other-schema"
	decision, err := mergeRuntime(runtime(a, deny), c.AuthorizationRequest{Operation: "instance.read", ResourceID: "schema"})
	if err != nil || !decision.Allow {
		t.Fatal("unrelated deny blocked matching resource", err)
	}
	deny.ResourceID = "*"
	decision, err = mergeRuntime(runtime(a, deny), c.AuthorizationRequest{Operation: "instance.read", ResourceID: "schema"})
	if err != nil || decision.Allow {
		t.Fatal("wildcard deny not applied", err)
	}
}

func TestRuntimePatchDoesNotCrossPairRoles(t *testing.T) {
	a, b := rule("a", "instance.patch"), rule("b", "instance.patch")
	a.RowFilter, a.NewRowFilter = filter("department", "A"), filter("department", "B")
	b.RowFilter, b.NewRowFilter = filter("department", "C"), filter("department", "D")
	request := c.AuthorizationRequest{Operation: "instance.patch", ResourceID: "schema"}
	_, err := mergeRuntime(runtime(a, b), request)
	var failure *AccessError
	if !errors.As(err, &failure) || failure.Status != 422 {
		t.Fatalf("unsafe patch combination not rejected: %v", err)
	}
	b.RowFilter = a.RowFilter
	decision, err := mergeRuntime(runtime(a, b), request)
	if err != nil || decision.NewRowFilter.Op != "or" || expressionKey(decision.RowFilter) != expressionKey(a.RowFilter) {
		t.Fatal("same old condition should safely OR new conditions", err)
	}
	b.RowFilter, b.NewRowFilter = filter("department", "C"), a.NewRowFilter
	decision, err = mergeRuntime(runtime(a, b), request)
	if err != nil || decision.RowFilter.Op != "or" || expressionKey(decision.NewRowFilter) != expressionKey(a.NewRowFilter) {
		t.Fatal("same new condition should safely OR old conditions", err)
	}
	a.NewRowFilter, b.NewRowFilter = nil, nil
	if _, err := mergeRuntime(runtime(a, b), request); err == nil {
		t.Fatal("implicit new=old filters lost their role coupling")
	}
}

func TestPermissionPolicyRejectsUnsupportedAndOversizedRules(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*c.PermissionRule)
	}{
		{"identity escalation", func(r *c.PermissionRule) { r.Operation = "identity.users.edit" }},
		{"unknown effect", func(r *c.PermissionRule) { r.Effect = "whatever" }},
		{"blank role", func(r *c.PermissionRule) { r.RoleID = "" }},
		{"blank resource", func(r *c.PermissionRule) { r.ResourceID = "" }},
		{"deny fields", func(r *c.PermissionRule) { r.Effect = "deny" }},
		{"deny rows", func(r *c.PermissionRule) {
			r.Effect = "deny"
			r.ReadFields = nil
			r.WriteFields = nil
			r.RowFilter = filter("field", "x")
		}},
		{"metadata rows", func(r *c.PermissionRule) { r.Operation = "definition.read"; r.RowFilter = filter("field", "x") }},
		{"unknown operator", func(r *c.PermissionRule) {
			r.RowFilter = &c.Expression{Op: "sql", Field: "field", Value: json.RawMessage(`"x"`)}
		}},
		{"malformed logical", func(r *c.PermissionRule) {
			r.RowFilter = &c.Expression{Op: "and", Field: "field", Args: []c.Expression{*filter("field", "x")}}
		}},
		{"null comparison", func(r *c.PermissionRule) {
			r.RowFilter = &c.Expression{Op: "eq", Field: "field", Value: json.RawMessage(`null`)}
		}},
		{"object comparison", func(r *c.PermissionRule) {
			r.RowFilter = &c.Expression{Op: "eq", Field: "field", Value: json.RawMessage(`{"x":1}`)}
		}},
		{"numeric prefix", func(r *c.PermissionRule) {
			r.RowFilter = &c.Expression{Op: "startsWith", Field: "field", Value: json.RawMessage(`1`)}
		}},
		{"empty in", func(r *c.PermissionRule) {
			r.RowFilter = &c.Expression{Op: "in", Field: "field", Value: json.RawMessage(`[]`)}
		}},
		{"in budget", func(r *c.PermissionRule) {
			r.RowFilter = &c.Expression{Op: "in", Field: "field", Value: json.RawMessage(`[` + strings.Repeat(`1,`, 100) + `1]`)}
		}},
		{"deep expression", func(r *c.PermissionRule) {
			e := filter("field", "x")
			for i := 0; i < 11; i++ {
				e = &c.Expression{Op: "not", Args: []c.Expression{*e}}
			}
			r.RowFilter = e
		}},
		{"nodes budget", func(r *c.PermissionRule) {
			e := &c.Expression{Op: "or"}
			for i := 0; i < 100; i++ {
				e.Args = append(e.Args, *filter("field", "x"))
			}
			r.RowFilter = e
		}},
		{"fields budget", func(r *c.PermissionRule) { r.ReadFields = make([]string, 257) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := rule("a", "instance.read")
			test.mutate(&r)
			if ValidatePermissionPolicy(c.PermissionPolicy{Rules: []c.PermissionRule{r}}) == nil {
				t.Fatal("unsupported policy accepted")
			}
		})
	}
	if ValidatePermissionPolicy(c.PermissionPolicy{Rules: make([]c.PermissionRule, 513)}) == nil {
		t.Fatal("policy rule budget missing")
	}
	r := rule("a", "instance.read")
	if ValidatePermissionPolicy(c.PermissionPolicy{Rules: []c.PermissionRule{r, r}}) == nil {
		t.Fatal("duplicate rule IDs accepted")
	}
	r.Operation, r.ReadFields, r.WriteFields = "definition.edit", []string{"*"}, []string{"*"}
	if err := ValidatePermissionPolicy(c.PermissionPolicy{Rules: []c.PermissionRule{r}}); err != nil {
		t.Fatal("bootstrap metadata rule rejected", err)
	}
}

func TestRuntimeMergedFilterBudget(t *testing.T) {
	var rules []c.PermissionRule
	for i := 0; i < 100; i++ {
		r := rule(string(rune('a'+i)), "instance.read")
		r.RowFilter = filter("field", string(rune('a'+i)))
		rules = append(rules, r)
	}
	if _, err := mergeRuntime(runtime(rules...), c.AuthorizationRequest{Operation: "instance.read", ResourceID: "schema"}); err == nil {
		t.Fatal("merged expression budget not enforced")
	}
}

func TestEvaluateRuntimeAlwaysCallsOPAWithBoundGrant(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/data/ontology/decision" || r.Method != "POST" || Bearer(r) != serviceToken {
			t.Error("incorrect OPA request")
		}
		var wire struct {
			Input struct {
				Principal c.Principal            `json:"principal"`
				Request   c.AuthorizationRequest `json:"request"`
				Runtime   struct {
					State, TenantID, Subject, Operation, ResourceID string
					Grant                                           c.Decision
				} `json:"runtime"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		input := wire.Input
		if input.Runtime.State != "current-state" || input.Runtime.TenantID != "tenant" || input.Runtime.Subject != "user" || input.Runtime.Operation != "instance.read" || input.Runtime.ResourceID != "schema" || input.Principal.Subject != "user" || input.Request.ResourceID != "schema" {
			t.Error("runtime not bound to current identity/request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": input.Runtime.Grant})
	}))
	defer server.Close()
	opa := &OPAAuthorizer{URL: server.URL, BearerToken: serviceToken}
	for _, rules := range [][]c.PermissionRule{{rule("a", "instance.read")}, nil} {
		decision, err := EvaluateRuntimePolicy(context.Background(), opa, runtime(rules...), c.Principal{Subject: "user", TenantID: "tenant"}, c.AuthorizationRequest{Operation: "instance.read", ResourceID: "schema"})
		if err != nil || decision.Allow != (len(rules) > 0) {
			t.Fatalf("decision=%#v error=%v", decision, err)
		}
	}
	if calls != 2 {
		t.Fatal("OPA was bypassed")
	}
}

func TestEvaluateRuntimeRejectsInvalidOPAResponses(t *testing.T) {
	for _, test := range []struct {
		name, body string
		wantError  bool
	}{
		{"deny", `{"result":{"allow":false}}`, false},
		{"undefined", `{}`, true},
		{"null", `{"result":null}`, true},
		{"malformed", `{`, true},
		{"trailing", `{"result":{"allow":false}} {}`, true},
		{"missing state", `{"result":{"allow":true}}`, true},
		{"old state", `{"result":{"allow":true,"state":"old-state"}}`, true},
		{"widened fields", `{"result":{"allow":true,"state":"current-state","readFields":["*"],"writeFields":["field-a"]}}`, true},
		{"oversized", strings.Repeat(" ", 1<<20) + `{}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			decision, err := EvaluateRuntimePolicy(context.Background(), &OPAAuthorizer{URL: server.URL}, runtime(rule("a", "instance.read")), c.Principal{Subject: "user", TenantID: "tenant"}, c.AuthorizationRequest{Operation: "instance.read", ResourceID: "schema"})
			if decision.Allow || (err != nil) != test.wantError {
				t.Fatalf("decision=%#v error=%v", decision, err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	decision, err := EvaluateRuntimePolicy(context.Background(), &OPAAuthorizer{URL: server.URL}, runtime(rule("a", "instance.read")), c.Principal{Subject: "user", TenantID: "tenant"}, c.AuthorizationRequest{Operation: "instance.read", ResourceID: "schema"})
	if err == nil || decision.Allow {
		t.Fatal("OPA outage did not fail closed")
	}
}

func TestExpressionKeyKeepsDecimalPrecision(t *testing.T) {
	a := &c.Expression{Op: "eq", Field: "decimal", Value: json.RawMessage(`9007199254740993.000`)}
	b := &c.Expression{Op: "eq", Field: "decimal", Value: json.RawMessage(`9007199254740993`)}
	if expressionKey(a) != expressionKey(b) {
		t.Fatal("equivalent decimal forms differ")
	}
	b.Value = json.RawMessage(`9007199254740992.000`)
	if expressionKey(a) == expressionKey(b) {
		t.Fatal("decimal permissions lost precision")
	}
}

func TestRuntimeWithRealOPA(t *testing.T) {
	base := os.Getenv("ONTOLOGY_TEST_OPA_URL")
	if base == "" {
		t.Skip("set ONTOLOGY_TEST_OPA_URL for actual OPA integration")
	}
	opa := &OPAAuthorizer{URL: base}
	r := rule("a", "instance.read")
	r.RowFilter = &c.Expression{Op: "eq", Field: "decimal", Value: json.RawMessage(`9007199254740993.000`)}
	p := c.Principal{Subject: "user", TenantID: "tenant"}
	request := c.AuthorizationRequest{Operation: "instance.read", ResourceID: "schema"}
	decision, err := EvaluateRuntimePolicy(context.Background(), opa, runtime(r), p, request)
	if err != nil || !decision.Allow || expressionKey(decision.RowFilter) != expressionKey(r.RowFilter) {
		t.Fatalf("actual OPA allow failed: %#v %v", decision, err)
	}
	decision, err = EvaluateRuntimePolicy(context.Background(), opa, runtime(), p, request)
	if err != nil || decision.Allow {
		t.Fatalf("actual OPA empty-runtime deny failed: %#v %v", decision, err)
	}
	r.Operation, r.RowFilter = "organization.read", nil
	request.Operation, request.ResourceID = "organization.read", ""
	r.ResourceID = "*"
	decision, err = EvaluateRuntimePolicy(context.Background(), opa, runtime(r), p, request)
	if err != nil || !decision.Allow {
		t.Fatalf("actual OPA no-resource allow failed: %#v %v", decision, err)
	}
}
