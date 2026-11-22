package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

var accountMentionLink = regexp.MustCompile(`\[@(?:[^\x5c\]\r\n]|\x5c.)+\]\(#onto-mention-account-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\)`)

func accountMentions(value json.RawMessage) map[string]bool {
	var body string
	_ = json.Unmarshal(value, &body)
	targets := map[string]bool{}
	for {
		match := accountMentionLink.FindStringSubmatchIndex(body)
		if match == nil {
			break
		}
		targets[body[match[2]:match[3]]] = true
		body = body[match[1]:]
	}
	return targets
}

func (s *platformMutationStore) notifyMentions(ctx context.Context, old, next c.Instance, input map[string]json.RawMessage) error {
	d := s.current.Definition
	targets := map[string][]string{}
	for _, field := range d.Schema.Properties {
		if _, changed := input[field.ID]; !changed || (field.DataType != "string" && field.DataType != "text" && field.DataType != "longtext") {
			continue
		}
		previous := accountMentions(old.Values[field.ID])
		for target := range accountMentions(next.Values[field.ID]) {
			if target != s.p.Subject && !previous[target] {
				targets[target] = append(targets[target], field.ID)
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	if len(targets) > c.MaxMentionTargets {
		return problem(422, "invalid_mentions", "单条记录最多新增 20 个提及账号")
	}
	ids := make([]string, 0, len(targets))
	for target := range targets {
		ids = append(ids, target)
	}
	slices.Sort(ids)
	if err := s.h.validateMentionTargets(ctx, c.MentionTargetCheck{Actor: s.p, TargetIDs: ids}); err != nil {
		return err
	}
	for _, id := range ids {
		recipient := c.Principal{TenantID: s.p.TenantID, Subject: id, ExpiresAt: time.Now().Add(time.Minute).Unix()}
		read, err := s.h.authorize(ctx, recipient, "instance.read", d.ID)
		if err != nil {
			return mentionAccessError(err)
		}
		if _, err := selectInstanceFields(ctx, s.tx, d, nil, s.p.TenantID, next.ID, read.RowFilter, false); err != nil {
			return mentionAccessError(err)
		}
		// Keep one notice per field so revoking access to all originating fields hides the mentions.
		for _, fieldID := range targets[id] {
			if !fieldAllowed(read.ReadFields, fieldID) {
				return problem(403, "mention_target_forbidden", "包含不能读取此记录属性的账号")
			}
			if _, err := s.tx.ExecContext(ctx, `INSERT INTO af_data_record_notices
				(tenant_id,subject_id,id,schema_id,entity_id,created_at,kind,field_id)
				VALUES (?,?,?,?,?,UTC_TIMESTAMP(6),?,?)`, s.p.TenantID, id, c.NewID(), d.ID, next.ID, accountMentionNoticeKind, fieldID); err != nil {
				return err
			}
		}
		if err := s.h.recheck(ctx, recipient, "instance.read", d.ID, read); err != nil {
			return mentionAccessError(err)
		}
	}
	return nil
}

func mentionAccessError(err error) error {
	var failure *apiError
	if errors.As(err, &failure) && (failure.status == 401 || failure.status == 403 || failure.status == 404) {
		return problem(403, "mention_target_forbidden", "包含不能读取此记录的账号")
	}
	return err
}

func (h *handler) validateMentionTargets(ctx context.Context, check c.MentionTargetCheck) error {
	body, err := json.Marshal(check)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(h.cfg.MetadataURL, "/")+"/internal/identity/mention-targets", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.cfg.ServiceToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return problem(503, "identity_unavailable", "无法确认提及账号")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 {
		return nil
	}
	if resp.StatusCode == 403 {
		return problem(403, "mention_target_forbidden", "包含当前不可提及的账号")
	}
	return problem(503, "identity_unavailable", "无法确认提及账号")
}
