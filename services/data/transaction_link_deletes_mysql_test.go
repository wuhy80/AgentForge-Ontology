package data

import (
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"testing"
)

func deleteTransactionEdge(edge c.TransactionLinkResult) c.TransactionLinkDelete {
	return c.TransactionLinkDelete{LinkID: edge.Instance.LinkID, ClientKey: "old-edge", ID: edge.Instance.ID, IfMatch: `"` + edge.Instance.ETag + `"`}
}

func TestMySQLTransactionReplaceOneToOneAndRecover(t *testing.T) {
	f, link := newTransactionLinkSQLFixture(t, "1:1")
	original := f.transaction(mixedLinkRequest(f, link), false, 200)
	body := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{transactionCreate(f.second.Definition.ID, "replacement", "REPLACEMENT")}, LinkDeletes: []c.TransactionLinkDelete{deleteTransactionEdge(original.LinkResults[0])}, Links: []c.TransactionLinkCreate{{LinkID: link.Definition.ID, ClientKey: "new-edge", Source: c.TransactionEndpoint{ID: original.Results[0].ID}, Target: c.TransactionEndpoint{ClientKey: "replacement"}}}}
	before := transactionLinkCounts(t, f, link.Definition.ID)
	out := f.transaction(body, false, 200)
	if !out.Committed || len(out.LinkDeleteResults) != 1 || len(out.LinkResults) != 1 {
		t.Fatal("replacement did not commit atomically")
	}
	after := transactionLinkCounts(t, f, link.Definition.ID)
	if after != [5]int{before[0], before[1] + 1, before[2] + 3, before[3] + 1, before[4]} {
		t.Fatal("replacement counts invalid")
	}
	for _, lookup := range []bool{true, false} {
		replay := f.transaction(body, lookup, 200)
		if !replay.Replayed || !replay.Committed || digest(replay.LinkDeleteResults) != digest(out.LinkDeleteResults) || transactionLinkCounts(t, f, link.Definition.ID) != after {
			t.Fatal("recovery reran replacement")
		}
	}
}

func TestMySQLTransactionDeleteRelationAndEndpointRecovery(t *testing.T) {
	f, link := newTransactionLinkSQLFixture(t, "1:N")
	original := f.transaction(mixedLinkRequest(f, link), false, 200)
	child := original.Results[1]
	body := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), LinkDeletes: []c.TransactionLinkDelete{deleteTransactionEdge(original.LinkResults[0])}, Operations: []c.TransactionOperation{{SchemaID: f.second.Definition.ID, ClientKey: "child", Operation: "delete", ID: child.ID, IfMatch: `"` + child.ETag + `"`}}}
	out := f.transaction(body, false, 200)
	if !out.Committed || out.Results[0].Outcome != "deleted" {
		t.Fatal("edge removal did not precede restrict endpoint delete")
	}
	if replay := f.transaction(body, true, 200); !replay.Committed || !replay.Replayed {
		t.Fatal("deleted endpoint prevented safe recovery")
	}
	f.mu.Lock()
	f.policy["link.delete"] = c.Decision{Allow: false, State: "changed"}
	f.mu.Unlock()
	f.transaction(body, true, 403)
}

func TestMySQLTransactionLinkDeleteRollbackAndStaleETag(t *testing.T) {
	for _, scenario := range []string{"constraint", "etag", "row_denied"} {
		t.Run(scenario, func(t *testing.T) {
			f, link := newTransactionLinkSQLFixture(t, "1:N")
			original := f.transaction(mixedLinkRequest(f, link), false, 200)
			body := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), LinkDeletes: []c.TransactionLinkDelete{deleteTransactionEdge(original.LinkResults[0])}}
			switch scenario {
			case "constraint":
				body.Operations = []c.TransactionOperation{transactionCreate(f.current.Definition.ID, "duplicate", "MASTER")}
			case "etag":
				body.LinkDeletes[0].IfMatch = `"` + c.NewID() + `"`
			case "row_denied":
				f.mu.Lock()
				f.policy["instance.read"] = c.Decision{Allow: true, State: "mutation-policy-1", ReadFields: []string{"*"}, RowFilter: &c.Expression{Op: "eq", Field: testFieldCode, Value: raw("INVISIBLE")}}
				f.mu.Unlock()
			}
			before := transactionLinkCounts(t, f, link.Definition.ID)
			out := f.transaction(body, false, 200)
			after := transactionLinkCounts(t, f, link.Definition.ID)
			if out.Status != "failed" || out.Committed || len(out.LinkDeleteResults) != 0 || after != [5]int{before[0], before[1], before[2], before[3] + 1, before[4]} {
				t.Fatal("failed deletion leaked writes or outbox")
			}
		})
	}
}
