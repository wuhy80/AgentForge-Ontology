package identity

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"golang.org/x/crypto/bcrypt"
)

func (s *Store) NewHandler() http.Handler {
	mux := http.NewServeMux()
	wrap := func(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := fn(w, r); err != nil {
				WriteError(w, err)
			}
		}
	}
	mux.HandleFunc("POST /metadata/auth/login", wrap(s.loginHTTP))
	mux.HandleFunc("GET /metadata/auth/session", wrap(s.sessionHTTP))
	mux.HandleFunc("POST /metadata/auth/logout", wrap(s.logoutHTTP))
	mux.HandleFunc("PUT /metadata/auth/password", wrap(s.passwordHTTP))
	s.registerExternalRoutes(mux, wrap)
	mux.HandleFunc("GET /metadata/admin/users", wrap(s.listUsers))
	mux.HandleFunc("POST /metadata/admin/users", wrap(s.createUser))
	mux.HandleFunc("GET /metadata/admin/users/{id}", wrap(s.getUser))
	mux.HandleFunc("PUT /metadata/admin/users/{id}", wrap(s.updateUser))
	mux.HandleFunc("GET /metadata/admin/roles", wrap(s.listRoles))
	mux.HandleFunc("POST /metadata/admin/roles", wrap(s.createRole))
	mux.HandleFunc("GET /metadata/admin/roles/{id}", wrap(s.getRole))
	mux.HandleFunc("PUT /metadata/admin/roles/{id}", wrap(s.updateRole))
	mux.HandleFunc("GET /metadata/admin/user-groups", wrap(s.listUserGroups))
	mux.HandleFunc("POST /metadata/admin/user-groups", wrap(s.createUserGroup))
	mux.HandleFunc("GET /metadata/admin/user-groups/{id}", wrap(s.getUserGroup))
	mux.HandleFunc("PUT /metadata/admin/user-groups/{id}", wrap(s.updateUserGroup))
	mux.HandleFunc("GET /metadata/admin/policy", wrap(s.getPolicy))
	mux.HandleFunc("PUT /metadata/admin/policy", wrap(s.savePolicy))
	mux.HandleFunc("POST /metadata/admin/policy/publish", wrap(s.publishPolicy))
	mux.HandleFunc("POST /metadata/admin/simulate", wrap(s.simulate))
	mux.HandleFunc("GET /metadata/admin/audit", wrap(s.listAudit))
	return mux
}

// NewHandler also permits constructor-style wiring without package internals.
func NewHandler(s *Store) http.Handler { return s.NewHandler() }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// WriteError is shared by the two service-token-protected internal endpoints.
func WriteError(w http.ResponseWriter, err error) {
	var shared *platform.AccessError
	if errors.As(err, &shared) {
		writeJSON(w, shared.Status, contracts.Error{Code: shared.Code, Message: shared.Message})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 404, contracts.Error{Code: "not_found", Message: "未找到当前租户的记录"})
		return
	}
	if strings.Contains(err.Error(), "Error 1062") {
		writeJSON(w, 409, contracts.Error{Code: "name_conflict", Message: "名称已经存在"})
		return
	}
	writeJSON(w, 503, contracts.Error{Code: "identity_unavailable", Message: "无法确认身份或权限状态，请稍后重试"})
}

func decode(r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return problem(400, "invalid_json", "请求格式不符合接口定义")
	}
	if d.Decode(new(any)) != io.EOF {
		return problem(400, "invalid_json", "只允许一个JSON值")
	}
	return nil
}

func match(r *http.Request, etag string) error {
	value := r.Header.Get("If-Match")
	if value == "" {
		return problem(428, "precondition_required", "请携带If-Match并发凭证")
	}
	if value != `"`+etag+`"` {
		return problem(412, "precondition_failed", "记录已改变，请重新读取后核对")
	}
	return nil
}

func (s *Store) loginHTTP(w http.ResponseWriter, r *http.Request) error {
	if err := s.checkOrigin(r.Header.Get("Origin")); err != nil {
		return err
	}
	var input contracts.LoginRequest
	if err := decode(r, &input); err != nil {
		return err
	}
	key := loginKey(r, input)
	if err := s.loginBudget(key); err != nil {
		return err
	}
	session, token, err := s.login(r.Context(), input)
	if err != nil {
		return err
	}
	s.resetLoginBudget(key)
	s.setCookie(w, token)
	writeJSON(w, 200, session)
	return nil
}

func (s *Store) sessionHTTP(w http.ResponseWriter, r *http.Request) error {
	session, err := s.sessionForRequest(r)
	if err != nil {
		return err
	}
	writeJSON(w, 200, session)
	return nil
}

func (s *Store) logoutHTTP(w http.ResponseWriter, r *http.Request) error {
	err := s.mutation(r, false, func(tx *sql.Tx, actor contracts.User) error {
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM af_identity_sessions WHERE token_hash=? AND tenant_id=? AND user_id=?", tokenHash(cookieToken(r)), actor.TenantID, actor.ID); err != nil {
			return err
		}
		return audit(r.Context(), tx, actor.TenantID, actor.ID, "auth.logout", actor.ID)
	})
	if err != nil {
		return err
	}
	s.clearCookie(w)
	w.WriteHeader(204)
	return nil
}

func (s *Store) passwordHTTP(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		Password        string `json:"password"`
	}
	if err := decode(r, &input); err != nil {
		return err
	}
	hash, err := passwordHash(input.Password)
	if err != nil {
		return err
	}
	var session contracts.Session
	var token string
	err = s.mutation(r, false, func(tx *sql.Tx, actor contracts.User) error {
		var old string
		if err := tx.QueryRowContext(r.Context(), "SELECT password_hash FROM af_identity_users WHERE tenant_id=? AND id=?", actor.TenantID, actor.ID).Scan(&old); err != nil {
			return err
		}
		if len(input.CurrentPassword) > 72 || bcrypt.CompareHashAndPassword([]byte(old), []byte(input.CurrentPassword)) != nil {
			return problem(403, "password_mismatch", "当前密码不正确")
		}
		etag, err := randomToken()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE af_identity_users SET password_hash=?,etag=? WHERE tenant_id=? AND id=?", hash, etag, actor.TenantID, actor.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM af_identity_sessions WHERE tenant_id=? AND user_id=?", actor.TenantID, actor.ID); err != nil {
			return err
		}
		actor.ETag = etag
		session, token, err = createSession(r.Context(), tx, actor)
		if err != nil {
			return err
		}
		if err := changeState(r.Context(), tx, actor.TenantID); err != nil {
			return err
		}
		return audit(r.Context(), tx, actor.TenantID, actor.ID, "auth.password", actor.ID)
	})
	if err != nil {
		return err
	}
	s.setCookie(w, token)
	writeJSON(w, 200, session)
	return nil
}
