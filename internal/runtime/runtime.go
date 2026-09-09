// Package runtime executes semantic object queries without exposing source details.
package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/agentforge/ontology/internal/datasource"
	"github.com/agentforge/ontology/internal/publish"
)

type Service struct {
	mu        sync.RWMutex
	snapshots map[string]*publish.Snapshot
	drivers   *datasource.Registry
}

func New(drivers *datasource.Registry) *Service {
	return &Service{snapshots: map[string]*publish.Snapshot{}, drivers: drivers}
}
func (s *Service) Activate(snapshot *publish.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[snapshot.Workspace.ID] = snapshot
}
func (s *Service) Snapshot(workspace string) (*publish.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.snapshots[workspace]
	if !ok {
		return nil, fmt.Errorf("workspace %q has no published snapshot", workspace)
	}
	return snap, nil
}

type SemanticFilter struct {
	Property string              `json:"property"`
	Operator datasource.Operator `json:"operator"`
	Value    any                 `json:"value"`
}
type ObjectQuery struct {
	Workspace string           `json:"workspace"`
	Object    string           `json:"object"`
	Fields    []string         `json:"fields,omitempty"`
	Filters   []SemanticFilter `json:"filters,omitempty"`
	Limit     int              `json:"limit,omitempty"`
}
type ObjectResult struct {
	Object        string           `json:"object"`
	PublicationID string           `json:"publicationId"`
	Items         []map[string]any `json:"items"`
}

func (s *Service) Describe(_ context.Context, workspace, key string) (*publish.RuntimeObject, error) {
	snap, err := s.Snapshot(workspace)
	if err != nil {
		return nil, err
	}
	obj, ok := snap.Objects[key]
	if !ok {
		return nil, fmt.Errorf("object %q does not exist", key)
	}
	return obj, nil
}
func (s *Service) Query(ctx context.Context, q ObjectQuery) (ObjectResult, error) {
	snap, err := s.Snapshot(q.Workspace)
	if err != nil {
		return ObjectResult{}, err
	}
	obj, ok := snap.Objects[q.Object]
	if !ok {
		return ObjectResult{}, fmt.Errorf("object %q does not exist", q.Object)
	}
	rows, err := s.queryObject(ctx, snap, obj, q.Fields, q.Filters, q.Limit)
	return ObjectResult{Object: q.Object, PublicationID: snap.PublicationID, Items: rows}, err
}
func (s *Service) Get(ctx context.Context, workspace, object string, id any) (map[string]any, error) {
	snap, err := s.Snapshot(workspace)
	if err != nil {
		return nil, err
	}
	obj, ok := snap.Objects[object]
	if !ok {
		return nil, fmt.Errorf("object %q does not exist", object)
	}
	if len(obj.Definition.Binding.PrimaryKey) != 1 {
		return nil, fmt.Errorf("get supports single-column primary keys")
	}
	result, err := s.queryObject(ctx, snap, obj, nil, []SemanticFilter{{Property: obj.Definition.Binding.PrimaryKey[0], Operator: datasource.OpEqual, Value: id}}, 1)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%s instance %v not found", object, id)
	}
	return result[0], nil
}

func (s *Service) queryObject(ctx context.Context, snap *publish.Snapshot, obj *publish.RuntimeObject, fields []string, filters []SemanticFilter, limit int) ([]map[string]any, error) {
	b := obj.Definition.Binding
	if b == nil {
		return nil, fmt.Errorf("object %q has no data binding", obj.Key)
	}
	ds, ok := snap.DataSources[b.Source]
	if !ok {
		return nil, fmt.Errorf("data source %q unavailable", b.Source)
	}
	driver, err := s.drivers.Driver(ds.Driver)
	if err != nil {
		return nil, err
	}
	physical := make([]string, 0, len(fields))
	if len(fields) > 0 {
		for _, name := range fields {
			binding, ok := b.Fields[name]
			if !ok {
				return nil, fmt.Errorf("property %q is not bound", name)
			}
			physical = append(physical, binding.Field)
		}
	}
	pf := make([]datasource.Filter, 0, len(filters))
	for _, f := range filters {
		if _, ok := obj.Properties[f.Property]; !ok {
			return nil, fmt.Errorf("property %q does not exist", f.Property)
		}
		binding, ok := b.Fields[f.Property]
		if !ok {
			return nil, fmt.Errorf("property %q is not bound", f.Property)
		}
		pf = append(pf, datasource.Filter{Field: binding.Field, Operator: f.Operator, Value: f.Value})
	}
	result, err := driver.Query(ctx, ds.Config, datasource.QueryRequest{Resource: b.Resource, Fields: physical, Filters: pf, Limit: limit})
	if err != nil {
		return nil, err
	}
	reverse := map[string]string{}
	for logical, binding := range b.Fields {
		reverse[binding.Field] = logical
	}
	for _, row := range result.Rows {
		for key, value := range row {
			if logical, ok := reverse[key]; ok && logical != key {
				delete(row, key)
				row[logical] = value
			}
		}
	}
	return result.Rows, nil
}

