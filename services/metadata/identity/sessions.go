package identity

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"golang.org/x/crypto/bcrypt"
)

const SessionCookieName = "ontology_session"

var dummyPasswordHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

func unsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// ConfigureAdditionalOrigins runs before serving requests. Origin remains the
// canonical URL for OAuth redirects and cookie security; aliases are exact
// browser origins and must use the same scheme as that canonical URL.
func (s *Store) ConfigureAdditionalOrigins(origins ...string) error {
	primary, err := url.Parse(s.Origin)
	if err != nil || !validPublicURL(s.Origin, false) {
		return fmt.Errorf("工作台主来源须为HTTPS或本机回环HTTP来源")
	}
	allowed := map[string]bool{}
	for _, raw := range origins {
		origin := strings.TrimSpace(raw)
		if origin == "" {
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || !validPublicURL(origin, false) || origin != u.Scheme+"://"+u.Host || u.Scheme != primary.Scheme || strings.ContainsAny(u.Host, "*\\") {
			return fmt.Errorf("附加工作台来源须为与主来源同协议的完整来源，不能含路径、通配符、查询或片段")
		}
		allowed[origin] = true
	}
	s.additionalOrigins = allowed
	return nil
}

func (s *Store) checkOrigin(origin string) error {
	if origin != "" && origin != s.Origin && !s.additionalOrigins[origin] {
		return &platform.AccessError{Status: 403, Code: "invalid_origin", Message: "请求来源不受信任"}
	}
	return nil
}

// CheckSession always reads the current account and role membership; the cookie
// carries no identity, grants, or selected tenant supplied by the browser.
func (s *Store) CheckSession(ctx context.Context, check contracts.SessionCheck) (contracts.Session, error) {
	if check.AccessToken != "" {
		return s.checkAccess(ctx, check)
	}
	var session contracts.Session
	if len(check.Token) != 64 {
		return session, &platform.AccessError{Status: 401, Code: "unauthenticated", Message: "请重新登录"}
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var tenant, userID, csrf string
		err := tx.QueryRowContext(ctx, "SELECT tenant_id,user_id,csrf_token,expires_at FROM af_identity_sessions WHERE token_hash=?", tokenHash(check.Token)).Scan(&tenant, &userID, &csrf, &session.ExpiresAt)
		if errors.Is(err, sql.ErrNoRows) || err == nil && session.ExpiresAt <= time.Now().Unix() {
			return &platform.AccessError{Status: 401, Code: "unauthenticated", Message: "会话已失效，请重新登录"}
		}
		if err != nil {
			return err
		}
		user, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id=?", tenant, userID))
		if errors.Is(err, sql.ErrNoRows) || err == nil && !user.Enabled {
			return &platform.AccessError{Status: 401, Code: "unauthenticated", Message: "账户已停用或不可用"}
		}
		if err != nil {
			return err
		}
		if _, err := roleIDs(ctx, tx, tenant, user.RoleIDs); err != nil {
			return err
		}
		if unsafeMethod(check.Method) {
			if err := s.checkOrigin(check.Origin); err != nil {
				return err
			}
			if len(check.CSRFToken) != 64 || subtle.ConstantTimeCompare([]byte(csrf), []byte(check.CSRFToken)) != 1 {
				return &platform.AccessError{Status: 403, Code: "csrf_invalid", Message: "会话校验失败，请重新读取当前会话"}
			}
		}
		session.User, session.CSRFToken = user, csrf
		return nil
	})
	return session, err
}

func (s *Store) login(ctx context.Context, input contracts.LoginRequest) (contracts.Session, string, error) {
	var session contracts.Session
	var token string
	_, key, nameErr := normalizedName(input.Username)
	if nameErr != nil || len(input.Tenant) == 0 || len(input.Tenant) > 100 || len(input.Password) > 72 {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(input.Password))
		return session, "", problem(401, "login_failed", "租户、用户名或密码不正确")
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := lockTenant(ctx, tx, input.Tenant); errors.Is(err, sql.ErrNoRows) {
			_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(input.Password))
			return problem(401, "login_failed", "租户、用户名或密码不正确")
		} else if err != nil {
			return err
		}
		var id, hash string
		err := tx.QueryRowContext(ctx, "SELECT id,password_hash FROM af_identity_users WHERE tenant_id=? AND username_key=?", input.Tenant, key).Scan(&id, &hash)
		if errors.Is(err, sql.ErrNoRows) {
			_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(input.Password))
			return problem(401, "login_failed", "租户、用户名或密码不正确")
		}
		if err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil {
			return problem(401, "login_failed", "租户、用户名或密码不正确")
		}
		user, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id=?", input.Tenant, id))
		if err != nil {
			return err
		}
		if !user.Enabled {
			return problem(401, "login_failed", "租户、用户名或密码不正确")
		}
		if _, err := roleIDs(ctx, tx, user.TenantID, user.RoleIDs); err != nil {
			return err
		}
		session, token, err = createSession(ctx, tx, user)
		if err != nil {
			return err
		}
		return audit(ctx, tx, user.TenantID, user.ID, "auth.login", user.ID)
	})
	return session, token, err
}

