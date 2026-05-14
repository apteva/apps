// FunctionsPanel — native React panel for the functions app. List of
// functions on the left as a table; clicking one opens a detail modal
// with source, env, recent invocations, and a live "Invoke" console.
// Loaded by the dashboard via dynamic import; uses host React via
// importmap; talks to the functions sidecar through
// /api/apps/functions/* with same-origin cookies.

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
    // (app, project) instead of opening its own.
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

type Runtime = "bun" | "node" | "python" | "sh";
type Status = "active" | "disabled";
type InvStatus = "ok" | "error" | "timeout";

interface FunctionRow {
  id: number;
  name: string;
  runtime: Runtime;
  source_kind: "inline" | "repo";
  source?: string;
  repo_id?: number;
  repo_path?: string;
  source_hash: string;
  env?: Record<string, string>;
  timeout_ms: number;
  max_memory_mb: number;
  status: Status;
  created_at?: string;
  updated_at?: string;
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

const RUNTIMES: Runtime[] = ["bun", "node", "python", "sh"];

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
  return s === "active"
    ? "bg-green/15 text-green"
    : "bg-border text-text-muted";
}

function invStatusTone(s: InvStatus): string {
  switch (s) {
    case "ok":      return "bg-green/15 text-green";
    case "error":   return "bg-red/15 text-red";
    case "timeout": return "bg-blue/15 text-blue";
    default:        return "bg-border text-text-muted";
  }
}

// A function's runtime decides the source-editor language hint and the
// placeholder shown in the create dialog. Kept here so both the create
// and edit paths agree.
const RUNTIME_HINT: Record<Runtime, string> = {
  bun: "TypeScript — read the event from stdin, write the response to stdout.",
  node: "JavaScript (ESM) — read the event from stdin, write the response to stdout.",
  python: "Python 3 — read the event from sys.stdin, print the response.",
  sh: "POSIX shell — the event JSON arrives on stdin; echo the response.",
};

const RUNTIME_SAMPLE: Record<Runtime, string> = {
  bun: `const event = await Bun.stdin.json();\nconsole.log(JSON.stringify({ hello: event.name ?? "world" }));`,
  node: `let raw = "";\nprocess.stdin.on("data", (c) => (raw += c));\nprocess.stdin.on("end", () => {\n  const event = JSON.parse(raw || "null");\n  console.log(JSON.stringify({ hello: event?.name ?? "world" }));\n});`,
  python: `import sys, json\nevent = json.load(sys.stdin)\nprint(json.dumps({"hello": event.get("name", "world")}))`,
  sh: `event=$(cat)\necho "{\\"received\\": $event}"`,
};

