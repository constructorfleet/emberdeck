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

Requests are JSON except raw `image/svg+xml` on `POST /api/v1/svg-import`,
`GET /api/v1/drivers/{id}/camera` JPEG responses, and range responses on
`GET /api/v1/drivers/{id}/jobs/{job_id}/content`. Successful deletions return
`204`; other responses are JSON. Errors return `{"error":"description"}`.
Unknown JSON fields and trailing objects are rejected.

A complete authenticated request:

```sh
curl --fail-with-body https://emberdeck.example.net/api/v1/jobs \
  -H 'Authorization: Bearer OPERATOR_KEY'
```

## Authentication and roles

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

### `GET /api/v1/auth`

Describe the caller so a client can check its role and worker scope before using
other routes.

| Request | Response | Errors |
| --- | --- | --- |
| No body. | `200` with the caller's `id`, `name`, `role`, and worker `driver_ids`. | `401` missing or invalid bearer key. |

A worker's response contains only its assigned IDs:

```json
{
  "id": "0123456789abcdef0123456789abcdef",
  "name": "bench worker",
  "role": "worker",
  "driver_ids": ["bench"]
}
```

### `GET /api/v1/credentials`

List credential metadata, including revoked entries for audit. Secrets and
hashes are never returned.

| Request | Response | Errors |
| --- | --- | --- |
| No body. | `200` array of credential metadata sorted by ID. | `401` missing or invalid key; `403` caller is not an admin. |

### `POST /api/v1/credentials`

Create a `viewer`, `operator`, `admin`, or `worker` credential. Worker
credentials must include one or more unique `driver_ids`; other roles must omit
them. An active worker credential already claiming a driver ID prevents another
worker credential from claiming it. The `201` response contains the generated
`key` exactly once, along with the metadata to record securely.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `name`, `role`, and optional `driver_ids` for workers. | `201` with metadata and the one-time `key`. | `400` invalid name, role, or scope; `401` missing or invalid key; `403` caller is not an admin; `409` worker ID already claimed. |

```sh
curl --fail-with-body -X POST https://emberdeck.example.net/api/v1/credentials \
  -H 'Authorization: Bearer ADMIN_KEY' -H 'Content-Type: application/json' \
  -d '{"name":"bench worker","role":"worker","driver_ids":["bench"]}'
```

Store the returned `key` in that device's secret manager or protected service
environment. Never put it in source control, a URL, shell history, support logs,
or an untrusted plugin.

### `DELETE /api/v1/credentials/{id}`

Revoke one credential and erase its active sessions. The final active admin
cannot be revoked; create and verify a replacement admin first. Revoking a
worker credential deletes only its online driver sessions and marks its active
jobs `interrupted`, clears their leases, and does not replay them. The worker's
next poll/report is rejected; its active stream fails closed. Other worker
sessions continue.

| Request | Response | Errors |
| --- | --- | --- |
| No body; credential ID in the path. | `204`. | `401` missing or invalid key; `403` caller is not an admin; `404` unknown or already revoked credential; `409` attempting to revoke the last active admin. |

```sh
curl --fail-with-body -X DELETE \
  https://emberdeck.example.net/api/v1/credentials/CREDENTIAL_ID \
  -H 'Authorization: Bearer ADMIN_KEY'
```

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

## Jobs

### `POST /api/v1/jobs`

Upload a program to the job library. Uploading stores a program; it does not
execute it or certify its machine safety. Driver line validation runs at upload;
an invalid line returns 422 with its line number. An optional `profile_id` binds
a declared profile revision for the program bounds review.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `name`, `gcode`, and optional `profile_id`. | `201` with job metadata and a `Location` header. | `400` malformed JSON, control bytes, or unknown profile; `401`/`403` authentication or role; `413` program over 4 MiB; `422` invalid G-code line. |

G-code content is limited to 4 MiB; the complete JSON upload is capped at 24 MiB
plus 64 KiB to bound escaping and framing overhead. Names are 1–200 bytes.
Control bytes other than tabs and line endings are rejected.

