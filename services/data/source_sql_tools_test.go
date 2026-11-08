package data

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestSourceRequestLimitIsCompiledIntoExecution(t *testing.T) {
	for _, driver := range []string{"mysql", "postgresql"} {
		source := c.DataSource{Driver: driver, Database: "business"}
		for _, test := range []struct {
			sql         string
			limit, want int
		}{{"SELECT * FROM orders", 0, 100}, {"SELECT * FROM orders LIMIT 20", 3, 3}, {"SELECT * FROM orders LIMIT 2", 10, 2}, {"SELECT * FROM orders", 1, 1}} {
			plan, statement, err := parseSourceQuery(source, test.sql, test.limit)
			if err != nil || plan.limit != test.want || !strings.HasSuffix(statement, " LIMIT "+strconv.Itoa(test.want+1)) {
				t.Fatalf("request cap not compiled: %+v %s %v", plan, statement, err)
			}
		}
		for _, limit := range []int{-1, 101} {
			if _, _, err := parseSourceQuery(source, "SELECT * FROM orders", limit); err == nil {
				t.Fatal("invalid request cap accepted")
			}
		}
	}
}

func TestSourceSQLFormattingPreservesEveryTokenAndBoundValue(t *testing.T) {
	input := `select "中文字段", amount from "orders" where ("中文字段" like 'O''Brien 中文\_%' or "中文字段" = 'NULL') and amount >= 9007199254740993.000100 and id = -9223372036854775808 ORDER BY amount desc LIMIT 20;`
	formatted, err := formatSourceSQL(input)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := lexSourceSQL(input)
	after, _ := lexSourceSQL(formatted)
	if !reflect.DeepEqual(before, after) || !strings.Contains(formatted, "\nwhere") {
		t.Fatalf("formatter changed source tokens: %s", formatted)
	}
	for _, driver := range []string{"mysql", "postgresql"} {
		source := c.DataSource{Driver: driver, Database: "business"}
		left, lSQL, err := parseSourceQuery(source, input, 4)
		if err != nil {
			t.Fatal(err)
		}
		right, rSQL, err := parseSourceQuery(source, formatted, 4)
		if err != nil || lSQL != rSQL || !reflect.DeepEqual(left.args, right.args) {
			t.Fatal("formatter changed effective SQL or precise literals")
		}
	}
}

func TestCatalogTemplatesRemainClosedAndBounded(t *testing.T) {
	for _, driver := range []string{"mysql", "postgresql"} {
		source := c.DataSource{Driver: driver, Database: "business", TenantIsolation: "dedicated"}
		for _, template := range sourceCatalogTemplates(driver) {
			plan, statement, err := parseSourceQuery(source, template+";", 3)
			if err != nil || !plan.catalogTemplate || plan.limit != 3 || !strings.HasSuffix(statement, " LIMIT 4") {
				t.Fatalf("catalog template unavailable: %s %v", statement, err)
			}
			formatted, err := formatSourceSQL(template)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = parseSourceQuery(source, formatted, 5); err != nil {
				t.Fatal("formatted template no longer executable", err)
			}
			for _, tampered := range []string{
				strings.ReplaceAll(template, "'af_'", "'AF_'"), strings.ReplaceAll(template, "'af_'", "'zz_'"), strings.ReplaceAll(template, "'af_'", "'aF_'"),
				strings.ReplaceAll(template, "DESC", "ASC"), strings.ReplaceAll(template, "LIMIT 20", "LIMIT 101"), template + "; SELECT * FROM orders",
				strings.ReplaceAll(template, "table_name", "\"table_name\""),
			} {
				if _, _, err := parseSourceQuery(source, tampered, 0); err == nil {
					t.Fatalf("catalog escape accepted: %s", tampered)
				}
			}
			if driver == "postgresql" {
				for _, predicate := range []string{" AND pg_catalog.has_table_privilege(c.oid, 'SELECT')", " AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')"} {
					if _, _, err := parseSourceQuery(source, strings.Replace(template, predicate, "", 1), 0); err == nil {
						t.Fatal("catalog visibility condition removed")
					}
				}
			} else if _, _, err := parseSourceQuery(c.DataSource{Driver: driver, Database: "mysql"}, template, 0); err == nil {
				t.Fatal("template opened a system database")
			}
		}
		if _, _, err := parseSourceQuery(source, "SELECT * FROM information_schema.tables", 0); err == nil {
			t.Fatal("generic system query allowed")
		}
	}
}

func TestSourceSQLToolsRequireAdministratorAndReadOnlyScope(t *testing.T) {
	app := NewHandler(Config{})
	for _, operation := range []string{"analyze", "format"} {
		r := sourceRequest("POST", "/data/sources/source/sql/"+operation, false)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("nonadmin source tool admitted: %d", w.Code)
		}
	}
	for _, write := range []string{"CREATE TABLE x(id int)", "INSERT INTO orders VALUES (1)", "UPDATE orders SET id=1", "DELETE FROM orders"} {
		if _, _, err := parseSourceQuery(c.DataSource{Driver: "mysql", Database: "business"}, write, 0); err == nil {
			t.Fatal("write SQL accepted by static tool parser")
		}
	}
	encoded, _ := json.Marshal(c.SourceQuery{SQL: "SELECT * FROM orders", Limit: 3})
	if !strings.Contains(string(encoded), `"limit":3`) {
		t.Fatal(string(encoded))
	}
	if _, err := querySourceWithLimit(context.Background(), nil, c.DataSource{TenantIsolation: "column"}, "SELECT * FROM orders", 3); err == nil {
		t.Fatal("shared query opened")
	}
}
