# HTTP API and worker contract

API prefix: `/api/v1`. Every API request requires `Authorization: Bearer KEY`.
The HTML, CSS, and JavaScript console shell are public; API data is private. The
browser keeps the entered key in session storage for the browser tab session.
Use HTTPS through a TLS reverse proxy for remote access. The native listener is
loopback-only by default and does not terminate TLS itself.

Optional Prometheus metrics are served from `/metrics` on the separately
configured `serve -metrics-listen` listener, not on the authenticated API mux or
in the generated API route index. See [metrics and monitoring](metrics.md) for
the contract, access control, and scrape examples.

## Credential roles and lifecycle

Credentials are named, independently revocable bearer secrets. The server stores
only SHA-256 hashes of secrets in `credentials.json`, written atomically in the
data directory. Generated secrets contain 256 bits of randomness and are
compared in constant time. Treat the one-time secret response like a password;
EmberDeck never returns it again, and list/auth responses never include secrets
or hashes.

| Role | Read jobs, drivers, profiles, projects, plugins | Mutate jobs/profiles/projects or generate | Worker polling | Manage credentials |
| --- | --- | --- | --- | --- |
| `viewer` | Yes | No | No | No |
| `operator` | Yes | Yes | No | No |
| `admin` | Yes | Yes | Yes, all driver IDs | Yes |
| `worker` | No | No | Only its assigned driver IDs | No |

The `admin` role includes operator access. Admin polling is retained for the
existing `EMBERDECK_API_KEY` bootstrap and in-process worker compatibility. Use
dedicated worker credentials for distributed devices; they cannot call any
operator route. Active worker credential driver-ID scopes cannot overlap.

`GET /api/v1/auth` describes the caller's ID, name, role, and worker driver IDs.
For example, a worker's response contains only its assigned IDs:

```json
{
  "id": "0123456789abcdef0123456789abcdef",
  "name": "bench worker",
  "role": "worker",
  "driver_ids": ["bench"]
}
```

Admins manage credentials through these routes:

### `GET /credentials`

Returns credential metadata, including revoked entries for audit. It never
returns a key or hash.

### `POST /credentials`

Create a `viewer`, `operator`, `admin`, or `worker` credential. Worker
credentials must include one or more unique `driver_ids`; other roles must omit
them. An active worker credential already claiming a driver ID prevents another
worker credential from claiming it. The `201` response contains the generated
`key` exactly once, along with the metadata to record securely.

```sh
curl --fail-with-body -X POST https://emberdeck.example.net/api/v1/credentials \
  -H 'Authorization: Bearer ADMIN_KEY' -H 'Content-Type: application/json' \
  -d '{"name":"bench worker","role":"worker","driver_ids":["bench"]}'
```

Store the returned `key` in that device's secret manager or protected service
environment. Never put it in source control, a URL, shell history, support logs,
or an untrusted plugin. A `409` means a requested worker ID is already claimed.

### `DELETE /credentials/{id}`

Revokes one credential and returns `204`. The final active admin cannot be
revoked; create and verify a replacement admin first. Revoking a worker
credential deletes only its online driver sessions and marks its active jobs
`interrupted`, clears their leases, and does not replay them. The worker's next
poll/report is rejected; its active stream fails closed. Other worker sessions
continue.

```sh
curl --fail-with-body -X DELETE \
  https://emberdeck.example.net/api/v1/credentials/CREDENTIAL_ID \
  -H 'Authorization: Bearer ADMIN_KEY'
```

Credential routes return `401` for a missing, unknown, or revoked bearer key,
`403` when the authenticated role is not allowed, `400` for invalid role/name/
scope input, and `409` when worker IDs overlap or an administrator attempts to
revoke the last active admin. An unknown credential ID returns `404`.

### Bootstrap, migration, and rotation

On the first start in a data directory without `credentials.json`, the current
`EMBERDECK_API_KEY` is migrated to an admin credential named `bootstrap-admin`;
only its SHA-256 hash is persisted. Existing jobs, profiles, and projects are
left in place. This behavior makes an existing single-key installation continue
to authenticate after upgrade. Once `credentials.json` exists, the server uses
only credentials in that store: the environment variable is not an authentication
fallback and changing it does not rotate or replace a key. The CLI still requires
`EMBERDECK_API_KEY` at startup; for local-worker deployments it must name an
active admin key because the local worker shares that configured key.

For rotation, while authenticated as an admin, create a replacement credential
of the intended role and deliver its one-time key to its user/device. Verify
viewer/operator/admin credentials with `GET /api/v1/auth`; verify a worker by
starting it and checking its driver with an operator/admin `GET /api/v1/drivers`.
Then revoke the previous credential. Worker scopes cannot overlap, so rotate a
worker by stopping it, ensuring no job is active
(revocation interrupts any active lease), revoking the old credential, creating
a new worker credential for the same driver IDs, updating the device's protected
environment, and verifying its new session. If all admin credentials are lost,
stop the server, back up the data
directory, remove only `credentials.json`, and restart with a newly provisioned
`EMBERDECK_API_KEY`; this reseeds a bootstrap admin but also discards all named
credential records and their revocation state. Protect that recovery operation.

