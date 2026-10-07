import React, { useCallback, useEffect, useMemo, useState } from "react";

const BASE = "/api/apps/actors";
const PAGE_SIZE = 50;
const h = React.createElement;

const STARTER_DEFINITION = {
  schema_version: 1,
  defaults: { start_url: "https://example.com", max_pages: 3 },
  presets: {},
  browser: { persist: false, viewport: { width: 1440, height: 900 } },
  allowed_hosts: ["example.com"],
  limits: { max_pages: "{{max_pages}}", max_items: 1000, max_duration_seconds: 600, step_retries: 2 },
  steps: [
    { action: "goto", url: "{{start_url}}" },
    { action: "extract", items: "body", fields: { title: { selector: "h1", type: "text", required: true }, url: { selector: "a", type: "url", attribute: "href" } } },
  ],
  output_schema: { title: "string", url: "url" },
};

export function endpoint(path, scope, params = {}) {
  const query = new URLSearchParams(params);
  if (scope.projectId) query.set("project_id", scope.projectId);
  if (scope.installId) query.set("install_id", String(scope.installId));
  return `${BASE}${path}${query.size ? `?${query}` : ""}`;
}

async function request(path, scope, options = {}, params = {}) {
  const response = await fetch(endpoint(path, scope, params), { credentials: "include", ...options });
  const text = await response.text();
  let body = {};
  try { body = text ? JSON.parse(text) : {}; } catch { body = { error: text }; }
  if (!response.ok || body.error) throw new Error(body.error || `HTTP ${response.status}`);
  return body;
}

function parseJSON(value, label) {
  try { return JSON.parse(value); } catch (error) { throw new Error(`${label}: ${error.message}`); }
}

function parsePresetPool(value) {
  return [...new Set(String(value || "").split(",").map((item) => item.trim()).filter(Boolean))];
}

function safeURL(value) {
  try { const parsed = new URL(String(value || "")); return ["http:", "https:"].includes(parsed.protocol) ? parsed.href : ""; } catch { return ""; }
}

function computerScreenshotURL(sessionId, projectId) {
  const query = new URLSearchParams({ t: String(Date.now()) });
  if (projectId) query.set("project_id", projectId);
  return `/api/apps/computer/sessions/${encodeURIComponent(sessionId)}/screenshot?${query}`;
}

function relativeTime(value) {
  const timestamp = new Date(value).getTime();
  if (!Number.isFinite(timestamp)) return value || "Unknown";
  const seconds = Math.round((timestamp - Date.now()) / 1000);
  const format = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  if (Math.abs(seconds) < 60) return format.format(seconds, "second");
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) return format.format(minutes, "minute");
  const hours = Math.round(minutes / 60);
  return Math.abs(hours) < 24 ? format.format(hours, "hour") : format.format(Math.round(hours / 24), "day");
}

function duration(ms) {
  const value = Number(ms);
  if (!Number.isFinite(value)) return "";
  if (value < 1000) return `${value} ms`;
  if (value < 60000) return `${(value / 1000).toFixed(1)} s`;
  return `${Math.floor(value / 60000)}m ${Math.round((value % 60000) / 1000)}s`;
}

function statusClass(status) {
  if (status === "completed") return "border-success/40 bg-success/10 text-success";
  if (status === "failed") return "border-red/40 bg-red/10 text-red";
  if (status === "running") return "border-info/40 bg-info/10 text-info";
  if (status === "queued") return "border-accent/40 bg-accent/10 text-accent";
  return "border-border bg-bg-input text-text-muted";
}

function Notice({ error, message }) {
  if (!error && !message) return null;
  return h("div", { role: error ? "alert" : "status", className: `mb-3 rounded border px-3 py-2 text-sm break-words ${error ? "border-red/40 bg-red/10 text-red" : "border-success/40 bg-success/10 text-success"}` }, error || message);
}

function Empty({ title, detail }) {
  return h("div", { className: "rounded border border-dashed border-border py-12 text-center" }, h("div", { className: "text-sm font-medium" }, title), h("div", { className: "mt-1 text-xs text-text-muted" }, detail));
}

