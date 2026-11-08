package data

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func TestObjectContextSelectionRejectsUnboundedOrAmbiguousSources(t *testing.T) {
	d := testDefinition()
	d.Schema.Indexes = []c.Index{{ID: c.NewID(), Unique: true, FieldIDs: []string{testFieldCode}}}
	base := c.ObjectContextRequest{SchemaID: d.ID, FieldID: testFieldDept, MatchFieldID: testFieldCode, MatchValue: raw("member")}
	property, sql, args, limit, err := objectContextSelection(d, base)
	if err != nil || property.ID != testFieldDept || !strings.Contains(sql, columnName(testFieldCode)) || len(args) != 1 || args[0] != "member" || limit != 2 {
		t.Fatal("valid unique source was not compiled with a bound scalar")
	}
	for name, change := range map[string]func(*c.ObjectContextRequest){
		"unknown mode":     func(r *c.ObjectContextRequest) { r.Mode = "first" },
		"unknown schema":   func(r *c.ObjectContextRequest) { r.SchemaID = c.NewID() },
		"field name":       func(r *c.ObjectContextRequest) { r.FieldID = "所属部门" },
		"match field name": func(r *c.ObjectContextRequest) { r.MatchFieldID = "设备编码" },
		"missing selector": func(r *c.ObjectContextRequest) { r.MatchFieldID = "" },
		"null":             func(r *c.ObjectContextRequest) { r.MatchValue = raw(nil) },
		"oversize":         func(r *c.ObjectContextRequest) { r.MatchValue = raw(strings.Repeat("x", maxObjectContextBytes)) },
		"type mismatch":    func(r *c.ObjectContextRequest) { r.MatchValue = raw(true) },
		"two selectors":    func(r *c.ObjectContextRequest) { r.InstanceID = c.NewID() },
		"fixed set": func(r *c.ObjectContextRequest) {
			r.InstanceID, r.MatchFieldID, r.MatchValue, r.Mode = c.NewID(), "", nil, "set"
		},
		"ambiguous scalar": func(r *c.ObjectContextRequest) { r.MatchFieldID = testFieldDept },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			change(&input)
			if _, _, _, _, err := objectContextSelection(d, input); err == nil {
				t.Fatal("unsupported context selector accepted")
			}
		})
	}
	base.Mode, base.MatchFieldID = "set", testFieldDept
	if _, _, _, limit, err := objectContextSelection(d, base); err != nil || limit != 101 {
		t.Fatal("bounded set source must scan at most 101 rows")
	}
	d.Schema.StorageBinding = &c.StorageBinding{DataSourceID: c.NewID(), Table: "external"}
	if _, _, _, _, err := objectContextSelection(d, base); err == nil {
		t.Fatal("mapped source accepted as platform context")
	}
}

func TestObjectContextInternalTransportDoesNotTrustBrowserIdentity(t *testing.T) {
	secret := strings.Repeat("c", 32)
	app := NewHandler(Config{ServiceToken: secret})
	for _, tc := range []struct {
		name, token, tenant, body string
		status                    int
	}{
		{"missing credentials", "", "tenant", `{}`, 401},
		{"browser token", "mcp_fake", "tenant", `{}`, 401},
		{"missing tenant", secret, "", `{}`, 400},
		{"unknown identity injection", secret, "tenant", `{"userId":"admin"}`, 400},
		{"malformed body", secret, "tenant", `{"schemaId":`, 400},
		{"multiple bodies", secret, "tenant", `{} {}`, 400},
		{"request budget", secret, "tenant", `{"schemaId":"` + strings.Repeat("a", 33<<10) + `"}`, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/internal/authorization/object-value", strings.NewReader(tc.body))
			r.Header.Set("X-Tenant-ID", tc.tenant)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d want %d", w.Code, tc.status)
			}
		})
	}
}

func TestObjectContextAdmissionDoesNotWaitForBusinessSlot(t *testing.T) {
	if err := dataServiceLimiter.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { <-dataServiceLimiter.slots }()
	app := ProtectResources(NewHandler(Config{ServiceToken: strings.Repeat("c", 32)}))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("POST", "/internal/authorization/object-value", strings.NewReader(`{}`)).WithContext(ctx)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("internal callback waited for occupied business admission: got %d", w.Code)
	}
}

type objectContextFixture struct {
	engine  *storage.Engine
	core    *handler
	app     http.Handler
	current c.CurrentDefinition
	intID   string
	change  atomic.Bool
	calls   atomic.Int64
	driftAt atomic.Int64
}

