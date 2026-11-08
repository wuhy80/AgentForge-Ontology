package data

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

const sqlDirectoryBudget = 1000

func (h *handler) registerOntologySQL(mux *http.ServeMux) {
	noStore := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Cache-Control", "no-store"); next(w, r) }
	}
	mux.HandleFunc("GET /data/query/context", noStore(h.queryContextHTTP))
	mux.HandleFunc("GET /data/schemas/{schemaId}/form", noStore(h.formDefinitionHTTP))
	mux.HandleFunc("POST /data/query/ontology-sql/analyze", noStore(h.analyzeOntologySQLHTTP))
	mux.HandleFunc("POST /data/query/ontology-sql/complete", noStore(h.completeOntologySQLHTTP))
	mux.HandleFunc("POST /data/query/ontology-sql/format", noStore(h.formatOntologySQLHTTP))
	mux.HandleFunc("POST /data/query/ontology-sql/insert-field", noStore(h.insertOntologySQLFieldHTTP))
	mux.HandleFunc("POST /data/query/ontology-sql/paths", noStore(h.queryPathsHTTP))
	mux.HandleFunc("POST /data/query/ontology-sql", noStore(h.executeOntologySQLHTTP))
}

func ontologySQLTextHash(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}

func decodeOntologySQL(w http.ResponseWriter, r *http.Request) (c.OntologySQLRequest, error) {
	var req c.OntologySQLRequest
	if err := decode(w, r, &req); err != nil {
		return req, err
	}
	if !utf8.ValidString(req.Text) || len(req.Text) > 64<<10 || len(req.Parameters) > 64 || len(req.RequestID) > 128 || len(req.DefaultCategoryID) > 128 {
		return req, problem(422, "ontology_sql_budget", "查询文本最多 64 KiB、64 个参数；请求标识和分类标识最多 128 字节")
	}
	return req, nil
}

