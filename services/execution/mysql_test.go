package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func TestMySQLExecutionDurabilityIsolationAndConcurrentClaim(t *testing.T) {
	dsn := os.Getenv("EXECUTION_TEST_DSN")
	if dsn == "" {
		t.Skip("set EXECUTION_TEST_DSN for an isolated real MySQL execution store test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 8, MaxIdle: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := storage.Migrate(ctx, engine.DB(), "execution", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant := "execution-test-" + c.NewID()
	defer func() {
		_, err := engine.DB().ExecContext(context.Background(), "DELETE FROM af_execution_invocations WHERE tenant_id=?", tenant)
		if err != nil {
			t.Error(err)
		}
	}()
	store := &SQLStore{DB: engine.DB()}
	record := Record{TenantID: tenant, SubjectID: "caller", RequestKey: "same", RequestHash: digest("request"), ImageID: "sha256:test", Input: map[string]json.RawMessage{"输入": json.RawMessage(`"9223372036854775807"`)}, DeadlineAt: time.Now().UTC().Add(time.Minute), Invocation: c.Invocation{ID: c.NewID(), DefinitionID: "fn", Kind: c.KindFunction, Status: "running", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), CodeDigest: sourceDigest("code")}}
	var claimed atomic.Int32
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.Claim(ctx, record)
			if err != nil {
				failures <- err
			}
			if ok {
				claimed.Add(1)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if claimed.Load() != 1 {
		t.Fatalf("claimed=%d", claimed.Load())
	}
	// A second repository observes the durable operation, without process cache.
	reopened := &SQLStore{DB: engine.DB()}
	read, err := reopened.FindKey(ctx, tenant, "caller", "same")
	if err != nil || read.Invocation.ID != record.Invocation.ID || string(read.Input["输入"]) != `"9223372036854775807"` {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	for _, scope := range [][2]string{{tenant, "other-user"}, {tenant + "-other", "caller"}} {
		if _, err := reopened.FindID(ctx, scope[0], scope[1], record.Invocation.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("foreign record exposed: %v", err)
		}
	}
	record.Invocation.Status = "succeeded"
	record.Invocation.Output = map[string]json.RawMessage{"result": json.RawMessage(`"9007199254740993.10"`)}
	record.Invocation.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if ok, err := store.Finish(ctx, record); err != nil || !ok {
		t.Fatalf("finish=%v %v", ok, err)
	}
	record.Invocation.Status = "failed"
	record.Invocation.Output = nil
	if ok, err := store.Finish(ctx, record); err != nil || ok {
		t.Fatalf("terminal record overwritten %v %v", ok, err)
	}
	read, err = reopened.FindID(ctx, tenant, "caller", record.Invocation.ID)
	if err != nil || read.Invocation.Status != "succeeded" || string(read.Invocation.Output["result"]) != `"9007199254740993.10"` {
		t.Fatalf("durable terminal record %+v %v", read, err)
	}
	// Persist a validated plan across repository restarts before Data can commit.
	record.RequestKey, record.Invocation.ID = "prepared", c.NewID()
	record.Invocation.Status, record.Invocation.Output = "running", nil
	if ok, err := store.Claim(ctx, record); err != nil || !ok {
		t.Fatalf("claim prepared %v %v", ok, err)
	}
	late := cloneRecord(record)
	record.Invocation.Status = "committing"
	record.Transaction = &c.WriteTransactionRequest{IdempotencyKey: "execution:" + record.Invocation.ID, Operations: []c.TransactionOperation{{SchemaID: "schema", Operation: "create", ClientKey: "a", Values: map[string]json.RawMessage{"field": json.RawMessage(`"value"`)}}}}
	record.PreparedOutput = map[string]json.RawMessage{"result": json.RawMessage(`"done"`)}
	if ok, err := store.Prepare(ctx, record); err != nil || !ok {
		t.Fatalf("prepare %v %v", ok, err)
	}
	late.Invocation.Status = "interrupted"
	if ok, err := store.Finish(ctx, late); err != nil || ok {
		t.Fatalf("late interruption overwrote committing %v %v", ok, err)
	}
	read, err = reopened.FindID(ctx, tenant, "caller", record.Invocation.ID)
	if err != nil || read.Invocation.Status != "committing" || read.Transaction == nil || len(read.PreparedOutput) != 1 {
		t.Fatal("prepared plan did not survive reopen")
	}
	read.Invocation.Status, read.Invocation.Output = "succeeded", read.PreparedOutput
	if ok, err := reopened.Finish(ctx, read); err != nil || !ok {
		t.Fatalf("finish prepared %v %v", ok, err)
	}
	if ok, err := store.Prepare(ctx, record); err != nil || ok {
		t.Fatalf("terminal record re-prepared %v %v", ok, err)
	}
}
