import { check } from "./verify-outcomes";
import type { ConfirmationReport } from "./operator-confirm";
export const AI_PARALLEL = "processes-ai-parallel-steps";
export const PARALLEL_APPROVAL =
  "Operator approved: exact parallel artifacts and session checkpoints.";
export function verifyAIParallel(
  calls: any[],
  runs: any[],
  workers: any[],
  fixture: { contexts: any[]; operations: any[] },
  confirmation: ConfirmationReport,
) {
  check(
    runs.length === 1 && runs[0].state === "completed" && runs[0].workflow,
    "Expected one completed parallel run",
  );
  const r = runs[0],
    keys = [
      "prepare",
      "alpha",
      "beta",
      "session_a",
      "session_b",
      "validate",
      "approve",
      "publish",
    ];
  check(
    r.assignment.worker_continuity === "per_executor" &&
      r.assignment.parallel_execution === "auto" &&
      r.assignment.max_parallel_steps === 2,
    "Missing frozen parallel assignment",
  );
  check(
    r.steps.map((s: any) => s.key).join(",") === keys.join(","),
    "Wrong parallel graph",
  );
  check(
    workers.length === 1 &&
      workers[0].run_id === r.id &&
      workers[0].agent_id === r.assignment.owner_agent_id,
    "Expected one durable owner",
  );
  const owner = workers[0].thread_id,
    agent = workers[0].agent_id,
    b = Object.fromEntries(r.steps.map((s: any) => [s.key, s]));
  const expectedDeps: Record<string, string[]> = {
    prepare: [],
    alpha: ["prepare"],
    beta: ["prepare"],
    session_a: ["prepare"],
    session_b: ["session_a"],
    validate: ["alpha", "beta", "session_b"],
    approve: ["validate"],
    publish: ["approve"],
  };
  for (const s of r.steps) {
    check(
      JSON.stringify([...s.definition.depends_on].sort()) ===
        JSON.stringify(expectedDeps[s.key].sort()),
      `${s.key}: wrong dependencies`,
    );
    check(
      s.state === "completed" && s.output,
      `${s.key}: missing completed receipt`,
    );
    if (s.key !== "approve")
      check(
        s.updated_by === `agent:${agent}:${owner}` &&
          s.target_thread_id === owner &&
          s.execution_id &&
          s.delivered_at,
        `${s.key}: lost owner/delivery identity`,
      );
  }
  const agentSteps = r.steps.filter((s: any) => s.executor.kind === "agent");
  check(
    new Set(agentSteps.map((s: any) => s.execution_id)).size ===
      agentSteps.length,
    "Mixed step execution identities",
  );
  check(
    b.approve.executor.kind === "human" &&
      b.approve.updated_by === "operator" &&
      b.approve.output === PARALLEL_APPROVAL,
    "Approval bypassed",
  );
  check(
    confirmation?.step.id === b.approve.id &&
      confirmation.step.run_id === r.id &&
      confirmation.output === PARALLEL_APPROVAL,
    "Missing real HTTP approval",
  );
  check(
    fixture.contexts.length === 1 &&
      fixture.contexts[0].thread_id === owner &&
      fixture.contexts[0].agent_id === agent &&
      fixture.operations.length === 7,
    "Repeated setup or missing fixture work",
  );
  const ops = fixture.operations,
    context = fixture.contexts[0],
    byOp = (n: string, a = "") =>
      ops.find((o) => o.name === n && o.artifact_id === a);
  const mapping: Record<string, any> = {
    prepare: byOp("prepare"),
    alpha: byOp("work", "alpha.png"),
    beta: byOp("work", "beta.png"),
    session_a: byOp("session", "a"),
    session_b: byOp("session", "b"),
    validate: byOp("validate"),
    publish: byOp("publish"),
  };
  for (const [k, o] of Object.entries(mapping)) {
    check(
      o &&
        o.completed_at >= o.started_at &&
        o.completed_at > 0 &&
        o.context_id === context.id &&
        o.tool_call_id,
      `${k}: incomplete real operation`,
    );
    const saved = JSON.parse(b[k].output),
      actual = JSON.parse(o.receipt_json);
    check(
      Object.keys(saved).length === Object.keys(actual).length &&
        Object.entries(actual).every(
          ([key, v]) => JSON.stringify(saved[key]) === JSON.stringify(v),
        ),
      `${k}: receipt identity lost`,
    );
    check(
      saved.context_id === context.id && saved.source_id === context.source_id,
      `${k}: wrong context/source`,
    );
    if (!["alpha", "beta"].includes(k))
      check(o.thread_id === owner, `${k}: session escaped owner`);
  }
  const a = mapping.alpha,
    z = mapping.beta;
  check(
    a.thread_id !== z.thread_id &&
      (a.thread_id !== owner || z.thread_id !== owner),
    "Independent work did not use separate child threads",
  );
  check(
    Math.max(a.started_at, z.started_at) <
      Math.min(a.completed_at, z.completed_at),
    "Independent worker execution did not overlap",
  );
  check(
    JSON.parse(a.receipt_json).digest !== JSON.parse(z.receipt_json).digest,
    "Mixed artifact digests",
  );
  check(
    mapping.session_b.started_at >= mapping.session_a.completed_at,
    "Session checkpoint ordering violated",
  );
  const successful = (c: any) => c.ok && c.completed;
  const children = new Set(
    [a.thread_id, z.thread_id].filter((id) => id !== owner),
  );
  const spawns = calls.filter((c) => successful(c) && c.name === "spawn");
  check(
    spawns.length === children.size &&
      spawns.every((c) => c.thread_id === owner) &&
      [...children].every((id) => spawns.some((c) => c.args?.id === id)),
    "Missing real owner-child spawn trace or excess workers",
  );
  const domain = calls.filter((c) => c.name.startsWith("test-parallel_"));
  check(
    domain.length === 7 && domain.every(successful),
    "Repeated/failed fixture calls",
  );
  for (const c of calls)
    if (c.name.startsWith("processes_step_") && children.has(c.thread_id))
      check(false, "Child took over durable step ownership");
  let active = new Set<string>();
  for (const c of calls.filter((c) => successful(c) && c.thread_id === owner)) {
    if (c.name === "processes_step_claim") {
      active.add(c.args.step_id);
      check(active.size <= 2, "Exceeded claimed concurrency");
      if (!c.result_truncated) {
        const ack = JSON.parse(c.result);
        check(
          ack.parallel_execution === "auto" &&
            ack.max_parallel_steps === 2 &&
            Array.isArray(ack.ready_steps) &&
            Array.isArray(ack.active_steps),
          "Missing compact work hints",
        );
      }
    }
    if (c.name === "processes_step_update") {
      // CLI telemetry previews stop at 1000 bytes, independently of the full
      // response delivered to the model. State/output are verified against the
      // durable step events above; full response shape is covered by Go tests.
      if (c.args.state === "completed") active.delete(c.args.step_id);
      check(c.result_original_bytes < 5000, "Oversized acknowledgement");
      const expectedDone =
        c.args.step_id === b.publish.id && c.args.state === "completed";
      if (c.result_truncated) {
        check(
          c.result.match(/"done":\s*(true|false)/)?.[1] ===
            String(expectedDone),
          "Premature done gate",
        );
        continue;
      }
      const ack = JSON.parse(c.result);
      check(
        ack.done === expectedDone && ack.state === c.args.state,
        "Premature done gate",
      );
      check(
        !("instructions" in ack) &&
          !("dependencies" in ack) &&
          !("output" in ack) &&
          c.result_original_bytes < 5000,
        "Oversized acknowledgement",
      );
      check(
        ack.reread?.args?.step_id === c.args.step_id,
        "Wrong recovery reference",
      );
    }
  }
  for (const s of r.steps) {
    const complete = s.events.filter((e: any) => e.state === "completed");
    check(complete.length === 1, `${s.key}: duplicated completion`);
    for (const dep of s.definition.depends_on) {
      const ready = s.events.find((e: any) =>
        ["ready", "waiting"].includes(e.state),
      );
      check(
        ready &&
          ready.id > b[dep].events.find((e: any) => e.state === "completed").id,
        `${s.key}: dependency released early`,
      );
    }
  }
  for (const key of ["alpha", "beta"]) {
    const o = mapping[key],
      claim = calls.find(
        (c) =>
          successful(c) &&
          c.name === "processes_step_claim" &&
          c.args.step_id === b[key].id,
      );
    const domainCall = domain.find(
      (c) =>
        c.name === "test-parallel_work" && c.args.artifact_id === o.artifact_id,
    );
    const done = calls.find(
      (c) =>
        successful(c) &&
        c.name === "processes_step_update" &&
        c.args.step_id === b[key].id &&
        c.args.state === "completed",
    );
    check(
      claim &&
        domainCall &&
        done &&
        calls.indexOf(claim) < calls.indexOf(domainCall) &&
        calls.indexOf(domainCall) < calls.indexOf(done),
      `${key}: missing claim/work/receipt ordering`,
    );
    check(
      o.thread_id === owner ||
        calls.some(
          (c) =>
            successful(c) &&
            c.name === "processes_step_update" &&
            c.args.step_id === b[key].id &&
            ["running", "waiting"].includes(c.args.state) &&
            String(c.args.output).includes(o.thread_id),
        ),
      `${key}: missing durable child checkpoint`,
    );
  }
  check(
    calls.filter(
      (c) => successful(c) && c.name === "done" && children.has(c.thread_id),
    ).length === children.size,
    "Children did not finish exactly once",
  );
  const finish = calls.filter(
    (c) => successful(c) && c.name === "done" && c.thread_id === owner,
  );
  check(
    finish.length === 1 &&
      children.size >= 1 &&
      calls
        .filter(
          (c) =>
            successful(c) && c.name === "done" && children.has(c.thread_id),
        )
        .every((c) => calls.indexOf(c) < calls.indexOf(finish[0])),
    "Owner finished before children settled",
  );
  check(
    calls.some(
      (c) =>
        successful(c) &&
        c.name === "processes_runs" &&
        calls.indexOf(c) > calls.indexOf(finish[0]),
    ),
    "Missing final history verification",
  );
}
