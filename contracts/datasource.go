package contracts

// DataSource credentials belong to Data. Passwords are write-only and encrypted
// at rest; definitions refer only to stable source IDs and physical mappings.
type DataSource struct {
	ID              string `json:"id"`
	TenantID        string `json:"tenantId"`
	Name            string `json:"name"`
	Driver          string `json:"driver"`
	Host            string `json:"host,omitempty"`
	Port            int    `json:"port,omitempty"`
	Database        string `json:"database"`
	Username        string `json:"username,omitempty"`
	TLSMode         string `json:"tlsMode,omitempty"`
	TenantIsolation string `json:"tenantIsolation"`
	Enabled         bool   `json:"enabled"`
	PasswordSet     bool   `json:"passwordSet"`
	ETag            string `json:"etag"`
}

type DataSourceInput struct {
	Name            string  `json:"name"`
	Driver          string  `json:"driver"`
	Host            string  `json:"host"`
	Port            int     `json:"port"`
	Database        string  `json:"database"`
	Username        string  `json:"username"`
	Password        *string `json:"password,omitempty"`
	TLSMode         string  `json:"tlsMode"`
	TenantIsolation string  `json:"tenantIsolation"`
	Enabled         bool    `json:"enabled"`
}

type TableInfo struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Type      string `json:"type"`
}

type SourceColumn struct {
	Name         string `json:"name"`
	DatabaseType string `json:"databaseType"`
	DataType     string `json:"dataType"`
	Nullable     bool   `json:"nullable"`
	PrimaryKey   bool   `json:"primaryKey"`
	Generated    bool   `json:"generated"`
	Precision    int    `json:"precision,omitempty"`
	Scale        int    `json:"scale,omitempty"`
	MaxLength    int64  `json:"maxLength,omitempty"`
	Position     int    `json:"position"`
}

type SourceIndex struct {
	Name      string   `json:"name"`
	Unique    bool     `json:"unique"`
	Primary   bool     `json:"primary"`
	Columns   []string `json:"columns"`
	Predicate string   `json:"predicate,omitempty"`
}

type SourceTable struct {
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Columns   []SourceColumn `json:"columns"`
	Indexes   []SourceIndex  `json:"indexes"`
	Signature string         `json:"signature"`
}

type DataSourceTestResult struct {
	Success       bool   `json:"success"`
	ServerVersion string `json:"serverVersion"`
}
