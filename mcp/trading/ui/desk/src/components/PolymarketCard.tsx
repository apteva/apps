import type { Sym } from "../api/types.ts";
type Symbol = Sym;
import { bigNum, pctSigned } from "../lib/format.ts";

export function PolymarketCard({ sym }: { sym: Symbol }) {
  const yes = sym.yes_price ?? 0;
  const no = sym.no_price ?? 0;
  const up = sym.change_pct >= 0;
  const resolves = sym.resolves_at ?? 0;
  const daysToResolve = Math.max(0, Math.floor((resolves - Date.now()) / (24 * 60 * 60 * 1000)));

  return (
    <section className="glass rounded-2xl p-5 flex flex-col fade-up min-h-0">
      <div className="flex items-center gap-2 mb-2">
        <span
          className="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded font-semibold"
          style={{ background: "var(--color-purple-light)", color: "var(--color-purple)" }}
        >
          Polymarket
        </span>
        <span className="text-[10.5px] t-tertiary mono">{sym.symbol}</span>
        <span className="ml-auto text-[10.5px] t-tertiary mono">
          Resolves in {daysToResolve}d
        </span>
      </div>

      <h2 className="text-[19px] font-semibold leading-snug t-primary tracking-tight max-w-3xl">
        {sym.name}
      </h2>

      {sym.consensus && (
        <p className="text-[11.5px] t-secondary mt-1 max-w-3xl leading-relaxed italic">
          {sym.consensus}
        </p>
      )}

      {/* YES / NO bar */}
      <div className="grid grid-cols-2 gap-3 mt-5">
        <OutcomeBlock side="YES" prob={yes}  trend={up ? sym.change_pct : -sym.change_pct} />
        <OutcomeBlock side="NO"  prob={no}   trend={up ? -sym.change_pct : sym.change_pct} />
      </div>

      {/* Probability bar */}
      <div className="mt-5">
        <div className="text-[10px] uppercase tracking-wider t-tertiary font-medium mb-1.5">Implied probability</div>
        <div className="h-3 rounded-full overflow-hidden flex" style={{ background: "var(--surface-inset)" }}>
          <div
            className="h-full"
            style={{ width: `${(yes * 100).toFixed(1)}%`, background: "var(--color-up)" }}
            title={`YES ${(yes * 100).toFixed(0)}%`}
          />
          <div
            className="h-full"
            style={{ width: `${(no * 100).toFixed(1)}%`, background: "var(--color-down)" }}
            title={`NO ${(no * 100).toFixed(0)}%`}
          />
        </div>
        <div className="flex justify-between mt-1 text-[10.5px] mono tabular">
          <span className="t-up">YES {(yes * 100).toFixed(0)}%</span>
          <span className="t-down">NO {(no * 100).toFixed(0)}%</span>
        </div>
      </div>

      <div className="flex-1 min-h-[120px] mt-5">
        <ProbHistory points={sym.spark} />
      </div>

      <footer className="grid grid-cols-3 gap-3 pt-4 mt-2 border-t border-[var(--border)]">
        <Mini label="Volume (24h)"      value={`$${bigNum(sym.volume_24h ?? 0)}`} />
        <Mini label="24h change (YES)"  value={pctSigned(sym.change_pct)} tone={up ? "up" : "down"} />
        <Mini label="Resolves" value={new Date(resolves).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })} />
      </footer>
    </section>
  );
}

function OutcomeBlock({
  side, prob, trend,
}: {
  side: "YES" | "NO"; prob: number; trend: number;
}) {
  const isYes = side === "YES";
  const color = isYes ? "var(--color-up)" : "var(--color-down)";
  const bg = isYes ? "var(--color-up-light)" : "var(--color-down-light)";
  const cents = Math.round(prob * 100);
  const trendUp = trend >= 0;
  return (
    <div className="rounded-xl p-4 border" style={{ background: bg, borderColor: "var(--border)" }}>
      <div className="flex items-baseline justify-between">
        <span className="text-[11px] uppercase tracking-wider font-semibold" style={{ color }}>{side}</span>
        <span className={`mono text-[11px] tabular ${trendUp ? "t-up" : "t-down"}`}>{pctSigned(trend)}</span>
      </div>
      <div className="flex items-baseline gap-1 mt-1">
        <span className="mono text-[34px] font-semibold tabular leading-none" style={{ color }}>{cents}</span>
        <span className="text-[11px] t-tertiary leading-none">¢</span>
      </div>
      <div className="text-[10.5px] t-tertiary mt-2">
        Pays $1.00 if {side === "YES" ? "yes" : "no"}, $0 otherwise
      </div>
    </div>
  );
}

function Mini({ label, value, tone }: { label: string; value: string; tone?: "up" | "down" }) {
  const cls = tone === "up" ? "t-up" : tone === "down" ? "t-down" : "t-primary";
  return (
    <div className="flex flex-col leading-tight">
      <span className="text-[10px] uppercase tracking-wider t-tertiary font-medium">{label}</span>
      <span className={`mono text-[13px] tabular ${cls}`}>{value}</span>
    </div>
  );
}

// Simple "probability history" — reuses the spark walk for visual flavor.
function ProbHistory({ points }: { points: number[] }) {
  const w = 800, h = 120, padX = 10, padY = 14;
  const innerW = w - padX * 2, innerH = h - padY * 2;
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
  // 50% line
  const midY = padY + innerH * 0.5;

  return (
    <div>
      <div className="text-[10px] uppercase tracking-wider t-tertiary font-medium mb-1">YES probability — 30d</div>
      <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" className="w-full h-[120px]">
        <line x1={padX} x2={w - padX} y1={midY} y2={midY} stroke="var(--grid)" strokeDasharray="3 3" />
        <path d={path} className="spark" stroke="var(--color-purple)" strokeWidth="1.6" />
      </svg>
    </div>
  );
}
