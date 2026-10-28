"use strict";

// Job run times and event history for the operator console. app.js calls
// these hooks; this module owns only the elements it creates.
window.EmberDeckJobHistory = (() => {
  const actions = {
    upload: "Uploaded",
    generate: "Generated from project",
    start: "Dispatched",
    pause: "Pause requested",
    resume: "Resume requested",
    cancel: "Cancel requested",
    delete: "Deleted",
    report: "Worker report",
    restart: "Server restart",
    heartbeat_expired: "Driver heartbeat expired",
    credential_revoked: "Worker credential revoked",
  };

  function element(name, className, text) {
    const node = document.createElement(name);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function timeElement(iso) {
    const node = element("time");
    node.dateTime = iso;
    const date = new Date(iso);
    node.textContent = Number.isNaN(date.getTime()) ? iso : date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "medium" });
    node.title = iso;
    return node;
  }

  function duration(ms) {
    if (!Number.isFinite(ms) || ms < 0) return "unknown";
    if (ms < 1000) return `${ms} ms`;
    const seconds = Math.round(ms / 1000);
    if (seconds < 60) return `${(ms / 1000).toFixed(1)} s`;
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    const rest = seconds % 60;
    return hours ? `${hours} h ${minutes} min` : `${minutes} min ${rest} s`;
  }

  // appendTimes adds the start, end, and duration summary to a job list row.
  function appendTimes(host, job) {
    const line = element("span", "job-times");
    if (!job.started && !job.ended) {
      line.textContent = "Not started";
    } else {
      const parts = [];
      if (job.started) parts.push(["Started ", timeElement(job.started)]);
      else parts.push(["Not started"]);
      if (job.ended) parts.push(["Ended ", timeElement(job.ended)]);
      if (job.duration_ms !== undefined && job.duration_ms !== null) parts.push([`Duration ${duration(job.duration_ms)}`]);
      parts.forEach((part, index) => {
        if (index) line.append(" · ");
        line.append(...part);
      });
    }
    host.append(line);
  }

  function renderEvents(host, events) {
    host.replaceChildren();
    host.append(element("h3", "job-history-title", "Event history"));
    if (!events.length) {
      host.append(element("p", "job-history-empty", "No retained events. Jobs stored before history was recorded, or older than the retention limit, have none."));
      return;
    }
    const list = element("ol", "job-history-list");
    list.setAttribute("aria-label", "Job event history");
    for (const event of events) {
      const item = element("li", "job-history-event");
      const head = element("div", "job-history-head");
      head.append(element("strong", "", actions[event.action] || event.action), element("span", "job-history-states", event.from ? `${event.from} → ${event.to}` : event.to));
      const meta = element("div", "job-history-meta");
      meta.append(timeElement(event.time));
      const facts = [event.credential_name ? `by ${event.source} ${event.credential_name}` : `by ${event.source}`];
      if (event.driver_id) facts.push(`driver ${event.driver_id}`);
      if (event.session_id) facts.push(`session ${event.session_id}`);
      if (event.lease_id) facts.push(`lease ${event.lease_id}`);
      meta.append(` · ${facts.join(" · ")}`);
      item.append(head, meta);
      if (event.detail) item.append(element("p", "job-history-detail", event.detail));
      list.append(item);
    }
    host.append(list);
  }

  // load fetches and renders one job's history into host. A response for a
  // job that is no longer shown is ignored.
  async function load(host, id, api) {
    host.dataset.jobId = id;
    host.hidden = false;
    try {
      const body = await api(`/api/v1/jobs/${encodeURIComponent(id)}/events`);
      if (host.dataset.jobId !== id) return;
      renderEvents(host, Array.isArray(body.events) ? body.events : []);
    } catch (error) {
      if (host.dataset.jobId !== id) return;
      host.replaceChildren(element("h3", "job-history-title", "Event history"), element("p", "job-history-empty", `History unavailable: ${error.message}`));
    }
  }

  return { appendTimes, load };
})();
