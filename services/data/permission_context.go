package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const maxObjectContextBytes = 64 << 10

// objectContextHTTP is a service-only attribute reader. Its caller must bind
// each request to an administrator-authored policy, never to browser input.
// It deliberately does not invoke Authorizer, which would recursively depend
// on this same attribute reader. Only selected values cross the internal API.
func (h *handler) objectContextHTTP(w http.ResponseWriter, r *http.Request) {
	tenant, err := h.internalTenant(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input c.ObjectContextRequest
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&input)
	if err == nil {
		var extra any
		if trailing := decoder.Decode(&extra); trailing != io.EOF {
			if trailing == nil {
				err = errors.New("multiple values")
			} else {
				err = trailing
			}
		}
	}
	if err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			writeError(w, problem(413, "permission_context_budget", "权限上下文请求不能超过32KiB"))
		} else {
			writeError(w, problem(400, "invalid_json", "请求须为单个合法且无未知字段的JSON"))
		}
		return
	}
	result, err := h.objectContextValue(r.Context(), tenant, input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func objectContextUnavailable() error {
	return problem(403, "permission_context_unavailable", "无法安全取得权限上下文")
}

func objectContextSelection(d c.Definition, input c.ObjectContextRequest) (c.Property, string, []any, int, error) {
	bad := func() (c.Property, string, []any, int, error) {
		return c.Property{}, "", nil, 0, objectContextUnavailable()
	}
	if d.Schema == nil || d.Schema.StorageBinding != nil || !uuidPattern.MatchString(input.SchemaID) || d.ID != input.SchemaID || (input.Mode != "" && input.Mode != "scalar" && input.Mode != "set") {
		return bad()
	}
	property, ok := findProperty(d.Schema, input.FieldID)
	if !ok || property.ID != input.FieldID {
		return bad()
	}
	limit := 2
	if input.Mode == "set" {
		limit = 101
	}
	if input.InstanceID != "" {
		if !uuidPattern.MatchString(input.InstanceID) || input.Mode == "set" || input.MatchFieldID != "" || len(input.MatchValue) != 0 {
			return bad()
		}
		return property, "`id`=?", []any{input.InstanceID}, limit, nil
	}
	match, ok := findProperty(d.Schema, input.MatchFieldID)
	if !ok || match.ID != input.MatchFieldID || len(input.MatchValue) == 0 || len(input.MatchValue) > maxObjectContextBytes || strings.TrimSpace(string(input.MatchValue)) == "null" {
		return bad()
	}
	if input.Mode != "set" {
		unique := false
		for _, index := range d.Schema.Indexes {
			unique = unique || index.Unique && len(index.FieldIDs) == 1 && index.FieldIDs[0] == match.ID
		}
		if !unique {
			return bad()
		}
	}
	value, err := typedValue(match, input.MatchValue)
	if err != nil || value == nil {
		return bad()
	}
	return property, quote(columnName(match.ID)) + "=" + parameter(match), []any{value}, limit, nil
}

func objectContextGate(ctx context.Context, tx *sql.Tx, tenant string, current c.CurrentDefinition) error {
	var status, actual string
	if err := tx.QueryRowContext(ctx, "SELECT status,digest FROM af_data_gates WHERE tenant_id=? AND definition_id=?", tenant, current.Definition.ID).Scan(&status, &actual); err != nil {
		return objectContextUnavailable()
	}
	if status != "ready" || actual != current.ETag {
		return objectContextUnavailable()
	}
	return nil
}

func (h *handler) objectContextValue(ctx context.Context, tenant string, input c.ObjectContextRequest) (c.ObjectContextResult, error) {
	var result c.ObjectContextResult
	if h.cfg.Engine == nil || !uuidPattern.MatchString(input.SchemaID) {
		return result, objectContextUnavailable()
	}
	current, err := h.current(ctx, tenant, input.SchemaID)
	if err != nil || current.Definition.TenantID != tenant || validateSchema(current.Definition) != nil {
		return result, objectContextUnavailable()
	}
	property, condition, args, limit, err := objectContextSelection(current.Definition, input)
	if err != nil {
		return result, err
	}
	// No gate row lock: this reader can be nested inside an outer Data write
	// transaction that already holds that gate. READ COMMITTED permits the
	// final check to observe a publication that started after the first check.
	tx, err := h.cfg.Engine.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = objectContextGate(ctx, tx, tenant, current); err != nil {
		return result, err
	}
	query := "SELECT `id`,`etag`," + quote(columnName(property.ID)) + " FROM " + quote(tableName(input.SchemaID)) + " WHERE `tenant_id`=? AND (" + condition + ") ORDER BY `id` ASC LIMIT ?"
	args = append([]any{tenant}, args...)
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	type evidenceRow struct {
		ID, ETag, ValueDigest string
	}
	evidence := []evidenceRow{}
	values := []json.RawMessage{}
	unique := map[string]bool{}
	valueBytes := 2
	for rows.Next() {
		var id, etag string
		var value any
		if err = rows.Scan(&id, &etag, &value); err != nil {
			break
		}
		if value == nil || etag == "" || len(evidence) == 100 {
			err = objectContextUnavailable()
			break
		}
		raw, conversionErr := rawDatabaseValue(property, value)
		if conversionErr != nil || len(raw) > maxObjectContextBytes || string(raw) == "null" {
			err = objectContextUnavailable()
			break
		}
		if _, conversionErr = typedValue(property, raw); conversionErr != nil {
			err = objectContextUnavailable()
			break
		}
		evidence = append(evidence, evidenceRow{ID: id, ETag: etag, ValueDigest: digest(raw)})
		if !unique[string(raw)] {
			valueBytes += len(raw) + 1
			if valueBytes > maxObjectContextBytes {
				err = objectContextUnavailable()
				break
			}
			unique[string(raw)] = true
			values = append(values, raw)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return result, err
	}
	if len(evidence) == 0 || input.Mode != "set" && len(evidence) != 1 {
		return result, objectContextUnavailable()
	}
	if input.Mode == "set" {
		result.Value, err = json.Marshal(values)
	} else {
		result.Value = values[0]
	}
	if err != nil || len(result.Value) > maxObjectContextBytes {
		return c.ObjectContextResult{}, objectContextUnavailable()
	}
	latest, err := h.current(ctx, tenant, input.SchemaID)
	if err != nil || latest.ETag != current.ETag || digest(latest.Definition) != digest(current.Definition) {
		return c.ObjectContextResult{}, objectContextUnavailable()
	}
	if err = objectContextGate(ctx, tx, tenant, current); err != nil {
		return c.ObjectContextResult{}, err
	}
	result.Digest = digest(struct {
		Tenant, Definition string
		Input              c.ObjectContextRequest
		Rows               []evidenceRow
	}{tenant, current.ETag, input, evidence})
	if err = tx.Commit(); err != nil {
		return c.ObjectContextResult{}, err
	}
	return result, nil
}
