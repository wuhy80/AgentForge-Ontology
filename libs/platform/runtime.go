package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

func policyError(message string) error {
	return &AccessError{Status: 422, Code: "unsupported_policy_combination", Message: message}
}

func policyOperation(operation string) bool {
	switch operation {
	case "organization.read", "organization.edit", "definition.read", "definition.edit", "definition.publish", "instance.create", "instance.read", "instance.patch", "instance.delete", "instance.query", "link.query", "link.create", "link.delete", "function.invoke", "action.invoke":
		return true
	}
	return false
}

func validIdentifier(value string, wildcard bool) bool {
	return value != "" && len(value) <= 200 && strings.TrimSpace(value) == value && (wildcard || value != "*") && !strings.ContainsAny(value, "\x00\r\n")
}

func canonicalFields(fields []string) ([]string, error) {
	if len(fields) > 256 {
		return nil, policyError("权限字段超过预算")
	}
	unique := map[string]bool{}
	for _, field := range fields {
		if !validIdentifier(field, true) {
			return nil, policyError("权限字段标识不合法")
		}
		unique[field] = true
	}
	if unique["*"] {
		return []string{"*"}, nil
	}
	result := make([]string, 0, len(unique))
	for field := range unique {
		result = append(result, field)
	}
	sort.Strings(result)
	return result, nil
}

