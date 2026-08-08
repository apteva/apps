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
  scheduled_for?: string;
  result?: string;
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
export function isActive(task: Task) {
  return (
    !isSchedule(task) &&
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
  if (task.schedule_kind === "once")
    return `One time · ${formatWhen(task.next_run_at || task.schedule_expression)}`;
  if (task.schedule_kind === "interval")
    return `Repeats every ${task.schedule_expression || "interval"}`;
  if (task.schedule_kind === "cron")
    return `Recurring · ${task.schedule_expression} · ${task.schedule_timezone || "UTC"}`;
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

export function StatePill({ task }: { task: Task }) {
  const label =
    isRecurring(task) && task.schedule_enabled !== false
      ? "recurring"
      : task.schedule_kind === "once" && task.schedule_enabled !== false
        ? "scheduled"
        : task.state;
  const tone =
    label === "recurring"
      ? "border-purple-400/35 bg-purple-400/10 text-purple-300"
      : label === "scheduled"
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
  if (task.progress === undefined) return null;
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
  return (
    <button
      type="button"
      onClick={onOpen}
      className="block w-full border-b border-border px-4 py-3 text-left last:border-b-0 hover:bg-bg-hover/60"
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
      {(task.current_step || task.description) && (
        <p className="mt-1 truncate text-[10px] text-text-muted">
          {task.current_step || task.description}
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
                    <span className="text-text">{eventLabel(event)}</span>
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
                  <TaskRow key={run.id} task={run} />
                ))}
              </div>
            </section>
          )}
        </div>
        {!isTerminal(current) && (
          <footer className="flex justify-end gap-2 border-t border-border p-4">
            {isSchedule(current) && (
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

function eventLabel(event: TaskEvent) {
  if (event.event_type === "created") return "Task created";
  if (event.event_type === "state_changed")
    return `State changed${event.to_state ? ` to ${event.to_state}` : ""}`;
  if (event.event_type === "schedule_paused") return "Schedule paused";
  if (event.event_type === "schedule_resumed") return "Schedule resumed";
  if (event.event_type === "schedule_run_requested") return "Run requested";
  if (event.event_type === "occurrence_skipped_overlap")
    return "Occurrence skipped to avoid overlap";
  return event.event_type.replaceAll("_", " ");
}

export function selectGroups(tasks: Task[]) {
  const roots = tasks.filter((task) => !task.parent_task_id);
  return {
    active: roots.filter((task) => isActive(task) || task.state === "failed"),
    upcoming: roots.filter((task) => isSchedule(task) && !isTerminal(task)),
    recent: roots.filter((task) => isTerminal(task)).slice(0, 8),
  };
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
