// Types mirror the JSON shapes the trading sidecar emits. Keep them
// hand-maintained against store.go — the same sin every typed-front-end
// commits, paid for by editor autocomplete and one place to add fields.

export type AssetClass = "equity" | "etf" | "crypto" | "polymarket";

export type Portfolio = {
  id: number;
  name: string;
  agent_id?: string;
  mandate: string;
  allowed_classes: AssetClass[];
  status: "active" | "paused" | "halted";
  mode: "paper" | "live";
  equity: number;
  cash: number;
  buying_power: number;
  day_pnl: number;
  day_pnl_pct: number;
  open_pnl: number;
  open_pnl_pct: number;
  watchlist?: string[];
};

export type Position = {
  symbol: string;
  asset_class: AssetClass;
  outcome?: "YES" | "NO";
  qty: number;
  avg_cost: number;
  market_price: number;
  market_value: number;
  unrealized_pnl: number;
  unrealized_pnl_pct: number;
  realized_pnl: number;
  weight_pct: number;
  // The API doesn't (yet) ship per-position day P&L; UI derives 0 for now.
  day_pnl: number;
};

export type Order = {
  id: string;
  portfolio_id: number;
  symbol: string;
  asset_class: AssetClass;
  side: "buy" | "sell" | "yes" | "no";
  type: "market" | "limit" | "stop";
  qty: number;
  filled_qty: number;
  avg_fill_price?: number;
  limit_price?: number;
  stop_price?: number;
  tif: "day" | "gtc" | "ioc";
  status: "working" | "filled" | "cancelled" | "rejected";
  rationale: string;
  source: string;
  rejection_code?: string;
  rejection_detail?: string;
  placed_at: string;
  resolved_at?: string;
};

export type JournalEntry = {
  id: number;
  portfolio_id: number;
  kind: "thesis" | "alert" | "fill" | "rationale" | "rejection" | "note";
  body: string;
  metadata?: Record<string, unknown>;
  created_at: string;
};

// Symbol — what /universe and /quotes/:s return. Decorated with a
// client-side `spark` for the watchlist sparkline; that field is
// derived (not from the server).
export type Sym = {
  symbol: string;
  asset_class: AssetClass;
  price: number;
  prev_close?: number;
  no_price?: number;
  yes_price?: number;
  volume_24h?: number;
  marked_at: string;
  // Optional polymarket extras (not yet shipped by the v0.1 API).
  resolves_at?: number;       // unix ms
  consensus?: string;
  // Derived client-side:
  name: string;
  change_pct: number;
  change_abs: number;
  spark: number[];
};
