# Product status

EmberDeck is an early-stage job manager, not a certified machine controller.
Simulator and software checks do not qualify a physical machine. See
[Safety & Limitations](safety.md) before operating hardware.

## Available

- Persistent G-code library, explicit dispatch, controller acknowledgements,
  pause/resume on capable workers, cancellation, and failure reporting.
- Simulator, GRBL serial and TCP transports, and distributed workers using
  sessions and single-execution leases. Active jobs interrupted by server restart
  or loss of worker communication are not automatically replayed.
- Declared GRBL machine profiles, saved rectangle and multi-path vector projects,
  and separate queued G-code generation for review and explicit dispatch. The
  editor supports vector cutting and engraving and a bounded static SVG importer
  for line/Bezier paths, basic shapes, and element transforms. Nested groups,
  smooth/arc commands, text import, editor transforms, undo/redo, and raster
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

## Not available or not qualified

- Physical controller, cutter, enclosure, or emergency-stop qualification.
- Complete coordinate interpretation for every uploaded program. Arcs,
  incremental or machine-coordinate moves, offset writes, and programs without a
  known initial head position are reported as unknown rather than guessed, so a
  review may still require an explicit bounds override.
- SVG text import, arbitrary path editing, rotation/alignment, raster engraving,
  and automatic design-to-machine configuration.
- Universal Wi-Fi, proprietary DSP/Ruida controllers, or OctoPrint plugin
  compatibility.
- Multi-user permissions, camera monitoring, and advanced job-history
  filtering/pagination.

## Feature request planning

Review the current open GitHub feature requests and their acceptance criteria
before choosing the next work slice. Issues [#3](https://github.com/constructorfleet/emberdeck/issues/3)
and [#4](https://github.com/constructorfleet/emberdeck/issues/4) remain part of
the active product goal: #3's feasibility investigation is complete, while its
firmware path remains unimplemented and unqualified; #4 remains open until the
designer's remaining capabilities are delivered.

- [x] [#3: ESP32 driver firmware and ESPHome feasibility](https://github.com/constructorfleet/emberdeck/issues/3).
  Compare standalone firmware, an ESPHome component, and existing bridges;
  decide the architecture, UART/controller boundary, resource/security/update
  constraints, failure behavior, licensing, supported-board candidates, and
  hardware qualification plan. If viable, define prototype and implementation
  milestones. [Feasibility decision](research/esp32-feasibility.md): conditionally
  viable as an ESP-IDF worker bridging an existing GRBL controller. Firmware
  support remains unimplemented and unqualified.
- [ ] [#4: Browser-based laser job designer](https://github.com/constructorfleet/emberdeck/issues/4).
  Review SketchForge-3D interaction patterns and licensing; specify the laser
  designer architecture and formats; implement design creation/import, editing,
  operations, profile-aware preview, saved editable projects, generated jobs,
  and explicit dispatch. Verify geometry/toolpaths at public interfaces and the
  complete browser workflow, and add screenshots from the implemented designer.
  Distinguish vector cutting, vector engraving, and raster engraving milestones.
  Implemented end-to-end milestones support the original saved rectangle and a
  schema-v2 multi-path editor with rectangle/ellipse/line primitives, ordered
  vector engrave/cut layers, profile preview, immutable generated jobs, and
  explicit dispatch. [Source review and remaining architecture](research/designer-architecture.md)
  track the remaining rotation/alignment, multi-selection/transforms, nested
  SVG groups, smooth/arc commands, text import, arbitrary path editing, and raster
  evaluation. A bounded static SVG importer now converts basic shapes and
  line/Bezier paths into editable, unassigned geometry. The full issue stays open.
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
  [review and before/after captures](browser-tests.md#documentation-navigation-review).
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
