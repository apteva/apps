import { useMemo, useState } from "react";
import {
  HostProps,
  Task,
  TaskDetails,
  TaskRow,
  isTerminal,
  useTasks,
} from "./taskShared";

export default function AgentTasksWidget(props: HostProps) {
  const agentId = props.agentId || props.instanceId;
  const query = useMemo(
    () => ({ agent_id: agentId ? String(agentId) : "", limit: "100" }),
    [agentId],
  );
  const { tasks, loading, error, reload } = useTasks(props, query);
  const [selected, setSelected] = useState<Task | null>(null);
  const visible = tasks
    .filter(
      (task) =>
        !task.parent_task_id && (!isTerminal(task) || task.state === "failed"),
    )
    .slice(0, 4);
  if (!agentId) return null;
  if (props.slot === "dashboard.agent_card") {
    return (
      <section className="min-w-0">
        <div className="mb-1 flex items-center text-[9px] font-bold uppercase tracking-wide text-text-dim">
          <span>Tasks</span>
          <span className="ml-auto">{visible.length}</span>
        </div>
        {error ? (
          <p className="text-[10px] text-red">{error}</p>
        ) : loading ? (
          <p className="text-[10px] text-text-dim">Loading…</p>
        ) : visible[0] ? (
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="min-w-0 flex-1 truncate text-xs font-semibold text-text">
                {visible[0].title}
              </span>
            </div>
            <p className="mt-1 truncate text-[10px] text-text-muted">
              {visible[0].current_step ||
                visible[0].description ||
                visible[0].state}
            </p>
          </div>
        ) : (
          <p className="text-[10px] text-text-dim">No current work.</p>
        )}
      </section>
    );
  }
  return (
    <section className="overflow-hidden rounded border border-border bg-bg-card">
      <header className="flex items-center border-b border-border px-3 py-2">
        <h3 className="text-[10px] font-bold uppercase tracking-wide text-text-dim">
          Tasks
        </h3>
        <span className="ml-auto text-[9px] text-text-dim">
          {visible.length}
        </span>
      </header>
      {error ? (
        <p className="p-3 text-[10px] text-red">{error}</p>
      ) : loading ? (
        <p className="p-3 text-[10px] text-text-dim">Loading…</p>
      ) : visible.length ? (
        visible.map((task) => (
          <TaskRow key={task.id} task={task} onOpen={() => setSelected(task)} />
        ))
      ) : (
        <p className="p-3 text-[10px] text-text-dim">No current work.</p>
      )}
      {selected && (
        <TaskDetails
          props={props}
          task={selected}
          onClose={() => setSelected(null)}
          onChanged={reload}
        />
      )}
    </section>
  );
}
