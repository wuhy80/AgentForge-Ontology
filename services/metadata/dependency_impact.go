package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

const (
	impactDefinitionBudget = 1000
	impactJSONBudget       = 1 << 20
	impactByteBudget       = 8 << 20
	impactEdgeBudget       = 4096
	impactResultBudget     = 200
)

type impactDefinition struct {
	d       contracts.Definition
	summary contracts.DefinitionDependencySummary
	refs    map[string][]string
}

// Each reason describes a logical reference, never a physical mapping or code.
func definitionReferenceReasons(d contracts.Definition) map[string][]string {
	refs := map[string][]string{}
	add := func(id, reason string) {
		if id == "" {
			return
		}
		for _, old := range refs[id] {
			if old == reason {
				return
			}
		}
		refs[id] = append(refs[id], reason)
	}
	props := func(items []contracts.Property) {
		for _, item := range items {
			add(item.EnumID, "enum")
		}
	}
	if d.Schema != nil {
		props(d.Schema.Properties)
	}
	if d.Link != nil {
		add(d.Link.SourceSchemaID, "link-source")
		add(d.Link.TargetSchemaID, "link-target")
	}
	for _, c := range []*contracts.Callable{d.Action, d.Function} {
		if c == nil {
			continue
		}
		props(c.Input)
		props(c.Output)
		for _, id := range c.TargetSchemaIDs {
			add(id, "action-target")
		}
		for _, item := range c.DataWrite {
			add(item.SchemaID, "data-write")
		}
		add(c.FunctionID, "implementation")
	}
	for id := range refs {
		sort.Strings(refs[id])
	}
	return refs
}

func (h *handler) impactAuthorized(ctx context.Context, p contracts.Principal, d impactDefinition) error {
	op := "definition.read"
	if d.summary.View == "draft" {
		op = "definition.edit"
	}
	return h.authorize(ctx, p, op, d.d.ID, d.d.Kind)
}

func impactForbidden(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == http.StatusForbidden
}

func impactBudgetError() error {
	return problem(422, "dependency_budget", "定义依赖查看超过当前预算，未返回部分结果")
}

