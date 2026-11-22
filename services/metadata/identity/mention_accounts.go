package identity

import (
	"database/sql"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (s *Store) mentionAccounts(w http.ResponseWriter, r *http.Request) error {
	return s.searchMentionAccounts(w, r, true)
}

func (s *Store) sharedMentionAccounts(w http.ResponseWriter, r *http.Request) error {
	return s.searchMentionAccounts(w, r, false)
}

func (s *Store) searchMentionAccounts(w http.ResponseWriter, r *http.Request, adminOnly bool) error {
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
	err := s.sessionRead(r, func(tx *sql.Tx, actor contracts.User) error {
		if !actor.Administrator && (adminOnly || !s.SharedGroupMentionAccounts) {
			return problem(403, "mention_directory_forbidden", "当前账号未获账号提及目录权限")
		}
		escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(prefix)
		query := "SELECT u.id,u.display_name FROM af_identity_users u WHERE u.tenant_id=? AND u.enabled=1 AND u.display_name LIKE ? ESCAPE '!'"
		args := []any{actor.TenantID, escaped + "%"}
		if !actor.Administrator {
			query += " AND EXISTS (SELECT 1 FROM af_identity_user_group_members mine JOIN af_identity_user_groups g ON g.tenant_id=mine.tenant_id AND g.id=mine.group_id AND g.enabled=1 JOIN af_identity_user_group_members peer ON peer.tenant_id=g.tenant_id AND peer.group_id=g.id WHERE mine.tenant_id=u.tenant_id AND mine.user_id=? AND peer.user_id=u.id)"
			args = append(args, actor.ID)
		}
		rows, err := tx.QueryContext(r.Context(), query+" ORDER BY u.display_name,u.id LIMIT 21", args...)
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
