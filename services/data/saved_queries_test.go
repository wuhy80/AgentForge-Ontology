package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestSavedQueryParameterBindingPreservesExactValuesAndRejectsAmbiguity(t *testing.T) {
	property := c.Property{ID: "amount-id", Name: "金额", DataType: "decimal", Precision: 20, Scale: 4, Nullable: true}
	schema := &c.Schema{Properties: []c.Property{property}}
	input := c.SavedQueryInput{Query: c.QuerySpec{Select: []string{property.ID}, Where: &c.Expression{Op: "eq", Field: property.ID, Parameter: "amount"}}, Parameters: []c.SavedQueryParameter{declarationForProperty("amount", property, false)}}
	copy, _ := cloneSavedInput(input)
	if _, err := savedParameterFields(&copy.Query, schema, copy.Parameters, map[string]json.RawMessage{"amount": raw("9007199254740993.0001")}, false); err != nil || string(copy.Query.Where.Value) != `"9007199254740993.0001"` || copy.Query.Where.Parameter != "" {
		t.Fatalf("exact decimal binding failed: %v", err)
	}
	if input.Query.Where.Parameter != "amount" || len(input.Query.Where.Value) != 0 {
		t.Fatal("execution mutated the persisted placeholder")
	}
	for _, name := range []string{"missing", "unused", "number", "null", "mixed", "name", "type", "duplicate", "value-ref", "logical"} {
		t.Run(name, func(t *testing.T) {
			candidate, _ := cloneSavedInput(input)
			values := map[string]json.RawMessage{"amount": raw("1.0000")}
			switch name {
			case "missing":
				delete(values, "amount")
			case "unused":
				values["extra"] = raw("1.0000")
			case "number":
				values["amount"] = json.RawMessage("9007199254740993")
			case "null":
				values["amount"] = json.RawMessage("null")
			case "mixed":
				candidate.Query.Where.Value = raw("1.0000")
			case "name":
				candidate.Query.Where.Field = property.Name
			case "type":
				candidate.Parameters[0].Precision = 19
			case "duplicate":
				candidate.Parameters = append(candidate.Parameters, candidate.Parameters[0])
			case "value-ref":
				candidate.Query.Where.ValueRef = &c.PermissionValueRef{}
			case "logical":
				candidate.Query.Where.Op = "and"
			}
			if _, err := savedParameterFields(&candidate.Query, schema, candidate.Parameters, values, false); err == nil {
				t.Fatal("ambiguous or invalid parameter accepted")
			}
		})
	}
	array := input
	array.Query.Where = &c.Expression{Op: "in", Field: property.ID, Parameter: "amounts"}
	array.Parameters = []c.SavedQueryParameter{declarationForProperty("amounts", property, true)}
	if _, err := savedParameterFields(&array.Query, schema, array.Parameters, map[string]json.RawMessage{"amounts": json.RawMessage(`["9007199254740993.0001",null]`)}, false); err != nil {
		t.Fatal("typed collection including nullable item rejected", err)
	}
}

type savedQueriesFixture struct {
	*mappedIntegrationFixture
	definition c.Definition
	input      c.SavedQueryInput
}

func newSavedQueriesFixture(t *testing.T) *savedQueriesFixture {
	t.Helper()
	f, definition, _ := enumDataFixture(t)
	t.Cleanup(func() {
		for _, table := range []string{"af_data_saved_queries", "af_data_saved_query_owners"} {
			_, _ = f.engine.DB().ExecContext(context.Background(), "DELETE FROM "+quote(table)+" WHERE tenant_id IN (?,?)", f.tenant, f.tenant+"-other")
		}
	})
	status := definition.Schema.Properties[0]
	input := c.SavedQueryInput{Name: "我的状态查询", Source: c.SavedQuerySource{Kind: "visual"}, Query: c.QuerySpec{From: c.Source{SchemaID: definition.ID}, Select: []string{status.ID}, Where: &c.Expression{Op: "eq", Field: status.ID, Parameter: "state"}, Page: c.Page{Limit: 1}}, Parameters: []c.SavedQueryParameter{declarationForProperty("state", status, false)}}
	return &savedQueriesFixture{mappedIntegrationFixture: f, definition: definition, input: input}
}

