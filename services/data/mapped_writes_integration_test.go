package data

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

type mappedWriteFixture struct {
	t       *testing.T
	ctx     context.Context
	cfg     Config
	h       *handler
	app     http.Handler
	source  c.DataSource
	db      *sql.DB
	current c.CurrentDefinition
	extra   map[string]c.CurrentDefinition
	mu      sync.Mutex
	code    string
	amount  string
	hidden  string
}

func newMappedWriteFixture(t *testing.T) *mappedWriteFixture {
	t.Helper()
	platformDSN, sourceDSN := os.Getenv("MYSQL_TEST_DSN"), os.Getenv("MAPPED_WRITE_TEST_DSN")
	if platformDSN == "" || sourceDSN == "" {
		t.Skip("MYSQL_TEST_DSN and MAPPED_WRITE_TEST_DSN are required for real source write verification")
	}
	platformConfig, e1 := mysql.ParseDSN(platformDSN)
	sourceConfig, e2 := mysql.ParseDSN(sourceDSN)
	if e1 != nil || e2 != nil || sourceConfig.DBName == platformConfig.DBName || !strings.HasPrefix(sourceConfig.DBName, "security_") {
		t.Fatal("source write verification requires a separate explicitly isolated security_ source database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: platformDSN, MaxOpen: 10, MaxIdle: 3})
	if err != nil {
		t.Fatal("cannot open isolated source write platform database")
	}
	if err := storage.Migrate(ctx, engine.DB(), "data", Migrations); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", sourceDSN)
	if err != nil {
		t.Fatal("cannot open isolated source write target")
	}
	input := mappedTestSourceInput(t, "mysql", sourceDSN, "隔离写入源")
	source := c.DataSource{ID: c.NewID(), TenantID: "mapped-write-" + c.NewID(), Name: input.Name, Driver: "mysql", Host: input.Host, Port: input.Port, Database: input.Database, Username: input.Username, TLSMode: "disable", TenantIsolation: "dedicated", Enabled: true, PasswordSet: true, ETag: digest(c.NewID())}
	f := &mappedWriteFixture{t: t, ctx: ctx, source: source, db: db}
	f.cfg = Config{Engine: engine, DataSourceKey: strings.Repeat("source-write-test-", 3), ServiceToken: strings.Repeat("source-service-test-", 3), CursorSecret: strings.Repeat("source-cursor-test-", 3), SourceNetworkPolicy: testSourceNetworkPolicy(t)}
	f.cfg.SourceNetworkPolicy, err = ParseSourceNetworkPolicy(fmt.Sprintf(`[{"cidr":"127.0.0.1/32","ports":[%d],"allowInsecureTLS":true}]`, source.Port))
	if err != nil {
		t.Fatal(err)
	}
	f.h = &handler{cfg: f.cfg}
	secret, err := f.h.encryptSourceSecret(source.TenantID, source.ID, *input.Password)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(source)
	if _, err := engine.DB().ExecContext(ctx, "INSERT INTO af_data_sources(tenant_id,id,name,name_key,config_json,secret_cipher,endpoint_key,tenant_isolation,etag) VALUES(?,?,?,?,?,?,?,?,?)", source.TenantID, source.ID, source.Name, nameKey(source.Name), string(raw), secret, endpointKey(source), source.TenantIsolation, source.ETag); err != nil {
		t.Fatal(err)
	}
	f.code, f.amount, f.hidden = c.NewID(), c.NewID(), c.NewID()
	d := c.Definition{ID: c.NewID(), TenantID: source.TenantID, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: "隔离可写对象", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", StorageBinding: &c.StorageBinding{DataSourceID: source.ID, Table: "write_" + strings.ReplaceAll(c.NewID(), "-", ""), IDColumn: "id"}, Properties: []c.Property{
		{ID: f.code, Name: "业务键", DataType: "string", Required: true, Mapping: &c.PropertyMapping{DataSourceID: source.ID, Column: "code"}},
		{ID: f.amount, Name: "精确值", DataType: "decimal", Precision: 38, Scale: 4, Required: true, Mapping: &c.PropertyMapping{DataSourceID: source.ID, Column: "amount"}},
		{ID: f.hidden, Name: "私有值", DataType: "text", Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: source.ID, Column: "private_value"}},
	}, Indexes: []c.Index{{ID: c.NewID(), Name: "业务键唯一", Unique: true, FieldIDs: []string{f.code}}, {ID: c.NewID(), Name: "精确组合唯一", Unique: true, FieldIDs: []string{f.code, f.amount}}}}}
	f.current = c.CurrentDefinition{Definition: d, ETag: digest(d), Status: "active"}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		current := f.current
		if other, ok := f.extra[strings.TrimPrefix(r.URL.Path, "/metadata/definitions/")]; ok {
			current = other
		}
		if r.URL.Path != "/metadata/definitions/"+current.Definition.ID || r.Header.Get("X-Tenant-ID") != source.TenantID {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(current)
	}))
	t.Cleanup(metadata.Close)
	f.cfg.MetadataURL, f.cfg.HTTPClient = metadata.URL, metadata.Client()
	f.cfg.Authorizer = authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		return c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: "source-write-policy"}, nil
	})
	f.h = &handler{cfg: f.cfg, client: metadata.Client()}
	f.app = NewHandler(f.cfg)
	operation := c.NewID()
	if _, err := f.h.prepareMapped(ctx, c.PrepareRequest{Definition: d, Digest: f.current.ETag, OperationID: operation}); err != nil {
		t.Fatal(err)
	}
	if response := f.request("POST", "/internal/publications/activate", c.ActivateRequest{DefinitionID: d.ID, Digest: f.current.ETag, OperationID: operation}, ""); response.Code != 200 {
		t.Fatalf("cannot activate isolated write mapping: %d", response.Code)
	}
	t.Cleanup(func() {
		for _, table := range []string{"af_source_write_receipts", "af_source_write_outbox", "af_source_write_gates"} {
			_, _ = db.ExecContext(context.Background(), "DELETE FROM "+quote(table)+" WHERE tenant_id=?", source.TenantID)
		}
		_, _ = db.ExecContext(context.Background(), "DELETE FROM af_source_write_identity WHERE tenant_id=? AND source_id=?", source.TenantID, source.ID)
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+quote(d.Schema.StorageBinding.Table))
		for _, table := range []string{"af_data_mapped_steps", "af_data_mapped_targets", "af_data_gates", "af_data_sources"} {
			_, _ = engine.DB().ExecContext(context.Background(), "DELETE FROM "+quote(table)+" WHERE tenant_id=?", source.TenantID)
		}
		_ = db.Close()
		_ = engine.Close()
	})
	return f
}

