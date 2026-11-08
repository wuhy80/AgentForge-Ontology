package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) recheck(ctx context.Context, p c.Principal, operation, id string, previous c.Decision) error {
	now, err := h.authorize(ctx, p, operation, id)
	if err != nil {
		return err
	}
	if digest(now) != digest(previous) {
		return problem(409, "authorization_changed", "授权已改变，请重新提交")
	}
	return nil
}
func projection(s *c.Schema, allowed []string) []c.Property {
	out := []c.Property{}
	for _, f := range s.Properties {
		if fieldAllowed(allowed, f.ID) {
			out = append(out, f)
		}
	}
	return out
}
func projectInstance(in c.Instance, allowed []string) c.Instance {
	out := in
	out.Values = map[string]json.RawMessage{}
	for id, v := range in.Values {
		if fieldAllowed(allowed, id) {
			out.Values[id] = v
		}
	}
	return out
}

func selectInstance(ctx context.Context, tx *sql.Tx, d c.Definition, tenant, id string, filter *c.Expression, lock bool) (c.Instance, error) {
	out := c.Instance{SchemaID: d.ID, Values: map[string]json.RawMessage{}}
	compiler := expressionCompiler{schema: d.Schema, allowed: []string{"*"}}
	condition, args, err := compiler.compile(filter, 0)
	if err != nil {
		return out, err
	}
	columns := []string{"`id`", "`etag`"}
	for _, p := range d.Schema.Properties {
		columns = append(columns, quote(columnName(p.ID)))
	}
	query := "SELECT " + strings.Join(columns, ",") + " FROM " + quote(tableName(d.ID)) + " WHERE `tenant_id`=? AND `id`=? AND (" + condition + ")"
	if lock {
		query += " FOR UPDATE"
	}
	allArgs := []any{tenant, id}
	allArgs = append(allArgs, args...)
	values := make([]any, len(columns))
	dest := make([]any, len(columns))
	for i := range values {
		dest[i] = &values[i]
	}
	err = tx.QueryRowContext(ctx, query, allArgs...).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return out, problem(404, "not_found", "实例不存在或不可访问")
	}
	if err != nil {
		return out, err
	}
	budget, err := newQueryBudget(nil)
	if err != nil {
		return out, err
	}
	if err = budget.checkDatabaseRow(values); err != nil {
		return out, err
	}
	out.ID = dbString(values[0])
	out.ETag = dbString(values[1])
	for i, p := range d.Schema.Properties {
		raw, e := rawDatabaseValue(p, values[i+2])
		if e != nil {
			return out, e
		}
		out.Values[p.ID] = raw
	}
	if err = budget.add(out); err != nil {
		return out, err
	}
	return out, nil
}
func dbString(v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	}
	return ""
}

func buildValues(s *c.Schema, input map[string]json.RawMessage, base map[string]json.RawMessage, allowed []string, creating bool) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for k, v := range base {
		out[k] = v
	}
	bound := map[string]bool{}
	for key, raw := range input {
		p, ok := findProperty(s, key)
		if !ok {
			return nil, problem(422, "unknown_field", "写入包含未知属性")
		}
		if bound[p.ID] {
			return nil, problem(422, "duplicate_field", "同一属性不能同时按名称和 ID 写入")
		}
		bound[p.ID] = true
		if p.ReadOnly || !fieldAllowed(allowed, p.ID) {
			return nil, problem(403, "forbidden_field", "属性只读或不允许写入")
		}
		if _, err := typedValue(p, raw); err != nil {
			return nil, err
		}
		out[p.ID] = raw
	}
	if creating {
		for _, p := range s.Properties {
			if _, ok := out[p.ID]; ok {
				continue
			}
			if len(p.Default) > 0 {
				out[p.ID] = p.Default
			} else if !p.Required && p.Nullable {
				out[p.ID] = json.RawMessage("null")
			} else {
				return nil, problem(422, "missing_field", "缺少必填属性或模型要求的值")
			}
		}
	}
	return out, nil
}

