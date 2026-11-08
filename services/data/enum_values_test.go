package data

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestEnumWritesPreserveUntouchedDeprecatedValuesAndRejectInvalidCodes(t *testing.T) {
	property := c.Property{ID: "status", Name: "状态", DataType: "string", EnumID: "enum", EnumETag: "current", EnumValues: []c.EnumValue{{Code: "A", Label: "可用"}, {Code: "B", Label: "历史", Deprecated: true}}}
	schema := &c.Schema{Properties: []c.Property{property, {ID: "note", Name: "说明", DataType: "string", Nullable: true}}}
	base := map[string]json.RawMessage{"status": raw("B"), "note": raw("before")}
	values, err := buildValues(schema, map[string]json.RawMessage{"note": raw("after")}, base, []string{"*"}, false)
	if err != nil || string(values["status"]) != `"B"` {
		t.Fatalf("untouched deprecated code lost: %v", err)
	}
	for _, code := range []string{"B", "可用", "unknown"} {
		if _, err := buildValues(schema, map[string]json.RawMessage{"status": raw(code)}, base, []string{"*"}, false); err == nil {
			t.Fatalf("new invalid/deprecated code %s accepted", code)
		}
	}
	if _, err := buildValues(schema, map[string]json.RawMessage{"status": raw("A")}, nil, []string{"*"}, true); err != nil {
		t.Fatal(err)
	}
	compiler := expressionCompiler{schema: schema, allowed: []string{"*"}}
	if _, _, err := compiler.compile(&c.Expression{Op: "eq", Field: "status", Value: raw("B")}, 0); err != nil {
		t.Fatal("deprecated code cannot be queried")
	}
}

func enumDataFixture(t *testing.T) (*mappedIntegrationFixture, c.Definition, c.Definition) {
	f := newMappedIntegrationFixture(t, "mysql", os.Getenv("MYSQL_TEST_DSN"))
	enum := c.Definition{ID: c.NewID(), TenantID: f.tenant, Kind: c.KindEnum, Name: "状态", Enum: &c.Enum{Values: []c.EnumValue{{Code: "A", Label: "可用"}, {Code: "B", Label: "历史", Deprecated: true}, {Code: "C", Label: "替换"}}}}
	f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
	definition := c.Definition{ID: c.NewID(), TenantID: f.tenant, Kind: c.KindSchema, Name: "枚举对象", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{ID: c.NewID(), Name: "状态", DataType: "string", Required: true, EnumID: enum.ID}, {ID: c.NewID(), Name: "说明", DataType: "string", Nullable: true}}}}
	operation := c.NewID()
	if response := f.prepare(definition, "", operation); response.Code != 200 {
		t.Fatalf("enum schema prepare: %d %s", response.Code, response.Body)
	}
	t.Cleanup(func() {
		_, _ = f.engine.DB().ExecContext(context.Background(), "DROP TABLE IF EXISTS "+quote(tableName(definition.ID)))
		for _, table := range []string{"af_data_idempotency", "af_data_outbox", "af_data_write_receipts", "af_data_transaction_receipts"} {
			_, _ = f.engine.DB().ExecContext(context.Background(), "DELETE FROM "+quote(table)+" WHERE tenant_id=?", f.tenant)
		}
	})
	f.activate(definition, operation)
	return f, definition, enum
}

