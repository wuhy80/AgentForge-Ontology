package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

const maxGroupMembers = 256
const maxUserGroups = 64
const maxDirectoryMemberships = 20000
const userGroupColumns = "id,name,description,enabled,role_ids,etag"

func scanUserGroup(row scanner) (contracts.UserGroup, error) {
	var result contracts.UserGroup
	var roles string
	if err := row.Scan(&result.ID, &result.Name, &result.Description, &result.Enabled, &roles, &result.ETag); err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(roles), &result.RoleIDs); err != nil {
		return result, err
	}
	if len(result.RoleIDs) > 64 {
		return result, problem(422, "role_budget", "用户组最多分配64个角色")
	}
	if result.RoleIDs == nil {
		result.RoleIDs = []string{}
	}
	result.UserIDs = []string{}
	return result, nil
}

func userGroup(ctx context.Context, tx *sql.Tx, tenant, id string) (contracts.UserGroup, error) {
	result, err := scanUserGroup(tx.QueryRowContext(ctx, "SELECT "+userGroupColumns+" FROM af_identity_user_groups WHERE tenant_id=? AND id=?", tenant, id))
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT user_id FROM af_identity_user_group_members WHERE tenant_id=? AND group_id=? ORDER BY user_id LIMIT 257", tenant, id)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return result, err
		}
		result.UserIDs = append(result.UserIDs, id)
		if len(result.UserIDs) > maxGroupMembers {
			return result, problem(422, "member_budget", "用户组最多包含256名成员")
		}
	}
	return result, rows.Err()
}

