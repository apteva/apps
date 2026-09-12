// Port of dashboard/src/components/chat/ToolActivity.tsx; keep presentation in parity.
import { useEffect, useMemo, useState } from "react";
import { useToolTranslation as useTranslation } from "./toolActivityAdapter";
import { AppIcon } from "./toolAppIcon";
import type { ToolActivity } from "./toolActivityModel";
import {
  resolveToolVisual,
  type ToolGlyph,
  type ToolVisual,
  type ToolVisualRegistry,
} from "./toolVisuals";

interface ToolActivityProps {
  tools: ToolActivity[];
  parallel?: boolean;
  continuing?: boolean;
  expanded?: boolean;
  onToggle?: () => void;
  registry: ToolVisualRegistry;
  detailsId?: string;
}

type VisualState = "preparing" | "running" | "done" | "failed" | "interrupted";

function visualState(tool: ToolActivity): VisualState {
  if (tool.state !== "done") return tool.state;
  return tool.success === false ? "failed" : "done";
}

function durationLabel(milliseconds?: number): string {
  if (milliseconds === undefined || milliseconds < 0) return "";
  return milliseconds >= 1000 ? `${(milliseconds / 1000).toFixed(1)}s` : `${milliseconds}ms`;
}

function stateLabel(tool: ToolActivity, t: (key: string, options?: Record<string, unknown>) => string): string {
  const state = visualState(tool);
  if (state === "interrupted") return t("tool.interrupted");
  if (state === "preparing") return t("chat.panel.toolPreparing");
  if (state === "running") return t("chat.panel.toolRunning");
  if (state === "failed") return t("chat.panel.toolFailed");
  return t("chat.panel.toolDone");
}

function reasonLabel(tool: ToolActivity, t: (key: string, options?: Record<string, unknown>) => string): string {
  if (tool.reason.trim()) return tool.reason.trim();
  const state = visualState(tool);
  if (state === "interrupted") return t("tool.interrupted");
  if (state === "preparing") return t("chat.panel.toolReasonPreparing");
  if (state === "running") return t("chat.panel.toolReasonRunning");
  if (state === "failed") return t("chat.panel.toolReasonFailed");
  return t("chat.panel.toolReasonDone");
}

function aggregateStatus(
  tools: ToolActivity[],
  t: (key: string, options?: Record<string, unknown>) => string,
): { text: string; state: VisualState } {
  const counts = { preparing: 0, running: 0, done: 0, failed: 0, interrupted: 0 };
  for (const tool of tools) counts[visualState(tool)] += 1;
  const parts = [
    counts.interrupted ? t("tool.interrupted") : "",
    counts.preparing ? t("chat.panel.toolsPreparingCount", { count: counts.preparing }) : "",
    counts.running ? t("chat.panel.toolsRunningCount", { count: counts.running }) : "",
    counts.failed ? t("chat.panel.toolsFailedCount", { count: counts.failed }) : "",
  ].filter(Boolean);
  if (parts.length === 0) parts.push(t("chat.panel.toolsCompleted"));
  const state: VisualState = counts.running || counts.preparing
    ? "running"
    : counts.failed
      ? "failed"
      : counts.interrupted ? "interrupted" : "done";
  return { text: parts.join(" · "), state };
}

function summaryFocusTool(tools: ToolActivity[]): ToolActivity {
  const active = tools.filter((tool) => tool.state === "preparing" || tool.state === "running");
  const candidates = active.length > 0 ? active : tools;
  return candidates.reduce((latest, tool) => {
    const latestTime = active.length > 0
      ? latest.startedAt
      : latest.finishedAt ?? latest.startedAt;
    const toolTime = active.length > 0
      ? tool.startedAt
      : tool.finishedAt ?? tool.startedAt;
    // Prefer the later item on a timestamp tie: the array order is the
    // stable arrival order, so it best represents what the operator saw last.
    return toolTime >= latestTime ? tool : latest;
  });
}

