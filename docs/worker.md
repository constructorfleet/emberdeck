# Worker control behavior

`worker.Run` creates a random session and polls the driver's endpoint with the
`pause_resume` capability on every request. It keeps one controller connection
for the assigned lease and sends progress, controller state, and command
responses through the same poll endpoint.

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

## Reconnection

An idle worker (one without an active lease) survives server restarts, network
drops, reverse-proxy errors, and session conflicts. When an idle poll fails, it
logs the attempt number, the error, and the next delay, then retries with a new
session. Delays start at 1 second, double after each failure up to 30 seconds,
and are jittered between half and all of that value. A successful poll logs the
reconnection and resets the delay. `worker.RunWithOptions` accepts
`worker.Options` to change the delays and the log destination.

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
