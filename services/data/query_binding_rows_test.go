package data

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func rowBindingQuery() c.QuerySpec {
	d := rowSQLDefinitions()
	return c.QuerySpec{From: c.Source{SchemaID: d[0].ID}, Rows: &c.RowQuery{
		Nodes: []c.RowQueryNode{{ID: "n0", SchemaID: d[0].ID, Alias: "设备"}, {ID: "n1", SchemaID: d[1].ID, Alias: "工单"}, {ID: "n2", SchemaID: d[2].ID, Alias: "产线"}},
		Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: d[3].ID, Direction: "out"}, {From: "n1", To: "n2", LinkID: d[4].ID, Direction: "out"}},
	}, Select: []string{"n0." + testFieldCode, "n1." + testFieldPrice, "n2." + testFieldPrice}, Where: &c.Expression{Op: "gt", Field: "n1." + testFieldPrice, Value: raw("1.000")}, OrderBy: []c.Order{{Field: "n2." + testFieldPrice, Direction: "DESC"}}, Page: c.Page{Limit: 1}}
}

func rowBindingConfig(t *testing.T, changeCurrent func(string, int, *c.CurrentDefinition), changeDecision func(c.AuthorizationRequest, int, *c.Decision)) Config {
	t.Helper()
	reads := map[string]int{}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, definition := range rowSQLDefinitions() {
			if r.URL.Path == "/metadata/definitions/"+definition.ID {
				reads[definition.ID]++
				current := c.CurrentDefinition{Definition: definition, ETag: "active", Status: "active"}
				if changeCurrent != nil {
					changeCurrent(definition.ID, reads[definition.ID], &current)
				}
				writeJSON(w, 200, current)
				return
			}
		}
		t.Errorf("logical binding opened a non-definition endpoint: %s", r.URL.Path)
		writeJSON(w, 404, c.Error{Code: "not_found"})
	}))
	t.Cleanup(metadata.Close)
	checks := map[string]int{}
	return Config{MetadataURL: metadata.URL, HTTPClient: metadata.Client(), CursorSecret: strings.Repeat("r", 32), Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, request c.AuthorizationRequest) (c.Decision, error) {
		key := request.ResourceID + request.Operation
		checks[key]++
		decision := c.Decision{Allow: true, State: "current", ReadFields: []string{"*"}}
		if changeDecision != nil {
			changeDecision(request, checks[key], &decision)
		}
		return decision, nil
	})}
}

func TestRowQueryBindingIsLogicalDetachedAndSafe(t *testing.T) {
	q := rowBindingQuery()
	before, _ := json.Marshal(q)
	cfg := rowBindingConfig(t, nil, nil) // Engine and source directory deliberately absent.
	out := decodeQueryBinding(t, callOntologySQL(t, NewHandler(cfg), "POST", "/data/query/bind", q))
	after, _ := json.Marshal(q)
	if string(before) != string(after) || !reflect.DeepEqual(q.Rows, out.Query.Rows) || out.Query.OrderBy[0].Direction != "desc" || len(out.Fields) != 3 || out.Fields[1].Name != "工单.金额" || out.Fields[2].DataType != "int64" {
		t.Fatalf("logical row binding lost scope or mutated input: %+v", out)
	}
	for _, field := range out.Fields {
		if field.Mapping != nil || field.Default != nil {
			t.Fatal("private storage metadata escaped")
		}
	}
}

