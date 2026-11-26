import { timingLabel, useTimingNow } from "./Timing";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { eventsForStep, useWorkerActivity } from "./execution-activity";
import { callsForExecution, resolveToolSource, type ToolSource } from "./ExecutionTools";
import { eventTitle, scheduleTitle } from "./start-labels";

type Props = {
  appName?: string;
  projectId?: string;
  installId?: number;
  dashboardScope?: "project" | "global";
  eventRevision?: number;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
  /** Dashboard context; projects carry Project Settings colours (newer dashboards). */
  widgetContext?: { projects?: Array<{ id: string; name?: string; color?: string }> };
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
  agent_name?: string;
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
export type AssignmentTrigger = {
  id: string;
  name: string;
  topic: string;
  source_install_id: number;
  status: string;
  subscription_enabled: boolean;
  sync_error?: string;
};
/** An active assignment as the overview lists it (Processes 0.25.0+). */
export type OverviewAssignment = {
  id: string;
  project_id?: string;
  project_name?: string;
  process_id: string;
  process_name: string;
  name: string;
  agent_id: number;
  agent_name?: string;
  start: "schedule" | "event" | "manual";
  schedule?: Item["schedule"];
  next_run_at?: string;
  triggers: AssignmentTrigger[];
  ongoing_count: number;
  live_run_id?: string;
  /** State of the open run the row links to (0.25.1+): running, waiting, blocked... */
  live_run_state?: string;
  last_run_id?: string;
  last_run_state?: string;
  last_run_at?: string;
  warning?: string;
  needs_attention: boolean;
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
    assignments?: number;
    event_driven?: number;
    assignment_issues?: number;
  };
  assignments?: OverviewAssignment[];
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
type TelemetryEvents = Parameters<typeof eventsForStep>[0];
export type StepAction = {
  id: string;
  tool: string;
  label: string;
  time: string;
  durationMs?: number;
  status: "running" | "completed" | "failed";
  args: Record<string, unknown>;
  result?: unknown;
};
export type ModelTurn = {
  id: string;
  start: string;
  end?: string;
  text: string;
  status: "running" | "completed" | "failed";
};
export type StepActivity = {
  actions: StepAction[];
  turns: ModelTurn[];
  /** Latest finished model message: the card's thought line. */
  thought: string;
  /** Start of the model turn in progress, when the worker is thinking now. */
  thinkingSince?: string;
  firstEventAt?: string;
  lastEventAt?: string;
  modelMs: number;
  toolMs: number;
};
const MODEL_EVENTS = ["llm.start", "llm.thinking", "llm.chunk", "llm.done", "llm.error", "llm.cancelled"];
const clean = (text: string) => text.replace(/\s+/g, " ").trim();
const baseName = (name: string) => name.replace(/^processes_/, "");
/** Plain words for a tool call: the agent's stated reason, or a fixed verb
 * for Processes' own protocol calls. The raw tool name stays in tooltips. */
export function actionLabel(tool: string, reason: string, args: Record<string, unknown> = {}) {
  switch (baseName(tool)) {
    case "step_update": {
      const state = String(args.state || "");
      return state === "blocked" ? "Reported blocker" : state === "waiting" ? "Started waiting" : "Completed step";
    }
    case "run_update":
      return "Completed run";
    case "step_evidence_save":
      return "Saved evidence";
    case "summary_update":
      return "Saved run summary";
    case "step_claim":
      return "Claimed step";
    case "step_read":
      return reason || "Read earlier result";
    case "pace":
      return "Waiting";
    case "search_tools":
      return reason || "Looked up tools";
  }
  return reason || tool.replace(/_/g, " ");
}
/** Events of one worker execution: execution-tagged events plus, for an
 * isolated worker (its thread serves one step and never claims it), the
 * thread's model turns, which carry no execution ID. */
function scopedEvents(events: TelemetryEvents, target: LiveTarget) {
  let scoped = eventsForStep(events, target.executionID || "", target.stepID, target.completedAt);
  const claimed = events.some((e) => e.type === "tool.call" && String(e.data?.name || "").endsWith("step_claim"));
  if (!claimed) {
    const end = target.completedAt ? Date.parse(target.completedAt) : Infinity;
    const seen = new Set(scoped);
    const untagged = (e: TelemetryEvents[number]) => {
      const ids = e.data?.execution_ids;
      return !e.data?.execution_id && !(Array.isArray(ids) && ids.length);
    };
    const model = events.filter(
      (e) => MODEL_EVENTS.includes(e.type) && !seen.has(e) && untagged(e) && Date.parse(e.time) <= end + 2000,
    );
    // Calls made before the worker claims its event carry no execution ID yet;
    // on a dedicated worker thread they still belong to this step.
    const early = events
      .filter((e) => (e.type === "tool.call" || e.type === "tool.result") && !seen.has(e) && untagged(e) && Date.parse(e.time) <= end)
      .map((e) => ({ ...e, data: { ...e.data, execution_ids: [target.executionID] } }));
    scoped = [...scoped, ...model, ...early].sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
  }
  return scoped;
}
export function stepActivity(events: TelemetryEvents, target: LiveTarget): StepActivity {
  const scoped = scopedEvents(events, target);
  const durations = new Map<string, number>();
  for (const e of scoped) {
    if (e.type !== "tool.result") continue;
    const id = String(e.data?.id || e.data?.call_id || "");
    const ms = Number(e.data?.duration_ms);
    if (id && Number.isFinite(ms)) durations.set(id, ms);
  }
  const done = !!target.completedAt;
  const actions = callsForExecution(scoped, target.executionID || "").map((c): StepAction => ({
    id: c.id,
    tool: c.name,
    label: actionLabel(c.name, c.reason, c.args),
    time: c.time,
    durationMs: durations.get(c.id),
    // A finished step has no call in flight, even when its result went unrecorded.
    status: done && c.status === "running" ? "completed" : c.status,
    args: c.args,
    result: c.result,
  }));
  const turns = new Map<string, ModelTurn>();
  for (const e of scoped) {
    if (!MODEL_EVENTS.includes(e.type)) continue;
    const data = e.data || {};
    const id = String(data.iteration ?? data.request_id ?? e.id ?? e.time);
    const turn = turns.get(id) || { id, start: e.time, text: "", status: "running" as ModelTurn["status"] };
    if (e.type === "llm.thinking" || e.type === "llm.chunk") turn.text += String(data.text || data.chunk || "");
    if (e.type === "llm.done") {
      turn.status = "completed";
      turn.end = e.time;
      if (data.message) turn.text = String(data.message);
    }
    if (e.type === "llm.error") {
      turn.status = "failed";
      turn.end = e.time;
      turn.text = String(data.error || data.message || "Model error");
    }
    // The worker is stopped after its final report; that cancelled turn is not work.
    if (e.type === "llm.cancelled") {
      turns.delete(id);
      continue;
    }
    turns.set(id, turn);
  }
  const list = [...turns.values()].sort((a, b) => Date.parse(a.start) - Date.parse(b.start));
  const last = list[list.length - 1];
  const finished = list.filter((t) => t.status === "completed" && clean(t.text));
  const span = (t: ModelTurn) => (t.end ? Math.max(0, Date.parse(t.end) - Date.parse(t.start)) : 0);
  const times = scoped.map((e) => e.time).sort();
  return {
    actions,
    turns: list.filter((t) => t.status !== "running" || t === last),
    thought: clean(finished[finished.length - 1]?.text || ""),
    thinkingSince: !done && last?.status === "running" ? last.start : undefined,
    firstEventAt: times[0],
    lastEventAt: times[times.length - 1],
    modelMs: list.reduce((n, t) => n + span(t), 0),
    toolMs: actions.reduce((n, a) => n + (a.durationMs || 0), 0),
  };
}
const EMPTY: StepActivity = { actions: [], turns: [], thought: "", modelMs: 0, toolMs: 0 };
function useActivity(target: LiveTarget | null, enabled: boolean) {
  const { events, loading, error } = useWorkerActivity(
    target?.agentID,
    target?.threadID,
    enabled && !!target?.agentID && !!target?.threadID,
    enabled,
  );
  const activity = useMemo(() => (target ? stepActivity(events, target) : EMPTY), [events, target?.executionID, target?.stepID, target?.completedAt]);
  return { activity, loading, error };
}
/** Re-renders every second while `active`, for elapsed-time counters. */
function useNow(active: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [active]);
  return now;
}
export function duration(ms?: number) {
  if (ms === undefined || !Number.isFinite(ms)) return "";
  if (ms < 10) return "<0.01s";
  if (ms < 1000) return `${(ms / 1000).toFixed(ms < 100 ? 2 : 1)}s`;
  if (ms < 10000) return `${(ms / 1000).toFixed(1)}s`;
  if (ms < 60000) return `${Math.round(ms / 1000)}s`;
  const s = Math.round(ms / 1000);
  return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
}
function StatusIcon({ status }: { status: StepAction["status"] }) {
  if (status === "running") return <span className="po-pulse" aria-label="in progress" />;
  return (
    <svg className={`po-icon ${status}`} viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" aria-label={status === "failed" ? "failed" : "done"}>
      <path d={status === "failed" ? "M6 6l12 12M18 6L6 18" : "M5 12.5l4.5 4.5L19 7"} />
    </svg>
  );
}
function ThoughtIcon() {
  return (
    <svg className="po-icon" viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M9 18h6M10 21h4M12 3a6 6 0 0 0-3.6 10.8c.6.5 1 1.2 1 2V16h5.2v-.2c0-.8.4-1.5 1-2A6 6 0 0 0 12 3z" />
    </svg>
  );
}
const sourceCache = new Map<string, Promise<ToolSource[]>>();
/** Installed apps and connected integrations of a project, for tool icons.
 * Same sources as the Processes panel; fetched once per project. */
