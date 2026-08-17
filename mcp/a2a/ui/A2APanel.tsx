// A2APanel — ledger view for agent-to-agent communication.
//
// Layout: task list (left) + conversation detail (right). Tasks and
// messages come from /api/apps/a2a/tasks*; live updates ride the
// AppBus (task.created / task.updated) via the dashboard's shared
// event bridge, with a direct EventSource fallback.
//
// Uses the dashboard's Tailwind theme tokens (bg-bg / text-text-muted /
// border-border / text-error / bg-success / …) so the panel recolors
// across themes. The bundled .mjs is produced by
// `bun run scripts/build-panels.ts --app a2a` from the apps repo root.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/a2a";

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
    // Shared per-(app, project) EventSource pool published by the
    // dashboard — one connection for every panel in the realm.
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
    // Fallback: direct EventSource (panel mounted outside the dashboard).
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

interface A2ATask {
  id: number;
  kind: "message" | "ask";
  status: string;
  from_agent_id: number;
  from_agent_name?: string;
  to_agent_id: number;
  to_agent_name?: string;
  created_at: string;
  updated_at: string;
}

interface A2AMessage {
  id: number;
  task_id: number;
  from_agent_id: number;
  to_agent_id: number;
  body: string;
  status_after?: string;
  created_at: string;
}

const STATUS_FILTERS = [
  { value: "", label: "All" },
  { value: "open", label: "Open" },
  { value: "completed", label: "Completed" },
  { value: "failed", label: "Failed" },
  { value: "canceled", label: "Canceled" },
];

function statusPillClass(status: string): string {
  switch (status) {
    case "completed":
      return "bg-success text-bg";
    case "failed":
      return "bg-error text-bg";
    case "input_required":
      return "bg-warn text-bg";
    case "canceled":
      return "bg-border text-text-muted";
    default: // submitted, working
      return "bg-accent text-bg";
  }
}

function statusLabel(status: string): string {
  return status.replace(/_/g, " ");
}

function relTime(iso: string): string {
  if (!iso) return "";
  const then = new Date(iso);
  const ms = Date.now() - then.getTime();
  if (Number.isNaN(ms)) return "";
  const m = Math.floor(ms / 60000);
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  if (d < 30) return `${d}d ago`;
  return then.toLocaleDateString();
}

