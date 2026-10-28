# Contributing

Use a feature branch and open a pull request targeting main. Direct pushes to
main are prohibited by branch protection. Use Conventional Commit PR titles and
commit messages, for example `feat: persist uploaded jobs`. Squash merging uses
the PR title. Run `scripts/check.sh` before pushing (install `docs/requirements.txt` in a
Python virtual environment first). Browser tests have their own documented
command in `docs/browser-tests.md`.

Write one failing behavior test, observe failure, implement the slice, then run
the full checks before committing. Public test seams: HTTP API, browser UI,
driver wire protocol, and versioned plugin API. Tests must survive internal
refactoring. Simulated hardware is not evidence of physical machine safety.

Documentation lives with code in `docs/` and README.md. Update it in the same PR
as behavior changes. CI builds the docs; each release deploys that release's
version to GitHub Pages. Never document planned behavior as available.

When updating linked Go dependencies, review their licenses and regenerate
`THIRD_PARTY_NOTICES.txt` with `python3 scripts/check-notices.py --write`. CI
compares notices with the modules actually linked into the shipped command.

Before planning a feature, inspect open GitHub issues and read relevant request
bodies/comments. Issues #3 and #4 are required product deliverables; their current
acceptance criteria and the delivery plan govern the scope.

Operator-facing controls belong in the illustrated operator guide. Regenerate
its screenshots with `npm run docs:screenshots` after UI changes. Browser CI
exercises the capture; release publishing regenerates screenshots from the
released UI before rendering and deploying documentation. The docs build checks
referenced image files and their alt text.
