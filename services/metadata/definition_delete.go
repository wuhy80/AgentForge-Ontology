package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type deletionPolicyReferences interface {
	HasDeletionPolicyReference(context.Context, *sql.Tx, string, string, c.Kind, bool) (bool, error)
}

func (h *handler) supportsDraftDeletion() bool {
	_, ok := h.cfg.CallablePolicyReferences.(deletionPolicyReferences)
	return ok
}

func (h *handler) deleteDefinition(preview bool) endpoint {
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
			if row.status == "deleted" {
				if preview {
					return sql.ErrNoRows
				}
				return matchHeader(r, row.draftDigest)
			}
			draftOnly := row.status == "draft" && !row.current.Valid && row.draft.Valid
			disabledEnum := row.status == disabledDefinition && row.d.Kind == c.KindEnum && row.current.Valid && !row.draft.Valid
			var managedStorage bool
			if row.status == disabledDefinition && row.current.Valid && !row.draft.Valid {
				d, err := parseDefinition(row.current)
				if err != nil {
					return err
				}
				managedStorage = d.Kind == c.KindSchema && d.Schema != nil && d.Schema.Mode == "schema_2_db" &&
					d.Schema.StorageLayout == "single" && d.Schema.StorageBinding == nil ||
					d.Kind == c.KindLink && d.Link != nil && d.Link.StorageBinding != nil && d.Link.StorageBinding.Mode == "managed"
			}
			if !draftOnly && !disabledEnum && !managedStorage {
				return problem(409, "unsupported_delete_state", "当前只支持删除从未发布的草稿或受支持的已停用定义")
			}
			digest := row.draftDigest
			publicationQuery := "SELECT EXISTS(SELECT 1 FROM af_meta_publications WHERE tenant_id=? AND definition_id=?"
			if !draftOnly {
				digest = row.currentDigest
				publicationQuery += " AND status NOT IN ('succeeded','rejected')"
			}
			if err := matchHeader(r, digest); err != nil {
				return err
			}
			var publication bool
			if err := tx.QueryRowContext(ctx, publicationQuery+")", p.TenantID, id).Scan(&publication); err != nil {
				return err
			}
			if publication {
				return problem(409, "definition_publication_exists", "定义存在阻断删除的发布记录")
			}
			if disabledEnum {
				if err := h.checkEnumDisable(ctx, tx, p.TenantID, id); err != nil {
					return err
				}
			} else {
				definitions, err := readImpactSnapshot(ctx, tx, p.TenantID)
				if err != nil {
					return err
				}
				for _, definition := range definitions {
					if len(definition.refs[id]) != 0 {
						return problem(409, "definition_in_use", "当前或草稿定义仍引用此定义")
					}
				}
			}
			if managedStorage {
				if err := h.checkManagedStorage(ctx, row.d, "managed-storage-delete-check", c.ManagedLinkLifecycle{Digest: digest, Preview: preview}); err != nil {
					return err
				}
			}
			policies, ok := h.cfg.CallablePolicyReferences.(deletionPolicyReferences)
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
			if _, err := tx.ExecContext(ctx, "UPDATE af_meta_definitions SET status='deleted',current_json=NULL,current_digest='',draft_json=NULL,draft_digest=? WHERE tenant_id=? AND id=?", digest, p.TenantID, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM af_meta_dependencies WHERE tenant_id=? AND source_id=?", p.TenantID, id); err != nil {
				return err
			}
			payload, err := json.Marshal(map[string]string{"actor": p.Subject, "kind": string(row.d.Kind), "digest": digest})
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
