// Package gateway adapts Ontology's public services to MCP. It owns no business
// data, authorization policies, user sessions, or executable implementations.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

const (
	MaxRequestBytes = 1 << 20
	MaxResultBytes  = 1 << 20
	ScopeDiscover   = "ontology:discover"
	ScopeQuery      = "ontology:query"
	ScopeInvoke     = "ontology:invoke"
)

// Config contains operator-selected upstreams, never client-supplied URLs.
type Config struct {
	MetadataURL, DataURL, ExecutionURL string
	ResourceURL, AuthIssuer            string
	AllowedOrigins                     []string
	SessionChecker                     platform.SessionChecker
	HTTPClient                         *http.Client
	Logger                             *slog.Logger
}

type gateway struct {
	cfg     Config
	client  *http.Client
	servers [8]*mcp.Server
}

type requestIdentity struct {
	token   string
	session c.Session
}
type identityKey struct{}

// NewHandler creates a stateless Streamable HTTP server. Authentication is
// repeated on every HTTP request and again before delivering each tool result.
func NewHandler(cfg Config) (http.Handler, error) {
	for _, endpoint := range []string{cfg.MetadataURL, cfg.DataURL, cfg.ExecutionURL} {
		if !validEndpoint(endpoint, false) {
			return nil, errors.New("MCP upstream URLs must be absolute HTTP URLs without credentials, query, or fragment")
		}
	}
	if !validEndpoint(cfg.ResourceURL, true) || !validEndpoint(cfg.AuthIssuer, true) {
		return nil, errors.New("MCP resource and issuer require HTTPS or loopback HTTP URLs")
	}
	resource, _ := url.Parse(cfg.ResourceURL)
	if resource.Path != "/mcp" {
		return nil, errors.New("MCP resource URL must end with /mcp")
	}
	if cfg.SessionChecker == nil {
		return nil, errors.New("MCP identity authority is required")
	}
	client := http.Client{Timeout: 40 * time.Second}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	g := &gateway{cfg: cfg, client: &client}
	for mask := range g.servers {
		g.servers[mask] = g.newServer(mask)
	}
	// The gateway checks Host against the configured public resource before
	// reaching the SDK. Its localhost-only default rejects a public Host when
	// a reverse proxy connects to our loopback listener.
	stream := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		identity, ok := r.Context().Value(identityKey{}).(requestIdentity)
		if !ok {
			return nil
		}
		return g.servers[scopeMask(identity.session.AccessScopes)]
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: MaxRequestBytes, DisableLocalhostProtection: true})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", g.ready)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", g.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", g.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", g.proxyOAuth)
	mux.HandleFunc("GET /oauth/authorize", g.proxyOAuth)
	for _, path := range []string{"/oauth/token", "/oauth/register", "/oauth/revoke"} {
		mux.HandleFunc("POST "+path, g.proxyOAuth)
	}
	mux.Handle("/mcp", g.protect(stream))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	}), nil
}

func validEndpoint(raw string, public bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	if public && u.Scheme == "http" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return false
	}
	return true
}

func scopeMask(scopes []string) int {
	mask := 0
	for _, scope := range scopes {
		switch scope {
		case ScopeDiscover:
			mask |= 1
		case ScopeQuery:
			mask |= 2
		case ScopeInvoke:
			mask |= 4
		}
	}
	// Query and invocation discovery are intentionally coupled. The identity
	// service rejects such scope sets too; enforce the invariant at the protocol
	// boundary so a misconfigured authority cannot expose orphaned tools.
	if mask&1 == 0 {
		mask &^= 2
		mask &^= 4
	}
	return mask
}

func (g *gateway) authenticate(ctx context.Context, token string) (c.Session, error) {
	if !validAccessToken(token) {
		return c.Session{}, &platform.AccessError{Status: 401, Code: "invalid_token", Message: "需要有效的 MCP 连接凭据"}
	}
	s, err := g.cfg.SessionChecker.CheckSession(ctx, c.SessionCheck{AccessToken: token, Method: http.MethodPost, Path: "/mcp", Resource: g.cfg.ResourceURL})
	if err != nil {
		return c.Session{}, err
	}
	if !s.User.Enabled || s.User.ID == "" || s.User.TenantID == "" || s.ExpiresAt <= time.Now().Unix() || s.AccessTokenID == "" || scopeMask(s.AccessScopes) == 0 {
		return c.Session{}, &platform.AccessError{Status: 401, Code: "invalid_token", Message: "MCP 连接凭据已失效"}
	}
	return s, nil
}

func validAccessToken(token string) bool {
	if len(token) != 68 || !strings.HasPrefix(token, "mcp_") {
		return false
	}
	for _, r := range token[4:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (g *gateway) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.allowedHost(r.Host) {
			writeJSON(w, 403, c.Error{Code: "invalid_host", Message: "请求主机不允许连接 MCP"})
			return
		}
		if raw := r.Header.Get("Origin"); raw != "" && !g.allowedOrigin(raw) {
			writeJSON(w, 403, c.Error{Code: "invalid_origin", Message: "请求来源不允许连接 MCP"})
			return
		}
		if len(r.Header.Values("Authorization")) != 1 || r.Header.Get("Cookie") != "" {
			g.writeAuthError(w, &platform.AccessError{Status: 401, Code: "invalid_token", Message: "请使用独立 MCP 连接凭据"})
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			g.writeAuthError(w, &platform.AccessError{Status: 401, Code: "invalid_token", Message: "需要 Bearer 连接凭据"})
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		s, err := g.authenticate(r.Context(), token)
		if err != nil {
			g.writeAuthError(w, err)
			return
		}
		ctx := context.WithValue(r.Context(), identityKey{}, requestIdentity{token: token, session: s})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (g *gateway) allowedHost(host string) bool {
	resource, _ := url.Parse(g.cfg.ResourceURL)
	if host == resource.Host {
		return true
	}
	// Local development may use another loopback port or localhost spelling.
	// Public resources accept only their configured authority, including port.
	return loopbackHost(resource.Host) && loopbackHost(host)
}

func loopbackHost(authority string) bool {
	host, _, err := net.SplitHostPort(authority)
	if err != nil {
		host = authority
	}
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

func (g *gateway) allowedOrigin(origin string) bool {
	u, _ := url.Parse(g.cfg.ResourceURL)
	if origin == u.Scheme+"://"+u.Host {
		return true
	}
	for _, v := range g.cfg.AllowedOrigins {
		if origin == v {
			return true
		}
	}
	return false
}

func (g *gateway) writeAuthError(w http.ResponseWriter, err error) {
	status, code, message := 503, "identity_unavailable", "身份服务暂不可用"
	var access *platform.AccessError
	if errors.As(err, &access) {
		switch access.Status {
		case 401:
			status, code, message = 401, "invalid_token", "MCP 连接凭据无效或已撤销"
		case 403:
			status, code, message = 403, "insufficient_scope", "连接范围不允许此请求"
		}
	}
	if status == 401 || status == 403 {
		u, _ := url.Parse(g.cfg.ResourceURL)
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+u.Scheme+"://"+u.Host+`/.well-known/oauth-protected-resource/mcp", error="`+code+`"`)
	}
	writeJSON(w, status, c.Error{Code: code, Message: message})
}

func (g *gateway) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"resource": g.cfg.ResourceURL, "resource_name": "Ontology MCP", "authorization_servers": []string{strings.TrimRight(g.cfg.AuthIssuer, "/")}, "scopes_supported": []string{ScopeDiscover, ScopeQuery, ScopeInvoke}, "bearer_methods_supported": []string{"header"}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
