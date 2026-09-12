// Timeline grouping ported from the dashboard toolActivityModel.
import type { Message as ChatMessageRow } from "./types";
export type ToolActivityState = "preparing" | "running" | "done" | "interrupted";

export interface ToolActivity {
  /** Stable first-seen UI identity. Never replaced when the core later
   * supplies a provider call id. */
  id: string;
  /** Provider/core correlation id, when available. */
  callId?: string;
  /** Agent/core that emitted the event. Required because room participants
   * can use the same thread name and provider call ID independently. */
  agentId: number;
  threadId: string;
  name: string;
  reason: string;
  state: ToolActivityState;
  success?: boolean;
  durationMs?: number;
  startedAt: number;
  finishedAt?: number;
}


export const TOOL_GROUP_IDLE_GAP_MS = 30_000;
export const MESSAGE_GROUP_GAP_MS = 5 * 60_000;
export const TIME_MARKER_GAP_MS = 15 * 60_000;
export type ChatTimelineItem =
  | { kind: "message"; key: string; ts: number; endTs: number; message: ChatMessageRow; compactBefore: boolean }
  | { kind: "tool"; key: string; ts: number; endTs: number; tool: ToolActivity }
  | { kind: "toolGroup"; key: string; ts: number; endTs: number; tools: ToolActivity[]; parallel: boolean }
  | { kind: "day"; key: string; ts: number }
  | { kind: "time"; key: string; ts: number };

type ContentTimelineItem = Exclude<ChatTimelineItem, { kind: "day" | "time" }>;

function localDayKey(timestamp: number): string {
  const date = new Date(timestamp);
  return `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
}

function toolsOverlap(tools: ToolActivity[]): boolean {
  if (tools.length < 2) return false;
  const sorted = [...tools].sort((a, b) => a.startedAt - b.startedAt);
  let latestFinish = sorted[0]!.finishedAt ?? Number.POSITIVE_INFINITY;
  for (let index = 1; index < sorted.length; index += 1) {
    const tool = sorted[index]!;
    if (tool.startedAt <= latestFinish) return true;
    latestFinish = Math.max(latestFinish, tool.finishedAt ?? Number.POSITIVE_INFINITY);
  }
  return false;
}

export function buildChatTimeline(
  messages: ChatMessageRow[],
  tools: Iterable<ToolActivity>,
  now = Date.now(),
): ChatTimelineItem[] {
  type RawItem =
    | { kind: "message"; ts: number; message: ChatMessageRow }
    | { kind: "tool"; ts: number; tool: ToolActivity };
  const raw: RawItem[] = [
    ...messages.map((message) => ({
      kind: "message" as const,
      ts: Date.parse(message.created_at) || 0,
      message,
    })),
    ...[...tools].map((tool) => ({ kind: "tool" as const, ts: tool.startedAt, tool })),
  ];
  raw.sort((a, b) => {
    if (a.ts !== b.ts) return a.ts - b.ts;
    if (a.kind !== b.kind) return a.kind === "message" ? -1 : 1;
    if (a.kind === "message" && b.kind === "message") return a.message.id - b.message.id;
    if (a.kind === "tool" && b.kind === "tool") return a.tool.id.localeCompare(b.tool.id);
    return 0;
  });

  const content: ContentTimelineItem[] = [];
  let pendingTools: ToolActivity[] = [];
  const flushTools = () => {
    if (pendingTools.length === 0) return;
    const first = pendingTools[0]!;
    content.push({
      kind: "toolGroup",
      key: `tool-group:${first.id}:${first.startedAt}`,
      ts: first.startedAt,
      endTs: Math.max(...pendingTools.map((tool) => tool.finishedAt || tool.startedAt)),
      tools: pendingTools,
      parallel: toolsOverlap(pendingTools),
    });
    pendingTools = [];
  };

  for (const item of raw) {
    if (item.kind === "message") {
      flushTools();
      const previous = content[content.length - 1];
      const compactBefore =
        previous?.kind === "message" &&
        previous.message.role === item.message.role &&
        item.ts - previous.ts <= MESSAGE_GROUP_GAP_MS &&
        !previous.message.components?.length &&
        !item.message.components?.length;
      content.push({
        kind: "message",
        key: `message:${item.message.id}`,
        ts: item.ts,
        endTs: item.ts,
        message: item.message,
        compactBefore,
      });
      continue;
    }
    const previousTool = pendingTools[pendingTools.length - 1];
    if (previousTool && item.tool.startedAt - previousTool.startedAt > TOOL_GROUP_IDLE_GAP_MS) {
      flushTools();
    }
    pendingTools.push(item.tool);
  }
  flushTools();

  if (content.length === 0) return [];
  const dayKeys = new Set(content.map((item) => localDayKey(item.ts)));
  const showDayMarkers = dayKeys.size > 1 || !dayKeys.has(localDayKey(now));
  const timeline: ChatTimelineItem[] = [];
  let previousContent: ContentTimelineItem | undefined;
  for (const item of content) {
    const dayChanged = !previousContent || localDayKey(previousContent.ts) !== localDayKey(item.ts);
    if (dayChanged && showDayMarkers) {
      timeline.push({ kind: "day", key: `day:${localDayKey(item.ts)}`, ts: item.ts });
    } else if (previousContent && item.ts - previousContent.endTs >= TIME_MARKER_GAP_MS) {
      timeline.push({ kind: "time", key: `time:${item.key}`, ts: item.ts });
    }
    timeline.push(item);
    previousContent = item;
  }
  return timeline;
}
