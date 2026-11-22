package metadata

import (
	"database/sql"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const disabledDefinition = "disabled"

func (h *handler) callableLifecycle(restore, preview bool) endpoint {
	return func(w http.ResponseWriter, r *http.Request, p c.Principal) error {
		ctx := r.Context()
		id := r.PathValue("id")
		var result c.CurrentDefinition
		check := func(tx *sql.Tx) error {
			statement := "SELECT " + definitionColumns + " FROM af_meta_definitions WHERE tenant_id=? AND id=?"
			if !preview {
				statement += " FOR UPDATE"
			}
			row, err := scanDefinition(tx.QueryRowContext(ctx, statement, p.TenantID, id))
			if err != nil {
				return err
			}
			if err = h.authorize(ctx, p, "definition.publish", id, row.d.Kind); err != nil {
				return err
			}
			if err = h.authorize(ctx, p, "definition.read", id, row.d.Kind); err != nil {
				return err
			}
			if preview {
				var active string
				err := tx.QueryRowContext(ctx, "SELECT publication_id FROM af_meta_tenant_guards WHERE tenant_id=?", p.TenantID).Scan(&active)
				if err != nil && err != sql.ErrNoRows {
					return err
				}
				if active != "" {
					return problem(409, "publication_in_progress", "该租户发布状态已改变，请先读取权威生效状态")
				}
			}
			if err = matchHeader(r, row.currentDigest); err != nil {
				return err
			}
			d, err := parseDefinition(row.current)
			if err != nil {
				return err
			}
			callable := d.Action
			if d.Kind == c.KindFunction {
				callable = d.Function
			}
			if callable == nil || callable.SideEffects != "" && callable.SideEffects != "none" || len(callable.DataWrite) != 0 {
				return problem(422, "unsupported_lifecycle", "当前仅支持停用或恢复无副作用的 Action 和 Function")
			}
			if row.draft.Valid {
				return problem(409, "lifecycle_draft_exists", "请先处理已保存的草稿")
			}
			d, err = resolveEnumReferences(ctx, tx, d)
			if err != nil {
				return err
			}
			next := disabledDefinition
			if restore {
				next = "active"
			}
			if row.status != next {
				if row.status != "active" && row.status != disabledDefinition {
					return problem(409, "invalid_lifecycle_state", "定义当前状态不可转换")
				}
				if restore {
					if err := validateReferences(ctx, tx, d); err != nil {
						return err
					}
				} else {
					var referenced bool
					err = tx.QueryRowContext(ctx, `SELECT
 EXISTS (SELECT 1 FROM af_meta_dependencies WHERE tenant_id=? AND target_id=?) OR
 EXISTS (SELECT 1 FROM af_meta_definitions WHERE tenant_id=? AND kind='action' AND draft_json IS NOT NULL
 AND JSON_UNQUOTE(JSON_EXTRACT(draft_json,'$.action.functionId'))=?)`, p.TenantID, id, p.TenantID, id).Scan(&referenced)
					if err != nil {
						return err
					}
					if referenced {
						return problem(409, "definition_in_use", "当前或草稿定义仍引用此定义")
					}
					if policies := h.cfg.CallablePolicyReferences; policies != nil {
						referenced, err = policies.HasCallablePolicyReference(ctx, tx, p.TenantID, id, d.Kind, !preview)
						if err != nil {
							return err
						}
						if referenced {
							return problem(409, "definition_policy_in_use", "存在引用此定义的调用授权，请由权限管理员处理后重试")
						}
					}
				}
			}
			if !preview && row.status != next {
				if _, err := tx.ExecContext(ctx, "UPDATE af_meta_definitions SET status=? WHERE tenant_id=? AND id=?", next, p.TenantID, id); err != nil {
					return err
				}
				event := "definition.disabled"
				if restore {
					event = "definition.restored"
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO af_meta_outbox (id,tenant_id,event_type,object_id,payload) VALUES (?,?,?,?,?)",
					c.NewID(), p.TenantID, event, id, "{\"status\":\""+next+"\"}"); err != nil {
					return err
				}
			}
			// Keep the retained definition readable to management callers.
			result = c.CurrentDefinition{Definition: d, ETag: row.currentDigest, Status: next}
			return nil
		}
		var err error
		if preview {
			w.Header().Set("Cache-Control", "no-store")
			tx, beginErr := h.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
			if beginErr != nil {
				return beginErr
			}
			defer tx.Rollback()
			err = check(tx)
			if err == nil {
				err = tx.Commit()
			}
		} else {
			err = h.withTenantTx(ctx, p.TenantID, "", check)
		}
		if err != nil {
			return err
		}
		w.Header().Set("ETag", etagHeader(result.ETag))
		if preview {
			writeJSON(w, http.StatusOK, c.CallableLifecyclePreview{DefinitionID: id, TargetStatus: result.Status})
		} else {
			writeJSON(w, http.StatusOK, result)
		}
		return nil
	}
}
