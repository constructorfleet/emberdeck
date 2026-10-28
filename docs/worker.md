# Worker control behavior

`worker.Run` creates one session and polls the driver's endpoint with the
`pause_resume` capability on every request. It keeps one controller connection
for the assigned lease and sends progress, controller state, and command
responses through the same poll endpoint.

The worker applies `pause` and `resume` commands to the active GRBL stream. A
paused assignment is connected and preflighted without sending source lines;
the stream remains gated until resume is confirmed by a fresh controller
status. During a pause, the worker continues reporting once per second so the
server lease remains live. The `controller_state` field reports the most
recent GRBL state (`Idle`, `Run`, `Hold:1`, or `Hold:0`). The worker records the driver's applied-control receipt before accepting a
new controller status as confirmation. The job state becomes
`paused` only after the worker observes `Hold:0` or `Idle` after the pause
command, and becomes `running` after a fresh `Run` or `Idle` following resume.

Cancel stops the same stream and reports a canceled receipt. A worker does not
reconnect or replay an interrupted lease. Communication failures stop the
stream and fail closed; inspect the machine before restarting a worker after a
failure.