func TestMySQLEnumWriteLifecycleBulkTransactionsAndCursor(t *testing.T) {
	f, definition, enum := enumDataFixture(t)
	statusID, noteID := definition.Schema.Properties[0].ID, definition.Schema.Properties[1].ID
	create := func(code, key string, want int) c.Instance {
		response := f.request("POST", "/data/schemas/"+definition.ID+"/instances", c.WriteRequest{Values: map[string]json.RawMessage{statusID: raw(code)}, IdempotencyKey: key}, false)
		if response.Code != want {
			t.Fatalf("enum create %s: %d %s", code, response.Code, response.Body)
		}
		var out c.Instance
		if want == 201 {
			_ = json.Unmarshal(response.Body.Bytes(), &out)
		}
		return out
	}
	one := create("A", "first", 201)
	create("A", "second", 201)
	create("B", "deprecated", 422)
	create("可用", "label", 422)
	create("unknown", "unknown", 422)
	query := c.QuerySpec{From: c.Source{SchemaID: definition.ID}, Select: []string{statusID}, Page: c.Page{Limit: 1}}
	first := f.query(query, 200)
	if !first.HasMore || first.NextCursor == "" || len(first.Fields) != 1 || len(first.Fields[0].EnumValues) != 3 {
		t.Fatal("enum metadata or continuation missing")
	}
	f.mu.Lock()
	enum.Enum.Values[0].Deprecated = true
	f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
	f.mu.Unlock()
	query.Page.Cursor = first.NextCursor
	f.query(query, 422)
	response := f.request("GET", "/data/schemas/"+definition.ID+"/instances/"+one.ID, nil, false)
	if response.Code != 200 {
		t.Fatal("deprecated historical value could not be read")
	}
	// PATCH through the same public handler, with the current row's strong ETag.
	patch := func(values map[string]json.RawMessage, etag string, want int) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PATCH", "/data/schemas/"+definition.ID+"/instances/"+one.ID, bytes.NewReader(mustMutationJSON(t, c.WriteRequest{Values: values})))
		r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{TenantID: f.tenant, Subject: "mapped-user"}))
		r.Header.Set("If-Match", `"`+etag+`"`)
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("enum patch: %d %s", w.Code, w.Body)
		}
		return w
	}
	updated := mutationDecode[c.Instance](t, patch(map[string]json.RawMessage{noteID: raw("保留历史状态")}, one.ETag, 200))
	if string(updated.Values[statusID]) != `"A"` {
		t.Fatal("untouched deprecated state changed")
	}
	patch(map[string]json.RawMessage{statusID: raw("A")}, updated.ETag, 422)
	patch(map[string]json.RawMessage{statusID: raw("C")}, updated.ETag, 200)
	create("A", "first", 409)
	create("A", "new-deprecated", 422)
	bulk := c.BulkWriteRequest{IdempotencyKey: "bulk", Items: []c.BulkWriteItem{{ClientKey: "valid", Values: map[string]json.RawMessage{statusID: raw("C")}}, {ClientKey: "invalid", Values: map[string]json.RawMessage{statusID: raw("A")}}}}
	if w := f.request("POST", "/data/schemas/"+definition.ID+"/instances/bulk-create", bulk, false); w.Code != 422 {
		t.Fatalf("invalid enum bulk accepted: %d %s", w.Code, w.Body)
	}
	query.Page = c.Page{Limit: 100}
	if got := f.query(query, 200); len(got.Data) != 2 {
		t.Fatal("invalid bulk left partial objects")
	}
	tx := c.WriteTransactionRequest{IdempotencyKey: "transaction", Operations: []c.TransactionOperation{{SchemaID: definition.ID, Operation: "create", ClientKey: "valid", Values: map[string]json.RawMessage{statusID: raw("C")}}, {SchemaID: definition.ID, Operation: "create", ClientKey: "invalid", Values: map[string]json.RawMessage{statusID: raw("A")}}}}
	w := f.request("POST", "/data/transactions", tx, false)
	var result c.WriteTransactionResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Committed || result.Status != "failed" {
		t.Fatalf("invalid enum transaction not atomically failed: %d %s", w.Code, w.Body)
	}
	if got := f.query(query, 200); len(got.Data) != 2 {
		t.Fatal("invalid transaction left partial objects")
	}
}

func TestMySQLEnumChangeDuringWriteRollsBack(t *testing.T) {
	f, definition, enum := enumDataFixture(t)
	var calls atomic.Int32
	f.authorize = func(c.AuthorizationRequest) c.Decision {
		if calls.Add(1) == 2 {
			f.mu.Lock()
			enum.Enum.Values[0].Deprecated = true
			f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
			f.mu.Unlock()
		}
		return f.decision
	}
	w := f.request("POST", "/data/schemas/"+definition.ID+"/instances", c.WriteRequest{IdempotencyKey: "changed", Values: map[string]json.RawMessage{definition.Schema.Properties[0].ID: raw("A")}}, false)
	if w.Code != 409 {
		t.Fatalf("enum change during write not rejected: %d %s", w.Code, w.Body)
	}
	f.authorize = nil
	if got := f.query(c.QuerySpec{From: c.Source{SchemaID: definition.ID}, Select: []string{definition.Schema.Properties[0].ID}, Page: c.Page{Limit: 100}}, 200); len(got.Data) != 0 {
		t.Fatal("enum change committed stale write")
	}
}

