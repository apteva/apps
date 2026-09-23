import { validTimings } from "./Timing";
import type { Step } from "./Workflow";
export const NODE_WIDTH = 236;
export const COLUMN_GAP = 328;
export const ROW_GAP = 192;
export type PositionedStep = Step & { position: { x: number; y: number } };
export function stepRanks(steps: Step[]): Map<string, number> {
  const byKey = new Map(steps.map((s) => [s.key, s]));
  const ranks = new Map<string, number>();
  const visit = (key: string, visiting = new Set<string>()): number => {
    if (ranks.has(key)) return ranks.get(key)!;
    if (visiting.has(key)) return 0;
    const next = new Set(visiting).add(key);
    const rank = Math.max(
      0,
      ...(byKey.get(key)?.depends_on || [])
        .filter((k) => byKey.has(k))
        .map((k) => visit(k, next) + 1),
    );
    ranks.set(key, rank);
    return rank;
  };
  steps.forEach((s) => visit(s.key));
  return ranks;
}
export function layoutSteps(steps: Step[], rowGap = ROW_GAP): PositionedStep[] {
  const ranks = stepRanks(steps),
    groups = new Map<number, Step[]>();
  steps.forEach((s) =>
    groups.set(ranks.get(s.key)!, [
      ...(groups.get(ranks.get(s.key)!) || []),
      s,
    ]),
  );
  const max = Math.max(1, ...[...groups.values()].map((g) => g.length));
  return steps.map((s) => {
    const group = groups.get(ranks.get(s.key)!)!;
    return {
      ...s,
      position: {
        x: ranks.get(s.key)! * COLUMN_GAP,
        y: ((max - group.length) / 2 + group.indexOf(s)) * rowGap + 100,
      },
    };
  });
}
export function canConnect(
  steps: Step[],
  source: string,
  target: string,
): boolean {
  if (
    source === target ||
    !steps.some((s) => s.key === source) ||
    !steps.some((s) => s.key === target)
  )
    return false;
  if (steps.find((s) => s.key === target)?.depends_on.includes(source))
    return false;
  const byKey = new Map(steps.map((s) => [s.key, s]));
  const seen = new Set<string>();
  const reaches = (key: string): boolean => {
    if (key === target) return true;
    if (seen.has(key)) return false;
    seen.add(key);
    return (byKey.get(key)?.depends_on || []).some(reaches);
  };
  return !reaches(source);
}
export function connectSteps(
  steps: Step[],
  source: string,
  target: string,
): Step[] {
  return canConnect(steps, source, target)
    ? steps.map((s) =>
        s.key === target ? { ...s, depends_on: [...s.depends_on, source] } : s,
      )
    : steps;
}
export function removeStep(steps: Step[], key: string): Step[] {
  return validTimings(steps
    .filter((s) => s.key !== key)
    .map((s) => ({ ...s, depends_on: s.depends_on.filter((k) => k !== key) })));
}
export function nextStepKey(steps: Step[]): string {
  let n = 1;
  while (steps.some((s) => s.key === `step_${n}`)) n++;
  return `step_${n}`;
}
export function stepProblem(s: Step): string {
  if (!s.name.trim()) return "Add a name";
  if (!s.instructions.trim()) return "Add instructions";
  if (!s.expected_output.trim()) return "Define the required output";
  if (!/^[a-zA-Z][a-zA-Z0-9_]{0,63}$/.test(s.role))
    return "Choose a valid role";
  return "";
}