function loadToolSources(projectID: string) {
  let pending = sourceCache.get(projectID);
  if (!pending) {
    const query = new URLSearchParams({ project_id: projectID });
    const json = (url: string) => fetch(url, { credentials: "same-origin" }).then((r) => (r.ok ? r.json() : [])).catch(() => []);
    pending = Promise.all([json(`/api/apps?${query}`), json(`/api/connections?${query}&include_app_owned=1`)]).then(([apps, connections]) => [
      ...(Array.isArray(apps) ? apps : []).map((app: any): ToolSource => ({
        key: `app:${app.install_id || app.name}`,
        label: app.display_name || app.name,
        icon: app.icon,
        iconStyle: app.icon_style,
        aliases: [app.name, app.display_name].filter(Boolean),
        tools: app.surfaces?.mcp_tool_names || [],
      })),
      ...(Array.isArray(connections) ? connections : []).map((c: any): ToolSource => ({
        key: `integration:${c.id}`,
        label: c.app_name || c.name || c.app_slug,
        icon: c.logo,
        iconStyle: "image",
        aliases: [c.app_slug, c.app_name, c.name].filter(Boolean),
      })),
    ]);
    sourceCache.set(projectID, pending);
  }
  return pending;
}
function useToolSources(projectID?: string) {
  const [sources, setSources] = useState<ToolSource[]>([]);
  useEffect(() => {
    if (!projectID) return;
    let live = true;
    loadToolSources(projectID).then((list) => live && setSources(list));
    return () => {
      live = false;
    };
  }, [projectID]);
  return sources;
}
/** The app or integration icon for a tool, else a small wrench. */
function ToolIcon({ tool, sources }: { tool: string; sources: ToolSource[] }) {
  const source = resolveToolSource(tool, sources);
  const [failed, setFailed] = useState("");
  const title = source?.label;
  if (source?.icon && failed !== source.icon) {
    if (source.iconStyle === "monochrome")
      return (
        <span className="po-tool-icon mono" title={title} aria-hidden="true">
          <span style={{ maskImage: `url("${source.icon}")`, WebkitMaskImage: `url("${source.icon}")` }} />
          <img src={source.icon} alt="" onError={() => setFailed(source.icon!)} />
        </span>
      );
    return <img className="po-tool-icon" src={source.icon} alt="" title={title} onError={() => setFailed(source.icon!)} />;
  }
  return (
    <svg className="po-tool-icon fallback" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M14.7 6.3a4 4 0 0 0-5.4 5.4l-6 6 2 2 6-6a4 4 0 0 0 5.4-5.4l-2.4 2.4-2-2z" />
    </svg>
  );
}
/** How an assignment starts, in words: "Every day at 09:00", "CRM · Conversation
 * message received", or "Started by hand". */
