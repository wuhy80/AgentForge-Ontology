package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// preview uses the identical schema validation and DDL compiler as prepare. It
// reads only the mapping catalog and never changes admission or business data.
func (h *handler) preview(w http.ResponseWriter, r *http.Request) {
	tenant, err := h.internalTenant(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req c.PrepareRequest
	if err = decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.Definition.TenantID != tenant {
		writeError(w, problem(422, "invalid_publication", "定义与租户不匹配"))
		return
	}
	result := c.PrepareResult{OperationID: req.OperationID, Status: "preview", Statements: []string{}}
	if req.Definition.Kind == c.KindLink && req.Definition.Link != nil && req.Definition.Link.StorageBinding != nil {
		result, err := h.previewLink(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if req.Definition.Kind != c.KindSchema {
		writeJSON(w, 200, result)
		return
	}
	if req.Definition.Schema != nil && req.Definition.Schema.StorageBinding != nil {
		result, err := h.previewMapped(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	statement, err := ddl(req.Definition)
	if err != nil {
		writeError(w, err)
		return
	}
	if err = h.checkBackend(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	var signature, status, previous, raw string
	err = h.cfg.Engine.DB().QueryRowContext(r.Context(), "SELECT storage_signature,status,digest,definition FROM af_data_gates WHERE tenant_id=? AND definition_id=?", tenant, req.Definition.ID).Scan(&signature, &status, &previous, &raw)
	if err == nil {
		if status != "ready" {
			writeError(w, problem(409, "publication_busy", "映射正在生效，请恢复后再预览"))
			return
		}
		if signature != storageSignature(req.Definition) {
			var old c.Definition
			if json.Unmarshal([]byte(raw), &old) != nil || digest(old) != previous {
				writeError(w, problem(500, "mapping_corrupt", "无法核对原定义"))
				return
			}
			addition, statements, err := platformAdditionPlan(old, req.Definition)
			if err == nil {
				if verifyTable(r.Context(), h.cfg.Engine.DB(), old) != nil {
					err = problem(422, "physical_schema_changed", "当前物理结构与原定义不符，不能预览迁移")
				}
			}
			if err == nil {
				err = preflightPlatformIndexes(r.Context(), h.cfg.Engine.DB(), old, addition)
			}
			if err != nil {
				writeError(w, err)
				return
			}
			result.Statements = statements
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if err = h.cfg.Engine.DB().QueryRowContext(r.Context(), "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", tableName(req.Definition.ID)).Scan(&exists); err != nil {
			writeError(w, err)
			return
		}
		if exists != 0 {
			writeError(w, problem(409, "unmanaged_table", "目标存在未纳管表，不能直接接管"))
			return
		}
		result.Statements = []string{statement}
	} else {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (h *handler) prepare(w http.ResponseWriter, r *http.Request) {
	tenant, err := h.internalTenant(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req c.PrepareRequest
	if err = decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.Definition.TenantID != tenant || !uuidPattern.MatchString(req.OperationID) || !uuidPattern.MatchString(req.Definition.ID) || digest(req.Definition) != req.Digest {
		writeError(w, problem(422, "invalid_publication", "操作、定义、租户或内容摘要不匹配"))
		return
	}
	if req.Definition.Kind == c.KindLink && req.Definition.Link != nil && req.Definition.Link.StorageBinding != nil {
		result, err := h.prepareLink(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if req.Definition.Kind != c.KindSchema {
		writeJSON(w, 200, c.PrepareResult{OperationID: req.OperationID, Status: "prepared"})
		return
	}
	if req.Definition.Schema != nil && req.Definition.Schema.StorageBinding != nil {
		result, err := h.prepareMapped(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	result, err := h.preparePlatform(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (h *handler) activate(w http.ResponseWriter, r *http.Request) {
	tenant, err := h.internalTenant(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req c.ActivateRequest
	if err = decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	current, err := h.current(r.Context(), tenant, req.DefinitionID)
	if err != nil {
		writeError(w, fmt.Errorf("verify activation current: %w", err))
		return
	}
	if current.ETag != req.Digest {
		writeError(w, problem(409, "publication_conflict", "候选摘要不是权威当前定义"))
		return
	}
	if current.Definition.Kind != c.KindSchema && !(current.Definition.Kind == c.KindLink && current.Definition.Link != nil && current.Definition.Link.StorageBinding != nil) {
		writeJSON(w, 200, c.PrepareResult{OperationID: req.OperationID, Status: "ready"})
		return
	}
	err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
		var operation, digest, status string
		if err := tx.QueryRowContext(r.Context(), "SELECT operation_id,digest,status FROM af_data_gates WHERE tenant_id=? AND definition_id=? FOR UPDATE", tenant, req.DefinitionID).Scan(&operation, &digest, &status); err != nil {
			return err
		}
		if operation != req.OperationID || digest != req.Digest || (status != "prepared" && status != "ready") {
			return problem(409, "publication_conflict", "操作并非已完成准备的当前映射")
		}
		if err := h.activateMappedWriteGate(r.Context(), current, req); err != nil {
			return err
		}
		_, err := tx.ExecContext(r.Context(), "UPDATE af_data_gates SET status='ready' WHERE tenant_id=? AND definition_id=? AND operation_id=? AND digest=?", tenant, req.DefinitionID, req.OperationID, req.Digest)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, c.PrepareResult{OperationID: req.OperationID, Status: "ready"})
}

func verifyTable(ctx context.Context, db schemaCatalog, d c.Definition) error {
	var engine, rowFormat string
	if err := db.QueryRowContext(ctx, "SELECT engine,row_format FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", tableName(d.ID)).Scan(&engine, &rowFormat); err != nil {
		return err
	}
	if !strings.EqualFold(engine, "InnoDB") || !strings.EqualFold(rowFormat, "Dynamic") {
		return fmt.Errorf("physical table must use InnoDB ROW_FORMAT=DYNAMIC")
	}
	rows, err := db.QueryContext(ctx, "SELECT column_name,column_type,is_nullable,collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position", tableName(d.ID))
	if err != nil {
		return err
	}
	defer rows.Close()
	type expected struct {
		kind      string
		nullable  bool
		collation string
	}
	columns := map[string]expected{"tenant_id": {kind: "varbinary(128)"}, "id": {kind: "char(36)", collation: "ascii_bin"}, "etag": {kind: "char(36)", collation: "ascii_bin"}}
	for _, p := range d.Schema.Properties {
		t, _ := sqlType(p)
		collation := ""
		if p.DataType == "string" || p.DataType == "text" || p.DataType == "longtext" {
			collation = "utf8mb4_0900_bin"
		}
		if p.DataType == "uuid" {
			collation = "ascii_bin"
		}
		columns[columnName(p.ID)] = expected{t, p.Nullable, collation}
	}
	count := 0
	for rows.Next() {
		var name, kind, nullable string
		var collation sql.NullString
		if err = rows.Scan(&name, &kind, &nullable, &collation); err != nil {
			return err
		}
		want, ok := columns[name]
		if !ok {
			return fmt.Errorf("unexpected physical column")
		}
		if kind == "tinyint" && want.kind == "tinyint(1)" {
			kind = "tinyint(1)"
		}
		if strings.ToLower(kind) != want.kind || (nullable == "YES") != want.nullable || collation.String != want.collation {
			return fmt.Errorf("physical column does not match validated definition")
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != len(columns) {
		return fmt.Errorf("physical columns incomplete")
	}
	if err = rows.Close(); err != nil {
		return err
	}
	indexRows, err := db.QueryContext(ctx, "SELECT index_name,non_unique,column_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? ORDER BY index_name,seq_in_index", tableName(d.ID))
	if err != nil {
		return err
	}
	defer indexRows.Close()
	type index struct {
		unique bool
		fields []string
	}
	actual := map[string]index{}
	for indexRows.Next() {
		var name, field string
		var nonUnique int
		if err = indexRows.Scan(&name, &nonUnique, &field); err != nil {
			return err
		}
		v := actual[name]
		v.unique = nonUnique == 0
		v.fields = append(v.fields, field)
		actual[name] = v
	}
	if err = indexRows.Err(); err != nil {
		return err
	}
	expectedIndexes := map[string]index{"PRIMARY": {true, []string{"tenant_id", "id"}}}
	for _, idx := range d.Schema.Indexes {
		fields := []string{"tenant_id"}
		for _, id := range idx.FieldIDs {
			fields = append(fields, columnName(id))
		}
		expectedIndexes[physical("ix_", idx.ID)] = index{idx.Unique, fields}
	}
	if len(actual) != len(expectedIndexes) {
		return fmt.Errorf("physical indexes differ")
	}
	for name, want := range expectedIndexes {
		got, ok := actual[name]
		if !ok || got.unique != want.unique || strings.Join(got.fields, ",") != strings.Join(want.fields, ",") {
			return fmt.Errorf("physical index does not match validated definition")
		}
	}
	return nil
}

func (h *handler) checkBackend(ctx context.Context) error {
	var pageSize int
	if err := h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT @@innodb_page_size").Scan(&pageSize); err != nil {
		return err
	}
	if pageSize != 16384 {
		return problem(422, "unsupported_backend", "首批仅认证 16KiB InnoDB 页大小")
	}
	return nil
}
