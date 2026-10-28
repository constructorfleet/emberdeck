# EmberDeck

**Your laser cutter's command deck.** Self-hosted job management with an embedded
browser console, distributed controller workers, and trusted process plugins.
One Go executable runs on Raspberry Pi, macOS, Linux, and Windows, or in Docker
and LXC. No Node runtime, frontend build, cloud account, or external database.

**Early development: simulator verified, physical hardware not yet qualified.**
GRBL 1.1 is the first protocol. Proprietary DSP controllers (including Ruida) are
not supported. EmberDeck is not a safety controller: supervise cutting and use
hardware interlocks and an emergency stop.

## Try it without hardware

Install Go 1.27 and build:

```sh
go build -o bin/emberdeck ./cmd/emberdeck
export EMBERDECK_API_KEY="$(openssl rand -hex 32)"
bin/emberdeck serve -simulate -data ./data
```

Open <http://127.0.0.1:8080>, enter the key, and upload a `.gcode` file. Select the
simulator, confirm readiness, and start a job. Progress counts acknowledged lines;
completion requires the controller to report Idle. Cancel is a best-effort
software stop, not a physical emergency stop.

```gcode
G21
G90
G1 X10 Y10 F100
M5
```

To split server and driver, run `serve` on the server host and a worker on the
machine host:

```sh
# Simulator worker; use HTTPS for remote connections.
bin/emberdeck driver -server http://127.0.0.1:8080 -id bench -simulate
# Serial worker (experimental, unqualified on physical hardware).
bin/emberdeck driver -server http://127.0.0.1:8080 -id bench -serial /dev/ttyUSB0 -baud 115200
```

`serve -serial PORT` or `serve -tcp HOST:PORT` runs an all-in-one physical driver.
`driver -list-ports` lists OS-reported serial ports. Opening a port can reset some
controllers; always inspect controller state and manufacturer instructions.

## Available now

- Durable upload, list, program view, deletion, explicit start, cancellation,
  line progress, controller completion, and failure reporting.
- One active job per online driver; assignment leases and unique worker sessions.
- Fail-closed worker behavior on server communication failures. Restarted or
  disconnected work becomes interrupted, with no automatic replay.
- GRBL send/response over serial or TCP and the same wire-boundary simulator.
- Versioned process plugins that can veto preflight. No OctoPrint plugin ABI.
- Required PR checks across Linux, macOS, Windows, plus container smoke testing.
  Release Please maintains the pending release; releases build native binaries,
  documentation, and container images.

## Documentation and development

[Deployment](docs/deployment.md) · [API](docs/api.md) ·
[Driver](docs/driver.md) · [Serial](docs/serial.md) · [Plugins](docs/plugins.md) ·
[Architecture](docs/architecture.md) · [Roadmap](docs/roadmap.md)

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r docs/requirements.txt
scripts/check.sh
```

This runs formatting, vet, race-enabled tests, command builds, and documentation
rendering. The docs include CLI help from that build and an API route index from
registered routes. Update explanatory docs in the same PR as behavior changes.
See [contributing](CONTRIBUTING.md) for the TDD and Conventional Commit workflow.

## License

AGPL-3.0-only. Commercial use, paid hosting, and support are permitted under the
license terms. Alternative proprietary licensing requires agreement from all
relevant copyright holders; this repository does not promise that right.