// Check new-row constraints through the same typed SQL compiler. Constants are
// bound into a one-row derived relation, so SQL three-valued logic stays intact.
func checkNewRow(ctx context.Context, tx *sql.Tx, s *c.Schema, values map[string]json.RawMessage, dec c.Decision) error {
	filter := dec.NewRowFilter
	if filter == nil {
		filter = dec.RowFilter
	}
	return checkRowFilter(ctx, tx, s, values, filter)
}

func checkRowFilter(ctx context.Context, tx *sql.Tx, s *c.Schema, values map[string]json.RawMessage, filter *c.Expression) error {
	if filter == nil {
		return nil
	}
	compiler := expressionCompiler{schema: s, allowed: []string{"*"}}
	condition, conditionArgs, err := compiler.compile(filter, 0)
	if err != nil {
		return err
	}
	cols := []string{}
	args := []any{}
	for _, p := range s.Properties {
		raw, ok := values[p.ID]
		if !ok {
			return problem(422, "missing_field", "新行属性不完整")
		}
		v, e := typedValue(p, raw)
		if e != nil {
			return e
		}
		t, _ := sqlType(p)
		cast := "CAST(? AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_0900_bin"
		switch p.DataType {
		case "decimal":
			cast = "CAST(? AS " + t + ")"
		case "int64", "boolean":
			cast = "CAST(? AS SIGNED)"
		case "timestamp":
			cast = "CAST(? AS DATETIME(6))"
		}
		cols = append(cols, cast+" AS "+quote(columnName(p.ID)))
		args = append(args, v)
	}
	args = append(conditionArgs, args...)
	var allowed bool
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(("+condition+"),FALSE) FROM (SELECT "+strings.Join(cols, ",")+") AS candidate", args...).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return problem(403, "forbidden_row", "数据不满足当前授权行条件")
	}
	return nil
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req c.WriteRequest
	if err = decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.IdempotencyKey == "" || len(req.IdempotencyKey) > 128 {
		writeError(w, problem(422, "invalid_idempotency_key", "创建需要 1..128 字节幂等键"))
		return
	}
	current, err := h.current(r.Context(), p.TenantID, r.PathValue("schemaId"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err = validateSchema(current.Definition); err != nil {
		writeError(w, err)
		return
	}
	dec, err := h.authorize(r.Context(), p, "instance.create", current.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if current.Definition.Schema.StorageBinding != nil {
		writeError(w, problem(422, "mapped_writes_unsupported", "外部映射对象暂只开放读取；跨数据源写入、幂等与outbox事务尚未开放"))
		return
	}
	var out c.Instance
	replayed := false
	err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.gate(r.Context(), tx, p, current); err != nil {
			return err
		}
		var requestHash, entityID, response string
		err := tx.QueryRowContext(r.Context(), "SELECT request_hash,entity_id,response FROM af_data_idempotency WHERE tenant_id=? AND schema_id=? AND subject_id=? AND request_key=? FOR UPDATE", p.TenantID, current.Definition.ID, p.Subject, req.IdempotencyKey).Scan(&requestHash, &entityID, &response)
		if err == nil {
			if requestHash != digest(req.Values) {
				return problem(409, "idempotency_conflict", "同一幂等键不能代表不同意图")
			}
			if _, e := selectInstance(r.Context(), tx, current.Definition, p.TenantID, entityID, dec.RowFilter, false); e != nil {
				return e
			}
			if json.Unmarshal([]byte(response), &out) != nil {
				return problem(500, "idempotency_corrupt", "无法核对原创建结果")
			}
			// A current row may have changed owner or department since creation.
			// Returning its historical response requires that old row to satisfy
			// the current read condition as well; NewRowFilter is not applicable.
			if e := checkRowFilter(r.Context(), tx, current.Definition.Schema, out.Values, dec.RowFilter); e != nil {
				return e
			}
			replayed = true
			return h.recheck(r.Context(), p, "instance.create", current.Definition.ID, dec)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		values, err := buildValues(current.Definition.Schema, req.Values, nil, dec.WriteFields, true)
		if err != nil {
			return err
		}
		if err = checkNewRow(r.Context(), tx, current.Definition.Schema, values, dec); err != nil {
			return err
		}
		out = c.Instance{ID: c.NewID(), SchemaID: current.Definition.ID, ETag: c.NewID(), Values: values}
		budget := jsonBudget{remaining: c.MaxDataResponseBytes - 1}
		if err = budget.add(out); err != nil {
			return err
		}
		cols := []string{"`tenant_id`", "`id`", "`etag`"}
		marks := []string{"?", "?", "?"}
		args := []any{p.TenantID, out.ID, out.ETag}
		for _, f := range current.Definition.Schema.Properties {
			v, e := typedValue(f, values[f.ID])
			if e != nil {
				return e
			}
			cols = append(cols, quote(columnName(f.ID)))
			marks = append(marks, "?")
			args = append(args, v)
		}
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO "+quote(tableName(current.Definition.ID))+" ("+strings.Join(cols, ",")+") VALUES ("+strings.Join(marks, ",")+")", args...); err != nil {
			return err
		}
		body, _ := json.Marshal(out)
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO af_data_idempotency (tenant_id,schema_id,subject_id,request_key,request_hash,entity_id,response) VALUES (?,?,?,?,?,?,?)", p.TenantID, current.Definition.ID, p.Subject, req.IdempotencyKey, digest(req.Values), out.ID, string(body)); err != nil {
			return err
		}
		if err = emit(r.Context(), tx, p.TenantID, "instance.created", out); err != nil {
			return err
		}
		return h.recheck(r.Context(), p, "instance.create", current.Definition.ID, dec)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+out.ETag+`"`)
	status := 201
	if replayed {
		status = 200
	}
	writeJSON(w, status, projectInstance(out, dec.ReadFields))
}

