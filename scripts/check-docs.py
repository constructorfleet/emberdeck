"""Require accompanying explanatory docs when production Go interfaces change."""
import os
import subprocess
base, head = os.getenv('BASE'), os.getenv('HEAD')
if not base or not head:
    raise SystemExit(0)
changed = subprocess.check_output(['git', 'diff', '--name-only', f'{base}...{head}'], text=True).splitlines()
production = [p for p in changed if p.endswith('.go') and not p.endswith('_test.go')]
docs = [p for p in changed if p.startswith('docs/') and p.endswith('.md') or p == 'README.md']
if production and not docs:
    raise SystemExit('Production Go changes require an accompanying docs/*.md or README.md update. Generated CLI/API indexes are rebuilt separately.')
