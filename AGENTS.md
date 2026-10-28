# EmberDeck agent workflow

Before planning or selecting a new unit of work, inspect the current code and
run `gh issue list --state open --limit 100 --json number,title,body,url,labels`.
Read relevant feature requests and their comments with `gh issue view` before
choosing scope. Use their current acceptance criteria; issue titles alone are
not the specification.

Issues [#3](https://github.com/constructorfleet/emberdeck/issues/3) and
[#4](https://github.com/constructorfleet/emberdeck/issues/4) are required parts of
the active product goal. Consult [the delivery plan](docs/roadmap.md) for their
relationship to the core job manager. Keep an issue open until its acceptance
criteria have direct implementation or investigation evidence, as requested by
that issue.

Use Luna agents for delegated work and assign non-overlapping file ownership.
Follow [CONTRIBUTING.md](CONTRIBUTING.md) for TDD, public test seams, build checks,
illustrated documentation, Conventional Commits, and protected PR delivery.
