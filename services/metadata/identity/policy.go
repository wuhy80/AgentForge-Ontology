package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func policyRecord(ctx context.Context, tx *sql.Tx, tenant string) (contracts.PolicyRecord, error) {
	return scanPolicy(tx.QueryRowContext(ctx, "SELECT "+policyColumns+" FROM af_identity_tenants WHERE tenant_id=?", tenant))
}

func (s *Store) getPolicy(w http.ResponseWriter, r *http.Request) error {
	var result contracts.PolicyRecord
	err := s.adminRead(r, func(tx *sql.Tx, actor contracts.User) error {
		var err error
		result, err = policyRecord(r.Context(), tx, actor.TenantID)
		return err
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+result.ETag+`"`)
	writeJSON(w, 200, result)
	return nil
}

func (s *Store) savePolicy(w http.ResponseWriter, r *http.Request) error {
	var input contracts.PermissionPolicy
	if err := decode(r, &input); err != nil {
		return err
	}
	for i := range input.Rules {
		if input.Rules[i].ID == "" {
			input.Rules[i].ID = contracts.NewID()
		}
	}
	var result contracts.PolicyRecord
	err := s.mutation(r, true, func(tx *sql.Tx, actor contracts.User) error {
		old, err := policyRecord(r.Context(), tx, actor.TenantID)
		if err != nil {
			return err
		}
		if err := match(r, old.ETag); err != nil {
			return err
		}
		if err := validatePolicy(r.Context(), tx, actor.TenantID, input); err != nil {
			return err
		}
		etag, err := randomToken()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE af_identity_tenants SET draft_policy=?,policy_etag=? WHERE tenant_id=?", marshal(input), etag, actor.TenantID); err != nil {
			return err
		}
		if err := audit(r.Context(), tx, actor.TenantID, actor.ID, "policy.save_draft", actor.TenantID); err != nil {
			return err
		}
		result, err = policyRecord(r.Context(), tx, actor.TenantID)
		return err
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+result.ETag+`"`)
	writeJSON(w, 200, result)
	return nil
}

func (s *Store) publishPolicy(w http.ResponseWriter, r *http.Request) error {
	var result contracts.PolicyRecord
	err := s.mutation(r, true, func(tx *sql.Tx, actor contracts.User) error {
		old, err := policyRecord(r.Context(), tx, actor.TenantID)
		if err != nil {
			return err
		}
		if err := match(r, old.ETag); err != nil {
			return err
		}
		if old.Draft == nil {
			result = old
			return nil
		}
		if err := validatePolicy(r.Context(), tx, actor.TenantID, *old.Draft); err != nil {
			return err
		}
		etag, err := randomToken()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE af_identity_tenants SET current_policy=?,draft_policy=NULL,policy_etag=?,authorization_state=? WHERE tenant_id=?", marshal(*old.Draft), etag, etag, actor.TenantID); err != nil {
			return err
		}
		if err := audit(r.Context(), tx, actor.TenantID, actor.ID, "policy.publish", actor.TenantID); err != nil {
			return err
		}
		result, err = policyRecord(r.Context(), tx, actor.TenantID)
		return err
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+result.ETag+`"`)
	writeJSON(w, 200, result)
	return nil
}

func (s *Store) simulate(w http.ResponseWriter, r *http.Request) error {
	var input contracts.SimulationRequest
	if err := decode(r, &input); err != nil {
		return err
	}
	var runtime contracts.RuntimePolicy
	var principal contracts.Principal
	err := s.mutation(r, true, func(tx *sql.Tx, actor contracts.User) error {
		user, err := scanUser(tx.QueryRowContext(r.Context(), "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id=?", actor.TenantID, input.UserID))
		if err != nil {
			return err
		}
		principal = contracts.Principal{TenantID: actor.TenantID, Subject: user.ID, ExpiresAt: time.Now().Add(time.Minute).Unix()}
		policy, err := policyRecord(r.Context(), tx, actor.TenantID)
		if err != nil {
			return err
		}
		candidate := policy.Current
		if input.Draft {
			if policy.Draft == nil {
				return problem(422, "draft_missing", "没有可模拟的策略草稿")
			}
			candidate = *policy.Draft
		}
		if err := validatePolicy(r.Context(), tx, actor.TenantID, candidate); err != nil {
			return err
		}
		runtime = contracts.RuntimePolicy{State: policy.State, Rules: []contracts.PermissionRule{}}
		if user.Enabled {
			ids, err := effectiveRoleIDs(r.Context(), tx, user)
			if err != nil {
				return err
			}
			active := map[string]bool{}
			for _, id := range ids {
				active[id] = true
			}
			for _, rule := range candidate.Rules {
				if active[rule.RoleID] {
					runtime.Rules = append(runtime.Rules, rule)
				}
			}
		}
		return audit(r.Context(), tx, actor.TenantID, actor.ID, "policy.simulate", user.ID)
	})
	if err != nil {
		return err
	}
	decision, err := platform.EvaluateRuntimePolicy(r.Context(), s.OPA, &runtime, principal, input.Request)
	if err != nil {
		return err
	}
	reason := "没有匹配的允许规则，或命中显式拒绝"
	if decision.Allow {
		reason = "匹配当前角色授权；行条件仍需在实际数据访问时校验"
	}
	writeJSON(w, 200, contracts.SimulationResult{Decision: decision, Reason: reason})
	return nil
}

func validatePolicy(ctx context.Context, tx *sql.Tx, tenant string, policy contracts.PermissionPolicy) error {
	if err := platform.ValidatePermissionPolicy(policy); err != nil {
		return err
	}
	roles := map[string]bool{}
	schemas := map[string]*contracts.Schema{}
	for _, rule := range policy.Rules {
		if !roles[rule.RoleID] {
			if _, err := roleIDs(ctx, tx, tenant, []string{rule.RoleID}); err != nil {
				return err
			}
			roles[rule.RoleID] = true
		}
		instance := strings.HasPrefix(rule.Operation, "instance.")
		if !instance && (rule.RowFilter != nil || rule.NewRowFilter != nil) {
			return problem(422, "unsupported_policy", "定义与组织管理不支持行条件")
		}
		if rule.ResourceID == "*" {
			if rule.RowFilter != nil || rule.NewRowFilter != nil || !wildcardFields(rule.ReadFields) || !wildcardFields(rule.WriteFields) {
				return problem(422, "unsupported_policy", "通配资源只支持全部或空字段集合，不能声明行条件")
			}
			continue
		}
		if !instance {
			var count int
			table := "af_meta_definitions"
			if strings.HasPrefix(rule.Operation, "organization.") {
				table = "af_meta_organizations"
			}
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE tenant_id=? AND id=?", tenant, rule.ResourceID).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return problem(422, "invalid_policy_resource", "授权资源必须属于当前租户")
			}
			if !wildcardFields(rule.ReadFields) || !wildcardFields(rule.WriteFields) {
				return problem(422, "unsupported_policy", "定义与组织授权以完整对象为单位，不支持属性投影")
			}
			continue
		}
		schema, exists := schemas[rule.ResourceID]
		if !exists {
			var raw string
			if err := tx.QueryRowContext(ctx, "SELECT current_json FROM af_meta_definitions WHERE tenant_id=? AND id=? AND kind='schema' AND current_json IS NOT NULL", tenant, rule.ResourceID).Scan(&raw); err != nil {
				if err == sql.ErrNoRows {
					return problem(422, "invalid_policy_resource", "数据授权必须绑定当前租户已生效的对象模型")
				}
				return err
			}
			var definition contracts.Definition
			if err := json.Unmarshal([]byte(raw), &definition); err != nil {
				return err
			}
			schema = definition.Schema
			if schema == nil {
				return problem(422, "invalid_policy_resource", "授权目标不是对象模型")
			}
			schemas[rule.ResourceID] = schema
		}
		fields := map[string]contracts.Property{}
		for _, field := range schema.Properties {
			fields[field.ID] = field
		}
		for _, list := range [][]string{rule.ReadFields, rule.WriteFields} {
			for _, id := range list {
				if id != "*" {
					if _, ok := fields[id]; !ok {
						return problem(422, "invalid_policy_field", "权限字段必须使用目标模型的当前属性绑定")
					}
				}
			}
		}
		for _, expression := range []*contracts.Expression{rule.RowFilter, rule.NewRowFilter} {
			if err := validateCondition(expression, fields); err != nil {
				return err
			}
		}
		if rule.NewRowFilter != nil && rule.Operation != "instance.create" && rule.Operation != "instance.patch" {
			return problem(422, "unsupported_policy", "只有创建和修改操作可声明新值条件")
		}
	}
	return nil
}

