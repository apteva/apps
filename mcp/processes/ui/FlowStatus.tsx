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
  for (const state of ["running", "blocked", "failed", "waiting", "ready"])
    if (values.includes(state)) return state;
  if (values.length && values.every((s) => s === "completed"))
    return "completed";
  return values.length ? "pending" : "idle";
}
export function FlowStatus({ state, label }: { state: string; label?: string }) {
  const Icon =
    state === "running"
      ? LoaderCircle
      : state === "completed"
        ? Check
        : state === "blocked"
          ? CircleAlert
          : state === "failed"
            ? CircleX
            : state === "waiting"
              ? Clock3
              : state === "ready"
                ? Play
                : CircleDashed;
  return (
    <span className="flow-status" data-state={state}>
      <Icon size={12} aria-hidden="true" />
      {(label || state).replaceAll("_", " ")}
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
