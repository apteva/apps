import { useMemo, useState } from "react";
import {
  HostProps,
  Task,
  TaskDetails,
  TaskRow,
  selectGroups,
  useAgentNames,
  useTasks,
} from "./taskShared";

export default function TaskOverviewWidget(props: HostProps) {
  const { tasks, loading, error, reload } = useTasks(props, { limit: "200" });
  const names = useAgentNames(props.projectId);
  const groups = useMemo(() => selectGroups(tasks), [tasks]);
  const [selected, setSelected] = useState<Task | null>(null);
  const total =
    groups.active.length + groups.upcoming.length + groups.recent.length;
  return (
    <section className="flex min-h-[28rem] flex-col overflow-hidden rounded border border-border bg-bg-card">
      <header className="flex items-center gap-2 border-b border-border px-4 py-3">
        <div>
          <h2 className="text-sm font-bold text-text">Tasks</h2>
          <p className="mt-0.5 text-[10px] text-text-dim">
            Live work, upcoming schedules, and recent outcomes
          </p>
        </div>
        <span className="rounded bg-bg-hover px-1.5 py-0.5 text-[9px] text-text-dim">
          {total}
        </span>
        <span className="ml-auto rounded border border-green/25 bg-green/10 px-1.5 py-0.5 text-[8px] font-bold uppercase text-green">
          Live
        </span>
      </header>
      {error ? (
        <p className="p-4 text-xs text-red">{error}</p>
      ) : loading ? (
        <p className="p-4 text-xs text-text-dim">Loading tasks…</p>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          <Group
            label="Active & attention"
            tasks={groups.active}
            names={names}
            onOpen={setSelected}
          />
          <Group
            label="Upcoming"
            tasks={groups.upcoming}
            names={names}
            onOpen={setSelected}
          />
          <Group
            label="Recent"
            tasks={groups.recent}
            names={names}
            onOpen={setSelected}
          />
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
}: {
  label: string;
  tasks: Task[];
  names: Map<number, string>;
  onOpen: (task: Task) => void;
}) {
  return (
    <div className="border-b border-border last:border-b-0">
      <div className="flex items-center px-4 py-2 text-[9px] font-bold uppercase tracking-wide text-text-dim">
        <span>{label}</span>
        <span className="ml-auto">{tasks.length}</span>
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
