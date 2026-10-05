import { expect, test } from "bun:test";
import { PARALLEL_APPROVAL, verifyAIParallel } from "./ai-parallel-steps";
function fixture() {
  const owner = "run-owner",
    context_id = "context",
    source_id = "source";
  const keys = [
    "prepare",
    "alpha",
    "beta",
    "session_a",
    "session_b",
    "validate",
    "approve",
    "publish",
  ];
  const deps = [
    [],
    ["prepare"],
    ["prepare"],
    ["prepare"],
    ["session_a"],
    ["alpha", "beta", "session_b"],
    ["validate"],
    ["approve"],
  ];
  const events = [
    [1, 3],
    [4, 10],
    [5, 11],
    [6, 13],
    [14, 16],
    [17, 19],
    [20, 21],
    [22, 24],
  ];
  const spec: any[] = [
    ["prepare", "", owner, 1, 2],
    ["work", "alpha.png", "child-a", 10, 60],
    ["work", "beta.png", "child-b", 20, 70],
    ["session", "a", owner, 71, 72],
    ["session", "b", owner, 73, 74],
    ["validate", "", owner, 75, 76],
    ["publish", "", owner, 78, 79],
  ];
  const receipts = spec.map((s, i) => ({
    context_id,
    source_id,
    operation_id: i + 1,
    ...(s[0] === "work"
      ? { artifact_id: s[1], digest: `digest-${i}` }
      : s[0] === "session"
        ? { phase: s[1], checkpoint: "saved" }
        : s[0] === "validate"
          ? { validated: ["alpha.png", "beta.png", "session-a", "session-b"] }
          : s[0] === "publish"
            ? { receipt: "accepted", artifact_ids: ["alpha.png", "beta.png"] }
            : {}),
  }));
  const operations = spec.map((s, i) => ({
    context_id,
    name: s[0],
    artifact_id: s[1],
    thread_id: s[2],
    tool_call_id: `call-${i}`,
    started_at: s[3],
    completed_at: s[4],
    receipt_json: JSON.stringify(receipts[i]),
  }));
  const run: any = {
    id: "run",
    process_id: "process",
    workflow: true,
    state: "completed",
    assignment: {
      worker_continuity: "per_executor",
      parallel_execution: "auto",
      max_parallel_steps: 2,
      owner_agent_id: 7,
    },
    steps: keys.map((key, i) => ({
      id: key,
      key,
      state: "completed",
      output:
        i === 6 ? PARALLEL_APPROVAL : JSON.stringify(receipts[i === 7 ? 6 : i]),
      target_thread_id: i === 6 ? "" : owner,
      updated_by: i === 6 ? "operator" : `agent:7:${owner}`,
      executor: i === 6 ? { kind: "human" } : { kind: "agent", agent_id: 7 },
      definition: { depends_on: deps[i] },
      execution_id: `exec-${i}`,
      delivered_at: "today",
      events: [
        { id: events[i][0], state: i === 6 ? "waiting" : "ready" },
        { id: events[i][1], state: "completed" },
      ],
    })),
  };
  const calls: any[] = [];
  const call = (
    name: string,
    args: any = {},
    thread_id = owner,
    result: any = {},
  ) => {
    const text = JSON.stringify(result);
    calls.push({
      name,
      args,
      thread_id,
      result: text,
      result_original_bytes: text.length,
      result_truncated: false,
      ok: true,
      completed: true,
    });
  };
  const read = (key: string) =>
    call("processes_step_claim", { step_id: key }, owner, {
      step: { state: "running" },
      parallel_execution: "auto",
      max_parallel_steps: 2,
      ready_steps: [],
      active_steps: [],
    });
  const update = (key: string, state = "completed", output = "receipt") =>
    call("processes_step_update", { step_id: key, state, output }, owner, {
      step_id: key,
      state,
      done: key === "publish" && state === "completed",
      reread: { args: { step_id: key } },
    });
  read("prepare");
  call("test-parallel_prepare");
  update("prepare");
  read("alpha");
  read("beta");
  for (const child of ["child-a", "child-b"]) call("spawn", { id: child });
  update("alpha", "running", "child=child-a;operation=alpha.png");
  update("beta", "running", "child=child-b;operation=beta.png");
  call("test-parallel_work", { artifact_id: "alpha.png" }, "child-a");
  call("test-parallel_work", { artifact_id: "beta.png" }, "child-b");
  call("done", {}, "child-a");
  call("done", {}, "child-b");
  update("alpha");
  update("beta");
  read("session_a");
  call("test-parallel_session", { phase: "a" });
  update("session_a");
  read("session_b");
  call("test-parallel_session", { phase: "b" });
  update("session_b");
  read("validate");
  call("test-parallel_validate");
  update("validate");
  read("publish");
  call("test-parallel_publish");
  update("publish");
  call("done");
  call("processes_runs", {}, "main");
  return {
    calls,
    runs: [run],
    workers: [{ run_id: "run", agent_id: 7, thread_id: owner }],
    fixture: {
      contexts: [{ id: context_id, source_id, thread_id: owner, agent_id: 7 }],
      operations,
    },
    confirmation: {
      step: {
        id: "approve",
        run_id: "run",
        process_id: "process",
        project_id: "project",
      },
      port: 1234,
      confirmedAt: "today",
      output: PARALLEL_APPROVAL,
    },
  };
}
const verify = (f: ReturnType<typeof fixture>) =>
  verifyAIParallel(f.calls, f.runs, f.workers, f.fixture, f.confirmation);
