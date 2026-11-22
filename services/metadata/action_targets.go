package metadata

import (
	"context"
	"database/sql"
	"errors"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Drafts may contain unfinished callable signatures, but an explicit Action
// target must already identify a current Schema in the same tenant. Publishing
// rechecks the complete dependency graph under the same tenant guard.
func validateActionTargetReferences(ctx context.Context, tx *sql.Tx, d contracts.Definition) error {
	if d.Action == nil {
		return nil
	}
	for _, id := range d.Action.TargetSchemaIDs {
		var raw sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT current_json FROM af_meta_definitions WHERE tenant_id=? AND id=? AND kind='schema' AND status='active' AND current_json IS NOT NULL", d.TenantID, id).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return problem(422, "invalid_action_target", "动作关联对象必须引用同租户已生效的Schema稳定标识")
		}
		if err != nil {
			return err
		}
		target, err := parseDefinition(raw)
		if err != nil {
			return err
		}
		if target.ID != id || target.TenantID != d.TenantID || target.Kind != contracts.KindSchema {
			return problem(422, "invalid_action_target", "动作关联对象必须引用同租户已生效的Schema稳定标识")
		}
		if read := d.Action.RecordRead; read != nil && read.SchemaID == id {
			if err := validateActionRecordFields(target.Schema, read); err != nil {
				return err
			}
		}
	}
	return nil
}

// Draft binding and Function lifecycle changes share the tenant write guard.
// Signature validation remains part of publication.
func validateActionFunctionReference(ctx context.Context, tx *sql.Tx, d contracts.Definition) error {
	if d.Action == nil || d.Action.FunctionID == "" {
		return nil
	}
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM af_meta_definitions
 WHERE tenant_id=? AND id=? AND kind='function' AND status='active' AND current_json IS NOT NULL)`,
		d.TenantID, d.Action.FunctionID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return problem(422, "invalid_dependency", "动作实现必须引用同租户已生效的 Function")
	}
	return nil
}

func validateActionRecordFields(schema *contracts.Schema, read *contracts.ActionRecordRead) error {
	if schema == nil || schema.StorageBinding != nil {
		return problem(422, "invalid_record_read", "当前记录读取需要平台托管 Schema")
	}
	for _, id := range read.FieldIDs {
		found := false
		for _, property := range schema.Properties {
			if property.ID == id && property.DataType != "longtext" {
				found = true
				break
			}
		}
		if !found {
			return problem(422, "invalid_record_read", "声明字段不存在或超出支持范围")
		}
	}
	return nil
}
