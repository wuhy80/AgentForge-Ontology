"""Preview and resume an isolated CSV create import through the public API."""
from __future__ import annotations

import argparse
import csv
from datetime import datetime
import getpass
import hashlib
import io
import json
import os
from pathlib import Path
import re
import sys
import tempfile
from urllib.parse import quote
import uuid

from tools.knowledge_base.client import Client, ImportFailure


def json_bytes(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode("utf-8")


def convert(value, field):
    kind = field["dataType"]
    if kind in ("string", "text", "longtext"):
        if kind == "string" and len(value) > 255:
            raise ValueError("字符串超过长度预算")
        if kind != "string" and len(value.encode("utf-8")) > (65535 if kind == "text" else 4194304):
            raise ValueError("文本超过长度预算")
        return value
    if kind == "int64" and re.fullmatch(r"-?(?:0|[1-9][0-9]{0,18})", value):
        if -(2**63) <= int(value) < 2**63:
            return value
    if kind == "decimal" and re.fullmatch(r"-?[0-9]+(?:\.[0-9]+)?", value):
        whole, _, fraction = value.lstrip("-").partition(".")
        scale = field.get("scale", 0)
        if len(whole.lstrip("0")) <= field["precision"] - scale and len(fraction) <= scale:
            return value
    if kind == "boolean" and value in ("true", "false"):
        return value == "true"
    if kind == "uuid":
        if str(uuid.UUID(value)) == value:
            return value
    if kind == "timestamp" and re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,6})?(?:Z|[+-]\d\d:\d\d)", value):
        datetime.fromisoformat(value)
        return value
    raise ValueError("值不符合字段类型")


def prepare(content, mapping, record, chunk_size):
    schema = record["definition"].get("schema")
    if record["status"] != "active" or not schema or schema["mode"] != "schema_2_db" or schema.get("storageBinding") or schema["storageLayout"] != "single":
        raise ImportFailure("仅支持已生效的平台托管单表 Schema")
    if not isinstance(mapping, dict) or not mapping or any(not isinstance(v, str) for v in mapping.values()) or len(set(mapping.values())) != len(mapping):
        raise ImportFailure("映射须将 CSV 列名一对一映射到稳定字段 ID")
    fields = {field["id"]: field for field in schema["properties"]}
    for field in fields.values():
        if field.get("enumId"):
            raise ImportFailure("首批 CSV 导入暂不支持含枚举字段的 Schema")
    for field_id in mapping.values():
        field = fields.get(field_id)
        if not field or field.get("readOnly") or field.get("fileKind"):
            raise ImportFailure(f"字段不可作为导入目标：{field_id}")
    for field in fields.values():
        if field.get("required") and field.get("default") is None and field["id"] not in mapping.values():
            raise ImportFailure(f"缺少必填字段映射：{field['id']}")
    reader = csv.reader(io.StringIO(content.decode("utf-8-sig"), newline=""), strict=True)
    header = next(reader, [])
    if not header or len(set(header)) != len(header) or set(mapping) - set(header):
        raise ImportFailure("CSV 表头重复、缺失或与映射不匹配")
    selected = [(header.index(column), fields[field]) for column, field in mapping.items()]
    chunks, current, current_bytes = [], [], 0
    for index, row in enumerate(reader, 1):
        if len(row) != len(header):
            raise ImportFailure(f"记录 {index} 列数与表头不一致")
        values = {}
        for position, field in selected:
            value = row[position]
            if value == "" and field["dataType"] not in ("string", "text", "longtext"):
                if field.get("nullable"):
                    values[field["id"]] = None
                    continue
            try:
                values[field["id"]] = convert(value, field)
            except (ValueError, KeyError) as error:
                raise ImportFailure(f"记录 {index} 字段 {field['id']} 类型校验失败") from error
        operation = {"schemaId": record["definition"]["id"], "schemaEtag": record["etag"],
                     "operation": "create", "clientKey": str(index), "values": values}
        size = len(json_bytes(operation)) + 1
        if size > 7 * 1024 * 1024:
            raise ImportFailure(f"记录 {index} 超过分块请求预算")
        if current and (len(current) == chunk_size or current_bytes + size > 7 * 1024 * 1024):
            chunks.append(current)
            current, current_bytes = [], 0
        current.append(operation)
        current_bytes += size
    if not current:
        raise ImportFailure("CSV 没有数据记录")
    chunks.append(current)
    return chunks


