# ESP32 worker contract prototype

This directory contains a portable C host prototype for the device-side job
staging gate described in the ESP32 feasibility report and issue #57. It stages
bounded content through an injected storage interface, verifies exact size and
SHA-256, checks the full G-code framing contract, and then returns an opaque
handle that can stream the original bytes to an injected UART sink. The device
contract caps a program at 4 MiB; an over-cap manifest is rejected without a
fetch and clears any previously staged bytes when storage reset succeeds.

**Host prototype only.** It does not implement EmberDeck network sessions,
worker credentials, leases, polling, heartbeats, progress, or receipts. It does
not qualify any board, UART wiring, controller, laser, or physical hardware.
The test UART is a memory sink and sends no physical output. This prototype is
not a supported or deployable worker firmware.

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
verification/single-use gate. They do not cover HTTP sessions, lease renewal,
receipts, or lost network responses, and do not require `idf.py` or an ESP-IDF
toolchain. No ESP-IDF target build or physical hardware test is claimed by this
prototype.
