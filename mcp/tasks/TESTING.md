# Tasks reliability checks

Run from `mcp/tasks` with the pinned SDK (outside the workspace overlay):

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go test -tags integration ./...
bun install --frozen-lockfile
bun test ui
bun run typecheck
```

Rebuild the four published panels from the apps root:

```sh
bun install
bun run scripts/build-panels.ts --app tasks
```

Tier 3 uses a real Core process and LLM, with deterministic fake domain tools
where scenarios need external work. Run all scenarios on a disposable server:

```sh
apteva test --provider openai-codex --model gpt-5.6-terra \
  --artifacts-dir /path/to/test-results ./scenarios/
```

The provider credential must be supplied at runtime through the supported test
runner environment. Never commit tokens. For request-thread response assertions,
the runner must count successful Core `done(message)` and `send(message)` calls
as responses from their originating thread. Failed tool calls are not responses.

The four additional live scenarios cover terminal schedule cancellation, a
manual occurrence on a paused schedule, complete cursor traversal, and a future
one-time deadline surviving pause/resume. Timing races and unavailable delivery
endpoints are tested deterministically in Go; asking an LLM to hit a narrow
race window would not prove these invariants.

`review_regressions_test.go` covers the reproduced review defects, including
stale due snapshots, restart delivery, evidence validation, terminal immutability,
assignment, concurrent recovery, lifecycle receipt ordering, upgrades, old active
work, and indexed overlap checks. UI tests exercise request reordering, visible
errors, keyboard dismissal/focus restoration, status projection, and local dates.

# Operations and retained history

`GET /delivery-health` (with the normal trusted project context) reports pending
and retrying deliveries, overdue schedules, unaccepted dispatches, failure
counts, and the ages of pending delivery and active work. A committed
operation can outlive a failed network response: retries use the stored target,
payload, and stable source ID. Do not create replacement work merely because a
notification is delayed. Scheduler logs expose retry errors and exhausted dispatch.

Lists are capped and cursor-paginated; activity and run history have their own
cursors. Operational queries exclude archived outcomes before applying limits,
and prioritize failures and blocked work. A parent/state index bounds overlap
lookups to one schedule's runs.

Terminal task records, event history, delivery source IDs, and lifecycle receipt
IDs are retained as an audit and deduplication ledger. There is no automatic
purge: deleting deduplication keys without a defined upstream redelivery horizon
can repeat work. Archive a consistent database backup before a deliberate
retention operation; preserve deduplication tombstones and recovery links.
This release bounds reads and rendering rather than silently deleting history.

Legacy upgrades that have not yet applied migration 004 preserve the original
ledger in `tasks_legacy_v2` and import records as terminal outcomes or blocked work.
Agent/project resolution runs at startup and every five minutes. Unresolved
records stay quarantined rather than being assigned to a guessed project.
Records already deleted by an earlier release's migration require an existing
backup; a forward migration cannot reconstruct them.

## Main-agent pacing regression (Tier 3)

Scenarios 21–23 replay direct console/admin events on the main thread using
`interaction: event`. Both “Wait for one hour” and “Stay idle for 60 minutes”
must leave the Tasks inventory empty, call Core pace, and set a one-hour rate
with a pending wake. The positive control “In one hour, review the inbox” must
create exactly one waiting one-time task. All three use a neutral directive
and the app's installed skill/tool descriptions, with three runs and a required
100% pass rate. Each run waits for six seconds of stable assertions and cleans
up its temporary agent and app data; it does not wait an actual hour.

Run individual YAML files normally with `apteva test --tier 3`. The CLI runner
must support `setup.interaction: event` and current random app-install tokens
(the disposable-server fixture requires `sqlite3` on PATH). To run all three using only the freshly
authenticated local server connection, the opt-in launcher reads and decrypts
that connection at runtime (no token is copied into source or printed):

```sh
RUN_TASKS_CODEX_PACING=1 \
APTEVA_CODEX_SERVER_HOME="$HOME/.apteva" \
APTEVA_CODEX_CONNECTION_ID=1 \
APTEVA_TEST_CLI_BIN=/absolute/path/to/apteva \
APTEVA_SERVER_BIN=/absolute/path/to/apteva-server \
APTEVA_CORE_BIN=/absolute/path/to/apteva-core \
go test -run '^TestTier3CodexPacing$' -count=1 -v -timeout 20m .
```

The launcher defaults to Codex `gpt-5.6-terra`; override with
`APTEVA_TEST_CODEX_MODEL`. It starts a disposable server and installs this local
Tasks source, leaving the real agent and its task records untouched.
