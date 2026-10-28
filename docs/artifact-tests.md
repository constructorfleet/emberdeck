# Shipped binary smoke test

`scripts/artifact-smoke.py` exercises a supplied EmberDeck executable through
its public HTTP API and GRBL TCP connection. It starts the binary's built-in
server and local worker, uploads one source `G1` line, and connects a
deterministic fake GRBL peer. The peer accepts the source and cleanup `M5`
while remaining in `Run`, confirms `Hold:0`, and then reports fresh `Run` and
`Idle` statuses after the operator resumes.

The smoke checks the reported version, `pause_resume` discovery, acknowledged
source-line totals, live paused heartbeats, and the actual realtime `!` and `~`
bytes. It verifies the job stays assigned at `Hold:0`, resumes on the same
connection, finishes only after a fresh `Idle`, and does not replay source or
cleanup G-code. This models GRBL's distinction between an `ok` acknowledgment
and the end of physical motion. It does not connect to physical hardware or
certify machine safety.

Run it against a local development build:

```sh
go build -o bin/ ./cmd/emberdeck
python3 scripts/artifact-smoke.py bin/emberdeck --expected-version dev
```

Pass the exact expected version when checking another build. On Windows, pass
the `.exe` file as the binary path. The test uses loopback ports, a temporary
data directory, and a test-only API key; it makes no external network requests.

`scripts/check.sh` runs the smoke test after building the native command. The
release workflow runs it against the Linux amd64 release binary before
calculating checksums and attaching release assets.
