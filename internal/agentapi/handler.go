// Package agentapi exposes a small semantic API suitable for REST and MCP adapters.
package agentapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agentforge/ontology/internal/ontology"
	"github.com/agentforge/ontology/internal/publish"
	"github.com/agentforge/ontology/internal/runtime"
)

type Handler struct {
	runtime   *runtime.Service
	logger    *slog.Logger
	mu        sync.Mutex
	revisions map[string]uint64
}

func New(rt *runtime.Service, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{runtime: rt, logger: logger, revisions: map[string]uint64{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("POST /v1/ontologies/publish", h.publish)
	mux.HandleFunc("POST /v1/ontology/search", h.search)
	mux.HandleFunc("POST /v1/ontology/describe", h.describe)
	mux.HandleFunc("POST /v1/objects/get", h.get)
	mux.HandleFunc("POST /v1/objects/query", h.query)
	mux.HandleFunc("POST /v1/objects/related", h.related)
	return requestID(mux)
}
func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	var draft ontology.Draft
	if !decode(w, r, &draft) {
		return
	}
	h.mu.Lock()
	revision := h.revisions[draft.Workspace.ID] + 1
	snapshot, errs := publish.Compile(draft, revision, time.Now())
	if len(errs) > 0 {
		h.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "validation_failed", "details": errs})
		return
	}
	h.revisions[draft.Workspace.ID] = revision
	h.runtime.Activate(snapshot)
	h.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"workspace": snapshot.Workspace.ID, "publicationId": snapshot.PublicationID,
		"revision": snapshot.Revision, "hash": snapshot.Hash, "publishedAt": snapshot.PublishedAt,
	})
}
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Workspace, Query string
		Limit            int
	}
	if !decode(w, r, &req) {
		return
	}
	hits, err := h.runtime.Search(r.Context(), req.Workspace, req.Query, req.Limit)
	respond(w, hits, err)
}
func (h *Handler) describe(w http.ResponseWriter, r *http.Request) {
	var req struct{ Workspace, Object string }
	if !decode(w, r, &req) {
		return
	}
	obj, err := h.runtime.Describe(r.Context(), req.Workspace, req.Object)
	respond(w, obj, err)
}
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Workspace, Object string
		ID                any
	}
	if !decode(w, r, &req) {
		return
	}
	item, err := h.runtime.Get(r.Context(), req.Workspace, req.Object, req.ID)
	respond(w, item, err)
}
func (h *Handler) query(w http.ResponseWriter, r *http.Request) {
	var req runtime.ObjectQuery
	if !decode(w, r, &req) {
		return
	}
	result, err := h.runtime.Query(r.Context(), req)
	respond(w, result, err)
}
func (h *Handler) related(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Workspace, Object string
		ID                any
		Link              string
		Fields            []string
		Limit             int
	}
	if !decode(w, r, &req) {
		return
	}
	result, err := h.runtime.Related(r.Context(), runtime.RelatedQuery{Workspace: req.Workspace, Object: req.Object, ID: req.ID, Link: req.Link, Fields: req.Fields, Limit: req.Limit})
	respond(w, result, err)
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "message": err.Error()})
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, value)
		return
	}
	status := http.StatusBadRequest
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]string{"error": "runtime_error", "message": err.Error()})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = time.Now().UTC().Format("20060102T150405.000000000")
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}
