package data

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	mysql "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func (h *handler) registerSources(mux *http.ServeMux) {
	wrap := func(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if err := fn(w, r); err != nil {
				writeError(w, err)
			}
		}
	}
	mux.HandleFunc("GET /data/sources", wrap(h.listSources))
	mux.HandleFunc("POST /data/sources", wrap(h.saveSource))
	mux.HandleFunc("GET /data/sources/{id}", wrap(h.getSourceHTTP))
	mux.HandleFunc("PUT /data/sources/{id}", wrap(h.saveSource))
	mux.HandleFunc("POST /data/sources/test", wrap(h.testSource))
	mux.HandleFunc("POST /data/sources/{id}/test", wrap(h.testSource))
	mux.HandleFunc("GET /data/sources/{id}/tables", wrap(h.listSourceTablesHTTP))
	mux.HandleFunc("GET /data/sources/{id}/tables/{table}", wrap(h.sourceTableHTTP))
}

func (h *handler) sourceAdmin(r *http.Request) (c.Principal, error) {
	p, err := h.principal(r)
	if err != nil {
		return p, err
	}
	u, ok := platform.SessionUser(r)
	if !ok || !u.Enabled || !u.Administrator || u.TenantID != p.TenantID || u.ID != p.Subject {
		return p, problem(403, "administrator_required", "只有当前租户管理员可以维护数据源配置")
	}
	return p, nil
}

func (h *handler) sourceCatalogPrincipal(r *http.Request) (c.Principal, error) {
	p, err := h.principal(r)
	if err != nil {
		return p, err
	}
	if _, err := h.sourceAdmin(r); err == nil {
		return p, nil
	}
	dec, err := h.authorize(r.Context(), p, "definition.edit", "")
	if err != nil {
		return p, err
	}
	if dec.RowFilter != nil || dec.NewRowFilter != nil {
		return p, problem(422, "unsupported_authorization", "数据源目录不支持带行条件的定义写入授权")
	}
	return p, nil
}

func sourceConfig(input c.DataSourceInput) (c.DataSource, error) {
	s := c.DataSource{Name: strings.TrimSpace(input.Name), Driver: strings.ToLower(strings.TrimSpace(input.Driver)), Host: strings.TrimSpace(input.Host), Port: input.Port, Database: strings.TrimSpace(input.Database), Username: strings.TrimSpace(input.Username), TLSMode: input.TLSMode, TenantIsolation: input.TenantIsolation, Enabled: input.Enabled}
	if s.Driver == "postgres" {
		s.Driver = "postgresql"
	}
	if s.Driver != "mysql" && s.Driver != "postgresql" {
		return s, problem(422, "unsupported_driver", "当前数据源支持 MySQL 与 PostgreSQL")
	}
	if s.Port == 0 {
		if s.Driver == "mysql" {
			s.Port = 3306
		} else {
			s.Port = 5432
		}
	}
	if s.TLSMode == "" {
		s.TLSMode = "disable"
	}
	if s.TenantIsolation == "" {
		s.TenantIsolation = "column"
	}
	if s.Name == "" || utf8.RuneCountInString(s.Name) > 128 || strings.IndexFunc(s.Name, unicode.IsControl) >= 0 {
		return s, problem(422, "invalid_source", "数据源名称须为1到128个非控制字符")
	}
	if s.Host == "" || len(s.Host) > 253 || strings.ContainsAny(s.Host, "/\\?#@ ") || strings.IndexFunc(s.Host, unicode.IsControl) >= 0 || s.Port < 1 || s.Port > 65535 {
		return s, problem(422, "invalid_source", "请填写有效的数据库主机和端口")
	}
	if strings.Contains(s.Host, ":") && net.ParseIP(strings.Trim(s.Host, "[]")) == nil {
		return s, problem(422, "invalid_source", "主机不可包含协议、端口或连接参数")
	}
	s.Host = strings.Trim(s.Host, "[]")
	if s.Database == "" || utf8.RuneCountInString(s.Database) > 64 || (s.Driver == "postgresql" && len(s.Database) > 63) || strings.IndexFunc(s.Database, unicode.IsControl) >= 0 || strings.ContainsAny(s.Database, "/\\?&#") {
		return s, problem(422, "invalid_source", "请填写有效的数据库名称")
	}
	if s.Username == "" || len(s.Username) > 128 || strings.IndexFunc(s.Username, unicode.IsControl) >= 0 {
		return s, problem(422, "invalid_source", "请填写有效的数据库用户名")
	}
	if input.Password != nil && len(*input.Password) > 4096 {
		return s, problem(422, "invalid_source", "数据源密码超过长度限制")
	}
	if s.TLSMode != "disable" && s.TLSMode != "require" && s.TLSMode != "verify-full" {
		return s, problem(422, "invalid_source", "TLS模式必须为disable、require或verify-full")
	}
	if s.TenantIsolation != "column" && s.TenantIsolation != "dedicated" {
		return s, problem(422, "invalid_source", "租户隔离必须为column或dedicated")
	}
	return s, nil
}

