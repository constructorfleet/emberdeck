# GRBL driver package

## API and program input

### Public streaming API

| Signature | Purpose | Connection ownership |
| --- | --- | --- |
| `func Validate(program string) (int, error)` | Validate the complete program and return its executable line count. | Does not use a connection. |
| `func Stream(ctx context.Context, conn io.ReadWriteCloser, program string, onProgress func(acked, total int)) error` | Stream a validated program without interactive controls. | Takes ownership after program and context validation; closes the connection on return. |
| `func StreamControlled(ctx context.Context, conn io.ReadWriteCloser, program string, commands <-chan Command, onProgress func(acked, total int), onState func(string), onApplied ...func(Command)) error` | Stream while accepting `Pause` and `Resume` commands. | Takes ownership after program and context validation; closes the connection on return. |
| `func StreamObserved(ctx context.Context, conn io.ReadWriteCloser, program string, commands <-chan Command, onProgress func(acked, total int), onState func(string), onStatus func(StatusReport), onApplied ...func(Command)) error` | Stream while also reporting parsed status reports. | Same ownership rule as `StreamControlled`. |
| `func StreamWithOptions(ctx context.Context, conn io.ReadWriteCloser, program string, opts Options) error` | Stream with controls and callbacks from `Options`, including configurable `Timeouts`. | Same ownership rule as `StreamControlled`. |
| `func DefaultTimeouts() Timeouts` | Return the default response and liveness bounds. | No connection. |
| `func ReadSettings(ctx context.Context, conn io.ReadWriteCloser, timeout time.Duration) (Settings, error)` | Read settings from an idle controller without streaming a job. | Caller retains ownership; this function does not close the connection. |

All streaming APIs validate the full input before sending bytes. They send one source line at a time, wait for its acknowledgment, report acknowledged executable line counts, then send and acknowledge an injected `M5` before waiting for `<Idle...>`. The cleanup line is excluded from progress. Invalid arguments return before connection ownership transfers.

### Program validation

- Input must be ASCII. Executable text containing GRBL realtime characters (`!`, `?`, `~`, or Ctrl-X) is rejected.
- Semicolon and parenthetical comments are stripped safely; nested or unterminated parenthetical comments are rejected.
- Lines are trimmed, blank lines are skipped, and CRLF is accepted.
- An executable line may contain at most 79 bytes, matching GRBL's 80-byte line buffer including its terminator.

The wire boundary is `io.ReadWriteCloser`, so a serial adapter and simulator can provide the same interface. Tests exercise this public wire protocol with both `net.Pipe` and the simulator.

## Controller protocol

### Readiness and controller settings

The initial status handshake fails closed on a GRBL `Grbl ` startup banner, a busy state, an alarm, or any state other than `Idle`. A startup banner means the controller reset: the job is refused before source-program bytes are sent, and the operator must re-verify work coordinates before another job. A banner during streaming also fails the job.

Set `driver.Options.OnSettings` to read settings during readiness. After the initial status query returns `Idle`, the stream sends the read-only `$$` command and collects `$N=value` lines until `ok`. It parses `$20` (soft limits), `$30` and `$31` (spindle range), `$32` (laser mode), and `$130`/`$131` (X/Y max travel), ignoring other settings, status reports, and messages. The callback runs only after the complete reply and before any program byte; a non-nil return refuses the job.

Malformed values for those settings, `error:`, `ALARM:`, a `Grbl ` startup banner, or no `ok` within the response timeout fail the job before streaming. The settings read never writes `$` settings. A refusal sends no feed hold or soft reset because no program byte was sent. A Pause queued during readiness gates the stream as usual and is applied when streaming would start.

`Settings.Mismatches(expected)` compares every non-nil expected value and returns one description per differing or unreported setting, for example `$30 (maximum spindle): controller 255, profile 1000`.

### Pause and resume

`onState` receives exact GRBL state fields from status reports matched to a current status query, such as `Idle`, `Run`, `Hold:1`, or `Hold:0`. Stale reports from a query already in flight when a control arrives are not reported as confirmation. An optional final `onApplied` callback reports when `!` or `~` has actually been written. This is an application receipt, not controller confirmation: workers must wait for a later fresh status before reporting paused or resumed. Stale and unsolicited statuses cannot satisfy that confirmation.

