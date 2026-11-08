package metadata

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

func validateCallableBinding(action contracts.Definition, function contracts.Definition) error {
	if action.Action == nil || function.Function == nil || function.Kind != contracts.KindFunction || action.TenantID != function.TenantID || action.Action.FunctionID != function.ID {
		return problem(422, "invalid_callable_binding", "Action实现必须引用同租户已生效的Function")
	}
	if action.Action.SideEffects != function.Function.SideEffects {
		return problem(422, "invalid_callable_binding", "Action与实现Function的副作用声明必须一致")
	}
	if !dataWriteCapabilitiesEqual(action.Action.DataWrite, function.Function.DataWrite) {
		return problem(422, "invalid_callable_binding", "Action与实现Function的业务写入范围必须完全一致")
	}
	if !contracts.CallableSignaturesEqual(action.Action, function.Function) {
		return problem(422, "invalid_callable_binding", "Action与实现Function的输入输出中文名称、类型及约束必须一致")
	}
	return nil
}

// A Function's display parameter names are the Python dictionary keys. Bound
// current Actions must continue to match when an implementation is republished.
func validateDependentActions(ctx context.Context, tx *sql.Tx, function contracts.Definition) error {
	if function.Function == nil {
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT d.current_json FROM af_meta_dependencies r JOIN af_meta_definitions d ON d.tenant_id=r.tenant_id AND d.id=r.source_id WHERE r.tenant_id=? AND r.target_id=? AND d.kind='action' AND d.status='active' AND d.current_json IS NOT NULL", function.TenantID, function.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var action contracts.Definition
		if err := json.Unmarshal([]byte(raw), &action); err != nil {
			return err
		}
		if action.Action != nil && action.Action.FunctionID == function.ID {
			if err := validateCallableBinding(action, function); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}
