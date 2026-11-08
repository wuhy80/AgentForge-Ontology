package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

type AuditEvent struct {
	Event      string `json:"event"`
	RequestID  string `json:"requestId"`
	Service    string `json:"service"`
	Operation  string `json:"operation"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"durationMs"`
	TenantID   string `json:"tenantId,omitempty"`
	Subject    string `json:"subject,omitempty"`
}

type auditState struct{ tenantID, subject string }
type auditContextKey struct{}

// AuditIdentity is called only after authoritative authentication. Header and
// body identity claims never populate the audit subject, including failed login.
func AuditIdentity(r *http.Request, user contracts.User) {
	if state, ok := r.Context().Value(auditContextKey{}).(*auditState); ok {
		state.tenantID, state.subject = user.TenantID, user.ID
	}
}

var auditRequestID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func auditOperation(method, path string) string {
	switch path {
	case "/metadata/auth/login":
		return "auth.login"
	case "/metadata/auth/session":
		return "auth.session"
	case "/metadata/auth/logout":
		return "auth.logout"
	case "/data/query/context":
		return "directory.read"
	case "/data/query", "/data/query/ontology-sql":
		return "instance.query"
	}
	if strings.HasPrefix(path, "/data/schemas/") {
		if strings.HasSuffix(path, "/form") {
			return "form.read"
		}
		if strings.Contains(path, "/instances") {
			switch method {
			case http.MethodPost:
				return "instance.create"
			case http.MethodPatch:
				return "instance.patch"
			case http.MethodDelete:
				return "instance.delete"
			case http.MethodGet:
				return "instance.read"
			}
		}
	}
	return "api.request"
}

// AuditRequests records only fixed operation labels, authoritative IDs and
// status/timing. It never serializes a request, URL, header, body or result.
// Install inside TrustTLSProxy and outside admission/authentication middleware.
func AuditRequests(service string, app http.Handler) http.Handler {
	return auditRequests(service, app, func(event AuditEvent) {
		raw, _ := json.Marshal(event)
		log.Print(string(raw))
	})
}

func auditRequests(service string, app http.Handler, record func(AuditEvent)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := ""
		trusted, _ := r.Context().Value(proxyTrustedContextKey{}).(bool)
		if trusted && len(r.Header.Values("X-Request-ID")) == 1 && auditRequestID.MatchString(r.Header.Get("X-Request-ID")) {
			id = r.Header.Get("X-Request-ID")
		}
		if id == "" {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				WriteError(w, 503, "audit_unavailable", "请求标识暂不可用")
				return
			}
			id = hex.EncodeToString(random[:])
		}
		state := &auditState{}
		r = r.WithContext(context.WithValue(r.Context(), auditContextKey{}, state))
		w.Header().Set("X-Request-ID", id)
		response := &auditResponse{ResponseWriter: w}
		completed := false
		defer func() {
			status := response.status
			if status == 0 {
				status = 200
			}
			if !completed {
				status = 500
			}
			record(AuditEvent{Event: "security.request", RequestID: id, Service: service, Operation: auditOperation(r.Method, r.URL.Path), Status: status, DurationMS: time.Since(started).Milliseconds(), TenantID: state.tenantID, Subject: state.subject})
		}()
		app.ServeHTTP(response, r)
		completed = true
	})
}

type auditResponse struct {
	http.ResponseWriter
	status int
}

func (w *auditResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *auditResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *auditResponse) Write(raw []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(raw)
}
