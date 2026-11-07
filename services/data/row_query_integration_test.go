package data

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func publishRowDefinition(t *testing.T, f *mappedIntegrationFixture, definition c.Definition) {
	t.Helper()
	operation := c.NewID()
	if response := f.prepare(definition, "", operation); response.Code != 200 {
		t.Fatalf("prepare row definition: %d %s", response.Code, response.Body)
	}
	f.activate(definition, operation)
}

func allRowPages(t *testing.T, f *mappedIntegrationFixture, q c.QuerySpec) []c.QueryRow {
	t.Helper()
	result, seen := []c.QueryRow{}, map[string]bool{}
	for page := 0; page < 30; page++ {
		response := f.query(q, 200)
		if !response.RowMode || len(response.Data) != 0 {
			t.Fatal("join rows were misrepresented as root instances")
		}
		for _, row := range response.Rows {
			if seen[row.ID] {
				t.Fatal("tuple pagination repeated a binding")
			}
			seen[row.ID] = true
			result = append(result, row)
		}
		if !response.HasMore {
			return result
		}
		if response.NextCursor == "" || len(response.Rows) == 0 {
			t.Fatal("join page has no continuation")
		}
		q.Page.Cursor = response.NextCursor
	}
	t.Fatal("join pagination did not terminate")
	return nil
}