Keep viewers read-only, issue one worker credential per physical worker/device,
scope it to only its driver ID, and give operator/admin keys only to people who
need those capabilities. Do not share the bootstrap admin key with remote workers;
replace legacy workers with scoped worker keys before revoking the bootstrap key.

Requests are JSON except raw `image/svg+xml` on `POST /svg-import`; responses are
JSON except successful deletion (204). Errors return
`{"error":"description"}`. Unknown JSON fields and trailing objects are rejected.

## Job library

`POST /jobs` accepts `{"name":"square.gcode","gcode":"G21\nG90\nM5","profile_id":"optional-profile-id"}` and
returns 201, the job metadata, and its Location. G-code content is limited to 4 MiB;
the complete JSON upload is capped at 24 MiB plus 64 KiB to bound escaping and
framing overhead. Names are 1–200 bytes. Control bytes other than tabs and line
endings are rejected.
Driver line validation runs at upload; an invalid line returns 422 with its line
number. An optional `profile_id` binds a declared profile revision for the
program bounds review. Uploading stores a program; it does not execute it or
certify its machine safety.

`GET /jobs` returns one page of job metadata, newest first:

```json
{"jobs":[{"id":"…","name":"square.gcode","state":"queued","size":10,"acked":0,"total":0,"created":"…"}],
 "total":1,"offset":0,"limit":50}
```

Query parameters are optional and combine with AND:

| Parameter | Meaning |
| --- | --- |
| `limit` | Page size, 1–200 (default 50). |
| `offset` | Number of matching jobs to skip (default 0). |
| `state` | Exact job state; repeat or comma-separate for several, for example `state=failed,interrupted`. |
| `name` | Case-insensitive substring of the job name. |
| `driver` | Exact assigned `driver_id`. |

Jobs that have been dispatched also include `started`; jobs that reached
`completed`, `failed`, `canceled`, or `interrupted` include `ended`, and
`duration_ms` when they had started. Jobs stored before these fields existed
omit them.

`total` counts every job that matches the filters. Invalid values, including
unknown states, return 400. List entries never include program bytes (`gcode`)
or a worker `lease`; `size` is the stored program length in bytes. `GET
/jobs/{id}` returns the same metadata plus the exact uploaded or generated
program as `gcode`. Both include optional live `execution` telemetry and, for
uploaded jobs, an optional `program_review` summary. The review's
`constant_power_m3` is true when the program selects GRBL's `M3` constant laser
power mode, which keeps output on while motion stops. No operator endpoint
returns a lease. `GET /jobs/{id}/review` returns the selected profile's
revision, program bounds, profile envelope, bounds status (`in_bounds`,
`out_of_bounds`, or `unknown`), warnings, and derived path. `profile_stale` is
true when the declared profile is missing or its revision changed since the job
was uploaded or generated. In that case bounds status is `unknown`;
`profile_bounds`, when present, is the saved profile snapshot for context and
must not be shown as the current work area. The profile envelope is
operator-declared information, not sensed or verified machine travel. A partial
path can show known bounds while its status remains `unknown`. `GET
/jobs/{id}/toolpath` returns a derived 2D path for visualization; it does not
change or validate the uploaded source. `DELETE /jobs/{id}` removes a queued or
terminal job;
a running or canceling job must first stop. These operations persist to the
private data directory. Run one server per directory; file locking enforces it.

### Job event history

`GET /jobs/{id}/events` returns the job's retained history, oldest first:

```json
{"job_id":"…","events":[
 {"seq":1,"time":"…","job_id":"…","to":"queued","source":"operator","action":"upload"},
 {"seq":2,"time":"…","job_id":"…","from":"queued","to":"running","source":"operator","action":"start",
  "driver_id":"bench","session_id":"3f9c0a1b2c3d4e5f","lease_id":"8d7e6f5a4b3c2d1e"}]}
```

Every job state change produces exactly one event, recorded where the change
is persisted. `from` is omitted when a job is created and `to` is `deleted`
when it is removed. `source` is `operator` (API requests), `worker` (driver
reports), or `server` (recovery and expiry). `action` is one of `upload`,
`generate`, `start`, `pause`, `resume`, `cancel`, `delete`, `report`,
`restart`, `heartbeat_expired`, or `credential_revoked` (an admin revoked the
worker credential of an active job). `credential_id` and `credential_name`
identify the API credential whose request caused the change; server events
have none. Any role that can read jobs can read their history. `detail` carries the reported error for
`failed` and `interrupted` events. Progress-only reports and repeated requests
that do not change the state record nothing.

