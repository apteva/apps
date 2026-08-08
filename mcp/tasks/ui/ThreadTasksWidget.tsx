import { useMemo, useState } from "react";
import { HostProps, Task, TaskDetails, TaskRow, useTasks } from "./taskShared";

export default function ThreadTasksWidget(props: HostProps) {
  const query = useMemo(
    () => ({
      agent_id: props.agentId ? String(props.agentId) : "",
      thread_id: props.threadId || "",
      limit: "100",
    }),
    [props.agentId, props.threadId],
  );
  const { tasks, loading, error, reload } = useTasks(props, query);
  const [selected, setSelected] = useState<Task | null>(null);
  if (!props.agentId || !props.threadId) return null;
  return (
    <section className="overflow-hidden border-t border-border">
      <header className="flex items-center px-4 py-3">
        <h3 className="text-[10px] font-bold uppercase tracking-wide text-text-dim">
          Thread tasks
        </h3>
        <span className="ml-auto text-[9px] text-text-dim">{tasks.length}</span>
      </header>
      {error ? (
        <p className="px-4 pb-3 text-[10px] text-red">{error}</p>
      ) : loading ? (
        <p className="px-4 pb-3 text-[10px] text-text-dim">Loading…</p>
      ) : tasks.length ? (
        tasks
          .slice(0, 5)
          .map((task) => (
            <TaskRow
              key={task.id}
              task={task}
              onOpen={() => setSelected(task)}
            />
          ))
      ) : (
        <p className="px-4 pb-3 text-[10px] text-text-dim">
          No tasks linked to this thread.
        </p>
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
