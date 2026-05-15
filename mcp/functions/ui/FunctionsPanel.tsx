// FunctionsPanel — native React panel for the functions app.
//
// A function is a thin definition that points at an immutable, built
// version. The panel lists functions, creates them (which deploys
// v1), deploys new versions, rolls back, and runs an inline invoke
// console against the active version. Loaded by the dashboard via
// dynamic import; uses host React via importmap; talks to the
// functions sidecar through /api/apps/functions/* with same-origin
// cookies.

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

type Status = "active" | "disabled";
type InvStatus = "ok" | "error" | "timeout";
type BuildStatus = "pending" | "building" | "ready" | "failed";

interface FunctionRow {
  id: number;
  name: string;
  runtime: string;
  source_kind: "inline" | "repo";
  source?: string;
  repo_id?: number;
  repo_path?: string;
  timeout_ms: number;
  max_memory_mb: number;
  status: Status;
  active_version_id?: number;
}

interface Version {
  id: number;
  function_id: number;
  version: number;
  source_kind: "inline" | "repo";
  source?: string;
  repo_id?: number;
  repo_path?: string;
  package_json?: string;
  build_status: BuildStatus;
  build_log?: string;
  created_at?: string;
}

interface Invocation {
  id: number;
  function_id: number;
  started_at: string;
  finished_at?: string;
  duration_ms: number;
  status: InvStatus;
  exit_code: number;
  trigger_kind: string;
  event_json?: string;
  response_body?: string;
  stderr?: string;
  error?: string;
}

const API = "/api/apps/functions";

// Starter handlers shown in the create dialog.
const SAMPLE_HANDLER =
  `export default async function handler(event, context) {\n` +
  `  // event: the JSON payload. context.call(app, tool, input)\n` +
  `  // reaches other Apteva apps. Return any JSON.\n` +
  `  return { hello: event?.name ?? "world" };\n` +
  `}`;

const SAMPLE_GO =
  "// Go: package main + a Handle func. The harness supplies main()\n" +
  "// and the Context type — don't write your own main().\n" +
  "package main\n\n" +
  "import \"encoding/json\"\n\n" +
  "func Handle(event json.RawMessage, ctx *Context) (any, error) {\n" +
  "\tvar e struct{ Name string `json:\"name\"` }\n" +
  "\t_ = json.Unmarshal(event, &e)\n" +
  "\tif e.Name == \"\" { e.Name = \"world\" }\n" +
  "\treturn map[string]any{\"hello\": e.Name}, nil\n" +
  "}";

function relTime(s?: string): string {
  if (!s) return "—";
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  const diff = (d.getTime() - Date.now()) / 1000;
  const abs = Math.abs(diff);
  if (abs < 60) return `${Math.round(abs)}s ago`;
  if (abs < 3600) return `${Math.round(abs / 60)}m ago`;
  if (abs < 86400) return `${Math.round(abs / 3600)}h ago`;
  return d.toLocaleString();
}

function fnStatusTone(s: Status): string {
  return s === "active" ? "bg-green/15 text-green" : "bg-border text-text-muted";
}

function invStatusTone(s: InvStatus): string {
  switch (s) {
    case "ok":      return "bg-green/15 text-green";
    case "error":   return "bg-red/15 text-red";
    case "timeout": return "bg-blue/15 text-blue";
    default:        return "bg-border text-text-muted";
  }
}

function buildStatusTone(s: BuildStatus): string {
  switch (s) {
    case "ready":    return "bg-green/15 text-green";
    case "failed":   return "bg-red/15 text-red";
    case "building": return "bg-blue/15 text-blue";
    default:         return "bg-border text-text-muted";
  }
}

type ApiFn = <T,>(method: string, path: string, body?: unknown, extra?: Record<string, string>) => Promise<T>;

