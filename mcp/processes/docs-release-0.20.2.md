# Processes 0.20.2

Run cards show their summary again.

The line under each run card is the run summary, which the agents write
themselves. Since 0.20.0, isolated workers only got the summary tool when the
procedure used cross-run memory, so most runs ended without a summary and the
card showed only "Team workflow".

- Every step worker has `processes_summary_update` again (5 Processes tools;
  memory procedures add 5 more).
- The worker on the run's last step (no other step depends on it) is told in
  its brief to save the run summary just before its final `step_update`: an
  outcome for the card, what each step did, and receipts or evidence IDs.
- A worker that blocks or fails is told to save the reason as a `blocker`
  summary.
- A summary write with a stale `expected_revision` now says which revision to
  retry with (still HTTP 409).

The run owner can still write the summary itself. Runs finished before this
release keep no summary. Agents still see 42 tools.

Validated with `GOWORK=off go test ./...` (one new test) and 209 Bun tests. No
UI, API shape, migration, Core, Server or SDK changes.
