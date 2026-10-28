"""Helpers for generated documentation navigation and heading search."""

from html import escape
from html.parser import HTMLParser


class _HeadingParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.headings = []
        self._heading = None
        self._skip_link = False

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if tag in {"h1", "h2", "h3", "h4", "h5", "h6"} and self._heading is None:
            self._heading = {
                "id": values.get("id", ""),
                "level": int(tag[1]),
                "parts": [],
            }
        elif tag == "a" and "headerlink" in values.get("class", "").split():
            self._skip_link = True

    def handle_endtag(self, tag):
        if tag == "a" and self._skip_link:
            self._skip_link = False
        if self._heading and tag == f"h{self._heading['level']}":
            text = " ".join("".join(self._heading["parts"]).split())
            if self._heading["id"] and text:
                self.headings.append(
                    {"id": self._heading["id"], "level": self._heading["level"], "text": text}
                )
            self._heading = None

    def handle_data(self, data):
        if self._heading and not self._skip_link:
            self._heading["parts"].append(data)


def extract_headings(rendered_html):
    parser = _HeadingParser()
    parser.feed(rendered_html)
    return parser.headings


def insert_page_toc(rendered_html, minimum_h2=4):
    """Insert a compact contents list after H1 on pages with enough sections."""
    headings = [heading for heading in extract_headings(rendered_html) if heading["level"] == 2]
    if len(headings) < minimum_h2 or "</h1>" not in rendered_html:
        return rendered_html
    items = "".join(
        '<li><a href="#' + escape(heading["id"], quote=True) + '">'
        + escape(heading["text"])
        + "</a></li>"
        for heading in headings
    )
    toc = '<nav aria-label="On this page" class="page-toc"><h2>On this page</h2><ol>' + items + "</ol></nav>"
    return rendered_html.replace("</h1>", "</h1>" + toc, 1)


def build_heading_index(pages):
    """Return every rendered page heading as a stable page-and-fragment record."""
    records = []
    for filename, rendered_html in sorted(pages.items()):
        headings = extract_headings(rendered_html)
        page_title = next((heading["text"] for heading in headings if heading["level"] == 1), filename)
        for heading in headings:
            records.append(
                {
                    "page": page_title,
                    "url": filename + "#" + heading["id"],
                    "heading": heading["text"],
                    "level": heading["level"],
                }
            )
    return records


def search_page_body():
    """Accessible client-side search UI backed by the generated heading index."""
    return '''<h1 id="search">Search the documentation<a class="headerlink" href="#search" title="Permanent link">¶</a></h1>
<p>Search page titles and section headings. Results link directly to the matching section.</p>
<form id="docs-search" role="search">
  <label for="docs-search-query">Search terms</label>
  <input id="docs-search-query" name="q" type="search" autocomplete="off">
  <button type="submit">Search</button>
</form>
<p id="docs-search-status" aria-live="polite">Enter a search term.</p>
<ol id="docs-search-results"></ol>
<script>
(() => {
  const form = document.querySelector('#docs-search');
  const input = document.querySelector('#docs-search-query');
  const status = document.querySelector('#docs-search-status');
  const results = document.querySelector('#docs-search-results');
  let headings = [];
  fetch('search-index.json').then((response) => {
    if (!response.ok) throw new Error('Search index unavailable');
    return response.json();
  }).then((index) => { headings = index; }).catch(() => {
    status.textContent = 'Search is temporarily unavailable.';
  });
  form.addEventListener('submit', (event) => {
    event.preventDefault();
    const terms = input.value.trim().toLocaleLowerCase().split(/\\s+/).filter(Boolean);
    results.replaceChildren();
    if (!terms.length) { status.textContent = 'Enter a search term.'; return; }
    const matches = headings.filter((item) => {
      const text = (item.page + ' ' + item.heading).toLocaleLowerCase();
      return terms.every((term) => text.includes(term));
    }).slice(0, 50);
    for (const item of matches) {
      const li = document.createElement('li');
      const link = document.createElement('a');
      link.href = item.url;
      link.textContent = item.heading;
      li.append(link, document.createTextNode(' — ' + item.page));
      results.append(li);
    }
    status.textContent = matches.length
      ? `${matches.length} matching section${matches.length === 1 ? '' : 's'}.`
      : 'No matching sections.';
  });
})();
</script>'''
