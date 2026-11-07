package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) registerLinks(mux *http.ServeMux) {
	mux.HandleFunc("POST /data/links/{linkId}/instances", h.createLinkInstance)
	mux.HandleFunc("GET /data/links/{linkId}/instances", h.listLinkInstances)
	mux.HandleFunc("DELETE /data/links/{linkId}/instances/{edgeId}", h.deleteLinkInstance)
}

func (h *handler) authorizeLink(ctx context.Context, p c.Principal, operation, id string) (c.Decision, error) {
	decision, err := h.authorizeKind(ctx, p, operation, id, c.KindLink)
	if err != nil {
		return decision, err
	}
	if decision.RowFilter != nil || decision.NewRowFilter != nil {
		return decision, problem(422, "unsupported_link_policy", "当前 Link 仅支持整关系静态授权，不能忽略关系行条件")
	}
	for _, fields := range [][]string{decision.ReadFields, decision.WriteFields} {
		for _, field := range fields {
			if field != "*" {
				return decision, problem(422, "unsupported_link_policy", "当前 Link 不支持关系属性权限")
			}
		}
	}
	return decision, nil
}

func (h *handler) recheckLink(ctx context.Context, p c.Principal, operation, id string, previous c.Decision) error {
	now, err := h.authorizeLink(ctx, p, operation, id)
	if err != nil {
		return err
	}
	if digest(now) != digest(previous) {
		return problem(409, "authorization_changed", "关系授权已改变，请重新提交")
	}
	return nil
}

func (h *handler) currentLink(ctx context.Context, p c.Principal, id string) (c.CurrentDefinition, error) {
	current, err := h.current(ctx, p.TenantID, id)
	if err != nil {
		return current, err
	}
	if err = validateLinkDefinition(current.Definition); err != nil {
		return current, err
	}
	return current, nil
}

func linkDirection(plan *linkPlan, direction string) (c.CurrentDefinition, c.CurrentDefinition, error) {
	if direction == "out" {
		return plan.source, plan.target, nil
	}
	if direction == "in" {
		return plan.target, plan.source, nil
	}
	return c.CurrentDefinition{}, c.CurrentDefinition{}, problem(422, "invalid_link_direction", "关系查询方向必须为 out 或 in")
}

func linkBoundDecision(target, origin, link c.Decision, plan *linkPlan, current c.CurrentDefinition) c.Decision {
	bound := target
	physical := []string{}
	for _, table := range append(append([]*mappedTable(nil), plan.sourceTables...), plan.targetTables...) {
		physical = append(physical, table.locator()+table.source.ETag+table.catalog.Signature+sourceVersionSignature(table.source))
	}
	if plan.junction != nil {
		physical = append(physical, plan.junction.locator()+plan.junction.catalog.Signature+sourceVersionSignature(plan.junction.source))
	}
	bound.State = digest(struct {
		Target, Origin, Link                   c.Decision
		LinkDigest, SourceDigest, TargetDigest string
		Physical                               []string
	}{target, origin, link, current.ETag, plan.source.ETag, plan.target.ETag, physical})
	return bound
}

func linkAliasColumn(table *mappedTable, alias, name string) string {
	return table.quoted(alias) + "." + table.quoted(name)
}
func linkExactColumn(table *mappedTable, column string, key c.SourceColumn) string {
	if sourceKeyType(key) == "string" {
		if table.source.Driver == "mysql" {
			return "BINARY " + column
		}
		return "convert_to(CAST(" + column + " AS text),'UTF8')"
	}
	return column
}
func linkAliasFilter(tables []*mappedTable, alias string) func(c.Property) (string, error) {
	return func(property c.Property) (string, error) {
		column, err := mappedFilterColumn(tables)(property)
		if err != nil {
			return "", err
		}
		return strings.ReplaceAll(column, tables[0].quoted(property.Mapping.Column), linkAliasColumn(tables[0], alias, property.Mapping.Column)), nil
	}
}

