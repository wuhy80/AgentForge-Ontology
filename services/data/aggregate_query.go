package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

var aggregateMeasureID = regexp.MustCompile(`^m[A-Za-z0-9_]{0,63}$`)

type compiledAggregate struct {
	SQL         string
	Args        []any
	Fields      []c.Property
	Bound       c.QuerySpec
	Fingerprint string
	Offset      int
}

func aggregateAlias(index int) string { return quote(fmt.Sprintf("af_a%d", index)) }

// bindAggregateQuery is purely logical. It never scans business data or opens a
// physical source; saved templates and OntologySQL use the same checks.
func bindAggregateQuery(d c.Definition, dec c.Decision, q c.QuerySpec) (c.QuerySpec, []c.Property, []string, error) {
	if d.Kind != c.KindSchema || d.Schema == nil {
		return q, nil, nil, problem(422, "aggregate_unsupported", "聚合需要可查询Schema")
	}
	if q.Aggregate == nil || q.Rows != nil || q.Via != nil || d.Schema.StorageBinding != nil {
		return q, nil, nil, problem(422, "aggregate_unsupported", "聚合首批仅支持默认平台单Schema，不能混用映射、关系行或固定实例导航")
	}
	if len(q.Select) != 0 {
		return q, nil, nil, problem(422, "invalid_aggregate", "聚合不能同时声明实体select投影")
	}
	if len(q.Aggregate.GroupBy) > c.MaxAggregateGroupFields || len(q.Aggregate.Measures) < 1 || len(q.Aggregate.Measures) > c.MaxAggregateMeasures || q.Page.Limit < 1 || q.Page.Limit > 100 || len(q.OrderBy) > 1 {
		return q, nil, nil, problem(422, "aggregate_budget", "聚合需要0..3个分组、1..20个计量、每页1..100组且至多1个排序字段")
	}
	bound := q
	bound.From = c.Source{SchemaID: d.ID}
	agg := *q.Aggregate
	agg.GroupBy = append([]string{}, q.Aggregate.GroupBy...)
	agg.Measures = append([]c.AggregateMeasure{}, q.Aggregate.Measures...)
	bound.Aggregate = &agg
	bound.OrderBy = append([]c.Order{}, q.OrderBy...)
	fields, expressions := []c.Property{}, []string{}
	ids, names := map[string]bool{}, map[string]bool{}
	get := func(key string) (c.Property, error) {
		p, ok := findProperty(d.Schema, key)
		if !ok {
			return p, problem(422, "unknown_field", "聚合引用未知属性")
		}
		if !fieldAllowed(dec.ReadFields, p.ID) {
			return p, problem(403, "forbidden_field", "无权使用该属性聚合")
		}
		return p, nil
	}
	appendField := func(p c.Property, expression string) error {
		if ids[p.ID] || names[nameKey(p.Name)] {
			return problem(422, "duplicate_aggregate_field", "聚合输出标识或名称重复，请为计量指定不同名称")
		}
		ids[p.ID], names[nameKey(p.Name)] = true, true
		p.ReadOnly = true
		p.Default, p.Mapping = nil, nil
		fields, expressions = append(fields, p), append(expressions, expression)
		return nil
	}
	for i, key := range agg.GroupBy {
		p, err := get(key)
		if err != nil {
			return q, nil, nil, err
		}
		if p.DataType == "text" || p.DataType == "longtext" {
			return q, nil, nil, problem(422, "aggregate_type_unsupported", "text/longtext不能用于分组")
		}
		agg.GroupBy[i] = p.ID
		if err = appendField(p, quote(columnName(p.ID))); err != nil {
			return q, nil, nil, err
		}
	}
	for i, m := range agg.Measures {
		if !aggregateMeasureID.MatchString(m.ID) {
			return q, nil, nil, problem(422, "invalid_aggregate_id", "计量id须以m开头，后接至多63个ASCII字母、数字或下划线")
		}
		var source c.Property
		column := "*"
		if m.Field != "" {
			var err error
			source, err = get(m.Field)
			if err != nil {
				return q, nil, nil, err
			}
			m.Field = source.ID
			column = quote(columnName(source.ID))
		}
		if m.Op != "count" && m.Field == "" {
			return q, nil, nil, problem(422, "invalid_aggregate", "除count(*)外的计量必须声明字段")
		}
		p := c.Property{ID: m.ID, Nullable: true, ReadOnly: true}
		var expression, display string
		switch m.Op {
		case "count", "countDistinct":
			if m.Op == "countDistinct" && (source.DataType == "text" || source.DataType == "longtext") {
				return q, nil, nil, problem(422, "aggregate_type_unsupported", "text/longtext不能用于去重计数")
			}
			p.DataType, p.Nullable = "int64", false
			if m.Op == "countDistinct" {
				expression = "COUNT(DISTINCT " + column + ")"
				display = "COUNT(DISTINCT " + source.Name + ")"
			} else {
				expression = "COUNT(" + column + ")"
				display = "COUNT(*)"
				if m.Field != "" {
					display = "COUNT(" + source.Name + ")"
				}
			}
		case "sum", "avg":
			if source.DataType != "int64" && source.DataType != "decimal" {
				return q, nil, nil, problem(422, "aggregate_type_unsupported", "SUM/AVG仅支持int64或decimal属性")
			}
			scale := source.Scale
			if source.DataType == "int64" {
				scale = 0
			}
			if scale > 30 {
				return q, nil, nil, problem(422, "aggregate_type_unsupported", "首批数值聚合的小数位不能超过30")
			}
			if m.Op == "avg" {
				scale += 4
				if scale > 30 {
					scale = 30
				}
			}
			p.DataType, p.Precision, p.Scale = "decimal", 65, scale
			decimal := fmt.Sprintf("DECIMAL(65,%d)", scale)
			// Casting the AVG input to the declared output scale guarantees at
			// least that many fraction digits even if div_precision_increment=0.
			input := column
			if m.Op == "avg" {
				input = "CAST(" + column + " AS " + decimal + ")"
			}
			operation := strings.ToUpper(m.Op) + "(" + input + ")"
			if m.Op == "avg" {
				operation = fmt.Sprintf("ROUND(%s,%d)", operation, scale)
			}
			expression = "CAST(" + operation + " AS " + decimal + ")"
			display = strings.ToUpper(m.Op) + "(" + source.Name + ")"
		case "min", "max":
			if source.DataType == "text" || source.DataType == "longtext" || source.DataType == "boolean" {
				return q, nil, nil, problem(422, "aggregate_type_unsupported", "MIN/MAX首批支持数值、string、uuid和timestamp属性")
			}
			p = source
			p.ID = m.ID
			p.Nullable = true
			expression = strings.ToUpper(m.Op) + "(" + column + ")"
			display = strings.ToUpper(m.Op) + "(" + source.Name + ")"
		default:
			return q, nil, nil, problem(422, "aggregate_operation_unsupported", "聚合仅支持count、countDistinct、sum、avg、min和max")
		}
		if m.Name == "" {
			m.Name = display
		}
		if nameKey(m.Name) == "" || utf8.RuneCountInString(m.Name) > 128 || strings.IndexFunc(m.Name, unicode.IsControl) >= 0 {
			return q, nil, nil, problem(422, "invalid_aggregate_name", "计量名称需要1..128个字符且不含控制字符")
		}
		p.Name = m.Name
		if err := appendField(p, expression); err != nil {
			return q, nil, nil, err
		}
		agg.Measures[i] = m
	}
	if len(bound.OrderBy) == 1 {
		order := &bound.OrderBy[0]
		p, ok := findProperty(&c.Schema{Properties: fields}, order.Field)
		if !ok {
			return q, nil, nil, problem(422, "unknown_aggregate_order", "聚合排序只能引用输出字段的标识或名称")
		}
		order.Field = p.ID
		order.Direction = strings.ToLower(order.Direction)
		if order.Direction != "asc" && order.Direction != "desc" {
			return q, nil, nil, problem(422, "invalid_order", "排序方向必须为asc或desc")
		}
	}
	return bound, fields, expressions, nil
}

