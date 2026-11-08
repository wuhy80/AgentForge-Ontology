package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

type Config struct {
	Engine         *storage.Engine
	Authorizer     platform.Authorizer
	SessionChecker platform.SessionChecker
	DataURL        string
	ServiceToken   string
	HTTPClient     *http.Client
}

type handler struct {
	cfg    Config
	db     *sql.DB
	client *http.Client
}
type internalReaderKey struct{}
type internalDraftReaderKey struct{}
type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string                    { return e.code + ": " + e.message }
func problem(status int, code, message string) error { return &apiError{status, code, message} }

func NewHandler(cfg Config) http.Handler {
	h := &handler{cfg: cfg, db: cfg.Engine.DB(), client: cfg.HTTPClient}
	if h.client == nil {
		h.client = &http.Client{Timeout: 30 * time.Second}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/metadata/business-directory", h.businessDirectoryHTTP)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.db.PingContext(ctx); err != nil {
			writeProblem(w, problem(503, "not_ready", "存储不可用"))
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /metadata/capabilities", h.wrap(h.capabilities))
	mux.HandleFunc("GET /metadata/callables", h.wrap(h.listCallables))
	mux.HandleFunc("GET /metadata/callables/{id}", h.wrap(h.describeCallable))
	mux.HandleFunc("POST /metadata/categories", h.wrap(h.createCategory))
	mux.HandleFunc("GET /metadata/categories", h.wrap(h.listCategories))
	mux.HandleFunc("POST /metadata/groups", h.wrap(h.createGroup))
	mux.HandleFunc("GET /metadata/groups", h.wrap(h.listGroups))
	mux.HandleFunc("POST /metadata/definitions", h.wrap(h.createDefinition))
	mux.HandleFunc("GET /metadata/definitions", h.wrap(h.listDefinitions))
	mux.HandleFunc("GET /metadata/resolve", h.wrap(h.resolve))
	mux.HandleFunc("GET /metadata/definitions/{id}", h.wrap(h.getCurrent))
	mux.HandleFunc("GET /metadata/definitions/{id}/draft", h.wrap(h.getDraft))
	mux.HandleFunc("PUT /metadata/definitions/{id}/draft", h.wrap(h.saveDraft))
	mux.HandleFunc("POST /metadata/definitions/{id}/preview", h.wrap(h.preview))
	mux.HandleFunc("POST /metadata/definitions/{id}/publish", h.wrap(h.publish))
	mux.HandleFunc("GET /metadata/publications/{id}", h.wrap(h.getPublication))
	mux.HandleFunc("GET /metadata/schemas/{id}/links", h.wrap(h.links))
	return mux
}

type endpoint func(http.ResponseWriter, *http.Request, contracts.Principal) error

func (h *handler) wrap(next endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := platform.Principal(r)
		if !ok && platform.EqualSecret(platform.Bearer(r), h.cfg.ServiceToken) {
			currentPath := r.PathValue("id") != "" && r.URL.Path == "/metadata/definitions/"+r.PathValue("id")
			draftPath := r.PathValue("id") != "" && r.URL.Path == "/metadata/definitions/"+r.PathValue("id")+"/draft"
			if r.Method != http.MethodGet || (!currentPath && !draftPath && r.URL.Path != "/metadata/resolve") {
				writeProblem(w, problem(403, "forbidden", "服务凭据仅可读取定义"))
				return
			}
			p = contracts.Principal{Subject: "__metadata_service__", TenantID: r.Header.Get("X-Tenant-ID")}
			ok = p.TenantID != ""
			r = r.WithContext(context.WithValue(r.Context(), internalReaderKey{}, true))
			if draftPath {
				r = r.WithContext(context.WithValue(r.Context(), internalDraftReaderKey{}, true))
			}
		}
		if !ok || p.TenantID == "" || len(p.TenantID) > 128 {
			writeProblem(w, problem(401, "unauthenticated", "需要可信身份与租户上下文"))
			return
		}
		if err := next(w, r, p); err != nil {
			writeProblem(w, err)
		}
	}
}

