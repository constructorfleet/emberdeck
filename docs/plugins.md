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
claim OctoPrint compatibility. Controller transport extensions use a separate
contract; see [driver adapters](#driver-adapters). The current adapter API is
trusted in-process Go code with worker registration, while command-line
registration and dynamic loading remain unavailable.

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

## Driver adapters

Preflight plugins only allow or veto a dispatch. Controller-specific network
protocols need a separate lifecycle and capability contract; they must not be
added to the preflight request. The `driveradapter` Go package defines the
current version 1 in-process API. An embedding Go application can register a
trusted adapter with `worker.AdapterRegistry` and run a worker with
`worker.RunWithAdapterRegistry`. The existing HTTP driver poll still owns
assignment and lease state; the registry only maps that server driver ID to a
discovered adapter device. The command line server and `emberdeck driver` do
not configure adapter registrations, and there is no dynamic loader or
operator configuration screen.

An adapter implements `driveradapter.Adapter` and declares protocol version,
name, version, transport, and its maximum capabilities. `Discover` returns
concrete devices; each device may advertise a subset of those capabilities.
`Configure` receives a device ID and at most 1 MiB of JSON, then returns one
controller session:

Capability metadata uses stable snake-case names: `PauseResume` maps to
`pause_resume`, `Cancel` to `cancel`, and `PositionFeedback` to
`position_feedback`. Manual action fields map to `manual_home`,
`manual_unlock`, `manual_jog`, `manual_set_zero`, and `manual_frame`. A device
may advertise only fields declared by its manifest. These manual fields are
metadata only: the worker does not project them into its HTTP poll capabilities
or execute manual assignments. Issue #52's worker integration must use these
exact names with a distinct manual lease/session, `laser_off=true`, and bounded
durations.

```text
Discover -> Configure(device, config) -> Start(assignment) -> Status* -> Close
                                                   |-> Pause / Resume (capability-gated)
                                                   |-> Cancel (capability-gated)
```

A `Start` assignment includes one job ID, lease ID, exact G-code bytes, size, and
SHA-256. The shared wrapper rejects unsupported versions, invalid identifiers,
size mismatch, digest mismatch, and programs larger than 4 MiB before calling
adapter code. Invalid input does not consume the session; any start attempt does,
even when it returns an error, because the controller may have accepted work
before the transport failed. The wrapper permits at most one start per session.
The server and worker must still enforce lease ownership and must not create a
replacement session to replay work after disconnect or reboot.

The current worker adapter path handles job assignments, pause/resume, and
cancel, with position feedback where declared. It does not execute manual
preparation actions. The separate #52 capability names `manual_home`,
`manual_unlock`, `manual_jog`, `manual_set_zero`, and `manual_frame` require a
distinct manual lease/session and `laser_off=true`; that execution path is not
implemented here.

`Status` separates accepted lines from optional executed lines and may provide
controller state and position with `confirmed`, `estimated`, `unknown`, or
`unavailable` confidence. Acceptance is not execution evidence. Accepted and
executed counters cannot regress during a lease. Controls return an unsupported
error unless the device declares that capability. Adapters must honor context
cancellation, return bounded errors, and identify uncertainty after connection
loss. This API does not establish physical safety or replace machine interlocks.

The API is trusted in-process Go code, not an isolated plugin process. The
software-only simulator is registered explicitly by the embedding application;
it completes on its first status read with deterministic accepted and executed
counts. It is example/test behavior, not physical-machine evidence. The
simulator example and public contract tests live in
[`driveradapter/simulator`](../driveradapter/simulator/simulator.go) and
[`driveradapter/contract_test.go`](../driveradapter/contract_test.go):

```sh
go test ./driveradapter/...
```

A later non-built-in network target is the Ruida RDC6445G Ethernet interface;
[Ruida lists Ethernet and USB for that model](https://www.rdacs.com/en/product-detail/rdc6445g.html).
EmberDeck has not implemented or qualified its wire protocol. Protocol details,
authentication, transfer and cancellation semantics, firmware variants, and
licensing need verification first. Built-in GRBL-over-TCP remains limited to
compatible GRBL endpoints; Wi-Fi does not imply protocol compatibility.

The worker adapter path has end-to-end coverage for HTTP assignment, lease-bound
completion, Start/Status failures, and no replay after failure. Broader reconnect
and device lifecycle behavior, command-line registration, operator discovery
and configuration UI, and hardware qualification remain open work for issue #15.
