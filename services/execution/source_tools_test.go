package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type languageRunner struct {
	*fakeRunner
	missing map[string]bool
	tools   func(context.Context, *c.ScriptRuntime, string, string) (c.SourceToolResult, error)
}

func (l *languageRunner) ReadyRuntime(ctx context.Context, language string) (string, error) {
	if l.missing[language] {
		return "", errors.New("missing image")
	}
	return l.Ready(ctx)
}
func (l *languageRunner) SourceTools(ctx context.Context, r *c.ScriptRuntime, image, operation string) (c.SourceToolResult, error) {
	if l.tools != nil {
		return l.tools(ctx, r, image, operation)
	}
	return c.SourceToolResult{Source: r.Source, Diagnostics: []c.SourceDiagnostic{}}, nil
}

func languageFixture() (*fixture, *languageRunner) {
	f := newFixture()
	l := &languageRunner{fakeRunner: f.runner, missing: map[string]bool{}}
	f.h = NewHandler(Config{Store: f.store, Metadata: f.meta, Authorizer: f.auth, SessionChecker: f.auth, Runner: l, MaxConcurrent: 1})
	return f, l
}
func sourceToolBody(id string) string {
	return mustJSON(c.SourceToolRequest{DefinitionID: id, Operation: "format", Runtime: &c.ScriptRuntime{Language: "go", Entrypoint: "Main", Source: "package main\nfunc Main(inputs map[string]any)(map[string]any,error){return nil,nil}", TimeoutSeconds: 30}})
}

func TestLanguageCapabilitiesAndDispatchAreIndependent(t *testing.T) {
	f, l := languageFixture()
	l.missing["javascript"] = true
	w := request(f.h, "GET", "/execution/capabilities", "", "")
	var got struct {
		Runtimes, SupportedRuntimes, SourceTools []string
		Invocation                               bool
	}
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || !got.Invocation || strings.Join(got.Runtimes, ",") != "python,go" || len(got.SupportedRuntimes) != 3 || strings.Join(got.SourceTools, ",") != "go" {
		t.Fatalf("capabilities %s", w.Body)
	}
	if w := request(f.h, "GET", "/readyz", "", ""); w.Code != 200 {
		t.Fatalf("one missing language made service unready: %s", w.Body)
	}
	f.fn.Function.Runtime = &c.ScriptRuntime{Language: "javascript", Source: "function main(inputs){return {结果:inputs.输入}}", Entrypoint: "main", TimeoutSeconds: 5}
	f.meta.set(f.fn)
	if w := request(f.h, "POST", "/execution/invocations", "missing-js", validBody); w.Code != 503 || f.runner.calls.Load() != 0 {
		t.Fatalf("missing JS fell back: %d %s", w.Code, w.Body)
	}
	l.missing["javascript"] = false
	for _, language := range []string{"javascript", "go"} {
		f.fn.Function.Runtime.Language = language
		if language == "go" {
			f.fn.Function.Runtime.Entrypoint = "Main"
		}
		f.meta.set(f.fn)
		l.run = func(_ context.Context, r *c.ScriptRuntime, _ map[string]json.RawMessage, _ string) (map[string]json.RawMessage, error) {
			if r.Language != language {
				t.Fatalf("wrong runtime %s", r.Language)
			}
			return map[string]json.RawMessage{"结果": json.RawMessage(`"9007199254740993.10"`)}, nil
		}
		result := invocation(t, request(f.h, "POST", "/execution/invocations", language, validBody), 201)
		if result.Status != "succeeded" {
			t.Fatalf("runtime not dispatched %+v", result)
		}
	}
	l.missing["go"] = true
	w = request(f.h, "GET", "/execution/capabilities", "", "")
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.SourceTools) != 0 {
		t.Fatalf("unavailable Go advertised tools: %s", w.Body)
	}
}

