// FinancePanel — unified personal-wealth tracker.
//
// Three tabs:
//   Overview  — net-worth headline + allocation donut + recent activity
//   Accounts  — list grouped by kind, drill-in shows ledger
//   Holdings  — every non-cash position across accounts, sortable
//
// All money is stored as integer minor units server-side (cents/pence
// etc.); the UI converts to a decimal for display via fmtMoney().
// Colors that ship into SVG are kept in CSS-variable form because the
// dashboard's Tailwind JIT doesn't scan apps/mcp/*/ui/ — class-based
// fill / stroke utilities would render as black.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/finance";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Settings {
  project_id: string;
  base_currency: string;
  week_starts_on: string;
}

interface Account {
  id: number;
  name: string;
  kind: string;
  source: string;
  currency: string;
  opening_balance: number;
  color: string;
  archived: boolean;
  cash_balance: number;
  holdings_value: number;
  total_value: number;
  created_at: string;
}

interface Instrument {
  id: number;
  kind: string;
  symbol: string;
  name: string;
  quote_currency: string;
}

interface Holding {
  id: number;
  account_id: number;
  instrument_id: number;
  quantity: number;
  cost_basis: number;
  current_price?: number | null;
  current_value: number;
  unrealized_pl: number;
  unrealized_pct: number;
  closed_at?: string;
}

interface Transaction {
  id: number;
  account_id: number;
  posted_at: string;
  kind: string;
  amount: number;
  currency: string;
  quantity: number;
  payee: string;
  memo: string;
}

interface Category {
  id: number;
  parent_id?: number;
  name: string;
  kind: "income" | "expense";
  color: string;
  archived: boolean;
}

interface Budget {
  id: number;
  category_id?: number;
  period: string;
  amount: number;
  currency: string;
}

interface BudgetStatus {
  budget_id: number;
  category_id?: number;
  category_name: string;
  period: string;
  period_start: string;
  period_end: string;
  budgeted: number;
  spent: number;
  remaining: number;
  pct_used: number;
  over: boolean;
  currency: string;
}

type Tab = "overview" | "accounts" | "holdings";

// ─── App-event subscription (inlined, matches calendar's pattern) ─

interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(a: string, p: string, fn: (ev: AppEventEnvelope<T>) => void): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) return bridge.subscribe(app, projectId, handler);
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url = `/api/app-events/${encodeURIComponent(app)}?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

// ─── Helpers ──────────────────────────────────────────────────────

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(`${API}${path}`, {
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    ...init,
  });
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
  return r.json();
}

function fmtMoney(minor: number, currency: string, opts?: { signed?: boolean }): string {
  const v = minor / 100;
  const str = new Intl.NumberFormat(undefined, {
    style: "currency",
    currency,
    maximumFractionDigits: 2,
  }).format(v);
  if (opts?.signed && v > 0) return "+" + str;
  return str;
}

function fmtPct(p: number): string {
  const s = p.toFixed(2);
  return p > 0 ? `+${s}%` : `${s}%`;
}

function fmtDate(s: string): string {
  return new Date(s).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
}

const KIND_LABEL: Record<string, string> = {
  cash: "Cash",
  brokerage: "Brokerage",
  p2p: "P2P Lending",
  crypto: "Crypto",
  real_estate: "Real Estate",
  vehicle: "Vehicle",
  pension: "Pension",
  loan: "Loan",
  other: "Other",
};

const KIND_ORDER = ["cash", "brokerage", "crypto", "p2p", "real_estate", "vehicle", "pension", "loan", "other"];

// ─── Icons ───────────────────────────────────────────────────────

function Icon({ name, size = 16 }: { name: string; size?: number }) {
  const stroke = "currentColor";
  const sw = 1.75;
  const common = { width: size, height: size, viewBox: "0 0 24 24", fill: "none", stroke, strokeWidth: sw, strokeLinecap: "round" as const, strokeLinejoin: "round" as const };
  switch (name) {
    case "wallet":
      return <svg {...common}><path d="M21 12V7a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-1"/><path d="M16 12h5v4h-5a2 2 0 0 1 0-4z"/></svg>;
    case "plus":
      return <svg {...common}><path d="M12 5v14M5 12h14"/></svg>;
    case "trending-up":
      return <svg {...common}><polyline points="3 17 9 11 13 15 21 7"/><polyline points="14 7 21 7 21 14"/></svg>;
    case "trending-down":
      return <svg {...common}><polyline points="3 7 9 13 13 9 21 17"/><polyline points="14 17 21 17 21 10"/></svg>;
    case "building":
      return <svg {...common}><rect x="4" y="2" width="16" height="20" rx="1"/><path d="M9 22V12h6v10M9 6h.01M15 6h.01M9 10h.01M15 10h.01"/></svg>;
    case "credit-card":
      return <svg {...common}><rect x="2" y="5" width="20" height="14" rx="2"/><path d="M2 10h20"/></svg>;
    case "coins":
      return <svg {...common}><circle cx="8" cy="8" r="6"/><path d="M18.09 10.37A6 6 0 1 1 10.34 18M7 6h1v4M16.71 13.88l.7.71-2.82 2.82"/></svg>;
    case "home":
      return <svg {...common}><path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2h-3v-7H8v7H5a2 2 0 0 1-2-2z"/></svg>;
    case "car":
      return <svg {...common}><path d="M14 16H9m10 0h2v-3.5a4 4 0 0 0-.65-2.2L19 7a2 2 0 0 0-1.66-1H6.66A2 2 0 0 0 5 7L3.65 10.3A4 4 0 0 0 3 12.5V16h2"/><circle cx="7" cy="17" r="2"/><circle cx="17" cy="17" r="2"/></svg>;
    case "piggy":
      return <svg {...common}><path d="M19 6h-1a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v1H3a1 1 0 0 0-1 1v3a1 1 0 0 0 1 1h1l1 8h13l1-8h2a1 1 0 0 0 1-1V8a1 1 0 0 0-1-1h-1z"/></svg>;
    case "minus":
      return <svg {...common}><path d="M5 12h14"/></svg>;
    case "x":
      return <svg {...common}><path d="M18 6L6 18M6 6l12 12"/></svg>;
    case "chevron-left":
      return <svg {...common}><polyline points="15 18 9 12 15 6"/></svg>;
    case "chevron-right":
      return <svg {...common}><polyline points="9 18 15 12 9 6"/></svg>;
    case "arrow-up-right":
      return <svg {...common}><line x1="7" y1="17" x2="17" y2="7"/><polyline points="7 7 17 7 17 17"/></svg>;
    case "arrow-down-right":
      return <svg {...common}><line x1="7" y1="7" x2="17" y2="17"/><polyline points="17 7 17 17 7 17"/></svg>;
    default:
      return null;
  }
}

function kindIcon(kind: string): string {
  switch (kind) {
    case "cash": return "wallet";
    case "brokerage": return "trending-up";
    case "crypto": return "coins";
    case "p2p": return "piggy";
    case "real_estate": return "home";
    case "vehicle": return "car";
    case "loan": return "credit-card";
    case "pension": return "building";
    default: return "wallet";
  }
}

// ─── Panel ───────────────────────────────────────────────────────

export default function FinancePanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("overview");
  const [settings, setSettings] = useState<Settings | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [holdings, setHoldings] = useState<Holding[]>([]);
  const [recentTxns, setRecentTxns] = useState<Transaction[]>([]);
  const [allocation, setAllocation] = useState<AllocationReport | null>(null);
  const [netWorthSeries, setNetWorthSeries] = useState<NetWorthSeries | null>(null);
  const [budgetStatus, setBudgetStatus] = useState<{ budgets: BudgetStatus[]; period_start: string; period_end: string } | null>(null);
  const [categories, setCategories] = useState<Category[]>([]);
  const [showNewAccount, setShowNewAccount] = useState(false);
  const [showNewBudget, setShowNewBudget] = useState(false);
  const [error, setError] = useState<string>("");

  const refresh = useCallback(async () => {
    try {
      const [s, a, h, t, alloc, nw, bs, cats] = await Promise.all([
        api<Settings>("/settings"),
        api<{ accounts: Account[] }>("/accounts"),
        api<{ holdings: Holding[] }>("/holdings"),
        api<{ transactions: Transaction[] }>("/txns?limit=20"),
        api<AllocationReport>("/reports/allocation"),
        api<NetWorthSeries>(`/reports/net-worth?series=monthly&from=${encodeURIComponent(monthsAgo(12))}&to=${encodeURIComponent(now())}`),
        api<{ budgets: BudgetStatus[]; period_start: string; period_end: string }>("/budgets/status?period=monthly"),
        api<{ categories: Category[] }>("/categories"),
      ]);
      setSettings(s);
      setAccounts(a.accounts ?? []);
      setHoldings((h.holdings ?? []).filter(x => !x.closed_at));
      setRecentTxns(t.transactions ?? []);
      setAllocation(alloc);
      setNetWorthSeries(nw);
      setBudgetStatus(bs);
      setCategories(cats.categories ?? []);
      setError("");
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => { refresh(); }, [refresh]);

  useAppEvents("finance", projectId, () => { refresh(); });

  const base = settings?.base_currency ?? "EUR";

  return (
    <div className="flex h-full flex-col gap-3 p-4">
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Icon name="wallet" size={20} />
          <h1 className="text-lg font-semibold">Finance</h1>
        </div>
        <div className="flex items-center gap-2">
          <nav className="flex rounded-md border border-border overflow-hidden text-sm">
            {(["overview", "accounts", "holdings"] as Tab[]).map(t => (
              <button
                key={t}
                onClick={() => setTab(t)}
                className={`px-3 py-1.5 capitalize ${tab === t ? "bg-accent text-bg" : "hover:bg-bg-hover"}`}
              >{t}</button>
            ))}
          </nav>
          <button
            onClick={() => setShowNewAccount(true)}
            className="flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm text-bg hover:bg-accent-hover"
          >
            <Icon name="plus" size={14} /> Account
          </button>
        </div>
      </header>

      {error && (
        <div className="rounded-md border border-error/30 bg-error/10 px-3 py-2 text-sm text-error border-error/30 bg-error/10 text-error">
          {error}
        </div>
      )}

      <div className="flex-1 overflow-auto">
        {tab === "overview" && (
          <OverviewTab
            accounts={accounts}
            holdings={holdings}
            recentTxns={recentTxns}
            allocation={allocation}
            netWorth={netWorthSeries}
            budgetStatus={budgetStatus}
            base={base}
            onSetBudget={() => setShowNewBudget(true)}
          />
        )}
        {tab === "accounts" && (
          <AccountsTab accounts={accounts} base={base} onChanged={refresh} />
        )}
        {tab === "holdings" && (
          <HoldingsTab holdings={holdings} accounts={accounts} />
        )}
      </div>

      {showNewAccount && (
        <NewAccountDialog
          onClose={() => setShowNewAccount(false)}
          onCreated={async () => { setShowNewAccount(false); await refresh(); }}
          defaultCurrency={base}
        />
      )}
      {showNewBudget && (
        <NewBudgetDialog
          categories={categories}
          base={base}
          onClose={() => setShowNewBudget(false)}
          onCreated={async () => { setShowNewBudget(false); await refresh(); }}
        />
      )}
    </div>
  );
}

// ─── Overview ────────────────────────────────────────────────────

interface AllocationGroup { kind?: string; currency?: string; value: number; }
interface AllocationReport {
  base_currency: string;
  total: number;
  by_account_kind: AllocationGroup[];
  by_instrument_kind: AllocationGroup[];
  by_currency: AllocationGroup[];
  top_instruments: Array<{ id: number; symbol: string; name: string; kind: string; value: number }>;
}
interface NetWorthSeries {
  series: string;
  base_currency: string;
  points: Array<{ as_of: string; total: number }>;
}

function OverviewTab({
  accounts, holdings, recentTxns, allocation, netWorth, budgetStatus, base, onSetBudget,
}: {
  accounts: Account[];
  holdings: Holding[];
  recentTxns: Transaction[];
  allocation: AllocationReport | null;
  netWorth: NetWorthSeries | null;
  budgetStatus: { budgets: BudgetStatus[]; period_start: string; period_end: string } | null;
  base: string;
  onSetBudget: () => void;
}) {
  const total = allocation?.total ?? 0;
  const lastDelta = useMemo(() => {
    const pts = netWorth?.points ?? [];
    if (pts.length < 2) return 0;
    return pts[pts.length - 1].total - pts[pts.length - 2].total;
  }, [netWorth]);

  // Top movers from current holdings (by unrealized_pct).
  const movers = useMemo(() => {
    const m = [...holdings].filter(h => h.current_price != null);
    m.sort((a, b) => Math.abs(b.unrealized_pct) - Math.abs(a.unrealized_pct));
    return m.slice(0, 4);
  }, [holdings]);

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <section className="rounded-lg border border-border bg-bg-card p-4 border-border bg-bg-card lg:col-span-2">
        <div className="flex items-start justify-between">
          <div>
            <div className="text-xs uppercase tracking-wide text-text-muted">Net worth</div>
            <div className="mt-1 text-3xl font-semibold">{fmtMoney(total, base)}</div>
            {lastDelta !== 0 && (
              <div className={`mt-1 flex items-center gap-1 text-sm ${lastDelta > 0 ? "text-success" : "text-error"}`}>
                <Icon name={lastDelta > 0 ? "trending-up" : "trending-down"} size={14} />
                {fmtMoney(Math.abs(lastDelta), base)} this month
              </div>
            )}
          </div>
        </div>
        <Sparkline points={(netWorth?.points ?? []).map(p => p.total)} height={80} />
      </section>

      <section className="rounded-lg border border-border bg-bg-card p-4 border-border bg-bg-card">
        <div className="text-xs uppercase tracking-wide text-text-muted">Allocation</div>
        <AllocationDonut groups={allocation?.by_account_kind ?? []} total={total} />
      </section>

      <BudgetsCard status={budgetStatus} base={base} onSetBudget={onSetBudget} />

      <section className="rounded-lg border border-border bg-bg-card p-4 border-border bg-bg-card lg:col-span-2">
        <div className="mb-3 text-xs uppercase tracking-wide text-text-muted">Top movers</div>
        {movers.length === 0 ? (
          <EmptyState message="No price data yet — set a price on an instrument to see P&L." />
        ) : (
          <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            {movers.map(h => {
              const inst = ""; // resolved via instrument id mapping if needed
              const acc = accounts.find(a => a.id === h.account_id);
              return (
                <li key={h.id} className="flex items-center justify-between rounded-md border border-border px-3 py-2 border-border">
                  <div>
                    <div className="text-sm font-medium">#{h.instrument_id} {inst}</div>
                    <div className="text-xs text-text-muted">{acc?.name ?? "—"} • {h.quantity}</div>
                  </div>
                  <div className={`text-right text-sm ${h.unrealized_pl >= 0 ? "text-success" : "text-error"}`}>
                    <div className="font-medium">{fmtMoney(h.current_value, acc?.currency ?? base)}</div>
                    <div className="text-xs">{fmtPct(h.unrealized_pct)}</div>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>

      <section className="rounded-lg border border-border bg-bg-card p-4 border-border bg-bg-card">
        <div className="mb-3 text-xs uppercase tracking-wide text-text-muted">Recent activity</div>
        {recentTxns.length === 0 ? (
          <EmptyState message="No transactions yet." />
        ) : (
          <ul className="divide-y divide-border-subtle text-sm">
            {recentTxns.slice(0, 8).map(t => (
              <li key={t.id} className="flex items-center justify-between py-2">
                <div>
                  <div className="font-medium">{t.payee || t.memo || KIND_LABEL[t.kind] || t.kind}</div>
                  <div className="text-xs text-text-muted">{fmtDate(t.posted_at)} • {t.kind}</div>
                </div>
                <div className={`text-right ${t.amount >= 0 ? "text-success" : "text-error"}`}>
                  {fmtMoney(t.amount, t.currency, { signed: true })}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function BudgetsCard({
  status, base, onSetBudget,
}: {
  status: { budgets: BudgetStatus[]; period_start: string; period_end: string } | null;
  base: string;
  onSetBudget: () => void;
}) {
  const periodLabel = useMemo(() => {
    if (!status?.period_start) return "This month";
    const d = new Date(status.period_start);
    return d.toLocaleDateString(undefined, { month: "long", year: "numeric" });
  }, [status]);

  return (
    <section className="rounded-lg border border-border bg-bg-card p-4 border-border bg-bg-card lg:col-span-2">
      <header className="mb-3 flex items-center justify-between">
        <div>
          <div className="text-xs uppercase tracking-wide text-text-muted">Budgets — {periodLabel}</div>
        </div>
        <button
          onClick={onSetBudget}
          className="flex items-center gap-1 text-xs text-text-muted hover:text-text"
        >
          <Icon name="plus" size={12} /> Set budget
        </button>
      </header>
      {!status || status.budgets.length === 0 ? (
        <EmptyState message='No budgets yet — set caps per category to track them here.' />
      ) : (
        <ul className="space-y-2">
          {status.budgets.map(b => <BudgetBar key={b.budget_id} b={b} base={base} />)}
        </ul>
      )}
    </section>
  );
}

function BudgetBar({ b, base }: { b: BudgetStatus; base: string }) {
  // Clamp the bar visual at 100% even when over-budget — we badge
  // over-spend separately so the bar stays comparable across rows.
  const pct = Math.min(100, b.pct_used);
  let band: "ok" | "warn" | "over" = "ok";
  if (b.over) band = "over";
  else if (b.pct_used >= 75) band = "warn";
  const barColor =
    band === "over" ? "bg-error"
    : band === "warn" ? "bg-warn"
    : "bg-success";
  const textColor =
    band === "over" ? "text-error"
    : band === "warn" ? "text-warn"
    : "text-text-muted";
  const ccy = b.currency || base;
  return (
    <li>
      <div className="mb-1 flex items-center justify-between text-sm">
        <div className="flex items-center gap-1.5 truncate">
          <span className="truncate">{b.category_name}</span>
          {b.over && <Icon name="trending-up" size={12} />}
        </div>
        <div className={`shrink-0 text-xs tabular-nums ${textColor}`}>
          {fmtMoney(b.spent, ccy)} / {fmtMoney(b.budgeted, ccy)}
          <span className="ml-2">{Math.round(b.pct_used)}%</span>
        </div>
      </div>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-bg-hover">
        <div className={`h-full rounded-full ${barColor}`} style={{ width: `${pct}%` }} />
      </div>
    </li>
  );
}

function Sparkline({ points, height = 60 }: { points: number[]; height?: number }) {
  if (points.length < 2) return <div style={{ height }} className="mt-3 text-xs text-text-dim">Not enough data</div>;
  const min = Math.min(...points);
  const max = Math.max(...points);
  const range = Math.max(1, max - min);
  const w = 600;
  const h = height;
  const pts = points.map((p, i) => {
    const x = (i / (points.length - 1)) * w;
    const y = h - ((p - min) / range) * (h - 4) - 2;
    return `${x},${y}`;
  }).join(" ");
  // Color via CSS var so dashboard's Tailwind JIT doesn't need to scan us.
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="mt-3 w-full" preserveAspectRatio="none" style={{ height }}>
      <polyline points={pts} fill="none" stroke="var(--accent)" strokeWidth={2} />
    </svg>
  );
}

function AllocationDonut({ groups, total }: { groups: AllocationGroup[]; total: number }) {
  const data = groups.filter(g => g.value > 0);
  if (total === 0 || data.length === 0) return <EmptyState message="No allocation yet." />;
  // Donut slice palette — theme-token-driven so it follows light/dark.
  // Six semantic colors, recycled when there are more slices than entries.
  const colors = [
    "var(--accent)",
    "var(--success)",
    "var(--warn)",
    "var(--error)",
    "var(--accent-muted)",
    "var(--text-dim)",
  ];
  const radius = 50;
  const cx = 60, cy = 60;
  let cum = 0;
  const arcs = data.map((g, i) => {
    const frac = g.value / total;
    const start = cum;
    cum += frac;
    const end = cum;
    return { ...g, color: colors[i % colors.length], path: arcPath(cx, cy, radius, start, end) };
  });
  return (
    <div className="mt-2 flex items-center gap-4">
      <svg viewBox="0 0 120 120" className="h-32 w-32 shrink-0">
        {arcs.map((a, i) => (
          <path key={i} d={a.path} fill={a.color} />
        ))}
        <circle cx={cx} cy={cy} r={28} fill="var(--bg-card)" />
      </svg>
      <ul className="flex-1 space-y-1 text-xs">
        {arcs.map((a, i) => (
          <li key={i} className="flex items-center gap-2">
            <span className="h-2 w-2 shrink-0 rounded-sm" style={{ background: a.color }} />
            <span className="flex-1 truncate">{KIND_LABEL[a.kind ?? ""] ?? a.kind ?? a.currency}</span>
            <span className="tabular-nums text-text-muted">{Math.round((a.value / total) * 100)}%</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

// arcPath returns an SVG path for a donut slice from `start` to `end`
// as fractions of a full turn (0..1). Drawn as a pie wedge; the inner
// donut hole is painted over with a centered circle.
function arcPath(cx: number, cy: number, r: number, start: number, end: number): string {
  const a0 = start * 2 * Math.PI - Math.PI / 2;
  const a1 = end * 2 * Math.PI - Math.PI / 2;
  const x0 = cx + r * Math.cos(a0);
  const y0 = cy + r * Math.sin(a0);
  const x1 = cx + r * Math.cos(a1);
  const y1 = cy + r * Math.sin(a1);
  const large = end - start > 0.5 ? 1 : 0;
  return `M ${cx} ${cy} L ${x0} ${y0} A ${r} ${r} 0 ${large} 1 ${x1} ${y1} Z`;
}

// ─── Accounts tab ────────────────────────────────────────────────

function AccountsTab({ accounts, base, onChanged }: { accounts: Account[]; base: string; onChanged: () => void }) {
  const [selected, setSelected] = useState<Account | null>(null);
  const grouped = useMemo(() => {
    const m: Record<string, Account[]> = {};
    for (const a of accounts) {
      if (a.archived) continue;
      (m[a.kind] ||= []).push(a);
    }
    return KIND_ORDER.map(k => [k, m[k] ?? []] as const).filter(([, v]) => v.length > 0);
  }, [accounts]);

  if (selected) {
    return <AccountDetail account={selected} onBack={() => setSelected(null)} onChanged={onChanged} />;
  }

  if (accounts.filter(a => !a.archived).length === 0) {
    return <EmptyState message='No accounts yet. Click "Account" above to add one.' />;
  }

  return (
    <div className="space-y-4">
      {grouped.map(([kind, accs]) => {
        const subtotal = accs.reduce((s, a) => s + a.total_value, 0);
        return (
          <section key={kind} className="rounded-lg border border-border bg-bg-card">
            <header className="flex items-center justify-between border-b border-border-subtle px-4 py-2 border-border-subtle">
              <div className="flex items-center gap-2">
                <Icon name={kindIcon(kind)} size={16} />
                <span className="text-sm font-medium">{KIND_LABEL[kind]}</span>
                <span className="text-xs text-text-muted">{accs.length}</span>
              </div>
              <span className="text-sm tabular-nums">{fmtMoney(subtotal, base)}</span>
            </header>
            <ul className="divide-y divide-border-subtle">
              {accs.map(a => (
                <li key={a.id}>
                  <button
                    onClick={() => setSelected(a)}
                    className="flex w-full items-center justify-between px-4 py-3 text-left hover:bg-bg-hover"
                  >
                    <div>
                      <div className="text-sm font-medium">{a.name}</div>
                      <div className="text-xs text-text-muted">
                        {a.source === "manual" ? "Manual" : a.source.replace("integration:", "")} • {a.currency}
                      </div>
                    </div>
                    <div className="text-right">
                      <div className="tabular-nums">{fmtMoney(a.total_value, a.currency)}</div>
                      {a.holdings_value > 0 && (
                        <div className="text-xs text-text-muted tabular-nums">
                          cash {fmtMoney(a.cash_balance, a.currency)}
                        </div>
                      )}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          </section>
        );
      })}
    </div>
  );
}

function AccountDetail({ account, onBack, onChanged }: { account: Account; onBack: () => void; onChanged: () => void }) {
  const [txns, setTxns] = useState<Transaction[]>([]);
  const [showNewTxn, setShowNewTxn] = useState(false);
  const refresh = useCallback(async () => {
    const r = await api<{ transactions: Transaction[] }>(`/txns?account_id=${account.id}&limit=200`);
    setTxns(r.transactions ?? []);
  }, [account.id]);
  useEffect(() => { refresh(); }, [refresh]);

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <button onClick={onBack} className="flex items-center gap-1 text-sm text-text-muted hover:text-text">
          <Icon name="chevron-left" size={14} /> Accounts
        </button>
        <button
          onClick={() => setShowNewTxn(true)}
          className="flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm text-bg hover:bg-accent-hover"
        >
          <Icon name="plus" size={14} /> Transaction
        </button>
      </div>

      <div className="rounded-lg border border-border bg-bg-card p-4 border-border bg-bg-card">
        <div className="flex items-center justify-between">
          <div>
            <div className="flex items-center gap-2 text-xs uppercase tracking-wide text-text-muted">
              <Icon name={kindIcon(account.kind)} size={14} /> {KIND_LABEL[account.kind]}
            </div>
            <h2 className="mt-1 text-xl font-semibold">{account.name}</h2>
          </div>
          <div className="text-right">
            <div className="text-2xl font-semibold tabular-nums">{fmtMoney(account.total_value, account.currency)}</div>
            <div className="text-xs text-text-muted tabular-nums">
              cash {fmtMoney(account.cash_balance, account.currency)}
              {account.holdings_value !== 0 && <> + holdings {fmtMoney(account.holdings_value, account.currency)}</>}
            </div>
          </div>
        </div>
      </div>

      <div className="rounded-lg border border-border bg-bg-card">
        <header className="border-b border-border-subtle px-4 py-2 text-xs uppercase tracking-wide text-text-muted border-border-subtle">Ledger</header>
        {txns.length === 0 ? (
          <EmptyState message="No transactions yet." />
        ) : (
          <ul className="divide-y divide-border-subtle text-sm">
            {txns.map(t => (
              <li key={t.id} className="flex items-center justify-between px-4 py-2">
                <div>
                  <div className="font-medium">{t.payee || t.memo || KIND_LABEL[t.kind] || t.kind}</div>
                  <div className="text-xs text-text-muted">{fmtDate(t.posted_at)} • {t.kind}{t.quantity !== 0 ? ` • ${t.quantity}` : ""}</div>
                </div>
                <div className={`text-right tabular-nums ${t.amount >= 0 ? "text-success" : "text-error"}`}>
                  {fmtMoney(t.amount, t.currency, { signed: true })}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>

      {showNewTxn && (
        <NewTxnDialog
          account={account}
          onClose={() => setShowNewTxn(false)}
          onCreated={async () => { setShowNewTxn(false); await refresh(); onChanged(); }}
        />
      )}
    </div>
  );
}

// ─── Holdings tab ────────────────────────────────────────────────

type SortKey = "value" | "pl" | "pct";

function HoldingsTab({ holdings, accounts }: { holdings: Holding[]; accounts: Account[] }) {
  const [sortKey, setSortKey] = useState<SortKey>("value");
  const [instruments, setInstruments] = useState<Record<number, Instrument>>({});

  // Fetch instrument names for the holdings we have. One call per
  // unique id rather than one batch fetch — there's no /instruments?ids
  // endpoint in v0.1.
  useEffect(() => {
    const missing = Array.from(new Set(holdings.map(h => h.instrument_id))).filter(id => !instruments[id]);
    if (missing.length === 0) return;
    let cancelled = false;
    (async () => {
      const updates: Record<number, Instrument> = {};
      for (const id of missing) {
        try {
          const ins = await api<Instrument>(`/instruments/${id}`);
          updates[id] = ins;
        } catch {}
      }
      if (!cancelled && Object.keys(updates).length > 0) {
        setInstruments(prev => ({ ...prev, ...updates }));
      }
    })();
    return () => { cancelled = true; };
  }, [holdings, instruments]);

  const sorted = useMemo(() => {
    const arr = [...holdings];
    arr.sort((a, b) => {
      switch (sortKey) {
        case "pl": return b.unrealized_pl - a.unrealized_pl;
        case "pct": return b.unrealized_pct - a.unrealized_pct;
        default: return b.current_value - a.current_value;
      }
    });
    return arr;
  }, [holdings, sortKey]);

  if (holdings.length === 0) {
    return <EmptyState message="No holdings yet — buy an instrument or set a holding manually to track it here." />;
  }

  const Header = ({ k, label, align = "left" }: { k: SortKey; label: string; align?: "left" | "right" }) => (
    <th
      onClick={() => setSortKey(k)}
      className={`cursor-pointer select-none px-3 py-2 text-xs uppercase tracking-wide text-text-muted ${align === "right" ? "text-right" : "text-left"} hover:text-text`}
    >
      {label}{sortKey === k && " ↓"}
    </th>
  );

  return (
    <div className="overflow-hidden rounded-lg border border-border bg-bg-card">
      <table className="w-full text-sm">
        <thead className="border-b border-border-subtle">
          <tr>
            <th className="px-3 py-2 text-left text-xs uppercase tracking-wide text-text-muted">Instrument</th>
            <th className="px-3 py-2 text-left text-xs uppercase tracking-wide text-text-muted">Account</th>
            <th className="px-3 py-2 text-right text-xs uppercase tracking-wide text-text-muted">Qty</th>
            <Header k="value" label="Value" align="right" />
            <th className="px-3 py-2 text-right text-xs uppercase tracking-wide text-text-muted">Cost basis</th>
            <Header k="pl" label="P&L" align="right" />
            <Header k="pct" label="%" align="right" />
          </tr>
        </thead>
        <tbody>
          {sorted.map(h => {
            const acc = accounts.find(a => a.id === h.account_id);
            const ins = instruments[h.instrument_id];
            return (
              <tr key={h.id} className="border-b border-border-subtle last:border-0 border-border-subtle">
                <td className="px-3 py-2">
                  <div className="font-medium">{ins?.symbol ?? `#${h.instrument_id}`}</div>
                  <div className="text-xs text-text-muted">{ins?.name ?? ""}</div>
                </td>
                <td className="px-3 py-2 text-text-muted">{acc?.name ?? "—"}</td>
                <td className="px-3 py-2 text-right tabular-nums">{h.quantity}</td>
                <td className="px-3 py-2 text-right tabular-nums">{fmtMoney(h.current_value, acc?.currency ?? "EUR")}</td>
                <td className="px-3 py-2 text-right tabular-nums text-text-muted">{fmtMoney(h.cost_basis, acc?.currency ?? "EUR")}</td>
                <td className={`px-3 py-2 text-right tabular-nums ${h.unrealized_pl >= 0 ? "text-success" : "text-error"}`}>
                  {fmtMoney(h.unrealized_pl, acc?.currency ?? "EUR", { signed: true })}
                </td>
                <td className={`px-3 py-2 text-right tabular-nums ${h.unrealized_pct >= 0 ? "text-success" : "text-error"}`}>
                  {fmtPct(h.unrealized_pct)}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ─── Dialogs ─────────────────────────────────────────────────────

