package metadata

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const disabledDefinition = "disabled"

func supportsMappedSchemaLifecycle(d c.Definition) bool {
	return d.Kind == c.KindSchema && d.Schema != nil && (d.Schema.Mode == "db_2_schema" || d.Schema.Mode == "schema_2_db") &&
		d.Schema.StorageLayout == "single" && d.Schema.StorageBinding != nil
}

func supportsStorageLifecycle(d c.Definition) bool {
	switch d.Kind {
	case c.KindLink:
		if d.Link != nil && d.Link.StorageBinding != nil {
			switch d.Link.StorageBinding.Mode {
			case "managed", "junction", "foreign_key":
				return true
			}
		}
	case c.KindSchema:
		return d.Schema != nil && d.Schema.Mode == "schema_2_db" &&
			d.Schema.StorageLayout == "single" && d.Schema.StorageBinding == nil
	}
	return false
}

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
			if row.status == "deleted" {
				return sql.ErrNoRows
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
			storedDefinition := supportsStorageLifecycle(d) || supportsMappedSchemaLifecycle(d)
			if !storedDefinition && d.Kind != c.KindEnum && (callable == nil || callable.SideEffects != "" && callable.SideEffects != "none" || len(callable.DataWrite) != 0) {
				return problem(422, "unsupported_lifecycle", "当前支持 Enum、托管和映射单表 Schema、已配置存储的关系及无副作用的 Action 和 Function")
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
				} else if d.Kind == c.KindEnum {
					if err := h.checkEnumDisable(ctx, tx, p.TenantID, id); err != nil {
						return err
					}
				} else if !storedDefinition {
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
					policies := h.cfg.CallablePolicyReferences
					if policies == nil {
						return problem(503, "policy_impact_unavailable", "无法复核定义相关策略")
					}
					referenced, err = policies.HasCallablePolicyReference(ctx, tx, p.TenantID, id, d.Kind, !preview)
					if err != nil {
						return err
					}
					if referenced {
						return problem(409, "definition_policy_in_use", "存在引用此定义的调用授权，请由权限管理员处理后重试")
					}
				}
			}
			if storedDefinition {
				if err := h.checkManagedLinkLifecycle(ctx, tx, d, row.currentDigest, restore, preview); err != nil {
					return err
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

func (h *handler) checkManagedLinkLifecycle(ctx context.Context, tx *sql.Tx, d c.Definition, digest string, restore, preview bool) error {
	policies := h.cfg.CallablePolicyReferences
	checkPolicy := func(lock bool) error {
		if policies == nil {
			return problem(503, "policy_impact_unavailable", "无法复核定义相关策略")
		}
		referenced, err := policies.HasCallablePolicyReference(ctx, tx, d.TenantID, d.ID, d.Kind, lock)
		if err != nil {
			return err
		}
		if referenced {
			return problem(409, "definition_policy_in_use", "仍有策略引用此定义")
		}
		return nil
	}
	if !restore {
		if d.Kind == c.KindSchema {
			definitions, err := readImpactSnapshot(ctx, tx, d.TenantID)
			if err != nil {
				return err
			}
			for _, definition := range definitions {
				if len(definition.refs[d.ID]) != 0 {
					return problem(409, "definition_in_use", "当前或草稿定义仍引用此对象模型")
				}
			}
		}
		if err := checkPolicy(false); err != nil {
			return err
		}
	}
	if err := h.checkManagedStorage(ctx, d, "managed-storage-lifecycle", c.ManagedLinkLifecycle{Digest: digest, Restore: restore, Preview: preview}); err != nil {
		return err
	}
	if !restore && !preview {
		// Take the policy lock after draining Data to avoid waiting on a
		// transaction that still needs a live authorization check.
		return checkPolicy(true)
	}
	return nil
}

func (h *handler) checkManagedStorage(ctx context.Context, d c.Definition, route string, command c.ManagedLinkLifecycle) error {
	if h.cfg.DataURL == "" || len(h.cfg.ServiceToken) < 32 {
		return problem(503, "link_lifecycle_unavailable", "无法复核定义存储")
	}
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	body, _ := json.Marshal(command)
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		strings.TrimRight(h.cfg.DataURL, "/")+"/internal/definitions/"+url.PathEscape(d.ID)+"/"+route, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.cfg.ServiceToken)
	req.Header.Set("X-Tenant-ID", d.TenantID)
	client := *h.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return problem(503, "link_lifecycle_unavailable", "定义存储未确认，请重试原操作核对状态")
	}
	defer response.Body.Close()
	if response.StatusCode != 204 {
		failure := lifecycleImpactFailure(response)
		if failure != nil {
			return failure
		}
		var conflict c.Error
		if response.StatusCode == 409 && json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&conflict) == nil {
			switch conflict.Code {
			case "definition_data_in_use":
				return problem(409, conflict.Code, "定义仍有业务数据")
			case "definition_retained_records":
				return problem(409, conflict.Code, "定义仍有保留的业务记录")
			case "definition_saved_query_in_use":
				return problem(409, conflict.Code, "仍有保存查询引用此定义")
			case "mapping_unavailable":
				return problem(409, conflict.Code, "定义存储未就绪或已改变")
			}
		}
		return problem(503, "link_lifecycle_unavailable", "无法复核定义存储")
	}
	return nil
}

func (h *handler) checkEnumDisable(ctx context.Context, tx *sql.Tx, tenant, id string) error {
	definitions, err := readImpactSnapshot(ctx, tx, tenant)
	if err != nil {
		return err
	}
	for _, definition := range definitions {
		if len(definition.refs[id]) > 0 {
			return problem(409, "definition_in_use", "当前或草稿定义仍引用此枚举")
		}
	}
	if h.cfg.DataURL == "" || len(h.cfg.ServiceToken) < 32 {
		return problem(503, "saved_query_impact_unavailable", "无法复核保存查询引用")
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet,
		strings.TrimRight(h.cfg.DataURL, "/")+"/internal/definitions/"+url.PathEscape(id)+"/saved-query-impact", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.cfg.ServiceToken)
	req.Header.Set("X-Tenant-ID", tenant)
	client := *h.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return problem(503, "saved_query_impact_unavailable", "无法复核保存查询引用")
	}
	defer response.Body.Close()
	if err := lifecycleImpactFailure(response); err != nil {
		return err
	}
	var impact struct {
		Referenced *bool `json:"referenced"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&impact) != nil || impact.Referenced == nil {
		return problem(503, "saved_query_impact_unavailable", "无法复核保存查询引用")
	}
	if *impact.Referenced {
		return problem(409, "definition_saved_query_in_use", "仍有保存查询引用此枚举")
	}
	return nil
}

func lifecycleImpactFailure(response *http.Response) error {
	if response.StatusCode != http.StatusUnprocessableEntity {
		return nil
	}
	var failure c.Error
	if json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&failure) == nil && failure.Code == "saved_query_impact_budget" {
		return problem(422, failure.Code, "租户保存查询超过影响检查预算，无法确认是否可停用")
	}
	return nil
}
