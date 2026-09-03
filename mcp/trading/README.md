# Trading

Multi-portfolio, multi-asset trading desk for Apteva agents, covering
equity, ETF, crypto, and Polymarket prediction markets. It supports paper
and broker-backed live execution, deterministic strategies, and backtests.
Version 0.6 adds a provider-neutral security master, revisioned corporate-action
ledger, authoritative exchange sessions, forward point-in-time universe
snapshots, auditable portfolio postings, and an event-driven data-quality desk.

Same canonical layout as `apps/mcp/crm` and `apps/mcp/storage`: a Go
sidecar serving MCP tools + REST routes, with two UI surfaces under
`ui/` — a small dashboard panel and a rich trader-terminal SPA.

## Layout

```
apps/mcp/trading/
├── apteva.yaml             # manifest — kind: source, declares 34 mcp_tools
├── go.mod / go.sum
├── main.go                 # App impl, HTTP routes, Workers wiring
├── tools.go                # MCP tools (the agent's surface)
├── store.go                # DB layer
├── exec.go                 # Paper-execution + alert engine (Workers)
├── pricing.go              # Explicit offline mock provider
├── pricing_live.go         # Live market-data router
├── handlers_test.go        # Tier 1 — in-process handler tests
├── manifest_test.go        # Cross-check declared tools vs handlers
├── integration_test.go     # Tier 2 — //go:build integration; spawned-binary
├── migrations/001_init.sql # portfolios, positions, orders, fills, journal, marks, watchlist, alerts, day_baselines
├── prompts/risk_rules.md   # appended to each instance directive at boot
├── scenarios/              # Tier 3 — live-agent YAML scenarios
│   ├── 01-create-portfolio.yaml
│   ├── 02-place-and-fill.yaml
│   ├── 03-cap-rebalance.yaml
│   └── 04-poly-fade.yaml
└── ui/
    ├── panel/              # the dashboard widget — vanilla HTML+JS
    │   ├── TradingPanel.html / .css / .js
    └── desk/               # the rich trader terminal — React + Tailwind 4
        ├── package.json · build.ts · tsconfig.json
        ├── src/
        │   ├── api/        # client, types, portfolios, markets
        │   ├── hooks/      # usePortfolios, usePortfolio, useUniverse, useFetch
        │   ├── lib/        # format, spark, agentIcon
        │   ├── components/ # AgentIcon, Header, PortfolioSidebar, ...
        │   ├── App.tsx · main.tsx · index.css
        └── dist/           # committed build
```

## Build + run locally

```bash
# Build the sidecar binary.
go build .

# Run with mock pricing, fast tick.
APTEVA_APP_PORT=8080 APTEVA_PROJECT_ID=demo APTEVA_APP_TOKEN=dev \
APTEVA_APP_CONFIG='{"starting_cash":"100000","tick_seconds":"3","pricing_provider":"mock"}' \
DB_PATH=/tmp/trading-data/trading.db \
./trading

# In another shell:
curl -s http://127.0.0.1:8080/health
curl -s -X POST http://127.0.0.1:8080/portfolios \
  -H "Content-Type: application/json" \
  -d '{"name":"Demo","mandate":"crypto trend","allowed_classes":["crypto"],"starting_cash":50000}'
curl -s -X POST http://127.0.0.1:8080/portfolios/1/orders \
  -H "Content-Type: application/json" \
  -d '{"symbol":"BTC-USD","side":"buy","type":"market","qty":0.05,
       "rationale":"starter position via REST — testing the order placement path."}'
# Wait for tick…
curl -s "http://127.0.0.1:8080/portfolios/1/orders?status=all"
```

## REST surface

Mounted under `/api/apps/trading/*` when the sidecar is reverse-proxied
by apteva-server. Bare paths shown here.

| Method | Path | Purpose |
|---|---|---|
| `GET`  | `/health` | Liveness — auto-mounted by the SDK |
| `GET`  | `/portfolios` | List (project-scoped) |
| `POST` | `/portfolios` | Create |
| `GET`  | `/portfolios/{id}` | Snapshot |
| `PATCH`| `/portfolios/{id}` | Status only — `{ "status": "active\|paused\|halted" }` |
| `GET`  | `/portfolios/{id}/positions` | Open positions, mark-decorated |
| `GET`  | `/portfolios/{id}/orders?status=…&limit=…` | Working / filled / cancelled / rejected / all |
| `POST` | `/portfolios/{id}/orders` | Place order — same body as MCP `order_place` |
| `GET`  | `/portfolios/{id}/journal?kind=…&limit=…` | Read journal |
| `GET`  | `/quotes/{symbol}` | Latest mark |
| `GET`  | `/universe` | All currently-known marks |
| `GET`  | `/healthz/details` | Engine, stream, provider, and reference-data health |
| `GET`  | `/reference/status` | Coverage counts, checkpoints, and survivorship status |
| `GET`  | `/reference/securities?q=…&as_of=…` | Stable security and dated listing identities |
| `GET`  | `/reference/corporate-actions` | Latest normalized corporate-action revisions |
| `GET`  | `/reference/sessions` | Authoritative normalized exchange sessions |
| `GET`  | `/reference/quality` | Open ingestion, identity, and accounting issues |
| `GET`  | `/reference/postings?portfolio_id=…` | Auditable broker-observed and simulated action effects |
| `POST` | `/reference/sync` | Trigger an immediate idempotent reference-data reconciliation |

