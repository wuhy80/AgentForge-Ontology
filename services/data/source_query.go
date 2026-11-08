package data

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Parse a closed SELECT grammar and emit new SQL. Never pass user SQL through
// to the driver: SELECT alone permits functions, file IO and locking clauses.
var sourceSQLToken = regexp.MustCompile("^(?:[\\s]+|`(?:``|[^`])+`|\"(?:\"\"|[^\"])+\"|'(?:''|[^'])*'|[\\p{L}_][\\p{L}\\p{N}_$]*|-?[0-9]+(?:\\.[0-9]+)?|<=|>=|<>|!=|[=<>*.,;()])")
var sourceSQLName = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_$]*$`)
var sourceSQLNumber = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)

type sourceSelect struct {
	tokens           []string
	pos              int
	source           c.DataSource
	namespace, table string
	columns          []string
	args             []any
	where, order     string
	limit            int
}

func sourceSQLUnsupported() error {
	return problem(422, "unsupported_source_sql", "仅支持单表 SELECT：字段或 *、WHERE 比较/LIKE/IS NULL（AND/OR）、ORDER BY、LIMIT 1～100；不支持函数、联表、子查询、注释或多语句")
}

func (p *sourceSelect) take(s string) bool {
	if p.pos < len(p.tokens) && strings.EqualFold(p.tokens[p.pos], s) {
		p.pos++
		return true
	}
	return false
}

func (p *sourceSelect) name() (string, error) {
	if p.pos >= len(p.tokens) {
		return "", sourceSQLUnsupported()
	}
	s := p.tokens[p.pos]
	p.pos++
	if s[0] == '`' || s[0] == '"' {
		q := s[:1]
		s = strings.ReplaceAll(s[1:len(s)-1], q+q, q)
	} else if !sourceSQLName.MatchString(s) {
		return "", sourceSQLUnsupported()
	} else if p.source.Driver == "postgresql" {
		s = strings.ToLower(s)
	}
	if _, err := sourceQuote(p.source.Driver, s); err != nil {
		return "", err
	}
	return s, nil
}

func (p *sourceSelect) column() (string, error) {
	s, err := p.name()
	if err != nil {
		return "", err
	}
	p.columns = append(p.columns, s)
	return sourceQuote(p.source.Driver, s)
}

func (p *sourceSelect) predicate(depth int) (string, error) {
	if depth > 8 {
		return "", sourceSQLUnsupported()
	}
	var parts []string
	for {
		var part string
		if p.take("(") {
			inner, err := p.predicate(depth + 1)
			if err != nil || !p.take(")") {
				return "", sourceSQLUnsupported()
			}
			part = "(" + inner + ")"
		} else {
			column, err := p.column()
			if err != nil {
				return "", err
			}
			if p.take("IS") {
				not := ""
				if p.take("NOT") {
					not = " NOT"
				}
				if !p.take("NULL") {
					return "", sourceSQLUnsupported()
				}
				part = column + " IS" + not + " NULL"
			} else {
				if p.pos >= len(p.tokens) {
					return "", sourceSQLUnsupported()
				}
				op := strings.ToUpper(p.tokens[p.pos])
				p.pos++
				switch op {
				case "=", "!=", "<>", "<", "<=", ">", ">=", "LIKE":
				default:
					return "", sourceSQLUnsupported()
				}
				if p.pos >= len(p.tokens) {
					return "", sourceSQLUnsupported()
				}
				value := p.tokens[p.pos]
				p.pos++
				var arg any
				if value[0] == '\'' {
					arg = strings.ReplaceAll(value[1:len(value)-1], "''", "'")
				} else if strings.EqualFold(value, "TRUE") || strings.EqualFold(value, "FALSE") {
					arg = strings.EqualFold(value, "TRUE")
				} else if sourceSQLNumber.MatchString(value) {
					arg = value
				} else {
					return "", sourceSQLUnsupported()
				}
				p.args = append(p.args, arg)
				placeholder := "?"
				if p.source.Driver == "postgresql" {
					placeholder = "$" + strconv.Itoa(len(p.args))
				}
				part = column + " " + op + " " + placeholder
			}
		}
		parts = append(parts, part)
		if p.take("AND") {
			parts = append(parts, "AND")
		} else if p.take("OR") {
			parts = append(parts, "OR")
		} else {
			break
		}
	}
	return strings.Join(parts, " "), nil
}

