# Processes 0.16.4

A persistent executor worker can choose useful parallel execution across independent
ready process steps using Core subthreads. It retains shared preparation and owns
each step's exact saved result. Processes continues enforcing dependencies,
scheduled starts, separate step identities and receipts, validation and human
approval. Only Processes application code changed; Core, Server and SDK sources
are unchanged.

Opt in on an assignment with `worker_continuity: "per_executor"` and
`parallel_execution: "auto"`. `max_parallel_steps` bounds unfinished claimed steps
per executor (default 4, range 1–8); the effective limit is stored and frozen in
each run. Existing assignments retain their current execution behavior.

- The persistent worker receives Core's normal subthread controls and chooses
  the thread arrangement. Processes does not prescribe a fixed team.
- Compact `ready_steps` and `active_steps` hints expose eligible work and durable
  checkpoint identities without repeating procedure instructions or outputs.
- Claims enforce dependency/time gates, exact worker ownership and concurrency
  capacity under the app's mutation lock. Waiting and blocked claims retain slots.
- Children return exact results to their parent; they cannot claim or complete
  its steps. The parent validates and saves separate receipts and can checkpoint
  child and operation identities before waiting.
- The worker keeps thread-bound sessions and shared mutable operations sequential,
  inspects existing children/checkpoints after recovery, and settles delegated work
  before finishing. All approval and completion gates remain enforced.

Verification passed before release:

- Full Go race suite (100.160 seconds), parallel fixture race test, 108 Bun
  UI/verifier tests, 19 Playwright tests, TypeScript checks and manifest validation.
- GPT-6.1 Sol parallel tier 3: all eight steps completed, two child artifact
  operations overlapped for 42.609 seconds, exact separate outputs and session
  checkpoints were preserved, and actual operator HTTP approval gated publication.
  The scenario and independent saved-state/tool-trace verifier passed in 36
  iterations, 347.698 seconds, with 896,167 reported tokens.
- GPT-6.1 Sol serial continuity tier 3: one worker, one preparation, no spawned
  children, distinct outputs, validation, HTTP operator approval and publication.
  Scenario and independent verifier passed in 26 iterations, 244.970 seconds.
- Race regressions cover simultaneous claims against frozen capacity, timed
  branches, sidecar restart/checkpoint recovery, lost completion replay, wrong
  worker authorization and cancellation. Verifier tests reject fabricated overlap,
  mixed receipts, stolen ownership, early joins and premature completion.

The live fixture proves actual overlap across process steps; its controlled
45-second operations are not a Media throughput or overall latency benchmark.
