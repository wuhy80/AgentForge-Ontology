package platform

import "testing"

func TestAccessRouteScopes(t *testing.T) {
	cases := []struct {
		method, path, scope string
		ok                  bool
	}{
		{"GET", "/metadata/categories", ScopeDiscover, true},
		{"GET", "/metadata/definitions/secret", "", false},
		{"GET", "/data/query/context", ScopeDiscover, true},
		{"POST", "/data/query/ontology-sql", ScopeQuery, true},
		{"POST", "/execution/invocations", ScopeInvoke, true},
		{"POST", "/metadata/admin/policy", "", false},
		{"GET", "/data/schemas/s/instances/i", ScopeQuery, true},
		{"GET", "/mcp", "", true},
		{"GET", "/data/query/../sources", "", false},
	}
	for _, test := range cases {
		scope, ok := AccessRoute(test.method, test.path)
		if ok != test.ok || test.ok && scope != test.scope {
			t.Fatalf("AccessRoute(%q,%q)=(%q,%v), want (%q,%v)", test.method, test.path, scope, ok, test.scope, test.ok)
		}
	}
}

func TestAccessTokenFormatAndScopes(t *testing.T) {
	if !ValidAccessToken("mcp_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Fatal("valid token rejected")
	}
	if ValidAccessToken("mcp_short") || ValidAccessToken("internal_service_credential") {
		t.Fatal("non-MCP token accepted")
	}
	if !HasAccessScope([]string{ScopeDiscover, ScopeQuery}, ScopeQuery) || HasAccessScope([]string{ScopeQuery}, ScopeDiscover) {
		t.Fatal("scope check incorrect")
	}
}
