package identity

import (
	"database/sql"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (s *Store) mentionAccounts(w http.ResponseWriter, r *http.Request) error {
	prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))
	if !utf8.ValidString(prefix) || utf8.RuneCountInString(prefix) < 1 || utf8.RuneCountInString(prefix) > 100 {
		return problem(422, "invalid_prefix", "请输入 1 至 100 字的名称前缀")
	}
	type account struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	data := []account{}
	more := false
	err := s.adminRead(r, func(tx *sql.Tx, actor contracts.User) error {
		escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(prefix)
		rows, err := tx.QueryContext(r.Context(), "SELECT id,display_name FROM af_identity_users WHERE tenant_id=? AND enabled=1 AND display_name LIKE ? ESCAPE '!' ORDER BY display_name,id LIMIT 21", actor.TenantID, escaped+"%")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if len(data) == 20 {
				more = true
				break
			}
			var item account
			if err := rows.Scan(&item.ID, &item.Label); err != nil {
				return err
			}
			data = append(data, item)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "hasMore": more})
	return nil
}
