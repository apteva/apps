import type { Message, ToolActivity } from "./types";
import type { ChatTimelineItem } from "./toolActivityModel";

export interface PendingResponse {
  optimistic?: boolean;
  agentId?: number;
  threadId?: string;
  afterMessageId?: number;
  createdAt?: number;
}

// An acknowledgement tracks the whole response, not an LLM phase. Keep it
// for pause/send controls, but let visible calls own the progress indicator.
export function pendingResponsePhase(bubble: PendingResponse, activities: ToolActivity[], messages: Message[]): "thinking" | "preparing" | null {
  const userMessage = messages.find(message => message.id === bubble.afterMessageId);
  const since = userMessage?.role === "user" ? Date.parse(userMessage.created_at) : bubble.createdAt ?? Infinity;
  const relevant = activities.filter(activity => activity.agent_id === bubble.agentId
    && (!bubble.threadId || activity.thread_id === bubble.threadId));
  if (relevant.some(activity => activity.status === "running")) return null;
  return relevant.some(activity => Date.parse(activity.started_at) >= since) ? "preparing" : "thinking";
}

// The latest group belonging to this response owns its indicator even when
// an intermediate reply or another participant follows it in the transcript.
export function responseToolGroup(response: PendingResponse, timeline: ChatTimelineItem[], messages: Message[]): string | undefined {
  const anchor = messages.find(message => message.id === response.afterMessageId);
  const since = !response.optimistic && anchor?.role === "user" ? Date.parse(anchor.created_at) : response.createdAt ?? Infinity;
  for (let i = timeline.length - 1; i >= 0; i--) {
    const item = timeline[i]!;
    const tools = item.kind === "toolGroup" ? item.tools : item.kind === "tool" ? [item.tool] : [];
    if (tools.some(tool => tool.agentId === response.agentId
      && (!response.threadId || tool.threadId === response.threadId) && tool.startedAt >= since)) return item.key;
  }
}
