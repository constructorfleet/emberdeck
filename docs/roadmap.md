# Delivery plan

Implemented capabilities still require release CI and hardware qualification
where indicated. This is an early foundation, not a production certification.

- [x] Portable Go project and protected PR workflow
- [x] Persistent authenticated job library and validation
- [x] Browser upload, view, dispatch, progress, cancellation, and deletion
- [x] Simulator through the controller wire boundary
- [x] Distributed workers with sessions, leases, and fail-closed interruption
- [x] GRBL acknowledgment streaming and Idle completion checks
- [x] Serial and TCP transports (physical hardware unqualified)
- [x] Versioned process preflight plugins and example plugin
- [x] Native, Docker, Pi/systemd, and LXC deployment instructions
- [x] Release automation for binaries, generated docs, and container images
- [x] Verify hosted cross-platform/container checks and v0.1.0 release deployment
- [x] Pause/resume with explicit controller state reporting
- [ ] Machine profiles, work-envelope validation, and coordinate preview
- [ ] Job history metadata, filtering, pagination, and large-library storage
- [ ] Separate operator/worker credentials and multi-user permissions
- [ ] Camera monitoring with authenticated access
- [ ] Supported-controller hardware qualification matrix and test evidence
- [ ] Installer/package validation on actual Pi, Windows, Mac, Linux, and LXC

Only the GRBL protocol is implemented. Ruida, proprietary DSP protocols, image
rasterization, CAD/CAM toolpath generation, and OctoPrint plugin compatibility
are outside the current implementation. Plugins can add adapters in later work.

## Feature requests

Review open GitHub feature requests before selecting new work, then read the
current issue body and comments for acceptance criteria. Issues #3 and #4 are
required parts of the active product goal alongside the core deployment and
job-management requirements above. Issues #7 and #8 are tracked below with
their implementation status and evidence:

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
  [Source review and proposed architecture](research/designer-architecture.md)
  cover the planning subset; the end-to-end implementation remains required.
- [ ] [#7: Live completed/remaining toolpath progress](https://github.com/constructorfleet/emberdeck/issues/7).
  Implement camera-independent execution monitoring for supported uploaded G-code
  as well as designer-generated jobs. Share the coordinate/toolpath renderer
  with #4, but keep this usable without the designer. Define required controller
  feedback and correlation; acknowledged/buffered commands are not completed
  motion. Show completed, active/head position, remaining, laser-off travel, and
  bounds with an accessible legend. Label estimated, ambiguous, repeated-path,
  interrupted, or unknown execution honestly. Verify wire correlation through
  pause/resume/disconnect and the browser workflow, then regenerate guide images.
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

The current issue bodies remain authoritative when their acceptance criteria
change. A roadmap checkmark requires corresponding evidence, not a planned task.
