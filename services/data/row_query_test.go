package data

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func testRowQuery() (*rowQueryPlan, c.QuerySpec) {
	d := testDefinition()
	link := c.Definition{ID: c.NewID(), Kind: c.KindLink, Link: &c.Link{StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	q := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Rows: &c.RowQuery{Nodes: []c.RowQueryNode{{ID: "n0", SchemaID: d.ID, Alias: "设备"}, {ID: "n1", SchemaID: d.ID, Alias: "关联设备"}}, Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: link.ID, Direction: "out"}}}, Select: []string{"n0." + testFieldCode, "n1." + testFieldCode}, Page: c.Page{Limit: 2}}
	plan := &rowQueryPlan{driver: "mysql", definitions: "definitions", authorization: "authorization", physical: "physical"}
	for _, node := range q.Rows.Nodes {
		plan.nodes = append(plan.nodes, rowQueryNode{node: node, current: c.CurrentDefinition{Definition: d, ETag: "etag"}, decision: c.Decision{Allow: true, ReadFields: []string{"*"}}, table: &mappedTable{managed: true, source: c.DataSource{Driver: "mysql"}, name: tableName(d.ID), keyColumn: "id", tenantColumn: "tenant_id"}})
	}
	plan.edges = []rowQueryEdge{{join: q.Rows.Joins[0], current: c.CurrentDefinition{Definition: link}}}
	return plan, q
}

func TestRowQueryRejectsInvalidTreesAndBudgets(t *testing.T) {
	_, valid := testRowQuery()
	if err := validateRowQueryShape(valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"via", "cycle", "missing-node", "unknown-from", "alias", "direction", "link-id", "limit", "projection", "sort-count"} {
		t.Run(change, func(t *testing.T) {
			_, q := testRowQuery()
			switch change {
			case "via":
				q.Via = &c.LinkTraversal{}
			case "cycle":
				q.Rows.Joins[0].To = "n0"
			case "missing-node":
				q.Rows.Nodes = q.Rows.Nodes[:1]
			case "unknown-from":
				q.Rows.Joins[0].From = "n9"
			case "alias":
				q.Rows.Nodes[1].Alias = q.Rows.Nodes[0].Alias
			case "direction":
				q.Rows.Joins[0].Direction = "both"
			case "link-id":
				q.Rows.Joins[0].LinkID = "name"
			case "limit":
				q.Page.Limit = 101
			case "projection":
				q.Select = nil
			case "sort-count":
				q.OrderBy = []c.Order{{}, {}}
			}
			if err := validateRowQueryShape(q); err == nil {
				t.Fatal("invalid row query accepted")
			}
		})
	}
}

func TestRowQueryFieldsRequirePerNodeReadPermission(t *testing.T) {
	for _, use := range []string{"select", "where", "order", "unknown-select", "unknown-where", "unknown-order"} {
		t.Run(use, func(t *testing.T) {
			plan, q := testRowQuery()
			plan.nodes[1].decision.ReadFields = []string{testFieldDept}
			q.Select = q.Select[:1]
			field := "n1." + testFieldCode
			if strings.HasPrefix(use, "unknown-") {
				field = "n9." + testFieldCode
			}
			switch strings.TrimPrefix(use, "unknown-") {
			case "select":
				q.Select = append(q.Select, field)
			case "where":
				q.Where = &c.Expression{Op: "eq", Field: field, Value: raw("P1")}
			case "order":
				q.OrderBy = []c.Order{{Field: field, Direction: "asc"}}
			}
			_, err := compileRowQuery(plan, c.Principal{TenantID: "tenant-a", Subject: "user"}, q, strings.Repeat("s", 32))
			problem, ok := err.(*apiError)
			want := 403
			if strings.HasPrefix(use, "unknown-") {
				want = 422
			}
			if !ok || problem.status != want {
				t.Fatalf("field validation status: %v", err)
			}
		})
	}
}

func TestRowQueryCompilesJoinBeforeLimitAndBindsAllPolicies(t *testing.T) {
	plan, q := testRowQuery()
	plan.nodes[0].decision.RowFilter = &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("A")}
	plan.nodes[1].decision.RowFilter = &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("B")}
	q.Where = &c.Expression{Op: "eq", Field: "n1." + testFieldCode, Value: raw("P' OR 1=1")}
	compiled, err := compileRowQuery(plan, c.Principal{TenantID: "tenant-a", Subject: "user"}, q, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled.SQL, "P' OR") || strings.Contains(compiled.SQL, "DISTINCT") || strings.Contains(compiled.SQL, "OFFSET") || strings.Count(compiled.SQL, " LIMIT ") != 1 || strings.Count(compiled.SQL, " JOIN ") != 2 {
		t.Fatalf("unsafe join semantics: %s", compiled.SQL)
	}
	if len(compiled.Args) != 7 || compiled.Args[0] != "P' OR 1=1" || compiled.Args[2] != "A" || compiled.Args[4] != "B" || compiled.Args[6] != 3 {
		t.Fatalf("policy/tenant binding mismatch: %#v", compiled.Args)
	}
	if compiled.Fields[0].Name != "设备.设备编码" || compiled.Fields[1].ID != "n1."+testFieldCode {
		t.Fatal("result fields lost node identities")
	}
}

