# The rules engine

Every strategy is a `trading-rules/1` program. The same program, evaluator and
simulator run event backtests, validation suites and live assignments. A
program trades 1–500 symbols: single-instrument trade rules, per-symbol rules,
pairs, and cross-sectional books that hold target weights (rotation, decile
spreads, time-series momentum, factor momentum, sentiment). The catalog
presets and the trade examples in `rule_examples/` are ordinary JSON programs;
the evaluator and execution engine contain no strategy-name or ticker branches.

## Supported mechanics

- Complete OHLCV candles, multiple completed timeframes and explicit offsets.
- Arithmetic and comparisons; AND/OR, crossover events, named calculations.
- SMA, SMA-seeded EMA, Wilder RSI/ATR, rolling extremes, return, standard
  deviation and log-return volatility. Nested SMA/EMA/stddev calculations can
  express MACD and Bollinger calculations without another strategy primitive.
- Availability-timed numerical features, quote/account state, daily filled-entry
  counts, local clock and weekday values.
- Durable variables and repeat policies: once per bar, once per local day,
  until an explicit reset, or on every matching event.
- Market/limit/stop entries; linked OCO groups; position flattening and group
  cancellation. Entry triggers do not implicitly exit an existing position.
- Fixed quantity, notional, account-equity fraction, or monetary stop-risk sizing.
- Absolute or entry-relative stop/target boundaries; activated trailing stops.
- Named, complete daily windows and deterministic IANA-timezone clock schedules.
- Opt-in cash-settled linear contracts, long/short positions, multiplier,
  fixed currency conversion, margin checks and reduce-only exits.
- Multi-symbol programs: `symbols` lists 1–500 instruments; expressions read the
  rule's current symbol or name one (`{"metric":"close","symbol":"KO"}`); rules
  with `for_each` run once per symbol. Correlation and beta against a named
  symbol, calendar metrics (`day_of_month`, `month`), and scalar operators
  `sign`, `log`, `exp`, `sqrt`, `clamp`, `if`.
- Cross-sectional operators that evaluate an expression for every symbol at
  the same moment: `cs_rank`, `cs_zscore`, `cs_demean`, `cs_count`, `top`,
  `bottom`, `quantile`. Ties break by symbol; symbols without data drop out.
- The `target` action: a signed weight per symbol, normalized as long and short
  legs, to a gross, or as given; per-symbol and gross caps; a turnover band.
  Closes and sales are ordered before buys. Working target orders that already
  get within a tenth of their size of the new book are kept, not replaced.
- Opt-in spot shorts (`simulation.shorts`): proceeds to cash, continuous borrow
  fees (default 25 bps a year, per-symbol overrides), Reg T initial (50%) and
  maintenance (30%) margin with forced covers, and unshortable symbols.

## Definition and expression syntax

A saved strategy definition is `{"program": {...}}`. The program's `symbols`
and base `timeframe` are the strategy's universe and cadence; nothing else is
stored. A one-symbol program needs no symbol on its expressions:

```json
{
  "program": {
    "version": "trading-rules/1",
    "symbols": ["EXAMPLE"],
    "timeframe": "1h",
    "timezone": "UTC",
    "rules": [{
      "id": "trend-entry",
      "on": "bar.close",
      "when": {
        "op": "and",
        "args": [
          {"op":"==","args":[{"metric":"position_qty"},{"value":0}]},
          {"op":"crosses_above","args":[{"metric":"ema","period":20},{"metric":"sma","period":50}]}
        ]
      },
      "actions": [{
        "kind":"enter", "side":"buy", "stop_pct":0.01,
        "target_pct":0.02, "sizing":{"mode":"fixed_risk","amount":100}
      }]
    }]
  }
}
```

Expressions contain exactly one of `value`, `metric`, `name`, or `op`.
`op` takes `args`; comparisons produce 0/1. Division by zero, a flat candle's
close fraction, unavailable feature fields, and incomplete warmup do not invent
values. AND/OR short circuit to allow explicit availability guards.
AND/OR accept 2–64 operands; arithmetic and comparisons accept two, while
`not` and `abs` accept one. Place availability guards before their dependents.

