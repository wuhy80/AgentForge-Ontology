package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type transactionFixture struct {
	*mutationFixture
	second c.CurrentDefinition
	defs   map[string]c.CurrentDefinition
}

func newTransactionFixture(t *testing.T) *transactionFixture {
	t.Helper()
	f := &transactionFixture{mutationFixture: newMutationFixture(t)}
	d := testDefinition()
	d.ID, d.TenantID, d.Name = c.NewID(), f.p.TenantID, "事务次表"
	d.Schema.Indexes = []c.Index{{ID: c.NewID(), Name: "编码唯一", Unique: true, FieldIDs: []string{testFieldCode}}}
	f.second = c.CurrentDefinition{Definition: d, ETag: digest(d), Status: "active"}
	f.defs = map[string]c.CurrentDefinition{f.current.Definition.ID: f.current, d.ID: f.second}
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tenant-ID") != f.p.TenantID {
			w.WriteHeader(404)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/metadata/definitions/")
		current, ok := f.defs[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, current)
	}))
	t.Cleanup(meta.Close)
	f.core.cfg.MetadataURL = meta.URL
	f.handler = NewHandler(f.core.cfg)
	prepare := c.PrepareRequest{Definition: d, Digest: f.second.ETag, OperationID: c.NewID()}
	for _, step := range []struct {
		path string
		body any
	}{{"prepare", prepare}, {"activate", c.ActivateRequest{DefinitionID: d.ID, Digest: f.second.ETag, OperationID: prepare.OperationID}}} {
		r := httptest.NewRequest("POST", "/internal/publications/"+step.path, bytes.NewReader(mustMutationJSON(t, step.body)))
		r.Header.Set("Authorization", "Bearer "+f.core.cfg.ServiceToken)
		r.Header.Set("X-Tenant-ID", f.p.TenantID)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("transaction schema %s failed: %d", step.path, w.Code)
		}
	}
	return f
}

func (f *transactionFixture) transaction(body c.WriteTransactionRequest, lookup bool, expected int) c.WriteTransactionResult {
	f.t.Helper()
	path := "/data/transactions"
	if lookup {
		path += "/status"
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(mustMutationJSON(f.t, body)))
	r = r.WithContext(platform.WithPrincipal(r.Context(), f.p))
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != expected {
		var failure c.Error
		_ = json.Unmarshal(w.Body.Bytes(), &failure)
		f.t.Fatalf("transaction HTTP %d want %d, code=%s", w.Code, expected, failure.Code)
	}
	if expected != 200 {
		return c.WriteTransactionResult{}
	}
	return mutationDecode[c.WriteTransactionResult](f.t, w)
}

func transactionCreate(schema, client, code string) c.TransactionOperation {
	return c.TransactionOperation{SchemaID: schema, Operation: "create", ClientKey: client, Values: mutationValues(code)}
}

func (f *transactionFixture) transactionCounts() [4]int {
	f.t.Helper()
	var counts [4]int
	queries := []string{"SELECT COUNT(*) FROM " + quote(tableName(f.current.Definition.ID)) + " WHERE tenant_id=?", "SELECT COUNT(*) FROM " + quote(tableName(f.second.Definition.ID)) + " WHERE tenant_id=?", "SELECT COUNT(*) FROM af_data_outbox WHERE tenant_id=?", "SELECT COUNT(*) FROM af_data_transaction_receipts WHERE tenant_id=?"}
	for i, query := range queries {
		if err := f.engine.DB().QueryRow(query, f.p.TenantID).Scan(&counts[i]); err != nil {
			f.t.Fatal("read transaction counts failed")
		}
	}
	return counts
}

