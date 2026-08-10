// BackupPanel — UI for the backup app.
//
// Layout: four sections stacked vertically.
//   1. Status      — last successful run + last failed run (if any)
//   2. Destinations — local + S3 destinations the user can target
//   3. Policies    — cron-driven schedules with destination
//   4. History     — recent runs (status, size, destination, restore)
//
// Talks to /api/apps/backup/* through the platform proxy. Uses the
// dashboard's Tailwind theme tokens (bg-bg-card / text-text-muted /
// border-border / etc.) so the panel recolors across light/dark/cooler-
// dark themes — same idioms as live-link / storage.
//
// Hand-authored as .tsx so the dashboard's Tailwind v4 build picks up
// the class names. The bundled .mjs is produced by
// `bun run scripts/build-panels.ts` from the apps repo root.

import { useCallback, useEffect, useId, useRef, useState } from "react";

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

interface Destination {
  id: number;
  name: string;
  kind: "local" | "s3" | "storage_app";
  config: Record<string, unknown>;
  connection_id?: number;
  enabled: boolean;
  created_at?: string;
}

interface Policy {
  id: number;
  name: string;
  schedule: string;
  destination_id: number;
  retention_keep: number;
  enabled: boolean;
  jobs_id?: string;
  jobs_project_id?: string;
  scope: Scope;
  created_at?: string;
  updated_at?: string;
}

interface Run {
  id: number;
  policy_id?: number;
  destination_id: number;
  destination_name: string;
  started_at: string;
  finished_at?: string;
  status: "running" | "success" | "failed";
  stage?: string;
  bytes_compressed: number;
  sha256?: string;
  remote_key?: string;
  error?: string;
  encrypted: boolean;
  scope: Scope;
}

interface Scope {
  kind: "platform" | "fleet_tenant";
  id?: string;
  source_app?: string;
}

interface FleetTenantScope {
  id: string;
  slug: string;
  status: string;
  restorable: boolean;
}

interface ScopesResponse {
  default_retention: number;
  encryption_enabled: boolean;
  platform: { label: string; coverage: string; gaps: string[] };
  fleet_bound: boolean;
  fleet_error?: string;
  fleet_tenants: FleetTenantScope[];
}

const API = "/api/apps/backup";

function formatBytes(n: number): string {
  if (!n) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
}

function formatTime(s: string | undefined): string {
  if (!s) return "—";
  const date = new Date(s);
  return Number.isNaN(date.getTime()) ? s : date.toLocaleString();
}

function durationOf(r: Run): string {
  if (!r.finished_at) return r.status === "running" ? "running…" : "—";
  try {
    const start = new Date(r.started_at).getTime();
    const end = new Date(r.finished_at).getTime();
    const ms = end - start;
    if (!Number.isFinite(ms) || ms < 0) return "—";
    if (ms < 1000) return `${ms} ms`;
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
    if (ms < 3_600_000) return `${Math.round(ms / 60_000)} min`;
    return `${(ms / 3_600_000).toFixed(1)} h`;
  } catch { return "—"; }
}

function statusColor(s: Run["status"]): string {
  if (s === "success") return "bg-success";
  if (s === "failed")  return "bg-error";
  return "bg-warn"; // running
}

function scopeLabel(scope: Scope | undefined, scopes: ScopesResponse | null): string {
  if (!scope || scope.kind === "platform") return "Platform";
  const tenant = scopes?.fleet_tenants.find((item) => item.id === scope.id);
  return tenant ? `Fleet: ${tenant.slug}` : `Fleet: ${scope.id || "tenant"}`;
}

