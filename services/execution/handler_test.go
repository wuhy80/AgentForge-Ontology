package execution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type memoryStore struct {
	mu         sync.Mutex
	records    map[string]Record
	onClaim    func()
	failFinish bool
}

func cloneRecord(in Record) Record {
	raw, _ := json.Marshal(in)
	var out Record
	_ = json.Unmarshal(raw, &out)
	return out
}
func (s *memoryStore) Ping(context.Context) error { return nil }
func (s *memoryStore) FindKey(_ context.Context, tenant, subject, key string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[tenant+":"+subject+":"+key]
	if !ok {
		return Record{}, sql.ErrNoRows
	}
	return cloneRecord(r), nil
}
func (s *memoryStore) FindID(_ context.Context, tenant, subject, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.records {
		if r.TenantID == tenant && r.SubjectID == subject && r.Invocation.ID == id {
			return cloneRecord(r), nil
		}
	}
	return Record{}, sql.ErrNoRows
}
func (s *memoryStore) Claim(_ context.Context, r Record) (bool, error) {
	s.mu.Lock()
	key := r.TenantID + ":" + r.SubjectID + ":" + r.RequestKey
	if _, ok := s.records[key]; ok {
		s.mu.Unlock()
		return false, nil
	}
	s.records[key] = cloneRecord(r)
	hook := s.onClaim
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return true, nil
}
func (s *memoryStore) Finish(_ context.Context, r Record) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFinish {
		return false, errors.New("database temporarily unavailable")
	}
	key := r.TenantID + ":" + r.SubjectID + ":" + r.RequestKey
	old, ok := s.records[key]
	if !ok || old.Invocation.Status != "running" {
		return false, nil
	}
	s.records[key] = cloneRecord(r)
	return true, nil
}

type memoryMetadata struct {
	mu          sync.Mutex
	definitions map[string]c.Definition
	drafts      map[string]c.Definition
}

func (m *memoryMetadata) Draft(_ context.Context, tenant, id string) (c.CurrentDefinition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[tenant+":"+id]
	if !ok {
		return c.CurrentDefinition{}, problem(404, "not_found", "not found")
	}
	raw, _ := json.Marshal(d)
	var isolated c.Definition
	_ = json.Unmarshal(raw, &isolated)
	return c.CurrentDefinition{Definition: isolated, Status: "draft", ETag: digest(isolated)}, nil
}

func (m *memoryMetadata) setDraft(d c.Definition) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drafts == nil {
		m.drafts = map[string]c.Definition{}
	}
	m.drafts[d.TenantID+":"+d.ID] = d
}

func (m *memoryMetadata) Current(_ context.Context, tenant, id string) (c.CurrentDefinition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.definitions[tenant+":"+id]
	if !ok {
		return c.CurrentDefinition{}, problem(404, "not_found", "not found")
	}
	raw, _ := json.Marshal(d)
	var isolated c.Definition
	_ = json.Unmarshal(raw, &isolated)
	return c.CurrentDefinition{Definition: isolated, Status: "active", ETag: digest(isolated)}, nil
}
func (m *memoryMetadata) set(d c.Definition) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.definitions[d.TenantID+":"+d.ID] = d
}

type testAuthority struct {
	mu        sync.Mutex
	denied    map[string]bool
	loggedOut atomic.Bool
}

