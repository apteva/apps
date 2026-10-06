import { useEffect, useMemo, useState } from "react";
import {
  audioWidgetPreferences, defaultAudioFilters, audioIssues, audioStages, audioStateLabel, metricLabel,
  type AudioDashboardHost, type AudioFilters,
} from "./audio-dashboard";
import { audioWidgetMeasurements, audioWidgetProblems } from "./audio-widget";
import { useAudioDashboard } from "./AudioHealthView";

const control = "w-full min-w-0 rounded border border-border bg-bg p-2 text-xs text-text";
export default function AudioHealthWidget(props: AudioDashboardHost) {
  const preferences = useMemo(() => audioWidgetPreferences(props.widgetSettings), [props.widgetSettings]);
  const [filters, setFilters] = useState<AudioFilters>(() => ({ ...defaultAudioFilters, range: preferences.range, provider: preferences.provider, state: "issues" }));
  const [search, setSearch] = useState(""), [cursor, setCursor] = useState(""), [previous, setPrevious] = useState<string[]>([]);
  const [expanded, setExpanded] = useState<string | null>(null);
  const change = (key: keyof AudioFilters, value: string) => {
    setFilters((current) => ({ ...current, [key]: value })); setCursor(""); setPrevious([]); setExpanded(null);
  };
  useEffect(() => {
    setFilters((current) => current.range === preferences.range && current.provider === preferences.provider ? current : ({ ...current, range: preferences.range, provider: preferences.provider }));
    setCursor(""); setPrevious([]);
  }, [preferences.range, preferences.provider]);
  useEffect(() => {
    if (filters.search === search) return;
    const timer = setTimeout(() => {
      setFilters((current) => current.search === search ? current : ({ ...current, search }));
      setCursor(""); setPrevious([]);
    }, 300);
    return () => clearTimeout(timer);
  }, [search, filters.search]);
  const { data, error, loading, updatesMode, reload } = useAudioDashboard(props, filters, cursor, preferences.maxCalls, 30000);
  if (!props.projectId) return <section className="rounded border border-border bg-bg-card p-4"><h2 className="text-sm font-semibold">Telephony audio health</h2><p className="text-xs text-text-muted">Select a project to see audio issues.</p></section>;
  const facets = data?.facets || {};
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <header className="flex flex-wrap items-center justify-between gap-2 border-b border-border p-3">
        <div><h2 className="text-sm font-semibold">Telephony audio health</h2><p className="mt-1 text-xs text-text-muted">Audio loss, delivery gaps and browser interruptions</p></div>
        <button type="button" aria-label="Refresh audio health" onClick={() => void reload()} disabled={loading} className="rounded border border-border px-3 py-1.5 text-xs text-text-muted disabled:opacity-50">{loading ? "Refreshing…" : "Refresh"}</button>
      </header>
      <div className="border-b border-border p-3">
        <div className="grid grid-cols-2 gap-3">
          <div><p className="text-xs text-text-muted">Calls with issues</p><strong className={`text-xl tabular-nums ${data && data.totals.affected > 0 ? "text-error" : "text-text"}`}>{data?.totals.affected ?? "—"}</strong></div>
          <div><p className="text-xs text-text-muted">Degraded live calls</p><strong className={`text-xl tabular-nums ${data && data.totals.degraded > 0 ? "text-error" : "text-text"}`}>{data?.totals.degraded ?? "—"}</strong></div>
        </div>
        <p className="mt-1 text-xs text-text-muted">Issue history includes ended calls.</p>
      </div>
      <div className="space-y-2 border-b border-border p-3">
        <div className="grid grid-cols-2 gap-2 @xl:grid-cols-3">
          <label className="text-xs text-text-muted">Time range<select aria-label="Audio time range" className={control} value={filters.range} onChange={(e) => change("range", e.target.value)}><option value="1h">Last hour</option><option value="24h">Last 24 hours</option><option value="7d">Last 7 days</option></select></label>
          <label className="text-xs text-text-muted">Problem<select aria-label="Audio problem" className={control} value={filters.issue} onChange={(e) => change("issue", e.target.value)}><option value="">All issues</option>{Object.entries(audioIssues).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        </div>
        <details><summary className="cursor-pointer text-xs text-text-muted">More filters</summary><div className="mt-2 grid grid-cols-2 gap-2">          <label className="text-xs text-text-muted">Provider<select aria-label="Audio provider" className={control} value={filters.provider} onChange={(e) => change("provider", e.target.value)}><option value="">All providers</option>{[...new Set([filters.provider, ...(facets.provider || []).map((item) => item.value)])].filter(Boolean).map((value) => <option key={value} value={value}>{value}</option>)}</select></label>
          <label className="text-xs text-text-muted">Search<input aria-label="Search audio issues" className={control} placeholder="Call, number or team" value={search} onChange={(e) => setSearch(e.target.value)} /></label>

          <label className="text-xs text-text-muted">Direction<select aria-label="Audio direction" className={control} value={filters.stage} onChange={(e) => change("stage", e.target.value)}><option value="">All directions</option>{Object.entries(audioStages).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label className="text-xs text-text-muted">Adviser<select aria-label="Audio adviser" className={control} value={filters.adviser} onChange={(e) => change("adviser", e.target.value)}><option value="">All advisers</option>{(facets.adviser || []).map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select></label>
          <label className="text-xs text-text-muted">Call state<select aria-label="Audio call state" className={control} value={filters.state} onChange={(e) => change("state", e.target.value)}><option value="issues">Calls with issues</option><option value="audio_degraded">Degraded now</option><option value="active">Active calls</option><option value="">All tracked calls</option></select></label>
        </div></details>
      </div>
      {error && <div role="alert" className="border-b border-error/30 bg-error/10 p-3 text-xs text-error">{error}<button onClick={() => void reload()} className="ml-2 underline">Retry</button>{data && <p>Last successful update: {new Date(data.generated_at).toLocaleTimeString()}</p>}</div>}
      {data && data.pending_reports > 0 && <p className="px-3 pt-2 text-xs text-warn">{data.pending_reports} reports awaiting indexing.</p>}
      <div className="min-h-0 flex-1 space-y-3 overflow-auto p-3" aria-label="Audio issue reports">
        {data?.calls.map((report) => {
          const problems = audioWidgetProblems(report), measurements = audioWidgetMeasurements(report);
          const tone = problems.some((item) => item.tone === "error") ? "error" : "warning";
          const open = expanded === report.call_id;
          return <article key={report.call_id} className={`rounded border border-l-4 p-3 ${problems.length ? tone === "error" ? "border-border border-l-error bg-error/5" : "border-border border-l-warn bg-warn/5" : "border-border"}`}>
            <div className="flex flex-wrap items-start justify-between gap-2">
              <div className="flex min-w-0 flex-wrap gap-1.5">{problems.length ? problems.map((item) => <span key={item.code} className={`rounded border px-2 py-0.5 text-xs font-semibold ${item.tone === "error" ? "border-error/30 bg-error/10 text-error" : "border-warn/30 bg-warn/10 text-warn"}`}>{item.label}</span>) : <span className="text-xs text-text-muted">No issues recorded</span>}</div>
              <span className="text-xs text-text-muted">{audioStateLabel(report.state)}</span>
            </div>
            <p className="mt-2 break-words text-xs text-text-muted">{report.adviser_label} · {report.provider}</p>
            {measurements.length > 0 && <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2">{measurements.map((item) => <div key={item.key}><dt className="text-xs text-text-muted">{item.label}</dt><dd className="text-sm font-semibold tabular-nums text-text">{item.value}</dd></div>)}</dl>}
            <div className="mt-3 flex justify-between gap-2 text-xs text-text-muted"><time dateTime={report.observed_at}>{new Date(report.observed_at).toLocaleTimeString()}</time><button aria-expanded={open} onClick={() => setExpanded(open ? null : report.call_id)} className="underline">{open ? "Hide details" : "Show details"}</button></div>
            {open && <div className="mt-3 space-y-2 border-t border-border pt-3 text-xs text-text-muted"><p className="break-all">Call {report.call_id} · {report.status}</p><p>{report.from} → {report.to}</p><p>Observed {new Date(report.observed_at).toLocaleString()}</p>{report.stages.map((stage) => <p key={stage}>{audioStages[stage as keyof typeof audioStages] || stage}</p>)}<dl className="grid grid-cols-2 gap-2">{Object.entries(report.metrics).filter(([, value]) => value > 0).map(([key, value]) => <div key={key}><dt>{(metricLabel[key] || key.replace(/_/g, " "))}</dt><dd className="text-text">{Math.round(value)}{key.endsWith("_ms") ? " ms" : ""}</dd></div>)}</dl></div>}
          </article>;
        })}
        {!data && !error && <p className="p-3 text-xs text-text-muted">Loading audio health…</p>}
        {data?.calls.length === 0 && <p className="p-3 text-xs text-text-muted">No matching issues. Calls without telemetry cannot be assessed.</p>}
      </div>
      <footer className="flex flex-wrap items-center justify-between gap-2 border-t border-border px-3 py-2 text-xs text-text-muted">
        <div><span>{updatesMode === "sse" ? "SSE updates" : updatesMode === "fallback" ? "Live updates unavailable · recovery polling" : "Connecting live updates…"}</span>{data && <p>Updated {new Date(data.generated_at).toLocaleTimeString()} · {data.calls.length} shown / {data.totals.calls} matching</p>}</div>
        {(previous.length > 0 || data?.next_cursor) && <div className="flex gap-3"><button disabled={!previous.length || loading} onClick={() => { setCursor(previous.at(-1) || ""); setPrevious(previous.slice(0, -1)); setExpanded(null); }} className="disabled:opacity-40">Previous</button><button disabled={!data?.next_cursor || loading} onClick={() => { setPrevious([...previous, cursor]); setCursor(data?.next_cursor || ""); setExpanded(null); }} className="disabled:opacity-40">Next</button></div>}
      </footer>
    </section>
  );
}
