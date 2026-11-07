package data

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

type mappedIntegrationFixture struct {
	t         *testing.T
	ctx       context.Context
	engine    *storage.Engine
	app       http.Handler
	tenant    string
	sources   []c.DataSource
	dbs       []*sql.DB
	decision  c.Decision
	current   map[string]c.CurrentDefinition
	mu        sync.Mutex
	service   string
	authorize func(c.AuthorizationRequest) c.Decision
}

func mappedTestSourceInput(t *testing.T, driver, dsn, name string) c.DataSourceInput {
	t.Helper()
	input := c.DataSourceInput{Name: name, Driver: driver, Enabled: true, TLSMode: "disable", TenantIsolation: "column"}
	password := ""
	if driver == "mysql" {
		configuration, err := mysql.ParseDSN(dsn)
		if err != nil {
			t.Fatal("invalid isolated MySQL source DSN")
		}
		host, port, err := net.SplitHostPort(configuration.Addr)
		if err != nil {
			t.Fatal("isolated MySQL source must use a TCP endpoint")
		}
		input.Host, input.Database, input.Username = host, configuration.DBName, configuration.User
		input.Port, _ = strconv.Atoi(port)
		password = configuration.Passwd
	} else {
		configuration, err := url.Parse(dsn)
		if err != nil || configuration.User == nil {
			t.Fatal("invalid isolated PostgreSQL source DSN")
		}
		input.Host, input.Database, input.Username = configuration.Hostname(), strings.TrimPrefix(configuration.Path, "/"), configuration.User.Username()
		input.Port, _ = strconv.Atoi(configuration.Port())
		password, _ = configuration.User.Password()
	}
	input.Password = &password
	return input
}

func newMappedIntegrationFixture(t *testing.T, secondaryDriver, secondaryDSN string) *mappedIntegrationFixture {
	t.Helper()
	platformDSN := os.Getenv("MYSQL_TEST_DSN")
	if platformDSN == "" || secondaryDSN == "" {
		t.Skip("isolated source DSNs are not configured; real mapped-source verification not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: platformDSN, MaxOpen: 6, MaxIdle: 3})
	if err != nil {
		t.Fatal("cannot connect isolated mapping platform store")
	}
	if err := storage.Migrate(ctx, engine.DB(), "data", Migrations); err != nil {
		engine.Close()
		t.Fatal(err)
	}
	f := &mappedIntegrationFixture{t: t, ctx: ctx, engine: engine, tenant: "mapped-" + c.NewID(), decision: c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: "mapped-test-policy"}, current: map[string]c.CurrentDefinition{}, service: strings.Repeat("mapped-service-", 3)}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, "/metadata/definitions/")
		record, exists := f.current[id]
		if !exists || record.Definition.TenantID != r.Header.Get("X-Tenant-ID") {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, record)
	}))
	t.Cleanup(metadata.Close)
	f.app = NewHandler(Config{Engine: engine, DataSourceKey: strings.Repeat("mapped-encryption-", 3), ServiceToken: f.service, CursorSecret: strings.Repeat("mapped-cursor-", 3), MetadataURL: metadata.URL, HTTPClient: metadata.Client(), Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, request c.AuthorizationRequest) (c.Decision, error) {
		if f.authorize != nil {
			return f.authorize(request), nil
		}
		return f.decision, nil
	})})
	t.Cleanup(func() {
		for _, name := range []string{"af_data_mapped_steps", "af_data_mapped_targets", "af_data_gates", "af_data_sources"} {
			_, _ = engine.DB().Exec("DELETE FROM "+quote(name)+" WHERE tenant_id=?", f.tenant)
		}
		for _, db := range f.dbs {
			_ = db.Close()
		}
		_ = engine.Close()
	})
	for index, backend := range []struct{ driver, sqlDriver, dsn string }{{"mysql", "mysql", platformDSN}, {secondaryDriver, map[string]string{"mysql": "mysql", "postgresql": "pgx"}[secondaryDriver], secondaryDSN}} {
		input := mappedTestSourceInput(t, backend.driver, backend.dsn, "映射源"+strconv.Itoa(index))
		response := f.request("POST", "/data/sources", input, false)
		if response.Code != 201 {
			t.Fatalf("create isolated mapping source failed: %d %s", response.Code, response.Body)
		}
		var source c.DataSource
		if json.Unmarshal(response.Body.Bytes(), &source) != nil || source.ID == "" {
			t.Fatal("invalid safe source response")
		}
		f.sources = append(f.sources, source)
		db, err := sql.Open(backend.sqlDriver, backend.dsn)
		if err != nil {
			t.Fatal("cannot open isolated source")
		}
		f.dbs = append(f.dbs, db)
	}
	return f
}

