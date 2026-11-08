package data

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func requireBudgetError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var failure c.Error
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Code != code || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected safe failure: status=%d code=%q bytes=%d", w.Code, failure.Code, w.Body.Len())
	}
}

func TestDataAdmissionBoundsWaitAndReleasesSlots(t *testing.T) {
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	app := withDataBudgets(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(204)
			return
		}
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		w.WriteHeader(204)
	}))
	go func() {
		defer close(done)
		app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/data/query/context", nil))
	}()
	<-started
	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest("GET", "/data/query/context", nil))
	requireBudgetError(t, w, 429, "data_busy")
	if calls.Load() != 1 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("overload reached work or lacked retry guidance")
	}
	w = httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 204 {
		t.Fatal("health unavailable during overload")
	}
	close(release)
	<-done
	w = httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest("GET", "/data/query/context", nil))
	if w.Code != 204 || calls.Load() != 2 {
		t.Fatal("admission slot not released")
	}
}

func TestDataLimitersShareProcessAdmissionAndOnlySkipTheirOwnNesting(t *testing.T) {
	outer := &dataResourceLimiter{slots: make(chan struct{}, 1)}
	inner := &dataResourceLimiter{slots: make(chan struct{}, 1)}
	// An unrelated limiter must not trust another wrapper's context ownership.
	inner.slots <- struct{}{}
	called := false
	app := outer.protect(inner.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })))
	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest("GET", "/data/query", nil))
	requireBudgetError(t, w, 429, "data_busy")
	if called {
		t.Fatal("unrelated limiter ownership bypassed admission")
	}
	<-inner.slots
	app = outer.protect(outer.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	w = httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest("GET", "/data/query", nil))
	if w.Code != 204 {
		t.Fatal("same limiter admitted twice")
	}
	// Independently constructed default handlers still use one process budget.
	dataServiceLimiter.slots <- struct{}{}
	defer func() { <-dataServiceLimiter.slots }()
	app = ProtectResources(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("full process budget admitted another handler") }))
	w = httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest("GET", "/data/query", nil))
	requireBudgetError(t, w, 429, "data_busy")
}

