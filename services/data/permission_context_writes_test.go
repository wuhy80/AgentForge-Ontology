package data

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestWriteContextStorageAndSourceIntersections(t *testing.T) {
	current := c.CurrentDefinition{Definition: testDefinition()}
	other := c.NewID()
	for _, tc := range []struct {
		name    string
		sources []string
		targets map[string]bool
		mapped  bool
		code    string
	}{
		{"same schema", []string{current.Definition.ID}, map[string]bool{current.Definition.ID: true}, false, "context_source_mutation"},
		{"another transaction target", []string{other}, map[string]bool{current.Definition.ID: true, other: true}, false, "context_source_mutation"},
		{"separate platform source", []string{other}, map[string]bool{current.Definition.ID: true}, false, ""},
		{"object source with mapped write", []string{other}, map[string]bool{current.Definition.ID: true}, true, "context_write_unsupported"},
		{"user context with mapped write", nil, map[string]bool{current.Definition.ID: true}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := current
			schema := *current.Definition.Schema
			copy.Definition.Schema = &schema
			if tc.mapped {
				copy.Definition.Schema.StorageBinding = &c.StorageBinding{DataSourceID: other}
			}
			err := validateWriteContext(copy, []c.Decision{{Allow: true, ContextSchemaIDs: tc.sources}}, tc.targets)
			if tc.code == "" {
				if err != nil {
					t.Fatal("safe write context rejected")
				}
				return
			}
			var api *apiError
			if !errors.As(err, &api) || api.status != 422 || api.code != tc.code {
				t.Fatalf("unexpected context gate error: %v", err)
			}
		})
	}
}

func TestQuerySpecsRejectPolicyOnlyValueReferences(t *testing.T) {
	principal := c.Principal{TenantID: testDefinition().TenantID, Subject: "reader"}
	secret := strings.Repeat("s", 32)
	for _, shape := range []string{"reference", "reference-and-value", "is-null", "not", "boolean-node"} {
		t.Run(shape, func(t *testing.T) {
			ref := &c.PermissionValueRef{Source: "user", Attribute: "id"}
			leaf := c.Expression{Op: "eq", Field: testFieldCode, ValueRef: ref}
			switch shape {
			case "reference-and-value":
				leaf.Value = raw("fixed")
			case "is-null":
				leaf.Op = "isNull"
			case "not":
				leaf = c.Expression{Op: "not", Args: []c.Expression{leaf}}
			case "boolean-node":
				leaf = c.Expression{Op: "and", ValueRef: ref, Args: []c.Expression{{Op: "eq", Field: testFieldCode, Value: raw("fixed")}}}
			}
			query := baseQuery()
			query.Where = &leaf
			encoded, _ := json.Marshal(query)
			var decoded c.QuerySpec
			if json.Unmarshal(encoded, &decoded) != nil {
				t.Fatal("cannot decode actual query DTO")
			}
			if _, err := compileQuery(testDefinition(), "current", principal, allowed(), decoded, secret); err == nil {
				t.Fatal("ordinary query accepted a policy-only reference")
			}
			mapped, tables := mappedFixture()
			if _, err := compileMappedQuery(mapped, tables, "current", principal, allowed(), decoded, secret, ""); err == nil {
				t.Fatal("mapped query accepted a policy-only reference")
			}
			plan, rows := testRowQuery()
			var qualify func(c.Expression) c.Expression
			qualify = func(e c.Expression) c.Expression {
				if e.Field != "" {
					e.Field = "n1." + e.Field
				}
				for i := range e.Args {
					e.Args[i] = qualify(e.Args[i])
				}
				return e
			}
			qualified := qualify(leaf)
			rows.Where = &qualified
			if _, err := compileRowQuery(plan, principal, rows, secret); err == nil {
				t.Fatal("row query accepted or silently dropped a policy-only reference")
			}
		})
	}
}

func permissionContextDecision(source string) c.Decision {
	return c.Decision{Allow: true, State: "mutation-policy-1", ReadFields: []string{"*"}, WriteFields: []string{"*"}, ContextSchemaIDs: []string{source}, ContextDigest: strings.Repeat("d", 64)}
}

func cleanupPermissionMutationFixture(t *testing.T, f *mutationFixture, extra ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, id := range append([]string{f.current.Definition.ID}, extra...) {
			_, _ = f.engine.DB().Exec("DROP TABLE " + quote(tableName(id)))
		}
		for _, table := range []string{"af_data_gates", "af_data_idempotency", "af_data_outbox", "af_data_write_receipts", "af_data_transaction_receipts"} {
			_, _ = f.engine.DB().Exec("DELETE FROM "+quote(table)+" WHERE tenant_id=?", f.p.TenantID)
		}
	})
}

