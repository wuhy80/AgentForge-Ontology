package metadata

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

type publicationRow struct {
	p                contracts.Publication
	candidate        contracts.Definition
	digest, previous string
	canReject        bool
}

func scanPublication(row rowScanner) (publicationRow, error) {
	var p publicationRow
	var raw string
	err := row.Scan(&p.p.ID, &p.p.TenantID, &p.p.DefinitionID, &raw, &p.digest, &p.previous, &p.p.Status, &p.p.Error)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(raw), &p.candidate)
	return p, err
}

const publicationColumns = "id,tenant_id,definition_id,candidate_json,candidate_digest,previous_digest,status,error_text"

func (h *handler) publish(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	id := r.PathValue("id")
	if err := h.authorize(r.Context(), p, "definition.publish", id, ""); err != nil {
		return err
	}
	pub, err := h.beginPublication(r.Context(), p.TenantID, id, r)
	if err != nil {
		return err
	}
	if pub.p.Status == "succeeded" {
		writeJSON(w, 200, pub.p)
		return nil
	}
	if pub.p.Status != "committed" && pub.p.Status != "activation_pending" {
		req := contracts.PrepareRequest{OperationID: pub.p.ID, Definition: pub.candidate, PreviousDigest: pub.previous, Digest: pub.digest}
		if err := h.dataCall(r.Context(), p.TenantID, "/internal/publications/prepare", req); err != nil {
			status := "prepare_failed"
			release := false
			var rejected *prepareRejection
			if errors.As(err, &rejected) && pub.canReject && (rejected.status == 400 || rejected.status == 422) {
				status = "rejected"
				release = true
			}
			pub.p.Status = status
			pub.p.Error = err.Error()
			if saveErr := h.savePublicationFailure(r.Context(), &pub, release); saveErr != nil {
				return saveErr
			}
			if pub.p.Status == "preparing" || pub.p.Status == "prepare_failed" || pub.p.Status == "rejected" {
				code := 503
				if pub.p.Status == "rejected" {
					code = 422
				}
				writeJSON(w, code, map[string]any{"publication": pub.p, "error": contracts.Error{Code: "publication_not_completed", Message: "Data准备未完成；按相同If-Match重试可恢复，预检拒绝后可修改草稿"}})
				return nil
			}
		}
		if err := h.commitPublication(r.Context(), &pub); err != nil {
			return err
		}
	}
	if pub.p.Status == "succeeded" {
		writeJSON(w, 200, pub.p)
		return nil
	}
	if pub.p.Status != "committed" && pub.p.Status != "activation_pending" {
		writeJSON(w, 422, map[string]any{"publication": pub.p, "error": contracts.Error{Code: "publication_rejected", Message: "该发布已被预检拒绝"}})
		return nil
	}
	if err := h.dataCall(r.Context(), p.TenantID, "/internal/publications/activate", contracts.ActivateRequest{OperationID: pub.p.ID, DefinitionID: id, Digest: pub.digest}); err != nil {
		pub.p.Status = "activation_pending"
		pub.p.Error = err.Error()
		if saveErr := h.savePublicationFailure(r.Context(), &pub, false); saveErr != nil {
			return saveErr
		}
		if pub.p.Status == "succeeded" {
			writeJSON(w, 200, pub.p)
		} else {
			writeJSON(w, 503, map[string]any{"publication": pub.p, "error": contracts.Error{Code: "activation_pending", Message: "当前定义已提交，Data仍暂停；重试同一发布恢复激活"}})
		}
		return nil
	}
	err = h.withPublicationTx(r.Context(), &pub, func(tx *sql.Tx, stored *publicationRow) error {
		if _, err := tx.ExecContext(r.Context(), "UPDATE af_meta_publications SET status='succeeded',error_text='' WHERE tenant_id=? AND id=?", p.TenantID, pub.p.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE af_meta_tenant_guards SET publication_id='' WHERE tenant_id=? AND publication_id=?", p.TenantID, pub.p.ID); err != nil {
			return err
		}
		stored.p.Status, stored.p.Error = "succeeded", ""
		return nil
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, pub.p)
	return nil
}

func (h *handler) beginPublication(ctx context.Context, tenant, id string, r *http.Request) (publicationRow, error) {
	var pub publicationRow
	err := h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO af_meta_tenant_guards (tenant_id) VALUES (?)", tenant); err != nil {
			return err
		}
		var active string
		if err := tx.QueryRowContext(ctx, "SELECT publication_id FROM af_meta_tenant_guards WHERE tenant_id=? FOR UPDATE", tenant).Scan(&active); err != nil {
			return err
		}
		if active != "" {
			var err error
			pub, err = scanPublication(tx.QueryRowContext(ctx, "SELECT "+publicationColumns+" FROM af_meta_publications WHERE tenant_id=? AND id=?", tenant, active))
			if err != nil {
				return err
			}
			if pub.p.DefinitionID != id {
				return problem(409, "publication_in_progress", "其他定义有未完成生效操作")
			}
			return matchHeader(r, pub.digest)
		}
		row, err := scanDefinition(tx.QueryRowContext(ctx, "SELECT "+definitionColumns+" FROM af_meta_definitions WHERE tenant_id=? AND id=? FOR UPDATE", tenant, id))
		if err != nil {
			return err
		}
		candidate, err := parseDefinition(row.draft)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) && row.currentDigest != "" {
				if err := matchHeader(r, row.currentDigest); err != nil {
					return err
				}
				pub, err = completedPublication(ctx, tx, tenant, id, row.currentDigest)
				return err
			}
			return err
		}
		if err := matchHeader(r, row.draftDigest); err != nil {
			return err
		}
		if err := canonicalDefinition(&candidate, false); err != nil {
			return err
		}
		if row.current.Valid {
			current, err := parseDefinition(row.current)
			if err != nil {
				return err
			}
			if err := compatibleUpdate(current, candidate); err != nil {
				return err
			}
		}
		if err := validateOrg(ctx, tx, candidate); err != nil {
			return err
		}
		if err := validateReferences(ctx, tx, candidate); err != nil {
			return err
		}
		_, nameKey, _ := normalizeName(candidate.Name)
		var duplicate int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM af_meta_definitions WHERE tenant_id=? AND category_id=? AND name_key=? AND id<>?", tenant, candidate.CategoryID, []byte(nameKey), id).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate != 0 {
			return problem(409, "name_conflict", "目标名称已被占用")
		}
		if row.currentDigest == row.draftDigest {
			// Repeated content is a no-op only while already current. A former
			// candidate appearing again represents a new publication intent.
			pub, err = completedPublication(ctx, tx, tenant, id, row.currentDigest)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "UPDATE af_meta_definitions SET draft_json=NULL,draft_digest='' WHERE tenant_id=? AND id=?", tenant, id)
			return err
		}
		pub = publicationRow{p: contracts.Publication{ID: contracts.NewID(), TenantID: tenant, DefinitionID: id, Status: "preparing"}, candidate: candidate, digest: row.draftDigest, previous: row.currentDigest, canReject: true}
		raw, _ := json.Marshal(candidate)
		if _, err := tx.ExecContext(ctx, "INSERT INTO af_meta_publications (id,tenant_id,definition_id,candidate_json,candidate_digest,previous_digest,status,error_text) VALUES (?,?,?,?,?,?,'preparing','')", pub.p.ID, tenant, id, string(raw), pub.digest, pub.previous); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE af_meta_tenant_guards SET publication_id=? WHERE tenant_id=?", pub.p.ID, tenant)
		return err
	})
	return pub, err
}

