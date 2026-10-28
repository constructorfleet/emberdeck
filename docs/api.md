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
returns 201, the job metadata, and its Location. The full JSON upload is limited to 4 MiB;
names are 1–200 bytes. Control bytes other than tabs and line endings are rejected.
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

`total` counts every job that matches the filters. Invalid values, including
unknown states, return 400. List entries never include program bytes (`gcode`)
or a worker `lease`; `size` is the stored program length in bytes. `GET
/jobs/{id}` returns the same metadata plus the exact uploaded or generated
program as `gcode`. Both include optional live `execution` telemetry and, for
uploaded jobs, an optional `program_review` summary. No operator endpoint
returns a lease. `GET /jobs/{id}/review` returns the selected profile's
revision, program bounds, profile envelope, bounds status (`in_bounds`,
`out_of_bounds`, or `unknown`), warnings, and derived path. A partial path can
show known bounds while its status remains `unknown`. A changed/deleted profile
also makes the status unknown. `GET /jobs/{id}/toolpath` returns a derived 2D
path for visualization; it does not change or validate the uploaded source. `DELETE /jobs/{id}` removes a queued or terminal job;
a running or canceling job must first stop. These operations persist to the
private data directory. Run one server per directory; file locking enforces it.

### Job storage

The data directory holds job metadata in `jobs.json` (format version 2) and
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

Libraries written by earlier releases embed programs in `jobs.json`. On startup
the server validates every entry, writes and syncs each program file, then
replaces `jobs.json` with the version 2 metadata in one atomic rename. If any
entry is invalid or any write fails, the server refuses to start and leaves the
original `jobs.json` unchanged. An interrupted migration simply runs again.
Earlier releases cannot read a migrated library and refuse to start, so keep a
backup of the data directory before upgrading if you might roll back.

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

`POST /jobs/{id}/start` takes `{"driver_id":"bench","confirmed":true,"bounds_override":true}`.
For uploaded jobs, `bounds_override:true` is required when the profile review is
unknown or out of bounds; review results do not replace the existing supervision
confirmation. Designer-generated jobs keep their profile-bound generation
validation and do not require this override. The operator must supervise work
and confirm physical readiness. A queued job can
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
See the [GRBL status report fields](https://github.com/gnea/grbl/blob/master/doc/markdown/interface.md#real-time-status-reports)
for its coordinate and line-number limits.

A controller `Idle` receipt drains the planned motion, but none of these values
prove the beam fired, the material was processed, or a physical machine is safe.
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
A poll that reports progress for its assigned job receives the same assignment
shape without `gcode`. The program is sent only when a poll does not already
report that job, such as at pickup.

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

## Static SVG import

`POST /svg-import` accepts raw `image/svg+xml` bytes up to 1 MiB and returns
normalized editable geometry; it does not save a project or assign machine
operations. Import the returned paths into a version 2 project with the regular
project endpoints. The root SVG must declare physical `width`, `height`, and
`viewBox`; supported units are mm, cm, inches, points, picas, and CSS pixels.
The viewport is mapped to lower-left-origin millimetres. A mismatched aspect ratio
requires explicit `preserveAspectRatio="none"`.

The static subset supports direct-child paths made from M/L/H/V/Z line commands,
lines, polylines, polygons, rectangles, circles, ellipses, and element-level
affine transforms. Circles and ellipses are represented by 32-point editable
polylines. Group nesting, CSS/style, text, curves, animation, DTD/entities,
external references, and active content are rejected. Fill, stroke, colors, and
SVG metadata never become laser operations. Imported geometry starts unassigned;
assign each object to an explicit operation layer before generating a job.

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
A subpath is a list of `{x_mm,y_mm}` points plus a `closed` boolean. Paths are
already in absolute work coordinates; SVG element transforms are applied at
import time, while styling and machine operation settings are not inferred. Operations are ordered independently
from the objects. Each object names its operation with `operation_id`; saving
an unassigned path is allowed so future importers can preserve editable source,
but generation requires every path to be assigned. An engraving path can be
open or closed. A cutting path must be closed and enclose nonzero area.

```json
{"schema_version":2,"name":"Mixed vector design","units":"mm","profile_id":"PROFILE_ID","profile_revision":1,"objects":[{"id":"outline","operation_id":"cut","subpaths":[{"points":[{"x_mm":10,"y_mm":10},{"x_mm":40,"y_mm":10},{"x_mm":40,"y_mm":30},{"x_mm":10,"y_mm":30}],"closed":true}]},{"id":"mark","operation_id":"engrave","subpaths":[{"points":[{"x_mm":15,"y_mm":15},{"x_mm":35,"y_mm":25}],"closed":false}]}],"operations":[{"id":"cut","type":"vector-cut","feed_mm_min":300,"power_percent":80},{"id":"engrave","type":"vector-engrave","feed_mm_min":600,"power_percent":25}]}
```

The `operations` array defines execution order; paths within an operation keep
their object and subpath order. IDs are 1–64 ASCII letters, digits, `_`, or `-`.
Version 2 accepts at most 500 objects, 100 operations, and 4,000 points across
all subpaths. Points must be finite, nonnegative, within 10,000 mm, use at most
three decimal places, and fit the declared workspace. Operation feed/power
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
