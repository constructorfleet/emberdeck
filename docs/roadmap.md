# Product status

EmberDeck is an early-stage job manager, not a certified machine controller.
Simulator and software checks do not qualify a physical machine. See
[Safety & Limitations](safety.md) before operating hardware.

## Available

- Persistent G-code library, explicit dispatch, controller acknowledgments,
  pause/resume on capable workers, cancellation, and failure reporting.
- Simulator, GRBL serial and TCP transports, and distributed workers using
  sessions and single-execution leases. Active jobs interrupted by server restart
  or loss of worker communication are not automatically replayed.
- Declared GRBL machine profiles, saved rectangle and multi-path vector projects,
  and separate queued G-code generation for review and explicit dispatch. The
  editor supports vector cutting and engraving and a bounded static SVG importer
  for line/Bezier paths, basic shapes, element transforms, multi-shape
  alignment, editable monoline text engraving, bounded nested SVG groups,
  bounded SVG `<text>` import, and in-editor undo/redo. Arc commands and raster
  engraving remain future work.
- Uploaded-job review against a selected machine profile, including upload-time
  line validation, known or unknown path bounds, unsupported/incomplete-motion
  warnings, a static envelope preview, and an explicit override before dispatch
  when bounds are unknown or outside the profile.
- Bounded linear XY toolpath visualization for supported programs. Its position
  correlation can be estimated, ambiguous, or unavailable; it is not proof of
  motion or laser output.
- Trusted process plugins for preflight checks. They are not sandboxes, machine
  drivers, or file converters. Ruida, proprietary DSP protocols, and OctoPrint
  plugin compatibility remain unsupported; plugins can add adapters later.
- Hashed credentials with viewer, operator, admin, and per-device worker roles.
  Worker credentials are scoped to driver IDs and can be revoked individually.
- Large-library job storage with per-job program files, paginated job listing
  filtered by state, name, and driver, and a durable per-job event history with
  operator, worker, and server sources and bounded retention.
- Read-only controller settings verification (`$$`) for profile-generated jobs,
  with `$32=0`/`M3` warnings for uploaded jobs.
- Per-job start and cancel confirmation naming the job and driver.
- Idle workers reconnect after server or network failures; all-in-one mode keeps
  serving HTTP when its local worker fails.

## Not available or not qualified

- Physical controller, cutter, enclosure, or emergency-stop qualification.
- Complete coordinate interpretation for every uploaded program. Arcs,
  incremental or machine-coordinate moves, offset writes, and programs whose
  first move could fire the laser from an unknown head position are reported as
  unknown rather than guessed, so a
  review may still require an explicit bounds override.
- Arbitrary path editing, raster engraving,
  and automatic design-to-machine configuration.
- Universal Wi-Fi, proprietary DSP/Ruida controllers, or OctoPrint plugin
  compatibility.
- RTSP and worker-local V4L2 camera sources. Current camera monitoring accepts
  an administrator-configured HTTP JPEG/MJPEG endpoint and proxies one bounded
  frame per second through EmberDeck authentication.

## Feature request planning