func (h *handler) authorize(ctx context.Context, p contracts.Principal, op, id string, kind contracts.Kind) error {
	if internal, _ := ctx.Value(internalReaderKey{}).(bool); internal && op == "definition.read" {
		return nil
	}
	if internal, _ := ctx.Value(internalDraftReaderKey{}).(bool); internal && op == "definition.edit" {
		// Set exclusively for the exact internal GET draft path. Execution must
		// separately check the real caller's edit and invocation permissions.
		return nil
	}
	if h.cfg.Authorizer == nil {
		return problem(503, "authorization_unavailable", "授权服务未配置")
	}
	d, err := h.cfg.Authorizer.Authorize(ctx, p, contracts.AuthorizationRequest{Operation: op, ResourceID: id, Kind: kind})
	if err != nil {
		var access *platform.AccessError
		if errors.As(err, &access) && (access.Status == 401 || access.Status == 403 || access.Status == 422 || access.Status == 503) {
			return problem(access.Status, access.Code, access.Message)
		}
		return problem(503, "authorization_unavailable", "无法确认当前授权")
	}
	if !d.Allow {
		return problem(403, "forbidden", "无权执行该操作")
	}
	if d.RowFilter != nil || d.NewRowFilter != nil {
		return problem(422, "unsupported_authorization", "定义管理尚不支持带行条件的授权；不能忽略条件执行")
	}
	return nil
}

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return problem(400, "invalid_json", "请求必须为已声明结构的JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return problem(400, "invalid_json", "只允许一个JSON值")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeProblem(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.status, contracts.Error{Code: ae.code, Message: ae.message})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 404, contracts.Error{Code: "not_found", Message: "未找到当前租户可用的对象"})
		return
	}
	// SQL details can include values and physical names and must not escape.
	if strings.Contains(err.Error(), "Error 1062") {
		writeJSON(w, 409, contracts.Error{Code: "name_conflict", Message: "名称或标识已存在"})
		return
	}
	writeJSON(w, 500, contracts.Error{Code: "internal_error", Message: "操作失败，请通过追踪记录核对"})
}

func etagHeader(s string) string { return `"` + s + `"` }
func matchHeader(r *http.Request, expected string) error {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" {
		return problem(428, "precondition_required", "必须携带If-Match并发凭证")
	}
	if v != etagHeader(expected) {
		return problem(412, "precondition_failed", "内容已改变，请重新读取")
	}
	return nil
}

func (h *handler) capabilities(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	if err := h.authorize(r.Context(), p, "definition.read", "", ""); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"definitions": []string{"schema", "enum", "link", "action", "function"}, "lifecycle": []string{"draft", "active"}, "definitionUpdates": "display-and-mapped-additions", "schemaModes": []string{"schema_2_db", "db_2_schema"}, "storageLayouts": []string{"single"}, "callableRuntimes": []string{"python"}, "invocation": false, "delete": false, "disable": false, "schemaInheritance": false, "dbToSchema": true})
	return nil
}

func (h *handler) withTenantTx(ctx context.Context, tenant, publicationID string, fn func(*sql.Tx) error) error {
	return h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO af_meta_tenant_guards (tenant_id) VALUES (?)", tenant); err != nil {
			return err
		}
		var active string
		if err := tx.QueryRowContext(ctx, "SELECT publication_id FROM af_meta_tenant_guards WHERE tenant_id=? FOR UPDATE", tenant).Scan(&active); err != nil {
			return err
		}
		if active != "" && active != publicationID || publicationID != "" && active != publicationID {
			return problem(409, "publication_in_progress", "该租户发布状态已改变，请先读取权威生效状态")
		}
		return fn(tx)
	})
}

