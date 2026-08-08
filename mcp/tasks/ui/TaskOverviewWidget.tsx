import { useMemo, useState } from "react";
import {
  HostProps,
  Task,
  TaskDetails,
  TaskRow,
  selectGroups,
  taskOverviewPreferences,
  useAgentNames,
  useTasks,
} from "./taskShared";

export default function TaskOverviewWidget(props: HostProps) {
  const { tasks, loading, error, reload } = useTasks(props, { limit: "200" });
  const names = useAgentNames(props.projectId);
  const groups = useMemo(() => selectGroups(tasks), [tasks]);
  const preferences = taskOverviewPreferences(props.widgetSettings);
  const [selected, setSelected] = useState<Task | null>(null);
  const sections = [
    preferences.showActive
      ? { label: "Active & attention", tasks: groups.active }
      : null,
    preferences.showUpcoming
      ? { label: "Upcoming", tasks: groups.upcoming }
      : null,
    preferences.showRecent
      ? { label: "Recent", tasks: groups.recent.slice(0, preferences.recentLimit) }
      : null,
  ].filter((section): section is { label: string; tasks: Task[] } => Boolean(section));
  const full = props.widgetSize === "full";
  const fullColumns = sections.length <= 1
    ? "xl:grid-cols-1"
    : sections.length === 2
      ? "xl:grid-cols-2"
      : "xl:grid-cols-3";
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
        <div className={`min-h-0 flex-1 overflow-auto ${full ? `grid grid-cols-1 ${fullColumns}` : ""}`}>
          {sections.map((section) => (
            <Group
              key={section.label}
              label={section.label}
              tasks={section.tasks}
              names={names}
              horizontal={full}
              onOpen={setSelected}
            />
          ))}
          {sections.length === 0 && (
            <p className="p-4 text-xs text-text-dim">All task sections are hidden.</p>
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

function Group({
  label,
  tasks,
  names,
  onOpen,
  horizontal,
}: {
  label: string;
  tasks: Task[];
  names: Map<number, string>;
  onOpen: (task: Task) => void;
  horizontal?: boolean;
}) {
  return (
    <div className={`border-b border-border last:border-b-0 ${horizontal ? "xl:border-b-0 xl:border-r xl:last:border-r-0" : ""}`}>
      <div className="flex items-center px-4 py-2 text-[9px] font-bold uppercase tracking-wide text-text-dim">
        <span>{label}</span>
        {tasks.length > 0 && <span className="ml-auto">{tasks.length}</span>}
      </div>
      {tasks.length ? (
        tasks.map((task) => (
          <TaskRow
            key={task.id}
            task={task}
            agentName={names.get(task.agent_id)}
            onOpen={() => onOpen(task)}
          />
        ))
      ) : (
        <p className="px-4 pb-3 text-[10px] text-text-dim">None.</p>
      )}
    </div>
  );
}
