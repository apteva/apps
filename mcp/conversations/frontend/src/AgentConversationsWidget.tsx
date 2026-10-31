import { ThreadActionRow } from "./threadActions";
import { DetailsDialog, NewConversationDialog } from "./ConversationsPanel";
import { agentAvatarMode, showsAgentAvatar, ConversationIdentity } from "./agentAppearance";
import type { AgentInfo } from "./types";
import type { ComposerOptions } from "./composer";
import type { ConversationHost } from "./hostUI";
import type { ConversationComposerHandle } from "./composerHost";
import { useConversationLocalization, type ConversationLocalization } from "./i18n";
import { useConversationAPI } from "./context";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type Ref } from "react";
import { ConversationActivityIndicator, ConversationUnreadIndicator, shouldShowConversationUnreadIndicator, useConversationActivity } from "./conversationActivity";
import {
  ConversationChat,
  MoreConversations,
  refreshConversationList,
  type Conversation,
} from "./ConversationsPanel";
import {
  agentConversationWidgetLayout,
  appendAgentScope,
  conversationDisplayMode,
  conversationListPath,
  fixedAgentConversationInput,
  PROJECT_WIDGET_WIDE_MIN_PX,
  selectedConversationSeenInput,
  showNewConversation,
  showPageContext,
  showToolCompletion,
  showToolDuration,
  singleConversationListPath,
  unreadSummaryPath,
  type AgentConversationWidgetSettings,
} from "./agentConversations";

export interface AgentConversationsWidgetProps extends ConversationLocalization {
  host?: ConversationHost;
  pageContext?: import("./pageContext").PageContext;
 composer?:ComposerOptions;
  appName: string;
  installId: number;
  projectId: string;
  instanceId: number;
  slot?: string;
  widgetSize?: "half" | "full";
  eventRevision?: number;
  widgetSettings?: AgentConversationWidgetSettings;
  /** Internal/public typed host bridge; never sends automatically. */
  composerRef?: Ref<ConversationComposerHandle>;
  onContextCleared?: (context: import("./pageContext").PageContext) => void;
}

interface ConversationBrowserProps extends AgentConversationsWidgetProps {
  /** Project widgets list every agent's conversations and pick agents per conversation. */
  allAgents?: boolean;
  /** Host route to the full Conversations page. */
  conversationsHref?: string;
}

interface UnreadEntry {
  conversation_id: string;
  latest_id: number;
  unread: number;
}

const EMPTY_CONVERSATION_REFRESH_MS = 8_000;
/** No instanceId means every agent in the project. */
function useAgentDirectory(projectId: string, instanceId: number | undefined, enabled: boolean) {
  const { conversationsClient } = useConversationAPI();
  const [directory, setDirectory] = useState<{scope: string; agents: AgentInfo[]}>({scope: "", agents: []});
  const scope = `${projectId}:${instanceId ?? "all"}`;
  useEffect(() => {
    let cancelled = false;
    const load = () => { if (enabled && projectId && (instanceId === undefined || instanceId > 0)) void conversationsClient.agents().then(agents => {
      if (!cancelled) setDirectory({scope, agents});
    }, () => { if (!cancelled) setDirectory({scope, agents: []}); }); };
    load();
    const timer = enabled ? window.setInterval(load, 8000) : undefined;
    return () => { cancelled = true; window.clearInterval(timer); };
  }, [conversationsClient, scope, enabled]);
  return directory.scope === scope ? directory.agents : [];
}


function useWideWidgetLayout(allowWide: boolean): boolean {
  const query = "(min-width: 768px)";
  const [wide, setWide] = useState(() =>
    allowWide && typeof window !== "undefined" && window.matchMedia(query).matches,
  );
  useEffect(() => {
    if (!allowWide) { setWide(false); return; }
    const media = window.matchMedia(query);
    const update = () => setWide(media.matches);
    media.addEventListener("change", update);
    update();
    return () => media.removeEventListener("change", update);
  }, [allowWide]);
  return wide;
}

