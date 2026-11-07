package data

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func sourceTestInput() c.DataSourceInput {
	secret := "fixture-password-must-not-leak"
	return c.DataSourceInput{Name: "业务数据库", Driver: "mysql", Host: "127.0.0.1", Database: "business", Username: "operator", Password: &secret, Enabled: true}
}

func TestSourceSecretEncryptionBindsTenantAndSourceIdentity(t *testing.T) {
	h := &handler{cfg: Config{DataSourceKey: strings.Repeat("source-secret-", 4)}}
	secret := "database_password_秘密"
	sealed, err := h.encryptSourceSecret("tenant-a", "source-a", secret)
	if err != nil || strings.Contains(sealed, secret) {
		t.Fatal("secret was not encrypted")
	}
	other, err := h.encryptSourceSecret("tenant-a", "source-a", secret)
	if err != nil || sealed == other {
		t.Fatal("encryption must use independent random nonces")
	}
	opened, err := h.decryptSourceSecret("tenant-a", "source-a", sealed)
	if err != nil || opened != secret {
		t.Fatal("encrypted source cannot be recovered")
	}
	for _, identity := range [][2]string{{"tenant-b", "source-a"}, {"tenant-a", "source-b"}} {
		if _, err = h.decryptSourceSecret(identity[0], identity[1], sealed); err == nil {
			t.Fatal("copied ciphertext crossed an identity boundary")
		}
	}
	h.cfg.DataSourceKey = strings.Repeat("different-key", 4)
	if _, err = h.decryptSourceSecret("tenant-a", "source-a", sealed); err == nil {
		t.Fatal("wrong encryption key was accepted")
	}
	h.cfg.DataSourceKey = ""
	if _, err = h.encryptSourceSecret("tenant-a", "source-a", secret); err == nil {
		t.Fatal("missing key must fail closed")
	}
}

func TestSourceValidationRejectsDSNInjectionAndUnsupportedDrivers(t *testing.T) {
	for name, change := range map[string]func(*c.DataSourceInput){
		"host URL":            func(in *c.DataSourceInput) { in.Host = "mysql://db.internal" },
		"host parameters":     func(in *c.DataSourceInput) { in.Host = "db.internal?sslmode=disable" },
		"host port":           func(in *c.DataSourceInput) { in.Host = "db.internal:3307" },
		"database parameters": func(in *c.DataSourceInput) { in.Database = "business?multiStatements=true" },
		"driver":              func(in *c.DataSourceInput) { in.Driver = "sqlite" },
		"port":                func(in *c.DataSourceInput) { in.Port = -1 },
		"TLS":                 func(in *c.DataSourceInput) { in.TLSMode = "prefer" },
		"isolation":           func(in *c.DataSourceInput) { in.TenantIsolation = "none" },
		"version policy":      func(in *c.DataSourceInput) { in.VersionPolicy = "force" },
		"auto with expected":  func(in *c.DataSourceInput) { in.ExpectedVersionLine = "26.7" },
		"unknown version":     func(in *c.DataSourceInput) { in.VersionPolicy, in.ExpectedVersionLine = "pinned", "999" },
	} {
		t.Run(name, func(t *testing.T) {
			input := sourceTestInput()
			change(&input)
			if _, err := sourceConfig(input); err == nil {
				t.Fatal("unsafe connection configuration was accepted")
			}
		})
	}
	input := sourceTestInput()
	input.Driver, input.Host = "postgresql", "::1"
	source, err := sourceConfig(input)
	if err != nil || source.Port != 5432 || source.TenantIsolation != "column" {
		t.Fatal("PostgreSQL defaults or IPv6 normalization failed")
	}
}

func sourceRequest(method, path string, admin bool) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	p := c.Principal{Subject: "actor", TenantID: "tenant"}
	ctx := platform.WithPrincipal(r.Context(), p)
	ctx = platform.WithSessionUser(ctx, c.User{ID: p.Subject, TenantID: p.TenantID, Administrator: admin, Enabled: true})
	return r.WithContext(ctx)
}

func TestSourceManagementRequiresCurrentSessionAdministrator(t *testing.T) {
	// A nil Engine makes accidental authorization bypass fail immediately.
	h := NewHandler(Config{})
	for _, path := range []string{"/data/sources", "/data/sources/source", "/data/sources/source/test", "/data/sources/test"} {
		method := "GET"
		if strings.HasSuffix(path, "/test") {
			method = "POST"
		}
		r := sourceRequest(method, path, false)
		r.Header.Set("X-Administrator", "true")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("forged administrator accepted on %s: %d", path, w.Code)
		}
	}
	r := sourceRequest("GET", "/data/sources", true)
	r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{Subject: "someone-else", TenantID: "tenant"}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("session user and principal mismatch was accepted")
	}
}

func TestSourceCatalogRejectsConditionalDefinitionAuthorization(t *testing.T) {
	h := NewHandler(Config{Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, request c.AuthorizationRequest) (c.Decision, error) {
		if request.Operation != "definition.edit" {
			t.Fatalf("catalog must use the existing definition edit permission, got %s", request.Operation)
		}
		return c.Decision{Allow: true, State: "current", RowFilter: &c.Expression{Op: "eq", Field: "department", Value: json.RawMessage(`"A"`)}}, nil
	})})
	for _, path := range []string{"/data/sources?view=select", "/data/sources/source/tables"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, sourceRequest("GET", path, false))
		if w.Code != 422 {
			t.Fatalf("conditional metadata authorization was ignored: %d", w.Code)
		}
	}
}

func TestPhysicalSourceIdentifiersAreQuotedAndBoundariesAreChecked(t *testing.T) {
	quoted, err := sourceQuote("postgresql", `column";drop table users;--`)
	if err != nil || quoted != `"column"";drop table users;--"` {
		t.Fatal("PostgreSQL identifier was not quoted")
	}
	quoted, err = sourceQuote("mysql", "column`;drop table users;--")
	if err != nil || quoted != "`column``;drop table users;--`" {
		t.Fatal("MySQL identifier was not quoted")
	}
	if _, err = sourceQuote("postgresql", strings.Repeat("中文", 12)); err == nil {
		t.Fatal("PostgreSQL byte limit was not enforced")
	}
	if _, err = sourceNamespace(c.DataSource{Driver: "mysql", Database: "business"}, "other_tenant"); err == nil {
		t.Fatal("MySQL database boundary was ignored")
	}
	if _, err = sourceNamespace(c.DataSource{Driver: "postgresql"}, "pg_catalog"); err == nil {
		t.Fatal("system namespace was selectable")
	}
	if err = validSourceTable(c.DataSource{Driver: "mysql"}, "af_identity_users"); err == nil {
		t.Fatal("platform credential table was selectable")
	}
}

func TestUnsupportedSourceTypesAreNotSilentlyRounded(t *testing.T) {
	for _, item := range []struct{ driver, native, complete string }{{"mysql", "bigint", "bigint unsigned"}, {"mysql", "double", "double"}, {"postgresql", "float8", "double precision"}, {"postgresql", "jsonb", "jsonb"}} {
		if sourceLogicalType(item.driver, item.native, item.complete, 0) != "unsupported" {
			t.Fatalf("lossy type was silently mapped: %#v", item)
		}
	}
}
