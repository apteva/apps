// A worker card replaces the spawn tool row once the spawned thread is linked
// to this conversation: what the helper is doing now, and its steps on demand.
import { useEffect, useMemo, useRef, useState } from "react";
import { useConversationAPI } from "./context";
import { useConversationLocalization } from "./i18n";
import { CheckIcon, ChevronIcon, FailureIcon, ToolVisualIcon, durationLabel } from "./ToolActivity";
import { resolveToolVisual, type ToolVisual, type ToolVisualRegistry } from "./toolVisuals";
import type { ToolActivity } from "./toolActivityModel";
import type { ConversationWorker, WorkerStep } from "./types";

type T = (key: string, params?: Record<string, unknown>) => string;

const VISIBLE_STEPS = 8;
const MAX_NESTING = 2;
const RESULT_PREVIEW_CHARS = 600;
const HELPER_VISUAL: ToolVisual = { key: "native:spawn", label: "Apteva", glyph: "agent" };

export function workerUnfinished(worker: ConversationWorker): boolean {
  return !worker.ended_at && !["done", "failed", "stopped"].includes(worker.state);
}

export function workerName(worker: ConversationWorker, reason?: string): string {
  return worker.name?.trim() || worker.purpose?.trim() || reason?.trim() || worker.thread_id;
}

/** Key a spawn tool row and the worker it started share. */
export function spawnKey(agentId: number, threadId: string, callId?: string): string {
  return `${agentId}:${threadId}:${callId ?? ""}`;
}

function descendants(worker: ConversationWorker, all: ConversationWorker[]): ConversationWorker[] {
  const out: ConversationWorker[] = [];
  const queue = [worker.thread_id];
  const seen = new Set(queue);
  while (queue.length) {
    const parent = queue.shift()!;
    for (const w of all) if (w.agent_id === worker.agent_id && w.parent_thread_id === parent && w.id !== worker.id && !seen.has(w.thread_id)) {
      seen.add(w.thread_id); out.push(w); queue.push(w.thread_id);
    }
  }
  return out;
}

function directChildren(worker: ConversationWorker, all: ConversationWorker[]): ConversationWorker[] {
  // Only the newest incarnation of each child thread.
  const byThread = new Map<string, ConversationWorker>();
  for (const w of all) if (w.agent_id === worker.agent_id && w.parent_thread_id === worker.thread_id && w.id !== worker.id) {
    const prev = byThread.get(w.thread_id);
    if (!prev || prev.id < w.id) byThread.set(w.thread_id, w);
  }
  return [...byThread.values()].sort((a, b) => a.id - b.id);
}

function stateText(worker: ConversationWorker, registry: ToolVisualRegistry, t: T, time: (v: string) => string): string {
  if (worker.outcome === "lost") return t("worker.state.lost");
  if (workerUnfinished(worker) && worker.current?.kind === "tool" && worker.current.tool) {
    return t("worker.usingTool", { tool: toolLabel(worker.current.tool, registry) });
  }
  if (workerUnfinished(worker) && worker.current?.kind === "model") return t("worker.thinking");
  if (worker.state === "sleeping" && worker.next_wake_at) return t("worker.state.sleepingUntil", { time: time(worker.next_wake_at) });
  const key = `worker.state.${worker.state}`;
  const label = t(key);
  return label === key ? worker.state : label;
}

function toolLabel(tool: string, _registry?: ToolVisualRegistry): string {
  // The icon names the app; the label says what the tool does.
  return tool.replace(/^.*__/, "").replace(/_/g, " ").trim() || tool;
}

function elapsedMs(worker: ConversationWorker, now: number): number | undefined {
  const start = Date.parse(worker.started_at);
  if (!Number.isFinite(start)) return undefined;
  const end = worker.ended_at ? Date.parse(worker.ended_at) : now;
  return Number.isFinite(end) ? Math.max(0, end - start) : undefined;
}

function clockLabel(ms?: number): string {
  if (ms === undefined) return "";
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
}

function useNow(active: boolean): number {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  return now;
}

export interface WorkerCardProps {
  worker: ConversationWorker;
  workers: ConversationWorker[];
  spawnTool?: ToolActivity;
  registry: ToolVisualRegistry;
  chatId: string;
  defaultExpanded?: boolean;
  level?: number;
}

