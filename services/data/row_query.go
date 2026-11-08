package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const maxRowQueryJoins = 4

type rowQueryNode struct {
	node                         c.RowQueryNode
	current                      c.CurrentDefinition
	definitionDecision, decision c.Decision
	table                        *mappedTable
}

// mappedTable.managed describes forward DDL ownership, which also applies to
// schema_2_db tables in explicit sources. Only an absent source binding selects
// the platform database, generated column names and platform instance IDs.
func (n rowQueryNode) platformStorage() bool {
	return n.current.Definition.Schema.StorageBinding == nil
}

type rowQueryEdge struct {
	join                         c.RowQueryJoin
	current                      c.CurrentDefinition
	definitionDecision, decision c.Decision
	plan                         *linkPlan
}
type rowQueryPlan struct {
	nodes                                []rowQueryNode
	edges                                []rowQueryEdge
	driver                               string
	definitions, authorization, physical string
}

func (p *rowQueryPlan) close() {
	for _, edge := range p.edges {
		edge.plan.close()
	}
}

func validateRowQueryShape(q c.QuerySpec) error {
	if q.Aggregate != nil {
		return problem(422, "aggregate_unsupported", "首批聚合不能混用关联行查询")
	}
	if q.Rows == nil || q.Via != nil || len(q.Rows.Joins) < 1 || len(q.Rows.Joins) > maxRowQueryJoins || len(q.Rows.Nodes) != len(q.Rows.Joins)+1 {
		return problem(422, "invalid_row_query", "关联行查询需要1到4条INNER Link和对应对象节点，不能混用固定实例导航")
	}
	if q.Page.Limit < 1 || q.Page.Limit > 100 || len(q.Select) < 1 || len(q.Select) > 100 || len(q.OrderBy) > 1 {
		return problem(422, "budget_exceeded", "需显式投影、每页1..100行且至多一个排序字段")
	}
	aliases := map[string]bool{}
	for i, node := range q.Rows.Nodes {
		alias := nameKey(node.Alias)
		if node.ID != fmt.Sprintf("n%d", i) || !uuidPattern.MatchString(node.SchemaID) || alias == "" || utf8.RuneCountInString(node.Alias) > 128 || strings.IndexFunc(node.Alias, unicode.IsControl) >= 0 || aliases[alias] {
			return problem(422, "invalid_row_query", "对象节点须依次为n0..n4，具有稳定Schema标识及不重复的有效别名")
		}
		aliases[alias] = true
	}
	for i, join := range q.Rows.Joins {
		validFrom := false
		for j := 0; j <= i; j++ {
			validFrom = validFrom || join.From == q.Rows.Nodes[j].ID
		}
		if !validFrom || join.To != q.Rows.Nodes[i+1].ID || !uuidPattern.MatchString(join.LinkID) || (join.Direction != "out" && join.Direction != "in") {
			return problem(422, "invalid_row_query", "每个INNER Link须从已有节点按明确方向关联一个新节点")
		}
	}
	return nil
}

