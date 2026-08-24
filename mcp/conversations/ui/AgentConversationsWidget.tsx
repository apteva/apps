import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ConversationChat,
  apiGet,
  apiPost,
  type Conversation,
} from "./ConversationsPanel";
import {
  appendAgentScope,
  fixedAgentConversationInput,
  scopedConversationListPath,
} from "./agentConversations";

interface AgentConversationsWidgetProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId: number;
  eventRevision?: number;
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

export default function AgentConversationsWidget({
  projectId,
  instanceId,
  eventRevision,
}: AgentConversationsWidgetProps) {
  const validAgent = Number.isInteger(instanceId) && instanceId > 0;
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [unread, setUnread] = useState<Map<string, UnreadEntry>>(new Map());
  const [selectedId, setSelectedId] = useState("");
  const [archived, setArchived] = useState(false);
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

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
    <div className="grid h-full min-h-0 grid-rows-[minmax(150px,34%)_minmax(0,1fr)] overflow-hidden bg-bg text-text md:grid-cols-[250px_minmax(0,1fr)] md:grid-rows-1 md:divide-x md:divide-border">
      <aside className="flex min-h-0 flex-col border-b border-border md:border-b-0">
        <div className="flex shrink-0 items-center gap-2 border-b border-border p-3">
          <button
            type="button"
            onClick={() => setCreating(true)}
            className="rounded bg-accent px-2.5 py-1.5 text-xs font-semibold text-bg"
          >
            New conversation
          </button>
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
        {creating && (
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
