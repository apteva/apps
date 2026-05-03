import { apiGet } from "./client.ts";

export type ProviderClass = "crypto" | "polymarket" | "equity" | "etf";

export type ProviderHealth = {
  name: string;            // "binance-public" | "polymarket-public" | "mock"
  last_ok_at?: string;
  errors_60s: number;
  stale: boolean;
};

export type HealthDetails = {
  ticks: number;
  fills_this_run: number;
  last_tick_at?: string;
  last_marks_refreshed: number;
  providers: Partial<Record<ProviderClass, ProviderHealth>>;
};

export const getHealth = () => apiGet<HealthDetails>("/healthz/details");
