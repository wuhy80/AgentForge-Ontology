package contracts

import "encoding/json"

const MaxAggregateGroups = 1000
const MaxAggregateGroupFields = 3
const MaxAggregateMeasures = 20

// AggregateQuery describes an independent, read-only result grain. It cannot
// be combined with entity projection, Link navigation or relationship rows.
type AggregateQuery struct {
	GroupBy  []string           `json:"groupBy,omitempty"`
	Measures []AggregateMeasure `json:"measures"`
}

// Count with no Field means COUNT(*). All other measures require a field.
// IDs match ^m[A-Za-z0-9_]{0,63}$ and identify result values, never instances.
type AggregateMeasure struct {
	ID    string `json:"id"`
	Op    string `json:"op"`
	Field string `json:"field,omitempty"`
	Name  string `json:"name,omitempty"`
}

// AggregateRow deliberately has no instance ID, Schema ID, ETag or entities.
type AggregateRow struct {
	Values map[string]json.RawMessage `json:"values"`
}
