"""Build a dependency-free, escaped documentation bundle from release sources."""
from pathlib import Path
from html import escape
import shutil
out = Path("dist/docs")
out.mkdir(parents=True, exist_ok=True)
source = [Path("README.md"), *sorted(Path("docs").glob("*.md"))]
links = []
for path in source:
    name = "index.html" if path.name == "README.md" else path.stem + ".html"
    links.append(f'<li><a href="{name}">{escape(path.stem)}</a></li>')
    (out / name).write_text('<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>EmberDeck — ' + escape(path.stem) + '</title><style>body{max-width:80ch;margin:3rem auto;padding:1rem;font:16px/1.6 system-ui}pre{white-space:pre-wrap}a{color:#b43d00}</style><nav><a href="contents.html">Documentation</a> · <a href="https://github.com/ConstructorFleet/emberdeck">Source</a></nav><pre>' + escape(path.read_text()) + '</pre>')
(out / "contents.html").write_text('<!doctype html><title>EmberDeck docs</title><h1>EmberDeck documentation</h1><ul>' + ''.join(links) + '</ul>')
shutil.copy("LICENSE", out / "LICENSE.txt")