func (h *handler) planRowQuery(ctx context.Context, p c.Principal, q c.QuerySpec) (*rowQueryPlan, error) {
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
	plan := &rowQueryPlan{driver: "mysql"}
	fail := func(err error) (*rowQueryPlan, error) { plan.close(); return nil, err }
	for i, node := range q.Rows.Nodes {
		current := root
		if i > 0 {
			current, err = h.current(ctx, p.TenantID, node.SchemaID)
			if err != nil {
				return fail(err)
			}
		}
		if current.Definition.ID != node.SchemaID {
			return fail(problem(503, "metadata_unavailable", "对象定义的权威标识不一致"))
		}
		if err = validateSchema(current.Definition); err != nil {
			return fail(err)
		}
		definitionDecision, e := h.authorize(ctx, p, "definition.read", node.SchemaID)
		if e != nil {
			return fail(e)
		}
		if definitionDecision.RowFilter != nil || definitionDecision.NewRowFilter != nil {
			return fail(problem(422, "unsupported_authorization", "定义发现不支持行条件授权"))
		}
		decision, e := h.authorize(ctx, p, "instance.query", node.SchemaID)
		if e != nil {
			return fail(e)
		}
		plan.nodes = append(plan.nodes, rowQueryNode{node: node, current: current, definitionDecision: definitionDecision, decision: decision})
	}
	for i, join := range q.Rows.Joins {
		current, e := h.currentLink(ctx, p, join.LinkID)
		if e != nil {
			return fail(e)
		}
		definitionDecision, e := h.authorizeKind(ctx, p, "definition.read", join.LinkID, c.KindLink)
		if e != nil {
			return fail(e)
		}
		if definitionDecision.RowFilter != nil || definitionDecision.NewRowFilter != nil {
			return fail(problem(422, "unsupported_authorization", "关系定义发现不支持行条件授权"))
		}
		decision, e := h.authorizeLink(ctx, p, "link.query", join.LinkID)
		if e != nil {
			return fail(e)
		}
		physical, e := h.planLink(ctx, current.Definition)
		if e != nil {
			return fail(e)
		}
		plan.edges = append(plan.edges, rowQueryEdge{join: join, current: current, definitionDecision: definitionDecision, decision: decision, plan: physical})
		fromIndex := 0
		for j := range plan.nodes {
			if plan.nodes[j].node.ID == join.From {
				fromIndex = j
				break
			}
		}
		origin, target, e := linkDirection(physical, join.Direction)
		if e != nil {
			return fail(e)
		}
		if origin.Definition.ID != plan.nodes[fromIndex].node.SchemaID || target.Definition.ID != plan.nodes[i+1].node.SchemaID {
			return fail(problem(422, "invalid_link_endpoint", "关联方向或关系端点与对象节点不一致"))
		}
		if origin.ETag != plan.nodes[fromIndex].current.ETag || target.ETag != plan.nodes[i+1].current.ETag || c.EnumContextDigest(origin.Definition) != c.EnumContextDigest(plan.nodes[fromIndex].current.Definition) || c.EnumContextDigest(target.Definition) != c.EnumContextDigest(plan.nodes[i+1].current.Definition) {
			return fail(problem(409, "definition_changed", "关联对象定义已经改变，请重新查询"))
		}
		if current.Definition.Link.StorageBinding.Mode == "managed" {
			for _, index := range []int{fromIndex, i + 1} {
				if plan.nodes[index].table == nil {
					plan.nodes[index].table = &mappedTable{managed: true, source: c.DataSource{Driver: "mysql"}, name: tableName(plan.nodes[index].node.SchemaID), keyColumn: "id", tenantColumn: "tenant_id"}
				}
			}
		} else {
			fromTables, toTables := physical.sourceTables, physical.targetTables
			if join.Direction == "in" {
				fromTables, toTables = toTables, fromTables
			}
			if plan.nodes[fromIndex].table == nil {
				plan.nodes[fromIndex].table = fromTables[0]
			}
			plan.nodes[i+1].table = toTables[0]
		}
	}
	primary := plan.nodes[0].table
	plan.driver = primary.source.Driver
	for _, node := range plan.nodes {
		if node.table == nil || node.platformStorage() != plan.nodes[0].platformStorage() || node.table.source.ID != primary.source.ID || node.table.source.Database != primary.source.Database || node.table.source.Driver != primary.source.Driver {
			return fail(problem(422, "cross_source_link_unsupported", "关联行查询仅支持同一数据源和数据库，不能混用平台表或跨数据源"))
		}
		if node.table.source.ETag != primary.source.ETag {
			return fail(problem(409, "source_changed", "关联数据源配置已改变，请重新查询"))
		}
	}
	definitions, authorization, physical := []any{}, []any{}, []string{}
	for _, node := range plan.nodes {
		definitions = append(definitions, node.current.Definition.ID+node.current.ETag+c.EnumContextDigest(node.current.Definition))
		authorization = append(authorization, node.definitionDecision, node.decision)
	}
	for _, edge := range plan.edges {
		definitions = append(definitions, edge.current.Definition.ID+edge.current.ETag)
		authorization = append(authorization, edge.definitionDecision, edge.decision)
		for _, table := range append(append([]*mappedTable{}, edge.plan.sourceTables...), edge.plan.targetTables...) {
			physical = append(physical, table.locator()+table.source.ETag+table.catalog.Signature+sourceVersionSignature(table.source))
		}
		if edge.plan.junction != nil {
			physical = append(physical, edge.plan.junction.locator()+edge.plan.junction.catalog.Signature+sourceVersionSignature(edge.plan.junction.source))
		}
	}
	plan.definitions, plan.authorization, plan.physical = digest(definitions), digest(authorization), digest(physical)
	return plan, nil
}

