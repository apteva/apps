// Agent work: what each participant agent is running right now, across main,
// its conversation threads and its helpers. Operators only.
import { useEffect, useState } from "react";
import { useConversationAPI } from "./context";
import { useConversationLocalization } from "./i18n";
import type { AgentWork, AgentWorkThread, Conversation } from "./types";

type T = (key: string, params?: Record<string, unknown>) => string;

const REFRESH_MS = 10_000;
const ENDED_STATES = new Set(["done", "failed", "stopped"]);

function ended(thread: AgentWorkThread): boolean {
  return Boolean(thread.ended_at) || ENDED_STATES.has(thread.state);
}

function stateLabel(thread: AgentWorkThread, t: T, time: (v: string) => string): string {
  if (!ended(thread) && thread.current?.kind === "tool" && thread.current.tool) return t("worker.usingTool", { tool: thread.current.tool.replace(/_/g, " ") });
  if (!ended(thread) && thread.current?.kind === "model") return t("worker.thinking");
  if (thread.state === "sleeping" && thread.next_wake_at) return t("worker.state.sleepingUntil", { time: time(thread.next_wake_at) });
  const key = `worker.state.${thread.state === "running" ? "working" : thread.state}`;
  const label = t(key);
  return label === key ? thread.state : label;
}

function StateDot({ thread }: { thread: AgentWorkThread }) {
  const working = !ended(thread) && (thread.state === "working" || thread.state === "running");
  const color = thread.state === "blocked" || thread.state === "failed" ? "var(--chat-tool-failed, #ff3d24)"
    : working ? "var(--chat-tool-running, #f97316)" : "currentColor";
  return (
    <svg viewBox="0 0 8 8" width="8" height="8" className={`shrink-0 text-text-dim ${working ? "chat-thread-working-dot" : ""}`} aria-hidden="true">
      <circle cx="4" cy="4" r="3" fill={color} />
    </svg>
  );
}

export function AgentWorkSection({ conversation, agentName, onOpenConversation }: {
  conversation: Conversation;
  agentName: (id: number) => string;
  onOpenConversation?: (id: string) => void;
}) {
  const { t: translate, dateTime } = useConversationLocalization();
  const t = translate as unknown as T;
  const time = (value: string) => dateTime(value, { timeStyle: "short" });
  const { conversationsClient } = useConversationAPI();
  const [work, setWork] = useState<AgentWork | null>(null);
  const [hidden, setHidden] = useState(false);
  const operator = conversation.audience !== "public";
  useEffect(() => {
    if (!operator) return;
    let active = true;
    setWork(null); setHidden(false);
    const load = () => conversationsClient.agentWork(conversation.id)
      .then(result => { if (active) setWork(result); })
      .catch(() => { if (active) setHidden(true); });
    void load();
    const timer = window.setInterval(() => { if (document.visibilityState !== "hidden") void load(); }, REFRESH_MS);
    return () => { active = false; window.clearInterval(timer); };
  }, [conversationsClient, conversation.id, operator]);
  if (!operator || hidden) return null;
  return (
    <section aria-labelledby={`agent-work-${conversation.id}`} data-agent-work>
      <div id={`agent-work-${conversation.id}`} className="mb-2 text-xs uppercase text-text-dim">{t("agentWork.title")}</div>
      {work === null ? (
        <p className="text-xs text-text-dim">{t("agentWork.loading")}</p>
      ) : !work.available ? (
        <p className="text-xs text-text-dim">{t("agentWork.unavailable")}</p>
      ) : (
        <div className="space-y-3">
          {work.agents.filter(agent => agent.available).map(agent => (
            <AgentThreads key={agent.agent_id} name={work.agents.length > 1 ? agentName(agent.agent_id) : ""} threads={agent.threads}
              t={t} time={time} onOpenConversation={onOpenConversation} />
          ))}
        </div>
      )}
    </section>
  );
}

