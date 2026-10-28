"""Render release docs, with API route index and CLI help derived from source/build."""
from pathlib import Path
from html import escape
from html.parser import HTMLParser
import os
import re
import shutil
import subprocess
import markdown
from urllib.parse import urlsplit, unquote
from docs_validation import validate_rendered_docs

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
        routes.update(re.findall(r'\.HandleFunc\("([A-Z]+ [^"\n]+)"', path.read_text()))
pages['api-routes'] = '# API route index\n\nGenerated from the registered server routes in this release. All API routes require bearer authentication.\n\n' + '\n'.join('- `' + route + '`' for route in sorted(routes)) + '\n\nSee [API contracts](api.html) for request shapes and state transitions.\n'
exe = Path('bin/emberdeck' + ('.exe' if os.name == 'nt' else '')).resolve()
help_text = []
for command in ['serve', 'driver']:
    result = subprocess.run([str(exe), command, '-h'], text=True, capture_output=True, check=True)
    help_text.append('## ' + command + '\n\n```text\n' + result.stdout + result.stderr + '```\n')
pages['cli'] = '# CLI reference\n\nGenerated by the executable built from this release. Use `emberdeck -version` to print its version.\n\n' + '\n'.join(help_text)

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

    def handle_starttag(self, tag, attrs):
        if tag == 'h1' and not self.heading:
            self.in_heading = True
        elif tag == 'p' and not self.description and not self.blocked_before_description:
            self.in_paragraph = True
            self.paragraph = []
        elif tag in ('pre', 'table') and not self.description and not self.in_heading:
            self.blocked_before_description = True

    def handle_endtag(self, tag):
        if tag == 'h1':
            self.in_heading = False
        elif tag == 'p' and self.in_paragraph:
            text = ' '.join(''.join(self.paragraph).split())
            if text:
                self.description = [text]
            self.in_paragraph = False

    def handle_data(self, data):
        if self.in_heading:
            self.heading.append(data)
        if self.in_paragraph:
            self.paragraph.append(data)


page_headings, page_descriptions, fallback_pages = {}, {}, []
for name, text in pages.items():
    rendered = markdown.markdown(text, extensions=['fenced_code', 'tables', 'toc'])
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
    'Deploy & Run': ['deployment', 'metrics', 'troubleshooting', 'glossary', 'safety'],
    'Develop & Extend': ['CONTRIBUTING', 'architecture', 'plugins', 'browser-tests', 'artifact-tests', 'roadmap', 'designer-architecture', 'esp32-feasibility', 'driver', 'worker', 'serial'],
    'Reference': ['api', 'api-routes', 'cli'],
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
style = '''*{box-sizing:border-box}body{margin:0;background:#10161e;color:#dae3ec;font:16px/1.7 system-ui,-apple-system,sans-serif}a{color:#ffb171;text-underline-offset:.18em}a:hover{color:#ffd0a4}a:focus-visible,summary:focus-visible{outline:3px solid #ffd18f;outline-offset:3px;border-radius:3px}.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}.skip-link{position:absolute;left:1rem;top:-5rem;z-index:10;padding:.65rem 1rem;background:#fff;color:#10161e;border-radius:4px}.skip-link:focus{top:1rem}nav{position:relative;z-index:2;display:flex;flex-wrap:wrap;justify-content:space-between;gap:.75rem;padding:.8rem max(1rem,4vw);background:#1a2532;align-items:center;border-bottom:1px solid #435264}nav .brand{font-weight:800;font-size:1.3rem;text-decoration:none;color:#fff}nav .version{color:#b6c4d2;font-size:.9rem}nav details{position:relative;min-width:0}nav summary{display:flex;flex-wrap:wrap;align-items:baseline;gap:.4rem;cursor:pointer;list-style:none;padding:.5rem .7rem;border-radius:4px;font-weight:650;color:#eef4fa}nav summary::-webkit-details-marker{display:none}nav summary::after{content:"▾";margin-left:.15em;color:#ffb171}.current-page{font-size:.76rem;color:#ffb171;font-weight:500}nav details[open] summary{background:#2a3949}nav ul{position:absolute;top:100%;right:0;min-width:15rem;margin:.3rem 0 0;padding:.4rem;list-style:none;background:#1a2532;border:1px solid #536579;border-radius:6px;box-shadow:0 8px 24px #0008}nav li a{display:block;padding:.45rem .65rem;border-radius:3px;text-decoration:none}nav li a:hover,nav li a[aria-current="page"]{background:#304256;color:#fff}nav li a[aria-current="page"]{box-shadow:inset 3px 0 #ffb171;font-weight:700}main{max-width:78ch;margin:2.5rem auto;padding:0 1.5rem;overflow-wrap:anywhere}h1{font-size:clamp(2rem,4vw,2.7rem);margin-top:0}h2{margin-top:2.5rem}h1,h2,h3{line-height:1.25;color:#fff}p,li{max-width:75ch}pre{overflow:auto;background:#1a2532;padding:1.2rem;border-radius:8px;border:1px solid #435264}code{font-size:.9em;overflow-wrap:anywhere}img{display:block;max-width:100%;height:auto;border:1px solid #536579;border-radius:8px;margin:1.2rem 0}a.screenshot-full-size{position:relative;left:50%;transform:translateX(-50%);display:block;width:fit-content;max-width:min(1200px,calc(100vw - 2rem));margin:1.2rem 0}img.documentation-screenshot{margin:0}img.documentation-screenshot-mobile{width:auto;max-height:min(75vh,720px);object-fit:contain;margin-inline:auto}table{display:block;overflow-x:auto;border-collapse:collapse;max-width:100%;width:max-content}td,th{padding:.6rem;border:1px solid #435264;text-align:left}footer{margin:3rem 0;border-top:1px solid #435264;padding-top:1rem;color:#b6c4d2}@media(max-width:740px){nav{display:grid;grid-template-columns:1fr auto;gap:.3rem .4rem;padding:.7rem 1rem}nav .brand{grid-column:1;grid-row:1}nav .version{grid-column:2;grid-row:1;margin:0;text-align:right}nav details{grid-column:1/-1;min-width:0}nav summary{padding:.6rem .45rem;font-size:.92rem;flex-wrap:wrap;gap:0 .35rem}nav details[open]{grid-column:1/-1}nav ul{position:static;min-width:0;margin:.2rem 0 .5rem;box-shadow:none}main{margin:1.75rem auto;padding:0 1rem}pre{padding:.85rem}img{margin:.9rem 0}a.screenshot-full-size{max-width:calc(100vw - 2rem);margin:.9rem 0}}'''

