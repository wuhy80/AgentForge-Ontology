package platform

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

const SessionCookieName = "ontology_session"

// AccessError preserves the distinction between an expired identity, a CSRF
// failure and an unavailable authority across the service boundary. Messages
// must be safe for a public caller and must never include session credentials.
type AccessError struct {
	Status  int
	Code    string
	Message string
}

func (e *AccessError) Error() string { return e.Message }

type SessionChecker interface {
	CheckSession(context.Context, contracts.SessionCheck) (contracts.Session, error)
}

type sessionUserContextKey struct{}

// WithSessionUser is only for trusted session middleware and in-process tests.
// HTTP inputs never populate this context directly.
func WithSessionUser(ctx context.Context, user contracts.User) context.Context {
	return context.WithValue(ctx, sessionUserContextKey{}, user)
}

func SessionUser(r *http.Request) (contracts.User, bool) {
	user, ok := r.Context().Value(sessionUserContextKey{}).(contracts.User)
	return user, ok
}

var opaqueCredential = regexp.MustCompile(`^[A-Za-z0-9_-]{32,512}$`)

// UnsafeMethod follows HTTP's safe-method definition. OPTIONS does not grant a
// business operation; the downstream router still determines supported routes.
func UnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// CheckRequestOrigin requires the browser's Origin for unsafe requests and
// compares its full authority with the request Host. A local reverse proxy must
// preserve Host; untrusted forwarding headers never select the expected origin.
func CheckRequestOrigin(r *http.Request) error {
	if !UnsafeMethod(r.Method) {
		return nil
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || (origin.Scheme != "http" && origin.Scheme != "https") {
		return &AccessError{Status: 403, Code: "invalid_origin", Message: "请求来源无效，请从当前工作台重新提交"}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if !strings.EqualFold(origin.Host, r.Host) || origin.Scheme != scheme {
		return &AccessError{Status: 403, Code: "invalid_origin", Message: "请求来源与当前服务不一致"}
	}
	return nil
}

// SessionRequest extracts only the opaque HttpOnly cookie. Bootstrap bearer
// credentials and caller-supplied tenant/user headers cannot select an identity.
func SessionRequest(r *http.Request) (contracts.SessionCheck, error) {
	check := contracts.SessionCheck{Method: r.Method, Origin: r.Header.Get("Origin"), CSRFToken: r.Header.Get("X-CSRF-Token")}
	if strings.HasPrefix(Bearer(r), "mcp_") {
		if !ValidAccessToken(Bearer(r)) {
			return check, &AccessError{Status: 401, Code: "unauthenticated", Message: "外部连接凭据无效"}
		}
		for _, cookie := range r.Cookies() {
			if cookie.Name == SessionCookieName {
				return check, &AccessError{Status: 401, Code: "ambiguous_credentials", Message: "请分别使用浏览器会话或外部连接凭据"}
			}
		}
		if _, allowed := AccessRoute(r.Method, r.URL.Path); !allowed {
			return check, &AccessError{Status: 403, Code: "connection_scope_denied", Message: "外部连接不允许此接口"}
		}
		if check.Origin != "" {
			originRequest := r.Clone(r.Context())
			originRequest.Method = http.MethodPost
			if err := CheckRequestOrigin(originRequest); err != nil {
				return check, err
			}
		}
		check.AccessToken, check.Path = Bearer(r), r.URL.Path
		return check, nil
	}
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == SessionCookieName {
			count++
			check.Token = cookie.Value
		}
	}
	if count != 1 || !opaqueCredential.MatchString(check.Token) {
		return check, &AccessError{Status: 401, Code: "unauthenticated", Message: "请登录后继续"}
	}
	if err := CheckRequestOrigin(r); err != nil {
		return check, err
	}
	if UnsafeMethod(r.Method) && !opaqueCredential.MatchString(check.CSRFToken) {
		return check, &AccessError{Status: 403, Code: "invalid_csrf", Message: "请求校验已失效，请刷新后重试"}
	}
	return check, nil
}

func validateSession(session contracts.Session, check contracts.SessionCheck) error {
	if !session.User.Enabled || strings.TrimSpace(session.User.ID) == "" || len(session.User.ID) > 200 || strings.TrimSpace(session.User.TenantID) == "" || len(session.User.TenantID) > 100 || session.ExpiresAt <= time.Now().Unix() {
		return &AccessError{Status: 401, Code: "unauthenticated", Message: "登录已失效，请重新登录"}
	}
	if check.AccessToken != "" {
		required, allowed := AccessRoute(check.Method, check.Path)
		if !ValidAccessToken(check.AccessToken) || !allowed || session.AccessTokenID == "" || !HasAccessScope(session.AccessScopes, required) {
			return &AccessError{Status: 403, Code: "connection_scope_denied", Message: "外部连接范围不足"}
		}
		return nil
	}
	if UnsafeMethod(check.Method) && (!opaqueCredential.MatchString(session.CSRFToken) || subtle.ConstantTimeCompare([]byte(session.CSRFToken), []byte(check.CSRFToken)) != 1) {
		return &AccessError{Status: 403, Code: "invalid_csrf", Message: "请求校验已失效，请刷新后重试"}
	}
	return nil
}

func writeAccessError(w http.ResponseWriter, err error) {
	var failure *AccessError
	if errors.As(err, &failure) && (failure.Status == 401 || failure.Status == 403 || failure.Status == 422 || failure.Status == 503) {
		WriteError(w, failure.Status, failure.Code, failure.Message)
		return
	}
	WriteError(w, 503, "identity_unavailable", "无法确认当前登录状态，请稍后重试")
}

// ProtectSessionRoutes queries the authoritative session store on every public
// request. The three auth routes own their cookie lifecycle; all other public
// routes require an enabled user and a live session. Internal endpoints retain
// their independent service authentication in the owning handler.
func ProtectSessionRoutes(serviceToken string, metadata bool, checker SessionChecker, app http.Handler) http.Handler {
	public := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		check, err := SessionRequest(r)
		if err != nil {
			writeAccessError(w, err)
			return
		}
		if checker == nil {
			writeAccessError(w, errors.New("session authority is not configured"))
			return
		}
		session, err := checker.CheckSession(r.Context(), check)
		if err == nil {
			err = validateSession(session, check)
		}
		if err != nil {
			writeAccessError(w, err)
			return
		}
		principal := contracts.Principal{Subject: session.User.ID, TenantID: session.User.TenantID, ExpiresAt: session.ExpiresAt}
		ctx := WithSessionUser(r.Context(), session.User)
		app.ServeHTTP(w, r.WithContext(WithPrincipal(ctx, principal)))
	})
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", app)
	mux.Handle("GET /readyz", app)
	mux.Handle("/internal/", app)
	if metadata {
		auth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := CheckRequestOrigin(r); err != nil {
				writeAccessError(w, err)
				return
			}
			app.ServeHTTP(w, r)
		})
		mux.Handle("POST /metadata/auth/login", auth)
		mux.Handle("GET /metadata/auth/session", auth)
		mux.Handle("POST /metadata/auth/logout", auth)
		mux.Handle("GET /.well-known/oauth-authorization-server", app)
		// OAuth discovery, authorization, token exchange, dynamic registration,
		// and revocation are public protocol endpoints. The identity handler
		// performs the session/consent check where the flow requires it.
		mux.Handle("GET /oauth/authorize", app)
		mux.Handle("POST /oauth/token", app)
		mux.Handle("POST /oauth/register", app)
		mux.Handle("POST /oauth/revoke", app)
		serviceRead := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if EqualSecret(Bearer(r), serviceToken) {
				app.ServeHTTP(w, r)
				return
			}
			public.ServeHTTP(w, r)
		})
		mux.Handle("GET /metadata/definitions/{id}", serviceRead)
		mux.Handle("GET /metadata/definitions/{id}/draft", serviceRead)
		mux.Handle("GET /metadata/resolve", serviceRead)
	}
	mux.Handle("/", public)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}
