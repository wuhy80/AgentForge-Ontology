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
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func (h *handler) registerSavedQueries(mux *http.ServeMux) {
	mux.HandleFunc("GET /data/saved-queries", h.savedQueriesListHTTP)
	mux.HandleFunc("POST /data/saved-queries", h.savedQueriesSaveHTTP(false))
	mux.HandleFunc("POST /data/saved-queries/compile", h.savedQueriesCompileHTTP)
	mux.HandleFunc("GET /data/saved-queries/{id}", h.savedQueriesGetHTTP)
	mux.HandleFunc("PUT /data/saved-queries/{id}", h.savedQueriesSaveHTTP(true))
	mux.HandleFunc("DELETE /data/saved-queries/{id}", h.savedQueriesDeleteHTTP)
	mux.HandleFunc("POST /data/saved-queries/{id}/execute", h.savedQueriesExecuteHTTP)
}

func (h *handler) savedQueryPrincipal(r *http.Request) (c.Principal, error) {
	p, err := h.principal(r)
	if err != nil {
		return p, err
	}
	user, exists := platform.SessionUser(r)
	if !exists || !user.Enabled || user.ID != p.Subject || user.TenantID != p.TenantID || strings.HasPrefix(platform.Bearer(r), "mcp_") {
		return p, problem(403, "saved_query_session_required", "保存查询仅供当前工作台用户会话使用")
	}
	return p, nil
}

func savedQueryNotFound() error {
	return problem(404, "saved_query_not_found", "保存查询不存在或不可访问")
}

type savedQueryScanner interface{ Scan(...any) error }

func scanSavedQuery(row savedQueryScanner, details bool) (c.SavedQuery, error) {
	var out c.SavedQuery
	var created, updated time.Time
	var encoded string
	dest := []any{&out.ID, &out.Name, &out.ETag, &created, &updated}
	if details {
		dest = append(dest, &encoded)
	}
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, savedQueryNotFound()
		}
		return out, err
	}
	out.CreatedAt, out.UpdatedAt = created.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano)
	if details {
		var input c.SavedQueryInput
		if len(encoded) > c.MaxSavedQueryBytes || json.Unmarshal([]byte(encoded), &input) != nil || input.Name != out.Name {
			return out, problem(500, "saved_query_corrupt", "无法读取保存查询模板")
		}
		out.Query, out.Source, out.Parameters = input.Query, input.Source, input.Parameters
	}
	return out, nil
}

const savedSummaryColumns = "id,name,etag,created_at,updated_at"

func (h *handler) loadSavedQuery(ctx context.Context, p c.Principal, id string) (c.SavedQuery, error) {
	if !uuidPattern.MatchString(id) {
		return c.SavedQuery{}, savedQueryNotFound()
	}
	return scanSavedQuery(h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT "+savedSummaryColumns+",template_json FROM af_data_saved_queries WHERE tenant_id=? AND user_id=? AND id=?", p.TenantID, p.Subject, id), true)
}

func savedQueryInput(out c.SavedQuery) c.SavedQueryInput {
	return c.SavedQueryInput{Name: out.Name, Source: out.Source, Query: out.Query, Parameters: out.Parameters}
}

func savedQueryPrecondition(r *http.Request) (string, error) {
	if len(r.Header.Values("If-Match")) != 1 {
		return "", problem(428, "precondition_required", "修改或删除保存查询需要单个强If-Match")
	}
	return mutationETag(r.Header.Get("If-Match"))
}

func (h *handler) savedQueriesGetHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.savedQueryPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := h.loadSavedQuery(r.Context(), p, r.PathValue("id"))
	if err == nil {
		_, _, err = h.validateSavedInput(r.Context(), p, savedQueryInput(out))
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+out.ETag+`"`)
	writeJSON(w, 200, out)
}

