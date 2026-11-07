"""Publication recovery regressions using an in-memory API and saved state."""
from copy import deepcopy
import unittest

from .apply import Importer
from .client import ApiFailure, ImportFailure


BLUEPRINT = {
    "category_name": "Knowledge",
    "groups": [{"key": "content", "name": "Content"}],
    "schemas": [
        {
            "key": key,
            "name": name,
            "group": "content",
            "schema": {
                "mode": "schema_2_db",
                "storageLayout": "single",
                "properties": [{
                    "key": "source_key", "name": "Source key",
                    "dataType": "string", "required": True,
                }],
            },
            "indexes": [{"name": "Unique source", "unique": True, "fields": ["source_key"]}],
        }
        for key, name in [("library", "Library"), ("notes", "Notes")]
    ],
    "links": [],
}


class SavedState:
    def __init__(self, data=None):
        self.data = deepcopy(data) if data is not None else {"definitions": {}, "entities": {}}
        self.saved = deepcopy(self.data)

    def save(self):
        self.saved = deepcopy(self.data)


class PublicationAPI:
    """Model the tenant publication gate and commit-before-activation boundary."""
    def __init__(self):
        self.current = {}
        self.drafts = {}
        self.calls = []
        self.pending = None
        self.next_id = 0
        self.lose_commit_response = False
        self.lose_prepare_response = False
        self.lose_create_response = False

    def catalog(self, path, **filters):
        if path == "/api/metadata/categories":
            return [{"id": "category", "name": "Knowledge"}]
        if path == "/api/metadata/groups":
            return [{"id": "group", "categoryId": "category", "name": "Content"}]
        if path == "/api/metadata/definitions":
            records = self.drafts if filters.get("view") == "draft" else self.current
            return deepcopy(list(records.values()))
        raise AssertionError((path, filters))

    def request(self, method, path, body=None, etag=None):
        self.calls.append((method, path, etag))
        if method == "POST" and path == "/api/metadata/definitions":
            if self.pending is not None:
                raise ApiFailure(409, "publication_in_progress", "Another publication is unfinished")
            self.next_id += 1
            definition = {**deepcopy(body), "id": f"definition-{self.next_id}", "tenantId": "tenant"}
            record = {"definition": definition, "etag": f"candidate-{self.next_id}", "status": "draft"}
            self.drafts[definition["id"]] = record
            if self.lose_create_response:
                self.lose_create_response = False
                raise ImportFailure("Simulated lost model creation response")
            return deepcopy(record)
        definition_id = path.split("/")[4]
        if method == "POST" and path.endswith("/preview"):
            if self.pending is not None:
                raise ApiFailure(409, "publication_in_progress", "Resume the existing publication first")
            return {"statements": []}
        if method == "POST" and path.endswith("/publish"):
            record = self.drafts.get(definition_id) or self.current[definition_id]
            if etag != record["etag"]:
                raise ApiFailure(412, "precondition_failed", "Candidate changed")
            if self.pending is not None and self.pending != definition_id:
                raise ApiFailure(409, "publication_in_progress", "Another publication is unfinished")
            if self.lose_prepare_response:
                self.lose_prepare_response = False
                self.pending = definition_id
                raise ImportFailure("Simulated lost prepare response before Metadata commit")
            self.current[definition_id] = deepcopy(record)
            self.drafts.pop(definition_id, None)
            if self.lose_commit_response:
                self.lose_commit_response = False
                self.pending = definition_id
                # Metadata's current definition is already active; only the
                # publication and tenant gate still await Data activation.
                self.current[definition_id]["status"] = "active"
                raise ImportFailure("Simulated lost response after Metadata commit")
            self.pending = None
            self.current[definition_id]["status"] = "active"
            return {"id": f"publication-{definition_id}", "definitionId": definition_id, "status": "succeeded"}
        if method == "GET":
            return deepcopy(self.current[definition_id])
        raise AssertionError((method, path))


