import type { ComposerSuggestion } from "./composerHost";
export type ConversationDisplayMode = "browser" | "single";

export interface AgentConversationWidgetSettings {
  thread_context_menu?:boolean;
  agent_avatars?: import("./agentAppearance").AgentAvatarMode;
  composer_layout?: "auto" | "compact" | "expanded" | "single-line";
  display_mode?: ConversationDisplayMode;
  show_page_context?: boolean;
  show_new_conversation?: boolean;
  show_tool_completion?: boolean;
  show_tool_duration?: boolean;
  /** Presentation-only empty transcript text; never persisted as a message. */
  empty_message?: string;
  /** Presentation-only welcome text; never persisted as a message. */
  welcome_text?: string;
  /** Presentation-only draft starters. */
  suggestions?: ComposerSuggestion[];
  /** Optional prefix for the visible context chip. */
  context_label?: string;
}

export function appendAgentScope(path: string, instanceId: number): string {
  if (!Number.isInteger(instanceId) || instanceId <= 0) {
    throw new Error("A target agent is required.");
  }
  return `${path}${path.includes("?") ? "&" : "?"}agent_id=${encodeURIComponent(instanceId)}`;
}

export function scopedConversationListPath(archived: boolean, instanceId: number): string {
  return appendAgentScope(`/chats${archived ? "?archived=1" : ""}`, instanceId);
}

/** The project widget passes no agent and lists every agent's conversations, as the page does. */
export function conversationListPath(archived: boolean, instanceId?: number): string {
  return instanceId === undefined ? `/chats${archived ? "?archived=1" : ""}` : scopedConversationListPath(archived, instanceId);
}

export function unreadSummaryPath(instanceId?: number): string {
  return instanceId === undefined ? "/unread-summary" : appendAgentScope("/unread-summary", instanceId);
}

export function singleConversationListPath(instanceId: number, limit = 1): string {
  if (!Number.isInteger(instanceId) || instanceId <= 0) {
    throw new Error("A target agent is required.");
  }
  if (!Number.isInteger(limit) || limit <= 0 || limit > 200) {
    throw new Error("Conversation limit must be between 1 and 200.");
  }
  return `/chats?lead_agent_id=${encodeURIComponent(instanceId)}&limit=${limit}`;
}

export function selectedConversationSeenInput(
  entries: ReadonlyArray<{ conversation_id: string; latest_id: number; unread: number }>,
  conversationId: string,
): { chat_id: string; last_seen_id: number } | null {
  if (!conversationId) return null;
  const entry = entries.find((item) => item.conversation_id === conversationId);
  if (!entry || entry.unread <= 0 || entry.latest_id <= 0) return null;
  return {
    chat_id: conversationId,
    last_seen_id: entry.latest_id,
  };
}

export function conversationDisplayMode(
  settings?: AgentConversationWidgetSettings,
  slot?: string,
): ConversationDisplayMode {
  if (settings?.display_mode === "single" || settings?.display_mode === "browser") return settings.display_mode;
  return slot === "dashboard.agent_detail" ? "single" : "browser";
}

export function showNewConversation(
  settings?: AgentConversationWidgetSettings,
): boolean {
  return settings?.show_new_conversation !== false;
}

export function showPageContext(
  settings?: AgentConversationWidgetSettings,
): boolean {
  return settings?.show_page_context !== false;
}

export function showToolCompletion(settings?: AgentConversationWidgetSettings): boolean {
  return settings?.show_tool_completion === true;
}

export function showToolDuration(settings?: AgentConversationWidgetSettings): boolean {
  return settings?.show_tool_duration === true;
}

export function fixedAgentConversationInput(instanceId: number, projectId: string, title: string) {
  if (!Number.isInteger(instanceId) || instanceId <= 0) {
    throw new Error("A target agent is required.");
  }
  return {
    agent_ids: [instanceId],
    lead_agent_id: instanceId,
    title: title.trim() || undefined,
    audience: "operator",
    project_id: projectId,
  };
}

/** Stacked layouts show the list or the open chat, one at a time. */
export function agentConversationWidgetLayout(wide: boolean, stacked = false) {
  return {
    gridTemplateColumns: wide ? "250px minmax(0,1fr)" : "minmax(0,1fr)",
    gridTemplateRows: wide || stacked
      ? "minmax(0,1fr)"
      : "minmax(150px,34%) minmax(0,1fr)",
  };
}

/** Project widgets size from their own tile, which on Home is often half the page. */
export const PROJECT_WIDGET_WIDE_MIN_PX = 640;