A metric can specify `timeframe`, `period` and `offset`. Higher candle frames
must be multiples of the base. Named calculations are resolved with cycle and
nesting checks. Rolling `sma`, `ema` and `stddev` can take an `input` expression
using that same timeframe. SMA-seeded EMA uses 5N completed observations; Wilder
RSI/ATR uses 5N+1. These bounded seeds are consistent across replay and restart,
but can differ from charts using a longer seed.

`feature` takes `feature` and `field`, reading only values already published.
Rolling/crossover expressions cannot pretend mutable account state or latest
features are a historical series. Crossovers compare with data available at the
previous completed base-candle signal, including when timeframes differ.

Rules accept `on: bar.close | quote | clock | fill | any`, optional `timeframe`,
`when`, `reset`, `repeat`, and ordered `actions`. Reset checks are independent of
entry eligibility. Variables must be declared in `initial`; `set` updates them.
Actions in one rule are planned together, and unavailable sizing/calculations
prevent that action batch from firing. Pending entries and held positions prevent
implicit pyramiding. Multiple opposing entries in the same action batch form
an explicitly named OCO group.

Unknown program fields, invalid operators, invalid schedules, cycles, excessive
history and incompatible timeframes are rejected. Expression depth is bounded;
programs are data, not unrestricted executable source.

## Market observations and execution

For `market.bar.close`, `event_time` is candle start and `available_at` is at or
after candle completion. Required numeric fields are `open`, `high`, `low`,
`price` (close), and optional nonnegative `volume`; `metadata.timeframe` identifies
the interval. A declared higher-timeframe stream can be supplied directly, or
complete contiguous base candles can construct it. Incomplete aggregates are not
published. Candle timestamps must increase within each stream. Simultaneously
published intraday higher frames precede lower frames.

`market.quote` contains positive `price` and optionally bid, ask and volume.
Every market observation requires a declared source. Closes are observations,
not executable liquidity. Protective fills use subsequent actual quote events;
no high/low price path is inferred. Stops use the executable bid/ask side when
supplied. Zero latency can fill a closing signal on the next-opening quote at the
same timestamp. Positive latency can defer that fill to a later observed quote.

Local windows require all base candles from start through end. Clock schedules
are expanded deterministically inside the captured tape horizon; they do not
invent prices or extend a dataset. A flatten action at 18:00 submits a close at
18:00, but fills require captured liquidity. It remains active to handle a late
entry fill during a cancellation race. Daylight saving uses the selected IANA
calendar; a nonexistent scheduled wall time is rejected.

## Instrument and risk accounting

Ordinary spot instruments retain cash-funded long-only accounting. Short entries
require an explicit simulation `contracts` entry:

```json
{
  "starting_cash":100000,
  "contracts":{"EXAMPLE":{"multiplier":1,"currency_rate":1,"margin_fraction":0.1}},
  "costs":{"qty_step":0.001,"fee_bps":1,"slippage_bps":2}
}
```

These numbers are illustrative, not broker specifications. Currency rate converts
settlement P&L into account currency. Contract principal is not deducted as a
spot purchase: equity is cash plus marked P&L; gross exposure and fees include
multiplier/conversion. Increasing exposure checks available margin and portfolio
risk for either direction. Reduce-only exits cannot open a reverse position.
API-created runs capture the portfolio universe and risk constraints. The offline
CLI instead uses the explicit configuration supplied to it.

Fixed stop risk sizes from the planned entry/stop distance, multiplier and
conversion, then rounds down to the venue quantity increment. Fees, spreads,
gaps and slippage can increase realized loss beyond that planned budget. Absolute
stops/targets remain anchored to their expression values; percent boundaries are
anchored to actual entry fills.

Each partial entry fill receives its own protective quantity. An exit cancels an
unfilled parent remainder. Partial protective fills reduce the sibling exit's
remaining quantity. Linked groups model **atomic venue-native OCO**, not a claim
that application-issued broker cancellations are instantaneous.

