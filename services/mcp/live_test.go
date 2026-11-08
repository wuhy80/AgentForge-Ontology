package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Opt-in smoke against running services. It reads existing authorized models
// and rows, creates only a temporary personal connection, then revokes it.
func TestLiveMCPQueryAndRevocation(t *testing.T) {
	endpoint := os.Getenv("MCP_TEST_URL")
	if endpoint == "" {
		t.Skip("set MCP_TEST_URL and MCP_TEST_ACCOUNT_FILE for the live MCP smoke")
	}
	metadata := os.Getenv("MCP_TEST_METADATA_URL")
	execution := os.Getenv("MCP_TEST_EXECUTION_URL")
	if execution == "" {
		execution = "http://127.0.0.1:18082"
	}
	origin := os.Getenv("MCP_TEST_ORIGIN")
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		t.Fatal("MCP_TEST_ORIGIN is required")
	}
	var account struct{ Tenant, Username, Password string }
	raw, err := os.ReadFile(os.Getenv("MCP_TEST_ACCOUNT_FILE"))
	if err != nil {
		t.Fatal("read local test account")
	}
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}), &account); err != nil {
		t.Fatal("decode local test account")
	}
	httpClient := &http.Client{Timeout: 15 * time.Second}
	var cookie *http.Cookie
	var session c.Session
	request := func(method, path string, input, output any, want int) {
		t.Helper()
		body, _ := json.Marshal(input)
		r, err := http.NewRequest(method, metadata+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		// Same Host preservation as the workbench reverse proxy.
		r.Host = u.Host
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
			r.Header.Set("X-CSRF-Token", session.CSRFToken)
		}
		res, err := httpClient.Do(r)
		if err != nil {
			t.Fatal("live metadata request failed")
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d", method, path, res.StatusCode, want)
		}
		if output != nil && json.NewDecoder(res.Body).Decode(output) != nil {
			t.Fatal("invalid live response")
		}
		if path == "/metadata/auth/login" {
			if len(res.Cookies()) != 1 {
				t.Fatal("missing live session cookie")
			}
			cookie = res.Cookies()[0]
		}
	}
	request("POST", "/metadata/auth/login", c.LoginRequest{Tenant: account.Tenant, Username: account.Username, Password: account.Password}, &session, 200)
	var credential c.AccessConnectionCreated
	request("POST", "/metadata/auth/connections", c.AccessConnectionInput{Name: "MCP live smoke", Scopes: []string{ScopeDiscover, ScopeQuery, ScopeInvoke}, ExpiresInSeconds: 3600}, &credential, 201)
	revoked := false
	defer func() {
		if !revoked {
			request("DELETE", "/metadata/auth/connections/"+credential.Connection.ID, nil, nil, 204)
		}
		request("POST", "/metadata/auth/logout", nil, nil, 204)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for _, version := range []string{"2026-07-28", "2025-03-26"} {
		t.Run(version, func(t *testing.T) {
			client := mcp.NewClient(&mcp.Implementation{Name: "ontology-live-smoke", Version: "1"}, nil)
			transport := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Timeout: 15 * time.Second, Transport: bearerTransport{base: http.DefaultTransport, token: credential.AccessToken}}, DisableStandaloneSSE: true}
			connection, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: version})
			if err != nil {
				t.Fatal("live MCP connect failed:", err)
			}
			defer connection.Close()
			tools, err := connection.ListTools(ctx, nil)
			if err != nil || len(tools.Tools) != 13 {
				t.Fatal("expected all 13 MCP tools")
			}
			call := func(name string, arguments, output any) {
				t.Helper()
				result, err := connection.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
				if err != nil || result.IsError {
					t.Fatalf("live tool %s failed: %v", name, err)
				}
				if output != nil {
					raw, _ := json.Marshal(result.StructuredContent)
					if err := json.Unmarshal(raw, output); err != nil {
						t.Fatalf("decode tool %s", name)
					}
				}
			}
			call("get_context", map[string]any{}, nil)
			call("list_categories", map[string]any{"limit": 1}, nil)
			var callables c.CallableDirectory
			call("list_callables", map[string]any{"limit": 1}, &callables)
			var models c.QueryContext
			call("list_models", map[string]any{"limit": 1}, &models)
			if len(models.Models) == 0 {
				t.Fatal("live fixture requires one authorized current model")
			}
			model := models.Models[0]
			call("describe_model", map[string]any{"schemaId": model.SchemaID}, nil)
			fields := []string{}
			if len(model.Fields) > 0 {
				fields = append(fields, model.Fields[0].ID)
			}
			query := c.QuerySpec{From: c.Source{SchemaID: model.SchemaID}, Select: fields, Page: c.Page{Limit: 1}}
			var rows c.QueryResult
			call("query_objects", map[string]any{"query": query}, &rows)
			if len(rows.Data) > 0 {
				call("get_object", map[string]any{"schemaId": model.SchemaID, "entityId": rows.Data[0].ID}, nil)
			}
			if len(model.Fields) > 0 {
				quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
				text := "SELECT " + quote(model.Fields[0].Name) + " FROM " + quote(model.Category) + "." + quote(model.Name) + " LIMIT 1"
				var analysis c.OntologySQLAnalysis
				call("analyze_query", map[string]any{"text": text}, &analysis)
				if !analysis.Ready {
					t.Fatal("live OntologySQL analysis was not ready")
				}
				var sqlRows c.OntologySQLResult
				call("query_ontology_sql", map[string]any{"text": text}, &sqlRows)
				if len(sqlRows.Data) != len(rows.Data) {
					t.Fatal("MCP query paths returned different row counts")
				}
				if len(rows.Data) > 0 && string(rows.Data[0].Values[fields[0]]) != string(sqlRows.Data[0].Values[fields[0]]) {
					t.Fatal("MCP SQL changed a precise value")
				}
			}
			if len(callables.Data) > 0 && len(callables.Data[0].Input) == 0 && callables.Data[0].Kind == c.KindFunction {
				fn := callables.Data[0]
				call("describe_callable", map[string]any{"definitionId": fn.ID}, nil)
				arguments := map[string]any{"definitionId": fn.ID, "input": map[string]any{}, "idempotencyKey": c.NewID()}
				var invocation, replay c.Invocation
				call("invoke_function", arguments, &invocation)
				if invocation.Status != "succeeded" {
					t.Fatal("live pure computation did not succeed")
				}
				call("invoke_function", arguments, &replay)
				if replay.ID != invocation.ID {
					t.Fatal("MCP idempotency replay ran twice")
				}
				call("get_invocation", map[string]any{"invocationId": invocation.ID}, nil)
				wrongKind, err := connection.CallTool(ctx, &mcp.CallToolParams{Name: "invoke_action", Arguments: arguments})
				if err != nil || !wrongKind.IsError {
					t.Fatal("Action tool accepted a Function")
				}
				// Direct resource requests must preserve the current-only scope.
				body, _ := json.Marshal(c.InvocationRequest{DefinitionID: fn.ID, DraftETag: strings.Repeat("a", 64), Input: map[string]json.RawMessage{}})
				draftRequest, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(execution, "/")+"/execution/invocations", bytes.NewReader(body))
				draftRequest.Header.Set("Authorization", "Bearer "+credential.AccessToken)
				draftRequest.Header.Set("Content-Type", "application/json")
				draftRequest.Header.Set("Idempotency-Key", c.NewID())
				res, err := httpClient.Do(draftRequest)
				if err != nil {
					t.Fatal("direct external execution probe failed")
				}
				_, _ = io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode != http.StatusForbidden {
					t.Fatalf("external connection reached draft execution: status=%d", res.StatusCode)
				}
			}
		})
	}
	request("DELETE", "/metadata/auth/connections/"+credential.Connection.ID, nil, nil, 204)
	revoked = true
	req, _ := http.NewRequest("POST", endpoint, bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatal("revocation probe failed")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode != 401 {
		t.Fatal("revoked token was accepted")
	}
}
