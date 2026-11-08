package data

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

type platformMigrationFixture struct {
	t       *testing.T
	engine  *storage.Engine
	ctx     context.Context
	d       c.Definition
	current c.CurrentDefinition
	mu      sync.RWMutex
	h       http.Handler
	secret  string
}

func newPlatformMigrationFixture(t *testing.T) *platformMigrationFixture {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set; real platform schema migration not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 8, MaxIdle: 3})
	if err != nil {
		t.Fatal("connect isolated MySQL failed")
	}
	t.Cleanup(func() { _ = engine.Close() })
	if err = storage.Migrate(ctx, engine.DB(), "data", Migrations); err != nil {
		t.Fatal(err)
	}
	d := testDefinition()
	d.ID, d.TenantID, d.CategoryID, d.GroupID = c.NewID(), c.NewID(), c.NewID(), c.NewID()
	d.Schema.Properties[1].Precision = 20
	f := &platformMigrationFixture{t: t, engine: engine, ctx: ctx, d: d, current: c.CurrentDefinition{Definition: d, ETag: digest(d), Status: "active"}, secret: strings.Repeat("m", 32)}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = engine.DB().ExecContext(cleanup, "DROP TABLE IF EXISTS "+quote(tableName(d.ID)))
		for _, table := range []string{"af_data_idempotency", "af_data_write_receipts", "af_data_outbox", "af_data_schema_migrations", "af_data_gates"} {
			_, _ = engine.DB().ExecContext(cleanup, "DELETE FROM "+table+" WHERE tenant_id=?", d.TenantID)
		}
	})
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.RLock()
		defer f.mu.RUnlock()
		if r.Header.Get("X-Tenant-ID") != d.TenantID {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, f.current)
	}))
	t.Cleanup(meta.Close)
	f.h = NewHandler(Config{Engine: engine, MetadataURL: meta.URL, ServiceToken: f.secret, CursorSecret: strings.Repeat("c", 32), Authorizer: authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		return c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: "migration-test"}, nil
	})})
	req := c.PrepareRequest{OperationID: c.NewID(), Definition: d, Digest: digest(d)}
	f.expect(f.call("POST", "/internal/publications/prepare", req, ""), 200)
	f.activate(req)
	return f
}

