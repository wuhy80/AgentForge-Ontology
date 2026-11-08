package data

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func aggregateTestQuery() c.QuerySpec {
	d := testDefinition()
	return c.QuerySpec{From: c.Source{SchemaID: d.ID}, Aggregate: &c.AggregateQuery{GroupBy: []string{"所属部门"}, Measures: []c.AggregateMeasure{{ID: "mCount", Op: "count", Name: "数量"}, {ID: "mSum", Op: "sum", Field: "采购价", Name: "合计"}, {ID: "mAvg", Op: "avg", Field: "采购价", Name: "均价"}}}, Page: c.Page{Limit: 2}}
}
func aggregateDecision() c.Decision {
	return c.Decision{Allow: true, ReadFields: []string{"*"}, State: "aggregate-test"}
}

func TestAggregateCompilerAuthorizesBeforeGroupingAndBindsAllSQLValues(t *testing.T) {
	d := testDefinition()
	p := c.Principal{TenantID: d.TenantID, Subject: "reader"}
	dec := aggregateDecision()
	dec.RowFilter = &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("授权部门")}
	q := aggregateTestQuery()
	q.Where = &c.Expression{Op: "startsWith", Field: testFieldCode, Value: raw("_%' OR 1=1 --")}
	q.OrderBy = []c.Order{{Field: "合计", Direction: "desc"}}
	out, err := compileAggregateQuery(d, "etag", p, dec, q, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.SQL, "GROUP BY") || !strings.Contains(out.SQL, "LIMIT 1001") || strings.Contains(out.SQL, "OR 1=1") || strings.Index(out.SQL, " WHERE ") > strings.Index(out.SQL, " GROUP BY ") {
		t.Fatal(out.SQL)
	}
	if !reflect.DeepEqual(out.Args, []any{d.TenantID, "!_!%' OR 1=1 --%", "授权部门"}) {
		t.Fatalf("unbound/incorrect args %#v", out.Args)
	}
	if out.Bound.From.SchemaID != d.ID || out.Bound.Aggregate.GroupBy[0] != testFieldDept || out.Bound.Aggregate.Measures[1].Field != testFieldPrice || out.Bound.OrderBy[0].Field != "mSum" {
		t.Fatalf("unstable binding %#v", out.Bound)
	}
	if out.Fields[1].DataType != "int64" || out.Fields[1].Nullable || out.Fields[2].Precision != 65 || out.Fields[2].Scale != 3 || out.Fields[3].Scale != 7 {
		t.Fatalf("incorrect field types %#v", out.Fields)
	}
	for _, f := range out.Fields {
		if !f.ReadOnly || f.Default != nil || f.Mapping != nil {
			t.Fatal("unsafe aggregate field metadata")
		}
	}
}

func TestAggregateRejectsUnsupportedOrUnauthorizedInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*c.QuerySpec, *c.Definition, *c.Decision)
	}{
		{"select", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Select = []string{testFieldDept} }},
		{"via", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Via = &c.LinkTraversal{} }},
		{"rows", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Rows = &c.RowQuery{} }},
		{"mapped", func(_ *c.QuerySpec, d *c.Definition, _ *c.Decision) {
			d.Schema.StorageBinding = &c.StorageBinding{DataSourceID: c.NewID()}
		}},
		{"no measures", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Aggregate.Measures = nil }},
		{"too many groups", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) {
			q.Aggregate.GroupBy = []string{testFieldCode, testFieldPrice, testFieldDept, testFieldDept}
		}},
		{"too many measures", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) {
			q.Aggregate.Measures = make([]c.AggregateMeasure, 21)
		}},
		{"duplicate group", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) {
			q.Aggregate.GroupBy = append(q.Aggregate.GroupBy, testFieldDept)
		}},
		{"unknown group", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Aggregate.GroupBy = []string{"unknown"} }},
		{"hidden group", func(_ *c.QuerySpec, _ *c.Definition, d *c.Decision) { d.ReadFields = []string{testFieldPrice} }},
		{"hidden measure", func(_ *c.QuerySpec, _ *c.Definition, d *c.Decision) { d.ReadFields = []string{testFieldDept} }},
		{"unsafe measure id", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Aggregate.Measures[0].ID = "m`unsafe" }},
		{"duplicate output name", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Aggregate.Measures[0].Name = "所属部门" }},
		{"sum text", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Aggregate.Measures[1].Field = testFieldCode }},
		{"unsupported source precision", func(_ *c.QuerySpec, d *c.Definition, _ *c.Decision) {
			d.Schema.Properties[1].Precision = 65
			d.Schema.Properties[1].Scale = 0
		}},
		{"unknown op", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Aggregate.Measures[0].Op = "median" }},
		{"missing countDistinct field", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) { q.Aggregate.Measures[0].Op = "countDistinct" }},
		{"non-output sort", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) {
			q.OrderBy = []c.Order{{Field: testFieldCode, Direction: "asc"}}
		}},
		{"unbound parameter", func(q *c.QuerySpec, _ *c.Definition, _ *c.Decision) {
			q.Where = &c.Expression{Op: "isNull", Field: testFieldCode, Parameter: "unbound"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, d, dec := aggregateTestQuery(), testDefinition(), aggregateDecision()
			tc.mutate(&q, &d, &dec)
			if _, err := compileAggregateQuery(d, "etag", c.Principal{TenantID: d.TenantID}, dec, q, strings.Repeat("s", 32)); err == nil {
				t.Fatal("invalid aggregate accepted")
			}
		})
	}
}