def write_state(path, state):
    temp = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as stream:
            temp = Path(stream.name)
            stream.write(json_bytes(state))
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temp, path)
    finally:
        if temp is not None:
            temp.unlink(missing_ok=True)


def apply(client, path, identity, chunks, progress=print):
    initial = {"manifest": identity, "id": uuid.uuid4().hex, "next": 0}
    try:
        with path.open("xb") as stream:
            stream.write(json_bytes(initial))
            stream.flush()
            os.fsync(stream.fileno())
    except FileExistsError:
        pass
    state = json.loads(path.read_bytes())
    if (state["manifest"] != identity or not re.fullmatch(r"[a-f0-9]{32}", state["id"])
            or type(state["next"]) is not int or not 0 <= state["next"] <= len(chunks)):
        raise ImportFailure("状态文件与本次用户、Schema、输入或分块参数不一致")
    for index in range(state["next"], len(chunks)):
        body = {"idempotencyKey": f"csv:{state['id']}:{index}", "operations": chunks[index]}
        result = client.request("POST", "/api/data/transactions", body)
        if result.get("status") != "committed" or result.get("committed") is not True:
            error = result.get("error") or {}
            raise ImportFailure(f"第 {index + 1} 块未提交，记录 {error.get('clientKey', '未知')}，"
                                f"字段 {error.get('fieldId', '未知')}，错误码 {error.get('code', 'unknown')}")
        state["next"] = index + 1
        write_state(path, state)
        progress(f"已确认 {index + 1}/{len(chunks)} 块")


def main():
    parser = argparse.ArgumentParser(description="预检或续跑 CSV 创建导入")
    parser.add_argument("--url", required=True)
    parser.add_argument("--tenant", required=True)
    parser.add_argument("--username", required=True)
    parser.add_argument("--schema", required=True)
    parser.add_argument("--csv", type=Path, required=True)
    parser.add_argument("--mapping", type=Path, required=True)
    parser.add_argument("--state", type=Path)
    parser.add_argument("--chunk-size", type=int, default=25, choices=range(1, 101), metavar="1..100")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    client = None
    try:
        if args.apply and not args.state:
            raise ImportFailure("执行导入须提供 --state")
        if args.state and args.state.resolve() in (args.csv.resolve(), args.mapping.resolve()):
            raise ImportFailure("状态文件不能覆盖输入文件")
        with args.csv.open("rb") as stream:
            content = stream.read(32 * 1024 * 1024 + 1)
        if len(content) > 32 * 1024 * 1024:
            raise ImportFailure("CSV 超过 32 MiB 预检预算")
        with args.mapping.open("rb") as stream:
            mapping_bytes = stream.read(65537)
        if len(mapping_bytes) > 65536:
            raise ImportFailure("字段映射超过 64 KiB")
        mapping = json.loads(mapping_bytes)
        client = Client(args.url)
        session = client.login(args.tenant, args.username, getpass.getpass("密码："))
        user = session["user"]
        record = client.request("GET", "/api/metadata/definitions/" + quote(args.schema, safe=""))
        if record["definition"]["id"] != args.schema or record["definition"]["tenantId"] != user["tenantId"]:
            raise ImportFailure("返回的 Schema 或租户与请求不一致")
        csv.field_size_limit(4 * 1024 * 1024)
        chunks = prepare(content, mapping, record, args.chunk_size)
        print(f"预检通过，共 {sum(map(len, chunks))} 条记录，{len(chunks)} 块")
        if args.apply:
            identity = {"origin": client.base_url, "tenant": user["tenantId"], "subject": user["id"],
                        "schema": args.schema, "etag": record["etag"], "chunkSize": args.chunk_size,
                        "csv": hashlib.sha256(content).hexdigest(), "mapping": hashlib.sha256(json_bytes(mapping)).hexdigest()}
            apply(client, args.state, identity, chunks)
    except ImportFailure as error:
        print(error, file=sys.stderr)
        return 1
    except (OSError, ValueError, TypeError, KeyError, csv.Error) as error:
        print(f"无法处理导入文件或状态（{type(error).__name__}）", file=sys.stderr)
        return 1
    finally:
        if client is not None:
            try:
                client.logout()
            except ImportFailure:
                pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
