import { check } from "./verify-outcomes";
export const RUN_RATINGS = "processes-run-ratings";

// Ratings are judged from persisted state: the model's own success claims or
// tool-call telemetry alone cannot satisfy these checks.
export function verifyRunRatings(calls: any[], version: any, runs: any[], ratings: any[], reviews: any[]) {
  const quality = JSON.parse(version.body_json).quality;
  check(quality?.request_rating === true && quality?.review?.failed_runs === true, "Frozen procedure lacks the requested quality policy");
  check(quality.tags?.join(",") === "Wrong output,Incomplete,Great", "Quality tags were not saved exactly");
  check(quality.criteria?.map((c: any) => `${c.key}:${c.type}`).join(",") === "accuracy:score,on_time:pass_fail", "Quality rubric was not saved exactly");
  check(runs.length === 2, `Expected exactly two runs, found ${runs.length}`);
  // Stored request keys are scoped as <assignment>:manual:<idempotency_key>.
  const ok = runs.find((r) => String(r.request_key).endsWith(":rate-ok")), failed = runs.find((r) => String(r.request_key).endsWith(":rate-fail"));
  check(ok?.state === "completed" && failed?.state === "failed", "Expected one completed and one failed run");
  check(ok.steps.find((s: any) => s.key === "check")?.output === "DIGEST-OK", "Completed run lacks its exact receipt");
  check(failed.steps.find((s: any) => s.key === "check")?.error === "FIXTURE-FAIL", "Failed run lacks its exact fixture error");
  const active = ratings.filter((r) => !r.superseded);
  check(active.length === 2 && ratings.length === 2, `Expected exactly two whole-run ratings and no revisions, found ${ratings.length}`);
  check(active.every((r) => r.step_id === "" && r.source === "agent" && /^agent:\d+$/.test(r.rater)), "Ratings must be agent whole-run ratings with a stable rater identity");
  const good = active.find((r) => r.run_id === ok.id), bad = active.find((r) => r.run_id === failed.id);
  check(good && good.score === 4 && good.tags_json === '["Great"]' && JSON.parse(good.criteria_json).accuracy === 4 && JSON.parse(good.criteria_json).on_time === true, "Completed run rating does not match the requested score, tag and rubric");
  check(bad && bad.score === 1 && bad.tags_json === '["Wrong output"]' && JSON.parse(bad.criteria_json).accuracy === 1 && JSON.parse(bad.criteria_json).on_time === false, "Failed run rating does not match the requested score, tag and rubric");
  check(good.version === ok.version && bad.version === failed.version, "Ratings were not attributed to the frozen procedure version");
  check(good.created_at >= ok.created_at && bad.created_at >= failed.created_at, "A rating predates its run");
  const owner = `agent:${ok.assignment.owner_agent_id}`;
  check(good.rater === owner && bad.rater === owner, "Ratings were not saved under the coordinator identity");
  // The failed run must queue both the run-state rule and the low-score rule;
  // the well-rated completed run must not enter review.
  check(reviews.every((r) => r.run_id === failed.id), "A review item was opened for the well-rated completed run");
  check(reviews.map((r) => r.reason).sort().join(",") === "failed,low_score", `Expected failed and low_score review items, found ${reviews.map((r) => r.reason)}`);
  check(reviews.every((r) => r.state === "resolved" && r.resolution === "Fixture failure confirmed" && r.assignee === owner && r.revision === 3), "Review items were not claimed then resolved exactly once by the coordinator");
  check(reviews.find((r) => r.reason === "low_score")?.rating_id === bad.id, "Low-score review does not reference the rating that opened it");
  const done = (c: any) => c.ok && c.completed;
  const rates = calls.filter((c) => c.name === "processes_run_rate" && c.completed);
  const refused = rates.filter((c) => !c.ok);
  check(refused.length === 1 && refused[0].args.step_id && String(refused[0].result).includes("a step it executed"), "Self-rating of an executed step was not attempted exactly once and refused");
  check(rates.filter(done).length === 2 && rates.filter(done).every((c) => !c.args.step_id), "Expected exactly two successful whole-run rating calls");
  const firstRate = calls.findIndex((c) => c.name === "processes_run_rate");
  check(calls.slice(0, firstRate).some((c) => done(c) && c.name === "processes_run_get"), "Coordinator rated without reading the run first");
  const updates = calls.filter((c) => done(c) && c.name === "processes_review_update");
  check(updates.filter((c) => c.args.state === "claimed").length === 2 && updates.filter((c) => c.args.state === "resolved").length === 2, "Expected two claims and two resolutions");
  check(calls.filter((c) => done(c) && c.name === "processes_quality_get").length === 1, "Expected exactly one quality read");
  const lastResolve = calls.map((c, i) => (done(c) && c.name === "processes_review_update" && c.args.state === "resolved" ? i : -1)).reduce((a, b) => Math.max(a, b), -1);
  check(calls.findIndex((c) => done(c) && c.name === "processes_quality_get") > lastResolve, "Quality read happened before the review queue was cleared");
  check(calls.every((c) => !["processes_start"].includes(c.name) || ["rate-ok", "rate-fail"].includes(c.args.idempotency_key)), "Started an unexpected run");
}
