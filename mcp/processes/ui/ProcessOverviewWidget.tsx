import { timingLabel, useTimingNow } from "./Timing";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import {
  eventsForStep,
  thoughtsFromEvents,
  useWorkerActivity,
} from "./execution-activity";
import { callsForExecution } from "./ExecutionTools";

type Props = {
  appName?: string;
  projectId?: string;
  installId?: number;
  dashboardScope?: "project" | "global";
  eventRevision?: number;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
};
type Step = {
  start_at?: string;
  due_at?: string;
  completed_at?: string;
  id: string;
  name: string;
  state: string;
  progress: number;
  kind: string;
  origin: string;
  executor: { kind: string; agent_id?: number };
  warning?: string;
  thread_id?: string;
  execution_id?: string;
};
export type Item = {
  id: string;
  project_id?: string;
  project_name?: string;
  process_id: string;
  process_name: string;
  assignment_id?: string;
  assignment_name?: string;
  agent_id?: number;
  version?: number;
  state: string;
  backend?: string;
  progress?: number;
  current_step?: string;
  created_at?: string;
  next_run_at?: string;
  schedule?: { kind: string; every?: string; cron?: string; timezone?: string };
  needs_attention?: boolean;
  warning?: string;
  steps?: Step[];
  steps_completed?: number;
  steps_total?: number;
  thread_id?: string;
  execution_id?: string;
  summary?: string;
};
export type Data = {
  scope?: "project" | "global";
  projects?: Array<{ id: string; name: string }>;
  counts: {
    active: number;
    scheduled: number;
    attention: number;
    recent: number;
    review?: number;
  };
  active: Item[];
  upcoming: Item[];
  recent: Item[];
  attention: Item[];
  warnings: string[];
  partial: boolean;
  generated_at: string;
};
const when = (s?: string) =>
  s
    ? new Date(s).toLocaleString(undefined, {
        dateStyle: "short",
        timeStyle: "short",
      })
    : "Time unavailable";
const terminal = (s: string) =>
  ["completed", "cancelled", "failed"].includes(s);
export function overviewLink(props: Props, x?: Item, assignment = false) {
  const q = new URLSearchParams();
  if (x?.project_id || props.projectId)
    q.set("project_id", x?.project_id || props.projectId || "");
  if (props.installId) q.set("install_id", String(props.installId));
  const app = props.appName || "processes";
  if (x) {
    q.set("process_id", x.process_id);
    if (assignment) q.set("assignment_id", x.assignment_id || x.id);
    else q.set("run_id", x.id);
  }
  return `/apps/${encodeURIComponent(app)}/page?${q}`;
}
export type QueueEntry = {
  item: Item;
  assignment: boolean;
  group: "running" | "scheduled" | "history";
};
export function overviewQueue(
  data: Data,
  settings?: Record<string, unknown>,
): QueueEntry[] {
  const working = (x: Item) =>
    ["running", "ready", "queued", "pending"].includes(x.state);
  const entries: QueueEntry[] = [
    ...(settings?.show_active === false
      ? []
      : data.active.map((item) => ({
          item,
          assignment: false,
          group: working(item)
            ? ("running" as const)
            : item.state === "scheduled"
              ? ("scheduled" as const)
              : ("history" as const),
        }))),
    ...(settings?.show_upcoming === false
      ? []
      : data.upcoming.map((item) => ({
          item,
          assignment: true,
          group: "scheduled" as const,
        }))),
    ...(settings?.show_recent === false
      ? []
      : data.recent.map((item) => ({
          item,
          assignment: false,
          group: "history" as const,
        }))),
    ...(settings?.show_active === false
      ? []
      : data.attention.map((item) => ({
          item,
          assignment: true,
          group: "history" as const,
        }))),
  ];
  const rank = { running: 0, scheduled: 1, history: 2 };
  const date = (x: QueueEntry) =>
    Date.parse(x.item.next_run_at || x.item.created_at || "") || 0;
  return entries.sort(
    (a, b) =>
      rank[a.group] - rank[b.group] ||
      (a.group === "scheduled" ? date(a) - date(b) : date(b) - date(a)),
  );
}
export function executionStatus({ item: x, assignment }: QueueEntry) {
  if (assignment)
    return {
      label: x.next_run_at ? "Next run" : "Assignment",
      state: x.state,
      current: false,
      extra: 0,
      hint: "",
    };
  if (terminal(x.state))
    return {
      label:
        x.state === "completed"
          ? "Finished"
          : x.state === "cancelled"
            ? "Stopped"
            : "Run failed",
      state: x.state,
      current: false,
      extra: 0,
      hint: x.warning || "",
    };
  const steps = x.steps || [];
  const current = steps.filter((s) =>
    ["running", "blocked", "waiting", "ready", "scheduled"].includes(s.state),
  );
  const matching = current.find((s) => s.state === x.state);
  if (["blocked", "waiting"].includes(x.state) && !matching)
    return {
      label:
        x.current_step ||
        (x.state === "blocked" ? "Run blocked" : "Waiting for input"),
      state: x.state,
      current: true,
      extra: 0,
      hint: x.warning || "",
    };
  const step =
    matching ||
    current.find((s) => s.state === "running") ||
    current.find((s) => s.state === "blocked") ||
    current.find((s) => s.state === "waiting") ||
    current[0];
  const same = current.filter((s) => s.state === step?.state);
  return {
    label: step
      ? `${step.name}${timingLabel(step) ? ` · ${timingLabel(step)}` : ""}`
      : x.current_step || "Preparing run",
    state: step?.state || x.state,
    current: true,
    extra: Math.max(0, same.length - 1),
    hint:
      current.map((s) => `${s.name} (${s.state})`).join(" · ") ||
      x.warning ||
      "",
  };
}

