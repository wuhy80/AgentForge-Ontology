package data

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestMySQLMappedForeignKeyCreateAuthorizationAndRollback(t *testing.T) {
	f := newMappedWriteFixture(t)
	target := f.current
	if err := InitializeMappedWrites(f.ctx, f.cfg, f.source.TenantID, target.Definition.ID); err != nil {
		t.Fatal(err)
	}
	response := f.request("POST", f.base(), c.WriteRequest{Values: f.values("library", "1"), IdempotencyKey: "library"}, "")
	f.requireStatus(response, 201)
	var library c.Instance
	_ = json.Unmarshal(response.Body.Bytes(), &library)
	var holder c.Definition
	raw, _ := json.Marshal(target.Definition)
	_ = json.Unmarshal(raw, &holder)
	holder.ID, holder.Name = c.NewID(), "持键对象"
	holder.Schema.StorageBinding.Table = "holder_" + strings.ReplaceAll(c.NewID(), "-", "")
	fk := c.NewID()
	holder.Schema.Properties = append(holder.Schema.Properties, c.Property{ID: fk, Name: "归属", DataType: "uuid", Required: true, Mapping: &c.PropertyMapping{DataSourceID: f.source.ID, Column: "parent_id"}})
	f.current = c.CurrentDefinition{Definition: holder, ETag: digest(holder), Status: "active"}
	f.extra = map[string]c.CurrentDefinition{target.Definition.ID: target}
	publish := func(d c.Definition) {
		t.Helper()
		operation := c.NewID()
		f.requireStatus(f.request("POST", "/internal/publications/prepare", c.PrepareRequest{Definition: d, Digest: digest(d), OperationID: operation}, ""), 200)
		f.requireStatus(f.request("POST", "/internal/publications/activate", c.ActivateRequest{DefinitionID: d.ID, Digest: digest(d), OperationID: operation}, ""), 200)
	}
	publish(holder)
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+quote(holder.Schema.StorageBinding.Table))
	})
	if err := InitializeMappedWrites(f.ctx, f.cfg, f.source.TenantID, holder.ID); err != nil {
		t.Fatal(err)
	}
	link := c.Definition{ID: c.NewID(), TenantID: f.source.TenantID, CategoryID: holder.CategoryID, GroupID: holder.GroupID, Kind: c.KindLink, Name: "归属关系", Link: &c.Link{SourceSchemaID: holder.ID, TargetSchemaID: target.Definition.ID, Cardinality: "N:1", OnDelete: "restrict", ForwardName: "归属", ReverseName: "包含", StorageBinding: &c.LinkStorageBinding{Mode: "foreign_key", DataSourceID: f.source.ID, ForeignKeySide: "source", ForeignKeyColumn: "parent_id"}}}
	f.extra[link.ID] = c.CurrentDefinition{Definition: link, ETag: digest(link), Status: "active"}
	publish(link)
	denyLink, denyTarget := false, false
	f.cfg.Authorizer = authorizerFunc(func(_ context.Context, _ c.Principal, request c.AuthorizationRequest) (c.Decision, error) {
		decision := c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: "relation-policy"}
		if request.Operation == "link.create" && denyLink {
			decision.Allow = false
		}
		if request.ResourceID == target.Definition.ID && request.Operation == "instance.read" && denyTarget {
			decision.RowFilter = &c.Expression{Op: "eq", Field: f.code, Value: json.RawMessage(`"invisible"`)}
		}
		return decision, nil
	})
	f.app = NewHandler(f.cfg)
	values := f.values("note", "2")
	values[fk] = json.RawMessage(strconvQuote(library.ID))
	denyLink = true
	f.requireStatus(f.request(http.MethodPost, f.base(), c.WriteRequest{Values: values, IdempotencyKey: "denied-link"}, ""), 403)
	denyLink, denyTarget = false, true
	f.requireStatus(f.request(http.MethodPost, f.base(), c.WriteRequest{Values: values, IdempotencyKey: "denied-row"}, ""), 403)
	denyTarget = false
	values[fk] = json.RawMessage(strconvQuote(c.NewID()))
	f.requireStatus(f.request(http.MethodPost, f.base(), c.WriteRequest{Values: values, IdempotencyKey: "missing-target"}, ""), 404)
	var count int
	if err := f.db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM "+quote(holder.Schema.StorageBinding.Table)).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed relation create left a row")
	}
	values[fk] = json.RawMessage(strconvQuote(library.ID))
	response = f.request(http.MethodPost, f.base(), c.WriteRequest{Values: values, IdempotencyKey: "valid-relation"}, "")
	f.requireStatus(response, 201)
	var note c.Instance
	_ = json.Unmarshal(response.Body.Bytes(), &note)
	if err := f.db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM af_source_write_outbox WHERE schema_id=? AND event_type='link.created'", link.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("relation event was not committed with holder")
	}
	f.requireStatus(f.request(http.MethodPatch, f.base()+"/"+note.ID, c.WriteRequest{Values: map[string]json.RawMessage{fk: values[fk]}}, note.ETag), 422)
	f.requireStatus(f.request(http.MethodDelete, f.base()+"/"+note.ID, nil, note.ETag), 422)
}
