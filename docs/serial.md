# Serial transport

`transport.OpenSerial(ctx, portName, baud)` opens an OS serial device as 8-N-1
and returns an `io.ReadWriteCloser` suitable for the existing controller
driver. `transport.ListSerialPorts()` returns the names reported by the current
operating system. Pass the opened connection to `driver.Stream`; its context
cancellation closes the connection, and the transport also closes it when the
context passed to `OpenSerial` is canceled.

The implementation uses [`go.bug.st/serial`](https://pkg.go.dev/go.bug.st/serial),
whose API exposes cross-platform serial open, read/write/close, read timeouts,
and port enumeration. Port names and permissions are OS-specific. On Linux,
the service account may need membership in the distribution's serial-device
group (commonly `dialout`); log out/in or restart the service after changing
group membership. On macOS and Windows, select the port name reported by the OS
and ensure no other process owns it.

No physical controller has been tested or qualified with this transport yet.
Baud rate, reset behavior, USB bridge compatibility, grounding, motion
completion, enclosure interlocks, and emergency stops must be verified for the
specific machine setup before any physical operation. Software feed-hold or
reset requests are not emergency stops. The package does not automatically
resume interrupted programs.
