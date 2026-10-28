# Architecture

EmberDeck is one Go executable with server, driver, and all-in-one modes. The
server owns durable jobs, assignments, authentication, and the embedded browser
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

Library mutations write and sync a temporary file before replacing jobs.json.
A failed write does not publish an in-memory change. A cross-platform OS lock
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

## Verification boundaries

HTTP tests cover persistence, lifecycle, duplicate ownership, and plugin vetoes.
The distributed integration test crosses real HTTP, worker dispatch, and the
GRBL simulator for completion and cancellation. Driver tests use wire peers;
plugin tests execute real child processes; serial tests inject the OS connector.
Browser verification covers operator flows. CI exercises three native operating
systems and a Docker smoke test. Physical qualification remains separate.
