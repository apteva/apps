# Processes 0.17.0

Run ratings. Operators, reviewers and agents rate finished runs and individual
steps with a 1–5 or thumbs score, reason tags, an optional typed rubric, a comment
and an optional corrected output. A procedure's `quality` block (scale, tags,
criteria, rating requests, review rules) is frozen with each version. One active
rating exists per rater and target; rating again creates a superseding revision.
Agents cannot rate steps they executed.

Review queue. Low scores (default 2 or lower), failed runs and a sampled share of
completed runs open review items, which can be claimed, released, resolved or
dismissed with optimistic revisions. Run-state rules fire in SQL with the state
transition.

Quality analytics. `quality_get` reports average score, rating coverage and score
distribution per version, top reason tags, rubric aggregates, step quality per
executor agent, lowest-rated runs and open review counts. `runs` and `run_get` carry
a compact rating summary. When a procedure sets `request_rating`, the coordinator's
terminal-run notice asks it to rate the outcome.

New MCP tools: `run_rate`, `run_ratings`, `quality_get`, `review_queue`,
`review_update`. New events: `run.rated`, `run.rating_revised`,
`review.item_created`, `review.item_updated`. The panel adds a rating bar on run
details, a rating badge and filter in the runs list, and a Quality tab with the
review queue. The overview counts reviews.

Migration 021 adds the ratings and review tables. Existing procedures, runs and
history are unchanged; procedures without a quality block use default tags and
the default low-score review rule.
