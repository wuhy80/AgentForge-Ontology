package data

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

const (
	lifecycleDraftStage  = "6f6f6666-6666-4666-8666-666666666666"
	lifecycleReviewStage = "7a7a7777-7777-4777-8777-777777777777"
	lifecycleDoneStage   = "8b8b8888-8888-4888-8888-888888888888"
	lifecycleSubmitMove  = "9c9c9999-9999-4999-8999-999999999999"
	lifecycleFinishMove  = "adaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

func lifecycleDeclaration() *c.SchemaLifecycle {
	return &c.SchemaLifecycle{InitialStageID: lifecycleDraftStage,
		Stages: []c.LifecycleStage{{ID: lifecycleDraftStage, Name: "草稿"}, {ID: lifecycleReviewStage, Name: "审核中"}, {ID: lifecycleDoneStage, Name: "已完成"}},
		Transitions: []c.LifecycleTransition{
			{ID: lifecycleSubmitMove, Name: "提交审核", From: lifecycleDraftStage, To: lifecycleReviewStage},
			{ID: lifecycleFinishMove, Name: "归档", From: lifecycleReviewStage, To: lifecycleDoneStage},
		}}
}

func lifecycleFixture(t *testing.T) *mutationFixture {
	t.Helper()
	return newMutationFixture(t, func(d *c.Definition) { d.Schema.Lifecycle = lifecycleDeclaration() })
}

func lifecycleTransitionBody(transition, note string) map[string]string {
	body := map[string]string{"transitionId": transition}
	if note != "" {
		body["note"] = note
	}
	return body
}

func (f *mutationFixture) lifecycle(record string, status int) c.RecordLifecycle {
	f.t.Helper()
	return mutationDecode[c.RecordLifecycle](f.t, f.call("GET", "/instances/"+record+"/lifecycle", nil, "", status))
}

func TestMySQLRecordLifecycleTransitions(t *testing.T) {
	f := lifecycleFixture(t)
	row := f.create("LIFECYCLE-1")
	actor := f.p.Subject
	follower := c.NewID()
	for _, subject := range []string{actor, follower} {
		f.p.Subject = subject
		f.call("PUT", "/instances/"+row.ID+"/follow", nil, "", 204)
	}
	revokedFollower := c.NewID()
	f.p.Subject = revokedFollower
	f.call("PUT", "/instances/"+row.ID+"/follow", nil, "", 204)
	f.p.Subject = actor
	authorizer := f.core.cfg.Authorizer
	f.core.cfg.Authorizer = authorizerFunc(func(ctx context.Context, p c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		if p.Subject == revokedFollower && req.Operation == "instance.read" {
			return c.Decision{Allow: false}, nil
		}
		return authorizer.Authorize(ctx, p, req)
	})
	f.handler = NewHandler(f.core.cfg)

	var persisted int
	if err := f.engine.DB().QueryRow("SELECT COUNT(*) FROM af_data_record_lifecycles WHERE tenant_id=?", f.p.TenantID).Scan(&persisted); err != nil || persisted != 1 {
		t.Fatalf("create did not open the initial stage: %d %v", persisted, err)
	}

	view := f.lifecycle(row.ID, 200)
	if view.StageID != lifecycleDraftStage || !view.Persisted || view.StageUnknown ||
		len(view.Stages) != 3 || len(view.Transitions) != 2 || len(view.History) != 0 ||
		!strings.Contains(strings.Join(view.Available, ","), lifecycleSubmitMove) {
		t.Fatalf("unexpected initial lifecycle: %+v", view)
	}

	f.call("POST", "/instances/"+row.ID+"/lifecycle/transitions", lifecycleTransitionBody(c.NewID(), ""), "", 422)
	f.call("POST", "/instances/"+row.ID+"/lifecycle/transitions", lifecycleTransitionBody(lifecycleFinishMove, ""), "", 409)
	f.call("POST", "/instances/"+row.ID+"/lifecycle/transitions",
		lifecycleTransitionBody(lifecycleSubmitMove, strings.Repeat("注", c.MaxLifecycleNoteRunes+1)), "", 422)

	body := mustMutationJSON(t, lifecycleTransitionBody(lifecycleSubmitMove, "review"))
	pathToTransition := "/data/schemas/" + row.SchemaID + "/instances/" + row.ID + "/lifecycle/transitions"
	start := make(chan struct{})
	results := make(chan int, 2)
	for range 2 {
		go func() {
			req := httptest.NewRequest("POST", pathToTransition, bytes.NewReader(body))
			req = req.WithContext(platform.WithPrincipal(req.Context(), f.p))
			w := httptest.NewRecorder()
			<-start
			f.handler.ServeHTTP(w, req)
			results <- w.Code
		}()
	}
	close(start)
	statuses := map[int]int{}
	for range 2 {
		statuses[<-results]++
	}
	if statuses[204] != 1 || statuses[409] != 1 {
		t.Fatalf("concurrent transition results: %v", statuses)
	}

	view = f.lifecycle(row.ID, 200)
	if view.StageID != lifecycleReviewStage || !view.Persisted || len(view.History) != 1 {
		t.Fatalf("transition did not commit one run: %+v", view)
	}
	run := view.History[0]
	var revokedNotices int
	if err := f.engine.DB().QueryRow("SELECT COUNT(*) FROM af_data_record_notices WHERE tenant_id=? AND subject_id=? AND id=?", f.p.TenantID, revokedFollower, run.ID).Scan(&revokedNotices); err != nil || revokedNotices != 0 {
		t.Fatalf("revoked follower received a notice: %d, %v", revokedNotices, err)
	}
	if run.TransitionID != lifecycleSubmitMove || run.Name != "提交审核" || run.Note != "review" ||
		run.From != lifecycleDraftStage || run.To != lifecycleReviewStage || run.At == "" {
		t.Fatalf("history run does not describe the transition: %+v", run)
	}
	inbox := func(method, path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/data/inbox/notifications"+path, nil)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, req.WithContext(platform.WithPrincipal(req.Context(), f.p)))
		if w.Code != status {
			t.Fatalf("inbox HTTP %d, want %d", w.Code, status)
		}
		return w
	}
	list := func() c.RecordNoticePage { return mutationDecode[c.RecordNoticePage](t, inbox("GET", "", 200)) }
	if len(list().Data) != 0 {
		t.Fatal("actor received their own transition")
	}
	f.p.Subject = follower
	page := list()
	if len(page.Data) != 1 || page.Data[0].ID != run.ID || page.Data[0].Kind != lifecycleNoticeKind || page.Unread.Count != 1 {
		t.Fatalf("expected one notification linked to the committed run: %+v", page)
	}
	path := "/" + lifecycleNoticeKind + "/" + row.SchemaID + "/" + row.ID + "/" + run.ID
	inbox("PUT", path, 204)
	inbox("PUT", path, 204)
	if list().Unread.Count != 0 {
		t.Fatal("transition remained unread")
	}
	f.mu.Lock()
	f.policy["instance.read"] = c.Decision{Allow: false}
	f.mu.Unlock()
	if len(list().Data) != 0 {
		t.Fatal("revoked follower can see transition")
	}
	f.mu.Lock()
	delete(f.policy, "instance.read")
	f.mu.Unlock()
	f.p.Subject = actor

	if len(view.Available) != 1 || view.Available[0] != lifecycleFinishMove {
		t.Fatalf("available transitions do not follow the new stage: %+v", view.Available)
	}

	// A stage removed from the definition never silently moves the record: the
	// current stage is reported and further transitions are refused.
	unknown := c.NewID()
	if _, err := f.engine.DB().Exec("UPDATE af_data_record_lifecycles SET stage_id=? WHERE tenant_id=?", unknown, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	view = f.lifecycle(row.ID, 200)
	if view.StageID != unknown || !view.StageUnknown || len(view.Available) != 0 {
		t.Fatalf("removed stage not reported: %+v", view)
	}
	f.call("POST", "/instances/"+row.ID+"/lifecycle/transitions", lifecycleTransitionBody(lifecycleFinishMove, ""), "", 409)
}

func TestMySQLRecordLifecycleNotifiesPendingAssignee(t *testing.T) {
	f := lifecycleFixture(t)
	row := f.create("LIFECYCLE-ASSIGNEE")
	assignee := c.NewID()
	if _, err := f.engine.DB().Exec(`INSERT INTO af_data_record_approvals
 (tenant_id,schema_id,entity_id,id,assignee,submitted_by,submitted_at,state,note)
 VALUES (?,?,?,?,?,?,UTC_TIMESTAMP(6),'pending','')`,
		f.p.TenantID, row.SchemaID, row.ID, c.NewID(), assignee, f.p.Subject); err != nil {
		t.Fatal(err)
	}
	f.call("POST", "/instances/"+row.ID+"/lifecycle/transitions", lifecycleTransitionBody(lifecycleSubmitMove, ""), "", 204)
	run := f.lifecycle(row.ID, 200).History[0]
	var count int
	if err := f.engine.DB().QueryRow("SELECT COUNT(*) FROM af_data_record_notices WHERE tenant_id=? AND subject_id=? AND id=?",
		f.p.TenantID, assignee, run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected one notice for pending assignee: count=%d err=%v", count, err)
	}
	actor := f.p.Subject
	f.p.Subject = assignee
	f.call("PUT", "/instances/"+row.ID+"/follow", nil, "", 204)
	f.p.Subject = actor
	completed := c.NewID()
	if _, err := f.engine.DB().Exec(`INSERT INTO af_data_record_approvals
 (tenant_id,schema_id,entity_id,id,assignee,submitted_by,submitted_at,decided_at,state,note)
 VALUES (?,?,?,?,?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6),'approved','')`,
		f.p.TenantID, row.SchemaID, row.ID, c.NewID(), completed, actor); err != nil {
		t.Fatal(err)
	}
	f.call("POST", "/instances/"+row.ID+"/lifecycle/transitions", lifecycleTransitionBody(lifecycleFinishMove, ""), "", 204)
	next := f.lifecycle(row.ID, 200).History[0]
	var subject string
	if err := f.engine.DB().QueryRow("SELECT COUNT(*),MAX(subject_id) FROM af_data_record_notices WHERE tenant_id=? AND id=?",
		f.p.TenantID, next.ID).Scan(&count, &subject); err != nil || count != 1 || subject != assignee {
		t.Fatalf("expected deduplicated notice excluding completed assignee: count=%d subject=%q err=%v", count, subject, err)
	}
}

func TestMySQLRecordLifecycleNotifiesConfiguredStageAccount(t *testing.T) {
	account := c.NewID()
	f := newMutationFixture(t, func(d *c.Definition) {
		d.Schema.Lifecycle = lifecycleDeclaration()
		d.Schema.Lifecycle.Stages[1].NotifyAccountIDs = []string{account}
	})
	row := f.create("LIFECYCLE-STAGE-ACCOUNT")
	f.call("POST", "/instances/"+row.ID+"/lifecycle/transitions", lifecycleTransitionBody(lifecycleSubmitMove, ""), "", 204)
	view := f.lifecycle(row.ID, 200)
	for _, stage := range view.Stages {
		if len(stage.NotifyAccountIDs) != 0 {
			t.Fatal("record history exposed configured account IDs")
		}
	}
	var count int
	if err := f.engine.DB().QueryRow("SELECT COUNT(*) FROM af_data_record_notices WHERE tenant_id=? AND subject_id=? AND id=?",
		f.p.TenantID, account, view.History[0].ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected one notice for configured account: count=%d err=%v", count, err)
	}
}

func TestMySQLRecordLifecycleNoticeFailurePreservesTransition(t *testing.T) {
	f := lifecycleFixture(t)
	row := f.create("LIFECYCLE-NOTICE-FAILURE")
	actor := f.p.Subject
	originalFollower := c.NewID()
	f.p.Subject = originalFollower
	f.call("PUT", "/instances/"+row.ID+"/follow", nil, "", 204)
	f.p.Subject = actor
	trigger := "lifecycle_notice_" + strings.ReplaceAll(c.NewID(), "-", "")
	if _, err := f.engine.DB().Exec("CREATE TRIGGER " + trigger + " BEFORE INSERT ON af_data_record_notices FOR EACH ROW " +
		"BEGIN IF NEW.entity_id='" + row.ID + "' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture notice failure'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	defer f.engine.DB().Exec("DROP TRIGGER IF EXISTS " + trigger)

	f.call("POST", "/instances/"+row.ID+"/lifecycle/transitions", lifecycleTransitionBody(lifecycleSubmitMove, ""), "", 204)
	view := f.lifecycle(row.ID, 200)
	if view.StageID != lifecycleReviewStage || len(view.History) != 1 || (!view.History[0].NotificationFailed || !view.History[0].NotificationRetryAllowed || view.History[0].NotificationFailureCode != "delivery_failed") {
		t.Fatalf("expected committed transition and visible notification failure: %+v", view)
	}
	runID := view.History[0].ID
	retry := "/instances/" + row.ID + "/lifecycle/runs/" + runID + "/retry-notification"
	f.call("POST", retry, nil, "", 500)
	if _, err := f.engine.DB().Exec("DROP TRIGGER " + trigger); err != nil {
		t.Fatal(err)
	}
	f.p.Subject = c.NewID()
	f.call("POST", retry, nil, "", 404)
	f.p.Subject = originalFollower
	f.call("DELETE", "/instances/"+row.ID+"/follow", nil, "", 204)
	newFollower := c.NewID()
	f.p.Subject = newFollower
	f.call("PUT", "/instances/"+row.ID+"/follow", nil, "", 204)
	f.p.Subject = actor
	f.mu.Lock()
	f.policy["instance.patch"] = c.Decision{Allow: false}
	f.mu.Unlock()
	f.call("POST", retry, nil, "", 403)
	view = f.lifecycle(row.ID, 200)
	if len(view.History) != 1 || !view.History[0].NotificationFailed || view.History[0].NotificationRetryAllowed {
		t.Fatalf("denied retry changed the failure state: %+v", view)
	}
	f.mu.Lock()
	delete(f.policy, "instance.patch")
	f.mu.Unlock()
	f.p.Subject = actor
	f.call("POST", retry, nil, "", 204)
	f.call("POST", retry, nil, "", 204)
	view = f.lifecycle(row.ID, 200)
	if view.StageID != lifecycleReviewStage || len(view.History) != 1 || view.History[0].NotificationFailed || view.History[0].NotificationFailureCode != "" {
		t.Fatalf("retry should only clear the failure: %+v", view)
	}
	var count int
	var recipient string
	if err := f.engine.DB().QueryRow("SELECT COUNT(*), MAX(subject_id) FROM af_data_record_notices WHERE tenant_id=? AND id=?", f.p.TenantID, runID).Scan(&count, &recipient); err != nil || count != 1 || recipient != newFollower {
		t.Fatalf("expected one notification after retry: count=%d recipient=%q err=%v", count, recipient, err)
	}
}

func TestMySQLRecordLifecycleRecipientAuthorizationFailureRollsBackNotices(t *testing.T) {
	f := lifecycleFixture(t)
	row := f.create("LIFECYCLE-RECIPIENT-FAILURE")
	actor := f.p.Subject
	first, second := c.NewID(), c.NewID()
	if first > second {
		first, second = second, first
	}
	for _, subject := range []string{first, second} {
		f.p.Subject = subject
		f.call("PUT", "/instances/"+row.ID+"/follow", nil, "", 204)
	}
	f.p.Subject = actor
	config := f.core.cfg
	authorizer := config.Authorizer
	var checkedSecond atomic.Bool
	revoked := false
	waitForDeliveryDeadline := true
	config.Authorizer = authorizerFunc(func(ctx context.Context, p c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		if p.Subject == second && req.Operation == "instance.read" {
			checkedSecond.Store(true)
		}
		if p.Subject == first && req.Operation == "instance.read" && revoked {
			return c.Decision{Allow: false}, nil
		}
		if checkedSecond.Load() && p.Subject == first && req.Operation == "instance.read" {
			if waitForDeliveryDeadline {
				select {
				case <-ctx.Done():
					return c.Decision{}, ctx.Err()
				case <-time.After(5 * time.Second):
					t.Error("notification authorization exceeded its delivery deadline")
				}
			}
			return c.Decision{}, context.DeadlineExceeded
		}
		return authorizer.Authorize(ctx, p, req)
	})
	f.handler = NewHandler(config)
	f.call("POST", "/instances/"+row.ID+"/lifecycle/transitions", lifecycleTransitionBody(lifecycleSubmitMove, ""), "", 204)
	view := f.lifecycle(row.ID, 200)
	if view.StageID != lifecycleReviewStage || len(view.History) != 1 || !view.History[0].NotificationRetryAllowed || view.History[0].NotificationFailureCode != "authorization_unavailable" {
		t.Fatalf("expected committed transition with retry available: %+v", view)
	}
	run := view.History[0].ID
	countNotices := func(want int) {
		t.Helper()
		var count int
		if err := f.engine.DB().QueryRow("SELECT COUNT(*) FROM af_data_record_notices WHERE tenant_id=? AND id=?", f.p.TenantID, run).Scan(&count); err != nil || count != want {
			t.Fatalf("expected %d notices, got %d: %v", want, count, err)
		}
	}
	countNotices(0)
	waitForDeliveryDeadline = false
	retry := "/instances/" + row.ID + "/lifecycle/runs/" + run + "/retry-notification"
	f.call("POST", retry, nil, "", 503)
	countNotices(0)
	revoked = true
	worker := &handler{cfg: config, client: f.core.client}
	setRunTime := func(hoursAgo int) {
		t.Helper()
		if _, err := f.engine.DB().Exec("UPDATE af_data_record_lifecycle_runs SET created_at=UTC_TIMESTAMP(6)-INTERVAL ? HOUR WHERE tenant_id=? AND id=?", hoursAgo, f.p.TenantID, run); err != nil {
			t.Fatal(err)
		}
	}
	setRunTime(25)
	if err := worker.retryLifecycleBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	countNotices(0)
	if !f.lifecycle(row.ID, 200).History[0].NotificationRetryAllowed {
		t.Fatal("expired automatic retry should preserve manual retry")
	}
	setRunTime(0)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		worker := &handler{cfg: config, client: f.core.client}
		go func() {
			<-start
			results <- worker.retryLifecycleBatch(context.Background())
		}()
	}
	close(start)
	var scanErrors []error
	for range 2 {
		if err := <-results; err != nil {
			scanErrors = append(scanErrors, err)
		}
	}
	if len(scanErrors) > 0 {
		t.Fatalf("concurrent scans failed: %v", scanErrors)
	}
	countNotices(1)
	if f.lifecycle(row.ID, 200).History[0].NotificationFailed {
		t.Fatal("worker did not clear the delivery failure")
	}
	var recipient string
	if err := f.engine.DB().QueryRow("SELECT subject_id FROM af_data_record_notices WHERE tenant_id=? AND id=?", f.p.TenantID, run).Scan(&recipient); err != nil || recipient != second {
		t.Fatalf("retry delivered to unexpected recipient: %q, %v", recipient, err)
	}
}

func TestMySQLRecordLifecycleAuthorizationAndCleanup(t *testing.T) {
	f := lifecycleFixture(t)
	row := f.create("LIFECYCLE-2")
	path := "/instances/" + row.ID + "/lifecycle/transitions"

	f.mu.Lock()
	f.policy["instance.patch"] = c.Decision{Allow: false}
	f.mu.Unlock()
	view := f.lifecycle(row.ID, 200)
	if len(view.Available) != 0 {
		t.Fatalf("read-only account saw available transitions: %+v", view.Available)
	}
	f.call("POST", path, lifecycleTransitionBody(lifecycleSubmitMove, ""), "", 403)
	f.mu.Lock()
	delete(f.policy, "instance.patch")
	f.mu.Unlock()

	f.mu.Lock()
	f.policy["instance.read"] = c.Decision{Allow: false}
	f.mu.Unlock()
	f.lifecycle(row.ID, 403)
	f.mu.Lock()
	delete(f.policy, "instance.read")
	f.mu.Unlock()

	// Records created before the lifecycle existed stay on the declared initial
	// stage until the first transition opens their row.
	if _, err := f.engine.DB().Exec("DELETE FROM af_data_record_lifecycles WHERE tenant_id=?", f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	view = f.lifecycle(row.ID, 200)
	if view.Persisted || view.StageID != lifecycleDraftStage {
		t.Fatalf("legacy record did not report the initial stage: %+v", view)
	}
	f.call("POST", path, lifecycleTransitionBody(lifecycleSubmitMove, ""), "", 204)
	if view = f.lifecycle(row.ID, 200); !view.Persisted || view.StageID != lifecycleReviewStage {
		t.Fatalf("lazy stage row was not committed: %+v", view)
	}

	f.call("DELETE", "/instances/"+row.ID, nil, row.ETag, 204)
	for _, table := range []string{"af_data_record_lifecycles", "af_data_record_lifecycle_runs"} {
		var remaining int
		if err := f.engine.DB().QueryRow("SELECT COUNT(*) FROM "+table+" WHERE tenant_id=?", f.p.TenantID).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("%s not cleaned up on delete: %d %v", table, remaining, err)
		}
	}
}

func TestMySQLRecordLifecycleRequiresDeclaration(t *testing.T) {
	f := newMutationFixture(t)
	row := f.create("LIFECYCLE-3")
	f.lifecycle(row.ID, 404)
	f.call("POST", "/instances/"+row.ID+"/lifecycle/transitions", lifecycleTransitionBody(lifecycleSubmitMove, ""), "", 404)
}
