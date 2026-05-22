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

import { useCallback, useEffect, useMemo, useState } from "react";

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

const RANGES = ["1mo", "6mo", "1y", "5y", "max"] as const;
type Range = (typeof RANGES)[number];

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
function changeColor(n: number | undefined): string {
  if (n == null || n === 0) return "var(--text-dim)";
  return n > 0 ? "var(--success)" : "var(--error)";
}

export default function StocksPanel(_props: NativePanelProps) {
  const [selected, setSelected] = useState<string | null>(null);
  return selected ? (
    <Detail symbol={selected} onBack={() => setSelected(null)} />
  ) : (
    <List onOpen={setSelected} />
  );
}

// ─── List view ─────────────────────────────────────────────────────

function List({ onOpen }: { onOpen: (sym: string) => void }) {
  const [stocks, setStocks] = useState<Stock[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [sector, setSector] = useState("");
  const [sort, setSort] = useState("name");
  const [minYield, setMinYield] = useState("");
  const [maxPayout, setMaxPayout] = useState("");
  const [maxPE, setMaxPE] = useState("");
  const [minGrowth, setMinGrowth] = useState("");
  const [query, setQuery] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const p = new URLSearchParams();
      if (sector) p.set("sector", sector);
      if (sort) p.set("sort", sort);
      if (minYield) p.set("min_yield", minYield);
      if (maxPayout) p.set("max_payout", maxPayout);
      if (maxPE) p.set("max_pe", maxPE);
      if (minGrowth) p.set("min_growth", minGrowth);
      const r = await fetch(`${API}/stocks?${p.toString()}`);
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const data = await r.json();
      setStocks(data.stocks ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [sector, sort, minYield, maxPayout, maxPE, minGrowth]);

  useEffect(() => { void load(); }, [load]);

  const sectors = useMemo(
    () => Array.from(new Set(stocks.map((s) => s.sector).filter(Boolean))).sort(),
    [stocks],
  );
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return stocks;
    return stocks.filter((s) => s.symbol.toLowerCase().includes(q) || s.name.toLowerCase().includes(q));
  }, [stocks, query]);

  return (
    <div className="h-full overflow-y-auto p-4">
      <div className="mb-4 flex items-center gap-2">
        <svg {...ico} width={20} height={20} className="text-accent">
          <line x1="3" y1="3" x2="3" y2="21" /><line x1="3" y1="21" x2="21" y2="21" />
          <polyline points="6 14 11 9 14 12 20 6" /><polyline points="20 10 20 6 16 6" />
        </svg>
        <h2 className="text-lg font-semibold text-text">Stocks</h2>
        <span className="text-xs text-text-muted">{shown.length} symbols</span>
      </div>

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="relative">
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Filter symbol or name…"
            className="rounded-md border border-border bg-bg-input px-3 py-1.5 text-sm text-text placeholder:text-text-dim"
          />
        </div>
        <select value={sector} onChange={(e) => setSector(e.target.value)}
          className="rounded-md border border-border bg-bg-input px-2 py-1.5 text-sm text-text">
          <option value="">All sectors</option>
          {sectors.map((s) => <option key={s} value={s}>{s}</option>)}
        </select>
        <select value={sort} onChange={(e) => setSort(e.target.value)}
          className="rounded-md border border-border bg-bg-input px-2 py-1.5 text-sm text-text">
          <option value="name">Sort: Name</option>
          <option value="price">Sort: Price</option>
          <option value="change">Sort: Day change</option>
          <option value="yield">Sort: Yield</option>
          <option value="pe">Sort: P/E (low→high)</option>
          <option value="payout">Sort: Payout (low→high)</option>
          <option value="growth">Sort: Div growth</option>
        </select>
        <NumFilter value={minYield} onChange={setMinYield} placeholder="Min yield %" />
        <NumFilter value={maxPayout} onChange={setMaxPayout} placeholder="Max payout %" />
        <NumFilter value={maxPE} onChange={setMaxPE} placeholder="Max P/E" />
        <NumFilter value={minGrowth} onChange={setMinGrowth} placeholder="Min div growth %" />
        <button onClick={() => void load()}
          className="ml-auto flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm text-bg hover:bg-accent-hover">
          <svg {...ico}><polyline points="23 4 23 10 17 10" /><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10" /></svg>
          Refresh
        </button>
      </div>

      {error && <div className="mb-3 rounded-md border border-error px-3 py-2 text-sm text-error">{error}</div>}

      <div className="overflow-x-auto rounded-lg border border-border bg-bg-card">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border text-xs uppercase tracking-wide text-text-muted">
              <th className="px-3 py-2 text-left">Symbol</th>
              <th className="px-3 py-2 text-left">Name</th>
              <th className="px-3 py-2 text-left">Sector</th>
              <th className="px-3 py-2 text-right">Price</th>
              <th className="px-3 py-2 text-right">Day</th>
              <th className="px-3 py-2 text-right">Yield</th>
              <th className="px-3 py-2 text-right">P/E</th>
              <th className="px-3 py-2 text-right">Payout</th>
              <th className="px-3 py-2 text-right">5Y Gr</th>
            </tr>
          </thead>
          <tbody>
            {loading && stocks.length === 0 && (
              <tr><td colSpan={9} className="px-3 py-8 text-center text-text-muted">Loading the universe…</td></tr>
            )}
            {!loading && shown.length === 0 && (
              <tr><td colSpan={9} className="px-3 py-8 text-center text-text-muted">No stocks match.</td></tr>
            )}
            {shown.map((s) => (
              <tr key={s.symbol} onClick={() => onOpen(s.symbol)}
                className="cursor-pointer border-b border-border-subtle hover:bg-bg-hover">
                <td className="px-3 py-2 font-medium text-text">{s.symbol}</td>
                <td className="px-3 py-2 text-text-muted" style={{ maxWidth: 240, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{s.name}</td>
                <td className="px-3 py-2 text-text-dim">{s.sector}</td>
                <td className="px-3 py-2 text-right tabular-nums text-text">{fmtMoney(s.price, s.currency)}</td>
                <td className="px-3 py-2 text-right tabular-nums" style={{ color: changeColor(s.change_pct) }}>{fmtPct(s.change_pct)}</td>
                <td className="px-3 py-2 text-right tabular-nums text-text-muted">{fmtYield(s.yield_pct)}</td>
                <td className="px-3 py-2 text-right tabular-nums text-text-muted">{s.pe != null ? s.pe.toFixed(1) : "—"}</td>
                <td className="px-3 py-2 text-right tabular-nums text-text-muted">{fmtYield(s.payout_pct)}</td>
                <td className="px-3 py-2 text-right tabular-nums" style={{ color: changeColor(s.growth_pct) }}>{fmtPct(s.growth_pct)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─── Detail view ───────────────────────────────────────────────────

function Detail({ symbol, onBack }: { symbol: string; onBack: () => void }) {
  const [d, setD] = useState<StockDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [range, setRange] = useState<Range>("1y");
  const [chart, setChart] = useState<ChartResp | null>(null);
  const [divs, setDivs] = useState<DividendResp | null>(null);

  useEffect(() => {
    let live = true;
    setError(null);
    fetch(`${API}/stock/${symbol}`).then((r) => r.json()).then((j) => {
      if (!live) return;
      if (j.error) setError(j.error); else setD(j);
    }).catch((e) => live && setError(String(e)));
    fetch(`${API}/dividends/${symbol}`).then((r) => r.json()).then((j) => { if (live && !j.error) setDivs(j); }).catch(() => {});
    return () => { live = false; };
  }, [symbol]);

  useEffect(() => {
    let live = true;
    setChart(null);
    fetch(`${API}/chart/${symbol}?range=${range}`).then((r) => r.json()).then((j) => { if (live && !j.error) setChart(j); }).catch(() => {});
    return () => { live = false; };
  }, [symbol, range]);

  return (
    <div className="h-full overflow-y-auto p-4">
      <button onClick={onBack} className="mb-3 flex items-center gap-1 text-sm text-text-muted hover:text-text">
        <svg {...ico}><polyline points="15 18 9 12 15 6" /></svg> All stocks
      </button>

      {error && <div className="mb-3 rounded-md border border-error px-3 py-2 text-sm text-error">{error}</div>}

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
        <LineChart bars={chart?.bars ?? []} />
      </div>

      {/* Key stats */}
      <div className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-3">
        <Stat label="Day range" value={d ? `${fmtMoney(d.day_low, d.currency)} – ${fmtMoney(d.day_high, d.currency)}` : "—"} />
        <Stat label="52-week range" value={d ? `${fmtMoney(d.fifty_two_week_low, d.currency)} – ${fmtMoney(d.fifty_two_week_high, d.currency)}` : "—"} />
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
        {divs && divs.history.length > 0 ? (
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
            <DividendBars history={divs.history} />
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

// NumFilter — a compact numeric screener input (digits + decimal only).
function NumFilter({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) {
  return (
    <input
      value={value}
      onChange={(e) => onChange(e.target.value.replace(/[^0-9.]/g, ""))}
      placeholder={placeholder}
      className="w-32 rounded-md border border-border bg-bg-input px-3 py-1.5 text-sm text-text placeholder:text-text-dim"
    />
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
function DividendBars({ history }: { history: Dividend[] }) {
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
              <title>{`${y}: $${v.toFixed(2)}/share`}</title>
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
function LineChart({ bars }: { bars: Bar[] }) {
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

  return (
    <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ height: 180, width: "100%" }} role="img">
      <path d={area} fill={color} opacity={0.08} />
      <path d={line} fill="none" stroke={color} strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}
