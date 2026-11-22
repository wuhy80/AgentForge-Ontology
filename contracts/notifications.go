package contracts

// RecordNotice identifies a notification about a record without embedding record data.
type RecordNotice struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	SchemaID string `json:"schemaId"`
	RecordID string `json:"recordId"`
	At       string `json:"at"`
	Read     bool   `json:"read"`
}

type RecordNoticePage struct {
	Data       []RecordNotice `json:"data"`
	NextCursor string         `json:"nextCursor,omitempty"`
}
