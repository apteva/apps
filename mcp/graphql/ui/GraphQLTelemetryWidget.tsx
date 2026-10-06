import { useEffect, useState } from "react";
import { hasLogErrors, formatBytes, telemetryQuery } from "./telemetry";

type HostProps = { projectId?: string; installId?: number; appName?: string; widgetSettings?: Record<string, any> };
type Filters = Record<string, string>;
type Log = Record<string, any>;
const input = "w-full rounded border border-border bg-surface-2 px-2 py-1.5 text-xs text-text focus:border-accent outline-none";
const button = "rounded border border-border px-2.5 py-1.5 text-xs hover:bg-surface-2 disabled:opacity-50";
const defaults: Filters = { range: "60", sort_by: "created_at", sort_order: "desc", limit: "100" };
const windows = [["15", "Last 15 minutes"], ["60", "Last hour"], ["1440", "Last 24 hours"], ["10080", "Last 7 days"], ["all", "All time"], ["custom", "Custom dates"]];
async function telemetryFetch(props: HostProps, path: string, query: URLSearchParams, signal?: AbortSignal) {
  if (props.projectId) query.set("project_id", props.projectId);
  if (props.installId) query.set("install_id", String(props.installId));
  const response = await fetch(`/api/apps/${encodeURIComponent(props.appName || "graphql")}/admin/${path}?${query}`, { credentials: "same-origin", signal });
  const body = await response.json();
  if (!response.ok) throw new Error(body.error || body.message || `Request failed (${response.status})`);
  return body;
}
function Field({ label, children }: { label: string; children: any }) {
  return <label className="block min-w-0 space-y-1 text-xs"><span className="text-text-dim">{label}</span>{children}</label>;
}
function NumberFilter({ name, label, draft, setField }: any) {
  return <Field label={label}><input className={input} type="number" min="0" step="1" value={draft[name] || ""} onChange={e => setField(name, e.target.value)} /></Field>;
}
function FilterButton({ active, children, onClick }: any) {
  return <button type="button" aria-pressed={active} className={`${button} ${active ? "border-accent bg-accent/10 text-accent" : "text-text-dim"}`} onClick={onClick}>{children}</button>;
}
function RuntimeDetails({ metrics }: { metrics: Log }) {
 const resolvers = Object.entries(metrics.resolver_timings || {}).sort((a: any, b: any) => b[1].total_ms - a[1].total_ms);
 return <div className="space-y-2">
  <h3 className="font-semibold">Runtime diagnostics</h3>
  <div className="flex flex-wrap gap-2">{[["Queue", `${Number(metrics.queue_ms || 0).toFixed(1)} ms`], ["Execution", metrics.coalesced ? "Joined shared execution" : "Own execution"], ["Waiters", metrics.waiters || 1], ["Loader hits", metrics.loader_hits || 0], ["Backend calls", metrics.backend_calls || 0], ["Backend operations", metrics.backend_reads || 0], ["Snapshot acquisition", `${Number(metrics.snapshot_ms || 0).toFixed(1)} ms`], ["Consistency", metrics.consistency || "none"]].map(([name, value]) => <span key={String(name)} className="rounded border border-border px-2 py-1">{name}: {value}</span>)}</div>
  {metrics.execution_id && <p className="break-all text-text-dim">Shared execution ID: {metrics.execution_id}</p>}
  {metrics.batch_sizes?.length > 0 && <p className="text-text-dim">Batch sizes: {metrics.batch_sizes.join(", ")}</p>}
  {resolvers.length > 0 && <div className="overflow-x-auto"><table className="w-full text-left"><caption className="mb-1 text-left text-text-dim">Resolver completion time, including waits for source reads</caption><thead><tr>{["Field", "Calls", "Total ms", "Max ms", "Errors"].map(label => <th key={label} className="px-2 py-1 font-normal text-text-dim">{label}</th>)}</tr></thead><tbody>{resolvers.map(([field, raw]: any) => <tr key={field} className="border-t border-border"><td className="px-2 py-1 font-mono">{field}</td><td className="px-2">{raw.calls}</td><td className="px-2">{Number(raw.total_ms).toFixed(1)}</td><td className="px-2">{Number(raw.max_ms).toFixed(1)}</td><td className={`px-2 ${raw.errors ? "text-red-300" : ""}`}>{raw.errors}</td></tr>)}</tbody></table></div>}
  {metrics.sources?.length > 0 && <details><summary className="cursor-pointer text-text-dim">Source freshness and coverage</summary><pre className="mt-2 overflow-auto whitespace-pre-wrap break-words">{JSON.stringify(metrics.sources, null, 2)}</pre></details>}
 </div>;
}
function Details({ log }: { log: Log }) {
  const errors = log.errors?.length ? log.errors : log.error ? [{ message: log.error }] : [];
  const [copied, setCopied] = useState(false);
  return <div className="space-y-3 rounded border border-border bg-surface-2/40 p-3 text-xs">
    {errors.length > 0 && <div className="space-y-2">{errors.map((error: any, index: number) => <div key={index} className="rounded border border-red-400/30 bg-red-400/5 p-2">
      <p className="whitespace-pre-wrap break-words text-red-300">{error.message}</p>
      <p className="mt-1 break-words text-text-dim">{error.extensions?.code || "GraphQL error"}{error.path?.length ? ` · Field: ${error.path.join(".")}` : ""}{error.locations?.length ? ` · ${error.locations.map((l: any) => `line ${l.line}:${l.column}`).join(", ")}` : ""}</p>
    </div>)}</div>}
    <dl className="grid grid-cols-1 gap-2 sm:grid-cols-2">
      {[["Request ID", log.request_id || "Unavailable (older request)"], ["Operation hash", log.operation_hash || "Unavailable"], ["Environment", log.environment || "Not recorded (older request)"], ["Authorization scope", log.authorization_scope || "Not recorded"], ["Release", log.api_release ? `v${log.api_release}` : "Legacy"], ["Response", `${formatBytes(log.response_bytes)} · ${log.row_count} rows · ${log.resolver_count} resolvers`]].map(([label, value]) => <div key={label}><dt className="text-text-dim">{label}</dt><dd className="break-all text-text">{value}</dd></div>)}
    </dl>
    {log.error_codes?.length > 0 && <p className="break-words text-red-300">Error codes: {log.error_codes.join(", ")}</p>}
    <div className="flex flex-wrap gap-2">{Object.entries(log.timings || {}).map(([name, ms]) => <span key={name} className="rounded border border-border px-2 py-1">{name}: {Number(ms).toFixed(1)} ms</span>)}</div>
    <p className="text-text-dim">Sources: {Object.entries(log.source_timings || {}).map(([name, ms]) => `${name} ${Number(ms).toFixed(1)} ms`).join(" · ") || "None recorded"}</p>
    {log.runtime && Object.keys(log.runtime).length > 0 && <RuntimeDetails metrics={log.runtime} />}
    <button type="button" className={button} onClick={async () => { try { await navigator.clipboard.writeText(JSON.stringify(log, null, 2)); setCopied(true); } catch { setCopied(false); } }}>{copied ? "Copied request details" : "Copy request details"}</button>
  </div>;
}

