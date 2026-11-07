package contracts

import "encoding/json"

// RowQuery is a bounded INNER Link join tree. Node IDs are n0..n4, in join
// order, and n0 is QuerySpec.From. Field references in Select/Where/OrderBy are
// nodeID + "." + stable property ID. Aliases only label query scopes/outputs.
// A row represents one binding of all nodes, without entity/value deduplication.
type RowQuery struct {
	Nodes []RowQueryNode `json:"nodes"`
	Joins []RowQueryJoin `json:"joins"`
}

type RowQueryNode struct {
	ID       string `json:"id"`
	SchemaID string `json:"schemaId"`
	Alias    string `json:"alias"`
}

type RowQueryJoin struct {
	From      string `json:"from"`
	To        string `json:"to"`
	LinkID    string `json:"linkId"`
	Direction string `json:"direction"`
}

type QueryRowEntity struct {
	NodeID     string `json:"nodeId"`
	SchemaID   string `json:"schemaId"`
	InstanceID string `json:"instanceId"`
}

type QueryRow struct {
	ID       string                     `json:"id"`
	Values   map[string]json.RawMessage `json:"values"`
	Entities []QueryRowEntity           `json:"entities"`
}

// Path planning reads authorized definitions only. It never reads business rows.
// Current SQL is authoritatively bound before any extension is proposed.
type QueryPathsRequest struct {
	OntologySQLRequest
	TargetSchemaID string `json:"targetSchemaId"`
}

type QueryPathStep struct {
	LinkID    string            `json:"linkId"`
	Name      string            `json:"name"`
	Category  string            `json:"category"`
	Direction string            `json:"direction"`
	FromAlias string            `json:"fromAlias"`
	ToAlias   string            `json:"toAlias"`
	Target    QueryContextModel `json:"target"`
}

type QueryPathCandidate struct {
	ID        string          `json:"id"`
	FromAlias string          `json:"fromAlias"`
	Steps     []QueryPathStep `json:"steps"`
	Text      string          `json:"text"`
}

type QueryPathsResult struct {
	RequestID   string               `json:"requestId"`
	TextHash    string               `json:"textHash"`
	Candidates  []QueryPathCandidate `json:"candidates"`
	Diagnostics []SQLDiagnostic      `json:"diagnostics"`
	Truncated   bool                 `json:"truncated"`
}

// Field insertion uses server-authorized aliases after a full parse/bind. It
// returns an edit, never executes SQL, and does not replace parameter drafts.
type QueryFieldInsertionRequest struct {
	OntologySQLRequest
	SchemaID      string `json:"schemaId"`
	FieldID       string `json:"fieldId"`
	Alias         string `json:"alias,omitempty"`
	SelectionFrom int    `json:"selectionFrom"`
	SelectionTo   int    `json:"selectionTo"`
}

type QueryFieldInsertionResult struct {
	RequestID   string          `json:"requestId"`
	TextHash    string          `json:"textHash"`
	Text        string          `json:"text"`
	From        int             `json:"from"`
	To          int             `json:"to"`
	Diagnostics []SQLDiagnostic `json:"diagnostics"`
}