func (f *mappedIntegrationFixture) request(method, path string, body any, internal bool) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	principal := c.Principal{TenantID: f.tenant, Subject: "mapped-reader"}
	r = r.WithContext(platform.WithSessionUser(platform.WithPrincipal(f.ctx, principal), c.User{ID: principal.Subject, TenantID: f.tenant, Enabled: true, Administrator: true}))
	if internal {
		r.Header.Set("Authorization", "Bearer "+f.service)
		r.Header.Set("X-Tenant-ID", f.tenant)
	}
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	return w
}

func (f *mappedIntegrationFixture) qualified(source int, name string) string {
	namespace, _ := sourceNamespace(f.sources[source], "")
	ns, _ := sourceQuote(f.sources[source].Driver, namespace)
	table, _ := sourceQuote(f.sources[source].Driver, name)
	return ns + "." + table
}

func (f *mappedIntegrationFixture) createTable(source int, name, columns string) {
	f.t.Helper()
	if _, err := f.dbs[source].ExecContext(f.ctx, "CREATE TABLE "+f.qualified(source, name)+" ("+columns+")"); err != nil {
		f.t.Fatal(err)
	}
	f.dropOnCleanup(source, name)
}

func (f *mappedIntegrationFixture) dropOnCleanup(source int, name string) {
	f.t.Cleanup(func() {
		_, _ = f.dbs[source].ExecContext(context.Background(), "DROP TABLE IF EXISTS "+f.qualified(source, name))
	})
}

func (f *mappedIntegrationFixture) exec(source int, statement string, args ...any) {
	f.t.Helper()
	if _, err := f.dbs[source].ExecContext(f.ctx, sourceBind(f.sources[source].Driver, statement), args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *mappedIntegrationFixture) prepare(definition c.Definition, previous, operation string) *httptest.ResponseRecorder {
	if operation == "" {
		operation = c.NewID()
	}
	req := c.PrepareRequest{OperationID: operation, Definition: definition, PreviousDigest: previous, Digest: digest(definition)}
	return f.request("POST", "/internal/publications/prepare", req, true)
}

func (f *mappedIntegrationFixture) activate(definition c.Definition, operation string) {
	f.t.Helper()
	f.mu.Lock()
	f.current[definition.ID] = c.CurrentDefinition{Definition: definition, ETag: digest(definition), Status: "active"}
	f.mu.Unlock()
	response := f.request("POST", "/internal/publications/activate", c.ActivateRequest{DefinitionID: definition.ID, OperationID: operation, Digest: digest(definition)}, true)
	if response.Code != 200 {
		f.t.Fatalf("activate mapped source failed: %d %s", response.Code, response.Body)
	}
}

func (f *mappedIntegrationFixture) query(query c.QuerySpec, status int) c.QueryResult {
	f.t.Helper()
	response := f.request("POST", "/data/query", query, false)
	if response.Code != status {
		f.t.Fatalf("mapped query status=%d want=%d body=%s", response.Code, status, response.Body)
	}
	var result c.QueryResult
	if status == 200 && json.Unmarshal(response.Body.Bytes(), &result) != nil {
		f.t.Fatal("invalid mapped query result")
	}
	for _, property := range result.Fields {
		if property.Mapping != nil || len(property.Default) != 0 {
			f.t.Fatal("mapped query result exposed physical mapping or definition default")
		}
	}
	return result
}

func (f *mappedIntegrationFixture) definition(primary, secondary, mode string) c.Definition {
	primaryNamespace, _ := sourceNamespace(f.sources[0], "")
	secondaryNamespace, _ := sourceNamespace(f.sources[1], "")
	d := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: "跨来源设备", Schema: &c.Schema{Mode: mode, StorageLayout: "single", StorageBinding: &c.StorageBinding{DataSourceID: f.sources[0].ID, Namespace: primaryNamespace, Table: primary, IDColumn: "id", TenantColumn: "tenant"}}}
	d.Schema.Properties = []c.Property{
		{ID: c.NewID(), Name: "设备编码", DataType: "string", Required: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[0].ID, Column: "code"}},
		{ID: c.NewID(), Name: "所属部门", DataType: "string", Required: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[0].ID, Column: "department"}},
		{ID: c.NewID(), Name: "采购价", DataType: "decimal", Precision: 20, Scale: 4, Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[1].ID, Namespace: secondaryNamespace, Table: secondary, Column: "amount", KeyColumn: "equipment_id", TenantColumn: "tenant"}},
	}
	return d
}

