import { useMemo, useState } from "react";
import type { Portfolio, Sym } from "../api/types.ts";
import { money, pctSigned } from "../lib/format.ts";

export function Watchlist({
  portfolio,
  universe,
  selected,
  onSelect,
}: {
  portfolio: Portfolio;
  universe: Sym[];
  selected: string;
  onSelect: (s: string) => void;
}) {
  const filters = useMemo(() => {
    const cls = portfolio.allowed_classes;
    const out: ("all" | "equity" | "crypto" | "etf" | "polymarket")[] = ["all"];
    if (cls.includes("equity")) out.push("equity");
    if (cls.includes("etf"))    out.push("etf");
    if (cls.includes("crypto")) out.push("crypto");
    if (cls.includes("polymarket")) out.push("polymarket");
    return out;
  }, [portfolio.id]);

  const [filter, setFilter] = useState<typeof filters[number]>("all");
  const watchlistSet = useMemo(() => new Set(portfolio.watchlist || []), [portfolio.id, portfolio.watchlist]);

  const rows = universe
    .filter((s) => watchlistSet.has(s.symbol))
    .filter((s) => filter === "all" || s.asset_class === filter);

  return (
    <section className="glass rounded-2xl flex flex-col fade-up min-h-0">
      <header className="px-4 pt-3.5 pb-3 flex items-center gap-2 border-b border-[var(--border)]">
        <h2 className="text-[12px] uppercase tracking-wider t-tertiary font-semibold">Watchlist</h2>
        <span className="ml-auto flex gap-0.5 flex-wrap">
          {filters.map((f) => (
            <button
              key={f}
              className={`tab ${filter === f ? "active" : ""}`}
              onClick={() => setFilter(f)}
            >
              {f === "polymarket" ? "poly" : f}
            </button>
          ))}
        </span>
      </header>
      <div className="flex-1 overflow-auto no-scrollbar">
        {rows.length === 0 && (
          <div className="px-4 py-6 text-[11px] t-tertiary text-center">
            No symbols in this filter.
          </div>
        )}
        {rows.map((s) => (
          <Row
            key={s.symbol}
            sym={s}
            active={s.symbol === selected}
            onClick={() => onSelect(s.symbol)}
          />
        ))}
      </div>
      <footer className="px-4 py-2.5 border-t border-[var(--border)] flex items-center gap-2">
        <button className="btn btn-ghost text-[11px]" disabled>
          <PlusIcon /> Add symbol
        </button>
      </footer>
    </section>
  );
}

function Row({ sym, active, onClick }: { sym: Sym; active: boolean; onClick: () => void }) {
  const up = sym.change_pct >= 0;
  const isPoly = sym.asset_class === "polymarket";
  return (
    <button
      onClick={onClick}
      className={`w-full text-left px-4 py-2.5 flex items-start gap-3 transition-colors border-l-2 ${
        active
          ? "bg-[var(--surface-inset)] border-l-[var(--color-accent)]"
          : "border-l-transparent hover:bg-[var(--surface-inset)]"
      }`}
    >
      <div className="flex-1 min-w-0">
        <div className="flex items-baseline gap-1.5">
          {isPoly ? (
            <span className="text-[10px] uppercase tracking-wider px-1 py-0.5 rounded font-semibold bg-purple-soft" style={{ background: "var(--color-purple-light)", color: "var(--color-purple)" }}>poly</span>
          ) : (
            <span className="text-[12.5px] font-semibold t-primary mono">{sym.symbol}</span>
          )}
          {sym.asset_class === "crypto" && <span className="text-[9px] uppercase tracking-wider t-tertiary">crypto</span>}
          {sym.asset_class === "etf" && <span className="text-[9px] uppercase tracking-wider t-tertiary">etf</span>}
        </div>
        <div className={`text-[11px] truncate leading-tight mt-0.5 ${isPoly ? "t-primary" : "t-tertiary"}`}>
          {sym.name}
        </div>
      </div>
      {!isPoly && <Sparkline points={sym.spark} up={up} />}
      <div className="flex flex-col items-end leading-tight min-w-[68px] shrink-0">
        {isPoly ? (
          <>
            <span className="mono text-[12px] font-medium t-primary tabular">
              <span className="t-up">{Math.round((sym.yes_price ?? 0) * 100)}</span>
              <span className="t-tertiary mx-0.5">/</span>
              <span className="t-down">{Math.round((sym.no_price ?? 0) * 100)}</span>
            </span>
            <span className="text-[10px] t-tertiary">YES / NO</span>
          </>
        ) : (
          <>
            <span className="mono text-[12.5px] font-medium t-primary tabular">{money(sym.price)}</span>
            <span className={`mono text-[11px] ${up ? "t-up" : "t-down"} tabular`}>{pctSigned(sym.change_pct)}</span>
          </>
        )}
      </div>
    </button>
  );
}

function Sparkline({ points, up }: { points: number[]; up: boolean }) {
  const w = 50; const h = 22;
  const min = Math.min(...points);
  const max = Math.max(...points);
  const span = max - min || 1;
  const path = points
    .map((p, i) => {
      const x = (i / (points.length - 1)) * w;
      const y = h - ((p - min) / span) * h;
      return `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  return (
    <svg width={w} height={h} className="shrink-0 mt-2" aria-hidden>
      <path d={path} className="spark" stroke={up ? "var(--color-up)" : "var(--color-down)"} />
    </svg>
  );
}

function PlusIcon() {
  return (
    <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}