export function assignmentStartText(x: OverviewAssignment, sources: ToolSource[] = []) {
  if (x.start === "schedule" && x.schedule) return scheduleTitle(x.schedule);
  const trigger = x.triggers.find((t) => t.status !== "archived");
  if (x.start === "event" && trigger) {
    const app = sources.find((s) => s.key === `app:${trigger.source_install_id}`)?.label;
    return [app, eventTitle(trigger.topic)].filter(Boolean).join(" · ");
  }
  return "Started by hand";
}
/** Event-driven assignments in a list, and those whose trigger would not fire. */
/** Badge for an assignment with an open run, named by that run's real state. */
export function assignmentRunState(x: OverviewAssignment): { state: string; label: string } | null {
  if (!x.live_run_id && x.ongoing_count <= 0) return null;
  switch (x.live_run_state) {
    case "running":
      return { state: "running", label: "Running" };
    case "ready":
    case "queued":
    case "pending":
      return { state: "ready", label: "Starting" };
    case "waiting":
    case "review":
      return { state: "waiting", label: "Waiting" };
    case "blocked":
      return { state: "blocked", label: "Blocked" };
  }
  return { state: "waiting", label: x.ongoing_count > 1 ? `${x.ongoing_count} open runs` : "Open run" };
}

/** A project's colour from the dashboard, or "" when it has none (or the
 * dashboard predates project colours). Only #rgb / #rrggbb is accepted. */
export function projectColor(props: Props, projectID?: string) {
  if (!projectID) return "";
  const color = props.widgetContext?.projects?.find((p) => p.id === projectID)?.color || "";
  return /^#(?:[0-9a-f]{3}|[0-9a-f]{6})$/i.test(color) ? color : "";
}

