package data

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type queryBindingFixture struct {
	definition     c.Definition
	decision       c.Decision
	enum           *c.CurrentDefinition
	changeCurrent  func(int, *c.CurrentDefinition)
	changeEnum     func(int, *c.CurrentDefinition)
	changeDecision func(int, *c.Decision)
}

func queryBindingConfig(t *testing.T, fixture queryBindingFixture) Config {
	t.Helper()
	d := fixture.definition
	var definitionReads, enumReads, checks atomic.Int32
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer binding-service" || r.Header.Get("X-Tenant-ID") != d.TenantID {
			t.Error("binding metadata lost trusted tenant context")
		}
		current := c.CurrentDefinition{Definition: d, ETag: "initial", Status: "active"}
		switch {
		case r.URL.Path == "/metadata/resolve":
			if r.URL.Query().Get("category") != "生产管理" || r.URL.Query().Get("name") != d.Name || r.URL.Query().Get("kind") != "schema" {
				t.Error("source names were not resolved through the existing endpoint")
			}
			fallthrough
		case r.URL.Path == "/metadata/definitions/"+d.ID:
			n := int(definitionReads.Add(1))
			if fixture.changeCurrent != nil {
				fixture.changeCurrent(n, &current)
			}
		case fixture.enum != nil && r.URL.Path == "/metadata/definitions/"+fixture.enum.Definition.ID:
			current = *fixture.enum
			n := int(enumReads.Add(1))
			if fixture.changeEnum != nil {
				fixture.changeEnum(n, &current)
			}
		default:
			t.Errorf("binding accessed unexpected metadata/source route: %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		writeJSON(w, 200, current)
	}))
	t.Cleanup(metadata.Close)
	// Nil Engine proves binding never reaches business SQL, gates or catalogs.
	return Config{MetadataURL: metadata.URL, HTTPClient: metadata.Client(), ServiceToken: "binding-service", CursorSecret: strings.Repeat("s", 32), Authorizer: authorizerFunc(func(_ context.Context, p c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		if p.TenantID != d.TenantID || p.Subject != "reader" || req.ResourceID != d.ID || req.Operation != "instance.query" || req.Kind != c.KindSchema {
			t.Errorf("binding changed existing query authorization: %+v %+v", p, req)
		}
		decision := fixture.decision
		if fixture.changeDecision != nil {
			fixture.changeDecision(int(checks.Add(1)), &decision)
		}
		return decision, nil
	})}
}

func decodeQueryBinding(t *testing.T, response *httptest.ResponseRecorder) c.QueryBinding {
	t.Helper()
	var out c.QueryBinding
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &out) != nil {
		t.Fatalf("binding HTTP%d: %s", response.Code, response.Body)
	}
	return out
}

