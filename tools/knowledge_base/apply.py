"""Resumable modeling and single-item import over the existing Data APIs."""
from __future__ import annotations

from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import time
import uuid

from .client import ImportFailure


def digest(value) -> str:
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode("utf-8")).hexdigest()


class State:
    def __init__(self, path: Path, context: dict):
        self.path = Path(path)
        if self.path.exists():
            self.data = json.loads(self.path.read_text(encoding="utf-8-sig"))
            if self.data.get("context") != context:
                raise ImportFailure("同步状态属于其他服务、租户、库或范围，请为本次来源指定独立 --state")
        else:
            self.data = {"format": 1, "context": context, "definitions": {}, "entities": {}}
        if self.data.get("format") != 1:
            raise ImportFailure("不支持的同步状态格式")

    def save(self):
        self.path.parent.mkdir(parents=True, exist_ok=True)
        temporary = self.path.with_name(self.path.name + ".tmp")
        with temporary.open("w", encoding="utf-8") as handle:
            handle.write(json.dumps(self.data, ensure_ascii=False, indent=2) + "\n")
            handle.flush()
            os.fsync(handle.fileno())
        # Windows file indexers can briefly retain an open destination handle.
        for attempt in range(10):
            try:
                temporary.replace(self.path)
                break
            except PermissionError:
                if attempt == 9:
                    raise
                time.sleep(0.1)


def definition_signature(definition: dict, *, logical=False):
    """Compare metadata; logical reuse alone may ignore explicit storage bindings."""
    result = {k: definition.get(k) for k in ("kind", "name", "categoryId", "groupId")}
    if definition["kind"] == "schema":
        schema = definition["schema"]
        names = {p["id"]: p["name"] for p in schema["properties"]}
        properties = []
        for prop in schema["properties"]:
            item = {"name": prop["name"], "dataType": prop["dataType"]}
            for key in ("required", "nullable", "readOnly"):
                item[key] = bool(prop.get(key))
            for key in ("precision", "scale", "enumId", "default"):
                if prop.get(key) is not None and prop.get(key) != "" and prop.get(key) != 0:
                    item[key] = prop[key]
            if not logical and prop.get("mapping") is not None:
                item["mapping"] = prop["mapping"]
            properties.append(item)
        result["schema"] = {
            "storageLayout": schema["storageLayout"],
            "properties": sorted(properties, key=lambda p: p["name"]),
            "indexes": sorted([{"name": i["name"], "unique": bool(i.get("unique")), "fields": [names[f] for f in i["fieldIds"]]} for i in schema.get("indexes", [])], key=lambda i: i["name"]),
        }
        if not logical:
            result["schema"]["mode"] = schema["mode"]
            if schema.get("storageBinding") is not None:
                result["schema"]["storageBinding"] = schema["storageBinding"]
    elif definition["kind"] == "link":
        result["link"] = deepcopy(definition["link"])
        if logical:
            result["link"].pop("storageBinding", None)
    return result


