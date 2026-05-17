// TradingPanel — native React port of the trading app's dashboard panel.
// Replaces the legacy iframe-mounted ui/panel/TradingPanel.html. Talks
// to /api/apps/trading/* through the platform proxy (the dashboard
// injects the per-install bearer token).
//
// Tabs: Portfolios | Trade | Positions | Brokers | Journal.
//
// All v1 features (portfolio CRUD, place + cancel orders, watchlist,
// broker browser, journal reads, account stats) ship in one bundle.
// The richer SPA at /ui/desk/ stays for charts + multi-portfolio
// dashboards.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// ─── Inlined SDK app-event subscription ────────────────────────────
// Same boilerplate StoragePanel inlines. Panels are runtime-bundled
// standalone .mjs files and apps install independently, so shared
// hooks across app directories would break standalone installs.

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
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) return bridge.subscribe(app, projectId, handler);
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
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

// ─── Types ─────────────────────────────────────────────────────────

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Portfolio {
  id: number;
  name: string;
  agent_id?: string;
  mandate: string;
  allowed_classes: string[];
  starting_cash: number;
  cash: number;
  status: string;
  mode: string;
  broker_slug?: string;
  equity?: number;
  day_pnl?: number;
  day_pnl_pct?: number;
  open_pnl?: number;
  open_pnl_pct?: number;
  buying_power?: number;
  watchlist?: string[];
}

interface Position {
  symbol: string;
  asset_class: string;
  outcome?: string;
  qty: number;
  avg_cost: number;
  market_price: number;
  market_value: number;
  unrealized_pnl: number;
  unrealized_pnl_pct: number;
  realized_pnl: number;
  weight_pct: number;
}

interface Order {
  id: string;
  portfolio_id: number;
  symbol: string;
  asset_class: string;
  side: string;
  type: string;
  qty: number;
  filled_qty: number;
  avg_fill_price?: number;
  limit_price?: number;
  stop_price?: number;
  tif: string;
  status: string;
  rationale: string;
  source: string;
  rejection_code?: string;
  rejection_detail?: string;
  placed_at: string;
  resolved_at?: string;
}

interface JournalEntry {
  id: number;
  portfolio_id: number;
  kind: string;
  body: string;
  metadata?: Record<string, unknown>;
  created_at: string;
}

interface BrokerInfo {
  slug: string;
  asset_classes: string[];
  order_types: string[];
  tifs: string[];
  fractional: boolean;
  quote: string;
  bound?: boolean;
  connections: { id: number; name: string; status: string }[];
}

interface Mark {
  symbol: string;
  asset_class: string;
  price?: number;
  yes_price?: number;
  no_price?: number;
  prev_close?: number;
  change_pct_24h?: number;
  volume_24h?: number;
  marked_at: string;
}

interface Bar {
  t: number;            // unix seconds
  o?: number; h?: number; l?: number; c?: number; v?: number;
  yes?: number;         // polymarket YES probability
}
interface HistoryResp {
  symbol: string;
  range: string;
  bars: Bar[];
}

const CHART_RANGES = ["1D", "5D", "1M", "3M", "1Y", "ALL"] as const;
type ChartRange = typeof CHART_RANGES[number];

// ─── Helpers ───────────────────────────────────────────────────────

const API = "/api/apps/trading";

function formatUSD(n: number | undefined | null): string {
  if (n == null || !isFinite(n)) return "—";
  const abs = Math.abs(n);
  if (abs >= 1_000_000) return `$${(n / 1_000_000).toFixed(2)}M`;
  if (abs >= 10_000) return `$${(n / 1_000).toFixed(1)}k`;
  return `$${n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}
function formatPct(n: number | undefined | null, digits = 2): string {
  if (n == null || !isFinite(n)) return "—";
  const sign = n > 0 ? "+" : "";
  return `${sign}${n.toFixed(digits)}%`;
}
function formatQty(n: number | undefined | null, digits = 4): string {
  if (n == null || !isFinite(n)) return "—";
  if (Math.abs(n) >= 100) return n.toFixed(2);
  return n.toFixed(digits).replace(/\.?0+$/, "");
}
function formatPrice(n: number | undefined, assetClass: string): string {
  if (n == null || !isFinite(n)) return "—";
  if (assetClass === "polymarket") return `${(n * 100).toFixed(1)}¢`;
  return `$${n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 4 })}`;
}
function classColor(c: string): string {
  switch (c) {
    case "crypto": return "var(--apteva-amber, #d97706)";
    case "equity":
    case "etf": return "var(--apteva-blue, #2563eb)";
    case "polymarket": return "var(--apteva-purple, #7c3aed)";
    default: return "var(--apteva-text-muted, #6b7280)";
  }
}
function relTime(iso: string | undefined): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (!isFinite(t)) return "";
  const seconds = Math.floor((Date.now() - t) / 1000);
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86_400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86_400)}d ago`;
}

// ─── Icons (SVG, theme-aware via currentColor) ─────────────────────

const Icon = {
  Plus: () => (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round">
      <path d="M12 5v14M5 12h14" />
    </svg>
  ),
  X: () => (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
      <path d="M18 6 6 18M6 6l12 12" />
    </svg>
  ),
  Pause: () => (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor">
      <rect x="6" y="5" width="4" height="14" rx="1" />
      <rect x="14" y="5" width="4" height="14" rx="1" />
    </svg>
  ),
  Play: () => (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor">
      <path d="M8 5v14l11-7z" />
    </svg>
  ),
  Refresh: () => (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 12a9 9 0 0 1 15-6.7L21 8" />
      <path d="M21 3v5h-5" />
      <path d="M21 12a9 9 0 0 1-15 6.7L3 16" />
      <path d="M3 21v-5h5" />
    </svg>
  ),
  ExternalLink: () => (
    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M15 3h6v6" />
      <path d="M10 14 21 3" />
      <path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5" />
    </svg>
  ),
  Trash: () => (
    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 6h18" /><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
      <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
    </svg>
  ),
  Dot: ({ color }: { color: string }) => (
    <svg width="8" height="8" viewBox="0 0 8 8"><circle cx="4" cy="4" r="3" fill={color} /></svg>
  ),
};

// ─── Chart components (hand-rolled SVG, no chart-lib dep) ────────
//
// Same pattern as ui/desk/src/components/PriceChart: build a normalized
// [0..1] series, project into the SVG viewbox, draw a single polyline
// + area fill. Light, theme-aware via currentColor + CSS vars.

function barValue(b: Bar): number {
  if (b.yes != null) return b.yes;
  if (b.c != null) return b.c;
  if (b.o != null) return b.o;
  return 0;
}

