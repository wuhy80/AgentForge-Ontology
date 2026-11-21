package metadata

import (
	"database/sql"
	"net/http"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) renameOrganization(kind string) endpoint {
	return func(w http.ResponseWriter, r *http.Request, p contracts.Principal) error {
		ctx := r.Context()
		if err := h.authorize(ctx, p, "organization.edit", "", ""); err != nil {
			return err
		}
		var request struct {
			Name         string `json:"name"`
			PreviousName string `json:"previousName"`
		}
		if err := decode(r, &request); err != nil {
			return err
		}
		name, key, err := normalizeName(request.Name)
		if err != nil {
			return err
		}
		previous, _, err := normalizeName(request.PreviousName)
		if err != nil {
			return err
		}
		org := contracts.Organization{ID: r.PathValue("id"), TenantID: p.TenantID}
		err = h.withTenantTx(ctx, p.TenantID, "", func(tx *sql.Tx) error {
			if err := tx.QueryRowContext(ctx, "SELECT parent_id,name FROM af_meta_organizations WHERE tenant_id=? AND id=? AND kind=?",
				p.TenantID, org.ID, kind).Scan(&org.CategoryID, &org.Name); err != nil {
				return err
			}
			if org.Name == name {
				return nil
			}
			if org.Name != previous {
				return problem(409, "organization_changed", "目录名称已经改变，请刷新后重试")
			}
			if err := h.authorize(ctx, p, "organization.edit", "", ""); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "UPDATE af_meta_organizations SET name=?,name_key=? WHERE tenant_id=? AND id=? AND kind=?",
				name, []byte(key), p.TenantID, org.ID, kind)
			if err == nil {
				org.Name = name
			}
			return err
		})
		if err != nil {
			return err
		}
		writeJSON(w, 200, org)
		return nil
	}
}
