import { useState } from "react";
import type { Order } from "../api/types.ts";
import { money, qty as fmtQty, timeAgo } from "../lib/format.ts";

type Tab = "working" | "filled" | "all";

export function OrdersTable({ orders }: { orders: Order[] }) {
  const [tab, setTab] = useState<Tab>("working");

  const rows = orders.filter((o) => {
    if (tab === "all") return true;
    if (tab === "working") return o.status === "working";
    if (tab === "filled") return o.status === "filled";
    return true;
  });

  return (
    <section className="glass rounded-2xl flex flex-col fade-up min-h-0">
      <header className="px-4 pt-3.5 pb-3 flex items-center gap-2 border-b border-[var(--border)]">
        <h2 className="text-[12px] uppercase tracking-wider t-tertiary font-semibold">Orders</h2>
        <span className="flex gap-0.5 ml-auto">
          {(["working", "filled", "all"] as const).map((t) => (
            <button key={t} className={`tab ${tab === t ? "active" : ""}`} onClick={() => setTab(t)}>
              {t}
            </button>
          ))}
        </span>
      </header>
      <div className="overflow-auto no-scrollbar">
        <table className="w-full text-[12px]">
          <thead>
            <tr className="text-left text-[10px] uppercase tracking-wider t-tertiary font-medium">
              <th className="px-4 py-2 font-medium">ID</th>
              <th className="px-2 py-2 font-medium">Asset</th>
              <th className="px-2 py-2 font-medium">Side</th>
              <th className="px-2 py-2 font-medium">Type</th>
              <th className="px-2 py-2 font-medium text-right">Qty</th>
              <th className="px-2 py-2 font-medium text-right">Price</th>
              <th className="px-2 py-2 font-medium">Status</th>
              <th className="px-2 py-2 font-medium">Source</th>
              <th className="px-4 py-2 font-medium text-right">Placed</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((o) => <Row key={o.id} o={o} />)}
            {rows.length === 0 && (
              <tr>
                <td colSpan={9} className="px-4 py-10 text-center t-tertiary text-[12px]">
                  No {tab} orders.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Row({ o }: { o: Order }) {
  const isPoly = o.asset_class === "polymarket";
  const sideUp = o.side === "buy" || o.side === "yes";
  const sideColor = sideUp ? "t-up" : "t-down";
  const priceLabel = o.type === "market"
    ? "MKT"
    : isPoly
      ? `${Math.round((o.limit_price ?? o.stop_price ?? 0) * 100)}¢`
      : money(o.limit_price ?? o.stop_price ?? 0);
  const symLabel = isPoly ? o.symbol.replace("POLY:", "") : o.symbol;

  return (
    <tr className="border-t border-[var(--border)]">
      <td className="px-4 py-2.5 mono t-tertiary text-[11px]">{o.id}</td>
      <td className="px-2 py-2.5">
        <div className="flex items-center gap-2 min-w-0">
          <ClassDot cls={o.asset_class} />
          <span className={`mono ${isPoly ? "text-[11px] t-secondary truncate max-w-[180px]" : "font-semibold t-primary"}`} title={o.symbol}>
            {symLabel}
          </span>
        </div>
      </td>
      <td className={`px-2 py-2.5 mono uppercase font-medium ${sideColor}`}>{o.side}</td>
      <td className="px-2 py-2.5 mono uppercase t-secondary text-[11px]">{o.type}</td>
      <td className="px-2 py-2.5 mono text-right tabular">
        {fmtQty(o.qty)}
        {o.filled_qty > 0 && o.filled_qty < o.qty ? <span className="t-tertiary text-[10px]"> ({o.filled_qty})</span> : null}
      </td>
      <td className="px-2 py-2.5 mono text-right tabular">{priceLabel}</td>
      <td className="px-2 py-2.5"><StatusPill status={o.status} /></td>
      <td className="px-2 py-2.5">
        <span
          className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium"
          style={{
            background: o.source === "agent" ? "var(--color-purple-light)" : "var(--surface-inset)",
            color: o.source === "agent" ? "var(--color-purple)" : "var(--text-secondary)",
          }}
        >
          {o.source === "agent" ? "agent" : "human"}
        </span>
      </td>
      <td className="px-4 py-2.5 text-right t-tertiary text-[11px] mono">{timeAgo(o.placed_at)}</td>
    </tr>
  );
}

function ClassDot({ cls }: { cls: Order["asset_class"] }) {
  const map: Record<Order["asset_class"], { color: string; label: string }> = {
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

function StatusPill({ status }: { status: Order["status"] }) {
  const config: Record<Order["status"], { label: string; cls: string }> = {
    working:   { label: "Working",   cls: "bg-accent-soft" },
    filled:    { label: "Filled",    cls: "bg-up-soft" },
    cancelled: { label: "Cancelled", cls: "" },
    rejected:  { label: "Rejected",  cls: "bg-down-soft" },
  };
  const c = config[status];
  return (
    <span
      className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium ${c.cls}`}
      style={!c.cls ? { background: "var(--surface-inset)", color: "var(--text-secondary)" } : undefined}
    >
      {c.label}
    </span>
  );
}
