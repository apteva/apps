// StocksPanel — explore stocks backed by Yahoo Finance.
//
// Master → detail:
//   List   — filter/sort the universe; symbol, price, day change, yield.
//   Detail — quote + key stats, a price chart (range chips), and the
//            dividend history.
//
// Read-only. All data comes from the stocks sidecar's REST mirror, which
// is itself a TTL cache over Yahoo's public /chart endpoint.
//
// Colors that ship into SVG are kept in CSS-variable form (var(--accent)
// etc.) because the dashboard's Tailwind JIT doesn't scan apps/mcp/*/ui/
// — class-based fill/stroke utilities would render as black.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/stocks";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Stock {
  symbol: string;
  name: string;
  exchange: string;
  sector: string;
  currency: string;
  price?: number;
  change_pct?: number;
  yield_pct?: number;
  pe?: number;
  payout_pct?: number;
  growth_pct?: number;
  mcap?: number;
  pinned?: boolean;
}

interface StockDetail {
  symbol: string;
  name: string;
  exchange: string;
  currency: string;
  price: number;
  previous_close: number;
  change: number;
  change_pct: number;
  day_high: number;
  day_low: number;
  fifty_two_week_high: number;
  fifty_two_week_low: number;
  volume: number;
  pe?: number;
  payout_pct?: number;
  mcap?: number;
  dividend_yield_pct?: number;
  dividend_growth_5y_pct?: number;
  dividend_frequency: string;
  last_dividend?: { ex_date: number; amount: number } | null;
}

interface Bar { t: number; o: number; h: number; l: number; c: number; v: number; }
interface ChartResp { symbol: string; range: string; interval: string; currency: string; bars: Bar[]; }

interface Dividend { ex_date: number; amount: number; }
interface DividendResp {
  symbol: string;
  summary: {
    trailing_12mo: number;
    frequency: string;
    payments: number;
    yield_pct?: number;
    growth_pct?: number;
    cagr_5y_pct?: number;
    latest?: { ex_date: number; amount: number };
  };
  history: Dividend[];
}

interface SyncStatus {
  universe: number;
  warmed: number;
  with_price: number;
  with_yield: number;
  with_fundamentals: number;
  fresh: number;
  last_batch?: number;
  newest_refresh?: number;
  batch_running?: boolean;
  batch_total?: number;
  batch_completed?: number;
  fundamentals_state: "ok" | "backoff" | "untried";
  fundamentals_retry_at?: number;
  target_freshness_seconds?: number;
}

interface Rules {
  query?: string; sector?: string; sort?: string;
  min_yield?: number; max_yield?: number;
  min_payout?: number; max_payout?: number;
  min_pe?: number; max_pe?: number;
  min_growth?: number; max_growth?: number;
  min_mcap?: number; max_mcap?: number;
}
interface Watchlist { id: number; name: string; kind: string; rules: Rules; }

interface ListResponse {
  stocks: Stock[];
  count: number;
  total: number;
  offset: number;
  limit: number;
  has_more: boolean;
  sectors?: string[];
  error?: string;
}

async function checkedJSON<T>(url: string, init?: RequestInit): Promise<T> {
  // App routes are backed by their own TTL cache. The browser must always
  // revalidate so live refresh progress and newly-landed rows are visible.
  const response = await fetch(url, { cache: "no-store", ...init });
  const text = await response.text();
  let body: (T & { error?: string }) | null = null;
  try {
    body = text ? JSON.parse(text) : ({} as T & { error?: string });
  } catch {
    throw new Error(`Invalid response (HTTP ${response.status})`);
  }
  if (!response.ok || body?.error) {
    throw new Error(body?.error || `HTTP ${response.status}`);
  }
  return body as T;
}

function appURL(path: string, projectId: string, installId: number, extra?: Record<string, string | number | boolean | undefined>): string {
  const q = new URLSearchParams({ project_id: projectId, install_id: String(installId) });
  for (const [key, value] of Object.entries(extra ?? {})) {
    if (value !== undefined && value !== "") q.set(key, String(value));
  }
  return `${API}${path}?${q.toString()}`;
}

// wlApi — thin REST client for the watchlist endpoints, project-scoped.
function wlApi(projectId: string, installId: number) {
  const json = { "Content-Type": "application/json" };
  return {
    list: (): Promise<{ watchlists: Watchlist[] }> => checkedJSON(appURL("/watchlists", projectId, installId)),
    save: (body: { id?: number; name: string; rules: Rules }): Promise<{ id: number }> =>
      checkedJSON(appURL("/watchlists", projectId, installId), { method: "POST", headers: json, body: JSON.stringify(body) }),
    del: (id: number) => checkedJSON<{ ok: boolean }>(appURL(`/watchlists/${id}`, projectId, installId), { method: "DELETE" }),
    member: (id: number, symbol: string, state: string) =>
      checkedJSON<{ ok: boolean }>(appURL(`/watchlists/${id}/member`, projectId, installId), { method: "POST", headers: json, body: JSON.stringify({ symbol, state }) }),
    get: (id: number, sort: string, offset: number, limit: number, signal?: AbortSignal): Promise<ListResponse> =>
      checkedJSON(appURL(`/watchlists/${id}`, projectId, installId, { sort, offset, limit }), { signal }),
  };
}
type WLApi = ReturnType<typeof wlApi>;

// Market cap is stored + filtered in billions; format compactly.
function fmtMcap(v: number | undefined): string {
  if (v == null || !isFinite(v) || v <= 0) return "—";
  if (v >= 1000) return `$${(v / 1000).toFixed(v >= 10000 ? 0 : 2)}T`;
  if (v >= 1) return `$${v.toFixed(v >= 100 ? 0 : 1)}B`;
  return `$${(v * 1000).toFixed(0)}M`;
}

// Screener metrics, each with a [min,max] domain and a label formatter. A
// handle parked at the domain edge means "no constraint" (shown as "Any").
// Param/rules keys are min_<key> / max_<key>.
const RANGE_DEFS = {
  yield: { min: 0, max: 15, step: 0.25, label: "Dividend yield", fmt: (v: number) => `${v}%` },
  payout: { min: 0, max: 150, step: 5, label: "Payout ratio", fmt: (v: number) => `${v}%` },
  pe: { min: 0, max: 60, step: 1, label: "P/E", fmt: (v: number) => `${v}` },
  growth: { min: -10, max: 30, step: 0.5, label: "5yr dividend growth", fmt: (v: number) => `${v}%` },
  mcap: { min: 0, max: 4000, step: 25, label: "Market cap", fmt: (v: number) => fmtMcap(v) },
} as const;
type MetricKey = keyof typeof RANGE_DEFS;
type Ranges = Record<MetricKey, [number, number]>;
const defaultRanges = (): Ranges => ({ yield: [0, 15], payout: [0, 150], pe: [0, 60], growth: [-10, 30], mcap: [0, 4000] });
const metricKeys = Object.keys(RANGE_DEFS) as MetricKey[];

