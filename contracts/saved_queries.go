package contracts

import "encoding/json"

const MaxSavedQueries = 100
const MaxSavedQueryBytes = 128 << 10
const MaxSavedQueryParameters = 64
const MaxSavedQueryPageSize = 50

// SavedQuerySource is editing provenance only. Query, never source text, is
// authoritative after saving; loading/execution never rebinds Chinese names.
type SavedQuerySource struct {
	Kind              string `json:"kind"`
	Text              string `json:"text,omitempty"`
	DefaultCategoryID string `json:"defaultCategoryId,omitempty"`
}

// SavedQueryParameter contains declarations only. Defaults, sample values and
// execution arguments are deliberately absent from the persistence protocol.
type SavedQueryParameter struct {
	Name      string `json:"name"`
	DataType  string `json:"dataType"`
	Nullable  bool   `json:"nullable"`
	Precision int    `json:"precision,omitempty"`
	Scale     int    `json:"scale,omitempty"`
	Array     bool   `json:"array,omitempty"`
	EnumID    string `json:"enumId,omitempty"`
}

type SavedQueryTemplate struct {
	Query      QuerySpec             `json:"query"`
	Parameters []SavedQueryParameter `json:"parameters"`
}

type SavedQueryInput struct {
	Name       string                `json:"name"`
	Source     SavedQuerySource      `json:"source"`
	Query      QuerySpec             `json:"query"`
	Parameters []SavedQueryParameter `json:"parameters"`
}

type SavedQuerySummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ETag      string `json:"etag"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type SavedQuery struct {
	SavedQuerySummary
	Source     SavedQuerySource      `json:"source"`
	Query      QuerySpec             `json:"query"`
	Parameters []SavedQueryParameter `json:"parameters"`
}

type SavedQueryPage struct {
	Data       []SavedQuerySummary `json:"data"`
	NextCursor string              `json:"nextCursor,omitempty"`
	HasMore    bool                `json:"hasMore"`
}

type SavedQueryExecuteRequest struct {
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
	Cursor     string                     `json:"cursor,omitempty"`
}

// Compile accepts no execution argument values. Recompiling is an explicit
// user operation, independent from saved-template loading or execution.
type SavedQueryCompileRequest struct {
	Text              string `json:"text"`
	DefaultCategoryID string `json:"defaultCategoryId,omitempty"`
}
