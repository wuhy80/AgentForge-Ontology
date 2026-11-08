package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

const maxConnections = 100

func (s *Store) registerExternalRoutes(mux *http.ServeMux, wrap func(func(http.ResponseWriter, *http.Request) error) http.HandlerFunc) {
	mux.HandleFunc("GET /metadata/auth/connections", wrap(s.listConnections))
	mux.HandleFunc("POST /metadata/auth/connections", wrap(s.createConnection))
	mux.HandleFunc("DELETE /metadata/auth/connections/{id}", wrap(s.revokeConnection))
	mux.HandleFunc("POST /metadata/auth/oauth/preview", wrap(s.oauthPreview))
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.oauthMetadata)
	mux.HandleFunc("GET /oauth/authorize", wrap(s.oauthAuthorize))
	mux.HandleFunc("POST /metadata/auth/oauth/authorize", wrap(s.oauthConsent))
	mux.HandleFunc("POST /oauth/token", s.oauthToken)
	mux.HandleFunc("POST /oauth/register", s.oauthRegister)
	mux.HandleFunc("POST /oauth/revoke", s.oauthRevoke)
}

func (s *Store) ConfigureExternal(issuer, resource string) error {
	if issuer == "" {
		issuer = s.Origin
	}
	if resource == "" {
		resource = "http://127.0.0.1:18084/mcp"
	}
	issuer = strings.TrimRight(issuer, "/")
	if !validPublicURL(issuer, false) || !validPublicURL(resource, true) {
		return fmt.Errorf("OAuth issuer与MCP resource须使用HTTPS或本机回环HTTP完整URL，不能含用户信息、查询或片段")
	}
	s.OAuthIssuer, s.MCPResource = issuer, resource
	return nil
}

func validPublicURL(raw string, allowPath bool) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 1024 && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (allowPath || u.Path == "" || u.Path == "/") && (u.Scheme == "https" || u.Scheme == "http" && loopbackHost(u.Hostname()))
}
func loopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func normalizeScopes(input []string) ([]string, error) {
	seen := map[string]bool{}
	if len(input) == 0 || len(input) > 3 {
		return nil, problem(422, "invalid_scope", "请选择1到3个有效接入范围")
	}
	for _, scope := range input {
		if scope != platform.ScopeDiscover && scope != platform.ScopeQuery && scope != platform.ScopeInvoke || seen[scope] {
			return nil, problem(422, "invalid_scope", "接入范围未知或重复")
		}
		seen[scope] = true
	}
	if !seen[platform.ScopeDiscover] {
		return nil, problem(422, "invalid_scope", "查询与调用需要同时选择模型发现范围")
	}
	out := append([]string{}, input...)
	sort.Strings(out)
	return out, nil
}

func accessRoleHash(ctx context.Context, tx *sql.Tx, user c.User) (string, error) {
	roles, err := effectiveRoleIDs(ctx, tx, user)
	if err != nil {
		return "", err
	}
	sort.Strings(roles)
	return tokenHash(marshal(roles)), nil
}

func (s *Store) checkAccess(ctx context.Context, check c.SessionCheck) (c.Session, error) {
	var result c.Session
	required, allowed := platform.AccessRoute(check.Method, check.Path)
	if check.Token != "" || !platform.ValidAccessToken(check.AccessToken) || !allowed || s.MCPResource == "" || check.Resource != "" && check.Resource != s.MCPResource || check.Path == "/mcp" && check.Resource != s.MCPResource {
		return result, problem(401, "invalid_token", "外部连接凭据或目标资源无效")
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var tenant, userID, resource, userETag, roleHash, scopes string
		var revoked int64
		err := tx.QueryRowContext(ctx, "SELECT id,tenant_id,user_id,resource,user_etag,role_hash,scopes,expires_at,revoked_at FROM af_identity_access_connections WHERE token_hash=?", tokenHash(check.AccessToken)).Scan(&result.AccessTokenID, &tenant, &userID, &resource, &userETag, &roleHash, &scopes, &result.ExpiresAt, &revoked)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (revoked != 0 || result.ExpiresAt <= time.Now().Unix() || resource != s.MCPResource) {
			return problem(401, "invalid_token", "外部连接已失效，请重新授权")
		}
		if err != nil {
			return err
		}
		user, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id=?", tenant, userID))
		if errors.Is(err, sql.ErrNoRows) || err == nil && (!user.Enabled || user.ETag != userETag) {
			return problem(401, "invalid_token", "账号已改变，请重新授权连接")
		}
		if err != nil {
			return err
		}
		currentRoles, err := accessRoleHash(ctx, tx, user)
		if err != nil {
			return err
		}
		if currentRoles != roleHash {
			return problem(401, "invalid_token", "角色或用户组已改变，请重新授权连接")
		}
		if json.Unmarshal([]byte(scopes), &result.AccessScopes) != nil {
			return problem(503, "identity_unavailable", "连接配置无法确认")
		}
		if _, err = normalizeScopes(result.AccessScopes); err != nil {
			return problem(503, "identity_unavailable", "连接范围无效")
		}
		if !platform.HasAccessScope(result.AccessScopes, required) {
			return problem(403, "insufficient_scope", "外部连接范围不足")
		}
		result.User = user
		return nil
	})
	return result, err
}

