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

## Declared machine profiles

Profiles are operator declarations stored separately from `jobs.json` in the
durable `profiles.json` library. EmberDeck does not measure or configure a
controller from these values. The initial designer profile supports only
`controller:"grbl-1.1"`, `units:"mm"`, `origin:"lower-left"`, and
`laser_mode_enabled:true`. The declared work origin is the currently prepared
GRBL work origin; EmberDeck does not home, zero, change coordinate systems, or
write controller settings. Operators must match `max_s` with GRBL `$30`,
`min_s:0` with `$31`, and laser mode with `$32=1` themselves. A profile must
explicitly declare `min_s:0`; other values are unsupported because linear power
percentage conversion would not match a nonzero GRBL minimum PWM. See GRBL's
[settings reference](https://github.com/gnea/grbl/blob/master/doc/markdown/settings.md)
and [laser mode reference](https://github.com/gnea/grbl/blob/master/doc/markdown/laser_mode.md).

`GET /profiles` lists profiles in ID order. `POST /profiles` creates one and
returns 201 with a Location header; omit `id` and `revision`. `GET /profiles/{id}`
reads a profile. `PUT /profiles/{id}` replaces its declaration and requires the
current positive `revision`; successful updates increment the revision, while
stale updates return 409. `DELETE /profiles/{id}` requires `{"revision":N}` and
returns 204. Missing profiles return 404. A profile referenced by a saved
project cannot be deleted.

All profile requests are authenticated, strict single JSON objects limited to
64 KiB, and reject unknown fields. Names are trimmed and must contain 1–120
bytes. Width and height must be positive and at most 10,000 mm. Feed limits
must satisfy `0 < min_feed_mm_min <= max_feed_mm_min <= 100000`. Dimension and
feed and percentage values support at most three decimal places. `max_s` is an
integer from 1 through 1,000,000,000; `max_power_percent` is greater than zero and at most
100. `min_s` must be explicitly present and equal zero. These are declared
limits, not hardware qualification or safety claims.

Example profile creation body (updates also include the expected `revision`):

```json
{"schema_version":1,"name":"Declared GRBL","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"min_s":0,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}
```

## Editable rectangle projects

Projects are saved independently in the durable `designs.json` library. This
initial project schema supports one rectangle and one `vector-engrave`
operation. Geometry uses millimetres from the lower-left declared work origin.
The profile reference includes its revision. `profile_stale` is computed on
reads and is true after the profile revision changes; save the project with the
current reviewed profile revision before generation. Project creation and
updates require the referenced profile to exist at the submitted revision,
reject out-of-envelope rectangles, and validate feed and power against that
profile. Missing or changed profiles return 409. Profile deletion returns 409
while a project references it.

`GET /projects` returns projects in ID order. `POST /projects` creates an
editable project and returns 201 with a Location header. `GET /projects/{id}`
reads one project. `PUT /projects/{id}` requires its current positive `revision`
and increments it on success; stale writes return 409. `DELETE /projects/{id}`
requires `{"revision":N}` and returns 204. Missing projects return 404.

The request shape is `{"schema_version":1,"name":"Panel outline",
"profile_id":"PROFILE_ID","profile_revision":1,"rectangle":{"x_mm":10,
"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave",
"feed_mm_min":600,"power_percent":25}}`. Updates also include `revision`.
All requests are authenticated strict JSON objects limited to 64 KiB, with no
unknown fields. Names are trimmed and 1–120 bytes. Coordinates are nonnegative,
rectangle dimensions are positive, and the full rectangle must fit the profile
envelope. Coordinates, dimensions, feed, and power accept at most three decimal
places. Feed must fit the profile range; power must be above zero and at most
the profile maximum, and must round to at least one GRBL S unit. Other operation
types and SVG, text, raster, and cut workflows are not supported by this API.

`POST /projects/{id}/generate` accepts `{"revision":N}` and returns 201 with a
Location pointing to a normal queued job. It requires the current project
revision and the exact current profile revision. The full G-code is checked by
the existing GRBL driver validator before persistence. The generated program is
an immutable job named `<project name>.gcode`; it contains deterministic
rectangle vector engraving moves, and is never dispatched automatically.
Generated jobs include `provenance` with `generator_version`, and complete
`project` and `profile` value snapshots including IDs and revisions. Later
project edits, project deletion, and profile deletion do not alter those job
bytes or snapshots. Uploaded jobs have no provenance and retain their existing
upload behavior.
