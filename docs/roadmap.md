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

## Required feature requests

Review open GitHub feature requests before selecting new work, then read the
current issue body and comments for acceptance criteria. The following requests
are part of the active goal, alongside the core deployment and job-management
requirements above:

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

The current issue bodies remain authoritative when their acceptance criteria
change. A roadmap checkmark requires corresponding evidence, not a planned task.