func createSession(ctx context.Context, tx *sql.Tx, user contracts.User) (contracts.Session, string, error) {
	var result contracts.Session
	token, err := randomToken()
	if err != nil {
		return result, "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return result, "", err
	}
	result = contracts.Session{User: user, ExpiresAt: time.Now().Add(sessionLifetime).Unix(), CSRFToken: csrf}
	_, err = tx.ExecContext(ctx, "INSERT INTO af_identity_sessions (token_hash,tenant_id,user_id,csrf_token,expires_at) VALUES (?,?,?,?,?)", tokenHash(token), user.TenantID, user.ID, csrf, result.ExpiresAt)
	return result, token, err
}

func cookieToken(r *http.Request) string {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (s *Store) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: strings.HasPrefix(s.Origin, "https://"), MaxAge: int(sessionLifetime.Seconds()), Expires: time.Now().Add(sessionLifetime)})
}

func (s *Store) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: strings.HasPrefix(s.Origin, "https://"), MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (s *Store) sessionForRequest(r *http.Request) (contracts.Session, error) {
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == SessionCookieName {
			count++
		}
	}
	if count != 1 {
		return contracts.Session{}, problem(401, "unauthenticated", "请重新登录")
	}
	return s.CheckSession(r.Context(), contracts.SessionCheck{Token: cookieToken(r), CSRFToken: r.Header.Get("X-CSRF-Token"), Method: r.Method, Origin: r.Header.Get("Origin")})
}

// mutation rechecks the session and administrator inside the tenant write lock
// so a concurrent disable or role change cannot slip between check and commit.
func (s *Store) mutation(r *http.Request, administrator bool, fn func(*sql.Tx, contracts.User) error) error {
	session, err := s.sessionForRequest(r)
	if err != nil {
		return err
	}
	return s.transaction(r.Context(), func(tx *sql.Tx) error {
		if err := lockTenant(r.Context(), tx, session.User.TenantID); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM af_identity_sessions WHERE token_hash=? AND tenant_id=? AND user_id=? AND expires_at>?", tokenHash(cookieToken(r)), session.User.TenantID, session.User.ID, time.Now().Unix()).Scan(&exists); err != nil {
			return err
		}
		actor, err := scanUser(tx.QueryRowContext(r.Context(), "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id=?", session.User.TenantID, session.User.ID))
		if err != nil {
			return err
		}
		if exists != 1 || !actor.Enabled {
			return problem(401, "unauthenticated", "会话已失效")
		}
		if administrator && !actor.Administrator {
			return problem(403, "administrator_required", "需要租户管理员权限")
		}
		return fn(tx, actor)
	})
}

func (s *Store) adminRead(r *http.Request, fn func(*sql.Tx, contracts.User) error) error {
	session, err := s.sessionForRequest(r)
	if err != nil {
		return err
	}
	return s.transaction(r.Context(), func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(r.Context(), "SELECT authorization_state FROM af_identity_tenants WHERE tenant_id=? FOR SHARE", session.User.TenantID).Scan(&state); err != nil {
			return err
		}
		actor, err := scanUser(tx.QueryRowContext(r.Context(), "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id=?", session.User.TenantID, session.User.ID))
		if err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM af_identity_sessions WHERE token_hash=? AND expires_at>?", tokenHash(cookieToken(r)), time.Now().Unix()).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 || !actor.Enabled {
			return problem(401, "unauthenticated", "会话已失效")
		}
		if !actor.Administrator {
			return problem(403, "administrator_required", "需要租户管理员权限")
		}
		return fn(tx, actor)
	})
}
