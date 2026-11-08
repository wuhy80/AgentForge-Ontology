package data

import (
	"bytes"
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

type authorizerFunc func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error)

func (f authorizerFunc) Authorize(ctx context.Context, p c.Principal, r c.AuthorizationRequest) (c.Decision, error) {
	return f(ctx, p, r)
}

func TestMySQLDecimalComparisonIsExactAboveTwoToThe53(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set; exact MySQL decimal comparison not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 4, MaxIdle: 2})
	if err != nil {
		t.Fatal("connect isolated MySQL failed")
	}
	defer engine.Close()
	d := testDefinition()
	d.ID = c.NewID()
	d.TenantID = c.NewID()
	d.Schema.Properties[1].Precision = 20
	d.Schema.Properties[1].Scale = 4
	statement, err := ddl(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.DB().ExecContext(ctx, statement); err != nil {
		t.Fatal(err)
	}
	lower, higher := "9007199254740992.0000", "9007199254740993.0000"
	lowerID, higherID := "ffffffff-ffff-ffff-ffff-ffffffffffff", "00000000-0000-0000-0000-000000000001"
	for _, row := range []struct{ id, price string }{{lowerID, lower}, {higherID, higher}} {
		_, err = engine.DB().ExecContext(ctx, "INSERT INTO "+quote(tableName(d.ID))+" (tenant_id,id,etag,"+quote(columnName(testFieldCode))+","+quote(columnName(testFieldPrice))+","+quote(columnName(testFieldDept))+") VALUES (?,?,?,?,?,?)", d.TenantID, row.id, c.NewID(), row.price, row.price, "D1")
		if err != nil {
			t.Fatal(err)
		}
	}
	p := c.Principal{TenantID: d.TenantID, Subject: "decimal-test"}
	dec := allowed()
	dec.ReadFields = []string{"*"}
	secret := strings.Repeat("d", 32)
	queryIDs := func(q c.QuerySpec, decision c.Decision) []string {
		t.Helper()
		compiled, e := compileQuery(d, "g", p, decision, q, secret)
		if e != nil {
			t.Fatal(e)
		}
		rows, e := engine.DB().QueryContext(ctx, compiled.SQL, compiled.Args...)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			values := make([]any, len(compiled.ScanFields)+2)
			dest := make([]any, len(values))
			for i := range values {
				dest[i] = &values[i]
			}
			if e = rows.Scan(dest...); e != nil {
				t.Fatal(e)
			}
			ids = append(ids, dbString(values[0]))
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		return ids
	}
	for _, tc := range []struct {
		op    string
		value json.RawMessage
		want  string
	}{{"eq", raw(higher), higherID}, {"in", raw([]string{higher}), higherID}, {"gt", raw(lower), higherID}, {"lt", raw(higher), lowerID}} {
		t.Run(tc.op, func(t *testing.T) {
			q := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Select: []string{testFieldPrice}, Where: &c.Expression{Op: tc.op, Field: testFieldPrice, Value: tc.value}, Page: c.Page{Limit: 10}}
			ids := queryIDs(q, dec)
			if len(ids) != 1 || ids[0] != tc.want {
				t.Fatalf("decimal %s was not exact: %v", tc.op, ids)
			}
		})
	}
	q := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Select: []string{testFieldPrice}, Page: c.Page{Limit: 10}}
	filtered := dec
	filtered.RowFilter = &c.Expression{Op: "eq", Field: testFieldPrice, Value: raw(higher)}
	ids := queryIDs(q, filtered)
	if len(ids) != 1 || ids[0] != higherID {
		t.Fatalf("authorization decimal filter widened: %v", ids)
	}
	values := map[string]json.RawMessage{testFieldCode: raw("new"), testFieldPrice: raw(higher), testFieldDept: raw("D1")}
	newDecision := dec
	newDecision.NewRowFilter = &c.Expression{Op: "eq", Field: testFieldPrice, Value: raw(lower)}
	err = engine.WithTx(ctx, func(tx *sql.Tx) error { return checkNewRow(ctx, tx, d.Schema, values, newDecision) })
	var denied *apiError
	if !errors.As(err, &denied) || denied.status != 403 {
		t.Fatalf("new-row decimal boundary widened: %v", err)
	}
	newDecision.NewRowFilter.Value = raw(higher)
	if err = engine.WithTx(ctx, func(tx *sql.Tx) error { return checkNewRow(ctx, tx, d.Schema, values, newDecision) }); err != nil {
		t.Fatalf("exact new-row value rejected: %v", err)
	}
	q.OrderBy = []c.Order{{Field: testFieldPrice, Direction: "asc"}}
	q.Page.Limit = 1
	compiled, err := compileQuery(d, "g", p, dec, q, secret)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signCursor(secret, pageCursor{Fingerprint: compiled.Fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: d.ID, Digest: "g", Authorization: digest(dec), LastID: lowerID, LastValue: raw(lower), Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	q.Page.Cursor = token
	ids = queryIDs(q, dec)
	if len(ids) != 1 || ids[0] != higherID {
		t.Fatalf("decimal cursor skipped the adjacent larger value: %v", ids)
	}
}

// MYSQL_TEST_DSN must reference an isolated development database. Tests create
// uniquely identified schemas/tenants and never drop or truncate shared tables.
func TestMySQLDataIntegration(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set; real MySQL integration not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 8, MaxIdle: 3})
	if err != nil {
		t.Fatal("connect isolated MySQL failed")
	}
	defer engine.Close()
	if err = storage.Migrate(ctx, engine.DB(), "data", Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	d := testDefinition()
	d.ID = c.NewID()
	d.TenantID = c.NewID()
	d.Schema.Indexes = []c.Index{{ID: c.NewID(), Name: "编码唯一", Unique: true, FieldIDs: []string{testFieldCode}}}
	current := c.CurrentDefinition{Definition: d, ETag: digest(d), Status: "active"}
	var currentMu sync.RWMutex
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		currentMu.RLock()
		defer currentMu.RUnlock()
		if r.Header.Get("X-Tenant-ID") != d.TenantID {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, current)
	}))
	defer meta.Close()
	secret := strings.Repeat("s", 32)
	decision := allowed()
	decision.RowFilter = &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("D1")}
	decision.NewRowFilter = decision.RowFilter
	var decisionMu sync.RWMutex
	auth := authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		decisionMu.RLock()
		defer decisionMu.RUnlock()
		return decision, nil
	})
	h := NewHandler(Config{SourceNetworkPolicy: testSourceNetworkPolicy(t), Engine: engine, Authorizer: auth, MetadataURL: meta.URL, ServiceToken: secret, CursorSecret: strings.Repeat("c", 32)})
	p := c.Principal{TenantID: d.TenantID, Subject: "test-user"}
	call := func(handler http.Handler, method, path string, body any, etag string, internal bool, principal c.Principal) *httptest.ResponseRecorder {
		t.Helper()
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req = req.WithContext(platform.WithPrincipal(req.Context(), principal))
		if etag != "" {
			req.Header.Set("If-Match", `"`+etag+`"`)
		}
		if internal {
			req.Header.Set("Authorization", "Bearer "+secret)
			req.Header.Set("X-Tenant-ID", principal.TenantID)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	expect := func(rec *httptest.ResponseRecorder, status int) {
		t.Helper()
		if rec.Code != status {
			t.Fatalf("HTTP %d, want %d: %s", rec.Code, status, rec.Body.String())
		}
	}
	prepare := c.PrepareRequest{OperationID: c.NewID(), Definition: d, Digest: digest(d)}
	rec := call(h, "POST", "/internal/publications/preview", prepare, "", true, p)
	expect(rec, 200)
	var count int
	if err = engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM af_data_gates WHERE tenant_id=? AND definition_id=?", p.TenantID, d.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview changed gate: %d %v", count, err)
	}
	if err = engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", tableName(d.ID)).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview executed DDL: %d %v", count, err)
	}
	wide := d
	wide.ID = c.NewID()
	wide.Schema = &c.Schema{Mode: "schema_2_db", StorageLayout: "single"}
	for i := 0; i < 100; i++ {
		wide.Schema.Properties = append(wide.Schema.Properties, c.Property{ID: c.NewID(), Name: strings.Repeat("列", i+1), DataType: "string", Required: true})
	}
	widePrepare := c.PrepareRequest{OperationID: c.NewID(), Definition: wide, Digest: digest(wide)}
	expect(call(h, "POST", "/internal/publications/preview", widePrepare, "", true, p), 422)
	expect(call(h, "POST", "/internal/publications/prepare", widePrepare, "", true, p), 422)
	if err = engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM af_data_gates WHERE tenant_id=? AND definition_id=?", p.TenantID, wide.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected wide schema left a closed gate: %d %v", count, err)
	}
	// Fixed UUID columns use ASCII and DYNAMIC rows: 100 UUID attributes are
	// physically feasible, even though 100 utf8mb4 CHAR(36) attributes are not.
	uuidWide := wide
	uuidWide.ID = c.NewID()
	uuidWide.Schema = &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: append([]c.Property(nil), wide.Schema.Properties...)}
	for i := range uuidWide.Schema.Properties {
		uuidWide.Schema.Properties[i].DataType = "uuid"
	}
	expect(call(h, "POST", "/internal/publications/prepare", c.PrepareRequest{OperationID: c.NewID(), Definition: uuidWide, Digest: digest(uuidWide)}, "", true, p), 200)
	if err = verifyTable(ctx, engine.DB(), uuidWide); err != nil {
		t.Fatalf("100 UUID attributes failed real physical validation: %v", err)
	}
	rec = call(h, "POST", "/internal/publications/prepare", prepare, "", true, p)
	expect(rec, 200)
	path := "/data/schemas/" + d.ID + "/instances"
	first := c.WriteRequest{Values: map[string]json.RawMessage{testFieldCode: raw("P1"), testFieldDept: raw("D1"), testFieldPrice: raw("100.125")}, IdempotencyKey: "create-1"}
	expect(call(h, "POST", path, first, "", false, p), 409) // Gate remains closed before activation.
	activate := c.ActivateRequest{OperationID: prepare.OperationID, DefinitionID: d.ID, Digest: prepare.Digest}
	expect(call(h, "POST", "/internal/publications/activate", activate, "", true, p), 200)
	bad := c.WriteRequest{Values: map[string]json.RawMessage{testFieldCode: raw("BAD"), testFieldDept: raw("D2")}, IdempotencyKey: "denied"}
	expect(call(h, "POST", path, bad, "", false, p), 403)
	rec = call(h, "POST", path, first, "", false, p)
	expect(rec, 201)
	var created c.Instance
	if json.Unmarshal(rec.Body.Bytes(), &created) != nil {
		t.Fatal("invalid create result")
	}
	if _, ok := created.Values[testFieldPrice]; ok {
		t.Fatal("create leaked unreadable price")
	}
	rec = call(h, "POST", path, first, "", false, p)
	expect(rec, 200)
	var replay c.Instance
	_ = json.Unmarshal(rec.Body.Bytes(), &replay)
	if created.ID != replay.ID || created.ETag != replay.ETag {
		t.Fatal("idempotent retry changed logical result")
	}
	duplicate := first
	duplicate.IdempotencyKey = "duplicate"
	expect(call(h, "POST", path, duplicate, "", false, p), 409)
	entityPath := path + "/" + created.ID
	expect(call(h, "PATCH", entityPath, c.WriteRequest{Values: map[string]json.RawMessage{testFieldDept: raw("D2")}}, created.ETag, false, p), 403)
	rec = call(h, "PATCH", entityPath, c.WriteRequest{Values: map[string]json.RawMessage{testFieldCode: raw("P1-new")}}, created.ETag, false, p)
	expect(rec, 200)
	var changed c.Instance
	_ = json.Unmarshal(rec.Body.Bytes(), &changed)
	expect(call(h, "PATCH", entityPath, c.WriteRequest{Values: map[string]json.RawMessage{testFieldCode: raw("lost-update")}}, created.ETag, false, p), 412)
	rec = call(h, "POST", path, first, "", false, p)
	expect(rec, 200)
	_ = json.Unmarshal(rec.Body.Bytes(), &replay)
	if string(replay.Values[testFieldCode]) != `"P1"` {
		t.Fatal("idempotent replay did not preserve original creation result")
	}
	second := c.WriteRequest{Values: map[string]json.RawMessage{testFieldCode: raw("P2"), testFieldDept: raw("D1")}, IdempotencyKey: "create-2"}
	expect(call(h, "POST", path, second, "", false, p), 201)
	q := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Select: []string{testFieldCode}, OrderBy: []c.Order{{Field: testFieldCode, Direction: "asc"}}, Page: c.Page{Limit: 1}}
	rec = call(h, "POST", "/data/query", q, "", false, p)
	expect(rec, 200)
	var page c.QueryResult
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if !page.HasMore || page.NextCursor == "" || len(page.Data) != 1 {
		t.Fatalf("invalid first page: %s", rec.Body.String())
	}
	decisionMu.Lock()
	decision.State = "policy-2"
	decisionMu.Unlock()
	q.Page.Cursor = page.NextCursor
	expect(call(h, "POST", "/data/query", q, "", false, p), 422)
	decisionMu.Lock()
	decision.ReadFields = []string{"*"}
	decisionMu.Unlock()
	q.Page = c.Page{Limit: 100}
	q.Where = &c.Expression{Op: "not", Args: []c.Expression{{Op: "gt", Field: testFieldPrice, Value: raw("220.000")}}}
	rec = call(h, "POST", "/data/query", q, "", false, p)
	expect(rec, 200)
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Data) != 1 || page.Data[0].ID != created.ID {
		t.Fatalf("NULL was treated as false under NOT: %s", rec.Body.String())
	}
	var authorizations atomic.Int32
	changingAuth := authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		decisionMu.RLock()
		d := decision
		decisionMu.RUnlock()
		if authorizations.Add(1) > 1 {
			d.State = "policy-changed-during-transaction"
		}
		return d, nil
	})
	changingHandler := NewHandler(Config{SourceNetworkPolicy: testSourceNetworkPolicy(t), Engine: engine, Authorizer: changingAuth, MetadataURL: meta.URL, ServiceToken: secret, CursorSecret: strings.Repeat("c", 32)})
	third := c.WriteRequest{Values: map[string]json.RawMessage{testFieldCode: raw("P3"), testFieldDept: raw("D1")}, IdempotencyKey: "rollback-on-revocation"}
	expect(call(changingHandler, "POST", path, third, "", false, p), 409)
	if err = engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quote(tableName(d.ID))+" WHERE tenant_id=?", p.TenantID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("failed write left business rows: %d %v", count, err)
	}
	if err = engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM af_data_outbox WHERE tenant_id=?", p.TenantID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("outbox did not share business rollback: %d %v", count, err)
	}
	expect(call(h, "GET", entityPath, nil, "", false, c.Principal{TenantID: c.NewID(), Subject: p.Subject}), 404)
	// Definition presentation rename closes admission until Metadata and Data
	// agree on the new current digest, without rebuilding the physical table.
	renamed := d
	renamed.Name = "泵设备"
	renamed.Schema = &c.Schema{Mode: d.Schema.Mode, StorageLayout: d.Schema.StorageLayout, Properties: append([]c.Property(nil), d.Schema.Properties...), Indexes: d.Schema.Indexes}
	renamed.Schema.Properties[0].Name = "设备编号"
	prepare2 := c.PrepareRequest{OperationID: c.NewID(), Definition: renamed, PreviousDigest: digest(d), Digest: digest(renamed)}
	expect(call(h, "POST", "/internal/publications/prepare", prepare2, "", true, p), 200)
	expect(call(h, "GET", entityPath, nil, "", false, p), 409)
	currentMu.Lock()
	current = c.CurrentDefinition{Definition: renamed, ETag: prepare2.Digest, Status: "active"}
	currentMu.Unlock()
	expect(call(h, "POST", "/internal/publications/activate", c.ActivateRequest{OperationID: prepare2.OperationID, DefinitionID: d.ID, Digest: prepare2.Digest}, "", true, p), 200)
	expect(call(h, "GET", entityPath, nil, "", false, p), 200)
	// A rejected physical change leaves the active gate untouched.
	unsupported := renamed
	unsupported.Schema = &c.Schema{Mode: renamed.Schema.Mode, StorageLayout: renamed.Schema.StorageLayout, Properties: append([]c.Property(nil), renamed.Schema.Properties...), Indexes: renamed.Schema.Indexes}
	unsupported.Schema.Properties[0].Required = false
	expect(call(h, "POST", "/internal/publications/prepare", c.PrepareRequest{OperationID: c.NewID(), Definition: unsupported, PreviousDigest: prepare2.Digest, Digest: digest(unsupported)}, "", true, p), 422)
	var status string
	if err = engine.DB().QueryRowContext(ctx, "SELECT status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", p.TenantID, d.ID).Scan(&status); err != nil || status != "ready" {
		t.Fatalf("preflight rejection paused a live model: %s %v", status, err)
	}
	// SQL NULL sent to a nullable property round-trips as JSON null.
	var nullValue any
	if err = engine.WithTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT "+quote(columnName(testFieldPrice))+" FROM "+quote(tableName(d.ID))+" WHERE tenant_id=? AND "+quote(columnName(testFieldCode))+"=?", p.TenantID, "P2").Scan(&nullValue)
	}); err != nil {
		t.Fatal(err)
	}
	if nullValue != nil {
		t.Fatal("nullable SQL value did not remain null")
	}
	// A historical creation response cannot inherit visibility from a row that
	// has since moved to a different department.
	decisionMu.Lock()
	decision.RowFilter = nil
	decision.NewRowFilter = nil
	decision.State = "transfer-admin"
	decisionMu.Unlock()
	rec = call(h, "GET", entityPath, nil, "", false, p)
	expect(rec, 200)
	var beforeTransfer c.Instance
	_ = json.Unmarshal(rec.Body.Bytes(), &beforeTransfer)
	expect(call(h, "PATCH", entityPath, c.WriteRequest{Values: map[string]json.RawMessage{testFieldDept: raw("D2")}}, beforeTransfer.ETag, false, p), 200)
	decisionMu.Lock()
	decision.RowFilter = &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("D2")}
	decision.NewRowFilter = nil
	decision.State = "department-2-only"
	decisionMu.Unlock()
	expect(call(h, "GET", entityPath, nil, "", false, p), 200)
	expect(call(h, "POST", path, first, "", false, p), 403)
}
