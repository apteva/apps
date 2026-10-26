import { metricDateLabel, metricDateRanges } from "./metricsPresentation";
export interface AnalyticsFreshness {
  observed_at: string;
  processed_through?: string;
  pending_dates: string[];
  estimated_dates: string[];
  unavailable_dates: string[];
}
export interface AnalyticsReport {
  requested_metrics?: string[];
  freshness?: AnalyticsFreshness;
  scope: string;
  entity_id: string;
  identity: string;
  source: string;
  window: { kind: string; start_date?: string; end_date?: string; timezone?: string };
  metric_info: Record<string, { kind: string; additive?: boolean; alias_of?: string; freshness?: AnalyticsFreshness }>;
  coverage?: { inventory: string; discovered: number; included: number; failed: number; complete: boolean; truncated: boolean; metric_posts?: Record<string, number>; note?: string };
  warnings?: string[];
}
export interface AnalyticsPostRow {
  id: string; post_id?: number; title: string; url: string; published_at?: string;
  status: string; error?: string; metrics: Record<string, number>; report?: AnalyticsReport;
}
export interface PeriodComparison {
  status: string; reason?: string; window: { start_date?: string; end_date?: string };
  metrics?: Record<string, number>; change_percent?: Record<string, number | null>;
  coverage?: AnalyticsReport["coverage"];
}
export const metricLabel = (name: string) => name.replace(/_/g, " ");
export function visibleMetricNames(available: string[] | undefined, report?: AnalyticsReport): string[] {
  return (available || []).filter(name => !report?.metric_info?.[name]?.alias_of);
}
export function comparableIdentity(report: AnalyticsReport | undefined, fallback: string) {
  return report?.identity || fallback;
}
export function ReportNotice({ report, comparison }: { report?: AnalyticsReport; comparison?: PeriodComparison }) {
  if (!report) return null;
  const c = report.coverage;
  const f = report.freshness;
  return <div className="text-xs text-text-dim flex flex-col gap-1" role="status">
    <div>{report.scope === "provider_account" ? "Whole account" : report.scope === "post" ? "Individual post" : "Selected destination"}
      {report.window.kind === "range" ? ` · ${report.window.start_date} – ${report.window.end_date}` : ` · ${report.window.kind} totals`}
      {report.window.timezone && ` · ${report.window.timezone}`}
      {report.source === "post_aggregation" && " · Calculated from individual posts"}</div>
    {f && <>
      <div>Provider API report; not real-time. Fetched {f.observed_at}.</div>
      {f.processed_through && <div>Processed through {metricDateLabel(f.processed_through)}.</div>}
      {!!f.pending_dates.length && <div>Pending: {metricDateRanges(f.pending_dates)}.</div>}
      {!!f.estimated_dates.length && <div>Includes provider estimates: {metricDateRanges(f.estimated_dates)}.</div>}
      {!!f.unavailable_dates.length && <div>Data or processing status unavailable for {f.unavailable_dates.length} date{f.unavailable_dates.length === 1 ? "" : "s"}; gaps are not zero activity.</div>}
    </>}
    {c && <>
      <div>{c.complete ? "Complete inventory" : "Partial coverage"} · {c.included}/{c.discovered} posts included{c.failed ? ` · ${c.failed} failed` : ""}{c.truncated ? " · inventory limit reached" : ""}</div>
      {c.metric_posts && <div>{Object.entries(c.metric_posts).map(([name, count]) => `${metricLabel(name)}: ${count} posts`).join(" · ")}</div>}
      {c.note && <div>{c.note}</div>}
    </>}
    {report.warnings?.map((warning, index) => <div key={index}>{warning}</div>)}
    {comparison && <div>{comparison.status === "unsupported" ? comparison.reason : <>
      Compared with {comparison.window.start_date} – {comparison.window.end_date}
      {comparison.status === "partial" && " · partial comparison"}
      {comparison.reason && ` · ${comparison.reason}`}
    </>}</div>}
  </div>;
}
export function RankedPosts({ rows, metric, onMetric, metrics }: {
  rows: AnalyticsPostRow[]; metric: string; onMetric: (metric: string) => void; metrics: string[];
}) {
  const names = Array.from(new Set([...metrics, ...rows.flatMap(row => Object.keys(row.metrics || {}))]));
  const sorted = [...rows].sort((a, b) => {
    const av = a.metrics?.[metric], bv = b.metrics?.[metric];
    if (av == null) return bv == null ? 0 : 1;
    if (bv == null) return -1;
    return bv - av;
  });
  return <section className="flex flex-col gap-2">
    <div className="flex items-center gap-3"><h3 className="text-sm font-medium">Post ranking</h3>
      <label className="text-xs">Rank by <select aria-label="Rank posts by" value={metric} onChange={event => onMetric(event.target.value)} className="bg-bg-input border border-border rounded px-2 py-1">
        {Array.from(new Set([metric, ...names])).map(name => <option key={name} value={name}>{metricLabel(name)}</option>)}
      </select></label></div>
    {!rows.length ? <div className="text-xs text-text-dim">No posts returned for this report.</div> : <div className="overflow-x-auto"><table className="w-full min-w-[640px] text-xs text-left"><thead><tr>
      <th className="p-2">Post</th><th className="p-2">Reporting window</th>{names.map(name => <th className="p-2" key={name}>{metricLabel(name)}</th>)}
    </tr></thead><tbody>{sorted.map(row => <tr key={row.id || row.post_id} className="border-t border-border">
      <td className="p-2 max-w-[240px]"><a href={row.url} target="_blank" rel="noreferrer" className="text-accent">{row.title || row.id}</a>{row.status !== "ok" && <div>{row.error || row.status}</div>}</td>
      <td className="p-2 whitespace-nowrap">{row.report?.window.kind === "range" ? `${row.report.window.start_date} – ${row.report.window.end_date}` : row.report?.window.kind || "Unavailable"}</td>
      {names.map(name => <td className="p-2 tabular-nums" key={name}>{row.metrics?.[name] == null ? "Unavailable" : row.metrics[name].toLocaleString()}</td>)}
    </tr>)}</tbody></table></div>}
  </section>;
}
