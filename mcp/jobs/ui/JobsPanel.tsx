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
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents. Every
    // panel mounted in the same realm reuses one EventSource per
    // (app, project) instead of opening its own. Without this, a few
    // panels mounted in the agent detail page burn the browser's
    // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
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

type ScheduleKind = "once" | "every" | "cron" | "random";
type Status = "pending" | "running" | "done" | "failed" | "cancelled";

interface Job {
  id: number;
  name: string;
  schedule_kind: ScheduleKind;
  every_seconds?: number;
  cron_expr?: string;
  run_at?: string;
  next_run_at?: string;
  scheduled_for?: string;
  timezone: string;
  random?: {
    period: "day";
    runs_per_period: number;
    window_start: string;
    window_end: string;
    min_spacing_minutes: number;
  };
  status: Status;
  target: unknown;
  owner_app?: string;
  owner_instance?: number;
}

interface JobRun {
  id: number;
  started_at: string;
  duration_ms: number;
  status: "ok" | "error" | "timeout" | "running" | "interrupted";
  http_status?: number;
  error?: string;
  scheduled_for?: string;
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
  if (j.schedule_kind === "random" && j.random) {
    return `${j.random.runs_per_period} random/day · ${j.random.window_start}-${j.random.window_end}`;
  }
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
    case "done":      return "bg-green/15 text-green";
    case "failed":    return "bg-red/15 text-red";
    case "cancelled": return "bg-border text-text-muted";
    default:          return "bg-border text-text-muted";
  }
}