func (h *handler) read(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	current, err := h.current(r.Context(), p.TenantID, r.PathValue("schemaId"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err = validateSchema(current.Definition); err != nil {
		writeError(w, err)
		return
	}
	dec, err := h.authorize(r.Context(), p, "instance.read", current.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if current.Definition.Schema.StorageBinding != nil {
		query := c.QuerySpec{From: c.Source{SchemaID: current.Definition.ID}, Page: c.Page{Limit: 1}}
		for _, property := range projection(current.Definition.Schema, dec.ReadFields) {
			query.Select = append(query.Select, property.ID)
		}
		result, err := h.queryMapped(r.Context(), p, current, dec, query, r.PathValue("entityId"), "instance.read")
		if err != nil {
			writeError(w, err)
			return
		}
		if len(result.Data) != 1 {
			writeError(w, problem(404, "not_found", "实例不存在或不可访问"))
			return
		}
		w.Header().Set("ETag", `"`+result.Data[0].ETag+`"`)
		writeJSON(w, 200, result.Data[0])
		return
	}
	if !uuidPattern.MatchString(r.PathValue("entityId")) {
		writeError(w, problem(404, "not_found", "实例不存在或不可访问"))
		return
	}
	var out c.Instance
	err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.gate(r.Context(), tx, p, current); err != nil {
			return err
		}
		var e error
		out, e = selectInstance(r.Context(), tx, current.Definition, p.TenantID, r.PathValue("entityId"), dec.RowFilter, false)
		if e != nil {
			return e
		}
		return h.recheck(r.Context(), p, "instance.read", current.Definition.ID, dec)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+out.ETag+`"`)
	writeJSON(w, 200, projectInstance(out, dec.ReadFields))
}

func (h *handler) patch(w http.ResponseWriter, r *http.Request)  { h.change(w, r, false) }
func (h *handler) delete(w http.ResponseWriter, r *http.Request) { h.change(w, r, true) }
func (h *handler) change(w http.ResponseWriter, r *http.Request, deleting bool) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	current, err := h.current(r.Context(), p.TenantID, r.PathValue("schemaId"))
	if err != nil {
		writeError(w, err)
		return
	}
	if current.Definition.Schema != nil && current.Definition.Schema.StorageBinding != nil {
		operation := "instance.patch"
		if deleting {
			operation = "instance.delete"
		}
		if _, err := h.authorize(r.Context(), p, operation, current.Definition.ID); err != nil {
			writeError(w, err)
			return
		}
		writeError(w, problem(422, "mapped_writes_unsupported", "外部映射对象暂只开放读取；跨数据源写入与outbox事务尚未开放"))
		return
	}
	etag := bareETag(r.Header.Get("If-Match"))
	if !uuidPattern.MatchString(etag) {
		writeError(w, problem(428, "precondition_required", "更新或删除需要读取时返回的 If-Match"))
		return
	}
	if !uuidPattern.MatchString(r.PathValue("entityId")) {
		writeError(w, problem(404, "not_found", "实例不存在或不可访问"))
		return
	}
	var req c.WriteRequest
	if !deleting {
		if err = decode(w, r, &req); err != nil {
			writeError(w, err)
			return
		}
		if len(req.Values) == 0 || req.IdempotencyKey != "" {
			writeError(w, problem(422, "invalid_patch", "PATCH 需要非空 values，不接受创建幂等键"))
			return
		}
	}
	if err = validateSchema(current.Definition); err != nil {
		writeError(w, err)
		return
	}
	operation := "instance.patch"
	if deleting {
		operation = "instance.delete"
	}
	dec, err := h.authorize(r.Context(), p, operation, current.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	var out c.Instance
	err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.gate(r.Context(), tx, p, current); err != nil {
			return err
		}
		old, err := selectInstance(r.Context(), tx, current.Definition, p.TenantID, r.PathValue("entityId"), dec.RowFilter, true)
		if err != nil {
			return err
		}
		if old.ETag != etag {
			return problem(412, "precondition_failed", "实例已被修改，请重新读取")
		}
		if deleting {
			if _, err = tx.ExecContext(r.Context(), "DELETE FROM "+quote(tableName(current.Definition.ID))+" WHERE tenant_id=? AND id=? AND etag=?", p.TenantID, old.ID, old.ETag); err != nil {
				return err
			}
			out = old
			if err = emit(r.Context(), tx, p.TenantID, "instance.deleted", old); err != nil {
				return err
			}
		} else {
			values, err := buildValues(current.Definition.Schema, req.Values, old.Values, dec.WriteFields, false)
			if err != nil {
				return err
			}
			if err = checkNewRow(r.Context(), tx, current.Definition.Schema, values, dec); err != nil {
				return err
			}
			out = old
			out.Values = values
			out.ETag = c.NewID()
			budget := jsonBudget{remaining: c.MaxDataResponseBytes - 1}
			if err = budget.add(out); err != nil {
				return err
			}
			sets := []string{"`etag`=?"}
			args := []any{out.ETag}
			keys := make([]string, 0, len(req.Values))
			for key := range req.Values {
				p, _ := findProperty(current.Definition.Schema, key)
				keys = append(keys, p.ID)
			}
			sort.Strings(keys)
			for _, id := range keys {
				f, _ := findProperty(current.Definition.Schema, id)
				v, e := typedValue(f, values[id])
				if e != nil {
					return e
				}
				sets = append(sets, quote(columnName(id))+"=?")
				args = append(args, v)
			}
			args = append(args, p.TenantID, old.ID, old.ETag)
			if _, err = tx.ExecContext(r.Context(), "UPDATE "+quote(tableName(current.Definition.ID))+" SET "+strings.Join(sets, ",")+" WHERE tenant_id=? AND id=? AND etag=?", args...); err != nil {
				return err
			}
			if err = emit(r.Context(), tx, p.TenantID, "instance.updated", out); err != nil {
				return err
			}
		}
		return h.recheck(r.Context(), p, operation, current.Definition.ID, dec)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if deleting {
		w.WriteHeader(204)
		return
	}
	w.Header().Set("ETag", `"`+out.ETag+`"`)
	writeJSON(w, 200, projectInstance(out, dec.ReadFields))
}

func emit(ctx context.Context, tx *sql.Tx, tenant, event string, instance c.Instance) error {
	payload, _ := json.Marshal(instance)
	_, err := tx.ExecContext(ctx, "INSERT INTO af_data_outbox (id,tenant_id,schema_id,entity_id,event_type,payload) VALUES (?,?,?,?,?,?)", c.NewID(), tenant, instance.SchemaID, instance.ID, event, string(payload))
	return err
}

func (h *handler) query(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req c.QuerySpec
	if err = decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.executeQuery(r.Context(), p, req)
	if err != nil {
		writeError(w, err)
		return
	}
	// Cursor continuations share this endpoint with OntologySQL; logical result
	// descriptions must not reintroduce physical mappings on subsequent pages.
	result.Fields = safeQueryProperties(result.Fields)
	writeJSON(w, 200, result)
}

// All query frontends enter this path. A bound QuerySpec is a query description,
// never an authorization token: current definitions and decisions are re-read.
func (h *handler) executeQuery(ctx context.Context, p c.Principal, req c.QuerySpec) (c.QueryResult, error) {
	if req.Rows != nil {
		return h.executeRowQuery(ctx, p, req)
	}
	current, err := h.resolve(ctx, p, req.From)
	if err != nil {
		return c.QueryResult{}, err
	}
	dec, err := h.authorize(ctx, p, "instance.query", current.Definition.ID)
	if err != nil {
		return c.QueryResult{}, err
	}
	if req.Via != nil {
		return h.queryLink(ctx, p, current, dec, req)
	}
	if current.Definition.Schema != nil && current.Definition.Schema.StorageBinding != nil {
		return h.queryMapped(ctx, p, current, dec, req, "", "instance.query")
	}
	compiled, err := compileQuery(current.Definition, current.ETag, p, dec, req, h.cfg.CursorSecret)
	if err != nil {
		return c.QueryResult{}, err
	}
	result := c.QueryResult{Data: []c.Instance{}, Fields: compiled.Projection}
	budget, err := newQueryBudget(safeQueryProperties(compiled.Projection))
	if err != nil {
		return result, err
	}
	var lastValue json.RawMessage
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, p, current); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, compiled.SQL, compiled.Args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			values := make([]any, len(compiled.ScanFields)+2)
			dest := make([]any, len(values))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				return err
			}
			if len(result.Data) == compiled.Limit {
				result.HasMore = true
				break
			}
			if err = budget.checkDatabaseRow(values); err != nil {
				return err
			}
			item := c.Instance{ID: dbString(values[0]), ETag: dbString(values[1]), SchemaID: current.Definition.ID, Values: map[string]json.RawMessage{}}
			for i, f := range compiled.ScanFields {
				raw, e := rawDatabaseValue(f, values[i+2])
				if e != nil {
					return e
				}
				if compiled.Sort != nil && f.ID == compiled.Sort.ID {
					lastValue = raw
				}
				for _, selected := range compiled.Projection {
					if selected.ID == f.ID {
						item.Values[f.ID] = raw
						break
					}
				}
			}
			if err = budget.add(item); err != nil {
				return err
			}
			result.Data = append(result.Data, item)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if err = rows.Close(); err != nil {
			return err
		}
		return h.recheck(ctx, p, "instance.query", current.Definition.ID, dec)
	})
	if err != nil {
		return c.QueryResult{}, err
	}
	if result.HasMore && len(result.Data) > 0 {
		result.NextCursor, err = signCursor(h.cfg.CursorSecret, pageCursor{Fingerprint: compiled.Fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: current.Definition.ID, Digest: current.ETag, Authorization: digest(dec), LastID: result.Data[len(result.Data)-1].ID, LastValue: lastValue, Expires: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			return c.QueryResult{}, err
		}
	}
	return result, nil
}