function PriceChart({ symbol, assetClass, api }: {
  symbol: string;
  assetClass: string;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
}) {
  const [range, setRange] = useState<ChartRange>("1D");
  const [bars, setBars] = useState<Bar[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!symbol) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    api<HistoryResp>("GET", `/history/${encodeURIComponent(symbol)}`, { range })
      .then((r) => { if (!cancelled) { setBars(r.bars || []); setLoading(false); } })
      .catch((e) => { if (!cancelled) { setError((e as Error).message); setLoading(false); setBars([]); } });
    return () => { cancelled = true; };
  }, [symbol, range, api]);

  if (!symbol) return null;
  if (error) return <div style={chartShellStyle}><EmptyState title="No history" hint={error} /></div>;

  const W = 600, H = 180, padX = 8, padY = 12;
  const values = bars.map(barValue);
  const min = values.length ? Math.min(...values) : 0;
  const max = values.length ? Math.max(...values) : 1;
  const range01 = max - min || 1;
  const first = values[0] ?? 0;
  const last = values[values.length - 1] ?? 0;
  const up = last >= first;
  const lineColor = up ? "#16a34a" : "#dc2626";

  const toX = (i: number) => padX + (i / Math.max(values.length - 1, 1)) * (W - 2 * padX);
  const toY = (v: number) => H - padY - ((v - min) / range01) * (H - 2 * padY);

  const pathD = values.map((v, i) => `${i === 0 ? "M" : "L"} ${toX(i).toFixed(2)} ${toY(v).toFixed(2)}`).join(" ");
  const fillD = pathD + ` L ${toX(values.length - 1).toFixed(2)} ${H - padY} L ${toX(0).toFixed(2)} ${H - padY} Z`;
  const lastY = values.length ? toY(last) : H / 2;

  return (
    <Section
      title="Chart"
      action={
        <div style={{ display: "flex", gap: 4 }}>
          {CHART_RANGES.map((r) => (
            <button
              key={r}
              onClick={() => setRange(r)}
              style={{
                fontSize: 10, padding: "3px 8px", borderRadius: 4,
                border: "1px solid var(--apteva-border, #e5e7eb)",
                background: r === range ? "var(--apteva-primary, #2563eb)" : "transparent",
                color: r === range ? "white" : "var(--apteva-text-muted, #6b7280)",
                cursor: "pointer", fontWeight: 600, letterSpacing: 0.3,
              }}
            >{r}</button>
          ))}
        </div>
      }
    >
      <div style={chartShellStyle}>
        {loading && values.length === 0 ? (
          <div style={{ height: H, display: "flex", alignItems: "center", justifyContent: "center", color: "var(--apteva-text-muted, #6b7280)", fontSize: 11 }}>
            Loading…
          </div>
        ) : values.length < 2 ? (
          <div style={{ height: H, display: "flex", alignItems: "center", justifyContent: "center", color: "var(--apteva-text-muted, #6b7280)", fontSize: 11 }}>
            No history available
          </div>
        ) : (
          <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ width: "100%", height: H, display: "block" }}>
            <defs>
              <linearGradient id="trading-chart-fill" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor={lineColor} stopOpacity="0.18" />
                <stop offset="100%" stopColor={lineColor} stopOpacity="0" />
              </linearGradient>
            </defs>
            {/* Horizontal gridlines at 25/50/75% */}
            {[0.25, 0.5, 0.75].map((p) => {
              const y = padY + p * (H - 2 * padY);
              return (
                <line
                  key={p}
                  x1={padX} x2={W - padX} y1={y} y2={y}
                  stroke="var(--apteva-border, #e5e7eb)" strokeWidth="1" strokeDasharray="2 4"
                />
              );
            })}
            {/* Area fill, then the line on top */}
            <path d={fillD} fill="url(#trading-chart-fill)" />
            <path d={pathD} fill="none" stroke={lineColor} strokeWidth="1.5" />
            {/* Last-value dot */}
            <circle cx={toX(values.length - 1)} cy={lastY} r="3" fill={lineColor} />
          </svg>
        )}
        <div style={{ display: "flex", justifyContent: "space-between", padding: "4px 8px 0", fontSize: 10, color: "var(--apteva-text-muted, #6b7280)" }}>
          <span>{values.length > 0 ? formatPrice(min, assetClass) : "—"}</span>
          <span style={{ color: up ? "#16a34a" : "#dc2626", fontWeight: 600 }}>
            {values.length > 0 ? formatPrice(last, assetClass) : "—"}
            {first > 0 && values.length > 1 && (
              <span style={{ marginLeft: 4, opacity: 0.8 }}>
                ({formatPct(((last - first) / first) * 100)})
              </span>
            )}
          </span>
          <span>{values.length > 0 ? formatPrice(max, assetClass) : "—"}</span>
        </div>
      </div>
    </Section>
  );
}

const chartShellStyle: React.CSSProperties = {
  border: "1px solid var(--apteva-border, #e5e7eb)",
  borderRadius: 6,
  background: "var(--apteva-bg, #fff)",
  padding: 6,
};

// Sparkline — tiny inline chart for table cells. Single polyline,
// theme-aware via CSS variable hint passed by caller.
function Sparkline({ values, up, width = 80, height = 24 }: {
  values: number[];
  up: boolean;
  width?: number;
  height?: number;
}) {
  if (values.length < 2) return <svg width={width} height={height} />;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min || 1;
  const toX = (i: number) => (i / (values.length - 1)) * width;
  const toY = (v: number) => height - ((v - min) / range) * (height - 2);
  const d = values.map((v, i) => `${i === 0 ? "M" : "L"} ${toX(i).toFixed(1)} ${toY(v).toFixed(1)}`).join(" ");
  const color = up ? "#16a34a" : "#dc2626";
  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} style={{ display: "block" }}>
      <path d={d} fill="none" stroke={color} strokeWidth="1.2" />
    </svg>
  );
}

// ─── Main component ────────────────────────────────────────────────

type TabId = "portfolios" | "trade" | "positions" | "brokers" | "journal";

