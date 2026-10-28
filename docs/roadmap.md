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
- [ ] Verify hosted cross-platform/container checks and first release deployment
- [ ] Pause/resume with explicit controller state reporting
- [ ] Machine profiles, work-envelope validation, and coordinate preview
- [ ] Job history metadata, filtering, pagination, and large-library storage
- [ ] Separate operator/worker credentials and multi-user permissions
- [ ] Camera monitoring with authenticated access
- [ ] Supported-controller hardware qualification matrix and test evidence
- [ ] Installer/package validation on actual Pi, Windows, Mac, Linux, and LXC

Only the GRBL protocol is implemented. Ruida, proprietary DSP protocols, image
rasterization, CAD/CAM toolpath generation, and OctoPrint plugin compatibility
are outside the current implementation. Plugins can add adapters in later work.
