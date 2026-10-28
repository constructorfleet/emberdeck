# Worker control behavior

`worker.Run` creates a random session and polls the driver's endpoint with the
`pause_resume` and `chunked_content_v1` capabilities on every request. It keeps
one controller connection for the assigned lease and sends progress, controller
state, and command responses through the same poll endpoint.

Embedding Go applications can instead build a `worker.AdapterRegistry`, call
`Register(driverID, adapter, deviceID, config)`, and pass it to
`worker.RunWithAdapterRegistry`. This routes the same HTTP-assigned job and
server-owned lease through the registered in-process adapter. Registration is
explicit Go wiring; it does not load code dynamically or add a command-line
configuration screen. Adapters without settings-probe support report that
limitation to the server, and control commands must be declared by the device's
capabilities or the worker fails the lease closed.

## Verified program transfer

The server advertises immutable content size and SHA-256 in protocol 2
assignments. The Go worker fetches the assignment in sequential byte ranges of
at most 64 KiB, using the same credential, process session, and job lease. Each
range can be safely retried; the worker checks its `Content-Range` and digest
header against the assignment before accepting the bytes. The full 4 MiB maximum
program is assembled in bounded memory, checked for exact size and SHA-256, and
passed through full G-code validation before the worker opens the controller.
Any short, altered, oversized, or invalid program is reported failed without
writing a program byte to the controller. Program content is capped at 4 MiB on
the server and worker.

Workers without `chunked_content_v1` continue to receive the protocol 1 full
`gcode` assignment. A new worker also accepts that full-program form from an
older server. Firmware with less RAM should stage the same bounded chunks in
PSRAM or flash and complete digest plus G-code verification before UART output.

Provision a credential with role `worker` and a `driver_ids` entry matching the
worker's `-id`, then provide its one-time key as `EMBERDECK_API_KEY`. Worker
credentials cannot read job libraries or call operator routes. The server
rejects a credential used for an unassigned ID. Use one credential per device;
do not copy the server's admin/bootstrap key to a remote worker. Admin access to
poll remains only as compatibility for the legacy bootstrap key and local worker.

The worker applies `pause` and `resume` commands to the active GRBL stream. A
paused assignment is connected and preflighted without sending source lines;
the stream remains gated until resume is confirmed by a fresh controller
status. During a pause, the worker continues reporting once per second so the
server lease remains live. The `controller_state` field reports the most
recent GRBL state (`Idle`, `Run`, `Hold:1`, or `Hold:0`). The worker records the driver's applied-control receipt before accepting a
new controller status as confirmation. The job state becomes
`paused` only after the worker observes `Hold:0` or `Idle` after the pause
command, and becomes `running` after a fresh `Run` or `Idle` following resume.

Cancel stops the same stream and reports a canceled receipt. A worker does not
reconnect or replay an interrupted lease.

## Controller settings

Every job's readiness phase reads the controller's settings with GRBL's
read-only `$$` command after the initial `Idle` status and before the first
program line (see [driver controller settings](driver.md#readiness-and-controller-settings)).
When the assignment carries `expected_settings` (a designer-generated job's
profile snapshot), the worker compares `$30`, `$31`, and `$32` and fails the job
with each differing field before streaming. Uploaded jobs have no expectation;
their settings are only reported.

When an idle poll response includes `read_settings:true`, the worker opens the
controller, sends only `$$`, and closes it again within 10 seconds. It never
writes settings or program bytes during this probe. Every later poll carries the
last values as `controller_settings` with `settings_read_at`, so the server can
refuse a mismatched generated job at **Start** with 409. A failed probe clears
those values and reports `settings_error`. If the probe sees a GRBL startup
banner, the controller reset and work coordinates may be lost: the worker fails
its next assigned job without connecting, then reads normally again.

## Reconnection

An idle worker (one without an active lease) survives server restarts, network
drops, reverse-proxy errors, and session conflicts. When an idle poll fails, it
logs the attempt number, the error, and the next delay, then retries with a new
session. Delays start at 1 second, double after each failure up to 30 seconds,
and are jittered between half and all of that value. A successful poll logs the
reconnection and resets the delay. The worker also logs its first successful
connection, each job it starts, and each job's terminal state and error.
`worker.RunWithOptions` accepts `worker.Options` to change the delays and the log
destination; the `emberdeck driver` command sends these lines through its
structured logger (see [Deployment](deployment.md#logs)).

A new session can receive HTTP 409 `driver ID already owned by another
session` while the server still holds the previous process's session. That
ownership lasts until the old session's 10-second heartbeat expires. The worker
keeps retrying, so the conflict resolves without operator action. If two live
workers share a driver ID, one of them keeps retrying and logging conflicts.
Give each worker a unique `-id`.

HTTP 400, 401, and 403 responses mean bad configuration, such as a wrong or
revoked key, a driver ID outside the credential's scope, or a server too old to
accept this worker's metadata. The worker exits on these responses instead of
retrying.

A worker with an active lease still fails closed. A failed progress or terminal
report cancels the stream and closes the controller connection. Revoking its
credential also removes its server session and immediately marks its active job
`interrupted`, clears the lease, and prevents replay; its next progress request
is rejected. Other devices and their leases are unaffected. On ordinary network
failure, when the old session's heartbeat expires, the server marks the job
`interrupted`. A new worker process never replays the job. Inspect the machine
before starting new work after such a failure.

`emberdeck driver` exits after a fail-closed failure. Run it under a service
manager that restarts it (see [Deployment](deployment.md#driver-as-a-service)).
In all-in-one mode (`serve -simulate`, `-tcp`, or `-serial`), the server
supervises its local worker with `worker.Supervise`. A worker failure is logged
with the real transport, for example `serial driver /dev/ttyUSB0: ...`. The
local driver shows as offline until a new session registers, and the HTTP
server keeps running.

Workers do not expose a metrics listener. When enabled, EmberDeck's central
server exports its observed worker heartbeat and job state at its separate
Prometheus listener; these are server-side observations, not physical machine
telemetry. See [metrics and monitoring](metrics.md).
