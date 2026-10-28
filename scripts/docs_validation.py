"""Checks links and image references in rendered documentation pages."""

from html.parser import HTMLParser
from pathlib import Path
import re
from urllib.parse import unquote, urlsplit
import json


class _PageParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.ids = set()
        self.references = []
        self.images = []
        self.anchors = []
        self.headings = []
        self.permalinks = set()
        self.page_toc = False
        self.has_search_form = False

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if values.get("id"):
            self.ids.add(values["id"])
        if tag in {"h1", "h2", "h3", "h4", "h5", "h6"}:
            self.headings.append({"id": values.get("id", ""), "level": int(tag[1])})
        if tag == "nav" and values.get("aria-label") == "On this page":
            self.page_toc = True
        if tag == "form" and values.get("role") == "search":
            self.has_search_form = True
        if tag == "a":
            self.references.append(("link", values.get("href", ""), values.get("alt", "")))
            self.anchors.append(values.get("href", ""))
            if "headerlink" in values.get("class", "").split():
                self.permalinks.add(values.get("href", ""))
        elif tag == "img":
            self.references.append(("image", values.get("src", ""), values.get("alt", "")))
            self.images.append({**values, "parent_href": self.anchors[-1] if self.anchors else ""})

    def handle_endtag(self, tag):
        if tag == "a" and self.anchors:
            self.anchors.pop()


_UNSTABLE_SOURCE_LINK = re.compile(
    r"https?://[^\s)\]>]*(?:/latest/|/blob/(?:main|master|dev)/)", re.IGNORECASE
)
_GITHUB_FILE_LINK = re.compile(
    r"https?://github\.com/[^/\s)]+/[^/\s)]+/(?:blob|tree)/([^/\s)#)]+)", re.IGNORECASE
)
_COMMIT_SHA = re.compile(r"[0-9a-f]{40}", re.IGNORECASE)
_VERSION_REF = re.compile(r"v?\d+(?:\.\d+)+(?:[-+][0-9A-Za-z.-]+)?")


def validate_markdown_sources(root: Path) -> None:
    """Require consistent spelling and immutable targets for cited source docs."""
    sources = [root / "README.md", root / "CONTRIBUTING.md"]
    sources.extend(sorted((root / "docs").rglob("*.md")))
    for source in sources:
        if not source.is_file():
            continue
        body = source.read_text(encoding="utf-8")
        relative = source.relative_to(root).as_posix()
        if re.search(r"acknowledgement", body, re.IGNORECASE):
            raise SystemExit(f"{relative} uses acknowledgement; use acknowledgment")
        for match in _UNSTABLE_SOURCE_LINK.finditer(body):
            raise SystemExit(
                f"{relative} has a moving external source link: {match.group(0)}"
            )
        for match in _GITHUB_FILE_LINK.finditer(body):
            ref = match.group(1)
            if not _COMMIT_SHA.fullmatch(ref) and not _VERSION_REF.fullmatch(ref):
                raise SystemExit(
                    f"{relative} has an unpinned GitHub source link: {match.group(0)}"
                )

def _png_dimensions(path: Path):
    header = path.read_bytes()[:24]
    if len(header) < 24 or header[:8] != b"\x89PNG\r\n\x1a\n" or header[12:16] != b"IHDR":
        raise SystemExit(f"Operator guide screenshot is not a readable PNG: {path.name}")
    width = int.from_bytes(header[16:20], "big")
    height = int.from_bytes(header[20:24], "big")
    if width < 1 or height < 1:
        raise SystemExit(f"Operator guide screenshot has invalid intrinsic dimensions: {path.name}")
    return width, height


def validate_rendered_docs(site: Path) -> None:
    """Fail for broken local files, fragments, or image references in a built site."""
    pages = {}
    for page in sorted(site.glob("*.html")):
        parser = _PageParser()
        parser.feed(page.read_text(encoding="utf-8"))
        pages[page.name] = parser

    for page_name, parser in pages.items():
        for heading in parser.headings:
            if heading["id"] and "#" + heading["id"] not in parser.permalinks:
                raise SystemExit(f"Documentation heading needs a permalink in {page_name}: #{heading['id']}")
        if sum(1 for heading in parser.headings if heading["level"] == 2 and heading["id"]) >= 4 and not parser.page_toc:
            raise SystemExit(f"Long documentation page needs an On this page list: {page_name}")
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

    guide = pages.get("operator-guide.html")
    if guide:
        for image in guide.images:
            source = image.get("src", "")
            if not source.startswith("assets/screenshots/"):
                continue
            if image.get("parent_href") != source:
                raise SystemExit(f"Operator guide screenshot needs a full-size screenshot link: {source}")
            if image.get("loading") != "lazy":
                raise SystemExit(f"Operator guide screenshot must set loading=lazy: {source}")
            parsed = urlsplit(source)
            image_path = site / unquote(parsed.path)
            try:
                width, height = _png_dimensions(image_path)
                declared = (int(image.get("width", "")), int(image.get("height", "")))
            except (ValueError, TypeError):
                raise SystemExit(f"Operator guide screenshot needs intrinsic dimensions: {source}")
            if declared != (width, height):
                raise SystemExit(f"Operator guide screenshot intrinsic dimensions do not match PNG: {source}")
            is_mobile = "-mobile" in Path(parsed.path).stem
            classes = set(image.get("class", "").split())
            if is_mobile and "documentation-screenshot-mobile" not in classes:
                raise SystemExit(f"Mobile operator screenshot needs a centered, capped image class: {source}")

    home = pages.get("index.html")
    if home:
        home_screenshots = [
            image
            for image in home.images
            if image.get("src", "").startswith("assets/screenshots/")
        ]
        if len(home_screenshots) != 1 or home_screenshots[0].get("src") != "assets/screenshots/designer-console-overview.png":
            raise SystemExit("Documentation home page must use only the designer console overview screenshot")
        if home_screenshots[0].get("parent_href") != home_screenshots[0].get("src"):
            raise SystemExit("Documentation home screenshot needs a full-size screenshot link")

    search_page = pages.get("search.html")
    if not search_page or not search_page.has_search_form:
        raise SystemExit("Documentation search page needs an accessible search form")
    index_path = site / "search-index.json"
    if not index_path.is_file():
        raise SystemExit("Generated documentation heading index is missing: search-index.json")
    try:
        heading_index = json.loads(index_path.read_text(encoding="utf-8"))
        indexed_urls = {item["url"] for item in heading_index}
    except (json.JSONDecodeError, KeyError, TypeError):
        raise SystemExit("Generated documentation heading index is invalid")
    expected_urls = {
        page_name + "#" + heading["id"]
        for page_name, parser in pages.items()
        if page_name != "404.html"
        for heading in parser.headings
        if heading["id"]
    }
    if indexed_urls != expected_urls or len(heading_index) != len(indexed_urls):
        raise SystemExit("Generated documentation heading index must contain every page heading exactly once")
