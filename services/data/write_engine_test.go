package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

type mutationFixture struct {
	t       *testing.T
	engine  *storage.Engine
	handler http.Handler
	core    *handler
	current c.CurrentDefinition
	p       c.Principal
	mu      sync.RWMutex
	policy  map[string]c.Decision
}

func newMutationFixture(t *testing.T) *mutationFixture {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set; real mutation transactions not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 12, MaxIdle: 3})
	if err != nil {
		t.Fatal("connect mutation test MySQL failed")
	}
	t.Cleanup(func() { _ = engine.Close() })
	if err = storage.Migrate(ctx, engine.DB(), "data", Migrations); err != nil {
		t.Fatal("migrate mutation test MySQL failed")
	}
	d := testDefinition()
	d.ID, d.TenantID = c.NewID(), c.NewID()
	d.Schema.Indexes = []c.Index{{ID: c.NewID(), Name: "编码唯一", Unique: true, FieldIDs: []string{testFieldCode}}, {ID: c.NewID(), Name: "金额唯一", Unique: true, FieldIDs: []string{testFieldPrice}}}
	f := &mutationFixture{t: t, engine: engine, p: c.Principal{TenantID: d.TenantID, Subject: "mutation-user"}, current: c.CurrentDefinition{Definition: d, ETag: digest(d), Status: "active"}, policy: map[string]c.Decision{}}
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tenant-ID") != d.TenantID {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, f.current)
	}))
	t.Cleanup(meta.Close)
	secret := strings.Repeat("m", 32)
	config := Config{Engine: engine, MetadataURL: meta.URL, ServiceToken: secret, CursorSecret: strings.Repeat("c", 32), Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, request c.AuthorizationRequest) (c.Decision, error) {
		f.mu.RLock()
		defer f.mu.RUnlock()
		if decision, ok := f.policy[request.Operation]; ok {
			return decision, nil
		}
		return c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: "mutation-policy-1"}, nil
	})}
	f.handler, f.core = NewHandler(config), &handler{cfg: config, client: http.DefaultClient}
	prepare := c.PrepareRequest{Definition: d, Digest: f.current.ETag, OperationID: c.NewID()}
	r := httptest.NewRequest("POST", "/internal/publications/prepare", bytes.NewReader(mustMutationJSON(t, prepare)))
	r.Header.Set("Authorization", "Bearer "+secret)
	r.Header.Set("X-Tenant-ID", d.TenantID)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("mutation schema prepare failed")
	}
	activate := c.ActivateRequest{DefinitionID: d.ID, Digest: f.current.ETag, OperationID: prepare.OperationID}
	r = httptest.NewRequest("POST", "/internal/publications/activate", bytes.NewReader(mustMutationJSON(t, activate)))
	r.Header.Set("Authorization", "Bearer "+secret)
	r.Header.Set("X-Tenant-ID", d.TenantID)
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("mutation schema activation failed")
	}
	return f
}

func mustMutationJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal("encode mutation fixture failed")
	}
	return body
}

func (f *mutationFixture) call(method, suffix string, body any, etag string, status int) *httptest.ResponseRecorder {
	f.t.Helper()
	var encoded []byte
	if body != nil {
		encoded = mustMutationJSON(f.t, body)
	}
	r := httptest.NewRequest(method, "/data/schemas/"+f.current.Definition.ID+suffix, bytes.NewReader(encoded))
	r = r.WithContext(platform.WithPrincipal(r.Context(), f.p))
	if etag != "" {
		r.Header.Set("If-Match", `"`+etag+`"`)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != status {
		var failure c.Error
		_ = json.Unmarshal(w.Body.Bytes(), &failure)
		f.t.Fatalf("mutation %s %s: HTTP %d want %d, code=%s", method, suffix, w.Code, status, failure.Code)
	}
	return w
}

func mutationValues(code string) map[string]json.RawMessage {
	return map[string]json.RawMessage{testFieldCode: raw(code), testFieldDept: raw("D1")}
}

func mutationDecode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("invalid mutation response")
	}
	return out
}

