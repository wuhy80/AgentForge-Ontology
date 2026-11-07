package data

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const maxOntologySQLBytes = 64 << 10
const maxOntologySQLTokens = 8192

type ontologySQLToken struct {
	Kind, Text, Raw string
	Range           c.SQLRange
}

func ontologySQLNameKey(name string) string {
	return cases.Fold().String(norm.NFC.String(strings.TrimSpace(name)))
}

func ontologySQLDiagnostic(code, message string, at c.SQLRange) c.SQLDiagnostic {
	return c.SQLDiagnostic{Code: code, Message: message, Severity: "error", Range: at}
}

// The same lexer is used by the strict parser, formatter, and tolerant editor
// completion. Text in strings is never normalized or interpreted as SQL.
func lexOntologySQL(text string) ([]ontologySQLToken, []c.SQLDiagnostic) {
	var out []ontologySQLToken
	i, offset, line, column := 0, 0, 1, 1
	lastCR := false
	advance := func() rune {
		r, size := utf8.DecodeRuneInString(text[i:])
		i += size
		units := 1
		if r > 0xffff {
			units = 2
		}
		offset += units
		if r == '\r' {
			line++
			column = 1
		} else if r == '\n' {
			if !lastCR {
				line++
			}
			column = 1
		} else {
			column += units
		}
		lastCR = r == '\r'
		return r
	}
	bad := func(code, message string, at c.SQLRange) ([]ontologySQLToken, []c.SQLDiagnostic) {
		at.To = offset
		return out, []c.SQLDiagnostic{ontologySQLDiagnostic(code, message, at)}
	}
	if len(text) > maxOntologySQLBytes {
		return bad("budget_exceeded", "OntologySQL 文本不能超过 64 KiB", c.SQLRange{Line: 1, Column: 1})
	}
	if !utf8.ValidString(text) {
		return bad("invalid_text", "查询文本必须为合法 UTF-8", c.SQLRange{Line: 1, Column: 1})
	}
	for i < len(text) {
		r, _ := utf8.DecodeRuneInString(text[i:])
		if unicode.IsSpace(r) {
			advance()
			continue
		}
		start := i
		at := c.SQLRange{From: offset, Line: line, Column: column}
		token := ontologySQLToken{Range: at}
		switch {
		case strings.HasPrefix(text[i:], "--"):
			token.Kind = "comment"
			for i < len(text) && text[i] != '\r' && text[i] != '\n' {
				advance()
			}
		case strings.HasPrefix(text[i:], "/*"):
			token.Kind = "comment"
			advance()
			advance()
			for i < len(text) && !strings.HasPrefix(text[i:], "*/") {
				if strings.HasPrefix(text[i:], "/*") {
					return bad("nested_comment", "首批不支持嵌套块注释", at)
				}
				advance()
			}
			if i == len(text) {
				return bad("incomplete_comment", "块注释尚未结束，请补充 */", at)
			}
			advance()
			advance()
		case r == '"' || r == '\'':
			quote := r
			advance()
			if quote == '"' {
				token.Kind = "identifier"
			} else {
				token.Kind = "string"
			}
			var value strings.Builder
			closed := false
			for i < len(text) {
				ch := advance()
				if ch == quote {
					if i < len(text) && rune(text[i]) == quote {
						advance()
						value.WriteRune(quote)
					} else {
						closed = true
						break
					}
				} else {
					value.WriteRune(ch)
				}
			}
			if !closed {
				return bad("incomplete_quote", "引号尚未闭合；标识符用双引号，字符串用单引号", at)
			}
			token.Text = value.String()
			if token.Kind == "identifier" {
				token.Text = norm.NFC.String(strings.TrimSpace(token.Text))
				if token.Text == "" || utf8.RuneCountInString(token.Text) > 128 || strings.IndexFunc(token.Text, unicode.IsControl) >= 0 {
					return bad("invalid_identifier", "业务名称须为 1..128 个字符且不含控制字符", at)
				}
			}
		case r == ':':
			token.Kind = "parameter"
			advance()
			nameStart := i
			for i < len(text) {
				ch, _ := utf8.DecodeRuneInString(text[i:])
				if !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && ch != '_' && !unicode.IsMark(ch) {
					break
				}
				advance()
			}
			if i == nameStart {
				return bad("invalid_parameter", "冒号后需要参数名，例如 :标题前缀", at)
			}
			token.Text = text[nameStart:i]
			if utf8.RuneCountInString(token.Text) > 128 {
				return bad("invalid_parameter", "参数名不能超过 128 个字符", at)
			}
		case (r >= '0' && r <= '9') || r == '-':
			token.Kind = "number"
			if r == '-' {
				advance()
			}
			digits := i
			for i < len(text) && text[i] >= '0' && text[i] <= '9' {
				advance()
			}
			if i == digits {
				return bad("invalid_number", "负号后需要十进制数字", at)
			}
			if i < len(text) && text[i] == '.' {
				advance()
				fraction := i
				for i < len(text) && text[i] >= '0' && text[i] <= '9' {
					advance()
				}
				if i == fraction {
					return bad("invalid_number", "小数点后需要数字", at)
				}
			}
			token.Text = text[start:i]
		case unicode.IsLetter(r) || r == '_':
			token.Kind = "word"
			for i < len(text) {
				ch, _ := utf8.DecodeRuneInString(text[i:])
				if !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && ch != '_' {
					break
				}
				advance()
			}
			token.Text = strings.ToUpper(text[start:i])
		case strings.ContainsRune("=<>!", r):
			token.Kind = "operator"
			advance()
			if i < len(text) && ((r == '<' && text[i] == '>') || text[i] == '=') {
				advance()
			}
			token.Text = text[start:i]
			if token.Text == "!" || token.Text == "==" {
				return bad("invalid_operator", "使用 =、<>、!=、<、<=、> 或 >= 比较", at)
			}
		case strings.ContainsRune("(),.;*", r):
			token.Kind = "punctuation"
			advance()
			token.Text = string(r)
		default:
			advance()
			return bad("invalid_token", "不支持的字符；业务名称需要使用双引号", at)
		}
		token.Raw = text[start:i]
		token.Range.To = offset
		out = append(out, token)
		if len(out) > maxOntologySQLTokens {
			return bad("budget_exceeded", "查询 token 过多，请简化文本", at)
		}
	}
	out = append(out, ontologySQLToken{Kind: "eof", Range: c.SQLRange{From: offset, To: offset, Line: line, Column: column}})
	return out, nil
}

