import { expect, test } from "bun:test";
import {
  verifyHistory,
  verifyMultiAgentTrajectory,
  verifySequentialWorker,
  verifyOperatorConfirmation,
  OPERATOR_CONFIRMATION,
} from "./verify-outcomes";
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
      },
      { key: "publish", state: "pending", delivered_at: "", task_id: "" },
    ],
  };
}
const verify = (run: unknown) =>
  verifyHistory("processes-human-review-step", { direct_runs: [run] });
test("accepts a persisted waiting human step", () =>
  expect(() => verify(waitingRun())).not.toThrow());
test("rejects publication dispatched before human review", () => {
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

// Deliberately non-sequential instance IDs: roles must resolve to real agents.
function teamRun() {
  const outputs = {
    research: "RESEARCH|page=photography|tip=window-light",
    audience: "AUDIENCE|page=photography|level=beginner",
    draft: "DRAFT|photography|beginner|window-light",
    review: "APPROVED|DRAFT|photography|beginner|window-light",
    publish: "RECEIPT|photography|beginner|window-light",
  };
  const specs = [
    { key: "research", role: "researcher", agent: 29, deps: [], ready: 1, done: 4 },
    { key: "audience", role: "writer", agent: 11, deps: [], ready: 2, done: 3 },
    { key: "draft", role: "writer", agent: 11, deps: ["research", "audience"], ready: 5, done: 6 },
    { key: "review", role: "reviewer", agent: 47, deps: ["draft"], ready: 7, done: 8 },
    { key: "publish", role: "publisher", agent: 29, deps: ["review"], ready: 9, done: 10 },
  ];
  return {
    state: "completed", workflow: true,
    assignment: {
      owner_agent_id: 29, parameters: { page: "photography" },
      roles: Object.fromEntries(specs.map(s => [s.role, { kind: "agent", agent_id: s.agent }])),
    },
    result: JSON.stringify(outputs),
    steps: specs.map(s => ({
      id: `step-${s.key}`, key: s.key, state: "completed",
      output: outputs[s.key as keyof typeof outputs],
      definition: { role: s.role, depends_on: s.deps },
      executor: { kind: "agent", agent_id: s.agent },
      updated_by: `agent:${s.agent}:main`,
      delivered_at: "2026-09-12T10:00:00Z", execution_id: `exec-${s.key}`,
      events: [{ id: s.ready, state: "ready" }, { id: s.done, state: "completed" }],
    })),
  };
}
const verifyTeam = (run: unknown) =>
  verifyHistory("processes-multi-agent-workflow", { direct_runs: [run] });
test("accepts a completed three-agent parallel join and review", () => {
  expect(() => verifyTeam(teamRun())).not.toThrow();
});
test("rejects roles collapsed onto the coordinator", () => {
  const run = teamRun();
  run.steps[2].executor.agent_id = 29;
  expect(() => verifyTeam(run)).toThrow("three distinct agent instances");
});
for (const [index, earlyID] of [[2, 3.5], [4, 7.5]]) {
  test(`rejects ${index === 2 ? "draft" : "publication"} released before its dependencies`, () => {
    const run = teamRun();
    run.steps[index].events[0].id = earlyID;
    expect(() => verifyTeam(run)).toThrow("dependency released too early");
  });
}
test("rejects a different agent reporting the result", () => {
  const run = teamRun();
  run.steps[2].updated_by = "agent:29:main";
  expect(() => verifyTeam(run)).toThrow("wrong reporting agent");
});
test("rejects removed native approval metadata", () => {
  const run = teamRun();
  (run.steps[3] as any).decision = "approved";
  expect(() => verifyTeam(run)).toThrow("removed native approval metadata");
});
test("rejects sequential release of the parallel inputs", () => {
  const run = teamRun();
  run.steps[0].events[1].id = 1.5;
  expect(() => verifyTeam(run)).toThrow("Parallel inputs");
});
function teamCalls(run: ReturnType<typeof teamRun>) {
  const agents = ["primary", "writer", "writer", "reviewer", "primary"];
  return run.steps.flatMap((s, i) => ["processes_step_get", "processes_step_update"].map(name => ({
    agent: agents[i], node: "main", name, completed: true, ok: true,
    args: { step_id: s.id, state: "completed" },
  })));
}
test("requires a successful read and completion by each assigned real agent", () => {
  const run = teamRun();
  expect(() => verifyMultiAgentTrajectory(teamCalls(run), run)).not.toThrow();
  const noRead = teamCalls(run).filter(c => !(c.agent === "reviewer" && c.name === "processes_step_get"));
  expect(() => verifyMultiAgentTrajectory(noRead, run)).toThrow("did not read its step");
  const wrongActor = teamCalls(run).map(c => c.agent === "reviewer" ? { ...c, agent: "primary" } : c);
  expect(() => verifyMultiAgentTrajectory(wrongActor, run)).toThrow("no successful real-agent completion trace");
  const failed = teamCalls(run).map(c => c.agent === "reviewer" ? { ...c, ok: false } : c);
  expect(() => verifyMultiAgentTrajectory(failed, run)).toThrow("no successful real-agent completion trace");
});

test("worker verification rejects main completion, wrong agent, and missing worker read", async () => {
  const { verifyStepWorkers } = await import("./verify-outcomes");
  const run = { steps: [{ id: "s1", key: "publish" }] };
  const calls = [
    { name: "spawn", agent: "primary", thread_id: "main", ok: true, completed: true, args: { id: "worker", paused: "true", tools: "processes_step_get,processes_step_update,processes_run_get,processes_run_update,processes_run_cancel" } },
    { name: "processes_step_assign", agent: "primary", thread_id: "main", ok: true, completed: true, args: { step_id: "s1", thread_id: "worker" } },
    { name: "processes_step_get", agent: "primary", thread_id: "worker", ok: true, completed: true, args: { step_id: "s1" } },
    { name: "worker_fixture_lookup", agent: "primary", thread_id: "worker", ok: true, completed: true, args: {} },
    { name: "processes_step_update", agent: "primary", thread_id: "worker", ok: true, completed: true, args: { step_id: "s1", state: "completed" } },
  ];
  calls[0].args.tools += ",worker_fixture_lookup";
  expect(() => verifyStepWorkers(calls, run)).not.toThrow();
  expect(() => verifyStepWorkers([calls[0], calls[1], calls[3], calls[4]], run)).toThrow("ordered spawn, assignment");
  expect(() => verifyStepWorkers([calls[0], calls[1], calls[2], calls[3], { ...calls[4], thread_id: "main" }], run)).toThrow("not recorded by a worker");
  expect(() => verifyStepWorkers([{ ...calls[0], agent: "other" }, calls[1], calls[2], calls[3], calls[4]], run)).toThrow("model must not assign");
  expect(() => verifyStepWorkers([{ ...calls[0], args: { ...calls[0].args, tools: "processes_step_get,processes_step_update,processes_run_get,processes_run_update,processes_run_cancel" } }, calls[1], calls[2], calls[3], calls[4]], run)).toThrow("used worker_fixture_lookup without receiving it");
  const appOwnedCalls = [
    { name: "processes_step_get", agent: "primary", thread_id: "worker", ok: true, completed: true, args: { step_id: "s1" } },
    { name: "worker_fixture_lookup", agent: "primary", thread_id: "worker", ok: true, completed: true, args: {} },
    { name: "processes_step_update", agent: "primary", thread_id: "worker", ok: true, completed: true, args: { step_id: "s1", state: "completed" } },
  ];
  expect(() => verifyStepWorkers(appOwnedCalls, run)).not.toThrow();
});

test("sequential verification rejects extra workers, premature done, and missing claims", () => {
  const worker = { agent_id: 29, thread_id: "weather-worker" };
  const run = {
    assignment: { owner_agent_id: 29 },
    steps: ["weather", "conversation", "push"].map((key, i) => ({
      id: key, key, updated_by: "agent:29:weather-worker",
      events: [{ id: 2 * i + 1, state: "ready" }, { id: 2 * i + 2, state: "completed" }],
    })),
  };
  const call = (name: string, args = {}, thread_id = worker.thread_id) => ({ name, args, thread_id, agent: "primary", completed: true, ok: true });
  const calls = [
    ...run.steps.flatMap(s => [call("processes_step_claim", { step_id: s.id }), call("processes_step_update", { step_id: s.id, state: "completed" })]),
    call("done"),
  ];
  expect(() => verifySequentialWorker(calls, run, [worker])).not.toThrow();
  const legacyCalls = [
    call("spawn", { id: worker.thread_id }, "main"),
    ...run.steps.flatMap(s => [call("processes_step_claim", { step_id: s.id }), call("processes_step_update", { step_id: s.id, state: "completed" })]),
    call("done"),
  ];
  expect(() => verifySequentialWorker(legacyCalls, run, [worker])).not.toThrow();
  expect(() => verifySequentialWorker([...legacyCalls, call("spawn", { id: "extra" }, "main")], run, [worker])).toThrow("app-provisioned worker");
  expect(() => verifySequentialWorker(calls.filter(c => c.name !== "processes_step_claim"), run, [worker])).toThrow("missing ordered claim");
  expect(() => verifySequentialWorker([calls[0], calls.at(-1)!, ...calls.slice(1, -1)], run, [worker])).toThrow("finish once after the final step");
  expect(() => verifySequentialWorker(calls, run, [worker, worker])).toThrow("one persisted run worker");
  run.steps[1].events[0].id = 1;
  expect(() => verifySequentialWorker(calls, run, [worker])).toThrow("dependency audit ordering failed");
});

function confirmedRun() {
  return {
    workflow: true,
    state: "completed",
    steps: [
      {
        key: "draft",
        id: "step-draft",
        state: "completed",
        output: "Golden hour flatters every portrait.",
        executor: { kind: "agent", agent_id: 7 },
        updated_by: "agent:7:main",
        completed_at: "2026-09-18T10:00:00Z",
        delivered_at: "2026-09-18T09:59:00Z",
        events: [
          { actor: "agent:7:main", state: "completed", created_at: "2026-09-18T10:00:00Z" },
        ],
      },
      {
        key: "confirm",
        id: "step-confirm",
        state: "completed",
        output: OPERATOR_CONFIRMATION,
        executor: { kind: "human" },
        updated_by: "operator",
        delivered_at: "",
        task_id: "",
        completed_at: "2026-09-18T10:05:00Z",
        events: [
          {
            actor: "operator",
            state: "completed",
            output: OPERATOR_CONFIRMATION,
            created_at: "2026-09-18T10:05:00Z",
          },
        ],
      },
      {
        key: "publish",
        id: "step-publish",
        state: "completed",
        output: "Published: Golden hour flatters every portrait.",
        executor: { kind: "agent", agent_id: 7 },
        updated_by: "agent:7:main",
        delivered_at: "2026-09-18T10:05:05Z",
        completed_at: "2026-09-18T10:06:00Z",
        events: [
          { actor: "agent:7:main", state: "completed", created_at: "2026-09-18T10:06:00Z" },
        ],
      },
    ],
  };
}
const confirmReport = (run: any) => ({ step: { id: run.steps[1].id } });
const verifyConfirm = (run: unknown) =>
  verifyHistory("processes-operator-confirmation", { direct_runs: [run] });

test("accepts an operator-confirmed run that resumed to publication", () => {
  const run = confirmedRun();
  expect(() => verifyConfirm(run)).not.toThrow();
  expect(() => verifyOperatorConfirmation(run, confirmReport(run))).not.toThrow();
});
test("rejects an agent completing the human confirmation step", () => {
  const run = confirmedRun();
  run.steps[1].updated_by = "agent:7:main";
  expect(() => verifyConfirm(run)).toThrow("not the operator");
});
test("rejects an agent appearing in the human step's audit", () => {
  const run = confirmedRun();
  run.steps[1].events.push({
    actor: "agent:7:worker",
    state: "completed",
    output: OPERATOR_CONFIRMATION,
    created_at: "2026-09-18T10:05:01Z",
  });
  expect(() => verifyOperatorConfirmation(run, confirmReport(run))).toThrow(
    "An agent acted on the human confirmation step",
  );
});
test("rejects publication released before the operator confirmed", () => {
  const run = confirmedRun();
  run.steps[2].delivered_at = "2026-09-18T10:04:00Z";
  expect(() => verifyConfirm(run)).toThrow("released before the operator confirmed");
});
test("rejects a publish step acted on before the confirmation was durable", () => {
  const run = confirmedRun();
  run.steps[2].events[0].created_at = "2026-09-18T10:04:30Z";
  expect(() => verifyOperatorConfirmation(run, confirmReport(run))).toThrow(
    "acted on before the operator confirmed",
  );
});
test("rejects a confirmation recorded against a different step", () => {
  const run = confirmedRun();
  expect(() =>
    verifyOperatorConfirmation(run, { step: { id: "step-other" } }),
  ).toThrow("human step is step-confirm");
});
test("rejects a dispatched human step", () => {
  const run = confirmedRun();
  run.steps[1].delivered_at = "2026-09-18T10:01:00Z";
  expect(() => verifyConfirm(run)).toThrow("never be dispatched");
});