func (f *mappedWriteFixture) request(method, path string, body any, etag string) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	p := c.Principal{TenantID: f.source.TenantID, Subject: "source-writer"}
	r = r.WithContext(platform.WithSessionUser(platform.WithPrincipal(f.ctx, p), c.User{ID: p.Subject, TenantID: p.TenantID, Enabled: true, Administrator: true}))
	if strings.HasPrefix(path, "/internal/") {
		r.Header.Set("Authorization", "Bearer "+f.cfg.ServiceToken)
		r.Header.Set("X-Tenant-ID", p.TenantID)
	}
	if etag != "" {
		r.Header.Set("If-Match", `"`+etag+`"`)
	}
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	return w
}

func (f *mappedWriteFixture) base() string {
	return "/data/schemas/" + f.current.Definition.ID + "/instances"
}

func (f *mappedWriteFixture) values(code, amount string) map[string]json.RawMessage {
	return map[string]json.RawMessage{f.code: json.RawMessage(strconvQuote(code)), f.amount: json.RawMessage(strconvQuote(amount)), f.hidden: json.RawMessage(`"SYNTHETIC_PRIVATE_VALUE"`)}
}

func strconvQuote(value string) string { raw, _ := json.Marshal(value); return string(raw) }

func (f *mappedWriteFixture) requireStatus(response *httptest.ResponseRecorder, expected int) {
	f.t.Helper()
	if response.Code != expected {
		var failure c.Error
		_ = json.Unmarshal(response.Body.Bytes(), &failure)
		f.t.Fatalf("isolated source operation status=%d want=%d code=%s", response.Code, expected, failure.Code)
	}
}

