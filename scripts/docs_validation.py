"""Checks links, references, and reference-page contracts in rendered docs."""

from html import unescape
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


_ROUTE_HEADING = re.compile(
    r'<h3 id="([^"]+)"><code>((?:GET|POST|PUT|DELETE) /api/v1/[^<]+)</code>'
)
_ROUTE_INDEX_LINK = re.compile(r'<a href="api\.html#([^"]+)">(.*?)</a>')
_CODE_BLOCK = re.compile(r"<pre[^>]*>(?:<span[^>]*></span>)?<code>(.*?)</code></pre>", re.DOTALL)
_TAG = re.compile(r"<[^>]+>")


def _code_text(block_html: str) -> str:
    return unescape(_TAG.sub("", block_html))


def validate_api_reference(site: Path, pages, routes) -> None:
    """Require a scannable, linked section for every registered API route."""
    if not routes:
        return
    api_page = pages.get("api.html")
    api_routes_page = pages.get("api-routes.html")
    if api_page is None or api_routes_page is None:
        raise SystemExit("Documentation must include API and API route reference pages")
    api_html = (site / "api.html").read_text(encoding="utf-8")
    index_html = (site / "api-routes.html").read_text(encoding="utf-8")

    matches = list(_ROUTE_HEADING.finditer(api_html))
    endpoints = {}
    for position, match in enumerate(matches):
        anchor, route = match.group(1), match.group(2)
        if route in endpoints:
            raise SystemExit(f"API reference documents {route} more than once")
        end = matches[position + 1].start() if position + 1 < len(matches) else len(api_html)
        endpoints[route] = (anchor, api_html[match.end():end])

    expected = set(routes)
    missing = sorted(expected - set(endpoints))
    extra = sorted(set(endpoints) - expected)
    if missing:
        raise SystemExit("API reference is missing endpoint sections: " + ", ".join(missing))
    if extra:
        raise SystemExit("API reference documents unknown endpoints: " + ", ".join(extra))

    for route, (anchor, section) in endpoints.items():
        for heading in ("Request", "Response", "Errors"):
            if f"<th>{heading}</th>" not in section:
                raise SystemExit(
                    f"API endpoint {route} needs a column labelled {heading} in its contract table"
                )
        if f'id="{anchor}"' not in api_html:
            raise SystemExit(f"API endpoint {route} has an unreachable section anchor")

    indexed = {}
    for anchor, label in _ROUTE_INDEX_LINK.findall(index_html):
        indexed[_code_text(label).strip()] = anchor
    if set(indexed) != expected:
        missing = sorted(expected - set(indexed))
        extra = sorted(set(indexed) - expected)
        detail = []
        if missing:
            detail.append("missing " + ", ".join(missing))
        if extra:
            detail.append("unknown " + ", ".join(extra))
        raise SystemExit("API route index must link every registered route: " + "; ".join(detail))
    for route, anchor in indexed.items():
        if endpoints[route][0] != anchor:
            raise SystemExit(f"API route index link for {route} does not match its section")

    blocks = [_code_text(block) for block in _CODE_BLOCK.findall(api_html)]
    if not any("curl" in block and "Authorization: Bearer" in block for block in blocks):
        raise SystemExit("API reference needs a complete curl example with a bearer header")
    for block in blocks:
        text = block.strip()
        if not text.startswith("{"):
            continue
        if "\n" not in text:
            raise SystemExit("API reference JSON examples must be pretty-printed")
        if max(len(line) for line in text.splitlines()) > 160:
            raise SystemExit("API reference JSON example lines must wrap for narrow screens")


def validate_cli_reference(site: Path, pages) -> None:
    """Require an operator-oriented CLI page instead of raw flag dumps."""
    if "cli.html" not in pages:
        raise SystemExit("Documentation must include a CLI reference page")
    html = (site / "cli.html").read_text(encoding="utf-8")
    text = _code_text(html)
    for required in ("EMBERDECK_API_KEY", "EMBERDECK_METRICS_KEY"):
        if required not in text:
            raise SystemExit(f"CLI reference must explain required environment variable {required}")
    blocks = [_code_text(block) for block in _CODE_BLOCK.findall(html)]
    for example in ("emberdeck serve", "emberdeck driver", "emberdeck driver -list-ports", "emberdeck -version"):
        if not any(example in block for block in blocks):
            raise SystemExit(f"CLI reference needs an example for {example}")
    links = set(re.findall(r'href="([^"]+)"', html))
    for target in ("deployment.html", "driver.html", "worker.html", "metrics.html"):
        if target not in links:
            raise SystemExit(f"CLI reference must link to {target}")


def validate_rendered_docs(site: Path, routes=()) -> None:
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

    if routes:
        validate_api_reference(site, pages, routes)
        validate_cli_reference(site, pages)

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
