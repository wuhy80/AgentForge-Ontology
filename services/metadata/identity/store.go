// Package identity owns local tenant accounts, sessions and current permissions.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const sessionLifetime = 8 * time.Hour
const maxList = 2000

type Store struct {
	DB            *sql.DB
	OPA           *platform.OPAAuthorizer
	Origin        string
	loginMu       sync.Mutex
	loginAttempts map[string]loginAttempt
}

func NewStore(db *sql.DB, opa *platform.OPAAuthorizer, origin string) *Store {
	return &Store{DB: db, OPA: opa, Origin: strings.TrimRight(origin, "/")}
}

func problem(status int, code, message string) error {
	return &platform.AccessError{Status: status, Code: code, Message: message}
}

func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func tokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func marshal(value any) string { raw, _ := json.Marshal(value); return string(raw) }

func normalizedName(value string) (string, []byte, error) {
	value = norm.NFC.String(strings.TrimSpace(value))
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return "", nil, problem(422, "invalid_name", "名称必须为1到128个字符")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", nil, problem(422, "invalid_name", "名称不能包含控制字符")
		}
	}
	return value, []byte(cases.Fold().String(value)), nil
}

func validPassword(value string) error {
	if len(value) < 12 || len(value) > 72 || !utf8.ValidString(value) {
		return problem(422, "invalid_password", "密码必须为12到72字节")
	}
	return nil
}

func passwordHash(value string) (string, error) {
	if err := validPassword(value); err != nil {
		return "", err
	}
	raw, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.DefaultCost)
	return string(raw), err
}

type scanner interface{ Scan(...any) error }

const userColumns = "id,tenant_id,username,display_name,enabled,administrator,role_ids,etag"

func scanUser(row scanner) (contracts.User, error) {
	var user contracts.User
	var raw string
	err := row.Scan(&user.ID, &user.TenantID, &user.Username, &user.DisplayName, &user.Enabled, &user.Administrator, &raw, &user.ETag)
	if err != nil {
		return user, err
	}
	if err := json.Unmarshal([]byte(raw), &user.RoleIDs); err != nil {
		return user, err
	}
	if user.RoleIDs == nil {
		user.RoleIDs = []string{}
	}
	return user, nil
}

func scanRole(row scanner) (contracts.Role, error) {
	var role contracts.Role
	err := row.Scan(&role.ID, &role.Name, &role.Description, &role.ETag)
	return role, err
}

func scanPolicy(row scanner) (contracts.PolicyRecord, error) {
	var result contracts.PolicyRecord
	var current string
	var draft sql.NullString
	if err := row.Scan(&current, &draft, &result.ETag, &result.State); err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(current), &result.Current); err != nil {
		return result, err
	}
	if draft.Valid {
		result.Draft = new(contracts.PermissionPolicy)
		if err := json.Unmarshal([]byte(draft.String), result.Draft); err != nil {
			return result, err
		}
	}
	return result, nil
}

const policyColumns = "current_policy,draft_policy,policy_etag,authorization_state"

func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func lockTenant(ctx context.Context, tx *sql.Tx, tenant string) error {
	var state string
	return tx.QueryRowContext(ctx, "SELECT authorization_state FROM af_identity_tenants WHERE tenant_id=? FOR UPDATE", tenant).Scan(&state)
}

func changeState(ctx context.Context, tx *sql.Tx, tenant string) error {
	state, err := randomToken()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE af_identity_tenants SET authorization_state=? WHERE tenant_id=?", state, tenant)
	return err
}

func audit(ctx context.Context, tx *sql.Tx, tenant, actor, operation, target string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO af_identity_audit (id,tenant_id,actor_id,operation,target_id) VALUES (?,?,?,?,?)", contracts.NewID(), tenant, actor, operation, target)
	return err
}

func roleIDs(ctx context.Context, tx *sql.Tx, tenant string, ids []string) ([]string, error) {
	if len(ids) > 64 {
		return nil, problem(422, "role_budget", "每个用户最多分配64个角色")
	}
	result := append([]string{}, ids...)
	sort.Strings(result)
	for i, id := range result {
		if id == "" || (i > 0 && result[i-1] == id) {
			return nil, problem(422, "invalid_role", "角色不能缺失或重复")
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM af_identity_roles WHERE tenant_id=? AND id=?", tenant, id).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 1 {
			return nil, problem(422, "invalid_role", "角色必须属于当前租户")
		}
	}
	return result, nil
}