export default function BackupPanel({ projectId, installId }: NativePanelProps) {
  const [destinations, setDestinations] = useState<Destination[]>([]);
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [scopes, setScopes] = useState<ScopesResponse | null>(null);
  const [selectedScope, setSelectedScope] = useState("platform");
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [runLimit, setRunLimit] = useState(50);
  const [hasMoreRuns, setHasMoreRuns] = useState(false);
  const [showAllScopes, setShowAllScopes] = useState(false);
  const reloadSeq = useRef(0);

  const withParams = useCallback((extra: Record<string, string> = {}) => {
    const u = new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra });
    return u.toString();
  }, [projectId, installId]);

  const api = useCallback(async <T,>(method: string, path: string, body?: unknown): Promise<T> => {
    const separator = path.includes("?") ? "&" : "?";
    const res = await fetch(`${API}${path}${separator}${withParams()}`, {
      method,
      credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) {
      const raw = await res.text().catch(() => "");
      let detail = raw;
      try {
        const parsed = JSON.parse(raw) as { error?: string };
        detail = parsed.error || raw;
      } catch { /* keep response text */ }
      throw new Error(detail ? `${res.status}: ${detail}` : String(res.status));
    }
    return res.json();
  }, [withParams]);

  const reload = useCallback(async () => {
    const seq = ++reloadSeq.current;
    try {
      const [d, p, r, s] = await Promise.all([
        api<{ destinations: Destination[] }>("GET", "/destinations"),
        api<{ policies: Policy[] }>("GET", "/policies"),
        api<{ runs: Run[]; has_more?: boolean }>("GET", `/runs?limit=${runLimit}`),
        api<ScopesResponse>("GET", "/scopes"),
      ]);
      if (seq !== reloadSeq.current) return;
      setDestinations(d.destinations || []);
      setPolicies(p.policies || []);
      setRuns(r.runs || []);
      setHasMoreRuns(Boolean(r.has_more));
      setScopes(s);
      setStatus("");
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  }, [api, runLimit]);

  const reloadRuns = useCallback(async () => {
    try {
      const result = await api<{ runs: Run[]; has_more?: boolean }>("GET", `/runs?limit=${runLimit}`);
      setRuns(result.runs || []);
      setHasMoreRuns(Boolean(result.has_more));
    } catch (e) {
      setStatus("Error refreshing backup history: " + (e as Error).message);
    }
  }, [api, runLimit]);

  useEffect(() => { reload(); }, [reload]);
  useAppEvents("backup", projectId, () => reloadRuns());
  useEffect(() => {
    const timer = window.setInterval(reloadRuns, 15_000);
    return () => window.clearInterval(timer);
  }, [reloadRuns]);

  useEffect(() => {
    const timer = window.setInterval(reload, 5 * 60_000);
    return () => window.clearInterval(timer);
  }, [reload]);

  useEffect(() => {
    if (!selectedScope.startsWith("fleet:") || !scopes) return;
    const tenantID = selectedScope.slice(6);
    if (!scopes.fleet_tenants.some((tenant) => tenant.id === tenantID && tenant.restorable)) {
      setSelectedScope("platform");
    }
  }, [scopes, selectedScope]);

  const scopeForSelection = useCallback((): Scope => {
    if (selectedScope.startsWith("fleet:")) {
      return { kind: "fleet_tenant", id: selectedScope.slice(6), source_app: "fleet" };
    }
    return { kind: "platform" };
  }, [selectedScope]);

  // Status surfaces should be about *current* destinations. A run
  // whose destination was deleted is just history — showing its
  // error confuses the operator into thinking the live setup is
  // broken when it's actually fine. Filter both summaries through
  // the current destination set.
  const liveDestIDs = new Set(destinations.map((d) => d.id));
  const selectedScopeValue = scopeForSelection();
  const liveRuns = runs.filter((r) =>
    liveDestIDs.has(r.destination_id) &&
    r.scope?.kind === selectedScopeValue.kind &&
    (selectedScopeValue.kind === "platform" || r.scope?.id === selectedScopeValue.id),
  );
  const lastSuccess = liveRuns.find(r => r.status === "success");
  const lastRun = liveRuns[0];
  const historyRuns = showAllScopes ? runs : runs.filter((r) =>
    r.scope?.kind === selectedScopeValue.kind &&
    (selectedScopeValue.kind === "platform" || r.scope?.id === selectedScopeValue.id),
  );
  const operationBusy = busy?.startsWith("run-") || busy?.startsWith("restore-");

  // ─── modals (themed; replace window.confirm / window.alert) ────

  // pending = an action awaiting operator confirmation; notice = a
  // dismissable post-action message. Two slots so a notice from a
  // completed restore can show without blocking a fresh confirm.
  const [pending, setPending] = useState<PendingAction | null>(null);
  const [notice, setNotice] = useState<{ title: string; body: string } | null>(null);

  // ─── actions ────────────────────────────────────────────────────

  const runNow = async (destID: number) => {
    setBusy(`run-${destID}`);
    setStatus("");
    try {
      const scope = scopeForSelection();
      await api("POST", "/run", {
        destination_id: destID,
        scope_kind: scope.kind,
        scope_id: scope.id,
        source_app: scope.source_app,
      });
      await reload();
    } catch (e) {
      setStatus("Run failed: " + (e as Error).message);
    } finally { setBusy(null); }
  };

  const doRestore = async (runID: number) => {
    setBusy(`restore-${runID}`);
    try {
      const out = await api<{ report: {
        restart_required?: boolean;
        partial_failure?: boolean;
        failures?: string[];
        failure_count?: number;
      } }>("POST", "/restore", { run_id: runID });
      const restart = out?.report?.restart_required;
      const run = runs.find((item) => item.id === runID);
      const fleetTenant = run?.scope?.kind === "fleet_tenant";
      setNotice({
        title: out?.report?.partial_failure ? "Restore partially completed" : "Restore complete",
        body: out?.report?.partial_failure
          ? `${out.report.failure_count || out.report.failures?.length || 0} item(s) could not be restored: ${(out.report.failures || []).join("; ")}`
          : fleetTenant
          ? `${scopeLabel(run?.scope, scopes)} was restored and restarted if it was previously running.`
          : restart
          ? "App databases were swapped live. Restart apteva-server to activate the platform DB swap."
          : "Platform app databases were restored.",
      });
      await reload();
    } catch (e) {
      setStatus("Restore failed: " + (e as Error).message);
    } finally { setBusy(null); }
  };

  const testDestination = async (id: number) => {
    setBusy(`test-${id}`);
    setStatus("");
    try {
      await api("POST", `/destinations/${id}/test`);
      setNotice({ title: "Destination healthy", body: "The destination is reachable and passed its access check." });
    } catch (e) {
      setStatus("Destination check failed: " + (e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const doDeleteDestination = async (id: number) => {
    try { await api("DELETE", `/destinations/${id}`); await reload(); }
    catch (e) { setStatus("Delete failed: " + (e as Error).message); }
  };

  const doDeletePolicy = async (id: number) => {
    try { await api("DELETE", `/policies/${id}`); await reload(); }
    catch (e) { setStatus("Delete failed: " + (e as Error).message); }
  };

  const onConfirm = async () => {
    if (!pending) return;
    const p = pending;
    setPending(null);
    if (p.kind === "restore") return doRestore(p.runID);
    if (p.kind === "delete-destination") return doDeleteDestination(p.id);
    if (p.kind === "delete-policy") return doDeletePolicy(p.id);
  };

  // ─── render ─────────────────────────────────────────────────────

  return (
    <div className="h-full flex flex-col p-6 gap-4 min-w-0 overflow-y-auto">
      <header>
        <h2 className="text-text text-base font-bold">Backup</h2>
        <p className="text-text-muted text-xs mt-1">
            Verified database snapshots for the platform or one local Fleet tenant.
        </p>
      </header>

      <section className="border-y border-border py-3 flex flex-wrap items-center gap-3">
        <label className="text-text-muted text-xs" htmlFor="backup-scope">Backup scope</label>
        <select
          id="backup-scope"
          value={selectedScope}
          onChange={(e) => setSelectedScope(e.target.value)}
          className="bg-bg border border-border rounded px-2 py-1.5 text-sm text-text min-w-52"
        >
          <option value="platform">Platform databases</option>
          {(scopes?.fleet_tenants || []).filter((t) => t.restorable).map((tenant) => (
            <option key={tenant.id} value={`fleet:${tenant.id}`}>Fleet: {tenant.slug}</option>
          ))}
        </select>
        <Pill>{scopes?.encryption_enabled ? "encrypted" : "not encrypted"}</Pill>
        {scopes && !scopes.fleet_bound && (
          <span className="text-text-muted text-xs">Bind Fleet to enable per-tenant backup.</span>
        )}
        {scopes?.fleet_error && (
          <span className="text-error text-xs">Fleet unavailable: {scopes.fleet_error}</span>
        )}
      </section>

      {selectedScope === "platform" && scopes?.platform && (
        <div className="text-text-muted text-xs border-l-2 border-warn pl-3">
          Includes {scopes.platform.coverage}. Excludes {scopes.platform.gaps.join(", ")}.
        </div>
      )}

      {destinations.some((destination) => destination.kind === "local") && (
        <div className="text-warn text-xs border border-warn/40 bg-warn/10 rounded px-3 py-2">
          Local destinations stay on this host. Keep an off-host destination for disaster recovery.
        </div>
      )}

      {status && (
        <div className="text-error text-xs border border-error/40 bg-error/10 rounded px-3 py-2">
          {status}
        </div>
      )}

      <section className="border-b border-border pb-4 space-y-2">
        <h3 className="text-text text-sm font-bold">Status</h3>
        {lastSuccess ? (
          <div className="text-text-muted text-sm">
            Last successful backup:{" "}
            <span className="text-text font-bold">{formatTime(lastSuccess.finished_at)}</span>{" "}
            to <span className="text-text font-bold">{lastSuccess.destination_name}</span>{" "}
            ({formatBytes(lastSuccess.bytes_compressed)})
          </div>
        ) : (
          <div className="text-text-muted text-sm italic">No successful backups yet.</div>
        )}
        {lastRun && lastRun.status !== "success" && (
          <div className="text-error text-xs">
            Last run: {lastRun.status}
            {lastRun.error ? ` — ${lastRun.error}` : ""}
          </div>
        )}
      </section>

      {/* Destinations */}
      <section className="border-b border-border pb-4 space-y-2">
        <h3 className="text-text text-sm font-bold">Destinations</h3>
        {destinations.length === 0 && (
          <div className="text-text-muted text-sm italic">No destinations yet — add one below.</div>
        )}
        {destinations.map(d => (
          <Row key={d.id}>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <strong className="text-text">{d.name}</strong>
                <Pill>{d.kind}</Pill>
                {d.kind === "s3" && d.connection_id ? <Pill>connection {d.connection_id}</Pill> : null}
              </div>
              <div className="text-text-muted text-xs mt-0.5 font-mono truncate">
                {d.kind === "local" && ((d.config.path as string) || "Install data directory / backups")}
                {d.kind === "s3" && `s3://${d.config.bucket}${d.config.key_prefix ? "/" + d.config.key_prefix : ""}`}
              </div>
            </div>
            <div className="flex items-center gap-2 shrink-0">
              <button
                onClick={() => runNow(d.id)}
                disabled={Boolean(operationBusy || busy?.startsWith("test-"))}
                className="px-3 py-1 text-xs bg-accent text-bg rounded font-bold hover:bg-accent-hover disabled:opacity-50"
              >
                {busy === `run-${d.id}` ? "Running…" : "Run now"}
              </button>
              <button
                onClick={() => testDestination(d.id)}
                disabled={Boolean(busy)}
                className="px-2 py-1 text-xs border border-border text-text-muted rounded hover:bg-bg-hover hover:text-text disabled:opacity-50"
              >
                {busy === `test-${d.id}` ? "Testing…" : "Test"}
              </button>
              <button
                onClick={() => setPending({ kind: "delete-destination", id: d.id, name: d.name })}
                disabled={Boolean(busy)}
                className="px-2 py-1 text-xs border border-border text-text-muted rounded hover:bg-bg-hover hover:text-text"
              >
                Delete
              </button>
            </div>
          </Row>
        ))}
        <DestinationForm destinations={destinations} onCreated={reload} api={api} installId={installId} />
      </section>

      {/* Policies */}
      <section className="border-b border-border pb-4 space-y-2">
        <h3 className="text-text text-sm font-bold">Policies</h3>
        {policies.length === 0 && (
          <div className="text-text-muted text-sm italic">No scheduled policies — add one to back up automatically.</div>
        )}
        {policies.map(p => (
          <Row key={p.id}>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 flex-wrap">
                <strong className="text-text">{p.name || `policy ${p.id}`}</strong>
                <code className="text-text-muted text-xs font-mono bg-bg-input px-1.5 py-0.5 rounded">{p.schedule}</code>
                <Pill>{scopeLabel(p.scope, scopes)}</Pill>
              </div>
              <div className="text-text-muted text-xs mt-0.5">
                → {destinations.find(d => d.id === p.destination_id)?.name || `destination ${p.destination_id}`}
                {" · keep last "}{p.retention_keep}
                {p.jobs_id ? ` · jobs#${p.jobs_id}` : (
                  <span className="text-warn"> · not scheduled</span>
                )}
              </div>
            </div>
            <button
              onClick={() => setPending({ kind: "delete-policy", id: p.id, name: p.name || `policy ${p.id}` })}
              disabled={Boolean(busy)}
              className="px-2 py-1 text-xs border border-border text-text-muted rounded hover:bg-bg-hover hover:text-text shrink-0"
            >
              Delete
            </button>
          </Row>
        ))}
        <PolicyForm destinations={destinations} scopes={scopes} onCreated={reload} api={api} />
      </section>

      {/* History */}
      <section className="pb-4 space-y-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-text text-sm font-bold">History</h3>
          <label className="text-text-muted text-xs flex items-center gap-1.5">
            <input
              type="checkbox"
              checked={showAllScopes}
              onChange={(event) => setShowAllScopes(event.target.checked)}
            />
            Show all scopes
          </label>
        </div>
        {historyRuns.length === 0 && (
          <div className="text-text-muted text-sm italic">No backup runs yet.</div>
        )}
        {historyRuns.map(r => (
          <Row key={r.id}>
            <div className="min-w-0 flex-1 flex items-start gap-2">
              <span className={`inline-block w-2 h-2 rounded-full mt-1.5 shrink-0 ${statusColor(r.status)}`} />
              <div className="min-w-0 flex-1">
                <div className="text-sm text-text-muted">
                  {formatTime(r.started_at)} →{" "}
                  <span className="text-text font-bold">{r.destination_name}</span>
                  {" · "}{formatBytes(r.bytes_compressed)}
                  {" · "}{durationOf(r)}
                  {" · "}<span className="text-text">{scopeLabel(r.scope, scopes)}</span>
                  {r.encrypted ? " · encrypted" : ""}
                  {r.status === "running" && r.stage ? ` · ${r.stage}` : ""}
                </div>
                {r.error && (
                  <div className="text-error text-xs mt-0.5 break-words">{r.error}</div>
                )}
              </div>
            </div>
            {r.status === "success" && r.remote_key && (
              <button
                onClick={() => setPending({ kind: "restore", runID: r.id, destName: r.destination_name, scope: r.scope, encrypted: r.encrypted })}
                disabled={Boolean(operationBusy || busy?.startsWith("test-"))}
                className="px-2 py-1 text-xs border border-border text-text-muted rounded hover:bg-bg-hover hover:text-text disabled:opacity-50 shrink-0"
              >
                {busy === `restore-${r.id}` ? "Restoring…" : "Restore"}
              </button>
            )}
          </Row>
        ))}
        {hasMoreRuns && runLimit < 500 && (
          <button
            type="button"
            onClick={() => setRunLimit((value) => Math.min(500, value + 50))}
            className="text-accent text-xs hover:underline"
          >
            Load 50 more
          </button>
        )}
      </section>

      {/* Themed modals — replace window.confirm/alert which look
          out of place against the dashboard chrome. */}
      <ConfirmModal
        pending={pending}
        onCancel={() => setPending(null)}
        onConfirm={onConfirm}
      />
      <NoticeModal notice={notice} onClose={() => setNotice(null)} />
    </div>
  );
}

// ─── modal + confirm types ────────────────────────────────────────

type PendingAction =
  | { kind: "restore"; runID: number; destName: string; scope: Scope; encrypted: boolean }
  | { kind: "delete-destination"; id: number; name: string }
  | { kind: "delete-policy"; id: number; name: string };

function ConfirmModal({
  pending, onCancel, onConfirm,
}: { pending: PendingAction | null; onCancel: () => void; onConfirm: () => void }) {
  if (!pending) return null;

  let title = "";
  let body: React.ReactNode = null;
  let confirmLabel = "Confirm";
  let danger = false;

  if (pending.kind === "restore") {
    title = "Restore from this backup?";
    confirmLabel = "Restore";
    danger = true;
    const fleetTenant = pending.scope?.kind === "fleet_tenant";
    body = (
      <>
        <div>
          Restore the backup taken on{" "}
          <span className="text-text font-bold">{pending.destName}</span> (run #{pending.runID}).
        </div>
        <ul className="list-disc pl-5 mt-2 space-y-1 text-text-muted">
          {fleetTenant ? (
            <li>Only Fleet tenant {pending.scope.id} will be replaced and restarted if it is currently running.</li>
          ) : (
            <>
              <li>App databases will be replaced live after integrity verification.</li>
              <li>The platform DB will be staged and applied on the next server restart.</li>
            </>
          )}
          <li>{pending.encrypted ? "The stored object is encrypted." : "The stored object is not encrypted."}</li>
          <li>This is destructive and cannot be undone.</li>
        </ul>
      </>
    );
  } else if (pending.kind === "delete-destination") {
    title = "Delete destination?";
    confirmLabel = "Delete";
    danger = true;
    body = (
      <>
        Delete <span className="text-text font-bold">{pending.name}</span>?
        Delete its policies first. Restore history keeps the destination configuration privately.
      </>
    );
  } else if (pending.kind === "delete-policy") {
    title = "Delete policy?";
    confirmLabel = "Delete";
    danger = true;
    body = (
      <>
        Delete <span className="text-text font-bold">{pending.name}</span>?
        Existing backups remain untouched.
      </>
    );
  }

  return (
    <ModalShell title={title} onClose={onCancel}>
      <div className="text-text-muted text-sm">{body}</div>
      <div className="flex justify-end gap-2 pt-1">
        <button
          onClick={onCancel}
          className="px-3 py-1.5 text-sm text-text-muted hover:text-text"
        >
          Cancel
        </button>
        <button
          onClick={onConfirm}
          className={`px-3 py-1.5 text-sm rounded font-bold ${
            danger
              ? "bg-error text-bg hover:opacity-90"
              : "bg-accent text-bg hover:bg-accent-hover"
          }`}
        >
          {confirmLabel}
        </button>
      </div>
    </ModalShell>
  );
}

function NoticeModal({
  notice, onClose,
}: { notice: { title: string; body: string } | null; onClose: () => void }) {
  if (!notice) return null;
  return (
    <ModalShell title={notice.title} onClose={onClose}>
      <div className="text-text-muted text-sm">{notice.body}</div>
      <div className="flex justify-end gap-2 pt-1">
        <button
          onClick={onClose}
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold hover:bg-accent-hover"
        >
          OK
        </button>
      </div>
    </ModalShell>
  );
}

// ModalShell — fixed-positioned overlay + centered card. Uses the
// dashboard's z-50 layer so it sits above the panel chrome but below
// any global toast layer. Esc closes; clicking the backdrop closes.
function ModalShell({
  title, children, onClose,
}: { title: string; children: React.ReactNode; onClose: () => void }) {
  const titleId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null;
    const focusableSelector = "button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href]";
    const focusable = () => Array.from(panelRef.current?.querySelectorAll<HTMLElement>(focusableSelector) || []);
    focusable()[0]?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
        return;
      }
      if (e.key !== "Tab") return;
      const elements = focusable();
      if (elements.length === 0) return;
      const first = elements[0];
      const last = elements[elements.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      previouslyFocused?.focus();
    };
  }, [onClose]);
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-bg-overlay"
      onClick={onClose}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="bg-bg-card border border-border rounded-lg shadow-popover w-full max-w-md p-5 space-y-3"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 id={titleId} className="text-text text-base font-bold">{title}</h3>
        {children}
      </div>
    </div>
  );
}

// ─── tiny presentational helpers ──────────────────────────────────

function Row({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex flex-col items-stretch sm:flex-row sm:items-center sm:justify-between gap-3 py-2 border-b border-border last:border-b-0">
      {children}
    </div>
  );
}

function Pill({ children }: { children: React.ReactNode }) {
  return (
    <span className="text-[10px] uppercase tracking-wide bg-bg-input text-text-muted px-1.5 py-0.5 rounded">
      {children}
    </span>
  );
}

// ─── forms ────────────────────────────────────────────────────────

// Connection — minimal shape we need from /api/connections.
interface Connection {
  id: number;
  name: string;
  app_slug: string;
  status: string;
}

// compatible_slugs for the cloud_storage role; mirrors apteva.yaml.
const CLOUD_STORAGE_SLUGS = ["aws-s3", "cloudflare-r2"];

function DestinationForm({
  destinations, onCreated, api, installId,
}: {
  destinations: Destination[];
  onCreated: () => void;
  api: <T>(method: string, path: string, body?: unknown) => Promise<T>;
  installId: number;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"local" | "s3">("local");
  const [path, setPath] = useState("");
  const [bucket, setBucket] = useState("");
  const [keyPrefix, setKeyPrefix] = useState("");
  const [connections, setConnections] = useState<Connection[] | null>(null);
  const [connID, setConnID] = useState<number | "">("");
  const [err, setErr] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const existingCloudConnection = destinations.find(
    (destination) => destination.kind === "s3" && destination.connection_id,
  )?.connection_id;

  // Lazy-load operator's S3-compatible connections the first time the
  // form is opened with kind=s3. /api/connections returns a bare JSON
  // array (not {connections: [...]}) — see handleListConnections in
  // server/connections.go. Filter client-side by app_slug + status.
  useEffect(() => {
    if (kind !== "s3" || connections !== null) return;
    (async () => {
      try {
        const res = await fetch("/api/connections", { credentials: "same-origin" });
        if (!res.ok) throw new Error(`${res.status}`);
        const body = await res.json() as Connection[];
        const list = (Array.isArray(body) ? body : []).filter(
          (c) => CLOUD_STORAGE_SLUGS.includes(c.app_slug) && c.status === "active",
        );
        setConnections(list);
        if (existingCloudConnection) setConnID(existingCloudConnection);
        else if (list.length === 1) setConnID(list[0].id);
      } catch (e) {
        setErr("Couldn't load connections: " + (e as Error).message);
        setConnections([]);
      }
    })();
  }, [kind, connections, existingCloudConnection]);

  const availableConnections = existingCloudConnection
    ? (connections || []).filter((connection) => connection.id === existingCloudConnection)
    : connections;

  if (!open) return (
    <button
      onClick={() => setOpen(true)}
      className="text-accent text-xs hover:underline self-start mt-1"
    >
      + Add destination
    </button>
  );

  const submit = async () => {
    setErr("");
    if (submitting) return;
    if (kind === "s3") {
      if (!connID) { setErr("Pick a cloud storage connection"); return; }
    }
    setSubmitting(true);
    try {
      if (kind === "s3") {
        // Bind the chosen connection to the cloud_storage role on this
        // install. Idempotent — same body on every save just refreshes
        // the binding to the picked value.
        const r = await fetch(`/api/apps/installs/${installId}/bindings`, {
          method: "PUT",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ cloud_storage: connID }),
        });
        if (!r.ok) throw new Error(`bind failed: ${r.status} ${await r.text().catch(() => "")}`);
      }
      const config = kind === "local"
        ? { path }
        : { bucket, key_prefix: keyPrefix };
      await api("POST", "/destinations", {
        name, kind, config, connection_id: kind === "s3" ? connID : undefined, enabled: true,
      });
      setOpen(false); setName(""); setPath(""); setBucket(""); setKeyPrefix("");
      onCreated();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="mt-2 pt-3 border-t border-border space-y-2">
      <div className="text-text font-bold text-sm">New destination</div>
      <FormGrid>
        <Label htmlFor="backup-destination-name">Name</Label>
        <Input id="backup-destination-name" value={name} onChange={setName} placeholder="nightly-r2" />

        <Label htmlFor="backup-destination-kind">Kind</Label>
        <Select id="backup-destination-kind" value={kind} onChange={(v) => setKind(v as "local" | "s3")}>
          <option value="local">local — host directory</option>
          <option value="s3">Cloudflare R2 or AWS S3</option>
        </Select>

        {kind === "local" && <>
          <Label htmlFor="backup-destination-path">Path</Label>
          <Input
            id="backup-destination-path"
            value={path}
            onChange={setPath}
            placeholder="leave blank to use the install's data dir"
          />
        </>}

        {kind === "s3" && <>
          <Label htmlFor="backup-destination-connection">Connection</Label>
          {connections === null ? (
            <div className="text-text-muted text-xs italic py-1.5">Loading your connections…</div>
          ) : availableConnections?.length === 0 ? (
            <div className="text-text-muted text-xs">
              {existingCloudConnection
                ? `Existing cloud destinations require connection ${existingCloudConnection}, but it is not active.`
                : <>No compatible connections found. Create one in the{" "}
                    <a href="/integrations" className="text-accent hover:underline">Integrations</a>{" "}
                    tab using Cloudflare R2 or AWS S3.</>}
            </div>
          ) : (
            <Select
              id="backup-destination-connection"
              value={connID === "" ? "" : String(connID)}
              onChange={(v) => setConnID(Number(v))}
            >
              <option value="">Pick a connection…</option>
              {availableConnections?.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name} · {c.app_slug}
                </option>
              ))}
            </Select>
          )}
          <Label htmlFor="backup-destination-bucket">Bucket</Label>
          <Input id="backup-destination-bucket" value={bucket} onChange={setBucket} placeholder="apteva-backups" />
          <Label htmlFor="backup-destination-prefix">Key prefix</Label>
          <Input id="backup-destination-prefix" value={keyPrefix} onChange={setKeyPrefix} placeholder="prod/" />
        </>}
      </FormGrid>
      {kind === "s3" && availableConnections && availableConnections.length > 0 && (
        <div className="text-text-muted text-xs">
          {existingCloudConnection
            ? `Cloud destinations on this install share connection ${existingCloudConnection}. Use different buckets or prefixes within that account.`
            : "Cloud destinations on this install share one bound account. Use different buckets or prefixes within it."}
          {" "}Credentials stay in the platform.
        </div>
      )}
      {err && <div className="text-error text-xs">{err}</div>}
      <div className="flex justify-end gap-2 pt-1">
        <button
          onClick={() => setOpen(false)}
          className="px-3 py-1.5 text-sm text-text-muted hover:text-text"
        >
          Cancel
        </button>
        <button
          onClick={submit}
          disabled={
            submitting ||
            !name ||
            (kind === "s3" && (!connID || !bucket))
          }
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold hover:bg-accent-hover disabled:opacity-50"
        >
          {submitting ? "Checking…" : "Create"}
        </button>
      </div>
    </div>
  );
}