export function ChatToolActivity({
  tools,
  parallel = false,
  continuing = false,
  expanded = false,
  onToggle,
  registry,
  detailsId,
}: ToolActivityProps) {
  const { t } = useTranslation();
  if (tools.length === 0) return null;

  const grouped = tools.length > 1;
  const status = grouped
    ? aggregateStatus(tools, t)
    : { text: stateLabel(tools[0]!, t), state: visualState(tools[0]!) };
  const title = grouped
    ? parallel
      ? t("chat.panel.parallelToolCalls", { count: tools.length })
      : t("chat.panel.toolCalls", { count: tools.length })
    : reasonLabel(tools[0]!, t);
  const focusTool = summaryFocusTool(tools);
  const focusReason = reasonLabel(focusTool, t);
  const copyIsActive =
    continuing || status.state === "preparing" || status.state === "running";
  const failedCount = tools.filter((tool) => visualState(tool) === "failed").length;
  const visibleFailure = failedCount > 0
    ? grouped
      ? t("chat.panel.toolsFailedCount", { count: failedCount })
      : stateLabel(tools[0]!, t)
    : "";
  const allSucceeded = !continuing && tools.every((tool) => visualState(tool) === "done");
  const remainingCount = tools.length - 1;
  const resolvedDetailsId = detailsId || `chat-tool-details-${tools[0]!.id.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
  const accessibleSummary = `${title}, ${focusReason}, ${status.text}${continuing ? `, ${t("chat.panel.startingResponse")}` : ""}`;

  return (
    <section
      className={`chat-tool-activity min-w-0 py-0.5 ${continuing ? "chat-tool-activity-continuing" : ""}`}
      aria-label={accessibleSummary}
    >
      <button
        type="button"
        className={`grid min-h-9 w-full min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 rounded-lg px-1 text-left ${
          grouped
            ? "hover:bg-bg-hover/60 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent"
            : "cursor-default"
        }`}
        aria-expanded={grouped ? expanded : undefined}
        aria-controls={grouped ? resolvedDetailsId : undefined}
        aria-disabled={grouped ? undefined : true}
        aria-busy={copyIsActive || undefined}
        tabIndex={grouped ? 0 : -1}
        onClick={grouped ? onToggle : undefined}
        title={grouped ? `${title} · ${expanded ? t("chat.panel.hideToolCalls") : t("chat.panel.showToolCalls")}` : focusReason}
      >
        <ToolIconStack tools={tools} registry={registry} continuing={continuing} />
        <span className="flex min-w-0 flex-col gap-0.5 sm:flex-row sm:items-center sm:gap-2">
          <span className="flex min-w-0 items-center gap-1.5">
            <span
              className={`chat-tool-copy truncate text-[13px] font-medium leading-5 ${copyIsActive ? "chat-tool-copy-running" : ""}`}
              title={focusReason}
            >
              {focusReason}
            </span>
            {remainingCount > 0 && (
              <span className="shrink-0 text-[11px] font-medium text-text-muted sm:text-xs">+{remainingCount}</span>
            )}
          </span>
          {visibleFailure ? (
            <span className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-[10px] font-medium uppercase tracking-wide chat-tool-failed-text sm:text-[11px]">
              <FailureIcon />
              <span>{visibleFailure}</span>
            </span>
          ) : allSucceeded ? (
            <span
              className="inline-flex shrink-0 items-center text-green"
              title={status.text}
              aria-hidden="true"
            >
              <CheckIcon />
            </span>
          ) : null}
        </span>
        {grouped
          ? <ChevronIcon expanded={expanded} />
          : <span className="h-4 w-4 shrink-0" aria-hidden="true" />}
      </button>
      {grouped && expanded && (
        <div id={resolvedDetailsId} className="mt-1 grid min-w-0 sm:pl-9">
          {tools.map((tool) => (
            <ToolCallRow key={tool.id} tool={tool} registry={registry} />
          ))}
        </div>
      )}
    </section>
  );
}

function ToolIconStack({
  tools,
  registry,
  continuing = false,
}: {
  tools: ToolActivity[];
  registry: ToolVisualRegistry;
  continuing?: boolean;
}) {
  const sources: Array<{ tool: ToolActivity; visual: ToolVisual }> = [];
  for (const tool of tools) {
    const visual = resolveToolVisual(tool.name, registry);
    const existing = sources.find((source) => source.visual.key === visual.key);
    if (existing) {
      // Preserve first-seen source ordering, but let any active call for that
      // source drive the single representative icon's running state.
      if (existing.tool.state === "done" && tool.state !== "done") existing.tool = tool;
      continue;
    }
    sources.push({ tool, visual });
  }
  const visible = sources.slice(0, 4);
  const extra = Math.max(0, sources.length - visible.length);
  return (
    <span
      className={`flex min-w-[1.9rem] items-center py-0.5 pl-0.5 ${continuing ? "chat-tool-icon-stack-running" : ""}`}
      aria-hidden="true"
    >
      {visible.map(({ tool, visual }, index) => (
        <span key={visual.key} className={index === 0 ? "relative" : "relative -ml-1.5"} style={{ zIndex: visible.length - index }}>
          <ToolSourceIcon tool={tool} visual={visual} />
        </span>
      ))}
      {extra > 0 && (
        <span className="relative -ml-1.5 inline-flex h-7 min-w-7 items-center justify-center rounded-md bg-bg-hover px-1 text-[10px] font-semibold text-text-muted">
          +{extra}
        </span>
      )}
    </span>
  );
}

function ToolCallRow({
  tool,
  registry,
  standalone = false,
}: {
  tool: ToolActivity;
  registry: ToolVisualRegistry;
  standalone?: boolean;
}) {
  const { t } = useTranslation();
  const visual = useMemo(() => resolveToolVisual(tool.name, registry), [tool.name, registry]);
  const state = visualState(tool);
  const duration = durationLabel(tool.durationMs);
  const stateText = stateLabel(tool, t);
  const reason = reasonLabel(tool, t);
  return (
    <div
      className={`grid min-h-10 min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 py-0.5 ${
        standalone ? "chat-tool-activity px-1" : "border-t border-border/60 first:border-t-0"
      }`}
      aria-label={`${reason}, ${stateText}${duration ? `, ${duration}` : ""}`}
    >
      <ToolSourceIcon tool={tool} visual={visual} />
      <span className={`chat-tool-copy min-w-0 [overflow-wrap:anywhere] text-[13px] font-medium leading-5 ${
        state === "preparing" || state === "running" ? "chat-tool-copy-running" : ""
      }`}>
        {reason}
      </span>
      <span className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-[10px] font-medium uppercase tracking-wide sm:text-[11px]">
        {state === "done" && (
          <span className="inline-flex text-green" title={stateText} aria-hidden="true">
            <CheckIcon />
          </span>
        )}
        {state === "failed" && (
          <span className="inline-flex items-center gap-1 chat-tool-failed-text">
            <FailureIcon />
            <span>{stateText}</span>
          </span>
        )}
        {duration && <span className="text-text-dim">{duration}</span>}
      </span>
    </div>
  );
}

function ToolSourceIcon({
  tool,
  visual,
}: {
  tool: ToolActivity;
  visual: ToolVisual;
}) {
  const canRenderImage = !!visual.iconUrl && /^(https?:|data:|\/)/.test(visual.iconUrl);
  const state = visualState(tool);
  const stateClass = state === "running" || state === "preparing"
    ? "chat-tool-icon-running"
    : "";
  return (
    <span
      className={`chat-tool-icon relative inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-bg-hover text-text-muted ${stateClass}`}
      title={visual.label}
      aria-hidden="true"
    >
      {canRenderImage ? (
        <AppIcon
          src={visual.iconUrl}
          iconStyle={visual.iconStyle}
          name={visual.label}
          size="sm"
          framed={false}
          className="text-accent"
        />
      ) : (
        <ToolGlyphIcon glyph={visual.glyph} />
      )}
    </span>
  );
}

function ToolGlyphIcon({ glyph }: { glyph: ToolGlyph }) {
  const common = {
    viewBox: "0 0 24 24",
    width: 16,
    height: 16,
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.8,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
  };
  if (glyph === "agent") return <svg {...common}><circle cx="12" cy="8" r="3"/><path d="M6.5 19c.8-3.4 2.6-5 5.5-5s4.7 1.6 5.5 5"/><path d="M18 6h3m-1.5-1.5v3"/></svg>;
  if (glyph === "chart") return <svg {...common}><path d="M4 19V9m6 10V5m6 14v-7m4 7H2"/></svg>;
  if (glyph === "document") return <svg {...common}><path d="M6 3h8l4 4v14H6z"/><path d="M14 3v5h5M9 13h6m-6 4h6"/></svg>;
  if (glyph === "globe") return <svg {...common}><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c3 3.2 3 14.8 0 18M12 3c-3 3.2-3 14.8 0 18"/></svg>;
  if (glyph === "memory") return <svg {...common}><path d="M9 5a3 3 0 0 0-5 2.2A3.5 3.5 0 0 0 5 14v2a3 3 0 0 0 4 2.8M15 5a3 3 0 0 1 5 2.2A3.5 3.5 0 0 1 19 14v2a3 3 0 0 1-4 2.8M9 4v16m6-16v16M9 9h6m-6 6h6"/></svg>;
  if (glyph === "message") return <svg {...common}><path d="M4 5h16v11H9l-5 4z"/><path d="M8 9h8m-8 3h5"/></svg>;
  if (glyph === "search") return <svg {...common}><circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 5 5"/></svg>;
  if (glyph === "table") return <svg {...common}><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M9 9v11"/></svg>;
  return <svg {...common}><path d="M14.7 6.3a4 4 0 0 0-5 5L4 17l3 3 5.7-5.7a4 4 0 0 0 5-5l-2.4 2.4-3-3z"/></svg>;
}

function ChevronIcon({ expanded }: { expanded: boolean }) {
  return (
    <svg viewBox="0 0 20 20" className={`h-4 w-4 shrink-0 text-text-muted transition-transform ${expanded ? "rotate-180" : ""}`} fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
      <path d="m5 7.5 5 5 5-5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function CheckIcon() {
  return <svg viewBox="0 0 16 16" className="h-3.5 w-3.5" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true"><path d="m3 8.5 3 3 7-7" strokeLinecap="round" strokeLinejoin="round" /></svg>;
}

function FailureIcon() {
  return <svg viewBox="0 0 16 16" className="h-3.5 w-3.5" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true"><path d="m4 4 8 8m0-8-8 8" strokeLinecap="round" /></svg>;
}