func (a *testAuthority) Authorize(_ context.Context, p c.Principal, r c.AuthorizationRequest) (c.Decision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return c.Decision{Allow: !a.denied[r.Operation+":"+r.ResourceID], State: "current-state"}, nil
}
func (a *testAuthority) deny(op, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.denied[op+":"+id] = true
}
func (a *testAuthority) CheckSession(_ context.Context, check c.SessionCheck) (c.Session, error) {
	if a.loggedOut.Load() {
		return c.Session{}, &platform.AccessError{Status: 401, Code: "unauthenticated", Message: "session revoked"}
	}
	return c.Session{User: c.User{ID: "user", TenantID: "tenant", Enabled: true}, ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
}

type fakeRunner struct {
	calls    atomic.Int32
	readyErr error
	run      func(context.Context, *c.ScriptRuntime, map[string]json.RawMessage, string) (map[string]json.RawMessage, error)
}

func (f *fakeRunner) Ready(context.Context) (string, error) {
	if f.readyErr != nil {
		return "", f.readyErr
	}
	return "sha256:" + strings.Repeat("a", 64), nil
}

func TestUnavailableRuntimeAdvertisesNoInvocationAndNeverFallsBack(t *testing.T) {
	f := newFixture()
	f.runner.readyErr = errors.New("Docker is unavailable")
	w := request(f.h, "GET", "/execution/capabilities", "", "")
	if w.Code != 200 {
		t.Fatalf("capability status %d", w.Code)
	}
	var capabilities struct {
		Invocation bool     `json:"invocation"`
		Runtimes   []string `json:"runtimes"`
	}
	if json.Unmarshal(w.Body.Bytes(), &capabilities) != nil || capabilities.Invocation || len(capabilities.Runtimes) != 0 {
		t.Fatalf("misleading capabilities %s", w.Body)
	}
	if w := request(f.h, "POST", "/execution/invocations", "unavailable", validBody); w.Code != 503 || f.runner.calls.Load() != 0 {
		t.Fatalf("unavailable runtime started work: %d %s", w.Code, w.Body)
	}
	if len(f.store.records) != 0 {
		t.Fatal("unavailable runtime saved a running invocation")
	}
	if w := request(f.h, "GET", "/readyz", "", ""); w.Code != 503 {
		t.Fatalf("unavailable runtime was ready: %d", w.Code)
	}
}
func (f *fakeRunner) Run(ctx context.Context, r *c.ScriptRuntime, input map[string]json.RawMessage, image string) (map[string]json.RawMessage, error) {
	f.calls.Add(1)
	if f.run != nil {
		return f.run(ctx, r, input, image)
	}
	return map[string]json.RawMessage{"结果": json.RawMessage(`"9007199254740993.10"`)}, nil
}

type fixture struct {
	h      http.Handler
	store  *memoryStore
	meta   *memoryMetadata
	auth   *testAuthority
	runner *fakeRunner
	fn     c.Definition
}

func newFixture() *fixture {
	f := &fixture{store: &memoryStore{records: map[string]Record{}}, meta: &memoryMetadata{definitions: map[string]c.Definition{}}, auth: &testAuthority{denied: map[string]bool{}}, runner: &fakeRunner{}}
	f.fn = c.Definition{ID: "function", TenantID: "tenant", CategoryID: "category", GroupID: "group", Kind: c.KindFunction, Name: "精确计算", Function: &c.Callable{Input: []c.Property{{ID: "fn-in", Name: "输入", DataType: "decimal", Precision: 25, Scale: 2, Required: true}}, Output: []c.Property{{ID: "fn-out", Name: "结果", DataType: "decimal", Precision: 25, Scale: 2, Required: true}}, SideEffects: "none", Runtime: &c.ScriptRuntime{Language: "python", Source: "def main(inputs):\n    return {'结果': inputs['输入']}", Entrypoint: "main", TimeoutSeconds: 5}}}
	f.meta.set(f.fn)
	f.h = NewHandler(Config{Store: f.store, Metadata: f.meta, Authorizer: f.auth, SessionChecker: f.auth, Runner: f.runner, MaxConcurrent: 1})
	return f
}

func request(h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://workbench.test"+path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: platform.SessionCookieName, Value: strings.Repeat("s", 48)})
	r.Header.Set("Origin", "http://workbench.test")
	r.Header.Set("X-CSRF-Token", strings.Repeat("c", 48))
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{TenantID: "tenant", Subject: "user", ExpiresAt: time.Now().Add(time.Hour).Unix()}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const validBody = `{"definitionId":"function","input":{"fn-in":"9007199254740993.10"}}`

func invocation(t *testing.T, w *httptest.ResponseRecorder, status int) c.Invocation {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body)
	}
	var out c.Invocation
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInvocationDurabilityIdempotencyAndCurrentAccess(t *testing.T) {
	f := newFixture()
	first := request(f.h, "POST", "/execution/invocations", "same", validBody)
	got := invocation(t, first, 201)
	if got.Status != "succeeded" || string(got.Output["fn-out"]) != `"9007199254740993.10"` {
		t.Fatalf("invalid result %+v", got)
	}
	if strings.Contains(first.Body.String(), "def main") || strings.Contains(first.Body.String(), "fn-in") || strings.Contains(first.Body.String(), "imageId") {
		t.Fatal("private evidence escaped")
	}
	replay := invocation(t, request(f.h, "POST", "/execution/invocations", "same", validBody), 200)
	if replay.ID != got.ID || f.runner.calls.Load() != 1 {
		t.Fatal("idempotency reran the computation")
	}
	if w := request(f.h, "POST", "/execution/invocations", "same", strings.Replace(validBody, "9007199254740993.10", "2.00", 1)); w.Code != 409 {
		t.Fatalf("conflict: %d %s", w.Code, w.Body)
	}
	if _, err := f.store.FindID(context.Background(), "other-tenant", "user", got.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cross-tenant record access")
	}
	if _, err := f.store.FindID(context.Background(), "tenant", "other-user", got.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cross-user record access")
	}
	f.auth.deny("function.invoke", "function")
	for _, w := range []*httptest.ResponseRecorder{request(f.h, "GET", "/execution/invocations/"+got.ID, "", ""), request(f.h, "POST", "/execution/invocations", "same", validBody)} {
		if w.Code != 403 || strings.Contains(w.Body.String(), "9007199254740993") {
			t.Fatalf("revoked result leaked: %d %s", w.Code, w.Body)
		}
	}
	f.auth.loggedOut.Store(true)
	if w := request(f.h, "GET", "/execution/invocations/"+got.ID, "", ""); w.Code != 401 {
		t.Fatalf("logged-out result accepted: %d", w.Code)
	}
}

func TestActionBindingRequiresBothInvokesAndMapsOwnIDs(t *testing.T) {
	f := newFixture()
	action := c.Definition{ID: "action", TenantID: "tenant", CategoryID: "category", GroupID: "group", Kind: c.KindAction, Name: "业务计算", Action: &c.Callable{FunctionID: "function", SideEffects: "none", Input: append([]c.Property(nil), f.fn.Function.Input...), Output: append([]c.Property(nil), f.fn.Function.Output...)}}
	action.Action.Input[0].ID = "action-in"
	action.Action.Output[0].ID = "action-out"
	f.meta.set(action)
	body := `{"definitionId":"action","input":{"action-in":"9007199254740993.10"}}`
	f.auth.deny("function.invoke", "function")
	if w := request(f.h, "POST", "/execution/invocations", "action-1", body); w.Code != 403 || f.runner.calls.Load() != 0 {
		t.Fatalf("action bypassed function.invoke: %d %s", w.Code, w.Body)
	}
	f.auth.mu.Lock()
	f.auth.denied = map[string]bool{}
	f.auth.mu.Unlock()
	got := invocation(t, request(f.h, "POST", "/execution/invocations", "action-1", body), 201)
	if got.Kind != c.KindAction || string(got.Output["action-out"]) != `"9007199254740993.10"` || got.Output["fn-out"] != nil {
		t.Fatalf("wrong action output %+v", got)
	}
	action.Action.Input[0].Scale = 1
	f.meta.set(action)
	if w := request(f.h, "POST", "/execution/invocations", "action-2", body); w.Code != 422 {
		t.Fatalf("incompatible signatures: %d %s", w.Code, w.Body)
	}
}

func inlineAction(f *fixture) c.Definition {
	raw, _ := json.Marshal(f.fn)
	var action c.Definition
	_ = json.Unmarshal(raw, &action)
	action.ID, action.Kind, action.Name, action.Action, action.Function = "inline-action", c.KindAction, "独立源码动作", action.Function, nil
	action.Action.Input[0].ID, action.Action.Output[0].ID = "action-in", "action-out"
	action.Action.TargetSchemaIDs = []string{"associated-schema"}
	return action
}

const inlineActionBody = `{"definitionId":"inline-action","input":{"action-in":"9007199254740993.10"}}`

func TestInlineActionOwnSourceAuthorizationSnapshotAndReplay(t *testing.T) {
	f := newFixture()
	action := inlineAction(f)
	f.meta.set(action)
	// A native Action must never invent or require a Function identity.
	f.auth.deny("function.invoke", f.fn.ID)
	f.auth.deny("function.invoke", action.ID)
	first := invocation(t, request(f.h, "POST", "/execution/invocations", "inline-1", inlineActionBody), 201)
	if first.Kind != c.KindAction || first.Status != "succeeded" || string(first.Output["action-out"]) != `"9007199254740993.10"` {
		t.Fatalf("invalid inline Action result: %+v", first)
	}
	record, err := f.store.FindKey(context.Background(), "tenant", "user", "inline-1")
	if err != nil || record.Implementation.Kind != c.KindAction || record.Implementation.ID != action.ID || record.Implementation.Function != nil || record.Implementation.Action.Runtime.Source != action.Action.Runtime.Source {
		t.Fatalf("Action implementation identity or code was lost: %+v %v", record.Implementation, err)
	}
	replay := invocation(t, request(f.h, "POST", "/execution/invocations", "inline-1", inlineActionBody), 200)
	if replay.ID != first.ID || f.runner.calls.Load() != 1 {
		t.Fatal("inline Action replay restarted the computation")
	}
	// The stored snapshot remains immutable and newly published code invalidates
	// old output as well as same-key replay, without automatically rerunning it.
	action.Action.Runtime.Source += "\n# updated Action source"
	f.meta.set(action)
	for _, w := range []*httptest.ResponseRecorder{
		request(f.h, "GET", "/execution/invocations/"+first.ID, "", ""),
		request(f.h, "POST", "/execution/invocations", "inline-1", inlineActionBody),
	} {
		if w.Code != 409 || strings.Contains(w.Body.String(), "9007199254740993") {
			t.Fatalf("stale Action output delivered: %d %s", w.Code, w.Body)
		}
	}
	next := invocation(t, request(f.h, "POST", "/execution/invocations", "inline-2", inlineActionBody), 201)
	if next.CodeDigest == first.CodeDigest || next.CodeDigest != sourceDigest(action.Action.Runtime.Source) || f.runner.calls.Load() != 2 {
		t.Fatal("new Action invocation did not use its current source")
	}
	old, _ := f.store.FindKey(context.Background(), "tenant", "user", "inline-1")
	if old.Implementation.Action.Runtime.Source != record.Implementation.Action.Runtime.Source {
		t.Fatal("source update rewrote the persisted Action snapshot")
	}
	f.auth.deny("action.invoke", action.ID)
	for _, w := range []*httptest.ResponseRecorder{
		request(f.h, "GET", "/execution/invocations/"+next.ID, "", ""),
		request(f.h, "POST", "/execution/invocations", "inline-2", inlineActionBody),
		request(f.h, "POST", "/execution/invocations", "inline-3", inlineActionBody),
	} {
		if w.Code != 403 || strings.Contains(w.Body.String(), "9007199254740993") {
			t.Fatalf("revoked Action access delivered output: %d %s", w.Code, w.Body)
		}
	}
	if f.runner.calls.Load() != 2 {
		t.Fatal("revoked Action executed")
	}
}

func TestInlineActionRejectsAmbiguousOrUnsupportedImplementations(t *testing.T) {
	for name, mutate := range map[string]func(*c.Callable){
		"bound and source":       func(callable *c.Callable) { callable.FunctionID = "function" },
		"artifact and source":    func(callable *c.Callable) { callable.ArtifactRef = "artifact" },
		"side effects":           func(callable *c.Callable) { callable.SideEffects = "dataWrite" },
		"unsupported language":   func(callable *c.Callable) { callable.Runtime.Language = "ruby" },
		"missing implementation": func(callable *c.Callable) { callable.Runtime = nil },
		"unsupported parameter":  func(callable *c.Callable) { callable.Input[0].EnumID = "enum" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			action := inlineAction(f)
			mutate(action.Action)
			f.meta.set(action)
			w := request(f.h, "POST", "/execution/invocations", "invalid-action", inlineActionBody)
			if w.Code != 422 || f.runner.calls.Load() != 0 || len(f.store.records) != 0 {
				t.Fatalf("unsupported Action was admitted: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestInlineActionRechecksOwnPermissionBeforeStartAndDelivery(t *testing.T) {
	for _, stage := range []string{"after-claim", "after-run"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture()
			action := inlineAction(f)
			f.meta.set(action)
			revoke := func() { f.auth.deny("action.invoke", action.ID) }
			if stage == "after-claim" {
				f.store.onClaim = revoke
			} else {
				f.runner.run = func(context.Context, *c.ScriptRuntime, map[string]json.RawMessage, string) (map[string]json.RawMessage, error) {
					revoke()
					return map[string]json.RawMessage{"结果": json.RawMessage(`"9007199254740993.10"`)}, nil
				}
			}
			w := request(f.h, "POST", "/execution/invocations", "revoked-inline", inlineActionBody)
			if w.Code != 403 || strings.Contains(w.Body.String(), "9007199254740993") {
				t.Fatalf("revoked native Action result escaped: %d %s", w.Code, w.Body)
			}
			record, err := f.store.FindKey(context.Background(), "tenant", "user", "revoked-inline")
			if err != nil || record.Invocation.Status == "running" || stage == "after-claim" && f.runner.calls.Load() != 0 {
				t.Fatalf("invalid persisted native Action outcome: %+v %v", record, err)
			}
		})
	}
}

func TestImplementationSnapshotReadsExistingPersistedJSON(t *testing.T) {
	f := newFixture()
	raw, err := json.Marshal(map[string]any{"root": f.fn, "function": f.fn})
	var record Record
	if err != nil || json.Unmarshal(raw, &record) != nil || digest(record.Implementation) != digest(f.fn) {
		t.Fatal("existing Function evidence no longer loads")
	}
}

func TestCallValidationRejectsUnimplementedCapabilities(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   string
		change func(*fixture)
		want   int
	}{
		{"missing key", validBody, nil, 400},
		{"numeric decimal", `{"definitionId":"function","input":{"fn-in":1.10}}`, nil, 422},
		{"unknown field", `{"definitionId":"function","input":{"secret":"x"}}`, nil, 422},
		{"missing input", `{"definitionId":"function","input":{}}`, nil, 422},
		{"caller identity", `{"definitionId":"function","input":{},"viaAction":"action"}`, nil, 400},
		{"unsupported language", validBody, func(f *fixture) { f.fn.Function.Runtime.Language = "ruby"; f.meta.set(f.fn) }, 422},
		{"side effect", validBody, func(f *fixture) { f.fn.Function.SideEffects = "externalWrite"; f.meta.set(f.fn) }, 422},
		{"legacy", validBody, func(f *fixture) { f.fn.Function.Runtime = nil; f.meta.set(f.fn) }, 422},
		{"enum", validBody, func(f *fixture) { f.fn.Function.Input[0].EnumID = "enum"; f.meta.set(f.fn) }, 422},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture()
			if test.change != nil {
				test.change(f)
			}
			key := "valid"
			if test.name == "missing key" {
				key = ""
			}
			w := request(f.h, "POST", "/execution/invocations", key, test.body)
			if w.Code != test.want || f.runner.calls.Load() != 0 {
				t.Fatalf("%d %s calls=%d", w.Code, w.Body, f.runner.calls.Load())
			}
		})
	}
	f := newFixture()
	f.runner.run = func(context.Context, *c.ScriptRuntime, map[string]json.RawMessage, string) (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{"结果": json.RawMessage(`1.1`), "未声明": json.RawMessage("null")}, nil
	}
	out := invocation(t, request(f.h, "POST", "/execution/invocations", "invalid-output", validBody), 201)
	if out.Status != "failed" || out.Error == nil || out.Error.Code != "invalid_output" || len(out.Output) != 0 {
		t.Fatalf("bad output %+v", out)
	}
}

