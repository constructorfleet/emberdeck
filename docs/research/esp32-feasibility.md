# ESP32 worker feasibility

**Evidence checked:** 2026-10-28. ESP-IDF references are pinned to v6.1; GitHub source and ESPHome documentation references use commit IDs.

**Decision: conditionally feasible as a worker that forwards EmberDeck's existing GRBL byte protocol to an already qualified controller.** A standalone ESP-IDF implementation is the best fit. Do not claim support for direct laser/motion control or ESPHome integration today.

The server and Go worker implement a bounded, digest-verified transfer protocol. A portable native C prototype now exercises bounded staging, content verification, and the no-output-before-verification gate. Production MCU firmware must still integrate that contract with the worker lifecycle. No board, electrical interface, or physical machine is qualified.

This report records the feasibility investigation requested by [issue #3](https://github.com/constructorfleet/emberdeck/issues/3), which is closed after its decision and milestone plan. The first no-motion milestone is implemented as a host-only contract prototype in [`firmware/esp32-worker/`](../../firmware/esp32-worker/README.md).

The prototype is not an ESP-IDF target build. It does not implement network sessions, worker credentials, leases, polling, heartbeats, progress, receipts, or a controller state machine. Its fake UART is an in-memory test sink. No ESP32 board, electrical interface, or physical machine is supported or qualified.

The active server and Go worker behavior below was checked against `docs/api.md`, `docs/architecture.md`, `docs/serial.md`, `docs/worker.md`, `internal/server/lifecycle.go`, `internal/server/server.go`, `worker/worker.go`, and `driver/driver.go`.

## Existing EmberDeck boundary

EmberDeck has a server-owned persistent job library and authenticated HTTP worker poll protocol. A worker registers a process-unique session; an assignment grants one job and lease. It reports acknowledged executable lines and the last controller state, and terminal receipts are lease-bound.

The server expires a worker after ten seconds without heartbeat. The Go worker polls each second and reports each second. Protocol 2 workers advertise `chunked_content_v1`, receive immutable size/digest metadata, and fetch bounded ranges under the same session and lease. Protocol 1 workers still receive the complete `job.gcode` in their assignment JSON.

Program content is capped at 4 MiB; upload JSON is capped at 24 MiB plus 64 KiB to accommodate JSON escaping and framing. These limits and semantics are described in the [API contract](../api.md) and implemented in [`worker/worker.go`](../../worker/worker.go), [`server.go`](../../internal/server/server.go), and [`lifecycle.go`](../../internal/server/lifecycle.go).

The controller implementation validates the full program, sends one line at a time, and waits for GRBL acknowledgment. It checks fresh initial status, reports acknowledged-line progress, injects and acknowledges `M5`, and completes only after `Idle`.

Pause needs fresh `Hold:0`/`Idle`; resume needs fresh `Run`/`Idle`. One connection and lease stay active while paused. Poll failure cancels the stream. Cancellation attempts a bounded hold/reset but is not a physical stop. Heartbeat expiry or a server restart interrupts active work. A device must never reconnect and replay a job or infer resume from reboot/status alone. See the [driver contract](../driver.md), [worker contract](../worker.md), and [architecture](../architecture.md).

## Architecture comparison

| Approach | Fit | Decision |
| --- | --- | --- |
| Standalone ESP-IDF worker firmware | Can own Wi-Fi, TLS, session/lease polling, bounded job transfer, GRBL UART, watchdog/failure states, and diagnostics without changing EmberDeck's controller boundary. | **Preferred conditional prototype.** Implement a small state machine, not a general GRBL controller. |
| ESPHome external component | External components compile into ESPHome builds; its native API/OTA target Home Assistant, not EmberDeck sessions, leases, acknowledgments, or pause rules. It does not solve full-job RAM or prove safe behavior. ESPHome excludes custom components from security support. | **Not the runtime.** Could only be an optional configuration wrapper around the same tested worker core. [External components at docs commit `7f1ec35`](https://github.com/esphome/esphome-docs/blob/7f1ec35b8bdc5fb1c8284bcabe438eb2de1b51dd/src/content/docs/components/external_components.mdx), [security scope at that commit](https://github.com/esphome/esphome-docs/blob/7f1ec35b8bdc5fb1c8284bcabe438eb2de1b51dd/src/content/docs/guides/security_best_practices.mdx). |
| Existing motion firmware (FluidNC) | FluidNC is ESP32 CNC firmware with GRBL-compatible G-code, machine configuration, and its own web UI. It directly owns motion/laser outputs rather than acting as a network-to-existing-controller bridge. | Use only as a **controller endpoint** after separate compatibility and hardware qualification; do not embed it as EmberDeck worker firmware. It duplicates the existing GRBL controller role and changes the safety/qualification scope. [FluidNC README at commit `c82d217`](https://github.com/bdring/FluidNC/blob/c82d217cad69fa937d57cee24fcf7ce9080fba05/README.md). |
| Existing serial bridge (ESP-Link) | ESP-Link describes an ESP8266 Wi-Fi/serial bridge on TCP port 23. Multiple clients can intermix serial input; its README calls master unstable and lists v2.2.3 as latest. | **Reject as worker.** It lacks EmberDeck leases/control/progress/no-replay. A bridge is only a transport fixture with exclusive access and separate worker software. [ESP-Link README at commit `cf329e8`](https://github.com/jeelabs/esp-link/blob/cf329e8b84a55a05afa04df971d9abd9793c9130/README.md). |

Keep the present boundary: EmberDeck owns jobs and authorization; a single-purpose MCU worker owns one active lease and translates protocol bytes; a separate GRBL-compatible controller owns motion and laser outputs. The candidate MCU must not drive stepper/laser outputs in this scope.

This avoids treating network connectivity as a safety function and avoids claiming a new direct-control firmware before its entire motion-control stack is qualified.

## Payload and device resources

The server caps program content at 4 MiB and serves protocol 2 content in 64 KiB-or-smaller ranges. The Go worker reassembles this bounded content in memory, verifies exact size and SHA-256, validates the whole G-code, and only then opens the controller. Chunking bounds individual HTTP responses but does not yet bound the worker's full-program staging memory. An MCU should validate from PSRAM or flash, or use a lower negotiated cap enforced by server and device.

ESP32-S3 has 512 KiB internal SRAM, insufficient for a full 4 MiB staging buffer. Espressif lists modules with up to 16 MiB PSRAM and DevKitC-1 N8R8 with 8 MiB octal PSRAM. That makes a streamed/PSRAM prototype plausible but does not prove available headroom under TLS.

See the [ESP32-S3 chip series comparison in ESP-IDF v6.1](https://docs.espressif.com/projects/esp-idf/en/v6.1/esp32s3/hw-reference/chip-series-comparison.html), [WROOM-2 datasheet](https://documentation.espressif.com/esp32-s3-wroom-2_datasheet_en.html), and [DevKitC-1 user guide v1.1 at commit `bd4d4ac`](https://github.com/espressif/esp-dev-kits/blob/bd4d4ac18657e73583af3d7c0b0a23c3cddf3735/docs/en/esp32-s3-devkitc-1/user_guide_v1.1.rst).

Prototype against **bounded chunks or a streaming response**, with the server preserving a single lease and the device validating before the first controller byte. Prefer a two-phase job fetch with immutable content digest/size, chunk range or chunk sequence, and a final verify step; keep server-side whole-program validation authoritative.

A temporary-file approach requires adequate flash/partition design and wear analysis. External PSRAM can be used as a bounded buffer, not as evidence that the present protocol is safe. A limited first prototype may impose a lower job-size cap, but that cap must be enforced by server and device and documented. Do not silently truncate or start a partial job.

## Network, credentials, discovery, and update model

For a remote server, the worker needs Wi-Fi, HTTPS server verification, DNS/time handling, certificate roots or a managed pin-rotation plan, and a device-unique credential. EmberDeck now has revocable worker credentials scoped to driver IDs; provision one per device and keep the admin credential off workers. Do not accept plaintext HTTP on untrusted networks.

ESP-IDF v6.1 offers HTTPS OTA and certificate-bundle verification, but not EmberDeck's update policy. See [HTTPS OTA and root verification](https://docs.espressif.com/projects/esp-idf/en/v6.1/esp32/api-reference/system/esp_https_ota.html) and [secure boot guidance](https://docs.espressif.com/projects/esp-idf/en/v6.1/esp32/security/secure-boot.html).

Provision Wi-Fi/server URL over USB/serial or a physically initiated setup mode. Discovery must be opt-in, status-only, and never expose keys or assign work. Local diagnostics should redact credentials and report controller state, lease digest, progress, firmware version, and fault. Persist pin/baud/server changes atomically after deliberate provisioning.

Firmware updates need authenticated, integrity-checked images, rollback/recovery, and power-loss testing. ESP-IDF supports OTA rollback and secure transport; review fuse programming and recovery implications before enabling Secure Boot or flash encryption on production units. See [ESP-IDF v5.3.4 OTA security](https://docs.espressif.com/projects/esp-idf/en/v5.3.4/esp32/security/security.html) and [ESP-IDF v6.1 Secure Boot](https://docs.espressif.com/projects/esp-idf/en/v6.1/esp32/security/secure-boot.html).

ESPHome's 2026.10.1 OTA documentation describes optional pre-shared-key encryption and safe mode. These depend on configured firmware and do not replace EmberDeck device identity or release policy. See [ESPHome OTA at docs commit `88c35dd`](https://github.com/esphome/esphome-docs/blob/88c35dd17ae549f01fe25a130680f4d77bb265fe/src/content/docs/components/ota/esphome.mdx).

## Controller connection and candidate board

Candidate for bench prototyping only: **Espressif ESP32-S3-DevKitC-1-N8R8**, listed with 8 MiB flash and 8 MiB octal PSRAM at 3.3 V SPI. It is not a supported production board. Connect UART TX/RX/GND only to a verified compatible GRBL 1.1 controller port.

ESP32-S3 has three configurable UARTs, but usable pins, boot/strapping pins, console conflict, USB connector behavior, and GPIO availability depend on exact board revision/module. See the [DevKitC-1 user guide v1.1 at commit `bd4d4ac`](https://github.com/espressif/esp-dev-kits/blob/bd4d4ac18657e73583af3d7c0b0a23c3cddf3735/docs/en/esp32-s3-devkitc-1/user_guide_v1.1.rst) and [ESP-IDF v6.1 UART reference](https://docs.espressif.com/projects/esp-idf/en/v6.1/esp32s3/api-reference/peripherals/uart.html).

ESP32 GPIO is a 3.3 V domain; no board spec establishes the target controller's UART voltage, isolation, or compatibility. Before wiring, verify idle voltage, logic thresholds, grounding/isolation, pinout, baud/framing, reset-on-open, and sender contention. Use appropriate level shifting/isolation; never connect GPIO directly to 5 V or RS-232, stepper-driver signals, or laser enable/PWM.

The chosen controller link is UART TX/RX. The S3's native USB Serial/JTAG CDC works directly to a PC for console/flashing without a USB-UART bridge. Hosting a USB CDC controller as USB host is a separate, unqualified design and is not implied by native USB Serial/JTAG.

See the [WROOM-2 datasheet](https://documentation.espressif.com/esp32-s3-wroom-2_datasheet_en.html), [ESP-IDF v6.1 USB Serial/JTAG guide](https://docs.espressif.com/projects/esp-idf/en/v6.1/esp32s3/api-guides/usb-serial-jtag-console.html), and [UART-to-PC connection options](https://docs.espressif.com/projects/esp-idf/en/v6.1/esp32s3/get-started/establish-serial-connection.html).

No common board is supported yet. The N8R8 is a practical resource-rich prototype candidate; a no-PSRAM ESP32-S3 or another ESP32 variant should not be accepted until the selected streaming/buffer design passes RAM and TLS stress tests. Board-specific safety outputs, interlocks, and emergency-stop circuits remain external and independent of firmware.

## Required worker state and failure behavior

Implement and test this contract before connecting a laser-capable machine:

- One process/device boot creates a fresh session. One assignment lease is executed once; lost poll/receipt responses must not cause source replay. A reset loses active execution state and reports/re-registers so the server marks the old lease interrupted. Never auto-resume on boot, reconnect, or a controller `Run`/`Idle` observation.
- Validate job size, framing, digest, and all G-code before sending the first byte. Then send one line and wait for exactly one expected acknowledgment; report `acked`, `total`, and controller status. Complete only with all source lines acknowledged, injected/acknowledged `M5`, then `Idle` confirmed by a fresh query.
- Keep the same lease and controller connection through pause. A pause assignment before source output must gate all output. Pause uses GRBL feed hold, reports `pausing` through `Hold:1`, and only reports `paused` after fresh `Hold:0`/`Idle`. Resume requires the server's explicit resume command and a fresh post-command `Run`/`Idle`; never infer permission from status alone.
- Cancel, watchdog expiry, brownout/reset, UART error, HTTP/TLS failure, heartbeat expiry, invalid response, or controller timeout must gate further G-code immediately and take a bounded best-effort hold/reset where the controller protocol supports it. The server must be allowed to interrupt the lease. No software path can promise that the physical laser is off; independent interlocks and emergency stop stay in series with laser enable.
- Power interruption during firmware update must boot either the old known-good image or a recovery mode, never an unsafe partially updated image. A failed update must not reset or requeue a job. Firmware health checks must occur with laser output disabled.

GRBL status/ack behavior and controller capabilities must be verified against the exact controller firmware, since current EmberDeck documents GRBL 1.1 compatibility but has no physical controller qualification. See the [GRBL interface](https://github.com/gnea/grbl/blob/bfb67f0c7963fe3ce4aaf8a97f9009ea5a8db36e/doc/markdown/interface.md) and [realtime commands](https://github.com/gnea/grbl/blob/bfb67f0c7963fe3ce4aaf8a97f9009ea5a8db36e/doc/markdown/commands.md).

## Licensing and maintenance

EmberDeck is AGPL-3.0. ESP-IDF v6.1's main repository `LICENSE` is Apache-2.0. ESPHome's license is mixed: its C/C++ runtime is GPLv3, while Python and other files are MIT. FluidNC applies GPL-3.0-or-later to files without another notice.

A firmware port that reuses EmberDeck code carries its existing AGPL obligations. An independently written protocol implementation on ESP-IDF still needs its own licensing decision and a dependency/license review. Reusing or combining FluidNC code likewise needs file-level review; this report makes no blanket compatibility conclusion.

See the [FluidNC license at commit `c82d217`](https://github.com/bdring/FluidNC/blob/c82d217cad69fa937d57cee24fcf7ce9080fba05/LICENSE), [ESPHome license at commit `0544ec9`](https://github.com/esphome/esphome/blob/0544ec9cd21fa3c41f17b9c66a8351be1a750eda/LICENSE), and [ESP-IDF v6.1 license](https://github.com/espressif/esp-idf/blob/v6.1/LICENSE).

Maintenance includes MCU protocol/TLS, certificate and credential rotation, flash layout, signed builds/rollback, board matrix, and controller-specific serial behavior. ESPHome external components are outside its security support. FluidNC retains its own release/qualification cadence if used.

## Prototype gates and hardware qualification

1. **Contract prototype (no motion hardware).** The server/Go protocol side of [#57 chunked verified transfer](https://github.com/constructorfleet/emberdeck/issues/57) and the portable C staging gate are implemented. The C prototype in [`firmware/esp32-worker/`](../../firmware/esp32-worker/README.md) enforces a 4 MiB cap, requests ordered ranges no larger than 4 KiB, checks SHA-256 and G-code framing, seals staged storage, and permits one fake-UART send only after verification.

   Native CMake/CTest coverage includes valid multi-range content, truncation, digest mismatch, malformed G-code, oversized manifests, out-of-order fetch, partial-stage cleanup, storage mutation, and no replay. These are host contract tests, not HTTP/session/lease/receipt tests and not an ESP-IDF build. The remaining work is device integration and #57's session, lease, progress, receipt, and protocol fault tests. Worker credential scoping is implemented by [#56](https://github.com/constructorfleet/emberdeck/issues/56).

2. **Protocol prototype (controller with motor/laser outputs disconnected).** Implement the ESP-IDF worker state machine on N8R8. Run UART transcripts for initial `Idle`, each `ok`, errors, alarm, stale status, hold `Hold:1`/`Hold:0`, pause-before-first-line, resume fresh status, cancel, and timeout. Assert exact emitted bytes and no G-code after each injected failure.

   Exercise heap/PSRAM high-water use under TLS, maximum configured job, Wi-Fi reconnect, bad clock/certificate, server outage, and watchdog reset.

3. **Electrical bench qualification.** For each named board and controller combination, document schematic/connector, voltage levels, isolation, ground/reference, baud/reset behavior, EMC/noise, power brownout, boot pin states, and USB/UART contention. Verify boot/reset/update failure always leaves laser enable de-energized at hardware level.

4. **Machine qualification (separate approval/evidence).** Keep laser disabled initially; use a test controller load, then motion-only with laser physically disconnected, then manufacturer-approved low-power testing in an enclosed machine. Measure stop/hold latency and failure response under Wi-Fi loss, server loss, MCU reset, and controller disconnect.

   Verify the independent door interlock, emergency stop, extraction, fire safeguards, and restart procedure. Publish a supported board/controller/firmware/baud matrix only after signed test reports and operator instructions exist.

**Acceptance scope:** The #3 feasibility decision and no-motion host contract prototype are complete; deployable ESP32 firmware is not. ESP32-S3-DevKitC-1-N8R8 is a candidate, not a supported board. ESPHome is unsuitable as the job-worker runtime unless it can host the exact lease/control contract without compromising failure behavior; the cited ESPHome docs do not describe such an integration. FluidNC is a direct motion controller option, not a bridge implementation.

Blockers for MCU support include ESP-IDF integration of network authentication, sessions and leases; progress and receipt handling; controller pause/resume/cancel behavior; secure update lifecycle; exact UART electrical target; protocol fault testing on firmware; and successful physical qualification. No firmware, board, or laser operation is claimed available.
