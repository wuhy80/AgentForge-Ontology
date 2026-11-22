package data

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestMySQLManagedLinkLifecycleFencesWrites(t *testing.T) {
	f, link := newTransactionLinkSQLFixture(t, "1:N")
	invoke := func(command c.ManagedLinkLifecycle) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/internal/definitions/"+link.Definition.ID+"/managed-link-lifecycle",
			bytes.NewReader(mustMutationJSON(t, command)))
		req.Header.Set("Authorization", "Bearer "+f.core.cfg.ServiceToken)
		req.Header.Set("X-Tenant-ID", f.p.TenantID)
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, req)
		return response
	}
	command := c.ManagedLinkLifecycle{Digest: link.ETag}
	preview := command
	preview.Preview = true
	if w := invoke(preview); w.Code != 204 {
		t.Fatalf("preview HTTP %d: %s", w.Code, w.Body)
	}
	ctx := context.Background()
	// A Link writer holding the same gate must drain before storage can pause.
	tx, err := f.engine.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := f.core.gate(ctx, tx, f.p, link); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- invoke(command) }()
	select {
	case w := <-done:
		t.Fatalf("lifecycle bypassed writer lock: HTTP %d", w.Code)
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != 204 {
			t.Fatalf("pause HTTP %d: %s", w.Code, w.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle did not drain")
	}
	f.transaction(mixedLinkRequest(f, link), false, 409)
	if w := invoke(command); w.Code != 204 {
		t.Fatalf("pause replay HTTP %d", w.Code)
	}
	restore := command
	restore.Restore = true
	if w := invoke(restore); w.Code != 204 {
		t.Fatalf("restore HTTP %d: %s", w.Code, w.Body)
	}
	if outcome := f.transaction(mixedLinkRequest(f, link), false, 200); !outcome.Committed {
		t.Fatal("restored relation did not accept a write")
	}
	if w := invoke(command); w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte("definition_data_in_use")) {
		t.Fatalf("stored edge did not block pause: HTTP %d %s", w.Code, w.Body)
	}
}
