package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type mappedCompiledQuery struct {
	SQL         string
	Args        []any
	Projection  []c.Property
	ScanFields  []c.Property
	Sort        *c.Property
	Fingerprint string
	Limit       int
}

// Rebinding is restricted to generated SQL. Quoted identifiers and literal
// escape markers are skipped so unusual catalog names cannot change parameters.
func sourceBind(driver, statement string) string {
	if driver != "postgresql" {
		return statement
	}
	var out strings.Builder
	var quote byte
	parameter := 0
	for i := 0; i < len(statement); i++ {
		ch := statement[i]
		if quote != 0 {
			out.WriteByte(ch)
			if ch == quote {
				if i+1 < len(statement) && statement[i+1] == quote {
					i++
					out.WriteByte(statement[i])
				} else {
					quote = 0
				}
			}
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			quote = ch
			out.WriteByte(ch)
		} else if ch == '?' {
			parameter++
			fmt.Fprintf(&out, "$%d", parameter)
		} else {
			out.WriteByte(ch)
		}
	}
	return out.String()
}

func mappedKeyArgument(table *mappedTable, id string) (any, error) {
	column, ok := table.column(table.keyColumn)
	if !ok || id == "" || len(id) > 1024 {
		return nil, problem(422, "invalid_source_key", "实例主键不合法")
	}
	if sourceKeyType(column) == "int64" {
		value, err := strconv.ParseInt(id, 10, 64)
		if err != nil || strconv.FormatInt(value, 10) != id {
			return nil, problem(422, "invalid_source_key", "整数实例主键须使用规范十进制字符串")
		}
		return value, nil
	}
	return id, nil
}

func sourceExactStringColumn(table *mappedTable, name string) string {
	if table.source.Driver == "mysql" {
		return "BINARY " + table.quoted(name)
	}
	return "convert_to(CAST(" + table.quoted(name) + " AS text),'UTF8')"
}

func sourceExactStringParameter(table *mappedTable) string {
	if table.source.Driver == "mysql" {
		return "BINARY ?"
	}
	return "convert_to(CAST(? AS text),'UTF8')"
}

func mappedKeySQL(table *mappedTable) (string, string) {
	column, _ := table.column(table.keyColumn)
	if sourceKeyType(column) == "string" {
		return sourceExactStringColumn(table, table.keyColumn), sourceExactStringParameter(table)
	}
	if table.source.Driver == "postgresql" {
		return "CAST(" + table.quoted(table.keyColumn) + " AS bigint)", "CAST(? AS bigint)"
	}
	return table.quoted(table.keyColumn), "?"
}

func mappedParameter(driver string) func(c.Property) string {
	return func(property c.Property) string {
		if driver == "postgresql" && property.DataType == "int64" {
			return "CAST(? AS bigint)"
		}
		return parameter(property)
	}
}

func mappedFilterColumn(tables []*mappedTable) func(c.Property) (string, error) {
	return func(property c.Property) (string, error) {
		table, _, ok := mappedPropertyTable(tables, property.ID)
		if !ok {
			return "", problem(422, "invalid_mapping", "属性没有可用物理映射")
		}
		if !table.primary {
			return "", problem(422, "cross_source_condition_unsupported", "当前跨来源查询仅支持主表属性筛选、排序及权限行条件；其他来源属性可以投影读取")
		}
		column := table.quoted(property.Mapping.Column)
		if property.DataType == "string" || property.DataType == "text" || property.DataType == "longtext" || property.DataType == "uuid" {
			if table.source.Driver == "mysql" {
				column = "BINARY " + column
			} else {
				column = "CAST(" + column + " AS text) COLLATE \"C\""
			}
		}
		return column, nil
	}
}

