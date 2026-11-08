package execution

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func draftBody(d c.Definition) string {
	return mustJSON(c.InvocationRequest{DefinitionID: d.ID, DraftETag: digest(d), Input: map[string]json.RawMessage{"fn-in": json.RawMessage(`"9007199254740993.10"`)}})
}

func TestDraftInvocationKeepsCurrentAndPersistsExactIdentity(t *testing.T) {
	f := newFixture()
	var draft c.Definition
	_ = json.Unmarshal([]byte(mustJSON(f.fn)), &draft)
	draft.Function.Runtime.Source = "def main(inputs):\n    return {'结果': '7.00'}"
	f.meta.setDraft(draft)
	f.runner.run = func(_ context.Context, r *c.ScriptRuntime, _ map[string]json.RawMessage, _ string) (map[string]json.RawMessage, error) {
		if r.Source == draft.Function.Runtime.Source {
			return map[string]json.RawMessage{"结果": json.RawMessage(`"7.00"`)}, nil
		}
		return map[string]json.RawMessage{"结果": json.RawMessage(`"9007199254740993.10"`)}, nil
	}
	got := invocation(t, request(f.h, "POST", "/execution/invocations", "draft", draftBody(draft)), 201)
	if got.Mode != "draft" || got.DraftETag != digest(draft) || string(got.Output["fn-out"]) != `"7.00"` || got.CodeDigest != sourceDigest(draft.Function.Runtime.Source) {
		t.Fatalf("wrong draft result: %+v", got)
	}
	current := invocation(t, request(f.h, "POST", "/execution/invocations", "current", validBody), 201)
	if current.Mode != "current" || current.DraftETag != "" || string(current.Output["fn-out"]) != `"9007199254740993.10"` {
		t.Fatalf("draft replaced current invocation: %+v", current)
	}
	replayed := invocation(t, request(f.h, "POST", "/execution/invocations", "draft", draftBody(draft)), 200)
	if replayed.ID != got.ID || f.runner.calls.Load() != 2 {
		t.Fatal("draft replay executed again")
	}
	if w := request(f.h, "POST", "/execution/invocations", "draft", validBody); w.Code != 409 {
		t.Fatalf("draft/current reused same key: %d %s", w.Code, w.Body)
	}
	draft.Function.Runtime.Source += "\n# changed"
	f.meta.setDraft(draft)
	if w := request(f.h, "GET", "/execution/invocations/"+got.ID, "", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "draft_changed") {
		t.Fatalf("changed draft exposed prior results: %d %s", w.Code, w.Body)
	}
}

func TestUnpublishedDraftRequiresEditAndInvokeAndFreshETag(t *testing.T) {
	for _, operation := range []string{"definition.edit", "function.invoke"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture()
			f.meta.setDraft(f.fn)
			delete(f.meta.definitions, "tenant:function")
			f.auth.deny(operation, "function")
			if w := request(f.h, "POST", "/execution/invocations", "denied", draftBody(f.fn)); w.Code != 403 || f.runner.calls.Load() != 0 {
				t.Fatalf("missing permission %s ran draft: %d %s", operation, w.Code, w.Body)
			}
		})
	}
	f := newFixture()
	f.meta.setDraft(f.fn)
	delete(f.meta.definitions, "tenant:function")
	got := invocation(t, request(f.h, "POST", "/execution/invocations", "unpublished", draftBody(f.fn)), 201)
	if got.Mode != "draft" || got.Status != "succeeded" {
		t.Fatalf("unpublished draft did not run: %+v", got)
	}
	f.auth.deny("definition.edit", "function")
	if w := request(f.h, "GET", "/execution/invocations/"+got.ID, "", ""); w.Code != 403 {
		t.Fatalf("revoked edit exposed result: %d %s", w.Code, w.Body)
	}
	f = newFixture()
	f.meta.setDraft(f.fn)
	body := strings.Replace(draftBody(f.fn), digest(f.fn), strings.Repeat("a", 64), 1)
	if w := request(f.h, "POST", "/execution/invocations", "stale", body); w.Code != 409 || f.runner.calls.Load() != 0 {
		t.Fatalf("stale draft executed: %d %s", w.Code, w.Body)
	}
	if w := request(f.h, "POST", "/execution/invocations", "invalid", strings.Replace(body, strings.Repeat("a", 64), "quoted-etag", 1)); w.Code != 400 {
		t.Fatalf("invalid draft ETag accepted: %d %s", w.Code, w.Body)
	}
}