func (s *Store) listUserGroups(w http.ResponseWriter, r *http.Request) error {
	result := []contracts.UserGroup{}
	err := s.adminRead(r, func(tx *sql.Tx, actor contracts.User) error {
		rows, err := tx.QueryContext(r.Context(), "SELECT "+userGroupColumns+" FROM af_identity_user_groups WHERE tenant_id=? ORDER BY id LIMIT 2001", actor.TenantID)
		if err != nil {
			return err
		}
		positions := map[string]int{}
		for rows.Next() {
			group, err := scanUserGroup(rows)
			if err != nil {
				rows.Close()
				return err
			}
			positions[group.ID] = len(result)
			result = append(result, group)
			if len(result) > maxList {
				rows.Close()
				return problem(422, "directory_budget", "用户组目录超过2000条读取预算")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		members, err := tx.QueryContext(r.Context(), "SELECT group_id,user_id FROM af_identity_user_group_members WHERE tenant_id=? ORDER BY group_id,user_id LIMIT 20001", actor.TenantID)
		if err != nil {
			return err
		}
		defer members.Close()
		count := 0
		for members.Next() {
			var groupID, userID string
			if err := members.Scan(&groupID, &userID); err != nil {
				return err
			}
			count++
			if count > maxDirectoryMemberships {
				return problem(422, "directory_budget", "用户组目录成员引用超过20000条读取预算")
			}
			position, ok := positions[groupID]
			if !ok {
				return problem(422, "invalid_membership", "用户组成员归属不可用")
			}
			result[position].UserIDs = append(result[position].UserIDs, userID)
			if len(result[position].UserIDs) > maxGroupMembers {
				return problem(422, "member_budget", "用户组最多包含256名成员")
			}
		}
		return members.Err()
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"data": result})
	return nil
}

func (s *Store) getUserGroup(w http.ResponseWriter, r *http.Request) error {
	var result contracts.UserGroup
	err := s.adminRead(r, func(tx *sql.Tx, actor contracts.User) error {
		var err error
		result, err = userGroup(r.Context(), tx, actor.TenantID, r.PathValue("id"))
		return err
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+result.ETag+`"`)
	writeJSON(w, 200, result)
	return nil
}

func (s *Store) createUserGroup(w http.ResponseWriter, r *http.Request) error {
	return s.saveUserGroup(w, r, true)
}
func (s *Store) updateUserGroup(w http.ResponseWriter, r *http.Request) error {
	return s.saveUserGroup(w, r, false)
}

func (s *Store) saveUserGroup(w http.ResponseWriter, r *http.Request, create bool) error {
	var input contracts.UserGroupInput
	if err := decode(r, &input); err != nil {
		return err
	}
	name, key, err := normalizedName(input.Name)
	if err != nil {
		return err
	}
	if len(input.Description) > 4096 {
		return problem(422, "invalid_description", "用户组说明最多4096字节")
	}
	members, err := uniqueIDs(input.UserIDs, maxGroupMembers, "member_budget", "用户组最多包含256名成员")
	if err != nil {
		return err
	}
	var result contracts.UserGroup
	err = s.mutation(r, true, func(tx *sql.Tx, actor contracts.User) error {
		old := contracts.UserGroup{ID: contracts.NewID(), UserIDs: []string{}, RoleIDs: []string{}}
		if !create {
			var err error
			old, err = userGroup(r.Context(), tx, actor.TenantID, r.PathValue("id"))
			if err != nil {
				return err
			}
			if err := match(r, old.ETag); err != nil {
				return err
			}
		}
		roles, err := roleIDs(r.Context(), tx, actor.TenantID, input.RoleIDs)
		if err != nil {
			return err
		}
		users, err := groupUsers(r.Context(), tx, actor.TenantID, members)
		if err != nil {
			return err
		}
		etag, err := randomToken()
		if err != nil {
			return err
		}
		result = contracts.UserGroup{ID: old.ID, Name: name, Description: strings.TrimSpace(input.Description), Enabled: input.Enabled, RoleIDs: roles, UserIDs: members, ETag: etag}
		if create {
			_, err = tx.ExecContext(r.Context(), "INSERT INTO af_identity_user_groups (id,tenant_id,name,name_key,description,enabled,role_ids,etag) VALUES (?,?,?,?,?,?,?,?)", result.ID, actor.TenantID, name, key, result.Description, result.Enabled, marshal(roles), etag)
		} else {
			_, err = tx.ExecContext(r.Context(), "UPDATE af_identity_user_groups SET name=?,name_key=?,description=?,enabled=?,role_ids=?,etag=? WHERE tenant_id=? AND id=?", name, key, result.Description, result.Enabled, marshal(roles), etag, actor.TenantID, result.ID)
		}
		if err != nil {
			return err
		}
		accessChanged := create || old.Enabled != result.Enabled || !reflect.DeepEqual(old.RoleIDs, result.RoleIDs) || !reflect.DeepEqual(old.UserIDs, result.UserIDs)
		if create || !reflect.DeepEqual(old.UserIDs, result.UserIDs) {
			if _, err := tx.ExecContext(r.Context(), "DELETE FROM af_identity_user_group_members WHERE tenant_id=? AND group_id=?", actor.TenantID, result.ID); err != nil {
				return err
			}
			for _, id := range members {
				if _, err := tx.ExecContext(r.Context(), "INSERT INTO af_identity_user_group_members (tenant_id,group_id,user_id) VALUES (?,?,?)", actor.TenantID, result.ID, id); err != nil {
					return err
				}
			}
		}
		var groupCount, membershipCount int
		if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM af_identity_user_groups WHERE tenant_id=?", actor.TenantID).Scan(&groupCount); err != nil {
			return err
		}
		if groupCount > maxList {
			return problem(422, "directory_budget", "用户组目录最多2000条")
		}
		if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM af_identity_user_group_members WHERE tenant_id=?", actor.TenantID).Scan(&membershipCount); err != nil {
			return err
		}
		if membershipCount > maxDirectoryMemberships {
			return problem(422, "directory_budget", "用户组目录成员引用最多20000条")
		}
		// Validate the proposed whole membership under the tenant write lock.
		// A rejection rolls back the group, state, audit and session deletions.
		for _, user := range users {
			if _, err := effectiveRoleIDs(r.Context(), tx, user); err != nil {
				return err
			}
		}
		if accessChanged {
			affected := map[string]bool{}
			for _, id := range old.UserIDs {
				affected[id] = true
			}
			for _, id := range members {
				affected[id] = true
			}
			ids := make([]string, 0, len(affected))
			for id := range affected {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			if len(ids) > 0 {
				args := []any{actor.TenantID}
				for _, id := range ids {
					args = append(args, id)
				}
				if _, err := tx.ExecContext(r.Context(), "DELETE FROM af_identity_sessions WHERE tenant_id=? AND user_id IN ("+placeholders(len(ids))+")", args...); err != nil {
					return err
				}
			}
			if err := changeState(r.Context(), tx, actor.TenantID); err != nil {
				return err
			}
		}
		operation := "user_group.update"
		if create {
			operation = "user_group.create"
		}
		return audit(r.Context(), tx, actor.TenantID, actor.ID, operation, result.ID)
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

func uniqueIDs(ids []string, max int, code, message string) ([]string, error) {
	if len(ids) > max {
		return nil, problem(422, code, message)
	}
	result := append([]string{}, ids...)
	sort.Strings(result)
	for i, id := range result {
		if id == "" || len(id) > 200 || (i > 0 && id == result[i-1]) {
			return nil, problem(422, "invalid_membership", "成员或角色不能缺失、重复或超过标识长度")
		}
	}
	return result, nil
}

func placeholders(count int) string { return strings.TrimRight(strings.Repeat("?,", count), ",") }

func groupUsers(ctx context.Context, tx *sql.Tx, tenant string, ids []string) ([]contracts.User, error) {
	result := []contracts.User{}
	if len(ids) == 0 {
		return result, nil
	}
	args := []any{tenant}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id IN ("+placeholders(len(ids))+") ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(ids) {
		return nil, problem(422, "invalid_member", "成员必须是当前租户已存在的用户")
	}
	return result, nil
}

// effectiveRoleIDs is shared by real authorization and policy simulation. Its
// caller holds the tenant's read or write lock for a consistent membership view.
// User.RoleIDs always remains the directly assigned roles in public responses.
func effectiveRoleIDs(ctx context.Context, tx *sql.Tx, user contracts.User) ([]string, error) {
	if len(user.RoleIDs) > 64 {
		return nil, problem(422, "role_budget", "用户直接角色超过64个")
	}
	unique := map[string]bool{}
	for _, id := range user.RoleIDs {
		unique[id] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT g.id,g.enabled,g.role_ids FROM af_identity_user_group_members m LEFT JOIN af_identity_user_groups g ON g.tenant_id=m.tenant_id AND g.id=m.group_id WHERE m.tenant_id=? AND m.user_id=? ORDER BY m.group_id LIMIT 65", user.TenantID, user.ID)
	if err != nil {
		return nil, err
	}
	count := 0
	for rows.Next() {
		var id sql.NullString
		var enabled sql.NullBool
		var raw sql.NullString
		if err := rows.Scan(&id, &enabled, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		count++
		if count > maxUserGroups {
			rows.Close()
			return nil, problem(422, "group_budget", "每个用户最多加入64个用户组，停用组同样计入")
		}
		if !id.Valid || !enabled.Valid || !raw.Valid {
			rows.Close()
			return nil, problem(422, "invalid_membership", "用户组成员归属不可用")
		}
		if enabled.Bool {
			var roles []string
			if err := json.Unmarshal([]byte(raw.String), &roles); err != nil {
				rows.Close()
				return nil, err
			}
			if len(roles) > 64 {
				rows.Close()
				return nil, problem(422, "role_budget", "用户组最多分配64个角色")
			}
			for _, id := range roles {
				unique[id] = true
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(unique) > 64 {
		return nil, problem(422, "effective_role_budget", "直接角色与启用用户组角色合计最多64个")
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	return roleIDs(ctx, tx, user.TenantID, ids)
}