function AgentThreads({ name, threads, t, time, onOpenConversation }: {
  name: string; threads: AgentWorkThread[]; t: T; time: (v: string) => string; onOpenConversation?: (id: string) => void;
}) {
  const main = threads.find(thread => thread.kind === "main");
  const live = threads.filter(thread => !ended(thread));
  const chats = live.filter(thread => thread.kind === "chat" || thread.kind === "voice");
  const helpers = live.filter(thread => thread.kind === "worker");
  const finished = threads.filter(thread => ended(thread) && thread.kind !== "main");
  const paused = threads.some(thread => thread.state === "paused");
  const blocked = threads.find(thread => thread.state === "blocked");
  const byId = new Map(threads.map(thread => [thread.id, thread]));
  const parentName = (thread: AgentWorkThread) => {
    const parent = thread.parent_id ? byId.get(thread.parent_id) : undefined;
    if (!parent) return thread.parent_id || "";
    if (parent.kind === "main") return t("agentWork.main");
    return parent.title || parent.name || parent.id;
  };
  return (
    <div className="space-y-2 text-xs">
      {name && <p className="truncate font-medium text-text-muted">{name}</p>}
      {paused ? (
        <p role="status" className="rounded border border-border bg-bg-hover/40 px-2 py-1.5 text-text-muted">{t("agentWork.paused")}</p>
      ) : blocked ? (
        <p role="status" className="rounded border border-border bg-bg-hover/40 px-2 py-1.5 chat-tool-failed-text">
          {blocked.blocked_reason ? t("agentWork.blocked", { reason: blocked.blocked_reason }) : t("agentWork.blockedNoReason")}
        </p>
      ) : null}
      {main && (
        <Row thread={main} label={t("agentWork.main")} t={t} time={time}
          extra={main.next_wake_at && main.state !== "sleeping" ? t("agentWork.nextWake", { time: time(main.next_wake_at) }) : ""} />
      )}
      {chats.length > 0 && (
        <div className="space-y-1">
          <p className="text-[11px] text-text-dim">{t("agentWork.conversations")}</p>
          {chats.map(thread => (
            <Row key={thread.id} thread={thread} t={t} time={time}
              label={thread.title || thread.name || thread.id}
              current={thread.this_conversation}
              onOpen={thread.chat_id && !thread.this_conversation && onOpenConversation ? () => onOpenConversation(thread.chat_id!) : undefined} />
          ))}
        </div>
      )}
      {helpers.length > 0 && (
        <div className="space-y-1">
          <p className="text-[11px] text-text-dim">{t("agentWork.helpers")}</p>
          {helpers.map(thread => (
            <Row key={thread.id} thread={thread} t={t} time={time} label={thread.name || thread.purpose || thread.id}
              extra={parentName(thread)} title={thread.purpose} />
          ))}
        </div>
      )}
      {!chats.length && !helpers.length && <p className="text-text-dim">{t("agentWork.none")}</p>}
      {finished.length > 0 && (
        <details className="min-w-0">
          <summary className="cursor-pointer text-[11px] text-text-dim hover:text-text">{t("agentWork.ended", { count: finished.length })}</summary>
          <div className="mt-1 space-y-1">
            {finished.map(thread => (
              <Row key={`${thread.id}:${thread.ended_at ?? ""}`} thread={thread} t={t} time={time}
                label={thread.kind === "worker" ? (thread.name || thread.purpose || thread.id) : (thread.title || thread.name || thread.id)} />
            ))}
          </div>
        </details>
      )}
    </div>
  );
}

function Row({ thread, label, extra = "", current = false, onOpen, title, t, time }: {
  thread: AgentWorkThread; label: string; extra?: string; current?: boolean; onOpen?: () => void; title?: string; t: T; time: (v: string) => string;
}) {
  const state = stateLabel(thread, t, time);
  const body = (
    <>
      <StateDot thread={thread} />
      <span className="min-w-0 flex-1">
        <span className={`block truncate ${current ? "font-medium text-text" : "text-text-muted"}`}>{label}</span>
        <span className="block truncate text-[11px] text-text-dim">{[current ? t("agentWork.thisConversation") : "", state, extra].filter(Boolean).join(" · ")}</span>
      </span>
    </>
  );
  const className = `flex min-h-9 w-full min-w-0 items-center gap-2 rounded px-1.5 py-1 text-left ${current ? "bg-bg-hover" : ""}`;
  return onOpen ? (
    <button type="button" className={`${className} hover:bg-bg-input focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent`} onClick={onOpen}
      title={title || t("agentWork.open", { title: label })} data-agent-thread={thread.id}>{body}</button>
  ) : (
    <div className={className} title={title} data-agent-thread={thread.id} aria-current={current ? "true" : undefined}>{body}</div>
  );
}