func managedLinkPredicate(plan *linkPlan, direction, anchor string, originDecision c.Decision) (sqlPredicate, error) {
	origin, target, err := linkDirection(plan, direction)
	if err != nil {
		return sqlPredicate{}, err
	}
	fromCol, toCol := "source_id", "target_id"
	if direction == "in" {
		fromCol, toCol = toCol, fromCol
	}
	compiler := expressionCompiler{schema: origin.Definition.Schema, allowed: []string{"*"}, column: func(property c.Property) (string, error) {
		return "`af_link_origin`." + quote(columnName(property.ID)), nil
	}}
	condition, args, err := compiler.compile(originDecision.RowFilter, 0)
	if err != nil {
		return sqlPredicate{}, err
	}
	sql := "EXISTS (SELECT 1 FROM " + quote(linkTableName(plan.definition.ID)) + " AS `af_link_edge` JOIN " + quote(tableName(origin.Definition.ID)) + " AS `af_link_origin` ON `af_link_origin`.`tenant_id`=`af_link_edge`.`tenant_id` AND `af_link_origin`.`id`=`af_link_edge`." + quote(fromCol) + " WHERE `af_link_edge`.`tenant_id`=? AND `af_link_edge`." + quote(fromCol) + "=? AND `af_link_edge`." + quote(toCol) + "=" + quote(tableName(target.Definition.ID)) + ".`id` AND (" + condition + "))"
	return sqlPredicate{SQL: sql, Args: append([]any{plan.definition.TenantID, anchor}, args...)}, nil
}

func externalLinkPredicate(plan *linkPlan, direction, anchor string, originDecision c.Decision) (sqlPredicate, error) {
	originTables, targetTables := plan.sourceTables, plan.targetTables
	if direction == "in" {
		originTables, targetTables = targetTables, originTables
	} else if direction != "out" {
		return sqlPredicate{}, problem(422, "invalid_link_direction", "关系方向必须为out或in")
	}
	origin, target := originTables[0], targetTables[0]
	compiler := expressionCompiler{schema: plan.source.Definition.Schema, allowed: []string{"*"}, column: linkAliasFilter(originTables, "af_link_origin"), parameter: mappedParameter(origin.source.Driver)}
	if direction == "in" {
		compiler.schema = plan.target.Definition.Schema
	}
	condition, authArgs, err := compiler.compile(originDecision.RowFilter, 0)
	if err != nil {
		return sqlPredicate{}, err
	}
	keyArg, err := mappedKeyArgument(origin, anchor)
	if err != nil {
		return sqlPredicate{}, err
	}
	originKey, _ := origin.column(origin.keyColumn)
	targetKey, _ := target.column(target.keyColumn)
	originID := linkExactColumn(origin, linkAliasColumn(origin, "af_link_origin", origin.keyColumn), originKey)
	targetID := linkExactColumn(target, target.qualified()+"."+target.quoted(target.keyColumn), targetKey)
	_, mark := mappedKeySQL(origin)
	conditions := []string{originID + "=" + mark, "(" + condition + ")"}
	args := append([]any{keyArg}, authArgs...)
	if origin.tenantColumn != "" {
		conditions = append(conditions, linkExactColumn(origin, linkAliasColumn(origin, "af_link_origin", origin.tenantColumn), c.SourceColumn{DataType: "string"})+"="+sourceExactStringParameter(origin))
		args = append(args, plan.definition.TenantID)
	}
	from := origin.qualified() + " AS " + origin.quoted("af_link_origin")
	b := plan.definition.Link.StorageBinding
	if b.Mode == "junction" {
		j := plan.junction
		originCol, targetCol := b.SourceColumn, b.TargetColumn
		if direction == "in" {
			originCol, targetCol = targetCol, originCol
		}
		from = j.qualified() + " AS " + j.quoted("af_link_edge") + " JOIN " + from + " ON " + linkExactColumn(j, linkAliasColumn(j, "af_link_edge", originCol), originKey) + "=" + originID
		conditions = append(conditions, linkExactColumn(j, linkAliasColumn(j, "af_link_edge", targetCol), targetKey)+"="+targetID)
		if j.tenantColumn != "" {
			conditions = append(conditions, linkExactColumn(j, linkAliasColumn(j, "af_link_edge", j.tenantColumn), c.SourceColumn{DataType: "string"})+"="+sourceExactStringParameter(j))
			args = append(args, plan.definition.TenantID)
		}
	} else {
		originHolds := (b.ForeignKeySide == "source" && direction == "out") || (b.ForeignKeySide == "target" && direction == "in")
		if originHolds {
			conditions = append(conditions, linkExactColumn(origin, linkAliasColumn(origin, "af_link_origin", b.ForeignKeyColumn), targetKey)+"="+targetID)
		} else {
			conditions = append(conditions, linkExactColumn(target, target.qualified()+"."+target.quoted(b.ForeignKeyColumn), originKey)+"="+originID)
		}
	}
	return sqlPredicate{SQL: "EXISTS (SELECT 1 FROM " + from + " WHERE " + strings.Join(conditions, " AND ") + ")", Args: args}, nil
}

