package metadata

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func queryDirectoryPath(path string) bool {
	if len(path) > 4096 {
		return false
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	allowed := map[string]bool{"limit": true, "after": true}
	switch u.Path {
	case "/metadata/categories":
	case "/metadata/definitions":
		allowed["kind"], allowed["view"], allowed["categoryId"] = true, true, true
		if q.Get("kind") != "schema" && q.Get("kind") != "link" || q.Get("view") != "" && q.Get("view") != "current" {
			return false
		}
	default:
		return false
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	return true
}

func queryDirectorySource(check c.SessionCheck) bool {
	if !strings.HasPrefix(check.AccessToken, "mcp_") || check.Token != "" {
		return false
	}
	if check.Method == http.MethodGet {
		return check.Path == "/data/query/context"
	}
	if check.Method != http.MethodPost {
		return false
	}
	switch check.Path {
	case "/data/query/ontology-sql", "/data/query/ontology-sql/analyze", "/data/query/ontology-sql/complete", "/data/query/ontology-sql/paths", "/data/query/ontology-sql/insert-field":
		return true
	}
	return false
}

// NewQueryDirectoryHandler preserves the requesting user for Data's directory
// compilation, while keeping full definitions unavailable to external tokens.
// It cannot route arbitrary API calls, select a tenant or grant definition.read.
func NewQueryDirectoryHandler(serviceToken string, checker platform.SessionChecker, app http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(serviceToken) < 32 || !platform.EqualSecret(platform.Bearer(r), serviceToken) {
			platform.WriteError(w, 401, "service_unauthenticated", "需要有效服务身份")
			return
		}
		var input c.QueryDirectoryRequest
		if err := decode(r, &input); err != nil || !queryDirectoryPath(input.Path) || !queryDirectorySource(input.Check) {
			platform.WriteError(w, 400, "invalid_directory_request", "只允许当前查询所需的模型、关系和分类目录")
			return
		}
		if checker == nil || app == nil {
			platform.WriteError(w, 503, "identity_unavailable", "身份或目录服务未配置")
			return
		}
		session, err := checker.CheckSession(r.Context(), input.Check)
		if err != nil {
			writeDirectoryAccessError(w, err)
			return
		}
		if !validDirectorySession(session) {
			platform.WriteError(w, 401, "unauthenticated", "外部连接已失效")
			return
		}
		inner, err := http.NewRequestWithContext(r.Context(), http.MethodGet, input.Path, nil)
		if err != nil {
			platform.WriteError(w, 400, "invalid_directory_request", "目录请求无效")
			return
		}
		p := c.Principal{TenantID: session.User.TenantID, Subject: session.User.ID, ExpiresAt: session.ExpiresAt}
		inner = inner.WithContext(platform.WithPrincipal(platform.WithSessionUser(inner.Context(), session.User), p))
		result := &directoryResponse{headers: http.Header{}}
		app.ServeHTTP(result, inner)
		if result.overflow {
			platform.WriteError(w, 422, "directory_budget", "查询目录响应超过预算，请缩小范围")
			return
		}
		latest, err := checker.CheckSession(r.Context(), input.Check)
		if err != nil {
			writeDirectoryAccessError(w, err)
			return
		}
		if !validDirectorySession(latest) || latest.User.ID != p.Subject || latest.User.TenantID != p.TenantID || latest.AccessTokenID != session.AccessTokenID {
			platform.WriteError(w, 401, "unauthenticated", "外部连接已失效")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		status := result.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(result.body.Bytes())
	})
}

func validDirectorySession(s c.Session) bool {
	if !s.User.Enabled || s.User.ID == "" || s.User.TenantID == "" || s.ExpiresAt <= time.Now().Unix() || s.AccessTokenID == "" {
		return false
	}
	for _, scope := range s.AccessScopes {
		if scope == "ontology:discover" {
			return true
		}
	}
	return false
}

func writeDirectoryAccessError(w http.ResponseWriter, err error) {
	var access *platform.AccessError
	if errors.As(err, &access) && (access.Status == 401 || access.Status == 403 || access.Status == 422 || access.Status == 503) {
		platform.WriteError(w, access.Status, access.Code, access.Message)
		return
	}
	platform.WriteError(w, 503, "identity_unavailable", "无法确认当前外部连接身份")
}

type directoryResponse struct {
	headers  http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (w *directoryResponse) Header() http.Header { return w.headers }
func (w *directoryResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *directoryResponse) Write(value []byte) (int, error) {
	if w.body.Len()+len(value) > 4<<20 {
		w.overflow = true
		return 0, io.ErrShortWrite
	}
	return w.body.Write(value)
}
