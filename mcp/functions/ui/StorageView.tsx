import { useEffect, useState } from "react";

type Api = <T,>(method: string, path: string, body?: unknown, extra?: Record<string, string>) => Promise<T>;
interface StorageReport {
  database_bytes: number;
  wal_bytes: number;
  reusable_bytes: number;
  auto_vacuum: string;
  oldest_retained?: string;
  cleanup_backlog: number;
  backlog_capped: boolean;
  retention: { summary_days: number; success_details_days: number; failure_details_days: number };
  alerts: { code: string; message: string }[];
  last_maintenance_error?: string;
}
function bytes(n: number) {
  if (n < 1024) return `${n} B`;
  const unit = Math.min(3, Math.floor(Math.log2(n) / 10));
  return `${(n / 1024 ** unit).toFixed(1)} ${["B", "KiB", "MiB", "GiB"][unit]}`;
}
export function StorageView({ api }: { api: Api }) {
  const [open, setOpen] = useState(false);
  const [report, setReport] = useState<StorageReport | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    let live = true;
    const load = async () => {
      try {
        const next = await api<StorageReport>("GET", "/storage");
        if (live) { setReport(next); setError(""); }
      } catch (e) { if (live) setError(e instanceof Error ? e.message : String(e)); }
    };
    void load();
    const timer = window.setInterval(load, 60000);
    return () => { live = false; window.clearInterval(timer); };
  }, [api, open]);
  return <details className="border-b border-border px-6 py-3 text-sm" onToggle={e => setOpen(e.currentTarget.open)}>
    <summary className="cursor-pointer text-text">Storage and retention</summary>
    {error && <p role="alert" className="text-red mt-2">{error}</p>}
    {!error && !report && open && <p className="text-text-muted mt-2">Loading storage health…</p>}
    {report && <div className="mt-3 space-y-3">
      <p className="text-xs text-text-muted">File sizes cover this Functions installation. Invocation age and cleanup backlog cover this project. Refreshes every minute.</p>
      <dl className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        {[["Database", bytes(report.database_bytes)], ["WAL", bytes(report.wal_bytes)], ["Reusable space", bytes(report.reusable_bytes)], ["Reclamation", report.auto_vacuum]].map(([label, value]) => <div key={label}><dt className="text-xs text-text-muted">{label}</dt><dd className="font-mono">{value}</dd></div>)}
      </dl>
      <p>Oldest invocation: {report.oldest_retained || "none"}. Cleanup backlog: {report.backlog_capped ? "1000+" : report.cleanup_backlog.toLocaleString()}.</p>
      <p className="text-text-muted">Summaries: {report.retention.summary_days} days. Successful call details: {report.retention.success_details_days} days. Failure details: {report.retention.failure_details_days} days.</p>
      {report.alerts.map(alert => <p role="status" key={alert.code} className="border border-border rounded p-2">{alert.message}</p>)}
      {report.last_maintenance_error && <p role="status" className="text-text-muted">Maintenance deferred: {report.last_maintenance_error}</p>}
    </div>}
  </details>;
}
