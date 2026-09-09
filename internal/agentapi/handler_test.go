package agentapi

import (
	"bytes"
	"github.com/agentforge/ontology/internal/datasource"
	"github.com/agentforge/ontology/internal/runtime"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthAndInvalidPublish(t *testing.T) {
	registry := datasource.NewRegistry()
	_ = registry.Register(&datasource.MemoryDriver{})
	handler := New(runtime.New(registry), nil)
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status %d", health.Code)
	}
	publish := httptest.NewRecorder()
	handler.ServeHTTP(publish, httptest.NewRequest(http.MethodPost, "/v1/ontologies/publish", bytes.NewBufferString(`{"workspace":{"id":"bad id"}}`)))
	if publish.Code != http.StatusUnprocessableEntity {
		t.Fatalf("publish status %d: %s", publish.Code, publish.Body.String())
	}
}
