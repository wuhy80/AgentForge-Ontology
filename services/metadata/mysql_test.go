package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

type publicationTestBlock struct {
	entered   chan string
	release   chan struct{}
	failFirst bool
	calls     atomic.Int32
	once      sync.Once
}

func newPublicationTestBlock(t *testing.T, failFirst bool) *publicationTestBlock {
	t.Helper()
	b := &publicationTestBlock{entered: make(chan string, 1), release: make(chan struct{}), failFirst: failFirst}
	t.Cleanup(b.unblock)
	return b
}

func (b *publicationTestBlock) unblock() { b.once.Do(func() { close(b.release) }) }

func (b *publicationTestBlock) wait(t *testing.T) string {
	t.Helper()
	select {
	case id := <-b.entered:
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not reach the coordinated phase")
		return ""
	}
}

func (b *publicationTestBlock) hold(id string) bool {
	if b == nil || b.calls.Add(1) != 1 {
		return false
	}
	b.entered <- id
	<-b.release
	return b.failFirst
}

// METADATA_TEST_DSN opts into a real MySQL test database. No existing rows are
// deleted: each run uses fresh tenants and server-generated object identities.
func TestMySQLLifecycleAndPublicationRecovery(t *testing.T) {
	dsn := os.Getenv("METADATA_TEST_DSN")
	if dsn == "" {
		t.Skip("set METADATA_TEST_DSN to exercise the real MySQL metadata contract")
	}
	engine, err := storage.Open(context.Background(), storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 8, MaxIdle: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := storage.Migrate(context.Background(), engine.DB(), "metadata", Migrations); err != nil {
		t.Fatal(err)
	}
	var prepareFailure, activateFailure atomic.Bool
	var prepares, activations atomic.Int32
	var publicationIDs sync.Map
	var prepareBlock, activateBlock atomic.Pointer[publicationTestBlock]
	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tenant-ID") == "" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
			t.Error("missing trusted coordination context")
		}
		switch r.URL.Path {
		case "/internal/publications/prepare":
			prepares.Add(1)
			var request contracts.PrepareRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if previous, loaded := publicationIDs.LoadOrStore(request.OperationID, request.Digest); loaded && previous != request.Digest {
				t.Error("retry changed publication intent")
			}
			if prepareBlock.Load().hold(request.OperationID) {
				w.WriteHeader(503)
				return
			}
			if prepareFailure.Swap(false) {
				w.WriteHeader(503)
				return
			}
			writeJSON(w, 200, contracts.PrepareResult{OperationID: request.OperationID, Status: "prepared"})
		case "/internal/publications/activate":
			activations.Add(1)
			var request contracts.ActivateRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if activateBlock.Load().hold(request.OperationID) {
				w.WriteHeader(503)
				return
			}
			if activateFailure.Swap(false) {
				w.WriteHeader(503)
				return
			}
			writeJSON(w, 200, map[string]string{"status": "ready"})
		case "/internal/publications/preview":
			var request contracts.PrepareRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			writeJSON(w, 200, contracts.PrepareResult{OperationID: request.OperationID, Status: "preview", Statements: []string{"CREATE TABLE <managed mapping>"}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(data.Close)
	var hiddenID atomic.Value
	hiddenID.Store("")
	authorizer := authorizerFunc(func(_ context.Context, principal contracts.Principal, req contracts.AuthorizationRequest) (contracts.Decision, error) {
		return contracts.Decision{Allow: !(principal.Subject == "restricted" && req.ResourceID == hiddenID.Load().(string))}, nil
	})
	api := NewHandler(Config{Engine: engine, Authorizer: authorizer, DataURL: data.URL, ServiceToken: strings.Repeat("x", 32)})
	tenant := "metadata-test-" + contracts.NewID()
	request := func(method, path, tenantID, etag string, body any) *httptest.ResponseRecorder {
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r = r.WithContext(platform.WithPrincipal(r.Context(), contracts.Principal{Subject: "test-user", TenantID: tenantID}))
		if etag != "" {
			r.Header.Set("If-Match", etagHeader(etag))
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	decodeCurrent := func(w *httptest.ResponseRecorder) contracts.CurrentDefinition {
		t.Helper()
		var value contracts.CurrentDefinition
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatalf("decode current: %v: %s", err, w.Body.String())
		}
		return value
	}
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("HTTP %d, want %d: %s", w.Code, status, w.Body.String())
		}
	}
	w := request("POST", "/metadata/categories", tenant, "", map[string]string{"name": "生产管理"})
	check(w, 201)
	var category contracts.Organization
	_ = json.Unmarshal(w.Body.Bytes(), &category)
	w = request("POST", "/metadata/groups", tenant, "", map[string]string{"categoryId": category.ID, "name": "默认"})
	check(w, 201)
	var group contracts.Organization
	_ = json.Unmarshal(w.Body.Bytes(), &group)
	definition := validSchema()
	definition.ID = ""
	definition.TenantID = ""
	definition.CategoryID = category.ID
	definition.GroupID = group.ID
	w = request("POST", "/metadata/definitions", tenant, "", definition)
	check(w, 201)
	draft := decodeCurrent(w)
	id := draft.Definition.ID
	check(request("GET", "/metadata/definitions/"+id, tenant, "", nil), 404)
	check(request("GET", "/metadata/definitions/"+id, strings.ToUpper(tenant), "", nil), 404)
	check(request("POST", "/metadata/definitions/"+id+"/preview", tenant, "", nil), 200)
	if prepares.Load() != 0 || activations.Load() != 0 {
		t.Fatal("preview started publication")
	}
	var activePublication string
	if err := engine.DB().QueryRow("SELECT publication_id FROM af_meta_tenant_guards WHERE tenant_id=?", tenant).Scan(&activePublication); err != nil {
		t.Fatal(err)
	}
	if activePublication != "" {
		t.Fatal("preview occupied the tenant publication guard")
	}
	check(request("GET", "/metadata/definitions/"+id, tenant, "", nil), 404)
	check(request("PUT", "/metadata/definitions/"+id+"/draft", tenant, "stale", draft.Definition), 412)
	duplicate := contracts.Definition{CategoryID: category.ID, GroupID: group.ID, Kind: contracts.KindEnum, Name: " 设备 ", Enum: &contracts.Enum{Values: []contracts.EnumValue{{Code: "A", Label: "甲"}}}}
	check(request("POST", "/metadata/definitions", tenant, "", duplicate), 409)
	prepareFailure.Store(true)
	check(request("POST", "/metadata/definitions/"+id+"/publish", tenant, draft.ETag, nil), 503)
	check(request("POST", "/metadata/categories", tenant, "", map[string]string{"name": "暂停中不可创建"}), 409)
	check(request("GET", "/metadata/definitions/"+id, tenant, "", nil), 404)
	activateFailure.Store(true)
	check(request("POST", "/metadata/definitions/"+id+"/publish", tenant, draft.ETag, nil), 503)
	w = request("GET", "/metadata/definitions/"+id, tenant, "", nil)
	check(w, 200)
	current := decodeCurrent(w)
	check(request("PUT", "/metadata/definitions/"+id+"/draft", tenant, current.ETag, current.Definition), 409)
	preparedBefore := prepares.Load()
	check(request("POST", "/metadata/definitions/"+id+"/publish", tenant, draft.ETag, nil), 200)
	if prepares.Load() != preparedBefore {
		t.Fatal("activation recovery redundantly reran prepare")
	}
	var events int
	if err := engine.DB().QueryRow("SELECT COUNT(*) FROM af_meta_outbox WHERE tenant_id=? AND object_id=?", tenant, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("publication emitted %d events", events)
	}
	before := activations.Load()
	check(request("POST", "/metadata/definitions/"+id+"/publish", tenant, draft.ETag, nil), 200)
	if activations.Load() != before {
		t.Fatal("completed publication reactivated")
	}
	current.Definition.Name = "生产设备"
	w = request("PUT", "/metadata/definitions/"+id+"/draft", tenant, current.ETag, current.Definition)
	check(w, 200)
	renamed := decodeCurrent(w)
	check(request("GET", "/metadata/resolve?category=生产管理&name=设备", tenant, "", nil), 200)
	check(request("GET", "/metadata/resolve?category=生产管理&name=生产设备", tenant, "", nil), 404)
	check(request("POST", "/metadata/definitions/"+id+"/publish", tenant, renamed.ETag, nil), 200)
	check(request("GET", "/metadata/resolve?category=生产管理&name=设备", tenant, "", nil), 404)
	w = request("GET", "/metadata/resolve?category=生产管理&name=生产设备", tenant, "", nil)
	check(w, 200)
	if decodeCurrent(w).Definition.ID != id {
		t.Fatal("rename changed identity")
	}
	// Returning to old content must create a fresh operation, even across
	// several A -> B -> A -> B cycles with identical candidate digests.
	seenOperations := map[string]bool{}
	decodePublication := func(w *httptest.ResponseRecorder) contracts.Publication {
		t.Helper()
		check(w, 200)
		var pub contracts.Publication
		if err := json.Unmarshal(w.Body.Bytes(), &pub); err != nil || pub.Status != "succeeded" || pub.ID == "" {
			t.Fatalf("invalid completed publication: %s", w.Body.String())
		}
		return pub
	}
	var lastPublication contracts.Publication
	for _, name := range []string{"设备", "生产设备", "设备", "生产设备"} {
		renamed.Definition.Name = name
		w = request("PUT", "/metadata/definitions/"+id+"/draft", tenant, renamed.ETag, renamed.Definition)
		check(w, 200)
		renamed = decodeCurrent(w)
		lastPublication = decodePublication(request("POST", "/metadata/definitions/"+id+"/publish", tenant, renamed.ETag, nil))
		if seenOperations[lastPublication.ID] {
			t.Fatal("new content transition reused a historical publication operation")
		}
		seenOperations[lastPublication.ID] = true
		w = request("GET", "/metadata/definitions/"+id, tenant, "", nil)
		check(w, 200)
		if got := decodeCurrent(w); got.Definition.Name != name || got.ETag != renamed.ETag {
			t.Fatalf("historical digest did not replace current: %s", w.Body.String())
		}
		check(request("GET", "/metadata/definitions/"+id+"/draft", tenant, "", nil), 404)
		check(request("GET", "/metadata/resolve?category=生产管理&name="+name, tenant, "", nil), 200)
		otherName := "设备"
		if name == otherName {
			otherName = "生产设备"
		}
		check(request("GET", "/metadata/resolve?category=生产管理&name="+otherName, tenant, "", nil), 404)
	}
	// An identical current draft clears without a new operation or outbox.
	beforePrepares, beforeActivations := prepares.Load(), activations.Load()
	w = request("PUT", "/metadata/definitions/"+id+"/draft", tenant, renamed.ETag, renamed.Definition)
	check(w, 200)
	if pub := decodePublication(request("POST", "/metadata/definitions/"+id+"/publish", tenant, renamed.ETag, nil)); pub.ID != lastPublication.ID {
		t.Fatal("no-op draft did not return the current publication")
	}
	if pub := decodePublication(request("POST", "/metadata/definitions/"+id+"/publish", tenant, renamed.ETag, nil)); pub.ID != lastPublication.ID {
		t.Fatal("completed retry selected an older publication for the same digest")
	}
	if prepares.Load() != beforePrepares || activations.Load() != beforeActivations {
		t.Fatal("no-op draft contacted Data")
	}
	check(request("GET", "/metadata/definitions/"+id+"/draft", tenant, "", nil), 404)
	// Force two retries of the same operation to finish in the reverse order.
	// The delayed failure must not overwrite a committed or succeeded phase.
	for _, pendingActivation := range []bool{false, true} {
		name := "设备"
		if pendingActivation {
			name = "生产设备"
		}
		renamed.Definition.Name = name
		w = request("PUT", "/metadata/definitions/"+id+"/draft", tenant, renamed.ETag, renamed.Definition)
		check(w, 200)
		renamed = decodeCurrent(w)
		preparation := newPublicationTestBlock(t, true)
		prepareBlock.Store(preparation)
		first := make(chan *httptest.ResponseRecorder, 1)
		go func() { first <- request("POST", "/metadata/definitions/"+id+"/publish", tenant, renamed.ETag, nil) }()
		operationID := preparation.wait(t)
		var second *httptest.ResponseRecorder
		if pendingActivation {
			activation := newPublicationTestBlock(t, true)
			activateBlock.Store(activation)
			secondResult := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				secondResult <- request("POST", "/metadata/definitions/"+id+"/publish", tenant, renamed.ETag, nil)
			}()
			if activation.wait(t) != operationID {
				t.Fatal("concurrent retry changed the operation identity")
			}
			preparation.unblock()
			if pub := decodePublication(<-first); pub.ID != operationID {
				t.Fatal("late prepare failure changed operation identity")
			}
			activation.unblock()
			second = <-secondResult
			activateBlock.Store(nil)
		} else {
			second = request("POST", "/metadata/definitions/"+id+"/publish", tenant, renamed.ETag, nil)
			preparation.unblock()
			if pub := decodePublication(<-first); pub.ID != operationID {
				t.Fatal("completed retry changed operation identity")
			}
		}
		prepareBlock.Store(nil)
		if pub := decodePublication(second); pub.ID != operationID {
			t.Fatal("parallel retry created another publication")
		}
	}
	if err := engine.DB().QueryRow("SELECT COUNT(*) FROM af_meta_outbox WHERE tenant_id=? AND object_id=?", tenant, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 8 {
		t.Fatalf("content transitions and concurrent retries emitted %d events, want 8", events)
	}
	renamed.Definition.Schema.Properties[0].DataType = "text"
	w = request("PUT", "/metadata/definitions/"+id+"/draft", tenant, renamed.ETag, renamed.Definition)
	check(w, 200)
	structural := decodeCurrent(w)
	prepareBefore := prepares.Load()
	check(request("POST", "/metadata/definitions/"+id+"/publish", tenant, structural.ETag, nil), 422)
	if prepares.Load() != prepareBefore {
		t.Fatal("incompatible change reached Data prepare")
	}
	additional := contracts.Definition{CategoryID: category.ID, GroupID: group.ID, Kind: contracts.KindEnum, Name: "设备状态", Enum: &contracts.Enum{Values: []contracts.EnumValue{{Code: "RUNNING", Label: "运行中"}}}}
	w = request("POST", "/metadata/definitions", tenant, "", additional)
	check(w, 201)
	additionalDraft := decodeCurrent(w)
	check(request("POST", "/metadata/definitions/"+additionalDraft.Definition.ID+"/publish", tenant, additionalDraft.ETag, nil), 200)
	hiddenID.Store(id)
	r := httptest.NewRequest("GET", "/metadata/definitions?limit=1", nil)
	r = r.WithContext(platform.WithPrincipal(r.Context(), contracts.Principal{Subject: "restricted", TenantID: tenant}))
	visiblePage := httptest.NewRecorder()
	api.ServeHTTP(visiblePage, r)
	check(visiblePage, 200)
	var listed struct {
		Data       []contracts.CurrentDefinition `json:"data"`
		HasMore    bool                          `json:"hasMore"`
		NextCursor string                        `json:"nextCursor"`
	}
	if err := json.Unmarshal(visiblePage.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data) != 1 || listed.Data[0].Definition.ID != additionalDraft.Definition.ID || listed.HasMore || listed.NextCursor != "" || strings.Contains(visiblePage.Body.String(), id) {
		t.Fatalf("hidden definition affected visible pagination: %s", visiblePage.Body.String())
	}
}
