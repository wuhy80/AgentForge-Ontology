package data

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const commentReplyNoticeKind = "comment_reply"
const recordUpdateNoticeKind = "record_updated"
const accountMentionNoticeKind = "account_mention"

const replyNoticeSelect = `SELECT reply.id,reply.schema_id,reply.entity_id,reply.created_at,rr.reply_id IS NOT NULL AS is_read,'comment_reply' AS kind,'' AS field_id` + replyInboxScope
const updateNoticeSelect = `SELECT id,schema_id,entity_id,created_at,is_read,kind,field_id FROM af_data_record_notices WHERE tenant_id=? AND subject_id=?`

func (h *handler) recordNotifications(w http.ResponseWriter, r *http.Request) {
	h.readReplyInbox(w, r, true)
}

func (h *handler) markRecordNotification(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("kind") == recordUpdateNoticeKind || r.PathValue("kind") == accountMentionNoticeKind {
		h.recordNoticeWrite(w, r, true)
		return
	}
	if r.PathValue("kind") != commentReplyNoticeKind {
		writeError(w, problem(http.StatusNotFound, "not_found", "通知不可访问"))
		return
	}
	r.SetPathValue("replyId", r.PathValue("noticeId"))
	h.commentReplies(w, r)
}

func recordNotice(item inboxReply) c.RecordNotice {
	return c.RecordNotice{ID: item.ID, Kind: item.kind, SchemaID: item.SchemaID,
		RecordID: item.RecordID, At: item.At, Read: item.read}
}

const unreadRecordBudget = 50

func (h *handler) recordUnreadSummary(ctx context.Context, p c.Principal) (c.RecordUnreadSummary, error) {
	type recordCount struct {
		target inboxReply
		count  int64
	}
	records := make([]recordCount, 0, unreadRecordBudget+1)
	err := h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT schema_id,entity_id,field_id,COUNT(*) FROM (`+replyNoticeSelect+
			` AND rr.reply_id IS NULL UNION ALL `+updateNoticeSelect+` AND is_read=false) notices
			  GROUP BY schema_id,entity_id,field_id ORDER BY schema_id,entity_id,field_id LIMIT ?`,
			p.TenantID, p.Subject, p.TenantID, p.Subject, unreadRecordBudget+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var record recordCount
			if err := rows.Scan(&record.target.SchemaID, &record.target.RecordID, &record.target.fieldID, &record.count); err != nil {
				return err
			}
			records = append(records, record)
		}
		return rows.Err()
	})
	if err != nil {
		return c.RecordUnreadSummary{}, err
	}
	summary := c.RecordUnreadSummary{Exact: len(records) <= unreadRecordBudget}
	if !summary.Exact {
		records = records[:unreadRecordBudget]
	}
	for _, record := range records {
		if err := h.checkReplyTarget(ctx, p, record.target); err != nil {
			var failure *apiError
			if errors.As(err, &failure) && (failure.status == 403 || failure.status == 404) {
				continue
			}
			return c.RecordUnreadSummary{}, err
		}
		summary.Count += record.count
	}
	return summary, nil
}