func (f *platformMigrationFixture) call(method, path string, body any, etag string) *httptest.ResponseRecorder {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req = req.WithContext(platform.WithPrincipal(req.Context(), c.Principal{TenantID: f.d.TenantID, Subject: "migration-author"}))
	if strings.HasPrefix(path, "/internal/") {
		req.Header.Set("Authorization", "Bearer "+f.secret)
		req.Header.Set("X-Tenant-ID", f.d.TenantID)
	}
	if etag != "" {
		req.Header.Set("If-Match", `"`+etag+`"`)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *platformMigrationFixture) expect(rec *httptest.ResponseRecorder, status int) {
	f.t.Helper()
	if rec.Code != status {
		f.t.Fatalf("HTTP %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
}

func (f *platformMigrationFixture) activate(req c.PrepareRequest) {
	f.t.Helper()
	f.mu.Lock()
	f.current = c.CurrentDefinition{Definition: req.Definition, ETag: req.Digest, Status: "active"}
	f.mu.Unlock()
	f.expect(f.call("POST", "/internal/publications/activate", c.ActivateRequest{OperationID: req.OperationID, DefinitionID: req.Definition.ID, Digest: req.Digest}, ""), 200)
}

func (f *platformMigrationFixture) create(code, key string) (c.Instance, c.WriteRequest) {
	f.t.Helper()
	body := c.WriteRequest{IdempotencyKey: key, Values: map[string]json.RawMessage{testFieldCode: raw(code), testFieldDept: raw("D1"), testFieldPrice: raw("9007199254740993.001")}}
	rec := f.call("POST", "/data/schemas/"+f.d.ID+"/instances", body, "")
	f.expect(rec, 201)
	var out c.Instance
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		f.t.Fatal("invalid created instance")
	}
	return out, body
}

func cloneMigrationDefinition(d c.Definition) c.Definition {
	b, _ := json.Marshal(d)
	var out c.Definition
	_ = json.Unmarshal(b, &out)
	return out
}

func TestMySQLPlatformSchemaAdditionsAndPreflight(t *testing.T) {
	f := newPlatformMigrationFixture(t)
	first, original := f.create("A", "first")
	f.create("B", "second")
	path := "/data/schemas/" + f.d.ID + "/instances"
	checkReady := func() {
		t.Helper()
		var status, got string
		if err := f.engine.DB().QueryRowContext(f.ctx, "SELECT status,digest FROM af_data_gates WHERE tenant_id=? AND definition_id=?", f.d.TenantID, f.d.ID).Scan(&status, &got); err != nil || status != "ready" || got != digest(f.d) {
			t.Fatalf("preflight changed live admission: %s %s %v", status, got, err)
		}
	}
	for _, change := range []func(*c.Definition){
		func(d *c.Definition) {
			d.Schema.Properties = append(d.Schema.Properties, c.Property{ID: c.NewID(), Name: "缺省必填", DataType: "string"})
		},
		func(d *c.Definition) { d.Schema.Properties[0].DataType = "uuid" },
		func(d *c.Definition) {
			d.Schema.Indexes = append(d.Schema.Indexes, c.Index{ID: c.NewID(), Name: "部门错误唯一", Unique: true, FieldIDs: []string{testFieldDept}})
		},
		func(d *c.Definition) {
			p := c.Property{ID: c.NewID(), Name: "相同默认值", DataType: "string", Default: raw("same")}
			d.Schema.Properties = append(d.Schema.Properties, p)
			d.Schema.Indexes = append(d.Schema.Indexes, c.Index{ID: c.NewID(), Name: "默认值错误唯一", Unique: true, FieldIDs: []string{p.ID}})
		},
	} {
		bad := cloneMigrationDefinition(f.d)
		change(&bad)
		req := c.PrepareRequest{OperationID: c.NewID(), Definition: bad, PreviousDigest: digest(f.d), Digest: digest(bad)}
		f.expect(f.call("POST", "/internal/publications/preview", req, ""), 422)
		f.expect(f.call("POST", "/internal/publications/prepare", req, ""), 422)
		checkReady()
	}
	next := cloneMigrationDefinition(f.d)
	textField := c.Property{ID: c.NewID(), Name: "新说明", DataType: "string", Nullable: true, Default: raw("初始'); DROP TABLE secrets; --")}
	boolField := c.Property{ID: c.NewID(), Name: "新开关", DataType: "boolean", Required: true, Default: raw(false)}
	decimalField := c.Property{ID: c.NewID(), Name: "精确默认", DataType: "decimal", Precision: 20, Scale: 4, Default: raw("9007199254740993.0001")}
	nullField := c.Property{ID: c.NewID(), Name: "空值", DataType: "string", Nullable: true}
	next.Schema.Properties = append(next.Schema.Properties, textField, boolField, decimalField, nullField)
	next.Schema.Indexes = []c.Index{{ID: c.NewID(), Name: "编码唯一", Unique: true, FieldIDs: []string{testFieldCode}}, {ID: c.NewID(), Name: "开关索引", FieldIDs: []string{boolField.ID}}}
	req := c.PrepareRequest{OperationID: c.NewID(), Definition: next, PreviousDigest: digest(f.d), Digest: digest(next)}
	preview := f.call("POST", "/internal/publications/preview", req, "")
	f.expect(preview, 200)
	checkReady()
	if err := verifyTable(f.ctx, f.engine.DB(), f.d); err != nil {
		t.Fatalf("preview executed DDL: %v", err)
	}
	prepared := f.call("POST", "/internal/publications/prepare", req, "")
	f.expect(prepared, 200)
	var previewResult, prepareResult c.PrepareResult
	_ = json.Unmarshal(preview.Body.Bytes(), &previewResult)
	_ = json.Unmarshal(prepared.Body.Bytes(), &prepareResult)
	if !reflect.DeepEqual(previewResult.Statements, prepareResult.Statements) {
		t.Fatal("preview and applied migration plans differ")
	}
	f.expect(f.call("GET", path+"/"+first.ID, nil, ""), 409)
	f.activate(req)
	rec := f.call("GET", path+"/"+first.ID, nil, "")
	f.expect(rec, 200)
	var after c.Instance
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if after.ID != first.ID || after.ETag == first.ETag || string(after.Values[textField.ID]) != string(textField.Default) || string(after.Values[boolField.ID]) != "false" || string(after.Values[decimalField.ID]) != `"9007199254740993.0001"` || string(after.Values[nullField.ID]) != "null" || string(after.Values[testFieldPrice]) != string(first.Values[testFieldPrice]) {
		t.Fatalf("migration lost identity, precision or backfill: %s", rec.Body.String())
	}
	f.expect(f.call("PATCH", path+"/"+first.ID, c.WriteRequest{Values: map[string]json.RawMessage{testFieldCode: raw("stale")}}, first.ETag), 412)
	f.expect(f.call("POST", path, original, ""), 409)
	f.expect(f.call("POST", "/internal/publications/prepare", req, ""), 200)
	rec = f.call("GET", path+"/"+first.ID, nil, "")
	f.expect(rec, 200)
	var repeated c.Instance
	_ = json.Unmarshal(rec.Body.Bytes(), &repeated)
	if repeated.ETag != after.ETag {
		t.Fatal("completed migration rotated ETag again")
	}
	created, _ := f.create("C", "new-defaults")
	if string(created.Values[boolField.ID]) != "false" || string(created.Values[decimalField.ID]) != string(decimalField.Default) {
		t.Fatal("new writes did not share declared defaults")
	}
	// Index-only migration preserves instance concurrency tokens and NULL
	// components retain MySQL's unique-index semantics.
	indexed := cloneMigrationDefinition(next)
	indexed.Schema.Indexes = append(indexed.Schema.Indexes, c.Index{ID: c.NewID(), Name: "可空唯一", Unique: true, FieldIDs: []string{nullField.ID}})
	idxReq := c.PrepareRequest{OperationID: c.NewID(), Definition: indexed, PreviousDigest: digest(next), Digest: digest(indexed)}
	f.expect(f.call("POST", "/internal/publications/prepare", idxReq, ""), 200)
	f.activate(idxReq)
	rec = f.call("GET", path+"/"+first.ID, nil, "")
	f.expect(rec, 200)
	_ = json.Unmarshal(rec.Body.Bytes(), &repeated)
	if repeated.ETag != after.ETag {
		t.Fatal("index-only migration changed instance ETag")
	}
}

func TestMySQLPlatformSchemaMigrationRecovery(t *testing.T) {
	for _, backfilled := range []bool{false, true} {
		t.Run(map[bool]string{false: "after-column-DDL", true: "after-backfill-commit"}[backfilled], func(t *testing.T) {
			f := newPlatformMigrationFixture(t)
			first, _ := f.create("A", "first")
			next := cloneMigrationDefinition(f.d)
			field := c.Property{ID: c.NewID(), Name: "恢复字段", DataType: "string", Required: true, Default: raw("恢复值")}
			next.Schema.Properties = append(next.Schema.Properties, field)
			req := c.PrepareRequest{OperationID: c.NewID(), Definition: next, PreviousDigest: digest(f.d), Digest: digest(next)}
			oldJSON, _ := json.Marshal(f.d)
			newJSON, _ := json.Marshal(next)
			// Simulate durable admission closure followed by a process dying
			// after nontransactional ADD COLUMN, before or after atomic backfill.
			tx, err := f.engine.DB().BeginTx(f.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(f.ctx, "INSERT INTO af_data_schema_migrations (tenant_id,operation_id,definition_id,previous_definition,target_digest,backfilled,status) VALUES (?,?,?,?,?,0,'preparing')", f.d.TenantID, req.OperationID, f.d.ID, string(oldJSON), req.Digest); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(f.ctx, "UPDATE af_data_gates SET digest=?,previous_digest=?,operation_id=?,status='preparing',definition=?,storage_signature=? WHERE tenant_id=? AND definition_id=?", req.Digest, req.PreviousDigest, req.OperationID, string(newJSON), storageSignature(next), f.d.TenantID, f.d.ID); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err = f.engine.DB().ExecContext(f.ctx, "ALTER TABLE "+quote(tableName(f.d.ID))+" ADD COLUMN "+platformColumn(field, true)); err != nil {
				t.Fatal(err)
			}
			fixedETag := c.NewID()
			if backfilled {
				tx, err = f.engine.DB().BeginTx(f.ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = tx.ExecContext(f.ctx, "UPDATE "+quote(tableName(f.d.ID))+" SET "+quote(columnName(field.ID))+"=?,etag=? WHERE tenant_id=?", "恢复值", fixedETag, f.d.TenantID); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.ExecContext(f.ctx, "UPDATE af_data_schema_migrations SET backfilled=1 WHERE tenant_id=? AND operation_id=?", f.d.TenantID, req.OperationID); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			path := "/data/schemas/" + f.d.ID + "/instances/" + first.ID
			f.expect(f.call("GET", path, nil, ""), 409)
			var wg sync.WaitGroup
			codes := make(chan int, 4)
			// Model independent replicas here; the public handler has a
			// separate per-process admission budget that serializes requests.
			storageHandler := &handler{cfg: Config{Engine: f.engine}}
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := storageHandler.preparePlatform(f.ctx, req)
					if err == nil {
						codes <- 200
					} else if api, ok := err.(*apiError); ok {
						codes <- api.status
					} else {
						codes <- 500
					}
				}()
			}
			wg.Wait()
			close(codes)
			for code := range codes {
				if code != 200 && code != 503 {
					t.Fatalf("unsafe concurrent recovery status %d", code)
				}
			}
			f.expect(f.call("POST", "/internal/publications/prepare", req, ""), 200)
			f.activate(req)
			rec := f.call("GET", path, nil, "")
			f.expect(rec, 200)
			var out c.Instance
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if string(out.Values[field.ID]) != `"恢复值"` || out.ETag == first.ETag || backfilled && out.ETag != fixedETag {
				t.Fatalf("recovery repeated or lost atomic backfill: %s", rec.Body.String())
			}
			if err = verifyTable(f.ctx, f.engine.DB(), next); err != nil {
				t.Fatal(err)
			}
		})
	}
}