func (f *savedQueriesFixture) call(method, path, tenant, user, etag string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	encoded, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	ctx := platform.WithPrincipal(f.ctx, c.Principal{TenantID: tenant, Subject: user})
	ctx = platform.WithSessionUser(ctx, c.User{TenantID: tenant, ID: user, Enabled: true})
	r = r.WithContext(ctx)
	if etag != "" {
		r.Header.Set("If-Match", `"`+etag+`"`)
	}
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	return w
}

func (f *savedQueriesFixture) own(method, path, etag string, body any, want int) *httptest.ResponseRecorder {
	f.t.Helper()
	w := f.call(method, path, f.tenant, "saved-owner", etag, body)
	if w.Code != want {
		f.t.Fatalf("%s %s: HTTP%d want%d: %s", method, path, w.Code, want, w.Body)
	}
	return w
}

func TestSavedQueryMySQLCRUDIsolationETagAndCurrentAuthorization(t *testing.T) {
	f := newSavedQueriesFixture(t)
	saved := mutationDecode[c.SavedQuery](t, f.own("POST", "/data/saved-queries", "", f.input, 201))
	path := "/data/saved-queries/" + saved.ID
	if saved.ETag == "" || saved.Query.Where.Parameter != "state" || len(saved.Query.Where.Value) != 0 {
		t.Fatal("saved template lost placeholder or ETag")
	}
	f.own("GET", path, "", nil, 200)
	for _, identity := range [][2]string{{f.tenant, "other-user"}, {f.tenant + "-other", "saved-owner"}} {
		for _, method := range []string{"GET", "DELETE", "PUT"} {
			w := f.call(method, path, identity[0], identity[1], saved.ETag, f.input)
			if w.Code != 404 && !(method == "PUT" && w.Code == 422) {
				t.Fatalf("private template exposed across identity: %s %d %s", method, w.Code, w.Body)
			}
		}
		if w := f.call("POST", path+"/execute", identity[0], identity[1], "", c.SavedQueryExecuteRequest{Parameters: map[string]json.RawMessage{"state": raw("A")}}); w.Code != 404 {
			t.Fatal("execution accepted another owner's query")
		}
	}
	f.own("PUT", path, "", f.input, 428)
	f.own("DELETE", path, "stale", nil, 412)
	f.input.Name = "已改名的查询"
	updated := mutationDecode[c.SavedQuery](t, f.own("PUT", path, saved.ETag, f.input, 200))
	f.own("PUT", path, saved.ETag, f.input, 412)
	if updated.ETag == saved.ETag || updated.CreatedAt != saved.CreatedAt {
		t.Fatal("update ETag or original creation time invalid")
	}
	// Exact runtime values are never stored with the template or a result.
	created := f.request("POST", "/data/schemas/"+f.definition.ID+"/instances", c.WriteRequest{IdempotencyKey: c.NewID(), Values: map[string]json.RawMessage{f.definition.Schema.Properties[0].ID: raw("A")}}, false)
	if created.Code != 201 {
		t.Fatalf("create saved-query record: %d %s", created.Code, created.Body)
	}
	result := mutationDecode[c.QueryResult](t, f.own("POST", path+"/execute", "", c.SavedQueryExecuteRequest{Parameters: map[string]json.RawMessage{"state": raw("A")}}, 200))
	if len(result.Data) != 1 {
		t.Fatal("bound saved query did not execute")
	}
	f.own("POST", path+"/execute", "", c.SavedQueryExecuteRequest{Parameters: map[string]json.RawMessage{"state": raw("可用")}}, 422)
	f.own("POST", path+"/execute", "", map[string]any{"parameters": map[string]string{"state": "A"}, "result": "do-not-persist"}, 400)
	var persisted string
	if err := f.engine.DB().QueryRow("SELECT template_json FROM af_data_saved_queries WHERE tenant_id=? AND user_id=? AND id=?", f.tenant, "saved-owner", saved.ID).Scan(&persisted); err != nil || strings.Contains(persisted, `"value"`) || strings.Contains(persisted, `"A"`) || strings.Contains(persisted, "do-not-persist") {
		t.Fatalf("runtime value entered saved template: %v", err)
	}
	f.decision.Allow = false
	denied := f.own("GET", path, "", nil, 403)
	if strings.Contains(denied.Body.String(), "source") || strings.Contains(denied.Body.String(), f.definition.Schema.Properties[0].ID) {
		t.Fatal("revoked detail exposed old metadata")
	}
	f.own("POST", path+"/execute", "", c.SavedQueryExecuteRequest{Parameters: map[string]json.RawMessage{"state": raw("A")}}, 403)
	page := mutationDecode[c.SavedQueryPage](t, f.own("GET", "/data/saved-queries", "", nil, 200))
	if len(page.Data) != 1 || page.Data[0].Name != f.input.Name {
		t.Fatal("revoked template could not be identified for deletion")
	}
	f.own("DELETE", path, updated.ETag, nil, 204)
	f.own("GET", path, "", nil, 404)
}

