// Intelligence world-model tabs: Overview, Questions, World, Scenarios,
// Ledger, Connectors. SVG uses attributes + dashboard CSS variables
// (panel sources are not scanned by the dashboard's Tailwind JIT).

import { useCallback, useEffect, useMemo, useState } from "react";

export type Api = <T>(path: string, params?: Record<string, string>) => Promise<T>;
export type Post = <T>(path: string, body: unknown) => Promise<T>;
export type SetError = (e: string | null) => void;

export const card = "border border-border rounded bg-bg-card";
export const input = "text-sm px-2 py-1.5 bg-bg-input border border-border rounded text-text";
export const btn = "px-3 py-1 rounded bg-accent text-bg font-medium disabled:opacity-50";
export const btnGhost = "px-2 py-1 rounded border border-border text-text-muted hover:text-text disabled:opacity-50";

export const pct = (p?: number | null) => (p == null ? "—" : `${(p * 100).toFixed(1)}%`);
export const when = (s?: string | null) => (s ? new Date(s).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) : "—");
const ago = (sec?: number) => (sec == null || sec < 0 ? "never" : sec < 120 ? `${sec}s ago` : sec < 7200 ? `${Math.round(sec / 60)}m ago` : sec < 172800 ? `${Math.round(sec / 3600)}h ago` : `${Math.round(sec / 86400)}d ago`);

export function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return <div className={`${card} p-3`}>
    <div className="text-xs uppercase tracking-wide text-text-dim">{label}</div>
    <div className="text-xl font-semibold mt-1">{value}</div>
    {hint && <div className="text-xs text-text-muted mt-1">{hint}</div>}
  </div>;
}

export function Badge({ children, tone = "muted" }: { children: React.ReactNode; tone?: "muted" | "accent" | "good" | "bad" | "warn" }) {
  const color = { muted: "var(--text-muted)", accent: "var(--accent)", good: "var(--color-green)", bad: "var(--color-error)", warn: "var(--color-warn)" }[tone];
  return <span className="text-xs px-1.5 py-0.5 rounded border" style={{ color, borderColor: color }}>{children}</span>;
}

const statusTone = (s: string): "good" | "accent" | "warn" | "muted" => (s === "observed" ? "good" : s === "reported" ? "accent" : s === "assumed" ? "warn" : "muted");

export function Empty({ children }: { children: React.ReactNode }) {
  return <div className="p-8 text-center text-text-muted">{children}</div>;
}

export function useLoad<T>(load: () => Promise<T>, deps: unknown[], setError: SetError) {
  const [data, setData] = useState<T | null>(null);
  const [busy, setBusy] = useState(false);
  const reload = useCallback(async () => {
    setBusy(true);
    try { setData(await load()); setError(null); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }, deps); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { reload(); }, [reload]);
  return { data, busy, reload };
}

// ─── Overview ──────────────────────────────────────────────────────

interface Move { question_id: number; question: string; from: number; to: number; delta: number; trigger: string; explanation: string; at: string }
interface QRow { id: number; text: string; venue: string; url?: string; deadline: string; status: string; outcome?: number | null; market_price?: number; evidence_count: number; category?: string; resolved_at?: string;
  forecast?: { probability?: number | null; abstained: boolean; revision: number; adjustment: number; trigger: string; created_at: string } }
interface Overview {
  since: string; new_evidence: { total: number; by_source: { source: string; count: number }[] }; forecast_moves: Move[]; new_forecasts: number;
  closing_soon: QRow[]; resolved: QRow[]; questions: { open: number; new: number };
  sources: { slug: string; enabled: boolean; freshness_sec: number; last_error: string; items_total: number }[];
  world: { entities: number; relationships: number; claims: number; evidence: number };
  llm: { enabled: boolean; model: string; calls_today: number; daily_limit: number };
  quality?: { summary: Record<string, number>; issues: Issue[]; total: number; basis: Record<string, Record<string, number>>; access: Record<string, number> };
}
interface Issue { type: string; severity: string; code: string; title: string; detail: string; entity_ids?: number[] }

const issueColor = (t: string) => (t === "conflict" ? "var(--color-error)" : t === "stale" ? "var(--color-warn)" : "var(--text-muted)");
const basisColor = (b: string) => (b === "observed" ? "var(--color-green)" : b === "inferred" ? "var(--accent)" : "var(--color-purple, #9b87f5)");
function BasisBadge({ basis }: { basis?: string }) {
  if (!basis) return null;
  return <span className="text-xs px-1.5 py-0.5 rounded border" style={{ color: basisColor(basis), borderColor: basisColor(basis) }}>{basis}</span>;
}
function IssueLine({ i }: { i: Issue }) {
  return <div className="px-3 py-1.5 border-b border-border text-sm">
    <div className="flex gap-2 items-start"><span className="text-xs px-1.5 rounded border shrink-0" style={{ color: issueColor(i.type), borderColor: issueColor(i.type) }}>{i.type}</span><span className="flex-1">{i.title}</span></div>
    <div className="text-xs text-text-dim mt-0.5">{i.detail}</div>
  </div>;
}

