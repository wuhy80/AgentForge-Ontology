package data

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func fieldAllowed(fields []string, id string) bool {
	for _, f := range fields {
		if f == "*" || f == id {
			return true
		}
	}
	return false
}

type expressionCompiler struct {
	schema    *c.Schema
	allowed   []string
	nodes     int
	column    func(c.Property) (string, error)
	parameter func(c.Property) string
}

func (b *expressionCompiler) placeholder(property c.Property) string {
	if b.parameter != nil {
		return b.parameter(property)
	}
	return parameter(property)
}

// MySQL can coerce DECIMAL versus a string parameter to DOUBLE. Explicitly
// typing every decimal parameter preserves adjacent values above 2^53.
func parameter(p c.Property) string {
	if p.DataType == "decimal" {
		t, _ := sqlType(p)
		return "CAST(? AS " + t + ")"
	}
	return "?"
}

func (b *expressionCompiler) compile(e *c.Expression, depth int) (string, []any, error) {
	if e == nil {
		return "1=1", nil, nil
	}
	b.nodes++
	if b.nodes > 100 || depth > 10 {
		return "", nil, problem(422, "budget_exceeded", "表达式超过节点或深度预算")
	}
	bad := func() (string, []any, error) {
		return "", nil, problem(422, "invalid_expression", "表达式结构不合法")
	}
	if e.Op == "and" || e.Op == "or" || e.Op == "not" {
		if e.Field != "" || len(e.Value) > 0 || len(e.Args) == 0 || (e.Op == "not" && len(e.Args) != 1) {
			return bad()
		}
		parts := []string{}
		args := []any{}
		for i := range e.Args {
			sql, a, err := b.compile(&e.Args[i], depth+1)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, "("+sql+")")
			args = append(args, a...)
		}
		if e.Op == "not" {
			return "NOT " + parts[0], args, nil
		}
		return strings.Join(parts, " "+strings.ToUpper(e.Op)+" "), args, nil
	}
	if len(e.Args) > 0 {
		return bad()
	}
	p, ok := findProperty(b.schema, e.Field)
	if !ok {
		return "", nil, problem(422, "unknown_field", "表达式引用未知属性")
	}
	if !fieldAllowed(b.allowed, p.ID) {
		return "", nil, problem(403, "forbidden_field", "不允许使用该属性查询")
	}
	col := quote(columnName(p.ID))
	if b.column != nil {
		var err error
		col, err = b.column(p)
		if err != nil {
			return "", nil, err
		}
	}
	if e.Op == "isNull" || e.Op == "isNotNull" {
		if len(e.Value) > 0 {
			return bad()
		}
		op := " IS NULL"
		if e.Op == "isNotNull" {
			op = " IS NOT NULL"
		}
		return col + op, nil, nil
	}
	if e.Op == "in" || e.Op == "notIn" {
		var raws []json.RawMessage
		if json.Unmarshal(e.Value, &raws) != nil || len(raws) == 0 || len(raws) > 100 {
			return bad()
		}
		args := make([]any, 0, len(raws))
		marks := make([]string, 0, len(raws))
		for _, raw := range raws {
			v, err := typedValue(p, raw)
			if err != nil {
				return "", nil, err
			}
			args = append(args, v)
			marks = append(marks, b.placeholder(p))
		}
		op := " IN "
		if e.Op == "notIn" {
			op = " NOT IN "
		}
		return col + op + "(" + strings.Join(marks, ",") + ")", args, nil
	}
	if len(e.Value) == 0 || string(e.Value) == "null" {
		return bad()
	}
	v, err := typedValue(p, e.Value)
	if err != nil {
		return "", nil, err
	}
	if e.Op == "startsWith" {
		if p.DataType != "string" && p.DataType != "text" && p.DataType != "longtext" {
			return bad()
		}
		s := v.(string)
		s = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
		return col + " LIKE ? ESCAPE '!'", []any{s + "%"}, nil
	}
	ops := map[string]string{"eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}
	op, ok := ops[e.Op]
	if !ok {
		return "", nil, problem(422, "unsupported", "不支持的查询操作符")
	}
	if p.DataType == "boolean" && e.Op != "eq" && e.Op != "ne" {
		return bad()
	}
	return col + " " + op + " " + b.placeholder(p), []any{v}, nil
}

type pageCursor struct {
	Fingerprint   string          `json:"f"`
	TenantID      string          `json:"t"`
	Subject       string          `json:"s"`
	SchemaID      string          `json:"d"`
	Digest        string          `json:"g"`
	Authorization string          `json:"a"`
	LastID        string          `json:"i"`
	LastValue     json.RawMessage `json:"v,omitempty"`
	Expires       int64           `json:"e"`
}

