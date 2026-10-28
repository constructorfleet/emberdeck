"""Render release docs, with API route index and CLI help derived from source/build."""
from pathlib import Path
from html import escape
from html.parser import HTMLParser
import json
import os
import re
import shutil
import subprocess
import markdown
from markdown.extensions.toc import slugify
from urllib.parse import urlsplit, unquote
from docs_validation import validate_markdown_sources, validate_rendered_docs
from docs_features import build_heading_index, insert_page_toc, search_page_body

validate_markdown_sources(Path.cwd())

out = Path('dist/docs')
if out.exists():
    shutil.rmtree(out)
out.mkdir(parents=True, exist_ok=True)
sources = [Path('README.md'), Path('CONTRIBUTING.md'), *sorted(Path('docs').rglob('*.md'))]
pages, source_paths = {}, {}
for path in sources:
    name = 'index' if path.name == 'README.md' else path.stem
    if name in pages:
        raise SystemExit(f'Documentation page names must be unique: {path}')
    pages[name] = path.read_text(encoding='utf-8')
    source_paths[name] = path
page_targets = {path.resolve(): name + '.html' for name, path in source_paths.items()}
repo_root = Path.cwd().resolve()
assets_root = (repo_root / 'docs/assets').resolve()
revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
source_url = 'https://github.com/constructorfleet/emberdeck/blob/' + revision + '/'
# GitHub Pages serves this project under its repository path; no CNAME is present.
site_base_url = 'https://constructorfleet.github.io/emberdeck/'
og_image_path = 'assets/screenshots/designer-console-overview.png'
if not (assets_root / 'screenshots/designer-console-overview.png').is_file():
    raise SystemExit(f'Open Graph image does not exist: {og_image_path}')
if not (assets_root / 'favicon.svg').is_file():
    raise SystemExit('Documentation favicon does not exist: docs/assets/favicon.svg')

def rewrite_link(match, source):
    href = match[1]
    parsed = urlsplit(href)
    if parsed.scheme or parsed.netloc or not parsed.path:
        return match[0]
    target = (source.parent / unquote(parsed.path)).resolve()
    fragment = '#' + parsed.fragment if parsed.fragment else ''
    if target in page_targets:
        return '](' + page_targets[target] + fragment + ')'
    if target == repo_root / 'LICENSE':
        return '](LICENSE.txt' + fragment + ')'
    try:
        relative = target.relative_to(assets_root)
        return '](assets/' + relative.as_posix() + fragment + ')'
    except ValueError:
        pass
    if target.is_file():
        try:
            return '](' + source_url + target.relative_to(repo_root).as_posix() + fragment + ')'
        except ValueError:
            pass
    return match[0]


def enhance_operator_guide_screenshots(body, page_name):
    """Link guide screenshots to originals and declare their PNG dimensions."""
    if page_name != 'operator-guide':
        return body

    def enhance(match):
        raw_tag = match.group(0)
        attributes = dict(re.findall(r'([\w-]+)="([^"]*)"', raw_tag))
        src = attributes.get('src', '')
        if not src.startswith('assets/screenshots/'):
            return raw_tag
        path = assets_root / src.removeprefix('assets/')
        header = path.read_bytes()[:24]
        if len(header) < 24 or header[:8] != b'\x89PNG\r\n\x1a\n' or header[12:16] != b'IHDR':
            raise SystemExit(f'Operator guide screenshot must be a PNG: {path}')
        width = int.from_bytes(header[16:20], 'big')
        height = int.from_bytes(header[20:24], 'big')
        if width < 1 or height < 1:
            raise SystemExit(f'Operator guide screenshot has invalid dimensions: {path}')
        alt = attributes.get('alt', '')
        classes = ['documentation-screenshot']
        if Path(src).stem.endswith('-mobile'):
            classes.append('documentation-screenshot-mobile')
        image = (
            '<img src="' + escape(src, quote=True) + '" alt="' + alt + '"'
            ' width="' + str(width) + '" height="' + str(height) + '"'
            ' loading="lazy" decoding="async" class="' + ' '.join(classes) + '">'
        )
        return (
            '<a class="screenshot-full-size" href="' + escape(src, quote=True) + '"'
            ' aria-label="Open full-size screenshot: ' + alt + '">' + image + '</a>'
        )

    return re.sub(r'<img\b[^>]*>', enhance, body)

