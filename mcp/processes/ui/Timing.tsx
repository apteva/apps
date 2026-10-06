import { useEffect, useState } from "react";
import type { Step, StepRun } from "./Workflow";
export type TimingRule = {
  after: "run_start" | "step_completed";
  step_key?: string;
  offset: number;
  unit: "minutes" | "hours" | "days";
};
export function ancestors(steps: Step[], key: string): Set<string> {
  const result = new Set<string>();
  const visit = (id: string) => {
    for (const dep of steps.find((s) => s.key === id)?.depends_on || []) {
      if (!result.has(dep)) {
        result.add(dep);
        visit(dep);
      }
    }
  };
  visit(key);
  return result;
}
// Removing a dependency also removes timing rules whose anchor is no longer reachable.
export function validTimings(steps: Step[]): Step[] {
  return steps.map((s) => {
    const keys = ancestors(steps, s.key);
    const keep = (r?: TimingRule) =>
      r?.after === "step_completed" && !keys.has(r.step_key || "")
        ? undefined
        : r;
    return {
      ...s,
      start_after: keep(s.start_after),
      due_after: keep(s.due_after),
    };
  });
}
export function timingRuleText(rule: TimingRule, steps: Step[]) {
  const anchor =
    rule.after === "run_start"
      ? "run starts"
      : `${steps.find((s) => s.key === rule.step_key)?.name || rule.step_key} completes`;
  return `${rule.offset} ${rule.unit} after ${anchor}`;
}
export function TimingRules({ step, steps }: { step: Step; steps: Step[] }) {
  return (
    <>
      {step.start_after && (
        <p className="pf-hint">
          Start: {timingRuleText(step.start_after, steps)}
        </p>
      )}
      {step.due_after && (
        <p className="pf-hint">Due: {timingRuleText(step.due_after, steps)}</p>
      )}
    </>
  );
}
export function TimingEditor({
  step,
  steps,
  onChange,
}: {
  step: Step;
  steps: Step[];
  onChange: (patch: Partial<Step>) => void;
}) {
  const keys = ancestors(steps, step.key);
  return (
    <fieldset>
      <legend>Timing</legend>
      {(["start_after", "due_after"] as const).map((field) => {
        const rule = step[field];
        const label = field === "start_after" ? "Start after" : "Due after";
        const patch = (value: Partial<TimingRule>) =>
          onChange({ [field]: { ...rule!, ...value } });
        return (
          <div key={field}>
            <label className="pf-check">
              <input
                type="checkbox"
                aria-label={`Enable ${label.toLowerCase()}`}
                checked={!!rule}
                onChange={(e) =>
                  onChange({
                    [field]: e.target.checked
                      ? {
                          after: step.depends_on.length
                            ? "step_completed"
                            : "run_start",
                          ...(step.depends_on.length
                            ? { step_key: step.depends_on[0] }
                            : {}),
                          offset: field === "start_after" ? 10 : 30,
                          unit: "minutes",
                        }
                      : undefined,
                  })
                }
              />
              {label}
            </label>
            {rule && (
              <>
                <div className="pf-two">
                  <input
                    aria-label={`${label} amount`}
                    type="number"
                    required
                    min={0}
                    max={
                      rule.unit === "days"
                        ? 365
                        : rule.unit === "hours"
                          ? 8760
                          : 525600
                    }
                    step={1}
                    value={rule.offset}
                    onChange={(e) => patch({ offset: Number(e.target.value) })}
                  />
                  <select
                    aria-label={`${label} unit`}
                    value={rule.unit}
                    onChange={(e) =>
                      patch({ unit: e.target.value as TimingRule["unit"] })
                    }
                  >
                    <option value="minutes">minutes</option>
                    <option value="hours">hours</option>
                    <option value="days">days</option>
                  </select>
                </div>
                <select
                  aria-label={`${label} reference`}
                  value={
                    rule.after === "run_start" ? "run_start" : rule.step_key
                  }
                  onChange={(e) =>
                    patch(
                      e.target.value === "run_start"
                        ? { after: "run_start", step_key: undefined }
                        : { after: "step_completed", step_key: e.target.value },
                    )
                  }
                >
                  <option value="run_start">After run starts</option>
                  {steps
                    .filter((s) => keys.has(s.key))
                    .map((s) => (
                      <option key={s.key} value={s.key}>
                        After {s.name} completes
                      </option>
                    ))}
                </select>
              </>
            )}
          </div>
        );
      })}
      <p className="pf-hint">
        Processes notifies the executor when the start time arrives. Due dates
        flag overdue work; they do not delay or cancel it. Days are 24 hours.
      </p>
    </fieldset>
  );
}
export type TimedExecution = {
  state: string;
  start_at?: string;
  due_at?: string;
  completed_at?: string;
};
export function timingLabel(s: TimedExecution, now = Date.now()): string {
  if (["completed", "failed", "cancelled"].includes(s.state)) return "";
  if (s.state === "scheduled" && s.start_at) {
    const minutes = Math.ceil((Date.parse(s.start_at) - now) / 60000);
    return minutes <= 0
      ? "Ready to notify"
      : minutes < 60
        ? `Starts in ${minutes} min`
        : `Starts in ${minutes < 1440 ? `${Math.ceil(minutes / 60)} hr` : `${Math.ceil(minutes / 1440)} days`}`;
  }
  if (s.due_at) {
    const minutes = Math.ceil((Date.parse(s.due_at) - now) / 60000);
    return minutes <= 0
      ? `Overdue ${Math.max(1, -minutes)} min`
      : minutes < 60
        ? `Due in ${minutes} min`
        : `Due in ${minutes < 1440 ? `${Math.ceil(minutes / 60)} hr` : `${Math.ceil(minutes / 1440)} days`}`;
  }
  return "";
}
export function useTimingNow() {
  const [, setTick] = useState(0);
  useEffect(() => {
    const timer = setInterval(() => setTick(value => value + 1), 15000);
    return () => clearInterval(timer);
  }, []);
  return Date.now();
}
export function TimingDetails({ step }: { step: StepRun }) {
  const now = useTimingNow();
  const label = timingLabel(step, now);
  return (
    <>
      {label && <p className="small muted">{label}</p>}
      {step.start_at && (
        <p className="small muted">
          Earliest start: {new Date(step.start_at).toLocaleString()}
        </p>
      )}
      {step.due_at && (
        <p className="small muted">
          Deadline: {new Date(step.due_at).toLocaleString()}
        </p>
      )}
    </>
  );
}
