# EmberDeck

Your laser cutter's command deck. A portable, self-hosted job manager with an
embedded browser UI, local or distributed drivers, and versioned plugins.

**Under active development. No physical hardware has been qualified yet.**

## Development

Requires Go 1.27. Run `go build ./...`, `go test -race ./...`, and `go vet ./...`.
Changes use test-first vertical slices through public interfaces.

See [architecture](docs/architecture.md), [delivery plan](docs/roadmap.md), and
[contributing](CONTRIBUTING.md). Each PR must pass CI before merging to main.

## License

AGPL-3.0-only. Commercial use, paid hosting, and support are permitted under the
license terms. Alternative proprietary licensing requires agreement from all
relevant copyright holders; this repository does not promise that right.