func wildcardFields(fields []string) bool {
	return len(fields) == 0 || len(fields) == 1 && fields[0] == "*"
}

func validateCondition(expression *contracts.Expression, fields map[string]contracts.Property) error {
	if expression == nil {
		return nil
	}
	if expression.Op == "and" || expression.Op == "or" || expression.Op == "not" {
		for i := range expression.Args {
			if err := validateCondition(&expression.Args[i], fields); err != nil {
				return err
			}
		}
		return nil
	}
	field, ok := fields[expression.Field]
	if !ok {
		return problem(422, "invalid_policy_field", "行条件必须绑定当前模型的属性")
	}
	if expression.Op == "isNull" || expression.Op == "isNotNull" {
		return nil
	}
	if expression.Op == "startsWith" && field.DataType != "string" && field.DataType != "text" {
		return problem(422, "invalid_policy_type", "前缀条件仅适用于文本")
	}
	if field.DataType == "boolean" && expression.Op != "eq" && expression.Op != "ne" && expression.Op != "in" && expression.Op != "notIn" {
		return problem(422, "invalid_policy_type", "布尔条件不支持大小比较")
	}
	values := []json.RawMessage{expression.Value}
	if expression.Op == "in" || expression.Op == "notIn" {
		if json.Unmarshal(expression.Value, &values) != nil {
			return problem(422, "invalid_policy_type", "集合条件必须是同类型值列表")
		}
	}
	for _, value := range values {
		if err := validateValue(field, value); err != nil {
			return err
		}
	}
	return nil
}