// Inspect lengths before transferring JSON, within the same read-only snapshot.
// Otherwise Rows.Close could drain an unbounded result even after our byte limit
// was reached. No tenant write guard, Data call or migration occurs.
func (h *handler) impactSnapshot(ctx context.Context, tenant string) ([]impactDefinition, error) {
	tx, err := h.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var count int
	var bytes, maxJSON int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(current_size+draft_size),0),COALESCE(MAX(GREATEST(current_size,draft_size)),0)
 FROM (SELECT COALESCE(OCTET_LENGTH(current_json),0) current_size,COALESCE(OCTET_LENGTH(draft_json),0) draft_size
 FROM af_meta_definitions WHERE tenant_id=? ORDER BY id LIMIT ?) sizes`, tenant, impactDefinitionBudget+1).Scan(&count, &bytes, &maxJSON)
	if err != nil {
		return nil, err
	}
	if count > impactDefinitionBudget || bytes > impactByteBudget || maxJSON > impactJSONBudget {
		return nil, impactBudgetError()
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,kind,current_json,current_digest,draft_json,draft_digest
 FROM af_meta_definitions WHERE tenant_id=? ORDER BY id LIMIT ?`, tenant, impactDefinitionBudget+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []impactDefinition{}
	edges := 0
	for rows.Next() {
		var id, currentETag, draftETag string
		var kind contracts.Kind
		var current, draft sql.NullString
		if err := rows.Scan(&id, &kind, &current, &currentETag, &draft, &draftETag); err != nil {
			return nil, err
		}
		for _, item := range []struct {
			raw        sql.NullString
			etag, view string
		}{{current, currentETag, "current"}, {draft, draftETag, "draft"}} {
			if !item.raw.Valid {
				continue
			}
			d, err := parseDefinition(item.raw)
			if err != nil {
				return nil, err
			}
			if d.ID != id || d.TenantID != tenant || d.Kind != kind {
				return nil, fmt.Errorf("inconsistent definition snapshot")
			}
			refs := definitionReferenceReasons(d)
			edges += len(refs)
			if edges > impactEdgeBudget {
				return nil, impactBudgetError()
			}
			result = append(result, impactDefinition{d: d, refs: refs, summary: contracts.DefinitionDependencySummary{ID: id, Kind: kind, Name: d.Name, CategoryID: d.CategoryID, GroupID: d.GroupID, View: item.view, ETag: item.etag}})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *handler) dependencyImpact(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return problem(400, "invalid_query", "依赖查看查询参数格式不合法")
	}
	view := query.Get("view")
	if view == "" {
		view = "current"
	}
	if view != "current" && view != "draft" {
		return problem(400, "invalid_view", "view只能为current或draft")
	}
	for key, values := range query {
		if key != "view" || len(values) != 1 {
			return problem(400, "invalid_query", "依赖查看只接受单个view参数")
		}
	}
	id := r.PathValue("id")
	var kind contracts.Kind
	var present bool
	if err := h.db.QueryRowContext(r.Context(), `SELECT kind,CASE WHEN ?='draft' THEN draft_json IS NOT NULL ELSE current_json IS NOT NULL END FROM af_meta_definitions WHERE tenant_id=? AND id=?`, view, p.TenantID, id).Scan(&kind, &present); err != nil {
		return err
	}
	root := impactDefinition{d: contracts.Definition{ID: id, Kind: kind}, summary: contracts.DefinitionDependencySummary{View: view}}
	if err := h.impactAuthorized(r.Context(), p, root); err != nil {
		if impactForbidden(err) {
			return sql.ErrNoRows
		}
		return err
	}
	if !present {
		return sql.ErrNoRows
	}
	definitions, err := h.impactSnapshot(r.Context(), p.TenantID)
	if err != nil {
		return err
	}
	found := false
	for _, d := range definitions {
		if d.d.ID == id && d.summary.View == view {
			root = d
			found = true
			break
		}
	}
	if !found {
		return problem(409, "dependency_context_changed", "定义已变化，请重新读取依赖")
	}
	result := contracts.DefinitionDependencyImpact{Scope: "direct-definition-references", Definition: root.summary, Dependencies: []contracts.DefinitionDependency{}, Dependents: []contracts.DefinitionDependency{}}
	visible := []impactDefinition{root}
	for _, d := range definitions {
		outgoing := root.refs[d.d.ID]
		incoming := d.refs[id]
		if (len(outgoing) == 0 || d.summary.View != "current") && len(incoming) == 0 {
			continue
		}
		if err := h.impactAuthorized(r.Context(), p, d); err != nil {
			if impactForbidden(err) {
				continue
			}
			return err
		}
		visible = append(visible, d)
		if len(outgoing) != 0 && d.summary.View == "current" {
			result.Dependencies = append(result.Dependencies, contracts.DefinitionDependency{Definition: d.summary, Reasons: outgoing})
		}
		if len(incoming) != 0 {
			result.Dependents = append(result.Dependents, contracts.DefinitionDependency{Definition: d.summary, Reasons: incoming})
		}
		if len(result.Dependencies) > impactResultBudget || len(result.Dependents) > impactResultBudget {
			return impactBudgetError()
		}
	}
	// A successful earlier decision is not a persistent grant. On revocation or
	// failed authorization, discard the entire report before writing any bytes.
	for _, d := range visible {
		if err := h.impactAuthorized(r.Context(), p, d); err != nil {
			if impactForbidden(err) {
				return problem(409, "dependency_context_changed", "依赖可见范围已变化，请重新读取")
			}
			return err
		}
	}
	writeJSON(w, 200, result)
	return nil
}