func compileMappedQuery(d c.Definition, tables []*mappedTable, etag string, principal c.Principal, decision c.Decision, query c.QuerySpec, secret, entityID string) (mappedCompiledQuery, error) {
	result := mappedCompiledQuery{Limit: query.Page.Limit}
	if query.Page.Limit < 1 || query.Page.Limit > 100 || (len(query.Select) == 0 && entityID == "") || len(query.Select) > 100 || len(query.OrderBy) > 1 {
		return result, problem(422, "budget_exceeded", "需显式投影、每页1到100条且最多一个主表排序属性")
	}
	primary := tables[0]
	idColumn := primary.quoted(primary.keyColumn)
	idCompare, idParameter := mappedKeySQL(primary)
	columns := []string{idColumn}
	selected := map[string]bool{}
	for _, key := range query.Select {
		property, ok := findProperty(d.Schema, key)
		if !ok {
			return result, problem(422, "unknown_field", "投影引用未知属性")
		}
		if selected[property.ID] {
			return result, problem(422, "duplicate_field", "投影属性重复")
		}
		if !fieldAllowed(decision.ReadFields, property.ID) {
			return result, problem(403, "forbidden_field", "无权读取该属性")
		}
		selected[property.ID] = true
		result.Projection = append(result.Projection, property)
		table, _, _ := mappedPropertyTable(tables, property.ID)
		if table.primary {
			result.ScanFields = append(result.ScanFields, property)
			columns = append(columns, primary.quoted(property.Mapping.Column))
		}
	}
	compiler := expressionCompiler{schema: d.Schema, allowed: decision.ReadFields, column: mappedFilterColumn(tables), parameter: mappedParameter(primary.source.Driver)}
	user, userArgs, err := compiler.compile(query.Where, 0)
	if err != nil {
		return result, err
	}
	authorization := expressionCompiler{schema: d.Schema, allowed: []string{"*"}, column: mappedFilterColumn(tables), parameter: mappedParameter(primary.source.Driver)}
	auth, authArgs, err := authorization.compile(decision.RowFilter, 0)
	if err != nil {
		return result, err
	}
	conditions := []string{"(" + user + ")", "(" + auth + ")"}
	args := append(userArgs, authArgs...)
	if primary.tenantColumn != "" {
		conditions = append(conditions, sourceExactStringColumn(primary, primary.tenantColumn)+" = "+sourceExactStringParameter(primary))
		args = append(args, principal.TenantID)
	}
	if entityID != "" {
		key, err := mappedKeyArgument(primary, entityID)
		if err != nil {
			return result, err
		}
		conditions = append(conditions, idCompare+" = "+idParameter)
		args = append(args, key)
	}
	order := idCompare + " ASC"
	descending := false
	if len(query.OrderBy) == 1 {
		property, ok := findProperty(d.Schema, query.OrderBy[0].Field)
		if !ok {
			return result, problem(422, "unknown_field", "排序引用未知属性")
		}
		if !fieldAllowed(decision.ReadFields, property.ID) {
			return result, problem(403, "forbidden_field", "无权使用该属性排序")
		}
		column, err := mappedFilterColumn(tables)(property)
		if err != nil {
			return result, err
		}
		if property.DataType == "text" || property.DataType == "longtext" {
			return result, problem(422, "unsupported", "长文本属性暂不支持排序")
		}
		direction := strings.ToLower(query.OrderBy[0].Direction)
		if direction != "asc" && direction != "desc" {
			return result, problem(422, "invalid_order", "排序方向必须为asc或desc")
		}
		descending = direction == "desc"
		result.Sort = &property
		order = column + " " + strings.ToUpper(direction)
		if primary.source.Driver == "postgresql" {
			if descending {
				order += " NULLS LAST"
			} else {
				order += " NULLS FIRST"
			}
		}
		order += ", " + idCompare + " ASC"
		if !selected[property.ID] {
			result.ScanFields = append(result.ScanFields, property)
			columns = append(columns, column)
		}
	}
	bound := query
	bound.Page.Cursor = ""
	sources := []string{}
	for _, table := range tables {
		sources = append(sources, table.locator()+table.source.ETag+table.catalog.Signature)
	}
	result.Fingerprint = digest(struct {
		Query   c.QuerySpec
		Sources []string
	}{bound, sources})
	if query.Page.Cursor != "" {
		cursor, err := parseBoundCursor(secret, query.Page.Cursor, false)
		if err != nil {
			return result, err
		}
		if cursor.TenantID != principal.TenantID || cursor.Subject != principal.Subject || cursor.SchemaID != d.ID || cursor.Digest != bareETag(etag) || cursor.Authorization != digest(decision) || cursor.Fingerprint != result.Fingerprint {
			return result, problem(422, "invalid_cursor", "定义、来源结构、权限或查询已改变，请重新查询")
		}
		lastID, err := mappedKeyArgument(primary, cursor.LastID)
		if err != nil {
			return result, problem(422, "invalid_cursor", "游标中的来源主键不合法")
		}
		if result.Sort == nil {
			conditions = append(conditions, idCompare+" > "+idParameter)
			args = append(args, lastID)
		} else {
			property := *result.Sort
			column, _ := mappedFilterColumn(tables)(property)
			value, err := typedValue(property, cursor.LastValue)
			if err != nil {
				return result, problem(422, "invalid_cursor", "游标排序值不合法")
			}
			if value == nil {
				if descending {
					conditions = append(conditions, "("+column+" IS NULL AND "+idCompare+" > "+idParameter+")")
				} else {
					conditions = append(conditions, "("+column+" IS NOT NULL OR ("+column+" IS NULL AND "+idCompare+" > "+idParameter+"))")
				}
				args = append(args, lastID)
			} else {
				op, nulls := ">", ""
				if descending {
					op = "<"
					if property.Nullable {
						nulls = " OR " + column + " IS NULL"
					}
				}
				mark := mappedParameter(primary.source.Driver)(property)
				conditions = append(conditions, "("+column+" "+op+" "+mark+" OR ("+column+" = "+mark+" AND "+idCompare+" > "+idParameter+")"+nulls+")")
				args = append(args, value, value, lastID)
			}
		}
	}
	result.SQL = sourceBind(primary.source.Driver, "SELECT "+strings.Join(columns, ",")+" FROM "+primary.qualified()+" WHERE "+strings.Join(conditions, " AND ")+" ORDER BY "+order+" LIMIT ?")
	result.Args = append(args, query.Page.Limit+1)
	return result, nil
}

