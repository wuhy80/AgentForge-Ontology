package data

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type ontologySQLScopeNode struct {
	ID, Alias string
	Current   c.CurrentDefinition
	Decision  c.Decision
}

type ontologySQLRowScope struct {
	Nodes []ontologySQLScopeNode
	Plan  c.RowQuery
}

func validSQLAlias(alias string) bool {
	if strings.TrimSpace(alias) == "" || utf8.RuneCountInString(alias) > 128 {
		return false
	}
	for _, r := range alias {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

type ontologySQLBindingError struct{ Diagnostic c.SQLDiagnostic }

func (e *ontologySQLBindingError) Error() string { return e.Diagnostic.Message }
func sqlScopeFailure(code, message string, at c.SQLRange) error {
	return &ontologySQLBindingError{Diagnostic: sqlDiagnostic(code, message, at)}
}

func (h *handler) rowSQLScope(r *http.Request, p c.Principal, stmt *ontologySQLStatement, defaultCategoryID string) (*ontologySQLRowScope, error) {
	current, err := h.resolveOntologySQLSource(r, p, stmt.SourceCategory, stmt.SourceSchema, defaultCategoryID)
	if err != nil {
		return nil, err
	}
	decision, err := h.authorize(r.Context(), p, "instance.query", current.Definition.ID)
	if err != nil {
		return nil, err
	}
	if err = validateSchema(current.Definition); err != nil {
		return nil, err
	}
	if err = validateOntologySQLDecision(current.Definition, decision); err != nil {
		return nil, err
	}
	if !validSQLAlias(stmt.Alias) {
		return nil, sqlScopeFailure("invalid_alias", "查询别名需要 1..128 个字符且不能包含控制字符", stmt.SourceRange)
	}
	scope := &ontologySQLRowScope{Nodes: []ontologySQLScopeNode{{ID: "n0", Alias: stmt.Alias, Current: current, Decision: decision}}, Plan: c.RowQuery{Nodes: []c.RowQueryNode{{ID: "n0", Alias: stmt.Alias, SchemaID: current.Definition.ID}}, Joins: []c.RowQueryJoin{}}}
	if len(stmt.Joins) == 0 {
		if err := h.recheckEnums(r.Context(), current); err != nil {
			return nil, err
		}
		return scope, nil
	}
	links, err := h.sqlAuthorizedLinks(r, p)
	if err != nil {
		return nil, err
	}
	for index, join := range stmt.Joins {
		var origin *ontologySQLScopeNode
		for i := range scope.Nodes {
			if nameKey(scope.Nodes[i].Alias) == nameKey(join.Alias) {
				return nil, sqlScopeFailure("duplicate_alias", "对象别名重复，请为每次对象出现指定不同别名", join.Range)
			}
			if nameKey(scope.Nodes[i].Alias) == nameKey(join.FromAlias) {
				origin = &scope.Nodes[i]
			}
		}
		if origin == nil {
			return nil, sqlScopeFailure("unknown_qualifier", "JOIN LINK 的起点必须是已经声明的对象别名", join.Range)
		}
		if !validSQLAlias(join.Alias) {
			return nil, sqlScopeFailure("invalid_alias", "查询别名需要 1..128 个字符且不能包含控制字符", join.Range)
		}
		type match struct {
			link              sqlAuthorizedLink
			direction, target string
		}
		matches := []match{}
		for _, candidate := range links {
			link := candidate.Current.Definition
			if join.Name != "" {
				if nameKey(link.Name) != nameKey(join.Name) {
					continue
				}
				if join.Category != "" && nameKey(candidate.Category) != nameKey(join.Category) {
					continue
				}
				if join.Category == "" && link.CategoryID != current.Definition.CategoryID {
					continue
				}
			}
			for _, direction := range []string{"out", "in"} {
				from, to, entry := link.Link.SourceSchemaID, link.Link.TargetSchemaID, link.Link.ForwardName
				if direction == "in" {
					from, to, entry = to, from, link.Link.ReverseName
				}
				if from != origin.Current.Definition.ID || join.Direction != "" && join.Direction != direction {
					continue
				}
				if join.Entry != "" && nameKey(entry) != nameKey(join.Entry) {
					continue
				}
				matches = append(matches, match{candidate, direction, to})
			}
		}
		if len(matches) == 0 {
			return nil, sqlScopeFailure("link_unavailable", "关系不存在、当前不可查询或映射能力不支持；请从可用关联路径中选择", join.Range)
		}
		if len(matches) > 1 {
			return nil, sqlScopeFailure("ambiguous_link", "关系入口不唯一，请使用 VIA 分类.Link主名 DIRECTION OUT/IN 明确关系及方向", join.Range)
		}
		selected := matches[0]
		target, err := h.current(r.Context(), p.TenantID, selected.target)
		if err != nil {
			return nil, err
		}
		definitionDecision, err := h.authorize(r.Context(), p, "definition.read", target.Definition.ID)
		if err != nil {
			return nil, err
		}
		if definitionDecision.RowFilter != nil || definitionDecision.NewRowFilter != nil {
			return nil, problem(422, "unsupported_authorization", "定义目录不支持带行条件的授权")
		}
		targetDecision, err := h.authorize(r.Context(), p, "instance.query", target.Definition.ID)
		if err != nil {
			return nil, err
		}
		if err = validateSchema(target.Definition); err != nil {
			return nil, err
		}
		if err = validateOntologySQLDecision(target.Definition, targetDecision); err != nil {
			return nil, err
		}
		id := fmt.Sprintf("n%d", index+1)
		scope.Plan.Nodes = append(scope.Plan.Nodes, c.RowQueryNode{ID: id, Alias: join.Alias, SchemaID: target.Definition.ID})
		scope.Plan.Joins = append(scope.Plan.Joins, c.RowQueryJoin{From: origin.ID, To: id, LinkID: selected.link.Current.Definition.ID, Direction: selected.direction})
		scope.Nodes = append(scope.Nodes, ontologySQLScopeNode{ID: id, Alias: join.Alias, Current: target, Decision: targetDecision})
	}
	for _, node := range scope.Nodes {
		if err := h.recheckEnums(r.Context(), node.Current); err != nil {
			return nil, err
		}
	}
	return scope, nil
}

func (b *ontologySQLBinder) rowField(field ontologySQLField) (c.Property, bool) {
	var selected *c.Property
	for _, node := range b.rowScope.Nodes {
		if field.Qualifier != "" && nameKey(field.Qualifier) != nameKey(node.Alias) {
			continue
		}
		for _, property := range node.Current.Definition.Schema.Properties {
			if nameKey(property.Name) != nameKey(field.Name) || !fieldAllowed(node.Decision.ReadFields, property.ID) {
				continue
			}
			if selected != nil {
				b.fail("ambiguous_field", "字段在多个对象别名中存在，请用对象别名限定", field.Range)
				return c.Property{}, false
			}
			if !primarySQLProperty(node.Current.Definition, property) {
				b.fail("row_projection_unsupported", "关系行查询当前仅支持各对象主表属性；其他来源属性请使用单对象查询", field.Range)
				return c.Property{}, false
			}
			bound := property
			bound.ID, bound.Name = node.ID+"."+property.ID, node.Alias+"."+property.Name
			selected = &bound
		}
	}
	if selected == nil {
		b.fail("field_unavailable", "字段或对象别名不存在或当前不可用", field.Range)
		return c.Property{}, false
	}
	return *selected, true
}

func bindRowOntologySQL(stmt *ontologySQLStatement, scope *ontologySQLRowScope, parameters map[string]json.RawMessage) c.OntologySQLAnalysis {
	b := ontologySQLBinder{statement: stmt, rowScope: scope, parameters: parameters, parameterIndex: map[string]int{}, analysis: c.OntologySQLAnalysis{Diagnostics: []c.SQLDiagnostic{}, Parameters: []c.SQLParameter{}, Fields: []c.Property{}}}
	q := c.QuerySpec{From: c.Source{SchemaID: scope.Nodes[0].Current.Definition.ID}, Rows: &scope.Plan, Select: []string{}, Page: c.Page{Limit: stmt.Limit}}
	selected := map[string]bool{}
	for _, field := range stmt.Projection {
		property, found := b.field(field)
		if !found {
			continue
		}
		if selected[property.ID] {
			b.fail("duplicate_projection", "投影字段重复，请只选择一次", field.Range)
			continue
		}
		selected[property.ID] = true
		q.Select = append(q.Select, property.ID)
		property.Mapping, property.Default = nil, nil
		b.analysis.Fields = append(b.analysis.Fields, property)
	}
	q.Where = b.expression(stmt.Where, 0)
	if stmt.Order != nil {
		property, found := b.field(*stmt.Order)
		if found {
			if property.DataType == "text" || property.DataType == "longtext" {
				b.fail("unsupported_sort", "不支持 text / longtext 排序，请选择可排序字段", stmt.Order.Range)
			}
			q.OrderBy = []c.Order{{Field: property.ID, Direction: stmt.Direction}}
		}
	}
	for name := range parameters {
		if _, ok := b.parameterIndex[name]; !ok {
			b.fail("unused_parameter", "提供了查询未使用的参数："+name, stmt.SourceRange)
		}
	}
	b.analysis.Ready = len(b.analysis.Diagnostics) == 0 && !b.missing
	if b.analysis.Ready {
		b.analysis.Query = &q
	}
	return b.analysis
}

func (h *handler) analyzeRowOntologySQL(r *http.Request, p c.Principal, req c.OntologySQLRequest, stmt *ontologySQLStatement) (c.OntologySQLAnalysis, error) {
	out := sqlAnalysis(req, nil)
	scope, err := h.rowSQLScope(r, p, stmt, req.DefaultCategoryID)
	if err != nil {
		var binding *ontologySQLBindingError
		var public *apiError
		if errors.As(err, &binding) {
			out.Diagnostics = append(out.Diagnostics, binding.Diagnostic)
			return out, nil
		}
		if hiddenSQLSource(err) {
			out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("source_unavailable", "查询对象或关系不存在或当前不可用", stmt.SourceRange))
			return out, nil
		}
		if errors.As(err, &public) && public.status == 422 {
			out.Diagnostics = append(out.Diagnostics, sqlDiagnostic(public.code, public.message, stmt.SourceRange))
			return out, nil
		}
		return out, err
	}
	out = bindRowOntologySQL(stmt, scope, req.Parameters)
	out.RequestID, out.TextHash = req.RequestID, ontologySQLTextHash(req.Text)
	out.Fields = safeQueryProperties(out.Fields)
	out.Scope = &scope.Plan
	return out, nil
}

func sqlIdentifierReference(alias, name string) string {
	return sqlQuotedIdentifier(alias) + "." + sqlQuotedIdentifier(name)
}

// The insertion endpoint only edits the explicit SELECT list. Source and Link
// scopes are fully bound first; supplied parameters remain separate and intact.
func (h *handler) insertOntologySQLFieldHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req c.QueryFieldInsertionRequest
	if err = decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if !utf8.ValidString(req.Text) || len(req.Text) > 64<<10 || len(req.Parameters) > 64 || len(req.RequestID) > 128 || len(req.DefaultCategoryID) > 128 || utf8.RuneCountInString(req.Alias) > 128 {
		writeError(w, problem(422, "ontology_sql_budget", "查询文本、参数或别名超过预算"))
		return
	}
	out := c.QueryFieldInsertionResult{RequestID: req.RequestID, TextHash: ontologySQLTextHash(req.Text), Text: req.Text, Diagnostics: []c.SQLDiagnostic{}}
	stmt, diagnostics := parseOntologySQL(req.Text)
	if len(diagnostics) > 0 {
		out.Diagnostics = diagnostics
		writeJSON(w, 200, out)
		return
	}
	scope, err := h.rowSQLScope(r, p, stmt, req.DefaultCategoryID)
	if err != nil {
		var binding *ontologySQLBindingError
		if errors.As(err, &binding) {
			out.Diagnostics = append(out.Diagnostics, binding.Diagnostic)
			writeJSON(w, 200, out)
			return
		}
		if hiddenSQLSource(err) {
			out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("source_unavailable", "查询对象或关系不存在或当前不可用", stmt.SourceRange))
			writeJSON(w, 200, out)
			return
		}
		writeError(w, err)
		return
	}
	analysis := bindRowOntologySQL(stmt, scope, req.Parameters)
	if len(analysis.Diagnostics) > 0 {
		out.Diagnostics = analysis.Diagnostics
		writeJSON(w, 200, out)
		return
	}
	var selected *ontologySQLScopeNode
	for i := range scope.Nodes {
		node := &scope.Nodes[i]
		if node.Current.Definition.ID != req.SchemaID || req.Alias != "" && nameKey(node.Alias) != nameKey(req.Alias) {
			continue
		}
		if selected != nil {
			out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("ambiguous_alias", "此模型在查询中出现多次，请先选择要插入的对象别名", stmt.SourceRange))
			writeJSON(w, 200, out)
			return
		}
		selected = node
	}
	if selected == nil {
		out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("source_unavailable", "此模型未加入当前查询，请先选择关联路径", stmt.SourceRange))
		writeJSON(w, 200, out)
		return
	}
	var property *c.Property
	for _, field := range selected.Current.Definition.Schema.Properties {
		if field.ID == req.FieldID && fieldAllowed(selected.Decision.ReadFields, field.ID) {
			copy := field
			property = &copy
			break
		}
	}
	if property == nil {
		out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("field_unavailable", "字段不存在或当前不可用", stmt.SourceRange))
		writeJSON(w, 200, out)
		return
	}
	if !primarySQLProperty(selected.Current.Definition, *property) {
		out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("row_projection_unsupported", "关系行查询当前仅支持各对象主表属性", stmt.SourceRange))
		writeJSON(w, 200, out)
		return
	}
	from, validFrom := sqlCursorByte(req.Text, req.SelectionFrom)
	to, validTo := sqlCursorByte(req.Text, req.SelectionTo)
	if !validFrom || !validTo || from > to {
		writeError(w, problem(400, "invalid_cursor_offset", "选区需要有效的 UTF-16 范围"))
		return
	}
	for _, token := range stmt.Tokens {
		if token.Kind != "comment" && token.Kind != "string" && token.Kind != "parameter" && token.Kind != "number" {
			continue
		}
		inside := req.SelectionFrom == req.SelectionTo && req.SelectionFrom > token.Range.From && (req.SelectionFrom < token.Range.To || token.Kind == "comment" && strings.HasPrefix(token.Raw, "--") && req.SelectionFrom == token.Range.To)
		if inside || req.SelectionFrom < token.Range.To && req.SelectionTo > token.Range.From {
			out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("unsafe_edit_position", "请先将光标移出字面值、参数和注释，原稿已保留", token.Range))
			writeJSON(w, 200, out)
			return
		}
	}
	reference := sqlIdentifierReference(selected.Alias, property.Name)
	for _, field := range stmt.Projection {
		if nameKey(field.Name) != nameKey(property.Name) {
			continue
		}
		if field.Qualifier == "" {
			bound := ontologySQLBinder{rowScope: scope, analysis: c.OntologySQLAnalysis{Diagnostics: []c.SQLDiagnostic{}}}
			matched, ok := bound.rowField(field)
			if !ok || matched.ID != selected.ID+"."+property.ID {
				continue
			}
		} else if nameKey(field.Qualifier) != nameKey(selected.Alias) {
			continue
		}
		out.From, out.To = field.Range.From, field.Range.To
		writeJSON(w, 200, out)
		return
	}
	last := stmt.Projection[len(stmt.Projection)-1]
	end, _ := sqlCursorByte(req.Text, last.Range.To)
	out.Text = req.Text[:end] + ", " + reference + req.Text[end:]
	out.From, out.To = last.Range.To+2, last.Range.To+2+lenUTF16(reference)
	writeJSON(w, 200, out)
}

