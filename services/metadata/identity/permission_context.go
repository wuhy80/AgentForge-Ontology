package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type permissionSubject struct {
	user          c.User
	roles, groups []string
}

func matchesPermission(rule c.PermissionRule, request c.AuthorizationRequest) bool {
	return rule.Operation == request.Operation && (rule.ResourceID == "*" || rule.ResourceID == request.ResourceID)
}

func visitValueRefs(e *c.Expression, visit func(c.PermissionValueRef) error) error {
	if e == nil {
		return nil
	}
	if e.ValueRef != nil {
		if err := visit(*e.ValueRef); err != nil {
			return err
		}
	}
	for i := range e.Args {
		if err := visitValueRefs(&e.Args[i], visit); err != nil {
			return err
		}
	}
	return nil
}

func permissionSubjectFor(ctx context.Context, tx *sql.Tx, user c.User, roles []string, runtime *c.RuntimePolicy, request c.AuthorizationRequest) (permissionSubject, error) {
	result := permissionSubject{user: user, roles: roles, groups: []string{}}
	needGroups := false
	for _, rule := range runtime.Rules {
		if !matchesPermission(rule, request) {
			continue
		}
		for _, e := range []*c.Expression{rule.RowFilter, rule.NewRowFilter} {
			_ = visitValueRefs(e, func(ref c.PermissionValueRef) error {
				needGroups = needGroups || (ref.Source == "user" && ref.Attribute == "userGroupIds")
				return nil
			})
		}
	}
	if !needGroups {
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT g.id FROM af_identity_user_group_members m JOIN af_identity_user_groups g ON g.tenant_id=m.tenant_id AND g.id=m.group_id WHERE m.tenant_id=? AND m.user_id=? AND g.enabled=TRUE ORDER BY g.id LIMIT 65", user.TenantID, user.ID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return result, err
		}
		result.groups = append(result.groups, id)
		if len(result.groups) > 64 {
			return result, problem(422, "group_budget", "用户组超过上下文预算")
		}
	}
	return result, rows.Err()
}

func userContextValue(subject permissionSubject, attribute string) (json.RawMessage, error) {
	var value any
	switch attribute {
	case "id":
		value = subject.user.ID
	case "tenantId":
		value = subject.user.TenantID
	case "username":
		value = subject.user.Username
	case "displayName":
		value = subject.user.DisplayName
	case "roleIds":
		if len(subject.roles) == 0 {
			return nil, contextUnavailable()
		}
		value = subject.roles
	case "userGroupIds":
		if len(subject.groups) == 0 {
			return nil, contextUnavailable()
		}
		value = subject.groups
	default:
		return nil, contextUnavailable()
	}
	if text, ok := value.(string); ok && text == "" {
		return nil, contextUnavailable()
	}
	return json.Marshal(value)
}

func contextUnavailable() error {
	return problem(403, "permission_context_unavailable", "无法取得有效的权限上下文，请联系管理员检查配置")
}

func validateUserReference(field c.Property, ref c.PermissionValueRef) error {
	if ref.Source != "user" {
		return nil
	}
	text := field.DataType == "string" || field.DataType == "text" || field.DataType == "longtext"
	identifier := ref.Attribute == "id" || ref.Attribute == "roleIds" || ref.Attribute == "userGroupIds"
	if !text && !(identifier && field.DataType == "uuid") {
		return problem(422, "invalid_policy_type", "当前用户属性与条件字段类型不匹配")
	}
	return nil
}

func contextSchema(ctx context.Context, tx *sql.Tx, tenant, id string) (*c.Schema, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT current_json FROM af_meta_definitions WHERE tenant_id=? AND id=? AND kind='schema' AND status='active' AND current_json IS NOT NULL", tenant, id).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, problem(422, "invalid_context_source", "取值来源必须是当前租户已生效的对象模型")
		}
		return nil, err
	}
	var d c.Definition
	if json.Unmarshal([]byte(raw), &d) != nil || d.Schema == nil || d.TenantID != tenant {
		return nil, contextUnavailable()
	}
	return d.Schema, nil
}

