// TradingPanel — native React panel for the trading app. Styled with
// the dashboard's Tailwind theme tokens (bg-bg, text-text, border-border,
// text-accent, …) so it matches CRM / Messaging / Storage / Finance.
//
// Talks to /api/apps/trading/* through the platform proxy.
//
// Tabs: Portfolios | Trade | Positions | Brokers | Journal.

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
  t: number;
  o?: number; h?: number; l?: number; c?: number; v?: number;
  yes?: number;
}
interface HistoryResp { symbol: string; range: string; bars: Bar[] }
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

// ─── Icons (SVG, theme-aware via currentColor) ─────────────────────

const Icon = {
  Plus:  () => <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round"><path d="M12 5v14M5 12h14"/></svg>,
  X:     () => <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>,
  Pause: () => <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><rect x="6" y="5" width="4" height="14" rx="1"/><rect x="14" y="5" width="4" height="14" rx="1"/></svg>,
  Play:  () => <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z"/></svg>,
  Refresh: () => <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/></svg>,
  ExternalLink: () => <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5"/></svg>,
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

  // SVG viewBox: 1000 wide, 300 tall. Chart pane reserves 56px on the
  // right for Y-axis labels + 24px at the bottom for X-axis labels.
  const W = 1000, H = 300;
  const padL = 6, padR = 60, padT = 8, padB = 28;
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

            {/* X-axis time labels at the bottom (DOM, so font stays
                pixel-accurate). */}
            <div className="absolute left-0 bottom-1 right-14 flex justify-between text-xs text-text-dim tabular-nums px-1">
              {xTicks.map((i, k) => (
                <span key={k}>{formatTimeTick(times[i], range)}</span>
              ))}
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
    if (["portfolio.created", "portfolio.status.changed", "order.filled", "position.changed"].includes(ev.topic)) {
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
        {(["portfolios","trade","positions","brokers","journal"] as TabId[]).map((id) => {
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
          className="px-3 py-1 text-xs rounded bg-accent text-bg font-medium inline-flex items-center gap-1 hover:opacity-90 no-underline"
        >Bind broker <Icon.ExternalLink /></a>
      }
    >
      <div className="mb-3 p-3 text-xs bg-bg-input border border-border rounded text-text-muted">
        Each portfolio binds to one broker at creation. Bind a connection
        via the dashboard's integrations page, then come back and create a
        live portfolio under the Portfolios tab.
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
                  <div key={c.id} className="px-2 py-1 mt-1 text-xs bg-bg-input border border-border rounded">
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