func compileAggregateQuery(d c.Definition, etag string, p c.Principal, dec c.Decision, q c.QuerySpec, secret string) (compiledAggregate, error) {
	var out compiledAggregate
	if err := validateSchema(d); err != nil {
		return out, err
	}
	bound, fields, expressions, err := bindAggregateQuery(d, dec, q)
	if err != nil {
		return out, err
	}
	compiler := expressionCompiler{schema: d.Schema, allowed: dec.ReadFields}
	user, args, err := compiler.compile(bound.Where, 0)
	if err != nil {
		return out, err
	}
	authCompiler := expressionCompiler{schema: d.Schema, allowed: []string{"*"}}
	auth, authArgs, err := authCompiler.compile(dec.RowFilter, 0)
	if err != nil {
		return out, err
	}
	cols, groups, orders := []string{}, []string{}, []string{}
	for i, expression := range expressions {
		cols = append(cols, expression+" AS "+aggregateAlias(i))
	}
	ordered := map[int]bool{}
	if len(bound.OrderBy) == 1 {
		for i, f := range fields {
			if f.ID == bound.OrderBy[0].Field {
				orders = append(orders, aggregateAlias(i)+" "+strings.ToUpper(bound.OrderBy[0].Direction))
				ordered[i] = true
			}
		}
	}
	for i := range bound.Aggregate.GroupBy {
		groups = append(groups, expressions[i])
		if !ordered[i] {
			orders = append(orders, aggregateAlias(i)+" ASC")
		}
	}
	out.SQL = "SELECT " + strings.Join(cols, ",") + " FROM " + quote(tableName(d.ID)) + " WHERE `tenant_id`=? AND (" + user + ") AND (" + auth + ")"
	if len(groups) > 0 {
		out.SQL += " GROUP BY " + strings.Join(groups, ",")
	}
	if len(orders) > 0 {
		out.SQL += " ORDER BY " + strings.Join(orders, ",")
	}
	out.SQL += " LIMIT " + strconv.Itoa(c.MaxAggregateGroups+1)
	out.Args = append([]any{p.TenantID}, args...)
	out.Args = append(out.Args, authArgs...)
	out.Fields = fields
	out.Bound = bound
	out.Bound.Page.Cursor = ""
	out.Fingerprint = enumQueryFingerprint(digest(out.Bound), d)
	if q.Page.Cursor != "" {
		cursor, e := parseBoundCursor(secret, q.Page.Cursor, false)
		if e != nil {
			return out, e
		}
		offset, e := strconv.Atoi(cursor.LastID)
		if e != nil || offset < 1 || offset > c.MaxAggregateGroups || strconv.Itoa(offset) != cursor.LastID || cursor.TenantID != p.TenantID || cursor.Subject != p.Subject || cursor.SchemaID != d.ID || cursor.Digest != bareETag(etag) || cursor.Authorization != digest(dec) || cursor.Fingerprint != out.Fingerprint || len(cursor.LastValue) != 0 {
			return out, problem(422, "invalid_cursor", "聚合定义、权限、查询或游标已改变，请重新查询")
		}
		out.Offset = offset
	}
	return out, nil
}