/** Project marker on All projects rows: colour dot (when the project has a
 * colour) next to the name, so colour is never the only cue. */
function ProjectTag({ props, id, name }: { props: Props; id?: string; name?: string }) {
  if (!name && !id) return null;
  const color = projectColor(props, id);
  return (
    <span className="po-project" data-project={id}>
      {color && <span className="po-project-dot" aria-hidden="true" style={{ backgroundColor: color }} />}
      <span className="po-project-name">{name || id}</span>
    </span>
  );
}

/** How often the All projects widget reloads; it gets no live app events. */
export const GLOBAL_REFRESH_MS = 15000;

/** Agent names a global overview carries on its rows, keyed by agent id. */
export function rowAgentNames(data: Data): Record<number, string> {
  const out: Record<number, string> = {};
  const rows: Array<{ agent_id?: number; agent_name?: string }> = [
    ...(data.active || []),
    ...(data.upcoming || []),
    ...(data.recent || []),
    ...(data.attention || []),
    ...(data.assignments || []),
  ];
  for (const x of rows) if (x.agent_id && x.agent_name) out[x.agent_id] = x.agent_name;
  return out;
}

export function eventSummary(data: Data) {
  const events = (data.assignments || []).filter((x) => x.start === "event");
  return {
    total: data.counts.event_driven ?? events.length,
    issues: events.filter((x) => x.needs_attention).length,
    installs: Array.from(new Set(events.flatMap((x) => x.triggers.map((t) => t.source_install_id)))),
  };
}
function SourceIcon({ source, kind }: { source?: ToolSource; kind: OverviewAssignment["start"] }) {
  const [failed, setFailed] = useState("");
  if (source?.icon && failed !== source.icon) {
    if (source.iconStyle === "monochrome")
      return (
        <span className="po-tool-icon po-start-icon mono" title={source.label} aria-hidden="true">
          <span style={{ maskImage: `url("${source.icon}")`, WebkitMaskImage: `url("${source.icon}")` }} />
          <img src={source.icon} alt="" onError={() => setFailed(source.icon!)} />
        </span>
      );
    return <img className="po-tool-icon po-start-icon" src={source.icon} alt="" title={source.label} onError={() => setFailed(source.icon!)} />;
  }
  const path =
    kind === "schedule"
      ? "M12 7v5l3 2M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0z"
      : kind === "event"
        ? "M13 3L4 14h7l-1 7 9-11h-7l1-7z"
        : "M8 5v14l11-7z";
  return (
    <svg className="po-tool-icon po-start-icon fallback" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={path} />
    </svg>
  );
}
/** One active assignment: how it starts, who runs it, and its last run. */
function AssignmentRow({ x, props, name, relative, badge, showProject }: {
  x: OverviewAssignment;
  props: Props;
  /** All projects with no project filter: mark each row's project. */
  showProject?: boolean;
  name: (id?: number) => string;
  relative: (value?: string) => string;
  badge: (state: string) => ReactNode;
}) {
  const sources = useToolSources(x.project_id || props.projectId);
  const trigger = x.triggers.find((t) => t.status !== "archived");
  const source = x.start === "event" && trigger ? sources.find((s) => s.key === `app:${trigger.source_install_id}`) : undefined;
  const start = assignmentStartText(x, sources);
  const runState = assignmentRunState(x);
  const state = runState ? runState.state : x.needs_attention ? "attention" : x.start === "schedule" ? "scheduled" : x.start;
  const label = runState ? runState.label : x.needs_attention ? (x.warning || "Attention").replace(/^Trigger /, "") : x.start === "schedule" ? `Next ${relative(x.next_run_at)}` : x.start === "event" ? "On event" : "Manual";
  const href = overviewLink(
    props,
    { id: x.live_run_id || x.id, process_id: x.process_id, project_id: x.project_id, assignment_id: x.id, state: "" } as Item,
    !x.live_run_id,
  );
  const context = [start, name(x.agent_id)].filter(Boolean).join(" · ");
  const last = x.last_run_at ? `Last ${relative(x.last_run_at)}` : "Never run";
  return (
    <a
      className="po-row po-assignment-row"
      href={href}
      data-start={x.start}
      title={[x.process_name, start, x.warning, trigger && `${trigger.name} · ${trigger.topic}`].filter(Boolean).join("\n")}
      aria-label={[x.name || x.process_name, label, showProject && x.project_name, context].filter(Boolean).join(" · ")}
    >
      <span className="po-copy">
        <span className="po-title-line">
          <span className="po-title">{x.name || x.process_name}</span>
          {showProject && <ProjectTag props={props} id={x.project_id} name={x.project_name} />}
        </span>
        <span className={`po-summary${x.needs_attention ? " attention" : ""}`}>
          <SourceIcon source={source} kind={x.start} />
          {context}
        </span>
      </span>
      <span className="po-meta">
        <span className={`po-badge ${state}`}>{label}</span>
        <span className="po-meta-line">
          {x.last_run_state && x.last_run_state !== "running" && <span className={`po-last ${x.last_run_state}`}>{stateLabel(x.last_run_state)}</span>}
          <span>{last}</span>
        </span>
      </span>
    </a>
  );
}
/** "All" view's one-line note about assignments that wait for events. */
function EventSummaryLine({ data, projectID, onOpen }: { data: Data; projectID?: string; onOpen: () => void }) {
  const sources = useToolSources(projectID);
  const { total, issues, installs } = eventSummary(data);
  if (!total) return null;
  return (
    <button type="button" className="po-event-summary" onClick={onOpen}>
      <span className="po-event-icons">
        {installs.slice(0, 3).map((id) => (
          <SourceIcon key={id} source={sources.find((s) => s.key === `app:${id}`)} kind="event" />
        ))}
      </span>
      <span className="po-event-text">
        {total} {total === 1 ? "assignment" : "assignments"} waiting for events
        {issues > 0 && <span className="po-event-issue"> · {issues} need attention</span>}
      </span>
      <span aria-hidden="true">→</span>
    </button>
  );
}
/** The running row's single activity line: the latest tool call with its
 * app's icon (the step name until there is one), and a live marker. The last finished
 * thought is the line's tooltip; the full timeline is in the details. */
