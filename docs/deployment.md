# Deployment

Prometheus metrics are disabled by default and use a separate listener. See
[metrics and monitoring](metrics.md) for native, systemd, Docker, and distributed
worker configuration, authentication, scrape setup, and alerts. Do not expose
the metrics port publicly.

EmberDeck currently supports the job manager, simulator, a TCP-connected GRBL
worker, and serial-connected GRBL workers. The CLI exposes serial port
selection and enumeration. No particular cutter, controller, enclosure, or emergency-stop
setup has been qualified. The simulator is software only and must be selected
explicitly.

Set a long, unique `EMBERDECK_API_KEY` before starting the server. The server
listens on loopback by default; the examples below bind all interfaces. Expose
that listener only on a trusted network, or put it behind a TLS reverse proxy.
Remote workers should use HTTPS or a trusted TLS tunnel. A raw TCP controller
connection is separate from the worker's authenticated HTTP connection.

## Docker Compose

Requires Docker with Compose v2. From the repository root:

```sh
export EMBERDECK_API_KEY='replace-with-a-long-random-secret'
docker compose up --build -d
```

The service listens on port 8080 and stores jobs in the named
`emberdeck-data` volume mounted at `/data`. The image runs as the distroless
non-root user. No source tree is mounted into the container, and no simulated
driver starts by default. Open `http://localhost:8080/` and enter the same API
key in the browser.

The optional listener is not published in the default Compose configuration.
For a private monitoring setup, add `-metrics-listen 0.0.0.0:9090` to the
command, publish `127.0.0.1:9090:9090` (or attach Prometheus to a private Compose
network), and set `EMBERDECK_METRICS_KEY` to a separate secret. A commented
Compose sketch is in `compose.yaml`.

To run an explicitly software-only demo with an in-process simulator:

```sh
docker compose --profile demo up --build demo
```

That service uses a separate `emberdeck-demo-data` volume. Remove either data
volume only when you intend to delete its saved jobs (`docker volume rm ...`).

Build an image for the current platform with `docker build -t emberdeck:local .`.
For a multi-architecture image, use Buildx, for example:

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t your-registry/emberdeck:tag --push .
```

The Go binary is built with `CGO_ENABLED=0` for the Buildx target OS and
architecture. The runtime image is a static distroless Linux image.

For releases, the workflow is configured to publish a multi-architecture image
to `ghcr.io/constructorfleet/emberdeck` after the release binaries and docs
publish successfully. A release tagged `vX.Y.Z` will use both `:vX.Y.Z` and
`:latest`, for `linux/amd64`, `linux/arm64`, and `linux/arm/v7`. To use a
published release image, replace the Compose service's `build: .` with:

```yaml
image: ghcr.io/constructorfleet/emberdeck:vX.Y.Z
```

The v0.1.0 release verified native assets, GitHub Pages, and anonymous image
manifest access for all three container architectures. Physical machine and
platform installation qualification remain separate from these software checks.

## Linux server or Raspberry Pi

Build for the target architecture with a Go 1.27 toolchain, or copy a matching
release binary to `/usr/local/bin/emberdeck`. For a 64-bit ARM Pi, build with
`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o emberdeck ./cmd/emberdeck`.
For 32-bit ARM, use the appropriate `GOARCH=arm` and `GOARM` for the installed
OS; 64-bit Raspberry Pi OS is the simpler deployment target. Then:

```sh
sudo useradd --system --home /var/lib/emberdeck --create-home emberdeck
sudo install -o root -g root -m 0755 emberdeck /usr/local/bin/emberdeck
sudo install -d -o root -g root -m 0755 /etc/emberdeck
sudo sh -c 'umask 077; printf "%s\n" "EMBERDECK_API_KEY=replace-with-a-long-random-secret" > /etc/emberdeck/emberdeck.env'
sudo install -o root -g root -m 0644 deploy/emberdeck.service /etc/systemd/system/emberdeck.service
sudo systemctl daemon-reload
sudo systemctl enable --now emberdeck
```

The unit uses systemd's `StateDirectory` to keep durable jobs under
`/var/lib/emberdeck` and runs without root privileges. View logs with
`journalctl -u emberdeck -f`. Its default configuration has no simulator.

An LXC container can use the same systemd unit and binary installation steps
inside a supported Linux guest. Allocate persistent storage for
`/var/lib/emberdeck`, allow outbound access to the worker/controller endpoints
that you intentionally use, and expose port 8080 only to trusted clients. The
CLI can select serial ports, but LXC USB/serial passthrough must be configured
by the host administrator before a worker can open a device.

## macOS native CLI

Install Go 1.27, then build and run the server from a terminal:

```sh
go build -o emberdeck ./cmd/emberdeck
export EMBERDECK_API_KEY='replace-with-a-long-random-secret'
./emberdeck serve -listen 127.0.0.1:8080 -data "$HOME/Library/Application Support/EmberDeck"
```

Open `http://localhost:8080/`. Add `-simulate` to `serve` only for a
software-only demo. Stop with Ctrl+C.

## Windows PowerShell native CLI

Install Go 1.27 and run in PowerShell from the repository root:

