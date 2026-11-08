package data

import (
	"context"
	"database/sql"
	"errors"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Source mutations hold this same source-local gate lock until their commit.
// Closing it drains admitted source transactions before any physical DDL or
// definition switch. Platform gate locks alone cannot provide that guarantee.
func (h *handler) blockMappedWriteGate(ctx context.Context, req c.PrepareRequest, tables []*mappedTable) (bool, error) {
	if len(tables) != 1 || tables[0].source.Driver != "mysql" || tables[0].source.TenantIsolation != "dedicated" {
		return false, nil
	}
	t := tables[0]
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var actual, status, operation, key string
	err = tx.QueryRowContext(ctx, "SELECT definition_digest,status,operation_id,table_key FROM af_source_write_gates WHERE tenant_id=? AND schema_id=? FOR UPDATE", req.Definition.TenantID, req.Definition.ID).Scan(&actual, &status, &operation, &key)
	if missingSourceControls(err) || errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if key != digest(t.locator()) {
		return false, problem(409, "mapped_write_recovery_required", "来源写入绑定无法与当前发布核对")
	}
	if actual == req.Digest && operation == req.OperationID {
		return status != "ready", tx.Commit()
	}
	if status != "ready" || actual != req.PreviousDigest {
		return false, problem(409, "mapped_write_recovery_required", "来源存在尚未恢复的写入或模型切换")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE af_source_write_gates SET status='blocked',definition_digest=?,operation_id=? WHERE tenant_id=? AND schema_id=?", req.Digest, req.OperationID, req.Definition.TenantID, req.Definition.ID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Called only after Metadata has committed the same current definition and the
// platform publication row has been verified as prepared for this operation.
// Failure keeps platform admission closed; a retry recovers the same operation.
func (h *handler) activateMappedWriteGate(ctx context.Context, current c.CurrentDefinition, req c.ActivateRequest) error {
	if current.Definition.Schema == nil || current.Definition.Schema.StorageBinding == nil {
		return nil
	}
	tables, err := h.mappedTables(ctx, current.Definition, false)
	if err != nil {
		return err
	}
	defer closeMappedTables(tables)
	if len(tables) != 1 || tables[0].source.Driver != "mysql" || tables[0].source.TenantIsolation != "dedicated" {
		return nil
	}
	t := tables[0]
	var exists int
	err = t.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM af_source_write_gates WHERE tenant_id=? AND schema_id=?", current.Definition.TenantID, current.Definition.ID).Scan(&exists)
	if missingSourceControls(err) || err == nil && exists == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateMappedWriteTable(ctx, current.Definition, tables); err != nil {
		return err
	}
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actual, operation, status, key string
	err = tx.QueryRowContext(ctx, "SELECT definition_digest,operation_id,status,table_key FROM af_source_write_gates WHERE tenant_id=? AND schema_id=? FOR UPDATE", current.Definition.TenantID, current.Definition.ID).Scan(&actual, &operation, &status, &key)
	if err != nil {
		return err
	}
	if actual != req.Digest || operation != req.OperationID || (status != "blocked" && status != "ready") || key != digest(t.locator()) {
		return problem(409, "mapped_write_recovery_required", "来源写入gate不属于当前已准备的模型操作")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE af_source_write_gates SET status='ready',catalog_signature=? WHERE tenant_id=? AND schema_id=?", t.catalog.Signature, current.Definition.TenantID, current.Definition.ID); err != nil {
		return err
	}
	return tx.Commit()
}
