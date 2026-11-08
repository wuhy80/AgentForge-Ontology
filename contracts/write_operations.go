package contracts

import "encoding/json"

const MaxBulkWriteItems = 100

// WriteMatch is either an explicit stable instance ID, or a complete business
// unique index identified by its stable definition ID. Physical names and
// arbitrary filters are never accepted.
type WriteMatch struct {
	ID      string                     `json:"id,omitempty"`
	IndexID string                     `json:"indexId,omitempty"`
	Values  map[string]json.RawMessage `json:"values,omitempty"`
}

type UpsertRequest struct {
	Match          WriteMatch                 `json:"match"`
	Values         map[string]json.RawMessage `json:"values"`
	ConflictPolicy string                     `json:"conflictPolicy,omitempty"`
	IdempotencyKey string                     `json:"idempotencyKey"`
}

// IfMatch contains the complete quoted strong HTTP ETag, as returned by the
// ETag header. Each item carries its own condition; the outer header cannot
// stand for several independent objects.
type BulkWriteItem struct {
	ClientKey string                     `json:"clientKey"`
	ID        string                     `json:"id,omitempty"`
	IfMatch   string                     `json:"ifMatch,omitempty"`
	Match     *WriteMatch                `json:"match,omitempty"`
	Values    map[string]json.RawMessage `json:"values,omitempty"`
}

type BulkWriteRequest struct {
	Atomic         *bool           `json:"atomic,omitempty"`
	Items          []BulkWriteItem `json:"items"`
	ConflictPolicy string          `json:"conflictPolicy,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey"`
}

// WriteResult is a permission-filtered receipt. Values may be omitted when the
// caller has no permission to receive the resulting business fields.
type WriteResult struct {
	SchemaID  string                     `json:"schemaId"`
	ClientKey string                     `json:"clientKey,omitempty"`
	Outcome   string                     `json:"outcome"`
	ID        string                     `json:"id"`
	ETag      string                     `json:"etag,omitempty"`
	Values    map[string]json.RawMessage `json:"values,omitempty"`
}

type BulkWriteResult struct {
	SchemaID  string        `json:"schemaId"`
	Atomic    bool          `json:"atomic"`
	Committed bool          `json:"committed"`
	Replayed  bool          `json:"replayed,omitempty"`
	Results   []WriteResult `json:"results"`
}

type WriteOperationCapability struct {
	Name        string   `json:"name"`
	Allowed     bool     `json:"allowed"`
	Reason      string   `json:"reason,omitempty"`
	WriteFields []string `json:"writeFields"`
}

type WriteUniqueKey struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	FieldIDs []string `json:"fieldIds"`
}

// WriteCapabilities contains logical, authorized metadata only. Availability
// is descriptive; each actual mutation checks current authority and storage.
type WriteCapabilities struct {
	SchemaID              string                     `json:"schemaId"`
	Operations            []WriteOperationCapability `json:"operations"`
	Fields                []FormField                `json:"fields"`
	MatchModes            []string                   `json:"matchModes"`
	UniqueKeys            []WriteUniqueKey           `json:"uniqueKeys"`
	OverwriteAllowed      bool                       `json:"overwriteAllowed"`
	OverwriteWriteFields  []string                   `json:"overwriteWriteFields"`
	OverwriteUniqueKeyIDs []string                   `json:"overwriteUniqueKeyIds"`
	MaxBulkItems          int                        `json:"maxBulkItems"`
	MaxRequestBytes       int                        `json:"maxRequestBytes"`
	MaxResponseBytes      int                        `json:"maxResponseBytes"`
}
