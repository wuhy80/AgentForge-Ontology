package identity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestSharedLoginBucketsAreStableAndSeparateInventedAccounts(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	bucket, key := sharedLoginBucket(secret, "tenant/account/ip", true)
	again, againKey := sharedLoginBucket(secret, "tenant/account/ip", true)
	invented, inventedKey := sharedLoginBucket(secret, "tenant/account/ip", false)
	if bucket != again || string(key) != string(againKey) || bucket >= loginBucketsPerClass || invented != bucket+loginBucketsPerClass || string(inventedKey) != string(key) {
		t.Fatal("shared login bucket partition or persistence changed")
	}
	other, otherKey := sharedLoginBucket([]byte(strings.Repeat("x", 32)), "tenant/account/ip", true)
	if other == bucket && string(otherKey) == string(key) {
		t.Fatal("stored secret does not key the bucket")
	}
}

func TestUntrustedForwardingCannotSelectLoginBudget(t *testing.T) {
	input := c.LoginRequest{Tenant: "tenant", Username: "admin"}
	r := httptest.NewRequest("POST", "/metadata/auth/login", nil)
	r.RemoteAddr = "198.51.100.1:10000"
	key := loginKey(r, input)
	r.Header.Set("X-Real-IP", "198.51.100.2")
	r.Header.Set("X-Forwarded-For", "198.51.100.2")
	if loginKey(r, input) != key {
		t.Fatal("untrusted headers selected login budget")
	}
	var keys []string
	h, err := platform.TrustTLSProxy(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { keys = append(keys, loginKey(r, input)) }), "127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"198.51.100.1", "198.51.100.2"} {
		r.RemoteAddr = "127.0.0.1:20000"
		r.Header.Set("X-Real-IP", ip)
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	if keys[0] == keys[1] {
		t.Fatal("trusted proxy merged independent browsers")
	}
}

func TestAnonymousRegistrationRateLimitPrecedesDatabase(t *testing.T) {
	s := NewStore(nil, nil, "https://workbench.test")
	// Invalid metadata avoids any database call while still consuming admission.
	for i := 0; i < 6; i++ {
		r := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(`{}`))
		r.RemoteAddr = "198.51.100.1:20000"
		w := httptest.NewRecorder()
		s.NewHandler().ServeHTTP(w, r)
		want := 400
		if i == 5 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("request %d status=%d want=%d", i, w.Code, want)
		}
	}
}
