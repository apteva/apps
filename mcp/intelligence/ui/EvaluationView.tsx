// Evaluation tab: frozen historical datasets, backtest runs and their
// report, kept apart from prospective (live) results and the follow-up
// plan for questions that have not resolved yet.

import { useEffect, useState } from "react";
import { Api, Badge, btn, btnGhost, card, Empty, input, Post, SetError, Stat, useLoad, when, pct } from "./WorldTabs";

interface CI { mean: number | null; lo: number | null; hi: number | null }
interface Dataset { id: number; name: string; status: string; n_items: number; content_hash: string; skipped: Record<string, number>; error: string; created_at: string; fetched_at?: string }
interface RunRow { id: number; dataset_id: number; status: string; progress?: string; error?: string; params: Record<string, unknown>; code_version: string; created_at: string; finished_at?: string }
interface MethodEntry { status?: Record<string, number>; not_ok_reasons?: Record<string, number>; brier?: CI; log_loss?: CI; ece?: number; brier_minus_market?: CI; verdict_vs_market?: string }
interface Horizon { horizon_hours: number; n_questions: number; n_scored: number; n_clusters: number; yes_rate: number | null; methods: Record<string, MethodEntry>;
  blocks: ({ block: number; n: number; from: string; to: string } & Record<string, number | string>)[] }
interface AISection { verdict: string; horizon_hours?: number; knowledge_cutoff?: string; model?: string; status?: Record<string, number>; not_ok_reasons?: Record<string, number>;
  n_scored?: number; n_shifted?: number; brier_ai?: CI; brier_market?: CI; brier_ai_minus_market?: CI; log_loss_ai_minus_market?: CI;
  examples_shifted?: { question: string; cutoff: string; market: number; ai: number; outcome: number; rationale: string }[] }
interface Report {
  run?: { id: number; status: string; message: string }; backtest_note?: string;
  historical_backtest?: { code_version: string; dataset: { id: number; name: string; content_hash: string; n_items: number; fetched_at?: string; skipped: Record<string, number> };
    backtest: { horizons: Horizon[] }; ai: AISection; leakage_controls: string[]; limitations: string[] };
  prospective_live: { as_of: string; n_resolved: number; n_scored: number; n_ai_adjusted: number; unscored_no_forecast_before_outcome: number; unscored_no_market_price: number; rule: string;
    brier_model?: CI; brier_market?: CI; brier_model_minus_market?: CI; verdict: string };
  follow_up: { open_questions: number; with_locked_forecast: number; earliest_n30: string; earliest_n100: string; steps: string[];
    questions: { question_id: number; question: string; venue: string; deadline: string; score_after: string; forecast: number | null; market_at_forecast?: number | null; locked?: boolean }[] };
  separation: string;
}

const METHODS = ["uniform", "base_rate", "market", "market_recal"];
const n3 = (v?: number | null) => (v == null ? "—" : v.toFixed(3));
const ciText = (c?: CI) => (!c || c.mean == null ? "—" : c.lo == null || c.hi == null ? `${n3(c.mean)} (no interval)` : `${n3(c.mean)} [${n3(c.lo)}, ${n3(c.hi)}]`);
const isDate = (s: string) => /^\d{4}-\d{2}-\d{2}T/.test(s);
const verdictTone = (v?: string): "good" | "bad" | "warn" | "muted" =>
  !v ? "muted" : v.startsWith("better") ? "good" : v.startsWith("worse") ? "bad" : v.startsWith("insufficient") || v.startsWith("no interval") || v.startsWith("not") ? "warn" : "muted";

function Counts({ m }: { m?: Record<string, number> }) {
  const e = Object.entries(m || {});
  if (e.length === 0) return <span className="text-text-dim">—</span>;
  return <span>{e.map(([k, v]) => `${k} ${v}`).join(" · ")}</span>;
}

