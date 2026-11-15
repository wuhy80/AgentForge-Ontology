package data

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func newTransactionLinkSQLFixture(t *testing.T, cardinality string) (*transactionFixture, c.CurrentDefinition) {
	t.Helper()
	f := newTransactionFixture(t)
	d := c.Definition{ID: c.NewID(), TenantID: f.p.TenantID, Kind: c.KindLink, Name: "事务关系", Link: &c.Link{SourceSchemaID: f.current.Definition.ID, TargetSchemaID: f.second.Definition.ID, ForwardName: "明细", ReverseName: "主记录", Cardinality: cardinality, OnDelete: "restrict", StorageBinding: &c.LinkStorageBinding{Mode: "managed"}}}
	current := c.CurrentDefinition{Definition: d, ETag: digest(d), Status: "active"}
	f.defs[d.ID] = current
	operationID := c.NewID()
	for _, step := range []struct {
		path string
		body any
	}{
		{"prepare", c.PrepareRequest{Definition: d, Digest: current.ETag, OperationID: operationID}},
		{"activate", c.ActivateRequest{DefinitionID: d.ID, Digest: current.ETag, OperationID: operationID}},
	} {
		r := httptest.NewRequest("POST", "/internal/publications/"+step.path, bytes.NewReader(mustMutationJSON(t, step.body)))
		r.Header.Set("Authorization", "Bearer "+f.core.cfg.ServiceToken)
		r.Header.Set("X-Tenant-ID", f.p.TenantID)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("Link %s failed: %d", step.path, w.Code)
		}
	}
	t.Cleanup(func() { _, _ = f.engine.DB().Exec("DROP TABLE IF EXISTS " + quote(linkTableName(d.ID))) })
	return f, current
}

func transactionLinkCounts(t *testing.T, f *transactionFixture, linkID string) [5]int {
	t.Helper()
	base := f.transactionCounts()
	var edges int
	if err := f.engine.DB().QueryRow("SELECT COUNT(*) FROM "+quote(linkTableName(linkID))+" WHERE tenant_id=?", f.p.TenantID).Scan(&edges); err != nil {
		t.Fatal("count transaction relations failed")
	}
	return [5]int{base[0], base[1], base[2], base[3], edges}
}

func mixedLinkRequest(f *transactionFixture, link c.CurrentDefinition) c.WriteTransactionRequest {
	return c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(f.current.Definition.ID, "master", "MASTER"), transactionCreate(f.second.Definition.ID, "detail", "DETAIL")}, Links: []c.TransactionLinkCreate{{LinkID: link.Definition.ID, ClientKey: "edge", Source: c.TransactionEndpoint{ClientKey: "master"}, Target: c.TransactionEndpoint{ClientKey: "detail"}}}}
}

func TestMySQLTransactionLinksCommitReplayAndRevocation(t *testing.T) {
	f, link := newTransactionLinkSQLFixture(t, "1:N")
	existing := f.create("EXISTING")
	body := mixedLinkRequest(f, link)
	body.Operations = append(body.Operations,
		c.TransactionOperation{SchemaID: f.current.Definition.ID, Operation: "upsert", ClientKey: "existing", Match: &c.WriteMatch{IndexID: f.current.Definition.Schema.Indexes[0].ID, Values: map[string]json.RawMessage{testFieldCode: raw("EXISTING")}}, IfMatch: `"` + existing.ETag + `"`, Values: map[string]json.RawMessage{testFieldDept: raw("D2")}},
		transactionCreate(f.second.Definition.ID, "other-detail", "OTHER_DETAIL"))
	body.Links = append(body.Links, c.TransactionLinkCreate{LinkID: link.Definition.ID, ClientKey: "other-edge", Source: c.TransactionEndpoint{ClientKey: "existing"}, Target: c.TransactionEndpoint{ClientKey: "other-detail"}})
	before := transactionLinkCounts(t, f, link.Definition.ID)
	result := f.transaction(body, false, 200)
	if !result.Committed || len(result.Results) != 4 || len(result.LinkResults) != 2 || result.LinkResults[0].Instance.SourceID != result.Results[0].ID || result.LinkResults[1].Instance.SourceID != existing.ID {
		t.Fatal("object and Link transaction lost actual endpoint identities")
	}
	for i, edge := range result.LinkResults {
		var sourceID, targetID string
		if err := f.engine.DB().QueryRow("SELECT source_id,target_id FROM "+quote(linkTableName(link.Definition.ID))+" WHERE tenant_id=? AND id=?", f.p.TenantID, edge.Instance.ID).Scan(&sourceID, &targetID); err != nil || sourceID != edge.Instance.SourceID || targetID != edge.Instance.TargetID || edge.ClientKey != body.Links[i].ClientKey || edge.Instance.ETag == "" {
			t.Fatal("committed Link does not match the result")
		}
	}
	after := transactionLinkCounts(t, f, link.Definition.ID)
	if after != [5]int{before[0] + 1, before[1] + 2, before[2] + 6, before[3] + 1, before[4] + 2} {
		t.Fatal("data, Link, outbox or receipt count incorrect")
	}
	for _, lookup := range []bool{true, false} {
		replayed := f.transaction(body, lookup, 200)
		if !replayed.Replayed || !replayed.Committed || digest(replayed.Results) != digest(result.Results) || digest(replayed.LinkResults) != digest(result.LinkResults) || transactionLinkCounts(t, f, link.Definition.ID) != after {
			t.Fatal("lost-response recovery reran objects or relations")
		}
	}
	changed := body
	changed.Links = append([]c.TransactionLinkCreate(nil), body.Links...)
	changed.Links[0].Target.ClientKey = "other-detail"
	f.transaction(changed, true, 409)
	f.transaction(changed, false, 409)
	// Current access revocation must preserve the committed receipt and return
	// no old relation identity, even when object writes remain allowed.
	f.mu.Lock()
	f.policy["link.create"] = c.Decision{Allow: false, State: "mutation-policy-1"}
	f.mu.Unlock()
	f.transaction(body, true, 403)
	f.mu.Lock()
	delete(f.policy, "link.create")
	f.mu.Unlock()
	if restored := f.transaction(body, true, 200); !restored.Committed || digest(restored.LinkResults) != digest(result.LinkResults) {
		t.Fatal("revocation replaced committed relation evidence")
	}
	other := f.p
	other.Subject = "another-user"
	if isolated, err := f.core.executeWriteTransaction(context.Background(), other, body, true); err != nil || isolated.Status != "not_found" || len(isolated.LinkResults) != 0 {
		t.Fatal("Link receipts leaked across subjects")
	}
}

