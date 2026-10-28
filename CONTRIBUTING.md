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
