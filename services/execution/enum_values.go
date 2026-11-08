package execution

import (
	"context"
	"encoding/json"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) resolveCallableEnums(ctx context.Context, p c.Principal, definition c.Definition) (c.Definition, error) {
	if c.EnumContextDigest(definition) == "" {
		return definition, nil
	}
	encoded, _ := json.Marshal(definition)
	var resolved c.Definition
	if err := json.Unmarshal(encoded, &resolved); err != nil {
		return definition, err
	}
	if err := c.ResolveDefinitionEnums(&resolved, func(id string) (c.CurrentDefinition, error) {
		return h.cfg.Metadata.Current(ctx, p.TenantID, id)
	}); err != nil {
		return definition, problem(422, "enum_unavailable", "参数枚举不可用或选项超过预算")
	}
	return resolved, nil
}
