import { test, expect } from "bun:test";
import { verifyRunRatings } from "./run-ratings";
function fixture() {
  const quality = { tags: ["Wrong output", "Incomplete", "Great"], criteria: [{ key: "accuracy", type: "score" }, { key: "on_time", type: "pass_fail" }], request_rating: true, review: { failed_runs: true } };
  const version = { version: 1, body_json: JSON.stringify({ quality }) };
  const run = (key: string, state: string, check: any) => ({ id: key, request_key: `assignment-a:manual:${key}`, state, version: 1, created_at: "2026-10-29T10:00:00Z", assignment: { owner_agent_id: 7 }, steps: [{ id: `${key}-draft`, key: "draft", output: "DIGEST-DRAFT" }, { key: "check", ...check }] });
  const runs = [run("rate-ok", "completed", { output: "DIGEST-OK" }), run("rate-fail", "failed", { output: "DIGEST-BROKEN", error: "FIXTURE-FAIL" })];
  const rating = (id: string, run: string, score: number, tag: string, accuracy: number, on_time: boolean) => ({ id, run_id: run, step_id: "", source: "agent", rater: "agent:7", version: 1, score, tags_json: JSON.stringify([tag]), criteria_json: JSON.stringify({ accuracy, on_time }), superseded: 0, created_at: "2026-10-29T10:05:00Z" });
  const ratings = [rating("good", "rate-ok", 4, "Great", 4, true), rating("bad", "rate-fail", 1, "Wrong output", 1, false)];
  const review = (reason: string, rating_id = "") => ({ run_id: "rate-fail", reason, rating_id, state: "resolved", resolution: "Fixture failure confirmed", assignee: "agent:7", revision: 3 });
  const reviews = [review("failed"), review("low_score", "bad")];
  const call = (name: string, args: any, ok = true, result = "{}") => ({ name, args, ok, completed: true, result });
  const calls = [
    call("processes_start", { idempotency_key: "rate-ok" }), call("processes_start", { idempotency_key: "rate-fail" }),
    call("processes_run_get", { run_id: "rate-ok" }), call("processes_run_rate", { run_id: "rate-ok", score: 4 }),
    call("processes_run_rate", { run_id: "rate-ok", step_id: "rate-ok-draft", score: 5 }, false, "an agent cannot rate a step it executed; save a run summary instead"),
    call("processes_run_get", { run_id: "rate-fail" }), call("processes_run_rate", { run_id: "rate-fail", score: 1 }),
    call("processes_review_queue", {}),
    call("processes_review_update", { state: "claimed" }), call("processes_review_update", { state: "resolved" }),
    call("processes_review_update", { state: "claimed" }), call("processes_review_update", { state: "resolved" }),
    call("processes_quality_get", {}),
  ];
  return { calls, version, runs, ratings, reviews };
}
const verify = (f: ReturnType<typeof fixture>) => verifyRunRatings(f.calls, f.version, f.runs, f.ratings, f.reviews);
test("accepts exact persisted ratings, refused self-rating and a resolved queue", () => expect(() => verify(fixture())).not.toThrow());
for (const [name, change] of Object.entries({
  "missing quality policy": (f: any) => (f.version.body_json = "{}"),
  "wrong score": (f: any) => (f.ratings[0].score = 5),
  "self step rating saved": (f: any) => f.ratings.push({ ...f.ratings[0], id: "self", step_id: "rate-ok-draft" }),
  "review for good run": (f: any) => f.reviews.push({ ...f.reviews[0], run_id: "rate-ok" }),
  "unresolved review": (f: any) => (f.reviews[0].state = "claimed"),
  "rated without reading": (f: any) => f.calls.splice(2, 1),
  "no refusal attempt": (f: any) => f.calls.splice(4, 1),
  "quality read too early": (f: any) => f.calls.splice(7, 0, f.calls.pop()),
  "extra run": (f: any) => f.runs.push({ ...f.runs[0], id: "x", request_key: "assignment-a:manual:x" }),
})) test(`rejects ${name}`, () => { const f = fixture(); change(f); expect(() => verify(f)).toThrow(); });
