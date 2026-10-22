import { expect, test } from "bun:test";
import { verifyWorkerCleanup, type CleanupObservation } from "./worker-cleanup";
function fixture() {
  const runs = ["complete", "fail", "cancel"].map((mode, i) => ({
    id: `run-${i}`,
    assignment: { parameters: { mode } },
    state: ["completed", "failed", "cancelled"][i],
    steps: [
      {
        key: "finish",
        output: [
          "FINAL-RECEIPT-123",
          "ACCEPTED-OPERATION-456",
          "ACCEPTED-OPERATION-789",
        ][i],
      },
    ],
  }));
  const cleanup = runs.map((r) => ({
    run_id: r.id,
    attempts: 1,
    completed_at: "2026-10-22T10:00:01Z",
    last_error: "",
  }));
  const observations: CleanupObservation[] = runs.map((r) => ({
    run_id: r.id,
    thread_id: `worker-${r.id}`,
    mode: r.assignment.parameters.mode,
    seenAlive: true,
    parked: true,
    timerArmedAt: "2026-10-22T10:00:00Z",
    wakeAt: "2026-10-22T10:00:12Z",
    cancelledAt: "2026-10-22T10:00:01Z",
    cleanedAt: "2026-10-22T10:00:02Z",
    checkedUntil: "2026-10-22T10:00:20Z",
    historyPreserved: true,
    configRemoved: true,
    lateEventRejected: true,
    noLaterHistory: true,
  }));
  return {
    runs,
    cleanup,
    observations,
    notifications: runs.map((r) => ({
      run_id: r.id,
      delivered_at: "2026-10-22T10:00:03Z",
      attempts: 1,
      last_error: "",
    })),
  };
}
test("accepts three terminal outcomes with live runtime and durable cleanup evidence", () => {
  const f = fixture();
  expect(() =>
    verifyWorkerCleanup(f.runs, f.cleanup, f.observations, f.notifications),
  ).not.toThrow();
});
for (const field of [
  "seenAlive",
  "configRemoved",
  "historyPreserved",
  "lateEventRejected",
  "noLaterHistory",
] as const) {
  test(`rejects missing ${field} runtime evidence`, () => {
    const f = fixture();
    f.observations[0][field] = false;
    expect(() =>
      verifyWorkerCleanup(f.runs, f.cleanup, f.observations, f.notifications),
    ).toThrow();
  });
}
test("rejects unacknowledged cleanup", () => {
  const f = fixture();
  f.cleanup[1].completed_at = "";
  expect(() =>
    verifyWorkerCleanup(f.runs, f.cleanup, f.observations, f.notifications),
  ).toThrow();
});
test("rejects altered in-flight receipt", () => {
  const f = fixture();
  f.runs[2].steps[0].output = "rolled back";
  expect(() =>
    verifyWorkerCleanup(f.runs, f.cleanup, f.observations, f.notifications),
  ).toThrow();
});
test("rejects cancellation with no armed timer", () => {
  const f = fixture();
  delete f.observations[2].wakeAt;
  expect(() =>
    verifyWorkerCleanup(f.runs, f.cleanup, f.observations, f.notifications),
  ).toThrow();
});
test("rejects observation that ends before the old wake", () => {
  const f = fixture();
  f.observations[2].checkedUntil = "2026-10-22T10:00:10Z";
  expect(() =>
    verifyWorkerCleanup(f.runs, f.cleanup, f.observations, f.notifications),
  ).toThrow();
});

import { verifyWorkerFinished } from "./verify-outcomes";
test("acknowledged cleanup replaces native done only for the exact worker and run", () => {
  const run = {
    id: "run-a",
    steps: [{ completed_at: "2026-10-22T10:00:01Z" }],
  };
  const worker = { agent_id: 7, thread_id: "worker-a" };
  const row = {
    run_id: run.id,
    ...worker,
    attempts: 1,
    completed_at: "2026-10-22T10:00:02Z",
    last_error: "",
  };
  expect(() => verifyWorkerFinished([], run, worker, 0, [row])).not.toThrow();
  for (const change of [
    { thread_id: "worker-b" },
    { run_id: "run-b" },
    { agent_id: 8 },
    { attempts: 0 },
    { completed_at: "" },
    { last_error: "offline" },
    { completed_at: "2026-10-22T10:00:00Z" },
  ]) {
    expect(() =>
      verifyWorkerFinished([], run, worker, 0, [{ ...row, ...change }]),
    ).toThrow();
  }
  expect(() =>
    verifyWorkerFinished(
      [
        {
          name: "done",
          thread_id: worker.thread_id,
          ok: true,
          completed: true,
        },
      ],
      run,
      worker,
      1,
      [row],
    ),
  ).toThrow();
});

test("rejects missing coordinator notification", () => {
  const f = fixture();
  f.notifications[0].delivered_at = "";
  expect(() =>
    verifyWorkerCleanup(f.runs, f.cleanup, f.observations, f.notifications),
  ).toThrow();
});
