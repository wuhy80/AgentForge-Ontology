package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) deleteDraft(preview bool) endpoint {
	return func(w http.ResponseWriter, r *http.Request, p c.Principal) error {
		ctx, id := r.Context(), r.PathValue("id")
		w.Header().Set("Cache-Control", "no-store")
		check := func(tx *sql.Tx) error {
			row, err := scanDefinition(tx.QueryRowContext(ctx, "SELECT "+definitionColumns+" FROM af_meta_definitions WHERE tenant_id=? AND id=?", p.TenantID, id))
			if err != nil {
				return err
			}
			for _, op := range []string{"definition.edit", "definition.publish"} {
				if err := h.authorize(ctx, p, op, id, row.d.Kind); err != nil {
					return err
				}
			}
			if preview {
				var publication string
				err = tx.QueryRowContext(ctx, "SELECT publication_id FROM af_meta_tenant_guards WHERE tenant_id=?", p.TenantID).Scan(&publication)
				if err != nil && err != sql.ErrNoRows {
					return err
				}
				if publication != "" {
					return problem(409, "publication_in_progress", "该租户发布尚未完成，请稍后重试")
				}
			}
			if row.status == "deleted" && !preview {
				return matchHeader(r, row.draftDigest)
			}
			if row.status != "draft" || row.current.Valid || !row.draft.Valid {
				return problem(409, "unsupported_delete_state", "当前只支持删除从未发布的草稿")
			}
			if err := matchHeader(r, row.draftDigest); err != nil {
				return err
			}
			var publication bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM af_meta_publications WHERE tenant_id=? AND definition_id=?)", p.TenantID, id).Scan(&publication); err != nil {
				return err
			}
			if publication {
				return problem(409, "definition_publication_exists", "定义存在发布记录，无法删除")
			}
			definitions, err := readImpactSnapshot(ctx, tx, p.TenantID)
			if err != nil {
				return err
			}
			for _, definition := range definitions {
				if len(definition.refs[id]) != 0 {
					return problem(409, "definition_in_use", "当前或草稿定义仍引用此定义")
				}
			}
			policies, ok := h.cfg.CallablePolicyReferences.(interface {
				HasDeletionPolicyReference(context.Context, *sql.Tx, string, string, c.Kind, bool) (bool, error)
			})
			if !ok {
				return problem(503, "policy_impact_unavailable", "无法复核定义相关策略")
			}
			referenced, err := policies.HasDeletionPolicyReference(ctx, tx, p.TenantID, id, row.d.Kind, !preview)
			if err != nil {
				return err
			}
			if referenced {
				return problem(409, "definition_policy_in_use", "仍有策略引用此定义")
			}
			if preview {
				return nil
			}
			if _, err := tx.ExecContext(ctx, "UPDATE af_meta_definitions SET status='deleted',draft_json=NULL WHERE tenant_id=? AND id=?", p.TenantID, id); err != nil {
				return err
			}
			payload, err := json.Marshal(map[string]string{"actor": p.Subject, "kind": string(row.d.Kind), "digest": row.draftDigest})
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO af_meta_outbox (id,tenant_id,event_type,object_id,payload) VALUES (?,?,?,?,?)", c.NewID(), p.TenantID, "definition.deleted", id, string(payload))
			return err
		}
		if preview {
			tx, err := h.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := check(tx); err != nil {
				return err
			}
		} else if err := h.withTenantTx(ctx, p.TenantID, "", check); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}