func TestCurrentChecksAtStartupAndResultDelivery(t *testing.T) {
	for _, mode := range []string{"revoke-after-run", "logout-after-run", "code-after-run", "revoke-after-claim"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture()
			want := 403
			change := func() {
				switch mode {
				case "logout-after-run":
					f.auth.loggedOut.Store(true)
				case "code-after-run":
					f.fn.Function.Runtime.Source += "\n# updated"
					f.meta.set(f.fn)
				default:
					f.auth.deny("function.invoke", "function")
				}
			}
			if mode == "logout-after-run" {
				want = 401
			}
			if mode == "code-after-run" {
				want = 409
			}
			if mode == "revoke-after-claim" {
				f.store.onClaim = change
			} else {
				f.runner.run = func(context.Context, *c.ScriptRuntime, map[string]json.RawMessage, string) (map[string]json.RawMessage, error) {
					change()
					return map[string]json.RawMessage{"结果": json.RawMessage(`"9007199254740993.10"`)}, nil
				}
			}
			w := request(f.h, "POST", "/execution/invocations", "change", validBody)
			if w.Code != want || strings.Contains(w.Body.String(), "9007199254740993") {
				t.Fatalf("changed access leaked output: %d %s", w.Code, w.Body)
			}
			r, err := f.store.FindKey(context.Background(), "tenant", "user", "change")
			if err != nil || r.Invocation.Status == "running" {
				t.Fatalf("terminal outcome was not saved: %+v %v", r, err)
			}
			if mode == "revoke-after-claim" && f.runner.calls.Load() != 0 {
				t.Fatal("revoked computation started")
			}
		})
	}
}

