package execution

import (
	"context"
	"database/sql"
	"encoding/json"
)

type jobStore interface {
	admitJob(context.Context, Record) (bool, error)
	leaseJob(context.Context) (Record, error)
	jobActive(context.Context, Record) (bool, error)
	finishJob(context.Context, Record) error
	cancelJob(context.Context, string, string, string) (Record, error)
}

func (s *SQLStore) jobActive(ctx context.Context, record Record) (bool, error) {
	var active bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM af_execution_invocations
		WHERE tenant_id=? AND id=? AND status='job_running' AND job_attempt=? AND deadline_at>UTC_TIMESTAMP(6))`,
		record.TenantID, record.Invocation.ID, record.Invocation.Attempt).Scan(&active)
	return active, err
}

func (s *SQLStore) CheckJobs(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, "SELECT job_attempt FROM af_execution_invocations, af_execution_job_admissions LIMIT 0")
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
	record, err := scanRecord(tx.QueryRowContext(ctx, `SELECT record_json FROM af_execution_invocations
		WHERE status='queued' OR (status='job_running' AND deadline_at<=UTC_TIMESTAMP(6))
		ORDER BY deadline_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`))
	if err != nil {
		return Record{}, err
	}
	record.Invocation.Status = "running"
	record.Invocation.Attempt++
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
	_, err = s.DB.ExecContext(ctx, `UPDATE af_execution_invocations SET status=?,record_json=?
		WHERE tenant_id=? AND id=? AND status='job_running' AND job_attempt=? AND deadline_at>UTC_TIMESTAMP(6)`,
		record.Invocation.Status, string(raw), record.TenantID, record.Invocation.ID, record.Invocation.Attempt)
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
		if _, err := tx.ExecContext(ctx, "UPDATE af_execution_invocations SET status='canceled',record_json=? WHERE tenant_id=? AND id=?", string(raw), tenant, id); err != nil {
			return Record{}, err
		}
	case "canceled":
	default:
		return Record{}, problem(409, "job_finished", "任务已结束")
	}
	return record, tx.Commit()
}
