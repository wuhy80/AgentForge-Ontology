package contracts

import "encoding/json"

// SQLRange uses zero-based UTF-16 half-open offsets. Line and Column are
// one-based display positions; CRLF is treated as one line break.
type SQLRange struct {
	From   int `json:"from"`
	To     int `json:"to"`
	Line   int `json:"line"`
	Column int `json:"column"`
}

type SQLDiagnostic struct {
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Severity string   `json:"severity"`
	Range    SQLRange `json:"range"`
}

type SQLParameter struct {
	Name      string     `json:"name"`
	DataType  string     `json:"dataType"`
	Nullable  bool       `json:"nullable"`
	Precision int        `json:"precision,omitempty"`
	Scale     int        `json:"scale,omitempty"`
	Ranges    []SQLRange `json:"ranges"`
	Provided  bool       `json:"provided"`
}

type OntologySQLRequest struct {
	Text              string                     `json:"text"`
	Parameters        map[string]json.RawMessage `json:"parameters,omitempty"`
	DefaultCategoryID string                     `json:"defaultCategoryId,omitempty"`
	RequestID         string                     `json:"requestId,omitempty"`
	CursorOffset      int                        `json:"cursorOffset,omitempty"`
}

type OntologySQLAnalysis struct {
	RequestID   string          `json:"requestId"`
	TextHash    string          `json:"textHash"`
	Diagnostics []SQLDiagnostic `json:"diagnostics"`
	Parameters  []SQLParameter  `json:"parameters"`
	Fields      []Property      `json:"fields"`
	Query       *QuerySpec      `json:"query,omitempty"`
	Ready       bool            `json:"ready"`
}

type SQLCompletion struct {
	Label      string   `json:"label"`
	InsertText string   `json:"insertText"`
	Detail     string   `json:"detail"`
	Kind       string   `json:"kind"`
	Range      SQLRange `json:"range"`
}

type OntologySQLCompletions struct {
	RequestID string          `json:"requestId"`
	TextHash  string          `json:"textHash"`
	Items     []SQLCompletion `json:"items"`
}

type OntologySQLFormat struct {
	RequestID   string          `json:"requestId"`
	TextHash    string          `json:"textHash"`
	Text        string          `json:"text"`
	Diagnostics []SQLDiagnostic `json:"diagnostics"`
}

type OntologySQLResult struct {
	QueryResult
	Query     QuerySpec `json:"query"`
	RequestID string    `json:"requestId"`
	TextHash  string    `json:"textHash"`
}