export default function JobsPanel(props: NativePanelProps) {
  return <JobsScope key={`${props.projectId}:${props.installId}`} {...props} />;
}
function JobsScope(props: NativePanelProps) {
  const [global, setGlobal] = useState(false);
  const [canShowGlobal, setCanShowGlobal] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    const q = new URLSearchParams({install_id: String(props.installId), ...(props.projectId ? {project_id: props.projectId} : {scope: "global"})});
    fetch(`${API}/scope?${q}`, {credentials: "same-origin", signal: controller.signal})
      .then(async (r): Promise<{global_install?: boolean}> => r.ok ? r.json() : {}).then(r => { if (!controller.signal.aborted) setCanShowGlobal(!!r.global_install); }).catch(() => {});
    return () => controller.abort();
  }, [props.installId, props.projectId]);
  return <div className="h-full flex flex-col">
    {canShowGlobal && props.projectId && <label className="px-6 py-2 text-sm">Scope <select aria-label="Job scope" value={global ? "global" : "project"} onChange={e => setGlobal(e.target.value === "global")}>
      <option value="project">Current project</option><option value="global">Global jobs (admin)</option>
    </select></label>}
    <ScopedJobsPanel key={global ? "global" : props.projectId} {...props} projectId={global ? "" : props.projectId}/>
  </div>;
}
function ScopedJobsPanel({ projectId, installId }: NativePanelProps) {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [statusFilter, setStatusFilter] = useState<Status | "">("");
  const [search, setSearch] = useState("");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [detail, setDetail] = useState<Job | null>(null);
  const [runs, setRuns] = useState<JobRun[]>([]);
  const [nextCursor, setNextCursor] = useState<number | null>(null);
  const [runsCursor, setRunsCursor] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [actionBusy, setActionBusy] = useState(false);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const selectedRef = useRef<number | null>(null);
  const listVersion = useRef(0);
  const detailVersion = useRef(0);
  const active = useRef(true);
  const actionLock = useRef(false);
  const controllers = useRef(new Set<AbortController>());
  useEffect(() => {
    active.current = true;
    return () => { active.current = false; listVersion.current++; detailVersion.current++; controllers.current.forEach(c => c.abort()); };
  }, []);

  const withParams = useCallback((extra: Record<string, string> = {}) => new URLSearchParams({
    ...(projectId ? {project_id: projectId} : {scope: "global"}), install_id: String(installId), ...extra,
  }).toString(), [projectId, installId]);
  const api = useCallback(async <T,>(method: string, path: string, body?: unknown, extra: Record<string, string> = {}): Promise<T> => {
    const controller = new AbortController(); controllers.current.add(controller);
    try {
      const res = await fetch(`${API}${path}?${withParams(extra)}`, {
        method, credentials: "same-origin", signal: controller.signal,
        headers: body ? {"Content-Type": "application/json"} : {}, body: body ? JSON.stringify(body) : undefined,
      });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return await res.json();
    } finally { controllers.current.delete(controller); }
  }, [withParams]);

  const loadList = useCallback(async (before?: number) => {
    const version = ++listVersion.current;
    setLoading(true);
    try {
      const r = await api<{jobs: Job[]; next_cursor?: number}>("GET", "/jobs", undefined, {
        ...(statusFilter ? {status: statusFilter} : {}), ...(search ? {search} : {}), ...(before ? {before: String(before)} : {}),
      });
      if (!active.current || version !== listVersion.current) return;
      setJobs(old => before ? [...old, ...r.jobs.filter(j => !old.some(o => o.id === j.id))] : r.jobs);
      setNextCursor(r.next_cursor || null); setError("");
    } catch(e) { if (active.current && version === listVersion.current) setError((e as Error).message); }
    finally { if (active.current && version === listVersion.current) setLoading(false); }
  }, [api, statusFilter, search]);
  const loadDetail = useCallback(async (id: number) => {
    const version = ++detailVersion.current;
    try {
      const [j, r] = await Promise.all([
        api<{job: Job}>("GET", `/jobs/${id}`), api<{runs: JobRun[]; next_cursor?: number}>("GET", `/jobs/${id}/runs`),
      ]);
      if (!active.current || selectedRef.current !== id || version !== detailVersion.current) return;
      setDetail(j.job); setRuns(r.runs); setRunsCursor(r.next_cursor || null);
    } catch(e) { if (active.current && selectedRef.current === id && version === detailVersion.current) {setDetail(null);setError((e as Error).message);} }
  }, [api]);
  const loadMoreRuns = async () => {
    const id = selectedRef.current, cursor = runsCursor, version = ++detailVersion.current;
    if (!id || !cursor) return;
    try {
      const r = await api<{runs: JobRun[]; next_cursor?: number}>("GET", `/jobs/${id}/runs`, undefined, {before: String(cursor)});
      if (!active.current || selectedRef.current !== id || version !== detailVersion.current) return;
      setRuns(old => [...old, ...r.runs.filter(r => !old.some(o => o.id === r.id))]);setRunsCursor(r.next_cursor || null);
    } catch(e) {if (active.current && selectedRef.current === id && version === detailVersion.current) setError((e as Error).message);}
  };
  useEffect(() => {
    listVersion.current++; setJobs([]);setNextCursor(null);
    const timer = setTimeout(() => loadList(), 200);
    return () => {clearTimeout(timer);listVersion.current++;};
  }, [loadList]);
  useEffect(() => {
    const timer = setInterval(() => {loadList();if (selectedRef.current) loadDetail(selectedRef.current);}, 30000);
    return () => clearInterval(timer);
  }, [loadList, loadDetail]);
  const eventTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {if (eventTimer.current) {clearTimeout(eventTimer.current);eventTimer.current = null;}}, [loadList, loadDetail]);
  useAppEvents("jobs", projectId, ev => {
    if (ev.install_id !== installId || !ev.topic.startsWith("job.") || eventTimer.current) return;
    eventTimer.current = setTimeout(() => {eventTimer.current = null;loadList();if (selectedRef.current) loadDetail(selectedRef.current);}, 200);
  });
  const act = async (method: string, suffix: string) => {
    const id = selectedRef.current;
    if (!id || !detail || detail.id !== id || actionLock.current) return;
    actionLock.current = true;setActionBusy(true);
    try {await api(method, `/jobs/${id}${suffix}`);if (active.current) {if (selectedRef.current === id) await loadDetail(id);await loadList();}}
    catch(e) {if (active.current) setError((e as Error).message);}
    finally {actionLock.current = false;if (active.current) setActionBusy(false);}
  };
  const handleRunNow = () => act("POST", "/run-now");
  const handleCancel = () => act("DELETE", "");
  const select = (id: number) => {
    selectedRef.current = id; detailVersion.current++;setSelectedId(id);setDetail(null);setRuns([]);setRunsCursor(null);loadDetail(id);
  };
  const closeDetail = () => {
    selectedRef.current = null;detailVersion.current++;setSelectedId(null);setDetail(null);setRuns([]);setRunsCursor(null);
  };

  return (
    <div className="h-full flex flex-col">
      {/* Header */}
      <header className="px-6 py-3 border-b border-border flex items-center gap-3">
        <h1 className="text-text font-medium">Jobs</h1>
        <span className="text-text-dim text-xs">
          {jobs.length} loaded
        </span>
        <select
          aria-label="Filter jobs by status"
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value as Status | "")}
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm ml-4"
        >
          <option value="">all</option>
          <option value="pending">pending</option>
          <option value="running">running</option>
          <option value="done">done</option>
          <option value="failed">failed</option>
          <option value="cancelled">cancelled</option>
        </select>
        <button
          type="button"
          onClick={() => loadList()} disabled={loading}
          className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
        >Refresh</button>
        <input aria-label="Search jobs by name or ID" placeholder="Name or ID" value={search} maxLength={200} onChange={e => setSearch(e.target.value)} className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
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
                  key={j.id} tabIndex={0} onKeyDown={e => {if (e.key === "Enter" || e.key === " ") {e.preventDefault();select(j.id);}}}
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
        {nextCursor && <button disabled={loading} onClick={() => loadList(nextCursor)} className="m-4 px-3 py-1 border border-border rounded">{loading ? "Loading…" : "Load more jobs"}</button>}
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
          key={detail.id} job={detail}
          runs={runs} busy={actionBusy} error={error} hasMoreRuns={!!runsCursor} onMoreRuns={loadMoreRuns}
          onClose={closeDetail}
          onRunNow={handleRunNow}
          onCancel={handleCancel}
        />
      )}
    </div>
  );
}