export default function TradingPanel({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<TabId>("portfolios");
  const [portfolios, setPortfolios] = useState<Portfolio[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const withParams = useCallback((extra: Record<string, string> = {}) => {
    const u = new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra });
    return u.toString();
  }, [projectId, installId]);

  const api = useCallback(async <T,>(method: string, path: string, params?: Record<string, string>, body?: unknown): Promise<T> => {
    const opts: RequestInit = { method, credentials: "same-origin", headers: {} };
    if (body !== undefined) {
      (opts.headers as Record<string, string>)["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    const qs = withParams(params || {});
    const res = await fetch(`${API}${path}?${qs}`, opts);
    if (!res.ok) {
      let detail = "";
      try { detail = await res.text(); } catch {}
      throw new Error(`${res.status}: ${detail || res.statusText}`);
    }
    return res.json() as Promise<T>;
  }, [withParams]);

  const loadPortfolios = useCallback(async () => {
    try {
      const r = await api<{ portfolios?: Portfolio[] }>("GET", "/portfolios");
      const list = r.portfolios || [];
      setPortfolios(list);
      // Auto-select first portfolio so other tabs aren't empty.
      setSelectedId((cur) => cur ?? (list.length > 0 ? list[0].id : null));
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [api]);

  useEffect(() => { loadPortfolios(); }, [loadPortfolios]);

  // Live updates: any portfolio mutation reloads the list. Per-tab
  // refreshes for positions/orders/journal are wired inside each tab
  // so they only run when that tab is mounted.
  useAppEvents("trading", projectId, (ev) => {
    if (
      ev.topic === "portfolio.created" ||
      ev.topic === "portfolio.status.changed" ||
      ev.topic === "order.filled" ||
      ev.topic === "position.changed"
    ) {
      loadPortfolios();
    }
  });

  const selected = useMemo(
    () => portfolios.find((p) => p.id === selectedId) || null,
    [portfolios, selectedId],
  );

  return (
    <div className="trading-panel" style={panelStyle}>
      <PanelHeader
        portfolios={portfolios}
        selectedId={selectedId}
        onSelect={setSelectedId}
        onRefresh={loadPortfolios}
        busy={busy}
      />

      <Tabs current={tab} onChange={setTab} />

      {error && <ErrorBar text={error} onDismiss={() => setError(null)} />}

      <div style={contentStyle}>
        {tab === "portfolios" && (
          <PortfoliosTab
            portfolios={portfolios}
            selectedId={selectedId}
            onSelect={setSelectedId}
            api={api}
            onChanged={loadPortfolios}
            setBusy={setBusy}
            setError={setError}
          />
        )}
        {tab === "trade" && (
          <TradeTab
            portfolio={selected}
            api={api}
            setBusy={setBusy}
            setError={setError}
            projectId={projectId}
          />
        )}
        {tab === "positions" && (
          <PositionsTab portfolio={selected} api={api} setError={setError} />
        )}
        {tab === "brokers" && (
          <BrokersTab api={api} setError={setError} />
        )}
        {tab === "journal" && (
          <JournalTab portfolio={selected} api={api} setError={setError} />
        )}
      </div>
    </div>
  );
}

// ─── Layout primitives ─────────────────────────────────────────────

const panelStyle: React.CSSProperties = {
  display: "flex",
  flexDirection: "column",
  height: "100%",
  minHeight: 600,
  background: "var(--apteva-bg, #fff)",
  color: "var(--apteva-text, #111)",
  fontFamily: "system-ui, -apple-system, sans-serif",
  fontSize: 13,
};
const contentStyle: React.CSSProperties = {
  flex: 1,
  overflow: "auto",
  padding: 16,
};

function PanelHeader({
  portfolios, selectedId, onSelect, onRefresh, busy,
}: {
  portfolios: Portfolio[];
  selectedId: number | null;
  onSelect: (id: number | null) => void;
  onRefresh: () => void;
  busy: boolean;
}) {
  const sel = portfolios.find((p) => p.id === selectedId);
  return (
    <header style={{
      display: "flex", alignItems: "center", gap: 12,
      padding: "10px 16px", borderBottom: "1px solid var(--apteva-border, #e5e7eb)",
    }}>
      <h1 style={{ margin: 0, fontSize: 14, fontWeight: 600 }}>Trading</h1>
      <select
        value={selectedId ?? ""}
        onChange={(e) => onSelect(e.target.value ? Number(e.target.value) : null)}
        style={{
          fontSize: 12, padding: "4px 8px",
          background: "var(--apteva-bg-subtle, #f9fafb)",
          border: "1px solid var(--apteva-border, #e5e7eb)",
          borderRadius: 4,
        }}
      >
        <option value="">— Select portfolio —</option>
        {portfolios.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name} {p.mode === "live" ? "(LIVE)" : ""}
          </option>
        ))}
      </select>
      {sel && <PortfolioStatusPill status={sel.status} mode={sel.mode} />}
      <span style={{ flex: 1 }} />
      <button
        onClick={onRefresh}
        title="Refresh"
        disabled={busy}
        style={iconBtnStyle}
      ><Icon.Refresh /></button>
      <a
        href="../desk/dist/"
        target="_blank"
        rel="noopener"
        style={{
          fontSize: 11, color: "var(--apteva-text-muted, #6b7280)",
          textDecoration: "none", display: "inline-flex", alignItems: "center", gap: 4,
        }}
        title="Open the rich desk SPA"
      >
        Desk <Icon.ExternalLink />
      </a>
    </header>
  );
}

function PortfolioStatusPill({ status, mode }: { status: string; mode: string }) {
  const color = status === "active" ? "#16a34a" : status === "halted" ? "#dc2626" : "#d97706";
  const bg = status === "active" ? "#dcfce7" : status === "halted" ? "#fee2e2" : "#fef3c7";
  return (
    <span style={{
      fontSize: 11, padding: "2px 8px", borderRadius: 12,
      background: bg, color, display: "inline-flex", alignItems: "center", gap: 4,
      textTransform: "uppercase", letterSpacing: 0.3, fontWeight: 600,
    }}>
      <Icon.Dot color={color} />
      {status}
      {mode === "live" && <span style={{ opacity: 0.7 }}>· live</span>}
    </span>
  );
}

function Tabs({ current, onChange }: { current: TabId; onChange: (t: TabId) => void }) {
  const items: { id: TabId; label: string }[] = [
    { id: "portfolios", label: "Portfolios" },
    { id: "trade", label: "Trade" },
    { id: "positions", label: "Positions" },
    { id: "brokers", label: "Brokers" },
    { id: "journal", label: "Journal" },
  ];
  return (
    <nav style={{
      display: "flex", gap: 0,
      borderBottom: "1px solid var(--apteva-border, #e5e7eb)",
      padding: "0 16px",
    }}>
      {items.map((it) => {
        const active = it.id === current;
        return (
          <button
            key={it.id}
            onClick={() => onChange(it.id)}
            style={{
              background: "none", border: "none",
              padding: "10px 14px", fontSize: 12, fontWeight: active ? 600 : 500,
              color: active ? "var(--apteva-text, #111)" : "var(--apteva-text-muted, #6b7280)",
              borderBottom: active ? "2px solid var(--apteva-primary, #2563eb)" : "2px solid transparent",
              marginBottom: -1, cursor: "pointer",
            }}
          >
            {it.label}
          </button>
        );
      })}
    </nav>
  );
}

const iconBtnStyle: React.CSSProperties = {
  background: "none",
  border: "1px solid var(--apteva-border, #e5e7eb)",
  color: "var(--apteva-text-muted, #6b7280)",
  borderRadius: 4,
  padding: "4px 6px",
  cursor: "pointer",
  display: "inline-flex",
  alignItems: "center",
};
const btnStyle: React.CSSProperties = {
  background: "var(--apteva-primary, #2563eb)",
  color: "white",
  border: "none",
  borderRadius: 4,
  padding: "6px 12px",
  fontSize: 12,
  fontWeight: 500,
  cursor: "pointer",
};
const btnSecondaryStyle: React.CSSProperties = {
  ...btnStyle,
  background: "var(--apteva-bg-subtle, #f3f4f6)",
  color: "var(--apteva-text, #111)",
  border: "1px solid var(--apteva-border, #e5e7eb)",
};
const inputStyle: React.CSSProperties = {
  width: "100%",
  fontSize: 12,
  padding: "6px 8px",
  background: "var(--apteva-bg, #fff)",
  border: "1px solid var(--apteva-border, #d1d5db)",
  borderRadius: 4,
  boxSizing: "border-box",
};
const labelStyle: React.CSSProperties = {
  display: "block",
  fontSize: 11,
  fontWeight: 500,
  color: "var(--apteva-text-muted, #6b7280)",
  marginBottom: 4,
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

function ErrorBar({ text, onDismiss }: { text: string; onDismiss: () => void }) {
  return (
    <div style={{
      padding: "8px 16px", background: "#fee2e2", color: "#991b1b",
      fontSize: 12, display: "flex", alignItems: "center", gap: 8,
    }}>
      <span style={{ flex: 1 }}>{text}</span>
      <button onClick={onDismiss} style={{ ...iconBtnStyle, color: "#991b1b", borderColor: "#fca5a5" }}>
        <Icon.X />
      </button>
    </div>
  );
}

function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div style={{
      padding: 32, textAlign: "center",
      color: "var(--apteva-text-muted, #6b7280)", fontSize: 12,
    }}>
      <div style={{ fontWeight: 500, marginBottom: 4, fontSize: 13 }}>{title}</div>
      {hint && <div style={{ opacity: 0.8 }}>{hint}</div>}
    </div>
  );
}