function DataHealth({ q }: { q: NonNullable<Overview["quality"]> }) {
  const sum = (b: string) => Object.values(q.basis || {}).reduce((n, m) => n + (m[b] || 0), 0);
  return <section className={card}>
    <div className="px-3 py-2 border-b border-border flex flex-wrap gap-3 items-center">
      <span className="font-semibold text-sm mr-auto">Data health</span>
      {(["conflict", "stale", "missing"] as const).map(t => <span key={t} className="text-xs"><b style={{ color: q.summary[t] ? issueColor(t) : undefined }}>{q.summary[t] || 0}</b> <span className="text-text-muted">{t}</span></span>)}
      <span className="text-xs text-text-dim">|</span>
      {(["observed", "inferred", "synthetic"] as const).map(b => <span key={b} className="text-xs flex items-center gap-1"><BasisBadge basis={b} /><span className="text-text-muted">{sum(b)}</span></span>)}
      <span className="text-xs text-text-dim">| {q.access.restricted || 0} restricted · {q.access.internal || 0} internal evidence</span>
    </div>
    {q.issues.length === 0 ? <Empty>No stale, missing or conflicting data found.</Empty> : q.issues.map((i, n) => <IssueLine key={n} i={i} />)}
    {q.total > q.issues.length && <div className="px-3 py-2 text-xs text-text-dim">{q.total - q.issues.length} more on the Map tab.</div>}
  </section>;
}

export function OverviewTab({ api, post, setError, openQuestion }: { api: Api; post: Post; setError: SetError; openQuestion: (id: number) => void }) {
  const { data, busy, reload } = useLoad(() => api<Overview>("/overview"), [api], setError);
  const [running, setRunning] = useState(false);
  const runNow = async () => {
    setRunning(true);
    try { await post("/ingest", {}); await reload(); } catch (e) { setError((e as Error).message); } finally { setRunning(false); }
  };
  if (!data) return <Empty>{busy ? "Loading…" : "No data yet."}</Empty>;
  const failing = data.sources.filter(s => s.enabled && s.last_error);
  return <div className="space-y-4">
    <div className="flex items-center gap-2">
      <div><div className="font-semibold">What changed</div><div className="text-xs text-text-muted">Since {when(data.since)} · evidence, forecasts and outcomes</div></div>
      <span className="flex-1" />
      <button className={btnGhost} onClick={reload} disabled={busy}>Refresh</button>
      <button className={btn} onClick={runNow} disabled={running}>{running ? "Running…" : "Run ingestion now"}</button>
    </div>
    <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(150px,1fr))" }}>
      <Stat label="Open questions" value={String(data.questions.open)} hint={`${data.questions.new} new`} />
      <Stat label="New evidence" value={String(data.new_evidence.total)} hint={`${data.world.evidence} total`} />
      <Stat label="Forecasts moved" value={String(data.forecast_moves.length)} hint={`${data.new_forecasts} first forecasts`} />
      <Stat label="World model" value={`${data.world.entities} entities`} hint={`${data.world.relationships} links · ${data.world.claims} claims`} />
      <Stat label="LLM" value={data.llm.enabled ? `${data.llm.calls_today}/${data.llm.daily_limit}` : "off"} hint={data.llm.enabled ? data.llm.model : "forecasts equal their prior"} />
    </div>
    {failing.length > 0 && <div className={`${card} p-3 text-sm`} style={{ borderColor: "var(--color-warn)" }}>
      {failing.map(s => <div key={s.slug}><b>{s.slug}</b>: <span className="text-text-muted">{s.last_error}</span></div>)}
    </div>}
    {data.quality && <DataHealth q={data.quality} />}
    <section className={card}>
      <div className="px-3 py-2 border-b border-border font-semibold text-sm">Forecasts that moved</div>
      {data.forecast_moves.length === 0 ? <Empty>No forecast moved by 3 points or more in this window.</Empty> :
        data.forecast_moves.map(m => <button key={`${m.question_id}-${m.at}`} onClick={() => openQuestion(m.question_id)} className="block w-full text-left px-3 py-2 border-b border-border hover:bg-bg-hover">
          <div className="flex gap-2 items-center"><span className="font-medium flex-1">{m.question}</span>
            <span className="text-sm tabular-nums">{pct(m.from)} → <b>{pct(m.to)}</b></span>
            <span className="text-xs tabular-nums" style={{ color: m.delta > 0 ? "var(--color-green)" : "var(--color-error)" }}>{m.delta > 0 ? "+" : ""}{(m.delta * 100).toFixed(1)}</span></div>
          <div className="text-xs text-text-muted mt-1">{m.trigger.replace("_", " ")} · {m.explanation}</div>
        </button>)}
    </section>
    <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(320px,1fr))" }}>
      <section className={card}>
        <div className="px-3 py-2 border-b border-border font-semibold text-sm">Closing within 48h</div>
        {data.closing_soon.length === 0 ? <Empty>Nothing closes in the next 48 hours.</Empty> : data.closing_soon.map(q => <QuestionLine key={q.id} q={q} onOpen={openQuestion} />)}
      </section>
      <section className={card}>
        <div className="px-3 py-2 border-b border-border font-semibold text-sm">Resolved</div>
        {data.resolved.length === 0 ? <Empty>No resolutions in this window.</Empty> : data.resolved.map(q => <QuestionLine key={q.id} q={q} onOpen={openQuestion} />)}
      </section>
    </div>
    <section className={card}>
      <div className="px-3 py-2 border-b border-border font-semibold text-sm">New evidence by source</div>
      <div className="px-3 py-2 flex flex-wrap gap-2 text-sm">{data.new_evidence.by_source.length === 0 ? <span className="text-text-muted">None.</span> :
        data.new_evidence.by_source.map(s => <span key={s.source} className="px-2 py-0.5 rounded bg-bg-input">{s.source} <b>{s.count}</b></span>)}</div>
    </section>
  </div>;
}

