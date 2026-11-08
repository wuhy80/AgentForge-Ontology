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
	Engine              *storage.Engine
	Authorizer          platform.Authorizer
	SessionChecker      platform.SessionChecker
	MetadataURL         string
	ServiceToken        string
	CursorSecret        string
	HTTPClient          *http.Client
	DataSourceKey       string
	SourceNetworkPolicy *SourceNetworkPolicy
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
	h.registerSources(mux)
	h.registerLinks(mux)
	h.registerOntologySQL(mux)
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
		writeJSON(w, 200, map[string]any{"backend": "mysql", "schemaModes": []string{"schema_2_db", "db_2_schema"}, "layouts": []string{"single"}, "operations": []string{"create", "read", "patch", "update", "delete", "upsert", "bulk_create", "bulk_update", "bulk_delete", "bulk_upsert", "query"}, "sourceDrivers": []string{"mysql", "postgresql"}, "mappedOperations": []string{"read", "query"}, "mappedUpdates": "nullable-fields-and-indexes", "maxMappedTables": maxMappedTables, "crossSourceProjection": true, "crossSourceConditions": false, "crossSourceTransactions": false, "dataTypes": []string{"string", "text", "longtext", "uuid", "int64", "decimal", "boolean", "timestamp"}, "maxTextBytes": c.MaxTextBytes, "maxLongTextBytes": c.MaxLongTextBytes, "maxRequestBytes": c.MaxDataRequestBytes, "maxResponseBytes": c.MaxDataResponseBytes, "maxConcurrentRequests": c.MaxDataConcurrentRequests, "maxQueuedRequests": c.MaxDataQueuedRequests, "maxQueueWaitMS": c.MaxDataQueueWaitMS, "maxPageSize": 100, "maxSortFields": 1, "links": true, "linkStorageModes": []string{"managed", "junction", "foreign_key"}, "linkDirections": []string{"out", "in"}, "maxLinkHops": 1, "rowQueries": true, "maxRowJoins": maxRowQueryJoins, "queryPathPlanning": true, "managedLinkOperations": []string{"create", "query", "delete"}, "mappedLinkOperations": []string{"query"}, "crossSourceLinks": false, "ontologySQL": true, "ontologySQLFeatures": []string{"select", "where", "parameters", "orderBy", "limit", "analyze", "complete", "format", "joinLink", "paths", "insertField"}, "maxOntologySQLBytes": 64 << 10, "maxOntologySQLParameters": 64, "maxOntologySQLCompletions": 50, "batch": true, "writeOperations": []string{"create", "update", "delete", "upsert", "bulk_create", "bulk_update", "bulk_delete", "bulk_upsert"}, "transactions": true, "transactionOperations": []string{"create", "update", "delete", "upsert"}, "transactionStorage": "platform", "maxTransactionItems": c.MaxBulkWriteItems, "perSchemaWriteCapabilities": true, "maxBulkWriteItems": c.MaxBulkWriteItems, "calculatedProperties": false})
	})
	mux.HandleFunc("POST /internal/publications/prepare", h.prepare)
	mux.HandleFunc("POST /internal/publications/activate", h.activate)
	mux.HandleFunc("POST /internal/publications/preview", h.preview)
	mux.HandleFunc("POST /internal/execution/transactions", h.executionTransactionHTTP)
	mux.HandleFunc("POST /data/transactions", h.writeTransactionHTTP(false))
	mux.HandleFunc("POST /data/transactions/status", h.writeTransactionHTTP(true))
	mux.HandleFunc("GET /data/schemas/{schemaId}/storage-mapping", h.storageMappingHTTP)
	mux.HandleFunc("POST /data/schemas/{schemaId}/instances", h.create)
	mux.HandleFunc("GET /data/schemas/{schemaId}/instances/{entityId}", h.read)
	mux.HandleFunc("PATCH /data/schemas/{schemaId}/instances/{entityId}", h.patch)
	mux.HandleFunc("DELETE /data/schemas/{schemaId}/instances/{entityId}", h.delete)
	mux.HandleFunc("POST /data/schemas/{schemaId}/instances/upsert", h.upsertHTTP)
	mux.HandleFunc("POST /data/schemas/{schemaId}/instances/bulk-create", h.bulkWriteHTTP("create"))
	mux.HandleFunc("POST /data/schemas/{schemaId}/instances/bulk-update", h.bulkWriteHTTP("update"))
	mux.HandleFunc("POST /data/schemas/{schemaId}/instances/bulk-delete", h.bulkWriteHTTP("delete"))
	mux.HandleFunc("POST /data/schemas/{schemaId}/instances/bulk-upsert", h.bulkWriteHTTP("upsert"))
	mux.HandleFunc("GET /data/schemas/{schemaId}/write-capabilities", h.writeCapabilitiesHTTP)
	mux.HandleFunc("POST /data/query", h.query)
	return withDataBudgets(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, err := encodeBudgetedJSON(v)
	if err != nil {
		w.Header().Del("ETag")
		var api *apiError
		if errors.As(err, &api) {
			status = api.status
			raw, _ = json.Marshal(c.Error{Code: api.code, Message: api.message})
		} else {
			status = http.StatusInternalServerError
			raw, _ = json.Marshal(c.Error{Code: "response_encoding", Message: "无法安全生成响应，请核对状态后重试"})
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		writeJSON(w, http.StatusGatewayTimeout, c.Error{Code: "request_timeout", Message: "请求超时，请缩小查询范围或核对写入状态后重试"})
		return
	}
	if errors.Is(err, context.Canceled) {
		writeJSON(w, http.StatusRequestTimeout, c.Error{Code: "request_canceled", Message: "请求已取消，请核对状态后重试"})
		return
	}
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
		case 1451:
			writeJSON(w, http.StatusConflict, c.Error{Code: "link_restrict", Message: "对象仍有关联关系，需先移除关系再删除"})
			return
		case 1452:
			writeJSON(w, http.StatusConflict, c.Error{Code: "link_endpoint_conflict", Message: "关系端点已改变或不存在，请重新读取"})
			return
		}
	}
	log.Printf("data operation failed: %T", err)
	writeJSON(w, 500, c.Error{Code: "storage_error", Message: "操作失败；请核对状态后重试"})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, c.MaxDataRequestBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			return problem(413, "request_too_large", "Data 请求体不能超过 8 MiB")
		}
		return problem(400, "invalid_json", "请求须为合法且无未知字段的 JSON")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			return problem(413, "request_too_large", "Data 请求体不能超过 8 MiB")
		}
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
	return h.authorizeKind(ctx, p, operation, id, c.KindSchema)
}

func (h *handler) authorizeKind(ctx context.Context, p c.Principal, operation, id string, kind c.Kind) (c.Decision, error) {
	if h.cfg.Authorizer == nil {
		return c.Decision{}, problem(503, "authorization_unavailable", "权限服务未配置")
	}
	dec, err := h.cfg.Authorizer.Authorize(ctx, p, c.AuthorizationRequest{Operation: operation, ResourceID: id, Kind: kind})
	if err != nil {
		var access *platform.AccessError
		if errors.As(err, &access) && (access.Status == 401 || access.Status == 403 || access.Status == 422 || access.Status == 503) {
			return dec, problem(access.Status, access.Code, access.Message)
		}
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