nav_style = '''
nav{display:block;padding:0;background:#1a2532}
.nav-inner{display:grid;grid-template-columns:1fr auto;grid-template-rows:auto auto;align-items:center;gap:.25rem 1rem;width:100%;max-width:78ch;margin:0 auto;padding:.55rem 1.5rem}
nav .brand{grid-column:1;grid-row:1;font-size:1.3rem}
nav .version{grid-column:2;grid-row:1}
.mobile-menu{display:contents}.mobile-menu>summary{display:none}
.nav-groups{grid-column:1/-1;grid-row:2;display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:.2rem;align-items:start}
nav summary{align-items:baseline;gap:.2rem .35rem;padding:.4rem .35rem}
.current-page{max-width:11ch;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.nav-groups>details>ul{position:absolute;z-index:3;top:100%;left:0;right:auto;width:max-content;min-width:min(15rem,calc(100vw - 2rem));max-width:calc(100vw - 2rem)}
.nav-groups>details:last-child>ul{left:auto;right:0}
@media(max-width:980px){
 .nav-inner{grid-template-columns:minmax(0,1fr) auto auto;grid-template-rows:auto;gap:.4rem .65rem;padding:.5rem 1rem}
 nav .brand{grid-column:1;grid-row:1;font-size:1.2rem}nav .version{grid-column:2;grid-row:1}
 .mobile-menu{display:contents}.mobile-menu>summary{display:flex;grid-column:3;grid-row:1;align-items:center;padding:.5rem .65rem}
 .mobile-menu>summary::-webkit-details-marker{display:none}.mobile-menu>summary::after{content:"▾";margin-left:.45rem;color:#ffb171}
 .nav-groups{display:none;grid-column:1/-1;grid-row:2;grid-template-columns:minmax(0,1fr);gap:.1rem;align-items:stretch}
  .mobile-menu[open]~.nav-groups{display:grid}
 .nav-groups>details{position:static;grid-column:auto;width:100%}
 .nav-groups>details>summary{width:100%;padding:.55rem .5rem}
 .nav-groups>details>ul{position:static;width:auto;min-width:0;max-width:100%;margin:.15rem 0 .4rem;box-shadow:none}
 main{margin:1.75rem auto;padding:0 1rem}pre{padding:.85rem}img{margin:.9rem 0}a.screenshot-full-size{width:calc(100vw - 2rem);max-width:calc(100vw - 2rem);overflow:auto;overscroll-behavior-inline:contain;margin:.9rem 0}img.documentation-screenshot{width:auto;max-width:none;max-height:min(75vh,720px);object-fit:contain;margin:0 auto}
}
'''

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
def render_page(name, body, page_name):
    title = page_title(name) + ' · EmberDeck docs'
    description = page_descriptions[name]
    filename = name + '.html'
    canonical = site_base_url + filename
    head = (
        '<!doctype html><html lang="en"><head><meta charset="utf-8">'
        '<meta name="viewport" content="width=device-width">'
        '<title>' + escape(title) + '</title>'
        '<meta name="description" content="' + escape(description) + '">'
        '<meta property="og:title" content="' + escape(title) + '">'
        '<meta property="og:description" content="' + escape(description) + '">'
        '<meta property="og:image" content="' + escape(site_base_url + og_image_path) + '">'
        '<link rel="canonical" href="' + escape(canonical) + '">'
        '<link rel="icon" type="image/svg+xml" href="assets/favicon.svg">'
        '<style>' + style + nav_style + '</style></head>'
    )
    nav = make_nav(page_name)
    footer = '<footer>Documentation for v' + escape(version) + ' · <a href="https://github.com/constructorfleet/emberdeck/tree/' + revision + '">Release source</a> · <a href="LICENSE.txt">AGPL-3.0</a></footer>'
    return head + '<body>' + nav + '<main id="main-content" tabindex="-1">' + body + footer + '</main><script>' + nav_script + '</script></body></html>'


for name, text in pages.items():
    # The sources are trusted repository documents, never job or plugin output.
    if name in source_paths:
        text = re.sub(r'\]\(([^)\s]+)\)', lambda match: rewrite_link(match, source_paths[name]), text)
    body = markdown.markdown(text, extensions=['fenced_code', 'tables', 'toc'])
    body = enhance_operator_guide_screenshots(body, name)
    (out / (name + '.html')).write_text(render_page(name, body, name), encoding='utf-8')

not_found_body = markdown.markdown(
    '# Page not found\n\nThe requested page could not be found. Return to the [Home](index.html) page.\n',
    extensions=['fenced_code', 'tables', 'toc'],
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

validate_rendered_docs(out)