func contextProperty(schema *c.Schema, id string) (c.Property, bool) {
	for _, field := range schema.Properties {
		if field.ID == id {
			return field, true
		}
	}
	return c.Property{}, false
}

func validateObjectReferences(ctx context.Context, tx *sql.Tx, tenant string, e *c.Expression, targets map[string]c.Property) error {
	if e == nil {
		return nil
	}
	if e.ValueRef != nil && e.ValueRef.Source == "object" {
		ref := *e.ValueRef
		schema, err := contextSchema(ctx, tx, tenant, ref.SchemaID)
		if err != nil {
			return err
		}
		if schema.StorageBinding != nil {
			return problem(422, "unsupported_context_source", "对象上下文首批仅支持平台默认存储")
		}
		source, exists := contextProperty(schema, ref.FieldID)
		target, targetExists := targets[e.Field]
		if !exists || !targetExists {
			return problem(422, "invalid_context_field", "取值来源字段已失效")
		}
		if source.DataType != target.DataType || (source.DataType == "decimal" && (source.Precision != target.Precision || source.Scale != target.Scale)) {
			return problem(422, "invalid_policy_type", "对象取值字段须与条件字段类型和精度一致")
		}
		if ref.InstanceID != "" {
			if !uuidValue.MatchString(ref.InstanceID) {
				return problem(422, "invalid_context_selector", "平台对象取值需要完整实例ID")
			}
		} else {
			match, exists := contextProperty(schema, ref.MatchFieldID)
			if !exists {
				return problem(422, "invalid_context_field", "用户匹配字段已失效")
			}
			if err := validateUserReference(match, c.PermissionValueRef{Source: "user", Attribute: ref.MatchUserAttribute}); err != nil {
				return err
			}
			if ref.Mode != "set" {
				unique := false
				for _, index := range schema.Indexes {
					unique = unique || (index.Unique && len(index.FieldIDs) == 1 && index.FieldIDs[0] == ref.MatchFieldID)
				}
				if !unique {
					return problem(422, "invalid_context_selector", "单值用户匹配必须使用单字段唯一索引")
				}
			}
		}
	}
	for i := range e.Args {
		if err := validateObjectReferences(ctx, tx, tenant, &e.Args[i], targets); err != nil {
			return err
		}
	}
	return nil
}