const RANGES = ["1mo", "6mo", "1y", "5y", "max"] as const;
type Range = (typeof RANGES)[number];
const PAGE_SIZE = 100;

const ico = {
  width: 15,
  height: 15,
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 2,
  strokeLinecap: "round" as const,
  strokeLinejoin: "round" as const,
};

function fmtMoney(n: number | undefined, ccy = "USD"): string {
  if (n == null || !isFinite(n)) return "—";
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: ccy, maximumFractionDigits: n < 10 ? 4 : 2 }).format(n);
  } catch {
    return n.toFixed(2);
  }
}
function fmtPct(n: number | undefined): string {
  if (n == null || !isFinite(n)) return "—";
  return `${n >= 0 ? "+" : ""}${n.toFixed(2)}%`;
}
function fmtYield(n: number | undefined): string {
  if (n == null || !isFinite(n)) return "—";
  return `${n.toFixed(2)}%`;
}
function fmtVolume(n: number | undefined): string {
  if (n == null || !isFinite(n) || n === 0) return "—";
  if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(Math.round(n));
}
function fmtDate(unix: number): string {
  return new Date(unix * 1000).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}
function ago(unix: number): string {
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
function changeColor(n: number | undefined): string {
  if (n == null || n === 0) return "var(--text-dim)";
  return n > 0 ? "var(--success)" : "var(--error)";
}

export default function StocksPanel({ projectId, installId }: NativePanelProps) {
  const [selected, setSelected] = useState<string | null>(null);
  const [watchlists, setWatchlists] = useState<Watchlist[]>([]);
  const api = useMemo(() => wlApi(projectId, installId), [projectId, installId]);
  const url = useCallback((path: string, extra?: Record<string, string | number | boolean | undefined>) =>
    appURL(path, projectId, installId, extra), [projectId, installId]);
  const reloadWL = useCallback((): Promise<void> => {
    return api.list().then((j) => setWatchlists(j.watchlists ?? [])).catch(() => {});
  }, [api]);
  useEffect(() => { reloadWL(); }, [reloadWL]);

  return selected ? (
    <Detail symbol={selected} watchlists={watchlists} api={api} url={url} onBack={() => setSelected(null)} />
  ) : (
    <List onOpen={setSelected} watchlists={watchlists} api={api} url={url} reloadWL={reloadWL} />
  );
}

// ─── List view ─────────────────────────────────────────────────────

function List({ onOpen, watchlists, api, url, reloadWL }: {
  onOpen: (sym: string) => void;
  watchlists: Watchlist[];
  api: WLApi;
  url: (path: string, extra?: Record<string, string | number | boolean | undefined>) => string;
  reloadWL: () => Promise<void>;
}) {
  const [activeWL, setActiveWL] = useState<number | null>(null);
  const [stocks, setStocks] = useState<Stock[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [mutationBusy, setMutationBusy] = useState(false);
  const [sector, setSector] = useState("");
  const [sort, setSort] = useState("name");
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [ranges, setRanges] = useState<Ranges>(defaultRanges);
  const [draftSector, setDraftSector] = useState("");
  const [draftRanges, setDraftRanges] = useState<Ranges>(defaultRanges);
  const setDraftRange = (k: MetricKey, v: [number, number]) => setDraftRanges((r) => ({ ...r, [k]: v }));
  const [query, setQuery] = useState("");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [page, setPage] = useState(0);
  const [total, setTotal] = useState(0);
  const [sectors, setSectors] = useState<string[]>([]);
  const [reloadKey, setReloadKey] = useState(0);

  useEffect(() => {
    const id = window.setTimeout(() => {
      setDebouncedQuery(query.trim());
      setPage(0);
    }, 300);
    return () => window.clearTimeout(id);
  }, [query]);

  useEffect(() => {
    const controller = new AbortController();
    let live = true;
    const run = async () => {
      setLoading(true);
      setError(null);
      try {
        let data: ListResponse;
        if (activeWL != null) {
          data = await api.get(activeWL, sort, page * PAGE_SIZE, PAGE_SIZE, controller.signal);
        } else {
          const extra: Record<string, string | number | boolean | undefined> = {
            q: debouncedQuery, sector, sort, offset: page * PAGE_SIZE, limit: PAGE_SIZE,
          };
          for (const k of metricKeys) {
            const def = RANGE_DEFS[k];
            const [lo, hi] = ranges[k];
            if (lo > def.min) extra[`min_${k}`] = lo;
            if (hi < def.max) extra[`max_${k}`] = hi;
          }
          data = await checkedJSON<ListResponse>(url("/stocks", extra), { signal: controller.signal });
        }
        if (!live) return;
        if (page > 0 && (data.stocks ?? []).length === 0) {
          setPage(data.total > 0 ? Math.floor((data.total - 1) / PAGE_SIZE) : 0);
          return;
        }
        setStocks(data.stocks ?? []);
        setTotal(data.total ?? data.count ?? 0);
        if (data.sectors) setSectors(data.sectors);
      } catch (e) {
        if (!live || (e instanceof DOMException && e.name === "AbortError")) return;
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        if (live) setLoading(false);
      }
    };
    void run();
    return () => { live = false; controller.abort(); };
  }, [activeWL, sector, sort, ranges, api, url, page, debouncedQuery, reloadKey]);

  const [nameOpen, setNameOpen] = useState(false);
  const [nameVal, setNameVal] = useState("");
  const [confirmDel, setConfirmDel] = useState(false);

  // Translate the current screener controls into a rules blob.
  const currentRules = useCallback((): Rules => {
    const rules: Rules = { sort };
    if (query.trim()) rules.query = query.trim();
    if (sector) rules.sector = sector;
    for (const k of metricKeys) {
      const def = RANGE_DEFS[k];
      const [lo, hi] = ranges[k];
      if (lo > def.min) (rules as Record<string, unknown>)[`min_${k}`] = lo;
      if (hi < def.max) (rules as Record<string, unknown>)[`max_${k}`] = hi;
    }
    return rules;
  }, [query, sector, sort, ranges]);

  const doSaveWatchlist = useCallback(async () => {
    const name = nameVal.trim();
    if (!name) return;
    setMutationBusy(true);
    setError(null);
    try {
      const j = await api.save({ name, rules: currentRules() });
      setNameOpen(false);
      setNameVal("");
      await reloadWL();
      if (j.id) { setActiveWL(j.id); setPage(0); }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setMutationBusy(false);
    }
  }, [api, nameVal, currentRules, reloadWL]);

  const doDeleteWatchlist = useCallback(async () => {
    if (activeWL == null) return;
    setMutationBusy(true);
    setError(null);
    try {
      await api.del(activeWL);
      setConfirmDel(false);
      setActiveWL(null);
      setPage(0);
      await reloadWL();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setMutationBusy(false);
    }
  }, [activeWL, api, reloadWL]);

  // Star toggle in a watchlist view: every row shown is a member, so this
  // removes it — drop the include pin if it was pinned, else exclude it.
  const removeFromWatchlist = useCallback(async (s: Stock) => {
    if (activeWL == null) return;
    setMutationBusy(true);
    setError(null);
    try {
      await api.member(activeWL, s.symbol, "exclude");
      setReloadKey((v) => v + 1);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setMutationBusy(false);
    }
  }, [activeWL, api]);

  const activeFilters =
    (sector ? 1 : 0) +
    metricKeys.filter((k) => ranges[k][0] > RANGE_DEFS[k].min || ranges[k][1] < RANGE_DEFS[k].max).length;
  const draftActiveFilters =
    (draftSector ? 1 : 0) +
    metricKeys.filter((k) => draftRanges[k][0] > RANGE_DEFS[k].min || draftRanges[k][1] < RANGE_DEFS[k].max).length;
  const resetDraftFilters = () => {
    setDraftSector("");
    setDraftRanges(defaultRanges());
  };
  const openFilters = () => {
    setDraftSector(sector);
    setDraftRanges(ranges);
    setFiltersOpen(true);
  };
  const closeFilters = useCallback(() => setFiltersOpen(false), []);
  const closeNameModal = useCallback(() => setNameOpen(false), []);
  const closeDeleteModal = useCallback(() => setConfirmDel(false), []);
  const applyFilters = () => {
    setSector(draftSector);
    setRanges(draftRanges);
    setPage(0);
    setFiltersOpen(false);
  };
  const shown = stocks;
  const refreshVisibleStocks = useCallback(() => setReloadKey((v) => v + 1), []);

  return (
    <div className="h-full overflow-y-auto p-4">
      <style>{tableCSS}</style>
      <div className="mb-4 flex items-center gap-2">
        <svg {...ico} width={20} height={20} className="text-accent">
          <line x1="3" y1="3" x2="3" y2="21" /><line x1="3" y1="21" x2="21" y2="21" />
          <polyline points="6 14 11 9 14 12 20 6" /><polyline points="20 10 20 6 16 6" />
        </svg>
        <h2 className="text-lg font-semibold text-text">Stocks</h2>
        <span className="text-xs text-text-muted">{total.toLocaleString()} symbols</span>
        {loading && stocks.length > 0 && <span role="status" className="text-xs text-text-dim">Updating…</span>}
      </div>

      <p className="mb-3 text-xs text-text-muted">Select a stock to view price history, fundamentals, and dividends.</p>

      <SyncBar url={url} onDataChanged={refreshVisibleStocks} />

      {watchlists.length > 0 && (
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <span className="text-xs uppercase tracking-wide text-text-dim">Watchlists</span>
          <Chip label="All stocks" active={activeWL == null} onClick={() => { setActiveWL(null); setPage(0); }} />
          {watchlists.map((w) => (
            <Chip key={w.id} label={w.name} active={activeWL === w.id} onClick={() => { setActiveWL(w.id); setPage(0); }} />
          ))}
        </div>
      )}

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="relative">
          <input
            aria-label="Search stocks"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Filter symbol or name…"
            className="rounded-md border border-border bg-bg-input px-3 py-1.5 text-sm text-text placeholder:text-text-dim"
          />
        </div>
        <select aria-label="Sort stocks" value={sort} onChange={(e) => { setSort(e.target.value); setPage(0); }}
          className="rounded-md border border-border bg-bg-input px-2 py-1.5 text-sm text-text">
          <option value="name">Sort: Name</option>
          <option value="price">Sort: Price</option>
          <option value="change">Sort: Day change</option>
          <option value="yield">Sort: Yield</option>
          <option value="pe">Sort: P/E (low→high)</option>
          <option value="payout">Sort: Payout (low→high)</option>
          <option value="growth">Sort: Div growth</option>
        </select>
        {activeWL == null ? (
          <>
            <button onClick={openFilters}
              className="flex items-center gap-1.5 rounded-md border border-border bg-bg-input px-3 py-1.5 text-sm text-text hover:bg-bg-hover">
              <svg {...ico}><polygon points="22 3 2 3 10 12.46 10 19 14 21 14 12.46 22 3" /></svg>
              Filters
              {activeFilters > 0 && <span className="rounded-full bg-accent px-1.5 text-xs text-bg">{activeFilters}</span>}
            </button>
            <button onClick={() => { setNameVal(""); setNameOpen(true); }}
              className="flex items-center gap-1.5 rounded-md border border-border bg-bg-input px-3 py-1.5 text-sm text-text hover:bg-bg-hover">
              <svg {...ico}><polygon points="12 2 15 9 22 9 16 14 18 21 12 17 6 21 8 14 2 9 9 9 12 2" /></svg>
              {activeFilters > 0 || query.trim() ? "Save screen" : "New manual list"}
            </button>
          </>
        ) : (
          <button onClick={() => setConfirmDel(true)}
            className="flex items-center gap-1.5 rounded-md border border-error px-3 py-1.5 text-sm text-error hover:bg-bg-hover">
            <svg {...ico}><polyline points="3 6 5 6 21 6" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" /></svg>
            Delete list
          </button>
        )}
        <button onClick={() => setReloadKey((v) => v + 1)} disabled={loading}
          className="ml-auto flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm text-bg hover:bg-accent-hover">
          <svg {...ico}><polyline points="23 4 23 10 17 10" /><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10" /></svg>
          Reload
        </button>
      </div>

      <FilterDrawer
        open={filtersOpen} onClose={closeFilters}
        sector={draftSector} setSector={setDraftSector} sectors={sectors}
        ranges={draftRanges} setRange={setDraftRange}
        matchCount={total} activeFilters={draftActiveFilters} onReset={resetDraftFilters} onApply={applyFilters}
      />

      {nameOpen && (
        <Modal title="New watchlist" onClose={closeNameModal}>
          <p className="mb-3 text-xs text-text-muted">
            {activeFilters > 0 || query.trim()
              ? "This saves the current search and filters as a dynamic watchlist."
              : "This creates an empty manual watchlist; add stocks from their detail page."}
          </p>
          <input autoFocus value={nameVal} onChange={(e) => setNameVal(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") void doSaveWatchlist(); }}
            placeholder="e.g. Dividend growers"
            className="mb-4 w-full rounded-md border border-border bg-bg-input px-3 py-2 text-sm text-text placeholder:text-text-dim" />
          <div className="flex justify-end gap-2">
            <button onClick={() => setNameOpen(false)}
              className="rounded-md border border-border px-3 py-1.5 text-sm text-text-muted hover:bg-bg-hover">Cancel</button>
            <button onClick={() => void doSaveWatchlist()} disabled={!nameVal.trim() || mutationBusy}
              className="rounded-md bg-accent px-3 py-1.5 text-sm text-bg hover:bg-accent-hover">Save</button>
          </div>
        </Modal>
      )}
      {confirmDel && (
        <Modal title="Delete watchlist" onClose={closeDeleteModal}>
          <p className="mb-4 text-sm text-text-muted">This removes the watchlist and its pins. The stocks themselves aren't affected.</p>
          <div className="flex justify-end gap-2">
            <button onClick={() => setConfirmDel(false)}
              className="rounded-md border border-border px-3 py-1.5 text-sm text-text-muted hover:bg-bg-hover">Cancel</button>
            <button onClick={() => void doDeleteWatchlist()} disabled={mutationBusy}
              className="rounded-md bg-error px-3 py-1.5 text-sm text-bg">Delete</button>
          </div>
        </Modal>
      )}

      {error && <div role="alert" className="mb-3 rounded-md border border-error px-3 py-2 text-sm text-error">{error}</div>}

      <div className="overflow-x-auto rounded-lg border border-border bg-bg-card">
        <table className="stx-table w-full text-sm">
          <thead>
            <tr className="border-b border-border text-xs uppercase tracking-wide text-text-muted">
              {activeWL != null && <th className="px-2 py-2" />}
              <th className="px-3 py-2 text-left">Symbol</th>
              <th className="stx-col-mid px-3 py-2 text-left">Name</th>
              <th className="stx-col-wide px-3 py-2 text-left">Sector</th>
              <th className="stx-col-wide px-3 py-2 text-right">Mkt Cap</th>
              <th className="px-3 py-2 text-right">Price</th>
              <th className="px-3 py-2 text-right">Day</th>
              <th className="px-3 py-2 text-right">Yield</th>
              <th className="stx-col-mid px-3 py-2 text-right">P/E</th>
              <th className="stx-col-wide px-3 py-2 text-right">Payout</th>
              <th className="stx-col-wide px-3 py-2 text-right">5Y Gr</th>
              <th className="px-3 py-2 text-right" aria-label="Details" />
            </tr>
          </thead>
          <tbody>
            {loading && stocks.length === 0 && (
              <tr><td colSpan={activeWL != null ? 12 : 11} className="px-3 py-8 text-center text-text-muted">Loading…</td></tr>
            )}
            {!loading && shown.length === 0 && (
              <tr><td colSpan={activeWL != null ? 12 : 11} className="px-3 py-8 text-center text-text-muted">{activeWL != null ? "This watchlist is empty." : "No stocks match."}</td></tr>
            )}
            {shown.map((s) => (
              <tr key={s.symbol} tabIndex={0} aria-label={`Open ${s.symbol} details`}
                onClick={(event) => {
                  if (event.target instanceof Element && event.target.closest("button")) return;
                  onOpen(s.symbol);
                }}
                onKeyDown={(event) => {
                  if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
                  event.preventDefault();
                  onOpen(s.symbol);
                }}
                className="cursor-pointer border-b border-border-subtle hover:bg-bg-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent">
                {activeWL != null && (
                  <td className="px-2 py-2">
                    <button type="button" aria-label={`Remove ${s.symbol} from watchlist`} disabled={mutationBusy}
                      onClick={() => void removeFromWatchlist(s)} className="text-accent hover:text-error">
                      <svg {...ico} width={14} height={14} fill="currentColor">
                        <polygon points="12 2 15 9 22 9 16 14 18 21 12 17 6 21 8 14 2 9 9 9 12 2" />
                      </svg>
                    </button>
                  </td>
                )}
                <td className="px-3 py-2 font-medium text-text">
                  <button type="button" onClick={() => onOpen(s.symbol)} className="underline-offset-2 hover:underline">{s.symbol}</button>
                </td>
                <td className="stx-col-mid px-3 py-2 text-text-muted" style={{ maxWidth: 240, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{s.name}</td>
                <td className="stx-col-wide px-3 py-2 text-text-dim">{s.sector}</td>
                <td className="stx-col-wide px-3 py-2 text-right tabular-nums text-text-muted">{fmtMcap(s.mcap)}</td>
                <td className="px-3 py-2 text-right tabular-nums text-text">{fmtMoney(s.price, s.currency)}</td>
                <td className="px-3 py-2 text-right tabular-nums" style={{ color: changeColor(s.change_pct) }}>{fmtPct(s.change_pct)}</td>
                <td className="px-3 py-2 text-right tabular-nums text-text-muted">{fmtYield(s.yield_pct)}</td>
                <td className="stx-col-mid px-3 py-2 text-right tabular-nums text-text-muted">{s.pe != null ? s.pe.toFixed(1) : "—"}</td>
                <td className="stx-col-wide px-3 py-2 text-right tabular-nums text-text-muted">{fmtYield(s.payout_pct)}</td>
                <td className="stx-col-wide px-3 py-2 text-right tabular-nums" style={{ color: changeColor(s.growth_pct) }}>{fmtPct(s.growth_pct)}</td>
                <td className="px-3 py-2 text-right">
                  <button type="button" onClick={() => onOpen(s.symbol)} aria-label={`View ${s.symbol} price history, fundamentals, and dividends`}
                    className="inline-flex items-center gap-1 text-xs text-accent hover:underline">
                    View <svg {...ico} width={12} height={12}><polyline points="9 18 15 12 9 6" /></svg>
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {total > PAGE_SIZE && (
        <div className="mt-3 flex items-center justify-between text-sm text-text-muted">
          <span>{page * PAGE_SIZE + 1}–{Math.min((page + 1) * PAGE_SIZE, total)} of {total.toLocaleString()}</span>
          <div className="flex gap-2">
            <button type="button" disabled={page === 0 || loading} onClick={() => setPage((p) => Math.max(0, p - 1))}
              className="rounded-md border border-border px-3 py-1.5 disabled:opacity-50">Previous</button>
            <button type="button" disabled={(page + 1) * PAGE_SIZE >= total || loading} onClick={() => setPage((p) => p + 1)}
              className="rounded-md border border-border px-3 py-1.5 disabled:opacity-50">Next</button>
          </div>
        </div>
      )}
    </div>
  );
}

// ─── Detail view ───────────────────────────────────────────────────

function Detail({ symbol, onBack, watchlists, api, url }: {
  symbol: string; onBack: () => void; watchlists: Watchlist[]; api: WLApi;
  url: (path: string, extra?: Record<string, string | number | boolean | undefined>) => string;
}) {
  const [d, setD] = useState<StockDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [detailLoading, setDetailLoading] = useState(true);
  const [range, setRange] = useState<Range>("1y");
  const [chart, setChart] = useState<ChartResp | null>(null);
  const [chartLoading, setChartLoading] = useState(true);
  const [divs, setDivs] = useState<DividendResp | null>(null);
  const [divsLoading, setDivsLoading] = useState(true);
  const [addMsg, setAddMsg] = useState("");
  const [refreshKey, setRefreshKey] = useState(0);

  const addTo = async (id: number) => {
    try {
      const wl = watchlists.find((w) => w.id === id);
      await api.member(id, symbol, "include");
      setAddMsg(`Added to ${wl?.name ?? "watchlist"}`);
      window.setTimeout(() => setAddMsg(""), 2500);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  useEffect(() => {
    const controller = new AbortController();
    let live = true;
    setError(null);
    setDetailLoading(true);
    setDivsLoading(true);
    setD(null);
    setDivs(null);
    const force = refreshKey > 0;
    void checkedJSON<StockDetail>(url(`/stock/${encodeURIComponent(symbol)}`, { refresh: force }), { signal: controller.signal })
      .then((value) => { if (live) setD(value); })
      .catch((e) => { if (live && !(e instanceof DOMException && e.name === "AbortError")) setError(e instanceof Error ? e.message : String(e)); })
      .finally(() => { if (live) setDetailLoading(false); });
    void checkedJSON<DividendResp>(url(`/dividends/${encodeURIComponent(symbol)}`, { refresh: force }), { signal: controller.signal })
      .then((value) => { if (live) setDivs(value); })
      .catch((e) => { if (live && !(e instanceof DOMException && e.name === "AbortError")) setError(e instanceof Error ? e.message : String(e)); })
      .finally(() => { if (live) setDivsLoading(false); });
    return () => { live = false; controller.abort(); };
  }, [symbol, refreshKey, url]);

  useEffect(() => {
    const controller = new AbortController();
    let live = true;
    setChart(null);
    setChartLoading(true);
    void checkedJSON<ChartResp>(url(`/chart/${encodeURIComponent(symbol)}`, { range, refresh: refreshKey > 0 }), { signal: controller.signal })
      .then((value) => { if (live) setChart(value); })
      .catch((e) => { if (live && !(e instanceof DOMException && e.name === "AbortError")) setError(e instanceof Error ? e.message : String(e)); })
      .finally(() => { if (live) setChartLoading(false); });
    return () => { live = false; controller.abort(); };
  }, [symbol, range, refreshKey, url]);

  return (
    <div className="h-full overflow-y-auto p-4">
      <div className="mb-3 flex items-center gap-3">
        <button onClick={onBack} className="flex items-center gap-1 text-sm text-text-muted hover:text-text">
          <svg {...ico}><polyline points="15 18 9 12 15 6" /></svg> All stocks
        </button>
        {addMsg && <span role="status" className="text-xs text-success">{addMsg}</span>}
        <button type="button" onClick={() => setRefreshKey((v) => v + 1)} disabled={detailLoading || chartLoading || divsLoading}
          className="ml-auto rounded-md border border-border px-2 py-1 text-xs text-text-muted hover:bg-bg-hover disabled:opacity-50">
          Refresh data
        </button>
        {watchlists.length > 0 && (
          <select value="" onChange={(e) => { if (e.target.value) void addTo(Number(e.target.value)); }}
            aria-label="Add stock to watchlist"
            className="rounded-md border border-border bg-bg-input px-2 py-1 text-xs text-text">
            <option value="">+ Add to watchlist…</option>
            {watchlists.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
          </select>
        )}
      </div>

      {error && <div role="alert" className="mb-3 rounded-md border border-error px-3 py-2 text-sm text-error">{error}</div>}

      <div className="mb-4 flex items-baseline gap-3">
        <h2 className="text-xl font-semibold text-text">{symbol}</h2>
        <span className="text-text-muted">{d?.name}</span>
        {d?.exchange && <span className="text-xs text-text-dim">{d.exchange}</span>}
      </div>

      <div className="mb-4 flex items-baseline gap-3">
        <div className="text-3xl font-semibold tabular-nums text-text">{fmtMoney(d?.price, d?.currency)}</div>
        {d && (
          <div className="text-sm tabular-nums" style={{ color: changeColor(d.change_pct) }}>
            {fmtMoney(d.change, d.currency)} ({fmtPct(d.change_pct)})
          </div>
        )}
      </div>

      {/* Chart */}
      <div className="mb-4 rounded-lg border border-border bg-bg-card p-4">
        <div className="mb-3 flex items-center justify-between">
          <span className="text-xs uppercase tracking-wide text-text-muted">Price</span>
          <div className="flex overflow-hidden rounded-md border border-border text-xs">
            {RANGES.map((rr) => (
              <button key={rr} onClick={() => setRange(rr)}
                className={`px-2.5 py-1 ${rr === range ? "bg-accent text-bg" : "text-text-muted hover:bg-bg-hover"}`}>
                {rr}
              </button>
            ))}
          </div>
        </div>
        {chartLoading ? (
          <div className="flex items-center justify-center text-sm text-text-muted" style={{ height: 180 }}>Loading chart…</div>
        ) : <LineChart bars={chart?.bars ?? []} currency={chart?.currency ?? d?.currency} />}
      </div>

      {/* Key stats */}
      <div className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-3">
        <Stat label="Day range" value={d ? `${fmtMoney(d.day_low, d.currency)} – ${fmtMoney(d.day_high, d.currency)}` : "—"} />
        <Stat label="52-week range" value={d ? `${fmtMoney(d.fifty_two_week_low, d.currency)} – ${fmtMoney(d.fifty_two_week_high, d.currency)}` : "—"} />
        <Stat label="Market cap" value={fmtMcap(d?.mcap)} />
        <Stat label="Volume" value={fmtVolume(d?.volume)} />
        <Stat label="P/E (TTM)" value={d?.pe != null ? d.pe.toFixed(1) : "—"} />
        <Stat label="Payout ratio" value={fmtYield(d?.payout_pct)} />
        <Stat label="Dividend yield" value={fmtYield(d?.dividend_yield_pct)} />
        <Stat label="Frequency" value={d?.dividend_frequency ?? "—"} />
        <Stat label="Previous close" value={fmtMoney(d?.previous_close, d?.currency)} />
      </div>

      {/* Dividends */}
      <div className="rounded-lg border border-border bg-bg-card p-4">
        <div className="mb-3 flex items-center gap-2">
          <svg {...ico} className="text-accent"><circle cx="12" cy="12" r="9" /><path d="M12 8v8M9.5 10.5a2.5 2.5 0 0 1 2.5-2 2 2 0 0 1 0 4 2 2 0 0 0 0 4 2.5 2.5 0 0 1-2.5-2" /></svg>
          <span className="text-xs uppercase tracking-wide text-text-muted">Dividends</span>
        </div>
        {divsLoading ? (
          <div className="py-6 text-center text-sm text-text-muted">Loading dividend history…</div>
        ) : divs && divs.history.length > 0 ? (
          <>
            <div className="mb-3 grid grid-cols-2 gap-3 sm:grid-cols-3">
              <Stat label="TTM / share" value={fmtMoney(divs.summary.trailing_12mo, d?.currency)} />
              <Stat label="Yield" value={fmtYield(divs.summary.yield_pct)} />
              <Stat label="Frequency" value={divs.summary.frequency} />
              <Stat label="YoY growth" value={fmtPct(divs.summary.growth_pct)} />
              <Stat label="5yr growth (CAGR)" value={fmtPct(divs.summary.cagr_5y_pct)} />
              <Stat label="Payments" value={String(divs.summary.payments)} />
            </div>
            <div className="mb-1 text-xs uppercase tracking-wide text-text-dim">Dividend / share, by year</div>
            <DividendBars history={divs.history} currency={d?.currency} />
            <div className="max-h-64 overflow-y-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border text-xs uppercase tracking-wide text-text-muted">
                    <th className="px-2 py-1.5 text-left">Ex-date</th>
                    <th className="px-2 py-1.5 text-right">Amount</th>
                  </tr>
                </thead>
                <tbody>
                  {divs.history.map((dv) => (
                    <tr key={dv.ex_date} className="border-b border-border-subtle">
                      <td className="px-2 py-1.5 text-text-muted">{fmtDate(dv.ex_date)}</td>
                      <td className="px-2 py-1.5 text-right tabular-nums text-text">{fmtMoney(dv.amount, d?.currency)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        ) : (
          <div className="py-6 text-center text-sm text-text-muted">No dividend history.</div>
        )}
      </div>
    </div>
  );
}

// SyncBar — live stock-refresh progress. Poll quickly during an active batch
// and refresh the visible rows whenever new market data lands.
function SyncBar({ url, onDataChanged }: {
  url: (path: string, extra?: Record<string, string | number | boolean | undefined>) => string;
  onDataChanged: () => void;
}) {
  const [s, setS] = useState<SyncStatus | null>(null);
  const dataSignature = useRef<string | null>(null);
  useEffect(() => {
    let live = true;
    let timer: number | undefined;
    let polling = false;

    const schedule = (delay: number) => {
      if (timer !== undefined) window.clearTimeout(timer);
      timer = window.setTimeout(() => { void poll(); }, delay);
    };
    const poll = async () => {
      if (!live || polling) return;
      if (document.hidden) {
        schedule(30000);
        return;
      }
      polling = true;
      try {
        const next = await checkedJSON<SyncStatus>(url("/status"));
        if (!live) return;
        setS(next);
        const signature = `${next.with_price}:${next.with_fundamentals}:${next.newest_refresh ?? 0}`;
        if (dataSignature.current !== null && dataSignature.current !== signature) onDataChanged();
        dataSignature.current = signature;
        schedule(next.batch_running ? 3000 : next.with_price < next.universe ? 10000 : 60000);
      } catch {
        if (live) schedule(15000);
      } finally {
        polling = false;
      }
    };
    const onVisibility = () => {
      if (!document.hidden) {
        if (timer !== undefined) window.clearTimeout(timer);
        void poll();
      }
    };

    void poll();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      live = false;
      if (timer !== undefined) window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [url, onDataChanged]);
  if (!s || !s.universe) return null;

  const pct = Math.round((s.with_price / s.universe) * 100);
  const synced = s.with_price >= s.universe;
  const fund =
    s.fundamentals_state === "ok" ? "P/E feed live"
    : s.fundamentals_state === "backoff" ? "P/E feed throttled — retrying"
    : "P/E feed pending";

  return (
    <div className="mb-3 rounded-md border border-border bg-bg-card px-3 py-2">
      <div className="mb-1 flex flex-wrap items-center justify-between gap-x-3 text-xs text-text-muted">
        <span>
          {synced ? "Stocks ready" : "Refreshing stocks…"}{" "}
          <span className="tabular-nums text-text">{s.with_price.toLocaleString()}</span> / {s.universe.toLocaleString()} with price data
          {" · "}<span className="tabular-nums">{s.with_fundamentals.toLocaleString()}</span> with P/E data
          {" · "}<span className="tabular-nums">{s.fresh.toLocaleString()}</span> recently refreshed
        </span>
        <span className="text-text-dim">
          {s.batch_running && s.batch_total
            ? `Batch ${(s.batch_completed ?? 0).toLocaleString()} / ${s.batch_total.toLocaleString()} · `
            : ""}
          {fund}{s.newest_refresh ? ` · data updated ${ago(s.newest_refresh)}` : ""}
        </span>
      </div>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-bg-hover">
        <div className="h-full rounded-full" style={{ width: `${pct}%`, backgroundColor: "var(--accent)" }} />
      </div>
    </div>
  );
}

// rangeCSS themes the dual-handle range. Two native range inputs overlap a
// shared track; pointer-events:none on the inputs + auto on the thumbs lets
// both handles be grabbed (the .track/.fill divs draw the visible bar, so
// accent-color's bright default track never shows). Injected by FilterDrawer.
const rangeCSS = `
.stx-dual{position:relative;height:24px}
.stx-track{position:absolute;top:10px;left:0;right:0;height:4px;border-radius:9999px;background:var(--bg-hover)}
.stx-fill{position:absolute;top:10px;height:4px;border-radius:9999px;background:var(--accent)}
.stx-dual input[type=range]{position:absolute;top:0;left:0;width:100%;height:24px;margin:0;background:none;pointer-events:none;-webkit-appearance:none;appearance:none}
.stx-dual input[type=range]:focus{outline:none}
.stx-dual input[type=range]::-webkit-slider-runnable-track{height:24px;background:transparent}
.stx-dual input[type=range]::-webkit-slider-thumb{pointer-events:auto;-webkit-appearance:none;appearance:none;width:14px;height:14px;margin-top:5px;border-radius:9999px;background:var(--accent);border:2px solid var(--bg-card);cursor:pointer}
.stx-dual input[type=range]::-moz-range-track{height:24px;background:transparent}
.stx-dual input[type=range]::-moz-range-thumb{pointer-events:auto;width:14px;height:14px;border:2px solid var(--bg-card);border-radius:9999px;background:var(--accent);cursor:pointer}
`;

const tableCSS = `
.stx-table thead{position:sticky;top:0;z-index:1;background:var(--bg-card)}
@media(max-width:900px){.stx-col-wide{display:none}}
@media(max-width:640px){.stx-col-mid{display:none}.stx-table th,.stx-table td{padding-left:.5rem;padding-right:.5rem}}
`;

// DualRange — a min/max screener control. A handle at the domain edge means
// no constraint on that side (shown as "Any"). Handles can't cross.
function DualRange({ label, value, set, min, max, step, fmt }: {
  label: string; value: [number, number]; set: (v: [number, number]) => void;
  min: number; max: number; step: number; fmt: (v: number) => string;
}) {
  const [lo, hi] = value;
  const pct = (v: number) => ((v - min) / (max - min)) * 100;
  const fmtEnd = (v: number, atEdge: boolean) => (atEdge ? "Any" : fmt(v));
  return (
    <div className="mb-5">
      <div className="mb-1 flex items-center justify-between text-xs">
        <span className="text-text-muted">{label}</span>
        <span className="tabular-nums text-text">{fmtEnd(lo, lo <= min)} – {fmtEnd(hi, hi >= max)}</span>
      </div>
      <div className="stx-dual">
        <div className="stx-track" />
        <div className="stx-fill" style={{ left: `${pct(lo)}%`, width: `${Math.max(0, pct(hi) - pct(lo))}%` }} />
        <input aria-label={`${label} minimum`} type="range" min={min} max={max} step={step} value={lo}
          onChange={(e) => set([Math.min(parseFloat(e.target.value), hi), hi])} />
        <input aria-label={`${label} maximum`} type="range" min={min} max={max} step={step} value={hi}
          onChange={(e) => set([lo, Math.max(parseFloat(e.target.value), lo)])} />
      </div>
    </div>
  );
}

// FilterDrawer — right-side slide-over with the screener controls. Driven
// by the same query params as the list; positioned with inline styles
// (fixed overlay) since arbitrary Tailwind positions don't apply here.
function FilterDrawer(props: {
  open: boolean; onClose: () => void;
  sector: string; setSector: (v: string) => void; sectors: string[];
  ranges: Ranges; setRange: (k: MetricKey, v: [number, number]) => void;
  matchCount: number; activeFilters: number; onReset: () => void; onApply: () => void;
}) {
  const drawerRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!props.open) return;
    const previous = document.activeElement as HTMLElement | null;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") props.onClose();
      if (event.key !== "Tab" || !drawerRef.current) return;
      const nodes = Array.from(drawerRef.current.querySelectorAll<HTMLElement>("button,input,select,[tabindex]:not([tabindex='-1'])"))
        .filter((node) => !node.hasAttribute("disabled"));
      if (nodes.length === 0) return;
      const first = nodes[0], last = nodes[nodes.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", onKey);
    return () => { window.removeEventListener("keydown", onKey); previous?.focus(); };
  }, [props.open, props.onClose]);
  if (!props.open) return null;
  return (
    <>
      <div aria-hidden="true" onClick={props.onClose} style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,0.45)", zIndex: 40 }} />
      <div ref={drawerRef} className="border-l border-border bg-bg-card p-4"
        role="dialog" aria-modal="true" aria-label="Stock filters"
        style={{ position: "fixed", top: 0, right: 0, bottom: 0, width: 320, maxWidth: "92vw", zIndex: 50, overflowY: "auto" }}>
        <style>{rangeCSS}</style>
        <div className="mb-4 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-text">Filters</h3>
          <button autoFocus aria-label="Close filters" onClick={props.onClose} className="text-text-muted hover:text-text">
            <svg {...ico}><path d="M18 6 6 18M6 6l12 12" /></svg>
          </button>
        </div>

        <div className="mb-4">
          <div className="mb-1 text-xs text-text-muted">Sector</div>
          <select value={props.sector} onChange={(e) => props.setSector(e.target.value)}
            className="w-full rounded-md border border-border bg-bg-input px-2 py-1.5 text-sm text-text">
            <option value="">All sectors</option>
            {props.sectors.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </div>

        {metricKeys.map((k) => {
          const def = RANGE_DEFS[k];
          return (
            <DualRange key={k} label={def.label} fmt={def.fmt}
              min={def.min} max={def.max} step={def.step}
              value={props.ranges[k]} set={(v) => props.setRange(k, v)} />
          );
        })}

        <div className="mt-5 flex items-center justify-between border-t border-border-subtle pt-3">
          <span className="text-xs text-text-muted">{props.matchCount.toLocaleString()} current matches · apply to update</span>
          <div className="flex gap-2">
            <button onClick={props.onReset} disabled={props.activeFilters === 0}
              className="rounded-md border border-border px-3 py-1.5 text-sm text-text-muted hover:bg-bg-hover">Reset</button>
            <button onClick={props.onApply}
              className="rounded-md bg-accent px-3 py-1.5 text-sm text-bg hover:bg-accent-hover">Done</button>
          </div>
        </div>
      </div>
    </>
  );
}

// Chip — a watchlist selector pill (active = accent fill).
function Chip({ label, active, onClick }: { label: string; active: boolean; onClick: () => void }) {
  return (
    <button onClick={onClick}
      className={`rounded-full border px-3 py-1 text-xs ${active ? "border-accent bg-accent text-bg" : "border-border bg-bg-input text-text hover:bg-bg-hover"}`}>
      {label}
    </button>
  );
}

// Modal — centered dialog over a dimmed backdrop (real modal, not a
// browser prompt/confirm). Positioned with inline styles since the panel's
// Tailwind can't generate arbitrary fixed-position utilities.
function Modal({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
      if (event.key !== "Tab" || !dialogRef.current) return;
      const nodes = Array.from(dialogRef.current.querySelectorAll<HTMLElement>("button,input,select,[tabindex]:not([tabindex='-1'])"))
        .filter((node) => !node.hasAttribute("disabled"));
      if (nodes.length === 0) return;
      const first = nodes[0], last = nodes[nodes.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", onKey);
    return () => { window.removeEventListener("keydown", onKey); previous?.focus(); };
  }, [onClose]);
  return (
    <>
      <div aria-hidden="true" onClick={onClose} style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,0.5)", zIndex: 60 }} />
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby="stocks-modal-title"
        className="rounded-lg border border-border bg-bg-card p-4"
        style={{ position: "fixed", top: "50%", left: "50%", transform: "translate(-50%,-50%)", width: 360, maxWidth: "90%", zIndex: 70 }}>
        <h3 id="stocks-modal-title" className="mb-3 text-sm font-semibold text-text">{title}</h3>
        {children}
      </div>
    </>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border border-border-subtle bg-bg p-2.5">
      <div className="text-xs uppercase tracking-wide text-text-dim">{label}</div>
      <div className="mt-0.5 text-sm tabular-nums text-text">{value}</div>
    </div>
  );
}

// Dividend history as annual dividend-per-share bars (summed per calendar
// year) — the conventional view, far clearer than one bar per payment for
// monthly/quarterly payers. The current, incomplete year is dropped so
// its short bar doesn't break the trend; hover a bar for the exact total.
// Heights use inline style: the dashboard's Tailwind JIT doesn't generate
// arbitrary h-[…] utilities for panel files.
function DividendBars({ history, currency }: { history: Dividend[]; currency?: string }) {
  const years = useMemo(() => {
    const byYear = new Map<number, number>();
    for (const d of history) {
      const y = new Date(d.ex_date * 1000).getFullYear();
      byYear.set(y, (byYear.get(y) ?? 0) + d.amount);
    }
    const cur = new Date().getFullYear();
    return [...byYear.entries()].filter(([y]) => y < cur).sort((a, b) => a[0] - b[0]).slice(-18);
  }, [history]);
  if (years.length < 2) return null;

  const W = 600, H = 150, padX = 2, padTop = 10, baseY = H - 2;
  const plotH = baseY - padTop;
  const max = Math.max(...years.map(([, v]) => v)) || 1;
  const bw = (W - 2 * padX) / years.length;

  return (
    <div className="mb-3">
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ height: 150, width: "100%" }} role="img">
        <line x1={padX} y1={baseY} x2={W - padX} y2={baseY} stroke="var(--border)" strokeWidth={1} />
        {years.map(([y, v], i) => {
          const h = (v / max) * plotH;
          return (
            <rect key={y} x={padX + i * bw + bw * 0.16} y={baseY - h}
              width={bw * 0.68} height={h} fill="var(--accent)" rx={1}>
              <title>{`${y}: ${fmtMoney(v, currency)}/share`}</title>
            </rect>
          );
        })}
      </svg>
      <div className="mt-1 flex justify-between text-xs text-text-dim">
        <span>{years[0][0]}</span>
        <span>{years[years.length - 1][0]}</span>
      </div>
    </div>
  );
}

// SVG line chart. Colored by net change over the window (green up, red
// down). Width is responsive via viewBox + preserveAspectRatio.
function LineChart({ bars, currency }: { bars: Bar[]; currency?: string }) {
  const [hover, setHover] = useState<number | null>(null);
  const W = 600, H = 180, pad = 4;
  if (!bars || bars.length < 2) {
    return <div className="flex items-center justify-center text-sm text-text-muted" style={{ height: 180 }}>No price data.</div>;
  }
  const closes = bars.map((b) => b.c);
  const min = Math.min(...closes), max = Math.max(...closes);
  const span = max - min || 1;
  const up = closes[closes.length - 1] >= closes[0];
  const color = up ? "var(--success)" : "var(--error)";

  const x = (i: number) => pad + (i / (bars.length - 1)) * (W - 2 * pad);
  const y = (c: number) => pad + (1 - (c - min) / span) * (H - 2 * pad);
  const line = bars.map((b, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(1)},${y(b.c).toFixed(1)}`).join(" ");
  const area = `${line} L${x(bars.length - 1).toFixed(1)},${H - pad} L${x(0).toFixed(1)},${H - pad} Z`;
  const hovered = hover == null ? null : bars[hover];

  return (
    <div className="relative">
      <div className="mb-1 flex justify-between text-xs text-text-dim">
        <span>{fmtMoney(min, currency)}</span><span>{fmtMoney(max, currency)}</span>
      </div>
      <div className="relative" style={{ height: 180 }}>
        <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ height: 180, width: "100%" }}
          role="img" aria-label={`Price history from ${fmtDate(bars[0].t)} to ${fmtDate(bars[bars.length - 1].t)}`}
          onMouseMove={(event) => {
            const rect = event.currentTarget.getBoundingClientRect();
            const ratio = Math.max(0, Math.min(1, (event.clientX - rect.left) / rect.width));
            setHover(Math.round(ratio * (bars.length - 1)));
          }}
          onMouseLeave={() => setHover(null)}>
          {[0.25, 0.5, 0.75].map((ratio) => <line key={ratio} x1={0} x2={W} y1={H * ratio} y2={H * ratio} stroke="var(--border)" opacity={0.45} />)}
          <path d={area} fill={color} opacity={0.08} />
          <path d={line} fill="none" stroke={color} strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
          {hovered && hover != null && (
            <line x1={x(hover)} x2={x(hover)} y1={0} y2={H} stroke="var(--text-muted)"
              strokeDasharray="3 3" vectorEffect="non-scaling-stroke" />
          )}
        </svg>
        {hovered && hover != null && (
          <span aria-hidden="true" className="pointer-events-none absolute rounded-full"
            style={{
              width: 8, height: 8,
              left: `${(x(hover) / W) * 100}%`,
              top: `${(y(hovered.c) / H) * 100}%`,
              transform: "translate(-50%, -50%)",
              backgroundColor: color,
              boxShadow: "0 0 0 2px var(--bg-card)",
            }} />
        )}
      </div>
      <div className="mt-1 flex justify-between text-xs text-text-dim">
        <span>{fmtDate(bars[0].t)}</span><span>{fmtDate(bars[bars.length - 1].t)}</span>
      </div>
      {hovered && (
        <div className="pointer-events-none absolute rounded-md border border-border bg-bg-card px-2 py-1 text-xs text-text"
          style={{ top: 28, left: `${Math.max(8, Math.min(82, ((hover ?? 0) / (bars.length - 1)) * 100))}%`, transform: "translateX(-50%)" }}>
          <div>{fmtDate(hovered.t)}</div><div className="font-medium">{fmtMoney(hovered.c, currency)}</div>
        </div>
      )}
    </div>
  );
}
