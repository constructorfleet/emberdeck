# Architecture

EmberDeck is one Go executable with server, driver, and all-in-one modes. The
server owns durable jobs, editable projects, declared profiles, assignments,
authentication, and the embedded browser
console. A worker owns one controller connection per job and reports progress
over a versioned HTTP polling protocol. One server can manage multiple workers.

Workers advertising `chunked_content_v1` receive a protocol 2 assignment with
immutable content size and SHA-256, then fetch 64 KiB-or-smaller ranges under
their live session and job lease. The worker verifies complete framing, size,
digest, and G-code before opening the controller. Protocol 1 workers keep the
whole-program assignment for compatibility. The shared `jobtransfer` contract
owns the 4 MiB cap, range limit, and manifest verification.

```text
Browser → HTTP server → durable job library (single-owner file lock)
                   ↑
              HTTP polling
                   ↑
         Worker → GRBL wire connection → serial / TCP / simulator
                   
Server dispatch → versioned preflight plugins → allow / veto
```

An optional, separately bound Prometheus listener reports server-observed job
states and driver heartbeats. It is disabled by default and does not aggregate
metrics from independent worker processes; distributed workers report their
state to the central server. See [metrics and monitoring](metrics.md).

## Storage and ownership

Library mutations write and sync a temporary file before replacing jobs.json,
profiles.json, designs.json, or credentials.json. Profiles and editable projects
are separate from generated jobs; each mutation replaces one library under the
server's lock.
Profile/project libraries are validated on startup; corrupt records or broken
references fail closed. An older project binding is a supported stale state,
not corruption, and requires review/resave before generation.
On Linux/macOS the containing directory is synced after rename, too. A failed
write does not publish an in-memory change. A directory-sync failure after
rename disables the API until restart and inspection, because persistence is
uncertain. Windows flushes the file but lacks directory-sync support in this
backend; power-loss recovery there is not guaranteed. Keep backups and inspect
the machine after any unclean shutdown before starting work. A cross-platform OS lock
prevents concurrent servers from writing the same directory. Back up the data
directory with the server stopped. Local filesystem semantics are assumed; NFS
and multi-server high availability have not been qualified.

The job library keeps each program in its own file under `programs/` and only
metadata, sizes, and SHA-256 digests in jobs.json. Progress-only worker reports
stay in memory; state transitions are synced before the request is answered, and
a clean shutdown saves pending progress. Startup migrates an older embedded-program
jobs.json atomically and fails closed on a missing or damaged program file.
Each state transition also appends an event to `job-events.jsonl`, synced before
the jobs.json rename, which records the last committed event sequence. Startup
discards uncommitted events and fails closed on damaged committed history.
Retention is bounded by event count and age.

Each driver ID has one live process session. Each assignment has a random lease.
A worker executes a lease once and reports terminal results idempotently. An
idle worker retries failed polls with jittered backoff and a fresh session; a
worker holding an active lease fails closed on network failure. The server treats expired leases and restarts as interrupted
work and never automatically reassigns it. Inspection is required before retry.

The `toolpath` package parses a bounded subset of linear XY G-code for display
and correlates fresh GRBL work-position reports to those segments. The driver
retains intermittent work-coordinate offsets and forwards optional line-number
markers. Worker reports keep acknowledged line counts distinct from execution
telemetry; the server validates and persists the latter with the job. Restart
or heartbeat expiry clears active correlation and marks confidence unknown,
retaining only the completed lower bound. This is a visualization estimate,
not a measurement of beam output or material removal.

Uploaded jobs retain a compact profile-review summary and profile revision.
Line-length validation runs before upload succeeds. Known path bounds are
compared with the selected lower-left work envelope; unsupported or incomplete
motion, a missing starting position, or a stale profile leaves the whole-program
bounds status unknown. Dispatch requires an explicit bounds override for
uploaded jobs whose status is not in bounds. Designer-generated jobs retain
their separate validated provenance and profile checks.

## Controller boundary

GRBL 1.1 send/response sends one line and awaits its acknowledgment. A readiness
status check precedes motion, and a final Idle check precedes completion. Controller
acceptance is not proof that physical motion has finished. Cancellation attempts
feed hold and soft reset, then closes the connection. Physical interlocks and an
emergency stop remain mandatory. No hardware has been qualified yet.

The simulator uses the same byte-stream interface. Simulator success proves the
software flow, not machine calibration, optics, work envelope, extraction,
interlocks, fire protection, or serial electrical behavior.

## Plugins and access

Plugins are separate processes speaking bounded versioned JSON. This avoids Go
shared-library ABI restrictions across operating systems. Plugins are trusted
code with the server's OS privileges; this is not a sandbox. OctoPrint Python
plugins are not directly compatible.

