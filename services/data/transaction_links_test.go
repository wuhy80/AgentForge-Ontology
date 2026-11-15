package data

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type transactionLinkFixture struct {
	h                    *handler
	p                    c.Principal
	source, target, link c.CurrentDefinition
	definitions          map[string]c.CurrentDefinition
	authorize            func(c.AuthorizationRequest) c.Decision
}

func newTransactionLinkFixture(t *testing.T) *transactionLinkFixture {
	t.Helper()
	f := &transactionLinkFixture{p: c.Principal{TenantID: "tenant-a", Subject: "link-user"}, definitions: map[string]c.CurrentDefinition{}}
	source, target := referenceSchema(), referenceSchema()
	source.ID, target.ID = "ffffffff-ffff-4fff-8fff-ffffffffffff", "22222222-2222-4222-8222-222222222222"
	link := c.Definition{ID: "11111111-1111-4111-8111-111111111111", TenantID: f.p.TenantID, Kind: c.KindLink, Name: "主从关系", Link: &c.Link{SourceSchemaID: source.ID, TargetSchemaID: target.ID, Cardinality: "1:N", OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	f.source = c.CurrentDefinition{Definition: source, ETag: digest(source), Status: "active"}
	f.target = c.CurrentDefinition{Definition: target, ETag: digest(target), Status: "active"}
	f.link = c.CurrentDefinition{Definition: link, ETag: digest(link), Status: "active"}
	for _, current := range []c.CurrentDefinition{f.source, f.target, f.link} {
		f.definitions[current.Definition.ID] = current
	}
	f.authorize = func(c.AuthorizationRequest) c.Decision {
		return c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: "link-state"}
	}
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tenant-ID") != f.p.TenantID {
			w.WriteHeader(404)
			return
		}
		current, found := f.definitions[strings.TrimPrefix(r.URL.Path, "/metadata/definitions/")]
		if !found {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, current)
	}))
	t.Cleanup(meta.Close)
	f.h = &handler{cfg: Config{MetadataURL: meta.URL, Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, r c.AuthorizationRequest) (c.Decision, error) {
		return f.authorize(r), nil
	})}, client: meta.Client()}
	return f
}

func (f *transactionLinkFixture) body() c.WriteTransactionRequest {
	return c.WriteTransactionRequest{IdempotencyKey: "mixed-plan", Operations: []c.TransactionOperation{transactionCreate(f.source.Definition.ID, "master", "MASTER"), transactionCreate(f.target.Definition.ID, "detail", "DETAIL")}, Links: []c.TransactionLinkCreate{{LinkID: f.link.Definition.ID, ClientKey: "edge", Source: c.TransactionEndpoint{ClientKey: "master"}, Target: c.TransactionEndpoint{ClientKey: "detail"}}}}
}

func (f *transactionLinkFixture) prepare(body c.WriteTransactionRequest) ([]preparedTransactionLink, []c.CurrentDefinition, error) {
	ops, schemas, err := f.h.prepareWriteTransaction(context.Background(), f.p, body)
	if err != nil {
		return nil, nil, err
	}
	return f.h.prepareTransactionLinks(context.Background(), f.p, body, ops, schemas)
}

func TestTransactionLinksPrepareStableGateGroupsAndOriginalIntent(t *testing.T) {
	f := newTransactionLinkFixture(t)
	body := f.body()
	before := digest(body)
	links, schemas, err := f.prepare(body)
	if err != nil || len(links) != 1 || len(schemas) != 2 || schemas[0].Definition.ID != f.target.Definition.ID || schemas[1].Definition.ID != f.source.Definition.ID || digest(body) != before {
		t.Fatalf("mixed plan lost definitions, gate order or original intent: %v", err)
	}
	gates := transactionLinkGates(append(links, links...))
	if len(gates) != 1 || gates[0].Definition.ID != f.link.Definition.ID {
		t.Fatal("Link gates were not deduplicated separately from Schema gates")
	}
	if err := f.h.recheckTransactionLinks(context.Background(), f.p, links); err != nil {
		t.Fatal(err)
	}
	// Existing endpoints also support relation-only transactions, still locking
	// both endpoint Schemas even though neither appears in object operations.
	body.Operations = nil
	body.Links[0].Source = c.TransactionEndpoint{ID: strings.ToUpper(c.NewID())}
	body.Links[0].Target = c.TransactionEndpoint{ID: c.NewID()}
	links, schemas, err = f.prepare(body)
	if err != nil || len(schemas) != 2 || links[0].request.Source.ID != strings.ToLower(body.Links[0].Source.ID) {
		t.Fatalf("existing endpoint plan lost gate or ID normalization: %v", err)
	}
	if digest(body) == before {
		t.Fatal("fixture did not change the request")
	}
}