function NewAccountDialog({ onClose, onCreated, defaultCurrency }: { onClose: () => void; onCreated: () => void; defaultCurrency: string }) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState("cash");
  const [currency, setCurrency] = useState(defaultCurrency);
  const [opening, setOpening] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const submit = async () => {
    setBusy(true); setErr("");
    try {
      const minor = parseMoneyDecimal(opening || "0");
      await api<Account>("/accounts", {
        method: "POST",
        body: JSON.stringify({ name, kind, currency, opening_balance: minor }),
      });
      onCreated();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };
  return (
    <Dialog title="New account" onClose={onClose}>
      <Field label="Name">
        <input value={name} onChange={e => setName(e.target.value)} className="input" autoFocus />
      </Field>
      <Field label="Kind">
        <select value={kind} onChange={e => setKind(e.target.value)} className="input">
          {KIND_ORDER.map(k => <option key={k} value={k}>{KIND_LABEL[k]}</option>)}
        </select>
      </Field>
      <Field label="Currency">
        <input value={currency} onChange={e => setCurrency(e.target.value.toUpperCase())} className="input uppercase" maxLength={3} />
      </Field>
      <Field label="Opening balance (decimal — e.g. 1234.56)">
        <input value={opening} onChange={e => setOpening(e.target.value)} className="input" placeholder="0" />
      </Field>
      {err && <p className="text-sm text-error">{err}</p>}
      <DialogActions>
        <button onClick={onClose} className="btn-secondary">Cancel</button>
        <button onClick={submit} disabled={busy || !name} className="btn-primary">{busy ? "Creating…" : "Create"}</button>
      </DialogActions>
    </Dialog>
  );
}

