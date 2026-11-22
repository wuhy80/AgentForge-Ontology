package data

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) recordFollow(w http.ResponseWriter, r *http.Request) {
	h.recordNoticeWrite(w, r, false)
}

func (h *handler) recordNoticeWrite(w http.ResponseWriter, r *http.Request, mark bool) {
	ctx := r.Context()
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	target := inboxReply{SchemaID: r.PathValue("schemaId"), RecordID: r.PathValue("entityId")}
	id := r.PathValue("noticeId")
	if !uuidPattern.MatchString(target.SchemaID) || !uuidPattern.MatchString(target.RecordID) ||
		(mark && !uuidPattern.MatchString(id)) {
		writeError(w, problem(404, "not_found", "记录或通知不可访问"))
		return
	}
	following := false
	err = h.withReplyTarget(ctx, p, target, r.Method != http.MethodGet, func(tx *sql.Tx, read c.Decision) error {
		args := []any{p.TenantID, target.SchemaID, target.RecordID, p.Subject}
		if mark {
			var fieldID string
			err := tx.QueryRowContext(ctx, "SELECT field_id FROM af_data_record_notices WHERE "+commentScope+" AND subject_id=? AND id=? AND kind=? FOR UPDATE",
				append(args, id, r.PathValue("kind"))...).Scan(&fieldID)
			if errors.Is(err, sql.ErrNoRows) {
				return problem(404, "not_found", "通知不可访问")
			}
			if err != nil {
				return err
			}
			if fieldID != "" && !fieldAllowed(read.ReadFields, fieldID) {
				return problem(403, "forbidden", "??????")
			}
			_, err = tx.ExecContext(ctx, "UPDATE af_data_record_notices SET is_read=true WHERE "+commentScope+" AND subject_id=? AND id=?", append(args, id)...)
			return err
		}
		switch r.Method {
		case http.MethodGet:
			return tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM af_data_record_follows WHERE "+commentScope+" AND subject_id=?)", args...).Scan(&following)
		case http.MethodPut:
			_, err := tx.ExecContext(ctx, "INSERT INTO af_data_record_follows (tenant_id,schema_id,entity_id,subject_id) VALUES (?,?,?,?) ON DUPLICATE KEY UPDATE subject_id=subject_id", args...)
			return err
		default:
			_, err := tx.ExecContext(ctx, "DELETE FROM af_data_record_follows WHERE "+commentScope+" AND subject_id=?", args...)
			return err
		}
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, c.RecordFollow{Following: following})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) withReplyTarget(ctx context.Context, p c.Principal, item inboxReply, lock bool, apply func(*sql.Tx, c.Decision) error) error {
	current, err := h.current(ctx, p.TenantID, item.SchemaID)
	if err != nil {
		return err
	}
	read, err := h.authorize(ctx, p, "instance.read", current.Definition.ID)
	if err != nil {
		return err
	}
	if item.fieldID != "" && !fieldAllowed(read.ReadFields, item.fieldID) {
		return problem(403, "forbidden", "??????")
	}
	d := current.Definition
	if d.Schema == nil || d.Schema.StorageBinding != nil {
		return problem(404, "not_found", "记录不可访问")
	}
	return h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, p, current); err != nil {
			return err
		}
		if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, item.RecordID, read.RowFilter, lock); err != nil {
			return err
		}
		if err := apply(tx, read); err != nil {
			return err
		}
		return h.recheck(ctx, p, "instance.read", d.ID, read)
	})
}