function PolicyForm({
  destinations, scopes, onCreated, api,
}: {
  destinations: Destination[];
  scopes: ScopesResponse | null;
  onCreated: () => void;
  api: <T>(method: string, path: string, body?: unknown) => Promise<T>;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("nightly");
  const [schedule, setSchedule] = useState("0 3 * * *");
  const [destID, setDestID] = useState<number | "">(destinations[0]?.id ?? "");
  const [keep, setKeep] = useState("");
  const [scopeKey, setScopeKey] = useState("platform");
  const [err, setErr] = useState("");
  const [submitting, setSubmitting] = useState(false);

  // The form is mounted alongside the parent; if destinations was empty
  // at first render and the user added one before opening the form,
  // useState's initializer (which runs only once) already locked destID
  // at "". Reconcile when destinations populate.
  useEffect(() => {
    if (destID === "" && destinations.length > 0) {
      setDestID(destinations[0].id);
    }
  }, [destinations, destID]);

  useEffect(() => {
    if (keep === "" && scopes) setKeep(String(scopes.default_retention));
  }, [scopes, keep]);

  if (!open) return (
    <button
      onClick={() => setOpen(true)}
      disabled={destinations.length === 0}
      title={destinations.length === 0 ? "Add a destination first" : ""}
      className="text-accent text-xs hover:underline self-start mt-1 disabled:opacity-50 disabled:no-underline"
    >
      + Add policy
    </button>
  );

  const submit = async () => {
    setErr("");
    if (submitting) return;
    if (!name.trim()) { setErr("Name is required"); return; }
    if (!schedule.trim()) { setErr("Schedule is required"); return; }
    if (!destID) { setErr("Pick a destination"); return; }
    const retention = Number(keep);
    if (!Number.isInteger(retention) || retention < 0) { setErr("Retention must be a whole number of 0 or greater"); return; }
    const scope: Scope = scopeKey.startsWith("fleet:")
      ? { kind: "fleet_tenant", id: scopeKey.slice(6), source_app: "fleet" }
      : { kind: "platform" };
    setSubmitting(true);
    try {
      await api("POST", "/policies", {
        name: name.trim(), schedule: schedule.trim(), destination_id: destID, retention_keep: retention, scope,
      });
      setOpen(false);
      onCreated();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="mt-2 pt-3 border-t border-border space-y-2">
      <div className="text-text font-bold text-sm">New policy</div>
      <FormGrid>
        <Label htmlFor="backup-policy-name">Name</Label>
        <Input id="backup-policy-name" value={name} onChange={setName} />
        <Label htmlFor="backup-policy-schedule">Schedule (cron)</Label>
        <Input id="backup-policy-schedule" value={schedule} onChange={setSchedule} placeholder="0 3 * * *" />
        <Label htmlFor="backup-policy-destination">Destination</Label>
        <Select id="backup-policy-destination" value={String(destID)} onChange={(v) => setDestID(Number(v))}>
          {destinations.map(d => <option key={d.id} value={d.id}>{d.name}</option>)}
        </Select>
        <Label htmlFor="backup-policy-scope">Scope</Label>
        <Select id="backup-policy-scope" value={scopeKey} onChange={setScopeKey}>
          <option value="platform">Platform databases</option>
          {(scopes?.fleet_tenants || []).filter((tenant) => tenant.restorable).map((tenant) => (
            <option key={tenant.id} value={`fleet:${tenant.id}`}>Fleet: {tenant.slug}</option>
          ))}
        </Select>
        <Label htmlFor="backup-policy-retention">Retention (last N)</Label>
        <Input id="backup-policy-retention" value={keep} onChange={setKeep} placeholder="0 keeps all backups" />
      </FormGrid>
      {err && <div className="text-error text-xs">{err}</div>}
      <div className="flex justify-end gap-2 pt-1">
        <button
          onClick={() => setOpen(false)}
          className="px-3 py-1.5 text-sm text-text-muted hover:text-text"
        >
          Cancel
        </button>
        <button
          onClick={submit}
          disabled={submitting || !name.trim() || !schedule.trim()}
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold hover:bg-accent-hover"
        >
          {submitting ? "Creating…" : "Create"}
        </button>
      </div>
    </div>
  );
}

function FormGrid({ children }: { children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-1 sm:grid-cols-[120px_minmax(0,1fr)] gap-2 items-center">
      {children}
    </div>
  );
}

function Label({ children, htmlFor }: { children: React.ReactNode; htmlFor?: string }) {
  return <label htmlFor={htmlFor} className="text-text-muted text-xs">{children}</label>;
}

function Input({
  id, value, onChange, placeholder,
}: { id?: string; value: string; onChange: (v: string) => void; placeholder?: string }) {
  return (
    <input
      id={id}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder}
      className="bg-bg border border-border rounded px-2 py-1.5 text-sm text-text font-mono focus:outline-none focus:border-accent w-full min-w-0"
    />
  );
}

function Select({
  id, value, onChange, children,
}: { id?: string; value: string; onChange: (v: string) => void; children: React.ReactNode }) {
  return (
    <select
      id={id}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="bg-bg border border-border rounded px-2 py-1.5 text-sm text-text w-full min-w-0"
    >
      {children}
    </select>
  );
}
