# Trading v0.10.0

Trading now runs rule-based strategies and agents through the same event-driven
historical simulation engine. Agents can react to prices, delayed sentiment,
news, custom events, and fill notifications while the UI shows replay progress.

## Changes

- Deterministic event ordering, configurable order/cancel latency, partial fills,
  transaction costs, portfolio risk checks, benchmark metrics, and durable
  checkpoints.
- Generic inputs with separate event and availability timestamps, preventing
  historical feeds from exposing information before it arrived.
- Agent simulation with isolated observations and staged orders, explicit
  decision completion and memory, bounded history and decision budgets, and
  pause/resume/cancel controls.
- Portable artifacts with input and result hashes, engine provenance, and
  recorded agent observations, commands, memory, and accepted tool calls.
  Recorded agent runs replay without model calls and reject missing or changed
  decisions.
- Broker account/environment verification, Alpaca paper routing and client-ID
  recovery, crypto historical data and ambiguous-submission recovery fixes.
- Corrected strategy candle timing, realized P&L, execution-policy capture,
  simulation controls, and SDK dependency pin (v0.77.0).

## Upgrade and execution notes

Migrations 019 and 020 add simulation checkpoints, output ledgers, and agent
decision storage. Existing runs retain their original execution model; create
new runs to use the event simulator. Saved simulation artifacts require the
matching engine source fingerprint.

Fresh agent simulations inherit source runtime model settings and can differ.
Deterministic reproduction uses recorded decisions. Inference freezes the
simulation clock; model wall-clock time is not added to execution latency.
The source agent must have access to the replay observation and completion tools.

News and sentiment feeds are supplied as timestamped inputs; this release does
not add provider subscriptions. Alpaca recovery requires the companion
integrations catalog update exposing `get_order_by_client_order_id` and the
credential-selected paper/live host. Publishing these sources does not upgrade
the integrations catalog embedded in an existing server.

The execution model is cash-funded and long-only. Bar-derived quotes are an
approximation of liquidity, not an order-book reconstruction. Model training
knowledge can still bias historical agent evaluations.

## Validation

Release checks cover Go tests and race detection, simulator/agent replay,
interrupted decisions and recovery, UI interaction tests, TypeScript checks,
production UI builds, and the companion Alpaca catalog regression tests.
Runtime lifecycle and broker tests use mocks; no real model session or broker
order was executed. Publishing this release does not update running installs.

See [Event backtesting](EVENT_BACKTESTING.md) for API/MCP examples and the agent
prices, sentiment, and news workflow.
