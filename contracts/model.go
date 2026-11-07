// Package contracts contains versioned wire contracts, not service repositories.
package contracts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type Kind string

const (
	KindSchema   Kind = "schema"
	KindEnum     Kind = "enum"
	KindLink     Kind = "link"
	KindAction   Kind = "action"
	KindFunction Kind = "function"
)

type Definition struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenantId"`
	CategoryID  string    `json:"categoryId"`
	GroupID     string    `json:"groupId"`
	Kind        Kind      `json:"kind"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Schema      *Schema   `json:"schema,omitempty"`
	Enum        *Enum     `json:"enum,omitempty"`
	Link        *Link     `json:"link,omitempty"`
	Action      *Callable `json:"action,omitempty"`
	Function    *Callable `json:"function,omitempty"`
}

type Schema struct {
	Mode           string          `json:"mode"`
	StorageLayout  string          `json:"storageLayout"`
	StorageBinding *StorageBinding `json:"storageBinding,omitempty"`
	Properties     []Property      `json:"properties"`
	Indexes        []Index         `json:"indexes,omitempty"`
}

// StorageBinding identifies the primary table that determines entity identity.
// Additional property sources must join its IDColumn through a unique key.
type StorageBinding struct {
	DataSourceID string `json:"dataSourceId"`
	Namespace    string `json:"namespace,omitempty"`
	Table        string `json:"table"`
	IDColumn     string `json:"idColumn,omitempty"`
	TenantColumn string `json:"tenantColumn,omitempty"`
}

// PropertyMapping binds a field to one physical column. An omitted table uses
// the primary table; secondary tables require a unique KeyColumn. Credentials
// and arbitrary SQL are never part of a definition.
type PropertyMapping struct {
	DataSourceID string `json:"dataSourceId"`
	Namespace    string `json:"namespace,omitempty"`
	Table        string `json:"table,omitempty"`
	Column       string `json:"column"`
	KeyColumn    string `json:"keyColumn,omitempty"`
	TenantColumn string `json:"tenantColumn,omitempty"`
}

type Property struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// DataType supports string, text, longtext, uuid, int64, decimal, boolean,
	// and timestamp. Text byte budgets are defined in limits.go.
	DataType  string           `json:"dataType"`
	Required  bool             `json:"required,omitempty"`
	Nullable  bool             `json:"nullable,omitempty"`
	ReadOnly  bool             `json:"readOnly,omitempty"`
	Precision int              `json:"precision,omitempty"`
	Scale     int              `json:"scale,omitempty"`
	EnumID    string           `json:"enumId,omitempty"`
	Default   json.RawMessage  `json:"default,omitempty"`
	Mapping   *PropertyMapping `json:"mapping,omitempty"`
}

type Index struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Unique   bool     `json:"unique"`
	FieldIDs []string `json:"fieldIds"`
}

type Enum struct {
	Values []EnumValue `json:"values"`
}
type EnumValue struct {
	Code       string `json:"code"`
	Label      string `json:"label"`
	Deprecated bool   `json:"deprecated,omitempty"`
}

type Link struct {
	SourceSchemaID string `json:"sourceSchemaId"`
	TargetSchemaID string `json:"targetSchemaId"`
	ForwardName    string `json:"forwardName"`
	ReverseName    string `json:"reverseName"`
	Cardinality    string `json:"cardinality"`
	OnDelete       string `json:"onDelete"`
}

// Callable definitions are discoverable metadata in the first increment;
// invocation is advertised separately in capabilities.
type Callable struct {
	Input  []Property `json:"input,omitempty"`
	Output []Property `json:"output,omitempty"`
	// TargetSchemaIDs associates Action metadata with existing schemas. It does
	// not grant invocation or instance access and is unsupported on Function.
	TargetSchemaIDs []string `json:"targetSchemaIds,omitempty"`
	FunctionID      string   `json:"functionId,omitempty"`
	ArtifactRef     string   `json:"artifactRef,omitempty"`
	SideEffects     string   `json:"sideEffects"`
}

type Organization struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenantId"`
	CategoryID string `json:"categoryId,omitempty"`
	Name       string `json:"name"`
}

type CurrentDefinition struct {
	Definition Definition `json:"definition"`
	ETag       string     `json:"etag"`
	Status     string     `json:"status"`
}

type Publication struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenantId"`
	DefinitionID string `json:"definitionId"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

type PrepareRequest struct {
	OperationID    string     `json:"operationId"`
	Definition     Definition `json:"definition"`
	PreviousDigest string     `json:"previousDigest"`
	Digest         string     `json:"digest"`
}

type PrepareResult struct {
	OperationID string   `json:"operationId"`
	Status      string   `json:"status"`
	Statements  []string `json:"statements,omitempty"`
}

type ActivateRequest struct {
	OperationID  string `json:"operationId"`
	DefinitionID string `json:"definitionId"`
	Digest       string `json:"digest"`
}

type Instance struct {
	ID       string                     `json:"id"`
	SchemaID string                     `json:"schemaId"`
	Values   map[string]json.RawMessage `json:"values"`
	ETag     string                     `json:"etag"`
}

type WriteRequest struct {
	Values         map[string]json.RawMessage `json:"values"`
	IdempotencyKey string                     `json:"idempotencyKey,omitempty"`
}

type QuerySpec struct {
	From    Source      `json:"from"`
	Where   *Expression `json:"where,omitempty"`
	Select  []string    `json:"select"`
	OrderBy []Order     `json:"orderBy,omitempty"`
	Page    Page        `json:"page"`
}

type Source struct {
	SchemaID string `json:"schemaId,omitempty"`
	Category string `json:"category,omitempty"`
	Schema   string `json:"schema,omitempty"`
}

type Expression struct {
	Op    string          `json:"op"`
	Field string          `json:"field,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
	Args  []Expression    `json:"args,omitempty"`
}
type Order struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}
type Page struct {
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor,omitempty"`
}
type QueryResult struct {
	Data       []Instance `json:"data"`
	Fields     []Property `json:"fields"`
	NextCursor string     `json:"nextCursor,omitempty"`
	HasMore    bool       `json:"hasMore"`
}

type Principal struct {
	Subject   string `json:"subject"`
	TenantID  string `json:"tenantId"`
	ExpiresAt int64  `json:"expiresAt"`
}

type AuthorizationRequest struct {
	Operation  string `json:"operation"`
	ResourceID string `json:"resourceId,omitempty"`
	Kind       Kind   `json:"kind,omitempty"`
}

// Empty field lists grant no fields. Only an explicit "*" grants all fields.
type Decision struct {
	Allow        bool        `json:"allow"`
	ReadFields   []string    `json:"readFields"`
	WriteFields  []string    `json:"writeFields"`
	RowFilter    *Expression `json:"rowFilter,omitempty"`
	NewRowFilter *Expression `json:"newRowFilter,omitempty"`
	State        string      `json:"state"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("generate ID: %w", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
