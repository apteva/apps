import { HostProps, StatePill, Progress } from "./taskShared";

export default function TaskCard(
  props: HostProps & {
    title?: string;
    state?: any;
    progress?: number;
    current_step?: string;
  },
) {
  const task: any = {
    id: props.taskId || "",
    title: props.title || "Task",
    state: props.state || "queued",
    progress: props.progress,
    current_step: props.current_step,
    assigned_thread_id: "",
    agent_id: props.agentId || 0,
    project_id: props.projectId || "",
    created_at: "",
    updated_at: "",
  };
  return (
    <div className="rounded border border-border bg-bg-card p-3">
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-xs font-semibold text-text">
          {task.title}
        </span>
        <StatePill task={task} />
      </div>
      {task.current_step && (
        <p className="mt-1 text-[10px] text-text-muted">{task.current_step}</p>
      )}
      <Progress task={task} />
    </div>
  );
}