func (h *handler) sourceCipher() (cipher.AEAD, error) {
	if len(h.cfg.DataSourceKey) < 32 {
		return nil, problem(503, "source_key_unavailable", "数据源加密密钥未配置，请设置独立的ONTOLOGY_DATA_SOURCE_KEY后重启Data服务")
	}
	key := sha256.Sum256([]byte("ontology-data-source-v1\x00" + h.cfg.DataSourceKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (h *handler) encryptSourceSecret(tenant, id, secret string) (string, error) {
	aead, err := h.sourceCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(secret), []byte(tenant+"\x00"+id))
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (h *handler) decryptSourceSecret(tenant, id, encrypted string) (string, error) {
	aead, err := h.sourceCipher()
	if err != nil {
		return "", err
	}
	sealed, err := base64.RawStdEncoding.DecodeString(encrypted)
	if err != nil || len(sealed) < aead.NonceSize()+aead.Overhead() {
		return "", problem(503, "source_secret_unavailable", "数据源凭据无法解密，请核对加密密钥")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(tenant+"\x00"+id))
	if err != nil {
		return "", problem(503, "source_secret_unavailable", "数据源凭据无法解密，请核对加密密钥")
	}
	return string(plain), nil
}

func endpointKey(s c.DataSource) string {
	database := s.Database
	if s.Driver == "mysql" {
		database = strings.ToLower(database)
	}
	return digest([]any{s.Driver, strings.ToLower(strings.TrimSuffix(s.Host, ".")), s.Port, database})
}

func (h *handler) loadSource(ctx context.Context, tenant, id string) (c.DataSource, string, error) {
	var s c.DataSource
	var body, encrypted string
	if !uuidPattern.MatchString(id) {
		return s, "", problem(404, "source_not_found", "当前租户没有此数据源")
	}
	err := h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT config_json,secret_cipher,etag FROM af_data_sources WHERE tenant_id=? AND id=?", tenant, id).Scan(&body, &encrypted, &s.ETag)
	if errors.Is(err, sql.ErrNoRows) {
		return s, "", problem(404, "source_not_found", "当前租户没有此数据源")
	}
	if err != nil {
		return s, "", err
	}
	etag := s.ETag
	if json.Unmarshal([]byte(body), &s) != nil || s.ID != id || s.TenantID != tenant {
		return s, "", problem(503, "source_unavailable", "数据源配置不可读取")
	}
	s.ETag = etag
	return s, encrypted, nil
}

func selectableSource(s c.DataSource) c.DataSource {
	s.Host, s.Username, s.TLSMode, s.Port, s.PasswordSet = "", "", "", 0, false
	return s
}

func (h *handler) listSources(w http.ResponseWriter, r *http.Request) error {
	selection := r.URL.Query().Get("view") == "select"
	var p c.Principal
	var err error
	if selection {
		p, err = h.sourceCatalogPrincipal(r)
	} else {
		p, err = h.sourceAdmin(r)
	}
	if err != nil {
		return err
	}
	rows, err := h.cfg.Engine.DB().QueryContext(r.Context(), "SELECT config_json,etag FROM af_data_sources WHERE tenant_id=? ORDER BY name_key,id LIMIT 101", p.TenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []c.DataSource{}
	for rows.Next() {
		var body, etag string
		if err = rows.Scan(&body, &etag); err != nil {
			return err
		}
		var s c.DataSource
		if json.Unmarshal([]byte(body), &s) != nil || s.TenantID != p.TenantID {
			return problem(503, "source_unavailable", "数据源目录不可读取")
		}
		s.ETag = etag
		if selection {
			if !s.Enabled {
				continue
			}
			s = selectableSource(s)
		}
		items = append(items, s)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(items) > 100 {
		return problem(422, "budget_exceeded", "数据源目录超过100项预算")
	}
	writeJSON(w, 200, map[string]any{"data": items, "hasMore": false})
	return nil
}

func (h *handler) getSourceHTTP(w http.ResponseWriter, r *http.Request) error {
	p, err := h.sourceAdmin(r)
	if err != nil {
		return err
	}
	s, _, err := h.loadSource(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+s.ETag+`"`)
	writeJSON(w, 200, s)
	return nil
}

func (h *handler) saveSource(w http.ResponseWriter, r *http.Request) error {
	p, err := h.sourceAdmin(r)
	if err != nil {
		return err
	}
	var input c.DataSourceInput
	if err = decode(w, r, &input); err != nil {
		return err
	}
	s, err := sourceConfig(input)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	creating := id == ""
	secret := ""
	var old c.DataSource
	var encrypted string
	if creating {
		id = c.NewID()
	} else {
		old, encrypted, err = h.loadSource(r.Context(), p.TenantID, id)
		if err != nil {
			return err
		}
		if r.Header.Get("If-Match") == "" {
			return problem(428, "precondition_required", "修改数据源必须携带If-Match")
		}
		if r.Header.Get("If-Match") != `"`+old.ETag+`"` {
			return problem(412, "precondition_failed", "数据源已改变，请重新读取后核对")
		}
		secret, err = h.decryptSourceSecret(p.TenantID, id, encrypted)
		if err != nil {
			return err
		}
	}
	if input.Password != nil {
		secret = *input.Password
	}
	s.ID, s.TenantID, s.PasswordSet = id, p.TenantID, secret != ""
	s.ETag = digest(c.NewID())
	encrypted, err = h.encryptSourceSecret(p.TenantID, id, secret)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(s)
	key := endpointKey(s)
	err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
		tenantGuard := digest("data-source-tenant:" + p.TenantID)
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO af_data_source_guards (endpoint_key) VALUES (?) ON DUPLICATE KEY UPDATE endpoint_key=endpoint_key", tenantGuard); err != nil {
			return err
		}
		var tenantLock string
		if err := tx.QueryRowContext(r.Context(), "SELECT endpoint_key FROM af_data_source_guards WHERE endpoint_key=? FOR UPDATE", tenantGuard).Scan(&tenantLock); err != nil {
			return err
		}
		if !creating {
			var etag string
			if err := tx.QueryRowContext(r.Context(), "SELECT etag FROM af_data_sources WHERE tenant_id=? AND id=? FOR UPDATE", p.TenantID, id).Scan(&etag); err != nil {
				return err
			}
			if etag != old.ETag {
				return problem(412, "precondition_failed", "数据源已改变，请重新读取后核对")
			}
		}
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO af_data_source_guards (endpoint_key) VALUES (?) ON DUPLICATE KEY UPDATE endpoint_key=endpoint_key", key); err != nil {
			return err
		}
		var guard string
		if err := tx.QueryRowContext(r.Context(), "SELECT endpoint_key FROM af_data_source_guards WHERE endpoint_key=? FOR UPDATE", key).Scan(&guard); err != nil {
			return err
		}
		var conflicts int
		if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM af_data_sources WHERE endpoint_key=? AND tenant_id<>? AND (tenant_isolation='dedicated' OR ?='dedicated')", key, p.TenantID, s.TenantIsolation).Scan(&conflicts); err != nil {
			return err
		}
		if conflicts > 0 {
			return problem(409, "source_isolation_conflict", "该目标数据库已被其他租户登记，不能作为租户专属数据源")
		}
		if creating {
			var count int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM af_data_sources WHERE tenant_id=?", p.TenantID).Scan(&count); err != nil {
				return err
			}
			if count >= 100 {
				return problem(422, "budget_exceeded", "每租户最多登记100个数据源")
			}
			_, err := tx.ExecContext(r.Context(), "INSERT INTO af_data_sources (tenant_id,id,name,name_key,config_json,secret_cipher,endpoint_key,tenant_isolation,etag) VALUES (?,?,?,?,?,?,?,?,?)", p.TenantID, id, s.Name, nameKey(s.Name), string(body), encrypted, key, s.TenantIsolation, s.ETag)
			return err
		}
		// The guard key conservatively folds MySQL database case to catch
		// deployments with case-insensitive physical names. This is not evidence
		// that two differently cased names identify the same actual database.
		if endpointKey(old) != key || old.Database != s.Database || old.TenantIsolation != s.TenantIsolation {
			var bound int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM af_data_gates WHERE tenant_id=? AND (JSON_UNQUOTE(JSON_EXTRACT(definition,'$.schema.storageBinding.dataSourceId'))=? OR JSON_SEARCH(definition,'one',?,NULL,'$.schema.properties[*].mapping.dataSourceId') IS NOT NULL OR JSON_UNQUOTE(JSON_EXTRACT(definition,'$.link.storageBinding.dataSourceId'))=?)", p.TenantID, id, id, id).Scan(&bound); err != nil {
				return err
			}
			if bound > 0 {
				return problem(409, "source_in_use", "已绑定模型的数据源不能改变目标数据库或租户隔离方式，请新建数据源并显式迁移")
			}
		}
		result, err := tx.ExecContext(r.Context(), "UPDATE af_data_sources SET name=?,name_key=?,config_json=?,secret_cipher=?,endpoint_key=?,tenant_isolation=?,etag=? WHERE tenant_id=? AND id=? AND etag=?", s.Name, nameKey(s.Name), string(body), encrypted, key, s.TenantIsolation, s.ETag, p.TenantID, id, old.ETag)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return problem(412, "precondition_failed", "数据源已改变，请重新读取后核对")
		}
		return nil
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+s.ETag+`"`)
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	writeJSON(w, status, s)
	return nil
}