### `GET /api/v1/jobs`

Return one page of job metadata, newest first.

| Request | Response | Errors |
| --- | --- | --- |
| No body. Optional query: `limit`, `offset`, `state`, `name`, `driver`. | `200` with `jobs`, `total`, `offset`, and `limit`. | `400` invalid filter or unknown state; `401`/`403` authentication or role; `500` storage failure. |

Query parameters combine with AND:

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

`total` counts every job that matches the filters. List entries never include
program bytes (`gcode`) or a worker `lease`; `size` is the stored program length
in bytes.

```json
{
  "jobs": [
    {
      "id": "0123456789abcdef0123456789abcdef",
      "name": "square.gcode",
      "state": "queued",
      "size": 10,
      "acked": 0,
      "total": 0,
      "created": "2026-10-27T12:00:00Z"
    }
  ],
  "total": 1,
  "offset": 0,
  "limit": 50
}
```

### `GET /api/v1/jobs/{id}`

Return one job's metadata plus the exact uploaded or generated program as
`gcode`. List entries omit the program; this route includes it. The response
also includes optional live `execution` telemetry and, for uploaded jobs, an
optional `program_review` summary. The review's `constant_power_m3` is true when
the program selects GRBL's `M3` constant laser power mode, which keeps output on
while motion stops. No operator endpoint returns a lease.

| Request | Response | Errors |
| --- | --- | --- |
| No body; job ID in the path. | `200` with job metadata and `gcode`. | `401`/`403` authentication or role; `404` unknown job; `500` unreadable stored program. |

### `GET /api/v1/jobs/{id}/review`

Return the selected profile's revision, program bounds, profile envelope, bounds
status (`in_bounds`, `out_of_bounds`, or `unknown`), warnings, and derived path.
`profile_stale` is true when the declared profile is missing or its revision
changed since the job was uploaded or generated. In that case bounds status is
`unknown`; `profile_bounds`, when present, is the saved profile snapshot for
context and must not be shown as the current work area. The profile envelope is
operator-declared information, not sensed or verified machine travel. A partial
path can show known bounds while its status remains `unknown`.

| Request | Response | Errors |
| --- | --- | --- |
| No body; job ID in the path. | `200` with the current review or generated-job snapshot. | `401`/`403` authentication or role; `404` unknown job. |

### `GET /api/v1/jobs/{id}/toolpath`

Return a derived 2D path for visualization. It does not change or validate the
uploaded source.

| Request | Response | Errors |
| --- | --- | --- |
| No body; job ID in the path. | `200` with parsed segments, bounds, warnings, and support status. | `401`/`403` authentication or role; `404` unknown job; `500` unreadable stored program. |

### `GET /api/v1/jobs/{id}/events`

Return one job's retained history, oldest first.

| Request | Response | Errors |
| --- | --- | --- |
| No body; job ID in the path. | `200` with `job_id` and `events`. | `401`/`403` authentication or role; `404` unknown job with no retained events; `500` storage failure. |

