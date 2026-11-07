package data

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// This connector exercises database/sql's actual connection acquisition and
// optional driver interfaces; it does not emulate a database's SQL semantics.
type sourceVersionFixtureConnector struct {
	mu          sync.Mutex
	connections []*sourceVersionFixtureConn
	next        int
	connectErr  error
}

func (f *sourceVersionFixtureConnector) Connect(context.Context) (driver.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connectErr != nil {
		return nil, f.connectErr
	}
	if f.next >= len(f.connections) {
		return nil, errors.New("unexpected additional physical connection")
	}
	conn := f.connections[f.next]
	f.next++
	return conn, nil
}

func (*sourceVersionFixtureConnector) Driver() driver.Driver { return sourceVersionFixtureDriver{} }

type sourceVersionFixtureDriver struct{}

func (sourceVersionFixtureDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the connector")
}

type sourceVersionFixtureConn struct {
	mu            sync.Mutex
	version       string
	sqlMode       string
	inspectionErr error
	inspections   int
	business      int
	pings         int
	closes        int
}

func newVersionFixtureConn() *sourceVersionFixtureConn {
	return &sourceVersionFixtureConn{version: "26.7.0", sqlMode: "STRICT_TRANS_TABLES"}
}

func (f *sourceVersionFixtureConn) Prepare(query string) (driver.Stmt, error) {
	return &sourceVersionFixtureStatement{conn: f, query: query}, nil
}
func (*sourceVersionFixtureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (f *sourceVersionFixtureConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	return nil
}
func (f *sourceVersionFixtureConn) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closes > 0 || f.inspections == 0 {
		return errors.New("ping used an uninspected or closed physical connection")
	}
	f.pings++
	return nil
}
func (f *sourceVersionFixtureConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closes > 0 || f.inspections == 0 || !strings.HasPrefix(query, "UPDATE fixture_business") {
		return nil, errors.New("business SQL used an uninspected or closed physical connection")
	}
	f.business++
	return driver.RowsAffected(1), nil
}
func (f *sourceVersionFixtureConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closes > 0 || !strings.HasPrefix(query, "SELECT VERSION(),@@version_comment") {
		return nil, errors.New("unexpected query or closed physical connection")
	}
	f.inspections++
	if f.inspectionErr != nil {
		return nil, f.inspectionErr
	}
	return &sourceVersionFixtureRows{values: []driver.Value{f.version, "MySQL Community Server - GPL", "InnoDB", "utf8mb4", "utf8mb4_general_ci", f.sqlMode, "SYSTEM", true}}, nil
}

type sourceVersionFixtureStatement struct {
	conn  *sourceVersionFixtureConn
	query string
}

func (*sourceVersionFixtureStatement) Close() error  { return nil }
func (*sourceVersionFixtureStatement) NumInput() int { return -1 }
func (s *sourceVersionFixtureStatement) Exec([]driver.Value) (driver.Result, error) {
	return s.conn.ExecContext(context.Background(), s.query, nil)
}
func (s *sourceVersionFixtureStatement) Query([]driver.Value) (driver.Rows, error) {
	return s.conn.QueryContext(context.Background(), s.query, nil)
}

type sourceVersionFixtureRows struct {
	values []driver.Value
	read   bool
}

func (*sourceVersionFixtureRows) Columns() []string {
	return []string{"version", "comment", "engine", "charset", "collation", "sql_mode", "timezone", "binary_collation"}
}
func (*sourceVersionFixtureRows) Close() error { return nil }
func (r *sourceVersionFixtureRows) Next(values []driver.Value) error {
	if r.read {
		return io.EOF
	}
	copy(values, r.values)
	r.read = true
	return nil
}

func sourceFixtureCheckedConnector(connections ...*sourceVersionFixtureConn) *checkedSourceConnector {
	return &checkedSourceConnector{
		base:   &sourceVersionFixtureConnector{connections: connections},
		source: c.DataSource{Driver: "mysql", VersionPolicy: "pinned", ExpectedVersionLine: "26.7"},
	}
}

func requireSourceVersionCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure *apiError
	if !errors.As(err, &failure) || failure.status != 422 || failure.code != code {
		t.Fatalf("wanted 422 %s, got %v", code, err)
	}
}

func TestCheckedSourceConnectorInspectsEveryPhysicalConnectionAndPreservesOptionalAPIs(t *testing.T) {
	first, second := newVersionFixtureConn(), newVersionFixtureConn()
	checked := sourceFixtureCheckedConnector(first, second)
	db := sql.OpenDB(checked)
	db.SetMaxOpenConns(2)
	defer db.Close()
	ctx := context.Background()
	left, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	for _, conn := range []*sql.Conn{left, right} {
		if err := conn.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, "UPDATE fixture_business SET value=?", "bound-value"); err != nil {
			t.Fatal(err)
		}
	}
	for _, conn := range []*sourceVersionFixtureConn{first, second} {
		if conn.inspections != 1 || conn.business != 1 || conn.pings != 1 || conn.closes != 0 {
			t.Fatalf("physical connection was not independently inspected and retained: %+v", conn)
		}
	}
	if !checked.connected || checked.baseline == "" || checked.observed.ServerVersion != "26.7.0" {
		t.Fatal("connection observations were not retained")
	}
}

