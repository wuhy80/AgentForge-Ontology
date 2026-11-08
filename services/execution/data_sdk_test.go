package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func sdkInput() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"schema": json.RawMessage(`"10000000-0000-4000-8000-000000000001"`),
		"field":  json.RawMessage(`"10000000-0000-4000-8000-000000000002"`),
		"id":     json.RawMessage(`"10000000-0000-4000-8000-000000000003"`),
		"index":  json.RawMessage(`"10000000-0000-4000-8000-000000000004"`),
		"etag":   json.RawMessage(`"\"complete-row-state\""`),
		"amount": json.RawMessage(`"9007199254740993.1000"`),
	}
}

var sdkSources = map[string]string{
	"python": `def main(inputs, ctx):
    s, f, id, tag = inputs["schema"], inputs["field"], inputs["id"], inputs["etag"]
    values, d = {f: inputs["amount"]}, ctx.data
    assert d.create(s, values, "create") is None
    d.update(s, id, values, tag, "update")
    d.delete(s, id, tag, "delete")
    d.upsert(s, {"id": id}, values, tag, "upsert")
    d.bulk_create(s, [{"clientKey": "bulk-create", "values": values}])
    d.bulk_update(s, [{"clientKey": "bulk-update", "id": id, "values": values, "ifMatch": tag}])
    d.bulk_delete(s, [{"clientKey": "bulk-delete", "id": id, "ifMatch": tag}])
    d.bulk_upsert(s, [{"clientKey": "bulk-upsert", "match": {"indexId": inputs["index"], "values": values}, "values": values}], "overwrite")
    values[f] = "changed after staging"
    return {"精确值": inputs["amount"], "暂存": True}
`,
	"javascript": `function main(inputs,ctx) {
  const s=inputs.schema,f=inputs.field,id=inputs.id,tag=inputs.etag,d=ctx.data,values={[f]:inputs.amount};
  if (d.create(s,values,'create') !== undefined) throw new Error('SDK returned a record');
  d.update(s,id,values,tag,'update');
  d.delete(s,id,tag,'delete');
  d.upsert(s,{id},values,tag,'upsert');
  d.bulk_create(s,[{clientKey:'bulk-create',values}]);
  d.bulk_update(s,[{clientKey:'bulk-update',id,values,ifMatch:tag}]);
  d.bulk_delete(s,[{clientKey:'bulk-delete',id,ifMatch:tag}]);
  d.bulk_upsert(s,[{clientKey:'bulk-upsert',match:{indexId:inputs.index,values},values}],'overwrite');
  values[f]='changed after staging';
  return {精确值:inputs.amount,暂存:true};
}`,
	"go": `package main
func Main(inputs map[string]any,ctx *OntologyContext) map[string]any {
  s,f,id,tag:=inputs["schema"].(string),inputs["field"].(string),inputs["id"].(string),inputs["etag"].(string)
  values:=map[string]any{f:inputs["amount"]};d:=ctx.Data
  d.Create(s,values,"create")
  d.Update(s,id,values,tag,"update")
  d.Delete(s,id,tag,"delete")
  d.Upsert(s,OntologyMatch{ID:id},values,tag,"upsert","check")
  d.BulkCreate(s,[]OntologyWriteItem{{ClientKey:"bulk-create",Values:values}})
  d.BulkUpdate(s,[]OntologyWriteItem{{ClientKey:"bulk-update",ID:id,Values:values,IfMatch:tag}})
  d.BulkDelete(s,[]OntologyWriteItem{{ClientKey:"bulk-delete",ID:id,IfMatch:tag}})
  d.BulkUpsert(s,[]OntologyWriteItem{{ClientKey:"bulk-upsert",Match:&OntologyMatch{IndexID:inputs["index"].(string),Values:values},Values:values}},"overwrite")
  values[f]="changed after staging"
  return map[string]any{"精确值":inputs["amount"],"暂存":true}
}`,
}

func sdkRuntime(language, source string) *c.ScriptRuntime {
	entrypoint := "main"
	if language == "go" {
		entrypoint = "Main"
	}
	return &c.ScriptRuntime{Language: language, Entrypoint: entrypoint, Source: source, TimeoutSeconds: 30}
}

