package data

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func TestMySQLSourceConfigurationLifecycleIsolationAndBoundEndpoint(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set; real source catalog persistence not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal("cannot open isolated source configuration test store")
	}
	t.Cleanup(func() { engine.Close() })
	if err = storage.Migrate(ctx, engine.DB(), "data", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant, other := c.NewID(), c.NewID()
	t.Cleanup(func() {
		engine.DB().Exec("DELETE FROM af_data_gates WHERE tenant_id IN (?,?)", tenant, other)
		engine.DB().Exec("DELETE FROM af_data_sources WHERE tenant_id IN (?,?)", tenant, other)
	})
	app := NewHandler(Config{Engine: engine, DataSourceKey: strings.Repeat("integration-encryption-key", 2), Authorizer: authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		return c.Decision{Allow: true, State: "current"}, nil
	})})
	request := func(tenant, method, path string, input any, etag string, admin bool) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		p := c.Principal{TenantID: tenant, Subject: "source-admin"}
		ctx := platform.WithPrincipal(ctx, p)
		ctx = platform.WithSessionUser(ctx, c.User{ID: p.Subject, TenantID: tenant, Enabled: true, Administrator: admin})
		r = r.WithContext(ctx)
		if etag != "" {
			r.Header.Set("If-Match", `"`+etag+`"`)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	input := sourceTestInput()
	input.Host, input.Database, input.TenantIsolation = "source-fixture.invalid", "fixture_"+strings.ReplaceAll(c.NewID(), "-", ""), "dedicated"
	w := request(tenant, "POST", "/data/sources", input, "", true)
	if w.Code != 201 || strings.Contains(w.Body.String(), *input.Password) || strings.Contains(w.Body.String(), `"password":`) {
		t.Fatalf("source creation failed or leaked password: %d", w.Code)
	}
	var source c.DataSource
	if json.Unmarshal(w.Body.Bytes(), &source) != nil || !source.PasswordSet {
		t.Fatal("source creation response missing safe credential marker")
	}
	var persisted string
	if err = engine.DB().QueryRowContext(ctx, "SELECT CONCAT(config_json,secret_cipher) FROM af_data_sources WHERE tenant_id=? AND id=?", tenant, source.ID).Scan(&persisted); err != nil || strings.Contains(persisted, *input.Password) {
		t.Fatal("plaintext database password was persisted")
	}
	w = request(other, "GET", "/data/sources/"+source.ID, nil, "", true)
	if w.Code != 404 {
		t.Fatal("cross-tenant source read was accepted")
	}
	w = request(other, "POST", "/data/sources", input, "", true)
	if w.Code != 409 {
		t.Fatal("dedicated target could be registered by two tenants")
	}
	w = request(tenant, "GET", "/data/sources?view=select", nil, "", false)
	if w.Code != 200 || strings.Contains(w.Body.String(), input.Host) || strings.Contains(w.Body.String(), input.Username) {
		t.Fatal("model selector leaked connection details")
	}
	input.Name, input.Password = "更新的数据源", nil
	w = request(tenant, "PUT", "/data/sources/"+source.ID, input, "", true)
	if w.Code != 428 {
		t.Fatal("source update accepted missing precondition")
	}
	w = request(tenant, "PUT", "/data/sources/"+source.ID, input, "stale", true)
	if w.Code != 412 {
		t.Fatal("source update accepted stale precondition")
	}
	w = request(tenant, "PUT", "/data/sources/"+source.ID, input, source.ETag, true)
	if w.Code != 200 {
		t.Fatalf("valid source update failed: %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &source)
	if !source.PasswordSet {
		t.Fatal("omitted password erased stored credentials")
	}
	definition := c.Definition{ID: c.NewID(), TenantID: tenant, Kind: c.KindSchema, Schema: &c.Schema{Properties: []c.Property{{ID: c.NewID(), Mapping: &c.PropertyMapping{DataSourceID: source.ID, Column: "external_name"}}}}}
	body, _ := json.Marshal(definition)
	if _, err = engine.DB().ExecContext(ctx, "INSERT INTO af_data_gates (tenant_id,definition_id,digest,previous_digest,operation_id,status,definition,storage_signature) VALUES (?,?,?,'',?,'ready',?,?)", tenant, definition.ID, strings.Repeat("d", 64), c.NewID(), string(body), strings.Repeat("s", 64)); err != nil {
		t.Fatal(err)
	}
	input.Database += "_other"
	w = request(tenant, "PUT", "/data/sources/"+source.ID, input, source.ETag, true)
	if w.Code != 409 {
		t.Fatal("property-only bound source target was silently changed")
	}
	input.Database = strings.ToUpper(source.Database)
	w = request(tenant, "PUT", "/data/sources/"+source.ID, input, source.ETag, true)
	if w.Code != 409 {
		t.Fatal("case-folded endpoint guard allowed redirecting a bound MySQL database")
	}
	input.Database = source.Database
	input.Enabled = false
	w = request(tenant, "PUT", "/data/sources/"+source.ID, input, source.ETag, true)
	if w.Code != 200 {
		t.Fatalf("bound source disable was rejected: %d", w.Code)
	}
	w = request(tenant, "POST", "/data/sources/"+source.ID+"/test", nil, "", true)
	if w.Code != 409 {
		t.Fatal("disabled source still opened a network connection")
	}
	// The endpoint guard must prevent both concurrent tenant registrations from
	// claiming the same dedicated physical database before either commit.
	raceInput := input
	raceInput.Name, raceInput.Database, raceInput.Enabled = "并发专属源", input.Database+"_race", true
	codes := make(chan int, 2)
	for _, scope := range []string{tenant, other} {
		go func(scope string) { codes <- request(scope, "POST", "/data/sources", raceInput, "", true).Code }(scope)
	}
	first, second := <-codes, <-codes
	if !((first == 201 && second == 409) || (first == 409 && second == 201)) {
		t.Fatalf("dedicated endpoint registration was not serialized: %d/%d", first, second)
	}
	w = request(tenant, "GET", "/data/sources/"+source.ID, nil, "", true)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &source) != nil {
		t.Fatal("cannot reload source for concurrency check")
	}
	for _, name := range []string{"并发改名一", "并发改名二"} {
		go func(name string) {
			changed := input
			changed.Name = name
			codes <- request(tenant, "PUT", "/data/sources/"+source.ID, changed, source.ETag, true).Code
		}(name)
	}
	first, second = <-codes, <-codes
	if !((first == 200 && second == 412) || (first == 412 && second == 200)) {
		t.Fatalf("source ETag updates were not serialized: %d/%d", first, second)
	}
}

func TestSourceCatalogRealDrivers(t *testing.T) {
	for _, backend := range []struct{ driver, sqlDriver, env string }{{"mysql", "mysql", "MYSQL_TEST_DSN"}, {"postgresql", "pgx", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			dsn := os.Getenv(backend.env)
			if dsn == "" {
				t.Skip(backend.env + " is not set; real source discovery not run")
			}
			db, err := sql.Open(backend.sqlDriver, dsn)
			if err != nil {
				t.Fatal("source driver cannot open isolated test connection")
			}
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			source := c.DataSource{Driver: backend.driver}
			if backend.driver == "mysql" {
				if err = db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&source.Database); err != nil {
					t.Fatal(err)
				}
			}
			namespace, err := sourceNamespace(source, "")
			if err != nil {
				t.Fatal(err)
			}
			name := "source_catalog_" + strings.ReplaceAll(c.NewID(), "-", "")[:12]
			qualified, _ := sourceQuote(source.Driver, namespace)
			quoted, _ := sourceQuote(source.Driver, name)
			qualified += "." + quoted
			statement := "CREATE TABLE " + qualified + " (id BIGINT NOT NULL PRIMARY KEY, account VARCHAR(100) NOT NULL UNIQUE, part_a BIGINT NOT NULL, part_b BIGINT NOT NULL, amount DECIMAL(20,4), UNIQUE (part_a,part_b))"
			if _, err = db.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
			defer db.ExecContext(context.Background(), "DROP TABLE "+qualified)
			if source.Driver == "postgresql" {
				idx, _ := sourceQuote(source.Driver, name+"_partial")
				if _, err = db.ExecContext(ctx, "CREATE UNIQUE INDEX "+idx+" ON "+qualified+" (part_a) WHERE part_b>0"); err != nil {
					t.Fatal(err)
				}
			}
			table, err := readSourceTable(ctx, db, source, namespace, name)
			if err != nil {
				t.Fatal(err)
			}
			if len(table.Columns) != 5 || !table.Columns[0].PrimaryKey || table.Columns[0].DataType != "int64" || table.Columns[4].Precision != 20 || table.Columns[4].Scale != 4 || !table.Columns[4].Nullable {
				t.Fatalf("source types or constraints were lost: %#v", table.Columns)
			}
			primary, unique, composite, partial := false, false, false, false
			for _, index := range table.Indexes {
				primary = primary || index.Primary && len(index.Columns) == 1 && index.Columns[0] == "id"
				unique = unique || index.Unique && len(index.Columns) == 1 && index.Columns[0] == "account"
				composite = composite || index.Unique && len(index.Columns) == 2
				partial = partial || index.Predicate != ""
			}
			if !primary || !unique || !composite || source.Driver == "postgresql" && !partial {
				t.Fatalf("index uniqueness scope was lost: %#v", table.Indexes)
			}
			again, err := readSourceTable(ctx, db, source, namespace, name)
			if err != nil || again.Signature != table.Signature {
				t.Fatal("repeated discovery changed stable structure digest")
			}
			items, err := listSourceTables(ctx, db, source, namespace)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range items {
				found = found || item.Name == name
				if strings.HasPrefix(item.Name, "af_") {
					t.Fatal("platform private table leaked into source selector")
				}
			}
			if !found {
				t.Fatal("created table missing from source selector")
			}
		})
	}
}

