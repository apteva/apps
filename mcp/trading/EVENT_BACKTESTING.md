# Event backtesting

New strategy and agent backtests use `event-sim/1`. Both receive timestamped
inputs and submit commands to the same order, fill, risk, and portfolio engine.
Existing stored runs retain their original execution model.

There are three decision modes:

| Mode | Decision maker | Reproduction |
|---|---|---|
| Strategy | Saved rules evaluate completed candles and available features | Reevaluate the captured rules on the same tape |
| Agent | An isolated agent reacts to selected inputs and executions | Replay its recorded observations and decisions without calling a model |
| Recorded orders | External `order.intent` / `order.cancel_intent` inputs | Execute the supplied order tape |

The decision layer is separate from execution. Live/paper agents continue using
the regular trading tools and portfolio order path. In an agent backtest, those
tools are scoped to the simulated observation and stage commands for the event
engine. This adds agent evaluation to historical simulation; it does not turn
historical runs into live broker sessions.

## Running and watching

Create a strategy run through the Strategies tab or `strategy_backtest_create`.
Create an agent run using **New agent event simulation** in Backtests: select a
source agent, symbols, dates, a directive, trigger types, a decision budget, and
optional additional event JSON. The UI/HTTP path captures historical bars and
merges additional events. `agent_backtest_create` instead accepts a complete
event tape supplied by your own feed adapters.
In Backtests, open **Execution settings and additional inputs** before starting
to configure the seed, acknowledgement/cancellation latency, latency jitter,
maximum quantity per quote, volume participation, and buy-and-hold benchmark.
The benchmark must have captured market data. HTTP creation automatically adds
an explicitly requested benchmark to the captured symbols (same market calendar).

Run dispatches a background worker and returns immediately. Progress includes
processed input count, simulated time, orders/fills, portfolio state, and
benchmark-relative return. Step processes one simulated timestamp. Pause waits
for the worker to stop; Resume continues its durable checkpoint. A process
restart leaves interrupted runs paused for explicit resume. While an agent is
deciding, the UI shows **Agent deciding · simulated clock paused**, and pause
and cancel remain usable. An unfinished decision is discarded on pause/cancel;
resume retries it from the last committed timestamp. Completed decisions saved
before an interrupted checkpoint are reused. Cancellation commits no further
financial actions.

All simulation orders are local accounting operations. They never call brokers.
The portfolio model is cash-funded and long-only; it does not model borrowing,
short-sale financing, or derivatives margin.

## API and MCP

Routes are relative to the trading app API and retain project authorization:

| Method | Route | Purpose |
|---|---|---|
| POST | `/portfolios/{id}/backtests` | Create; supply `strategy_id` or `agent_id`, dates/symbols, optional `simulation` config and `inputs`; agent options use `agent_simulation` |
| POST | `/backtests/{id}/run` | Start/resume in the background |
| POST | `/backtests/{id}/step` | Process the next simulated timestamp |
| POST | `/backtests/{id}/pause` | Pause at a committed checkpoint |
| POST | `/backtests/{id}/cancel` | Cancel |
| GET | `/backtests/{id}/simulation` | Execution configuration, extra inputs, and current state |
| PUT | `/backtests/{id}/inputs` | Replace extra inputs/configuration while queued |
| GET | `/backtests/{id}/events` | Recent persisted order/fill/decision activity |
| GET | `/backtests/{id}/performance` | Portfolio series and performance/benchmark metrics |
| GET | `/backtests/{id}/artifact` | Download a portable JSON result bundle |
| POST | `/backtests/import` | Import `{portfolio_id, name?, artifact}` as a new queued run |

MCP exposes `strategy_backtest_create` (including `simulation` and `inputs`),
`agent_backtest_create` (complete tape, source agent, and `agent_simulation`),
`backtest_control` (`backtest_id`, `action`: `status`, `run`, `step`, `pause`,
`cancel`), and `backtest_artifact` (export by `backtest_id`, or import with
`portfolio_id` and `artifact`).

Example optional configuration:

```json
{
  "simulation": {
    "seed": 42,
    "submission_latency_ms": 150,
    "cancellation_latency_ms": 100,
    "latency_jitter_ms": 20,
    "max_fill_qty": 10,
    "participation_rate": 0.05,
    "benchmark_symbol": "AAPL",
    "costs": {"fee_bps": 1, "slippage_bps": 5, "spread_bps": 2, "impact_bps": 10}
  },
  "inputs": [{
    "id": "sentiment-aapl-001",
    "type": "feature.sentiment",
    "symbol": "AAPL",
    "source": "research-feed-v1",
    "event_time": "2026-01-05T14:00:00Z",
    "available_at": "2026-01-05T14:05:00Z",
    "data": {"score": 0.8}
  }]
}
```

A strategy condition can use `"indicator": "feature:sentiment.score"`.
The feature is unavailable before 14:05 even though it describes a 14:00 event.
Omitting `symbol` defines a global feature fallback. Indicator windows and
rebalance cadence still use completed strategy candles; new feature arrivals
become eligible at the next strategy evaluation.