func TestDockerDataSDKStagesEightOperationsAndPreservesExactValues(t *testing.T) {
	for _, language := range supportedRuntimes {
		t.Run(language, func(t *testing.T) {
			runner, image := dockerLanguage(t, language)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			result, err := runner.RunWithData(ctx, sdkRuntime(language, sdkSources[language]), sdkInput(), image)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Operations) != 8 || string(result.Output["精确值"]) != `"9007199254740993.1000"` || string(result.Output["暂存"]) != "true" {
				t.Fatalf("SDK lost complete plan or exact scalar output: %s", mustJSON(result))
			}
			for i, item := range result.Operations {
				if item.Operation != []string{"create", "update", "delete", "upsert"}[i%4] || item.SchemaID != "10000000-0000-4000-8000-000000000001" {
					t.Fatalf("SDK changed operation order or stable schema identity: %s", mustJSON(item))
				}
				if item.Operation != "delete" && string(item.Values["10000000-0000-4000-8000-000000000002"]) != `"9007199254740993.1000"` {
					t.Fatalf("SDK retained a mutable input map or rounded a precise value: %s", mustJSON(item))
				}
				if item.Operation == "update" || item.Operation == "delete" {
					if item.IfMatch != `"complete-row-state"` {
						t.Fatal("SDK lost the quoted strong If-Match credential")
					}
				}
			}
			if result.Operations[7].ConflictPolicy != "overwrite" || result.Operations[7].Match.IndexID == "" {
				t.Fatal("bulk upsert lost explicit unique match or conflict policy")
			}
		})
	}
}

func sdkFailureSource(language, failure string) string {
	if language == "python" {
		body := map[string]string{
			"caught_invalid":  "try:\n        ctx.data.update(s, inputs['id'], values, 'unquoted', 'bad')\n    except Exception:\n        pass",
			"caught_binding":  "try:\n        ctx.data.create()\n    except Exception:\n        pass",
			"caught_bad_bulk": "try:\n        ctx.data.bulk_create(s, [{'clientKey':'bulk-first','values':values}, {'clientKey':'bulk-bad','values':values,'id':inputs['id']}])\n    except Exception:\n        pass",
			"caught_read":     "try:\n        ctx.data.read(s)\n    except Exception:\n        pass",
			"caught_overflow": "try:\n        for n in range(101):\n            ctx.data.create(s, values, 'item-'+str(n))\n    except Exception:\n        pass",
			"user_exception":  "raise RuntimeError('SYNTHETIC_PRIVATE')",
			"invalid_output":  "return {'bad': []}",
			"timeout":         "while True:\n        pass",
		}[failure]
		return "def main(inputs, ctx):\n    s, f = inputs['schema'], inputs['field']\n    values = {f: inputs['amount']}\n    ctx.data.create(s, values, 'first')\n    " + body + "\n    return {}\n"
	}
	if language == "javascript" {
		body := map[string]string{
			"caught_invalid":  "try {ctx.data.update(s,inputs.id,values,'unquoted','bad')} catch {}",
			"caught_bad_bulk": "try {ctx.data.bulk_create(s,[{clientKey:'bulk-first',values},{clientKey:'bulk-bad',values,id:inputs.id}])} catch {}",
			"caught_read":     "try {ctx.data.read(s)} catch {}",
			"caught_overflow": "try {for(let n=0;n<101;n++) ctx.data.create(s,values,'item-'+n)} catch {}",
			"user_exception":  "throw new Error('SYNTHETIC_PRIVATE');",
			"invalid_output":  "return {bad:[]};",
			"timeout":         "while(true){}",
		}[failure]
		return "function main(inputs,ctx){const s=inputs.schema,values={[inputs.field]:inputs.amount};ctx.data.create(s,values,'first');" + body + "return {}; }"
	}
	body := map[string]string{
		"caught_invalid":  `func(){defer func(){recover()}();ctx.Data.Update(s,inputs["id"].(string),values,"unquoted","bad")}()`,
		"caught_bad_bulk": `func(){defer func(){recover()}();ctx.Data.BulkCreate(s,[]OntologyWriteItem{{ClientKey:"bulk-first",Values:values},{ClientKey:"bulk-bad",ID:inputs["id"].(string),Values:values}})}()`,
		"caught_overflow": `func(){defer func(){recover()}();for n:=0;n<101;n++ {ctx.Data.Create(s,values,fmt.Sprint("item-",n))}}()`,
		"user_exception":  `panic("SYNTHETIC_PRIVATE")`,
		"invalid_output":  `return map[string]any{"bad":[]any{}}`,
		"timeout":         `for {}`,
	}[failure]
	imports := ""
	if failure == "caught_overflow" {
		imports = "import \"fmt\"\n"
	}
	return "package main\n" + imports + "func Main(inputs map[string]any,ctx *OntologyContext) map[string]any {s:=inputs[\"schema\"].(string);values:=map[string]any{inputs[\"field\"].(string):inputs[\"amount\"]};ctx.Data.Create(s,values,\"first\");" + body + ";return map[string]any{}}"
}

