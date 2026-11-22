package data

import (
	"context"
	"database/sql"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Match definition bindings in persisted plans, excluding SQL source and field IDs.
const savedQueryDefinitionMatch = `JSON_CONTAINS(JSON_EXTRACT(template_json,
 '$.query.from.schemaId', '$.query.relation.linkId', '$.query.rows.nodes[*].schemaId',
 '$.query.rows.joins[*].linkId', '$.parameters[*].enumId'), JSON_QUOTE(?))`

const savedQueryImpactBudget = 200

func (h *handler) savedQueryImpactHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	tenant, err := h.internalTenant(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) || r.URL.RawQuery != "" {
		writeError(w, problem(422, "invalid_definition_id", "需要有效的定义 ID，且不接受查询参数"))
		return
	}
	out, err := savedQueryImpact(r.Context(), h.cfg.Engine.DB(), tenant, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func savedQueryImpact(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, tenant, id string) (c.SavedQueryDefinitionImpact, error) {
	var scanned int
	var out c.SavedQueryDefinitionImpact
	// LIMIT keeps the scan bounded. An over-budget sample cannot certify absence.
	err := db.QueryRowContext(ctx, `SELECT COUNT(*),
 COALESCE(MAX(`+savedQueryDefinitionMatch+`),0)
 FROM (SELECT template_json FROM af_data_saved_queries WHERE tenant_id=?
 ORDER BY user_id,id LIMIT ?) AS templates`, id, tenant, savedQueryImpactBudget+1).Scan(&scanned, &out.Referenced)
	if err != nil {
		return out, err
	}
	if !out.Referenced && scanned > savedQueryImpactBudget {
		return out, problem(422, "saved_query_impact_budget", "租户保存查询超过影响检查预算")
	}
	return out, nil
}