func scanConnection(row scanner) (c.AccessConnection, error) {
	var out c.AccessConnection
	var scopes string
	err := row.Scan(&out.ID, &out.Name, &scopes, &out.CreatedAt, &out.ExpiresAt, &out.RevokedAt, &out.Kind, &out.ClientID)
	if err == nil {
		err = json.Unmarshal([]byte(scopes), &out.Scopes)
	}
	return out, err
}

const connectionColumns = "id,name,scopes,created_at,expires_at,revoked_at,kind,client_id"

func (s *Store) listConnections(w http.ResponseWriter, r *http.Request) error {
	session, err := s.sessionForRequest(r)
	if err != nil {
		return err
	}
	result := []c.AccessConnection{}
	rows, err := s.DB.QueryContext(r.Context(), "SELECT "+connectionColumns+" FROM af_identity_access_connections WHERE tenant_id=? AND user_id=? ORDER BY created_at DESC,id LIMIT 101", session.User.TenantID, session.User.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		entry, e := scanConnection(rows)
		if e != nil {
			return e
		}
		result = append(result, entry)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(result) > maxConnections {
		return problem(422, "connection_budget", "连接目录超过预算，请联系管理员清理")
	}
	// Recheck after the read so account/session changes do not return stale data.
	if _, err = s.sessionForRequest(r); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"data": result, "issuer": s.OAuthIssuer, "resource": s.MCPResource})
	return nil
}

func (s *Store) issueConnection(ctx context.Context, tx *sql.Tx, user c.User, name string, scopes []string, seconds int64, kind, clientID string) (c.AccessConnectionCreated, error) {
	var out c.AccessConnectionCreated
	if s.MCPResource == "" {
		return out, problem(503, "external_auth_unavailable", "外部连接尚未配置")
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM af_identity_access_connections WHERE tenant_id=? AND user_id=?", user.TenantID, user.ID).Scan(&count); err != nil {
		return out, err
	}
	// Expired and revoked rows are bounded history, pruned only as part of an
	// explicit credential mutation, never at service startup or normal queries.
	if count >= maxConnections {
		if _, err := tx.ExecContext(ctx, "DELETE FROM af_identity_access_connections WHERE tenant_id=? AND user_id=? AND (revoked_at>0 OR expires_at<=?)", user.TenantID, user.ID, time.Now().Unix()); err != nil {
			return out, err
		}
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM af_identity_access_connections WHERE tenant_id=? AND user_id=?", user.TenantID, user.ID).Scan(&count); err != nil {
			return out, err
		}
		if count >= maxConnections {
			return out, problem(422, "connection_budget", "最多保留100个有效连接，请先撤销旧连接")
		}
	}
	roleHash, err := accessRoleHash(ctx, tx, user)
	if err != nil {
		return out, err
	}
	raw, err := randomToken()
	if err != nil {
		return out, err
	}
	out.AccessToken = "mcp_" + raw
	now := time.Now().Unix()
	out.Connection = c.AccessConnection{ID: c.NewID(), Name: name, Scopes: scopes, CreatedAt: now, ExpiresAt: now + seconds, Kind: kind, ClientID: clientID}
	_, err = tx.ExecContext(ctx, "INSERT INTO af_identity_access_connections (id,tenant_id,user_id,name,token_hash,scopes,resource,kind,client_id,user_etag,role_hash,created_at,expires_at,revoked_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,0)", out.Connection.ID, user.TenantID, user.ID, name, tokenHash(out.AccessToken), marshal(scopes), s.MCPResource, kind, clientID, user.ETag, roleHash, now, out.Connection.ExpiresAt)
	if err != nil {
		return c.AccessConnectionCreated{}, err
	}
	if err = audit(ctx, tx, user.TenantID, user.ID, "connection.create", out.Connection.ID); err != nil {
		return c.AccessConnectionCreated{}, err
	}
	return out, nil
}

