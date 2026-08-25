import { useCallback, useEffect, useMemo, useState } from "react";

export interface HostProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  agentId?: number;
  instanceId?: number;
  threadId?: string;
  slot?: string;
  eventRevision?: number;
  taskId?: string;
  preview?: boolean;
  widgetId?: string;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}

export interface TaskOverviewPreferences {
  showActive: boolean;
  showUpcoming: boolean;
  showRecent: boolean;
  recentLimit: number;
}

export type TaskQueueFilter =
  | "active"
  | "scheduled"
  | "recurring"
  | "recent";

export function taskOverviewPreferences(
  settings?: Record<string, unknown>,
): TaskOverviewPreferences {
  const rawLimit = Number(settings?.recent_limit ?? 4);
  return {
    showActive: settings?.show_active !== false,
    showUpcoming: settings?.show_upcoming !== false,
    showRecent: settings?.show_recent !== false,
    recentLimit: Number.isFinite(rawLimit)
      ? Math.max(1, Math.min(12, Math.round(rawLimit)))
      : 4,
  };
}

export interface Task {
  id: string;
  agent_id: number;
  project_id: string;
  title: string;
  description?: string;
  state:
    | "queued"
    | "running"
    | "waiting"
    | "blocked"
    | "completed"
    | "failed"
    | "cancelled";
  progress?: number;
  current_step?: string;
  created_by_thread_id?: string;
  assigned_thread_id: string;
  execution_thread_id?: string;
  parent_task_id?: string;
  schedule_kind?: "once" | "interval" | "cron";
  schedule_expression?: string;
  schedule_timezone?: string;
  schedule_enabled?: boolean;
  next_run_at?: string;
  last_run_at?: string;
  last_dispatched_at?: string;
  last_occurrence_id?: string;
  last_occurrence_status?: string;
  last_error?: string;
  last_result_reference?: string;
  scheduled_for?: string;
  dispatched_at?: string;
  dispatch_attempts?: number;
  last_dispatch_attempt_at?: string;
  accepted_at?: string;
  telemetry_reference?: string;
  result?: string;
  result_reference?: string;
  error?: string;
  created_at: string;
  updated_at: string;
  started_at?: string;
  completed_at?: string;
}

export interface TaskEvent {
  id: string;
  task_id: string;
  event_type: string;
  thread_id?: string;
  from_state?: string;
  to_state?: string;
  data?: Record<string, unknown>;
  created_at: string;
}

export interface TaskResponse {
  tasks: Task[];
  enabled: boolean;
  scheduling_enabled: boolean;
}

export interface Agent {
  id: number;
  name: string;
  project_id?: string;
}

function baseURL(props: HostProps): string {
  const name = props.appName || "tasks";
  const query = new URLSearchParams();
  if (props.installId) query.set("install_id", String(props.installId));
  if (props.projectId) query.set("project_id", props.projectId);
  return `/api/apps/${encodeURIComponent(name)}/tasks?${query.toString()}`;
}

function endpoint(props: HostProps, suffix = ""): string {
  const base = baseURL(props);
  if (!suffix) return base;
  const [path, query] = base.split("?");
  return `${path}/${suffix}?${query}`;
}

async function json<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, { credentials: "same-origin", ...init });
  if (!response.ok)
    throw new Error(
      (await response.text()) || `Request failed (${response.status})`,
    );
  return response.json() as Promise<T>;
}

