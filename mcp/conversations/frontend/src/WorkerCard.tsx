// What a helper did: its steps (newest last, earlier ones on request) and its
// result. HelperGroup.tsx shows helpers in the chat and in the detail view.
import { useEffect, useState } from "react";
import { useConversationAPI } from "./context";
import { FailureIcon, ToolVisualIcon, durationLabel } from "./ToolActivity";
import { resolveToolVisual, type ToolVisualRegistry } from "./toolVisuals";
import type { ConversationWorker, WorkerStep } from "./types";

export type T = (key: string, params?: Record<string, unknown>) => string;

const VISIBLE_STEPS = 8;
const RESULT_PREVIEW_CHARS = 600;
// Core plumbing that other rows already show: send is the report row, done
// the end of the helper.
const HIDDEN_STEP_TOOLS = new Set(["send", "done", "search_tools"]);
// Pace and wait return at once; the wait is the gap until the next step.
const WAIT_TOOLS = new Set(["pace", "wait"]);

export function workerUnfinished(worker: ConversationWorker): boolean {
  return !worker.ended_at && !["done", "failed", "stopped"].includes(worker.state);
}

/** "fan-care_review" → "Fan care review": a thread id read as a title. */
export function humanizeThreadId(id: string): string {
  const words = id.replace(/[-_.]+/g, " ").replace(/([a-z])([A-Z])/g, "$1 $2").trim().toLowerCase();
  return words ? words[0]!.toUpperCase() + words.slice(1) : id;
}

/**
 * A helper's title: the reason its spawn gave. Never its instructions, which
 * are a paragraph written for the model.
 */
export function workerTitle(worker: Pick<ConversationWorker, "label" | "name" | "thread_id">, spawnReason?: string): string {
  return worker.label?.trim() || spawnReason?.trim() || humanizeThreadId(worker.name?.trim() || worker.thread_id);
}

/** Key a spawn tool row and the worker it started share. */
export function spawnKey(agentId: number, threadId: string, callId?: string): string {
  return `${agentId}:${threadId}:${callId ?? ""}`;
}

export function descendants(worker: ConversationWorker, all: ConversationWorker[]): ConversationWorker[] {
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

export function directChildren(worker: ConversationWorker, all: ConversationWorker[]): ConversationWorker[] {
  // Only the newest incarnation of each child thread.
  const byThread = new Map<string, ConversationWorker>();
  for (const w of all) if (w.agent_id === worker.agent_id && w.parent_thread_id === worker.thread_id && w.id !== worker.id) {
    const prev = byThread.get(w.thread_id);
    if (!prev || prev.id < w.id) byThread.set(w.thread_id, w);
  }
  return [...byThread.values()].sort((a, b) => a.id - b.id);
}

export function toolLabel(tool: string, _registry?: ToolVisualRegistry): string {
  // The icon names the app; the label says what the tool does.
  return tool.replace(/^.*__/, "").replace(/_/g, " ").trim() || tool;
}

export function elapsedMs(worker: ConversationWorker, now: number): number | undefined {
  const start = Date.parse(worker.started_at);
  if (!Number.isFinite(start)) return undefined;
  const end = worker.ended_at ? Date.parse(worker.ended_at) : now;
  return Number.isFinite(end) ? Math.max(0, end - start) : undefined;
}

export function clockLabel(ms?: number): string {
  if (ms === undefined) return "";
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
}

export function useNow(active: boolean): number {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  return now;
}

export function WorkerResult({ worker, t }: { worker: ConversationWorker; t: T }) {
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

export function WorkerSteps({ worker, chatId, registry, t, onInfo }: {
  worker: ConversationWorker; chatId: string; registry: ToolVisualRegistry; t: T;
  onInfo?: (info: { model?: string; endText?: string }) => void;
}) {
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
  useEffect(() => {
    if (!steps || !onInfo) return;
    const model = [...steps].reverse().find(step => step.kind === "model" && step.model)?.model;
    const endText = steps.find(step => step.kind === "end")?.text;
    onInfo({ model, endText });
  }, [steps, onInfo]);
  if (steps === null) return <p className="text-[11px] text-text-muted" role="status">{error ? t("worker.stepsUnavailable") : t("worker.loadingSteps")}</p>;
  // Spawn steps show as nested cards, and the end step as the result.
  let shown = steps.filter(step => step.kind !== "end" && !(step.kind === "tool" && HIDDEN_STEP_TOOLS.has((step.tool || "").toLowerCase())));
  // The last turn of a finished helper is the one that called done. Its done
  // call is hidden and the helper ended with it, so it reads as stray
  // thinking after the report: leave it out.
  if (!unfinished || steps.some(step => step.kind === "end")) {
    while (shown.length && shown[shown.length - 1]!.kind === "model") shown = shown.slice(0, -1);
  }
  const waitedMs = (step: WorkerStep): number | undefined => {
    if (step.kind !== "tool" || !WAIT_TOOLS.has((step.tool || "").toLowerCase())) return undefined;
    const start = Date.parse(step.started_at);
    const next = steps.find(other => Date.parse(other.started_at) > start && other !== step);
    if (next) return Math.max(0, Date.parse(next.started_at) - start);
    return unfinished ? Math.max(0, now - start) : undefined;
  };
  if (!shown.length) return <p className="text-[11px] text-text-muted">{t("worker.noSteps")}</p>;
  const visible = showAll ? shown : shown.slice(-VISIBLE_STEPS);
  const hidden = shown.length - visible.length;
  return (
    <div className="grid min-w-0">
      {(hidden > 0 || (showAll && cursor)) && (
        <button type="button" className="justify-self-start py-1 text-[11px] text-accent" onClick={() => void loadEarlier()}>{t("worker.showEarlier")}</button>
      )}
      <ol className="m-0 grid min-w-0 list-none p-0">
        {visible.map((step, index) => <WorkerStepRow key={`${step.started_at}:${step.kind}:${step.tool ?? ""}:${index}`} step={step} registry={registry} t={t} now={now} waitedMs={waitedMs(step)} />)}
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

function WorkerStepRow({ step, registry, t, now, waitedMs }: { step: WorkerStep; registry: ToolVisualRegistry; t: T; now: number; waitedMs?: number }) {
  const open = !step.ended_at && step.status !== "ended" && step.status !== "unknown";
  const duration = waitedMs ?? step.duration_ms ?? (open ? Math.max(0, now - Date.parse(step.started_at)) : undefined);
  const statusNote = step.status === "ended" ? t("worker.stepEnded") : step.status === "unknown" ? t("worker.stepUnknown") : "";
  const failed = step.status === "failed" || step.status === "error" || Boolean(step.error);
  if (step.kind === "model") {
    return (
      <li className="grid min-h-8 min-w-0 grid-cols-[auto_minmax(0,1fr)] items-center gap-2 border-t border-border/60 py-0.5 first:border-t-0">
        {open ? (
          <span className="chat-thinking-dots inline-flex h-7 w-7 items-center justify-center gap-1 text-text-muted" aria-hidden="true"><span /><span /><span /></span>
        ) : (
          // A finished turn: a still mark, so only live thinking moves.
          <span className="inline-flex h-7 w-7 items-center justify-center text-text-dim" aria-hidden="true">
            <svg viewBox="0 0 12 12" width="12" height="12"><circle cx="6" cy="6" r="2" fill="currentColor" /></svg>
          </span>
        )}
        <span className="text-[12px] text-text-muted">
          {t(open ? "worker.thinkingFor" : "worker.thoughtFor", { duration: durationLabel(duration) || "…" })}
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
