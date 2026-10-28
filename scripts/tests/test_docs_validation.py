import sys
import tempfile
import unittest
from pathlib import Path
import struct

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from docs_validation import (
    validate_api_reference,
    validate_cli_reference,
    validate_markdown_sources,
    validate_rendered_docs,
)


class RenderedDocsValidationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.site = Path(self.temp.name)
        (self.site / "index.html").write_text(
            '<html><body><h1 id="home">Home<a class="headerlink" href="#home">¶</a></h1>'
            '<a href="guide.html#setup">Setup</a>'
            '<a href="assets/screenshots/designer-console-overview.png">'
            '<img src="assets/screenshots/designer-console-overview.png" alt="Overview"></a></body></html>',
            encoding="utf-8",
        )
        (self.site / "guide.html").write_text(
            '<html><body><h2 id="setup">Setup<a class="headerlink" href="#setup">¶</a></h2></body></html>',
            encoding="utf-8",
        )
        (self.site / "search.html").write_text(
            '<form role="search"><input type="search" aria-label="Search"></form>',
            encoding="utf-8",
        )
        (self.site / "search-index.json").write_text(
            '[{"page":"Home","url":"index.html#home","heading":"Home","level":1},'
            '{"page":"Setup","url":"guide.html#setup","heading":"Setup","level":2}]',
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

    def test_rejects_heading_without_permalink(self):
        (self.site / "guide.html").write_text('<h2 id="setup">Setup</h2>', encoding="utf-8")
        with self.assertRaisesRegex(SystemExit, "needs a permalink.*guide.html"):
            validate_rendered_docs(self.site)

    def test_rejects_missing_generated_heading_index(self):
        (self.site / "search-index.json").unlink()
        with self.assertRaisesRegex(SystemExit, "heading index is missing"):
            validate_rendered_docs(self.site)

    def test_rejects_long_page_without_contents_list(self):
        (self.site / "guide.html").write_text(
            ''.join(f'<h2 id="section-{i}">Section {i}<a class="headerlink" href="#section-{i}">¶</a></h2>' for i in range(4)),
            encoding="utf-8",
        )
        (self.site / "search-index.json").write_text(
            '[{"page":"Home","url":"index.html#home","heading":"Home","level":1}]',
            encoding="utf-8",
        )
        with self.assertRaisesRegex(SystemExit, "On this page list.*guide.html"):
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


class ReferencePageValidationTests(unittest.TestCase):
    route = "GET /api/v1/jobs"

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.site = Path(self.temp.name)
        self.pages = {"api.html": True, "api-routes.html": True}
        self.write_api()

    def tearDown(self):
        self.temp.cleanup()

    def write_api(self, section=None, index=True):
        if section is None:
            section = (
                f'<h3 id="get-apiv1jobs"><code>{self.route}</code>'
                '<a class="headerlink" href="#get-apiv1jobs">¶</a></h3>'
                "<table><tr><th>Request</th><th>Response</th><th>Errors</th></tr>"
                "<tr><td>No body.</td><td><code>200</code></td><td><code>401</code></td></tr></table>"
            )
        curl = (
            "<pre><code>curl --fail-with-body https://example.test/api/v1/jobs"
            " -H 'Authorization: Bearer KEY'</code></pre>"
        )
        (self.site / "api.html").write_text(section + curl, encoding="utf-8")
        links = (
            f'<a href="api.html#get-apiv1jobs"><code>{self.route}</code></a>'
            if index
            else ""
        )
        (self.site / "api-routes.html").write_text(links, encoding="utf-8")

    def validate(self, routes=None):
        validate_api_reference(self.site, self.pages, routes or {self.route})

    def test_accepts_linked_endpoint_with_contract_columns(self):
        self.validate()

    def test_rejects_missing_endpoint_section(self):
        self.write_api(section="")
        with self.assertRaisesRegex(SystemExit, "missing endpoint sections"):
            self.validate()

    def test_rejects_endpoint_without_error_column(self):
        self.write_api(
            '<h3 id="get-apiv1jobs"><code>GET /api/v1/jobs</code></h3>'
            "<table><tr><th>Request</th><th>Response</th></tr></table>"
        )
        with self.assertRaisesRegex(SystemExit, "column labelled Errors"):
            self.validate()

    def test_rejects_unlinked_route_index_entry(self):
        self.write_api(index=False)
        with self.assertRaisesRegex(SystemExit, "route index must link"):
            self.validate()

    def test_rejects_unformatted_json_example(self):
        self.write_api(
            '<h3 id="get-apiv1jobs"><code>GET /api/v1/jobs</code></h3>'
            "<table><tr><th>Request</th><th>Response</th><th>Errors</th></tr></table>"
            "<pre><code>curl -H 'Authorization: Bearer KEY' https://example.test"
            '</code></pre><pre><code>{"jobs":[]}</code></pre>'
        )
        with self.assertRaisesRegex(SystemExit, "pretty-printed"):
            self.validate()

    def test_rejects_missing_bearer_curl_example(self):
        self.write_api(
            '<h3 id="get-apiv1jobs"><code>GET /api/v1/jobs</code></h3>'
            "<table><tr><th>Request</th><th>Response</th><th>Errors</th></tr></table>"
        )
        api = (self.site / "api.html").read_text(encoding="utf-8")
        (self.site / "api.html").write_text(
            api.replace("Authorization: Bearer KEY", ""), encoding="utf-8"
        )
        with self.assertRaisesRegex(SystemExit, "bearer header"):
            self.validate()

    def test_cli_reference_requires_environment_and_examples(self):
        (self.site / "cli.html").write_text(
            "<h1>CLI</h1><p>EMBERDECK_API_KEY and EMBERDECK_METRICS_KEY</p>"
            "<pre><code>emberdeck serve\nemberdeck driver\n"
            "emberdeck driver -list-ports\nemberdeck -version</code></pre>"
            '<a href="deployment.html">Deploy</a><a href="driver.html">Driver</a>'
            '<a href="worker.html">Worker</a><a href="metrics.html">Metrics</a>',
            encoding="utf-8",
        )
        validate_cli_reference(self.site, {"cli.html": True})
        (self.site / "cli.html").write_text("<p>Usage</p>", encoding="utf-8")
        with self.assertRaisesRegex(SystemExit, "EMBERDECK_API_KEY"):
            validate_cli_reference(self.site, {"cli.html": True})


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
