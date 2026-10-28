# HTTP API and worker contract

API prefix: `/api/v1`. Every API request requires `Authorization: Bearer KEY`
using `EMBERDECK_API_KEY`. The HTML, CSS, and JavaScript console shell are public;
job data is private. The browser keeps the key in session storage for the browser tab session.
Use a long random secret. Operators and trusted workers currently share the key;
do not give it to untrusted users or plugins. There is no multi-user RBAC yet.
Use HTTPS through a TLS reverse proxy for remote access. The native listener is
loopback-only by default and does not terminate TLS itself.

Requests and responses are JSON, except successful deletion (204). Errors return
`{"error":"description"}`. Unknown JSON fields and trailing objects are rejected.

## Job library

`POST /jobs` accepts `{"name":"square.gcode","gcode":"G21\nG90\nM5"}` and
returns 201, the job, and its Location. The full JSON upload is limited to 4 MiB;
names are 1–200 bytes. Control bytes other than tabs and line endings are rejected.
Uploading stores a program; it does not execute it or certify its machine safety.

`GET /jobs` returns jobs in creation order. `GET /jobs/{id}` returns the uploaded
program and current state. `DELETE /jobs/{id}` removes a queued or terminal job;
a running or canceling job must first stop. These operations persist to the
private data directory. Run one server per directory; file locking enforces it.

## Dispatch and monitoring

`GET /drivers` returns online workers, their last heartbeat time, and busy status.

`POST /jobs/{id}/start` takes `{"driver_id":"bench","confirmed":true}`. The
operator must supervise work and confirm physical readiness. A queued job can
start only on an online, idle worker. The server validates the complete program
and runs all configured preflight plugins before assignment. A plugin rejection,
error, or timeout prevents dispatch (422). No automatic queue advancement occurs.

`POST /jobs/{id}/pause` takes `{}`. An assigned worker must advertise
`pause_resume`; older workers return 409 instead of receiving an unsupported
control command. Running or resuming work becomes pausing. It becomes paused
only after the worker observes GRBL `Hold:0` or `Idle` following feed hold.
`Hold:1` means decelerating, and remains pausing. Repeating pause preserves a
confirmed hold.

`POST /jobs/{id}/resume` takes `{"confirmed":true}` and requires a confirmed
paused job. Readiness must be explicitly reconfirmed by the operator. The job
becomes resuming until the worker observes a fresh `Run` or `Idle` after the
resume command. There is no automatic resume when a held controller reports
ready. All pause states retain the same lease and keep the assigned driver busy.

`POST /jobs/{id}/cancel` takes `{}`. Queued work becomes canceled immediately;
running, pausing, paused, or resuming work becomes canceling until the worker reports its best-effort stop.
Network or software cancellation cannot guarantee that a laser has stopped.

Job fields include `state`, `driver_id`, `acked`, `total`, `controller_state`, and
`error`. Controller state is the last observed GRBL status, not a hardware
safety guarantee. Progress is
controller acknowledgment, not a percentage of physical work or a time estimate.
The worker reports completed only after all lines are accepted and GRBL is Idle.
A ten second heartbeat expiration or server restart marks active jobs—including paused work—interrupted.
Interrupted/failed/completed/canceled jobs are terminal; inspect the machine and
upload a new job if another run is appropriate. The system never automatically
replays a job after a restart.

## Driver polling protocol v1

`POST /drivers/{id}/poll` registers or reports one trusted worker. Driver IDs are
1–64 letters, numbers, underscores, or hyphens. Every poll includes a process
unique `session` string. Reusing a live ID with another session returns 409.

An idle poll is `{"session":"random-session-id"}`. The response is either
`{"command":"idle","protocol":"1"}` or an assignment with `command:"run"`,
`lease`, and `job` (including `id` and `gcode`). Treat a lease as single execution
authority. Never execute a completed lease again after lost responses.

Workers supporting confirmed control include `capabilities:["pause_resume"]`
in polls. While assigned, poll each second with `session`, `job_id`, `lease`,
`state`, `acked`, `total`, optional `error`, and the last `controller_state`.
Worker state may be running, pausing, paused, or resuming before a terminal
receipt. Pause/resume commands use the existing assignment shape, same job ID,
and lease; they never authorize reconnecting or replaying an earlier program.
A pause command may be the initial assignment, before any source bytes were
sent. Such work must remain gated until an explicit resume.

The server returns `command:"pause"` while pausing/paused and
`command:"resume"` while resuming. Paused workers continue heartbeats; an
intentional hold can last indefinitely while communications remain healthy.
A worker must confirm a fresh post-command status rather than reusing a
readiness or earlier poll response. Capability-advertising workers must include
`controller_state:"Idle"` in completed receipts. A `cancel` command requires immediate
best-effort stop. Terminal states are `completed`, `failed`, and `canceled`.
Completed requires all lines acknowledged and Idle verified by the worker.
If physical completion races a cancellation request, verified completion wins
and releases the worker instead of leaving the job stuck canceling. Identical terminal receipts are idempotent. Invalid leases,
regressing progress, and duplicate sessions are rejected. Lost communication
stops the worker; restart it only after inspecting the machine.

`GET /plugins` lists configured preflight manifests. Plugins are explicit trusted
executables loaded at startup. See the plugin protocol for installation and limits.