func TestQueryBindingBindsChineseNamesAndReturnsOnlySafeOutputFields(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "platform", true: "mapped"}[mapped], func(t *testing.T) {
			d := testDefinition()
			d.Description = "private-definition-description"
			d.Schema.Properties[0].Default = raw("private-default")
			if mapped {
				sourceID := c.NewID()
				d.Schema.Mode = "db_2_schema"
				d.Schema.StorageBinding = &c.StorageBinding{DataSourceID: sourceID, Table: "private_table", IDColumn: "private_id"}
				for i := range d.Schema.Properties {
					d.Schema.Properties[i].Mapping = &c.PropertyMapping{DataSourceID: sourceID, Column: "private_column"}
				}
			}
			dec := allowed()
			// Authority may filter on an unreadable column. That condition must
			// validate, remain private and never enter the user's bound query.
			dec.RowFilter = &c.Expression{Op: "gt", Field: testFieldPrice, Value: raw("1.000")}
			cfg := queryBindingConfig(t, queryBindingFixture{definition: d, decision: dec})
			q := baseQuery()
			q.From = c.Source{Category: "生产管理", Schema: "设备"}
			q.Where = &c.Expression{Op: "and", Args: []c.Expression{{Op: "startsWith", Field: "设备编码", Value: raw("_%'")}, {Op: "eq", Field: "所属部门", Value: raw("研发")}}}
			q.OrderBy = []c.Order{{Field: "所属部门", Direction: "DESC"}}
			before, _ := json.Marshal(q)
			h := &handler{cfg: cfg, client: cfg.HTTPClient}
			bound, err := h.bindQuery(context.Background(), c.Principal{TenantID: d.TenantID, Subject: "reader"}, q)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(q)
			if string(before) != string(after) || cfg.Engine != nil {
				t.Fatal("logical binding mutated the caller or needs a business Engine")
			}
			response := callOntologySQL(t, NewHandler(cfg), "POST", "/data/query/bind", q)
			out := decodeQueryBinding(t, response)
			if !reflect.DeepEqual(bound, out) || out.Query.From != (c.Source{SchemaID: d.ID}) || !reflect.DeepEqual(out.Query.Select, []string{testFieldCode}) || out.Query.Where.Args[0].Field != testFieldCode || out.Query.Where.Args[1].Field != testFieldDept || out.Query.OrderBy[0] != (c.Order{Field: testFieldDept, Direction: "desc"}) {
				t.Fatalf("unstable binding: %+v", out)
			}
			if len(out.Fields) != 1 || out.Fields[0].ID != testFieldCode || out.Fields[0].Default != nil || out.Fields[0].Mapping != nil || string(out.Query.Where.Args[0].Value) != string(raw("_%'")) {
				t.Fatal("binding changed values or leaked non-output fields")
			}
			for _, secret := range []string{"private_", "private-", "采购价", testFieldPrice, "9007199254740993", "rowFilter", "storageBinding", "definition", "SQL"} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatalf("private metadata escaped: %s", secret)
				}
			}
		})
	}
}

func TestQueryBindingAggregateUsesSharedCompilerAndExactTypes(t *testing.T) {
	d := testDefinition()
	d.Schema.Properties[1].Precision = 24
	q := aggregateTestQuery()
	q.From = c.Source{Category: "生产管理", Schema: "设备"}
	q.Where = &c.Expression{Op: "gt", Field: "采购价", Value: raw("9007199254740993.001")}
	q.OrderBy = []c.Order{{Field: "合计", Direction: "desc"}}
	app := NewHandler(queryBindingConfig(t, queryBindingFixture{definition: d, decision: aggregateDecision()}))
	out := decodeQueryBinding(t, callOntologySQL(t, app, "POST", "/data/query/bind", q))
	if out.Query.From.SchemaID != testDefinition().ID || out.Query.Where.Field != testFieldPrice || out.Query.Aggregate.GroupBy[0] != testFieldDept || out.Query.Aggregate.Measures[1].Field != testFieldPrice || out.Query.OrderBy[0].Field != "mSum" || string(out.Query.Where.Value) != `"9007199254740993.001"` {
		t.Fatalf("aggregate binding: %+v", out)
	}
	if len(out.Fields) != 4 || out.Fields[1].DataType != "int64" || out.Fields[2].DataType != "decimal" || out.Fields[2].Precision != 65 || out.Fields[2].Scale != 3 || out.Fields[3].Scale != 7 {
		t.Fatalf("aggregate numeric metadata: %+v", out.Fields)
	}
	for _, field := range out.Fields {
		if !field.ReadOnly || field.Mapping != nil || field.Default != nil {
			t.Fatal("aggregate returned unsafe or writable output metadata")
		}
	}
}

