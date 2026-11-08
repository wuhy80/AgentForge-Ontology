package data

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestSourceVersionsHTTPConfigurationChecksAndLegacy(t *testing.T) {
	for _, backend := range []struct{ driver, env string }{{"mysql", "MYSQL_TEST_DSN"}, {"postgresql", "POSTGRES_SOURCE_TEST_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			f := newMappedIntegrationFixture(t, backend.driver, os.Getenv(backend.env))
			saved := f.sources[1]
			input := mappedTestSourceInput(t, backend.driver, os.Getenv(backend.env), "版本校验")
			request := func(method, path string, body any, etag, tenant string) *httptest.ResponseRecorder {
				t.Helper()
				raw, _ := json.Marshal(body)
				r := httptest.NewRequest(method, path, bytes.NewReader(raw))
				if tenant == "" {
					tenant = f.tenant
				}
				p := c.Principal{TenantID: tenant, Subject: "version-admin"}
				r = r.WithContext(platform.WithSessionUser(platform.WithPrincipal(f.ctx, p), c.User{ID: p.Subject, TenantID: tenant, Enabled: true, Administrator: true}))
				if etag != "" {
					r.Header.Set("If-Match", `"`+etag+`"`)
				}
				w := httptest.NewRecorder()
				f.app.ServeHTTP(w, r)
				return w
			}
			if saved.VersionPolicy != "auto" || saved.Compatibility == nil || saved.Compatibility.CompatibilityStatus != "supported" || !saved.Compatibility.Connected || saved.Compatibility.ServerVersion == "" {
				t.Fatal("enabled source was saved without a real supported-version observation")
			}
			path := "/data/sources/" + saved.ID
			originalPassword := *input.Password
			input.Password = nil
			input.VersionPolicy, input.ExpectedVersionLine = "pinned", saved.Compatibility.VersionLine
			if w := request("POST", path+"/test", input, "", ""); w.Code != 428 {
				t.Fatalf("missing test precondition: %d", w.Code)
			}
			if w := request("POST", path+"/test", input, "stale", ""); w.Code != 412 {
				t.Fatalf("stale test precondition: %d", w.Code)
			}
			w := request("POST", path+"/test", input, saved.ETag, "")
			var check c.DataSourceTestResult
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &check) != nil || !check.Success || check.Compatibility.VersionLine != input.ExpectedVersionLine {
				t.Fatalf("edited test did not reuse saved password: %d %s", w.Code, w.Body)
			}
			if strings.Contains(w.Body.String(), originalPassword) {
				t.Fatal("test response exposed retained password")
			}
			// Testing current form values does not save their version policy.
			w = request("GET", path, nil, "", "")
			var unchanged c.DataSource
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &unchanged) != nil || unchanged.VersionPolicy != "auto" || unchanged.ETag != saved.ETag {
				t.Fatal("form test changed saved configuration")
			}
			wrong := "wrong-password-must-not-leak"
			input.Password = &wrong
			w = request("POST", path+"/test", input, saved.ETag, "")
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &check) != nil || check.Success || check.Compatibility.Connected || check.Compatibility.CompatibilityStatus != "failed" || strings.Contains(w.Body.String(), wrong) {
				t.Fatal("edited credentials were ignored or connection failure was not sanitized")
			}
			w = request("PUT", path, input, saved.ETag, "")
			if w.Code != 422 {
				t.Fatal("enabled configuration accepted a failed connection check")
			}
			input.Password = nil
			w = request("PUT", path, input, saved.ETag, "")
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.VersionPolicy != "pinned" || !saved.PasswordSet || saved.Compatibility == nil {
				t.Fatalf("checked pin/save failed: %d %s", w.Code, w.Body)
			}
			etag := saved.ETag
			w = request("POST", path+"/test", nil, "", "")
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &check) != nil || !check.Success {
				t.Fatal("saved configuration test failed")
			}
			w = request("GET", path, nil, "", "")
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.ETag != etag || saved.Compatibility.CheckedAt != check.Compatibility.CheckedAt {
				t.Fatal("observation refresh changed ETag or was not persisted")
			}
			if w := request("POST", path+"/test", nil, "", "another-tenant"); w.Code != 404 {
				t.Fatal("version diagnostics crossed tenant boundary")
			}
			selected := f.request("GET", "/data/sources?view=select", nil, false)
			if selected.Code != 200 || strings.Contains(selected.Body.String(), `"serverVersion"`) || strings.Contains(selected.Body.String(), originalPassword) || strings.Contains(selected.Body.String(), `"host"`) {
				t.Fatal("model selector exposed server details or credentials")
			}
			// Simulate an old stored record. Reading supplies defaults without
			// rewriting config JSON; actual use still performs version checks.
			_, err := f.engine.DB().ExecContext(f.ctx, "UPDATE af_data_sources SET config_json=JSON_REMOVE(config_json,'$.versionPolicy','$.expectedVersionLine','$.compatibility') WHERE tenant_id=? AND id=?", f.tenant, saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			w = request("GET", path, nil, "", "")
			saved = c.DataSource{}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.VersionPolicy != "auto" || saved.ExpectedVersionLine != "" || saved.Compatibility != nil || saved.ETag != etag {
				t.Fatal("legacy source was rewritten or marked verified")
			}
			w = request("GET", path+"/tables", nil, "", "")
			if w.Code != 200 {
				t.Fatalf("legacy source could not be inspected at use: %d %s", w.Code, w.Body)
			}
			// Bypassing the form by corrupting a legacy policy still cannot
			// bypass the common physical connection guard.
			_, err = f.engine.DB().ExecContext(f.ctx, "UPDATE af_data_sources SET config_json=JSON_SET(config_json,'$.versionPolicy','pinned','$.expectedVersionLine','999') WHERE tenant_id=? AND id=?", f.tenant, saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			w = request("GET", path+"/tables", nil, "", "")
			if w.Code != 422 || !strings.Contains(w.Body.String(), "unsupported_database_version") {
				t.Fatal("runtime directory ignored a source version constraint")
			}
			// Disabled records can be saved without opening unreachable hosts.
			input.Name, input.Enabled, input.VersionPolicy, input.ExpectedVersionLine = "离线版本配置", false, "auto", ""
			input.Host, input.Port = "127.0.0.1", 1
			input.Password = &originalPassword
			w = request("POST", "/data/sources", input, "", "")
			saved = c.DataSource{}
			if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Compatibility != nil {
				t.Fatal("offline save required a network connection or retained stale observation")
			}
			if w := request("POST", "/data/sources/"+saved.ID+"/test", nil, "", ""); w.Code != 409 {
				t.Fatal("disabled saved source was opened")
			}
			input.Enabled = true
			if w := request("PUT", "/data/sources/"+saved.ID, input, saved.ETag, ""); w.Code != 403 || !strings.Contains(w.Body.String(), "source_network_denied") {
				t.Fatal("unlisted destination was not rejected by the operator network policy")
			}
		})
	}
}
