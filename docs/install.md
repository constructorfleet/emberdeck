# Install

The quickest install is a prebuilt release binary. Choose the asset for your
operating system and CPU architecture from
[GitHub Releases](https://github.com/ConstructorFleet/emberdeck/releases). The
release also publishes the multi-architecture Linux image
`ghcr.io/constructorfleet/emberdeck:vX.Y.Z` (amd64, arm64, and arm/v7); `:latest`
tracks the latest release.

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

Download all six binaries and `SHA256SUMS` to the same directory. Verify them
before running a binary:

```sh
sha256sum -c SHA256SUMS
```

macOS provides the equivalent `shasum -a 256 -c SHA256SUMS`; Windows users can
run `sha256sum -c SHA256SUMS` from Git Bash or WSL. For a Linux amd64 install,
make the binary executable, check its version, then create and display a private
API key before starting a software-only demo:

```sh
chmod +x emberdeck-linux-amd64
./emberdeck-linux-amd64 -version
export EMBERDECK_API_KEY="$(openssl rand -hex 32)"
printf 'API key (keep private): %s\n' "$EMBERDECK_API_KEY"
./emberdeck-linux-amd64 serve -simulate -data ./data
```

Use the matching asset name in the commands above. If Gatekeeper blocks a
downloaded macOS binary after checksum verification, remove its quarantine
attribute. For Apple silicon:

```sh
xattr -d com.apple.quarantine ./emberdeck-darwin-arm64
chmod +x ./emberdeck-darwin-arm64
./emberdeck-darwin-arm64 -version
```

The first server start migrates the supplied key into a `bootstrap-admin`
credential. Keep the displayed key private. Once initial access is established,
rotate to named credentials; see the
[HTTP API and worker contract](api.md) for the credential contract. Open
`http://127.0.0.1:8080` (type it into your browser) and enter the displayed key.
The simulator is software only and sends no commands to a physical cutter.
For Windows PowerShell, see the
[native CLI steps in Deployment](deployment.md#windows-powershell-native-cli).

## Published install checks

The [Published install validation workflow](../.github/workflows/published-install-validation.yml)
runs weekly and can be started manually. It downloads the two newest published
binary releases and verifies their SHA-256 manifests on Ubuntu x86-64, macOS
Apple silicon, Windows x86-64, and Linux ARM64 under QEMU user-mode emulation on
an Ubuntu x86-64 runner. It launches the prior binary, authenticates
with a test key, uploads and dispatches a simulated job, then starts the current
binary against the same data directory to check upgrade and restart
persistence. It also runs the released binary through the fake-GRBL artifact
smoke test and removes the temporary install directory.

The workflow also pulls the two newest published GHCR release images as `linux/amd64`
and checks each image's version. It starts the prior image, verifies
unauthenticated access is rejected and authenticated access succeeds, dispatches
a simulated job, restarts the container, then replaces it with the current image
using the same named data volume. A second job and restart check the upgraded
image; the workflow removes the test container and volume afterward. This
validates published Docker images on an Ubuntu x86-64 runner. The separate
[Container workflow](../.github/workflows/container.yml) builds from the current
checkout and exercises local image behavior.

The ARM64 run executes the published `emberdeck-linux-arm64` release asset and
checks its checksum, version, authentication, simulated dispatch, upgrade, and
data persistence. [Run 40961963400](https://github.com/constructorfleet/emberdeck/actions/runs/40961963400)
passed on 2026-10-29. QEMU user-mode emulation does not boot an ARM64 kernel or
Raspberry Pi OS and does not validate Pi-specific services, serial permissions,
GPIO, USB devices, or physical hardware. Raspberry Pi OS still needs a recorded
native install run.

The scheduled/manual published-install workflow installs the two newest
published Linux binaries and both systemd units inside an Ubuntu guest under
classic LXC. It checks authentication, simulated dispatch, upgrade and saved
jobs, server/driver crash restart, service startup after guest stop/start, and
uninstall with data retained. [Run
40960601096](https://github.com/constructorfleet/emberdeck/actions/runs/40960601096)
passed for `v0.17.0` to `v0.18.0` on LXC 5.0.3 with cgroup v2. The driver uses
`-simulate`, so this is not serial or physical controller evidence, and guest
stop/start does not verify host LXC autostart. The workflow also verifies that
the fixture is selected by `lxc-autostart --list` and that a host
`lxc-autostart` dispatch starts it with systemd running. This exercises the
host command path but does not reboot the runner or verify LXC service boot
integration. Raspberry Pi OS, serial device permissions, Windows COM ports, and
macOS serial devices still need recorded platform runs; simulator and
fake-controller results are not physical hardware evidence.

[Run 40963463879](https://github.com/constructorfleet/emberdeck/actions/runs/40963463879)
passed on 2026-10-29. Its LXC job installed `v0.17.0` then upgraded to
`v0.18.0` and reported host autostart selection/dispatch with systemd running.

## Build from source

To build rather than download, install Go 1.27 and clone the repository:

```sh
git clone https://github.com/ConstructorFleet/emberdeck.git
cd emberdeck
go build -o emberdeck ./cmd/emberdeck
export EMBERDECK_API_KEY="$(openssl rand -hex 32)"
printf 'API key (keep private): %s\n' "$EMBERDECK_API_KEY"
./emberdeck -version
./emberdeck serve -simulate -data ./data
```

The first start creates the `bootstrap-admin` credential from the displayed key.
Rotate to named credentials after initial access; see the
[HTTP API and worker contract](api.md). Then follow the
[Operator Guide](operator-guide.md) for the console workflow. For deployment
options, see [Deployment](deployment.md); review
[Troubleshooting](troubleshooting.md), [Glossary](glossary.md), and
[Safety & Limitations](safety.md) before connecting a machine.