export default function FunctionsPanel({ projectId, installId }: NativePanelProps) {
  const [functions, setFunctions] = useState<FunctionRow[]>([]);
  const [runtimeFilter, setRuntimeFilter] = useState<Runtime | "">("");
  const [statusFilter, setStatusFilter] = useState<Status | "">("");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [detail, setDetail] = useState<FunctionRow | null>(null);
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
      if (runtimeFilter) extra.runtime = runtimeFilter;
      if (statusFilter) extra.status = statusFilter;
      const r = await api<{ functions?: FunctionRow[] }>("GET", "/functions", undefined, extra);
      setFunctions(r.functions || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }, [api, runtimeFilter, statusFilter]);

  const loadDetail = useCallback(
    async (id: number) => {
      try {
        const f = await api<{ function: FunctionRow }>("GET", `/functions/${id}`);
        const r = await api<{ invocations?: Invocation[] }>("GET", `/functions/${id}/invocations`);
        setDetail(f.function);
        setInvocations(r.invocations || []);
      } catch (e) {
        setDetail(null);
        setError((e as Error).message);
      }
    },
    [api],
  );

  useEffect(() => { loadList(); }, [loadList]);

  // Live refresh: react instantly to create/update/delete events.
  useAppEvents("functions", projectId, (ev) => {
    if (
      ev.topic === "function.created" ||
      ev.topic === "function.updated" ||
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
    setInvocations([]);
  };

  const handleToggleStatus = async () => {
    if (!detail) return;
    const next: Status = detail.status === "active" ? "disabled" : "active";
    try {
      await api("PATCH", `/functions/${detail.id}`, { status: next });
      await loadDetail(detail.id);
      await loadList();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const handleDelete = async () => {
    if (!detail) return;
    try {
      await api("DELETE", `/functions/${detail.id}`);
      closeDetail();
      await loadList();
    } catch (e) {
      setError("Delete failed: " + (e as Error).message);
    }
  };

  return (
    <div className="h-full flex flex-col">
      {/* Header */}
      <header className="px-6 py-3 border-b border-border flex items-center gap-3">
        <h1 className="text-text font-medium">Functions</h1>
        <span className="text-text-dim text-xs">
          {functions.length} function{functions.length !== 1 ? "s" : ""}
        </span>
        <select
          value={runtimeFilter}
          onChange={(e) => setRuntimeFilter(e.target.value as Runtime | "")}
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm ml-4"
        >
          <option value="">all runtimes</option>
          {RUNTIMES.map((r) => (
            <option key={r} value={r}>{r}</option>
          ))}
        </select>
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value as Status | "")}
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
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

      {/* List */}
      <main className="flex-1 overflow-auto">
        {error ? (
          <div className="p-6 text-red text-sm">{error}</div>
        ) : functions.length === 0 ? (
          <div className="py-12 px-6 text-center text-text-muted text-sm">
            No functions yet.{" "}
            <button
              type="button"
              onClick={() => setCreating(true)}
              className="text-accent"
            >Create one</button>
            .
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50 sticky top-0">
              <tr>
                <th className="text-left px-4 py-2 font-normal">Name</th>
                <th className="text-left px-4 py-2 font-normal w-24">Runtime</th>
                <th className="text-left px-4 py-2 font-normal w-28">Source</th>
                <th className="text-left px-4 py-2 font-normal w-24">Status</th>
                <th className="text-left px-4 py-2 font-normal w-24">Timeout</th>
                <th className="text-left px-4 py-2 font-normal w-40">Endpoint</th>
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
                  <td className="px-4 py-2 text-text-muted">{f.source_kind}</td>
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

      {/* Create dialog */}
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

      {/* Detail dialog */}
      {detail && (
        <DetailDialog
          fn={detail}
          invocations={invocations}
          onClose={closeDetail}
          onToggleStatus={handleToggleStatus}
          onDelete={handleDelete}
          onInvoked={() => loadDetail(detail.id)}
          withParams={withParams}
        />
      )}
    </div>
  );
}

// ─── Detail dialog ────────────────────────────────────────────────────
// Modal showing one function's metadata, source, env, an inline invoke
// console, and the recent-invocations table.

function DetailDialog({
  fn, invocations, onClose, onToggleStatus, onDelete, onInvoked, withParams,
}: {
  fn: FunctionRow;
  invocations: Invocation[];
  onClose: () => void;
  onToggleStatus: () => void | Promise<void>;
  onDelete: () => void | Promise<void>;
  onInvoked: () => void | Promise<void>;
  withParams: (extra?: Record<string, string>) => string;
}) {
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [expanded, setExpanded] = useState<number | null>(null);

  const handleConfirmDelete = async () => {
    setDeleting(true);
    try { await onDelete(); }
    finally { setDeleting(false); setConfirming(false); }
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
            </div>
            <p className="text-text-muted text-sm">
              {fn.source_kind === "inline"
                ? "inline source"
                : `repo #${fn.repo_id} · ${fn.repo_path}`}
              {" · "}timeout {(fn.timeout_ms / 1000).toFixed(0)}s
              {" · "}mem {fn.max_memory_mb}MB
            </p>
            <p className="text-text-dim text-xs mt-1 font-mono">
              POST /api/apps/functions/fn/{fn.name}
            </p>
          </div>
          <button onClick={onClose} className="text-text-muted hover:text-text text-xl leading-none">×</button>
        </header>

        {/* Invoke console */}
        <InvokeConsole fn={fn} withParams={withParams} onInvoked={onInvoked} />

        {/* Source */}
        {fn.source_kind === "inline" && fn.source && (
          <section>
            <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Source</h3>
            <pre className="text-[11px] bg-bg-input border border-border rounded p-3 overflow-auto max-h-48 whitespace-pre-wrap font-mono">
              {fn.source}
            </pre>
          </section>
        )}

        {/* Env */}
        {fn.env && Object.keys(fn.env).length > 0 && (
          <section>
            <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Environment</h3>
            <div className="flex flex-wrap gap-1">
              {Object.keys(fn.env).map((k) => (
                <span key={k} className="text-[11px] px-1.5 py-0.5 rounded bg-bg-input border border-border font-mono">
                  {k}
                </span>
              ))}
            </div>
          </section>
        )}

        {/* Actions */}
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onToggleStatus}
            className="px-3 py-1 text-sm border border-border text-text-muted rounded hover:bg-bg-input"
          >{fn.status === "active" ? "Disable" : "Enable"}</button>
          {confirming ? (
            <div className="ml-auto flex items-center gap-2">
              <span className="text-text-muted text-xs">Delete this function and all its invocations?</span>
              <button
                type="button"
                onClick={() => setConfirming(false)}
                disabled={deleting}
                className="px-3 py-1 text-sm border border-border text-text-muted rounded"
              >Keep</button>
              <button
                type="button"
                onClick={handleConfirmDelete}
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
          <div className="text-[10px] uppercase tracking-wide text-text-dim mb-1">stdout</div>
          <pre className="text-[11px] bg-bg border border-border rounded p-2 overflow-auto max-h-32 whitespace-pre-wrap font-mono">{inv.response_body}</pre>
        </div>
      )}
      {inv.stderr && (
        <div>
          <div className="text-[10px] uppercase tracking-wide text-text-dim mb-1">stderr</div>
          <pre className="text-[11px] bg-bg border border-border rounded p-2 overflow-auto max-h-32 whitespace-pre-wrap font-mono text-red">{inv.stderr}</pre>
        </div>
      )}
      {inv.error && (
        <div className="text-red text-xs">error: {inv.error}</div>
      )}
    </div>
  );
}

// ─── Invoke console ───────────────────────────────────────────────────
// Inline, in the detail dialog. Posts the event JSON straight to
// /functions/<id>/invoke; the response body is the function's stdout,
// and X-Apteva-Function-Status carries ok / error / timeout.

function InvokeConsole({
  fn, withParams, onInvoked,
}: {
  fn: FunctionRow;
  withParams: (extra?: Record<string, string>) => string;
  onInvoked: () => void | Promise<void>;
}) {
  const [eventText, setEventText] = useState("{}");
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<{
    status: string;
    body: string;
    invocationId: string;
  } | null>(null);
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

// ─── Create-function dialog ───────────────────────────────────────────
// Posts to /functions with the schema buildAndCreateFunction expects:
// { name, runtime, source_kind, source | repo_id+repo_path, env?,
//   timeout_ms?, max_memory_mb? }.

type ApiFn = <T,>(method: string, path: string, body?: unknown, extra?: Record<string, string>) => Promise<T>;

function CreateFunctionDialog({
  onClose, onCreated, api,
}: {
  onClose: () => void;
  onCreated: () => void;
  api: ApiFn;
}) {
  const [name, setName] = useState("");
  const [runtime, setRuntime] = useState<Runtime>("bun");
  const [sourceKind, setSourceKind] = useState<"inline" | "repo">("inline");
  const [source, setSource] = useState(RUNTIME_SAMPLE.bun);
  const [touchedSource, setTouchedSource] = useState(false);
  const [repoId, setRepoId] = useState("");
  const [repoPath, setRepoPath] = useState("");
  const [envText, setEnvText] = useState("");
  const [timeoutSec, setTimeoutSec] = useState("30");
  const [maxMemoryMb, setMaxMemoryMb] = useState("256");
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState("");

  // Swap the inline sample to match the chosen runtime — but only
  // while the author hasn't typed their own body yet.
  const pickRuntime = (r: Runtime) => {
    setRuntime(r);
    if (!touchedSource) setSource(RUNTIME_SAMPLE[r]);
  };

  const submit = async () => {
    setErr("");
    if (!name.trim()) { setErr("Name is required."); return; }
    if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(name.trim())) {
      setErr("Name must be a lowercase slug: [a-z0-9][a-z0-9-]{0,62}.");
      return;
    }

    const body: Record<string, unknown> = {
      name: name.trim(),
      runtime,
      source_kind: sourceKind,
    };

    if (sourceKind === "inline") {
      if (!source.trim()) { setErr("Source body is required for an inline function."); return; }
      body.source = source;
    } else {
      const rid = Number(repoId);
      if (!Number.isInteger(rid) || rid <= 0) { setErr("Repo id must be a positive integer."); return; }
      if (!repoPath.trim()) { setErr("Repo path is required for a repo function."); return; }
      body.repo_id = rid;
      body.repo_path = repoPath.trim();
    }

    if (envText.trim()) {
      const env: Record<string, string> = {};
      for (const line of envText.split("\n")) {
        const t = line.trim();
        if (!t) continue;
        const eq = t.indexOf("=");
        if (eq < 1) { setErr(`Bad env line (want KEY=value): ${t}`); return; }
        env[t.slice(0, eq).trim()] = t.slice(eq + 1);
      }
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
            {RUNTIMES.map((r) => (
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
          <p className="text-text-dim text-[11px] mt-0.5">{RUNTIME_HINT[runtime]}</p>
        </div>

        <div className="flex flex-col gap-1">
          <label className={labelCls}>Source</label>
          <div className="flex gap-1">
            {(["inline", "repo"] as const).map((k) => (
              <button
                key={k}
                type="button"
                onClick={() => setSourceKind(k)}
                className={`flex-1 px-2 py-1 text-xs border rounded ${
                  sourceKind === k
                    ? "border-accent text-accent bg-accent/10"
                    : "border-border text-text-muted hover:bg-bg-input"
                }`}
              >{k}</button>
            ))}
          </div>
          {sourceKind === "inline" ? (
            <textarea
              value={source}
              onChange={(e) => { setSource(e.target.value); setTouchedSource(true); }}
              spellCheck={false}
              className={inputCls + " mt-1 font-mono min-h-[140px]"}
            />
          ) : (
            <div className="flex gap-2 mt-1">
              <input
                type="number"
                min="1"
                value={repoId}
                onChange={(e) => setRepoId(e.target.value)}
                placeholder="repo id"
                className="w-28 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              />
              <input
                type="text"
                value={repoPath}
                onChange={(e) => setRepoPath(e.target.value)}
                placeholder="entry file path within the repo"
                className="flex-1 min-w-0 bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono"
              />
            </div>
          )}
        </div>

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
            <input
              type="number"
              min="1"
              max="300"
              value={timeoutSec}
              onChange={(e) => setTimeoutSec(e.target.value)}
              className={inputCls}
            />
          </div>
          <div className="flex flex-col gap-1 flex-1">
            <label className={labelCls}>Max memory (MB)</label>
            <input
              type="number"
              min="1"
              max="1024"
              value={maxMemoryMb}
              onChange={(e) => setMaxMemoryMb(e.target.value)}
              className={inputCls}
            />
          </div>
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