func (h *handler) createCategory(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	return h.createOrganization(w, r, p, "category")
}
func (h *handler) createGroup(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	return h.createOrganization(w, r, p, "group")
}
func (h *handler) createOrganization(w http.ResponseWriter, r *http.Request, p contracts.Principal, kind string) error {
	if err := h.authorize(r.Context(), p, "organization.edit", "", ""); err != nil {
		return err
	}
	var org contracts.Organization
	if err := decode(r, &org); err != nil {
		return err
	}
	if org.ID != "" || org.TenantID != "" && org.TenantID != p.TenantID {
		return problem(400, "invalid_identity", "ID由服务端分配，tenantId必须与身份一致")
	}
	name, key, err := normalizeName(org.Name)
	if err != nil {
		return err
	}
	org.Name = name
	org.ID = contracts.NewID()
	org.TenantID = p.TenantID
	if kind == "category" && org.CategoryID != "" || kind == "group" && org.CategoryID == "" {
		return problem(400, "invalid_organization", "分类与分组归属不合法")
	}
	err = h.withTenantTx(r.Context(), p.TenantID, "", func(tx *sql.Tx) error {
		if kind == "group" {
			var id string
			if err := tx.QueryRowContext(r.Context(), "SELECT id FROM af_meta_organizations WHERE tenant_id=? AND id=? AND kind='category'", p.TenantID, org.CategoryID).Scan(&id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(r.Context(), "INSERT INTO af_meta_organizations (id,tenant_id,kind,parent_id,name,name_key) VALUES (?,?,?,?,?,?)", org.ID, p.TenantID, kind, org.CategoryID, org.Name, []byte(key))
		return err
	})
	if err != nil {
		return err
	}
	writeJSON(w, 201, org)
	return nil
}

func (h *handler) listCategories(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	return h.listOrganizations(w, r, p, "category")
}
func (h *handler) listGroups(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	return h.listOrganizations(w, r, p, "group")
}
func pageLimit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 100, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 200 {
		return 0, problem(400, "invalid_limit", "limit必须为1到200")
	}
	return n, nil
}
func (h *handler) listOrganizations(w http.ResponseWriter, r *http.Request, p contracts.Principal, kind string) error {
	if err := h.authorize(r.Context(), p, "organization.read", "", ""); err != nil {
		return err
	}
	limit, err := pageLimit(r)
	if err != nil {
		return err
	}
	query := "SELECT id,parent_id,name FROM af_meta_organizations WHERE tenant_id=? AND kind=? AND id>?"
	args := []any{p.TenantID, kind, r.URL.Query().Get("after")}
	if kind == "group" {
		if r.URL.Query().Get("categoryId") == "" {
			return problem(400, "category_required", "分组列表必须指定categoryId")
		}
		query += " AND parent_id=?"
		args = append(args, r.URL.Query().Get("categoryId"))
	}
	query += " ORDER BY id LIMIT ?"
	args = append(args, limit+1)
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	data := []contracts.Organization{}
	for rows.Next() {
		org := contracts.Organization{TenantID: p.TenantID}
		if err := rows.Scan(&org.ID, &org.CategoryID, &org.Name); err != nil {
			return err
		}
		data = append(data, org)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	more := len(data) > limit
	if more {
		data = data[:limit]
	}
	after := ""
	if more {
		after = data[len(data)-1].ID
	}
	writeJSON(w, 200, map[string]any{"data": data, "hasMore": more, "nextCursor": after})
	return nil
}

func validateOrg(ctx context.Context, tx *sql.Tx, d contracts.Definition) error {
	var parent string
	if err := tx.QueryRowContext(ctx, "SELECT parent_id FROM af_meta_organizations WHERE tenant_id=? AND id=? AND kind='group'", d.TenantID, d.GroupID).Scan(&parent); err != nil {
		return err
	}
	if parent != d.CategoryID {
		return problem(400, "invalid_organization", "分组不属于指定分类")
	}
	return nil
}

type definitionRow struct {
	d                                  contracts.Definition
	current, draft                     sql.NullString
	currentDigest, draftDigest, status string
}
type rowScanner interface{ Scan(...any) error }

func scanDefinition(row rowScanner) (definitionRow, error) {
	var v definitionRow
	err := row.Scan(&v.d.ID, &v.d.TenantID, &v.d.CategoryID, &v.d.GroupID, &v.d.Kind, &v.d.Name, &v.current, &v.currentDigest, &v.draft, &v.draftDigest, &v.status)
	return v, err
}

const definitionColumns = "id,tenant_id,category_id,group_id,kind,name,current_json,current_digest,draft_json,draft_digest,status"

func parseDefinition(raw sql.NullString) (contracts.Definition, error) {
	var d contracts.Definition
	if !raw.Valid {
		return d, sql.ErrNoRows
	}
	if err := json.Unmarshal([]byte(raw.String), &d); err != nil {
		return d, fmt.Errorf("corrupt stored definition: %w", err)
	}
	return d, nil
}

func (h *handler) createDefinition(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	var d contracts.Definition
	if err := decode(r, &d); err != nil {
		return err
	}
	if d.ID != "" || d.TenantID != "" && d.TenantID != p.TenantID {
		return problem(400, "invalid_identity", "ID由服务端分配，tenantId必须与身份一致")
	}
	if err := h.authorize(r.Context(), p, "definition.edit", "", d.Kind); err != nil {
		return err
	}
	d.ID = contracts.NewID()
	d.TenantID = p.TenantID
	if err := canonicalDefinition(&d, true); err != nil {
		return err
	}
	digest := digestDefinition(d)
	raw, _ := json.Marshal(d)
	_, key, _ := normalizeName(d.Name)
	err := h.withTenantTx(r.Context(), p.TenantID, "", func(tx *sql.Tx) error {
		if err := validateOrg(r.Context(), tx, d); err != nil {
			return err
		}
		if err := validateActionTargetReferences(r.Context(), tx, d); err != nil {
			return err
		}
		if err := validateLinkReferences(r.Context(), tx, d); err != nil {
			return err
		}
		_, err := tx.ExecContext(r.Context(), "INSERT INTO af_meta_definitions (id,tenant_id,category_id,group_id,kind,name,name_key,draft_json,draft_digest,status) VALUES (?,?,?,?,?,?,?,?,?,'draft')", d.ID, d.TenantID, d.CategoryID, d.GroupID, d.Kind, d.Name, []byte(key), string(raw), digest)
		return err
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etagHeader(digest))
	writeJSON(w, 201, contracts.CurrentDefinition{Definition: d, ETag: digest, Status: "draft"})
	return nil
}

func (h *handler) getCurrent(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	return h.getDefinition(w, r, p, false)
}
func (h *handler) getDraft(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	return h.getDefinition(w, r, p, true)
}
func (h *handler) getDefinition(w http.ResponseWriter, r *http.Request, p contracts.Principal, draft bool) error {
	row, err := scanDefinition(h.db.QueryRowContext(r.Context(), "SELECT "+definitionColumns+" FROM af_meta_definitions WHERE tenant_id=? AND id=?", p.TenantID, r.PathValue("id")))
	if err != nil {
		return err
	}
	op := "definition.read"
	if draft {
		op = "definition.edit"
	}
	if err := h.authorize(r.Context(), p, op, row.d.ID, row.d.Kind); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.status == 403 {
			return sql.ErrNoRows
		}
		return err
	}
	raw, digest, status := row.current, row.currentDigest, row.status
	if draft {
		raw, digest, status = row.draft, row.draftDigest, "draft"
	}
	d, err := parseDefinition(raw)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etagHeader(digest))
	writeJSON(w, 200, contracts.CurrentDefinition{Definition: d, ETag: digest, Status: status})
	return nil
}

func (h *handler) saveDraft(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	var d contracts.Definition
	if err := decode(r, &d); err != nil {
		return err
	}
	id := r.PathValue("id")
	if d.ID != "" && d.ID != id || d.TenantID != "" && d.TenantID != p.TenantID {
		return problem(400, "invalid_identity", "请求身份与路由不一致")
	}
	d.ID = id
	d.TenantID = p.TenantID
	if err := canonicalDefinition(&d, true); err != nil {
		return err
	}
	if err := h.authorize(r.Context(), p, "definition.edit", id, d.Kind); err != nil {
		return err
	}
	digest := digestDefinition(d)
	raw, _ := json.Marshal(d)
	err := h.withTenantTx(r.Context(), p.TenantID, "", func(tx *sql.Tx) error {
		row, err := scanDefinition(tx.QueryRowContext(r.Context(), "SELECT "+definitionColumns+" FROM af_meta_definitions WHERE tenant_id=? AND id=? FOR UPDATE", p.TenantID, id))
		if err != nil {
			return err
		}
		expected := row.draftDigest
		if expected == "" {
			expected = row.currentDigest
		}
		if err := matchHeader(r, expected); err != nil {
			return err
		}
		if row.d.Kind != d.Kind || row.d.CategoryID != d.CategoryID || row.d.GroupID != d.GroupID {
			return problem(422, "unsupported_change", "首批不支持定义改变类型或组织归属")
		}
		if err := validateOrg(r.Context(), tx, d); err != nil {
			return err
		}
		if err := validateActionTargetReferences(r.Context(), tx, d); err != nil {
			return err
		}
		if err := validateLinkReferences(r.Context(), tx, d); err != nil {
			return err
		}
		_, err = tx.ExecContext(r.Context(), "UPDATE af_meta_definitions SET draft_json=?,draft_digest=? WHERE tenant_id=? AND id=?", string(raw), digest, p.TenantID, id)
		return err
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etagHeader(digest))
	writeJSON(w, 200, contracts.CurrentDefinition{Definition: d, ETag: digest, Status: "draft"})
	return nil
}

func (h *handler) listDefinitions(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	// Paging is derived from authorized objects; no hidden object identity is
	// exposed as a continuation marker. Large sparse grants fail boundedly.
	const scanBudget = 1000
	view := r.URL.Query().Get("view")
	if view != "" && view != "current" && view != "draft" {
		return problem(400, "invalid_view", "view只能为current或draft")
	}
	op, columns, present := "definition.read", "current_json,current_digest,status", "current_json"
	if view == "draft" {
		op, columns, present = "definition.edit", "draft_json,draft_digest,'draft'", "draft_json"
	}
	if err := h.authorize(r.Context(), p, op, "", ""); err != nil {
		return err
	}
	limit, err := pageLimit(r)
	if err != nil {
		return err
	}
	// The column fragments above are fixed service constants, never user input.
	query := "SELECT " + columns + " FROM af_meta_definitions WHERE tenant_id=? AND " + present + " IS NOT NULL AND id>?"
	args := []any{p.TenantID, r.URL.Query().Get("after")}
	for _, filter := range []string{"categoryId", "kind"} {
		if value := r.URL.Query().Get(filter); value != "" {
			col := "category_id"
			if filter == "kind" {
				col = "kind"
			}
			query += " AND " + col + "=?"
			args = append(args, value)
		}
	}
	query += " ORDER BY id LIMIT ?"
	args = append(args, scanBudget+1)
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return err
	}
	data := []contracts.CurrentDefinition{}
	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > scanBudget {
			rows.Close()
			return problem(422, "directory_budget", "授权目录扫描超过当前预算，请按分类或类型缩小范围")
		}
		var raw sql.NullString
		v := contracts.CurrentDefinition{}
		if err := rows.Scan(&raw, &v.ETag, &v.Status); err != nil {
			rows.Close()
			return err
		}
		v.Definition, err = parseDefinition(raw)
		if err != nil {
			rows.Close()
			return err
		}
		if err := h.authorize(r.Context(), p, op, v.Definition.ID, v.Definition.Kind); err != nil {
			var ae *apiError
			if errors.As(err, &ae) && ae.status == 403 {
				continue
			}
			rows.Close()
			return err
		}
		data = append(data, v)
		if len(data) > limit {
			break
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	more := len(data) > limit
	if more {
		data = data[:limit]
	}
	after := ""
	if more {
		after = data[len(data)-1].Definition.ID
	}
	writeJSON(w, 200, map[string]any{"data": data, "hasMore": more, "nextCursor": after})
	return nil
}

func (h *handler) resolve(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	_, category, err := normalizeName(r.URL.Query().Get("category"))
	if err != nil {
		return problem(400, "category_required", "必须指定分类中文名称")
	}
	_, name, err := normalizeName(r.URL.Query().Get("name"))
	if err != nil {
		return err
	}
	var raw sql.NullString
	v := contracts.CurrentDefinition{}
	err = h.db.QueryRowContext(r.Context(), `SELECT d.current_json,d.current_digest,d.status FROM af_meta_definitions d JOIN af_meta_organizations c ON c.id=d.category_id AND c.tenant_id=d.tenant_id WHERE d.tenant_id=? AND c.kind='category' AND c.name_key=? AND d.name_key=? AND d.current_json IS NOT NULL`, p.TenantID, []byte(category), []byte(name)).Scan(&raw, &v.ETag, &v.Status)
	if err != nil {
		return err
	}
	v.Definition, err = parseDefinition(raw)
	if err != nil {
		return err
	}
	if kind := r.URL.Query().Get("kind"); kind != "" && kind != string(v.Definition.Kind) {
		return sql.ErrNoRows
	}
	if err := h.authorize(r.Context(), p, "definition.read", v.Definition.ID, v.Definition.Kind); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.status == 403 {
			return sql.ErrNoRows
		}
		return err
	}
	w.Header().Set("ETag", etagHeader(v.ETag))
	writeJSON(w, 200, v)
	return nil
}

func (h *handler) links(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	id := r.PathValue("id")
	if err := h.authorize(r.Context(), p, "definition.read", id, contracts.KindSchema); err != nil {
		return err
	}
	var exists string
	if err := h.db.QueryRowContext(r.Context(), "SELECT id FROM af_meta_definitions WHERE tenant_id=? AND id=? AND kind='schema' AND current_json IS NOT NULL", p.TenantID, id).Scan(&exists); err != nil {
		return err
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT d.current_json FROM af_meta_definitions d
 WHERE d.tenant_id=? AND d.kind='link' AND d.current_json IS NOT NULL
 AND EXISTS (SELECT 1 FROM af_meta_dependencies dep WHERE dep.tenant_id=d.tenant_id AND dep.source_id=d.id AND dep.target_id=?)
 ORDER BY d.id LIMIT 1001`, p.TenantID, id)
	if err != nil {
		return err
	}
	defs := []contracts.Definition{}
	for rows.Next() {
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		d, err := parseDefinition(raw)
		if err != nil {
			rows.Close()
			return err
		}
		defs = append(defs, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(defs) > 1000 {
		return problem(422, "budget_exceeded", "关系目录超过当前预算")
	}
	data := []map[string]any{}
	for _, d := range defs {
		if err := h.authorize(r.Context(), p, "definition.read", d.ID, d.Kind); err != nil {
			var ae *apiError
			if errors.As(err, &ae) && ae.status == 403 {
				continue
			}
			return err
		}
		l := d.Link
		if l == nil {
			return problem(500, "corrupt_definition", "关系定义损坏")
		}
		other := l.TargetSchemaID
		if other == id {
			other = l.SourceSchemaID
		}
		if err := h.authorize(r.Context(), p, "definition.read", other, contracts.KindSchema); err != nil {
			var ae *apiError
			if errors.As(err, &ae) && ae.status == 403 {
				continue
			}
			return err
		}
		if l.SourceSchemaID == id {
			data = append(data, map[string]any{"linkRef": map[string]string{"id": d.ID}, "direction": "out", "name": l.ForwardName, "sourceSchemaId": l.SourceSchemaID, "targetSchemaId": l.TargetSchemaID, "cardinality": l.Cardinality})
		}
		if l.TargetSchemaID == id {
			reverse := l.Cardinality
			if reverse == "1:N" {
				reverse = "N:1"
			} else if reverse == "N:1" {
				reverse = "1:N"
			}
			data = append(data, map[string]any{"linkRef": map[string]string{"id": d.ID}, "direction": "in", "name": l.ReverseName, "sourceSchemaId": l.TargetSchemaID, "targetSchemaId": l.SourceSchemaID, "cardinality": reverse})
		}
	}
	writeJSON(w, 200, map[string]any{"data": data})
	return nil
}