func rowNodeByID(plan *rowQueryPlan, id string) *rowQueryNode {
	for i := range plan.nodes {
		if plan.nodes[i].node.ID == id {
			return &plan.nodes[i]
		}
	}
	return nil
}
func rowTableSQL(node rowQueryNode) string {
	if node.platformStorage() {
		return quote(node.table.name)
	}
	return node.table.qualified()
}
func rowColumn(node rowQueryNode, column string) string {
	return node.table.quoted(node.node.ID) + "." + node.table.quoted(column)
}
func rowKey(node rowQueryNode) (string, string) {
	column := rowColumn(node, node.table.keyColumn)
	if node.platformStorage() {
		return column, "?"
	}
	key, _ := node.table.column(node.table.keyColumn)
	if sourceKeyType(key) == "string" {
		return linkExactColumn(node.table, column, key), sourceExactStringParameter(node.table)
	}
	if node.table.source.Driver == "postgresql" {
		return "CAST(" + column + " AS bigint)", "CAST(? AS bigint)"
	}
	return column, "?"
}
func rowFieldColumn(node rowQueryNode, property c.Property, comparison bool) (string, error) {
	column := columnName(property.ID)
	if !node.platformStorage() {
		if !primarySQLProperty(node.current.Definition, property) {
			return "", problem(422, "row_projection_unsupported", "关联行查询当前只支持各对象的主表属性；跨表或跨源属性须单模型读取")
		}
		column = property.Mapping.Column
	}
	result := rowColumn(node, column)
	if comparison && (property.DataType == "string" || property.DataType == "text" || property.DataType == "longtext" || property.DataType == "uuid") {
		if node.table.source.Driver == "mysql" {
			result = "BINARY " + result
		} else {
			result = "CAST(" + result + " AS text) COLLATE \"C\""
		}
	}
	return result, nil
}

type rowCompiledQuery struct {
	SQL                string
	Args               []any
	Fields, ScanFields []c.Property
	Sort               *c.Property
	Fingerprint        string
	Limit              int
}
type rowCursorValues struct {
	Keys []string        `json:"keys"`
	Sort json.RawMessage `json:"sort,omitempty"`
}

