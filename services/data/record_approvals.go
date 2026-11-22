package data

import (
	"database/sql"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type recordApproval struct {
	ID          string     `json:"id"`
	Assignee    string     `json:"assignee"`
	State       string     `json:"state"`
	Note        string     `json:"note,omitempty"`
	SubmittedBy string     `json:"submittedBy"`
	SubmittedAt time.Time  `json:"submittedAt"`
	DecidedAt   *time.Time `json:"decidedAt,omitempty"`
	CanDecide   bool       `json:"canDecide"`
}

func (h *handler) recordApprovals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	target := inboxReply{SchemaID: r.PathValue("schemaId"), RecordID: r.PathValue("entityId")}
	id := r.PathValue("approvalId")
	if !uuidPattern.MatchString(target.SchemaID) || !uuidPattern.MatchString(target.RecordID) ||
		(r.Method == http.MethodPut && !uuidPattern.MatchString(id)) {
		writeError(w, problem(404, "not_found", "????????"))
		return
	}
	var input struct {
		Assignee string `json:"assignee,omitempty"`
		Decision string `json:"decision,omitempty"`
		Note     string `json:"note,omitempty"`
	}
	if r.Method != http.MethodGet {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := decode(w, r, &input); err != nil {
			writeError(w, err)
			return
		}
		input.Note = strings.TrimSpace(input.Note)
		if (r.Method == http.MethodPost && (!uuidPattern.MatchString(input.Assignee) || input.Assignee == p.Subject || input.Decision != "" || input.Note != "")) ||
			(r.Method == http.MethodPut && (input.Assignee != "" || (input.Decision != "approve" && input.Decision != "return") ||
				(input.Decision == "return" && input.Note == "") || utf8.RuneCountInString(input.Note) > 1000)) {
			writeError(w, problem(422, "invalid_approval", "???????????"))
			return
		}
	}
	page := []recordApproval{}
	err = h.withReplyTarget(ctx, p, target, r.Method != http.MethodGet, func(tx *sql.Tx, _ c.Decision, d c.Definition) error {
		args := []any{p.TenantID, target.SchemaID, target.RecordID}
		switch r.Method {
		case http.MethodGet:
			rows, err := tx.QueryContext(ctx, "SELECT id,assignee,state,note,submitted_by,submitted_at,decided_at FROM af_data_record_approvals WHERE "+commentScope+" AND (assignee=? OR submitted_by=?) ORDER BY submitted_at DESC,id DESC LIMIT 50", append(args, p.Subject, p.Subject)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var row recordApproval
				if err := rows.Scan(&row.ID, &row.Assignee, &row.State, &row.Note, &row.SubmittedBy, &row.SubmittedAt, &row.DecidedAt); err != nil {
					return err
				}
				row.CanDecide = row.Assignee == p.Subject && row.State == "pending"
				page = append(page, row)
			}
			return rows.Err()
		case http.MethodPost:
			change, err := h.authorize(ctx, p, "instance.update", target.SchemaID)
			if err != nil {
				return err
			}
			if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, target.RecordID, change.RowFilter, false); err != nil {
				return err
			}
			if err := h.validateMentionTargets(ctx, c.MentionTargetCheck{Actor: p, TargetIDs: []string{input.Assignee}}); err != nil {
				return err
			}
			recipient := c.Principal{TenantID: p.TenantID, Subject: input.Assignee, ExpiresAt: time.Now().Add(time.Minute).Unix()}
			read, err := h.authorize(ctx, recipient, "instance.read", target.SchemaID)
			if err != nil {
				return mentionAccessError(err)
			}
			if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, target.RecordID, read.RowFilter, false); err != nil {
				return mentionAccessError(err)
			}
			var pending string
			err = tx.QueryRowContext(ctx, "SELECT id FROM af_data_record_approvals WHERE "+commentScope+" AND state='pending' LIMIT 1 FOR UPDATE", args...).Scan(&pending)
			if err == nil {
				return problem(409, "approval_pending", "?????????")
			}
			if err != sql.ErrNoRows {
				return err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO af_data_record_approvals (tenant_id,schema_id,entity_id,id,assignee,submitted_by,submitted_at,state,note) VALUES (?,?,?,?,?,?,UTC_TIMESTAMP(6),'pending','')", append(args, c.NewID(), input.Assignee, p.Subject)...)
			if err != nil {
				return err
			}
			if err := h.recheck(ctx, recipient, "instance.read", target.SchemaID, read); err != nil {
				return mentionAccessError(err)
			}
			return h.recheck(ctx, p, "instance.update", target.SchemaID, change)
		default:
			var state string
			err := tx.QueryRowContext(ctx, "SELECT state FROM af_data_record_approvals WHERE "+commentScope+" AND id=? AND assignee=? FOR UPDATE", append(args, id, p.Subject)...).Scan(&state)
			if err == sql.ErrNoRows {
				return problem(404, "not_found", "??????")
			}
			if err != nil {
				return err
			}
			if state != "pending" {
				return problem(409, "approval_resolved", "?????")
			}
			state = "approved"
			if input.Decision == "return" {
				state = "returned"
			}
			_, err = tx.ExecContext(ctx, "UPDATE af_data_record_approvals SET state=?,note=?,decided_at=UTC_TIMESTAMP(6) WHERE "+commentScope+" AND id=? AND assignee=?",
				append([]any{state, input.Note}, append(args, id, p.Subject)...)...)
			return err
		}
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{"data": page})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
