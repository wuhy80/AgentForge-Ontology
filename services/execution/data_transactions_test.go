package execution

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type preparingMemoryStore struct {
	*memoryStore
	failPrepare bool
}

func (s *preparingMemoryStore) Prepare(_ context.Context, r Record) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPrepare {
		return false, errors.New("prepare failed")
	}
	k := r.TenantID + ":" + r.SubjectID + ":" + r.RequestKey
	old, ok := s.records[k]
	if !ok || old.Invocation.Status != "running" {
		return false, nil
	}
	s.records[k] = cloneRecord(r)
	return true, nil
}
func (s *preparingMemoryStore) Finish(_ context.Context, r Record) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFinish {
		return false, errors.New("finish failed")
	}
	k := r.TenantID + ":" + r.SubjectID + ":" + r.RequestKey
	old, ok := s.records[k]
	from := "running"
	if r.Transaction != nil {
		from = "committing"
	}
	if !ok || old.Invocation.Status != from {
		return false, nil
	}
	s.records[k] = cloneRecord(r)
	return true, nil
}

type dataPlanRunner struct {
	*fakeRunner
	result c.ScriptExecutionResult
	err    error
}

func (r *dataPlanRunner) RunWithData(context.Context, *c.ScriptRuntime, map[string]json.RawMessage, string) (c.ScriptExecutionResult, error) {
	r.calls.Add(1)
	return r.result, r.err
}

type dataClientFunc func(context.Context, c.ExecutionTransactionRequest) (c.WriteTransactionResult, error)

func (f dataClientFunc) Execute(ctx context.Context, r c.ExecutionTransactionRequest) (c.WriteTransactionResult, error) {
	return f(ctx, r)
}

func dataFixture() (*fixture, *preparingMemoryStore, *dataPlanRunner) {
	f := newFixture()
	f.fn.Function.SideEffects = "dataWrite"
	f.fn.Function.DataWrite = []c.DataWriteCapability{{SchemaID: "schema", Operations: []string{"create"}}}
	f.meta.set(f.fn)
	s := &preparingMemoryStore{memoryStore: f.store}
	r := &dataPlanRunner{fakeRunner: f.runner, result: c.ScriptExecutionResult{Output: map[string]json.RawMessage{"结果": json.RawMessage(`"1.00"`)}, Operations: []c.TransactionOperation{{SchemaID: "schema", Operation: "create", ClientKey: "a", Values: map[string]json.RawMessage{"field": json.RawMessage(`"value"`)}}}}}
	return f, s, r
}
func setDataHandler(f *fixture, s Store, r Runner, client DataTransactionClient) {
	f.h = NewHandler(Config{Store: s, Metadata: f.meta, Authorizer: f.auth, SessionChecker: f.auth, Runner: r, DataTransactions: client})
}

func TestDataWritePlanPersistsBeforeCommitAndRecoversLostResponseWithoutRerun(t *testing.T) {
	f, s, r := dataFixture()
	calls := 0
	client := dataClientFunc(func(ctx context.Context, body c.ExecutionTransactionRequest) (c.WriteTransactionResult, error) {
		calls++
		stored, err := s.FindID(ctx, "tenant", "user", body.InvocationID)
		if err != nil || stored.Invocation.Status != "committing" || stored.Transaction == nil || len(stored.Invocation.Output) != 0 || len(stored.PreparedOutput) != 1 {
			t.Fatal("Data called before durable preparation")
		}
		if body.Session.Token == "" || body.Transaction.IdempotencyKey != "execution:"+body.InvocationID || body.DefinitionDigest != digest(f.fn) {
			t.Fatal("unbound bridge request")
		}
		if calls == 1 {
			return c.WriteTransactionResult{}, errors.New("response lost after commit")
		}
		if !body.Lookup {
			t.Fatal("GET retried a write")
		}
		return c.WriteTransactionResult{Atomic: true, Committed: true, Status: "committed", Replayed: true}, nil
	})
	setDataHandler(f, s, r, client)
	pending := invocation(t, request(f.h, "POST", "/execution/invocations", "write", validBody), 202)
	if pending.Status != "committing" || len(pending.Output) != 0 || pending.Error.Code != "transaction_pending" {
		t.Fatal("ambiguous commit exposed as terminal")
	}
	// A restart and an expired computation deadline must preserve committing.
	s.mu.Lock()
	record := s.records["tenant:user:write"]
	record.DeadlineAt = time.Now().Add(-time.Hour)
	s.records["tenant:user:write"] = record
	s.mu.Unlock()
	setDataHandler(f, s, r, client)
	got := invocation(t, request(f.h, "GET", "/execution/invocations/"+pending.ID, "", ""), 200)
	if got.Status != "succeeded" || got.Transaction == nil || !got.Transaction.Committed || string(got.Output["fn-out"]) != `"1.00"` || r.calls.Load() != 1 || calls != 2 {
		t.Fatal("recovery did not use original plan")
	}
}

