package contracts

import "encoding/json"

// DataSource credentials belong to Data. Passwords are write-only and encrypted
// at rest; definitions refer only to stable source IDs and physical mappings.
type DataSource struct {
	ID                  string               `json:"id"`
	TenantID            string               `json:"tenantId"`
	Name                string               `json:"name"`
	Driver              string               `json:"driver"`
	VersionPolicy       string               `json:"versionPolicy,omitempty"`
	ExpectedVersionLine string               `json:"expectedVersionLine,omitempty"`
	Host                string               `json:"host,omitempty"`
	Port                int                  `json:"port,omitempty"`
	Database            string               `json:"database"`
	Username            string               `json:"username,omitempty"`
	TLSMode             string               `json:"tlsMode,omitempty"`
	TenantIsolation     string               `json:"tenantIsolation"`
	Enabled             bool                 `json:"enabled"`
	PasswordSet         bool                 `json:"passwordSet"`
	ETag                string               `json:"etag"`
	Compatibility       *SourceCompatibility `json:"compatibility,omitempty"`
}

type DataSourceInput struct {
	Name                string  `json:"name"`
	Driver              string  `json:"driver"`
	VersionPolicy       string  `json:"versionPolicy,omitempty"`
	ExpectedVersionLine string  `json:"expectedVersionLine,omitempty"`
	Host                string  `json:"host"`
	Port                int     `json:"port"`
	Database            string  `json:"database"`
	Username            string  `json:"username"`
	Password            *string `json:"password,omitempty"`
	TLSMode             string  `json:"tlsMode"`
	TenantIsolation     string  `json:"tenantIsolation"`
	Enabled             bool    `json:"enabled"`
}

type TableInfo struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	ColumnCount *int   `json:"columnCount,omitempty"`
	// PrimaryKeyCount counts key columns, including all columns of a composite key.
	PrimaryKeyCount *int `json:"primaryKeyCount,omitempty"`
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
	// nil means not returned; JSON null means the catalog returned SQL NULL.
	// These display-only fields do not participate in physical mapping signatures.
	DefaultValue json.RawMessage `json:"defaultValue,omitempty"`
	Comment      string          `json:"comment,omitempty"`
}

type SourceOverviewMetric struct {
	Available      bool   `json:"available"`
	Value          string `json:"value,omitempty"`
	Unit           string `json:"unit"`
	Scope          string `json:"scope"`
	Basis          string `json:"basis"`
	SampleWindowMS int64  `json:"sampleWindowMs,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type SourceOverviewMetrics struct {
	Connections  SourceOverviewMetric `json:"connections"`
	StorageBytes SourceOverviewMetric `json:"storageBytes"`
	QPS          SourceOverviewMetric `json:"qps"`
}

type SourceOverview struct {
	SourceID      string                `json:"sourceId"`
	ETag          string                `json:"etag"`
	CheckedAt     string                `json:"checkedAt"`
	ServerVersion string                `json:"serverVersion"`
	Database      string                `json:"database"`
	Charset       string                `json:"charset,omitempty"`
	Metrics       SourceOverviewMetrics `json:"metrics"`
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
	Success       bool                `json:"success"`
	ServerVersion string              `json:"serverVersion"`
	Compatibility SourceCompatibility `json:"compatibility"`
}

// SourceQuery is a bounded, read-only physical-table query for tenant admins.
// It is distinct from business-model OntologySQL and requires a dedicated source.
type SourceQuery struct {
	SQL string `json:"sql"`
}

type SourceQueryResult struct {
	Columns   []string    `json:"columns"`
	Rows      [][]*string `json:"rows"`
	HasMore   bool        `json:"hasMore"`
	Limit     int         `json:"limit"`
	ElapsedMS int64       `json:"elapsedMs"`
}

// SourceCompatibility is a server observation, never a client assertion. Saved
// observations belong to the source configuration ETag and do not change it.
type SourceCompatibility struct {
	Connected           bool     `json:"connected"`
	CompatibilityStatus string   `json:"compatibilityStatus"`
	DetectedProduct     string   `json:"detectedProduct,omitempty"`
	ServerVersion       string   `json:"serverVersion,omitempty"`
	VersionLine         string   `json:"versionLine,omitempty"`
	CapabilityProfile   string   `json:"capabilityProfile,omitempty"`
	Capabilities        []string `json:"capabilities"`
	CheckedAt           string   `json:"checkedAt"`
	Code                string   `json:"code,omitempty"`
	Message             string   `json:"message,omitempty"`
	// Signature is intentionally not exposed in the source management API. It
	// is attached to the live connection and participates in physical plans.
	Signature string `json:"-"`
}

// SchemaStorageMapping describes the current effective definition's physical
// mapping and the columns observed in the database catalog. It contains no
// connection credentials and does not change the definition or database.
type SchemaStorageMapping struct {
	SchemaID       string                   `json:"schemaId"`
	DefinitionETag string                   `json:"definitionETag"`
	Managed        bool                     `json:"managed"`
	Mode           string                   `json:"mode"`
	Primary        StorageMappingTable      `json:"primary"`
	Properties     []StoragePropertyMapping `json:"properties"`
}

type StorageMappingTable struct {
	DataSourceID   string `json:"dataSourceId,omitempty"`
	DataSourceName string `json:"dataSourceName"`
	Driver         string `json:"driver"`
	Namespace      string `json:"namespace"`
	Table          string `json:"table"`
	KeyColumn      string `json:"keyColumn"`
	TenantColumn   string `json:"tenantColumn,omitempty"`
}

type StoragePropertyMapping struct {
	StorageMappingTable
	PropertyID   string `json:"propertyId"`
	PropertyName string `json:"propertyName"`
	Column       string `json:"column"`
	DatabaseType string `json:"databaseType"`
	Exists       bool   `json:"exists"`
}