function QuestionLine({ q, onOpen }: { q: QRow; onOpen: (id: number) => void }) {
  const p = q.forecast?.probability;
  return <button onClick={() => onOpen(q.id)} className="block w-full text-left px-3 py-2 border-b border-border hover:bg-bg-hover">
    <div className="flex gap-2 items-center"><span className="flex-1">{q.text}</span>
      {q.status === "resolved" ? <Badge tone={q.outcome === 1 ? "good" : "bad"}>{q.outcome === 1 ? "YES" : "NO"}</Badge> : null}
      <span className="text-sm tabular-nums">{pct(p)}</span></div>
    <div className="text-xs text-text-dim mt-0.5">{q.venue || "manual"} · deadline {when(q.deadline)} · market {pct(q.market_price)}</div>
  </button>;
}

// ─── Questions ─────────────────────────────────────────────────────

interface Forecast { id: number; revision: number; probability?: number | null; abstained: boolean; abstain_reason?: string; prior?: number | null; market_price?: number | null;
  adjustment: number; evidence_cutoff: string; snapshot_hash: string; evidence_ids: number[]; model: string; model_version: string; assumptions: string[]; explanation: string; trigger: string; created_at: string }
interface QuestionDetail {
  question: { id: number; text: string; resolution_rule: string; resolution_source: string; deadline: string; status: string; outcome?: number | null; url?: string; venue?: string; topics: string[] };
  forecasts: Forecast[]; prices: { price: number; observed_at: string }[];
  evidence: { id: number; title: string; source: string; source_ref?: string; published_time?: string; ingested_at?: string }[];
  entities: { id: number; name: string; kind: string }[];
}

export function QuestionsTab({ api, post, setError, selected, setSelected }: { api: Api; post: Post; setError: SetError; selected: number | null; setSelected: (id: number | null) => void }) {
  const [status, setStatus] = useState("open");
  const [filter, setFilter] = useState("");
  const { data, busy, reload } = useLoad(() => api<{ questions: QRow[] }>("/questions", { status, limit: "300" }), [api, status], setError);
  const rows = useMemo(() => (data?.questions || []).filter(q => !filter || q.text.toLowerCase().includes(filter.toLowerCase())), [data, filter]);
  if (selected) return <QuestionView id={selected} api={api} post={post} setError={setError} back={() => { setSelected(null); reload(); }} />;
  return <div className="space-y-3">
    <div className="flex gap-2 items-center">
      <select className={input} value={status} onChange={e => setStatus(e.target.value)}>
        {["open", "resolved", "void", "all"].map(s => <option key={s} value={s}>{s}</option>)}
      </select>
      <input className={`${input} flex-1`} placeholder="Filter questions" value={filter} onChange={e => setFilter(e.target.value)} />
      <button className={btnGhost} onClick={reload} disabled={busy}>Refresh</button>
    </div>
    <div className={`${card} overflow-auto`}>
      <table className="w-full text-sm">
        <thead><tr className="text-left text-xs text-text-dim border-b border-border">
          <th className="px-3 py-2">Question</th><th className="px-2">Source</th><th className="px-2">Deadline</th><th className="px-2 text-right">Market</th><th className="px-2 text-right">Forecast</th><th className="px-2 text-right">Evidence</th>
        </tr></thead>
        <tbody>{rows.map(q => {
          const p = q.forecast?.probability;
          const diff = p != null && q.market_price != null ? p - q.market_price : null;
          return <tr key={q.id} className="border-b border-border hover:bg-bg-hover cursor-pointer" onClick={() => setSelected(q.id)}>
            <td className="px-3 py-2">{q.text}{q.status === "resolved" && <span className="ml-2"><Badge tone={q.outcome === 1 ? "good" : "bad"}>{q.outcome === 1 ? "YES" : "NO"}</Badge></span>}</td>
            <td className="px-2 text-text-muted">{q.venue || "manual"}</td>
            <td className="px-2 text-text-muted whitespace-nowrap">{when(q.deadline)}</td>
            <td className="px-2 text-right tabular-nums">{pct(q.market_price)}</td>
            <td className="px-2 text-right tabular-nums">{q.forecast?.abstained ? <span className="text-text-dim">abstain</span> : pct(p)}
              {diff != null && Math.abs(diff) >= 0.005 && <span className="text-xs ml-1" style={{ color: diff > 0 ? "var(--color-green)" : "var(--color-error)" }}>{diff > 0 ? "+" : ""}{(diff * 100).toFixed(1)}</span>}</td>
            <td className="px-2 text-right tabular-nums">{q.evidence_count}</td>
          </tr>;
        })}</tbody>
      </table>
      {!busy && rows.length === 0 && <Empty>No {status === "all" ? "" : status} questions yet. The prediction-markets connector imports questions closing in the next days; run ingestion from Overview.</Empty>}
    </div>
  </div>;
}