func newObjectContextFixture(t *testing.T) *objectContextFixture {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set; real object context lookup not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 5, MaxIdle: 2})
	if err != nil {
		t.Fatal("connect object context test MySQL failed")
	}
	t.Cleanup(func() { _ = engine.Close() })
	if err = storage.Migrate(ctx, engine.DB(), "data", Migrations); err != nil {
		t.Fatal("migrate object context test MySQL failed")
	}
	d := testDefinition()
	d.ID, d.TenantID = c.NewID(), c.NewID()
	d.Schema.Properties[1].Precision, d.Schema.Properties[1].Scale = 20, 4
	intID := c.NewID()
	d.Schema.Properties = append(d.Schema.Properties, c.Property{ID: intID, Name: "大整数", DataType: "int64", Nullable: true})
	d.Schema.Indexes = []c.Index{{ID: c.NewID(), Name: "成员唯一", Unique: true, FieldIDs: []string{testFieldCode}}}
	statement, err := ddl(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.DB().ExecContext(ctx, statement); err != nil {
		t.Fatal("create object context test table failed")
	}
	t.Cleanup(func() {
		_, _ = engine.DB().Exec("DROP TABLE " + quote(tableName(d.ID)))
		_, _ = engine.DB().Exec("DELETE FROM af_data_gates WHERE tenant_id=? AND definition_id=?", d.TenantID, d.ID)
	})
	current := c.CurrentDefinition{Definition: d, ETag: digest(d), Status: "active"}
	_, err = engine.DB().ExecContext(ctx, "INSERT INTO af_data_gates (tenant_id,definition_id,digest,previous_digest,operation_id,status,definition,storage_signature) VALUES (?,?,?,?,?,'ready',?,?)", d.TenantID, d.ID, current.ETag, "", c.NewID(), string(mustMutationJSON(t, d)), digest(d.Schema))
	if err != nil {
		t.Fatal("create object context test gate failed")
	}
	f := &objectContextFixture{engine: engine, current: current, intID: intID}
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("c", 32) || r.Header.Get("X-Tenant-ID") != d.TenantID || r.URL.Path != "/metadata/definitions/"+d.ID {
			w.WriteHeader(404)
			return
		}
		out := f.current
		call := f.calls.Add(1)
		if f.change.Load() || f.driftAt.Load() != 0 && call >= f.driftAt.Load() {
			out.ETag = strings.Repeat("0", 64)
		}
		writeJSON(w, 200, out)
	}))
	t.Cleanup(meta.Close)
	config := Config{Engine: engine, MetadataURL: meta.URL, ServiceToken: strings.Repeat("c", 32), Authorizer: authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		t.Error("object context reader must not recursively invoke authorization")
		return c.Decision{}, nil
	})}
	f.core, f.app = &handler{cfg: config, client: http.DefaultClient}, NewHandler(config)
	return f
}

func (f *objectContextFixture) insert(t *testing.T, tenant, code, dept string, price any) string {
	t.Helper()
	id := c.NewID()
	query := "INSERT INTO " + quote(tableName(f.current.Definition.ID)) + " (tenant_id,id,etag," + quote(columnName(testFieldCode)) + "," + quote(columnName(testFieldDept)) + "," + quote(columnName(testFieldPrice)) + "," + quote(columnName(f.intID)) + ") VALUES (?,?,?,?,?,?,?)"
	if _, err := f.engine.DB().Exec(query, tenant, id, c.NewID(), code, dept, price, "9223372036854775807"); err != nil {
		t.Fatal("insert object context fixture failed")
	}
	return id
}

func (f *objectContextFixture) lookup(t *testing.T, input c.ObjectContextRequest, status int) c.ObjectContextResult {
	t.Helper()
	r := httptest.NewRequest("POST", "/internal/authorization/object-value", bytes.NewReader(mustMutationJSON(t, input)))
	r.Header.Set("Authorization", "Bearer "+f.core.cfg.ServiceToken)
	r.Header.Set("X-Tenant-ID", f.current.Definition.TenantID)
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("context lookup got %d want %d: %s", w.Code, status, w.Body.String())
	}
	var out c.ObjectContextResult
	if status == 200 {
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Digest) != 64 || len(out.Value) == 0 {
			t.Fatal("invalid object context response")
		}
	} else if strings.Contains(w.Body.String(), "9223372036854775807") || strings.Contains(w.Body.String(), "9007199254740993") {
		t.Fatal("context rejection leaked source values")
	}
	return out
}