func signCursor(secret string, p pageCursor) (string, error) {
	if len(secret) < 32 {
		return "", problem(503, "configuration", "游标签名密钥至少需要 32 字节")
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func parseCursor(secret, token string) (pageCursor, error) {
	return parseBoundCursor(secret, token, true)
}

func parseBoundCursor(secret, token string, uuidOnly bool) (pageCursor, error) {
	var p pageCursor
	bad := func() (pageCursor, error) {
		return p, problem(422, "invalid_cursor", "游标已失效或不属于本次查询")
	}
	if len(secret) < 32 || len(token) > 8192 {
		return bad()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return bad()
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return bad()
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return bad()
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return bad()
	}
	if json.Unmarshal(b, &p) != nil || p.Expires < time.Now().Unix() || p.LastID == "" || len(p.LastID) > 1024 || (uuidOnly && !uuidPattern.MatchString(p.LastID)) {
		return bad()
	}
	return p, nil
}

type compiledQuery struct {
	SQL         string
	Args        []any
	Projection  []c.Property
	ScanFields  []c.Property
	Sort        *c.Property
	Fingerprint string
	Limit       int
}

func compileQuery(d c.Definition, etag string, p c.Principal, dec c.Decision, q c.QuerySpec, secret string) (compiledQuery, error) {
	var result compiledQuery
	if err := validateSchema(d); err != nil {
		return result, err
	}
	if q.Page.Limit < 1 || q.Page.Limit > 100 || len(q.Select) == 0 || len(q.Select) > 100 || len(q.OrderBy) > 1 {
		return result, problem(422, "budget_exceeded", "需显式投影、每页 1..100 条且至多一个排序字段")
	}
	selected := map[string]bool{}
	cols := []string{"`id`", "`etag`"}
	for _, key := range q.Select {
		f, ok := findProperty(d.Schema, key)
		if !ok {
			return result, problem(422, "unknown_field", "投影引用未知属性")
		}
		if selected[f.ID] {
			return result, problem(422, "duplicate_field", "投影属性重复")
		}
		if !fieldAllowed(dec.ReadFields, f.ID) {
			return result, problem(403, "forbidden_field", "无权读取该属性")
		}
		selected[f.ID] = true
		result.Projection = append(result.Projection, f)
		result.ScanFields = append(result.ScanFields, f)
		cols = append(cols, quote(columnName(f.ID)))
	}
	compiler := expressionCompiler{schema: d.Schema, allowed: dec.ReadFields}
	user, args, err := compiler.compile(q.Where, 0)
	if err != nil {
		return result, err
	}
	authCompiler := expressionCompiler{schema: d.Schema, allowed: []string{"*"}}
	auth, authArgs, err := authCompiler.compile(dec.RowFilter, 0)
	if err != nil {
		return result, err
	}
	conditions := []string{"`tenant_id` = ?", "(" + user + ")", "(" + auth + ")"}
	allArgs := []any{p.TenantID}
	allArgs = append(allArgs, args...)
	allArgs = append(allArgs, authArgs...)
	order := "`id` ASC"
	descending := false
	if len(q.OrderBy) == 1 {
		f, ok := findProperty(d.Schema, q.OrderBy[0].Field)
		if !ok {
			return result, problem(422, "unknown_field", "排序引用未知属性")
		}
		if !fieldAllowed(dec.ReadFields, f.ID) {
			return result, problem(403, "forbidden_field", "无权使用该属性排序")
		}
		if f.DataType == "text" || f.DataType == "longtext" {
			return result, problem(422, "unsupported", "首批不支持 text / longtext 排序")
		}
		direction := strings.ToLower(q.OrderBy[0].Direction)
		if direction != "asc" && direction != "desc" {
			return result, problem(422, "invalid_order", "排序方向必须为 asc 或 desc")
		}
		descending = direction == "desc"
		result.Sort = &f
		order = quote(columnName(f.ID)) + " " + strings.ToUpper(direction) + ", `id` ASC"
		if !selected[f.ID] {
			result.ScanFields = append(result.ScanFields, f)
			cols = append(cols, quote(columnName(f.ID)))
		}
	}
	bound := q
	bound.Page.Cursor = ""
	result.Fingerprint = digest(bound)
	if q.Page.Cursor != "" {
		cur, err := parseCursor(secret, q.Page.Cursor)
		if err != nil {
			return result, err
		}
		if cur.TenantID != p.TenantID || cur.Subject != p.Subject || cur.SchemaID != d.ID || cur.Digest != bareETag(etag) || cur.Authorization != digest(dec) || cur.Fingerprint != result.Fingerprint {
			return result, problem(422, "invalid_cursor", "定义、权限或查询已改变，请重新查询")
		}
		if result.Sort == nil {
			conditions = append(conditions, "`id` > ?")
			allArgs = append(allArgs, cur.LastID)
		} else {
			f := *result.Sort
			col := quote(columnName(f.ID))
			value, err := typedValue(f, cur.LastValue)
			if err != nil {
				return result, problem(422, "invalid_cursor", "游标排序值不合法")
			}
			if value == nil {
				if descending {
					conditions = append(conditions, "("+col+" IS NULL AND `id` > ?)")
				} else {
					conditions = append(conditions, "("+col+" IS NOT NULL OR ("+col+" IS NULL AND `id` > ?))")
				}
				allArgs = append(allArgs, cur.LastID)
			} else {
				op := ">"
				nullPart := ""
				if descending {
					op = "<"
					if f.Nullable {
						nullPart = " OR " + col + " IS NULL"
					}
				}
				conditions = append(conditions, "("+col+" "+op+" "+parameter(f)+" OR ("+col+" = "+parameter(f)+" AND `id` > ?)"+nullPart+")")
				allArgs = append(allArgs, value, value, cur.LastID)
			}
		}
	}
	result.SQL = "SELECT " + strings.Join(cols, ",") + " FROM " + quote(tableName(d.ID)) + " WHERE " + strings.Join(conditions, " AND ") + " ORDER BY " + order + " LIMIT ?"
	result.Args = append(allArgs, q.Page.Limit+1)
	result.Limit = q.Page.Limit
	return result, nil
}

func (q compiledQuery) String() string { return fmt.Sprintf("%s [%d parameters]", q.SQL, len(q.Args)) }