func TestMySQLEnumChangesDuringReadAndQueryRejectDelivery(t *testing.T) {
	for _, operation := range []string{"instance.read", "instance.query"} {
		t.Run(operation, func(t *testing.T) {
			f, definition, enum := enumDataFixture(t)
			created := f.request("POST", "/data/schemas/"+definition.ID+"/instances", c.WriteRequest{IdempotencyKey: c.NewID(), Values: map[string]json.RawMessage{definition.Schema.Properties[0].ID: raw("A")}}, false)
			if created.Code != 201 {
				t.Fatalf("create enum record: %d %s", created.Code, created.Body)
			}
			instance := mutationDecode[c.Instance](t, created)
			var calls atomic.Int32
			f.authorize = func(request c.AuthorizationRequest) c.Decision {
				if request.Operation == operation && calls.Add(1) == 2 {
					f.mu.Lock()
					enum.Enum.Values[0].Label = "读取时变化"
					f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
					f.mu.Unlock()
				}
				return f.decision
			}
			if operation == "instance.query" {
				f.query(c.QuerySpec{From: c.Source{SchemaID: definition.ID}, Select: []string{definition.Schema.Properties[0].ID}, Page: c.Page{Limit: 1}}, 409)
			} else if response := f.request("GET", "/data/schemas/"+definition.ID+"/instances/"+instance.ID, nil, false); response.Code != 409 {
				t.Fatalf("enum change during read exposed stale response: %d %s", response.Code, response.Body)
			}
		})
	}
}

func TestMappedEnumReadQueryAndContinuation(t *testing.T) {
	for _, backend := range []struct{ driver, env string }{{"mysql", "MYSQL_TEST_DSN"}, {"postgresql", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			f := newMappedIntegrationFixture(t, backend.driver, sourceIntegrationDSN(backend.driver, backend.env))
			name := "enum_mapped_" + strings.ReplaceAll(c.NewID(), "-", "")[:12]
			f.createTable(1, name, "tenant VARCHAR(128) NOT NULL,id BIGINT NOT NULL,state VARCHAR(255) NOT NULL,PRIMARY KEY(tenant,id)")
			for id, value := range []string{"A", "B"} {
				f.exec(1, "INSERT INTO "+f.qualified(1, name)+" (tenant,id,state) VALUES (?,?,?)", f.tenant, id+1, value)
			}
			enum := c.Definition{ID: c.NewID(), TenantID: f.tenant, Kind: c.KindEnum, Name: "状态", Enum: &c.Enum{Values: []c.EnumValue{{Code: "A", Label: "可用"}, {Code: "B", Label: "历史", Deprecated: true}}}}
			f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
			namespace, _ := sourceNamespace(f.sources[1], "")
			property := c.Property{ID: c.NewID(), Name: "状态", DataType: "string", EnumID: enum.ID, Required: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[1].ID, Column: "state"}}
			definition := c.Definition{ID: c.NewID(), TenantID: f.tenant, Kind: c.KindSchema, Name: "映射枚举", Schema: &c.Schema{Mode: "db_2_schema", StorageLayout: "single", StorageBinding: &c.StorageBinding{DataSourceID: f.sources[1].ID, Namespace: namespace, Table: name, IDColumn: "id", TenantColumn: "tenant"}, Properties: []c.Property{property}}}
			publishRowDefinition(t, f, definition)
			query := c.QuerySpec{From: c.Source{SchemaID: definition.ID}, Select: []string{property.ID}, Page: c.Page{Limit: 1}}
			first := f.query(query, 200)
			if !first.HasMore || len(first.Fields) != 1 || len(first.Fields[0].EnumValues) != 2 {
				t.Fatal("mapped enum metadata or pagination missing")
			}
			query.Where = &c.Expression{Op: "eq", Field: property.ID, Value: raw("B")}
			if result := f.query(query, 200); len(result.Data) != 1 || result.Data[0].ID != "2" {
				t.Fatal("deprecated mapped code cannot be queried")
			}
			query.Where.Op = "contains"
			f.query(query, 422)
			if response := f.request("GET", "/data/schemas/"+definition.ID+"/instances/2", nil, false); response.Code != 200 {
				t.Fatalf("mapped deprecated code cannot be read: %d %s", response.Code, response.Body)
			}
			enum.Enum.Values[0].Label = "已改标签"
			f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
			query.Where, query.Page.Cursor = nil, first.NextCursor
			f.query(query, 422)
			query.Page.Cursor = ""
			var calls atomic.Int32
			f.authorize = func(request c.AuthorizationRequest) c.Decision {
				if request.Operation == "instance.query" && calls.Add(1) == 2 {
					f.mu.Lock()
					enum.Enum.Values[0].Label = "执行时变化"
					f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
					f.mu.Unlock()
				}
				return f.decision
			}
			f.query(query, 409)
		})
	}
}

