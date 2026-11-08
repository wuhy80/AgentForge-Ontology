package contracts

import "encoding/json"

// CallableSummary is the invocation-facing current signature. It intentionally
// excludes source, artifacts, test cases, storage mappings and implementation IDs.
// Discovery requires current invocation permission, not full definition access.
type CallableSummary struct {
	ID          string              `json:"id"`
	Kind        Kind                `json:"kind"`
	CategoryID  string              `json:"categoryId"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Input       []CallableParameter `json:"input"`
	Output      []CallableParameter `json:"output"`
	SideEffects string              `json:"sideEffects"`
}

type CallableParameter struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	DataType   string          `json:"dataType"`
	Required   bool            `json:"required,omitempty"`
	Nullable   bool            `json:"nullable,omitempty"`
	Precision  int             `json:"precision,omitempty"`
	Scale      int             `json:"scale,omitempty"`
	EnumID     string          `json:"enumId,omitempty"`
	EnumValues []EnumValue     `json:"enumValues,omitempty"`
	EnumETag   string          `json:"enumEtag,omitempty"`
	Default    json.RawMessage `json:"default,omitempty"`
}

type CallableDirectory struct {
	Data       []CallableSummary `json:"data"`
	HasMore    bool              `json:"hasMore"`
	NextCursor string            `json:"nextCursor,omitempty"`
}
