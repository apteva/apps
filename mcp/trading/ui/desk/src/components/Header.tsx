import type { Portfolio } from "../api/types.ts";
import type { HealthDetails, ProviderClass } from "../api/health.ts";
import { money, moneySigned, pctSigned } from "../lib/format.ts";
import { DataSourcePill } from "./DataSourcePill.tsx";

export function Header({
  dark,
  onToggleDark,
  portfolios,
  health,
}: {
  dark: boolean;
  onToggleDark: () => void;
  portfolios: Portfolio[];
  health: HealthDetails | null;
}) {
  const totalEquity = portfolios.reduce((a, p) => a + (p.equity || 0), 0);
  const totalDay    = portfolios.reduce((a, p) => a + (p.day_pnl || 0), 0);
  const totalOpen   = portfolios.reduce((a, p) => a + (p.open_pnl || 0), 0);
  const yEq = totalEquity - totalDay;
  const dayPct = yEq > 0 ? (totalDay / yEq) * 100 : 0;
  const openBasis = totalEquity - totalOpen;
  const openPct = openBasis > 0 ? (totalOpen / openBasis) * 100 : 0;
  const active = portfolios.filter((p) => p.status === "active").length;
  const paused = portfolios.length - active;

  const dayUp = totalDay >= 0;
  const openUp = totalOpen >= 0;

  return (
    <header className="glass rounded-2xl px-5 py-3 flex items-center gap-6 fade-up">
      <div className="flex items-center gap-2.5 shrink-0">
        <div className="w-7 h-7 rounded-lg bg-accent-soft grid place-items-center">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d="M3 17l6-6 4 4 8-8" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" />
            <path d="M14 7h7v7" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </div>
        <div className="flex flex-col leading-tight">
          <span className="font-semibold tracking-tight t-primary">Apteva Trading</span>
          <span className="text-[10px] uppercase tracking-wider t-tertiary">Agent fleet</span>
        </div>
      </div>

      <div className="h-6 w-px bg-[var(--border)]" />

      <Stat label="Fleet equity" value={money(totalEquity)} />
      <Stat label="Day P&L" value={moneySigned(totalDay)} sub={pctSigned(dayPct)} tone={dayUp ? "up" : "down"} />
      <Stat label="Open P&L" value={moneySigned(totalOpen)} sub={pctSigned(openPct)} tone={openUp ? "up" : "down"} />

      <div className="ml-auto flex items-center gap-2 flex-wrap">
        {(["crypto", "polymarket", "equity"] as ProviderClass[]).map((c) => (
          <DataSourcePill key={c} health={health} cls={c} />
        ))}
        <span className="w-px h-5 bg-[var(--border)] mx-1" />
        <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[11px] font-medium bg-up-soft tabular">
          <span className="status-dot" style={{ color: "var(--color-up)", background: "var(--color-up)" }} />
          {active} active
        </span>
        {paused > 0 && (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[11px] font-medium bg-warn-soft tabular">
            {paused} paused
          </span>
        )}
        <button onClick={onToggleDark} className="btn btn-ghost" title="Toggle theme">
          {dark ? <SunIcon /> : <MoonIcon />}
        </button>
      </div>
    </header>
  );
}

function Stat({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: "up" | "down" }) {
  const valueClass = tone === "up" ? "t-up" : tone === "down" ? "t-down" : "t-primary";
  return (
    <div className="flex flex-col leading-tight min-w-0">
      <span className="text-[10px] uppercase tracking-wider t-tertiary font-medium">{label}</span>
      <div className="flex items-baseline gap-1.5">
        <span className={`mono text-[15px] font-semibold ${valueClass}`}>{value}</span>
        {sub && <span className={`mono text-[11px] ${valueClass}`}>{sub}</span>}
      </div>
    </div>
  );
}

function SunIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" />
    </svg>
  );
}
function MoonIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
    </svg>
  );
}
