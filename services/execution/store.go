package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Record is private execution evidence, never an HTTP response. Code, caller
// input and exact definitions remain inside this service's store.
type Record struct {
	Invocation  c.Invocation               `json:"invocation"`
	TenantID    string                     `json:"tenantId"`
	SubjectID   string                     `json:"subjectId"`
	RequestKey  string                     `json:"requestKey"`
	RequestHash string                     `json:"requestHash"`
	Root        c.Definition               `json:"root"`
	Function    c.Definition               `json:"function"`
	Input       map[string]json.RawMessage `json:"input"`
	ImageID     string                     `json:"imageId"`
	DeadlineAt  time.Time                  `json:"deadlineAt"`
}

type Store interface {
	Ping(context.Context) error
	FindKey(context.Context, string, string, string) (Record, error)
	FindID(context.Context, string, string, string) (Record, error)
	Claim(context.Context, Record) (bool, error)
	Finish(context.Context, Record) (bool, error)
}

type SQLStore struct{ DB *sql.DB }

func (s *SQLStore) Ping(ctx context.Context) error {
	if err := s.DB.PingContext(ctx); err != nil {
		return err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM af_execution_invocations LIMIT 0")
	if err != nil {
		return err
	}
	return rows.Close()
}

func scanRecord(row *sql.Row) (Record, error) {
	var out Record
	var raw string
	if err := row.Scan(&raw); err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Record{}, errors.New("invalid stored execution record")
	}
	return out, nil
}

func (s *SQLStore) FindKey(ctx context.Context, tenant, subject, key string) (Record, error) {
	return scanRecord(s.DB.QueryRowContext(ctx, "SELECT record_json FROM af_execution_invocations WHERE tenant_id=? AND subject_id=? AND request_key=?", tenant, subject, key))
}

func (s *SQLStore) FindID(ctx context.Context, tenant, subject, id string) (Record, error) {
	return scanRecord(s.DB.QueryRowContext(ctx, "SELECT record_json FROM af_execution_invocations WHERE tenant_id=? AND subject_id=? AND id=?", tenant, subject, id))
}

func (s *SQLStore) Claim(ctx context.Context, record Record) (bool, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	_, err = s.DB.ExecContext(ctx, "INSERT INTO af_execution_invocations (tenant_id,subject_id,request_key,request_hash,id,status,deadline_at,record_json) VALUES (?,?,?,?,?,?,?,?)", record.TenantID, record.SubjectID, record.RequestKey, record.RequestHash, record.Invocation.ID, record.Invocation.Status, record.DeadlineAt.UTC(), string(raw))
	var duplicate *mysql.MySQLError
	if errors.As(err, &duplicate) && duplicate.Number == 1062 {
		return false, nil
	}
	return err == nil, err
}

// A terminal record is immutable. A late process cannot overwrite an already
// interrupted outcome, and an ambiguous database write never triggers a rerun.
func (s *SQLStore) Finish(ctx context.Context, record Record) (bool, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	result, err := s.DB.ExecContext(ctx, "UPDATE af_execution_invocations SET status=?,record_json=? WHERE tenant_id=? AND subject_id=? AND id=? AND status='running'", record.Invocation.Status, string(raw), record.TenantID, record.SubjectID, record.Invocation.ID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