func TestMySQLWriteTransactionMixedSchemasAndRecovery(t *testing.T) {
	f := newTransactionFixture(t)
	base := f.create("BASE")
	seed := f.transaction(c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(f.second.Definition.ID, "seed", "DELETE")}}, false, 200).Results[0]
	body := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{
		transactionCreate(f.current.Definition.ID, "created", "CREATED"),
		{SchemaID: f.current.Definition.ID, Operation: "update", ClientKey: "updated", ID: base.ID, IfMatch: `"` + base.ETag + `"`, Values: map[string]json.RawMessage{testFieldDept: raw("D2")}},
		{SchemaID: f.second.Definition.ID, Operation: "delete", ClientKey: "deleted", ID: seed.ID, IfMatch: `"` + seed.ETag + `"`},
		{SchemaID: f.second.Definition.ID, Operation: "upsert", ClientKey: "upserted", Match: &c.WriteMatch{IndexID: f.second.Definition.Schema.Indexes[0].ID, Values: map[string]json.RawMessage{testFieldCode: raw("UPSERTED")}}, Values: map[string]json.RawMessage{testFieldDept: raw("D1")}},
	}}
	before := f.transactionCounts()
	result := f.transaction(body, false, 200)
	if !result.Atomic || !result.Committed || result.Status != "committed" || result.Replayed || len(result.Results) != 4 {
		t.Fatal("mixed transaction did not commit atomically")
	}
	for i, outcome := range []string{"created", "updated", "deleted", "created"} {
		if result.Results[i].ClientKey != body.Operations[i].ClientKey || result.Results[i].SchemaID != body.Operations[i].SchemaID || result.Results[i].Outcome != outcome {
			t.Fatal("transaction result order or identity changed")
		}
	}
	if result.Results[2].ETag != "" || len(result.Results[2].Values) != 0 {
		t.Fatal("deleted transaction item leaked content")
	}
	after := f.transactionCounts()
	if after != [4]int{before[0] + 1, before[1], before[2] + 4, before[3] + 1} {
		t.Fatal("mixed transaction data, outbox or receipt count incorrect")
	}
	for _, lookup := range []bool{true, false} {
		replayed := f.transaction(body, lookup, 200)
		if !replayed.Replayed || !replayed.Committed || replayed.Status != "committed" || string(mustMutationJSON(t, replayed.Results)) != string(mustMutationJSON(t, result.Results)) || f.transactionCounts() != after {
			t.Fatal("transaction recovery performed writes or changed the receipt")
		}
	}
	changed := body
	changed.Operations = append([]c.TransactionOperation(nil), body.Operations...)
	changed.Operations[0].ClientKey = "different-intent"
	f.transaction(changed, true, 409)
	f.transaction(changed, false, 409)
	missing := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(f.current.Definition.ID, "unseen", "UNSEEN")}}
	unseen := f.transaction(missing, true, 200)
	if unseen.Status != "not_found" || unseen.Committed || unseen.Replayed || f.transactionCounts() != after {
		t.Fatal("status lookup created a receipt or performed a write")
	}
	other := f.p
	other.Subject = "another-transaction-user"
	isolated, err := f.core.executeWriteTransaction(context.Background(), other, body, true)
	if err != nil || isolated.Status != "not_found" {
		t.Fatal("transaction receipt leaked across subjects")
	}
}

