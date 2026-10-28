# Operator console guide

The console stores G-code programs, dispatches them to a connected driver, and
shows progress reported by that driver. Keep the machine supervised whenever a
program is running or paused. Pause and resume controls are available only when
the assigned driver reports the `pause_resume` capability. The console shell,
stylesheet, script, and icon load without the API key. Only `/api/` requests
need the key, so a mistyped address shows **Page not found** rather than an
authentication error.

## Connect and choose a driver

Open the EmberDeck console and enter the API key supplied by the server
operator; the key field is masked. Surrounding whitespace is removed before the
key is checked. **Connect**
validates the key with the server before saving it in this browser session or
starting library refreshes. While that check is in progress, the button reads
**Connecting…** and is disabled. A rejected key is not saved or retried; the
console stops polling, focuses the key field, and lets you correct it and try
again. API-dependent controls remain disabled until the key is accepted.

After connecting, the access panel collapses to **Connected · Change key ·
Disconnect**. Choose **Change key** to return to the key field and validate a
different key, or **Disconnect** to forget the session key and stop polling. In
either case the library controls are disabled until you connect again. Once
connected, choose the driver assigned to the machine you prepared.

![Connected operator console with a queued program, driver selector, and safety check](assets/screenshots/operator-console-library.png)

## Find the main controls

The console groups design sources, G-code intake, and stored programs on one
page. The overview below shows a saved profile and project alongside the upload
and job controls. Close the project editor to return to the saved-project list;
select **Reopen** to continue editing.

![Console overview with a saved machine profile, saved project, G-code intake, and job library](assets/screenshots/designer-console-overview.png)

| Area | Controls | What they do |
| --- | --- | --- |
| Machine profiles | **New profile**, **Edit profile**, **Delete profile** | Store the declared GRBL work area and limits used by the rectangle editor. |
| Design projects | **New rectangle project** or **New vector project**, **Reopen**, **Save**, **Generate queued job**, **Delete** | Edit a v1 rectangle engraving source or a v2 multi-path design with ordered engraving/cutting layers, preview its declared envelope, or create a separate queued program. |
| Add a program | File chooser, display name, profile selector, **Add to library** | Upload plain-text G-code (up to 4 MiB), validate line limits, and review its known path against the selected profile. |
| Stored programs | Driver selector, readiness checkbox, bounds override, **View**, **Start**, **Pause**, **Resume**, **Cancel**, **Delete** | Inspect and manage jobs through explicit bounds override and readiness confirmation. Available controls depend on job state and driver capabilities. |

The profile and project lists remain separate from generated jobs: deleting a
source project does not delete a generated program, and editing a source does
not change an existing program.

## Add and inspect a program

Choose a G-code file, enter a display name, select the declared machine profile,
and add it to the library. Upload rejects driver line-limit errors with their
source line before the job is queued. The job row shows known XY bounds and a
static path preview against the profile work area. Arcs, incremental positioning,
machine-coordinate moves, work-coordinate writes, an unknown starting position,
or a stale/missing profile keep the overall bounds status **unknown**. The
preview cannot establish the current head position or physical machine safety.

An **in bounds** review does not replace checking the program and machine. For
**out of bounds** or **unknown** jobs, inspect the warnings, path, and profile;
the per-job override checkbox must be selected before **Start** is enabled.
Then verify the machine's interlocks and check the supervision box. Both
confirmations are explicit and the bounds override is cleared after dispatch.

![Uploaded G-code with an out-of-bounds profile review, path bounds, and explicit override control](assets/screenshots/operator-console-uploaded-bounds-review.png)

## Declare a machine profile and design a rectangle