function ProbabilityChart({ prices, forecasts, deadline }: { prices: { price: number; observed_at: string }[]; forecasts: Forecast[]; deadline: string }) {
  const W = 640, H = 180, P = 28;
  const pts = [...prices.map(p => +new Date(p.observed_at)), ...forecasts.map(f => +new Date(f.created_at))];
  if (pts.length === 0) return null;
  const t0 = Math.min(...pts), t1 = Math.max(Math.max(...pts) + 1, Math.min(+new Date(deadline), Math.max(...pts) + 86400000));
  const x = (t: number) => P + ((t - t0) / Math.max(1, t1 - t0)) * (W - 2 * P);
  const y = (p: number) => H - P - p * (H - 2 * P);
  const step = (series: { t: number; p: number }[]) => series.map((s, i) => `${i === 0 ? "M" : `H${x(s.t).toFixed(1)}V`}${i === 0 ? `${x(s.t).toFixed(1)},${y(s.p).toFixed(1)}` : y(s.p).toFixed(1)}`).join(" ") + (series.length ? ` H${x(t1).toFixed(1)}` : "");
  const market = prices.map(p => ({ t: +new Date(p.observed_at), p: p.price }));
  const model = forecasts.filter(f => f.probability != null).map(f => ({ t: +new Date(f.created_at), p: f.probability as number }));
  return <svg viewBox={`0 0 ${W} ${H}`} width="100%" role="img" aria-label="Market price and forecast over time">
    {[0, 0.25, 0.5, 0.75, 1].map(g => <g key={g}><line x1={P} x2={W - P} y1={y(g)} y2={y(g)} stroke="var(--border)" strokeWidth="1" />
      <text x={4} y={y(g) + 3} fontSize="9" fill="var(--text-dim)">{g * 100}%</text></g>)}
    {market.length > 0 && <path d={step(market)} fill="none" stroke="var(--text-muted)" strokeWidth="1.5" strokeDasharray="4 3" />}
    {model.length > 0 && <path d={step(model)} fill="none" stroke="var(--accent)" strokeWidth="2" />}
    {model.map((m, i) => <circle key={i} cx={x(m.t)} cy={y(m.p)} r="3" fill="var(--accent)" />)}
    <text x={W - P} y={12} fontSize="10" textAnchor="end" fill="var(--text-muted)">dashed: market · solid: forecast</text>
  </svg>;
}

function QuestionView({ id, api, post, setError, back }: { id: number; api: Api; post: Post; setError: SetError; back: () => void }) {
  const { data, busy, reload } = useLoad(() => api<QuestionDetail>("/question", { id: String(id) }), [api, id], setError);
  const [forecasting, setForecasting] = useState(false);
  const forecastNow = async () => {
    setForecasting(true);
    try { await post("/forecast-now", { question_id: id }); await reload(); } catch (e) { setError((e as Error).message); } finally { setForecasting(false); }
  };
  if (!data) return <Empty>{busy ? "Loading…" : "Not found."}</Empty>;
  const q = data.question;
  const revs = [...data.forecasts].reverse();
  const open = q.status === "open" && new Date(q.deadline) > new Date();
  return <div className="space-y-4">
    <div className="flex items-start gap-2">
      <button className={btnGhost} onClick={back}>Back</button>
      <div className="flex-1">
        <div className="font-semibold">{q.text}</div>
        <div className="text-xs text-text-muted mt-1">{q.venue || "manual"} · deadline {when(q.deadline)} · {q.status}{q.status === "resolved" ? ` (${q.outcome === 1 ? "YES" : "NO"})` : ""}
          {q.url && <> · <a href={q.url} target="_blank" rel="noreferrer" className="text-accent">source</a></>}</div>
      </div>
      {open && <button className={btn} onClick={forecastNow} disabled={forecasting}>{forecasting ? "Forecasting…" : "Forecast now"}</button>}
    </div>
    <div className={`${card} p-3`}><ProbabilityChart prices={data.prices} forecasts={data.forecasts} deadline={q.deadline} /></div>
    <details className={`${card} p-3`}><summary className="cursor-pointer text-sm font-semibold">Resolution rule</summary>
      <div className="text-sm text-text-muted whitespace-pre-wrap mt-2">{q.resolution_rule || "—"}</div>
      <div className="text-xs text-text-dim mt-2">Outcome source: {q.resolution_source}</div></details>
    <section className={card}>
      <div className="px-3 py-2 border-b border-border font-semibold text-sm">Forecast revisions</div>
      {revs.length === 0 ? <Empty>No forecast recorded yet.</Empty> : revs.map(f => <div key={f.id} className="px-3 py-2 border-b border-border">
        <div className="flex gap-2 items-center text-sm">
          <b>r{f.revision}</b><span className="tabular-nums">{f.abstained ? "abstained" : pct(f.probability)}</span>
          <span className="text-text-dim">prior {pct(f.prior)} · market {pct(f.market_price)}{f.adjustment ? ` · shift ${f.adjustment > 0 ? "+" : ""}${f.adjustment}` : ""}</span>
          <span className="flex-1" /><Badge>{f.trigger.replace("_", " ")}</Badge><span className="text-xs text-text-dim">{when(f.created_at)}</span>
        </div>
        <div className="text-xs text-text-muted mt-1">{f.abstained ? f.abstain_reason : f.explanation}</div>
        {f.assumptions.length > 0 && <div className="text-xs mt-1" style={{ color: "var(--color-warn)" }}>Assumes: {f.assumptions.join("; ")}</div>}
        <div className="text-xs text-text-dim mt-1">cutoff {when(f.evidence_cutoff)} · {f.evidence_ids.length} evidence · {f.model} {f.model_version} · snapshot {f.snapshot_hash.slice(0, 10)}</div>
      </div>)}
    </section>
    <section className={card}>
      <div className="px-3 py-2 border-b border-border font-semibold text-sm">Linked evidence</div>
      {data.evidence.length === 0 ? <Empty>No evidence linked yet. The news connector searches each open question's topics ({q.topics.join(", ") || "none"}).</Empty> :
        data.evidence.map(e => <div key={e.id} className="px-3 py-2 border-b border-border text-sm">
          <span className="text-text-dim text-xs mr-2">#{e.id}</span>
          {e.source_ref?.startsWith("http") ? <a href={e.source_ref} target="_blank" rel="noreferrer" className="hover:text-accent">{e.title}</a> : e.title}
          <div className="text-xs text-text-dim">{e.source} · published {when(e.published_time)} · ingested {when(e.ingested_at)}</div>
        </div>)}
    </section>
    {data.entities.length > 0 && <div className="flex flex-wrap gap-2 text-sm">{data.entities.map(e => <span key={e.id} className="px-2 py-0.5 rounded bg-bg-input">{e.name} <span className="text-text-dim text-xs">{e.kind}</span></span>)}</div>}
  </div>;
}