func lockSavedQueryOwner(ctx context.Context, tx *sql.Tx, p c.Principal) error {
	if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO af_data_saved_query_owners (tenant_id,user_id) VALUES (?,?)", p.TenantID, p.Subject); err != nil {
		return err
	}
	var owner string
	return tx.QueryRowContext(ctx, "SELECT user_id FROM af_data_saved_query_owners WHERE tenant_id=? AND user_id=? FOR UPDATE", p.TenantID, p.Subject).Scan(&owner)
}

func (h *handler) savedQueriesSaveHTTP(update bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := h.savedQueryPrincipal(r)
		if err != nil {
			writeError(w, err)
			return
		}
		var input c.SavedQueryInput
		if err := decode(w, r, &input); err != nil {
			writeError(w, err)
			return
		}
		id, previous := c.NewID(), ""
		if update {
			id = r.PathValue("id")
			if !uuidPattern.MatchString(id) {
				writeError(w, savedQueryNotFound())
				return
			}
			previous, err = savedQueryPrecondition(r)
			if err != nil {
				writeError(w, err)
				return
			}
			owned, err := h.loadSavedQuery(r.Context(), p, id)
			if err != nil {
				writeError(w, err)
				return
			}
			if owned.ETag != previous {
				writeError(w, problem(412, "precondition_failed", "保存查询已改变，请重新读取后修改"))
				return
			}
		}
		input, _, err = h.validateSavedInput(r.Context(), p, input)
		if err != nil {
			writeError(w, err)
			return
		}
		encoded, _ := json.Marshal(input)
		now := time.Now().UTC().Truncate(time.Microsecond)
		out := c.SavedQuery{SavedQuerySummary: c.SavedQuerySummary{ID: id, Name: input.Name, ETag: digest([]any{p.TenantID, p.Subject, id, c.NewID(), input}), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}, Source: input.Source, Query: input.Query, Parameters: input.Parameters}
		err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
			if update {
				var actual string
				var created time.Time
				if err := tx.QueryRowContext(r.Context(), "SELECT etag,created_at FROM af_data_saved_queries WHERE tenant_id=? AND user_id=? AND id=? FOR UPDATE", p.TenantID, p.Subject, id).Scan(&actual, &created); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return savedQueryNotFound()
					}
					return err
				}
				if actual != previous {
					return problem(412, "precondition_failed", "保存查询已改变，请重新读取后修改")
				}
				out.CreatedAt = created.UTC().Format(time.RFC3339Nano)
				_, err := tx.ExecContext(r.Context(), "UPDATE af_data_saved_queries SET name=?,name_key=?,template_json=?,etag=?,updated_at=? WHERE tenant_id=? AND user_id=? AND id=?", out.Name, nameKey(out.Name), string(encoded), out.ETag, now, p.TenantID, p.Subject, id)
				return err
			}
			if err := lockSavedQueryOwner(r.Context(), tx, p); err != nil {
				return err
			}
			var count int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM af_data_saved_queries WHERE tenant_id=? AND user_id=?", p.TenantID, p.Subject).Scan(&count); err != nil {
				return err
			}
			if count >= c.MaxSavedQueries {
				return problem(422, "saved_query_quota", "每个用户最多保存100份查询，请先删除不再使用的模板")
			}
			_, err := tx.ExecContext(r.Context(), "INSERT INTO af_data_saved_queries (tenant_id,user_id,id,name,name_key,template_json,etag,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)", p.TenantID, p.Subject, id, out.Name, nameKey(out.Name), string(encoded), out.ETag, now, now)
			return err
		})
		if err != nil {
			writeError(w, err)
			return
		}
		// Saving private configuration is independent from business access.
		// If authorization changed while the row was stored, retain the owned
		// template for list/delete but never deliver its old sensitive body.
		if _, _, err := h.validateSavedInput(r.Context(), p, input); err != nil {
			writeError(w, err)
			return
		}
		status := 201
		if update {
			status = 200
		}
		w.Header().Set("ETag", `"`+out.ETag+`"`)
		writeJSON(w, status, out)
	}
}

