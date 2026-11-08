package platform

import (
	"encoding/json"
	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

func validateReferenceBudget(expressions ...*contracts.Expression) error {
	keys := map[string]bool{}
	var visit func(*contracts.Expression) error
	visit = func(e *contracts.Expression) error {
		if e == nil {
			return nil
		}
		if e.ValueRef != nil {
			raw, _ := json.Marshal(e.ValueRef)
			keys[string(raw)] = true
			if len(keys) > 16 {
				return policyError("单条规则最多使用16种上下文引用")
			}
		}
		for i := range e.Args {
			if err := visit(&e.Args[i]); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range expressions {
		if err := visit(e); err != nil {
			return err
		}
	}
	return nil
}

// ValidatePermissionValueRef is shared by save/publish validation and runtime
// resolution. Unknown providers and mixed selectors are never ignored.
func ValidatePermissionValueRef(ref contracts.PermissionValueRef, op string) error {
	set := op == "in" || op == "notIn"
	switch op {
	case "eq", "ne", "gt", "gte", "lt", "lte", "startsWith", "in", "notIn":
	default:
		return policyError("此操作符不接受动态条件值")
	}
	bad := func() error { return policyError("权限上下文引用来源、选择方式或值类型不受支持") }
	switch ref.Source {
	case "user":
		if ref.SchemaID != "" || ref.FieldID != "" || ref.InstanceID != "" || ref.MatchFieldID != "" || ref.MatchUserAttribute != "" || ref.Mode != "" {
			return bad()
		}
		switch ref.Attribute {
		case "id", "tenantId", "username", "displayName":
			if set {
				return bad()
			}
		case "roleIds", "userGroupIds":
			if !set {
				return bad()
			}
		default:
			return bad()
		}
	case "object":
		if ref.Attribute != "" || !validIdentifier(ref.SchemaID, false) || !validIdentifier(ref.FieldID, false) || (ref.Mode != "" && ref.Mode != "scalar" && ref.Mode != "set") || set != (ref.Mode == "set") {
			return bad()
		}
		if ref.InstanceID != "" {
			if !validIdentifier(ref.InstanceID, false) || ref.MatchFieldID != "" || ref.MatchUserAttribute != "" || set {
				return bad()
			}
		} else if !validIdentifier(ref.MatchFieldID, false) || (ref.MatchUserAttribute != "id" && ref.MatchUserAttribute != "username") {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}