```powershell
go build -o emberdeck.exe ./cmd/emberdeck
$env:EMBERDECK_API_KEY = 'replace-with-a-long-random-secret'
New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\EmberDeck" | Out-Null
./emberdeck.exe serve -listen 127.0.0.1:8080 -data "$env:LOCALAPPDATA\EmberDeck"
```

Open `http://localhost:8080/`. Add `-simulate` only to run the software-only
demo. Stop with Ctrl+C.

## Distributed worker

Run the server without `-simulate`, then start one worker process for the
simulator or a network-accessible GRBL controller. The worker's `-server` value
must be reachable from that host. Use an HTTPS URL or trusted TLS tunnel when
the connection crosses an untrusted network:

```sh
export EMBERDECK_API_KEY='the-same-server-key'
./emberdeck driver -server https://emberdeck.example.net -id bench -tcp 192.0.2.40:23
```

For a simulator instead of TCP hardware:

```sh
./emberdeck driver -server https://emberdeck.example.net -id bench -simulate
```

`-tcp` expects an already network-accessible GRBL-compatible endpoint. It does
not open a local serial port, configure a USB adapter, or establish TLS to the
controller. The software driver and simulator have not been qualified against
physical hardware; maintain independent enclosure interlocks and emergency
stop controls.

## Serial workers and all-in-one hardware mode

List ports using `emberdeck driver -list-ports`. Then select the correct device:

```sh
./emberdeck driver -server https://emberdeck.example.net -id bench -serial /dev/ttyUSB0 -baud 115200
# Alternatively, one local server and controller worker:
./emberdeck serve -serial /dev/ttyUSB0 -baud 115200 -data ./data
```

In all-in-one mode, a local worker failure is logged with its transport (for
example `serial driver /dev/ttyUSB0: ...`). The driver shows as offline while the
worker restarts, and the web console stays available.

Windows names are typically `COM3`; macOS names often begin `/dev/cu.`. Use the
OS-reported name instead of assuming either convention. The Linux service user
needs access to the selected device (see [serial transport](serial.md)). Docker
needs explicit `--device` or Compose `devices` plus appropriate group permissions;
no device passthrough is configured by default. These commands expose the
transport; they do not qualify a physical machine for use.

## Driver as a service

A standalone `emberdeck driver` retries when idle, so a server upgrade or
network drop does not need operator action. It exits only on a configuration
error or after failing closed during an active job (see
[worker reconnection](worker.md#reconnection)). Run it under a supervisor
that always restarts it.

### systemd

Install the binary and `emberdeck` user as in the Linux section above, then:

```sh
sudo sh -c 'umask 077; cat > /etc/emberdeck/driver.env' <<'ENV'
EMBERDECK_API_KEY=the-same-server-key
EMBERDECK_SERVER_URL=https://emberdeck.example.net
EMBERDECK_DRIVER_ID=bench
EMBERDECK_DRIVER_CONNECTION=-serial /dev/ttyUSB0 -baud 115200
ENV
sudo install -o root -g root -m 0644 deploy/emberdeck-driver.service /etc/systemd/system/emberdeck-driver.service
sudo systemctl daemon-reload
sudo systemctl enable --now emberdeck-driver
journalctl -u emberdeck-driver -f
```

For a TCP controller, use `EMBERDECK_DRIVER_CONNECTION=-tcp 192.0.2.40:23`.
The unit sets `Restart=always` and `RestartSec=5`, and removes systemd's
start rate limit. It adds the `dialout` supplementary group for serial access.
Change that group if your distribution assigns serial devices differently.

### Docker Compose

The `driver` profile in `compose.yaml` runs a TCP worker with
`restart: always`:

```sh
export EMBERDECK_API_KEY='the-same-server-key'
export EMBERDECK_CONTROLLER_TCP=192.0.2.40:23
# Optional: defaults are http://emberdeck:8080 (the Compose server) and bench.
export EMBERDECK_SERVER_URL=https://emberdeck.example.net
export EMBERDECK_DRIVER_ID=bench
docker compose --profile driver up --build -d driver
```

`EMBERDECK_CONTROLLER_TCP` is required. Without it the worker exits with a
usage error, and Compose keeps restarting it. For a serial controller, add the
device and group to a Compose override, and replace the command's `-tcp` pair
with `-serial` and the device path. Retry logs appear in
`docker compose logs -f driver`.

A restarted worker uses a new session. After a crash, its first polls can log
HTTP 409 `driver ID already owned by another session` for up to 10 seconds
before it takes over the driver ID. This is expected.

## Upgrades and mixed versions

Stop the server and all workers and inspect the machine before an upgrade or
rollback. Paused work remains an active job and must not be treated as a safe
restart point. A current server interrupts every active control state on restart;
older releases do not understand the new paused/resuming records, so complete or
cancel work before downgrading and retain a stopped-server data backup.

Designer data adds `profiles.json`, `designs.json`, and generated source/profile
snapshots in `jobs.json`. Back up the whole stopped-server data directory, not
just the job file. Older servers cannot manage editable projects and can discard
generated-job provenance when rewriting their older job model. Use a separate
data directory restored from a backup matching that server version for rollback;
do not alternate old and new servers against the same writable library.

Older workers can still execute jobs on a current server, but confirmed
pause/resume is offered only when the worker advertises that capability. Upgrade
the server before a new worker: an older server rejects the new worker metadata,
and the worker fails closed rather than ignoring an incompatible control contract.