func TestSourceToolsRequireSavedEditableCallableWithoutInvoke(t *testing.T) {
	f, l := languageFixture()
	f.auth.deny("function.invoke", f.fn.ID)
	called := false
	l.tools = func(ctx context.Context, _ *c.ScriptRuntime, _ string, operation string) (c.SourceToolResult, error) {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) < 9*time.Second {
			t.Fatal("unbounded source tools")
		}
		if operation != "format" {
			t.Fatal(operation)
		}
		return c.SourceToolResult{Source: "formatted", Diagnostics: []c.SourceDiagnostic{}}, nil
	}
	if w := request(f.h, "POST", "/execution/source-tools", "", sourceToolBody(f.fn.ID)); w.Code != 200 || !called {
		t.Fatalf("edit-only source tools denied: %d %s", w.Code, w.Body)
	}
	if len(f.store.records) != 0 || f.runner.calls.Load() != 0 {
		t.Fatal("source tool created an invocation")
	}
	f.auth.deny("definition.edit", f.fn.ID)
	called = false
	if w := request(f.h, "POST", "/execution/source-tools", "", sourceToolBody(f.fn.ID)); w.Code != 403 || called {
		t.Fatalf("uneditable source processed: %d %s", w.Code, w.Body)
	}
}

func TestSourceToolsDenyInvalidIdentityAndRecheckDelivery(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fixture, *languageRunner)
		id     string
		status int
	}{
		{"unsaved", func(*fixture, *languageRunner) {}, "", 422},
		{"unknown", func(*fixture, *languageRunner) {}, "unknown", 404},
		{"foreign tenant", func(f *fixture, _ *languageRunner) {
			d := f.fn
			d.TenantID = "other"
			f.meta.definitions["tenant:function"] = d
		}, "function", 404},
		{"schema", func(f *fixture, _ *languageRunner) { f.fn.Kind = c.KindSchema; f.meta.set(f.fn) }, "function", 404},
		{"revoke edit", func(f *fixture, l *languageRunner) {
			l.tools = func(context.Context, *c.ScriptRuntime, string, string) (c.SourceToolResult, error) {
				f.auth.deny("definition.edit", f.fn.ID)
				return c.SourceToolResult{Source: "must not deliver"}, nil
			}
		}, "function", 403},
		{"revoke session", func(f *fixture, l *languageRunner) {
			l.tools = func(context.Context, *c.ScriptRuntime, string, string) (c.SourceToolResult, error) {
				f.auth.loggedOut.Store(true)
				return c.SourceToolResult{Source: "must not deliver"}, nil
			}
		}, "function", 401},
		{"tool error", func(_ *fixture, l *languageRunner) {
			l.tools = func(context.Context, *c.ScriptRuntime, string, string) (c.SourceToolResult, error) {
				return c.SourceToolResult{}, errors.New("private filesystem")
			}
		}, "function", 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, l := languageFixture()
			test.change(f, l)
			w := request(f.h, "POST", "/execution/source-tools", "", sourceToolBody(test.id))
			if w.Code != test.status || strings.Contains(w.Body.String(), "must not deliver") || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("unsafe source tool result %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestSourceToolsShareExecutionSlotsAndAcceptDraft(t *testing.T) {
	f, l := languageFixture()
	f.meta.setDraft(f.fn)
	delete(f.meta.definitions, "tenant:function")
	entered, release := make(chan struct{}), make(chan struct{})
	l.tools = func(context.Context, *c.ScriptRuntime, string, string) (c.SourceToolResult, error) {
		close(entered)
		<-release
		return c.SourceToolResult{Source: "source", Diagnostics: []c.SourceDiagnostic{}}, nil
	}
	done := make(chan int, 1)
	go func() { done <- request(f.h, "POST", "/execution/source-tools", "", sourceToolBody(f.fn.ID)).Code }()
	<-entered
	if w := request(f.h, "POST", "/execution/invocations", "busy", validBody); w.Code != 429 {
		t.Errorf("tools did not reserve invocation slot: %d %s", w.Code, w.Body)
	}
	if w := request(f.h, "POST", "/execution/source-tools", "", sourceToolBody(f.fn.ID)); w.Code != 429 {
		t.Errorf("tools did not share slot: %d %s", w.Code, w.Body)
	}
	close(release)
	if code := <-done; code != 200 {
		t.Fatalf("saved draft not accepted: %d", code)
	}
}
