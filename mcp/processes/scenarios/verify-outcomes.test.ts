import { expect, test } from "bun:test";
import { verifyHistory, verifyMultiAgentTrajectory } from "./verify-outcomes";
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
      definition: { role: s.role, kind: s.key === "review" ? "approval" : "work", depends_on: s.deps },
      decision: s.key === "review" ? "approved" : "",
      executor: { kind: "agent", agent_id: s.agent },
      updated_by: `agent:${s.agent}:main`,
      delivered_at: "2026-09-12T10:00:00Z", execution_id: `exec-${s.key}`,
      events: [{ id: s.ready, state: "ready" }, { id: s.done, state: "completed" }],
    })),
  };
}
const verifyTeam = (run: unknown) =>
  verifyHistory("processes-multi-agent-workflow", { direct_runs: [run] });
test("accepts a completed three-agent parallel join and approval", () => {
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
test("requires explicit approval", () => {
  const run = teamRun();
  run.steps[3].decision = "";
  expect(() => verifyTeam(run)).toThrow("Review gate was bypassed");
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