function Section({ title, action, children }: {
  title: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section style={{ marginBottom: 24 }}>
      <div style={{
        display: "flex", alignItems: "center", justifyContent: "space-between",
        marginBottom: 8,
      }}>
        <h2 style={{ margin: 0, fontSize: 12, fontWeight: 600, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--apteva-text-muted, #6b7280)" }}>{title}</h2>
        {action}
      </div>
      {children}
    </section>
  );
}

// ─── Portfolios tab ────────────────────────────────────────────────

interface PortfoliosTabProps {
  portfolios: Portfolio[];
  selectedId: number | null;
  onSelect: (id: number) => void;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  onChanged: () => void;
  setBusy: (b: boolean) => void;
  setError: (e: string | null) => void;
}

function PortfoliosTab({ portfolios, selectedId, onSelect, api, onChanged, setBusy, setError }: PortfoliosTabProps) {
  const [showCreate, setShowCreate] = useState(false);

  const togglePause = async (p: Portfolio) => {
    setBusy(true);
    try {
      const next = p.status === "active" ? "paused" : "active";
      await api("PATCH", `/portfolios/${p.id}`, undefined, { status: next });
      onChanged();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };

  return (
    <>
      <Section
        title="Portfolios"
        action={
          <button onClick={() => setShowCreate((s) => !s)} style={btnStyle}>
            <span style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
              <Icon.Plus /> New
            </span>
          </button>
        }
      >
        {showCreate && (
          <CreatePortfolioForm
            api={api}
            onCreated={() => { setShowCreate(false); onChanged(); }}
            onCancel={() => setShowCreate(false)}
            setError={setError}
          />
        )}
        {portfolios.length === 0 && !showCreate && (
          <EmptyState title="No portfolios yet" hint="Click New to create your first paper portfolio." />
        )}
        {portfolios.length > 0 && (
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))", gap: 12 }}>
            {portfolios.map((p) => {
              const isSelected = p.id === selectedId;
              return (
                <div
                  key={p.id}
                  onClick={() => onSelect(p.id)}
                  style={{
                    cursor: "pointer",
                    padding: 12,
                    border: isSelected ? "2px solid var(--apteva-primary, #2563eb)" : "1px solid var(--apteva-border, #e5e7eb)",
                    borderRadius: 6,
                    background: isSelected ? "var(--apteva-primary-bg, #eff6ff)" : "var(--apteva-bg, #fff)",
                  }}
                >
                  <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 8 }}>
                    <strong style={{ fontSize: 13 }}>{p.name}</strong>
                    <PortfolioStatusPill status={p.status} mode={p.mode} />
                  </div>
                  {p.broker_slug && (
                    <div style={{ fontSize: 11, color: "var(--apteva-text-muted, #6b7280)", marginBottom: 4 }}>
                      via {p.broker_slug}
                    </div>
                  )}
                  {p.mandate && (
                    <div style={{ fontSize: 11, color: "var(--apteva-text-muted, #6b7280)", marginBottom: 8, fontStyle: "italic" }}>
                      "{p.mandate.length > 80 ? p.mandate.slice(0, 80) + "…" : p.mandate}"
                    </div>
                  )}
                  <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8, fontSize: 11 }}>
                    <div>
                      <div style={labelStyle}>Equity</div>
                      <div style={{ fontWeight: 600 }}>{formatUSD(p.equity)}</div>
                    </div>
                    <div>
                      <div style={labelStyle}>Cash</div>
                      <div>{formatUSD(p.cash)}</div>
                    </div>
                    <div>
                      <div style={labelStyle}>Day P&L</div>
                      <div style={{ color: pnlColor(p.day_pnl) }}>
                        {formatUSD(p.day_pnl)} <span style={{ opacity: 0.7 }}>({formatPct(p.day_pnl_pct)})</span>
                      </div>
                    </div>
                    <div>
                      <div style={labelStyle}>Open P&L</div>
                      <div style={{ color: pnlColor(p.open_pnl) }}>
                        {formatUSD(p.open_pnl)} <span style={{ opacity: 0.7 }}>({formatPct(p.open_pnl_pct)})</span>
                      </div>
                    </div>
                  </div>
                  <div style={{ display: "flex", gap: 6, marginTop: 10, flexWrap: "wrap" }}>
                    {p.allowed_classes.map((c) => (
                      <span key={c} style={{
                        fontSize: 10, padding: "1px 6px", borderRadius: 10,
                        background: "var(--apteva-bg-subtle, #f3f4f6)",
                        color: classColor(c), fontWeight: 600,
                      }}>{c}</span>
                    ))}
                    <span style={{ flex: 1 }} />
                    <button
                      onClick={(e) => { e.stopPropagation(); togglePause(p); }}
                      style={iconBtnStyle}
                      title={p.status === "active" ? "Pause" : "Resume"}
                    >
                      {p.status === "active" ? <Icon.Pause /> : <Icon.Play />}
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </Section>
    </>
  );
}

function pnlColor(n: number | undefined): string {
  if (n == null || n === 0) return "inherit";
  return n > 0 ? "#16a34a" : "#dc2626";
}

function CreatePortfolioForm({ api, onCreated, onCancel, setError }: {
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  onCreated: () => void;
  onCancel: () => void;
  setError: (e: string | null) => void;
}) {
  const [mode, setMode] = useState<"paper" | "live">("paper");
  const [name, setName] = useState("");
  const [mandate, setMandate] = useState("");
  const [startingCash, setStartingCash] = useState("100000");
  const [brokerSlug, setBrokerSlug] = useState("");
  const [brokers, setBrokers] = useState<BrokerInfo[]>([]);
  const [classes, setClasses] = useState<string[]>(["equity", "etf"]);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    api<{ brokers?: BrokerInfo[] }>("GET", "/brokers")
      .then((r) => setBrokers(r.brokers || []))
      .catch(() => undefined);
  }, [api]);

  const adapter = brokers.find((b) => b.slug === brokerSlug);

  // When the operator picks a broker, default allowed_classes to the
  // intersection with the broker's capabilities so they don't have to
  // think about which asset classes the broker supports.
  useEffect(() => {
    if (adapter) setClasses(adapter.asset_classes);
  }, [adapter]);

  const submit = async () => {
    if (!name.trim()) return;
    setSubmitting(true);
    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        mandate: mandate.trim(),
        mode,
        allowed_classes: classes,
      };
      if (mode === "paper") body.starting_cash = Number(startingCash);
      if (mode === "live") body.broker_slug = brokerSlug;
      await api("POST", "/portfolios", undefined, body);
      onCreated();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  const liveBrokers = brokers.filter((b) => b.bound);

  return (
    <div style={{
      padding: 16, marginBottom: 16,
      border: "1px solid var(--apteva-border, #e5e7eb)",
      borderRadius: 6, background: "var(--apteva-bg-subtle, #f9fafb)",
    }}>
      <div style={{ display: "flex", gap: 12, marginBottom: 12 }}>
        <label style={{ fontSize: 12, display: "flex", alignItems: "center", gap: 6 }}>
          <input type="radio" checked={mode === "paper"} onChange={() => setMode("paper")} />
          Paper
        </label>
        <label style={{ fontSize: 12, display: "flex", alignItems: "center", gap: 6 }}>
          <input
            type="radio"
            checked={mode === "live"}
            onChange={() => setMode("live")}
            disabled={liveBrokers.length === 0}
          />
          Live
          {liveBrokers.length === 0 && (
            <span style={{ fontSize: 10, color: "var(--apteva-text-muted, #6b7280)" }}>
              (no broker bound — see Brokers tab)
            </span>
          )}
        </label>
      </div>

      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12, marginBottom: 12 }}>
        <div>
          <label style={labelStyle}>Name</label>
          <input value={name} onChange={(e) => setName(e.target.value)} style={inputStyle} placeholder="e.g. tech-longs" />
        </div>
        {mode === "paper" ? (
          <div>
            <label style={labelStyle}>Starting cash (USD)</label>
            <input value={startingCash} onChange={(e) => setStartingCash(e.target.value)} style={inputStyle} type="number" />
          </div>
        ) : (
          <div>
            <label style={labelStyle}>Broker</label>
            <select value={brokerSlug} onChange={(e) => setBrokerSlug(e.target.value)} style={inputStyle}>
              <option value="">— Pick —</option>
              {liveBrokers.map((b) => (
                <option key={b.slug} value={b.slug}>
                  {b.slug} ({b.asset_classes.join(", ")})
                </option>
              ))}
            </select>
          </div>
        )}
      </div>

      <div style={{ marginBottom: 12 }}>
        <label style={labelStyle}>Mandate</label>
        <textarea
          value={mandate} onChange={(e) => setMandate(e.target.value)}
          style={{ ...inputStyle, minHeight: 60, resize: "vertical" }}
          placeholder="What this portfolio is for (free-text; surfaced to the agent's prompt)."
        />
      </div>

      <div style={{ marginBottom: 12 }}>
        <label style={labelStyle}>Asset classes</label>
        <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
          {(adapter?.asset_classes || ["equity", "etf", "crypto", "polymarket"]).map((c) => {
            const on = classes.includes(c);
            return (
              <button
                key={c}
                onClick={() => setClasses((cs) => on ? cs.filter((x) => x !== c) : [...cs, c])}
                style={{
                  fontSize: 11, padding: "4px 10px", borderRadius: 12,
                  border: "1px solid " + (on ? classColor(c) : "var(--apteva-border, #e5e7eb)"),
                  background: on ? classColor(c) : "transparent",
                  color: on ? "white" : "var(--apteva-text, #111)",
                  cursor: "pointer", fontWeight: 500,
                }}
              >{c}</button>
            );
          })}
        </div>
      </div>

      <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
        <button onClick={onCancel} style={btnSecondaryStyle}>Cancel</button>
        <button
          onClick={submit}
          disabled={submitting || !name.trim() || (mode === "live" && !brokerSlug)}
          style={btnStyle}
        >
          {submitting ? "Creating…" : "Create portfolio"}
        </button>
      </div>
    </div>
  );
}

