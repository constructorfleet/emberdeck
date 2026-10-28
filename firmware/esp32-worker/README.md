# ESP32 worker contract prototype

This directory contains a portable C host prototype for the device-side job
staging gate described in the ESP32 feasibility report and issue #57. It stages
bounded content through an injected storage interface, verifies exact size and
SHA-256, checks the full G-code framing contract, and then returns an opaque
handle that can stream the original bytes to an injected UART sink. The
one-assignment coordinator opens a fresh session through an adapter, polls once,
binds range fetches and a terminal report to that assignment's lease, and passes
the verified handle to the injected controller adapter only after staging and
verification succeed. That adapter owns GRBL line buffering, UART writes, and
acknowledgment waits; it must call the active-poll callback at least once per
second while sending or waiting for a controller acknowledgment. The server
expires sessions after 10 seconds, so this cadence leaves room for network and
scheduling delays. Each poll carries the original session, job, and lease plus
monotonic acknowledged-line progress. The coordinator rejects regressing
progress or a changing total, and rejects a response bound to another job or
lease. A cancel response stops further output and reports canceled under the
same lease. An active-poll failure stops output and attempts a failed receipt;
the cycle never claims or replays another assignment. Pause/resume are not
implemented by this slice. The device contract caps a program at 4 MiB; an
over-cap manifest is rejected without a fetch and clears any previously staged
bytes when storage reset succeeds.

**Worker contract and ESP-IDF transport prototype.** The coordinator defines
injected session, assignment, range-fetch, active-poll, controller, and receipt
interfaces. The portable HTTP
poll adapter now owns fresh 128-bit session generation, capability advertisement,
and bounded parsing of idle or protocol-2 assignments. It sends the worker key
as a bearer token and accepts a job only when its ID, lease, size fields, and
SHA-256 encoding are valid. The ESP-IDF adapter uses `esp_http_client`, the
certificate bundle, disabled redirects, bounded response buffers, and
`esp_fill_random`. The NVS receipt store uses a versioned, checksummed blob.
Credentials are supplied by the caller; Wi-Fi setup, secure credential
provisioning, and controller integration remain application responsibilities.
The adapter fetches lease-bound byte ranges and requires exact 206 status, Content-Range,
Content-Length, content type, and manifest digest headers. Active polls must
match the assigned job and lease. Terminal reports retry the identical JSON
receipt after transport errors or server errors, relying on the server's
idempotent receipt contract. The coordinator also writes the exact terminal
receipt, session, job ID, and lease to an injected receipt store before the
first POST. A saved receipt is retried before any assignment poll; the store is
cleared only after HTTP 200, then polling continues on the saved session. The
store's `save` and `clear` callbacks must return success only after durable
replacement or removal. Closing a session clears local assignment state without
an HTTP request; the server expires sessions by heartbeat TTL. Native
tests use an in-memory fake HTTP transport rather than a socket HTTP server.
The one-second polling requirement is an adapter contract, not a timer enforced
by this portable core. The fake controller polls after
acknowledged lines; it cannot prove cadence during a long-running command or
blocked UART. It does not qualify any board, UART wiring, controller, laser, or
physical hardware. The fake HTTP test drops all initial terminal acknowledgments,
then verifies that a later run retries the same receipt on the saved session
without streaming the assignment again. The test transport and controller send
no physical output.
This is still a worker prototype, not deployable or qualified firmware.

The storage adapter owns placement and lifetime (for example PSRAM or flash).
Its `reset` operation must discard the previous staged image; calling it with
size zero discards a partial image after any verification failure. `seal` must
make the verified generation immutable until `unseal` is called during handle
release. The caller must retain the storage backend and its context while a
verified handle exists. Before UART output, the core re-reads and re-hashes
staged bytes, then emits only from the sealed generation. A storage failure or
digest mismatch during that pre-pass emits no UART bytes. The handle is
single-use, including when the UART sink reports an error. Handle operations
are single-task only; callers must serialize send and release.

The host parser mirrors the worker framing checks in `driver.Validate`: only
printable ASCII plus LF/CR/tab are accepted; nested, unmatched, or unclosed
parenthetical comments fail; semicolon and parenthetical comments are stripped
for line validation; real-time GRBL bytes outside comments fail; trimmed lines
are limited to 79 bytes; and at least one executable line is required. It
validates incrementally from staged storage and preserves the original bytes
for the UART stream.

## Host build and test

From this directory, with CMake, a C11 compiler, and CTest installed:

```sh
cmake -S . -B build
cmake --build build
ctest --test-dir build --output-on-failure
```

`component/CMakeLists.txt` also provides the conventional ESP-IDF component
registration layout. GitHub CI builds and links `idf-smoke/` with ESP-IDF 5.3.2
for ESP32. This compile smoke does not start Wi-Fi, contact a server, or qualify
a board. The native tests cover ordered bounded fetches,
short/truncated data, digest mismatch, malformed G-code, over-cap manifests,
cleanup after partial staging, storage immutability, and the no-output-before-
verification/single-use gate. The HTTP client tests cover fresh session IDs,
request authentication and capability advertisement, idle/protocol-2 response
parsing, manifest binding, and rejection of malformed, duplicate-field, and
legacy assignment responses. The adapter test drives the full coordinator with
a fake HTTP server and UART, including malformed or truncated ranges, digest
mismatch, invalid active responses, lost terminal responses, and durable receipt
recovery after all initial acknowledgments are lost. The cycle tests exercise the
public coordinator seam with fake session, assignment, active-poll, range,
report, and receipt-store callbacks plus a fake controller and memory UART sink,
including repeated progress polls, cancellation, active-poll failure, lease
binding, and terminal completion gating.
Native tests do not exercise the real TLS transport or measure polling cadence.
The local CMake build does not require `idf.py`; the ESP-IDF target build runs in
GitHub CI. No physical hardware test or qualification is claimed.
