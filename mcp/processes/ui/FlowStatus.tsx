import { timingLabel, useTimingNow, type TimedExecution } from "./Timing";
import {
  Check,
  CircleAlert,
  CircleDashed,
  CircleX,
  Clock3,
  LoaderCircle,
  Play,
  ShieldCheck,
  Workflow,
} from "lucide-react";

export function flowState(states: (string | undefined)[]): string {
  const values = states.filter(Boolean);
  for (const state of [
    "running",
    "blocked",
    "failed",
    "waiting",
    "ready",
    "scheduled",
  ])
    if (values.includes(state)) return state;
  if (values.length && values.every((s) => s === "completed"))
    return "completed";
  return values.length ? "pending" : "idle";
}
export function FlowStatus({
  state,
  label,
  timing,
}: {
  state: string;
  label?: string;
  timing?: TimedExecution;
}) {
  const now = useTimingNow();
  const timedLabel = timing ? timingLabel(timing, now) : "";
  const overdue =
    !!timing?.due_at &&
    Date.parse(timing.due_at) < now &&
    !["completed", "failed", "cancelled"].includes(state);
  const Icon =
    state === "running"
      ? LoaderCircle
      : state === "completed"
        ? Check
        : state === "blocked"
          ? CircleAlert
          : state === "failed"
            ? CircleX
            : state === "waiting" || state === "scheduled"
              ? Clock3
              : state === "ready"
                ? Play
                : CircleDashed;
  return (
    <span
      className="flow-status"
      data-state={overdue ? "overdue" : state}
      title={
        timing
          ? [
              timing.start_at &&
                `Earliest start: ${new Date(timing.start_at).toLocaleString()}`,
              timing.due_at &&
                `Deadline: ${new Date(timing.due_at).toLocaleString()}`,
            ]
              .filter(Boolean)
              .join(" · ")
          : undefined
      }
    >
      <Icon size={12} aria-hidden="true" />
      {(label || timedLabel || state).replaceAll("_", " ")}
    </span>
  );
}
export function StepKind({
  approval,
  index,
}: {
  approval: boolean;
  index?: number;
}) {
  const Icon = approval ? ShieldCheck : Workflow;
  return (
    <span className="flow-kind">
      <Icon size={14} aria-hidden="true" />
      {approval ? "Approval" : "Work"}
      {index !== undefined && (
        <span className="flow-number">
          {String(index + 1).padStart(2, "0")}
        </span>
      )}
    </span>
  );
}