func TestAggregateSavedValidationUsesOnlyMetadataAndPreservesInput(t *testing.T) {
	d := testDefinition()
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metadata/definitions/"+d.ID || r.Header.Get("X-Tenant-ID") != d.TenantID {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, c.CurrentDefinition{Definition: d, ETag: "etag", Status: "active"})
	}))
	defer metadata.Close()
	h := &handler{cfg: Config{MetadataURL: metadata.URL, Authorizer: authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		return aggregateDecision(), nil
	})}, client: metadata.Client()}
	for _, q := range []c.QuerySpec{aggregateTestQuery(), baseQuery()} {
		q.Where = &c.Expression{Op: "eq", Field: "所属部门", Value: raw("allowed")}
		before, _ := json.Marshal(q)
		bound, err := h.validateBoundQuery(context.Background(), c.Principal{TenantID: d.TenantID, Subject: "reader"}, q)
		if err != nil {
			t.Fatal(err)
		}
		if bound.From.SchemaID != d.ID || bound.Where.Field != testFieldDept || h.cfg.Engine != nil {
			t.Fatal("metadata-only validation did not bind IDs")
		}
		after, _ := json.Marshal(q)
		if string(before) != string(after) {
			t.Fatal("read-only validation mutated caller's query")
		}
	}
}

func TestAggregateCursorBindsQueryPermissionDefinitionAndEnum(t *testing.T) {
	d := testDefinition()
	q := aggregateTestQuery()
	dec := aggregateDecision()
	p := c.Principal{TenantID: d.TenantID, Subject: "reader"}
	secret := strings.Repeat("s", 32)
	out, err := compileAggregateQuery(d, "etag", p, dec, q, secret)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signCursor(secret, pageCursor{Fingerprint: out.Fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: d.ID, Digest: "etag", Authorization: digest(dec), LastID: "2", Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	q.Page.Cursor = token
	res, err := compileAggregateQuery(d, "etag", p, dec, q, secret)
	if err != nil || res.Offset != 2 {
		t.Fatal(res, err)
	}
	for _, change := range []string{"query", "permission", "definition", "enum", "subject"} {
		t.Run(change, func(t *testing.T) {
			next := q
			def := d
			decision := dec
			principal := p
			etag := "etag"
			switch change {
			case "query":
				next.Page.Limit++
			case "permission":
				decision.State = "changed"
			case "definition":
				etag = "changed"
			case "enum":
				schema := *d.Schema
				schema.Properties = append([]c.Property{}, schema.Properties...)
				schema.Properties[2].EnumID = "enum"
				schema.Properties[2].EnumETag = "enum-etag"
				def.Schema = &schema
			case "subject":
				principal.Subject = "other"
			}
			if _, err := compileAggregateQuery(def, etag, principal, decision, next, secret); err == nil {
				t.Fatal("stale cursor accepted")
			}
		})
	}
}

func TestOntologySQLAggregateLowersToSharedQueryAndRejectsInvalidGrain(t *testing.T) {
	a := compileOntologySQLTest(t, `SELECT "编码", COUNT(*) AS "数量", COUNT("计数") AS "非空", COUNT(DISTINCT "计数") AS "去重", SUM("金额") AS "合计", AVG("金额") AS "均值", MIN("更新时间") AS "最早", MAX("计数") AS "最大" FROM "笔记" WHERE "计数" > :下限 GROUP BY "编码" ORDER BY "合计" DESC LIMIT 2`, map[string]json.RawMessage{"下限": raw("9007199254740993")})
	if !a.Ready || a.Query == nil || a.Query.Aggregate == nil || len(a.Query.Select) != 0 || len(a.Query.Aggregate.Measures) != 7 || a.Query.Aggregate.GroupBy[0] != "code" || a.Query.Aggregate.Measures[2].Op != "countDistinct" || a.Query.OrderBy[0].Field != "m3" {
		t.Fatalf("aggregate binding %#v", a)
	}
	if string(a.Query.Where.Value) != `"9007199254740993"` || len(a.Parameters) != 1 || len(a.Fields) != 8 {
		t.Fatal("parameter or aggregate fields lost")
	}
	for _, sql := range []string{`SELECT "编码" FROM "笔记" GROUP BY "编码"`, `SELECT "编码", COUNT(*) FROM "笔记"`, `SELECT "编码", COUNT(*) FROM "笔记" GROUP BY "计数"`, `SELECT SUM("编码") FROM "笔记"`, `SELECT COUNT(*) AS "数量", SUM("金额") AS "数量" FROM "笔记"`, `SELECT COUNT(*) FROM "笔记" ORDER BY "计数"`} {
		a = compileOntologySQLTest(t, sql, nil)
		if a.Ready || len(a.Diagnostics) == 0 {
			t.Fatalf("invalid aggregate accepted %s %#v", sql, a)
		}
	}
	for _, sql := range []string{`SELECT SUM(*) FROM "笔记"`, `SELECT SUM(DISTINCT "金额") FROM "笔记"`, `SELECT COUNT(*), "编码" FROM "笔记" GROUP BY "编码"`, `SELECT COUNT(*) FROM "笔记" HAVING COUNT(*) > 1`} {
		if s, d := parseOntologySQL(sql); s != nil || len(d) == 0 {
			t.Fatalf("unsupported syntax accepted %s", sql)
		}
	}
}