func TestCheckedSourceConnectorRejectsChangedPoolConnectionsBeforeBusinessSQL(t *testing.T) {
	for _, scenario := range []string{"proxy_configuration", "unsupported_patch", "wrong_series", "inspection_failure"} {
		t.Run(scenario, func(t *testing.T) {
			first, replacement := newVersionFixtureConn(), newVersionFixtureConn()
			wantCode := "source_version_changed"
			switch scenario {
			case "proxy_configuration":
				replacement.sqlMode += ",NO_BACKSLASH_ESCAPES"
			case "unsupported_patch":
				replacement.version = "26.7.1"
				wantCode = "unsupported_database_version"
			case "wrong_series":
				replacement.version = "27.0.0"
				wantCode = "database_version_mismatch"
			case "inspection_failure":
				replacement.inspectionErr = errors.New("driver error including password=secret")
				wantCode = "database_version_unavailable"
			}
			checked := sourceFixtureCheckedConnector(first, replacement)
			db := sql.OpenDB(checked)
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(0) // Force the next use to create a replacement session.
			defer db.Close()
			if _, err := db.ExecContext(context.Background(), "UPDATE fixture_business SET value=?", "first"); err != nil {
				t.Fatal(err)
			}
			baseline := checked.baseline
			_, err := db.ExecContext(context.Background(), "UPDATE fixture_business SET value=?", "must-not-run")
			requireSourceVersionCode(t, err, wantCode)
			if strings.Contains(err.Error(), "secret") || replacement.business != 0 || replacement.inspections != 1 || replacement.closes != 1 || first.business != 1 || first.closes != 1 || checked.baseline != baseline {
				t.Fatal("rejected connection leaked diagnostics, executed SQL, changed baseline or remained open")
			}
		})
	}
}

func TestCheckedSourceConnectorConcurrentProxyBackendsCannotBothPass(t *testing.T) {
	first, second := newVersionFixtureConn(), newVersionFixtureConn()
	second.sqlMode += ",NO_BACKSLASH_ESCAPES"
	checked := sourceFixtureCheckedConnector(first, second)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := checked.Connect(context.Background())
			if conn != nil {
				_ = conn.Close()
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	passed, rejected := 0, 0
	for err := range results {
		if err == nil {
			passed++
		} else {
			requireSourceVersionCode(t, err, "source_version_changed")
			rejected++
		}
	}
	if passed != 1 || rejected != 1 || first.closes != 1 || second.closes != 1 || first.business+second.business != 0 {
		t.Fatal("heterogeneous initial connections both passed or connection cleanup failed")
	}
}

func TestCheckedSourceConnectorSanitizesConnectionFailure(t *testing.T) {
	checked := &checkedSourceConnector{base: &sourceVersionFixtureConnector{connectErr: errors.New("secret DSN password=private")}}
	conn, err := checked.Connect(context.Background())
	requireSourceVersionCode(t, err, "source_connection_failed")
	if conn != nil || checked.connected || strings.Contains(err.Error(), "private") {
		t.Fatal("failed connection leaked credentials or was marked inspected")
	}
}

func TestMappedCursorInvalidatesAfterPrimaryOrProjectedSourceVersionChanges(t *testing.T) {
	for _, changed := range []int{0, 1} {
		d, tables := mappedFixture()
		for _, table := range tables {
			table.source.Compatibility = &c.SourceCompatibility{Signature: "original-server-version"}
		}
		principal, decision := c.Principal{TenantID: d.TenantID, Subject: "reader"}, allowed()
		decision.ReadFields = []string{"*"}
		query := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Select: []string{testFieldCode, testFieldPrice}, Page: c.Page{Limit: 1}}
		secret := strings.Repeat("cursor-secret", 4)
		compiled, err := compileMappedQuery(d, tables, "current", principal, decision, query, secret, "")
		if err != nil {
			t.Fatal(err)
		}
		query.Page.Cursor, err = signCursor(secret, pageCursor{TenantID: d.TenantID, Subject: principal.Subject, SchemaID: d.ID, Digest: "current", Authorization: digest(decision), Fingerprint: compiled.Fingerprint, LastID: c.NewID(), Expires: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = compileMappedQuery(d, tables, "current", principal, decision, query, secret, ""); err != nil {
			t.Fatal("unchanged version rejected valid cursor", err)
		}
		tables[changed].source.Compatibility.Signature = "changed-server-version-or-configuration"
		_, err = compileMappedQuery(d, tables, "current", principal, decision, query, secret, "")
		requireSourceVersionCode(t, err, "invalid_cursor")
	}
}

func TestSourceVersionOptionsRequireCatalogAuthorization(t *testing.T) {
	denied := NewHandler(Config{Authorizer: authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		return c.Decision{Allow: false}, nil
	})})
	w := httptest.NewRecorder()
	denied.ServeHTTP(w, sourceRequest("GET", "/data/source-versions", false))
	if w.Code != 403 {
		t.Fatalf("version directory bypassed catalog authorization: %d", w.Code)
	}
	for _, admin := range []bool{false, true} {
		app := NewHandler(Config{Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, request c.AuthorizationRequest) (c.Decision, error) {
			if request.Operation != "definition.edit" {
				t.Fatal("version options used an unrelated authorization")
			}
			return c.Decision{Allow: true, State: "current"}, nil
		})})
		w = httptest.NewRecorder()
		app.ServeHTTP(w, sourceRequest("GET", "/data/source-versions", admin))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"versionLine":"26.7"`) || strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "serverVersion") {
			t.Fatal("version directory missing supported versions or exposing server details")
		}
	}
}