func TestMySQLWriteTransactionDurableFailureAndGuard(t *testing.T) {
	f := newTransactionFixture(t)
	low, high := f.current.Definition.ID, f.second.Definition.ID
	if low > high {
		low, high = high, low
	}
	missingID := c.NewID()
	body := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{
		transactionCreate(low, "must-rollback", "ROLLBACK"),
		{SchemaID: high, Operation: "update", ClientKey: "absent", ID: missingID, IfMatch: `"old"`, Values: map[string]json.RawMessage{testFieldDept: raw("D2")}},
	}}
	before := f.transactionCounts()
	failed := f.transaction(body, false, 200)
	if failed.Status != "failed" || failed.Committed || failed.Error == nil || failed.Error.Code != "not_found" || len(failed.Results) != 0 {
		t.Fatal("business failure did not return a safe terminal receipt")
	}
	after := f.transactionCounts()
	if after != [4]int{before[0], before[1], before[2], before[3] + 1} {
		t.Fatal("failed transaction retained business data or outbox")
	}
	for _, lookup := range []bool{true, false} {
		replayed := f.transaction(body, lookup, 200)
		if !replayed.Replayed || replayed.Status != "failed" || replayed.Error == nil || replayed.Error.Code != "not_found" || f.transactionCounts() != after {
			t.Fatal("failed transaction recovery retried business writes")
		}
	}
	guarded := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(low, "guarded", "GUARDED")}}
	calls := 0
	result, err := f.core.executeGuardedWriteTransaction(context.Background(), f.p, guarded, false, func(context.Context) error {
		calls++
		if calls > 1 {
			return problem(401, "session_revoked", "会话已撤销")
		}
		return nil
	})
	if err != nil || calls != 2 || result.Status != "failed" || result.Error == nil || result.Error.Code != "session_revoked" {
		t.Fatal("precommit guard did not roll back and persist failure")
	}
	guardCounts := f.transactionCounts()
	if guardCounts != [4]int{after[0], after[1], after[2], after[3] + 1} {
		t.Fatal("guard failure leaked data or lost terminal receipt")
	}
	guarded.IdempotencyKey = c.NewID()
	calls = 0
	_, err = f.core.executeGuardedWriteTransaction(context.Background(), f.p, guarded, false, func(context.Context) error {
		calls++
		if calls > 1 {
			return errors.New("authority transport failure")
		}
		return nil
	})
	if err == nil || f.transactionCounts() != guardCounts {
		t.Fatal("unknown authority failure was misreported as a durable rollback")
	}
	base := f.create("EXISTING")
	conflict := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(f.current.Definition.ID, "unique-conflict", "EXISTING")}}
	failed = f.transaction(conflict, false, 200)
	if failed.Status != "failed" || failed.Error == nil || failed.Error.Code != "constraint_conflict" || strings.Contains(failed.Error.Message, base.ID) {
		t.Fatal("real SQL constraint failure was not recorded safely")
	}
}

func TestMySQLWriteTransactionConcurrentReceiptAndSchemaIdentity(t *testing.T) {
	f := newTransactionFixture(t)
	id := c.NewID()
	body := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{
		{SchemaID: f.current.Definition.ID, Operation: "upsert", ClientKey: "first-schema", Match: &c.WriteMatch{ID: id}, Values: mutationValues("SHARED_ID")},
		{SchemaID: f.second.Definition.ID, Operation: "upsert", ClientKey: "second-schema", Match: &c.WriteMatch{ID: id}, Values: mutationValues("SHARED_ID")},
	}}
	const count = 8
	var group sync.WaitGroup
	results := make(chan c.WriteTransactionResult, count)
	failures := make(chan error, count)
	start := make(chan struct{})
	for i := 0; i < count; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := f.core.executeWriteTransaction(context.Background(), f.p, body, false)
			if err != nil {
				failures <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent transaction failed: %v", err)
	}
	fresh := 0
	for result := range results {
		if !result.Committed || result.Status != "committed" || len(result.Results) != 2 || result.Results[0].ID != id || result.Results[1].ID != id {
			t.Fatal("cross-schema identities were deduplicated together")
		}
		if !result.Replayed {
			fresh++
		}
	}
	if fresh != 1 || f.transactionCounts() != [4]int{1, 1, 2, 1} {
		t.Fatal("concurrent same-key transaction committed more than once")
	}
}

