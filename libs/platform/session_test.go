package platform

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const sessionToken = "opaque_session_credential_1234567890123456"
const csrfToken = "opaque_csrf_credential_1234567890123456789"
const serviceToken = "internal_service_credential_123456789012345"

type sessionCheckerFunc func(context.Context, c.SessionCheck) (c.Session, error)

func (f sessionCheckerFunc) CheckSession(ctx context.Context, check c.SessionCheck) (c.Session, error) {
	return f(ctx, check)
}

func liveSession() c.Session {
	return c.Session{User: c.User{ID: "trusted-user", TenantID: "trusted-tenant", Enabled: true}, ExpiresAt: time.Now().Add(time.Hour).Unix(), CSRFToken: csrfToken}
}

func sessionRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, "http://workbench.test"+path, nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionToken})
	if UnsafeMethod(method) {
		r.Header.Set("Origin", "http://workbench.test")
		r.Header.Set("X-CSRF-Token", csrfToken)
	}
	return r
}

func TestSessionMiddlewareUsesAuthorityOnEveryRequest(t *testing.T) {
	calls, reached := 0, 0
	current := liveSession()
	var failure error
	checker := sessionCheckerFunc(func(_ context.Context, check c.SessionCheck) (c.Session, error) {
		calls++
		if check.Token != sessionToken {
			t.Fatal("session credential was not forwarded")
		}
		return current, failure
	})
	handler := ProtectSessionRoutes(serviceToken, false, checker, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		p, ok := Principal(r)
		if !ok || p.Subject != "trusted-user" || p.TenantID != "trusted-tenant" {
			t.Fatalf("untrusted identity selected: %#v", p)
		}
		w.WriteHeader(204)
	}))
	check := func(status int) {
		t.Helper()
		r := sessionRequest("GET", "/data/instances")
		r.Header.Set("X-Tenant-ID", "forged-tenant")
		r.Header.Set("X-Subject", "forged-user")
		r.Header.Set("Authorization", "Bearer old-bootstrap-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d, want=%d: %s", w.Code, status, w.Body.String())
		}
	}
	check(204)
	current.User.Enabled = false
	check(401)
	current = liveSession()
	current.ExpiresAt = time.Now().Add(-time.Second).Unix()
	check(401)
	current = liveSession()
	failure = &AccessError{Status: 401, Code: "unauthenticated", Message: "revoked"}
	check(401)
	failure = errors.New("unavailable secret credential must not escape")
	check(503)
	if calls != 5 || reached != 1 {
		t.Fatalf("authority calls=%d handler calls=%d", calls, reached)
	}
}

func TestSessionMiddlewareRejectsBootstrapAndMalformedCookies(t *testing.T) {
	called := 0
	handler := ProtectSessionRoutes(serviceToken, false, sessionCheckerFunc(func(context.Context, c.SessionCheck) (c.Session, error) {
		called++
		return liveSession(), nil
	}), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("request reached application") }))
	for _, cookies := range [][]string{nil, {"short"}, {sessionToken, sessionToken}, {strings.Repeat("x", 513)}, {strings.Repeat("x", 31) + "."}} {
		r := httptest.NewRequest("GET", "http://workbench.test/data", nil)
		r.Header.Set("Authorization", "Bearer "+serviceToken)
		for _, cookie := range cookies {
			r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("status=%d", w.Code)
		}
	}
	if called != 0 {
		t.Fatal("invalid local credentials reached authority")
	}
}

func TestSessionUnsafeOriginAndCSRF(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{"same origin", func(*http.Request) {}, 204},
		{"missing origin", func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"null origin", func(r *http.Request) { r.Header.Set("Origin", "null") }, 403},
		{"other host", func(r *http.Request) { r.Header.Set("Origin", "http://attacker.test") }, 403},
		{"other port", func(r *http.Request) { r.Header.Set("Origin", "http://workbench.test:8080") }, 403},
		{"other scheme", func(r *http.Request) { r.Header.Set("Origin", "https://workbench.test") }, 403},
		{"userinfo", func(r *http.Request) { r.Header.Set("Origin", "http://user@workbench.test") }, 403},
		{"origin path", func(r *http.Request) { r.Header.Set("Origin", "http://workbench.test/path") }, 403},
		{"forged forwarded", func(r *http.Request) {
			r.Header.Set("Origin", "https://proxy.test")
			r.Header.Set("X-Forwarded-Host", "proxy.test")
			r.Header.Set("X-Forwarded-Proto", "https")
		}, 403},
		{"missing csrf", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403},
		{"wrong csrf", func(r *http.Request) { r.Header.Set("X-CSRF-Token", strings.Repeat("x", 40)) }, 403},
		{"tls same origin", func(r *http.Request) {
			r.TLS = &tls.ConnectionState{}
			r.Header.Set("Origin", "https://workbench.test")
		}, 204},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := ProtectSessionRoutes(serviceToken, false, sessionCheckerFunc(func(context.Context, c.SessionCheck) (c.Session, error) { return liveSession(), nil }), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
			r := sessionRequest("POST", "/data/query")
			test.mutate(r)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status=%d want=%d: %s", w.Code, test.status, w.Body.String())
			}
		})
	}
}

