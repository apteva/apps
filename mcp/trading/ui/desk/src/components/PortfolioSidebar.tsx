import type { Portfolio } from "../api/types.ts";
import { money, moneySigned, pctSigned } from "../lib/format.ts";
import { AgentIcon } from "./AgentIcon.tsx";
import { iconFor } from "../lib/agentIcon.ts";

export function PortfolioSidebar({
  portfolios,
  selectedId,
  onSelect,
}: {
  portfolios: Portfolio[];
  selectedId: number;
  onSelect: (id: number) => void;
}) {
  return (
    <aside className="glass rounded-2xl flex flex-col fade-up min-h-0">
      <header className="px-4 pt-3.5 pb-3 flex items-center gap-2 border-b border-[var(--border)]">
        <h2 className="text-[12px] uppercase tracking-wider t-tertiary font-semibold">Portfolios</h2>
        <span className="ml-auto text-[10px] t-tertiary mono">{portfolios.length}</span>
      </header>
      <div className="flex-1 overflow-auto no-scrollbar p-2 space-y-1.5">
        {portfolios.map((p) => (
          <PortfolioCard
            key={p.id}
            p={p}
            active={p.id === selectedId}
            onClick={() => onSelect(p.id)}
          />
        ))}
      </div>
      <footer className="px-3 py-2.5 border-t border-[var(--border)]">
        <button className="btn w-full justify-center text-[11px]" disabled>
          <PlusIcon /> New portfolio
        </button>
      </footer>
    </aside>
  );
}

function PortfolioCard({
  p,
  active,
  onClick,
}: {
  p: Portfolio;
  active: boolean;
  onClick: () => void;
}) {
  const dayUp = p.day_pnl >= 0;
  const statusColor =
    p.status === "active" ? "var(--color-up)" :
    p.status === "paused" ? "var(--color-warn)" : "var(--color-down)";

  return (
    <button
      onClick={onClick}
      className={`w-full text-left rounded-xl p-2.5 border transition-colors ${
        active
          ? "bg-[var(--surface-strong)] border-[var(--border-strong)]"
          : "bg-transparent border-transparent hover:bg-[var(--surface-inset)]"
      }`}
    >
      <div className="flex items-start gap-2">
        <span
          className="shrink-0 mt-0.5 w-7 h-7 rounded-md grid place-items-center"
          style={{ background: "var(--surface-inset)", color: "var(--text-secondary)" }}
        >
          <AgentIcon name={iconFor(p)} size={15} />
        </span>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-1.5">
            <span className="text-[12.5px] font-semibold t-primary truncate">{p.name}</span>
            <span
              className="status-dot shrink-0"
              style={{ color: statusColor, background: statusColor, marginLeft: 2 }}
              title={p.status}
            />
          </div>
          <div className="text-[10.5px] t-tertiary truncate mono">{p.agent_id || "unbound"}</div>
        </div>
      </div>
      <div className="mt-2 flex items-baseline justify-between">
        <span className="mono text-[12.5px] tabular t-primary font-medium">{money(p.equity)}</span>
        <span className={`mono text-[11px] tabular ${dayUp ? "t-up" : "t-down"}`}>
          {moneySigned(p.day_pnl)} <span className="opacity-70">{pctSigned(p.day_pnl_pct)}</span>
        </span>
      </div>
      <div className="mt-1.5 flex gap-0.5 items-center text-[9.5px] uppercase tracking-wider t-tertiary">
        {p.allowed_classes.map((c) => (
          <span
            key={c}
            className="px-1 py-0.5 rounded font-medium"
            style={{ background: "var(--surface-inset)" }}
          >
            {c === "polymarket" ? "poly" : c}
          </span>
        ))}
        <span
          className="ml-auto px-1 py-0.5 rounded font-medium"
          style={{
            background: p.mode === "paper" ? "var(--color-warn-light)" : "var(--color-up-light)",
            color: p.mode === "paper" ? "var(--color-warn)" : "var(--color-up)",
          }}
        >
          {p.mode}
        </span>
      </div>
    </button>
  );
}

function PlusIcon() {
  return (
    <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}
