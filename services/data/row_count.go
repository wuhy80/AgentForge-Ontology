package data

import (
	"context"
	"encoding/json"
	"strconv"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func rowCountFields(q c.QuerySpec) ([]c.Property, error) {
	a := q.Aggregate
	if len(a.GroupBy) != 0 || len(q.Select) != 0 || len(q.OrderBy) != 0 || q.Page.Cursor != "" ||
		len(a.Measures) == 0 || len(a.Measures) > c.MaxAggregateMeasures {
		return nil, problem(422, "unsupported_row_aggregate", "关联计数需要空投影，当前只支持不分组的计数")
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
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return c.QueryResult{}, err
		}
		return c.QueryResult{}, problem(503, "invalid_row_aggregate", "关联计数没有返回汇总结果")
	}
	counts, dest := make([]int64, len(compiled.Fields)), make([]any, len(compiled.Fields))
	for i := range counts {
		dest[i] = &counts[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return c.QueryResult{}, err
	}
	if err := rows.Close(); err != nil {
		return c.QueryResult{}, err
	}
	values := make(map[string]json.RawMessage, len(counts))
	for i, count := range counts {
		values[compiled.Fields[i].ID] = json.RawMessage(strconv.Quote(strconv.FormatInt(count, 10)))
	}
	return c.QueryResult{Data: []c.Instance{}, Fields: compiled.Fields, AggregateMode: true,
		AggregateRows: []c.AggregateRow{{Values: values}}}, nil
}
