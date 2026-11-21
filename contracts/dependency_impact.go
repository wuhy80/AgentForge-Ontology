package contracts

// DefinitionDependencySummary is a view-specific, authorized logical summary.
// ETag is informational; this read-only report never authorizes a mutation.
type DefinitionDependencySummary struct {
	ID         string `json:"id"`
	Kind       Kind   `json:"kind"`
	Name       string `json:"name"`
	CategoryID string `json:"categoryId"`
	GroupID    string `json:"groupId"`
	View       string `json:"view"`
	ETag       string `json:"etag"`
}

type DefinitionDependency struct {
	Definition DefinitionDependencySummary `json:"definition"`
	Reasons    []string                    `json:"reasons"`
}

// DefinitionDependencyImpact covers direct definition references only. It
// excludes instances, policies, saved queries, running jobs and external apps.
// Omitted unauthorized references have no placeholders or hidden counts.
type DefinitionDependencyImpact struct {
	Scope        string                      `json:"scope"`
	Definition   DefinitionDependencySummary `json:"definition"`
	Dependencies []DefinitionDependency      `json:"dependencies"`
	Dependents   []DefinitionDependency      `json:"dependents"`
}

// CallableLifecyclePreview describes a validated target status at read time.
type CallableLifecyclePreview struct {
	DefinitionID string `json:"definitionId"`
	TargetStatus string `json:"targetStatus"`
}
