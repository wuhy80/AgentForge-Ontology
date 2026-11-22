package contracts

// NoticeDelivery describes an authorized delivery result.
type NoticeDelivery struct {
	Channel string `json:"channel"`
	Status  string `json:"status"`
	At      string `json:"at"`
	Failure string `json:"failure,omitempty"`
}

type RecordNotice struct {
	Delivery *NoticeDelivery `json:"delivery,omitempty"`
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	SchemaID string          `json:"schemaId"`
	RecordID string          `json:"recordId"`
	At       string          `json:"at"`
	Read     bool            `json:"read"`
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

const MaxMentionTargets = 20

type MentionTargetCheck struct {
	Actor     Principal `json:"actor"`
	TargetIDs []string  `json:"targetIds"`
}
