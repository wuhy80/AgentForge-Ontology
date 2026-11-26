package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// record lifecycles keep the current stage and the committed transition history
// beside the managed record. The definition stays in Metadata; Data only reads
// the current one, so a renamed stage never rewrites history and a removed stage
// is reported instead of silently moving a record.
const lifecycleStageScope = "tenant_id=? AND schema_id=? AND entity_id=?"

func (h *handler) recordLifecycle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	schemaID, recordID := r.PathValue("schemaId"), r.PathValue("entityId")
	if !uuidPattern.MatchString(schemaID) || !uuidPattern.MatchString(recordID) {
		writeError(w, problem(404, "not_found", "记录不可访问"))
		return
	}
	var input struct {
		TransitionID string `json:"transitionId,omitempty"`
		Note         string `json:"note,omitempty"`
	}
	transition := r.Method == http.MethodPost
	if transition {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := decode(w, r, &input); err != nil {
			writeError(w, err)
			return
		}
		input.Note = strings.TrimSpace(input.Note)
		if !uuidPattern.MatchString(input.TransitionID) || utf8.RuneCountInString(input.Note) > c.MaxLifecycleNoteRunes {
			writeError(w, problem(422, "invalid_transition", "流转标识或说明不合法"))
			return
		}
	}
	var view c.RecordLifecycle
	err = h.withLifecycleTarget(ctx, p, schemaID, recordID, transition, func(tx *sql.Tx, _ c.Decision, d c.Definition, l *c.SchemaLifecycle) error {
		stageID, updatedAt, persisted, err := readLifecycleStage(ctx, tx, p.TenantID, schemaID, recordID)
		if err != nil {
			return err
		}
		if !persisted {
			stageID = l.InitialStageID
		}
		if !transition {
			view = c.RecordLifecycle{Stages: lifecycleDisplayStages(l.Stages), Transitions: l.Transitions, StageID: stageID,
				UpdatedAt: updatedAt, Persisted: persisted, History: []c.LifecycleRun{}}
			view.StageUnknown = stageKnown(l, stageID) == false
			if available, err := h.lifecycleAvailability(ctx, tx, p, d, recordID, stageID); err != nil {
				return err
			} else {
				view.Available = available
			}
			return readLifecycleHistory(ctx, tx, p, schemaID, recordID, l, &view)
		}
		move, ok := lifecycleTransition(l, input.TransitionID)
		if !ok {
			return problem(422, "invalid_transition", "流转不属于当前生效定义")
		}
		if !stageKnown(l, stageID) {
			return problem(409, "lifecycle_stage_unknown", "当前阶段已不在生效定义中，请先处理该记录")
		}
		if move.From != stageID {
			return problem(409, "lifecycle_stage_mismatch", "记录阶段已变化，请重新读取后再提交")
		}
		// The stage row is created lazily so records that predate the lifecycle
		// declaration keep their initial stage without a backfill.
		if _, err := tx.ExecContext(ctx, "INSERT INTO af_data_record_lifecycles (tenant_id,schema_id,entity_id,stage_id,updated_at,updated_by) VALUES (?,?,?,?,UTC_TIMESTAMP(6),?) ON DUPLICATE KEY UPDATE stage_id=stage_id",
			p.TenantID, schemaID, recordID, stageID, p.Subject); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE af_data_record_lifecycles SET stage_id=?,updated_at=UTC_TIMESTAMP(6),updated_by=? WHERE "+lifecycleStageScope+" AND stage_id=?",
			move.To, p.Subject, p.TenantID, schemaID, recordID, stageID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return problem(409, "lifecycle_stage_mismatch", "记录阶段已变化，请重新读取后再提交")
		}
		runID := c.NewID()
		_, err = tx.ExecContext(ctx, "INSERT INTO af_data_record_lifecycle_runs (tenant_id,schema_id,entity_id,id,transition_id,from_stage_id,to_stage_id,actor,note,created_at) VALUES (?,?,?,?,?,?,?,?,?,UTC_TIMESTAMP(6))",
			p.TenantID, schemaID, recordID, runID, move.ID, move.From, move.To, p.Subject, input.Note)
		if err != nil {
			return err
		}
		_, err = h.attemptLifecycleNotice(ctx, tx, p, d, recordID, runID, move.To)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if transition {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *handler) notifyLifecycleRecipients(ctx context.Context, tx *sql.Tx, actor c.Principal, d c.Definition, record, run, stageID string) error {
	const limit = 200
	// Bound the batch independently of the parent transaction so a slow
	// authorization service leaves time to record the failed delivery.
	deliveryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var accounts []string
	for _, stage := range d.Schema.Lifecycle.Stages {
		if stage.ID == stageID {
			accounts = stage.NotifyAccountIDs
			break
		}
	}
	if len(accounts) > c.MaxLifecycleStageRecipients {
		return problem(422, "lifecycle_notice_budget", "阶段通知账号超出检查预算")
	}
	accountJSON, err := json.Marshal(append([]string{}, accounts...))
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT subject_id FROM (
 SELECT subject_id FROM af_data_record_follows WHERE `+lifecycleStageScope+`
 UNION DISTINCT
 SELECT assignee FROM af_data_record_approvals WHERE `+lifecycleStageScope+` AND state='pending'
 UNION DISTINCT
 SELECT CAST(account_id AS BINARY) FROM JSON_TABLE(?, '$[*]' COLUMNS(account_id VARCHAR(36) PATH '$')) AS configured
 ) recipients WHERE subject_id<>? ORDER BY subject_id LIMIT ?`,
		actor.TenantID, d.ID, record, actor.TenantID, d.ID, record, string(accountJSON), actor.Subject, limit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	var recipients []string
	for rows.Next() {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			return err
		}
		recipients = append(recipients, subject)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(recipients) > limit {
		return problem(422, "lifecycle_notice_budget", "通知接收人数超出检查预算")
	}
	type delivery struct {
		recipient c.Principal
		read      c.Decision
	}
	deliveries := make([]delivery, 0, len(recipients))
	for _, subject := range recipients {
		recipient := c.Principal{TenantID: actor.TenantID, Subject: subject, ExpiresAt: time.Now().Add(time.Minute).Unix()}
		read, err := h.authorize(deliveryCtx, recipient, "instance.read", d.ID)
		if err == nil {
			_, err = selectInstanceFields(ctx, tx, d, nil, actor.TenantID, record, read.RowFilter, false)
		}
		if err != nil {
			var failure *apiError
			if errors.As(err, &failure) && (failure.status == 401 || failure.status == 403 || failure.status == 404) {
				continue
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO af_data_record_notices
 (tenant_id,subject_id,id,schema_id,entity_id,created_at,kind) VALUES (?,?,?,?,?,UTC_TIMESTAMP(6),?)`,
			actor.TenantID, subject, run, d.ID, record, lifecycleNoticeKind); err != nil {
			return err
		}
		deliveries = append(deliveries, delivery{recipient: recipient, read: read})
	}
	// Recheck after all inserts so an authorization change while processing
	// another recipient rolls back the entire delivery attempt.
	for _, notice := range deliveries {
		if err := h.recheck(deliveryCtx, notice.recipient, "instance.read", d.ID, notice.read); err != nil {
			return err
		}
	}
	return nil
}

