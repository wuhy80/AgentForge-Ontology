package data

import (
	"encoding/json"
	"fmt"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func bindAggregateOntologySQL(s *ontologySQLStatement, d c.Definition, parameters map[string]json.RawMessage) c.OntologySQLAnalysis {
	b := ontologySQLBinder{statement: s, definition: d, parameters: parameters, parameterIndex: map[string]int{}, analysis: c.OntologySQLAnalysis{Diagnostics: []c.SQLDiagnostic{}, Parameters: []c.SQLParameter{}, Fields: []c.Property{}}}
	if d.Schema == nil {
		b.fail("source_unavailable", "模型不存在或当前不可查询", s.SourceRange)
		return b.analysis
	}
	if len(s.Joins) > 0 {
		b.fail("aggregate_unsupported", "聚合首批不支持JOIN LINK", s.SourceRange)
		return b.analysis
	}
	q := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Select: []string{}, Aggregate: &c.AggregateQuery{GroupBy: []string{}, Measures: []c.AggregateMeasure{}}, Page: c.Page{Limit: s.Limit}}
	if len(parameters) > 64 {
		b.fail("budget_exceeded", "参数不能超过64个", s.SourceRange)
	}
	if len(s.Projection) != len(s.GroupBy) {
		b.fail("aggregate_group_mismatch", "SELECT中的普通字段须与GROUP BY逐项一致", s.SourceRange)
	}
	for i, f := range s.GroupBy {
		p, found := b.field(f)
		if !found {
			continue
		}
		q.Aggregate.GroupBy = append(q.Aggregate.GroupBy, p.ID)
		if i < len(s.Projection) {
			projected, ok := b.field(s.Projection[i])
			if ok && projected.ID != p.ID {
				b.fail("aggregate_group_mismatch", "SELECT中的普通字段须与GROUP BY逐项一致", s.Projection[i].Range)
			}
		}
	}
	for i, m := range s.Measures {
		measure := c.AggregateMeasure{ID: fmt.Sprintf("m%d", i), Op: m.Op, Name: m.Name}
		if m.Field != nil {
			p, found := b.field(*m.Field)
			if !found {
				continue
			}
			measure.Field = p.ID
		}
		q.Aggregate.Measures = append(q.Aggregate.Measures, measure)
	}
	q.Where = b.expression(s.Where, 0)
	if s.Order != nil {
		if s.Order.Qualifier != "" && nameKey(s.Order.Qualifier) != nameKey(s.Alias) {
			b.fail("unknown_qualifier", "聚合排序限定名与FROM别名不一致", s.Order.Range)
		}
		q.OrderBy = []c.Order{{Field: s.Order.Name, Direction: s.Direction}}
	}
	// The supplied Schema contains only query-authorized fields. A wildcard
	// here cannot reintroduce a hidden name or any physical field metadata.
	bound, fields, _, err := bindAggregateQuery(d, c.Decision{ReadFields: []string{"*"}}, q)
	if err != nil {
		if e, ok := err.(*apiError); ok {
			b.fail(e.code, e.message, s.SourceRange)
		} else {
			b.fail("invalid_aggregate", "聚合结构无效", s.SourceRange)
		}
	} else {
		q = bound
		b.analysis.Fields = safeQueryProperties(fields)
	}
	for name := range parameters {
		if _, ok := b.parameterIndex[name]; !ok {
			b.fail("unused_parameter", "提供了查询未使用的参数："+name, s.SourceRange)
		}
	}
	b.analysis.Ready = len(b.analysis.Diagnostics) == 0 && !b.missing
	if b.analysis.Ready {
		b.analysis.Query = &q
	}
	return b.analysis
}
