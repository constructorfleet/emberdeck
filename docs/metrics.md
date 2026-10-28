# Prometheus metrics

EmberDeck can expose a Prometheus text-format scrape endpoint at `/metrics` on a
**separate HTTP listener**. It is disabled by default. The exporter is per server
instance and reports the server's view of its durable job library and worker
heartbeats; distributed workers do not expose separate exporters.

Metrics reflect state observed by EmberDeck, including worker-reported controller
feedback. They are not evidence of physical laser output, material processing,
or machine safety. Controller acceptance and reported completion are not physical
verification.

## Contract

| Metric | Type | Unit | Labels | Meaning |
| --- | --- | --- | --- | --- |
| `emberdeck_build_info` | Gauge | 1 | `version` | Always 1 for the running build. |
| `emberdeck_http_requests_total` | Counter | requests | `method`, `code` | Requests handled by the main API/console listener. `method` is `GET`, `POST`, `PUT`, `DELETE`, `HEAD`, or `OTHER`; `code` is a three-digit HTTP status. |
| `emberdeck_http_request_duration_seconds` | Histogram | seconds | `method` | Main-listener request latency, with the same bounded method values. Uses Prometheus default histogram buckets. |
| `emberdeck_jobs` | Gauge | jobs | `state` | Current durable job count for each supported state: `queued`, `running`, `pausing`, `paused`, `resuming`, `canceling`, `completed`, `failed`, `canceled`, `interrupted`. Zero-valued states are included. |
| `emberdeck_job_state_transitions_total` | Counter | transitions | `from`, `to` | Successful durable state changes during this process. Newly created jobs use `from="new"`. Deleting a job is not a state transition. |
| `emberdeck_drivers_online` | Gauge | drivers | none | Drivers whose last heartbeat is within the server's 10-second expiration interval. |
| `emberdeck_drivers_busy` | Gauge | drivers | none | Online drivers with active assigned work. |
| `emberdeck_oldest_driver_heartbeat_age_seconds` | Gauge | seconds | none | Age of the oldest heartbeat among online drivers; zero if none are online. |
| `emberdeck_driver_expirations_total` | Counter | sessions | none | Driver sessions removed by the normal server expiration path. A scrape is read-only; this counter advances when a subsequent server operation processes an expired session. |
| `emberdeck_plugin_invocations_total` | Counter | invocations | `outcome` | Configured preflight plugin invocations. Outcomes are `ok`, `error`, or `rejected`. The family is absent until a plugin runs. |
| `emberdeck_plugin_invocation_duration_seconds` | Histogram | seconds | `outcome` | Duration of each configured preflight plugin invocation; uses Prometheus default buckets. |
| `go_*` | Go runtime collectors | varies | collector-defined | Go runtime health from the Prometheus Go collector. |
| `process_*` | Process collectors | varies | collector-defined | Process health from the Prometheus process collector. |

Job gauges are reconstructed from the durable library at startup, including
active work converted to `interrupted` during restart recovery. Transition and
expiration counters are process-lifetime counters and reset when the server
restarts. HTTP counters and histograms are likewise process-lifetime. No metric
uses a job ID, driver ID, project or file name, path, G-code, credential, or raw
error as a label or sample value. The API listener's request metric labels
contain only the method allowlist and HTTP status code.

The initial exporter covers the supported server/API, job, worker-heartbeat, and
preflight-plugin lifecycle. It does not currently provide per-job duration,
connection/protocol failure, import/conversion, or physical machine telemetry
metrics; those extension points are not represented by the current server
behavior.

## Configuration and access control

`serve -metrics-listen ADDR` enables the listener; the default empty value keeps
it disabled and does not change normal server exposure. The endpoint is always
`/metrics`. When `EMBERDECK_METRICS_KEY` is non-empty, scrapes require
`Authorization: Bearer <key>`; missing or incorrect credentials receive 401.
When unset, any client that can reach the metrics listener can scrape it.
Set `EMBERDECK_METRICS_KEY_FILE` to a file containing the key to load it as a
Docker secret; a non-empty `EMBERDECK_METRICS_KEY` takes precedence, and one
trailing line ending is removed from the file value. An unreadable configured
file stops startup with an error.

Bind to loopback or a private monitoring network, restrict firewall/security
group access to Prometheus, and use a TLS proxy or trusted tunnel across an
untrusted network. Do not publish the metrics port to the public internet. The
metrics key is distinct from `EMBERDECK_API_KEY`; do not reuse the API key.

Native loopback example:

```sh
export EMBERDECK_API_KEY='replace-with-a-long-random-api-secret'
export EMBERDECK_METRICS_KEY='replace-with-a-different-long-random-secret'
emberdeck serve -listen 127.0.0.1:8080 -metrics-listen 127.0.0.1:9090 -data ./data
```

For systemd, place the optional metrics key in the protected environment file and
add `-metrics-listen 127.0.0.1:9090` to `ExecStart`; keep the service listener on
loopback unless network policy deliberately allows monitoring hosts. For Docker,
bind the listener to the container interface and publish it only on a loopback
host address or an isolated monitoring network (the repository Compose example
is commented out and does not expose it by default). For distributed workers,
scrape the central server: worker processes report heartbeat and execution state
to it, and do not expose their own metrics endpoint.

## Prometheus scrape configuration

The ready-to-use example is [`deploy/prometheus/emberdeck.yml`](../deploy/prometheus/emberdeck.yml).
It uses `bearer_token_file`; provision that file as a read-only secret containing
the value of `EMBERDECK_METRICS_KEY`. If the server intentionally has no metrics
key, remove the `bearer_token_file` setting and rely on network access controls.

```yaml
scrape_configs:
  - job_name: emberdeck
    metrics_path: /metrics
    static_configs:
      - targets: ["127.0.0.1:9090"]
    bearer_token_file: /etc/prometheus/secrets/emberdeck-metrics-key
```

## Alert examples

The complete rules are in [`deploy/prometheus/emberdeck.rules.yml`](../deploy/prometheus/emberdeck.rules.yml).
Tune durations and thresholds to the expected operating schedule. Offline-driver
alerting is appropriate only for installations expected to keep a worker online.

```yaml
groups:
  - name: emberdeck
    rules:
      - alert: EmberDeckNoDriversOnline
        expr: emberdeck_drivers_online == 0
        for: 2m
        labels: {severity: warning}
        annotations:
          summary: No EmberDeck drivers are online
      - alert: EmberDeckJobFailureRateHigh
        expr: |
          sum(rate(emberdeck_job_state_transitions_total{to="failed"}[15m]))
          / clamp_min(sum(rate(emberdeck_job_state_transitions_total{to=~"completed|failed"}[15m])), 1e-9) > 0.2
        for: 5m
        labels: {severity: warning}
        annotations:
          summary: EmberDeck job failure rate is above 20%
      - alert: EmberDeckWorkInterrupted
        expr: sum(emberdeck_jobs{state="interrupted"}) > 0
        labels: {severity: critical}
        annotations:
          summary: EmberDeck has interrupted work that needs operator inspection
```

An interrupted job may indicate server restart or expired worker heartbeat.
Inspect the machine and job before creating new work; EmberDeck never replays
interrupted work automatically.
