# Agent paper desk

The opt-in desk supports small, explicitly unvalidated paper experiments. Agents
own research and entry decisions. Trading owns authenticated approvals, order
idempotency and execution of preapproved exits. This does not install or run a
Strategies scheduler.

## Handoff

1. The operator enables `desk_configure` on a paper/simulation portfolio and
   assigns three distinct manager, reviewer and executor agent IDs.
2. The manager calls `desk_propose` with immutable quantity, entry, stop,
   target, maximum holding time, hypothesis, setup version and evidence.
3. The reviewer independently checks the stored packet and calls `desk_review`.
   Trading authenticates the calling agent, validates current risk/data, and
   persists the verdict, packet hash and approval reference.
4. The executor calls `desk_submit` with the decision ID and approval reference.
   Order terms come from the stored packet. Submissions reject changed terms,
   mismatched reviews, expiry, excessive price drift and policy breaches.
5. Accepted orders and their plan ownership commit in the same transaction.
   Retrying the same decision returns the same order. `desk_cancel` cancels an
   unfilled entry without disabling a filled position's exit plan.

The default approval lifetime is at most 15 minutes from proposal creation.
Price movement over 0.5% requires a fresh decision. Unknown predictive edge does
not block an otherwise valid paper experiment.

## Role permissions

Attach the native Trading app MCP to agents so the platform forwards their
authenticated caller IDs. Managed wrappers that discard caller identity cannot
propose, review or submit desk decisions.

Use deny-by-default grants for each agent:

| Role | Permissions in addition to `trading.read` and `trading.journal` |
| --- | --- |
| Research | `trading.observe` |
| Portfolio manager | `trading.observe`, `trading.propose` |
| Risk reviewer | `trading.review`, `trading.pause` |
| Execution | `trading.execute`, `trading.pause` |

All public MCP tools declare permission boundaries. Keep `trading.admin` and
`trading.order.direct` out of these grants. On enabled desk portfolios raw
`order_place` also fails closed, including requests with invented approval
arguments. Operator administration remains available through the platform.

## Risk and exits

Initial policy allows at most $1,000 notional per order, $25 planned loss
including modeled round-trip fees/slippage/spread, and preserves at least 60%
cash. Existing portfolio risk/universe policies also apply. These settings are
per-portfolio and do not enable broker or live execution.

The `paper_desk_exits` worker checks durable plans every five seconds. It
recovers filled entries, starts the holding clock at the actual fill, and
submits a reduce-only market exit when the stop, target or holding deadline is
reached. The exit is idempotent and survives agent/app restarts. Expired
unfilled entries are cancelled.

Stops and planned-loss budgets are not fill-price guarantees. The worker waits
for valid quotes and executable sessions, respects a paused/halted portfolio,
and records errors for operator/agent reconciliation. An unexpected external
position change is reported rather than silently attributed to the plan.

## Observation and evaluation

`desk_market_context` provides deterministic facts from completed source
candles: interval, latest/prior OHLCV, SMA20/50 and prior range/volume. It neither
generates entries nor claims a validated forecast. `desk_health` reports market
readiness by symbol, account state and pending exit errors. Closed ETFs do not
make healthy crypto unavailable.

Record qualifying and rejected setups through `desk_observe`. The stable IDs
prevent duplicate observations. `desk_scorecard` separates observations from
orders and includes realized cash-flow P&L after fees only for closed
experiments. Purpose `rehearsal` is excluded. The simulated uninvested-cash
benchmark is zero; open plans, fees and execution errors remain visible.

Use a separate paper portfolio for the execution rehearsal. Verify a
nonmarketable limit and cancellation, independent approval, an entry fill,
idempotent retry, automatic exit/restart recovery and final cash/position
reconciliation before relying on the agent workflow.

## Research and instrument policy

`desk_universe` pages through instruments known to Trading. Research can
inspect instruments outside the portfolio's entry policy. Use completed bars
for stock, ETF, and crypto analysis, and the general quote and history tools
for event-market research. Discovery is not proof that an instrument is active,
liquid, or currently executable.

Paper entries support stocks, ETFs, and USD spot crypto. The native portfolio
universe policy determines symbol eligibility at proposal, approval, submission
and while an entry is working. Existing protective exits can reduce positions
even after the symbol is removed from the entry universe. An open plan owns its
symbol until it closes. Prediction markets are research-only in this workflow
until outcome execution, order-book liquidity and expiry can be verified.

Record the hypothesis, versioned setup, source evidence, invalidation, expected
holding time and cost assumptions. Review gross fill-price P&L and fees together
with modeled spread and slippage already embedded in fills. The closed-trade
scorecard reports each separately and excludes rehearsals from the experiment
totals. A positive cost add-back on the same fills is not a cost-free backtest.
