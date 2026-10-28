"use strict";

(() => {
  const $ = (selector) => document.querySelector(selector);
  const keyInput = $("#api-key");
  const authForm = $("#auth-form");
  const connectButton = $("#connect-button");
  const connectionState = $("#connection-state");
  const connectionControls = $("#connection-controls");
  const uploadForm = $("#upload-form");
  const fileInput = $("#job-file");
  const jobName = $("#job-name");
  const uploadProfileSelect = $("#upload-profile-select");
  const list = $("#job-list");
  const notice = $("#notice");
  const empty = $("#empty-state");
  const programPanel = $("#program-panel");
  const driverSelect = $("#driver-select");
  const confirmation = $("#operator-confirmation");
  const actionDialog = $("#action-dialog");
  let pendingAction = null;
  let token = "";
  let connected = false;
  let jobs = [];
  let hasLoadedJobs = false;
  // GET /jobs returns one page of metadata; program bytes come from GET /jobs/{id}.
  const jobPageSize = 20;
  let jobTotal = 0;
  let jobOffset = 0;
  let refreshQueued = false;
  const jobFilterName = $("#job-filter-name");
  const jobFilterState = $("#job-filter-state");
  let drivers = [];
  let selectedDriver = "";
  let shownJobID = "";
  let polling = false;
  let pollTimer = null;
  let sessionVersion = 0;
  let profiles = [];
  let projects = [];
  let editingProfile = null;
  let openProject = null;
  let openVectorProject = null;
  let vectorObjects = [];
  let vectorOperations = [];
  let selectedVectorObject = "";
  let selectedVectorObjects = new Set();
  let vectorIDSequence = 0;
  const toolpathCache = new Map();
  const toolpathViewports = new Map();
  const boundsOverrides = new Set();

  function message(text) {
    notice.textContent = text;
    notice.hidden = !text;
  }

  function setConnecting(isConnecting) {
    connectButton.disabled = isConnecting;
    $("#connect-label").textContent = isConnecting ? "Connecting…" : "Connect";
    $("#connect-arrow").style.display = isConnecting ? "none" : "";
  }

  function setSessionConnected(isConnected) {
    connected = isConnected;
    authForm.hidden = isConnected;
    connectionState.hidden = isConnected;
    connectionControls.hidden = !isConnected;
    connectionState.textContent = isConnected ? "Connected · library synced" : "Not connected";
    $("#upload-form").querySelectorAll("input, button").forEach((control) => { control.disabled = !isConnected; });
    $("#refresh").disabled = !isConnected;
    $("#driver-select").disabled = !isConnected || !drivers.length;
    confirmation.disabled = !isConnected;
    jobFilterName.disabled = !isConnected;
    jobFilterState.disabled = !isConnected;
    $("#new-profile").disabled = !isConnected;
    $("#new-project").disabled = !isConnected || !selectedProfile();
    $("#new-vector-project").disabled = !isConnected || !selectedProfile();
    $("#vector-svg-file").disabled = !isConnected;
  }

  function stopPolling() {
    if (pollTimer !== null) window.clearInterval(pollTimer);
    pollTimer = null;
    polling = false;
  }

  function forgetSession({ focus = false, clearInput = true } = {}) {
    token = "";
    sessionVersion++;
    stopPolling();
    sessionStorage.removeItem("emberdeck-api-key");
    setSessionConnected(false);
    jobs = [];
    hasLoadedJobs = false;
    jobTotal = 0;
    jobOffset = 0;
    refreshQueued = false;
    drivers = [];
    selectedDriver = "";
    toolpathCache.clear();
    toolpathViewports.clear();
    profiles = [];
    projects = [];
    editingProfile = null;
    openProject = null;
    openVectorProject = null;
    shownJobID = "";
    pendingAction = null;
    if (actionDialog.open) actionDialog.close();
    programPanel.hidden = true;
    $("#profile-form").hidden = true;
    $("#project-form").hidden = true;
    $("#vector-form").hidden = true;
    if (clearInput) keyInput.value = "";
    renderDrivers();
    renderJobs();
    renderProfiles();
    setDesignerStatus("Connect to load saved profiles and projects.");
    if (focus) keyInput.focus();
  }

  async function api(path, options = {}) {
    const requestToken = token;
    const headers = new Headers(options.headers || {});
    headers.set("Authorization", `Bearer ${requestToken}`);
    if (options.body) headers.set("Content-Type", "application/json");
    const response = await fetch(path, { ...options, headers });
    if (!response.ok) {
      if (response.status === 401 && token === requestToken) forgetSession({ focus: true, clearInput: false });
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
    const iconPaths = {
      "↑": "M12 19V5m-6 6 6-6 6 6",
      "↓": "M12 5v14m-6-6 6 6 6-6",
      "×": "m6 6 12 12M18 6 6 18",
      "−": "M5 12h14",
      "+": "M12 5v14M5 12h14",
    };
    if (iconPaths[label]) {
      const icon = document.createElementNS(svgNS, "svg");
      icon.setAttribute("class", "button-icon");
      icon.setAttribute("viewBox", "0 0 24 24");
      icon.setAttribute("aria-hidden", "true");
      icon.setAttribute("focusable", "false");
      const path = document.createElementNS(svgNS, "path");
      path.setAttribute("d", iconPaths[label]);
      path.setAttribute("fill", "none");
      path.setAttribute("stroke", "currentColor");
      path.setAttribute("stroke-width", "2");
      path.setAttribute("stroke-linecap", "round");
      path.setAttribute("stroke-linejoin", "round");
      icon.append(path);
      element.append(icon);
    } else {
      element.textContent = label;
    }
    if (ariaLabel) element.setAttribute("aria-label", ariaLabel);
    // aria-disabled keeps an unavailable control focusable, so its click must do nothing.
    element.addEventListener("click", (event) => {
      if (element.getAttribute("aria-disabled") === "true") { event.preventDefault(); return; }
      action(event);
    });
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
    select.disabled = !connected || !profiles.length;
    if (profiles.some((profile) => profile.id === prior)) select.value = prior;
    else if (profiles.length) select.value = profiles[0].id;
    const hasProfile = Boolean(selectedProfile());
    $("#new-profile").disabled = !connected;
    renderUploadProfiles();
    $("#edit-profile").disabled = !connected || !hasProfile;
    $("#delete-profile").disabled = !connected || !hasProfile;
    $("#new-project").disabled = !connected || !hasProfile;
    $("#new-vector-project").disabled = !connected || !hasProfile;
    renderProjects();
    renderVectorProjects();
    updateProjectValidation();
    updateVectorValidation();
  }

  function renderUploadProfiles() {
    const prior = uploadProfileSelect.value;
    uploadProfileSelect.replaceChildren();
    const none = document.createElement("option");
    none.value = "";
    none.textContent = "No profile selected · bounds unknown";
    uploadProfileSelect.append(none);
    for (const profile of profiles) {
      const option = document.createElement("option");
      option.value = profile.id;
      option.textContent = `${profile.name} · r${profile.revision}`;
      uploadProfileSelect.append(option);
    }
    if (profiles.some((profile) => profile.id === prior)) uploadProfileSelect.value = prior;
    else if (profiles.some((profile) => profile.id === $("#profile-select").value)) uploadProfileSelect.value = $("#profile-select").value;
    else uploadProfileSelect.value = "";
  }

  function renderProjects() {
    const list = $("#project-list");
    list.replaceChildren();
    for (const project of projects.filter((candidate) => candidate.schema_version === 1)) {
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
    if (!connected) return;
    const version = sessionVersion;
    try {
      const [nextProfiles, nextProjects] = await Promise.all([api("/api/v1/profiles"), api("/api/v1/projects")]);
      if (!connected || version !== sessionVersion) return;
      profiles = Array.isArray(nextProfiles) ? nextProfiles : [];
      projects = Array.isArray(nextProjects) ? nextProjects : [];
      renderProfiles();
      setDesignerStatus(`${profiles.length} profile${profiles.length === 1 ? "" : "s"} · ${projects.length} saved project${projects.length === 1 ? "" : "s"}`);
    } catch (error) {
      if (!connected || version !== sessionVersion) return;
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
    return /^-?\d+(?:\.\d{1,3})?$/.test(value);
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

  function vectorID(prefix) {
    vectorIDSequence += 1;
    return `${prefix}-${Date.now().toString(36)}-${vectorIDSequence}`;
  }

  function currentVectorPayload(profile) {
    return {
      schema_version: 2,
      name: $("#vector-name").value.trim(),
      units: "mm",
      profile_id: profile.id,
      profile_revision: profile.revision,
      objects: vectorObjects.map(vectorObjectPayload),
      operations: vectorOperations.map(({ id, type, feed_mm_min, power_percent }) => ({ id, type, feed_mm_min, power_percent })),
    };
  }

  function vectorObjectPayload(object) {
    const payload = { id: object.id, kind: objectKind(object), ...(object.operation_id ? { operation_id: object.operation_id } : {}), subpaths: object.subpaths };
    if (object.transform && !isIdentityTransform(object.transform)) payload.transform = normalizedTransform(object.transform);
    return payload;
  }

  function normalizedTransform(transform = {}) {
    return {
      translate_x_mm: transform.translate_x_mm ?? 0,
      translate_y_mm: transform.translate_y_mm ?? 0,
      scale_x: transform.scale_x ?? 1,
      scale_y: transform.scale_y ?? 1,
      rotation_deg: transform.rotation_deg ?? 0,
    };
  }

  function isIdentityTransform(transform) {
    const value = normalizedTransform(transform);
    return value.translate_x_mm === 0 && value.translate_y_mm === 0 && value.scale_x === 1 && value.scale_y === 1 && value.rotation_deg === 0;
  }

  function objectKind(object) { return object.kind || "path"; }

  function boundsFor(object) {
    const points = object.subpaths.flatMap((path) => path.points);
    return {
      x: Math.min(...points.map((point) => point.x_mm)),
      y: Math.min(...points.map((point) => point.y_mm)),
      width: Math.max(...points.map((point) => point.x_mm)) - Math.min(...points.map((point) => point.x_mm)),
      height: Math.max(...points.map((point) => point.y_mm)) - Math.min(...points.map((point) => point.y_mm)),
    };
  }

  function transformedSubpaths(object) {
    const transform = normalizedTransform(object.transform);
    const points = object.subpaths.flatMap((path) => path.points);
    const centerX = (Math.min(...points.map((point) => point.x_mm)) + Math.max(...points.map((point) => point.x_mm))) / 2;
    const centerY = (Math.min(...points.map((point) => point.y_mm)) + Math.max(...points.map((point) => point.y_mm))) / 2;
    const angle = transform.rotation_deg * Math.PI / 180;
    const cos = Math.cos(angle), sin = Math.sin(angle);
    const round = (value) => Math.round(value * 1000) / 1000;
    return object.subpaths.map((path) => ({
      ...path,
      points: path.points.map((point) => {
        const x = (point.x_mm - centerX) * transform.scale_x;
        const y = (point.y_mm - centerY) * transform.scale_y;
        return {
          x_mm: round(centerX + x * cos - y * sin + transform.translate_x_mm),
          y_mm: round(centerY + x * sin + y * cos + transform.translate_y_mm),
        };
      }),
    }));
  }

  function transformedBounds(object) {
    const points = transformedSubpaths(object).flatMap((path) => path.points);
    const xs = points.map((point) => point.x_mm), ys = points.map((point) => point.y_mm);
    const left = Math.min(...xs), right = Math.max(...xs), bottom = Math.min(...ys), top = Math.max(...ys);
    return { left, right, bottom, top, centerX: (left + right) / 2, centerY: (bottom + top) / 2 };
  }

  function updateSelectedTransform() {
    const object = vectorObjects.find((candidate) => candidate.id === selectedVectorObject);
    if (!object) return;
    const values = ["#vector-translate-x", "#vector-translate-y", "#vector-scale-x", "#vector-scale-y", "#vector-rotation"].map(finiteField);
    if (values.some((value) => value === null)) return updateVectorValidation();
    const [translate_x_mm, translate_y_mm, scale_x, scale_y, rotation_deg] = values;
    object.transform = { translate_x_mm, translate_y_mm, scale_x, scale_y, rotation_deg };
    if (isIdentityTransform(object.transform)) delete object.transform;
    updateVectorValidation();
  }

  function shapePoints(type, x, y, width, height) {
    const point = (x_mm, y_mm) => ({ x_mm: Math.round(x_mm * 1000) / 1000, y_mm: Math.round(y_mm * 1000) / 1000 });
    if (type === "line") return [point(x, y), point(x + width, y + height)];
    if (type === "ellipse") {
      return Array.from({ length: 32 }, (_, index) => {
        const angle = (index * 2 * Math.PI) / 32;
        return point(x + width / 2 + Math.cos(angle) * width / 2, y + height / 2 + Math.sin(angle) * height / 2);
      });
    }
    return [point(x, y), point(x + width, y), point(x + width, y + height), point(x, y + height)];
  }

  function pathHasArea(points) {
    let twiceArea = 0;
    points.forEach((point, index) => {
      const next = points[(index + 1) % points.length];
      twiceArea += milli(point.x_mm) * milli(next.y_mm) - milli(next.x_mm) * milli(point.y_mm);
    });
    return twiceArea !== 0;
  }

  function renderVectorObjectList() {
    const list = $("#vector-object-list");
    list.replaceChildren();
    vectorObjects.forEach((object, index) => {
      const row = document.createElement("li");
      row.className = object.id === selectedVectorObject ? "selected" : "";
      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.className = "vector-alignment-select";
      checkbox.checked = selectedVectorObjects.has(object.id);
      checkbox.setAttribute("aria-label", `Select shape ${index + 1} ${objectKind(object)} for alignment`);
      checkbox.addEventListener("change", () => {
        if (checkbox.checked) {
          selectedVectorObjects.add(object.id);
          selectedVectorObject = object.id;
        } else {
          selectedVectorObjects.delete(object.id);
          if (selectedVectorObject === object.id) selectedVectorObject = [...selectedVectorObjects][0] || vectorObjects[0]?.id || "";
        }
        renderVectorEditor();
      });
      const select = button(`${index + 1}. ${objectKind(object)}`, "vector-select", () => {
        selectedVectorObject = object.id;
        if (!selectedVectorObjects.has(object.id)) selectedVectorObjects = new Set([object.id]);
        renderVectorEditor();
      });
      const assigned = vectorOperations.find((operation) => operation.id === object.operation_id);
      const detail = document.createElement("span");
      detail.textContent = assigned ? assigned.type.replace("vector-", "") : "Unassigned";
      const remove = button("Remove", "button button-small button-danger", () => {
        vectorObjects = vectorObjects.filter((candidate) => candidate.id !== object.id);
        selectedVectorObjects.delete(object.id);
        selectedVectorObject = vectorObjects[0]?.id || "";
        renderVectorEditor();
      }, `Remove shape ${index + 1}`);
      row.append(checkbox, select, detail, remove);
      list.append(row);
    });
    renderVectorAlignmentControls();
    const object = vectorObjects.find((candidate) => candidate.id === selectedVectorObject);
    $("#vector-geometry").hidden = !object;
    $("#vector-transform").hidden = !object;
    if (!object) return;
    const bounds = boundsFor(object);
    $("#vector-x").value = bounds.x.toFixed(3).replace(/0+$/, "").replace(/\.$/, "");
    $("#vector-y").value = bounds.y.toFixed(3).replace(/0+$/, "").replace(/\.$/, "");
    $("#vector-width").value = Math.max(0.001, bounds.width).toFixed(3).replace(/0+$/, "").replace(/\.$/, "");
    $("#vector-height").value = Math.max(0.001, bounds.height).toFixed(3).replace(/0+$/, "").replace(/\.$/, "");
    const transform = normalizedTransform(object.transform);
    $("#vector-translate-x").value = String(transform.translate_x_mm);
    $("#vector-translate-y").value = String(transform.translate_y_mm);
    $("#vector-scale-x").value = String(transform.scale_x);
    $("#vector-scale-y").value = String(transform.scale_y);
    $("#vector-rotation").value = String(transform.rotation_deg);
    const assignment = $("#vector-object-operation");
    assignment.replaceChildren(new Option("Unassigned", ""));
    vectorOperations.forEach((operation, index) => assignment.append(new Option(`${index + 1}. ${operation.type.replace("vector-", "")}`, operation.id)));
    assignment.value = object.operation_id || "";
  }

  function renderVectorAlignmentControls() {
    const canAlign = selectedVectorObjects.size >= 2;
    document.querySelectorAll("[data-align]").forEach((control) => { control.disabled = !canAlign; });
  }

  function alignSelectedVectorObjects(alignment) {
    const selected = vectorObjects.filter((object) => selectedVectorObjects.has(object.id));
    if (selected.length < 2) return;
    const bounds = selected.map(transformedBounds);
    const left = Math.min(...bounds.map((box) => box.left));
    const right = Math.max(...bounds.map((box) => box.right));
    const bottom = Math.min(...bounds.map((box) => box.bottom));
    const top = Math.max(...bounds.map((box) => box.top));
    const round = (value) => Math.round(value * 1000) / 1000;
    selected.forEach((object, index) => {
      const box = bounds[index];
      const transform = normalizedTransform(object.transform);
      if (alignment === "left") transform.translate_x_mm = round(transform.translate_x_mm + left - box.left);
      else if (alignment === "center-x") transform.translate_x_mm = round(transform.translate_x_mm + (left + right) / 2 - box.centerX);
      else if (alignment === "right") transform.translate_x_mm = round(transform.translate_x_mm + right - box.right);
      else if (alignment === "top") transform.translate_y_mm = round(transform.translate_y_mm + top - box.top);
      else if (alignment === "center-y") transform.translate_y_mm = round(transform.translate_y_mm + (bottom + top) / 2 - box.centerY);
      else if (alignment === "bottom") transform.translate_y_mm = round(transform.translate_y_mm + bottom - box.bottom);
      object.transform = transform;
      if (isIdentityTransform(transform)) delete object.transform;
    });
    const names = { left: "left", "center-x": "horizontal centers", right: "right", top: "top", "center-y": "vertical centers", bottom: "bottom" };
    $("#vector-align-status").textContent = `${selected.length} shapes aligned ${names[alignment]}.`;
    renderVectorEditor();
  }

  function renderVectorOperations() {
    const list = $("#vector-operation-list");
    list.replaceChildren();
    vectorOperations.forEach((operation, index) => {
      const row = document.createElement("li");
      row.className = "layer-row";
      const number = document.createElement("span");
      number.className = "layer-number";
      number.textContent = String(index + 1).padStart(2, "0");
      const type = document.createElement("select");
      type.setAttribute("aria-label", `Operation ${index + 1} type`);
      [["vector-engrave", "Engrave"], ["vector-cut", "Cut"]].forEach(([value, label]) => type.append(new Option(label, value)));
      type.value = operation.type;
      type.addEventListener("change", () => { operation.type = type.value; renderVectorEditor(); });
      const feed = document.createElement("input");
      feed.type = "number"; feed.min = "0.001"; feed.step = "0.001"; feed.value = operation.feed_mm_min;
      feed.setAttribute("aria-label", `Operation ${index + 1} feed (mm/min)`);
      feed.addEventListener("input", () => { operation.feed_mm_min = Number(feed.value); updateVectorValidation(); });
      const power = document.createElement("input");
      power.type = "number"; power.min = "0.001"; power.max = "100"; power.step = "0.001"; power.value = operation.power_percent;
      power.setAttribute("aria-label", `Operation ${index + 1} power (%)`);
      power.addEventListener("input", () => { operation.power_percent = Number(power.value); updateVectorValidation(); });
      const up = button("↑", "button button-small", () => moveVectorOperation(index, -1), `Move operation ${index + 1} up`);
      up.disabled = index === 0;
      const down = button("↓", "button button-small", () => moveVectorOperation(index, 1), `Move operation ${index + 1} down`);
      down.disabled = index === vectorOperations.length - 1;
      const remove = button("×", "button button-small button-danger", () => {
        const removed = vectorOperations.splice(index, 1)[0];
        vectorObjects.forEach((object) => { if (object.operation_id === removed.id) delete object.operation_id; });
        renderVectorEditor();
      }, `Delete operation ${index + 1}`);
      row.append(number, type, feed, power, up, down, remove);
      list.append(row);
    });
  }

  function moveVectorOperation(index, direction) {
    const target = index + direction;
    if (target < 0 || target >= vectorOperations.length) return;
    [vectorOperations[index], vectorOperations[target]] = [vectorOperations[target], vectorOperations[index]];
    renderVectorEditor();
  }

  function renderVectorPreview(profile) {
    const content = $("#vector-preview-content");
    content.replaceChildren();
    const W = Number(profile?.width_mm), H = Number(profile?.height_mm);
    if (!(W > 0 && H > 0)) return;
    const scale = Math.min(550 / W, 290 / H), left = 35, bottom = 325;
    content.append(svgElement("rect", { class: "workspace-shape", x: left, y: bottom - H * scale, width: W * scale, height: H * scale }));
    content.append(svgElement("text", { x: left + 4, y: bottom - H * scale + 14 }, `Declared workspace · ${W} × ${H} mm`));
    vectorObjects.forEach((object) => {
      const operation = vectorOperations.find((candidate) => candidate.id === object.operation_id);
      const commands = [];
      transformedSubpaths(object).forEach((path) => {
        path.points.forEach((point, index) => commands.push(`${index ? "L" : "M"} ${left + point.x_mm * scale} ${bottom - point.y_mm * scale}`));
        if (path.closed) commands.push("Z");
      });
      const path = svgElement("path", { class: `vector-path ${operation ? (operation.type === "vector-cut" ? "cut-path" : "engrave-path") : "unassigned-path"}`, d: commands.join(" ") });
      if (selectedVectorObjects.has(object.id)) path.classList.add("selected-vector-path");
      if (!operation) {
        path.style.stroke = "var(--text-muted)";
        path.style.strokeDasharray = "2 3";
      }
      content.append(path);
    });
    content.append(svgElement("text", { x: left, y: 349 }, "Engrave · green   Cut · red dashed   Unassigned · gray dotted"));
  }

  function vectorValidation() {
    const profile = selectedProfile();
    if (!profile) return "Select or create a declared profile first.";
    if (!$("#vector-name").value.trim()) return "Enter a project name.";
    if (!vectorObjects.length) return "Add at least one shape.";
    if (vectorObjects.length > 500 || vectorOperations.length > 100) return "The project exceeds the supported shape or operation count.";
    if (vectorObjects.some((object) => object.id === selectedVectorObject)) {
      const geometry = ["#vector-x", "#vector-y", "#vector-width", "#vector-height"].map(finiteField);
      if (geometry.some((value) => value === null)) return "Enter finite position and size values with at most three decimal places.";
      if (geometry[0] < 0 || geometry[1] < 0 || geometry[2] <= 0 || geometry[3] <= 0) return "Position must be nonnegative and shape width and height must be positive.";
      const transform = ["#vector-translate-x", "#vector-translate-y", "#vector-scale-x", "#vector-scale-y", "#vector-rotation"].map(finiteField);
      if (transform.some((value) => value === null)) return "Enter finite transform values with at most three decimal places.";
      if (transform[2] < 0.01 || transform[2] > 100 || transform[3] < 0.01 || transform[3] > 100 || transform[4] < -360 || transform[4] > 360) return "Scale must be from 0.01 to 100 and rotation from −360 to 360 degrees.";
    }
    for (const operation of vectorOperations) {
      if (!Number.isFinite(operation.feed_mm_min) || operation.feed_mm_min < profile.min_feed_mm_min || operation.feed_mm_min > profile.max_feed_mm_min) return "Operation feed is outside the selected profile range.";
      if (!Number.isFinite(operation.power_percent) || operation.power_percent <= 0 || operation.power_percent > profile.max_power_percent) return "Operation power is outside the selected profile limit.";
      if (!decimalPlacesAtMostThree(String(operation.feed_mm_min)) || !decimalPlacesAtMostThree(String(operation.power_percent))) return "Operation values must use at most three decimal places.";
      if (Math.floor((profile.max_s * milli(operation.power_percent) + 50000) / 100000) < 1) return "An operation power rounds to S0; increase commanded power before saving.";
    }
    for (const object of vectorObjects) {
      if (!object.subpaths.length || object.subpaths.some((path) => path.points.length < (path.closed ? 3 : 2))) return "Each shape needs a valid path.";
      for (const point of object.subpaths.flatMap((path) => path.points)) {
        if (![point.x_mm, point.y_mm].every(Number.isFinite) || !decimalPlacesAtMostThree(String(point.x_mm)) || !decimalPlacesAtMostThree(String(point.y_mm)) || point.x_mm < 0 || point.y_mm < 0 || point.x_mm > 10000 || point.y_mm > 10000) return "A shape has invalid source coordinates or unsupported coordinate precision.";
      }
      const transformed = transformedSubpaths(object);
      for (const point of transformed.flatMap((path) => path.points)) {
        if (![point.x_mm, point.y_mm].every(Number.isFinite) || point.x_mm < 0 || point.y_mm < 0 || milli(point.x_mm) > milli(profile.width_mm) || milli(point.y_mm) > milli(profile.height_mm)) return "A transformed shape is outside the declared workspace.";
      }
      if (object.operation_id) {
        const operation = vectorOperations.find((candidate) => candidate.id === object.operation_id);
        if (!operation) return "A shape refers to a missing operation layer.";
        if (operation.type === "vector-cut" && transformed.some((path) => !path.closed || !pathHasArea(path.points))) return "Cut operations require closed shapes with nonzero area. Assign this path to an engraving layer or change its geometry.";
      }
    }
    return "";
  }

  function vectorMatchesSaved(profile) {
    if (!openVectorProject || !profile) return false;
    return JSON.stringify(currentVectorPayload(profile)) === JSON.stringify({
      schema_version: 2, name: openVectorProject.name, units: openVectorProject.units, profile_id: openVectorProject.profile_id,
      profile_revision: openVectorProject.profile_revision,
      objects: openVectorProject.objects.map(vectorObjectPayload),
      operations: openVectorProject.operations,
    });
  }

  function updateVectorValidation() {
    if ($("#vector-form").hidden) return;
    const profile = selectedProfile();
    const error = vectorValidation();
    const stale = Boolean(openVectorProject && profile && (openVectorProject.profile_id !== profile.id || openVectorProject.profile_revision !== profile.revision));
    const warning = $("#vector-validation");
    warning.textContent = stale ? "Profile revision changed. Review this project against the current declared workspace and save it before generating." : error;
    warning.hidden = !stale && !error;
    const saveButton = $("#vector-form").querySelector('button[type="submit"]');
    saveButton.disabled = Boolean(error) || !$("#profile-form").hidden;
    const complete = vectorObjects.every((object) => object.operation_id && vectorOperations.some((operation) => operation.id === object.operation_id));
    const matches = vectorMatchesSaved(profile);
    $("#vector-save-hint").hidden = matches;
    $("#generate-vector-project").disabled = Boolean(error) || !openVectorProject || stale || !matches || !complete || !$("#profile-form").hidden;
    renderVectorPreview(profile);
  }

  function renderVectorEditor() {
    renderVectorObjectList();
    renderVectorOperations();
    updateVectorValidation();
  }

  function fillVectorProject(project = null) {
    openVectorProject = project;
    vectorObjects = project ? project.objects.map((object) => ({ ...object, kind: objectKind(object), transform: object.transform ? { ...object.transform } : undefined, subpaths: object.subpaths.map((path) => ({ ...path, points: path.points.map((point) => ({ ...point })) })) })) : [];
    vectorOperations = project ? project.operations.map((operation) => ({ ...operation })) : [{ id: vectorID("operation"), type: "vector-engrave", feed_mm_min: 600, power_percent: 25 }];
    selectedVectorObject = vectorObjects[0]?.id || "";
    selectedVectorObjects = new Set(selectedVectorObject ? [selectedVectorObject] : []);
    $("#vector-form").hidden = false;
    $("#vector-name").value = project ? project.name : "Untitled vector design";
    $("#vector-form-title").textContent = project ? `Edit ${project.name}` : "New multi-path vector project";
    $("#delete-vector-project").hidden = !project;
    renderVectorEditor();
  }

  function addVectorShape(type) {
    const profile = selectedProfile();
    if (!profile) return;
    const x = Math.min(10 + vectorObjects.length * 5, Math.max(0, profile.width_mm - 20));
    const y = Math.min(10 + vectorObjects.length * 5, Math.max(0, profile.height_mm - 20));
    const width = Math.min(30, profile.width_mm - x), height = Math.min(20, profile.height_mm - y);
    const closed = type !== "line";
    const object = { id: vectorID("shape"), kind: type, subpaths: [{ points: shapePoints(type, x, y, width, height), closed }] };
    if (vectorOperations[0]) object.operation_id = vectorOperations[0].id;
    vectorObjects.push(object);
    selectedVectorObject = object.id;
    selectedVectorObjects = new Set([object.id]);
    renderVectorEditor();
  }

  function updateSelectedGeometry() {
    const object = vectorObjects.find((candidate) => candidate.id === selectedVectorObject);
    if (!object) return;
    const vals = ["#vector-x", "#vector-y", "#vector-width", "#vector-height"].map(finiteField);
    if (vals.some((value) => value === null) || vals[2] <= 0 || vals[3] <= 0) return updateVectorValidation();
    const [x, y, width, height] = vals;
    const kind = objectKind(object);
    if (kind === "path") {
      const previous = boundsFor(object);
      const round = (value) => Math.round(value * 1000) / 1000;
      object.subpaths = object.subpaths.map((path) => ({
        ...path,
        points: path.points.map((point) => ({
          x_mm: round(previous.width > 0 ? x + (point.x_mm - previous.x) * width / previous.width : x + width / 2),
          y_mm: round(previous.height > 0 ? y + (point.y_mm - previous.y) * height / previous.height : y + height / 2),
        })),
      }));
    } else {
      object.subpaths = [{ points: shapePoints(kind, x, y, width, height), closed: object.subpaths[0].closed }];
    }
    updateVectorValidation();
  }

  function renderVectorProjects() {
    const list = $("#vector-project-list");
    list.replaceChildren();
    projects.filter((project) => project.schema_version === 2).forEach((project) => {
      const row = document.createElement("li");
      const label = document.createElement("strong");
      label.textContent = `${project.name} · r${project.revision} · ${project.objects.length} shapes`;
      const actions = document.createElement("div"); actions.className = "designer-actions";
      actions.append(button(`Reopen vector ${project.name}`, "button button-small", () => {
        const profile = profiles.find((candidate) => candidate.id === project.profile_id);
        if (profile) $("#profile-select").value = profile.id;
        fillVectorProject(project);
        $("#vector-form").scrollIntoView({ behavior: "smooth", block: "nearest" });
      }));
      actions.append(button("Delete", "button button-small button-danger", () => deleteVectorProject(project.id, project.name), `Delete vector project ${project.name}`));
      row.append(label, actions); list.append(row);
    });
  }

  async function saveVectorProject(event) {
    event.preventDefault();
    const profile = selectedProfile();
    if (!profile || vectorValidation()) { updateVectorValidation(); return; }
    const payload = currentVectorPayload(profile);
    if (openVectorProject) payload.revision = openVectorProject.revision;
    try {
      const saved = await api(openVectorProject ? `/api/v1/projects/${encodeURIComponent(openVectorProject.id)}` : "/api/v1/projects", {
        method: openVectorProject ? "PUT" : "POST", body: JSON.stringify(payload),
      });
      await refreshDesigner();
      openVectorProject = projects.find((project) => project.id === saved.id) || saved;
      vectorObjects = openVectorProject.objects.map((object) => ({ ...object, kind: objectKind(object), transform: object.transform ? { ...object.transform } : undefined }));
      vectorOperations = openVectorProject.operations.map((operation) => ({ ...operation }));
      $("#vector-form-title").textContent = `Edit ${saved.name}`;
      $("#delete-vector-project").hidden = false;
      renderVectorEditor();
      setDesignerStatus("Vector project saved.");
    } catch (error) { setDesignerError(error.message); }
  }

  async function deleteVectorProject(id, name) {
    const project = projects.find((candidate) => candidate.id === id);
    if (!project || !window.confirm(`Delete vector project “${name}”? Generated jobs remain unchanged.`)) return;
    try {
      await api(`/api/v1/projects/${encodeURIComponent(id)}`, { method: "DELETE", body: JSON.stringify({ revision: project.revision }) });
      if (openVectorProject?.id === id) { openVectorProject = null; $("#vector-form").hidden = true; }
      await refreshDesigner();
      setDesignerStatus("Vector project deleted. Generated jobs are unchanged.");
    } catch (error) { setDesignerError(error.message); }
  }

  async function generateVectorProject() {
    if (!openVectorProject || !vectorMatchesSaved(selectedProfile()) || vectorValidation()) return updateVectorValidation();
    try {
      const job = await api(`/api/v1/projects/${encodeURIComponent(openVectorProject.id)}/generate`, { method: "POST", body: JSON.stringify({ revision: openVectorProject.revision }) });
      confirmation.checked = false;
      await refresh();
      await showProgram(job.id);
      setDesignerStatus("Generated job added to the library as queued. Review it and dispatch explicitly when ready.");
    } catch (error) { setDesignerError(error.message); }
  }

  $("#new-profile").addEventListener("click", () => beginProfileEdit());
  $("#edit-profile").addEventListener("click", () => beginProfileEdit(selectedProfile()));
  $("#cancel-profile").addEventListener("click", () => { $("#profile-form").hidden = true; updateProjectValidation(); });
  $("#profile-select").addEventListener("change", () => { if (!$("#project-form").hidden) updateProjectValidation(); });
  $("#profile-select").addEventListener("change", updateVectorValidation);
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
      if (!$("#vector-form").hidden) updateVectorValidation();
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
  $("#new-vector-project").addEventListener("click", () => fillVectorProject());
  document.querySelectorAll("[data-add-shape]").forEach((control) => control.addEventListener("click", () => addVectorShape(control.dataset.addShape)));
  $("#vector-svg-file").addEventListener("change", async (event) => {
    const file = event.target.files && event.target.files[0];
    event.target.value = "";
    if (!file) return;
    if (!connected || $("#vector-form").hidden) { setDesignerError("Open a vector project before importing SVG geometry."); return; }
    try {
      const response = await fetch("/api/v1/svg-import", {
        method: "POST",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "image/svg+xml" },
        body: file,
      });
      const imported = await response.json();
      if (response.status === 401 && token) forgetSession({ focus: true, clearInput: false });
      if (!response.ok) throw new Error(imported.error || `SVG import failed (${response.status})`);
      const objects = imported.objects.map((object) => ({
        id: vectorID("svg"), kind: "path", subpaths: object.subpaths.map((path) => ({
          closed: path.closed,
          points: path.points.map((point) => ({ x_mm: point.x_mm, y_mm: point.y_mm })),
        })),
      }));
      vectorObjects.push(...objects);
      selectedVectorObject = objects[0]?.id || selectedVectorObject;
      selectedVectorObjects = new Set(objects.length ? [objects[0].id] : []);
      renderVectorEditor();
      setDesignerStatus(`Imported ${objects.length} editable SVG path${objects.length === 1 ? "" : "s"}. Assign each path to an operation layer; no machine settings were imported.`);
    } catch (error) { setDesignerError(error.message); }
  });
  $("#add-operation").addEventListener("click", () => {
    const operation = { id: vectorID("operation"), type: "vector-engrave", feed_mm_min: 600, power_percent: 25 };
    vectorOperations.push(operation);
    renderVectorEditor();
  });
  document.querySelectorAll("[data-align]").forEach((control) => {
    control.addEventListener("click", () => alignSelectedVectorObjects(control.dataset.align));
  });
  $("#vector-form").addEventListener("submit", saveVectorProject);
  $("#vector-form").addEventListener("input", (event) => {
    if (event.target.matches("#vector-x, #vector-y, #vector-width, #vector-height")) updateSelectedGeometry();
    else if (event.target.matches("#vector-translate-x, #vector-translate-y, #vector-scale-x, #vector-scale-y, #vector-rotation")) updateSelectedTransform();
    else updateVectorValidation();
  });
  $("#vector-form").addEventListener("change", (event) => {
    if (event.target.id === "vector-object-operation") {
      const object = vectorObjects.find((candidate) => candidate.id === selectedVectorObject);
      if (object) { if (event.target.value) object.operation_id = event.target.value; else delete object.operation_id; }
      renderVectorEditor();
    } else updateVectorValidation();
  });
  $("#close-vector-project").addEventListener("click", () => { openVectorProject = null; $("#vector-form").hidden = true; });
  $("#delete-vector-project").addEventListener("click", () => { if (openVectorProject) deleteVectorProject(openVectorProject.id, openVectorProject.name); });
  $("#generate-vector-project").addEventListener("click", generateVectorProject);

  function renderDrivers() {
    const prior = selectedDriver;
    driverSelect.replaceChildren();
    if (!drivers.length) {
      const option = document.createElement("option");
      option.value = "";
      option.textContent = connected ? "No drivers connected" : "Connect to load drivers";
      driverSelect.append(option);
      driverSelect.disabled = true;
      selectedDriver = "";
      $("#driver-status").textContent = connected ? "Start a driver worker to run programs." : "Drivers are not loaded.";
      window.EmberDeckControllerSettings?.renderDriver($("#controller-settings"), null); // controller-settings.js
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
    driverSelect.disabled = !connected;
    const selected = drivers.find((driver) => driver.id === selectedDriver);
    $("#driver-status").textContent = selected ? (selected.busy ? "Driver has an active job." : `Last seen ${new Date(selected.last_seen).toLocaleTimeString()}`) : "Selected driver is disconnected.";
    window.EmberDeckControllerSettings?.renderDriver($("#controller-settings"), selected); // controller-settings.js
  }

  // Row controls carry their job and action so focus can follow a control across list re-renders.
  function rowControl(target) {
    if (!target) return null;
    const controls = [...list.querySelectorAll("[data-job-action]")].filter((control) => control.dataset.jobId === target.jobID);
    return controls.find((control) => control.dataset.jobAction === target.action) || (target.fallback ? controls[0] : null) || null;
  }

  function rowButton(job, action, label, className, handler) {
    const element = button(label, className, handler, `${label} ${job.name}`);
    element.dataset.jobId = job.id;
    element.dataset.jobAction = action;
    return element;
  }

  function markUnavailable(control, reason, container) {
    control.setAttribute("aria-disabled", "true");
    if (!reason) return;
    const note = document.createElement("span");
    note.className = "action-reason";
    note.id = `${control.dataset.jobAction}-reason-${control.dataset.jobId}`;
    note.textContent = reason;
    control.setAttribute("aria-describedby", note.id);
    container.append(note);
  }

  function startBlocker(job, needsBoundsOverride) {
    const driver = drivers.find((candidate) => candidate.id === selectedDriver);
    if (!selectedDriver || !drivers.length) return "Needs a connected driver.";
    if (!driver) return `Needs a connected driver; ${selectedDriver} is disconnected.`;
    if (driver.busy) return `Driver ${driver.id} is busy with another job.`;
    if (needsBoundsOverride && !boundsOverrides.has(job.id)) return "Needs the bounds override above.";
    if (!confirmation.checked) return "Needs the safety check above the list.";
    return "";
  }

  function openActionDialog({ eyebrow, title, details, body, confirmLabel, confirmClass, dismissLabel, onConfirm, returnFocus }) {
    if (actionDialog.open) return;
    $("#action-dialog-eyebrow").textContent = eyebrow;
    $("#action-dialog-title").textContent = title;
    const detailList = $("#action-dialog-details");
    detailList.replaceChildren();
    for (const [term, value] of details) {
      const dt = document.createElement("dt");
      dt.textContent = term;
      const dd = document.createElement("dd");
      dd.textContent = value;
      detailList.append(dt, dd);
    }
    $("#action-dialog-body").textContent = body;
    const confirm = $("#action-dialog-confirm");
    confirm.textContent = confirmLabel;
    confirm.className = `button ${confirmClass}`;
    $("#action-dialog-dismiss").textContent = dismissLabel;
    pendingAction = { onConfirm, returnFocus };
    actionDialog.returnValue = "";
    actionDialog.showModal();
    $("#action-dialog-dismiss").focus();
  }

  actionDialog.addEventListener("close", async () => {
    const action = pendingAction;
    pendingAction = null;
    if (!action) return;
    rowControl(action.returnFocus)?.focus();
    if (actionDialog.returnValue !== "confirm") return;
    await action.onConfirm();
    rowControl({ ...action.returnFocus, fallback: true })?.focus();
  });

  function confirmStart(job, needsBoundsOverride) {
    if (startBlocker(job, needsBoundsOverride)) return;
    const driverID = selectedDriver;
    const review = job.program_review;
    const details = [["Job", job.name], ["Driver", driverID]];
    if (job.provenance?.profile?.name) details.push(["Profile", job.provenance.profile.name]);
    details.push(["Bounds review", needsBoundsOverride ? `${review?.bounds_status || "unknown"} · override selected` : (review?.bounds_status || "in_bounds").replace("_", " ")]);
    details.push(...(window.EmberDeckControllerSettings?.startDetails(job, drivers.find((driver) => driver.id === driverID)) || [])); // controller-settings.js
    openActionDialog({
      eyebrow: "CONFIRM DISPATCH",
      title: `Start ${job.name}?`,
      details,
      body: `EmberDeck will send this program to ${driverID} as soon as you confirm. Check that this driver controls the machine you prepared, the work origin is set, and you are supervising the cut.`,
      confirmLabel: `Start on ${driverID}`,
      confirmClass: "button-primary",
      dismissLabel: "Go back",
      onConfirm: () => startJob(job.id, needsBoundsOverride, driverID),
      returnFocus: { jobID: job.id, action: "start" },
    });
  }

  function confirmCancel(job) {
    const details = [["Job", job.name], ["Driver", job.driver_id || "unassigned"], ["State", job.state]];
    if (job.total > 0) details.push(["Progress", `${job.acked || 0} / ${job.total} lines acknowledged`]);
    openActionDialog({
      eyebrow: "CONFIRM CANCELLATION",
      title: `Cancel ${job.name}?`,
      details,
      body: "Cancel stops streaming and asks the driver to halt the controller. A canceled job cannot be resumed or restarted; its program stays in the library until you delete it. Keep physical emergency stops independent of this control.",
      confirmLabel: "Cancel job",
      confirmClass: "button-danger",
      dismissLabel: job.state === "paused" ? "Keep paused" : "Keep running",
      onConfirm: () => cancelJob(job.id),
      returnFocus: { jobID: job.id, action: "cancel" },
    });
  }

  async function startJob(id, boundsOverride, driverID) {
    if (!confirmation.checked) { message("Confirm the safety check before starting a program."); confirmation.focus(); return; }
    if (!driverID) { message("Select a connected driver first."); driverSelect.focus(); return; }
    try {
      await api(`/api/v1/jobs/${encodeURIComponent(id)}/start`, {
        method: "POST",
        body: JSON.stringify({ driver_id: driverID, confirmed: true, bounds_override: Boolean(boundsOverride) }),
      });
      boundsOverrides.delete(id);
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
      message("Pause requested. Wait for the controller to confirm it is held; Resume continues the job after the safety check.");
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
    const focused = document.activeElement;
    const keepFocus = focused && list.contains(focused) && focused.dataset.jobAction ? { jobID: focused.dataset.jobId, action: focused.dataset.jobAction, fallback: true } : null;
    list.replaceChildren();
    const filtered = Boolean(jobFilterName.value.trim() || jobFilterState.value);
    $("#job-count").textContent = hasLoadedJobs ? String(jobTotal).padStart(2, "0") : "—";
    empty.hidden = jobs.length > 0;
    if (!jobs.length) {
      empty.querySelector("strong").textContent = !connected ? "Your library is ready" : filtered ? "No matching programs" : "No programs yet";
      empty.querySelector("span:last-child").textContent = !connected ? "Connect to load saved programs." : filtered ? "Clear the name or state filter to see every stored program." : "Add a G-code file to get started.";
    }
    const pager = $("#job-pager");
    pager.hidden = jobTotal <= jobPageSize && jobOffset === 0;
    $("#jobs-range").textContent = jobs.length ? `${jobOffset + 1}–${jobOffset + jobs.length} of ${jobTotal}` : `0 of ${jobTotal}`;
    $("#jobs-newer").disabled = jobOffset === 0;
    $("#jobs-older").disabled = jobOffset + jobs.length >= jobTotal;
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
      window.EmberDeckJobHistory?.appendTimes(info, job);
      if (job.controller_state) {
        const controller = document.createElement("span");
        controller.className = "controller-state";
        controller.textContent = `Last controller report: ${job.controller_state}`;
        info.append(controller);
      }
      const review = job.program_review;
      const needsBoundsOverride = job.state === "queued" && !job.provenance && (!review || review.bounds_status !== "in_bounds");
      if (needsBoundsOverride) {
        const label = document.createElement("label");
        label.className = "bounds-override";
        const checkbox = document.createElement("input");
        checkbox.type = "checkbox";
        checkbox.checked = boundsOverrides.has(job.id);
        checkbox.dataset.jobId = job.id;
        checkbox.dataset.jobAction = "bounds-override";
        checkbox.setAttribute("aria-label", `Override ${review?.bounds_status || "unknown"} bounds review for ${job.name}`);
        checkbox.addEventListener("change", () => {
          if (checkbox.checked) boundsOverrides.add(job.id);
          else boundsOverrides.delete(job.id);
          renderJobs();
        });
        const text = document.createElement("span");
        text.textContent = `Bounds review is ${review?.bounds_status || "unknown"}; inspect the path and profile before overriding.`;
        label.append(checkbox, text);
        info.append(label);
      }
      window.EmberDeckControllerSettings?.appendJobWarnings(info, job, drivers.find((driver) => driver.id === selectedDriver)); // controller-settings.js
      const active = ["running", "pausing", "paused", "resuming", "canceling"].includes(job.state);
      if (active || job.total > 0) {
        const progress = document.createElement("progress");
        progress.max = Math.max(job.total || 0, 1);
        progress.value = Math.min(job.acked || 0, progress.max);
        progress.setAttribute("aria-label", `${job.acked || 0} of ${job.total || 0} lines acknowledged`);
        info.append(progress);
      }
      if (active || job.execution || review) {
        const toolpathView = document.createElement("section");
        toolpathView.className = "toolpath-live";
        toolpathView.dataset.toolpathJob = job.id;
        toolpathView.setAttribute("aria-label", active || job.execution ? `Live toolpath for ${job.name}` : `Static G-code bounds review for ${job.name}`);
        toolpathView.textContent = "Loading controller path feedback…";
        info.append(toolpathView);
      }
      if (job.error) {
        const failure = document.createElement("span");
        failure.className = "job-error";
        failure.textContent = job.error;
        info.append(failure);
      }
      const controls = document.createElement("div");
      controls.className = "job-controls";
      const actions = document.createElement("div");
      actions.className = "job-actions";
      // Reasons and hints sit beside the buttons they describe.
      const notes = document.createElement("div");
      notes.className = "job-action-notes";
      actions.append(rowButton(job, "view", "View", "button button-small", () => showProgram(job.id)));
      if (job.state === "queued") {
        const start = rowButton(job, "start", "Start", "button button-small button-start", () => confirmStart(job, needsBoundsOverride));
        const blocker = startBlocker(job, needsBoundsOverride);
        if (blocker) markUnavailable(start, blocker, notes);
        actions.append(start);
      }
      if (job.state === "running") {
        const assignedDriver = drivers.find((driver) => driver.id === job.driver_id);
        if (assignedDriver && Array.isArray(assignedDriver.capabilities) && assignedDriver.capabilities.includes("pause_resume")) {
          // Pause is not confirmed: holding should be immediate, and Resume reverses it.
          const pause = rowButton(job, "pause", "Pause", "button button-small", () => pauseJob(job.id));
          const hint = document.createElement("span");
          hint.className = "action-hint";
          hint.id = `pause-hint-${job.id}`;
          hint.textContent = "Pause holds the job without canceling it; Resume continues it after the safety check.";
          pause.setAttribute("aria-describedby", hint.id);
          notes.append(hint);
          actions.append(pause);
        }
      }
      if (job.state === "pausing") {
        const pause = rowButton(job, "pause", "Pausing…", "button button-small", () => {});
        markUnavailable(pause, "", notes);
        actions.append(pause);
      }
      if (job.state === "paused") {
        const resume = rowButton(job, "resume", "Resume", "button button-small button-start", () => resumeJob(job.id));
        if (!checked) markUnavailable(resume, "Needs the safety check above the list to resume.", notes);
        actions.append(resume);
      }
      if (active) {
        const cancel = rowButton(job, "cancel", job.state === "canceling" ? "Canceling…" : "Cancel", "button button-small button-danger", () => confirmCancel(job));
        if (job.state === "canceling") markUnavailable(cancel, "", notes);
        actions.append(cancel);
      }
      if (["queued", "failed", "completed", "canceled", "interrupted"].includes(job.state)) {
        actions.append(rowButton(job, "delete", "Delete", "button button-small button-danger", () => deleteJob(job.id, job.name)));
      }
      controls.append(actions);
      if (notes.childElementCount) controls.append(notes);
      item.append(info, controls);
      list.append(item);
    }
    confirmation.checked = checked;
    if (keepFocus) rowControl(keepFocus)?.focus();
  }

  function coordinate(value) {
    if (!Number.isFinite(value)) return "?";
    return Number(value.toFixed(3)).toString();
  }

  function renderToolpathView(host, job, path, review) {
    host.replaceChildren();
    const execution = job.execution || {};
    const progress = execution.path_progress || { completed: 0, total: path.segments.length, confidence: "unknown" };
    const summary = document.createElement("p");
    summary.className = "toolpath-summary";
    summary.textContent = job.execution ? `Path progress: ${progress.completed || 0} of ${progress.total || 0} segments · ${progress.confidence || "unknown"}` : `Static program review · bounds ${review?.bounds_status || "unknown"} · execution not reported`;
    host.append(summary);
    if (review?.bounds) {
      const boundsText = document.createElement("p");
      boundsText.className = `toolpath-caveat bounds-status-${review.bounds_status}`;
      boundsText.textContent = `Program bounds: X ${coordinate(review.bounds.min.x)}–${coordinate(review.bounds.max.x)} mm · Y ${coordinate(review.bounds.min.y)}–${coordinate(review.bounds.max.y)} mm · ${review.bounds_status.replaceAll("_", " ")}.`;
      host.append(boundsText);
    }
    if (review?.warnings?.length) {
      const reviewWarning = document.createElement("p");
      reviewWarning.className = "toolpath-caveat";
      reviewWarning.textContent = `Review: ${review.warnings.join("; ")}`;
      host.append(reviewWarning);
    }
    if (execution.position_units === "assumed-mm") {
      const assumption = document.createElement("p");
      assumption.className = "toolpath-caveat";
      assumption.textContent = "Position match assumes GRBL $13=0 (millimetre reports). Verify this setting. A path estimate does not prove laser output or material processing.";
      host.append(assumption);
    } else if (execution.machine_position) {
      const machine = execution.machine_position;
      const position = document.createElement("p");
      position.className = "toolpath-caveat";
      position.textContent = `Machine position in controller units: X${coordinate(machine.x)} Y${coordinate(machine.y)}. Work-position units or offset are not known, so the head marker is not aligned to this path.`;
      host.append(position);
    }
    if (path.warnings && path.warnings.length) {
      const warning = document.createElement("p");
      warning.className = "toolpath-caveat";
      warning.textContent = `Partial path preview: ${path.warnings.join("; ")}`;
      host.append(warning);
    }
    if (!path.segments.length) {
      const emptyPath = document.createElement("p");
      emptyPath.className = "toolpath-caveat";
      emptyPath.textContent = "No supported 2D linear segments are available to preview. Execution progress is unknown.";
      host.append(emptyPath);
      return;
    }

    const points = path.segments.flatMap((segment) => [segment.start, segment.end]);
    if (review?.profile_bounds) points.push(review.profile_bounds.min, review.profile_bounds.max);
    const workPosition = execution.position_units === "assumed-mm" ? execution.work_position : null;
    if (workPosition) points.push(workPosition);
    const xs = points.map((point) => point.x), ys = points.map((point) => point.y);
    const minX = Math.min(...xs), maxX = Math.max(...xs), minY = Math.min(...ys), maxY = Math.max(...ys);
    const span = Math.max(maxX - minX, maxY - minY, 1);
    const pad = span * 0.08;
    const fit = { x: minX - pad, y: -maxY - pad, width: maxX - minX + 2 * pad || 1, height: maxY - minY + 2 * pad || 1 };
    let view = toolpathViewports.get(job.id);
    if (!view) { view = { ...fit }; toolpathViewports.set(job.id, view); }
    const controls = document.createElement("div");
    controls.className = "toolpath-controls";
    const svg = document.createElementNS(svgNS, "svg");
    svg.setAttribute("viewBox", `${view.x} ${view.y} ${view.width} ${view.height}`);
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", job.execution ? `Fitted XY execution preview for ${job.name}; completed estimate, active path and remaining paths are shown` : `Fitted static XY program preview for ${job.name}`);
    svg.setAttribute("preserveAspectRatio", "xMidYMid meet");
    svg.classList.add("toolpath-svg");
    const group = document.createElementNS(svgNS, "g");
    group.setAttribute("transform", "scale(1 -1)");
    if (review?.profile_bounds) {
      const profileBounds = review.profile_bounds;
      const box = document.createElementNS(svgNS, "rect");
      box.setAttribute("class", "toolpath-profile-bounds");
      box.setAttribute("x", profileBounds.min.x); box.setAttribute("y", profileBounds.min.y);
      box.setAttribute("width", profileBounds.max.x - profileBounds.min.x);
      box.setAttribute("height", profileBounds.max.y - profileBounds.min.y);
      group.append(box);
    }
    const activeIndex = Number.isInteger(progress.active) ? progress.active : -1;
    for (let index = 0; index < path.segments.length; index++) {
      const segment = path.segments[index];
      const line = document.createElementNS(svgNS, "line");
      const stateClass = index < (progress.completed || 0) ? "path-completed" : index === activeIndex ? "path-active" : "path-remaining";
      line.setAttribute("class", `toolpath-segment ${stateClass} ${segment.rapid ? "path-travel" : "path-motion"} ${segment.laser_on ? "path-laser-on" : "path-laser-off"}`);
      line.setAttribute("x1", segment.start.x); line.setAttribute("y1", segment.start.y);
      line.setAttribute("x2", segment.end.x); line.setAttribute("y2", segment.end.y);
      group.append(line);
    }
    if (workPosition) {
      const head = document.createElementNS(svgNS, "circle");
      head.setAttribute("class", "toolpath-head");
      head.setAttribute("cx", workPosition.x); head.setAttribute("cy", workPosition.y);
      head.setAttribute("r", String(span * 0.012));
      group.append(head);
    }
    svg.append(group);
    const setView = () => svg.setAttribute("viewBox", `${view.x} ${view.y} ${view.width} ${view.height}`);
    const zoom = (factor) => {
      const width = view.width * factor, height = view.height * factor;
      view.x += (view.width - width) / 2; view.y += (view.height - height) / 2;
      view.width = width; view.height = height; setView();
    };
    controls.append(button("−", "button button-small", () => zoom(1.25), "Zoom out toolpath"));
    controls.append(button("+", "button button-small", () => zoom(0.8), "Zoom in toolpath"));
    controls.append(button("Fit", "button button-small", () => { Object.assign(view, fit); setView(); }, "Fit toolpath to view"));
    let drag = null;
    svg.addEventListener("pointerdown", (event) => {
      drag = { x: event.clientX, y: event.clientY };
      svg.setPointerCapture(event.pointerId);
    });
    svg.addEventListener("pointermove", (event) => {
      if (!drag) return;
      const rect = svg.getBoundingClientRect();
      view.x -= (event.clientX - drag.x) * view.width / Math.max(rect.width, 1);
      view.y -= (event.clientY - drag.y) * view.height / Math.max(rect.height, 1);
      drag = { x: event.clientX, y: event.clientY }; setView();
    });
    svg.addEventListener("pointerup", () => { drag = null; });
    svg.addEventListener("pointercancel", () => { drag = null; });
    const legend = document.createElement("p");
    legend.className = "toolpath-legend";
    legend.textContent = job.execution
      ? (review?.profile_bounds ? "Completed estimate · active segment · remaining path · laser-on solid · laser-off travel dashed · head marker where work coordinates are available · dashed rectangle is the selected profile work area." : "Completed estimate · active segment · remaining path · laser-on solid · laser-off travel dashed · head marker where work coordinates are available. Machine bounds are unknown without a selected profile.")
      : (review?.profile_bounds ? "Program motion · laser-on solid · laser-off travel dashed · dashed rectangle is the selected profile work area." : "Program motion · laser-on solid · laser-off travel dashed · machine bounds are unknown without a selected profile.");
    host.append(controls, svg, legend);
  }

  async function refreshToolpathViews() {
    for (const job of jobs) {
      if (!(job.program_review || job.execution || ["running", "pausing", "paused", "resuming", "canceling"].includes(job.state))) continue;
      const host = Array.from(list.querySelectorAll(".toolpath-live")).find((node) => node.dataset.toolpathJob === job.id);
      if (!host) continue;
      try {
        if (!toolpathCache.has(job.id)) toolpathCache.set(job.id, await api(`/api/v1/jobs/${encodeURIComponent(job.id)}/toolpath`));
        renderToolpathView(host, job, toolpathCache.get(job.id), job.program_review);
      } catch (error) {
        host.textContent = `Toolpath preview unavailable: ${error.message}`;
      }
    }
  }

  function jobsPath() {
    const query = new URLSearchParams({ limit: String(jobPageSize), offset: String(jobOffset) });
    const name = jobFilterName.value.trim();
    if (name) query.set("name", name);
    if (jobFilterState.value) query.set("state", jobFilterState.value);
    return `/api/v1/jobs?${query}`;
  }

  // Filter and page changes queue a follow-up refresh when a poll is already in
  // flight; other callers keep the existing skip-while-polling behavior.
  async function refresh({ queue = false } = {}) {
    if (!connected) return;
    if (polling) { if (queue === true) refreshQueued = true; return; }
    polling = true;
    const version = sessionVersion;
    try {
      const results = await Promise.all([api(jobsPath()), api("/api/v1/drivers")]);
      if (!connected || version !== sessionVersion) return;
      let page = results[0];
      if (!page.jobs.length && jobOffset > 0 && page.total > 0) {
        // Deletions or a narrower filter can leave the current page empty.
        jobOffset = Math.floor((page.total - 1) / jobPageSize) * jobPageSize;
        page = await api(jobsPath());
        if (!connected || version !== sessionVersion) return;
      }
      jobs = page.jobs;
      jobTotal = page.total;
      drivers = results[1];
      hasLoadedJobs = true;
      message("");
      renderDrivers();
      renderJobs();
      await refreshToolpathViews();
      if (shownJobID && !programPanel.hidden) window.EmberDeckJobHistory?.load($("#program-history"), shownJobID, api);
      await refreshDesigner();
    } catch (error) {
      if (!connected || version !== sessionVersion) return;
      message(error.message);
    } finally {
      if (version === sessionVersion) {
        polling = false;
        if (refreshQueued) { refreshQueued = false; refresh(); }
      }
    }
  }

  function startPolling() {
    if (connected && pollTimer === null) pollTimer = window.setInterval(refresh, 2000);
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
        let operationSummary;
        if (source.schema_version === 2) {
          const objects = Array.isArray(source.objects) ? source.objects : [];
          const operations = Array.isArray(source.operations) ? source.operations : [];
          const pathCount = objects.reduce((total, object) => total + (Array.isArray(object.subpaths) ? object.subpaths.length : 0), 0);
          const unassigned = objects.filter((object) => !object.operation_id).length;
          const layers = operations.map((operation, index) => {
            const count = objects.filter((object) => object.operation_id === operation.id).length;
            const kind = operation.type === "vector-cut" ? "Cut" : "Engrave";
            return `${index + 1}. ${kind} · ${count} ${count === 1 ? "object" : "objects"} · feed ${operation.feed_mm_min ?? "?"} mm/min · commanded power ${operation.power_percent ?? "?"}%`;
          });
          operationSummary = `Geometry snapshot: ${objects.length} ${objects.length === 1 ? "object" : "objects"} · ${pathCount} ${pathCount === 1 ? "path" : "paths"}${unassigned ? ` · ${unassigned} unassigned` : ""}`;
          operationSummary += ` · Operation layers: ${layers.length ? layers.join(" | ") : "none"}`;
        } else {
          const rectangle = source.rectangle || {};
          const operation = source.operation || {};
          operationSummary = "Operation snapshot: " + (operation.type || "vector engraving") + " · rectangle X" + (rectangle.x_mm ?? "?") + " Y" + (rectangle.y_mm ?? "?") + " mm · " + (rectangle.width_mm ?? "?") + " × " + (rectangle.height_mm ?? "?") + " mm · feed " + (operation.feed_mm_min ?? "?") + " mm/min · commanded power " + (operation.power_percent ?? "?") + "%";
        }
        const details = [
          "Project snapshot: " + (source.name || "saved project") + (source.revision ? " · revision " + source.revision : ""),
          "Profile snapshot: " + (profile.name || "declared profile") + (profile.revision ? " · revision " + profile.revision : "") + " · " + (profile.width_mm ?? "?") + " × " + (profile.height_mm ?? "?") + " " + (profile.units || "mm") + " · " + (profile.origin || "lower-left") + " origin · $30=" + (profile.max_s ?? "?") + " · $31=" + (profile.min_s ?? "?") + " · $32=1 · feed " + (profile.min_feed_mm_min ?? "?") + "–" + (profile.max_feed_mm_min ?? "?") + " mm/min · max commanded power " + (profile.max_power_percent ?? "?") + "%",
          operationSummary,
          "Generator version: " + (job.provenance.generator_version || "unknown") + ". The generated program and source snapshots are immutable.",
        ];
        for (const detail of details) {
          const line = document.createElement("p");
          line.textContent = detail;
          provenance.append(line);
        }
        provenance.hidden = false;
      } else provenance.hidden = true;
      window.EmberDeckJobHistory?.load($("#program-history"), id, api);
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

  async function connectWithKey(rawKey) {
    const candidate = rawKey.trim();
    if (!candidate || connectButton.disabled) return;
    keyInput.value = candidate;
    sessionStorage.removeItem("emberdeck-api-key");
    token = candidate;
    setConnecting(true);
    try {
      await api("/api/v1/jobs");
      if (token !== candidate) return;
      sessionStorage.setItem("emberdeck-api-key", candidate);
      sessionVersion++;
      setSessionConnected(true);
      message("");
      await refresh();
      if (connected) startPolling();
    } catch (error) {
      if (token === candidate) forgetSession({ clearInput: false });
      message(error.message);
      keyInput.focus();
    } finally {
      setConnecting(false);
    }
  }

  authForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    await connectWithKey(keyInput.value);
  });

  $("#change-key").addEventListener("click", () => {
    const currentKey = token;
    forgetSession({ clearInput: false });
    keyInput.value = currentKey;
    keyInput.focus();
  });
  $("#disconnect").addEventListener("click", () => forgetSession());

  fileInput.addEventListener("change", () => {
    const file = fileInput.files && fileInput.files[0];
    if (!file) return;
    $("#file-label").textContent = file.name;
    if (!jobName.value) jobName.value = file.name;
  });

  uploadForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!connected) { message("Connect with your API key before adding a program."); keyInput.focus(); return; }
    const file = fileInput.files && fileInput.files[0];
    if (!file) return;
    try {
      const gcode = await file.text();
      await api("/api/v1/jobs", { method: "POST", body: JSON.stringify({ name: jobName.value, gcode, profile_id: uploadProfileSelect.value }) });
      uploadForm.reset();
      $("#file-label").textContent = "Choose a G-code file";
      jobOffset = 0; // New programs are listed first.
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
  let filterTimer = null;
  jobFilterName.addEventListener("input", () => {
    window.clearTimeout(filterTimer);
    filterTimer = window.setTimeout(() => { jobOffset = 0; refresh({ queue: true }); }, 250);
  });
  jobFilterState.addEventListener("change", () => { jobOffset = 0; refresh({ queue: true }); });
  $("#jobs-newer").addEventListener("click", () => { jobOffset = Math.max(0, jobOffset - jobPageSize); refresh({ queue: true }); });
  $("#jobs-older").addEventListener("click", () => { jobOffset += jobPageSize; refresh({ queue: true }); });
  $("#refresh").addEventListener("click", refresh);
  $("#close-program").addEventListener("click", () => { shownJobID = ""; programPanel.hidden = true; });
  setSessionConnected(false);
  renderDrivers();
  renderJobs();
  const rememberedKey = (sessionStorage.getItem("emberdeck-api-key") || "").trim();
  sessionStorage.removeItem("emberdeck-api-key");
  if (rememberedKey) {
    keyInput.value = rememberedKey;
    connectWithKey(rememberedKey);
  }
})();
