"""Keep bundled notices aligned with Go modules linked into the shipped binary."""
import os
from pathlib import Path
import subprocess
import sys

fmt = '{{if .Module}}{{if not .Module.Main}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}{{end}}'
# Release binaries are built for macOS, Linux, and Windows, and some modules are
# only linked on one platform (for example the Prometheus procfs collector on
# Linux). Use the union so the notice file, and this check, agree everywhere.
modules = set()
for goos in ('darwin', 'linux', 'windows'):
    result = subprocess.check_output(
        ['go', 'list', '-deps', '-f', fmt, './cmd/emberdeck'],
        env={**os.environ, 'GOOS': goos},
        text=True,
    )
    modules.update(line for line in result.splitlines() if line)
modules = sorted(modules)
parts = ['Third-party notices for EmberDeck\n\nEmberDeck itself is AGPL-3.0-only; see LICENSE. The following original\nnotices accompany code linked into its executable.\n']
goroot = Path(subprocess.check_output(['go', 'env', 'GOROOT'], text=True).strip())
go_license = goroot / 'LICENSE'
if not go_license.is_file():
    go_license = goroot.parent / 'LICENSE'  # Homebrew places it outside libexec.
parts.append('\n=== Go runtime and standard library ===\n\n' + go_license.read_text(encoding='utf-8'))
for module in modules:
    name, version, directory = module.split('|', 2)
    license_file = Path(directory) / 'LICENSE'
    if not license_file.is_file():
        license_file = Path(directory) / 'LICENSE.txt'
    if not license_file.is_file():
        raise SystemExit(f'Review license file for {name} {version}')
    parts.append(f'\n=== {name} {version} ===\n\n' + license_file.read_text(encoding='utf-8'))
expected = '\n'.join(parts)
target = Path('THIRD_PARTY_NOTICES.txt')
if '--write' in sys.argv:
    target.write_text(expected, encoding='utf-8')
elif not target.is_file() or target.read_text(encoding='utf-8') != expected:
    raise SystemExit('Dependency notices changed: review and run python3 scripts/check-notices.py --write')
