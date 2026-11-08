package data

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"golang.org/x/text/unicode/norm"
)

var savedParameterName = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_]{0,63}$`)

func cloneSavedInput(input c.SavedQueryInput) (c.SavedQueryInput, error) {
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > c.MaxSavedQueryBytes {
		return input, problem(422, "saved_query_budget", "保存查询模板不能超过128KiB")
	}
	var out c.SavedQueryInput
	if err := json.Unmarshal(encoded, &out); err != nil {
		return input, problem(422, "invalid_saved_query", "查询模板不合法")
	}
	return out, nil
}

func savedInputFields(input c.SavedQueryInput) error {
	if !utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) == 0 || utf8.RuneCountInString(input.Name) > 128 || len(nameKey(input.Name)) > 512 || strings.IndexFunc(input.Name, unicode.IsControl) >= 0 {
		return problem(422, "invalid_saved_query_name", "保存查询名称须为1..128个字符且不能包含控制字符")
	}
	if input.Source.Kind != "visual" && input.Source.Kind != "ontology-sql" || !utf8.ValidString(input.Source.Text) || len(input.Source.Text) > maxOntologySQLBytes || strings.ContainsRune(input.Source.Text, 0) || input.Source.DefaultCategoryID != "" && !uuidPattern.MatchString(input.Source.DefaultCategoryID) {
		return problem(422, "invalid_saved_query_source", "查询来源须为visual或ontology-sql，编辑文本不能超过64KiB")
	}
	if input.Source.Kind == "visual" && input.Source.Text != "" {
		return problem(422, "invalid_saved_query_source", "可视化查询不能携带SQL编辑文本")
	}
	if !uuidPattern.MatchString(input.Query.From.SchemaID) || input.Query.From.Schema != "" || input.Query.From.Category != "" || input.Query.Rows != nil || input.Query.Via != nil || input.Query.Page.Cursor != "" {
		return problem(422, "unsupported_saved_query", "保存查询仅支持稳定Schema ID的单模型模板，不能保存关联模式、名称绑定或运行游标")
	}
	if len(input.Parameters) > c.MaxSavedQueryParameters {
		return problem(422, "saved_query_budget", "保存查询最多64个参数")
	}
	return nil
}

func declarationForProperty(name string, property c.Property, array bool) c.SavedQueryParameter {
	return c.SavedQueryParameter{Name: name, DataType: property.DataType, Nullable: array && property.Nullable, Precision: property.Precision, Scale: property.Scale, Array: array, EnumID: property.EnumID}
}

func savedSampleValue(property c.Property) json.RawMessage {
	if property.EnumID != "" && len(property.EnumValues) > 0 {
		raw, _ := json.Marshal(property.EnumValues[0].Code)
		return raw
	}
	switch property.DataType {
	case "boolean":
		return json.RawMessage("false")
	case "int64", "decimal":
		return json.RawMessage(`"0"`)
	case "timestamp":
		return json.RawMessage(`"2000-01-01T00:00:00Z"`)
	case "uuid":
		return json.RawMessage(`"00000000-0000-4000-8000-000000000000"`)
	default:
		return json.RawMessage(`""`)
	}
}

// savedParameterFields rejects name-based field fallback before the ordinary
// query compiler runs. Samples check logical capabilities without scanning rows;
// they are never persisted and never used for an execution.
func savedParameterFields(query *c.QuerySpec, schema *c.Schema, declarations []c.SavedQueryParameter, values map[string]json.RawMessage, sample bool) ([]c.SavedQueryParameter, error) {
	fields := map[string]c.Property{}
	for _, property := range schema.Properties {
		fields[property.ID] = property
	}
	for _, id := range query.Select {
		if _, exists := fields[id]; !exists {
			return nil, problem(422, "saved_query_binding_changed", "保存查询投影必须引用仍存在的稳定属性ID")
		}
	}
	measureIDs := map[string]bool{}
	if query.Aggregate != nil {
		for _, id := range query.Aggregate.GroupBy {
			if _, exists := fields[id]; !exists {
				return nil, problem(422, "saved_query_binding_changed", "聚合分组必须引用仍存在的稳定属性ID")
			}
		}
		for _, measure := range query.Aggregate.Measures {
			measureIDs[measure.ID] = true
			if measure.Field != "" {
				if _, exists := fields[measure.Field]; !exists {
					return nil, problem(422, "saved_query_binding_changed", "聚合计量必须引用仍存在的稳定属性ID")
				}
			}
		}
	}
	for _, order := range query.OrderBy {
		if _, exists := fields[order.Field]; !exists && !measureIDs[order.Field] {
			return nil, problem(422, "saved_query_binding_changed", "保存查询排序必须引用仍存在的稳定属性ID")
		}
	}
	provided := map[string]c.SavedQueryParameter{}
	for _, declaration := range declarations {
		if !savedParameterName.MatchString(declaration.Name) || len(declaration.Name) > 128 {
			return nil, problem(422, "invalid_saved_query_parameter", "参数名须为1..64个字母、数字或下划线，且不能以数字开头")
		}
		if _, exists := provided[declaration.Name]; exists {
			return nil, problem(422, "invalid_saved_query_parameter", "参数声明不能重名")
		}
		provided[declaration.Name] = declaration
	}
	used := map[string]c.SavedQueryParameter{}
	nodes := 0
	var visit func(*c.Expression, int) error
	visit = func(expression *c.Expression, depth int) error {
		if expression == nil {
			return nil
		}
		nodes++
		if nodes > 100 || depth > 10 || expression.ValueRef != nil {
			return problem(422, "invalid_saved_query_expression", "查询条件超过预算或携带不支持的动态授权引用")
		}
		logical := expression.Op == "and" || expression.Op == "or" || expression.Op == "not"
		if logical {
			if expression.Parameter != "" {
				return problem(422, "invalid_saved_query_parameter", "逻辑操作符不能使用参数占位")
			}
			for i := range expression.Args {
				if err := visit(&expression.Args[i], depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		property, exists := fields[expression.Field]
		if !exists {
			return problem(422, "saved_query_binding_changed", "保存查询条件必须引用仍存在的稳定属性ID")
		}
		if expression.Parameter == "" {
			return nil
		}
		if len(expression.Value) != 0 || len(expression.Args) != 0 || expression.Op == "isNull" || expression.Op == "isNotNull" {
			return problem(422, "invalid_saved_query_parameter", "参数占位不能同时携带值、子条件或空值操作符")
		}
		array := expression.Op == "in" || expression.Op == "notIn"
		expected := declarationForProperty(expression.Parameter, property, array)
		declaration, declared := provided[expression.Parameter]
		if !declared || declaration != expected {
			return problem(422, "saved_query_parameter_type", "参数声明必须与其绑定属性的当前类型及集合语义一致")
		}
		if prior, exists := used[expression.Parameter]; exists && prior != expected {
			return problem(422, "saved_query_parameter_type", "同名参数不能绑定不同类型、枚举或集合语义")
		}
		used[expression.Parameter] = expected
		var raw json.RawMessage
		if sample {
			raw = savedSampleValue(property)
			if array {
				raw, _ = json.Marshal([]json.RawMessage{raw})
			}
		} else {
			var exists bool
			raw, exists = values[expression.Parameter]
			if !exists {
				return problem(422, "missing_saved_query_parameter", "缺少查询运行参数")
			}
		}
		if err := validateSavedParameterValue(property, raw, array); err != nil {
			return err
		}
		var compact bytes.Buffer
		if json.Compact(&compact, raw) != nil {
			return problem(422, "invalid_saved_query_parameter", "运行参数须为合法JSON值")
		}
		expression.Parameter, expression.Value = "", append(json.RawMessage(nil), compact.Bytes()...)
		return nil
	}
	if err := visit(query.Where, 0); err != nil {
		return nil, err
	}
	if len(used) != len(provided) {
		return nil, problem(422, "unused_saved_query_parameter", "参数声明包含未使用项")
	}
	if !sample {
		if len(values) != len(used) {
			return nil, problem(422, "unused_saved_query_parameter", "运行参数包含查询未使用的项")
		}
		encoded, _ := json.Marshal(values)
		if len(encoded) > c.MaxSavedQueryBytes {
			return nil, problem(422, "saved_query_budget", "运行参数总大小不能超过128KiB")
		}
	}
	out := make([]c.SavedQueryParameter, 0, len(used))
	for _, declaration := range used {
		out = append(out, declaration)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func validateSavedParameterValue(property c.Property, raw json.RawMessage, array bool) error {
	var items []json.RawMessage
	if array {
		if json.Unmarshal(raw, &items) != nil || len(items) < 1 || len(items) > 100 {
			return problem(422, "invalid_saved_query_parameter", "集合参数须包含1..100个标量值")
		}
	} else {
		items = []json.RawMessage{raw}
	}
	for _, item := range items {
		if !array && bytes.Equal(bytes.TrimSpace(item), []byte("null")) {
			return problem(422, "invalid_saved_query_parameter", "标量比较参数不能为null，请使用空值操作符")
		}
		if _, err := typedValue(property, item); err != nil {
			return problem(422, "saved_query_parameter_type", "运行参数与属性类型或精度不符")
		}
		if err := c.ValidateEnumValue(property, item, true); err != nil {
			return problem(422, "saved_query_parameter_type", "枚举查询参数必须使用已定义的业务code")
		}
	}
	return nil
}

func (h *handler) validateSavedInput(ctx context.Context, p c.Principal, input c.SavedQueryInput) (c.SavedQueryInput, c.CurrentDefinition, error) {
	input.Name = norm.NFC.String(strings.TrimSpace(input.Name))
	if err := savedInputFields(input); err != nil {
		return input, c.CurrentDefinition{}, err
	}
	input, err := cloneSavedInput(input)
	if err != nil {
		return input, c.CurrentDefinition{}, err
	}
	current, err := h.current(ctx, p.TenantID, input.Query.From.SchemaID)
	if err != nil {
		return input, current, err
	}
	if err := validateSchema(current.Definition); err != nil {
		return input, current, err
	}
	discovery, err := h.authorize(ctx, p, "definition.read", current.Definition.ID)
	if err != nil {
		return input, current, err
	}
	if discovery.RowFilter != nil || discovery.NewRowFilter != nil {
		return input, current, problem(422, "unsupported_authorization", "保存查询定义发现不支持行条件授权")
	}
	sample, _ := cloneSavedInput(input)
	declarations, err := savedParameterFields(&sample.Query, current.Definition.Schema, input.Parameters, nil, true)
	if err != nil {
		return input, current, err
	}
	bound, err := h.validateBoundQuery(ctx, p, sample.Query)
	if err != nil {
		return input, current, err
	}
	// Stable references have already been checked above. Retain the shared
	// compiler's canonical order/measure labels while restoring placeholders;
	// temporary type-checking samples must never enter persisted definitions.
	bound.Where = input.Query.Where
	input.Query = bound
	if err := h.recheck(ctx, p, "definition.read", current.Definition.ID, discovery); err != nil {
		return input, current, err
	}
	latest, err := h.current(ctx, p.TenantID, current.Definition.ID)
	if err != nil {
		return input, current, err
	}
	if latest.ETag != current.ETag || c.EnumContextDigest(latest.Definition) != c.EnumContextDigest(current.Definition) {
		return input, current, problem(409, "definition_changed", "模型或枚举已改变，请重新检查保存查询")
	}
	input.Parameters = declarations
	encoded, _ := json.Marshal(input)
	if len(encoded) > c.MaxSavedQueryBytes {
		return input, current, problem(422, "saved_query_budget", "规范化后的保存查询模板不能超过128KiB")
	}
	return input, current, nil
}
