package contracts

// BusinessDirectoryRequest is exchanged only over the service-authenticated
// Metadata bridge. The authority derives tenant/user from Check, never headers.
type BusinessDirectoryRequest struct {
	Check      SessionCheck `json:"check"`
	CategoryID string       `json:"categoryId,omitempty"`
	SchemaID   string       `json:"schemaId,omitempty"`
	After      string       `json:"after,omitempty"`
	Limit      int          `json:"limit"`
}

type BusinessDirectoryEntry struct {
	Current  CurrentDefinition `json:"current"`
	Category string            `json:"category"`
	Decision Decision          `json:"decision"`
}

// BusinessDirectoryPage may contain private definition material needed by Data.
// It is never a public response; Data emits only the authorized logical fields.
type BusinessDirectoryPage struct {
	Entries    []BusinessDirectoryEntry `json:"entries"`
	HasMore    bool                     `json:"hasMore"`
	NextCursor string                   `json:"nextCursor,omitempty"`
	Scanned    int                      `json:"scanned"`
}

type FormDefinition struct {
	Definition    FormSchemaDefinition `json:"definition"`
	CreateAllowed bool                 `json:"createAllowed"`
	PatchAllowed  bool                 `json:"patchAllowed"`
	Operation     string               `json:"operation"`
}

type FormSchemaDefinition struct {
	ID     string     `json:"id"`
	Schema FormSchema `json:"schema"`
}

type FormSchema struct {
	Properties     []FormField `json:"properties"`
	StorageBinding bool        `json:"storageBinding,omitempty"`
}

// FormField intentionally omits physical mappings, defaults and policy filters.
type FormField struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	DataType  string `json:"dataType"`
	Required  bool   `json:"required"`
	Nullable  bool   `json:"nullable"`
	ReadOnly  bool   `json:"readOnly"`
	Precision int    `json:"precision,omitempty"`
	Scale     int    `json:"scale,omitempty"`
	EnumID    string `json:"enumId,omitempty"`
}
