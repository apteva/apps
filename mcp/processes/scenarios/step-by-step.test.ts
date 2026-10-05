import { expect, test } from "bun:test";
import { verifyControlEvidence } from "./step-by-step";
function fixture() {
  const keys = ["prepare", "alpha", "beta", "approve", "publish"],
    deps = [[], ["prepare"], ["prepare"], ["alpha", "beta"], ["approve"]];
  const t = (ms: number) => new Date(100000 + ms).toISOString();
  const steps = keys.map((key, i) => ({
    id: key,
    key,
    definition: { depends_on: deps[i] },
    released_at: t(i * 5000 + 2000),
    delivered_at: key === "approve" ? "" : t(i * 5000 + 2100),
    completed_at: t(i * 5000 + 3000),
  }));
  const r = {
    id: "r",
    state: "completed",
    assignment: { control_mode: "step_by_step", owner_agent_id: 7 },
    steps,
  };
  const advances = steps.map((s) => ({
    step_id: s.id,
    run_id: "r",
    created_at: s.released_at,
    actor: s.key === "prepare" ? "agent:7:main" : "operator",
    request_key: s.key === "prepare" ? "release-prepare" : s.key,
  }));
  const report = {
    observations: steps
      .slice(1)
      .map((s, i) => ({
        step_id: s.id,
        key: s.key,
        held_at: t((i + 1) * 5000),
        released_at: s.released_at,
        response: { step_id: s.id },
        duplicate: { duplicate: true },
      })),
    approval: { step: { id: "approve" } },
  };
  return { runs: [r], advances, report };
}
test("controlled verifier requires durable releases and real held-state observations", () => {
  const x = fixture();
  expect(() =>
    verifyControlEvidence(x.runs, x.advances, x.report),
  ).not.toThrow();
});
for (const [name, change] of [
  [
    "automatic mode",
    (x: any) => (x.runs[0].assignment.control_mode = "automatic"),
  ],
  ["duplicate release", (x: any) => x.advances.push(x.advances[0])],
  ["missing held observation", (x: any) => x.report.observations.pop()],
  [
    "premature delivery",
    (x: any) =>
      (x.runs[0].steps[1].delivered_at = new Date(100001).toISOString()),
  ],
  [
    "premature dependency",
    (x: any) =>
      (x.runs[0].steps[0].completed_at = new Date(999999).toISOString()),
  ],
  ["wrong durable ID", (x: any) => (x.advances[1].run_id = "wrong")],
  ["worker release", (x: any) => (x.advances[1].actor = "agent:7:worker")],
  [
    "missing duplicate acknowledgement",
    (x: any) => (x.report.observations[0].duplicate.duplicate = false),
  ],
  [
    "no hold interval",
    (x: any) =>
      (x.report.observations[0].held_at = x.report.observations[0].released_at),
  ],
  ["no HTTP approval", (x: any) => (x.report.approval = null)],
] as [string, (x: any) => void][])
  test(`controlled verifier rejects ${name}`, () => {
    const x = fixture();
    change(x);
    expect(() => verifyControlEvidence(x.runs, x.advances, x.report)).toThrow();
  });
