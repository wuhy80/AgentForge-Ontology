package data

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type noticeFailure struct {
	cause  error
	schema string
	record string
}

func (e *noticeFailure) Error() string { return e.cause.Error() }
func (e *noticeFailure) Unwrap() error { return e.cause }

func saveNoticeFailure(ctx context.Context, tx *sql.Tx, p c.Principal, cause error) error {
	var failed *noticeFailure
	if !errors.As(cause, &failed) {
		return nil
	}
	code := "delivery_unavailable"
	var api *apiError
	if errors.As(cause, &api) {
		switch api.code {
		case "invalid_mentions":
			code = "invalid_mentions"
		case "mention_target_forbidden":
			code = "mention_target_forbidden"
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO af_data_record_notices
  (tenant_id,subject_id,id,schema_id,entity_id,created_at,is_read,kind,failure_code)
  VALUES (?,?,?,?,?,UTC_TIMESTAMP(6),true,?,?)`, p.TenantID, p.Subject, c.NewID(), failed.schema, failed.record, mentionFailureKind, code)
	return err
}

func (h *handler) logNoticeFailure(ctx context.Context, p c.Principal, cause error) {
	var failed *noticeFailure
	if !errors.As(cause, &failed) {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := h.cfg.Engine.WithTx(auditCtx, func(tx *sql.Tx) error {
		return saveNoticeFailure(auditCtx, tx, p, cause)
	}); err != nil {
		log.Print("Data could not persist notification delivery failure")
	}
}

func noticeFailureMessage(code string) string {
	switch code {
	case "invalid_mentions":
		return "提及账号数量超限"
	case "mention_target_forbidden":
		return "包含当前不可提及或无法读取记录的账号"
	default:
		return "通知投递暂不可用"
	}
}