// Return the delivery error separately so the caller can commit its failure status.
func (h *handler) attemptLifecycleNotice(ctx context.Context, tx *sql.Tx, p c.Principal, d c.Definition, recordID, runID, stageID string) (error, error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT lifecycle_notice"); err != nil {
		return nil, err
	}
	deliveryErr := h.notifyLifecycleRecipients(ctx, tx, p, d, recordID, runID, stageID)
	code := ""
	if deliveryErr != nil {
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT lifecycle_notice"); err != nil {
			return nil, err
		}
		code = "delivery_failed"
		var failure *apiError
		if errors.As(deliveryErr, &failure) {
			switch failure.code {
			case "authorization_unavailable", "authorization_changed", "lifecycle_notice_budget":
				code = failure.code
			}
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE af_data_record_lifecycle_runs SET notification_failed=?,notification_failure_code=? WHERE "+lifecycleStageScope+" AND id=?", deliveryErr != nil, code, p.TenantID, d.ID, recordID, runID)
	return deliveryErr, err
}

func (h *handler) retryLifecycleNotice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	schemaID, recordID, runID := r.PathValue("schemaId"), r.PathValue("entityId"), r.PathValue("runId")
	if !uuidPattern.MatchString(schemaID) || !uuidPattern.MatchString(recordID) || !uuidPattern.MatchString(runID) {
		writeError(w, problem(404, "not_found", "流转记录不可访问"))
		return
	}
	err = h.retryLifecycleDelivery(ctx, p, schemaID, recordID, runID)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) retryLifecycleDelivery(ctx context.Context, p c.Principal, schemaID, recordID, runID string) error {
	var deliveryErr error
	err := h.withLifecycleTarget(ctx, p, schemaID, recordID, true, func(tx *sql.Tx, _ c.Decision, d c.Definition, _ *c.SchemaLifecycle) error {
		var failed bool
		var stageID string
		err := tx.QueryRowContext(ctx, "SELECT notification_failed,to_stage_id FROM af_data_record_lifecycle_runs WHERE "+lifecycleStageScope+" AND id=? AND actor=? FOR UPDATE",
			p.TenantID, schemaID, recordID, runID, p.Subject).Scan(&failed, &stageID)
		if errors.Is(err, sql.ErrNoRows) {
			return problem(404, "not_found", "流转记录不可访问")
		}
		if err != nil || !failed {
			return err
		}
		deliveryErr, err = h.attemptLifecycleNotice(ctx, tx, p, d, recordID, runID, stageID)
		return err
	})
	if err != nil {
		return err
	}
	return deliveryErr
}