func TestRowQueryMappedMultiLinkIntegration(t *testing.T) {
	for _, backend := range []struct{ driver, env string }{{"mysql", "MYSQL_TEST_DSN"}, {"postgresql", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			f := newMappedIntegrationFixture(t, backend.driver, sourceIntegrationDSN(backend.driver, backend.env))
			index := 1
			suffix := strings.ReplaceAll(c.NewID(), "-", "")[:12]
			aName, bName, cName, eName := "row_a_"+suffix, "row_b_"+suffix, "row_c_"+suffix, "row_e_"+suffix
			columns := "tenant VARCHAR(128) NOT NULL,id BIGINT NOT NULL,name VARCHAR(255) NOT NULL,owner VARCHAR(255) NOT NULL,fk BIGINT NULL,amount DECIMAL(20,4) NULL,counter BIGINT NOT NULL,PRIMARY KEY(tenant,id)"
			for _, name := range []string{aName, bName, cName} {
				f.createTable(index, name, columns)
			}
			f.createTable(index, eName, "tenant VARCHAR(128) NOT NULL,source_id BIGINT NOT NULL,target_id BIGINT NOT NULL,PRIMARY KEY(tenant,source_id,target_id)")
			insert := func(table, tenant string, id int, owner string, fk any, amount any) {
				f.exec(index, "INSERT INTO "+f.qualified(index, table)+" (tenant,id,name,owner,fk,amount,counter) VALUES (?,?,?,?,?,?,?)", tenant, id, "同名", owner, fk, amount, int64(9007199254740993))
			}
			for _, row := range []struct {
				id    int
				owner string
			}{{1, "O1"}, {2, "O2"}} {
				insert(aName, f.tenant, row.id, row.owner, nil, nil)
			}
			for _, row := range []struct {
				id, fk int
				owner  string
				amount any
			}{{10, 100, "O1", "9007199254740993.0001"}, {11, 101, "O1", nil}, {12, 100, "O2", "1.0000"}, {13, 102, "O1", "2.0000"}, {14, 100, "O1", "9007199254740993.0000"}} {
				insert(bName, f.tenant, row.id, row.owner, row.fk, row.amount)
			}
			for _, row := range []struct {
				id    int
				owner string
			}{{100, "O1"}, {101, "O1"}, {102, "O2"}} {
				insert(cName, f.tenant, row.id, row.owner, nil, nil)
			}
			for _, pair := range [][2]int{{1, 10}, {1, 11}, {1, 12}, {1, 13}, {1, 14}, {2, 10}} {
				f.exec(index, "INSERT INTO "+f.qualified(index, eName)+" (tenant,source_id,target_id) VALUES (?,?,?)", f.tenant, pair[0], pair[1])
			}
			// These IDs and relation pairs exist in a shared physical source; no
			// case-insensitive collation may turn them into this tenant's rows.
			insert(aName, strings.ToUpper(f.tenant), 99, "O1", nil, nil)
			f.exec(index, "INSERT INTO "+f.qualified(index, eName)+" (tenant,source_id,target_id) VALUES (?,?,?)", strings.ToUpper(f.tenant), 1, 100)
			makeSchema := func(name string) c.Definition {
				namespace, _ := sourceNamespace(f.sources[index], "")
				definition := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: name, Schema: &c.Schema{Mode: "db_2_schema", StorageLayout: "single", StorageBinding: &c.StorageBinding{DataSourceID: f.sources[index].ID, Namespace: namespace, Table: name, IDColumn: "id", TenantColumn: "tenant"}}}
				for _, property := range []c.Property{{Name: "名称", DataType: "string", Required: true}, {Name: "归属", DataType: "string", Required: true}, {Name: "金额", DataType: "decimal", Precision: 20, Scale: 4, Nullable: true}, {Name: "计数", DataType: "int64", Required: true}} {
					property.ID = c.NewID()
					property.Mapping = &c.PropertyMapping{DataSourceID: f.sources[index].ID, Column: map[string]string{"名称": "name", "归属": "owner", "金额": "amount", "计数": "counter"}[property.Name]}
					definition.Schema.Properties = append(definition.Schema.Properties, property)
				}
				return definition
			}
			a, b, third := makeSchema(aName), makeSchema(bName), makeSchema(cName)
			for _, definition := range []c.Definition{a, b, third} {
				publishRowDefinition(t, f, definition)
			}
			ab := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: a.CategoryID, GroupID: a.GroupID, Kind: c.KindLink, Name: "A关联B", Link: &c.Link{SourceSchemaID: a.ID, TargetSchemaID: b.ID, ForwardName: "关联B", ReverseName: "关联A", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "junction", DataSourceID: f.sources[index].ID, Table: eName, SourceColumn: "source_id", TargetColumn: "target_id", TenantColumn: "tenant"}}}
			bc := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: b.CategoryID, GroupID: b.GroupID, Kind: c.KindLink, Name: "B归属C", Link: &c.Link{SourceSchemaID: b.ID, TargetSchemaID: third.ID, ForwardName: "归属C", ReverseName: "拥有B", Cardinality: "N:1", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: f.sources[index].ID, ForeignKeySide: "source", ForeignKeyColumn: "fk"}}}
			publishRowDefinition(t, f, ab)
			publishRowDefinition(t, f, bc)
			policyState := "initial"
			f.authorize = func(request c.AuthorizationRequest) c.Decision {
				decision := c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: policyState}
				if request.Operation == "instance.query" {
					for _, definition := range []c.Definition{a, b, third} {
						if definition.ID == request.ResourceID {
							decision.RowFilter = &c.Expression{Op: "eq", Field: definition.Schema.Properties[1].ID, Value: raw("O1")}
						}
					}
				}
				return decision
			}
			query := c.QuerySpec{From: c.Source{SchemaID: a.ID}, Rows: &c.RowQuery{Nodes: []c.RowQueryNode{{ID: "n0", SchemaID: a.ID, Alias: "a"}, {ID: "n1", SchemaID: b.ID, Alias: "b"}, {ID: "n2", SchemaID: third.ID, Alias: "c"}}, Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: ab.ID, Direction: "out"}, {From: "n1", To: "n2", LinkID: bc.ID, Direction: "out"}}}, Select: []string{"n0." + a.Schema.Properties[0].ID, "n1." + b.Schema.Properties[2].ID, "n2." + third.Schema.Properties[3].ID}, Page: c.Page{Limit: 1}}
			rows := allRowPages(t, f, query)
			if len(rows) != 3 || rows[0].Entities[1].InstanceID != "10" || rows[1].Entities[1].InstanceID != "11" || rows[2].Entities[1].InstanceID != "14" || string(rows[0].Values[query.Select[1]]) != `"9007199254740993.0001"` || string(rows[0].Values[query.Select[2]]) != `"9007199254740993"` {
				t.Fatalf("multi-link policy/precision/tuple result: %+v", rows)
			}
			for _, direction := range []string{"asc", "desc"} {
				sorted := query
				sorted.OrderBy = []c.Order{{Field: query.Select[1], Direction: direction}}
				rows = allRowPages(t, f, sorted)
				want := []string{"11", "14", "10"}
				if direction == "desc" {
					want = []string{"10", "14", "11"}
				}
				for i, id := range want {
					if len(rows) != 3 || rows[i].Entities[1].InstanceID != id {
						t.Fatalf("precise nullable %s order: %+v", direction, rows)
					}
				}
			}
			// Preserve separate bindings even when selected business values match.
			branch := query
			branch.Rows = &c.RowQuery{Nodes: []c.RowQueryNode{query.Rows.Nodes[0], query.Rows.Nodes[1], {ID: "n2", SchemaID: b.ID, Alias: "b2"}}, Joins: []c.RowQueryJoin{query.Rows.Joins[0], {From: "n0", To: "n2", LinkID: ab.ID, Direction: "out"}}}
			branch.Select = []string{query.Select[0]}
			if rows := allRowPages(t, f, branch); len(rows) != 16 {
				t.Fatalf("branch bindings were lost or deduplicated: %d", len(rows))
			}
			reverse := query
			reverse.From.SchemaID = third.ID
			reverse.Rows = &c.RowQuery{Nodes: []c.RowQueryNode{{ID: "n0", SchemaID: third.ID, Alias: "c"}, {ID: "n1", SchemaID: b.ID, Alias: "b"}, {ID: "n2", SchemaID: a.ID, Alias: "a"}}, Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: bc.ID, Direction: "in"}, {From: "n1", To: "n2", LinkID: ab.ID, Direction: "in"}}}
			reverse.Select = []string{"n0." + third.Schema.Properties[0].ID}
			if rows := allRowPages(t, f, reverse); len(rows) != 3 {
				t.Fatalf("reverse multi-link result: %+v", rows)
			}
			first := f.query(query, 200)
			stale := query
			stale.Page.Cursor = first.NextCursor
			policyState = "changed"
			f.query(stale, 422)
			policyState = "initial"
			f.mu.Lock()
			original := f.current[third.ID]
			definitionChanged := original
			definitionChanged.ETag = digest("changed definition")
			f.current[third.ID] = definitionChanged
			f.mu.Unlock()
			f.query(stale, 422)
			f.mu.Lock()
			f.current[third.ID] = original
			f.mu.Unlock()
			// Physical catalog changes also invalidate a cursor even when all
			// published definitions and permissions keep their identities.
			f.exec(index, "ALTER TABLE "+f.qualified(index, bName)+" ADD COLUMN query_cursor_change INT NULL")
			f.query(stale, 422)
			wrongRoot := query
			wrongRoot.From.SchemaID = b.ID
			f.query(wrongRoot, 422)
			baseAuthorize := f.authorize
			for _, deny := range []string{"link", "field", "definition"} {
				f.authorize = func(request c.AuthorizationRequest) c.Decision {
					decision := baseAuthorize(request)
					if deny == "link" && request.Operation == "link.query" && request.ResourceID == bc.ID || deny == "definition" && request.Operation == "definition.read" && request.ResourceID == third.ID {
						decision.Allow = false
					}
					if deny == "field" && request.Operation == "instance.query" && request.ResourceID == third.ID {
						decision.ReadFields = []string{third.Schema.Properties[0].ID}
					}
					return decision
				}
				f.query(query, 403)
			}
			f.authorize = baseAuthorize
			// A same-type identifier at a different source is explicitly rejected.
			f.mu.Lock()
			changed := f.current[third.ID]
			copySchema, copyBinding := *changed.Definition.Schema, *changed.Definition.Schema.StorageBinding
			copyBinding.DataSourceID = f.sources[0].ID
			copySchema.StorageBinding = &copyBinding
			copySchema.Properties = append([]c.Property(nil), copySchema.Properties...)
			for i := range copySchema.Properties {
				copyMapping := *copySchema.Properties[i].Mapping
				copyMapping.DataSourceID = f.sources[0].ID
				copySchema.Properties[i].Mapping = &copyMapping
			}
			changed.Definition.Schema = &copySchema
			f.current[third.ID] = changed
			f.mu.Unlock()
			response := f.request("POST", "/data/query", query, false)
			if response.Code != 422 || !strings.Contains(response.Body.String(), "cross_source_link_unsupported") {
				t.Fatalf("cross-source join was not rejected: %d %s", response.Code, response.Body)
			}
		})
	}
}

