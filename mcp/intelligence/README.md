# Intelligence

Intelligence ingests lawful public data, keeps it as source-attributed
evidence, builds a time-aware world model on top of it, records calibrated
forecasts on concrete questions before their outcomes are known, scores those
forecasts against prediction markets, and runs explicit "what if" scenarios.
Market data and trading signals remain inside the app as a domain pack.

Version 0.5.0 renamed the app from `market-intel` to `intelligence`.
Version 0.6.0 adds evaluation: reproducible backtests on frozen historical
data, kept apart from prospective results and a follow-up plan.

## Three layers, kept apart

| Layer | What it holds | Tables |
|---|---|---|
| Observed | What a source said, and when the app learned it | `intel_sources`, `intel_ingest_runs`, `intelligence_evidence`, `intel_claims` |
| Estimated | The app's current model of the world | `entities`, `intel_relationships`, `intel_state_values` |
| Futures | Questions, market prices, forecasts, outcomes, scenarios | `intel_questions`, `intel_question_prices`, `intel_forecasts`, `intel_scenarios` |

Each evidence row has two clocks: when the event was observed (`observed_at`)
and when the app ingested it (`ingested_at`). Forecasts and replays use the
ingested clock, so a forecast can never see evidence that arrived after it
was made. Duplicate content returns the existing row and never overwrites it.

## Connectors

The `intelligence_cycle` worker runs due connectors every five minutes, then
claim extraction and forecast updates.

- **prediction-markets** (15 min): Polymarket, Kalshi and Manifold binary
  markets closing in 12h to 10 days, filtered by volume. New questions must be
  priced between 0.03 and 0.97. They are capped per topic, per venue event and
  per question template, so a ladder or a repeated sports prop cannot flood the
  set. Generic market titles get their event name added in front. Markets about
  private lives or individual wallets are excluded. Open questions are
  re-priced every run and resolved from the venue's final outcome.
- **news** (30 min): one batched GDELT query covers up to eight questions.
  GDELT allows one request every five seconds per IP and slows down clients
  that exceed it, so failures are reported and retried on the next run.
  Connected news integrations (NewsAPI, GNews, Tavily) are queried for each
  question.
- **bls**: CPI, unemployment and payrolls become indicator entities with
  state values. **fred** is off by default and needs the FRED integration.

Use `sources_list`, `source_configure` and `ingest_now` to inspect and tune
them.

## World model

- Entities have a kind. There is no `person` kind: people appear only as
  `public_role` entities. Question text is used only for search terms and
  never becomes an entity.
- Relationships need evidence or claim ids when their status is `observed` or
  `reported`. Otherwise they are `inferred` or `assumed`.
- The LLM extractor (through the `llm` app; config `llm_model`, `none`
  disables it) turns headlines into claims and reported relationships. Each
  one cites its evidence.

## Forecasts and the ledger

Forecast model v1, "market-prior + evidence-shift":

- **Prior:** the market price.
- **Shift:** the LLM proposes a direction and a strength from 0 to 3. It must
  cite evidence ids from the frozen evidence set. Each point of strength
  shifts the forecast 0.2 log-odds, up to 0.6 in total.
- **Abstain:** with no market price and no evidence, the forecast abstains.
- **Revisions:** a forecast is first made when a question is created. It is
  revised when new evidence is linked or the market moves 0.05 or more.
- **Replay:** each forecast stores a snapshot hash. `replay` with
  `forecast_id` re-checks it.

For each question, the `ledger` scores one forecast: the latest one made at
least 24h before the deadline. If none qualifies, it scores the latest one
made before resolution. It reports:

- Brier score and log loss, alongside the market and a 0.5 baseline;
- skill against the market;
- calibration;
- coverage;
- breakdowns by horizon and by venue.

## Evaluation

Evaluation asks one question: on specific resolvable YES/NO questions, do the
statistical and AI-assisted methods beat simple baselines? It keeps
historical backtests and live (prospective) evidence apart and never pools
them.

**Dataset.** `evaluation_dataset_build` freezes resolved binary questions
from Manifold and Kalshi, with each question's trade-by-trade price history.
Questions are filtered for liquidity (traders, trades), lifetime and at most
two per linked event. The dataset gets a content hash; every run checks it
before scoring. Skip reasons are counted.

**Methods,** at each horizon h (default 24h and 168h before the earlier of
close and resolution):

- `uniform`: 0.5.
- `base_rate`: the YES rate (Laplace-smoothed) of questions resolved before
  the forecast time.
- `market`: the last trade at or before the forecast time.
- `market_recal`: the market price recalibrated by a logistic fit on
  questions resolved before the forecast time, shrunk toward the identity.
- `ai_shift` (optional): the live forecast model, market prior plus the
  capped LLM evidence shift, on GDELT news first seen before the forecast
  time.

Trained methods abstain until 30 outcomes are known.

**Leakage controls.**