func TestMySQLMappedWritesAndSourcePublication(t *testing.T) {
	f := newMappedWriteFixture(t)
	response := f.request("POST", f.base(), c.WriteRequest{Values: f.values("one", "9007199254740993.1"), IdempotencyKey: "create-one"}, "")
	f.requireStatus(response, 422)
	if err := InitializeMappedWrites(f.ctx, f.cfg, f.source.TenantID, f.current.Definition.ID); err != nil {
		t.Fatal(err)
	}
	if err := InitializeMappedWrites(f.ctx, f.cfg, f.source.TenantID, f.current.Definition.ID); err != nil {
		t.Fatal("explicit initialization must be repeatable")
	}
	response = f.request("POST", f.base(), c.WriteRequest{Values: f.values("one", "9007199254740993.1"), IdempotencyKey: "create-one"}, "")
	f.requireStatus(response, 201)
	var one c.Instance
	if json.Unmarshal(response.Body.Bytes(), &one) != nil || len(one.ETag) != 64 {
		t.Fatal("source creation did not return its authenticated complete-row ETag")
	}
	response = f.request("POST", f.base(), c.WriteRequest{Values: f.values("one", "9007199254740993.1"), IdempotencyKey: "create-one"}, "")
	f.requireStatus(response, 200)
	query := c.QuerySpec{From: c.Source{SchemaID: one.SchemaID}, Select: []string{f.code}, Page: c.Page{Limit: 10}}
	response = f.request("POST", "/data/query", query, "")
	f.requireStatus(response, 200)
	var result c.QueryResult
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	if len(result.Data) != 1 || result.Data[0].ETag != one.ETag || len(result.Data[0].Values) != 1 || bytes.Contains(response.Body.Bytes(), []byte("SYNTHETIC_PRIVATE_VALUE")) {
		t.Fatal("projected source query changed ETag or exposed an internal ETag input")
	}
	if _, err := f.db.ExecContext(f.ctx, "UPDATE "+quote(f.current.Definition.Schema.StorageBinding.Table)+" SET private_value=? WHERE id=?", "SYNTHETIC_CHANGED_PRIVATE", one.ID); err != nil {
		t.Fatal(err)
	}
	f.requireStatus(f.request("PATCH", f.base()+"/"+one.ID, c.WriteRequest{Values: map[string]json.RawMessage{f.amount: json.RawMessage(`"2"`)}}, one.ETag), 412)
	response = f.request("GET", f.base()+"/"+one.ID, nil, "")
	f.requireStatus(response, 200)
	_ = json.Unmarshal(response.Body.Bytes(), &one)
	response = f.request("PATCH", f.base()+"/"+one.ID, c.WriteRequest{Values: map[string]json.RawMessage{f.amount: json.RawMessage(`"2"`)}}, one.ETag)
	f.requireStatus(response, 200)
	_ = json.Unmarshal(response.Body.Bytes(), &one)
	if string(one.Values[f.amount]) != `"2.0000"` {
		t.Fatal("source write receipt did not use canonical decimal storage encoding")
	}
	match := c.WriteMatch{IndexID: f.current.Definition.Schema.Indexes[0].ID, Values: map[string]json.RawMessage{f.code: json.RawMessage(`"one"`)}}
	response = f.request("POST", f.base()+"/upsert", c.UpsertRequest{Match: match, Values: map[string]json.RawMessage{f.amount: json.RawMessage(`"3"`)}, IdempotencyKey: "upsert-one"}, one.ETag)
	f.requireStatus(response, 200)
	var upsert c.WriteResult
	_ = json.Unmarshal(response.Body.Bytes(), &upsert)
	if upsert.ID != one.ID || upsert.Outcome != "updated" {
		t.Fatal("source unique-key upsert selected an incorrect instance")
	}
	decimalMatch := c.WriteMatch{IndexID: f.current.Definition.Schema.Indexes[1].ID, Values: map[string]json.RawMessage{f.code: json.RawMessage(`"one"`), f.amount: json.RawMessage(`"3.0"`)}}
	response = f.request("POST", f.base()+"/upsert", c.UpsertRequest{Match: decimalMatch, Values: map[string]json.RawMessage{f.code: json.RawMessage(`"one"`), f.amount: json.RawMessage(`"3"`)}, IdempotencyKey: "upsert-noop"}, upsert.ETag)
	f.requireStatus(response, 200)
	var noop c.WriteResult
	_ = json.Unmarshal(response.Body.Bytes(), &noop)
	if noop.ID != one.ID || noop.ETag != upsert.ETag || noop.Outcome != "updated" {
		t.Fatal("source no-op upsert changed a complete row ETag")
	}
	response = f.request("PATCH", f.base()+"/"+one.ID, c.WriteRequest{Values: map[string]json.RawMessage{f.amount: json.RawMessage(`"3.0000"`)}}, noop.ETag)
	f.requireStatus(response, 200)
	_ = json.Unmarshal(response.Body.Bytes(), &one)
	response = f.request("GET", f.base()+"/"+one.ID, nil, "")
	f.requireStatus(response, 200)
	var unchanged c.Instance
	_ = json.Unmarshal(response.Body.Bytes(), &unchanged)
	if one.ETag != noop.ETag || unchanged.ETag != one.ETag || string(unchanged.Values[f.amount]) != `"3.0000"` {
		t.Fatal("source no-op update receipt and read have different row state credentials")
	}
	bulk := c.BulkWriteRequest{IdempotencyKey: "rollback-create", Items: []c.BulkWriteItem{{ClientKey: "two", Values: f.values("two", "4")}, {ClientKey: "duplicate", Values: f.values("one", "5")}}}
	f.requireStatus(f.request("POST", f.base()+"/bulk-create", bulk, ""), 409)
	var count int
	if err := f.db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM "+quote(f.current.Definition.Schema.StorageBinding.Table)+" WHERE code='two'").Scan(&count); err != nil || count != 0 {
		t.Fatal("source atomic batch retained an earlier successful item")
	}
	if err := f.db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM af_source_write_receipts WHERE request_key='rollback-create'").Scan(&count); err != nil || count != 0 {
		t.Fatal("source atomic failure retained an idempotency receipt")
	}
	bulk.IdempotencyKey = "bulk-create"
	bulk.Items[1] = c.BulkWriteItem{ClientKey: "three", Values: f.values("three", "5")}
	response = f.request("POST", f.base()+"/bulk-create", bulk, "")
	f.requireStatus(response, 200)
	var created c.BulkWriteResult
	_ = json.Unmarshal(response.Body.Bytes(), &created)
	if !created.Committed || len(created.Results) != 2 {
		t.Fatal("source batch did not commit its complete result")
	}
	updates := c.BulkWriteRequest{IdempotencyKey: "bulk-update"}
	for _, item := range created.Results {
		updates.Items = append(updates.Items, c.BulkWriteItem{ClientKey: item.ClientKey, ID: item.ID, IfMatch: `"` + item.ETag + `"`, Values: map[string]json.RawMessage{f.amount: json.RawMessage(`"6"`)}})
	}
	response = f.request("POST", f.base()+"/bulk-update", updates, "")
	f.requireStatus(response, 200)
	var updated c.BulkWriteResult
	_ = json.Unmarshal(response.Body.Bytes(), &updated)
	deletes := c.BulkWriteRequest{IdempotencyKey: "bulk-delete"}
	for _, item := range updated.Results {
		deletes.Items = append(deletes.Items, c.BulkWriteItem{ClientKey: item.ClientKey, ID: item.ID, IfMatch: `"` + item.ETag + `"`})
	}
	f.requireStatus(f.request("POST", f.base()+"/bulk-delete", deletes, ""), 200)
	f.requireStatus(f.request("POST", f.base()+"/bulk-delete", deletes, ""), 200)
	newID := c.NewID()
	bulkUpsert := c.BulkWriteRequest{IdempotencyKey: "bulk-upsert", ConflictPolicy: "overwrite", Items: []c.BulkWriteItem{{ClientKey: "new", Match: &c.WriteMatch{ID: newID}, Values: f.values("four", "7")}, {ClientKey: "old", Match: &match, Values: map[string]json.RawMessage{f.amount: json.RawMessage(`"8"`)}}}}
	f.requireStatus(f.request("POST", f.base()+"/bulk-upsert", bulkUpsert, ""), 200)
	response = f.request("GET", f.base()+"/"+newID, nil, "")
	f.requireStatus(response, 200)
	var fourth c.Instance
	_ = json.Unmarshal(response.Body.Bytes(), &fourth)
	f.requireStatus(f.request("DELETE", f.base()+"/"+newID, nil, fourth.ETag), 204)
	if err := f.cfg.Engine.DB().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM af_data_outbox WHERE tenant_id=?", f.source.TenantID).Scan(&count); err != nil || count != 0 {
		t.Fatal("source mutation wrote its events in a different database transaction")
	}
	if err := f.db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM af_source_write_outbox WHERE tenant_id=?", f.source.TenantID).Scan(&count); err != nil || count < 8 {
		t.Fatal("source transaction did not retain its own outbox events")
	}
	f.testSourceWriteCatalogVisibility()
	f.testPublicationDrainsSourceTransactions()
}

