# Trading

Paper trading desk for Apteva agents. Multi-portfolio, multi-asset
(equity, crypto, polymarket prediction markets). Deterministic
paper-execution engine — no broker, no real money.

Same canonical layout as `apps/mcp/crm` and `apps/mcp/storage`: a Go
sidecar serving MCP tools + REST routes, with two UI surfaces under
`ui/` — a small dashboard panel and a rich trader-terminal SPA.

## Layout

```
apps/mcp/trading/
├── apteva.yaml             # manifest — kind: source, declares 15 mcp_tools
├── go.mod / go.sum
├── main.go                 # App impl, HTTP routes, Workers wiring
├── tools.go                # MCP tools (the agent's surface)
├── store.go                # DB layer
├── exec.go                 # Paper-execution + alert engine (Workers)
├── pricing.go              # Pricing provider — mock by default, swappable
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
APTEVA_APP_CONFIG='{"starting_cash":"100000","tick_seconds":"3"}' \
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
| `GET`  | `/healthz/details` | Engine debug — last_tick_at, fills_this_run |

## MCP tools (15)

**Reads (8):** `portfolio_list`, `portfolio_get`, `account_summary`,
`positions_list`, `orders_list`, `market_quote`, `market_history`,
`journal_read`.

**Writes (5):** `order_place`, `order_cancel`, `journal_write`,
`watchlist_add`, `watchlist_remove`.

**Governance (2):** `alert_create`, `portfolio_pause`.

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

Counts today: 14 Tier 1 handler tests + 2 manifest cross-checks · 4
Tier 2 integration tests · 4 Tier 3 scenarios.

## Pricing provider

Default `pricing_provider: mock` — deterministic walks anchored to the
hand-picked universe in `pricing.go`. Same RNG as the desk-UI mockup
so the visual story stays continuous. Swapping in a live provider
(yfinance, coingecko, polymarket-clob) is a matter of implementing
the `Provider` interface in another file and wiring it in `newProvider`
inside `main.go` — no other file needs to change.

## Approvals — by design, not in the sidecar

If a portfolio's mandate calls for human sign-off above some notional,
the **agent** does the asking via its existing `channel-chat` (or
Slack / Telegram) channel. The trading sidecar never knows about
approvals, never expires a token, never reconciles state across
systems — that policy lives where it belongs, in the agent's directive
per portfolio. See `prompts/risk_rules.md` for the language that ships
with the app.
