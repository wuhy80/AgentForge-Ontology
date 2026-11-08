package execution

// These SDKs only collect an untrusted logical write plan. They have no data
// reader, generated record IDs, database handle, network client or credentials.
// Any SDK failure permanently poisons the invocation, even when user code
// catches the exception. Execution and Data still validate the entire plan.
const pythonDataSDK = `def __ontology_data_context():
    import math, re, types
    plan, keys = [], set()
    poisoned = False
    uuid = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
    def require(condition):
        if not condition:
            raise ValueError("invalid data write plan")
    def identifier(value):
        require(type(value) is str and uuid.fullmatch(value) is not None)
        return value
    def values_copy(values):
        require(type(values) is dict)
        result = {}
        for key, value in values.items():
            identifier(key)
            require(value is None or type(value) in (str, bool, int) or type(value) is float and math.isfinite(value))
            result[key] = value
        return result
    def etag(value, required):
        require(type(value) is str)
        if not value and not required:
            return value
        require(len(value) > 2 and value[0] == '"' and value[-1] == '"' and all(ord(ch) >= 33 and ord(ch) != 127 and ch != '"' for ch in value[1:-1]))
        return value
    def match_copy(match):
        require(type(match) is dict)
        if "id" in match:
            require(set(match) == {"id"})
            return {"id": identifier(match["id"])}
        require(set(match) == {"indexId", "values"} and len(match["values"]) > 0)
        return {"indexId": identifier(match["indexId"]), "values": values_copy(match["values"])}
    def stage(operation, schema, item, policy="check"):
        nonlocal poisoned
        try:
            require(not poisoned and len(plan) < 100)
            identifier(schema)
            require(type(item) is dict)
            allowed = {"create": {"clientKey", "values"}, "update": {"clientKey", "id", "values", "ifMatch"}, "delete": {"clientKey", "id", "ifMatch"}, "upsert": {"clientKey", "match", "values", "ifMatch"}}[operation]
            require(set(item).issubset(allowed))
            key = item.get("clientKey")
            require(type(key) is str and 0 < len(key.encode("utf-8")) <= 128 and all(ord(ch) >= 32 and not 127 <= ord(ch) <= 159 for ch in key) and key not in keys)
            out = {"schemaId": schema, "operation": operation, "clientKey": key}
            if operation != "delete":
                out["values"] = values_copy(item["values"])
            if operation in ("update", "delete"):
                out["id"] = identifier(item["id"])
                out["ifMatch"] = etag(item["ifMatch"], True)
            if operation == "upsert":
                require(policy in ("check", "overwrite"))
                out["match"] = match_copy(item["match"])
                out["conflictPolicy"] = policy
                token = etag(item.get("ifMatch", ""), False)
                if token:
                    out["ifMatch"] = token
            plan.append(out)
            keys.add(key)
        except BaseException:
            poisoned = True
            raise
    def bulk(operation, schema, items, policy="check"):
        nonlocal poisoned
        try:
            require(type(items) is list and 0 < len(items) <= 100 and len(plan) + len(items) <= 100)
            for item in items:
                stage(operation, schema, item, policy)
        except BaseException:
            poisoned = True
            raise
    class Data:
        def __getattr__(self, name):
            nonlocal poisoned
            poisoned = True
            raise AttributeError("unsupported data operation")
        def create(self, schema_id, values, client_key):
            stage("create", schema_id, {"values": values, "clientKey": client_key})
        def update(self, schema_id, id, values, if_match, client_key):
            stage("update", schema_id, {"id": id, "values": values, "ifMatch": if_match, "clientKey": client_key})
        def delete(self, schema_id, id, if_match, client_key):
            stage("delete", schema_id, {"id": id, "ifMatch": if_match, "clientKey": client_key})
        def upsert(self, schema_id, match, values, if_match, client_key, conflict_policy="check"):
            stage("upsert", schema_id, {"match": match, "values": values, "ifMatch": if_match, "clientKey": client_key}, conflict_policy)
        def bulk_create(self, schema_id, items):
            bulk("create", schema_id, items)
        def bulk_update(self, schema_id, items):
            bulk("update", schema_id, items)
        def bulk_delete(self, schema_id, items):
            bulk("delete", schema_id, items)
        def bulk_upsert(self, schema_id, items, conflict_policy="check"):
            bulk("upsert", schema_id, items, conflict_policy)
    def guard(method):
        def invoke(*args, **kwargs):
            nonlocal poisoned
            try:
                return method(*args, **kwargs)
            except BaseException:
                poisoned = True
                raise
        return invoke
    for name in ("create", "update", "delete", "upsert", "bulk_create", "bulk_update", "bulk_delete", "bulk_upsert"):
        setattr(Data, name, guard(getattr(Data, name)))
    def finish(output):
        require(not poisoned)
        require(type(output) is dict and all(type(key) is str for key in output))
        for value in output.values():
            require(value is None or type(value) in (str, bool, int) or type(value) is float and math.isfinite(value))
        return {"output": output, "operations": plan}
    return types.SimpleNamespace(data=Data()), finish
`