func rowKeyArguments(plan *rowQueryPlan, keys []string) ([]any, error) {
	if len(keys) != len(plan.nodes) {
		return nil, problem(422, "invalid_cursor", "关联游标的对象身份数量不匹配")
	}
	args := []any{}
	for i, node := range plan.nodes {
		if node.platformStorage() {
			if !uuidPattern.MatchString(keys[i]) {
				return nil, problem(422, "invalid_cursor", "关联游标对象标识不合法")
			}
			args = append(args, keys[i])
		} else {
			arg, err := mappedKeyArgument(node.table, keys[i])
			if err != nil {
				return nil, problem(422, "invalid_cursor", "关联游标主键不合法")
			}
			args = append(args, arg)
		}
	}
	return args, nil
}
func rowKeysAfter(plan *rowQueryPlan, keyArgs []any) (string, []any) {
	parts, args := []string{}, []any{}
	for i, node := range plan.nodes {
		terms := []string{}
		for j := 0; j < i; j++ {
			column, mark := rowKey(plan.nodes[j])
			terms = append(terms, column+"="+mark)
			args = append(args, keyArgs[j])
		}
		column, mark := rowKey(node)
		terms = append(terms, column+">"+mark)
		args = append(args, keyArgs[i])
		parts = append(parts, "("+strings.Join(terms, " AND ")+")")
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func compileRowQuery(plan *rowQueryPlan, p c.Principal, q c.QuerySpec, secret string) (rowCompiledQuery, error) {
	compiled := rowCompiledQuery{Limit: q.Page.Limit}
	allProperties, allowed := []c.Property{}, []string{}
	properties := map[string]c.Property{}
	owners := map[string]rowQueryNode{}
	for _, node := range plan.nodes {
		for _, property := range node.current.Definition.Schema.Properties {
			id := node.node.ID + "." + property.ID
			copy := property
			copy.ID = id
			copy.Name = node.node.Alias + "." + property.Name
			allProperties = append(allProperties, copy)
			properties[id] = copy
			owners[id] = node
			if fieldAllowed(node.decision.ReadFields, property.ID) {
				allowed = append(allowed, id)
			}
		}
	}
	columnFor := func(property c.Property, comparison bool) (string, error) {
		node, ok := owners[property.ID]
		if !ok {
			return "", problem(422, "unknown_field", "关联字段引用未知对象")
		}
		original := property
		original.ID = strings.TrimPrefix(property.ID, node.node.ID+".")
		return rowFieldColumn(node, original, comparison)
	}
	columns := []string{}
	for _, node := range plan.nodes {
		columns = append(columns, rowColumn(node, node.table.keyColumn))
	}
	seen := map[string]bool{}
	for _, id := range q.Select {
		property, ok := properties[id]
		if !ok {
			return compiled, problem(422, "unknown_field", "关联投影字段不存在")
		}
		if seen[id] {
			return compiled, problem(422, "duplicate_field", "关联投影字段重复")
		}
		if !fieldAllowed(allowed, id) {
			return compiled, problem(403, "forbidden_field", "无权读取关联属性")
		}
		column, err := columnFor(property, false)
		if err != nil {
			return compiled, err
		}
		seen[id] = true
		compiled.Fields = append(compiled.Fields, property)
		compiled.ScanFields = append(compiled.ScanFields, property)
		columns = append(columns, column)
	}
	compiler := expressionCompiler{schema: &c.Schema{Properties: allProperties}, allowed: allowed, column: func(property c.Property) (string, error) { return columnFor(property, true) }, parameter: mappedParameter(plan.driver)}
	user, userArgs, err := compiler.compile(q.Where, 0)
	if err != nil {
		return compiled, err
	}
	conditions, args := []string{"(" + user + ")"}, userArgs
	for _, node := range plan.nodes {
		if node.table.tenantColumn != "" {
			column := rowColumn(node, node.table.tenantColumn)
			mark := "?"
			if !node.platformStorage() {
				column = linkExactColumn(node.table, column, c.SourceColumn{DataType: "string"})
				mark = sourceExactStringParameter(node.table)
			}
			conditions = append(conditions, column+"="+mark)
			args = append(args, p.TenantID)
		}
		authCompiler := expressionCompiler{schema: node.current.Definition.Schema, allowed: []string{"*"}, column: func(property c.Property) (string, error) { return rowFieldColumn(node, property, true) }, parameter: mappedParameter(plan.driver)}
		condition, authArgs, e := authCompiler.compile(node.decision.RowFilter, 0)
		if e != nil {
			return compiled, e
		}
		conditions = append(conditions, "("+condition+")")
		args = append(args, authArgs...)
	}
	from := rowTableSQL(plan.nodes[0]) + " AS " + plan.nodes[0].table.quoted(plan.nodes[0].node.ID)
	for _, edge := range plan.edges {
		origin, target := *rowNodeByID(plan, edge.join.From), *rowNodeByID(plan, edge.join.To)
		originKey, _ := rowKey(origin)
		targetKey, _ := rowKey(target)
		binding := edge.current.Definition.Link.StorageBinding
		if binding.Mode == "managed" || binding.Mode == "junction" {
			edgeAlias := "e" + target.node.ID
			fromColumn, toColumn := "source_id", "target_id"
			tableName := quote(linkTableName(edge.current.Definition.ID))
			quoteID := origin.table.quoted
			tenantColumn := "tenant_id"
			if binding.Mode == "junction" {
				fromColumn, toColumn = binding.SourceColumn, binding.TargetColumn
				tableName = edge.plan.junction.qualified()
				tenantColumn = edge.plan.junction.tenantColumn
			}
			if edge.join.Direction == "in" {
				fromColumn, toColumn = toColumn, fromColumn
			}
			fromEdge := quoteID(edgeAlias) + "." + quoteID(fromColumn)
			toEdge := quoteID(edgeAlias) + "." + quoteID(toColumn)
			if binding.Mode == "junction" {
				originKeyColumn, _ := origin.table.column(origin.table.keyColumn)
				targetKeyColumn, _ := target.table.column(target.table.keyColumn)
				fromEdge = linkExactColumn(origin.table, fromEdge, originKeyColumn)
				toEdge = linkExactColumn(target.table, toEdge, targetKeyColumn)
			}
			from += " JOIN " + tableName + " AS " + quoteID(edgeAlias) + " ON " + fromEdge + "=" + originKey
			from += " JOIN " + rowTableSQL(target) + " AS " + quoteID(target.node.ID) + " ON " + toEdge + "=" + targetKey
			if tenantColumn != "" {
				column := quoteID(edgeAlias) + "." + quoteID(tenantColumn)
				mark := "?"
				if binding.Mode == "junction" {
					column = linkExactColumn(origin.table, column, c.SourceColumn{DataType: "string"})
					mark = sourceExactStringParameter(origin.table)
				}
				conditions = append(conditions, column+"="+mark)
				args = append(args, p.TenantID)
			}
		} else {
			holder, referenced := origin, target
			originHolds := (binding.ForeignKeySide == "source" && edge.join.Direction == "out") || (binding.ForeignKeySide == "target" && edge.join.Direction == "in")
			if !originHolds {
				holder, referenced = target, origin
			}
			key, _ := referenced.table.column(referenced.table.keyColumn)
			foreign := linkExactColumn(holder.table, rowColumn(holder, binding.ForeignKeyColumn), key)
			reference, _ := rowKey(referenced)
			from += " JOIN " + rowTableSQL(target) + " AS " + target.table.quoted(target.node.ID) + " ON " + foreign + "=" + reference
		}
	}
	orderParts := []string{}
	descending := false
	if len(q.OrderBy) > 0 {
		order := q.OrderBy[0]
		property, ok := properties[order.Field]
		if !ok {
			return compiled, problem(422, "unknown_field", "关联排序属性不存在")
		}
		if !fieldAllowed(allowed, property.ID) {
			return compiled, problem(403, "forbidden_field", "无权使用关联属性排序")
		}
		if property.DataType == "text" || property.DataType == "longtext" {
			return compiled, problem(422, "unsupported_sort", "关联查询不支持text/longtext排序")
		}
		direction := strings.ToLower(order.Direction)
		if direction != "asc" && direction != "desc" {
			return compiled, problem(422, "invalid_order", "排序方向须为asc或desc")
		}
		descending = direction == "desc"
		column, e := columnFor(property, true)
		if e != nil {
			return compiled, e
		}
		compiled.Sort = &property
		orderText := column + " " + strings.ToUpper(direction)
		if plan.driver == "postgresql" {
			if descending {
				orderText += " NULLS LAST"
			} else {
				orderText += " NULLS FIRST"
			}
		}
		orderParts = append(orderParts, orderText)
		if !seen[property.ID] {
			compiled.ScanFields = append(compiled.ScanFields, property)
			scanColumn, e := columnFor(property, false)
			if e != nil {
				return compiled, e
			}
			columns = append(columns, scanColumn)
		}
	}
	for _, node := range plan.nodes {
		column, _ := rowKey(node)
		orderParts = append(orderParts, column+" ASC")
	}
	bound := q
	bound.Page.Cursor = ""
	compiled.Fingerprint = digest(struct {
		Query    c.QuerySpec
		Physical string
	}{bound, plan.physical})
	if q.Page.Cursor != "" {
		cursor, e := parseBoundCursor(secret, q.Page.Cursor, false)
		if e != nil {
			return compiled, e
		}
		if cursor.Fingerprint != compiled.Fingerprint || cursor.TenantID != p.TenantID || cursor.Subject != p.Subject || cursor.SchemaID != plan.nodes[0].node.SchemaID || cursor.Digest != plan.definitions || cursor.Authorization != plan.authorization {
			return compiled, problem(422, "invalid_cursor", "关联查询、定义、来源结构或授权已改变，请重新运行")
		}
		var last rowCursorValues
		if json.Unmarshal(cursor.LastValue, &last) != nil {
			return compiled, problem(422, "invalid_cursor", "关联游标内容不合法")
		}
		keyArgs, e := rowKeyArguments(plan, last.Keys)
		if e != nil {
			return compiled, e
		}
		after, afterArgs := rowKeysAfter(plan, keyArgs)
		if compiled.Sort == nil {
			conditions = append(conditions, after)
			args = append(args, afterArgs...)
		} else {
			property := *compiled.Sort
			value, e := typedValue(property, last.Sort)
			if e != nil {
				return compiled, problem(422, "invalid_cursor", "关联游标排序值不合法")
			}
			column, e := columnFor(property, true)
			if e != nil {
				return compiled, e
			}
			if value == nil {
				if descending {
					conditions = append(conditions, "("+column+" IS NULL AND "+after+")")
				} else {
					conditions = append(conditions, "("+column+" IS NOT NULL OR ("+column+" IS NULL AND "+after+"))")
				}
				args = append(args, afterArgs...)
			} else {
				op, nulls := ">", ""
				if descending {
					op = "<"
					if property.Nullable {
						nulls = " OR " + column + " IS NULL"
					}
				}
				mark := mappedParameter(plan.driver)(property)
				conditions = append(conditions, "("+column+op+mark+" OR ("+column+"="+mark+" AND "+after+")"+nulls+")")
				args = append(args, value, value)
				args = append(args, afterArgs...)
			}
		}
	}
	compiled.SQL = sourceBind(plan.driver, "SELECT "+strings.Join(columns, ",")+" FROM "+from+" WHERE "+strings.Join(conditions, " AND ")+" ORDER BY "+strings.Join(orderParts, ",")+" LIMIT ?")
	compiled.Args = append(args, compiled.Limit+1)
	compiled.Fields = safeQueryProperties(compiled.Fields)
	return compiled, nil
}

func (h *handler) rowQueryGates(ctx context.Context, tx *sql.Tx, p c.Principal, plan *rowQueryPlan) error {
	primary := plan.nodes[0].table
	if !plan.nodes[0].platformStorage() {
		var etag string
		if err := tx.QueryRowContext(ctx, "SELECT etag FROM af_data_sources WHERE tenant_id=? AND id=? FOR UPDATE", p.TenantID, primary.source.ID).Scan(&etag); err != nil {
			return err
		}
		if etag != primary.source.ETag {
			return problem(409, "source_changed", "关联数据源配置已改变，请重新查询")
		}
	}
	for _, currents := range [][]c.CurrentDefinition{func() []c.CurrentDefinition {
		out := []c.CurrentDefinition{}
		for _, node := range plan.nodes {
			out = append(out, node.current)
		}
		return out
	}(), func() []c.CurrentDefinition {
		out := []c.CurrentDefinition{}
		for _, edge := range plan.edges {
			out = append(out, edge.current)
		}
		return out
	}()} {
		sort.Slice(currents, func(i, j int) bool { return currents[i].Definition.ID < currents[j].Definition.ID })
		previous := ""
		for _, current := range currents {
			if current.Definition.ID == previous {
				continue
			}
			previous = current.Definition.ID
			if err := h.gate(ctx, tx, p, current); err != nil {
				return err
			}
		}
	}
	return nil
}
func (h *handler) recheckRowQuery(ctx context.Context, p c.Principal, plan *rowQueryPlan) error {
	for _, node := range plan.nodes {
		current, err := h.current(ctx, p.TenantID, node.node.SchemaID)
		if err != nil {
			return err
		}
		if current.ETag != node.current.ETag || c.EnumContextDigest(current.Definition) != c.EnumContextDigest(node.current.Definition) {
			return problem(409, "definition_changed", "关联对象定义已改变，请重新查询")
		}
		if err := h.recheck(ctx, p, "definition.read", node.node.SchemaID, node.definitionDecision); err != nil {
			return err
		}
		if err := h.recheck(ctx, p, "instance.query", node.node.SchemaID, node.decision); err != nil {
			return err
		}
	}
	for _, edge := range plan.edges {
		current, err := h.currentLink(ctx, p, edge.current.Definition.ID)
		if err != nil {
			return err
		}
		if current.ETag != edge.current.ETag {
			return problem(409, "definition_changed", "关联定义已改变，请重新查询")
		}
		decision, err := h.authorizeKind(ctx, p, "definition.read", edge.current.Definition.ID, c.KindLink)
		if err != nil {
			return err
		}
		if digest(decision) != digest(edge.definitionDecision) {
			return problem(409, "authorization_changed", "关联定义发现授权已改变")
		}
		if err := h.recheckLink(ctx, p, "link.query", edge.current.Definition.ID, edge.decision); err != nil {
			return err
		}
	}
	for _, node := range plan.nodes {
		if err := h.recheckEnums(ctx, node.current); err != nil {
			return err
		}
	}
	return nil
}

type rowQueryRunner interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readRowQuery(ctx context.Context, runner rowQueryRunner, plan *rowQueryPlan, compiled rowCompiledQuery) (c.QueryResult, rowCursorValues, error) {
	result := c.QueryResult{RowMode: true, Data: []c.Instance{}, Rows: []c.QueryRow{}, Fields: compiled.Fields}
	last := rowCursorValues{}
	budget, err := newQueryBudget(safeQueryProperties(compiled.Fields))
	if err != nil {
		return result, last, err
	}
	rows, err := runner.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return result, last, problem(422, "source_query_failed", "无法执行关联行查询，请核对源结构及数据库读取权限")
	}
	defer rows.Close()
	for rows.Next() {
		if len(result.Rows) == compiled.Limit {
			result.HasMore = true
			break
		}
		values, dest := make([]any, len(plan.nodes)+len(compiled.ScanFields)), make([]any, len(plan.nodes)+len(compiled.ScanFields))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return result, last, err
		}
		if err = budget.checkDatabaseRow(values); err != nil {
			return result, last, err
		}
		item := c.QueryRow{Values: map[string]json.RawMessage{}, Entities: []c.QueryRowEntity{}}
		keys := []string{}
		for i, node := range plan.nodes {
			var id string
			if node.platformStorage() {
				id = dbString(values[i])
				if !uuidPattern.MatchString(id) {
					return result, last, problem(409, "source_drift", "平台对象标识无法稳定表示")
				}
			} else {
				id, err = mappedDatabaseID(node.table, values[i])
				if err != nil {
					return result, last, err
				}
			}
			keys = append(keys, id)
			item.Entities = append(item.Entities, c.QueryRowEntity{NodeID: node.node.ID, SchemaID: node.node.SchemaID, InstanceID: id})
		}
		last = rowCursorValues{Keys: keys}
		item.ID = digest(item.Entities)
		for i, property := range compiled.ScanFields {
			raw, e := mappedRawValue(property, values[len(plan.nodes)+i])
			if e != nil {
				return result, last, e
			}
			if compiled.Sort != nil && compiled.Sort.ID == property.ID {
				last.Sort = raw
			}
			for _, selected := range compiled.Fields {
				if selected.ID == property.ID {
					item.Values[property.ID] = raw
					break
				}
			}
		}
		if err = budget.add(item); err != nil {
			return result, last, err
		}
		result.Rows = append(result.Rows, item)
	}
	if err = rows.Err(); err != nil {
		return result, last, err
	}
	if err = rows.Close(); err != nil {
		return result, last, err
	}
	return result, last, nil
}