func TestRowQueryBindingValidatesMappedScopeWithoutOpeningSources(t *testing.T) {
	for _, mode := range []string{"same source", "cross source", "secondary property"} {
		t.Run(mode, func(t *testing.T) {
			sourceID, otherSource := c.NewID(), c.NewID()
			cfg := rowBindingConfig(t, func(id string, _ int, current *c.CurrentDefinition) {
				definition := &current.Definition
				if definition.Schema != nil {
					definition.Schema.Mode = "db_2_schema"
					bindingID := sourceID
					if mode == "cross source" && id == rowSQLLineID {
						bindingID = otherSource
					}
					definition.Schema.StorageBinding = &c.StorageBinding{DataSourceID: bindingID, Table: "private_business", IDColumn: "id", TenantColumn: "tenant"}
					for i := range definition.Schema.Properties {
						definition.Schema.Properties[i].Mapping = &c.PropertyMapping{DataSourceID: bindingID, Column: "private_column"}
					}
					if mode == "secondary property" && id == rowSQLWorkID {
						definition.Schema.Properties[1].Mapping.Table = "private_other"
					}
				} else if definition.Link != nil {
					definition.Link.StorageBinding = &c.LinkStorageBinding{Mode: "junction", DataSourceID: sourceID, Table: "private_edges", SourceColumn: "from_id", TargetColumn: "to_id", TenantColumn: "tenant"}
				}
			}, nil)
			w := callOntologySQL(t, NewHandler(cfg), "POST", "/data/query/bind", rowBindingQuery())
			want := 422
			if mode == "same source" {
				want = 200
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private_") {
				t.Fatalf("logical mapping capability or privacy: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestRowQueryBindingRejectsNamesHiddenFieldsAndStaleScopes(t *testing.T) {
	for _, test := range []struct {
		name     string
		edit     func(*c.QuerySpec)
		current  func(string, int, *c.CurrentDefinition)
		decision func(c.AuthorizationRequest, int, *c.Decision)
		want     int
	}{
		{name: "filter name", want: 422, edit: func(q *c.QuerySpec) { q.Where.Field = "工单.金额" }},
		{name: "projection name", want: 422, edit: func(q *c.QuerySpec) { q.Select[1] = "工单.金额" }},
		{name: "hidden field", want: 403, decision: func(r c.AuthorizationRequest, _ int, d *c.Decision) {
			if r.ResourceID == rowSQLWorkID && r.Operation == "instance.query" {
				d.ReadFields = []string{rowSQLWorkField}
			}
		}},
		{name: "link revoked", want: 403, decision: func(r c.AuthorizationRequest, n int, d *c.Decision) {
			if r.ResourceID == rowSQLLinkB && r.Operation == "link.query" && n >= 2 {
				d.Allow = false
			}
		}},
		{name: "target discovery revoked", want: 403, decision: func(r c.AuthorizationRequest, n int, d *c.Decision) {
			if r.ResourceID == rowSQLLineID && r.Operation == "definition.read" && n >= 2 {
				d.Allow = false
			}
		}},
		{name: "target policy changed", want: 409, decision: func(r c.AuthorizationRequest, n int, d *c.Decision) {
			if r.ResourceID == rowSQLWorkID && r.Operation == "instance.query" && n >= 2 {
				d.State = "changed"
			}
		}},
		{name: "target changed", want: 409, current: func(id string, n int, d *c.CurrentDefinition) {
			if id == rowSQLLineID && n >= 2 {
				d.ETag = "changed"
			}
		}},
		{name: "link changed", want: 409, current: func(id string, n int, d *c.CurrentDefinition) {
			if id == rowSQLLinkA && n >= 2 {
				d.ETag = "changed"
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := rowBindingQuery()
			if test.edit != nil {
				test.edit(&q)
			}
			w := callOntologySQL(t, NewHandler(rowBindingConfig(t, test.current, test.decision)), "POST", "/data/query/bind", q)
			if w.Code != test.want || strings.Contains(w.Body.String(), `"query"`) || strings.Contains(w.Body.String(), `"fields"`) {
				t.Fatalf("stale/unauthorized scope delivered: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestSavedRowTemplateUsesScopedParameterTypesWithoutBusinessDatabase(t *testing.T) {
	cfg := rowBindingConfig(t, nil, nil)
	h := &handler{cfg: cfg, client: cfg.HTTPClient}
	input := c.SavedQueryInput{Name: "关联模板", Source: c.SavedQuerySource{Kind: "visual"}, Query: rowBindingQuery()}
	input.Query.Where = &c.Expression{Op: "gte", Field: "n1." + testFieldPrice, Parameter: "金额"}
	input.Parameters = []c.SavedQueryParameter{declarationForProperty("金额", rowSQLDefinitions()[1].Schema.Properties[1], false)}
	validated, schema, err := h.validateSavedInput(context.Background(), c.Principal{TenantID: "tenant-a", Subject: "reader"}, input)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Query.Where.Parameter != "金额" || len(validated.Query.Where.Value) != 0 {
		t.Fatal("sample persisted in template")
	}
	if _, err = savedParameterFields(&validated.Query, schema, validated.Parameters, map[string]json.RawMessage{"金额": raw("123456789012345.123")}, false); err != nil || string(validated.Query.Where.Value) != `"123456789012345.123"` {
		t.Fatalf("scoped exact parameter failed: %v", err)
	}
	input.Query.Where.Field = "n2." + testFieldPrice // Same field ID, different node and int64 type.
	if _, _, err = h.validateSavedInput(context.Background(), c.Principal{TenantID: "tenant-a", Subject: "reader"}, input); err == nil {
		t.Fatal("cross-node parameter type drift accepted")
	}
}
