package contracts

import "encoding/json"

// ScriptRuntime is the first executable implementation. Source is part of the
// current definition; JavaScript, custom images and external URLs are not yet
// accepted. The Python entrypoint accepts/returns dictionaries keyed by the
// declared Chinese parameter names. HTTP parameters use stable property IDs.
type ScriptRuntime struct {
	Language       string `json:"language"`
	Source         string `json:"source"`
	Entrypoint     string `json:"entrypoint"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type InvocationRequest struct {
	DefinitionID string                     `json:"definitionId"`
	Input        map[string]json.RawMessage `json:"input"`
}

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
}