func TestSourceHTTPConnectionAndCatalogRealDrivers(t *testing.T) {
	platformDSN := os.Getenv("MYSQL_TEST_DSN")
	if platformDSN == "" {
		t.Skip("MYSQL_TEST_DSN is not set; source HTTP persistence and live connections not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: platformDSN})
	if err != nil {
		t.Fatal("cannot open isolated source HTTP test store")
	}
	t.Cleanup(func() { engine.Close() })
	if err = storage.Migrate(ctx, engine.DB(), "data", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant := c.NewID()
	t.Cleanup(func() { engine.DB().Exec("DELETE FROM af_data_sources WHERE tenant_id=?", tenant) })
	app := NewHandler(Config{Engine: engine, DataSourceKey: strings.Repeat("source-http-fixture-key", 2)})
	request := func(method, path string, input any) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		p := c.Principal{TenantID: tenant, Subject: "source-http-admin"}
		ctx := platform.WithSessionUser(platform.WithPrincipal(ctx, p), c.User{ID: p.Subject, TenantID: p.TenantID, Administrator: true, Enabled: true})
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r.WithContext(ctx))
		return w
	}
	for _, backend := range []struct{ driver, sqlDriver, env string }{{"mysql", "mysql", "MYSQL_TEST_DSN"}, {"postgresql", "pgx", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			dsn := os.Getenv(backend.env)
			if dsn == "" {
				t.Skip(backend.env + " is not set; live source HTTP test not run")
			}
			input := c.DataSourceInput{Name: "HTTP源-" + backend.driver, Driver: backend.driver, TLSMode: "disable", TenantIsolation: "column", Enabled: true}
			password := ""
			if backend.driver == "mysql" {
				cfg, err := mysql.ParseDSN(dsn)
				if err != nil {
					t.Fatal("invalid isolated MySQL test DSN")
				}
				input.Host, input.Port = "127.0.0.1", 3306
				if host, port, err := net.SplitHostPort(cfg.Addr); err == nil {
					input.Host = host
					input.Port, _ = strconv.Atoi(port)
				}
				input.Database, input.Username, password = cfg.DBName, cfg.User, cfg.Passwd
			} else {
				u, err := url.Parse(dsn)
				if err != nil || u.User == nil {
					t.Fatal("invalid isolated PostgreSQL test DSN")
				}
				input.Host, input.Database, input.Username = u.Hostname(), strings.TrimPrefix(u.Path, "/"), u.User.Username()
				input.Port, _ = strconv.Atoi(u.Port())
				password, _ = u.User.Password()
			}
			input.Password = &password
			db, err := sql.Open(backend.sqlDriver, dsn)
			if err != nil {
				t.Fatal("cannot open isolated source HTTP test connection")
			}
			defer db.Close()
			sourceConfig, err := sourceConfig(input)
			if err != nil {
				t.Fatal(err)
			}
			namespace, err := sourceNamespace(sourceConfig, "")
			if err != nil {
				t.Fatal(err)
			}
			name := "source_http_" + strings.ReplaceAll(c.NewID(), "-", "")[:12]
			quotedNS, _ := sourceQuote(backend.driver, namespace)
			quotedTable, _ := sourceQuote(backend.driver, name)
			qualified := quotedNS + "." + quotedTable
			if _, err = db.ExecContext(ctx, "CREATE TABLE "+qualified+" (id BIGINT PRIMARY KEY, business_name VARCHAR(100) NOT NULL)"); err != nil {
				t.Fatal(err)
			}
			defer db.ExecContext(context.Background(), "DROP TABLE "+qualified)
			w := request("POST", "/data/sources/test", input)
			if w.Code != 200 {
				t.Fatalf("unsaved connection test failed: status=%d, body=%s", w.Code, w.Body)
			}
			w = request("POST", "/data/sources", input)
			if w.Code != 201 {
				t.Fatalf("source persistence failed: status=%d", w.Code)
			}
			if password != "" && strings.Contains(w.Body.String(), password) {
				t.Fatal("source HTTP response disclosed credentials")
			}
			var saved c.DataSource
			if json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.ID == "" {
				t.Fatal("source response not usable")
			}
			w = request("POST", "/data/sources/"+saved.ID+"/test", nil)
			if w.Code != 200 {
				t.Fatalf("persisted encrypted source connection test failed: status=%d", w.Code)
			}
			var result c.DataSourceTestResult
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Success || result.ServerVersion == "" {
				t.Fatal("live source did not report a successful connection")
			}
			w = request("GET", "/data/sources/"+saved.ID+"/tables", nil)
			if w.Code != 200 || !strings.Contains(w.Body.String(), name) {
				t.Fatalf("live table selection failed: status=%d", w.Code)
			}
			w = request("GET", "/data/sources/"+saved.ID+"/tables/"+name+"?namespace="+url.QueryEscape(namespace), nil)
			if w.Code != 200 {
				t.Fatalf("live column selection failed: status=%d", w.Code)
			}
			var table c.SourceTable
			if json.Unmarshal(w.Body.Bytes(), &table) != nil || len(table.Columns) != 2 || !table.Columns[0].PrimaryKey || table.Columns[1].Name != "business_name" {
				t.Fatal("source HTTP catalog lost physical mapping identity")
			}
			w = request("GET", "/data/sources/"+saved.ID+"/tables/af_identity_users", nil)
			if w.Code != 422 {
				t.Fatal("HTTP catalog allowed platform-private source mapping")
			}
		})
	}
}