routes = set()
for path in Path('internal/server').glob('*.go'):
    if not path.name.endswith('_test.go'):
        routes.update(re.findall(r'\.(?:handleAPI|HandleFunc)\("([A-Z]+ [^"\n]+)"', path.read_text()))
if not routes:
    raise SystemExit('No API routes were found to index')
api_route_links = '\n'.join(
    '- [`' + route + '`](api.html#' + slugify('`' + route + '`', '-') + ')'
    for route in sorted(routes)
)
pages['api-routes'] = '# API route index\n\nGenerated from the registered server routes in this release. Every entry links to its request, response, and error contract in the [API reference](api.html). All API routes require bearer authentication.\n\n' + api_route_links + '\n'
exe = Path('bin/emberdeck' + ('.exe' if os.name == 'nt' else '')).resolve()
help_text = []
for command in ['serve', 'driver']:
    result = subprocess.run([str(exe), command, '-h'], text=True, capture_output=True, check=True)
    help_text.append('## ' + command + '\n\n```text\n' + result.stdout + result.stderr + '```\n')
pages['cli'] = '''# CLI reference

The `emberdeck` binary is both the server and the distributed worker. The page is
generated from the executable built from this release; use `emberdeck -version`
to confirm the installed build.

## Environment

`emberdeck serve` requires `EMBERDECK_API_KEY`. On first start it becomes the
named `bootstrap-admin` credential, so choose a long random secret and keep it
out of shell history where possible. A local worker started by `serve -simulate`,
`-tcp`, or `-serial` uses the same key.

`EMBERDECK_METRICS_KEY` is optional. When set, the separately configured
`serve -metrics-listen` endpoint requires that key as a bearer token. See
[Metrics and monitoring](metrics.html).

## `emberdeck serve`

Start the HTTP server, optionally with one local driver. Select one of
`-simulate`, `-tcp`, or `-serial`; omit all three to run only the server.

```sh
EMBERDECK_API_KEY='replace-with-a-long-random-secret' \\
  emberdeck serve -listen 127.0.0.1:8080 -data ./data -simulate
```

See [Deployment](deployment.html) for TLS, service units, and data-directory
ownership. See [Driver and hardware setup](driver.html) for serial and TCP
controller connections.

## `emberdeck driver`

Run a worker on another host and connect it to a controller. `-server` is
required, and exactly one of `-simulate`, `-tcp`, or `-serial` must be supplied.

```sh
EMBERDECK_API_KEY='worker-key' \\
  emberdeck driver -server https://emberdeck.example.net -id bench -serial /dev/ttyUSB0 -baud 115200
```

Use a dedicated worker credential scoped to `-id`; see
[Worker setup](worker.html) and the credential lifecycle in the
[API reference](api.html).

## `emberdeck driver -list-ports`

List candidate serial devices and exit. This cannot be combined with connection
or server options.

```sh
emberdeck driver -list-ports
```

The output names platform serial devices such as `/dev/ttyUSB0` on Linux,
`/dev/cu.*` on macOS, or a `COM` port on Windows.

## `emberdeck -version`

Print the executable's version.

```sh
emberdeck -version
```

''' + '\n'.join(help_text)
pages['search'] = search_page_body()