func (f *mappedWriteFixture) testSourceWriteCatalogVisibility() {
	f.t.Helper()
	ddlDB := f.db
	if dsn := os.Getenv("MAPPED_WRITE_DDL_TEST_DSN"); dsn != "" {
		config, err := mysql.ParseDSN(dsn)
		sourceConfig, sourceErr := mysql.ParseDSN(os.Getenv("MAPPED_WRITE_TEST_DSN"))
		if err != nil || sourceErr != nil || config.DBName != f.source.Database || !strings.HasPrefix(config.DBName, "security_") || config.Addr != sourceConfig.Addr || config.Net != sourceConfig.Net {
			f.t.Fatal("maintenance fixture connection must use the same isolated security_ source database")
		}
		ddlDB, err = sql.Open("mysql", dsn)
		if err != nil {
			f.t.Fatal("cannot open isolated maintenance fixture connection")
		}
		defer ddlDB.Close()
	}
	tables, err := f.h.mappedTables(f.ctx, f.current.Definition, false)
	if err != nil {
		f.t.Fatal(err)
	}
	defer closeMappedTables(tables)
	trigger := "trigger_" + strings.ReplaceAll(c.NewID(), "-", "")
	if _, err := ddlDB.ExecContext(f.ctx, "CREATE TRIGGER "+quote(trigger)+" BEFORE UPDATE ON "+quote(tables[0].name)+" FOR EACH ROW SET NEW.amount=NEW.amount"); err != nil {
		f.t.Fatal(err)
	}
	defer ddlDB.ExecContext(context.Background(), "DROP TRIGGER IF EXISTS "+quote(trigger))
	if err := validateMappedWriteTable(f.ctx, f.current.Definition, tables); err == nil {
		f.t.Fatal("source with a visible trigger was certified writable")
	}
	f.t.Run("hidden_trigger_requires_metadata_visibility", func(t *testing.T) {
		dsn := os.Getenv("MAPPED_WRITE_LIMITED_TEST_DSN")
		if dsn == "" {
			t.Skip("MAPPED_WRITE_LIMITED_TEST_DSN is required to verify the synthetic DML-only account")
		}
		config, err := mysql.ParseDSN(dsn)
		if err != nil || config.DBName != f.source.Database || !strings.HasPrefix(config.DBName, "security_") {
			t.Fatal("limited metadata verification requires the same isolated security_ source")
		}
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Fatal("cannot open synthetic limited metadata account")
		}
		defer db.Close()
		var count int
		if err := db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM information_schema.triggers WHERE event_object_schema=? AND event_object_table=?", f.source.Database, tables[0].name).Scan(&count); err != nil || count != 0 {
			t.Fatal("synthetic DML account did not demonstrate the hidden-trigger catalog condition")
		}
		limited := *tables[0]
		limited.db = db
		err = validateMappedWriteTable(f.ctx, f.current.Definition, []*mappedTable{&limited})
		var api *apiError
		if !errors.As(err, &api) || api.code != "mapped_write_metadata_visibility_unsupported" {
			t.Fatal("hidden trigger was certified absent for a DML-only source account")
		}
	})
	if _, err := ddlDB.ExecContext(f.ctx, "DROP TRIGGER "+quote(trigger)); err != nil {
		f.t.Fatal(err)
	}
	child := "child_" + strings.ReplaceAll(c.NewID(), "-", "")
	if _, err := f.db.ExecContext(f.ctx, "CREATE TABLE "+quote(child)+" (id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,parent_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, FOREIGN KEY(parent_id) REFERENCES "+quote(tables[0].name)+"(id) ON DELETE CASCADE) ENGINE=InnoDB"); err != nil {
		f.t.Fatal(err)
	}
	defer f.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+quote(child))
	if err := validateMappedWriteTable(f.ctx, f.current.Definition, tables); err == nil {
		f.t.Fatal("source with an inbound cascading foreign key was certified writable")
	}
	if _, err := f.db.ExecContext(f.ctx, "DROP TABLE "+quote(child)); err != nil {
		f.t.Fatal(err)
	}
	f.t.Run("cross_schema_cascade_visible_with_global_references", func(t *testing.T) {
		dsn := os.Getenv("MAPPED_WRITE_REFERENCING_TEST_DSN")
		if dsn == "" {
			t.Skip("MAPPED_WRITE_REFERENCING_TEST_DSN is required for cross-schema metadata certification")
		}
		config, err := mysql.ParseDSN(dsn)
		sourceConfig, sourceErr := mysql.ParseDSN(os.Getenv("MAPPED_WRITE_TEST_DSN"))
		if err != nil || sourceErr != nil || config.DBName == f.source.Database || !strings.HasPrefix(config.DBName, "security_") || config.Addr != sourceConfig.Addr || config.Net != sourceConfig.Net {
			t.Fatal("cross-schema metadata verification requires a different isolated security_ database on the same instance")
		}
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Fatal("cannot open isolated referencing database")
		}
		defer db.Close()
		externalChild := "cross_child_" + strings.ReplaceAll(c.NewID(), "-", "")
		if _, err := db.ExecContext(f.ctx, "CREATE TABLE "+quote(externalChild)+" (id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,parent_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, FOREIGN KEY(parent_id) REFERENCES "+quote(f.source.Database)+"."+quote(tables[0].name)+"(id) ON DELETE CASCADE) ENGINE=InnoDB"); err != nil {
			t.Fatal(err)
		}
		defer db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+quote(externalChild))
		var references, columns int
		if err := f.db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM information_schema.referential_constraints WHERE constraint_schema=? AND table_name=? AND unique_constraint_schema=? AND referenced_table_name=?", config.DBName, externalChild, f.source.Database, tables[0].name).Scan(&references); err != nil || references != 1 {
			t.Fatal("global REFERENCES did not expose the cross-schema inbound foreign key")
		}
		if err := f.db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM information_schema.key_column_usage WHERE table_schema=? AND table_name=? AND referenced_table_schema=? AND referenced_table_name=?", config.DBName, externalChild, f.source.Database, tables[0].name).Scan(&columns); err != nil || columns != 1 {
			t.Fatal("global REFERENCES did not expose the cross-schema referencing columns")
		}
		err = validateMappedWriteTable(f.ctx, f.current.Definition, tables)
		var api *apiError
		if !errors.As(err, &api) || api.code != "mapped_write_effects_unsupported" {
			t.Fatal("cross-schema inbound cascade was certified safe for source mutation")
		}
	})
}