func TestDraftRechecksBeforeAdmissionAfterRunAndOnRead(t *testing.T) {
	f := newFixture()
	f.meta.setDraft(f.fn)
	f.store.onClaim = func() { f.auth.deny("definition.edit", "function") }
	w := request(f.h, "POST", "/execution/invocations", "changed-before-run", draftBody(f.fn))
	if w.Code != 403 || f.runner.calls.Load() != 0 {
		t.Fatalf("draft started after edit revoked: %d %s", w.Code, w.Body)
	}
	r, err := f.store.FindKey(context.Background(), "tenant", "user", "changed-before-run")
	if err != nil || r.Invocation.Status != "failed" {
		t.Fatalf("draft left running: %+v %v", r, err)
	}
	f = newFixture()
	f.meta.setDraft(f.fn)
	f.runner.run = func(_ context.Context, _ *c.ScriptRuntime, _ map[string]json.RawMessage, _ string) (map[string]json.RawMessage, error) {
		f.meta.mu.Lock()
		delete(f.meta.drafts, "tenant:function")
		f.meta.mu.Unlock()
		return map[string]json.RawMessage{"结果": json.RawMessage(`"1.00"`)}, nil
	}
	w = request(f.h, "POST", "/execution/invocations", "published-during-run", draftBody(f.fn))
	if w.Code != 404 || strings.Contains(w.Body.String(), "1.00") {
		t.Fatalf("removed draft returned result: %d %s", w.Code, w.Body)
	}
	r, err = f.store.FindKey(context.Background(), "tenant", "user", "published-during-run")
	if err != nil || r.Invocation.Status != "succeeded" {
		t.Fatalf("terminal evidence not saved: %+v %v", r, err)
	}
}

func TestBoundDraftActionRequiresCurrentFunctionAuthorization(t *testing.T) {
	f := newFixture()
	action := c.Definition{ID: "action", TenantID: "tenant", Kind: c.KindAction, Name: "草稿动作", Action: &c.Callable{FunctionID: f.fn.ID, SideEffects: "none", Input: f.fn.Function.Input, Output: f.fn.Function.Output}}
	f.meta.setDraft(action)
	body := draftBody(action)
	got := invocation(t, request(f.h, http.MethodPost, "/execution/invocations", "bound-draft", body), 201)
	if got.Kind != c.KindAction || got.Mode != "draft" {
		t.Fatalf("bound draft identity lost: %+v", got)
	}
	f.auth.deny("function.invoke", f.fn.ID)
	if w := request(f.h, http.MethodPost, "/execution/invocations", "bound-denied", body); w.Code != 403 {
		t.Fatalf("bound draft bypassed function permission: %d %s", w.Code, w.Body)
	}
	if w := request(f.h, http.MethodGet, "/execution/invocations/"+got.ID, "", ""); w.Code != 403 {
		t.Fatalf("bound draft result bypassed function permission: %d %s", w.Code, w.Body)
	}
}

func TestInvocationReturnsOnlySanitizedSourceDiagnostics(t *testing.T) {
	f := newFixture()
	f.runner.run = func(_ context.Context, runtime *c.ScriptRuntime, _ map[string]json.RawMessage, _ string) (map[string]json.RawMessage, error) {
		return nil, sourceFailure(runtime.Source, []byte(`{"code":"python_error","line":2,"message":"secret exception text"}`))
	}
	w := request(f.h, "POST", "/execution/invocations", "diagnostic", validBody)
	got := invocation(t, w, 201)
	if got.Status != "failed" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Line != 2 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("unsafe or missing diagnostic: %s", w.Body)
	}
}

func TestLegacyCurrentRecordDeliversExplicitModeWithoutChangingReplayHash(t *testing.T) {
	f := newFixture()
	got := invocation(t, request(f.h, "POST", "/execution/invocations", "legacy", validBody), 201)
	f.store.mu.Lock()
	record := f.store.records["tenant:user:legacy"]
	record.Invocation.Mode = ""
	f.store.records["tenant:user:legacy"] = record
	f.store.mu.Unlock()
	for _, w := range []*httptest.ResponseRecorder{
		request(f.h, "GET", "/execution/invocations/"+got.ID, "", ""),
		request(f.h, "POST", "/execution/invocations", "legacy", validBody),
	} {
		out := invocation(t, w, 200)
		if out.Mode != c.InvocationModeCurrent || out.ID != got.ID || f.runner.calls.Load() != 1 {
			t.Fatalf("legacy record changed identity or reran: %s", w.Body)
		}
	}
}