func (h *handler) analyzeOntologySQLHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	req, err := decodeOntologySQL(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := h.analyzeOntologySQL(r, p, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (h *handler) formatOntologySQLHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := h.principal(r); err != nil {
		writeError(w, err)
		return
	}
	req, err := decodeOntologySQL(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	formatted, diagnostics := formatOntologySQL(req.Text)
	writeJSON(w, 200, c.OntologySQLFormat{RequestID: req.RequestID, TextHash: ontologySQLTextHash(req.Text), Text: formatted, Diagnostics: diagnostics})
}

func (h *handler) executeOntologySQLHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	req, err := decodeOntologySQL(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	analysis, err := h.analyzeOntologySQL(r, p, req)
	if err != nil {
		writeError(w, err)
		return
	}
	if !analysis.Ready || analysis.Query == nil {
		message := "查询检查未通过；请修正诊断并填写全部参数"
		if len(analysis.Diagnostics) > 0 {
			message = analysis.Diagnostics[0].Message
		}
		writeError(w, problem(422, "invalid_ontology_sql", message))
		return
	}
	result, err := h.executeQuery(r.Context(), p, *analysis.Query)
	if err != nil {
		writeError(w, err)
		return
	}
	result.Fields = safeQueryProperties(result.Fields)
	writeJSON(w, 200, c.OntologySQLResult{QueryResult: result, Query: *analysis.Query, RequestID: req.RequestID, TextHash: analysis.TextHash})
}

func safeQueryProperties(properties []c.Property) []c.Property {
	out := make([]c.Property, 0, len(properties))
	for _, p := range properties {
		out = append(out, c.Property{ID: p.ID, Name: p.Name, DataType: p.DataType, Nullable: p.Nullable, Required: p.Required, ReadOnly: p.ReadOnly, Precision: p.Precision, Scale: p.Scale})
	}
	return out
}

func sqlAnalysis(req c.OntologySQLRequest, diagnostics []c.SQLDiagnostic) c.OntologySQLAnalysis {
	if diagnostics == nil {
		diagnostics = []c.SQLDiagnostic{}
	}
	return c.OntologySQLAnalysis{RequestID: req.RequestID, TextHash: ontologySQLTextHash(req.Text), Diagnostics: diagnostics, Parameters: []c.SQLParameter{}, Fields: []c.Property{}}
}

func sqlDiagnostic(code, message string, source c.SQLRange) c.SQLDiagnostic {
	return c.SQLDiagnostic{Code: code, Message: message, Severity: "error", Range: source}
}

func hiddenSQLSource(err error) bool {
	var e *apiError
	return errors.As(err, &e) && (e.status == 403 || e.status == 404 || e.code == "definition_unavailable")
}

func (h *handler) analyzeOntologySQL(r *http.Request, p c.Principal, req c.OntologySQLRequest) (c.OntologySQLAnalysis, error) {
	stmt, diagnostics := parseOntologySQL(req.Text)
	out := sqlAnalysis(req, diagnostics)
	if stmt == nil || len(diagnostics) > 0 {
		return out, nil
	}
	if len(stmt.Joins) > 0 {
		return h.analyzeRowOntologySQL(r, p, req, stmt)
	}
	current, err := h.resolveOntologySQLSource(r, p, stmt.SourceCategory, stmt.SourceSchema, req.DefaultCategoryID)
	if err != nil {
		if hiddenSQLSource(err) {
			out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("source_unavailable", "模型不存在或当前不可查询；请从授权模型目录选择", stmt.SourceRange))
			return out, nil
		}
		return out, err
	}
	dec, err := h.authorize(r.Context(), p, "instance.query", current.Definition.ID)
	if err != nil {
		if hiddenSQLSource(err) {
			out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("source_unavailable", "模型不存在或当前不可查询；请从授权模型目录选择", stmt.SourceRange))
			return out, nil
		}
		return out, err
	}
	definition := current.Definition
	if err := validateSchema(definition); err != nil {
		return out, err
	}
	filteredSchema := *definition.Schema
	filteredSchema.Properties = []c.Property{}
	for _, property := range definition.Schema.Properties {
		if fieldAllowed(dec.ReadFields, property.ID) {
			filteredSchema.Properties = append(filteredSchema.Properties, property)
		}
	}
	filtered := definition
	filtered.Schema = &filteredSchema
	out = bindOntologySQL(stmt, filtered, req.Parameters)
	out.RequestID, out.TextHash = req.RequestID, ontologySQLTextHash(req.Text)
	out.Fields = safeQueryProperties(out.Fields)
	if err := validateOntologySQLDecision(definition, dec); err != nil {
		var public *apiError
		if !errors.As(err, &public) {
			return out, err
		}
		out.Diagnostics = append(out.Diagnostics, sqlDiagnostic(public.code, public.message, stmt.SourceRange))
	}
	// Missing parameters do not hide structural capability errors.
	var checkField func(ontologySQLField)
	checkField = func(field ontologySQLField) {
		for _, property := range filteredSchema.Properties {
			if nameKey(property.Name) == nameKey(field.Name) && !primarySQLProperty(definition, property) {
				out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("cross_source_condition_unsupported", "当前仅支持主表属性筛选或排序；此属性可以投影读取", field.Range))
			}
		}
	}
	var checkExpression func(*ontologySQLExpression)
	checkExpression = func(expression *ontologySQLExpression) {
		if expression == nil {
			return
		}
		if expression.Field.Name != "" {
			checkField(expression.Field)
		}
		for _, child := range expression.Args {
			checkExpression(child)
		}
	}
	checkExpression(stmt.Where)
	if stmt.Order != nil {
		checkField(*stmt.Order)
	}
	if len(out.Diagnostics) > 0 {
		out.Ready, out.Query = false, nil
		out.Fields = []c.Property{}
	}
	if out.Query != nil {
		if _, err = compileQuery(definition, current.ETag, p, dec, *out.Query, h.cfg.CursorSecret); err == nil {
			err = validateOntologySQLCapabilities(definition, dec, *out.Query)
		}
		if err != nil {
			var public *apiError
			if !errors.As(err, &public) {
				return out, err
			}
			out.Diagnostics = append(out.Diagnostics, sqlDiagnostic(public.code, public.message, stmt.SourceRange))
			out.Ready, out.Query = false, nil
		}
	}
	return out, nil
}

// Logical capability checks do not open sources or inspect business values.
// Execution additionally validates authoritative catalogs and publication gates.
func primarySQLProperty(d c.Definition, p c.Property) bool {
	if d.Schema.StorageBinding == nil {
		return true
	}
	b, m := d.Schema.StorageBinding, p.Mapping
	return m != nil && m.DataSourceID == b.DataSourceID && (m.Table == "" || m.Table == b.Table) && (m.Namespace == "" || m.Namespace == b.Namespace)
}

