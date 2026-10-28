"""Checks links and image references in rendered documentation pages."""

from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit


class _PageParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.ids = set()
        self.references = []
        self.images = []
        self.anchors = []

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if values.get("id"):
            self.ids.add(values["id"])
        if tag == "a":
            self.references.append(("link", values.get("href", ""), values.get("alt", "")))
            self.anchors.append(values.get("href", ""))
        elif tag == "img":
            self.references.append(("image", values.get("src", ""), values.get("alt", "")))
            self.images.append({**values, "parent_href": self.anchors[-1] if self.anchors else ""})

    def handle_endtag(self, tag):
        if tag == "a" and self.anchors:
            self.anchors.pop()


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