func (h *handler) savedQueriesDeleteHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.savedQueryPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		writeError(w, savedQueryNotFound())
		return
	}
	previous, err := savedQueryPrecondition(r)
	if err != nil {
		writeError(w, err)
		return
	}
	err = h.cfg.Engine.WithTx(r.Context(), func(tx *sql.Tx) error {
		if err := lockSavedQueryOwner(r.Context(), tx, p); err != nil {
			return err
		}
		var actual string
		if err := tx.QueryRowContext(r.Context(), "SELECT etag FROM af_data_saved_queries WHERE tenant_id=? AND user_id=? AND id=? FOR UPDATE", p.TenantID, p.Subject, id).Scan(&actual); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return savedQueryNotFound()
			}
			return err
		}
		if actual != previous {
			return problem(412, "precondition_failed", "保存查询已改变，请重新读取后删除")
		}
		_, err := tx.ExecContext(r.Context(), "DELETE FROM af_data_saved_queries WHERE tenant_id=? AND user_id=? AND id=?", p.TenantID, p.Subject, id)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) savedQueriesListHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.savedQueryPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	limit, after := 20, ""
	for key, values := range r.URL.Query() {
		if key != "limit" && key != "cursor" || len(values) != 1 {
			writeError(w, problem(400, "invalid_saved_query_page", "列表仅接受单项limit与cursor"))
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > c.MaxSavedQueryPageSize {
			writeError(w, problem(422, "invalid_saved_query_page", "保存查询每页须为1..50项"))
			return
		}
	}
	fingerprint := digest([]any{"private-saved-queries-v1", limit})
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		cursor, parseErr := parseCursor(h.cfg.CursorSecret, encoded)
		if len(encoded) > 4096 || parseErr != nil || cursor.TenantID != p.TenantID || cursor.Subject != p.Subject || cursor.SchemaID != "saved-queries" || cursor.Fingerprint != fingerprint || !uuidPattern.MatchString(cursor.LastID) {
			writeError(w, problem(422, "invalid_cursor", "保存查询游标已失效或不属于当前用户"))
			return
		}
		after = cursor.LastID
	}
	rows, err := h.cfg.Engine.DB().QueryContext(r.Context(), "SELECT "+savedSummaryColumns+" FROM af_data_saved_queries WHERE tenant_id=? AND user_id=? AND id>? ORDER BY id LIMIT ?", p.TenantID, p.Subject, after, limit+1)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	out := c.SavedQueryPage{Data: []c.SavedQuerySummary{}}
	for rows.Next() {
		item, err := scanSavedQuery(rows, false)
		if err != nil {
			writeError(w, err)
			return
		}
		if len(out.Data) == limit {
			out.HasMore = true
			break
		}
		out.Data = append(out.Data, item.SavedQuerySummary)
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	if out.HasMore {
		out.NextCursor, err = signCursor(h.cfg.CursorSecret, pageCursor{TenantID: p.TenantID, Subject: p.Subject, SchemaID: "saved-queries", Fingerprint: fingerprint, LastID: out.Data[len(out.Data)-1].ID, Expires: time.Now().Add(15 * time.Minute).Unix()})
		if err != nil {
			writeError(w, err)
			return
		}
	}
	writeJSON(w, 200, out)
}

func (h *handler) savedQueriesExecuteHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.savedQueryPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var request c.SavedQueryExecuteRequest
	if err := decode(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	if len(request.Cursor) > 4096 || len(request.Parameters) > c.MaxSavedQueryParameters {
		writeError(w, problem(422, "saved_query_budget", "运行参数或游标超过预算"))
		return
	}
	saved, err := h.loadSavedQuery(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	input, current, err := h.validateSavedInput(r.Context(), p, savedQueryInput(saved))
	if err == nil {
		_, err = savedParameterFields(&input.Query, current.Definition.Schema, input.Parameters, request.Parameters, false)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	input.Query.Page.Cursor = request.Cursor
	result, err := h.executeQuery(r.Context(), p, input.Query)
	if err != nil {
		writeError(w, err)
		return
	}
	result.Fields = safeQueryProperties(result.Fields)
	writeJSON(w, 200, result)
}