func TestMySQLWriteTransactionReplayPermissionsAndDuplicateTargets(t *testing.T) {
	f := newTransactionFixture(t)
	body := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(f.current.Definition.ID, "private", "PRIVATE")}}
	created := f.transaction(body, false, 200)
	f.mu.Lock()
	f.policy["instance.create"] = c.Decision{Allow: true, ReadFields: []string{testFieldCode}, WriteFields: []string{"*"}, State: "mutation-policy-1"}
	f.mu.Unlock()
	filtered := f.transaction(body, true, 200)
	if !filtered.Replayed || len(filtered.Results[0].Values) != 1 || string(filtered.Results[0].Values[testFieldCode]) != `"PRIVATE"` {
		t.Fatal("transaction status did not apply current field projection")
	}
	changed := f.call("PATCH", "/instances/"+created.Results[0].ID, c.WriteRequest{Values: map[string]json.RawMessage{testFieldDept: raw("D2")}}, created.Results[0].ETag, 200)
	actual := mutationDecode[c.Instance](t, changed)
	f.mu.Lock()
	decision := f.policy["instance.create"]
	decision.NewRowFilter = &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("D1")}
	f.policy["instance.create"] = decision
	f.mu.Unlock()
	f.transaction(body, true, 403)
	f.mu.Lock()
	delete(f.policy, "instance.create")
	f.mu.Unlock()
	duplicate := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{
		{SchemaID: f.current.Definition.ID, Operation: "update", ClientKey: "id", ID: actual.ID, IfMatch: `"` + actual.ETag + `"`, Values: map[string]json.RawMessage{testFieldDept: raw("D3")}},
		{SchemaID: f.current.Definition.ID, Operation: "upsert", ClientKey: "key", Match: &c.WriteMatch{IndexID: f.current.Definition.Schema.Indexes[0].ID, Values: map[string]json.RawMessage{testFieldCode: raw("PRIVATE")}}, ConflictPolicy: "overwrite", Values: map[string]json.RawMessage{testFieldDept: raw("D4")}},
	}}
	before := f.transactionCounts()
	failed := f.transaction(duplicate, false, 200)
	if failed.Status != "failed" || failed.Error == nil || failed.Error.Code != "duplicate_target" || f.transactionCounts() != [4]int{before[0], before[1], before[2], before[3] + 1} {
		t.Fatal("mixed operations changed one actual target twice")
	}
	current := f.defs[f.second.Definition.ID]
	current.Definition.Schema.StorageBinding = &c.StorageBinding{}
	f.defs[f.second.Definition.ID] = current
	unsupported := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(f.second.Definition.ID, "mapped", "UNSUPPORTED")}}
	f.mu.Lock()
	f.policy["instance.create"] = c.Decision{Allow: false, State: "mutation-policy-1"}
	f.mu.Unlock()
	denied := f.transaction(unsupported, false, 200)
	if denied.Status != "failed" || denied.Error == nil || denied.Error.Code != "forbidden" {
		t.Fatal("inaccessible storage mode was disclosed before authorization")
	}
	deniedLookup := f.transaction(unsupported, true, 200)
	if !deniedLookup.Replayed || deniedLookup.Status != "failed" || deniedLookup.Error == nil || deniedLookup.Error.Code != "forbidden" || len(deniedLookup.Results) != 0 {
		t.Fatal("minimal permission rejection evidence was not recoverable")
	}
	f.mu.Lock()
	delete(f.policy, "instance.create")
	f.mu.Unlock()
	unsupported.IdempotencyKey = c.NewID()
	mapped := f.transaction(unsupported, false, 200)
	if mapped.Status != "failed" || mapped.Error == nil || mapped.Error.Code != "transaction_storage_unsupported" {
		t.Fatal("mapped transaction storage was not explicitly rejected")
	}
}

