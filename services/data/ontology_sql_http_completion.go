package data

import (
	"net/http"
	"sort"
	"strings"
	"unicode/utf16"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func sqlQuotedIdentifier(text string) string { return `"` + strings.ReplaceAll(text, `"`, `""`) + `"` }

func sqlRuneUnits(r rune) int {
	if r > 0xffff {
		return 2
	}
	return 1
}

// Browser offsets are UTF-16 positions. Reject the middle of a surrogate pair.
func sqlCursorByte(text string, offset int) (int, bool) {
	if offset < 0 {
		return 0, false
	}
	units := 0
	for index, r := range text {
		if units == offset {
			return index, true
		}
		units += sqlRuneUnits(r)
		if units > offset {
			return 0, false
		}
	}
	return len(text), units == offset
}

func (h *handler) completeOntologySQLHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	req, err := decodeOntologySQL(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, ok := sqlCursorByte(req.Text, req.CursorOffset); !ok {
		writeError(w, problem(400, "invalid_cursor_offset", "光标必须为有效 UTF-16 文档位置"))
		return
	}
	items, err := h.completeOntologySQL(r, p, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, c.OntologySQLCompletions{RequestID: req.RequestID, TextHash: ontologySQLTextHash(req.Text), Items: items})
}

func completionTokens(text string) []ontologySQLToken {
	tokens, _ := lexOntologySQL(text)
	out := make([]ontologySQLToken, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind != "comment" && token.Kind != "eof" {
			out = append(out, token)
		}
	}
	return out
}

func (h *handler) completionModel(r *http.Request, p c.Principal, tokens []ontologySQLToken, defaultCategory string) (*c.QueryContextModel, string, error) {
	for i, token := range tokens {
		if token.Kind != "word" || token.Text != "FROM" {
			continue
		}
		if i+1 >= len(tokens) || tokens[i+1].Kind != "identifier" {
			return nil, "", nil
		}
		category, schema, next := "", tokens[i+1].Text, i+2
		if next < len(tokens) && tokens[next].Text == "." {
			if next+1 >= len(tokens) || tokens[next+1].Kind != "identifier" {
				return nil, "", nil
			}
			category, schema, next = schema, tokens[next+1].Text, next+2
		}
		alias := schema
		if next+1 < len(tokens) && tokens[next].Kind == "word" && tokens[next].Text == "AS" && tokens[next+1].Kind == "identifier" {
			alias = tokens[next+1].Text
		}
		current, err := h.resolveOntologySQLSource(r, p, category, schema, defaultCategory)
		if err != nil {
			if hiddenSQLSource(err) {
				return nil, alias, nil
			}
			return nil, alias, err
		}
		decision, err := h.authorize(r.Context(), p, "instance.query", current.Definition.ID)
		if err != nil {
			if hiddenSQLSource(err) {
				return nil, alias, nil
			}
			return nil, alias, err
		}
		d := current.Definition
		if err := validateSchema(d); err != nil {
			return nil, alias, nil
		}
		if err := validateOntologySQLDecision(d, decision); err != nil {
			return nil, alias, err
		}
		model := queryContextModel(d, decision, category)
		return &model, alias, nil
	}
	return nil, "", nil
}

func (h *handler) completeOntologySQL(r *http.Request, p c.Principal, req c.OntologySQLRequest) ([]c.SQLCompletion, error) {
	items := []c.SQLCompletion{}
	tokens := completionTokens(req.Text)
	allTokens, lexDiagnostics := lexOntologySQL(req.Text)
	cursorByte, _ := sqlCursorByte(req.Text, req.CursorOffset)
	before := req.Text[:cursorByte]
	for _, token := range allTokens {
		if (token.Kind == "string" || token.Kind == "comment") && token.Range.From < req.CursorOffset && token.Range.To >= req.CursorOffset {
			return items, nil
		}
	}
	for _, diagnostic := range lexDiagnostics {
		if diagnostic.Range.From >= req.CursorOffset {
			continue
		}
		start, _ := sqlCursorByte(req.Text, diagnostic.Range.From)
		if diagnostic.Code == "incomplete_comment" || diagnostic.Code == "incomplete_quote" && req.Text[start] == '\'' {
			return items, nil
		}
	}
	line, column := 1, 1
	lastCR := false
	for _, r := range before {
		if r == '\r' {
			line, column = line+1, 1
		} else if r == '\n' {
			if !lastCR {
				line++
			}
			column = 1
		} else {
			column += sqlRuneUnits(r)
		}
		lastCR = r == '\r'
	}
	replace := c.SQLRange{From: req.CursorOffset, To: req.CursorOffset, Line: line, Column: column}
	prefix, currentKind := "", ""
	prior := []ontologySQLToken{}
	for _, token := range tokens {
		if token.Range.To < req.CursorOffset || token.Range.To == req.CursorOffset && (token.Kind == "punctuation" || token.Kind == "operator") {
			prior = append(prior, token)
			continue
		}
		if token.Range.From < req.CursorOffset && token.Range.To >= req.CursorOffset && (token.Kind == "identifier" || token.Kind == "word" || token.Kind == "parameter") {
			replace, prefix, currentKind = token.Range, token.Text, token.Kind
			start, _ := sqlCursorByte(req.Text, token.Range.From)
			rawPrefix := before[start:]
			if token.Kind == "identifier" {
				prefix = strings.ReplaceAll(strings.TrimPrefix(rawPrefix, `"`), `""`, `"`)
				if token.Range.To == req.CursorOffset {
					prefix = token.Text
				}
			}
			if token.Kind == "parameter" {
				prefix = strings.TrimPrefix(rawPrefix, ":")
			}
			if token.Kind == "word" {
				prefix = rawPrefix
			}
		}
		break
	}
	// The lexer returns complete preceding tokens on an unclosed identifier.
	// Use the raw suffix only as an edit prefix, never as executable syntax.
	if replace.From == req.CursorOffset {
		lastEnd := 0
		if len(prior) > 0 {
			lastEnd, _ = sqlCursorByte(req.Text, prior[len(prior)-1].Range.To)
		}
		suffix := strings.TrimLeft(before[lastEnd:], " \t\r\n")
		if strings.HasPrefix(suffix, `"`) && !strings.Contains(suffix[1:], `"`) {
			prefix, currentKind = suffix[1:], "identifier"
			replace.From = req.CursorOffset - len(utf16.Encode([]rune(suffix)))
			replace.Column -= req.CursorOffset - replace.From
		}
		if strings.HasPrefix(suffix, ":") && !strings.ContainsAny(suffix, " \t\r\n,()") {
			prefix, currentKind = strings.TrimPrefix(suffix, ":"), "parameter"
			replace.From = req.CursorOffset - len(utf16.Encode([]rune(suffix)))
		}
	}
	add := func(label, insertion, detail, kind string) {
		if len(items) >= 50 {
			return
		}
		if prefix != "" && !strings.Contains(nameKey(label), nameKey(prefix)) && !strings.Contains(nameKey(strings.ReplaceAll(insertion, `"`, "")), nameKey(prefix)) {
			return
		}
		items = append(items, c.SQLCompletion{Label: label, InsertText: insertion, Detail: detail, Kind: kind, Range: replace})
	}
	if currentKind == "parameter" || strings.HasSuffix(before, ":") {
		seen := map[string]bool{}
		for _, token := range tokens {
			if token.Kind == "parameter" {
				seen[token.Text] = true
			}
		}
		for name := range req.Parameters {
			seen[name] = true
		}
		names := make([]string, 0, len(seen))
		for name := range seen {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			add(name, ":"+name, "命名参数", "parameter")
		}
		return items, nil
	}
	clause, fromCount := "SELECT", 0
	for _, token := range prior {
		if token.Kind != "word" {
			continue
		}
		switch token.Text {
		case "SELECT", "FROM", "WHERE", "LIMIT":
			clause = token.Text
		case "ORDER":
			clause = "ORDER BY"
		}
		if token.Text == "FROM" {
			fromCount++
		}
	}
	if clause == "FROM" {
		from := -1
		for i, token := range prior {
			if token.Kind == "word" && token.Text == "FROM" {
				from = i
			}
		}
		endSource := from + 2
		if from >= 0 && from+2 < len(prior) && prior[from+2].Text == "." {
			endSource = from + 4
		}
		if from >= 0 && endSource <= len(prior) && endSource >= 2 && prior[endSource-1].Kind == "identifier" && currentKind != "identifier" {
			words := []string{"AS", "WHERE", "ORDER BY", "LIMIT"}
			if endSource < len(prior) && prior[endSource].Text == "AS" {
				if endSource+1 == len(prior) {
					add(prior[endSource-1].Text, sqlQuotedIdentifier(prior[endSource-1].Text), "模型别名", "alias")
					return items, nil
				}
				words = []string{"WHERE", "ORDER BY", "LIMIT"}
			}
			for _, word := range words {
				add(word, word+" ", "查询子句", "keyword")
			}
			return items, nil
		}
		categoryID := req.DefaultCategoryID
		categoryPrefix := ""
		if len(prior) >= 3 && prior[len(prior)-1].Text == "." && prior[len(prior)-2].Kind == "identifier" {
			categoryPrefix = prior[len(prior)-2].Text
			categoryID = ""
		}
		if categoryPrefix != "" {
			categories, err := h.sqlCategories(r, p)
			if err != nil {
				return nil, err
			}
			for id, name := range categories {
				if nameKey(name) == nameKey(categoryPrefix) {
					categoryID = id
					break
				}
			}
			if categoryID == "" {
				return items, nil
			}
		}
		context, err := h.ontologySQLContext(r, p, categoryID, "", prefix, "", 50)
		if err != nil {
			return nil, err
		}
		for _, model := range context.Models {
			insertion := sqlQuotedIdentifier(model.Category) + "." + sqlQuotedIdentifier(model.Name)
			if categoryPrefix != "" {
				insertion = sqlQuotedIdentifier(model.Name)
			}
			add(model.Name, insertion, model.Category+" · 对象模型", "model")
		}
		return items, nil
	}
	if clause == "ORDER BY" && len(prior) > 0 && prior[len(prior)-1].Kind == "word" && prior[len(prior)-1].Text == "ORDER" {
		add("BY", "BY ", "排序子句", "keyword")
		return items, nil
	}
	model, alias, err := h.completionModel(r, p, tokens, req.DefaultCategoryID)
	if err != nil {
		return nil, err
	}
	qualifier := ""
	if len(prior) >= 2 && prior[len(prior)-1].Text == "." && prior[len(prior)-2].Kind == "identifier" {
		qualifier = prior[len(prior)-2].Text
	}
	rowModels, rowAliases, err := h.completionRowScopes(r, p, req.Text, req.DefaultCategoryID)
	if err != nil {
		return nil, err
	}
	if len(rowModels) > 0 {
		for index, candidate := range rowModels {
			if qualifier != "" && nameKey(qualifier) != nameKey(rowAliases[index]) {
				continue
			}
			if clause != "SELECT" && clause != "WHERE" && clause != "ORDER BY" {
				continue
			}
			for _, field := range candidate.Fields {
				if clause == "WHERE" && !field.Filterable || clause == "ORDER BY" && !field.Sortable {
					continue
				}
				insertion := sqlIdentifierReference(rowAliases[index], field.Name)
				if qualifier != "" {
					insertion = sqlQuotedIdentifier(field.Name)
				}
				add(field.Name, insertion, rowAliases[index]+" · "+field.DataType, "field")
			}
		}
		if qualifier != "" {
			return items, nil
		}
		// Continue keyword completion below without adding the root a second time.
		model = nil
	}
	if qualifier != "" && (model == nil || nameKey(qualifier) != nameKey(alias)) {
		return items, nil
	}
	if model != nil && (clause == "SELECT" || clause == "WHERE" || clause == "ORDER BY") {
		for _, field := range model.Fields {
			if clause == "WHERE" && !field.Filterable || clause == "ORDER BY" && !field.Sortable {
				continue
			}
			insertion := sqlQuotedIdentifier(alias) + "." + sqlQuotedIdentifier(field.Name)
			if qualifier != "" {
				insertion = sqlQuotedIdentifier(field.Name)
			}
			add(field.Name, insertion, field.DataType, "field")
		}
	}
	if qualifier != "" {
		return items, nil
	}
	if len(prior) == 0 && currentKind != "identifier" {
		add("SELECT", "SELECT ", "显式选择业务属性", "keyword")
	}
	if clause == "SELECT" && fromCount == 0 {
		add("FROM", "FROM ", "选择对象模型", "keyword")
	}
	if clause == "WHERE" {
		for _, word := range []string{"AND", "OR", "NOT", "IS NULL", "IS NOT NULL", "IN", "NOT IN", "TRUE", "FALSE"} {
			add(word, word+" ", "条件语法", "keyword")
		}
		add("STARTS_WITH", "STARTS_WITH(", "文本按字面前缀筛选", "function")
		if len(prior) > 0 && prior[len(prior)-1].Kind == "identifier" {
			ops := []string{"=", "<>", "<", "<=", ">", ">="}
			if model != nil {
				for _, field := range model.Fields {
					if nameKey(field.Name) == nameKey(prior[len(prior)-1].Text) && field.DataType == "boolean" {
						ops = []string{"=", "<>"}
					}
				}
			}
			for _, op := range ops {
				add(op, op+" ", "比较操作符", "operator")
			}
		}
	}
	if clause == "ORDER BY" {
		add("ASC", "ASC", "升序", "keyword")
		add("DESC", "DESC", "降序", "keyword")
	}
	if clause == "LIMIT" {
		for _, size := range []string{"25", "50", "100"} {
			add(size, size, "每页条数", "literal")
		}
	}
	return items, nil
}

// Build only a complete FROM/JOIN prefix. An unfinished projection or WHERE
// must not stop qualified field completion, and incomplete joins do not guess.
func (h *handler) completionRowScopes(r *http.Request, p c.Principal, text, categoryID string) ([]c.QueryContextModel, []string, error) {
	tokens := completionTokens(text)
	start, end, hasJoin := -1, len(text), false
	for _, token := range tokens {
		if token.Kind != "word" {
			continue
		}
		if token.Text == "FROM" && start < 0 {
			start, _ = sqlCursorByte(text, token.Range.From)
			continue
		}
		if start < 0 {
			continue
		}
		if token.Text == "JOIN" {
			hasJoin = true
		}
		if token.Text == "WHERE" || token.Text == "ORDER" || token.Text == "LIMIT" {
			end, _ = sqlCursorByte(text, token.Range.From)
			break
		}
	}
	if start < 0 || !hasJoin {
		return nil, nil, nil
	}
	stmt, diagnostics := parseOntologySQL(`SELECT "completion" ` + text[start:end])
	if len(diagnostics) > 0 || stmt == nil {
		return nil, nil, nil
	}
	scope, err := h.rowSQLScope(r, p, stmt, categoryID)
	if err != nil {
		if hiddenSQLSource(err) {
			return nil, nil, nil
		}
		if _, ok := err.(*ontologySQLBindingError); ok {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	models, aliases := []c.QueryContextModel{}, []string{}
	for _, node := range scope.Nodes {
		model := queryContextModel(node.Current.Definition, node.Decision, "")
		fields := []c.QueryContextField{}
		for _, field := range model.Fields {
			property, ok := findProperty(node.Current.Definition.Schema, field.ID)
			if ok && primarySQLProperty(node.Current.Definition, property) {
				fields = append(fields, field)
			}
		}
		model.Fields = fields
		models, aliases = append(models, model), append(aliases, node.Alias)
	}
	return models, aliases, nil
}