func (h *handler) executeRowQuery(ctx context.Context, p c.Principal, q c.QuerySpec) (c.QueryResult, error) {
	plan, err := h.planRowQuery(ctx, p, q)
	if err != nil {
		return c.QueryResult{}, err
	}
	defer plan.close()
	compiled, err := compileRowQuery(plan, p, q, h.cfg.CursorSecret)
	if err != nil {
		return c.QueryResult{}, err
	}
	var result c.QueryResult
	var last rowCursorValues
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.rowQueryGates(ctx, tx, p, plan); err != nil {
			return err
		}
		var runner rowQueryRunner = tx
		if !plan.nodes[0].platformStorage() {
			runner = plan.nodes[0].table.db
		}
		var err error
		result, last, err = readRowQuery(ctx, runner, plan, compiled)
		if err != nil {
			return err
		}
		return h.recheckRowQuery(ctx, p, plan)
	})
	if err != nil {
		return c.QueryResult{}, err
	}
	if result.HasMore && len(result.Rows) > 0 {
		value, _ := json.Marshal(last)
		result.NextCursor, err = signCursor(h.cfg.CursorSecret, pageCursor{Fingerprint: compiled.Fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: plan.nodes[0].node.SchemaID, Digest: plan.definitions, Authorization: plan.authorization, LastID: result.Rows[len(result.Rows)-1].ID, LastValue: value, Expires: time.Now().Add(time.Hour).Unix()})
	}
	return result, err
}
