package data

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func newAggregateFixture(t *testing.T) (*mappedIntegrationFixture, c.Definition) {
	return newAggregatePrecisionFixture(t, 30, 6)
}

func newAggregatePrecisionFixture(t *testing.T, precision, scale int) (*mappedIntegrationFixture, c.Definition) {
	t.Helper()
	f := newMappedIntegrationFixture(t, "mysql", sourceIntegrationDSN("mysql", "MYSQL_TEST_DSN"))
	d := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: "统计对象", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{
		{ID: c.NewID(), Name: "分组", DataType: "string", Nullable: true},
		{ID: c.NewID(), Name: "金额", DataType: "decimal", Precision: precision, Scale: scale, Nullable: true},
		{ID: c.NewID(), Name: "件数", DataType: "int64", Nullable: true},
		{ID: c.NewID(), Name: "范围", DataType: "string", Required: true},
	}}}
	publishRowDefinition(t, f, d)
	t.Cleanup(func() {
		_, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + quote(tableName(d.ID)))
		for _, name := range []string{"af_data_outbox", "af_data_write_receipts", "af_data_idempotency", "af_data_transaction_receipts"} {
			_, _ = f.engine.DB().Exec("DELETE FROM "+quote(name)+" WHERE tenant_id=?", f.tenant)
		}
	})
	return f, d
}

