package data

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const commentReplyNoticeKind = "comment_reply"

func (h *handler) recordNotifications(w http.ResponseWriter, r *http.Request) {
	h.readReplyInbox(w, r, true)
}

func (h *handler) markRecordNotification(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("kind") != commentReplyNoticeKind {
		writeError(w, problem(http.StatusNotFound, "not_found", "通知不可访问"))
		return
	}
	r.SetPathValue("replyId", r.PathValue("noticeId"))
	h.commentReplies(w, r)
}

func recordNotice(item inboxReply) c.RecordNotice {
	return c.RecordNotice{ID: item.ID, Kind: commentReplyNoticeKind, SchemaID: item.SchemaID,
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
		rows, err := tx.QueryContext(ctx, `SELECT reply.schema_id,reply.entity_id,COUNT(*)`+replyInboxScope+
			` AND rr.reply_id IS NULL GROUP BY reply.schema_id,reply.entity_id
			   ORDER BY reply.schema_id,reply.entity_id LIMIT ?`, p.TenantID, p.Subject, unreadRecordBudget+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var record recordCount
			if err := rows.Scan(&record.target.SchemaID, &record.target.RecordID, &record.count); err != nil {
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
