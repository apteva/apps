# Processes v0.9.3

Sequential agent-backed procedures whose agent steps share one executor now
reuse a single worker for the run. Each step is still claimed and completed
individually, with dependency checks, frozen instructions, assignment parameters
and approval evidence. Parallel branches, different agents and Tasks-backed
runs retain their existing execution paths.

The new `step_claim` tool records worker ownership and marks ready work running.
Subsequent ready steps are delivered directly to that worker with short notices.
Migration `008_run_workers.sql` adds the ownership table; no existing records are
rewritten. No new platform permissions or Core/server/SDK changes are required.
The app continues to require Apteva 0.51.3 and app-sdk v0.81.0.

## Validation

The simulated three-step weather Tier 3 benchmark passed with one worker instead
of three, 17 model iterations instead of 21, and 186,234 reported tokens instead
of 245,322 (24% fewer). Whole-scenario time was 93.3 seconds versus 98.2 seconds
before; an earlier post-change run took 79.5 seconds. Workflow-only timings were
mixed, so these samples do not establish a consistent latency improvement.

The five-step, three-agent Tier 3 regression passed with parallel inputs,
dependency joins, explicit approval and independent workers. The test fixture
now clearly delimits exact expected output strings; its assertions are unchanged.
See [scenario results](scenarios/README.md#sequential-worker-benchmark) for metrics
and the recorded failed test attempts.

Release validation passed with the pinned SDK and `GOWORK=off`: the Go race and
integration suite, 33 UI/verifier tests, all eight Playwright browser tests,
the panel build and host-import verification, and the release binary build.
