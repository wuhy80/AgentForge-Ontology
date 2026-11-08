package platform

import (
	"net/http"
	"path"
	"regexp"
	"strings"
)

const (
	ScopeDiscover = "ontology:discover"
	ScopeQuery    = "ontology:query"
	ScopeInvoke   = "ontology:invoke"
)

var accessCredential = regexp.MustCompile(`^mcp_[a-f0-9]{64}$`)

// ValidAccessToken distinguishes revocable external credentials from internal
// service secrets, historical bootstrap bearers and browser session cookies.
func ValidAccessToken(value string) bool { return accessCredential.MatchString(value) }

// AccessRoute is a deliberately narrow allowlist. It limits credentials even
// when their owner is a tenant administrator; resource authorization still runs
// in the service. The MCP logical resource has per-tool scope checks as well.
func AccessRoute(method, requestPath string) (string, bool) {
	if requestPath == "" || path.Clean(requestPath) != requestPath || strings.ContainsAny(requestPath, "\\?#") {
		return "", false
	}
	if requestPath == "/mcp" && (method == "GET" || method == "POST" || method == "DELETE") {
		return "", true
	}
	parts := strings.Split(strings.TrimPrefix(requestPath, "/"), "/")
	if method == http.MethodGet {
		switch requestPath {
		case "/metadata/capabilities", "/metadata/categories", "/metadata/groups", "/data/query/context":
			return ScopeDiscover, true
		case "/data/capabilities":
			return ScopeQuery, true
		case "/execution/capabilities":
			return ScopeInvoke, true
		case "/metadata/callables":
			return ScopeDiscover, true
		}
		if len(parts) == 3 && parts[0] == "metadata" && parts[1] == "callables" && parts[2] != "" {
			return ScopeDiscover, true
		}
		if len(parts) == 5 && parts[0] == "data" && parts[1] == "schemas" && parts[2] != "" && parts[3] == "instances" && parts[4] != "" {
			return ScopeQuery, true
		}
		if len(parts) == 4 && parts[0] == "data" && parts[1] == "links" && parts[2] != "" && parts[3] == "instances" {
			return ScopeQuery, true
		}
		if len(parts) == 3 && parts[0] == "execution" && parts[1] == "invocations" && parts[2] != "" {
			return ScopeInvoke, true
		}
	}
	if method == http.MethodPost {
		switch requestPath {
		case "/data/query", "/data/query/ontology-sql", "/data/query/ontology-sql/analyze", "/data/query/ontology-sql/format", "/data/query/ontology-sql/complete", "/data/query/ontology-sql/paths", "/data/query/ontology-sql/insert-field":
			return ScopeQuery, true
		case "/execution/invocations":
			return ScopeInvoke, true
		}
	}
	return "", false
}

func HasAccessScope(scopes []string, required string) bool {
	if required == "" {
		return len(scopes) > 0
	}
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}
