package data

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

type Config struct {
	Engine       *storage.Engine
	Authorizer   platform.Authorizer
	MetadataURL  string
	ServiceToken string
	CursorSecret string
	HTTPClient   *http.Client
}

type handler struct {
	cfg    Config
	client *http.Client
}
type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string                    { return e.message }
func problem(status int, code, message string) error { return &apiError{status, code, message} }

func NewHandler(cfg Config) http.Handler {
	h := &handler{cfg: cfg, client: cfg.HTTPClient}
	if h.client == nil {
		h.client = &http.Client{Timeout: 10 * time.Second}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if cfg.Engine == nil || cfg.Engine.DB().PingContext(ctx) != nil {
			writeError(w, problem(503, "not_ready", "数据存储未就绪"))
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /data/capabilities", func(w http.ResponseWriter, r *http.Request) {
		if _, err := h.principal(r); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"backend": "mysql", "schemaModes": []string{"schema_2_db"}, "layouts": []string{"single"}, "operations": []string{"create", "read", "patch", "delete", "query"}, "maxPageSize": 100, "maxSortFields": 1, "links": false, "ontologySQL": false, "batch": false, "calculatedProperties": false})
	})
	mux.HandleFunc("POST /internal/publications/prepare", h.prepare)
	mux.HandleFunc("POST /internal/publications/activate", h.activate)
	mux.HandleFunc("POST /internal/publications/preview", h.preview)
	mux.HandleFunc("POST /data/schemas/{schemaId}/instances", h.create)
	mux.HandleFunc("GET /data/schemas/{schemaId}/instances/{entityId}", h.read)
	mux.HandleFunc("PATCH /data/schemas/{schemaId}/instances/{entityId}", h.patch)
	mux.HandleFunc("DELETE /data/schemas/{schemaId}/instances/{entityId}", h.delete)
	mux.HandleFunc("POST /data/query", h.query)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, err error) {
	var e *apiError
	if errors.As(err, &e) {
		writeJSON(w, e.status, c.Error{Code: e.code, Message: e.message})
		return
	}
	var dbError *mysql.MySQLError
	if errors.As(err, &dbError) {
		switch dbError.Number {
		case 1062:
			writeJSON(w, http.StatusConflict, c.Error{Code: "constraint_conflict", Message: "写入与当前唯一约束冲突"})
			return
		case 1205, 1213:
			writeJSON(w, http.StatusConflict, c.Error{Code: "transaction_conflict", Message: "并发操作冲突，请核对后重试"})
			return
		}
	}
	log.Printf("data operation failed: %T", err)
	writeJSON(w, 500, c.Error{Code: "storage_error", Message: "操作失败；请核对状态后重试"})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return problem(400, "invalid_json", "请求须为合法且无未知字段的 JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return problem(400, "invalid_json", "请求只能包含一个 JSON 值")
	}
	return nil
}
func (h *handler) principal(r *http.Request) (c.Principal, error) {
	p, ok := platform.Principal(r)
	if !ok || p.TenantID == "" || len(p.TenantID) > 128 || p.Subject == "" || len(p.Subject) > 255 {
		return p, problem(401, "unauthorized", "需要可信身份和租户")
	}
	return p, nil
}
func (h *handler) internalTenant(r *http.Request) (string, error) {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(h.cfg.ServiceToken) < 32 || subtle.ConstantTimeCompare([]byte(provided), []byte(h.cfg.ServiceToken)) != 1 {
		return "", problem(401, "unauthorized", "内部认证失败")
	}
	tenant := r.Header.Get("X-Tenant-ID")
	if tenant == "" || len(tenant) > 128 {
		return "", problem(400, "invalid_tenant", "需要内部租户上下文")
	}
	return tenant, nil
}
func (h *handler) authorize(ctx context.Context, p c.Principal, operation, id string) (c.Decision, error) {
	if h.cfg.Authorizer == nil {
		return c.Decision{}, problem(503, "authorization_unavailable", "权限服务未配置")
	}
	dec, err := h.cfg.Authorizer.Authorize(ctx, p, c.AuthorizationRequest{Operation: operation, ResourceID: id, Kind: c.KindSchema})
	if err != nil {
		return dec, problem(503, "authorization_unavailable", "无法确认当前授权")
	}
	if !dec.Allow {
		return dec, problem(403, "forbidden", "不允许该操作")
	}
	if dec.State == "" {
		return dec, problem(503, "authorization_unavailable", "缺少权威授权状态标记")
	}
	return dec, nil
}

func (h *handler) current(ctx context.Context, tenant, id string) (c.CurrentDefinition, error) {
	return h.metadata(ctx, tenant, "/metadata/definitions/"+url.PathEscape(id))
}
func (h *handler) metadata(ctx context.Context, tenant, path string) (c.CurrentDefinition, error) {
	var out c.CurrentDefinition
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.cfg.MetadataURL, "/")+path, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+h.cfg.ServiceToken)
	req.Header.Set("X-Tenant-ID", tenant)
	resp, err := h.client.Do(req)
	if err != nil {
		return out, problem(503, "metadata_unavailable", "无法读取权威当前定义")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return out, problem(404, "not_found", "当前定义不存在")
	}
	if resp.StatusCode != 200 {
		return out, problem(503, "metadata_unavailable", "当前定义不可读取")
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&out) != nil || out.Definition.TenantID != tenant || out.ETag == "" {
		return out, problem(503, "metadata_unavailable", "当前定义响应不合法")
	}
	out.ETag = bareETag(out.ETag)
	if out.Status != "active" {
		return out, problem(409, "definition_unavailable", "定义尚未生效或已停用")
	}
	return out, nil
}
func (h *handler) resolve(ctx context.Context, p c.Principal, source c.Source) (c.CurrentDefinition, error) {
	if source.SchemaID != "" {
		current, err := h.current(ctx, p.TenantID, source.SchemaID)
		if err != nil {
			return current, err
		}
		if source.Category != "" || source.Schema != "" {
			named, e := h.resolve(ctx, p, c.Source{Category: source.Category, Schema: source.Schema})
			if e != nil {
				return named, e
			}
			if named.Definition.ID != source.SchemaID {
				return named, problem(422, "ambiguous_source", "名称与 ID 指向不同 Schema")
			}
		}
		return current, nil
	}
	if source.Category == "" || source.Schema == "" {
		return c.CurrentDefinition{}, problem(422, "invalid_source", "需要完整分类和 Schema 中文名或稳定 ID")
	}
	return h.metadata(ctx, p.TenantID, "/metadata/resolve?"+url.Values{"category": {source.Category}, "name": {source.Schema}, "kind": {"schema"}}.Encode())
}

// Holding this row lock serializes requests against publication admission. A
// publication cannot close the gate until admitted local transactions drain.
func (h *handler) gate(ctx context.Context, tx *sql.Tx, p c.Principal, current c.CurrentDefinition) error {
	var status, actual string
	err := tx.QueryRowContext(ctx, "SELECT status,digest FROM af_data_gates WHERE tenant_id=? AND definition_id=? FOR UPDATE", p.TenantID, current.Definition.ID).Scan(&status, &actual)
	if errors.Is(err, sql.ErrNoRows) {
		return problem(409, "mapping_unavailable", "Schema 物理映射未就绪")
	}
	if err != nil {
		return err
	}
	if status != "ready" || actual != current.ETag {
		return problem(409, "mapping_unavailable", "Schema 正在生效或当前映射已改变，请重试")
	}
	return nil
}