func TestRowQueryManagedSelfLinkIntegration(t *testing.T) {
	f := newMappedIntegrationFixture(t, "mysql", os.Getenv("MYSQL_TEST_DSN"))
	property := c.Property{ID: c.NewID(), Name: "名称", DataType: "string", Required: true}
	definition := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: "自关联对象", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{property}}}
	publishRowDefinition(t, f, definition)
	t.Cleanup(func() { _, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + quote(tableName(definition.ID))) })
	t.Cleanup(func() {
		for _, table := range []string{"af_data_idempotency", "af_data_outbox"} {
			_, _ = f.engine.DB().Exec("DELETE FROM "+quote(table)+" WHERE tenant_id=?", f.tenant)
		}
	})
	ids := []string{}
	for i := 0; i < 3; i++ {
		response := f.request("POST", "/data/schemas/"+definition.ID+"/instances", c.WriteRequest{Values: map[string]json.RawMessage{property.ID: raw("同名")}, IdempotencyKey: c.NewID()}, false)
		var instance c.Instance
		if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &instance) != nil {
			t.Fatalf("create managed row: %d %s", response.Code, response.Body)
		}
		ids = append(ids, instance.ID)
	}
	link := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: definition.CategoryID, GroupID: definition.GroupID, Kind: c.KindLink, Name: "自关联", Link: &c.Link{SourceSchemaID: definition.ID, TargetSchemaID: definition.ID, ForwardName: "后继", ReverseName: "前驱", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	publishRowDefinition(t, f, link)
	t.Cleanup(func() { _, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + quote(linkTableName(link.ID))) })
	for _, pair := range [][2]int{{0, 1}, {1, 2}} {
		response := f.request("POST", "/data/links/"+link.ID+"/instances", c.LinkWriteRequest{SourceID: ids[pair[0]], TargetID: ids[pair[1]]}, false)
		if response.Code != 201 {
			t.Fatalf("create managed link: %d %s", response.Code, response.Body)
		}
	}
	q := c.QuerySpec{From: c.Source{SchemaID: definition.ID}, Rows: &c.RowQuery{Nodes: []c.RowQueryNode{{ID: "n0", SchemaID: definition.ID, Alias: "a"}, {ID: "n1", SchemaID: definition.ID, Alias: "b"}, {ID: "n2", SchemaID: definition.ID, Alias: "c"}}, Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: link.ID, Direction: "out"}, {From: "n1", To: "n2", LinkID: link.ID, Direction: "out"}}}, Select: []string{"n0." + property.ID, "n1." + property.ID, "n2." + property.ID}, Page: c.Page{Limit: 1}}
	rows := allRowPages(t, f, q)
	if len(rows) != 1 || rows[0].Entities[0].InstanceID != ids[0] || rows[0].Entities[2].InstanceID != ids[2] {
		t.Fatalf("managed self-link did not execute both hops: %+v", rows)
	}
	// The supported maximum of four joins can traverse the same definition
	// in both directions while each occurrence retains a separate alias.
	q.Rows.Nodes = append(q.Rows.Nodes, c.RowQueryNode{ID: "n3", SchemaID: definition.ID, Alias: "d"}, c.RowQueryNode{ID: "n4", SchemaID: definition.ID, Alias: "e"})
	q.Rows.Joins = append(q.Rows.Joins, c.RowQueryJoin{From: "n2", To: "n3", LinkID: link.ID, Direction: "in"}, c.RowQueryJoin{From: "n3", To: "n4", LinkID: link.ID, Direction: "in"})
	rows = allRowPages(t, f, q)
	if len(rows) != 1 || rows[0].Entities[4].InstanceID != ids[0] {
		t.Fatalf("four-link self-query failed: %+v", rows)
	}
}

