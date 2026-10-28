"""Checks links and image references in rendered documentation pages."""

from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit


class _PageParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.ids = set()
        self.references = []

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if values.get("id"):
            self.ids.add(values["id"])
        if tag == "a":
            self.references.append(("link", values.get("href", ""), values.get("alt", "")))
        elif tag == "img":
            self.references.append(("image", values.get("src", ""), values.get("alt", "")))


def validate_rendered_docs(site: Path) -> None:
    """Fail for broken local files, fragments, or image references in a built site."""
    pages = {}
    for page in sorted(site.glob("*.html")):
        parser = _PageParser()
        parser.feed(page.read_text(encoding="utf-8"))
        pages[page.name] = parser

    for page_name, parser in pages.items():
        for kind, reference, alt in parser.references:
            if not reference:
                raise SystemExit(f"Empty documentation {kind} in {page_name}")
            if kind == "image" and not alt.strip():
                raise SystemExit(f"Documentation screenshot needs alt text in {page_name}")
            parsed = urlsplit(reference)
            if parsed.scheme or parsed.netloc:
                continue
            target_name = unquote(parsed.path) if parsed.path else page_name
            target = site / target_name
            if kind == "image":
                if not target.is_file():
                    raise SystemExit(f"Documentation page {page_name} references missing image: {reference}")
                continue
            if not target.is_file():
                raise SystemExit(f"Broken documentation link in {page_name}: {reference}")
            if parsed.fragment:
                fragment = unquote(parsed.fragment)
                target_page = pages.get(target.name)
                if target_page is None or fragment not in target_page.ids:
                    raise SystemExit(f"Broken documentation missing fragment in {page_name}: {reference}")
