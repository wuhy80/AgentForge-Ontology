package integration_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sort"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/services/execution"
)

type lostTransactionResponse struct {
	inner execution.DataTransactionClient
	lost  bool
}

func (l *lostTransactionResponse) Execute(ctx context.Context, request c.ExecutionTransactionRequest) (c.WriteTransactionResult, error) {
	result, err := l.inner.Execute(ctx, request)
	if err == nil && result.Committed && !request.Lookup && !l.lost {
		l.lost = true
		return c.WriteTransactionResult{}, errors.New("synthetic response loss after actual Data commit")
	}
	return result, err
}

func verifyTransactionRecoveryHTTP(t *testing.T, admin *executionHTTP, cfg execution.Config, roots *x509.CertPool, dataURL string, publish func(c.Definition) c.CurrentDefinition) {
	t.Helper()
	schema := publish(c.Definition{Kind: c.KindSchema, Name: "事务恢复对象", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{Name: "代码", DataType: "string", Required: true}}}}).Definition
	field := schema.Schema.Properties[0].ID
	fn := publish(c.Definition{Kind: c.KindFunction, Name: "丢失响应事务恢复", Function: &c.Callable{SideEffects: "dataWrite", DataWrite: []c.DataWriteCapability{{SchemaID: schema.ID, Operations: []string{"create"}}}, Output: []c.Property{{Name: "完成", DataType: "boolean", Required: true}}, Runtime: &c.ScriptRuntime{Language: "python", Entrypoint: "main", TimeoutSeconds: 10, Source: fmt.Sprintf("def main(inputs,ctx):\n    ctx.data.create(%q,{%q:'only-once'},'once')\n    return {'完成':True}\n", schema.ID, field)}}}).Definition
	loss := &lostTransactionResponse{inner: cfg.DataTransactions}
	cfg.DataTransactions = loss
	start := func() *httptest.Server {
		server := httptest.NewTLSServer(platform.ProtectSessionRoutes(serviceToken, false, cfg.SessionChecker, execution.NewHandler(cfg)))
		roots.AddCert(server.Certificate())
		return server
	}
	first := start()
	intent := c.InvocationRequest{DefinitionID: fn.ID, Input: map[string]json.RawMessage{}}
	key := c.NewID()
	var pending c.Invocation
	admin.request("POST", first.URL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": key}, 202, &pending)
	first.Close()
	if pending.Status != "committing" || len(pending.Output) != 0 || !loss.lost {
		t.Fatal("actual commit response loss not preserved")
	}
	// New handler, same SQL store, no in-memory invocation state survives.
	second := start()
	defer second.Close()
	var result c.Invocation
	admin.request("GET", second.URL+"/execution/invocations/"+pending.ID, nil, nil, 200, &result)
	if result.Status != "succeeded" || result.Transaction == nil || !result.Transaction.Committed || result.ID != pending.ID {
		t.Fatal("durable transaction did not recover after restart")
	}
	admin.request("POST", second.URL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": key}, 200, &result)
	var page c.QueryResult
	admin.request("POST", dataURL+"/data/query", c.QuerySpec{From: c.Source{SchemaID: schema.ID}, Select: []string{field}, Page: c.Page{Limit: 10}}, nil, 200, &page)
	if len(page.Data) != 1 {
		t.Fatal("recovery duplicated a committed write")
	}
}

// Runs within the isolated three-service, real OPA/session/container fixture.
// Verification uses public reads; no private database access supplies authority.
func verifyDataWriteTransactionsHTTP(t *testing.T, admin, other *executionHTTP, execURL, dataURL string, publish func(c.Definition) c.CurrentDefinition) {
	t.Helper()
	schemas := []c.CurrentDefinition{}
	for _, name := range []string{"事务来源甲", "事务来源乙"} {
		field := c.NewID()
		schemas = append(schemas, publish(c.Definition{Kind: c.KindSchema, Name: name, Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{ID: field, Name: "代码", DataType: "string", Required: true}}, Indexes: []c.Index{{ID: c.NewID(), Name: "代码唯一", Unique: true, FieldIDs: []string{field}}}}}))
	}
	sort.Slice(schemas, func(i, j int) bool { return schemas[i].Definition.ID < schemas[j].Definition.ID })
	a, b := schemas[0].Definition, schemas[1].Definition
	af, bf := a.Schema.Properties[0].ID, b.Schema.Properties[0].ID
	values := func(field, code string) map[string]json.RawMessage {
		return map[string]json.RawMessage{field: raw(code)}
	}
	var seed c.Instance
	admin.request("POST", dataURL+"/data/schemas/"+b.ID+"/instances", c.WriteRequest{Values: values(bf, "collision"), IdempotencyKey: c.NewID()}, nil, 201, &seed)
	count := func(schema c.Definition, field, code string) int {
		var page c.QueryResult
		admin.request("POST", dataURL+"/data/query", c.QuerySpec{From: c.Source{SchemaID: schema.ID}, Select: []string{field}, Where: &c.Expression{Op: "eq", Field: field, Value: raw(code)}, Page: c.Page{Limit: 10}}, nil, 200, &page)
		return len(page.Data)
	}
	// Public mixed transaction and status share the same receipt and rights.
	direct := c.WriteTransactionRequest{IdempotencyKey: c.NewID(), Operations: []c.TransactionOperation{{SchemaID: a.ID, Operation: "create", ClientKey: "a", Values: values(af, "direct")}, {SchemaID: b.ID, Operation: "create", ClientKey: "b", Values: values(bf, "direct")}}}
	var transaction c.WriteTransactionResult
	admin.request("POST", dataURL+"/data/transactions", direct, nil, 200, &transaction)
	if !transaction.Committed || len(transaction.Results) != 2 {
		t.Fatal("public transaction did not commit")
	}
	admin.request("POST", dataURL+"/data/transactions/status", direct, nil, 200, &transaction)
	if !transaction.Replayed || !transaction.Committed {
		t.Fatal("public transaction receipt not recovered")
	}
	admin.request("POST", dataURL+"/data/transactions", direct, map[string]string{"X-CSRF-Token": ""}, 403, nil)
	other.request("POST", dataURL+"/data/transactions", direct, nil, 200, &transaction)
	if transaction.Committed || transaction.Status != "failed" || transaction.Error == nil || transaction.Error.Code != "not_found" {
		t.Fatal("foreign schema transaction was not denied")
	}
	if count(a, af, "direct") != 1 || count(b, bf, "direct") != 1 {
		t.Fatal("public transaction duplicated writes")
	}

	for _, language := range []string{"python", "javascript", "go"} {
		entry, source := "main", ""
		switch language {
		case "python":
			source = fmt.Sprintf("def main(inputs, ctx):\n    ctx.data.create(%q, {%q: inputs['代码']}, 'a')\n    ctx.data.create(%q, {%q: 'collision' if inputs['碰撞'] else inputs['代码']}, 'b')\n    return {'完成': True}\n", a.ID, af, b.ID, bf)
		case "javascript":
			source = fmt.Sprintf("function main(inputs, ctx) { ctx.data.create(%q, {[%q]: inputs['代码']}, 'a'); ctx.data.create(%q, {[%q]: inputs['碰撞'] ? 'collision' : inputs['代码']}, 'b'); return {完成: true}; }", a.ID, af, b.ID, bf)
		case "go":
			entry = "Main"
			source = fmt.Sprintf("package main\nfunc Main(inputs map[string]any, ctx *OntologyContext) map[string]any { ctx.Data.Create(%q,map[string]any{%q:inputs[\"代码\"]},\"a\");code:=inputs[\"代码\"];if inputs[\"碰撞\"]==true{code=\"collision\"};ctx.Data.Create(%q,map[string]any{%q:code},\"b\");return map[string]any{\"完成\":true} }", a.ID, af, b.ID, bf)
		}
		newCallable := func() *c.Callable {
			return &c.Callable{SideEffects: "dataWrite", DataWrite: []c.DataWriteCapability{{SchemaID: a.ID, Operations: []string{"create"}}, {SchemaID: b.ID, Operations: []string{"create"}}}, Input: []c.Property{{Name: "代码", DataType: "string", Required: true}, {Name: "碰撞", DataType: "boolean", Required: true}}, Output: []c.Property{{Name: "完成", DataType: "boolean", Required: true}}, Runtime: &c.ScriptRuntime{Language: language, Source: source, Entrypoint: entry, TimeoutSeconds: 30}}
		}
		fn := publish(c.Definition{Kind: c.KindFunction, Name: language + "事务函数", Function: newCallable()})
		inline := publish(c.Definition{Kind: c.KindAction, Name: language + "事务动作", Action: newCallable()})
		boundCall := newCallable()
		boundCall.Runtime = nil
		boundCall.FunctionID = fn.Definition.ID
		bound := publish(c.Definition{Kind: c.KindAction, Name: language + "事务绑定动作", Action: boundCall})
		for _, d := range []c.Definition{fn.Definition, inline.Definition, bound.Definition} {
			call := d.Function
			if d.Kind == c.KindAction {
				call = d.Action
			}
			for _, fail := range []bool{false, true} {
				code := language + "-" + d.ID + fmt.Sprint(fail)
				intent := c.InvocationRequest{DefinitionID: d.ID, Input: map[string]json.RawMessage{call.Input[0].ID: raw(code), call.Input[1].ID: raw(fail)}}
				key := c.NewID()
				var invocation c.Invocation
				admin.request("POST", execURL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": key}, 201, &invocation)
				if invocation.Transaction == nil || invocation.Transaction.OperationCount != 2 {
					t.Fatal("missing transaction evidence")
				}
				if fail {
					if invocation.Status != "failed" || invocation.Transaction.Committed || count(a, af, code) != 0 || count(b, bf, code) != 0 {
						t.Fatal("second schema failure left partial writes")
					}
				} else if invocation.Status != "succeeded" || !invocation.Transaction.Committed || count(a, af, code) != 1 || count(b, bf, code) != 1 {
					t.Fatal("callable transaction failed")
				}
				var replay c.Invocation
				admin.request("POST", execURL+"/execution/invocations", intent, map[string]string{"Idempotency-Key": key}, 200, &replay)
				if replay.ID != invocation.ID || replay.Status != invocation.Status {
					t.Fatal("callable transaction replay changed execution")
				}
				if !fail && count(a, af, code) != 1 {
					t.Fatal("callable replay repeated a write")
				}
			}
		}
	}
}