## Agent decisions

Each subscribed event starts a fresh environment and conversation using the
source agent's runtime provider/model selection. The supplied simulation
directive replaces its usual directive; the portfolio mandate is the default.
The source agent must permit the trading tools, including the replay observation
and finish tools. The runner clones the trading app and requests blocked
external integrations. Other trading tools cannot fall through to live providers
or the cloned portfolio database while the replay mailbox is active.

The agent receives an immutable observation containing simulated time, the
current event, portfolio state and metrics, bounded already-consumed input
history, and explicit memory from its previous completed decision. In the
observation, the simulated portfolio always has ID `1`.

| Tool | Meaning during agent replay |
|---|---|
| `backtest_observation` | Current observation, event, portfolio, and memory |
| `backtest_events` / `market_history` | Already-consumed input history, bounded by `history_limit` |
| `market_quote` | Latest quote already observed for the requested symbol |
| `portfolio_get`, `account_summary`, `positions_list`, `orders_list` | Simulated state; orders also show staged commands |
| `order_place` / `order_cancel` | Stage commands; no broker call and no fill while reasoning |
| `journal_read` / `journal_write` | Read prior explicit memory / record a note in the accepted tool trace |
| `backtest_decision_finish` | Seal commands, rationale, and memory for the next event |

The agent must explicitly finish even when choosing no trade. Only the `memory`
passed to finish survives as memory in the next fresh conversation; journal
notes remain in the artifact trace. Commands use the engine's normal latency,
liquidity, cost, and risk models after completion. Fill notifications are
`execution.fill` events and can trigger another decision, including for partial
fills. They appear as the current event and in the output ledger; bounded input
history contains external tape events.

`trigger_types` supports exact names and suffix wildcards such as `feature.*`
and `news.*`; omitted/empty subscribes to all inputs and fill notifications.
Defaults are 1,000 decisions per run, 180 wall-clock seconds per decision, and
100 input events of history. Limits are 10,000 decisions, 5–1,800 seconds, and
1–1,000 history events. Each turn allows 64 commands and 256 accepted tool calls.
A budget exhaustion, timeout, or runtime error fails the run explicitly. Resume
retries an unfinished turn; it does not increase an exhausted decision budget.

The simulation clock stays frozen during inference. Model wall-clock time is
not added to order latency. Each fresh environment has startup overhead.
Source model settings are inherited at execution time, not pinned in artifacts;
new model runs can differ. Exact reproduction applies to the **recorded
decisions**, not to re-running model inference. The harness controls tape and
tool availability but cannot remove historical knowledge learned by the model
during training.

Example `agent_backtest_create` arguments (replace the portfolio and agent IDs
with accessible ones). This is a complete tape: prices, delayed sentiment,
news text, and a later quote on which an acknowledged order can fill.

```json
{
  "portfolio_id": 1,
  "agent_id": 123,
  "name": "Agent with prices, sentiment and news",
  "starting_cash": 10000,
  "symbols": ["AAPL"],
  "agent_simulation": {
    "directive": "Use only observed evidence. Explain each decision. Keep exposure within the portfolio risk limits.",
    "trigger_types": ["market.quote", "feature.*", "news.*", "execution.fill"],
    "max_decisions": 100,
    "timeout_seconds": 180,
    "history_limit": 100
  },
  "simulation": {"seed": 42, "submission_latency_ms": 150, "max_fill_qty": 2, "benchmark_symbol": "AAPL"},
  "inputs": [
    {"id": "q1", "type": "market.quote", "symbol": "AAPL", "source": "captured-prices", "event_time": "2026-01-05T15:00:00Z", "available_at": "2026-01-05T15:00:00Z", "data": {"price": 100, "volume": 1000}},
    {"id": "s1", "type": "feature.sentiment", "symbol": "AAPL", "source": "sentiment-v1", "event_time": "2026-01-05T15:00:00Z", "available_at": "2026-01-05T15:01:00Z", "data": {"score": 0.8}},
    {"id": "n1", "type": "news.article", "symbol": "AAPL", "source": "captured-news", "event_time": "2026-01-05T15:01:00Z", "available_at": "2026-01-05T15:02:00Z", "metadata": {"headline": "Company announces a product launch", "body": "Historical article text goes here."}},
    {"id": "q2", "type": "market.quote", "symbol": "AAPL", "source": "captured-prices", "event_time": "2026-01-05T15:03:00Z", "available_at": "2026-01-05T15:03:00Z", "data": {"price": 101, "volume": 1000}}
  ]
}
```

Creation returns a queued backtest. Start it with `backtest_control` using its
`backtest_id` and `action: "run"`. News and sentiment acquisition are feed-adapter
responsibilities: the app accepts their timestamped payloads, but this feature
does not install a news or sentiment provider.

## Extending inputs and execution