func parseSourceSelect(source c.DataSource, text string) (*sourceSelect, string, error) {
	p := &sourceSelect{source: source, limit: 100}
	if len(text) > 16<<10 || !utf8.ValidString(text) {
		return nil, "", sourceSQLUnsupported()
	}
	for rest := text; rest != ""; {
		token := sourceSQLToken.FindString(rest)
		if token == "" || len(p.tokens) >= 2048 {
			return nil, "", sourceSQLUnsupported()
		}
		rest = rest[len(token):]
		if strings.TrimSpace(token) != "" {
			p.tokens = append(p.tokens, token)
		}
	}
	if !p.take("SELECT") {
		return nil, "", sourceSQLUnsupported()
	}
	var projection []string
	if p.take("*") {
		projection = []string{"*"}
	} else {
		for {
			col, err := p.column()
			if err != nil {
				return nil, "", err
			}
			projection = append(projection, col)
			if !p.take(",") {
				break
			}
		}
	}
	if !p.take("FROM") {
		return nil, "", sourceSQLUnsupported()
	}
	name, err := p.name()
	if err != nil {
		return nil, "", err
	}
	p.table = name
	if p.take(".") {
		p.namespace = name
		p.table, err = p.name()
		if err != nil {
			return nil, "", err
		}
	}
	p.namespace, err = sourceNamespace(source, p.namespace)
	if err != nil {
		return nil, "", err
	}
	if err = validSourceTable(source, p.table); err != nil {
		return nil, "", err
	}
	if p.take("WHERE") {
		p.where, err = p.predicate(0)
		if err != nil {
			return nil, "", err
		}
	}
	if p.take("ORDER") {
		if !p.take("BY") {
			return nil, "", sourceSQLUnsupported()
		}
		var order []string
		for {
			col, err := p.column()
			if err != nil {
				return nil, "", err
			}
			if p.take("DESC") {
				col += " DESC"
			} else if p.take("ASC") {
				col += " ASC"
			}
			order = append(order, col)
			if !p.take(",") {
				break
			}
		}
		p.order = strings.Join(order, ", ")
	}
	if p.take("LIMIT") {
		if p.pos >= len(p.tokens) {
			return nil, "", sourceSQLUnsupported()
		}
		p.limit, err = strconv.Atoi(p.tokens[p.pos])
		p.pos++
		if err != nil || p.limit < 1 || p.limit > 100 {
			return nil, "", sourceSQLUnsupported()
		}
	}
	p.take(";")
	if p.pos != len(p.tokens) {
		return nil, "", sourceSQLUnsupported()
	}
	ns, _ := sourceQuote(source.Driver, p.namespace)
	table, _ := sourceQuote(source.Driver, p.table)
	selectSQL := "SELECT "
	if source.Driver == "mysql" {
		selectSQL += "/*+ MAX_EXECUTION_TIME(10000) */ "
	}
	selectSQL += strings.Join(projection, ", ") + " FROM " + ns + "." + table
	if p.where != "" {
		selectSQL += " WHERE " + p.where
	}
	if p.order != "" {
		selectSQL += " ORDER BY " + p.order
	}
	selectSQL += " LIMIT " + strconv.Itoa(p.limit+1)
	return p, selectSQL, nil
}