func TestMappedSourceIntegration(t *testing.T) {
	for _, backend := range []struct{ driver, env string }{{"mysql", "MYSQL_TEST_DSN"}, {"postgresql", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run("mysql_to_"+backend.driver, func(t *testing.T) {
			f := newMappedIntegrationFixture(t, backend.driver, sourceIntegrationDSN(backend.driver, backend.env))
			suffix := strings.ReplaceAll(c.NewID(), "-", "")[:12]
			primary, secondary := "mapped_primary_"+suffix, "mapped_secondary_"+suffix
			f.createTable(0, primary, "tenant VARCHAR(128) COLLATE utf8mb4_0900_ai_ci NOT NULL,id BIGINT NOT NULL,code VARCHAR(255) NOT NULL,department VARCHAR(255) NOT NULL,PRIMARY KEY(tenant,id)")
			f.createTable(1, secondary, "tenant VARCHAR(128) NOT NULL,equipment_id BIGINT NOT NULL,amount DECIMAL(20,4) NOT NULL,PRIMARY KEY(tenant,equipment_id)")
			for _, row := range []struct {
				tenant           string
				id               int
				code, department string
			}{{f.tenant, 1, "P_1", "D1"}, {f.tenant, 2, "P_2", "D1"}, {f.tenant, 3, "P_3", "D2"}, {strings.ToUpper(f.tenant), 99, "P_99", "D1"}, {f.tenant + " ", 98, "P_98", "D1"}} {
				f.exec(0, "INSERT INTO "+f.qualified(0, primary)+" (tenant,id,code,department) VALUES (?,?,?,?)", row.tenant, row.id, row.code, row.department)
			}
			f.exec(1, "INSERT INTO "+f.qualified(1, secondary)+" (tenant,equipment_id,amount) VALUES (?,?,?)", f.tenant, 1, "9007199254740993.0000")
			f.exec(1, "INSERT INTO "+f.qualified(1, secondary)+" (tenant,equipment_id,amount) VALUES (?,?,?)", strings.ToUpper(f.tenant), 2, "999.0000")
			d := f.definition(primary, secondary, "db_2_schema")
			before := []string{}
			for i, name := range []string{primary, secondary} {
				catalog, err := readSourceTable(f.ctx, f.dbs[i], f.sources[i], "", name)
				if err != nil {
					t.Fatal(err)
				}
				before = append(before, catalog.Signature)
			}
			preview := f.request("POST", "/internal/publications/preview", c.PrepareRequest{Definition: d}, true)
			if preview.Code != 200 {
				t.Fatalf("reverse preview failed: %d %s", preview.Code, preview.Body)
			}
			var plan c.PrepareResult
			if json.Unmarshal(preview.Body.Bytes(), &plan) != nil || len(plan.Statements) != 0 {
				t.Fatal("reverse preview emitted DDL")
			}
			operation := c.NewID()
			prepared := f.prepare(d, "", operation)
			if prepared.Code != 200 {
				t.Fatalf("reverse prepare failed: %d %s", prepared.Code, prepared.Body)
			}
			f.activate(d, operation)
			for i, name := range []string{primary, secondary} {
				catalog, err := readSourceTable(f.ctx, f.dbs[i], f.sources[i], "", name)
				if err != nil || catalog.Signature != before[i] {
					t.Fatal("reverse publication changed source structure")
				}
			}
			code, department, price := d.Schema.Properties[0].ID, d.Schema.Properties[1].ID, d.Schema.Properties[2].ID
			f.decision.RowFilter = &c.Expression{Op: "eq", Field: department, Value: raw("D1")}
			query := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Select: []string{code, price}, Page: c.Page{Limit: 1}}
			first := f.query(query, 200)
			if len(first.Data) != 1 || first.Data[0].ID != "1" || string(first.Data[0].Values[price]) != `"9007199254740993.0000"` || !first.HasMore || first.NextCursor == "" {
				t.Fatalf("cross-source precision/pagination failed: %+v", first)
			}
			query.Page.Cursor = first.NextCursor
			second := f.query(query, 200)
			if len(second.Data) != 1 || second.Data[0].ID != "2" || string(second.Data[0].Values[price]) != "null" || second.HasMore {
				t.Fatalf("missing relationship or exact tenant isolation failed: %+v", second)
			}
			read := f.request("GET", "/data/schemas/"+d.ID+"/instances/1", nil, false)
			if read.Code != 200 || !strings.Contains(read.Body.String(), "9007199254740993.0000") {
				t.Fatalf("mapped single read failed: %d %s", read.Code, read.Body)
			}
			if denied := f.request("GET", "/data/schemas/"+d.ID+"/instances/99", nil, false); denied.Code != 404 {
				t.Fatal("other tenant became an alias in single read")
			}
			query.Page.Cursor = ""
			query.Where = &c.Expression{Op: "eq", Field: price, Value: raw("9007199254740993.0000")}
			f.query(query, 422)
			query.Where = nil
			f.decision.ReadFields = []string{code}
			f.query(query, 403)
			query.Select = []string{code}
			f.decision.RowFilter = &c.Expression{Op: "eq", Field: price, Value: raw("1.0000")}
			f.query(query, 422)
			f.decision.ReadFields = []string{"*"}
			f.decision.RowFilter = nil
			query.Select = []string{code, price}
			all := f.query(query, 200)
			query.Page.Cursor = all.NextCursor
			f.exec(1, "ALTER TABLE "+f.qualified(1, secondary)+" ADD COLUMN extra_note VARCHAR(255) NULL")
			f.query(query, 422)
			for _, method := range []string{"POST", "PATCH", "DELETE"} {
				path := "/data/schemas/" + d.ID + "/instances"
				body := any(c.WriteRequest{Values: map[string]json.RawMessage{code: raw("write")}, IdempotencyKey: c.NewID()})
				if method != "POST" {
					path += "/1"
					body = nil
				}
				response := f.request(method, path, body, false)
				if response.Code != 422 || !strings.Contains(response.Body.String(), "mapped_writes_unsupported") {
					t.Fatalf("external write did not fail explicitly: %s %d %s", method, response.Code, response.Body)
				}
			}
		})
	}
}

