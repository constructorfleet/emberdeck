# Serial transport

`transport.OpenSerial(ctx, portName, baud)` opens an OS serial device as 8-N-1
and returns an `io.ReadWriteCloser` suitable for the existing controller
driver. It explicitly requests DTR off and RTS off. `transport.OpenSerialWithBits`
accepts a `transport.ModemBits{DTR: ..., RTS: ...}` policy. `transport.ListSerialPorts()`
returns the names reported by the current operating system. Pass the opened
connection to `driver.Stream`; its context cancellation closes the connection,
and the transport also closes it when the context passed to `OpenSerial` is
canceled. `OpenSerialWith` retains its original opener seam and off/off default;
custom openers can opt into `transport.ModemBitsOpener` to receive another policy.

Off/off is the conservative default because DTR assertion can reset some
Arduino-class GRBL controllers. It is not a hardware guarantee: the serial
library documents that Unix-like systems may pulse DTR/RTS briefly while
opening even if the requested initial levels are off. EmberDeck's CLI exposes
the same independent settings as `-serial-dtr on|off` and `-serial-rts on|off`.
The one-connection-per-job lifecycle remains in place; each stream owns and
closes its connection, and persistent connection reuse has not been validated.

The implementation uses [`go.bug.st/serial` v1.8.0](https://pkg.go.dev/go.bug.st/serial/v1.8.0),
whose API exposes cross-platform serial open, read/write/close, read timeouts,
and port enumeration. Operator port selection and device permissions are covered
in [Deployment](deployment.md#serial-workers-and-all-in-one-hardware-mode).

No physical controller has been tested or qualified with this transport yet.
In particular, issue #48's bench confirmation of open-time modem-line and reset
behavior remains outstanding; the issue stays open until that evidence exists.
Baud rate, reset behavior, USB bridge compatibility, grounding, motion
completion, enclosure interlocks, and emergency stops must be verified for the
specific machine setup before any physical operation. Software feed-hold or
reset requests are not emergency stops. The package does not automatically
resume interrupted programs.
