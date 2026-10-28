# Process plugins

EmberDeck process plugins use protocol version 1. The host discovers plugins
only in immediate child directories of an explicitly configured plugin root.
Each plugin directory contains `plugin.json`:

```json
{
  "protocolVersion": 1,
  "name": "gcode-preflight",
  "executable": "emberdeck-example-plugin"
}
```

The executable path must be relative, remain inside its plugin directory (also
after resolving symlinks), and name an executable file. Windows builds use the
`.exe` suffix in the manifest. Optional `args` is an array of arguments passed
directly to the process; no shell is involved. Names must be unique.

For each invocation, the host sends one JSON object to stdin and expects exactly
one JSON object on stdout. Stderr is diagnostic output. Both sides should keep
messages within 1 MiB. The v1 request is:

```json
{"protocolVersion":1,"gcode":"G28\nG1 X10 F300"}
```

The v1 response is:

```json
{"protocolVersion":1,"ok":true,"message":"G-code passed basic syntax preflight"}
```

The host enforces the earlier of the caller's deadline and a five second
default, bounds manifests and messages to 1 MiB and stderr to 4 KiB, validates
the response version, and reports a nonzero exit as an invocation error.
Plugins are trusted local executables and are not sandboxed. They run with the
same operating-system permissions as the EmberDeck server, so a plugin can read
or change anything that account can access. Install only code you trust. The
example G-code preflight checks basic text shape only; it does not certify
safety, machine compatibility, or successful execution. This protocol does not
claim OctoPrint compatibility.

## Build and install the example

`serve -plugins DIR` loads plugins from the configured root directory. Discovery
checks each immediate child directory for `plugin.json`; it does not recurse
through nested directories. Build the example from an EmberDeck source checkout
with Go 1.27, and put its executable and manifest together in a child directory
of the plugin root:

```sh
go build -o ./emberdeck ./cmd/emberdeck
mkdir -p ./plugins/gcode-preflight
go build -o ./plugins/gcode-preflight/emberdeck-example-plugin ./cmd/emberdeck-example-plugin
cat > ./plugins/gcode-preflight/plugin.json <<'JSON'
{
  "protocolVersion": 1,
  "name": "gcode-preflight",
  "executable": "emberdeck-example-plugin"
}
JSON
```

The executable must have execute permission on Unix-like systems. On Windows,
build `emberdeck-example-plugin.exe` and use that name for `executable` in the
manifest. Start the server with the plugin root:

```sh
export EMBERDECK_API_KEY="$(openssl rand -hex 32)"
printf 'API key (keep private): %s\n' "$EMBERDECK_API_KEY"
./emberdeck serve -plugins ./plugins -data ./data
```

The example is a preflight process, not a driver or file converter. It can reject
G-code before dispatch, but passing this basic syntax check is not a safety or
machine-compatibility approval. Because plugins are trusted processes rather
than sandboxed code, only build and install plugins from sources you have
reviewed.

The public Go API is `plugin.Discover(root)` and `Plugin.Run(ctx, Request)`.
Discovery fails if any manifest is malformed or invalid, rather than silently
ignoring that plugin.

Configured preflight invocations contribute bounded `ok`, `error`, and `rejected`
outcomes and duration histograms to the optional server metrics endpoint. No
plugin names, messages, G-code, or raw errors are exported. See
[metrics and monitoring](metrics.md) for the contract and deployment guidance.
