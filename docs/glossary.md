# Glossary

- **Driver:** The server's named connection to a controller, selected in the
  console. The `emberdeck driver` CLI command runs a worker process that
  registers this driver ID; it is distinct from the Go GRBL driver package.
- **Worker:** A process that contacts the server, receives a job assignment, and
  communicates with the controller. A worker can run on the server host or on a
  separate host.
- **Lease:** The server-issued, single-execution authority for a worker to run
  one assigned job. A lost or interrupted lease is not reused to replay work.
- **Job:** A stored G-code program with its execution state. Uploading or
  generating a job queues it for review; it does not start execution.
- **Profile:** An operator-declared GRBL machine description, including the
  declared work area and limits used by the supported designer. It is not read
  from or written to the controller and does not qualify hardware.
- **Project:** Editable design source and operation settings saved separately
  from jobs. Generating a project creates a separate queued G-code job.
- **Toolpath:** A geometric interpretation of supported program motion used for
  visualization. It may be estimated, ambiguous, or unavailable and is not proof
  that motion occurred or that the laser fired.

See [Safety & Limitations](safety.md) for how to interpret status, profiles, and
toolpath displays.