func completedPublication(ctx context.Context, tx *sql.Tx, tenant, id, digest string) (publicationRow, error) {
	return scanPublication(tx.QueryRowContext(ctx, "SELECT "+publicationColumns+" FROM af_meta_publications WHERE tenant_id=? AND definition_id=? AND candidate_digest=? AND status='succeeded' ORDER BY (SELECT created_at FROM af_meta_outbox WHERE af_meta_outbox.id=af_meta_publications.id) DESC,id DESC LIMIT 1", tenant, id, digest))
}

func validateReferences(ctx context.Context, tx *sql.Tx, d contracts.Definition) error {
	refs := map[string]contracts.Definition{}
	for _, id := range dependencyIDs(d) {
		if id == d.ID {
			return problem(400, "dependency_cycle", "定义不能直接引用自身")
		}
		var raw sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT current_json FROM af_meta_definitions WHERE tenant_id=? AND id=? AND status='active' AND current_json IS NOT NULL", d.TenantID, id).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return problem(422, "invalid_dependency", "引用必须属于同租户且已生效")
			}
			return err
		}
		ref, err := parseDefinition(raw)
		if err != nil {
			return err
		}
		refs[id] = ref
	}
	checkProps := func(ps []contracts.Property) error {
		for _, p := range ps {
			if p.EnumID != "" {
				ref := refs[p.EnumID]
				if ref.Kind != contracts.KindEnum {
					return problem(422, "invalid_dependency", "enumId必须引用Enum")
				}
				if len(p.Default) > 0 && string(p.Default) != "null" {
					var code string
					if err := json.Unmarshal(p.Default, &code); err != nil {
						return problem(400, "invalid_default", "Enum默认值必须为业务code")
					}
					found := false
					for _, v := range ref.Enum.Values {
						if v.Code == code && !v.Deprecated {
							found = true
						}
					}
					if !found {
						return problem(400, "invalid_default", "Enum默认值不是可写业务code")
					}
				}
			}
		}
		return nil
	}
	if d.Schema != nil {
		if err := checkProps(d.Schema.Properties); err != nil {
			return err
		}
	}
	if d.Link != nil {
		if refs[d.Link.SourceSchemaID].Kind != contracts.KindSchema || refs[d.Link.TargetSchemaID].Kind != contracts.KindSchema {
			return problem(422, "invalid_endpoint", "Link端点必须引用Schema")
		}
	}
	for _, c := range []*contracts.Callable{d.Action, d.Function} {
		if c != nil {
			if err := checkProps(c.Input); err != nil {
				return err
			}
			if err := checkProps(c.Output); err != nil {
				return err
			}
			if c.FunctionID != "" && refs[c.FunctionID].Kind != contracts.KindFunction {
				return problem(422, "invalid_dependency", "实现必须引用Function")
			}
		}
	}
	// Callable-to-callable dependencies may form longer cycles. The bounded
	// traversal runs under the tenant write guard so concurrent edits cannot
	// invalidate the checked graph before current is replaced.
	seen := map[string]bool{}
	queue := dependencyIDs(d)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == d.ID {
			return problem(400, "dependency_cycle", "定义引用成环")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if len(seen) > 4096 {
			return problem(422, "dependency_budget", "依赖图超过当前检查预算")
		}
		rows, err := tx.QueryContext(ctx, "SELECT target_id FROM af_meta_dependencies WHERE tenant_id=? AND source_id=?", d.TenantID, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var next string
			if err := rows.Scan(&next); err != nil {
				rows.Close()
				return err
			}
			queue = append(queue, next)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// withPublicationTx serializes phase changes against the durable tenant guard.
// An already completed operation is safe to replay even if a later operation
// now owns that guard; a stale retry must never mutate the newer operation.
func (h *handler) withPublicationTx(ctx context.Context, pub *publicationRow, fn func(*sql.Tx, *publicationRow) error) error {
	var stored publicationRow
	err := h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		var active string
		if err := tx.QueryRowContext(ctx, "SELECT publication_id FROM af_meta_tenant_guards WHERE tenant_id=? FOR UPDATE", pub.p.TenantID).Scan(&active); err != nil {
			return err
		}
		var err error
		stored, err = scanPublication(tx.QueryRowContext(ctx, "SELECT "+publicationColumns+" FROM af_meta_publications WHERE tenant_id=? AND id=? FOR UPDATE", pub.p.TenantID, pub.p.ID))
		if err != nil {
			return err
		}
		if active != pub.p.ID {
			if stored.p.Status == "succeeded" || stored.p.Status == "rejected" {
				return nil
			}
			return problem(409, "publication_in_progress", "该租户发布状态已改变，请先读取权威生效状态")
		}
		return fn(tx, &stored)
	})
	if err == nil {
		*pub = stored
	}
	return err
}