- Prices come from trade timestamps, never from final or later prices.
- Questions created after the forecast time are excluded.
- Training uses only outcomes known strictly before each forecast time.
- AI evidence must have a first-seen time before the forecast time; undated
  articles are dropped.
- AI forecasts are scored only on questions resolving after the model's
  stated knowledge cutoff (`ai_knowledge_cutoff`, required). That cutoff is
  stated by the operator and not verified, and the report says so.

**Report.** `evaluation_run` scores, for each method:

- Brier score and log loss with cluster-bootstrap 95% intervals (linked
  questions resample together);
- calibration bins and ECE;
- paired differences from the market;
- results by chronological block;
- failure and abstention counts with reasons;
- a stated set of limitations.

A verdict needs at least 30 scored questions and an interval that excludes
zero; otherwise the report says "insufficient sample" or "no detectable
difference". Runs record their parameters, seed and code version. The
statistical methods are deterministic.

**Prospective results and follow-up.** `evaluation_report` adds two sections,
computed when read:

- `prospective_live`: the live ledger's forecasts, each recorded before its
  outcome, scored model against market with intervals.
- `follow_up`: every open question with its scoring date (48h after its
  deadline), and the earliest dates when the prospective sample can reach 30
  and 100.

Observation periods are not shortened. Unresolved questions stay in the
plan until they resolve.

Evaluation produces scores, never trades or decisions about individuals.

## Scenarios

`scenario_run` applies the shocks you state to a chosen time. They propagate
through typed relationships using explicit rules (factor and direction). Each
link is weighted by its evidence status. The output is labelled
`hypothetical`, shows every assumption chain, and lists the open questions
exposed to the shocks. Links with no rule are reported, not guessed.

## Scope

The app is for decision support from lawful data only. It does not build
profiles of private individuals or make consequential decisions about them.
It does not do covert surveillance or manipulation. It does not trade
autonomously.

## Evidence tools

- `evidence_record`: record a document, claim, event, measurement, or other
  source observation with event, publication, validity, and observation times.
- `search`: search evidence by text, source, entity, kind, event/publication
  range, or `as_of` cutoff.
- `timeline`: return evidence in chronological order for a topic or entity.
- `replay`: produce a deterministic snapshot hash and records visible at an
  `as_of` cutoff. This is the safe input boundary for analysis and backtests.

All evidence rows carry `event_time`, `published_time`, `observed_at`,
`valid_from`, and `valid_to` where available. `as_of` applies the observed and
valid-time constraints together, preventing future information from leaking
into historical analysis.

## Signal pipeline

Every five minutes the worker:

1. requests the most liquid instruments from each configured provider;
2. loads closed candles only and computes reproducible features;
3. evaluates trend/momentum, mean-reversion, and volume-breakout strategies;
4. rejects stale quotes, thin markets, wide spreads, weak confidence, and
   opportunities whose expected move does not clear fees and slippage;
5. stores accepted signals with entry, stop, targets, expiry, rationale,
   features, and complete provenance;
6. emits `signal.created`; and
7. evaluates matured signals to build an immutable live performance record.

The public `binance-public` provider uses Binance spot REST without credentials.
No synthetic prices are used. `signal_backtest` performs a walk-forward replay
over closed candles with conservative same-candle stop/target handling.

## Paid provider contract

Signal logic depends on `MarketDataProvider`, not Binance. A paid integration
adapter must expose a universe tool and a bars tool returning these normalized
shapes:

```json
{"instruments":[{"symbol":"BTCUSDT","canonical_symbol":"BTC-USD","asset_class":"crypto","bid":100,"ask":100.1,"last":100.05,"quote_volume_24h":50000000,"quote_time":1760000000}]}
```

```json
{"bars":[{"time":1760000000,"open":99,"high":101,"low":98,"close":100,"volume":1200,"closed":true}]}
```

Configure `paid_provider_slug`, `paid_provider_venue`,
`paid_provider_universe_tool`, and `paid_provider_bars_tool`. Credentials remain
inside the bound Apteva integration. A provider that omits `closed=true` cannot
produce a signal.

## Productization

`signal_feeds` separates the computation from the product: each feed has an
asset class, interval, quality gates, visibility (`private`, `delayed`, or
`premium`), and delay. `signals_list` exposes stable public IDs and
`signal_metrics` exposes measured results. Billing/entitlement enforcement can
therefore be added at the feed boundary without changing signal generation.

Backtests are evidence, not a promise. Before charging subscribers, collect a
meaningful live evaluated sample, calibrate confidence against observed hit
rates, document venue/fee assumptions, and have the distribution terms reviewed
for the jurisdictions where the feed will be sold.

## Verification

```sh
GOWORK=off go test ./...
RUN_MARKET_INTEL_LIVE=1 GOWORK=off go test -run 'Test(BinanceProviderLive|SignalPipelineLive)$' -v
```