var uuidValue = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var decimalValue = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func validateValue(field contracts.Property, raw json.RawMessage) error {
	bad := func() error {
		return problem(422, "invalid_policy_type", "行条件值与属性类型或范围不匹配")
	}
	if string(raw) == "null" {
		return bad()
	}
	if field.DataType == "boolean" {
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return bad()
		}
		return nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return bad()
	}
	switch field.DataType {
	case "string":
		if len([]rune(value)) > 255 {
			return bad()
		}
	case "text":
		if len(value) > 65535 {
			return bad()
		}
	case "uuid":
		if !uuidValue.MatchString(value) {
			return bad()
		}
	case "int64":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || strconv.FormatInt(parsed, 10) != value {
			return bad()
		}
	case "decimal":
		if !decimalValue.MatchString(value) {
			return bad()
		}
		parts := strings.Split(strings.TrimPrefix(value, "-"), ".")
		fraction := 0
		if len(parts) == 2 {
			fraction = len(parts[1])
		}
		if len(strings.TrimLeft(parts[0], "0")) > field.Precision-field.Scale || fraction > field.Scale {
			return bad()
		}
	case "timestamp":
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || parsed.Nanosecond()%1000 != 0 || parsed.UTC().Year() < 1000 || parsed.UTC().Year() > 9999 {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}
