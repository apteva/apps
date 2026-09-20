# Bench

Reproducible, publishable benchmarks for agents.

## Categories and scenario tags

A pack can declare one normalized category such as `coding`, `customer-support`,
or `research`. Categories group independently sealed packs into a discoverable
benchmark family and can scope the global leaderboard. Scenario tags describe
the work inside a pack, for example `bug-fix`, `typescript`, `backend`, or
`test-writing`.

```sh
bench_pack_create  { "name": "Coding Core", "category": "coding" }
bench_scenario_put { "pack_id": "...", "name": "Repair a failing test", "prompt": "...",
                     "tags": ["bug-fix", "typescript"], "environment_id": "env-code",
                     "checks": [...], "budget": {...} }
bench_category_list {}
bench_leaderboard_global { "category": "coding" }
bench_result_list { "category": "coding", "tag": "bug-fix" }
```

Category and tag values are converted to stable slugs, deduplicated, and sorted.
They are part of the sealed pack digest, copied into run provenance and results,
and retained in evidence and full-data exports. Existing packs remain valid and
are simply uncategorized until a draft is updated or forked.

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

## Targets: existing agents or hidden setups

Every run accepts multiple targets. A target uses exactly one execution source:

- `agent_id` snapshots an existing project agent for regression testing.
- `draft` creates a transient agent inside the isolated Environment. It never
  appears in the project's agent list and is destroyed with the run.

This compares two directives on the same model without creating project agents:

```json
{
  "pack_id": "<sealed id>",
  "targets": [
    {
      "draft": {
        "name": "Concise candidate",
        "directive": "Use the available tools and answer concisely.",
        "mode": "autonomous",
        "config": "{}"
      },
      "model": "openai-codex/gpt-5.6-sol"
    },
    {
      "draft": {
        "name": "Thorough candidate",
        "directive": "Use the available tools, verify the result, and explain it.",
        "mode": "cautious",
        "config": "{}"
      },
      "model": "openai-codex/gpt-5.6-sol"
    }
  ],
  "trials": 5
}
```

The complete draft, provider, and model are stored in the Bench and Evals run
snapshots. Leaderboards key draft candidates by that full setup, so candidates
using the same model but different directives remain separate.

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

## Scoring profiles

Scoring is represented by immutable, content-hashed profiles. Bench ships
Verified v1, Graded v1, and Correctness only. Custom profiles are authored as
drafts through `/api/profiles`, previewed against an existing sealed pack, and
then sealed. A draft pack selects a sealed profile by `profile_digest`; sealing
the pack pins that digest permanently, and every resulting run and evidence
bundle carries the same contract.

Profile components can be success gates, budgeted metrics, or thresholds.
Supported efficiency metrics and curves are discoverable through
`bench_scoring_get`; profile definitions themselves are available through
`bench_profile_list` and `bench_profile_get`.

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

## Reading all benchmark data

Bench exposes its complete persisted record over MCP. `bench_data_export` is the
convenient one-call snapshot for backups and small datasets. For larger records,
use the paginated tools so responses stay bounded:

| Data | MCP tools |
|---|---|
| Packs and scenarios | `bench_pack_list`, `bench_pack_get` |
| Runs | `bench_run_list`, `bench_run_get`, `bench_run_search` |
| Individual scored results | `bench_result_list`, `bench_result_get` |
| Baselines | `bench_baseline_list`, `bench_baseline_get`, `bench_baseline_compare` |
| Scoring contracts | `bench_profile_list`, `bench_profile_get`, `bench_scoring_get` |
| Evals materialization links | `bench_pack_suite_list` |
| Reproduction bundles | `bench_evidence_export` |

`bench_run_search`, `bench_result_list`, and `bench_baseline_list` return a
`page` object with `limit`, `offset`, `total`, and `has_more`. Page sizes are
capped at 500. Raw results include invalid harness outcomes as well as admitted
results so an auditor can account for every attempted trial.

Evidence bundles include the exact sealed scoring profile used by the run. The
legacy verified-v1 formula fields remain present for verified-v1 consumers, but
are not attached to runs scored under a custom profile.

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
