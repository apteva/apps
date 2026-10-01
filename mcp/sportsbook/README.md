# Sportsbook 0.1

A dedicated Apteva sports research and **paper betting** app. Go/app-sdk sidecar, project-scoped SQLite, native React dashboard panel following the CRM layout, and an isolated panel preview. It does not submit real wagers.

## What works

- Project-owned sports and competitions, with names, enabled status and provider key mappings editable in the native **Sports** tab or through MCP tools. Football and tennis are initial defaults, not an allowlist.
- Configurable two-way or three-way pre-match match-winner markets, regulation/completed-match rules, baseline/Elo/disabled predictions, history scope and home advantage. Imported markets freeze their configuration.
- Multiple bound providers per integration role, platform defaults and sport-specific routing overrides.
- Bookmaker comparisons, timestamped odds history, and explicit 15-minute quote expiry.
- Immutable experimental Elo predictions using imported completed results; insufficient history produces a clearly labeled, margin-adjusted bookmaker baseline.
- Optional LLM explanations through OpenAI or Anthropic connections. The model cannot modify numerical predictions, stakes or accounting.
- Single-bet proposals, expected value, integer monetary stakes, paper bankrolls, per-bet and total-exposure limits.
- Atomic paper acceptance, stake reservation, manual win/loss/void settlement and balanced double-entry ledger transactions.
- Idempotent acceptance and settlement; audit records; isolated example fixtures and bankrolls.
- Four native panel tabs: Events, Predictions, Bets, Integrations. The panel uses the same compact list/detail layout, Tailwind theme tokens and shared React ESM contract as CRM; it supports narrow screens.

## Integration roles

All four roles declare `mode: multiple`. Connections are selected in the platform's app integration settings. Sportsbook never stores credentials or discovers unrelated connections.

| Role | Available adapters | Normalized capability |
| --- | --- | --- |
| `sports_data` | TheSportsDB, The Odds API, API-Sports Football, API Tennis | Import a UTC date's fixtures and completed results |
| `odds` | The Odds API, API Tennis | Complete match-winner bookmaker markets |
| `llm` | OpenAI API, Anthropic API | Explain the supplied prediction evidence using an explicitly selected model |
| `execution` | Optional app binding for `sportsbook-executor` | Extension point; live execution is disabled in 0.2 |

Paper execution is built in. The execution role uses the SDK's app-binding path because an eventual executor owns the commercial venue's ticket/reconciliation contract. Binding an app does not enable live submission: `bet_submit` consistently returns `live_execution_unavailable` without a provider call.

`IntegrationFor(role)` supplies the platform default; `IntegrationsFor(role)` supplies all selected targets. Sportsbook validates adapter coverage itself. `provider_routes` can override the default by sport. Sports imports use one compatible provider; `all_sources=true` fans odds imports out to all selected providers that support the requested sport. An explicit `connection_id` must belong to the requested role. Removed routes fail clearly rather than silently selecting another account.

A provider may be selected in more than one role, for example API Tennis for fixtures and odds. Routing is independent in each role. An all-sports preference applies only within that provider's coverage; other sports use a compatible default. Provider-specific tool names and response shapes are confined to `providers.go` and `feeds.go`.

The Odds API uses saved sport or competition mappings such as `basketball_nba`, `soccer_epl` or an active tennis key. A sport-level football default of `soccer_epl` is seeded for compatibility. TheSportsDB uses mapped sport names (such as `Basketball`); API-Sports Football and API Tennis remain fixed-sport adapters, with optional numeric league/tournament mappings. The Odds API also imports upcoming events and up to three days of completed scores through the sports data role. Its final totals never train regulation-only markets. Odds are filtered to the requested UTC event date. API Tennis prices have no provider update timestamp in this adapter, so their observation time is the successful retrieval time. Account coverage, quotas and endpoint access depend on the provider subscription. Provider contracts and the Basketball mapping → import → baseline → proposal → acceptance → settlement flow are tested with fixtures; paid accounts have not been used for live verification.

