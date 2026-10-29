# Deployment

## Documentation publishing

The published documentation is rebuilt from the source revision attached to
each release. Changes merged to `main` appear on GitHub Pages with the next
release; they are not published immediately. Every page displays the version
that was used to build it.

## Download a release

Install the published executable from
[GitHub Releases](https://github.com/ConstructorFleet/emberdeck/releases) before
building from source. Each release attaches the following native binaries and
checksum file:

| Platform | Release asset |
| --- | --- |
| macOS Intel (amd64) | `emberdeck-darwin-amd64` |
| macOS Apple silicon (arm64) | `emberdeck-darwin-arm64` |
| Linux x86-64 (amd64) | `emberdeck-linux-amd64` |
| Linux 32-bit ARM, ARMv7 (arm) | `emberdeck-linux-arm` |
| Linux 64-bit ARM (arm64) | `emberdeck-linux-arm64` |
| Windows x86-64 (amd64) | `emberdeck-windows-amd64.exe` |
| Checksums | `SHA256SUMS` |
| Linux container (amd64, arm64, arm/v7) | `ghcr.io/constructorfleet/emberdeck:vX.Y.Z` |

Download all six binary assets and `SHA256SUMS` into one directory and verify
them before execution. On Linux, run `sha256sum -c SHA256SUMS`; macOS has the
equivalent `shasum -a 256 -c SHA256SUMS`. Windows users can run `sha256sum` from
Git Bash or WSL.

For example, on Linux amd64:

```sh
sha256sum -c SHA256SUMS
chmod +x emberdeck-linux-amd64
./emberdeck-linux-amd64 -version
export EMBERDECK_API_KEY="$(openssl rand -hex 32)"
printf 'API key (keep private): %s\n' "$EMBERDECK_API_KEY"
./emberdeck-linux-amd64 serve -listen 127.0.0.1:8080 -data ./data
```

Use the binary name for your platform. On macOS, Gatekeeper may quarantine a
downloaded executable. If it blocks execution, verify the checksum, remove the
quarantine attribute from the selected binary, then make it executable. For
Apple silicon:

```sh
xattr -d com.apple.quarantine ./emberdeck-darwin-arm64
chmod +x ./emberdeck-darwin-arm64
./emberdeck-darwin-arm64 -version
```

The server requires a private `EMBERDECK_API_KEY`. Keep the displayed key
private. The native listener defaults to loopback; bind to all interfaces only
on a trusted network or behind a TLS reverse proxy. Remote workers should use
HTTPS or a trusted TLS tunnel. A raw TCP controller connection is separate from
the worker's authenticated HTTP connection.

## Build from source

The release binary is the recommended path. To compile it yourself, install Go
1.27 and clone the repository:

```sh
git clone https://github.com/ConstructorFleet/emberdeck.git
cd emberdeck
go build -o emberdeck ./cmd/emberdeck
export EMBERDECK_API_KEY="$(openssl rand -hex 32)"
printf 'API key (keep private): %s\n' "$EMBERDECK_API_KEY"
./emberdeck -version
./emberdeck serve -simulate -data ./data
```

This starts the explicitly software-only simulator. Continue with the
[Operator Guide](operator-guide.md), and read
[Safety & Limitations](safety.md) before connecting a machine.

Prometheus metrics are disabled by default and use a separate listener. See
[metrics and monitoring](metrics.md) for native, systemd, Docker, and distributed
worker configuration, authentication, scrape setup, and alerts. Do not expose
the metrics port publicly.

EmberDeck currently supports the job manager, simulator, a TCP-connected GRBL
worker, and serial-connected GRBL workers. The CLI exposes serial port
selection and enumeration. No particular cutter, controller, enclosure, or emergency-stop
setup has been qualified. The simulator is software only and must be selected
explicitly.

For a first-run route, see [Install](install.md), then read the
[Operator Guide](operator-guide.md). Keep the [Troubleshooting](troubleshooting.md),
[Glossary](glossary.md), and [Safety & Limitations](safety.md) pages nearby.

Set a long, unique `EMBERDECK_API_KEY` before starting the server. If the data
directory has no `credentials.json`, the first start stores its SHA-256 hash as
the `bootstrap-admin` credential; the key itself is not written to disk. Existing
single-key deployments keep working after this migration. Once the file exists,
the environment value is not an authentication fallback: it must match an active
stored credential when used for API calls. Changing it does not rotate a key.

After first start, create named credentials through the admin API. For example,
create a worker credential scoped to one device (the response's `key` is shown
once):

```sh
curl --fail-with-body -X POST https://emberdeck.example.net/api/v1/credentials \
  -H 'Authorization: Bearer ADMIN_KEY' -H 'Content-Type: application/json' \
  -d '{"name":"bench worker","role":"worker","driver_ids":["bench"]}'
```

Set the returned secret as the driver's `EMBERDECK_API_KEY`; keep the admin key
on the server/operator side. The server listens on loopback by default. The
Docker Compose and systemd examples bind all host interfaces; the macOS and
Windows examples bind loopback. Expose a non-loopback listener only on a trusted
network or behind a TLS reverse proxy. Remote workers should use HTTPS or a
trusted TLS tunnel. A raw TCP controller connection is separate from the
worker's authenticated HTTP connection. See
[the API credential contract](api.md#authentication-and-roles) for roles,
rotation, revocation, and recovery.

## Logs

`emberdeck serve` and `emberdeck driver` write structured logs to standard
error, so `docker logs`, `docker compose logs`, and `journalctl` collect them.
Choose the detail and format with flags or environment variables; flags win:

| Flag | Environment | Values | Default |
| --- | --- | --- | --- |
| `-log-level` | `EMBERDECK_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |
| `-log-format` | `EMBERDECK_LOG_FORMAT` | `text`, `json` | `text` |

Docker secrets are supported for every environment setting above and for
`EMBERDECK_API_KEY` and `EMBERDECK_METRICS_KEY`: set the variable name with a
`_FILE` suffix to the path containing its value (for example,
`EMBERDECK_API_KEY_FILE=/run/secrets/emberdeck_api_key`). A non-empty direct
variable takes precedence over its `_FILE` variant. One trailing line ending is
removed from a file value. An unreadable configured file stops startup with an
error. Command-line flags still take precedence over environment defaults.

At `info`, the server logs its version, data and plugin directories, listen
addresses, local-driver start and restarts, shutdown, every job state change
(with job ID, name, driver, acknowledged lines, and error), and each API request
that changes data. Failed and interrupted jobs and rejected requests (4xx) are
`warn`; server errors (5xx) and storage failures that disable the API are
`error`. Successful reads, console assets, and worker polls repeat every few
seconds, so they are logged only at `debug`. Request records include the method,
path, status, duration, and remote address; query strings, headers, and API keys
are never logged. A driver process logs its connection, each job it runs and the
job's outcome, poll retries, and reconnections.

For example, with Docker Compose:

```sh
EMBERDECK_LOG_LEVEL=debug docker compose up -d
docker compose logs -f emberdeck
```

A server log line in the default text format looks like:

```text
time=2026-10-28T12:00:00.000Z level=INFO msg="job state changed" seq=42 job_id=4f0c... name=panel.gcode from=queued to=running source=operator action=start credential_id=39120f... credential_name=bootstrap-admin driver_id=bench session_id=2e6fa5281e17051a lease_id=7e28154b21a2ff95 acked=0 total=412
```

## Docker Compose

Requires Docker with Compose v2 and a source checkout (see
[Build from source](#build-from-source)). From the repository root:

```sh
export EMBERDECK_API_KEY='replace-with-a-long-random-secret'
docker compose up --build -d
```

The service listens on port 8080 and stores jobs in the named
`emberdeck-data` volume mounted at `/data`. The image runs as the distroless
non-root user. No source tree is mounted into the container, and no simulated
driver starts by default. Open `http://localhost:8080/` (type it into your
browser) and enter the same API key in the browser.

The optional listener is not published in the default Compose configuration.
For a private monitoring setup, add `-metrics-listen 0.0.0.0:9090` to the
command, publish `127.0.0.1:9090:9090` (or attach Prometheus to a private Compose
network), and set `EMBERDECK_METRICS_KEY` to a separate secret. A commented
Compose sketch is in `compose.yaml`.

To run an explicitly software-only demo with an in-process simulator:

```sh
docker compose --profile demo up --build demo
```

That service uses a separate `emberdeck-demo-data` volume. Remove a volume only
when you intend to delete its saved jobs. For example, the regular service uses
`emberdeck-data` and the demo uses `emberdeck-demo-data`:

```sh
docker volume rm emberdeck-data
docker volume rm emberdeck-demo-data
```

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

The v0.11.0 release workflow verified native assets, GitHub Pages, and anonymous
image manifest access for the supported container architectures. Physical
machine and platform installation qualification remain separate from these
software checks.

## Linux server or Raspberry Pi

Download the matching Linux binary (`amd64`, `arm`, or `arm64`) from the release
table above. A 64-bit Raspberry Pi OS can use `emberdeck-linux-arm64`; 32-bit
ARMv7 uses `emberdeck-linux-arm`. Install the binary and systemd service as the
`emberdeck` user:

```sh
sudo useradd --system --home /var/lib/emberdeck --create-home emberdeck
sudo install -o root -g root -m 0755 ./emberdeck-linux-arm64 /usr/local/bin/emberdeck
sudo install -d -o root -g root -m 0750 /etc/emberdeck
sudo sh -c 'umask 077; printf "%s\n" "EMBERDECK_API_KEY=replace-with-a-long-random-secret" > /etc/emberdeck/emberdeck.env'
```

The service requires `/etc/emberdeck/emberdeck.env` to exist and contain a
long, unique `EMBERDECK_API_KEY`. It must be owned by `root:root` and mode
`0600`: it contains a secret, and systemd's system manager reads it before
starting the unprivileged service. A missing environment file or key prevents
startup.

Install the unit directly from this guide; no source checkout is needed:

```sh
sudo tee /etc/systemd/system/emberdeck.service >/dev/null <<'UNIT'
[Unit]
Description=EmberDeck laser job manager
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=emberdeck
Group=emberdeck
EnvironmentFile=/etc/emberdeck/emberdeck.env
ExecStart=/usr/local/bin/emberdeck serve -listen 0.0.0.0:8080 -data /var/lib/emberdeck
Restart=on-failure
RestartSec=3
StateDirectory=emberdeck
StateDirectoryMode=0750
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/emberdeck

[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable --now emberdeck
```

The unit uses systemd's `StateDirectory` for durable jobs under
`/var/lib/emberdeck` and runs without root privileges. It has no simulator. The
`0.0.0.0:8080` bind exposes the service on the host's interfaces; restrict it to
a trusted network or put it behind a TLS reverse proxy. View logs with
`journalctl -u emberdeck -f`.

An LXC container can use the same systemd unit and binary installation steps
inside a supported Linux guest. Allocate persistent storage for
`/var/lib/emberdeck`, allow outbound access to the worker/controller endpoints
that you intentionally use, and expose port 8080 only to trusted clients. The
CLI can select serial ports, but LXC USB/serial passthrough must be configured
by the host administrator before a worker can open a device.

The scheduled/manual [published install workflow](../.github/workflows/published-install-validation.yml)
installs the two newest published Linux binaries and the server/driver systemd
units inside an Ubuntu system guest with classic LXC. It verifies checksums,
first-start authentication, a worker-scoped simulated dispatch, upgrade and job
persistence, server and driver crash restarts, service startup after guest
stop/start, and uninstall while retaining saved data. [Run
40960601096](https://github.com/constructorfleet/emberdeck/actions/runs/40960601096)
passed on 2026-10-29 for `v0.17.0` to `v0.18.0` using LXC 5.0.3 and cgroup v2;
Ubuntu Noble reached systemd state `running`, and `systemd-journald` was active.
The driver uses `-simulate`, so this does not verify serial permissions or
controller behavior. It also does not qualify every LXC host, Raspberry Pi OS,
host LXC service boot integration, or physical hardware.

The same workflow also runs the published Linux ARM64 binary install smoke on
an Ubuntu x86-64 runner with QEMU user-mode emulation. This verifies the ARM64
asset checksum and runtime install/upgrade path. [Run
40961963400](https://github.com/constructorfleet/emberdeck/actions/runs/40961963400)
passed on 2026-10-29. This does not boot Raspberry Pi OS or qualify Pi-specific
services, serial permissions, GPIO, USB devices, or physical hardware. See the
[install validation scope](install.md#published-install-checks).

The LXC smoke also checks that the fixture is selected by the host's
`lxc-autostart --list` and that a host `lxc-autostart` dispatch starts it with
systemd running. [Run
40963463879](https://github.com/constructorfleet/emberdeck/actions/runs/40963463879)
passed on 2026-10-29. This does not reboot the host or verify its LXC service
boot integration.

The same LXC run verifies the live driver process has the `dialout`
supplementary GID and checks DAC access to a temporary `root:dialout` mode-0660
PTY. The `emberdeck` identity is denied without that group and allowed through
a transient systemd unit with `SupplementaryGroups=dialout`. [Run
40965092700](https://github.com/constructorfleet/emberdeck/actions/runs/40965092700)
passed this check. The test does not open the PTY through EmberDeck's serial
transport, so USB enumeration, udev ownership, modem-bit ioctls, controller
behavior, and physical serial access remain unverified.

The published container job separately maps a temporary `/dev/null`-backed
character-device node into a helper container running as UID 65532. [Run
40967145179](https://github.com/constructorfleet/emberdeck/actions/runs/40967145179)
passed the DAC denial without the device's numeric supplementary GID and the
open with that GID. This checks Docker `--device` mapping and generic Linux
character-device permissions; it does not test the published EmberDeck image,
Compose serial configuration, PTY/devpts, USB/udev, serial transport, or
physical hardware.

### Uninstall a Linux or LXC systemd install

Run these commands on the Linux host, or inside the guest for an LXC install.
Stop and remove the server and driver units you installed:

```sh
sudo systemctl disable --now emberdeck.service
sudo systemctl disable --now emberdeck-driver.service
sudo rm -f /etc/systemd/system/emberdeck.service
sudo rm -f /etc/systemd/system/emberdeck-driver.service
sudo systemctl daemon-reload
sudo systemctl reset-failed
```

If only one unit was installed, skip the commands for the other. Remove the
binary and `/etc/emberdeck` environment files separately if you no longer need
them:

```sh
sudo rm -f /usr/local/bin/emberdeck
sudo rm -f /etc/emberdeck/emberdeck.env /etc/emberdeck/driver.env
```

The environment files contain API keys; remove them only after recording any
values needed for a reinstall or credential rotation. The server's jobs,
library, and credentials are stored in `/var/lib/emberdeck`. Keep that directory
to preserve server data. Delete it only after backing up anything you need and
deciding to permanently erase the saved data:

```sh
sudo rm -rf /var/lib/emberdeck
```

Do not use `userdel -r emberdeck` as an uninstall shortcut: the system user's
home directory is `/var/lib/emberdeck`. Removing an LXC guest or its host-side
storage is a separate operation and may delete all guest data.

## macOS native CLI

Download the matching macOS asset (`emberdeck-darwin-arm64` for Apple silicon or
`emberdeck-darwin-amd64` for Intel) and verify its checksum as described above.
If Gatekeeper blocks execution, remove the quarantine attribute after checksum
verification. Example for Apple silicon:

```sh
xattr -d com.apple.quarantine ./emberdeck-darwin-arm64
chmod +x ./emberdeck-darwin-arm64
./emberdeck-darwin-arm64 -version
export EMBERDECK_API_KEY="$(openssl rand -hex 32)"
printf 'API key (keep private): %s\n' "$EMBERDECK_API_KEY"
./emberdeck-darwin-arm64 serve -listen 127.0.0.1:8080 -data "$HOME/Library/Application Support/EmberDeck"
```

Open `http://localhost:8080/` (type it into your browser). Add `-simulate` to
`serve` only for a software-only demo. Stop with Ctrl+C.

## Windows PowerShell native CLI

Download `emberdeck-windows-amd64.exe` and verify the checksum with Git Bash or
WSL as described above. Run in PowerShell:

```powershell
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$bytes = New-Object byte[] 32
$rng.GetBytes($bytes)
$env:EMBERDECK_API_KEY = [Convert]::ToBase64String($bytes)
$rng.Dispose()
Write-Host "API key (keep private): $env:EMBERDECK_API_KEY"
New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\EmberDeck" | Out-Null
.\emberdeck-windows-amd64.exe -version
.\emberdeck-windows-amd64.exe serve -listen 127.0.0.1:8080 -data "$env:LOCALAPPDATA\EmberDeck"
```

Open `http://localhost:8080/` (type it into your browser). Add `-simulate` only
to run the software-only demo. Stop with Ctrl+C.

## Distributed worker

Run the server without `-simulate`, create a worker credential for its driver ID,
then start one worker process for the simulator or a network-accessible GRBL
controller. Set the worker's environment to its dedicated worker key (not the
admin key). The worker's `-server` value must be reachable from that host. Use
an HTTPS URL or trusted TLS tunnel when the connection crosses an untrusted
network:

```sh
export EMBERDECK_API_KEY='the-one-time-worker-key-for-bench'
./emberdeck driver -server https://emberdeck.example.net -id bench -tcp 192.0.2.40:23
```

For a simulator instead of TCP hardware:

```sh
export EMBERDECK_API_KEY='the-one-time-worker-key-for-bench'
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
./emberdeck driver -server https://emberdeck.example.net -id bench -serial /dev/ttyUSB0 -baud 115200 -serial-dtr off -serial-rts off
# Alternatively, one local server and controller worker:
./emberdeck serve -serial /dev/ttyUSB0 -baud 115200 -serial-dtr off -serial-rts off -data ./data
```

Serial opens explicitly request DTR off and RTS off by default. These can be
set independently with `-serial-dtr on|off` and `-serial-rts on|off` on either
`driver` or `serve`; leave them off unless the specific controller and USB
adapter require otherwise. Asserting DTR can reset Arduino-class GRBL boards.
The serial library notes that Unix-like systems may briefly pulse modem lines
while opening a port even when the requested initial state is off, so this
policy does not guarantee that opening a port cannot reset a controller. EmberDeck
still opens a connection per job and closes it after the stream; it does not
keep a serial port persistent between jobs because stream/worker connection
ownership currently closes each connection, and changing that lifecycle safely
requires a separate design and wire validation.

If a `Grbl ` startup banner appears during job readiness, the worker refuses
the job before sending its program and reports that the controller reset and
work coordinates must be re-verified. No physical controller has been bench
confirmed for this behavior or modem-bit policy; issue #48's hardware
confirmation criterion remains open. Check the specific machine setup before
any physical operation.

In all-in-one mode, a local worker failure is logged with its transport (for
example `serial driver /dev/ttyUSB0: ...`). The driver shows as offline while the
worker restarts, and the web console stays available.

Use `emberdeck driver -list-ports` to see the names reported by the operating
system. Windows names are typically `COM3`; macOS names often begin `/dev/cu.`.
On Linux, the account running EmberDeck needs permission to open the device,
commonly through the `dialout` group or a distribution-configured udev device
group; restart its login session or service after changing group membership.
Docker needs explicit `--device` or Compose `devices` plus appropriate group
permissions; no device passthrough is configured by default. The Compose
`serial` profile below maps a Linux serial device and its numeric supplementary
group. Select the reported port and verify the required baud rate and the
controller's state. Opening some serial devices can reset a controller. These
steps expose the transport; they do not qualify a physical machine for use. See
[Troubleshooting](troubleshooting.md#serial-permission-denied) if access is denied.

The macOS CI check uses a native `/dev/ttys*` PTY and EmberDeck's production
serial opener. It verifies DAC denial for `_www` and that the owning user gets
past file access to the PTY's `ENOTTY` modem-control boundary. macOS denies
creating test aliases inside `/dev`; this does not test real `/dev/cu.*` IOKit
enumeration, USB adapter permissions, serial streaming, or physical controller
behavior. Windows COM and Raspberry Pi OS evidence remain outstanding.

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
EMBERDECK_API_KEY=the-one-time-worker-key-for-bench
EMBERDECK_SERVER_URL=https://emberdeck.example.net
EMBERDECK_DRIVER_ID=bench
EMBERDECK_DRIVER_CONNECTION=-serial /dev/ttyUSB0 -baud 115200 -serial-dtr off -serial-rts off
ENV
sudo install -o root -g root -m 0644 deploy/emberdeck-driver.service /etc/systemd/system/emberdeck-driver.service
sudo systemctl daemon-reload
sudo systemctl enable --now emberdeck-driver
journalctl -u emberdeck-driver -f
```

For a TCP controller, use `EMBERDECK_DRIVER_CONNECTION=-tcp 192.0.2.40:23`.
Keep the driver's key in this protected environment file, separate from the
server's admin key. The unit sets `Restart=always` and `RestartSec=5`, and removes systemd's
start rate limit. It adds the `dialout` supplementary group for serial access.
Change that group if your distribution assigns serial devices differently.

### Docker Compose

The `driver` profile in `compose.yaml` runs a TCP worker with
`restart: always`. Its current environment mapping passes the same
`EMBERDECK_API_KEY` as the server for legacy compatibility. For least privilege,
run a standalone worker with a worker-scoped secret (or provide a Compose
override replacing the driver's environment mapping):

```sh
export EMBERDECK_API_KEY='the-server-admin-key'
export EMBERDECK_CONTROLLER_TCP=192.0.2.40:23
# Optional: defaults are http://emberdeck:8080 (the Compose server) and bench.
export EMBERDECK_SERVER_URL=https://emberdeck.example.net
export EMBERDECK_DRIVER_ID=bench
docker compose --profile driver up --build -d driver
```

Example standalone container using a dedicated worker key:

```sh
docker run --restart unless-stopped \
  -e EMBERDECK_API_KEY='the-one-time-worker-key-for-bench' \
  ghcr.io/constructorfleet/emberdeck:vX.Y.Z \
  driver -server https://emberdeck.example.net -id bench -tcp 192.0.2.40:23
```

`EMBERDECK_CONTROLLER_TCP` is required. Without it the worker exits with a
usage error, and Compose keeps restarting it. For a Linux serial controller,
the `serial` profile configures the actual `driver -serial` command, Docker
device mapping, and numeric supplementary group. Create a worker-scoped
credential for the selected driver ID first, then set the host device and group
ID. For example:

```sh
export EMBERDECK_DRIVER_API_KEY='the-one-time-worker-key-for-bench'
export EMBERDECK_API_KEY='the-server-admin-key'
export EMBERDECK_SERVER_URL=https://emberdeck.example.net
export EMBERDECK_DRIVER_ID=bench
export EMBERDECK_SERIAL_DEVICE=/dev/ttyUSB0
export EMBERDECK_SERIAL_GID="$(stat -c '%g' "$EMBERDECK_SERIAL_DEVICE")"
export EMBERDECK_SERIAL_BAUD=115200
docker compose --profile serial up --build -d serial-driver
docker compose logs -f serial-driver
```

Compose parses the whole file even when only `serial-driver` is selected, so
`EMBERDECK_API_KEY` must also be set for interpolation; it is not passed to
this worker. The serial profile has deliberately nonfunctional placeholder
device, group, and key values when those variables are unset so ordinary
non-serial Compose use remains valid. Set the serial variables above before
starting this profile.

The group ID must match the device's owning group on the Docker host; the host
user or service manager must also have permission to access it. The profile
defaults DTR and RTS to off and supports `EMBERDECK_SERIAL_DTR` and
`EMBERDECK_SERIAL_RTS` overrides. This profile targets Linux Docker/Compose
device passthrough. Docker Desktop on macOS and Windows requires a platform
specific serial-device bridge and is not qualified by this configuration. A
successful Compose start does not qualify USB enumeration, udev rules,
modem-line behavior, or a physical controller. Retry logs appear in
`docker compose logs -f serial-driver`.

The CI [Container workflow](../.github/workflows/container.yml) checks the
profile's resolved device mapping, numeric group, worker key, and serial flags,
then starts the server and a worker with a temporary mode-0660 character
device. The first run omits the device GID and expects `permission denied`; the
second adds the configured GID and verifies EmberDeck's serial opener gets past
device access and reaches the expected `ENOTTY` termios boundary on the
`/dev/null`-backed device. This exercises Linux Compose device mapping and the
production serial opener through DAC. It does not test a serial stream, GRBL
protocol, USB/udev, modem-line behavior on a controller, or physical hardware.
Windows COM, macOS serial devices, and Raspberry Pi OS still need recorded
platform runs. [Container workflow run
40972583163](https://github.com/constructorfleet/emberdeck/actions/runs/40972583163)
passed this check on 2026-10-29.

To rotate a worker key, stop the worker and ensure it has no active job, revoke
the old credential with `DELETE /api/v1/credentials/{id}`, create a new worker
credential for the same driver ID, update its protected environment, start it,
and check `GET /api/v1/drivers` with an operator/admin credential to verify its
session. Scopes cannot overlap; revoking an active worker interrupts its lease.
See the [API guide](api.md#bootstrap-migration-and-rotation) for rotation and
bootstrap recovery details.

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