export function EvaluationTab({ api, post, setError }: { api: Api; post: Post; setError: SetError }) {
  const ds = useLoad(() => api<{ datasets: Dataset[] }>("/eval-datasets"), [api], setError);
  const runs = useLoad(() => api<{ runs: RunRow[] }>("/eval-runs"), [api], setError);
  const [runId, setRunId] = useState<number | null>(null);
  const rep = useLoad(() => api<Report>("/eval-report", runId ? { run_id: String(runId) } : {}), [api, runId], setError);
  const [days, setDays] = useState("30");
  const [perVenue, setPerVenue] = useState("150");
  const [datasetId, setDatasetId] = useState("");
  const [horizons, setHorizons] = useState("24,168");
  const [ai, setAI] = useState(false);
  const [knowledge, setKnowledge] = useState("");
  const [aiMax, setAIMax] = useState("30");
  const [busy, setBusy] = useState(false);

  const active = (ds.data?.datasets || []).some(d => d.status === "building") || (runs.data?.runs || []).some(r => r.status === "running");
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => { ds.reload(); runs.reload(); }, 5000);
    return () => clearInterval(t);
  }, [active, ds.reload, runs.reload]);
  useEffect(() => {
    const ready = (ds.data?.datasets || []).find(d => d.status === "ready");
    if (!datasetId && ready) setDatasetId(String(ready.id));
  }, [ds.data, datasetId]);

  const build = async () => {
    setBusy(true);
    try { await post("/eval-dataset-build", { since_days: Number(days), max_per_venue: Number(perVenue) }); ds.reload(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  const run = async () => {
    setBusy(true);
    try {
      const body: Record<string, unknown> = { dataset_id: Number(datasetId), horizons_hours: horizons, ai };
      if (ai) { body.ai_knowledge_cutoff = knowledge; body.ai_max_items = Number(aiMax); }
      const r = await post<{ run_id: number }>("/eval-run", body);
      setRunId(r.run_id); runs.reload();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };

  const r = rep.data;
  const bt = r?.historical_backtest;
  return <div className="space-y-4">
    <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(320px,1fr))" }}>
      <section className={card}>
        <div className="px-3 py-2 border-b border-border flex items-center"><span className="font-semibold text-sm flex-1">Datasets</span><button className={btnGhost} onClick={ds.reload} disabled={ds.busy}>Refresh</button></div>
        <div className="px-3 py-2 flex flex-wrap gap-2 items-center border-b border-border text-xs text-text-muted">
          Resolved in the last <input className={input} style={{ width: 56 }} value={days} onChange={e => setDays(e.target.value)} /> days, up to
          <input className={input} style={{ width: 64 }} value={perVenue} onChange={e => setPerVenue(e.target.value)} /> per venue
          <button className={btn} onClick={build} disabled={busy || active}>Build</button>
        </div>
        {(ds.data?.datasets || []).length === 0 ? <Empty>No dataset yet. Building one freezes resolved Manifold and Kalshi questions with their trade history.</Empty> :
          <table className="w-full text-sm"><tbody>{(ds.data?.datasets || []).map(d => <tr key={d.id} className="border-b border-border" title={Object.entries(d.skipped || {}).map(([k, v]) => `${k}: ${v}`).join("\n")}>
            <td className="px-3 py-1.5">#{d.id} {d.name}</td><td className="px-2"><Badge tone={d.status === "ready" ? "good" : d.status === "failed" ? "bad" : "accent"}>{d.status}</Badge></td>
            <td className="px-2 tabular-nums">{d.n_items} items</td><td className="px-2 text-xs text-text-dim font-mono">{d.content_hash.slice(0, 12)}</td>
            <td className="px-2 text-xs" style={{ color: d.error ? "var(--color-error)" : undefined }}>{d.error || when(d.fetched_at || d.created_at)}</td></tr>)}</tbody></table>}
      </section>
      <section className={card}>
        <div className="px-3 py-2 border-b border-border flex items-center"><span className="font-semibold text-sm flex-1">Runs</span><button className={btnGhost} onClick={runs.reload} disabled={runs.busy}>Refresh</button></div>
        <div className="px-3 py-2 flex flex-wrap gap-2 items-center border-b border-border text-xs text-text-muted">
          Dataset <select className={input} value={datasetId} onChange={e => setDatasetId(e.target.value)}>
            {(ds.data?.datasets || []).filter(d => d.status === "ready").map(d => <option key={d.id} value={d.id}>#{d.id}</option>)}</select>
          horizons (h) <input className={input} style={{ width: 72 }} value={horizons} onChange={e => setHorizons(e.target.value)} />
          <label className="flex items-center gap-1"><input type="checkbox" checked={ai} onChange={e => setAI(e.target.checked)} /> AI</label>
          {ai && <>knowledge cutoff <input className={input} style={{ width: 110 }} placeholder="YYYY-MM-DD" value={knowledge} onChange={e => setKnowledge(e.target.value)} />
            max <input className={input} style={{ width: 48 }} value={aiMax} onChange={e => setAIMax(e.target.value)} /></>}
          <button className={btn} onClick={run} disabled={busy || active || !datasetId || (ai && !knowledge)}>Run</button>
        </div>
        {(runs.data?.runs || []).length === 0 ? <Empty>No runs yet.</Empty> :
          <table className="w-full text-sm"><tbody>{(runs.data?.runs || []).map(x => <tr key={x.id} className={`border-b border-border cursor-pointer hover:bg-bg-hover ${r?.run?.id === x.id ? "bg-bg-hover" : ""}`} onClick={() => setRunId(x.id)} title={x.code_version}>
            <td className="px-3 py-1.5">#{x.id}</td><td className="px-2 text-text-muted">dataset #{x.dataset_id}{x.params?.ai ? " · AI" : ""}</td>
            <td className="px-2"><Badge tone={x.status === "done" ? "good" : x.status === "failed" ? "bad" : "accent"}>{x.status}</Badge></td>
            <td className="px-2 text-xs" style={{ color: x.error ? "var(--color-error)" : undefined }}>{x.progress || x.error || when(x.finished_at || x.created_at)}</td></tr>)}</tbody></table>}
      </section>
    </div>

    {!r ? <Empty>{rep.busy ? "Loading…" : "No report."}</Empty> : <>
      <div className={`${card} p-3 text-xs text-text-muted`}>{r.separation}</div>

      <section className={card}>
        <div className="px-3 py-2 border-b border-border flex items-center gap-2"><span className="font-semibold text-sm flex-1">Historical backtest{r.run ? ` · run #${r.run.id}` : ""}</span>
          {bt && <span className="text-xs text-text-dim">{bt.code_version}</span>}</div>
        {!bt ? <Empty>{r.backtest_note || (r.run?.status === "failed" ? `Run failed: ${r.run.message}` : "Run still in progress.")}</Empty> : <div className="p-3 space-y-4">
          <div className="text-xs text-text-muted">Dataset #{bt.dataset.id} {bt.dataset.name} · {bt.dataset.n_items} questions · hash <span className="font-mono">{bt.dataset.content_hash.slice(0, 16)}</span> · fetched {when(bt.dataset.fetched_at)}
            {Object.keys(bt.dataset.skipped || {}).length > 0 && <> · skipped: <Counts m={bt.dataset.skipped} /></>}</div>
          {bt.backtest.horizons.map(h => <div key={h.horizon_hours} className="space-y-2">
            <div className="text-sm font-semibold">{h.horizon_hours}h before close <span className="font-normal text-xs text-text-muted">· scored {h.n_scored} of {h.n_questions} · {h.n_clusters} clusters · YES rate {pct(h.yes_rate)}</span></div>
            <table className="w-full text-sm"><thead><tr className="text-left text-xs text-text-dim border-b border-border"><th className="py-1">Method</th><th className="px-2">Brier [95% CI]</th><th className="px-2">Log loss</th><th className="px-2 text-right">ECE</th><th className="px-2">Brier − market</th><th className="px-2">Coverage</th></tr></thead>
              <tbody>{METHODS.map(m => { const e = h.methods[m] || {}; return <tr key={m} className="border-b border-border">
                <td className="py-1.5 font-medium">{m}</td><td className="px-2 tabular-nums">{ciText(e.brier)}</td><td className="px-2 tabular-nums">{ciText(e.log_loss)}</td><td className="px-2 text-right tabular-nums">{n3(e.ece)}</td>
                <td className="px-2 tabular-nums">{e.brier_minus_market ? <span title={e.verdict_vs_market}>{ciText(e.brier_minus_market)} <Badge tone={verdictTone(e.verdict_vs_market)}>{e.verdict_vs_market?.split(" (")[0]}</Badge></span> : "baseline"}</td>
                <td className="px-2 text-xs text-text-muted" title={Object.entries(e.not_ok_reasons || {}).map(([k, v]) => `${k}: ${v}`).join("\n")}><Counts m={e.status} /></td></tr>; })}</tbody></table>
            {h.blocks.length > 1 && <table className="w-full text-xs"><thead><tr className="text-left text-text-dim border-b border-border"><th className="py-1">Block</th><th className="px-2">Forecast times</th><th className="px-2 text-right">n</th>{METHODS.map(m => <th key={m} className="px-2 text-right">{m}</th>)}</tr></thead>
              <tbody>{h.blocks.map(b => <tr key={b.block} className="border-b border-border"><td className="py-1">{b.block}</td><td className="px-2 text-text-muted">{when(b.from)} – {when(b.to)}</td><td className="px-2 text-right tabular-nums">{b.n}</td>
                {METHODS.map(m => <td key={m} className="px-2 text-right tabular-nums">{n3(b["brier_" + m] as number)}</td>)}</tr>)}</tbody></table>}
          </div>)}
          <AIBlock ai={bt.ai} />
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(280px,1fr))" }}>
            <div><div className="text-xs uppercase tracking-wide text-text-dim mb-1">Leakage controls</div><ul className="text-xs text-text-muted list-disc pl-4 space-y-1">{bt.leakage_controls.map(c => <li key={c}>{c}</li>)}</ul></div>
            <div><div className="text-xs uppercase tracking-wide text-text-dim mb-1">Limitations</div><ul className="text-xs text-text-muted list-disc pl-4 space-y-1">{bt.limitations.map(c => <li key={c}>{c}</li>)}</ul></div>
          </div>
        </div>}
      </section>

      <section className={card}>
        <div className="px-3 py-2 border-b border-border font-semibold text-sm">Prospective (live forecasts recorded before outcomes)</div>
        <div className="p-3 space-y-3">
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(150px,1fr))" }}>
            <Stat label="Scored" value={String(r.prospective_live.n_scored)} hint={`${r.prospective_live.n_resolved} resolved · ${r.prospective_live.n_ai_adjusted} AI-adjusted`} />
            <Stat label="Brier: model" value={ciText(r.prospective_live.brier_model)} />
            <Stat label="Brier: market" value={ciText(r.prospective_live.brier_market)} />
            <Stat label="Model − market" value={ciText(r.prospective_live.brier_model_minus_market)} hint="< 0 beats market" />
          </div>
          <div className="flex gap-2 items-center text-sm"><Badge tone={verdictTone(r.prospective_live.verdict)}>verdict</Badge>{r.prospective_live.verdict}</div>
          <div className="text-xs text-text-muted">{r.prospective_live.rule} Unscored: {r.prospective_live.unscored_no_forecast_before_outcome} without a forecast before the outcome, {r.prospective_live.unscored_no_market_price} without a market price.</div>
        </div>
      </section>

      <section className={card}>
        <div className="px-3 py-2 border-b border-border font-semibold text-sm">Follow-up plan</div>
        <div className="p-3 space-y-3">
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(150px,1fr))" }}>
            <Stat label="Open questions" value={String(r.follow_up.open_questions)} hint={`${r.follow_up.with_locked_forecast} with a locked forecast`} />
            <Stat label="Earliest n = 30" value={isDate(r.follow_up.earliest_n30) ? when(r.follow_up.earliest_n30) : "—"} hint={isDate(r.follow_up.earliest_n30) ? "first possible verdict" : r.follow_up.earliest_n30} />
            <Stat label="Earliest n = 100" value={isDate(r.follow_up.earliest_n100) ? when(r.follow_up.earliest_n100) : "—"} hint={isDate(r.follow_up.earliest_n100) ? "stable verdict" : r.follow_up.earliest_n100} />
          </div>
          <ol className="text-xs text-text-muted list-decimal pl-4 space-y-1">{r.follow_up.steps.map(s => <li key={s}>{s}</li>)}</ol>
          {r.follow_up.questions.length > 0 && <table className="w-full text-sm"><thead><tr className="text-left text-xs text-text-dim border-b border-border"><th className="py-1">Question</th><th className="px-2">Deadline</th><th className="px-2">Score after</th><th className="px-2 text-right">Forecast</th><th className="px-2 text-right">Market</th><th className="px-2">Locked</th></tr></thead>
            <tbody>{r.follow_up.questions.slice(0, 50).map(q => <tr key={q.question_id} className="border-b border-border">
              <td className="py-1.5">{q.question}</td><td className="px-2 text-text-muted">{when(q.deadline)}</td><td className="px-2 text-text-muted">{when(q.score_after)}</td>
              <td className="px-2 text-right tabular-nums">{pct(q.forecast)}</td><td className="px-2 text-right tabular-nums">{pct(q.market_at_forecast)}</td>
              <td className="px-2">{q.locked ? <Badge tone="good">yes</Badge> : <span className="text-text-dim">no</span>}</td></tr>)}</tbody></table>}
        </div>
      </section>
    </>}
  </div>;
}

function AIBlock({ ai }: { ai: AISection }) {
  return <div className="space-y-2">
    <div className="text-sm font-semibold">AI-assisted (market + capped evidence shift){ai.horizon_hours ? ` · ${ai.horizon_hours}h` : ""}
      {ai.model && <span className="font-normal text-xs text-text-muted"> · {ai.model} · knowledge cutoff {ai.knowledge_cutoff}</span>}</div>
    <div className="flex gap-2 items-center text-sm"><Badge tone={verdictTone(ai.verdict)}>verdict</Badge>{ai.verdict}</div>
    {ai.n_scored != null && <>
      <div className="text-xs text-text-muted">Scored {ai.n_scored} · shifted {ai.n_shifted} · <Counts m={ai.status} />
        {Object.keys(ai.not_ok_reasons || {}).length > 0 && <> · not scored: {Object.entries(ai.not_ok_reasons || {}).map(([k, v]) => `${k} (${v})`).join("; ")}</>}</div>
      <table className="text-sm"><tbody>
        <tr><td className="pr-4 text-text-muted">Brier AI</td><td className="tabular-nums">{ciText(ai.brier_ai)}</td></tr>
        <tr><td className="pr-4 text-text-muted">Brier market (same questions)</td><td className="tabular-nums">{ciText(ai.brier_market)}</td></tr>
        <tr><td className="pr-4 text-text-muted">Brier AI − market</td><td className="tabular-nums">{ciText(ai.brier_ai_minus_market)}</td></tr>
        <tr><td className="pr-4 text-text-muted">Log loss AI − market</td><td className="tabular-nums">{ciText(ai.log_loss_ai_minus_market)}</td></tr>
      </tbody></table>
      {(ai.examples_shifted || []).length > 0 && <table className="w-full text-xs"><thead><tr className="text-left text-text-dim border-b border-border"><th className="py-1">Question</th><th className="px-2 text-right">Market</th><th className="px-2 text-right">AI</th><th className="px-2">Outcome</th><th className="px-2">Rationale</th></tr></thead>
        <tbody>{(ai.examples_shifted || []).map((x, i) => <tr key={i} className="border-b border-border"><td className="py-1">{x.question}</td><td className="px-2 text-right tabular-nums">{pct(x.market)}</td><td className="px-2 text-right tabular-nums">{pct(x.ai)}</td>
          <td className="px-2"><Badge tone={x.outcome ? "good" : "bad"}>{x.outcome ? "YES" : "NO"}</Badge></td><td className="px-2 text-text-muted">{x.rationale}</td></tr>)}</tbody></table>}
    </>}
  </div>;
}
