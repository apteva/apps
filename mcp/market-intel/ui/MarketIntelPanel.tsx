// MarketIntelPanel — v0.1.3 console for the market-intel gateway.
// Tabs:
//   Markets — browse the normalized market catalog
//   Indicators — technical indicator snapshots for agents and operators
//   Sources — which data integrations are bound (what the gateway can answer)
//   Query   — a test harness: run enrich / probability / context and see
//             the normalized result
//
// Styled with the dashboard's Tailwind theme tokens (bg-bg, text-text-muted,
// border-border, …) so it matches the trading / crm / storage panels.

import { useCallback, useEffect, useRef, useState } from "react";

// ─── Inlined SDK app-event hook (forward-compat; v0.2 emits signals) ──
interface AppEventEnvelope<T = unknown> {
  topic: string; app: string; project_id: string; install_id: number;
  seq: number; time: string; data: T;
}
function useAppEvents<T = unknown>(app: string, projectId: string | null | undefined, onEvent: (e: AppEventEnvelope<T>) => void) {
  const ref = useRef(onEvent); ref.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const bridge = (window as unknown as { __aptevaAppEvents?: { subscribe(a: string, p: string, fn: (e: AppEventEnvelope<T>) => void): () => void } }).__aptevaAppEvents;
    if (bridge) return bridge.subscribe(app, projectId, (e) => ref.current(e));
    return;
  }, [app, projectId]);
}

interface NativePanelProps { appName: string; installId: number; projectId: string; instanceId?: number }

const API = "/api/apps/market-intel";

// Static slug → domain map for grouping the Sources tab. Mirrors the
// roles in apteva.yaml.
const SOURCE_DOMAINS: Record<string, string> = {
  polymarket: "Prediction markets", "polymarket-data": "Prediction markets", "polymarket-clob": "Prediction markets",
  kalshi: "Prediction markets", "manifold-markets": "Prediction markets",
  "the-odds-api": "Sports", "the-sports-db": "Sports", "api-sports": "Sports", "tennis-abstract": "Sports",
  fred: "Macro", bls: "Macro", eia: "Macro", finnhub: "Macro",
  tavily: "News", perplexity: "News", exa: "News", newsapi: "News", gnews: "News", gdelt: "News", wikipedia: "News",
  coingecko: "Crypto", etherscan: "Crypto", polygonscan: "Crypto", "whale-alert": "Crypto", defillama: "Crypto",
  "anthropic-api": "LLM", "openai-api": "LLM",
};

const Icon = {
  Refresh: () => <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/></svg>,
  Dot: ({ on }: { on: boolean }) => <svg width="8" height="8" viewBox="0 0 8 8"><circle cx="4" cy="4" r="3" className={on ? "text-green" : "text-text-dim"} fill="currentColor" /></svg>,
};

type TabId = "markets" | "indicators" | "sources" | "query";

export default function MarketIntelPanel({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<TabId>("markets");
  const [error, setError] = useState<string | null>(null);

  const withParams = useCallback((extra: Record<string, string> = {}) =>
    new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra }).toString(),
    [projectId, installId]);

  const api = useCallback(async <T,>(path: string, params?: Record<string, string>): Promise<T> => {
    const res = await fetch(`${API}${path}?${withParams(params || {})}`, { credentials: "same-origin" });
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return res.json() as Promise<T>;
  }, [withParams]);

  useAppEvents("market-intel", projectId, () => { /* v0.2: refresh on signal events */ });

  return (
    <div className="h-full flex flex-col bg-bg text-text text-sm">
      <header className="px-4 py-2 flex items-center gap-3 border-b border-border">
        <h1 className="text-sm font-semibold m-0">Market Intelligence</h1>
        <span className="text-xs text-text-dim">gateway · v0.1.3</span>
        <span className="flex-1" />
      </header>

      <nav className="flex border-b border-border px-3 text-xs">
        {(["markets", "indicators", "sources", "query"] as TabId[]).map((id) => (
          <button key={id} onClick={() => setTab(id)}
            className={`px-3 py-2 capitalize ${tab === id ? "text-text font-semibold border-b-2 border-accent -mb-px" : "text-text-muted hover:text-text border-b-2 border-transparent -mb-px"}`}>
            {id}
          </button>
        ))}
      </nav>

      {error && (
        <div className="px-4 py-2 bg-error/10 text-error text-sm flex items-center gap-2 border-b border-error/30">
          <span className="flex-1">{error}</span>
          <button onClick={() => setError(null)} className="hover:text-text">×</button>
        </div>
      )}

      <div className="flex-1 overflow-auto p-4">
        {tab === "markets" && <MarketsTab api={api} setError={setError} />}
        {tab === "indicators" && <IndicatorsTab api={api} setError={setError} />}
        {tab === "sources" && <SourcesTab api={api} setError={setError} />}
        {tab === "query" && <QueryTab api={api} setError={setError} />}
      </div>
    </div>
  );
}

