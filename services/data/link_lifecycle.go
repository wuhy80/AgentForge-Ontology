package data

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/go-sql-driver/mysql"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) managedLinkLifecycleHTTP(w http.ResponseWriter, r *http.Request) {
	tenant, err := h.internalTenant(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	var command c.ManagedLinkLifecycle
	if err = decode(w, r, &command); err != nil {
		writeError(w, err)
		return
	}
	if !uuidPattern.MatchString(id) || len(command.Digest) != 64 {
		writeError(w, problem(422, "invalid_lifecycle", "定义标识或摘要无效"))
		return
	}
	deletionCheck := r.Pattern == "POST /internal/definitions/{id}/managed-storage-delete-check"
	if deletionCheck && command.Restore {
		writeError(w, problem(422, "invalid_lifecycle", "删除检查不能请求恢复"))
		return
	}
	ctx := r.Context()
	check := func(tx *sql.Tx) error {
		statement := "SELECT definition,digest,status FROM af_data_gates WHERE tenant_id=? AND definition_id=?"
		if !command.Preview {
			statement += " FOR UPDATE"
		}
		var raw, digest, status string
		err := tx.QueryRowContext(ctx, statement, tenant, id).Scan(&raw, &digest, &status)
		if err == sql.ErrNoRows || err == nil && (digest != command.Digest || status != "ready" && status != "lifecycle_paused") {
			return problem(409, "mapping_unavailable", "定义存储未就绪或已改变")
		}
		if err != nil {
			return err
		}
		if deletionCheck && status != "lifecycle_paused" {
			return problem(409, "mapping_unavailable", "删除检查要求存储已停用")
		}
		var definition c.Definition
		if err := json.Unmarshal([]byte(raw), &definition); err != nil {
			return err
		}
		link := definition.Kind == c.KindLink && definition.Link != nil && definition.Link.StorageBinding != nil && definition.Link.StorageBinding.Mode == "managed"
		schema := definition.Kind == c.KindSchema && definition.Schema != nil && definition.Schema.Mode == "schema_2_db" &&
			definition.Schema.StorageLayout == "single" && definition.Schema.StorageBinding == nil
		if definition.ID != id || definition.TenantID != tenant || (!link && !schema) {
			return problem(422, "unsupported_lifecycle", "当前只支持平台托管关系和托管单表对象")
		}
		table := tableName(id)
		verify := func() error { return verifyTable(ctx, tx, definition) }
		if link {
			table = linkTableName(id)
			verify = func() error { return verifyLinkTable(ctx, tx, definition) }
		}
		var hasData bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+quote(table)+" WHERE tenant_id=?)", tenant).Scan(&hasData); err != nil {
			var mysqlErr *mysql.MySQLError
			if errors.As(err, &mysqlErr) && (mysqlErr.Number == 1146 || mysqlErr.Number == 1054) {
				return problem(409, "mapping_unavailable", "物理结构无法确认")
			}
			return err
		}
		next := "ready"
		if err := verify(); err != nil {
			return problem(409, "mapping_unavailable", "物理结构无法确认")
		}
		if !command.Restore {
			if hasData {
				return problem(409, "definition_data_in_use", "定义仍有实例")
			}
			if deletionCheck && schema {
				var retained bool
				err := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM af_data_property_files WHERE tenant_id=? AND schema_id=?) OR
 EXISTS(SELECT 1 FROM af_data_record_comments WHERE tenant_id=? AND schema_id=?)`, tenant, id, tenant, id).Scan(&retained)
				if err != nil {
					return err
				}
				if retained {
					return problem(409, "definition_data_in_use", "定义仍有附件或评论")
				}
			}
			impact, err := savedQueryImpact(ctx, tx, tenant, id)
			if err != nil {
				return err
			}
			if impact.Referenced {
				return problem(409, "definition_saved_query_in_use", "仍有保存查询引用此定义")
			}
			next = "lifecycle_paused"
		}
		if !deletionCheck && !command.Preview && status != next {
			_, err = tx.ExecContext(ctx, "UPDATE af_data_gates SET status=? WHERE tenant_id=? AND definition_id=?", next, tenant, id)
		}
		return err
	}
	if command.Preview {
		w.Header().Set("Cache-Control", "no-store")
		tx, beginErr := h.cfg.Engine.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
		if beginErr != nil {
			writeError(w, beginErr)
			return
		}
		defer tx.Rollback()
		err = check(tx)
		if err == nil {
			err = tx.Commit()
		}
	} else {
		err = h.cfg.Engine.WithTx(ctx, check)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (h *handler) lockSavedQueryDefinitions(r *http.Request, tx *sql.Tx, tenant string, q c.QuerySpec) error {
	ids := []string{q.From.SchemaID}
	if q.Relation != nil {
		ids = append(ids, q.Relation.LinkID)
	}
	if q.Rows != nil {
		for _, node := range q.Rows.Nodes {
			ids = append(ids, node.SchemaID)
		}
		for _, join := range q.Rows.Joins {
			ids = append(ids, join.LinkID)
		}
	}
	sort.Strings(ids)
	previous := ""
	for _, id := range ids {
		if id == previous {
			continue
		}
		previous = id
		var status string
		err := tx.QueryRowContext(r.Context(), "SELECT status FROM af_data_gates WHERE tenant_id=? AND definition_id=? FOR UPDATE", tenant, id).Scan(&status)
		if err == sql.ErrNoRows || err == nil && status != "ready" {
			return problem(409, "mapping_unavailable", "保存查询引用的定义存储未就绪")
		}
		if err != nil {
			return err
		}
		if _, err := h.current(r.Context(), tenant, id); err != nil {
			return err
		}
	}
	return nil
}