test("verifies actual child overlap, exact owner receipts, serial session and human gate", () =>
  expect(() => verify(fixture())).not.toThrow());
const mutations: Record<string, (f: any) => void> = {
  "mixed execution identities": (f) =>
    (f.runs[0].steps[2].execution_id = f.runs[0].steps[1].execution_id),
  "sequential work disguised as parallel": (f) =>
    (f.fixture.operations[2].started_at = 61),
  "same child for both independent operations": (f) =>
    (f.fixture.operations[2].thread_id = "child-a"),
  "fabricated artifact receipt": (f) =>
    (f.runs[0].steps[2].output = f.runs[0].steps[1].output),
  "child owns durable step": (f) =>
    (f.runs[0].steps[1].updated_by = "agent:7:child-a"),
  "missing child checkpoint": (f) =>
    (f.calls = f.calls.filter(
      (c: any) =>
        !(
          c.name === "processes_step_update" &&
          c.args.step_id === "alpha" &&
          c.args.state === "running"
        ),
    )),
  "child updates Processes": (f) =>
    f.calls.push({ name: "processes_step_update", thread_id: "child-a" }),
  "owner session delegated": (f) =>
    (f.fixture.operations[3].thread_id = "child-a"),
  "session b before a": (f) => (f.fixture.operations[4].started_at = 71),
  "early validation join": (f) => (f.runs[0].steps[5].events[0].id = 9),
  "self approval": (f) => (f.runs[0].steps[6].updated_by = "agent:7:run-owner"),
  "no operator HTTP evidence": (f) => (f.confirmation.step.id = "wrong"),
  "premature done": (f) => {
    const c = f.calls.find((c: any) => c.name === "processes_step_update");
    c.result = JSON.stringify({ ...JSON.parse(c.result), done: true });
  },
  "repeated preparation": (f) => f.fixture.contexts.push(f.fixture.contexts[0]),
  "missing actual spawn": (f) =>
    (f.calls = f.calls.filter(
      (c: any) => !(c.name === "spawn" && c.args.id === "child-b"),
    )),
  "owner finishes before child": (f) => {
    const child = f.calls.findIndex(
      (c: any) => c.name === "done" && c.thread_id === "child-b",
    );
    const owner = f.calls.findIndex(
      (c: any) => c.name === "done" && c.thread_id === "run-owner",
    );
    const [c] = f.calls.splice(owner, 1);
    f.calls.splice(child, 0, c);
  },
  "oversized update": (f) =>
    (f.calls.find(
      (c: any) => c.name === "processes_step_update",
    ).result_original_bytes = 20000),
  "wrong recovery reference": (f) => {
    const c = f.calls.find((c: any) => c.name === "processes_step_update");
    c.result = JSON.stringify({
      ...JSON.parse(c.result),
      reread: { args: { step_id: "wrong" } },
    });
  },
};
for (const [name, mutate] of Object.entries(mutations))
  test(`rejects ${name}`, () => {
    const f = fixture();
    mutate(f);
    expect(() => verify(f)).toThrow();
  });
test("accepts useful owner-child overlap without prescribing two children", () => {
  const f = fixture();
  f.fixture.operations[2].thread_id = "run-owner";
  f.calls = f.calls.filter(
    (c) =>
      !(c.name === "spawn" && c.args.id === "child-b") &&
      !(c.name === "done" && c.thread_id === "child-b"),
  );
  const work = f.calls.find(
    (c) => c.name === "test-parallel_work" && c.args.artifact_id === "beta.png",
  );
  work.thread_id = "run-owner";
  expect(() => verify(f)).not.toThrow();
});
test("accepts shortened CLI previews while checking original bytes and done", () => {
  const f = fixture();
  const c = f.calls.find((c) => c.name === "processes_step_update");
  c.result = '{"active_steps":[],"done":false,"ready_steps":[{"id":"partial...';
  c.result_truncated = true;
  c.result_original_bytes = 1029;
  expect(() => verify(f)).not.toThrow();
});
test("rejects premature done even in a shortened telemetry preview", () => {
  const f = fixture();
  const c = f.calls.find((c) => c.name === "processes_step_update");
  c.result = '{"done":true,"ready_steps":[{"id":"partial...';
  c.result_truncated = true;
  c.result_original_bytes = 1029;
  expect(() => verify(f)).toThrow();
});
