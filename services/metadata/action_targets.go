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
	}
	return nil
}