// ─── Markets tab (auto-loads on mount) ─────────────────────────────

interface MarketRow {
  venue: string; id: string; question: string;
  yes_price?: number; volume?: number; close_time?: string; url?: string;
}

function venueClass(v: string): string {
  switch (v) {
    case "polymarket": return "bg-accent/15 text-accent";
    case "kalshi": return "bg-blue-500/20 text-blue-400";
    case "manifold": return "bg-amber/10 text-amber";
    default: return "bg-bg-input text-text-muted";
  }
}
function fmtVol(n?: number): string {
  if (n == null) return "—";
  if (n >= 1_000_000) return `$${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `$${(n / 1_000).toFixed(0)}k`;
  return `$${n.toFixed(0)}`;
}

function MarketsTab({ api, setError }: {
  api: <T>(p: string, q?: Record<string, string>) => Promise<T>;
  setError: (e: string | null) => void;
}) {
  const [rows, setRows] = useState<MarketRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);

  const load = useCallback(async () => {
    setBusy(true);
    try {
      const r = await api<{ markets: MarketRow[] }>("/markets", { limit: "40" });
      setRows(r.markets || []);
      setLoaded(true);
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }, [api, setError]);

  // Auto-load on mount — the whole point: live data with zero clicks.
  useEffect(() => { load(); }, [load]);

  return (
    <>
      <div className="flex items-center justify-between mb-3">
        <div className="text-xs text-text-muted">
          Live markets across the public venues (Polymarket · Kalshi · Manifold), volume-ranked. No setup — all public.
        </div>
        <button onClick={load} disabled={busy} className="p-1.5 rounded border border-border text-text-muted hover:bg-bg-hover disabled:opacity-50"><Icon.Refresh /></button>
      </div>

      {busy && rows.length === 0 ? (
        <div className="p-8 text-center text-text-dim text-sm">Loading live markets…</div>
      ) : loaded && rows.length === 0 ? (
        <div className="p-8 text-center text-text-muted text-sm">
          <div className="font-medium text-text mb-1">No markets returned</div>
          <div className="opacity-80 text-xs">The public venues may be unreachable from this host. Check the Sources tab.</div>
        </div>
      ) : (
        <div className="border border-border rounded overflow-hidden bg-bg-card">
          <table className="w-full text-xs border-collapse">
            <thead className="bg-bg-input text-text-dim">
              <tr>
                {["Venue", "Market", "YES", "Volume", ""].map((h) => (
                  <th key={h} className="px-3 py-2 text-left font-semibold uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((m) => (
                <tr key={m.venue + ":" + m.id} className="border-t border-border">
                  <td className="px-3 py-2"><span className={`text-xs px-2 py-0.5 rounded-full font-semibold ${venueClass(m.venue)}`}>{m.venue}</span></td>
                  <td className="px-3 py-2 max-w-md"><span className="truncate block">{m.question}</span></td>
                  <td className="px-3 py-2 tabular-nums font-semibold">{m.yes_price != null ? `${(m.yes_price * 100).toFixed(0)}¢` : "—"}</td>
                  <td className="px-3 py-2 tabular-nums text-text-muted">{fmtVol(m.volume)}</td>
                  <td className="px-3 py-2 text-right">
                    {m.url && <a href={m.url} target="_blank" rel="noopener" className="text-accent hover:underline">open</a>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

// ─── Indicators tab ────────────────────────────────────────────────

interface IndicatorResult {
  symbol: string;
  interval: string;
  range: string;
  as_of?: string;
  source: string;
  bar_count: number;
  values: Record<string, unknown>;
  summary?: { bias?: string; confidence?: number; score?: number; reasons?: string[]; warnings?: string[] };
  warnings?: string[];
}

const INTERVALS = ["5m", "15m", "1h", "4h", "1d", "1w"];
const RANGES = ["1D", "5D", "1M", "3M", "6M", "1Y", "ALL"];
const PRESETS = ["trend", "momentum", "mean_reversion", "volatility", "breakout", "risk"];

function fmtNum(v: unknown): string {
  if (typeof v !== "number" || !Number.isFinite(v)) return "—";
  const abs = Math.abs(v);
  if (abs >= 1000) return v.toLocaleString(undefined, { maximumFractionDigits: 1 });
  if (abs >= 10) return v.toFixed(2);
  return v.toFixed(4).replace(/\.?0+$/, "");
}

function indicatorValueText(v: unknown): string {
  if (typeof v === "number") return fmtNum(v);
  if (v && typeof v === "object") {
    return Object.entries(v as Record<string, unknown>)
      .map(([k, val]) => `${k} ${fmtNum(val)}`)
      .join(" · ");
  }
  return "—";
}

function biasClass(bias?: string): string {
  if (bias === "bullish") return "text-green";
  if (bias === "bearish") return "text-red";
  return "text-text-muted";
}

function IndicatorsTab({ api, setError }: {
  api: <T>(p: string, q?: Record<string, string>) => Promise<T>;
  setError: (e: string | null) => void;
}) {
  const [symbol, setSymbol] = useState("BTC-USD");
  const [interval, setIntervalValue] = useState("1h");
  const [range, setRange] = useState("3M");
  const [preset, setPreset] = useState("trend");
  const [result, setResult] = useState<IndicatorResult | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setBusy(true);
    try {
      const r = await api<IndicatorResult>("/query/indicators", { symbol, interval, range, preset });
      setResult(r);
      setError(null);
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }, [api, symbol, interval, range, preset, setError]);

  useEffect(() => { load(); }, []);

  const inputCls = "w-full text-sm px-2 py-1.5 bg-bg-input border border-border rounded text-text";
  const labelCls = "block text-xs uppercase tracking-wide font-medium text-text-dim mb-1";
  const summary = result?.summary;
  const entries = result ? Object.entries(result.values || {}) : [];

  return (
    <>
      <div className="p-3 border border-border rounded bg-bg-card mb-4">
        <div className="grid gap-3 mb-3" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))" }}>
          <div><label className={labelCls}>Symbol</label><input className={inputCls} value={symbol} onChange={(e) => setSymbol(e.target.value.toUpperCase())} /></div>
          <div><label className={labelCls}>Interval</label><select className={inputCls} value={interval} onChange={(e) => setIntervalValue(e.target.value)}>{INTERVALS.map((x) => <option key={x} value={x}>{x}</option>)}</select></div>
          <div><label className={labelCls}>Range</label><select className={inputCls} value={range} onChange={(e) => setRange(e.target.value)}>{RANGES.map((x) => <option key={x} value={x}>{x}</option>)}</select></div>
          <div><label className={labelCls}>Preset</label><select className={inputCls} value={preset} onChange={(e) => setPreset(e.target.value)}>{PRESETS.map((x) => <option key={x} value={x}>{x.replace("_", " ")}</option>)}</select></div>
          <div className="flex items-end"><button disabled={busy || !symbol.trim()} onClick={load} className="w-full px-3 py-1.5 text-sm rounded bg-accent text-bg font-medium hover:opacity-90 disabled:opacity-50">{busy ? "Running…" : "Run"}</button></div>
        </div>
        {result && (
          <div className="flex flex-wrap gap-3 text-xs text-text-muted">
            <span>{result.source}</span>
            <span>{result.bar_count} bars</span>
            {result.as_of && <span>{new Date(result.as_of).toLocaleString()}</span>}
          </div>
        )}
      </div>

      {result && (
        <div className="space-y-4">
          <div className="border border-border rounded bg-bg-card p-3">
            <div className="flex items-center gap-3">
              <div className="text-xs uppercase tracking-wide text-text-dim">Bias</div>
              <div className={`text-lg font-semibold capitalize ${biasClass(summary?.bias)}`}>{summary?.bias || "neutral"}</div>
              <div className="text-xs text-text-muted">confidence {fmtNum(summary?.confidence)}</div>
              <div className="flex-1" />
              <div className="text-xs text-text-muted">{result.symbol} · {result.interval} · {result.range}</div>
            </div>
            {(summary?.reasons || []).length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1">
                {(summary?.reasons || []).map((reason) => <span key={reason} className="text-xs px-2 py-0.5 rounded bg-bg-input text-text-muted">{reason}</span>)}
              </div>
            )}
            {((summary?.warnings || []).length > 0 || (result.warnings || []).length > 0) && (
              <div className="mt-2 text-xs text-amber">{[...(summary?.warnings || []), ...(result.warnings || [])].join(" · ")}</div>
            )}
          </div>

          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(210px, 1fr))" }}>
            {entries.map(([key, value]) => (
              <div key={key} className="border border-border rounded bg-bg-card p-3">
                <div className="text-xs uppercase tracking-wide text-text-dim mb-1">{key}</div>
                <div className="text-sm font-semibold tabular-nums">{indicatorValueText(value)}</div>
              </div>
            ))}
          </div>
        </div>
      )}
      {!result && !busy && (
        <div className="p-8 text-center text-text-muted text-sm">
          <div className="font-medium text-text mb-1">No indicator result</div>
        </div>
      )}
    </>
  );
}

// ─── Sources tab ───────────────────────────────────────────────────

interface SourceRow { slug: string; bound: boolean; public: boolean; available: boolean }

function SourcesTab({ api, setError }: {
  api: <T>(p: string, q?: Record<string, string>) => Promise<T>;
  setError: (e: string | null) => void;
}) {
  const [rows, setRows] = useState<SourceRow[]>([]);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setBusy(true);
    try {
      const r = await api<{ sources: SourceRow[] }>("/sources");
      setRows(r.sources || []);
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }, [api, setError]);

  useEffect(() => { load(); }, [load]);

  const groups: Record<string, SourceRow[]> = {};
  for (const r of rows) {
    const d = SOURCE_DOMAINS[r.slug] || "Other";
    (groups[d] ||= []).push(r);
  }
  const availCount = rows.filter((r) => r.available).length;

  // Label: public sources work with no setup; keyed need binding.
  const label = (r: SourceRow): { text: string; cls: string } => {
    if (r.public) return { text: "public", cls: "text-green" };
    if (r.bound) return { text: "bound", cls: "text-green" };
    return { text: "needs key", cls: "text-text-dim" };
  };

  return (
    <>
      <div className="flex items-center justify-between mb-3">
        <div className="text-xs text-text-muted">
          {availCount} of {rows.length} sources available. Public sources work with zero setup; keyed sources need a connection bound on the Integrations page.
        </div>
        <button onClick={load} disabled={busy} className="p-1.5 rounded border border-border text-text-muted hover:bg-bg-hover disabled:opacity-50"><Icon.Refresh /></button>
      </div>
      {Object.keys(groups).sort().map((domain) => (
        <div key={domain} className="mb-4">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-text-dim mb-2">{domain}</h2>
          <div className="grid gap-2" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))" }}>
            {groups[domain].map((r) => {
              const l = label(r);
              return (
                <div key={r.slug} className={`flex items-center gap-2 px-3 py-2 rounded border border-border ${r.available ? "bg-bg-card" : "bg-bg-input"}`}>
                  <Icon.Dot on={r.available} />
                  <span className="text-xs font-medium flex-1">{r.slug}</span>
                  <span className={`text-xs ${l.cls}`}>{l.text}</span>
                </div>
              );
            })}
          </div>
        </div>
      ))}
      <div className="mt-2 p-3 text-xs bg-bg-input border border-border rounded text-text-muted">
        <strong className="text-text">Public</strong> sources (Polymarket gamma + data, GDELT, Wikipedia, Kalshi, Manifold) work
        immediately — no key, no binding. <strong className="text-text">Keyed</strong> sources
        (the-odds-api, FRED, Finnhub, NewsAPI, …) need a connection bound on the dashboard's
        Integrations page. Every source is optional; the gateway omits whatever's unavailable.
      </div>
    </>
  );
}

// ─── Query tab (test console) ──────────────────────────────────────

function QueryTab({ api, setError }: {
  api: <T>(p: string, q?: Record<string, string>) => Promise<T>;
  setError: (e: string | null) => void;
}) {
  const [market, setMarket] = useState("POLY:alcaraz-fo-2026");
  const [entityA, setEntityA] = useState("Carlos Alcaraz");
  const [entityB, setEntityB] = useState("Jannik Sinner");
  const [domain, setDomain] = useState("tennis");
  const [sport, setSport] = useState("tennis_atp");
  const [topic, setTopic] = useState("");
  const [result, setResult] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const run = async (path: string, params: Record<string, string>) => {
    setBusy(true); setResult(null);
    try {
      setResult(await api(path, params));
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };

  const inputCls = "w-full text-sm px-2 py-1.5 bg-bg-input border border-border rounded text-text";
  const labelCls = "block text-xs uppercase tracking-wide font-medium text-text-dim mb-1";

  return (
    <>
      <div className="p-3 border border-border rounded bg-bg-card mb-4">
        <div className="grid grid-cols-2 gap-3 mb-3">
          <div><label className={labelCls}>Market</label><input className={inputCls} value={market} onChange={(e) => setMarket(e.target.value)} /></div>
          <div><label className={labelCls}>Domain</label><input className={inputCls} value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="tennis · sports · macro · crypto" /></div>
          <div><label className={labelCls}>Entity A (priced outcome)</label><input className={inputCls} value={entityA} onChange={(e) => setEntityA(e.target.value)} /></div>
          <div><label className={labelCls}>Entity B</label><input className={inputCls} value={entityB} onChange={(e) => setEntityB(e.target.value)} /></div>
          <div><label className={labelCls}>Sport key (the-odds-api)</label><input className={inputCls} value={sport} onChange={(e) => setSport(e.target.value)} placeholder="tennis_atp · basketball_nba" /></div>
          <div><label className={labelCls}>News topic (optional)</label><input className={inputCls} value={topic} onChange={(e) => setTopic(e.target.value)} placeholder="defaults to entities" /></div>
        </div>
        <div className="flex gap-2 flex-wrap justify-end">
          <button disabled={busy} onClick={() => run("/query/context", { topic: topic || `${entityA} ${entityB}` })}
            className="px-3 py-1 text-sm rounded border border-border text-text hover:bg-bg-hover disabled:opacity-50">context</button>
          <button disabled={busy} onClick={() => run("/query/probability", { entity: entityA, domain, sport })}
            className="px-3 py-1 text-sm rounded border border-border text-text hover:bg-bg-hover disabled:opacity-50">probability</button>
          <button disabled={busy} onClick={() => run("/query/enrich", { market, entity_a: entityA, entity_b: entityB, domain, sport, topic })}
            className="px-3 py-1 text-sm rounded bg-accent text-bg font-medium hover:opacity-90 disabled:opacity-50">{busy ? "Running…" : "Run enrich"}</button>
        </div>
      </div>

      {result != null && (
        <div className="border border-border rounded bg-bg-card overflow-hidden">
          <div className="px-3 py-2 border-b border-border text-xs font-semibold uppercase tracking-wide text-text-dim">Result</div>
          <pre className="p-3 text-xs overflow-auto whitespace-pre-wrap" style={{ maxHeight: 480 }}>
            {JSON.stringify(result, null, 2)}
          </pre>
        </div>
      )}
      {result == null && !busy && (
        <div className="p-8 text-center text-text-muted text-sm">
          <div className="font-medium text-text mb-1">Test the gateway</div>
          <div className="opacity-80 text-xs">Fill the form and run a query. Bind sources first (Sources tab) — unbound sources are silently skipped.</div>
        </div>
      )}
    </>
  );
}