func validateOntologySQLCapabilities(d c.Definition, decision c.Decision, q c.QuerySpec) error {
	var check func(*c.Expression) error
	check = func(e *c.Expression) error {
		if e == nil {
			return nil
		}
		if e.Field != "" {
			p, ok := findProperty(d.Schema, e.Field)
			if !ok || !primarySQLProperty(d, p) {
				return problem(422, "cross_source_condition_unsupported", "当前仅支持主表属性筛选、排序及权限行条件；其他来源属性可以投影读取")
			}
		}
		for i := range e.Args {
			if err := check(&e.Args[i]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(q.Where); err != nil {
		return err
	}
	if err := check(decision.RowFilter); err != nil {
		return err
	}
	for _, order := range q.OrderBy {
		p, ok := findProperty(d.Schema, order.Field)
		if !ok || !primarySQLProperty(d, p) {
			return problem(422, "cross_source_condition_unsupported", "当前仅支持按主表属性排序")
		}
	}
	return nil
}

func validateOntologySQLDecision(d c.Definition, decision c.Decision) error {
	compiler := expressionCompiler{schema: d.Schema, allowed: []string{"*"}}
	if _, _, err := compiler.compile(decision.RowFilter, 0); err != nil {
		return err
	}
	return validateOntologySQLCapabilities(d, decision, c.QuerySpec{})
}

// Directory reads retain the caller's identity. External tokens use the narrow
// service-authenticated bridge, which checks that token and runs the same user
// directory authorization; service credentials never substitute for the user.
func (h *handler) sqlDirectoryGET(r *http.Request, path string, out any) error {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(h.cfg.MetadataURL, "/")+path, nil)
	if err != nil {
		return err
	}
	for _, header := range []string{"Cookie", "Authorization"} {
		if value := r.Header.Get(header); value != "" {
			req.Header.Set(header, value)
		}
	}
	if strings.HasPrefix(platform.Bearer(r), "mcp_") {
		check, err := platform.SessionRequest(r)
		if err != nil {
			return problem(401, "unauthenticated", "无法确认外部连接身份")
		}
		body, err := json.Marshal(c.QueryDirectoryRequest{Check: check, Path: path})
		if err != nil {
			return err
		}
		req, err = http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(h.cfg.MetadataURL, "/")+"/internal/metadata/query-directory", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+h.cfg.ServiceToken)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return problem(503, "metadata_unavailable", "无法读取授权查询目录")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var failure c.Error
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&failure)
		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 422 {
			return problem(resp.StatusCode, failure.Code, failure.Message)
		}
		return problem(503, "metadata_unavailable", "授权查询目录不可读取")
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out) != nil {
		return problem(503, "metadata_unavailable", "授权查询目录响应不合法")
	}
	return nil
}