`session_id` and `lease_id` are opaque 16-hex-digit digests of the worker
session and job lease. They correlate events and logs but cannot be used as
credentials; neither the session nor the lease is ever returned. `seq` grows
across all jobs. History remains readable after a job is deleted until retention
removes it; an ID with no job and no retained events returns 404.

With `serve -log-level info` or lower, the server also writes one structured
log record (`msg="job state changed"`) per event. It has the same `seq`,
`source`, `action`, credential, driver, and session/lease reference fields, plus
the job name and progress. A job created by an upload logs `from=new`.

Retention is always bounded. `serve -job-event-limit` (default 50,000) caps the
number of events kept, removing the oldest first, and `serve
-job-event-retention` (default `2160h`, 90 days; `0` disables the age limit)
removes older events. Pruned events stop being served immediately. The log file
is compacted at startup and after pruned events exceed 10% of the limit.

### Job storage

The data directory holds job metadata in `jobs.json` (format version 3) and
each program in its own `programs/<id>.gcode` file. Metadata records each
program's size and SHA-256 digest. Startup fails closed if a listed program is
missing or has the wrong size. Reading a program whose bytes do not match the
digest returns 500. Program files no job names are removed at startup. Upload program reviews are stored with
the metadata; uploaded jobs without one (such as migrated jobs) are reviewed
once at startup from their program files.

State changes (upload, start, pause, resume, cancel, delete, and terminal
worker receipts) are written and synced before the request is acknowledged.
Progress-only worker reports (acknowledged lines, controller state, and
execution telemetry while the job state is unchanged) update memory only. A
graceful shutdown saves them. After a crash, restart marks active work
interrupted anyway, so the latest progress before the crash may be lost.

Job events are appended to `job-events.jsonl` and synced before `jobs.json` is
replaced, and `jobs.json` records the last committed event sequence, so its
atomic rename commits states and their events together. At startup, events
beyond that sequence come from a save interrupted by a crash; they are
discarded. A missing, malformed, or incomplete committed history makes the
server refuse to start without rewriting anything. If an index write fails, the
appended events are removed; if that is impossible, the server stops accepting
API requests. Version 2 libraries open with empty histories and are written as
version 3 on the next change; existing jobs get no reconstructed events.

Libraries written by earlier releases embed programs in `jobs.json`. On startup
the server validates every entry, writes and syncs each program file, then
replaces `jobs.json` with the version 3 metadata in one atomic rename. If any
entry is invalid or any write fails, the server refuses to start and leaves the
original `jobs.json` unchanged. An interrupted migration simply runs again.
Earlier releases cannot read a migrated library and refuse to start; releases
without job history likewise refuse version 3. Keep a backup of the data
directory before upgrading if you might roll back.

The derived path supports up to 10,000 linear XY segments and a 4 MiB source.
It interprets G0/G1, G20/G21, G90/G91, G17/G40/G49/G80/G94, and M3/M4/M5.
Arcs, machine-coordinate moves, work-coordinate-system changes (G54-G59),
coordinate-offset mutations, unmodeled M-codes, other axes, and unknown motion
modes are not approximated. The response sets
`supported:false` for unmodeled motion and includes warnings when a move is
omitted, such as an initial move whose start position is unknown. This parser
is a visualization aid; the normal GRBL validator and operator review remain
separate.

## Dispatch and monitoring

`GET /drivers` returns online workers, their last heartbeat time, and busy status.
It also returns `camera_available`, a boolean that does not reveal the source
URL or credentials. Workers that implement machine-preparation controls advertise
per-action capabilities: `manual_home`, `manual_unlock`, `manual_jog`,
`manual_set_zero`, and `manual_frame`. The console enables an action only when
its capability is present and the reported controller state permits it.

`POST /drivers/{id}/manual` queues one typed manual assignment. Every request
requires `confirmed:true`; jog additionally requires `axis` (`X`, `Y`, or `Z`),
`distance_mm` within ±25 mm, and `feed_mm_min` from 1 to 3000. Frame requires a
`job_id` whose geometry review is known and in bounds. Home, jog, and frame
require a reported `Idle` controller; unlock requires `Alarm`; setting X/Y zero
requires `Idle`. The response includes an opaque public assignment ID, action,
state, `laser_off:true`, and a maximum duration. The worker lease secret is never
returned. A manual assignment reserves the driver separately from a job lease;
job G-code is not included.

`GET /drivers/{id}/manual` returns the current manual assignment, or 404 when
none exists. `DELETE /drivers/{id}/manual` requires `{"confirmed":true}`. A
queued assignment is released immediately; a worker-owned assignment enters
`canceling` and must report completion before the lease clears. An interrupted
assignment requires a fresh Idle report before release. If GRBL's parser reset
cannot be confirmed after an ambiguous partial command write, a durable driver
quarantine blocks job starts, manual assignments, and lease release across
server restarts. The worker retries a soft reset and clears quarantine only
after the GRBL restart banner and a terminal Idle or Alarm status are confirmed.
Manual control never starts automatically after a worker reconnects.

