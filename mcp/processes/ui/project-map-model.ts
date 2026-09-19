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
export type MapTrigger = {
  id: string;
  status: string;
  config?: { name?: string; topic?: string };
  sync_pending?: boolean;
  subscription_enabled?: boolean;
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
  triggers?: MapTrigger[];
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
export const ENDPOINT_WIDTH = 112;
export const ENDPOINT_HEIGHT = 40;
export const ENDPOINT_GAP = 18;
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
  endpointPositions: {
    start: { x: number; y: number };
    end: { x: number; y: number };
  };
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
  const flowX = vertical ? 22 : 22 + ENDPOINT_WIDTH + ENDPOINT_GAP;
  const flowY = vertical ? 130 + ENDPOINT_HEIGHT + ENDPOINT_GAP : 130;
  const positions: MapLayout["positions"] = {};
  for (const s of steps) {
    const rank = ranks.get(s.key)!,
      group = groups.get(rank)!;
    const lane = (breadth - group.length) / 2 + group.indexOf(s);
    positions[s.key] = {
      x: flowX + (vertical ? lane : rank) * (STEP_WIDTH + 64),
      y: flowY + (vertical ? rank : lane) * (stepHeight + 64),
    };
  }
  const stepRight =
    flowX + (vertical ? breadth - 1 : depth - 1) * (STEP_WIDTH + 64) + STEP_WIDTH;
  const stepBottom =
    flowY + (vertical ? depth - 1 : breadth - 1) * (stepHeight + 64) + stepHeight;
  const endpointY =
    flowY + Math.max(0, (stepBottom - flowY - ENDPOINT_HEIGHT) / 2);
  const endpointX =
    flowX + (stepRight - flowX - ENDPOINT_WIDTH) / 2;
  const width = vertical
    ? 44 + breadth * (STEP_WIDTH + 64) - 64
    : stepRight + ENDPOINT_GAP + ENDPOINT_WIDTH + 22;
  let height = vertical
    ? stepBottom + ENDPOINT_GAP + ENDPOINT_HEIGHT + 22
    : stepBottom + 22;
  const other = supplementalRuns(process, runs);
  const columns = Math.max(1, Math.floor((width - 40) / (STEP_WIDTH + 16)));
  const runWidth = (width - 44 - (columns - 1) * 16) / columns;
  const runPositions: MapLayout["runPositions"] = {};
  other.forEach((run, i) => {
    runPositions[run.id] = {
      x: 22 + (i % columns) * (runWidth + 16),
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
    endpointPositions: vertical
      ? {
          start: { x: endpointX, y: 130 },
          end: { x: endpointX, y: stepBottom + ENDPOINT_GAP },
        }
      : {
          start: { x: 22, y: endpointY },
          end: { x: stepRight + ENDPOINT_GAP, y: endpointY },
        },
  };
}
// Pack the process boundaries into a few shelves while considering both
// dependency directions. A fixed "always wide" shelf makes every short SOP a
// very thin horizontal strip and forces long SOPs to be shrunk by fitView. The
// area target below lets compact flows stay horizontal while turning longer
// flows vertical when that gives the project map a better overall shape.
export function layoutProject(
  processes: MapProcess[],
  runs: Record<string, MapRun[]>,
): MapLayout[] {
  if (!processes.length) return [];
  const choices = [...processes]
    .sort((a, b) =>
      (a.category || "Uncategorized").localeCompare(b.category || "Uncategorized") ||
      a.name.localeCompare(b.name) ||
      a.id.localeCompare(b.id),
    )
    .map((p) => [shape(p, runs[p.id] || [], false), shape(p, runs[p.id] || [], true)]);
  const area = choices.reduce(
    (sum, pair) => sum + Math.min(...pair.map((shape) => shape.width * shape.height)),
    0,
  );
  let best: MapLayout[] = [];
  let bestScore = Infinity;
  // The canvas is normally wider than it is tall. Trying several target widths
  // keeps the packing stable as the number and size of SOPs changes, without
  // hard-coding the viewport width into this pure layout function.
  for (const factor of [0.7, 0.9, 1.1, 1.3, 1.6, 2]) {
    const targetWidth = Math.max(
      Math.sqrt(area) * factor,
      Math.min(2400, processes.length * 360),
    );
    let x = 0;
    let y = 0;
    let rowHeight = 0;
    let packedWidth = 0;
    let rowCount = 1;
    const result: MapLayout[] = [];
    for (const pair of choices) {
      const horizontal = pair.find((candidate) => !candidate.vertical)!;
      const vertical = pair.find((candidate) => candidate.vertical)!;
      // Keep a process horizontal whenever it fits on the current shelf. If
      // only the vertical shape fits, rotate that process instead of creating
      // another shelf; otherwise start a new shelf with the readable
      // horizontal shape. This gives the project map a deliberate mix rather
      // than making every process share one orientation.
      let selected = horizontal;
      if (x && x + horizontal.width > targetWidth && x + vertical.width <= targetWidth)
        selected = vertical;
      const needsNewRow = x > 0 && x + selected.width > targetWidth;
      if (needsNewRow) {
        y += rowHeight + 48;
        x = 0;
        rowHeight = 0;
        rowCount++;
        selected = horizontal.width <= targetWidth ? horizontal : vertical;
      }
      result.push({ ...selected, x, y });
      x += selected.width + 48;
      rowHeight = Math.max(rowHeight, selected.height);
      packedWidth = Math.max(packedWidth, x - 48);
    }
    const packedHeight = y + rowHeight;
    const score =
      packedWidth * packedHeight +
      Math.abs(Math.log(Math.max(packedWidth, 1) / Math.max(packedHeight, 1))) *
        packedWidth *
        packedHeight *
        0.35 +
      rowCount * targetWidth * 120;
    if (score < bestScore) {
      bestScore = score;
      best = result;
    }
  }
  return best;
}
