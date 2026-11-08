package metadata

import (
	"context"
	"database/sql"
	"encoding/json"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func enumReference(ctx context.Context, tx *sql.Tx, tenant, id string) (c.CurrentDefinition, error) {
	var current c.CurrentDefinition
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT current_json,current_digest,status FROM af_meta_definitions WHERE tenant_id=? AND id=? AND kind='enum' AND status='active'", tenant, id).Scan(&raw, &current.ETag, &current.Status); err != nil {
		if err == sql.ErrNoRows {
			return current, problem(422, "invalid_enum_reference", "枚举必须属于同租户且已生效")
		}
		return current, err
	}
	definition, err := parseDefinition(raw)
	current.Definition = definition
	return current, err
}

func resolveEnumReferences(ctx context.Context, tx *sql.Tx, definition c.Definition) (c.Definition, error) {
	// Detached copy: response-only options must never enter a saved definition.
	encoded, _ := json.Marshal(definition)
	var resolved c.Definition
	if err := json.Unmarshal(encoded, &resolved); err != nil {
		return resolved, err
	}
	if err := c.ResolveDefinitionEnums(&resolved, func(id string) (c.CurrentDefinition, error) {
		return enumReference(ctx, tx, definition.TenantID, id)
	}); err != nil {
		return resolved, problem(422, "invalid_enum_reference", "枚举引用无效或选项超过预算")
	}
	return resolved, nil
}

func (h *handler) definitionEnumMetadata(ctx context.Context, definition c.Definition) (c.Definition, error) {
	if c.EnumContextDigest(definition) == "" {
		return definition, nil
	}
	tx, err := h.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return definition, err
	}
	defer tx.Rollback()
	resolved, err := resolveEnumReferences(ctx, tx, definition)
	if err != nil {
		return definition, err
	}
	return resolved, tx.Commit()
}

func validateEnumReferences(ctx context.Context, tx *sql.Tx, definition c.Definition) error {
	resolved, err := resolveEnumReferences(ctx, tx, definition)
	if err != nil {
		return err
	}
	for _, property := range c.DefinitionProperties(&resolved) {
		if len(property.Default) != 0 && c.ValidateEnumValue(*property, property.Default, false) != nil {
			return problem(422, "invalid_enum_default", "枚举默认值必须是当前未废弃的业务code")
		}
	}
	return nil
}

// Enum changes are additive. Existing stable codes remain readable forever in
// this increment; a deprecated code cannot be introduced by a new write.
func compatibleEnumUpdate(old, next c.Definition) bool {
	if old.Kind != c.KindEnum || next.Kind != c.KindEnum || old.Enum == nil || next.Enum == nil {
		return false
	}
	remaining := map[string]bool{}
	for _, value := range next.Enum.Values {
		remaining[value.Code] = true
	}
	for _, value := range old.Enum.Values {
		if !remaining[value.Code] {
			return false
		}
	}
	return true
}

func validateEnumDependents(ctx context.Context, tx *sql.Tx, candidate c.Definition) error {
	if candidate.Kind != c.KindEnum {
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT d.current_json FROM af_meta_dependencies r JOIN af_meta_definitions d ON d.tenant_id=r.tenant_id AND d.id=r.source_id WHERE r.tenant_id=? AND r.target_id=? AND d.current_json IS NOT NULL LIMIT 4097", candidate.TenantID, candidate.ID)
	if err != nil {
		return err
	}
	var definitions []c.Definition
	for rows.Next() {
		var raw sql.NullString
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		definition, e := parseDefinition(raw)
		if e != nil {
			rows.Close()
			return e
		}
		definitions = append(definitions, definition)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(definitions) > 4096 {
		return problem(422, "dependency_budget", "枚举依赖超过检查预算")
	}
	for _, definition := range definitions {
		for _, property := range c.DefinitionProperties(&definition) {
			if property.EnumID != candidate.ID || len(property.Default) == 0 {
				continue
			}
			property.EnumValues, property.EnumETag = candidate.Enum.Values, "candidate"
			if c.ValidateEnumValue(*property, property.Default, false) != nil {
				return problem(422, "enum_default_in_use", "枚举值仍被生效定义用作默认值，不能废弃")
			}
		}
	}
	return nil
}
