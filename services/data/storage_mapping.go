package data

import (
	"context"
	"database/sql"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// storageMappingHTTP exposes mapping metadata to model authors. Instance read
// permission alone never grants physical database catalog access.
func (h *handler) storageMappingHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	decision, err := h.authorizeStorageMapping(r.Context(), p, r.PathValue("schemaId"))
	if err != nil {
		writeError(w, err)
		return
	}
	current, err := h.current(r.Context(), p.TenantID, r.PathValue("schemaId"))
	if err != nil {
		writeError(w, err)
		return
	}
	if current.Definition.ID != r.PathValue("schemaId") {
		writeError(w, problem(503, "metadata_unavailable", "当前定义响应身份不一致"))
		return
	}
	if err := validateSchema(current.Definition); err != nil {
		writeError(w, err)
		return
	}
	out := c.SchemaStorageMapping{
		SchemaID: current.Definition.ID, DefinitionETag: current.ETag,
		Managed: current.Definition.Schema.StorageBinding == nil,
		Mode:    current.Definition.Schema.Mode, Properties: []c.StoragePropertyMapping{},
	}
	err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
		// The same publication gate used by queries prevents exposing an old
		// definition as the active mapping while a new mapping is being installed.
		if err := h.gate(r.Context(), tx, p, current); err != nil {
			return err
		}
		if out.Managed {
			if err := defaultStorageMapping(r.Context(), tx, current.Definition, &out); err != nil {
				return err
			}
			return h.recheck(r.Context(), p, "definition.edit", current.Definition.ID, decision)
		}
		tables, err := h.mappedTables(r.Context(), current.Definition, true)
		if err != nil {
			return err
		}
		defer closeMappedTables(tables)
		out.Primary = mappedStorageTable(tables[0])
		for _, property := range current.Definition.Schema.Properties {
			table, _, ok := mappedPropertyTable(tables, property.ID)
			if !ok {
				return problem(409, "mapping_unavailable", "属性映射未就绪")
			}
			column, exists := table.column(property.Mapping.Column)
			out.Properties = append(out.Properties, c.StoragePropertyMapping{
				StorageMappingTable: mappedStorageTable(table),
				PropertyID:          property.ID, PropertyName: property.Name,
				Column: property.Mapping.Column, DatabaseType: column.DatabaseType, Exists: exists,
			})
		}
		return h.recheck(r.Context(), p, "definition.edit", current.Definition.ID, decision)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (h *handler) authorizeStorageMapping(ctx context.Context, p c.Principal, schemaID string) (c.Decision, error) {
	decision, err := h.authorize(ctx, p, "definition.edit", schemaID)
	if err != nil {
		return decision, err
	}
	if decision.RowFilter != nil || decision.NewRowFilter != nil {
		return decision, problem(422, "unsupported_authorization", "物理映射目录不支持带行条件的定义授权")
	}
	for _, fields := range [][]string{decision.ReadFields, decision.WriteFields} {
		for _, field := range fields {
			if field != "*" {
				return decision, problem(422, "unsupported_authorization", "物理映射目录不支持属性投影的定义授权")
			}
		}
	}
	return decision, nil
}

func mappedStorageTable(table *mappedTable) c.StorageMappingTable {
	return c.StorageMappingTable{
		DataSourceID: table.source.ID, DataSourceName: table.source.Name, Driver: table.source.Driver,
		Namespace: table.namespace, Table: table.name, KeyColumn: table.keyColumn, TenantColumn: table.tenantColumn,
	}
}

func defaultStorageMapping(ctx context.Context, tx *sql.Tx, d c.Definition, out *c.SchemaStorageMapping) error {
	primary := c.StorageMappingTable{DataSourceName: "平台默认存储", Driver: "mysql", Table: tableName(d.ID), KeyColumn: "id", TenantColumn: "tenant_id"}
	if err := tx.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&primary.Namespace); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT column_name,column_type FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position", primary.Table)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns := map[string]string{}
	for rows.Next() {
		var name, databaseType string
		if err := rows.Scan(&name, &databaseType); err != nil {
			return err
		}
		columns[name] = databaseType
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out.Primary = primary
	for _, property := range d.Schema.Properties {
		name := columnName(property.ID)
		databaseType, exists := columns[name]
		out.Properties = append(out.Properties, c.StoragePropertyMapping{
			StorageMappingTable: primary, PropertyID: property.ID, PropertyName: property.Name,
			Column: name, DatabaseType: databaseType, Exists: exists,
		})
	}
	return nil
}
