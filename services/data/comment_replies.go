package data

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const replyInboxBatch = 50

type replyInboxPosition struct {
	ID       string
	SchemaID string
	RecordID string
	At       time.Time
	Expires  int64
	Kind     string `json:",omitempty"`
}

func replyInboxCursor(secret string, p c.Principal, token string, position replyInboxPosition) (replyInboxPosition, string, error) {
	bad := problem(422, "invalid_cursor", "回复收件箱游标无效")
	if len(secret) < 32 {
		return position, "", problem(503, "configuration", "游标签名密钥未配置")
	}
	key := sha256.Sum256([]byte("comment-reply-inbox\x00" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return position, "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return position, "", err
	}
	scope, _ := json.Marshal([2]string{p.TenantID, p.Subject})
	if token != "" {
		if len(token) > 1024 {
			return position, "", bad
		}
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(raw) < aead.NonceSize() {
			return position, "", bad
		}
		payload, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], scope)
		if err != nil || json.Unmarshal(payload, &position) != nil || position.Expires <= time.Now().Unix() ||
			(position.Kind != "" && position.Kind != commentReplyNoticeKind && position.Kind != recordUpdateNoticeKind && position.Kind != accountMentionNoticeKind && position.Kind != mentionFailureKind) ||
			!uuidPattern.MatchString(position.ID) || !uuidPattern.MatchString(position.SchemaID) || !uuidPattern.MatchString(position.RecordID) || position.At.IsZero() {
			return position, "", bad
		}
		return position, "", nil
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return position, "", err
	}
	position.Expires = time.Now().Add(15 * time.Minute).Unix()
	payload, err := json.Marshal(position)
	if err != nil {
		return position, "", err
	}
	return position, base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, payload, scope)), nil
}

const replyInboxScope = ` FROM af_data_record_comments parent JOIN af_data_record_comments reply
ON reply.tenant_id=parent.tenant_id AND reply.schema_id=parent.schema_id AND reply.entity_id=parent.entity_id AND reply.parent_id=parent.id
LEFT JOIN af_data_comment_reply_reads rr ON rr.tenant_id=reply.tenant_id AND rr.schema_id=reply.schema_id AND rr.entity_id=reply.entity_id AND rr.reply_id=reply.id AND rr.subject_id=parent.author
WHERE parent.tenant_id=? AND parent.author=? AND reply.author<>parent.author`

type inboxReply struct {
	ID          string `json:"id"`
	SchemaID    string `json:"schemaId"`
	RecordID    string `json:"recordId"`
	At          string `json:"at"`
	read        bool
	kind        string
	fieldID     string
	failureCode string
}

func (h *handler) commentReplyInbox(w http.ResponseWriter, r *http.Request) {
	h.readReplyInbox(w, r, false)
}

