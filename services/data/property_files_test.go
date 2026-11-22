package data

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestMySQLPropertyFileUsesPatchAuthorization(t *testing.T) {
	f := newMappedIntegrationFixture(t, "mysql", os.Getenv("MYSQL_TEST_DSN"))
	d := testDefinition()
	d.ID, d.TenantID = c.NewID(), f.tenant
	field := c.Property{ID: c.NewID(), Name: "附件", DataType: "longtext", Nullable: true, FileKind: c.FilePropertyAttachmentList}
	d.Schema.Properties, d.Schema.Indexes = []c.Property{field}, nil
	publishRowDefinition(t, f, d)
	t.Cleanup(func() {
		_, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + quote(tableName(d.ID)))
		for _, table := range []string{"af_data_property_files", "af_data_outbox", "af_data_write_receipts", "af_data_idempotency"} {
			_, _ = f.engine.DB().Exec("DELETE FROM "+quote(table)+" WHERE tenant_id=?", f.tenant)
		}
	})
	base := "/data/schemas/" + d.ID + "/instances"
	created := f.request("POST", base, c.WriteRequest{IdempotencyKey: c.NewID()}, false)
	var row c.Instance
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &row) != nil {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	path := base + "/" + row.ID + "/files/" + field.ID
	data := []byte("test attachment")
	request := func(method, path, etag string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r = r.WithContext(platform.WithPrincipal(f.ctx, c.Principal{TenantID: f.tenant, Subject: "mapped-reader"}))
		r.Header.Set("If-Match", etag)
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, r)
		return w
	}
	etag := created.Header().Get("ETag")
	f.decision.WriteFields = nil
	if denied := request("PUT", path+"?name=sample.txt", etag); denied.Code != 403 {
		t.Fatalf("upload without field grant: %d %s", denied.Code, denied.Body)
	}
	f.decision.WriteFields = []string{field.ID}
	saved := request("PUT", path+"?name=sample.txt", etag)
	if saved.Code != 204 || saved.Header().Get("ETag") == etag {
		t.Fatalf("upload with patch authorization: %d %s", saved.Code, saved.Body)
	}
	read := f.request("GET", base+"/"+row.ID, nil, false)
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &row) != nil {
		t.Fatalf("read: %d %s", read.Code, read.Body)
	}
	files, err := parsePropertyFileList(row.Values[field.ID])
	if err != nil || len(files) != 1 {
		t.Fatalf("file summaries: %+v, %v", files, err)
	}
	download := request("GET", path+"?fileId="+files[0].ID, "")
	if download.Code != 200 || !bytes.Equal(download.Body.Bytes(), data) {
		t.Fatalf("download: %d", download.Code)
	}
	removed := request("DELETE", path+"?fileId="+files[0].ID, saved.Header().Get("ETag"))
	if removed.Code != 204 {
		t.Fatalf("remove: %d %s", removed.Code, removed.Body)
	}
}

func TestFilePropertyRequiresDedicatedWrite(t *testing.T) {
	d := testDefinition()
	p := c.Property{ID: c.NewID(), Name: "主图", DataType: "longtext", Nullable: true, FileKind: c.FilePropertyImage}
	d.Schema.Properties = []c.Property{p}
	d.Schema.Indexes = nil
	if err := validateSchema(d); err != nil {
		t.Fatal(err)
	}
	if _, err := propertyFileField(d, p.Name); err == nil {
		t.Fatal("file endpoint accepted a display name as field ID")
	}
	if _, err := propertyFileField(d, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := buildValues(d.Schema, map[string]json.RawMessage{p.ID: raw("forged summary")}, nil, []string{"*"}, false); err == nil {
		t.Fatal("generic mutation accepted file contents")
	}
	values, err := buildValues(d.Schema, nil, nil, []string{"*"}, true)
	if err != nil || string(values[p.ID]) != "null" {
		t.Fatalf("create should initialize optional file to null: %v", err)
	}
	d.Schema.Properties[0].Nullable = false
	if err := validateSchema(d); err == nil {
		t.Fatal("accepted non-nullable file property")
	}
}

func TestParsePropertyFileList(t *testing.T) {
	id := c.NewID()
	entry := c.FileSummary{ID: id, Name: "说明.txt", MediaType: "text/plain", Size: 5}
	encode := func(files []c.FileSummary) json.RawMessage {
		encoded, _ := json.Marshal(files)
		return raw(string(encoded))
	}
	files, err := parsePropertyFileList(encode([]c.FileSummary{entry}))
	if err != nil || len(files) != 1 || files[0] != entry {
		t.Fatalf("file list round trip: %v %v", files, err)
	}
	if files, err := parsePropertyFileList(json.RawMessage("null")); err != nil || len(files) != 0 {
		t.Fatalf("empty file field: %v %v", files, err)
	}
	for _, invalid := range []json.RawMessage{raw("{}"), encode([]c.FileSummary{entry, entry}), encode([]c.FileSummary{{Name: entry.Name}})} {
		if _, err := parsePropertyFileList(invalid); err == nil {
			t.Fatalf("accepted invalid file list: %s", invalid)
		}
	}
}