```json
{
  "job_id": "0123456789abcdef0123456789abcdef",
  "events": [
    {
      "seq": 1,
      "time": "2026-10-27T12:00:00Z",
      "job_id": "0123456789abcdef0123456789abcdef",
      "to": "queued",
      "source": "operator",
      "action": "upload"
    },
    {
      "seq": 2,
      "time": "2026-10-27T12:01:00Z",
      "job_id": "0123456789abcdef0123456789abcdef",
      "from": "queued",
      "to": "running",
      "source": "operator",
      "action": "start",
      "driver_id": "bench",
      "session_id": "3f9c0a1b2c3d4e5f",
      "lease_id": "8d7e6f5a4b3c2d1e"
    }
  ]
}
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

### `DELETE /api/v1/jobs/{id}`

Remove a queued or terminal job. A running or canceling job must first stop.

| Request | Response | Errors |
| --- | --- | --- |
| No body; job ID in the path. | `204`. | `401`/`403` authentication or role; `404` unknown job; `409` active job; `500` could not persist deletion. |

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

## Dispatch, control, and monitoring

### `GET /api/v1/drivers`

Return online workers, their last heartbeat time, busy status, and optional
controller settings. It also returns `camera_available`, a boolean that does not
reveal the source URL or credentials. Workers that implement machine-preparation
controls advertise per-action capabilities: `manual_home`, `manual_unlock`,
`manual_jog`, `manual_set_zero`, and `manual_frame`. The console enables an
action only when its capability is present and the reported controller state
permits it. When the worker has read them, `controller_settings` holds
the controller's `$20` (soft limits), `$30`/`$31` (spindle range), `$32` (laser
mode), and `$130`/`$131` (X/Y max travel) with `settings_read_at`; a failed read
is reported as `settings_error`. Values come from GRBL's read-only `$$` command.
EmberDeck never writes `$` settings.

| Request | Response | Errors |
| --- | --- | --- |
| No body. | `200` array of driver records, including `camera_available` when configured. | `401`/`403` authentication or role; `500` could not persist driver expiration. |

### `POST /api/v1/jobs/{id}/start`

Assign one queued job to an online, idle worker. The server validates the
complete program and runs all configured preflight plugins before assignment. A
plugin rejection, error, or timeout prevents dispatch (422). No automatic queue
advancement occurs.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `driver_id`, `confirmed`, and optional `bounds_override`. | `200` with the running job. | `400` missing confirmation; `401`/`403`; `404` unknown job; `409` not queued, offline/busy driver, bounds override required, settings mismatch, or active manual lease; `413` legacy assignment too large; `422` validation or plugin rejection; `500` persistence failure. |

`confirmed` must be true: the operator must supervise work and confirm physical
readiness. For uploaded jobs, `bounds_override:true` is required when the
profile review is unknown or out of bounds; review results do not replace the
existing supervision confirmation. Designer-generated jobs keep their
profile-bound generation validation and do not require this override.

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

### `POST /api/v1/jobs/{id}/pause`

Request a confirmed pause. An assigned worker must advertise `pause_resume`;
older workers return 409 instead of receiving an unsupported control command.
Running or resuming work becomes pausing. It becomes paused only after the
worker observes GRBL `Hold:0` or `Idle` following feed hold. `Hold:1` means
decelerating, and remains pausing. Repeating pause preserves a confirmed hold.

| Request | Response | Errors |
| --- | --- | --- |
| Empty JSON object `{}`. | `200` with the job in `pausing` or `paused`. | `401`/`403`; `404` unknown job; `409` job not running/pausable or worker lacks `pause_resume`; `500` persistence failure. |

### `POST /api/v1/jobs/{id}/resume`

Resume a confirmed paused job. Readiness must be explicitly reconfirmed by the
operator. The job becomes resuming until the worker observes a fresh `Run` or
`Idle` after the resume command. There is no automatic resume when a held
controller reports ready. All pause states retain the same lease and keep the
assigned driver busy.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `confirmed:true`. | `200` with the job in `resuming`. | `400` missing confirmation; `401`/`403`; `404` unknown job; `409` job not paused; `500` persistence failure. |

### `POST /api/v1/jobs/{id}/cancel`

Cancel a queued or active job. Queued work becomes canceled immediately;
running, pausing, paused, or resuming work becomes canceling until the worker
reports its best-effort stop. Network or software cancellation cannot guarantee
that a laser has stopped.

| Request | Response | Errors |
| --- | --- | --- |
| Empty JSON object `{}`. | `200` with the canceled or canceling job. | `401`/`403`; `404` unknown job; `409` job already terminal; `500` persistence failure. |

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

## Camera monitoring

### `PUT /api/v1/drivers/{id}/camera`

Configure or clear one driver's camera source. The upstream URL and credentials
stay on the server; the browser receives JPEG frames only through the
authenticated frame route. The URL must not contain embedded user-info; optional
Basic Auth credentials are stored server-side and never returned by the API.
Basic Auth over HTTP sends credentials without encryption: use HTTPS where the
camera supports it, or restrict HTTP sources to a trusted, isolated network.
EmberDeck accepts an MJPEG multipart stream or a single JPEG response, refuses
redirects, and reads at most one 2 MiB frame within three seconds. Camera failure
never affects job execution. A camera is not a safety device.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with optional `url`, `username`, and `password`; an empty `url` clears the source. | `204`. | `400` invalid driver ID, non-HTTP(S) URL, embedded URL credentials, or credentials without a URL; `401`/`403`; `500` could not persist; `503` storage unavailable. |

### `GET /api/v1/drivers/{id}/camera`

Fetch one JPEG frame from the configured source. The endpoint requires an
authenticated viewer, operator, or admin credential, disables caching, and
limits requests to one frame per second per driver. A missing, slow, or invalid
source returns `502 camera offline or stale`. Camera delivery is independent of
job execution; a camera failure does not change job state. Cameras are for
observation only and do not replace supervision, enclosures, interlocks, or
emergency stops.

| Request | Response | Errors |
| --- | --- | --- |
| No body; driver ID in the path. | `200 image/jpeg` with one frame. | `400` invalid driver ID; `401`/`403`; `404` no camera configured; `429` frame rate limited with `Retry-After: 1`; `502` camera offline or stale. |

## Manual machine preparation

Manual actions use a short-lived, driver-scoped lease. They require operator
confirmation, advertise explicit worker capabilities, and never run while a job
is active. Motion requires an `Idle` controller; `unlock` requires `Alarm`.
Laser output is off for every manual action.

### `POST /api/v1/drivers/{id}/manual`

Queue one typed manual assignment for `jog`, `home`, `unlock`, `set-zero`, or
`frame`. Every request requires `confirmed:true`; jog additionally requires
`axis` (`X`, `Y`, or `Z`), `distance_mm` within ±25 mm, and `feed_mm_min` from 1
to 3000. Frame requires a `job_id` whose geometry review is known and in bounds.
Home, jog, and frame require a reported `Idle` controller; unlock requires
`Alarm`; setting X/Y zero requires `Idle`. The response includes an opaque
public assignment ID, action, state, `laser_off:true`, and a maximum duration.
The worker lease secret is never returned. A manual assignment reserves the
driver separately from a job lease; job G-code is not included.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `action`, `confirmed:true`, and action-specific `axis`, `distance_mm`, `feed_mm_min`, or `job_id`. | `201` with the lease, bounds for framing, and maximum duration. | `400` unsupported action, invalid jog, missing fields, or missing confirmation; `401`/`403`; `409` driver offline/busy, missing capability, wrong controller state, active job, or unconfirmed parser reset; `500` persistence failure. |

### `GET /api/v1/drivers/{id}/manual`

Read the current manual-control lease and its state.

| Request | Response | Errors |
| --- | --- | --- |
| No body; driver ID in the path. | `200` with the lease state and worker-reported progress. | `401`/`403`; `404` no manual assignment. |

### `DELETE /api/v1/drivers/{id}/manual`

Release or cancel a manual-control lease. A queued assignment is released
immediately; a worker-owned assignment enters `canceling` and must report
completion before the lease clears. An interrupted assignment requires a fresh
Idle report before release. If GRBL's parser reset cannot be confirmed after an
ambiguous partial command write, a durable driver quarantine blocks job starts,
manual assignments, and lease release across server restarts. The worker retries
a soft reset and clears quarantine only after the GRBL restart banner and a
terminal Idle or Alarm status are confirmed. Manual control never starts
automatically after a worker reconnects.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `confirmed:true`. | `200` released, or `202` canceling. | `400` missing confirmation; `401`/`403`; `404` no assignment; `409` driver offline, unconfirmed parser reset, or missing fresh Idle status; `500` persistence failure. |

## Worker polling and content transfer

### `POST /api/v1/drivers/{id}/poll`

Register or report one trusted worker. Driver IDs are 1–64 letters, numbers,
underscores, or hyphens. Every poll includes a process unique `session` string.
Reusing a live ID with another session returns 409.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `session` and optional `job_id`, `lease`, `state`, `acked`, `total`, `error`, `controller_state`, controller settings, execution telemetry, or manual receipt. | `200` with idle metadata or an assignment. | `400` invalid driver ID, session, settings, or progress; `401`/`403` wrong credential scope; `409` duplicate session or invalid lease; `500` storage failure. |

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
{
  "command": "run",
  "protocol": 2,
  "lease": "lease-value",
  "job": {
    "id": "0123456789abcdef0123456789abcdef",
    "content": {
      "size": 11,
      "sha256": "0000000000000000000000000000000000000000000000000000000000000000"
    }
  }
}
```

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