func TestRowQueryCursorBindsAllContextsAndUsesTuple(t *testing.T) {
	plan, q := testRowQuery()
	p, secret := c.Principal{TenantID: "tenant-a", Subject: "user"}, strings.Repeat("s", 32)
	compiled, err := compileRowQuery(plan, p, q, secret)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(rowCursorValues{Keys: []string{c.NewID(), c.NewID()}})
	token, err := signCursor(secret, pageCursor{Fingerprint: compiled.Fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: q.From.SchemaID, Digest: plan.definitions, Authorization: plan.authorization, LastID: "row-digest", LastValue: value, Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	q.Page.Cursor = token
	continuation, err := compileRowQuery(plan, p, q, secret)
	if err != nil || !strings.Contains(continuation.SQL, "`n0`.`id`=? AND `n1`.`id`>?") {
		t.Fatalf("stable tuple cursor failed: %s %v", continuation.SQL, err)
	}
	for _, change := range []string{"tenant", "subject", "definition", "authorization", "physical", "query", "tamper"} {
		t.Run(change, func(t *testing.T) {
			copyPlan, copyQ, copyP := *plan, q, p
			switch change {
			case "tenant":
				copyP.TenantID = "other"
			case "subject":
				copyP.Subject = "other"
			case "definition":
				copyPlan.definitions = "changed"
			case "authorization":
				copyPlan.authorization = "changed"
			case "physical":
				copyPlan.physical = "changed"
			case "query":
				copyQ.Page.Limit++
			case "tamper":
				copyQ.Page.Cursor += "x"
			}
			if _, err := compileRowQuery(&copyPlan, copyP, copyQ, secret); err == nil {
				t.Fatal("cursor reused across changed context")
			}
		})
	}
}

func TestRowQueryRejectsSecondaryTableProjection(t *testing.T) {
	plan, q := testRowQuery()
	definition := plan.nodes[1].current.Definition
	schema := *definition.Schema
	schema.StorageBinding = &c.StorageBinding{DataSourceID: c.NewID(), Table: "primary"}
	schema.Properties = append([]c.Property(nil), schema.Properties...)
	schema.Properties[0].Mapping = &c.PropertyMapping{DataSourceID: schema.StorageBinding.DataSourceID, Table: "secondary", Column: "code"}
	definition.Schema = &schema
	plan.nodes[1].current.Definition = definition
	plan.nodes[1].table = &mappedTable{source: c.DataSource{Driver: "mysql"}, name: "primary"}
	_, err := compileRowQuery(plan, c.Principal{}, q, strings.Repeat("s", 32))
	if problem, ok := err.(*apiError); !ok || problem.code != "row_projection_unsupported" {
		t.Fatalf("secondary-table field silently accepted: %v", err)
	}
}

func TestRowQueryExplicitForwardStorageUsesSourceMappings(t *testing.T) {
	plan, q := testRowQuery()
	sourceID := c.NewID()
	for i := range plan.nodes {
		node := &plan.nodes[i]
		definition, schema := node.current.Definition, *node.current.Definition.Schema
		schema.StorageBinding = &c.StorageBinding{DataSourceID: sourceID, Namespace: "business", Table: "forward_object", IDColumn: "object_key", TenantColumn: "tenant"}
		schema.Properties = append([]c.Property(nil), schema.Properties...)
		for j := range schema.Properties {
			schema.Properties[j].Mapping = &c.PropertyMapping{DataSourceID: sourceID, Column: "business_column_" + schema.Properties[j].ID}
		}
		definition.Schema = &schema
		node.current.Definition = definition
		node.table = &mappedTable{managed: true, source: c.DataSource{ID: sourceID, Driver: "mysql", Database: "business"}, namespace: "business", name: "forward_object", keyColumn: "object_key", tenantColumn: "tenant", catalog: c.SourceTable{Columns: []c.SourceColumn{{Name: "object_key", DataType: "string"}}}}
		if node.platformStorage() {
			t.Fatal("forward DDL ownership selected platform storage")
		}
		if table := rowTableSQL(*node); table != "`business`.`forward_object`" {
			t.Fatalf("explicit source lost its physical namespace: %s", table)
		}
	}
	// Isolate physical node/field/key compilation from relationship planning.
	plan.edges = nil
	compiled, err := compileRowQuery(plan, c.Principal{TenantID: "tenant", Subject: "user"}, q, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled.SQL, columnName(testFieldCode)) || !strings.Contains(compiled.SQL, "`n0`.`business_column_"+testFieldCode+"`") || !strings.Contains(compiled.SQL, "BINARY `n0`.`object_key`") {
		t.Fatalf("explicit forward source used generated platform names: %s", compiled.SQL)
	}
	if _, err := rowKeyArguments(plan, []string{"business-key-A", "business-key-B"}); err != nil {
		t.Fatalf("explicit forward source incorrectly required platform UUID keys: %v", err)
	}
}
