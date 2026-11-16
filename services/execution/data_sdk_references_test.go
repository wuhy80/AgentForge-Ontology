package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

var sdkReferenceSources = map[string]string{
	"python": `def main(inputs, ctx):
    d, s, f, r, id, tag = ctx.data, inputs['schema'], inputs['field'], inputs['ref'], inputs['id'], inputs['etag']
    d.create(s, {f: inputs['amount']}, 'master')
    d.upsert(s, {'id': id}, {f: inputs['amount']}, tag, 'existing')
    refs = {r: {'clientKey': 'master'}}
    d.create(s, {f: inputs['amount']}, 'child', refs)
    refs[r]['clientKey'] = 'changed-after-staging'
    d.update(s, id, {}, tag, 'updated-child', {r: {'clientKey': 'existing'}})
    d.upsert(s, {'indexId': inputs['index'], 'values': {f: 'DETAIL'}}, {}, '', 'upserted-child', value_refs={r: {'clientKey': 'master'}})
    d.bulk_create(s, [{'clientKey': 'bulk-first', 'valueRefs': {r: {'clientKey': 'master'}}}, {'clientKey': 'bulk-second', 'valueRefs': {r: {'clientKey': 'bulk-first'}}}])
    d.bulk_update(s, [{'clientKey': 'bulk-update', 'id': id, 'ifMatch': tag, 'valueRefs': {r: {'clientKey': 'upserted-child'}}}])
    d.bulk_upsert(s, [{'clientKey': 'bulk-upsert', 'match': {'id': id}, 'valueRefs': {r: {'clientKey': 'existing'}}}])
    return {'精确值': inputs['amount']}
`,
	"javascript": `function main(inputs,ctx) {
  const d=ctx.data,s=inputs.schema,f=inputs.field,r=inputs.ref,id=inputs.id,tag=inputs.etag;
  d.create(s,{[f]:inputs.amount},'master');
  d.upsert(s,{id},{[f]:inputs.amount},tag,'existing');
  const refs = {[r]:{clientKey:'master'}};
  d.create(s,{[f]:inputs.amount},'child',refs);
  refs[r].clientKey='changed-after-staging';
  d.update(s,id,{},tag,'updated-child',{[r]:{clientKey:'existing'}});
  d.upsert(s,{indexId:inputs.index,values:{[f]:'DETAIL'}},{},'','upserted-child','check',{[r]:{clientKey:'master'}});
  d.bulk_create(s,[{clientKey:'bulk-first',valueRefs:{[r]:{clientKey:'master'}}},{clientKey:'bulk-second',valueRefs:{[r]:{clientKey:'bulk-first'}}}]);
  d.bulk_update(s,[{clientKey:'bulk-update',id,ifMatch:tag,valueRefs:{[r]:{clientKey:'upserted-child'}}}]);
  d.bulk_upsert(s,[{clientKey:'bulk-upsert',match:{id},valueRefs:{[r]:{clientKey:'existing'}}}]);
  return {精确值:inputs.amount};
}`,
	"go": `package main
func Main(inputs map[string]any,ctx *OntologyContext) map[string]any {
  d,s,f,r,id,tag:=ctx.Data,inputs["schema"].(string),inputs["field"].(string),inputs["ref"].(string),inputs["id"].(string),inputs["etag"].(string)
  d.Create(s,map[string]any{f:inputs["amount"]},"master")
  d.Upsert(s,OntologyMatch{ID:id},map[string]any{f:inputs["amount"]},tag,"existing","check")
  refs:=map[string]OntologyResultReference{r:{ClientKey:"master"}}
  d.Create(s,map[string]any{f:inputs["amount"]},"child",refs)
  refs[r]=OntologyResultReference{ClientKey:"changed-after-staging"}
  d.Update(s,id,map[string]any{},tag,"updated-child",map[string]OntologyResultReference{r:{ClientKey:"existing"}})
  d.Upsert(s,OntologyMatch{IndexID:inputs["index"].(string),Values:map[string]any{f:"DETAIL"}},nil,"","upserted-child","",map[string]OntologyResultReference{r:{ClientKey:"master"}})
  d.BulkCreate(s,[]OntologyWriteItem{{ClientKey:"bulk-first",ValueRefs:map[string]OntologyResultReference{r:{ClientKey:"master"}}},{ClientKey:"bulk-second",ValueRefs:map[string]OntologyResultReference{r:{ClientKey:"bulk-first"}}}})
  d.BulkUpdate(s,[]OntologyWriteItem{{ClientKey:"bulk-update",ID:id,IfMatch:tag,ValueRefs:map[string]OntologyResultReference{r:{ClientKey:"upserted-child"}}}})
  d.BulkUpsert(s,[]OntologyWriteItem{{ClientKey:"bulk-upsert",Match:&OntologyMatch{ID:id},ValueRefs:map[string]OntologyResultReference{r:{ClientKey:"existing"}}}},"")
  return map[string]any{"精确值":inputs["amount"]}
}`,
}