func TestDockerDataSDKDiscardsEveryPlanOnFailure(t *testing.T) {
	for _, language := range supportedRuntimes {
		t.Run(language, func(t *testing.T) {
			runner, image := dockerLanguage(t, language)
			failures := []string{"caught_invalid", "caught_bad_bulk", "caught_overflow", "user_exception", "invalid_output", "timeout"}
			if language != "go" {
				failures = append(failures, "caught_read")
			}
			if language == "python" {
				failures = append(failures, "caught_binding")
			}
			for _, failure := range failures {
				t.Run(failure, func(t *testing.T) {
					timeout := 35 * time.Second
					if failure == "timeout" {
						timeout = 2 * time.Second
					}
					ctx, cancel := context.WithTimeout(context.Background(), timeout)
					defer cancel()
					result, err := runner.RunWithData(ctx, sdkRuntime(language, sdkFailureSource(language, failure)), sdkInput(), image)
					if err == nil || result.Output != nil || len(result.Operations) != 0 || strings.Contains(fmt.Sprint(err), "SYNTHETIC_PRIVATE") {
						t.Fatalf("failed invocation returned a partial plan or private diagnostic: %s %v", mustJSON(result), err)
					}
				})
			}
		})
	}
}

func TestDockerDataSDKAcceptsExactlyOneHundredFlatItems(t *testing.T) {
	sources := map[string]string{
		"python":     "def main(inputs,ctx):\n    ctx.data.bulk_create(inputs['schema'], [{'clientKey':'item'+str(n),'values':{inputs['field']:inputs['amount']}} for n in range(100)])\n    return {}\n",
		"javascript": "function main(inputs,ctx){ctx.data.bulk_create(inputs.schema,Array.from({length:100},(_,n)=>({clientKey:'item'+n,values:{[inputs.field]:inputs.amount}})));return {};}",
		"go":         "package main\nimport \"fmt\"\nfunc Main(inputs map[string]any,ctx *OntologyContext)map[string]any{items:=make([]OntologyWriteItem,100);for n:=range items{items[n]=OntologyWriteItem{ClientKey:fmt.Sprint(\"item\",n),Values:map[string]any{inputs[\"field\"].(string):inputs[\"amount\"]}}};ctx.Data.BulkCreate(inputs[\"schema\"].(string),items);return map[string]any{}}",
	}
	for _, language := range supportedRuntimes {
		t.Run(language, func(t *testing.T) {
			runner, image := dockerLanguage(t, language)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			result, err := runner.RunWithData(ctx, sdkRuntime(language, sources[language]), sdkInput(), image)
			if err != nil || len(result.Operations) != 100 || result.Operations[99].ClientKey != "item99" {
				t.Fatalf("SDK rejected the supported flat budget boundary: %d %v", len(result.Operations), err)
			}
		})
	}
}

func TestDockerDataSDKJavaScriptDoesNotExposeHostConstructors(t *testing.T) {
	runner, image := dockerLanguage(t, "javascript")
	for _, expression := range []string{"ctx.constructor.constructor('return process')()", "ctx.data.create.constructor('return process')()", "ctx.data.bulk_create.constructor('return process')()"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		result, err := runner.RunWithData(ctx, sdkRuntime("javascript", "function main(inputs,ctx){return {escaped:typeof ("+expression+")}}"), sdkInput(), image)
		cancel()
		if err == nil || result.Output != nil || len(result.Operations) != 0 {
			t.Fatal("SDK exposed a host Function constructor")
		}
	}
}

func TestDockerDataSDKGoCompatibility(t *testing.T) {
	runner, image := dockerLanguage(t, "go")
	for _, source := range []string{
		`package main
func Main(inputs map[string]any)(map[string]any,error){return map[string]any{"ok":true},nil}`,
		`package main
func Main(inputs map[string]any,ctx *OntologyContext)(map[string]any,error){return map[string]any{"ok":true},nil}`,
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		result, err := runner.RunWithData(ctx, sdkRuntime("go", source), sdkInput(), image)
		cancel()
		if err != nil || string(result.Output["ok"]) != "true" || len(result.Operations) != 0 {
			t.Fatalf("Go compatible entrypoint failed: %s %v", mustJSON(result), err)
		}
	}
}

func TestDataRunnerRejectsMalformedPrivateEnvelope(t *testing.T) {
	for _, raw := range []string{`{}`, `{"output":{},"operations":null}`, `{"output":null,"operations":[]}`, `{"output":{"nested":{}},"operations":[]}`, `{"output":{},"operations":[],"extra":true}`, `{"output":{},"operations":[{"unexpected":"value"}]}`} {
		runner := &DockerRunner{Isolation: "trusted", command: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
			output := ""
			if args[0] == "start" {
				output = raw
			}
			return helperCommand(ctx, 0, output)
		}}
		result, err := runner.RunWithData(context.Background(), sdkRuntime("python", "def main(inputs,ctx): return {}"), nil, "sha256:"+strings.Repeat("a", 64))
		if err == nil || result.Output != nil || len(result.Operations) != 0 {
			t.Fatalf("malformed runner envelope was accepted: %s", raw)
		}
	}
}