// This code executes inside the VM. Only JSON strings and booleans cross the
// host/VM interface; SDK functions never carry Node's Function constructor.
const javascriptDataSDK = `
const __ontologyWriteState = (() => {
  let poisoned = false;
  const plan = [], keys = new Set();
  const require = value => { if (!value) throw new Error('invalid data write plan'); };
  const identifier = value => { require(typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(value)); return value; };
  const object = value => {
    require(value !== null && typeof value === 'object' && [null,Object.prototype].includes(Object.getPrototypeOf(value)));
    const out = Object.create(null);
    for (const key of Reflect.ownKeys(value)) {
      const d = Object.getOwnPropertyDescriptor(value,key);
      require(typeof key === 'string' && d && d.enumerable && Object.hasOwn(d,'value'));
      out[key] = d.value;
    }
    return out;
  };
  const values = value => {
    const out = object(value);
    for (const key of Object.keys(out)) {
      identifier(key);
      const v = out[key];
      require(v === null || typeof v === 'string' || typeof v === 'boolean' || typeof v === 'number' && Number.isFinite(v));
    }
    return out;
  };
  const etag = (value,required) => { require(typeof value === 'string'); if (value === '' && !required) return value; require(/^"[^"\x00-\x20\x7f]+"$/.test(value)); return value; };
  const match = value => {
    const v = object(value), ks = Object.keys(v);
    if (Object.hasOwn(v,'id')) { require(ks.length === 1); return {id:identifier(v.id)}; }
    require(ks.length === 2 && Object.hasOwn(v,'indexId') && Object.hasOwn(v,'values'));
    const copied = values(v.values); require(Object.keys(copied).length > 0);
    return {indexId:identifier(v.indexId),values:copied};
  };
  const stage = (operation,schema,item,policy='check') => {
    try {
      require(!poisoned && plan.length < 100); identifier(schema);
      const v = object(item), allowed = {create:['clientKey','values'],update:['clientKey','id','values','ifMatch'],delete:['clientKey','id','ifMatch'],upsert:['clientKey','match','values','ifMatch']}[operation];
      require(Object.keys(v).every(key => allowed.includes(key)));
      require(typeof v.clientKey === 'string' && v.clientKey.length > 0 && !/[\x00-\x1f\x7f-\x9f]/.test(v.clientKey) && encodeURIComponent(v.clientKey).replace(/%[A-F\d]{2}/gi,'x').length <= 128 && !keys.has(v.clientKey));
      const out = {schemaId:schema,operation,clientKey:v.clientKey};
      if (operation !== 'delete') out.values = values(v.values);
      if (operation === 'update' || operation === 'delete') { out.id=identifier(v.id); out.ifMatch=etag(v.ifMatch,true); }
      if (operation === 'upsert') { require(policy === 'check' || policy === 'overwrite'); out.match=match(v.match); out.conflictPolicy=policy; const token=etag(v.ifMatch ?? '',false); if(token) out.ifMatch=token; }
      plan.push(out); keys.add(v.clientKey);
    } catch (failure) { poisoned=true; throw failure; }
  };
  const bulk = (operation,schema,items,policy='check') => {
    try { require(Array.isArray(items) && items.length > 0 && items.length <= 100 && plan.length+items.length <= 100); for(const item of items) stage(operation,schema,item,policy); }
    catch(failure) { poisoned=true; throw failure; }
  };
  const data = Object.freeze({
    create(schema,values,clientKey) { stage('create',schema,{values,clientKey}); },
    update(schema,id,values,ifMatch,clientKey) { stage('update',schema,{id,values,ifMatch,clientKey}); },
    delete(schema,id,ifMatch,clientKey) { stage('delete',schema,{id,ifMatch,clientKey}); },
    upsert(schema,match,values,ifMatch,clientKey,conflictPolicy='check') { stage('upsert',schema,{match,values,ifMatch,clientKey},conflictPolicy); },
    bulk_create(schema,items) { bulk('create',schema,items); },
    bulk_update(schema,items) { bulk('update',schema,items); },
    bulk_delete(schema,items) { bulk('delete',schema,items); },
    bulk_upsert(schema,items,conflictPolicy='check') { bulk('upsert',schema,items,conflictPolicy); }
  });
  const checkedData = new Proxy(data,{get(target,key,receiver) { if (!Reflect.has(target,key)) { poisoned=true; throw new Error('unsupported data operation'); } return Reflect.get(target,key,receiver); }});
  return Object.freeze({context:Object.freeze({data:checkedData}),finish(outputJSON) { require(!poisoned); return JSON.stringify({output:JSON.parse(outputJSON),operations:plan}); }});
})();
`
