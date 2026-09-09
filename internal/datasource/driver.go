// Package datasource provides pluggable data access hidden behind semantic queries.
package datasource

import (
	"context"
	"fmt"
	"sync"
)

type Capabilities struct{ Transactions, JSON, CTE, WindowFunctions, TimeSeries, BatchLookup bool }
type Schema struct {
	Resources map[string][]string `json:"resources"`
}
type Operator string

const (
	OpEqual        Operator = "eq"
	OpGreater      Operator = "gt"
	OpGreaterEqual Operator = "gte"
	OpLess         Operator = "lt"
	OpLessEqual    Operator = "lte"
	OpIn           Operator = "in"
)

type Filter struct {
	Field    string   `json:"field"`
	Operator Operator `json:"operator"`
	Value    any      `json:"value"`
}
type QueryRequest struct {
	Resource string   `json:"resource"`
	Fields   []string `json:"fields,omitempty"`
	Filters  []Filter `json:"filters,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}
type QueryResult struct {
	Rows []map[string]any `json:"rows"`
}

type Driver interface {
	Type() string
	TestConnection(context.Context, map[string]any) error
	Capabilities() Capabilities
	Introspect(context.Context, map[string]any) (*Schema, error)
	Query(context.Context, map[string]any, QueryRequest) (*QueryResult, error)
}

type Registry struct {
	mu      sync.RWMutex
	drivers map[string]Driver
}

func NewRegistry() *Registry { return &Registry{drivers: map[string]Driver{}} }
func (r *Registry) Register(d Driver) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d == nil || d.Type() == "" {
		return fmt.Errorf("invalid driver")
	}
	if _, ok := r.drivers[d.Type()]; ok {
		return fmt.Errorf("driver %q already registered", d.Type())
	}
	r.drivers[d.Type()] = d
	return nil
}
func (r *Registry) Driver(kind string) (Driver, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.drivers[kind]
	if !ok {
		return nil, fmt.Errorf("data source driver %q is not registered", kind)
	}
	return d, nil
}