// ─── Trade tab ────────────────────────────────────────────────────

function TradeTab({ portfolio, api, setBusy, setError, projectId }: {
  portfolio: Portfolio | null;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  setBusy: (b: boolean) => void;
  setError: (e: string | null) => void;
  projectId: string;
}) {
  const [orders, setOrders] = useState<Order[]>([]);
  const [working, setWorking] = useState<Order[]>([]);

  const reload = useCallback(async () => {
    if (!portfolio) return;
    try {
      const [w, all] = await Promise.all([
        api<{ orders: Order[] }>("GET", `/portfolios/${portfolio.id}/orders`, { status: "working" }),
        api<{ orders: Order[] }>("GET", `/portfolios/${portfolio.id}/orders`, { status: "all", limit: "20" }),
      ]);
      setWorking(w.orders || []);
      setOrders(all.orders || []);
    } catch (e) { setError((e as Error).message); }
  }, [portfolio, api, setError]);

  useEffect(() => { reload(); }, [reload]);
  useAppEvents("trading", projectId, (ev) => {
    if (["order.placed", "order.filled", "order.cancelled", "order.rejected"].includes(ev.topic)) reload();
  });

  if (!portfolio) return <EmptyState title="Pick a portfolio" hint="Use the dropdown above or the Portfolios tab." />;

  const cancel = async (orderID: string) => {
    setBusy(true);
    try {
      await api("POST", `/portfolios/${portfolio.id}/orders/${encodeURIComponent(orderID)}/cancel`, { reason: "manual_cancel_via_panel" });
      reload();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };

  return (
    <>
      <StatsCard portfolio={portfolio} />
      <PlaceOrderFormWithChart portfolio={portfolio} api={api} onPlaced={reload} setError={setError} />
      <Section title="Working orders">
        {working.length === 0
          ? <EmptyState title="No working orders" />
          : <OrdersTable orders={working} onCancel={cancel} />}
      </Section>
      <Section title="Recent orders">
        {orders.length === 0
          ? <EmptyState title="No orders yet" />
          : <OrdersTable orders={orders} />}
      </Section>
    </>
  );
}

// PlaceOrderFormWithChart — wraps PlaceOrderForm + PriceChart with a
// shared symbol input. The chart updates whenever the user types a
// resolvable symbol; the form does the actual order submission against
// the same value. Keeping the chart visible while the operator fills in
// qty/price gives them context for the order they're about to place.
function PlaceOrderFormWithChart({ portfolio, api, onPlaced, setError }: {
  portfolio: Portfolio;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  onPlaced: () => void;
  setError: (e: string | null) => void;
}) {
  const [symbol, setSymbol] = useState("");
  const assetClass = inferAssetClass(symbol);
  return (
    <>
      <PriceChart symbol={symbol.trim()} assetClass={assetClass} api={api} />
      <PlaceOrderForm
        portfolio={portfolio} api={api} onPlaced={onPlaced} setError={setError}
        symbol={symbol} setSymbol={setSymbol}
      />
    </>
  );
}

// Match the engine's classifier in pricing.go: POLY:* → polymarket,
// *-USD → crypto, everything else → equity. Lets the chart format the
// y-axis label correctly without an extra round-trip.
function inferAssetClass(symbol: string): string {
  const s = symbol.toUpperCase().trim();
  if (s.startsWith("POLY:")) return "polymarket";
  if (s.endsWith("-USD")) return "crypto";
  return "equity";
}

function StatsCard({ portfolio }: { portfolio: Portfolio }) {
  const cells = [
    { label: "Equity", value: formatUSD(portfolio.equity) },
    { label: "Cash", value: formatUSD(portfolio.cash) },
    { label: "Buying power", value: formatUSD(portfolio.buying_power) },
    { label: "Day P&L", value: formatUSD(portfolio.day_pnl), sub: formatPct(portfolio.day_pnl_pct), color: pnlColor(portfolio.day_pnl) },
    { label: "Open P&L", value: formatUSD(portfolio.open_pnl), sub: formatPct(portfolio.open_pnl_pct), color: pnlColor(portfolio.open_pnl) },
  ];
  return (
    <div style={{
      display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(140px, 1fr))",
      gap: 8, marginBottom: 16,
    }}>
      {cells.map((c) => (
        <div key={c.label} style={{
          padding: "10px 12px",
          background: "var(--apteva-bg-subtle, #f9fafb)",
          border: "1px solid var(--apteva-border, #e5e7eb)",
          borderRadius: 4,
        }}>
          <div style={labelStyle}>{c.label}</div>
          <div style={{ fontWeight: 600, fontSize: 14, color: c.color }}>
            {c.value} {c.sub && <span style={{ fontSize: 11, opacity: 0.8, fontWeight: 400 }}>{c.sub}</span>}
          </div>
        </div>
      ))}
    </div>
  );
}

