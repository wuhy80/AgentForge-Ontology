package metadata

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type discoveredCallable struct {
	definition c.Definition
	digest     string
	summary    c.CallableSummary
	state      string
}

func callableBody(d c.Definition) *c.Callable {
	if d.Kind == c.KindFunction {
		return d.Function
	}
	if d.Kind == c.KindAction {
		return d.Action
	}
	return nil
}

func callableParameters(properties []c.Property) []c.CallableParameter {
	result := make([]c.CallableParameter, 0, len(properties))
	for _, p := range properties {
		result = append(result, c.CallableParameter{ID: p.ID, Name: p.Name, DataType: p.DataType,
			Required: p.Required, Nullable: p.Nullable, Precision: p.Precision, Scale: p.Scale,
			Default: append([]byte(nil), p.Default...)})
	}
	return result
}

func projectCallable(d c.Definition) c.CallableSummary {
	body := callableBody(d)
	return c.CallableSummary{ID: d.ID, Kind: d.Kind, CategoryID: d.CategoryID, Name: d.Name,
		Description: d.Description, Input: callableParameters(body.Input), Output: callableParameters(body.Output), SideEffects: body.SideEffects}
}

func (h *handler) callablePermission(ctx context.Context, p c.Principal, d c.Definition) (string, error) {
	if h.cfg.Authorizer == nil {
		return "", problem(503, "authorization_unavailable", "授权服务未配置")
	}
	op := "function.invoke"
	if d.Kind == c.KindAction {
		op = "action.invoke"
	}
	decision, err := h.cfg.Authorizer.Authorize(ctx, p, c.AuthorizationRequest{Operation: op, ResourceID: d.ID, Kind: d.Kind})
	if err != nil {
		var access *platform.AccessError
		if errors.As(err, &access) && (access.Status == 401 || access.Status == 403 || access.Status == 422 || access.Status == 503) {
			return "", problem(access.Status, access.Code, access.Message)
		}
		return "", problem(503, "authorization_unavailable", "无法确认当前调用权限")
	}
	if !decision.Allow {
		return "", problem(403, "forbidden", "无权发现或调用此定义")
	}
	if decision.State == "" {
		return "", problem(503, "authorization_unavailable", "无法确认当前授权状态")
	}
	whole := func(fields []string) bool { return len(fields) == 0 || len(fields) == 1 && fields[0] == "*" }
	if decision.RowFilter != nil || decision.NewRowFilter != nil || !whole(decision.ReadFields) || !whole(decision.WriteFields) {
		return "", problem(422, "unsupported_authorization", "调用签名发现不支持参数字段或行条件授权")
	}
	return decision.State, nil
}

func (h *handler) activeCallable(ctx context.Context, tenant, id string) (c.Definition, string, error) {
	var raw sql.NullString
	var digest string
	err := h.db.QueryRowContext(ctx, "SELECT current_json,current_digest FROM af_meta_definitions WHERE tenant_id=? AND id=? AND kind IN ('action','function') AND status='active' AND current_json IS NOT NULL", tenant, id).Scan(&raw, &digest)
	if err != nil {
		return c.Definition{}, "", err
	}
	d, err := parseDefinition(raw)
	if err == nil && (d.TenantID != tenant || d.ID != id || callableBody(d) == nil) {
		err = problem(503, "invalid_metadata", "无法确认当前调用定义")
	}
	return d, digest, err
}

func inlineCallableAvailable(body *c.Callable) bool {
	if body == nil || body.SideEffects != "none" || len(body.DataWrite) != 0 || body.ArtifactRef != "" || body.FunctionID != "" || c.ValidateScriptRuntime(body.Runtime) != nil {
		return false
	}
	for _, properties := range [][]c.Property{body.Input, body.Output} {
		if len(properties) > 256 {
			return false
		}
		for _, p := range properties {
			if p.EnumID != "" || p.Mapping != nil {
				return false
			}
		}
	}
	return true
}

