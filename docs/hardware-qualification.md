# Hardware qualification

EmberDeck's software and simulator checks do not qualify a controller or laser
machine. A controller is qualified only for the exact board revision, firmware,
transport, baud rate, electrical setup, and EmberDeck release recorded in a
signed test report. Qualification does not transfer to another board revision,
firmware build, wiring arrangement, or machine.

## Qualification matrix

| Controller board and revision | Firmware version/build | Transport | Baud rate | Status | Signed test report |
| --- | --- | --- | --- | --- | --- |
| No exact controller setup has been physically tested | Not recorded | Not tested | Not tested | **Unqualified** | None; no signed report exists |

There are currently no qualified physical controller setups. GRBL protocol
support describes the software interface EmberDeck implements; it is not a
claim that a GRBL 1.1 board or complete cutter has been qualified. Do not use a
“supported” label for hardware until a signed report for that exact setup is
published here.

### Status meanings

- **Qualified:** Every required test below passed for the exact recorded setup,
  and an operator and an independent reviewer signed the report.
- **Partial:** A signed report records which tests passed, failed, or were not
  run. Partial status is not a supported-hardware claim.
- **Unqualified:** No complete signed report exists, a required test failed, or
  the exact hardware and configuration are not known.

## Test procedure

Use the machine and controller manufacturers' instructions for all setup and
operation. A qualified operator must supervise the test, and an independent
safety reviewer must verify each gate. Stop if any result is unexpected. Software
pause, cancel, feed hold, and reset are not emergency stops.

### 1. Record the setup

Record the EmberDeck release and configuration; controller manufacturer, exact
board model and revision; firmware version and build identifier; worker host and
version; transport adapter and wiring; baud rate and serial settings; machine
model; enclosure and extraction configuration; and the names of the operator and
reviewer. Attach the controller configuration dump and the vendor instructions
used for the test. Do not infer an unknown field from a product family name.

### 2. Controller-only tests with motion and laser disabled

Use a manufacturer-approved test load or disconnected controller output. Keep
the machine's laser physically disconnected. Record the controller state and
the exact bytes/results for each case:

- Opening and closing the transport, including whether DTR/RTS resets the
  controller and whether a startup banner is observed.
- Readiness and settings reads from an idle controller, plus refusal from
  startup, alarm, busy, and malformed-response states.
- Long moves that fill the controller planner. Record when each acknowledgment
  arrives while status continues to report motion, and verify the worker does
  not treat buffered or acknowledged commands as completed motion.
- Door, Alarm, and Hold reports, including fresh Hold transitions after pause
  and resume commands.
- Pause and cancel latency, measured from the operator request to the controller
  state transition. Repeat at idle and while a long move is in progress.
- Controller serial unplug, worker process loss, and EmberDeck server loss.
  Record the observed controller state and confirm the job is interrupted rather
  than replayed after reconnection.

### 3. Motion-only test

Keep the laser physically disconnected and verify that it cannot emit. Use an
enclosed machine, an empty work area, and the manufacturer's approved motion-only
procedure. Confirm work coordinates and travel limits using the manufacturer's
method. Exercise only the documented test program. Verify that upload and
dispatch require explicit operator action, and record motion, Door/Alarm/Hold,
pause, cancel, disconnect, and recovery behavior. Stop if the machine moves
outside its declared area or a safety control behaves unexpectedly.

### 4. Enclosed low-power test

Proceed only after the controller-only and motion-only gates pass and the
manufacturer explicitly approves the test. Use the manufacturer's low-power
procedure inside the closed enclosure with extraction operating. Keep the
operator at the machine's independent emergency stop. Record commanded settings
and measured results using the manufacturer's approved method. Do not treat an
EmberDeck status, preview, or power command as evidence of optical output or
safe operation.

### 5. Verify independent safeguards

With the manufacturer's prescribed test method, confirm the enclosure door
interlock independently prevents or stops laser output, and confirm the physical
emergency stop independently stops the machine. Record who observed each result
and how it was verified. Do not bypass either safeguard for an EmberDeck test.

## Signed report template

Publish one report per exact hardware and configuration entry in the matrix. It
must include:

- Setup fields from step 1, plus the exact EmberDeck release and report date.
- Test results, measurements, deviations, failures, and unrun cases from every
  procedure step.
- Supporting logs and photographs that do not expose credentials or personal
  information.
- The operator's and independent reviewer's names, roles, signatures, and
  signature dates.
- The resulting status and its precise limits. A change to board revision,
  firmware, transport, baud rate, wiring, or machine setup requires a new report.

Until a signed report is linked in the matrix, the corresponding hardware stays
unqualified. See [Safety & Limitations](safety.md) for the operating boundary.
