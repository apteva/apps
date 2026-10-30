# Processes 0.17.1

Ratings are now an external judgement, like a manager reviewing someone else's
work. An agent that owns a run or executed any of its steps can no longer rate
that run or any of its steps; it records its own account with a run summary
instead. People in the panel and agents with no part in the run still rate.

Rating requests no longer go to the run's coordinator. A procedure's `quality`
block names a `reviewer` (`{kind: agent, agent_id}` or `{kind: human}`), and
`request_rating` now requires an agent reviewer. When a run finishes, Processes
sends that reviewer one durable rating request (retried with backoff, attached to
the Processes tools first). A reviewer that is also the run's owner or an executor
is skipped rather than asked to grade itself. Terminal coordinator notices no longer
mention rating.

Tool descriptions, the skill and the panel rating bar now say who rates and why;
executors are pointed at `quality_get` to learn what reviewers expect.

Migration 022 adds the durable rating-request outbox. 0.17.0 procedure versions
that set `request_rating` without a reviewer keep running but send no requests;
saving a new version requires a reviewer.
