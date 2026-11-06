package platform

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

const testSecret = "test-only-authentication-secret-32-bytes"

func TestIdentityCannotChangeTenantOrAcceptExpiredToken(t *testing.T) {
	p := contracts.Principal{Subject: "repairer", TenantID: "tenant-a", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	token, err := SignToken(testSecret, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyToken(testSecret, token)
	if err != nil || got != p {
		t.Fatalf("round trip: %v, %v", got, err)
	}
	parts := strings.Split(token, ".")
	data, _ := base64.RawURLEncoding.DecodeString(parts[0])
	parts[0] = base64.RawURLEncoding.EncodeToString([]byte(strings.ReplaceAll(string(data), "tenant-a", "tenant-b")))
	if _, err := VerifyToken(testSecret, strings.Join(parts, ".")); err == nil {
		t.Fatal("tampered tenant was accepted")
	}
	if _, err := VerifyToken("a-different-long-enough-authentication-secret", token); err == nil {
		t.Fatal("wrong signing key accepted")
	}
	p.ExpiresAt = time.Now().Add(-time.Second).Unix()
	if _, err := SignToken(testSecret, p); err == nil {
		t.Fatal("expired identity accepted")
	}
}

func TestAuthenticationIgnoresClientTenantHeader(t *testing.T) {
	p := contracts.Principal{Subject: "u", TenantID: "tenant-a", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	token, _ := SignToken(testSecret, p)
	seen := false
	h := Authenticate(testSecret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := Principal(r)
		if !ok || got.TenantID != "tenant-a" {
			t.Fatal("tenant was not taken from verified identity")
		}
		seen = true
		w.WriteHeader(204)
	}))
	r := httptest.NewRequest("GET", "/data/query", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Tenant-ID", "tenant-b")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || !seen {
		t.Fatalf("request failed: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 401 {
		t.Fatalf("missing identity status %d", w.Code)
	}
}

func TestOPAFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body         string
		status             int
		wantErr, wantAllow bool
	}{
		{"undefined", `{}`, 200, true, false},
		{"null", `{"result":null}`, 200, true, false},
		{"error", `{"error":"failure"}`, 500, true, false},
		{"missing_state", `{"result":{"allow":true}}`, 200, true, false},
		{"malformed", `{"result":`, 200, true, false},
		{"trailing", `{"result":{"allow":true,"state":"s"}} {}`, 200, true, false},
		{"unknown_contract", `{"result":{"allow":true,"state":"s","rawSQL":"1=1"}}`, 200, true, false},
		{"denied", `{"result":{"allow":false}}`, 200, false, false},
		{"granted", `{"result":{"allow":true,"state":"s1","readFields":["name"]}}`, 200, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/data/ontology/decision" || r.Method != "POST" {
					t.Error("wrong OPA endpoint")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			a := OPAAuthorizer{URL: srv.URL}
			d, err := a.Authorize(context.Background(), contracts.Principal{Subject: "u", TenantID: "t"}, contracts.AuthorizationRequest{Operation: "instance.read"})
			if (err != nil) != tc.wantErr || d.Allow != tc.wantAllow {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestOPAUnavailabilityDoesNotReusePreviousGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"result":{"allow":true,"state":"s"}}`)
	}))
	a := OPAAuthorizer{URL: srv.URL}
	p := contracts.Principal{Subject: "u", TenantID: "t"}
	req := contracts.AuthorizationRequest{Operation: "instance.read"}
	if d, err := a.Authorize(context.Background(), p, req); err != nil || !d.Allow {
		t.Fatalf("first decision: %v", err)
	}
	srv.Close()
	if d, err := a.Authorize(context.Background(), p, req); err == nil || d.Allow {
		t.Fatal("unavailable OPA reused an allow decision")
	}
}