type ontologySQLField struct {
	Qualifier, Name string
	Range           c.SQLRange
}
type ontologySQLValue struct {
	Kind, Text string
	Range      c.SQLRange
}
type ontologySQLExpression struct {
	Op     string
	Field  ontologySQLField
	Values []ontologySQLValue
	Args   []*ontologySQLExpression
	Range  c.SQLRange
}
type ontologySQLStatement struct {
	SourceCategory, SourceSchema, Alias string
	SourceRange                         c.SQLRange
	Joins                               []ontologySQLJoin
	Projection                          []ontologySQLField
	Where                               *ontologySQLExpression
	Order                               *ontologySQLField
	Direction                           string
	Limit                               int
	Tokens                              []ontologySQLToken
}
type ontologySQLJoin struct {
	FromAlias, Entry, Category, Name, Direction, Alias string
	Range                                              c.SQLRange
}
type ontologySQLParser struct {
	tokens       []ontologySQLToken
	index, nodes int
	diagnostics  []c.SQLDiagnostic
}

func (p *ontologySQLParser) peek() ontologySQLToken { return p.tokens[p.index] }
func (p *ontologySQLParser) take() ontologySQLToken {
	t := p.peek()
	if t.Kind != "eof" {
		p.index++
	}
	return t
}
func (p *ontologySQLParser) accept(text string) bool {
	t := p.peek()
	if (t.Kind == "word" || t.Kind == "punctuation" || t.Kind == "operator") && t.Text == text {
		p.take()
		return true
	}
	return false
}
func (p *ontologySQLParser) fail(code, message string, at c.SQLRange) {
	if len(p.diagnostics) == 0 {
		p.diagnostics = append(p.diagnostics, ontologySQLDiagnostic(code, message, at))
	}
}
func (p *ontologySQLParser) expect(text string) bool {
	if p.accept(text) {
		return true
	}
	p.fail("syntax_error", "此处需要 "+text, p.peek().Range)
	return false
}
func (p *ontologySQLParser) identifier() ontologySQLToken {
	t := p.take()
	if t.Kind != "identifier" {
		p.fail("expected_identifier", "业务名称需要使用双引号，例如 \"标题\"", t.Range)
	}
	return t
}
func (p *ontologySQLParser) field() ontologySQLField {
	t := p.identifier()
	f := ontologySQLField{Name: t.Text, Range: t.Range}
	if p.accept(".") {
		next := p.identifier()
		f.Qualifier, f.Name, f.Range.To = f.Name, next.Text, next.Range.To
	}
	return f
}
func (p *ontologySQLParser) value() ontologySQLValue {
	t := p.take()
	if t.Kind == "string" || t.Kind == "number" || t.Kind == "parameter" {
		return ontologySQLValue{Kind: t.Kind, Text: t.Text, Range: t.Range}
	}
	if t.Kind == "word" && (t.Text == "TRUE" || t.Text == "FALSE" || t.Text == "NULL") {
		return ontologySQLValue{Kind: strings.ToLower(t.Text), Text: strings.ToLower(t.Text), Range: t.Range}
	}
	p.fail("expected_value", "此处需要字面值或 :命名参数；首批不支持字段间比较或子查询", t.Range)
	return ontologySQLValue{Range: t.Range}
}
func (p *ontologySQLParser) add(e *ontologySQLExpression) *ontologySQLExpression {
	p.nodes++
	if p.nodes > 100 {
		p.fail("budget_exceeded", "筛选表达式不能超过 100 个节点", e.Range)
	}
	return e
}
func (p *ontologySQLParser) expr(depth int) *ontologySQLExpression { return p.or(depth) }
func (p *ontologySQLParser) or(depth int) *ontologySQLExpression {
	first := p.and(depth)
	if !p.accept("OR") {
		return first
	}
	args := []*ontologySQLExpression{first, p.and(depth + 1)}
	for p.accept("OR") && len(p.diagnostics) == 0 {
		args = append(args, p.and(depth+1))
	}
	return p.add(&ontologySQLExpression{Op: "or", Args: args, Range: first.Range})
}
func (p *ontologySQLParser) and(depth int) *ontologySQLExpression {
	first := p.not(depth)
	if !p.accept("AND") {
		return first
	}
	args := []*ontologySQLExpression{first, p.not(depth + 1)}
	for p.accept("AND") && len(p.diagnostics) == 0 {
		args = append(args, p.not(depth+1))
	}
	return p.add(&ontologySQLExpression{Op: "and", Args: args, Range: first.Range})
}
func (p *ontologySQLParser) not(depth int) *ontologySQLExpression {
	at := p.peek().Range
	if depth > 10 {
		p.fail("budget_exceeded", "筛选表达式嵌套不能超过 10 层", at)
		return &ontologySQLExpression{Range: at}
	}
	if p.accept("NOT") {
		return p.add(&ontologySQLExpression{Op: "not", Args: []*ontologySQLExpression{p.not(depth + 1)}, Range: at})
	}
	if p.accept("(") {
		e := p.expr(depth + 1)
		p.expect(")")
		return e
	}
	return p.predicate()
}
func (p *ontologySQLParser) predicate() *ontologySQLExpression {
	at := p.peek().Range
	if p.accept("STARTS_WITH") {
		p.expect("(")
		f := p.field()
		p.expect(",")
		v := p.value()
		p.expect(")")
		return p.add(&ontologySQLExpression{Op: "startsWith", Field: f, Values: []ontologySQLValue{v}, Range: at})
	}
	f := p.field()
	e := &ontologySQLExpression{Field: f, Range: at}
	if p.accept("IS") {
		e.Op = "isNull"
		if p.accept("NOT") {
			e.Op = "isNotNull"
		}
		p.expect("NULL")
		return p.add(e)
	}
	negated := p.accept("NOT")
	if p.accept("IN") {
		e.Op = "in"
		if negated {
			e.Op = "notIn"
		}
		p.expect("(")
		if p.peek().Text == ")" {
			p.fail("empty_collection", "IN 集合不能为空", p.peek().Range)
		} else {
			e.Values = append(e.Values, p.value())
			for p.accept(",") && len(p.diagnostics) == 0 {
				e.Values = append(e.Values, p.value())
				if len(e.Values) > 100 {
					p.fail("budget_exceeded", "IN 集合不能超过 100 项", p.peek().Range)
				}
			}
		}
		p.expect(")")
		return p.add(e)
	}
	if negated {
		p.fail("syntax_error", "字段后的 NOT 仅可用于 NOT IN；否定整个条件请用 NOT (条件)", p.peek().Range)
		return p.add(e)
	}
	op := p.take()
	e.Op = map[string]string{"=": "eq", "<>": "ne", "!=": "ne", "<": "lt", "<=": "lte", ">": "gt", ">=": "gte"}[op.Text]
	if op.Kind != "operator" || e.Op == "" {
		p.fail("expected_operator", "此处需要比较操作符、IS NULL 或 IN", op.Range)
	}
	v := p.value()
	e.Values = []ontologySQLValue{v}
	if v.Kind == "null" {
		p.fail("null_comparison", "比较 NULL 请使用 IS NULL 或 IS NOT NULL", v.Range)
	}
	return p.add(e)
}

