// JobsPanel — native React panel for the jobs app. Two-pane: list of
// scheduled jobs on the left, detail + recent runs on the right.
// Loaded by the dashboard via dynamic import; uses host React via
// importmap; talks to the jobs sidecar through /api/apps/jobs/* with
// same-origin cookies.

import { useCallback, useEffect, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

type ScheduleKind = "once" | "every" | "cron";
type Status = "scheduled" | "running" | "succeeded" | "failed" | "cancelled" | "paused";

interface Job {
  id: string;
  name: string;
  schedule_kind: ScheduleKind;
  every_seconds?: number;
  cron_expr?: string;
  run_at?: string;
  next_run_at?: string;
  status: Status;
  target: unknown;
  owner_app?: string;
  owner_instance?: number;
}

interface JobRun {
  id: string;
  started_at: string;
  duration_ms: number;
  status: "succeeded" | "failed" | "skipped";
  http_status?: number;
  error?: string;
}

const API = "/api/apps/jobs";

function humaniseSchedule(j: Job): string {
  if (j.schedule_kind === "once") return "once at " + relTime(j.run_at);
  if (j.schedule_kind === "every") {
    const s = j.every_seconds || 0;
    if (s % 3600 === 0) return `every ${s / 3600}h`;
    if (s % 60 === 0) return `every ${s / 60}m`;
    return `every ${s}s`;
  }
  if (j.schedule_kind === "cron") return `cron: ${j.cron_expr}`;
  return j.schedule_kind;
}

function relTime(s?: string): string {
  if (!s) return "—";
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  const diff = (d.getTime() - Date.now()) / 1000;
  const abs = Math.abs(diff);
  const sign = diff >= 0 ? "in " : "";
  const past = diff < 0 ? " ago" : "";
  if (abs < 60) return `${sign}${Math.round(abs)}s${past}`;
  if (abs < 3600) return `${sign}${Math.round(abs / 60)}m${past}`;
  if (abs < 86400) return `${sign}${Math.round(abs / 3600)}h${past}`;
  return d.toLocaleString();
}

function statusTone(s: Status): string {
  switch (s) {
    case "running":   return "bg-accent/15 text-accent";
    case "succeeded": return "bg-green/15 text-green";
    case "failed":    return "bg-red/15 text-red";
    case "cancelled": return "bg-border text-text-muted";
    case "paused":    return "bg-blue/15 text-blue";
    default:          return "bg-border text-text-muted";
  }
}

export default function JobsPanel({ projectId, installId }: NativePanelProps) {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [statusFilter, setStatusFilter] = useState<Status | "">("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detail, setDetail] = useState<Job | null>(null);
  const [runs, setRuns] = useState<JobRun[]>([]);
  const [error, setError] = useState("");

  const withParams = useCallback(
    (extra: Record<string, string> = {}) => {
      const u = new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      });
      return u.toString();
    },
    [projectId, installId],
  );

  const api = useCallback(
    async <T,>(method: string, path: string, body?: unknown, extra: Record<string, string> = {}): Promise<T> => {
      const res = await fetch(`${API}${path}?${withParams(extra)}`, {
        method,
        credentials: "same-origin",
        headers: body ? { "Content-Type": "application/json" } : {},
        body: body ? JSON.stringify(body) : undefined,
      });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.json();
    },
    [withParams],
  );

  const loadList = useCallback(async () => {
    try {
      const extra: Record<string, string> = {};
      if (statusFilter) extra.status = statusFilter;
      const r = await api<{ jobs?: Job[] }>("GET", "/jobs", undefined, extra);
      setJobs(r.jobs || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }, [api, statusFilter]);

  const loadDetail = useCallback(
    async (id: string) => {
      try {
        const j = await api<{ job: Job }>("GET", `/jobs/${id}`);
        const r = await api<{ runs?: JobRun[] }>("GET", `/jobs/${id}/runs`);
        setDetail(j.job);
        setRuns(r.runs || []);
      } catch (e) {
        setDetail(null);
        setError((e as Error).message);
      }
    },
    [api],
  );

  // Initial load + soft refresh every 5s so "running" / "next run"
  // stay current. The selected job's detail is reloaded too.
  useEffect(() => { loadList(); }, [loadList]);
  useEffect(() => {
    const id = setInterval(() => {
      loadList();
      if (selectedId) loadDetail(selectedId);
    }, 5000);
    return () => clearInterval(id);
  }, [loadList, loadDetail, selectedId]);

  const handleRunNow = async () => {
    if (!detail) return;
    try {
      await api("POST", `/jobs/${detail.id}/run-now`);
      await loadDetail(detail.id);
      await loadList();
    } catch (e) {
      alert("Run failed: " + (e as Error).message);
    }
  };

  const handleCancel = async () => {
    if (!detail) return;
    if (!confirm("Cancel this job?")) return;
    try {
      await api("DELETE", `/jobs/${detail.id}`);
      await loadDetail(detail.id);
      await loadList();
    } catch (e) {
      alert("Cancel failed: " + (e as Error).message);
    }
  };

  const select = (id: string) => {
    setSelectedId(id);
    loadDetail(id);
  };

  return (
    <div className="h-full flex">
      {/* List */}
      <aside className="w-96 border-r border-border flex flex-col">
        <div className="p-3 border-b border-border flex items-center gap-2">
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value as Status | "")}
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
          >
            <option value="">all</option>
            <option value="scheduled">scheduled</option>
            <option value="running">running</option>
            <option value="succeeded">succeeded</option>
            <option value="failed">failed</option>
            <option value="paused">paused</option>
            <option value="cancelled">cancelled</option>
          </select>
          <button
            type="button"
            onClick={loadList}
            className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
          >Refresh</button>
        </div>
        <div className="flex-1 overflow-auto">
          {error ? (
            <div className="p-4 text-red text-xs">{error}</div>
          ) : jobs.length === 0 ? (
            <div className="p-4 text-text-muted text-sm">No jobs scheduled.</div>
          ) : (
            <ul>
              {jobs.map((j) => (
                <li
                  key={j.id}
                  onClick={() => select(j.id)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    j.id === selectedId ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="flex items-center gap-2">
                    <span className="text-sm text-text font-medium truncate flex-1">{j.name}</span>
                    <span className={`text-[10px] px-1.5 py-0.5 rounded ${statusTone(j.status)}`}>
                      {j.status}
                    </span>
                  </div>
                  <div className="text-xs text-text-muted truncate mt-0.5">
                    {humaniseSchedule(j)} · next {relTime(j.next_run_at)}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="p-2 text-xs text-text-dim border-t border-border">
          {jobs.length} job{jobs.length !== 1 ? "s" : ""}
        </div>
      </aside>

      {/* Detail */}
      <main className="flex-1 overflow-auto p-6">
        {!detail ? (
          <div className="text-text-muted text-sm text-center mt-12">
            Select a job to see details and recent runs.
          </div>
        ) : (
          <div className="max-w-3xl space-y-5">
            <header>
              <div className="flex items-center gap-2 mb-1">
                <h1 className="text-xl text-text font-semibold">{detail.name}</h1>
                <span className={`text-[11px] px-2 py-0.5 rounded ${statusTone(detail.status)}`}>
                  {detail.status}
                </span>
              </div>
              <p className="text-text-muted text-sm">
                {humaniseSchedule(detail)} · next {relTime(detail.next_run_at)}
              </p>
              {detail.owner_app && (
                <p className="text-text-dim text-xs mt-1">
                  scheduled by <span className="text-text-muted">{detail.owner_app}</span>
                  {detail.owner_instance ? ` · instance ${detail.owner_instance}` : ""}
                </p>
              )}
            </header>

            <section>
              <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Target</h2>
              <pre className="text-[11px] bg-bg-input border border-border rounded p-3 overflow-auto whitespace-pre-wrap">
                {JSON.stringify(detail.target, null, 2)}
              </pre>
            </section>

            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={handleRunNow}
                className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
              >Run now</button>
              <button
                type="button"
                onClick={handleCancel}
                className="px-3 py-1 text-sm text-red border border-red/50 rounded hover:bg-red/10 ml-auto"
              >Cancel job</button>
            </div>

            <section>
              <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
                Recent runs ({runs.length})
              </h2>
              {runs.length === 0 ? (
                <p className="text-text-muted text-sm">No runs yet.</p>
              ) : (
                <table className="w-full text-sm">
                  <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50">
                    <tr>
                      <th className="text-left px-3 py-2 font-normal">Started</th>
                      <th className="text-left px-3 py-2 font-normal w-20">Duration</th>
                      <th className="text-left px-3 py-2 font-normal w-24">Status</th>
                      <th className="text-left px-3 py-2 font-normal w-16">HTTP</th>
                      <th className="text-left px-3 py-2 font-normal">Error</th>
                    </tr>
                  </thead>
                  <tbody>
                    {runs.map((r) => (
                      <tr key={r.id} className="border-t border-border">
                        <td className="px-3 py-2 text-text-muted">{relTime(r.started_at)}</td>
                        <td className="px-3 py-2 text-text-muted">{r.duration_ms} ms</td>
                        <td className="px-3 py-2">
                          <span className={`text-[10px] px-1.5 py-0.5 rounded ${
                            r.status === "succeeded" ? "bg-green/15 text-green" :
                            r.status === "failed" ? "bg-red/15 text-red" :
                            "bg-border text-text-muted"
                          }`}>{r.status}</span>
                        </td>
                        <td className="px-3 py-2 text-text-muted">{r.http_status ?? "—"}</td>
                        <td className="px-3 py-2 text-red text-xs truncate max-w-md" title={r.error}>
                          {r.error || ""}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </section>
          </div>
        )}
      </main>
    </div>
  );
}