function LiveLine({ item, projectID }: { item: Item; projectID?: string }) {
  const target = useMemo(() => liveTarget(item), [item]);
  const { activity } = useActivity(target, true);
  const sources = useToolSources(projectID);
  const latest = activity.actions[activity.actions.length - 1];
  const since = activity.thinkingSince || (!activity.actions.length && !activity.turns.length ? activity.firstEventAt || item.created_at : undefined);
  const now = useNow(!!since);
  if (!target) return null;
  const elapsed = since ? duration(Math.max(0, now - Date.parse(since))) : "";
  const marker = latest?.status === "running" ? "Running" : activity.thinkingSince ? `Thinking ${elapsed}` : since ? `Starting ${elapsed}` : "";
  const title = [target.label, latest && `${latest.label} (${latest.tool})`, activity.thought && `“${activity.thought}”`].filter(Boolean).join("\n");
  return (
    <span className="po-live" title={title}>
      <span className="po-live-text">
        {latest ? (
          <>
            <ToolIcon tool={latest.tool} sources={sources} />
            <span className={latest.status === "failed" ? "po-failed" : undefined}>{latest.label}</span>
          </>
        ) : (
          <span className="po-live-step">{target.label}</span>
        )}
      </span>
      {marker && (
        <span className="po-marker">
          <span className="po-pulse" aria-hidden="true" />
          {marker}
        </span>
      )}
    </span>
  );
}
type FeedEntry =
  | { kind: "turn"; time: string; turn: ModelTurn }
  | { kind: "action"; time: string; action: StepAction };
