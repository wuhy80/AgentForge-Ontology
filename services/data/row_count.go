package data

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func rowCountFields(q c.QuerySpec) ([]c.Property, error) {
	a := q.Aggregate
	if len(a.GroupBy) > c.MaxAggregateGroupFields || len(q.Select) != 0 || len(q.OrderBy) != 0 ||
		len(a.Measures) == 0 || len(a.Measures) > c.MaxAggregateMeasures {
		return nil, problem(422, "unsupported_row_aggregate", "关联计数需要空投影且最多三个分组字段，按分组字段排序")
	}
	fields := make([]c.Property, 0, len(a.Measures))
	seen := map[string]bool{}
	for _, m := range a.Measures {
		if !aggregateMeasureID.MatchString(m.ID) || seen[m.ID] || len(m.Name) > 128 ||
			(m.Op != "count" && m.Op != "countDistinct") || (m.Op == "countDistinct" && m.Field == "") {
			return nil, problem(422, "invalid_row_aggregate", "关联计数需要唯一指标 ID 和有效计数操作")
		}
		if m.Field != "" {
			found := false
			for _, node := range q.Rows.Nodes {
				found = found || node.ID == m.Field
			}
			if !found {
				return nil, problem(422, "invalid_row_aggregate", "关联计数的字段必须是对象节点 ID")
			}
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		fields = append(fields, c.Property{ID: m.ID, Name: name, DataType: "int64", ReadOnly: true})
		seen[m.ID] = true
	}
	return fields, nil
}

func readRowCounts(ctx context.Context, runner rowQueryRunner, compiled rowCompiledQuery) (c.QueryResult, error) {
	rows, err := runner.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return c.QueryResult{}, err
	}
	defer rows.Close()
	out := c.QueryResult{Data: []c.Instance{}, Fields: compiled.Fields, AggregateMode: true, AggregateRows: []c.AggregateRow{}}
	budget, err := newQueryBudget(safeQueryProperties(compiled.Fields))
	if err != nil {
		return c.QueryResult{}, err
	}
	for rows.Next() {
		if len(out.AggregateRows) >= c.MaxAggregateGroups {
			return c.QueryResult{}, problem(422, "aggregate_group_budget", "关联计数超过分组预算，请缩小查询范围")
		}
		values, dest := make([]any, len(compiled.Fields)), make([]any, len(compiled.Fields))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return c.QueryResult{}, err
		}
		if err := budget.checkDatabaseRow(values); err != nil {
			return c.QueryResult{}, err
		}
		item := c.AggregateRow{Values: make(map[string]json.RawMessage, len(values))}
		for i, field := range compiled.Fields {
			raw, err := mappedRawValue(field, values[i])
			if err != nil {
				return c.QueryResult{}, err
			}
			item.Values[field.ID] = raw
		}
		if err := budget.add(item); err != nil {
			return c.QueryResult{}, err
		}
		out.AggregateRows = append(out.AggregateRows, item)
	}
	return out, rows.Err()
}

func rowCountOffset(p c.Principal, plan *rowQueryPlan, token, fingerprint, secret string) (int, error) {
	if token == "" {
		return 0, nil
	}
	cursor, err := parseBoundCursor(secret, token, false)
	if err != nil {
		return 0, err
	}
	start, err := strconv.Atoi(cursor.LastID)
	if err != nil || start < 1 || start > c.MaxAggregateGroups || cursor.TenantID != p.TenantID ||
		cursor.Subject != p.Subject || cursor.SchemaID != plan.nodes[0].node.SchemaID || cursor.Digest != plan.definitions ||
		cursor.Authorization != plan.authorization || cursor.Fingerprint != fingerprint || len(cursor.LastValue) != 0 {
		return 0, problem(422, "invalid_cursor", "关联统计条件或授权已改变，请重新运行")
	}
	return start, nil
}

func pageRowCounts(p c.Principal, plan *rowQueryPlan, compiled rowCompiledQuery, secret string, result c.QueryResult) (c.QueryResult, error) {
	start := min(compiled.Offset, len(result.AggregateRows))
	end := min(start+compiled.Limit, len(result.AggregateRows))
	result.HasMore = end < len(result.AggregateRows)
	result.AggregateRows = result.AggregateRows[start:end]
	if !result.HasMore {
		return result, nil
	}
	var err error
	result.NextCursor, err = signCursor(secret, pageCursor{Fingerprint: compiled.Fingerprint, TenantID: p.TenantID,
		Subject: p.Subject, SchemaID: plan.nodes[0].node.SchemaID, Digest: plan.definitions, Authorization: plan.authorization,
		LastID: strconv.Itoa(end), Expires: time.Now().Add(15 * time.Minute).Unix()})
	return result, err
}
