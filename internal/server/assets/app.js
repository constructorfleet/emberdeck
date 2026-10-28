"use strict";

(() => {
  const $ = (selector) => document.querySelector(selector);
  const keyInput = $("#api-key");
  const authForm = $("#auth-form");
  const uploadForm = $("#upload-form");
  const fileInput = $("#job-file");
  const jobName = $("#job-name");
  const list = $("#job-list");
  const notice = $("#notice");
  const empty = $("#empty-state");
  const programPanel = $("#program-panel");
  const driverSelect = $("#driver-select");
  const confirmation = $("#operator-confirmation");
  let token = sessionStorage.getItem("emberdeck-api-key") || "";
  let jobs = [];
  let drivers = [];
  let selectedDriver = "";
  let shownJobID = "";
  let polling = false;
  let pollTimer = null;
  let profiles = [];
  let projects = [];
  let editingProfile = null;
  let openProject = null;

  function message(text) {
    notice.textContent = text;
    notice.hidden = !text;
  }

  async function api(path, options = {}) {
    const headers = new Headers(options.headers || {});
    headers.set("Authorization", `Bearer ${token}`);
    if (options.body) headers.set("Content-Type", "application/json");
    const response = await fetch(path, { ...options, headers });
    if (!response.ok) {
      let detail = `Request failed (${response.status})`;
      try { detail = (await response.json()).error || detail; } catch (_) { /* keep status */ }
      throw new Error(detail);
    }
    return response.status === 204 ? null : response.json();
  }

  function button(label, className, action, ariaLabel) {
    const element = document.createElement("button");
    element.type = "button";
    element.className = className;
    element.textContent = label;
    if (ariaLabel) element.setAttribute("aria-label", ariaLabel);
    element.addEventListener("click", action);
    return element;
  }

  const profileFields = {
    name: "#profile-name", width_mm: "#profile-width", height_mm: "#profile-height",
    max_s: "#profile-max-s", min_feed_mm_min: "#profile-min-feed",
    max_feed_mm_min: "#profile-max-feed", max_power_percent: "#profile-max-power",
  };
  const projectFields = {
    name: "#project-name", x_mm: "#rect-x", y_mm: "#rect-y", width_mm: "#rect-width",
    height_mm: "#rect-height", feed_mm_min: "#operation-feed", power_percent: "#operation-power",
  };
  const svgNS = "http://www.w3.org/2000/svg";

  function selectedProfile() {
    return profiles.find((profile) => profile.id === $("#profile-select").value) || null;
  }

  function setDesignerStatus(text) {
    $("#designer-status").textContent = text;
    $("#designer-status").classList.remove("error");
  }

  function setDesignerError(text) {
    $("#designer-status").textContent = text;
    $("#designer-status").classList.add("error");
  }

  function renderProfiles() {
    const select = $("#profile-select");
    const prior = select.value;
    select.replaceChildren();
    if (!profiles.length) {
      const option = document.createElement("option");
      option.value = "";
      option.textContent = "Create a declared profile";
      select.append(option);
    } else {
      for (const profile of profiles) {
        const option = document.createElement("option");
        option.value = profile.id;
        option.textContent = `${profile.name} · r${profile.revision}`;
        select.append(option);
      }
    }
    select.disabled = !profiles.length;
    if (profiles.some((profile) => profile.id === prior)) select.value = prior;
    else if (profiles.length) select.value = profiles[0].id;
    const hasProfile = Boolean(selectedProfile());
    $("#edit-profile").disabled = !hasProfile;
    $("#delete-profile").disabled = !hasProfile;
    $("#new-project").disabled = !hasProfile;
    renderProjects();
    updateProjectValidation();
  }

  function renderProjects() {
    const list = $("#project-list");
    list.replaceChildren();
    for (const project of projects) {
      const row = document.createElement("li");
      const label = document.createElement("strong");
      label.textContent = `${project.name} · r${project.revision}`;
      const actions = document.createElement("div");
      actions.className = "designer-actions";
      actions.append(button(`Reopen ${project.name}`, "button button-small", () => openSavedProject(project.id)));
      actions.append(button("Delete", "button button-small button-danger", () => deleteProject(project.id, project.name), `Delete project ${project.name}`));
      row.append(label, actions);
      list.append(row);
    }
  }

  async function refreshDesigner() {
    if (!token) return;
    try {
      const [nextProfiles, nextProjects] = await Promise.all([api("/api/v1/profiles"), api("/api/v1/projects")]);
      profiles = Array.isArray(nextProfiles) ? nextProfiles : [];
      projects = Array.isArray(nextProjects) ? nextProjects : [];
      renderProfiles();
      setDesignerStatus(`${profiles.length} profile${profiles.length === 1 ? "" : "s"} · ${projects.length} saved project${projects.length === 1 ? "" : "s"}`);
    } catch (error) {
      setDesignerError(`Designer storage unavailable: ${error.message}`);
    }
  }

  function profilePayload() {
    return {
      schema_version: 1,
      name: $(profileFields.name).value.trim(),
      controller: "grbl-1.1", units: "mm", origin: "lower-left",
      width_mm: Number($(profileFields.width_mm).value),
      height_mm: Number($(profileFields.height_mm).value),
      min_s: 0,
      max_s: Number($(profileFields.max_s).value),
      min_feed_mm_min: Number($(profileFields.min_feed_mm_min).value),
      max_feed_mm_min: Number($(profileFields.max_feed_mm_min).value),
      max_power_percent: Number($(profileFields.max_power_percent).value),
      laser_mode_enabled: true,
    };
  }

  function beginProfileEdit(profile = null) {
    editingProfile = profile;
    $("#profile-form").hidden = false;
    $("#profile-form-title").textContent = profile ? `Edit ${profile.name}` : "New declared profile";
    for (const [key, selector] of Object.entries(profileFields)) $(selector).value = profile ? String(profile[key]) : "";
    $("#profile-min-s").value = "0";
    $("#profile-form").scrollIntoView({ behavior: "smooth", block: "nearest" });
    $(profileFields.name).focus();
    updateProjectValidation();
  }

  function projectPayload(profile) {
    return {
      schema_version: 1,
      name: $(projectFields.name).value.trim(),
      profile_id: profile.id,
      profile_revision: profile.revision,
      rectangle: {
        x_mm: Number($(projectFields.x_mm).value), y_mm: Number($(projectFields.y_mm).value),
        width_mm: Number($(projectFields.width_mm).value), height_mm: Number($(projectFields.height_mm).value),
      },
      operation: { type: "vector-engrave", feed_mm_min: Number($(projectFields.feed_mm_min).value), power_percent: Number($(projectFields.power_percent).value) },
    };
  }

  function matchesSavedProject(profile) {
    if (!openProject || !profile) return false;
    const current = projectPayload(profile);
    const savedRectangle = openProject.rectangle || {};
    const savedOperation = openProject.operation || {};
    return current.name === openProject.name && current.profile_id === openProject.profile_id && current.profile_revision === openProject.profile_revision &&
      current.rectangle.x_mm === savedRectangle.x_mm && current.rectangle.y_mm === savedRectangle.y_mm &&
      current.rectangle.width_mm === savedRectangle.width_mm && current.rectangle.height_mm === savedRectangle.height_mm &&
      current.operation.type === savedOperation.type && current.operation.feed_mm_min === savedOperation.feed_mm_min &&
      current.operation.power_percent === savedOperation.power_percent;
  }

  function decimalPlacesAtMostThree(raw) {
    const value = String(raw).trim();
    return /^\d+(?:\.\d{1,3})?$/.test(value);
  }

  function finiteField(selector) {
    const raw = $(selector).value;
    const value = Number(raw);
    return raw !== "" && Number.isFinite(value) && decimalPlacesAtMostThree(raw) ? value : null;
  }

  function milli(value) { return Math.round(value * 1000); }

  function projectValidation() {
    const profile = selectedProfile();
    if (!profile) return "Select or create a declared profile first.";
    const values = {};
    for (const key of ["x_mm", "y_mm", "width_mm", "height_mm", "feed_mm_min", "power_percent"]) {
      values[key] = finiteField(projectFields[key]);
      if (values[key] === null) return "Enter finite millimetre and feed values with at most three decimal places.";
    }
    if (values.x_mm < 0 || values.y_mm < 0 || values.width_mm <= 0 || values.height_mm <= 0) return "Position must be nonnegative and rectangle dimensions must be positive.";
    if (milli(values.x_mm) + milli(values.width_mm) > milli(profile.width_mm) || milli(values.y_mm) + milli(values.height_mm) > milli(profile.height_mm)) return "Rectangle is outside the declared workspace.";
    if (milli(values.feed_mm_min) < milli(profile.min_feed_mm_min) || milli(values.feed_mm_min) > milli(profile.max_feed_mm_min)) return "Engraving feed is outside the selected profile range.";
    if (values.power_percent <= 0 || milli(values.power_percent) > milli(profile.max_power_percent)) return "Commanded power percentage is outside the selected profile limit.";
    const sProduct = profile.max_s * milli(values.power_percent);
    if (!Number.isSafeInteger(sProduct) || Math.floor((sProduct + 50000) / 100000) < 1) return "This percentage rounds to S0; increase commanded power before saving.";
    return "";
  }

  function svgElement(name, attributes = {}, text = "") {
    const node = document.createElementNS(svgNS, name);
    for (const [key, value] of Object.entries(attributes)) node.setAttribute(key, String(value));
    if (text) node.textContent = text;
    return node;
  }

  function renderPreview(profile) {
    const content = $("#preview-content");
    content.replaceChildren();
    const W = Number(profile && profile.width_mm), H = Number(profile && profile.height_mm);
    const x = finiteField(projectFields.x_mm), y = finiteField(projectFields.y_mm);
    const w = finiteField(projectFields.width_mm), h = finiteField(projectFields.height_mm);
    if (![W, H].every(Number.isFinite) || W <= 0 || H <= 0) return;
    const scale = Math.min(550 / W, 290 / H);
    if (!Number.isFinite(scale) || scale <= 0) return;
    const left = 35, bottom = 325, width = W * scale, height = H * scale;
    if (![left, bottom, width, height].every(Number.isFinite)) return;
    content.append(svgElement("rect", { class: "workspace-shape", x: left, y: bottom - height, width, height }));
    content.append(svgElement("text", { x: left + 4, y: bottom - height + 14 }, `Declared workspace · ${W} × ${H} mm`));
    if (![x, y, w, h].every(Number.isFinite) || w <= 0 || h <= 0 || x < 0 || y < 0 || milli(x) + milli(w) > milli(W) || milli(y) + milli(h) > milli(H)) return;
    const px = left + x * scale, py = bottom - (y + h) * scale, pw = w * scale;
    if (![px, py, pw].every(Number.isFinite)) return;
    content.append(svgElement("path", { class: "engrave-path", d: `M ${px} ${bottom-y*scale} L ${px+pw} ${bottom-y*scale} L ${px+pw} ${py} L ${px} ${py} Z` }));
    content.append(svgElement("circle", { class: "approach-point", cx: px, cy: bottom-y*scale, r: 5 }));
    content.append(svgElement("text", { x: left, y: 349 }, `Laser-off approach endpoint · X${x} Y${y} mm`));
  }

  function updateProjectValidation() {
    const form = $("#project-form");
    if (form.hidden) return;
    const profile = selectedProfile();
    const warning = $("#project-validation");
    const stale = Boolean(openProject && profile && (openProject.profile_id !== profile.id || openProject.profile_revision !== profile.revision));
    const matchesSaved = matchesSavedProject(profile);
    const profileDraftOpen = !$("#profile-form").hidden;
    $("#stale-profile-warning").hidden = !stale;
    $("#profile-save-hint").hidden = !profileDraftOpen || !openProject;
    $("#stale-preview").hidden = !stale;
    const error = projectValidation();
    warning.textContent = error;
    warning.hidden = !error;
    const valuesReady = !error && Boolean($(projectFields.name).value.trim());
    $("#project-form").querySelector('button[type="submit"]').disabled = !valuesReady;
    $("#project-save-hint").hidden = matchesSaved || !$(projectFields.name).value.trim();
    $("#generate-project").disabled = !valuesReady || !openProject || stale || !matchesSaved || profileDraftOpen;
    renderPreview(profile);
  }

  function fillProject(project) {
    openProject = project;
    $("#project-form").hidden = false;
    $("#project-form-title").textContent = project ? `Edit ${project.name}` : "New rectangle project";
    $(projectFields.name).value = project ? project.name : "Untitled rectangle";
    const rect = project ? project.rectangle : { x_mm: 10, y_mm: 20, width_mm: 30, height_mm: 20 };
    $(projectFields.x_mm).value = String(rect.x_mm);
    $(projectFields.y_mm).value = String(rect.y_mm);
    $(projectFields.width_mm).value = String(rect.width_mm);
    $(projectFields.height_mm).value = String(rect.height_mm);
    $(projectFields.feed_mm_min).value = String(project ? project.operation.feed_mm_min : 600);
    $(projectFields.power_percent).value = String(project ? project.operation.power_percent : 25);
    $("#delete-project").hidden = !project;
    $("#stale-profile-warning").hidden = true;
    updateProjectValidation();
  }

  function openSavedProject(id) {
    const project = projects.find((candidate) => candidate.id === id);
    if (!project) return;
    const profile = profiles.find((candidate) => candidate.id === project.profile_id);
    if (profile) $("#profile-select").value = profile.id;
    fillProject(project);
    $("#project-form").scrollIntoView({ behavior: "smooth", block: "nearest" });
  }

  async function deleteProject(id, name) {
    const project = projects.find((candidate) => candidate.id === id);
    if (!project || !window.confirm(`Delete project “${name}”? Generated jobs remain unchanged.`)) return;
    try {
      await api(`/api/v1/projects/${encodeURIComponent(id)}`, { method: "DELETE", body: JSON.stringify({ revision: project.revision }) });
      if (openProject && openProject.id === id) { openProject = null; $("#project-form").hidden = true; }
      await refreshDesigner();
      setDesignerStatus("Project deleted. Generated jobs are unchanged.");
    } catch (error) { setDesignerError(error.message); }
  }

  async function saveProject(event) {
    event.preventDefault();
    const profile = selectedProfile();
    const error = projectValidation();
    if (!profile || error || !$(projectFields.name).value.trim()) { updateProjectValidation(); return; }
    const payload = projectPayload(profile);
    if (openProject) payload.revision = openProject.revision;
    try {
      const saved = await api(openProject ? `/api/v1/projects/${encodeURIComponent(openProject.id)}` : "/api/v1/projects", {
        method: openProject ? "PUT" : "POST", body: JSON.stringify(payload),
      });
      openProject = saved;
      $("#project-form-title").textContent = `Edit ${saved.name}`;
      $("#delete-project").hidden = false;
      await refreshDesigner();
      openProject = projects.find((project) => project.id === saved.id) || saved;
      updateProjectValidation();
      setDesignerStatus("Project saved.");
    } catch (error) { setDesignerError(error.message); }
  }

  async function generateProject() {
    if (!openProject || projectValidation() || !matchesSavedProject(selectedProfile())) { updateProjectValidation(); return; }
    try {
      const job = await api(`/api/v1/projects/${encodeURIComponent(openProject.id)}/generate`, { method: "POST", body: JSON.stringify({ revision: openProject.revision }) });
      confirmation.checked = false;
      renderJobs();
      await refresh();
      await showProgram(job.id);
      setDesignerStatus("Generated job added to the library as queued. Review it and dispatch explicitly when ready.");
    } catch (error) { setDesignerError(error.message); }
  }

  $("#new-profile").addEventListener("click", () => beginProfileEdit());
  $("#edit-profile").addEventListener("click", () => beginProfileEdit(selectedProfile()));
  $("#cancel-profile").addEventListener("click", () => { $("#profile-form").hidden = true; updateProjectValidation(); });
  $("#profile-select").addEventListener("change", () => { if (!$("#project-form").hidden) updateProjectValidation(); });
  $("#profile-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const payload = profilePayload();
    if (editingProfile) payload.revision = editingProfile.revision;
    try {
      const saved = await api(editingProfile ? `/api/v1/profiles/${encodeURIComponent(editingProfile.id)}` : "/api/v1/profiles", { method: editingProfile ? "PUT" : "POST", body: JSON.stringify(payload) });
      await refreshDesigner();
      $("#profile-select").value = saved.id;
      $("#profile-form").hidden = true;
      if (!$("#project-form").hidden) updateProjectValidation();
      setDesignerStatus("Profile saved. Confirm it matches the prepared machine before use.");
    } catch (error) { setDesignerError(error.message); }
  });
  $("#delete-profile").addEventListener("click", async () => {
    const profile = selectedProfile();
    if (!profile || !window.confirm(`Delete profile “${profile.name}”?`)) return;
    try {
      await api(`/api/v1/profiles/${encodeURIComponent(profile.id)}`, { method: "DELETE", body: JSON.stringify({ revision: profile.revision }) });
      if (openProject && openProject.profile_id === profile.id) { openProject = null; $("#project-form").hidden = true; }
      await refreshDesigner();
      setDesignerStatus("Profile deleted.");
    } catch (error) { setDesignerError(error.message); }
  });
  $("#new-project").addEventListener("click", () => fillProject(null));
  $("#project-form").addEventListener("submit", saveProject);
  $("#project-form").addEventListener("input", updateProjectValidation);
  $("#project-form").addEventListener("change", updateProjectValidation);
  $("#cancel-project").addEventListener("click", () => { openProject = null; $("#project-form").hidden = true; });
  $("#delete-project").addEventListener("click", () => { if (openProject) deleteProject(openProject.id, openProject.name); });
  $("#generate-project").addEventListener("click", generateProject);

  function renderDrivers() {
    const prior = selectedDriver;
    driverSelect.replaceChildren();
    if (!drivers.length) {
      const option = document.createElement("option");
      option.value = "";
      option.textContent = token ? "No drivers connected" : "Connect to load drivers";
      driverSelect.append(option);
      driverSelect.disabled = true;
      selectedDriver = "";
      $("#driver-status").textContent = token ? "Start a driver worker to run programs." : "Drivers are not loaded.";
      return;
    }
    for (const driver of drivers) {
      const option = document.createElement("option");
      option.value = driver.id;
      option.textContent = `${driver.id}${driver.busy ? " · busy" : " · ready"}`;
      driverSelect.append(option);
    }
    if (prior && drivers.some((driver) => driver.id === prior)) selectedDriver = prior;
    else selectedDriver = drivers[0].id;
    if (prior && !drivers.some((driver) => driver.id === prior)) {
      const gone = document.createElement("option");
      gone.value = prior;
      gone.textContent = `${prior} · disconnected`;
      driverSelect.append(gone);
      selectedDriver = prior;
    }
    driverSelect.value = selectedDriver;
    driverSelect.disabled = false;
    const selected = drivers.find((driver) => driver.id === selectedDriver);
    $("#driver-status").textContent = selected ? (selected.busy ? "Driver has an active job." : `Last seen ${new Date(selected.last_seen).toLocaleTimeString()}`) : "Selected driver is disconnected.";
  }

  async function startJob(id) {
    if (!confirmation.checked) { message("Confirm the safety check before starting a program."); confirmation.focus(); return; }
    if (!selectedDriver) { message("Select a connected driver first."); driverSelect.focus(); return; }
    try {
      await api(`/api/v1/jobs/${encodeURIComponent(id)}/start`, {
        method: "POST",
        body: JSON.stringify({ driver_id: selectedDriver, confirmed: true }),
      });
      confirmation.checked = false;
      message("Program dispatched. Keep the machine supervised.");
      await refresh();
    } catch (error) { message(error.message); }
  }

  async function cancelJob(id) {
    try {
      await api(`/api/v1/jobs/${encodeURIComponent(id)}/cancel`, { method: "POST", body: "{}" });
      message("Cancellation requested.");
      await refresh();
    } catch (error) { message(error.message); }
  }

  async function pauseJob(id) {
    try {
      await api(`/api/v1/jobs/${encodeURIComponent(id)}/pause`, { method: "POST", body: "{}" });
      message("Pause requested. Wait for the controller to confirm it is held.");
      await refresh();
    } catch (error) { message(error.message); }
  }

  async function resumeJob(id) {
    if (!confirmation.checked) { message("Confirm the safety check before resuming the program."); confirmation.focus(); return; }
    try {
      await api(`/api/v1/jobs/${encodeURIComponent(id)}/resume`, { method: "POST", body: JSON.stringify({ confirmed: true }) });
      confirmation.checked = false;
      message("Resume requested. Keep the machine supervised.");
      await refresh();
    } catch (error) { message(error.message); }
  }

  function renderJobs() {
    const checked = confirmation.checked;
    list.replaceChildren();
    $("#job-count").textContent = String(jobs.length).padStart(2, "0");
    empty.hidden = jobs.length > 0;
    if (!jobs.length) {
      empty.querySelector("strong").textContent = token ? "No programs yet" : "Your library is ready";
      empty.querySelector("span:last-child").textContent = token ? "Add a G-code file to get started." : "Connect to load saved programs.";
    }
    for (const job of jobs) {
      const item = document.createElement("li");
      item.className = "job-item";
      const info = document.createElement("div");
      info.className = "job-info";
      const title = document.createElement("strong");
      title.textContent = job.name;
      const state = document.createElement("span");
      const details = [job.state];
      if (job.driver_id) details.push(`driver ${job.driver_id}`);
      if (job.state === "running" || job.state === "canceling" || job.total > 0) details.push(`${job.acked || 0} / ${job.total || 0} lines`);
      state.textContent = details.join(" · ");
      info.append(title, state);
      if (job.controller_state) {
        const controller = document.createElement("span");
        controller.className = "controller-state";
        controller.textContent = `Last controller report: ${job.controller_state}`;
        info.append(controller);
      }
      const active = ["running", "pausing", "paused", "resuming", "canceling"].includes(job.state);
      if (active || job.total > 0) {
        const progress = document.createElement("progress");
        progress.max = Math.max(job.total || 0, 1);
        progress.value = Math.min(job.acked || 0, progress.max);
        progress.setAttribute("aria-label", `${job.acked || 0} of ${job.total || 0} lines acknowledged`);
        info.append(progress);
      }
      if (job.error) {
        const failure = document.createElement("span");
        failure.className = "job-error";
        failure.textContent = job.error;
        info.append(failure);
      }
      const actions = document.createElement("div");
      actions.className = "job-actions";
      actions.append(button("View", "button button-small", () => showProgram(job.id)));
      if (job.state === "queued") {
        const start = button("Start", "button button-small button-start", () => startJob(job.id));
        start.disabled = !checked || !selectedDriver || !drivers.some((driver) => driver.id === selectedDriver && !driver.busy);
        actions.append(start);
      }
      if (job.state === "running") {
        const assignedDriver = drivers.find((driver) => driver.id === job.driver_id);
        if (assignedDriver && Array.isArray(assignedDriver.capabilities) && assignedDriver.capabilities.includes("pause_resume")) {
          actions.append(button("Pause", "button button-small", () => pauseJob(job.id)));
        }
      }
      if (job.state === "pausing") {
        const pause = button("Pausing…", "button button-small", () => {});
        pause.disabled = true;
        actions.append(pause);
      }
      if (job.state === "paused") {
        const resume = button("Resume", "button button-small button-start", () => resumeJob(job.id));
        resume.disabled = !checked;
        actions.append(resume);
      }
      if (active) {
        const cancel = button(job.state === "canceling" ? "Canceling…" : "Cancel", "button button-small button-danger", () => cancelJob(job.id));
        cancel.disabled = job.state === "canceling";
        actions.append(cancel);
      }
      if (["queued", "failed", "completed", "canceled", "interrupted"].includes(job.state)) {
        actions.append(button("Delete", "button button-small button-danger", () => deleteJob(job.id, job.name), `Delete ${job.name}`));
      }
      item.append(info, actions);
      list.append(item);
    }
    confirmation.checked = checked;
  }

  async function refresh() {
    if (!token || polling) return;
    polling = true;
    try {
      const results = await Promise.all([api("/api/v1/jobs"), api("/api/v1/drivers")]);
      jobs = results[0];
      drivers = results[1];
      $("#connection-state").textContent = "Connected · library synced";
      $("#connection-state").classList.add("connected");
      renderDrivers();
      renderJobs();
      await refreshDesigner();
    } catch (error) {
      $("#connection-state").textContent = "Connection failed";
      $("#connection-state").classList.remove("connected");
      message(error.message);
    } finally { polling = false; }
  }

  function startPolling() {
    if (pollTimer === null) pollTimer = window.setInterval(refresh, 2000);
  }

  async function showProgram(id) {
    try {
      const job = await api(`/api/v1/jobs/${encodeURIComponent(id)}`);
      shownJobID = id;
      $("#program-title").textContent = job.name;
      $("#program-code").textContent = job.gcode;
      const provenance = $("#program-provenance");
      provenance.replaceChildren();
      if (job.provenance) {
        const source = job.provenance.project || {};
        const profile = job.provenance.profile || {};
        const rectangle = source.rectangle || {};
        const operation = source.operation || {};
        const details = [
          "Project snapshot: " + (source.name || "saved project") + (source.revision ? " · revision " + source.revision : ""),
          "Profile snapshot: " + (profile.name || "declared profile") + (profile.revision ? " · revision " + profile.revision : "") + " · " + (profile.width_mm ?? "?") + " × " + (profile.height_mm ?? "?") + " " + (profile.units || "mm") + " · " + (profile.origin || "lower-left") + " origin · $30=" + (profile.max_s ?? "?") + " · $31=" + (profile.min_s ?? "?") + " · $32=1 · feed " + (profile.min_feed_mm_min ?? "?") + "–" + (profile.max_feed_mm_min ?? "?") + " mm/min · max commanded power " + (profile.max_power_percent ?? "?") + "%",
          "Operation snapshot: " + (operation.type || "vector engraving") + " · rectangle X" + (rectangle.x_mm ?? "?") + " Y" + (rectangle.y_mm ?? "?") + " mm · " + (rectangle.width_mm ?? "?") + " × " + (rectangle.height_mm ?? "?") + " mm · feed " + (operation.feed_mm_min ?? "?") + " mm/min · commanded power " + (operation.power_percent ?? "?") + "%",
          "Generator version: " + (job.provenance.generator_version || "unknown") + ". The generated program and source snapshots are immutable.",
        ];
        for (const detail of details) {
          const line = document.createElement("p");
          line.textContent = detail;
          provenance.append(line);
        }
        provenance.hidden = false;
      } else provenance.hidden = true;
      programPanel.hidden = false;
      programPanel.scrollIntoView({ behavior: "smooth", block: "start" });
      message("");
    } catch (error) { message(error.message); }
  }

  async function deleteJob(id, name) {
    if (!window.confirm(`Delete “${name}” from the library?`)) return;
    try {
      await api(`/api/v1/jobs/${encodeURIComponent(id)}`, { method: "DELETE" });
      if (shownJobID === id) {
        shownJobID = "";
        programPanel.hidden = true;
      }
      await refresh();
      message("Program deleted.");
    } catch (error) { message(error.message); }
  }

  authForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    token = keyInput.value.trim();
    sessionStorage.setItem("emberdeck-api-key", token);
    await refresh();
    startPolling();
  });

  fileInput.addEventListener("change", () => {
    const file = fileInput.files && fileInput.files[0];
    if (!file) return;
    $("#file-label").textContent = file.name;
    if (!jobName.value) jobName.value = file.name;
  });

  uploadForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!token) { message("Connect with your API key before adding a program."); keyInput.focus(); return; }
    const file = fileInput.files && fileInput.files[0];
    if (!file) return;
    try {
      const gcode = await file.text();
      await api("/api/v1/jobs", { method: "POST", body: JSON.stringify({ name: jobName.value, gcode }) });
      uploadForm.reset();
      $("#file-label").textContent = "Choose a G-code file";
      await refresh();
      message("Program added to the library.");
    } catch (error) { message(error.message); }
  });

  driverSelect.addEventListener("change", () => {
    selectedDriver = driverSelect.value;
    renderDrivers();
    renderJobs();
  });
  confirmation.addEventListener("change", renderJobs);
  $("#refresh").addEventListener("click", refresh);
  $("#close-program").addEventListener("click", () => { shownJobID = ""; programPanel.hidden = true; });
  if (token) {
    keyInput.value = token;
    refresh();
    startPolling();
  }
  renderDrivers();
  renderJobs();
})();
