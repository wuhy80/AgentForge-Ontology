package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestPermissionReferencesAreTypedAndCannotEscapeAuthority(t *testing.T) {
	user := c.PermissionValueRef{Source: "user", Attribute: "id"}
	object := c.PermissionValueRef{Source: "object", SchemaID: "source", FieldID: "department", MatchFieldID: "owner", MatchUserAttribute: "id"}
	for _, ref := range []c.PermissionValueRef{user, object, {Source: "object", SchemaID: "source", FieldID: "department", InstanceID: "instance"}} {
		r := rule("a", "instance.query")
		r.RowFilter = &c.Expression{Op: "eq", Field: "owner", ValueRef: &ref}
		if err := ValidatePermissionPolicy(c.PermissionPolicy{Rules: []c.PermissionRule{r}}); err != nil {
			t.Fatal(err)
		}
		if _, err := mergeRuntime(runtime(r), c.AuthorizationRequest{Operation: "instance.query", ResourceID: "schema"}); err == nil {
			t.Fatal("unresolved reference escaped authority")
		}
		r.RowFilter.Value = json.RawMessage(`"forged"`)
		if ValidatePermissionPolicy(c.PermissionPolicy{Rules: []c.PermissionRule{r}}) == nil {
			t.Fatal("mixed literal/reference accepted")
		}
	}
	for _, test := range []struct {
		ref c.PermissionValueRef
		op  string
	}{
		{c.PermissionValueRef{Source: "browser", Attribute: "id"}, "eq"},
		{c.PermissionValueRef{Source: "user", Attribute: "administrator"}, "eq"},
		{c.PermissionValueRef{Source: "user", Attribute: "id", SchemaID: "smuggled"}, "eq"},
		{c.PermissionValueRef{Source: "user", Attribute: "roleIds"}, "eq"},
		{user, "in"}, {user, "isNull"},
		{c.PermissionValueRef{Source: "object", SchemaID: "source", FieldID: "dept"}, "eq"},
		{c.PermissionValueRef{Source: "object", SchemaID: "source", FieldID: "dept", InstanceID: "id", MatchFieldID: "owner", MatchUserAttribute: "id"}, "eq"},
		{c.PermissionValueRef{Source: "object", SchemaID: "source", FieldID: "dept", InstanceID: "id", Mode: "set"}, "in"},
		{c.PermissionValueRef{Source: "object", SchemaID: "source", FieldID: "dept", MatchFieldID: "owner", MatchUserAttribute: "displayName"}, "eq"},
	} {
		if ValidatePermissionValueRef(test.ref, test.op) == nil {
			t.Fatalf("unsafe reference accepted: source=%s op=%s", test.ref.Source, test.op)
		}
	}
	if ValidatePermissionValueRef(c.PermissionValueRef{Source: "user", Attribute: "userGroupIds"}, "notIn") != nil {
		t.Fatal("group set rejected")
	}
	object.Mode = "set"
	if ValidatePermissionValueRef(object, "in") != nil {
		t.Fatal("object set rejected")
	}
}

func TestResolvedContextSetSurvivesRuntimeMerge(t *testing.T) {
	r := rule("a", "instance.query")
	r.RowFilter = &c.Expression{Op: "in", Field: "owner", Value: json.RawMessage(`["role"]`)}
	if err := ValidatePermissionPolicy(c.PermissionPolicy{Rules: []c.PermissionRule{r}}); err != nil {
		t.Fatal("literal policy", err)
	}
	decision, err := mergeRuntime(runtime(r), c.AuthorizationRequest{Operation: "instance.query", ResourceID: "schema"})
	if err != nil || !decision.Allow || string(decision.RowFilter.Value) != `["role"]` {
		t.Fatal("set runtime merge", err)
	}
}

func TestOPAResultMustRetainContextEvidence(t *testing.T) {
	r := rule("a", "instance.read")
	r.RowFilter = filter("department", "D1")
	for _, tamper := range []bool{false, true} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			var input struct {
				Input struct {
					Runtime struct {
						Grant c.Decision `json:"grant"`
					} `json:"runtime"`
				} `json:"input"`
			}
			if json.NewDecoder(request.Body).Decode(&input) != nil {
				t.Error("invalid input")
			}
			if tamper {
				input.Input.Runtime.Grant.ContextDigest = strings.Repeat("b", 64)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": input.Input.Runtime.Grant})
		}))
		runtime := runtime(r)
		runtime.ContextDigest, runtime.ContextSchemaIDs = strings.Repeat("a", 64), []string{"source"}
		decision, err := EvaluateRuntimePolicy(context.Background(), &OPAAuthorizer{URL: s.URL}, runtime, c.Principal{TenantID: "tenant", Subject: "user"}, c.AuthorizationRequest{Operation: "instance.read", ResourceID: "schema"})
		s.Close()
		if tamper && (err == nil || decision.Allow) {
			t.Fatal("OPA changed attribute dependency evidence")
		}
		if !tamper && (err != nil || !decision.Allow || decision.ContextDigest != runtime.ContextDigest) {
			t.Fatal("context evidence was lost", err)
		}
	}
}

func TestRemoteObjectContextUsesServiceIdentityAndTenant(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/internal/authorization/object-value" || r.Header.Get("X-Tenant-ID") != "tenant" || Bearer(r) != strings.Repeat("s", 32) || r.Header.Get("Cookie") != "" {
			t.Error("incorrect object context transport identity")
		}
		_ = json.NewEncoder(w).Encode(c.ObjectContextResult{Value: json.RawMessage(`"D1"`), Digest: strings.Repeat("a", 64)})
	}))
	defer s.Close()
	reader := &RemoteObjectContext{URL: s.URL, ServiceToken: strings.Repeat("s", 32)}
	out, err := reader.Read(context.Background(), "tenant", c.ObjectContextRequest{SchemaID: "schema", FieldID: "dept", InstanceID: "record"})
	if err != nil || string(out.Value) != `"D1"` {
		t.Fatal("attribute transport failed", err)
	}
}