func TestDockerDataSDKResultReferences(t *testing.T) {
	for _, language := range supportedRuntimes {
		t.Run(language, func(t *testing.T) {
			runner, image := dockerLanguage(t, language)
			input := sdkInput()
			input["ref"] = json.RawMessage(`"10000000-0000-4000-8000-000000000005"`)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			result, err := runner.RunWithData(ctx, sdkRuntime(language, sdkReferenceSources[language]), input, image)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Operations) != 9 || string(result.Output["精确值"]) != `"9007199254740993.1000"` {
				t.Fatal("reference plan lost operations or exact output")
			}
			for i, source := range []string{"", "", "master", "existing", "master", "master", "bulk-first", "upserted-child", "existing"} {
				refs := result.Operations[i].ValueRefs
				if source == "" {
					if len(refs) != 0 {
						t.Fatal("legacy operation gained references")
					}
				} else if len(refs) != 1 || refs["10000000-0000-4000-8000-000000000005"].ClientKey != source {
					t.Fatalf("operation %d lost original result reference: %s", i, mustJSON(result.Operations[i]))
				}
			}
			if string(result.Operations[2].Values["10000000-0000-4000-8000-000000000002"]) != `"9007199254740993.1000"` {
				t.Fatal("literal and reference values were mixed or rounded")
			}
		})
	}
}

