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


def definition_signature(definition: dict):
    """Compare binding semantics, ignoring server IDs and display descriptions."""
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
            properties.append(item)
        result["schema"] = {
            "mode": schema["mode"], "storageLayout": schema["storageLayout"],
            "properties": sorted(properties, key=lambda p: p["name"]),
            "indexes": sorted([{"name": i["name"], "unique": bool(i.get("unique")), "fields": [names[f] for f in i["fieldIds"]]} for i in schema.get("indexes", [])], key=lambda i: i["name"]),
        }
    elif definition["kind"] == "link":
        result["link"] = definition["link"]
    return result


class Importer:
    def __init__(self, client, state: State, blueprint: dict):
        self.client, self.state, self.blueprint = client, state, blueprint
        self.schemas = {s["key"]: s for s in blueprint["schemas"]}
        self.definitions = {}
        self.stats = {"created": {}, "updated": {}, "unchanged": {}, "unavailable": {}}

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
            properties = []
            property_ids = {}
            for prop in item["schema"]["properties"]:
                prop = deepcopy(prop)
                key = prop.pop("key")
                prop["id"] = str(uuid.uuid4())
                property_ids[key] = prop["id"]
                properties.append(prop)
            indexes = []
            for index in item["schema"].get("indexes", item.get("indexes", [])):
                indexes.append({"id": str(uuid.uuid4()), "name": index["name"], "unique": bool(index.get("unique")), "fieldIds": [property_ids[key] for key in index["fields"]]})
            payload = {"name": item["name"], "description": item.get("description", ""), "categoryId": category["id"], "groupId": group_ids[item["group"]], "kind": "schema", "schema": {"mode": "schema_2_db", "storageLayout": "single", "properties": properties, "indexes": indexes}}
            self.definitions[item["key"]] = self._ensure_definition(item["key"], payload, current, drafts)
        for item in self.blueprint["links"]:
            payload = {"name": item["name"], "description": item.get("description", "") or "文档关系定义；当前仅元数据，Data 尚不支持 Link 实例读写。", "categoryId": category["id"], "groupId": group_ids[item["group"]], "kind": "link", "link": {
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
        if definition_signature(record["definition"]) != definition_signature(payload):
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
