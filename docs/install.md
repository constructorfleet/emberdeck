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
