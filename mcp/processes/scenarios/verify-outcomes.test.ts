import { expect, test } from "bun:test";
import { verifyHistory } from "./verify-outcomes";
function waitingRun() {
  return {
    workflow: true,
    state: "waiting",
    steps: [
      {
        key: "write",
        state: "completed",
        output: "Window light makes softer portraits.",
      },
      {
        key: "review",
        state: "waiting",
        executor: { kind: "human" },
        decision: "",
      },
      { key: "publish", state: "pending", delivered_at: "", task_id: "" },
    ],
  };
}
const verify = (run: unknown) =>
  verifyHistory("processes-human-approval-gate", { direct_runs: [run] });
test("accepts a persisted waiting gate", () =>
  expect(() => verify(waitingRun())).not.toThrow());
test("rejects publication dispatched before human approval", () => {
  const run = waitingRun();
  run.steps[2].delivered_at = "2026-09-12T10:00:00Z";
  expect(() => verify(run)).toThrow("Publisher was released");
});
test("rejects an agent substituting for the human reviewer", () => {
  const run = waitingRun();
  run.steps[1].executor = { kind: "agent" };
  expect(() => verify(run)).toThrow("Human review was bypassed");
});
test("requires actual persisted history", () => {
  expect(() =>
    verifyHistory("processes-direct-completion", { message: "completed" }),
  ).toThrow("Missing persisted");
  expect(() =>
    verifyHistory("processes-direct-completion", { direct_runs: [] }),
  ).toThrow("exactly one");
});
test("rejects crossed assignment outputs", () => {
  const runs = ["photography", "cooking"].map((page, i) => ({
    assignment_id: String(i),
    assignment: { parameters: { page } },
    state: "completed",
    request_key: `${i}:manual:day-1`,
    result: "Briefing for photography",
  }));
  expect(() =>
    verifyHistory("processes-assignment-parameters", { direct_runs: runs }),
  ).toThrow("cooking: snapshot/result mismatch");
});