func lenUTF16(text string) int {
	total := 0
	for _, r := range text {
		total += sqlRuneUnits(r)
	}
	return total
}

// qualifyRowSQLReferences freezes existing field scopes before a Link adds new
// names to the statement. Only unqualified field token ranges are prefixed;
// parameters, literals, comments, whitespace and quoted spelling stay intact.
func qualifyRowSQLReferences(text string, stmt *ontologySQLStatement, scope *ontologySQLRowScope) (string, error) {
	if stmt == nil || scope == nil || len(scope.Nodes) == 0 {
		return text, problem(422, "source_unavailable", "查询对象尚未绑定，原稿已保留")
	}
	type referenceEdit struct {
		from, to int
		value    string
	}
	edits := []referenceEdit{}
	seen := map[c.SQLRange]bool{}
	binder := ontologySQLBinder{rowScope: scope, analysis: c.OntologySQLAnalysis{Diagnostics: []c.SQLDiagnostic{}}}
	var failure error
	visit := func(field ontologySQLField) {
		if failure != nil || field.Name == "" || seen[field.Range] {
			return
		}
		seen[field.Range] = true
		property, ok := binder.rowField(field)
		if !ok {
			failure = &ontologySQLBindingError{Diagnostic: binder.analysis.Diagnostics[len(binder.analysis.Diagnostics)-1]}
			return
		}
		if field.Qualifier != "" {
			return
		}
		nodeID, _, found := strings.Cut(property.ID, ".")
		alias := ""
		for _, node := range scope.Nodes {
			if node.ID == nodeID {
				alias = node.Alias
				break
			}
		}
		from, validFrom := sqlCursorByte(text, field.Range.From)
		to, validTo := sqlCursorByte(text, field.Range.To)
		if !found || alias == "" || !validFrom || !validTo || from >= to {
			failure = sqlScopeFailure("invalid_source_range", "字段位置与查询原稿不一致，原稿已保留", field.Range)
			return
		}
		edits = append(edits, referenceEdit{from: from, to: to, value: sqlQuotedIdentifier(alias) + "." + text[from:to]})
	}
	for _, field := range stmt.Projection {
		visit(field)
	}
	var expression func(*ontologySQLExpression)
	expression = func(node *ontologySQLExpression) {
		if node == nil || failure != nil {
			return
		}
		visit(node.Field)
		for _, child := range node.Args {
			expression(child)
		}
	}
	expression(stmt.Where)
	if stmt.Order != nil {
		visit(*stmt.Order)
	}
	if failure != nil {
		return text, failure
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].from > edits[j].from })
	end := len(text)
	out := text
	for _, edit := range edits {
		if edit.to > end {
			return text, problem(422, "invalid_source_range", "字段位置重叠，原稿已保留")
		}
		out = out[:edit.from] + edit.value + out[edit.to:]
		end = edit.from
	}
	return out, nil
}