function NewTxnDialog({ account, onClose, onCreated }: { account: Account; onClose: () => void; onCreated: () => void }) {
  const [kind, setKind] = useState<"deposit" | "withdraw" | "income" | "expense" | "fee" | "tax">("expense");
  const [amount, setAmount] = useState("");
  const [postedAt, setPostedAt] = useState(() => new Date().toISOString().slice(0, 10));
  const [payee, setPayee] = useState("");
  const [memo, setMemo] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const submit = async () => {
    setBusy(true); setErr("");
    try {
      const minor = parseMoneyDecimal(amount || "0");
      await api<Transaction>("/txns", {
        method: "POST",
        body: JSON.stringify({
          account_id: account.id,
          kind,
          amount: minor,
          posted_at: postedAt + "T00:00:00Z",
          payee, memo,
        }),
      });
      onCreated();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };
  return (
    <Dialog title={`New transaction — ${account.name}`} onClose={onClose}>
      <Field label="Kind">
        <select value={kind} onChange={e => setKind(e.target.value as "deposit" | "withdraw" | "income" | "expense" | "fee" | "tax")} className="input">
          <option value="deposit">Deposit</option>
          <option value="withdraw">Withdraw</option>
          <option value="income">Income</option>
          <option value="expense">Expense</option>
          <option value="fee">Fee</option>
          <option value="tax">Tax</option>
        </select>
      </Field>
      <Field label={`Amount (${account.currency})`}>
        <input value={amount} onChange={e => setAmount(e.target.value)} className="input" placeholder="0.00" autoFocus />
      </Field>
      <Field label="Date">
        <input type="date" value={postedAt} onChange={e => setPostedAt(e.target.value)} className="input" />
      </Field>
      <Field label="Payee">
        <input value={payee} onChange={e => setPayee(e.target.value)} className="input" />
      </Field>
      <Field label="Memo">
        <input value={memo} onChange={e => setMemo(e.target.value)} className="input" />
      </Field>
      {err && <p className="text-sm text-error">{err}</p>}
      <DialogActions>
        <button onClick={onClose} className="btn-secondary">Cancel</button>
        <button onClick={submit} disabled={busy} className="btn-primary">{busy ? "Saving…" : "Save"}</button>
      </DialogActions>
    </Dialog>
  );
}

function NewBudgetDialog({
  categories, base, onClose, onCreated,
}: {
  categories: Category[];
  base: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  // "" = total spend (NULL category_id on the server).
  const [categoryID, setCategoryID] = useState<string>("");
  const [amount, setAmount] = useState("");
  const [period, setPeriod] = useState<"monthly" | "weekly" | "quarterly" | "yearly">("monthly");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  // Only expense categories are meaningful to budget against; sort so
  // top-level parents come first, with children indented underneath.
  const sorted = useMemo(() => {
    const expense = categories.filter(c => c.kind === "expense" && !c.archived);
    const byId = new Map(expense.map(c => [c.id, c] as const));
    const rootIDs = expense.filter(c => !c.parent_id || !byId.has(c.parent_id)).map(c => c.id);
    const out: Array<{ c: Category; depth: number }> = [];
    const visit = (id: number, depth: number) => {
      const c = byId.get(id);
      if (!c) return;
      out.push({ c, depth });
      for (const child of expense) {
        if (child.parent_id === id) visit(child.id, depth + 1);
      }
    };
    for (const id of rootIDs) visit(id, 0);
    return out;
  }, [categories]);

  const submit = async () => {
    setBusy(true); setErr("");
    try {
      const minor = parseMoneyDecimal(amount || "0");
      if (minor <= 0) throw new Error("amount must be positive");
      const body: Record<string, unknown> = { period, amount: minor };
      if (categoryID !== "") body.category_id = Number(categoryID);
      await api<Budget>("/budgets", { method: "POST", body: JSON.stringify(body) });
      onCreated();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <Dialog title="Set budget" onClose={onClose}>
      <Field label="Category">
        <select value={categoryID} onChange={e => setCategoryID(e.target.value)} className="input">
          <option value="">— Total monthly spend —</option>
          {sorted.map(({ c, depth }) => (
            <option key={c.id} value={c.id}>{" ".repeat(depth * 4) + c.name}</option>
          ))}
        </select>
      </Field>
      <Field label="Period">
        <select value={period} onChange={e => setPeriod(e.target.value as typeof period)} className="input">
          <option value="weekly">Weekly</option>
          <option value="monthly">Monthly</option>
          <option value="quarterly">Quarterly</option>
          <option value="yearly">Yearly</option>
        </select>
      </Field>
      <Field label={`Amount (${base})`}>
        <input value={amount} onChange={e => setAmount(e.target.value)} className="input" placeholder="500.00" autoFocus />
      </Field>
      {err && <p className="text-sm text-error">{err}</p>}
      <DialogActions>
        <button onClick={onClose} className="btn-secondary">Cancel</button>
        <button onClick={submit} disabled={busy || !amount} className="btn-primary">{busy ? "Saving…" : "Save"}</button>
      </DialogActions>
    </Dialog>
  );
}

// ─── Generic UI bits ─────────────────────────────────────────────

function Dialog({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-bg-overlay p-4">
      <div className="w-full max-w-md rounded-lg border border-border bg-bg-card p-5 shadow-xl">
        <header className="mb-3 flex items-center justify-between">
          <h3 className="text-base font-semibold">{title}</h3>
          <button onClick={onClose} className="text-text-dim hover:text-text"><Icon name="x" size={18} /></button>
        </header>
        <div className="space-y-3">{children}</div>
        <style>{`
          .input { width: 100%; padding: 0.5rem 0.75rem; border-radius: 0.375rem; border: 1px solid var(--border); background: var(--bg-input); color: var(--text); }
          .input:focus { outline: 2px solid var(--accent); outline-offset: -1px; }
          .btn-primary { padding: 0.5rem 1rem; border-radius: 0.375rem; background: var(--accent); color: var(--bg); font-size: 0.875rem; }
          .btn-primary:disabled { opacity: 0.5; }
          .btn-primary:hover:not(:disabled) { background: var(--accent-hover); }
          .btn-secondary { padding: 0.5rem 1rem; border-radius: 0.375rem; background: transparent; color: var(--text); font-size: 0.875rem; border: 1px solid var(--border); }
          .btn-secondary:hover { background: var(--bg-hover); }
        `}</style>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs font-medium uppercase tracking-wide text-text-muted">{label}</span>
      {children}
    </label>
  );
}

function DialogActions({ children }: { children: React.ReactNode }) {
  return <div className="mt-4 flex justify-end gap-2">{children}</div>;
}

function EmptyState({ message }: { message: string }) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-8 text-center text-sm text-text-muted">
      <Icon name="wallet" size={24} />
      <p>{message}</p>
    </div>
  );
}

// ─── decimal-to-minor helper ─────────────────────────────────────

// Lightweight echo of the server's parseMoneyToMinor. We keep it in
// the UI so the user can type "12.34" or "12,34" naturally and we
// send minor units over the wire.
function parseMoneyDecimal(s: string): number {
  s = s.trim();
  if (!s) return 0;
  let neg = false;
  if (s.startsWith("(") && s.endsWith(")")) { neg = true; s = s.slice(1, -1); }
  if (s.startsWith("-")) { neg = true; s = s.slice(1); }
  s = s.replace(/\s/g, "");
  const lastDot = s.lastIndexOf(".");
  const lastComma = s.lastIndexOf(",");
  let integer = s, fraction = "";
  if (lastDot >= 0 && lastComma >= 0) {
    if (lastDot > lastComma) { integer = s.slice(0, lastDot).replace(/,/g, ""); fraction = s.slice(lastDot + 1); }
    else { integer = s.slice(0, lastComma).replace(/\./g, ""); fraction = s.slice(lastComma + 1); }
  } else if (lastDot >= 0) { integer = s.slice(0, lastDot); fraction = s.slice(lastDot + 1); }
  else if (lastComma >= 0) { integer = s.slice(0, lastComma); fraction = s.slice(lastComma + 1); }
  if (fraction.length > 2) { integer += fraction; fraction = ""; }
  if (!fraction) fraction = "00";
  if (fraction.length === 1) fraction += "0";
  fraction = fraction.slice(0, 2);
  const v = parseInt(integer || "0", 10) * 100 + parseInt(fraction, 10);
  return neg ? -v : v;
}

// ─── time helpers ────────────────────────────────────────────────

function now(): string { return new Date().toISOString(); }
function monthsAgo(n: number): string {
  const d = new Date();
  d.setMonth(d.getMonth() - n);
  return d.toISOString();
}
