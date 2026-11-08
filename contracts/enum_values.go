package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

const MaxResolvedEnumBytes = 256 << 10
const MaxReferencedEnums = 32

// DefinitionProperties visits actual fields, including callable inputs and
// outputs. The returned pointers are owned by definition, not detached copies.
func DefinitionProperties(definition *Definition) []*Property {
	var out []*Property
	add := func(properties []Property) {
		for i := range properties {
			out = append(out, &properties[i])
		}
	}
	if definition.Schema != nil {
		add(definition.Schema.Properties)
	}
	for _, callable := range []*Callable{definition.Action, definition.Function} {
		if callable != nil {
			add(callable.Input)
			add(callable.Output)
		}
	}
	return out
}

// ResolveDefinitionEnums attaches only current business codes and labels. The
// lookup must be tenant scoped and authoritative; client snapshots are ignored.
// The caller owns a detached definition so hydration cannot mutate its store.
func ResolveDefinitionEnums(definition *Definition, lookup func(string) (CurrentDefinition, error)) error {
	resolved := map[string]CurrentDefinition{}
	used := 0
	for _, property := range DefinitionProperties(definition) {
		property.EnumValues, property.EnumETag = nil, ""
		if property.EnumID == "" {
			continue
		}
		if property.DataType != "string" {
			return errors.New("enum field must use string codes")
		}
		current, ok := resolved[property.EnumID]
		if !ok {
			if len(resolved) >= MaxReferencedEnums {
				return errors.New("enum dependency budget exceeded")
			}
			var err error
			current, err = lookup(property.EnumID)
			if err != nil {
				return err
			}
			if current.Definition.TenantID != definition.TenantID || current.Definition.ID != property.EnumID || current.Definition.Kind != KindEnum || current.Definition.Enum == nil || current.Status != "active" || current.ETag == "" {
				return errors.New("enum dependency is not current in this tenant")
			}
			values := current.Definition.Enum.Values
			if len(values) == 0 || len(values) > 1024 {
				return errors.New("invalid enum values")
			}
			seen := map[string]bool{}
			for _, value := range values {
				if value.Code == "" || len(value.Code) > 128 || seen[value.Code] || value.Label == "" {
					return errors.New("invalid enum code or label")
				}
				seen[value.Code] = true
			}
			resolved[property.EnumID] = current
		}
		property.EnumValues = append([]EnumValue(nil), current.Definition.Enum.Values...)
		property.EnumETag = current.ETag
		encoded, _ := json.Marshal(property.EnumValues)
		used += len(encoded) + len(property.EnumETag) + len(property.EnumID)
		if used > MaxResolvedEnumBytes {
			return errors.New("enum metadata budget exceeded")
		}
	}
	encoded, _ := json.Marshal(definition)
	if len(encoded) > 1<<20 {
		return errors.New("resolved definition exceeds metadata response budget")
	}
	return nil
}

// EnumContextDigest binds dependency metadata without changing the stable
// definition ETag used by publication gates. It is empty for existing schemas.
func EnumContextDigest(definition Definition) string {
	type item struct {
		ID, ETag string
		Values   []EnumValue
	}
	byID := map[string]item{}
	for _, property := range DefinitionProperties(&definition) {
		if property.EnumID != "" {
			byID[property.EnumID] = item{property.EnumID, property.EnumETag, property.EnumValues}
		}
	}
	if len(byID) == 0 {
		return ""
	}
	items := make([]item, 0, len(byID))
	for _, value := range byID {
		items = append(items, value)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	encoded, _ := json.Marshal(items)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// ValidateEnumValue is separate from scalar type validation: reads/filters can
// use deprecated codes, while new values cannot introduce one.
func ValidateEnumValue(property Property, raw json.RawMessage, allowDeprecated bool) error {
	if property.EnumID == "" || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var code string
	if property.DataType != "string" || property.EnumETag == "" || json.Unmarshal(raw, &code) != nil {
		return errors.New("enum context or code invalid")
	}
	for _, value := range property.EnumValues {
		if value.Code == code && (allowDeprecated || !value.Deprecated) {
			return nil
		}
	}
	return errors.New("enum code is unknown or deprecated")
}
