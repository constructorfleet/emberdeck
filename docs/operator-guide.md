# Operator console guide

The console stores G-code programs, dispatches them to a connected driver, and
shows progress reported by that driver. Keep the machine supervised whenever a
program is running or paused. Pause and resume controls are available only when
the assigned driver reports the `pause_resume` capability.

## Connect and choose a driver

Open the EmberDeck console and enter the API key supplied by the server
operator. The key field is masked, and the key stays in this browser session.
After connecting, choose the driver assigned to the machine you prepared.

![Connected operator console with a queued program, driver selector, and safety check](assets/screenshots/operator-console-library.png)

## Add and inspect a program

Choose a G-code file, enter a display name, and add it to the library. Use
**View** to inspect the program text before dispatch. Review the program and
machine setup, verify the machine's interlocks, and check the supervision box
before selecting **Start**. The checkbox is cleared after dispatch.

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
This initial editor supports one rectangle and vector engraving only. It does
not import SVG or text, generate cutting operations, or process raster images.

![Editable rectangle project with declared workspace and laser-off approach endpoint](assets/screenshots/designer-project.png)

Saved profiles and projects can be reopened, edited, and deleted. Editing a
profile makes its projects require review against the new revision; save the
project against the selected revision before generating. Deleting a referenced
profile is blocked by the server.
While a profile editor is open, generation is disabled. Save or cancel those
profile edits first; after saving a changed profile, review and save the project
against its new revision before generating.

**Generate queued job** creates a normal library program. It does not dispatch
the job, and it clears any earlier supervision confirmation. Use **View** to
inspect its generated program and immutable project and profile provenance,
then review the machine setup and use the existing explicit supervision
confirmation and **Start** control when ready.

![Generated rectangle program with immutable source provenance](assets/screenshots/designer-generated-review.png)

![Rectangle designer controls and preview on a narrow screen](assets/screenshots/designer-mobile.png)

## Monitor a running program

The job row shows acknowledged lines and a progress bar. **View** keeps the
program available while job and driver status continue to refresh.

![Running program progress with its source program open](assets/screenshots/operator-console-running-view.png)

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
