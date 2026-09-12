export function check(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}
/** Checks authoritative persisted history, never the model's claim or telemetry preview. */
export function verifyHistory(scenario: string, history: any) {
  const runs = history.direct_runs;
  check(Array.isArray(runs), "Missing persisted run history");
  check((history.runs || []).length === 0, "Unexpected Tasks-backed runs");
  if (scenario === "processes-direct-completion") {
    check(runs.length === 1, "Expected exactly one direct occurrence");
    check(
      runs[0].state === "completed" && runs[0].result === "Net: 800",
      "Direct result was not saved",
    );
    check(
      runs[0].backend === "agent" && !runs[0].workflow,
      "Expected standalone direct execution",
    );
  } else if (scenario === "processes-assignment-parameters") {
    check(
      runs.length === 2 &&
        new Set(runs.map((r: any) => r.assignment_id)).size === 2,
      "Expected two distinct assignments and occurrences",
    );
    for (const page of ["photography", "cooking"]) {
      const run = runs.find((r: any) => r.assignment.parameters.page === page);
      check(
        run?.state === "completed" && run.result === `Briefing for ${page}`,
        `${page}: snapshot/result mismatch`,
      );
      check(
        run.request_key === "manual:day-1" ||
          run.request_key === `${run.assignment_id}:manual:day-1`,
        "Assignments did not reuse day-1",
      );
    }
  } else if (scenario === "processes-human-approval-gate") {
    check(
      runs.length === 1 && runs[0].workflow && runs[0].state === "waiting",
      "Expected one waiting workflow",
    );
    const steps = runs[0].steps;
    check(steps.length === 3, "Expected exactly three steps");
    const byKey = Object.fromEntries(steps.map((s: any) => [s.key, s]));
    check(
      byKey.write.state === "completed" &&
        byKey.write.output === "Window light makes softer portraits.",
      "Draft output missing",
    );
    check(
      byKey.review.state === "waiting" &&
        byKey.review.executor.kind === "human" &&
        !byKey.review.decision,
      "Human review was bypassed",
    );
    check(
      byKey.publish.state === "pending" &&
        !byKey.publish.delivered_at &&
        !byKey.publish.task_id,
      "Publisher was released before approval",
    );
  } else throw new Error(`No outcome verifier for ${scenario}`);
}
