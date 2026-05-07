// DeployPanel — control surface for the deploy app. Two-pane:
// left = deployment list + create button, right = detail (status,
// build/release history, env editor, log tail). Mirrors the Code
// panel's interaction model so users moving between them feel
// consistent.

import { useCallback, useEffect, useRef, useState } from "react";

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

interface Deployment {
  id: number;
  name: string;
  description?: string;
  source_kind: string;
  source_ref: string;
  framework: string;
  build_cmd: string;
  start_cmd: string;
  port_hint: number;
  env_json: string;
  domain: string;
  domain_record_id?: string;
  domain_attached_at?: string;
  current_release_id?: number | null;
  archived_at?: string;
  created_at: string;
  updated_at: string;
}

interface CertStatus {
  status: "pending" | "issuing" | "live" | "failed" | "revoked";
  expires_at?: string;
  error?: string;
}
interface MetaInfo {
  domains_available: boolean;
  certs_available: boolean;
  domains: { name: string }[];
  public_host: string;
  certs: Record<string, CertStatus>;
}

interface Build {
  id: number;
  deployment_id: number;
  source_sha: string;
  framework: string;
  status: "pending" | "running" | "succeeded" | "failed" | "cancelled";
  duration_ms: number;
  exit_code: number;
  artifact_path: string;
  artifact_size: number;
  log_path: string;
  error: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

interface Release {
  id: number;
  deployment_id: number;
  build_id: number;
  status: "starting" | "live" | "stopped" | "crashed" | "failed";
  port: number;
  pid: number;
  started_at?: string;
  stopped_at?: string;
  restart_count: number;
  log_path: string;
  error: string;
  created_at: string;
}

interface DeploymentDetail {
  deployment: Deployment;
  builds: Build[];
  releases: Release[];
  current_release: Release | null;
  url: string;
}

const API = "/api/apps/deploy/api";

const FRAMEWORKS = ["", "go", "node", "bun", "static", "blank"] as const;
const SOURCE_KINDS = ["code", "local"] as const;

function statusColor(s: string): string {
  if (s === "live" || s === "succeeded") return "text-green";
  if (s === "running" || s === "starting" || s === "pending") return "text-blue";
  if (s === "stopped") return "text-text-dim";
  return "text-red";
}

function formatSize(n: number): string {
  if (!n) return "—";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

function formatDuration(ms: number): string {
  if (!ms) return "—";
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

export default function DeployPanel({ projectId, installId }: NativePanelProps) {
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [detail, setDetail] = useState<DeploymentDetail | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [showCreate, setShowCreate] = useState(false);
  const [logs, setLogs] = useState("");
  const [logKind, setLogKind] = useState<"build" | "release">("release");
  const [logTargetId, setLogTargetId] = useState<number | null>(null);
  const [meta, setMeta] = useState<MetaInfo | null>(null);
  const [showAttachDomain, setShowAttachDomain] = useState(false);

  const withParams = useCallback(
    (extra: Record<string, string> = {}) =>
      new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      }).toString(),
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

  const apiText = useCallback(
    async (path: string, extra: Record<string, string> = {}): Promise<string> => {
      const res = await fetch(`${API}${path}?${withParams(extra)}`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.text();
    },
    [withParams],
  );

  const loadDeployments = useCallback(async () => {
    try {
      const r = await api<{ deployments?: Deployment[] }>("GET", "/deployments");
      setDeployments(r.deployments || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }, [api]);

  const loadDetail = useCallback(
    async (id: number) => {
      try {
        const d = await api<DeploymentDetail>("GET", `/deployments/${id}`);
        setDetail(d);
        // Always re-anchor the log pane to THIS deployment, so
        // switching from a deployment with a live release to a
        // deployment without one doesn't leave the previous one's
        // log target stuck in view. Preference: live release > latest
        // build > nothing.
        if (d.current_release) {
          setLogKind("release");
          setLogTargetId(d.current_release.id);
        } else if (d.builds && d.builds[0]) {
          setLogKind("build");
          setLogTargetId(d.builds[0].id);
        } else {
          setLogTargetId(null);
        }
      } catch (e) {
        setError((e as Error).message);
      }
    },
    [api],
  );

  useEffect(() => { loadDeployments(); }, [loadDeployments]);

  // Capabilities: whether the optional Domains app is installed +
  // the registered domains for the picker. Cheap one-shot per mount.
  useEffect(() => {
    let cancelled = false;
    api<MetaInfo>("GET", "/_meta")
      .then((m) => { if (!cancelled) setMeta(m); })
      .catch(() => { if (!cancelled) setMeta({ domains_available: false, domains: [], public_host: "" }); });
    return () => { cancelled = true; };
  }, [api]);

  // Refresh on relevant events.
  useAppEvents<{ deployment_id?: number; build_id?: number; release_id?: number }>("deploy", projectId, (ev) => {
    if (ev.topic.startsWith("deploy.")) {
      loadDeployments();
      if (selected != null && ev.data?.deployment_id === selected) {
        loadDetail(selected);
      }
    }
  });

  // Auto-tail logs every 2s when there's an active build or live release.
  useEffect(() => {
    if (logTargetId == null) return;
    let alive = true;
    const tick = async () => {
      try {
        const txt = await apiText(`/${logKind === "build" ? "builds" : "releases"}/${logTargetId}/log`, { tail: "300" });
        if (alive) setLogs(txt);
      } catch {/* swallow — endpoint may 404 briefly */}
    };
    tick();
    const handle = window.setInterval(tick, 2000);
    return () => { alive = false; window.clearInterval(handle); };
  }, [logKind, logTargetId, apiText]);

  const selectDeployment = (id: number) => {
    // Clear deployment-scoped state synchronously BEFORE the async
    // fetch so the panel renders empty during the load window
    // instead of the previously-selected deployment's data. Without
    // this, switching from a deployment with a live release to
    // another briefly shows the previous deployment's logs / build
    // card / status — visibly confusing for ~1s.
    setSelected(id);
    setDetail(null);
    setLogs("");
    setLogTargetId(null);
    setLogKind("release");
    loadDetail(id);
  };

  const handleBuild = async (release: boolean) => {
    if (!detail) return;
    setBusy(true);
    try {
      const r = await api<{ build: Build; release?: Release; release_error?: string }>(
        "POST", `/deployments/${detail.deployment.id}/build`, { release },
      );
      // Switch log target to the freshly-created build first.
      setLogKind("build");
      setLogTargetId(r.build.id);
      if (r.release_error) setError("Release: " + r.release_error);
    } catch (e) {
      setError("Build failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const handleReleaseBuild = async (buildId: number) => {
    if (!detail) return;
    try {
      const r = await api<{ release: Release }>(
        "POST", `/deployments/${detail.deployment.id}/release`, { build_id: buildId },
      );
      setLogKind("release");
      setLogTargetId(r.release.id);
    } catch (e) {
      setError("Release failed: " + (e as Error).message);
    }
  };

  const [confirmState, setConfirmState] = useState<ConfirmRequest | null>(null);

  const handleStop = () => {
    if (!detail) return;
    setConfirmState({
      title: "Stop release",
      body: "Stop the live release? The supervised process will be terminated.",
      confirmLabel: "Stop",
      tone: "warning",
      onConfirm: async () => {
        try {
          await api("POST", `/deployments/${detail.deployment.id}/stop`);
        } catch (e) {
          setError("Stop failed: " + (e as Error).message);
        }
      },
    });
  };

  const handleDetachDomain = () => {
    if (!detail) return;
    setConfirmState({
      title: "Detach domain",
      body: `Remove the domain "${detail.deployment.domain}" from this deployment? The DNS record will be deleted via the Domains app.`,
      confirmLabel: "Detach",
      tone: "warning",
      onConfirm: async () => {
        try {
          await api("POST", `/deployments/${detail.deployment.id}/detach-domain`);
          loadDetail(detail.deployment.id);
        } catch (e) {
          setError("Detach failed: " + (e as Error).message);
        }
      },
    });
  };

  const handleDestroy = () => {
    if (!detail) return;
    setConfirmState({
      title: "Destroy deployment",
      body: `Destroy deployment "${detail.deployment.name}"? This stops the live release and deletes all builds and artifacts on disk. This can't be undone.`,
      confirmLabel: "Destroy",
      tone: "danger",
      onConfirm: async () => {
        try {
          await api("DELETE", `/deployments/${detail.deployment.id}`);
          setSelected(null);
          setDetail(null);
        } catch (e) {
          setError("Destroy failed: " + (e as Error).message);
        }
      },
    });
  };

  return (
    <div className="h-full flex">
      {/* Deployments list */}
      <aside className="w-72 border-r border-border flex flex-col">
        <div className="p-3 border-b border-border flex items-center gap-2">
          <span className="text-xs uppercase tracking-wide text-text-dim flex-1">deployments</span>
          <button
            type="button"
            onClick={() => setShowCreate(true)}
            className="px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >+ New</button>
        </div>
        <div className="flex-1 overflow-auto">
          {error && <div className="p-3 text-red text-xs">{error}</div>}
          {deployments.length === 0 ? (
            <div className="p-3 text-text-muted text-sm">No deployments yet.</div>
          ) : (
            <ul>
              {deployments.map((d) => (
                <li
                  key={d.id}
                  onClick={() => selectDeployment(d.id)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    d.id === selected ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="flex items-center gap-1">
                    <span className="text-sm text-text font-medium truncate flex-1">{d.name}</span>
                    {d.framework && (
                      <span className="text-[10px] px-1 py-0.5 rounded bg-blue/15 text-blue">
                        {d.framework}
                      </span>
                    )}
                    {d.current_release_id ? (
                      <span className="text-[10px] px-1 py-0.5 rounded bg-green/15 text-green">live</span>
                    ) : (
                      <span className="text-[10px] px-1 py-0.5 rounded bg-border text-text-dim">idle</span>
                    )}
                  </div>
                  <div className="text-xs text-text-muted truncate">
                    {d.source_kind}:{d.source_ref}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </aside>

      {/* Detail */}
      <main className="flex-1 overflow-hidden flex flex-col">
        {!detail ? (
          <div className="p-8 text-text-muted text-sm text-center mt-12">
            {deployments.length === 0
              ? "Click + New to wire up your first deployment."
              : "Pick a deployment on the left."}
          </div>
        ) : (
          <>
            <header className="p-4 border-b border-border flex items-center gap-3 flex-wrap">
              <div className="flex-1 min-w-0">
                <div className="text-text font-semibold text-lg truncate">
                  {detail.deployment.name}
                </div>
                <div className="text-xs text-text-dim truncate">
                  {detail.deployment.source_kind}:{detail.deployment.source_ref}
                  {detail.deployment.framework ? ` · ${detail.deployment.framework}` : ""}
                </div>
              </div>
              {detail.url && (
                <a
                  href={detail.url}
                  target="_blank"
                  rel="noreferrer"
                  className="text-xs text-accent hover:underline truncate max-w-[260px]"
                >{detail.url} ↗</a>
              )}
              {detail.deployment.domain ? (
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={handleDetachDomain}
                    title={detail.deployment.domain_attached_at ? `Attached ${detail.deployment.domain_attached_at}` : "Free-text domain (no DNS managed)"}
                    className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
                  >
                    {detail.deployment.domain_attached_at ? "Detach" : "Clear"} {detail.deployment.domain}
                  </button>
                  {meta?.certs_available && <CertBadge status={meta.certs[detail.deployment.domain]} />}
                </div>
              ) : meta?.domains_available && (
                <button
                  type="button"
                  onClick={() => setShowAttachDomain(true)}
                  className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
                >+ Attach domain</button>
              )}
              <button
                type="button"
                onClick={() => handleBuild(true)}
                disabled={busy}
                className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-40"
              >{busy ? "Building…" : "Build & Release"}</button>
              <button
                type="button"
                onClick={() => handleBuild(false)}
                disabled={busy}
                className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-40"
              >Build only</button>
              {detail.current_release && detail.current_release.status === "live" && (
                <button
                  type="button"
                  onClick={handleStop}
                  className="px-2 py-1 text-xs border border-red text-red/80 rounded hover:bg-red/10"
                >Stop</button>
              )}
              <button
                type="button"
                onClick={handleDestroy}
                className="px-2 py-1 text-xs border border-red text-red/70 rounded hover:bg-red/10"
              >Destroy</button>
            </header>

            <section className="grid grid-cols-2 gap-4 p-4 border-b border-border text-xs">
              <div>
                <div className="text-text-dim uppercase mb-1">Current release</div>
                {detail.current_release ? (
                  <div className="space-y-1">
                    <div>
                      <span className={statusColor(detail.current_release.status) + " font-medium"}>
                        {detail.current_release.status}
                      </span>
                      <span className="text-text-dim"> · port {detail.current_release.port} · pid {detail.current_release.pid}</span>
                    </div>
                    <div className="text-text-dim">
                      build #{detail.current_release.build_id}
                      {detail.current_release.started_at && ` · started ${detail.current_release.started_at}`}
                    </div>
                    {detail.current_release.error && (
                      <div className="text-red truncate" title={detail.current_release.error}>
                        {detail.current_release.error}
                      </div>
                    )}
                  </div>
                ) : (
                  <div className="text-text-muted">No live release.</div>
                )}
              </div>
              <div>
                <div className="text-text-dim uppercase mb-1">Latest build</div>
                {detail.builds[0] ? (
                  <div className="space-y-1">
                    <div className="flex items-baseline gap-2">
                      <span className={statusColor(detail.builds[0].status) + " font-medium"}>
                        #{detail.builds[0].id} {detail.builds[0].status}
                      </span>
                      <span className="text-text-dim">
                        · {formatDuration(detail.builds[0].duration_ms)} · {formatSize(detail.builds[0].artifact_size)}
                      </span>
                      {detail.builds[0].status === "succeeded"
                        && detail.current_release?.build_id !== detail.builds[0].id && (
                        <button
                          type="button"
                          onClick={() => handleReleaseBuild(detail.builds[0].id)}
                          className="ml-auto px-2 py-0.5 text-[11px] border border-accent text-accent rounded hover:bg-accent hover:text-bg"
                          title="Promote this build to a live release"
                        >Release this build →</button>
                      )}
                    </div>
                    <div className="text-text-dim truncate">
                      built as: {detail.builds[0].framework}
                      {detail.builds[0].framework !== detail.deployment.framework
                        && detail.deployment.framework !== ""
                        && (
                          <span className="text-yellow/80" title="Deployment's framework changed since this build — next build will use the deployment's current framework.">
                            {" "}· next build: {detail.deployment.framework}
                          </span>
                        )}
                    </div>
                    {detail.builds[0].error && (
                      <div className="text-red truncate" title={detail.builds[0].error}>
                        {detail.builds[0].error}
                      </div>
                    )}
                  </div>
                ) : (
                  <div className="text-text-muted">No builds yet.</div>
                )}
              </div>
            </section>

            <section className="px-4 py-2 border-b border-border flex items-center gap-2 text-xs">
              <span className="text-text-dim uppercase">logs</span>
              <button
                type="button"
                onClick={() => detail.current_release && (setLogKind("release"), setLogTargetId(detail.current_release!.id))}
                className={`px-2 py-0.5 rounded border ${logKind === "release" ? "border-accent text-accent" : "border-border text-text-dim hover:bg-bg-input"}`}
              >Runtime</button>
              <button
                type="button"
                onClick={() => detail.builds[0] && (setLogKind("build"), setLogTargetId(detail.builds[0].id))}
                className={`px-2 py-0.5 rounded border ${logKind === "build" ? "border-accent text-accent" : "border-border text-text-dim hover:bg-bg-input"}`}
              >Latest build</button>
              <span className="text-text-dim ml-auto">
                {logKind} #{logTargetId ?? "-"}
              </span>
            </section>

            <div className="flex-1 overflow-auto bg-bg">
              <pre className="text-[11px] font-mono p-4 text-text whitespace-pre">
                {logs || (logTargetId == null ? "(no log target — build something)" : "(empty)")}
              </pre>
            </div>

            <section className="border-t border-border p-3 max-h-44 overflow-auto">
              <div className="text-xs text-text-dim uppercase mb-2">Builds</div>
              <table className="w-full text-xs">
                <thead className="text-text-dim">
                  <tr>
                    <th className="text-left font-normal">#</th>
                    <th className="text-left font-normal">Status</th>
                    <th className="text-left font-normal">Duration</th>
                    <th className="text-left font-normal">Size</th>
                    <th className="text-left font-normal">Created</th>
                    <th className="text-right font-normal">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {detail.builds.map((b) => (
                    <tr key={b.id} className="border-t border-border/40">
                      <td className="py-1">{b.id}</td>
                      <td className={statusColor(b.status)}>{b.status}</td>
                      <td>{formatDuration(b.duration_ms)}</td>
                      <td>{formatSize(b.artifact_size)}</td>
                      <td className="text-text-dim truncate">{b.created_at}</td>
                      <td className="text-right space-x-2">
                        <button
                          type="button"
                          onClick={() => { setLogKind("build"); setLogTargetId(b.id); }}
                          className="text-text-dim hover:text-text"
                        >log</button>
                        {b.status === "succeeded" && (
                          <button
                            type="button"
                            onClick={() => handleReleaseBuild(b.id)}
                            className="text-accent hover:underline"
                          >release</button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </section>
          </>
        )}
      </main>

      {showCreate && (
        <CreateDeploymentDialog
          onClose={() => setShowCreate(false)}
          onCreated={(d) => {
            setShowCreate(false);
            loadDeployments().then(() => selectDeployment(d.id));
          }}
          api={api}
          projectId={projectId}
          meta={meta}
        />
      )}

      {showAttachDomain && detail && (
        <AttachDomainDialog
          deployment={detail.deployment}
          meta={meta}
          onClose={() => setShowAttachDomain(false)}
          onAttached={() => {
            setShowAttachDomain(false);
            loadDetail(detail.deployment.id);
          }}
          api={api}
        />
      )}

      {confirmState && (
        <ConfirmDialog
          request={confirmState}
          onClose={() => setConfirmState(null)}
        />
      )}
    </div>
  );
}

// ─── ConfirmDialog ─────────────────────────────────────────────────
//
// Drop-in replacement for window.confirm: a centred modal with title,
// body, and two action buttons. The "danger" tone is used for
// destructive actions (Destroy); "warning" for reversible interrupts
// (Stop). Spawned by setting confirmState — the dialog clears its own
// state through onClose so callers don't have to.

interface ConfirmRequest {
  title: string;
  body: string;
  confirmLabel: string;
  tone?: "warning" | "danger";
  onConfirm: () => void | Promise<void>;
}

function ConfirmDialog({ request, onClose }: { request: ConfirmRequest; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const accent =
    request.tone === "danger"
      ? "bg-red text-white hover:bg-red/90"
      : "bg-blue text-white hover:bg-blue/90";

  const submit = async () => {
    setBusy(true);
    try {
      await request.onConfirm();
      onClose();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-[420px] bg-bg border border-border rounded p-5 space-y-4"
        role="dialog"
        aria-modal="true"
      >
        <h2 className="text-text font-semibold">{request.title}</h2>
        <p className="text-text-muted text-sm">{request.body}</p>
        <div className="flex justify-end gap-2 pt-1">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={busy}
            autoFocus
            className={`px-3 py-1.5 text-sm rounded ${accent} disabled:opacity-50`}
          >
            {busy ? "Working…" : request.confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

interface CodeRepo {
  slug: string;
  name?: string;
  framework?: string;
  archived?: boolean;
}

function CreateDeploymentDialog({
  onClose,
  onCreated,
  api,
  projectId,
  meta,
}: {
  onClose: () => void;
  onCreated: (d: Deployment) => void;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
  projectId: string;
  meta: MetaInfo | null;
}) {
  const [name, setName] = useState("");
  const [sourceKind, setSourceKind] = useState<(typeof SOURCE_KINDS)[number]>("code");
  const [sourceRef, setSourceRef] = useState("");
  const [framework, setFramework] = useState<(typeof FRAMEWORKS)[number]>("");
  const [buildCmd, setBuildCmd] = useState("");
  const [startCmd, setStartCmd] = useState("");
  const [env, setEnv] = useState("");
  const [domainApex, setDomainApex] = useState("");
  const [domainSub, setDomainSub] = useState("");
  const [domainText, setDomainText] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const [repos, setRepos] = useState<CodeRepo[] | null>(null);
  const [reposErr, setReposErr] = useState("");
  useEffect(() => {
    if (sourceKind !== "code") return;
    if (repos !== null) return;
    let cancelled = false;
    fetch(`/api/apps/code/api/repos?project_id=${encodeURIComponent(projectId)}`, {
      credentials: "same-origin",
    })
      .then(async (r) => {
        if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
        return r.json() as Promise<{ repositories?: CodeRepo[] }>;
      })
      .then((j) => {
        if (cancelled) return;
        setRepos((j.repositories || []).filter((r) => !r.archived));
      })
      .catch((e) => {
        if (cancelled) return;
        setReposErr((e as Error).message);
        setRepos([]);
      });
    return () => {
      cancelled = true;
    };
  }, [sourceKind, projectId, repos]);

  const submit = async () => {
    if (!name.trim() || !sourceRef.trim()) {
      setErr("name and source_ref required");
      return;
    }
    setBusy(true);
    try {
      let domain = "";
      if (meta?.domains_available && domainApex) {
        domain = domainSub.trim() ? `${domainSub.trim()}.${domainApex}` : domainApex;
      } else if (!meta?.domains_available && domainText.trim()) {
        domain = domainText.trim();
      }
      const r = await api<{ deployment: Deployment; domain_error?: string }>("POST", "/deployments", {
        name: name.trim(),
        source_kind: sourceKind,
        source_ref: sourceRef.trim(),
        framework,
        build_cmd: buildCmd.trim(),
        start_cmd: startCmd.trim(),
        env_json: env.trim() || "{}",
        domain,
      });
      if (r.domain_error) {
        // Deployment was created; only the DNS step failed. Surface
        // it without aborting so the user can retry attach later.
        setErr("Created, but domain attach failed: " + r.domain_error);
        return;
      }
      onCreated(r.deployment);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => { e.preventDefault(); submit(); }}
        className="w-[480px] bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">New deployment</h2>
        <div className="grid grid-cols-2 gap-3">
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Name (slug)</label>
            <input
              autoFocus
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-api"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Source</label>
            <select
              value={sourceKind}
              onChange={(e) => setSourceKind(e.target.value as (typeof SOURCE_KINDS)[number])}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              {SOURCE_KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
            </select>
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Framework</label>
            <select
              value={framework}
              onChange={(e) => setFramework(e.target.value as (typeof FRAMEWORKS)[number])}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              {FRAMEWORKS.map((f) => (
                <option key={f} value={f}>{f === "" ? "(auto-detect)" : f}</option>
              ))}
            </select>
          </div>
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">
              Source ref ({sourceKind === "code" ? "repository from Code app" : "absolute path on host"})
            </label>
            {sourceKind === "code" ? (
              repos === null ? (
                <div className="w-full text-xs text-text-dim px-2 py-1.5">Loading repositories…</div>
              ) : repos.length === 0 ? (
                <div className="text-xs text-text-dim space-y-1">
                  <div>{reposErr ? `Couldn't load repos: ${reposErr}` : "No repositories in this project yet."}</div>
                  <div>Open the Code panel to create one, then return here.</div>
                </div>
              ) : (
                <select
                  value={sourceRef}
                  onChange={(e) => {
                    setSourceRef(e.target.value);
                    if (framework === "") {
                      const r = repos.find((x) => x.slug === e.target.value);
                      const f = (r?.framework || "").toLowerCase();
                      if ((FRAMEWORKS as readonly string[]).includes(f)) {
                        setFramework(f as (typeof FRAMEWORKS)[number]);
                      }
                    }
                  }}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                >
                  <option value="">— select a repository —</option>
                  {repos.map((r) => (
                    <option key={r.slug} value={r.slug}>
                      {r.slug}
                      {r.framework ? ` · ${r.framework}` : ""}
                      {r.name && r.name !== r.slug ? ` (${r.name})` : ""}
                    </option>
                  ))}
                </select>
              )
            ) : (
              <input
                type="text"
                value={sourceRef}
                onChange={(e) => setSourceRef(e.target.value)}
                placeholder="/abs/path/to/repo"
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
            )}
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Build cmd (optional)</label>
            <input
              type="text"
              value={buildCmd}
              onChange={(e) => setBuildCmd(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Start cmd (optional)</label>
            <input
              type="text"
              value={startCmd}
              onChange={(e) => setStartCmd(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Env (JSON object, optional)</label>
            <textarea
              value={env}
              onChange={(e) => setEnv(e.target.value)}
              placeholder='{"LOG_LEVEL":"info"}'
              rows={2}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">
              Domain (optional)
              {meta?.domains_available
                ? meta.public_host
                  ? <span className="text-text-dim"> · CNAME → {meta.public_host}</span>
                  : <span className="text-yellow"> · public_host not configured</span>
                : <span className="text-text-dim"> · free-text (Domains app not installed)</span>}
            </label>
            {meta?.domains_available ? (
              meta.domains.length === 0 ? (
                <div className="text-xs text-text-dim">No domains registered. Open the Domains panel to add one.</div>
              ) : (
                <div className="flex gap-2">
                  <input
                    type="text"
                    value={domainSub}
                    onChange={(e) => setDomainSub(e.target.value)}
                    placeholder="app (or empty for apex)"
                    className="w-32 bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                  />
                  <span className="self-center text-text-dim">.</span>
                  <select
                    value={domainApex}
                    onChange={(e) => setDomainApex(e.target.value)}
                    className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                  >
                    <option value="">— pick a domain —</option>
                    {meta.domains.map((d) => (
                      <option key={d.name} value={d.name}>{d.name}</option>
                    ))}
                  </select>
                </div>
              )
            ) : (
              <input
                type="text"
                value={domainText}
                onChange={(e) => setDomainText(e.target.value)}
                placeholder="app.acme.com"
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
            )}
          </div>
        </div>
        {err && <div className="text-red text-xs">{err}</div>}
        <div className="flex items-center justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >Cancel</button>
          <button
            type="submit"
            disabled={busy}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Creating…" : "Create"}</button>
        </div>
      </form>
    </div>
  );
}

// ─── AttachDomainDialog ───────────────────────────────────────────
//
// Picker for an existing deployment: choose a registered apex +
// subdomain, optionally override target/type, submit. Surfaces the
// same domain_error path as deploy_init when DNS write fails.

function AttachDomainDialog({
  deployment,
  meta,
  onClose,
  onAttached,
  api,
}: {
  deployment: Deployment;
  meta: MetaInfo | null;
  onClose: () => void;
  onAttached: () => void;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
}) {
  const [apex, setApex] = useState("");
  const [sub, setSub] = useState("");
  const [target, setTarget] = useState("");
  const [recordType, setRecordType] = useState<"CNAME" | "A">("CNAME");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const canSubmit = !!apex && (recordType === "CNAME" ? !!sub : true);

  const submit = async () => {
    if (!canSubmit) {
      setErr(recordType === "CNAME" ? "Apex CNAME isn't allowed; pick a subdomain or switch to A." : "Pick a domain.");
      return;
    }
    setBusy(true);
    try {
      const fqdn = sub.trim() ? `${sub.trim()}.${apex}` : apex;
      await api("POST", `/deployments/${deployment.id}/attach-domain`, {
        fqdn,
        target: target.trim(),
        type: recordType,
      });
      onAttached();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => { e.preventDefault(); submit(); }}
        className="w-[480px] bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">Attach domain</h2>
        {(!meta || meta.domains.length === 0) ? (
          <div className="text-xs text-text-dim">No domains registered. Open the Domains panel to add one first.</div>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="text-xs text-text-muted block mb-1">Type</label>
                <select
                  value={recordType}
                  onChange={(e) => setRecordType(e.target.value as "CNAME" | "A")}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                >
                  <option value="CNAME">CNAME</option>
                  <option value="A">A</option>
                </select>
              </div>
              <div>
                <label className="text-xs text-text-muted block mb-1">Apex domain</label>
                <select
                  value={apex}
                  onChange={(e) => setApex(e.target.value)}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                >
                  <option value="">— pick —</option>
                  {meta.domains.map((d) => (
                    <option key={d.name} value={d.name}>{d.name}</option>
                  ))}
                </select>
              </div>
              <div className="col-span-2">
                <label className="text-xs text-text-muted block mb-1">
                  Subdomain {recordType === "CNAME" && <span className="text-text-dim">(required for CNAME)</span>}
                </label>
                <input
                  type="text"
                  value={sub}
                  onChange={(e) => setSub(e.target.value)}
                  placeholder="app"
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                />
              </div>
              <div className="col-span-2">
                <label className="text-xs text-text-muted block mb-1">
                  Target
                  <span className="text-text-dim">
                    {" · "}
                    {meta.public_host
                      ? `defaults to public_host: ${meta.public_host}`
                      : "public_host not configured — required"}
                  </span>
                </label>
                <input
                  type="text"
                  value={target}
                  onChange={(e) => setTarget(e.target.value)}
                  placeholder={recordType === "A" ? "1.2.3.4" : "edge.acme.com"}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                />
              </div>
            </div>
            {err && <div className="text-red text-xs">{err}</div>}
          </>
        )}
        <div className="flex items-center justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >Cancel</button>
          <button
            type="submit"
            disabled={busy || !canSubmit || !meta || meta.domains.length === 0}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Attaching…" : "Attach"}</button>
        </div>
      </form>
    </div>
  );
}

// ─── CertBadge ────────────────────────────────────────────────────
//
// One-glance cert status next to an attached domain. The Certs app
// owns issuance state — this is purely a render of the meta payload.

function CertBadge({ status }: { status?: CertStatus }) {
  if (!status) {
    return <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-dim">no cert</span>;
  }
  const cls =
    status.status === "live"     ? "bg-green/15 text-green" :
    status.status === "issuing"  ? "bg-blue/15 text-blue" :
    status.status === "pending"  ? "bg-blue/15 text-blue" :
    status.status === "failed"   ? "bg-red/15 text-red" :
                                   "bg-border text-text-dim";
  const label =
    status.status === "live"    ? `cert · expires ${formatExpires(status.expires_at)}` :
    status.status === "issuing" ? "cert · issuing…" :
    status.status === "pending" ? "cert · pending" :
    status.status === "failed"  ? "cert · failed" :
                                  `cert · ${status.status}`;
  return (
    <span
      className={`text-[10px] px-1.5 py-0.5 rounded ${cls}`}
      title={status.error || (status.expires_at ? `expires ${status.expires_at}` : "")}
    >
      {label}
    </span>
  );
}

function formatExpires(iso?: string): string {
  if (!iso) return "?";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "?";
  return d.toISOString().slice(0, 10);
}
