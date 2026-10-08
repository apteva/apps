import { AgentMark } from "./AgentMark";
import type { AgentInfo, Conversation } from "./types";
export type AgentAvatarMode = "hidden" | "header" | "threads" | "both";
export function agentAvatarMode(value?: AgentAvatarMode, fallback: AgentAvatarMode = "hidden"): AgentAvatarMode {
  return ["hidden", "header", "threads", "both"].includes(value || "") ? value! : fallback;
}
export function showsAgentAvatar(mode: AgentAvatarMode, location: "header" | "threads"): boolean {
  return mode === "both" || mode === location;
}
/** Identity only: online/unread/working state belongs to its existing indicator. */
export function ConversationIdentity({ conversation, agents, size = "sm" }: {
  conversation: Pick<Conversation, "kind" | "lead_agent_id">; agents: readonly AgentInfo[]; size?: "sm" | "md";
}) {
  if (conversation.kind === "room") return <span data-conversation-icon="room" aria-hidden="true"
    className={`${size === "sm" ? "h-8 w-8 rounded-lg" : "h-10 w-10 rounded-xl"} inline-flex shrink-0 items-center justify-center`}
    style={{ color: "var(--accent)", background: "var(--bg-input)", border: "1px solid var(--border)" }}>
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
      <circle cx="9" cy="8" r="3"/><path d="M3 21v-2a6 6 0 0 1 12 0v2M16 5a3 3 0 0 1 0 6M17 15a5 5 0 0 1 4 4v2"/>
    </svg>
  </span>;
  const agent = agents.find(item => item.id === conversation.lead_agent_id);
  return <AgentMark icon={agent?.icon} color={agent?.icon_color} size={size}/>;
}
