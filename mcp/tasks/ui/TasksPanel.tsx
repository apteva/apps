import { useEffect, useMemo, useState } from "react";
import {
  Agent,
  HostProps,
  Task,
  TaskDetails,
  TaskRow,
  isSchedule,
  isTerminal,
  taskAPI,
  useAgentNames,
  useTasks,
} from "./taskShared";

type View = "active" | "attention" | "scheduled" | "completed" | "all";

export default function TasksPanel(props: HostProps) {
  const { tasks, loading, error, reload } = useTasks(props, { limit: "500" });
  const names = useAgentNames(props.projectId);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [view, setView] = useState<View>("active");
  const [agentId, setAgentId] = useState(0);
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<Task | null>(null);
  const [creating, setCreating] = useState(false);

  useEffect(() => {
    if (!props.projectId) return;
    fetch(`/api/agents?project_id=${encodeURIComponent(props.projectId)}`, {
      credentials: "same-origin",
    })
      .then((response) => (response.ok ? response.json() : []))
      .then(setAgents)
      .catch(() => setAgents([]));
  }, [props.projectId]);

  const visible = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return tasks.filter((task) => {
      if (task.parent_task_id) return false;
      if (agentId && task.agent_id !== agentId) return false;
      if (view === "active" && (isTerminal(task) || isSchedule(task)))
        return false;
      if (view === "attention" && !["blocked", "failed"].includes(task.state))
        return false;
      if (view === "scheduled" && (!isSchedule(task) || isTerminal(task)))
        return false;
      if (view === "completed" && task.state !== "completed") return false;
      if (
        needle &&
        !`${task.title} ${task.description || ""} ${task.current_step || ""} ${task.result || ""}`
          .toLowerCase()
          .includes(needle)
      )
        return false;
      return true;
    });
  }, [tasks, view, agentId, search]);

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg">
      <header className="border-b border-border px-4 py-4 sm:px-6">
        <div className="flex flex-wrap items-center gap-3">
          <div>
            <div className="flex items-center gap-2">
              <h1 className="text-lg font-bold text-text">Tasks</h1>
              <span className="rounded border border-green/25 bg-green/10 px-1.5 py-0.5 text-[8px] font-bold uppercase text-green">
                Live
              </span>
            </div>
            <p className="mt-1 text-xs text-text-dim">
              Durable work, schedules, and outcomes owned by this app.
            </p>
          </div>
          <button
            onClick={() => setCreating(true)}
            className="ml-auto rounded border border-accent bg-accent/10 px-4 py-2 text-xs font-bold text-accent hover:bg-accent/20"
          >
            + New task
          </button>
        </div>
        <div className="mt-4 flex flex-wrap gap-2">
          <select
            value={agentId}
            onChange={(event) => setAgentId(Number(event.target.value))}
            className="rounded border border-border bg-bg-input px-3 py-2 text-xs text-text"
          >
            <option value={0}>All agents</option>
            {agents.map((agent) => (
              <option key={agent.id} value={agent.id}>
                {agent.name}
              </option>
            ))}
          </select>
          <input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="Search tasks…"
            className="min-w-[14rem] flex-1 rounded border border-border bg-bg-input px-3 py-2 text-xs text-text placeholder:text-text-dim"
          />
        </div>
      </header>
      <main className="min-h-0 flex-1 overflow-auto p-4 sm:p-6">
        <div className="overflow-hidden rounded border border-border bg-bg-card">
          <nav className="flex flex-wrap gap-1 border-b border-border p-2">
            {(
              ["active", "attention", "scheduled", "completed", "all"] as View[]
            ).map((item) => (
              <button
                key={item}
                onClick={() => setView(item)}
                className={`rounded px-3 py-2 text-[10px] font-bold capitalize ${view === item ? "bg-accent/15 text-accent" : "text-text-dim hover:bg-bg-hover hover:text-text"}`}
              >
                {item === "attention" ? "Needs attention" : item}
              </button>
            ))}
            <span className="ml-auto self-center px-2 text-[9px] text-text-dim">
              {visible.length} shown
            </span>
          </nav>
          {error ? (
            <p className="p-6 text-xs text-red">{error}</p>
          ) : loading ? (
            <p className="p-6 text-xs text-text-dim">Loading tasks…</p>
          ) : visible.length ? (
            visible.map((task) => (
              <TaskRow
                key={task.id}
                task={task}
                agentName={names.get(task.agent_id)}
                onOpen={() => setSelected(task)}
              />
            ))
          ) : (
            <div className="grid min-h-64 place-items-center p-8 text-xs text-text-dim">
              No matching tasks.
            </div>
          )}
        </div>
      </main>
      {creating && (
        <NewTask
          props={props}
          agents={agents}
          initialAgent={agentId || props.instanceId || 0}
          onClose={() => setCreating(false)}
          onCreated={(task) => {
            setCreating(false);
            setSelected(task);
            void reload();
          }}
        />
      )}
      {selected && (
        <TaskDetails
          props={props}
          task={selected}
          onClose={() => setSelected(null)}
          onChanged={reload}
        />
      )}
    </div>
  );
}

