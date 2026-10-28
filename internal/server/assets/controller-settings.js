"use strict";

// Controller settings read by each driver's worker with GRBL's read-only `$$`
// command. The console shows them for the selected driver and warns about
// uploaded jobs that would not go dark when motion stops. The server compares
// profile-generated jobs with their profile snapshot at Start.
window.EmberDeckControllerSettings = (() => {
  const fields = [
    ["$30", "max spindle"],
    ["$31", "min spindle"],
    ["$32", "laser mode"],
    ["$20", "soft limits"],
    ["$130", "X max travel mm"],
    ["$131", "Y max travel mm"],
  ];

  function summary(driver) {
    if (!driver) return "";
    const settings = driver.controller_settings;
    if (!settings) {
      return driver.settings_error
        ? `Controller settings unavailable: ${driver.settings_error}`
        : "Controller settings not reported yet; the worker reads them with $$ while idle.";
    }
    const values = fields
      .filter(([key]) => typeof settings[key] === "number")
      .map(([key, label]) => `${key}=${settings[key]} (${label})`);
    const readAt = driver.settings_read_at ? ` at ${new Date(driver.settings_read_at).toLocaleTimeString()}` : "";
    return `Controller settings read${readAt}: ${values.join(" · ") || "none of $20/$30/$31/$32/$130/$131 reported"}`;
  }

  // renderDriver shows the selected driver's last reported settings in host.
  function renderDriver(host, driver) {
    if (!host) return;
    host.textContent = summary(driver);
    host.hidden = !driver;
  }

  // profileMismatches mirrors the server's Start check for a generated job:
  // $30, $31, and $32 must equal the job's profile snapshot.
  function profileMismatches(profile, settings) {
    const expected = [
      ["$30", "maximum spindle", profile.max_s],
      ["$31", "minimum spindle", profile.min_s],
      ["$32", "laser mode", profile.laser_mode_enabled ? 1 : 0],
    ];
    return expected
      .filter(([key, , want]) => settings[key] !== want)
      .map(([key, label, want]) => `${key} (${label}): ${typeof settings[key] === "number" ? `controller ${settings[key]}` : "not reported by controller"}, profile ${want}`);
  }

  // jobWarnings lists settings warnings for a queued job on the selected
  // driver: profile mismatches for generated jobs (Start will be refused) and
  // laser-mode warnings for uploaded jobs.
  function jobWarnings(job, driver) {
    if (!job || job.state !== "queued") return [];
    const settings = driver?.controller_settings;
    if (job.provenance) {
      if (!driver) return [];
      if (!settings) return [`Driver ${driver.id} has not reported controller settings; Start is refused until it does.`];
      const diff = profileMismatches(job.provenance.profile, settings);
      return diff.length ? [`Controller settings on ${driver.id} differ from this job's profile snapshot: ${diff.join("; ")}. Start will be refused.`] : [];
    }
    const warnings = [];
    if (settings && settings["$32"] === 0) {
      warnings.push(`Driver ${driver.id} reports $32=0 (laser mode off): the laser keeps its commanded power while motion stops, including during a feed hold.`);
    }
    if (job.program_review?.constant_power_m3) {
      warnings.push("Program uses M3 constant power: the laser keeps its commanded power while motion stops, including during a feed hold.");
    }
    if (driver && !settings) {
      warnings.push(`Driver ${driver.id} has not reported controller settings; check $32 before starting.`);
    }
    return warnings;
  }

  // startDetails adds the driver's settings and any warnings to the Start
  // confirmation dialog as [term, value] pairs.
  function startDetails(job, driver) {
    if (!driver) return [];
    const settings = driver.controller_settings;
    const values = settings
      ? fields.filter(([key]) => typeof settings[key] === "number").map(([key]) => `${key}=${settings[key]}`).join(" · ")
      : driver.settings_error ? `unavailable: ${driver.settings_error}` : "not reported";
    return [["Controller settings", values], ...jobWarnings(job, driver).map((text) => ["Settings warning", text])];
  }

  function appendJobWarnings(host, job, driver) {
    for (const text of jobWarnings(job, driver)) {
      const warning = document.createElement("span");
      warning.className = "job-error laser-mode-warning";
      warning.textContent = text;
      host.append(warning);
    }
  }

  return { summary, renderDriver, jobWarnings, startDetails, appendJobWarnings };
})();