func TestMySQLWriteContextSingleBulkAndCapabilityGates(t *testing.T) {
	f := newMutationFixture(t)
	cleanupPermissionMutationFixture(t, f)
	base := f.create("BASE")
	before := f.counts()
	for _, name := range writePermissionNames {
		f.policy[name] = permissionContextDecision(f.current.Definition.ID)
	}
	for _, tc := range []struct {
		method, path string
		body         any
		etag         string
	}{
		{"POST", "/instances", c.WriteRequest{Values: mutationValues("CREATE"), IdempotencyKey: c.NewID()}, ""},
		{"PATCH", "/instances/" + base.ID, c.WriteRequest{Values: mutationValues("UPDATE")}, base.ETag},
		{"DELETE", "/instances/" + base.ID, nil, base.ETag},
		{"POST", "/instances/upsert", c.UpsertRequest{Match: c.WriteMatch{ID: c.NewID()}, Values: mutationValues("UPSERT"), IdempotencyKey: c.NewID()}, ""},
		{"POST", "/instances/bulk-create", c.BulkWriteRequest{IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{{ClientKey: "create", Values: mutationValues("BULK")}}}, ""},
		{"POST", "/instances/bulk-update", c.BulkWriteRequest{IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{{ClientKey: "update", ID: base.ID, IfMatch: `"` + base.ETag + `"`, Values: mutationValues("BULK")}}}, ""},
		{"POST", "/instances/bulk-delete", c.BulkWriteRequest{IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{{ClientKey: "delete", ID: base.ID, IfMatch: `"` + base.ETag + `"`}}}, ""},
		{"POST", "/instances/bulk-upsert", c.BulkWriteRequest{IdempotencyKey: c.NewID(), Items: []c.BulkWriteItem{{ClientKey: "upsert", Match: &c.WriteMatch{ID: c.NewID()}, Values: mutationValues("BULK")}}}, ""},
	} {
		w := f.call(tc.method, tc.path, tc.body, tc.etag, 422)
		failure := mutationDecode[c.Error](t, w)
		if failure.Code != "context_source_mutation" || f.counts() != before {
			t.Fatal("overlapping context write was not rejected before data or event mutation")
		}
	}
	caps := mutationDecode[c.WriteCapabilities](t, f.call("GET", "/write-capabilities", nil, "", 200))
	for _, operation := range caps.Operations {
		if operation.Allowed || operation.Reason != "context_write_unsupported" {
			t.Fatal("capability discovery advertised an overlapping context write")
		}
	}
	if caps.OverwriteAllowed || len(caps.MatchModes) != 0 || len(caps.UniqueKeys) != 0 {
		t.Fatal("upsert options remained usable despite denied context")
	}
	for _, name := range writePermissionNames {
		f.policy[name] = permissionContextDecision(c.NewID())
	}
	caps = mutationDecode[c.WriteCapabilities](t, f.call("GET", "/write-capabilities", nil, "", 200))
	for _, operation := range caps.Operations {
		if !operation.Allowed {
			t.Fatal("separate platform context source disabled a safe operation")
		}
	}
	f.create("SEPARATE")
	if got := f.counts(); got[0] != before[0]+1 || got[1] != before[1]+1 {
		t.Fatal("safe platform context write did not commit")
	}
}

func TestMySQLWriteContextOverwriteDiscoveryIsIndependent(t *testing.T) {
	f := newMutationFixture(t)
	cleanupPermissionMutationFixture(t, f)
	f.policy["instance.upsert.overwrite"] = permissionContextDecision(f.current.Definition.ID)
	caps := mutationDecode[c.WriteCapabilities](t, f.call("GET", "/write-capabilities", nil, "", 200))
	if caps.OverwriteAllowed || len(caps.OverwriteWriteFields) != 0 || len(caps.OverwriteUniqueKeyIDs) != 0 {
		t.Fatal("overwrite-only source intersection was advertised as available")
	}
	for _, operation := range caps.Operations {
		if !operation.Allowed {
			t.Fatal("overwrite context incorrectly disabled ordinary check operations")
		}
	}
	body := c.UpsertRequest{Match: c.WriteMatch{ID: c.NewID()}, Values: mutationValues("SAFE"), IdempotencyKey: c.NewID()}
	f.call("POST", "/instances/upsert", body, "", 201)
	body.ConflictPolicy, body.IdempotencyKey = "overwrite", c.NewID()
	before := f.counts()
	f.call("POST", "/instances/upsert", body, "", 422)
	if f.counts() != before {
		t.Fatal("rejected overwrite changed data or events")
	}
}