An administrator can configure a driver's optional HTTP
camera source with `PUT /drivers/{id}/camera` and
`{"url":"http://camera.local/mjpeg","username":"viewer","password":"secret"}`.
Use `{}` to remove the source. The URL must not contain embedded user-info;
optional Basic Auth credentials are stored server-side and never returned by
the API. Basic Auth over HTTP sends credentials without encryption: use HTTPS
where the camera supports it, or restrict HTTP sources to a trusted, isolated
network. EmberDeck accepts an MJPEG multipart stream or a single JPEG response,
refuses redirects, and reads at most one 2 MiB frame within three seconds.

`GET /drivers/{id}/camera` returns the first JPEG frame from the configured
source. The endpoint requires an authenticated viewer, operator, or admin
credential, disables caching, and limits requests to one frame per second per
driver. A missing, slow, or invalid source returns `502 camera offline or
stale`. Camera delivery is independent of job execution; a camera failure does
not change job state. Cameras are for observation only and do not replace
supervision, enclosures, interlocks, or emergency stops.

When the worker has read them, `controller_settings` holds the controller's
`$20` (soft limits), `$30`/`$31` (spindle range), `$32` (laser mode), and
`$130`/`$131` (X/Y max travel) with `settings_read_at`; a failed read is
reported as `settings_error`. Values come from GRBL's read-only `$$` command.
EmberDeck never writes `$` settings.

`POST /jobs/{id}/start` takes `{"driver_id":"bench","confirmed":true,"bounds_override":true}`.
For uploaded jobs, `bounds_override:true` is required when the profile review is
unknown or out of bounds; review results do not replace the existing supervision
confirmation. Designer-generated jobs keep their profile-bound generation
validation and do not require this override. The operator must supervise work
and confirm physical readiness. A queued job can
start only on an online, idle worker. The server validates the complete program
and runs all configured preflight plugins before assignment. A plugin rejection,
error, or timeout prevents dispatch (422). No automatic queue advancement occurs.

A designer-generated job also requires the selected driver's last reported
controller settings to match its immutable profile snapshot: `$30` equals
`max_s`, `$31` equals `min_s` (0), and `$32` is 1 for laser mode. Otherwise
start returns 409 with each differing field, for example `$30 (maximum
spindle): controller 255, profile 1000`, and the job stays queued. A driver
that has not reported settings, or whose last read failed, also returns 409.
The worker repeats the comparison against the job's snapshot before the first
program byte and fails the job without streaming if the controller changed. The
profile does not declare soft limits or travel, so `$20`, `$130`, and `$131` are
reported but not compared. Uploaded jobs are not blocked by settings; the
console warns when the driver reports `$32=0` or the program uses `M3`.

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

Job fields include `state`, `driver_id`, `acked`, `total`, `controller_state`,
optional `execution`, and `error`. Acknowledged-line progress is kept separate
from path progress; an `ok` receipt never advances the completed-path count.
`execution` can contain raw machine/work positions, the last work offset,
position-report time, units, and `path_progress` with completed lower bound,
active segment, total, and confidence. Confidence is `reported` when an optional
GRBL `Ln` marker maps uniquely to an N-numbered segment, `estimated` when work
position matches one parsed segment, `ambiguous` for repeated/overlapping
matches, and `unknown` when correlation is unavailable. `controller_idle` means
the worker received fresh final Idle after the full stream and laser-off
cleanup; it is not proof of laser output or material processing.

