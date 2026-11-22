package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type jobStore interface {
	admitJob(context.Context, Record) (bool, error)
	leaseJob(context.Context) (Record, error)
	jobActive(context.Context, Record) (bool, error)
	finishJob(context.Context, Record) error
	cancelJob(context.Context, string, string, string) (Record, error)
	purgeJobs(context.Context) error
	listJobs(context.Context, string, string, string, string, int) ([]c.InvocationSummary, error)
}

func (s *SQLStore) jobActive(ctx context.Context, record Record) (bool, error) {
	var active bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM af_execution_invocations
		WHERE tenant_id=? AND id=? AND status='job_running' AND job_attempt=? AND deadline_at>UTC_TIMESTAMP(6))`,
		record.TenantID, record.Invocation.ID, record.Invocation.Attempt).Scan(&active)
	return active, err
}

func (s *SQLStore) CheckJobs(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, "SELECT job_attempt,job_created,job_purge_at FROM af_execution_invocations, af_execution_job_admissions LIMIT 0")
	if err != nil {
		return err
	}
	return rows.Close()
}

func (s *SQLStore) admitJob(ctx context.Context, record Record) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO af_execution_job_admissions (tenant_id) VALUES (?)
		ON DUPLICATE KEY UPDATE tenant_id=tenant_id`, record.TenantID); err != nil {
		return false, err
	}
	var pending int
	var duplicate bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM af_execution_invocations
		WHERE tenant_id=? AND subject_id=? AND request_key=?)`, record.TenantID, record.SubjectID, record.RequestKey).Scan(&duplicate); err != nil {
		return false, err
	}
	if duplicate {
		return false, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM af_execution_invocations
		WHERE tenant_id=? AND status IN ('queued','job_running')`, record.TenantID).Scan(&pending); err != nil {
		return false, err
	}
	if pending >= 32 {
		return false, problem(429, "job_queue_full", "当前租户待处理任务已满，请稍后重试")
	}
	claimed, err := claimRecord(ctx, tx, record)
	if err != nil {
		return false, err
	}
	return claimed, tx.Commit()
}

func (s *SQLStore) leaseJob(ctx context.Context) (Record, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	const claim = `SELECT record_json FROM af_execution_invocations FORCE INDEX (job_claim)
		WHERE status=? AND deadline_at<=UTC_TIMESTAMP(6)
		ORDER BY deadline_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`
	record, err := scanRecord(tx.QueryRowContext(ctx, claim, "job_running"))
	if errors.Is(err, sql.ErrNoRows) {
		record, err = scanRecord(tx.QueryRowContext(ctx, claim, "queued"))
	}
	if err != nil {
		return Record{}, err
	}
	record.Invocation.Status = "running"
	record.Invocation.Attempt++
	record.Invocation.Error = nil
	raw, err := json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE af_execution_invocations SET status='job_running',
		job_attempt=?,deadline_at=UTC_TIMESTAMP(6)+INTERVAL 60 SECOND,record_json=?
		WHERE tenant_id=? AND id=?`, record.Invocation.Attempt, string(raw), record.TenantID, record.Invocation.ID)
	if err != nil {
		return Record{}, err
	}
	return record, tx.Commit()
}

func (s *SQLStore) finishJob(ctx context.Context, record Record) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	var delay int
	if record.Invocation.Status == "queued" {
		delay = 5 * record.Invocation.Attempt
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE af_execution_invocations SET status=?,record_json=?,
		deadline_at=UTC_TIMESTAMP(6)+INTERVAL ? SECOND,
		job_purge_at=IF(?='queued',NULL,UTC_TIMESTAMP(6)+INTERVAL 30 DAY)
		WHERE tenant_id=? AND id=? AND status='job_running' AND job_attempt=? AND deadline_at>UTC_TIMESTAMP(6)`,
		record.Invocation.Status, string(raw), delay, record.Invocation.Status, record.TenantID, record.Invocation.ID, record.Invocation.Attempt)
	return err
}

func (s *SQLStore) cancelJob(ctx context.Context, tenant, subject, id string) (Record, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	record, err := scanRecord(tx.QueryRowContext(ctx, `SELECT record_json FROM af_execution_invocations
		WHERE tenant_id=? AND subject_id=? AND id=? FOR UPDATE`, tenant, subject, id))
	if err != nil {
		return Record{}, err
	}
	if record.Job == nil {
		return Record{}, sql.ErrNoRows
	}
	switch record.Invocation.Status {
	case "queued", "running":
		closeJob(&record, "canceled", "", "")
		raw, err := json.Marshal(record)
		if err != nil {
			return Record{}, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE af_execution_invocations SET status='canceled',record_json=?,job_purge_at=UTC_TIMESTAMP(6)+INTERVAL 30 DAY WHERE tenant_id=? AND id=?", string(raw), tenant, id); err != nil {
			return Record{}, err
		}
	case "canceled":
	default:
		return Record{}, problem(409, "job_finished", "任务已结束")
	}
	return record, tx.Commit()
}

func (s *SQLStore) purgeJobs(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM af_execution_invocations WHERE job_purge_at<=UTC_TIMESTAMP(6) ORDER BY job_purge_at LIMIT 200")
	return err
}

func (s *SQLStore) listJobs(ctx context.Context, tenant, subject, created, id string, limit int) ([]c.InvocationSummary, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,job_created,job_attempt,
		JSON_UNQUOTE(JSON_EXTRACT(record_json,'$.invocation.definitionName')),
		JSON_UNQUOTE(JSON_EXTRACT(record_json,'$.invocation.kind')),
		IF(status='job_running','running',status),
		COALESCE(JSON_UNQUOTE(JSON_EXTRACT(record_json,'$.invocation.finishedAt')),'')
		FROM af_execution_invocations
		WHERE tenant_id=? AND subject_id=? AND job_created IS NOT NULL
		AND (job_purge_at IS NULL OR job_purge_at>UTC_TIMESTAMP(6))
		AND (job_created<? OR (job_created=? AND id<?)) ORDER BY job_created DESC,id DESC LIMIT ?`,
		tenant, subject, created, created, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]c.InvocationSummary, 0, limit)
	for rows.Next() {
		var item c.InvocationSummary
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.Attempt, &item.DefinitionName, &item.Kind, &item.Status, &item.FinishedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (h *handler) reapJobs(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purgeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := h.jobs.purgeJobs(purgeCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("execution job retention failed: %T", err)
			}
		}
	}
}