export function WorkerCard({ worker, workers, spawnTool, registry, chatId, defaultExpanded = false, level = 0 }: WorkerCardProps) {
  const { t: translate, dateTime } = useConversationLocalization();
  const t = translate as unknown as T;
  const time = (value: string) => dateTime(value, { timeStyle: "short" });
  const [expanded, setExpanded] = useState(defaultExpanded);
  const unfinished = workerUnfinished(worker);
  const now = useNow(unfinished);
  const name = workerName(worker, spawnTool?.reason);
  const status = stateText(worker, registry, t, time);
  const helpers = useMemo(() => descendants(worker, workers), [worker, workers]);
  const children = useMemo(() => directChildren(worker, workers), [worker, workers]);
  const working = unfinished && worker.state === "working";
  const visual = worker.current?.kind === "tool" && worker.current.tool && unfinished ? resolveToolVisual(worker.current.tool, registry) : HELPER_VISUAL;
  const failed = worker.state === "failed" || worker.outcome === "lost" || worker.outcome === "failed";
  const detailsId = `worker-${chatId}-${worker.id}`.replace(/[^a-zA-Z0-9_-]/g, "-");
  const [announcement, setAnnouncement] = useState("");
  const previous = useRef(unfinished);
  useEffect(() => {
    if (previous.current && !unfinished) setAnnouncement(t(failed ? "worker.announceFailed" : "worker.announceFinished", { name }));
    previous.current = unfinished;
  }, [unfinished, failed, name, t]);

  return (
    <section
      className={`chat-worker-card min-w-0 shrink-0 rounded-lg ${level > 0 ? "border-l border-border pl-2" : ""}`}
      data-worker-thread={worker.thread_id}
      data-worker-state={worker.state}
      aria-label={t("worker.cardLabel", { name, status })}
    >
      <button
        type="button"
        className="grid min-h-9 w-full min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 rounded-lg px-1 text-left hover:bg-bg-hover/60 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent"
        aria-expanded={expanded}
        aria-controls={detailsId}
        title={t("worker.toggle", { name })}
        onClick={() => setExpanded(value => !value)}
      >
        <ToolVisualIcon visual={visual} running={working} />
        <span className="flex min-w-0 flex-col gap-0.5 sm:flex-row sm:items-center sm:gap-2">
          <span className="flex min-w-0 items-center gap-1.5">
            <span className={`chat-tool-copy truncate text-[13px] font-medium leading-5 ${working ? "chat-tool-copy-running" : ""}`} title={name}>{name}</span>
            {helpers.length > 0 && <span className="shrink-0 text-[11px] font-medium text-text-muted sm:text-xs">{t("worker.moreHelpers", { count: helpers.length })}</span>}
          </span>
          <span className={`inline-flex min-w-0 items-center gap-1 text-[11px] leading-4 ${failed ? "chat-tool-failed-text" : "text-text-muted"}`}>
            {failed ? <FailureIcon /> : !unfinished ? <span className="inline-flex text-green" aria-hidden="true"><CheckIcon /></span> : null}
            <span className="truncate">{status}</span>
          </span>
        </span>
        <span className="inline-flex shrink-0 items-center gap-2">
          <span className="text-[11px] tabular-nums text-text-dim">{clockLabel(elapsedMs(worker, now))}</span>
          <ChevronIcon expanded={expanded} />
        </span>
      </button>
      <span className="sr-only" role="status" aria-live="polite">{announcement}</span>
      {expanded && (
        <div id={detailsId} className="mt-1 grid min-w-0 gap-2 pb-1 pl-1 sm:pl-9">
          <WorkerHeader worker={worker} t={t} time={time} />
          <WorkerSteps worker={worker} chatId={chatId} registry={registry} t={t} />
          {children.length > 0 && (level < MAX_NESTING ? (
            <div className="grid min-w-0 gap-1" aria-label={t("worker.nestedHelpers")}>
              {children.map(child => <WorkerCard key={child.id} worker={child} workers={workers} registry={registry} chatId={chatId} level={level + 1} />)}
            </div>
          ) : (
            <p className="text-[11px] text-text-muted">{t("worker.moreHelpers", { count: helpers.length })}</p>
          ))}
          {worker.result && <WorkerResult worker={worker} t={t} />}
        </div>
      )}
    </section>
  );
}