const stateLabel = (state: string) =>
  state === "sync pending"
    ? "Needs attention"
    : (state.charAt(0).toUpperCase() + state.slice(1)).replaceAll("_", " ");
const live = (state: string) =>
  ["running", "ready", "queued", "pending", "waiting", "blocked"].includes(state);
export type LiveTarget = {
  agentID?: number;
  threadID?: string;
  executionID?: string;
  stepID?: string;
  completedAt?: string;
  label: string;
};
/** The worker whose activity represents a run right now: the running step
 * (else the blocked/waiting/ready one), or the owner thread of a direct run. */
export function liveTarget(x: Item, stepID?: string): LiveTarget | null {
  const steps = x.steps || [];
  if (!steps.length)
    return x.execution_id
      ? { agentID: x.agent_id, threadID: x.thread_id, executionID: x.execution_id, label: x.current_step || "Run" }
      : null;
  const order = ["running", "blocked", "waiting", "ready"];
  const step = stepID
    ? steps.find((s) => s.id === stepID)
    : order.map((state) => steps.find((s) => s.state === state)).find(Boolean) ||
      [...steps].reverse().find((s) => s.execution_id);
  if (!step || step.executor.kind !== "agent" || !step.execution_id) return null;
  return {
    agentID: step.executor.agent_id,
    threadID: step.thread_id,
    executionID: step.execution_id,
    stepID: step.id,
    completedAt: step.completed_at,
    label: step.name,
  };
}
export type ActivityItem = {
  id: string;
  kind: "thought" | "tool" | "wait";
  time: string;
  title: string;
  text: string;
  status: "running" | "completed" | "failed";
};
const toolLabel = (name: string) => name.replace(/_/g, " ");
const tail = (text: string, n = 160) => {
  const t = text.replace(/\s+/g, " ").trim();
  return t.length > n ? `…${t.slice(-n)}` : t;
};
/** Thoughts and tool calls of one worker execution, oldest first. */
export function activityItems(events: Parameters<typeof eventsForStep>[0], target: LiveTarget): ActivityItem[] {
  let scoped = eventsForStep(events, target.executionID || "", target.stepID, target.completedAt);
  // An isolated worker's thread serves one step and never claims it, and
  // model events carry no execution ID, so keep that thread's model turns.
  const claimed = events.some((e) => e.type === "tool.call" && String(e.data?.name || "").endsWith("step_claim"));
  if (!claimed) {
    const end = target.completedAt ? Date.parse(target.completedAt) : Infinity;
    const model = events.filter(
      (e) =>
        ["llm.start", "llm.thinking", "llm.chunk", "llm.done", "llm.error"].includes(e.type) &&
        !(e.data?.execution_id || e.data?.execution_ids) &&
        Date.parse(e.time) <= end,
    );
    scoped = [...scoped, ...model].sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
  }
  const turns = thoughtsFromEvents(scoped);
  const thoughts = turns.filter((t, i) => (t.reasoning || t.response) || (t.status === "running" && i === turns.length - 1)).map((t): ActivityItem => ({
    id: `thought:${t.id}`,
    kind: "thought",
    time: t.time,
    title: t.status === "running" ? "Thinking" : t.status === "failed" ? "Model error" : "Thought",
    text: tail(t.reasoning || t.response || ""),
    status: t.status as ActivityItem["status"],
  }));
  const calls = callsForExecution(scoped, target.executionID || "").map((c): ActivityItem => ({
    id: `tool:${c.id}`,
    kind: c.name === "pace" ? "wait" : "tool",
    time: c.time,
    title: c.name === "pace" ? "Waiting" : toolLabel(c.name),
    text: c.reason,
    // A finished step has no call in flight, even when its result went unrecorded.
    status: target.completedAt && c.status === "running" ? "completed" : c.status,
  }));
  const all = [...thoughts, ...calls].sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
  // A model turn in progress only matters while it is the latest activity.
  return all.filter((a, i) => !(a.kind === "thought" && a.status === "running" && !a.text && i !== all.length - 1));
}
function useActivity(target: LiveTarget | null, enabled: boolean) {
  const { events, loading, error } = useWorkerActivity(
    target?.agentID,
    target?.threadID,
    enabled && !!target?.agentID && !!target?.threadID,
    enabled,
  );
  const items = useMemo(() => (target ? activityItems(events, target) : []), [events, target?.executionID, target?.stepID, target?.completedAt]);
  return { items, loading, error };
}
function ActivityIcon({ kind }: { kind: ActivityItem["kind"] }) {
  const path =
    kind === "thought"
      ? "M9 18h6M10 21h4M12 3a6 6 0 0 0-3.6 10.8c.6.5 1 1.2 1 2V16h5.2v-.2c0-.8.4-1.5 1-2A6 6 0 0 0 12 3z"
      : kind === "wait"
        ? "M12 7v5l3 2M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0z"
        : "M14.7 6.3a4 4 0 0 0-5.4 5.4l-6 6 2 2 6-6a4 4 0 0 0 5.4-5.4l-2.4 2.4-2-2z";
  return (
    <svg className="po-icon" viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={path} />
    </svg>
  );
}
/** One-line latest activity for a running row. */
function LiveLine({ item }: { item: Item }) {
  const target = useMemo(() => liveTarget(item), [item]);
  const { items } = useActivity(target, true);
  const last = items[items.length - 1];
  if (!target) return null;
  return (
    <span className="po-live" title={last ? `${last.title}: ${last.text}` : target.label}>
      {last ? <ActivityIcon kind={last.kind} /> : <span className="po-pulse" aria-hidden="true" />}
      <span className="po-live-text">
        {last
          ? `${last.status === "running" && last.kind === "tool" ? "Running " : ""}${last.title}${last.text ? ` · ${last.text}` : ""}`
          : `${target.label} · starting`}
      </span>
    </span>
  );
}
function ActivityFeed({ target, running }: { target: LiveTarget; running: boolean }) {
  const { items, loading, error } = useActivity(target, true);
  const end = useRef<HTMLOListElement>(null);
  const [all, setAll] = useState(false);
  const shown = all ? items : items.slice(-8);
  useEffect(() => {
    end.current?.lastElementChild?.scrollIntoView?.({ block: "nearest" });
  }, [items.length]);
  if (!target.agentID || !target.threadID)
    return <p className="po-muted">Activity was not tracked for this step.</p>;
  return (
    <div className="po-feed">
      {items.length > 8 && (
        <button type="button" className="po-link-button" onClick={() => setAll(!all)}>
          {all ? "Show latest only" : `Show all ${items.length}`}
        </button>
      )}
      <ol ref={end}>
        {shown.map((a) => (
          <li key={a.id} data-status={a.status} data-kind={a.kind}>
            <ActivityIcon kind={a.kind} />
            <span className="po-feed-title">{a.title}</span>
            <span className="po-feed-text" title={a.text}>{a.text || (a.status === "running" ? "…" : "")}</span>
            {a.status === "running" && <span className="po-pulse" aria-label="in progress" />}
          </li>
        ))}
      </ol>
      {error && <p className="po-muted" role="alert">{error}</p>}
      {!items.length && (
        <p className="po-muted">
          {loading ? "Loading activity…" : running ? "Waiting for the worker's first thought or tool call." : "No activity was recorded."}
        </p>
      )}
    </div>
  );
}
function Progress({ done, total }: { done: number; total: number }) {
  return (
    <span className="po-progress" role="progressbar" aria-valuemin={0} aria-valuemax={total} aria-valuenow={done} aria-label={`${done} of ${total} steps complete`}>
      <span style={{ width: `${total ? Math.round((100 * done) / total) : 0}%` }} />
    </span>
  );
}