## Creating and inspecting backtests

Agents can author and manage these programs entirely through MCP:

1. `strategy_catalog` returns the complete three examples, execution
   configurations, expression syntax, supported primitives, limits and workflow.
2. `strategy_validate` checks a `definition` without saving it and returns
   structured validation feedback.
3. `strategy_create` takes `name` and `definition`, returning a saved strategy
   ID. `strategy_update` versions changed definitions; `strategy_get` reads them.
4. `strategy_backtest_create` takes `portfolio_id`, `strategy_id`, a sourced
   OHLC/quote `inputs` tape and explicit `simulation` configuration.
5. `backtest_control` with `action: run` starts execution; `action: status`
   reports progress. Wait for `completed`, then inspect `summary.metrics`.
6. `strategy_scorecard_update` defines required metric thresholds and evaluation
   scope. `strategy_scorecard_evaluate` durably records a completed run's
   verdict, metrics, policy and strategy version; `strategy_scorecard_get`
   returns this history. A losing result is a valid research outcome.
7. `validation_create`, `validation_control` and `validation_report` evaluate
   captured data with holdouts, walk-forward selection, cost robustness or
   stress scenarios. `backtest_artifact` exports results or imports a replay.
8. `strategy_update` saves a new definition version. Earlier backtests and
   scorecard evaluations retain the version and policy they actually tested.

`strategy_evaluate` previews what the program would do on the latest closed bar
(target weights and orders, against a portfolio or a cash account); it is not
historical performance evidence. Never label a synthetic fixture as historical
or out-of-sample evidence; never weaken a scorecard to turn a loss into a pass.

Tier 3 scenarios under `scenarios/strategies/` exercise real LLM authoring of
all three screenshot programs and a new EMA/SMA program, followed by synthetic
winning/losing backtests, durable pass/fail evaluations, a cost robustness suite,
artifact export and versioned revision. They use isolated test installations
and assert persisted outcomes. A third scenario checks JSON-text definition
validation, saving, revision and retrieval through a real agent. Run them with `apteva test --tier 3
./scenarios/strategies/`; choose a provider/model supported by your test setup.

Strategy create/validate/update accept a definition object or JSON object text,
using the same strict program validator. Malformed definition updates fail
without silently preserving an old version.

No UI interaction or custom strategy code is required. MCP workflow tests create,
validate, update and retrieve all three catalog programs, then run and reproduce
a session strategy through the registered, agent-visible tools using JSON wire
values. These are local implementation tests, not a deployment check. Result
integrity hashes tolerate JSON object key reordering by clients while preserving
numeric precision and detecting changed contents.

Save a program using the existing strategy API/MCP. `POST
/portfolios/{id}/backtests` or MCP `strategy_backtest_create` accepts `strategy_id`,
`simulation`, and either `start_at`/`end_at` (history is captured for the
program symbols at the program timeframe; `inputs` may add feature events) or
`inputs` as a complete sourced market tape, which bypasses the history fetcher.
Captured US-equity daily bars complete at the session close. Runs use the existing
background worker, event ledger, snapshots, pause/resume and result artifacts.
Simulation settings preserve captured instrument specifications and schedules.

The Strategies UI offers the presets and trade examples, sourced-event file
import, execution configuration, backtests, evaluation, validation and
assignment. A visual rule builder edits symbols, per-symbol rules, calculation
trees, triggers, repeat/reset policies, enter/target actions, protection and
sizing, and named windows/schedules. The advanced JSON editor exposes the same
program.

## Live assignments

A live assignment evaluates each newly closed bar of the program timeframe. It
loads recent completed bars for every program symbol from Trading, replays them
through the same evaluator for indicator history, and runs the latest moment
with the portfolio's cash, positions and marks as account state (a symbol
without a mark is priced at its latest close). Variables and fired latches
persist per assignment in `strategy_rule_state`; candles are rebuilt from bars
each time. Target books go through the existing paper rebalance; entries and
flattens become revision-checked market orders.

