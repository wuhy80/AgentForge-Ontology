package contracts

// StorageRelocation is a fixed manifest entry for an explicit offline
// maintenance operation. It is not accepted by ordinary publication endpoints.
// The complete previous definition permits verification and resumption without
// changing stable model, property, index, or instance identities.
type StorageRelocation struct {
	OperationID string            `json:"operationId"`
	SourceETag  string            `json:"sourceETag"`
	Previous    CurrentDefinition `json:"previous"`
	Definition  Definition        `json:"definition"`
	Digest      string            `json:"digest"`
}

// StorageRelocationResult contains verification evidence, never row contents or
// connection credentials. Source rows and their original tables remain intact.
type StorageRelocationResult struct {
	DefinitionID  string   `json:"definitionId"`
	Rows          int      `json:"rows"`
	ContentDigest string   `json:"contentDigest"`
	Statements    []string `json:"statements,omitempty"`
}