const pretty = (value: unknown) => {
  if (value === undefined || value === null || value === "") return "";
  if (typeof value === "string") {
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }
  return JSON.stringify(value, null, 2);
};
/** The selected step's full timeline: thoughts and actions with timings. */
function ActivityFeed({ target, running, projectID }: { target: LiveTarget; running: boolean; projectID?: string }) {
  const { activity, loading, error } = useActivity(target, true);
  const sources = useToolSources(projectID);
  const [all, setAll] = useState(false);
  const [open, setOpen] = useState("");
  const now = useNow(!!activity.thinkingSince);
  const entries: FeedEntry[] = [
    ...activity.turns.filter((t) => t.status !== "completed" || clean(t.text)).map((turn): FeedEntry => ({ kind: "turn", time: turn.start, turn })),
    ...activity.actions.map((action): FeedEntry => ({ kind: "action", time: action.time, action })),
  ].sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
  const shown = all ? entries : entries.slice(-12);
  if (!target.agentID || !target.threadID)
    return <p className="po-muted">Activity was not tracked for this step.</p>;
  const end = target.completedAt ? Date.parse(target.completedAt) : activity.lastEventAt ? Date.parse(activity.lastEventAt) : NaN;
  const wall = activity.firstEventAt && Number.isFinite(end) ? Math.max(0, (activity.thinkingSince ? now : end) - Date.parse(activity.firstEventAt)) : 0;
  const busy = activity.modelMs + activity.toolMs;
  return (
    <div className="po-feed">
      {busy > 0 && (
        <div className="po-timing">
          <span>
            {wall ? `${duration(wall)} total · ` : ""}thinking {duration(activity.modelMs)} · tools {duration(activity.toolMs)}
            {activity.actions.length ? ` · ${activity.actions.length} action${activity.actions.length === 1 ? "" : "s"}` : ""}
          </span>
          <span className="po-split" aria-hidden="true">
            <span className="model" style={{ flexGrow: activity.modelMs }} />
            <span className="tools" style={{ flexGrow: Math.max(activity.toolMs, busy * 0.01) }} />
          </span>
        </div>
      )}
      {entries.length > 12 && (
        <button type="button" className="po-link-button" onClick={() => setAll(!all)}>
          {all ? "Show latest only" : `Show all ${entries.length}`}
        </button>
      )}
      <ol>
        {shown.map((entry) => {
          if (entry.kind === "turn") {
            const t = entry.turn;
            const thinking = t.status === "running";
            return (
              <li key={`turn:${t.id}`} data-kind="thought" data-status={t.status}>
                <div className="po-feed-line">
                  {thinking ? <span className="po-pulse" aria-label="in progress" /> : <ThoughtIcon />}
                  <span className="po-feed-text" title={t.text || undefined}>
                    {thinking ? "Thinking…" : t.status === "failed" ? `Model error · ${clean(t.text)}` : clean(t.text)}
                  </span>
                  <span className="po-feed-time">
                    {thinking ? duration(Math.max(0, now - Date.parse(t.start))) : duration(t.end ? Date.parse(t.end) - Date.parse(t.start) : undefined)}
                  </span>
                </div>
              </li>
            );
          }
          const a = entry.action;
          const expanded = open === a.id;
          const args = pretty(Object.fromEntries(Object.entries(a.args).filter(([k]) => k !== "_reason")));
          const result = pretty(a.result);
          return (
            <li key={`action:${a.id}`} data-kind="action" data-status={a.status}>
              <button type="button" className="po-feed-line" aria-expanded={expanded} title={a.tool} onClick={() => setOpen(expanded ? "" : a.id)}>
                <StatusIcon status={a.status} />
                <ToolIcon tool={a.tool} sources={sources} />
                <span className="po-feed-title">{a.label}</span>
                <span className="po-feed-tool">{a.tool.replace(/_/g, " ")}</span>
                <span className="po-feed-time">{duration(a.durationMs)}</span>
              </button>
              {expanded && (
                <div className="po-call">
                  {args !== "{}" && <><strong>Arguments</strong><pre>{args}</pre></>}
                  <strong>Result</strong>
                  <pre>{result || (a.status === "running" ? "Still running." : "No result was recorded.")}</pre>
                </div>
              )}
            </li>
          );
        })}
      </ol>
      {error && <p className="po-muted" role="alert">{error}</p>}
      {!entries.length && (
        <p className="po-muted">
          {loading ? "Loading activity…" : running ? "Waiting for the worker's first thought or action." : "No activity was recorded."}
        </p>
      )}
    </div>
  );
}
const ACTIVE_STEP = ["running", "blocked", "waiting", "ready"];
/** The step(s) a run is on now: running first, then blocked, waiting, ready.
 * Parallel steps in the same state collapse to "Name +N". */
export function currentStepLabel(steps: Step[]) {
  for (const state of ACTIVE_STEP) {
    const now = steps.filter((s) => s.state === state);
    if (now.length) return now.length > 1 ? `${now[0].name} +${now.length - 1}` : now[0].name;
  }
  return "";
}
/** One segment per step: filled when done, pulsing while running, amber
 * when blocked or waiting, red when failed, grey when not reached. */