func (h *handler) queryAggregate(ctx context.Context, p c.Principal, current c.CurrentDefinition, dec c.Decision, q c.QuerySpec) (c.QueryResult, error) {
	out := c.QueryResult{Data: []c.Instance{}, AggregateMode: true, AggregateRows: []c.AggregateRow{}}
	compiled, err := compileAggregateQuery(current.Definition, current.ETag, p, dec, q, h.cfg.CursorSecret)
	if err != nil {
		return out, err
	}
	out.Fields = safeQueryProperties(compiled.Fields)
	budget, err := newQueryBudget(out.Fields)
	if err != nil {
		return out, err
	}
	all := []c.AggregateRow{}
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if e := h.gate(ctx, tx, p, current); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, compiled.SQL, compiled.Args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			if len(all) == c.MaxAggregateGroups {
				return problem(422, "aggregate_group_budget", "聚合结果超过1000组，请增加筛选或减少分组；不能截断统计")
			}
			values, dest := make([]any, len(compiled.Fields)), make([]any, len(compiled.Fields))
			for i := range values {
				dest[i] = &values[i]
			}
			if e = rows.Scan(dest...); e != nil {
				return e
			}
			if e = budget.checkDatabaseRow(values); e != nil {
				return e
			}
			item := c.AggregateRow{Values: map[string]json.RawMessage{}}
			for i, field := range compiled.Fields {
				raw, err := rawDatabaseValue(field, values[i])
				if err != nil {
					return err
				}
				item.Values[field.ID] = raw
			}
			if e = budget.add(item); e != nil {
				return e
			}
			all = append(all, item)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if e = rows.Close(); e != nil {
			return e
		}
		// SELECT can clamp an out-of-range DECIMAL and merely report a warning.
		// AVG explicitly rounds to its declared scale above; no implicit
		// truncation, saturation or other numeric warning is an accepted result.
		// Read the count before any later statement can replace diagnostics.
		var warnings int
		if e = tx.QueryRowContext(ctx, "SHOW COUNT(*) WARNINGS").Scan(&warnings); e != nil {
			return e
		}
		if warnings != 0 {
			return problem(422, "aggregate_numeric_range", "聚合产生数值溢出或截断，不能交付不精确的统计结果")
		}
		if e = h.recheck(ctx, p, "instance.query", current.Definition.ID, dec); e != nil {
			return e
		}
		return h.recheckEnums(ctx, current)
	})
	if err != nil {
		var numeric *mysql.MySQLError
		if errors.As(err, &numeric) && (numeric.Number == 1264 || numeric.Number == 1265 || numeric.Number == 1690) {
			return c.QueryResult{}, problem(422, "aggregate_numeric_range", "聚合超出受支持的精度或范围")
		}
		return c.QueryResult{}, err
	}
	start := compiled.Offset
	if start > len(all) {
		start = len(all)
	}
	end := start + q.Page.Limit
	if end > len(all) {
		end = len(all)
	}
	out.AggregateRows = append(out.AggregateRows, all[start:end]...)
	out.HasMore = end < len(all)
	if out.HasMore {
		out.NextCursor, err = signCursor(h.cfg.CursorSecret, pageCursor{Fingerprint: compiled.Fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: current.Definition.ID, Digest: current.ETag, Authorization: digest(dec), LastID: strconv.Itoa(end), Expires: time.Now().Add(15 * time.Minute).Unix()})
	}
	return out, err
}

// validateBoundQuery is a read-only logical admission path for saved queries.
// It deliberately does not call query executors, publication gates or SELECT
// over business tables. Parameters must already have typed validation samples.
func (h *handler) validateBoundQuery(ctx context.Context, p c.Principal, q c.QuerySpec) (c.QuerySpec, error) {
	if q.Rows != nil || q.Via != nil {
		return q, problem(422, "saved_query_unsupported", "保存查询首批仅支持单Schema实体或聚合查询")
	}
	q.Page.Cursor = ""
	bound, err := h.compileBoundQuery(ctx, p, q)
	if err != nil {
		return q, err
	}
	if err = h.recheck(ctx, p, "instance.query", bound.current.Definition.ID, bound.decision); err != nil {
		return bound.Query, err
	}
	if err = h.recheckEnums(ctx, bound.current); err != nil {
		return bound.Query, err
	}
	return bound.Query, nil
}
