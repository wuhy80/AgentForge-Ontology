package data

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type sourceSessionCheckerFunc func(context.Context, c.SessionCheck) (c.Session, error)

func (f sourceSessionCheckerFunc) CheckSession(ctx context.Context, check c.SessionCheck) (c.Session, error) {
	return f(ctx, check)
}

type catalogFixtureConnector struct {
	query func(string) (driver.Rows, error)
}

func (f *catalogFixtureConnector) Connect(context.Context) (driver.Conn, error) {
	return &catalogFixtureConn{f}, nil
}
func (*catalogFixtureConnector) Driver() driver.Driver { return sourceVersionFixtureDriver{} }

type catalogFixtureConn struct{ fixture *catalogFixtureConnector }

func (*catalogFixtureConn) Close() error { return nil }
func (*catalogFixtureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (*catalogFixtureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (f *catalogFixtureConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	return f.fixture.query(query)
}

type catalogFixtureRows struct {
	columns  []string
	values   [][]driver.Value
	position int
}

func (r *catalogFixtureRows) Columns() []string { return r.columns }
func (*catalogFixtureRows) Close() error        { return nil }
func (r *catalogFixtureRows) Next(dest []driver.Value) error {
	if r.position >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.position])
	r.position++
	return nil
}
func catalogRows(columns []string, values ...[]driver.Value) driver.Rows {
	return &catalogFixtureRows{columns: columns, values: values}
}

func TestSourceOverviewUnavailableMetricsNeverBecomeZero(t *testing.T) {
	for _, driverName := range []string{"mysql", "postgresql"} {
		db := sql.OpenDB(&catalogFixtureConnector{query: func(string) (driver.Rows, error) { return nil, errors.New("private permission details") }})
		result := readSourceOverview(context.Background(), db, c.DataSource{Driver: driverName, Database: "business"})
		_ = db.Close()
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), `"value"`) {
			t.Fatalf("failed statistics became values: %s", encoded)
		}
		for _, metric := range []c.SourceOverviewMetric{result.Metrics.Connections, result.Metrics.StorageBytes, result.Metrics.QPS} {
			if metric.Available || metric.Value != "" || metric.Reason == "" || metric.Basis == "" || metric.Scope == "" {
				t.Fatalf("unknown metric claimed available: %+v", metric)
			}
		}
	}
}

func TestSourceOverviewScopesPrecisionAndSampling(t *testing.T) {
	statusReads := 0
	db := sql.OpenDB(&catalogFixtureConnector{query: func(query string) (driver.Rows, error) {
		switch {
		case strings.HasPrefix(query, "SELECT DEFAULT_CHARACTER"):
			return catalogRows([]string{"charset"}, []driver.Value{"utf8mb4"}), nil
		case strings.HasPrefix(query, "SELECT COUNT"):
			return catalogRows([]string{"total", "known", "size"}, []driver.Value{int64(2), int64(1), "9007199254740993"}), nil
		default:
			statusReads++
			return catalogRows([]string{"name", "value"}, []driver.Value{"Threads_connected", "7"}, []driver.Value{"Questions", strconv.Itoa(100 + statusReads*10)}), nil
		}
	}})
	result := readSourceOverview(context.Background(), db, c.DataSource{Driver: "mysql", Database: "business"})
	_ = db.Close()
	if !result.Metrics.Connections.Available || result.Metrics.Connections.Value != "7" || result.Metrics.Connections.Scope != "instance" || result.Metrics.StorageBytes.Available || !result.Metrics.QPS.Available || result.Metrics.QPS.SampleWindowMS < 250 || !strings.Contains(result.Metrics.QPS.Basis, "管理查询") {
		t.Fatalf("wrong MySQL metric scope/sampling: %+v", result)
	}
	db = sql.OpenDB(&catalogFixtureConnector{query: func(query string) (driver.Rows, error) {
		value := "UTF8"
		if strings.Contains(query, "numbackends") {
			value = "3"
		}
		if strings.Contains(query, "pg_database_size") {
			value = "9007199254740993"
		}
		return catalogRows([]string{"value"}, []driver.Value{value}), nil
	}})
	result = readSourceOverview(context.Background(), db, c.DataSource{Driver: "postgresql", Database: "business"})
	_ = db.Close()
	if result.Charset != "UTF8" || result.Metrics.Connections.Value != "3" || result.Metrics.Connections.Scope != "database" || result.Metrics.StorageBytes.Value != "9007199254740993" || result.Metrics.QPS.Available || result.Metrics.QPS.Value != "" {
		t.Fatalf("wrong PG precision/scope or fabricated QPS: %+v", result)
	}
}

