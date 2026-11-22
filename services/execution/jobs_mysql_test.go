package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

type jobTestAuthority struct {
	*testAuthority
	tenant      string
	unavailable *atomic.Bool
}

func (a jobTestAuthority) CheckSession(ctx context.Context, check c.SessionCheck) (c.Session, error) {
	if check.SessionFingerprint != "" && a.unavailable.Load() {
		return c.Session{}, &platform.AccessError{Status: 503}
	}
	session, err := a.testAuthority.CheckSession(ctx, check)
	if check.SessionFingerprint != "" && check.SessionFingerprint != sourceDigest(strings.Repeat("s", 64)) {
		return c.Session{}, errors.New("wrong session fingerprint")
	}
	session.User.TenantID = a.tenant
	return session, err
}

func TestMySQLJobLifecycle(t *testing.T) {
	dsn := os.Getenv("EXECUTION_TEST_DSN")
	if dsn == "" {
		t.Skip("set EXECUTION_TEST_DSN for isolated job lifecycle")
	}
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 8, MaxIdle: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := storage.Migrate(ctx, engine.DB(), "execution", Migrations); err != nil {
		t.Fatal(err)
	}
	f := newFixture()
	tenant := "jobs-" + c.NewID()
	defer func() {
		_, _ = engine.DB().Exec("DELETE FROM af_execution_invocations WHERE tenant_id=?", tenant)
		_, _ = engine.DB().Exec("DELETE FROM af_execution_job_admissions WHERE tenant_id=?", tenant)
	}()
	f.fn.TenantID = tenant
	f.meta.set(f.fn)
	store := &SQLStore{DB: engine.DB()}
	var unavailable atomic.Bool
	auth := jobTestAuthority{testAuthority: f.auth, tenant: tenant, unavailable: &unavailable}
	// A stopped initial worker allows deterministic testing of enqueue and
	// lease expiry before the replacement worker begins.
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	cfg := Config{WorkerContext: stopped, Store: store, Metadata: f.meta, Authorizer: auth, SessionChecker: auth, Runner: f.runner, MaxConcurrent: 2}
	app := NewHandler(cfg)
	call := func(method, path, key, body, requestTenant string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://workbench.test"+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: platform.SessionCookieName, Value: strings.Repeat("s", 64)})
		r.Header.Set("Origin", "http://workbench.test")
		r.Header.Set("X-CSRF-Token", strings.Repeat("c", 64))
		r.Header.Set("Idempotency-Key", key)
		r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{TenantID: requestTenant, Subject: "user", ExpiresAt: time.Now().Add(time.Hour).Unix()}))
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	const collection = "/execution/invocations"
	body := strings.Replace(validBody, `{"definitionId"`, `{"async":true,"definitionId"`, 1)
	job := invocation(t, call("POST", collection, "same-job", body, tenant), 202)
	path := collection + "/" + job.ID
	if job.Status != "queued" || invocation(t, call("POST", collection, "same-job", body, tenant), 202).ID != job.ID {
		t.Fatal("idempotent task admission did not return the same queued task")
	}
	if w := call("POST", collection, "same-job", validBody, tenant); w.Code != 409 {
		t.Fatalf("same key switched execution mode: %d", w.Code)
	}
	var winners []Record
	var mutex sync.Mutex
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			lease, err := store.leaseJob(ctx)
			if errors.Is(err, sql.ErrNoRows) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			mutex.Lock()
			winners = append(winners, lease)
			mutex.Unlock()
		})
	}
	workers.Wait()
	if len(winners) != 1 || winners[0].Invocation.ID != job.ID || winners[0].Invocation.Attempt != 1 {
		t.Fatalf("unexpected lease winners: %d", len(winners))
	}
	if _, err := store.cancelJob(ctx, tenant+"-other", "user", job.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant cancel: %v", err)
	}
	if _, err := engine.DB().Exec("UPDATE af_execution_invocations SET deadline_at=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE tenant_id=? AND id=?", tenant, job.ID); err != nil {
		t.Fatal(err)
	}
	old := winners[0]
	closeJob(&old, "succeeded", "", "")
	if err := store.finishJob(ctx, old); err != nil {
		t.Fatal(err)
	}
	if state := invocation(t, call("GET", path, "", "", tenant), 202); state.Status != "running" {
		t.Fatalf("expired attempt published a result: %s", state.Status)
	}
	cfg.WorkerContext, stop = context.WithCancel(ctx)
	defer stop()
	app = NewHandler(cfg)
	for deadline := time.Now().Add(8 * time.Second); ; {
		response := call("GET", path, "", "", tenant)
		if response.Code == 200 {
			state := invocation(t, response, 200)
			if state.Status != "succeeded" || state.Attempt != 2 || string(state.Output["fn-out"]) != `"9007199254740993.10"` {
				t.Fatalf("unexpected recovered task: %+v", state)
			}
			break
		}
		if response.Code != 202 || time.Now().After(deadline) {
			t.Fatalf("recovered task did not complete: %d %s", response.Code, response.Body)
		}
		time.Sleep(25 * time.Millisecond)
	}
	stop()
	cfg.WorkerContext = stopped
	app = NewHandler(cfg)
	job = invocation(t, call("POST", collection, "cancel-job", body, tenant), 202)
	running, err := store.leaseJob(ctx)
	if err != nil || running.Invocation.ID != job.ID {
		t.Fatalf("claim cancel fixture: %v", err)
	}
	path = collection + "/" + job.ID
	for range 2 {
		if state := invocation(t, call("DELETE", path, "", "", tenant), 200); state.Status != "canceled" {
			t.Fatalf("cancelled task status: %s", state.Status)
		}
	}
	closeJob(&running, "succeeded", "", "")
	if err := store.finishJob(ctx, running); err != nil {
		t.Fatal(err)
	}
	if state := invocation(t, call("GET", path, "", "", tenant), 200); state.Status != "canceled" || f.runner.calls.Load() != 1 {
		t.Fatal("cancelled task was overwritten")
	}
	job = invocation(t, call("POST", collection, "revoked-job", body, tenant), 202)
	running, err = store.leaseJob(ctx)
	if err != nil || running.Invocation.ID != job.ID {
		t.Fatalf("claim revoked fixture: %v", err)
	}
	f.auth.loggedOut.Store(true)
	worker := handler{cfg: cfg, jobs: store}
	worker.runJob(ctx, running)
	f.auth.loggedOut.Store(false)
	path = collection + "/" + job.ID
	state := invocation(t, call("GET", path, "", "", tenant), 200)
	if state.Status != "failed" || state.Error.Code != "authorization_or_definition_changed" || f.runner.calls.Load() != 1 {
		t.Fatalf("revoked session ran a job: %+v", state)
	}
	job = invocation(t, call("POST", collection, "stop-running-job", body, tenant), 202)
	running, err = store.leaseJob(ctx)
	if err != nil || running.Invocation.ID != job.ID {
		t.Fatalf("claim active cancellation fixture: %v", err)
	}
	started, done := make(chan struct{}), make(chan struct{})
	worker.cfg.Runner = &fakeRunner{run: func(ctx context.Context, _ *c.ScriptRuntime, _ map[string]json.RawMessage, _ string) (map[string]json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	go func() {
		defer close(done)
		worker.runJob(ctx, running)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("Worker did not start the cancellation fixture")
	}
	path = collection + "/" + job.ID
	invocation(t, call("DELETE", path, "", "", tenant), 200)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not interrupt the Worker")
	}
	if state := invocation(t, call("GET", path, "", "", tenant), 200); state.Status != "canceled" {
		t.Fatalf("unexpected status after active cancellation: %s", state.Status)
	}
	for range 32 {
		invocation(t, call("POST", collection, c.NewID(), body, tenant), 202)
	}
	if w := call("POST", collection, c.NewID(), body, tenant); w.Code != 429 {
		t.Fatalf("queue limit: %d %s", w.Code, w.Body)
	}
	if state := invocation(t, call("POST", collection, "cancel-job", body, tenant), 200); state.Status != "canceled" {
		t.Fatal("queue limit blocked a valid idempotent replay")
	}
	var page c.InvocationPage
	response := call("GET", collection, "", "", tenant)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Data) != 20 || page.NextCursor == "" {
		t.Fatalf("invalid first job page: %d %s", response.Code, response.Body)
	}
	var next c.InvocationPage
	response = call("GET", collection+"?cursor="+page.NextCursor, "", "", tenant)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &next) != nil || len(next.Data) == 0 || next.Data[0].ID == page.Data[19].ID {
		t.Fatalf("invalid continuation: %d %s", response.Code, response.Body)
	}
	if foreign, err := store.listJobs(ctx, tenant, "another-user", "9999", "", 20); err != nil || len(foreign) != 0 {
		t.Fatalf("foreign job history exposed: %v", err)
	}
	if _, err := engine.DB().Exec("UPDATE af_execution_invocations SET job_purge_at=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE tenant_id=? AND status='canceled'", tenant); err != nil {
		t.Fatal(err)
	}
	if err := store.purgeJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindKey(ctx, tenant, "user", "cancel-job"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired task and idempotency key survived retention: %v", err)
	}
	leased, err := store.leaseJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unavailable.Store(true)
	worker.runJob(ctx, leased)
	unavailable.Store(false)
	retried, err := store.FindID(ctx, tenant, "user", leased.Invocation.ID)
	if err != nil || retried.Invocation.Status != "queued" || retried.Invocation.Error == nil || retried.Invocation.Error.Code != "job_authority_unavailable" {
		t.Fatalf("transient authority failure was not rescheduled: %+v %v", retried.Invocation, err)
	}
	var scheduled bool
	if err := engine.DB().QueryRow("SELECT deadline_at>UTC_TIMESTAMP(6) FROM af_execution_invocations WHERE tenant_id=? AND id=?", tenant, leased.Invocation.ID).Scan(&scheduled); err != nil || !scheduled {
		t.Fatalf("retry was not delayed: %v", err)
	}
	if _, err := engine.DB().Exec("UPDATE af_execution_invocations SET deadline_at=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE tenant_id=? AND id=?", tenant, leased.Invocation.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.leaseJob(ctx)
	if err != nil || recovered.Invocation.ID != leased.Invocation.ID || recovered.Invocation.Attempt != 2 {
		t.Fatalf("retry could not be claimed after its delay: %v", err)
	}
	worker.cfg.Runner = f.runner
	worker.runJob(ctx, recovered)
	state = invocation(t, call("GET", collection+"/"+leased.Invocation.ID, "", "", tenant), 200)
	if state.Status != "succeeded" || state.Error != nil {
		t.Fatalf("recovered attempt retained an error: %+v", state)
	}
}