// Bootstrap only establishes an empty tenant. Repeating it never resets an
// existing account, password, role or policy, even with different arguments.
func (s *Store) Bootstrap(ctx context.Context, tenant, username, password string) error {
	if tenant == "" || len(tenant) > 100 || strings.TrimSpace(tenant) != tenant {
		return problem(422, "invalid_tenant", "租户标识不合法")
	}
	name, key, err := normalizedName(username)
	if err != nil {
		return err
	}
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	state, err := randomToken()
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO af_identity_tenants (tenant_id,authorization_state,current_policy,policy_etag) VALUES (?,?,?,?)", tenant, state, `{"rules":[]}`, state); err != nil {
			return err
		}
		if err := lockTenant(ctx, tx, tenant); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM af_identity_users WHERE tenant_id=?", tenant).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return nil
		}
		roleID, userID := contracts.NewID(), contracts.NewID()
		if _, err := tx.ExecContext(ctx, "INSERT INTO af_identity_roles (id,tenant_id,name,name_key,description,etag) VALUES (?,?,?,?,?,?)", roleID, tenant, "租户管理员", []byte("租户管理员"), "初始业务操作授权；管理员标记仅用于管理用户与策略", state); err != nil {
			return err
		}
		policy := contracts.PermissionPolicy{Rules: []contracts.PermissionRule{}}
		for _, operation := range []string{"organization.read", "organization.edit", "definition.read", "definition.edit", "definition.publish", "instance.read", "instance.create", "instance.patch", "instance.delete", "instance.query"} {
			policy.Rules = append(policy.Rules, contracts.PermissionRule{ID: contracts.NewID(), RoleID: roleID, Effect: "allow", Operation: operation, ResourceID: "*", ReadFields: []string{"*"}, WriteFields: []string{"*"}})
		}
		if _, err := tx.ExecContext(ctx, "UPDATE af_identity_tenants SET current_policy=? WHERE tenant_id=?", marshal(policy), tenant); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO af_identity_users (id,tenant_id,username,username_key,display_name,password_hash,enabled,administrator,role_ids,etag) VALUES (?,?,?,?,?,?,true,true,?,?)", userID, tenant, name, key, name, hash, marshal([]string{roleID}), state); err != nil {
			return err
		}
		return audit(ctx, tx, tenant, userID, "identity.bootstrap", userID)
	})
}

func (s *Store) Authorize(ctx context.Context, p contracts.Principal, req contracts.AuthorizationRequest) (contracts.Decision, error) {
	var runtime contracts.RuntimePolicy
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		policy, err := scanPolicy(tx.QueryRowContext(ctx, "SELECT "+policyColumns+" FROM af_identity_tenants WHERE tenant_id=? FOR SHARE", p.TenantID))
		if errors.Is(err, sql.ErrNoRows) {
			return problem(401, "unauthenticated", "用户不可用")
		}
		if err != nil {
			return err
		}
		user, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM af_identity_users WHERE tenant_id=? AND id=?", p.TenantID, p.Subject))
		if errors.Is(err, sql.ErrNoRows) {
			return problem(401, "unauthenticated", "用户不可用")
		}
		if err != nil {
			return err
		}
		if !user.Enabled {
			return problem(401, "unauthenticated", "用户不可用")
		}
		ids, err := roleIDs(ctx, tx, p.TenantID, user.RoleIDs)
		if err != nil {
			return err
		}
		active := map[string]bool{}
		for _, id := range ids {
			active[id] = true
		}
		runtime.State = policy.State
		runtime.Rules = []contracts.PermissionRule{}
		for _, rule := range policy.Current.Rules {
			if active[rule.RoleID] {
				runtime.Rules = append(runtime.Rules, rule)
			}
		}
		return nil
	})
	if err != nil {
		return contracts.Decision{}, err
	}
	return platform.EvaluateRuntimePolicy(ctx, s.OPA, &runtime, p, req)
}

// userInput validates public content without trusting user-supplied identities.
func userInput(input *contracts.UserInput, create bool) ([]byte, error) {
	name, key, err := normalizedName(input.Username)
	if err != nil {
		return nil, err
	}
	input.Username = name
	display, _, err := normalizedName(input.DisplayName)
	if err != nil {
		return nil, err
	}
	input.DisplayName = display
	if create || input.Password != "" {
		if err := validPassword(input.Password); err != nil {
			return nil, err
		}
	}
	return key, nil
}