func TestDataWriteMissingReceiptOnlyPostResumesAndFinishFailureRecovers(t *testing.T) {
	f, s, r := dataFixture()
	writes, lookups := 0, 0
	committed := false
	client := dataClientFunc(func(_ context.Context, b c.ExecutionTransactionRequest) (c.WriteTransactionResult, error) {
		if b.Lookup {
			lookups++
			if !committed {
				return c.WriteTransactionResult{Atomic: true, Status: "not_found"}, nil
			}
		} else {
			writes++
			if writes == 1 {
				return c.WriteTransactionResult{}, errors.New("request lost before commit")
			}
			committed = true
		}
		return c.WriteTransactionResult{Atomic: true, Committed: true, Status: "committed"}, nil
	})
	setDataHandler(f, s, r, client)
	pending := invocation(t, request(f.h, "POST", "/execution/invocations", "resume", validBody), 202)
	got := invocation(t, request(f.h, "GET", "/execution/invocations/"+pending.ID, "", ""), 202)
	if got.Status != "committing" || writes != 1 || lookups != 1 {
		t.Fatal("missing receipt claimed rollback or GET wrote")
	}
	s.failFinish = true
	if w := request(f.h, "POST", "/execution/invocations", "resume", validBody); w.Code != 500 {
		t.Fatal("fixture did not fail terminal persistence")
	}
	s.failFinish = false
	got = invocation(t, request(f.h, "GET", "/execution/invocations/"+pending.ID, "", ""), 200)
	if got.Status != "succeeded" || r.calls.Load() != 1 || writes != 2 {
		t.Fatal("terminal persistence recovery reran script or writes")
	}
}

func TestDataWriteValidationAndRunnerFailureNeverCommit(t *testing.T) {
	for _, kind := range []string{"runtime", "output", "scope", "prepare"} {
		t.Run(kind, func(t *testing.T) {
			f, s, r := dataFixture()
			calls := 0
			switch kind {
			case "runtime":
				r.err = errors.New("code failed")
			case "output":
				r.result.Output = map[string]json.RawMessage{}
			case "scope":
				r.result.Operations[0].SchemaID = "undeclared"
			case "prepare":
				s.failPrepare = true
			}
			setDataHandler(f, s, r, dataClientFunc(func(context.Context, c.ExecutionTransactionRequest) (c.WriteTransactionResult, error) {
				calls++
				return c.WriteTransactionResult{}, nil
			}))
			w := request(f.h, "POST", "/execution/invocations", "reject", validBody)
			if calls != 0 {
				t.Fatal("invalid execution committed")
			}
			if kind == "prepare" {
				if w.Code != 500 {
					t.Fatal(w.Code)
				}
			} else {
				got := invocation(t, w, 201)
				if got.Status != "failed" || len(got.Output) != 0 {
					t.Fatal("invalid execution succeeded")
				}
			}
		})
	}
}

