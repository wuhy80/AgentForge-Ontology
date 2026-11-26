package data

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

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
	if d.Kind == c.KindSchema && d.Schema != nil && d.Schema.StorageLayout == "single" && d.Schema.StorageBinding != nil {
		return h.mappedStorageImpact(ctx, tenant, current)
	}
	if d.Kind == c.KindLink && d.Link != nil && d.Link.StorageBinding != nil {
		switch d.Link.StorageBinding.Mode {
		case "junction", "foreign_key":
			return h.mappedLinkStorageImpact(ctx, tenant, current)
		}
	}
	var table string
	switch {
	case d.Kind == c.KindSchema && d.Schema != nil && d.Schema.Mode == "schema_2_db" && d.Schema.StorageLayout == "single" && d.Schema.StorageBinding == nil:
		table = tableName(id)
	case d.Kind == c.KindLink && d.Link != nil && d.Link.StorageBinding != nil && d.Link.StorageBinding.Mode == "managed":
		table = linkTableName(id)
	default:
		return out, problem(422, "unsupported_storage_impact", "当前仅支持单表对象及已配置存储的关系")
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

func (h *handler) mappedStorageImpact(ctx context.Context, tenant string, current c.CurrentDefinition) (c.StorageDefinitionImpact, error) {
	var out c.StorageDefinitionImpact
	err := h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, c.Principal{TenantID: tenant}, current); err != nil {
			return err
		}
		tables, err := h.mappedTables(ctx, current.Definition, false)
		if err != nil {
			return err
		}
		defer closeMappedTables(tables)
		if err := validateCurrentMappedCatalog(ctx, current, tables); err != nil {
			return err
		}
		return mappedImpactExists(ctx, tables[0], tenant, "", &out)
	})
	return out, err
}

func (h *handler) mappedLinkStorageImpact(ctx context.Context, tenant string, current c.CurrentDefinition) (c.StorageDefinitionImpact, error) {
	var out c.StorageDefinitionImpact
	plan, err := h.logicalLinkPlan(ctx, current.Definition)
	if err != nil {
		return out, err
	}
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.linkGates(ctx, tx, c.Principal{TenantID: tenant}, plan, &current); err != nil {
			return err
		}
		opened, err := h.openLinkPlan(ctx, plan)
		if err != nil {
			return err
		}
		defer opened.close()
		binding := current.Definition.Link.StorageBinding
		if binding.Mode == "junction" {
			return mappedImpactExists(ctx, opened.junction, tenant, "", &out)
		}
		holder := opened.sourceTables[0]
		if binding.ForeignKeySide == "target" {
			holder = opened.targetTables[0]
		}
		return mappedImpactExists(ctx, holder, tenant, binding.ForeignKeyColumn, &out)
	})
	return out, err
}

func mappedImpactExists(ctx context.Context, table *mappedTable, tenant, foreignKey string, out *c.StorageDefinitionImpact) error {
	query := "SELECT EXISTS (SELECT 1 FROM " + table.qualified()
	var args []any
	var predicates []string
	if table.tenantColumn != "" {
		predicates = append(predicates, sourceExactStringColumn(table, table.tenantColumn)+" = "+sourceExactStringParameter(table))
		args = append(args, tenant)
	}
	if foreignKey != "" {
		column, err := sourceQuote(table.source.Driver, foreignKey)
		if err != nil {
			return err
		}
		predicates = append(predicates, column+" IS NOT NULL")
	}
	if len(predicates) > 0 {
		query += " WHERE " + strings.Join(predicates, " AND ")
	}
	return table.db.QueryRowContext(ctx, sourceBind(table.source.Driver, query+")"), args...).Scan(&out.HasData)
}