func (h *handler) readReplyInbox(w http.ResponseWriter, r *http.Request, includeRead bool) {
	ctx := r.Context()
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var position replyInboxPosition
	secret := h.cfg.CursorSecret
	if includeRead && len(secret) >= 32 {
		secret = "record-notifications-v2\x00" + secret
	}
	if token := r.URL.Query().Get("cursor"); token != "" {
		position, _, err = replyInboxCursor(secret, p, token, position)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	// Bound the candidate set before checking each target object's current permissions.
	candidates := []inboxReply{}
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		query := "SELECT id,schema_id,entity_id,created_at,is_read,kind,field_id,failure_code FROM (" + replyNoticeSelect
		args := []any{p.TenantID, p.Subject}
		if includeRead {
			query += " UNION ALL " + updateNoticeSelect
			args = append(args, p.TenantID, p.Subject)
		}
		if !includeRead {
			query += " AND rr.reply_id IS NULL"
		}
		query += ") notices WHERE 1=1"
		if !position.At.IsZero() && includeRead {
			query += " AND (created_at,id,schema_id,entity_id,kind)<(?,?,?,?,?)"
			args = append(args, position.At, position.ID, position.SchemaID, position.RecordID, position.Kind)
		} else if !position.At.IsZero() {
			query += " AND (created_at,id,schema_id,entity_id)<(?,?,?,?)"
			args = append(args, position.At, position.ID, position.SchemaID, position.RecordID)
		}
		rows, err := tx.QueryContext(ctx, query+" ORDER BY created_at DESC,id DESC,schema_id DESC,entity_id DESC,kind DESC LIMIT ?", append(args, replyInboxBatch+1)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item inboxReply
			var at time.Time
			if err := rows.Scan(&item.ID, &item.SchemaID, &item.RecordID, &at, &item.read, &item.kind, &item.fieldID, &item.failureCode); err != nil {
				return err
			}
			item.At = at.UTC().Format(time.RFC3339Nano)
			candidates = append(candidates, item)
		}
		return rows.Err()
	})
	if err != nil {
		writeError(w, err)
		return
	}
	data := []inboxReply{}
	next := ""
	if len(candidates) > replyInboxBatch {
		candidates = candidates[:replyInboxBatch]
		last := candidates[len(candidates)-1]
		at, err := time.Parse(time.RFC3339Nano, last.At)
		if err != nil {
			writeError(w, err)
			return
		}
		position := replyInboxPosition{ID: last.ID, SchemaID: last.SchemaID, RecordID: last.RecordID, At: at}
		if includeRead {
			position.Kind = last.kind
		}
		_, next, err = replyInboxCursor(secret, p, "", position)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	for _, item := range candidates {
		if err := h.checkReplyTarget(ctx, p, item); err != nil {
			var failure *apiError
			if errors.As(err, &failure) && (failure.status == 403 || failure.status == 404) {
				continue
			}
			writeError(w, err)
			return
		}
		data = append(data, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	if includeRead {
		page := c.RecordNoticePage{Data: make([]c.RecordNotice, 0, len(data)), NextCursor: next}
		if position.At.IsZero() {
			unread, err := h.recordUnreadSummary(ctx, p)
			if err != nil {
				writeError(w, err)
				return
			}
			page.Unread = &unread
		}
		for _, item := range data {
			page.Data = append(page.Data, recordNotice(item))
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	out := map[string]any{"data": data}
	if next != "" {
		out["nextCursor"] = next
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) checkReplyTarget(ctx context.Context, p c.Principal, item inboxReply) error {
	return h.withReplyTarget(ctx, p, item, false, func(*sql.Tx, c.Decision, c.Definition) error { return nil })
}

func (h *handler) commentReplies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	current, err := h.current(ctx, p.TenantID, r.PathValue("schemaId"))
	if err != nil {
		writeError(w, err)
		return
	}
	d, entity := current.Definition, r.PathValue("entityId")
	mark := r.Method == http.MethodPut
	id := r.PathValue("replyId")
	if d.Schema == nil || d.Schema.StorageBinding != nil || !uuidPattern.MatchString(entity) || (mark && !uuidPattern.MatchString(id)) {
		writeError(w, problem(404, "not_found", "记录或回复不可访问"))
		return
	}
	read, err := h.authorize(ctx, p, "instance.read", d.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	type reply struct {
		ID string `json:"id"`
		At string `json:"at"`
	}
	data := []reply{}
	var unread int
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, p, current); err != nil {
			return err
		}
		if _, err := selectInstanceFields(ctx, tx, d, nil, p.TenantID, entity, read.RowFilter, mark); err != nil {
			return err
		}
		const join = " FROM af_data_record_comments reply JOIN af_data_record_comments parent ON parent.tenant_id=reply.tenant_id AND parent.schema_id=reply.schema_id AND parent.entity_id=reply.entity_id AND parent.id=reply.parent_id"
		const scope = " WHERE reply.tenant_id=? AND reply.schema_id=? AND reply.entity_id=? AND parent.author=? AND reply.author<>parent.author"
		args := []any{p.TenantID, d.ID, entity, p.Subject}
		if mark {
			var replyID string
			err := tx.QueryRowContext(ctx, "SELECT reply.id"+join+scope+" AND reply.id=?", append(args, id)...).Scan(&replyID)
			if err == sql.ErrNoRows {
				return problem(404, "not_found", "回复不可访问")
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO af_data_comment_reply_reads (tenant_id,schema_id,entity_id,reply_id,subject_id) VALUES (?,?,?,?,?) ON DUPLICATE KEY UPDATE reply_id=reply_id", p.TenantID, d.ID, entity, replyID, p.Subject); err != nil {
				return err
			}
		} else {
			const missing = " AND NOT EXISTS (SELECT 1 FROM af_data_comment_reply_reads rr WHERE rr.tenant_id=reply.tenant_id AND rr.schema_id=reply.schema_id AND rr.entity_id=reply.entity_id AND rr.reply_id=reply.id AND rr.subject_id=parent.author)"
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*)"+join+scope+missing, args...).Scan(&unread); err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, "SELECT reply.id,reply.created_at"+join+scope+missing+" ORDER BY reply.created_at DESC,reply.id DESC LIMIT 25", args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var item reply
				var at time.Time
				if err := rows.Scan(&item.ID, &at); err != nil {
					return err
				}
				item.At = at.UTC().Format(time.RFC3339Nano)
				data = append(data, item)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
		return h.recheck(ctx, p, "instance.read", d.ID, read)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if mark {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "unread": unread})
}
