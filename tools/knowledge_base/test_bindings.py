"""Explicit-source reuse and saved-manifest verification without live writes."""
from copy import deepcopy
from unittest.mock import patch
import unittest

from .apply import Importer, definition_signature
from .client import ImportFailure
from .test_publication import BLUEPRINT, PublicationAPI, SavedState


def relation_blueprint():
    blueprint = deepcopy(BLUEPRINT)
    blueprint["links"] = [{
        "key": "library_notes", "name": "Library notes", "group": "content",
        "source_schema": "library", "target_schema": "notes", "forwardName": "Contains notes",
        "reverseName": "Belongs to library", "cardinality": "1:N", "onDelete": "restrict",
    }]
    return blueprint


class BindingReuseTests(unittest.TestCase):
    def prepared(self):
        api, state, blueprint = PublicationAPI(), SavedState(), relation_blueprint()
        Importer(api, state, blueprint).ensure_models()
        for record in api.current.values():
            schema = record["definition"].get("schema")
            if schema:
                schema["mode"] = "db_2_schema"
                schema["storageBinding"] = {"dataSourceId": "knowledge-source", "table": record["definition"]["id"], "idColumn": "id"}
                for prop in schema["properties"]:
                    prop["mapping"] = {"dataSourceId": "knowledge-source", "column": "source_key"}
            else:
                record["definition"]["link"]["storageBinding"] = {"mode": "foreign_key", "dataSourceId": "knowledge-source", "foreignKeySide": "target", "foreignKeyColumn": "library_id"}
        api.calls.clear()
        return api, state, blueprint

    def test_models_only_reuses_published_explicit_sources_and_links_without_overwriting(self):
        api, state, blueprint = self.prepared()
        before = deepcopy(api.current)
        importer = Importer(api, state, blueprint)
        result = importer.ensure_models()
        self.assertEqual(1, result["linkDefinitions"])
        self.assertEqual(before, api.current)
        self.assertEqual([], api.calls)
        self.assertEqual("db_2_schema", importer.definitions["notes"]["schema"]["mode"])
        self.assertEqual("knowledge-source", importer.definitions["notes"]["schema"]["storageBinding"]["dataSourceId"])

    def test_logic_change_still_rejects_reuse_of_a_bound_link(self):
        api, state, blueprint = self.prepared()
        relation = next(record for record in api.current.values() if record["definition"]["kind"] == "link")
        relation["definition"]["link"]["cardinality"] = "N:M"
        with self.assertRaisesRegex(ImportFailure, "蓝图不一致"):
            Importer(api, state, blueprint).ensure_models()
        self.assertFalse(any(method != "GET" for method, _, _ in api.calls))

    def test_user_draft_is_preserved_even_when_current_binding_is_compatible(self):
        api, state, blueprint = self.prepared()
        api.drafts["definition-2"] = deepcopy(api.current["definition-2"])
        api.drafts["definition-2"]["status"] = "draft"
        with self.assertRaisesRegex(ImportFailure, "保留已有草稿"):
            Importer(api, state, blueprint).ensure_models()
        self.assertEqual([], api.calls)
        self.assertIn("definition-2", api.drafts)

    def test_import_preflight_stops_before_creating_missing_models_or_instances(self):
        api, state, blueprint = self.prepared()
        # There could be a newly added Link after migration; it must not be
        # created before the tool notices that data writes are unsupported.
        api.current.pop("definition-3")
        state.data["definitions"].pop("library_notes")
        importer = Importer(api, state, blueprint)
        importer.ensure_models = lambda: self.fail("Preflight must run before any modeling operation")
        with self.assertRaisesRegex(ImportFailure, "显式数据源"):
            importer.apply_scan({"complete": True, "entities": {"notes": []}})
        self.assertEqual([], api.calls)

    def test_direct_upsert_also_refuses_mapped_instance_writes(self):
        api, state, blueprint = self.prepared()
        importer = Importer(api, state, blueprint)
        importer.definitions["notes"] = deepcopy(api.current["definition-2"]["definition"])
        with self.assertRaisesRegex(ImportFailure, "写入尚未开放"):
            importer._upsert("notes", "source", {})
        self.assertEqual([], api.calls)

    def test_strict_recovery_comparison_keeps_physical_bindings(self):
        api, _, _ = self.prepared()
        current = deepcopy(api.current["definition-2"])
        changed = deepcopy(current["definition"])
        changed["schema"]["storageBinding"]["dataSourceId"] = "another-source"
        self.assertNotEqual(definition_signature(current["definition"]), definition_signature(changed))
        self.assertFalse(Importer._reusable_definition(current, changed))


