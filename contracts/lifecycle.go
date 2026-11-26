package contracts

// Record lifecycle definitions travel with the current Schema definition. Stage
// and transition IDs are stable; names are display text only. Data keeps the
// per-record stage and its history so a publication never rewrites history.
const (
	MaxLifecycleStages      = 20
	MaxLifecycleTransitions = 40
	MaxLifecycleNameRunes   = 64
	MaxLifecycleNoteRunes   = 500
	MaxLifecycleHistory     = 50
)

// SchemaLifecycle declares the ordered stages of one managed record and the
// transitions allowed between them. A transition is selected by stable ID, so
// renaming stages or transitions never changes a bound request.
type SchemaLifecycle struct {
	InitialStageID string                `json:"initialStageId"`
	Stages         []LifecycleStage      `json:"stages"`
	Transitions    []LifecycleTransition `json:"transitions,omitempty"`
}

// LifecycleStage is one named stage. Order in the definition is the declared
// display order; it does not constrain transitions.
type LifecycleStage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LifecycleTransition is one directed, named transition between two stages.
type LifecycleTransition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// RecordLifecycle is the authorized view of one managed record's current stage,
// the transitions the current account may run now, and its transition history.
type RecordLifecycle struct {
	Stages       []LifecycleStage      `json:"stages"`
	Transitions  []LifecycleTransition `json:"transitions"`
	StageID      string                `json:"stageId,omitempty"`
	UpdatedAt    string                `json:"updatedAt,omitempty"`
	Persisted    bool                  `json:"persisted"`
	StageUnknown bool                  `json:"stageUnknown,omitempty"`
	Available    []string              `json:"available,omitempty"`
	History      []LifecycleRun        `json:"history"`
	Truncated    bool                  `json:"truncated,omitempty"`
}

// LifecycleRun is one committed transition. Name and note are display text; the
// IDs remain authoritative even after the definition changes.
type LifecycleRun struct {
	ID                 string `json:"id"`
	TransitionID       string `json:"transitionId"`
	Name               string `json:"name,omitempty"`
	From               string `json:"from"`
	To                 string `json:"to"`
	Note               string `json:"note,omitempty"`
	At                 string `json:"at"`
	NotificationFailed bool   `json:"notificationFailed,omitempty"`
}
