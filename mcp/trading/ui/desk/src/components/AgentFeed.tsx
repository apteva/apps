import type { Portfolio, JournalEntry } from "../api/types.ts";
import { timeAgo } from "../lib/format.ts";
import { AgentIcon } from "./AgentIcon.tsx";
import { iconFor } from "../lib/agentIcon.ts";

// Journal entries are the agent-feed source of truth — every fill,
// every rationale, every alert lands here. We map kinds to colors;
// `note` and `rejection` round out the palette beyond what the
// initial mock-data design covered.
const KIND_META: Record<JournalEntry["kind"], { label: string; color: string; bg: string }> = {
  thesis:    { label: "thesis",    color: "var(--color-accent)", bg: "var(--color-accent-light)" },
  rationale: { label: "rationale", color: "var(--color-purple)", bg: "var(--color-purple-light)" },
  fill:      { label: "fill",      color: "var(--color-up)",     bg: "var(--color-up-light)" },
  alert:     { label: "alert",     color: "var(--color-warn)",   bg: "var(--color-warn-light)" },
  rejection: { label: "rejection", color: "var(--color-down)",   bg: "var(--color-down-light)" },
  note:      { label: "note",      color: "var(--text-secondary)", bg: "var(--surface-inset)" },
};

export function AgentFeed({
  portfolio,
  entries,
}: {
  portfolio: Portfolio;
  entries: JournalEntry[];
}) {
  return (
    <section className="glass rounded-2xl flex flex-col fade-up min-h-0">
      <header className="px-4 pt-3.5 pb-3 flex items-center gap-2 border-b border-[var(--border)]">
        <span className="status-dot" style={{ color: "var(--color-up)", background: "var(--color-up)" }} />
        <h2 className="text-[12px] uppercase tracking-wider t-tertiary font-semibold">Agent activity</h2>
        <span className="ml-auto inline-flex items-center gap-1.5 text-[10px] t-tertiary mono">
          <AgentIcon name={iconFor(portfolio)} size={12} />
          {portfolio.agent_id || portfolio.name}
        </span>
      </header>
      <div className="flex-1 overflow-auto no-scrollbar">
        <ol className="px-4 py-2">
          {entries.map((e) => <Item key={e.id} e={e} />)}
          {entries.length === 0 && (
            <li className="py-8 text-center t-tertiary text-[12px]">No journal entries yet.</li>
          )}
        </ol>
      </div>
      <footer className="px-4 py-2.5 border-t border-[var(--border)] flex items-center gap-2 text-[11px]">
        <span className="t-tertiary">Polling every 5s</span>
        <span className="ml-auto t-tertiary mono">{entries.length} entries</span>
      </footer>
    </section>
  );
}

function Item({ e }: { e: JournalEntry }) {
  const m = KIND_META[e.kind] ?? KIND_META.note;
  const meta = e.metadata ? formatMeta(e.metadata) : "";
  return (
    <li className="py-2 flex gap-3 items-start text-[12px] border-b border-[var(--border)] last:border-b-0">
      <span
        className="shrink-0 mt-0.5 px-1.5 py-0.5 rounded text-[9.5px] uppercase tracking-wider font-semibold tabular"
        style={{ background: m.bg, color: m.color }}
      >
        {m.label}
      </span>
      <div className="flex-1 min-w-0">
        <p className="t-primary leading-snug">{e.body}</p>
        {meta && <p className="t-tertiary text-[11px] mono mt-0.5">{meta}</p>}
      </div>
      <span className="shrink-0 text-[10px] t-tertiary mono">{timeAgo(e.created_at)}</span>
    </li>
  );
}

// Render the most useful 1–2 fields out of `metadata` as a one-line
// caption — order_id, symbol, side+qty, etc. Keeps the feed dense
// without showing the raw JSON.
function formatMeta(meta: Record<string, unknown>): string {
  const parts: string[] = [];
  if (typeof meta.order_id === "string") parts.push(meta.order_id);
  if (typeof meta.symbol === "string" && typeof meta.side === "string" && typeof meta.qty === "number") {
    parts.push(`${(meta.side as string).toUpperCase()} ${meta.qty} ${meta.symbol}`);
  } else if (typeof meta.symbol === "string") {
    parts.push(meta.symbol);
  }
  if (typeof meta.price === "number") parts.push(`@ ${meta.price}`);
  if (typeof meta.rule === "string") parts.push(`rule: ${meta.rule}`);
  return parts.join(" · ");
}