func (f *mutationFixture) create(code string) c.Instance {
	f.t.Helper()
	return mutationDecode[c.Instance](f.t, f.call("POST", "/instances", c.WriteRequest{Values: mutationValues(code), IdempotencyKey: c.NewID()}, "", 201))
}

func (f *mutationFixture) counts() [3]int {
	f.t.Helper()
	var counts [3]int
	queries := []string{"SELECT COUNT(*) FROM " + quote(tableName(f.current.Definition.ID)) + " WHERE tenant_id=?", "SELECT COUNT(*) FROM af_data_outbox WHERE tenant_id=?", "SELECT COUNT(*) FROM af_data_write_receipts WHERE tenant_id=?"}
	for i, query := range queries {
		if err := f.engine.DB().QueryRow(query, f.p.TenantID).Scan(&counts[i]); err != nil {
			f.t.Fatal("read mutation counts failed")
		}
	}
	return counts
}

func TestMutationStrongETag(t *testing.T) {
	for _, value := range []string{"", "*", "abc", `W/"abc"`, `"a", "b"`, `""`, "\"a\nb\"", `"a\b"`} {
		if _, err := mutationETag(value); err == nil {
			t.Fatal("invalid condition accepted")
		}
	}
	for _, value := range []string{`"` + c.NewID() + `"`, `"` + strings.Repeat("a", 64) + `"`} {
		if _, err := mutationETag(value); err != nil {
			t.Fatal("valid strong condition rejected")
		}
	}
}

func TestMySQLMutationEightOperations(t *testing.T) {
	f := newMutationFixture(t)
	capabilities := mutationDecode[c.WriteCapabilities](t, f.call("GET", "/write-capabilities", nil, "", 200))
	if len(capabilities.Operations) != 8 || len(capabilities.UniqueKeys) != 2 || !capabilities.OverwriteAllowed {
		t.Fatal("eight authorized write capabilities missing")
	}
	for _, operation := range capabilities.Operations {
		if !operation.Allowed {
			t.Fatal("ready operation unavailable")
		}
	}
	base := f.create("BASE")
	updated := mutationDecode[c.Instance](t, f.call("PATCH", "/instances/"+base.ID, c.WriteRequest{Values: map[string]json.RawMessage{testFieldDept: raw("D2")}}, base.ETag, 200))
	if updated.ID != base.ID || updated.ETag == base.ETag {
		t.Fatal("single update identity or ETag incorrect")
	}
	f.call("DELETE", "/instances/"+base.ID, nil, updated.ETag, 204)
	upsert := c.UpsertRequest{Match: c.WriteMatch{ID: c.NewID()}, Values: mutationValues("UPSERT"), IdempotencyKey: c.NewID()}
	first := mutationDecode[c.WriteResult](t, f.call("POST", "/instances/upsert", upsert, "", 201))
	replay := mutationDecode[c.WriteResult](t, f.call("POST", "/instances/upsert", upsert, "", 200))
	if first.Outcome != "created" || replay.ID != first.ID || replay.ETag != first.ETag {
		t.Fatal("upsert idempotent create changed")
	}
	upsert.IdempotencyKey = c.NewID()
	f.call("POST", "/instances/upsert", upsert, "", 428)
	upsert.Values[testFieldDept] = raw("D2")
	changed := mutationDecode[c.WriteResult](t, f.call("POST", "/instances/upsert", upsert, first.ETag, 200))
	if changed.Outcome != "updated" || changed.ID != first.ID {
		t.Fatal("upsert conditional update incorrect")
	}
	upsert.IdempotencyKey = c.NewID()
	f.call("POST", "/instances/upsert", upsert, first.ETag, 412)
	upsert.ConflictPolicy, upsert.IdempotencyKey = "overwrite", c.NewID()
	f.call("POST", "/instances/upsert", upsert, "", 200)
	bulk := c.BulkWriteRequest{IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{{ClientKey: "one", Values: mutationValues("BULK1")}, {ClientKey: "two", Values: mutationValues("BULK2")}}}
	created := mutationDecode[c.BulkWriteResult](t, f.call("POST", "/instances/bulk-create", bulk, "", 200))
	if !created.Committed || !created.Atomic || len(created.Results) != 2 || created.Results[0].ClientKey != "one" {
		t.Fatal("bulk create receipt invalid")
	}
	repeated := mutationDecode[c.BulkWriteResult](t, f.call("POST", "/instances/bulk-create", bulk, "", 200))
	if !repeated.Replayed || repeated.Results[0].ID != created.Results[0].ID {
		t.Fatal("bulk create replay changed")
	}
	bulk = c.BulkWriteRequest{IdempotencyKey: c.NewID()}
	for _, item := range created.Results {
		bulk.Items = append(bulk.Items, c.BulkWriteItem{ClientKey: item.ClientKey, ID: item.ID, IfMatch: `"` + item.ETag + `"`, Values: map[string]json.RawMessage{testFieldDept: raw("D3")}})
	}
	changedBulk := mutationDecode[c.BulkWriteResult](t, f.call("POST", "/instances/bulk-update", bulk, "", 200))
	for _, item := range changedBulk.Results {
		if string(item.Values[testFieldDept]) != `"D3"` || item.Outcome != "updated" {
			t.Fatal("bulk update values invalid")
		}
	}
	for i, item := range changedBulk.Results {
		bulk.Items[i] = c.BulkWriteItem{ClientKey: item.ClientKey, ID: item.ID, IfMatch: `"` + item.ETag + `"`}
	}
	bulk.IdempotencyKey = c.NewID()
	deleted := mutationDecode[c.BulkWriteResult](t, f.call("POST", "/instances/bulk-delete", bulk, "", 200))
	if deleted.Results[0].Outcome != "deleted" || len(deleted.Results[0].Values) != 0 {
		t.Fatal("bulk deletion leaked historical content")
	}
	f.call("POST", "/instances/bulk-delete", bulk, "", 200)
	bulk = c.BulkWriteRequest{ConflictPolicy: "overwrite", IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{{ClientKey: "old", Match: &c.WriteMatch{ID: first.ID}, Values: map[string]json.RawMessage{testFieldDept: raw("D4")}}, {ClientKey: "new", Match: &c.WriteMatch{ID: c.NewID()}, Values: mutationValues("BULKUPSERT")}}}
	merged := mutationDecode[c.BulkWriteResult](t, f.call("POST", "/instances/bulk-upsert", bulk, "", 200))
	if merged.Results[0].Outcome != "updated" || merged.Results[1].Outcome != "created" {
		t.Fatal("bulk upsert branches incorrect")
	}
}

