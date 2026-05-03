import { apiGet } from "./client.ts";
import type { Sym } from "./types.ts";
import { spark } from "../lib/spark.ts";

// The sidecar's /universe returns raw marks (price + asset_class +
// prev_close + ...). We decorate each one with display fields the UI
// needs but the API doesn't ship: name, change_pct/change_abs, spark.

const NAMES: Record<string, string> = {
  AAPL:      "Apple Inc.",
  NVDA:      "NVIDIA",
  MSFT:      "Microsoft",
  TSLA:      "Tesla",
  GOOGL:     "Alphabet Class A",
  META:      "Meta Platforms",
  SPY:       "S&P 500 ETF",
  "BTC-USD": "Bitcoin",
  "ETH-USD": "Ethereum",
  "SOL-USD": "Solana",
  "AVAX-USD":"Avalanche",
  "DOGE-USD":"Dogecoin",
  "POLY:fed-cut-march":     "Will the Fed cut rates 50bps at the March 2026 FOMC?",
  "POLY:recession-2026":    "Will the US enter a recession in 2026?",
  "POLY:btc-100k-2026":     "Will Bitcoin close above $100,000 by end of 2026?",
  "POLY:trump-approval-50": "Trump approval > 50% in any major poll in Q2 2026?",
  "POLY:openai-ipo-2026":   "Will OpenAI announce an IPO before Dec 2026?",
  "POLY:gpt5-2026":         "Will OpenAI release GPT-5 in 2026?",
};

type RawSym = Omit<Sym, "name" | "change_pct" | "change_abs" | "spark">;

export async function getUniverse(): Promise<Sym[]> {
  const r = await apiGet<{ symbols: RawSym[] }>("/universe");
  return (r.symbols || []).map(decorate);
}

export async function getQuote(symbol: string): Promise<Sym> {
  const r = await apiGet<RawSym>(`/quotes/${encodeURIComponent(symbol)}`);
  return decorate(r);
}

function decorate(s: RawSym): Sym {
  const prev = s.prev_close ?? s.price;
  const change_abs = s.price - prev;
  const change_pct = prev > 0 ? (change_abs / prev) * 100 : 0;
  return {
    ...s,
    name: NAMES[s.symbol] ?? s.symbol,
    change_abs,
    change_pct,
    spark: spark(s.symbol, change_pct >= 0 ? 1 : -1),
  };
}
