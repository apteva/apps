// Scenarios: conditional what-ifs with their evidence, replay, assumption and
// sensitivity report, and the historical shock test. Colours come from
// dashboard CSS variables through inline styles (panel sources are not
// scanned by the dashboard's Tailwind JIT).

import { useState } from "react";
import { Api, Badge, Empty, Post, SetError, btn, btnGhost, card, input, pct, useLoad, when } from "./WorldTabs";

interface Link { shock?: string; entity?: string; key?: string; from?: string; type?: string; to?: string; status?: string; causal_basis?: string; n_sources?: number; evidence_ids?: number[] }
interface Exposure { entity_id: number; entity: string; kind: string; impact: number; depth: number; relies_on_assumed_links: boolean; relies_on_weak_links?: boolean; relies_on_single_source_links?: boolean; chain: Link[] }
interface QExp { question_id: number; question: string; exposure?: number; impact?: number; via_entity: string; current_forecast_unchanged?: number; current_forecast?: number }
interface ScenarioResult {
  id: number; name: string; note: string; causal_note?: string; kind?: string; as_of?: string; known_at?: string; snapshot_hash?: string;
  exposed_entities: Exposure[]; exposed_questions: QExp[]; questions_note?: string;
  unsupported_shocks: { entity_id: number; reason: string }[]; unruled_links: { type: string; links: number }[];
  evidence_profile?: { links_used: number; by_causal_basis: Record<string, number>; single_source_links_used: number; links_in_graph: number; ungraded_links_in_graph: number; weak_links_not_used: number; unsupported_links_not_used: number };
  limitations?: string[];
}
interface OAT { assumption: string; kind: string; base: unknown; low: unknown; high: unknown; exposed_low: number; exposed_high: number; top5_overlap_low: number; top5_overlap_high: number; max_abs_impact_change: number }
interface Range { entity_id: number; entity: string; base_impact: number; p5: number; p50: number; p95: number; p_exposed: number; p_top5: number }
interface Frag { entity_id: number; entity: string; base_impact: number; path_links: number; critical_links: string[]; single_source_links_on_path: number; classification: string; why: string }
interface CI { mean: number | null; lo: number | null; hi: number | null }
interface HistGroup { n: number; events: number; mean_abs_move_after?: number; mean_abs_move_before?: number; share_moved_after?: number }
interface History { id?: number; created_at?: string; status?: string; note?: string; params?: { window_hours: number }; events_used?: number; observations?: number;
  groups?: Record<string, HistGroup>; exposed_vs_other?: { after_minus_other: CI; before_minus_other: CI }; verdict?: { status: string; detail: string } }
interface Report { draws: number; seed: number; assumptions: { assumption: string; source: string; used: boolean }[]; one_at_a_time: OAT[]; ranges: Range[]; ranges_note: string;
  fragility: Frag[]; classification_counts: Record<string, number>; historical_test?: History; graph_matches_run?: boolean; generated_at?: string; evidence_profile?: { links: number; by_causal_basis: Record<string, number>; links_by_source_count: Record<string, number> } }
interface Replay { reproduced: boolean; same_graph: boolean; same_exposures: boolean; stored_snapshot_hash: string; replayed_snapshot_hash: string; note?: string }

const basisTone = (b?: string): "good" | "accent" | "warn" | "muted" | "bad" =>
  b === "mechanism" ? "good" : b === "asserted" ? "accent" : b === "assumed" ? "warn" : b === "unsupported" ? "bad" : "muted";
const classTone = (c: string): "good" | "warn" | "bad" => (c === "fragile" ? "bad" : c === "sensitive" ? "warn" : "good");
const fmtCI = (c?: CI) => (!c || c.mean == null ? "—" : `${c.mean >= 0 ? "+" : ""}${c.mean.toFixed(3)}${c.lo != null && c.hi != null ? ` [${c.lo.toFixed(3)}, ${c.hi.toFixed(3)}]` : " (no interval)"}`);
const short = (h?: string) => (h ? h.slice(0, 12) : "—");

