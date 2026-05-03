import { apiGet, apiPost } from "./client.ts";
import type { Portfolio, Position, Order, JournalEntry } from "./types.ts";

export const listPortfolios   = ()                    => apiGet<{ portfolios: Portfolio[] }>("/portfolios").then(r => r.portfolios);
export const getPortfolio     = (id: number)          => apiGet<{ portfolio: Portfolio }>(`/portfolios/${id}`).then(r => r.portfolio);
export const listPositions    = (id: number)          => apiGet<{ positions: Position[] }>(`/portfolios/${id}/positions`).then(r => r.positions);
export const listOrders       = (id: number, status = "all", limit = 50) =>
  apiGet<{ orders: Order[] }>(`/portfolios/${id}/orders`, { status, limit }).then(r => r.orders);
export const readJournal      = (id: number, opts: { kind?: string; limit?: number } = {}) =>
  apiGet<{ entries: JournalEntry[] }>(`/portfolios/${id}/journal`, opts).then(r => r.entries);

export type PlaceOrderArgs = {
  symbol: string;
  side: "buy" | "sell" | "yes" | "no";
  type: "market" | "limit" | "stop";
  qty: number;
  limit_price?: number;
  stop_price?: number;
  tif?: "day" | "gtc" | "ioc";
  rationale: string;
};

export type PlaceOrderResult =
  | { order_id: string; status: "working" | "filled" }
  | { status: "rejected"; code: string; detail: string };

export const placeOrder = (portfolioId: number, args: PlaceOrderArgs) =>
  apiPost<PlaceOrderResult>(`/portfolios/${portfolioId}/orders`, args);