func TestMySQLRowEnumMetadataCursorAndLateChange(t *testing.T) {
	f, definition, enum := enumDataFixture(t)
	field := definition.Schema.Properties[0].ID
	var instances []c.Instance
	for i := 0; i < 3; i++ {
		response := f.request("POST", "/data/schemas/"+definition.ID+"/instances", c.WriteRequest{IdempotencyKey: c.NewID(), Values: map[string]json.RawMessage{field: raw("A")}}, false)
		if response.Code != 201 {
			t.Fatalf("create row object: %d %s", response.Code, response.Body)
		}
		instances = append(instances, mutationDecode[c.Instance](t, response))
	}
	link := c.Definition{ID: c.NewID(), TenantID: f.tenant, Kind: c.KindLink, Name: "枚举关联", Link: &c.Link{SourceSchemaID: definition.ID, TargetSchemaID: definition.ID, ForwardName: "后继", ReverseName: "前驱", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	publishRowDefinition(t, f, link)
	t.Cleanup(func() { _, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + quote(linkTableName(link.ID))) })
	for i := 0; i < 2; i++ {
		response := f.request("POST", "/data/links/"+link.ID+"/instances", c.LinkWriteRequest{SourceID: instances[i].ID, TargetID: instances[i+1].ID}, false)
		if response.Code != 201 {
			t.Fatalf("create enum link: %d %s", response.Code, response.Body)
		}
	}
	query := c.QuerySpec{From: c.Source{SchemaID: definition.ID}, Rows: &c.RowQuery{Nodes: []c.RowQueryNode{{ID: "n0", SchemaID: definition.ID, Alias: "来源"}, {ID: "n1", SchemaID: definition.ID, Alias: "目标"}}, Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: link.ID, Direction: "out"}}}, Select: []string{"n0." + field, "n1." + field}, Page: c.Page{Limit: 1}}
	first := f.query(query, 200)
	if !first.RowMode || !first.HasMore || len(first.Fields) != 2 || len(first.Fields[0].EnumValues) != 3 || len(first.Fields[1].EnumValues) != 3 {
		t.Fatal("row query lost node enum metadata or cursor")
	}
	enum.Enum.Values[0].Label = "游标后变化"
	f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
	query.Page.Cursor = first.NextCursor
	f.query(query, 422)
	query.Page.Cursor = ""
	var calls atomic.Int32
	f.authorize = func(request c.AuthorizationRequest) c.Decision {
		if request.Operation == "link.query" && calls.Add(1) == 2 {
			f.mu.Lock()
			enum.Enum.Values[0].Label = "最终关系授权时变化"
			f.current[enum.ID] = c.CurrentDefinition{Definition: enum, ETag: digest(enum), Status: "active"}
			f.mu.Unlock()
		}
		return f.decision
	}
	f.query(query, 409)
}