func TestTransactionLinksRejectInvalidEndpointsAndSharedClientKeys(t *testing.T) {
	for _, name := range []string{"unknown", "wrong_schema", "update", "delete", "both", "neither", "invalid_id", "shared_key", "duplicate_link_key", "invalid_link", "mapped_link", "link_row_policy", "context_source"} {
		t.Run(name, func(t *testing.T) {
			f := newTransactionLinkFixture(t)
			body := f.body()
			switch name {
			case "unknown":
				body.Links[0].Source.ClientKey = "missing"
			case "wrong_schema":
				body.Links[0].Source.ClientKey = "detail"
			case "update", "delete":
				body.Operations[0].Operation, body.Operations[0].ID, body.Operations[0].IfMatch = name, c.NewID(), `"current"`
				if name == "delete" {
					body.Operations[0].Values = nil
				}
			case "both":
				body.Links[0].Source.ID = c.NewID()
			case "neither":
				body.Links[0].Source = c.TransactionEndpoint{}
			case "invalid_id":
				body.Links[0].Source = c.TransactionEndpoint{ID: "not-a-uuid"}
			case "shared_key":
				body.Links[0].ClientKey = "master"
			case "duplicate_link_key":
				body.Links = append(body.Links, body.Links[0])
			case "invalid_link":
				body.Links[0].LinkID = "invalid"
			case "mapped_link":
				f.link.Definition.Link.StorageBinding = &c.LinkStorageBinding{Mode: "junction", DataSourceID: c.NewID(), Table: "edges", SourceColumn: "parent", TargetColumn: "child"}
				f.definitions[f.link.Definition.ID] = f.link
			case "link_row_policy", "context_source":
				base := f.authorize
				f.authorize = func(r c.AuthorizationRequest) c.Decision {
					d := base(r)
					if r.Operation == "link.create" && name == "link_row_policy" {
						d.RowFilter = &c.Expression{Op: "eq", Field: testFieldCode, Value: raw("private")}
					}
					if r.Operation == "instance.read" && name == "context_source" {
						d.ContextSchemaIDs = []string{f.source.Definition.ID}
					}
					return d
				}
			}
			_, _, err := f.prepare(body)
			var api *apiError
			if !errors.As(err, &api) || api.status != 422 {
				t.Fatalf("invalid mixed plan accepted: %v", err)
			}
		})
	}
}

func TestTransactionLinksRequireIndependentReadAndLinkAuthority(t *testing.T) {
	for _, deny := range []string{"link.create", "source.read", "target.read", "changed_state"} {
		t.Run(deny, func(t *testing.T) {
			f := newTransactionLinkFixture(t)
			base := f.authorize
			f.authorize = func(r c.AuthorizationRequest) c.Decision {
				d := base(r)
				if r.Operation == deny || r.Operation == "instance.read" && (deny == "source.read" && r.ResourceID == f.source.Definition.ID || deny == "target.read" && r.ResourceID == f.target.Definition.ID) {
					d.Allow = false
				}
				if deny == "changed_state" && r.Operation == "instance.read" {
					d.State = "different"
				}
				return d
			}
			body := f.body()
			if deny == "link.create" {
				body.Links[0].Source.ClientKey = "unknown"
			}
			_, _, err := f.prepare(body)
			var api *apiError
			want := 403
			if deny == "changed_state" {
				want = 409
			}
			if !errors.As(err, &api) || api.status != want {
				t.Fatalf("reference expanded authority or validation preceded link denial: %v", err)
			}
		})
	}
}

func TestTransactionLinksRecheckAuthorityAndCurrentDefinitions(t *testing.T) {
	for _, change := range []string{"link", "source", "target", "authorization"} {
		t.Run(change, func(t *testing.T) {
			f := newTransactionLinkFixture(t)
			links, _, err := f.prepare(f.body())
			if err != nil {
				t.Fatal(err)
			}
			if change == "authorization" {
				base := f.authorize
				f.authorize = func(r c.AuthorizationRequest) c.Decision {
					d := base(r)
					if r.Operation == "instance.read" {
						d.ReadFields = nil
					}
					return d
				}
			} else {
				id := map[string]string{"link": f.link.Definition.ID, "source": f.source.Definition.ID, "target": f.target.Definition.ID}[change]
				current := f.definitions[id]
				current.ETag = "changed"
				f.definitions[id] = current
			}
			var api *apiError
			err = f.h.recheckTransactionLinks(context.Background(), f.p, links)
			if !errors.As(err, &api) || api.status != 409 {
				t.Fatalf("precommit changed %s was accepted: %v", change, err)
			}
		})
	}
}