Position-based correlation assumes GRBL reports in millimetres (`$13=0`), and
the UI displays that assumption. The worker does not read or change `$13` or
other controller settings. If only MPos is reported without a current/cached
WCO, the UI shows its raw controller-unit position and leaves work-path
correlation unknown. GRBL may emit WCO intermittently; the worker retains the
last reported offset. GRBL `Ln` is optional at compile time, and only applies
when the source contains matching N words. The line marker, when available,
describes the executing line; it is not inferred from acknowledged commands.
See the [GRBL 1.1 status report fields at commit `bfb67f0`](https://github.com/gnea/grbl/blob/bfb67f0c7963fe3ce4aaf8a97f9009ea5a8db36e/doc/markdown/interface.md#real-time-status-reports)
for its coordinate and line-number limits.

A controller `Idle` receipt drains the planned motion, but none of these values
prove the beam fired, the material was processed, or a physical machine is safe.
The worker reports completed only after all lines are accepted and GRBL is Idle.
A ten second heartbeat expiration or server restart marks active jobs—including paused work—interrupted.
Interrupted/failed/completed/canceled jobs are terminal; inspect the machine and
upload a new job if another run is appropriate. The system never automatically
replays a job after a restart.

## Driver polling and content transfer

`POST /drivers/{id}/poll` registers or reports one trusted worker. Driver IDs are
1–64 letters, numbers, underscores, or hyphens. Every poll includes a process
unique `session` string. Reusing a live ID with another session returns 409.

An idle poll is `{"session":"random-session-id"}`. The response is idle metadata
or an assignment with `command:"run"`, `protocol`, `lease`, and `job`. Workers
that do not advertise `chunked_content_v1` retain protocol 1: the initial
assignment includes `job.gcode`. This is the compatibility path for existing
workers. An idle response includes
`read_settings:true` while the server has no settings for the driver (at most
every 30 seconds); the worker then reads them with `$$` and reports them on the
next poll. Every poll may carry `controller_settings` (keys `$20`, `$30`, `$31`,
`$32`, `$130`, `$131`), `settings_read_at`, and `settings_error`. The latest
poll replaces the server's copy, so omitting settings clears them. Boolean
settings must be 0 or 1; invalid values return 400. A generated job's
assignment includes `expected_settings` with its profile's `$30`, `$31`, and
`$32`; the worker must refuse the job before streaming when they differ. Treat
a lease as single execution authority. Never execute a completed lease again
after lost responses. A poll that reports progress for its assigned job
receives assignment metadata without program bytes. The program is sent only
when a poll does not already report that job, such as at pickup.

Workers advertising `capabilities:["chunked_content_v1"]` receive protocol 2
assignments with an immutable manifest instead of `job.gcode`:

```json
{"command":"run","protocol":2,"lease":"…","job":{"id":"…","content":{"size":11,"sha256":"…"}}}
```

Fetch the content with `GET /drivers/{id}/jobs/{job_id}/content`, an explicit
single `Range: bytes=start-end`, and `X-EmberDeck-Session` plus
`X-EmberDeck-Lease` headers. Each inclusive range is at most 64 KiB. A successful
response is `206 application/octet-stream` with `Content-Range` and
`X-EmberDeck-Content-SHA256`. Ranges are idempotent and may be retried. The
endpoint is restricted to the current worker credential, session, driver, active
job, and lease; canceled or terminal leases cannot fetch more bytes. The server
accepts no program over 4 MiB.

Before opening the controller connection, the worker must receive every byte,
check each response range, verify the exact size and SHA-256 against the
assignment manifest, then validate all G-code. A truncated, altered, oversized,
or malformed program is failed without sending any program bytes. Workers keep
the complete verified program in memory in this Go implementation; a constrained
firmware implementation may stage the same bounded content in PSRAM or flash.
For a failure before streaming, the worker reports `acked:0` and the assignment's
`total` line count so the server can accept and persist the failed receipt without
claiming controller progress.
Protocol 1 assignments are capped at 8 MiB to keep existing Go workers within
their response-reader limit; starting a larger encoded assignment with a legacy
worker returns 413 and leaves the job queued. New workers also accept a
protocol-1 full-program assignment from an older server, preserving mixed-version
deployments.

Workers supporting confirmed control include `pause_resume` in polls. Chunked
workers also include `chunked_content_v1`. While assigned, poll each second with
`session`, `job_id`, `lease`,
`state`, `acked`, `total`, optional `error`, and the last `controller_state`.
Worker state may be running, pausing, paused, or resuming before a terminal
receipt. Pause/resume commands use the existing assignment shape, same job ID,
and lease; they never authorize reconnecting or replaying an earlier program.
A pause command may be the initial assignment, before any source bytes were
sent. Such work must remain gated until an explicit resume.

For an active lease, `acked` progress must be monotonic and within `0..total`;
a decreasing or out-of-range report returns HTTP 400 without changing the
stored progress. A terminal receipt is committed with its state, counts,
controller state, error, and execution telemetry. Retrying the identical receipt
is idempotent (HTTP 200); changing any of those fields conflicts with the
committed result (HTTP 409).

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
durable `profiles.json` library. EmberDeck never configures a controller from
these values; it reads `$30`, `$31`, and `$32` and refuses to start a generated
job when they differ (see [Dispatch and monitoring](#dispatch-and-monitoring)). The initial designer profile supports only
`controller:"grbl-1.1"`, `units:"mm"`, `origin:"lower-left"`, and
`laser_mode_enabled:true`. The declared work origin is the currently prepared
GRBL work origin; EmberDeck does not home, zero, change coordinate systems, or
write controller settings. Operators must set GRBL `$30` to `max_s`, `$31` to
0, and `$32` to 1 themselves. A profile must
explicitly declare `min_s:0`; other values are unsupported because linear power
percentage conversion would not match a nonzero GRBL minimum PWM. See GRBL's
[settings reference at commit `bfb67f0`](https://github.com/gnea/grbl/blob/bfb67f0c7963fe3ce4aaf8a97f9009ea5a8db36e/doc/markdown/settings.md)
and [laser mode reference at the same commit](https://github.com/gnea/grbl/blob/bfb67f0c7963fe3ce4aaf8a97f9009ea5a8db36e/doc/markdown/laser_mode.md).

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

## Static SVG import

`POST /svg-import` accepts raw `image/svg+xml` bytes up to 1 MiB and returns
normalized editable geometry plus `provenance` identifying the built-in importer
and its version. It does not save the source or project, persist provenance, or
assign machine operations. Import the returned paths into a version 2 project
with the regular project endpoints. The root SVG must declare physical `width`, `height`, and
`viewBox`; supported units are mm, cm, inches, points, picas, and CSS pixels.
The viewport is mapped to lower-left-origin millimetres. A mismatched aspect ratio
requires explicit `preserveAspectRatio="none"`.

The static subset supports paths made from M/L/H/V/Z line commands, C/c and
S/s cubic Bezier commands, and Q/q and T/t quadratic Bezier commands, plus
lines, polylines, polygons, rectangles, circles, ellipses, and affine element
transforms. Nested `<g>` groups compose their transforms with descendant groups
and elements. Groups accept only `id` and `transform` attributes and may nest up
to 16 levels. The root may be unqualified or use the official SVG namespace;
foreign element namespaces and namespaced attributes are rejected.

Static `<text>` elements are converted to open editable strokes using the fixed
`emberdeck-stick-v1` font. `font-family` must equal that font ID, and `font-size`
must be a unitless number from 1 to 100 SVG user units. Optional `x` and `y` are
finite unitless numbers defaulting to zero. Text accepts 1–32 ASCII characters:
A–Z, a–z, digits, space, and `. , : ; ! ? + - / _ ( )`; lowercase uses
uppercase glyph strokes. Only `x`, `y`, `font-family`, `font-size`, `id`, and
`transform` are accepted on `<text>`. Style and other unsupported attributes,
other fonts, and nested elements such as `<tspan>` are rejected.

Curves are flattened deterministically to editable line segments with a maximum
0.05 mm chord deviation in final imported millimetre coordinates. Flattening
rejects curves that cannot meet that tolerance within 16 subdivision levels or
the 4,000-point import limit. Circles and ellipses are represented by 32-point
editable polylines. Elliptical arcs (A/a), CSS/style, animation, DTD/entities,
external references, embedded raster images, and active content are rejected.
Fill, stroke, colors, and SVG metadata never become laser operations. Imported
geometry, including text strokes, starts unassigned; assign each object to an
explicit operation layer before generating a job. Raster engraving is not
implemented.

## Importer interface (Go API)

`importer` defines the version 1 Go extension point for converting source files
into editable design data or candidate G-code jobs. It is distinct from process
plugins, which run preflight checks, and driver adapters, which communicate
with controllers.

Implement `importer.Importer` with `Manifest()` and
`Import(context.Context, Request)`. The manifest declares a stable importer ID
and version, protocol version, source formats (IDs, extensions, and media
types), output types, and an optional JSON options schema. Construct an
`importer.Registry` from trusted implementations; `List` returns defensive
copies of the capabilities, and `Import` selects an importer by ID and output
type. Version 1 defines `editable_design` and `gcode_job` outputs.

The registry limits source bytes to 1 MiB, options to 1 MiB, and JSON result
payloads to 4 MiB. It validates formats and output types, and attaches importer
ID/version, selected source format, and options to result provenance. Importers
validate options against their schema, return actionable errors, and honor
context cancellation. Warnings can mark a transformation as lossy.

The registry is an in-process Go extension point. Implementations are trusted
code running with the server's permissions; they are not sandboxed. The registry
does not fetch external resources or extract archives. Importers must bound
resource use and reject active or external content unless a future contract
supports it. A `gcode_job` result is only a candidate: the host must apply the
normal program validation, profile review, preview, and operator confirmation
before saving or dispatching it. Importing never starts a machine.

The first adapter is `svgimport.Importer`, advertising `.svg`/
`image/svg+xml` input and `editable_design` output. It has no conversion options.
The existing `POST /svg-import` endpoint invokes it and returns normalized
unassigned geometry with importer ID/version provenance. The designer already
previews and saves this geometry to a project; desktop and mobile screenshots
are in the [operator guide](operator-guide.md#create-a-multi-path-vector-project). The server
currently registers only this built-in importer. Runtime loading or selection
of third-party importers, source retention, and persistence of provenance with
projects remain open work tracked by
[#16](https://github.com/constructorfleet/emberdeck/issues/16).

## Implementing a third-party importer

## Implement the interface

An importer declares a stable ID, its version, the exact source-format IDs it
accepts, its output types, and a JSON options schema. The registry validates
metadata and request bounds. The importer remains responsible for validating
options against its schema and for bounding CPU, memory, and any temporary
storage it uses.

```go
package sample

import (
    "context"
    "encoding/json"
    "errors"

    "github.com/ConstructorFleet/emberdeck/importer"
)

type Importer struct{}

func (Importer) Manifest() importer.Manifest {
    return importer.Manifest{
        ProtocolVersion: importer.ProtocolVersion,
        ID:              "example.vector",
        Name:            "Example vector",
        Version:         "1.0.0",
        SourceFormats: []importer.SourceFormat{{
            ID:         "example-vector",
            Name:       "Example Vector",
            Extensions: []string{".evf"},
            MediaTypes: []string{"application/vnd.example.vector"},
        }},
        OutputTypes: []importer.OutputType{importer.OutputEditableDesign},
        OptionsSchema: json.RawMessage(`{"type":"object","properties":{"scale":{"type":"number","minimum":0.01,"maximum":100}},"additionalProperties":false}`),
    }
}

func (Importer) Import(ctx context.Context, request importer.Request) (importer.Result, error) {
    if err := ctx.Err(); err != nil {
        return importer.Result{}, err
    }
    if request.SourceFormat != "example-vector" || request.OutputType != importer.OutputEditableDesign {
        return importer.Result{}, errors.New("example importer requires example-vector editable-design input")
    }
    // Validate request.Options against Manifest().OptionsSchema, then parse
    // request.Source and marshal the agreed editable-design representation.
    payload, err := json.Marshal(map[string]any{"objects": []any{}})
    if err != nil {
        return importer.Result{}, err
    }
    return importer.Result{
        OutputType: importer.OutputEditableDesign,
        Payload:    payload,
        Warnings: []importer.Warning{{
            Code: "metadata-dropped", Message: "The source title was not retained.", Lossy: true,
        }},
    }, nil
}
```

The example's empty `objects` payload only illustrates the interface shape; it
is not a supported design document. Agree on a payload representation with the
consumer before registering a real importer. `editable_design` and `gcode_job`
are output labels, not schemas. Today, `svgimport.Document` is the only editable
design payload consumed by the EmberDeck server. The generic `gcode_job` output
has no registered consumer yet and cannot be saved or dispatched through the
current importer endpoint.

## Register and call it

A Go host creates a registry from implementations it trusts. The registry
rejects invalid manifests and duplicate IDs. Capability lists are sorted by ID
and returned as defensive copies.

```go
registry, err := importer.NewRegistry(sample.Importer{})
if err != nil {
    return err
}

result, err := registry.Import(ctx, "example.vector", importer.Request{
    SourceFormat: "example-vector",
    OutputType:   importer.OutputEditableDesign,
    Source:       sourceBytes,
    Options:      json.RawMessage(`{"scale":1.5}`),
})
if err != nil {
    return err
}
// result.Provenance identifies the importer, version, format, and options.
// Review result.Warnings before accepting the conversion.
```

The registry accepts non-empty source data up to 1 MiB, JSON options up to 1 MiB,
and a JSON result payload up to 4 MiB. It allows at most 64 warnings; each needs
a non-empty code and message within the documented bounds. It checks that the
selected format and output were declared and that the result uses the requested
output type. It does not enforce an import timeout or terminate code that ignores
context cancellation. Importers must check `ctx` during long work and reject
malformed input with an actionable error.

The registry requires an options schema to be a JSON object, but does not
interpret JSON Schema keywords or validate the supplied options against that
schema. Importer implementations must perform that validation. Do not silently
choose scale, units, origin, speed, power, or controller settings.

## Compatibility and versioning

`ProtocolVersion` must equal the host's supported `importer.ProtocolVersion`
(currently `1`); there is no protocol negotiation or fallback. Increment the
protocol version when changing the shared Go contract incompatibly. `Manifest.Version`
is recorded verbatim in result provenance but is not parsed or compared by the
registry; use semantic versions as a publisher convention and document the compatibility
policy for their releases. Treat importer IDs and source-format IDs
as stable identifiers. A breaking input or output change should use a new
importer or format ID so callers can select it deliberately. The registry does
not infer a format from a filename or MIME type: callers select the declared
format ID explicitly.

The registry fills provenance after a successful conversion with the importer
ID/version, selected format, and exact options bytes. It does not retain original
source files or persist provenance with projects. Preserve source and conversion
metadata in the host application when reproducibility is required.

## Safety and output handling

Treat source files as untrusted. Reject malformed data, external references,
active content, and archive traversal unless the importer has a reviewed,
explicit policy for them. Do not fetch network resources. Bound decompressed
size, nesting, work, and temporary files in the importer; the generic registry
only bounds byte payloads and warning counts.

A conversion warning can set `Lossy: true` when information was dropped or
approximated. Return actionable errors for unsupported features rather than
silently producing misleading geometry. Geometry import must not assign
machine operations. A generated G-code payload is only a candidate: the host
must validate it, review it against a machine profile, show a preview, and ask
for explicit operator confirmation before saving or dispatching. Importing
never starts a machine.

See the [HTTP API and worker contract](api.md#importer-interface-go-api) for the
built-in SVG subset and current server behavior. The implemented SVG upload,
preview, and save flow is illustrated in the [operator guide](operator-guide.md#create-a-multi-path-vector-project).

## Editable design projects

Projects are saved independently in the durable `designs.json` library. Schema
version 1 remains supported for the original single rectangle/vector-engraving
editor. Schema version 2 stores multiple ordered line paths and operation
layers. Both use millimetres from the lower-left declared work origin. The
profile reference includes its revision. `profile_stale` is computed on reads
and is true after the profile revision changes; save the project with the
current reviewed profile revision before generation. Project creation and
updates require the referenced profile to exist at the submitted revision.
Missing or changed profiles return 409. Profile deletion returns 409 while a
project references it.

`GET /projects` returns projects in ID order. `POST /projects` creates an
editable project and returns 201 with a Location header. `GET /projects/{id}`
reads one project. `PUT /projects/{id}` requires its current positive `revision`
and increments it on success; stale writes return 409. `DELETE /projects/{id}`
requires `{"revision":N}` and returns 204. Missing projects return 404.

Version 1 request shape is `{"schema_version":1,"name":"Panel outline",
"profile_id":"PROFILE_ID","profile_revision":1,"rectangle":{"x_mm":10,
"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave",
"feed_mm_min":600,"power_percent":25}}`. Updates also include `revision`.
All requests are authenticated strict JSON objects limited to 64 KiB, with no
unknown fields. Names are trimmed and 1–120 bytes. Coordinates are nonnegative,
rectangle dimensions are positive, and the full rectangle must fit the profile
envelope. Coordinates, dimensions, feed, and power accept at most three decimal
places. Feed must fit the profile range; power must be above zero and at most
the profile maximum, and must round to at least one GRBL S unit. Other operation
types are not supported by version 1.

Version 2 geometry is an ordered list of objects, each with an ID, a `kind`
(`rectangle`, `ellipse`, `line`, or generic `path`), and one or more subpaths.
A subpath is a list of `{x_mm,y_mm}` points plus a `closed` boolean. An optional
`transform` stores `translate_x_mm`, `translate_y_mm`, `scale_x`, `scale_y`, and
`rotation_deg`. Scale and rotation are applied around the source geometry's
bounding-box center, then the millimetre translation is applied. Omitted
transforms use identity values. Scales range from 0.01 to 100, rotation from
−360 to 360 degrees, and transform values use at most three decimal places.
Transformed points are rounded to 0.001 mm and must fit the profile workspace.
SVG element transforms are applied at import time, while styling and machine
operation settings are not inferred.
Operations are ordered independently from the objects. Each object names its
operation with `operation_id`; imported or newly drawn unassigned paths may be
saved while they are reviewed, but generation requires every path to be
assigned. An engraving path can be open or closed. A cutting path must be closed
and enclose nonzero area.

```json
{"schema_version":2,"name":"Mixed vector design","units":"mm","profile_id":"PROFILE_ID","profile_revision":1,"objects":[{"id":"outline","operation_id":"cut","subpaths":[{"points":[{"x_mm":10,"y_mm":10},{"x_mm":40,"y_mm":10},{"x_mm":40,"y_mm":30},{"x_mm":10,"y_mm":30}],"closed":true}]},{"id":"mark","operation_id":"engrave","subpaths":[{"points":[{"x_mm":15,"y_mm":15},{"x_mm":35,"y_mm":25}],"closed":false}]}],"operations":[{"id":"cut","type":"vector-cut","feed_mm_min":300,"power_percent":80},{"id":"engrave","type":"vector-engrave","feed_mm_min":600,"power_percent":25}]}
```

The `operations` array defines execution order; paths within an operation keep
their object and subpath order. IDs are 1–64 ASCII letters, digits, `_`, or `-`.
Version 2 accepts at most 500 objects, 100 operations, and 4,000 points across
all subpaths. Source points must be finite, within 0–10,000 mm, and use at most
three decimal places; transformed points must also fit the declared workspace. Operation feed/power
limits are checked against the profile. All requests remain strict JSON with a
64 KiB body limit.

`POST /projects/{id}/generate` accepts `{"revision":N}` and returns 201 with a
Location pointing to a normal queued job. It requires the current project
revision and the exact current profile revision. The full G-code is checked by
the existing GRBL driver validator before persistence. The generated program is
an immutable job named `<project name>.gcode`; it contains deterministic
rectangle vector engraving moves for version 1, or ordered vector cut/engrave
path moves for version 2, and is never dispatched automatically. Version 2
generation emits laser-off rapid positioning and GRBL dynamic laser mode
(`M4`) for both operation types so output is disabled when motion is stopped or
held. Cutting paths are closed contours; no kerf compensation, tabs, pass
count, or raster engraving is implemented.
Generated jobs include `provenance` with `generator_version`, and complete
`project` and `profile` value snapshots including IDs and revisions. Later
project edits, project deletion, and profile deletion do not alter those job
bytes or snapshots. Uploaded jobs have no provenance and retain their existing
upload behavior.
