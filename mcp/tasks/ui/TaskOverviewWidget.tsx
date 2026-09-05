import { useEffect, useMemo, useState } from "react";
import {
  HostProps,
  Task,
  TaskDetails,
  TaskQueueFilter,
  TaskRow,
  normalizeTaskQueueFilters,
  selectTaskQueue,
  taskOverviewPreferences,
  useAgentNames,
  useTasks,
} from "./taskShared";

export default function TaskOverviewWidget(props: HostProps) {
  const operational = useTasks(props, { limit: "100", view: "operational" });
  const recent = useTasks(props, { limit: "12", view: "recent", include_runs: "true" });
  const tasks = useMemo(() => [...operational.tasks, ...recent.tasks.filter(task => !operational.tasks.some(existing => existing.id === task.id))], [operational.tasks, recent.tasks]);
  const loading = operational.loading || recent.loading;
  const error = operational.error || recent.error;
  const reload = () => { void operational.reload(); void recent.reload(); };
  const names = useAgentNames(props.projectId);
  const preferences = taskOverviewPreferences(props.widgetSettings);
  const filterStorageKey = `apteva:tasks:overview-filters:${props.widgetId || `${props.projectId || "global"}:${props.installId || 0}`}`;
  const [filters, setFilters] = useState<TaskQueueFilter[]>(() => {
    if (typeof window === "undefined") return [];
    try {
      return normalizeTaskQueueFilters(
        JSON.parse(window.localStorage.getItem(filterStorageKey) || "[]"),
      );
    } catch {
      return [];
    }
  });
  useEffect(() => {
    try {
      window.localStorage.setItem(filterStorageKey, JSON.stringify(filters));
    } catch {
      // Filtering remains usable when browser storage is unavailable.
    }
  }, [filterStorageKey, filters]);
  const queue = useMemo(
    () => selectTaskQueue(tasks, preferences, filters),
    [
      tasks,
      preferences.showActive,
      preferences.showUpcoming,
      preferences.showRecent,
      preferences.recentLimit,
      filters,
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
      <nav
        className="flex shrink-0 gap-1 overflow-x-auto border-b border-border px-4 py-2"
        aria-label="Filter tasks"
      >
        <FilterButton
          label="All"
          active={filters.length === 0}
          onClick={() => setFilters([])}
        />
        {(["active", "scheduled", "recurring", "recent"] as TaskQueueFilter[]).map(
          (filter) => (
            <FilterButton
              key={filter}
              label={filter[0].toUpperCase() + filter.slice(1)}
              active={filters.includes(filter)}
              onClick={() =>
                setFilters((current) =>
                  current.includes(filter)
                    ? current.filter((item) => item !== filter)
                    : [...current, filter],
                )
              }
            />
          ),
        )}
      </nav>
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
          {operational.hasMore && <button disabled={loading} onClick={() => void operational.loadMore()} className="w-full p-3 text-xs text-accent">Load more work</button>}
          {queue.length === 0 && (
            <div className="flex h-full min-h-28 items-center px-4 text-xs text-text-dim">
              {filters.length > 0
                ? "No tasks match these filters."
                : "No tasks to show."}
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

function FilterButton({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`shrink-0 rounded border px-2 py-1 text-[9px] font-semibold ${
        active
          ? "border-accent/40 bg-accent/10 text-accent"
          : "border-border text-text-dim hover:bg-bg-hover hover:text-text"
      }`}
    >
      {label}
    </button>
  );
}
