"""Require accompanying explanatory docs when production Go interfaces change."""
import os
import subprocess
base, head = os.getenv('BASE'), os.getenv('HEAD')
if not base or not head:
    raise SystemExit(0)
changed = subprocess.check_output(['git', 'diff', '--name-only', f'{base}...{head}'], text=True).splitlines()
ui = [p for p in changed if p.startswith('internal/server/assets/')]
production = [p for p in changed if p.endswith('.go') and not p.endswith('_test.go')] + ui
docs = [p for p in changed if p.startswith('docs/') and p.endswith('.md') or p == 'README.md']
if production and not docs:
    raise SystemExit('Production Go changes require an accompanying docs/*.md or README.md update. Generated CLI/API indexes are rebuilt separately.')

if ui and not any(p == 'docs/operator-guide.md' or p.startswith('docs/assets/screenshots/') for p in changed):
    raise SystemExit('Console changes require an operator guide or regenerated screenshot update; browser CI validates capture from the actual UI.')