function WorkerHeader({ worker, t, time }: { worker: ConversationWorker; t: T; time: (v: string) => string }) {
  const parts = [worker.model ? t("worker.model", { model: worker.model }) : "", worker.started_at ? t("worker.started", { time: time(worker.started_at) }) : ""].filter(Boolean);
  return (
    <div className="grid min-w-0 gap-0.5">
      {worker.purpose && <p className="text-[13px] leading-5 text-text [overflow-wrap:anywhere]">{worker.purpose}</p>}
      {parts.length > 0 && <p className="text-[11px] text-text-muted">{parts.join(" · ")}</p>}
    </div>
  );
}

function WorkerResult({ worker, t }: { worker: ConversationWorker; t: T }) {
  const [full, setFull] = useState(false);
  const text = worker.result ?? "";
  const long = text.length > RESULT_PREVIEW_CHARS;
  return (
    <div className="grid min-w-0 gap-1 rounded-md border border-border bg-bg-hover/40 p-2">
      <p className="text-[11px] font-medium uppercase tracking-wide text-text-muted">{t("worker.result")}</p>
      <p className="whitespace-pre-wrap text-[13px] leading-5 text-text [overflow-wrap:anywhere]">{full || !long ? text : `${text.slice(0, RESULT_PREVIEW_CHARS).trimEnd()}…`}</p>
      {(long || worker.result_truncated) && (
        <div className="flex flex-wrap items-center gap-2 text-[11px]">
          {long && <button type="button" className="text-accent" onClick={() => setFull(value => !value)}>{t(full ? "worker.showLess" : "worker.showFull")}</button>}
          {worker.result_truncated && (full || !long) && <span className="text-text-muted">{t("worker.resultShortened")}</span>}
        </div>
      )}
    </div>
  );
}

function WorkerSteps({ worker, chatId, registry, t }: { worker: ConversationWorker; chatId: string; registry: ToolVisualRegistry; t: T }) {
  const { conversationsClient } = useConversationAPI();
  const [steps, setSteps] = useState<WorkerStep[] | null>(null);
  const [cursor, setCursor] = useState("");
  const [error, setError] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const unfinished = workerUnfinished(worker);
  const now = useNow(unfinished);
  useEffect(() => {
    let active = true;
    const load = () => conversationsClient.workerActivity(chatId, worker.agent_id, worker.thread_id, { detail: true, limit: 20 })
      .then(page => {
        if (!active) return;
        // Newest first from the server; keep older pages already loaded.
        setSteps(current => mergeSteps([...page.steps].reverse(), current ?? []));
        setCursor(current => current || page.next_cursor || "");
        setError(false);
      })
      .catch(() => { if (active) setError(true); });
    void load();
    if (!unfinished) return () => { active = false; };
    const timer = window.setInterval(load, 5000);
    return () => { active = false; window.clearInterval(timer); };
  }, [conversationsClient, chatId, worker.agent_id, worker.thread_id, unfinished, worker.revision]);
  const loadEarlier = async () => {
    setShowAll(true);
    if (!cursor) return;
    try {
      const page = await conversationsClient.workerActivity(chatId, worker.agent_id, worker.thread_id, { detail: true, limit: 20, cursor });
      setSteps(current => mergeSteps(current ?? [], [...page.steps].reverse()));
      setCursor(page.next_cursor || "");
    } catch { setError(true); }
  };
  if (steps === null) return <p className="text-[11px] text-text-muted" role="status">{error ? t("worker.stepsUnavailable") : t("worker.loadingSteps")}</p>;
  // Spawn steps show as nested cards, and the end step as the result.
  const shown = steps.filter(step => step.kind !== "end");
  if (!shown.length) return <p className="text-[11px] text-text-muted">{t("worker.noSteps")}</p>;
  const visible = showAll ? shown : shown.slice(-VISIBLE_STEPS);
  const hidden = shown.length - visible.length;
  return (
    <div className="grid min-w-0">
      {(hidden > 0 || (showAll && cursor)) && (
        <button type="button" className="justify-self-start py-1 text-[11px] text-accent" onClick={() => void loadEarlier()}>{t("worker.showEarlier")}</button>
      )}
      <ol className="m-0 grid min-w-0 list-none p-0">
        {visible.map((step, index) => <WorkerStepRow key={`${step.started_at}:${step.kind}:${step.tool ?? ""}:${index}`} step={step} registry={registry} t={t} now={now} />)}
      </ol>
    </div>
  );
}

