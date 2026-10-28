# Browser console tests

The Playwright suite runs the embedded console against a real EmberDeck HTTP
server with its GRBL simulator. It covers API key connection, G-code upload,
the safety confirmation gate, live progress, program viewing, completion,
controller acknowledged pause and resume, cancellation while paused, and deletion.
The pause test observes the simulator's `Hold:0` acknowledgement across repeated
job polls, confirms progress stays fixed while held, and observes `Run` after
resume before checking completion. It does not connect to or qualify physical
hardware.

## Run locally

Install the pinned Node dependencies and Chromium, then run the suite:

```sh
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
