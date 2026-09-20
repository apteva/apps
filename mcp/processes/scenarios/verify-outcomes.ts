export function check(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

/** Written by the runner's operator watcher; asserted here from saved state. */
export const OPERATOR_CONFIRMATION = "Operator confirmed: cleared to publish.";
const CONFIRMED_DRAFT = "Golden hour flatters every portrait.";
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
  } else if (
    [
      "processes-multi-agent-workflow",
      "processes-event-trigger-workflow",
    ].includes(scenario)
  ) {
    check(
      runs.length === 1 && runs[0].workflow && runs[0].state === "completed",
      "Expected one completed team workflow",
    );
    verifyMultiAgentRun(runs[0]);
  } else if (scenario === "processes-browser-continuity") {
    check(runs.length === 1 && runs[0].workflow && runs[0].state === "completed", "Expected one completed browser workflow");
    check(runs[0].steps.length === 3, "Expected three browser continuity steps");
    const workerIDs = new Set(runs[0].steps.map((s: any) => s.updated_by));
    check(workerIDs.size === 1 && [...workerIDs][0] !== `agent:${runs[0].assignment.owner_agent_id}:main`, "Browser steps were not completed by one isolated worker");
    for (const step of runs[0].steps) {
      check(step.state === "completed" && step.output && step.executor.kind === "agent", `${step.key}: browser step did not complete with evidence`);
    }
  } else if (scenario === "processes-sequential-worker") {
    check(runs.length === 1 && runs[0].state === "completed", "Expected one completed sequential run");
    const expected = ["WEATHER|Barcelona|22C", "CONVERSATION|Barcelona|22C", "PUSH|Barcelona|22C"];
    check(runs[0].steps.length === 3, "Expected three sequential steps");
    runs[0].steps.forEach((s: any, i: number) => {
      check(s.state === "completed" && s.output === expected[i], "Sequential output mismatch");
      check(s.executor.agent_id === runs[0].assignment.owner_agent_id, "Wrong executor");
      if (i) check(s.delivered_at >= runs[0].steps[i - 1].updated_at, "Dependency released early");
    });
  } else if (scenario === "processes-operator-confirmation") {
    check(
      runs.length === 1 && runs[0].workflow && runs[0].state === "completed",
      "Expected one completed confirmation workflow",
    );
    const steps = runs[0].steps;
    check(steps.length === 3, "Expected exactly three steps");
    const byKey = Object.fromEntries(steps.map((s: any) => [s.key, s]));
    check(
      byKey.draft.state === "completed" &&
        byKey.draft.output === CONFIRMED_DRAFT &&
        byKey.draft.executor.kind === "agent",
      "Draft was not completed by the agent",
    );
    check(
      byKey.confirm.executor.kind === "human" &&
        byKey.confirm.state === "completed" &&
        byKey.confirm.output === OPERATOR_CONFIRMATION,
      "Confirmation step did not record the operator's evidence",
    );
    check(
      byKey.confirm.updated_by === "operator",
      `Confirmation was completed by ${byKey.confirm.updated_by}, not the operator`,
    );
    check(
      !byKey.confirm.decision,
      "A work step must not carry an approval decision",
    );
    check(
      !byKey.confirm.delivered_at && !byKey.confirm.task_id,
      "A human step must never be dispatched to an executor",
    );
    check(
      byKey.publish.state === "completed" &&
        byKey.publish.output === `Published: ${CONFIRMED_DRAFT}` &&
        byKey.publish.executor.kind === "agent",
      "Publication did not complete after confirmation",
    );
    check(
      byKey.publish.delivered_at && byKey.publish.delivered_at > byKey.confirm.completed_at,
      "Publisher was released before the operator confirmed",
    );
  } else throw new Error(`No outcome verifier for ${scenario}`);
}

/**
 * The append-only audit is the authority on who released the run: an agent must
 * never appear on the human step, and the publisher must be delivered only after
 * the operator's confirmation is durable.
 */