function useModal(onClose: () => void) {
  const ref = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);close.current = onClose;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const root = ref.current;root?.focus();
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") {e.preventDefault();close.current();}
      if (e.key !== "Tab" || !root) return;
      const elements = Array.from(root.querySelectorAll<HTMLElement>('button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex="0"]'));
      if (!elements.length) {e.preventDefault();return;}
      const first = elements[0], last = elements[elements.length - 1];
      if (e.shiftKey && (document.activeElement === first || document.activeElement === root)) {e.preventDefault();last.focus();}
      else if (!e.shiftKey && (document.activeElement === last || document.activeElement === root)) {e.preventDefault();first.focus();}
    };
    root?.addEventListener("keydown", handler);
    return () => {root?.removeEventListener("keydown", handler);previous?.focus();};
  }, []);
  return ref;
}

// ─── Detail dialog ────────────────────────────────────────────────────
// Modal showing one job's metadata, target, recent runs, and the
// run-now / cancel actions. Replaces the old right-pane detail view.

function DetailDialog({
  job, runs, onClose, onRunNow, onCancel, busy, error, hasMoreRuns, onMoreRuns,
}: {
  job: Job;
  runs: JobRun[];
  busy: boolean; error: string; hasMoreRuns: boolean; onMoreRuns: () => Promise<void>;
  onClose: () => void;
  onRunNow: () => void | Promise<void>;
  onCancel: () => void | Promise<void>;
}) {
  const modalRef = useModal(onClose);
  const [confirming, setConfirming] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const cancelled = job.status === "cancelled";
  const terminal = cancelled || job.status === "done" || job.status === "failed";
  const handleConfirmCancel = async () => {
    setCancelling(true);
    try { await onCancel(); }
    finally { setCancelling(false); setConfirming(false); }
  };
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        ref={modalRef} role="dialog" aria-modal="true" aria-label={`Job ${job.name}`} tabIndex={-1}
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
          <button aria-label="Close dialog" onClick={onClose} className="text-text-muted hover:text-text text-xl leading-none">×</button>
        </header>

        {error && <p role="alert" className="text-red text-sm break-words">{error}</p>}
        <section>
          <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Target (payload and credentials hidden)</h3>
          <pre className="text-[11px] bg-bg-input border border-border rounded p-3 overflow-auto whitespace-pre-wrap">
            {JSON.stringify(job.target, null, 2)}
          </pre>
        </section>
        {hasMoreRuns && <button onClick={onMoreRuns} className="text-accent">Load older runs</button>}

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onRunNow}
            disabled={cancelled || job.status === "running" || cancelling || busy}
            className="px-3 py-1 text-sm border border-accent text-accent rounded disabled:opacity-50"
          >Run now</button>
          {terminal ? (
            <span className="ml-auto text-text-dim text-xs">Job is {job.status}</span>
          ) : confirming ? (
            <div className="ml-auto flex items-center gap-2">
              <span className="text-text-muted text-xs">Cancel this job?</span>
              <button
                type="button"
                onClick={() => setConfirming(false)}
                disabled={cancelling}
                className="px-3 py-1 text-sm border border-border text-text-muted rounded"
              >Keep</button>
              <button
                type="button"
                onClick={handleConfirmCancel}
                disabled={cancelling}
                className="px-3 py-1 text-sm bg-red text-white rounded font-bold disabled:opacity-50"
              >{cancelling ? "Cancelling…" : "Yes, cancel"}</button>
            </div>
          ) : (
            <button
              type="button"
              onClick={() => setConfirming(true)}
              className="px-3 py-1 text-sm text-red border border-red rounded ml-auto"
            >Cancel job</button>
          )}
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
                  <th className="text-left px-3 py-2 font-normal">Scheduled</th>
                  <th className="text-left px-3 py-2 font-normal w-20">Duration</th>
                  <th className="text-left px-3 py-2 font-normal w-24">Status</th>
                  <th className="text-left px-3 py-2 font-normal w-16">HTTP</th>
                  <th className="text-left px-3 py-2 font-normal">Error</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((r) => (
                  <tr key={r.id} className="border-t border-border">
                    <td className="px-3 py-2 text-text-muted" title={`Started ${r.started_at}`}>{relTime(r.scheduled_for || r.started_at)}</td>
                    <td className="px-3 py-2 text-text-muted">{r.duration_ms} ms</td>
                    <td className="px-3 py-2">
                      <span className={`text-[10px] px-1.5 py-0.5 rounded ${
                        r.status === "ok" ? "bg-green/15 text-green" :
                        r.status === "error" || r.status === "timeout" ? "bg-red/15 text-red" :
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
// Random previews are calculated by the backend so creation and dispatch use
// the same seed, timezone database, and schedule algorithm.

type ApiFn = <T,>(method: string, path: string, body?: unknown, extra?: Record<string, string>) => Promise<T>;

function CreateJobDialog({
  onClose, onCreated, api,
}: {
  onClose: () => void;
  onCreated: () => void;
  api: ApiFn;
}) {
  const submitLock = useRef(false);
  const modalRef = useModal(() => {if (!submitLock.current) onClose();});
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
  const [timezone, setTimezone] = useState("UTC");
  const [randomRuns, setRandomRuns] = useState("5");
  const [randomWindowStart, setRandomWindowStart] = useState("08:00");
  const [randomWindowEnd, setRandomWindowEnd] = useState("22:00");
  const [randomMinSpacing, setRandomMinSpacing] = useState("60");
  const [scheduleSeed, setScheduleSeed] = useState("");
  const [previewRuns, setPreviewRuns] = useState<string[]>([]);
  const [previewError, setPreviewError] = useState("");
  const [previewing, setPreviewing] = useState(false);

  const [targetKind, setTargetKind] = useState<"event" | "http" | "app_tool">("event");
  const [agentId, setAgentId] = useState("");
  const [eventMessage, setEventMessage] = useState("");
  const [httpUrl, setHttpUrl] = useState("");
  const [httpMethod, setHttpMethod] = useState("POST");
  const [httpBodyText, setHttpBodyText] = useState("");
  const [appName, setAppName] = useState("functions");
  const [toolName, setToolName] = useState("functions_invoke");
  const [toolInputText, setToolInputText] = useState('{"name":"my-function","event":{}}');

  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (scheduleKind !== "random") {
      setPreviewRuns([]);
      setPreviewError("");
      setPreviewing(false);
      return;
    }
    const runs = Number(randomRuns);
    const spacing = Number(randomMinSpacing);
    if (!Number.isInteger(runs) || runs < 1 || !Number.isInteger(spacing) || spacing < 0 || !timezone.trim()) {
      setPreviewRuns([]);
      setPreviewing(false);
      return;
    }
    setPreviewing(true);
    let cancelled = false;
    const timer = window.setTimeout(async () => {
      try {
        const result = await api<{ runs: string[]; schedule_seed: string }>("POST", "/preview", {
          schedule: {
            kind: "random",
            period: "day",
            runs_per_period: runs,
            window_start: randomWindowStart,
            window_end: randomWindowEnd,
            min_spacing_minutes: spacing,
          },
          timezone: timezone.trim(),
          schedule_seed: scheduleSeed || undefined,
          limit: 5,
        });
        if (cancelled) return;
        setScheduleSeed(result.schedule_seed);
        setPreviewRuns(result.runs || []);
        setPreviewError("");
        setPreviewing(false);
      } catch (e) {
        if (cancelled) return;
        setPreviewRuns([]);
        setPreviewError((e as Error).message);
        setPreviewing(false);
      }
    }, 250);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [api, scheduleKind, timezone, randomRuns, randomWindowStart, randomWindowEnd, randomMinSpacing, scheduleSeed]);

  const submit = async () => {
    if (submitLock.current) return;
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
      const seconds = n * mult;
      if (!Number.isSafeInteger(seconds) || seconds < 1 || seconds > 31536000) {setErr("Interval must be whole seconds from 1 second to 365 days.");return;}
      schedule.every_seconds = seconds;
    } else if (scheduleKind === "cron") {
      if (!cronExpr.trim()) { setErr("Cron expression required."); return; }
      schedule.cron = cronExpr.trim();
    } else {
      const runs = Number(randomRuns);
      const spacing = Number(randomMinSpacing);
      if (!Number.isInteger(runs) || runs < 1 || runs > 100) { setErr("Runs per day must be a positive integer."); return; }
      if (!Number.isInteger(spacing) || spacing < 0 || spacing > 1440) { setErr("Minimum spacing must be zero or more minutes."); return; }
      schedule.period = "day";
      schedule.runs_per_period = runs;
      schedule.window_start = randomWindowStart;
      schedule.window_end = randomWindowEnd;
      schedule.min_spacing_minutes = spacing;
    }

    const target: Record<string, unknown> = { kind: targetKind };
    if (targetKind === "event") {
      const id = Number(agentId);
      if (!Number.isSafeInteger(id) || id <= 0) { setErr("Agent ID must be a positive integer."); return; }
      if (!eventMessage.trim()) { setErr("Event message required."); return; }
      target.agent_id = id;
      target.message = eventMessage;
    } else if (targetKind === "http") {
      if (!httpUrl.trim()) { setErr("HTTP target requires an absolute URL."); return; }
      target.url = httpUrl.trim();
      target.method = httpMethod;
      if (httpBodyText.trim()) {
        try {
          target.body = JSON.parse(httpBodyText);
        } catch {
          setErr("Body must be valid JSON (or leave blank).");
          return;
        }
      }
    } else {
      if (!appName.trim() || !toolName.trim()) { setErr("App and tool are required."); return; }
      target.app = appName.trim();
      target.tool = toolName.trim();
      try {
        const input = toolInputText.trim() ? JSON.parse(toolInputText) : {};
        if (!input || Array.isArray(input) || typeof input !== "object") throw new Error();
        target.input = input;
      } catch {
        setErr("Tool input must be a JSON object.");
        return;
      }
    }

    submitLock.current = true;setSubmitting(true);
    try {
      await api("POST", "/jobs", {
        name: name.trim(),
        schedule,
        target,
        timezone: scheduleKind === "once" || scheduleKind === "every" ? "UTC" : timezone.trim() || "UTC",
        schedule_seed: scheduleKind === "random" ? scheduleSeed : undefined,
      });
      onCreated();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      submitLock.current = false;setSubmitting(false);
    }
  };

  const inputCls = "w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm";
  const labelCls = "text-xs uppercase tracking-wide text-text-dim";

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" onClick={() => {if (!submitLock.current) onClose();}}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        ref={modalRef} role="dialog" aria-modal="true" aria-label="Create job" tabIndex={-1}
        className="relative bg-bg-card border border-border rounded-lg shadow-lg max-w-2xl w-full mx-4 overflow-auto flex flex-col max-h-[90vh] p-4 gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <div className="text-text font-medium">New job</div>
          <button aria-label="Close dialog" onClick={() => {if (!submitLock.current) onClose();}} className="text-text-muted hover:text-text">×</button>
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
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-1">
            {(["once", "every", "cron", "random"] as ScheduleKind[]).map((k) => (
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
            <label className="text-sm">Run at ({Intl.DateTimeFormat().resolvedOptions().timeZone})
            <input
              type="datetime-local"
              value={runAtLocal}
              onChange={(e) => setRunAtLocal(e.target.value)}
              className={inputCls + " mt-1"}
            />
              <span className="block text-xs text-text-muted mt-1">{Number.isNaN(new Date(runAtLocal).getTime()) ? "Choose a valid local time" : `Resolves to ${new Date(runAtLocal).toISOString()}`}</span>
            </label>
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
          {scheduleKind === "random" && (
            <div className="grid grid-cols-2 gap-2 mt-1">
              <label className="flex flex-col gap-1 text-xs text-text-dim">
                Runs per day
                <input
                  type="number"
                  min="1"
                  max="100"
                  value={randomRuns}
                  onChange={(e) => setRandomRuns(e.target.value)}
                  className={inputCls}
                />
              </label>
              <label className="flex flex-col gap-1 text-xs text-text-dim">
                Minimum spacing
                <div className="flex items-center gap-2">
                  <input
                    type="number"
                    min="0"
                    max="1440"
                    value={randomMinSpacing}
                    onChange={(e) => setRandomMinSpacing(e.target.value)}
                    className={inputCls}
                  />
                  <span className="text-text-muted">min</span>
                </div>
              </label>
              <label className="flex flex-col gap-1 text-xs text-text-dim">
                Daily start
                <input type="time" value={randomWindowStart} onChange={(e) => setRandomWindowStart(e.target.value)} className={inputCls} />
              </label>
              <label className="flex flex-col gap-1 text-xs text-text-dim">
                Daily end
                <input type="time" value={randomWindowEnd} onChange={(e) => setRandomWindowEnd(e.target.value)} className={inputCls} />
              </label>
            </div>
          )}
          {(scheduleKind === "cron" || scheduleKind === "random") && (
          <label className="flex flex-col gap-1 mt-1 text-xs text-text-dim">
            Timezone (cron and random schedules)
            <input
              type="text"
              value={timezone}
              onChange={(e) => setTimezone(e.target.value)}
              placeholder="Europe/Paris"
              className={inputCls}
            />
          </label>
          )}
          {scheduleKind === "random" && (
            <div className="border-t border-border pt-2 mt-1">
              <div className="text-xs uppercase tracking-wide text-text-dim mb-1">Next five runs</div>
              {previewing ? (
                <div className="text-text-dim text-xs">Calculating...</div>
              ) : previewError ? (
                <div className="text-red text-xs">{previewError}</div>
              ) : previewRuns.length ? (
                <ol className="grid gap-1 text-xs text-text-muted">
                  {previewRuns.map((run) => <li key={run}>{run}</li>)}
                </ol>
              ) : null}
            </div>
          )}
        </div>

        <div className="flex flex-col gap-1">
          <label className={labelCls}>Target</label>
          <div className="flex gap-1">
            {(["event", "app_tool", "http"] as const).map((k) => (
              <button
                key={k}
                type="button"
                onClick={() => setTargetKind(k)}
                className={`flex-1 px-2 py-1 text-xs border rounded ${
                  targetKind === k
                    ? "border-accent text-accent bg-accent/10"
                    : "border-border text-text-muted hover:bg-bg-input"
                }`}
              >{k === "app_tool" ? "app tool" : k}</button>
            ))}
          </div>
          {targetKind === "event" ? (
            <div className="flex flex-col gap-2 mt-1">
              <input
                type="text"
                value={agentId}
                onChange={(e) => setAgentId(e.target.value)}
                placeholder="agent ID"
                className={inputCls}
              />
              <textarea
                value={eventMessage}
                onChange={(e) => setEventMessage(e.target.value)}
                placeholder="Message to deliver to the agent"
                className={inputCls + " min-h-[64px]"}
              />
            </div>
          ) : targetKind === "http" ? (
            <div className="flex flex-col gap-2 mt-1">
              <input
                type="text"
                value={httpUrl}
                onChange={(e) => setHttpUrl(e.target.value)}
                placeholder="absolute HTTP(S) URL"
                className={inputCls}
              />
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
          ) : (
            <div className="flex flex-col gap-2 mt-1">
              <div className="flex gap-2">
                <input value={appName} onChange={(e) => setAppName(e.target.value)} placeholder="app slug" className={inputCls} />
                <input value={toolName} onChange={(e) => setToolName(e.target.value)} placeholder="tool name" className={inputCls} />
              </div>
              <textarea
                value={toolInputText}
                onChange={(e) => setToolInputText(e.target.value)}
                placeholder='Tool input JSON, e.g. {"name":"daily-report","event":{}}'
                className={inputCls + " min-h-[80px] font-mono"}
              />
            </div>
          )}
        </div>

        {err && <div className="text-red text-xs">{err}</div>}

        <div className="flex gap-2 justify-end mt-1">
          <button
            type="button"
            onClick={() => {if (!submitLock.current) onClose();}}
            className="px-3 py-1.5 text-sm text-text-muted"
          >Cancel</button>
          <button
            type="button"
            onClick={submit}
            disabled={submitting || !name.trim() || (scheduleKind === "random" && (previewing || !scheduleSeed || !!previewError))}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >{submitting ? "Creating…" : "Create"}</button>
        </div>
      </div>
    </div>
  );
}