Authentication is a durable credential registry, not a process-wide shared-key
check. It stores SHA-256 secret hashes and metadata; bearer secrets are generated
with 256 bits of entropy and compared in constant time. Route registration
centralizes role authorization: viewers read, operators also mutate/generate,
admins additionally manage credentials, and workers can only poll/report for
their explicitly assigned driver IDs. Active worker scopes cannot overlap.
Revoking a worker credential removes its sessions and interrupts its active
leases before acknowledging revocation; no other worker is changed and no job is
replayed. If the credential-store file is absent, startup atomically seeds an
admin hash from `EMBERDECK_API_KEY` for explicit single-key migration. Once the
store exists, the environment key is not a fallback. Admin's worker-poll access
is retained only for bootstrap/local-worker compatibility; distributed workers
should use scoped worker credentials.

Remote workers must use TLS or a trusted TLS tunnel. Use a separate, revocable
worker credential for each device and scope it to the driver's ID. Camera access
controls are not implemented.

The browser console shell, its stylesheet, script, and favicon are public assets.
Only paths under `/api/` require the bearer key, so a mistyped URL returns an
ordinary `404` instead of an authentication error. The assets never include job,
profile, or project data.

## Verification boundaries

HTTP tests cover persistence, lifecycle, duplicate ownership, and plugin vetoes.
The distributed integration test crosses real HTTP, worker dispatch, and the
GRBL simulator for completion and cancellation. Driver tests use wire peers;
plugin tests execute real child processes; serial tests inject the OS connector.
Browser verification covers operator flows. CI exercises three native operating
systems and a Docker smoke test. Physical qualification remains separate.

## Editable source and generated programs

Version 1 designer projects store one rectangle and one vector-engraving
operation. Version 2 stores multiple editable line paths, each assignable to an
ordered vector-engraving or vector-cut layer. The browser creates and edits
rectangle, ellipse, and line paths with numeric position, size, translation,
scale, and rotation controls. Per-object transforms stay in the saved source;
preview, profile bounds validation, and job generation apply the same transform
around the source geometry's bounding-box center. Saved unassigned paths remain
editable, but cannot generate a job until an operator assigns them. Cutting
paths must be closed and enclose nonzero area after transformation.

A selected GRBL 1.1 profile declares a millimetre work area with a lower-left
origin, feed and commanded-power limits, and controller settings. These values
are operator declarations; the server does not measure the machine, home it,
set its origin, or configure GRBL.
The supported power mapping requires matching `$30`, `$31=0`, and `$32=1`.
Workers read these values with GRBL's read-only `$$` during readiness, before
any program byte. The server refuses to start a profile-generated job (409) when
the driver's reported `$30`, `$31`, or `$32` differ from the job's profile
snapshot, and the worker re-checks before streaming. Uploaded jobs are warned, not
blocked, for `$32=0` or `M3`. The operator must still match the prepared work
coordinates to that machine.

Profile and project revisions prevent silent overwrites. Changing a profile
makes bound projects stale until the operator reviews and resaves them.
Generation validates geometry and the entire GRBL program, then creates an
ordinary queued job with immutable project/profile snapshots. Version 2 emits
operations in layer order, paths in object order, laser-off rapid moves, and
GRBL dynamic laser mode for both cut and engrave operations. Editing or deleting
the source does not change generated bytes or provenance. Dispatch keeps the
existing confirmation, plugin preflight, and worker lease.

The designer SVG previews show static model geometry. The operator console also
has a separate camera-free live view for supported uploaded or generated linear
XY G-code. It shows laser-off travel and available work position; unsupported
motion is surfaced as warnings. Position matching assumes `$13=0` and is not
machine-bound or safety-qualified. The bounded SVG importer supports static line
geometry, cubic/quadratic Bezier curves flattened at a 0.05 mm chord tolerance,
and element-level affine transforms. It rejects styles, text, nested groups,
arc commands, and active or external content. Arbitrary path editing, undo/redo,
nested SVG groups, and raster engraving remain future work; see the [designer
plan](research/designer-architecture.md) and
[operator guide](operator-guide.md).

## Confirmed controller control

Pause/resume is an explicit state machine across API, worker, and controller.
The server stores operator intent (`pausing` / `resuming`) separately from the
last observed controller state. Only the worker can confirm `paused` after
`Hold:0` or `Idle`, or resumed execution after a fresh `Run` / `Idle`. The driver
gates source dispatch while controlling or held, including an in-flight line's
acknowledgment and the final M5 cleanup. One connection and lease span the entire
hold; neither reconnection nor replay is a resume operation.

Workers advertise the capability before the server accepts control requests.
GRBL feed hold is a software motion control, not an enclosure interlock or a
physical emergency stop. Cancellation and communication failure still take the
bounded stop/reset path. Any active control state is interrupted on restart or
heartbeat expiration, and never automatically resumed.
