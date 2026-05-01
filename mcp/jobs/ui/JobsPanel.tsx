// JobsPanel — native React panel for the jobs app. Two-pane: list of
// scheduled jobs on the left, detail + recent runs on the right.
// Loaded by the dashboard via dynamic import; uses host React via
// importmap; talks to the jobs sidecar through /api/apps/jobs/* with
// same-origin cookies.

import { useCallback, useEffect, useRef, useState } from "react";

// Inlined SDK app-event subscription. Each app ships its own copy
// because panels are bundled standalone and apps install independently.
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

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
  const [creating, setCreating] = useState(false);

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

  // Live refresh: react instantly to schedule/cancel/queue events
  // (the 5s polling above stays as a safety net for status changes
  // the dispatcher writes to the DB without an explicit emit).
  useAppEvents("jobs", projectId, (ev) => {
    if (
      ev.topic === "job.scheduled" ||
      ev.topic === "job.cancelled" ||
      ev.topic === "job.queued"
    ) {
      loadList();
      if (selectedId) loadDetail(selectedId);
    }
  });

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

  const closeDetail = () => {
    setSelectedId(null);
    setDetail(null);
    setRuns([]);
  };

  return (
    <div className="h-full flex flex-col">
      {/* Header */}
      <header className="px-6 py-3 border-b border-border flex items-center gap-3">
        <h1 className="text-text font-medium">Jobs</h1>
        <span className="text-text-dim text-xs">
          {jobs.length} job{jobs.length !== 1 ? "s" : ""}
        </span>
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value as Status | "")}
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm ml-4"
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
        <button
          type="button"
          onClick={() => setCreating(true)}
          className="ml-auto px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
        >+ New job</button>
      </header>

      {/* List */}
      <main className="flex-1 overflow-auto">
        {error ? (
          <div className="p-6 text-red text-sm">{error}</div>
        ) : jobs.length === 0 ? (
          <div className="py-12 px-6 text-center text-text-muted text-sm">
            No jobs scheduled.{" "}
            <button
              type="button"
              onClick={() => setCreating(true)}
              className="text-accent"
            >Schedule one</button>
            .
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50 sticky top-0">
              <tr>
                <th className="text-left px-4 py-2 font-normal">Name</th>
                <th className="text-left px-4 py-2 font-normal w-48">Schedule</th>
                <th className="text-left px-4 py-2 font-normal w-32">Status</th>
                <th className="text-left px-4 py-2 font-normal w-40">Next run</th>
                <th className="text-left px-4 py-2 font-normal w-40">Owner</th>
              </tr>
            </thead>
            <tbody>
              {jobs.map((j) => (
                <tr
                  key={j.id}
                  onClick={() => select(j.id)}
                  className="border-t border-border cursor-pointer hover:bg-bg-input/50"
                >
                  <td className="px-4 py-2 text-text font-medium truncate max-w-md">{j.name}</td>
                  <td className="px-4 py-2 text-text-muted">{humaniseSchedule(j)}</td>
                  <td className="px-4 py-2">
                    <span className={`text-[10px] px-1.5 py-0.5 rounded ${statusTone(j.status)}`}>
                      {j.status}
                    </span>
                  </td>
                  <td className="px-4 py-2 text-text-muted">{relTime(j.next_run_at)}</td>
                  <td className="px-4 py-2 text-text-dim text-xs truncate">
                    {j.owner_app || "—"}
                    {j.owner_instance ? ` · #${j.owner_instance}` : ""}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </main>

      {/* Create dialog */}
      {creating && (
        <CreateJobDialog
          onClose={() => setCreating(false)}
          onCreated={async () => {
            setCreating(false);
            await loadList();
          }}
          api={api}
        />
      )}

      {/* Detail dialog */}
      {detail && (
        <DetailDialog
          job={detail}
          runs={runs}
          onClose={closeDetail}
          onRunNow={handleRunNow}
          onCancel={handleCancel}
        />
      )}
    </div>
  );
}

// ─── Detail dialog ────────────────────────────────────────────────────
// Modal showing one job's metadata, target, recent runs, and the
// run-now / cancel actions. Replaces the old right-pane detail view.

function DetailDialog({
  job, runs, onClose, onRunNow, onCancel,
}: {
  job: Job;
  runs: JobRun[];
  onClose: () => void;
  onRunNow: () => void;
  onCancel: () => void;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        className="relative bg-bg-card border border-border rounded-lg shadow-lg max-w-4xl w-full mx-4 overflow-auto flex flex-col max-h-[90vh] p-5 gap-4"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="flex items-start gap-3">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 mb-1">
              <h2 className="text-lg text-text font-semibold truncate">{job.name}</h2>
              <span className={`text-[11px] px-2 py-0.5 rounded ${statusTone(job.status)}`}>
                {job.status}
              </span>
            </div>
            <p className="text-text-muted text-sm">
              {humaniseSchedule(job)} · next {relTime(job.next_run_at)}
            </p>
            {job.owner_app && (
              <p className="text-text-dim text-xs mt-1">
                scheduled by <span className="text-text-muted">{job.owner_app}</span>
                {job.owner_instance ? ` · instance ${job.owner_instance}` : ""}
              </p>
            )}
          </div>
          <button onClick={onClose} className="text-text-muted hover:text-text text-xl leading-none">×</button>
        </header>

        <section>
          <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Target</h3>
          <pre className="text-[11px] bg-bg-input border border-border rounded p-3 overflow-auto whitespace-pre-wrap">
            {JSON.stringify(job.target, null, 2)}
          </pre>
        </section>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onRunNow}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >Run now</button>
          <button
            type="button"
            onClick={onCancel}
            className="px-3 py-1 text-sm text-red border border-red/50 rounded hover:bg-red/10 ml-auto"
          >Cancel job</button>
        </div>

        <section>
          <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">
            Recent runs ({runs.length})
          </h3>
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
    </div>
  );
}

// ─── Create-job dialog ────────────────────────────────────────────────
// Inline modal modeled on TasksPanel.Dialog. Posts to /jobs with the
// schema dbScheduleJob expects: { name, schedule:{kind,...}, target:{kind,...} }.
// Kept deliberately small — advanced fields (timezone, max_retries) are
// omitted; defaults on the backend are sane.

type ApiFn = <T,>(method: string, path: string, body?: unknown, extra?: Record<string, string>) => Promise<T>;

function CreateJobDialog({
  onClose, onCreated, api,
}: {
  onClose: () => void;
  onCreated: () => void;
  api: ApiFn;
}) {
  const [name, setName] = useState("");
  const [scheduleKind, setScheduleKind] = useState<ScheduleKind>("once");
  // once: a datetime-local string (browser local TZ). Default = now+5min.
  const [runAtLocal, setRunAtLocal] = useState(() => {
    const d = new Date(Date.now() + 5 * 60 * 1000);
    // toISOString → 'YYYY-MM-DDTHH:mm:ss.sssZ'; <input type=datetime-local>
    // wants 'YYYY-MM-DDTHH:mm' in local time.
    const pad = (n: number) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  });
  // every: store amount + unit, convert to seconds at submit time.
  const [everyAmount, setEveryAmount] = useState("5");
  const [everyUnit, setEveryUnit] = useState<"s" | "m" | "h">("m");
  const [cronExpr, setCronExpr] = useState("*/5 * * * *");

  const [targetKind, setTargetKind] = useState<"event" | "http">("event");
  const [agentId, setAgentId] = useState("self");
  const [eventMessage, setEventMessage] = useState("");
  const [httpApp, setHttpApp] = useState("");
  const [httpPath, setHttpPath] = useState("");
  const [httpUrl, setHttpUrl] = useState("");
  const [httpMethod, setHttpMethod] = useState("POST");
  const [httpBodyText, setHttpBodyText] = useState("");

  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    setErr("");
    if (!name.trim()) { setErr("Name is required."); return; }

    const schedule: Record<string, unknown> = { kind: scheduleKind };
    if (scheduleKind === "once") {
      const d = new Date(runAtLocal);
      if (isNaN(d.getTime())) { setErr("Invalid run-at time."); return; }
      schedule.run_at = d.toISOString();
    } else if (scheduleKind === "every") {
      const n = Number(everyAmount);
      if (!Number.isFinite(n) || n <= 0) { setErr("Interval must be > 0."); return; }
      const mult = everyUnit === "h" ? 3600 : everyUnit === "m" ? 60 : 1;
      schedule.every_seconds = Math.round(n * mult);
    } else {
      if (!cronExpr.trim()) { setErr("Cron expression required."); return; }
      schedule.cron = cronExpr.trim();
    }

    const target: Record<string, unknown> = { kind: targetKind };
    if (targetKind === "event") {
      if (!eventMessage.trim()) { setErr("Event message required."); return; }
      target.agent_id = agentId.trim() || "self";
      target.message = eventMessage;
    } else {
      // http: either {app, path} or {url}
      if (httpUrl.trim()) {
        target.url = httpUrl.trim();
      } else if (httpApp.trim() && httpPath.trim()) {
        target.app = httpApp.trim();
        target.path = httpPath.trim().startsWith("/") ? httpPath.trim() : "/" + httpPath.trim();
      } else {
        setErr("HTTP target needs either url, or both app and path.");
        return;
      }
      target.method = httpMethod;
      if (httpBodyText.trim()) {
        try {
          target.body = JSON.parse(httpBodyText);
        } catch {
          setErr("Body must be valid JSON (or leave blank).");
          return;
        }
      }
    }

    setSubmitting(true);
    try {
      await api("POST", "/jobs", { name: name.trim(), schedule, target });
      onCreated();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  const inputCls = "w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm";
  const labelCls = "text-xs uppercase tracking-wide text-text-dim";

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        className="relative bg-bg-card border border-border rounded-lg shadow-lg max-w-2xl w-full mx-4 overflow-auto flex flex-col max-h-[90vh] p-4 gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <div className="text-text font-medium">New job</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>

        <div className="flex flex-col gap-1">
          <label className={labelCls}>Name</label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Nightly digest"
            autoFocus
            className={inputCls}
          />
        </div>

        <div className="flex flex-col gap-1">
          <label className={labelCls}>Schedule</label>
          <div className="flex gap-1">
            {(["once", "every", "cron"] as ScheduleKind[]).map((k) => (
              <button
                key={k}
                type="button"
                onClick={() => setScheduleKind(k)}
                className={`flex-1 px-2 py-1 text-xs border rounded ${
                  scheduleKind === k
                    ? "border-accent text-accent bg-accent/10"
                    : "border-border text-text-muted hover:bg-bg-input"
                }`}
              >{k}</button>
            ))}
          </div>
          {scheduleKind === "once" && (
            <input
              type="datetime-local"
              value={runAtLocal}
              onChange={(e) => setRunAtLocal(e.target.value)}
              className={inputCls + " mt-1"}
            />
          )}
          {scheduleKind === "every" && (
            <div className="flex gap-2 mt-1">
              <input
                type="number"
                min="1"
                value={everyAmount}
                onChange={(e) => setEveryAmount(e.target.value)}
                className="flex-1 min-w-0 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              />
              <select
                value={everyUnit}
                onChange={(e) => setEveryUnit(e.target.value as "s" | "m" | "h")}
                className="w-24 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              >
                <option value="s">seconds</option>
                <option value="m">minutes</option>
                <option value="h">hours</option>
              </select>
            </div>
          )}
          {scheduleKind === "cron" && (
            <input
              type="text"
              value={cronExpr}
              onChange={(e) => setCronExpr(e.target.value)}
              placeholder="M H DOM MON DOW (e.g. */5 * * * *)"
              className={inputCls + " mt-1 font-mono"}
            />
          )}
        </div>

        <div className="flex flex-col gap-1">
          <label className={labelCls}>Target</label>
          <div className="flex gap-1">
            {(["event", "http"] as const).map((k) => (
              <button
                key={k}
                type="button"
                onClick={() => setTargetKind(k)}
                className={`flex-1 px-2 py-1 text-xs border rounded ${
                  targetKind === k
                    ? "border-accent text-accent bg-accent/10"
                    : "border-border text-text-muted hover:bg-bg-input"
                }`}
              >{k}</button>
            ))}
          </div>
          {targetKind === "event" ? (
            <div className="flex flex-col gap-2 mt-1">
              <input
                type="text"
                value={agentId}
                onChange={(e) => setAgentId(e.target.value)}
                placeholder="agent id (or 'self')"
                className={inputCls}
              />
              <textarea
                value={eventMessage}
                onChange={(e) => setEventMessage(e.target.value)}
                placeholder="Message to deliver to the agent"
                className={inputCls + " min-h-[64px]"}
              />
            </div>
          ) : (
            <div className="flex flex-col gap-2 mt-1">
              <input
                type="text"
                value={httpUrl}
                onChange={(e) => setHttpUrl(e.target.value)}
                placeholder="absolute URL (requires net.egress) — leave blank to use app+path"
                className={inputCls}
              />
              <div className="flex gap-2">
                <input
                  type="text"
                  value={httpApp}
                  onChange={(e) => setHttpApp(e.target.value)}
                  placeholder="app slug (e.g. crm)"
                  className="flex-1 min-w-0 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                  disabled={!!httpUrl.trim()}
                />
                <input
                  type="text"
                  value={httpPath}
                  onChange={(e) => setHttpPath(e.target.value)}
                  placeholder="/path"
                  className="flex-1 min-w-0 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                  disabled={!!httpUrl.trim()}
                />
              </div>
              <div className="flex gap-2">
                <select
                  value={httpMethod}
                  onChange={(e) => setHttpMethod(e.target.value)}
                  className="w-24 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                >
                  <option>POST</option>
                  <option>GET</option>
                  <option>PUT</option>
                  <option>PATCH</option>
                  <option>DELETE</option>
                </select>
                <textarea
                  value={httpBodyText}
                  onChange={(e) => setHttpBodyText(e.target.value)}
                  placeholder='JSON body (optional, e.g. {"x":1})'
                  className="flex-1 min-w-0 bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono"
                />
              </div>
            </div>
          )}
        </div>

        {err && <div className="text-red text-xs">{err}</div>}

        <div className="flex gap-2 justify-end mt-1">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1.5 text-sm text-text-muted"
          >Cancel</button>
          <button
            type="button"
            onClick={submit}
            disabled={submitting || !name.trim()}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >{submitting ? "Creating…" : "Create"}</button>
        </div>
      </div>
    </div>
  );
}
