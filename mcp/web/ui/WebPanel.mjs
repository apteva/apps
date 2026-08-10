import React, { useCallback, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";

const BASE = "/api/apps/web";
const PAGE_SIZE = 50;
const h = React.createElement;

const STARTER_DEFINITION = {
  schema_version: 1,
  defaults: { start_url: "https://example.com", max_pages: 3 },
  presets: {},
  browser: { backend: "local", proxy_mode: "none", persist: false, viewport: { width: 1440, height: 900 } },
  allowed_hosts: ["example.com"],
  limits: { max_pages: "{{max_pages}}", max_items: 1000, max_duration_seconds: 600, step_retries: 2 },
  steps: [
    { action: "goto", url: "{{start_url}}" },
    { action: "extract", items: "article", fields: { title: { selector: "h2", type: "text", required: true }, url: { selector: "a", type: "url", attribute: "href" } } },
    { action: "paginate", locator: { text: "Next", role: "button" }, max_pages: "{{max_pages}}", optional: true },
  ],
  output_schema: { title: "string", url: "url" },
};

function endpoint(path, projectId, params = {}) {
  const query = new URLSearchParams(params);
  if (projectId) query.set("project_id", projectId);
  return `${BASE}${path}${query.size ? `?${query}` : ""}`;
}

async function request(path, projectId, options = {}, params = {}) {
  const response = await fetch(endpoint(path, projectId, params), { credentials: "include", ...options });
  const text = await response.text();
  let body = {};
  try { body = text ? JSON.parse(text) : {}; } catch { body = { error: text }; }
  if (!response.ok || body.error) throw new Error(body.error || `HTTP ${response.status}`);
  return body;
}

function parseJSON(value, label) {
  try { return JSON.parse(value); } catch (error) { throw new Error(`${label}: ${error.message}`); }
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

function ExtractorsView({ projectId, refreshRuns }) {
  const [extractors, setExtractors] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [editing, setEditing] = useState(null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [definition, setDefinition] = useState(JSON.stringify(STARTER_DEFINITION, null, 2));
  const [input, setInput] = useState("{}");
  const [preset, setPreset] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setError("");
    try { const body = await request("/extractors", projectId); setExtractors(body.extractors || []); }
    catch (err) { setError(String(err.message || err)); }
    finally { setLoading(false); }
  }, [projectId]);
  useEffect(() => { load(); }, [load]);

  function edit(record) {
    setEditing(record || null); setName(record?.name || ""); setDescription(record?.description || "");
    setDefinition(JSON.stringify(record?.definition || STARTER_DEFINITION, null, 2)); setError(""); setMessage("");
  }

  async function save(event) {
    event.preventDefault(); setBusy(true); setError(""); setMessage("");
    try {
      const body = { id: editing?.id, expected_revision: editing?.revision, name, description, enabled: editing?.enabled ?? true, definition: parseJSON(definition, "Definition") };
      const result = await request("/extractors", projectId, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      edit(result.extractor); setMessage(`Saved ${result.extractor.name} revision ${result.extractor.revision}.`); await load();
    } catch (err) { setError(String(err.message || err)); } finally { setBusy(false); }
  }

  async function run(id) {
    setBusy(true); setError(""); setMessage("");
    try {
      const result = await request("/extractors/run", projectId, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ extractor_id: id, preset: preset || undefined, input: parseJSON(input, "Run input") }) });
      setMessage(`Run #${result.run_id} is ${result.status}.`); refreshRuns?.();
    } catch (err) { setError(String(err.message || err)); } finally { setBusy(false); }
  }

  async function remove(record) {
    if (!window.confirm(`Delete extractor “${record.name}”? Cancel its schedules first. Historical run snapshots will remain.`)) return;
    setBusy(true); setError(""); setMessage("");
    try { await request(`/extractors/${record.id}`, projectId, { method: "DELETE" }); setMessage(`Deleted ${record.name}.`); if (editing?.id === record.id) edit(null); await load(); }
    catch (err) { setError(String(err.message || err)); } finally { setBusy(false); }
  }

  return h("div", { className: "grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(360px,1fr)]" },
    h("section", null,
      h("div", { className: "mb-3 flex items-center" }, h("div", null, h("h2", { className: "font-semibold" }, "Extractors"), h("p", { className: "text-xs text-text-muted" }, "Reusable browser workflows with immutable run snapshots.")), h("button", { type: "button", onClick: () => edit(null), className: "ml-auto rounded bg-accent px-3 py-1.5 text-sm text-white" }, "New extractor")),
      loading ? h("div", { className: "py-10 text-center text-sm text-text-muted" }, "Loading extractors…") : extractors.length === 0 ? h(Empty, { title: "No extractors yet", detail: "Create one from the version 1 starter definition." }) :
        h("div", { className: "grid gap-2" }, extractors.map((record) => h("article", { key: record.id, className: "rounded border border-border bg-bg-card p-3" },
          h("div", { className: "flex gap-2" }, h("div", { className: "min-w-0 flex-1" }, h("div", { className: "font-medium" }, record.name), h("div", { className: "mt-0.5 text-xs text-text-muted" }, record.description || "No description")), h("span", { className: "text-xs text-text-dim" }, `rev ${record.revision}`)),
          h("div", { className: "mt-2 flex flex-wrap gap-2 text-xs text-text-muted" }, h("span", null, `${record.definition.steps?.length || 0} steps`), h("span", null, `${record.definition.allowed_hosts?.length || 0} hosts`), h("span", null, record.enabled ? "Enabled" : "Disabled")),
          h("div", { className: "mt-3 flex gap-2" }, h("button", { type: "button", onClick: () => edit(record), className: "rounded border border-border px-2 py-1 text-xs hover:bg-bg-input" }, "Edit"), h("button", { type: "button", disabled: busy || !record.enabled, onClick: () => run(record.id), className: "rounded border border-accent px-2 py-1 text-xs text-accent hover:bg-accent/10 disabled:opacity-50" }, "Test run"), h("button", { type: "button", disabled: busy, onClick: () => remove(record), className: "ml-auto rounded border border-red px-2 py-1 text-xs text-red disabled:opacity-50" }, "Delete"))
        )))
    ),
    h("section", { className: "rounded border border-border bg-bg-card p-4" },
      h("h2", { className: "font-semibold" }, editing ? `Edit ${editing.name}` : "New extractor"),
      h("p", { className: "mb-3 text-xs text-text-muted" }, editing ? `Saving creates revision ${editing.revision + 1}.` : "The definition is validated before it is saved."),
      h(Notice, { error, message }),
      h("form", { onSubmit: save, className: "grid gap-3" },
        h("label", { className: "grid gap-1 text-xs" }, "Name", h("input", { required: true, maxLength: 120, value: name, onChange: (e) => setName(e.target.value), className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" })),
        h("label", { className: "grid gap-1 text-xs" }, "Description", h("input", { value: description, onChange: (e) => setDescription(e.target.value), className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" })),
        h("label", { className: "grid gap-1 text-xs" }, "Definition JSON", h("textarea", { value: definition, onChange: (e) => setDefinition(e.target.value), rows: 20, spellCheck: false, className: "min-h-80 rounded border border-border bg-bg-input px-2 py-2 font-mono text-xs" })),
        h("button", { disabled: busy, className: "rounded bg-accent px-3 py-2 text-sm text-white disabled:opacity-50" }, busy ? "Working…" : editing ? "Save revision" : "Create extractor")
      ),
      h("div", { className: "mt-4 border-t border-border pt-3" }, h("h3", { className: "text-sm font-medium" }, "Test run options"),
        h("div", { className: "mt-2 grid gap-2 sm:grid-cols-2" }, h("input", { value: preset, onChange: (e) => setPreset(e.target.value), placeholder: "Preset (optional)", className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }), h("textarea", { value: input, onChange: (e) => setInput(e.target.value), rows: 3, spellCheck: false, "aria-label": "Run input JSON", className: "rounded border border-border bg-bg-input px-2 py-1.5 font-mono text-xs" }))
      )
    )
  );
}

function RunsView({ projectId, refreshToken }) {
  const [runs, setRuns] = useState([]); const [total, setTotal] = useState(0); const [loading, setLoading] = useState(true); const [error, setError] = useState("");
  const [status, setStatus] = useState("all"); const [query, setQuery] = useState(""); const [busy, setBusy] = useState(0);
  const load = useCallback(async (append = false) => {
    setError(""); try { const body = await request("/runs", projectId, {}, { limit: PAGE_SIZE, offset: append ? runs.length : 0 }); setRuns((old) => append ? old.concat(body.runs || []) : (body.runs || [])); setTotal(body.total || 0); }
    catch (err) { setError(String(err.message || err)); } finally { setLoading(false); }
  }, [projectId, runs.length]);
  useEffect(() => { load(false); }, [projectId, refreshToken]);
  const active = runs.some((run) => run.status === "queued" || run.status === "running");
  useEffect(() => { if (!active) return undefined; const timer = setInterval(() => load(false), 3000); return () => clearInterval(timer); }, [active, projectId]);
  const filtered = useMemo(() => runs.filter((run) => (status === "all" || run.status === status) && (!query || [run.kind, run.summary, run.error].join(" ").toLowerCase().includes(query.toLowerCase()))), [runs, status, query]);
  async function action(id, name) { setBusy(id); setError(""); try { await request(`/runs/${id}/${name}`, projectId, { method: "POST" }); await load(false); } catch (err) { setError(String(err.message || err)); } finally { setBusy(0); } }
  return h("section", null,
    h("div", { className: "mb-3 flex flex-wrap gap-2" }, h("input", { type: "search", value: query, onChange: (e) => setQuery(e.target.value), placeholder: "Search runs", className: "min-w-48 flex-1 rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }), h("select", { value: status, onChange: (e) => setStatus(e.target.value), className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }, ["all", "queued", "running", "completed", "failed", "cancelled"].map((value) => h("option", { key: value, value }, value === "all" ? "All statuses" : value))), h("button", { onClick: () => load(false), className: "rounded border border-border px-3 py-1.5 text-sm" }, "Refresh")),
    h(Notice, { error }), loading ? h("div", { className: "py-12 text-center text-sm text-text-muted" }, "Loading runs…") : filtered.length === 0 ? h(Empty, { title: "No matching runs", detail: "Extractor and Web tool activity appears here." }) :
      h("div", { className: "grid gap-2" }, filtered.map((run) => {
        const details = run.details || {}; const live = run.status === "running" && details.session_id; const screenshot = safeURL(details.screenshot_url); const dataset = safeURL(details.dataset_url); const csv = safeURL(details.csv_url); const trace = safeURL(details.trace_url);
        return h("details", { key: run.id, className: "rounded border border-border bg-bg-card" },
          h("summary", { className: "cursor-pointer list-none p-3" }, h("div", { className: "flex gap-2" }, h("div", { className: "min-w-0 flex-1" }, h("div", { className: "text-sm font-medium capitalize" }, run.kind === "extractor" ? `Extractor #${run.extractor_id}` : run.kind), h("div", { className: "mt-1 text-xs text-text-muted break-words" }, run.summary || `Run #${run.id}`)), h("span", { className: `h-fit rounded border px-2 py-0.5 text-xs ${statusClass(run.status)}` }, run.status)), h("div", { className: "mt-2 text-xs text-text-dim" }, `${relativeTime(run.created_at)}${run.duration_ms != null ? ` • ${duration(run.duration_ms)}` : ""}`)),
          h("div", { className: "border-t border-border p-3" },
            h("div", { className: "grid gap-2 text-xs sm:grid-cols-3" }, [["Items", details.item_count], ["Pages", details.page_count], ["Revision", run.extractor_revision], ["Backend", details.backend], ["Current URL", details.current_url]].filter(([, v]) => v !== undefined && v !== "").map(([label, value]) => h("div", { key: label, className: "rounded bg-bg-input p-2" }, h("div", { className: "text-text-dim" }, label), h("div", { className: "mt-0.5 break-all" }, String(value))))),
            run.error && h("div", { className: "mt-3 border-l-2 border-red pl-2 text-xs text-red" }, run.error),
            live && h("div", { className: "mt-3" }, h("div", { className: "mb-1 text-xs text-text-muted" }, "Live Computer view"), h("img", { src: computerScreenshotURL(details.session_id, projectId), alt: "Live extractor browser", className: "max-h-80 rounded border border-border object-contain" })),
            screenshot && h("img", { src: screenshot, alt: "Final browser state", loading: "lazy", className: "mt-3 max-h-80 rounded border border-border object-contain" }),
            Array.isArray(details.items) && details.items.length > 0 && h("pre", { className: "mt-3 max-h-64 overflow-auto rounded bg-bg-input p-2 text-xs" }, JSON.stringify(details.items, null, 2)),
            Array.isArray(details.trace_preview) && details.trace_preview.length > 0 && h("div", { className: "mt-3" }, h("div", { className: "mb-1 text-xs font-medium" }, "Step timeline"), h("ol", { className: "grid gap-1" }, details.trace_preview.map((event, index) => h("li", { key: `${event.at || index}-${index}`, className: "flex flex-wrap gap-x-2 rounded bg-bg-input px-2 py-1 text-xs" }, h("span", { className: "font-medium" }, event.action), h("span", { className: event.status === "failed" ? "text-red" : "text-text-muted" }, event.status), event.duration_ms != null && h("span", { className: "text-text-dim" }, duration(event.duration_ms)), event.message && h("span", { className: "w-full break-words text-text-muted" }, event.message))))),
            h("div", { className: "mt-3 flex flex-wrap gap-2" }, [["Download JSONL", dataset], ["Download CSV", csv], ["Open trace", trace]].filter(([, href]) => href).map(([label, href]) => h("a", { key: label, href, target: "_blank", rel: "noopener noreferrer", className: "rounded border border-border px-2 py-1 text-xs text-accent" }, label)), (run.status === "queued" || run.status === "running") && h("button", { disabled: busy === run.id, onClick: () => action(run.id, "cancel"), className: "rounded border border-red px-2 py-1 text-xs text-red" }, "Cancel"), ["completed", "failed", "cancelled"].includes(run.status) && run.extractor_id && h("button", { disabled: busy === run.id, onClick: () => action(run.id, "retry"), className: "rounded border border-border px-2 py-1 text-xs" }, "Retry"))
          )
        );
      })),
    runs.length < total && h("div", { className: "mt-4 text-center" }, h("button", { onClick: () => load(true), className: "rounded border border-border px-4 py-2 text-sm" }, "Load more"))
  );
}

function SchedulesView({ projectId, extractors }) {
  const [jobs, setJobs] = useState([]); const [error, setError] = useState(""); const [message, setMessage] = useState(""); const [busy, setBusy] = useState(false);
  const [history, setHistory] = useState({});
  const [extractorId, setExtractorId] = useState(""); const [name, setName] = useState(""); const [schedule, setSchedule] = useState(JSON.stringify({ kind: "every", every_seconds: 8640 }, null, 2)); const [timezone, setTimezone] = useState("UTC"); const [input, setInput] = useState("{}");
  const load = useCallback(async () => { setError(""); try { const body = await request("/schedules", projectId); setJobs(body.jobs || []); } catch (err) { setError(String(err.message || err)); } }, [projectId]);
  useEffect(() => { load(); }, [load]);
  async function create(event) { event.preventDefault(); setBusy(true); setError(""); setMessage(""); try { const body = { extractor_id: Number(extractorId), name: name || undefined, schedule: parseJSON(schedule, "Schedule"), timezone, input: parseJSON(input, "Input") }; const out = await request("/schedules", projectId, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); setMessage(out.job?.id ? `Created schedule #${out.job.id}.` : "Created schedule."); await load(); } catch (err) { setError(String(err.message || err)); } finally { setBusy(false); } }
  async function scheduleAction(id, action) { setBusy(true); setError(""); try { await request(`/schedules/${id}/${action}`, projectId, { method: "POST" }); setMessage(action === "run" ? "Immediate delivery queued." : "Schedule cancelled."); await load(); } catch (err) { setError(String(err.message || err)); } finally { setBusy(false); } }
  async function loadHistory(id) { setError(""); try { const body = await request("/schedules", projectId, {}, { job_id: id }); setHistory((current) => ({ ...current, [id]: body.runs || [] })); } catch (err) { setError(String(err.message || err)); } }
  return h("div", { className: "grid gap-4 xl:grid-cols-[minmax(0,1fr)_380px]" },
    h("section", null, h("h2", { className: "mb-3 font-semibold" }, "Schedules"), jobs.length === 0 ? h(Empty, { title: "No extractor schedules", detail: "Jobs owns schedule timing, delivery retries, and trigger history." }) : h("div", { className: "grid gap-2" }, jobs.map((job) => { const targetInput = job.target?.input || {}; const deliveryRuns = history[job.id]; const cadence = job.every_seconds ? `every ${job.every_seconds}s` : job.cron_expr ? `cron ${job.cron_expr}` : job.schedule_kind; return h("article", { key: job.id, className: "rounded border border-border bg-bg-card p-3" }, h("div", { className: "flex gap-2" }, h("div", { className: "min-w-0 flex-1" }, h("div", { className: "font-medium" }, job.name), h("div", { className: "mt-1 text-xs text-text-muted" }, `${cadence} • ${job.timezone || "UTC"}`)), h("span", { className: `h-fit rounded border px-2 py-0.5 text-xs ${statusClass(job.status)}` }, job.status)), h("div", { className: "mt-2 text-xs text-text-dim" }, `Extractor #${targetInput.extractor_id || "—"}${targetInput.preset ? ` • ${targetInput.preset}` : ""} • Next ${job.next_run_at ? relativeTime(job.next_run_at) : "—"} • Last ${job.last_status || "—"}`), job.last_error && h("div", { className: "mt-2 text-xs text-red" }, job.last_error), deliveryRuns && h("div", { className: "mt-2 rounded bg-bg-input p-2 text-xs" }, deliveryRuns.length ? deliveryRuns.slice(0, 5).map((run) => h("div", { key: run.id, className: "flex justify-between gap-2 py-0.5" }, h("span", null, relativeTime(run.started_at)), h("span", null, run.status))) : "No deliveries yet."), h("div", { className: "mt-3 flex flex-wrap gap-2" }, h("button", { onClick: () => loadHistory(job.id), className: "rounded border border-border px-2 py-1 text-xs" }, "Delivery history"), h("button", { disabled: busy || job.status === "cancelled", onClick: () => scheduleAction(job.id, "run"), className: "rounded border border-accent px-2 py-1 text-xs text-accent disabled:opacity-50" }, "Run now"), h("button", { disabled: busy || job.status === "cancelled", onClick: () => scheduleAction(job.id, "cancel"), className: "rounded border border-red px-2 py-1 text-xs text-red disabled:opacity-50" }, "Pause / cancel"))); }))),
    h("section", { className: "rounded border border-border bg-bg-card p-4" }, h("h2", { className: "font-semibold" }, "New schedule"), h("p", { className: "mb-3 text-xs text-text-muted" }, "Jobs delivers triggers; Web queues the actual browser run."), h(Notice, { error, message }), h("form", { onSubmit: create, className: "grid gap-3" }, h("select", { required: true, value: extractorId, onChange: (e) => setExtractorId(e.target.value), className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }, h("option", { value: "" }, "Choose extractor"), extractors.map((record) => h("option", { key: record.id, value: record.id }, record.name))), h("input", { value: name, onChange: (e) => setName(e.target.value), placeholder: "Schedule name", className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }), h("label", { className: "grid gap-1 text-xs" }, "Schedule JSON", h("textarea", { value: schedule, onChange: (e) => setSchedule(e.target.value), rows: 5, spellCheck: false, className: "rounded border border-border bg-bg-input p-2 font-mono text-xs" })), h("input", { value: timezone, onChange: (e) => setTimezone(e.target.value), placeholder: "Timezone", className: "rounded border border-border bg-bg-input px-2 py-1.5 text-sm" }), h("label", { className: "grid gap-1 text-xs" }, "Input overrides", h("textarea", { value: input, onChange: (e) => setInput(e.target.value), rows: 4, spellCheck: false, className: "rounded border border-border bg-bg-input p-2 font-mono text-xs" })), h("button", { disabled: busy, className: "rounded bg-accent px-3 py-2 text-sm text-white disabled:opacity-50" }, busy ? "Creating…" : "Create schedule")))
  );
}

function WebPanel({ projectId } = {}) {
  const [tab, setTab] = useState("extractors"); const [refreshRuns, setRefreshRuns] = useState(0); const [extractors, setExtractors] = useState([]);
  useEffect(() => { request("/extractors", projectId).then((body) => setExtractors(body.extractors || [])).catch(() => {}); }, [projectId, tab]);
  return h("div", { className: "h-full min-w-0 overflow-auto bg-bg text-text" },
    h("header", { className: "sticky top-0 z-10 border-b border-border bg-bg px-4 pt-3" }, h("div", null, h("h1", { className: "text-lg font-semibold" }, "Web"), h("p", { className: "text-xs text-text-muted" }, "Browser intelligence and reusable extractors")), h("nav", { className: "mt-3 flex gap-1", "aria-label": "Web sections" }, [["extractors", "Extractors"], ["runs", "Runs"], ["schedules", "Schedules"]].map(([id, label]) => h("button", { key: id, type: "button", onClick: () => setTab(id), className: `border-b-2 px-3 py-2 text-sm ${tab === id ? "border-accent text-text" : "border-transparent text-text-muted hover:text-text"}` }, label)))),
    h("main", { className: "mx-auto w-full max-w-6xl p-4" }, tab === "extractors" ? h(ExtractorsView, { projectId, refreshRuns: () => { setRefreshRuns((v) => v + 1); setTab("runs"); } }) : tab === "runs" ? h(RunsView, { projectId, refreshToken: refreshRuns }) : h(SchedulesView, { projectId, extractors }))
  );
}

export default WebPanel;

if (typeof document !== "undefined") {
  const element = document.getElementById("root");
  if (element) createRoot(element).render(h(WebPanel));
}
