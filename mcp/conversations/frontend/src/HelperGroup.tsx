// Helpers in the chat. The helpers one reply started read as one line ("3
// helpers · 1 finished, 2 working"), then one row per helper with only what it
// is doing now or how it ended. Everything a helper did is in its detail view,
// shown in the panel's side column, or in a drawer where there is none.
import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useConversationAPI } from "./context";
import { useConversationLocalization } from "./i18n";
import { CheckIcon, ChevronIcon, FailureIcon, ToolVisualIcon } from "./ToolActivity";
import { resolveToolVisual, type ToolVisual, type ToolVisualRegistry } from "./toolVisuals";
import type { ConversationWorker, TurnWork } from "./types";
import {
  WorkerResult, WorkerSteps, clockLabel, descendants, directChildren, elapsedMs, toolLabel, useNow, workerTitle, workerUnfinished, type T,
} from "./WorkerCard";

export const STOPPED_BY_OPERATOR = "stopped_by_operator";
const HELPER_VISUAL: ToolVisual = { key: "native:spawn", label: "Apteva", glyph: "agent" };
const MAX_DOTS = 6;
const PURPOSE_PREVIEW_CHARS = 280;

export function workerFailed(worker: ConversationWorker): boolean {
  return worker.state === "failed" || worker.outcome === "failed" || worker.outcome === "lost";
}

function workerStopped(worker: ConversationWorker): boolean {
  return !workerFailed(worker) && (worker.state === "stopped" || worker.outcome === STOPPED_BY_OPERATOR || worker.outcome === "cancelled");
}

