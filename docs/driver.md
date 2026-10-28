# GRBL driver package

`driver.Validate(program)` checks the full program and returns its executable line count. `driver.Stream(ctx, conn, program, onProgress)` streams a complete G-code program over an `io.ReadWriteCloser`. It sends one line at a time, waits for the corresponding `ok` or `error:` response, reports acknowledged line counts, and sends and acknowledges an injected `M5` cleanup command, then polls GRBL status until it sees `<Idle...>`. Progress counts uploaded executable lines; the cleanup command is excluded. The caller owns and should close the connection after the call.

The program is validated in full before any bytes are sent. Input must be ASCII; GRBL realtime characters (`!`, `?`, `~`, Ctrl-X) in executable text are rejected. Semicolon and parenthetical comments are stripped safely; nested or unterminated parenthetical comments are rejected. Lines are trimmed, blank lines are skipped, CRLF is accepted, and executable lines may contain at most 79 bytes, matching GRBL's 80-byte line buffer including its terminator.

A controller error or unexpected response returns an error. Missing acknowledgements and missing status reports time out after 30 seconds. The status poll interval is 200 ms (5 Hz). The initial status handshake ignores a GRBL welcome banner if the serial device emits one, but fails closed on busy, alarm, or other non-Idle states. It never resumes or unlocks the controller. On context cancellation the driver makes a best-effort feed-hold and soft-reset write for at most 100 ms, then closes the connection to interrupt pending I/O. This is a software request only; it is not an emergency stop and does not guarantee that motion has stopped. Keep physical emergency stops and enclosure interlocks independent.

The wire boundary is `io.ReadWriteCloser`, so a serial adapter and simulator can provide the same interface. `driver.NewSimulator()` returns a deterministic in-memory endpoint that acknowledges commands and supports status, feed hold, resume, and reset for development and tests. It does not model physical motion. The package does not open a serial port, manage job persistence, or automatically resume interrupted work. Tests exercise this boundary with both `net.Pipe` and the simulator.

Protocol basis: [gnea/grbl interface documentation](https://github.com/gnea/grbl/blob/master/doc/markdown/interface.md) and [realtime commands](https://github.com/gnea/grbl/blob/master/doc/markdown/commands.md).

## Worker package

`worker.Run(ctx, serverURL, key, driverID, connector)` owns a unique session, polls for assignments, streams one assigned lease through the connector, and reports progress and terminal state. Poll failures fail closed and cancel an active stream. Terminal report retries reuse the same job ID and lease, so a lost HTTP response cannot cause the job to be resent to the controller. The worker package does not start physical drivers by itself; callers provide a connector for the intended wire endpoint.
