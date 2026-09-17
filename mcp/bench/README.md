# Bench

Reproducible, publishable benchmarks for agents.

Evals answers *"did my agent regress?"*. Bench answers *"which target is better,
and can someone else reproduce that number?"* — so Bench does not run anything
itself. It delegates execution to **Evals**, which runs cases inside isolated
**Environments**, and owns the four things that turn a test result into a
benchmark result:

| Bench owns | Delegated |
|---|---|
| Sealed, content-hashed scenario packs | Execution, cohorts, assertions, judge → `evals` |
| The frozen scoring contract and budgets | Isolation, seeding, snapshots → `environments` |
| Admission control | |
| Cross-run leaderboards and baselines | |

## Packs: draft, then sealed

A **draft** pack is freely editable and cannot be run. `bench_pack_seal` copies it
into a **sealed** pack: immutable, versioned, and identified by a SHA-256 digest
over its definition. The draft stays editable, so authoring the next version
never mutates the one that existing scores refer to.

This is the whole point. A benchmark anyone can edit is not a benchmark — if a
budget can be lowered after a bad score, every earlier number is meaningless.
Leaderboards therefore join only on `(pack_digest, scoring_version)`; results
from two different digests are never averaged together.

Sealing refuses a scenario that pins no environment or snapshot (its world is not
reproducible) or that carries no budgets (it is not scoreable).

```sh
bench_pack_create   { "name": "Apteva Core" }
bench_scenario_put  { "pack_id": "...", "name": "Create then update a contact", "prompt": "...",
                      "environment_id": "env-crm", "checks": [...], "budget": {...} }
bench_pack_seal     { "id": "..." }                      # → version 1.0.0 + digest
bench_run_create    { "pack_id": "<sealed id>", "targets": [...], "trials": 5 }
bench_leaderboard   { "pack_digest": "..." }
```

## Scoring: `2026-07.verified-v1`

Pass rate is the headline; the score is secondary.

| Component | Points |
|---|---|
| Deterministic task success | 70 |
| Duration within budget | 10 |
| Actual cost, or tokens when cost is unavailable | 10 |
| Turns within budget | 5 |
| Zero tool errors | 5 |

A failed run scores **0** — efficiency is only meaningful on work that actually
succeeded. Efficiency components award full points at or under budget and decay
linearly to zero at twice budget. Tool-call *count* is reported but never
rewarded or penalized.

Not every provider reports cost. When it is missing, efficiency falls back to
tokens and the result records `cost_basis: "tokens_total"`; the leaderboard flags
any target whose runs mix the two bases, because their cost columns are not
comparable.

Changing any weight or curve above means minting a new scoring version, not
editing this one.

## Admission control

A provider or environment failure before the agent ever ran says nothing about
the target, and must never become a zero-score row that drags a pass rate down.

| Admission | Meaning |
|---|---|
| `verified` | The agent ran and deterministic checks were evaluated. Counts. |
| `diagnostic` | The agent ran, but the scenario declares no checks, so the outcome rests on a judge. Scored, not comparable. |
| `invalid` | Harness failure. Withheld from the record entirely, and announced as `bench.result.rejected`. |

An agent that ran to completion and simply got the task wrong is `verified` and
scores zero — that *is* evidence.

## Known limitation: snapshot identity

`Provenance.SnapshotsVerified` is always `false` today, and every evidence bundle
says so in its `caveats`.

Environments identifies a snapshot by id, not by a content digest, so two runs
naming the same snapshot are *asserted* — not proven — to have seen the same
world. Closing this needs a digest on the Environments snapshot model; until
then, treat cross-time comparisons on the same digest as trustworthy only insofar
as the underlying snapshot was not re-created.

## Development

```sh
go build ./...
go test ./...
APTEVA_GATEWAY_URL=http://localhost:5280 APTEVA_APP_TOKEN=dev-token APTEVA_INSTALL_ID=0 go run .
```

The panel is bundled from the repo root:

```sh
bun run scripts/build-panels.ts --app bench
```

One Evals suite is materialized per sealed digest and reused by every later run
of that digest, so Evals does not accumulate a suite per benchmark run.
