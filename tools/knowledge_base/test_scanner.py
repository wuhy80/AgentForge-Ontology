"""Offline fixtures only: python -m unittest tools.knowledge_base.test_scanner -v."""

import hashlib
import json
import os
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch

from tools.knowledge_base.blueprint import BLUEPRINT, definition_inputs, field
from tools.knowledge_base.scanner import scan_vault


class ScannerTests(unittest.TestCase):
    def setUp(self):
        self.temp = TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        (self.root / "Ontology").mkdir()

    def write(self, path, content):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content.encode("utf-8") if isinstance(content, str) else content)
        return target

    def scan(self, scope="Ontology", **kwargs):
        return scan_vault(self.root, scope, library_key="fixture-vault", **kwargs)

    @staticmethod
    def references(scan, kind=None):
        return [entry for entry in scan["entities"]["references"] if kind is None or entry["data"]["reference_kind"] == kind]

    def test_preserves_original_bytes_yaml_dates_and_distinct_multivalues(self):
        raw = ('\ufeff---\r\ntags: [Ontology, 技术/数据库, "a,b"]\r\n'
               'aliases: ["对象模型", "模型别名"]\r\n'
               'source:\r\n  - https://Example.com/page?x=1&x=2#section\r\n  - https://example.org/second\r\n'
               'created: 2026-06-05\r\nupdated: 2026-06-06T12:34:56+08:00\r\n'
               'status: 初稿\r\ntype: study\r\n---\r\n# 业务标题\r\n正文 #内联标签\r\n')
        self.write("Ontology/对象.md", raw)
        result = self.scan()
        self.assertTrue(result["complete"])
        note = result["entities"]["notes"][0]
        self.assertEqual(note["data"]["raw_markdown"].encode("utf-8"), raw.encode("utf-8"))
        self.assertEqual(note["data"]["content_hash"], hashlib.sha256(raw.encode()).hexdigest())
        self.assertEqual(note["data"]["byte_length"], str(len(raw.encode())))
        self.assertEqual(note["data"]["raw_yaml"], raw.split("---\r\n")[1])
        self.assertEqual(note["data"]["source_created"], "2026-06-05")
        self.assertEqual(note["data"]["source_updated"], "2026-06-06T12:34:56+08:00")
        self.assertEqual(note["data"]["title"], "业务标题")
        self.assertEqual(note["aliases"], ["对象模型", "模型别名"])
        self.assertEqual(note["tags"], ["Ontology", "技术/数据库", "a,b"])
        self.assertEqual({entry["data"]["raw_value"] for entry in result["entities"]["tags"]}, {"Ontology", "技术/数据库", "a,b", "内联标签"})
        self.assertEqual({entry["data"]["raw_url"] for entry in result["entities"]["resources"]}, set(note["source_values"]))
        self.assertTrue(all(entry["data"]["normalized_url"] == entry["data"]["raw_url"] for entry in result["entities"]["resources"]))
        for entries in result["entities"].values():
            for entry in entries:
                self.assertEqual(entry["source_key"], entry["data"]["source_key"])
                self.assertNotIn("synced_at", entry["data"])
                self.assertNotIn(None, entry["data"].values())
                if entry is not result["entities"]["knowledge_base"][0]:
                    self.assertEqual(entry["bindings"]["library_id"], result["entities"]["knowledge_base"][0]["source_key"])

    def test_skips_all_code_comment_forms_and_preserves_wiki_locations(self):
        text = ("# 当前\n```markdown\n[[fenced]] https://hidden.invalid\n```\n"
                "~~~\n![[tilde]]\n~~~\n    [[indented]]\n\t[[tabbed]]\n"
                "`[[inline]]` `` ![[ticks]] ``\n<!-- [[html]]\nhttps://hidden.invalid -->\n"
                "%% [[obsidian]]\n![[hidden]] %%\n\\[[escaped]]\n"
                "可见 [[Target#第一节#^block-1\\|显示别名]] 和 ![[image.png|300x200]]\n"
                "同页 [[#当前]]，块 [[#^here]]\n")
        self.write("Ontology/Source.md", text)
        self.write("Ontology/Target.md", "# Target\n")
        self.write("Ontology/image.png", b"\x89PNG\r\n")
        result = self.scan()
        refs = self.references(result)
        self.assertEqual(len(refs), 4)
        ref = next(entry for entry in refs if entry["data"]["alias"] == "显示别名")
        data = ref["data"]
        self.assertEqual(data["raw_text"], "[[Target#第一节#^block-1\\|显示别名]]")
        self.assertEqual(data["target_text"], "Target#第一节#^block-1")
        self.assertEqual(data["heading"], "第一节")
        self.assertEqual(data["block"], "block-1")
        self.assertEqual(data["resolution"], "resolved")
        start, end = int(data["offset"]), int(data["end_offset"])
        self.assertEqual(text[start:end], data["raw_text"])
        self.assertEqual(int(data["line"]), text.count("\n", 0, start) + 1)
        self.assertEqual(int(data["column"]), start - text.rfind("\n", 0, start))
        image = next(entry for entry in refs if entry["data"]["reference_kind"] == "embed")
        self.assertEqual(json.loads(image["data"]["parameters"]), {"display": "300x200"})
        self.assertIn("target_attachment_id", image["bindings"])
        self.assertNotIn("target_attachment_id", image["data"])
        self.assertEqual(result["counts"]["resources"], 0)
        self.assertEqual(result["counts"]["attachments"], 1)

    def test_duplicate_names_aliases_and_case_collisions_never_pick_first(self):
        self.write("Ontology/A/Duplicate.md", "---\naliases: [共享别名]\n---\n# A\n")
        self.write("Ontology/B/Duplicate.md", "---\naliases: [共享别名]\n---\n# B\n")
        self.write("Ontology/Other.md", "---\naliases: [Duplicate]\n---\n# Other\n")
        self.write("Ontology/Source.md", "[[Duplicate]] [[共享别名]] [[Ontology/A/Duplicate]] [[./A/Duplicate.md]]")
        result = self.scan()
        refs = self.references(result)
        ambiguous = [entry for entry in refs if entry["data"]["resolution"] == "ambiguous"]
        self.assertEqual(len(ambiguous), 2)
        self.assertEqual(len(json.loads(next(entry["data"]["candidates"] for entry in ambiguous if entry["data"]["target_text"] == "Duplicate"))), 3)
        for entry in ambiguous:
            self.assertFalse(any(key.startswith("target_") for key in entry["bindings"]))
        resolved = [entry for entry in refs if entry["data"]["resolution"] == "resolved"]
        self.assertEqual(len(resolved), 2)
        self.assertEqual(resolved[0]["bindings"]["target_note_id"], resolved[1]["bindings"]["target_note_id"])

    def test_scope_never_reads_other_directories_private_files_or_cache(self):
        self.write("Other/Secret.md", "must not read")
        self.write("Ontology/.hidden/Hidden.md", "must not read")
        self.write("Ontology/private/Private.md", "must not read")
        self.write("Ontology/私密/Private.md", "must not read")
        self.write("Ontology/cache/Cached.md", "must not read")
        self.write("Ontology/_site/Built.md", "must not read")
        self.write("Ontology/AGENTS.md", "must not read")
        self.write("Ontology/Source.md", "[[Other/Secret]] [[../Other/Secret]] [[Missing]] [[Ontology/.hidden/Hidden]]")
        original_read = Path.read_bytes
        reads = []

        def read(path):
            reads.append(path.relative_to(self.root).as_posix())
            return original_read(path)

        with patch.object(Path, "read_bytes", read):
            result = self.scan()
        self.assertEqual(reads, ["Ontology/Source.md"])
        self.assertEqual(result["counts"]["notes"], 1)
        self.assertEqual(sorted(entry["data"]["resolution"] for entry in self.references(result)), ["out_of_scope", "out_of_scope", "out_of_scope", "unresolved"])
        self.assertTrue(result["complete"])

    def test_source_urls_one_by_one_and_yaml_wikilinks_are_not_duplicated(self):
        self.write("Ontology/Target.md", "# Target\n")
        self.write("Ontology/Source.md", '---\nsource:\n - "[[Target|来源模型]]"\n - https://example.org/a?q=x&keep=1\n - 手工整理\n---\n[显示](https://example.org/wiki/Test_(x)?q=2#keep)\nhttps://example.org/bare?q=3).\n[[https://example.org/a?q=4#fragment]]\n')
        result = self.scan()
        refs = self.references(result)
        self.assertEqual(len(refs), 6)
        self.assertEqual({entry["data"]["raw_url"] for entry in result["entities"]["resources"]}, {
            "https://example.org/a?q=x&keep=1", "https://example.org/wiki/Test_(x)?q=2#keep",
            "https://example.org/bare?q=3", "https://example.org/a?q=4#fragment",
        })
        wiki = next(entry for entry in refs if entry["data"]["raw_text"] == "[[Target|来源模型]]")
        self.assertEqual(wiki["data"]["source_section"], "frontmatter.source")
        self.assertIn("target_note_id", wiki["bindings"])
        source = next(entry for entry in refs if entry["data"]["target_text"] == "手工整理")
        self.assertEqual(source["data"]["resolution"], "unresolved")

    def test_missing_and_unsafe_yaml_stays_raw_and_marks_incomplete(self):
        examples = [
            "---\ntags: [unclosed\n---\n# body\n",
            "---\ntags: [a]\ntags: [b]\n---\n",
            "---\nunsafe: !!python/object/apply:os.system ['invalid']\n---\n",
            "---\nmissing closing\n",
            "---\n- not a mapping\n---\n",
        ]
        for index, text in enumerate(examples):
            self.write(f"Ontology/Bad{index}.md", text)
        result = self.scan()
        self.assertFalse(result["complete"])
        self.assertEqual(len(result["diagnostics"]), len(examples))
        self.assertTrue(all(entry["data"]["frontmatter_status"] == "invalid" for entry in result["entities"]["notes"]))
        self.assertEqual({entry["data"]["raw_markdown"] for entry in result["entities"]["notes"]}, set(examples))

    def test_dates_explicit_timestamp_tags_remain_strings(self):
        self.write("Ontology/Date.md", "---\ncreated: !!timestamp 2026-01-02\nupdated: '2026-01-03'\n---\n")
        result = self.scan()
        self.assertTrue(result["complete"])
        note = result["entities"]["notes"][0]
        self.assertEqual(note["data"]["source_created"], "2026-01-02")
        self.assertEqual(note["data"]["source_updated"], "2026-01-03")

    def test_invalid_multivalues_report_incomplete_instead_of_coercing(self):
        self.write("Ontology/Types.md", "---\ntags: [valid, {invalid: object}]\nsource: [https://example.org, 123]\naliases: [yes]\ncreated: 123\n---\n")
        result = self.scan()
        self.assertFalse(result["complete"])
        self.assertEqual({item["code"] for item in result["diagnostics"]}, {"invalid_tags", "invalid_sources", "invalid_aliases", "unsupported_metadata_scalar"})
        self.assertEqual([entry["data"]["raw_value"] for entry in result["entities"]["tags"]], ["valid"])
        self.assertNotIn("source_created", result["entities"]["notes"][0]["data"])

    def test_repeated_mentions_have_distinct_records_and_shared_target(self):
        self.write("Ontology/Tags.md", "---\ntags: [same, same]\nsource: [https://example.org, https://example.org]\n---\n#same #same\n")
        result = self.scan()
        refs = self.references(result)
        self.assertTrue(result["complete"])
        self.assertEqual(len(refs), 6)
        self.assertEqual(len({entry["source_key"] for entry in refs}), 6)
        self.assertEqual(result["counts"]["tags"], 1)
        self.assertEqual(result["counts"]["resources"], 1)
        self.assertEqual(len([association for association in result["associations"] if association["kind"] == "note_tag"]), 1)
        for schema_key, entries in result["entities"].items():
            allowed = {prop["key"] for model in BLUEPRINT["schemas"] if model["key"] == schema_key for prop in model["schema"]["properties"]}
            for entry in entries:
                self.assertFalse(set(entry["data"]) - allowed)
                self.assertLessEqual(len(entry["source_key"]), 80)
                for key in ("offset", "end_offset", "line", "column", "byte_length"):
                    if key in entry["data"]:
                        self.assertIsInstance(entry["data"][key], str)

    def test_stable_source_keys_are_scope_independent_but_not_path_identity(self):
        self.write("Ontology/Sub/Note.md", "# Note\n")
        first = self.scan()
        nested = self.scan("Ontology/Sub")
        self.assertEqual(first["entities"]["knowledge_base"][0]["source_key"], nested["entities"]["knowledge_base"][0]["source_key"])
        self.assertEqual(first["entities"]["notes"][0]["source_key"], nested["entities"]["notes"][0]["source_key"])
        self.assertEqual(first, self.scan())
        original = first["entities"]["notes"][0]["source_key"]
        (self.root / "Ontology/Sub/Note.md").rename(self.root / "Ontology/Sub/Renamed.md")
        self.assertNotEqual(original, self.scan()["entities"]["notes"][0]["source_key"])

    def test_long_markdown_is_not_truncated_and_invalid_utf8_does_not_complete(self):
        raw = "# 长正文\r\n" + "中文正文" * 30000 + "\r\n[[Missing]]\r\n"
        self.write("Ontology/Long.md", raw)
        first = self.scan()
        self.assertTrue(first["complete"])
        self.assertGreater(int(first["entities"]["notes"][0]["data"]["byte_length"]), 65535)
        self.assertEqual(first["entities"]["notes"][0]["data"]["raw_markdown"], raw)
        self.write("Ontology/Invalid.md", b"\xff\xfe")
        second = self.scan()
        self.assertFalse(second["complete"])
        self.assertEqual(second["counts"]["notes"], 1)
        self.assertEqual(second["diagnostics"][0]["code"], "file_read_failed")

    def test_read_failure_prevents_complete_scan(self):
        self.write("Ontology/Fail.md", "# Fail\n")
        with patch.object(Path, "read_bytes", side_effect=PermissionError("denied")):
            result = self.scan()
        self.assertFalse(result["complete"])
        self.assertEqual(result["counts"]["notes"], 0)

    def test_configuration_rejects_root_escape_and_private_scope(self):
        for scope in ("", ".", "..", "../Other", "Ontology/../Other", "C:/Other", "/Other", "Ontology/private", "Other", "技术笔记/虚拟机", "技术笔记/安全"):
            with self.subTest(scope=scope), self.assertRaises(ValueError):
                self.scan(scope)
        with self.assertRaises(ValueError):
            scan_vault(self.root, library_key="")

    def test_actual_export_exclusions_are_not_read(self):
        for path in ("技术笔记/虚拟机/VM.md", "技术笔记/安全/Secret.md", "技术笔记/版本管理/知识库双仓库拆分与多机同步.md"):
            self.write(path, "must not read")
        self.write("技术笔记/版本管理/Allowed.md", "# Allowed\n")
        original_read = Path.read_bytes
        reads = []

        def read(path):
            reads.append(path.relative_to(self.root).as_posix())
            return original_read(path)

        with patch.object(Path, "read_bytes", read):
            result = self.scan("技术笔记")
        self.assertTrue(result["complete"])
        self.assertEqual(reads, ["技术笔记/版本管理/Allowed.md"])

    def test_mutation_during_read_marks_scan_incomplete(self):
        path = self.write("Ontology/Mutable.md", "# Before\n")
        original_read = Path.read_bytes

        def read(candidate):
            value = original_read(candidate)
            path.write_bytes(b"# Changed after read\n")
            return value

        with patch.object(Path, "read_bytes", read):
            result = self.scan()
        self.assertFalse(result["complete"])
        self.assertIn("source_changed_during_scan", {item["code"] for item in result["diagnostics"]})

    def test_case_or_unicode_source_path_collision_marks_incomplete(self):
        self.write("Ontology/Case.md", "# Upper\n")
        second = self.root / "Ontology/case.md"
        if second.exists():
            self.write("Ontology/caf\u00e9.md", "# NFC\n")
            if (self.root / "Ontology/cafe\u0301.md").exists():
                self.skipTest("filesystem cannot hold case or Unicode-normalization duplicates")
            self.write("Ontology/cafe\u0301.md", "# NFD\n")
        else:
            self.write("Ontology/case.md", "# Lower\n")
        result = self.scan()
        self.assertFalse(result["complete"])
        self.assertIn("source_path_collision", {item["code"] for item in result["diagnostics"]})

    def test_symlink_is_excluded_without_following_target(self):
        self.write("Other/Private.md", "must not read")
        try:
            os.symlink(self.root / "Other", self.root / "Ontology/linked", target_is_directory=True)
        except OSError:
            self.skipTest("symlink creation unavailable to this account")
        result = self.scan()
        self.assertEqual(result["counts"]["notes"], 0)
        self.assertEqual(result["diagnostics"][0]["code"], "symlink_excluded")
        with self.assertRaises(ValueError):
            self.scan("Ontology/linked")