Until Trading supports them, live runs refuse programs that short (sell entries,
`legs` books with a short leg, or a negative target weight at run time), use
protective stops/targets/trails, place limit or stop entries, read features, or
trigger on anything but `bar.close`. Validation warns "Backtest only" for these.

Offline use requires no server or broker:

```sh
GOWORK=off go run ./cmd/rule-backtest \
  --example rule_examples/donchian.json \
  --input /absolute/path/to/events.json \
  --output /absolute/path/to/result.json
```

The CLI accepts an event array or a bundle with `inputs`, and an optional
`--config` JSON override. Output preserves sources, program/configuration, full
input/output tapes, source fingerprints, hashes, metrics and terminal state.
A source label is provenance supplied by the caller, not independent validation.

## The three screenshot examples

| Example | Rules implemented | Explicit assumptions |
|---|---|---|
| Donchian | 175 closed hourly bars; minute close above/below bounds; 0.5% stop, 1% target; trail arms at 0.5%, follows 0.1%; $100 risk; directional midpoint rearming | Hourly channel refreshes on each completed H1 publication; one held position at a time |
| ATR candle | H1 range > 2.5 × Wilder ATR200; outer-quarter close and candle direction; market entry; signal-close-anchored 0.5% stop and 3.5% target; $100 risk | ATR includes the completed signal candle; 1001-candle bounded seed; no overlapping held positions |
| Session range | 08:00–11:00 high/low; paired stop entries; opposite range stop; first-fill OCO cancellation; one daily entry set; 18:00 flatten/cancel; $100 example risk | `Europe/Helsinki` is an explicit +2/+3 example, not a claim about the original broker's DST calendar; same-day windows |

## Reproducible proof and limits

Generate all nine controlled cases and independently replay each through the
standalone runner, comparing complete input/output hashes and metrics:

```sh
bun run prove-rules.ts /absolute/path/to/evidence
```

This writes a readable `REPORT.md`, summary, event bundles and test log. To run
the acceptance tests directly:

```sh
RULE_PROOF_DIR=/absolute/path/to/evidence GOWORK=off \
  go test ./... -short -run 'TestScreenshot|TestRuleBacktest|TestRuleDeadline|TestRuleMCP' -v
```

Controlled acceptance tapes prove long/short signal behavior, fixed-risk sizing,
stop/target results, trailing activation, midpoint rearming, OCO cancellation,
timed liquidation and unfilled-order cancellation. Additional tests cover
partial fills, margin, executable-side stops, DST, missing candles, nested
indicators, invalid definitions, warmup isolation and byte-identical checkpoint
replay. Synthetic outcomes are **not historical returns or profitability evidence**.

Current boundaries are explicit: at most 500 symbols per program, no implicit
pyramiding, same-day windows, a bounded expression catalog, fixed FX conversion,
no dividend events (use total-return adjusted bars for shorts), and no
financing charges or automatic margin liquidation on linear contracts. Live
execution has the limits listed under Live assignments. A 500-symbol ×
2,500-bar daily cross-sectional backtest runs in about 17 s on an M-series
laptop; the input tape itself is most of its ~1.8 GB heap
(`RUN_SCALE_TESTS=1 go test -run TestScaleCrossSectionalBook ./internal/ruleengine`).
Real historical proof requires appropriately sourced gold and DE40 OHLC and
executable quotes, and a correctly specified instrument/execution model.

## Historical candle research

### Historical quote and candle exports

`market_data_import` accepts CSV data through MCP without a broker connection.
The same parser is available offline through `cmd/market-import`. It is generic:
no instrument names or provider-specific strategy logic are embedded in it.
Supply `symbol` and `streams`; each stream requires `kind` (`quotes` or `bars`),
`source` and header-bearing `csv`. Quotes require `timestamp,bid,ask`; bars require
`timestamp,open,high,low,close`, `timeframe` (1m/5m/15m/1h/4h/1d) and `price_basis`
(bid/ask/mid/last). Optional nonnegative volume is retained. A bar timestamp is
the candle's start; it becomes available only after the declared interval.