func TestSourceCatalogBatchCountsPreserveZeroAndDirectoryOnFailure(t *testing.T) {
	for _, failStats := range []bool{false, true} {
		queries := 0
		db := sql.OpenDB(&catalogFixtureConnector{query: func(query string) (driver.Rows, error) {
			queries++
			if !strings.Contains(query, "COUNT(*)") {
				return catalogRows([]string{"table_name"}, []driver.Value{"orders"}, []driver.Value{"empty_key"}, []driver.Value{"columns_unknown"}, []driver.Value{"af_private"}), nil
			}
			if failStats {
				return nil, errors.New("private directory denied")
			}
			if strings.Contains(query, "statistics") {
				return catalogRows([]string{"table_name", "count"}, []driver.Value{"orders", int64(2)}), nil
			}
			return catalogRows([]string{"table_name", "count"}, []driver.Value{"orders", int64(5)}, []driver.Value{"empty_key", int64(1)}), nil
		}})
		db.SetMaxOpenConns(1)
		items, err := listSourceTables(context.Background(), db, c.DataSource{Driver: "mysql", Database: "business"}, "")
		_ = db.Close()
		if err != nil || len(items) != 3 || queries != 3 {
			t.Fatalf("directory failed or used per-table queries: %d %+v %v", queries, items, err)
		}
		if failStats {
			if items[0].ColumnCount != nil || items[0].PrimaryKeyCount != nil {
				t.Fatal("failed counts claimed zero")
			}
		} else if items[0].ColumnCount == nil || *items[0].ColumnCount != 5 || items[0].PrimaryKeyCount == nil || *items[0].PrimaryKeyCount != 2 || items[1].PrimaryKeyCount == nil || *items[1].PrimaryKeyCount != 0 || items[2].ColumnCount != nil || items[2].PrimaryKeyCount == nil || *items[2].PrimaryKeyCount != 0 {
			t.Fatalf("composite/no-primary counts wrong: %+v", items)
		}
		encoded, _ := json.Marshal(items[2])
		if strings.Contains(string(encoded), `"columnCount"`) {
			t.Fatalf("missing column catalog was serialized as a count: %s", encoded)
		}
	}
}

func TestSourceColumnDefaultDistinguishesCatalogNullFromMissing(t *testing.T) {
	col := c.SourceColumn{}
	encoded, _ := json.Marshal(col)
	if strings.Contains(string(encoded), "defaultValue") {
		t.Fatal("unreturned default serialized as NULL")
	}
	setSourceColumnDisplay(&col, sql.NullString{}, sql.NullString{Valid: true, String: "中文说明"})
	encoded, _ = json.Marshal(col)
	if !strings.Contains(string(encoded), `"defaultValue":null`) || col.Comment != "中文说明" {
		t.Fatal(string(encoded))
	}
	setSourceColumnDisplay(&col, sql.NullString{Valid: true, String: "NULL"}, sql.NullString{})
	if string(col.DefaultValue) != `"NULL"` {
		t.Fatalf("literal default confused with SQL NULL: %s", col.DefaultValue)
	}
}

func TestSourceOverviewRequiresAdministratorAndLiveSession(t *testing.T) {
	app := NewHandler(Config{})
	for _, admin := range []bool{false, true} {
		w := httptest.NewRecorder()
		app.ServeHTTP(w, sourceRequest("GET", "/data/sources/unknown/overview", admin))
		want := 403
		if admin {
			want = 503
		}
		if w.Code != want {
			t.Fatalf("overview accepted unverified identity: %d %s", w.Code, w.Body)
		}
	}
	h := &handler{cfg: Config{SessionChecker: sourceSessionCheckerFunc(func(context.Context, c.SessionCheck) (c.Session, error) {
		return c.Session{User: c.User{ID: "actor", TenantID: "tenant", Enabled: true, Administrator: true}, ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
	})}}
	if err := h.sourceOverviewSession(sourceRequest("GET", "/", true), c.Principal{Subject: "actor", TenantID: "tenant"}); err == nil {
		t.Fatal("missing opaque session token accepted")
	}
}
