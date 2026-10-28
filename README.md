# Overview

**Your laser cutter's command deck.** Self-hosted job management with an embedded
browser console, distributed controller workers, and trusted process plugins.
One Go executable runs on Raspberry Pi, macOS, Linux, and Windows, or in Docker
and LXC. No Node runtime, frontend build, cloud account, or external database.

**Early development: simulator verified, physical hardware not yet qualified.**
GRBL 1.1 is the first protocol. Proprietary DSP controllers (including Ruida) are
not supported. EmberDeck is not a safety controller: supervise cutting and use
hardware interlocks and an emergency stop.

Start with [Install](docs/install.md) to build a local software-only demo, then
follow the [illustrated Operator Guide](docs/operator-guide.md) to use the console.
For deployment choices, see [Deployment](docs/deployment.md). Review
[Troubleshooting](docs/troubleshooting.md), the [Glossary](docs/glossary.md), and
[Safety & Limitations](docs/safety.md) before connecting a machine.

![EmberDeck operator console showing a controller-confirmed paused job](docs/assets/screenshots/operator-console-paused.png)

![Console overview with a declared machine profile, saved project, G-code intake, and job library](docs/assets/screenshots/designer-console-overview.png)

## Available now

- Durable upload, list, program view, deletion, explicit start, confirmed
  pause/resume, cancellation, separate line and spatial path progress,
  controller completion, and failure reporting.
- Upload-time driver line validation and profile-aware static path/bounds review.
  Unknown or out-of-bounds uploaded programs require a separate per-job override
  before dispatch; the review never certifies hardware safety.
- Live controller status distinguishes pause requests, deceleration, confirmed
  holds, and resumed execution. The same connection and lease span a pause.
- Declared GRBL profiles, saved rectangle projects, and schema-v2 multi-path
  vector projects with editable rectangle/ellipse/line paths, ordered engraving
  and cutting layers, bounded profile preview, and separate generated jobs for
  review and explicit dispatch. SVG/text import, transforms, raster engraving,
  and driver-adapter configuration remain
  future work; the [operator guide](docs/operator-guide.md) illustrates the
  available controls and calls out the missing UI surfaces.
- One active job per online driver; assignment leases and unique worker sessions.
- Fail-closed worker behavior on server communication failures. Restarted or
  disconnected work becomes interrupted, with no automatic replay.
- GRBL send/response over serial or TCP and the same wire-boundary simulator.
- Versioned process plugins that can veto preflight. No OctoPrint plugin ABI.
- Required PR checks across Linux, macOS, Windows, plus container smoke testing.
  Release Please maintains the pending release; releases build native binaries,
  documentation, and container images.

## Design a rectangle

Declare the prepared machine profile, edit and save a rectangle engraving
project, then generate a separate queued program for review. Generation clears
the previous readiness confirmation and never starts the machine. See the
[designer walkthrough](docs/operator-guide.md#declare-a-machine-profile-and-design-a-rectangle)
for the supported GRBL settings and coordinate assumptions.

![Saved rectangle engraving source and its declared work-area preview](docs/assets/screenshots/designer-project.png)

## What EmberDeck does

EmberDeck stores G-code jobs, dispatches them to a connected GRBL driver after
operator confirmation, and reports controller acknowledgements and available
execution telemetry. It can also save declared machine profiles and vector
design projects and generate separate queued jobs for review. It does not start
generated jobs automatically. See the [operator guide](docs/operator-guide.md)
for the implemented console workflow and [Safety & Limitations](docs/safety.md)
for important operating constraints.