class ManifestVerificationTests(unittest.TestCase):
    def fixture(self):
        blueprint = {"category_name": "Knowledge", "groups": [{"key": "content", "name": "Content"}], "links": [], "schemas": [{
            "key": "notes", "group": "content", "name": "Notes", "indexes": [],
            "schema": {"mode": "schema_2_db", "storageLayout": "single", "properties": [
                {"key": "source_key", "name": "Source", "dataType": "string", "required": True, "nullable": False},
                {"key": "body", "name": "Body", "dataType": "longtext", "required": True, "nullable": False},
                {"key": "size", "name": "Size", "dataType": "int64", "required": True, "nullable": False},
                {"key": "optional", "name": "Optional", "dataType": "text", "nullable": True},
                {"key": "synced_at", "name": "Synced", "dataType": "timestamp", "nullable": True},
            ]},
        }]}
        definition = Importer._schema_payload(blueprint["schemas"][0], "category", "group")
        definition.update(id="notes", tenantId="tenant")
        definition["schema"].update(mode="db_2_schema", storageBinding={"dataSourceId": "source", "table": "notes", "idColumn": "id"})
        for prop in definition["schema"]["properties"]:
            prop["mapping"] = {"dataSourceId": "source", "column": prop["name"].lower()}
        property_ids = {prop["name"]: prop["id"] for prop in definition["schema"]["properties"]}
        record = {"status": "active", "etag": "current", "definition": definition}
        instance = {"schemaId": "notes", "id": "note-1", "etag": "row", "values": {
            property_ids["Source"]: "note-source", property_ids["Body"]: "\ufeff---\r\n完整正文\r\n", property_ids["Size"]: "9007199254740993", property_ids["Optional"]: None, property_ids["Synced"]: "2026-11-07T00:00:00Z",
        }}
        class ReadOnlyAPI:
            def __init__(self):
                self.calls = []

            def request(self, method, path, body=None, etag=None):
                self.calls.append((method, path))
                if method != "GET":
                    raise AssertionError("Read-only verification attempted a business write")
                return deepcopy(record if path == "/api/metadata/definitions/notes" else instance)
        state = SavedState({"context": {"scope": "Ontology", "library_key": "vault"}, "category": {"id": "category"}, "groups": {"content": "group"}, "definitions": {"notes": {"id": "notes"}}, "entities": {"note-source": {"id": "note-1", "schema": "notes"}}})
        state.save = lambda: self.fail("Read-only verification must not rewrite sync state")
        api = ReadOnlyAPI()
        importer = Importer(api, state, blueprint)
        manifest = {"format": "agentforge.knowledge-base.scan.v1", "complete": True, "scope": "Ontology", "library_key": "vault", "counts": {"notes": 1}, "entities": {"notes": [{"source_key": "note-source", "data": {"source_key": "note-source", "body": "\ufeff---\r\n完整正文\r\n", "size": "9007199254740993"}}]}}
        return importer, api, manifest, instance, property_ids

    def test_verifies_saved_manifest_full_longtext_and_exact_numbers_without_scanning(self):
        importer, api, manifest, _, _ = self.fixture()
        with patch("tools.knowledge_base.scanner.scan_vault", side_effect=AssertionError("Must not scan source files")) as scan:
            result = importer.verify_existing_manifest(manifest)
        scan.assert_not_called()
        self.assertEqual({"notes": 1}, result["verified"])
        self.assertEqual({"GET"}, {method for method, _ in api.calls})
        self.assertTrue(result["readOnly"])
        self.assertFalse(result["sourceRescanned"])
        self.assertEqual(0, result["businessWrites"])

    def test_changed_body_or_missing_nullable_response_field_cannot_report_success(self):
        for change in ("body", "missing_nullable"):
            with self.subTest(change=change):
                importer, api, manifest, instance, ids = self.fixture()
                if change == "body":
                    instance["values"][ids["Body"]] += "corruption"
                else:
                    del instance["values"][ids["Optional"]]
                with self.assertRaisesRegex(ImportFailure, "回读与既有扫描清单不同"):
                    importer.verify_existing_manifest(manifest)
                self.assertTrue(all(method == "GET" for method, _ in api.calls))

    def test_incomplete_or_other_library_manifest_stops_before_api_reads(self):
        for alteration in ({"complete": False}, {"library_key": "another"}):
            with self.subTest(alteration=alteration):
                importer, api, manifest, _, _ = self.fixture()
                manifest.update(alteration)
                with self.assertRaises(ImportFailure):
                    importer.verify_existing_manifest(manifest)
                self.assertEqual([], api.calls)


if __name__ == "__main__":
    unittest.main()
