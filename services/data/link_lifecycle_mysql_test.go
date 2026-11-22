package data

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestMySQLManagedSchemaLifecycle(t *testing.T) {
	f := newMutationFixture(t)
	id := f.current.Definition.ID
	invoke := func(restore, preview bool, expected int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/internal/definitions/"+id+"/managed-storage-lifecycle",
			bytes.NewReader(mustMutationJSON(t, c.ManagedLinkLifecycle{Digest: f.current.ETag, Restore: restore, Preview: preview})))
		req.Header.Set("Authorization", "Bearer "+f.core.cfg.ServiceToken)
		req.Header.Set("X-Tenant-ID", f.p.TenantID)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, req)
		if w.Code != expected {
			t.Errorf("lifecycle HTTP %d, want %d: %s", w.Code, expected, w.Body)
		}
		return w
	}
	invoke(false, true, 204)
	ctx := context.Background()
	tx, err := f.engine.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	query := c.QuerySpec{From: c.Source{SchemaID: id}}
	if err := f.core.lockSavedQueryDefinitions(httptest.NewRequest("POST", "/data/saved-queries", nil), tx, f.p.TenantID, query); err != nil {
		t.Fatal(err)
	}
	queryID := c.NewID()
	template := mustMutationJSON(t, c.SavedQueryInput{Query: query})
	if _, err := tx.ExecContext(ctx, `INSERT INTO af_data_saved_queries
 (tenant_id,user_id,id,name,name_key,template_json,etag,created_at,updated_at)
 VALUES (?,?,?,?,?,?,?,NOW(6),NOW(6))`, f.p.TenantID, f.p.Subject, queryID, "saved", "saved", template, f.current.ETag); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- invoke(false, false, 409) }()
	select {
	case <-done:
		t.Fatal("schema pause bypassed the writer gate")
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if !bytes.Contains(w.Body.Bytes(), []byte("definition_saved_query_in_use")) {
			t.Fatalf("pause missed newly committed saved query: %s", w.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("schema pause did not proceed after writer released the gate")
	}
	if _, err := f.engine.DB().ExecContext(ctx, "DELETE FROM af_data_saved_queries WHERE tenant_id=? AND user_id=? AND id=?", f.p.TenantID, f.p.Subject, queryID); err != nil {
		t.Fatal(err)
	}
	invoke(false, true, 204)
	invoke(false, false, 204)
	f.call("POST", "/instances", c.WriteRequest{IdempotencyKey: c.NewID(), Values: mutationValues("blocked")}, "", 409)
	invoke(true, false, 204)
	f.create("restored")
	invoke(false, true, 409)
	invoke(false, false, 409)
}

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
	// A saved row join has a direct Link dependency even while the table is empty.
	queryID := c.NewID()
	template := mustMutationJSON(t, c.SavedQueryInput{Query: c.QuerySpec{
		From: c.Source{SchemaID: link.Definition.Link.SourceSchemaID},
		Rows: &c.RowQuery{
			Nodes: []c.RowQueryNode{
				{ID: "n0", SchemaID: link.Definition.Link.SourceSchemaID, Alias: "source"},
				{ID: "n1", SchemaID: link.Definition.Link.TargetSchemaID, Alias: "target"},
			},
			Joins: []c.RowQueryJoin{{Type: "inner", From: "n0", To: "n1", LinkID: link.Definition.ID, Direction: "out"}},
		},
	}})
	if _, err := f.engine.DB().ExecContext(ctx, `INSERT INTO af_data_saved_queries
 (tenant_id,user_id,id,name,name_key,template_json,etag,created_at,updated_at)
 VALUES (?,?,?,?,?,?,?,NOW(6),NOW(6))`, f.p.TenantID, f.p.Subject, queryID, "join", "join", template, link.ETag); err != nil {
		t.Fatal(err)
	}
	removeSaved := func() {
		if _, err := f.engine.DB().ExecContext(ctx, "DELETE FROM af_data_saved_queries WHERE tenant_id=? AND user_id=? AND id=?", f.p.TenantID, f.p.Subject, queryID); err != nil {
			t.Error(err)
		}
	}
	defer removeSaved()
	for _, check := range []c.ManagedLinkLifecycle{preview, command} {
		if w := invoke(check); w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte("definition_saved_query_in_use")) {
			t.Fatalf("saved join did not block pause (preview=%t): HTTP %d %s", check.Preview, w.Code, w.Body)
		}
	}
	removeSaved()
	if w := invoke(preview); w.Code != 204 {
		t.Fatalf("preview after removing saved join: HTTP %d %s", w.Code, w.Body)
	}
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
