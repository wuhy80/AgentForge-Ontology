"""Run with: python -m tools.knowledge_base --vault <path> [--apply]."""
from __future__ import annotations

import argparse
from contextlib import contextmanager
import getpass
import json
import os
from pathlib import Path
import sys

from .apply import Importer, State, digest
from .blueprint import BLUEPRINT
from .client import Client, ImportFailure


def protect_source(vault: Path, destination: Path):
    if destination.resolve().is_relative_to(vault.resolve()):
        raise ImportFailure("扫描产物与同步状态必须保存在源知识库之外，保持源库只读")


def protect_destinations(vault: Path, state: Path | None, output: Path | None, credentials: Path | None):
    destinations = [p for p in (state, output) if p is not None]
    if state:
        destinations += [state.with_name(state.name + suffix) for suffix in (".tmp", ".lock")]
    resolved = [p.resolve() for p in destinations]
    for path in destinations:
        protect_source(vault, path)
    if len(set(resolved)) != len(resolved):
        raise ImportFailure("扫描产物、状态、临时文件及锁文件不能使用相同路径")
    if credentials and credentials.resolve() in resolved:
        raise ImportFailure("输出文件不能覆盖账号凭据文件")


@contextmanager
def state_lock(path: Path):
    """OS lock releases even on process failure; retained file is harmless."""
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.with_name(path.name + ".lock").open("a+b") as handle:
        handle.seek(0, 2)
        if handle.tell() == 0:
            handle.write(b"0")
            handle.flush()
        handle.seek(0)
        try:
            if os.name == "nt":
                import msvcrt
                msvcrt.locking(handle.fileno(), msvcrt.LK_NBLCK, 1)
            else:
                import fcntl
                fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            raise ImportFailure("同一状态已有导入进程运行，请稍后重试") from None
        try:
            yield
        finally:
            handle.seek(0)
            if os.name == "nt":
                import msvcrt
                msvcrt.locking(handle.fileno(), msvcrt.LK_UNLCK, 1)
            else:
                import fcntl
                fcntl.flock(handle.fileno(), fcntl.LOCK_UN)


def verify(importer: Importer, scan: dict):
    """Read current values and compare every imported source field, not counts."""
    records = importer.state.data["entities"]
    verified = {}
    for schema_key, envelopes in scan["entities"].items():
        for envelope in envelopes:
            source_key = envelope["source_key"]
            record = records.get(source_key)
            if not record or not record.get("id"):
                raise ImportFailure("验证缺少来源身份映射，请先完整导入")
            schema_id = importer.definitions[schema_key]["id"]
            current = importer.client.request("GET", f"/api/data/schemas/{schema_id}/instances/{record['id']}")
            data = dict(envelope["data"])
            for key, target_key in envelope.get("bindings", {}).items():
                data[key] = records[target_key]["id"]
            for key, prop in importer._properties(schema_key).items():
                if key != "synced_at" and key not in data and prop.get("nullable"):
                    data[key] = None
            desired = importer._wire(schema_key, data)
            if any(current["values"].get(key) != value for key, value in desired.items()):
                raise ImportFailure(f"{schema_key} 回读与来源不同，停止报告成功")
            verified[schema_key] = verified.get(schema_key, 0) + 1
    return verified


def main(argv=None):
    parser = argparse.ArgumentParser(description="个人知识库模型与只读源文件导入；默认仅扫描")
    parser.add_argument("--vault", required=True, type=Path, help="源知识库根目录（只读）")
    parser.add_argument("--scope", default="Ontology", help="白名单内容目录；默认 Ontology")
    parser.add_argument("--library-key", default="obsidian-vault", help="稳定来源库标识，与本机路径和扫描范围分开")
    parser.add_argument("--url", default="http://127.0.0.1:5173", help="工作台同源地址")
    parser.add_argument("--credentials", type=Path, help="本地账号 JSON；未指定时交互输入账号密码")
    parser.add_argument("--tenant", help="租户；可从账号文件读取")
    parser.add_argument("--username", help="账号；可从账号文件读取")
    parser.add_argument("--state", type=Path, help="忽略目录中的同步映射；按服务/租户/来源/范围隔离")
    parser.add_argument("--output", type=Path, help="可选完整扫描 JSON，包含笔记原文，建议置于 .cache")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--apply", action="store_true", help="通过公开 API 实际创建模型并导入")
    mode.add_argument("--verify", action="store_true", help="仅登录和回读验证已有导入，不写业务数据")
    parser.add_argument("--models-only", action="store_true", help="配合 --apply，只创建/生效模型")
    args = parser.parse_args(argv)
    if args.models_only and not args.apply:
        parser.error("--models-only 需配合 --apply")
    try:
        from .scanner import scan_vault
        scan = scan_vault(args.vault, scope=args.scope, library_key=args.library_key)
        protect_destinations(args.vault, args.state, args.output, args.credentials)
        if args.output and not (args.apply or args.verify):
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(json.dumps(scan, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        summary = {"scope": args.scope, "complete": scan["complete"], "counts": scan["counts"], "diagnostics": scan["diagnostics"]}
        if not args.apply and not args.verify:
            print(json.dumps(summary, ensure_ascii=False, indent=2))
            return 0 if scan["complete"] else 1
        if not scan["complete"]:
            raise ImportFailure("扫描不完整，请先运行默认扫描并修复错误；本次未登录或写入")
        credentials = json.loads(args.credentials.read_text(encoding="utf-8-sig")) if args.credentials else {}
        tenant = args.tenant or credentials.get("Tenant") or credentials.get("tenant")
        username = args.username or credentials.get("Username") or credentials.get("username")
        password = credentials.get("Password") or credentials.get("password") or os.getenv("ONTOLOGY_PASSWORD")
        if not tenant:
            tenant = input("租户：").strip()
        if not username:
            username = input("账号：").strip()
        if not password:
            password = getpass.getpass("密码：")
        context = {"url": args.url.rstrip("/"), "tenant": tenant, "vault": str(args.vault.resolve()), "scope": args.scope, "library_key": args.library_key}
        state_path = args.state or Path(__file__).resolve().parents[2] / ".cache" / "knowledge-base" / (digest(context)[:20] + ".json")
        protect_destinations(args.vault, state_path, args.output, args.credentials)
        if args.output:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(json.dumps(scan, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        with state_lock(state_path):
            state = State(state_path, context)
            client = Client(args.url)
            client.login(tenant, username, password)
            try:
                importer = Importer(client, state, BLUEPRINT)
                if args.verify:
                    for item in BLUEPRINT["schemas"]:
                        binding = state.data["definitions"].get(item["key"])
                        if not binding:
                            raise ImportFailure("验证缺少模型映射，请先导入")
                        importer.definitions[item["key"]] = client.request("GET", "/api/metadata/definitions/" + binding["id"])["definition"]
                    result = {"verified": verify(importer, scan)}
                else:
                    result = importer.apply_scan(scan, models_only=args.models_only)
                    if not args.models_only:
                        result["verified"] = verify(importer, scan)
                result["state"] = str(state_path)
                print(json.dumps(result, ensure_ascii=False, indent=2))
            finally:
                try:
                    client.logout()
                except ImportFailure:
                    print("导入会话注销未确认，服务端会话仍按有效期管理。", file=sys.stderr)
        return 0
    except (ImportFailure, ValueError, OSError) as error:
        print(f"知识库操作停止：{error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
