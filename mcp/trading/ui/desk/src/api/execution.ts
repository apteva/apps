import { apiGet, apiPut } from "./client.ts";

export type VenueProfile = {
  venue_slug: string; asset_class: string; symbol: string;
  status: "active" | "degraded" | "maintenance" | "outage";
  calendar: string; session_policy: "continuous" | "regular_only" | "venue_managed";
  maker_fee_bps: number; taker_fee_bps: number; fee_currency: string;
  spread_model: "quote" | "fixed_bps" | "none"; fallback_spread_bps: number;
  slippage_model: "fixed_bps" | "none"; slippage_bps: number;
  min_qty: number; min_notional: number; qty_step: number; price_tick: number;
  funding_rate_bps: number; funding_interval_hours: number;
  runtime?: { status: string; consecutive_failures: number; last_error?: string; retry_at?: string };
};

export type ExecutionCost = {
  id: number; portfolio_id: number; order_id?: string; fill_id?: number;
  venue_slug: string; symbol: string; kind: "fee" | "spread" | "slippage" | "funding" | "rebate";
  amount: number; currency: string; rate_bps?: number; liquidity_role?: string;
  provider_event_id?: string; occurred_at: string;
};

export const getVenueProfiles = () => apiGet<{profiles: VenueProfile[]}>("/execution/venues").then(r => r.profiles ?? []);
export const updateVenueProfile = (profile: VenueProfile) => apiPut<{profile: VenueProfile}>("/execution/venues", profile);
export const getExecutionCosts = (portfolioId: number) => apiGet<{costs: ExecutionCost[]; totals: Record<string, number>}>(`/portfolios/${portfolioId}/execution-costs`, {limit: 200});
