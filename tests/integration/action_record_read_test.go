package integration_test

import (
	"encoding/json"
	"fmt"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func verifyActionRecordReadHTTP(t *testing.T, admin, other *executionHTTP, principal c.User, executionURL, dataURL string,
	publish func(c.Definition) c.CurrentDefinition, savePolicy func([]c.PermissionRule), base []c.PermissionRule, role string,
) {
	model := publish(c.Definition{Kind: c.KindSchema, Name: "执行读取验收记录", Schema: &c.Schema{
		Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{
			{Name: "数值", DataType: "int64", Required: true},
			{Name: "可见", DataType: "boolean", Required: true},
		},
	}})
	value, visible := model.Definition.Schema.Properties[0].ID, model.Definition.Schema.Properties[1].ID
	instances := dataURL + "/data/schemas/" + model.Definition.ID + "/instances"
	create := func(show bool) c.Instance {
		var row c.Instance
		admin.request("POST", instances, c.WriteRequest{
			IdempotencyKey: c.NewID(), Values: map[string]json.RawMessage{value: raw("9007199254740993"), visible: raw(show)},
		}, nil, 201, &row)
		return row
	}
	row, hidden := create(true), create(false)
	var rules []c.PermissionRule
	for _, rule := range base {
		if rule.RoleID == role && rule.Operation == "instance.read" {
			continue
		}
		rules = append(rules, rule)
	}
	read := c.PermissionRule{ID: c.NewID(), RoleID: role, Effect: "allow", Operation: "instance.read", ResourceID: model.Definition.ID,
		ReadFields: []string{value}, RowFilter: &c.Expression{Op: "eq", Field: visible, Value: raw(true)}}
	setRead := func(rule c.PermissionRule) { savePolicy(append(append([]c.PermissionRule{}, rules...), rule)) }
	setRead(read)
	path := executionURL + "/execution/invocations"
	for _, runtime := range []struct{ language, entry, source string }{
		{"python", "main", fmt.Sprintf("def main(inputs):\n    return {'结果': inputs['当前记录'][%q]}\n", value)},
		{"javascript", "main", fmt.Sprintf("function main(inputs) { return {结果: inputs['当前记录'][%q]}; }", value)},
		{"go", "Main", fmt.Sprintf("package main\nfunc Main(inputs map[string]any) (map[string]any, error) { return map[string]any{\"结果\": inputs[\"当前记录\"].(map[string]any)[%q]}, nil }", value)},
	} {
		action := publish(c.Definition{Kind: c.KindAction, Name: runtime.language + "读取记录", Action: &c.Callable{
			SideEffects: "none", TargetSchemaIDs: []string{model.Definition.ID},
			Input:      []c.Property{{ID: "anchor", Name: "实例", DataType: "uuid", Required: true}},
			Output:     []c.Property{{Name: "结果", DataType: "int64", Required: true}},
			RecordRead: &c.ActionRecordRead{SchemaID: model.Definition.ID, InstanceInputID: "anchor", FieldIDs: []string{value}},
			Runtime:    &c.ScriptRuntime{Language: runtime.language, Entrypoint: runtime.entry, Source: runtime.source, TimeoutSeconds: 30},
		}})
		intent := func(id string) c.InvocationRequest {
			return c.InvocationRequest{DefinitionID: action.Definition.ID, Input: map[string]json.RawMessage{"anchor": raw(id)}}
		}
		headers := map[string]string{"Idempotency-Key": c.NewID()}
		var result, replay c.Invocation
		admin.request("POST", path, intent(row.ID), headers, 201, &result)
		if result.Status != "succeeded" || string(result.Output[action.Definition.Action.Output[0].ID]) != `"9007199254740993"` {
			t.Fatalf("%s did not read exact current record: %+v", runtime.language, result)
		}
		admin.request("POST", path, intent(row.ID), headers, 200, &replay)
		if replay.ID != result.ID {
			t.Fatalf("%s replay changed invocation ID", runtime.language)
		}
		resultURL := path + "/" + result.ID
		other.request("GET", resultURL, nil, nil, 404, nil)
		other.request("POST", path, intent(row.ID), map[string]string{"Idempotency-Key": c.NewID()}, 404, nil)
		admin.request("POST", path, intent(hidden.ID), map[string]string{"Idempotency-Key": c.NewID()}, 404, nil)
		withoutField := read
		withoutField.ReadFields = []string{visible}
		setRead(withoutField)
		admin.request("POST", path, intent(row.ID), map[string]string{"Idempotency-Key": c.NewID()}, 403, nil)
		admin.request("GET", resultURL, nil, nil, 403, nil)
		setRead(read)
		var changed, reverted c.Instance
		admin.request("PATCH", instances+"/"+row.ID, c.WriteRequest{Values: map[string]json.RawMessage{value: raw("9007199254740994")}},
			map[string]string{"If-Match": `"` + row.ETag + `"`}, 200, &changed)
		admin.request("GET", resultURL, nil, nil, 409, nil)
		admin.request("POST", path, intent(row.ID), headers, 409, nil)
		admin.request("PATCH", instances+"/"+row.ID, c.WriteRequest{Values: map[string]json.RawMessage{value: raw("9007199254740993")}},
			map[string]string{"If-Match": `"` + changed.ETag + `"`}, 200, &reverted)
		row = reverted
		t.Logf("%s current-record invocation, authorization, replay and stale-result checks passed", runtime.language)
	}
	writer := publish(c.Definition{Kind: c.KindAction, Name: "读取后更新记录", Action: &c.Callable{
		SideEffects: "dataWrite", TargetSchemaIDs: []string{model.Definition.ID},
		DataWrite:  []c.DataWriteCapability{{SchemaID: model.Definition.ID, Operations: []string{"update"}}},
		Input:      []c.Property{{ID: "anchor", Name: "实例", DataType: "uuid", Required: true}},
		Output:     []c.Property{{Name: "结果", DataType: "int64", Required: true}},
		RecordRead: &c.ActionRecordRead{SchemaID: model.Definition.ID, InstanceInputID: "anchor", FieldIDs: []string{value}},
		Runtime: &c.ScriptRuntime{Language: "python", Entrypoint: "main", TimeoutSeconds: 30, Source: fmt.Sprintf(
			"def main(inputs, ctx):\n    v = str(int(inputs['当前记录'][%q]) + 1)\n    ctx.data.update(%q, inputs['实例'], {%q: v}, '\"current-record\"', 'update')\n    return {'结果': v}\n",
			value, model.Definition.ID, value)},
	}})
	intent := c.InvocationRequest{DefinitionID: writer.Definition.ID, Input: map[string]json.RawMessage{"anchor": raw(row.ID)}}
	var session c.SessionCheck
	for _, cookie := range admin.cookies {
		if cookie.Name == platform.SessionCookieName {
			session = c.SessionCheck{Token: cookie.Value, Method: "POST", CSRFToken: admin.csrf, Origin: executionOrigin}
		}
	}
	var snapshot c.ActionRecordSnapshot
	internalHeaders := map[string]string{"Authorization": "Bearer " + serviceToken}
	admin.request("POST", dataURL+"/internal/execution/current-record", c.ActionRecordReadRequest{
		Session: session, ActionID: writer.Definition.ID, DefinitionDigest: writer.ETag, InstanceID: row.ID,
		TenantID: principal.TenantID, SubjectID: principal.ID,
	}, internalHeaders, 200, &snapshot)
	headers := map[string]string{"Idempotency-Key": c.NewID()}
	var written, replay c.Invocation
	admin.request("POST", path, intent, headers, 201, &written)
	if written.Status != "succeeded" || written.Transaction == nil || !written.Transaction.Committed ||
		string(written.Output[writer.Definition.Action.Output[0].ID]) != `"9007199254740994"` {
		t.Fatalf("read and write did not commit: %+v", written)
	}
	admin.request("POST", path, intent, headers, 200, &replay)
	if replay.ID != written.ID {
		t.Fatal("read and write replay ran the Action again")
	}
	var stored c.Instance
	admin.request("GET", instances+"/"+row.ID, nil, nil, 200, &stored)
	if stored.ETag == row.ETag || string(stored.Values[value]) != `"9007199254740994"` {
		t.Fatalf("record update missing or repeated: %+v", stored)
	}
	invocationID := c.NewID()
	var stale c.WriteTransactionResult
	admin.request("POST", dataURL+"/internal/execution/transactions", c.ExecutionTransactionRequest{
		Session: session, InvocationID: invocationID, DefinitionID: writer.Definition.ID, ImplementationID: writer.Definition.ID,
		DefinitionDigest: writer.ETag, ImplementationDigest: writer.ETag,
		RecordRead: &c.ExecutionRecordWriteGuard{InstanceID: row.ID, ETag: snapshot.ETag, Authorization: snapshot.Authorization},
		Transaction: c.WriteTransactionRequest{IdempotencyKey: "execution:" + invocationID, Operations: []c.TransactionOperation{{
			Operation: "update", SchemaID: model.Definition.ID, ID: row.ID, ClientKey: "stale",
			IfMatch: `"` + snapshot.ETag + `"`, Values: map[string]json.RawMessage{value: raw("0")},
		}}},
	}, internalHeaders, 200, &stale)
	if stale.Committed || stale.Status != "failed" || stale.Error == nil || stale.Error.Code != "precondition_failed" {
		t.Fatalf("stale record guard did not terminate safely: %+v", stale)
	}
	var unaffected c.Instance
	admin.request("GET", instances+"/"+row.ID, nil, nil, 200, &unaffected)
	if unaffected.ETag != stored.ETag || string(unaffected.Values[value]) != string(stored.Values[value]) {
		t.Fatal("stale transaction modified the record")
	}
	savePolicy(base)
}
