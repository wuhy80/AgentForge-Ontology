package contracts

import "encoding/json"

// DataWriteCapability bounds a callable's write plan. It grants no authority:
// every operation also requires the invoking user's current row/field rights.
type DataWriteCapability struct {
	SchemaID   string   `json:"schemaId"`
	Operations []string `json:"operations"`
}

// TransactionOperation is a logical mutation, never SQL. Conditions and values
// follow the eight schema write interfaces. A transaction contains 1..100 items.
type TransactionOperation struct {
	SchemaID       string                     `json:"schemaId"`
	Operation      string                     `json:"operation"`
	ClientKey      string                     `json:"clientKey"`
	ID             string                     `json:"id,omitempty"`
	IfMatch        string                     `json:"ifMatch,omitempty"`
	Match          *WriteMatch                `json:"match,omitempty"`
	Values         map[string]json.RawMessage `json:"values,omitempty"`
	ConflictPolicy string                     `json:"conflictPolicy,omitempty"`
}

type WriteTransactionRequest struct {
	IdempotencyKey string                 `json:"idempotencyKey"`
	Operations     []TransactionOperation `json:"operations"`
}

// Status is committed or failed for durable receipts; not_found is only a
// lookup response and never proves an earlier request cannot still commit.
type WriteTransactionResult struct {
	Atomic    bool          `json:"atomic"`
	Committed bool          `json:"committed"`
	Replayed  bool          `json:"replayed,omitempty"`
	Status    string        `json:"status"`
	Results   []WriteResult `json:"results"`
	Error     *Error        `json:"error,omitempty"`
}

// ScriptExecutionResult is a private runner envelope. No credentials or live
// database handles enter the sandbox; Execution validates this plan then asks
// Data to commit it once, after validating the declared function output.
type ScriptExecutionResult struct {
	Output     map[string]json.RawMessage `json:"output"`
	Operations []TransactionOperation     `json:"operations"`
}

// ExecutionTransactionRequest is confined to an authenticated internal route.
// Data revalidates the original session, current definitions and capabilities;
// the service credential by itself never authorizes business writes.
type ExecutionTransactionRequest struct {
	Session              SessionCheck            `json:"session"`
	InvocationID         string                  `json:"invocationId"`
	DefinitionID         string                  `json:"definitionId"`
	DefinitionDigest     string                  `json:"definitionDigest"`
	ImplementationID     string                  `json:"implementationId"`
	ImplementationDigest string                  `json:"implementationDigest"`
	Lookup               bool                    `json:"lookup,omitempty"`
	Transaction          WriteTransactionRequest `json:"transaction"`
}
