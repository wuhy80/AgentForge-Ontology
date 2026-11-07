package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestSessionUserUsesCurrentAuthorityAndIgnoresCallerAdministrator(t *testing.T) {
	current := liveSession()
	current.User.Administrator = false
	checks := 0
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := SessionUser(r)
		if !ok || user.ID != current.User.ID || user.TenantID != current.User.TenantID || user.Administrator != current.User.Administrator {
			t.Fatal("current authoritative user was not propagated")
		}
		checks++
		w.WriteHeader(204)
	})
	h := ProtectSessionRoutes(serviceToken, false, sessionCheckerFunc(func(context.Context, c.SessionCheck) (c.Session, error) { return current, nil }), app)
	for _, administrator := range []bool{false, true, false} {
		current.User.Administrator = administrator
		r := sessionRequest("GET", "/data/sources")
		r.Header.Set("X-Administrator", "true")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatal("valid session was rejected")
		}
	}
	if checks != 3 {
		t.Fatal("authoritative user context was cached")
	}
	if _, ok := SessionUser(httptest.NewRequest("GET", "/", nil)); ok {
		t.Fatal("missing authority user was synthesized")
	}
}
