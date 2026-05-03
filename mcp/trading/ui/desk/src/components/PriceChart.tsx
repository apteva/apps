import { useMemo, useState } from "react";
import type { Sym } from "../api/types.ts";
import { money, pctSigned, bigNum } from "../lib/format.ts";

const RANGES = ["1D", "5D", "1M", "3M", "1Y", "ALL"] as const;
type Range = typeof RANGES[number];

export function PriceChart({ sym }: { sym: Sym }) {
  const [range, setRange] = useState<Range>("1D");

  // Synthesize a longer series from the spark, scaled by range.
  const series = useMemo(() => {
    const n = range === "1D" ? 78 : range === "5D" ? 130 : range === "1M" ? 220 : range === "3M" ? 320 : 540;
    return extendWalk(sym.spark, n, sym.price, sym.change_pct);
  }, [sym.symbol, range, sym.price, sym.change_pct]);

  const up = sym.change_pct >= 0;

  return (
    <section className="glass rounded-2xl p-5 flex flex-col fade-up min-h-0">
      <header className="flex items-start gap-4">
        <div>
          <div className="flex items-baseline gap-2">
            <h2 className="text-2xl font-bold tracking-tight mono t-primary">{sym.symbol}</h2>
            <span className="text-[12px] t-secondary">{sym.name}</span>
          </div>
          <div className="flex items-baseline gap-2 mt-1">
            <span className="text-3xl font-semibold mono tabular t-primary">{money(sym.price)}</span>
            <span className={`text-sm font-medium mono ${up ? "t-up" : "t-down"} tabular`}>
              {pctSigned(sym.change_pct)} · {sym.change_abs >= 0 ? "+" : "−"}${Math.abs(sym.change_abs).toFixed(2)}
            </span>
          </div>
        </div>
        <div className="ml-auto flex flex-col items-end gap-1.5">
          <div className="flex gap-0.5">
            {RANGES.map((r) => (
              <button
                key={r}
                onClick={() => setRange(r)}
                className={`tab ${range === r ? "active" : ""}`}
              >
                {r}
              </button>
            ))}
          </div>
          <div className="text-[11px] t-tertiary mono">
            {sym.volume_24h ? `Vol ${bigNum(sym.volume_24h)}` : ""}
          </div>
        </div>
      </header>

      <div className="mt-4 flex-1 min-h-[280px]">
        <Chart points={series} up={up} />
      </div>

      <footer className="grid grid-cols-4 gap-3 pt-4 mt-2 border-t border-[var(--border)]">
        <Mini label="Open"  value={money(sym.price - sym.change_abs * 0.6)} />
        <Mini label="High"  value={money(sym.price * 1.012)} />
        <Mini label="Low"   value={money(sym.price * 0.987)} />
        <Mini label="Prev"  value={money(sym.price - sym.change_abs)} />
      </footer>
    </section>
  );
}

function Chart({ points, up }: { points: number[]; up: boolean }) {
  const w = 800;
  const h = 280;
  const padX = 12;
  const padY = 18;
  const innerW = w - padX * 2;
  const innerH = h - padY * 2;
  const min = Math.min(...points);
  const max = Math.max(...points);
  const span = max - min || 1;
  const path = points
    .map((p, i) => {
      const x = padX + (i / (points.length - 1)) * innerW;
      const y = padY + innerH - ((p - min) / span) * innerH;
      return `${i === 0 ? "M" : "L"}${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");
  const lastY = padY + innerH - ((points[points.length - 1]! - min) / span) * innerH;
  const fill = `${path} L${(padX + innerW).toFixed(2)},${(padY + innerH).toFixed(2)} L${padX.toFixed(2)},${(padY + innerH).toFixed(2)} Z`;
  const color = up ? "var(--color-up)" : "var(--color-down)";
  const fillStop = up ? "var(--color-up-light)" : "var(--color-down-light)";

  // Horizontal grid lines (5 bands).
  const grids = [0, 0.25, 0.5, 0.75, 1].map((t) => padY + innerH * t);
  // Last price ticks
  const lastPrice = points[points.length - 1]!;

  return (
    <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" className="w-full h-full">
      <defs>
        <linearGradient id="fillGrad" x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stopColor={fillStop} stopOpacity="1" />
          <stop offset="100%" stopColor={fillStop} stopOpacity="0" />
        </linearGradient>
      </defs>
      {grids.map((y, i) => (
        <line key={i} x1={padX} x2={w - padX} y1={y} y2={y} stroke="var(--grid)" strokeWidth="1" strokeDasharray="2 4" />
      ))}
      <path d={fill} fill="url(#fillGrad)" />
      <path d={path} className="spark" stroke={color} strokeWidth="1.6" />
      {/* Last price marker line + label */}
      <line x1={padX} x2={w - padX - 56} y1={lastY} y2={lastY} stroke={color} strokeOpacity="0.5" strokeDasharray="3 3" />
      <g transform={`translate(${w - padX - 54}, ${lastY - 9})`}>
        <rect width="50" height="18" rx="3" fill={color} />
        <text x="25" y="13" textAnchor="middle" fontSize="10.5" fill="#fff" fontFamily="JetBrains Mono, monospace" fontWeight="600">
          {lastPrice >= 1000 ? lastPrice.toFixed(0) : lastPrice.toFixed(2)}
        </text>
      </g>
    </svg>
  );
}

function Mini({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col leading-tight">
      <span className="text-[10px] uppercase tracking-wider t-tertiary font-medium">{label}</span>
      <span className="mono text-[13px] t-primary tabular">{value}</span>
    </div>
  );
}

// Extend a 30-point seed walk to N points so the chart looks like
// progressively more history.
function extendWalk(seed: number[], n: number, anchor: number, biasPct: number): number[] {
  const out: number[] = [];
  let v = anchor / (1 + biasPct / 100); // start of period
  // deterministic pseudo-random from seed
  let s = seed.reduce((a, b, i) => (a * 1664525 + b * 31 * (i + 1) + 1013904223) | 0, 7);
  const rng = () => {
    s = (s * 1664525 + 1013904223) | 0;
    return ((s >>> 0) / 0xffffffff);
  };
  const step = (anchor - v) / n;
  const vol = anchor * 0.0035;
  for (let i = 0; i < n; i++) {
    v += step + (rng() - 0.5) * vol;
    out.push(v);
  }
  out[out.length - 1] = anchor; // pin the close
  return out;
}