function Section({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return <section className={card}>
    <div className="px-3 py-2 border-b border-border"><div className="font-semibold text-sm">{title}</div>{hint && <div className="text-xs text-text-muted">{hint}</div>}</div>
    {children}
  </section>;
}

export function ScenariosTab({ api, post, setError, openQuestion }: { api: Api; post: Post; setError: SetError; openQuestion: (id: number) => void }) {
  const [entity, setEntity] = useState("");
  const [description, setDescription] = useState("");
  const [severity, setSeverity] = useState(1);
  const [asOf, setAsOf] = useState("");
  const [skipAssumed, setSkipAssumed] = useState(false);
  const [includeWeak, setIncludeWeak] = useState(false);
  const [result, setResult] = useState<ScenarioResult | null>(null);
  const [running, setRunning] = useState(false);
  const [grading, setGrading] = useState<string | null>(null);
  const history = useLoad(() => api<{ scenarios: { id: number; name: string; created_at: string; has_report: boolean; result: ScenarioResult }[] }>("/scenarios", { limit: "10" }), [api], setError);
  const run = async () => {
    setRunning(true);
    try {
      const body: Record<string, unknown> = { name: `${entity}: ${description}`, shocks: [{ entity, description, severity }], skip_assumed: skipAssumed, include_weak: includeWeak };
      if (asOf) body.as_of = new Date(asOf).toISOString();
      const r = await post<ScenarioResult>("/scenario-run", body);
      setResult(r); history.reload();
    } catch (e) { setError((e as Error).message); } finally { setRunning(false); }
  };
  const grade = async () => {
    setGrading("Grading…");
    try {
      const r = await post<{ links_graded: number; by_causal_basis: Record<string, number>; ungraded_links_remaining: number; links_by_causal_basis?: Record<string, number>; error?: string }>("/relationships-grade", { limit: 60 });
      const prof = Object.entries(r.links_by_causal_basis || {}).map(([k, v]) => `${k} ${v}`).join(" · ");
      setGrading(`Graded ${r.links_graded} links; ${r.ungraded_links_remaining} left ungraded. All links: ${prof}${r.error ? ` (stopped: ${r.error})` : ""}`);
    } catch (e) { setGrading(null); setError((e as Error).message); }
  };
  return <div className="space-y-4">
    <div className={`${card} p-3 space-y-2`}>
      <div><div className="font-semibold">Scenario</div>
        <div className="text-xs text-text-muted">Conditional, not a forecast: "if this shock happened, and links transmitted it as assumed, what would be exposed". Exposure scores are relative, not probabilities. Links are not causal findings.</div></div>
      <div className="grid gap-2" style={{ gridTemplateColumns: "minmax(160px,1fr) minmax(200px,2fr) 140px auto" }}>
        <input className={input} placeholder="Entity name (e.g. Port of Rotterdam)" value={entity} onChange={e => setEntity(e.target.value)} />
        <input className={input} placeholder="Shock (e.g. closed for 5 days)" value={description} onChange={e => setDescription(e.target.value)} />
        <label className="text-xs text-text-dim">Severity {severity.toFixed(2)}<input type="range" min="0.1" max="1" step="0.05" value={severity} onChange={e => setSeverity(+e.target.value)} className="block w-full" /></label>
        <button className={btn} onClick={run} disabled={running || !entity || !description}>{running ? "Running…" : "Run"}</button>
      </div>
      <div className="flex flex-wrap gap-4 items-center text-xs text-text-muted">
        <label className="flex gap-2 items-center"><input type="checkbox" checked={includeWeak} onChange={e => setIncludeWeak(e.target.checked)} /> Include weak links (co-mention, ungraded)</label>
        <label className="flex gap-2 items-center"><input type="checkbox" checked={skipAssumed} onChange={e => setSkipAssumed(e.target.checked)} /> Ignore operator-assumed links</label>
        <label className="flex gap-2 items-center">As of <input type="datetime-local" className={input} value={asOf} onChange={e => setAsOf(e.target.value)} /></label>
        <span>A past date only sees links recorded by then.</span>
      </div>
      <div className="flex gap-2 items-center text-xs text-text-muted">
        <button className={btnGhost} onClick={grade} disabled={grading === "Grading…"}>Grade links</button>
        <span>{grading || "Reads each ungraded link's headlines and records whether they describe a mechanism, assert it, only mention both, or do not support it."}</span>
      </div>
    </div>
    {result && <ScenarioView key={result.id} r={result} api={api} post={post} setError={setError} openQuestion={openQuestion} />}
    <Section title="Recent scenarios">
      {(history.data?.scenarios || []).length === 0 ? <Empty>No scenarios run yet.</Empty> :
        history.data!.scenarios.map(s => <button key={s.id} onClick={() => setResult({ ...s.result, id: s.id, name: s.name })} className="block w-full text-left px-3 py-1.5 border-b border-border hover:bg-bg-hover text-sm">
          #{s.id} {s.name} <span className="text-xs text-text-dim">{when(s.created_at)} · {s.result.exposed_entities?.length || 0} exposed{s.has_report ? " · report" : ""}</span></button>)}
    </Section>
    <HistoryTest api={api} post={post} setError={setError} />
  </div>;
}

function ChainLine({ chain }: { chain: Link[] }) {
  return <div className="text-xs text-text-dim mt-1 flex flex-wrap gap-x-1 gap-y-1 items-center">
    {chain.slice(1).map((c, i) => <span key={i} className="flex gap-1 items-center">{i > 0 && <span>→</span>}
      {c.from} <i>{c.type}</i> {c.to}
      <Badge tone={basisTone(c.causal_basis)}>{c.causal_basis || "ungraded"}</Badge>
      {c.status !== "assumed" && <span style={{ color: (c.n_sources || 0) <= 1 ? "var(--color-warn)" : "var(--text-muted)" }}>{c.n_sources || 0} src</span>}
    </span>)}
  </div>;
}

function ScenarioView({ r, api, post, setError, openQuestion }: { r: ScenarioResult; api: Api; post: Post; setError: SetError; openQuestion: (id: number) => void }) {
  const [replay, setReplay] = useState<Replay | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const stored = useLoad(() => (r.id ? api<{ report: Report | null }>("/scenario-report", { scenario_id: String(r.id) }).catch(() => ({ report: null })) : Promise.resolve({ report: null })), [api, r.id], setError);
  const [report, setReport] = useState<Report | null>(null);
  const rep = report || stored.data?.report || null;
  const prof = r.evidence_profile;
  const doReplay = async () => {
    setBusy("replay");
    try { setReplay(await post<Replay>("/scenario-replay", { scenario_id: r.id })); } catch (e) { setError((e as Error).message); } finally { setBusy(null); }
  };
  const doSensitivity = async () => {
    setBusy("sensitivity");
    try { setReport(await post<Report>("/scenario-sensitivity", { scenario_id: r.id, draws: 200, seed: 1 })); } catch (e) { setError((e as Error).message); } finally { setBusy(null); }
  };
  return <div className="space-y-3">
    <div className={`${card} p-3 space-y-1`}>
      <div className="flex gap-2 items-center flex-wrap"><span className="font-semibold text-sm flex-1">#{r.id} {r.name}</span>
        <Badge tone="warn">conditional scenario, not a forecast</Badge>
        {r.snapshot_hash && <button className={btnGhost} onClick={doReplay} disabled={!!busy}>{busy === "replay" ? "Replaying…" : "Replay"}</button>}
        {r.snapshot_hash && <button className={btnGhost} onClick={doSensitivity} disabled={!!busy}>{busy === "sensitivity" ? "Testing…" : rep ? "Re-run sensitivity" : "Sensitivity report"}</button>}
      </div>
      <div className="text-xs" style={{ color: "var(--color-warn)" }}>{r.note}</div>
      {r.causal_note && <div className="text-xs text-text-muted">{r.causal_note}</div>}
      {r.snapshot_hash ? <div className="text-xs text-text-dim">as of {when(r.as_of)} · links known at {when(r.known_at)} · graph {short(r.snapshot_hash)}</div>
        : <div className="text-xs text-text-dim">Run before 0.7.0: no stored spec, so it cannot be replayed or tested. Run it again.</div>}
      {replay && <div className="text-xs" style={{ color: replay.reproduced ? "var(--color-green)" : "var(--color-error)" }}>
        {replay.reproduced ? `Reproduced: same graph (${short(replay.replayed_snapshot_hash)}) and same exposures.` : `Not reproduced: graph ${replay.same_graph ? "same" : "changed"}, exposures ${replay.same_exposures ? "same" : "changed"}. ${replay.note || ""}`}</div>}
    </div>
    {prof && <div className={`${card} p-3 text-xs text-text-muted space-y-1`}>
      <div className="font-semibold text-sm text-text">Evidence behind this run</div>
      <div>{prof.links_used} links used: {Object.entries(prof.by_causal_basis).map(([k, v]) => `${v} ${k}`).join(", ") || "none"}. {prof.single_source_links_used} rest on a single source.</div>
      <div>Graph at the cutoff: {prof.links_in_graph} links, {prof.ungraded_links_in_graph} ungraded. Not used: {prof.weak_links_not_used} weak, {prof.unsupported_links_not_used} unsupported.</div>
    </div>}
    {r.unsupported_shocks.length > 0 && <div className={`${card} p-3 text-sm text-text-muted`}>{r.unsupported_shocks.map(u => <div key={u.entity_id}>{u.reason}</div>)}</div>}
    <Section title="Exposed entities" hint="Relative exposure along the strongest path. Each link shows what its sources support and how many sources there are.">
      {r.exposed_entities.length === 0 ? <Empty>No exposure through links that may carry a shock.</Empty> : r.exposed_entities.map(e => <div key={e.entity_id} className="px-3 py-2 border-b border-border">
        <div className="flex gap-2 items-center text-sm"><span className="flex-1">{e.entity} <span className="text-xs text-text-dim">{e.kind}</span></span>
          {e.relies_on_assumed_links && <Badge tone="warn">via assumption</Badge>}
          {e.relies_on_weak_links && <Badge tone="muted">via weak link</Badge>}
          {e.relies_on_single_source_links && <Badge tone="warn">single source</Badge>}
          <div className="w-24 h-2 rounded bg-bg-input"><div className="h-2 rounded" style={{ width: `${Math.round(e.impact * 100)}%`, background: "var(--accent)" }} /></div>
          <span className="tabular-nums text-xs w-10 text-right">{e.impact.toFixed(2)}</span></div>
        <ChainLine chain={e.chain} />
      </div>)}
    </Section>
    {r.exposed_questions.length > 0 && <Section title="Open questions touched" hint={r.questions_note || "Forecasts are not changed by scenarios."}>
      {r.exposed_questions.map(q => <button key={q.question_id} onClick={() => openQuestion(q.question_id)} className="block w-full text-left px-3 py-1.5 border-b border-border hover:bg-bg-hover text-sm">
        {q.question} <span className="text-xs text-text-dim">via {q.via_entity} · exposure {(q.exposure ?? q.impact ?? 0).toFixed(2)} · forecast unchanged {pct(q.current_forecast_unchanged ?? q.current_forecast)}</span></button>)}</Section>}
    {r.limitations && <Section title="Limitations"><ul className="px-5 py-2 text-xs text-text-muted list-disc space-y-1">{r.limitations.map((l, i) => <li key={i}>{l}</li>)}</ul></Section>}
    {rep && <ReportView rep={rep} />}
  </div>;
}

function ReportView({ rep }: { rep: Report }) {
  const maxChange = Math.max(0.001, ...rep.one_at_a_time.map(o => o.max_abs_impact_change));
  const topN = Math.min(5, rep.ranges.filter(r => r.base_impact > 0).length);
  return <div className="space-y-3">
    <div className="text-xs text-text-muted">Assumption and sensitivity report · {rep.draws} draws, seed {rep.seed}{rep.generated_at ? ` · ${when(rep.generated_at)}` : ""}{rep.graph_matches_run === false ? " · graph changed since the run" : ""}.
      {" "}{Object.entries(rep.classification_counts || {}).map(([k, v]) => `${v} ${k}`).join(", ")}</div>
    <Section title="Fragility of the top exposures" hint="Labels describe sensitivity to the model's own assumptions. None of them is validation against data.">
      {rep.fragility.length === 0 ? <Empty>No exposures.</Empty> : rep.fragility.map(f => <div key={f.entity_id} className="px-3 py-2 border-b border-border text-sm">
        <div className="flex gap-2 items-center"><span className="flex-1">{f.entity} <span className="text-xs text-text-dim">{f.base_impact.toFixed(2)}</span></span><Badge tone={classTone(f.classification)}>{f.classification}</Badge></div>
        <div className="text-xs text-text-muted">{f.why}{f.critical_links.length > 0 ? `. Depends on: ${f.critical_links.join("; ")}` : ""}</div>
      </div>)}
    </Section>
    <Section title="Ranges under varied assumptions" hint={rep.ranges_note}>
      {rep.ranges.length === 0 ? <Empty>No exposures.</Empty> : <div className="px-3 py-2 space-y-1">
        {rep.ranges.slice(0, 15).map(r => <div key={r.entity_id} className="flex gap-2 items-center text-xs">
          <span className="w-40 truncate">{r.entity}</span>
          <div className="flex-1 h-3 relative rounded bg-bg-input">
            <div className="absolute h-3 rounded" style={{ left: `${r.p5 * 100}%`, width: `${Math.max(1, (r.p95 - r.p5) * 100)}%`, background: "var(--accent)", opacity: 0.35 }} />
            <div className="absolute h-3" style={{ left: `${r.p50 * 100}%`, width: 2, background: "var(--accent)" }} />
            <div className="absolute h-3" style={{ left: `${r.base_impact * 100}%`, width: 2, background: "var(--text)" }} />
          </div>
          <span className="tabular-nums w-28 text-right text-text-muted">{r.p5.toFixed(2)}–{r.p95.toFixed(2)}</span>
          <span className="tabular-nums w-24 text-right text-text-dim">top 5 {pct(r.p_top5)}</span>
        </div>)}
        <div className="text-xs text-text-dim">Bar: 5th–95th percentile; thin mark: median; dark mark: the run's value.</div>
      </div>}
    </Section>
    <Section title="One assumption at a time" hint="Each assumption halved and raised by half (switches flipped). Sorted by the largest change in any exposure.">
      <div className="px-3 py-2 space-y-1">{rep.one_at_a_time.map(o => <div key={o.assumption} className="flex gap-2 items-center text-xs">
        <span className="w-56 truncate">{o.assumption}</span>
        <span className="w-28 text-text-dim tabular-nums">{String(o.base)} → {String(o.low)}{o.kind !== "switch" ? ` / ${String(o.high)}` : ""}</span>
        <div className="flex-1 h-2 rounded bg-bg-input"><div className="h-2 rounded" style={{ width: `${(o.max_abs_impact_change / maxChange) * 100}%`, background: "var(--color-warn)" }} /></div>
        <span className="tabular-nums w-12 text-right">{o.max_abs_impact_change.toFixed(3)}</span>
        <span className="w-32 text-right text-text-dim">{topN > 0 ? `top ${topN} kept ${Math.min(o.top5_overlap_low, o.top5_overlap_high)}/${topN}` : ""}</span>
      </div>)}</div>
    </Section>
    <Section title="Assumptions" hint="Every number the propagation uses. None was estimated from data.">
      <div className="px-3 py-2 text-xs space-y-0.5">{rep.assumptions.map((a, i) => <div key={i} className="flex gap-2"><span className={`flex-1 ${a.used ? "" : "text-text-dim"}`}>{a.assumption}</span><span className="text-text-dim">{a.source}{a.used ? "" : " · not used here"}</span></div>)}</div>
    </Section>
    {rep.historical_test && <HistorySummary h={rep.historical_test} />}
  </div>;
}

function HistorySummary({ h }: { h: History }) {
  if (h.status === "not run") return <div className={`${card} p-3 text-xs text-text-muted`}>Historical shock test: not run. {h.note}</div>;
  const g = h.groups || {};
  const row = (k: string) => { const x = g[k]; return x && x.n > 0 ? `${x.n} obs / ${x.events} events · after ${x.mean_abs_move_after?.toFixed(3)} · before ${x.mean_abs_move_before?.toFixed(3)} · moved ≥ threshold ${pct(x.share_moved_after)}` : "no observations"; };
  const tone = h.verdict?.status === "associated" ? "var(--color-green)" : h.verdict?.status === "insufficient" || h.verdict?.status === "confounded" || h.verdict?.status === "untestable" ? "var(--color-warn)" : "var(--text-muted)";
  return <Section title={`Historical shock test${h.id ? ` #${h.id}` : ""}`} hint={h.note}>
    <div className="px-3 py-2 text-xs space-y-1">
      <div style={{ color: tone }}><b>{h.verdict?.status}</b>: {h.verdict?.detail}</div>
      <div className="text-text-muted">{h.events_used} events, {h.observations} question observations, window {h.params?.window_hours}h{h.created_at ? ` · ${when(h.created_at)}` : ""}</div>
      <div><span className="text-text-dim w-20 inline-block">direct</span>{row("direct")}</div>
      <div><span className="text-text-dim w-20 inline-block">exposed</span>{row("exposed")}</div>
      <div><span className="text-text-dim w-20 inline-block">other</span>{row("other")}</div>
      <div className="text-text-muted">Exposed minus other, mean absolute move: after {fmtCI(h.exposed_vs_other?.after_minus_other)}; before (placebo) {fmtCI(h.exposed_vs_other?.before_minus_other)}</div>
    </div>
  </Section>;
}

function HistoryTest({ api, post, setError }: { api: Api; post: Post; setError: SetError }) {
  const [hours, setHours] = useState(24);
  const [running, setRunning] = useState(false);
  const tests = useLoad(() => api<{ tests: { id: number; created_at: string; report: History }[] }>("/scenario-history-tests", { limit: "1" }), [api], setError);
  const run = async () => {
    setRunning(true);
    try { await post("/scenario-history-test", { window_hours: hours }); tests.reload(); } catch (e) { setError((e as Error).message); } finally { setRunning(false); }
  };
  const latest = tests.data?.tests?.[0];
  return <div className="space-y-2">
    <div className={`${card} p-3 flex gap-2 items-center flex-wrap text-xs text-text-muted`}>
      <span className="font-semibold text-sm text-text flex-1">Historical shock test</span>
      <span>Replays past news events through the links known at the time and compares later market moves. Association, not causation.</span>
      <select className={input} value={hours} onChange={e => setHours(+e.target.value)}>{[6, 12, 24, 48].map(h => <option key={h} value={h}>{h}h window</option>)}</select>
      <button className={btn} onClick={run} disabled={running}>{running ? "Running…" : "Run test"}</button>
    </div>
    {latest ? <HistorySummary h={{ ...latest.report, id: latest.id, created_at: latest.created_at }} /> : <Empty>No historical test run yet.</Empty>}
  </div>;
}
