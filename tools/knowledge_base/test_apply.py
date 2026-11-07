"""Recovery and ownership checks without creating database test objects."""
from copy import deepcopy
from pathlib import Path
import tempfile
import unittest
import uuid

from .apply import Importer, State
from .blueprint import BLUEPRINT
from .client import ApiFailure, ImportFailure
from .__main__ import protect_source


class MemoryAPI:
    def __init__(self):
        self.rows = {}
        self.creates = self.patches = 0
        self.lose_create_response = False

    def query(self, schema_id, fields, where):
        return [deepcopy(row) for (schema, _), row in self.rows.items() if schema == schema_id and row["values"].get(where["field"]) == where["value"]]

    def request(self, method, path, body=None, etag=None):
        parts = path.split("/")
        schema = parts[4]
        if method == "POST":
            self.creates += 1
            row = {"id": str(uuid.uuid4()), "schemaId": schema, "values": deepcopy(body["values"]), "etag": str(uuid.uuid4())}
            self.rows[schema, row["id"]] = row
            if self.lose_create_response:
                self.lose_create_response = False
                raise ImportFailure("模拟：服务已提交，响应丢失")
            return deepcopy(row)
        row = self.rows[schema, parts[6]]
        if method == "PATCH":
            if etag != row["etag"]:
                raise ApiFailure(412, "precondition_failed", "内容已改变")
            self.patches += 1
            row["values"].update(deepcopy(body["values"]))
            row["etag"] = str(uuid.uuid4())
        return deepcopy(row)


class ImportRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "state.json"
        self.context = {"url": "http://127.0.0.1:5173", "tenant": "unit", "vault": "unit-vault", "scope": "Ontology"}
        self.api = MemoryAPI()
        self.importer = self.make_importer()
        self.data = {"source_key": "note-source", "library_key": "vault", "source_locator": "Ontology/note.md", "source_fingerprint": "a" * 64, "available": True, "library_id": str(uuid.uuid4()), "title": "初始标题", "path": "Ontology/note.md", "raw_markdown": "原文\r\n", "raw_yaml": "", "content_hash": "b" * 64, "byte_length": 8, "frontmatter_status": "absent"}

    def make_importer(self):
        importer = Importer(self.api, State(self.path, self.context), BLUEPRINT)
        for item in BLUEPRINT["schemas"]:
            schema = deepcopy(item["schema"])
            for prop in schema["properties"]:
                prop["id"] = prop.pop("key")
            importer.definitions[item["key"]] = {"id": item["key"], "schema": schema}
        return importer

    def test_lost_response_resumes_without_duplicate_or_changing_identity(self):
        self.api.lose_create_response = True
        with self.assertRaises(ImportFailure):
            self.importer._upsert("notes", "note-source", self.data)
        original = next(iter(self.api.rows.values()))
        resumed = self.make_importer()._upsert("notes", "note-source", self.data)
        self.assertEqual(original["id"], resumed["id"])
        self.assertEqual(1, self.api.creates)

    def test_unchanged_import_preserves_sync_time_and_etag(self):
        original = self.importer._upsert("notes", "note-source", self.data)
        reread = self.importer._upsert("notes", "note-source", self.data)
        self.assertEqual(original, reread)
        self.assertEqual(0, self.api.patches)

    def test_external_edit_blocks_source_update(self):
        original = self.importer._upsert("notes", "note-source", self.data)
        self.api.rows["notes", original["id"]]["values"]["title"] = "系统中的人工修改"
        self.api.rows["notes", original["id"]]["etag"] = "external-edit"
        with self.assertRaises(ImportFailure):
            self.importer._upsert("notes", "note-source", {**self.data, "title": "来源也更新"})
        self.assertEqual(0, self.api.patches)

    def test_source_update_keeps_identity_and_explicit_null_clears_target(self):
        data = {"source_key": "reference", "available": True, "target_note_id": str(uuid.uuid4()), "raw_text": "[[原目标]]"}
        original = self.importer._upsert("references", "reference", data)
        updated = self.importer._upsert("references", "reference", {**data, "target_note_id": None, "raw_text": "[[失效目标]]"})
        self.assertEqual(original["id"], updated["id"])
        self.assertIsNone(updated["values"]["target_note_id"])

    def test_state_loss_refuses_to_overwrite_different_existing_content(self):
        self.importer._upsert("notes", "note-source", self.data)
        self.path.unlink()
        with self.assertRaises(ImportFailure):
            self.make_importer()._upsert("notes", "note-source", {**self.data, "title": "变动"})
        self.assertEqual(0, self.api.patches)

    def test_incomplete_scan_performs_no_model_or_instance_writes(self):
        self.importer.ensure_models = lambda: self.fail("不完整扫描不得构建模型")
        with self.assertRaises(ImportFailure):
            self.importer.apply_scan({"complete": False})
        self.assertEqual(0, self.api.creates)

    def test_state_cannot_be_reused_across_tenants(self):
        self.importer.state.save()
        with self.assertRaises(ImportFailure):
            State(self.path, {**self.context, "tenant": "another"})

    def test_possible_source_rename_waits_before_any_model_or_instance_write(self):
        self.importer.state.data["entities"]["old-path"] = {"schema": "notes", "id": "old-identity", "available": True}
        self.importer.ensure_models = lambda: self.fail("疑似重命名不得先写模型")
        with self.assertRaisesRegex(ImportFailure, "重命名"):
            self.importer.apply_scan({"complete": True, "entities": {"notes": [{"source_key": "new-path"}]}})
        self.assertEqual(0, self.api.creates)

    def test_outputs_cannot_write_inside_source_vault(self):
        with self.assertRaises(ImportFailure):
            protect_source(Path(self.temp.name), Path(self.temp.name) / "somewhere" / "state.json")


if __name__ == "__main__":
    unittest.main()