class Importer:
    def __init__(self, client, state: State, blueprint: dict):
        self.client, self.state, self.blueprint = client, state, blueprint
        self.schemas = {s["key"]: s for s in blueprint["schemas"]}
        self.definitions = {}
        self.stats = {"created": {}, "updated": {}, "unchanged": {}, "unavailable": {}}

    @staticmethod
    def _schema_payload(item, category_id, group_id):
        properties, property_ids = [], {}
        for candidate in item["schema"]["properties"]:
            prop = deepcopy(candidate)
            key = prop.pop("key")
            prop["id"] = str(uuid.uuid4())
            property_ids[key] = prop["id"]
            properties.append(prop)
        indexes = [{"id": str(uuid.uuid4()), "name": index["name"], "unique": bool(index.get("unique")), "fieldIds": [property_ids[key] for key in index["fields"]]}
                   for index in item["schema"].get("indexes", item.get("indexes", []))]
        return {"name": item["name"], "description": item.get("description", ""), "categoryId": category_id, "groupId": group_id, "kind": "schema",
                "schema": {"mode": "schema_2_db", "storageLayout": "single", "properties": properties, "indexes": indexes}}

    @staticmethod
    def _reusable_definition(record, payload):
        existing = record["definition"]
        explicit = ((existing.get("schema") or {}).get("storageBinding") or (existing.get("link") or {}).get("storageBinding"))
        expected_binding = ((payload.get("schema") or {}).get("storageBinding") or (payload.get("link") or {}).get("storageBinding"))
        if record.get("status") == "active" and explicit and not expected_binding:
            schema = existing.get("schema")
            if schema and schema.get("mode") not in ("schema_2_db", "db_2_schema"):
                return False
            # Only reuse a published model. Creation/recovery comparisons retain
            # the physical binding so an unexpected candidate is never adopted.
            return definition_signature(existing, logical=True) == definition_signature(payload, logical=True)
        return definition_signature(existing) == definition_signature(payload)

    def load_existing_models(self):
        """Load current Schema metadata for read-only verification; never create or save."""
        category_id = self.state.data.get("category", {}).get("id")
        groups = self.state.data.get("groups", {})
        for item in self.blueprint["schemas"]:
            binding = self.state.data["definitions"].get(item["key"], {})
            group_id = groups.get(item["group"])
            if not binding.get("id") or not category_id or not group_id:
                raise ImportFailure("验证缺少模型、分类或分组的稳定身份映射，请核对原同步状态")
            record = self.client.request("GET", "/api/metadata/definitions/" + binding["id"])
            definition = record["definition"]
            expected = self._schema_payload(item, category_id, group_id)
            expected["name"] = definition["name"]
            if record.get("status") != "active" or definition["id"] != binding["id"] or not self._reusable_definition(record, expected):
                raise ImportFailure(f"{item['name']} 当前定义与已导入字段不兼容，停止只读验证")
            self.definitions[item["key"]] = definition
        return {key: definition["id"] for key, definition in self.definitions.items()}

    def verify_existing_manifest(self, manifest):
        """Compare all imported source fields with a saved complete scan; no source I/O."""
        if manifest.get("complete") is not True or manifest.get("format") != "agentforge.knowledge-base.scan.v1":
            raise ImportFailure("只读验证需要完整的既有扫描清单，不接受不完整扫描")
        context = self.state.data.get("context", {})
        if any(manifest.get(key) != context.get(key) for key in ("scope", "library_key")):
            raise ImportFailure("扫描清单与同步状态的知识库或范围不同")
        if set(manifest.get("entities", {})) != set(self.schemas):
            raise ImportFailure("扫描清单缺少模型或包含未知模型，停止只读验证")
        self.load_existing_models()
        records, verified, seen = self.state.data["entities"], {}, set()
        for schema_key, envelopes in manifest["entities"].items():
            if manifest.get("counts", {}).get(schema_key) != len(envelopes):
                raise ImportFailure("扫描清单计数与实际记录不同")
            properties = self._properties(schema_key)
            for envelope in envelopes:
                source_key = envelope["source_key"]
                record = records.get(source_key)
                if source_key in seen or envelope["data"].get("source_key") != source_key:
                    raise ImportFailure("扫描清单来源键重复或与字段不一致")
                seen.add(source_key)
                if not record or not record.get("id") or record.get("schema") != schema_key:
                    raise ImportFailure("验证缺少匹配的实例稳定身份映射")
                data = deepcopy(envelope["data"])
                for key, target in envelope.get("bindings", {}).items():
                    if not records.get(target, {}).get("id"):
                        raise ImportFailure("验证缺少引用目标的稳定身份映射")
                    data[key] = records[target]["id"]
                for key, prop in properties.items():
                    if key == "synced_at":
                        data.pop(key, None)
                    elif key not in data:
                        if not prop.get("nullable"):
                            raise ImportFailure(f"扫描清单缺少必填字段 {schema_key}.{key}")
                        data[key] = None
                schema_id = self.definitions[schema_key]["id"]
                current = self.client.request("GET", f"/api/data/schemas/{schema_id}/instances/{record['id']}")
                desired = self._wire(schema_key, data)
                if current.get("id") != record["id"] or current.get("schemaId") != schema_id or any(key not in current["values"] or current["values"][key] != value for key, value in desired.items()):
                    raise ImportFailure(f"{schema_key} 回读与既有扫描清单不同，停止报告成功")
                verified[schema_key] = verified.get(schema_key, 0) + 1
            verified.setdefault(schema_key, 0)
        return {"verified": verified, "readOnly": True, "businessWrites": 0, "sourceRescanned": False}

    def _assert_existing_targets_writable(self):
        """Reject mapped targets before ensure_models could create other definitions."""
        categories = self.client.catalog("/api/metadata/categories")
        category_id = self.state.data.get("category", {}).get("id")
        matches = [item for item in categories if item["id"] == category_id] if category_id else [item for item in categories if item["name"] == self.blueprint["category_name"]]
        if category_id and not matches:
            raise ImportFailure("已绑定分类不可读取，停止以免重建副本")
        if not matches:
            return
        if len(matches) != 1:
            raise ImportFailure("分类名称存在歧义")
        current = self.client.catalog("/api/metadata/definitions", categoryId=matches[0]["id"])
        for item in self.blueprint["schemas"]:
            bound_id = self.state.data["definitions"].get(item["key"], {}).get("id")
            existing = [record["definition"] for record in current if record["definition"]["id"] == bound_id] if bound_id else [record["definition"] for record in current if record["definition"]["name"] == item["name"]]
            if any(definition.get("schema", {}).get("storageBinding") for definition in existing):
                raise ImportFailure("知识库已使用显式数据源，当前尚不支持映射实例写入；本次未创建或更新任何业务资料。请使用只读验证入口核对既有扫描清单")

    def ensure_models(self):
        client = self.client
        category_name = self.blueprint["category_name"]
        categories = client.catalog("/api/metadata/categories")
        saved_category = self.state.data.get("category", {}).get("id")
        matches = [c for c in categories if c["id"] == saved_category] if saved_category else [c for c in categories if c["name"] == category_name]
        if saved_category and not matches:
            raise ImportFailure("已绑定分类不可读取，停止以免重建副本")
        if len(matches) > 1:
            raise ImportFailure("分类名称存在歧义")
        category = matches[0] if matches else client.request("POST", "/api/metadata/categories", {"name": category_name})
        self.state.data["category"] = category
        self.state.save()
        groups = client.catalog("/api/metadata/groups", categoryId=category["id"])
        group_ids = {}
        saved_groups = self.state.data.setdefault("groups", {})
        for group in self.blueprint["groups"]:
            name = group if isinstance(group, str) else group["name"]
            group_key = group.get("key", name) if isinstance(group, dict) else name
            saved_id = saved_groups.get(group_key)
            match = [g for g in groups if g["id"] == saved_id] if saved_id else [g for g in groups if g["name"] == name]
            if saved_id and not match:
                raise ImportFailure("已绑定分组不可读取，停止以免重建副本")
            record = match[0] if match else client.request("POST", "/api/metadata/groups", {"name": name, "categoryId": category["id"]})
            saved_groups[group_key] = record["id"]
            self.state.save()
            group_ids[name] = record["id"]
            if isinstance(group, dict) and "key" in group:
                group_ids[group["key"]] = record["id"]
        current = client.catalog("/api/metadata/definitions", categoryId=category["id"])
        drafts = client.catalog("/api/metadata/definitions", categoryId=category["id"], view="draft")
        for item in self.blueprint["schemas"]:
            payload = self._schema_payload(item, category["id"], group_ids[item["group"]])
            self.definitions[item["key"]] = self._ensure_definition(item["key"], payload, current, drafts)
        for item in self.blueprint["links"]:
            payload = {"name": item["name"], "description": item.get("description", "") or "文档关系定义；数据库映射须显式配置并发布，工具不自动建立关系事实。", "categoryId": category["id"], "groupId": group_ids[item["group"]], "kind": "link", "link": {
                "sourceSchemaId": self.definitions[item["source_schema"]]["id"],
                "targetSchemaId": self.definitions[item["target_schema"]]["id"],
                **{k: item[k] for k in ("forwardName", "reverseName", "cardinality", "onDelete")},
            }}
            self._ensure_definition(item["key"], payload, current, drafts)
        return {"category": category["name"], "groups": len(self.blueprint["groups"]), "schemas": len(self.blueprint["schemas"]), "linkDefinitions": len(self.blueprint["links"])}

    def _ensure_definition(self, key, payload, current, drafts):
        saved = self.state.data["definitions"].get(key, {})
        bound_id = saved.get("id")
        live = [r for r in current if r["definition"]["id"] == bound_id] if bound_id else [r for r in current if r["definition"]["name"] == payload["name"]]
        pending = [r for r in drafts if r["definition"]["id"] == bound_id] if bound_id else [r for r in drafts if r["definition"]["name"] == payload["name"]]
        if bound_id and not live and not pending:
            raise ImportFailure("已绑定模型不可读取，停止以免按名称重建副本")
        if bound_id and (live or pending):
            # A model display rename retains its existing binding identity.
            payload["name"] = (live or pending)[0]["definition"]["name"]
        if live:
            if pending:
                raise ImportFailure(f"{payload['name']} 存在未生效编辑，停止以保留已有草稿")
            record = live[0]
        elif pending:
            record = pending[0]
            planned = saved.get("create_payload")
            owned = bound_id == record["definition"]["id"] and saved.get("etag") == record["etag"]
            if planned and planned.get("kind") == "schema":
                recovered = record["definition"]
                # Exact generated property/index IDs prove this draft came from
                # the persisted intent; matching business names alone do not.
                def identities(candidate):
                    schema = candidate["schema"]
                    return ([p["id"] for p in schema["properties"]], [i["id"] for i in schema.get("indexes", [])])
                owned = (identities(planned) == identities(recovered) and definition_signature(planned) == definition_signature(recovered)
                         and planned.get("description", "") == recovered.get("description", ""))
            if not owned:
                raise ImportFailure(f"{payload['name']} 是未绑定的已有草稿，未自动发布")
        else:
            planned = saved.get("create_payload")
            if planned:
                if definition_signature(planned) != definition_signature(payload):
                    raise ImportFailure("未确认的模型创建意图与当前蓝图不同，请先恢复原意图")
                payload = planned
            else:
                self.state.data["definitions"][key] = {"create_payload": payload}
                self.state.save()
            record = self.client.request("POST", "/api/metadata/definitions", payload)
        if not (self._reusable_definition(record, payload) if live else definition_signature(record["definition"]) == definition_signature(payload)):
            raise ImportFailure(f"已有定义 {payload['name']} 与蓝图不一致，未覆盖")
        definition = record["definition"]
        resume = saved.get("publicationPending")
        if not live or resume:
            if live and saved.get("etag") != record["etag"]:
                raise ImportFailure("未完成发布的当前内容已改变，保留恢复意图并停止")
            self.state.data["definitions"][key] = {"id": definition["id"], "etag": record["etag"], "publicationPending": True}
            self.state.save()
            if not live and not resume:
                self.client.request("POST", f"/api/metadata/definitions/{definition['id']}/preview")
            publication = self.client.request("POST", f"/api/metadata/definitions/{definition['id']}/publish", etag=record["etag"])
            if publication["status"] != "succeeded":
                raise ImportFailure(f"{payload['name']} 生效尚未成功，请使用同一状态文件重试")
            definition = self.client.request("GET", f"/api/metadata/definitions/{definition['id']}")["definition"]
        self.state.data["definitions"][key] = {"id": definition["id"]}
        self.state.save()
        return definition

    def _properties(self, schema_key):
        definition = self.definitions[schema_key]
        by_name = {p["name"]: p for p in definition["schema"]["properties"]}
        return {p["key"]: by_name[p["name"]] for p in self.schemas[schema_key]["schema"]["properties"]}

    def _wire(self, schema_key, data):
        properties = self._properties(schema_key)
        values = {}
        for key, value in data.items():
            if key not in properties:
                raise ImportFailure(f"{schema_key} 包含未定义字段 {key}")
            prop = properties[key]
            if value is None and not prop.get("nullable"):
                raise ImportFailure(f"{schema_key}.{key} 不允许空值")
            if value is not None and prop["dataType"] in ("int64", "decimal"):
                value = str(value)
            values[prop["id"]] = value
        return values

    def _read_data(self, schema_key, instance):
        return {key: instance["values"][prop["id"]] for key, prop in self._properties(schema_key).items() if prop["id"] in instance["values"]}

    def _count(self, action, schema_key):
        counters = self.stats[action]
        counters[schema_key] = counters.get(schema_key, 0) + 1

    def apply_scan(self, scan: dict, models_only=False):
        if scan.get("complete") is not True:
            raise ImportFailure("扫描不完整或有解析错误，未执行任何建模或导入")
        if not models_only:
            previous_notes = {key for key, record in self.state.data["entities"].items() if record.get("schema") == "notes" and record.get("available", True)}
            incoming_notes = {e["source_key"] for e in scan["entities"].get("notes", [])}
            if previous_notes - incoming_notes and incoming_notes - previous_notes:
                raise ImportFailure("同时发现笔记路径新增和消失，可能发生重命名；本次未写入，请先核对来源映射，避免改变既有身份")
            self._assert_existing_targets_writable()
        result = self.ensure_models()
        if models_only:
            return {**result, "modelsOnly": True}
        entities = self.state.data["entities"]
        known_ids = {key: value["id"] for key, value in entities.items() if value.get("id")}
        seen = set()
        for schema_key in self.schemas:
            for envelope in scan["entities"].get(schema_key, []):
                source_key = envelope["source_key"]
                if source_key in seen:
                    raise ImportFailure("扫描来源键重复，停止导入")
                seen.add(source_key)
                data = deepcopy(envelope["data"])
                for key, target_key in envelope.get("bindings", {}).items():
                    if target_key not in known_ids:
                        raise ImportFailure(f"{schema_key}.{key} 目标尚未导入，停止避免错误绑定")
                    data[key] = known_ids[target_key]
                for key, prop in self._properties(schema_key).items():
                    if key != "synced_at" and key not in data and prop.get("nullable"):
                        data[key] = None
                instance = self._upsert(schema_key, source_key, data)
                known_ids[source_key] = instance["id"]
        # Only records owned by this complete, scoped state may be marked missing.
        for source_key, record in list(entities.items()):
            if source_key not in seen and record.get("id") and record.get("available", True) and record["schema"] != "knowledge_base":
                properties = self._properties(record["schema"])
                current = self.client.request("GET", f"/api/data/schemas/{self.definitions[record['schema']]['id']}/instances/{record['id']}")
                if current["values"].get(properties["available"]["id"]) is False:
                    record.update(etag=current["etag"], available=False)
                    self.state.save()
                    continue
                if record.get("etag") != current["etag"]:
                    raise ImportFailure("缺失来源对应实例已被其他用户修改，未标记失效")
                values = {properties["available"]["id"]: False, properties["synced_at"]["id"]: datetime.now(timezone.utc).isoformat()}
                updated = self.client.request("PATCH", f"/api/data/schemas/{self.definitions[record['schema']]['id']}/instances/{record['id']}", {"values": values}, etag=current["etag"])
                record.update(etag=updated["etag"], available=False)
                self.state.save()
                self._count("unavailable", record["schema"])
        return {**result, **self.stats, "linkFactsWritten": 0, "associations": len(scan.get("associations", []))}

    def _upsert(self, schema_key, source_key, data):
        definition = self.definitions[schema_key]
        if definition.get("schema", {}).get("storageBinding"):
            raise ImportFailure("显式数据源的实例写入尚未开放，未执行创建或更新；请改用只读验证")
        properties = self._properties(schema_key)
        schema_id = definition["id"]
        records = self.state.data["entities"]
        record = records.get(source_key)
        data.pop("synced_at", None)
        desired = self._wire(schema_key, data)
        wanted_hash = digest(desired)
        if record and record.get("id"):
            current = self.client.request("GET", f"/api/data/schemas/{schema_id}/instances/{record['id']}")
        else:
            rows = self.client.query(schema_id, [p["id"] for p in properties.values()], {"op": "eq", "field": properties["source_key"]["id"], "value": source_key})
            if len(rows) > 1:
                raise ImportFailure("来源键存在重复实例")
            current = rows[0] if rows else None
        if current:
            actual = {key: current["values"].get(key) for key in desired}
            if digest(actual) == wanted_hash:
                records[source_key] = {"id": current["id"], "schema": schema_key, "etag": current["etag"], "hash": wanted_hash, "available": True}
                self.state.save()
                self._count("unchanged", schema_key)
                return current
            if not record or record.get("etag") != current["etag"]:
                raise ImportFailure(f"{schema_key} 的来源对象已在系统中改变或缺少可信同步状态，未覆盖；请核对冲突")
            data["synced_at"] = datetime.now(timezone.utc).isoformat()
            updated = self.client.request("PATCH", f"/api/data/schemas/{schema_id}/instances/{current['id']}", {"values": self._wire(schema_key, data)}, etag=current["etag"])
            action = "updated"
        else:
            # Persist the exact create intent BEFORE sending. An uncertain response
            # is reconciled by unique source key on the next invocation.
            if record and record.get("pending_hash") and record["pending_hash"] != wanted_hash:
                raise ImportFailure("上次创建结果未确认且源内容已改变，请先恢复上次输入")
            record = record or {"schema": schema_key, "create_key": str(uuid.uuid4())}
            data["synced_at"] = record.get("pending_time") or datetime.now(timezone.utc).isoformat()
            record.update(pending_hash=wanted_hash, pending_time=data["synced_at"])
            records[source_key] = record
            self.state.save()
            updated = self.client.request("POST", f"/api/data/schemas/{schema_id}/instances", {"values": self._wire(schema_key, data), "idempotencyKey": record["create_key"]})
            action = "created"
        records[source_key] = {"id": updated["id"], "schema": schema_key, "etag": updated["etag"], "hash": wanted_hash, "available": True}
        self.state.save()
        self._count(action, schema_key)
        return updated