function ActorsView({ scope, refreshRuns, onExplore }) {
  const [actors, setActors] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [editing, setEditing] = useState(null);
  const [editorOpen, setEditorOpen] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [definition, setDefinition] = useState(JSON.stringify(STARTER_DEFINITION, null, 2));
  const [input, setInput] = useState("{}");
  const [operation, setOperation] = useState("run");
  const [preset, setPreset] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setError("");
    try { const body = await request("/actors", scope); setActors(body.actors || []); }
    catch (err) { setError(String(err.message || err)); }
    finally { setLoading(false); }
  }, [scope]);
  useEffect(() => { load(); }, [load]);

  function edit(record) {
    setEditorOpen(true); setEditing(record || null); setName(record?.name || ""); setDescription(record?.description || "");
    setDefinition(JSON.stringify(record?.definition || STARTER_DEFINITION, null, 2)); setError(""); setMessage("");
  }

  async function save(event) {
    event.preventDefault(); setBusy(true); setError(""); setMessage("");
    try {
      const body = { id: editing?.id, expected_revision: editing?.revision, name, description, enabled: editing?.enabled ?? true, definition: parseJSON(definition, "Definition") };
      const result = await request("/actors", scope, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      edit(result.actor); setMessage(`Saved ${result.actor.name} revision ${result.actor.revision}.`); await load();
    } catch (err) { setError(String(err.message || err)); } finally { setBusy(false); }
  }

  async function run(id) {
    setBusy(true); setError(""); setMessage("");
    try {
      const result = await request("/actors/run", scope, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ actor_id: id, operation, preset: preset || undefined, input: parseJSON(input, "Run input") }) });
      setMessage(`Run #${result.run_id} is ${result.status}.`); refreshRuns?.();
    } catch (err) { setError(String(err.message || err)); } finally { setBusy(false); }
  }

  async function remove(record) {
    if (!window.confirm(`Delete actor “${record.name}”? Cancel its schedules first. Historical run snapshots will remain.`)) return;
    setBusy(true); setError(""); setMessage("");
    try { await request(`/actors/${record.id}`, scope, { method: "DELETE" }); setMessage(`Deleted ${record.name}.`); if (editing?.id === record.id) { setEditing(null); setEditorOpen(false); } await load(); }
    catch (err) { setError(String(err.message || err)); } finally { setBusy(false); }
  }

  return h("div", { className: "flex flex-wrap items-start gap-4" },
    h("section", { className: "min-w-0", style: { flex: "1 1 260px" } },
      h(Notice, { error, message }),
      h("div", { className: "mb-3 flex items-center gap-3" }, h("div", null, h("h2", { className: "font-semibold" }, "Actors"), h("p", { className: "text-xs text-text-muted" }, "Browser workflows and durable crawl definitions with immutable run snapshots.")), h("button", { type: "button", onClick: () => edit(null), className: "ml-auto rounded bg-accent px-3 py-1.5 text-sm text-white" }, "New actor")),
      loading ? h("div", { className: "py-10 text-center text-sm text-text-muted" }, "Loading actors…") : error && actors.length === 0 ? h("button", { type: "button", onClick: load, className: "rounded border border-border px-3 py-1.5 text-sm" }, "Retry loading") : actors.length === 0 ? h(Empty, { title: "No actors yet", detail: "Create a browser workflow or schema version 2 crawl." }) :
        h("div", { className: "grid gap-2" }, actors.map((record) => h("article", { key: record.id, className: "rounded border border-border bg-bg-card p-3" },
          h("div", { className: "flex gap-2" }, h("div", { className: "min-w-0 flex-1" }, h("div", { className: "font-medium" }, record.name), h("div", { className: "mt-0.5 text-xs text-text-muted" }, record.description || "No description")), h("span", { className: "text-xs text-text-dim" }, `rev ${record.revision}`)),
          h("div", { className: "mt-2 flex flex-wrap gap-2 text-xs text-text-muted" }, record.definition.crawl ? h("span", null, `${Object.keys(record.definition.crawl.routes || {}).length} routes · ${Object.keys(record.definition.crawl.datasets || {}).length} datasets`) : h("span", null, `${record.definition.steps?.length || 0} steps`), h("span", null, `${record.definition.allowed_hosts?.length || 0} hosts`), h("span", null, record.enabled ? "Enabled" : "Disabled")),
          h("div", { className: "mt-3 flex gap-2" }, h("button", { type: "button", onClick: () => edit(record), className: "rounded border border-border px-2 py-1 text-xs hover:bg-bg-input" }, "Edit"), h("button", { type: "button", disabled: busy || !record.enabled, onClick: () => run(record.id), className: "rounded border border-accent px-2 py-1 text-xs text-accent hover:bg-accent/10 disabled:opacity-50" }, "Test run"), h("button", { type: "button", onClick: () => onExplore({ actorId: record.id }), className: "rounded border border-border px-2 py-1 text-xs text-accent" }, "Explore data"), h("button", { type: "button", disabled: busy, onClick: () => remove(record), className: "ml-auto rounded border border-red px-2 py-1 text-xs text-red disabled:opacity-50" }, "Delete"))
        )))
    ),
    editorOpen && h("section", { className: "min-w-0 rounded border border-border bg-bg-card p-4", style: { flex: "1.4 1 360px" } },
      h("div", { className: "mb-1 flex items-center gap-3" }, h("h2", { className: "min-w-0 flex-1 font-semibold break-words" }, editing ? `Edit ${editing.name}` : "New actor"), h("button", { type: "button", onClick: () => setEditorOpen(false), className: "rounded border border-border px-2 py-1 text-xs" }, "Close")),
      h("p", { className: "mb-3 text-xs text-text-muted" }, editing ? `Saving creates revision ${editing.revision + 1}.` : "The definition is validated before it is saved."),
      h("form", { onSubmit: save, className: "grid gap-3" },
        h("label", { className: "grid gap-1 text-xs" }, "Name", h("input", { required: true, maxLength: 120, value: name, onChange: (e) => setName(e.target.value), className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" })),
        h("label", { className: "grid gap-1 text-xs" }, "Description", h("input", { value: description, onChange: (e) => setDescription(e.target.value), className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" })),
        h("label", { className: "grid gap-1 text-xs" }, "Definition JSON", h("textarea", { value: definition, onChange: (e) => setDefinition(e.target.value), rows: 14, spellCheck: false, className: "w-full min-w-0 max-h-96 resize-y rounded border border-border bg-bg-input px-2 py-2 font-mono text-xs" })),
        h("button", { disabled: busy, className: "rounded bg-accent px-3 py-2 text-sm text-white disabled:opacity-50" }, busy ? "Working…" : editing ? "Save revision" : "Create actor")
      ),
      h("div", { className: "mt-4 border-t border-border pt-3" }, h("h3", { className: "text-sm font-medium" }, "Test run options"), h("input", { value: operation, onChange: (e) => setOperation(e.target.value), "aria-label": "Operation", placeholder: "Operation (run)", className: "mt-2 rounded border border-border bg-bg-input p-2 text-sm" }),
        h("div", { className: "mt-2 grid gap-2 sm:grid-cols-2" }, h("input", { value: preset, onChange: (e) => setPreset(e.target.value), placeholder: "Preset (optional)", className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }), h("textarea", { value: input, onChange: (e) => setInput(e.target.value), rows: 3, spellCheck: false, "aria-label": "Run input JSON", className: "rounded border border-border bg-bg-input px-2 py-1.5 font-mono text-xs" }))
      )
    )
  );
}

function RunsView({ scope, refreshToken, onExplore }) {
  const [runs, setRuns] = useState([]); const [total, setTotal] = useState(0); const [loading, setLoading] = useState(true); const [error, setError] = useState("");
  const [status, setStatus] = useState("all"); const [query, setQuery] = useState(""); const [busy, setBusy] = useState(0);
  const load = useCallback(async (append = false) => {
    setError(""); try { const body = await request("/runs", scope, {}, { limit: PAGE_SIZE, offset: append ? runs.length : 0 }); setRuns((old) => append ? old.concat(body.runs || []) : (body.runs || [])); setTotal(body.total || 0); }
    catch (err) { setError(String(err.message || err)); } finally { setLoading(false); }
  }, [scope, runs.length]);
  useEffect(() => { load(false); }, [scope, refreshToken]);
  const active = runs.some((run) => run.status === "queued" || run.status === "running");
  useEffect(() => { if (!active) return undefined; const timer = setInterval(() => load(false), 3000); return () => clearInterval(timer); }, [active, scope]);
  const filtered = useMemo(() => runs.filter((run) => (status === "all" || run.status === status) && (!query || [run.kind, run.summary, run.error].join(" ").toLowerCase().includes(query.toLowerCase()))), [runs, status, query]);
  async function action(id, name) { setBusy(id); setError(""); try { await request(`/runs/${id}/${name}`, scope, { method: "POST" }); await load(false); } catch (err) { setError(String(err.message || err)); } finally { setBusy(0); } }
  return h("section", null,
    h("div", { className: "mb-3 flex flex-wrap gap-2" }, h("input", { type: "search", value: query, onChange: (e) => setQuery(e.target.value), placeholder: "Search runs", className: "min-w-48 flex-1 rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }), h("select", { value: status, onChange: (e) => setStatus(e.target.value), className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }, ["all", "queued", "running", "completed", "failed", "cancelled"].map((value) => h("option", { key: value, value }, value === "all" ? "All statuses" : value))), h("button", { onClick: () => load(false), className: "rounded border border-border px-3 py-1.5 text-sm" }, "Refresh")),
    h(Notice, { error }), loading ? h("div", { className: "py-12 text-center text-sm text-text-muted" }, "Loading runs…") : filtered.length === 0 ? h(Empty, { title: "No matching runs", detail: "Actor and Actors tool activity appears here." }) :
      h("div", { className: "grid gap-2" }, filtered.map((run) => {
        const details = run.details || {}; const live = run.status === "running" && details.session_id; const screenshot = safeURL(details.screenshot_url); const dataset = safeURL(details.dataset_url); const csv = safeURL(details.csv_url); const trace = safeURL(details.trace_url);
        return h("details", { key: run.id, className: "rounded border border-border bg-bg-card" },
          h("summary", { className: "cursor-pointer p-3" }, h("div", { className: "flex gap-2" }, h("div", { className: "min-w-0 flex-1" }, h("div", { className: "text-sm font-medium capitalize" }, run.kind === "actor" ? `Actor #${run.actor_id}` : run.kind), h("div", { className: "mt-1 text-xs text-text-muted break-words" }, run.summary || `Run #${run.id}`)), h("span", { className: `h-fit rounded border px-2 py-0.5 text-xs ${statusClass(run.status)}` }, run.status), h("button", { type: "button", onClick: e => { e.preventDefault(); e.stopPropagation(); onExplore({ runId: run.id }); }, className: "shrink-0 rounded border border-accent px-3 py-1 text-xs text-accent" }, "Explore data")), h("div", { className: "mt-2 text-xs text-text-dim" }, `${relativeTime(run.created_at)}${run.duration_ms != null ? ` • ${duration(run.duration_ms)}` : ""}`)),
          h("div", { className: "border-t border-border p-3" },
            h("div", { className: "grid gap-2 text-xs sm:grid-cols-3" }, [["Items", details.item_count], ["Pages", details.page_count], ["Revision", run.actor_revision], ["Preset", details.preset], ["Backend", details.browser_config?.backend || details.backend], ["Viewport", details.browser_config?.viewport ? `${details.browser_config.viewport.width}×${details.browser_config.viewport.height}` : undefined], ["Current URL", details.current_url]].filter(([, v]) => v !== undefined && v !== "").map(([label, value]) => h("div", { key: label, className: "rounded bg-bg-input p-2" }, h("div", { className: "text-text-dim" }, label), h("div", { className: "mt-0.5 break-all" }, String(value))))),
            details.browser_config?.environment && h("div", { className: "mt-3" }, h("div", { className: "mb-1 text-xs font-medium" }, "Resolved browser environment"), h("pre", { className: "max-h-48 overflow-auto rounded bg-bg-input p-2 text-xs" }, JSON.stringify(details.browser_config.environment, null, 2))),
            run.error && h("div", { className: "mt-3 border-l-2 border-red pl-2 text-xs text-red" }, run.error),
            live && h("div", { className: "mt-3" }, h("div", { className: "mb-1 text-xs text-text-muted" }, "Live Computer view"), h("img", { src: computerScreenshotURL(details.session_id, scope.projectId), alt: "Live actor browser", className: "max-h-80 rounded border border-border object-contain" })),
            screenshot && h("img", { src: screenshot, alt: "Final browser state", loading: "lazy", className: "mt-3 max-h-80 rounded border border-border object-contain" }),
            Array.isArray(details.items) && details.items.length > 0 && h("pre", { className: "mt-3 max-h-64 overflow-auto rounded bg-bg-input p-2 text-xs" }, JSON.stringify(details.items, null, 2)),
            Array.isArray(details.trace_preview) && details.trace_preview.length > 0 && h("div", { className: "mt-3" }, h("div", { className: "mb-1 text-xs font-medium" }, "Step timeline"), h("ol", { className: "grid gap-1" }, details.trace_preview.map((event, index) => h("li", { key: `${event.at || index}-${index}`, className: "flex flex-wrap gap-x-2 rounded bg-bg-input px-2 py-1 text-xs" }, h("span", { className: "font-medium" }, event.action), h("span", { className: event.status === "failed" ? "text-red" : "text-text-muted" }, event.status), event.duration_ms != null && h("span", { className: "text-text-dim" }, duration(event.duration_ms)), event.message && h("span", { className: "w-full break-words text-text-muted" }, event.message))))),
            h("div", { className: "mt-3 flex flex-wrap gap-2" }, [["Raw JSON", endpoint(`/runs/${run.id}/dataset`, scope)], ["Download JSONL", dataset], ["Download CSV", csv], ["Open trace", trace]].filter(([, href]) => href).map(([label, href]) => h("a", { key: label, href, target: "_blank", rel: "noopener noreferrer", className: "rounded border border-border px-2 py-1 text-xs text-accent" }, label)), (run.status === "queued" || run.status === "running") && h("button", { disabled: busy === run.id, onClick: () => action(run.id, "cancel"), className: "rounded border border-red px-2 py-1 text-xs text-red" }, "Cancel"), ["completed", "failed", "cancelled"].includes(run.status) && run.actor_id && h("button", { disabled: busy === run.id, onClick: () => action(run.id, "retry"), className: "rounded border border-border px-2 py-1 text-xs" }, "Retry"))
          )
        );
      })),
    runs.length < total && h("div", { className: "mt-4 text-center" }, h("button", { onClick: () => load(true), className: "rounded border border-border px-4 py-2 text-sm" }, "Load more"))
  );
}

function SchedulesView({ scope, actors }) {
  const [jobs, setJobs] = useState([]); const [error, setError] = useState(""); const [message, setMessage] = useState(""); const [busy, setBusy] = useState(false);
  const [history, setHistory] = useState({});
  const [operation, setOperation] = useState("run"); const [actorId, setActorId] = useState(""); const [name, setName] = useState(""); const [schedule, setSchedule] = useState(JSON.stringify({ kind: "every", every_seconds: 8640 }, null, 2)); const [timezone, setTimezone] = useState("UTC"); const [presetPool, setPresetPool] = useState(""); const [input, setInput] = useState("{}");
  const load = useCallback(async () => { setError(""); try { const body = await request("/schedules", scope); setJobs(body.jobs || []); } catch (err) { setError(String(err.message || err)); } }, [scope]);
  useEffect(() => { load(); }, [load]);
  async function create(event) { event.preventDefault(); setBusy(true); setError(""); setMessage(""); try { const pool = parsePresetPool(presetPool); const body = { actor_id: Number(actorId), name: name || undefined, schedule: parseJSON(schedule, "Schedule"), timezone, preset_pool: pool.length ? pool : undefined, input: parseJSON(input, "Input") }; const out = await request("/schedules", scope, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); setMessage(out.job?.id ? `Created schedule #${out.job.id}.` : "Created schedule."); await load(); } catch (err) { setError(String(err.message || err)); } finally { setBusy(false); } }
  async function scheduleAction(id, action) { setBusy(true); setError(""); try { await request(`/schedules/${id}/${action}`, scope, { method: "POST" }); setMessage(action === "run" ? "Immediate delivery queued." : "Schedule cancelled."); await load(); } catch (err) { setError(String(err.message || err)); } finally { setBusy(false); } }
  async function loadHistory(id) { setError(""); try { const body = await request("/schedules", scope, {}, { job_id: id }); setHistory((current) => ({ ...current, [id]: body.runs || [] })); } catch (err) { setError(String(err.message || err)); } }
  return h("div", { className: "grid gap-4 xl:grid-cols-[minmax(0,1fr)_380px]" },
    h("section", null, h("h2", { className: "mb-3 font-semibold" }, "Schedules"), jobs.length === 0 ? h(Empty, { title: "No actor schedules", detail: "Jobs owns schedule timing, delivery retries, and trigger history." }) : h("div", { className: "grid gap-2" }, jobs.map((job) => { const targetInput = job.target?.input || {}; const deliveryRuns = history[job.id]; const cadence = job.every_seconds ? `every ${job.every_seconds}s` : job.cron_expr ? `cron ${job.cron_expr}` : job.schedule_kind; const profileLabel = Array.isArray(targetInput.preset_pool) && targetInput.preset_pool.length ? ` • rotates ${targetInput.preset_pool.join(", ")}` : targetInput.preset ? ` • ${targetInput.preset}` : ""; return h("article", { key: job.id, className: "rounded border border-border bg-bg-card p-3" }, h("div", { className: "flex gap-2" }, h("div", { className: "min-w-0 flex-1" }, h("div", { className: "font-medium" }, job.name), h("div", { className: "mt-1 text-xs text-text-muted" }, `${cadence} • ${job.timezone || "UTC"}`)), h("span", { className: `h-fit rounded border px-2 py-0.5 text-xs ${statusClass(job.status)}` }, job.status)), h("div", { className: "mt-2 text-xs text-text-dim" }, `Actor #${targetInput.actor_id || "—"}${profileLabel} • Next ${job.next_run_at ? relativeTime(job.next_run_at) : "—"} • Last ${job.last_status || "—"}`), job.last_error && h("div", { className: "mt-2 text-xs text-red" }, job.last_error), deliveryRuns && h("div", { className: "mt-2 rounded bg-bg-input p-2 text-xs" }, deliveryRuns.length ? deliveryRuns.slice(0, 5).map((run) => h("div", { key: run.id, className: "flex justify-between gap-2 py-0.5" }, h("span", null, relativeTime(run.started_at)), h("span", null, run.status))) : "No deliveries yet."), h("div", { className: "mt-3 flex flex-wrap gap-2" }, h("button", { onClick: () => loadHistory(job.id), className: "rounded border border-border px-2 py-1 text-xs" }, "Delivery history"), h("button", { disabled: busy || job.status === "cancelled", onClick: () => scheduleAction(job.id, "run"), className: "rounded border border-accent px-2 py-1 text-xs text-accent disabled:opacity-50" }, "Run now"), h("button", { disabled: busy || job.status === "cancelled", onClick: () => scheduleAction(job.id, "cancel"), className: "rounded border border-red px-2 py-1 text-xs text-red disabled:opacity-50" }, "Pause / cancel"))); }))),
    h("section", { className: "rounded border border-border bg-bg-card p-4" }, h("h2", { className: "font-semibold" }, "New schedule"), h("p", { className: "mb-3 text-xs text-text-muted" }, "Jobs delivers triggers; Actors deterministically selects one profile from the optional preset pool for each occurrence."), h(Notice, { error, message }), h("form", { onSubmit: create, className: "grid gap-3" }, h("select", { required: true, value: actorId, onChange: (e) => setActorId(e.target.value), className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }, h("option", { value: "" }, "Choose actor"), actors.map((record) => h("option", { key: record.id, value: record.id }, record.name))), h("input", { value: name, onChange: (e) => setName(e.target.value), placeholder: "Schedule name", className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }), h("input", { value: operation, onChange: (e) => setOperation(e.target.value), "aria-label": "Scheduled operation", placeholder: "Operation", className: "rounded border border-border bg-bg-input p-2 text-sm" }), h("label", { className: "grid gap-1 text-xs" }, "Schedule JSON", h("textarea", { value: schedule, onChange: (e) => setSchedule(e.target.value), rows: 5, spellCheck: false, className: "rounded border border-border bg-bg-input p-2 font-mono text-xs" })), h("input", { value: timezone, onChange: (e) => setTimezone(e.target.value), placeholder: "Timezone", className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }), h("label", { className: "grid gap-1 text-xs" }, "Preset rotation pool", h("input", { value: presetPool, onChange: (e) => setPresetPool(e.target.value), placeholder: "fr_desktop, fr_mobile", className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }), h("span", { className: "text-text-dim" }, "Comma-separated preset names. One is chosen deterministically per scheduled occurrence.")), h("label", { className: "grid gap-1 text-xs" }, "Input overrides", h("textarea", { value: input, onChange: (e) => setInput(e.target.value), rows: 4, spellCheck: false, className: "rounded border border-border bg-bg-input p-2 font-mono text-xs" })), h("button", { disabled: busy, className: "rounded bg-accent px-3 py-2 text-sm text-white disabled:opacity-50" }, busy ? "Creating…" : "Create schedule")))
  );
}

function TasksView({ scope, actors, onRun }) {
  const [tasks, setTasks] = useState([]);
  const [name, setName] = useState("");
  const [actorId, setActorId] = useState("");
  const [operation, setOperation] = useState("run");
  const [input, setInput] = useState("{}");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    try { const data = await request("/tasks", scope); setTasks(data.tasks || []); }
    catch (err) { setError(err.message); }
  }, [scope]);
  useEffect(() => { load(); }, [load]);
  async function save(event) {
    event.preventDefault(); setBusy(true); setError("");
    try {
      await request("/tasks", scope, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, actor_id: Number(actorId), operation, input: parseJSON(input, "Input") }) });
      setName(""); await load();
    } catch (err) { setError(err.message); } finally { setBusy(false); }
  }
  async function action(task, kind) {
    setBusy(true); setError("");
    try {
      if (kind === "run") {
        await request(`/tasks/${task.id}/run`, scope, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" }); onRun();
      } else {
        await request(`/tasks/${task.id}`, scope, { method: "DELETE" }); await load();
      }
    } catch (err) { setError(err.message); } finally { setBusy(false); }
  }
  const fieldClass = "rounded border border-border bg-bg-input p-2 text-sm";
  return h("div", { className: "grid gap-4 lg:grid-cols-2" },
    h("section", { className: "grid content-start gap-3" },
      h("h2", { className: "font-semibold" }, "Saved tasks"),
      tasks.length === 0 && h(Empty, { title: "No tasks yet", detail: "Save inputs for an operation. Tasks keep the actor revision you selected." }),
      tasks.map((task) => h("article", { key: task.id, className: "rounded border border-border bg-bg-card p-3" },
        h("div", { className: "font-medium" }, task.name),
        h("p", { className: "my-2 text-xs text-text-muted" }, `Actor #${task.actor_id} · ${task.operation} · revision ${task.revision}`),
        h("div", { className: "flex gap-2" },
          h("button", { disabled: busy, onClick: () => action(task, "run"), className: fieldClass }, "Run task"),
          h("button", { disabled: busy, onClick: () => action(task, "delete"), className: fieldClass }, "Delete task"))))) ,
    h("form", { onSubmit: save, className: "grid content-start gap-3 rounded border border-border bg-bg-card p-4" },
      h("h2", { className: "font-semibold" }, "New task"), h(Notice, { error }),
      h("label", null, "Name", h("input", { required: true, value: name, maxLength: 120, onChange: (e) => setName(e.target.value), className: `${fieldClass} w-full` })),
      h("label", null, "Actor", h("select", { required: true, value: actorId, onChange: (e) => {
        setActorId(e.target.value); const actor = actors.find((item) => String(item.id) === e.target.value); setOperation(Object.keys(actor?.definition.operations || {})[0] || "run");
      }, className: `${fieldClass} w-full` }, h("option", { value: "" }, "Choose actor"), actors.map((actor) => h("option", { key: actor.id, value: actor.id }, `${actor.name} (revision ${actor.revision})`)))),
      h("label", null, "Operation", h("input", { value: operation, required: true, onChange: (e) => setOperation(e.target.value), className: `${fieldClass} w-full` })),
      h("label", null, "Input JSON", h("textarea", { value: input, rows: 8, spellCheck: false, onChange: (e) => setInput(e.target.value), className: `${fieldClass} w-full font-mono` })),
      h("button", { disabled: busy, className: "rounded bg-accent p-2 text-white disabled:opacity-50" }, "Save task"))) ;
}

export function dataColumns(items, schema = {}) {
  const keys = [...new Set([...Object.keys(schema || {}), ...items.flatMap(item => Object.keys(item))])];
  return keys.sort((a, b) => (a === "name" ? -1 : b === "name" ? 1 : 0));
}

export function csvRows(items, columns) {
  const cell = value => {
    const raw = value == null ? "" : typeof value === "object" ? JSON.stringify(value) : String(value);
    // Spreadsheet exports treat text beginning with formula markers as text.
    return '"' + (/^[=+@-]/.test(raw) && typeof value === "string" ? "'" : "") + raw.replace(/"/g, '""') + '"';
  };
  return [columns.map(cell).join(","), ...items.map(item => columns.map(key => cell(item[key])).join(","))].join("\r\n");
}

function DataView({ scope, actors, selection }) {
  const [runs, setRuns] = useState([]); const [runTotal, setRunTotal] = useState(0);
  const [runId, setRunId] = useState(selection?.runId || 0);
  const [dataset, setDataset] = useState(""); const [datasets, setDatasets] = useState([]);
  const [items, setItems] = useState([]); const [total, setTotal] = useState(0);
  const [cursor, setCursor] = useState(0); const [nextCursor, setNextCursor] = useState(0); const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true); const [error, setError] = useState(""); const [query, setQuery] = useState(""); const [inspected, setInspected] = useState(null);
  useEffect(() => {
    let cancelled = false;
    request("/runs", scope, {}, { limit: PAGE_SIZE }).then(body => {
      if (cancelled) return;
      const list = body.runs || []; setRuns(list); setRunTotal(body.total || 0);
      const candidates = selection?.actorId ? list.filter(run => run.actor_id === selection.actorId) : list;
      const initial = selection?.runId || (candidates.find(run => Number(run.details?.item_count) > 0) || candidates[0])?.id || 0;
      setRunId(initial); if (!initial) setLoading(false);
    }).catch(err => { if (!cancelled) { setError(err.message); setLoading(false); } });
    return () => { cancelled = true; };
  }, [scope, selection]);
  useEffect(() => {
    if (!runId) return;
    let cancelled = false; setLoading(true); setError("");
    request(`/runs/${runId}/dataset`, scope, {}, { limit: PAGE_SIZE, after: cursor, ...(dataset ? { dataset } : {}) }).then(body => {
      if (cancelled) return;
      setDatasets(body.datasets || []);
      if (!dataset && body.datasets?.length) { setDataset(body.datasets[0].name); return; }
      setItems(previous => cursor ? [...previous, ...(body.items || [])] : (body.items || []));
      setTotal(body.total || 0); setNextCursor(body.next_cursor || 0); setHasMore(Boolean(body.has_more)); setLoading(false);
    }).catch(err => { if (!cancelled) { setError(err.message); setLoading(false); } });
    return () => { cancelled = true; };
  }, [scope, runId, dataset, cursor]);
  const schema = datasets.find(item => item.name === dataset)?.schema || {};
  const columns = useMemo(() => dataColumns(items, schema), [items, schema]);
  const filtered = useMemo(() => items.filter(item => !query || JSON.stringify(item).toLowerCase().includes(query.toLowerCase())), [items, query]);
  const run = runs.find(item => item.id === runId);
  const actorName = id => actors.find(actor => actor.id === id)?.name || `Actor #${id}`;
  const fieldClass = "min-w-0 rounded border border-border bg-bg-input px-3 py-2 text-sm";
  function resetData() { setLoading(false); setError(""); setItems([]); setDatasets([]); setTotal(0); setCursor(0); setNextCursor(0); setHasMore(false); setQuery(""); setInspected(null); }
  async function olderRuns() {
    setError("");
    try { const body = await request("/runs", scope, {}, { limit: PAGE_SIZE, offset: runs.length }); setRuns(previous => [...previous, ...(body.runs || [])]); setRunTotal(body.total || 0); }
    catch (err) { setError(err.message); }
  }
  function download(format) {
    const content = format === "csv" ? csvRows(filtered, columns) : JSON.stringify(filtered, null, 2);
    const url = URL.createObjectURL(new Blob([content], { type: format === "csv" ? "text/csv;charset=utf-8" : "application/json" }));
    const link = document.createElement("a"); link.href = url; link.download = `${(dataset || "dataset").replace(/[^a-zA-Z0-9_-]/g, "_")}-run-${runId}.${format}`; link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  return h("section", { className: "min-w-0" },
    h("h2", { className: "font-semibold" }, "Explore data"),
    h("p", { className: "mb-4 text-xs text-text-muted" }, "Choose a run and dataset to browse its saved rows. Each run keeps its own results, including partial results."),
    h(Notice, { error }),
    h("div", { className: "mb-3 flex flex-wrap gap-3" },
      h("label", { className: "grid min-w-0 flex-1 gap-1 text-xs", style: { flexBasis: 260 } }, "Run", h("select", { value: runId, onChange: e => { resetData(); setDataset(""); setRunId(Number(e.target.value)); }, className: fieldClass }, h("option", { value: 0 }, "Choose run"), runs.map(item => h("option", { key: item.id, value: item.id }, `Run #${item.id} · ${item.actor_id ? actorName(item.actor_id) : item.kind} · ${item.details?.item_count ?? 0} rows · ${item.status}`)))),
      h("label", { className: "grid min-w-0 flex-1 gap-1 text-xs", style: { flexBasis: 220 } }, "Dataset", h("select", { "aria-label": "Dataset", value: dataset, disabled: !datasets.length, onChange: e => { setItems([]); setCursor(0); setQuery(""); setInspected(null); setDataset(e.target.value); }, className: fieldClass }, !datasets.length && h("option", { value: "" }, "No datasets"), datasets.map(item => h("option", { key: item.name, value: item.name }, `${item.name} (${item.count} rows)`)))),
      runs.length < runTotal && h("button", { type: "button", onClick: olderRuns, className: fieldClass }, "Load older runs")
    ),
    run && h("p", { className: "mb-3 text-xs text-text-muted" }, `Run #${run.id} · ${run.status} · ${relativeTime(run.created_at)}`),
    runId ? h(React.Fragment, null,
      h("div", { className: "mb-3 flex flex-wrap items-center gap-2" },
        h("input", { type: "search", placeholder: "Search loaded rows", "aria-label": "Search loaded rows", value: query, onChange: e => setQuery(e.target.value), className: `${fieldClass} flex-1` }),
        h("button", { type: "button", disabled: !filtered.length || loading, onClick: () => download("json"), className: `${fieldClass} disabled:opacity-50` }, "Export JSON"),
        h("button", { type: "button", disabled: !filtered.length || loading, onClick: () => download("csv"), className: `${fieldClass} disabled:opacity-50` }, "Export CSV")
      ),
      h("p", { className: "mb-3 text-xs text-text-muted", role: "status" }, `${filtered.length} shown · ${items.length} loaded · ${total} rows in dataset. Exports include the shown rows.`),
      loading && h("p", { className: "mb-3 text-xs text-text-muted" }, "Loading data…"),
      items.length > 0 ? h("div", { className: "max-w-full overflow-auto rounded border border-border", style: { maxHeight: 480 } },
        h("table", { className: "w-full text-left text-xs", "aria-label": `${dataset} rows` },
          h("thead", { className: "sticky top-0 bg-bg-card" }, h("tr", null, h("th", { className: "border-b border-border px-3 py-2" }, "Row"), columns.map(key => h("th", { key, className: "border-b border-border px-3 py-2 whitespace-nowrap" }, key.replace(/_/g, " "), schema[key] && h("div", { className: "mt-1 font-normal text-text-dim" }, schema[key]))))),
          h("tbody", null, filtered.map((item, index) => h("tr", { key: index, className: "hover:bg-bg-card" },
            h("td", { className: "border-b border-border px-3 py-2" }, h("button", { type: "button", onClick: () => setInspected(item), className: "text-accent whitespace-nowrap", "aria-label": `View row ${index + 1}` }, `View ${index + 1}`)),
            columns.map(key => { const value = item[key]; const text = value === null ? "null" : value === undefined ? "—" : typeof value === "object" ? JSON.stringify(value) : value === "" ? '""' : String(value); const url = typeof value === "string" ? safeURL(value) : "";
              return h("td", { key, title: text, className: `border-b border-border px-3 py-2 whitespace-nowrap ${value == null ? "text-text-dim" : ""}` }, url ? h("a", { href: url, target: "_blank", rel: "noopener noreferrer", className: "text-accent" }, text.slice(0, 90)) : text.slice(0, 160));
            })
          )))
        )
      ) : !loading && !error && h(Empty, { title: "No saved rows", detail: "This run has no extracted data yet. Choose another run or execute an actor." }),
      items.length > 0 && filtered.length === 0 && h("p", { className: "mt-3 text-sm text-text-muted" }, "No loaded rows match your search."),
      hasMore && h("button", { type: "button", disabled: loading, onClick: () => setCursor(nextCursor), className: `${fieldClass} mt-3 disabled:opacity-50` }, loading ? "Loading…" : "Load more rows"),
      inspected && h("div", { className: "mt-4 rounded border border-border bg-bg-card p-3" }, h("div", { className: "mb-2 flex items-center justify-between" }, h("h3", { className: "text-sm font-semibold" }, "Row details"), h("button", { type: "button", onClick: () => setInspected(null), className: fieldClass }, "Close details")), h("pre", { className: "max-h-80 overflow-auto text-xs" }, JSON.stringify(inspected, null, 2)))
    ) : !loading && !error && h(Empty, { title: "No runs selected", detail: "Run an actor to collect data, or choose a run above." })
  );
}

// The dashboard owns mounting. Importing this module must never alter its root.
function ActorsPanel({ projectId, installId } = {}) {
  const scope = useMemo(() => ({ projectId, installId }), [projectId, installId]);
  const [selection, setSelection] = useState(null);
  const explore = value => { setSelection(value); setTab("data"); };
  const [tab, setTab] = useState("actors"); const [refreshRuns, setRefreshRuns] = useState(0); const [actors, setActors] = useState([]);
  useEffect(() => { if (!installId) return; request("/actors", scope).then((body) => setActors(body.actors || [])).catch(() => {}); }, [scope, tab]);
  if (!installId) return h(Notice, { error: "Open Actors from an installed app panel." });
  return h("div", { className: "flex h-full min-h-0 min-w-0 flex-col overflow-hidden bg-bg text-text" },
    h("header", { className: "shrink-0 border-b border-border bg-bg px-4 pt-3" }, h("div", null, h("h1", { className: "text-lg font-semibold" }, "Actors"), h("p", { className: "text-xs text-text-muted" }, "Browser workflows, durable crawls, structured datasets, and schedules")), h("nav", { className: "mt-3 flex gap-1", "aria-label": "Actors sections" }, [["actors", "Actors"], ["tasks", "Tasks"], ["runs", "Runs"], ["data", "Data"], ["schedules", "Schedules"]].map(([id, label]) => h("button", { key: id, type: "button", onClick: () => { if (id === "data") setSelection(null); setTab(id); }, className: `border-b-2 px-3 py-2 text-sm ${tab === id ? "border-accent text-text" : "border-transparent text-text-muted hover:text-text"}` }, label)))),
    h("main", { className: "min-h-0 min-w-0 flex-1 overflow-auto p-4" }, tab === "actors" ? h(ActorsView, { scope, onExplore: explore, refreshRuns: () => { setRefreshRuns((v) => v + 1); setTab("runs"); } }) : tab === "tasks" ? h(TasksView, { scope, actors, onRun: () => { setRefreshRuns((v) => v + 1); setTab("runs"); } }) : tab === "runs" ? h(RunsView, { scope, refreshToken: refreshRuns, onExplore: explore }) : tab === "data" ? h(DataView, { scope, actors, selection }) : h(SchedulesView, { scope, actors }))
  );
}

export default ActorsPanel;
