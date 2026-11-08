package contracts

import "encoding/json"

// ScriptRuntime is the first executable implementation. Source is part of the
// current Action or Function definition; JavaScript, custom images and external
// URLs are not yet accepted. The Python entrypoint accepts/returns dictionaries
// keyed by declared Chinese parameter names. HTTP uses stable property IDs.
type ScriptRuntime struct {
	Language       string `json:"language"`
	Source         string `json:"source"`
	Entrypoint     string `json:"entrypoint"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type InvocationRequest struct {
	DefinitionID string                     `json:"definitionId"`
	Input        map[string]json.RawMessage `json:"input"`
	// A non-empty ETag selects the exact saved draft instead of current code.
	DraftETag string `json:"draftEtag,omitempty"`
}

// SourceDiagnostic contains only controlled messages and one-based source
// coordinates. It never contains exception text, source snippets or a stack.
type SourceDiagnostic struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Line      int    `json:"line"`
	Column    int    `json:"column,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
	EndColumn int    `json:"endColumn,omitempty"`
}

type CallableTestCase struct {
	ID    string                     `json:"id"`
	Name  string                     `json:"name"`
	Input map[string]json.RawMessage `json:"input"`
}

type InvocationMode string

const (
	InvocationModeCurrent InvocationMode = "current"
	InvocationModeDraft   InvocationMode = "draft"
)

// Invocation deliberately omits code, raw input, stdout and tracebacks. The
// execution service owns the snapshot; every response requires current access.
type Invocation struct {
	ID             string                     `json:"id"`
	DefinitionID   string                     `json:"definitionId"`
	DefinitionName string                     `json:"definitionName"`
	Kind           Kind                       `json:"kind"`
	Status         string                     `json:"status"`
	Output         map[string]json.RawMessage `json:"output,omitempty"`
	Error          *Error                     `json:"error,omitempty"`
	CreatedAt      string                     `json:"createdAt"`
	FinishedAt     string                     `json:"finishedAt,omitempty"`
	CodeDigest     string                     `json:"codeDigest"`
	Mode           InvocationMode             `json:"mode"`
	DraftETag      string                     `json:"draftEtag,omitempty"`
	Diagnostics    []SourceDiagnostic         `json:"diagnostics,omitempty"`
}
