package data

import (
	"context"
	"encoding/json"
	"net/url"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) hydrateEnums(ctx context.Context, current *c.CurrentDefinition) error {
	if c.EnumContextDigest(current.Definition) == "" {
		return nil
	}
	if err := c.ResolveDefinitionEnums(&current.Definition, func(id string) (c.CurrentDefinition, error) {
		return h.metadata(ctx, current.Definition.TenantID, "/metadata/definitions/"+url.PathEscape(id))
	}); err != nil {
		return problem(422, "enum_unavailable", "当前枚举不可用或选项超过预算")
	}
	return nil
}

func (h *handler) recheckEnums(ctx context.Context, current c.CurrentDefinition) error {
	if c.EnumContextDigest(current.Definition) == "" {
		return nil
	}
	latest, err := h.current(ctx, current.Definition.TenantID, current.Definition.ID)
	if err != nil {
		return err
	}
	if latest.ETag != current.ETag || c.EnumContextDigest(latest.Definition) != c.EnumContextDigest(current.Definition) {
		return problem(409, "enum_changed", "模型或枚举已改变，请重新读取后重试")
	}
	return nil
}

func enumWriteValue(property c.Property, raw json.RawMessage) error {
	if c.ValidateEnumValue(property, raw, false) != nil {
		return problem(422, "invalid_enum_value", "枚举值必须是当前未废弃的业务code："+property.Name)
	}
	return nil
}

// Changes to an enum affect logical query metadata and continuation evidence,
// while leaving the physical Schema gate's authoritative ETag untouched.
func enumQueryFingerprint(base string, definition c.Definition) string {
	if context := c.EnumContextDigest(definition); context != "" {
		return digest([]string{base, context})
	}
	return base
}
