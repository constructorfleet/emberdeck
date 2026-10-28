# Install

The current local-demo path builds from source with Go 1.27. It runs an
explicitly software-only simulator; it does not install or qualify a physical
machine controller.

From an EmberDeck source checkout, build the executable and start the server:

```sh
go build -o bin/emberdeck ./cmd/emberdeck
export EMBERDECK_API_KEY="$(openssl rand -hex 32)"
printf 'API key: %s\n' "$EMBERDECK_API_KEY"
bin/emberdeck serve -simulate -data ./data
```

Keep the displayed key private. Open `http://127.0.0.1:8080` in your browser and
enter that key to connect. The simulator is software only; it sends no commands
to a physical cutter.

To try a program in the simulator, upload plain-text G-code such as:

```gcode
G21
G90
G1 X10 Y10 F100
M5
```

Continue with the [Operator Guide](operator-guide.md) for the console workflow.
For serial access and other deployment modes, see [Deployment](deployment.md).
The [Troubleshooting](troubleshooting.md), [Glossary](glossary.md), and
[Safety & Limitations](safety.md) pages explain common problems and boundaries.
