package data

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type logicalQueryBinding struct {
	c.QueryBinding
	current  c.CurrentDefinition
	decision c.Decision
}

func (h *handler) bindQueryHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	// The request is QuerySpec directly. Preserve the presence of cursor so an
	// explicit empty/null cursor cannot silently become a first-page binding.
	var req struct {
		c.QuerySpec
		Page struct {
			Limit  int             `json:"limit"`
			Cursor json.RawMessage `json:"cursor"`
		} `json:"page"`
	}
	if err = decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if len(req.Page.Cursor) != 0 {
		writeError(w, problem(422, "query_binding_cursor", "查询绑定不能携带运行游标"))
		return
	}
	req.QuerySpec.Page = c.Page{Limit: req.Page.Limit}
	out, err := h.bindQuery(r.Context(), p, req.QuerySpec)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) bindQuery(ctx context.Context, p c.Principal, q c.QuerySpec) (c.QueryBinding, error) {
	if q.Page.Cursor != "" {
		return c.QueryBinding{}, problem(422, "query_binding_cursor", "查询绑定不能携带运行游标")
	}
	bound, err := h.compileBoundQuery(ctx, p, q)
	if err != nil {
		return c.QueryBinding{}, err
	}
	// Binding holds no publication gate and scans no business data. Re-read the
	// authoritative definition and its hydrated Enum context before delivery.
	latest, err := h.current(ctx, p.TenantID, bound.current.Definition.ID)
	if err != nil {
		return c.QueryBinding{}, err
	}
	if latest.ETag != bound.current.ETag || latest.Definition.ID != bound.current.Definition.ID {
		return c.QueryBinding{}, problem(409, "definition_changed", "模型已改变，请重新绑定查询")
	}
	if c.EnumContextDigest(latest.Definition) != c.EnumContextDigest(bound.current.Definition) {
		return c.QueryBinding{}, problem(409, "enum_changed", "模型或枚举已改变，请重新绑定查询")
	}
	if err = h.recheck(ctx, p, "instance.query", bound.current.Definition.ID, bound.decision); err != nil {
		return c.QueryBinding{}, err
	}
	return bound.QueryBinding, nil
}

// compileBoundQuery is the shared logical compiler for public bindings and
// saved-query admission. It never opens a physical source or scans business
// rows. Callers retain their own delivery rechecks and cursor policy.
func (h *handler) compileBoundQuery(ctx context.Context, p c.Principal, q c.QuerySpec) (logicalQueryBinding, error) {
	var out logicalQueryBinding
	raw, err := json.Marshal(q)
	if err != nil {
		return out, err
	}
	var detached c.QuerySpec
	if err = json.Unmarshal(raw, &detached); err != nil {
		return out, err
	}
	q = detached
	if q.Rows != nil || q.Via != nil {
		return out, problem(422, "query_binding_unsupported", "查询绑定首批仅支持单Schema实体或默认平台聚合查询")
	}
	current, err := h.resolve(ctx, p, q.From)
	if err != nil {
		return out, err
	}
	dec, err := h.authorize(ctx, p, "instance.query", current.Definition.ID)
	if err != nil {
		return out, err
	}
	var fields []c.Property
	if q.Aggregate != nil {
		compiled, err := compileAggregateQuery(current.Definition, current.ETag, p, dec, q, h.cfg.CursorSecret)
		if err != nil {
			return out, err
		}
		q, fields = compiled.Bound, compiled.Fields
	} else {
		compiled, err := compileQuery(current.Definition, current.ETag, p, dec, q, h.cfg.CursorSecret)
		if err != nil {
			return out, err
		}
		if err = validateOntologySQLCapabilities(current.Definition, dec, q); err != nil {
			return out, err
		}
		fields = compiled.Projection
		q.From = c.Source{SchemaID: current.Definition.ID}
		for i, key := range q.Select {
			property, _ := findProperty(current.Definition.Schema, key)
			q.Select[i] = property.ID
		}
		for i, order := range q.OrderBy {
			property, _ := findProperty(current.Definition.Schema, order.Field)
			q.OrderBy[i].Field = property.ID
			q.OrderBy[i].Direction = strings.ToLower(order.Direction)
		}
	}
	var bind func(*c.Expression)
	bind = func(e *c.Expression) {
		if e == nil {
			return
		}
		if e.Field != "" {
			property, _ := findProperty(current.Definition.Schema, e.Field)
			e.Field = property.ID
		}
		for i := range e.Args {
			bind(&e.Args[i])
		}
	}
	bind(q.Where)
	out.QueryBinding = c.QueryBinding{Query: q, Fields: safeQueryProperties(fields)}
	out.current, out.decision = current, dec
	return out, nil
}
