package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

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
			if row.status == "deleted" {
				if preview {
					return sql.ErrNoRows
				}
				return matchHeader(r, row.draftDigest)
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
			draftOnly := row.status == "draft" && !row.current.Valid && row.draft.Valid
			disabledEnum := row.status == disabledDefinition && row.d.Kind == c.KindEnum && row.current.Valid && !row.draft.Valid
			var storedDefinition bool
			var disabledCallable bool
			if row.status == disabledDefinition && row.current.Valid && !row.draft.Valid {
				d, err := parseDefinition(row.current)
				if err != nil {
					return err
				}
				storedDefinition = supportsStorageLifecycle(d)
				callable := callableBody(d)
				disabledCallable = callable != nil && (callable.SideEffects == "" || callable.SideEffects == "none") && len(callable.DataWrite) == 0
			}
			if !draftOnly && !disabledEnum && !storedDefinition && !disabledCallable {
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
			if storedDefinition {
				if err := h.checkManagedStorage(ctx, row.d, "managed-storage-delete-check", c.ManagedLinkLifecycle{Digest: digest, Preview: preview}); err != nil {
					return err
				}
			}
			if disabledCallable {
				if err := h.checkCallableDeletionExecution(ctx, p.TenantID, id); err != nil {
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

func (h *handler) checkCallableDeletionExecution(ctx context.Context, tenant, id string) error {
	unavailable := problem(503, "execution_impact_unavailable", "无法复核未完成执行")
	if h.cfg.ExecutionURL == "" || len(h.cfg.ServiceToken) < 32 {
		return unavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.cfg.ExecutionURL, "/")+
		"/internal/definitions/"+url.PathEscape(id)+"/execution-impact", nil)
	if err != nil {
		return unavailable
	}
	req.Header.Set("Authorization", "Bearer "+h.cfg.ServiceToken)
	req.Header.Set("X-Tenant-ID", tenant)
	client := *h.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return unavailable
	}
	defer response.Body.Close()
	var impact struct{ Pending *bool }
	if response.StatusCode != 200 || json.NewDecoder(http.MaxBytesReader(nil, response.Body, 1024)).Decode(&impact) != nil || impact.Pending == nil {
		return unavailable
	}
	if *impact.Pending {
		return problem(409, "definition_execution_in_use", "定义仍有未完成执行")
	}
	return nil
}
