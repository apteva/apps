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
  } else if (scenario === "processes-multi-agent-workflow") {
    check(
      runs.length === 1 && runs[0].workflow && runs[0].state === "completed",
      "Expected one completed team workflow",
    );
    verifyMultiAgentRun(runs[0]);
  } else throw new Error(`No outcome verifier for ${scenario}`);
}

const teamOutputs: Record<string, string> = {
  research: "RESEARCH|page=photography|tip=window-light",
  audience: "AUDIENCE|page=photography|level=beginner",
  draft: "DRAFT|photography|beginner|window-light",
  review: "APPROVED|DRAFT|photography|beginner|window-light",
  publish: "RECEIPT|photography|beginner|window-light",
};
const teamDependencies: Record<string, string[]> = {
  research: [],
  audience: [],
  draft: ["research", "audience"],
  review: ["draft"],
  publish: ["review"],
};
export function verifyMultiAgentRun(run: any) {
  check(
    run.assignment.parameters.page === "photography",
    "Frozen page parameter changed",
  );
  const steps = run.steps;
  check(
    steps.length === 5 && new Set(steps.map((s: any) => s.key)).size === 5,
    "Expected five unique steps",
  );
  const byKey = Object.fromEntries(steps.map((s: any) => [s.key, s]));
  const coordinator = run.assignment.owner_agent_id;
  const writer = byKey.draft.executor.agent_id;
  const reviewer = byKey.review.executor.agent_id;
  check(
    [coordinator, writer, reviewer].every(
      (id) => Number.isInteger(id) && id > 0,
    ) && new Set([coordinator, writer, reviewer]).size === 3,
    "Expected three distinct agent instances",
  );
  check(
    byKey.research.executor.agent_id === coordinator &&
      byKey.publish.executor.agent_id === coordinator &&
      byKey.audience.executor.agent_id === writer,
    "Role binding mismatch",
  );
  const result = JSON.parse(run.result);
  for (const [key, output] of Object.entries(teamOutputs)) {
    const step = byKey[key];
    check(
      step?.state === "completed" &&
        step.output === output &&
        result[key] === output,
      `${key}: missing recorded output`,
    );
    check(
      step.executor.kind === "agent" &&
        step.updated_by.startsWith(`agent:${step.executor.agent_id}:`),
      `${key}: wrong reporting agent`,
    );
    check(
      run.assignment.roles[step.definition.role].agent_id ===
        step.executor.agent_id,
      `${key}: role snapshot changed`,
    );
    check(
      JSON.stringify([...(step.definition.depends_on || [])].sort()) ===
        JSON.stringify([...teamDependencies[key]].sort()),
      `${key}: dependency graph changed`,
    );
    const completed = step.events.filter((e: any) => e.state === "completed");
    const ready = step.events.find((e: any) => e.state === "ready");
    check(
      completed.length === 1 && ready && ready.id < completed[0].id,
      `${key}: duplicate or missing completion audit`,
    );
    check(
      !!step.delivered_at && !!step.execution_id,
      `${key}: no tracked event delivery`,
    );
    for (const dep of teamDependencies[key]) {
      const event = byKey[dep].events.find((e: any) => e.state === "completed");
      check(
        event && event.id < ready.id,
        `${key}: dependency released too early`,
      );
    }
  }
  check(
    byKey.review.decision === "approved" &&
      byKey.review.definition.kind === "approval",
    "Review gate was bypassed",
  );
  const roots = [byKey.research, byKey.audience];
  const readyIDs = roots.map(
    (s) => s.events.find((e: any) => e.state === "ready").id,
  );
  const completedIDs = roots.map(
    (s) => s.events.find((e: any) => e.state === "completed").id,
  );
  check(
    Math.max(...readyIDs) < Math.min(...completedIDs),
    "Parallel inputs were not both released at run start",
  );
}
export function verifyMultiAgentTrajectory(calls: any[], run: any) {
  const agents: Record<string, string> = {
    research: "primary",
    audience: "writer",
    draft: "writer",
    review: "reviewer",
    publish: "primary",
  };
  for (const step of run.steps) {
    const alias = agents[step.key];
    const doneIndex = calls.findIndex(
      (c) =>
        c.agent === alias &&
        c.node === "main" &&
        c.name === "processes_step_update" &&
        c.completed &&
        c.ok &&
        c.args.step_id === step.id &&
        c.args.state === "completed",
    );
    check(
      doneIndex >= 0,
      `${step.key}: no successful real-agent completion trace`,
    );
    const read = calls
      .slice(0, doneIndex)
      .some(
        (c) =>
          c.agent === alias &&
          c.node === "main" &&
          c.name === "processes_step_get" &&
          c.completed &&
          c.ok &&
          c.args.step_id === step.id,
      );
    check(
      read,
      `${step.key}: executor did not read its step before completion`,
    );
  }
}