func openSourceConnection(ctx context.Context, s c.DataSource, password string) (*sql.DB, error) {
	var driver, dsn string
	if s.Driver == "mysql" {
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = s.Username, password, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), s.Database
		cfg.ParseTime, cfg.Loc, cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = true, time.UTC, 5*time.Second, 10*time.Second, 10*time.Second
		switch s.TLSMode {
		case "require":
			cfg.TLSConfig = "skip-verify"
		case "verify-full":
			cfg.TLSConfig = "true"
		}
		driver, dsn = "mysql", cfg.FormatDSN()
	} else if s.Driver == "postgresql" {
		u := url.URL{Scheme: "postgres", User: url.UserPassword(s.Username, password), Host: net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), Path: "/" + s.Database}
		q := url.Values{"sslmode": {s.TLSMode}, "connect_timeout": {"5"}, "timezone": {"UTC"}, "application_name": {"ontology-data"}}
		u.RawQuery = q.Encode()
		driver, dsn = "pgx", u.String()
	} else {
		return nil, problem(422, "unsupported_driver", "不支持的数据源类型")
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, problem(422, "invalid_source", "数据源连接配置不合法")
	}
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Minute)
	db.SetConnMaxIdleTime(15 * time.Second)
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = db.PingContext(probe); err != nil {
		db.Close()
		return nil, problem(422, "source_connection_failed", "无法连接数据源，请核对地址、凭据、TLS和数据库权限")
	}
	return db, nil
}

