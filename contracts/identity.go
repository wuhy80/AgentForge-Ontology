package contracts

// Identity records are tenant scoped; password and session digests never cross
// the public API. ETags are optimistic concurrency tokens, not business versions.
type User struct {
	ID            string   `json:"id"`
	TenantID      string   `json:"tenantId"`
	Username      string   `json:"username"`
	DisplayName   string   `json:"displayName"`
	Enabled       bool     `json:"enabled"`
	Administrator bool     `json:"administrator"`
	RoleIDs       []string `json:"roleIds"`
	ETag          string   `json:"etag"`
}

type Role struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ETag        string `json:"etag"`
}

type Session struct {
	User      User   `json:"user"`
	ExpiresAt int64  `json:"expiresAt"`
	CSRFToken string `json:"csrfToken"`
}

type LoginRequest struct {
	Tenant   string `json:"tenant"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type UserInput struct {
	Username      string   `json:"username"`
	DisplayName   string   `json:"displayName"`
	Password      string   `json:"password,omitempty"`
	Enabled       bool     `json:"enabled"`
	Administrator bool     `json:"administrator"`
	RoleIDs       []string `json:"roleIds"`
}

type RoleInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type PermissionRule struct {
	ID           string      `json:"id"`
	RoleID       string      `json:"roleId"`
	Effect       string      `json:"effect"`
	Operation    string      `json:"operation"`
	ResourceID   string      `json:"resourceId"`
	ReadFields   []string    `json:"readFields"`
	WriteFields  []string    `json:"writeFields"`
	RowFilter    *Expression `json:"rowFilter,omitempty"`
	NewRowFilter *Expression `json:"newRowFilter,omitempty"`
}

type PermissionPolicy struct {
	Rules []PermissionRule `json:"rules"`
}

type PolicyRecord struct {
	Current PermissionPolicy  `json:"current"`
	Draft   *PermissionPolicy `json:"draft,omitempty"`
	ETag    string            `json:"etag"`
	State   string            `json:"state"`
}

type AuditRecord struct {
	ID        string `json:"id"`
	ActorID   string `json:"actorId"`
	Operation string `json:"operation"`
	TargetID  string `json:"targetId"`
	CreatedAt string `json:"createdAt"`
}

type SimulationRequest struct {
	UserID  string               `json:"userId"`
	Request AuthorizationRequest `json:"request"`
	Draft   bool                 `json:"draft"`
}

type SimulationResult struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
}

// RuntimePolicy is supplied by the authoritative application to OPA; browsers
// never provide it. Rules have already been scoped to the user's active roles.
type RuntimePolicy struct {
	State string           `json:"state"`
	Rules []PermissionRule `json:"rules"`
}

type SessionCheck struct {
	Token     string `json:"token"`
	CSRFToken string `json:"csrfToken"`
	Method    string `json:"method"`
	Origin    string `json:"origin"`
}

type AuthorizationCheck struct {
	Principal Principal            `json:"principal"`
	Request   AuthorizationRequest `json:"request"`
}
