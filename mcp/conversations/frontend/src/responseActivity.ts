import type { Message, ToolActivity } from "./types";

export interface PendingResponse {
  agentId?: number;
  threadId?: string;
  afterMessageId?: number;
  createdAt?: number;
}

// An acknowledgement tracks the whole response, not an LLM phase. Keep it
// for pause/send controls, but let visible calls own the progress indicator.
export function pendingResponsePhase(bubble: PendingResponse, activities: ToolActivity[], messages: Message[]): "thinking" | "preparing" | null {
  const userMessage = messages.find(message => message.id === bubble.afterMessageId);
  const since = userMessage ? Date.parse(userMessage.created_at) : bubble.createdAt ?? Infinity;
  const relevant = activities.filter(activity => activity.agent_id === bubble.agentId
    && (!bubble.threadId || activity.thread_id === bubble.threadId));
  if (relevant.some(activity => activity.status === "running")) return null;
  return relevant.some(activity => Date.parse(activity.started_at) >= since) ? "preparing" : "thinking";
}
