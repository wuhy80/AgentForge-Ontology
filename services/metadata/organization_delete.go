package metadata

import (
	"database/sql"
	"net/http"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) deleteOrganization(kind string) endpoint {
	return func(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
		ctx, id := r.Context(), r.PathValue("id")
		if err := h.authorize(ctx, p, "organization.edit", "", ""); err != nil {
			return err
		}
		previous, _, err := normalizeName(r.URL.Query().Get("previousName"))
		if err != nil {
			return err
		}
		err = h.withTenantTx(ctx, p.TenantID, "", func(tx *sql.Tx) error {
			var name string
			if err := tx.QueryRowContext(ctx, "SELECT name FROM af_meta_organizations WHERE tenant_id=? AND id=? AND kind=?",
				p.TenantID, id, kind).Scan(&name); err != nil {
				return err
			}
			if name != previous {
				return problem(409, "organization_changed", "目录名称已经改变，请刷新后重试")
			}
			if err := h.authorize(ctx, p, "organization.edit", "", ""); err != nil {
				return err
			}
			query := `SELECT EXISTS(SELECT 1 FROM af_meta_definitions
 WHERE tenant_id=? AND (group_id=? OR JSON_UNQUOTE(JSON_EXTRACT(draft_json,'$.groupId'))=?))`
			args := []any{p.TenantID, id, id}
			if kind == "category" {
				query = `SELECT EXISTS(SELECT 1 FROM af_meta_definitions
 WHERE tenant_id=? AND (category_id=? OR JSON_UNQUOTE(JSON_EXTRACT(draft_json,'$.categoryId'))=?))
 OR EXISTS(SELECT 1 FROM af_meta_organizations WHERE tenant_id=? AND parent_id=?)`
				args = append(args, p.TenantID, id)
			}
			var occupied bool
			if err := tx.QueryRowContext(ctx, query, args...).Scan(&occupied); err != nil {
				return err
			}
			if occupied {
				return problem(409, "organization_not_empty", "目录仍有分组或保留的定义，无法删除")
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM af_meta_organizations WHERE tenant_id=? AND id=? AND kind=?", p.TenantID, id, kind)
			return err
		})
		if err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}
