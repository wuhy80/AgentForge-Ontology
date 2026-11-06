package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	mysqlorm "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// This driver checks the actual connection used by database/sql and GORM. It
// deliberately has no SQL parser and does not claim to emulate MySQL semantics.
type expectation struct {
	op, contains string
	wantTx       bool
	columns      []string
	rows         [][]driver.Value
	err          error
}
type fixture struct {
	mu           sync.Mutex
	expectations []expectation
	failures     []string
}

func (f *fixture) next(op, query string, inTx bool) expectation {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.expectations) == 0 {
		f.failures = append(f.failures, "unexpected "+op+": "+query)
		return expectation{err: errors.New("unexpected database operation")}
	}
	e := f.expectations[0]
	f.expectations = f.expectations[1:]
	if e.op != op || !strings.Contains(query, e.contains) || ((op == "exec" || op == "query") && e.wantTx != inTx) {
		message := fmt.Sprintf("got %s %q tx=%v, wanted %s containing %q tx=%v", op, query, inTx, e.op, e.contains, e.wantTx)
		f.failures = append(f.failures, message)
		return expectation{err: errors.New(message)}
	}
	return e
}
func (f *fixture) assert(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.expectations) > 0 {
		t.Errorf("%d database operations were not observed", len(f.expectations))
	}
	for _, failure := range f.failures {
		t.Error(failure)
	}
}

type fakeConnector struct{ f *fixture }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeConnection{f: c.f}, nil
}
func (c fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use the connector") }

type fakeConnection struct {
	f    *fixture
	inTx bool
}

func (c *fakeConnection) Prepare(q string) (driver.Stmt, error) {
	return &fakeStatement{c: c, q: q}, nil
}
func (c *fakeConnection) Close() error { return nil }
func (c *fakeConnection) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *fakeConnection) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	e := c.f.next("begin", "", c.inTx)
	if e.err != nil {
		return nil, e.err
	}
	if opts.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		return nil, errors.New("transaction isolation was not READ COMMITTED")
	}
	c.inTx = true
	return fakeTransaction{c: c}, nil
}
func (c *fakeConnection) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	e := c.f.next("exec", q, c.inTx)
	return fakeResult{}, e.err
}

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeResult) RowsAffected() (int64, error) { return 1, nil }
func (c *fakeConnection) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	e := c.f.next("query", q, c.inTx)
	if e.err != nil {
		return nil, e.err
	}
	return &fakeRows{columns: e.columns, rows: e.rows}, nil
}

type fakeTransaction struct{ c *fakeConnection }

func (tx fakeTransaction) Commit() error {
	e := tx.c.f.next("commit", "", tx.c.inTx)
	tx.c.inTx = false
	return e.err
}
func (tx fakeTransaction) Rollback() error {
	e := tx.c.f.next("rollback", "", tx.c.inTx)
	tx.c.inTx = false
	return e.err
}

type fakeStatement struct {
	c *fakeConnection
	q string
}

func (s *fakeStatement) Close() error  { return nil }
func (s *fakeStatement) NumInput() int { return -1 }
func (s *fakeStatement) Exec(args []driver.Value) (driver.Result, error) {
	return s.c.ExecContext(context.Background(), s.q, namedValues(args))
}
func (s *fakeStatement) Query(args []driver.Value) (driver.Rows, error) {
	return s.c.QueryContext(context.Background(), s.q, namedValues(args))
}
func namedValues(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}

type fakeRows struct {
	columns []string
	rows    [][]driver.Value
	pos     int
}

func (r *fakeRows) Columns() []string { return r.columns }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

