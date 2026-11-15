package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

const testReferenceField = "1b74bd75-135a-4f2f-8f10-7ac08918ef13"

func referenceSchema() c.Definition {
	d := testDefinition()
	d.Schema.Properties = append(d.Schema.Properties, c.Property{ID: testReferenceField, Name: "主记录", DataType: "uuid", Nullable: true})
	return d
}

func referenceItem() c.TransactionOperation {
	return c.TransactionOperation{SchemaID: testDefinition().ID, Operation: "create", ClientKey: "detail", ValueRefs: map[string]c.TransactionResultReference{testReferenceField: {ClientKey: "master"}}}
}

func TestTransactionReferenceValidation(t *testing.T) {
	for _, name := range []string{"valid", "unknown", "forward", "self", "update_source", "delete_source", "empty_source", "control_source", "name", "non_uuid", "literal", "literal_name", "null_literal", "match_key", "delete_target"} {
		t.Run(name, func(t *testing.T) {
			d := referenceSchema()
			item := referenceItem()
			prior := map[string]string{"master": "create"}
			switch name {
			case "unknown", "forward":
				delete(prior, "master")
			case "self":
				item.ValueRefs[testReferenceField] = c.TransactionResultReference{ClientKey: "detail"}
			case "update_source", "delete_source":
				prior["master"] = strings.TrimSuffix(name, "_source")
			case "empty_source":
				item.ValueRefs[testReferenceField] = c.TransactionResultReference{}
			case "control_source":
				item.ValueRefs[testReferenceField] = c.TransactionResultReference{ClientKey: "bad\nkey"}
			case "name":
				item.ValueRefs = map[string]c.TransactionResultReference{"主记录": {ClientKey: "master"}}
			case "non_uuid":
				item.ValueRefs = map[string]c.TransactionResultReference{testFieldCode: {ClientKey: "master"}}
			case "literal", "null_literal":
				item.Values = map[string]json.RawMessage{testReferenceField: raw(c.NewID())}
				if name == "null_literal" {
					item.Values[testReferenceField] = raw(nil)
				}
			case "literal_name":
				item.Values = map[string]json.RawMessage{"主记录": raw(c.NewID())}
			case "match_key":
				item.Operation = "upsert"
				item.Match = &c.WriteMatch{Values: map[string]json.RawMessage{testReferenceField: raw(c.NewID())}}
			case "delete_target":
				item.Operation = "delete"
			}
			values, err := transactionReferenceValues(d.Schema, item, prior)
			if name == "valid" {
				if err != nil || len(values) != 1 || len(item.Values) != 0 {
					t.Fatal("reference-only values were rejected or input was mutated")
				}
				return
			}
			var api *apiError
			if !errors.As(err, &api) || api.status != 422 || api.code != "invalid_transaction_reference" {
				t.Fatalf("invalid reference accepted: %v", err)
			}
		})
	}
}

// Mock Metadata and authorization over HTTP; no database or live session is used.
func TestTransactionReferencesPreparationAndDependencyOrder(t *testing.T) {
	d := referenceSchema()
	denied := false
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.CurrentDefinition{Definition: d, Status: "active", ETag: digest(d)})
	}))
	defer meta.Close()
	h := &handler{cfg: Config{MetadataURL: meta.URL, Authorizer: authorizerFunc(func(context.Context, c.Principal, c.AuthorizationRequest) (c.Decision, error) {
		return c.Decision{Allow: !denied, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: "reference-policy"}, nil
	})}, client: meta.Client()}
	p := c.Principal{TenantID: d.TenantID, Subject: "reference-user"}
	body := c.WriteTransactionRequest{IdempotencyKey: "reference-plan", Operations: []c.TransactionOperation{transactionCreate(d.ID, "master", "MASTER"), referenceItem()}}
	before := mustMutationJSON(t, body)
	ops, _, err := h.prepareWriteTransaction(context.Background(), p, body)
	if err != nil || len(ops) != 2 || !bytes.Equal(before, mustMutationJSON(t, body)) {
		t.Fatalf("valid reference preparation failed or changed original intent: %v", err)
	}
	// Make the child sort before its source by Schema ID. Both execution and
	// replay must still process the source first, with responses in input order.
	ops[0].current.Definition.ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	ops[1].current.Definition.ID = "00000000-0000-4000-8000-000000000000"
	order, err := transactionOperationOrder(ops)
	if err != nil || !reflect.DeepEqual(order, []int{0, 1}) {
		t.Fatal("Schema sorting violated the reference dependency")
	}
	for _, operation := range []string{"update", "upsert"} {
		child := referenceItem()
		child.Operation, child.IfMatch = operation, `"current"`
		if operation == "update" {
			child.ID = c.NewID()
		} else {
			child.Match = &c.WriteMatch{ID: c.NewID()}
		}
		candidate := c.WriteTransactionRequest{Operations: []c.TransactionOperation{body.Operations[0], child}}
		if _, _, err = h.prepareWriteTransaction(context.Background(), p, candidate); err != nil {
			t.Fatalf("reference-only %s rejected: %v", operation, err)
		}
	}
	// Cycles/forward refs cannot be admitted even though both clientKeys exist.
	body.Operations[0].ValueRefs = map[string]c.TransactionResultReference{testReferenceField: {ClientKey: "detail"}}
	if _, _, err = h.prepareWriteTransaction(context.Background(), p, body); err == nil {
		t.Fatal("forward/cyclic reference admitted")
	}
	denied = true
	_, _, err = h.prepareWriteTransaction(context.Background(), p, body)
	var api *apiError
	if !errors.As(err, &api) || api.status != 403 {
		t.Fatal("reference validation disclosed fields before authorization")
	}
	// A broken internal dependency must fail promptly, never spin until timeout.
	ops[0].valueRefs = map[string]c.TransactionResultReference{testReferenceField: {ClientKey: "detail"}}
	if _, err = transactionOperationOrder(ops); err == nil {
		t.Fatal("broken internal dependency was silently reordered")
	}
	ops[0].valueRefs, ops[1].valueRefs = nil, nil
	if order, err = transactionOperationOrder(ops); err != nil || !reflect.DeepEqual(order, []int{1, 0}) {
		t.Fatal("unreferenced transactions lost their existing target order")
	}
}