class BlueprintTests(unittest.TestCase):
    def test_six_models_and_candidate_links_use_real_contract_without_runtime_claim(self):
        self.assertEqual(len(BLUEPRINT["schemas"]), 6)
        self.assertEqual(len(BLUEPRINT["links"]), 7)
        self.assertEqual({group["name"] for group in BLUEPRINT["groups"]}, {"内容资产", "知识组织", "引用溯源"})
        group_ids = {group["key"]: group["key"] + "-id" for group in BLUEPRINT["groups"]}
        schema_ids = {schema["key"]: schema["key"] + "-id" for schema in BLUEPRINT["schemas"]}
        inputs = definition_inputs("category-id", group_ids, schema_ids)
        self.assertEqual(len(inputs), 13)
        self.assertTrue(all(entry["payload"]["categoryId"] == "category-id" for entry in inputs))
        self.assertTrue(all(not link["runtime_supported"] for link in BLUEPRINT["links"]))
        for schema in BLUEPRINT["schemas"]:
            props = {prop["key"]: prop for prop in schema["schema"]["properties"]}
            self.assertTrue(props["source_key"]["required"])
            self.assertEqual(props["source_key"]["dataType"], "string")
            self.assertTrue(all(prop["nullable"] != prop["required"] for prop in props.values()))
            self.assertEqual(schema["indexes"][0], {"name": "来源键唯一", "unique": True, "fields": ["source_key"]})
        self.assertTrue(all("key" not in prop for entry in inputs[:6] for prop in entry["payload"]["schema"]["properties"]))
        note = next(schema for schema in BLUEPRINT["schemas"] if schema["key"] == "notes")
        self.assertEqual(next(prop["dataType"] for prop in note["schema"]["properties"] if prop["key"] == "raw_markdown"), "longtext")
        self.assertEqual(field("test", "测试")["nullable"], True)


if __name__ == "__main__":
    unittest.main()