export function verifyOperatorConfirmation(run: any, report: any) {
  const byKey = Object.fromEntries(run.steps.map((s: any) => [s.key, s]));
  const confirm = byKey.confirm;
  check(
    report?.step?.id === confirm.id,
    `Operator confirmed ${report?.step?.id}, but the run's human step is ${confirm.id}`,
  );
  const operatorEvents = confirm.events.filter(
    (e: any) => e.actor === "operator" && e.state === "completed",
  );
  check(
    operatorEvents.length === 1 && operatorEvents[0].output === OPERATOR_CONFIRMATION,
    "Expected exactly one operator completion in the step audit",
  );
  check(
    !confirm.events.some((e: any) => e.actor.startsWith("agent:")),
    "An agent acted on the human confirmation step",
  );
  // Ordering is proven from persisted timestamps, not from the watcher's clock.
  check(
    byKey.publish.events.every(
      (e: any) => e.created_at >= operatorEvents[0].created_at,
    ),
    "The publish step was acted on before the operator confirmed",
  );
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

/** Require actual isolated workers, not just a main-thread spawn attempt. */
export function verifyStepWorkers(calls: any[], run: any) {
  const requiredProcessTools = [
    "processes_step_get",
    "processes_step_update",
    "processes_run_get",
    "processes_run_update",
    "processes_run_cancel",
  ];
  const workers = new Set<string>();
  for (const step of run.steps) {
    const done = calls.find(c => c.name === "processes_step_update" && c.ok && c.completed && c.args?.step_id === step.id && c.args?.state === "completed");
    check(done?.thread_id && done.thread_id !== "main", `${step.key}: outcome was not recorded by a worker`);
    const worker = `${done.agent}:${done.thread_id}`;
    check(!workers.has(worker), `${step.key}: worker reused across steps`);
    workers.add(worker);
    // Processes provisions workers through the platform thread API, not a
    // model-mediated spawn call. Keep accepting the older trace shape for
    // historical artifacts, but the live path is identified by its durable
    // non-main worker target and authoritative read/update sequence.
    const spawnIndex = calls.findIndex(c => c.name === "spawn" && c.ok && c.completed && c.thread_id === "main" && c.agent === done.agent && c.args?.id === done.thread_id);
    const spawn = spawnIndex >= 0 ? calls[spawnIndex] : undefined;
    let granted: string[] = [];
    if (spawn) {
      granted = typeof spawn.args?.tools === "string"
        ? spawn.args.tools.split(",").map((name: string) => name.trim()).filter(Boolean)
        : Array.isArray(spawn.args?.tools) ? spawn.args.tools : [];
      check(requiredProcessTools.every(tool => granted.includes(tool)), `${step.key}: worker spawn omitted required Processes tools`);
    }
    // In the current path the platform inherits the executor's complete
    // spawnable MCP profile, so the worker's domain tools are not represented
    // in a model-facing spawn call. Keep the explicit-grant audit only for
    // legacy traces that still contain one.
    if (spawn) {
      const coordinationTools = new Set(["done", "send", "pace", "search_tools", ...requiredProcessTools]);
      const workerDomainCalls = calls.filter(c => c.thread_id === done.thread_id && c.ok && c.completed && !coordinationTools.has(c.name));
      for (const domainCall of workerDomainCalls) {
        check(granted.includes(domainCall.name), `${step.key}: worker used ${domainCall.name} without receiving it in spawn.tools`);
      }
    } else {
      check(!calls.some(c => c.name === "processes_step_assign" && c.ok && c.completed && c.args?.step_id === step.id), `${step.key}: model must not assign an app-provisioned worker`);
    }
    const assignIndex = calls.findIndex(c => c.name === "processes_step_assign" && c.ok && c.completed && c.agent === done.agent && c.thread_id === "main" && c.args?.step_id === step.id && c.args?.thread_id === done.thread_id);
    const readIndex = calls.findIndex(c => c.name === "processes_step_get" && c.ok && c.completed && c.agent === done.agent && c.thread_id === done.thread_id && c.args?.step_id === step.id);
    if (spawn) {
      check(assignIndex > spawnIndex && readIndex > assignIndex, `${step.key}: ordered spawn, assignment, and worker read failed`);
    }
    check(readIndex < calls.indexOf(done), `${step.key}: missing authoritative worker read`);
  }
}

/** Check the optimization itself, using both persisted ownership and real calls. */
export function verifySequentialWorker(calls: any[], run: any, workers: any[], requireDone = true) {
  check(workers.length === 1 && workers[0].agent_id === run.assignment.owner_agent_id, "Expected one persisted run worker");
  const thread = workers[0].thread_id;
  check(thread && thread !== "main", "Run worker must be isolated");
  const spawns = calls.filter(c => c.name === "spawn" && c.ok && c.completed);
  // App-owned provisioning is the normal path: no model spawn is required.
  // Accept one correctly targeted legacy spawn so historical artifacts remain
  // verifiable, but reject extra or unrelated worker creation.
  const targetedSpawns = spawns.filter(c => c.thread_id === "main" && c.args?.id === thread);
  check(
    (spawns.length === 0 && targetedSpawns.length === 0) ||
      (spawns.length === 1 && targetedSpawns.length === 1),
    "Expected app-provisioned worker (or exactly one legacy main-thread spawn)",
  );
  // Single-agent fixtures do not include a topology alias on every tool call;
  // the durable thread ID is the authority in that case. Topology runs do
  // include `agent`, so retain that additional attribution check when present.
  const workerAgent = calls.find(c => c.thread_id === thread && c.agent)?.agent;
  check(calls.some(c => c.thread_id === thread), "No worker execution trace");
  const isWorkerCall = (c: any) => c.thread_id === thread && (!workerAgent || c.agent === workerAgent);
  let previous = targetedSpawns.length ? calls.indexOf(targetedSpawns[0]) : -1;
  let previousEvent = -1;
  for (const step of run.steps) {
    check(step.updated_by === `agent:${workers[0].agent_id}:${thread}`, `${step.key}: persisted executor changed`);
    const ready = step.events.find((e: any) => e.state === "ready");
    const completions = step.events.filter((e: any) => e.state === "completed");
    check(ready && completions.length === 1 && ready.id > previousEvent && completions[0].id > ready.id, `${step.key}: dependency audit ordering failed`);
    previousEvent = completions[0].id;
    const claimIndex = calls.findIndex(c => c.name === "processes_step_claim" && c.ok && c.completed && isWorkerCall(c) && c.args?.step_id === step.id);
    const doneIndex = calls.findIndex(c => c.name === "processes_step_update" && c.ok && c.completed && isWorkerCall(c) && c.args?.step_id === step.id && c.args?.state === "completed");
    check(claimIndex > previous && doneIndex > claimIndex, `${step.key}: missing ordered claim/completion in the run worker`);
    previous = doneIndex;
  }
  const done = calls.filter(c => c.name === "done" && isWorkerCall(c) && c.ok && c.completed);
  if (requireDone) check(done.length === 1 && calls.indexOf(done[0]) > previous, "Worker must finish once after the final step");
  else check(done.length <= 1 && (!done.length || calls.indexOf(done[0]) > previous), "Worker done was out of order");
}