### `GET /api/v1/drivers/{id}/jobs/{job_id}/content`

Fetch part of the immutable manifest assigned to a chunked worker. Fetch the
content with an explicit single `Range: bytes=start-end` and
`X-EmberDeck-Session` plus `X-EmberDeck-Lease` headers. Each inclusive range is
at most 64 KiB. A successful response is `206 application/octet-stream` with
`Content-Range` and `X-EmberDeck-Content-SHA256`. Ranges are idempotent and may
be retried. The endpoint is restricted to the current worker credential,
session, driver, active job, and lease; canceled or terminal leases cannot fetch
more bytes. The server accepts no program over 4 MiB.

| Request | Response | Errors |
| --- | --- | --- |
| No body. `Range`, `X-EmberDeck-Session`, and `X-EmberDeck-Lease` headers are required. | `206` with the requested bytes and integrity headers. | `400` invalid IDs or missing headers; `401`/`403` wrong credential; `404` missing program; `409` stale session or lease; `416` invalid or oversized range, with `Content-Range: bytes */SIZE`; `500` unreadable program. |

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

## Declared machine profiles

Profiles are operator declarations stored separately from `jobs.json` in the
durable `profiles.json` library. EmberDeck never configures a controller from
these values; it reads `$30`, `$31`, and `$32` and refuses to start a generated
job when they differ (see [Dispatch, control, and monitoring](#dispatch-control-and-monitoring)). The initial designer profile supports only
`controller:"grbl-1.1"`, `units:"mm"`, `origin:"lower-left"`, and
`laser_mode_enabled:true`. The declared work origin is the currently prepared
GRBL work origin; EmberDeck does not home, zero, change coordinate systems, or
write controller settings. Operators must set GRBL `$30` to `max_s`, `$31` to
0, and `$32` to 1 themselves. A profile must
explicitly declare `min_s:0`; other values are unsupported because linear power
percentage conversion would not match a nonzero GRBL minimum PWM. See GRBL's
[settings reference at commit `bfb67f0`](https://github.com/gnea/grbl/blob/bfb67f0c7963fe3ce4aaf8a97f9009ea5a8db36e/doc/markdown/settings.md)
and [laser mode reference at the same commit](https://github.com/gnea/grbl/blob/bfb67f0c7963fe3ce4aaf8a97f9009ea5a8db36e/doc/markdown/laser_mode.md).

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
{
  "schema_version": 1,
  "name": "Declared GRBL",
  "controller": "grbl-1.1",
  "units": "mm",
  "origin": "lower-left",
  "width_mm": 300,
  "height_mm": 200,
  "min_s": 0,
  "max_s": 1000,
  "min_feed_mm_min": 1,
  "max_feed_mm_min": 6000,
  "max_power_percent": 100,
  "laser_mode_enabled": true
}
```

### `GET /api/v1/profiles`

List declared profiles in ID order.

| Request | Response | Errors |
| --- | --- | --- |
| No body. | `200` array of profiles. | `401`/`403` authentication or role. |

### `POST /api/v1/profiles`

Create a declared profile.

| Request | Response | Errors |
| --- | --- | --- |
| JSON profile object without `id` or `revision`. | `201` with the stored profile and a `Location` header. | `400` invalid or unknown fields; `401`/`403`; `500` persistence failure. |

### `GET /api/v1/profiles/{id}`

Read one declared profile.

| Request | Response | Errors |
| --- | --- | --- |
| No body; profile ID in the path. | `200` with the profile. | `401`/`403`; `404` unknown profile. |

### `PUT /api/v1/profiles/{id}`

Replace a profile declaration. The request must include the current positive
`revision`; successful updates increment it.

| Request | Response | Errors |
| --- | --- | --- |
| JSON profile object including `revision`. | `200` with the updated profile. | `400` invalid fields; `401`/`403`; `404` unknown profile; `409` stale revision; `500` persistence failure. |

### `DELETE /api/v1/profiles/{id}`

Delete a declared profile. A profile referenced by a saved project cannot be
deleted.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `revision`. | `204`. | `400` invalid revision; `401`/`403`; `404` unknown profile; `409` stale revision or project reference; `500` persistence failure. |

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

`GET /api/v1/projects/{id}` reads one project. Each save advances the positive
integer `revision` and returns `updated_at` (RFC 3339 UTC) and `updated_by` (the
authenticated credential name, when present). `PUT /api/v1/projects/{id}`
requires the revision the editor read; stale writes return 409 with both `error`
and the current saved project, including its revision and save metadata. Clients
must keep local edits when this happens and offer reload or save-as-copy
recovery. This revision check also protects the race where another save lands
after a client's last refresh. Projects written before save metadata was
introduced may omit `updated_at` and `updated_by` until the next save.

```json
{
  "error": "project revision conflict",
  "current": {
    "schema_version": 1,
    "id": "PROJECT_ID",
    "revision": 3,
    "updated_at": "2026-10-28T18:42:00Z",
    "updated_by": "shop operator",
    "name": "Panel outline",
    "profile_id": "PROFILE_ID",
    "profile_revision": 1,
    "rectangle": {
      "x_mm": 10,
      "y_mm": 20,
      "width_mm": 30,
      "height_mm": 20
    },
    "operation": {
      "type": "vector-engrave",
      "feed_mm_min": 600,
      "power_percent": 25
    },
    "profile_stale": false
  }
}
```

Version 1 request shape is a single rectangle and operation. Updates also
include `revision`. All requests are authenticated strict JSON objects limited
to 64 KiB, with no unknown fields. Names are trimmed and 1–120 bytes.
Coordinates are nonnegative, rectangle dimensions are positive, and the full
rectangle must fit the profile envelope. Coordinates, dimensions, feed, and
power accept at most three decimal places. Feed must fit the profile range;
power must be above zero and at most the profile maximum, and must round to at
least one GRBL S unit. Other operation types are not supported by version 1.

```json
{
  "schema_version": 1,
  "name": "Panel outline",
  "profile_id": "PROFILE_ID",
  "profile_revision": 1,
  "rectangle": {
    "x_mm": 10,
    "y_mm": 20,
    "width_mm": 30,
    "height_mm": 20
  },
  "operation": {
    "type": "vector-engrave",
    "feed_mm_min": 600,
    "power_percent": 25
  }
}
```

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
{
  "schema_version": 2,
  "name": "Mixed vector design",
  "units": "mm",
  "profile_id": "PROFILE_ID",
  "profile_revision": 1,
  "objects": [
    {
      "id": "outline",
      "operation_id": "cut",
      "subpaths": [
        {
          "points": [
            {
              "x_mm": 10,
              "y_mm": 10
            },
            {
              "x_mm": 40,
              "y_mm": 10
            },
            {
              "x_mm": 40,
              "y_mm": 30
            },
            {
              "x_mm": 10,
              "y_mm": 30
            }
          ],
          "closed": true
        }
      ]
    },
    {
      "id": "mark",
      "operation_id": "engrave",
      "subpaths": [
        {
          "points": [
            {
              "x_mm": 15,
              "y_mm": 15
            },
            {
              "x_mm": 35,
              "y_mm": 25
            }
          ],
          "closed": false
        }
      ]
    }
  ],
  "operations": [
    {
      "id": "cut",
      "type": "vector-cut",
      "feed_mm_min": 300,
      "power_percent": 80
    },
    {
      "id": "engrave",
      "type": "vector-engrave",
      "feed_mm_min": 600,
      "power_percent": 25
    }
  ]
}
```

The `operations` array defines execution order; paths within an operation keep
their object and subpath order. IDs are 1–64 ASCII letters, digits, `_`, or `-`.
Version 2 accepts at most 500 objects, 100 operations, and 4,000 points across
all subpaths. Points must be finite, nonnegative, within 10,000 mm, use at most
three decimal places, and fit the declared workspace. Operation feed/power
limits are checked against the profile. All requests remain strict JSON with a
64 KiB body limit.

### `GET /api/v1/projects`

List saved projects in ID order.

| Request | Response | Errors |
| --- | --- | --- |
| No body. | `200` array of projects with computed `profile_stale`. | `401`/`403` authentication or role. |

### `POST /api/v1/projects`

Create an editable design project at schema version 1 or 2.

| Request | Response | Errors |
| --- | --- | --- |
| JSON project object without `id` or `revision`. | `201` with the stored project and a `Location` header. | `400` invalid geometry or fields; `401`/`403`; `409` missing or changed profile revision; `500` persistence failure. |

### `GET /api/v1/projects/{id}`

Read one saved project.

| Request | Response | Errors |
| --- | --- | --- |
| No body; project ID in the path. | `200` with the project and computed `profile_stale`. | `401`/`403`; `404` unknown project. |

### `PUT /api/v1/projects/{id}`

Replace a saved project. The request must include its current positive
`revision`; stale writes return 409.

| Request | Response | Errors |
| --- | --- | --- |
| JSON project object including `revision`. | `200` with the updated project and save metadata. | `400` invalid geometry or fields; `401`/`403`; `404` unknown project; `409` stale revision or profile revision, with the current saved project; `500` persistence failure. |

### `DELETE /api/v1/projects/{id}`

Delete a saved project.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `revision`. | `204`. | `400` invalid revision; `401`/`403`; `404` unknown project; `409` stale revision; `500` persistence failure. |

### `POST /api/v1/projects/{id}/generate`

Generate an immutable queued job from a saved project. It requires the current
project revision and the exact current profile revision. The full G-code is
checked by the existing GRBL driver validator before persistence. The generated
program is an immutable job named `<project name>.gcode`; it contains
deterministic rectangle vector engraving moves for version 1, or ordered vector
cut/engrave path moves for version 2, and is never dispatched automatically.
Version 2 generation emits laser-off rapid positioning and GRBL dynamic laser
mode (`M4`) for both operation types so output is disabled when motion is
stopped or held. Cutting paths are closed contours; no kerf compensation, tabs,
pass count, or raster engraving is implemented. Generated jobs include
`provenance` with `generator_version`, and complete `project` and `profile`
value snapshots including IDs and revisions. Later project edits, project
deletion, and profile deletion do not alter those job bytes or snapshots.
Uploaded jobs have no provenance and retain their existing upload behavior.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `revision`. | `201` with the queued job and a `Location` header. | `400` invalid revision; `401`/`403`; `404` unknown project; `409` stale project/profile revision or unassigned paths; `422` generated program rejected by validation; `500` persistence failure. |

### `POST /api/v1/designer/text-geometry`

Convert bounded monoline text into editable path geometry. The response is not
a saved project or job; it never selects machine power, speed, origin, or units
other than millimetres.

| Request | Response | Errors |
| --- | --- | --- |
| JSON object with `content`, `font`, `size_mm`, `x_mm`, and `y_mm`. | `200` with `subpaths` in millimetres. | `400` unsupported font, empty or overlong text, out-of-range size/origin, or generated geometry beyond 10,000 mm. |

Text is 1–32 characters in the documented monoline font, height is 1–100 mm,
and the origin and generated strokes must stay within 0–10,000 mm. Coordinates
use at most three decimal places.

## Static SVG import

### `POST /api/v1/svg-import`

Accept raw `image/svg+xml` bytes up to 1 MiB and return normalized editable
geometry plus `provenance` identifying the built-in importer and its version. It
does not save the source or project, persist provenance, or assign machine
operations. Import the returned paths into a version 2 project with the regular
project endpoints.

| Request | Response | Errors |
| --- | --- | --- |
| Raw `image/svg+xml` body, up to 1 MiB. | `200` with imported subpaths and importer provenance. | `400` malformed SVG, missing physical dimensions, unsupported elements, active content, or external references; `401`/`403`; `413` body over 1 MiB. |

The root SVG must declare physical `width`, `height`, and
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
The existing `POST /api/v1/svg-import` endpoint invokes it and returns normalized
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

// Manifest returns stable, versioned capability metadata. Keep IDs stable:
// saved results may reference them.
func (Importer) Manifest() importer.Manifest {
    return importer.Manifest{
        ProtocolVersion: importer.ProtocolVersion,
        ID:              "example-dxf",
        Name:            "Example DXF importer",
        Version:         "1.0.0",
        SourceFormats: []importer.SourceFormat{{
            ID: "dxf", Name: "DXF",
            Extensions: []string{".dxf"},
            MediaTypes: []string{"image/vnd.dxf"},
        }},
        OutputTypes:   []importer.OutputType{importer.OutputEditableDesign},
        OptionsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
    }
}

// Import receives an immutable, registry-bounded request. The registry fills
// provenance after this method returns; never dispatch a machine or fetch a
// network resource here.
func (Importer) Import(ctx context.Context, request importer.Request) (importer.Result, error) {
    if err := ctx.Err(); err != nil {
        return importer.Result{}, err
    }
    if len(request.Source) == 0 {
        return importer.Result{}, errors.New("source is empty")
    }
    return importer.Result{
        OutputType: request.OutputType,
        Payload:    json.RawMessage(`{"objects":[]}`),
        Warnings: []importer.Warning{{
            Code: "empty-geometry", Message: "example importer produced no geometry", Lossy: true,
        }},
    }, nil
}
```

## Register and call it

Build a registry from trusted constructors and fail closed when a manifest is
invalid or duplicated. Call `Import` with an explicit importer ID, source format,
output type, source bytes, and options. Treat the result as untrusted input even
though the implementation is trusted: bound its size and validate geometry
before preview or save.

```go
registry, err := importer.NewRegistry(sample.Importer{})
if err != nil {
    return err
}
result, err := registry.Import(ctx, "example-dxf", importer.Request{
    SourceFormat: "dxf",
    OutputType:   importer.OutputEditableDesign,
    Source:       source,
})
```

## Compatibility and versioning

`importer.ProtocolVersion` and each manifest's `ProtocolVersion` must match. The
registry rejects unknown output types and mismatched formats. Capability metadata
is versioned; adding a new output type is a contract revision, not a silent
extension. Keep importer IDs and versions in persisted provenance so an
operator can identify the converter and options that produced a result. Do not
claim compatibility with every file from an external application; document the
supported subset and version limits.

## Safety and output handling

Conversion is not validation and never starts a machine. A candidate
`gcode_job` must pass the same driver validation, profile review, preview, and
explicit operator confirmation as a native job. Imported editable geometry is
saved with its operation assignment still pending; generation remains a separate
explicit step. Malformed, oversized, unsupported, externally referenced, or
active content must fail with an actionable error rather than a partial result.
Plugin processes are trusted code unless a separate isolation design exists;
this contract does not sandbox them.

## Plugins

### `GET /api/v1/plugins`

List configured preflight plugin manifests. Plugins are explicit trusted
executables loaded at startup. They are not sandboxes, machine drivers, or file
converters. See [the plugin protocol](plugins.md) for installation and limits.

| Request | Response | Errors |
| --- | --- | --- |
| No body. | `200` array of plugin manifests. | `401`/`403` authentication or role. |
