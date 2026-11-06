package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	data "github.com/wuhy80/AgentForge-Ontology/services/data"
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
)

const authSecret = "integration-only-authentication-key-32-bytes"
const serviceToken = "integration-only-service-credential-32-bytes"
const cursorSecret = "integration-only-cursor-signing-key-32-bytes"

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

type harness struct {
	t      *testing.T
	client *http.Client
}

func (h harness) request(method, path, token, etag string, body any, want int, out any) http.Header {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if etag != "" {
		r.Header.Set("If-Match", etag)
	}
	r.Header.Set("Content-Type", "application/json")
	res, err := h.client.Do(r)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		h.t.Fatal(err)
	}
	if res.StatusCode != want {
		h.t.Fatalf("%s %s: got %d want %d: %s", method, r.URL.Path, res.StatusCode, want, b)
	}
	if out != nil && json.Unmarshal(b, out) != nil {
		h.t.Fatalf("invalid response on %s: %s", r.URL.Path, b)
	}
	return res.Header
}

func TestMySQLAndOPAEndToEnd(t *testing.T) {
	mdsn, ddsn, opaURL := os.Getenv("ONTOLOGY_TEST_METADATA_DSN"), os.Getenv("ONTOLOGY_TEST_DATA_DSN"), os.Getenv("ONTOLOGY_TEST_OPA_URL")
	if mdsn == "" || ddsn == "" || opaURL == "" {
		t.Skip("set ONTOLOGY_TEST_METADATA_DSN, ONTOLOGY_TEST_DATA_DSN and ONTOLOGY_TEST_OPA_URL to run real MySQL + OPA verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	me, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: mdsn, MaxOpen: 6, MaxIdle: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer me.Close()
	de, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: ddsn, MaxOpen: 6, MaxIdle: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer de.Close()
	if err := storage.Migrate(ctx, me.DB(), "metadata", metadata.Migrations); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(ctx, de.DB(), "data", data.Migrations); err != nil {
		t.Fatal(err)
	}
	h := harness{t: t, client: &http.Client{Timeout: 20 * time.Second}}
	tenant := "verification-" + c.NewID()
	otherTenant := "verification-other-" + c.NewID()
	codeID, nameID, deptID, priceID, countID := c.NewID(), c.NewID(), c.NewID(), c.NewID(), c.NewID()
	all := map[string]any{"allow": true, "readFields": []string{"*"}, "writeFields": []string{"*"}}
	adminOps := map[string]any{}
	for _, op := range []string{"organization.edit", "organization.read", "definition.edit", "definition.read", "definition.publish", "instance.create", "instance.read", "instance.patch", "instance.delete", "instance.query"} {
		adminOps[op] = map[string]any{"*": all}
	}
	readerGrant := map[string]any{"allow": true, "readFields": []string{codeID, nameID}, "rowFilter": c.Expression{Op: "eq", Field: deptID, Value: raw("D1")}}
	readerOps := map[string]any{"instance.read": map[string]any{"*": readerGrant}, "instance.query": map[string]any{"*": readerGrant}}
	h.request("PUT", opaURL+"/v1/data/grants/"+url.PathEscape(tenant), "", "", map[string]any{"admin": adminOps, "reader": readerOps}, 204, nil)
	h.request("PUT", opaURL+"/v1/data/grants/"+url.PathEscape(otherTenant), "", "", map[string]any{"admin": adminOps}, 204, nil)
	t.Cleanup(func() {
		h.request("DELETE", opaURL+"/v1/data/grants/"+url.PathEscape(tenant), "", "", nil, 204, nil)
		h.request("DELETE", opaURL+"/v1/data/grants/"+url.PathEscape(otherTenant), "", "", nil, 204, nil)
	})
	identity := func(subject, tenant string) string {
		token, err := platform.SignToken(authSecret, c.Principal{Subject: subject, TenantID: tenant, ExpiresAt: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	admin, reader, other := identity("admin", tenant), identity("reader", tenant), identity("admin", otherTenant)
	authorizer := &platform.OPAAuthorizer{URL: opaURL, Client: h.client}
	metaMux := http.NewServeMux()
	metaSrv := httptest.NewServer(metaMux)
	defer metaSrv.Close()
	var failActivation atomic.Bool
	dataApp := data.NewHandler(data.Config{Engine: de, Authorizer: authorizer, MetadataURL: metaSrv.URL, ServiceToken: serviceToken, CursorSecret: cursorSecret})
	dataSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/publications/activate" && failActivation.Load() {
			platform.WriteError(w, 503, "injected_failure", "verification interrupted activation")
			return
		}
		platform.ProtectRoutes(authSecret, serviceToken, false, dataApp).ServeHTTP(w, r)
	}))
	defer dataSrv.Close()
	metaApp := metadata.NewHandler(metadata.Config{Engine: me, Authorizer: authorizer, DataURL: dataSrv.URL, ServiceToken: serviceToken, HTTPClient: h.client})
	metaMux.Handle("/", platform.ProtectRoutes(authSecret, serviceToken, true, metaApp))

	t.Log("real OPA decisions; tenant-scoped organization and current-definition publication")
	var category, group c.Organization
	h.request("POST", metaSrv.URL+"/metadata/categories", admin, "", map[string]string{"name": "生产管理"}, 201, &category)
	h.request("POST", metaSrv.URL+"/metadata/groups", admin, "", map[string]string{"name": "设备模型", "categoryId": category.ID}, 201, &group)
	def := c.Definition{CategoryID: category.ID, GroupID: group.ID, Kind: c.KindSchema, Name: "设备", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{
		{ID: codeID, Name: "设备编码", DataType: "string", Required: true},
		{ID: nameID, Name: "设备名称", DataType: "string", Required: true},
		{ID: deptID, Name: "所属部门", DataType: "string", Required: true},
		{ID: priceID, Name: "采购价", DataType: "decimal", Precision: 18, Scale: 3, Nullable: true},
		{ID: countID, Name: "累计计数", DataType: "int64", Required: true},
	}, Indexes: []c.Index{{ID: c.NewID(), Name: "设备编码唯一", Unique: true, FieldIDs: []string{codeID}}}}}
	var draft c.CurrentDefinition
	draftHeaders := h.request("POST", metaSrv.URL+"/metadata/definitions", admin, "", def, 201, &draft)
	schemaID := draft.Definition.ID
	definitionURL := metaSrv.URL + "/metadata/definitions/" + schemaID
	instancesURL := dataSrv.URL + "/data/schemas/" + schemaID + "/instances"
	h.request("GET", definitionURL, admin, "", nil, 404, nil)
	h.request("POST", metaSrv.URL+"/metadata/definitions", admin, "", c.Definition{CategoryID: category.ID, GroupID: group.ID, Kind: c.KindEnum, Name: "设备", Enum: &c.Enum{Values: []c.EnumValue{{Code: "A", Label: "甲"}}}}, 409, nil)
	h.request("GET", definitionURL, other, "", nil, 404, nil)

	failActivation.Store(true)
	h.request("POST", definitionURL+"/publish", admin, draftHeaders.Get("ETag"), nil, 503, nil)
	var gate string
	if err := de.DB().QueryRowContext(ctx, "SELECT status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", tenant, schemaID).Scan(&gate); err != nil {
		t.Fatal(err)
	}
	if gate == "ready" || gate == "active" {
		t.Fatal("failed activation reopened data gate")
	}
	h.request("POST", instancesURL, admin, "", c.WriteRequest{Values: map[string]json.RawMessage{codeID: raw("PUMP-000")}, IdempotencyKey: c.NewID()}, 409, nil)
	failActivation.Store(false)
	var pub c.Publication
	h.request("POST", definitionURL+"/publish", admin, draftHeaders.Get("ETag"), nil, 200, &pub)
	if pub.Status != "succeeded" {
		t.Fatalf("publication not completed: %+v", pub)
	}
	var current c.CurrentDefinition
	h.request("GET", definitionURL, admin, "", nil, 200, &current)
	if current.Definition.ID != schemaID || current.Status != "active" {
		t.Fatal("wrong current definition")
	}

	t.Log("typed instance persistence, idempotency, unique keys, ETags and outbox")
	create := func(code, department string, price any) c.Instance {
		var result c.Instance
		body := c.WriteRequest{Values: map[string]json.RawMessage{codeID: raw(code), nameID: raw("输送泵"), deptID: raw(department), priceID: raw(price), countID: raw("9007199254740993")}, IdempotencyKey: c.NewID()}
		h.request("POST", instancesURL, admin, "", body, 201, &result)
		if string(result.Values[countID]) != `"9007199254740993"` {
			t.Fatalf("int64 lost precision: %s", result.Values[countID])
		}
		var replay c.Instance
		h.request("POST", instancesURL, admin, "", body, 200, &replay)
		if replay.ID != result.ID {
			t.Fatal("idempotent retry created a new instance")
		}
		body.Values[nameID] = raw("different intent")
		h.request("POST", instancesURL, admin, "", body, 409, nil)
		return result
	}
	first := create("PUMP-001", "D1", "123456789012345.678")
	second := create("PUMP-002", "D1", nil)
	_ = create("PUMP-003", "D2", "3.100")
	h.request("POST", instancesURL, admin, "", c.WriteRequest{Values: map[string]json.RawMessage{codeID: raw("PUMP-001"), nameID: raw("duplicate"), deptID: raw("D1"), countID: raw("1")}, IdempotencyKey: c.NewID()}, 409, nil)
	var outboxCount int
	if err := de.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM af_data_outbox WHERE tenant_id=?", tenant).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 3 {
		t.Fatalf("duplicate or failed create wrote outbox: %d", outboxCount)
	}
	itemURL := instancesURL + "/" + first.ID
	var read c.Instance
	oldHeaders := h.request("GET", itemURL, admin, "", nil, 200, &read)
	h.request("GET", itemURL, other, "", nil, 404, nil)
	h.request("PATCH", itemURL, admin, "", c.WriteRequest{Values: map[string]json.RawMessage{nameID: raw("新名称")}}, 428, nil)
	h.request("PATCH", itemURL, admin, oldHeaders.Get("ETag"), c.WriteRequest{Values: map[string]json.RawMessage{nameID: raw("新名称")}}, 200, &read)
	h.request("PATCH", itemURL, admin, oldHeaders.Get("ETag"), c.WriteRequest{Values: map[string]json.RawMessage{nameID: raw("过期覆盖")}}, 412, nil)

	t.Log("Chinese binding, row/column enforcement and signed stable pagination")
	query := c.QuerySpec{From: c.Source{Category: "生产管理", Schema: "设备"}, Select: []string{"设备编码", "设备名称"}, OrderBy: []c.Order{{Field: "设备编码", Direction: "asc"}}, Page: c.Page{Limit: 1}}
	var page c.QueryResult
	h.request("POST", dataSrv.URL+"/data/query", reader, "", query, 200, &page)
	if len(page.Data) != 1 || page.Data[0].ID != first.ID || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page: %+v", page)
	}
	if len(page.Data[0].Values) != 2 || len(page.Fields) != 2 {
		t.Fatal("hidden fields leaked")
	}
	oldCursor := page.NextCursor
	query.Page.Cursor = oldCursor
	h.request("POST", dataSrv.URL+"/data/query", reader, "", query, 200, &page)
	if len(page.Data) != 1 || page.Data[0].ID != second.ID || page.HasMore {
		t.Fatalf("second page: %+v", page)
	}
	query.Page.Cursor = oldCursor + "tampered"
	h.request("POST", dataSrv.URL+"/data/query", reader, "", query, 422, nil)
	query.Page.Cursor = ""
	query.Select = []string{"采购价"}
	h.request("POST", dataSrv.URL+"/data/query", reader, "", query, 403, nil)
	query.Select = []string{"设备编码"}
	query.Where = &c.Expression{Op: "gt", Field: "采购价", Value: raw("0.000")}
	h.request("POST", dataSrv.URL+"/data/query", reader, "", query, 403, nil)
	query.Where = &c.Expression{Op: "eq", Field: "设备编码", Value: raw("' OR 1=1 --")}
	h.request("POST", dataSrv.URL+"/data/query", admin, "", query, 200, &page)
	if len(page.Data) != 0 {
		t.Fatal("query literal escaped into SQL")
	}
	query.Where = &c.Expression{Op: "isNull", Field: "采购价"}
	h.request("POST", dataSrv.URL+"/data/query", admin, "", query, 200, &page)
	if len(page.Data) != 1 || page.Data[0].ID != second.ID {
		t.Fatal("NULL semantics incorrect")
	}

	t.Log("rename preserves IDs; old cursors invalidate; revocation applies to next access")
	renamed := current.Definition
	renamed.Name = "泵设备"
	var renamedDraft c.CurrentDefinition
	renameHeaders := h.request("PUT", definitionURL+"/draft", admin, `"`+current.ETag+`"`, renamed, 200, &renamedDraft)
	h.request("POST", definitionURL+"/publish", admin, renameHeaders.Get("ETag"), nil, 200, &pub)
	query = c.QuerySpec{From: c.Source{SchemaID: schemaID}, Select: []string{"设备编码", "设备名称"}, OrderBy: []c.Order{{Field: "设备编码", Direction: "asc"}}, Page: c.Page{Limit: 1, Cursor: oldCursor}}
	h.request("POST", dataSrv.URL+"/data/query", reader, "", query, 422, nil)
	h.request("GET", metaSrv.URL+"/metadata/resolve?category="+url.QueryEscape("生产管理")+"&name="+url.QueryEscape("设备")+"&kind=schema", admin, "", nil, 404, nil)
	h.request("GET", metaSrv.URL+"/metadata/resolve?category="+url.QueryEscape("生产管理")+"&name="+url.QueryEscape("泵设备")+"&kind=schema", admin, "", nil, 200, &current)
	if current.Definition.ID != schemaID {
		t.Fatal("rename changed identity")
	}
	h.request("PUT", opaURL+"/v1/data/grants/"+url.PathEscape(tenant)+"/reader", "", "", map[string]any{}, 204, nil)
	h.request("GET", itemURL, reader, "", nil, 403, nil)
	query.Page.Cursor = ""
	h.request("POST", dataSrv.URL+"/data/query", reader, "", query, 403, nil)
	h.request("GET", itemURL, serviceToken, "", nil, 401, nil)
	h.request("POST", dataSrv.URL+"/internal/publications/activate", admin, "", c.ActivateRequest{DefinitionID: schemaID, Digest: current.ETag}, 401, nil)
	t.Logf("verified schema %s in isolated tenant %s; only this test's new records were created", schemaID, tenant)
}
