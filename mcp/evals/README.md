# Evals


## Active cancellation and attempt history

Evals 0.9.6 builds on published 0.9.5 and requires Environments 0.7.14 or later.
Cancelling an experiment immediately cancels queued tests. Active tests persist
`cancel_requested_at` and show `cancelling` until Environments finishes bounded
voice shutdown. Cancellation is independent of the originating HTTP connection.
It prevents further stages, scoring, claiming and automatic or explicit retries
within that cancelled experiment. Cancelled results remain unscored. Pending
cancellation is recovered after a runner restart.

The runner uses the same shutdown operation during cancellation, normal completion
and deferred cleanup. It does not requeue an invalid simulation until the preceding
environment has stopped successfully. HTTP cancellation returns 202 with current
progress; MCP returns the experiment alongside acknowledgement. Inspect progress
with `eval_experiment_get` or `eval_run_get`.

Every started attempt retains its ordinal, environment/run ids, voice evidence,
original failure and cleanup response in `attempts`. Cleanup errors and unexpected
404s are retained separately, and a failed stop is followed by stored-run inspection
where available. Retrying cannot erase the first attempt's evidence.

Voice cases accept `microphone_mode` (`auto`, `continuous`, `discontinuous`) and
`final_reply_contains`, forwarded to Environments. These are generic transport
and scenario requirements, independent of provider/model names.

Validation uses full Go race suites plus actual local WebSocket/HTTP fixtures.
Live GPT-Live startup/long-pause/backend-delay validation is not claimed by this
release; correcting idle input does not by itself prove the provider timeout fixed.

### Voice startup policy

Voice cases spawn their target with `start_paused: false`, allowing realtime
workers to delegate on existing Core versions. Environments also leaves the
simulated caller unpaused. Main may run during the test. Text cases retain the
startup pause, inject their task and then release the instance gate as before.
No manual resume or caller-opening injection is added by this startup change.
