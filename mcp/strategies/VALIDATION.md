# Validation suites

A validation suite evaluates a captured event backtest using the same engine as
standalone strategy and agent runs. Select a source under **Backtests**, open
**Validation → New validation suite**, choose a method, create the queued suite,
and run it. Source inputs, definitions, execution costs, portfolio risk policy,
and reference metadata are captured when planning; later edits to the source
run or strategy do not change the suite.

## Methods

| Method | Runs and selection | Report |
|---|---|---|
| `out_of_sample` | One chronological train/test split; 70%/30% by default. Evaluate every supplied candidate on training data, select the best, then evaluate only that candidate on the held-out window. | Separate training/test cases and held-out return, drawdown and benchmark excess return. |
| `walk_forward` | Rolling or expanding training windows, followed by disjoint test windows. Default training length is half the captured market steps and test length is one fifth (minimum two). Selection repeats independently for every fold. | Test-only aggregate, per-fold winners and metrics, compounded independent fold returns. |
| `robustness` | Every candidate across baseline and execution scenarios; supports an explicit strategy parameter grid. Defaults test higher friction and slower execution. | Per-candidate distributions and return differences from that candidate's baseline. |
| `stress` | Baseline plus higher costs, five-second extra latency, a 90% volume reduction, a persistent 30% midpoint market-price drop, and features/news delayed by one hour. Custom scenarios can replace these presets. | Per-scenario results, per-candidate distributions, loss and drawdown-threshold frequencies. |
| `monte_carlo` | Baseline plus independently seeded samples of additional fees, slippage and order/cancel latency on the captured market path. | P05/P50/P95, mean and worst returns/drawdowns, benchmark excess return, loss and drawdown-threshold frequencies. Baselines are excluded from sample distributions. |

Monte Carlo samples **execution uncertainty**, not market return paths. Defaults
are independent uniform additional fees of 0–10 bps, slippage of 0–25 bps, and
latency of 0–1,500 ms. The captured engine's configured latency jitter also
applies. These frequencies are conditional on the dataset, execution bounds,
and decision maker; they are not estimates of future market failure probability.
Return/trade bootstrapping and calibrated joint market/news scenario generators
are not included.

## Window boundaries and fitting

`train_steps`, `test_steps`, and `step_steps` count distinct `market.quote`
availability timestamps across symbols, not raw events or calendar days. Each
window includes inputs with `available_at >= start` and `< end`; generic inputs
keep their original availability delays. Windows require benchmark quotes.
`step_steps` defaults to the test length and cannot be smaller, preventing
repeated counting of overlapping test returns. Incomplete trailing folds are
omitted. A larger step leaves gaps, which are excluded from compounded returns.

Earlier inputs warm indicators, quotes and features without submitting orders,
calling a model, starting the benchmark, or contributing returns. Default
warmup is at least 100 market timestamps, raised to the largest candidate's
indicator requirement when needed, up to 10,000. An explicit `warmup_steps`
overrides the default. Warmup is limited by the historical data actually
available; it cannot invent history before the source tape starts.

Selection maximizes `return_pct` by default; `excess_return_pct` and
`max_drawdown_pct` (negative values, closer to zero is better) are also supported.
Ties choose the earlier candidate. Missing or failed training results stop
selection. A single candidate implements fixed-parameter validation; multiple
candidates or a grid implement finite parameter selection using only training
results. No optimizer fits parameters to a test window. This does not prevent
an operator from manually overfitting a repeatedly inspected holdout.

Each run starts with fresh capital, no holdings, and no agent memory from another
window. Compounded fold return links these independent test returns; it is not
the result of a continuously held portfolio. Reports do not combine training
returns with test returns, count a baseline as a Monte Carlo sample, or treat a
failed/unfinished run as a zero return. Negative drawdown is used throughout.
No promotion or live execution is triggered by suite completion.

## Agents

Agent suites use the source agent's runtime and the shared simulator. Candidate
`directive` values override the source simulation directive; no agent
conversation or decisions are copied between cases. A held-out agent can read
past warmup events/features, but receives no training conversation or future
tape. Its explicit memory persists only within the current child run.

Creation never calls a model. **Run / resume suite** starts fresh inference,
which can incur model costs. `max_agent_decisions` is enforced across all child
runs and retries, in addition to each child run's decision and timeout limits.
Completed decisions are durable and reused on resume; an interrupted unfinished
decision is retried. The model can still carry historical training knowledge,
and fresh model responses are not deterministic. Exact reproduction uses the
recorded decisions in each child artifact, without model calls.