func TestMySQLMutationAtomicRollbackAndAuthorization(t *testing.T) {
	f := newMutationFixture(t)
	one, two := f.create("ONE"), f.create("TWO")
	before := f.counts()
	bulk := c.BulkWriteRequest{IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{{ClientKey: "new", Values: mutationValues("NEW")}, {ClientKey: "conflict", Values: mutationValues("ONE")}}}
	f.call("POST", "/instances/bulk-create", bulk, "", 409)
	if f.counts() != before {
		t.Fatal("unique failure left data/events/receipt")
	}
	bulk = c.BulkWriteRequest{IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{{ClientKey: "one", ID: one.ID, IfMatch: `"` + one.ETag + `"`, Values: map[string]json.RawMessage{testFieldDept: raw("D2")}}, {ClientKey: "two", ID: two.ID, IfMatch: `"` + c.NewID() + `"`, Values: map[string]json.RawMessage{testFieldDept: raw("D2")}}}}
	f.call("POST", "/instances/bulk-update", bulk, "", 412)
	if f.counts() != before {
		t.Fatal("stale condition left data/events/receipt")
	}
	actual := mutationDecode[c.Instance](t, f.call("GET", "/instances/"+one.ID, nil, "", 200))
	if actual.ETag != one.ETag || string(actual.Values[testFieldDept]) != `"D1"` {
		t.Fatal("failed atomic update modified a preceding row")
	}
	falseValue := false
	bulk.Atomic = &falseValue
	f.call("POST", "/instances/bulk-update", bulk, "", 422)
	bulk.Atomic = nil
	bulk.Items[1] = bulk.Items[0]
	bulk.Items[1].ClientKey = "duplicate-target"
	f.call("POST", "/instances/bulk-update", bulk, "", 422)
	upsert := c.UpsertRequest{Match: c.WriteMatch{ID: one.ID}, Values: map[string]json.RawMessage{testFieldDept: raw("D2")}, ConflictPolicy: "overwrite", IdempotencyKey: c.NewID()}
	deny := c.Decision{Allow: false, State: "mutation-policy-1"}
	f.policy["instance.upsert.overwrite"] = deny
	f.call("POST", "/instances/upsert", upsert, "", 403)
	gate := c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{testFieldCode}, State: "mutation-policy-1"}
	f.policy["instance.upsert.overwrite"] = gate
	f.call("POST", "/instances/upsert", upsert, "", 403)
	gate.WriteFields = []string{"*"}
	gate.NewRowFilter = &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("D1")}
	f.policy["instance.upsert.overwrite"] = gate
	f.call("POST", "/instances/upsert", upsert, "", 403)
	delete(f.policy, "instance.upsert.overwrite")
	gate.NewRowFilter, gate.RowFilter = nil, &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("HIDDEN")}
	f.policy["instance.upsert"] = gate
	f.call("POST", "/instances/upsert", upsert, "", 409)
	if f.counts() != before {
		t.Fatal("upsert authorization failure changed persisted effects")
	}
}

