// Package ontology defines the authoring model for the industrial ontology.
package ontology

import "time"

// Draft is the mutable authoring document. Runtime code must only consume a Snapshot.
type Draft struct {
	Workspace   Workspace    `json:"workspace"`
	Vendors     []Vendor     `json:"vendors"`
	DataSources []DataSource `json:"dataSources,omitempty"`
	UpdatedAt   time.Time    `json:"updatedAt,omitempty"`
}

type Workspace struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName,omitempty"`
	Description string   `json:"description,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

type Vendor struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName,omitempty"`
	Description string     `json:"description,omitempty"`
	Aliases     []string   `json:"aliases,omitempty"`
	Domains     []Domain   `json:"domains,omitempty"`
	Enums       []EnumType `json:"enums,omitempty"`
	Functions   []Function `json:"functions,omitempty"`
}

type Domain struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	DisplayName string       `json:"displayName,omitempty"`
	Description string       `json:"description,omitempty"`
	Aliases     []string     `json:"aliases,omitempty"`
	Objects     []ObjectType `json:"objects,omitempty"`
}

type ObjectType struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	DisplayName string       `json:"displayName,omitempty"`
	Description string       `json:"description,omitempty"`
	Aliases     []string     `json:"aliases,omitempty"`
	Tags        []string     `json:"tags,omitempty"`
	Properties  []Property   `json:"properties,omitempty"`
	Links       []Link       `json:"links,omitempty"`
	Actions     []Action     `json:"actions,omitempty"`
	Binding     *DataBinding `json:"dataBinding,omitempty"`
	StorageMode StorageMode  `json:"storageMode,omitempty"`
}

type StorageMode string

const (
	StorageVirtual      StorageMode = "virtual"
	StorageMaterialized StorageMode = "materialized"
)

type Property struct {
	Name        string       `json:"name"`
	DisplayName string       `json:"displayName,omitempty"`
	Description string       `json:"description,omitempty"`
	Aliases     []string     `json:"aliases,omitempty"`
	Examples    []any        `json:"examples,omitempty"`
	Type        PropertyType `json:"type"`
	Enum        string       `json:"enum,omitempty"`
	Unit        string       `json:"unit,omitempty"`
	Precision   *int         `json:"precision,omitempty"`
	Min         *float64     `json:"min,omitempty"`
	Max         *float64     `json:"max,omitempty"`
	Default     any          `json:"default,omitempty"`
	Nullable    bool         `json:"nullable,omitempty"`
	ReadOnly    bool         `json:"readOnly,omitempty"`
	PrimaryKey  bool         `json:"primaryKey,omitempty"`
}

type PropertyType string

const (
	TypeString    PropertyType = "string"
	TypeText      PropertyType = "text"
	TypeInt       PropertyType = "int"
	TypeInt64     PropertyType = "int64"
	TypeFloat     PropertyType = "float"
	TypeDouble    PropertyType = "double"
	TypeDecimal   PropertyType = "decimal"
	TypeBool      PropertyType = "bool"
	TypeDate      PropertyType = "date"
	TypeDateTime  PropertyType = "datetime"
	TypeTimestamp PropertyType = "timestamp"
	TypeJSON      PropertyType = "json"
	TypeEnum      PropertyType = "enum"
	TypeImage     PropertyType = "image"
	TypeFile      PropertyType = "file"
	TypeObject    PropertyType = "object"
	TypeArray     PropertyType = "array"
)

type EnumType struct {
	Name        string      `json:"name"`
	DisplayName string      `json:"displayName,omitempty"`
	Description string      `json:"description,omitempty"`
	Aliases     []string    `json:"aliases,omitempty"`
	Values      []EnumValue `json:"values"`
}

type EnumValue struct {
	Value       any    `json:"value"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
}

type Cardinality string

const (
	OneToOne   Cardinality = "one-to-one"
	OneToMany  Cardinality = "one-to-many"
	ManyToOne  Cardinality = "many-to-one"
	ManyToMany Cardinality = "many-to-many"
)

type Link struct {
	Name        string       `json:"name"`
	DisplayName string       `json:"displayName,omitempty"`
	Description string       `json:"description,omitempty"`
	Aliases     []string     `json:"aliases,omitempty"`
	Target      ObjectRef    `json:"target"`
	ReverseName string       `json:"reverseName"`
	Cardinality Cardinality  `json:"cardinality"`
	Resolver    LinkResolver `json:"resolver"`
}

type ObjectRef struct {
	Vendor string `json:"vendor"`
	Domain string `json:"domain"`
	Object string `json:"object"`
}

type LinkResolver struct {
	Type        string `json:"type"` // field in the MVP; sql/function are reserved.
	SourceField string `json:"sourceField,omitempty"`
	TargetField string `json:"targetField,omitempty"`
}

type DataSource struct {
	ID          string         `json:"id"`
	Driver      string         `json:"driver"`
	Description string         `json:"description,omitempty"`
	Config      map[string]any `json:"config,omitempty"`
}

type DataBinding struct {
	Source     string                  `json:"source"`
	Resource   string                  `json:"resource"`
	PrimaryKey []string                `json:"primaryKey"`
	Fields     map[string]FieldBinding `json:"fields"`
}

type FieldBinding struct {
	Field string `json:"field"`
}

type IOField struct {
	Name        string       `json:"name"`
	Type        PropertyType `json:"type"`
	Description string       `json:"description,omitempty"`
	Required    bool         `json:"required,omitempty"`
}

type Function struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName,omitempty"`
	Description string    `json:"description"`
	Aliases     []string  `json:"aliases,omitempty"`
	Input       []IOField `json:"input,omitempty"`
	Output      IOField   `json:"output"`
	Provider    Provider  `json:"provider"`
}

type Action struct {
	Name           string    `json:"name"`
	DisplayName    string    `json:"displayName,omitempty"`
	Description    string    `json:"description"`
	Aliases        []string  `json:"aliases,omitempty"`
	Parameters     []IOField `json:"parameters,omitempty"`
	Provider       Provider  `json:"provider"`
	Idempotent     bool      `json:"idempotent,omitempty"`
	TimeoutSeconds int       `json:"timeoutSeconds,omitempty"`
}

type Provider struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config,omitempty"`
}
