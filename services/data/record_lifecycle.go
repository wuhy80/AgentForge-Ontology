package data

import (
	"context"
	"database/sql"
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
			view = c.RecordLifecycle{Stages: l.Stages, Transitions: l.Transitions, StageID: stageID,
				UpdatedAt: updatedAt, Persisted: persisted, History: []c.LifecycleRun{}}
			view.StageUnknown = stageKnown(l, stageID) == false
			if available, err := h.lifecycleAvailability(ctx, tx, p, d, recordID, stageID); err != nil {
				return err
			} else {
				view.Available = available
			}
			return readLifecycleHistory(ctx, tx, p.TenantID, schemaID, recordID, l, &view)
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
		return notifyRecordFollowers(ctx, tx, p, schemaID, recordID, runID, lifecycleNoticeKind)
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
			change, err := h.authorize(ctx, p, "instance.update", d.ID)
			if err != nil {
				return err
			}
			if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, recordID, change.RowFilter, true); err != nil {
				return err
			}
			if err := apply(tx, change, d, lifecycle); err != nil {
				return err
			}
			if err := h.recheck(ctx, p, "instance.update", d.ID, change); err != nil {
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

func readLifecycleHistory(ctx context.Context, tx *sql.Tx, tenant, schema, record string, l *c.SchemaLifecycle, view *c.RecordLifecycle) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,transition_id,from_stage_id,to_stage_id,note,created_at FROM af_data_record_lifecycle_runs WHERE "+lifecycleStageScope+" ORDER BY created_at DESC,id DESC LIMIT ?",
		tenant, schema, record, c.MaxLifecycleHistory+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var run c.LifecycleRun
		var at time.Time
		if err := rows.Scan(&run.ID, &run.TransitionID, &run.From, &run.To, &run.Note, &at); err != nil {
			return err
		}
		run.At = at.UTC().Format(time.RFC3339Nano)
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
	change, err := h.authorize(ctx, p, "instance.update", d.ID)
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
