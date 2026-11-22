import tempfile
import unittest
from pathlib import Path

from tools.csv_import import apply, prepare
from tools.knowledge_base.client import ImportFailure


class CSVImportTests(unittest.TestCase):
    def setUp(self):
        self.record = {"status": "active", "etag": "schema-etag", "definition": {"id": "schema", "schema": {
            "mode": "schema_2_db", "storageLayout": "single", "properties": [
                {"id": "amount", "dataType": "decimal", "precision": 18, "scale": 2, "required": True},
                {"id": "count", "dataType": "int64", "nullable": True},
            ]}}}
        self.mapping = {"amount": "amount", "count": "count"}

    def test_preflight_preserves_precision_and_reports_invalid_record(self):
        operations = prepare(b"amount,count\n9007199254740993.01,9223372036854775807\n1.00,\n", self.mapping, self.record, 1)
        self.assertEqual(operations[0][0]["values"], {"amount": "9007199254740993.01", "count": "9223372036854775807"})
        self.assertIsNone(operations[1][0]["values"]["count"])
        with self.assertRaisesRegex(ImportFailure, "记录 2 字段 amount"):
            prepare(b"amount,count\n1.00,1\n1.001,2\n", self.mapping, self.record, 1)

    def test_lost_response_reuses_exact_transaction_and_refuses_other_identity(self):
        chunks = prepare(b"amount,count\n1.00,1\n2.00,2\n", self.mapping, self.record, 1)

        class Client:
            attempts = []
            commits = {}

            def request(self, method, path, body):
                key = body["idempotencyKey"]
                self.attempts.append(key)
                if key in self.commits:
                    assert self.commits[key] == body
                else:
                    self.commits[key] = body
                    if len(self.commits) == 2:
                        raise ImportFailure("lost response")
                return {"committed": True, "status": "committed"}

        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "checkpoint.json"
            client = Client()
            with self.assertRaisesRegex(ImportFailure, "lost response"):
                apply(client, state, {"subject": "owner"}, chunks, lambda _: None)
            with self.assertRaisesRegex(ImportFailure, "状态文件"):
                apply(client, state, {"subject": "other"}, chunks)
            apply(client, state, {"subject": "owner"}, chunks, lambda _: None)
            self.assertEqual(len(client.attempts), 3)
            self.assertEqual(client.attempts[1], client.attempts[2])
            self.assertEqual(len(client.commits), 2)