func (f *mappedWriteFixture) testPublicationDrainsSourceTransactions() {
	f.t.Helper()
	p := c.Principal{TenantID: f.source.TenantID, Subject: "source-writer"}
	entered, release := make(chan struct{}), make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- f.h.withMappedMutationStore(f.ctx, p, f.current, func(mutationStore) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	previous := f.current
	next := previous.Definition
	next.Name += "改名"
	operation := c.NewID()
	req := c.PrepareRequest{Definition: next, PreviousDigest: previous.ETag, Digest: digest(next), OperationID: operation}
	prepared := make(chan error, 1)
	go func() { _, err := f.h.prepareMapped(f.ctx, req); prepared <- err }()
	select {
	case <-prepared:
		close(release)
		f.t.Fatal("source publication did not wait for admitted source transaction")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-writerDone; err != nil {
		f.t.Fatal(err)
	}
	if err := <-prepared; err != nil {
		f.t.Fatal(err)
	}
	if err := f.h.withMappedMutationStore(f.ctx, p, previous, func(mutationStore) error { return nil }); err == nil {
		f.t.Fatal("source admitted a stale write after source gate closure")
	}
	f.mu.Lock()
	f.current = c.CurrentDefinition{Definition: next, ETag: req.Digest, Status: "active"}
	f.mu.Unlock()
	f.requireStatus(f.request("POST", "/internal/publications/activate", c.ActivateRequest{DefinitionID: next.ID, Digest: req.Digest, OperationID: operation}, ""), 200)
	ready, _, err := f.h.mappedMutationReadiness(f.ctx, f.current)
	if err != nil || !ready {
		f.t.Fatal("source write gate did not recover the activated current definition")
	}
}