def verify_manifest_cli(argv=None):
    """Read an existing manifest and state without scanning or changing source files."""
    import argparse
    import sys
    from .blueprint import BLUEPRINT
    from .client import Client

    parser = argparse.ArgumentParser(description="只读核对既有知识库扫描清单，不重扫源文件、不创建模型或写实例")
    parser.add_argument("--verify-manifest", required=True, type=Path, help="已保存的完整扫描JSON，包含原文")
    parser.add_argument("--state", required=True, type=Path, help="原导入状态及稳定对象ID")
    parser.add_argument("--credentials", required=True, type=Path, help="本地账号JSON；密码不进入命令行")
    parser.add_argument("--url", default="http://127.0.0.1:5173", help="原工作台同源地址")
    args = parser.parse_args(argv)
    try:
        saved = json.loads(args.state.read_text(encoding="utf-8-sig"))
        context = saved.get("context", {})
        credentials = json.loads(args.credentials.read_text(encoding="utf-8-sig"))
        tenant = credentials.get("Tenant") or credentials.get("tenant")
        username = credentials.get("Username") or credentials.get("username")
        password = credentials.get("Password") or credentials.get("password")
        if not tenant or not username or not password:
            raise ImportFailure("账号文件缺少租户、用户名或密码")
        if context.get("url") != args.url.rstrip("/") or context.get("tenant") != tenant:
            raise ImportFailure("只读验证的服务或租户与原同步状态不同")
        manifest = json.loads(args.verify_manifest.read_text(encoding="utf-8-sig"))
        state = State(args.state, context)
        client = Client(args.url)
        client.login(tenant, username, password)
        try:
            result = Importer(client, state, BLUEPRINT).verify_existing_manifest(manifest)
            print(json.dumps(result, ensure_ascii=False, indent=2))
        finally:
            try:
                client.logout()
            except ImportFailure:
                print("验证会话注销未确认，服务端会话仍按有效期管理。", file=sys.stderr)
        return 0
    except (ImportFailure, ValueError, OSError) as error:
        print(f"知识库只读验证停止：{error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(verify_manifest_cli())