func TestRunningReplayConcurrencyAndInterruptedOutcome(t *testing.T) {
	f := newFixture()
	entered, release := make(chan struct{}), make(chan struct{})
	f.runner.run = func(ctx context.Context, _ *c.ScriptRuntime, _ map[string]json.RawMessage, _ string) (map[string]json.RawMessage, error) {
		close(entered)
		select {
		case <-release:
			return map[string]json.RawMessage{"结果": json.RawMessage(`"9007199254740993.10"`)}, nil
		case <-ctx.Done():
			return nil, &RunError{"timed_out", "execution_timeout", "timeout"}
		}
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(f.h, "POST", "/execution/invocations", "running", validBody) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("runner never started")
	}
	running := invocation(t, request(f.h, "POST", "/execution/invocations", "running", validBody), 202)
	if running.Status != "running" || f.runner.calls.Load() != 1 {
		t.Fatal("replay restarted running execution")
	}
	if w := request(f.h, "POST", "/execution/invocations", "another", validBody); w.Code != 429 {
		t.Fatalf("concurrency limit: %d %s", w.Code, w.Body)
	}
	close(release)
	invocation(t, <-done, 201)
	record, _ := f.store.FindKey(context.Background(), "tenant", "user", "running")
	record.RequestKey = "interrupted"
	record.Invocation.ID = c.NewID()
	record.Invocation.Status = "running"
	record.Invocation.Output = nil
	record.DeadlineAt = time.Now().Add(-time.Minute)
	_, _ = f.store.Claim(context.Background(), record)
	out := invocation(t, request(f.h, "GET", "/execution/invocations/"+record.Invocation.ID, "", ""), 200)
	if out.Status != "interrupted" || f.runner.calls.Load() != 1 {
		t.Fatalf("abandoned execution was rerun %+v", out)
	}
}