func TestMySQLAggregateMaximumSupportedDecimalAndWarningRejection(t *testing.T) {
	f, d := newAggregatePrecisionFixture(t, 38, 0)
	largest := strings.Repeat("9", 38)
	insertAggregateRows(t, f, d, [][]any{{"A", largest, "1", "allowed"}, {"A", largest, "2", "allowed"}, {"A", nil, "2", "allowed"}})
	f.engine.DB().SetMaxOpenConns(1)
	if _, err := f.engine.DB().Exec("SET SESSION div_precision_increment=0"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = f.engine.DB().Exec("SET SESSION div_precision_increment=DEFAULT") }()
	q := mysqlAggregateQuery(d)
	q.Aggregate.GroupBy = nil
	q.Page.Limit = 10
	result := f.query(q, 200)
	values := result.AggregateRows[0].Values
	if string(values["mSum"]) != string(raw("1"+strings.Repeat("9", 37)+"8")) || string(values["mAvg"]) != string(raw(largest+".0000")) || string(values["mIntAvg"]) != `"1.6667"` {
		t.Fatalf("maximum supported decimal or explicit AVG rounding was imprecise: %#v", values)
	}
	// An externally altered physical table must not turn SELECT saturation into
	// a valid exact result, even though its persisted Schema still caps inputs
	// at precision 38. The public compiler separately rejects precision 65.
	if _, err := f.engine.DB().Exec("ALTER TABLE " + quote(tableName(d.ID)) + " MODIFY COLUMN " + quote(columnName(d.Schema.Properties[1].ID)) + " DECIMAL(65,0) NULL"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.DB().Exec("DELETE FROM "+quote(tableName(d.ID))+" WHERE tenant_id=?", f.tenant); err != nil {
		t.Fatal(err)
	}
	insertAggregateRows(t, f, d, [][]any{{"A", strings.Repeat("9", 65), "1", "allowed"}})
	q.Aggregate.Measures = []c.AggregateMeasure{{ID: "mAvg", Op: "avg", Field: d.Schema.Properties[1].ID}}
	response := f.request("POST", "/data/query", q, false)
	if response.Code != 422 || !strings.Contains(response.Body.String(), "aggregate_numeric_range") || strings.Contains(response.Body.String(), "aggregateRows") {
		t.Fatalf("SELECT truncation was delivered: %d %s", response.Code, response.Body)
	}
	insertAggregateRows(t, f, d, [][]any{{"A", strings.Repeat("9", 65), "1", "allowed"}})
	q.Aggregate.Measures[0].Op = "sum"
	response = f.request("POST", "/data/query", q, false)
	if response.Code != 422 || !strings.Contains(response.Body.String(), "aggregate_numeric_range") {
		t.Fatalf("SUM overflow was delivered: %d %s", response.Code, response.Body)
	}
}

func insertAggregateRows(t *testing.T, f *mappedIntegrationFixture, d c.Definition, rows [][]any) {
	t.Helper()
	columns := []string{"`tenant_id`", "`id`", "`etag`"}
	for _, p := range d.Schema.Properties {
		columns = append(columns, quote(columnName(p.ID)))
	}
	for _, row := range rows {
		args := []any{f.tenant, c.NewID(), c.NewID()}
		args = append(args, row...)
		marks := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
		if _, err := f.engine.DB().ExecContext(f.ctx, "INSERT INTO "+quote(tableName(d.ID))+" ("+strings.Join(columns, ",")+") VALUES ("+marks+")", args...); err != nil {
			t.Fatal(err)
		}
	}
}

func mysqlAggregateQuery(d c.Definition) c.QuerySpec {
	return c.QuerySpec{From: c.Source{SchemaID: d.ID}, Aggregate: &c.AggregateQuery{GroupBy: []string{d.Schema.Properties[0].ID}, Measures: []c.AggregateMeasure{
		{ID: "mCount", Op: "count"}, {ID: "mNonNull", Op: "count", Field: d.Schema.Properties[1].ID}, {ID: "mDistinct", Op: "countDistinct", Field: d.Schema.Properties[1].ID},
		{ID: "mSum", Op: "sum", Field: d.Schema.Properties[1].ID}, {ID: "mAvg", Op: "avg", Field: d.Schema.Properties[1].ID}, {ID: "mMin", Op: "min", Field: d.Schema.Properties[1].ID}, {ID: "mMax", Op: "max", Field: d.Schema.Properties[1].ID},
		{ID: "mIntSum", Op: "sum", Field: d.Schema.Properties[2].ID}, {ID: "mIntAvg", Op: "avg", Field: d.Schema.Properties[2].ID},
	}}, Page: c.Page{Limit: 1}}
}

func TestMySQLAggregateExactValuesNullsAuthorizationAndPages(t *testing.T) {
	f, d := newAggregateFixture(t)
	insertAggregateRows(t, f, d, [][]any{{nil, nil, nil, "allowed"}, {"A", "9007199254740993.123456", "9007199254740993", "allowed"}, {"A", "2.000000", "2", "allowed"}, {"A", nil, nil, "allowed"}, {"B", "3.000000", "3", "allowed"}, {"B", "99.000000", "99", "hidden"}})
	f.decision.RowFilter = &c.Expression{Op: "eq", Field: d.Schema.Properties[3].ID, Value: raw("allowed")}
	q := mysqlAggregateQuery(d)
	first := f.query(q, 200)
	if !first.AggregateMode || first.RowMode || len(first.Data) != 0 || len(first.Rows) != 0 || !first.HasMore || len(first.AggregateRows) != 1 {
		t.Fatalf("wrong aggregate grain %#v", first)
	}
	null := first.AggregateRows[0].Values
	if string(null[d.Schema.Properties[0].ID]) != "null" || string(null["mCount"]) != `"1"` || string(null["mNonNull"]) != `"0"` || string(null["mSum"]) != "null" || string(null["mAvg"]) != "null" {
		t.Fatalf("NULL group semantics %#v", null)
	}
	q.Page.Cursor = first.NextCursor
	second := f.query(q, 200)
	values := second.AggregateRows[0].Values
	for key, want := range map[string]string{"mCount": `"3"`, "mNonNull": `"2"`, "mDistinct": `"2"`, "mSum": `"9007199254740995.123456"`, "mAvg": `"4503599627370497.5617280000"`, "mMin": `"2.000000"`, "mMax": `"9007199254740993.123456"`, "mIntSum": `"9007199254740995"`, "mIntAvg": `"4503599627370497.5000"`} {
		if string(values[key]) != want {
			t.Fatalf("%s got %s want %s", key, values[key], want)
		}
	}
	q.Page.Cursor = second.NextCursor
	last := f.query(q, 200)
	if last.HasMore || string(last.AggregateRows[0].Values["mSum"]) != `"3.000000"` {
		t.Fatal("row authorization applied after grouping or incomplete pagination")
	}
	q.Page.Cursor = ""
	q.OrderBy = []c.Order{{Field: "mSum", Direction: "desc"}}
	sorted := f.query(q, 200)
	if string(sorted.AggregateRows[0].Values[d.Schema.Properties[0].ID]) != `"A"` {
		t.Fatal("aggregate ordering did not use output measure")
	}
	q.Page.Cursor = sorted.NextCursor
	f.decision.State = "changed"
	f.query(q, 422)
	f.decision.ReadFields = []string{d.Schema.Properties[0].ID}
	q.Page.Cursor = ""
	f.query(q, 403)
	f.decision.ReadFields = []string{}
	q.Aggregate = &c.AggregateQuery{Measures: []c.AggregateMeasure{{ID: "mCount", Op: "count"}}}
	q.OrderBy = nil
	if counted := f.query(q, 200); string(counted.AggregateRows[0].Values["mCount"]) != `"5"` {
		t.Fatal("count(*) did not apply hidden authorized row filter")
	}
}

func TestMySQLAggregateEmptyInputAndSQLBoundContinuation(t *testing.T) {
	f, d := newAggregateFixture(t)
	q := mysqlAggregateQuery(d)
	q.Aggregate.GroupBy = nil
	q.Page.Limit = 10
	empty := f.query(q, 200)
	if len(empty.AggregateRows) != 1 || string(empty.AggregateRows[0].Values["mCount"]) != `"0"` || string(empty.AggregateRows[0].Values["mDistinct"]) != `"0"` || string(empty.AggregateRows[0].Values["mSum"]) != "null" {
		t.Fatal("empty global aggregate incorrect")
	}
	q.Aggregate.GroupBy = []string{d.Schema.Properties[0].ID}
	if grouped := f.query(q, 200); len(grouped.AggregateRows) != 0 {
		t.Fatal("empty GROUP BY invented a row")
	}
	insertAggregateRows(t, f, d, [][]any{{"A", "1.000001", "1", "allowed"}, {"A", "2.000002", "2", "allowed"}, {"B", "3.000003", "3", "allowed"}})
	request := c.OntologySQLRequest{Text: `SELECT "分组", COUNT(*) AS "数量", SUM("金额") AS "合计", AVG("件数") AS "均数" FROM "统计对象" GROUP BY "分组" ORDER BY "合计" DESC LIMIT 1`}
	// The fixture exposes current reads by ID, so analyze the same AST against
	// its authoritative definition, then execute its ID-bound query via HTTP.
	s, diagnostics := parseOntologySQL(request.Text)
	if len(diagnostics) > 0 {
		t.Fatal(diagnostics)
	}
	a := bindOntologySQL(s, d, nil)
	if !a.Ready || a.Query == nil {
		t.Fatalf("SQL aggregate analysis %#v", a)
	}
	jsonResult := f.query(*a.Query, 200)
	if !jsonResult.HasMore || len(jsonResult.AggregateRows) != 1 || string(jsonResult.AggregateRows[0].Values[d.Schema.Properties[0].ID]) != `"A"` || string(jsonResult.AggregateRows[0].Values["m2"]) != `"1.5000"` {
		t.Fatal("SQL aggregate did not use common executor")
	}
	query := *a.Query
	query.Page.Cursor = jsonResult.NextCursor
	next := f.query(query, 200)
	if next.HasMore || string(next.AggregateRows[0].Values[d.Schema.Properties[0].ID]) != `"B"` {
		t.Fatal("bound SQL continuation failed")
	}
	// Results have no fabricated identity or credentials anywhere in a row.
	wire, _ := json.Marshal(jsonResult.AggregateRows[0])
	if strings.Contains(string(wire), `"etag"`) || strings.Contains(string(wire), `"schemaId"`) || strings.Contains(string(wire), `"id"`) {
		t.Fatal("aggregate row impersonates instance")
	}
}

func TestMySQLAggregateGroupBudgetRejectsWholeResult(t *testing.T) {
	f, d := newAggregateFixture(t)
	rows := make([][]any, c.MaxAggregateGroups+1)
	for i := range rows {
		rows[i] = []any{fmt.Sprintf("G%04d", i), "1.000000", "1", "allowed"}
	}
	insertAggregateRows(t, f, d, rows)
	q := mysqlAggregateQuery(d)
	response := f.request("POST", "/data/query", q, false)
	if response.Code != 422 || !strings.Contains(response.Body.String(), "aggregate_group_budget") || strings.Contains(response.Body.String(), "aggregateRows") {
		t.Fatalf("over-budget aggregate was truncated: %d %s", response.Code, response.Body)
	}
	// Exactly 1000 complete groups remain queryable and paginate independently
	// of the source record page size.
	q.Where = &c.Expression{Op: "ne", Field: d.Schema.Properties[0].ID, Value: raw("G1000")}
	q.Page.Limit = 100
	first := f.query(q, 200)
	if len(first.AggregateRows) != 100 || !first.HasMore {
		t.Fatal("exact group budget rejected or source was pre-paged")
	}
}

func TestMySQLAggregateEnumAndAuthorizationChangeRejectDelivery(t *testing.T) {
	f, d := newAggregateFixture(t)
	insertAggregateRows(t, f, d, [][]any{{"A", "1.000000", "1", "allowed"}})
	enum := c.Definition{ID: c.NewID(), TenantID: f.tenant, Kind: c.KindEnum, Name: "分组枚举", Enum: &c.Enum{Values: []c.EnumValue{{Code: "A", Label: "分组A"}}}}
	f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
	d.Schema.Properties[0].EnumID = enum.ID
	// Enum binding is logical here; the physical gate identity follows the
	// current Schema as it would after a display-only publication.
	f.current[d.ID] = c.CurrentDefinition{Definition: d, ETag: digest(d), Status: "active"}
	if _, err := f.engine.DB().Exec("UPDATE af_data_gates SET digest=? WHERE tenant_id=? AND definition_id=?", digest(d), f.tenant, d.ID); err != nil {
		t.Fatal(err)
	}
	q := mysqlAggregateQuery(d)
	result := f.query(q, 200)
	if len(result.Fields[0].EnumValues) != 1 || !reflect.DeepEqual(result.Fields[0].EnumValues, enum.Enum.Values) {
		t.Fatal("group enum metadata lost")
	}
	var calls atomic.Int32
	f.authorize = func(request c.AuthorizationRequest) c.Decision {
		if request.Operation == "instance.query" && calls.Add(1) == 2 {
			f.mu.Lock()
			enum.Enum.Values[0].Label = "已改名"
			f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
			f.mu.Unlock()
		}
		return f.decision
	}
	f.query(q, 409)
}
