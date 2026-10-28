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

**Host prototype only.** The coordinator defines injected session, assignment,
range-fetch, active-poll, controller, and receipt interfaces; it does not
implement the EmberDeck HTTP client, session ID generation, worker credentials,
or retries after lost receipts. The one-second polling requirement is an
adapter contract, not a timer enforced by this portable core. The fake
controller polls after acknowledged lines; it cannot prove cadence during a
long-running command or blocked UART. It does not qualify any board, UART
wiring, controller, laser, or physical hardware. The test HTTP adapter and
controller are in-memory fakes and send no physical output. This prototype is
not supported or deployable worker firmware.

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
registration layout. The native tests cover ordered bounded fetches,
short/truncated data, digest mismatch, malformed G-code, over-cap manifests,
cleanup after partial staging, storage immutability, and the no-output-before-
verification/single-use gate. The cycle tests exercise the public coordinator
seam with fake session, assignment, active-poll, range, and report callbacks
plus a fake controller and memory UART sink, including repeated progress polls,
cancellation, active-poll failure, lease binding, and terminal completion gating.
They do not cover real HTTP sessions,
measured polling cadence, retries after lost terminal reports, or physical
hardware, and do not require `idf.py` or an ESP-IDF toolchain. No ESP-IDF target
build or physical hardware test is claimed by this prototype.
