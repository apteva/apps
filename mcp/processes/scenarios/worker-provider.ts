import { check } from "./verify-outcomes";
export const WORKER_PROVIDER = "processes-worker-provider";
export const DEFAULT_PROVIDER = "openai-codex";
export const PINNED_PROVIDER = "opencode-go";
// Named because the provider's default medium model can be unavailable upstream.
export const PINNED_MODEL = "kimi-k3";

// Expected provider per step, keyed by the run's idempotency key.
const EXPECTED: Record<string, Record<string, string>> = {
  "pin-whole": { draft: PINNED_PROVIDER, check: PINNED_PROVIDER },
  "stay-default": { draft: DEFAULT_PROVIDER, check: DEFAULT_PROVIDER },
  "pin-step": { draft: DEFAULT_PROVIDER, check: PINNED_PROVIDER },
};

// threadProviders comes from Core's own llm.start telemetry, one level below the
// app: what the model claims and what Processes requested can't satisfy it.
export function verifyWorkerProvider(runs: any[], rows: any[], envelopes: any[], threadProviders: Record<string, string[]>, calls: any[]) {
  check(runs.length === 3, `Expected exactly three runs, found ${runs.length}`);
  const workers = new Set<string>();
  for (const [key, steps] of Object.entries(EXPECTED)) {
    const run = runs.find((r) => String(r.request_key).endsWith(`manual:${key}`));
    check(run?.state === "completed", `Run ${key} did not complete`);
    check(run.steps.length === 2, `Run ${key} should have two steps`);
    for (const step of run.steps) {
      const want = steps[step.key];
      check(step.output === (step.key === "draft" ? "DIGEST-DRAFT" : "DIGEST-OK"), `${key}/${step.key} lacks its exact receipt`);
      const thread = step.target_thread_id;
      check(thread, `${key}/${step.key} has no worker`);
      workers.add(thread);
      const observed = threadProviders[thread] || [];
      check(observed.length === 1 && observed[0] === want, `${key}/${step.key}: worker ${thread} ran on [${observed}], expected ${want}`);
      const spawned = envelopes.filter((e) => e.spawn_json && JSON.parse(e.spawn_json).thread_id === thread);
      check(spawned.length > 0 && spawned.every((e) => (JSON.parse(e.spawn_json).provider || DEFAULT_PROVIDER) === want), `${key}/${step.key}: saved spawn request doesn't match ${want}`);
      const row = rows.find((r) => r.run_id === run.id && r.thread_id === thread);
      if (want === PINNED_PROVIDER) check(row?.pin === want && row.active === want && !row.missing, `${key}/${step.key}: reported status ${JSON.stringify(row)}`);
      else check(!row, `${key}/${step.key}: an unpinned worker reported a pin`);
    }
    const ids = run.steps.map((s: any) => s.target_thread_id);
    check(new Set(ids).size === 2, `Run ${key} should have one worker per step`);
    check(run.assignment.provider === (key === "pin-whole" ? PINNED_PROVIDER : undefined), `Run ${key} froze the wrong provider`);
    check(JSON.stringify(run.assignment.step_providers) === (key === "pin-step" ? `{"check":"${PINNED_PROVIDER}"}` : undefined), `Run ${key} froze the wrong step providers`);
  }
  check(workers.size === 6, `Expected six separate workers, found ${workers.size}`);
  // Everything that isn't a pinned worker, the coordinator included, stayed put.
  const pinned = new Set(rows.map((r) => r.thread_id));
  for (const [thread, observed] of Object.entries(threadProviders)) {
    if (pinned.has(thread)) continue;
    check(observed.length === 1 && observed[0] === DEFAULT_PROVIDER, `Thread ${thread || "(main)"} should stay on the default but ran on [${observed}]`);
  }
  const refused = calls.filter((c) => c.completed && c.name === "processes_assignment_create" && JSON.stringify(c.args).includes("Refused"));
  check(refused.length === 1 && !refused[0].ok && String(refused[0].result).includes("per_executor"), "The mixed per_executor assignment wasn't attempted once and refused");
}