/** Measures the widget's own box; a Home tile can be narrow on a wide screen. */
function useContainerWide(enabled: boolean, minWidth: number) {
  const [element, setElement] = useState<HTMLDivElement | null>(null);
  const [wide, setWide] = useState(false);
  useLayoutEffect(() => {
    if (!enabled || !element) { setWide(false); return; }
    const update = () => setWide(element.getBoundingClientRect().width >= minWidth);
    update();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(update);
    observer.observe(element);
    return () => observer.disconnect();
  }, [element, enabled, minWidth]);
  return [setElement, wide] as const;
}

function ConversationBrowser({
  host,
  projectId,
  instanceId,
  eventRevision,
  widgetSettings,
  widgetSize,
  composerRef,
  onContextCleared,
  allAgents = false,
  conversationsHref,
}: ConversationBrowserProps) {
  const { t, relativeTime } = useConversationLocalization();
  const { conversationsClient, apiGet, apiPost, apiPatch, apiDelete } = useConversationAPI();
  const agentId = allAgents ? undefined : instanceId;
  const validAgent = Number.isInteger(instanceId) && instanceId > 0;
  const validScope = allAgents || validAgent;
  const showCreate = showNewConversation(widgetSettings);
  const avatarMode = agentAvatarMode(widgetSettings?.agent_avatars, allAgents ? "threads" : "hidden");
  const appearanceAgents = useAgentDirectory(projectId, agentId, showsAgentAvatar(avatarMode, "threads"));
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [unread, setUnread] = useState<Map<string, UnreadEntry>>(new Map());
  const [selectedId, setSelectedId] = useState("");
  const [archived, setArchived] = useState(false);
  const [listEventRevision, setListEventRevision] = useState(0);
  const activeConversations = useConversationActivity(projectId, agentId, !archived && validScope, () => setListEventRevision(value => value + 1));
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [pickerAgents, setPickerAgents] = useState<AgentInfo[] | null>(null);
  const [pickerError, setPickerError] = useState("");
  const viewportWide = useWideWidgetLayout(!allAgents && widgetSize !== "half");
  const [setFrame, tileWide] = useContainerWide(allAgents, PROJECT_WIDGET_WIDE_MIN_PX);
  const wideLayout = allAgents ? tileWide : viewportWide;
  // Narrow project tiles show the list or the open chat, like the page on a phone.
  const stacked = allAgents && !wideLayout;
  const [detailOpen, setDetailOpen] = useState(false);
  const layout = agentConversationWidgetLayout(wideLayout, stacked);
  // Opening a chat leaves focus to the composer; going back returns it to the row.
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const frameRef = useRef<HTMLDivElement | null>(null);
  const attachFrame = useCallback((element: HTMLDivElement | null) => { frameRef.current = element; setFrame(element); }, [setFrame]);
  const wasDetailRef = useRef(false);
  useLayoutEffect(() => {
    const showingDetail = stacked && detailOpen;
    if (!showingDetail && wasDetailRef.current) {
      const id = returnFocusRef.current?.dataset.conversationId;
      frameRef.current?.querySelector<HTMLButtonElement>(`button[data-conversation-id="${CSS.escape(id ?? "")}"]`)?.focus();
    }
    wasDetailRef.current = showingDetail;
  }, [stacked, detailOpen]);

  useEffect(()=>{setConversations([]);setSelectedId("");setDetailOpen(false);},[projectId,instanceId,archived]);
  const listStateRef=useRef({scope:"",count:0,selected:""});
  const listScope=`${projectId}:${agentId ?? "all"}:${archived}`;
  listStateRef.current=listStateRef.current.scope===listScope ? {scope:listScope,count:conversations.length,selected:selectedId} : {scope:listScope,count:0,selected:""};
  const load = useCallback(async () => {
    if (!validScope || !projectId) return;
    const snapshot={...listStateRef.current};
    try {
      const [rows, unreadRows] = await Promise.all([
        refreshConversationList(conversationListPath(archived,agentId),conversationsClient,snapshot.count,snapshot.selected),
        apiGet<UnreadEntry[]>(unreadSummaryPath(agentId), projectId),
      ]);
      if(snapshot.scope!==listStateRef.current.scope)return;
      const scoped = rows.filter((conversation) =>
        !conversation.project_id || conversation.project_id === projectId,
      );
      if(listStateRef.current.count<=Math.max(100,snapshot.count))setConversations(scoped);
      setUnread(new Map(unreadRows.map((entry) => [entry.conversation_id, entry])));
      setSelectedId((current) =>
        (scoped.some(row=>row.id===current) ? current : scoped[0]?.id) || "",
      );
      setError("");
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : String(loadError));
    }
  }, [archived, agentId, projectId, validScope]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(load, 8_000);
    return () => window.clearInterval(timer);
  }, [load, eventRevision]);
  useEffect(() => { if (listEventRevision) void load(); }, [listEventRevision, load]);

  const selected = useMemo(
    () => conversations.find((conversation) => conversation.id === selectedId) ?? null,
    [conversations, selectedId],
  );

  const adopt = async (conversation: Conversation) => {
    setArchived(false);
    setCreating(false);
    setTitle("");
    setConversations((current) => [conversation, ...current.filter((item) => item.id !== conversation.id)]);
    setSelectedId(conversation.id);
    setDetailOpen(true);
    if (!archived) await load();
  };

  const create = async () => {
    if (!validAgent || saving) return;
    setSaving(true);
    setError("");
    try {
      await adopt(await apiPost<Conversation>(
        "/chats",
        fixedAgentConversationInput(instanceId, projectId, title),
        projectId,
      ));
    } catch (createError) {
      setError(createError instanceof Error ? createError.message : String(createError));
    } finally {
      setSaving(false);
    }
  };

  // The project picker reuses the page's dialog, which needs the agent directory.
  const openCreate = () => {
    setCreating(true);
    if (!allAgents || pickerAgents) return;
    conversationsClient.agents().then(
      (list) => { setPickerAgents(list); setPickerError(""); },
      (loadError) => setPickerError(loadError instanceof Error ? loadError.message : String(loadError)),
    );
  };

  if (!projectId || !validScope) {
    return (
      <div className="grid h-full place-items-center bg-bg p-6 text-center text-sm text-text-muted">
        {t("chat.invalidScope")}
      </div>
    );
  }

  const showList = !stacked || !detailOpen || !selected;
  const showChat = !stacked || (detailOpen && Boolean(selected));
  return (
    <div
      ref={attachFrame}
      data-conversations-layout={stacked ? "stacked" : wideLayout ? "wide" : "split"}
      className="grid h-full min-h-0 overflow-hidden bg-bg text-text"
      style={layout}
    >
      {showList && <aside
        className="flex min-h-0 flex-col"
        style={wideLayout
          ? { borderRight: "1px solid var(--color-border)" }
          : stacked ? undefined : { borderBottom: "1px solid var(--color-border)" }}
      >
        <div className="flex shrink-0 items-center gap-2 border-b border-border p-3">
          {showCreate && (
            <button
              type="button"
              onClick={openCreate}
              className="rounded bg-accent px-2.5 py-1.5 text-xs font-semibold text-bg"
            >
              {t("chat.new")}
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
            {archived ? t("common.active") : t("common.archived")}
          </button>
        </div>
        {!allAgents && showCreate && creating && (
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
              placeholder={t("chat.titlePlaceholder")}
              autoFocus
              className="w-full rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text focus:border-accent focus:outline-none"
            />
            <div className="flex gap-2">
              <button type="submit" disabled={saving} className="rounded bg-accent px-2.5 py-1.5 text-xs font-semibold text-bg disabled:opacity-40">
                {saving ? t("common.creating") : t("common.create")}
              </button>
              <button type="button" disabled={saving} onClick={() => setCreating(false)} className="rounded px-2.5 py-1.5 text-xs text-text-muted hover:bg-bg-input">
                {t("common.cancel")}
              </button>
            </div>
          </form>
        )}
        {error && <p className="shrink-0 border-b border-border px-3 py-2 text-xs text-error">{error}</p>}
        <div className="min-h-0 flex-1 overflow-auto">
          {conversations.length === 0 ? (
            <p className="p-5 text-center text-xs text-text-muted">
              {archived ? t("chat.noArchived") : t(allAgents ? "chat.noConversations" : "chat.noAgentConversations")}
            </p>
          ) : (
            <ul className="m-0 list-none divide-y divide-border p-0">
              <li><MoreConversations path={conversationListPath(archived,agentId)} projectId={projectId} rows={conversations} onRows={setConversations}/></li>
              {conversations.map((conversation) => {
                const unreadCount = unread.get(conversation.id)?.unread ?? 0;
                return (
                  <li key={conversation.id}>
                    <ThreadActionRow conversation={conversation} unread={unreadCount>0} latestId={unread.get(conversation.id)?.latest_id} enabled={widgetSettings?.thread_context_menu!==false} onChanged={()=>void load()} renderDetails={close=><DetailsDialog open conversation={conversation} agents={appearanceAgents} onClose={close} onChanged={()=>void load()} onRemoved={()=>{close();void load()}}/>}>
                    <button
                      type="button"
                      data-conversation-id={conversation.id}
                      onClick={(event) => {
                        returnFocusRef.current = event.currentTarget;
                        setSelectedId(conversation.id);
                        setDetailOpen(true);
                      }}
                      className={`w-full border-l-2 px-3 py-2.5 text-left ${conversation.id === selectedId ? "border-accent bg-bg-hover" : "border-transparent hover:bg-bg-hover"}`}
                    >
                      <div className="thread-row-heading flex items-center gap-2">
                        {showsAgentAvatar(avatarMode, "threads") && <ConversationIdentity conversation={conversation} agents={appearanceAgents}/>}
                        <span className="min-w-0 flex-1 truncate text-sm font-medium text-text">{conversation.title}</span>
                        <ConversationActivityIndicator active={activeConversations.has(conversation.id)} />
                        {!(conversation.id === selectedId && showChat) && <ConversationUnreadIndicator unread={shouldShowConversationUnreadIndicator(unreadCount > 0, activeConversations.has(conversation.id))} />}
                      </div>
                      <div className="mt-1 flex items-center gap-2 text-xs text-text-dim">
                        {allAgents && (
                          <span className="min-w-0 truncate">
                            {conversation.lead_agent_name || t("chat.agentName", { id: String(conversation.lead_agent_id) })}
                          </span>
                        )}
                        {conversation.kind === "room" && <span>{t("chat.room")}</span>}
                        <span className="ml-auto shrink-0">{relativeTime(conversation.updated_at)}</span>
                      </div>
                    </button>
                    </ThreadActionRow>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
        {conversationsHref && (
          <a
            href={conversationsHref}
            className="flex shrink-0 items-center justify-between gap-2 border-t border-border px-3 py-2 text-xs text-text-muted no-underline hover:bg-bg-hover hover:text-text"
          >
            <span className="truncate">{t("chat.openFullView")}</span>
            <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M5 12h14 M13 6l6 6-6 6"/></svg>
          </a>
        )}
      </aside>}

      {showChat && (selected ? (
        <ConversationChat ref={composerRef} key={`${selected.project_id}:${selected.id}`}
          host={host}
          conversation={selected}
          agentAvatars={avatarMode}
          archived={archived}
          leadingAction={stacked ? (
            <button
              type="button"
              onClick={() => setDetailOpen(false)}
              className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded text-text-muted hover:bg-bg-input hover:text-text"
              aria-label={t("panel.backToChats")}
            >
              <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg>
            </button>
          ) : undefined}
          onActed={load}
          onRemoved={() => {
            setConversations(current=>current.filter(c=>c.id!==selectedId));
            setSelectedId("");
            setDetailOpen(false);
            void load();
          }}
          emptyMessage={widgetSettings?.empty_message}
          welcomeText={widgetSettings?.welcome_text}
          suggestions={widgetSettings?.suggestions}
          contextLabel={widgetSettings?.context_label}
          onContextCleared={onContextCleared}
          showPageContext={showPageContext(widgetSettings)}
          showToolCompletion={showToolCompletion(widgetSettings)}
          showToolDuration={showToolDuration(widgetSettings)}
        />
      ) : (
        <section className="grid min-h-0 place-items-center p-6 text-center text-sm text-text-muted">
          {t(allAgents ? "chat.selectOrStart" : "chat.selectOrCreate")}
        </section>
      ))}
      {allAgents && showCreate && (
        <NewConversationDialog
          open={creating}
          projectId={projectId}
          agents={pickerAgents}
          agentsError={pickerError}
          onClose={() => setCreating(false)}
          onCreated={(conversation) => void adopt(conversation)}
        />
      )}
    </div>
  );
}


function SingleConversation({
  host,
  projectId,
  instanceId,
  eventRevision,
  widgetSettings,
  composerRef,
  onContextCleared,
}: AgentConversationsWidgetProps) {
  const { t, relativeTime } = useConversationLocalization();
  const { conversationsClient, apiGet, apiPost, apiPatch, apiDelete } = useConversationAPI();
  const validAgent = Number.isInteger(instanceId) && instanceId > 0;
  const showCreate = showNewConversation(widgetSettings);
  const avatarMode = agentAvatarMode(widgetSettings?.agent_avatars);
  const appearanceAgents = useAgentDirectory(projectId, instanceId, showsAgentAvatar(avatarMode, "threads"));
  const requestGeneration = useRef(0);
  const historyGeneration = useRef(0);
  const emptyRefreshInFlight = useRef(false);
  const previousEventRevision = useRef(eventRevision);
  const [selected, setSelected] = useState<Conversation | null>(null);
  const [agentName, setAgentName] = useState("");
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const activeHistory = useConversationActivity(projectId, instanceId, historyOpen && validAgent);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [history, setHistory] = useState<Conversation[]>([]);
  const [historyUnread,setHistoryUnread]=useState<Map<string,UnreadEntry>>(new Map());
  const [error, setError] = useState("");

  const loadLatest = useCallback(async (quiet = false) => {
    const generation = ++requestGeneration.current;
    if (!quiet) {
      setSelected(null);
      setLoading(true);
    }
    setError("");
    if (!projectId || !validAgent) {
      if (!quiet) setLoading(false);
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
      if (!quiet && generation === requestGeneration.current) setLoading(false);
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

  const refreshEmpty = useCallback(async () => {
    if (
      emptyRefreshInFlight.current ||
      selected ||
      loading ||
      !projectId ||
      !validAgent
    ) return;
    emptyRefreshInFlight.current = true;
    try {
      await loadLatest(true);
    } finally {
      emptyRefreshInFlight.current = false;
    }
  }, [loadLatest, loading, projectId, selected, validAgent]);

  // Host contribution events can announce a conversation created elsewhere.
  // Refresh only the empty state: once a conversation is open, keep it stable
  // until the user explicitly creates or selects another one.
  useEffect(() => {
    const changed = previousEventRevision.current !== eventRevision;
    previousEventRevision.current = eventRevision;
    if (changed) void refreshEmpty();
  }, [eventRevision, refreshEmpty]);

  // Event delivery is best-effort across older hosts. A small fallback poll
  // runs only while empty and stops immediately when a conversation appears.
  useEffect(() => {
    if (selected || loading || !projectId || !validAgent) return;
    const timer = window.setInterval(
      () => void refreshEmpty(),
      EMPTY_CONVERSATION_REFRESH_MS,
    );
    return () => window.clearInterval(timer);
  }, [loading, projectId, refreshEmpty, selected, validAgent]);

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

  const historyChanged = async (conversation:Conversation,updated?:Conversation,removed=false,read=false) => {
    try {
    if(read||removed)setHistoryUnread(current=>{const next=new Map(current);next.delete(conversation.id);return next});
    if(removed||updated?.archived_at){
      setHistory(current=>current.filter(row=>row.id!==conversation.id));
      if(selected?.id===conversation.id)await loadLatest();
      return;
    }
    const next=updated??await apiGet<Conversation>(`/chats?id=${encodeURIComponent(conversation.id)}`,projectId);
    if(next.archived_at){setHistory(current=>current.filter(row=>row.id!==next.id));if(selected?.id===next.id)await loadLatest();return;}
    setHistory(current=>current.map(row=>row.id===next.id?next:row));
    setSelected(current=>current?.id===next.id?next:current);
    } catch(error) {setError(error instanceof Error?error.message:String(error));}
  };

  const openHistory = async () => {
    const generation = ++historyGeneration.current;
    setHistoryOpen(true);
    setHistoryLoading(true);
    setError("");
    try {
      const [rows,unreadRows] = await Promise.all([apiGet<Conversation[]>(singleConversationListPath(instanceId, 50), projectId),apiGet<UnreadEntry[]>(appendAgentScope("/unread-summary",instanceId),projectId)]);
      if (generation === historyGeneration.current) {setHistory(rows);setHistoryUnread(new Map(unreadRows.map(row=>[row.conversation_id,row])))};
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
        {t("chat.invalidScope")}
      </div>
    );
  }

  const displayAgentName = agentName || t("chat.agentName", { id: String(instanceId) });

  return (
    <div className="relative flex h-full min-h-0 flex-col overflow-hidden bg-bg text-text">
      {error && selected && (
        <p className="shrink-0 border-b border-border px-4 py-2 text-xs text-error">{error}</p>
      )}
      {loading ? (
        <section className="grid min-h-0 flex-1 place-items-center p-6 text-sm text-text-muted">
          {t("chat.loading")}
        </section>
      ) : selected ? (
        <ConversationChat ref={composerRef} key={`${selected.project_id}:${selected.id}`}
          host={host}
          conversation={selected}
          agentAvatars={avatarMode}
          archived={false}
          emptyMessage={widgetSettings?.empty_message}
          welcomeText={widgetSettings?.welcome_text}
          suggestions={widgetSettings?.suggestions}
          contextLabel={widgetSettings?.context_label}
          onContextCleared={onContextCleared}
          showPageContext={showPageContext(widgetSettings)}
          showToolCompletion={showToolCompletion(widgetSettings)}
          showToolDuration={showToolDuration(widgetSettings)}
          headerActions={(
            <>
              {showCreate && (
                <button
                  type="button"
                  onClick={() => void create()}
                  disabled={creating}
                  className="rounded px-2 py-1.5 text-xs text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-40"
                >
                  {creating ? t("chat.starting") : t("chat.new")}
                </button>
              )}
              <button
                type="button"
                onClick={() => void openHistory()}
                className="rounded px-2 py-1.5 text-xs text-text-muted hover:bg-bg-input hover:text-text"
              >
                {t("chat.history")}
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
              {showCreate ? t("chat.startWith", { name: displayAgentName }) : t("chat.noneWith", { name: displayAgentName })}
            </h2>
            <p className="mt-1 text-xs text-text-muted">{t("chat.historyHint")}</p>
          </div>
          {showCreate && (
            <button
              type="button"
              onClick={() => void create()}
              disabled={creating}
              className="rounded bg-accent px-3 py-2 text-xs font-semibold text-bg disabled:opacity-40"
            >
              {creating ? t("chat.starting") : t("chat.start")}
            </button>
          )}
          {error && (
            <div className="space-y-2">
              <p className="text-xs text-error">{error}</p>
              <button type="button" onClick={() => void loadLatest()} className="text-xs text-text-muted underline hover:text-text">
                {t("common.tryAgain")}
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
            aria-label={t("chat.closeHistory")}
          />
          <aside className="relative flex h-full w-full max-w-sm flex-col border-l border-border bg-bg-card shadow-xl">
            <div className="flex shrink-0 items-center border-b border-border px-4 py-3">
              <h2 className="text-sm font-semibold text-text">{t("chat.historyTitle")}</h2>
              <button
                type="button"
                onClick={() => {
                  historyGeneration.current += 1;
                  setHistoryOpen(false);
                }}
                className="ml-auto rounded px-2 py-1 text-xs text-text-muted hover:bg-bg-input hover:text-text"
              >
                {t("common.close")}
              </button>
            </div>
            <div className="min-h-0 flex-1 overflow-auto">
              {historyLoading ? (
                <p className="p-5 text-center text-xs text-text-muted">{t("chat.loadingHistory")}</p>
              ) : history.length === 0 ? (
                <p className="p-5 text-center text-xs text-text-muted">{t("chat.noEarlier")}</p>
              ) : (
                <ul className="divide-y divide-border">
                  <li><MoreConversations path={singleConversationListPath(instanceId,50)} projectId={projectId} rows={history} onRows={setHistory}/></li>
                  {history.map((conversation) => (
                    <li key={conversation.id}>
                      <ThreadActionRow conversation={conversation} unread={(historyUnread.get(conversation.id)?.unread??0)>0} latestId={historyUnread.get(conversation.id)?.latest_id} enabled={widgetSettings?.thread_context_menu!==false} onChanged={(updated,removed,read)=>void historyChanged(conversation,updated,removed,read)} renderDetails={close=><DetailsDialog open conversation={conversation} agents={appearanceAgents} onClose={close} onChanged={()=>void historyChanged(conversation)} onRemoved={()=>{close();void historyChanged(conversation,undefined,true)}}/>}>
                      <button
                        type="button"
                        onClick={() => {
                          requestGeneration.current += 1;
                          setSelected(conversation);
                          setHistoryOpen(false);
                        }}
                        className={`w-full px-4 py-3 text-left hover:bg-bg-hover ${conversation.id === selected?.id ? "bg-bg-hover" : ""}`}
                      >
                        <span className="thread-row-heading flex items-center gap-2 text-sm font-medium text-text">
                          {showsAgentAvatar(avatarMode, "threads") && <ConversationIdentity conversation={conversation} agents={appearanceAgents}/>}
                          <span className="min-w-0 flex-1 truncate">{conversation.title}</span>
                          <ConversationActivityIndicator active={activeHistory.has(conversation.id)} />
                        </span>
                        <span className="mt-1 block text-xs text-text-dim">{relativeTime(conversation.updated_at)}</span>
                      </button>
                      </ThreadActionRow>
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

export interface ProjectConversationsWidgetProps extends Omit<AgentConversationsWidgetProps, "instanceId"> {
  conversationsHref?: string;
}

/** Every agent's conversations in one widget: the page's list, chat and new-conversation picker. */
export function ProjectConversationsWidget(props: ProjectConversationsWidgetProps) {
  return <ConversationBrowser {...props} instanceId={0} allAgents />;
}

export default function AgentConversationsWidget(props: AgentConversationsWidgetProps) {
  const scopedProps = props;
  return conversationDisplayMode(props.widgetSettings, props.slot) === "single"
    ? <SingleConversation key={`${props.projectId}:${props.instanceId}`} {...scopedProps} />
    : <ConversationBrowser {...scopedProps} />;
}
