# Safety & Limitations

EmberDeck is job-management software, not a safety controller. Physical
controllers, cutters, enclosures, interlocks, emergency stops, and complete
machine setups have not been qualified. Simulator tests and software checks are
not evidence of physical safety. Follow the machine and controller manufacturer's
instructions and supervise every operation. The exact setup and evidence status
are tracked in the [hardware qualification matrix](hardware-qualification.md).

## Supervision and independent safeguards

Keep the machine supervised whenever a job is running or paused. Use independent
physical enclosure interlocks and an emergency stop. Software pause, cancel,
feed-hold, or reset requests are not emergency stops and cannot guarantee that
motion or laser output has stopped. A controller status is a report from the
controller, not a physical sensor.

A camera feed is only an observation aid. It can be delayed, stale, obstructed,
or unavailable, and it does not detect every hazardous condition. It is not a
safety device and does not replace direct supervision, enclosure interlocks, or
an emergency stop.

## Laser output, motion, and travel

Review the G-code and machine setup before dispatch. A toolpath display may label
supported commanded laser-on motion separately from laser-off travel, but its
interpretation is limited to the program features it understands. It does not
measure optical output or prove material processing. Dynamic laser mode, a
commanded power value, or an `Idle` status is not proof that a physical machine
is safe or that the beam behaved as expected. Verify controller settings and
laser behavior on the specific machine using the manufacturer's guidance.

Machine profiles are declarations: EmberDeck does not home, zero, measure, or
configure the controller from them. A declared work area or preview is not a
verified hardware boundary. Position-based progress can be estimated, ambiguous,
or unknown; acknowledged lines do not prove completed motion, and the visualization
does not establish physical machine bounds.

## Interrupted work is not replayed

After a server restart or loss of worker communication, active work is marked
interrupted. EmberDeck fails closed and never automatically replays the job.
The machine may have moved or stopped in an unknown state. Inspect it and the
controller before deciding whether to create and explicitly dispatch another
job. A paused job is also not a safe restart point.

The [Operator Guide](operator-guide.md) describes the confirmation workflow;
[Troubleshooting](troubleshooting.md) covers common connection and recovery
issues.