func TestRowQueryStringKeysUseExactComparisonsIntegration(t *testing.T) {
	for _, backend := range []struct{ driver, env string }{{"mysql", "MYSQL_TEST_DSN"}, {"postgresql", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			f := newMappedIntegrationFixture(t, backend.driver, sourceIntegrationDSN(backend.driver, backend.env))
			index, suffix := 1, strings.ReplaceAll(c.NewID(), "-", "")[:12]
			name, edgeName := "row_keys_"+suffix, "row_key_edges_"+suffix
			keyType := "VARCHAR(64)"
			if backend.driver == "mysql" {
				keyType += " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
			}
			f.createTable(index, name, "tenant VARCHAR(128) NOT NULL,id "+keyType+" NOT NULL,name VARCHAR(255) NOT NULL,PRIMARY KEY(tenant,id)")
			f.createTable(index, edgeName, "tenant VARCHAR(128) NOT NULL,source_id VARCHAR(64) NOT NULL,target_id VARCHAR(64) NOT NULL,PRIMARY KEY(tenant,source_id,target_id)")
			for _, id := range []string{"A", "a", "B", "C"} {
				f.exec(index, "INSERT INTO "+f.qualified(index, name)+" (tenant,id,name) VALUES (?,?,?)", f.tenant, id, "同名")
			}
			for _, pair := range [][2]string{{"A", "a"}, {"a", "B"}, {"á", "C"}} {
				f.exec(index, "INSERT INTO "+f.qualified(index, edgeName)+" (tenant,source_id,target_id) VALUES (?,?,?)", f.tenant, pair[0], pair[1])
			}
			namespace, _ := sourceNamespace(f.sources[index], "")
			property := c.Property{ID: c.NewID(), Name: "名称", DataType: "string", Required: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[index].ID, Column: "name"}}
			definition := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: name, Schema: &c.Schema{Mode: "db_2_schema", StorageLayout: "single", StorageBinding: &c.StorageBinding{DataSourceID: f.sources[index].ID, Namespace: namespace, Table: name, IDColumn: "id", TenantColumn: "tenant"}, Properties: []c.Property{property}}}
			publishRowDefinition(t, f, definition)
			link := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: definition.CategoryID, GroupID: definition.GroupID, Kind: c.KindLink, Name: "精确主键关联", Link: &c.Link{SourceSchemaID: definition.ID, TargetSchemaID: definition.ID, ForwardName: "后继", ReverseName: "前驱", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "junction", DataSourceID: f.sources[index].ID, Table: edgeName, SourceColumn: "source_id", TargetColumn: "target_id", TenantColumn: "tenant"}}}
			publishRowDefinition(t, f, link)
			q := c.QuerySpec{From: c.Source{SchemaID: definition.ID}, Rows: &c.RowQuery{Nodes: []c.RowQueryNode{{ID: "n0", SchemaID: definition.ID, Alias: "a"}, {ID: "n1", SchemaID: definition.ID, Alias: "b"}}, Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: link.ID, Direction: "out"}}}, Select: []string{"n0." + property.ID}, Page: c.Page{Limit: 1}}
			rows := allRowPages(t, f, q)
			if len(rows) != 2 || rows[0].Entities[0].InstanceID != "A" || rows[1].Entities[0].InstanceID != "a" {
				t.Fatalf("string key joins or cursor changed case/accents: %+v", rows)
			}
			q.Rows.Nodes = append(q.Rows.Nodes, c.RowQueryNode{ID: "n2", SchemaID: definition.ID, Alias: "c"})
			q.Rows.Joins = append(q.Rows.Joins, c.RowQueryJoin{From: "n1", To: "n2", LinkID: link.ID, Direction: "out"})
			rows = allRowPages(t, f, q)
			if len(rows) != 1 || rows[0].Entities[0].InstanceID != "A" || rows[0].Entities[2].InstanceID != "B" {
				t.Fatalf("string key multi-link was not exact: %+v", rows)
			}
		})
	}
}