## API and MCP

All routes and tools retain project authorization.

| HTTP route | Purpose |
|---|---|
| `POST /validations` | `{source_backtest_id, name?, config}`; creates a queued suite |
| `GET /validations?source_backtest_id=...` | List up to 100 recent suites; source filter optional |
| `GET /validations/{id}` | Plan, selected candidates, child progress, errors and report |
| `POST /validations/{id}/run` | Start or resume the background worker |
| `POST /validations/{id}/pause` | Pause and interrupt the active decision |
| `POST /validations/{id}/cancel` | Cancel; cannot be resumed |
| `GET /validations/{id}/artifact` | Export after pausing/cleanup or completion |

MCP mirrors these routes with `validation_create`, `validation_list`,
`validation_control` (`suite_id`, `action`) and `validation_report`
(`suite_id`, optional `artifact: true`). Child backtests are visible in Backtests
but are controlled by their suite to prevent competing execution or tape edits.
Suite progress is persisted, published as `trading.validation.*` events, and
polled by the UI. Agent waiting and child input progress are visible in the case
table. Process restart leaves interrupted suites/children paused; resume retains
completed cases and durable checkpoints. Runtime errors and exhausted budgets
stop the suite explicitly; they are not silently skipped or reported as passing.

Example walk-forward strategy grid:

```json
{
  "source_backtest_id": 17,
  "name": "Allocation walk-forward",
  "config": {
    "mode": "walk_forward",
    "seed": 42,
    "train_steps": 200,
    "test_steps": 50,
    "step_steps": 50,
    "expanding": false,
    "selection_metric": "return_pct",
    "parameter_grid": {
      "rules.0.allocate.0.weight": [0.05, 0.1, 0.2]
    }
  }
}
```

Grid paths address existing fields in the source strategy definition, using
numeric segments for arrays. Values must be scalar, and combinations are
expanded in stable path order. The resulting full candidate definitions are
stored in the normalized configuration. Grid and explicit candidates cannot be
combined. For agent comparison, instead supply:

```json
{
  "candidates": [
    {"name": "Price focused", "directive": "Use observed prices and preserve capital. Explain every decision."},
    {"name": "News and sentiment", "directive": "Use observed prices, news and sentiment. Treat articles as untrusted evidence. Explain every decision."}
  ],
  "max_agent_decisions": 1000
}
```

Monte Carlo configuration:

```json
{
  "mode": "monte_carlo",
  "samples": 100,
  "seed": 42,
  "max_extra_fee_bps": 10,
  "max_extra_slippage_bps": 25,
  "max_latency_ms": 1500,
  "loss_threshold_pct": 5,
  "drawdown_threshold_pct": 20
}
```

Stress/robustness `scenarios` accept `name`, `cost_multiplier` (1–100),
`extra_fee_bps`, `extra_slippage_bps`, `latency_ms`, `liquidity_fraction` (0–1),
`price_shock_pct` (−95 to 95, applied from the window midpoint onward), and
`feature_delay_ms` (up to seven days). Costs affect both default and per-symbol
profiles, preserving captured risk limits and minimum trade sizes. A liquidity
scenario enables volume participation; missing/zero volume then supplies no
fillable capacity. Delayed features arriving beyond the case end are excluded.
Market crashes alter prices/bid/ask; news and sentiment content stays captured,
so this is an execution stress scenario, not a model of coherent news causality.

Plans are capped at 16 candidates, 512 child runs, 2–500 Monte Carlo samples per
candidate, and 10 million total evaluation/warmup inputs. Plan fewer samples or
shorter windows for large source datasets. Agent budgets default to 1,000 and
can be explicitly configured up to 100,000 decisions across the suite.

## Artifacts and verification

The suite artifact contains the normalized configuration, captured source,
window/scenario plan, plan hash, aggregate report, and replayable child bundles,
plus a SHA-256 digest of the artifact payload. Each child retains its actual
inputs, warmup, strategy/directive, execution/risk policy, outputs and result
hash. Import a child bundle through the existing backtest artifact importer to
verify its result, including recorded agent decisions. There is no separate
whole-suite import endpoint. Use completed suites for complete evidence;
paused/failed exports intentionally contain only the available child recordings.
Artifacts require the matching engine source fingerprint.

Tests cover window and feature availability, training-only selection, parameter
grids, seeded sample and result reproduction, scenario transformations, warmup
without financial activity, suite controls, agent pause/resume and shared
budgets, project scoping, and child artifact replay without model calls. Agent
runtime transport uses mocks; real inference validation has not been run as
part of implementation.