func TestValueContractsPreserveExactNumbersAndOptionalValues(t *testing.T) {
	props := []c.Property{{ID: "decimal", Name: "金额", DataType: "decimal", Precision: 30, Scale: 4, Required: true}, {ID: "integer", Name: "数量", DataType: "int64", Default: json.RawMessage(`"9223372036854775807"`)}, {ID: "optional", Name: "说明", DataType: "string"}, {ID: "nullable", Name: "空值", DataType: "string", Nullable: true}}
	in, err := buildInput(props, map[string]json.RawMessage{"decimal": json.RawMessage(`"9007199254740993.1250"`), "nullable": json.RawMessage(`null`)})
	if err != nil || !bytes.Equal(in["金额"], []byte(`"9007199254740993.1250"`)) || string(in["数量"]) != `"9223372036854775807"` {
		t.Fatalf("exact/default input %v %v", in, err)
	}
	if _, ok := in["说明"]; ok {
		t.Fatal("missing optional parameter injected")
	}
	for _, value := range []string{`9007199254740993.125`, `"1e2"`, `"0.00001"`, `null`} {
		if _, err := buildInput(props, map[string]json.RawMessage{"decimal": json.RawMessage(value)}); err == nil {
			t.Fatalf("invalid decimal accepted: %s", value)
		}
	}
	if validateValue(c.Property{Name: "时间", DataType: "timestamp"}, json.RawMessage(`"2026-11-07T01:02:03.1234567Z"`)) == nil {
		t.Fatal("submicrosecond timestamp accepted")
	}
}