func TestQueryBindingRejectsHiddenFieldsAndUnsupportedCombinations(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
		code string
		edit func(*c.QuerySpec, *c.Definition, *c.Decision)
	}{
		{"empty projection", 422, "budget_exceeded", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Select = []string{} }},
		{"hidden projection", 403, "forbidden_field", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Select = []string{"采购价"} }},
		{"hidden condition", 403, "forbidden_field", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) {
			q.Where = &c.Expression{Op: "eq", Field: "采购价", Value: raw("1.000")}
		}},
		{"hidden sort", 403, "forbidden_field", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) {
			q.OrderBy = []c.Order{{Field: "采购价", Direction: "asc"}}
		}},
		{"hidden aggregate", 403, "forbidden_field", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { *q = aggregateTestQuery() }},
		{"rows", 422, "query_binding_unsupported", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Rows = &c.RowQuery{} }},
		{"via", 422, "query_binding_unsupported", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Via = &c.LinkTraversal{} }},
		{"mixed aggregate", 422, "invalid_aggregate", func(q *c.QuerySpec, _ *c.Definition, d *c.Decision) {
			q.Aggregate = aggregateTestQuery().Aggregate
			*d = aggregateDecision()
		}},
		{"mapped aggregate", 422, "aggregate_unsupported", func(q *c.QuerySpec, d *c.Definition, dec *c.Decision) {
			*q, *dec = aggregateTestQuery(), aggregateDecision()
			d.Schema.Mode = "db_2_schema"
			sourceID := c.NewID()
			d.Schema.StorageBinding = &c.StorageBinding{DataSourceID: sourceID, Table: "mapped"}
			for i := range d.Schema.Properties {
				d.Schema.Properties[i].Mapping = &c.PropertyMapping{DataSourceID: sourceID, Column: "value"}
			}
		}},
		{"parameter", 422, "unbound_parameter", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) {
			q.Where = &c.Expression{Op: "eq", Field: "设备编码", Parameter: "输入"}
		}},
		{"logical parameter", 422, "unbound_parameter", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) {
			q.Where = &c.Expression{Op: "and", Parameter: "输入", Args: []c.Expression{{Op: "isNull", Field: "设备编码"}}}
		}},
		{"unknown operator", 422, "unsupported", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) {
			q.Where = &c.Expression{Op: "containsSql", Field: "设备编码", Value: raw("x")}
		}},
		{"invalid authority condition", 422, "unknown_field", func(_ *c.QuerySpec, _ *c.Definition, d *c.Decision) {
			d.RowFilter = &c.Expression{Op: "eq", Field: "missing", Value: raw("x")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, d, dec := baseQuery(), testDefinition(), allowed()
			tc.edit(&q, &d, &dec)
			app := NewHandler(queryBindingConfig(t, queryBindingFixture{definition: d, decision: dec}))
			response := callOntologySQL(t, app, "POST", "/data/query/bind", q)
			var out c.Error
			if response.Code != tc.want || json.Unmarshal(response.Body.Bytes(), &out) != nil || out.Code != tc.code || strings.Contains(response.Body.String(), `"query"`) || strings.Contains(response.Body.String(), `"fields"`) {
				t.Fatalf("unexpected rejection HTTP%d: %s", response.Code, response.Body)
			}
		})
	}
	app := NewHandler(queryBindingConfig(t, queryBindingFixture{definition: testDefinition(), decision: allowed()}))
	for _, cursor := range []any{"", "runtime-cursor", nil, true, map[string]any{}} {
		request := map[string]any{"from": baseQuery().From, "select": baseQuery().Select, "page": map[string]any{"limit": 10, "cursor": cursor}}
		response := callOntologySQL(t, app, "POST", "/data/query/bind", request)
		if response.Code != 422 || !strings.Contains(response.Body.String(), "query_binding_cursor") {
			t.Fatalf("cursor accepted: HTTP%d %s", response.Code, response.Body)
		}
	}
	for _, request := range []any{
		map[string]any{"from": baseQuery().From, "select": baseQuery().Select, "page": baseQuery().Page, "parameters": map[string]any{"输入": "value"}},
		map[string]any{"query": baseQuery()},
		map[string]any{"from": baseQuery().From, "select": baseQuery().Select, "page": map[string]any{"limit": 10, "offset": 1}},
	} {
		response := callOntologySQL(t, app, "POST", "/data/query/bind", request)
		if response.Code != 400 || !strings.Contains(response.Body.String(), "invalid_json") {
			t.Fatalf("unknown protocol accepted: HTTP%d %s", response.Code, response.Body)
		}
	}
}

