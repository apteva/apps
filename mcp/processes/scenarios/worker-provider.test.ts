import { expect, test } from "bun:test";
import { DEFAULT_PROVIDER as D, PINNED_PROVIDER as P, verifyWorkerProvider } from "./worker-provider";

function fixture() {
  const run = (key: string, assignment: any, draft: string, check: string) => ({
    id: key, request_key: `a:manual:${key}`, state: "completed", assignment,
    steps: [
      { key: "draft", output: "DIGEST-DRAFT", target_thread_id: `${key}-draft`, want: draft },
      { key: "check", output: "DIGEST-OK", target_thread_id: `${key}-check`, want: check },
    ],
  });
  const runs: any[] = [
    run("pin-whole", { provider: P }, P, P),
    run("stay-default", {}, D, D),
    run("pin-step", { step_providers: { check: P } }, D, P),
  ];
  const threads: Record<string, string[]> = { "": [D] };
  const rows: any[] = [], envelopes: any[] = [];
  for (const r of runs) for (const s of r.steps) {
    threads[s.target_thread_id] = [s.want];
    envelopes.push({ spawn_json: JSON.stringify({ thread_id: s.target_thread_id, ...(s.want === P ? { provider: P } : {}) }) });
    if (s.want === P) rows.push({ run_id: r.id, thread_id: s.target_thread_id, pin: P, active: P, missing: 0 });
  }
  const calls: any[] = [{ completed: true, ok: false, name: "processes_assignment_create", args: { name: "Refused" }, result: "worker_continuity per_executor keeps one worker" }];
  return { runs, rows, envelopes, threads, calls };
}
const verify = (f: ReturnType<typeof fixture>) => verifyWorkerProvider(f.runs, f.rows, f.envelopes, f.threads, f.calls);

test("accepts each assignment on its own provider", () => verify(fixture()));
test("rejects a pinned worker that ran on the default", () => {
  const f = fixture();
  f.threads["pin-whole-draft"] = [D];
  expect(() => verify(f)).toThrow("expected opencode-go");
});
test("rejects a pinned worker that fell back partway", () => {
  const f = fixture();
  f.threads["pin-step-check"] = [P, D];
  expect(() => verify(f)).toThrow("pin-step/check");
});
test("rejects the default assignment reaching the pinned provider", () => {
  const f = fixture();
  f.threads["stay-default-check"] = [P];
  expect(() => verify(f)).toThrow("expected openai-codex");
});
test("rejects the coordinator leaving the default", () => {
  const f = fixture();
  f.threads[""] = [D, P];
  expect(() => verify(f)).toThrow("(main)");
});
test("rejects a shared worker across differing providers", () => {
  const f = fixture();
  f.runs[2].steps[1].target_thread_id = "pin-step-draft";
  expect(() => verify(f)).toThrow();
});
test("rejects a run that froze the wrong settings", () => {
  const f = fixture();
  f.runs[1].assignment = { provider: P };
  expect(() => verify(f)).toThrow("stay-default");
});
test("rejects a missing provider status", () => {
  const f = fixture();
  f.rows[0].missing = 1;
  expect(() => verify(f)).toThrow("reported status");
});
test("rejects an unattempted refusal", () => {
  const f = fixture();
  f.calls = [];
  expect(() => verify(f)).toThrow("per_executor");
});
