package data

import (
	"encoding/json"
	"net/http"
	"sort"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) savedQueriesCompileHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.savedQueryPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var request c.SavedQueryCompileRequest
	if err := decode(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	template, err := h.compileSavedQuery(r, p, request)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, template)
}

// compileSavedQuery is the one explicit name-binding operation for SQL
// templates. Parameters are identified from the AST, never by looking for
// matching values in the compiled query, so literals cannot become arguments.
func (h *handler) compileSavedQuery(r *http.Request, p c.Principal, request c.SavedQueryCompileRequest) (c.SavedQueryTemplate, error) {
	out := c.SavedQueryTemplate{Parameters: []c.SavedQueryParameter{}}
	statement, diagnostics := parseOntologySQL(request.Text)
	if statement == nil || len(diagnostics) != 0 {
		message := "SQL模板语法检查未通过"
		if len(diagnostics) != 0 {
			message = diagnostics[0].Message
		}
		return out, problem(422, "invalid_saved_query_sql", message)
	}
	if len(statement.Joins) != 0 {
		return out, problem(422, "unsupported_saved_query", "保存查询首批仅支持单模型模板，不支持JOIN LINK")
	}
	current, err := h.resolveOntologySQLSource(r, p, statement.SourceCategory, statement.SourceSchema, request.DefaultCategoryID)
	if err != nil {
		return out, err
	}
	if err := validateSchema(current.Definition); err != nil {
		return out, err
	}
	decision, err := h.authorize(r.Context(), p, "instance.query", current.Definition.ID)
	if err != nil {
		return out, err
	}
	filtered := current.Definition
	schema := *filtered.Schema
	schema.Properties = projection(filtered.Schema, decision.ReadFields)
	filtered.Schema = &schema
	binder := ontologySQLBinder{statement: statement, definition: filtered, analysis: c.OntologySQLAnalysis{Diagnostics: []c.SQLDiagnostic{}}}
	declarations := map[string]c.SavedQueryParameter{}
	samples := map[string]json.RawMessage{}
	var visit func(*ontologySQLExpression) error
	visit = func(expression *ontologySQLExpression) error {
		if expression == nil {
			return nil
		}
		if expression.Op == "and" || expression.Op == "or" || expression.Op == "not" {
			for _, child := range expression.Args {
				if err := visit(child); err != nil {
					return err
				}
			}
			return nil
		}
		property, found := binder.field(expression.Field)
		if !found {
			return problem(422, "saved_query_binding_changed", "SQL模板引用不存在或不可查询的属性")
		}
		for _, value := range expression.Values {
			if value.Kind != "parameter" {
				continue
			}
			if expression.Op == "in" || expression.Op == "notIn" {
				return problem(422, "saved_query_sql_parameter_collection", "SQL模板首批不支持IN列表中的命名参数；请使用标量比较，或在可视化模板中声明整组集合参数")
			}
			declaration := declarationForProperty(value.Text, property, false)
			if existing, known := declarations[value.Text]; known && existing != declaration {
				return problem(422, "saved_query_parameter_type", "同名参数不能用于不同类型或枚举的属性")
			}
			declarations[value.Text] = declaration
			samples[value.Text] = savedSampleValue(property)
		}
		return nil
	}
	if err := visit(statement.Where); err != nil {
		return out, err
	}
	if len(declarations) > c.MaxSavedQueryParameters {
		return out, problem(422, "saved_query_budget", "SQL模板最多64个参数")
	}
	analysis := bindOntologySQL(statement, filtered, samples)
	if !analysis.Ready || analysis.Query == nil || len(analysis.Diagnostics) != 0 {
		message := "SQL模板绑定检查未通过"
		if len(analysis.Diagnostics) != 0 {
			message = analysis.Diagnostics[0].Message
		}
		return out, problem(422, "invalid_saved_query_sql", message)
	}
	query := *analysis.Query
	var placeholders func(*ontologySQLExpression, *c.Expression)
	placeholders = func(expression *ontologySQLExpression, bound *c.Expression) {
		if expression == nil || bound == nil {
			return
		}
		for i, child := range expression.Args {
			placeholders(child, &bound.Args[i])
		}
		if len(expression.Values) == 1 && expression.Values[0].Kind == "parameter" {
			bound.Parameter, bound.Value = expression.Values[0].Text, nil
		}
	}
	placeholders(statement.Where, query.Where)
	for _, declaration := range declarations {
		out.Parameters = append(out.Parameters, declaration)
	}
	sort.Slice(out.Parameters, func(i, j int) bool { return out.Parameters[i].Name < out.Parameters[j].Name })
	input := c.SavedQueryInput{Name: "保存查询模板检查", Source: c.SavedQuerySource{Kind: "ontology-sql", Text: request.Text, DefaultCategoryID: request.DefaultCategoryID}, Query: query, Parameters: out.Parameters}
	input, _, err = h.validateSavedInput(r.Context(), p, input)
	if err != nil {
		return out, err
	}
	out.Query, out.Parameters = input.Query, input.Parameters
	return out, nil
}
