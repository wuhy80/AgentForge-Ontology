package data

import (
	"encoding/json"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type ontologySQLBinder struct {
	statement      *ontologySQLStatement
	definition     c.Definition
	parameters     map[string]json.RawMessage
	analysis       c.OntologySQLAnalysis
	parameterIndex map[string]int
	missing        bool
	rowScope       *ontologySQLRowScope
	nodes          int
}

func (b *ontologySQLBinder) fail(code, message string, at c.SQLRange) {
	if len(b.analysis.Diagnostics) < 100 {
		b.analysis.Diagnostics = append(b.analysis.Diagnostics, ontologySQLDiagnostic(code, message, at))
	}
}

func (b *ontologySQLBinder) field(field ontologySQLField) (c.Property, bool) {
	if b.rowScope != nil {
		return b.rowField(field)
	}
	if field.Qualifier != "" && ontologySQLNameKey(field.Qualifier) != ontologySQLNameKey(b.statement.Alias) {
		b.fail("unknown_qualifier", "字段限定名与 FROM 中的模型别名不一致", field.Range)
		return c.Property{}, false
	}
	for _, p := range b.definition.Schema.Properties {
		if ontologySQLNameKey(field.Name) == ontologySQLNameKey(p.Name) {
			return p, true
		}
	}
	b.fail("field_unavailable", "字段不存在或当前不可用", field.Range)
	return c.Property{}, false
}

func (b *ontologySQLBinder) value(value ontologySQLValue, property c.Property, allowNull bool) (json.RawMessage, bool) {
	var wire json.RawMessage
	switch value.Kind {
	case "parameter":
		index, known := b.parameterIndex[value.Text]
		if !known {
			if len(b.parameterIndex) >= 64 {
				b.fail("budget_exceeded", "命名参数不能超过 64 个", value.Range)
				return nil, false
			}
			index = len(b.analysis.Parameters)
			b.parameterIndex[value.Text] = index
			_, provided := b.parameters[value.Text]
			b.analysis.Parameters = append(b.analysis.Parameters, c.SQLParameter{Name: value.Text, DataType: property.DataType, Nullable: property.Nullable && allowNull, Precision: property.Precision, Scale: property.Scale, Ranges: []c.SQLRange{}, Provided: provided})
		}
		parameter := &b.analysis.Parameters[index]
		parameter.Ranges = append(parameter.Ranges, value.Range)
		if parameter.DataType != property.DataType || parameter.Precision != property.Precision || parameter.Scale != property.Scale {
			b.fail("parameter_type_conflict", "同名参数用于不同字段类型；请使用不同参数名", value.Range)
			return nil, false
		}
		parameter.Nullable = parameter.Nullable && property.Nullable && allowNull
		var provided bool
		wire, provided = b.parameters[value.Text]
		if !provided {
			b.missing = true
			return nil, false
		}
	case "null":
		wire = json.RawMessage("null")
	case "number":
		if property.DataType != "int64" && property.DataType != "decimal" {
			b.fail("type_mismatch", "数值字面值只能用于 int64 或 decimal 字段", value.Range)
			return nil, false
		}
		wire, _ = json.Marshal(value.Text)
	case "string":
		if property.DataType == "boolean" || property.DataType == "int64" || property.DataType == "decimal" {
			b.fail("type_mismatch", "此字段需要有类型的布尔或数值字面值；参数按协议传递", value.Range)
			return nil, false
		}
		wire, _ = json.Marshal(value.Text)
	case "true", "false":
		if property.DataType != "boolean" {
			b.fail("type_mismatch", "布尔字面值只能用于 boolean 字段", value.Range)
			return nil, false
		}
		wire = json.RawMessage(value.Text)
	default:
		b.fail("invalid_value", "此处需要有类型的字面值或参数", value.Range)
		return nil, false
	}
	// Reusing the Data type checker keeps range/precision/UUID/timestamp limits
	// identical to QuerySpec; no number passes through floating point.
	wire = json.RawMessage(strings.TrimSpace(string(wire)))
	if string(wire) == "null" && !allowNull {
		b.fail("null_comparison", "比较 NULL 请改用 IS NULL 或 IS NOT NULL；标量参数不能替代操作符", value.Range)
		return nil, false
	}
	if _, err := typedValue(property, wire); err != nil {
		message := property.Name + " 的值与 " + property.DataType + " 类型或范围不符"
		if property.DataType == "int64" || property.DataType == "decimal" {
			message += "；数值参数须以十进制字符串传递"
		}
		if property.DataType == "timestamp" {
			message += "；时间须带时区并最多保留微秒"
		}
		b.fail("invalid_value", message, value.Range)
		return nil, false
	}
	return append(json.RawMessage(nil), wire...), true
}

func (b *ontologySQLBinder) expression(expression *ontologySQLExpression, depth int) *c.Expression {
	if expression == nil {
		return nil
	}
	b.nodes++
	if b.nodes > 100 || depth > 10 {
		b.fail("budget_exceeded", "筛选表达式超过节点或深度预算", expression.Range)
		return nil
	}
	e := &c.Expression{Op: expression.Op}
	if e.Op == "and" || e.Op == "or" || e.Op == "not" {
		for _, arg := range expression.Args {
			bound := b.expression(arg, depth+1)
			if bound != nil {
				e.Args = append(e.Args, *bound)
			}
		}
		return e
	}
	property, found := b.field(expression.Field)
	if !found {
		return nil
	}
	e.Field = property.ID
	if e.Op == "isNull" || e.Op == "isNotNull" {
		return e
	}
	if e.Op == "startsWith" && property.DataType != "string" && property.DataType != "text" && property.DataType != "longtext" {
		b.fail("unsupported_operator", "STARTS_WITH 只能用于文本字段", expression.Field.Range)
	}
	if property.DataType == "boolean" && e.Op != "eq" && e.Op != "ne" && e.Op != "in" && e.Op != "notIn" {
		b.fail("unsupported_operator", "boolean 字段不支持大小比较", expression.Field.Range)
	}
	collection := e.Op == "in" || e.Op == "notIn"
	var values []json.RawMessage
	for _, value := range expression.Values {
		wire, ok := b.value(value, property, collection)
		if ok {
			values = append(values, wire)
		}
	}
	if collection {
		e.Value, _ = json.Marshal(values)
	} else if len(values) > 0 {
		e.Value = values[0]
	}
	return e
}

// Definitions passed to the binder contain only currently query-authorized
// fields. Unknown and hidden names deliberately receive one generic diagnostic.
func bindOntologySQL(statement *ontologySQLStatement, definition c.Definition, parameters map[string]json.RawMessage) c.OntologySQLAnalysis {
	b := ontologySQLBinder{statement: statement, definition: definition, parameters: parameters, parameterIndex: map[string]int{}, analysis: c.OntologySQLAnalysis{Diagnostics: []c.SQLDiagnostic{}, Parameters: []c.SQLParameter{}, Fields: []c.Property{}}}
	if statement == nil || definition.Schema == nil {
		b.fail("source_unavailable", "模型不存在或当前不可查询", c.SQLRange{Line: 1, Column: 1})
		return b.analysis
	}
	if len(parameters) > 64 {
		b.fail("budget_exceeded", "参数不能超过 64 个", statement.SourceRange)
	}
	query := c.QuerySpec{From: c.Source{SchemaID: definition.ID}, Select: []string{}, Page: c.Page{Limit: statement.Limit}}
	selected := map[string]bool{}
	for _, field := range statement.Projection {
		property, found := b.field(field)
		if !found {
			continue
		}
		if selected[property.ID] {
			b.fail("duplicate_projection", "投影字段重复，请只选择一次", field.Range)
			continue
		}
		selected[property.ID] = true
		query.Select = append(query.Select, property.ID)
		// Analysis never exposes physical source/column mappings or defaults.
		property.Mapping = nil
		property.Default = nil
		b.analysis.Fields = append(b.analysis.Fields, property)
	}
	query.Where = b.expression(statement.Where, 0)
	if statement.Order != nil {
		property, found := b.field(*statement.Order)
		if found {
			if property.DataType == "text" || property.DataType == "longtext" {
				b.fail("unsupported_sort", "首批不支持 text / longtext 排序，请选择可排序字段", statement.Order.Range)
			}
			query.OrderBy = []c.Order{{Field: property.ID, Direction: statement.Direction}}
		}
	}
	for name := range parameters {
		if _, known := b.parameterIndex[name]; !known {
			b.fail("unused_parameter", "提供了查询未使用的参数："+name, statement.SourceRange)
		}
	}
	b.analysis.Ready = len(b.analysis.Diagnostics) == 0 && !b.missing
	if b.analysis.Ready {
		b.analysis.Query = &query
	}
	return b.analysis
}
