package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type fakeChecker struct {
	session c.Session
	err     error
	checks  []c.SessionCheck
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func TestOfficialSDKClientDiscoversOntologyTools(t *testing.T) {
	token := "mcp_0000000000000000000000000000000000000000000000000000000000000000"
	checker := &fakeChecker{session: c.Session{User: c.User{ID: "u1", TenantID: "t1", Enabled: true}, ExpiresAt: time.Now().Add(time.Hour).Unix(), AccessScopes: []string{platform.ScopeDiscover}, AccessTokenID: "a1"}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata/capabilities" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"invocation":false}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	defer upstream.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The gateway is mounted below, so this server is replaced after creation.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer backend.Close()
	h, err := NewHandler(Config{MetadataURL: upstream.URL, DataURL: upstream.URL, ExecutionURL: upstream.URL, ResourceURL: backend.URL + "/mcp", AuthIssuer: backend.URL, SessionChecker: checker})
	if err != nil {
		t.Fatal(err)
	}
	// Use an httptest listener with a wrapper that injects the bearer header.
	server := httptest.NewServer(h)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{base: http.DefaultTransport, token: token}}, DisableStandaloneSSE: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 6 {
		t.Fatalf("expected six discovery tools, got %d", len(list.Tools))
	}
}

func (f *fakeChecker) CheckSession(_ context.Context, check c.SessionCheck) (c.Session, error) {
	f.checks = append(f.checks, check)
	return f.session, f.err
}

func TestProtectedResourceAndMCPBearerBoundary(t *testing.T) {
	checker := &fakeChecker{session: c.Session{User: c.User{ID: "u1", TenantID: "t1", Enabled: true}, ExpiresAt: time.Now().Add(time.Hour).Unix(), AccessScopes: []string{platform.ScopeDiscover}, AccessTokenID: "a1"}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	defer upstream.Close()
	h, err := NewHandler(Config{MetadataURL: upstream.URL, DataURL: upstream.URL, ExecutionURL: upstream.URL, ResourceURL: "http://127.0.0.1:18084/mcp", AuthIssuer: "http://127.0.0.1:18084", SessionChecker: checker})
	if err != nil {
		t.Fatal(err)
	}
	resource := httptest.NewRecorder()
	h.ServeHTTP(resource, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18084/.well-known/oauth-protected-resource/mcp", nil))
	if resource.Code != 200 {
		t.Fatalf("resource metadata status=%d", resource.Code)
	}
	unauth := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18084/mcp", nil)
	h.ServeHTTP(unauth, req)
	if unauth.Code != 401 {
		t.Fatalf("unauthenticated status=%d", unauth.Code)
	}
	bad := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18084/mcp", nil)
	req.Header.Set("Authorization", "Bearer mcp_0000000000000000000000000000000000000000000000000000000000000000")
	h.ServeHTTP(bad, req)
	if bad.Code != 200 && bad.Code != 400 && bad.Code != 401 && bad.Code != 415 {
		t.Fatalf("unexpected valid-shape response status=%d", bad.Code)
	}
	if len(checker.checks) != 1 || checker.checks[0].AccessToken == "" || checker.checks[0].Path != "/mcp" {
		t.Fatalf("MCP identity check was not forwarded: %#v", checker.checks)
	}
}

func TestConfigRejectsUnsafeURLs(t *testing.T) {
	checker := &fakeChecker{}
	_, err := NewHandler(Config{MetadataURL: "http://user:x@example/", DataURL: "http://127.0.0.1:1", ExecutionURL: "http://127.0.0.1:2", ResourceURL: "http://127.0.0.1:18084/mcp", AuthIssuer: "http://127.0.0.1:18084", SessionChecker: checker})
	if err == nil {
		t.Fatal("expected URL validation failure")
	}
}

func TestPublicResourceHostThroughLoopbackProxy(t *testing.T) {
	checker := &fakeChecker{session: c.Session{User: c.User{ID: "u1", TenantID: "t1", Enabled: true}, ExpiresAt: time.Now().Add(time.Hour).Unix(), AccessScopes: []string{platform.ScopeDiscover}, AccessTokenID: "a1"}}
	h, err := NewHandler(Config{MetadataURL: "http://127.0.0.1:1", DataURL: "http://127.0.0.1:2", ExecutionURL: "http://127.0.0.1:3", ResourceURL: "https://ontology.example:30011/mcp", AuthIssuer: "https://ontology.example:30011", SessionChecker: checker})
	if err != nil {
		t.Fatal(err)
	}
	// A real loopback listener supplies LocalAddrContextKey, which the SDK's
	// default localhost protection consults (a Recorder alone does not).
	server := httptest.NewServer(h)
	defer server.Close()
	for _, tc := range []struct {
		name, host, origin, forwardedHost string
		status                            int
	}{
		{"public proxy", "ontology.example:30011", "https://ontology.example:30011", "", 200},
		{"native client", "ontology.example:30011", "", "", 200},
		{"other host", "attacker.example:30011", "https://ontology.example:30011", "ontology.example:30011", 403},
		{"wrong port", "ontology.example:443", "https://ontology.example:30011", "", 403},
		{"loopback host for public resource", "127.0.0.1:18084", "", "", 403},
		{"other origin", "ontology.example:30011", "https://attacker.example", "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"proxy-test","version":"1"}}}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Host = tc.host
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Authorization", "Bearer mcp_0000000000000000000000000000000000000000000000000000000000000000")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.forwardedHost != "" {
				req.Header.Set("X-Forwarded-Host", tc.forwardedHost)
			}
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != tc.status {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
			if tc.status == 200 {
				var result struct {
					Result struct {
						ProtocolVersion string `json:"protocolVersion"`
					} `json:"result"`
				}
				if err := json.Unmarshal(body, &result); err != nil || result.Result.ProtocolVersion == "" {
					t.Fatalf("initialize failed: %s", body)
				}
			}
		})
	}
}