// resolvePermissionContext resolves only matching grants. Each distinct object
// operand is read once and rechecked after collection; OPA/Data receive only
// literal operands. The recheck detects changed evidence, not a distributed lock.
func (s *Store) resolvePermissionContext(ctx context.Context, runtime c.RuntimePolicy, subject permissionSubject, request c.AuthorizationRequest) (c.RuntimePolicy, bool, error) {
	resolved := c.RuntimePolicy{State: runtime.State, Rules: []c.PermissionRule{}}
	for _, rule := range runtime.Rules {
		if matchesPermission(rule, request) {
			if rule.Effect == "deny" {
				resolved.Rules = []c.PermissionRule{rule}
				return resolved, false, nil
			}
			resolved.Rules = append(resolved.Rules, rule)
		}
	}
	if err := platform.ValidatePermissionPolicy(c.PermissionPolicy{Rules: resolved.Rules}); err != nil {
		return resolved, false, err
	}
	protected := false
	for _, rule := range resolved.Rules {
		for _, e := range []*c.Expression{rule.RowFilter, rule.NewRowFilter} {
			_ = visitValueRefs(e, func(c.PermissionValueRef) error { protected = true; return nil })
		}
	}
	if !protected {
		return resolved, false, nil
	}
	originalRules := append([]c.PermissionRule(nil), resolved.Rules...)
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if err := s.transaction(ctx, func(tx *sql.Tx) error {
		return validatePolicy(ctx, tx, subject.user.TenantID, c.PermissionPolicy{Rules: resolved.Rules})
	}); err != nil {
		return resolved, true, err
	}
	cache := map[string]json.RawMessage{}
	dependencies := map[string]string{}
	sources := map[string]bool{}
	type objectRead struct {
		key   string
		input c.ObjectContextRequest
	}
	objectReads := []objectRead{}
	var resolve func(*c.Expression) (*c.Expression, error)
	resolve = func(original *c.Expression) (*c.Expression, error) {
		if original == nil {
			return nil, nil
		}
		e := *original
		e.Args = make([]c.Expression, len(original.Args))
		for i := range original.Args {
			child, err := resolve(&original.Args[i])
			if err != nil {
				return nil, err
			}
			e.Args[i] = *child
		}
		if e.ValueRef == nil {
			return &e, nil
		}
		ref := *e.ValueRef
		if ref.Source == "object" && ref.Mode == "" {
			ref.Mode = "scalar"
		}
		key := marshal(ref)
		value, exists := cache[key]
		if !exists {
			if len(cache) >= 16 {
				return nil, problem(422, "context_budget", "单次授权最多解析16种上下文引用")
			}
			var err error
			if ref.Source == "user" {
				value, err = userContextValue(subject, ref.Attribute)
			} else {
				if s.ObjectContext == nil {
					return nil, problem(503, "context_unavailable", "对象上下文服务未配置")
				}
				input := c.ObjectContextRequest{SchemaID: ref.SchemaID, FieldID: ref.FieldID, InstanceID: ref.InstanceID, MatchFieldID: ref.MatchFieldID, Mode: ref.Mode}
				if ref.MatchFieldID != "" {
					input.MatchValue, err = userContextValue(subject, ref.MatchUserAttribute)
					if err != nil {
						return nil, err
					}
				}
				var output c.ObjectContextResult
				output, err = s.ObjectContext(ctx, subject.user.TenantID, input)
				if err == nil {
					stamp, decodeErr := hex.DecodeString(output.Digest)
					if decodeErr != nil || len(stamp) != 32 {
						return nil, contextUnavailable()
					}
					value, dependencies[key], sources[ref.SchemaID] = bytes.Clone(output.Value), output.Digest, true
					objectReads = append(objectReads, objectRead{key: key, input: input})
				}
			}
			if err != nil {
				return nil, contextUnavailable()
			}
			cache[key] = value
		}
		e.Value, e.ValueRef = value, nil
		return &e, nil
	}
	for i := range resolved.Rules {
		var err error
		resolved.Rules[i].RowFilter, err = resolve(resolved.Rules[i].RowFilter)
		if err != nil {
			return resolved, true, err
		}
		resolved.Rules[i].NewRowFilter, err = resolve(resolved.Rules[i].NewRowFilter)
		if err != nil {
			return resolved, true, err
		}
	}
	// Complete the first pass before rereading any operand. Reusing each exact
	// request catches fields collected across a row ETag/definition transition,
	// including a change whose final value happens to equal the previous value.
	for _, read := range objectReads {
		output, err := s.ObjectContext(ctx, subject.user.TenantID, read.input)
		if err != nil || output.Digest != dependencies[read.key] || !bytes.Equal(output.Value, cache[read.key]) {
			return resolved, true, contextUnavailable()
		}
	}
	// Recheck source bindings as well as resolved target values in one Metadata
	// snapshot; validating only literals would lose the source type/selector.
	if err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := validatePolicy(ctx, tx, subject.user.TenantID, c.PermissionPolicy{Rules: originalRules}); err != nil {
			return err
		}
		return validatePolicy(ctx, tx, subject.user.TenantID, c.PermissionPolicy{Rules: resolved.Rules})
	}); err != nil {
		return resolved, true, contextUnavailable()
	}
	if len(sources) > 0 {
		for id := range sources {
			resolved.ContextSchemaIDs = append(resolved.ContextSchemaIDs, id)
		}
		sort.Strings(resolved.ContextSchemaIDs)
		raw, _ := json.Marshal(dependencies)
		stamp := sha256.Sum256(raw)
		resolved.ContextDigest = hex.EncodeToString(stamp[:])
	}
	return resolved, true, nil
}
