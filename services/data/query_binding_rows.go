package data

import (
	"context"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Resolve stable row scopes without opening a source, catalog or business DB.
// Physical readiness, source versions and publication gates remain execution
// checks. Reuse rowQueryPlan's definition/decision snapshots for delivery checks.
func (h *handler) resolveRowBinding(ctx context.Context, p c.Principal, q c.QuerySpec) (*rowQueryPlan, error) {
	if err := validateRowQueryShape(q); err != nil {
		return nil, err
	}
	root, err := h.resolve(ctx, p, q.From)
	if err != nil {
		return nil, err
	}
	if root.Definition.ID != q.Rows.Nodes[0].SchemaID {
		return nil, problem(422, "invalid_row_query", "关联行根节点与FROM模型不一致")
	}
	plan := &rowQueryPlan{}
	for i, node := range q.Rows.Nodes {
		current := root
		if i > 0 {
			current, err = h.current(ctx, p.TenantID, node.SchemaID)
			if err != nil {
				return nil, err
			}
		}
		if current.Definition.ID != node.SchemaID {
			return nil, problem(503, "metadata_unavailable", "对象定义的权威标识不一致")
		}
		if err := validateSchema(current.Definition); err != nil {
			return nil, err
		}
		discovery, err := h.authorize(ctx, p, "definition.read", node.SchemaID)
		if err != nil {
			return nil, err
		}
		if discovery.RowFilter != nil || discovery.NewRowFilter != nil {
			return nil, problem(422, "unsupported_authorization", "定义发现不支持行条件授权")
		}
		decision, err := h.authorize(ctx, p, "instance.query", node.SchemaID)
		if err != nil {
			return nil, err
		}
		if err := validateOntologySQLDecision(current.Definition, decision); err != nil {
			return nil, err
		}
		plan.nodes = append(plan.nodes, rowQueryNode{node: node, current: current, definitionDecision: discovery, decision: decision})
	}
	for _, join := range q.Rows.Joins {
		current, err := h.currentLink(ctx, p, join.LinkID)
		if err != nil {
			return nil, err
		}
		discovery, err := h.authorizeKind(ctx, p, "definition.read", join.LinkID, c.KindLink)
		if err != nil {
			return nil, err
		}
		if discovery.RowFilter != nil || discovery.NewRowFilter != nil {
			return nil, problem(422, "unsupported_authorization", "关系定义发现不支持行条件授权")
		}
		decision, err := h.authorizeLink(ctx, p, "link.query", join.LinkID)
		if err != nil {
			return nil, err
		}
		if err := validateLinkDefinition(current.Definition); err != nil {
			return nil, err
		}
		origin, target := rowNodeByID(plan, join.From), rowNodeByID(plan, join.To)
		link := current.Definition.Link
		from, to := link.SourceSchemaID, link.TargetSchemaID
		if join.Direction == "in" {
			from, to = to, from
		}
		if origin.node.SchemaID != from || target.node.SchemaID != to {
			return nil, problem(422, "invalid_link_endpoint", "关联方向或关系端点与对象节点不一致")
		}
		sb, tb, lb := origin.current.Definition.Schema.StorageBinding, target.current.Definition.Schema.StorageBinding, link.StorageBinding
		if lb.Mode == "managed" {
			if sb != nil || tb != nil {
				return nil, problem(422, "unsupported_link_mapping", "平台关联表仅支持平台默认存储对象")
			}
		} else if sb == nil || tb == nil || sb.DataSourceID != tb.DataSourceID || sb.DataSourceID != lb.DataSourceID {
			return nil, problem(422, "cross_source_link_unsupported", "关联查询仅支持同一数据源中的主表和关系")
		}
		plan.edges = append(plan.edges, rowQueryEdge{join: join, current: current, definitionDecision: discovery, decision: decision})
	}
	return plan, nil
}

func rowBindingSchema(plan *rowQueryPlan) *c.Schema {
	schema := &c.Schema{}
	for _, node := range plan.nodes {
		for _, property := range node.current.Definition.Schema.Properties {
			if leftRowTarget(plan, node.node.ID) {
				property.Required, property.Nullable = false, true
			}
			property.ID = node.node.ID + "." + property.ID
			property.Name = node.node.Alias + "." + property.Name
			schema.Properties = append(schema.Properties, property)
		}
	}
	return schema
}

func validateRowBinding(plan *rowQueryPlan, q c.QuerySpec) ([]c.Property, error) {
	schema := rowBindingSchema(plan)
	properties, allowed := map[string]c.Property{}, []string{}
	for _, node := range plan.nodes {
		for _, property := range node.current.Definition.Schema.Properties {
			id := node.node.ID + "." + property.ID
			properties[id] = property
			if fieldAllowed(node.decision.ReadFields, property.ID) {
				allowed = append(allowed, id)
			}
		}
	}
	// Only stable node.field IDs are accepted. Names are labels, never fallback
	// bindings, including in filters and ordering after a model rename.
	field := func(id string) (c.Property, error) {
		property, exists := properties[id]
		if !exists {
			return c.Property{}, problem(422, "unknown_field", "关联字段须使用对象节点和稳定属性ID")
		}
		if !fieldAllowed(allowed, id) {
			return c.Property{}, problem(403, "forbidden_field", "无权使用关联属性")
		}
		node := rowNodeByID(plan, strings.SplitN(id, ".", 2)[0])
		if !primarySQLProperty(node.current.Definition, property) {
			return c.Property{}, problem(422, "row_projection_unsupported", "关联查询仅支持各对象的主表属性")
		}
		return property, nil
	}
	fields, seen := []c.Property{}, map[string]bool{}
	for _, id := range q.Select {
		property, err := field(id)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, problem(422, "duplicate_field", "关联投影字段重复")
		}
		seen[id] = true
		node := rowNodeByID(plan, strings.SplitN(id, ".", 2)[0])
		property.ID, property.Name = id, node.node.Alias+"."+property.Name
		fields = append(fields, property)
	}
	compiler := expressionCompiler{schema: schema, allowed: allowed, column: func(property c.Property) (string, error) {
		_, err := field(property.ID)
		return "logical_field", err
	}}
	nodes := 0
	var stableFields func(*c.Expression, int) error
	stableFields = func(e *c.Expression, depth int) error {
		if e == nil {
			return nil
		}
		nodes++
		if nodes > 100 || depth > 10 {
			return problem(422, "budget_exceeded", "查询条件超过节点或嵌套预算")
		}
		if e.Field != "" {
			if _, err := field(e.Field); err != nil {
				return err
			}
		}
		for i := range e.Args {
			if err := stableFields(&e.Args[i], depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := stableFields(q.Where, 0); err != nil {
		return nil, err
	}
	if _, _, err := compiler.compile(q.Where, 0); err != nil {
		return nil, err
	}
	if q.Aggregate != nil {
		measures, err := rowCountFields(q)
		if err != nil {
			return nil, err
		}
		for _, id := range q.Aggregate.GroupBy {
			property, err := field(id)
			if err != nil {
				return nil, err
			}
			if seen[id] || property.DataType == "text" || property.DataType == "longtext" {
				return nil, problem(422, "invalid_row_aggregate", "分组字段必须唯一且支持分组")
			}
			seen[id] = true
			node := rowNodeByID(plan, strings.SplitN(id, ".", 2)[0])
			property.ID, property.Name = id, node.node.Alias+"."+property.Name
			if leftRowTarget(plan, node.node.ID) {
				property.Required, property.Nullable = false, true
			}
			fields = append(fields, property)
		}
		return append(fields, measures...), nil
	}
	for _, order := range q.OrderBy {
		property, err := field(order.Field)
		if err != nil {
			return nil, err
		}
		if property.DataType == "text" || property.DataType == "longtext" {
			return nil, problem(422, "unsupported_sort", "不支持text/longtext排序")
		}
		if order.Direction != "asc" && order.Direction != "desc" {
			return nil, problem(422, "invalid_order", "排序方向必须为asc或desc")
		}
	}
	return fields, nil
}

func (h *handler) compileRowBinding(ctx context.Context, p c.Principal, q c.QuerySpec) (logicalQueryBinding, error) {
	plan, err := h.resolveRowBinding(ctx, p, q)
	if err != nil {
		return logicalQueryBinding{}, err
	}
	q.From = c.Source{SchemaID: plan.nodes[0].node.SchemaID}
	for i := range q.OrderBy {
		q.OrderBy[i].Direction = strings.ToLower(q.OrderBy[i].Direction)
	}
	fields, err := validateRowBinding(plan, q)
	if err != nil {
		return logicalQueryBinding{}, err
	}
	return logicalQueryBinding{QueryBinding: c.QueryBinding{Query: q, Fields: safeQueryProperties(fields)}, rows: plan}, nil
}
