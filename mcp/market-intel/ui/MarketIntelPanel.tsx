// MarketIntelPanel — v0.1 console for the market-intel gateway.
// Two tabs:
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

type TabId = "sources" | "query";

export default function MarketIntelPanel({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<TabId>("sources");
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
        <span className="text-xs text-text-dim">gateway · v0.1</span>
        <span className="flex-1" />
      </header>

      <nav className="flex border-b border-border px-3 text-xs">
        {(["sources", "query"] as TabId[]).map((id) => (
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
        {tab === "sources" && <SourcesTab api={api} setError={setError} />}
        {tab === "query" && <QueryTab api={api} setError={setError} />}
      </div>
    </div>
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