func (h *handler) commitPublication(ctx context.Context, pub *publicationRow) error {
	return h.withPublicationTx(ctx, pub, func(tx *sql.Tx, stored *publicationRow) error {
		if stored.p.Status == "committed" || stored.p.Status == "activation_pending" {
			return nil
		}
		row, err := scanDefinition(tx.QueryRowContext(ctx, "SELECT "+definitionColumns+" FROM af_meta_definitions WHERE tenant_id=? AND id=? FOR UPDATE", pub.p.TenantID, pub.p.DefinitionID))
		if err != nil {
			return err
		}
		if row.currentDigest == pub.digest {
			return nil
		}
		if row.currentDigest != pub.previous || row.draftDigest != pub.digest {
			return problem(409, "publication_conflict", "当前定义或草稿与发布记录不一致")
		}
		raw, _ := json.Marshal(pub.candidate)
		_, key, _ := normalizeName(pub.candidate.Name)
		if _, err := tx.ExecContext(ctx, "UPDATE af_meta_definitions SET current_json=?,current_digest=?,draft_json=NULL,draft_digest='',status='active',name=?,name_key=? WHERE tenant_id=? AND id=?", string(raw), pub.digest, pub.candidate.Name, []byte(key), pub.p.TenantID, pub.p.DefinitionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM af_meta_dependencies WHERE tenant_id=? AND source_id=?", pub.p.TenantID, pub.p.DefinitionID); err != nil {
			return err
		}
		for _, target := range dependencyIDs(pub.candidate) {
			if _, err := tx.ExecContext(ctx, "INSERT INTO af_meta_dependencies (tenant_id,source_id,target_id) VALUES (?,?,?)", pub.p.TenantID, pub.p.DefinitionID, target); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO af_meta_outbox (id,tenant_id,event_type,object_id,payload) VALUES (?,?,'definition.activated',?,?)", pub.p.ID, pub.p.TenantID, pub.p.DefinitionID, string(raw)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE af_meta_publications SET status='committed',error_text='' WHERE tenant_id=? AND id=?", pub.p.TenantID, pub.p.ID); err != nil {
			return err
		}
		stored.p.Status, stored.p.Error = "committed", ""
		return nil
	})
}

