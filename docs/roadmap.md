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
  alignment, editable monoline text engraving, bounded nested SVG groups, and
  bounded SVG `<text>` import. Arc commands, undo/redo, and raster engraving
  remain future work.
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
  incremental or machine-coordinate moves, offset writes, and programs without a
  known initial head position are reported as unknown rather than guessed, so a
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
- [ ] [#57: Chunked, digest-verified job transfer for constrained workers](https://github.com/constructorfleet/emberdeck/issues/57).
  The server and Go worker negotiate protocol 2 for immutable, 4 MiB-bounded
  programs transferred in lease-bound 64 KiB ranges; the worker verifies the
  complete size, SHA-256, and G-code before opening the controller. Protocol 1
  whole-program assignments remain compatible. A portable C host prototype
  adds bounded staging, size/framing/digest/G-code validation, immutable
  verified storage, and a UART output gate that emits no bytes before
  verification. Its injected controller adapter can poll the same session,
  job, and lease while streaming; host cycle tests cover repeated monotonic
  progress, cancel, failed-poll receipts with controller-state preservation,
  lease binding, and terminal gating.
  The callback cadence is not timer-enforced, and these tests do not implement
  a real HTTP client or demonstrate Go-worker compatibility through an
  integrated fake HTTP/UART flow. Remaining work includes real session and
  credential adapters, measured heartbeat cadence, lost/duplicate receipt
  handling, integrated Go compatibility tests, ESP-IDF firmware, and physical
  firmware/board qualification under #3. The C prototype is not deployable
  firmware and does not qualify hardware.
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
- [ ] [#54: Profile-aware bounds review for uploaded G-code](https://github.com/constructorfleet/emberdeck/issues/54).
  The first slice now validates driver line limits during upload, stores a
  selected profile revision, reports known bounds and unsupported/incomplete
  motion, renders a static profile-envelope preview, and requires an explicit
  override when uploaded-job bounds are unknown or outside the profile. Keep the
  issue open for broader parser coverage and a review state that can establish
  fully known in-bounds programs without guessing an initial head position.
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

The current issue bodies remain authoritative when their acceptance criteria
change. A roadmap checkmark requires corresponding evidence, not a planned task.
Capabilities may change between releases. The installed version and the
operator-facing documentation shipped with it describe what is currently
available; unsupported items above are not implied features.
