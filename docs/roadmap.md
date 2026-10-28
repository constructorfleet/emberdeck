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
  and separate queued G-code generation for review and explicit dispatch.
- Uploaded-job review against a selected machine profile, including upload-time
  line validation, known or unknown path bounds, unsupported/incomplete-motion
  warnings, a static envelope preview, and an explicit override before dispatch
  when bounds are unknown or outside the profile.
- Bounded linear XY toolpath visualization for supported programs. Its position
  correlation can be estimated, ambiguous, or unavailable; it is not proof of
  motion or laser output.
- Trusted process plugins for preflight checks. They are not sandboxes, machine
  drivers, or file converters.

## Not available or not qualified

- Physical controller, cutter, enclosure, or emergency-stop qualification.
- Complete coordinate interpretation for every uploaded program. Arcs,
  incremental or machine-coordinate moves, offset writes, and programs without a
  known initial head position are reported as unknown rather than guessed, so a
  review may still require an explicit bounds override.
- SVG/text import, arbitrary path editing, rotation/alignment, raster engraving,
  and automatic design-to-machine configuration.
- Universal Wi-Fi, proprietary DSP/Ruida controllers, or OctoPrint plugin
  compatibility.
- Multi-user permissions, camera monitoring, and advanced job-history
  filtering/pagination.

Capabilities may change between releases. The installed version and the
operator-facing documentation shipped with it describe what is currently
available; unsupported items above are not implied features.
