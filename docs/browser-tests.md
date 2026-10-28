# Browser console tests

The Playwright suite runs the embedded console against a real EmberDeck HTTP
server with its GRBL simulator. It covers API key connection, G-code upload,
the safety confirmation gate, Start and Cancel confirmation dialogs that name
the job and driver, focusable unavailable-Start reasons, job-named row action
labels, live progress, program viewing, completion,
controller acknowledged pause and resume, cancellation while paused, and deletion.
The pause test observes the simulator's `Hold:0` acknowledgement across repeated
job polls and confirms progress stays fixed while held. After the explicit
Resume, it verifies acknowledged progress advances and accepts either a sampled
`Run` report or a completed job with the final `Idle` report; a short simulator
job can finish between UI polls. It does not connect to or qualify physical
hardware.

The rectangle designer workflow creates a declared GRBL profile and a saved
vector-engrave rectangle project through the browser. It verifies invalid bounds
block save and generation, profile revision changes require project review, and
projects persist across page reload. Generation adds a queued job and opens its
program and provenance for review without starting it. It refuses generation
while edits are unsaved and clears an already checked readiness box when a new
program is generated. The test then checks the existing readiness gate,
dispatches explicitly to the simulator, and verifies the job completes. It also
checks narrow-screen overflow with both editor forms open at tablet boundaries.
An open profile draft also blocks generation until it is saved or canceled.

The schema-v2 vector workflow creates rectangle, ellipse, and line paths, adds
and edits ordered engraving/cutting layers, assigns paths, validates the
profile-aware preview, and saves/reopens the project. It verifies that generation
creates a queued job with per-layer provenance but sends no start request, then
uses the existing confirmation/Start flow to complete on the simulator. A
generic multi-subpath imported path fixture verifies that resizing preserves
kind and all subpaths. Tests use the rendered UI and real HTTP API rather than
mocking storage.

## Run locally

Install the Python documentation dependency, pinned Node dependencies, and
Chromium, then run the suite:

```sh
python3 -m pip install -r docs/requirements.txt
npm ci
npx playwright install chromium
npm test
```

The test runner builds `bin/emberdeck` from current sources unless
`EMBERDECK_BINARY` supplies an explicit prebuilt binary. It starts
`emberdeck serve -simulate` with a temporary data directory and a test-only API
key, waits for the HTTP server, runs the Playwright tests, then stops the server
and removes the temporary data. No production data or API key is used.

The runner chooses an available local port automatically. Set
`EMBERDECK_E2E_PORT` to use a specific port. Set `EMBERDECK_BINARY` to use an
already-built binary at another path.

The CI workflow installs Chromium and its Linux dependencies, builds the CLI,
and runs `npm test`. On failure, it uploads Playwright screenshots, traces,
videos, and the HTML report as the `browser-test-failures` artifact.

## Documentation navigation test

The static site navigation is grouped by reader task:

- **Getting Started:** Overview, Install, Operator Guide.
- **Deploy & Run:** Deployment, Troubleshooting, Glossary, Safety & Limitations.
- **Develop & Extend:** Contributing, Architecture, Plugins, Browser Tests,
  Artifact Tests, Roadmap, Designer Architecture, ESP32 Feasibility, GRBL Driver
  Package, Worker Package, and Serial Transport Package.
- **Reference:** API, API Routes, and CLI.

`e2e/docs-navigation.spec.js` builds and opens the public rendered HTML. It
checks each page for one current link, all four groups and their links, working
local page links and images, desktop/mobile overflow, keyboard access, visible
focus, and touch activation. It also covers generated API Routes and CLI pages.