function NewTask({
  props,
  agents,
  initialAgent,
  onClose,
  onCreated,
}: {
  props: HostProps;
  agents: Agent[];
  initialAgent: number;
  onClose: () => void;
  onCreated: (task: Task) => void;
}) {
  const [agentId, setAgentId] = useState(initialAgent || agents[0]?.id || 0);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [scheduleKind, setScheduleKind] = useState<
    "" | "once" | "interval" | "cron"
  >("");
  const [scheduleValue, setScheduleValue] = useState("");
  const [timezone, setTimezone] = useState("UTC");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const create = async () => {
    if (!agentId || !title.trim()) return;
    setBusy(true);
    setError("");
    try {
      let schedule: Record<string, string> | undefined;
      if (scheduleKind === "once")
        schedule = {
          kind: "once",
          at: new Date(scheduleValue).toISOString(),
          timezone,
        };
      if (scheduleKind === "interval")
        schedule = { kind: "interval", every: scheduleValue, timezone };
      if (scheduleKind === "cron")
        schedule = { kind: "cron", cron: scheduleValue, timezone };
      const response = await taskAPI.create(props, {
        agent_id: agentId,
        title: title.trim(),
        description: description.trim(),
        ...(schedule ? { schedule } : {}),
      });
      onCreated(response.task);
    } catch (reason) {
      setError(
        reason instanceof Error ? reason.message : "Unable to create task",
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <div
      className="fixed inset-0 z-[100] grid place-items-center p-4"
      role="dialog"
      aria-modal="true"
      aria-label="New task"
    >
      <button
        className="absolute inset-0 bg-black/65"
        onClick={onClose}
        aria-label="Close"
      />
      <div className="relative w-full max-w-xl rounded border border-border bg-bg-card shadow-2xl">
        <header className="flex items-center border-b border-border px-5 py-4">
          <div>
            <h2 className="text-sm font-bold text-text">New task</h2>
            <p className="mt-1 text-[10px] text-text-dim">
              Assign durable work to an agent thread.
            </p>
          </div>
          <button
            onClick={onClose}
            className="ml-auto text-lg text-text-dim hover:text-text"
          >
            ×
          </button>
        </header>
        <div className="space-y-4 p-5">
          <Field label="Agent">
            <select
              value={agentId}
              onChange={(event) => setAgentId(Number(event.target.value))}
              className="w-full rounded border border-border bg-bg-input px-3 py-2 text-xs text-text"
            >
              {agents.map((agent) => (
                <option key={agent.id} value={agent.id}>
                  {agent.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Task">
            <input
              autoFocus
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              placeholder="Prepare the weekly client briefing"
              className="w-full rounded border border-border bg-bg-input px-3 py-2 text-xs text-text placeholder:text-text-dim"
            />
          </Field>
          <Field label="Instructions (optional)">
            <textarea
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              placeholder="Outcome, constraints, and what successful completion looks like."
              className="min-h-28 w-full rounded border border-border bg-bg-input px-3 py-2 text-xs leading-5 text-text placeholder:text-text-dim"
            />
          </Field>
          <Field label="Schedule (optional)">
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
              <select
                value={scheduleKind}
                onChange={(event) => {
                  setScheduleKind(event.target.value as any);
                  setScheduleValue("");
                }}
                className="rounded border border-border bg-bg-input px-3 py-2 text-xs text-text"
              >
                <option value="">Start now</option>
                <option value="once">One time</option>
                <option value="interval">Recurring interval</option>
                <option value="cron">Recurring cron</option>
              </select>
              {scheduleKind && (
                <input
                  type={scheduleKind === "once" ? "datetime-local" : "text"}
                  value={scheduleValue}
                  onChange={(event) => setScheduleValue(event.target.value)}
                  placeholder={
                    scheduleKind === "interval"
                      ? "e.g. 1h"
                      : scheduleKind === "cron"
                        ? "0 9 * * *"
                        : ""
                  }
                  className="rounded border border-border bg-bg-input px-3 py-2 text-xs text-text placeholder:text-text-dim"
                />
              )}
              {scheduleKind && (
                <input
                  value={timezone}
                  onChange={(event) => setTimezone(event.target.value)}
                  placeholder="UTC"
                  className="rounded border border-border bg-bg-input px-3 py-2 text-xs text-text"
                />
              )}
            </div>
          </Field>
          {error && <p className="text-xs text-red">{error}</p>}
        </div>
        <footer className="flex justify-end gap-2 border-t border-border px-5 py-4">
          <button
            onClick={onClose}
            className="rounded border border-border px-4 py-2 text-xs text-text-muted hover:bg-bg-hover"
          >
            Cancel
          </button>
          <button
            disabled={
              busy ||
              !agentId ||
              !title.trim() ||
              (!!scheduleKind && !scheduleValue)
            }
            onClick={() => void create()}
            className="rounded border border-accent bg-accent/10 px-4 py-2 text-xs font-bold text-accent hover:bg-accent/20 disabled:opacity-40"
          >
            {busy ? "Creating…" : "Create task"}
          </button>
        </footer>
      </div>
    </div>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-[10px] font-bold uppercase tracking-wide text-text-dim">
        {label}
      </span>
      {children}
    </label>
  );
}
