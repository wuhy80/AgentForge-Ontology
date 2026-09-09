package datasource

import (
	"context"
	"fmt"
	"reflect"
	"sort"
)

// MemoryDriver is a deterministic virtual source for development and tests.
type MemoryDriver struct{ Resources map[string][]map[string]any }

func (d *MemoryDriver) Type() string                                         { return "memory" }
func (d *MemoryDriver) TestConnection(context.Context, map[string]any) error { return nil }
func (d *MemoryDriver) Capabilities() Capabilities                           { return Capabilities{BatchLookup: true} }
func (d *MemoryDriver) Introspect(_ context.Context, _ map[string]any) (*Schema, error) {
	s := &Schema{Resources: map[string][]string{}}
	for resource, rows := range d.Resources {
		set := map[string]bool{}
		for _, row := range rows {
			for k := range row {
				set[k] = true
			}
		}
		for k := range set {
			s.Resources[resource] = append(s.Resources[resource], k)
		}
		sort.Strings(s.Resources[resource])
	}
	return s, nil
}
func (d *MemoryDriver) Query(ctx context.Context, config map[string]any, req QueryRequest) (*QueryResult, error) {
	resources := d.Resources
	if configured := configuredResources(config); len(configured) > 0 {
		resources = configured
	}
	rows, ok := resources[req.Resource]
	if !ok {
		return nil, fmt.Errorf("resource %q does not exist", req.Resource)
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	out := &QueryResult{Rows: []map[string]any{}}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !matches(row, req.Filters) {
			continue
		}
		projected := map[string]any{}
		if len(req.Fields) == 0 {
			for k, v := range row {
				projected[k] = v
			}
		} else {
			for _, k := range req.Fields {
				if v, ok := row[k]; ok {
					projected[k] = v
				}
			}
		}
		out.Rows = append(out.Rows, projected)
		if len(out.Rows) >= limit {
			break
		}
	}
	return out, nil
}
func matches(row map[string]any, filters []Filter) bool {
	for _, f := range filters {
		value, ok := row[f.Field]
		if !ok {
			return false
		}
		switch f.Operator {
		case OpEqual:
			if !reflect.DeepEqual(value, f.Value) {
				return false
			}
		case OpIn:
			values, ok := f.Value.([]any)
			if !ok {
				return false
			}
			found := false
			for _, candidate := range values {
				if reflect.DeepEqual(value, candidate) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		case OpGreater, OpGreaterEqual, OpLess, OpLessEqual:
			a, aok := number(value)
			b, bok := number(f.Value)
			if !aok || !bok {
				return false
			}
			if f.Operator == OpGreater && a <= b {
				return false
			}
			if f.Operator == OpGreaterEqual && a < b {
				return false
			}
			if f.Operator == OpLess && a >= b {
				return false
			}
			if f.Operator == OpLessEqual && a > b {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// configuredResources makes the memory driver useful in examples without adding a database.
// Production drivers should obtain rows from their external system, never from ontology metadata.
func configuredResources(config map[string]any) map[string][]map[string]any {
	out := map[string][]map[string]any{}
	raw, ok := config["resources"].(map[string]any)
	if !ok {
		return out
	}
	for name, value := range raw {
		items, ok := value.([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			if row, ok := item.(map[string]any); ok {
				out[name] = append(out[name], row)
			}
		}
	}
	return out
}
