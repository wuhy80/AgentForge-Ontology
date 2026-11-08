package data

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestMappedLinksExecuteDatabaseJoinsAndPermissions(t *testing.T) {
	for _, backend := range []struct{ driver, env string }{{"mysql", "MYSQL_TEST_DSN"}, {"postgresql", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			f := newMappedIntegrationFixture(t, backend.driver, sourceIntegrationDSN(backend.driver, backend.env))
			index := 1
			suffix := strings.ReplaceAll(c.NewID(), "-", "")[:12]
			sourceName, targetName, edgeName := "link_source_"+suffix, "link_target_"+suffix, "link_edge_"+suffix
			columns := "tenant VARCHAR(128) NOT NULL,id BIGINT NOT NULL,name VARCHAR(255) NOT NULL,owner VARCHAR(255) NOT NULL,fk BIGINT NULL,PRIMARY KEY(tenant,id)"
			f.createTable(index, sourceName, columns)
			f.createTable(index, targetName, columns)
			f.createTable(index, edgeName, "tenant VARCHAR(128) NOT NULL,source_id BIGINT NOT NULL,target_id BIGINT NOT NULL,PRIMARY KEY(tenant,source_id,target_id)")
			for _, row := range []struct {
				id    int
				owner string
				fk    int
			}{{1, "O1", 10}, {2, "O2", 10}, {3, "O1", 11}} {
				f.exec(index, "INSERT INTO "+f.qualified(index, sourceName)+" (tenant,id,name,owner,fk) VALUES (?,?,?,?,?)", f.tenant, row.id, "源"+string(rune('A'+row.id)), row.owner, row.fk)
			}
			for _, row := range []struct {
				id    int
				owner string
			}{{10, "O1"}, {11, "O1"}, {12, "O2"}, {13, "O1"}} {
				f.exec(index, "INSERT INTO "+f.qualified(index, targetName)+" (tenant,id,name,owner) VALUES (?,?,?,?)", f.tenant, row.id, "目标", row.owner)
			}
			for _, edge := range []struct{ source, target int }{{1, 10}, {1, 11}, {1, 12}, {2, 10}} {
				f.exec(index, "INSERT INTO "+f.qualified(index, edgeName)+" (tenant,source_id,target_id) VALUES (?,?,?)", f.tenant, edge.source, edge.target)
			}
			// A shared physical table contains another tenant's edge to an ID also
			// present in this tenant. It must never become an alias of our relation.
			f.exec(index, "INSERT INTO "+f.qualified(index, edgeName)+" (tenant,source_id,target_id) VALUES (?,?,?)", strings.ToUpper(f.tenant), 1, 13)
			makeSchema := func(name string) c.Definition {
				namespace, _ := sourceNamespace(f.sources[index], "")
				return c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: name, Schema: &c.Schema{Mode: "db_2_schema", StorageLayout: "single", StorageBinding: &c.StorageBinding{DataSourceID: f.sources[index].ID, Namespace: namespace, Table: name, IDColumn: "id", TenantColumn: "tenant"}, Properties: []c.Property{{ID: c.NewID(), Name: "名称", DataType: "string", Required: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[index].ID, Column: "name"}}, {ID: c.NewID(), Name: "归属", DataType: "string", Required: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[index].ID, Column: "owner"}}}}}
			}
			source, target := makeSchema(sourceName), makeSchema(targetName)
			publish := func(d c.Definition) {
				t.Helper()
				operation := c.NewID()
				response := f.prepare(d, "", operation)
				if response.Code != 200 {
					t.Fatalf("prepare %s failed: %d %s", d.Kind, response.Code, response.Body)
				}
				f.activate(d, operation)
			}
			publish(source)
			publish(target)
			link := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: source.CategoryID, GroupID: source.GroupID, Kind: c.KindLink, Name: "来源关联目标", Link: &c.Link{SourceSchemaID: source.ID, TargetSchemaID: target.ID, ForwardName: "关联目标", ReverseName: "关联来源", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "junction", DataSourceID: f.sources[index].ID, Table: edgeName, SourceColumn: "source_id", TargetColumn: "target_id", TenantColumn: "tenant"}}}
			preview := f.request("POST", "/internal/publications/preview", c.PrepareRequest{Definition: link}, true)
			if preview.Code != 200 || strings.Contains(preview.Body.String(), "CREATE TABLE") {
				t.Fatalf("external relation preview modified source: %d %s", preview.Code, preview.Body)
			}
			publish(link)
			f.authorize = func(request c.AuthorizationRequest) c.Decision {
				decision := f.decision
				if request.Kind == c.KindLink {
					return decision
				}
				if request.ResourceID == source.ID {
					decision.RowFilter = &c.Expression{Op: "eq", Field: source.Schema.Properties[1].ID, Value: raw("O1")}
					decision.ReadFields = []string{source.Schema.Properties[0].ID}
				}
				if request.ResourceID == target.ID {
					decision.RowFilter = &c.Expression{Op: "eq", Field: target.Schema.Properties[1].ID, Value: raw("O1")}
					decision.ReadFields = []string{target.Schema.Properties[0].ID}
				}
				return decision
			}
			query := c.QuerySpec{From: c.Source{SchemaID: target.ID}, Via: &c.LinkTraversal{LinkID: link.ID, Direction: "out", InstanceID: "1"}, Select: []string{target.Schema.Properties[0].ID}, Page: c.Page{Limit: 1}}
			first := f.query(query, 200)
			if len(first.Data) != 1 || first.Data[0].ID != "10" || !first.HasMore || first.NextCursor == "" {
				t.Fatalf("first external relation page incorrect: %+v", first)
			}
			query.Page.Cursor = first.NextCursor
			second := f.query(query, 200)
			if len(second.Data) != 1 || second.Data[0].ID != "11" || second.HasMore {
				t.Fatalf("relation permissions not pushed before limit: %+v", second)
			}
			query.Via.InstanceID = "2"
			query.Page.Cursor = ""
			f.query(query, 404)
			query.Via.InstanceID = "999"
			f.query(query, 404)
			query.Via.InstanceID = "01"
			f.query(query, 422)
			query.Via.InstanceID = "1"
			query.Where = &c.Expression{Op: "eq", Field: target.Schema.Properties[1].ID, Value: raw("O1")}
			f.query(query, 403)
			query.Where = nil
			query.Select = append(query.Select, target.Schema.Properties[1].ID)
			f.query(query, 403)
			query.Select = query.Select[:1]
			query.From.SchemaID = source.ID
			query.Select = []string{source.Schema.Properties[0].ID}
			query.Via.Direction = "in"
			query.Via.InstanceID = "10"
			reverse := f.query(query, 200)
			if len(reverse.Data) != 1 || reverse.Data[0].ID != "1" || reverse.HasMore {
				t.Fatalf("reverse relation leaked hidden source: %+v", reverse)
			}
			query.Via.Direction = "both"
			f.query(query, 422)
			query.Via.Direction = "out"
			f.query(query, 422)
			for _, mode := range []string{"source", "target"} {
				fk := link
				fk.ID = c.NewID()
				value := *link.Link
				fk.Link = &value
				fk.Link.Cardinality = "N:1"
				if mode == "target" {
					fk.Link.Cardinality = "1:N"
				}
				fk.Link.StorageBinding = &c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: f.sources[index].ID, ForeignKeySide: mode, ForeignKeyColumn: "fk"}
				publish(fk)
				if mode == "source" {
					query.From.SchemaID = target.ID
					query.Select = []string{target.Schema.Properties[0].ID}
					query.Via = &c.LinkTraversal{LinkID: fk.ID, Direction: "out", InstanceID: "1"}
					got := f.query(query, 200)
					if len(got.Data) != 1 || got.Data[0].ID != "10" {
						t.Fatal("source foreign key did not join actual database")
					}
					query.From.SchemaID = source.ID
					query.Select = []string{source.Schema.Properties[0].ID}
					query.Via.Direction = "in"
					query.Via.InstanceID = "10"
					got = f.query(query, 200)
					if len(got.Data) != 1 || got.Data[0].ID != "1" {
						t.Fatal("reverse foreign key did not apply row permission")
					}
				}
				if mode == "target" {
					f.exec(index, "UPDATE "+f.qualified(index, targetName)+" SET fk=? WHERE tenant=? AND id=?", 1, f.tenant, 10)
					query.From.SchemaID = target.ID
					query.Select = []string{target.Schema.Properties[0].ID}
					query.Via = &c.LinkTraversal{LinkID: fk.ID, Direction: "out", InstanceID: "1"}
					got := f.query(query, 200)
					if len(got.Data) != 1 || got.Data[0].ID != "10" {
						t.Fatal("target foreign key did not join")
					}
				}
				if response := f.request("POST", "/data/links/"+fk.ID+"/instances", c.LinkWriteRequest{SourceID: c.NewID(), TargetID: c.NewID()}, false); response.Code != 422 {
					t.Fatal("external relation write accepted")
				}
			}
			// A cardinality stronger than the catalog's unique constraints is rejected.
			invalid := link
			invalid.ID = c.NewID()
			value := *link.Link
			invalid.Link = &value
			invalid.Link.Cardinality = "1:1"
			if response := f.prepare(invalid, "", c.NewID()); response.Code != 422 {
				t.Fatalf("unenforced relation cardinality accepted: %d %s", response.Code, response.Body)
			}
			// A retry after durable admission closure must remain resumable when
			// a source loses a required constraint. Returning 422 here would let
			// Metadata reject the operation while leaving its Data gate closed.
			recovery := link
			recovery.ID = c.NewID()
			operation := c.NewID()
			if response := f.prepare(recovery, "", operation); response.Code != 200 {
				t.Fatalf("prepare recovery fixture: %d %s", response.Code, response.Body)
			}
			if backend.driver == "mysql" {
				f.exec(index, "ALTER TABLE "+f.qualified(index, edgeName)+" DROP PRIMARY KEY")
			} else {
				constraint, _ := sourceQuote("postgresql", edgeName+"_pkey")
				f.exec(index, "ALTER TABLE "+f.qualified(index, edgeName)+" DROP CONSTRAINT "+constraint)
			}
			if response := f.prepare(recovery, "", operation); response.Code != 500 {
				t.Fatalf("closed external Link became rejectable: %d %s", response.Code, response.Body)
			}
			var status, actualOperation string
			if err := f.engine.DB().QueryRowContext(f.ctx, "SELECT status,operation_id FROM af_data_gates WHERE tenant_id=? AND definition_id=?", f.tenant, recovery.ID).Scan(&status, &actualOperation); err != nil || status != "prepared" || actualOperation != operation {
				t.Fatal("failed recovery changed durable gate")
			}
			f.exec(index, "ALTER TABLE "+f.qualified(index, edgeName)+" ADD PRIMARY KEY (tenant,source_id,target_id)")
			if response := f.prepare(recovery, "", operation); response.Code != 200 {
				t.Fatalf("same-operation recovery failed: %d %s", response.Code, response.Body)
			}
			f.activate(recovery, operation)
			// Changing another endpoint's schema invalidates an existing cursor even
			// when the return schema and its read permission did not change.
			query = c.QuerySpec{From: c.Source{SchemaID: target.ID}, Via: &c.LinkTraversal{LinkID: link.ID, Direction: "out", InstanceID: "1"}, Select: []string{target.Schema.Properties[0].ID}, Page: c.Page{Limit: 1}}
			first = f.query(query, 200)
			query.Page.Cursor = first.NextCursor
			f.mu.Lock()
			changed := f.current[source.ID]
			changed.ETag = digest("changed-origin")
			f.current[source.ID] = changed
			f.mu.Unlock()
			if _, err := f.engine.DB().ExecContext(f.ctx, "UPDATE af_data_gates SET digest=? WHERE tenant_id=? AND definition_id=?", changed.ETag, f.tenant, source.ID); err != nil {
				t.Fatal(err)
			}
			f.query(query, 422)
			f.authorize = nil
			// Tenant-local cleanup does not drop or truncate shared platform tables.
			t.Cleanup(func() {
				_, _ = f.engine.DB().ExecContext(context.Background(), "DELETE FROM af_data_outbox WHERE tenant_id=?", f.tenant)
			})
		})
	}
}