func (h *handler) openSource(ctx context.Context, tenant, id string) (*sql.DB, c.DataSource, error) {
	s, encrypted, err := h.loadSource(ctx, tenant, id)
	if err != nil {
		return nil, s, err
	}
	if !s.Enabled {
		return nil, s, problem(409, "source_disabled", "数据源已停用")
	}
	secret, err := h.decryptSourceSecret(tenant, id, encrypted)
	if err != nil {
		return nil, s, err
	}
	db, err := openSourceConnection(ctx, s, secret)
	return db, s, err
}

func (h *handler) testSource(w http.ResponseWriter, r *http.Request) error {
	p, err := h.sourceAdmin(r)
	if err != nil {
		return err
	}
	var db *sql.DB
	if id := r.PathValue("id"); id != "" {
		db, _, err = h.openSource(r.Context(), p.TenantID, id)
	} else {
		var input c.DataSourceInput
		if err = decode(w, r, &input); err != nil {
			return err
		}
		s, invalid := sourceConfig(input)
		if invalid != nil {
			return invalid
		}
		secret := ""
		if input.Password != nil {
			secret = *input.Password
		}
		db, err = openSourceConnection(r.Context(), s, secret)
	}
	if err != nil {
		return err
	}
	defer db.Close()
	var version string
	if err = db.QueryRowContext(r.Context(), "SELECT VERSION()").Scan(&version); err != nil {
		return problem(422, "source_connection_failed", "数据源已连接但无法读取数据库版本")
	}
	writeJSON(w, 200, c.DataSourceTestResult{Success: true, ServerVersion: version})
	return nil
}