func newFixtureDB(t *testing.T, expectations ...expectation) (*sql.DB, *fixture) {
	t.Helper()
	f := &fixture{expectations: expectations}
	db := sql.OpenDB(fakeConnector{f: f})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close(); f.assert(t) })
	return db, f
}
func newFixtureEngine(t *testing.T, expectations ...expectation) *Engine {
	t.Helper()
	db, _ := newFixtureDB(t, expectations...)
	r := NewRegistry()
	if err := r.Register("fixture", func(context.Context, Config) (*sql.DB, gorm.Dialector, Capabilities, error) {
		return db, mysqlorm.New(mysqlorm.Config{Conn: db, SkipInitializeWithVersion: true}), Capabilities{Driver: "fixture", LocalTransactions: true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	engine, err := r.Open(context.Background(), Config{Driver: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestSessionORMAndSQLRollbackTogether(t *testing.T) {
	engine := newFixtureEngine(t, expectation{op: "begin"}, expectation{op: "exec", contains: "INSERT INTO raw_records", wantTx: true}, expectation{op: "exec", contains: "INSERT INTO `orm_records`", wantTx: true}, expectation{op: "rollback"})
	reject := errors.New("reject business operation")
	err := engine.WithSession(context.Background(), func(s *Session) error {
		if _, err := s.ExecContext(context.Background(), "INSERT INTO raw_records (id) VALUES (?)", "a"); err != nil {
			return err
		}
		if err := s.Create("orm_records", &struct {
			ID    string
			Value string
		}{ID: "b", Value: "中文值"}); err != nil {
			return err
		}
		return reject
	})
	if !errors.Is(err, reject) {
		t.Fatalf("got %v", err)
	}
}

func TestSessionORMAndSQLCommitTogether(t *testing.T) {
	engine := newFixtureEngine(t, expectation{op: "begin"}, expectation{op: "exec", contains: "INSERT INTO `orm_records`", wantTx: true}, expectation{op: "exec", contains: "INSERT INTO outbox", wantTx: true}, expectation{op: "commit"})
	err := engine.WithSession(context.Background(), func(s *Session) error {
		if err := s.Create("orm_records", &struct{ ID string }{ID: "a"}); err != nil {
			return err
		}
		_, err := s.ExecContext(context.Background(), "INSERT INTO outbox (id) VALUES (?)", "a")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTransactionPanicRollsBack(t *testing.T) {
	engine := newFixtureEngine(t, expectation{op: "begin"}, expectation{op: "rollback"})
	defer func() {
		if recover() != "boom" {
			t.Error("panic was not propagated")
		}
	}()
	_ = engine.WithTx(context.Background(), func(*sql.Tx) error { panic("boom") })
}

func TestRegistryRejectsUnknownDuplicateAndLateAdapters(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("mysql", connectMySQL); !errors.Is(err, ErrDuplicateDriver) {
		t.Fatalf("duplicate adapter: %v", err)
	}
	if _, err := r.Open(context.Background(), Config{Driver: "postgres"}); !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("unknown adapter: %v", err)
	}
	if err := r.Register("later", connectMySQL); err == nil {
		t.Fatal("registry accepted a change after first open")
	}
}

func lockPrefix() []expectation {
	return []expectation{{op: "query", contains: "SELECT DATABASE()", columns: []string{"database"}, rows: [][]driver.Value{{"fixture"}}}, {op: "query", contains: "SELECT GET_LOCK", columns: []string{"lock"}, rows: [][]driver.Value{{int64(1)}}}}
}
func unlock() expectation {
	return expectation{op: "query", contains: "SELECT RELEASE_LOCK", columns: []string{"released"}, rows: [][]driver.Value{{int64(1)}}}
}
func journalRow(checksum, status string) expectation {
	return expectation{op: "query", contains: "SELECT checksum, status", columns: []string{"checksum", "status"}, rows: [][]driver.Value{{checksum, status}}}
}

func TestMigrationDoesNotReplayUnknownDDL(t *testing.T) {
	m := Migration{ID: "001_entities", SQL: "CREATE TABLE entities (id VARCHAR(36) NOT NULL PRIMARY KEY) ENGINE=InnoDB"}
	expect := lockPrefix()
	expect = append(expect, expectation{op: "exec", contains: "CREATE TABLE IF NOT EXISTS af_storage_migrations"}, journalRow(MigrationChecksum(m), "running"), unlock())
	db, _ := newFixtureDB(t, expect...)
	if err := Migrate(context.Background(), db, "data", []Migration{m}); !errors.Is(err, ErrMigrationNeedsRepair) {
		t.Fatalf("got %v", err)
	}
}

func TestMigrationRejectsChecksumChange(t *testing.T) {
	m := Migration{ID: "001", SQL: "CREATE TABLE example (id INT) ENGINE=InnoDB"}
	expect := lockPrefix()
	expect = append(expect, expectation{op: "exec", contains: "CREATE TABLE IF NOT EXISTS af_storage_migrations"}, journalRow("different-checksum", "succeeded"), unlock())
	db, _ := newFixtureDB(t, expect...)
	if err := Migrate(context.Background(), db, "metadata", []Migration{m}); !errors.Is(err, ErrMigrationChanged) {
		t.Fatalf("got %v", err)
	}
}

func TestMigrationRecordsFailedDDL(t *testing.T) {
	m := Migration{ID: "001", SQL: "CREATE TABLE example (id INT) ENGINE=InnoDB"}
	expect := lockPrefix()
	expect = append(expect, expectation{op: "exec", contains: "CREATE TABLE IF NOT EXISTS af_storage_migrations"}, expectation{op: "query", contains: "SELECT checksum, status", columns: []string{"checksum", "status"}}, expectation{op: "exec", contains: "INSERT INTO af_storage_migrations"}, expectation{op: "exec", contains: m.SQL, err: errors.New("connection lost; DDL outcome unknown")}, expectation{op: "exec", contains: "status = 'needs_repair'"}, unlock())
	db, _ := newFixtureDB(t, expect...)
	if err := Migrate(context.Background(), db, "metadata", []Migration{m}); !errors.Is(err, ErrMigrationNeedsRepair) {
		t.Fatalf("got %v", err)
	}
}

func TestMigrationSuccessAndSubsequentNoop(t *testing.T) {
	m := Migration{ID: "001", SQL: "CREATE TABLE example (id INT) ENGINE=InnoDB"}
	expect := lockPrefix()
	expect = append(expect, expectation{op: "exec", contains: "CREATE TABLE IF NOT EXISTS af_storage_migrations"}, expectation{op: "query", contains: "SELECT checksum, status", columns: []string{"checksum", "status"}}, expectation{op: "exec", contains: "INSERT INTO af_storage_migrations"}, expectation{op: "exec", contains: m.SQL}, expectation{op: "exec", contains: "status = 'succeeded'"}, unlock())
	expect = append(expect, lockPrefix()...)
	expect = append(expect, expectation{op: "exec", contains: "CREATE TABLE IF NOT EXISTS af_storage_migrations"}, journalRow(MigrationChecksum(m), "succeeded"), unlock())
	db, _ := newFixtureDB(t, expect...)
	if err := Migrate(context.Background(), db, "metadata", []Migration{m}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db, "metadata", []Migration{m}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationCompletionRequiresSuccessfulStructureVerification(t *testing.T) {
	m := Migration{ID: "001", SQL: "CREATE TABLE example (id INT) ENGINE=InnoDB"}
	expect := lockPrefix()
	expect = append(expect, journalRow(MigrationChecksum(m), "needs_repair"), expectation{op: "query", contains: "SHOW COLUMNS FROM example", columns: []string{"field"}, rows: [][]driver.Value{{"wrong_field"}}}, unlock())
	db, _ := newFixtureDB(t, expect...)
	err := CompleteMigration(context.Background(), db, "metadata", m, func(ctx context.Context, conn *sql.Conn) error {
		var field string
		if err := conn.QueryRowContext(ctx, "SHOW COLUMNS FROM example").Scan(&field); err != nil {
			return err
		}
		if field != "id" {
			return errors.New("target column mismatch")
		}
		return nil
	})
	if !errors.Is(err, ErrMigrationNeedsRepair) {
		t.Fatalf("got %v", err)
	}
}

func TestMigrationRejectsDuplicateIDsBeforeDatabaseAccess(t *testing.T) {
	err := Migrate(context.Background(), nil, "data", []Migration{{ID: "001", SQL: "A"}, {ID: "001", SQL: "B"}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("got %v", err)
	}
}
