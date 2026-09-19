import { stepRanks } from "./flow-model";
import type { Step, StepRun } from "./Workflow";

export type MapAssignment = {
  id: string;
  name: string;
  target?: string;
  status?: string;
  schedule?: { kind: string };
  owner_agent_id?: number;
};
export type MapProcess = {
  id: string;
  name: string;
  description?: string;
  category?: string;
  tags?: string[];
  status: string;
  version: number;
  steps?: Step[];
  assignments?: MapAssignment[];
};
export type MapRun = {
  id: string;
  state: string;
  version?: number;
  backend?: string;
  steps?: StepRun[];
  assignment_id?: string;
  assignment?: MapAssignment;
  schedule_kind?: string;
  current_step?: string;
};
export const liveRun = (run: MapRun) =>
  !["completed", "failed", "cancelled", "canceled", "archived"].includes(
    run.state,
  ) &&
  (!run.schedule_kind || run.schedule_kind === "once");
export const runLabel = (run: MapRun) =>
  `${run.assignment?.name || run.assignment_id || "Manual"} · ${run.id.slice(-8)}`;
export function runColor(id: string) {
  let hash = 0;
  for (const c of id) hash = (hash * 31 + c.charCodeAt(0)) >>> 0;
  return ["#549cff", "#c084fc", "#2ac6a0", "#f4ad57", "#ee80ac", "#72c9de"][
    hash % 6
  ];
}
export const overlayRuns = (p: MapProcess, runs: MapRun[]) =>
  runs.filter(
    (r) =>
      liveRun(r) &&
      r.version === p.version &&
      r.steps?.some((s) => p.steps?.some((def) => def.key === s.key)),
  );
// Every live run belongs either on the shared steps or in an explicit run card.
export function supplementalRuns(p: MapProcess, runs: MapRun[]) {
  const mapped = new Set(overlayRuns(p, runs).map((r) => r.id));
  return runs.filter((r) => liveRun(r) && !mapped.has(r.id));
}
export function currentRunSteps(run: MapRun) {
  const active =
    run.steps?.filter((s) =>
      ["running", "blocked", "waiting", "ready", "scheduled"].includes(s.state),
    ) || [];
  return active.length
    ? active.map((s) => s.definition?.name || s.key).join(" · ")
    : run.current_step ||
        (run.steps?.length ? "Awaiting next step" : "No step tracking");
}
export const STEP_WIDTH = 248;
export type MapLayout = {
  process: MapProcess;
  x: number;
  y: number;
  width: number;
  height: number;
  vertical: boolean;
  stepHeight: number;
  runWidth: number;
  runPositions: Record<string, { x: number; y: number }>;
  positions: Record<string, { x: number; y: number }>;
};
function shape(
  process: MapProcess,
  runs: MapRun[],
  vertical: boolean,
): MapLayout {
  const steps = process.steps || [],
    ranks = stepRanks(steps),
    groups = new Map<number, Step[]>();
  for (const s of steps)
    groups.set(ranks.get(s.key)!, [
      ...(groups.get(ranks.get(s.key)!) || []),
      s,
    ]);
  const depth = Math.max(1, ...[...ranks.values()].map((r) => r + 1));
  const breadth = Math.max(1, ...[...groups.values()].map((g) => g.length));
  const stepHeight = 100 + overlayRuns(process, runs).length * 48;
  const positions: MapLayout["positions"] = {};
  for (const s of steps) {
    const rank = ranks.get(s.key)!,
      group = groups.get(rank)!;
    const lane = (breadth - group.length) / 2 + group.indexOf(s);
    positions[s.key] = {
      x: 28 + (vertical ? lane : rank) * (STEP_WIDTH + 64),
      y: 108 + (vertical ? rank : lane) * (stepHeight + 64),
    };
  }
  const width = 56 + (vertical ? breadth : depth) * (STEP_WIDTH + 64) - 64;
  let height = 136 + (vertical ? depth : breadth) * (stepHeight + 64) - 64;
  const other = supplementalRuns(process, runs);
  const columns = Math.max(1, Math.floor((width - 40) / (STEP_WIDTH + 16)));
  const runWidth = (width - 56 - (columns - 1) * 16) / columns;
  const runPositions: MapLayout["runPositions"] = {};
  other.forEach((run, i) => {
    runPositions[run.id] = {
      x: 28 + (i % columns) * (runWidth + 16),
      y: height + Math.floor(i / columns) * 126,
    };
  });
  if (other.length) height += Math.ceil(other.length / columns) * 126 + 12;
  return {
    process,
    x: 0,
    y: 0,
    vertical,
    stepHeight,
    positions,
    width,
    height,
    runWidth,
    runPositions,
  };
}
// Try shelf widths and both dependency directions. Penalize elongated canvases,
// keeping SOP boundaries disjoint without duplicating a graph for each run.
export function layoutProject(
  processes: MapProcess[],
  runs: Record<string, MapRun[]>,
): MapLayout[] {
  if (!processes.length) return [];
  const choices = [...processes]
    .sort((a, b) => a.id.localeCompare(b.id))
    .map((p) => [
      shape(p, runs[p.id] || [], false),
      shape(p, runs[p.id] || [], true),
    ]);
  const area = choices.reduce(
    (sum, c) => sum + Math.min(...c.map((s) => s.width * s.height)),
    0,
  );
  let best: MapLayout[] = [],
    score = Infinity;
  for (const factor of [0.7, 0.9, 1.1, 1.3, 1.6, 2]) {
    const target = Math.sqrt(area) * factor;
    let x = 0,
      y = 0,
      rowHeight = 0,
      width = 0;
    const result: MapLayout[] = [];
    for (const pair of choices) {
      const options = pair.flatMap((s) =>
        [false, true].map((newRow) => {
          const px = newRow ? 0 : x,
            py = newRow && x ? y + rowHeight + 36 : y;
          const w = Math.max(width, px + s.width),
            h = py + Math.max(newRow ? 0 : rowHeight, s.height);
          return {
            s,
            px,
            py,
            newRow,
            cost:
              Math.max(0, px + s.width - target) * 4000 +
              w * h +
              Math.abs(w - h) * 120,
          };
        }),
      );
      options.sort((a, b) => a.cost - b.cost);
      const pick = options[0];
      if (pick.newRow && x) {
        y = pick.py;
        rowHeight = 0;
      }
      result.push({ ...pick.s, x: pick.px, y: pick.py });
      x = pick.px + pick.s.width + 36;
      rowHeight = Math.max(rowHeight, pick.s.height);
      width = Math.max(width, x - 36);
    }
    const height = y + rowHeight;
    const candidate =
      width * height * (1 + Math.abs(Math.log(width / height)) * 0.8);
    if (candidate < score) {
      score = candidate;
      best = result;
    }
  }
  return best;
}
