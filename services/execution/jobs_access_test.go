package execution

import (
	"context"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type jobReadinessRunner struct {
	*fakeRunner
	onReady func()
}

func (r jobReadinessRunner) Ready(ctx context.Context) (string, error) {
	r.onReady()
	return r.fakeRunner.Ready(ctx)
}

func TestJobDoesNotStartAfterDefinitionDeletion(t *testing.T) {
	for _, duringReadiness := range []bool{false, true} {
		name := "before_worker_claim"
		if duringReadiness {
			name = "during_runtime_readiness"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			deleteDefinition := func() {
				f.meta.mu.Lock()
				defer f.meta.mu.Unlock()
				delete(f.meta.definitions, "tenant:function")
			}
			runner := jobReadinessRunner{fakeRunner: f.runner, onReady: deleteDefinition}
			worker := handler{cfg: Config{Authorizer: f.auth, SessionChecker: f.auth, Metadata: f.meta, Runner: runner}}
			record := Record{
				TenantID: "tenant", SubjectID: "user", Root: f.fn, Implementation: f.fn,
				Job:        &jobAdmission{ExpiresAt: time.Now().Add(time.Minute)},
				Invocation: c.Invocation{ID: c.NewID(), Status: "running", Attempt: 1},
			}
			if !duringReadiness {
				deleteDefinition()
			}
			worker.computeJob(context.Background(), &record)
			if f.runner.calls.Load() != 0 {
				t.Fatal("worker ran the deleted definition")
			}
			if record.Invocation.Status != "failed" || record.Invocation.Error == nil || record.Invocation.Error.Code != "authorization_or_definition_changed" {
				t.Fatalf("expected terminal authorization failure, got %s (%v)", record.Invocation.Status, record.Invocation.Error)
			}
		})
	}
}