class PublicationRecoveryTests(unittest.TestCase):
    def assert_recovered_before_next_model(self, api):
        recovery = api.calls.index(("POST", "/api/metadata/definitions/definition-1/publish", "candidate-1"))
        next_creation = next(index for index, call in enumerate(api.calls)
                             if call[:2] == ("POST", "/api/metadata/definitions"))
        self.assertLess(recovery, next_creation)
        self.assertIsNone(api.pending)
        self.assertEqual(2, len(api.current))
        self.assertEqual(2, api.next_id, "Recovery must keep the original Schema identity")

    def committed_first_model(self):
        api = PublicationAPI()
        state = SavedState()
        api.lose_commit_response = True
        with self.assertRaises(ImportFailure):
            Importer(api, state, BLUEPRINT).ensure_models()
        self.assertEqual("active", api.current["definition-1"]["status"])
        self.assertEqual("definition-1", api.pending)
        self.assertFalse(api.drafts, "The committed candidate is no longer listed as a draft")
        self.assertEqual("candidate-1", state.saved["definitions"]["library"]["etag"])
        self.assertTrue(state.saved["definitions"]["library"]["publicationPending"])
        return api, state

    def test_live_catalog_with_pending_state_resumes_before_later_model_creation(self):
        api, state = self.committed_first_model()
        api.calls.clear()
        result = Importer(api, state, BLUEPRINT).ensure_models()
        self.assertEqual(2, result["schemas"])
        self.assert_recovered_before_next_model(api)

    def test_restart_after_lost_commit_response_uses_durable_original_etag(self):
        api, previous_state = self.committed_first_model()
        restarted_state = SavedState(previous_state.saved)
        api.calls.clear()
        Importer(api, restarted_state, BLUEPRINT).ensure_models()
        self.assert_recovered_before_next_model(api)
        self.assertEqual("definition-1", restarted_state.saved["definitions"]["library"]["id"])
        self.assertFalse(restarted_state.saved["definitions"]["library"].get("publicationPending"))

    def test_prepared_draft_resumes_publication_without_previewing_busy_mapping(self):
        api = PublicationAPI()
        state = SavedState()
        api.lose_prepare_response = True
        with self.assertRaises(ImportFailure):
            Importer(api, state, BLUEPRINT).ensure_models()
        self.assertTrue(state.saved["definitions"]["library"]["publicationPending"])
        self.assertFalse(api.current)
        self.assertIn("definition-1", api.drafts)
        api.calls.clear()
        Importer(api, SavedState(state.saved), BLUEPRINT).ensure_models()
        self.assertNotIn(("POST", "/api/metadata/definitions/definition-1/preview", None), api.calls)
        self.assert_recovered_before_next_model(api)

    def test_unbound_existing_draft_is_not_published(self):
        api = PublicationAPI()
        state = SavedState()
        Importer(api, state, BLUEPRINT).ensure_models()
        record = api.current.pop("definition-1")
        record["status"] = "draft"
        api.drafts["definition-1"] = record
        del state.data["definitions"]["library"]
        api.calls.clear()
        with self.assertRaises(ImportFailure):
            Importer(api, state, BLUEPRINT).ensure_models()
        self.assertEqual([], api.calls, "An existing unowned draft must remain untouched")
        self.assertIn("definition-1", api.drafts)

    def test_lost_schema_create_response_recovers_only_exact_generated_field_ids(self):
        api = PublicationAPI()
        state = SavedState()
        api.lose_create_response = True
        with self.assertRaises(ImportFailure):
            Importer(api, state, BLUEPRINT).ensure_models()
        planned = state.saved["definitions"]["library"]["create_payload"]
        self.assertEqual(planned["schema"], api.drafts["definition-1"]["definition"]["schema"])
        Importer(api, SavedState(state.saved), BLUEPRINT).ensure_models()
        self.assertEqual(2, api.next_id)
        self.assertEqual(2, len(api.current))

    def test_display_rename_keeps_saved_schema_identity_without_creating_copy(self):
        api = PublicationAPI()
        state = SavedState()
        Importer(api, state, BLUEPRINT).ensure_models()
        renamed = api.current["definition-1"]["definition"]
        renamed["name"] = "Renamed knowledge library"
        renamed["description"] = "Display description edited in the workspace"
        api.calls.clear()
        resumed = Importer(api, SavedState(state.saved), BLUEPRINT)
        resumed.ensure_models()
        self.assertEqual("definition-1", resumed.definitions["library"]["id"])
        self.assertEqual("Renamed knowledge library", resumed.definitions["library"]["name"])
        self.assertEqual(2, api.next_id)
        self.assertFalse(any(method == "POST" for method, _, _ in api.calls))


if __name__ == "__main__":
    unittest.main()
