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
  execution_environment: "simulation" | "broker_paper" | "broker_live" | "backtest";
  live_armed: boolean;
  broker_slug?: string;
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
  security_id?: string;
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
  security_id?: string;
  asset_class: AssetClass;
  side: "buy" | "sell" | "yes" | "no";
  outcome?: "YES" | "NO";
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
  kind: "thesis" | "alert" | "fill" | "rationale" | "rejection" | "note" | "corporate_action";
  body: string;
  metadata?: Record<string, unknown>;
  created_at: string;
};

export type RiskPolicy = {
  portfolio_id: number; risk_level: "conservative" | "balanced" | "aggressive" | "custom";
  max_daily_loss_pct: number; max_drawdown_pct: number; max_position_pct: number;
  max_gross_exposure_pct: number; max_order_pct: number;
};
export type RiskState = { high_water_equity: number; current_drawdown_pct: number };
export type PortfolioObjective = {
  id: number; name: string; metric: string; target_pct: number; direction: "at_least" | "at_most";
  starts_at: string; deadline_at?: string; status: string; actual_pct?: number; progress_pct?: number;
  achieved: boolean; period_state: string;
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
  bid_price?: number;
  ask_price?: number;
  bid_size?: number;
  ask_size?: number;
  last_trade_price?: number;
  last_trade_size?: number;
  feed?: string;
  quote_at?: string;
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
