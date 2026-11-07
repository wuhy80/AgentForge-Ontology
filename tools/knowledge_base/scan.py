"""Read-only CLI: python -m tools.knowledge_base.scan VAULT_ROOT [--scope Ontology]."""

import argparse
import json
import sys

if __package__:
    from .scanner import scan_vault
else:
    from scanner import scan_vault


def main(argv=None):
    parser = argparse.ArgumentParser(description="只读扫描显式知识库范围，不请求 API 或数据库")
    parser.add_argument("vault_root")
    parser.add_argument("--scope", default="Ontology")
    parser.add_argument("--library-key", default=None, help="稳定来源库标识；默认目录名，不随scope变化")
    args = parser.parse_args(argv)
    try:
        result = scan_vault(args.vault_root, args.scope, library_key=args.library_key)
    except (OSError, ValueError) as error:
        parser.error(str(error))
    sys.stdout.reconfigure(encoding="utf-8")
    json.dump(result, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return 0 if result["complete"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