## Reference-data behavior

Alpaca Market Data supplies the broad corporate-action feed and live SSE
mutations. Alpaca Trading supplies assets, sessions, and account activities.
Unbound integrations degrade visibly through `reference_data_status`; they do
not fabricate reference data. Current Alpaca asset snapshots establish
survivorship coverage from the first successful sync forward. Historical runs
before that watermark are explicitly marked `survivorship_safe: false`.

Paper portfolios automatically apply deterministic splits, cash distributions,
symbol changes, and worthless removals exactly once. Multi-leg actions remain
visible for broker reconciliation instead of being guessed from incomplete
terms. Broker-backed portfolios treat broker positions and activities as the
accounting source of truth.

## MCP tools (34)

**Portfolio and execution (13):** `portfolio_create`, `brokers_list`,
`portfolio_list`, `portfolio_get`, `account_summary`, `positions_list`,
`orders_list`, `order_place`, `order_cancel`, `watchlist_add`,
`watchlist_remove`, `portfolio_pause`, `portfolio_arm_live`.

**Market and reference data (8):** `market_quote`, `market_history`,
`market_source`, `market_calendar`, `reference_data_status`,
`security_resolve`, `corporate_actions_list`, `exchange_sessions_list`.

**Strategy and backtesting (10):** `strategy_create`, `strategy_update`,
`strategy_get`, `strategy_list`, `strategy_validate`, `strategy_evaluate`,
`strategy_assign`, `strategy_backtest_create`, `strategy_validate_backtest`,
`backtest_market_step`.

**Alerts and journal (3):** `alert_create`, `journal_write`, `journal_read`.

`order_place` always takes a `rationale` (≥ 30 chars). On reject, the
status field is `"rejected"` with a structured `code` + `detail` —
**not** an MCP error. The agent reads it on its next loop and
adjusts.

## Test pyramid

| Tier | What | How | Speed |
|---|---|---|---|
| **1** in-process | Every MCP handler exercised against in-memory SQLite | `go test ./...` | < 0.1s |
| **2** real binary | Spawned sidecar talked to via JSON-RPC + REST; engine ticks for real | `go test -tags integration ./...` | ~8s |
| **3** live agent | YAML scenarios run by `apteva test ./scenarios/` — real agent, real LLM | `apteva test ./scenarios/` | tens of seconds + LLM cost |

## Pricing provider

The default `pricing_provider: live` routes crypto to Binance public REST,
prediction markets to Polymarket Gamma, and equity/ETF data to Alpaca when it
is bound. Alpaca quotes, trades, minute bars, corrections, market status, and
broker order updates stream over WebSockets; REST snapshots remain the retry
and recovery path. Yahoo Finance is the unauthenticated display/backtest
fallback, but broker-backed equity orders require Alpaca marks by default.
The active feed (`sip`, `iex`, or `delayed_sip`) and stream health are exposed
to the dashboard. Set `pricing_provider: mock` for deterministic tests and
offline demos.

Portfolios use an explicit execution environment: `simulation`,
`broker_paper`, `broker_live`, or `backtest`. Broker-live portfolios start
disarmed and reject orders and automated strategy runs until an operator arms
them with the exact `LIVE MONEY` confirmation. Strategy executions claim each
closed signal bar durably before order submission, preventing duplicate broker
orders after a restart. Backtests capture data source, adjustment, execution
model, row count, and a SHA-256 dataset identity alongside professional risk
metrics.

## Approvals — by design, not in the sidecar

If a portfolio's mandate calls for human sign-off above some notional,
the **agent** does the asking via its existing `channel-chat` (or
Slack / Telegram) channel. The trading sidecar never knows about
approvals, never expires a token, never reconciles state across
systems — that policy lives where it belongs, in the agent's directive
per portfolio. See `prompts/risk_rules.md` for the language that ships
with the app.
