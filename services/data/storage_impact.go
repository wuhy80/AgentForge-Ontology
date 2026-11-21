package data

import (
	"database/sql"
	"errors"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) storageImpactHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	impact, err := h.storageImpact(r)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, impact)
}

func (h *handler) storageImpact(r *http.Request) (c.StorageDefinitionImpact, error) {
	var out c.StorageDefinitionImpact
	tenant, err := h.internalTenant(r)
	if err != nil {
		return out, err
	}
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) || r.URL.RawQuery != "" {
		return out, problem(422, "invalid_definition_id", "需要有效的定义 ID，且不接受查询参数")
	}
	ctx := r.Context()
	current, err := h.current(ctx, tenant, id)
	if err != nil {
		return out, err
	}
	d := current.Definition
	if d.ID != id {
		return out, problem(503, "metadata_unavailable", "当前定义与请求不一致")
	}
	var table string
	switch {
	case d.Kind == c.KindSchema && d.Schema != nil && d.Schema.Mode == "schema_2_db" && d.Schema.StorageLayout == "single" && d.Schema.StorageBinding == nil:
		table = tableName(id)
	case d.Kind == c.KindLink && d.Link != nil && d.Link.StorageBinding != nil && d.Link.StorageBinding.Mode == "managed":
		table = linkTableName(id)
	default:
		return out, problem(422, "unsupported_storage_impact", "当前仅支持检查平台托管单表对象和关系")
	}
	tx, err := h.cfg.Engine.DB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var status, digest string
	err = tx.QueryRowContext(ctx, "SELECT status,digest FROM af_data_gates WHERE tenant_id=? AND definition_id=?", tenant, id).Scan(&status, &digest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err != nil || status != "ready" || digest != current.ETag {
		return out, problem(409, "mapping_unavailable", "当前存储映射未就绪或已变化")
	}
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+quote(table)+" WHERE tenant_id=?)", tenant).Scan(&out.HasData); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
