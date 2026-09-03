import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ConversationChat,
  apiGet,
  apiPost,
  type Conversation,
} from "./ConversationsPanel";
import {
  agentConversationWidgetLayout,
  appendAgentScope,
  conversationDisplayMode,
  fixedAgentConversationInput,
  scopedConversationListPath,
  showNewConversation,
  singleConversationListPath,
  type AgentConversationWidgetSettings,
} from "./agentConversations";

interface AgentConversationsWidgetProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId: number;
  eventRevision?: number;
  widgetSettings?: AgentConversationWidgetSettings;
}

interface UnreadEntry {
  conversation_id: string;
  latest_id: number;
  unread: number;
}

function relativeTime(iso: string): string {
  const elapsed = Date.now() - new Date(iso).getTime();
  if (!Number.isFinite(elapsed) || elapsed < 60_000) return "now";
  const minutes = Math.floor(elapsed / 60_000);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours}h` : `${Math.floor(hours / 24)}d`;
}

function useWideWidgetLayout(): boolean {
  const query = "(min-width: 768px)";
  const [wide, setWide] = useState(() =>
    typeof window !== "undefined" && window.matchMedia(query).matches,
  );
  useEffect(() => {
    const media = window.matchMedia(query);
    const update = () => setWide(media.matches);
    media.addEventListener("change", update);
    update();
    return () => media.removeEventListener("change", update);
  }, []);
  return wide;
}

function ConversationBrowser({
  projectId,
  instanceId,
  eventRevision,
  widgetSettings,
}: AgentConversationsWidgetProps) {
  const validAgent = Number.isInteger(instanceId) && instanceId > 0;
  const showCreate = showNewConversation(widgetSettings);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [unread, setUnread] = useState<Map<string, UnreadEntry>>(new Map());
  const [selectedId, setSelectedId] = useState("");
  const [archived, setArchived] = useState(false);
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const wideLayout = useWideWidgetLayout();
  const layout = agentConversationWidgetLayout(wideLayout);

  const load = useCallback(async () => {
    if (!validAgent || !projectId) return;
    try {
      const [rows, unreadRows] = await Promise.all([
        apiGet<Conversation[]>(scopedConversationListPath(archived, instanceId), projectId),
        apiGet<UnreadEntry[]>(appendAgentScope("/unread-summary", instanceId), projectId),
      ]);
      const scoped = rows.filter((conversation) =>
        !conversation.project_id || conversation.project_id === projectId,
      );
      setConversations(scoped);
      setUnread(new Map(unreadRows.map((entry) => [entry.conversation_id, entry])));
      setSelectedId((current) =>
        current && scoped.some((conversation) => conversation.id === current)
          ? current
          : (scoped[0]?.id ?? ""),
      );
      setError("");
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : String(loadError));
    }
  }, [archived, instanceId, projectId, validAgent]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(load, 8_000);
    return () => window.clearInterval(timer);
  }, [load, eventRevision]);

  useEffect(() => {
    if (!selectedId) return;
    const entry = unread.get(selectedId);
    if (!entry?.unread) return;
    void apiPost("/seen", {
      chat_id: selectedId,
      last_seen_id: entry.latest_id,
    }, projectId).then(load, () => undefined);
  }, [load, projectId, selectedId, unread]);

  const selected = useMemo(
    () => conversations.find((conversation) => conversation.id === selectedId) ?? null,
    [conversations, selectedId],
  );

  const create = async () => {
    if (!validAgent || saving) return;
    setSaving(true);
    setError("");
    try {
      const conversation = await apiPost<Conversation>(
        "/chats",
        fixedAgentConversationInput(instanceId, projectId, title),
        projectId,
      );
      setArchived(false);
      setCreating(false);
      setTitle("");
      setConversations((current) => [conversation, ...current.filter((item) => item.id !== conversation.id)]);
      setSelectedId(conversation.id);
      if (!archived) await load();
    } catch (createError) {
      setError(createError instanceof Error ? createError.message : String(createError));
    } finally {
      setSaving(false);
    }
  };

  if (!projectId || !validAgent) {
    return (
      <div className="grid h-full place-items-center bg-bg p-6 text-center text-sm text-text-muted">
        Agent conversations needs a valid project and target agent.
      </div>
    );
  }

  return (
    <div
      className="grid h-full min-h-0 overflow-hidden bg-bg text-text"
      style={layout}
    >
      <aside
        className="flex min-h-0 flex-col"
        style={wideLayout
          ? { borderRight: "1px solid var(--color-border)" }
          : { borderBottom: "1px solid var(--color-border)" }}
      >
        <div className="flex shrink-0 items-center gap-2 border-b border-border p-3">
          {showCreate && (
            <button
              type="button"
              onClick={() => setCreating(true)}
              className="rounded bg-accent px-2.5 py-1.5 text-xs font-semibold text-bg"
            >
              New conversation
            </button>
          )}
          <button
            type="button"
            onClick={() => {
              setArchived((value) => !value);
              setSelectedId("");
            }}
            className={`ml-auto rounded px-2 py-1.5 text-xs ${archived ? "bg-bg-input text-text" : "text-text-muted hover:bg-bg-input hover:text-text"}`}
          >
            {archived ? "Active" : "Archived"}
          </button>
        </div>
        {showCreate && creating && (
          <form
            className="shrink-0 space-y-2 border-b border-border p-3"
            onSubmit={(event) => {
              event.preventDefault();
              void create();
            }}
          >
            <input
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              placeholder="Conversation title (optional)"
              autoFocus
              className="w-full rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text focus:border-accent focus:outline-none"
            />
            <div className="flex gap-2">
              <button type="submit" disabled={saving} className="rounded bg-accent px-2.5 py-1.5 text-xs font-semibold text-bg disabled:opacity-40">
                {saving ? "Creating…" : "Create"}
              </button>
              <button type="button" disabled={saving} onClick={() => setCreating(false)} className="rounded px-2.5 py-1.5 text-xs text-text-muted hover:bg-bg-input">
                Cancel
              </button>
            </div>
          </form>
        )}
        {error && <p className="shrink-0 border-b border-border px-3 py-2 text-xs text-error">{error}</p>}
        <div className="min-h-0 flex-1 overflow-auto">
          {conversations.length === 0 ? (
            <p className="p-5 text-center text-xs text-text-muted">
              {archived ? "No archived conversations." : "No conversations with this agent yet."}
            </p>
          ) : (
            <ul className="divide-y divide-border">
              {conversations.map((conversation) => {
                const unreadCount = unread.get(conversation.id)?.unread ?? 0;
                return (
                  <li key={conversation.id}>
                    <button
                      type="button"
                      onClick={() => setSelectedId(conversation.id)}
                      className={`w-full border-l-2 px-3 py-2.5 text-left ${conversation.id === selectedId ? "border-accent bg-bg-hover" : "border-transparent hover:bg-bg-hover"}`}
                    >
                      <div className="flex items-center gap-2">
                        <span className="min-w-0 flex-1 truncate text-sm font-medium text-text">{conversation.title}</span>
                        {unreadCount > 0 && conversation.id !== selectedId && (
                          <span className="rounded-full bg-accent px-1.5 py-0.5 text-xs text-bg">{unreadCount}</span>
                        )}
                      </div>
                      <div className="mt-1 flex items-center gap-2 text-xs text-text-dim">
                        {conversation.kind === "room" && <span>room</span>}
                        <span className="ml-auto">{relativeTime(conversation.updated_at)}</span>
                      </div>
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      </aside>

      {selected ? (
        <ConversationChat
          conversation={selected}
          archived={archived}
          onActed={load}
          onRemoved={() => {
            setSelectedId("");
            void load();
          }}
        />
      ) : (
        <section className="grid min-h-0 place-items-center p-6 text-center text-sm text-text-muted">
          Select a conversation or create one with this agent.
        </section>
      )}
    </div>
  );
}

interface AgentInfo {
  id: number;
  name: string;
}

function SingleConversation({
  projectId,
  instanceId,
  widgetSettings,
}: AgentConversationsWidgetProps) {
  const validAgent = Number.isInteger(instanceId) && instanceId > 0;
  const showCreate = showNewConversation(widgetSettings);
  const requestGeneration = useRef(0);
  const historyGeneration = useRef(0);
  const [selected, setSelected] = useState<Conversation | null>(null);
  const [agentName, setAgentName] = useState("");
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [history, setHistory] = useState<Conversation[]>([]);
  const [error, setError] = useState("");

  const loadLatest = useCallback(async () => {
    const generation = ++requestGeneration.current;
    setSelected(null);
    setLoading(true);
    setError("");
    if (!projectId || !validAgent) {
      setLoading(false);
      return;
    }
    try {
      const rows = await apiGet<Conversation[]>(singleConversationListPath(instanceId), projectId);
      if (generation !== requestGeneration.current) return;
      setSelected(rows[0] ?? null);
    } catch (loadError) {
      if (generation !== requestGeneration.current) return;
      setError(loadError instanceof Error ? loadError.message : String(loadError));
    } finally {
      if (generation === requestGeneration.current) setLoading(false);
    }
  }, [instanceId, projectId, validAgent]);

  useEffect(() => {
    setHistoryOpen(false);
    setHistory([]);
    setAgentName("");
    void loadLatest();
    if (projectId && validAgent) {
      const generation = requestGeneration.current;
      void apiGet<AgentInfo[]>("/agents", projectId).then(
        (agents) => {
          if (generation !== requestGeneration.current) return;
          setAgentName(agents.find((agent) => agent.id === instanceId)?.name ?? "");
        },
        () => undefined,
      );
    }
    return () => {
      requestGeneration.current += 1;
      historyGeneration.current += 1;
    };
  }, [instanceId, loadLatest, projectId, validAgent]);

  const create = async () => {
    if (!projectId || !validAgent || !showCreate || creating) return;
    setCreating(true);
    setError("");
    try {
      const conversation = await apiPost<Conversation>(
        "/chats",
        fixedAgentConversationInput(instanceId, projectId, ""),
        projectId,
      );
      requestGeneration.current += 1;
      setSelected(conversation);
      setHistoryOpen(false);
      setHistory((current) => [conversation, ...current.filter((item) => item.id !== conversation.id)]);
    } catch (createError) {
      setError(createError instanceof Error ? createError.message : String(createError));
    } finally {
      setCreating(false);
    }
  };

  const openHistory = async () => {
    const generation = ++historyGeneration.current;
    setHistoryOpen(true);
    setHistoryLoading(true);
    setError("");
    try {
      const rows = await apiGet<Conversation[]>(singleConversationListPath(instanceId, 50), projectId);
      if (generation === historyGeneration.current) setHistory(rows);
    } catch (historyError) {
      if (generation === historyGeneration.current) {
        setError(historyError instanceof Error ? historyError.message : String(historyError));
      }
    } finally {
      if (generation === historyGeneration.current) setHistoryLoading(false);
    }
  };

  if (!projectId || !validAgent) {
    return (
      <div className="grid h-full place-items-center bg-bg p-6 text-center text-sm text-text-muted">
        Agent conversations needs a valid project and target agent.
      </div>
    );
  }

  const displayAgentName = agentName || `agent ${instanceId}`;

  return (
    <div className="relative flex h-full min-h-0 flex-col overflow-hidden bg-bg text-text">
      {error && selected && (
        <p className="shrink-0 border-b border-border px-4 py-2 text-xs text-error">{error}</p>
      )}
      {loading ? (
        <section className="grid min-h-0 flex-1 place-items-center p-6 text-sm text-text-muted">
          Loading conversation…
        </section>
      ) : selected ? (
        <ConversationChat
          conversation={selected}
          archived={false}
          headerActions={(
            <>
              {showCreate && (
                <button
                  type="button"
                  onClick={() => void create()}
                  disabled={creating}
                  className="rounded px-2 py-1.5 text-xs text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-40"
                >
                  {creating ? "Starting…" : "New conversation"}
                </button>
              )}
              <button
                type="button"
                onClick={() => void openHistory()}
                className="rounded px-2 py-1.5 text-xs text-text-muted hover:bg-bg-input hover:text-text"
              >
                History
              </button>
            </>
          )}
          onActed={() => undefined}
          onRemoved={() => void loadLatest()}
        />
      ) : (
        <section className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 p-6 text-center">
          <div>
            <h2 className="text-sm font-semibold text-text">
              {showCreate ? `Start a conversation with ${displayAgentName}` : `No conversation with ${displayAgentName} yet`}
            </h2>
            <p className="mt-1 text-xs text-text-muted">Messages will stay in this agent's durable conversation history.</p>
          </div>
          {showCreate && (
            <button
              type="button"
              onClick={() => void create()}
              disabled={creating}
              className="rounded bg-accent px-3 py-2 text-xs font-semibold text-bg disabled:opacity-40"
            >
              {creating ? "Starting…" : "Start conversation"}
            </button>
          )}
          {error && (
            <div className="space-y-2">
              <p className="text-xs text-error">{error}</p>
              <button type="button" onClick={() => void loadLatest()} className="text-xs text-text-muted underline hover:text-text">
                Try again
              </button>
            </div>
          )}
        </section>
      )}

      {historyOpen && (
        <div className="absolute inset-0 z-10 flex justify-end">
          <button
            type="button"
            className="absolute inset-0 bg-black/60"
            onClick={() => {
              historyGeneration.current += 1;
              setHistoryOpen(false);
            }}
            aria-label="Close conversation history"
          />
          <aside className="relative flex h-full w-full max-w-sm flex-col border-l border-border bg-bg-card shadow-xl">
            <div className="flex shrink-0 items-center border-b border-border px-4 py-3">
              <h2 className="text-sm font-semibold text-text">Conversation history</h2>
              <button
                type="button"
                onClick={() => {
                  historyGeneration.current += 1;
                  setHistoryOpen(false);
                }}
                className="ml-auto rounded px-2 py-1 text-xs text-text-muted hover:bg-bg-input hover:text-text"
              >
                Close
              </button>
            </div>
            <div className="min-h-0 flex-1 overflow-auto">
              {historyLoading ? (
                <p className="p-5 text-center text-xs text-text-muted">Loading history…</p>
              ) : history.length === 0 ? (
                <p className="p-5 text-center text-xs text-text-muted">No earlier conversations.</p>
              ) : (
                <ul className="divide-y divide-border">
                  {history.map((conversation) => (
                    <li key={conversation.id}>
                      <button
                        type="button"
                        onClick={() => {
                          requestGeneration.current += 1;
                          setSelected(conversation);
                          setHistoryOpen(false);
                        }}
                        className={`w-full px-4 py-3 text-left hover:bg-bg-hover ${conversation.id === selected?.id ? "bg-bg-hover" : ""}`}
                      >
                        <span className="block truncate text-sm font-medium text-text">{conversation.title}</span>
                        <span className="mt-1 block text-xs text-text-dim">{relativeTime(conversation.updated_at)}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </aside>
        </div>
      )}
    </div>
  );
}

export default function AgentConversationsWidget(props: AgentConversationsWidgetProps) {
  return conversationDisplayMode(props.widgetSettings) === "single"
    ? <SingleConversation key={`${props.projectId}:${props.instanceId}`} {...props} />
    : <ConversationBrowser {...props} />;
}
