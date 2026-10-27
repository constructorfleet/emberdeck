# Architecture

## Target design

EmberDeck is one Go executable with server, driver, and all-in-one modes. The
server owns durable job records, assignments, access control, and browser UI.
A driver owns exactly one controller connection and reports progress over an
authenticated, versioned HTTP protocol. Remote drivers must use TLS or a trusted
TLS tunnel. The simulator uses the same dispatch boundary as physical drivers.

The first physical protocol is GRBL 1.1 send/response: send one line, await an
acknowledgment, and query controller status before declaring physical completion.
A controller accepting a line is not proof that motion has finished. Software
stop is best effort; a physical emergency stop and enclosure interlocks remain
mandatory. Never automatically resume a job after process or connection failure.

Plugins are separate processes speaking a versioned JSON protocol, avoiding Go
shared-library ABI and operating system restrictions. Plugins are trusted code,
not sandboxed. OctoPrint Python plugins are not directly compatible; protocol
adapters may be implemented later without implying compatibility today.

## Verification boundaries

HTTP API tests exercise persistence and lifecycle through requests. Driver tests
use a controller simulator at the wire boundary. Plugin tests execute a real
fixture process. Browser checks cover operator flows. None qualifies hardware.

## Current implementation

Repository and delivery scaffolding only. See the roadmap for pending work.