function StepBar({ steps, done, total }: { steps: Step[]; done: number; total: number }) {
  if (!steps.length) return <Progress done={done} total={total} />;
  return (
    <span className="po-stepbar" role="progressbar" aria-valuemin={0} aria-valuemax={total} aria-valuenow={done} aria-label={`${done} of ${total} steps complete`}>
      {steps.map((st) => (
        <span key={st.id} data-state={st.state} title={`${st.name} · ${stateLabel(st.state)}`} />
      ))}
    </span>
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
              <StepBar steps={steps} done={x.steps_completed || 0} total={x.steps_total} />
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
              <ActivityFeed key={`${target.executionID}:${target.stepID}`} target={target} running={running} projectID={x.project_id || props.projectId} />
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
.po-faint{opacity:.6;font-style:normal}.po-sep{margin:0 6px;opacity:.6}.po-icon.completed{color:#43c878}.po-icon.failed{color:#f08b8b}.po-widget .po-marker{flex:none;display:inline-flex;align-items:center;gap:5px;white-space:nowrap;color:#8eabff;font-variant-numeric:tabular-nums}.po-widget .po-progress{margin-top:7px}
.po-widget .po-live{display:flex;align-items:center;gap:8px;margin-top:5px;font-size:11px;line-height:18px;color:var(--color-text-muted,#aaa);min-width:0}.po-widget .po-live-text{flex:1;min-width:0;display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-widget .po-live-text>*{vertical-align:middle}.po-widget .po-live-step{color:var(--color-text-muted,#aaa)}.po-widget .po-live-text .po-tool-icon{margin-right:5px}.po-widget .po-live-text>span:last-child:not(.po-live-step){color:var(--color-text,#eceef2)}.po-failed{color:#f08b8b!important}.po-tool-icon{display:inline-block;flex:none;width:14px;height:14px;border-radius:3px;object-fit:contain;vertical-align:middle}.po-tool-icon.fallback{color:var(--color-text-muted,#aaa)}.po-tool-icon.mono{position:relative;color:var(--color-accent,#ff8c36)}.po-tool-icon.mono>span{display:block;width:100%;height:100%;background:currentColor;mask-size:contain;mask-repeat:no-repeat;mask-position:center;-webkit-mask-size:contain;-webkit-mask-repeat:no-repeat;-webkit-mask-position:center}.po-tool-icon.mono>img{position:absolute;width:1px;height:1px;opacity:0}
.po-widget .po-title-line{display:flex;align-items:baseline;gap:8px;min-width:0}.po-widget .po-title-line .po-title{flex:0 0 auto;max-width:100%;min-width:0}.po-widget .po-title-step{flex:0 1000 auto;min-width:0;font-size:12px;font-weight:500;line-height:20px;color:var(--color-text-muted,#aaa);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-stepbar{display:flex;gap:3px;height:3px;margin-top:7px}.po-modal .po-stepbar{margin-top:6px}.po-stepbar>span{flex:1;min-width:4px;border-radius:2px;background:var(--color-border,#303030)}.po-stepbar>[data-state=completed],.po-stepbar>[data-state=skipped]{background:#43c878}.po-stepbar>[data-state=running],.po-stepbar>[data-state=ready]{background:#8eabff;animation:po-breathe 3.2s ease-in-out infinite}@keyframes po-breathe{0%,100%{opacity:.9}50%{opacity:.65}}.po-stepbar>[data-state=blocked],.po-stepbar>[data-state=waiting]{background:#e3b86d}.po-stepbar>[data-state=failed]{background:#f08b8b}.po-stepbar>[data-state=cancelled]{background:var(--color-text-muted,#777);opacity:.5}@media(prefers-reduced-motion:reduce){.po-stepbar>span{animation:none!important}}
.po-timing{display:grid;gap:5px;padding:4px 12px 8px;font-size:11px;color:var(--color-text-muted,#aaa);border-bottom:1px solid var(--color-border,#303030);margin-bottom:4px}.po-split{display:flex;height:4px;border-radius:2px;overflow:hidden;background:var(--color-border,#303030)}.po-split .model{background:#8eabff80}.po-split .tools{background:#43c878}.po-feed li{display:block;padding:0}.po-feed-line{display:flex;align-items:center;gap:8px;width:100%;padding:5px 12px;font:inherit;font-size:11px;line-height:16px;color:inherit;background:transparent;border:0;text-align:left;min-width:0}button.po-feed-line{cursor:pointer}button.po-feed-line:hover{background:var(--color-bg-hover,#202020)}.po-feed li[data-kind=thought] .po-feed-text{color:var(--color-text-muted,#aaa)}.po-feed-tool{flex:none;max-width:30%;font-size:10px;color:var(--color-text-muted,#888);opacity:.8;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-feed-title{flex:1;min-width:0;max-width:none;font-weight:500}.po-feed-time{flex:none;min-width:36px;text-align:right;font-size:10px;color:var(--color-text-muted,#999);font-variant-numeric:tabular-nums}.po-call{padding:2px 12px 8px 32px;font-size:10px;color:var(--color-text-muted,#aaa)}.po-call strong{display:block;margin:4px 0 2px;font-weight:600}.po-call pre{margin:0;max-height:160px;overflow:auto;padding:6px 8px;background:var(--color-bg-hover,#1d1d1d);border-radius:3px;white-space:pre-wrap;overflow-wrap:anywhere;font-size:10px;line-height:14px}
.po-modal footer{padding:10px 16px;border-top:1px solid var(--color-border,#303030);font-size:11px}.po-modal footer a{color:var(--color-accent,#ff8c36);text-decoration:none}
@media(max-width:480px){.po-modal-backdrop{padding:0;align-items:flex-end}.po-modal{max-height:90vh;border-radius:8px 8px 0 0}}
.po-widget a.po-row{text-decoration:none;color:inherit}
.po-widget a.po-row:hover{background:var(--color-bg-hover,#1e1e1e)}
.po-widget .po-assignment-row .po-summary{display:flex;align-items:center;gap:6px}
.po-widget .po-start-icon{width:14px;height:14px;margin-right:0}
.po-widget .po-start-icon.fallback{color:var(--color-text-muted,#aaa)}
.po-widget .po-badge.event,.po-widget .po-badge.manual{color:var(--color-text-muted,#aaa)}
.po-widget .po-badge{max-width:150px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.po-widget .po-last{text-transform:capitalize}.po-widget .po-last.completed{color:#43c878}.po-widget .po-last.failed{color:#f08b8b}.po-widget .po-last.cancelled{color:#d49ab2}
.po-widget button.po-event-summary{display:flex;align-items:center;gap:8px;width:100%;padding:10px 16px;background:transparent;border:0;border-bottom:1px solid var(--color-border,#303030);color:var(--color-text-muted,#aaa);font-size:11px;text-align:left}
.po-widget button.po-event-summary:hover{background:var(--color-bg-hover,#1e1e1e);color:var(--color-text,#eceef2)}
.po-widget .po-event-icons{display:flex;gap:4px;flex:none}
.po-widget .po-event-text{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.po-widget .po-event-issue{color:#e3b86d}
.po-widget .po-project{flex:0 1 auto;display:inline-flex;align-items:center;gap:5px;min-width:0;max-width:40%;font-size:11px;font-weight:500;line-height:20px;color:var(--color-text-muted,#aaa);white-space:nowrap}
.po-widget .po-project-dot{flex:none;width:8px;height:8px;border-radius:50%}
.po-widget .po-project-name{overflow:hidden;text-overflow:ellipsis}
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
    if (global) q.set("scope", "global");
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
          // No project to list agents from: names come with the rows.
          if (global) setNames(rowAgentNames(d));
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
  // All projects gets no live app events: refresh on a timer while visible.
  useEffect(() => {
    if (props.dashboardScope !== "global") return;
    const timer = setInterval(() => {
      if (typeof document === "undefined" || document.visibilityState !== "hidden") setRefresh((n) => n + 1);
    }, GLOBAL_REFRESH_MS);
    return () => clearInterval(timer);
  }, [props.dashboardScope]);
  useEffect(() => {
    if (props.dashboardScope === "global" || !props.projectId) return;
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
  const assignmentList = data?.assignments || [];
  const assignmentsShown = assignmentList.slice(0, limit);
  const assignmentsTotal = data?.counts.assignments ?? assignmentList.length;
  const detail = queue.find((x) => key(x) === selected);
  const badge = (state: string) => (
    <span
      className={`po-badge ${state === "sync pending" ? "attention" : state}`}
    >
      {state === "sync pending" ? "Attention" : state.replaceAll("_", " ")}
    </span>
  );
  // Rows name their project only when they can come from several.
  const showProject = props.dashboardScope === "global" && !projectFilter;
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
            ? "Live runs, active assignments and recent outcomes across projects"
            : "Live runs, active assignments and recent outcomes"}
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
          ["assignments", "Assignments"],
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
          {view === "assignments" ? (
          <div className="po-queue" aria-label="Active assignments">
            {assignmentsShown.map((x) => (
              <AssignmentRow key={`${x.project_id || ""}:${x.id}`} x={x} props={props} name={name} relative={relative} badge={badge} showProject={showProject} />
            ))}
            {!assignmentsShown.length && (
              <p className="po-empty">No active assignments.</p>
            )}
          </div>
          ) : (
          <div className="po-queue" aria-label="Process executions">
            {shown.map((entry) => {
              const x = entry.item;
              const status = executionStatus(entry);
              // The project shows as its own tag (All projects) or is implied.
              const identity = [
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
                    <span className="po-title-line">
                      <span className="po-title">{x.process_name}</span>
                      {showProject && <ProjectTag props={props} id={x.project_id} name={x.project_name} />}
                      {!entry.assignment && !terminal(x.state) && currentStepLabel(x.steps || []) && (
                        <span className="po-title-step">
                          <span aria-hidden="true">›</span> {currentStepLabel(x.steps || [])}
                        </span>
                      )}
                    </span>
                    {!entry.assignment && x.state === "running" && liveTarget(x) ? (
                      <LiveLine item={x} projectID={x.project_id || props.projectId} />
                    ) : (
                      <span className={`po-summary${terminal(x.state) && x.state !== "completed" || ["blocked", "waiting"].includes(x.state) ? ` ${tone}` : ""}`}>
                        {context}
                      </span>
                    )}
                    {!entry.assignment && !terminal(x.state) && !!x.steps_total && (
                      <StepBar steps={x.steps || []} done={x.steps_completed || 0} total={x.steps_total} />
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
                    : "running processes"}
                .
              </p>
            )}
            {view === "all" && (
              <EventSummaryLine
                data={data}
                projectID={projectFilter || props.projectId}
                onOpen={() => {
                  setView("assignments");
                  setSelected(null);
                }}
              />
            )}
          </div>
          )}
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
              {view === "assignments"
                ? assignmentsTotal > assignmentsShown.length
                  ? `Showing ${assignmentsShown.length} of ${assignmentsTotal}`
                  : `${assignmentsShown.length} active`
                : filtered.length > limit
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
