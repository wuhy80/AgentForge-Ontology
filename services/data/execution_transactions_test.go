package data

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type transactionSessionFunc func(context.Context, c.SessionCheck) (c.Session, error)

func (f transactionSessionFunc) CheckSession(ctx context.Context, check c.SessionCheck) (c.Session, error) {
	return f(ctx, check)
}

type transactionAuthorityFunc func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error)

func (f transactionAuthorityFunc) Authorize(ctx context.Context, p c.Principal, r c.AuthorizationRequest) (c.Decision, error) {
	return f(ctx, p, r)
}

func TestExecutionTransactionBridgeRequiresOriginalBrowserContext(t *testing.T) {
	key := strings.Repeat("k", 48)
	h := &handler{cfg: Config{ServiceToken: key}}
	for _, name := range []string{"service", "missing_session", "external", "safe_write", "wrong_key", "missing_origin"} {
		t.Run(name, func(t *testing.T) {
			body := c.ExecutionTransactionRequest{InvocationID: "invocation", Session: c.SessionCheck{Token: strings.Repeat("s", 64), Method: "POST", CSRFToken: strings.Repeat("c", 64), Origin: "https://workbench.test"}, Transaction: c.WriteTransactionRequest{IdempotencyKey: "execution:invocation"}}
			want := 403
			switch name {
			case "service":
				want = 401
			case "missing_session":
				body.Session.Token = ""
			case "external":
				body.Session.AccessToken = "mcp_" + strings.Repeat("a", 64)
			case "safe_write":
				body.Session.Method = "GET"
			case "wrong_key":
				body.Transaction.IdempotencyKey = "other"
			case "missing_origin":
				body.Session.Origin = ""
			}
			raw, _ := json.Marshal(body)
			request := httptest.NewRequest("POST", "/internal/execution/transactions", bytes.NewReader(raw))
			if name != "service" {
				request.Header.Set("Authorization", "Bearer "+key)
			}
			w := httptest.NewRecorder()
			h.executionTransactionHTTP(w, request)
			if w.Code != want {
				t.Fatalf("got %d want %d", w.Code, want)
			}
		})
	}
}

func TestExecutionTransactionPrincipalRechecksCSRFAndLiveSession(t *testing.T) {
	session := c.Session{User: c.User{ID: "user", TenantID: "tenant", Enabled: true}, CSRFToken: strings.Repeat("c", 64), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	h := &handler{cfg: Config{SessionChecker: transactionSessionFunc(func(context.Context, c.SessionCheck) (c.Session, error) { return session, nil })}}
	check := c.SessionCheck{Token: strings.Repeat("s", 64), Method: "POST", CSRFToken: session.CSRFToken}
	if p, err := h.executionTransactionPrincipal(context.Background(), check); err != nil || p.TenantID != "tenant" || p.Subject != "user" {
		t.Fatal("live identity lost")
	}
	check.CSRFToken = ""
	if _, err := h.executionTransactionPrincipal(context.Background(), check); err == nil {
		t.Fatal("missing original CSRF accepted")
	}
	check.Method = "GET"
	if _, err := h.executionTransactionPrincipal(context.Background(), check); err != nil {
		t.Fatal("safe status lookup needs no CSRF")
	}
	session.User.Enabled = false
	if _, err := h.executionTransactionPrincipal(context.Background(), check); err == nil {
		t.Fatal("disabled identity accepted")
	}
}

func TestExecutionTransactionGuardBindsDefinitionsInvokeAndDeclaredWrites(t *testing.T) {
	fn := c.Definition{ID: "function", TenantID: "tenant", Kind: c.KindFunction, Function: &c.Callable{SideEffects: "dataWrite", DataWrite: []c.DataWriteCapability{{SchemaID: "schema", Operations: []string{"create"}}}, Runtime: &c.ScriptRuntime{Language: "python", Entrypoint: "main", Source: "def main(inputs,ctx): return {}", TimeoutSeconds: 5}}}
	action := c.Definition{ID: "action", TenantID: "tenant", Kind: c.KindAction, Action: &c.Callable{FunctionID: fn.ID, SideEffects: "dataWrite", DataWrite: fn.Function.DataWrite}}
	definitions := map[string]c.Definition{fn.ID: fn, action.ID: action}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, ok := definitions[strings.TrimPrefix(r.URL.Path, "/metadata/definitions/")]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(c.CurrentDefinition{Definition: d, Status: "active", ETag: digest(d)})
	}))
	defer server.Close()
	denied := ""
	h := &handler{cfg: Config{MetadataURL: server.URL, Authorizer: transactionAuthorityFunc(func(_ context.Context, _ c.Principal, r c.AuthorizationRequest) (c.Decision, error) {
		return c.Decision{Allow: r.Operation != denied, State: "state"}, nil
	})}, client: server.Client()}
	p := c.Principal{TenantID: "tenant", Subject: "user"}
	valid := c.ExecutionTransactionRequest{DefinitionID: action.ID, ImplementationID: fn.ID, DefinitionDigest: digest(action), ImplementationDigest: digest(fn), Transaction: c.WriteTransactionRequest{Operations: []c.TransactionOperation{{SchemaID: "schema", Operation: "create"}}}}
	if err := h.checkExecutionTransaction(context.Background(), p, valid); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"root_invoke", "function_invoke", "snapshot", "scope", "unbound"} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(valid)
			var body c.ExecutionTransactionRequest
			_ = json.Unmarshal(raw, &body)
			switch name {
			case "root_invoke":
				denied = "action.invoke"
			case "function_invoke":
				denied = "function.invoke"
			case "snapshot":
				body.ImplementationDigest = "old"
			case "scope":
				body.Transaction.Operations[0].Operation = "delete"
			case "unbound":
				body.ImplementationID = action.ID
				body.ImplementationDigest = digest(action)
			}
			if err := h.checkExecutionTransaction(context.Background(), p, body); err == nil {
				t.Fatal("untrusted execution context accepted")
			}
			denied = ""
		})
	}
}
