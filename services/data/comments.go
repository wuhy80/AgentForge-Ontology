package data

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const commentScope = "tenant_id=? AND schema_id=? AND entity_id=?"
const commentPageSize = 25

func (h *handler) recordComments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	current, err := h.current(ctx, p.TenantID, r.PathValue("schemaId"))
	if err != nil {
		writeError(w, err)
		return
	}
	d := current.Definition
	entity := r.PathValue("entityId")
	if !uuidPattern.MatchString(entity) || d.Schema == nil || d.Schema.StorageBinding != nil {
		writeError(w, problem(404, "not_found", "记录不存在或不支持评论"))
		return
	}
	read, err := h.authorize(ctx, p, "instance.read", d.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	mutation := r.Method != http.MethodGet
	id := r.PathValue("commentId")
	var input struct {
		Body      string `json:"body"`
		ParentID  string `json:"parentId,omitempty"`
		RequestID string `json:"requestId,omitempty"`
	}
	update, updateErr := h.authorize(ctx, p, "instance.patch", d.ID)
	if mutation {
		if updateErr != nil {
			writeError(w, updateErr)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		if err = decode(w, r, &input); err != nil {
			writeError(w, err)
			return
		}
		input.Body = strings.TrimSpace(input.Body)
		if input.Body == "" || utf8.RuneCountInString(input.Body) > 4000 ||
			(r.Method == http.MethodPost && !uuidPattern.MatchString(input.RequestID)) ||
			(r.Method != http.MethodPost && input.RequestID != "") ||
			(input.ParentID != "" && (r.Method != http.MethodPost || !uuidPattern.MatchString(input.ParentID))) {
			writeError(w, problem(422, "invalid_comment", "请填写有效评论，正文最多 4000 字"))
			return
		}
	}
	var result any
	status := http.StatusOK
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, p, current); err != nil {
			return err
		}
		if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, entity, read.RowFilter, mutation); err != nil {
			return err
		}
		args := []any{p.TenantID, d.ID, entity}
		if mutation {
			if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, entity, update.RowFilter, true); err != nil {
				return err
			}
			if r.Method == http.MethodPost {
				var author, body, parent, etag string
				var at time.Time
				err := tx.QueryRowContext(ctx, "SELECT author,body,parent_id,etag,created_at FROM af_data_record_comments WHERE "+commentScope+" AND id=?",
					append(args, input.RequestID)...).Scan(&author, &body, &parent, &etag, &at)
				if err == nil {
					if author != p.Subject || body != input.Body || parent != input.ParentID {
						return problem(409, "comment_conflict", "评论请求已存在，请重新读取")
					}
					result = c.RecordComment{ID: input.RequestID, Author: author, Body: body, ParentID: parent, CreatedAt: at.UTC().Format(time.RFC3339Nano), ETag: etag, Editable: true}
					if err := h.recheck(ctx, p, "instance.patch", d.ID, update); err != nil {
						return err
					}
					return h.recheck(ctx, p, "instance.read", d.ID, read)
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if input.ParentID != "" {
					var parent string
					err := tx.QueryRowContext(ctx, "SELECT id FROM af_data_record_comments WHERE "+commentScope+" AND id=?", append(args, input.ParentID)...).Scan(&parent)
					if errors.Is(err, sql.ErrNoRows) {
						return problem(404, "not_found", "回复目标不存在")
					}
					if err != nil {
						return err
					}
				}
				newID, etag, now := input.RequestID, c.NewID(), time.Now().UTC().Truncate(time.Microsecond)
				if _, err := tx.ExecContext(ctx, "INSERT INTO af_data_record_comments (tenant_id,schema_id,entity_id,id,author,parent_id,body,created_at,etag) VALUES (?,?,?,?,?,?,?,?,?)",
					append(args, newID, p.Subject, input.ParentID, input.Body, now, etag)...); err != nil {
					return err
				}
				result = c.RecordComment{ID: newID, Author: p.Subject, Body: input.Body, ParentID: input.ParentID, CreatedAt: now.Format(time.RFC3339Nano), ETag: etag, Editable: true}
				status = http.StatusCreated
			} else {
				if !uuidPattern.MatchString(id) {
					return problem(404, "not_found", "评论不存在")
				}
				if len(r.Header.Values("If-Match")) != 1 {
					return problem(428, "precondition_required", "编辑评论需要 If-Match")
				}
				etag, err := mutationETag(r.Header.Get("If-Match"))
				if err != nil {
					return err
				}
				var previous string
				err = tx.QueryRowContext(ctx, "SELECT etag FROM af_data_record_comments WHERE "+commentScope+" AND id=? AND author=? FOR UPDATE",
					append(args, id, p.Subject)...).Scan(&previous)
				if errors.Is(err, sql.ErrNoRows) {
					return problem(404, "not_found", "评论不存在或不可编辑")
				}
				if err != nil {
					return err
				}
				if previous != etag {
					return problem(412, "precondition_failed", "评论已修改，请重新读取")
				}
				if _, err := tx.ExecContext(ctx, "UPDATE af_data_record_comments SET body=?,etag=? WHERE "+commentScope+" AND id=?",
					[]any{input.Body, c.NewID(), p.TenantID, d.ID, entity, id}...); err != nil {
					return err
				}
				status = http.StatusNoContent
			}
			if err := h.recheck(ctx, p, "instance.patch", d.ID, update); err != nil {
				return err
			}
		} else {
			query := "SELECT id,author,parent_id,body,created_at,etag FROM af_data_record_comments WHERE " + commentScope
			if cursor := r.URL.Query().Get("cursor"); cursor != "" {
				if !uuidPattern.MatchString(cursor) {
					return problem(422, "invalid_cursor", "评论游标无效")
				}
				var at time.Time
				err := tx.QueryRowContext(ctx, "SELECT created_at FROM af_data_record_comments WHERE "+commentScope+" AND id=?", append(args, cursor)...).Scan(&at)
				if errors.Is(err, sql.ErrNoRows) {
					return problem(422, "invalid_cursor", "评论游标无效")
				}
				if err != nil {
					return err
				}
				query += " AND (created_at > ? OR (created_at = ? AND id > ?))"
				args = append(args, at, at, cursor)
			}
			rows, err := tx.QueryContext(ctx, query+" ORDER BY created_at,id LIMIT 26", args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			page := c.RecordCommentPage{Data: []c.RecordComment{}}
			for rows.Next() {
				var item c.RecordComment
				var at time.Time
				if err := rows.Scan(&item.ID, &item.Author, &item.ParentID, &item.Body, &at, &item.ETag); err != nil {
					return err
				}
				if len(page.Data) == commentPageSize {
					page.NextCursor = page.Data[commentPageSize-1].ID
					break
				}
				item.CreatedAt = at.UTC().Format(time.RFC3339Nano)
				item.Editable = item.Author == p.Subject
				page.Data = append(page.Data, item)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if updateErr == nil {
				_, checkErr := selectInstanceFields(ctx, tx, d, nil, p.TenantID, entity, update.RowFilter, false)
				page.CanPost = checkErr == nil && h.recheck(ctx, p, "instance.patch", d.ID, update) == nil
			}
			for i := range page.Data {
				page.Data[i].Editable = page.CanPost && page.Data[i].Editable
			}
			result = page
		}
		return h.recheck(ctx, p, "instance.read", d.ID, read)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, result)
}