func mappedDatabaseID(table *mappedTable, value any) (string, error) {
	column, _ := table.column(table.keyColumn)
	property := c.Property{DataType: column.DataType}
	raw, err := rawDatabaseValue(property, value)
	if err != nil {
		return "", err
	}
	var text string
	if json.Unmarshal(raw, &text) != nil || text == "" || len(text) > 1024 {
		return "", problem(409, "source_drift", "来源主键值无法稳定表示")
	}
	if _, err := mappedKeyArgument(table, text); err != nil {
		return "", err
	}
	return text, nil
}

func mappedRawValue(property c.Property, value any) (json.RawMessage, error) {
	raw, err := rawDatabaseValue(property, value)
	if err != nil {
		return nil, problem(409, "source_drift", "来源值无法转换为声明的属性类型")
	}
	if _, err = typedValue(property, raw); err != nil {
		return nil, problem(409, "source_drift", "来源值超出声明属性的类型或范围")
	}
	return raw, nil
}

func readMappedProjection(ctx context.Context, tables []*mappedTable, compiled mappedCompiledQuery, schemaID string) (c.QueryResult, json.RawMessage, error) {
	result := c.QueryResult{Fields: compiled.Projection, Data: []c.Instance{}}
	primary := tables[0]
	rows, err := primary.db.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return result, nil, problem(422, "source_query_failed", "无法执行来源查询，请核对当前源结构、主键与读取权限")
	}
	var lastValue json.RawMessage
	for rows.Next() {
		if len(result.Data) == compiled.Limit {
			result.HasMore = true
			break
		}
		values, dest := make([]any, len(compiled.ScanFields)+1), make([]any, len(compiled.ScanFields)+1)
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			_ = rows.Close()
			return result, nil, err
		}
		id, err := mappedDatabaseID(primary, values[0])
		if err != nil {
			_ = rows.Close()
			return result, nil, err
		}
		item := c.Instance{ID: id, SchemaID: schemaID, Values: map[string]json.RawMessage{}}
		for _, property := range compiled.Projection {
			item.Values[property.ID] = json.RawMessage("null")
		}
		for i, property := range compiled.ScanFields {
			raw, err := mappedRawValue(property, values[i+1])
			if err != nil {
				_ = rows.Close()
				return result, nil, err
			}
			if compiled.Sort != nil && property.ID == compiled.Sort.ID {
				lastValue = raw
			}
			if _, selected := item.Values[property.ID]; selected {
				item.Values[property.ID] = raw
			}
		}
		result.Data = append(result.Data, item)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return result, nil, err
	}
	if err = rows.Close(); err != nil {
		return result, nil, err
	}
	if len(result.Data) == 0 {
		return result, lastValue, nil
	}
	byID := map[string]*c.Instance{}
	for i := range result.Data {
		byID[result.Data[i].ID] = &result.Data[i]
	}
	for _, table := range tables[1:] {
		properties := []c.Property{}
		for _, selected := range compiled.Projection {
			for _, property := range table.properties {
				if property.ID == selected.ID {
					properties = append(properties, property)
				}
			}
		}
		if len(properties) == 0 {
			continue
		}
		columns := []string{table.quoted(table.keyColumn)}
		for _, property := range properties {
			columns = append(columns, table.quoted(property.Mapping.Column))
		}
		marks, args := []string{}, []any{}
		keyCompare, keyParameter := mappedKeySQL(table)
		for _, item := range result.Data {
			key, err := mappedKeyArgument(table, item.ID)
			if err != nil {
				return result, nil, err
			}
			marks = append(marks, keyParameter)
			args = append(args, key)
		}
		where := keyCompare + " IN (" + strings.Join(marks, ",") + ")"
		if table.tenantColumn != "" {
			where += " AND " + sourceExactStringColumn(table, table.tenantColumn) + " = " + sourceExactStringParameter(table)
			args = append(args, tables[0].source.TenantID)
		}
		statement := sourceBind(table.source.Driver, "SELECT "+strings.Join(columns, ",")+" FROM "+table.qualified()+" WHERE "+where+" LIMIT ?")
		args = append(args, len(result.Data)+1)
		rows, err := table.db.QueryContext(ctx, statement, args...)
		if err != nil {
			return result, nil, problem(422, "source_query_failed", "无法读取关联来源，请核对唯一关联键、租户列和读取权限")
		}
		seen := map[string]bool{}
		for rows.Next() {
			values, dest := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				_ = rows.Close()
				return result, nil, err
			}
			id, err := mappedDatabaseID(table, values[0])
			if err != nil {
				_ = rows.Close()
				return result, nil, err
			}
			if seen[id] {
				_ = rows.Close()
				return result, nil, problem(409, "source_drift", "关联来源返回重复唯一键，不能选择任意一条记录")
			}
			seen[id] = true
			item := byID[id]
			if item == nil {
				continue
			}
			for i, property := range properties {
				raw, err := mappedRawValue(property, values[i+1])
				if err != nil {
					_ = rows.Close()
					return result, nil, err
				}
				item.Values[property.ID] = raw
			}
		}
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			return result, nil, err
		}
		if err = rows.Close(); err != nil {
			return result, nil, err
		}
	}
	for i := range result.Data {
		result.Data[i].ETag = digest(result.Data[i])
	}
	return result, lastValue, nil
}