func TestMySQLMutationUniqueMatchAndConcurrentUpsert(t *testing.T) {
	f := newMutationFixture(t)
	first := f.create("KEY")
	request := c.UpsertRequest{Match: c.WriteMatch{IndexID: f.current.Definition.Schema.Indexes[0].ID, Values: map[string]json.RawMessage{testFieldCode: raw("KEY")}}, Values: map[string]json.RawMessage{testFieldDept: raw("D2")}, ConflictPolicy: "overwrite", IdempotencyKey: c.NewID()}
	result := mutationDecode[c.WriteResult](t, f.call("POST", "/instances/upsert", request, "", 200))
	if result.ID != first.ID {
		t.Fatal("unique match chose wrong instance")
	}
	decimal := c.UpsertRequest{Match: c.WriteMatch{IndexID: f.current.Definition.Schema.Indexes[1].ID, Values: map[string]json.RawMessage{testFieldPrice: raw("1.0")}}, Values: map[string]json.RawMessage{testFieldCode: raw("DECIMAL"), testFieldDept: raw("D1"), testFieldPrice: raw("1.000")}, ConflictPolicy: "overwrite", IdempotencyKey: c.NewID()}
	created := mutationDecode[c.WriteResult](t, f.call("POST", "/instances/upsert", decimal, "", 201))
	decimal.IdempotencyKey = c.NewID()
	decimal.Values[testFieldDept] = raw("D2")
	changed := mutationDecode[c.WriteResult](t, f.call("POST", "/instances/upsert", decimal, "", 200))
	if changed.ID != created.ID || string(changed.Values[testFieldPrice]) != `"1.000"` {
		t.Fatal("equivalent decimal key was changed or matched incorrectly")
	}
	decimal.Values[testFieldPrice] = raw("2.000")
	decimal.IdempotencyKey = c.NewID()
	f.call("POST", "/instances/upsert", decimal, "", 422)
	// Different unique keys resolving the same row must not be treated as two
	// independent updates within one transaction.
	bulk := c.BulkWriteRequest{ConflictPolicy: "overwrite", IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{{ClientKey: "id", Match: &c.WriteMatch{ID: first.ID}, Values: map[string]json.RawMessage{testFieldDept: raw("D3")}}, {ClientKey: "key", Match: &request.Match, Values: map[string]json.RawMessage{testFieldDept: raw("D4")}}}}
	before := f.counts()
	f.call("POST", "/instances/bulk-upsert", bulk, "", 422)
	if f.counts() != before {
		t.Fatal("duplicate resolved target left committed effects")
	}
	const concurrency = 8
	var wg sync.WaitGroup
	errors := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			decisions, err := f.core.mutationDecisions(ctx, f.p, f.current.Definition.ID, "upsert", "overwrite")
			if err != nil {
				errors <- err
				return
			}
			match, err := normalizeMutationMatch(f.current, c.WriteMatch{IndexID: f.current.Definition.Schema.Indexes[0].ID, Values: map[string]json.RawMessage{testFieldCode: raw("CONCURRENT")}}, decisions)
			if err != nil {
				errors <- err
				return
			}
			_, err = f.core.executeMutations(ctx, f.p, f.current, mutationRequest{Operation: "upsert", ReceiptOp: "upsert", IdempotencyKey: fmt.Sprintf("concurrent-%d", i), Hash: digest(i), ConflictPolicy: "overwrite", Commands: []mutationCommand{{Match: match, Values: map[string]json.RawMessage{testFieldDept: raw("D1")}}}}, decisions)
			errors <- err
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal("concurrent upsert transaction failed")
		}
	}
	var count int
	if err := f.engine.DB().QueryRow("SELECT COUNT(*) FROM "+quote(tableName(f.current.Definition.ID))+" WHERE tenant_id=? AND "+quote(columnName(testFieldCode))+"=?", f.p.TenantID, "CONCURRENT").Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent upsert created more than one matching instance")
	}
}

