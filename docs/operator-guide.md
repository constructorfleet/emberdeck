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