// Each fixture catches the invalid SDK call. finish must still discard the
// complete plan, including previously valid writes and a partially staged bulk.
func sdkInvalidReferenceSource(language, scenario string) string {
	if language == "python" {
		body := map[string]string{
			"unknown":       "d.create(s, {}, 'child', {r: {'clientKey': 'missing'}})",
			"self":          "d.create(s, {}, 'child', {r: {'clientKey': 'child'}})",
			"forward_bulk":  "d.bulk_create(s, [{'clientKey':'child','valueRefs':{r:{'clientKey':'later'}}}, {'clientKey':'later','values':{}}])",
			"partial_bulk":  "d.bulk_create(s, [{'clientKey':'valid','valueRefs':{r:{'clientKey':'master'}}}, {'clientKey':'bad','valueRefs':{r:{'clientKey':'missing'}}}])",
			"update_source": "d.update(s, id, {f: 'x'}, tag, 'updated')\n        d.create(s, {}, 'child', {r: {'clientKey': 'updated'}})",
			"delete_source": "d.delete(s, id, tag, 'deleted')\n        d.create(s, {}, 'child', {r: {'clientKey': 'deleted'}})",
			"literal":       "d.create(s, {r: id}, 'child', {r: {'clientKey': 'master'}})",
			"match":         "d.upsert(s, {'indexId': inputs['index'], 'values': {r: id}}, {}, '', 'child', value_refs={r: {'clientKey': 'master'}})",
			"field_name":    "d.create(s, {}, 'child', {'主记录': {'clientKey': 'master'}})",
			"empty_source":  "d.create(s, {}, 'child', {r: {'clientKey': ''}})",
			"selector":      "d.create(s, {}, 'child', {r: {'clientKey': 'master', 'path': 'id'}})",
			"delete_target": "d.bulk_delete(s, [{'clientKey':'deleted','id':id,'ifMatch':tag,'valueRefs':{r:{'clientKey':'master'}}}])",
		}[scenario]
		return "def main(inputs, ctx):\n    d,s,f,r,id,tag=ctx.data,inputs['schema'],inputs['field'],inputs['field'],inputs['id'],inputs['etag']\n    d.create(s, {f: 'x'}, 'master')\n    try:\n        " + body + "\n    except Exception:\n        pass\n    return {}\n"
	}
	if language == "javascript" {
		body := map[string]string{
			"unknown":       "d.create(s,{},'child',{[r]:{clientKey:'missing'}})",
			"self":          "d.create(s,{},'child',{[r]:{clientKey:'child'}})",
			"forward_bulk":  "d.bulk_create(s,[{clientKey:'child',valueRefs:{[r]:{clientKey:'later'}}},{clientKey:'later',values:{}}])",
			"partial_bulk":  "d.bulk_create(s,[{clientKey:'valid',valueRefs:{[r]:{clientKey:'master'}}},{clientKey:'bad',valueRefs:{[r]:{clientKey:'missing'}}}])",
			"update_source": "d.update(s,id,{[f]:'x'},tag,'updated');d.create(s,{},'child',{[r]:{clientKey:'updated'}})",
			"delete_source": "d.delete(s,id,tag,'deleted');d.create(s,{},'child',{[r]:{clientKey:'deleted'}})",
			"literal":       "d.create(s,{[r]:id},'child',{[r]:{clientKey:'master'}})",
			"match":         "d.upsert(s,{indexId:inputs.index,values:{[r]:id}},{},'','child','check',{[r]:{clientKey:'master'}})",
			"field_name":    "d.create(s,{},'child',{'主记录':{clientKey:'master'}})",
			"empty_source":  "d.create(s,{},'child',{[r]:{clientKey:''}})",
			"selector":      "d.create(s,{},'child',{[r]:{clientKey:'master',path:'id'}})",
			"delete_target": "d.bulk_delete(s,[{clientKey:'deleted',id,ifMatch:tag,valueRefs:{[r]:{clientKey:'master'}}}])",
		}[scenario]
		return "function main(inputs,ctx){const d=ctx.data,s=inputs.schema,f=inputs.field,r=f,id=inputs.id,tag=inputs.etag;d.create(s,{[f]:'x'},'master');try{" + body + "}catch{}return {}}"
	}
	body := map[string]string{
		"unknown":       `d.Create(s,nil,"child",map[string]OntologyResultReference{r:{ClientKey:"missing"}})`,
		"self":          `d.Create(s,nil,"child",map[string]OntologyResultReference{r:{ClientKey:"child"}})`,
		"forward_bulk":  `d.BulkCreate(s,[]OntologyWriteItem{{ClientKey:"child",ValueRefs:map[string]OntologyResultReference{r:{ClientKey:"later"}}},{ClientKey:"later",Values:map[string]any{}}})`,
		"partial_bulk":  `d.BulkCreate(s,[]OntologyWriteItem{{ClientKey:"valid",ValueRefs:map[string]OntologyResultReference{r:{ClientKey:"master"}}},{ClientKey:"bad",ValueRefs:map[string]OntologyResultReference{r:{ClientKey:"missing"}}}})`,
		"update_source": `d.Update(s,id,map[string]any{f:"x"},tag,"updated");d.Create(s,nil,"child",map[string]OntologyResultReference{r:{ClientKey:"updated"}})`,
		"delete_source": `d.Delete(s,id,tag,"deleted");d.Create(s,nil,"child",map[string]OntologyResultReference{r:{ClientKey:"deleted"}})`,
		"literal":       `d.Create(s,map[string]any{r:id},"child",map[string]OntologyResultReference{r:{ClientKey:"master"}})`,
		"match":         `d.Upsert(s,OntologyMatch{IndexID:inputs["index"].(string),Values:map[string]any{r:id}},nil,"","child","",map[string]OntologyResultReference{r:{ClientKey:"master"}})`,
		"field_name":    `d.Create(s,nil,"child",map[string]OntologyResultReference{"主记录":{ClientKey:"master"}})`,
		"empty_source":  `d.Create(s,nil,"child",map[string]OntologyResultReference{r:{ClientKey:""}})`,
		"selector":      `d.Create(s,map[string]any{},"child",map[string]OntologyResultReference{},map[string]OntologyResultReference{})`,
		"delete_target": `d.BulkDelete(s,[]OntologyWriteItem{{ClientKey:"deleted",ID:id,IfMatch:tag,ValueRefs:map[string]OntologyResultReference{r:{ClientKey:"master"}}}})`,
	}[scenario]
	return fmt.Sprintf(`package main
func Main(inputs map[string]any,ctx *OntologyContext)map[string]any {
  d,s,f,r,id,tag:=ctx.Data,inputs["schema"].(string),inputs["field"].(string),inputs["field"].(string),inputs["id"].(string),inputs["etag"].(string)
  _=r;_=id;_=tag
  d.Create(s,map[string]any{f:"x"},"master")
  func(){defer func(){recover()}();%s}()
  return map[string]any{}
}`, body)
}

func TestDockerDataSDKInvalidReferencesDiscardWholePlan(t *testing.T) {
	for _, language := range supportedRuntimes {
		t.Run(language, func(t *testing.T) {
			runner, image := dockerLanguage(t, language)
			for _, scenario := range []string{"unknown", "self", "forward_bulk", "partial_bulk", "update_source", "delete_source", "literal", "match", "field_name", "empty_source", "selector", "delete_target"} {
				t.Run(scenario, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
					defer cancel()
					result, err := runner.RunWithData(ctx, sdkRuntime(language, sdkInvalidReferenceSource(language, scenario)), sdkInput(), image)
					if err == nil || result.Output != nil || len(result.Operations) != 0 {
						t.Fatal("caught invalid reference exposed a partial plan")
					}
				})
			}
		})
	}
}