func TestDataQueueBoundsCountAndCancelsBeforeWork(t *testing.T) {
	limiter := &dataResourceLimiter{slots: make(chan struct{}, 1), pending: make(chan struct{}, 4)}
	limiter.slots <- struct{}{}
	var calls atomic.Int32
	app := limiter.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	results := make(chan *httptest.ResponseRecorder, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 4; i++ {
		request := httptest.NewRequest("POST", "/data/query", nil)
		if i == 0 {
			request = request.WithContext(ctx)
		}
		go func(r *http.Request) { w := httptest.NewRecorder(); app.ServeHTTP(w, r); results <- w }(request)
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	waitForCount := func(want int) {
		t.Helper()
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		for len(limiter.pending) != want {
			select {
			case <-ticker.C:
			case <-deadline.C:
				t.Fatal("queue count not reached")
			}
		}
	}
	waitForCount(4)
	w := httptest.NewRecorder()
	started := time.Now()
	app.ServeHTTP(w, httptest.NewRequest("POST", "/data/query", nil))
	requireBudgetError(t, w, 429, "data_busy")
	if time.Since(started) >= time.Duration(c.MaxDataQueueWaitMS)*time.Millisecond || calls.Load() != 0 {
		t.Fatal("full queue admitted or waited for another request")
	}
	cancel()
	canceled := <-results
	requireBudgetError(t, canceled, 408, "request_canceled")
	waitForCount(3)
	<-limiter.slots
	for i := 0; i < 3; i++ {
		if result := <-results; result.Code != 204 {
			t.Fatal("bounded queued request did not proceed")
		}
	}
	if calls.Load() != 3 || len(limiter.pending) != 0 {
		t.Fatal("canceled queue entry reached work or was not released")
	}
}

func TestDataResponseBudgetNeverPublishesPartialSuccess(t *testing.T) {
	app := withDataBudgets(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"synthetic"`)
		w.WriteHeader(200)
		for i := 0; i < 3; i++ {
			_, _ = w.Write([]byte(strings.Repeat("x", c.MaxDataResponseBytes/2)))
		}
	}))
	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest("GET", "/data/query", nil))
	requireBudgetError(t, w, 422, "response_budget")
	if w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "xxxx") {
		t.Fatal("partial data or success headers escaped")
	}
}

func TestDataDeadlineAndCancellationReturnSafeJSON(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	called := false
	app := withDataBudgets(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/data/query", nil).WithContext(ctx)
	app.ServeHTTP(w, r)
	requireBudgetError(t, w, 504, "request_timeout")
	if called {
		t.Fatal("expired request reached work")
	}
	ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	app = withDataBudgets(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"private": "SYNTHETIC_ONLY"})
		cancel2()
	}))
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/data/query", nil).WithContext(ctx)
	app.ServeHTTP(w, r)
	requireBudgetError(t, w, 408, "request_canceled")
	if strings.Contains(w.Body.String(), "SYNTHETIC_ONLY") {
		t.Fatal("canceled buffered response published")
	}
}

func TestJSONBudgetIncludesEscapingAndPreservesLongText(t *testing.T) {
	type privateEmbedded struct{ Body string }
	value := c.Instance{ID: c.NewID(), SchemaID: c.NewID(), ETag: c.NewID(), Values: map[string]json.RawMessage{"body": raw(strings.Repeat("a", c.MaxLongTextBytes))}}
	w := httptest.NewRecorder()
	writeJSON(w, 200, value)
	if w.Code != 200 || !json.Valid(w.Body.Bytes()) || w.Body.Len() <= c.MaxLongTextBytes {
		t.Fatal("supported 4MiB longtext response rejected")
	}
	w = httptest.NewRecorder()
	writeJSON(w, 201, map[string]string{"body": strings.Repeat("<", 2<<20)})
	requireBudgetError(t, w, 422, "response_budget")
	for _, value := range []any{
		struct{ privateEmbedded }{privateEmbedded{Body: strings.Repeat("<", 30)}},
		map[string]any{"raw": json.RawMessage(`"<>&"`), "utf8": "\u2028\u2029\xff", "bytes": []byte{1, 2, 3}},
		c.OntologySQLResult{QueryResult: c.QueryResult{Fields: []c.Property{{ID: "id", Name: "字段", DataType: "text"}}, Data: []c.Instance{}}},
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		budget := jsonBudget{remaining: len(encoded)}
		if err = budget.add(value); err == nil && budget.remaining > 0 {
			t.Fatal("JSON estimate under-counted encoding")
		}
	}
}

type resourceBudgetRows struct {
	*catalogFixtureRows
	closed bool
}

func (r *resourceBudgetRows) Close() error { r.closed = true; return nil }

func TestRowQueryBudgetStopsAccumulatingBeforeFullPage(t *testing.T) {
	plan, _ := testRowQuery()
	plan.nodes = plan.nodes[:1]
	property := c.Property{ID: "review-body", Name: "body", DataType: "longtext", Nullable: true}
	compiled := rowCompiledQuery{Limit: 100, Fields: []c.Property{property}, ScanFields: []c.Property{property}}
	fixture := &resourceBudgetRows{catalogFixtureRows: &catalogFixtureRows{columns: []string{"id", "body"}}}
	body := []byte(strings.Repeat("a", 256<<10))
	for i := 0; i < 100; i++ {
		fixture.values = append(fixture.values, []driver.Value{c.NewID(), body})
	}
	db := sql.OpenDB(&catalogFixtureConnector{query: func(string) (driver.Rows, error) { return fixture, nil }})
	defer db.Close()
	result, _, err := readRowQuery(context.Background(), db, plan, compiled)
	api, ok := err.(*apiError)
	if !ok || api.code != "response_budget" || len(result.Rows) >= 100 || fixture.position >= 100 || !fixture.closed {
		t.Fatalf("unbounded query: rows=%d scanned=%d closed=%v error=%T", len(result.Rows), fixture.position, fixture.closed, err)
	}
}

func TestMappedQueryRejectsEscapedOversizeBeforeProjection(t *testing.T) {
	property := c.Property{ID: c.NewID(), Name: "body", DataType: "longtext", Nullable: true}
	fixture := &resourceBudgetRows{catalogFixtureRows: &catalogFixtureRows{columns: []string{"id", "body"}, values: [][]driver.Value{{c.NewID(), []byte(strings.Repeat("<", 2<<20))}}}}
	db := sql.OpenDB(&catalogFixtureConnector{query: func(string) (driver.Rows, error) { return fixture, nil }})
	defer db.Close()
	table := &mappedTable{db: db, keyColumn: "id", catalog: c.SourceTable{Columns: []c.SourceColumn{{Name: "id", DataType: "uuid"}}}}
	result, _, err := readMappedProjection(context.Background(), []*mappedTable{table}, mappedCompiledQuery{Limit: 100, Projection: []c.Property{property}, ScanFields: []c.Property{property}}, c.NewID())
	api, ok := err.(*apiError)
	if !ok || api.code != "response_budget" || len(result.Data) != 0 || !fixture.closed {
		t.Fatalf("encoded source row not rejected before projection: rows=%d closed=%v error=%T", len(result.Data), fixture.closed, err)
	}
}

func TestCreateMissingRequiredValueDoesNotDiscloseHiddenMetadata(t *testing.T) {
	visible, hidden := c.NewID(), c.NewID()
	schema := &c.Schema{Properties: []c.Property{{ID: visible, Name: "visible", DataType: "string", Required: true}, {ID: hidden, Name: "SYNTHETIC_PRIVATE_FIELD", DataType: "string", Required: true}}}
	_, err := buildValues(schema, map[string]json.RawMessage{visible: raw("value")}, nil, []string{visible}, true)
	w := httptest.NewRecorder()
	writeError(w, err)
	requireBudgetError(t, w, 422, "missing_field")
	if strings.Contains(w.Body.String(), hidden) || strings.Contains(w.Body.String(), "SYNTHETIC_PRIVATE_FIELD") {
		t.Fatal("missing-field response leaked hidden metadata")
	}
}