func TestMappedForwardPublicationAndInterruptedMultiTableResume(t *testing.T) {
	for _, backend := range []struct{ driver, env string }{{"mysql", "MYSQL_TEST_DSN"}, {"postgresql", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run("mysql_to_"+backend.driver, func(t *testing.T) {
			f := newMappedIntegrationFixture(t, backend.driver, sourceIntegrationDSN(backend.driver, backend.env))
			suffix := strings.ReplaceAll(c.NewID(), "-", "")[:12]
			primary, secondary := "mapped_managed_"+suffix, "mapped_prices_"+suffix
			f.dropOnCleanup(0, primary)
			f.dropOnCleanup(1, secondary)
			d := f.definition(primary, secondary, "schema_2_db")
			preview := f.request("POST", "/internal/publications/preview", c.PrepareRequest{Definition: d}, true)
			if preview.Code != 200 {
				t.Fatalf("forward preview failed: %d %s", preview.Code, preview.Body)
			}
			var before int
			if err := f.dbs[0].QueryRowContext(f.ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=? AND table_name=?", f.sources[0].Database, primary).Scan(&before); err != nil || before != 0 {
				t.Fatal("preview created target table")
			}
			operation := c.NewID()
			response := f.prepare(d, "", operation)
			if response.Code != 200 {
				t.Fatalf("forward prepare failed: %d %s", response.Code, response.Body)
			}
			f.activate(d, operation)
			id := c.NewID()
			f.exec(0, "INSERT INTO "+f.qualified(0, primary)+" (tenant,id,code,department) VALUES (?,?,?,?)", f.tenant, id, "NEW-1", "D1")
			f.exec(1, "INSERT INTO "+f.qualified(1, secondary)+" (tenant,equipment_id,amount) VALUES (?,?,?)", f.tenant, id, "9007199254740993.0000")
			query := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Select: []string{d.Schema.Properties[0].ID, d.Schema.Properties[2].ID}, Page: c.Page{Limit: 10}}
			if result := f.query(query, 200); len(result.Data) != 1 || string(result.Data[0].Values[d.Schema.Properties[2].ID]) != `"9007199254740993.0000"` {
				t.Fatal("forward mapped query did not use published physical columns")
			}
			oldDigest := digest(d)
			next := d
			next.Schema = &c.Schema{Mode: d.Schema.Mode, StorageLayout: d.Schema.StorageLayout, StorageBinding: d.Schema.StorageBinding, Properties: append([]c.Property(nil), d.Schema.Properties...)}
			next.Schema.Properties = append(next.Schema.Properties, c.Property{ID: c.NewID(), Name: "说明", DataType: "string", Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[0].ID, Column: "description"}}, c.Property{ID: c.NewID(), Name: "报价说明", DataType: "string", Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[1].ID, Table: secondary, Column: "price_note", KeyColumn: "equipment_id", TenantColumn: "tenant"}})
			operation = c.NewID()
			// Simulate process loss after the first source committed its additive
			// DDL, with admission and per-source progress durably retained.
			if _, err := f.engine.DB().ExecContext(f.ctx, "UPDATE af_data_gates SET digest=?,previous_digest=?,operation_id=?,status='preparing',definition=?,storage_signature=? WHERE tenant_id=? AND definition_id=?", digest(next), oldDigest, operation, mappedDefinitionJSON(next), storageSignature(next), f.tenant, d.ID); err != nil {
				t.Fatal(err)
			}
			f.exec(0, "ALTER TABLE "+f.qualified(0, primary)+" ADD COLUMN description VARCHAR(255) NULL")
			f.query(query, 409)
			response = f.prepare(next, oldDigest, operation)
			if response.Code != 200 {
				t.Fatalf("interrupted multi-source migration did not resume: %d %s", response.Code, response.Body)
			}
			f.activate(next, operation)
			var complete int
			if err := f.engine.DB().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM af_data_mapped_steps WHERE tenant_id=? AND operation_id=? AND status='prepared'", f.tenant, operation).Scan(&complete); err != nil || complete != 2 {
				t.Fatal("per-source migration progress not durably completed")
			}
			query.Select = append(query.Select, next.Schema.Properties[3].ID, next.Schema.Properties[4].ID)
			result := f.query(query, 200)
			if len(result.Data) != 1 || string(result.Data[0].Values[next.Schema.Properties[3].ID]) != "null" || string(result.Data[0].Values[next.Schema.Properties[4].ID]) != "null" {
				t.Fatal("safe additions changed existing data")
			}
			if retry := f.prepare(next, oldDigest, operation); retry.Code != 200 {
				t.Fatal("completed mapped publication could not be retried idempotently")
			}
			conflict := d
			conflict.ID = c.NewID()
			conflict.Name = "冲突目标"
			if rejected := f.prepare(conflict, "", c.NewID()); rejected.Code != 422 {
				t.Fatalf("another model could claim managed target: %d %s", rejected.Code, rejected.Body)
			}
			var state string
			if err := f.engine.DB().QueryRowContext(f.ctx, "SELECT status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", f.tenant, next.ID).Scan(&state); err != nil || state != "ready" {
				t.Fatal("rejected target conflict paused current model")
			}
		})
	}
}