class PageMetadataParser(HTMLParser):
    """Read a rendered H1 and its first useful paragraph without Markdown syntax."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.heading = []
        self.description = []
        self.in_heading = False
        self.in_paragraph = False
        self.paragraph = []
        self.blocked_before_description = False
        self.in_permalink = False

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if tag == 'a' and 'headerlink' in values.get('class', '').split():
            self.in_permalink = True
        if tag == 'h1' and not self.heading:
            self.in_heading = True
        elif tag == 'p' and not self.description and not self.blocked_before_description:
            self.in_paragraph = True
            self.paragraph = []
        elif tag in ('pre', 'table') and not self.description and not self.in_heading:
            self.blocked_before_description = True

    def handle_endtag(self, tag):
        if tag == 'a' and self.in_permalink:
            self.in_permalink = False
        elif tag == 'h1':
            self.in_heading = False
        elif tag == 'p' and self.in_paragraph:
            text = ' '.join(''.join(self.paragraph).split())
            if text:
                self.description = [text]
            self.in_paragraph = False

    def handle_data(self, data):
        if self.in_heading and not self.in_permalink:
            self.heading.append(data)
        if self.in_paragraph:
            self.paragraph.append(data)


page_headings, page_descriptions, fallback_pages = {}, {}, []
for name, text in pages.items():
    rendered = markdown.markdown(
        text,
        extensions=['fenced_code', 'tables', 'toc'],
        extension_configs={'toc': {'permalink': True}},
    )
    metadata = PageMetadataParser()
    metadata.feed(rendered)
    heading = ' '.join(''.join(metadata.heading).split())
    if not heading:
        raise SystemExit(f'Documentation page needs an H1: {name}')
    page_headings[name] = heading
    description = ' '.join(metadata.description)
    if not description:
        description = f'Documentation for {heading} in EmberDeck.'
        fallback_pages.append(name)
    if len(description) > 160:
        clipped = description[:157]
        if ' ' in clipped:
            clipped = clipped.rsplit(' ', 1)[0]
        description = clipped.rstrip(' ,;:') + '…'
    page_descriptions[name] = description

version = Path('version.txt').read_text().strip()
groups = {
    'Getting Started': ['index', 'install', 'operator-guide'],
    'Deploy & Run': ['deployment', 'metrics', 'troubleshooting', 'glossary', 'safety', 'hardware-qualification'],
    'Develop & Extend': ['CONTRIBUTING', 'architecture', 'plugins', 'browser-tests', 'artifact-tests', 'roadmap', 'designer-architecture', 'esp32-feasibility', 'driver', 'worker', 'serial'],
    'Reference': ['api', 'api-routes', 'cli', 'search'],
}
page_title_overrides = {}
def page_title(name):
    return page_title_overrides.get(name, page_headings[name])

grouped_pages = [name for group in groups.values() for name in group]
if set(grouped_pages) != set(pages) or len(grouped_pages) != len(pages):
    raise SystemExit(f'Documentation navigation must include every page exactly once: pages={sorted(pages)}, grouped={sorted(grouped_pages)}')
def make_nav(page_name):
    result = '<a class="skip-link" href="#main-content">Skip to content</a><nav aria-label="Documentation"><div class="nav-inner"><a class="brand" href="index.html">EmberDeck</a><span class="version">v' + escape(version) + '</span><details class="mobile-menu" name="docs-nav-mobile" open><summary>Menu</summary></details><div class="nav-groups">'
    for label, names in groups.items():
        selected_name = next((name for name in names if name == page_name), None)
        current_title = page_title(selected_name) if selected_name else ''
        result += '<details name="docs-nav"><summary><span>' + escape(label) + '</span>'
        if current_title:
            result += '<span class="sr-only">Current page: </span><span class="current-page">' + escape(current_title) + '</span>'
        result += '</summary><ul>'
        for name in names:
            current = ' aria-current="page"' if name == page_name else ''
            title = page_title(name)
            result += '<li><a href="' + name + '.html"' + current + '>' + escape(title) + '</a></li>'
        result += '</ul></details>'
    return result + '</div></div></nav>'
style_tokens = '''
:root{
 color-scheme:dark;
 --color-canvas:#101313;--color-panel:#171b1a;--color-panel-2:#1d2220;--color-border:#2c3430;--color-border-strong:#647168;
 --color-text:#edf1e9;--color-text-strong:#edf1e9;--color-muted:#929b92;--color-white:#edf1e9;--color-ink:#101313;
 --color-accent:#c1f36b;--color-accent-soft:#d1ff86;--color-focus:#c1f36b;--color-selected:#26311b;--color-shadow:rgba(0,0,0,.32);
 --color-inline-code:#1d2220;--color-print-paper:#fff;--color-print-ink:#000;
 --syntax-keyword:#c1f36b;--syntax-string:#f1d28d;--syntax-comment:#929b92;--syntax-builtin:#91c33d;--syntax-number:#ffad9c;--syntax-name:#edf1e9;--syntax-operator:#b9c4b7;--syntax-error:#ffad9c;
 --font-sans:Inter,-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;--font-mono:'SFMono-Regular',Consolas,'Liberation Mono',monospace;
 --space-0-5:.125rem;--space-1:.25rem;--space-1-5:.375rem;--space-2:.5rem;--space-3:.75rem;--space-4:1rem;--space-5:1.5rem;--space-6:2rem;--space-7:2.5rem;--space-8:3rem;
 --radius-sm:3px;--radius-md:6px;--radius-lg:8px;
 --type-xs:.75rem;--type-sm:.9rem;--type-base:1rem;--type-lg:1.25rem;--type-xl:1.5rem;--type-display:clamp(2rem,4vw,2.7rem);
}
'''

style = '''*{box-sizing:border-box}body{margin:0;background:var(--color-canvas);color:var(--color-text);font:var(--type-base)/1.7 var(--font-sans)}a{color:var(--color-accent);text-underline-offset:.18em}a:hover{color:var(--color-accent-soft)}a:focus-visible,summary:focus-visible{outline:3px solid var(--color-focus);outline-offset:3px;border-radius:var(--radius-sm)}.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}.skip-link{position:absolute;left:var(--space-4);top:-5rem;z-index:10;padding:var(--space-3) var(--space-4);background:var(--color-white);color:var(--color-ink);border-radius:var(--radius-md)}.skip-link:focus{top:var(--space-4)}nav{position:relative;z-index:2;display:flex;flex-wrap:wrap;justify-content:space-between;gap:var(--space-3);padding:var(--space-3) max(var(--space-4),4vw);background:var(--color-panel);align-items:center;border-bottom:1px solid var(--color-border)}nav .brand{font-weight:800;font-size:1.3rem;text-decoration:none;color:var(--color-white)}nav .version{color:var(--color-muted);font-size:var(--type-sm)}nav details{position:relative;min-width:0}nav summary{display:flex;flex-wrap:wrap;align-items:baseline;gap:var(--space-2);cursor:pointer;list-style:none;padding:var(--space-2) var(--space-3);border-radius:var(--radius-md);font-weight:650;color:var(--color-text-strong)}nav summary::-webkit-details-marker{display:none}nav summary::after{content:"▾";margin-left:.15em;color:var(--color-accent)}.current-page{font-size:var(--type-xs);color:var(--color-accent);font-weight:500}nav details[open] summary{background:var(--color-panel-2)}nav ul{position:absolute;top:100%;right:0;min-width:15rem;margin:var(--space-1) 0 0;padding:var(--space-2);list-style:none;background:var(--color-panel);border:1px solid var(--color-border-strong);border-radius:var(--radius-md);box-shadow:0 8px 24px var(--color-shadow)}nav li a{display:block;padding:var(--space-2) var(--space-3);border-radius:var(--radius-sm);text-decoration:none}nav li a:hover,nav li a[aria-current="page"]{background:var(--color-selected);color:var(--color-white)}nav li a[aria-current="page"]{box-shadow:inset 3px 0 var(--color-accent);font-weight:700}main{max-width:78ch;margin:var(--space-7) auto;padding:0 var(--space-5);overflow-wrap:anywhere}h1{font-size:var(--type-display);margin-top:0}h2{font-size:var(--type-xl);margin-top:var(--space-7)}h3{font-size:var(--type-lg)}h1,h2,h3{line-height:1.25;color:var(--color-white)}p,li{max-width:75ch}pre{overflow:auto;background:var(--color-panel);padding:var(--space-4);border-radius:var(--radius-lg);border:1px solid var(--color-border);line-height:1.45}code{font-family:var(--font-mono);font-size:.9em;overflow-wrap:anywhere}main p code,main li code{background:var(--color-inline-code);padding:var(--space-1) var(--space-2);border:1px solid var(--color-border);border-radius:var(--radius-sm)}img{display:block;max-width:100%;height:auto;border:1px solid var(--color-border-strong);border-radius:var(--radius-lg);margin:var(--space-4) 0}a.screenshot-full-size{position:relative;left:50%;transform:translateX(-50%);display:block;width:fit-content;max-width:min(1200px,calc(100vw - 2rem));margin:var(--space-4) 0}img.documentation-screenshot{margin:0}img.documentation-screenshot-mobile{width:auto;max-height:min(75vh,720px);object-fit:contain;margin-inline:auto}table{display:block;overflow-x:auto;border-collapse:collapse;max-width:100%;width:max-content}td,th{padding:var(--space-3);border:1px solid var(--color-border);text-align:left}footer{margin:var(--space-8) 0;border-top:1px solid var(--color-border);padding-top:var(--space-4);color:var(--color-muted)}@media(max-width:740px){nav{display:grid;grid-template-columns:1fr auto;gap:var(--space-1) var(--space-2);padding:var(--space-3) var(--space-4)}nav .brand{grid-column:1;grid-row:1}nav .version{grid-column:2;grid-row:1;margin:0;text-align:right}nav details{grid-column:1/-1;min-width:0}nav summary{padding:var(--space-3) var(--space-2);font-size:.92rem;flex-wrap:wrap;gap:0 var(--space-1-5)}nav details[open]{grid-column:1/-1}nav ul{position:static;min-width:0;margin:var(--space-1) 0 var(--space-2);box-shadow:none}main{margin:var(--space-6) auto;padding:0 var(--space-4)}pre{padding:var(--space-4)}img{margin:var(--space-4) 0}a.screenshot-full-size{max-width:calc(100vw - 2rem);margin:var(--space-4) 0}}'''

nav_style = '''
nav{display:block;padding:0;background:var(--color-panel)}
.nav-inner{display:grid;grid-template-columns:1fr auto;grid-template-rows:auto auto;align-items:center;gap:var(--space-1) var(--space-4);width:100%;max-width:78ch;margin:0 auto;padding:var(--space-3) var(--space-5)}
nav .brand{grid-column:1;grid-row:1;font-size:1.3rem}
nav .version{grid-column:2;grid-row:1}
.mobile-menu{display:contents}.mobile-menu>summary{display:none}
.nav-groups{grid-column:1/-1;grid-row:2;display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:var(--space-1);align-items:start}
nav summary{align-items:baseline;gap:var(--space-1) var(--space-1-5);padding:var(--space-2) var(--space-1-5)}
.current-page{max-width:11ch;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.nav-groups>details>ul{position:absolute;z-index:3;top:100%;left:0;right:auto;width:max-content;min-width:min(15rem,calc(100vw - 2rem));max-width:calc(100vw - 2rem)}
.nav-groups>details:last-child>ul{left:auto;right:0}
@media(max-width:980px){
 .nav-inner{grid-template-columns:minmax(0,1fr) auto auto;grid-template-rows:auto;gap:var(--space-2) var(--space-3);padding:var(--space-2) var(--space-4)}
 nav .brand{grid-column:1;grid-row:1;font-size:var(--space-4)}nav .version{grid-column:2;grid-row:1}
 .mobile-menu{display:contents}.mobile-menu>summary{display:flex;grid-column:3;grid-row:1;align-items:center;padding:var(--space-2) var(--space-3)}
 .mobile-menu>summary::-webkit-details-marker{display:none}.mobile-menu>summary::after{content:"▾";margin-left:var(--space-2);color:var(--color-accent)}
 .nav-groups{display:none;grid-column:1/-1;grid-row:2;grid-template-columns:minmax(0,1fr);gap:var(--space-0-5);align-items:stretch}
  .mobile-menu[open]~.nav-groups{display:grid}
 .nav-groups>details{position:static;grid-column:auto;width:100%}
 .nav-groups>details>summary{width:100%;padding:var(--space-3) var(--space-2)}
 .nav-groups>details>ul{position:static;width:auto;min-width:0;max-width:100%;margin:var(--space-0-5) 0 var(--space-2);box-shadow:none}
 main{margin:var(--space-6) auto;padding:0 var(--space-4)}pre{padding:var(--space-4)}img{margin:var(--space-4) 0}a.screenshot-full-size{width:calc(100vw - 2rem);max-width:calc(100vw - 2rem);overflow:auto;overscroll-behavior-inline:contain;margin:var(--space-4) 0}img.documentation-screenshot{width:auto;max-width:none;max-height:min(75vh,720px);object-fit:contain;margin:0 auto}
}
'''

code_style = '''
.codehilite{position:relative;overflow:auto;margin:var(--space-4) 0;background:var(--color-panel);border:1px solid var(--color-border);border-radius:var(--radius-md)}
.codehilite pre{margin:0;padding:var(--space-5);border:0;border-radius:0;background:transparent;color:var(--color-text);font:1rem/1.45 var(--font-mono);white-space:pre;overflow:auto}
.copy-code-button{position:absolute;z-index:1;top:var(--space-2);right:var(--space-2);padding:var(--space-1) var(--space-2);border:1px solid var(--color-border-strong);border-radius:var(--radius-sm);background:var(--color-panel-2);color:var(--color-text);font:var(--type-xs) var(--font-mono);cursor:pointer}
.copy-code-button:hover{border-color:var(--color-accent);color:var(--color-accent)}
.copy-code-button:focus-visible{outline:2px solid var(--color-focus);outline-offset:2px}
.codehilite .k,.codehilite .kd,.codehilite .kn,.codehilite .kp,.codehilite .kr,.codehilite .kt{color:var(--syntax-keyword);font-weight:700}
.codehilite .s,.codehilite .sa,.codehilite .sb,.codehilite .sc,.codehilite .dl,.codehilite .sd,.codehilite .s2,.codehilite .se,.codehilite .sh,.codehilite .si,.codehilite .sx,.codehilite .sr,.codehilite .s1,.codehilite .ss{color:var(--syntax-string)}
.codehilite .c,.codehilite .ch,.codehilite .cm,.codehilite .c1,.codehilite .cs{color:var(--syntax-comment);font-style:italic}
.codehilite .nb,.codehilite .bp{color:var(--syntax-builtin)}
.codehilite .nf,.codehilite .nc{color:var(--color-accent)}
.codehilite .m,.codehilite .mb,.codehilite .mf,.codehilite .mh,.codehilite .mi,.codehilite .mo{color:var(--syntax-number)}
.codehilite .o,.codehilite .ow{color:var(--syntax-operator)}
.codehilite .err{color:var(--syntax-error);text-decoration:underline}
@media(max-width:740px){.codehilite pre{padding:var(--space-4)}.copy-code-button{top:var(--space-1);right:var(--space-1)}}
@media print{
 html,body{background:var(--color-print-paper)!important;color:var(--color-print-ink)!important;color-scheme:light}
 nav,.skip-link,.copy-code-button{display:none!important}
 main{max-width:none;margin:0;padding:0}
 a,a:hover,a:visited{color:var(--color-print-ink)!important;text-decoration:underline}
 pre,.codehilite,.codehilite pre{overflow:visible;background:var(--color-print-paper)!important;color:var(--color-print-ink)!important;border-color:currentColor;box-shadow:none}
 .codehilite pre{white-space:pre-wrap}
 img{max-width:100%}
}
'''
scroll_region_style = '''
.scroll-region{max-width:100%;overflow:auto;overscroll-behavior:contain}
.scroll-region:focus-visible{outline:3px solid var(--color-focus);outline-offset:3px;border-radius:var(--radius-sm)}
.scroll-region pre{overflow:visible}
.scroll-region table{display:table;overflow:visible;width:max-content;max-width:none}
.scroll-region td,.scroll-region th{overflow-wrap:normal;word-break:normal;hyphens:none}
footer{max-width:78ch;margin:var(--space-8) auto 0;padding:var(--space-4) var(--space-5) var(--space-5)}
footer a{display:inline-block;min-height:44px;padding:var(--space-2)}
@media(max-width:740px){footer{margin-top:var(--space-6);padding:var(--space-3) var(--space-4) var(--space-4)}}
'''

def add_scrollable_code_region(body):
    """Make overflowing code samples keyboard reachable as named regions."""
    return re.sub(
        r'<pre(?:\s[^>]*)?>.*?</pre>',
        lambda match: '<div class="scroll-region code-scroll-region" tabindex="0" role="region" aria-label="Scrollable code sample">' + match.group(0) + '</div>',
        body,
        flags=re.DOTALL,
    )

def add_scrollable_table_region(body):
    """Give horizontally overflowing tables a named, focusable scroll region."""
    return re.sub(
        r'<table(?:\s[^>]*)?>.*?</table>',
        lambda match: '<div class="scroll-region table-scroll-region" tabindex="0" role="region" aria-label="Scrollable documentation table">' + match.group(0) + '</div>',
        body,
        flags=re.DOTALL,
    )

def shell_break_positions(line):
    """Find spaces where a shell command can continue without splitting a token."""
    positions, quote, escaped = [], None, False
    for index, character in enumerate(line):
        if escaped:
            escaped = False
        elif character == '\\':
            escaped = True
        elif quote:
            if character == quote:
                quote = None
        elif character in ('"', "'"):
            quote = character
        elif character == '`':
            return []
        elif character.isspace():
            positions.append(index)
    return positions

def wrap_shell_command_line(line, width=72):
    """Use shell continuations to keep long, safely splittable commands readable."""
    newline = '\r\n' if line.endswith('\r\n') else '\n' if line.endswith('\n') else ''
    content = line[:-len(newline)] if newline else line
    indentation = content[:len(content) - len(content.lstrip())]
    command = content[len(indentation):]
    if len(command) <= width or command.rstrip().endswith('\\'):
        return line

    segments = []
    while len(command) > width:
        breaks = [position for position in shell_break_positions(command) if position <= width]
        if not breaks:
            return line
        split = breaks[-1]
        segments.append(command[:split].rstrip())
        command = command[split:].lstrip()
    if not segments:
        return line
    continuation = ' \\\n' + indentation + '  '
    return indentation + continuation.join([*segments, command]) + newline

def wrap_long_shell_commands(text, width=72):
    """Reflow long lines in shell-language fenced blocks, preserving shell tokens."""
    fence = re.compile(r'(?m)^```([^\n]*)\n(.*?)^```[ \t]*$', re.DOTALL)

    def wrap_block(match):
        languages = match.group(1).strip().lower().split()
        if not languages or languages[0] not in ('sh', 'bash', 'shell'):
            return match.group(0)
        code = ''.join(wrap_shell_command_line(line, width) for line in match.group(2).splitlines(keepends=True))
        return '```' + match.group(1) + '\n' + code + '```'

    return fence.sub(wrap_block, text)


nav_script = '''
(() => {
  const nav = document.querySelector('nav[aria-label="Documentation"]');
  if (!nav) return;
  const groups = [...nav.querySelectorAll('.nav-groups > details')];
  const mobile = nav.querySelector('.mobile-menu');
  const navGroups = nav.querySelector('.nav-groups');
  const compact = window.matchMedia('(max-width: 980px)');
  const syncMobileDisclosure = () => { mobile.open = !compact.matches; };
  syncMobileDisclosure();
  compact.addEventListener('change', syncMobileDisclosure);
  const closeGroups = (except) => groups.forEach((group) => { if (group !== except) group.open = false; });
  nav.addEventListener('toggle', (event) => {
    const group = event.target;
    if (groups.includes(group) && group.open) closeGroups(group);
  }, true);
  document.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape') return;
    const group = groups.find((item) => item.open && item.contains(document.activeElement));
    if (group) {
      event.preventDefault();
      group.open = false;
      group.querySelector('summary').focus();
      return;
    }
    if (mobile?.open && (mobile.contains(document.activeElement) || navGroups.contains(document.activeElement))) {
      event.preventDefault();
      mobile.open = false;
      mobile.querySelector('summary').focus();
    }
  });
  document.addEventListener('click', (event) => {
    const open = groups.find((group) => group.open);
    if (open && !open.contains(event.target)) open.open = false;
    if (mobile?.open && !mobile.contains(event.target) && !navGroups.contains(event.target)) mobile.open = false;
  });
  document.addEventListener('focusin', (event) => {
    const open = groups.find((group) => group.open);
    if (open && !open.contains(event.target)) open.open = false;
    if (mobile?.open && !mobile.contains(event.target) && !navGroups.contains(event.target)) mobile.open = false;
  });
})();
'''

code_copy_script = '''
document.addEventListener('click', async (event) => {
  const button = event.target.closest('.copy-code-button');
  if (!button) return;
  const code = button.closest('.codehilite')?.querySelector('pre');
  if (!code) return;
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(code.innerText);
    } else {
      const field = document.createElement('textarea');
      field.value = code.innerText;
      field.setAttribute('readonly', '');
      field.style.position = 'fixed';
      field.style.opacity = '0';
      document.body.append(field);
      field.select();
      const copied = document.execCommand('copy');
      field.remove();
      if (!copied) throw new Error('Clipboard write was rejected');
    }
    button.textContent = 'Copied';
  } catch (_) {
    button.textContent = 'Copy failed';
  }
});
'''

def add_code_copy_buttons(body):
    """Add a labeled copy action to every server-rendered code block."""
    button = '<button class="copy-code-button" type="button" aria-label="Copy code" aria-live="polite">Copy code</button>'
    body = body.replace('<div class="codehilite">', '<div class="codehilite">' + button)
    return re.sub(
        r'<pre class="codehilite">(.*?)</pre>',
        lambda match: '<div class="codehilite">' + button + '<pre>' + match.group(1) + '</pre></div>',
        body,
        flags=re.DOTALL,
    )
def render_page(name, body, page_name):
    title = page_title(name) + ' · EmberDeck docs'
    description = page_descriptions[name]
    filename = name + '.html'
    canonical = site_base_url + filename
    head = (
        '<!doctype html><html lang="en"><head><meta charset="utf-8">'
        '<meta name="viewport" content="width=device-width, initial-scale=1">'
        '<meta name="color-scheme" content="dark">'
        '<title>' + escape(title) + '</title>'
        '<meta name="description" content="' + escape(description) + '">'
        '<meta property="og:title" content="' + escape(title) + '">'
        '<meta property="og:description" content="' + escape(description) + '">'
        '<meta property="og:image" content="' + escape(site_base_url + og_image_path) + '">'
        '<link rel="canonical" href="' + escape(canonical) + '">'
        '<link rel="icon" type="image/svg+xml" href="assets/favicon.svg">'
        '<style>' + style_tokens + style + nav_style + code_style + scroll_region_style + '</style></head>'
    )
    nav = make_nav(page_name)
    footer = '<footer>Documentation for v' + escape(version) + ' · <a href="https://github.com/constructorfleet/emberdeck/tree/' + revision + '">Release source</a> · <a href="LICENSE.txt">AGPL-3.0</a></footer>'
    return head + '<body>' + nav + '<main id="main-content" tabindex="-1">' + body + '</main>' + footer + '<script>' + nav_script + '</script><script>' + code_copy_script + '</script></body></html>'


for name, text in pages.items():
    # The sources are trusted repository documents, never job or plugin output.
    if name in source_paths:
        text = wrap_long_shell_commands(text)
        text = re.sub(r'\]\(([^)\s]+)\)', lambda match: rewrite_link(match, source_paths[name]), text)
    body = markdown.markdown(
        text,
        extensions=['fenced_code', 'tables', 'toc', 'codehilite'],
        extension_configs={'toc': {'permalink': True}},
    )
    body = insert_page_toc(body)
    body = enhance_operator_guide_screenshots(body, name)
    body = add_code_copy_buttons(body)
    body = add_scrollable_code_region(body)
    body = add_scrollable_table_region(body)
    (out / (name + '.html')).write_text(render_page(name, body, name), encoding='utf-8')

heading_pages = {
    page.name: page.read_text(encoding='utf-8')
    for page in out.glob('*.html')
    if page.name != '404.html'
}
(out / 'search-index.json').write_text(
    json.dumps(build_heading_index(heading_pages), ensure_ascii=False, indent=2) + '\n',
    encoding='utf-8',
)

not_found_body = markdown.markdown(
    '# Page not found\n\nThe requested page could not be found. Return to the [Home](index.html) page.\n',
    extensions=['fenced_code', 'tables', 'toc', 'codehilite'],
    extension_configs={'toc': {'permalink': True}},
)
not_found_metadata = PageMetadataParser()
not_found_metadata.feed(not_found_body)
page_headings['404'] = ' '.join(''.join(not_found_metadata.heading).split())
page_descriptions['404'] = ' '.join(not_found_metadata.description) or 'This page could not be found. Return to the EmberDeck documentation overview.'
(out / '404.html').write_text(render_page('404', not_found_body, None), encoding='utf-8')
shutil.copy('LICENSE', out / 'LICENSE.txt')
shutil.copy('THIRD_PARTY_NOTICES.txt', out / 'THIRD_PARTY_NOTICES.txt')

assets = Path('docs/assets')
if assets.exists():
    shutil.copytree(assets, out / 'assets')

validate_rendered_docs(out, routes)
