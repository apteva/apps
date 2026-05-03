import type { Portfolio } from "../api/types.ts";
import { money, moneySigned, pctSigned } from "../lib/format.ts";
import { AgentIcon } from "./AgentIcon.tsx";
import { iconFor } from "../lib/agentIcon.ts";

export function PortfolioHeader({ p }: { p: Portfolio }) {
  const dayUp = p.day_pnl >= 0;
  const openUp = p.open_pnl >= 0;

  return (
    <section className="glass rounded-2xl px-5 py-4 fade-up">
      <div className="flex items-start gap-4">
        <div
          className="w-11 h-11 rounded-xl grid place-items-center shrink-0"
          style={{ background: "var(--surface-inset)", color: "var(--text-secondary)", border: "1px solid var(--border)" }}
        >
          <AgentIcon name={iconFor(p)} size={22} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <h1 className="text-lg font-semibold tracking-tight t-primary">{p.name}</h1>
            <span
              className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-[10px] font-medium tabular"
              style={{
                background: p.mode === "paper" ? "var(--color-warn-light)" : "var(--color-up-light)",
                color: p.mode === "paper" ? "var(--color-warn)" : "var(--color-up)",
              }}
            >
              {p.mode.toUpperCase()}
            </span>
            <span
              className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-[10px] font-medium"
              style={{ background: "var(--surface-inset)", color: "var(--text-secondary)" }}
            >
              <span className="status-dot" style={{
                color: p.status === "active" ? "var(--color-up)" : "var(--color-warn)",
                background: p.status === "active" ? "var(--color-up)" : "var(--color-warn)",
              }} />
              {p.status}
            </span>
          </div>
          <div className="text-[11px] t-tertiary mt-0.5 mono">
            agent: {p.agent_id || "unbound"}
          </div>
          <p className="text-[12.5px] t-secondary mt-2 leading-relaxed max-w-3xl">
            {p.mandate}
          </p>
        </div>
        <div className="grid grid-cols-2 gap-x-5 gap-y-1 shrink-0 ml-auto">
          <Stat label="Equity"        value={money(p.equity)} />
          <Stat label="Day P&L"       value={moneySigned(p.day_pnl)}   sub={pctSigned(p.day_pnl_pct)}   tone={dayUp ? "up" : "down"} />
          <Stat label="Open P&L"      value={moneySigned(p.open_pnl)}  sub={pctSigned(p.open_pnl_pct)}  tone={openUp ? "up" : "down"} />
          <Stat label="Buying power"  value={money(p.buying_power)} muted />
        </div>
      </div>
    </section>
  );
}

function Stat({
  label, value, sub, tone, muted,
}: {
  label: string; value: string; sub?: string; tone?: "up" | "down"; muted?: boolean;
}) {
  const valueClass = tone === "up" ? "t-up" : tone === "down" ? "t-down" : muted ? "t-secondary" : "t-primary";
  return (
    <div className="flex flex-col leading-tight min-w-0">
      <span className="text-[10px] uppercase tracking-wider t-tertiary font-medium">{label}</span>
      <div className="flex items-baseline gap-1.5">
        <span className={`mono text-[14px] font-semibold tabular ${valueClass}`}>{value}</span>
        {sub && <span className={`mono text-[10.5px] tabular ${valueClass}`}>{sub}</span>}
      </div>
    </div>
  );
}