function DetailModal({
  entry,
  props,
  name,
  badge,
  onClose,
}: {
  entry: QueueEntry;
  props: Props;
  name: (id?: number) => string;
  badge: (state: string) => ReactNode;
  onClose: () => void;
}) {
  const x = entry.item;
  const steps = x.steps || [];
  const [stepID, setStepID] = useState<string | undefined>(undefined);
  const target = useMemo(() => (entry.assignment ? null : liveTarget(x, stepID)), [x, stepID, entry.assignment]);
  const close = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    close.current?.focus();
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("keydown", key);
      previous?.focus?.();
    };
  }, []);
  const running = live(x.state) && !entry.assignment;
  const selected = target?.stepID;
  return createPortal(
    <div className="po-modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <style>{css}</style>
      <section className="po-modal po-detail" role="dialog" aria-modal="true" aria-label={`${x.process_name} details`}>
        <header>
          <div className="po-modal-title">
            <h3>{x.process_name}</h3>
            <p>
              {[x.project_name, x.assignment_name, name(x.agent_id)].filter(Boolean).join(" · ")}
            </p>
            <p>
              {entry.assignment ? "Next" : "Started"} {when(entry.assignment ? x.next_run_at : x.created_at)}
              {x.version ? ` · v${x.version}` : ""}
            </p>
          </div>
          <div className="po-modal-actions">
            {badge(x.state)}
            <button ref={close} type="button" className="po-icon-button" aria-label="Close details" onClick={onClose}>
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
            </button>
          </div>
        </header>
        <div className="po-modal-body">
          {x.summary && <p className="po-outcome">{x.summary}</p>}
          {x.warning && <p className="po-warning-text">{x.warning}</p>}
          {!!x.steps_total && (
            <div className="po-modal-progress">
              <span>{x.steps_completed || 0}/{x.steps_total} steps complete</span>
              <Progress done={x.steps_completed || 0} total={x.steps_total} />
            </div>
          )}
          {!!steps.length && (
            <ol className="po-steps">
              {steps.map((st) => (
                <li key={st.id} data-state={st.state} data-selected={selected === st.id}>
                  <button
                    type="button"
                    disabled={st.executor.kind !== "agent" || !st.execution_id}
                    aria-pressed={selected === st.id}
                    onClick={() => setStepID(st.id)}
                  >
                    <span className="po-step-dot" aria-hidden="true" />
                    <span className="po-step-copy">
                      {st.name}
                      <small>
                        {st.executor.kind === "human" ? "Human" : name(st.executor.agent_id)}
                        {timingLabel(st) ? ` · ${timingLabel(st)}` : ""}
                        {st.warning ? ` · ${st.warning}` : ""}
                      </small>
                    </span>
                    {badge(st.state)}
                  </button>
                </li>
              ))}
            </ol>
          )}
          {target && (
            <div className="po-activity">
              <h4>{steps.length ? `${target.label} · activity` : "Activity"}</h4>
              <ActivityFeed key={`${target.executionID}:${target.stepID}`} target={target} running={running} />
            </div>
          )}
        </div>
        <footer>
          <a href={overviewLink(props, x, entry.assignment)}>
            {entry.assignment ? "Open assignment" : "Open run in Processes"} ↗
          </a>
        </footer>
      </section>
    </div>,
    document.body,
  );
}

