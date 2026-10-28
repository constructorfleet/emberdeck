# GRBL driver package

`driver.Stream(ctx, conn, program, onProgress)` streams a complete G-code program over an `io.ReadWriteCloser`. It sends one line at a time, waits for the corresponding `ok` or `error:` response, reports acknowledged line counts, and polls GRBL status after all acknowledgements until it sees `<Idle...>`. The caller owns and should close the connection after the call.

The program is validated in full before any bytes are sent. Input must be ASCII; embedded GRBL realtime characters (`!`, `?`, `~`, Ctrl-X) and comment delimiters (`(`, `)`, `;`) are rejected. Lines are trimmed, blank lines are skipped, CRLF is accepted, and lines may contain at most 79 bytes, matching GRBL's 80-byte line buffer including its terminator. This deliberately conservative policy avoids confusing inline comments or realtime controls with streamed commands.

A controller error or unexpected response returns an error. Missing acknowledgements and missing status reports time out after 30 seconds. The status poll interval is 200 ms (5 Hz). On context cancellation the driver makes a best-effort feed-hold and soft-reset write, then closes the connection to interrupt pending I/O. This is a software request only; it is not an emergency stop and does not guarantee that motion has stopped. Keep physical emergency stops and enclosure interlocks independent.

The wire boundary is `io.ReadWriteCloser`, so a serial adapter and a simulator can both provide the same interface. The package does not open a serial port, manage job persistence, or automatically resume interrupted work. Tests exercise this boundary with `net.Pipe` and a simulated GRBL peer.

Protocol basis: [gnea/grbl interface documentation](https://github.com/gnea/grbl/blob/master/doc/markdown/interface.md) and [realtime commands](https://github.com/gnea/grbl/blob/master/doc/markdown/commands.md).