func TestMappedPreflightRejectsBeforeClosingAdmission(t *testing.T) {
	f := newMappedIntegrationFixture(t, "mysql", os.Getenv("MYSQL_TEST_DSN"))
	suffix := strings.ReplaceAll(c.NewID(), "-", "")[:12]
	primary, secondary := "mapped_preflight_"+suffix, "mapped_preflight_prices_"+suffix
	f.createTable(0, primary, "tenant VARCHAR(128) NOT NULL,id BIGINT NOT NULL,code VARCHAR(255) NOT NULL,department VARCHAR(255) NOT NULL,PRIMARY KEY(tenant,id)")
	f.createTable(1, secondary, "tenant VARCHAR(128) NOT NULL,equipment_id BIGINT NOT NULL,amount DECIMAL(20,4) NOT NULL,PRIMARY KEY(tenant,equipment_id)")
	for _, scenario := range []string{"missing_table", "missing_source", "source_drift", "source_disabled", "unmanaged_forward_table"} {
		t.Run(scenario, func(t *testing.T) {
			d := f.definition(primary, secondary, "db_2_schema")
			switch scenario {
			case "missing_table":
				d.Schema.StorageBinding.Table = "missing_" + suffix
			case "missing_source":
				d.Schema.StorageBinding.DataSourceID = c.NewID()
				d.Schema.Properties[0].Mapping.DataSourceID = d.Schema.StorageBinding.DataSourceID
				d.Schema.Properties[1].Mapping.DataSourceID = d.Schema.StorageBinding.DataSourceID
			case "source_drift":
				d.Schema.Properties[0].Mapping.Column = "missing_column"
			case "source_disabled":
				var raw string
				if err := f.engine.DB().QueryRowContext(f.ctx, "SELECT config_json FROM af_data_sources WHERE tenant_id=? AND id=?", f.tenant, f.sources[0].ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var source c.DataSource
				_ = json.Unmarshal([]byte(raw), &source)
				source.Enabled = false
				body, _ := json.Marshal(source)
				if _, err := f.engine.DB().ExecContext(f.ctx, "UPDATE af_data_sources SET config_json=? WHERE tenant_id=? AND id=?", string(body), f.tenant, source.ID); err != nil {
					t.Fatal(err)
				}
				defer f.engine.DB().ExecContext(f.ctx, "UPDATE af_data_sources SET config_json=? WHERE tenant_id=? AND id=?", raw, f.tenant, source.ID)
			case "unmanaged_forward_table":
				d.Schema.Mode = "schema_2_db"
			}
			response := f.prepare(d, "", c.NewID())
			if response.Code != 422 {
				t.Fatalf("preflight must reject editable draft with422: %d %s", response.Code, response.Body)
			}
			var gates int
			if err := f.engine.DB().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM af_data_gates WHERE tenant_id=? AND definition_id=?", f.tenant, d.ID).Scan(&gates); err != nil || gates != 0 {
				t.Fatal("failed preflight closed admission")
			}
		})
	}
	// The same catalog rejection after a persisted preparing gate must retain a
	// recoverable server failure, rather than claiming the draft was rejected.
	d := f.definition(primary, secondary, "db_2_schema")
	d.Schema.Properties[0].Mapping.Column = "missing_column"
	operation := c.NewID()
	if _, err := f.engine.DB().ExecContext(f.ctx, "INSERT INTO af_data_gates (tenant_id,definition_id,digest,previous_digest,operation_id,status,definition,storage_signature) VALUES (?,?,?,'',?,'preparing',?,?)", f.tenant, d.ID, digest(d), operation, mappedDefinitionJSON(d), storageSignature(d)); err != nil {
		t.Fatal(err)
	}
	if response := f.prepare(d, "", operation); response.Code != 500 {
		t.Fatalf("closed admission error lost recoverability: %d %s", response.Code, response.Body)
	}
	var status string
	if err := f.engine.DB().QueryRowContext(f.ctx, "SELECT status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", f.tenant, d.ID).Scan(&status); err != nil || status != "preparing" {
		t.Fatal("failed resume released the persisted gate")
	}
}