// handleCodeTab makes Tab insert two spaces in a code textarea
// (instead of moving focus). Uses execCommand("insertText") when
// available so the textarea's native undo history survives the
// edit; falls back to a manual splice otherwise.
function handleCodeTab(
  e: { key: string; preventDefault: () => void; currentTarget: HTMLTextAreaElement },
  current: string,
  setValue: (v: string) => void,
) {
  if (e.key !== "Tab") return;
  e.preventDefault();
  let ok = false;
  try { ok = document.execCommand("insertText", false, "  "); } catch {}
  if (ok) return;
  const ta = e.currentTarget;
  const start = ta.selectionStart;
  const end = ta.selectionEnd;
  setValue(current.substring(0, start) + "  " + current.substring(end));
  requestAnimationFrame(() => {
    ta.selectionStart = ta.selectionEnd = start + 2;
  });
}

export default function FunctionsPanel({ projectId, installId }: NativePanelProps) {
  const [functions, setFunctions] = useState<FunctionRow[]>([]);
  const [statusFilter, setStatusFilter] = useState<Status | "">("");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [detail, setDetail] = useState<FunctionRow | null>(null);
  const [versions, setVersions] = useState<Version[]>([]);
  const [invocations, setInvocations] = useState<Invocation[]>([]);
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

  const api: ApiFn = useCallback(
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
      const r = await api<{ functions?: FunctionRow[] }>("GET", "/functions", undefined, extra);
      setFunctions(r.functions || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }, [api, statusFilter]);

  const loadDetail = useCallback(
    async (id: number) => {
      try {
        const f = await api<{ function: FunctionRow }>("GET", `/functions/${id}`);
        const v = await api<{ versions?: Version[] }>("GET", `/functions/${id}/versions`);
        const r = await api<{ invocations?: Invocation[] }>("GET", `/functions/${id}/invocations`);
        setDetail(f.function);
        setVersions(v.versions || []);
        setInvocations(r.invocations || []);
      } catch (e) {
        setDetail(null);
        setError((e as Error).message);
      }
    },
    [api],
  );

  useEffect(() => { loadList(); }, [loadList]);

  useAppEvents("functions", projectId, (ev) => {
    if (
      ev.topic === "function.created" ||
      ev.topic === "function.updated" ||
      ev.topic === "function.deployed" ||
      ev.topic === "function.deleted"
    ) {
      loadList();
      if (selectedId) loadDetail(selectedId);
    }
  });

  const select = (id: number) => {
    setSelectedId(id);
    loadDetail(id);
  };

  const closeDetail = () => {
    setSelectedId(null);
    setDetail(null);
    setVersions([]);
    setInvocations([]);
  };

  const refreshDetail = useCallback(async () => {
    if (selectedId) await loadDetail(selectedId);
    await loadList();
  }, [selectedId, loadDetail, loadList]);

  const activeVersionNo = (f: FunctionRow): string => {
    const v = versions.find((x) => x.id === f.active_version_id);
    return v ? `v${v.version}` : f.active_version_id ? "…" : "—";
  };

  return (
    <div className="h-full flex flex-col">
      <header className="px-6 py-3 border-b border-border flex items-center gap-3">
        <h1 className="text-text font-medium">Functions</h1>
        <span className="text-text-dim text-xs">
          {functions.length} function{functions.length !== 1 ? "s" : ""}
        </span>
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value as Status | "")}
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm ml-4"
        >
          <option value="">all statuses</option>
          <option value="active">active</option>
          <option value="disabled">disabled</option>
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
        >+ New function</button>
      </header>

      <main className="flex-1 overflow-auto">
        {error ? (
          <div className="p-6 text-red text-sm">{error}</div>
        ) : functions.length === 0 ? (
          <div className="py-12 px-6 text-center text-text-muted text-sm">
            No functions yet.{" "}
            <button type="button" onClick={() => setCreating(true)} className="text-accent">
              Create one
            </button>
            .
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50 sticky top-0">
              <tr>
                <th className="text-left px-4 py-2 font-normal">Name</th>
                <th className="text-left px-4 py-2 font-normal w-24">Runtime</th>
                <th className="text-left px-4 py-2 font-normal w-24">Status</th>
                <th className="text-left px-4 py-2 font-normal w-24">Timeout</th>
                <th className="text-left px-4 py-2 font-normal w-44">Endpoint</th>
              </tr>
            </thead>
            <tbody>
              {functions.map((f) => (
                <tr
                  key={f.id}
                  onClick={() => select(f.id)}
                  className="border-t border-border cursor-pointer hover:bg-bg-input/50"
                >
                  <td className="px-4 py-2 text-text font-medium truncate max-w-md">{f.name}</td>
                  <td className="px-4 py-2 text-text-muted font-mono text-xs">{f.runtime}</td>
                  <td className="px-4 py-2">
                    <span className={`text-[10px] px-1.5 py-0.5 rounded ${fnStatusTone(f.status)}`}>
                      {f.status}
                    </span>
                  </td>
                  <td className="px-4 py-2 text-text-muted">{(f.timeout_ms / 1000).toFixed(0)}s</td>
                  <td className="px-4 py-2 text-text-dim text-xs font-mono truncate">/fn/{f.name}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </main>

      {creating && (
        <CreateFunctionDialog
          onClose={() => setCreating(false)}
          onCreated={async () => {
            setCreating(false);
            await loadList();
          }}
          api={api}
        />
      )}

      {detail && (
        <DetailDialog
          fn={detail}
          versions={versions}
          invocations={invocations}
          api={api}
          activeVersionNo={activeVersionNo(detail)}
          onClose={closeDetail}
          onChanged={refreshDetail}
          withParams={withParams}
        />
      )}
    </div>
  );
}

// ─── Detail dialog ────────────────────────────────────────────────────

function DetailDialog({
  fn, versions, invocations, api, activeVersionNo, onClose, onChanged, withParams,
}: {
  fn: FunctionRow;
  versions: Version[];
  invocations: Invocation[];
  api: ApiFn;
  activeVersionNo: string;
  onClose: () => void;
  onChanged: () => void | Promise<void>;
  withParams: (extra?: Record<string, string>) => string;
}) {
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deploying, setDeploying] = useState(false);
  const [expanded, setExpanded] = useState<number | null>(null);
  const [busy, setBusy] = useState("");

  const handleToggleStatus = async () => {
    const next: Status = fn.status === "active" ? "disabled" : "active";
    try {
      await api("PATCH", `/functions/${fn.id}`, { status: next });
      await onChanged();
    } catch (e) {
      setBusy("status: " + (e as Error).message);
    }
  };

  const handleDelete = async () => {
    setDeleting(true);
    try {
      await api("DELETE", `/functions/${fn.id}`);
      onClose();
    } catch (e) {
      setBusy("delete: " + (e as Error).message);
    } finally {
      setDeleting(false);
      setConfirming(false);
    }
  };

  const handleRollback = async (version: number) => {
    try {
      await api("POST", `/functions/${fn.id}/rollback`, { version });
      await onChanged();
    } catch (e) {
      setBusy(`rollback to v${version}: ` + (e as Error).message);
    }
  };

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
              <h2 className="text-lg text-text font-semibold truncate">{fn.name}</h2>
              <span className={`text-[11px] px-2 py-0.5 rounded ${fnStatusTone(fn.status)}`}>
                {fn.status}
              </span>
              <span className="text-[11px] px-2 py-0.5 rounded bg-border text-text-muted font-mono">
                {fn.runtime}
              </span>
              <span className="text-[11px] px-2 py-0.5 rounded bg-accent/15 text-accent font-mono">
                {activeVersionNo} active
              </span>
            </div>
            <p className="text-text-muted text-sm">
              timeout {(fn.timeout_ms / 1000).toFixed(0)}s · mem {fn.max_memory_mb}MB
            </p>
            <p className="text-text-dim text-xs mt-1 font-mono">
              POST /api/apps/functions/fn/{fn.name}
            </p>
          </div>
          <button onClick={onClose} className="text-text-muted hover:text-text text-xl leading-none">×</button>
        </header>

        {busy && <div className="text-red text-xs">{busy}</div>}

        <InvokeConsole fn={fn} withParams={withParams} onInvoked={onChanged} />

        {/* Versions */}
        <section>
          <div className="flex items-center gap-2 mb-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Versions ({versions.length})
            </h3>
            <button
              type="button"
              onClick={() => setDeploying(true)}
              className="ml-auto px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg"
            >Deploy new version</button>
          </div>
          {versions.length === 0 ? (
            <p className="text-text-muted text-sm">No versions.</p>
          ) : (
            <table className="w-full text-sm">
              <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50">
                <tr>
                  <th className="text-left px-3 py-2 font-normal w-20">Version</th>
                  <th className="text-left px-3 py-2 font-normal w-24">Build</th>
                  <th className="text-left px-3 py-2 font-normal w-20">Deps</th>
                  <th className="text-left px-3 py-2 font-normal">Deployed</th>
                  <th className="text-left px-3 py-2 font-normal w-28"></th>
                </tr>
              </thead>
              <tbody>
                {versions.map((v) => {
                  const isActive = v.id === fn.active_version_id;
                  return (
                    <tr key={v.id} className="border-t border-border">
                      <td className="px-3 py-2 text-text font-mono">
                        v{v.version}
                        {isActive && <span className="ml-1 text-accent text-[10px]">● active</span>}
                      </td>
                      <td className="px-3 py-2">
                        <span className={`text-[10px] px-1.5 py-0.5 rounded ${buildStatusTone(v.build_status)}`}>
                          {v.build_status}
                        </span>
                      </td>
                      <td className="px-3 py-2 text-text-muted text-xs">
                        {v.package_json ? "package.json" : "—"}
                      </td>
                      <td className="px-3 py-2 text-text-muted">{relTime(v.created_at)}</td>
                      <td className="px-3 py-2">
                        {!isActive && v.build_status === "ready" && (
                          <button
                            type="button"
                            onClick={() => handleRollback(v.version)}
                            className="px-2 py-0.5 text-xs border border-border text-text-muted rounded hover:bg-bg-input"
                          >Activate</button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </section>

        {/* Actions */}
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={handleToggleStatus}
            className="px-3 py-1 text-sm border border-border text-text-muted rounded hover:bg-bg-input"
          >{fn.status === "active" ? "Disable" : "Enable"}</button>
          {confirming ? (
            <div className="ml-auto flex items-center gap-2">
              <span className="text-text-muted text-xs">Delete this function, all its versions + invocations?</span>
              <button
                type="button"
                onClick={() => setConfirming(false)}
                disabled={deleting}
                className="px-3 py-1 text-sm border border-border text-text-muted rounded"
              >Keep</button>
              <button
                type="button"
                onClick={handleDelete}
                disabled={deleting}
                className="px-3 py-1 text-sm bg-red text-white rounded font-bold disabled:opacity-50"
              >{deleting ? "Deleting…" : "Yes, delete"}</button>
            </div>
          ) : (
            <button
              type="button"
              onClick={() => setConfirming(true)}
              className="px-3 py-1 text-sm text-red border border-red rounded ml-auto"
            >Delete function</button>
          )}
        </div>

        {/* Recent invocations */}
        <section>
          <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">
            Recent invocations ({invocations.length})
          </h3>
          {invocations.length === 0 ? (
            <p className="text-text-muted text-sm">No invocations yet.</p>
          ) : (
            <table className="w-full text-sm">
              <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50">
                <tr>
                  <th className="text-left px-3 py-2 font-normal">Started</th>
                  <th className="text-left px-3 py-2 font-normal w-24">Trigger</th>
                  <th className="text-left px-3 py-2 font-normal w-20">Duration</th>
                  <th className="text-left px-3 py-2 font-normal w-24">Status</th>
                  <th className="text-left px-3 py-2 font-normal w-16">Exit</th>
                </tr>
              </thead>
              <tbody>
                {invocations.flatMap((inv) => {
                  const rows = [
                    <tr
                      key={inv.id}
                      onClick={() => setExpanded(expanded === inv.id ? null : inv.id)}
                      className="border-t border-border cursor-pointer hover:bg-bg-input/50"
                    >
                      <td className="px-3 py-2 text-text-muted">{relTime(inv.started_at)}</td>
                      <td className="px-3 py-2 text-text-dim text-xs">{inv.trigger_kind}</td>
                      <td className="px-3 py-2 text-text-muted">{inv.duration_ms} ms</td>
                      <td className="px-3 py-2">
                        <span className={`text-[10px] px-1.5 py-0.5 rounded ${invStatusTone(inv.status)}`}>
                          {inv.status}
                        </span>
                      </td>
                      <td className="px-3 py-2 text-text-muted">{inv.exit_code}</td>
                    </tr>,
                  ];
                  if (expanded === inv.id) {
                    rows.push(
                      <tr key={inv.id + "-detail"} className="border-t border-border bg-bg-input/30">
                        <td colSpan={5} className="px-3 py-2">
                          <InvocationDetail inv={inv} />
                        </td>
                      </tr>,
                    );
                  }
                  return rows;
                })}
              </tbody>
            </table>
          )}
        </section>

        {deploying && (
          <DeployDialog
            fn={fn}
            api={api}
            onClose={() => setDeploying(false)}
            onDeployed={async () => {
              setDeploying(false);
              await onChanged();
            }}
          />
        )}
      </div>
    </div>
  );
}

function InvocationDetail({ inv }: { inv: Invocation }) {
  return (
    <div className="flex flex-col gap-2">
      {inv.event_json && (
        <div>
          <div className="text-[10px] uppercase tracking-wide text-text-dim mb-1">Event</div>
          <pre className="text-[11px] bg-bg border border-border rounded p-2 overflow-auto max-h-32 whitespace-pre-wrap font-mono">{inv.event_json}</pre>
        </div>
      )}
      {inv.response_body && (
        <div>
          <div className="text-[10px] uppercase tracking-wide text-text-dim mb-1">Return value</div>
          <pre className="text-[11px] bg-bg border border-border rounded p-2 overflow-auto max-h-32 whitespace-pre-wrap font-mono">{inv.response_body}</pre>
        </div>
      )}
      {inv.stderr && (
        <div>
          <div className="text-[10px] uppercase tracking-wide text-text-dim mb-1">Console output</div>
          <pre className="text-[11px] bg-bg border border-border rounded p-2 overflow-auto max-h-32 whitespace-pre-wrap font-mono text-text-muted">{inv.stderr}</pre>
        </div>
      )}
      {inv.error && <div className="text-red text-xs">error: {inv.error}</div>}
    </div>
  );
}

// ─── Invoke console ───────────────────────────────────────────────────

function InvokeConsole({
  fn, withParams, onInvoked,
}: {
  fn: FunctionRow;
  withParams: (extra?: Record<string, string>) => string;
  onInvoked: () => void | Promise<void>;
}) {
  const [eventText, setEventText] = useState("{}");
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<{ status: string; body: string; invocationId: string } | null>(null);
  const [err, setErr] = useState("");

  const run = async () => {
    setErr("");
    let body: unknown;
    const trimmed = eventText.trim();
    if (trimmed === "") {
      body = null;
    } else {
      try {
        body = JSON.parse(trimmed);
      } catch {
        setErr("Event must be valid JSON (or leave blank).");
        return;
      }
    }
    setRunning(true);
    setResult(null);
    try {
      const res = await fetch(`${API}/functions/${fn.id}/invoke?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const text = await res.text();
      setResult({
        status: res.headers.get("X-Apteva-Function-Status") || (res.ok ? "ok" : "error"),
        body: text,
        invocationId: res.headers.get("X-Apteva-Function-Invocation") || "",
      });
      await onInvoked();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setRunning(false);
    }
  };

  return (
    <section className="border border-border rounded-lg p-3 bg-bg-input/30">
      <div className="flex items-center gap-2 mb-2">
        <h3 className="text-xs uppercase tracking-wide text-text-dim">Invoke</h3>
        {fn.status !== "active" && (
          <span className="text-[10px] text-text-muted">function is disabled — enable it to invoke</span>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <textarea
          value={eventText}
          onChange={(e) => setEventText(e.target.value)}
          spellCheck={false}
          placeholder='Event JSON, e.g. {"name":"world"}'
          className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono min-h-[64px]"
        />
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={run}
            disabled={running || fn.status !== "active"}
            className="px-3 py-1 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >{running ? "Running…" : "Invoke"}</button>
          {err && <span className="text-red text-xs">{err}</span>}
        </div>
        {result && (
          <div className="flex flex-col gap-1">
            <div className="flex items-center gap-2 text-xs">
              <span className={`text-[10px] px-1.5 py-0.5 rounded ${
                result.status === "ok" ? "bg-green/15 text-green" :
                result.status === "timeout" ? "bg-blue/15 text-blue" :
                "bg-red/15 text-red"
              }`}>{result.status}</span>
              {result.invocationId && (
                <span className="text-text-dim">invocation #{result.invocationId}</span>
              )}
            </div>
            <pre className="text-[11px] bg-bg border border-border rounded p-2 overflow-auto max-h-40 whitespace-pre-wrap font-mono">
              {result.body || "(empty response)"}
            </pre>
          </div>
        )}
      </div>
    </section>
  );
}

// ─── Create / Deploy dialogs ──────────────────────────────────────────

function envLinesToMap(envText: string): Record<string, string> | string {
  const env: Record<string, string> = {};
  for (const line of envText.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const eq = t.indexOf("=");
    if (eq < 1) return `Bad env line (want KEY=value): ${t}`;
    env[t.slice(0, eq).trim()] = t.slice(eq + 1);
  }
  return env;
}

const inputCls = "w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm";
const labelCls = "text-xs uppercase tracking-wide text-text-dim";

function CreateFunctionDialog({
  onClose, onCreated, api,
}: {
  onClose: () => void;
  onCreated: () => void;
  api: ApiFn;
}) {
  const [name, setName] = useState("");
  const [runtime, setRuntime] = useState<"node" | "go">("node");
  const [source, setSource] = useState(SAMPLE_HANDLER);
  const [touchedSource, setTouchedSource] = useState(false);
  const [packageJSON, setPackageJSON] = useState("");
  const [envText, setEnvText] = useState("");
  const [timeoutSec, setTimeoutSec] = useState("30");
  const [maxMemoryMb, setMaxMemoryMb] = useState("256");
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState("");

  // Swap the starter handler to match the runtime — but only while
  // the author hasn't typed their own.
  const pickRuntime = (r: "node" | "go") => {
    setRuntime(r);
    if (!touchedSource) setSource(r === "go" ? SAMPLE_GO : SAMPLE_HANDLER);
  };

  const submit = async () => {
    setErr("");
    if (!name.trim()) { setErr("Name is required."); return; }
    if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(name.trim())) {
      setErr("Name must be a lowercase slug: [a-z0-9][a-z0-9-]{0,62}.");
      return;
    }
    if (!source.trim()) { setErr("Handler source is required."); return; }

    const body: Record<string, unknown> = {
      name: name.trim(),
      runtime,
      source_kind: "inline",
      source,
    };
    if (runtime === "node" && packageJSON.trim()) body.package_json = packageJSON;
    if (envText.trim()) {
      const env = envLinesToMap(envText);
      if (typeof env === "string") { setErr(env); return; }
      body.env = env;
    }
    const tSec = Number(timeoutSec);
    if (Number.isFinite(tSec) && tSec > 0) body.timeout_ms = Math.round(tSec * 1000);
    const mem = Number(maxMemoryMb);
    if (Number.isFinite(mem) && mem > 0) body.max_memory_mb = Math.round(mem);

    setSubmitting(true);
    try {
      await api("POST", "/functions", body);
      onCreated();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        className="relative bg-bg-card border border-border rounded-lg shadow-lg max-w-5xl w-full mx-4 overflow-auto flex flex-col max-h-[90vh] p-4 gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <div className="text-text font-medium">New function</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>

        <div className="flex flex-col gap-1">
          <label className={labelCls}>Name</label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. hello-world"
            autoFocus
            className={inputCls + " font-mono"}
          />
        </div>

        <div className="flex flex-col gap-1">
          <label className={labelCls}>Runtime</label>
          <div className="flex gap-1">
            {(["node", "go"] as const).map((r) => (
              <button
                key={r}
                type="button"
                onClick={() => pickRuntime(r)}
                className={`flex-1 px-2 py-1 text-xs border rounded font-mono ${
                  runtime === r
                    ? "border-accent text-accent bg-accent/10"
                    : "border-border text-text-muted hover:bg-bg-input"
                }`}
              >{r}</button>
            ))}
          </div>
        </div>

        <div className="flex flex-col gap-1">
          <label className={labelCls}>
            {runtime === "go"
              ? "Handler (go) — func Handle(event json.RawMessage, ctx *Context) (any, error)"
              : "Handler (node) — export default async (event, context) => result"}
          </label>
          <textarea
            value={source}
            onChange={(e) => { setSource(e.target.value); setTouchedSource(true); }}
            onKeyDown={(e) => handleCodeTab(e, source, (v) => { setSource(v); setTouchedSource(true); })}
            spellCheck={false}
            wrap="off"
            style={{ tabSize: 2 }}
            className={inputCls + " font-mono text-[13px] leading-snug min-h-[480px]"}
          />
        </div>

        {runtime === "node" && (
          <div className="flex flex-col gap-1">
            <label className={labelCls}>package.json (optional — deps installed at deploy)</label>
            <textarea
              value={packageJSON}
              onChange={(e) => setPackageJSON(e.target.value)}
              spellCheck={false}
              placeholder='{"dependencies":{"ky":"^1.0.0"}}'
              className={inputCls + " font-mono min-h-[48px]"}
            />
          </div>
        )}

        <div className="flex flex-col gap-1">
          <label className={labelCls}>Environment (one KEY=value per line, optional)</label>
          <textarea
            value={envText}
            onChange={(e) => setEnvText(e.target.value)}
            spellCheck={false}
            placeholder="API_BASE=https://example.com"
            className={inputCls + " font-mono min-h-[48px]"}
          />
        </div>

        <div className="flex gap-2">
          <div className="flex flex-col gap-1 flex-1">
            <label className={labelCls}>Timeout (seconds)</label>
            <input type="number" min="1" max="300" value={timeoutSec}
              onChange={(e) => setTimeoutSec(e.target.value)} className={inputCls} />
          </div>
          <div className="flex flex-col gap-1 flex-1">
            <label className={labelCls}>Max memory (MB)</label>
            <input type="number" min="1" max="1024" value={maxMemoryMb}
              onChange={(e) => setMaxMemoryMb(e.target.value)} className={inputCls} />
          </div>
        </div>

        {err && <div className="text-red text-xs">{err}</div>}

        <div className="flex gap-2 justify-end mt-1">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
          <button
            type="button"
            onClick={submit}
            disabled={submitting || !name.trim()}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >{submitting ? "Deploying…" : "Create + deploy v1"}</button>
        </div>
      </div>
    </div>
  );
}

function DeployDialog({
  fn, api, onClose, onDeployed,
}: {
  fn: FunctionRow;
  api: ApiFn;
  onClose: () => void;
  onDeployed: () => void;
}) {
  const [source, setSource] = useState(fn.source ?? SAMPLE_HANDLER);
  const [packageJSON, setPackageJSON] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    setErr("");
    if (!source.trim()) { setErr("Handler source is required."); return; }
    const body: Record<string, unknown> = { source_kind: "inline", source };
    if (packageJSON.trim()) body.package_json = packageJSON;
    setSubmitting(true);
    try {
      await api("POST", `/functions/${fn.id}/deploy`, body);
      onDeployed();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        className="relative bg-bg-card border border-border rounded-lg shadow-lg max-w-5xl w-full mx-4 overflow-auto flex flex-col max-h-[90vh] p-4 gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <div className="text-text font-medium">Deploy new version of {fn.name}</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>

        <div className="flex flex-col gap-1">
          <label className={labelCls}>Handler source</label>
          <textarea
            value={source}
            onChange={(e) => setSource(e.target.value)}
            onKeyDown={(e) => handleCodeTab(e, source, setSource)}
            spellCheck={false}
            wrap="off"
            style={{ tabSize: 2 }}
            className={inputCls + " font-mono text-[13px] leading-snug min-h-[520px]"}
          />
        </div>

        <div className="flex flex-col gap-1">
          <label className={labelCls}>package.json (optional — deps installed at deploy)</label>
          <textarea
            value={packageJSON}
            onChange={(e) => setPackageJSON(e.target.value)}
            spellCheck={false}
            placeholder='{"dependencies":{}}'
            className={inputCls + " font-mono min-h-[48px]"}
          />
        </div>

        {err && <div className="text-red text-xs">{err}</div>}

        <div className="flex gap-2 justify-end mt-1">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
          <button
            type="button"
            onClick={submit}
            disabled={submitting}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >{submitting ? "Building…" : "Build + activate"}</button>
        </div>
      </div>
    </div>
  );
}