func TestMySQLObjectContextScopedReadsAndDependencyChanges(t *testing.T) {
	f := newObjectContextFixture(t)
	id := f.insert(t, f.current.Definition.TenantID, "user-a", "D1", "9007199254740993.0000")
	f.insert(t, c.NewID(), "user-a", "hidden-other-tenant", "1.0000")
	input := c.ObjectContextRequest{SchemaID: f.current.Definition.ID, FieldID: testFieldPrice, MatchFieldID: testFieldCode, MatchValue: raw("user-a")}
	first := f.lookup(t, input, 200)
	if string(first.Value) != `"9007199254740993.0000"` {
		t.Fatal("context decimal value lost precision or tenant filter")
	}
	input.FieldID, input.InstanceID, input.MatchFieldID, input.MatchValue = f.intID, id, "", nil
	large := f.lookup(t, input, 200)
	if string(large.Value) != `"9223372036854775807"` {
		t.Fatal("context int64 value lost precision")
	}
	input.FieldID = testFieldPrice
	before := f.lookup(t, input, 200)
	if _, err := f.engine.DB().Exec("UPDATE "+quote(tableName(f.current.Definition.ID))+" SET "+quote(columnName(testFieldPrice))+"=?,etag=? WHERE tenant_id=? AND id=?", "9007199254740992.0000", c.NewID(), f.current.Definition.TenantID, id); err != nil {
		t.Fatal(err)
	}
	after := f.lookup(t, input, 200)
	if before.Digest == after.Digest || string(after.Value) != `"9007199254740992.0000"` {
		t.Fatal("source changes did not affect dependency digest")
	}
	input.InstanceID = c.NewID()
	f.lookup(t, input, 403)
	input.InstanceID = id
	if _, err := f.engine.DB().Exec("UPDATE "+quote(tableName(f.current.Definition.ID))+" SET "+quote(columnName(testFieldPrice))+"=NULL WHERE tenant_id=? AND id=?", f.current.Definition.TenantID, id); err != nil {
		t.Fatal(err)
	}
	f.lookup(t, input, 403)
	f.change.Store(true)
	f.lookup(t, input, 403)
	f.change.Store(false)
	input.FieldID = f.intID
	f.driftAt.Store(f.calls.Load() + 2)
	f.lookup(t, input, 403)
}

func TestMySQLObjectContextRejectsPhysicalUniquenessDrift(t *testing.T) {
	f := newObjectContextFixture(t)
	f.insert(t, f.current.Definition.TenantID, "user-a", "D1", "1.0000")
	index := f.current.Definition.Schema.Indexes[0]
	if _, err := f.engine.DB().Exec("ALTER TABLE " + quote(tableName(f.current.Definition.ID)) + " DROP INDEX " + quote(physical("ix_", index.ID))); err != nil {
		t.Fatal("remove isolated uniqueness fixture failed")
	}
	f.insert(t, f.current.Definition.TenantID, "user-a", "D2", "1.0000")
	f.lookup(t, c.ObjectContextRequest{SchemaID: f.current.Definition.ID, FieldID: testFieldDept, MatchFieldID: testFieldCode, MatchValue: raw("user-a")}, 403)
}

func TestMySQLObjectContextSetsAreCompleteAndBounded(t *testing.T) {
	f := newObjectContextFixture(t)
	for i := 0; i < 100; i++ {
		f.insert(t, f.current.Definition.TenantID, fmt.Sprintf("member-%03d", i), "D1", "1.0000")
	}
	input := c.ObjectContextRequest{SchemaID: f.current.Definition.ID, FieldID: testFieldCode, MatchFieldID: testFieldDept, MatchValue: raw("D1"), Mode: "set"}
	out := f.lookup(t, input, 200)
	var values []string
	if json.Unmarshal(out.Value, &values) != nil || len(values) != 100 {
		t.Fatal("bounded source set was incomplete")
	}
	input.FieldID = testFieldPrice
	out = f.lookup(t, input, 200)
	if string(out.Value) != `["1.0000"]` {
		t.Fatal("duplicate values were not deterministically collapsed")
	}
	input.Mode = "scalar"
	f.lookup(t, input, 403)
	input.Mode, input.MatchValue = "set", raw("missing")
	f.lookup(t, input, 403)
	input.MatchValue = raw("D1")
	f.insert(t, f.current.Definition.TenantID, "member-100", "D1", "1.0000")
	f.lookup(t, input, 403)
}

func TestMySQLObjectContextReadDoesNotDeadlockOuterWriteGate(t *testing.T) {
	f := newObjectContextFixture(t)
	id := f.insert(t, f.current.Definition.TenantID, "user-a", "D1", "1.0000")
	tx, err := f.engine.DB().BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = f.core.gate(context.Background(), tx, c.Principal{TenantID: f.current.Definition.TenantID}, f.current); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := f.core.objectContextValue(ctx, f.current.Definition.TenantID, c.ObjectContextRequest{SchemaID: f.current.Definition.ID, FieldID: testFieldDept, InstanceID: id})
	if err != nil || string(out.Value) != `"D1"` {
		t.Fatal("nested context read blocked on its caller's publication gate")
	}
}
