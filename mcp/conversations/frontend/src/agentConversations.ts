export type ConversationDisplayMode = "browser" | "single";

export interface AgentConversationWidgetSettings {
  display_mode?: ConversationDisplayMode;
  show_new_conversation?: boolean;
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
): ConversationDisplayMode {
  return settings?.display_mode === "single" ? "single" : "browser";
}

export function showNewConversation(
  settings?: AgentConversationWidgetSettings,
): boolean {
  return settings?.show_new_conversation !== false;
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

export function agentConversationWidgetLayout(wide: boolean) {
  return {
    gridTemplateColumns: wide ? "250px minmax(0,1fr)" : "minmax(0,1fr)",
    gridTemplateRows: wide
      ? "minmax(0,1fr)"
      : "minmax(150px,34%) minmax(0,1fr)",
  };
}
