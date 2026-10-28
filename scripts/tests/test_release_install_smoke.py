import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from release_install_smoke import verify_asset_checksum


class ReleaseAssetChecksumTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.asset = self.root / "emberdeck-linux-amd64"
        self.asset.write_bytes(b"release binary fixture")
        self.manifest = self.root / "SHA256SUMS"

    def tearDown(self):
        self.temp.cleanup()

    def test_published_checksum_manifest_accepts_the_named_asset(self):
        self.manifest.write_text(
            "0b1bce3c20ee349712cc45888b09bade75a6a3c2d6059652b1e847742d092a44  emberdeck-linux-amd64\n",
            encoding="utf-8",
        )
        verify_asset_checksum(self.asset, self.manifest)

    def test_published_checksum_manifest_rejects_a_modified_asset(self):
        self.manifest.write_text(
            "0000000000000000000000000000000000000000000000000000000000000000  emberdeck-linux-amd64\n",
            encoding="utf-8",
        )
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            verify_asset_checksum(self.asset, self.manifest)

    def test_missing_manifest_entry_is_an_error(self):
        self.manifest.write_text("", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "missing from SHA256SUMS"):
            verify_asset_checksum(self.asset, self.manifest)


if __name__ == "__main__":
    unittest.main()
