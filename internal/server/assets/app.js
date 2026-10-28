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
    $("#driver-status").textContent = selected ? (selected.busy ? "Driver is running a program." : `Last seen ${new Date(selected.last_seen).toLocaleTimeString()}`) : "Selected driver is disconnected.";
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
      if (job.state === "running" || job.state === "canceling" || job.total > 0) {
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
      if (job.state === "running" || job.state === "canceling") {
        const cancel = button(job.state === "canceling" ? "Canceling…" : "Cancel", "button button-small button-danger", () => cancelJob(job.id));
        cancel.disabled = job.state === "canceling";
        actions.append(cancel);
      }
      if (job.state === "queued" || job.state === "failed" || job.state === "completed" || job.state === "canceled") {
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
