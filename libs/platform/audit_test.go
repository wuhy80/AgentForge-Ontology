package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestPrivacyAuditUsesOnlyAuthoritativeIdentity(t *testing.T) {
	const secret = "DO_NOT_LOG_SYNTHETIC_SECRET"
	for _, authenticated := range []bool{false, true} {
		var events []AuditEvent
		app := auditRequests("data", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authenticated {
				AuditIdentity(r, c.User{ID: "verified-user", TenantID: "verified-tenant"})
			}
			w.WriteHeader(403)
			_, _ = w.Write([]byte(secret))
		}), func(event AuditEvent) { events = append(events, event) })
		r := httptest.NewRequest("POST", "https://review.test/data/query?private="+secret, strings.NewReader(secret))
		for _, key := range []string{"Authorization", "Cookie", "X-CSRF-Token", "X-Tenant-ID", "X-User-ID", "X-Request-ID"} {
			r.Header.Set(key, secret)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if len(events) != 1 {
			t.Fatal("audit event missing")
		}
		event := events[0]
		raw, _ := json.Marshal(event)
		if strings.Contains(string(raw), secret) || event.Status != 403 || event.Operation != "instance.query" || !auditRequestID.MatchString(event.RequestID) || w.Header().Get("X-Request-ID") != event.RequestID {
			t.Fatal("unsafe audit event")
		}
		if authenticated && (event.Subject != "verified-user" || event.TenantID != "verified-tenant") || !authenticated && (event.Subject != "" || event.TenantID != "") {
			t.Fatal("untrusted audit subject")
		}
	}
}