func TestRowQueryExplicitForwardSourcesIntegration(t *testing.T) {
	for _, backend := range []struct{ driver, env string }{{"mysql", "MYSQL_TEST_DSN"}, {"postgresql", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			f := newMappedIntegrationFixture(t, backend.driver, sourceIntegrationDSN(backend.driver, backend.env))
			index, suffix := 1, strings.ReplaceAll(c.NewID(), "-", "")[:12]
			makeSchema := func(prefix string) c.Definition {
				namespace, _ := sourceNamespace(f.sources[index], "")
				name := "row_forward_" + prefix + "_" + suffix
				f.dropOnCleanup(index, name)
				definition := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: c.NewID(), GroupID: c.NewID(), Kind: c.KindSchema, Name: name, Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", StorageBinding: &c.StorageBinding{DataSourceID: f.sources[index].ID, Namespace: namespace, Table: name, IDColumn: "id", TenantColumn: "tenant"}, Properties: []c.Property{{ID: c.NewID(), Name: "标题", DataType: "string", Required: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[index].ID, Column: "title"}}, {ID: c.NewID(), Name: "归属主键", DataType: "uuid", Nullable: true, Mapping: &c.PropertyMapping{DataSourceID: f.sources[index].ID, Column: "parent_id"}}}}}
				publishRowDefinition(t, f, definition)
				return definition
			}
			a, b, third := makeSchema("a"), makeSchema("b"), makeSchema("c")
			rootID, otherID, targetID := c.NewID(), c.NewID(), c.NewID()
			for _, row := range []struct {
				definition c.Definition
				id, title  string
				parent     any
			}{{a, rootID, "来源", nil}, {b, otherID, "中间对象", targetID}, {third, targetID, "目标", nil}} {
				f.exec(index, "INSERT INTO "+f.qualified(index, row.definition.Schema.StorageBinding.Table)+" (tenant,id,title,parent_id) VALUES (?,?,?,?)", f.tenant, row.id, row.title, row.parent)
			}
			edgeName := "row_forward_edges_" + suffix
			keyType := "CHAR(36)"
			if backend.driver == "postgresql" {
				keyType = "UUID"
			}
			f.createTable(index, edgeName, "tenant VARCHAR(128) NOT NULL,source_id "+keyType+" NOT NULL,target_id "+keyType+" NOT NULL,PRIMARY KEY(tenant,source_id,target_id)")
			f.exec(index, "INSERT INTO "+f.qualified(index, edgeName)+" (tenant,source_id,target_id) VALUES (?,?,?)", f.tenant, rootID, otherID)
			ab := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: a.CategoryID, GroupID: a.GroupID, Kind: c.KindLink, Name: "正向对象关联", Link: &c.Link{SourceSchemaID: a.ID, TargetSchemaID: b.ID, ForwardName: "关联", ReverseName: "被关联", Cardinality: "N:M", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "junction", DataSourceID: f.sources[index].ID, Table: edgeName, SourceColumn: "source_id", TargetColumn: "target_id", TenantColumn: "tenant"}}}
			bc := c.Definition{ID: c.NewID(), TenantID: f.tenant, CategoryID: b.CategoryID, GroupID: b.GroupID, Kind: c.KindLink, Name: "正向对象归属", Link: &c.Link{SourceSchemaID: b.ID, TargetSchemaID: third.ID, ForwardName: "归属", ReverseName: "包含", Cardinality: "N:1", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: f.sources[index].ID, ForeignKeySide: "source", ForeignKeyColumn: "parent_id"}}}
			publishRowDefinition(t, f, ab)
			publishRowDefinition(t, f, bc)
			q := c.QuerySpec{From: c.Source{SchemaID: a.ID}, Rows: &c.RowQuery{Nodes: []c.RowQueryNode{{ID: "n0", SchemaID: a.ID, Alias: "a"}, {ID: "n1", SchemaID: b.ID, Alias: "b"}, {ID: "n2", SchemaID: third.ID, Alias: "c"}}, Joins: []c.RowQueryJoin{{From: "n0", To: "n1", LinkID: ab.ID, Direction: "out"}, {From: "n1", To: "n2", LinkID: bc.ID, Direction: "out"}}}, Select: []string{"n0." + a.Schema.Properties[0].ID, "n1." + b.Schema.Properties[0].ID, "n2." + third.Schema.Properties[0].ID}, Where: &c.Expression{Op: "eq", Field: "n1." + b.Schema.Properties[0].ID, Value: raw("中间对象")}, Page: c.Page{Limit: 1}}
			rows := allRowPages(t, f, q)
			if len(rows) != 1 || rows[0].Entities[0].InstanceID != rootID || rows[0].Entities[2].InstanceID != targetID || string(rows[0].Values[q.Select[2]]) != `"目标"` {
				t.Fatalf("explicit forward source used platform table, column or identity rules: %+v", rows)
			}
		})
	}
}