func TestMySQLMutationReplayHonorsCurrentNewRowPolicy(t *testing.T) {
	f := newMutationFixture(t)
	request := c.UpsertRequest{Match: c.WriteMatch{ID: c.NewID()}, Values: mutationValues("REPLAY-ROW"), ConflictPolicy: "overwrite", IdempotencyKey: c.NewID()}
	created := mutationDecode[c.WriteResult](t, f.call("POST", "/instances/upsert", request, "", 201))
	f.call("PATCH", "/instances/"+created.ID, c.WriteRequest{Values: map[string]json.RawMessage{testFieldDept: raw("D2")}}, created.ETag, 200)
	before := f.counts()
	for _, department := range []string{"D1", "D2"} {
		// D1 allows the historical outcome but excludes the current row; D2
		// allows the current row but excludes the historical outcome.
		f.policy["instance.upsert"] = c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: "mutation-policy-1", NewRowFilter: &c.Expression{Op: "eq", Field: testFieldDept, Value: raw(department)}}
		f.call("POST", "/instances/upsert", request, "", 403)
		if f.counts() != before {
			t.Fatal("refused replay changed persisted effects")
		}
	}
}

func TestMySQLMutationCapabilitiesSeparateOverwriteScope(t *testing.T) {
	f := newMutationFixture(t)
	one := f.create("SCOPE")
	f.policy["instance.upsert.overwrite"] = c.Decision{Allow: true, ReadFields: []string{testFieldDept}, WriteFields: []string{testFieldDept}, State: "mutation-policy-1", RowFilter: &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("D1")}}
	capabilities := mutationDecode[c.WriteCapabilities](t, f.call("GET", "/write-capabilities", nil, "", 200))
	if !capabilities.OverwriteAllowed || len(capabilities.OverwriteWriteFields) != 1 || capabilities.OverwriteWriteFields[0] != testFieldDept || len(capabilities.OverwriteUniqueKeyIDs) != 0 || len(capabilities.UniqueKeys) != 2 {
		t.Fatal("optional overwrite scope expanded or narrowed ordinary check capabilities")
	}
	for _, operation := range capabilities.Operations {
		if operation.Name == "upsert" && len(operation.WriteFields) != 3 {
			t.Fatal("check mode lost its authorized fields due to overwrite restrictions")
		}
	}
	request := c.UpsertRequest{Match: c.WriteMatch{IndexID: f.current.Definition.Schema.Indexes[0].ID, Values: map[string]json.RawMessage{testFieldCode: raw("SCOPE")}}, Values: map[string]json.RawMessage{testFieldPrice: raw("1.000")}, IdempotencyKey: c.NewID()}
	f.call("POST", "/instances/upsert", request, one.ETag, 200)
}
