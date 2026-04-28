// Contacts panel — vanilla DOM. Reads project_id from the URL the
// platform supplied; calls /api/apps/crm/* same-origin so cookies
// + session token authenticate every request.

(function () {
  const params = new URLSearchParams(window.location.search);
  const projectId = params.get("project_id") || "";
  const installId = params.get("install_id") || "";

  // The platform proxies us at /api/apps/crm. Strip our own /ui prefix
  // — fetches go up one level.
  const API = "/api/apps/crm";

  const state = {
    contacts: [],
    selectedId: null,
    detail: null,        // full contact + activities loaded
  };

  // ────── tiny DOM helper ───────────────────────────────────────────
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

  // ────── API helpers ───────────────────────────────────────────────
  function withProject(path, extra = {}) {
    const u = new URL(API + path, window.location.origin);
    u.searchParams.set("project_id", projectId);
    if (installId) u.searchParams.set("install_id", installId);
    for (const [k, v] of Object.entries(extra)) {
      if (v !== undefined && v !== null) u.searchParams.set(k, String(v));
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

  // ────── List ──────────────────────────────────────────────────────
  async function loadList(query) {
    setStatus("Loading…");
    try {
      const params = {};
      if (query) params.q = query;
      const r = await api("GET", "/contacts", null, params);
      state.contacts = r.contacts || [];
      renderList();
      setStatus(`${state.contacts.length} contacts`);
    } catch (e) {
      setStatus("Error: " + e.message);
    }
  }
  function renderList() {
    const ul = document.getElementById("list");
    ul.innerHTML = "";
    for (const c of state.contacts) {
      const li = el("li", {
        class: c.id === state.selectedId ? "active" : "",
        onClick: () => selectContact(c.id),
      },
        el("div", { class: "name" }, displayName(c)),
        el("div", { class: "secondary" }, secondaryLine(c)),
      );
      ul.appendChild(li);
    }
  }
  function displayName(c) {
    return c.display_name ||
      [c.first_name, c.last_name].filter(Boolean).join(" ") ||
      c.primary_email || c.primary_phone || "(no name)";
  }
  function secondaryLine(c) {
    const bits = [];
    if (c.company) bits.push(c.company);
    if (c.job_title) bits.push(c.job_title);
    if (c.primary_email) bits.push(c.primary_email);
    return bits.join(" · ");
  }
  function setStatus(text) {
    document.getElementById("list-status").textContent = text;
  }

  // ────── Detail ────────────────────────────────────────────────────
  async function selectContact(id) {
    state.selectedId = id;
    renderList();
    document.querySelector(".layout").classList.add("detail-open");
    setDetailLoading();
    try {
      const [c, a] = await Promise.all([
        api("GET", `/contacts/${id}`),
        api("GET", `/contacts/${id}/activities`),
      ]);
      state.detail = { contact: c.contact, activities: a.activities || [] };
      renderDetail();
    } catch (e) {
      renderDetailError(e.message);
    }
  }
  function setDetailLoading() {
    document.getElementById("detail").innerHTML =
      '<div class="empty-state"><p>Loading…</p></div>';
  }
  function renderDetailError(msg) {
    const root = document.getElementById("detail");
    root.innerHTML = "";
    root.appendChild(el("div", { class: "banner-error" }, msg));
  }
  function renderDetail() {
    const root = document.getElementById("detail");
    root.innerHTML = "";
    const c = state.detail.contact;
    const acts = state.detail.activities;

    root.appendChild(el("h1", {}, displayName(c)));
    root.appendChild(el("p", { class: "subtitle" }, secondaryLine(c) || "—"));

    // Editable core fields.
    const grid = el("div", { class: "grid" });
    [
      ["First name", "first_name"],
      ["Last name", "last_name"],
      ["Display name", "display_name"],
      ["Pronouns", "pronouns"],
      ["Company", "company"],
      ["Job title", "job_title"],
      ["Status", "status", ["active", "archived", "spam", "merged"]],
    ].forEach(([label, key, opts]) => {
      grid.appendChild(el("label", {}, label));
      let input;
      if (opts) {
        input = el("select");
        opts.forEach((opt) =>
          input.appendChild(el("option", { value: opt, selected: c[key] === opt ? "" : null }, opt)),
        );
      } else {
        input = el("input", { type: "text", value: c[key] || "" });
      }
      input.dataset.field = key;
      grid.appendChild(input);
    });
    root.appendChild(grid);

    // Channels.
    if (c.channels && c.channels.length) {
      const sec = el("div", { class: "section" });
      sec.appendChild(el("h2", {}, "Channels"));
      for (const ch of c.channels) {
        sec.appendChild(el("div", { class: "row" },
          el("span", { class: "kind" }, ch.kind),
          el("span", { class: "value" }, ch.value),
          ch.label ? el("span", { class: "label-tag" }, ch.label) : null,
          ch.is_primary ? el("span", { class: "primary-tag" }, "primary") : null,
        ));
      }
      root.appendChild(sec);
    }

    // Tags.
    if (c.tags && c.tags.length) {
      const sec = el("div", { class: "section" });
      sec.appendChild(el("h2", {}, "Tags"));
      const tagBox = el("div", { class: "tags" });
      for (const t of c.tags) tagBox.appendChild(el("span", { class: "tag" }, t));
      sec.appendChild(tagBox);
      root.appendChild(sec);
    }

    // Custom attributes.
    if (c.attributes && c.attributes.length) {
      const sec = el("div", { class: "section" });
      sec.appendChild(el("h2", {}, "Attributes"));
      const attrGrid = el("div", { class: "grid" });
      for (const a of c.attributes) {
        attrGrid.appendChild(el("label", {}, a.label || a.key));
        attrGrid.appendChild(el("div", { class: "value" }, formatAttrValue(a)));
      }
      sec.appendChild(attrGrid);
      root.appendChild(sec);
    }

    // Activities.
    const actSec = el("div", { class: "section" });
    actSec.appendChild(el("h2", {}, `Activity (${acts.length})`));
    const actList = el("div", { class: "activities" });
    if (acts.length === 0) {
      actList.appendChild(el("p", { class: "subtitle" }, "No activity logged."));
    } else {
      for (const a of acts) {
        actList.appendChild(el("div", { class: "activity" },
          el("div", { class: "head" },
            el("span", { class: "kind-pill" }, a.kind),
            el("span", {}, formatTime(a.occurred_at) + (a.source ? ` · ${a.source}` : "")),
          ),
          el("div", { class: "body" }, a.body),
        ));
      }
    }
    actSec.appendChild(actList);
    root.appendChild(actSec);

    // Actions.
    const actions = el("div", { class: "actions" });
    actions.appendChild(el("button", { class: "primary", onClick: saveContact }, "Save"));
    actions.appendChild(el("button", { onClick: addActivityPrompt }, "Log activity"));
    actions.appendChild(el("button", { class: "danger", onClick: archiveContact }, "Archive"));
    root.appendChild(actions);
  }

  function formatAttrValue(a) {
    if (a.value == null) return "—";
    if (Array.isArray(a.value)) return a.value.join(", ");
    if (typeof a.value === "boolean") return a.value ? "yes" : "no";
    return String(a.value);
  }
  function formatTime(s) {
    if (!s) return "";
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  async function saveContact() {
    if (!state.detail) return;
    const c = state.detail.contact;
    const patch = {};
    document.querySelectorAll("[data-field]").forEach((el) => {
      patch[el.dataset.field] = el.value;
    });
    try {
      const r = await api("PATCH", `/contacts/${c.id}`, patch);
      state.detail.contact = r.contact;
      await loadList(document.getElementById("q").value.trim());
      renderDetail();
    } catch (e) {
      alert("Save failed: " + e.message);
    }
  }
  async function archiveContact() {
    if (!state.detail) return;
    if (!confirm(`Archive ${displayName(state.detail.contact)}?`)) return;
    try {
      await api("DELETE", `/contacts/${state.detail.contact.id}`);
      state.selectedId = null;
      state.detail = null;
      document.getElementById("detail").innerHTML =
        '<div class="empty-state"><p>Archived.</p></div>';
      await loadList(document.getElementById("q").value.trim());
    } catch (e) {
      alert("Archive failed: " + e.message);
    }
  }
  async function addActivityPrompt() {
    if (!state.detail) return;
    const kind = prompt("Kind (call / meeting / note / email_sent / email_received):", "note");
    if (!kind) return;
    const body = prompt("Body:");
    if (!body) return;
    try {
      // Activities endpoint uses the same pattern as contacts/:id/activities.
      // We POST to /contacts/:id/activities with { kind, body }.
      const id = state.detail.contact.id;
      await fetch(withProject(`/contacts/${id}/activities`), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ kind, body, source: "human" }),
      });
      // Reload the timeline.
      const r = await api("GET", `/contacts/${id}/activities`);
      state.detail.activities = r.activities || [];
      renderDetail();
    } catch (e) {
      alert("Log failed: " + e.message);
    }
  }

  async function newContactPrompt() {
    const first = prompt("First name:");
    if (!first) return;
    const email = prompt("Email (optional):", "");
    try {
      const body = {
        first_name: first,
        source: "human",
        channels: email ? [{ kind: "email", value: email, is_primary: true }] : [],
      };
      const r = await api("POST", "/contacts", body);
      await loadList();
      selectContact(r.contact.id);
    } catch (e) {
      alert("Create failed: " + e.message);
    }
  }

  // ────── Wire up ───────────────────────────────────────────────────
  document.getElementById("q").addEventListener("input", debounce((e) => loadList(e.target.value.trim()), 250));
  document.getElementById("new").addEventListener("click", newContactPrompt);

  function debounce(fn, ms) {
    let t;
    return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
  }

  if (!projectId) {
    document.getElementById("detail").innerHTML =
      '<div class="banner-error">Missing project_id in URL — the dashboard should provide one.</div>';
  } else {
    loadList();
  }
})();