func (s *Store) createConnection(w http.ResponseWriter, r *http.Request) error {
	var input c.AccessConnectionInput
	if err := decode(r, &input); err != nil {
		return err
	}
	name, _, err := normalizedName(input.Name)
	if err != nil {
		return err
	}
	if !utf8.ValidString(name) {
		return problem(422, "invalid_name", "连接名称无效")
	}
	scopes, err := normalizeScopes(input.Scopes)
	if err != nil {
		return err
	}
	if input.ExpiresInSeconds < 3600 || input.ExpiresInSeconds > 30*24*3600 {
		return problem(422, "invalid_expiry", "连接有效期必须为1小时到30天")
	}
	var out c.AccessConnectionCreated
	err = s.mutation(r, false, func(tx *sql.Tx, user c.User) error {
		var e error
		out, e = s.issueConnection(r.Context(), tx, user, name, scopes, input.ExpiresInSeconds, "personal", "")
		return e
	})
	if err != nil {
		return err
	}
	writeJSON(w, 201, out)
	return nil
}

func (s *Store) revokeConnection(w http.ResponseWriter, r *http.Request) error {
	err := s.mutation(r, false, func(tx *sql.Tx, user c.User) error {
		result, err := tx.ExecContext(r.Context(), "UPDATE af_identity_access_connections SET revoked_at=? WHERE id=? AND tenant_id=? AND user_id=?", time.Now().Unix(), r.PathValue("id"), user.TenantID, user.ID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var exists int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM af_identity_access_connections WHERE id=? AND tenant_id=? AND user_id=?", r.PathValue("id"), user.TenantID, user.ID).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return problem(404, "not_found", "未找到当前账号的连接")
			}
		}
		return audit(r.Context(), tx, user.TenantID, user.ID, "connection.revoke", r.PathValue("id"))
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func oauthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
func (s *Store) oauthMetadata(w http.ResponseWriter, r *http.Request) {
	issuer := strings.TrimRight(s.OAuthIssuer, "/")
	if issuer == "" {
		oauthError(w, 503, "server_error")
		return
	}
	writeJSON(w, 200, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/oauth/authorize", "token_endpoint": issuer + "/oauth/token", "registration_endpoint": issuer + "/oauth/register", "revocation_endpoint": issuer + "/oauth/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{platform.ScopeDiscover, platform.ScopeQuery, platform.ScopeInvoke}, "token_endpoint_auth_methods_supported": []string{"none"}})
}
func validRedirect(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && len(raw) <= 2048 && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path != "" && (u.Scheme == "https" || u.Scheme == "http" && loopbackHost(u.Hostname()))
}
func (s *Store) oauthRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name                    string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	if err := decode(r, &in); err != nil || len(in.Name) == 0 || len(in.Name) > 128 || len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 {
		oauthError(w, 400, "invalid_client_metadata")
		return
	}
	if in.TokenEndpointAuthMethod != "" && in.TokenEndpointAuthMethod != "none" || len(in.GrantTypes) > 0 && (len(in.GrantTypes) != 1 || in.GrantTypes[0] != "authorization_code") || len(in.ResponseTypes) > 0 && (len(in.ResponseTypes) != 1 || in.ResponseTypes[0] != "code") {
		oauthError(w, 400, "invalid_client_metadata")
		return
	}
	for _, u := range in.RedirectURIs {
		if !validRedirect(u) {
			oauthError(w, 400, "invalid_redirect_uri")
			return
		}
	}
	id := c.NewID()
	now := time.Now().Unix()
	exp := now + 365*24*3600
	if _, err := s.DB.ExecContext(r.Context(), "INSERT INTO af_identity_oauth_clients (id,name,redirect_uris,created_at,expires_at) VALUES (?,?,?,?,?)", id, in.Name, marshal(in.RedirectURIs), now, exp); err != nil {
		oauthError(w, 503, "server_error")
		return
	}
	writeJSON(w, 201, map[string]any{"client_id": id, "client_name": in.Name, "redirect_uris": in.RedirectURIs, "client_id_issued_at": now, "token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code"}, "response_types": []string{"code"}})
}
func (s *Store) oauthClient(ctx context.Context, id, redirect string) (bool, error) {
	if !validRedirect(redirect) || id == "" {
		return false, nil
	}
	var raw string
	var exp int64
	err := s.DB.QueryRowContext(ctx, "SELECT redirect_uris,expires_at FROM af_identity_oauth_clients WHERE id=?", id).Scan(&raw, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if exp <= time.Now().Unix() {
		return false, nil
	}
	var list []string
	if json.Unmarshal([]byte(raw), &list) != nil {
		return false, nil
	}
	for _, v := range list {
		if subtle.ConstantTimeCompare([]byte(v), []byte(redirect)) == 1 {
			return true, nil
		}
	}
	return false, nil
}
func (s *Store) oauthAuthorize(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	in := c.OAuthAuthorizationInput{ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), ResponseType: q.Get("response_type"), CodeChallenge: q.Get("code_challenge"), CodeChallengeMethod: q.Get("code_challenge_method"), Scope: q.Get("scope"), State: q.Get("state"), Resource: q.Get("resource")}
	ok, err := s.oauthClient(r.Context(), in.ClientID, in.RedirectURI)
	if err != nil {
		return err
	}
	if !ok || in.ResponseType != "code" || in.CodeChallengeMethod != "S256" || len(in.CodeChallenge) != 43 || in.Resource != "" && in.Resource != s.MCPResource {
		return problem(400, "invalid_request", "OAuth请求无效")
	}
	if in.Resource == "" {
		in.Resource = s.MCPResource
	}
	if _, err := normalizeScopes(strings.Fields(in.Scope)); err != nil {
		return err
	}
	// The UI performs a consent POST using the authenticated workbench session.
	params := url.Values{}
	params.Set("client_id", in.ClientID)
	params.Set("redirect_uri", in.RedirectURI)
	params.Set("response_type", "code")
	params.Set("code_challenge", in.CodeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("scope", in.Scope)
	params.Set("resource", in.Resource)
	params.Set("state", in.State)
	redirect := strings.TrimRight(s.Origin, "/") + "/#oauth/authorize?" + params.Encode()
	http.Redirect(w, r, redirect, http.StatusFound)
	return nil
}

func (s *Store) decodeOAuthJSON(r *http.Request) (c.OAuthAuthorizationInput, error) {
	var in c.OAuthAuthorizationInput
	if err := decode(r, &in); err != nil {
		return in, err
	}
	if in.ResponseType == "" {
		in.ResponseType = "code"
	}
	if in.CodeChallengeMethod == "" {
		in.CodeChallengeMethod = "S256"
	}
	if in.Resource == "" {
		in.Resource = s.MCPResource
	}
	return in, nil
}
func (s *Store) validateOAuthInput(ctx context.Context, in c.OAuthAuthorizationInput) (c.OAuthClientSummary, []string, error) {
	ok, err := s.oauthClient(ctx, in.ClientID, in.RedirectURI)
	if err != nil {
		return c.OAuthClientSummary{}, nil, err
	}
	if !ok || in.ResponseType != "code" || in.CodeChallengeMethod != "S256" || len(in.CodeChallenge) != 43 || in.Resource != s.MCPResource {
		return c.OAuthClientSummary{}, nil, problem(400, "invalid_request", "OAuth请求无效")
	}
	var name, raw string
	var exp int64
	if err = s.DB.QueryRowContext(ctx, "SELECT name,redirect_uris,expires_at FROM af_identity_oauth_clients WHERE id=?", in.ClientID).Scan(&name, &raw, &exp); err != nil || exp <= time.Now().Unix() {
		return c.OAuthClientSummary{}, nil, problem(400, "invalid_client", "OAuth客户端无效")
	}
	var redirects []string
	if json.Unmarshal([]byte(raw), &redirects) != nil {
		return c.OAuthClientSummary{}, nil, problem(503, "server_error", "客户端注册信息无效")
	}
	scopes, err := normalizeScopes(strings.Fields(in.Scope))
	if err != nil {
		return c.OAuthClientSummary{}, nil, err
	}
	return c.OAuthClientSummary{ClientID: in.ClientID, Name: name, RedirectURIs: redirects}, scopes, nil
}
func (s *Store) oauthPreview(w http.ResponseWriter, r *http.Request) error {
	in, err := s.decodeOAuthJSON(r)
	if err != nil {
		return err
	}
	client, scopes, err := s.validateOAuthInput(r.Context(), in)
	if err != nil {
		return err
	}
	if _, err = s.sessionForRequest(r); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"client": client, "scopes": scopes, "resource": s.MCPResource})
	return nil
}
func (s *Store) oauthConsent(w http.ResponseWriter, r *http.Request) error {
	in, err := s.decodeOAuthJSON(r)
	if err != nil {
		return err
	}
	client, scopes, err := s.validateOAuthInput(r.Context(), in)
	if err != nil {
		return err
	}
	if _, err := s.sessionForRequest(r); err != nil {
		return err
	}
	if !in.Approve {
		return writeOAuthRedirect(w, in.RedirectURI, in.State, "access_denied", "")
	}
	code, err := randomToken()
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	if err = s.mutation(r, false, func(tx *sql.Tx, user c.User) error {
		roleHash, e := accessRoleHash(r.Context(), tx, user)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(r.Context(), "INSERT INTO af_identity_oauth_codes (code_hash,tenant_id,user_id,user_etag,role_hash,client_id,redirect_uri,code_challenge,scopes,resource,expires_at,consumed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,0)", tokenHash(code), user.TenantID, user.ID, user.ETag, roleHash, client.ClientID, in.RedirectURI, in.CodeChallenge, marshal(scopes), s.MCPResource, now+300)
		return e
	}); err != nil {
		return err
	}
	return writeOAuthRedirect(w, in.RedirectURI, in.State, "", code)
}
func writeOAuthRedirect(w http.ResponseWriter, redirect, state, oauthErr, code string) error {
	u, err := url.Parse(redirect)
	if err != nil {
		return problem(400, "invalid_redirect_uri", "回调地址无效")
	}
	q := u.Query()
	if oauthErr != "" {
		q.Set("error", oauthErr)
	} else {
		q.Set("code", code)
	}
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	writeJSON(w, 200, map[string]string{"redirectUri": u.String()})
	return nil
}
func pkce(challenge, verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, ch := range verifier {
		if !(ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || strings.ContainsRune("-._~", ch)) {
			return false
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(challenge), []byte(base64.RawURLEncoding.EncodeToString(sum[:]))) == 1
}
func (s *Store) oauthToken(w http.ResponseWriter, r *http.Request) {
	var outToken string
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	if r.Form.Get("grant_type") != "authorization_code" {
		oauthError(w, 400, "invalid_grant")
		return
	}
	code := r.Form.Get("code")
	if len(code) < 32 {
		oauthError(w, 400, "invalid_grant")
		return
	}
	var tenant, userID, userETag, roleHash, clientID, redirect, challenge, scopes, resource string
	var issuedScopes []string
	var exp, consumed int64
	err := s.transaction(r.Context(), func(tx *sql.Tx) error {
		row := tx.QueryRowContext(r.Context(), "SELECT tenant_id,user_id,user_etag,role_hash,client_id,redirect_uri,code_challenge,scopes,resource,expires_at,consumed_at FROM af_identity_oauth_codes WHERE code_hash=? FOR UPDATE", tokenHash(code))
		if e := row.Scan(&tenant, &userID, &userETag, &roleHash, &clientID, &redirect, &challenge, &scopes, &resource, &exp, &consumed); e != nil {
			return e
		}
		if consumed != 0 || exp <= time.Now().Unix() || clientID != r.Form.Get("client_id") || redirect != r.Form.Get("redirect_uri") || resource != r.Form.Get("resource") || !pkce(challenge, r.Form.Get("code_verifier")) {
			return problem(400, "invalid_grant", "授权码无效或已使用")
		}
		if _, e := tx.ExecContext(r.Context(), "UPDATE af_identity_oauth_codes SET consumed_at=? WHERE code_hash=? AND consumed_at=0", time.Now().Unix(), tokenHash(code)); e != nil {
			return e
		}
		user, e := scanUser(tx.QueryRowContext(r.Context(), "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id=?", tenant, userID))
		if e != nil || !user.Enabled || user.ETag != userETag {
			return problem(400, "invalid_grant", "账号已改变，请重新授权")
		}
		current, e := accessRoleHash(r.Context(), tx, user)
		if e != nil || current != roleHash {
			return problem(400, "invalid_grant", "权限已改变，请重新授权")
		}
		var sc []string
		if e := json.Unmarshal([]byte(scopes), &sc); e != nil {
			return e
		}
		if _, e := normalizeScopes(sc); e != nil {
			return e
		}
		issuedScopes = sc
		created, e := s.issueConnection(r.Context(), tx, user, "OAuth "+clientID, sc, int64(time.Hour/time.Second), "oauth", clientID)
		if e != nil {
			return e
		}
		outToken = created.AccessToken
		return nil
	})
	if err != nil {
		oauthError(w, 400, "invalid_grant")
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": outToken, "token_type": "Bearer", "expires_in": 3600, "scope": strings.Join(issuedScopes, " "), "resource": resource})
}

func (s *Store) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	token := r.Form.Get("token")
	if !platform.ValidAccessToken(token) {
		w.WriteHeader(200)
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), "UPDATE af_identity_access_connections SET revoked_at=? WHERE token_hash=?", time.Now().Unix(), tokenHash(token)); err != nil {
		oauthError(w, 503, "server_error")
		return
	}
	w.WriteHeader(200)
}