func TestDataWriteDurableFailureAndAccessRecheck(t *testing.T) {
	f, s, r := dataFixture()
	calls := 0
	setDataHandler(f, s, r, dataClientFunc(func(context.Context, c.ExecutionTransactionRequest) (c.WriteTransactionResult, error) {
		calls++
		return c.WriteTransactionResult{Atomic: true, Status: "failed", Error: &c.Error{Code: "precondition_failed", Message: "全部回滚"}}, nil
	}))
	got := invocation(t, request(f.h, "POST", "/execution/invocations", "rollback", validBody), 201)
	if got.Status != "failed" || got.Transaction == nil || got.Transaction.Committed || got.Error.Code != "precondition_failed" {
		t.Fatal("durable rollback not delivered")
	}
	f.auth.deny("function.invoke", f.fn.ID)
	if w := request(f.h, "GET", "/execution/invocations/"+got.ID, "", ""); w.Code != 403 || calls != 1 {
		t.Fatal("current invoke denial bypassed")
	}
}

func TestDataWriteRejectsDraftAndExternalConnection(t *testing.T) {
	f, s, r := dataFixture()
	f.meta.setDraft(f.fn)
	setDataHandler(f, s, r, dataClientFunc(func(context.Context, c.ExecutionTransactionRequest) (c.WriteTransactionResult, error) {
		t.Fatal("unexpected commit")
		return c.WriteTransactionResult{}, nil
	}))
	draft := strings.TrimSuffix(validBody, "}") + `,"draftEtag":"` + digest(f.fn) + `"}`
	if w := request(f.h, "POST", "/execution/invocations", "draft", draft); w.Code != 422 {
		t.Fatal(w.Code)
	}
	if w := externalInvocationRequest(f.h, "POST", "/execution/invocations", "external", validBody); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if r.calls.Load() != 0 {
		t.Fatal("disallowed invocation ran")
	}
}

func TestRemoteDataTransactionsNeverFollowsRedirectWithSession(t *testing.T) {
	leak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect leaked internal request") }))
	defer leak.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, leak.URL, 307) }))
	defer server.Close()
	client := &RemoteDataTransactions{URL: server.URL, ServiceToken: strings.Repeat("k", 48)}
	if _, err := client.Execute(context.Background(), c.ExecutionTransactionRequest{}); err == nil {
		t.Fatal("redirect accepted as receipt")
	}
}

func TestUnknownWriteDefinitionChangeNeverSuggestsANewRequestKey(t *testing.T) {
	f, s, r := dataFixture()
	setDataHandler(f, s, r, dataClientFunc(func(context.Context, c.ExecutionTransactionRequest) (c.WriteTransactionResult, error) {
		return c.WriteTransactionResult{}, errors.New("unknown commit")
	}))
	pending := invocation(t, request(f.h, "POST", "/execution/invocations", "unknown", validBody), 202)
	f.fn.Function.Runtime.Source = "def main(inputs,ctx): return {}"
	f.meta.set(f.fn)
	w := request(f.h, "GET", "/execution/invocations/"+pending.ID, "", "")
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	var failure c.Error
	_ = json.Unmarshal(w.Body.Bytes(), &failure)
	if failure.Code != "definition_changed" || !strings.Contains(failure.Message, "请勿更换请求标识") {
		t.Fatal("unknown transaction suggested repeating effects")
	}
	stored, _ := s.FindID(context.Background(), "tenant", "user", pending.ID)
	if stored.Invocation.Status != "committing" || r.calls.Load() != 1 {
		t.Fatal("definition change altered unknown commit")
	}
}

func TestDataWriteRequiresConfiguredDataEndpointBeforeRunningCode(t *testing.T) {
	for _, endpoint := range []string{"", "ftp://data.test", "http://user:secret@data.test", "http://data.test?query=1"} {
		f, s, r := dataFixture()
		setDataHandler(f, s, r, &RemoteDataTransactions{URL: endpoint, ServiceToken: strings.Repeat("k", 48)})
		var caps struct {
			DataWriteTransactions bool `json:"dataWriteTransactions"`
		}
		w := request(f.h, "GET", "/execution/capabilities", "", "")
		if json.Unmarshal(w.Body.Bytes(), &caps) != nil || caps.DataWriteTransactions {
			t.Fatal("unconfigured data endpoint advertised")
		}
		if got := request(f.h, "POST", "/execution/invocations", "bad-endpoint", validBody); got.Code != 503 || r.calls.Load() != 0 {
			t.Fatal("unconfigured data endpoint executed code")
		}
	}
}
