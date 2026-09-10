import { useCallback, useEffect, useRef, useState } from "react";
import { HostProps, StatePill, Progress, TaskDetails, taskAPI, type Task } from "./taskShared";
import { useTaskEvents } from "./taskEvents";

export default function TaskCard(props: HostProps & {
  title?: string; state?: Task["state"]; progress?: number; current_step?: string;
}) {
  const [record, setRecord] = useState<Task | null>(null);
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const request = useRef<AbortController | null>(null);
  const refresh = useCallback(async () => {
    if (!props.projectId || !props.taskId) return;
    request.current?.abort();
    const controller = new AbortController(); request.current = controller;
    try {
      const response = await taskAPI.get(props, props.taskId, controller.signal);
      if (!controller.signal.aborted) { setRecord(response.task); setError(""); }
    } catch {
      if (!controller.signal.aborted) setError("Unable to refresh task");
    }
  }, [props.appName, props.projectId, props.installId, props.taskId]);
  useEffect(() => {
    setRecord(null); setOpen(false); setError(""); void refresh();
    return () => request.current?.abort();
  }, [refresh]);
  useTaskEvents(props, refresh, !!props.taskId);
  const task: Task = record || {
    id: props.taskId || "", title: props.title || "Task", state: props.state || "queued",
    progress: props.progress, current_step: props.current_step, assigned_thread_id: "",
    agent_id: props.agentId || 0, project_id: props.projectId || "", created_at: "", updated_at: "",
  };
  return <>
    <button type="button" disabled={!task.id || !props.projectId} onClick={() => setOpen(true)}
      aria-label={`Open task ${task.title}`}
      className="w-full rounded border border-border bg-bg-card p-3 text-left hover:border-accent">
      <div className="flex items-center gap-2"><StatePill task={task} />
        <span className="min-w-0 flex-1 truncate text-xs font-semibold text-text">{task.title}</span>
        <span className="text-xs text-text-dim" aria-hidden="true">→</span>
      </div>
      {task.current_step && <p className="mt-1 text-[10px] text-text-muted">{task.current_step}</p>}
      <Progress task={task} />
      {error && <p className="mt-1 text-[10px] text-red">{error}</p>}
    </button>
    {open && <TaskDetails props={props} task={task} onClose={() => setOpen(false)} onChanged={() => void refresh()} />}
  </>;
}