func TestMySQLTransactionLinksRollbackOnEndpointAndCardinality(t *testing.T) {
	for _, failure := range []string{"missing_endpoint", "hidden_endpoint", "duplicate_pair", "cardinality", "precommit_guard"} {
		t.Run(failure, func(t *testing.T) {
			f, link := newTransactionLinkSQLFixture(t, "1:1")
			body := mixedLinkRequest(f, link)
			want := "not_found"
			switch failure {
			case "missing_endpoint":
				body.Links[0].Target = c.TransactionEndpoint{ID: c.NewID()}
			case "hidden_endpoint":
				f.mu.Lock()
				f.policy["instance.read"] = c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: "mutation-policy-1", RowFilter: &c.Expression{Op: "eq", Field: testFieldDept, Value: raw("HIDDEN")}}
				f.mu.Unlock()
			case "duplicate_pair":
				second := body.Links[0]
				second.ClientKey = "duplicate"
				body.Links = append(body.Links, second)
				want = "constraint_conflict"
			case "cardinality":
				body.Operations = append(body.Operations, transactionCreate(f.second.Definition.ID, "other-detail", "OTHER_DETAIL"))
				second := body.Links[0]
				second.ClientKey, second.Target.ClientKey = "other-edge", "other-detail"
				body.Links = append(body.Links, second)
				want = "constraint_conflict"
			case "precommit_guard":
				want = "session_revoked"
			}
			before := transactionLinkCounts(t, f, link.Definition.ID)
			var failed c.WriteTransactionResult
			if failure == "precommit_guard" {
				calls := 0
				var err error
				failed, err = f.core.executeGuardedWriteTransaction(context.Background(), f.p, body, false, func(context.Context) error {
					calls++
					if calls > 1 {
						return problem(401, "session_revoked", "会话已撤销")
					}
					return nil
				})
				if err != nil || calls != 2 {
					t.Fatal("mixed transaction did not reach commit guard")
				}
			} else {
				failed = f.transaction(body, false, 200)
			}
			if failed.Committed || failed.Status != "failed" || failed.Error == nil || failed.Error.Code != want || len(failed.Results) != 0 || len(failed.LinkResults) != 0 {
				t.Fatal("failed mixed transaction exposed partial success")
			}
			after := transactionLinkCounts(t, f, link.Definition.ID)
			if after != [5]int{before[0], before[1], before[2], before[3] + 1, before[4]} {
				t.Fatal("failed mixed transaction retained objects, relations or outbox")
			}
			for _, lookup := range []bool{true, false} {
				replayed := f.transaction(body, lookup, 200)
				if !replayed.Replayed || replayed.Status != "failed" || len(replayed.LinkResults) != 0 || transactionLinkCounts(t, f, link.Definition.ID) != after {
					t.Fatal("failed relation recovery changed business data")
				}
			}
		})
	}
}

func TestMySQLTransactionLinksReplayChecksHistoricalAndCurrentRows(t *testing.T) {
	f, link := newTransactionLinkSQLFixture(t, "1:N")
	body := mixedLinkRequest(f, link)
	result := f.transaction(body, false, 200)
	if !result.Committed {
		t.Fatal("mixed transaction did not commit")
	}
	// Move both endpoints into D2 using the public single-schema transaction,
	// leaving historical D1 values in the relation receipt.
	for i, schema := range []string{f.current.Definition.ID, f.second.Definition.ID} {
		update := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{{SchemaID: schema, Operation: "update", ClientKey: "change", ID: result.Results[i].ID, IfMatch: `"` + result.Results[i].ETag + `"`, Values: map[string]json.RawMessage{testFieldDept: raw("D2")}}}}
		if !f.transaction(update, false, 200).Committed {
			t.Fatal("endpoint update failed")
		}
	}
	for _, visible := range []string{"D1", "D2"} {
		f.mu.Lock()
		f.policy["instance.read"] = c.Decision{Allow: true, ReadFields: []string{"*"}, WriteFields: []string{"*"}, State: "mutation-policy-1", RowFilter: &c.Expression{Op: "eq", Field: testFieldDept, Value: raw(visible)}}
		f.mu.Unlock()
		status := 403 // D2 rejects the historical D1 snapshot.
		if visible == "D1" {
			status = 404
		} // D1 rejects the current D2 endpoint.
		f.transaction(body, true, status)
	}
	f.mu.Lock()
	delete(f.policy, "instance.read")
	f.mu.Unlock()
	if restored := f.transaction(body, true, 200); !restored.Committed || digest(restored.LinkResults) != digest(result.LinkResults) {
		t.Fatal("row denial changed original Link receipt")
	}
}