// withLifecycleTarget applies the same record visibility rules as comments and
// approvals: the current definition must declare a managed lifecycle, the
// account must read the record, and a transition additionally needs the update
// decision with its row filter.
func (h *handler) withLifecycleTarget(ctx context.Context, p c.Principal, schemaID, recordID string, lock bool, apply func(*sql.Tx, c.Decision, c.Definition, *c.SchemaLifecycle) error) error {
	current, err := h.current(ctx, p.TenantID, schemaID)
	if err != nil {
		return err
	}
	d := current.Definition
	if d.Schema == nil || d.Schema.StorageBinding != nil || d.Schema.Lifecycle == nil {
		return problem(404, "not_found", "记录不可访问")
	}
	lifecycle := d.Schema.Lifecycle
	read, err := h.authorize(ctx, p, "instance.read", d.ID)
	if err != nil {
		return err
	}
	return h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, p, current); err != nil {
			return err
		}
		if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, recordID, read.RowFilter, lock); err != nil {
			return err
		}
		if lock {
			change, err := h.authorize(ctx, p, "instance.patch", d.ID)
			if err != nil {
				return err
			}
			if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, recordID, change.RowFilter, true); err != nil {
				return err
			}
			if err := apply(tx, change, d, lifecycle); err != nil {
				return err
			}
			if err := h.recheck(ctx, p, "instance.patch", d.ID, change); err != nil {
				return err
			}
		} else if err := apply(tx, read, d, lifecycle); err != nil {
			return err
		}
		return h.recheck(ctx, p, "instance.read", d.ID, read)
	})
}

func readLifecycleStage(ctx context.Context, tx *sql.Tx, tenant, schema, record string) (string, string, bool, error) {
	var stageID string
	var at time.Time
	err := tx.QueryRowContext(ctx, "SELECT stage_id,updated_at FROM af_data_record_lifecycles WHERE "+lifecycleStageScope, tenant, schema, record).Scan(&stageID, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return stageID, at.UTC().Format(time.RFC3339Nano), true, nil
}

func readLifecycleHistory(ctx context.Context, tx *sql.Tx, p c.Principal, schema, record string, l *c.SchemaLifecycle, view *c.RecordLifecycle) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,transition_id,from_stage_id,to_stage_id,note,created_at,notification_failed,notification_failure_code,actor FROM af_data_record_lifecycle_runs WHERE "+lifecycleStageScope+" ORDER BY created_at DESC,id DESC LIMIT ?",
		p.TenantID, schema, record, c.MaxLifecycleHistory+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var run c.LifecycleRun
		var at time.Time
		var actor string
		if err := rows.Scan(&run.ID, &run.TransitionID, &run.From, &run.To, &run.Note, &at, &run.NotificationFailed, &run.NotificationFailureCode, &actor); err != nil {
			return err
		}
		run.At = at.UTC().Format(time.RFC3339Nano)
		run.NotificationRetryAllowed = run.NotificationFailed && actor == p.Subject && view.Available != nil
		view.History = append(view.History, run)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(view.History) > c.MaxLifecycleHistory {
		view.History = view.History[:c.MaxLifecycleHistory]
		view.Truncated = true
	}
	for i := range view.History {
		if move, ok := lifecycleTransition(l, view.History[i].TransitionID); ok {
			view.History[i].Name = move.Name
		}
	}
	return nil
}

// lifecycleAvailability lists the transitions this account may run on the
// current stage. Row-restricted accounts only see what they could submit.
func (h *handler) lifecycleAvailability(ctx context.Context, tx *sql.Tx, p c.Principal, d c.Definition, record, stageID string) ([]string, error) {
	change, err := h.authorize(ctx, p, "instance.patch", d.ID)
	if err != nil {
		return nil, nil
	}
	if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, record, change.RowFilter, false); err != nil {
		var failure *apiError
		if errors.As(err, &failure) && (failure.status == 403 || failure.status == 404) {
			return nil, nil
		}
		return nil, err
	}
	available := []string{}
	for _, move := range d.Schema.Lifecycle.Transitions {
		if move.From == stageID {
			available = append(available, move.ID)
		}
	}
	return available, nil
}

func lifecycleTransition(l *c.SchemaLifecycle, id string) (c.LifecycleTransition, bool) {
	for _, move := range l.Transitions {
		if move.ID == id {
			return move, true
		}
	}
	return c.LifecycleTransition{}, false
}

func stageKnown(l *c.SchemaLifecycle, id string) bool {
	for _, stage := range l.Stages {
		if stage.ID == id {
			return true
		}
	}
	return false
}

func lifecycleDisplayStages(stages []c.LifecycleStage) []c.LifecycleStage {
	display := make([]c.LifecycleStage, len(stages))
	for i, stage := range stages {
		display[i] = c.LifecycleStage{ID: stage.ID, Name: stage.Name}
	}
	return display
}
