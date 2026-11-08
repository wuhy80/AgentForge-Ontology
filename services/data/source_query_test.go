package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestSourceSelectCompilesClosedReadOnlyGrammar(t *testing.T) {
	for _, driver := range []string{"mysql", "postgresql"} {
		source := c.DataSource{Driver: driver, Database: "business", TenantIsolation: "dedicated"}
		p, statement, err := parseSourceSelect(source, `SELECT "name", "amount" FROM "orders" WHERE ("name" LIKE 'O''Brien%' OR "name" IS NULL) AND "amount" >= 9007199254740993.0001 ORDER BY "amount" DESC, "name" ASC LIMIT 2;`)
		if err != nil || p.limit != 2 || p.table != "orders" || len(p.args) != 2 || p.args[0] != "O'Brien%" || p.args[1] != "9007199254740993.0001" {
			t.Fatalf("invalid compiled query: %#v %v", p, err)
		}
		if strings.Contains(statement, "O'Brien") || strings.Contains(statement, "9007199254740993") || !strings.HasSuffix(statement, " LIMIT 3") {
			t.Fatal("values were interpolated or result lookahead was lost")
		}
		if driver == "postgresql" && (!strings.Contains(statement, `"public"."orders"`) || !strings.Contains(statement, "$2")) {
			t.Fatal(statement)
		}
		if driver == "mysql" && (!strings.Contains(statement, "`business`.`orders`") || !strings.Contains(statement, "MAX_EXECUTION_TIME")) {
			t.Fatal(statement)
		}
		if _, _, err := parseSourceSelect(source, "SELECT * FROM orders"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSourceSelectRejectsWritesEscapeRoutesAndUnsupportedSQL(t *testing.T) {
	for _, driver := range []string{"mysql", "postgresql"} {
		source := c.DataSource{Driver: driver, Database: "business"}
		for _, text := range []string{
			"", "DELETE FROM orders", "UPDATE orders SET name='x'", "DROP TABLE orders", "SELECT * FROM orders; DELETE FROM orders", "SELECT * FROM orders;;",
			"SELECT SLEEP(1) FROM orders", "SELECT LOAD_FILE('/etc/passwd') FROM orders", "SELECT pg_sleep(1) FROM orders", "SELECT * INTO OUTFILE '/tmp/a' FROM orders",
			"SELECT * FROM orders FOR UPDATE", "SELECT * FROM orders LOCK IN SHARE MODE", "SELECT * FROM orders JOIN users ON 1=1", "SELECT * FROM orders UNION SELECT * FROM users",
			"SELECT * FROM (SELECT * FROM orders) x", "WITH x AS (DELETE FROM orders RETURNING *) SELECT * FROM x", "EXPLAIN SELECT * FROM orders",
			"SELECT * FROM af_data_sources", "SELECT * FROM AF_data_sources", "SELECT * FROM information_schema.tables", "SELECT * FROM pg_catalog.pg_authid",
			"SELECT * FROM orders -- comment", "SELECT /* comment */ * FROM orders", "SELECT * FROM orders WHERE name='a' OR 1=1", "SELECT * FROM orders WHERE name=other",
			"SELECT * FROM orders WHERE name IN (SELECT name FROM users)", "SELECT * FROM orders LIMIT 0", "SELECT * FROM orders LIMIT 101", "SELECT * FROM orders LIMIT 1 OFFSET 2",
			"SELECT * FROM orders WHERE name=:name", "SELECT @var FROM orders", "SELECT * FROM orders WHERE name=$$value$$", "SELECT * FROM orders WHERE (name='a'",
		} {
			if _, statement, err := parseSourceSelect(source, text); err == nil {
				t.Errorf("accepted %s SQL %q as %q", driver, text, statement)
			}
		}
	}
	if _, _, err := parseSourceSelect(c.DataSource{Driver: "mysql", Database: "business"}, "SELECT * FROM other.orders"); err == nil {
		t.Fatal("cross-database query accepted")
	}
}

func TestSourceQueryRequiresCurrentAdministrator(t *testing.T) {
	app := NewHandler(Config{})
	for _, admin := range []bool{false, true} {
		r := httptest.NewRequest("POST", "/data/sources/source/query", strings.NewReader(`{"sql":"SELECT * FROM orders"}`))
		if admin {
			p := c.Principal{TenantID: "tenant", Subject: "user"}
			// A principal without an authoritative session user is insufficient.
			r = r.WithContext(platform.WithPrincipal(r.Context(), p))
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != 401 && w.Code != 403 {
			t.Fatalf("accepted non-admin request: %d", w.Code)
		}
	}
	if _, err := querySource(context.Background(), nil, c.DataSource{TenantIsolation: "column"}, "SELECT * FROM orders"); err == nil {
		t.Fatal("shared source allowed SQL without tenant filtering")
	}
}

func TestSourceQueryRealDrivers(t *testing.T) {
	for _, backend := range []struct{ driver, sqlDriver, env string }{{"mysql", "mysql", "MYSQL_TEST_DSN"}, {"postgresql", "pgx", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			dsn := sourceIntegrationDSN(backend.driver, backend.env)
			if dsn == "" {
				t.Skip(backend.env + " not configured")
			}
			db, err := sql.Open(backend.sqlDriver, dsn)
			if err != nil {
				t.Fatal("cannot open source query fixture")
			}
			defer db.Close()
			ctx := context.Background()
			source := c.DataSource{Driver: backend.driver, TenantIsolation: "dedicated"}
			if source.Driver == "mysql" {
				if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&source.Database); err != nil {
					t.Fatal(err)
				}
			}
			ns, err := sourceNamespace(source, "")
			if err != nil {
				t.Fatal(err)
			}
			name := "source_query_" + strings.ReplaceAll(c.NewID(), "-", "")[:12]
			quotedNS, _ := sourceQuote(source.Driver, ns)
			quotedName, _ := sourceQuote(source.Driver, name)
			qualified := quotedNS + "." + quotedName
			if _, err := db.ExecContext(ctx, "CREATE TABLE "+qualified+" (id BIGINT PRIMARY KEY, name VARCHAR(100), amount DECIMAL(24,4), stamp TIMESTAMP(6))"); err != nil {
				t.Fatal(err)
			}
			defer db.ExecContext(ctx, "DROP TABLE "+qualified)
			if _, err := db.ExecContext(ctx, "INSERT INTO "+qualified+" VALUES (1,'中文',9007199254740993.0001,'2026-11-07 12:34:56.123456'),(2,'',NULL,NULL),(3,NULL,1.0000,NULL)"); err != nil {
				t.Fatal(err)
			}
			result, err := querySource(ctx, db, source, "SELECT * FROM "+qualified+" ORDER BY id LIMIT 2;")
			if err != nil {
				t.Fatal(err)
			}
			if !result.HasMore || len(result.Rows) != 2 || !reflect.DeepEqual(result.Columns, []string{"id", "name", "amount", "stamp"}) || *result.Rows[0][2] != "9007199254740993.0001" || *result.Rows[1][1] != "" || result.Rows[1][2] != nil || !strings.Contains(*result.Rows[0][3], ".123456") {
				t.Fatalf("precision/null/limit mismatch: %#v", result)
			}
			filtered, err := querySource(ctx, db, source, "SELECT name FROM "+qualified+" WHERE id >= 2 AND name IS NOT NULL ORDER BY id")
			if err != nil || len(filtered.Rows) != 1 || *filtered.Rows[0][0] != "" {
				t.Fatalf("filter/empty string lost: %#v %v", filtered, err)
			}
			if _, err := querySource(ctx, db, source, "SELECT missing FROM "+qualified); err == nil {
				t.Fatal("missing field accepted")
			}
			if _, err := querySource(ctx, db, source, "DELETE FROM "+qualified); err == nil {
				t.Fatal("write accepted")
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified).Scan(&count); err != nil || count != 3 {
				t.Fatal("read-only query altered source rows")
			}
			body, err := json.Marshal(result)
			if err != nil || strings.Contains(string(body), `9007199254740992`) {
				t.Fatal("precision lost in response")
			}
		})
	}
}
