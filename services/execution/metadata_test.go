package execution

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMetadataReaderRejectsRedirectsAndWrongTenant(t *testing.T) {
	f := newFixture()
	var foreign atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreign.Add(1) }))
	defer target.Close()
	redirect := false
	wrongTenant := false
	draft := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 40) || r.Header.Get("X-Tenant-ID") != "tenant" {
			t.Error("missing trusted request identity")
		}
		if redirect {
			http.Redirect(w, r, target.URL, 302)
			return
		}
		d := f.fn
		if wrongTenant {
			d.TenantID = "other"
		}
		status := "active"
		if draft {
			if r.URL.Path != "/metadata/definitions/function/draft" {
				t.Error("draft read used wrong endpoint")
			}
			status = "draft"
		}
		writeJSON(w, 200, map[string]any{"definition": d, "etag": "etag", "status": status})
	}))
	defer server.Close()
	reader := &RemoteMetadata{URL: server.URL, ServiceToken: strings.Repeat("s", 40)}
	if _, err := reader.Current(context.Background(), "tenant", "function"); err != nil {
		t.Fatal(err)
	}
	draft = true
	if _, err := reader.Draft(context.Background(), "tenant", "function"); err != nil {
		t.Fatal(err)
	}
	wrongTenant = true
	if _, err := reader.Draft(context.Background(), "tenant", "function"); err == nil {
		t.Fatal("wrong-tenant draft accepted")
	}
	draft = false
	wrongTenant = true
	if _, err := reader.Current(context.Background(), "tenant", "function"); err == nil {
		t.Fatal("wrong-tenant current accepted")
	}
	redirect = true
	if _, err := reader.Current(context.Background(), "tenant", "function"); err == nil {
		t.Fatal("redirect followed")
	}
	if foreign.Load() != 0 {
		t.Fatal("service credential sent to redirect target")
	}
}