func querySource(ctx context.Context, db *sql.DB, source c.DataSource, text string) (c.SourceQueryResult, error) {
	started := time.Now()
	out := c.SourceQueryResult{Columns: []string{}, Rows: [][]*string{}}
	if source.TenantIsolation != "dedicated" {
		return out, problem(422, "source_query_isolation", "共享数据库请通过模型数据探索查询；直接 SQL 仅开放给当前租户专属数据源")
	}
	p, statement, err := parseSourceSelect(source, text)
	if err != nil {
		return out, err
	}
	table, err := readSourceTable(ctx, db, source, p.namespace, p.table)
	if err != nil {
		return out, err
	}
	for _, column := range p.columns {
		found := false
		for _, actual := range table.Columns {
			if column == actual.Name {
				found = true
				break
			}
		}
		if !found {
			return out, problem(422, "source_query_column", "查询字段不存在，请刷新表结构后核对")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, problem(422, "source_query_failed", "无法开启只读查询，请核对数据库权限")
	}
	defer tx.Rollback()
	if source.Driver == "postgresql" {
		if _, err = tx.ExecContext(ctx, "SET LOCAL statement_timeout = '10s'"); err != nil {
			return out, problem(422, "source_query_failed", "无法设置查询超时")
		}
	}
	rows, err := tx.QueryContext(ctx, statement, p.args...)
	if err != nil {
		return out, problem(422, "source_query_failed", "SQL 查询失败，请核对字段、条件类型和数据库读取权限")
	}
	defer rows.Close()
	out.Columns, err = rows.Columns()
	if err != nil {
		return out, problem(422, "source_query_failed", "无法读取查询列")
	}
	budget := 0
	for rows.Next() {
		if len(out.Rows) == p.limit {
			out.HasMore = true
			break
		}
		values := make([]any, len(out.Columns))
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err = rows.Scan(destinations...); err != nil {
			return out, problem(422, "source_query_failed", "无法读取查询结果")
		}
		row := make([]*string, len(values))
		for i, value := range values {
			if value == nil {
				continue
			}
			var s string
			switch v := value.(type) {
			case []byte:
				s = string(v)
			case time.Time:
				s = v.Format("2006-01-02T15:04:05.999999999Z07:00")
			default:
				s = fmt.Sprint(value)
			}
			if !utf8.ValidString(s) {
				return out, problem(422, "source_query_binary", "查询包含二进制字段，请明确选择需要查看的文本字段")
			}
			budget += len(s)
			if budget > 4<<20 {
				return out, problem(422, "budget_exceeded", "结果超过4 MiB，请减少字段或 LIMIT 后重试")
			}
			row[i] = &s
		}
		out.Rows = append(out.Rows, row)
	}
	if rows.Err() != nil {
		return out, problem(422, "source_query_failed", "查询超时或结果读取中断")
	}
	out.Limit = p.limit
	out.ElapsedMS = time.Since(started).Milliseconds()
	return out, nil
}

func (h *handler) sourceQueryHTTP(w http.ResponseWriter, r *http.Request) error {
	p, err := h.sourceAdmin(r)
	if err != nil {
		return err
	}
	var input c.SourceQuery
	if err = decode(w, r, &input); err != nil {
		return err
	}
	source, _, err := h.loadSource(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if r.Header.Get("If-Match") == "" {
		return problem(428, "precondition_required", "查询必须携带数据源 If-Match")
	}
	if r.Header.Get("If-Match") != `"`+source.ETag+`"` {
		return problem(412, "precondition_failed", "数据源已改变，请返回列表刷新后重试")
	}
	if source.TenantIsolation != "dedicated" {
		return problem(422, "source_query_isolation", "共享数据库请通过模型数据探索查询；直接 SQL 仅开放给当前租户专属数据源")
	}
	if _, _, err = parseSourceSelect(source, input.SQL); err != nil {
		return err
	}
	db, current, err := h.openSource(r.Context(), p.TenantID, source.ID)
	if err != nil {
		return err
	}
	defer db.Close()
	if current.ETag != source.ETag {
		return problem(412, "precondition_failed", "数据源已改变，请返回列表刷新后重试")
	}
	result, err := querySource(r.Context(), db, current, input.SQL)
	if err != nil {
		return err
	}
	writeJSON(w, 200, result)
	return nil
}