func (h *handler) queryLink(ctx context.Context, p c.Principal, target c.CurrentDefinition, targetDecision c.Decision, query c.QuerySpec) (c.QueryResult, error) {
	var result c.QueryResult
	via := query.Via
	if via == nil || !uuidPattern.MatchString(via.LinkID) || via.InstanceID == "" || len(via.InstanceID) > 1024 {
		return result, problem(422, "invalid_link_query", "关系查询需要有效的关系、方向和起点实例")
	}
	current, err := h.currentLink(ctx, p, via.LinkID)
	if err != nil {
		return result, err
	}
	linkDecision, err := h.authorizeLink(ctx, p, "link.query", current.Definition.ID)
	if err != nil {
		return result, err
	}
	plan, err := h.planLink(ctx, current.Definition)
	if err != nil {
		return result, err
	}
	defer plan.close()
	origin, returned, err := linkDirection(plan, via.Direction)
	if err != nil {
		return result, err
	}
	if returned.Definition.ID != target.Definition.ID || returned.ETag != target.ETag {
		return result, problem(422, "invalid_link_endpoint", "查询模型必须是当前方向的返回端对象模型")
	}
	originDecision, err := h.authorize(ctx, p, "instance.read", origin.Definition.ID)
	if err != nil {
		return result, err
	}
	bound := linkBoundDecision(targetDecision, originDecision, linkDecision, plan, current)
	var fingerprint string
	var lastValue json.RawMessage
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.linkGates(ctx, tx, p, plan, &current); err != nil {
			return err
		}
		if current.Definition.Link.StorageBinding.Mode == "managed" {
			if !uuidPattern.MatchString(via.InstanceID) {
				return problem(422, "invalid_link_query", "平台对象实例标识必须为UUID")
			}
			if _, err := selectInstance(ctx, tx, origin.Definition, p.TenantID, via.InstanceID, originDecision.RowFilter, false); err != nil {
				return err
			}
			predicate, err := managedLinkPredicate(plan, via.Direction, via.InstanceID, originDecision)
			if err != nil {
				return err
			}
			compiled, err := compileQueryPredicate(target.Definition, target.ETag, p, bound, query, h.cfg.CursorSecret, &predicate)
			if err != nil {
				return err
			}
			fingerprint = compiled.Fingerprint
			result, lastValue, err = readLocalProjection(ctx, tx, compiled, target.Definition.ID)
			if err != nil {
				return err
			}
		} else {
			originTables, returnedTables := plan.sourceTables, plan.targetTables
			if via.Direction == "in" {
				originTables, returnedTables = returnedTables, originTables
			}
			anchorQuery := c.QuerySpec{Page: c.Page{Limit: 1}}
			anchor, err := compileMappedQuery(origin.Definition, originTables, origin.ETag, p, originDecision, anchorQuery, h.cfg.CursorSecret, via.InstanceID)
			if err != nil {
				return err
			}
			var id any
			if err = originTables[0].db.QueryRowContext(ctx, anchor.SQL, anchor.Args...).Scan(&id); errors.Is(err, sql.ErrNoRows) {
				return problem(404, "not_found", "起点实例不存在或不可访问")
			} else if err != nil {
				return err
			}
			predicate, err := externalLinkPredicate(plan, via.Direction, via.InstanceID, originDecision)
			if err != nil {
				return err
			}
			compiled, err := compileMappedQueryPredicate(target.Definition, returnedTables, target.ETag, p, bound, query, h.cfg.CursorSecret, "", &predicate)
			if err != nil {
				return err
			}
			fingerprint = compiled.Fingerprint
			result, lastValue, err = readMappedProjection(ctx, returnedTables, compiled, target.Definition.ID)
			if err != nil {
				return err
			}
		}
		if err := h.recheckLink(ctx, p, "link.query", current.Definition.ID, linkDecision); err != nil {
			return err
		}
		if err := h.recheck(ctx, p, "instance.read", origin.Definition.ID, originDecision); err != nil {
			return err
		}
		return h.recheck(ctx, p, "instance.query", target.Definition.ID, targetDecision)
	})
	if err != nil {
		return result, err
	}
	if result.HasMore && len(result.Data) > 0 {
		result.NextCursor, err = signCursor(h.cfg.CursorSecret, pageCursor{Fingerprint: fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: target.Definition.ID, Digest: target.ETag, Authorization: digest(bound), LastID: result.Data[len(result.Data)-1].ID, LastValue: lastValue, Expires: time.Now().Add(time.Hour).Unix()})
	}
	return result, err
}

