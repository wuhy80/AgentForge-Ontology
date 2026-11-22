package contracts

type RecordComment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	ParentID  string `json:"parentId,omitempty"`
	CreatedAt string `json:"createdAt"`
	ETag      string `json:"etag"`
	Editable  bool   `json:"editable"`
}

type RecordCommentPage struct {
	Data       []RecordComment `json:"data"`
	NextCursor string          `json:"nextCursor,omitempty"`
	CanPost    bool            `json:"canPost"`
}
