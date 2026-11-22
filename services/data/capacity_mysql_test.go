package data

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestMySQLQueryCapacityBaseline(t *testing.T) {
	if os.Getenv("ONTOLOGY_TEST_CAPACITY") != "1" {
		t.Skip("set ONTOLOGY_TEST_CAPACITY=1 and MYSQL_TEST_DSN for the isolated capacity baseline")
	}
	if os.Getenv("MYSQL_TEST_DSN") == "" {
		t.Fatal("MYSQL_TEST_DSN must point to an isolated database")
	}
	f, schema := newAggregateFixture(t)
	const rowCount, warmup, samples = 1000, 10, 100
	rows := make([][]any, rowCount)
	for i := range rows {
		rows[i] = []any{fmt.Sprintf("group-%02d", i%32), "12.500000", i, "allowed"}
	}
	insertAggregateRows(t, f, schema, rows)

	entity := c.QuerySpec{From: c.Source{SchemaID: schema.ID}, Select: []string{schema.Schema.Properties[0].ID}, Page: c.Page{Limit: 50}}
	page := f.query(entity, 200)
	if len(page.Data) != 50 || page.NextCursor == "" {
		t.Fatal("fixture must produce a full first page and continuation")
	}
	continuation := entity
	continuation.Page.Cursor = page.NextCursor
	aggregate := mysqlAggregateQuery(schema)
	aggregate.Page.Limit = 50
	link := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: schema.CategoryID, GroupID: schema.GroupID, Kind: c.KindLink, Name: "容量关联",
		Link: &c.Link{SourceSchemaID: schema.ID, TargetSchemaID: schema.ID, ForwardName: "后继", ReverseName: "前驱", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	publishRowDefinition(t, f, link)
	table := quote(linkTableName(link.ID))
	t.Cleanup(func() { _, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + table) })
	ids, err := f.engine.DB().QueryContext(f.ctx, "SELECT id FROM "+quote(tableName(schema.ID))+" WHERE tenant_id=? ORDER BY id", f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer ids.Close()
	var ordered []string
	for ids.Next() {
		var id string
		if err := ids.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ordered = append(ordered, id)
	}
	if ids.Err() != nil || len(ordered) != rowCount {
		t.Fatalf("unexpected capacity seed: %d rows, %v", len(ordered), ids.Err())
	}
	for i, id := range ordered {
		_, err := f.engine.DB().ExecContext(f.ctx, "INSERT INTO "+table+" (tenant_id,id,source_id,target_id,etag) VALUES (?,?,?,?,?),(?,?,?,?,?)",
			f.tenant, c.NewID(), id, ordered[(i+1)%rowCount], c.NewID(), f.tenant, c.NewID(), id, ordered[(i+2)%rowCount], c.NewID())
		if err != nil {
			t.Fatal(err)
		}
	}
	relation := entity
	relation.Relation = &c.RelationFilter{LinkID: link.ID, Direction: "out", Operator: "exists"}
	fixed := c.QuerySpec{From: entity.From, Page: entity.Page, Select: []string{"n0." + entity.Select[0]}, Rows: &c.RowQuery{
		Nodes: []c.RowQueryNode{{ID: "n0", SchemaID: schema.ID, Alias: "a"}, {ID: "n1", SchemaID: schema.ID, Alias: "b"}, {ID: "n2", SchemaID: schema.ID, Alias: "c"}},
		Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: link.ID, Direction: "out"}, {From: "n1", To: "n2", LinkID: link.ID, Direction: "out"}},
	}}
	fixed.Where = &c.Expression{Op: "eq", Field: fixed.Select[0], Value: raw("group-00")}
	if os.Getenv("ONTOLOGY_TEST_CAPACITY_WIDE_PATH") == "1" {
		wide := fixed
		wide.Where = nil
		started := time.Now()
		response := f.request("POST", "/data/query", wide, false)
		if response.Code == 504 {
			requireBudgetError(t, response, 504, "request_timeout")
		} else if response.Code != 200 {
			t.Fatalf("wide fixed path: %d %s", response.Code, response.Body)
		}
		t.Logf("unfiltered fixed path returned HTTP %d in %s", response.Code, time.Since(started))
	}
	grouped := fixed
	grouped.Rows = &c.RowQuery{Nodes: fixed.Rows.Nodes[:2], Joins: fixed.Rows.Joins[:1]}
	grouped.Select = nil
	grouped.Aggregate = &c.AggregateQuery{GroupBy: fixed.Select, Measures: []c.AggregateMeasure{{ID: "mEdges", Op: "count"}}}
	for _, workload := range []struct {
		name string
		spec c.QuerySpec
		want int
	}{{"entity", entity, 50}, {"continuation", continuation, 50}, {"aggregate", aggregate, 32},
		{"relation_filter", relation, 50}, {"relation_aggregate", grouped, 1}, {"fixed_path", fixed, 50}} {
		t.Log(workload.name)
		if workload.name == "fixed_path" {
			started := time.Now()
			probe := f.request("POST", "/data/query", workload.spec, false)
			if probe.Code == 504 {
				var outcome c.Error
				if err := json.Unmarshal(probe.Body.Bytes(), &outcome); err != nil || outcome.Code != "request_timeout" {
					t.Fatalf("unexpected fixed-path timeout: %s", probe.Body)
				}
				t.Logf("fixed path exceeded request budget after %s", time.Since(started))
				continue
			}
			if probe.Code != 200 {
				t.Fatalf("unexpected fixed-path response: %d %s", probe.Code, probe.Body)
			}
		}
		measureDataCapacity(t, warmup, samples, func() {
			result := f.query(workload.spec, 200)
			if len(result.Data)+len(result.Rows)+len(result.AggregateRows) != workload.want {
				t.Fatalf("unexpected result size for %s", workload.name)
			}
		})
	}
}

func TestMySQLMixedWriteCapacityBaseline(t *testing.T) {
	if os.Getenv("ONTOLOGY_TEST_CAPACITY") != "1" {
		t.Skip("set ONTOLOGY_TEST_CAPACITY=1 for the isolated capacity baseline")
	}
	if os.Getenv("MYSQL_TEST_DSN") == "" {
		t.Fatal("MYSQL_TEST_DSN must point to an isolated database")
	}
	f, link := newTransactionLinkSQLFixture(t, "1:N")
	sequence := 0
	measureDataCapacity(t, 10, 100, func() {
		sequence++
		request := mixedLinkRequest(f, link)
		request.Operations[0] = transactionCreate(f.current.Definition.ID, "master", fmt.Sprintf("MASTER-%03d", sequence))
		request.Operations[1] = transactionCreate(f.second.Definition.ID, "detail", fmt.Sprintf("DETAIL-%03d", sequence))
		result := f.transaction(request, false, 200)
		if !result.Committed || len(result.Results) != 2 || len(result.LinkResults) != 1 {
			t.Fatal("mixed transaction did not commit two objects and one edge")
		}
	})
}

func measureDataCapacity(t *testing.T, warmup, samples int, run func()) {
	t.Helper()
	durations := make([]time.Duration, samples)
	for i := -warmup; i < samples; i++ {
		started := time.Now()
		run()
		if i >= 0 {
			durations[i] = time.Since(started)
		}
	}
	slices.Sort(durations)
	percentile := func(p int) time.Duration { return durations[(samples*p+99)/100-1] }
	t.Logf("warmup=%d samples=%d concurrency=1 p50=%s p95=%s p99=%s",
		warmup, samples, percentile(50), percentile(95), percentile(99))
}