func TestSessionControlledRouteExceptions(t *testing.T) {
	for _, test := range []struct {
		method, path   string
		bearer, origin bool
		status         int
	}{
		{"GET", "/healthz", false, false, 204},
		{"GET", "/readyz", false, false, 204},
		{"POST", "/internal/identity/session", false, false, 204},
		{"POST", "/metadata/auth/login", false, true, 204},
		{"GET", "/metadata/auth/session", false, false, 204},
		{"POST", "/metadata/auth/logout", false, true, 204},
		{"GET", "/.well-known/oauth-authorization-server", false, false, 204},
		{"GET", "/oauth/authorize", false, false, 204},
		{"POST", "/oauth/token", false, false, 204},
		{"POST", "/oauth/register", false, false, 204},
		{"POST", "/oauth/revoke", false, false, 204},
		{"POST", "/oauth/unrecognized", false, false, 401},
		{"POST", "/metadata/auth/login", false, false, 403},
		{"GET", "/metadata/definitions/schema-id", true, false, 204},
		{"GET", "/metadata/definitions/schema-id/draft", true, false, 204},
		{"GET", "/metadata/definitions/schema-id/draft", false, false, 401},
		{"PUT", "/metadata/definitions/schema-id/draft", true, true, 401},
		{"GET", "/metadata/resolve", true, false, 204},
		{"GET", "/metadata/definitions/schema-id", false, false, 401},
		{"GET", "/metadata/definitions", true, false, 401},
		{"POST", "/metadata/definitions/schema-id/publish", true, true, 401},
		{"GET", "/data/instances", true, false, 401},
	} {
		t.Run(test.method+test.path+string(rune(test.status)), func(t *testing.T) {
			handler := ProtectSessionRoutes(serviceToken, true, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
			r := httptest.NewRequest(test.method, "http://workbench.test"+test.path, nil)
			if test.bearer {
				r.Header.Set("Authorization", "Bearer "+serviceToken)
			}
			if test.origin {
				r.Header.Set("Origin", "http://workbench.test")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status=%d want=%d", w.Code, test.status)
			}
		})
	}
}

func TestRemoteSessionCheckerProtocol(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		body       string
		wantStatus int
	}{
		{"valid", 200, "", 0},
		{"revoked", 401, "{}", 401},
		{"csrf", 403, "{}", 403},
		{"outage", 500, "private error", 503},
		{"redirect", 302, "", 503},
		{"malformed", 200, "{", -1},
		{"unknown field", 200, `{"extra":true}`, -1},
		{"trailing", 200, "{} {}", -1},
		{"null", 200, "null", 401},
		{"oversized", 200, strings.Repeat(" ", 1<<20) + "{}", -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			redirected := false
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/internal/identity/session" || Bearer(r) != serviceToken {
					t.Error("incorrect authority request")
				}
				var check c.SessionCheck
				if json.NewDecoder(r.Body).Decode(&check) != nil || check.Token != sessionToken || check.CSRFToken != csrfToken {
					t.Error("incorrect authority body")
				}
				w.Header().Set("Location", target.URL)
				w.WriteHeader(test.status)
				if test.body == "" && test.status == 200 {
					_ = json.NewEncoder(w).Encode(liveSession())
				} else {
					_, _ = w.Write([]byte(test.body))
				}
			}))
			defer server.Close()
			checker := &RemoteSessionChecker{URL: server.URL, ServiceToken: serviceToken}
			session, err := checker.CheckSession(context.Background(), c.SessionCheck{Token: sessionToken, CSRFToken: csrfToken, Method: "POST"})
			if test.wantStatus == 0 {
				if err != nil || session.User.ID != "trusted-user" {
					t.Fatalf("session=%#v error=%v", session, err)
				}
			} else if test.wantStatus == -1 {
				if err == nil {
					t.Fatal("invalid response accepted")
				}
			} else {
				var failure *AccessError
				if !errors.As(err, &failure) || failure.Status != test.wantStatus {
					t.Fatalf("error=%v want status=%d", err, test.wantStatus)
				}
			}
			if redirected {
				t.Fatal("service credential followed a redirect")
			}
		})
	}
}

func TestRemoteAuthorizerDoesNotCacheAllow(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/authorization/decision" || Bearer(r) != serviceToken {
			t.Error("incorrect authority request")
		}
		var check c.AuthorizationCheck
		if json.NewDecoder(r.Body).Decode(&check) != nil || check.Principal.Subject != "user" || check.Request.Operation != "instance.read" {
			t.Error("incorrect authority context")
		}
		switch calls {
		case 1:
			_ = json.NewEncoder(w).Encode(c.Decision{Allow: true, State: "state-1", ReadFields: []string{"field"}})
		case 2:
			_ = json.NewEncoder(w).Encode(c.Decision{})
		case 3:
			_ = json.NewEncoder(w).Encode(c.Decision{Allow: true})
		default:
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	authorizer := &RemoteAuthorizer{URL: server.URL, ServiceToken: serviceToken}
	for i := 1; i <= 4; i++ {
		decision, err := authorizer.Authorize(context.Background(), c.Principal{Subject: "user", TenantID: "tenant"}, c.AuthorizationRequest{Operation: "instance.read"})
		if i == 1 && (err != nil || !decision.Allow) {
			t.Fatalf("initial allow failed: %v", err)
		}
		if i == 2 && (err != nil || decision.Allow) {
			t.Fatal("deny reused prior allow")
		}
		if i > 2 && (err == nil || decision.Allow) {
			t.Fatal("invalid/outage response reused prior allow")
		}
	}
	if calls != 4 {
		t.Fatal("authority was not called on every authorization")
	}
}
