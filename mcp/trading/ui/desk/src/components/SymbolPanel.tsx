import type { Sym } from "../api/types.ts";
import { PriceChart } from "./PriceChart.tsx";
import { PolymarketCard } from "./PolymarketCard.tsx";

// Dispatches the right panel for the selected symbol — a regular
// price chart for equity / etf / crypto, a Polymarket card for
// prediction markets. Keeping this in one place means the rest of
// the app stays asset-class-agnostic.
export function SymbolPanel({ symbol, universe }: { symbol: string; universe: Sym[] }) {
  const sym = universe.find((s) => s.symbol === symbol);
  if (!sym) {
    return (
      <section className="glass rounded-2xl p-6 text-[12px] t-tertiary fade-up">
        Symbol {symbol} not found in universe.
      </section>
    );
  }
  if (sym.asset_class === "polymarket") return <PolymarketCard sym={sym} />;
  return <PriceChart sym={sym} />;
}
