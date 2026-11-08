package contracts

// QueryContext contains only discoverable, query-authorized logical names and
// capabilities. It intentionally carries no physical mappings or business rows.
type QueryContext struct {
	Models     []QueryContextModel `json:"models"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

type QueryContextModel struct {
	SchemaID   string              `json:"schemaId"`
	CategoryID string              `json:"categoryId"`
	Category   string              `json:"category"`
	Name       string              `json:"name"`
	Fields     []QueryContextField `json:"fields"`
}

type QueryContextField struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	DataType   string      `json:"dataType"`
	Nullable   bool        `json:"nullable"`
	Precision  int         `json:"precision,omitempty"`
	Scale      int         `json:"scale,omitempty"`
	EnumID     string      `json:"enumId,omitempty"`
	EnumValues []EnumValue `json:"enumValues,omitempty"`
	EnumETag   string      `json:"enumEtag,omitempty"`
	Filterable bool        `json:"filterable"`
	Sortable   bool        `json:"sortable"`
}