## Predictions and research

Elo v2 uses an initial rating of 1500, K=24, and at least five completed observations for each participant. Each market snapshots its history scope and home advantage; three-way profiles add a smoothed historical draw rate. Default football settings retain competition scope and a 60-point home advantage; default tennis uses sport scope and two-way Elo without surface adjustments. New sports default to bookmaker baselines in the UI; Elo requires explicit selection and remains experimental. At most the latest 5,000 completed results available at prediction time are replayed in chronological order.

Every prediction stores its model identifier, feature values, actual training observations or baseline quotes, creation time, and expiry. Results whose event time or receipt time is in the future are excluded. UI and LLM reads receive compact feature summaries; full snapshots remain in the database. These estimates are **experimental and uncalibrated**; no accuracy, profitability or independent edge is claimed.

Bookmaker baselines normalize implied probabilities within a complete bookmaker market, deduplicate bookmakers across sources, and normalize the median probabilities. Two-outcome football prices are rejected because they may represent draw-no-bet or another ruleset. Estimated return per unit stake for supported win/lose markets is `probability × decimal_odds − 1`.

Provider event IDs are mapped through `event_aliases`. A cross-source event can share an identity only when normalized participant names, sport, canonical competition and start time agree and there is exactly one candidate. Ambiguous or differing names remain separate. Full entity registries, operator-specific tennis retirement rules, totals, handicaps, props, accumulators, exchange lay bets and in-play execution are later work.

## Financial guarantees

- Amounts use integer minor units; odds use integer millionths. Returns use integer arithmetic with half-up rounding.
- Paper bankrolls support EUR, USD and GBP. Default stake cap is 2% and open exposure cap is 10% of current paper equity.
- A proposal binds to an immutable prediction and exact quote. Acceptance rechecks the active quote snapshot, freshness, event status, expiry, funds and limits in one database transaction.
- A successful complete odds snapshot removes omitted quotes from current availability while retaining their history. Older timestamps are not accepted as fresh quotes.
- Repeating acceptance for a proposal returns its existing bet. Repeating the same settlement returns the existing result. Changing a settled result is rejected in this release.
- The ledger moves cash into a locked account on acceptance; settlement releases the stake and books its return and P/L. Each transaction must balance to zero.
- Manual paper settlement needs a note. It is a simulation result, not a provider-certified ticket settlement.

## Authorization

The manifest declares separate `sportsbook.read`, `configure`, `sync`, `propose`, `paper_execute`, `paper_settle`, and reserved `live_execute` permissions. MCP handlers enforce authenticated caller grants and project scope; missing caller identity fails closed. HTTP RPC is an operator surface behind the SDK's app-token auth and platform proxy. Agent and bound-app identity headers are rejected there so they cannot bypass MCP grants. Signed end-user principals, when supplied by the platform, are verified against the installation token.

Model-supplied project IDs cannot change the installation's scope. Example data is opt-in and is never mixed with connected-data bankrolls. All provider requests use platform-bound connections.

## Database

`migrations/001_init.sql` creates:

- Research: `events`, `event_aliases`, `markets`, `odds_observations`, `quote_heads`.
- Models: `predictions`, `explanations`.
- Providers: `provider_routes` (connection references; no secrets).
- Betting: `bankrolls`, `proposals`, `bets`, `ledger_entries`, `audit_events`.

Project-scoped keys and foreign keys prevent cross-project relationships. Writes use the SDK's serialized SQLite pool. Refreshes are explicit in 0.1 to control provider quotas. Continuous feeds, durable ingestion jobs, provider health history, automatic settlement, model calibration/backtesting and PostgreSQL support are not implemented yet.

## Configure another sport