func (h *handler) savePublicationFailure(ctx context.Context, pub *publicationRow, release bool) error {
	return h.withPublicationTx(ctx, pub, func(tx *sql.Tx, stored *publicationRow) error {
		// Another retry may have advanced the same operation while this
		// network call was in flight. Failures cannot move it backwards.
		if pub.p.Status == "activation_pending" {
			if stored.p.Status != "committed" && stored.p.Status != "activation_pending" {
				return nil
			}
		} else if stored.p.Status != "preparing" && stored.p.Status != "prepare_failed" {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE af_meta_publications SET status=?,error_text=? WHERE tenant_id=? AND id=?", pub.p.Status, pub.p.Error, pub.p.TenantID, pub.p.ID); err != nil {
			return err
		}
		stored.p.Status, stored.p.Error = pub.p.Status, pub.p.Error
		if release {
			_, err := tx.ExecContext(ctx, "UPDATE af_meta_tenant_guards SET publication_id='' WHERE tenant_id=? AND publication_id=?", pub.p.TenantID, pub.p.ID)
			return err
		}
		return nil
	})
}

type prepareRejection struct{ status int }

func (e *prepareRejection) Error() string {
	return fmt.Sprintf("Data preflight rejected (HTTP %d)", e.status)
}
func (h *handler) dataCall(ctx context.Context, tenant, path string, body any) error {
	_, err := h.dataExchange(ctx, tenant, path, body)
	return err
}