function absTime(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

// Exchange glyph for the empty states — inline SVG, currentColor only
// (Tailwind color utilities don't reach into panel SVG).
function ExchangeGlyph({ size = 28 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M8 3 4 7l4 4" />
      <path d="M4 7h16" />
      <path d="m16 21 4-4-4-4" />
      <path d="M20 17H4" />
    </svg>
  );
}

export default function A2APanel({ projectId }: NativePanelProps) {
  const [tasks, setTasks] = useState<A2ATask[]>([]);
  const [statusFilter, setStatusFilter] = useState("");
  const [selectedId, setSelectedId] = useState(0);
  const [messages, setMessages] = useState<A2AMessage[]>([]);
  const [note, setNote] = useState("");

  const url = useCallback(
    (path: string) => {
      const sep = path.includes("?") ? "&" : "?";
      return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
    },
    [projectId],
  );

  const loadTasks = useCallback(async () => {
    try {
      const params = new URLSearchParams();
      if (statusFilter) params.set("status", statusFilter);
      const res = await fetch(url(`/tasks?${params.toString()}`), {
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      const list: A2ATask[] = (await res.json()).tasks ?? [];
      setTasks(list);
      setSelectedId((prev) =>
        prev && list.some((t) => t.id === prev) ? prev : (list[0]?.id ?? 0),
      );
      setNote(`${list.length} task${list.length === 1 ? "" : "s"}`);
    } catch (err) {
      setNote(err instanceof Error ? err.message : String(err));
    }
  }, [statusFilter, url]);

  const loadMessages = useCallback(
    async (taskId: number) => {
      if (!taskId) {
        setMessages([]);
        return;
      }
      try {
        const res = await fetch(url(`/tasks/${taskId}/messages`), {
          credentials: "same-origin",
        });
        if (!res.ok) throw new Error(await res.text());
        setMessages((await res.json()).messages ?? []);
      } catch {
        setMessages([]);
      }
    },
    [url],
  );

  useEffect(() => {
    loadTasks();
  }, [loadTasks]);

  useEffect(() => {
    loadMessages(selectedId);
  }, [selectedId, loadMessages]);

  const selectedIdRef = useRef(selectedId);
  selectedIdRef.current = selectedId;
  useAppEvents<{ id?: number }>("a2a", projectId, (ev) => {
    if (ev.topic !== "task.created" && ev.topic !== "task.updated") return;
    loadTasks();
    if (ev.data?.id && ev.data.id === selectedIdRef.current) {
      loadMessages(selectedIdRef.current);
    }
  });

  const selected = useMemo(
    () => tasks.find((t) => t.id === selectedId) ?? null,
    [tasks, selectedId],
  );

  const agentName = useCallback(
    (task: A2ATask, agentId: number): string => {
      if (agentId === task.from_agent_id && task.from_agent_name)
        return task.from_agent_name;
      if (agentId === task.to_agent_id && task.to_agent_name)
        return task.to_agent_name;
      return `agent ${agentId}`;
    },
    [],
  );

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div>
          <h1 className="text-sm font-semibold">Agent to Agent</h1>
          <p className="text-xs text-text-muted">
            Messages and request/reply tasks exchanged between agents.
          </p>
        </div>
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          className="ml-auto bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
        >
          {STATUS_FILTERS.map((f) => (
            <option key={f.value} value={f.value}>
              {f.label}
            </option>
          ))}
        </select>
        <button
          type="button"
          onClick={loadTasks}
          className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input"
        >
          Refresh
        </button>
      </header>

      <main className="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[380px_minmax(0,1fr)]">
        <aside className="border-r border-border min-h-0 overflow-auto">
          {tasks.length === 0 ? (
            <div className="p-6 text-sm text-text-muted flex flex-col items-center gap-3 text-center">
              <span className="text-text-dim">
                <ExchangeGlyph />
              </span>
              <p>No agent-to-agent tasks yet.</p>
              <p className="text-xs text-text-dim">
                Agents start exchanges with the agent_send and agent_ask
                tools; every exchange lands here.
              </p>
            </div>
          ) : (
            <ul className="divide-y divide-border">
              {tasks.map((t) => (
                <li key={t.id}>
                  <button
                    type="button"
                    onClick={() => setSelectedId(t.id)}
                    className={`w-full text-left px-4 py-3 hover:bg-bg-input ${t.id === selectedId ? "bg-bg-input" : ""}`}
                  >
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium truncate">
                        {agentName(t, t.from_agent_id)}
                        <span className="text-text-dim px-1">&rarr;</span>
                        {agentName(t, t.to_agent_id)}
                      </span>
                      <span
                        className={`ml-auto shrink-0 text-xs px-1.5 py-0.5 rounded ${statusPillClass(t.status)}`}
                      >
                        {statusLabel(t.status)}
                      </span>
                    </div>
                    <div className="mt-1.5 flex items-center gap-2 text-xs text-text-dim">
                      <span className="px-1.5 py-0.5 rounded bg-bg border border-border">
                        {t.kind === "ask" ? "request" : "message"}
                      </span>
                      <span>#{t.id}</span>
                      <span className="ml-auto">{relTime(t.updated_at)}</span>
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </aside>

        <section className="min-h-0 flex flex-col">
          {selected ? (
            <>
              <div className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
                <div>
                  <h2 className="text-sm font-semibold">
                    Task #{selected.id}
                    <span className="text-text-muted font-normal">
                      {" "}
                      &middot; {agentName(selected, selected.from_agent_id)}
                      <span className="px-1 text-text-dim">&rarr;</span>
                      {agentName(selected, selected.to_agent_id)}
                    </span>
                  </h2>
                  <p className="text-xs text-text-muted">
                    {selected.kind === "ask"
                      ? "Request with reply"
                      : "One-way message"}{" "}
                    &middot; started {absTime(selected.created_at)}
                  </p>
                </div>
                <span
                  className={`ml-auto text-xs px-2 py-1 rounded ${statusPillClass(selected.status)}`}
                >
                  {statusLabel(selected.status)}
                </span>
              </div>
              <div className="flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-3">
                {messages.length === 0 ? (
                  <p className="text-sm text-text-muted">No messages recorded.</p>
                ) : (
                  messages.map((m) => {
                    const fromRequester =
                      m.from_agent_id === selected.from_agent_id;
                    return (
                      <div
                        key={m.id}
                        className={`max-w-[85%] rounded border border-border bg-bg-card px-3 py-2 ${fromRequester ? "self-start" : "self-end"}`}
                      >
                        <div className="flex items-center gap-2 text-xs text-text-muted">
                          <span className="font-medium text-text">
                            {agentName(selected, m.from_agent_id)}
                          </span>
                          {m.status_after && (
                            <span
                              className={`px-1.5 py-0.5 rounded ${statusPillClass(m.status_after)}`}
                            >
                              {statusLabel(m.status_after)}
                            </span>
                          )}
                          <span className="ml-auto">{relTime(m.created_at)}</span>
                        </div>
                        <p className="mt-1.5 text-sm whitespace-pre-wrap">
                          {m.body}
                        </p>
                      </div>
                    );
                  })
                )}
              </div>
            </>
          ) : (
            <div className="flex-1 flex flex-col items-center justify-center gap-3 text-text-muted">
              <span className="text-text-dim">
                <ExchangeGlyph size={36} />
              </span>
              <p className="text-sm">Select a task to see the exchange.</p>
            </div>
          )}
          <footer className="shrink-0 border-t border-border px-4 py-2 text-xs text-text-muted">
            {note}
          </footer>
        </section>
      </main>
    </div>
  );
}