export const taskAPI = {
  list: (props: HostProps, params: Record<string, string> = {}) => {
    const url = new URL(baseURL(props), window.location.origin);
    for (const [key, value] of Object.entries(params))
      if (value) url.searchParams.set(key, value);
    return json<TaskResponse>(url.pathname + url.search);
  },
  get: (props: HostProps, id: string) =>
    json<{ task: Task; events: TaskEvent[] }>(
      endpoint(props, encodeURIComponent(id)),
    ),
  runs: (props: HostProps, id: string) =>
    json<{ runs: Task[] }>(endpoint(props, `${encodeURIComponent(id)}/runs`)),
  create: (props: HostProps, input: Record<string, unknown>) =>
    json<{ task: Task; created: boolean }>(baseURL(props), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),
  update: (props: HostProps, id: string, input: Record<string, unknown>) =>
    json<{ task: Task }>(endpoint(props, encodeURIComponent(id)), {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),
  cancel: (props: HostProps, id: string) =>
    json<{ task: Task }>(endpoint(props, encodeURIComponent(id)), {
      method: "DELETE",
    }),
  action: (
    props: HostProps,
    id: string,
    action: "pause" | "resume" | "run-now",
  ) =>
    json<{ task: Task }>(
      endpoint(props, `${encodeURIComponent(id)}/${action}`),
      { method: "POST" },
    ),
};

export function useTasks(
  props: HostProps,
  params: Record<string, string> = {},
) {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const paramsKey = JSON.stringify(params);
  const reload = useCallback(async () => {
    if (!props.projectId) return;
    try {
      const response = await taskAPI.list(props, params);
      setTasks(response.tasks || []);
      setError("");
    } catch (reason) {
      setError(
        reason instanceof Error ? reason.message : "Unable to load tasks",
      );
    } finally {
      setLoading(false);
    }
    // The serialized values, rather than the params object identity, define the query.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.appName, props.installId, props.projectId, paramsKey]);

  useEffect(() => {
    void reload();
  }, [reload, props.eventRevision]);
  return { tasks, loading, error, reload, setTasks };
}

export function isSchedule(task: Task) {
  return Boolean(task.schedule_kind);
}
export function isRecurring(task: Task) {
  return task.schedule_kind === "interval" || task.schedule_kind === "cron";
}
export function isTerminal(task: Task) {
  return ["completed", "failed", "cancelled"].includes(task.state);
}
export function isScheduleDefinition(task: Task) {
  return isSchedule(task) && task.state === "waiting" && !isTerminal(task);
}
export function isPendingSchedule(task: Task) {
  return (
    isScheduleDefinition(task) &&
    task.schedule_enabled === true &&
    Boolean(task.next_run_at)
  );
}
export function isPausedSchedule(task: Task) {
  return isScheduleDefinition(task) && task.schedule_enabled === false;
}
export function isActive(task: Task) {
  return (
    !isScheduleDefinition(task) &&
    ["queued", "running", "waiting", "blocked"].includes(task.state)
  );
}

export function formatWhen(value?: string) {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function relativeWhen(value?: string) {
  if (!value) return "";
  const delta = new Date(value).getTime() - Date.now();
  const abs = Math.abs(delta);
  const unit =
    abs < 60_000
      ? "second"
      : abs < 3_600_000
        ? "minute"
        : abs < 86_400_000
          ? "hour"
          : "day";
  const divisor =
    unit === "second"
      ? 1000
      : unit === "minute"
        ? 60_000
        : unit === "hour"
          ? 3_600_000
          : 86_400_000;
  return new Intl.RelativeTimeFormat(undefined, { numeric: "auto" }).format(
    Math.round(delta / divisor),
    unit,
  );
}

export function scheduleLabel(task: Task) {
  if (task.schedule_kind === "once") {
    const label = isPendingSchedule(task) ? "One time" : "One-time run";
    return `${label} · ${formatWhen(task.next_run_at || task.scheduled_for || task.schedule_expression)}`;
  }
  if (task.schedule_kind === "interval") {
    const label = isPausedSchedule(task) ? "Paused" : "Repeats";
    return `${label} every ${task.schedule_expression || "interval"}`;
  }
  if (task.schedule_kind === "cron") {
    const label = isPausedSchedule(task) ? "Paused" : "Recurring";
    return `${label} · ${task.schedule_expression} · ${task.schedule_timezone || "UTC"}`;
  }
  return "";
}

const stateTone: Record<Task["state"], string> = {
  queued: "border-blue/35 bg-blue/10 text-blue",
  running: "border-accent/35 bg-accent/10 text-accent",
  waiting: "border-purple-400/35 bg-purple-400/10 text-purple-300",
  blocked: "border-yellow/35 bg-yellow/10 text-yellow",
  completed: "border-green/35 bg-green/10 text-green",
  failed: "border-red/35 bg-red/10 text-red",
  cancelled: "border-border bg-bg-hover text-text-dim",
};

export function taskStateLabel(task: Task) {
  if (task.parent_task_id && task.state === "queued" && task.accepted_at)
    return "accepted";
  if (task.parent_task_id && task.state === "queued" && task.dispatched_at)
    return "dispatched";
  if (isTerminal(task) || ["queued", "running", "blocked"].includes(task.state))
    return task.state;
  if (isPendingSchedule(task))
    return isRecurring(task) ? "recurring" : "scheduled";
  if (isPausedSchedule(task)) return "paused";
  return task.state;
}

export function StatePill({ task }: { task: Task }) {
  const label = taskStateLabel(task);
  const tone =
    label === "recurring"
      ? "border-purple-400/35 bg-purple-400/10 text-purple-300"
      : label === "scheduled"
        ? "border-blue/35 bg-blue/10 text-blue"
      : label === "paused"
          ? "border-border bg-bg-hover text-text-dim"
        : label === "accepted"
          ? "border-accent/35 bg-accent/10 text-accent"
        : label === "dispatched"
          ? "border-blue/35 bg-blue/10 text-blue"
        : stateTone[task.state];
  return (
    <span
      className={`rounded border px-1.5 py-0.5 text-[9px] font-bold uppercase tracking-wide ${tone}`}
    >
      {label}
    </span>
  );
}

export function Progress({ task }: { task: Task }) {
  if (
    task.progress === undefined ||
    isTerminal(task) ||
    isScheduleDefinition(task)
  )
    return null;
  return (
    <div className="mt-2 flex items-center gap-2">
      <div className="h-1 flex-1 overflow-hidden rounded-full bg-bg-hover">
        <div
          className={`h-full ${task.state === "completed" ? "bg-green" : "bg-accent"}`}
          style={{ width: `${Math.max(0, Math.min(100, task.progress))}%` }}
        />
      </div>
      <span className="text-[9px] text-text-dim">{task.progress}%</span>
    </div>
  );
}

export function TaskRow({
  task,
  onOpen,
  agentName,
}: {
  task: Task;
  onOpen?: () => void;
  agentName?: string;
}) {
  const summary = taskRowSummary(task);
  const attentionTone =
    task.state === "failed"
      ? "border-l-2 border-l-red bg-red/5"
      : task.state === "blocked"
        ? "border-l-2 border-l-yellow bg-yellow/5"
        : "";
  return (
    <button
      type="button"
      onClick={onOpen}
      className={`block w-full border-b border-border px-4 py-3 text-left last:border-b-0 hover:bg-bg-hover/60 ${attentionTone}`}
    >
      <div className="flex min-w-0 items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-xs font-semibold text-text">
          {task.title}
        </span>
        <StatePill task={task} />
        <span className="shrink-0 text-[9px] text-text-dim">
          {relativeWhen(task.updated_at)}
        </span>
      </div>
      {summary && (
        <p className="mt-1 truncate text-[10px] text-text-muted">
          {summary}
        </p>
      )}
      <Progress task={task} />
      <div className="mt-1.5 flex flex-wrap gap-1.5 text-[9px] text-text-dim">
        {agentName && <span>{agentName}</span>}
        {isSchedule(task) && (
          <>
            <span>·</span>
            <span className={isRecurring(task) ? "text-purple-300" : ""}>
              {scheduleLabel(task)}
            </span>
            {task.next_run_at && task.schedule_enabled !== false && (
              <>
                <span>·</span>
                <span>{relativeWhen(task.next_run_at)}</span>
              </>
            )}
          </>
        )}
      </div>
    </button>
  );
}

export function taskRowSummary(task: Task) {
  if (isSchedule(task) && task.last_error) return `Latest workflow failed: ${task.last_error}`;
  if (isSchedule(task) && task.last_occurrence_status)
    return `Latest workflow: ${task.last_occurrence_status}`;
  if (task.error) return task.error;
  if (isTerminal(task) && task.result) return task.result;
  return task.current_step || task.description || "";
}

export function TaskDetails({
  props,
  task,
  onClose,
  onChanged,
}: {
  props: HostProps;
  task: Task;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [detail, setDetail] = useState<{
    task: Task;
    events: TaskEvent[];
  } | null>(null);
  const [runs, setRuns] = useState<Task[]>([]);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    void Promise.all([
      taskAPI.get(props, task.id),
      isSchedule(task)
        ? taskAPI.runs(props, task.id)
        : Promise.resolve({ runs: [] }),
    ]).then(([next, child]) => {
      setDetail(next);
      setRuns(child.runs);
    });
  }, [
    props.appName,
    props.installId,
    props.projectId,
    task.id,
    props.eventRevision,
  ]);
  const current = detail?.task || task;
  const action = async (name: "pause" | "resume" | "run-now" | "cancel") => {
    setBusy(true);
    try {
      if (name === "cancel") await taskAPI.cancel(props, task.id);
      else await taskAPI.action(props, task.id, name);
      onChanged();
      onClose();
    } finally {
      setBusy(false);
    }
  };
  return (
    <div
      className="fixed inset-0 z-[90]"
      role="dialog"
      aria-modal="true"
      aria-label={`Task ${task.title}`}
    >
      <button
        className="absolute inset-0 bg-black/65"
        onClick={onClose}
        aria-label="Close task"
      />
      <aside className="absolute inset-y-0 right-0 flex w-full max-w-xl flex-col border-l border-border bg-bg-card shadow-2xl">
        <header className="border-b border-border p-5">
          <div className="flex items-center gap-2">
            <StatePill task={current} />
            <button
              className="ml-auto text-lg text-text-dim hover:text-text"
              onClick={onClose}
            >
              ×
            </button>
          </div>
          <h2 className="mt-3 text-base font-bold text-text">
            {current.title}
          </h2>
          {current.description && (
            <p className="mt-2 whitespace-pre-wrap text-xs leading-5 text-text-muted">
              {current.description}
            </p>
          )}
          <Progress task={current} />
        </header>
        <div className="flex-1 space-y-5 overflow-auto p-5">
          {isSchedule(current) && (
            <section className="rounded border border-purple-400/20 bg-purple-400/5 p-3">
              <h3 className="text-[10px] font-bold uppercase tracking-wide text-purple-300">
                Schedule
              </h3>
              <p className="mt-1 text-xs text-text">{scheduleLabel(current)}</p>
              {current.next_run_at && (
                <p className="mt-1 text-[10px] text-text-muted">
                  Next: {formatWhen(current.next_run_at)}
                </p>
              )}
              <div className="mt-3 border-t border-purple-400/15 pt-2 text-[10px] text-text-muted">
                <p>Scheduler: {current.schedule_enabled === false ? "paused" : "active"}</p>
                <p>Latest workflow: {current.last_occurrence_status || "not dispatched yet"}</p>
                {current.last_dispatched_at && <p>Last dispatched: {formatWhen(current.last_dispatched_at)}</p>}
                {current.last_run_at && <p>Last accepted: {formatWhen(current.last_run_at)}</p>}
                {current.last_error && <p className="mt-1 text-red">{current.last_error}</p>}
              </div>
            </section>
          )}
          {(current.result || current.error) && (
            <section>
              <h3 className="text-[10px] font-bold uppercase tracking-wide text-text-dim">
                {current.error ? "Error" : "Result"}
              </h3>
              <p
                className={`mt-2 whitespace-pre-wrap rounded border p-3 text-xs leading-5 ${current.error ? "border-red/30 bg-red/5 text-red" : "border-border bg-bg text-text"}`}
              >
                {current.error || current.result}
              </p>
            </section>
          )}
          <section>
            <h3 className="text-[10px] font-bold uppercase tracking-wide text-text-dim">
              Timeline
            </h3>
            <div className="mt-2 space-y-1">
              {(detail?.events || []).map((event) => (
                <div
                  key={event.id}
                  className="rounded border border-border bg-bg px-3 py-2"
                >
                  <div className="flex items-center gap-2 text-[10px]">
                    <span className="text-text">{taskEventLabel(event)}</span>
                    <span className="ml-auto text-text-dim">
                      {relativeWhen(event.created_at)}
                    </span>
                  </div>
                  {event.data?.current_step ? (
                    <p className="mt-1 text-[10px] text-text-muted">
                      {String(event.data.current_step)}
                    </p>
                  ) : null}
                </div>
              ))}
            </div>
          </section>
          {runs.length > 0 && (
            <section>
              <h3 className="text-[10px] font-bold uppercase tracking-wide text-text-dim">
                Recent occurrences
              </h3>
              <div className="mt-2 overflow-hidden rounded border border-border">
                {runs.map((run) => (
                  <div key={run.id} className="border-b border-border p-3 last:border-b-0">
                    <div className="flex items-center gap-2">
                      <StatePill task={run} />
                      <span className="ml-auto text-[9px] text-text-dim">{formatWhen(run.scheduled_for || run.created_at)}</span>
                    </div>
                    <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 text-[9px] text-text-muted">
                      <span>Dispatched</span><span>{formatWhen(run.dispatched_at) || "No"}</span>
                      <span>Delivery attempts</span><span>{run.dispatch_attempts || 0}</span>
                      <span>Last attempt</span><span>{formatWhen(run.last_dispatch_attempt_at) || "No"}</span>
                      <span>Accepted</span><span>{formatWhen(run.accepted_at) || "No"}</span>
                      <span>Started</span><span>{formatWhen(run.started_at) || "No"}</span>
                      <span>Completed</span><span>{formatWhen(run.completed_at) || "No"}</span>
                    </div>
                    {run.error && <p className="mt-2 text-[10px] text-red">{run.error}</p>}
                    {run.result && <p className="mt-2 text-[10px] text-text">{run.result}</p>}
                    <div className="mt-2 flex gap-3 text-[9px] text-accent">
                      {run.telemetry_reference && <a href={run.telemetry_reference} target="_blank" rel="noreferrer">Agent telemetry</a>}
                      {run.result_reference && <span>Result: {run.result_reference}</span>}
                    </div>
                  </div>
                ))}
              </div>
            </section>
          )}
        </div>
        {!isTerminal(current) && (
          <footer className="flex justify-end gap-2 border-t border-border p-4">
            {isScheduleDefinition(current) && (
              <>
                <button
                  disabled={busy}
                  className="rounded border border-border px-3 py-2 text-xs text-text-muted hover:bg-bg-hover"
                  onClick={() =>
                    void action(
                      current.schedule_enabled === false ? "resume" : "pause",
                    )
                  }
                >
                  {current.schedule_enabled === false ? "Resume" : "Pause"}
                </button>
                <button
                  disabled={busy}
                  className="rounded border border-border px-3 py-2 text-xs text-text-muted hover:bg-bg-hover"
                  onClick={() => void action("run-now")}
                >
                  Run now
                </button>
              </>
            )}
            <button
              disabled={busy}
              className="rounded border border-red/40 px-3 py-2 text-xs text-red hover:bg-red/10"
              onClick={() => void action("cancel")}
            >
              Cancel task
            </button>
          </footer>
        )}
      </aside>
    </div>
  );
}

export function taskEventLabel(event: TaskEvent) {
  let label: string;
  if (event.event_type === "created") label = "Task created";
  else if (
    (event.event_type === "state_changed" || event.event_type === "updated") &&
    event.to_state
  ) {
    label = `${event.to_state.charAt(0).toUpperCase()}${event.to_state.slice(1)}`;
  } else if (event.event_type === "schedule_paused") label = "Schedule paused";
  else if (event.event_type === "schedule_resumed") label = "Schedule resumed";
  else if (event.event_type === "schedule_run_requested") label = "Run requested";
  else if (event.event_type === "occurrence_skipped_overlap")
    label = "Occurrence skipped to avoid overlap";
  else if (event.event_type === "occurrence_dispatched")
    label = "Occurrence dispatched";
  else if (event.event_type === "occurrence_redispatched") {
    const attempt = event.data?.dispatch_attempt;
    label =
      typeof attempt === "number"
        ? `Occurrence redispatched · attempt ${attempt}`
        : "Occurrence redispatched";
  } else if (event.event_type === "occurrence_accepted")
    label = "Occurrence accepted";
  else label = event.event_type.replaceAll("_", " ");

  const progress = event.data?.progress;
  return typeof progress === "number" && Number.isFinite(progress)
    ? `${label} · ${progress}%`
    : label;
}

export function selectGroups(tasks: Task[]) {
  const roots = tasks.filter((task) => !task.parent_task_id);
  return {
    active: roots.filter((task) => isActive(task)),
    upcoming: roots.filter((task) => isPendingSchedule(task)),
    recent: roots.filter((task) => isTerminal(task)).slice(0, 8),
  };
}

export function taskQueueRank(task: Task) {
  if (task.state === "failed") return 0;
  if (task.state === "blocked") return 1;
  if (task.state === "running") return 2;
  if (task.state === "queued") return 3;
  if (isActive(task)) return 4;
  if (isPendingSchedule(task)) return 5;
  if (isPausedSchedule(task)) return 6;
  if (task.state === "completed") return 7;
  if (task.state === "cancelled") return 8;
  return 9;
}

function taskTime(task: Task, scheduled: boolean) {
  const value = scheduled
    ? task.next_run_at || task.schedule_expression
    : task.completed_at || task.updated_at || task.created_at;
  const time = value ? new Date(value).getTime() : Number.NaN;
  return Number.isNaN(time) ? 0 : time;
}

export function selectTaskQueue(
  tasks: Task[],
  preferences: TaskOverviewPreferences = taskOverviewPreferences(),
  filters: TaskQueueFilter[] = [],
) {
  const roots = tasks.filter((task) => !task.parent_task_id);
  const operational = roots.filter((task) => {
    if (task.state === "failed" || isActive(task))
      return preferences.showActive;
    if (isPendingSchedule(task) || isPausedSchedule(task))
      return preferences.showUpcoming;
    return false;
  });
  const recent = preferences.showRecent
    ? roots
        .filter(
          (task) =>
            task.state === "completed" || task.state === "cancelled",
        )
        .sort((a, b) => taskTime(b, false) - taskTime(a, false))
        .slice(0, preferences.recentLimit)
    : [];

  const queue = [...operational, ...recent].sort((a, b) => {
    const rank = taskQueueRank(a) - taskQueueRank(b);
    if (rank !== 0) return rank;
    if (isPendingSchedule(a) && isPendingSchedule(b))
      return taskTime(a, true) - taskTime(b, true);
    return taskTime(b, false) - taskTime(a, false);
  });

  if (filters.length === 0) return queue;
  return queue.filter((task) =>
    filters.some((filter) => taskMatchesQueueFilter(task, filter)),
  );
}

export function normalizeTaskQueueFilters(value: unknown): TaskQueueFilter[] {
  if (!Array.isArray(value)) return [];
  const valid = new Set<TaskQueueFilter>([
    "active",
    "scheduled",
    "recurring",
    "recent",
  ]);
  return Array.from(
    new Set(
      value.filter(
        (item): item is TaskQueueFilter =>
          typeof item === "string" && valid.has(item as TaskQueueFilter),
      ),
    ),
  );
}

export function taskMatchesQueueFilter(
  task: Task,
  filter: TaskQueueFilter,
) {
  if (filter === "active") return task.state === "failed" || isActive(task);
  if (filter === "scheduled")
    return (
      task.schedule_kind === "once" &&
      (isPendingSchedule(task) || isPausedSchedule(task))
    );
  if (filter === "recurring")
    return isRecurring(task) && (isPendingSchedule(task) || isPausedSchedule(task));
  return task.state === "completed" || task.state === "cancelled";
}

export function useAgentNames(projectId?: string) {
  const [agents, setAgents] = useState<Agent[]>([]);
  useEffect(() => {
    if (projectId)
      void json<Agent[]>(
        `/api/agents?project_id=${encodeURIComponent(projectId)}`,
      )
        .then(setAgents)
        .catch(() => setAgents([]));
  }, [projectId]);
  return useMemo(
    () => new Map(agents.map((agent) => [agent.id, agent.name])),
    [agents],
  );
}
