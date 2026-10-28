import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from docs_validation import validate_rendered_docs


class RenderedDocsValidationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.site = Path(self.temp.name)
        (self.site / "index.html").write_text(
            '<html><body><h1 id="home">Home</h1>'
            '<a href="guide.html#setup">Setup</a>'
            '<img src="assets/guide.png" alt="Guide"></body></html>',
            encoding="utf-8",
        )
        (self.site / "guide.html").write_text(
            '<html><body><h2 id="setup">Setup</h2></body></html>',
            encoding="utf-8",
        )
        (self.site / "assets").mkdir()
        (self.site / "assets/guide.png").write_bytes(b"image")

    def tearDown(self):
        self.temp.cleanup()

    def test_accepts_existing_pages_fragments_and_images(self):
        validate_rendered_docs(self.site)

    def test_rejects_missing_fragment_targets(self):
        (self.site / "index.html").write_text(
            '<a href="guide.html#missing">Broken</a>', encoding="utf-8"
        )
        with self.assertRaisesRegex(SystemExit, "missing fragment.*guide.html#missing"):
            validate_rendered_docs(self.site)

    def test_rejects_missing_image_files(self):
        (self.site / "index.html").write_text(
            '<img src="assets/missing.png" alt="Missing">', encoding="utf-8"
        )
        with self.assertRaisesRegex(SystemExit, "missing image.*assets/missing.png"):
            validate_rendered_docs(self.site)


if __name__ == "__main__":
    unittest.main()