`internal/backtest` is an independent Go package with `Input`, `Config`, `State`,
`Command`, `Strategy`, and `Engine.Advance`. It performs no network or database
I/O and never reads wall time. A custom strategy callback receives every event.
Custom types can carry numeric `data` and string `metadata`; add a feed adapter
by converting its records to this contract, without changing the clock or ledger.

Inputs have unique IDs and separate `event_time` and `available_at` timestamps.
The latter orders replay. At equal timestamps features precede bar closes,
then custom inputs, then opening quotes. Symbol/source/ID break remaining ties. Quote inputs represent snapshots: supply
one quote per symbol per availability timestamp, coalescing updates or preserving
finer timestamp precision when a feed contains several updates at once.
Internal acknowledgement, expiry and cancellation events use monotonic sequence
numbers. IDs and latency jitter derive from the configured seed.

Built-in market events are `market.quote` (`price`, optional `bid`, `ask`,
`volume`) and `market.bar.close` (`price`, `volume`, `step`). The app adapter
supports `order.intent` with `symbol`, numeric `qty`/`limit_price`/`stop_price`,
and metadata `side`, `order_type`, `tif`, `expires_at`; `order.cancel_intent`
uses metadata `order_id`. Set the artifact spec `decision_mode` to
`recorded_orders` to replay these decisions without also running strategy rules. A complete
custom market tape can be imported in an artifact; queued extra-input editing
does not overwrite captured market prices.

Market, limit, and stop orders support GTC, IOC remainder cancellation, and day
expiry with an explicit exchange-aware `expires_at`. Fills require a fresh quote
after acknowledgement. Simultaneous sells fund buys. Liquidity is shared among
orders for a symbol; max-fill quantity and volume participation produce partial
fills. Costs include fees, spread, slippage, and a configurable volume impact
estimate. Quantity/minimum-notional constraints and captured portfolio exposure,
daily loss, and drawdown limits are checked during execution.

Historical bars expose their close only at the completed-bar time. Opening
quotes use the previous completed bar's volume as an explicit liquidity
estimate, never the current bar's future volume. This is a bar-level model,
not an order-book reconstruction: latency fills wait until the next available
quote, limit/stop triggers use quotes rather than inferred intrabar paths, and
volume impact is an estimate. Import finer quote data for finer execution timing.

## Validation suites

Use a captured event run as the source of out-of-sample, walk-forward, robustness,
stress, or execution Monte Carlo validation. Strategies and agents use the same
engine, with warmup separated from financial results and training-only candidate
selection. See [Validation suites](VALIDATION.md) for configuration and limits.

## Reproduction and result artifacts

State and each timestamp's event outputs commit in one database transaction
using a revision check. Pause/cancel or a competing writer cannot partly commit
a step. The checkpoint contains the input cursor, simulated clock, pending
events, positions, cash, cumulative realized P&L/fees, orders, features, benchmark,
and strategy history. Closed positions do not erase realized profit or loss.

Bundles contain the canonical input tape, input hash, strategy definition and
version, execution settings and captured risk policy, reference-data manifest,
engine version and source fingerprint, complete event/order/fill/portfolio
ledger, benchmark history in portfolio snapshots, final metrics, and result hash.
The benchmark is a frictionless buy-and-hold series starting at the first
available benchmark quote. Excess return is strategy return minus benchmark
return, in percentage points.

Import validates hashes and the engine fingerprint, ignores old output state,
and replays the inputs from starting cash. Completion compares the new result
hash with the imported expected hash. Database IDs, wall-clock timestamps and
UI notification timing are excluded from the reproducible result. Different
engine source versions must be replayed with their matching implementation.
Changing configuration or extra inputs creates a new input hash; it is no
longer an exact reproduction of the original bundle.

Agent bundles additionally contain the simulation directive, source agent ID
and name, triggers and budgets, each observation and its hash, accepted tool
calls, order/cancel commands, rationale, explicit memory, and a decision-set
hash. Each decision is saved before its financial checkpoint so a retry does
not ask the model for an already completed decision. Import verifies hashes
and decision structure; replay also verifies each observation against current
simulated state. Missing or mismatched decisions fail closed without invoking
an agent. Imported agent replay inputs are immutable. Export a completed run
for a complete reproduction; an incomplete recording cannot supply decisions
for its unfinished future events.

Runtime lifecycle and transport are covered by mocked integration tests,
including pause/cancel cleanup and replay without model calls. A real runtime
and model smoke test is still required before claiming end-to-end deployment
validation.

Verification:

```sh
GOWORK=off GOCACHE=/private/tmp/codex-go-build go test ./... -short
GOWORK=off GOCACHE=/private/tmp/codex-go-build go test -race ./internal/backtest
GOWORK=off GOCACHE=/private/tmp/codex-go-build go test -race -run 'TestAgent|TestRecordedAgent' .
cd ui/desk
bun install --frozen-lockfile
bun test
```