func TestQueryBindingRechecksDefinitionEnumAndEntireDecisionBeforeDelivery(t *testing.T) {
	for _, change := range []string{"definition", "disabled", "enum", "permission", "row filter", "revoked"} {
		t.Run(change, func(t *testing.T) {
			fixture := queryBindingFixture{definition: testDefinition(), decision: allowed()}
			want, code := 409, "authorization_changed"
			switch change {
			case "definition", "disabled":
				code = "definition_changed"
				fixture.changeCurrent = func(n int, current *c.CurrentDefinition) {
					if n >= 2 {
						current.ETag = "changed"
						if change == "disabled" {
							current.Status = "disabled"
						}
					}
				}
				if change == "disabled" {
					code = "definition_unavailable"
				}
			case "enum":
				code = "enum_changed"
				enumID := c.NewID()
				fixture.definition.Schema.Properties[0].EnumID = enumID
				fixture.enum = &c.CurrentDefinition{Definition: c.Definition{ID: enumID, TenantID: fixture.definition.TenantID, Kind: c.KindEnum, Name: "编码", Enum: &c.Enum{Values: []c.EnumValue{{Code: "A", Label: "可用"}}}}, ETag: "initial-enum", Status: "active"}
				fixture.changeEnum = func(n int, current *c.CurrentDefinition) {
					if n >= 2 {
						current.ETag = "changed-enum"
					}
				}
			case "permission", "row filter", "revoked":
				fixture.changeDecision = func(n int, decision *c.Decision) {
					if n < 2 {
						return
					}
					switch change {
					case "permission":
						decision.ReadFields = []string{testFieldCode}
					case "row filter":
						decision.RowFilter = &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("new-private-scope")}
					case "revoked":
						decision.Allow = false
					}
				}
				if change == "revoked" {
					want, code = 403, "forbidden"
				}
			}
			app := NewHandler(queryBindingConfig(t, fixture))
			response := callOntologySQL(t, app, "POST", "/data/query/bind", baseQuery())
			var failure c.Error
			if response.Code != want || json.Unmarshal(response.Body.Bytes(), &failure) != nil || failure.Code != code || strings.Contains(response.Body.String(), `"query"`) || strings.Contains(response.Body.String(), `"fields"`) || strings.Contains(response.Body.String(), "new-private-scope") {
				t.Fatalf("stale binding delivered: HTTP%d %s", response.Code, response.Body)
			}
		})
	}
}

func TestQueryBindingUsesExistingSessionAndCSRFBoundary(t *testing.T) {
	app := NewHandler(queryBindingConfig(t, queryBindingFixture{definition: testDefinition(), decision: allowed()}))
	csrf := strings.Repeat("c", 64)
	protected := platform.ProtectSessionRoutes("", false, sourceSessionCheckerFunc(func(context.Context, c.SessionCheck) (c.Session, error) {
		return c.Session{User: c.User{ID: "reader", TenantID: "tenant-a", Enabled: true}, ExpiresAt: time.Now().Add(time.Hour).Unix(), CSRFToken: csrf}, nil
	}), app)
	for _, tc := range []struct {
		cookie, csrf string
		want         int
	}{{"", "", 401}, {strings.Repeat("s", 64), "", 403}, {strings.Repeat("s", 64), strings.Repeat("x", 64), 403}, {strings.Repeat("s", 64), csrf, 200}} {
		encoded, _ := json.Marshal(baseQuery())
		request := httptest.NewRequest("POST", "http://workbench.test/data/query/bind", strings.NewReader(string(encoded)))
		request.Header.Set("Origin", "http://workbench.test")
		if tc.cookie != "" {
			request.AddCookie(&http.Cookie{Name: platform.SessionCookieName, Value: tc.cookie})
		}
		request.Header.Set("X-CSRF-Token", tc.csrf)
		response := httptest.NewRecorder()
		protected.ServeHTTP(response, request)
		if response.Code != tc.want {
			t.Fatalf("session/CSRF HTTP%d want%d: %s", response.Code, tc.want, response.Body)
		}
	}
	response := callOntologySQL(t, app, "GET", "/data/capabilities", nil)
	var capabilities map[string]any
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &capabilities) != nil || capabilities["queryBinding"] != true {
		t.Fatal("query binding capability is missing")
	}
}
