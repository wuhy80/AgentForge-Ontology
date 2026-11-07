package identity

import (
	"database/sql"
	"net/http"
	"reflect"
	"strings"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (s *Store) listUsers(w http.ResponseWriter, r *http.Request) error {
	result := []contracts.User{}
	err := s.adminRead(r, func(tx *sql.Tx, actor contracts.User) error {
		rows, err := tx.QueryContext(r.Context(), "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? ORDER BY id LIMIT 2001", actor.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			user, err := scanUser(rows)
			if err != nil {
				return err
			}
			result = append(result, user)
			if len(result) > maxList {
				return problem(422, "directory_budget", "用户目录超过2000条读取预算")
			}
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"data": result})
	return nil
}

func (s *Store) getUser(w http.ResponseWriter, r *http.Request) error {
	var result contracts.User
	err := s.adminRead(r, func(tx *sql.Tx, actor contracts.User) error {
		var err error
		result, err = scanUser(tx.QueryRowContext(r.Context(), "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id=?", actor.TenantID, r.PathValue("id")))
		return err
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+result.ETag+`"`)
	writeJSON(w, 200, result)
	return nil
}

func (s *Store) createUser(w http.ResponseWriter, r *http.Request) error {
	var input contracts.UserInput
	if err := decode(r, &input); err != nil {
		return err
	}
	key, err := userInput(&input, true)
	if err != nil {
		return err
	}
	hash, err := passwordHash(input.Password)
	if err != nil {
		return err
	}
	var result contracts.User
	err = s.mutation(r, true, func(tx *sql.Tx, actor contracts.User) error {
		ids, err := roleIDs(r.Context(), tx, actor.TenantID, input.RoleIDs)
		if err != nil {
			return err
		}
		etag, err := randomToken()
		if err != nil {
			return err
		}
		result = contracts.User{ID: contracts.NewID(), TenantID: actor.TenantID, Username: input.Username, DisplayName: input.DisplayName, Enabled: input.Enabled, Administrator: input.Administrator, RoleIDs: ids, ETag: etag}
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO af_identity_users (id,tenant_id,username,username_key,display_name,password_hash,enabled,administrator,role_ids,etag) VALUES (?,?,?,?,?,?,?,?,?,?)", result.ID, result.TenantID, result.Username, key, result.DisplayName, hash, result.Enabled, result.Administrator, marshal(ids), etag); err != nil {
			return err
		}
		if err := changeState(r.Context(), tx, actor.TenantID); err != nil {
			return err
		}
		return audit(r.Context(), tx, actor.TenantID, actor.ID, "user.create", result.ID)
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+result.ETag+`"`)
	writeJSON(w, 201, result)
	return nil
}

func (s *Store) updateUser(w http.ResponseWriter, r *http.Request) error {
	var input contracts.UserInput
	if err := decode(r, &input); err != nil {
		return err
	}
	key, err := userInput(&input, false)
	if err != nil {
		return err
	}
	var hash string
	if input.Password != "" {
		hash, err = passwordHash(input.Password)
		if err != nil {
			return err
		}
	}
	var result contracts.User
	err = s.mutation(r, true, func(tx *sql.Tx, actor contracts.User) error {
		old, err := scanUser(tx.QueryRowContext(r.Context(), "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id=? FOR UPDATE", actor.TenantID, r.PathValue("id")))
		if err != nil {
			return err
		}
		if err := match(r, old.ETag); err != nil {
			return err
		}
		ids, err := roleIDs(r.Context(), tx, actor.TenantID, input.RoleIDs)
		if err != nil {
			return err
		}
		if old.Enabled && old.Administrator && (!input.Enabled || !input.Administrator) {
			if old.ID == actor.ID {
				return problem(422, "self_administrator", "不能停用自己或撤销自己的管理员身份")
			}
			var count int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM af_identity_users WHERE tenant_id=? AND enabled=true AND administrator=true", actor.TenantID).Scan(&count); err != nil {
				return err
			}
			if count <= 1 {
				return problem(422, "last_administrator", "租户必须保留至少一名有效管理员")
			}
		}
		etag, err := randomToken()
		if err != nil {
			return err
		}
		result = contracts.User{ID: old.ID, TenantID: actor.TenantID, Username: input.Username, DisplayName: input.DisplayName, Enabled: input.Enabled, Administrator: input.Administrator, RoleIDs: ids, ETag: etag}
		if _, err := tx.ExecContext(r.Context(), "UPDATE af_identity_users SET username=?,username_key=?,display_name=?,enabled=?,administrator=?,role_ids=?,etag=? WHERE tenant_id=? AND id=?", result.Username, key, result.DisplayName, result.Enabled, result.Administrator, marshal(ids), etag, actor.TenantID, result.ID); err != nil {
			return err
		}
		if hash != "" {
			if _, err := tx.ExecContext(r.Context(), "UPDATE af_identity_users SET password_hash=? WHERE tenant_id=? AND id=?", hash, actor.TenantID, result.ID); err != nil {
				return err
			}
		}
		if hash != "" || old.Enabled != result.Enabled || old.Administrator != result.Administrator || !reflect.DeepEqual(old.RoleIDs, result.RoleIDs) || old.Username != result.Username {
			if _, err := tx.ExecContext(r.Context(), "DELETE FROM af_identity_sessions WHERE tenant_id=? AND user_id=?", actor.TenantID, result.ID); err != nil {
				return err
			}
		}
		if err := changeState(r.Context(), tx, actor.TenantID); err != nil {
			return err
		}
		return audit(r.Context(), tx, actor.TenantID, actor.ID, "user.update", result.ID)
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+result.ETag+`"`)
	writeJSON(w, 200, result)
	return nil
}

func (s *Store) listRoles(w http.ResponseWriter, r *http.Request) error {
	result := []contracts.Role{}
	err := s.adminRead(r, func(tx *sql.Tx, actor contracts.User) error {
		rows, err := tx.QueryContext(r.Context(), "SELECT id,name,description,etag FROM af_identity_roles WHERE tenant_id=? ORDER BY id LIMIT 2001", actor.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			role, err := scanRole(rows)
			if err != nil {
				return err
			}
			result = append(result, role)
			if len(result) > maxList {
				return problem(422, "directory_budget", "角色目录超过2000条读取预算")
			}
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"data": result})
	return nil
}

func (s *Store) getRole(w http.ResponseWriter, r *http.Request) error {
	var result contracts.Role
	err := s.adminRead(r, func(tx *sql.Tx, actor contracts.User) error {
		var err error
		result, err = scanRole(tx.QueryRowContext(r.Context(), "SELECT id,name,description,etag FROM af_identity_roles WHERE tenant_id=? AND id=?", actor.TenantID, r.PathValue("id")))
		return err
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+result.ETag+`"`)
	writeJSON(w, 200, result)
	return nil
}

func (s *Store) createRole(w http.ResponseWriter, r *http.Request) error {
	return s.saveRole(w, r, true)
}
func (s *Store) updateRole(w http.ResponseWriter, r *http.Request) error {
	return s.saveRole(w, r, false)
}
func (s *Store) saveRole(w http.ResponseWriter, r *http.Request, create bool) error {
	var input contracts.RoleInput
	if err := decode(r, &input); err != nil {
		return err
	}
	name, key, err := normalizedName(input.Name)
	if err != nil {
		return err
	}
	if len(input.Description) > 4096 {
		return problem(422, "invalid_description", "角色说明最多4096字节")
	}
	var result contracts.Role
	err = s.mutation(r, true, func(tx *sql.Tx, actor contracts.User) error {
		id := contracts.NewID()
		if !create {
			old, err := scanRole(tx.QueryRowContext(r.Context(), "SELECT id,name,description,etag FROM af_identity_roles WHERE tenant_id=? AND id=?", actor.TenantID, r.PathValue("id")))
			if err != nil {
				return err
			}
			if err := match(r, old.ETag); err != nil {
				return err
			}
			id = old.ID
		}
		etag, err := randomToken()
		if err != nil {
			return err
		}
		result = contracts.Role{ID: id, Name: name, Description: strings.TrimSpace(input.Description), ETag: etag}
		if create {
			_, err = tx.ExecContext(r.Context(), "INSERT INTO af_identity_roles (id,tenant_id,name,name_key,description,etag) VALUES (?,?,?,?,?,?)", id, actor.TenantID, name, key, result.Description, etag)
		} else {
			_, err = tx.ExecContext(r.Context(), "UPDATE af_identity_roles SET name=?,name_key=?,description=?,etag=? WHERE tenant_id=? AND id=?", name, key, result.Description, etag, actor.TenantID, id)
		}
		if err != nil {
			return err
		}
		if err := changeState(r.Context(), tx, actor.TenantID); err != nil {
			return err
		}
		operation := "role.update"
		if create {
			operation = "role.create"
		}
		return audit(r.Context(), tx, actor.TenantID, actor.ID, operation, id)
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+result.ETag+`"`)
	status := 200
	if create {
		status = 201
	}
	writeJSON(w, status, result)
	return nil
}

func (s *Store) listAudit(w http.ResponseWriter, r *http.Request) error {
	result := []contracts.AuditRecord{}
	hasMore := false
	err := s.adminRead(r, func(tx *sql.Tx, actor contracts.User) error {
		rows, err := tx.QueryContext(r.Context(), "SELECT id,actor_id,operation,target_id,DATE_FORMAT(created_at,'%Y-%m-%dT%H:%i:%s.%fZ') FROM af_identity_audit WHERE tenant_id=? ORDER BY created_at DESC,id DESC LIMIT 201", actor.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item contracts.AuditRecord
			if err := rows.Scan(&item.ID, &item.ActorID, &item.Operation, &item.TargetID, &item.CreatedAt); err != nil {
				return err
			}
			if len(result) == 200 {
				hasMore = true
				break
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"data": result, "hasMore": hasMore})
	return nil
}
