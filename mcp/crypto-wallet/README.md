# Crypto Wallet

On-chain wallets for the company's own funds, spent by agents under rules they
cannot change. v0.1: ETH and USDC on Base and Base Sepolia, keys held by the app.

Not for holding other people's money: doing that makes the operator a crypto
service provider (MiCA in the EU, money-transmitter rules in the US).

## Who can do what

| | agents (MCP tools) | people (panel) |
|---|---|---|
| list wallets, balances, receive address, history, pending approvals | `wallets.read` | yes |
| propose a transfer | `transfers.propose` | (approve only) |
| propose a recipient | `recipients.propose`, waits for approval + delay | add directly |
| approve or reject transfers and recipients | `approvals.decide` | yes |
| create wallet, change policy, freeze, export key | no tool | yes |

Each MCP tool requires one of the app's declared permissions, so which agent
may do what is set with the platform's grants (per install and agent; the
default effect is allow). A common setup: deny `approvals.decide` by default
and allow it for the one agent that acts for the treasurer. Decisions record
the user or agent that made them. Panel routes require the platform's operator
header and the app token.

When something needs approval the app publishes
`crypto_wallet.approval.requested`; subscribe to it to notify people. Nothing
waits on a reply.

## Policy

Checked in this order, inside one database transaction under a per-wallet lock,
before anything is signed: frozen, token list, recipient allowlist (and its
waiting period for agent-proposed addresses), per-transfer cap, rolling 24h cap,
pending-approval limit, approval threshold. Approved transfers are re-checked
at approval time, whoever approves. Defaults: $100 per transfer, $250 per 24h, approval above $25,
USDC only, 24h delay for agent-proposed recipients. Every policy change is a new
version; the ledger table is append-only (triggers abort UPDATE and DELETE).

USD values: USDC at $1, ETH from the Coinbase spot price (cached 1 minute;
transfers of ETH are refused when the price is unavailable).

## Keys

Each private key is sealed with its own AES-256-GCM data key, which is sealed
with the master key. The master key is the `master_key` secret in the app's
config (or `CRYPTO_WALLET_MASTER_KEY`), kept outside the app database, so a
database backup alone cannot spend funds. Generate it from the panel on first
run. Losing or changing it makes existing wallets unusable: export keys you care
about (panel, Key tab, type the address to confirm; the export is logged).

## Chain

Plain JSON-RPC (EIP-1559 type-2 transactions, secp256k1 via decred). The
`chain-watcher` worker runs every 20 seconds: it confirms sent transfers
(3 blocks on Base, 1 on Sepolia), marks dropped ones failed, and records
incoming USDC. Incoming ETH shows in the balance but is not itemized.

## Develop

```sh
go test ./...                                   # unit + policy tests, fake chain
RUN_CHAIN_TESTS=1 go test -run Chain ./...      # live Base / Base Sepolia checks, moves no funds
cd ../.. && bun run scripts/build-panels.ts --app crypto-wallet
```

Standalone: `APTEVA_APP_TOKEN=dev-token APTEVA_APP_PORT=18931 DB_PATH=./cw.db
APTEVA_MIGRATIONS_DIR=migrations APTEVA_UI_DIR=ui CRYPTO_WALLET_MASTER_KEY=<base64>
go run .`. Panel routes refuse requests while `APTEVA_APP_TOKEN` is empty unless
`CRYPTO_WALLET_DEV_INSECURE=1`.

## Not in v0.1

Other key providers (Coinbase CDP, Turnkey, Safe multisig), other chains and
tokens, recovery phrases, itemized incoming ETH.