1. Open **Sports**, choose **+ New sport**, enter a stable ID and name, and choose the supported match-winner outcome/rule/model profile. **Save sport** writes the sport and market configuration atomically.
2. Add competitions with stable IDs. Each belongs to one sport. Imports also register unmapped competition names; configure explicit mappings to unify different provider names.
3. Add provider mappings per role and sport/competition. For example, Basketball → NBA → The Odds API → `basketball_nba`. **Discover sports** lists provider keys from a bound account without creating or enabling sports automatically.
4. Bind provider connections in platform settings. Configure role defaults in **Integrations**, then select a sport, competition and UTC date to import. Multiple accounts for a provider share that provider's key mappings; role routing selects the account.

Catalog tables are `sports`, `competitions`, `provider_sport_mappings` and `sport_market_types`. `catalog_get`, `sport_upsert`, `competition_upsert`, `provider_sport_mapping_set` and `sport_market_set` expose configuration to agents; `provider_sports_list` discovers The Odds API coverage. All mutations require `sportsbook.configure`; discovery requires `sportsbook.sync`.

Disabling sports, competitions or market definitions blocks new imports, predictions, proposals and acceptance while allowing settlement of existing bets. Catalog changes do not rewrite historical predictions, market rules, bet terms or ledger entries. Migration 002 preserves v0.1 records, backfills competition identities and snapshots legacy football/tennis market settings.

A catalog entry is not a promise of live coverage. Generic adapters support additional two-participant sports according to provider coverage; fixed-sport adapters cannot be relabeled as arbitrary sports. Racing fields, totals, spreads, outrights, props and custom outcome/settlement engines still require new adapters. Account entitlements and provider-specific settlement conventions must match the configured market. Automatic settlement and real wagering remain unavailable.

## Run the local preview

From this directory:

```sh
bun install --cwd ui
bun run --cwd ui dev
```

Open `http://127.0.0.1:8079/ui/index.html`. Choose **Examples** in the workspace selector, then **Load examples**. Select an event, run a prediction, choose a price, add a rationale and create a proposal. Review it in **Bets**, accept it, then record a paper settlement.

The preview loads the actual `SportsbookPanel.mjs` through a shared React import map, uses a preview-only copy of the ui-kit token stylesheet, builds the Go binary, creates an isolated temporary database and random local app token, and exposes a loopback-only app proxy. It does not read existing Apteva API keys or connect provider accounts. Ports can be changed with `SPORTSBOOK_BACKEND_PORT` and `SPORTSBOOK_PREVIEW_PORT`. Ctrl-C stops both processes.

For platform installation, use `apteva.yaml` as a source app manifest. The SDK serves `ui/` and applies migrations; build the UI before packaging. The app is project-scoped. The module is pinned to the published app-sdk `v0.90.0`. Release `sportsbook/v0.2.0` pins the source manifest and marketplace entry to this version. Use `GOWORK=off` to verify the published SDK dependency independently of any local workspace overlay.

## Verify

```sh
env GOWORK=off go test -race ./...
env GOWORK=off go vet ./...
env GOWORK=off go build ./...
bun run --cwd ui typecheck
bun run --cwd ui build
```

Tests cover real SQLite transactions, acceptance races, duplicate settlement, risk caps, quote expiry and replacement, project/example isolation, permission gates, signed identities, provider response contracts, provider routing and LLM immutability. Browser verification covers the full example prediction/proposal/acceptance/winning-settlement flow.

## Layout

- `main.go`: manifest, SDK lifecycle, authenticated HTTP and MCP dispatch.
- `catalog.go`: project catalog, atomic configuration, provider mappings and enabled-entry gates.
- `store.go`: queries, money inputs, ledger primitives and tool schemas.
- `providers.go`, `feeds.go`: bindings, routing, provider calls and normalization.
- `predictions.go`: versioned Elo and bookmaker baseline snapshots.
- `betting.go`: proposal, reservation, risk and settlement transactions.
- `demo.go`: explicit fictional fixtures and history.
- `ui/SportsbookPanel.tsx`: native dashboard panel; default export with the same app/project/install props as CRM.
- `ui/preview.tsx`: local ESM panel-mount harness; no separate product shell.
- `ui/dev.ts`: isolated local preview launcher.
