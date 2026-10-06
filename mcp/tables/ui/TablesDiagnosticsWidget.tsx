import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/tables";

export interface TablesDiagnosticsWidgetProps {
  appName?: string;
  installId: number;
  projectId: string;
  instanceId?: number;
  compact?: boolean;
}

interface Diagnostic {
  id: number;
  recorded_at: string;
  operation: string;
  query_id: string;
  outcome: "ok" | "error" | "timeout" | "canceled" | string;
  stage?: string;
  deadline_source?: string;
  total_ms: number;
  sql_ms: number;
  read_queue_ms: number;
  rows_returned: number;
  error_type?: string;
  sqlite_error_code?: number;
}

interface DiagnosticsResponse {
  diagnostics: Diagnostic[];
  total: number;
  error_count: number;
  slow_count: number;
  has_more: boolean;
}

function apiURL(projectId: string, installId: number): string {
  const url = new URL(`${API}/diagnostics`, window.location.origin);
  url.searchParams.set("project_id", projectId);
  url.searchParams.set("install_id", String(installId));
  url.searchParams.set("limit", "30");
  return url.pathname + url.search;
}

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function percentile(values: number[], fraction: number): number {
  if (!values.length) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.min(sorted.length - 1, Math.ceil(sorted.length * fraction) - 1)];
}

function outcomeClass(outcome: string): string {
  if (outcome === "ok") return "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400";
  if (outcome === "timeout") return "bg-amber-500/15 text-amber-600 dark:text-amber-400";
  if (outcome === "canceled") return "bg-sky-500/15 text-sky-600 dark:text-sky-400";
  return "bg-red/10 text-red";
}

export default function TablesDiagnosticsWidget({ projectId, installId, compact = true }: TablesDiagnosticsWidgetProps) {
  const [data, setData] = useState<DiagnosticsResponse | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const load = useCallback(async () => {
    if (!projectId) return;
    try {
      const response = await fetch(apiURL(projectId, installId), { credentials: "same-origin" });
      if (!response.ok) throw new Error(await response.text());
      setData((await response.json()) as DiagnosticsResponse);
      setError("");
    } catch (caught) {
      setError((caught as Error).message || "Could not load diagnostics");
    } finally {
      setLoading(false);
    }
  }, [installId, projectId]);
  useEffect(() => {
    void load();
    const timer = window.setInterval(load, 10000);
    return () => window.clearInterval(timer);
  }, [load]);

  const records = data?.diagnostics ?? [];
  const p95 = useMemo(() => percentile(records.map((item) => item.total_ms), 0.95), [records]);
  const shell = compact
    ? "flex h-full min-h-0 flex-col overflow-hidden rounded border border-border bg-bg-card"
    : "min-h-full overflow-auto bg-bg p-6";
  return (
    <section className={shell} aria-label="Tables diagnostics">
      <header className={`flex items-start justify-between gap-3 border-b border-border ${compact ? "px-4 py-3" : "bg-bg-card px-6 py-5"}`}>
        <div>
          <p className="text-[11px] uppercase tracking-[0.16em] text-text-dim">Operations</p>
          <h1 className={`${compact ? "text-sm" : "mt-1 text-xl"} font-semibold text-text`}>Read diagnostics</h1>
          <p className="mt-1 text-xs text-text-dim">Slow reads and database failures, with sensitive query data redacted.</p>
        </div>
        <button type="button" onClick={() => void load()} className="rounded-md border border-border px-2.5 py-1.5 text-xs text-text-muted hover:bg-bg-hover">Refresh</button>
      </header>
      {error && <div role="alert" className="border-b border-red/30 bg-red/10 px-4 py-2 text-xs text-red">{error}</div>}
      {loading && !data ? <p className="p-4 text-xs text-text-dim">Loading diagnostics…</p> : (
        <div className={`${compact ? "min-h-0 flex-1 overflow-auto" : "mx-auto max-w-5xl"} p-4`}>
          <div className="grid gap-3 sm:grid-cols-4">
            <Metric label="Recorded" value={String(data?.total ?? 0)} />
            <Metric label="Errors" value={String(data?.error_count ?? 0)} tone={(data?.error_count ?? 0) > 0 ? "error" : "neutral"} />
            <Metric label="Slow / timeout" value={String(data?.slow_count ?? 0)} tone={(data?.slow_count ?? 0) > 0 ? "warn" : "neutral"} />
            <Metric label="Recent p95" value={`${Math.round(p95)} ms`} />
          </div>
          {records.length === 0 ? <div className="mt-4 rounded-lg border border-dashed border-border p-6 text-center text-xs text-text-dim">No slow or failed reads recorded.</div> : (
            <div className="mt-4 overflow-hidden rounded-lg border border-border bg-bg-card">
              <div className="grid grid-cols-[minmax(0,1fr)_auto_auto] gap-3 border-b border-border px-3 py-2 text-[10px] uppercase tracking-wide text-text-dim"><span>Operation</span><span>Duration</span><span>Outcome</span></div>
              <div className="divide-y divide-border">
                {records.map((item) => <div key={item.id} className="grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-3 px-3 py-2.5 text-xs"><div className="min-w-0"><div className="flex items-center gap-2"><span className="truncate font-mono text-text">{item.operation}</span><span className="text-[10px] text-text-dim">{formatTime(item.recorded_at)}</span></div><div className="mt-1 flex flex-wrap gap-x-3 text-[10px] text-text-dim"><span>query {item.query_id}</span>{item.stage && <span>stage {item.stage}</span>}{item.read_queue_ms > 0 && <span>queue {item.read_queue_ms} ms</span>}{item.error_type && <span>{item.error_type}</span>}</div></div><span className="font-mono tabular-nums text-text">{item.total_ms} ms</span><span className={`rounded-full px-2 py-0.5 text-[10px] font-medium ${outcomeClass(item.outcome)}`}>{item.outcome}</span></div>)}
              </div>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function Metric({ label, value, tone = "neutral" }: { label: string; value: string; tone?: "neutral" | "error" | "warn" }) {
  const valueClass = tone === "error" ? "text-red" : tone === "warn" ? "text-amber-600 dark:text-amber-400" : "text-text";
  return <div className="rounded-lg border border-border bg-bg-card px-3 py-2.5"><div className="text-[10px] uppercase tracking-wide text-text-dim">{label}</div><div className={`mt-1 font-mono text-lg font-semibold ${valueClass}`}>{value}</div></div>;
}