// ─── World ─────────────────────────────────────────────────────────

interface Entity { id: number; name: string; kind: string; description: string; aliases: string[]; external_ids: Record<string, string>; basis?: string }
interface Rel { id: number; from_entity_id: number; from: string; type: string; to_entity_id: number; to: string; status: string; confidence?: number; evidence_ids: number[]; basis?: string }
interface EntityLoc { lat: number; lon: number; label?: string; precision: string; provenance: string; source: string; basis: string; evidence_id?: number }
interface Series { variable: string; source: string; latest_at: string; cadence_days: number; age_days: number; status: string }
interface EntityDetail { entity: Entity; relationships: Rel[]; claims: { id: number; subject: string; predicate: string; object: string; status: string; evidence_id: number; basis?: string }[];
  state: { variable: string; value: number; unit: string; valid_at: string; source: string; basis?: string }[]; open_questions: QRow[];
  locations?: EntityLoc[]; issues?: Issue[]; series?: Series[] }

const KINDS = ["", "organization", "facility", "place", "region", "commodity", "product", "technology", "market", "institution", "infrastructure", "event", "indicator", "public_role", "concept"];

export function WorldTab({ api, setError, openQuestion }: { api: Api; setError: SetError; openQuestion: (id: number) => void }) {
  const [query, setQuery] = useState("");
  const [kind, setKind] = useState("");
  const [selected, setSelected] = useState<number | null>(null);
  const { data, busy, reload } = useLoad(() => api<{ entities: Entity[] }>("/entities", { query, kind, limit: "200" }), [api, kind], setError);
  return <div className="grid gap-4" style={{ gridTemplateColumns: "minmax(240px,1fr) minmax(0,2fr)" }}>
    <div className="space-y-2">
      <div className="flex gap-2">
        <input className={`${input} flex-1`} placeholder="Search entities" value={query} onChange={e => setQuery(e.target.value)} onKeyDown={e => { if (e.key === "Enter") reload(); }} />
        <select className={input} value={kind} onChange={e => setKind(e.target.value)}>{KINDS.map(k => <option key={k} value={k}>{k || "any kind"}</option>)}</select>
      </div>
      <div className={`${card} overflow-auto`} style={{ maxHeight: 560 }}>
        {(data?.entities || []).map(e => <button key={e.id} onClick={() => setSelected(e.id)} className={`block w-full text-left px-3 py-1.5 border-b border-border hover:bg-bg-hover ${selected === e.id ? "bg-bg-hover" : ""}`}>
          <span className="text-sm">{e.name}</span> <span className="text-xs text-text-dim">{e.kind}</span></button>)}
        {!busy && (data?.entities || []).length === 0 && <Empty>No entities yet. They are created from question topics, measured series, and LLM extraction of headlines.</Empty>}
      </div>
    </div>
    <div>{selected ? <EntityView id={selected} api={api} setError={setError} select={setSelected} openQuestion={openQuestion} /> : <Empty>Select an entity to see its relationships, claims, measurements and linked questions.</Empty>}</div>
  </div>;
}

