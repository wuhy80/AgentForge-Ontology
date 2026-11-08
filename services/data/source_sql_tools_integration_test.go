package data

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func TestSourceSQLToolsHTTPStaticOnlyAndDeliveryChecks(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err = storage.Migrate(ctx, engine.DB(), "data", Migrations); err != nil {
		t.Fatal(err)
	}
	tenant, sourceID := c.NewID(), c.NewID()
	defer engine.DB().Exec("DELETE FROM af_data_sources WHERE tenant_id=?", tenant)
	source := c.DataSource{ID: sourceID, TenantID: tenant, Name: "静态SQL工具", Driver: "mysql", Host: "static-tools-fixture.invalid", Port: 3306, Database: "business", Username: "fixture", TLSMode: "disable", TenantIsolation: "dedicated", Enabled: true, ETag: digest(c.NewID())}
	mode, calls := "", 0
	checker := sourceSessionCheckerFunc(func(context.Context, c.SessionCheck) (c.Session, error) {
		calls++
		user := c.User{ID: "actor", TenantID: tenant, Enabled: true, Administrator: true}
		if calls == 2 {
			switch mode {
			case "revoke":
				user.Administrator = false
			case "logout":
				return c.Session{}, &platform.AccessError{Status: 401, Code: "unauthenticated", Message: "会话已撤销"}
			case "change":
				if _, e := engine.DB().Exec("UPDATE af_data_sources SET etag=? WHERE tenant_id=? AND id=?", digest(c.NewID()), tenant, sourceID); e != nil {
					t.Fatal(e)
				}
			}
		}
		return c.Session{User: user, ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
	})
	app := NewHandler(Config{SourceNetworkPolicy: testSourceNetworkPolicy(t), Engine: engine, SessionChecker: checker})
	saveConfig := func() {
		t.Helper()
		body, _ := json.Marshal(source)
		if _, e := engine.DB().Exec("INSERT INTO af_data_sources(tenant_id,id,name,name_key,config_json,secret_cipher,endpoint_key,tenant_isolation,etag) VALUES(?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE config_json=VALUES(config_json),etag=VALUES(etag)", tenant, sourceID, source.Name, nameKey(source.Name), string(body), "unusable-encrypted-secret", digest(c.NewID()), source.TenantIsolation, source.ETag); e != nil {
			t.Fatal(e)
		}
	}
	saveConfig()
	request := func(operation, etag, actorTenant string, input c.SourceQuery, admin bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", "/data/sources/"+sourceID+"/sql/"+operation, bytes.NewReader(body))
		r.AddCookie(&http.Cookie{Name: platform.SessionCookieName, Value: strings.Repeat("s", 48)})
		r.Header.Set("Origin", "http://workbench.test")
		r.Host = "workbench.test"
		r.Header.Set("X-CSRF-Token", strings.Repeat("c", 48))
		if etag != "" {
			r.Header.Set("If-Match", `"`+etag+`"`)
		}
		p := c.Principal{TenantID: actorTenant, Subject: "actor"}
		r = r.WithContext(platform.WithSessionUser(platform.WithPrincipal(ctx, p), c.User{ID: p.Subject, TenantID: p.TenantID, Enabled: true, Administrator: admin}))
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	valid := c.SourceQuery{SQL: "select * from missing_table WHERE id = 9223372036854775807 LIMIT 20", Limit: 3}
	for _, operation := range []string{"analyze", "format"} {
		calls = 0
		w := request(operation, source.ETag, tenant, valid, true)
		var result c.SourceSQLToolResult
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Valid || result.Dialect != "mysql" || result.SQL == "" || !strings.Contains(result.Message, "执行时核验") || calls != 2 {
			t.Fatalf("static tools touched offline connection or invalid delivery: %d %s", w.Code, w.Body)
		}
		for _, test := range []struct {
			name, etag, actorTenant string
			admin                   bool
			status                  int
		}{{"nonadmin", source.ETag, tenant, false, 403}, {"foreign", source.ETag, c.NewID(), true, 404}, {"missing etag", "", tenant, true, 428}, {"stale etag", "stale", tenant, true, 412}} {
			calls = 0
			w = request(operation, test.etag, test.actorTenant, valid, test.admin)
			if w.Code != test.status {
				t.Fatalf("%s %s boundary: %d %s", operation, test.name, w.Code, w.Body)
			}
		}
		for _, state := range []string{"shared", "disabled"} {
			source.Enabled = state != "disabled"
			source.TenantIsolation = "dedicated"
			want := 409
			if state == "shared" {
				source.TenantIsolation = "column"
				want = 422
			}
			saveConfig()
			w = request(operation, source.ETag, tenant, valid, true)
			if w.Code != want {
				t.Fatalf("%s scope failed: %d %s", state, w.Code, w.Body)
			}
		}
		source.Enabled = true
		source.TenantIsolation = "dedicated"
		saveConfig()
		for _, bad := range []c.SourceQuery{{SQL: "UPDATE orders SET id=1"}, {SQL: valid.SQL, Limit: 101}} {
			calls = 0
			w = request(operation, source.ETag, tenant, bad, true)
			if w.Code != 422 {
				t.Fatalf("unsafe/static invalid request: %d %s", w.Code, w.Body)
			}
		}
		for _, change := range []string{"revoke", "logout", "change"} {
			mode = change
			calls = 0
			w = request(operation, source.ETag, tenant, valid, true)
			want := 403
			if mode == "logout" {
				want = 401
			}
			if mode == "change" {
				want = 412
			}
			if w.Code != want || strings.Contains(w.Body.String(), `"sql"`) {
				t.Fatalf("result leaked after %s: %d %s", mode, w.Code, w.Body)
			}
			mode = ""
			saveConfig()
		}
	}
}