func parseOntologySQL(text string) (*ontologySQLStatement, []c.SQLDiagnostic) {
	tokens, diagnostics := lexOntologySQL(text)
	if len(diagnostics) > 0 {
		return nil, diagnostics
	}
	p := ontologySQLParser{}
	for _, t := range tokens {
		if t.Kind != "comment" {
			p.tokens = append(p.tokens, t)
		}
	}
	s := &ontologySQLStatement{Limit: 25, Direction: "asc", Tokens: tokens}
	if !p.expect("SELECT") {
		return nil, p.diagnostics
	}
	if p.peek().Text == "*" {
		p.fail("unsupported_projection", "请显式选择字段；首批不支持 SELECT *", p.peek().Range)
	}
	s.Projection = append(s.Projection, p.field())
	for p.accept(",") && len(p.diagnostics) == 0 {
		s.Projection = append(s.Projection, p.field())
		if len(s.Projection) > 100 {
			p.fail("budget_exceeded", "投影不能超过 100 个字段", p.peek().Range)
		}
	}
	p.expect("FROM")
	source := p.identifier()
	s.SourceSchema, s.SourceRange = source.Text, source.Range
	if p.accept(".") {
		model := p.identifier()
		s.SourceCategory, s.SourceSchema, s.SourceRange.To = source.Text, model.Text, model.Range.To
	}
	s.Alias = s.SourceSchema
	if p.accept("AS") {
		s.Alias = p.identifier().Text
	}
	for p.accept("JOIN") && len(p.diagnostics) == 0 {
		join := ontologySQLJoin{Range: p.tokens[p.index-1].Range}
		p.expect("LINK")
		join.FromAlias = p.identifier().Text
		if p.accept(".") {
			join.Entry = p.identifier().Text
		} else {
			p.expect("VIA")
			name := p.identifier()
			join.Name = name.Text
			if p.accept(".") {
				join.Category, join.Name = join.Name, p.identifier().Text
			}
			p.expect("DIRECTION")
			if p.accept("OUT") {
				join.Direction = "out"
			} else if p.accept("IN") {
				join.Direction = "in"
			} else {
				p.fail("invalid_link_direction", "DIRECTION 需要 OUT 或 IN", p.peek().Range)
			}
		}
		p.expect("AS")
		alias := p.identifier()
		join.Alias, join.Range.To = alias.Text, alias.Range.To
		s.Joins = append(s.Joins, join)
		if len(s.Joins) > 4 {
			p.fail("budget_exceeded", "关系行查询最多支持 4 个 Link", join.Range)
		}
	}
	if p.accept("WHERE") {
		s.Where = p.expr(0)
	}
	if p.accept("ORDER") {
		p.expect("BY")
		f := p.field()
		s.Order = &f
		if p.accept("DESC") {
			s.Direction = "desc"
		} else {
			p.accept("ASC")
		}
	}
	if p.accept("LIMIT") {
		t := p.take()
		n, err := strconv.Atoi(t.Text)
		if t.Kind != "number" || err != nil || n < 1 || n > 100 || strconv.Itoa(n) != t.Text {
			p.fail("invalid_limit", "LIMIT 需要 1..100 的整数字面值", t.Range)
		} else {
			s.Limit = n
		}
	}
	p.accept(";")
	if p.peek().Kind != "eof" {
		p.fail("unsupported_syntax", "仅支持单条 SELECT 与 INNER JOIN LINK；不支持任意键 JOIN、聚合、列别名或 OFFSET", p.peek().Range)
	}
	if len(p.diagnostics) > 0 {
		return nil, p.diagnostics
	}
	return s, nil
}