function PlaceOrderForm({ portfolio, api, onPlaced, setError, symbol, setSymbol }: {
  portfolio: Portfolio;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  onPlaced: () => void;
  setError: (e: string | null) => void;
  symbol: string;
  setSymbol: (s: string) => void;
}) {
  const [side, setSide] = useState<string>("buy");
  const [type, setType] = useState<string>("market");
  const [qty, setQty] = useState("");
  const [limitPrice, setLimitPrice] = useState("");
  const [stopPrice, setStopPrice] = useState("");
  const [rationale, setRationale] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [quote, setQuote] = useState<Mark | null>(null);
  const isPoly = symbol.toUpperCase().startsWith("POLY:");

  // Quote fetch on symbol blur — small affordance so the agent sees
  // the live mark before they pick a price.
  const fetchQuote = useCallback(async () => {
    if (!symbol.trim()) { setQuote(null); return; }
    try {
      const r = await api<Mark>("GET", `/quotes/${encodeURIComponent(symbol.trim())}`);
      setQuote(r);
    } catch { setQuote(null); }
  }, [symbol, api]);

  const submit = async () => {
    if (!symbol.trim() || !qty || !rationale.trim()) return;
    setSubmitting(true);
    try {
      const body: Record<string, unknown> = {
        symbol: symbol.trim(),
        side, type,
        qty: Number(qty),
        rationale: rationale.trim(),
      };
      if (limitPrice) body.limit_price = Number(limitPrice);
      if (stopPrice) body.stop_price = Number(stopPrice);
      const r = await api<{ order_id?: string; status?: string; code?: string; detail?: string }>(
        "POST", `/portfolios/${portfolio.id}/orders`, undefined, body,
      );
      if (r.status === "rejected") {
        setError(`Order rejected: ${r.code} — ${r.detail}`);
      } else {
        setSymbol(""); setQty(""); setLimitPrice(""); setStopPrice(""); setRationale("");
        setQuote(null);
        onPlaced();
      }
    } catch (e) { setError((e as Error).message); } finally { setSubmitting(false); }
  };

  return (
    <Section title="Place order">
      <div style={{
        padding: 12,
        border: "1px solid var(--apteva-border, #e5e7eb)",
        borderRadius: 6,
        background: "var(--apteva-bg, #fff)",
      }}>
        <div style={{ display: "grid", gridTemplateColumns: "2fr 1fr 1fr 1fr", gap: 8, marginBottom: 8 }}>
          <div>
            <label style={labelStyle}>Symbol</label>
            <input value={symbol} onChange={(e) => setSymbol(e.target.value)} onBlur={fetchQuote}
              style={inputStyle} placeholder="AAPL · BTC-USD · POLY:slug" />
            {quote && (
              <div style={{ fontSize: 11, marginTop: 2, color: "var(--apteva-text-muted, #6b7280)" }}>
                Mark: {formatPrice(quote.price ?? quote.yes_price, quote.asset_class)}
                {quote.change_pct_24h != null && (
                  <span style={{ marginLeft: 6, color: pnlColor(quote.change_pct_24h) }}>
                    {formatPct(quote.change_pct_24h)}
                  </span>
                )}
              </div>
            )}
          </div>
          <div>
            <label style={labelStyle}>Side</label>
            <select value={side} onChange={(e) => setSide(e.target.value)} style={inputStyle}>
              {isPoly ? (
                <>
                  <option value="yes">YES (buy)</option>
                  <option value="no">NO (buy)</option>
                </>
              ) : (
                <>
                  <option value="buy">Buy</option>
                  <option value="sell">Sell</option>
                </>
              )}
            </select>
          </div>
          <div>
            <label style={labelStyle}>Type</label>
            <select value={type} onChange={(e) => setType(e.target.value)} style={inputStyle}>
              <option value="market">Market</option>
              <option value="limit">Limit</option>
              {!isPoly && <option value="stop">Stop</option>}
            </select>
          </div>
          <div>
            <label style={labelStyle}>Qty</label>
            <input value={qty} onChange={(e) => setQty(e.target.value)} type="number" step="any" style={inputStyle} />
          </div>
        </div>

        {(type === "limit" || type === "stop") && (
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8, marginBottom: 8 }}>
            {type === "limit" && (
              <div>
                <label style={labelStyle}>Limit price</label>
                <input value={limitPrice} onChange={(e) => setLimitPrice(e.target.value)} type="number" step="any" style={inputStyle}
                  placeholder={isPoly ? "0–1 (probability)" : "USD"} />
              </div>
            )}
            {type === "stop" && (
              <div>
                <label style={labelStyle}>Stop price</label>
                <input value={stopPrice} onChange={(e) => setStopPrice(e.target.value)} type="number" step="any" style={inputStyle} />
              </div>
            )}
          </div>
        )}

        <div style={{ marginBottom: 12 }}>
          <label style={labelStyle}>Rationale (≥30 chars, required)</label>
          <textarea
            value={rationale} onChange={(e) => setRationale(e.target.value)}
            style={{ ...inputStyle, minHeight: 50, resize: "vertical" }}
            placeholder="Why are you placing this order? Used for audit + agent introspection."
          />
          <div style={{ fontSize: 10, color: "var(--apteva-text-muted, #6b7280)", marginTop: 2 }}>
            {rationale.length}/30
          </div>
        </div>

        <div style={{ display: "flex", justifyContent: "flex-end" }}>
          <button
            onClick={submit}
            disabled={submitting || !symbol.trim() || !qty || rationale.trim().length < 30}
            style={btnStyle}
          >
            {submitting ? "Placing…" : `Place ${side.toUpperCase()} ${type}`}
          </button>
        </div>
      </div>
    </Section>
  );
}

