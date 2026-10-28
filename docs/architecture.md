# Architecture

EmberDeck is one Go executable with server, driver, and all-in-one modes. The
server owns durable jobs, editable projects, declared profiles, assignments,
authentication, and the embedded browser
console. A worker owns one controller connection per job and reports progress
over a versioned HTTP polling protocol. One server can manage multiple workers.

```text
Browser → HTTP server → durable job library (single-owner file lock)
                   ↑
              HTTP polling
                   ↑
         Worker → GRBL wire connection → serial / TCP / simulator
                   
Server dispatch → versioned preflight plugins → allow / veto
```

## Storage and ownership

Library mutations write and sync a temporary file before replacing jobs.json,
profiles.json, or designs.json. Profiles and editable projects are separate from
generated jobs; each mutation replaces one library under the server's lock.
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
and multi-server high availability have not been qualified. The whole job library
is currently rewritten per mutation, so very large libraries need future work.

Each driver ID has one live process session. Each assignment has a random lease.
A worker executes a lease once, reports terminal results idempotently, and stops
on network failure. The server treats expired leases and restarts as interrupted
work and never automatically reassigns it. Inspection is required before retry.

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

Remote workers must use TLS or a trusted TLS tunnel. Operators and workers share
one key in the initial implementation. Multi-user permissions, separate worker
credentials, and camera access controls remain on the roadmap.

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
rectangle, ellipse, and line paths with numeric position and size controls;
saved unassigned paths remain editable, but cannot generate a job until an
operator assigns them. Cutting paths must be closed and enclose nonzero area.

A selected GRBL 1.1 profile declares a millimetre work area with a lower-left
origin, feed and commanded-power limits, and controller settings. These values
are operator declarations; the server does not measure the machine, home it,
set its origin, or configure GRBL.
The supported power mapping requires matching `$30`, `$31=0`, and `$32=1`.
Selecting a profile does not verify a connected driver's configuration; the
operator must match the profile and prepared work coordinates to that machine.

Profile and project revisions prevent silent overwrites. Changing a profile
makes bound projects stale until the operator reviews and resaves them.
Generation validates geometry and the entire GRBL program, then creates an
ordinary queued job with immutable project/profile snapshots. Version 2 emits
operations in layer order, paths in object order, laser-off rapid moves, and
GRBL dynamic laser mode for both cut and engrave operations. Editing or deleting
the source does not change generated bytes or provenance. Dispatch keeps the
existing confirmation, plugin preflight, and worker lease.

The SVG previews show static model geometry. The actual starting head position
is unknown, so the preview does not invent a travel path from the work origin.
They do not represent live execution. SVG/text import, rotation, alignment,
multi-selection, transforms, raster engraving, and live toolpath tracking are
not implemented by this milestone; see the
[designer plan](research/designer-architecture.md) and
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
