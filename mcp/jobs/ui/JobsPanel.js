// Jobs panel — vanilla DOM. Reads project_id from the URL the
// platform supplied; calls /api/apps/jobs/* same-origin so cookies +
// session token authenticate every request.
//
// Embedding from another app's panel: pass ?owner_app=<slug> to scope
// the list to a single caller (e.g. ?owner_app=crm shows only CRM's
// scheduled jobs).

(function () {
  const params = new URLSearchParams(window.location.search);
  const projectId = params.get("project_id") || "";
  const installId = params.get("install_id") || "";
  const ownerApp  = params.get("owner_app") || "";

  const API = "/api/apps/jobs";

  const state = {
    jobs: [],
    selectedId: null,
    runs: [],
  };

  function el(tag, props = {}, ...children) {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(props)) {
      if (k === "class") node.className = v;
      else if (k === "html") node.innerHTML = v;
      else if (k.startsWith("on") && typeof v === "function") {
        node.addEventListener(k.slice(2).toLowerCase(), v);
      } else if (v !== undefined && v !== null) {
        node.setAttribute(k, v);
      }
    }
    for (const c of children) {
      if (c == null) continue;
      node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    }
    return node;
  }

  function withProject(path, extra = {}) {
    const u = new URL(API + path, window.location.origin);
    if (projectId) u.searchParams.set("project_id", projectId);
    if (installId) u.searchParams.set("install_id", installId);
    for (const [k, v] of Object.entries(extra)) {
      if (v !== undefined && v !== null && v !== "") u.searchParams.set(k, String(v));
    }
    return u.toString();
  }
  async function api(method, path, body, params) {
    const res = await fetch(withProject(path, params || {}), {
      method,
      credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) {
      const t = await res.text().catch(() => "");
      throw new Error(`${res.status}: ${t || res.statusText}`);
    }
    return res.json();
  }

  // ────── Humanise schedule + time ─────────────────────────────────
  function humaniseSchedule(j) {
    if (!j) return "";
    if (j.schedule_kind === "once") return "once at " + relTime(j.run_at);
    if (j.schedule_kind === "every") {
      const s = j.every_seconds || 0;
      if (s % 3600 === 0) return "every " + (s / 3600) + "h";
      if (s % 60 === 0)   return "every " + (s / 60)   + "m";
      return "every " + s + "s";
    }
    if (j.schedule_kind === "cron") return "cron: " + j.cron_expr;
    return j.schedule_kind;
  }
  function relTime(s) {
    if (!s) return "—";
    const d = new Date(s);
    if (isNaN(d)) return s;
    const diff = (d - new Date()) / 1000;
    const abs = Math.abs(diff);
    const sign = diff >= 0 ? "in " : "";
    const past = diff < 0 ? " ago" : "";
    if (abs < 60) return sign + Math.round(abs) + "s" + past;
    if (abs < 3600) return sign + Math.round(abs / 60) + "m" + past;
    if (abs < 86400) return sign + Math.round(abs / 3600) + "h" + past;
    return d.toLocaleString();
  }

  // ────── List ─────────────────────────────────────────────────────
  async function loadList() {
    setStatus("Loading…");
    try {
      const filt = {};
      const sf = document.getElementById("status-filter").value;
      if (sf) filt.status = sf;
      if (ownerApp) filt.owner_app = ownerApp;
      const r = await api("GET", "/jobs", null, filt);
      state.jobs = r.jobs || [];
      renderList();
      setStatus(`${state.jobs.length} jobs${ownerApp ? " · " + ownerApp : ""}`);
    } catch (e) {
      setStatus("Error: " + e.message);
    }
  }
  function renderList() {
    const ul = document.getElementById("list");
    ul.innerHTML = "";
    for (const j of state.jobs) {
      const li = el("li", {
        class: "job-row" + (j.id === state.selectedId ? " selected" : ""),
        onclick: () => selectJob(j.id),
      },
        el("span", { class: "job-name" }, j.name),
        el("span", { class: "job-status " + j.status }, j.status),
        el("span", { class: "job-meta" },
          humaniseSchedule(j) + " · next " + relTime(j.next_run_at)),
      );
      ul.appendChild(li);
    }
  }
  function setStatus(msg) {
    document.getElementById("list-status").textContent = msg || "";
  }

  // ────── Detail / runs ────────────────────────────────────────────
  async function selectJob(id) {
    state.selectedId = id;
    renderList();
    try {
      const j = (await api("GET", "/jobs/" + id)).job;
      const runs = (await api("GET", "/jobs/" + id + "/runs")).runs || [];
      state.runs = runs;
      renderDetail(j, runs);
    } catch (e) {
      document.getElementById("detail").textContent = "Error: " + e.message;
    }
  }
  function renderDetail(j, runs) {
    const d = document.getElementById("detail");
    d.innerHTML = "";
    d.appendChild(el("h2", {}, j.name));
    d.appendChild(el("div", { class: "job-meta" },
      humaniseSchedule(j) + " · status: " + j.status +
      " · next: " + relTime(j.next_run_at)));
    d.appendChild(el("pre", { class: "target" }, JSON.stringify(j.target, null, 2)));

    const actions = el("div", { class: "actions" },
      el("button", { onclick: async () => { await api("POST", "/jobs/" + j.id + "/run-now"); selectJob(j.id); loadList(); } }, "Run now"),
      el("button", { class: "danger", onclick: async () => {
          if (!confirm("Cancel this job?")) return;
          await api("DELETE", "/jobs/" + j.id);
          selectJob(j.id); loadList();
      } }, "Cancel"),
    );
    d.appendChild(actions);

    const tbl = el("table", { class: "runs" },
      el("thead", {},
        el("tr", {},
          el("th", {}, "Started"),
          el("th", {}, "Duration"),
          el("th", {}, "Status"),
          el("th", {}, "HTTP"),
          el("th", {}, "Error"),
        ),
      ),
    );
    const tbody = el("tbody");
    for (const r of runs) {
      tbody.appendChild(el("tr", {},
        el("td", {}, relTime(r.started_at)),
        el("td", {}, r.duration_ms + " ms"),
        el("td", {}, r.status),
        el("td", {}, r.http_status ? String(r.http_status) : "—"),
        el("td", { class: r.error ? "err" : "" }, r.error || ""),
      ));
    }
    tbl.appendChild(tbody);
    d.appendChild(tbl);
  }

  // ────── Boot ─────────────────────────────────────────────────────
  document.getElementById("refresh").addEventListener("click", loadList);
  document.getElementById("status-filter").addEventListener("change", loadList);
  loadList();
  // Soft refresh every 5s so "running" / "next run" stay current.
  setInterval(loadList, 5000);
})();