function OrdersTable({ orders, onCancel }: { orders: Order[]; onCancel?: (id: string) => void }) {
  return (
    <div style={{
      border: "1px solid var(--apteva-border, #e5e7eb)",
      borderRadius: 6, overflow: "hidden",
    }}>
      <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 11 }}>
        <thead>
          <tr style={{ background: "var(--apteva-bg-subtle, #f9fafb)" }}>
            {["Order", "Symbol", "Side", "Type", "Qty", "Status", ""].map((h) => (
              <th key={h} style={thStyle}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {orders.map((o) => (
            <tr key={o.id} style={{ borderTop: "1px solid var(--apteva-border, #e5e7eb)" }}>
              <td style={tdStyle}><code style={{ fontSize: 10 }}>{o.id}</code></td>
              <td style={tdStyle}><strong>{o.symbol}</strong></td>
              <td style={{ ...tdStyle, textTransform: "uppercase", color: o.side === "buy" || o.side === "yes" ? "#16a34a" : "#dc2626" }}>{o.side}</td>
              <td style={tdStyle}>{o.type}{o.limit_price ? ` @ ${formatPrice(o.limit_price, o.asset_class)}` : ""}</td>
              <td style={tdStyle}>{formatQty(o.qty)}{o.filled_qty > 0 && o.filled_qty < o.qty && ` (${formatQty(o.filled_qty)} filled)`}</td>
              <td style={tdStyle}><OrderStatusPill status={o.status} /></td>
              <td style={{ ...tdStyle, textAlign: "right" }}>
                {onCancel && o.status === "working" && (
                  <button onClick={() => onCancel(o.id)} style={iconBtnStyle} title="Cancel"><Icon.X /></button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function OrderStatusPill({ status }: { status: string }) {
  const map: Record<string, { c: string; bg: string }> = {
    working:   { c: "#1d4ed8", bg: "#dbeafe" },
    filled:    { c: "#15803d", bg: "#dcfce7" },
    cancelled: { c: "#6b7280", bg: "#f3f4f6" },
    rejected:  { c: "#b91c1c", bg: "#fee2e2" },
  };
  const s = map[status] || { c: "#6b7280", bg: "#f3f4f6" };
  return (
    <span style={{
      fontSize: 10, padding: "2px 6px", borderRadius: 10,
      background: s.bg, color: s.c, fontWeight: 600, textTransform: "uppercase",
    }}>{status}</span>
  );
}

const thStyle: React.CSSProperties = {
  padding: "6px 10px", textAlign: "left", fontSize: 10, fontWeight: 600,
  textTransform: "uppercase", letterSpacing: 0.4,
  color: "var(--apteva-text-muted, #6b7280)",
};
const tdStyle: React.CSSProperties = { padding: "6px 10px", verticalAlign: "middle" };

// ─── Positions tab ────────────────────────────────────────────────

function PositionsTab({ portfolio, api, setError }: {
  portfolio: Portfolio | null;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  setError: (e: string | null) => void;
}) {
  const [positions, setPositions] = useState<Position[]>([]);
  const [sparklines, setSparklines] = useState<Record<string, number[]>>({});

  const load = useCallback(async () => {
    if (!portfolio) return;
    try {
      const r = await api<{ positions: Position[] }>("GET", `/portfolios/${portfolio.id}/positions`);
      setPositions(r.positions || []);
    } catch (e) { setError((e as Error).message); }
  }, [portfolio, api, setError]);

  useEffect(() => { load(); }, [load]);

  // Fetch a 1D sparkline per position symbol. Stale-while-revalidate
  // is OK; we don't block the table render on these.
  useEffect(() => {
    let cancelled = false;
    const fetchAll = async () => {
      const results: Record<string, number[]> = {};
      await Promise.all(positions.map(async (p) => {
        try {
          const r = await api<HistoryResp>("GET", `/history/${encodeURIComponent(p.symbol)}`, { range: "1D" });
          results[p.symbol] = (r.bars || []).map(barValue);
        } catch { /* skip — sparkline is optional */ }
      }));
      if (!cancelled) setSparklines(results);
    };
    if (positions.length > 0) fetchAll();
    return () => { cancelled = true; };
  }, [positions, api]);

  if (!portfolio) return <EmptyState title="Pick a portfolio" />;
  if (positions.length === 0) return <EmptyState title="No open positions" hint="Place an order in the Trade tab." />;

  return (
    <Section title={`Open positions · ${portfolio.name}`}>
      <div style={{
        border: "1px solid var(--apteva-border, #e5e7eb)",
        borderRadius: 6, overflow: "hidden",
      }}>
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 11 }}>
          <thead>
            <tr style={{ background: "var(--apteva-bg-subtle, #f9fafb)" }}>
              {["Symbol", "Class", "Qty", "Avg cost", "Mark", "Market value", "Unrealized", "Weight", "1D"].map((h) => (
                <th key={h} style={thStyle}>{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {positions.map((p) => {
              const spark = sparklines[p.symbol] || [];
              const sparkUp = spark.length >= 2 ? spark[spark.length - 1] >= spark[0] : true;
              return (
                <tr key={p.symbol + (p.outcome || "")} style={{ borderTop: "1px solid var(--apteva-border, #e5e7eb)" }}>
                  <td style={tdStyle}>
                    <strong>{p.symbol}</strong>
                    {p.outcome && <span style={{ marginLeft: 6, fontSize: 10, color: "var(--apteva-text-muted, #6b7280)" }}>{p.outcome}</span>}
                  </td>
                  <td style={tdStyle}><span style={{ color: classColor(p.asset_class), fontWeight: 600 }}>{p.asset_class}</span></td>
                  <td style={tdStyle}>{formatQty(p.qty)}</td>
                  <td style={tdStyle}>{formatPrice(p.avg_cost, p.asset_class)}</td>
                  <td style={tdStyle}>{formatPrice(p.market_price, p.asset_class)}</td>
                  <td style={tdStyle}>{formatUSD(p.market_value)}</td>
                  <td style={{ ...tdStyle, color: pnlColor(p.unrealized_pnl) }}>
                    {formatUSD(p.unrealized_pnl)} <span style={{ opacity: 0.7 }}>({formatPct(p.unrealized_pnl_pct)})</span>
                  </td>
                  <td style={tdStyle}>{formatPct(p.weight_pct, 1)}</td>
                  <td style={tdStyle}><Sparkline values={spark} up={sparkUp} /></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Section>
  );
}

// ─── Brokers tab ──────────────────────────────────────────────────

function BrokersTab({ api, setError }: {
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  setError: (e: string | null) => void;
}) {
  const [brokers, setBrokers] = useState<BrokerInfo[]>([]);

  const load = useCallback(async () => {
    try {
      const r = await api<{ brokers: BrokerInfo[] }>("GET", "/brokers");
      setBrokers(r.brokers || []);
    } catch (e) { setError((e as Error).message); }
  }, [api, setError]);

  useEffect(() => { load(); }, [load]);

  return (
    <Section
      title="Brokers"
      action={
        <a
          href="/dashboard/integrations"
          target="_blank"
          rel="noopener"
          style={{ ...btnStyle, textDecoration: "none", display: "inline-flex", alignItems: "center", gap: 4 }}
        >
          Bind broker <Icon.ExternalLink />
        </a>
      }
    >
      <div style={{
        marginBottom: 12,
        padding: 10,
        fontSize: 11,
        background: "var(--apteva-bg-subtle, #f9fafb)",
        border: "1px solid var(--apteva-border, #e5e7eb)",
        borderRadius: 4,
        color: "var(--apteva-text-muted, #6b7280)",
      }}>
        Each portfolio binds to one broker at creation. Bind a connection
        via the dashboard's integrations page, then come back and create a
        live portfolio under the Portfolios tab.
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))", gap: 12 }}>
        {brokers.map((b) => (
          <div key={b.slug} style={{
            padding: 12,
            border: "1px solid var(--apteva-border, #e5e7eb)",
            borderRadius: 6,
            background: b.bound ? "var(--apteva-bg, #fff)" : "var(--apteva-bg-subtle, #f9fafb)",
          }}>
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 8 }}>
              <strong style={{ fontSize: 13 }}>{b.slug}</strong>
              <span style={{
                fontSize: 10, padding: "2px 8px", borderRadius: 10,
                background: b.bound ? "#dcfce7" : "#f3f4f6",
                color: b.bound ? "#15803d" : "#6b7280",
                fontWeight: 600, textTransform: "uppercase",
              }}>
                {b.bound ? "bound" : "unbound"}
              </span>
            </div>
            <div style={{ marginBottom: 6, fontSize: 11 }}>
              <span style={labelStyle}>Classes</span>
              <div style={{ display: "flex", gap: 4, flexWrap: "wrap", marginTop: 2 }}>
                {b.asset_classes.map((c) => (
                  <span key={c} style={{
                    fontSize: 10, padding: "1px 6px", borderRadius: 10,
                    background: "var(--apteva-bg-subtle, #f3f4f6)",
                    color: classColor(c), fontWeight: 600,
                  }}>{c}</span>
                ))}
              </div>
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 6, fontSize: 11, marginTop: 8 }}>
              <div><span style={labelStyle}>Quote</span><div>{b.quote}</div></div>
              <div><span style={labelStyle}>Fractional</span><div>{b.fractional ? "yes" : "no"}</div></div>
            </div>
            <div style={{ marginTop: 8, fontSize: 11 }}>
              <span style={labelStyle}>TIFs</span>
              <div>{b.tifs.join(", ")}</div>
            </div>
            {b.connections.length > 0 && (
              <div style={{ marginTop: 8, fontSize: 11 }}>
                <span style={labelStyle}>Connections</span>
                {b.connections.map((c) => (
                  <div key={c.id} style={{
                    padding: "4px 8px", marginTop: 4, fontSize: 11,
                    background: "var(--apteva-bg-subtle, #f3f4f6)",
                    border: "1px solid var(--apteva-border, #e5e7eb)",
                    borderRadius: 4,
                  }}>
                    #{c.id} · {c.name || "(unnamed)"} · {c.status}
                  </div>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </Section>
  );
}

// ─── Journal tab ──────────────────────────────────────────────────

function JournalTab({ portfolio, api, setError }: {
  portfolio: Portfolio | null;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  setError: (e: string | null) => void;
}) {
  const [entries, setEntries] = useState<JournalEntry[]>([]);
  const [kind, setKind] = useState("");

  const load = useCallback(async () => {
    if (!portfolio) return;
    try {
      const params: Record<string, string> = { limit: "50" };
      if (kind) params.kind = kind;
      const r = await api<{ entries: JournalEntry[] }>("GET", `/portfolios/${portfolio.id}/journal`, params);
      setEntries(r.entries || []);
    } catch (e) { setError((e as Error).message); }
  }, [portfolio, api, setError, kind]);

  useEffect(() => { load(); }, [load]);

  if (!portfolio) return <EmptyState title="Pick a portfolio" />;

  return (
    <Section
      title="Journal"
      action={
        <select value={kind} onChange={(e) => setKind(e.target.value)} style={{ ...inputStyle, width: "auto" }}>
          <option value="">All kinds</option>
          <option value="rationale">Rationale</option>
          <option value="fill">Fill</option>
          <option value="alert">Alert</option>
          <option value="note">Note</option>
        </select>
      }
    >
      {entries.length === 0 ? (
        <EmptyState title="No journal entries" />
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
          {entries.map((e) => (
            <div key={e.id} style={{
              padding: 10,
              border: "1px solid var(--apteva-border, #e5e7eb)",
              borderRadius: 4,
              background: "var(--apteva-bg, #fff)",
            }}>
              <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 4, fontSize: 11 }}>
                <span style={{
                  textTransform: "uppercase", letterSpacing: 0.5, fontWeight: 600,
                  color: kindColor(e.kind),
                }}>{e.kind}</span>
                <span style={{ color: "var(--apteva-text-muted, #6b7280)" }}>{relTime(e.created_at)}</span>
              </div>
              <div style={{ fontSize: 12, whiteSpace: "pre-wrap" }}>{e.body}</div>
            </div>
          ))}
        </div>
      )}
    </Section>
  );
}

function kindColor(kind: string): string {
  switch (kind) {
    case "fill": return "#15803d";
    case "alert": return "#b91c1c";
    case "rationale": return "#1d4ed8";
    case "note": return "#6b7280";
    default: return "#6b7280";
  }
}