// A discoverable signature follows the same root and implementation invocation
// checks as Execution. No definition.read grant is needed to learn this minimum.
func (h *handler) inspectCallable(ctx context.Context, p c.Principal, d c.Definition) (c.CallableSummary, string, error) {
	if d.TenantID != p.TenantID || callableBody(d) == nil {
		return c.CallableSummary{}, "", sql.ErrNoRows
	}
	state, err := h.callablePermission(ctx, p, d)
	if err != nil {
		return c.CallableSummary{}, "", err
	}
	body := callableBody(d)
	if d.Kind == c.KindAction && body.FunctionID != "" && body.Runtime == nil && body.SideEffects == "none" && body.ArtifactRef == "" {
		fn, _, err := h.activeCallable(ctx, p.TenantID, body.FunctionID)
		if err != nil {
			return c.CallableSummary{}, "", err
		}
		if fn.Kind != c.KindFunction || !inlineCallableAvailable(fn.Function) || validateCallableBinding(d, fn) != nil {
			return c.CallableSummary{}, "", sql.ErrNoRows
		}
		fnState, err := h.callablePermission(ctx, p, fn)
		if err != nil {
			return c.CallableSummary{}, "", err
		}
		if state != fnState {
			return c.CallableSummary{}, "", problem(409, "authorization_changed", "授权已改变，请重新发现调用签名")
		}
	} else if !inlineCallableAvailable(body) {
		return c.CallableSummary{}, "", sql.ErrNoRows
	}
	return projectCallable(d), state, nil
}

func hiddenCallable(err error) bool {
	var ae *apiError
	return errors.Is(err, sql.ErrNoRows) || errors.As(err, &ae) && ae.status == 403
}

func (h *handler) verifyCallableDiscovery(ctx context.Context, p c.Principal, item discoveredCallable) error {
	d, digest, err := h.activeCallable(ctx, p.TenantID, item.definition.ID)
	if err != nil {
		return err
	}
	_, state, err := h.inspectCallable(ctx, p, d)
	if err != nil {
		return err
	}
	if digest != item.digest || state != item.state {
		return problem(409, "catalog_changed", "当前定义或授权已改变，请重新读取调用目录")
	}
	return nil
}

func (h *handler) describeCallable(w http.ResponseWriter, r *http.Request, p c.Principal) error {
	d, digest, err := h.activeCallable(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	summary, state, err := h.inspectCallable(r.Context(), p, d)
	if hiddenCallable(err) {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	if err := h.verifyCallableDiscovery(r.Context(), p, discoveredCallable{d, digest, summary, state}); err != nil {
		return err
	}
	writeJSON(w, 200, summary)
	return nil
}

func (h *handler) listCallables(w http.ResponseWriter, r *http.Request, p c.Principal) error {
	limit, err := pageLimit(r)
	if err != nil {
		return err
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != string(c.KindFunction) && kind != string(c.KindAction) {
		return problem(400, "invalid_kind", "调用目录kind只能为action或function")
	}
	query := "SELECT current_json,current_digest FROM af_meta_definitions WHERE tenant_id=? AND kind IN ('action','function') AND status='active' AND current_json IS NOT NULL AND id>?"
	args := []any{p.TenantID, r.URL.Query().Get("after")}
	if kind != "" {
		query += " AND kind=?"
		args = append(args, kind)
	}
	if category := r.URL.Query().Get("categoryId"); category != "" {
		query += " AND category_id=?"
		args = append(args, category)
	}
	query += " ORDER BY id LIMIT 1001"
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return err
	}
	// Release the directory connection before checking bound implementations.
	candidates := []discoveredCallable{}
	for rows.Next() {
		var raw sql.NullString
		item := discoveredCallable{}
		if err := rows.Scan(&raw, &item.digest); err != nil {
			rows.Close()
			return err
		}
		item.definition, err = parseDefinition(raw)
		if err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	items := []discoveredCallable{}
	for i, item := range candidates {
		if i == 1000 {
			return problem(422, "directory_budget", "调用目录扫描超过预算，请按分类或类型缩小范围")
		}
		item.summary, item.state, err = h.inspectCallable(r.Context(), p, item.definition)
		if hiddenCallable(err) {
			continue
		}
		if err != nil {
			return err
		}
		items = append(items, item)
		if len(items) > limit {
			break
		}
	}
	result := c.CallableDirectory{Data: []c.CallableSummary{}, HasMore: len(items) > limit}
	if result.HasMore {
		items = items[:limit]
		result.NextCursor = items[len(items)-1].definition.ID
	}
	for _, item := range items {
		if err := h.verifyCallableDiscovery(r.Context(), p, item); err != nil {
			return err
		}
		result.Data = append(result.Data, item.summary)
	}
	writeJSON(w, 200, result)
	return nil
}