function EntityGraph({ center, rels, select }: { center: Entity; rels: Rel[]; select: (id: number) => void }) {
  const W = 560, H = 300, cx = W / 2, cy = H / 2, R = 115;
  const others = rels.slice(0, 16).map(r => r.from_entity_id === center.id ? { id: r.to_entity_id, name: r.to, rel: r, out: true } : { id: r.from_entity_id, name: r.from, rel: r, out: false });
  const dash: Record<string, string> = { observed: "", reported: "", inferred: "5 3", assumed: "2 3" };
  const color: Record<string, string> = { observed: "var(--color-green)", reported: "var(--accent)", inferred: "var(--text-muted)", assumed: "var(--color-warn)" };
  return <svg viewBox={`0 0 ${W} ${H}`} width="100%" role="img" aria-label="Relationships around the selected entity">
    {others.map((o, i) => {
      const a = (i / Math.max(1, others.length)) * Math.PI * 2 - Math.PI / 2;
      const x = cx + Math.cos(a) * R * 1.6, y = cy + Math.sin(a) * R;
      return <g key={o.rel.id} style={{ cursor: "pointer" }} onClick={() => select(o.id)}>
        <line x1={cx} y1={cy} x2={x} y2={y} stroke={color[o.rel.status] || "var(--border)"} strokeWidth="1.5" strokeDasharray={dash[o.rel.status] || ""} />
        <text x={(cx + x) / 2} y={(cy + y) / 2 - 3} fontSize="9" textAnchor="middle" fill="var(--text-dim)">{o.out ? o.rel.type : `${o.rel.type} (in)`}</text>
        <circle cx={x} cy={y} r="5" fill="var(--bg-card)" stroke={color[o.rel.status] || "var(--border)"} strokeWidth="1.5" />
        <text x={x} y={y + 16} fontSize="10" textAnchor="middle" fill="var(--text-muted)">{o.name.length > 24 ? o.name.slice(0, 23) + "…" : o.name}</text>
      </g>;
    })}
    <circle cx={cx} cy={cy} r="8" fill="var(--accent)" />
    <text x={cx} y={cy - 14} fontSize="11" fontWeight="600" textAnchor="middle" fill="var(--text)">{center.name}</text>
  </svg>;
}

