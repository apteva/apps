// TradingPanel — native React panel for the trading app. Styled with
// the dashboard's Tailwind theme tokens (bg-bg, text-text, border-border,
// text-accent, …) so it matches CRM / Messaging / Storage / Finance.
//
// Talks to /api/apps/trading/* through the platform proxy.
//
// Tabs: Portfolios | Trade | Positions | Agents | Brokers | Journal.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// ─── Inlined SDK app-event subscription ────────────────────────────

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

// ─── Dashboard telemetry subscription ─────────────────────────────

interface TelemetryEvent {
  id: string;
  instance_id: number;
  thread_id: string;
  type: string;
  time: string;
  data: Record<string, any>;
}

function useTelemetryEvents(
  projectId: string | undefined | null,
  onEvent: (ev: TelemetryEvent) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!projectId) return;
    const handler = (ev: TelemetryEvent) => handlerRef.current(ev);
    const bridge = (window as unknown as {
      __aptevaTelemetryBus?: {
        subscribe(instanceId: number | null, fn: (ev: TelemetryEvent) => void): () => void;
      };
    }).__aptevaTelemetryBus;
    if (bridge) return bridge.subscribe(null, handler);

    let es: EventSource | null = null;
    let cancelled = false;
    const url = `/api/telemetry/stream?all=1&project_id=${encodeURIComponent(projectId)}`;
    es = new EventSource(url, { withCredentials: true });
    es.onmessage = (e) => {
      if (cancelled) return;
      try { handler(JSON.parse(e.data) as TelemetryEvent); } catch {}
    };
    return () => {
      cancelled = true;
      if (es) es.close();
    };
  }, [projectId]);
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
interface Agent {
  id: number;
  name: string;
  status: string;
  mode?: string;
  project_id?: string;
}
interface MCPServerConfig {
  name?: string;
  transport?: string;
  url?: string;
  command?: string;
  args?: string[];
}
interface AgentLiveState {
  agent_id: number;
  state: "idle" | "thinking" | "tool" | "error" | "thread" | "event";
  label: string;
  detail?: string;
  tool?: string;
  symbol?: string;
  portfolio_id?: number;
  thread_id?: string;
  last_summary?: string;
  last_event_at?: string;
  trading_event_at?: string;
}
interface AgentActivity {
  id: string;
  agent_id: number;
  kind: "thinking" | "market" | "portfolio" | "risk" | "order" | "journal" | "alert" | "thread" | "error" | "event";
  summary: string;
  detail?: string;
  tool?: string;
  symbol?: string;
  portfolio_id?: number;
  thread_id?: string;
  time: string;
  status?: "running" | "success" | "error" | "info";
}
interface Bar {
  t: number;
  o?: number; h?: number; l?: number; c?: number; v?: number;
  yes?: number;
}
interface HistoryResp { symbol: string; range: string; bars: Bar[] }
interface BacktestRun {
  id: number;
  portfolio_id: number;
  source_agent_id: number;
  environment_id?: string;
  environment_agent_id?: number;
  environment_portfolio_id?: number;
  name: string;
  status: "queued" | "running" | "completed" | "failed" | "cancelled";
  symbols: string[];
  start_at: string;
  end_at: string;
  interval: string;
  starting_cash: number;
  fee_bps: number;
  slippage_bps: number;
  current_step: number;
  total_steps: number;
  summary?: { prices?: Array<{ symbol: string; price: number; asset_class?: string }> };
  error?: string;
  created_at: string;
  updated_at: string;
}
interface BacktestEvent {
  id: number;
  run_id: number;
  kind: string;
  message: string;
  data?: Record<string, unknown>;
  created_at: string;
}
interface BacktestLiveEvent {
  id: string;
  kind: AgentActivity["kind"];
  summary: string;
  detail?: string;
  time: string;
  status?: AgentActivity["status"];
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
function durationLabel(ms: unknown): string {
  if (typeof ms !== "number" || !Number.isFinite(ms)) return "";
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${ms}ms`;
}
function inferAssetClass(symbol: string): string {
  const s = symbol.toUpperCase().trim();
  if (s.startsWith("POLY:")) return "polymarket";
  if (s.endsWith("-USD")) return "crypto";
  return "equity";
}
// Per-asset-class accent — used for chips and badges so the operator
// can scan a mixed-class portfolio quickly. Maps to standard Tailwind
// utility colors the dashboard already ships.
function classBadgeClass(c: string): string {
  switch (c) {
    case "crypto": return "bg-amber/10 text-amber border-amber/40";
    case "equity":
    case "etf": return "bg-blue-500/20 text-blue-400 border-blue-500/30";
    case "polymarket": return "bg-accent/15 text-accent border-accent/50";
    default: return "bg-bg-input text-text-muted border-border";
  }
}

const TRADING_TOOLS = new Set([
  "portfolio_create", "brokers_list", "portfolio_list", "portfolio_get",
  "account_summary", "positions_list", "orders_list", "order_place",
  "order_cancel", "market_quote", "market_history", "market_source",
  "watchlist_add", "watchlist_remove", "alert_create", "journal_write",
  "journal_read", "portfolio_pause",
]);

function compactText(value: unknown, fallback = ""): string {
  if (value == null) return fallback;
  const text = String(value).replace(/\s+/g, " ").trim();
  return text || fallback;
}

function parsePayload(value: unknown): unknown {
  if (typeof value !== "string") return value;
  const trimmed = value.trim();
  if (!trimmed || !["{", "["].includes(trimmed[0])) return value;
  try { return JSON.parse(trimmed); } catch { return value; }
}

function objectPayload(value: unknown): Record<string, any> {
  const parsed = parsePayload(value);
  return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, any> : {};
}

function toolBaseName(name: unknown): string {
  const raw = compactText(name);
  if (!raw) return "";
  const parts = raw.split(/__|[./:]/).filter(Boolean);
  return parts[parts.length - 1] || raw;
}

function tradingToolName(name: unknown): string {
  const base = toolBaseName(name);
  return base.startsWith("trading_") ? base.slice("trading_".length) : base;
}

function toolArgs(data: Record<string, any>): Record<string, any> {
  return objectPayload(data.args ?? data.arguments ?? data.input ?? data.params);
}

function toolResult(data: Record<string, any>): Record<string, any> {
  return objectPayload(data.result ?? data.output ?? data.message);
}

function extractSymbol(payload: Record<string, any>): string | undefined {
  const direct = payload.symbol ?? payload.ticker ?? payload.market_symbol;
  if (typeof direct === "string" && direct.trim()) return direct.trim().toUpperCase();
  const nested = payload.order || payload.quote || payload.position || payload.market;
  if (nested && typeof nested === "object") return extractSymbol(nested as Record<string, any>);
  return undefined;
}

function extractPortfolioID(payload: Record<string, any>): number | undefined {
  const raw = payload.portfolio_id ?? payload.portfolioId ?? payload.id;
  const n = typeof raw === "number" ? raw : Number(raw);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

function portfolioHasSymbol(portfolio: Portfolio | null, symbol?: string): boolean {
  if (!portfolio || !symbol) return false;
  const wanted = symbol.toUpperCase();
  return (portfolio.watchlist || []).some((s) => s.toUpperCase() === wanted);
}

function portfolioAgentID(portfolio: Portfolio | null): number | undefined {
  const raw = compactText(portfolio?.agent_id);
  if (!raw) return undefined;
  const match = raw.match(/(\d+)$/);
  const n = match ? Number(match[1]) : Number(raw);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

function isTradingMCPServer(server: MCPServerConfig): boolean {
  const name = compactText(server.name).toLowerCase();
  const url = compactText(server.url).toLowerCase();
  return name === "trading" || url.includes("/api/apps/trading/mcp");
}

function activityKindForTool(tool: string): AgentActivity["kind"] {
  if (tool.startsWith("market_")) return "market";
  if (tool.includes("position") || tool === "account_summary") return "risk";
  if (tool.includes("order")) return "order";
  if (tool.includes("journal")) return "journal";
  if (tool.includes("alert")) return "alert";
  return "portfolio";
}

function labelForTool(tool: string): string {
  switch (tool) {
    case "market_quote": return "checking price";
    case "market_history": return "reading chart";
    case "portfolio_get": return "reviewing portfolio";
    case "account_summary": return "checking account";
    case "positions_list": return "checking exposure";
    case "orders_list": return "reviewing orders";
    case "order_place": return "placing order";
    case "order_cancel": return "cancelling order";
    case "journal_write": return "writing journal";
    case "alert_create": return "setting alert";
    case "portfolio_pause": return "pausing portfolio";
    default: return tool.replace(/_/g, " ");
  }
}

function eventRelevantToPortfolio(
  portfolio: Portfolio | null,
  portfolioID?: number,
  symbol?: string,
): boolean {
  if (!portfolio) return true;
  if (portfolioID != null) return portfolioID === portfolio.id;
  return portfolioHasSymbol(portfolio, symbol);
}

function summarizeTradingTool(tool: string, args: Record<string, any>, result?: Record<string, any>): string {
  const symbol = extractSymbol(args);
  const portfolioID = extractPortfolioID(args);
  const target = symbol || (portfolioID ? `portfolio #${portfolioID}` : "");
  if (tool === "order_place") {
    const side = compactText(args.side).toUpperCase();
    const qty = compactText(args.qty);
    const rejected = result?.status === "rejected" || result?.code || result?.detail;
    const order = [side, qty, symbol].filter(Boolean).join(" ");
    return rejected ? `Order rejected: ${compactText(result?.code || result?.detail, order || "order")}` : `Placed ${order || "order"}`;
  }
  if (tool === "market_quote" && symbol) return `Checked ${symbol} price`;
  if (tool === "market_history" && symbol) return `Reviewed ${symbol} chart`;
  if (tool === "positions_list") return "Reviewed open exposure";
  if (tool === "account_summary") return "Checked account risk";
  if (tool === "portfolio_get") return "Reviewed portfolio state";
  if (tool === "journal_write") return "Wrote portfolio journal note";
  if (tool === "alert_create") return `Created alert${target ? ` for ${target}` : ""}`;
  return `${labelForTool(tool)}${target ? ` · ${target}` : ""}`;
}

// ─── Icons (SVG, theme-aware via currentColor) ─────────────────────

const Icon = {
  Plus:  () => <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round"><path d="M12 5v14M5 12h14"/></svg>,
  X:     () => <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>,
  Pause: () => <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><rect x="6" y="5" width="4" height="14" rx="1"/><rect x="14" y="5" width="4" height="14" rx="1"/></svg>,
  Play:  () => <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z"/></svg>,
  Refresh: () => <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/></svg>,
  ExternalLink: () => <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5"/></svg>,
  Trash: () => <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M19 6l-1 14H6L5 6"/><path d="M10 11v5M14 11v5"/></svg>,
};

// ─── Chart components (hand-rolled SVG, no chart-lib dep) ─────────

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
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);
  const svgRef = useRef<SVGSVGElement | null>(null);

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

  // SVG viewBox. Chart pane reserves 56px on the right for the
  // Y-axis label gutter; X-axis labels are rendered in their own
  // DOM row underneath the SVG (not inside it) so they can't be
  // clipped, scaled, or overlapped by the data line.
  const W = 1000, H = 300;
  const padL = 6, padR = 60, padT = 8, padB = 8;
  const plotW = W - padL - padR;
  const plotH = H - padT - padB;

  const values = bars.map(barValue);
  const times = bars.map((b) => b.t);
  const minRaw = values.length ? Math.min(...values) : 0;
  const maxRaw = values.length ? Math.max(...values) : 1;
  // Pad y-range 5% above/below so the line never touches the edges.
  const headroom = (maxRaw - minRaw || maxRaw) * 0.08;
  const min = minRaw - headroom;
  const max = maxRaw + headroom;
  const range01 = max - min || 1;
  const first = values[0] ?? 0;
  const last = values[values.length - 1] ?? 0;
  const up = last >= first;
  // Soft accent + a tint for direction. Real trading platforms use the
  // accent for "no change" then green/red only when there's signal —
  // avoids the chart screaming red the whole day for a 0.1% move.
  const trendStrong = first > 0 && Math.abs((last - first) / first) > 0.001;
  const lineColor = !trendStrong ? "#60a5fa" : up ? "#22c55e" : "#ef4444";
  const lineColorSoft = lineColor;

  const toX = (i: number) => padL + (i / Math.max(values.length - 1, 1)) * plotW;
  const toY = (v: number) => padT + (1 - (v - min) / range01) * plotH;

  // Smooth path via Catmull-Rom → cubic-Bezier conversion. Real
  // chart libs do this; gives a continuous-looking curve instead
  // of the jagged polyline at low bar counts.
  const pts = values.map((v, i) => [toX(i), toY(v)] as [number, number]);
  const linePath = catmullRomPath(pts);
  const fillPath = linePath + ` L ${toX(values.length - 1).toFixed(2)} ${H - padB} L ${toX(0).toFixed(2)} ${H - padB} Z`;

  // Hover: snap to nearest bar based on cursor X.
  const onMove = (e: React.MouseEvent<SVGSVGElement>) => {
    if (!svgRef.current || values.length === 0) return;
    const rect = svgRef.current.getBoundingClientRect();
    const xPx = e.clientX - rect.left;
    const pctX = xPx / rect.width;
    const xVB = pctX * W;
    if (xVB < padL || xVB > W - padR) { setHoverIdx(null); return; }
    const i = Math.round(((xVB - padL) / plotW) * (values.length - 1));
    setHoverIdx(Math.max(0, Math.min(values.length - 1, i)));
  };
  const onLeave = () => setHoverIdx(null);

  const hoverV = hoverIdx != null ? values[hoverIdx] : null;
  const hoverT = hoverIdx != null ? times[hoverIdx] : null;

  // Y-axis ticks: 4 values across the range (max, two middle, min)
  const yTicks = values.length
    ? [maxRaw, minRaw + (maxRaw - minRaw) * 0.66, minRaw + (maxRaw - minRaw) * 0.33, minRaw]
    : [];

  // X-axis ticks: 4 evenly spaced timestamps.
  const xTicks = values.length >= 2 ? [0, Math.floor(values.length / 3), Math.floor((values.length * 2) / 3), values.length - 1] : [];

  return (
    <Section
      title="Chart"
      action={
        <div className="flex gap-1">
          {CHART_RANGES.map((r) => (
            <button
              key={r}
              onClick={() => setRange(r)}
              className={`px-2 py-0.5 text-xs rounded border ${
                r === range
                  ? "bg-accent text-bg border-accent"
                  : "border-border text-text-muted hover:bg-bg-hover"
              }`}
            >{r}</button>
          ))}
        </div>
      }
    >
      <div className="border border-border rounded bg-bg-card overflow-hidden">
        {/* Header strip: symbol + last + change */}
        <div className="px-3 py-2 flex items-baseline gap-2 border-b border-border">
          <span className="text-sm font-semibold text-text">{symbol}</span>
          <span className={`text-xs px-1.5 py-0.5 rounded font-semibold ${classBadgeClass(assetClass)}`}>{assetClass}</span>
          <span className="flex-1" />
          {values.length > 0 && (
            <>
              <span className="text-base font-semibold tabular-nums text-text">{formatPrice(last, assetClass)}</span>
              {first > 0 && (
                <span className={`text-xs tabular-nums ${trendStrong ? (up ? "text-green" : "text-red") : "text-text-muted"}`}>
                  {formatPct(((last - first) / first) * 100)}
                </span>
              )}
            </>
          )}
        </div>

        {error ? (
          <div className="h-56 flex items-center justify-center text-text-dim text-xs">{error}</div>
        ) : loading && values.length === 0 ? (
          <div className="h-56 flex items-center justify-center text-text-dim text-xs">Loading…</div>
        ) : values.length < 2 ? (
          <div className="h-56 flex items-center justify-center text-text-dim text-xs">No history available</div>
        ) : (
          <div className="relative">
            <svg
              ref={svgRef}
              viewBox={`0 0 ${W} ${H}`}
              preserveAspectRatio="none"
              className="block w-full"
              style={{ height: 240 }}
              onMouseMove={onMove}
              onMouseLeave={onLeave}
            >
              <defs>
                <linearGradient id="trading-chart-fill" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={lineColorSoft} stopOpacity="0.28" />
                  <stop offset="100%" stopColor={lineColorSoft} stopOpacity="0" />
                </linearGradient>
              </defs>

              {/* Horizontal gridlines, one per Y-tick value */}
              {yTicks.map((v, i) => {
                const y = toY(v);
                return (
                  <line
                    key={`gy-${i}`}
                    x1={padL} x2={W - padR} y1={y} y2={y}
                    stroke="currentColor"
                    className="text-border"
                    strokeWidth="1"
                    vectorEffect="non-scaling-stroke"
                    strokeDasharray="2 4"
                    strokeOpacity="0.5"
                  />
                );
              })}

              {/* Area fill */}
              <path d={fillPath} fill="url(#trading-chart-fill)" />

              {/* The price line — vector-effect keeps the stroke width
                  constant regardless of horizontal SVG stretching, which
                  is what fixes the old "fat blob" rendering. */}
              <path
                d={linePath}
                fill="none"
                stroke={lineColor}
                strokeWidth="1.5"
                vectorEffect="non-scaling-stroke"
                strokeLinejoin="round"
                strokeLinecap="round"
              />

              {/* Last-price horizontal dashed line + tick on the right */}
              <line
                x1={padL} x2={W - padR}
                y1={toY(last)} y2={toY(last)}
                stroke={lineColor}
                strokeWidth="1"
                vectorEffect="non-scaling-stroke"
                strokeDasharray="3 4"
                strokeOpacity="0.5"
              />

              {/* Hover crosshair + price dot */}
              {hoverIdx != null && (
                <>
                  <line
                    x1={toX(hoverIdx)} x2={toX(hoverIdx)}
                    y1={padT} y2={H - padB}
                    stroke="currentColor"
                    className="text-text-dim"
                    strokeWidth="1"
                    vectorEffect="non-scaling-stroke"
                    strokeDasharray="2 3"
                  />
                  <circle
                    cx={toX(hoverIdx)} cy={toY(values[hoverIdx])}
                    r="3"
                    fill={lineColor}
                    stroke="var(--bg-card, #111)"
                    strokeWidth="1.5"
                    vectorEffect="non-scaling-stroke"
                  />
                </>
              )}
            </svg>

            {/* Y-axis price labels overlaid on the right side (DOM, not SVG,
                so they're crisp and don't stretch with viewBox). */}
            <div className="absolute top-0 bottom-0 right-0 pointer-events-none" style={{ width: 56 }}>
              {yTicks.map((v, i) => {
                // toY returns a value in viewBox units (0..H); convert to
                // a CSS percentage of the SVG container's height.
                const pctTop = (toY(v) / H) * 100;
                return (
                  <div
                    key={`yl-${i}`}
                    className="absolute right-1 text-xs text-text-dim tabular-nums"
                    style={{ top: `calc(${pctTop}% - 7px)` }}
                  >
                    {formatYTick(v, assetClass)}
                  </div>
                );
              })}
              {/* Last-price chip — sits on the right edge at lastY,
                  highlighted with the line color. */}
              <div
                className="absolute right-0 px-1.5 py-0.5 text-xs font-semibold tabular-nums rounded-sm pointer-events-none"
                style={{
                  top: `calc(${(toY(last) / H) * 100}% - 9px)`,
                  background: lineColor,
                  color: "var(--bg, #fff)",
                }}
              >
                {formatYTick(last, assetClass)}
              </div>
            </div>

            {/* Hover tooltip — floating chip above the crosshair */}
            {hoverIdx != null && hoverV != null && hoverT != null && (
              <div
                className="absolute pointer-events-none px-2 py-1 text-xs rounded bg-bg border border-border shadow"
                style={{
                  left: `calc(${(toX(hoverIdx) / W) * 100}% + 6px)`,
                  top: `calc(${(toY(hoverV) / H) * 100}% - 32px)`,
                  // Don't let the tooltip run past the right edge into
                  // the Y-axis label gutter.
                  maxWidth: 160,
                }}
              >
                <div className="text-text font-semibold tabular-nums">{formatPrice(hoverV, assetClass)}</div>
                <div className="text-text-dim">{formatTimeTick(hoverT, range)}</div>
              </div>
            )}
          </div>
        )}

        {/* X-axis time labels — rendered as a sibling row of the chart
            pane, NOT inside the SVG container. Lives in its own band so
            the labels can't be clipped, hidden behind the gradient
            fill, or scaled by the viewBox stretching. Right padding
            matches the Y-axis label gutter (56px) so the ticks line up
            with the plot area, not the price labels. */}
        {values.length >= 2 && (
          <div
            className="flex justify-between text-xs text-text-dim tabular-nums px-2 py-1 border-t border-border"
            style={{ paddingRight: 60 }}
          >
            {xTicks.map((i, k) => (
              <span key={k}>{formatTimeTick(times[i], range)}</span>
            ))}
          </div>
        )}
      </div>
    </Section>
  );
}