func TestTransactionReferencesResolveActualUpsertIDAndRespectWriteFields(t *testing.T) {
	d := referenceSchema()
	item := referenceItem()
	item.Values = mutationValues("DETAIL")
	item.Values[testFieldPrice] = raw("900719925474099.123")
	values, err := transactionReferenceValues(d.Schema, item, map[string]string{"master": "upsert"})
	if err != nil {
		t.Fatal(err)
	}
	prepared := preparedTransactionOperation{current: c.CurrentDefinition{Definition: d}, command: mutationCommand{Values: values}, valueRefs: item.ValueRefs}
	actualID := c.NewID()
	completed := map[string]mutationSavedItem{"master": {ClientKey: "master", Outcome: "updated", Instance: c.Instance{ID: actualID}}}
	command, err := resolveTransactionReferences(prepared, completed)
	if err != nil || string(command.Values[testReferenceField]) != string(raw(actualID)) || string(command.Values[testFieldPrice]) != `"900719925474099.123"` {
		t.Fatal("resolution lost actual upsert identity or decimal precision")
	}
	if string(prepared.command.Values[testReferenceField]) == string(raw(actualID)) || len(item.Values) != 3 {
		t.Fatal("resolution changed the prepared or original request")
	}
	if _, err = buildValues(d.Schema, command.Values, nil, []string{"*"}, true); err != nil {
		t.Fatalf("resolved UUID did not pass ordinary field validation: %v", err)
	}
	if _, err = buildValues(d.Schema, command.Values, nil, []string{testFieldCode, testFieldDept, testFieldPrice}, true); err == nil {
		t.Fatal("reference bypassed writable field restrictions")
	}
	d.Schema.Properties[len(d.Schema.Properties)-1].ReadOnly = true
	if _, err = buildValues(d.Schema, command.Values, nil, []string{"*"}, true); err == nil {
		t.Fatal("reference bypassed read-only property protection")
	}
	for _, invalid := range []map[string]mutationSavedItem{nil, {"master": {Outcome: "deleted", Instance: c.Instance{ID: actualID}}}, {"master": {Outcome: "created", Instance: c.Instance{ID: "invalid"}}}} {
		if _, err = resolveTransactionReferences(prepared, invalid); err == nil {
			t.Fatal("unresolved, deleted or malformed source was used")
		}
	}
}

func TestTransactionReferenceOrderHandlesFullBudgetAndFanout(t *testing.T) {
	operations := make([]preparedTransactionOperation, c.MaxBulkWriteItems)
	for i := range operations {
		operations[i].current.Definition.ID = fmt.Sprintf("%08x-0000-4000-8000-000000000000", len(operations)-i)
		operations[i].command.ClientKey = fmt.Sprintf("item-%d", i)
		if i > 0 {
			// Every item depends on the first; the last half also forms a chain.
			operations[i].valueRefs = map[string]c.TransactionResultReference{testReferenceField: {ClientKey: "item-0"}}
			if i > len(operations)/2 {
				operations[i].valueRefs[testFieldCode] = c.TransactionResultReference{ClientKey: operations[i-1].command.ClientKey}
			}
		}
	}
	order, err := transactionOperationOrder(operations)
	if err != nil || len(order) != len(operations) || order[0] != 0 {
		t.Fatal("full-budget reference plan could not be ordered")
	}
	positions := map[string]int{}
	for position, index := range order {
		for _, ref := range operations[index].valueRefs {
			if source, done := positions[ref.ClientKey]; !done || source >= position {
				t.Fatal("fanout or chained reference ran before its source")
			}
		}
		positions[operations[index].command.ClientKey] = position
	}
}

func TestTransactionReferenceHTTPContractAndCapabilities(t *testing.T) {
	h := NewHandler(Config{})
	p := c.Principal{TenantID: "tenant", Subject: "reference-user"}
	r := httptest.NewRequest("GET", "/data/capabilities", nil)
	r = r.WithContext(platform.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var capabilities struct {
		Enabled bool     `json:"transactionValueRefs"`
		Types   []string `json:"transactionValueRefTargetTypes"`
		Sources []string `json:"transactionValueRefSourceOperations"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &capabilities) != nil || !capabilities.Enabled || !reflect.DeepEqual(capabilities.Types, []string{"uuid"}) || !reflect.DeepEqual(capabilities.Sources, []string{"create", "upsert"}) {
		t.Fatal("reference capability boundary missing")
	}
	// Arbitrary selectors cannot be smuggled into the typed reference protocol.
	body := `{"idempotencyKey":"invalid","operations":[{"schemaId":"schema","operation":"create","clientKey":"detail","valueRefs":{"field":{"clientKey":"master","path":"values.private"}}}]}`
	r = httptest.NewRequest("POST", "/data/transactions", strings.NewReader(body))
	r = r.WithContext(platform.WithPrincipal(r.Context(), p))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("unknown reference selector accepted: %d", w.Code)
	}
}