function EntityView({ id, api, setError, select, openQuestion }: { id: number; api: Api; setError: SetError; select: (id: number) => void; openQuestion: (id: number) => void }) {
  const { data, busy } = useLoad(() => api<EntityDetail>("/entity", { id: String(id) }), [api, id], setError);
  if (!data) return <Empty>{busy ? "Loading…" : "Not found."}</Empty>;
  const latest = new Map<string, EntityDetail["state"][number]>();
  data.state.forEach(s => { if (!latest.has(s.variable)) latest.set(s.variable, s); });
  return <div className="space-y-3">
    <div><div className="font-semibold flex items-center gap-2">{data.entity.name} <span className="text-xs text-text-dim font-normal">{data.entity.kind}</span><BasisBadge basis={data.entity.basis} /></div>
      {data.entity.aliases.length > 0 && <div className="text-xs text-text-muted">Also: {data.entity.aliases.join(", ")}</div>}
      {data.entity.description && <div className="text-sm text-text-muted mt-1">{data.entity.description}</div>}
      <div className="text-xs mt-1">{data.locations && data.locations.length > 0
        ? data.locations.map((l, i) => <div key={i} className="text-text-muted">Location: {l.label || `${l.lat.toFixed(2)}, ${l.lon.toFixed(2)}`} ({l.precision}) · {l.source}: {l.provenance}</div>)
        : <span className="text-text-dim">No location recorded.</span>}</div></div>
    {data.issues && data.issues.length > 0 && <section className={card}><div className="px-3 py-2 border-b border-border font-semibold text-sm">Data quality</div>
      {data.issues.map((i, n) => <IssueLine key={n} i={i} />)}</section>}
    {data.relationships.length > 0 && <div className={`${card} p-2`}><EntityGraph center={data.entity} rels={data.relationships} select={select} />
      <div className="flex gap-3 text-xs text-text-dim px-2"><Badge tone="good">observed</Badge><Badge tone="accent">reported</Badge><Badge>inferred</Badge><Badge tone="warn">assumed</Badge></div></div>}
    {latest.size > 0 && <div className="grid gap-2" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(160px,1fr))" }}>
      {[...latest.values()].map(s => { const f = data.series?.find(x => x.variable === s.variable && x.source === s.source);
        return <Stat key={`${s.variable}-${s.source}`} label={`${s.variable} · ${s.source}${s.basis && s.basis !== "observed" ? " · " + s.basis : ""}`} value={`${s.value} ${s.unit}`}
          hint={`as of ${new Date(s.valid_at).toLocaleDateString()}${f ? f.status === "stale" ? ` · STALE (usually every ${f.cadence_days} d)` : f.cadence_days ? ` · every ~${f.cadence_days} d` : "" : ""}`} />; })}</div>}
    <section className={card}><div className="px-3 py-2 border-b border-border font-semibold text-sm">Relationships</div>
      {data.relationships.length === 0 ? <Empty>No relationships recorded.</Empty> : data.relationships.map(r => <div key={r.id} className="px-3 py-1.5 border-b border-border text-sm flex gap-2 items-center">
        <span className="flex-1">{r.from} <span className="text-text-dim">{r.type}</span> {r.to}</span><Badge tone={statusTone(r.status)}>{r.status}</Badge>{r.basis === "synthetic" && <BasisBadge basis="synthetic" />}
        <span className="text-xs text-text-dim">{r.evidence_ids.length ? `evidence ${r.evidence_ids.join(", ")}` : "no evidence"}</span></div>)}
    </section>
    {data.open_questions.length > 0 && <section className={card}><div className="px-3 py-2 border-b border-border font-semibold text-sm">Open questions</div>
      {data.open_questions.map(q => <QuestionLine key={q.id} q={q} onOpen={openQuestion} />)}</section>}
    {data.claims.length > 0 && <section className={card}><div className="px-3 py-2 border-b border-border font-semibold text-sm">Claims</div>
      {data.claims.map(c => <div key={c.id} className="px-3 py-1.5 border-b border-border text-sm flex gap-2"><span className="flex-1">{c.subject} <span className="text-text-dim">{c.predicate}</span> {c.object}</span>
        <Badge tone={c.status === "accepted" ? "good" : c.status === "rejected" ? "bad" : "muted"}>{c.status}</Badge><BasisBadge basis={c.basis} /><span className="text-xs text-text-dim">#{c.evidence_id}</span></div>)}</section>}
  </div>;
}

// Scenarios live in ScenariosTab.tsx.

// ─── Ledger ────────────────────────────────────────────────────────

interface Summary { n: number; brier_model?: number; brier_market?: number; brier_uniform?: number; log_loss_model?: number; log_loss_market?: number; skill_vs_market?: number; n_with_market?: number }
interface Ledger { summary: Summary; by_horizon: Record<string, Summary>; by_venue: Record<string, Summary>; calibration: { range: string; n: number; mean_forecast: number; observed_rate: number }[];
  counts: Record<string, number>; coverage: number; method: string; preliminary: boolean;
  scored: { question_id: number; question: string; outcome: number; probability: number; market_price?: number; horizon_bucket: string; brier_model: number; brier_market?: number }[] }

export function LedgerTab({ api, setError, openQuestion }: { api: Api; setError: SetError; openQuestion: (id: number) => void }) {
  const { data, busy } = useLoad(() => api<Ledger>("/ledger"), [api], setError);
  if (!data) return <Empty>{busy ? "Loading…" : "No ledger yet."}</Empty>;
  const s = data.summary;
  const f = (v?: number) => (v == null ? "—" : v.toFixed(3));
  return <div className="space-y-4">
    {data.preliminary && <div className={`${card} p-3 text-sm text-text-muted`} style={{ borderColor: "var(--color-warn)" }}>
      Preliminary: {s.n} scored question{s.n === 1 ? "" : "s"}. Scores on small samples are noisy; treat them as evidence the loop works, not of predictive skill.</div>}
    <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(140px,1fr))" }}>
      <Stat label="Scored" value={String(s.n)} hint={`${data.counts.resolved} resolved · ${data.counts.open} open`} />
      <Stat label="Brier: model" value={f(s.brier_model)} hint="lower is better" />
      <Stat label="Brier: market" value={f(s.brier_market)} hint={`${s.n_with_market ?? 0} with market`} />
      <Stat label="Brier: 0.5" value={f(s.brier_uniform)} />
      <Stat label="Skill vs market" value={s.skill_vs_market == null ? "—" : `${(s.skill_vs_market * 100).toFixed(1)}%`} hint="> 0 beats market" />
      <Stat label="Coverage" value={`${(data.coverage * 100).toFixed(0)}%`} hint={`${data.counts.abstained_only} abstained`} />
    </div>
    <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(300px,1fr))" }}>
      <section className={`${card} p-3`}><div className="font-semibold text-sm mb-2">Calibration</div><Calibration bins={data.calibration} /></section>
      <section className={card}><div className="px-3 py-2 border-b border-border font-semibold text-sm">By horizon</div>
        <table className="w-full text-sm"><tbody>{Object.entries(data.by_horizon).map(([k, v]) => <tr key={k} className="border-b border-border">
          <td className="px-3 py-1.5">{k}</td><td className="px-2 tabular-nums">n {v.n}</td><td className="px-2 tabular-nums">model {f(v.brier_model)}</td><td className="px-2 tabular-nums">market {f(v.brier_market)}</td></tr>)}</tbody></table>
        {Object.keys(data.by_horizon).length === 0 && <Empty>Nothing scored yet.</Empty>}</section>
    </div>
    <section className={card}><div className="px-3 py-2 border-b border-border font-semibold text-sm">Scored forecasts</div>
      {data.scored.length === 0 ? <Empty>Questions are scored once they resolve. Forecasts recorded after an outcome is known are never scored.</Empty> :
        <table className="w-full text-sm"><thead><tr className="text-left text-xs text-text-dim border-b border-border"><th className="px-3 py-1.5">Question</th><th className="px-2">Outcome</th><th className="px-2 text-right">Forecast</th><th className="px-2 text-right">Market</th><th className="px-2 text-right">Brier</th><th className="px-2">Horizon</th></tr></thead>
          <tbody>{data.scored.map(r => <tr key={r.question_id} className="border-b border-border hover:bg-bg-hover cursor-pointer" onClick={() => openQuestion(r.question_id)}>
            <td className="px-3 py-1.5">{r.question}</td><td className="px-2"><Badge tone={r.outcome ? "good" : "bad"}>{r.outcome ? "YES" : "NO"}</Badge></td>
            <td className="px-2 text-right tabular-nums">{pct(r.probability)}</td><td className="px-2 text-right tabular-nums">{pct(r.market_price)}</td>
            <td className="px-2 text-right tabular-nums" style={{ color: r.brier_market != null ? (r.brier_model <= r.brier_market ? "var(--color-green)" : "var(--color-error)") : undefined }}>{r.brier_model.toFixed(3)}</td>
            <td className="px-2 text-text-dim">{r.horizon_bucket}</td></tr>)}</tbody></table>}
    </section>
    <div className="text-xs text-text-dim">{data.method}</div>
  </div>;
}

