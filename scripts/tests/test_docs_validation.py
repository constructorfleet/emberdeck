import sys
import tempfile
import unittest
from pathlib import Path
import struct

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from docs_validation import validate_markdown_sources, validate_rendered_docs


class RenderedDocsValidationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.site = Path(self.temp.name)
        (self.site / "index.html").write_text(
            '<html><body><h1 id="home">Home</h1>'
            '<a href="guide.html#setup">Setup</a>'
            '<a href="assets/screenshots/designer-console-overview.png">'
            '<img src="assets/screenshots/designer-console-overview.png" alt="Overview"></a></body></html>',
            encoding="utf-8",
        )
        (self.site / "guide.html").write_text(
            '<html><body><h2 id="setup">Setup</h2></body></html>',
            encoding="utf-8",
        )
        (self.site / "assets").mkdir()
        (self.site / "assets/guide.png").write_bytes(b"image")
        (self.site / "assets/screenshots").mkdir()
        png_header = lambda width, height: b"\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR" + struct.pack(">II", width, height)
        (self.site / "assets/screenshots/designer-console-overview.png").write_bytes(png_header(1440, 1562))
        (self.site / "operator-guide.html").write_text(
            '<html><body><a href="assets/screenshots/detail.png">'
            '<img src="assets/screenshots/detail.png" alt="Detail" '
            'width="320" height="180" loading="lazy"></a></body></html>',
            encoding="utf-8",
        )
        (self.site / "assets/screenshots/detail.png").write_bytes(
            png_header(320, 180)
        )

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

    def test_operator_screenshots_link_their_full_size_png(self):
        (self.site / "operator-guide.html").write_text(
            '<img src="assets/screenshots/detail.png" alt="Detail" '
            'width="320" height="180" loading="lazy">',
            encoding="utf-8",
        )
        with self.assertRaisesRegex(SystemExit, "full-size screenshot link"):
            validate_rendered_docs(self.site)


    def test_operator_screenshots_declare_matching_intrinsic_dimensions(self):
        (self.site / "operator-guide.html").write_text(
            '<a href="assets/screenshots/detail.png">'
            '<img src="assets/screenshots/detail.png" alt="Detail" '
            'width="319" height="180" loading="lazy"></a>',
            encoding="utf-8",
        )
        with self.assertRaisesRegex(SystemExit, "intrinsic dimensions"):
            validate_rendered_docs(self.site)

    def test_operator_screenshots_are_lazy_loaded(self):
        (self.site / "operator-guide.html").write_text(
            '<a href="assets/screenshots/detail.png">'
            '<img src="assets/screenshots/detail.png" alt="Detail" '
            'width="320" height="180"></a>',
            encoding="utf-8",
        )
        with self.assertRaisesRegex(SystemExit, "loading=lazy"):
            validate_rendered_docs(self.site)

    def test_mobile_operator_screenshots_mark_responsive_class(self):
        (self.site / "assets/screenshots/detail-mobile.png").write_bytes(
            b"\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR" + struct.pack(">II", 320, 480)
        )
        (self.site / "operator-guide.html").write_text(
            '<a href="assets/screenshots/detail-mobile.png">'
            '<img src="assets/screenshots/detail-mobile.png" alt="Mobile" '
            'width="320" height="480" loading="lazy"></a>',
            encoding="utf-8",
        )
        with self.assertRaisesRegex(SystemExit, "centered, capped"):
            validate_rendered_docs(self.site)

    def test_home_page_uses_one_overview_screenshot(self):
        (self.site / "index.html").write_text(
            '<img src="assets/screenshots/designer-console-overview.png" alt="Overview">'
            '<img src="assets/screenshots/detail.png" alt="Extra">',
            encoding="utf-8",
        )
        with self.assertRaisesRegex(SystemExit, "only the designer console overview"):
            validate_rendered_docs(self.site)

    def test_home_overview_links_to_full_size_png(self):
        (self.site / "index.html").write_text(
            '<img src="assets/screenshots/designer-console-overview.png" alt="Overview">',
            encoding="utf-8",
        )
        with self.assertRaisesRegex(SystemExit, "full-size screenshot link"):
            validate_rendered_docs(self.site)


class MarkdownSourceValidationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        (self.root / "docs").mkdir()

    def tearDown(self):
        self.temp.cleanup()

    def test_rejects_acknowledgement_spelling(self):
        (self.root / "docs/driver.md").write_text(
            "GRBL acknowledgement arrives here.\n", encoding="utf-8"
        )
        with self.assertRaisesRegex(SystemExit, "docs/driver.md.*acknowledgement"):
            validate_markdown_sources(self.root)

    def test_rejects_moving_support_link_targets(self):
        (self.root / "docs/driver.md").write_text(
            "[GRBL](https://github.com/gnea/grbl/blob/master/doc/markdown/interface.md)\n",
            encoding="utf-8",
        )
        with self.assertRaisesRegex(SystemExit, "docs/driver.md.*blob/master"):
            validate_markdown_sources(self.root)

    def test_rejects_latest_vendor_documentation(self):
        (self.root / "docs/driver.md").write_text(
            "[ESP-IDF](https://docs.espressif.com/projects/esp-idf/en/latest/esp32/security/secure-boot.html)\n",
            encoding="utf-8",
        )
        with self.assertRaisesRegex(SystemExit, "moving external source link"):
            validate_markdown_sources(self.root)

    def test_accepts_consistent_spelling_and_pinned_links(self):
        (self.root / "README.md").write_text(
            "Acknowledgment is documented.\n", encoding="utf-8"
        )
        (self.root / "docs/driver.md").write_text(
            "[GRBL](https://github.com/gnea/grbl/blob/0123456789abcdef0123456789abcdef01234567/doc/markdown/interface.md)\n",
            encoding="utf-8",
        )
        validate_markdown_sources(self.root)

    def test_rejects_short_commit_refs(self):
        (self.root / "docs/driver.md").write_text(
            "[GRBL](https://github.com/gnea/grbl/blob/abc1234/doc/markdown/interface.md)\n",
            encoding="utf-8",
        )
        with self.assertRaisesRegex(SystemExit, "unpinned GitHub source link"):
            validate_markdown_sources(self.root)


if __name__ == "__main__":
    unittest.main()
