package execution

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func externalInvocationRequest(h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://workbench.test"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer mcp_"+strings.Repeat("a", 64))
	r.Header.Set("Idempotency-Key", key)
	r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{TenantID: "tenant", Subject: "user", ExpiresAt: time.Now().Add(time.Hour).Unix()}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestExternalConnectionRejectsDraftExecutionReplayAndResults(t *testing.T) {
	f := newFixture()
	f.meta.setDraft(f.fn)
	body := draftBody(f.fn)
	denied := externalInvocationRequest(f.h, "POST", "/execution/invocations", "external-draft", body)
	if denied.Code != 403 || f.runner.calls.Load() != 0 || len(f.store.records) != 0 {
		t.Fatalf("external connection admitted a draft: status=%d calls=%d", denied.Code, f.runner.calls.Load())
	}
	// The same user may run the draft in the workbench. That permission must
	// not let a narrower external credential replay or read its output.
	draft := invocation(t, request(f.h, "POST", "/execution/invocations", "workbench-draft", body), 201)
	for _, result := range []*httptest.ResponseRecorder{
		externalInvocationRequest(f.h, "POST", "/execution/invocations", "workbench-draft", body),
		externalInvocationRequest(f.h, "GET", "/execution/invocations/"+draft.ID, "", ""),
	} {
		if result.Code != 403 || strings.Contains(result.Body.String(), "9007199254740993") {
			t.Fatalf("external connection received draft output: status=%d", result.Code)
		}
	}
	current := invocation(t, externalInvocationRequest(f.h, "POST", "/execution/invocations", "external-current", validBody), 201)
	read := invocation(t, externalInvocationRequest(f.h, "GET", "/execution/invocations/"+current.ID, "", ""), 200)
	if read.ID != current.ID || read.Mode != c.InvocationModeCurrent || f.runner.calls.Load() != 2 {
		t.Fatal("current execution changed while enforcing the draft boundary")
	}
}