func TestLinkCompilersRequireResolvedRelationContext(t *testing.T) {
	d := testDefinition()
	query := c.QuerySpec{From: c.Source{SchemaID: d.ID}, Via: &c.LinkTraversal{LinkID: c.NewID(), Direction: "out", InstanceID: c.NewID()}, Select: []string{d.Schema.Properties[0].ID}, Page: c.Page{Limit: 10}}
	if _, err := compileQuery(d, "digest", c.Principal{TenantID: d.TenantID}, allowed(), query, strings.Repeat("s", 32)); err == nil {
		t.Fatal("unresolved Link silently became a single-schema query")
	}
	if _, err := compileMappedQuery(d, nil, "digest", c.Principal{TenantID: d.TenantID}, allowed(), query, strings.Repeat("s", 32), ""); err == nil {
		t.Fatal("unresolved mapped Link silently became a single-schema query")
	}
	encoded, err := json.Marshal(query)
	if err != nil || !strings.Contains(string(encoded), `"via"`) {
		t.Fatal("traversal omitted from cursor-bound query")
	}
}

func TestManagedLinkCardinalityAndConcurrentWrites(t *testing.T) {
	f := newMappedIntegrationFixture(t, "mysql", os.Getenv("MYSQL_TEST_DSN"))
	// This is a database concurrency contract, including contention across
	// replicas. Trusted in-process test context bypasses process admission so
	// eight writes still reach MySQL concurrently; production HTTP cannot set
	// this private ownership marker. Admission/429 is tested separately.
	storageApp := f.app
	f.app = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), dataBudgetContextKey{}, dataServiceLimiter)
		storageApp.ServeHTTP(w, r.WithContext(ctx))
	})
	property := c.Property{ID: c.NewID(), Name: "名称", DataType: "string", Required: true}
	schema := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: "基数测试对象", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{property}}}
	operation := c.NewID()
	if response := f.prepare(schema, "", operation); response.Code != 200 {
		t.Fatalf("prepare managed endpoint: %d %s", response.Code, response.Body)
	}
	f.activate(schema, operation)
	t.Cleanup(func() { _, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + quote(tableName(schema.ID))) })
	t.Cleanup(func() {
		for _, table := range []string{"af_data_idempotency", "af_data_outbox"} {
			_, _ = f.engine.DB().Exec("DELETE FROM "+quote(table)+" WHERE tenant_id=?", f.tenant)
		}
	})
	instances := []string{}
	for i := 0; i < 14; i++ {
		response := f.request("POST", "/data/schemas/"+schema.ID+"/instances", c.WriteRequest{Values: map[string]json.RawMessage{property.ID: raw(c.NewID())}, IdempotencyKey: c.NewID()}, false)
		var instance c.Instance
		if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &instance) != nil {
			t.Fatalf("create managed endpoint: %d %s", response.Code, response.Body)
		}
		instances = append(instances, instance.ID)
	}
	for _, cardinality := range []string{"N:M", "1:N", "N:1", "1:1"} {
		t.Run(cardinality, func(t *testing.T) {
			link := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: schema.CategoryID, GroupID: schema.GroupID, Kind: c.KindLink, Name: "测试" + cardinality, Link: &c.Link{SourceSchemaID: schema.ID, TargetSchemaID: schema.ID, ForwardName: "正向", ReverseName: "反向", Cardinality: cardinality, OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
			operation := c.NewID()
			if response := f.prepare(link, "", operation); response.Code != 200 {
				t.Fatalf("prepare managed relation: %d %s", response.Code, response.Body)
			}
			f.activate(link, operation)
			// Child cleanups run before the parent endpoint table is dropped.
			t.Cleanup(func() { _, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + quote(linkTableName(link.ID))) })
			create := func(source, target string) int {
				return f.request("POST", "/data/links/"+link.ID+"/instances", c.LinkWriteRequest{SourceID: source, TargetID: target}, false).Code
			}
			if got := create(instances[0], instances[1]); got != 201 {
				t.Fatalf("first relation status=%d", got)
			}
			if got := create(instances[0], instances[1]); got != 409 {
				t.Fatalf("duplicate relation accepted: %d", got)
			}
			want := 201
			if cardinality == "N:1" || cardinality == "1:1" {
				want = 409
			}
			if got := create(instances[0], instances[2]); got != want {
				t.Fatalf("source cardinality status=%d want=%d", got, want)
			}
			want = 201
			if cardinality == "1:N" || cardinality == "1:1" {
				want = 409
			}
			if got := create(instances[3], instances[1]); got != want {
				t.Fatalf("target cardinality status=%d want=%d", got, want)
			}
			if got := create(instances[4], instances[4]); got != 201 {
				t.Fatalf("legal self edge rejected: %d", got)
			}
			if got := create(c.NewID(), instances[4]); got != 404 {
				t.Fatalf("missing endpoint accepted: %d", got)
			}
			var wait sync.WaitGroup
			statuses := make(chan int, 8)
			for i := 0; i < 8; i++ {
				wait.Add(1)
				go func(index int) {
					defer wait.Done()
					source, target := instances[5], instances[6+index]
					if cardinality == "1:N" {
						source, target = target, source
					}
					statuses <- create(source, target)
				}(i)
			}
			wait.Wait()
			close(statuses)
			created, conflicts := 0, 0
			for status := range statuses {
				if status == 201 {
					created++
				} else if status == 409 {
					conflicts++
				} else {
					t.Fatalf("unexpected concurrent status=%d", status)
				}
			}
			if cardinality == "N:M" {
				if created != 8 {
					t.Fatalf("many-to-many relation incorrectly limited: %d", created)
				}
			} else if created != 1 || conflicts != 7 {
				t.Fatalf("concurrent unique cardinality violated: created=%d conflicts=%d", created, conflicts)
			}
			var edges, events int
			if err := f.engine.DB().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM "+quote(linkTableName(link.ID))+" WHERE tenant_id=?", f.tenant).Scan(&edges); err != nil {
				t.Fatal(err)
			}
			if err := f.engine.DB().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM af_data_outbox WHERE tenant_id=? AND schema_id=? AND event_type='link.created'", f.tenant, link.ID).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if events != edges {
				t.Fatalf("failed relation writes emitted outbox events: edges=%d events=%d", edges, events)
			}
		})
	}
}
