import io
import gzip
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

from release_archive import REQUIRED, create, verify


class ReleaseArchiveTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.source = self.root / 'source'
        for name in REQUIRED:
            path = self.source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b'fixture\n')
        self.archive = self.root / 'release.tar.gz'
        self.revision = 'a' * 40
        create(self.source, self.archive, self.revision)

    def rewrite(self, transform):
        with tarfile.open(self.archive) as source:
            entries = [(member, source.extractfile(member).read()) for member in source.getmembers()]
        with tarfile.open(self.archive, 'w:gz') as target:
            for member, data in transform(entries):
                target.addfile(member, io.BytesIO(data) if member.isfile() else None)

    def test_roundtrip_and_reject_revision_mismatch(self):
        destination = self.root / 'installed'
        manifest = verify(self.archive, self.revision, destination)
        self.assertEqual(manifest['revision'], self.revision)
        self.assertEqual((destination / 'bin/data').read_bytes(), b'fixture\n')
        with self.assertRaises(ValueError):
            verify(self.archive, 'b' * 40)

    def test_tampered_binary_is_rejected_before_extraction(self):
        def change(entries):
            for member, data in entries:
                if member.name == 'bin/data':
                    data = b'tampered'
                    member.size = len(data)
                yield member, data
        self.rewrite(change)
        destination = self.root / 'installed'
        with self.assertRaises(ValueError):
            verify(self.archive, self.revision, destination)
        self.assertFalse(destination.exists())

    def test_traversal_symlink_and_duplicate_are_rejected(self):
        for kind in ['traversal', 'symlink', 'duplicate']:
            with self.subTest(kind=kind):
                create(self.source, self.archive, self.revision)
                def change(entries):
                    yield from entries
                    extra = tarfile.TarInfo('../outside' if kind == 'traversal' else 'web/extra')
                    if kind == 'duplicate':
                        extra.name = 'web/index.html'
                    if kind == 'symlink':
                        extra.type = tarfile.SYMTYPE
                        extra.linkname = '/etc'
                    yield extra, b''
                self.rewrite(change)
                with self.assertRaises(ValueError):
                    verify(self.archive)
        self.assertFalse((self.root / 'outside').exists())

    def test_wrong_target_and_missing_binary_are_rejected(self):
        for change in [lambda m: m.update(target='windows/amd64'), lambda m: m['files'].pop('bin/data')]:
            create(self.source, self.archive, self.revision)
            def transform(entries):
                for member, data in entries:
                    if member.name == 'manifest.json':
                        manifest = json.loads(data)
                        change(manifest)
                        data = json.dumps(manifest).encode()
                        member.size = len(data)
                    yield member, data
            self.rewrite(transform)
            with self.assertRaises(ValueError):
                verify(self.archive)

    def test_large_entry_rejected_before_reading_its_body(self):
        entry = tarfile.TarInfo('web/large')
        entry.size = 513 * 1024 * 1024
        with gzip.open(self.archive, 'wb') as output:
            output.write(entry.tobuf())
        with self.assertRaisesRegex(ValueError, '资源预算'):
            verify(self.archive)


if __name__ == '__main__':
    unittest.main()