function Calibration({ bins }: { bins: Ledger["calibration"] }) {
  const S = 220, P = 24;
  const xy = (v: number) => P + v * (S - 2 * P);
  const yy = (v: number) => S - P - v * (S - 2 * P);
  if (bins.length === 0) return <div className="text-sm text-text-muted">No resolved forecasts yet.</div>;
  return <svg viewBox={`0 0 ${S} ${S}`} width="100%" style={{ maxWidth: 280 }} role="img" aria-label="Calibration: forecast probability against observed frequency">
    <rect x={P} y={P} width={S - 2 * P} height={S - 2 * P} fill="none" stroke="var(--border)" />
    <line x1={xy(0)} y1={yy(0)} x2={xy(1)} y2={yy(1)} stroke="var(--text-dim)" strokeDasharray="3 3" />
    {bins.map(b => <circle key={b.range} cx={xy(b.mean_forecast)} cy={yy(b.observed_rate)} r={Math.min(10, 3 + Math.sqrt(b.n) * 1.5)} fill="var(--accent)" fillOpacity="0.7" />)}
    <text x={S / 2} y={S - 4} fontSize="9" textAnchor="middle" fill="var(--text-dim)">forecast</text>
    <text x={8} y={S / 2} fontSize="9" textAnchor="middle" fill="var(--text-dim)" transform={`rotate(-90 8 ${S / 2})`}>observed</text>
  </svg>;
}

// ─── Connectors ────────────────────────────────────────────────────

interface Source { slug: string; connector: string; enabled: boolean; interval_sec: number; permitted_use: string; licence: string; freshness_sec: number; last_error: string; items_total: number; calls_total: number; config: Record<string, unknown> }
interface Run { id: number; source: string; started_at: string; status: string; fetched: number; inserted: number; duplicates: number; calls: number; error: string }

export function ConnectorsTab({ api, post, setError }: { api: Api; post: Post; setError: SetError }) {
  const { data, busy, reload } = useLoad(() => api<{ sources: Source[]; recent_runs: Run[] }>("/connectors"), [api], setError);
  const [running, setRunning] = useState<string | null>(null);
  const toggle = async (s: Source) => {
    try { await post("/connector-configure", { slug: s.slug, enabled: !s.enabled }); reload(); } catch (e) { setError((e as Error).message); }
  };
  const runOne = async (slug: string) => {
    setRunning(slug);
    try { await post("/ingest", { source: slug, skip_forecasts: true }); reload(); } catch (e) { setError((e as Error).message); } finally { setRunning(null); }
  };
  return <div className="space-y-4">
    <section className={card}>
      <div className="px-3 py-2 border-b border-border flex items-center"><span className="font-semibold text-sm flex-1">Connectors</span><button className={btnGhost} onClick={reload} disabled={busy}>Refresh</button></div>
      {(data?.sources || []).map(s => <div key={s.slug} className="px-3 py-2 border-b border-border">
        <div className="flex gap-2 items-center text-sm">
          <span className="font-medium">{s.slug}</span>
          <Badge tone={!s.enabled ? "muted" : s.last_error ? "warn" : "good"}>{!s.enabled ? "disabled" : s.last_error ? "error" : "ok"}</Badge>
          <span className="text-xs text-text-dim">every {Math.round(s.interval_sec / 60)}m · last success {ago(s.freshness_sec)} · {s.items_total} items · {s.calls_total} calls</span>
          <span className="flex-1" />
          <button className={btnGhost} onClick={() => runOne(s.slug)} disabled={running !== null || !s.enabled}>{running === s.slug ? "Running…" : "Run"}</button>
          <button className={btnGhost} onClick={() => toggle(s)}>{s.enabled ? "Disable" : "Enable"}</button>
        </div>
        <div className="text-xs text-text-muted mt-1">{s.permitted_use} <span className="text-text-dim">· {s.licence}</span></div>
        {s.last_error && <div className="text-xs mt-1" style={{ color: "var(--color-warn)" }}>{s.last_error}</div>}
      </div>)}
    </section>
    <section className={card}><div className="px-3 py-2 border-b border-border font-semibold text-sm">Recent runs</div>
      <table className="w-full text-sm"><thead><tr className="text-left text-xs text-text-dim border-b border-border"><th className="px-3 py-1.5">Source</th><th className="px-2">Started</th><th className="px-2">Status</th><th className="px-2 text-right">Fetched</th><th className="px-2 text-right">New</th><th className="px-2 text-right">Dupes</th><th className="px-2 text-right">Calls</th></tr></thead>
        <tbody>{(data?.recent_runs || []).map(r => <tr key={r.id} className="border-b border-border" title={r.error}>
          <td className="px-3 py-1.5">{r.source}</td><td className="px-2 text-text-muted">{when(r.started_at)}</td><td className="px-2"><Badge tone={r.status === "ok" ? "good" : r.status === "error" ? "bad" : "muted"}>{r.status}</Badge></td>
          <td className="px-2 text-right tabular-nums">{r.fetched}</td><td className="px-2 text-right tabular-nums">{r.inserted}</td><td className="px-2 text-right tabular-nums">{r.duplicates}</td><td className="px-2 text-right tabular-nums">{r.calls}</td></tr>)}</tbody></table>
      {(data?.recent_runs || []).length === 0 && <Empty>No runs yet.</Empty>}
    </section>
  </div>;
}