| Event | Driver behavior | Confirmation |
| --- | --- | --- |
| Pause requested | Gate source lines, including when a line acknowledgment is pending; send `!`. | Fresh `Hold:1` means pausing. Fresh `Hold:0` or `Idle` after the request confirms paused. |
| Resume requested | Accept only after a confirmed hold; send `~` and keep source output gated. | Fresh `Run` or `Idle` after the command confirms resumed. |
| New Pause while Resume is pending | Cancel the pending or unconfirmed Resume. | The operator must request Resume again. |
| External Hold observed | Gate source output. | Never resume automatically. |

The driver allows one status query at a time, at no more than 5 Hz. No source line or injected `M5` is sent during a hold. A valid paused status can continue indefinitely, including while an in-flight line acknowledgment remains pending. In final cleanup, a held job cannot complete on `Idle` until explicitly resumed and a fresh post-resume status confirms the controller state.

```text
  Run ──Pause──> Pausing ──fresh Hold:0 or Idle──> Paused
   ^                                                │
   │                                                │ Resume (confirmed hold)
   │                                                v
   └────────────── fresh Run or Idle ─────────── Resuming

Fresh Hold:1 reports the pausing transition; fresh Hold:0/Idle confirms Paused.
Fresh Run/Idle after Resume confirms the controller has resumed.
A new Pause cancels a pending Resume; a fresh explicit Resume is required.
```

### Timeouts and long moves

`driver.DefaultTimeouts()` returns the default response and liveness bounds. `driver.StreamWithOptions(ctx, conn, program, driver.Options{...})` accepts a `Timeouts` value with `Response` and `Liveness` durations. Zero fields use defaults; negative values are rejected. Bounds should stay well above the 200 ms status polling interval. `Stream`, `StreamControlled`, `StreamObserved`, and the worker use defaults.

| Condition | Default behavior |
| --- | --- |
| Line acknowledgment | Must normally arrive within 30 seconds. A healthy confirmed hold suspends the deadline until resume is confirmed. |
| Missing status response | Fails within 30 seconds of the unanswered query; status queries are sent at up to 5 Hz. |
| No acknowledgment and no progress after a line | Fails 30 seconds after the line was sent. |
| `Run` status with no reported progress | Fails 30 seconds after the last progress report, or 30 seconds after the line was sent if later. |
| Pause or resume transition | Must complete within 30 seconds. |

GRBL withholds `ok` while its planner buffer is full, so a slow, long move can keep one line unacknowledged for much longer than 30 seconds while the machine works normally. While an acknowledgment is outstanding, each fresh `Run` status report whose position (`MPos` or `WPos`) or planner block count (`Bf`) differs from the previous fresh report extends that line's deadline to at least 30 seconds after the report. A line therefore has no fixed maximum duration while the controller keeps reporting progress.

Progress is judged at the controller's reporting resolution. A move slow enough that neither its reported position nor planner count changes for 30 seconds is treated as stalled.

### Errors and physical stop limits

A controller error or unexpected response returns an error. On cancellation or a streaming error after controller interaction, the driver makes a bounded best-effort feed-hold and soft-reset write, then closes the connection to interrupt pending I/O. This is a software request only; it is not an emergency stop and does not guarantee motion has stopped. Keep physical emergency stops and enclosure interlocks independent.

## Development and references

### Simulator

`driver.NewSimulator()` returns an in-memory endpoint that acknowledges commands and reports `Hold:1` followed by `Hold:0`; it supports resume and reset for development and tests. It answers `$$` with `driver.SimulatorSettings` (`$30=1000`, `$31=0`, `$32=1`, `$20=0`, `$130=300`, `$131=200`). It does not model physical motion. The package does not open a serial port or manage job persistence.

### Protocol references

The driver behavior is based on GRBL's [interface documentation at commit `bfb67f0`](https://github.com/gnea/grbl/blob/bfb67f0c7963fe3ce4aaf8a97f9009ea5a8db36e/doc/markdown/interface.md) and [realtime commands at the same commit](https://github.com/gnea/grbl/blob/bfb67f0c7963fe3ce4aaf8a97f9009ea5a8db36e/doc/markdown/commands.md).