func TestMySQLWriteContextAcrossTransactionTargets(t *testing.T) {
	f := newTransactionFixture(t)
	cleanupPermissionMutationFixture(t, f.mutationFixture, f.second.Definition.ID)
	base := f.create("BASE")
	f.core.cfg.Authorizer = authorizerFunc(func(_ context.Context, _ c.Principal, request c.AuthorizationRequest) (c.Decision, error) {
		decision := c.Decision{Allow: true, State: "mutation-policy-1", ReadFields: []string{"*"}, WriteFields: []string{"*"}}
		if request.ResourceID == f.current.Definition.ID {
			decision = permissionContextDecision(f.second.Definition.ID)
		}
		return decision, nil
	})
	f.handler = NewHandler(f.core.cfg)
	body := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{
		{SchemaID: f.current.Definition.ID, Operation: "update", ClientKey: "update", ID: base.ID, IfMatch: `"` + base.ETag + `"`, Values: map[string]json.RawMessage{testFieldDept: raw("D2")}},
		transactionCreate(f.second.Definition.ID, "source-write", "SOURCE"),
	}}
	before := f.transactionCounts()
	for _, reverse := range []bool{false, true} {
		if reverse {
			body.Operations[0], body.Operations[1] = body.Operations[1], body.Operations[0]
			body.IdempotencyKey = c.NewID()
		}
		result := f.transaction(body, false, 200)
		if result.Committed || result.Status != "failed" || result.Error == nil || result.Error.Code != "context_source_mutation" {
			t.Fatal("cross-model source mutation was not durably rejected")
		}
		after := f.transactionCounts()
		if after[0] != before[0] || after[1] != before[1] || after[2] != before[2] {
			t.Fatal("cross-model context failure left business writes or outbox events")
		}
	}
	read := mutationDecode[c.Instance](t, f.call("GET", "/instances/"+base.ID, nil, "", 200))
	if read.ETag != base.ETag || string(read.Values[testFieldDept]) != `"D1"` {
		t.Fatal("rejected context transaction altered existing record")
	}
}

func TestMappedWriteContextRejectedBeforeConnectingSource(t *testing.T) {
	d, _ := mappedFixture()
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.CurrentDefinition{Definition: d, Status: "active", ETag: digest(d)})
	}))
	defer meta.Close()
	h := &handler{client: meta.Client(), cfg: Config{MetadataURL: meta.URL, ServiceToken: strings.Repeat("s", 32), Authorizer: authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		return permissionContextDecision(c.NewID()), nil
	})}}
	_, _, err := h.prepareMutation(context.Background(), c.Principal{TenantID: d.TenantID, Subject: "user"}, d.ID, "create", "check")
	var api *apiError
	if !errors.As(err, &api) || api.code != "context_write_unsupported" || api.status != 422 {
		t.Fatal("mapped object context write reached storage instead of being explicitly rejected")
	}
}

func TestFormDiscoveryRespectsOperationContextGates(t *testing.T) {
	d := testDefinition()
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.CurrentDefinition{Definition: d, Status: "active", ETag: digest(d)})
	}))
	defer meta.Close()
	createSource := d.ID
	app := NewHandler(Config{MetadataURL: meta.URL, ServiceToken: strings.Repeat("s", 32), Authorizer: authorizerFunc(func(_ context.Context, _ c.Principal, req c.AuthorizationRequest) (c.Decision, error) {
		if req.Operation == "instance.create" {
			return permissionContextDecision(createSource), nil
		}
		return c.Decision{Allow: true, State: "mutation-policy-1", ReadFields: []string{"*"}, WriteFields: []string{"*"}}, nil
	})})
	for _, operation := range []string{"create", "patch"} {
		w := callOntologySQL(t, app, "GET", "/data/schemas/"+d.ID+"/form?operation="+operation, nil)
		var form c.FormDefinition
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &form) != nil || form.CreateAllowed || !form.PatchAllowed {
			t.Fatal("form discovery advertised an unsafe create or hid a safe patch")
		}
		for _, field := range form.Definition.Schema.Properties {
			if field.ReadOnly != (operation == "create") {
				t.Fatal("form field writability ignored the selected operation's context")
			}
		}
	}
	createSource = c.NewID()
	w := callOntologySQL(t, app, "GET", "/data/schemas/"+d.ID+"/form?operation=create", nil)
	var form c.FormDefinition
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &form) != nil || !form.CreateAllowed {
		t.Fatal("form did not restore safe nonintersecting source writes")
	}
}