// Formatting only touches whitespace and keyword spelling after the entire
// statement parses. Keeping token Raw preserves identifiers, literals/comments.
func formatOntologySQL(text string) (string, []c.SQLDiagnostic) {
	statement, diagnostics := parseOntologySQL(text)
	if len(diagnostics) > 0 {
		return text, diagnostics
	}
	var out strings.Builder
	previous := ""
	lineStart := true
	writeSpace := func() {
		if !lineStart && previous != " " {
			out.WriteByte(' ')
			previous = " "
		}
	}
	newline := func() {
		if !lineStart {
			out.WriteByte('\n')
			lineStart = true
		}
		previous = ""
	}
	for _, t := range statement.Tokens {
		if t.Kind == "eof" {
			break
		}
		if t.Kind == "comment" {
			writeSpace()
			out.WriteString(t.Raw)
			lineStart = false
			previous = "comment"
			if strings.HasPrefix(t.Raw, "--") {
				newline()
			}
			continue
		}
		if t.Kind == "word" && (t.Text == "FROM" || t.Text == "JOIN" || t.Text == "WHERE" || t.Text == "ORDER" || t.Text == "LIMIT") {
			newline()
		}
		value := t.Raw
		if t.Kind == "word" {
			value = t.Text
		}
		switch t.Text {
		case ".", ",", ")", ";":
			out.WriteString(value)
		case "(":
			if previous != "STARTS_WITH" {
				writeSpace()
			}
			out.WriteString(value)
		default:
			if previous != "." && previous != "(" {
				writeSpace()
			}
			out.WriteString(value)
		}
		previous = t.Text
		lineStart = false
	}
	return strings.TrimSpace(out.String()), nil
}