export function LogsPanel(props: HostProps & { apiSlug: string; compact?: boolean; slowMS?: number; initialRange?: string; initialView?: string; initialLimit?: number }) {
  const slowMS = props.slowMS || 1000;
  const initial = { ...defaults, range: props.initialRange || "60", limit: String(props.initialLimit || (props.compact ? 10 : 100)), ...(props.initialView === "errors" ? { has_errors: "true" } : props.initialView === "slow" ? { min_duration_ms: String(slowMS), sort_by: "duration_ms" } : {}) };
  const [active, setActive] = useState<Filters>(initial);
  const [draft, setDraft] = useState<Filters>(initial);
  const [logs, setLogs] = useState<Log[]>([]);
  const [summary, setSummary] = useState<Log | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [updated, setUpdated] = useState<Date | null>(null);
  const [expanded, setExpanded] = useState<number | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [live, setLive] = useState(!!props.compact);
  // Each effect owns a cancellation signal: an older response cannot replace a newer API/filter selection.
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError("");
    let query: URLSearchParams;
    try { query = telemetryQuery(active, slowMS); query.set("api_slug", props.apiSlug); }
    catch { setError("Enter valid dates for the custom time range."); setLoading(false); return; }
    telemetryFetch(props, "logs", query, controller.signal).then(body => {
      if (controller.signal.aborted) return;
      setLogs(body.logs || []); setSummary(body.summary || null); setUpdated(new Date());
    }).catch(err => { if (!controller.signal.aborted) setError(err.message); }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [props.projectId, props.installId, props.appName, props.apiSlug, active, slowMS, refresh]);
  useEffect(() => { setLogs([]); setSummary(null); setUpdated(null); setExpanded(null); }, [props.projectId, props.installId, props.apiSlug]);
  useEffect(() => {
    if (!live) return;
    const timer = setInterval(() => { if (!document.hidden) setRefresh(value => value + 1); }, 30_000);
    return () => clearInterval(timer);
  }, [live]);
  function apply(next: Filters) { setDraft(next); setActive(next); setExpanded(null); }
  function quick(view: string) {
    const next = { ...active };
    for (const key of ["has_errors", "min_duration_ms", "min_response_bytes", "error_code", "status_code", "min_queue_ms", "coalesced"]) delete next[key];
    next.sort_order = "desc"; next.sort_by = "created_at";
    if (view === "errors") next.has_errors = "true";
    if (view === "slow") { next.min_duration_ms = String(slowMS); next.sort_by = "duration_ms"; }
    if (view === "queued") { next.min_queue_ms = "1"; next.sort_by = "queue_ms"; }
    if (view === "shared") next.coalesced = "true";
    if (view === "large") { next.min_response_bytes = "1048576"; next.sort_by = "response_bytes"; }
    apply(next);
  }
  const setField = (key: string, value: string) => setDraft(current => ({ ...current, [key]: value }));
  const count = Number(summary?.requests || 0);
  return <section className={`${props.compact ? "" : "p-5"} text-text`}>
    <header className="mb-3 flex flex-wrap items-center justify-between gap-2">
      <div><h2 className="text-sm font-semibold">{props.compact ? "Request health" : "Requests and errors"}</h2><p className="mt-1 text-xs text-text-dim">HTTP failures and GraphQL errors are counted, including partial errors with status 200.</p></div>
      <div className="flex items-center gap-2"><label className="flex items-center gap-1 text-xs text-text-dim"><input type="checkbox" checked={live} onChange={e => setLive(e.target.checked)} />Live · 30s</label><button className={button} disabled={loading} onClick={() => setRefresh(value => value + 1)}>{loading ? "Refreshing…" : "Refresh"}</button></div>
    </header>
    <nav aria-label="Filter GraphQL requests" className="mb-3 flex flex-wrap gap-2">
      <FilterButton active={!active.has_errors && !active.min_duration_ms && !active.min_response_bytes && !active.min_queue_ms && !active.coalesced} onClick={() => quick("all")}>All requests</FilterButton>
      <FilterButton active={active.has_errors === "true"} onClick={() => quick("errors")}>Errors</FilterButton>
      <FilterButton active={!!active.min_duration_ms} onClick={() => quick("slow")}>Slow ≥ {slowMS} ms</FilterButton>
      <FilterButton active={!!active.min_response_bytes} onClick={() => quick("large")}>Large ≥ 1 MB</FilterButton>
    <FilterButton active={!!active.min_queue_ms} onClick={() => quick("queued")}>Queued</FilterButton>
      <FilterButton active={active.coalesced === "true"} onClick={() => quick("shared")}>Shared executions</FilterButton>
    </nav>
    <form className="mb-3 space-y-3 rounded border border-border p-3" onSubmit={e => { e.preventDefault(); apply(draft); }}>
      <div className={`grid gap-2 ${props.compact ? "grid-cols-2" : "grid-cols-1 sm:grid-cols-2 xl:grid-cols-4"}`}>
        <Field label="Time range"><select className={input} value={draft.range} onChange={e => setField("range", e.target.value)}>{!windows.some(([value]) => value === draft.range) && <option value={draft.range}>Last {draft.range} minutes</option>}{windows.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field>
        <Field label="Search"><input className={input} placeholder="Error, operation, or request ID" value={draft.search || ""} onChange={e => setField("search", e.target.value)} /></Field>
        {!props.compact && <Field label="Environment"><select className={input} value={draft.environment || ""} onChange={e => setField("environment", e.target.value)}>{[["", "All environments"], ["development", "Development"], ["staging", "Staging"], ["production", "Production"]].map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field>}
        <Field label="Sort"><select className={input} value={draft.sort_by} onChange={e => setField("sort_by", e.target.value)}>{[["created_at", "Time"], ["duration_ms", "Duration"], ["response_bytes", "Response size"], ["row_count", "Rows"], ["resolver_count", "Resolvers"], ["status_code", "Status"], ["queue_ms", "Queue wait"], ["backend_reads", "Backend operations"], ["operation_name", "Operation"]].map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field>
      </div>
      {draft.range === "custom" && <div className="grid grid-cols-2 gap-2">{[["since", "From"], ["until", "Until"]].map(([key, label]) => <Field key={key} label={label}><input className={input} type="datetime-local" value={draft[key] || ""} onChange={e => setField(key, e.target.value)} /></Field>)}</div>}
      <details><summary className="cursor-pointer text-xs text-text-dim">Advanced filters</summary><div className="mt-2 grid grid-cols-2 gap-2 xl:grid-cols-4">
        <Field label="Result"><select className={input} value={draft.has_errors || ""} onChange={e => setField("has_errors", e.target.value)}><option value="">All results</option><option value="true">With errors</option><option value="false">Successful only</option></select></Field>
        {props.compact && <Field label="Environment"><select className={input} value={draft.environment || ""} onChange={e => setField("environment", e.target.value)}>{[["", "All environments"], ["development", "Development"], ["staging", "Staging"], ["production", "Production"]].map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field>}
        <Field label="Error code"><input className={input} placeholder="permission_denied" value={draft.error_code || ""} onChange={e => setField("error_code", e.target.value)} /></Field>
        <Field label="HTTP status"><input className={input} type="number" min="100" max="599" value={draft.status_code || ""} onChange={e => setField("status_code", e.target.value)} /></Field>
        <Field label="Exact operation name"><input className={input} value={draft.operation_name || ""} onChange={e => setField("operation_name", e.target.value)} /></Field>
        <Field label="Operation type"><select className={input} value={draft.operation_type || ""} onChange={e => setField("operation_type", e.target.value)}>{[["", "All types"], ["query", "Query"], ["mutation", "Mutation"], ["subscription", "Subscription"]].map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field>
        {[["min_duration_ms", "Min duration (ms)"], ["max_duration_ms", "Max duration (ms)"], ["min_response_bytes", "Min response (bytes)"], ["max_response_bytes", "Max response (bytes)"], ["min_rows", "Min rows"], ["max_rows", "Max rows"], ["min_resolvers", "Min resolvers"], ["max_resolvers", "Max resolvers"], ["min_queue_ms", "Min queue wait (ms)"], ["min_backend_reads", "Min backend operations"]].map(([name, label]) => <NumberFilter key={name} {...{ name, label, draft, setField }} />)}
        <Field label="Execution sharing"><select className={input} value={draft.coalesced || ""} onChange={e => setField("coalesced", e.target.value)}><option value="">All executions</option><option value="true">Joined shared execution</option><option value="false">Own execution</option></select></Field>
        <Field label="Order"><select className={input} value={draft.sort_order} onChange={e => setField("sort_order", e.target.value)}><option value="desc">Descending</option><option value="asc">Ascending</option></select></Field>
        <Field label="List limit"><input className={input} type="number" min="1" max="500" value={draft.limit} onChange={e => setField("limit", e.target.value)} /></Field>
      </div></details>
      <div className="flex flex-wrap gap-2"><button className={`${button} bg-accent text-white`} type="submit" disabled={loading}>Apply filters</button><button className={button} type="button" onClick={() => apply({ ...defaults, range: props.initialRange || "60", limit: String(props.initialLimit || (props.compact ? 10 : 100)) })}>Clear filters</button></div>
    </form>
    {error && <div role="alert" className="mb-3 rounded border border-red-400/30 bg-red-400/10 p-3 text-xs text-red-300">{error}{updated && " · Previous results are shown below."}</div>}
    {summary && <div className={`mb-3 grid gap-2 ${props.compact ? "grid-cols-3" : "grid-cols-2 md:grid-cols-4"}`}>
      {[["Matching requests", count.toLocaleString()], ["Errors", `${summary.errors}${count ? ` · ${(summary.errors * 100 / count).toFixed(1)}%` : ""}`], ["Slow requests", summary.slow], ["Average", `${Number(summary.avg_duration_ms).toFixed(1)} ms`], ["Slowest", `${summary.max_duration_ms} ms`], ["Response bytes", formatBytes(summary.response_bytes)], ["Avg queue wait", `${Number(summary.avg_queue_ms || 0).toFixed(1)} ms`], ["Shared callers", summary.coalesced || 0]].map(([label, value]) => <div key={label} className="rounded border border-border p-2"><p className="text-[10px] text-text-dim">{label}</p><p className={`mt-1 text-sm font-semibold ${label === "Errors" && summary.errors ? "text-red-300" : ""}`}>{value}</p></div>)}
    </div>}
    <p className="mb-2 text-[10px] text-text-dim">{updated ? `Updated ${updated.toLocaleTimeString()} · Showing ${logs.length} of ${count.toLocaleString()} matching requests. Counts include all matches, not just this list.` : "Loading requests…"}</p>
    {!loading && !error && logs.length === 0 && <p className="py-6 text-center text-xs text-text-dim">No requests match these filters.</p>}
    {props.compact ? <div className="space-y-2">{logs.map(log => <article key={log.id} className={`rounded border p-2 ${hasLogErrors(log) ? "border-red-400/30 bg-red-400/5" : "border-border"}`}>
      <button className="w-full text-left" aria-expanded={expanded === log.id} onClick={() => setExpanded(expanded === log.id ? null : log.id)}><div className="flex justify-between gap-2 text-xs"><span className="min-w-0 truncate font-semibold">{log.operation_name || "Unnamed operation"}</span><span className="shrink-0">{log.duration_ms} ms</span></div><p className={`mt-1 whitespace-pre-wrap break-words text-xs ${hasLogErrors(log) ? "text-red-300" : "text-text-dim"}`}>{log.error || (hasLogErrors(log) ? (log.error_codes || []).join(", ") || `HTTP ${log.status_code}` : `HTTP ${log.status_code} · ${formatBytes(log.response_bytes)} · ${log.row_count} rows`)}</p><p className="mt-1 text-[10px] text-text-dim">{new Date(log.created_at).toLocaleString()} · {expanded === log.id ? "Hide" : "View"} details</p></button>
      {expanded === log.id && <div className="mt-2"><Details log={log} /></div>}
    </article>)}</div> : <div className="overflow-x-auto"><table className="w-full text-left text-xs"><thead className="text-text-dim"><tr>{["Time", "Operation", "Result / errors", "Duration", "Rows", "Resolvers", "Response", "Sources", "Details"].map(label => <th key={label} className="whitespace-nowrap px-2 py-2 font-normal">{label}</th>)}</tr></thead><tbody>{logs.map(log => {
      const failed = hasLogErrors(log); const slow = log.duration_ms >= slowMS;
      return <LogRows key={log.id} log={log} failed={failed} slow={slow} expanded={expanded === log.id} toggle={() => setExpanded(expanded === log.id ? null : log.id)} />;
    })}</tbody></table></div>}
    <p className="mt-3 text-[10px] text-text-dim">HTTP requests only. Earlier records may lack environment or full error details.</p>
  </section>;
}
function LogRows({ log, failed, slow, expanded, toggle }: any) {
  return <>
    <tr className={`border-t border-border ${failed ? "bg-red-400/5" : slow ? "bg-yellow-400/5" : ""}`}>
      <td className="whitespace-nowrap px-2 py-3 align-top">{new Date(log.created_at).toLocaleString()}</td>
      <td className="px-2 py-3 align-top"><p className="font-semibold">{log.operation_name || "Unnamed operation"}</p><p className="mt-1 text-[10px] text-text-dim">{log.operation_type || "Rejected request"} · {log.environment || "Legacy environment"} · {log.api_release ? `v${log.api_release}` : "Legacy release"}</p></td>
      <td className="min-w-[220px] max-w-[360px] px-2 py-3 align-top"><span className={failed ? "font-semibold text-red-300" : "text-green-400"}>{failed ? "Error" : "Success"} · HTTP {log.status_code}</span>{log.error && <p className="mt-1 whitespace-pre-wrap break-words text-red-300">{log.error}</p>}{log.error_codes?.length > 0 && <p className="mt-1 break-words text-[10px] text-red-300">{log.error_codes.join(", ")}</p>}</td>
      <td className={`whitespace-nowrap px-2 py-3 align-top ${slow ? "font-semibold text-yellow-300" : ""}`}>{log.duration_ms} ms{slow && <p className="text-[10px]">Slow</p>}</td>
      <td className="px-2 py-3 align-top">{log.row_count}</td><td className="px-2 py-3 align-top">{log.resolver_count}</td><td className="whitespace-nowrap px-2 py-3 align-top">{formatBytes(log.response_bytes)}</td>
      <td className="px-2 py-3 align-top">{Object.entries(log.source_timings || {}).map(([name, ms]) => <p key={name} className="whitespace-nowrap">{name} {Number(ms).toFixed(1)} ms</p>)}</td>
      <td className="px-2 py-3 align-top"><button className={button} aria-expanded={expanded} onClick={toggle}>{expanded ? "Hide" : "Details"}</button></td>
    </tr>
    {expanded && <tr><td colSpan={9} className="px-2 pb-3"><Details log={log} /></td></tr>}
  </>;
}
function integerSetting(value: any, fallback: number, min: number, max: number) { const number = Number(value); return Number.isInteger(number) && number >= min && number <= max ? number : fallback; }
export default function GraphQLTelemetryWidget(props: HostProps) {
  const settings = props.widgetSettings || {};
  const preferredAPI = typeof settings.api_slug === "string" && settings.api_slug.trim() ? settings.api_slug.trim() : "default";
  const [apiSlug, setApiSlug] = useState(preferredAPI);
  const [apis, setAPIs] = useState<Log[]>([]);
  const [error, setError] = useState("");
  const slowMS = integerSetting(settings.slow_threshold_ms, 1000, 1, 3_600_000);
  const range = String(integerSetting(settings.window_minutes, 60, 1, 10080));
  const limit = integerSetting(settings.recent_limit, 10, 1, 50);
  const view = ["all", "errors", "slow"].includes(settings.default_view) ? settings.default_view : "all";
  useEffect(() => {
    const controller = new AbortController(); setError(""); setAPIs([]); setApiSlug(preferredAPI);
    telemetryFetch(props, "apis", new URLSearchParams(), controller.signal).then(body => {
      if (!controller.signal.aborted) setAPIs(body.apis || []);
    }).catch(err => { if (!controller.signal.aborted) setError(err.message); });
    return () => controller.abort();
  }, [props.projectId, props.installId, props.appName, preferredAPI]);
  return <section className="h-full overflow-auto rounded border border-border bg-bg-card p-4 text-text">
    <header className="mb-3 flex flex-wrap items-center justify-between gap-2"><h2 className="text-sm font-bold">GraphQL</h2><label className="flex items-center gap-2 text-xs text-text-dim">API<select className={`${input} w-auto`} value={apiSlug} onChange={e => setApiSlug(e.target.value)}>{!apis.some(api => api.slug === apiSlug) && <option value={apiSlug}>{apiSlug}</option>}{apis.map(api => <option key={api.slug} value={api.slug}>{api.name || api.slug}</option>)}</select></label></header>
    {error && <p role="alert" className="mb-2 text-xs text-red-300">{error}</p>}
    <LogsPanel key={`${props.projectId}:${props.installId}:${range}:${view}:${limit}:${slowMS}`} {...props} apiSlug={apiSlug} compact slowMS={slowMS} initialRange={range} initialView={view} initialLimit={limit} />
  </section>;
}