func TestSavedQueryMySQLConcurrentQuotaAndSignedPagination(t *testing.T) {
	f := newSavedQueriesFixture(t)
	encoded, _ := json.Marshal(f.input)
	now := time.Now().UTC()
	for i := 0; i < c.MaxSavedQueries-1; i++ {
		name := fmt.Sprintf("已有查询%03d", i)
		if _, err := f.engine.DB().Exec("INSERT INTO af_data_saved_queries (tenant_id,user_id,id,name,name_key,template_json,etag,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)", f.tenant, "saved-owner", c.NewID(), name, nameKey(name), string(encoded), digest(name), now, now); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	responses := make(chan int, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			input := f.input
			input.Name = fmt.Sprintf("并发查询%d", index)
			responses <- f.call("POST", "/data/saved-queries", f.tenant, "saved-owner", "", input).Code
		}(i)
	}
	close(start)
	workers.Wait()
	close(responses)
	statuses := map[int]int{}
	for code := range responses {
		statuses[code]++
	}
	if statuses[201] != 1 || statuses[422] != 1 {
		t.Fatalf("concurrent creates exceeded quota or did not serialize: %v", statuses)
	}
	var count int
	if err := f.engine.DB().QueryRow("SELECT COUNT(*) FROM af_data_saved_queries WHERE tenant_id=? AND user_id=?", f.tenant, "saved-owner").Scan(&count); err != nil || count != c.MaxSavedQueries {
		t.Fatalf("quota not enforced: count=%d err=%v", count, err)
	}
	first := mutationDecode[c.SavedQueryPage](t, f.own("GET", "/data/saved-queries?limit=1", "", nil, 200))
	if len(first.Data) != 1 || !first.HasMore || first.NextCursor == "" {
		t.Fatal("bounded list did not provide continuation")
	}
	f.own("GET", "/data/saved-queries?limit=1&cursor="+first.NextCursor, "", nil, 200)
	if w := f.call("GET", "/data/saved-queries?limit=1&cursor="+first.NextCursor, f.tenant, "another-user", "", nil); w.Code != 422 {
		t.Fatal("signed cursor accepted for another user")
	}
	f.own("GET", "/data/saved-queries?limit=51", "", nil, 422)
	f.own("GET", "/data/saved-queries?limit=1&limit=2", "", nil, 400)
}

func TestSavedQueryMySQLConcurrentUpdatesUseStrongETag(t *testing.T) {
	f := newSavedQueriesFixture(t)
	saved := mutationDecode[c.SavedQuery](t, f.own("POST", "/data/saved-queries", "", f.input, 201))
	start := make(chan struct{})
	responses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func(index int) {
			<-start
			input := f.input
			input.Name = fmt.Sprintf("修改%d", index)
			responses <- f.call("PUT", "/data/saved-queries/"+saved.ID, f.tenant, "saved-owner", saved.ETag, input).Code
		}(i)
	}
	close(start)
	statuses := map[int]int{}
	for i := 0; i < 2; i++ {
		statuses[<-responses]++
	}
	if statuses[200] != 1 || statuses[412] != 1 {
		t.Fatalf("ETag allowed lost update: %v", statuses)
	}
}
