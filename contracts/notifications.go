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

// RecordUnreadSummary counts visible unread notifications within the server's scan budget.
type RecordUnreadSummary struct {
	Count int64 `json:"count"`
	Exact bool  `json:"exact"`
}

type RecordNoticePage struct {
	Unread     *RecordUnreadSummary `json:"unread,omitempty"`
	Data       []RecordNotice       `json:"data"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

type RecordFollow struct {
	Following bool `json:"following"`
}