Review the current open GitHub feature requests and their acceptance criteria
before choosing the next work slice. Issues [#3](https://github.com/constructorfleet/emberdeck/issues/3)
and [#4](https://github.com/constructorfleet/emberdeck/issues/4) are required
parts of the active product goal. Both are closed against their stated scope:
The #3 feasibility investigation is complete, while its firmware path remains
unimplemented and unqualified; #4's designer workflow and illustrated-guide
acceptance criteria are complete. Raster engraving remains a separate,
unsupported milestone.

- [x] [#3: ESP32 driver firmware and ESPHome feasibility](https://github.com/constructorfleet/emberdeck/issues/3).
  Compare standalone firmware, an ESPHome component, and existing bridges;
  decide the architecture, UART/controller boundary, resource/security/update
  constraints, failure behavior, licensing, supported-board candidates, and
  hardware qualification plan. If viable, define prototype and implementation
  milestones. [Feasibility decision](research/esp32-feasibility.md): conditionally
  viable as an ESP-IDF worker bridging an existing GRBL controller. Firmware
  support remains unimplemented and unqualified.
- [x] [#57: Chunked, digest-verified job transfer for constrained workers](https://github.com/constructorfleet/emberdeck/issues/57).
  The server and Go worker negotiate protocol 2 for immutable, 4 MiB-bounded
  programs transferred in lease-bound 64 KiB ranges; the worker verifies the
  complete size, SHA-256, and G-code before opening the controller. Protocol 1
  whole-program assignments remain compatible. The portable C worker stages
  the program in bounded ranges, verifies size, framing, SHA-256, and G-code,
  then permits one UART send through an immutable verified handle. Its
  authenticated HTTPS adapter creates fresh 128-bit sessions, negotiates the
  bounded protocol-2 assignment, validates lease-bound range responses, and
  checks active and terminal receipts. A durable NVS outbox retries identical
  terminal receipts across device restarts; the server retains a receipt digest
  across job deletion so a lost acknowledgment remains recoverable across
  server restarts. Fake HTTP/UART tests cover fresh sessions, leases, progress,
  malformed or truncated content, digest rejection, no output before
  verification, duplicate/lost receipts, and receipt recovery. A server plus
  Go-worker integration test runs a large designer-generated job through the
  compact protocol-2 response, while protocol-1 assignments remain unchanged.
  CI compiles and links the component against ESP-IDF 5.3.2 for ESP32. The
  polling cadence remains callback-driven rather than timer-enforced, and the
  CI build does not exercise Wi-Fi or networking. The component has no Wi-Fi
  provisioning or controller-board integration yet; physical firmware/board
  qualification remains under #3. Host and ESP-IDF compile checks do not
  qualify hardware.
- [x] [#4: Browser-based laser job designer](https://github.com/constructorfleet/emberdeck/issues/4).
  Review SketchForge-3D interaction patterns and licensing; specify the laser
  designer architecture and formats; implement design creation/import, editing,
  operations, profile-aware preview, saved editable projects, generated jobs,
  and explicit dispatch. Verify geometry/toolpaths at public interfaces and the
  complete browser workflow, and add screenshots from the implemented designer.
  Distinguish vector cutting, vector engraving, and raster engraving milestones.
  Implemented end-to-end milestones support the original saved rectangle and a
  schema-v2 multi-path editor with rectangle/ellipse/line primitives, ordered
  vector engrave/cut layers, profile preview, immutable generated jobs, and
  explicit dispatch. Single-object translation, X/Y scaling, and rotation are
  saved, previewed, checked against the work area, and applied to generated
  toolpaths. Keyboard-accessible multi-selection aligns left/center/right and
  top/middle/bottom using transformed bounds, saves the resulting translations,
  and preserves profile warnings. [Source review and remaining architecture](research/designer-architecture.md)
  track arc commands, arbitrary path editing, and raster evaluation. A bounded
  static SVG importer converts basic shapes and line/Bezier paths into editable,
  unassigned geometry. Nested `<g>` transforms compose with child transforms;
  groups allow only `id` and `transform` and can nest up to 16 levels. Static
  `<text>` imports use `emberdeck-stick-v1`, a unitless font size of 1–100, and
  at most 32 ASCII characters from A–Z, a–z, digits, space, and
  `. , : ; ! ? + - / _ ( )`; output is open unassigned strokes. Unsupported
  attributes, styles, other fonts, and nested text content such as `<tspan>` are
  rejected. Imported geometry carries no operation or machine settings. Native text objects also
  preserve editable content/font/size/origin and use deterministic generated
  stroke geometry for preview and G-code. The fixed hand-authored
  `emberdeck-stick-v1` font supports ASCII A–Z/a–z, digits, space, and
  `. , : ; ! ? + - / _ ( )`; text is limited to 1–32 characters and 1–100 mm
  high and can only be engraved. Raster engraving remains unsupported. The
  [designer architecture review](research/designer-architecture.md) documents
  the remaining scope and limits; raster engraving is a separate milestone.
- [ ] [#7: Live completed/remaining toolpath progress](https://github.com/constructorfleet/emberdeck/issues/7).
  Implement camera-independent execution monitoring for supported uploaded G-code
  as well as designer-generated jobs. Share the coordinate/toolpath renderer
  with #4, but keep this usable without the designer. Define required controller
  feedback and correlation; acknowledged/buffered commands are not completed
  motion. Show completed, active/head position, remaining, laser-off travel, and
  bounds with an accessible legend. Label estimated, ambiguous, repeated-path,
  interrupted, or unknown execution honestly. Verify wire correlation through
  pause/resume/disconnect and the browser workflow, then regenerate guide images.
  The initial implementation now parses bounded linear XY G-code, renders a
  camera-free completed/active/remaining view, and correlates GRBL work position
  separately from acknowledged lines. The issue remains open for broader motion
  coverage, machine-bound visualization, and additional interruption/controller
  validation.
- [x] [#54: Profile-aware bounds review for uploaded G-code](https://github.com/constructorfleet/emberdeck/issues/54).
  Upload validates driver line limits (422 with the source line), stores a
  selected profile revision, reports known bounds and unsupported/incomplete
  motion (arcs, `G91`, `G53`, `G28`/`G30`, `G10`), renders a static
  profile-envelope preview, and requires an explicit override when uploaded-job
  bounds are unknown or outside the profile. A laser-off first positioning move
  no longer hides a fully known program, so in-bounds uploads start without an
  override; a first move that could fire the laser stays unknown.
- [ ] [#52: Machine-preparation controls](https://github.com/constructorfleet/emberdeck/issues/52).
  Direct GRBL workers now provide confirmed home/unlock, bounded jog, X/Y zero,
  and laser-off frame actions through an exclusive manual lease. Wire and worker
  tests cover `$H`, `$X`, `$J=`, jog cancel `0x85`, `G10 L20`, `M5`, frame motion,
  no job G-code in manual polls, and fail-closed stop receipts. The browser gates
  each action by its exact capability and controller state and requires both a
  preparation check and an action confirmation; the operator guide includes
  screenshots and documents homing-reset position loss. Adapter `manual_*`
  fields from #135 remain declarations only: the worker does not expose them as
  controls or execute adapter manual assignments. Physical stop/hold latency,
  board qualification, and hardware safety remain unverified; #59 tracks that
  evidence. Keep #52 in the open feature plan until that hardware behavior is
  qualified.
- [x] [#8: Categorized documentation navigation and UX cleanup](https://github.com/constructorfleet/emberdeck/issues/8).
  Review the deployed documentation and record a content grouping; implement
  categorized navigation and presentation improvements across the site while
  preserving existing page URLs and generated references. Keep common operator
  tasks easy to find, show hierarchy and current page, and make narrow-screen
  menus accessible to keyboard, touch, and assistive technology. Verify desktop
  and mobile layouts, focus and menu semantics, links, images, and rendered
  release docs; capture before/after screenshots. The shared renderer and
  rendered-browser tests implement this navigation; see the
  [navigation browser test](browser-tests.md#documentation-navigation-test).
  Verify the published release before closing the GitHub issue.
- [ ] [#15: Driver-adapter plugins for network cutters](https://github.com/constructorfleet/emberdeck/issues/15).
  Define and implement a versioned adapter lifecycle, discovery/configuration,
  capabilities, transfer/status/control feedback, and bounded failures while
  preserving sessions, leases, confirmation, and no replay. Support adapters on
  server or distributed hosts, provide a simulated example and a concrete
  non-built-in network protocol target, and verify the operator workflow.
  Built-in GRBL-over-TCP already supports compatible network endpoints; this
  request does not imply universal Wi-Fi compatibility or qualified hardware.
- [ ] [#16: Importer/converter plugins](https://github.com/constructorfleet/emberdeck/issues/16).
  Add a distinct versioned import/conversion contract with format capabilities,
  options, editable-design or validated-job outputs, original-source retention,
  version/settings provenance, warnings, preview, and explicit operator review.
  Share the designer model with #4. Provide a bounded SVG example and tests for
  malformed, oversized, unsupported, lossy, and external/active-content cases.
  Trusted plugin processes are not sandboxes; input conversion never starts a
  machine or silently chooses units, origin, speed, power, or controller settings.
  The initial in-process contract and SVG adapter are documented in
  [Importer interface](api.md#importer-interface-go-api); runtime third-party loading and persisted
  provenance remain open.

The current issue bodies remain authoritative when their acceptance criteria
change. A roadmap checkmark requires corresponding evidence, not a planned task.
Capabilities may change between releases. The installed version and the
operator-facing documentation shipped with it describe what is currently
available; unsupported items above are not implied features.
