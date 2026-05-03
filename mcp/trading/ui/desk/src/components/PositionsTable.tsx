import type { Position } from "../api/types.ts";
import { money, moneySigned, pctSigned, qty as fmtQty } from "../lib/format.ts";

export function PositionsTable({
  positions,
  onSelect,
}: {
  positions: Position[];
  onSelect: (s: string) => void;
}) {
  return (
    <section className="glass rounded-2xl flex flex-col fade-up min-h-0">
      <header className="px-4 pt-3.5 pb-3 flex items-center gap-2 border-b border-[var(--border)]">
        <h2 className="text-[12px] uppercase tracking-wider t-tertiary font-semibold">Positions</h2>
        <span className="ml-auto text-[11px] t-tertiary mono">{positions.length} open</span>
      </header>
      <div className="overflow-auto no-scrollbar">
        <table className="w-full text-[12px]">
          <thead>
            <tr className="text-left text-[10px] uppercase tracking-wider t-tertiary font-medium">
              <th className="px-4 py-2 font-medium">Asset</th>
              <th className="px-2 py-2 font-medium text-right">Qty</th>
              <th className="px-2 py-2 font-medium text-right">Entry</th>
              <th className="px-2 py-2 font-medium text-right">Mark</th>
              <th className="px-2 py-2 font-medium text-right">Mkt value</th>
              <th className="px-2 py-2 font-medium text-right">Unrealized</th>
              <th className="px-2 py-2 font-medium text-right">Day</th>
              <th className="px-4 py-2 font-medium text-right">Weight</th>
            </tr>
          </thead>
          <tbody>
            {positions.map((p) => <Row key={p.symbol + (p.outcome ?? "")} p={p} onClick={() => onSelect(p.symbol)} />)}
            {positions.length === 0 && (
              <tr><td colSpan={8} className="px-4 py-10 text-center t-tertiary text-[12px]">No positions.</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Row({ p, onClick }: { p: Position; onClick: () => void }) {
  const up = p.unrealized_pnl >= 0;
  const dayUp = p.day_pnl >= 0;
  const isPoly = p.asset_class === "polymarket";
  const formatPrice = (n: number) => isPoly ? `${(n * 100).toFixed(0)}¢` : money(n);

  return (
    <tr className="border-t border-[var(--border)] hover:bg-[var(--surface-inset)] cursor-pointer transition-colors" onClick={onClick}>
      <td className="px-4 py-2.5">
        <div className="flex items-center gap-2 min-w-0">
          <ClassDot cls={p.asset_class} />
          <div className="flex flex-col min-w-0">
            {isPoly ? (
              <>
                <div className="flex items-center gap-1.5">
                  <span
                    className="text-[10px] uppercase tracking-wider px-1 py-0.5 rounded font-semibold"
                    style={{
                      background: p.outcome === "YES" ? "var(--color-up-light)" : "var(--color-down-light)",
                      color: p.outcome === "YES" ? "var(--color-up)" : "var(--color-down)",
                    }}
                  >
                    {p.outcome}
                  </span>
                  <span className="text-[10.5px] t-tertiary mono truncate">{p.symbol.replace("POLY:", "")}</span>
                </div>
              </>
            ) : (
              <span className="mono font-semibold t-primary">{p.symbol}</span>
            )}
          </div>
        </div>
      </td>
      <td className="px-2 py-2.5 mono text-right tabular">{fmtQty(p.qty)}</td>
      <td className="px-2 py-2.5 mono text-right tabular t-secondary">{formatPrice(p.avg_cost)}</td>
      <td className="px-2 py-2.5 mono text-right tabular">{formatPrice(p.market_price)}</td>
      <td className="px-2 py-2.5 mono text-right tabular">{money(p.market_value)}</td>
      <td className={`px-2 py-2.5 mono text-right tabular ${up ? "t-up" : "t-down"}`}>
        {moneySigned(p.unrealized_pnl)}
        <span className="text-[10px] ml-1">{pctSigned(p.unrealized_pnl_pct)}</span>
      </td>
      <td className={`px-2 py-2.5 mono text-right tabular ${dayUp ? "t-up" : "t-down"}`}>
        {moneySigned(p.day_pnl)}
      </td>
      <td className="px-4 py-2.5 text-right">
        <WeightBar pct={p.weight_pct} />
      </td>
    </tr>
  );
}

function ClassDot({ cls }: { cls: Position["asset_class"] }) {
  const map: Record<Position["asset_class"], { color: string; label: string }> = {
    equity:     { color: "var(--color-accent)",  label: "EQ" },
    etf:        { color: "var(--color-accent)",  label: "EF" },
    crypto:     { color: "var(--color-warn)",    label: "CR" },
    polymarket: { color: "var(--color-purple)",  label: "PM" },
  };
  const m = map[cls];
  return (
    <span
      className="text-[8.5px] tracking-wider font-bold rounded shrink-0 px-1 py-0.5 mono"
      style={{ background: m.color + "22", color: m.color }}
    >
      {m.label}
    </span>
  );
}

function WeightBar({ pct }: { pct: number }) {
  const w = Math.min(100, pct);
  return (
    <div className="inline-flex items-center gap-2 min-w-[80px] justify-end">
      <span className="mono text-[11px] tabular t-secondary">{pct.toFixed(1)}%</span>
      <span className="relative inline-block w-14 h-1.5 rounded-full bg-[var(--surface-inset)] overflow-hidden">
        <span
          className="absolute inset-y-0 left-0 rounded-full"
          style={{ width: w + "%", background: "var(--color-accent)" }}
        />
      </span>
    </div>
  );
}
