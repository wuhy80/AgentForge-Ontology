package data

import (
	"database/sql"
	"net/http"
	"time"
)

func (h *handler) commentReplies(w http.ResponseWriter, r *http.Request) {
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
	d, entity := current.Definition, r.PathValue("entityId")
	mark := r.Method == http.MethodPut
	id := r.PathValue("replyId")
	if d.Schema == nil || d.Schema.StorageBinding != nil || !uuidPattern.MatchString(entity) || (mark && !uuidPattern.MatchString(id)) {
		writeError(w, problem(404, "not_found", "记录或回复不可访问"))
		return
	}
	read, err := h.authorize(ctx, p, "instance.read", d.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	type reply struct {
		ID string `json:"id"`
		At string `json:"at"`
	}
	data := []reply{}
	var unread int
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, p, current); err != nil {
			return err
		}
		if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, entity, read.RowFilter, mark); err != nil {
			return err
		}
		const join = " FROM af_data_record_comments reply JOIN af_data_record_comments parent ON parent.tenant_id=reply.tenant_id AND parent.schema_id=reply.schema_id AND parent.entity_id=reply.entity_id AND parent.id=reply.parent_id"
		const scope = " WHERE reply.tenant_id=? AND reply.schema_id=? AND reply.entity_id=? AND parent.author=? AND reply.author<>parent.author"
		args := []any{p.TenantID, d.ID, entity, p.Subject}
		if mark {
			var replyID string
			err := tx.QueryRowContext(ctx, "SELECT reply.id"+join+scope+" AND reply.id=?", append(args, id)...).Scan(&replyID)
			if err == sql.ErrNoRows {
				return problem(404, "not_found", "回复不可访问")
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO af_data_comment_reply_reads (tenant_id,schema_id,entity_id,reply_id,subject_id) VALUES (?,?,?,?,?) ON DUPLICATE KEY UPDATE reply_id=reply_id", p.TenantID, d.ID, entity, replyID, p.Subject); err != nil {
				return err
			}
		} else {
			const missing = " AND NOT EXISTS (SELECT 1 FROM af_data_comment_reply_reads rr WHERE rr.tenant_id=reply.tenant_id AND rr.schema_id=reply.schema_id AND rr.entity_id=reply.entity_id AND rr.reply_id=reply.id AND rr.subject_id=parent.author)"
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*)"+join+scope+missing, args...).Scan(&unread); err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, "SELECT reply.id,reply.created_at"+join+scope+missing+" ORDER BY reply.created_at DESC,reply.id DESC LIMIT 25", args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var item reply
				var at time.Time
				if err := rows.Scan(&item.ID, &at); err != nil {
					return err
				}
				item.At = at.UTC().Format(time.RFC3339Nano)
				data = append(data, item)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
		return h.recheck(ctx, p, "instance.read", d.ID, read)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if mark {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "unread": unread})
}