func readLocalProjection(ctx context.Context, tx *sql.Tx, compiled compiledQuery, schemaID string) (c.QueryResult, json.RawMessage, error) {
	result := c.QueryResult{Data: []c.Instance{}, Fields: compiled.Projection}
	var lastValue json.RawMessage
	rows, err := tx.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return result, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]any, len(compiled.ScanFields)+2)
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return result, nil, err
		}
		if len(result.Data) == compiled.Limit {
			result.HasMore = true
			break
		}
		item := c.Instance{ID: dbString(values[0]), ETag: dbString(values[1]), SchemaID: schemaID, Values: map[string]json.RawMessage{}}
		for i, property := range compiled.ScanFields {
			raw, err := rawDatabaseValue(property, values[i+2])
			if err != nil {
				return result, nil, err
			}
			if compiled.Sort != nil && compiled.Sort.ID == property.ID {
				lastValue = raw
			}
			for _, selected := range compiled.Projection {
				if selected.ID == property.ID {
					item.Values[property.ID] = raw
					break
				}
			}
		}
		result.Data = append(result.Data, item)
	}
	if err = rows.Err(); err != nil {
		return result, nil, err
	}
	if err = rows.Close(); err != nil {
		return result, nil, err
	}
	return result, lastValue, nil
}

func (h *handler) createLinkInstance(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var request c.LinkWriteRequest
	if err = decode(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	if !uuidPattern.MatchString(request.SourceID) || !uuidPattern.MatchString(request.TargetID) {
		writeError(w, problem(422, "invalid_link_instance", "关系两端必须为有效平台实例标识"))
		return
	}
	current, err := h.currentLink(r.Context(), p, r.PathValue("linkId"))
	if err != nil {
		writeError(w, err)
		return
	}
	decision, err := h.authorizeLink(r.Context(), p, "link.create", current.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if current.Definition.Link.StorageBinding.Mode != "managed" {
		writeError(w, problem(422, "mapped_link_writes_unsupported", "外部关系映射只开放查询，不能通过平台修改来源关系"))
		return
	}
	plan, err := h.planLink(r.Context(), current.Definition)
	if err != nil {
		writeError(w, err)
		return
	}
	defer plan.close()
	sourceDecision, err := h.authorize(r.Context(), p, "instance.read", plan.source.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	targetDecision, err := h.authorize(r.Context(), p, "instance.read", plan.target.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	out := c.LinkInstance{ID: c.NewID(), LinkID: current.Definition.ID, SourceID: request.SourceID, TargetID: request.TargetID, ETag: c.NewID()}
	err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.linkGates(r.Context(), tx, p, plan, &current); err != nil {
			return err
		}
		if _, err := selectInstance(r.Context(), tx, plan.source.Definition, p.TenantID, request.SourceID, sourceDecision.RowFilter, true); err != nil {
			return err
		}
		if _, err := selectInstance(r.Context(), tx, plan.target.Definition, p.TenantID, request.TargetID, targetDecision.RowFilter, true); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO "+quote(linkTableName(current.Definition.ID))+" (tenant_id,id,source_id,target_id,etag) VALUES (?,?,?,?,?)", p.TenantID, out.ID, out.SourceID, out.TargetID, out.ETag); err != nil {
			return err
		}
		if err := emitLink(r.Context(), tx, p.TenantID, "link.created", out); err != nil {
			return err
		}
		if err := h.recheckLink(r.Context(), p, "link.create", current.Definition.ID, decision); err != nil {
			return err
		}
		if err := h.recheck(r.Context(), p, "instance.read", plan.source.Definition.ID, sourceDecision); err != nil {
			return err
		}
		return h.recheck(r.Context(), p, "instance.read", plan.target.Definition.ID, targetDecision)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+out.ETag+`"`)
	writeJSON(w, 201, out)
}

func (h *handler) deleteLinkInstance(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	header := r.Header.Get("If-Match")
	etag := bareETag(header)
	if len(header) != 38 || header != `"`+etag+`"` || !uuidPattern.MatchString(etag) {
		writeError(w, problem(428, "precondition_required", "删除关系需要读取时返回的If-Match"))
		return
	}
	current, err := h.currentLink(r.Context(), p, r.PathValue("linkId"))
	if err != nil {
		writeError(w, err)
		return
	}
	decision, err := h.authorizeLink(r.Context(), p, "link.delete", current.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if current.Definition.Link.StorageBinding.Mode != "managed" {
		writeError(w, problem(422, "mapped_link_writes_unsupported", "外部关系映射只开放查询"))
		return
	}
	if !uuidPattern.MatchString(r.PathValue("edgeId")) {
		writeError(w, problem(404, "not_found", "关系实例不存在或不可访问"))
		return
	}
	plan, err := h.planLink(r.Context(), current.Definition)
	if err != nil {
		writeError(w, err)
		return
	}
	defer plan.close()
	sourceDecision, err := h.authorize(r.Context(), p, "instance.read", plan.source.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	targetDecision, err := h.authorize(r.Context(), p, "instance.read", plan.target.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.linkGates(r.Context(), tx, p, plan, &current); err != nil {
			return err
		}
		out := c.LinkInstance{LinkID: current.Definition.ID}
		err := tx.QueryRowContext(r.Context(), "SELECT id,source_id,target_id,etag FROM "+quote(linkTableName(current.Definition.ID))+" WHERE tenant_id=? AND id=? FOR UPDATE", p.TenantID, r.PathValue("edgeId")).Scan(&out.ID, &out.SourceID, &out.TargetID, &out.ETag)
		if errors.Is(err, sql.ErrNoRows) {
			return problem(404, "not_found", "关系实例不存在或不可访问")
		}
		if err != nil {
			return err
		}
		if _, err := selectInstance(r.Context(), tx, plan.source.Definition, p.TenantID, out.SourceID, sourceDecision.RowFilter, true); err != nil {
			return err
		}
		if _, err := selectInstance(r.Context(), tx, plan.target.Definition, p.TenantID, out.TargetID, targetDecision.RowFilter, true); err != nil {
			return err
		}
		if out.ETag != etag {
			return problem(412, "precondition_failed", "关系实例已改变，请重新读取")
		}
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM "+quote(linkTableName(current.Definition.ID))+" WHERE tenant_id=? AND id=? AND etag=?", p.TenantID, out.ID, etag); err != nil {
			return err
		}
		if err := emitLink(r.Context(), tx, p.TenantID, "link.deleted", out); err != nil {
			return err
		}
		if err := h.recheckLink(r.Context(), p, "link.delete", current.Definition.ID, decision); err != nil {
			return err
		}
		if err := h.recheck(r.Context(), p, "instance.read", plan.source.Definition.ID, sourceDecision); err != nil {
			return err
		}
		return h.recheck(r.Context(), p, "instance.read", plan.target.Definition.ID, targetDecision)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(204)
}

func emitLink(ctx context.Context, tx *sql.Tx, tenant, event string, instance c.LinkInstance) error {
	payload, _ := json.Marshal(instance)
	_, err := tx.ExecContext(ctx, "INSERT INTO af_data_outbox (id,tenant_id,schema_id,entity_id,event_type,payload) VALUES (?,?,?,?,?,?)", c.NewID(), tenant, instance.LinkID, instance.ID, event, string(payload))
	return err
}

func (h *handler) listLinkInstances(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	parameters := r.URL.Query()
	for key, values := range parameters {
		if (key != "direction" && key != "instanceId" && key != "limit" && key != "cursor") || len(values) != 1 {
			writeError(w, problem(422, "invalid_link_query", "关系列表包含未知或重复参数"))
			return
		}
	}
	direction, anchor := parameters.Get("direction"), parameters.Get("instanceId")
	limit, err := strconv.Atoi(parameters.Get("limit"))
	if err != nil || limit < 1 || limit > 100 || !uuidPattern.MatchString(anchor) {
		writeError(w, problem(422, "invalid_link_query", "关系列表需要实例标识与1到100的分页大小"))
		return
	}
	current, err := h.currentLink(r.Context(), p, r.PathValue("linkId"))
	if err != nil {
		writeError(w, err)
		return
	}
	decision, err := h.authorizeLink(r.Context(), p, "link.query", current.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if current.Definition.Link.StorageBinding.Mode != "managed" {
		writeError(w, problem(422, "mapped_link_instances_unsupported", "外部关系只提供对象导航查询，不提供平台关系实例列表"))
		return
	}
	plan, err := h.planLink(r.Context(), current.Definition)
	if err != nil {
		writeError(w, err)
		return
	}
	defer plan.close()
	origin, target, err := linkDirection(plan, direction)
	if err != nil {
		writeError(w, err)
		return
	}
	originDecision, err := h.authorize(r.Context(), p, "instance.read", origin.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	targetDecision, err := h.authorize(r.Context(), p, "instance.query", target.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	bound := linkBoundDecision(targetDecision, originDecision, decision, plan, current)
	fingerprint := digest(struct {
		Direction, Anchor string
		Limit             int
	}{direction, anchor, limit})
	lastID := ""
	if token := parameters.Get("cursor"); token != "" {
		cursor, e := parseCursor(h.cfg.CursorSecret, token)
		if e != nil {
			writeError(w, e)
			return
		}
		if cursor.TenantID != p.TenantID || cursor.Subject != p.Subject || cursor.SchemaID != current.Definition.ID || cursor.Digest != current.ETag || cursor.Authorization != digest(bound) || cursor.Fingerprint != fingerprint {
			writeError(w, problem(422, "invalid_cursor", "关系定义、查询或权限已改变"))
			return
		}
		lastID = cursor.LastID
	}
	out := struct {
		Data       []c.LinkInstance `json:"data"`
		NextCursor string           `json:"nextCursor,omitempty"`
		HasMore    bool             `json:"hasMore"`
	}{Data: []c.LinkInstance{}}
	err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.linkGates(r.Context(), tx, p, plan, &current); err != nil {
			return err
		}
		if _, err := selectInstance(r.Context(), tx, origin.Definition, p.TenantID, anchor, originDecision.RowFilter, false); err != nil {
			return err
		}
		fromCol, toCol := "source_id", "target_id"
		if direction == "in" {
			fromCol, toCol = toCol, fromCol
		}
		originCompiler := expressionCompiler{schema: origin.Definition.Schema, allowed: []string{"*"}, column: func(property c.Property) (string, error) {
			return "`af_link_origin`." + quote(columnName(property.ID)), nil
		}}
		targetCompiler := expressionCompiler{schema: target.Definition.Schema, allowed: []string{"*"}, column: func(property c.Property) (string, error) {
			return "`af_link_target`." + quote(columnName(property.ID)), nil
		}}
		originSQL, originArgs, err := originCompiler.compile(originDecision.RowFilter, 0)
		if err != nil {
			return err
		}
		targetSQL, targetArgs, err := targetCompiler.compile(targetDecision.RowFilter, 0)
		if err != nil {
			return err
		}
		statement := "SELECT e.id,e.source_id,e.target_id,e.etag FROM " + quote(linkTableName(current.Definition.ID)) + " AS e JOIN " + quote(tableName(origin.Definition.ID)) + " AS `af_link_origin` ON `af_link_origin`.tenant_id=e.tenant_id AND `af_link_origin`.id=e." + quote(fromCol) + " JOIN " + quote(tableName(target.Definition.ID)) + " AS `af_link_target` ON `af_link_target`.tenant_id=e.tenant_id AND `af_link_target`.id=e." + quote(toCol) + " WHERE e.tenant_id=? AND e." + quote(fromCol) + "=? AND (" + originSQL + ") AND (" + targetSQL + ")"
		args := append([]any{p.TenantID, anchor}, originArgs...)
		args = append(args, targetArgs...)
		if lastID != "" {
			statement += " AND e.id>?"
			args = append(args, lastID)
		}
		statement += " ORDER BY e.id ASC LIMIT ?"
		args = append(args, limit+1)
		rows, err := tx.QueryContext(r.Context(), statement, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			edge := c.LinkInstance{LinkID: current.Definition.ID}
			if err = rows.Scan(&edge.ID, &edge.SourceID, &edge.TargetID, &edge.ETag); err != nil {
				rows.Close()
				return err
			}
			if len(out.Data) == limit {
				out.HasMore = true
				break
			}
			out.Data = append(out.Data, edge)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err = rows.Close(); err != nil {
			return err
		}
		if err = h.recheckLink(r.Context(), p, "link.query", current.Definition.ID, decision); err != nil {
			return err
		}
		if err = h.recheck(r.Context(), p, "instance.read", origin.Definition.ID, originDecision); err != nil {
			return err
		}
		return h.recheck(r.Context(), p, "instance.query", target.Definition.ID, targetDecision)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if out.HasMore && len(out.Data) > 0 {
		out.NextCursor, err = signCursor(h.cfg.CursorSecret, pageCursor{Fingerprint: fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: current.Definition.ID, Digest: current.ETag, Authorization: digest(bound), LastID: out.Data[len(out.Data)-1].ID, Expires: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			writeError(w, err)
			return
		}
	}
	writeJSON(w, 200, out)
}
