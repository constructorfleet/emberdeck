import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from docs_features import build_heading_index, insert_page_toc


class DocumentationNavigationTests(unittest.TestCase):
    def test_inserts_on_this_page_navigation_for_four_or_more_h2_headings(self):
        page = (
            '<h1 id="guide">Guide</h1>'
            '<h2 id="one">One</h2><p>First</p>'
            '<h2 id="two">Two</h2><p>Second</p>'
            '<h2 id="three">Three</h2><p>Third</p>'
            '<h2 id="four">Four</h2><p>Fourth</p>'
        )

        rendered = insert_page_toc(page)

        self.assertIn('<nav aria-label="On this page" class="page-toc">', rendered)
        self.assertIn('<a href="#four">Four</a>', rendered)
        self.assertLess(rendered.index('class="page-toc"'), rendered.index('<h2 id="one">'))

    def test_omits_on_this_page_navigation_for_short_pages(self):
        page = '<h1 id="guide">Guide</h1><h2 id="one">One</h2><h2 id="two">Two</h2>'
        self.assertNotIn('page-toc', insert_page_toc(page))

    def test_heading_index_contains_all_headings_with_page_fragments(self):
        pages = {
            "deployment.html": (
                '<h1 id="deployment">Deployment</h1>'
                '<h2 id="linux">Linux install<a class="headerlink" href="#linux">¶</a></h2>'
                '<h3 id="docker">Docker</h3>'
            ),
            "api.html": '<h1 id="api">API reference</h1><h2 id="jobs">Jobs</h2>',
        }

        index = build_heading_index(pages)

        self.assertEqual(
            index,
            [
                {"page": "API reference", "url": "api.html#api", "heading": "API reference", "level": 1},
                {"page": "API reference", "url": "api.html#jobs", "heading": "Jobs", "level": 2},
                {"page": "Deployment", "url": "deployment.html#deployment", "heading": "Deployment", "level": 1},
                {"page": "Deployment", "url": "deployment.html#linux", "heading": "Linux install", "level": 2},
                {"page": "Deployment", "url": "deployment.html#docker", "heading": "Docker", "level": 3},
            ],
        )
        self.assertEqual(json.loads(json.dumps(index))[3]["heading"], "Linux install")


if __name__ == "__main__":
    unittest.main()