func validateFilter(expression *contracts.Expression) error {
	nodes := 0
	var visit func(*contracts.Expression, int) error
	visit = func(e *contracts.Expression, depth int) error {
		if e == nil {
			return nil
		}
		nodes++
		if nodes > 100 || depth > 10 {
			return policyError("权限条件超过节点或深度预算")
		}
		if e.Op == "and" || e.Op == "or" || e.Op == "not" {
			if e.Field != "" || len(e.Value) != 0 || len(e.Args) == 0 || (e.Op == "not" && len(e.Args) != 1) {
				return policyError("权限条件结构不合法")
			}
			for i := range e.Args {
				if err := visit(&e.Args[i], depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		if len(e.Args) > 0 || !validIdentifier(e.Field, false) {
			return policyError("权限条件必须引用稳定属性标识")
		}
		switch e.Op {
		case "isNull", "isNotNull":
			if len(e.Value) != 0 {
				return policyError("空值条件不能附带值")
			}
			return nil
		case "in", "notIn":
			var values []json.RawMessage
			if json.Unmarshal(e.Value, &values) != nil || len(values) == 0 || len(values) > 100 {
				return policyError("集合条件必须包含 1 至 100 个标量")
			}
			for _, value := range values {
				if !scalarValue(value, false) {
					return policyError("集合条件只支持非空标量")
				}
			}
			return nil
		case "eq", "ne", "lt", "lte", "gt", "gte", "startsWith":
			if !scalarValue(e.Value, e.Op == "startsWith") {
				return policyError("比较条件必须使用非空标量")
			}
			return nil
		default:
			return policyError("权限条件使用了未支持的操作符")
		}
	}
	return visit(expression, 0)
}

func scalarValue(raw json.RawMessage, onlyString bool) bool {
	if len(raw) == 0 || len(raw) > 65536 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return false
	}
	switch value.(type) {
	case string:
		return true
	case bool:
		return !onlyString
	case json.Number:
		if onlyString || len(value.(json.Number)) > 256 {
			return false
		}
		text := string(value.(json.Number))
		if index := strings.IndexAny(text, "eE"); index >= 0 {
			exponent, err := strconv.Atoi(text[index+1:])
			if err != nil || exponent < -1000 || exponent > 1000 {
				return false
			}
		}
		return true
	}
	return false
}

// ValidatePermissionPolicy checks the supported protocol and budgets. The
// authoritative store additionally binds roles, resources and property types.
func ValidatePermissionPolicy(policy contracts.PermissionPolicy) error {
	if len(policy.Rules) > 512 {
		return policyError("权限规则超过 512 条预算")
	}
	ids := map[string]bool{}
	for _, rule := range policy.Rules {
		if !validIdentifier(rule.ID, false) || ids[rule.ID] || !validIdentifier(rule.RoleID, false) || !validIdentifier(rule.ResourceID, true) || !policyOperation(rule.Operation) || (rule.Effect != "allow" && rule.Effect != "deny") {
			return policyError("权限规则标识、操作或效果不合法")
		}
		ids[rule.ID] = true
		if _, err := canonicalFields(rule.ReadFields); err != nil {
			return err
		}
		if _, err := canonicalFields(rule.WriteFields); err != nil {
			return err
		}
		if rule.Effect == "deny" && (len(rule.ReadFields) > 0 || len(rule.WriteFields) > 0 || rule.RowFilter != nil || rule.NewRowFilter != nil) {
			return policyError("拒绝规则仅支持整项操作，不支持字段或行条件")
		}
		if !strings.HasPrefix(rule.Operation, "instance.") && (rule.RowFilter != nil || rule.NewRowFilter != nil) {
			return policyError("定义、组织与关系操作不支持行条件")
		}
		if (strings.HasPrefix(rule.Operation, "link.") || callableOperation(rule.Operation)) && (!wholeResourceFields(rule.ReadFields) || !wholeResourceFields(rule.WriteFields)) {
			return policyError("关系及函数动作调用授权以完整资源为单位，不支持字段投影")
		}
		if err := validateFilter(rule.RowFilter); err != nil {
			return err
		}
		if err := validateFilter(rule.NewRowFilter); err != nil {
			return err
		}
	}
	return nil
}

func wholeResourceFields(fields []string) bool {
	return len(fields) == 0 || len(fields) == 1 && fields[0] == "*"
}

func callableOperation(operation string) bool {
	return operation == "function.invoke" || operation == "action.invoke"
}

func validateDecision(decision contracts.Decision) error {
	if strings.TrimSpace(decision.State) == "" || len(decision.State) > 512 {
		return errors.New("authorization state is required")
	}
	if _, err := canonicalFields(decision.ReadFields); err != nil {
		return err
	}
	if _, err := canonicalFields(decision.WriteFields); err != nil {
		return err
	}
	if err := validateFilter(decision.RowFilter); err != nil {
		return err
	}
	return validateFilter(decision.NewRowFilter)
}

// expressionKey preserves exact JSON numbers, including decimal values beyond
// float64 precision, while accepting the equivalent formatting produced by OPA.
func expressionKey(expression *contracts.Expression) string {
	if expression == nil {
		return "null"
	}
	data, _ := json.Marshal(expression)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	_ = decoder.Decode(&value)
	var normalize func(any) any
	normalize = func(v any) any {
		switch item := v.(type) {
		case json.Number:
			n, ok := new(big.Rat).SetString(string(item))
			if ok {
				return map[string]string{"number": n.RatString()}
			}
		case map[string]any:
			for k, entry := range item {
				item[k] = normalize(entry)
			}
		case []any:
			for i, entry := range item {
				item[i] = normalize(entry)
			}
		}
		return v
	}
	key, _ := json.Marshal(normalize(value))
	return string(key)
}

func unionFilters(filters []*contracts.Expression) *contracts.Expression {
	unique := map[string]bool{}
	args := make([]contracts.Expression, 0, len(filters))
	for _, expression := range filters {
		if expression == nil {
			return nil
		}
		key := expressionKey(expression)
		if !unique[key] {
			unique[key] = true
			args = append(args, *expression)
		}
	}
	if len(args) == 1 {
		return &args[0]
	}
	return &contracts.Expression{Op: "or", Args: args}
}

func mergeRuntime(runtime *contracts.RuntimePolicy, request contracts.AuthorizationRequest) (contracts.Decision, error) {
	if runtime == nil || strings.TrimSpace(runtime.State) == "" || len(runtime.State) > 512 {
		return contracts.Decision{}, errors.New("authoritative policy state is required")
	}
	if err := ValidatePermissionPolicy(contracts.PermissionPolicy{Rules: runtime.Rules}); err != nil {
		return contracts.Decision{}, err
	}
	if !policyOperation(request.Operation) {
		return contracts.Decision{}, policyError("未支持的业务授权操作")
	}
	if strings.HasPrefix(request.Operation, "link.") && request.Kind != "" && request.Kind != contracts.KindLink {
		return contracts.Decision{}, policyError("关系操作必须使用Link资源类型")
	}
	if request.Operation == "function.invoke" && request.Kind != contracts.KindFunction || request.Operation == "action.invoke" && request.Kind != contracts.KindAction {
		return contracts.Decision{}, policyError("函数或动作调用必须声明对应的资源类型")
	}
	if callableOperation(request.Operation) && !validIdentifier(request.ResourceID, false) {
		return contracts.Decision{}, policyError("调用必须指定具体函数或动作的稳定标识")
	}
	decision := contracts.Decision{State: runtime.State}
	var oldFilters, newFilters []*contracts.Expression
	for _, rule := range runtime.Rules {
		if rule.Operation != request.Operation || (rule.ResourceID != "*" && rule.ResourceID != request.ResourceID) {
			continue
		}
		if rule.Effect == "deny" {
			return decision, nil
		}
	}
	for _, rule := range runtime.Rules {
		if rule.Effect != "allow" || rule.Operation != request.Operation || (rule.ResourceID != "*" && rule.ResourceID != request.ResourceID) {
			continue
		}
		read, _ := canonicalFields(rule.ReadFields)
		write, _ := canonicalFields(rule.WriteFields)
		// Link permissions grant the operation as a whole. Accepted [] and [*]
		// field lists carry no projection semantics and cannot create a conflict
		// when two roles grant the same static relation operation.
		if strings.HasPrefix(request.Operation, "link.") || callableOperation(request.Operation) {
			read, write = nil, nil
		}
		if decision.Allow && (strings.Join(read, "\x00") != strings.Join(decision.ReadFields, "\x00") || strings.Join(write, "\x00") != strings.Join(decision.WriteFields, "\x00")) {
			return contracts.Decision{}, policyError("多角色字段范围不同，无法安全合并权限")
		}
		decision.Allow, decision.ReadFields, decision.WriteFields = true, read, write
		oldFilters = append(oldFilters, rule.RowFilter)
		newFilter := rule.NewRowFilter
		if newFilter == nil {
			newFilter = rule.RowFilter
		}
		newFilters = append(newFilters, newFilter)
	}
	if !decision.Allow {
		return decision, nil
	}
	if request.Operation == "instance.patch" && len(oldFilters) > 1 {
		sameOld, sameNew := true, true
		for i := 1; i < len(oldFilters); i++ {
			sameOld = sameOld && expressionKey(oldFilters[i]) == expressionKey(oldFilters[0])
			sameNew = sameNew && expressionKey(newFilters[i]) == expressionKey(newFilters[0])
		}
		if !sameOld && !sameNew {
			return contracts.Decision{}, policyError("多角色修改前后行条件无法安全合并")
		}
	}
	decision.RowFilter, decision.NewRowFilter = unionFilters(oldFilters), unionFilters(newFilters)
	if err := validateDecision(decision); err != nil {
		return contracts.Decision{}, err
	}
	return decision, nil
}

// EvaluateRuntimePolicy keeps the row/field grant coupled, then asks OPA to
// evaluate this request's authoritative state. A runtime input never falls back
// to example/static grants. No allow is cached across requests or outages.
func EvaluateRuntimePolicy(ctx context.Context, opa *OPAAuthorizer, runtime *contracts.RuntimePolicy, principal contracts.Principal, request contracts.AuthorizationRequest) (contracts.Decision, error) {
	var denied contracts.Decision
	if strings.TrimSpace(principal.Subject) == "" || strings.TrimSpace(principal.TenantID) == "" || request.Operation == "" {
		return denied, errors.New("missing authorization context")
	}
	grant, err := mergeRuntime(runtime, request)
	if err != nil {
		return denied, err
	}
	if opa == nil {
		return denied, errors.New("authorization engine is not configured")
	}
	endpoint, err := url.Parse(opa.URL)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return denied, errors.New("authorization engine is not configured")
	}
	input := map[string]any{"input": map[string]any{"principal": principal, "request": request, "runtime": map[string]any{"state": runtime.State, "tenantId": principal.TenantID, "subject": principal.Subject, "operation": request.Operation, "resourceId": request.ResourceID, "grant": grant}}}
	value, err := json.Marshal(input)
	if err != nil {
		return denied, errors.New("invalid authorization input")
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	call, err := http.NewRequestWithContext(callCtx, http.MethodPost, strings.TrimRight(opa.URL, "/")+"/v1/data/ontology/decision", bytes.NewReader(value))
	if err != nil {
		return denied, errors.New("invalid authorization endpoint")
	}
	call.Header.Set("Content-Type", "application/json")
	if opa.BearerToken != "" {
		call.Header.Set("Authorization", "Bearer "+opa.BearerToken)
	}
	client := http.Client{Timeout: 5 * time.Second}
	if opa.Client != nil {
		client = *opa.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(call)
	if err != nil {
		return denied, errors.New("authorization engine is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return denied, errors.New("authorization engine rejected request")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return denied, errors.New("invalid authorization response size")
	}
	var wire struct {
		Result *contracts.Decision `json:"result"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Result == nil {
		return denied, errors.New("invalid authorization response")
	}
	actual := *wire.Result
	if !actual.Allow {
		return denied, nil
	}
	if !grant.Allow || validateDecision(actual) != nil || actual.State != grant.State {
		return denied, errors.New("authorization decision does not match authoritative state")
	}
	read, _ := canonicalFields(actual.ReadFields)
	write, _ := canonicalFields(actual.WriteFields)
	if strings.Join(read, "\x00") != strings.Join(grant.ReadFields, "\x00") || strings.Join(write, "\x00") != strings.Join(grant.WriteFields, "\x00") || expressionKey(actual.RowFilter) != expressionKey(grant.RowFilter) || expressionKey(actual.NewRowFilter) != expressionKey(grant.NewRowFilter) {
		return denied, errors.New("authorization engine changed authoritative constraints")
	}
	return grant, nil
}