func TestTransactionEndpointResolvesActualResultIdentity(t *testing.T) {
	schemaID, actualID := c.NewID(), c.NewID()
	endpoint := c.TransactionEndpoint{ClientKey: "upsert"}
	results := map[string]c.Instance{"upsert": {SchemaID: schemaID, ID: actualID}}
	if id, err := resolveTransactionEndpoint(endpoint, schemaID, results); err != nil || id != actualID {
		t.Fatal("actual upsert identity lost")
	}
	if _, err := resolveTransactionEndpoint(endpoint, c.NewID(), results); err == nil {
		t.Fatal("cross-Schema result accepted")
	}
	if _, err := resolveTransactionEndpoint(endpoint, schemaID, nil); err == nil {
		t.Fatal("unresolved result accepted")
	}
}

func TestTransactionLinksRejectCorruptReceiptBeforeReadingRows(t *testing.T) {
	f := newTransactionLinkFixture(t)
	body := f.body()
	// Literal endpoints make this independent of regenerated create IDs.
	body.Operations = nil
	body.Links[0].Source = c.TransactionEndpoint{ID: c.NewID()}
	body.Links[0].Target = c.TransactionEndpoint{ID: c.NewID()}
	links, _, err := f.prepare(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, broken := range []string{"count", "client_key", "endpoint", "etag"} {
		source := c.Instance{SchemaID: f.source.Definition.ID, ID: body.Links[0].Source.ID}
		target := c.Instance{SchemaID: f.target.Definition.ID, ID: body.Links[0].Target.ID}
		out := c.LinkInstance{ID: c.NewID(), LinkID: f.link.Definition.ID, SourceID: source.ID, TargetID: target.ID, ETag: c.NewID()}
		saved := transactionSaved{Status: "committed", Links: []transactionSavedLink{{Result: c.TransactionLinkResult{ClientKey: "edge", Instance: out}, Source: source, Target: target}}}
		switch broken {
		case "count":
			saved.Links = nil
		case "client_key":
			saved.Links[0].Result.ClientKey = "other"
		case "endpoint":
			saved.Links[0].Result.Instance.SourceID = c.NewID()
		case "etag":
			saved.Links[0].Result.Instance.ETag = "invalid"
		}
		_, err := replayTransactionLinks(context.Background(), nil, f.p, links, saved)
		var api *apiError
		if !errors.As(err, &api) || api.code != "idempotency_corrupt" {
			t.Fatalf("corrupt %s receipt reached business reads: %v", broken, err)
		}
	}
}

func TestTransactionLinkHTTPRejectsUnsupportedCommandsAndCombinedBudget(t *testing.T) {
	h := NewHandler(Config{})
	p := c.Principal{TenantID: "tenant-a", Subject: "link-user"}
	for _, body := range []string{
		`{"idempotencyKey":"intent","operations":[],"links":[{"linkId":"id","clientKey":"edge","operation":"delete","source":{"id":"x"},"target":{"id":"y"}}]}`,
		`{"idempotencyKey":"intent","operations":[],"links":[{"linkId":"id","clientKey":"edge","source":{"clientKey":"parent","path":"values.private"},"target":{"id":"y"}}]}`,
	} {
		r := httptest.NewRequest("POST", "/data/transactions", strings.NewReader(body))
		r = r.WithContext(platform.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("unsupported Link command silently accepted: %d", w.Code)
		}
	}
	body := c.WriteTransactionRequest{IdempotencyKey: "combined-limit", Operations: make([]c.TransactionOperation, c.MaxBulkWriteItems), Links: []c.TransactionLinkCreate{{}}}
	_, err := (&handler{}).executeWriteTransaction(context.Background(), p, body, false)
	var api *apiError
	if !errors.As(err, &api) || api.code != "invalid_transaction" {
		t.Fatal("Link items bypassed transaction item budget")
	}
	r := httptest.NewRequest("GET", "/data/capabilities", nil)
	r = r.WithContext(platform.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var caps struct {
		Operations []string `json:"transactionLinkOperations"`
		Storage    string   `json:"transactionLinkStorage"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &caps) != nil || !reflect.DeepEqual(caps.Operations, []string{"create", "delete"}) || caps.Storage != "managed" {
		t.Fatal("Link transaction capability boundary missing")
	}
}

func TestTransactionLinkDeletesValidationAndAuthority(t *testing.T) {
	for _, scenario := range []string{"valid", "duplicate", "shared_key", "unquoted", "weak", "bad_id", "denied", "changed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionLinkFixture(t)
			body := c.WriteTransactionRequest{IdempotencyKey: "delete-edge", LinkDeletes: []c.TransactionLinkDelete{{LinkID: f.link.Definition.ID, ClientKey: "delete", ID: c.NewID(), IfMatch: `"` + c.NewID() + `"`}}}
			switch scenario {
			case "duplicate":
				body.LinkDeletes = append(body.LinkDeletes, body.LinkDeletes[0])
				body.LinkDeletes[1].ClientKey = "other"
			case "shared_key":
				body.Operations = []c.TransactionOperation{transactionCreate(f.source.Definition.ID, "delete", "CODE")}
			case "unquoted":
				body.LinkDeletes[0].IfMatch = c.NewID()
			case "weak":
				body.LinkDeletes[0].IfMatch = "W/" + body.LinkDeletes[0].IfMatch
			case "bad_id":
				body.LinkDeletes[0].ID = "invalid"
			case "denied":
				base := f.authorize
				f.authorize = func(r c.AuthorizationRequest) c.Decision {
					d := base(r)
					if r.Operation == "link.delete" {
						d.Allow = false
					}
					return d
				}
			}
			before := digest(body)
			links, schemas, err := f.prepare(body)
			if digest(body) != before {
				t.Fatal("preflight mutated retry intent")
			}
			if scenario == "valid" || scenario == "changed" {
				if err != nil || len(schemas) != 2 || len(links) != 1 || links[0].deletion == nil {
					t.Fatalf("delete-only plan invalid: %v", err)
				}
				if scenario == "changed" {
					base := f.authorize
					f.authorize = func(r c.AuthorizationRequest) c.Decision {
						d := base(r)
						if r.Operation == "link.delete" {
							d.State = "changed"
						}
						return d
					}
					if f.h.recheckTransactionLinks(context.Background(), f.p, links) == nil {
						t.Fatal("delete authority not rechecked")
					}
				}
			} else if err == nil {
				t.Fatal("invalid delete plan accepted")
			}
		})
	}
	f := newTransactionLinkFixture(t)
	body := c.WriteTransactionRequest{IdempotencyKey: "too-many", Operations: make([]c.TransactionOperation, 100), LinkDeletes: []c.TransactionLinkDelete{{}}}
	if _, err := f.h.executeWriteTransaction(context.Background(), f.p, body, false); err == nil {
		t.Fatal("deletes bypassed shared budget")
	}
}

func TestTransactionLinkDeleteReplayRejectsCorruptOrChangedAuthorization(t *testing.T) {
	for _, broken := range []string{"count", "id", "etag", "client", "endpoint", "authorization"} {
		t.Run(broken, func(t *testing.T) {
			f := newTransactionLinkFixture(t)
			deletion := c.TransactionLinkDelete{LinkID: f.link.Definition.ID, ClientKey: "remove", ID: c.NewID(), IfMatch: `"` + c.NewID() + `"`}
			links, _, err := f.prepare(c.WriteTransactionRequest{IdempotencyKey: "delete", LinkDeletes: []c.TransactionLinkDelete{deletion}})
			if err != nil {
				t.Fatal(err)
			}
			source, target := c.Instance{ID: c.NewID(), SchemaID: f.source.Definition.ID}, c.Instance{ID: c.NewID(), SchemaID: f.target.Definition.ID}
			saved := transactionSaved{Status: "committed", LinkDeletes: []transactionSavedLink{{Result: c.TransactionLinkResult{ClientKey: "remove", Instance: c.LinkInstance{ID: deletion.ID, ETag: bareETag(deletion.IfMatch), LinkID: deletion.LinkID, SourceID: source.ID, TargetID: target.ID}}, Source: source, Target: target, Authorization: transactionLinkAuthorization(links[0])}}}
			switch broken {
			case "count":
				saved.LinkDeletes = nil
			case "id":
				saved.LinkDeletes[0].Result.Instance.ID = c.NewID()
			case "etag":
				saved.LinkDeletes[0].Result.Instance.ETag = c.NewID()
			case "client":
				saved.LinkDeletes[0].Result.ClientKey = "other"
			case "endpoint":
				saved.LinkDeletes[0].Target.SchemaID = c.NewID()
			case "authorization":
				saved.LinkDeletes[0].Authorization["target"] = "changed"
			}
			_, err = replayTransactionLinkDeletes(context.Background(), nil, f.p, links, saved)
			var api *apiError
			want := "idempotency_corrupt"
			if broken == "authorization" {
				want = "authorization_changed"
			}
			if !errors.As(err, &api) || api.code != want {
				t.Fatalf("invalid deletion receipt reached business SQL: %v", err)
			}
		})
	}
}
