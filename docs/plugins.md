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
Plugins are trusted local executables and are not
sandboxed. Install only plugins you trust. The example G-code preflight checks
basic text shape only; it does not certify safety, machine compatibility, or
successful execution. This protocol does not claim OctoPrint compatibility.

The public Go API is `plugin.Discover(root)` and `Plugin.Run(ctx, Request)`.
Discovery fails if any manifest is malformed or invalid, rather than silently
ignoring that plugin.

Configured preflight invocations contribute bounded `ok`, `error`, and `rejected`
outcomes and duration histograms to the optional server metrics endpoint. No
plugin names, messages, G-code, or raw errors are exported. See
[metrics and monitoring](metrics.md) for the contract and deployment guidance.
