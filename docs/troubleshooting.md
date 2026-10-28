# Troubleshooting

## Server or worker shows no useful logs

Logs go to standard error. Read them with `docker compose logs -f emberdeck`,
`docker logs <container>`, or `journalctl -u emberdeck -f`. The default `info`
level omits successful reads and worker polls; set `EMBERDECK_LOG_LEVEL=debug`
(or `-log-level debug`) to see every request, and `EMBERDECK_LOG_FORMAT=json`
for a log collector. See [Deployment](deployment.md#logs) for what each level
records.

## Driver is not listed

The console lists drivers that have registered and are currently online. Check
that the worker process is running, its `-server` URL reaches the intended
server, and its `-id` is the driver you selected. The browser and worker must use
the server's current API key. For a serial worker, confirm the selected device
and access permissions; `emberdeck driver -list-ports` lists OS-reported ports.
Inspect the worker log for connection or serial-open errors. See
[Deployment](deployment.md#distributed-worker) for worker setup.

## HTTP 409: driver ID already owned by another session

A worker process has a unique session. After a process restart, the server can
temporarily retain ownership for up to 10 seconds after its last heartbeat. An
idle worker retries with a fresh session; allow it to retry. Do not start a
second live worker with the same `-id`: one worker will continue to encounter
conflicts. Give each active worker a unique ID. If the conflict continues, check
that another process or host is not still using that ID. See
[worker reconnection](worker.md#reconnection).

## Job is interrupted after a restart or disconnection

Active jobs are marked interrupted when the server restarts or the worker loses
its lease. EmberDeck does not automatically resume or replay them. Inspect the
machine and controller state before taking any further action. If another run is
appropriate, review and dispatch a new job explicitly; do not treat the
interrupted job as a safe restart point. See
[Safety & Limitations](safety.md#interrupted-work-is-not-replayed).

## Serial permission denied

Check the device name with `emberdeck driver -list-ports`, then confirm that the
account running the worker can open it. On Linux the device group is commonly
`dialout`; after changing membership, restart the login session or service. For
Docker, pass the device through explicitly and provide the needed group
permission. Also ensure another program is not holding the port. See
[serial setup in Deployment](deployment.md#serial-workers-and-all-in-one-hardware-mode).

## API key is rejected

An `authentication required` response means the supplied key was not accepted.
The first start in a data directory without `credentials.json` migrates
`EMBERDECK_API_KEY` to a hashed `bootstrap-admin` credential; after that the
server accepts only active stored credentials, so changing the environment
value does not rotate a key. Check that the browser or worker uses a credential
created through the admin API, that it has not been revoked, and that a worker
credential's `driver_ids` includes its `-id`. In the browser, use **Change key**
to enter the correct value; the console validates it before saving it for that
tab session. Keep the bootstrap admin key on the server side and give each
remote worker its own credential. See the
[credential contract](api.md#credential-roles-and-lifecycle) and the
[Operator Guide](operator-guide.md#connect-and-choose-a-driver).