func (h *handler) queryMapped(ctx context.Context, principal c.Principal, current c.CurrentDefinition, decision c.Decision, query c.QuerySpec, entityID, operation string) (c.QueryResult, error) {
	var result c.QueryResult
	var compiled mappedCompiledQuery
	var lastValue json.RawMessage
	err := h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, principal, current); err != nil {
			return err
		}
		tables, err := h.mappedTables(ctx, current.Definition, false)
		if err != nil {
			return err
		}
		defer closeMappedTables(tables)
		if err = validateMappedCatalog(tables); err != nil {
			return err
		}
		if current.Definition.Schema.Mode == "schema_2_db" {
			for _, table := range tables {
				remaining, err := mappedTablePlan(ctx, current.Definition, tables, table)
				if err != nil {
					return err
				}
				if len(remaining) != 0 {
					return problem(409, "source_drift", "当前来源结构缺少已发布的列或索引，请恢复结构后重试")
				}
			}
		} else if err = validateReverseIndexes(current.Definition, tables); err != nil {
			return err
		}
		compiled, err = compileMappedQuery(current.Definition, tables, current.ETag, principal, decision, query, h.cfg.CursorSecret, entityID)
		if err != nil {
			return err
		}
		result, lastValue, err = readMappedProjection(ctx, tables, compiled, current.Definition.ID)
		if err != nil {
			return err
		}
		return h.recheck(ctx, principal, operation, current.Definition.ID, decision)
	})
	if err != nil {
		return result, err
	}
	if result.HasMore && len(result.Data) > 0 {
		result.NextCursor, err = signCursor(h.cfg.CursorSecret, pageCursor{Fingerprint: compiled.Fingerprint, TenantID: principal.TenantID, Subject: principal.Subject, SchemaID: current.Definition.ID, Digest: current.ETag, Authorization: digest(decision), LastID: result.Data[len(result.Data)-1].ID, LastValue: lastValue, Expires: time.Now().Add(time.Hour).Unix()})
	}
	return result, err
}
