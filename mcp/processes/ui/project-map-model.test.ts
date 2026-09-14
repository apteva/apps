import { expect, test } from "bun:test";
import {
  layoutProject,
  overlayRuns,
  supplementalRuns,
  liveRun,
  type MapProcess,
} from "./project-map-model";
const step = (key: string, depends_on: string[] = []) => ({
  key,
  name: key,
  depends_on,
  role: "agent",
  kind: "work" as const,
  instructions: "Do it",
  expected_output: "Evidence",
});
test("packing contains every step and never overlaps boundaries for large projects", () => {
  const processes: MapProcess[] = Array.from({ length: 24 }, (_, i) => ({
    id: `p${i}`,
    name: `SOP ${i}`,
    status: "draft",
    version: 1,
    steps: Array.from({ length: (i % 8) + 1 }, (_, j) =>
      step(`s${j}`, j ? [`s${j - 1}`] : []),
    ),
  }));
  const boxes = layoutProject(processes, {});
  expect(boxes).toHaveLength(24);
  for (const a of boxes) {
    expect(Object.keys(a.positions)).toHaveLength(a.process.steps!.length);
    for (const p of Object.values(a.positions)) {
      expect(p.x + 248).toBeLessThan(a.width);
      expect(p.y + a.stepHeight).toBeLessThan(a.height);
    }
    for (const b of boxes)
      if (a !== b)
        expect(
          a.x + a.width <= b.x ||
            b.x + b.width <= a.x ||
            a.y + a.height <= b.y ||
            b.y + b.height <= a.y,
        ).toBe(true);
  }
});
test("recurring definitions and historical/unknown versions do not become current step overlays", () => {
  expect(
    liveRun({ id: "schedule", state: "pending", schedule_kind: "cron" }),
  ).toBe(false);
  expect(liveRun({ id: "once", state: "running", schedule_kind: "once" })).toBe(
    true,
  );
  const p: MapProcess = {
    id: "p",
    name: "SOP",
    status: "active",
    version: 3,
    steps: [step("s1")],
  };
  const steps = [{ key: "s1" }] as any;
  expect(
    overlayRuns(p, [
      { id: "new", state: "running", version: 3, steps },
      { id: "old", state: "running", version: 2, steps },
      { id: "unknown", state: "running", steps },
    ]).map((r) => r.id),
  ).toEqual(["new"]);
});

test("every live run appears exactly once in the overlay or supplemental set", () => {
  const p: MapProcess = {
    id: "p",
    name: "SOP",
    status: "active",
    version: 3,
    steps: [step("s1")],
  };
  const runs = [
    {
      id: "current",
      state: "running",
      version: 3,
      steps: [{ key: "s1" }] as any,
    },
    {
      id: "earlier",
      state: "running",
      version: 2,
      steps: [{ key: "old" }] as any,
    },
    { id: "no-steps", state: "blocked", version: 3 },
    { id: "unknown", state: "waiting" },
    {
      id: "mismatch",
      state: "ready",
      version: 3,
      steps: [{ key: "other" }] as any,
    },
    { id: "done", state: "completed", version: 3 },
  ];
  expect(overlayRuns(p, runs).map((r) => r.id)).toEqual(["current"]);
  expect(supplementalRuns(p, runs).map((r) => r.id)).toEqual([
    "earlier",
    "no-steps",
    "unknown",
    "mismatch",
  ]);
  const [box] = layoutProject([p], { p: runs });
  for (const position of Object.values(box.runPositions)) {
    expect(position.y + 110).toBeLessThan(box.height);
    expect(position.x + box.runWidth).toBeLessThan(box.width);
    for (const s of Object.values(box.positions))
      expect(position.y).toBeGreaterThan(s.y + box.stepHeight);
  }
});
