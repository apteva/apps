import { useMemo, useState } from "react";
import {
  HostProps,
  Task,
  TaskDetails,
  TaskRow,
  selectTaskQueue,
  taskOverviewPreferences,
  useAgentNames,
  useTasks,
} from "./taskShared";

export default function TaskOverviewWidget(props: HostProps) {
  const { tasks, loading, error, reload } = useTasks(props, { limit: "200" });
  const names = useAgentNames(props.projectId);
  const preferences = taskOverviewPreferences(props.widgetSettings);
  const queue = useMemo(
    () => selectTaskQueue(tasks, preferences),
    [
      tasks,
      preferences.showActive,
      preferences.showUpcoming,
      preferences.showRecent,
      preferences.recentLimit,
    ],
  );
  const [selected, setSelected] = useState<Task | null>(null);
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded border border-border bg-bg-card">
      <header className="flex items-center gap-2 border-b border-border px-4 py-3">
        <div>
          <h2 className="text-sm font-bold text-text">Tasks</h2>
          <p className="mt-0.5 text-[10px] text-text-dim">
            Live work, upcoming schedules, and recent outcomes
          </p>
        </div>
      </header>
      {error ? (
        <p className="p-4 text-xs text-red">{error}</p>
      ) : loading ? (
        <p className="p-4 text-xs text-text-dim">Loading tasks…</p>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          {queue.map((task) => (
            <TaskRow
              key={task.id}
              task={task}
              agentName={names.get(task.agent_id)}
              onOpen={() => setSelected(task)}
            />
          ))}
          {queue.length === 0 && (
            <div className="flex h-full min-h-28 items-center px-4 text-xs text-text-dim">
              No tasks to show.
            </div>
          )}
        </div>
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