// ─── Chart helpers ────────────────────────────────────────────────

// catmullRomPath — converts a list of points to an SVG `d` string that
// smoothly interpolates between them with cubic Bezier curves derived
// from the Catmull-Rom spline. Tension = 0.5 (the canonical "uniform"
// flavor used by most chart libraries).
function catmullRomPath(points: [number, number][]): string {
  if (points.length === 0) return "";
  if (points.length === 1) return `M ${points[0][0]} ${points[0][1]}`;
  if (points.length === 2) return `M ${points[0][0]} ${points[0][1]} L ${points[1][0]} ${points[1][1]}`;
  let d = `M ${points[0][0]} ${points[0][1]}`;
  for (let i = 0; i < points.length - 1; i++) {
    const p0 = points[Math.max(i - 1, 0)];
    const p1 = points[i];
    const p2 = points[i + 1];
    const p3 = points[Math.min(i + 2, points.length - 1)];
    const cp1x = p1[0] + (p2[0] - p0[0]) / 6;
    const cp1y = p1[1] + (p2[1] - p0[1]) / 6;
    const cp2x = p2[0] - (p3[0] - p1[0]) / 6;
    const cp2y = p2[1] - (p3[1] - p1[1]) / 6;
    d += ` C ${cp1x.toFixed(2)} ${cp1y.toFixed(2)}, ${cp2x.toFixed(2)} ${cp2y.toFixed(2)}, ${p2[0].toFixed(2)} ${p2[1].toFixed(2)}`;
  }
  return d;
}

// formatYTick — terse number formatter for axis labels. The bottom-row
// formatPrice has the dollar sign + commas; here we want the smallest
// readable form (no `$` to save horizontal room, since the chip is
// narrow and the asset-class chip in the header already conveys the
// currency).
function formatYTick(v: number, assetClass: string): string {
  if (!isFinite(v)) return "—";
  if (assetClass === "polymarket") return `${(v * 100).toFixed(0)}¢`;
  const abs = Math.abs(v);
  if (abs >= 10_000) return v.toLocaleString(undefined, { maximumFractionDigits: 0 });
  if (abs >= 100)    return v.toLocaleString(undefined, { maximumFractionDigits: 1 });
  if (abs >= 1)      return v.toFixed(2);
  return v.toFixed(4);
}

// formatTimeTick — chart x-axis labels per range. Time-of-day for
// intraday, dates for multi-day. Uses Intl.DateTimeFormat for proper
// locale handling.
function formatTimeTick(unixSeconds: number, range: ChartRange): string {
  const d = new Date(unixSeconds * 1000);
  if (!isFinite(d.getTime())) return "";
  switch (range) {
    case "1D":
      return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
    case "5D":
      return d.toLocaleString([], { weekday: "short", hour: "2-digit", hour12: false });
    case "1M":
    case "3M":
      return d.toLocaleDateString([], { month: "short", day: "numeric" });
    case "1Y":
    case "ALL":
      return d.toLocaleDateString([], { month: "short", year: "2-digit" });
  }
}

function Sparkline({ values, up, width = 80, height = 24 }: {
  values: number[]; up: boolean; width?: number; height?: number;
}) {
  if (values.length < 2) return <svg width={width} height={height} />;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min || 1;
  const toX = (i: number) => (i / (values.length - 1)) * width;
  const toY = (v: number) => 1 + (height - 2) * (1 - (v - min) / range);
  const pts = values.map((v, i) => [toX(i), toY(v)] as [number, number]);
  const d = catmullRomPath(pts);
  const color = up ? "#22c55e" : "#ef4444";
  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className="block">
      <path d={d} fill="none" stroke={color} strokeWidth="1.2" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}

// ─── Layout primitives ─────────────────────────────────────────────

function Section({ title, action, children }: {
  title: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className="mb-6">
      <div className="flex items-center justify-between mb-2">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-text-dim">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  );
}

function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="p-8 text-center text-text-muted text-sm">
      <div className="font-medium text-text mb-1">{title}</div>
      {hint && <div className="opacity-80 text-xs">{hint}</div>}
    </div>
  );
}

function ErrorBar({ text, onDismiss }: { text: string; onDismiss: () => void }) {
  return (
    <div className="px-4 py-2 bg-error/10 text-error text-sm flex items-center gap-2 border-b border-error/30">
      <span className="flex-1">{text}</span>
      <button onClick={onDismiss} className="p-1 rounded hover:bg-error/20"><Icon.X /></button>
    </div>
  );
}

function PortfolioStatusPill({ status, mode }: { status: string; mode: string }) {
  const cls =
    status === "active"
      ? "bg-green/10 text-green border-green/40"
      : status === "halted"
      ? "bg-red/10 text-red border-red/40"
      : "bg-amber/10 text-amber border-amber/40";
  return (
    <span className={`text-xs px-2 py-0.5 rounded-full border uppercase tracking-wide font-semibold inline-flex items-center gap-1 ${cls}`}>
      {status}
      {mode === "live" && <span className="opacity-70 normal-case">· live</span>}
    </span>
  );
}

function OrderStatusPill({ status }: { status: string }) {
  const cls =
    status === "working"   ? "bg-blue-500/20 text-blue-400" :
    status === "filled"    ? "bg-green/10 text-green" :
    status === "cancelled" ? "bg-bg-input text-text-muted" :
    status === "rejected"  ? "bg-red/10 text-red" :
                              "bg-bg-input text-text-muted";
  return <span className={`text-xs px-2 py-0.5 rounded uppercase font-semibold tracking-wide ${cls}`}>{status}</span>;
}