Connect with the server API key, then create a profile from the machine's
documented settings and declared work-area dimensions. This profile is an
operator declaration; EmberDeck does not query, home, zero, or write controller
settings. Confirm that `$30` matches the declared maximum spindle value, `$31=0`,
and `$32=1`, and prepare the lower-left origin of the active GRBL work coordinate
system before using the profile.
Selecting a profile does not verify the controller or driver assigned to the
job. Before **Start**, confirm that the selected dispatch driver's controller
has the matching `$30`, `$31`, and `$32` settings and prepared work coordinates.
See the official [GRBL settings reference](https://github.com/gnea/grbl/blob/master/doc/markdown/settings.md)
and [laser mode reference](https://github.com/gnea/grbl/blob/master/doc/markdown/laser_mode.md).
The commanded S percentage is not a measurement of optical output.

![Declared GRBL profile with the work envelope and spindle/feed limits](assets/screenshots/designer-profile.png)

Create a rectangle project and enter its X/Y position, width, height, feed, and
commanded power percentage in millimetres. The SVG uses lower-left model
coordinates; its screen Y direction is only a view transform. It labels the
declared workspace, outline path, and laser-off approach endpoint. The actual
starting position is unknown, so the preview does not draw a travel path to the
endpoint. Geometry outside the declared envelope, invalid feed or power, and
values with more than three decimal places block saving and generation.
The version 1 rectangle editor supports one rectangle and vector engraving.
Version 2 supports multiple rectangle, ellipse, and line paths, described below.
Neither workflow imports SVG or text or processes raster images.

![Editable rectangle project with declared workspace and laser-off approach endpoint](assets/screenshots/designer-project.png)

If a shape extends past the declared workspace, the editor explains the bounds
error and disables both saving and generation until the dimensions are corrected.

![Out-of-bounds rectangle with saving and generation disabled](assets/screenshots/designer-project-out-of-bounds.png)

Saved profiles and projects can be reopened, edited, and deleted. Editing a
profile makes its projects require review against the new revision; save the
project against the selected revision before generating. Deleting a referenced
profile is blocked by the server.
While a profile editor is open, generation is disabled. Save or cancel those
profile edits first; after saving a changed profile, review and save the project
against its new revision before generating.

![Saved project blocked from generation until it is reviewed against the changed profile](assets/screenshots/designer-stale-profile.png)

**Generate queued job** creates a normal library program. It does not dispatch
the job, and it clears any earlier supervision confirmation. Use **View** to
inspect its generated program and immutable project and profile provenance,
then review the machine setup and use the existing explicit supervision
confirmation and **Start** control when ready.

![Generated rectangle program with immutable source provenance](assets/screenshots/designer-generated-review.png)

![Rectangle designer controls and preview on a narrow screen](assets/screenshots/designer-mobile.png)

## Create a multi-path vector project

Select **New vector project** to add multiple rectangle, ellipse, and line
shapes. Select a shape to edit its X/Y position and width/height, assign it to
an operation layer, or remove it. Add engraving or cutting layers, set each
layer's feed and commanded power, and move layers to change execution order.
Every path must be assigned before generation. Lines can be engraved; cutting
requires closed shapes with nonzero area. The preview uses green for engraving,
red dashed outlines for cutting, and gray dotted paths for unassigned geometry.

![Multi-path designer with editable shapes, ordered engraving and cutting layers, and profile-aware preview](assets/screenshots/designer-vector-project.png)

Generation creates an immutable queued G-code program with the project, profile,
and operation layer values in its provenance. Both cutting and engraving use
GRBL dynamic laser mode (`M4`), which turns output off when motion stops or is
held. This behavior and the declared machine profile still require verification
on the target controller and machine. Review the G-code and setup, then use the
existing readiness confirmation and **Start** control to dispatch.

![Generated multi-path program showing ordered operation provenance](assets/screenshots/designer-vector-review.png)

![Multi-path vector editor on a narrow screen](assets/screenshots/designer-vector-mobile.png)

## Monitor a running program

The job row shows controller-acknowledged lines separately from the spatial
toolpath. Open **View** during a supported run to see completed, active, and
remaining XY segments, laser-off travel, and the latest available work position.
Use **+**, **−**, **Fit**, or drag the plot to inspect it. The plot fits the
program geometry; it does not know the physical machine bounds. An estimated
head position assumes GRBL reports millimetres (`$13=0`), and the UI labels that
assumption. A repeated or overlapping path can make correlation ambiguous;
missing coordinates, disconnects, pauses, and restarts can leave progress
unknown. Acknowledged lines are never treated as proof of completed motion.

![Running program with estimated live toolpath, head marker, legend, and G-code source open](assets/screenshots/operator-console-running-view.png)

## Pause and resume

A held status reports motion, not laser output. Follow the controller's
[laser-mode requirements](https://github.com/gnea/grbl/wiki/Grbl-v1.1-Laser-Mode)
and keep physical interlocks and emergency stops independent of software hold.

Select **Pause** while a capable assigned driver is running the program. The
console displays **Pausing…** while it waits for the driver and controller; that
status does not mean the machine is held. The row changes to **paused** only
after the controller reports a stable hold. The controller status is shown
separately as the last status received, for example `Hold:0`. It is a report
from the driver, not a live physical sensor.

![Program paused after the controller reports Hold:0](assets/screenshots/operator-console-paused.png)

To continue, check the safety confirmation again and select **Resume**. The
confirmation is cleared after the request. The console shows the pending
`resuming` state until the driver reports that the controller is running again.

## Cancel and remove jobs

**Cancel** remains available while a job is running, pausing, paused, or
resuming. Active jobs cannot be deleted. Completed, failed, canceled, and
interrupted jobs can be removed with **Delete** after confirming the browser
prompt.

![Completed program with its final acknowledged line count and delete control](assets/screenshots/operator-console-completed.png)

![Canceled program with its terminal delete control](assets/screenshots/operator-console-canceled.png)

![Narrow console layout showing the pause control and live job status](assets/screenshots/operator-console-mobile.png)

## Local browser walkthrough

The Playwright browser suite runs the console against an isolated local server
and GRBL simulator. It verifies controller acknowledgements and progress
behavior; the simulator does not qualify physical machine safety. See
[Browser console tests](browser-tests.md) for the command and scope.

Regenerate the screenshots from the current console and simulator with
`npm run docs:screenshots`.

## Requested UI that is not available yet

The current designer does not have multi-selection, rotation/alignment, text or
SVG import, undo/redo, or raster engraving. Live path correlation currently
covers supported linear XY moves; arcs and unsupported coordinate modes are
reported as warnings, and the view does not show declared machine bounds. The
console also has no driver-adapter or importer-plugin configuration screens.
Existing plugins are trusted preflight
processes; they are not machine drivers or file converters. See the [designer
plan](research/designer-architecture.md),
[feature roadmap](roadmap.md#feature-requests), and the open requests for
[#4](https://github.com/ConstructorFleet/emberdeck/issues/4),
[#7](https://github.com/ConstructorFleet/emberdeck/issues/7),
[#15](https://github.com/ConstructorFleet/emberdeck/issues/15), and
[#16](https://github.com/ConstructorFleet/emberdeck/issues/16).