const css = `
.po-widget{color:var(--color-text,#eceef2);background:var(--color-bg-card,#141414);border:1px solid var(--color-border,#303030);border-radius:4px;font-family:inherit;min-width:0;overflow:hidden}
.po-widget *{box-sizing:border-box}.po-widget h2,.po-widget p{margin:0}.po-widget header{padding:14px 16px;border-bottom:1px solid var(--color-border,#303030)}.po-widget h2{font-size:14px;font-weight:700}.po-widget header p{font-size:11px;color:var(--color-text-muted,#aaa);margin-top:4px}.po-widget button,.po-widget a,.po-widget select{font-family:inherit}.po-widget button{cursor:pointer}.po-widget :is(button,a,select):focus-visible{outline:2px solid var(--color-accent,#ff8c36);outline-offset:-2px}.po-widget .po-project-filter{display:flex;align-items:center;gap:7px;margin-top:10px;font-size:11px;color:var(--color-text-muted,#aaa)}.po-widget .po-project-filter select{min-width:0;max-width:100%;padding:5px 7px;color:var(--color-text,#eceef2);background:var(--color-bg-card,#141414);border:1px solid var(--color-border,#303030);border-radius:4px;font-size:11px}.po-widget .po-filters{display:flex;gap:5px;padding:8px 16px;border-bottom:1px solid var(--color-border,#303030);overflow-x:auto}.po-widget .po-filters button,.po-widget .po-close{background:transparent;color:var(--color-text-muted,#aaa);font-size:11px;font-weight:600;border:1px solid var(--color-border,#303030);border-radius:4px;padding:6px 9px;white-space:nowrap}.po-widget .po-filters button[aria-pressed=true]{color:var(--color-accent,#ff8c36);border-color:var(--color-accent,#ff8c36);background:color-mix(in srgb,var(--color-accent,#ff8c36) 10%,transparent)}
.po-widget .po-row{display:grid;grid-template-columns:minmax(0,1fr) auto;column-gap:16px;align-items:start;width:100%;height:80px;padding:12px 16px;background:transparent;color:inherit;border:0;border-bottom:1px solid var(--color-border,#303030);text-align:left}.po-widget .po-row:hover,.po-widget .po-row[aria-expanded=true]{background:var(--color-bg-hover,#202020)}.po-widget .po-copy{min-width:0}.po-widget .po-execution{min-width:0;text-align:right}.po-widget .po-current{display:block;font-size:12px;font-weight:500;line-height:20px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.po-widget .po-execution-meta{display:flex;align-items:center;justify-content:flex-end;gap:7px;margin-top:5px;font-size:10px;line-height:18px;color:var(--color-text-muted,#aaa);white-space:nowrap}.po-widget .po-badge.review{color:#e3b86d;border-color:#e3b86d60}.po-widget .po-title{display:block;font-size:13px;line-height:20px;font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-widget .po-summary{display:block;margin-top:5px;font-size:11px;line-height:18px;color:var(--color-text-muted,#aaa);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-widget time{font-size:10px;color:var(--color-text-muted,#999);text-align:right;white-space:nowrap}.po-widget .po-badge{display:inline-block;justify-self:start;font-size:10px;font-weight:600;text-transform:uppercase;line-height:16px;padding:2px 6px;border-radius:4px;border:1px solid var(--color-border,#424242);color:var(--color-text-muted,#aaa)}.po-widget .po-badge.running,.po-widget .po-badge.ready{color:#8eabff;border-color:#8eabff60}.po-widget .po-badge.scheduled{color:#be9ff5;border-color:#be9ff560}.po-widget .po-badge.completed{color:#43c878;border-color:#43c87860}.po-widget .po-badge.blocked,.po-widget .po-badge.waiting,.po-widget .po-badge.attention{color:#e3b86d;border-color:#e3b86d60}.po-widget .po-badge.failed{color:#f08b8b;border-color:#f08b8b60}
.po-widget .po-warning{font-size:11px;color:#e3b86d;padding:10px 16px;overflow-wrap:anywhere}.po-widget .po-empty{font-size:12px;color:var(--color-text-muted,#aaa);padding:24px 16px}.po-widget footer{display:flex;justify-content:space-between;gap:8px;padding:9px 16px;font-size:11px;color:var(--color-text-muted,#999)}.po-widget a{color:var(--color-accent,#ff8c36);text-decoration:none;font-size:11px}.po-widget .po-detail{padding:16px;border-bottom:1px solid var(--color-border,#303030)}.po-widget .po-detail-head{display:flex;align-items:center;justify-content:space-between;gap:12px}.po-widget .po-detail h3{font-size:13px;margin:0}.po-widget .po-detail p{font-size:12px;color:var(--color-text-muted,#aaa);margin:8px 0;overflow-wrap:anywhere}.po-widget .po-detail ol{padding:0;margin:10px 0;list-style:none;max-height:200px;overflow:auto}.po-widget .po-detail li{display:flex;justify-content:space-between;align-items:center;gap:12px;font-size:12px;padding:10px 0;border-top:1px solid var(--color-border,#303030)}.po-widget .po-step-copy{min-width:0}.po-widget .po-step-copy small{display:block;color:var(--color-text-muted,#aaa);margin-top:4px}
.po-widget .po-row{height:auto;min-height:0;padding:12px 16px}.po-dot{width:8px;height:8px;margin-top:6px;border-radius:50%;background:var(--color-text-muted,#888)}.po-dot.running,.po-dot.ready{background:#8eabff}.po-dot.scheduled,.po-dot.active{background:#be9ff5}.po-dot.completed{background:#43c878}.po-dot.blocked,.po-dot.waiting,.po-dot.attention{background:#e3b86d}.po-dot.failed{background:#f08b8b}.po-dot.live{animation:po-pulse 1.2s ease-in-out infinite}@media(prefers-reduced-motion:reduce){.po-dot.live{animation:none}}.po-widget .po-meta{display:flex;flex-direction:column;align-items:flex-end;gap:5px;min-width:72px;font-size:11px;line-height:20px;color:var(--color-text-muted,#999);font-variant-numeric:tabular-nums;white-space:nowrap}.po-widget .po-meta time{font-size:11px;line-height:20px}.po-widget .po-meta-line{display:flex;gap:8px;line-height:18px}.po-widget .po-meta .po-badge{line-height:14px}.po-widget .po-summary.blocked,.po-widget .po-summary.waiting,.po-widget .po-summary.attention{color:#e3b86d}.po-widget .po-summary.failed{color:#f08b8b}.po-widget .po-live{display:flex;align-items:center;gap:6px;margin-top:6px;font-size:11px;line-height:16px;color:var(--color-text-muted,#aaa);min-width:0}.po-widget .po-live-text{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-icon{flex:none;color:var(--color-text-muted,#aaa)}.po-progress{display:block;height:3px;background:var(--color-border,#303030);border-radius:2px;overflow:hidden;margin-top:8px}.po-progress span{display:block;height:100%;background:#8eabff;transition:width .3s}.po-pulse{flex:none;width:7px;height:7px;border-radius:50%;background:#8eabff;animation:po-pulse 1.2s ease-in-out infinite}@keyframes po-pulse{50%{opacity:.25}}@media(prefers-reduced-motion:reduce){.po-pulse{animation:none}}
.po-modal-backdrop{position:fixed;inset:0;z-index:1000;display:flex;align-items:center;justify-content:center;padding:24px;background:rgba(0,0,0,.55)}
.po-modal{width:min(640px,100%);max-height:min(720px,calc(100vh - 48px));display:flex;flex-direction:column;color:var(--color-text,#eceef2);background:var(--color-bg-card,#141414);border:1px solid var(--color-border,#303030);border-radius:6px;box-shadow:0 18px 48px rgba(0,0,0,.45);font-family:inherit;overflow:hidden}.po-modal *{box-sizing:border-box}.po-modal :is(button,a):focus-visible{outline:2px solid var(--color-accent,#ff8c36);outline-offset:-2px}
.po-modal header{display:flex;justify-content:space-between;gap:12px;padding:16px;border-bottom:1px solid var(--color-border,#303030)}.po-modal h3{margin:0;font-size:14px}.po-modal h4{margin:0 0 8px;font-size:12px;font-weight:600}.po-modal-title{min-width:0}.po-modal-title p{margin:4px 0 0;font-size:11px;color:var(--color-text-muted,#aaa)}.po-modal-actions{display:flex;align-items:flex-start;gap:8px}.po-icon-button{display:grid;place-items:center;width:28px;height:28px;padding:0;background:transparent;color:var(--color-text-muted,#aaa);border:1px solid var(--color-border,#303030);border-radius:4px;cursor:pointer}.po-icon-button:hover{color:var(--color-text,#eceef2)}
.po-modal-body{overflow:auto;padding:14px 16px;display:grid;grid-template-columns:minmax(0,1fr);gap:14px}.po-activity{min-width:0}.po-outcome{margin:0;font-size:12px;line-height:18px;padding:10px 12px;border-left:2px solid #43c878;background:var(--color-bg-hover,#1d1d1d);overflow-wrap:anywhere}.po-warning-text{margin:0;font-size:12px;color:#e3b86d;overflow-wrap:anywhere}.po-modal-progress{display:grid;gap:2px;font-size:11px;color:var(--color-text-muted,#aaa)}
.po-steps{list-style:none;margin:0;padding:0;border:1px solid var(--color-border,#303030);border-radius:4px}.po-steps li+li{border-top:1px solid var(--color-border,#303030)}.po-steps button{display:flex;align-items:center;gap:10px;width:100%;padding:9px 12px;background:transparent;color:inherit;border:0;text-align:left;font:inherit;font-size:12px;cursor:pointer}.po-steps button:disabled{cursor:default}.po-steps button[aria-pressed=true]{background:var(--color-bg-hover,#202020)}.po-steps .po-step-copy{flex:1;min-width:0}.po-steps small{display:block;margin-top:2px;font-size:10px;color:var(--color-text-muted,#aaa);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-step-dot{flex:none;width:8px;height:8px;border-radius:50%;background:var(--color-border,#555)}li[data-state=running] .po-step-dot{background:#8eabff;animation:po-pulse 1.2s ease-in-out infinite}li[data-state=completed] .po-step-dot{background:#43c878}li[data-state=blocked] .po-step-dot,li[data-state=waiting] .po-step-dot{background:#e3b86d}li[data-state=failed] .po-step-dot{background:#f08b8b}
.po-badge{display:inline-block;font-size:10px;font-weight:600;text-transform:uppercase;line-height:16px;padding:2px 6px;border-radius:4px;border:1px solid var(--color-border,#424242);color:var(--color-text-muted,#aaa);white-space:nowrap}.po-badge.running,.po-badge.ready{color:#8eabff;border-color:#8eabff60}.po-badge.scheduled{color:#be9ff5;border-color:#be9ff560}.po-badge.completed{color:#43c878;border-color:#43c87860}.po-badge.blocked,.po-badge.waiting,.po-badge.attention{color:#e3b86d;border-color:#e3b86d60}.po-badge.failed{color:#f08b8b;border-color:#f08b8b60}
.po-feed{border:1px solid var(--color-border,#303030);border-radius:4px;padding:6px 0}.po-feed ol{list-style:none;margin:0;padding:0;max-height:240px;overflow:auto}.po-feed li{display:flex;align-items:center;gap:8px;padding:5px 12px;font-size:11px;line-height:16px;min-width:0}.po-feed-title{flex:none;font-weight:600;max-width:45%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-feed li[data-kind=thought] .po-feed-title{font-weight:500;color:var(--color-text-muted,#aaa)}.po-feed-text{flex:1;min-width:0;color:var(--color-text-muted,#aaa);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-feed li[data-status=failed] .po-feed-title{color:#f08b8b}.po-muted{margin:0;padding:6px 12px;font-size:11px;color:var(--color-text-muted,#aaa)}.po-link-button{margin:0 12px 4px;padding:0;background:none;border:0;color:var(--color-accent,#ff8c36);font:inherit;font-size:11px;cursor:pointer}
.po-modal footer{padding:10px 16px;border-top:1px solid var(--color-border,#303030);font-size:11px}.po-modal footer a{color:var(--color-accent,#ff8c36);text-decoration:none}
@media(max-width:480px){.po-modal-backdrop{padding:0;align-items:flex-end}.po-modal{max-height:90vh;border-radius:8px 8px 0 0}}
@media(max-width:480px){.po-widget .po-row{column-gap:10px;padding:12px}.po-widget .po-badge{font-size:9px;padding:2px 4px}.po-widget header,.po-widget .po-filters{padding-left:12px;padding-right:12px}}
`;
export default function ProcessOverviewWidget(props: Props) {
  return (
    <Overview
      key={`${props.projectId}:${props.installId}:${props.appName}:${props.dashboardScope}`}
      {...props}
    />
  );
}
function Overview(props: Props) {
  useTimingNow();
  const [view, setView] = useState("all"),
    [projectFilter, setProjectFilter] = useState(""),
    [selected, setSelected] = useState<string | null>(null),
    [data, setData] = useState<Data | null>(null),
    [error, setError] = useState(""),
    [refresh, setRefresh] = useState(0),
    [names, setNames] = useState<Record<number, string>>({});
  useEffect(() => {
    const global = props.dashboardScope === "global";
    if (!global && !props.projectId) return;
    let alive = true;
    const c = new AbortController();
    const q = new URLSearchParams();
    if (props.projectId) q.set("project_id", props.projectId);
    if (global && projectFilter) q.set("project_id", projectFilter);
    if (props.installId) q.set("install_id", String(props.installId));
    fetch(
      `/api/apps/${encodeURIComponent(props.appName || "processes")}/processes/overview?${q}`,
      { credentials: "same-origin", signal: c.signal },
    )
      .then(async (r) => {
        if (!r.ok) throw new Error(`Overview unavailable (${r.status})`);
        return r.json();
      })
      .then((d) => {
        if (alive) {
          setData(d);
          setError("");
        }
      })
      .catch((e) => {
        if (alive) setError(e.message);
      });
    return () => {
      alive = false;
      c.abort();
    };
  }, [
    props.appName,
    props.projectId,
    props.installId,
    props.dashboardScope,
    projectFilter,
    props.eventRevision,
    refresh,
  ]);
  useEffect(() => {
    const global = props.dashboardScope === "global";
    if (!global && !props.projectId) return;
    let alive = true;
    const c = new AbortController();
    const agentQuery = props.projectId
      ? `?${new URLSearchParams({ project_id: props.projectId })}`
      : "";
    fetch(
      `/api/agents${agentQuery}`,
      { credentials: "same-origin", signal: c.signal },
    )
      .then((r) => {
        if (!r.ok) throw new Error("Agents unavailable");
        return r.json();
      })
      .then((d) => {
        if (alive)
          setNames(
            Object.fromEntries(
              (Array.isArray(d) ? d : d.agents || []).map((a: any) => [
                a.id,
                a.name,
              ]),
            ),
          );
      })
      .catch(() => {});
    return () => {
      alive = false;
      c.abort();
    };
  }, [props.dashboardScope, props.projectId]);
  const raw = Number(
    props.widgetSettings?.row_limit ?? props.widgetSettings?.recent_limit ?? 4,
  );
  const limit = Number.isFinite(raw)
    ? Math.max(1, Math.min(6, Math.round(raw)))
    : 4;
  const name = (id?: number) =>
    id ? names[id] || `Agent ${id}` : "Unassigned";
  const key = (x: QueueEntry) =>
    `${x.assignment ? "assignment" : "run"}:${x.item.process_id}:${x.item.id}`;
  const queue = data ? overviewQueue(data, props.widgetSettings) : [];
  const filtered = queue.filter((x) => view === "all" || x.group === view);
  const shown = filtered.slice(0, limit);
  const detail = queue.find((x) => key(x) === selected);
  const badge = (state: string) => (
    <span
      className={`po-badge ${state === "sync pending" ? "attention" : state}`}
    >
      {state === "sync pending" ? "Attention" : state.replaceAll("_", " ")}
    </span>
  );
  const relative = (value?: string) => {
    const time = Date.parse(value || "");
    if (!Number.isFinite(time)) return "—";
    const delta = time - Date.now(),
      abs = Math.abs(delta);
    if (abs < 60000) return delta > 0 ? "Soon" : "Just now";
    const amount =
      abs < 3600000
        ? `${Math.floor(abs / 60000)}m`
        : abs < 86400000
          ? `${Math.floor(abs / 3600000)}h`
          : `${Math.floor(abs / 86400000)}d`;
    return delta > 0 ? `in ${amount}` : `${amount} ago`;
  };
  return (
    <section aria-label="Processes overview" className="po-widget">
      <style>{css}</style>
      <header>
        <h2>Processes</h2>
        <p>
          {props.dashboardScope === "global"
            ? "Running flows, upcoming schedules, and recent outcomes across projects"
            : "Running flows, upcoming schedules, and recent outcomes"}
        </p>
        {props.dashboardScope === "global" && data?.projects?.length ? (
          <label className="po-project-filter">
            <span>Project</span>
            <select
              aria-label="Filter by project"
              value={projectFilter}
              onChange={(e) => {
                setProjectFilter(e.target.value);
                setSelected(null);
              }}
            >
              <option value="">All projects</option>
              {data.projects.map((project) => (
                <option key={project.id} value={project.id}>
                  {project.name || project.id}
                </option>
              ))}
            </select>
          </label>
        ) : null}
      </header>
      <nav className="po-filters" aria-label="Filter processes">
        {[
          ["all", "All"],
          ["running", "Running"],
          ["scheduled", "Scheduled"],
          ["history", "History"],
        ].map(([value, label]) => (
          <button
            key={value}
            type="button"
            aria-pressed={view === value}
            onClick={() => {
              setView(value);
              setSelected(null);
            }}
          >
            {label}
          </button>
        ))}
      </nav>
      {error && (
        <p role="alert" className="po-warning">
          {error}
          {data ? " · Showing the last received snapshot." : ""}{" "}
          <button className="po-close" onClick={() => setRefresh((v) => v + 1)}>
            Retry
          </button>
        </p>
      )}
      {!props.projectId && props.dashboardScope !== "global" ? (
        <p className="po-empty">Select a project to see its processes.</p>
      ) : !data ? (
        <p role="status" className="po-empty">
          {error ? "Overview unavailable." : "Loading processes…"}
        </p>
      ) : (
        <>
          {!!data.warnings.length && (
            <p className="po-warning">{data.warnings.join(" ")}</p>
          )}
          <div className="po-queue" aria-label="Process executions">
            {shown.map((entry) => {
              const x = entry.item;
              const status = executionStatus(entry);
              const identity = [
                x.project_name || x.project_id,
                x.assignment_name || name(x.agent_id),
              ]
                .filter(Boolean)
                .join(" · ");
              const progress = x.steps_total
                ? `${x.steps_completed || 0}/${x.steps_total}`
                : "";
              const statusLabel =
                entry.assignment && x.next_run_at
                  ? `Next ${relative(x.next_run_at)}`
                  : status.label + (status.extra ? ` +${status.extra}` : "");
              const tone =
                status.state === "sync pending" ? "attention" : status.state;
              const stateName = stateLabel(status.state);
              // The badge carries the state; the context line never repeats it.
              const context = entry.assignment
                ? [statusLabel, x.assignment_name || name(x.agent_id)].filter(Boolean).join(" · ")
                : x.state === "completed"
                  ? x.summary || identity
                  : terminal(x.state)
                    ? x.summary || x.warning || status.label
                    : status.current
                      ? statusLabel
                      : identity;
              const at = entry.assignment ? x.next_run_at : x.created_at;
              return (
                <button
                  type="button"
                  className="po-row"
                  data-state={x.state}
                  key={key(entry)}
                  aria-haspopup="dialog"
                  aria-expanded={selected === key(entry)}
                  aria-label={`${x.process_name} · ${stateName} · ${context}`}
                  title={status.hint || `${entry.assignment ? "Next" : "Started"}: ${when(at)}\n${identity}`}
                  onClick={() => setSelected(key(entry))}
                >
                  <span className="po-copy">
                    <span className="po-title">{x.process_name}</span>
                    <span className={`po-summary${terminal(x.state) && x.state !== "completed" || ["blocked", "waiting"].includes(x.state) ? ` ${tone}` : ""}`}>
                      {context}
                    </span>
                    {!entry.assignment && !terminal(x.state) && live(x.state) && (
                      <LiveLine item={x} />
                    )}
                    {!entry.assignment && !terminal(x.state) && !!x.steps_total && (
                      <Progress done={x.steps_completed || 0} total={x.steps_total} />
                    )}
                  </span>
                  <span className="po-meta">
                    {badge(status.state)}
                    <span className="po-meta-line">
                      <time dateTime={at}>{relative(at)}</time>
                      {progress && <span aria-label={`${progress} steps`}>{progress}</span>}
                    </span>
                  </span>
                </button>
              );
            })}
            {!shown.length && (
              <p className="po-empty">
                No{" "}
                {view === "all"
                  ? "process executions"
                  : view === "history"
                    ? "past or blocked runs"
                    : view === "scheduled"
                      ? "scheduled processes"
                      : "running processes"}
                .
              </p>
            )}
          </div>
          {detail && (
            <DetailModal
              key={key(detail)}
              entry={detail}
              props={props}
              name={name}
              badge={badge}
              onClose={() => setSelected(null)}
            />
          )}
          <footer>
            <span>
              {filtered.length > limit
                ? `Showing first ${limit}`
                : `${shown.length} shown`}
            </span>
            <a
              href={overviewLink({
                ...props,
                projectId: projectFilter || props.projectId,
              })}
            >
              All processes ↗
            </a>
          </footer>
        </>
      )}
    </section>
  );
}