function pnlClass(n: number | undefined): string {
  if (n == null || n === 0) return "text-text";
  return n > 0 ? "text-green" : "text-red";
}

// ─── Main ──────────────────────────────────────────────────────────

type TabId = "portfolios" | "trade" | "positions" | "agents" | "backtests" | "brokers" | "journal";

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
    const res = await fetch(`${API}${path}?${withParams(params || {})}`, opts);
    if (!res.ok) {
      let detail = ""; try { detail = await res.text(); } catch {}
      throw new Error(`${res.status}: ${detail || res.statusText}`);
    }
    return res.json() as Promise<T>;
  }, [withParams]);

  const loadPortfolios = useCallback(async () => {
    try {
      const r = await api<{ portfolios?: Portfolio[] }>("GET", "/portfolios");
      const list = r.portfolios || [];
      setPortfolios(list);
      setSelectedId((cur) => cur ?? (list.length > 0 ? list[0].id : null));
      setError(null);
    } catch (e) { setError((e as Error).message); }
  }, [api]);

  useEffect(() => { loadPortfolios(); }, [loadPortfolios]);

  useAppEvents("trading", projectId, (ev) => {
    if (["portfolio.created", "portfolio.status.changed", "portfolio.agent.changed", "order.filled", "position.changed"].includes(ev.topic)) {
      loadPortfolios();
    }
  });

  const selected = useMemo(
    () => portfolios.find((p) => p.id === selectedId) || null,
    [portfolios, selectedId],
  );

  return (
    <div className="h-full flex flex-col bg-bg text-text text-sm">
      <header className="px-4 py-2 flex items-center gap-3 border-b border-border">
        <h1 className="text-sm font-semibold m-0">Trading</h1>
        <select
          value={selectedId ?? ""}
          onChange={(e) => setSelectedId(e.target.value ? Number(e.target.value) : null)}
          className="text-xs px-2 py-1 bg-bg-input border border-border rounded text-text"
        >
          <option value="">— Select portfolio —</option>
          {portfolios.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name} {p.mode === "live" ? "(LIVE)" : ""}
            </option>
          ))}
        </select>
        {selected && <PortfolioStatusPill status={selected.status} mode={selected.mode} />}
        <span className="flex-1" />
        <button
          onClick={loadPortfolios}
          title="Refresh"
          disabled={busy}
          className="p-1.5 rounded border border-border text-text-muted hover:bg-bg-hover disabled:opacity-50"
        ><Icon.Refresh /></button>
        <a
          href="../desk/dist/"
          target="_blank"
          rel="noopener"
          className="text-xs text-text-dim hover:text-accent inline-flex items-center gap-1"
          title="Open the rich desk SPA"
        >Desk <Icon.ExternalLink /></a>
      </header>

      <nav className="flex border-b border-border px-3 text-xs">
        {(["portfolios","trade","positions","agents","backtests","brokers","journal"] as TabId[]).map((id) => {
          const active = id === tab;
          return (
            <button
              key={id}
              onClick={() => setTab(id)}
              className={`px-3 py-2 capitalize ${
                active
                  ? "text-text font-semibold border-b-2 border-accent -mb-px"
                  : "text-text-muted hover:text-text border-b-2 border-transparent -mb-px"
              }`}
            >{id}</button>
          );
        })}
      </nav>

      {error && <ErrorBar text={error} onDismiss={() => setError(null)} />}

      <div className="flex-1 overflow-auto p-4">
        {tab === "portfolios" && (
          <PortfoliosTab
            portfolios={portfolios} selectedId={selectedId} onSelect={setSelectedId}
            api={api} onChanged={loadPortfolios} setBusy={setBusy} setError={setError}
          />
        )}
        {tab === "trade" && (
          <TradeTab portfolio={selected} api={api} setBusy={setBusy} setError={setError} projectId={projectId} />
        )}
        {tab === "positions" && (
          <PositionsTab portfolio={selected} api={api} setError={setError} />
        )}
        {tab === "agents" && (
          <AgentsTab portfolio={selected} api={api} projectId={projectId} onChanged={loadPortfolios} setError={setError} />
        )}
        {tab === "backtests" && (
          <BacktestsTab portfolio={selected} api={api} projectId={projectId} setError={setError} />
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

// ─── Portfolios tab ────────────────────────────────────────────────

function PortfoliosTab({ portfolios, selectedId, onSelect, api, onChanged, setBusy, setError }: {
  portfolios: Portfolio[];
  selectedId: number | null;
  onSelect: (id: number) => void;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  onChanged: () => void;
  setBusy: (b: boolean) => void;
  setError: (e: string | null) => void;
}) {
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
    <Section
      title="Portfolios"
      action={
        <button
          onClick={() => setShowCreate((s) => !s)}
          className="px-3 py-1 text-xs rounded bg-accent text-bg font-medium inline-flex items-center gap-1 hover:opacity-90"
        ><Icon.Plus /> New</button>
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
      {portfolios.length === 0 && !showCreate ? (
        <EmptyState title="No portfolios yet" hint="Click New to create your first paper portfolio." />
      ) : (
        <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))" }}>
          {portfolios.map((p) => {
            const isSelected = p.id === selectedId;
            return (
              <div
                key={p.id}
                onClick={() => onSelect(p.id)}
                className={`p-3 rounded border cursor-pointer transition-colors ${
                  isSelected
                    ? "border-accent bg-accent/15"
                    : "border-border bg-bg-card hover:bg-bg-hover"
                }`}
              >
                <div className="flex items-center justify-between mb-2">
                  <strong className="text-sm">{p.name}</strong>
                  <PortfolioStatusPill status={p.status} mode={p.mode} />
                </div>
                {p.broker_slug && (
                  <div className="text-xs text-text-dim mb-1">via {p.broker_slug}</div>
                )}
                {p.mandate && (
                  <div className="text-xs text-text-muted italic mb-2">
                    "{p.mandate.length > 80 ? p.mandate.slice(0, 80) + "…" : p.mandate}"
                  </div>
                )}
                <div className="grid grid-cols-2 gap-2 text-xs">
                  <Stat label="Equity" value={formatUSD(p.equity)} />
                  <Stat label="Cash" value={formatUSD(p.cash)} />
                  <Stat label="Day P&L" value={formatUSD(p.day_pnl)} sub={formatPct(p.day_pnl_pct)} colorClass={pnlClass(p.day_pnl)} />
                  <Stat label="Open P&L" value={formatUSD(p.open_pnl)} sub={formatPct(p.open_pnl_pct)} colorClass={pnlClass(p.open_pnl)} />
                </div>
                <div className="flex gap-1 mt-2 flex-wrap items-center">
                  {p.allowed_classes.map((c) => (
                    <span key={c} className={`text-xs px-2 py-0.5 rounded-full border font-semibold ${classBadgeClass(c)}`}>{c}</span>
                  ))}
                  <span className="flex-1" />
                  <button
                    onClick={(e) => { e.stopPropagation(); togglePause(p); }}
                    className="p-1 rounded border border-border text-text-muted hover:bg-bg-hover"
                    title={p.status === "active" ? "Pause" : "Resume"}
                  >{p.status === "active" ? <Icon.Pause /> : <Icon.Play />}</button>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </Section>
  );
}

function Stat({ label, value, sub, colorClass }: { label: string; value: string; sub?: string; colorClass?: string }) {
  return (
    <div>
      <div className="text-xs uppercase tracking-wide text-text-dim font-medium">{label}</div>
      <div className={`font-semibold ${colorClass || "text-text"}`}>
        {value}{sub && <span className="text-xs font-normal opacity-80 ml-1">{sub}</span>}
      </div>
    </div>
  );
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
  useEffect(() => { if (adapter) setClasses(adapter.asset_classes); }, [adapter]);

  const submit = async () => {
    if (!name.trim()) return;
    setSubmitting(true);
    try {
      const body: Record<string, unknown> = {
        name: name.trim(), mandate: mandate.trim(), mode, allowed_classes: classes,
      };
      if (mode === "paper") body.starting_cash = Number(startingCash);
      if (mode === "live") body.broker_slug = brokerSlug;
      await api("POST", "/portfolios", undefined, body);
      onCreated();
    } catch (e) { setError((e as Error).message); } finally { setSubmitting(false); }
  };

  const liveBrokers = brokers.filter((b) => b.bound);

  return (
    <div className="p-4 mb-4 border border-border rounded bg-bg-card">
      <div className="flex gap-4 mb-3">
        <label className="text-sm flex items-center gap-2 cursor-pointer">
          <input type="radio" checked={mode === "paper"} onChange={() => setMode("paper")} />
          Paper
        </label>
        <label className="text-sm flex items-center gap-2 cursor-pointer">
          <input type="radio" checked={mode === "live"} onChange={() => setMode("live")} disabled={liveBrokers.length === 0} />
          Live
          {liveBrokers.length === 0 && (
            <span className="text-xs text-text-dim">(no broker bound — see Brokers tab)</span>
          )}
        </label>
      </div>

      <div className="grid grid-cols-2 gap-3 mb-3">
        <div>
          <FieldLabel>Name</FieldLabel>
          <input value={name} onChange={(e) => setName(e.target.value)} className={inputClass} placeholder="e.g. tech-longs" />
        </div>
        {mode === "paper" ? (
          <div>
            <FieldLabel>Starting cash (USD)</FieldLabel>
            <input value={startingCash} onChange={(e) => setStartingCash(e.target.value)} className={inputClass} type="number" />
          </div>
        ) : (
          <div>
            <FieldLabel>Broker</FieldLabel>
            <select value={brokerSlug} onChange={(e) => setBrokerSlug(e.target.value)} className={inputClass}>
              <option value="">— Pick —</option>
              {liveBrokers.map((b) => (
                <option key={b.slug} value={b.slug}>{b.slug} ({b.asset_classes.join(", ")})</option>
              ))}
            </select>
          </div>
        )}
      </div>

      <div className="mb-3">
        <FieldLabel>Mandate</FieldLabel>
        <textarea
          value={mandate} onChange={(e) => setMandate(e.target.value)}
          className={`${inputClass} min-h-16 resize-y`}
          placeholder="What this portfolio is for (free-text; surfaced to the agent's prompt)."
        />
      </div>

      <div className="mb-3">
        <FieldLabel>Asset classes</FieldLabel>
        <div className="flex gap-1.5 flex-wrap">
          {(adapter?.asset_classes || ["equity", "etf", "crypto", "polymarket"]).map((c) => {
            const on = classes.includes(c);
            return (
              <button
                key={c}
                onClick={() => setClasses((cs) => on ? cs.filter((x) => x !== c) : [...cs, c])}
                className={`text-xs px-3 py-1 rounded-full border font-medium ${
                  on ? classBadgeClass(c) : "border-border text-text-muted hover:bg-bg-hover"
                }`}
              >{c}</button>
            );
          })}
        </div>
      </div>

      <div className="flex gap-2 justify-end">
        <button onClick={onCancel} className="px-3 py-1 text-sm rounded border border-border text-text hover:bg-bg-hover">Cancel</button>
        <button
          onClick={submit}
          disabled={submitting || !name.trim() || (mode === "live" && !brokerSlug)}
          className="px-3 py-1 text-sm rounded bg-accent text-bg font-medium hover:opacity-90 disabled:opacity-50"
        >{submitting ? "Creating…" : "Create portfolio"}</button>
      </div>
    </div>
  );
}

function FieldLabel({ children }: { children: React.ReactNode }) {
  return <label className="block text-xs uppercase tracking-wide font-medium text-text-dim mb-1">{children}</label>;
}
const inputClass = "w-full text-sm px-2 py-1.5 bg-bg-input border border-border rounded text-text";

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="p-2 rounded bg-bg-input border border-border">
      <FieldLabel>{label}</FieldLabel>
      <div className="text-sm font-medium text-text tabular-nums">{value}</div>
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
        {working.length === 0 ? <EmptyState title="No working orders" /> : <OrdersTable orders={working} onCancel={cancel} />}
      </Section>
      <Section title="Recent orders">
        {orders.length === 0 ? <EmptyState title="No orders yet" /> : <OrdersTable orders={orders} />}
      </Section>
    </>
  );
}

function StatsCard({ portfolio }: { portfolio: Portfolio }) {
  const cells = [
    { label: "Equity", value: formatUSD(portfolio.equity) },
    { label: "Cash", value: formatUSD(portfolio.cash) },
    { label: "Buying power", value: formatUSD(portfolio.buying_power) },
    { label: "Day P&L", value: formatUSD(portfolio.day_pnl), sub: formatPct(portfolio.day_pnl_pct), colorClass: pnlClass(portfolio.day_pnl) },
    { label: "Open P&L", value: formatUSD(portfolio.open_pnl), sub: formatPct(portfolio.open_pnl_pct), colorClass: pnlClass(portfolio.open_pnl) },
  ];
  return (
    <div className="grid gap-2 mb-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(140px, 1fr))" }}>
      {cells.map((c) => (
        <div key={c.label} className="px-3 py-2 bg-bg-card border border-border rounded">
          <div className="text-xs uppercase tracking-wide text-text-dim font-medium">{c.label}</div>
          <div className={`text-base font-semibold ${c.colorClass || "text-text"}`}>
            {c.value} {c.sub && <span className="text-xs opacity-80 font-normal">{c.sub}</span>}
          </div>
        </div>
      ))}
    </div>
  );
}

function PlaceOrderFormWithChart({ portfolio, api, onPlaced, setError }: {
  portfolio: Portfolio;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  onPlaced: () => void;
  setError: (e: string | null) => void;
}) {
  const [symbol, setSymbol] = useState("");
  const [universe, setUniverse] = useState<Mark[]>([]);
  const assetClass = inferAssetClass(symbol);

  // Pull the engine's known universe once — these are the symbols we
  // have marks for + the symbols the provider knows how to quote
  // (mock universe, plus binance-public crypto, plus polymarket gamma,
  // plus equity via alpaca-market-data when bound). Refreshed on the
  // panel's lifecycle; live ticks update marks server-side and the
  // SymbolSelect's per-symbol formatPrice picks up new prices from
  // the cached snapshot if needed.
  useEffect(() => {
    api<{ symbols: Mark[] }>("GET", "/universe")
      .then((r) => setUniverse(r.symbols || []))
      .catch(() => undefined);
  }, [api]);

  return (
    <>
      <WatchlistChips
        portfolio={portfolio}
        api={api}
        currentSymbol={symbol}
        onSelect={setSymbol}
        setError={setError}
      />
      <PriceChart symbol={symbol.trim()} assetClass={assetClass} api={api} />
      <PlaceOrderForm
        portfolio={portfolio} api={api} onPlaced={onPlaced} setError={setError}
        symbol={symbol} setSymbol={setSymbol}
        universe={universe}
      />
    </>
  );
}

// SymbolSelect — combobox over the engine's known universe. Filters by
// case-insensitive prefix as the operator types, shows asset-class
// chip + current mark per row, commits on click / Enter (so the chart
// doesn't refetch on every keystroke). Free-form text submission still
// works — useful for live brokers (Alpaca, Binance) that support
// thousands of symbols beyond what our universe knows about.
function SymbolSelect({ value, onCommit, universe, allowedClasses }: {
  value: string;
  onCommit: (s: string) => void;
  universe: Mark[];
  allowedClasses?: string[];
}) {
  const [query, setQuery] = useState(value);
  const [open, setOpen] = useState(false);
  const [activeIdx, setActiveIdx] = useState(0);
  const wrapRef = useRef<HTMLDivElement | null>(null);

  // Sync local query with parent's committed value (so click-a-chip
  // updates what's in the box).
  useEffect(() => { setQuery(value); }, [value]);

  // Close dropdown on outside click.
  useEffect(() => {
    const onDocClick = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", onDocClick);
    return () => document.removeEventListener("mousedown", onDocClick);
  }, []);

  const q = query.trim().toUpperCase();
  const matches = useMemo(() => {
    let pool = universe;
    if (allowedClasses && allowedClasses.length > 0) {
      pool = pool.filter((m) => allowedClasses.includes(m.asset_class));
    }
    if (q === "") return pool.slice(0, 50);
    return pool
      .filter((m) => m.symbol.toUpperCase().includes(q))
      .slice(0, 50);
  }, [q, universe, allowedClasses?.join(",")]);

  const commit = (s: string) => {
    const v = s.trim();
    setQuery(v);
    onCommit(v);
    setOpen(false);
  };

  return (
    <div ref={wrapRef} className="relative">
      <input
        value={query}
        onChange={(e) => { setQuery(e.target.value); setOpen(true); setActiveIdx(0); }}
        onFocus={() => setOpen(true)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") { e.preventDefault(); setActiveIdx((i) => Math.min(i + 1, matches.length - 1)); }
          else if (e.key === "ArrowUp") { e.preventDefault(); setActiveIdx((i) => Math.max(i - 1, 0)); }
          else if (e.key === "Enter") {
            e.preventDefault();
            if (open && matches[activeIdx]) commit(matches[activeIdx].symbol);
            else commit(query);
          } else if (e.key === "Escape") setOpen(false);
        }}
        onBlur={() => { /* commit happens via Enter/click; blur just commits typed value too */ commit(query); }}
        className={inputClass}
        placeholder="Search BTC, AAPL, POLY:… (type or pick)"
        autoComplete="off"
      />
      {open && matches.length > 0 && (
        <div className="absolute z-10 left-0 right-0 mt-1 max-h-64 overflow-auto bg-bg-card border border-border rounded shadow-lg">
          {matches.map((m, i) => {
            const active = i === activeIdx;
            const price = m.price ?? m.yes_price;
            const chg = m.change_pct_24h;
            return (
              <div
                key={m.symbol}
                onMouseDown={(e) => { e.preventDefault(); commit(m.symbol); }}
                onMouseEnter={() => setActiveIdx(i)}
                className={`flex items-center gap-2 px-2 py-1.5 text-xs cursor-pointer ${active ? "bg-bg-hover" : ""}`}
              >
                <span className={`px-2 py-0.5 rounded-full border font-semibold text-xs ${classBadgeClass(m.asset_class)}`}
                  style={{ minWidth: 60, textAlign: "center" }}>
                  {m.asset_class}
                </span>
                <span className="font-semibold text-text flex-1">{m.symbol}</span>
                {price != null && (
                  <span className="text-text-muted tabular-nums">{formatPrice(price, m.asset_class)}</span>
                )}
                {chg != null && (
                  <span className={`tabular-nums ${pnlClass(chg)}`} style={{ minWidth: 56, textAlign: "right" }}>
                    {formatPct(chg, 1)}
                  </span>
                )}
              </div>
            );
          })}
        </div>
      )}
      {open && q !== "" && matches.length === 0 && (
        <div className="absolute z-10 left-0 right-0 mt-1 px-2 py-2 bg-bg-card border border-border rounded text-xs text-text-dim">
          No match in known universe — press Enter to use "{query.trim()}" anyway
          (live brokers may support more symbols than we track).
        </div>
      )}
    </div>
  );
}

// WatchlistChips — click-to-load symbol chips above the chart. Each
// chip shows the symbol's current mark + 24h change; clicking sets
// the symbol input so the chart renders and the order form is ready
// to fill. The "+" button on the right adds the current symbol input
// to the watchlist if it's typed in but not yet tracked.
function WatchlistChips({ portfolio, api, currentSymbol, onSelect, setError }: {
  portfolio: Portfolio;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  currentSymbol: string;
  onSelect: (s: string) => void;
  setError: (e: string | null) => void;
}) {
  const watchlist = portfolio.watchlist || [];
  const [quotes, setQuotes] = useState<Record<string, Mark>>({});
  const [busy, setBusy] = useState(false);

  // Per-symbol quote fetch. Parallel, stale-while-revalidate — the
  // chips render with whatever marks we already cached; new arrivals
  // patch into the map.
  useEffect(() => {
    let cancelled = false;
    Promise.all(
      watchlist.map(async (sym) => {
        try {
          const r = await api<Mark>("GET", `/quotes/${encodeURIComponent(sym)}`);
          return [sym, r] as const;
        } catch { return null; }
      }),
    ).then((rows) => {
      if (cancelled) return;
      const next: Record<string, Mark> = {};
      for (const r of rows) if (r) next[r[0]] = r[1];
      setQuotes(next);
    });
    return () => { cancelled = true; };
  }, [watchlist.join(","), api]);

  const typed = currentSymbol.trim();
  const inWatchlist = typed && watchlist.some((s) => s.toUpperCase() === typed.toUpperCase());
  const canAdd = typed && !inWatchlist;

  const addToWatchlist = async () => {
    if (!canAdd) return;
    setBusy(true);
    try {
      await api("POST", `/portfolios/${portfolio.id}/watchlist`, undefined, { symbol: typed });
      // No need to mutate local watchlist — the portfolio reload via
      // SSE (watchlist.changed) will refresh selected.watchlist.
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };

  const removeFromWatchlist = async (sym: string) => {
    setBusy(true);
    try {
      await api("DELETE", `/portfolios/${portfolio.id}/watchlist`, { symbol: sym });
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };

  return (
    <Section
      title="Watchlist"
      action={canAdd && (
        <button
          onClick={addToWatchlist}
          disabled={busy}
          className="px-2 py-0.5 text-xs rounded border border-border text-text-muted hover:bg-bg-hover inline-flex items-center gap-1"
          title={`Track ${typed}`}
        ><Icon.Plus /> Add {typed}</button>
      )}
    >
      {watchlist.length === 0 ? (
        <div className="px-3 py-2 text-xs text-text-dim bg-bg-card border border-border rounded">
          No symbols tracked. Type a symbol below (e.g. <code>BTC-USD</code>, <code>AAPL</code>, <code>POLY:btc-100k-2026</code>) and click + Add.
        </div>
      ) : (
        <div className="flex gap-2 flex-wrap">
          {watchlist.map((sym) => {
            const q = quotes[sym];
            const active = currentSymbol.trim().toUpperCase() === sym.toUpperCase();
            const cls = inferAssetClass(sym);
            const price = q?.price ?? q?.yes_price;
            const chg = q?.change_pct_24h;
            return (
              <div
                key={sym}
                className={`group flex items-center gap-2 px-2 py-1 rounded border cursor-pointer transition-colors ${
                  active
                    ? "border-accent bg-accent/15"
                    : "border-border bg-bg-card hover:bg-bg-hover"
                }`}
                onClick={() => onSelect(sym)}
              >
                <span className={`text-xs font-semibold ${active ? "text-accent" : "text-text"}`}>{sym}</span>
                {price != null && (
                  <span className="text-xs text-text-muted tabular-nums">
                    {formatPrice(price, cls)}
                  </span>
                )}
                {chg != null && (
                  <span className={`text-xs tabular-nums ${pnlClass(chg)}`}>{formatPct(chg, 1)}</span>
                )}
                <button
                  onClick={(e) => { e.stopPropagation(); removeFromWatchlist(sym); }}
                  className="opacity-0 group-hover:opacity-100 text-text-dim hover:text-red"
                  title="Remove"
                ><Icon.X /></button>
              </div>
            );
          })}
        </div>
      )}
    </Section>
  );
}

function PlaceOrderForm({ portfolio, api, onPlaced, setError, symbol, setSymbol, universe }: {
  portfolio: Portfolio;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  onPlaced: () => void;
  setError: (e: string | null) => void;
  symbol: string;
  setSymbol: (s: string) => void;
  universe: Mark[];
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

  // Refresh the inline quote whenever the committed symbol changes.
  // Effect-driven (not callback-driven) so click-a-chip / pick-from-
  // dropdown both fire the fetch without the closure-staleness trap.
  useEffect(() => {
    if (!symbol.trim()) { setQuote(null); return; }
    let cancelled = false;
    api<Mark>("GET", `/quotes/${encodeURIComponent(symbol.trim())}`)
      .then((r) => { if (!cancelled) setQuote(r); })
      .catch(() => { if (!cancelled) setQuote(null); });
    return () => { cancelled = true; };
  }, [symbol, api]);

  const submit = async () => {
    if (!symbol.trim() || !qty || !rationale.trim()) return;
    setSubmitting(true);
    try {
      const body: Record<string, unknown> = {
        symbol: symbol.trim(), side, type,
        qty: Number(qty), rationale: rationale.trim(),
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
      <div className="p-3 border border-border rounded bg-bg-card">
        <div className="grid gap-2 mb-2" style={{ gridTemplateColumns: "2fr 1fr 1fr 1fr" }}>
          <div>
            <FieldLabel>Symbol</FieldLabel>
            <SymbolSelect
              value={symbol}
              onCommit={(s) => setSymbol(s)}
              universe={universe}
              allowedClasses={portfolio.allowed_classes}
            />
            {quote && (
              <div className="text-xs mt-1 text-text-dim">
                Mark: {formatPrice(quote.price ?? quote.yes_price, quote.asset_class)}
                {quote.change_pct_24h != null && (
                  <span className={`ml-1.5 ${pnlClass(quote.change_pct_24h)}`}>{formatPct(quote.change_pct_24h)}</span>
                )}
              </div>
            )}
          </div>
          <div>
            <FieldLabel>Side</FieldLabel>
            <select value={side} onChange={(e) => setSide(e.target.value)} className={inputClass}>
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
            <FieldLabel>Type</FieldLabel>
            <select value={type} onChange={(e) => setType(e.target.value)} className={inputClass}>
              <option value="market">Market</option>
              <option value="limit">Limit</option>
              {!isPoly && <option value="stop">Stop</option>}
            </select>
          </div>
          <div>
            <FieldLabel>Qty</FieldLabel>
            <input value={qty} onChange={(e) => setQty(e.target.value)} type="number" step="any" className={inputClass} />
          </div>
        </div>

        {(type === "limit" || type === "stop") && (
          <div className="grid grid-cols-2 gap-2 mb-2">
            {type === "limit" && (
              <div>
                <FieldLabel>Limit price</FieldLabel>
                <input value={limitPrice} onChange={(e) => setLimitPrice(e.target.value)} type="number" step="any"
                  className={inputClass} placeholder={isPoly ? "0–1 (probability)" : "USD"} />
              </div>
            )}
            {type === "stop" && (
              <div>
                <FieldLabel>Stop price</FieldLabel>
                <input value={stopPrice} onChange={(e) => setStopPrice(e.target.value)} type="number" step="any" className={inputClass} />
              </div>
            )}
          </div>
        )}

        <div className="mb-3">
          <FieldLabel>Rationale (≥30 chars, required)</FieldLabel>
          <textarea
            value={rationale} onChange={(e) => setRationale(e.target.value)}
            className={`${inputClass} min-h-14 resize-y`}
            placeholder="Why are you placing this order? Used for audit + agent introspection."
          />
          <div className="text-xs text-text-dim mt-0.5">{rationale.length}/30</div>
        </div>

        <div className="flex justify-end">
          <button
            onClick={submit}
            disabled={submitting || !symbol.trim() || !qty || rationale.trim().length < 30}
            className="px-3 py-1.5 rounded bg-accent text-bg font-medium hover:opacity-90 disabled:opacity-50"
          >{submitting ? "Placing…" : `Place ${side.toUpperCase()} ${type}`}</button>
        </div>
      </div>
    </Section>
  );
}

function OrdersTable({ orders, onCancel }: { orders: Order[]; onCancel?: (id: string) => void }) {
  return (
    <div className="border border-border rounded overflow-hidden bg-bg-card">
      <table className="w-full text-xs border-collapse">
        <thead className="bg-bg-input text-text-dim">
          <tr>
            {["Order", "Symbol", "Side", "Type", "Qty", "Status", ""].map((h) => (
              <th key={h} className="px-3 py-2 text-left font-semibold uppercase tracking-wide">{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {orders.map((o) => (
            <tr key={o.id} className="border-t border-border">
              <td className="px-3 py-2"><code className="text-xs">{o.id}</code></td>
              <td className="px-3 py-2 font-semibold">{o.symbol}</td>
              <td className={`px-3 py-2 uppercase font-semibold ${o.side === "buy" || o.side === "yes" ? "text-green" : "text-red"}`}>{o.side}</td>
              <td className="px-3 py-2">{o.type}{o.limit_price ? ` @ ${formatPrice(o.limit_price, o.asset_class)}` : ""}</td>
              <td className="px-3 py-2">{formatQty(o.qty)}{o.filled_qty > 0 && o.filled_qty < o.qty && ` (${formatQty(o.filled_qty)} filled)`}</td>
              <td className="px-3 py-2"><OrderStatusPill status={o.status} /></td>
              <td className="px-3 py-2 text-right">
                {onCancel && o.status === "working" && (
                  <button onClick={() => onCancel(o.id)} title="Cancel"
                    className="p-1 rounded border border-border text-text-muted hover:bg-bg-hover">
                    <Icon.X />
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

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

  useEffect(() => {
    let cancelled = false;
    const fetchAll = async () => {
      const results: Record<string, number[]> = {};
      await Promise.all(positions.map(async (p) => {
        try {
          const r = await api<HistoryResp>("GET", `/history/${encodeURIComponent(p.symbol)}`, { range: "1D" });
          results[p.symbol] = (r.bars || []).map(barValue);
        } catch {}
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
      <div className="border border-border rounded overflow-hidden bg-bg-card">
        <table className="w-full text-xs border-collapse">
          <thead className="bg-bg-input text-text-dim">
            <tr>
              {["Symbol","Class","Qty","Avg cost","Mark","Market value","Unrealized","Weight","1D"].map((h) => (
                <th key={h} className="px-3 py-2 text-left font-semibold uppercase tracking-wide">{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {positions.map((p) => {
              const spark = sparklines[p.symbol] || [];
              const sparkUp = spark.length >= 2 ? spark[spark.length - 1] >= spark[0] : true;
              return (
                <tr key={p.symbol + (p.outcome || "")} className="border-t border-border">
                  <td className="px-3 py-2">
                    <strong>{p.symbol}</strong>
                    {p.outcome && <span className="ml-1.5 text-xs text-text-dim">{p.outcome}</span>}
                  </td>
                  <td className="px-3 py-2">
                    <span className={`text-xs px-2 py-0.5 rounded-full border font-semibold ${classBadgeClass(p.asset_class)}`}>{p.asset_class}</span>
                  </td>
                  <td className="px-3 py-2">{formatQty(p.qty)}</td>
                  <td className="px-3 py-2">{formatPrice(p.avg_cost, p.asset_class)}</td>
                  <td className="px-3 py-2">{formatPrice(p.market_price, p.asset_class)}</td>
                  <td className="px-3 py-2">{formatUSD(p.market_value)}</td>
                  <td className={`px-3 py-2 ${pnlClass(p.unrealized_pnl)}`}>
                    {formatUSD(p.unrealized_pnl)} <span className="opacity-70 text-xs">({formatPct(p.unrealized_pnl_pct)})</span>
                  </td>
                  <td className="px-3 py-2">{formatPct(p.weight_pct, 1)}</td>
                  <td className="px-3 py-2"><Sparkline values={spark} up={sparkUp} /></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Section>
  );
}

// ─── Agents tab ───────────────────────────────────────────────────

function AgentsTab({ portfolio, api, projectId, onChanged, setError }: {
  portfolio: Portfolio | null;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  projectId: string;
  onChanged: () => void;
  setError: (e: string | null) => void;
}) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [agentTradingMCPs, setAgentTradingMCPs] = useState<Record<number, string[]>>({});
  const [agentConfigLoading, setAgentConfigLoading] = useState(false);
  const [states, setStates] = useState<Record<number, AgentLiveState>>({});
  const [activities, setActivities] = useState<AgentActivity[]>([]);
  const [quotes, setQuotes] = useState<Record<string, Mark>>({});
  const [bindAgentId, setBindAgentId] = useState<string>("");
  const [binding, setBinding] = useState(false);
  const seenRef = useRef<Set<string>>(new Set());
  const seenOrderRef = useRef<string[]>([]);
  const statesRef = useRef<Record<number, AgentLiveState>>({});

  useEffect(() => {
    const id = portfolioAgentID(portfolio);
    setBindAgentId(id ? String(id) : "");
  }, [portfolio?.id, portfolio?.agent_id]);

  useEffect(() => {
    let cancelled = false;
    const params = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    fetch(`/api/agents${params}`, { credentials: "same-origin" })
      .then((r) => r.ok ? r.json() : Promise.reject(new Error(`${r.status}: ${r.statusText}`)))
      .then((rows: Agent[]) => { if (!cancelled) setAgents(rows || []); })
      .catch((e) => { if (!cancelled) setError((e as Error).message); });
    return () => { cancelled = true; };
  }, [projectId, setError]);

  useEffect(() => {
    let cancelled = false;
    if (agents.length === 0) {
      setAgentTradingMCPs({});
      setAgentConfigLoading(false);
      return () => { cancelled = true; };
    }
    setAgentConfigLoading(true);
    Promise.all(agents.map(async (agent) => {
      try {
        const r = await fetch(`/api/agents/${agent.id}/config`, { credentials: "same-origin" });
        if (!r.ok) return [agent.id, [] as string[]] as const;
        const cfg = await r.json() as { mcp_servers?: MCPServerConfig[] };
        const trading = (cfg.mcp_servers || [])
          .filter(isTradingMCPServer)
          .map((s) => compactText(s.name, "trading"));
        return [agent.id, trading] as const;
      } catch {
        return [agent.id, [] as string[]] as const;
      }
    })).then((rows) => {
      if (cancelled) return;
      const next: Record<number, string[]> = {};
      for (const [id, names] of rows) next[id] = names;
      setAgentTradingMCPs(next);
      setAgentConfigLoading(false);
    });
    return () => { cancelled = true; };
  }, [agents]);

  useEffect(() => {
    if (!portfolio) return;
    setActivities((prev) => prev.filter((a) =>
      eventRelevantToPortfolio(portfolio, a.portfolio_id, a.symbol),
    ));
  }, [portfolio?.id]);

  const upsertState = useCallback((agentID: number, patch: Partial<AgentLiveState>) => {
    setStates((prev) => {
      const { agent_id: _ignored, ...safePatch } = patch;
      const current = prev[agentID] || {
        agent_id: agentID,
        state: "idle" as const,
        label: "Waiting",
      };
      const next = {
        ...prev,
        [agentID]: {
          ...current,
          ...safePatch,
          agent_id: agentID,
        },
      };
      statesRef.current = next;
      return next;
    });
  }, []);

  const addActivity = useCallback((activity: AgentActivity) => {
    setActivities((prev) => {
      if (prev.some((a) => a.id === activity.id)) return prev;
      return [activity, ...prev].slice(0, 80);
    });
  }, []);

  useTelemetryEvents(projectId, (ev) => {
    const dedupeKey = ev.id || `${ev.instance_id}:${ev.thread_id}:${ev.type}:${ev.time}`;
    if (seenRef.current.has(dedupeKey)) return;
    seenRef.current.add(dedupeKey);
    seenOrderRef.current.push(dedupeKey);
    if (seenOrderRef.current.length > 800) {
      const old = seenOrderRef.current.shift();
      if (old) seenRef.current.delete(old);
    }
    const agentID = ev.instance_id;
    if (!agentID) return;

    const data = ev.data || {};
    const nowPatch: Partial<AgentLiveState> = {
      last_event_at: ev.time,
      thread_id: ev.thread_id || "main",
    };

    if (ev.type === "llm.start") {
      upsertState(agentID, { ...nowPatch, state: "thinking", label: "Thinking" });
      return;
    }
    if (ev.type === "llm.done") {
      const summary = compactText(data.message || data.summary);
      const prev = statesRef.current[agentID];
      upsertState(agentID, {
        ...nowPatch,
        state: "idle",
        label: "Waiting",
        detail: summary || prev?.detail,
        last_summary: summary || prev?.last_summary,
      });
      if (summary && eventRelevantToPortfolio(portfolio, prev?.portfolio_id, prev?.symbol) && prev?.trading_event_at) {
        addActivity({
          id: `thought:${dedupeKey}`,
          agent_id: agentID,
          kind: "thinking",
          summary: summary.slice(0, 180),
          symbol: prev.symbol,
          portfolio_id: prev.portfolio_id,
          thread_id: ev.thread_id || "main",
          time: ev.time,
          status: "success",
        });
      }
      return;
    }
    if (ev.type === "llm.error" || ev.type.includes("error")) {
      const msg = compactText(data.error || data.message || ev.type, ev.type);
      upsertState(agentID, { ...nowPatch, state: "error", label: "Error", detail: msg });
      addActivity({
        id: `error:${dedupeKey}`,
        agent_id: agentID,
        kind: "error",
        summary: msg,
        thread_id: ev.thread_id || "main",
        time: ev.time,
        status: "error",
      });
      return;
    }
    if (ev.type === "thread.spawn" || ev.type === "thread.done") {
      const summary = ev.type === "thread.spawn"
        ? `Spawned ${compactText(data.name || ev.thread_id, "worker thread")}`
        : `Finished ${ev.thread_id || "thread"}`;
      upsertState(agentID, { ...nowPatch, state: "thread", label: summary, detail: compactText(data.directive) });
      addActivity({
        id: `thread:${dedupeKey}`,
        agent_id: agentID,
        kind: "thread",
        summary,
        detail: compactText(data.directive),
        thread_id: ev.thread_id || "main",
        time: ev.time,
        status: ev.type === "thread.done" ? "success" : "info",
      });
      return;
    }
    if (ev.type !== "tool.call" && ev.type !== "tool.result") return;

    const tool = tradingToolName(data.name || data.tool);
    if (!TRADING_TOOLS.has(tool)) return;
    const args = toolArgs(data);
    const result = ev.type === "tool.result" ? toolResult(data) : undefined;
    const symbol = extractSymbol(args) || extractSymbol(result || {});
    const portfolioID = extractPortfolioID(args) || extractPortfolioID(result || {});
    if (!eventRelevantToPortfolio(portfolio, portfolioID, symbol)) return;

    const summary = summarizeTradingTool(tool, args, result);
    const status = ev.type === "tool.call" ? "running" : (data.is_error || result?.status === "rejected" ? "error" : "success");
    upsertState(agentID, {
      ...nowPatch,
      state: status === "error" ? "error" : "tool",
      label: ev.type === "tool.call" ? labelForTool(tool) : summary,
      detail: tool,
      tool,
      symbol,
      portfolio_id: portfolioID,
      trading_event_at: ev.time,
    });
    addActivity({
      id: `tool:${data.id || dedupeKey}:${ev.type}`,
      agent_id: agentID,
      kind: activityKindForTool(tool),
      summary,
      detail: ev.type === "tool.call" ? "running" : durationLabel(data.duration_ms),
      tool,
      symbol,
      portfolio_id: portfolioID,
      thread_id: ev.thread_id || "main",
      time: ev.time,
      status,
    });
  });

  const loadQuotes = useCallback(async () => {
    if (!portfolio || !portfolio.watchlist || portfolio.watchlist.length === 0) {
      setQuotes({});
      return;
    }
    const rows = await Promise.all(portfolio.watchlist.map(async (sym) => {
      try {
        const q = await api<Mark>("GET", `/quotes/${encodeURIComponent(sym)}`);
        return [sym, q] as const;
      } catch { return null; }
    }));
    const next: Record<string, Mark> = {};
    for (const row of rows) if (row) next[row[0]] = row[1];
    setQuotes(next);
  }, [portfolio?.id, portfolio?.watchlist?.join(","), api]);

  useEffect(() => {
    loadQuotes().catch(() => undefined);
    const id = window.setInterval(() => { loadQuotes().catch(() => undefined); }, 10000);
    return () => window.clearInterval(id);
  }, [loadQuotes]);

  useAppEvents("trading", projectId, (ev) => {
    if (ev.topic === "tick" || ev.topic === "watchlist.changed") loadQuotes().catch(() => undefined);
  });

  if (!portfolio) return <EmptyState title="Pick a portfolio" hint="No portfolio selected." />;

  const agentName = (id: number) => agents.find((a) => a.id === id)?.name || `Agent #${id}`;
  const portfolioBoundAgentID = portfolioAgentID(portfolio);
  const tradingMCPAgentIds = new Set(
    Object.entries(agentTradingMCPs)
      .filter(([, names]) => names.length > 0)
      .map(([id]) => Number(id)),
  );
  const portfolioEventAgentIds = new Set([
    ...Object.values(states)
      .filter((s) => !!s.trading_event_at && eventRelevantToPortfolio(portfolio, s.portfolio_id, s.symbol))
      .map((s) => s.agent_id),
    ...activities.map((a) => a.agent_id),
  ]);
  const rows = agents
    .filter((a) =>
      tradingMCPAgentIds.has(a.id) ||
      portfolioEventAgentIds.has(a.id) ||
      portfolioBoundAgentID === a.id,
    )
    .map((a) => ({ agent: a, state: states[a.id] }))
    .sort((a, b) => {
      const at = Date.parse(a.state?.last_event_at || "") || 0;
      const bt = Date.parse(b.state?.last_event_at || "") || 0;
      return bt - at || Number(b.agent.status === "running") - Number(a.agent.status === "running");
    });
  const liveBySymbol = (symbol: string) =>
    rows.filter((r) => r.state?.symbol?.toUpperCase() === symbol.toUpperCase());
  const bindPortfolioAgent = async () => {
    if (!portfolio) return;
    setBinding(true);
    try {
      const agentID = bindAgentId ? Number(bindAgentId) : null;
      await api("PATCH", `/portfolios/${portfolio.id}/agent`, undefined, { agent_id: agentID });
      setError(null);
      onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBinding(false);
    }
  };
  const selectedBindingAgent = bindAgentId ? agents.find((a) => a.id === Number(bindAgentId)) : null;
  const selectedHasTradingMCP = bindAgentId ? tradingMCPAgentIds.has(Number(bindAgentId)) : false;
  const bindingChanged = (portfolioBoundAgentID ? String(portfolioBoundAgentID) : "") !== bindAgentId;

  return (
    <>
      <Section title="Portfolio agent">
        <div className="flex flex-wrap items-center gap-2">
          <select
            value={bindAgentId}
            onChange={(e) => setBindAgentId(e.target.value)}
            className="text-xs px-2 py-1 bg-bg-input border border-border rounded text-text min-w-[220px]"
          >
            <option value="">Unbound</option>
            {agents.map((agent) => (
              <option key={agent.id} value={agent.id}>
                {agent.name}{agentTradingMCPs[agent.id]?.length ? " (trading)" : ""}
              </option>
            ))}
          </select>
          <button
            onClick={bindPortfolioAgent}
            disabled={binding || !bindingChanged}
            className="px-3 py-1 text-xs rounded bg-accent text-bg font-medium hover:opacity-90 disabled:opacity-50"
          >
            {binding ? "Saving" : bindAgentId ? "Bind" : "Unbind"}
          </button>
          {selectedBindingAgent && (
            <span className="text-xs px-2 py-0.5 rounded bg-bg-input text-text-muted">
              {selectedBindingAgent.status}
            </span>
          )}
          {selectedHasTradingMCP && (
            <span className="text-xs px-2 py-0.5 rounded bg-accent/10 text-accent border border-accent/30">
              trading MCP
            </span>
          )}
          {portfolioBoundAgentID && (
            <span className="text-xs text-text-dim">current #{portfolioBoundAgentID}</span>
          )}
        </div>
      </Section>

      <Section title={`Live watchlist · ${portfolio.name}`}>
        {(!portfolio.watchlist || portfolio.watchlist.length === 0) ? (
          <EmptyState title="No symbols tracked" hint="Watchlist is empty." />
        ) : (
          <div className="grid gap-2" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))" }}>
            {portfolio.watchlist.map((sym) => {
              const q = quotes[sym];
              const cls = q?.asset_class || inferAssetClass(sym);
              const price = q?.price ?? q?.yes_price;
              const focused = liveBySymbol(sym);
              return (
                <div key={sym} className="p-3 bg-bg-card border border-border rounded">
                  <div className="flex items-center gap-2">
                    <strong className="text-sm truncate">{sym}</strong>
                    <span className={`text-xs px-2 py-0.5 rounded-full border font-semibold ${classBadgeClass(cls)}`}>{cls}</span>
                  </div>
                  <div className="mt-2 flex items-baseline gap-2">
                    <span className="text-lg font-semibold tabular-nums">{price != null ? formatPrice(price, cls) : "—"}</span>
                    {q?.change_pct_24h != null && (
                      <span className={`text-xs tabular-nums ${pnlClass(q.change_pct_24h)}`}>{formatPct(q.change_pct_24h, 1)}</span>
                    )}
                  </div>
                  <div className="mt-2 flex gap-1 flex-wrap min-h-5">
                    {focused.length === 0 ? (
                      <span className="text-xs text-text-dim">No agent focused</span>
                    ) : focused.slice(0, 3).map((r) => (
                      <span key={r.agent.id} className="text-xs px-2 py-0.5 rounded-full bg-accent/10 text-accent border border-accent/30">
                        {r.agent.name}
                      </span>
                    ))}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </Section>

      <Section title="Agents working this portfolio">
        {agentConfigLoading ? (
          <EmptyState title="Checking agent bindings" hint="Reading agent MCP configuration." />
        ) : rows.length === 0 ? (
          <EmptyState title="No trading agents linked" hint="No matching trading MCP or portfolio telemetry." />
        ) : (
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(260px, 1fr))" }}>
            {rows.map(({ agent, state }) => (
              <AgentLiveCard
                key={agent.id}
                agent={agent}
                state={state}
                hasTradingMCP={tradingMCPAgentIds.has(agent.id)}
                portfolioBound={portfolioBoundAgentID === agent.id}
              />
            ))}
          </div>
        )}
      </Section>

      <Section title="Trading activity">
        {activities.length === 0 ? (
          <EmptyState title="No trading activity yet" hint="Waiting for trading activity." />
        ) : (
          <div className="border border-border rounded bg-bg-card overflow-hidden">
            {activities.map((a) => (
              <div key={a.id} className="px-3 py-2 border-t first:border-t-0 border-border flex items-start gap-3">
                <span className={`mt-1 w-2 h-2 rounded-full shrink-0 ${activityDotClass(a)}`} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="text-xs font-semibold text-text truncate">{agentName(a.agent_id)}</span>
                    <span className="text-xs uppercase tracking-wide text-text-dim">{a.kind}</span>
                    {a.symbol && <span className="text-xs px-1.5 py-0.5 rounded bg-bg-input text-text-muted">{a.symbol}</span>}
                    <span className="ml-auto text-xs text-text-dim shrink-0">{relTime(a.time)}</span>
                  </div>
                  <div className="text-sm text-text truncate mt-0.5">{a.summary}</div>
                  {a.tool && (
                    <div className="text-xs text-text-dim mt-0.5">
                      {a.tool}{a.thread_id ? ` · ${a.thread_id}` : ""}{a.detail ? ` · ${a.detail}` : ""}
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </Section>
    </>
  );
}

function AgentLiveCard({ agent, state, hasTradingMCP, portfolioBound }: {
  agent: Agent;
  state?: AgentLiveState;
  hasTradingMCP?: boolean;
  portfolioBound?: boolean;
}) {
  const live = state && Date.now() - (Date.parse(state.last_event_at || "") || 0) < 30_000;
  const statusText = state?.label || (agent.status === "running" ? "Ready" : agent.status);
  return (
    <div className="p-3 bg-bg-card border border-border rounded">
      <div className="flex items-center gap-2 mb-2">
        <span className={`w-2 h-2 rounded-full ${state?.state === "error" ? "bg-red" : live ? "bg-green animate-pulse" : agent.status === "running" ? "bg-amber" : "bg-text-dim"}`} />
        <strong className="text-sm truncate">{agent.name}</strong>
        <span className="ml-auto text-xs text-text-dim">{state?.last_event_at ? relTime(state.last_event_at) : agent.status}</span>
      </div>
      <div className="text-sm text-text">{statusText}</div>
      <div className="mt-1 flex gap-1 flex-wrap">
        {hasTradingMCP && <span className="text-xs px-2 py-0.5 rounded bg-accent/10 text-accent border border-accent/30">trading MCP</span>}
        {portfolioBound && <span className="text-xs px-2 py-0.5 rounded bg-bg-input text-text-muted">portfolio linked</span>}
        {state?.symbol && <span className="text-xs px-2 py-0.5 rounded bg-bg-input text-text-muted">{state.symbol}</span>}
        {state?.tool && <span className="text-xs px-2 py-0.5 rounded bg-accent/10 text-accent border border-accent/30">{state.tool}</span>}
        {state?.thread_id && <span className="text-xs px-2 py-0.5 rounded bg-bg-input text-text-dim">{state.thread_id}</span>}
      </div>
      {state?.last_summary && (
        <div className="mt-2 text-xs text-text-muted overflow-hidden" style={{ display: "-webkit-box", WebkitLineClamp: 2, WebkitBoxOrient: "vertical" }}>
          "{state.last_summary.slice(0, 180)}"
        </div>
      )}
      {!state && (
        <div className="mt-2 text-xs text-text-dim">No portfolio telemetry yet.</div>
      )}
    </div>
  );
}

function activityDotClass(a: AgentActivity): string {
  if (a.status === "error") return "bg-red";
  if (a.status === "running") return "bg-amber animate-pulse";
  if (a.kind === "order") return "bg-green";
  if (a.kind === "market") return "bg-accent";
  return "bg-text-dim";
}

// ─── Backtests tab ────────────────────────────────────────────────

function BacktestsTab({ portfolio, api, projectId, setError }: {
  portfolio: Portfolio | null;
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  projectId: string;
  setError: (e: string | null) => void;
}) {
  const [runs, setRuns] = useState<BacktestRun[]>([]);
  const [selectedRunId, setSelectedRunId] = useState<number | null>(null);
  const [events, setEvents] = useState<BacktestEvent[]>([]);
  const [liveEvents, setLiveEvents] = useState<BacktestLiveEvent[]>([]);
  const [busy, setBusy] = useState(false);
  const [name, setName] = useState("");
  const [symbolQuery, setSymbolQuery] = useState("");
  const [selectedSymbols, setSelectedSymbols] = useState<string[]>([]);
  const [universe, setUniverse] = useState<Mark[]>([]);
  const [startAt, setStartAt] = useState(defaultDate(-90));
  const [endAt, setEndAt] = useState(defaultDate(0));
  const [startingCash, setStartingCash] = useState("");
  const [feeBps, setFeeBps] = useState("1");
  const [slippageBps, setSlippageBps] = useState("5");
  const liveSeenRef = useRef<Set<string>>(new Set());

  const load = useCallback(async () => {
    if (!portfolio) return;
    try {
      const r = await api<{ backtests: BacktestRun[] }>("GET", `/portfolios/${portfolio.id}/backtests`);
      const list = r.backtests || [];
      setRuns(list);
      setSelectedRunId((cur) => cur ?? list[0]?.id ?? null);
      setError(null);
    } catch (e) { setError((e as Error).message); }
  }, [portfolio?.id, api, setError]);

  const selectedRun = useMemo(
    () => runs.find((r) => r.id === selectedRunId) || null,
    [runs, selectedRunId],
  );
  const liveTelemetryProjectId = selectedRun?.environment_id || projectId;

  const loadEvents = useCallback(async () => {
    if (!selectedRun) {
      setEvents([]);
      return;
    }
    try {
      const r = await api<{ events: BacktestEvent[] }>("GET", `/backtests/${selectedRun.id}/events`, { limit: "80" });
      setEvents(r.events || []);
    } catch (e) { setError((e as Error).message); }
  }, [selectedRun?.id, api, setError]);

  useEffect(() => { load(); }, [load]);
  useEffect(() => { loadEvents(); }, [loadEvents]);
  useEffect(() => {
    liveSeenRef.current = new Set();
    setLiveEvents([]);
  }, [selectedRun?.id]);
  useEffect(() => {
    api<{ symbols: Mark[] }>("GET", "/universe")
      .then((r) => setUniverse(r.symbols || []))
      .catch(() => undefined);
  }, [api]);
  useAppEvents("trading", projectId, (ev) => {
    if (ev.topic.startsWith("trading.backtest.")) {
      load();
      loadEvents();
    }
  });
  useTelemetryEvents(liveTelemetryProjectId, (ev) => {
    if (!selectedRun?.environment_agent_id || ev.instance_id !== selectedRun.environment_agent_id) return;
    const data = ev.data || {};
    const key = `telemetry:${ev.id || `${ev.instance_id}:${ev.thread_id}:${ev.type}:${ev.time}`}`;
    if (liveSeenRef.current.has(key)) return;
    liveSeenRef.current.add(key);

    let item: BacktestLiveEvent | null = null;
    if (ev.type === "event.received") {
      item = {
        id: key,
        kind: "event",
        summary: compactText(data.message || "Replay step received"),
        time: ev.time,
        status: "info",
      };
    } else if (ev.type === "llm.start") {
      item = { id: key, kind: "thinking", summary: "Agent thinking", time: ev.time, status: "running" };
    } else if (ev.type === "llm.done") {
      const summary = compactText(data.message || data.summary, "Agent finished thinking");
      item = { id: key, kind: "thinking", summary: summary.slice(0, 220), time: ev.time, status: "success" };
    } else if (ev.type === "tool.call" || ev.type === "tool.result") {
      const tool = tradingToolName(data.name || data.tool);
      if (!TRADING_TOOLS.has(tool)) return;
      const args = toolArgs(data);
      const result = ev.type === "tool.result" ? toolResult(data) : undefined;
      item = {
        id: key,
        kind: activityKindForTool(tool),
        summary: summarizeTradingTool(tool, args, result),
        detail: ev.type === "tool.call" ? "running" : durationLabel(data.duration_ms),
        time: ev.time,
        status: ev.type === "tool.call" ? "running" : (data.is_error ? "error" : "success"),
      };
    } else if (ev.type.includes("error")) {
      item = {
        id: key,
        kind: "error",
        summary: compactText(data.error || data.message || ev.type, ev.type),
        time: ev.time,
        status: "error",
      };
    }
    if (!item) return;
    setLiveEvents((prev) => [item!, ...prev.filter((x) => x.id !== item!.id)].slice(0, 80));
  });
  useAppEvents("trading", selectedRun?.environment_id || null, (ev) => {
    if (!selectedRun?.environment_id || ev.project_id !== selectedRun.environment_id) return;
    if (ev.topic === "tick") return;
    const key = `app:${ev.seq}:${ev.topic}`;
    if (liveSeenRef.current.has(key)) return;
    liveSeenRef.current.add(key);
    const item = summarizeBacktestAppEvent(key, ev.topic, (ev.data || {}) as Record<string, any>, ev.time);
    if (!item) return;
    setLiveEvents((prev) => [item, ...prev.filter((x) => x.id !== item.id)].slice(0, 80));
  });

  useEffect(() => {
    if (!portfolio) return;
    setName(`${portfolio.name} replay`);
    setSelectedSymbols(cleanSymbolList(portfolio.watchlist || []));
    setSymbolQuery("");
    setStartingCash(String(Math.round(portfolio.starting_cash || portfolio.cash || 100000)));
  }, [portfolio?.id]);

  if (!portfolio) return <EmptyState title="Pick a portfolio" hint="No portfolio selected." />;

  const create = async () => {
    setBusy(true);
    try {
      const body = {
        name,
        symbols: selectedSymbols,
        start_at: startAt,
        end_at: endAt,
        interval: "1d",
        starting_cash: Number(startingCash) || portfolio.starting_cash || portfolio.cash,
        fee_bps: Number(feeBps) || 0,
        slippage_bps: Number(slippageBps) || 0,
      };
      const r = await api<{ backtest: BacktestRun }>("POST", `/portfolios/${portfolio.id}/backtests`, undefined, body);
      setSelectedRunId(r.backtest.id);
      await load();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  };

  const addSymbol = (symbol: string) => {
    const next = symbol.trim().toUpperCase();
    if (!next) return;
    setSelectedSymbols((prev) => prev.some((s) => s.toUpperCase() === next)
      ? prev
      : [...prev, next]);
    setSymbolQuery("");
  };
  const removeSymbol = (symbol: string) => {
    setSelectedSymbols((prev) => prev.filter((s) => s.toUpperCase() !== symbol.toUpperCase()));
  };

  const action = async (run: BacktestRun, op: "start" | "step" | "cancel") => {
    setBusy(true);
    try {
      const r = await api<{ backtest?: BacktestRun; status?: string }>("POST", `/backtests/${run.id}/${op}`);
      if (r.backtest) {
        setRuns((prev) => prev.map((x) => x.id === r.backtest!.id ? r.backtest! : x));
      }
      await load();
      await loadEvents();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  };

  return (
    <>
      <Section title="New backtest">
        <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))" }}>
          <label className="text-xs">
            <FieldLabel>Name</FieldLabel>
            <input value={name} onChange={(e) => setName(e.target.value)} className={inputClass} />
          </label>
          <div className="text-xs md:col-span-2">
            <FieldLabel>Symbols</FieldLabel>
            <SymbolSelect
              value={symbolQuery}
              onCommit={addSymbol}
              universe={universe}
              allowedClasses={portfolio.allowed_classes}
            />
            <div className="mt-2 flex flex-wrap gap-1 min-h-6">
              {selectedSymbols.length === 0 ? (
                <span className="text-xs text-text-dim">No symbols selected</span>
              ) : selectedSymbols.map((sym) => {
                const mark = universe.find((m) => m.symbol.toUpperCase() === sym.toUpperCase());
                const cls = mark?.asset_class || inferAssetClass(sym);
                return (
                  <button
                    key={sym}
                    type="button"
                    onClick={() => removeSymbol(sym)}
                    className={`text-xs px-2 py-0.5 rounded-full border font-semibold inline-flex items-center gap-1 ${classBadgeClass(cls)}`}
                    title={`Remove ${sym}`}
                  >
                    {sym} <Icon.X />
                  </button>
                );
              })}
            </div>
          </div>
          <label className="text-xs">
            <FieldLabel>Start</FieldLabel>
            <input type="date" value={startAt} onChange={(e) => setStartAt(e.target.value)} className={inputClass} />
          </label>
          <label className="text-xs">
            <FieldLabel>End</FieldLabel>
            <input type="date" value={endAt} onChange={(e) => setEndAt(e.target.value)} className={inputClass} />
          </label>
          <label className="text-xs">
            <FieldLabel>Cash</FieldLabel>
            <input value={startingCash} onChange={(e) => setStartingCash(e.target.value)} className={inputClass} inputMode="decimal" />
          </label>
          <label className="text-xs">
            <FieldLabel>Fee bps</FieldLabel>
            <input value={feeBps} onChange={(e) => setFeeBps(e.target.value)} className={inputClass} inputMode="decimal" />
          </label>
          <label className="text-xs">
            <FieldLabel>Slippage bps</FieldLabel>
            <input value={slippageBps} onChange={(e) => setSlippageBps(e.target.value)} className={inputClass} inputMode="decimal" />
          </label>
          <div className="flex items-end">
            <button
              onClick={create}
              disabled={busy || !portfolioAgentID(portfolio) || selectedSymbols.length === 0}
              className="w-full px-3 py-1.5 text-xs rounded bg-accent text-bg font-medium hover:opacity-90 disabled:opacity-50"
            >
              Create
            </button>
          </div>
        </div>
        {!portfolioAgentID(portfolio) && (
          <div className="mt-2 text-xs text-amber">Bind a portfolio agent before creating a backtest.</div>
        )}
      </Section>

      <Section title="Runs">
        {runs.length === 0 ? (
          <EmptyState title="No backtests" hint="Create a run for this portfolio." />
        ) : (
          <div className="grid gap-3" style={{ gridTemplateColumns: "minmax(260px, 360px) minmax(0, 1fr)" }}>
            <div className="border border-border rounded bg-bg-card overflow-hidden">
              {runs.map((run) => (
                <button
                  key={run.id}
                  onClick={() => setSelectedRunId(run.id)}
                  className={`w-full text-left px-3 py-2 border-t first:border-t-0 border-border hover:bg-bg-hover ${selectedRunId === run.id ? "bg-bg-input" : ""}`}
                >
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-sm truncate">{run.name}</span>
                    <BacktestStatus status={run.status} />
                  </div>
                  <div className="mt-1 text-xs text-text-dim">
                    {run.start_at} to {run.end_at} · {run.current_step}/{run.total_steps}
                  </div>
                </button>
              ))}
            </div>
            <div>
              {selectedRun ? (
                <BacktestRunDetail run={selectedRun} events={events} liveEvents={liveEvents} busy={busy} onAction={action} />
              ) : (
                <EmptyState title="Select a run" />
              )}
            </div>
          </div>
        )}
      </Section>
    </>
  );
}

function BacktestRunDetail({ run, events, liveEvents, busy, onAction }: {
  run: BacktestRun;
  events: BacktestEvent[];
  liveEvents: BacktestLiveEvent[];
  busy: boolean;
  onAction: (run: BacktestRun, op: "start" | "step" | "cancel") => void;
}) {
  const pct = run.total_steps > 0 ? Math.min(100, Math.round((run.current_step / run.total_steps) * 100)) : 0;
  const prices = run.summary?.prices || [];
  return (
    <div className="space-y-3">
      <div className="p-3 border border-border rounded bg-bg-card">
        <div className="flex flex-wrap items-center gap-2">
          <strong className="text-sm">{run.name}</strong>
          <BacktestStatus status={run.status} />
          <span className="text-xs text-text-dim">agent #{run.source_agent_id}</span>
          {run.environment_id && <span className="text-xs text-text-dim">env {run.environment_id}</span>}
          <span className="flex-1" />
          {run.status === "queued" || run.status === "failed" ? (
            <button disabled={busy} onClick={() => onAction(run, "start")} className="px-2 py-1 text-xs rounded bg-accent text-bg font-medium disabled:opacity-50">Start</button>
          ) : null}
          {run.status === "running" && (
            <>
              <button disabled={busy} onClick={() => onAction(run, "step")} className="px-2 py-1 text-xs rounded bg-accent text-bg font-medium disabled:opacity-50">Step</button>
              <button disabled={busy} onClick={() => onAction(run, "cancel")} className="px-2 py-1 text-xs rounded border border-border text-text-muted hover:bg-bg-hover disabled:opacity-50">Cancel</button>
            </>
          )}
        </div>
        <div className="mt-3 h-2 rounded bg-bg-input overflow-hidden">
          <div className="h-full bg-accent" style={{ width: `${pct}%` }} />
        </div>
        <div className="mt-2 grid grid-cols-2 md:grid-cols-4 gap-2 text-xs">
          <Metric label="Step" value={`${run.current_step}/${run.total_steps}`} />
          <Metric label="Cash" value={formatUSD(run.starting_cash)} />
          <Metric label="Fee" value={`${run.fee_bps} bps`} />
          <Metric label="Slippage" value={`${run.slippage_bps} bps`} />
        </div>
        {prices.length > 0 && (
          <div className="mt-3 flex flex-wrap gap-1">
            {prices.map((p) => (
              <span key={p.symbol} className="text-xs px-2 py-0.5 rounded bg-bg-input text-text-muted">
                {p.symbol} {formatPrice(p.price, p.asset_class || inferAssetClass(p.symbol))}
              </span>
            ))}
          </div>
        )}
        {run.error && <div className="mt-2 text-xs text-red">{run.error}</div>}
      </div>
      <div className="border border-border rounded bg-bg-card overflow-hidden">
        <div className="px-3 py-2 border-b border-border text-xs font-semibold uppercase tracking-wide text-text-dim">
          Live agent activity
        </div>
        {liveEvents.length === 0 ? (
          <EmptyState title="No live activity yet" />
        ) : liveEvents.map((ev) => (
          <div key={ev.id} className="px-3 py-2 border-t first:border-t-0 border-border flex gap-2">
            <span className={`mt-1 w-2 h-2 rounded-full shrink-0 ${liveEventDotClass(ev)}`} />
            <div className="min-w-0 flex-1">
              <div className="flex gap-2 text-xs">
                <span className="font-semibold uppercase tracking-wide text-accent">{ev.kind}</span>
                <span className="text-text-dim">{relTime(ev.time)}</span>
                {ev.detail && <span className="text-text-dim truncate">{ev.detail}</span>}
              </div>
              <div className="text-sm mt-0.5 break-words">{ev.summary}</div>
            </div>
          </div>
        ))}
      </div>
      <div className="border border-border rounded bg-bg-card overflow-hidden">
        <div className="px-3 py-2 border-b border-border text-xs font-semibold uppercase tracking-wide text-text-dim">
          Backtest events
        </div>
        {events.length === 0 ? (
          <EmptyState title="No events" />
        ) : events.map((ev) => (
          <div key={ev.id} className="px-3 py-2 border-t first:border-t-0 border-border">
            <div className="flex gap-2 text-xs">
              <span className="font-semibold uppercase tracking-wide text-accent">{ev.kind}</span>
              <span className="text-text-dim">{relTime(ev.created_at)}</span>
            </div>
            <div className="text-sm mt-0.5">{ev.message}</div>
          </div>
        ))}
      </div>
    </div>
  );
}

function summarizeBacktestAppEvent(id: string, topic: string, data: Record<string, any>, time: string): BacktestLiveEvent | null {
  const symbol = compactText(data.symbol || data.order?.symbol || data.position?.symbol);
  if (topic === "trading.backtest.market_step") {
    return { id, kind: "market", summary: "Loaded replay market prices", time, status: "info" };
  }
  if (topic === "order.placed") {
    const side = compactText(data.side || data.order?.side).toUpperCase();
    const qty = compactText(data.qty || data.order?.qty);
    return { id, kind: "order", summary: `Order placed ${[side, qty, symbol].filter(Boolean).join(" ")}`, time, status: "running" };
  }
  if (topic === "order.filled") {
    const side = compactText(data.side || data.order?.side).toUpperCase();
    const qty = compactText(data.qty || data.filled_qty || data.order?.qty);
    const price = Number(data.avg_fill_price || data.price || data.order?.avg_fill_price);
    const px = Number.isFinite(price) ? ` @ ${formatPrice(price, inferAssetClass(symbol))}` : "";
    return { id, kind: "order", summary: `Order filled ${[side, qty, symbol].filter(Boolean).join(" ")}${px}`, time, status: "success" };
  }
  if (topic === "journal.appended") {
    return { id, kind: "journal", summary: compactText(data.body || data.kind || "Journal entry added").slice(0, 220), time, status: "success" };
  }
  if (topic === "position.changed") {
    return { id, kind: "risk", summary: `Position updated${symbol ? ` · ${symbol}` : ""}`, time, status: "success" };
  }
  return { id, kind: "event", summary: topic, time, status: "info" };
}

function liveEventDotClass(ev: BacktestLiveEvent): string {
  if (ev.status === "error") return "bg-red";
  if (ev.status === "running") return "bg-amber animate-pulse";
  if (ev.kind === "order") return "bg-green";
  if (ev.kind === "market") return "bg-accent";
  return "bg-text-dim";
}

function BacktestStatus({ status }: { status: BacktestRun["status"] }) {
  const cls = status === "running" ? "bg-amber/10 text-amber" :
    status === "completed" ? "bg-green/10 text-green" :
    status === "failed" || status === "cancelled" ? "bg-red/10 text-red" :
    "bg-bg-input text-text-muted";
  return <span className={`text-xs px-2 py-0.5 rounded uppercase font-semibold ${cls}`}>{status}</span>;
}

function defaultDate(offsetDays: number) {
  const d = new Date();
  d.setDate(d.getDate() + offsetDays);
  return d.toISOString().slice(0, 10);
}

function cleanSymbolList(symbols: string[]) {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const symbol of symbols) {
    const next = symbol.trim().toUpperCase();
    if (!next || seen.has(next)) continue;
    seen.add(next);
    out.push(next);
  }
  return out;
}

// ─── Brokers tab ──────────────────────────────────────────────────

function BrokersTab({ api, setError }: {
  api: <T>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  setError: (e: string | null) => void;
}) {
  const [brokers, setBrokers] = useState<BrokerInfo[]>([]);
  const [connectSlug, setConnectSlug] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await api<{ brokers: BrokerInfo[] }>("GET", "/brokers");
      setBrokers(r.brokers || []);
    } catch (e) { setError((e as Error).message); }
  }, [api, setError]);

  useEffect(() => { load(); }, [load]);

  // Disconnect a specific connection by id, then refresh the brokers
  // list. Hits the platform endpoint directly — same auth cookie as
  // the dashboard, no panel/server proxy needed.
  const disconnect = async (connID: number, slug: string) => {
    if (!window.confirm(`Disconnect ${slug} connection #${connID}? Any live portfolio bound to it will reject orders until rebound.`)) return;
    try {
      const res = await fetch(`/api/connections/${connID}`, {
        method: "DELETE", credentials: "same-origin",
      });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      load();
    } catch (e) { setError((e as Error).message); }
  };

  return (
    <Section title="Brokers">
      <div className="mb-3 p-3 text-xs bg-bg-input border border-border rounded text-text-muted">
        Connect a broker to enable live trading. Each portfolio binds to
        one broker at creation (Portfolios tab → New → Live). Credentials
        are encrypted at rest and stay on this server.
      </div>
      <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))" }}>
        {brokers.map((b) => (
          <div key={b.slug} className={`p-3 border border-border rounded ${b.bound ? "bg-bg-card" : "bg-bg-input"}`}>
            <div className="flex items-center justify-between mb-2">
              <strong className="text-sm">{b.slug}</strong>
              <span className={`text-xs px-2 py-0.5 rounded-full font-semibold uppercase tracking-wide ${
                b.bound ? "bg-green/10 text-green" : "bg-bg-input text-text-muted"
              }`}>{b.bound ? "bound" : "unbound"}</span>
            </div>
            <div className="mb-2 text-xs">
              <FieldLabel>Classes</FieldLabel>
              <div className="flex gap-1 flex-wrap mt-0.5">
                {b.asset_classes.map((c) => (
                  <span key={c} className={`text-xs px-2 py-0.5 rounded-full border font-semibold ${classBadgeClass(c)}`}>{c}</span>
                ))}
              </div>
            </div>
            <div className="grid grid-cols-2 gap-2 text-xs mt-2">
              <div><FieldLabel>Quote</FieldLabel><div className="text-text">{b.quote}</div></div>
              <div><FieldLabel>Fractional</FieldLabel><div className="text-text">{b.fractional ? "yes" : "no"}</div></div>
            </div>
            <div className="mt-2 text-xs">
              <FieldLabel>TIFs</FieldLabel>
              <div className="text-text">{b.tifs.join(", ")}</div>
            </div>
            {b.connections.length > 0 && (
              <div className="mt-2 text-xs">
                <FieldLabel>Connections</FieldLabel>
                {b.connections.map((c) => (
                  <div key={c.id} className="flex items-center justify-between px-2 py-1 mt-1 text-xs bg-bg-input border border-border rounded">
                    <span className="truncate">#{c.id} · {c.name || "(unnamed)"} · {c.status}</span>
                    <button
                      onClick={() => disconnect(c.id, b.slug)}
                      className="p-0.5 rounded text-text-dim hover:text-red"
                      title={`Disconnect ${c.name || "#" + c.id}`}
                    ><Icon.Trash /></button>
                  </div>
                ))}
              </div>
            )}
            <div className="mt-3 flex gap-2">
              <button
                onClick={() => setConnectSlug(b.slug)}
                className="flex-1 px-2 py-1 text-xs rounded bg-accent text-bg font-medium inline-flex items-center justify-center gap-1 hover:opacity-90"
              ><Icon.Plus /> {b.bound ? "Add another" : "Connect"}</button>
            </div>
          </div>
        ))}
      </div>

      {connectSlug && (
        <BrokerConnectModal
          slug={connectSlug}
          onClose={() => setConnectSlug(null)}
          onConnected={() => { setConnectSlug(null); load(); }}
          setError={setError}
        />
      )}
    </Section>
  );
}

// CatalogApp — minimal subset of the platform's catalog response we
// actually need to render the form. apteva-server's
// /api/integrations/catalog/<slug> returns much more (tool defs,
// webhooks, etc.); we ignore the rest.
interface CatalogApp {
  slug: string;
  name: string;
  description?: string;
  auth: {
    types: string[];
    credential_fields?: Array<{
      name: string;
      label?: string;
      description?: string;
      required?: boolean;
      type?: string; // "password" | "text" | etc
      default?: string;
    }>;
  };
}

// BrokerConnectModal — inline credential form for any broker integration
// in our compatible_slugs set. Mirrors the dashboard's "Connect" flow:
// fetch the catalog entry to know what fields to render, then POST
// /api/connections with {source: "local", app_slug, auth_type,
// credentials, ...}. Reuses the user's session cookie (same origin),
// so no API token needed.
//
// We don't handle OAuth here — none of the current brokers
// (binance-trading, alpaca-trading, polymarket-clob) use OAuth. If a
// future broker does, the social app's popup flow is the reference.
function BrokerConnectModal({ slug, onClose, onConnected, setError }: {
  slug: string;
  onClose: () => void;
  onConnected: () => void;
  setError: (e: string | null) => void;
}) {
  const [app, setApp] = useState<CatalogApp | null>(null);
  const [loading, setLoading] = useState(true);
  const [name, setName] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  const [localErr, setLocalErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    fetch(`/api/integrations/catalog/${encodeURIComponent(slug)}`, { credentials: "same-origin" })
      .then((r) => r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`)))
      .then((data: CatalogApp) => {
        if (cancelled) return;
        setApp(data);
        // Seed the connection name with the slug so the operator
        // doesn't have to type anything for the common case.
        setName(data.name || slug);
        // Pre-fill any fields that declare a default in the catalog
        // (e.g. alpaca-trading's `host` defaults to paper-api).
        const seed: Record<string, string> = {};
        for (const f of data.auth?.credential_fields || []) {
          if (f.default) seed[f.name] = f.default;
        }
        setValues(seed);
        setLoading(false);
      })
      .catch((e) => {
        if (cancelled) return;
        setLocalErr((e as Error).message);
        setLoading(false);
      });
    return () => { cancelled = true; };
  }, [slug]);

  const submit = async () => {
    if (!app) return;
    setSubmitting(true);
    setLocalErr(null);
    try {
      const authType = (app.auth.types || ["api_key"])[0];
      const res = await fetch("/api/connections", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          source: "local",
          app_slug: slug,
          name: name.trim() || slug,
          auth_type: authType,
          credentials: values,
          // The trading panel binds via portfolio.broker_slug — we don't
          // want apteva-server auto-creating an mcp_servers row here.
          auto_mcp: false,
          created_via: "app_install",
        }),
      });
      if (!res.ok) {
        const body = await res.text().catch(() => "");
        throw new Error(`${res.status}: ${body}`);
      }
      onConnected();
    } catch (e) {
      setLocalErr((e as Error).message);
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  const fields = app?.auth?.credential_fields || [];
  const requiredMissing = fields.some((f) => f.required !== false && !((values[f.name] || "").trim()));

  // Heuristic: any field whose name or label suggests a secret gets
  // type=password so it's masked in the input. Avoids surprising the
  // operator if the catalog forgets to declare `type: password`.
  const isSensitive = (name: string, declared?: string) => {
    if (declared === "password") return true;
    const n = name.toLowerCase();
    return n.includes("secret") || n.includes("private_key") || n.includes("passphrase") || n.includes("password");
  };

  return (
    <div
      className="fixed inset-0 bg-black/60 grid place-items-center z-50"
      onClick={onClose}
    >
      <div
        className="bg-bg-card border border-border rounded p-4 w-full max-w-md max-h-[90vh] overflow-auto"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-3">
          <div className="text-text font-medium">Connect {app?.name || slug}</div>
          <button onClick={onClose} className="text-text-muted hover:text-text"><Icon.X /></button>
        </div>

        {loading ? (
          <div className="py-6 text-center text-text-dim text-sm">Loading credential schema…</div>
        ) : !app ? (
          <div className="py-6 text-center text-red text-sm">{localErr || "Couldn't load catalog"}</div>
        ) : (
          <>
            {app.description && (
              <div className="text-xs text-text-muted mb-3">{app.description}</div>
            )}
            <div className="mb-3">
              <FieldLabel>Connection name</FieldLabel>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                className={inputClass}
                placeholder={slug}
              />
            </div>
            {fields.map((f) => (
              <div key={f.name} className="mb-3">
                <FieldLabel>{f.label || f.name}{f.required !== false ? "" : " (optional)"}</FieldLabel>
                <input
                  value={values[f.name] || ""}
                  onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}
                  type={isSensitive(f.name, f.type) ? "password" : "text"}
                  className={inputClass}
                  autoComplete="off"
                  spellCheck={false}
                />
                {f.description && (
                  <div className="text-xs text-text-dim mt-1">{f.description}</div>
                )}
              </div>
            ))}
            {localErr && (
              <div className="mb-3 p-2 text-xs bg-error/10 border border-error/30 rounded text-error">
                {localErr}
              </div>
            )}
            <div className="flex gap-2 justify-end">
              <button onClick={onClose} className="px-3 py-1 text-sm rounded border border-border text-text hover:bg-bg-hover">Cancel</button>
              <button
                onClick={submit}
                disabled={submitting || requiredMissing}
                className="px-3 py-1 text-sm rounded bg-accent text-bg font-medium hover:opacity-90 disabled:opacity-50"
              >{submitting ? "Connecting…" : "Connect"}</button>
            </div>
          </>
        )}
      </div>
    </div>
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
        <select value={kind} onChange={(e) => setKind(e.target.value)} className={`${inputClass} w-auto`}>
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
        <div className="flex flex-col gap-2">
          {entries.map((e) => (
            <div key={e.id} className="p-3 border border-border rounded bg-bg-card">
              <div className="flex justify-between mb-1 text-xs">
                <span className={`uppercase tracking-wide font-semibold ${kindClass(e.kind)}`}>{e.kind}</span>
                <span className="text-text-dim">{relTime(e.created_at)}</span>
              </div>
              <div className="text-sm whitespace-pre-wrap">{e.body}</div>
            </div>
          ))}
        </div>
      )}
    </Section>
  );
}

function kindClass(kind: string): string {
  switch (kind) {
    case "fill": return "text-green";
    case "alert": return "text-red";
    case "rationale": return "text-accent";
    case "note": return "text-text-dim";
    default: return "text-text-dim";
  }
}