type RelatedQuery struct {
	Workspace, Object string
	ID                any
	Link              string
	Fields            []string
	Limit             int
}

func (s *Service) Related(ctx context.Context, q RelatedQuery) (ObjectResult, error) {
	snap, err := s.Snapshot(q.Workspace)
	if err != nil {
		return ObjectResult{}, err
	}
	source, ok := snap.Objects[q.Object]
	if !ok {
		return ObjectResult{}, fmt.Errorf("object %q does not exist", q.Object)
	}
	link, ok := source.Links[q.Link]
	if !ok {
		return ObjectResult{}, fmt.Errorf("link %q does not exist", q.Link)
	}
	instance, err := s.Get(ctx, q.Workspace, q.Object, q.ID)
	if err != nil {
		return ObjectResult{}, err
	}
	value, ok := instance[link.Resolver.SourceField]
	if !ok {
		return ObjectResult{}, fmt.Errorf("link source property %q has no value", link.Resolver.SourceField)
	}
	targetKey := link.Target.Vendor + "." + link.Target.Domain + "." + link.Target.Object
	target, ok := snap.Objects[targetKey]
	if !ok {
		return ObjectResult{}, fmt.Errorf("target object %q unavailable", targetKey)
	}
	values := []any{value}
	if list, ok := value.([]any); ok {
		values = list
	}
	rows, err := s.queryObject(ctx, snap, target, q.Fields, []SemanticFilter{{Property: link.Resolver.TargetField, Operator: datasource.OpIn, Value: values}}, q.Limit)
	return ObjectResult{Object: targetKey, PublicationID: snap.PublicationID, Items: rows}, err
}

type SearchHit struct {
	Kind        string `json:"kind"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Score       int    `json:"score"`
}

func (s *Service) Search(_ context.Context, workspace, query string, limit int) ([]SearchHit, error) {
	snap, err := s.Snapshot(workspace)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, fmt.Errorf("query is required")
	}
	hits := []SearchHit{}
	for key, obj := range snap.Objects {
		score := match(q, obj.Definition.Name, obj.Definition.DisplayName, obj.Definition.Description, strings.Join(obj.Definition.Aliases, " "), strings.Join(obj.Definition.Tags, " "))
		if score > 0 {
			hits = append(hits, SearchHit{Kind: "object", Key: key, Name: obj.Definition.Name, DisplayName: obj.Definition.DisplayName, Description: obj.Definition.Description, Score: score})
		}
		for _, p := range obj.Definition.Properties {
			if ps := match(q, p.Name, p.DisplayName, p.Description, strings.Join(p.Aliases, " ")); ps > 0 {
				hits = append(hits, SearchHit{Kind: "property", Key: key + "." + p.Name, Name: p.Name, DisplayName: p.DisplayName, Description: p.Description, Score: ps})
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].Key < hits[j].Key
		}
		return hits[i].Score > hits[j].Score
	})
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
func match(q string, values ...string) int {
	best := 0
	for i, v := range values {
		v = strings.ToLower(v)
		if v == q {
			return 100 - i
		}
		if strings.Contains(v, q) && 80-i > best {
			best = 80 - i
		}
	}
	return best
}
