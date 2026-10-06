import { useCallback, useEffect, useMemo, useState } from "react";
import { activityPreferences as settings, activityPageLink, activityURL, activityStatusLabel as label, activityStatusTone as tone, activityTime as relativeTime, useInvocationFeed, type ActivityHostProps as HostProps, type ActivityAPI, type ActivityFilter } from "./activity";

export default function FunctionActivityWidget(props: HostProps) {
  const preferences = useMemo(() => settings(props.widgetSettings), [props.widgetSettings]);
  const limit = props.widgetSize === "full" ? Math.min(20, preferences.maxItems * 2) : preferences.maxItems;
  const [filter, setFilter] = useState<ActivityFilter>(preferences.status);
  useEffect(() => setFilter(preferences.status), [preferences.status, props.projectId, props.installId]);
  const api: ActivityAPI = useCallback(async <T,>(_method: string, path: string, _body?: unknown, extra: Record<string, string> = {}, signal?: AbortSignal) => {
    const response = await fetch(activityURL(props, path, extra), { credentials: "same-origin", signal });
    if (!response.ok) throw new Error((await response.text().catch(() => "")) || `Request failed (${response.status})`);
    return await response.json() as T;
  }, [props.appName, props.projectId, props.installId]);
  const { page: result, error, loading, refresh: load } = useInvocationFeed(api, filter, "", limit, true, props.eventRevision || 0, !!props.projectId);
  const items = result?.invocations || null;
  const page = activityPageLink(props, filter);
  if (!props.projectId) {
    return <section className="flex h-full min-h-0 items-center justify-center rounded-lg border border-border bg-bg-card p-5 text-center"><div><p className="text-sm font-semibold text-text">Function activity</p><p className="mt-1 text-xs text-text-dim">Select a project to see function logs.</p></div></section>;
  }
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <header className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0"><div className="flex items-center gap-2"><h2 className="text-sm font-bold text-text">Function activity</h2><span className={`rounded px-1.5 py-0.5 text-[9px] font-bold ${error ? "bg-border text-text-muted" : "bg-green/15 text-green"}`}>{error ? "RECONNECTING" : "LIVE"}</span></div><p className="mt-0.5 text-[11px] text-text-dim">{filter === "errors" ? "Errors and timeouts" : filter === "running" ? "Running invocations" : "Recent invocations"}</p></div>
        <a href={page} className="shrink-0 pt-0.5 text-[11px] text-text-muted hover:text-text">View logs →</a>
      </header>
      <nav className="flex shrink-0 gap-1 border-b border-border px-4 py-2" aria-label="Filter function activity">
        {([ ["all", "All"], ["errors", "Errors"], ["running", "Running"] ] as const).map(([value, title]) => <button key={value} type="button" aria-pressed={filter === value} onClick={() => setFilter(value)} className={`rounded border px-2 py-1 text-[10px] ${filter === value ? "border-accent/40 bg-accent/10 text-accent" : "border-border text-text-muted hover:bg-bg-input"}`}>{title}</button>)}
      </nav>
      {error ? <div className="flex min-h-28 flex-1 flex-col items-start justify-center gap-2 p-4" role="alert"><p className="text-xs text-red">{error}</p><button type="button" onClick={() => void load()} className="rounded border border-border px-2 py-1 text-[10px] text-text-muted hover:bg-bg-hover">Retry</button></div> : items === null ? <p className="flex min-h-28 flex-1 items-center p-4 text-xs text-text-dim">Loading function activity…</p> : items.length === 0 ? <div className="flex min-h-28 flex-1 flex-col justify-center p-4"><p className="text-xs font-semibold text-text">No matching invocations</p><p className="mt-1 text-[11px] text-text-dim">New activity will appear here automatically.</p></div> : <div className="min-h-0 flex-1 divide-y divide-border overflow-y-auto" aria-live="polite">{items.map((item) => <a key={item.id} href={activityPageLink(props, filter, item.id)} className="block px-4 py-2.5 hover:bg-bg-hover/60"><div className="flex min-w-0 items-center gap-2"><span className="truncate text-xs font-semibold text-text">{item.function_name || `Function #${item.function_id}`}</span><span className={`shrink-0 rounded px-1.5 py-0.5 text-[8px] font-bold uppercase ${tone(item.status)}`}>{label(item.status)}</span><span className="ml-auto shrink-0 text-[10px] tabular-nums text-text-dim">{relativeTime(item.started_at)}</span></div><div className="mt-1 flex min-w-0 items-center gap-2 text-[10px] text-text-muted"><span>{item.trigger_kind}</span>{item.status !== "running" && <span>{item.duration_ms} ms</span>}{item.error && <span className="truncate text-red" title={item.error}>{item.error}</span>}</div></a>)}</div>}
    </section>
  );
}