function firstLine(text?: string): string {
  const line = (text ?? "").split(/\n+/).map(part => part.replace(/^[#>*\-\s]+/, "").trim()).find(Boolean) ?? "";
  return line.length > 160 ? `${line.slice(0, 159).trimEnd()}…` : line;
}

/** What a helper is doing now, or how it ended. One line, never its history. */
export function helperActivity(worker: ConversationWorker, t: T, time: (value: string) => string): string {
  if (worker.outcome === STOPPED_BY_OPERATOR) return t("helpers.stoppedByYou");
  if (worker.outcome === "lost") return t("worker.state.lost");
  if (workerUnfinished(worker)) {
    if (worker.current?.kind === "tool" && worker.current.tool) return worker.current.reason?.trim() || t("worker.usingTool", { tool: toolLabel(worker.current.tool) });
    if (worker.current?.kind === "model") return t("worker.thinking");
    if (worker.state === "sleeping" && worker.next_wake_at) return t("worker.state.sleepingUntil", { time: time(worker.next_wake_at) });
  } else if (worker.state === "done" || worker.state === "failed") {
    const line = firstLine(worker.result);
    if (line) return line;
  }
  const key = `worker.state.${worker.state}`;
  const label = t(key);
  return label === key ? worker.state : label;
}

/** "5 helpers working", "5 helpers · 2 finished, 3 working", "5 helpers finished". */
export function helperGroupSummary(workers: ConversationWorker[], t: T): string {
  const count = workers.length;
  const working = workers.filter(workerUnfinished).length;
  const failed = workers.filter(w => !workerUnfinished(w) && workerFailed(w)).length;
  const stopped = workers.filter(w => !workerUnfinished(w) && workerStopped(w)).length;
  const finished = count - working - failed - stopped;
  if (working === count) return t("helpers.allWorking", { count });
  if (finished === count) return t("helpers.allFinished", { count });
  const parts = [
    finished ? t("helpers.finished", { count: finished }) : "",
    working ? t("helpers.working", { count: working }) : "",
    failed ? t("helpers.failed", { count: failed }) : "",
    stopped ? t("helpers.stopped", { count: stopped }) : "",
  ].filter(Boolean);
  return `${t("helpers.count", { count })} · ${parts.join(", ")}`;
}

function groupElapsed(workers: ConversationWorker[], now: number): number | undefined {
  const starts = workers.map(w => Date.parse(w.started_at)).filter(Number.isFinite);
  if (!starts.length) return undefined;
  const live = workers.some(workerUnfinished);
  const ends = workers.map(w => (w.ended_at ? Date.parse(w.ended_at) : now)).filter(Number.isFinite);
  const end = live ? now : Math.max(...ends);
  return Math.max(0, end - Math.min(...starts));
}

const compactNumbers = new Map<string, Intl.NumberFormat>();
/** 48200 → "48.2K" in the conversation's language. */
export function compactNumber(value: number, locale: string): string {
  let format = compactNumbers.get(locale);
  if (!format) {
    try { format = new Intl.NumberFormat(locale, { notation: "compact", maximumFractionDigits: 1 }); }
    catch { format = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 }); }
    compactNumbers.set(locale, format);
  }
  return format.format(value);
}

function StopGlyph() {
  return <svg viewBox="0 0 16 16" className="h-3.5 w-3.5" aria-hidden="true"><rect x="4" y="4" width="8" height="8" rx="1.5" fill="currentColor" /></svg>;
}

function ArrowGlyph() {
  return (
    <svg viewBox="0 0 20 20" className="h-4 w-4 shrink-0 text-text-dim" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
      <path d="m7.5 5 5 5-5 5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function StateDot({ worker }: { worker: ConversationWorker }) {
  const live = workerUnfinished(worker);
  const working = live && worker.state === "working";
  const color = workerFailed(worker) ? "var(--chat-tool-failed, #ff3d24)"
    : working ? "var(--chat-tool-running, #f97316)"
    : !live && !workerStopped(worker) ? "var(--color-green, #22c55e)" : "currentColor";
  return (
    <svg viewBox="0 0 8 8" width="8" height="8" className={`shrink-0 text-text-dim ${working ? "chat-thread-working-dot" : ""}`} aria-hidden="true">
      <circle cx="4" cy="4" r="3" fill={color} />
    </svg>
  );
}

/** One icon for a helper's state: its current tool while working. */
function HelperStateIcon({ worker, registry }: { worker: ConversationWorker; registry: ToolVisualRegistry }) {
  const live = workerUnfinished(worker);
  if (live) {
    const visual = worker.current?.kind === "tool" && worker.current.tool ? resolveToolVisual(worker.current.tool, registry) : HELPER_VISUAL;
    return <ToolVisualIcon visual={visual} running={worker.state === "working"} />;
  }
  const tone = workerFailed(worker) ? "chat-tool-failed-text" : workerStopped(worker) ? "text-text-muted" : "text-green";
  return (
    <span className={`inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-bg-hover ${tone}`} aria-hidden="true">
      {workerFailed(worker) ? <FailureIcon /> : workerStopped(worker) ? <StopGlyph /> : <CheckIcon />}
    </span>
  );
}

// ─── detail host ──────────────────────────────────────────────────────

/**
 * Where a helper's detail opens. The panel provides it: the detail fills the
 * side column when that column is on screen. Without a host, or without the
 * column, the chat shows the detail in a drawer.
 */
export interface HelperDetailHost {
  columnVisible: boolean;
  openId: number | null;
  open: (workerId: number | null) => void;
  slot: HTMLElement | null;
  setSlot: (element: HTMLElement | null) => void;
}
export const HelperDetailContext = createContext<HelperDetailHost | null>(null);
export function useHelperDetailHost(): HelperDetailHost | null {
  return useContext(HelperDetailContext);
}

/** The chat's side of the detail: which helper is open, and where it shows. */
export function useHelperDetail() {
  const host = useHelperDetailHost();
  const [localOpen, setLocalOpen] = useState<number | null>(null);
  const openId = host ? host.openId : localOpen;
  const open = host ? host.open : setLocalOpen;
  const place = (detail: ReactNode): ReactNode => {
    if (!detail) return null;
    if (host?.columnVisible) return host.slot ? createPortal(detail, host.slot) : null;
    return <HelperDrawer onClose={() => open(null)}>{detail}</HelperDrawer>;
  };
  return { openId, open, place, inColumn: Boolean(host?.columnVisible) };
}

function HelperDrawer({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  const { t } = useConversationLocalization();
  const returnFocus = useRef<Element | null>(null);
  useEffect(() => {
    returnFocus.current = document.activeElement;
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      if (returnFocus.current instanceof HTMLElement) returnFocus.current.focus();
    };
  }, [onClose]);
  return (
    <div className="fixed inset-0 z-[90]" data-helper-drawer>
      <button type="button" className="absolute inset-0 bg-black/50" onClick={onClose} aria-label={t("common.close")} tabIndex={-1} />
      <aside className="absolute inset-y-0 right-0 flex w-[min(100vw,440px)] flex-col border-l border-border bg-bg shadow-2xl" role="dialog" aria-modal="true" aria-label={t("helpers.detail")}>
        {children}
      </aside>
    </div>
  );
}

// ─── chat group ───────────────────────────────────────────────────────

export interface HelperGroupProps {
  /** The helpers one reply started, oldest first. */
  workers: ConversationWorker[];
  /** Every helper of the conversation, for nested counts. */
  all: ConversationWorker[];
  registry: ToolVisualRegistry;
  /** The spawn call's reason by worker id, for helpers saved before titles were kept. */
  spawnReasons?: Map<number, string>;
  onOpen: (worker: ConversationWorker) => void;
  /** Start with every helper's row shown. */
  defaultExpanded?: boolean;
}

export function HelperGroup({ workers, all, registry, spawnReasons, onOpen, defaultExpanded }: HelperGroupProps) {
  const { t: translate, dateTime } = useConversationLocalization();
  const t = translate as unknown as T;
  const time = (value: string) => dateTime(value, { timeStyle: "short" });
  const live = workers.some(workerUnfinished);
  // Live helpers show their rows; a finished group is one quiet line.
  const [expanded, setExpanded] = useState(defaultExpanded ?? live);
  const now = useNow(live);
  const summary = helperGroupSummary(workers, t);
  const [announcement, setAnnouncement] = useState("");
  const wasLive = useRef(live);
  useEffect(() => {
    if (wasLive.current && !live) setAnnouncement(summary);
    wasLive.current = live;
  }, [live, summary]);
  if (workers.length === 1) {
    const worker = workers[0]!;
    return (
      <section className="chat-helper-group min-w-0 shrink-0" data-helper-group data-helper-count={1}>
        <HelperRow worker={worker} all={all} registry={registry} title={workerTitle(worker, spawnReasons?.get(worker.id))} t={t} time={time} now={now} onOpen={onOpen} />
        <span className="sr-only" role="status" aria-live="polite">{announcement}</span>
      </section>
    );
  }
  const listId = `helpers-${workers[0]!.chat_id}-${workers[0]!.id}`.replace(/[^a-zA-Z0-9_-]/g, "-");
  return (
    <section className="chat-helper-group min-w-0 shrink-0" data-helper-group data-helper-count={workers.length}>
      <button
        type="button"
        className="grid min-h-9 w-full min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 rounded-lg px-1 text-left hover:bg-bg-hover/60 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent"
        aria-expanded={expanded}
        aria-controls={listId}
        onClick={() => setExpanded(value => !value)}
      >
        <span className="inline-flex h-7 w-7 shrink-0 flex-wrap content-center items-center justify-center gap-0.5 rounded-md bg-bg-hover" aria-hidden="true">
          {workers.slice(0, MAX_DOTS).map(worker => <StateDot key={worker.id} worker={worker} />)}
        </span>
        <span className={`chat-tool-copy min-w-0 text-[13px] font-medium leading-5 [overflow-wrap:anywhere] ${live ? "chat-tool-copy-running" : ""}`} data-helper-summary>{summary}</span>
        <span className="inline-flex shrink-0 items-center gap-2">
          <span className="text-[11px] tabular-nums text-text-dim">{clockLabel(groupElapsed(workers, now))}</span>
          <ChevronIcon expanded={expanded} />
        </span>
      </button>
      <span className="sr-only" role="status" aria-live="polite">{announcement}</span>
      {expanded && (
        <ul id={listId} className="m-0 grid min-w-0 list-none gap-0.5 p-0 sm:pl-9">
          {workers.map(worker => (
            <li key={worker.id} className="min-w-0">
              <HelperRow worker={worker} all={all} registry={registry} title={workerTitle(worker, spawnReasons?.get(worker.id))} t={t} time={time} now={now} onOpen={onOpen} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function HelperRow({ worker, all, registry, title, t, time, now, onOpen }: {
  worker: ConversationWorker; all: ConversationWorker[]; registry: ToolVisualRegistry; title: string; t: T;
  time: (value: string) => string; now: number; onOpen: (worker: ConversationWorker) => void;
}) {
  const live = workerUnfinished(worker);
  const working = live && worker.state === "working";
  const failed = workerFailed(worker);
  const activity = helperActivity(worker, t, time);
  const nested = useMemo(() => descendants(worker, all).length, [worker, all]);
  return (
    <button
      type="button"
      className="grid min-h-10 w-full min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-start gap-2 rounded-lg px-1 py-1 text-left hover:bg-bg-hover/60 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent"
      data-worker-thread={worker.thread_id}
      data-worker-state={worker.state}
      aria-label={t("helpers.rowLabel", { name: title, status: activity })}
      title={t("helpers.open", { name: title })}
      onClick={() => onOpen(worker)}
    >
      <HelperStateIcon worker={worker} registry={registry} />
      <span className="grid min-w-0 gap-0.5 pt-0.5">
        <span className="flex min-w-0 flex-wrap items-baseline gap-x-1.5">
          <span className="min-w-0 text-[13px] font-medium leading-5 text-text [overflow-wrap:anywhere]" data-helper-title>{title}</span>
          {nested > 0 && <span className="shrink-0 text-[11px] text-text-muted">{t("worker.moreHelpers", { count: nested })}</span>}
        </span>
        <span
          className={`min-w-0 text-[12px] leading-4 [overflow-wrap:anywhere] ${failed ? "chat-tool-failed-text" : "text-text-muted"} ${working ? "chat-tool-copy chat-tool-copy-running" : ""}`}
          style={{ display: "-webkit-box", WebkitLineClamp: 2, WebkitBoxOrient: "vertical", overflow: "hidden" }}
          data-helper-activity
        >{activity}</span>
      </span>
      <span className="inline-flex shrink-0 items-center gap-1 pt-1.5">
        <span className="text-[11px] tabular-nums text-text-dim">{clockLabel(elapsedMs(worker, now))}</span>
        <ArrowGlyph />
      </span>
    </button>
  );
}

// ─── detail ──────────────────────────────────────────────────────────

export interface HelperDetailProps {
  worker: ConversationWorker;
  all: ConversationWorker[];
  chatId: string;
  registry: ToolVisualRegistry;
  spawnReason?: string;
  /** Operators can stop a helper that is still running. */
  canStop: boolean;
  onOpen: (worker: ConversationWorker) => void;
  onClose: () => void;
  /** The parent helper, when this one was started by another helper. */
  parent?: ConversationWorker;
}

export function HelperDetail({ worker, all, chatId, registry, spawnReason, canStop, onOpen, onClose, parent }: HelperDetailProps) {
  const { t: translate, dateTime, locale } = useConversationLocalization();
  const t = translate as unknown as T;
  const time = (value: string) => dateTime(value, { timeStyle: "short" });
  const { conversationsClient } = useConversationAPI();
  const live = workerUnfinished(worker);
  const now = useNow(live);
  const title = workerTitle(worker, spawnReason);
  const activity = helperActivity(worker, t, time);
  const children = useMemo(() => directChildren(worker, all), [worker, all]);
  const [stepInfo, setStepInfo] = useState<{ model?: string; endText?: string }>({});
  const shown = useMemo(() => ({ ...worker, model: stepInfo.model || worker.model, result: worker.result || stepInfo.endText }), [worker, stepInfo]);
  const [confirming, setConfirming] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [stopError, setStopError] = useState("");
  const [fullTask, setFullTask] = useState(false);
  const closeRef = useRef<HTMLButtonElement>(null);
  useEffect(() => { closeRef.current?.focus(); }, [worker.id]);
  useEffect(() => { setConfirming(false); setStopError(""); setStepInfo({}); setFullTask(false); }, [worker.id]);
  const stop = async () => {
    setStopping(true);
    setStopError("");
    try {
      await conversationsClient.stopWorker(chatId, worker.id);
      setConfirming(false);
    } catch {
      setStopError(t("helpers.stopFailed"));
    } finally {
      setStopping(false);
    }
  };
  const meta = [
    shown.model ? t("worker.model", { model: shown.model }) : "",
    worker.started_at ? t("worker.started", { time: time(worker.started_at) }) : "",
    worker.tool_steps ? t("helpers.toolCalls", { count: worker.tool_steps }) : "",
    worker.tokens ? t("helpers.tokens", { count: compactNumber(worker.tokens, locale) }) : "",
  ].filter(Boolean);
  const purpose = worker.purpose?.trim() ?? "";
  const longPurpose = purpose.length > PURPOSE_PREVIEW_CHARS;
  return (
    <div className="flex h-full min-h-0 flex-col bg-bg" data-helper-detail={worker.thread_id}>
      <div className="flex min-h-10 shrink-0 items-center gap-2 border-b border-border px-3">
        <span className="min-w-0 flex-1 truncate text-xs font-semibold uppercase text-text-muted">{t("helpers.detail")}</span>
        <button
          ref={closeRef}
          type="button"
          onClick={onClose}
          className="inline-flex h-8 w-8 items-center justify-center rounded text-lg text-text-muted hover:bg-bg-input hover:text-text"
          aria-label={t("helpers.closeDetail")}
        >×</button>
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-4">
        <div className="grid min-w-0 gap-4">
          <header className="grid min-w-0 gap-2">
            {parent && (
              <button type="button" className="justify-self-start text-[11px] text-accent" onClick={() => onOpen(parent)}>
                {t("helpers.startedBy", { name: workerTitle(parent) })}
              </button>
            )}
            <div className="flex min-w-0 items-start gap-2">
              <HelperStateIcon worker={worker} registry={registry} />
              <div className="grid min-w-0 flex-1 gap-0.5">
                <h2 className="m-0 text-[15px] font-semibold leading-5 text-text [overflow-wrap:anywhere]">{title}</h2>
                <p className={`m-0 text-[12px] leading-4 [overflow-wrap:anywhere] ${workerFailed(worker) ? "chat-tool-failed-text" : "text-text-muted"}`} data-helper-activity>
                  {activity}
                  <span className="ml-2 tabular-nums text-text-dim">{clockLabel(elapsedMs(worker, now))}</span>
                </p>
              </div>
            </div>
            {canStop && live && (
              <div className="flex min-w-0 flex-wrap items-center gap-2" data-helper-stop>
                {!confirming ? (
                  <button type="button" className="inline-flex items-center gap-1.5 rounded border border-border px-2 py-1 text-xs text-text-muted hover:bg-bg-input hover:text-text" onClick={() => setConfirming(true)}>
                    <StopGlyph />{t("helpers.stop")}
                  </button>
                ) : (
                  <>
                    <span className="text-xs text-text">{t("helpers.stopConfirm")}</span>
                    <button type="button" disabled={stopping} className="rounded border border-border px-2 py-1 text-xs chat-tool-failed-text hover:bg-bg-input disabled:opacity-50" onClick={() => void stop()}>
                      {t(stopping ? "helpers.stopping" : "helpers.stopYes")}
                    </button>
                    <button type="button" disabled={stopping} className="rounded px-2 py-1 text-xs text-text-muted hover:bg-bg-input hover:text-text" onClick={() => setConfirming(false)}>
                      {t("common.cancel")}
                    </button>
                  </>
                )}
                {stopError && <span role="alert" className="text-xs chat-tool-failed-text">{stopError}</span>}
              </div>
            )}
          </header>
          {purpose && (
            <section className="grid min-w-0 gap-1">
              <h3 className="m-0 text-[11px] font-medium uppercase tracking-wide text-text-muted">{t("helpers.task")}</h3>
              <p className="m-0 whitespace-pre-wrap text-[13px] leading-5 text-text [overflow-wrap:anywhere]">
                {fullTask || !longPurpose ? purpose : `${purpose.slice(0, PURPOSE_PREVIEW_CHARS).trimEnd()}…`}
              </p>
              {longPurpose && <button type="button" className="justify-self-start text-[11px] text-accent" onClick={() => setFullTask(value => !value)}>{t(fullTask ? "worker.showLess" : "worker.showFull")}</button>}
            </section>
          )}
          {meta.length > 0 && <p className="m-0 text-[11px] text-text-muted">{meta.join(" · ")}</p>}
          <section className="grid min-w-0 gap-1">
            <h3 className="m-0 text-[11px] font-medium uppercase tracking-wide text-text-muted">{t("helpers.steps")}</h3>
            <WorkerSteps worker={worker} chatId={chatId} registry={registry} t={t} onInfo={setStepInfo} />
          </section>
          {children.length > 0 && (
            <section className="grid min-w-0 gap-1" aria-label={t("worker.nestedHelpers")}>
              <h3 className="m-0 text-[11px] font-medium uppercase tracking-wide text-text-muted">{t("worker.nestedHelpers")}</h3>
              {children.map(child => (
                <HelperRow key={child.id} worker={child} all={all} registry={registry} title={workerTitle(child)} t={t} time={time} now={now} onOpen={onOpen} />
              ))}
            </section>
          )}
          {shown.result && !live && <WorkerResult worker={shown} t={t} />}
        </div>
      </div>
    </div>
  );
}

// ─── turn footer ─────────────────────────────────────────────────────

/** Responses shorter than this show no footer. */
export const TURN_WORK_MIN_MS = 10_000;

export function turnWork(metadata?: Record<string, unknown>): TurnWork | undefined {
  const work = metadata?.work as TurnWork | undefined;
  return work && typeof work.duration_ms === "number" ? work : undefined;
}

/** "Worked for 1m 12s · 48.2K tokens" under a response that took a while. */
export function TurnWorkFooter({ work }: { work: TurnWork }) {
  const { t: translate, locale } = useConversationLocalization();
  const t = translate as unknown as T;
  if (work.duration_ms < TURN_WORK_MIN_MS) return null;
  const parts = [t("helpers.workedFor", { duration: clockLabel(work.duration_ms) })];
  if (work.tokens) parts.push(t("helpers.tokens", { count: compactNumber(work.tokens, locale) }));
  return <p className="m-0 text-[11px] text-text-dim" data-turn-work>{parts.join(" · ")}</p>;
}