```json
{
  "symbol": "XAUUSD",
  "streams": [{
    "kind": "quotes",
    "source": "my_broker_historical_export",
    "csv": "Time,Bid,Ask\n2025-01-02T10:00:00Z,2600,2600.2\n",
    "columns": {"timestamp":"Time", "bid":"Bid", "ask":"Ask"}
  }]
}
```

This small JSON example is illustrative, not downloaded history. Column maps
translate normalized fields to exact export headers. `delimiter` supports a
single character, including tab. `timestamp_format` defaults to RFC3339 with
explicit offset; unix_s/ms/us and custom Go layouts with an explicit IANA
`timezone` are also supported. Ambiguous/nonexistent local DST timestamps are
rejected; export UTC or explicit offsets. UTC-aligned bar starts, finite positive
prices, valid OHLC ranges, noncrossed quotes and ordered rows are required.
Simultaneous quotes remain distinct. Import is bounded to 16 MiB of CSV and one
million rows; partition larger exports and include indicator warmup per study.

Returned `inputs` can be passed to `strategy_backtest_create` or `rule-backtest`.
The dataset includes raw CSV SHA-256 identities, normalized input identity,
source metadata and gap counts. Gaps are reported without inventing missing
bars; a gap may represent a legitimate market closure. Matching a broker also
requires its chart price basis, instrument multiplier, lot constraints, fees,
session timezone and an explicit account-currency conversion assumption.
Contract configuration is independent of the data import. Current simulation
uses fixed FX and excludes overnight financing and margin liquidation.

```sh
GOWORK=off go run ./cmd/market-import --spec export-import.json --output dataset.json
GOWORK=off go run ./cmd/rule-backtest --example rule_examples/donchian.json \
  --input dataset.json --config broker-assumptions.json --output result.json
```

Potential sources are [Dukascopy's free historical exports](https://www.dukascopy.com/swiss/english/marketwatch/historical/)
and a broker's own MT5/API exports. Dukascopy lists Germany 40 as DEU.IDX/EUR;
broker tickers and products can differ. This release does not include an
authenticated broker-history connector or an automated Dukascopy downloader.
The pre-release public download probes for XAUUSD and DEUIDXEUR returned HTTP
429; no original-instrument profitability results are claimed. MT5 exports may
need separate date/time columns combined before import and mapped CSV headers.
No fallback converts equity ETFs, futures or a cash index into CFD quotes.

### Sampled crypto candle study

`cmd/rule-research` runs the same evaluator and simulator against Binance-format
minute CSV archives, with preceding hourly candles for warmup. It changes only
the example instrument and uses an explicit 100000-account-currency balance,
100 planned stop risk, 1× exposure cap and configurable execution costs:

```sh
GOWORK=off go run ./cmd/rule-research \
  --example rule_examples/donchian.json --symbol BTCUSDT \
  --csv /absolute/path/to/2025-minute-candles.csv \
  --warmup /absolute/path/to/preceding-hourly-candles.csv \
  --start 2025-01-01T00:00:00Z --end 2026-01-01T00:00:00Z \
  --fee-bps 5 --slippage-bps 1 --spread-bps 2 \
  --output /absolute/path/to/research-result.json
```

This is an exploratory **sampled execution model**: minute-open trade prices
serve as execution proxies, without measured historical bid/ask quotes. The
simulator does not infer a path through each candle's high/low, so intraminute
stop, target and trailing events can be missed. Funding is excluded. The final
observed close proxy liquidates remaining positions with costs. Warmup does not
trade. Session examples retain the original clock windows and timezone when
adapted to crypto; they do not become DE40 reproductions.

Results retain CSV identities, adapted definitions, cost assumptions, complete
trade accounting, event hashes, an order audit and daily equity samples. Source
archives and publisher checksums should accompany research results. Monetary
results must be interpreted with the execution limitations; they are not
tick-accurate historical results or evidence of future profitability.