func TestMySQLWriteTransactionPreflightFailureRecovery(t *testing.T) {
	f := newTransactionFixture(t)
	base := f.create("BASE")
	invalid := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{{SchemaID: f.current.Definition.ID, Operation: "update", ClientKey: "invalid-condition", ID: base.ID, IfMatch: `W/"old"`, Values: map[string]json.RawMessage{testFieldDept: raw("D2")}}}}
	before := f.transactionCounts()
	for _, lookup := range []bool{false, true, false} {
		failed := f.transaction(invalid, lookup, 200)
		if failed.Status != "failed" || failed.Error == nil || failed.Error.Code != "precondition_required" || failed.Committed || len(failed.Results) != 0 {
			t.Fatal("deterministic format failure was not recoverable as durable failed")
		}
	}
	if f.transactionCounts() != [4]int{before[0], before[1], before[2], before[3] + 1} {
		t.Fatal("preflight failure performed data writes or created multiple receipts")
	}
	invalid.IdempotencyKey = c.NewID()
	unchanged := f.transactionCounts()
	f.transaction(invalid, true, 428)
	if f.transactionCounts() != unchanged {
		t.Fatal("invalid status lookup created a rejection receipt")
	}
	committedBody := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(f.current.Definition.ID, "committed", "COMMITTED")}}
	committed := f.transaction(committedBody, false, 200)
	f.mu.Lock()
	f.policy["instance.create"] = c.Decision{Allow: false, State: "mutation-policy-1"}
	f.mu.Unlock()
	committedCounts := f.transactionCounts()
	for _, lookup := range []bool{false, true} {
		f.transaction(committedBody, lookup, 403)
	}
	deniedBody := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(f.current.Definition.ID, "denied", "DENIED")}}
	denied := f.transaction(deniedBody, false, 200)
	if denied.Status != "failed" || denied.Error == nil || denied.Error.Code != "forbidden" || len(denied.Results) != 0 {
		t.Fatal("operation permission rejection was not recorded safely")
	}
	denied = f.transaction(deniedBody, true, 200)
	if denied.Status != "failed" || !denied.Replayed || denied.Error == nil || denied.Error.Code != "forbidden" || len(denied.Results) != 0 {
		t.Fatal("rejected permission evidence leaked business results")
	}
	if f.transactionCounts() != [4]int{committedCounts[0], committedCounts[1], committedCounts[2], committedCounts[3] + 1} {
		t.Fatal("preflight permission rejection changed committed data")
	}
	f.mu.Lock()
	delete(f.policy, "instance.create")
	f.mu.Unlock()
	protected := f.transaction(committedBody, true, 200)
	if protected.Status != "committed" || !protected.Committed || protected.Results[0].ID != committed.Results[0].ID {
		t.Fatal("current rejection overwrote a previous committed receipt")
	}
}

func TestMySQLWriteTransactionRejectedPreflightWaitsForInFlightCommit(t *testing.T) {
	f := newTransactionFixture(t)
	body := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(f.current.Definition.ID, "in-flight", "IN_FLIGHT")}}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	type reply struct {
		result c.WriteTransactionResult
		err    error
	}
	first, second := make(chan reply, 1), make(chan reply, 1)
	go func() {
		calls := 0
		result, err := f.core.executeGuardedWriteTransaction(context.Background(), f.p, body, false, func(ctx context.Context) error {
			calls++
			if calls == 2 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
		first <- reply{result, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight transaction did not reach the commit guard")
	}
	f.mu.Lock()
	f.policy["instance.create"] = c.Decision{Allow: false, State: "mutation-policy-1"}
	f.mu.Unlock()
	go func() {
		result, err := f.core.executeWriteTransaction(context.Background(), f.p, body, false)
		second <- reply{result, err}
	}()
	select {
	case <-second:
		t.Fatal("preflight rejection bypassed the original transaction receipt lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	committed := <-first
	if committed.err != nil || committed.result.Status != "committed" {
		t.Fatal("admitted transaction did not commit")
	}
	rejected := <-second
	var api *apiError
	if !errors.As(rejected.err, &api) || api.status != 403 || rejected.result.Status == "failed" {
		t.Fatal("competing preflight rejection replaced a committed result")
	}
	f.mu.Lock()
	delete(f.policy, "instance.create")
	f.mu.Unlock()
	recovered := f.transaction(body, true, 200)
	if recovered.Status != "committed" || !recovered.Committed || f.transactionCounts() != [4]int{1, 0, 1, 1} {
		t.Fatal("concurrent rejection corrupted the terminal receipt")
	}
}