func (h *handler) sqlCategories(r *http.Request, p c.Principal) (map[string]string, error) {
	result, after := map[string]string{}, ""
	for page := 0; page < 6; page++ {
		var list struct {
			Data       []c.Organization `json:"data"`
			HasMore    bool             `json:"hasMore"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := h.sqlDirectoryGET(r, "/metadata/categories?"+url.Values{"limit": {"200"}, "after": {after}}.Encode(), &list); err != nil {
			return nil, err
		}
		for _, category := range list.Data {
			if category.TenantID != p.TenantID || category.ID == "" {
				return nil, problem(503, "metadata_unavailable", "目录租户或身份不一致")
			}
			result[category.ID] = category.Name
		}
		if len(result) > sqlDirectoryBudget {
			break
		}
		if !list.HasMore {
			return result, nil
		}
		if list.NextCursor == "" || list.NextCursor == after {
			return nil, problem(503, "metadata_unavailable", "目录分页响应不合法")
		}
		after = list.NextCursor
	}
	return nil, problem(422, "directory_budget", "分类目录超过当前预算，请缩小目录规模")
}

func (h *handler) resolveOntologySQLSource(r *http.Request, p c.Principal, category, schema, categoryID string) (c.CurrentDefinition, error) {
	if category == "" {
		if categoryID == "" {
			return c.CurrentDefinition{}, problem(404, "source_unavailable", "请选择当前分类或在 FROM 中指定分类")
		}
		categories, err := h.sqlCategories(r, p)
		if err != nil {
			return c.CurrentDefinition{}, err
		}
		category = categories[categoryID]
		if category == "" {
			return c.CurrentDefinition{}, problem(404, "source_unavailable", "分类不可用")
		}
	}
	current, err := h.resolve(r.Context(), p, c.Source{Category: category, Schema: schema})
	if err != nil {
		return current, err
	}
	decision, err := h.authorize(r.Context(), p, "definition.read", current.Definition.ID)
	if err != nil {
		return current, err
	}
	if decision.RowFilter != nil || decision.NewRowFilter != nil {
		return current, problem(422, "unsupported_authorization", "定义目录不支持带行条件的授权")
	}
	return current, nil
}

func (h *handler) queryContextHTTP(w http.ResponseWriter, r *http.Request) {
	// Leave a transport margin within OntoApp's five-second upstream budget.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	limit := 30
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 50 {
			writeError(w, problem(400, "invalid_limit", "查询目录 limit 必须为 1 到 50"))
			return
		}
	}
	values := r.URL.Query()
	if len(values.Get("cursor")) > queryDirectoryCursorReserve {
		writeError(w, problem(400, "invalid_query_context", "目录游标过长"))
		return
	}
	for _, value := range []string{values.Get("categoryId"), values.Get("schemaId"), values.Get("search")} {
		if len(value) > 256 {
			writeError(w, problem(400, "invalid_query_context", "查询目录参数过长"))
			return
		}
	}
	out, err := h.ontologySQLContext(r, p, values.Get("categoryId"), values.Get("schemaId"), values.Get("search"), values.Get("cursor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (h *handler) ontologySQLContext(r *http.Request, p c.Principal, categoryID, schemaID, search, cursor string, limit int) (c.QueryContext, error) {
	out := c.QueryContext{Models: []c.QueryContextModel{}}
	fingerprint := directoryFingerprint(categoryID, schemaID, search, limit)
	after, err := h.directoryAfter(r, p, cursor, fingerprint)
	if err != nil {
		return out, err
	}
	search = nameKey(search)
	scanned, used, state := 0, queryDirectoryCursorReserve+128, ""
	var last c.BusinessDirectoryEntry
	finish := func(more bool) (c.QueryContext, error) {
		if len(out.Models) > 0 {
			if e := h.recheck(r.Context(), p, "instance.query", last.Current.Definition.ID, last.Decision); e != nil {
				return out, e
			}
			if more {
				out.NextCursor, err = h.directoryCursor(p, last.Current, last.Decision, fingerprint)
				if err != nil {
					return out, err
				}
			}
		}
		encoded, e := json.Marshal(out)
		if e != nil || len(encoded)+1 > queryDirectoryMaxBytes {
			return out, problem(422, "directory_budget", "查询目录响应超过字节预算，请缩小范围")
		}
		return out, nil
	}
	for page := 0; page < sqlDirectoryBudget; page++ {
		list, e := h.businessDirectory(r, categoryID, schemaID, after)
		if e != nil {
			return out, e
		}
		scanned += list.Scanned
		if scanned > sqlDirectoryBudget {
			return out, problem(422, "directory_budget", "查询目录超过扫描预算，请指定分类或缩小范围")
		}
		for _, entry := range list.Entries {
			d, decision := entry.Current.Definition, entry.Decision
			if d.TenantID != p.TenantID || entry.Current.ETag == "" || !decision.Allow || decision.State == "" {
				return out, problem(503, "metadata_unavailable", "业务目录身份或授权响应无效")
			}
			if state != "" && state != decision.State {
				return out, problem(409, "authorization_changed", "授权已改变，请重新读取目录")
			}
			state = decision.State
			if entry.Current.Status != "active" || d.Kind != c.KindSchema || d.Schema == nil || schemaID != "" && d.ID != schemaID || categoryID != "" && d.CategoryID != categoryID {
				return out, problem(503, "metadata_unavailable", "业务目录模型响应无效")
			}
			if validateSchema(d) != nil {
				continue
			}
			if e = validateOntologySQLDecision(d, decision); e != nil {
				return out, e
			}
			model := queryContextModel(d, decision, entry.Category)
			if model.Category == "" || len(model.Fields) == 0 || !matchesQueryContext(model, search) {
				continue
			}
			encoded, e := json.Marshal(model)
			if e != nil {
				return out, e
			}
			if len(encoded)+queryDirectoryCursorReserve+128 > queryDirectoryMaxBytes {
				return out, problem(422, "directory_budget", "单个模型超过目录字节预算，不能截断字段")
			}
			if len(out.Models) == limit || used+len(encoded)+1 > queryDirectoryMaxBytes {
				return finish(true)
			}
			used += len(encoded) + 1
			out.Models = append(out.Models, model)
			last = entry
		}
		if !list.HasMore {
			return finish(false)
		}
		if list.NextCursor <= after {
			return out, problem(503, "metadata_unavailable", "业务目录分页未前进")
		}
		after = list.NextCursor
	}
	return out, problem(422, "directory_budget", "查询目录超过扫描预算，请指定分类或缩小范围")
}

func queryContextModel(d c.Definition, decision c.Decision, category string) c.QueryContextModel {
	model := c.QueryContextModel{SchemaID: d.ID, CategoryID: d.CategoryID, Category: category, Name: d.Name, Fields: []c.QueryContextField{}}
	for _, property := range d.Schema.Properties {
		if !fieldAllowed(decision.ReadFields, property.ID) {
			continue
		}
		filterable := primarySQLProperty(d, property)
		model.Fields = append(model.Fields, c.QueryContextField{ID: property.ID, Name: property.Name, DataType: property.DataType, Nullable: property.Nullable, Precision: property.Precision, Scale: property.Scale, Filterable: filterable, Sortable: filterable && property.DataType != "text" && property.DataType != "longtext"})
	}
	return model
}

// Match only the safe model after authorization; a hidden field must not make
// its owning model discoverable through a search hit.
func matchesQueryContext(model c.QueryContextModel, search string) bool {
	search = nameKey(search)
	if search == "" || strings.Contains(nameKey(model.Name), search) || strings.Contains(nameKey(model.Category), search) {
		return true
	}
	for _, field := range model.Fields {
		if strings.Contains(nameKey(field.Name), search) {
			return true
		}
	}
	return false
}