func (h *handler) dataExchange(ctx context.Context, tenant, path string, body any) (contracts.PrepareResult, error) {
	var result contracts.PrepareResult
	if h.cfg.DataURL == "" || h.cfg.ServiceToken == "" {
		return result, fmt.Errorf("Data coordination is not configured")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(h.cfg.DataURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.cfg.ServiceToken)
	req.Header.Set("X-Tenant-ID", tenant)
	resp, err := h.client.Do(req)
	if err != nil {
		return result, fmt.Errorf("Data coordination unavailable; retry this publication")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		if (path == "/internal/publications/prepare" || path == "/internal/publications/preview") && (resp.StatusCode == 400 || resp.StatusCode == 409 || resp.StatusCode == 422) {
			return result, &prepareRejection{status: resp.StatusCode}
		}
		return result, fmt.Errorf("Data coordination incomplete (HTTP %d)", resp.StatusCode)
	}
	if path == "/internal/publications/prepare" || path == "/internal/publications/preview" {
		decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
		if err := decoder.Decode(&result); err != nil {
			return result, fmt.Errorf("invalid Data preparation acknowledgement")
		}
		expected := body.(contracts.PrepareRequest)
		status := "prepared"
		if path == "/internal/publications/preview" {
			status = "preview"
		}
		validStatus := result.Status == status || path == "/internal/publications/prepare" && result.Status == "ready"
		if result.OperationID != expected.OperationID || !validStatus {
			return result, fmt.Errorf("Data preparation acknowledgement does not match operation")
		}
	}
	return result, nil
}

func (h *handler) preview(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	row, err := scanDefinition(h.db.QueryRowContext(r.Context(), "SELECT "+definitionColumns+" FROM af_meta_definitions WHERE tenant_id=? AND id=?", p.TenantID, r.PathValue("id")))
	if err != nil {
		return err
	}
	if err := h.authorize(r.Context(), p, "definition.edit", row.d.ID, row.d.Kind); err != nil {
		return err
	}
	raw, digest := row.draft, row.draftDigest
	if !raw.Valid {
		raw, digest = row.current, row.currentDigest
	}
	d, err := parseDefinition(raw)
	if err != nil {
		return err
	}
	if err := canonicalDefinition(&d, false); err != nil {
		return err
	}
	result, err := h.dataExchange(r.Context(), p.TenantID, "/internal/publications/preview", contracts.PrepareRequest{OperationID: contracts.NewID(), Definition: d, Digest: digest, PreviousDigest: row.currentDigest})
	if err != nil {
		var rejection *prepareRejection
		if errors.As(err, &rejection) {
			return problem(rejection.status, "preview_rejected", "候选不能进入当前Data存储计划，未执行或生效")
		}
		return problem(503, "preview_unavailable", "无法完成Data计划预览；候选未执行或生效")
	}
	w.Header().Set("ETag", etagHeader(digest))
	writeJSON(w, 200, result)
	return nil
}

func (h *handler) getPublication(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
	pub, err := scanPublication(h.db.QueryRowContext(r.Context(), "SELECT "+publicationColumns+" FROM af_meta_publications WHERE tenant_id=? AND id=?", p.TenantID, r.PathValue("id")))
	if err != nil {
		return err
	}
	if err := h.authorize(r.Context(), p, "definition.publish", pub.p.DefinitionID, ""); err != nil {
		return err
	}
	writeJSON(w, 200, pub.p)
	return nil
}