function stepKey(step: WorkerStep): string {
  return `${step.kind}:${step.started_at}:${step.tool ?? ""}:${step.to ?? ""}`;
}

/** Oldest first; a newer copy of the same step replaces the older one. */
function mergeSteps(newer: WorkerStep[], older: WorkerStep[]): WorkerStep[] {
  const map = new Map<string, WorkerStep>();
  for (const step of older) map.set(stepKey(step), step);
  for (const step of newer) map.set(stepKey(step), step);
  return [...map.values()].sort((a, b) => Date.parse(a.started_at) - Date.parse(b.started_at));
}

function WorkerStepRow({ step, registry, t, now }: { step: WorkerStep; registry: ToolVisualRegistry; t: T; now: number }) {
  const open = !step.ended_at && step.status !== "ended" && step.status !== "unknown";
  const duration = step.duration_ms ?? (open ? Math.max(0, now - Date.parse(step.started_at)) : undefined);
  const statusNote = step.status === "ended" ? t("worker.stepEnded") : step.status === "unknown" ? t("worker.stepUnknown") : "";
  const failed = step.status === "failed" || step.status === "error" || Boolean(step.error);
  if (step.kind === "model") {
    return (
      <li className="grid min-h-8 min-w-0 grid-cols-[auto_minmax(0,1fr)] items-center gap-2 border-t border-border/60 py-0.5 first:border-t-0">
        <span className="chat-thinking-dots inline-flex h-7 w-7 items-center justify-center gap-1 text-text-muted" aria-hidden="true"><span /><span /><span /></span>
        <span className="text-[12px] text-text-muted">
          {t("worker.thinkingFor", { duration: durationLabel(duration) || "…" })}
          {statusNote && <span className="ml-2">{statusNote}</span>}
        </span>
      </li>
    );
  }
  if (step.kind === "message") {
    const text = step.text || "";
    return (
      <li className="min-w-0 border-t border-border/60 py-1.5 text-[12px] leading-5 text-text first:border-t-0 [overflow-wrap:anywhere]">
        {t(step.to === "main" ? "worker.reportedMain" : "worker.reported", { text: text || "…" })}
      </li>
    );
  }
  const visual = resolveToolVisual(step.tool || "", registry);
  const label = step.reason?.trim() || toolLabel(step.tool || "", registry);
  const hasDetail = Boolean(step.arguments || step.result || step.error);
  return (
    <li className="min-w-0 border-t border-border/60 py-0.5 first:border-t-0">
      <div className="grid min-h-9 min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2">
        <ToolVisualIcon visual={visual} running={open} />
        <span className={`chat-tool-copy min-w-0 text-[12px] font-medium leading-5 [overflow-wrap:anywhere] ${open ? "chat-tool-copy-running" : ""}`} title={step.tool}>{label}</span>
        <span className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-[10px] font-medium uppercase tracking-wide sm:text-[11px]">
          {failed && <span className="inline-flex items-center gap-1 chat-tool-failed-text"><FailureIcon /><span>{t("worker.stepFailed")}</span></span>}
          {statusNote && <span className="normal-case text-text-muted">{statusNote}</span>}
          {duration !== undefined && <span className="text-text-dim">{durationLabel(duration)}</span>}
        </span>
      </div>
      {hasDetail && (
        <details className="mb-1 ml-9 min-w-0 text-[11px]">
          <summary className="cursor-pointer text-text-muted hover:text-text">{t("worker.stepDetails")}</summary>
          <div className="mt-1 grid min-w-0 gap-1">
            {step.arguments && <StepBlock label={t("worker.arguments")} text={step.arguments} />}
            {(step.result || step.error) && <StepBlock label={t("worker.output")} text={step.error || step.result || ""} failed={Boolean(step.error)} />}
          </div>
        </details>
      )}
    </li>
  );
}

function StepBlock({ label, text, failed = false }: { label: string; text: string; failed?: boolean }) {
  return (
    <div className="min-w-0">
      <p className="text-[10px] font-medium uppercase tracking-wide text-text-muted">{label}</p>
      <pre className={`max-h-48 overflow-auto whitespace-pre-wrap rounded border border-border bg-bg p-1.5 font-mono text-[11px] leading-4 [overflow-wrap:anywhere] ${failed ? "chat-tool-failed-text" : "text-text"}`}>{text}</pre>
    </div>
  );
}
