import { useCallback, useEffect, useState } from "react";

type API = <T>(method: string, path: string, query?: Record<string,string>, body?: unknown) => Promise<T>;
type Case = { ordinal:number; fold:number; phase:string; candidate:number; start:string; end:string; shock:{name:string} };
type Result = { ordinal:number; run_id:number; selected_candidate:number; status:string; error?:string; summary?:{metrics?:Record<string,number>;agent_waiting?:boolean;processed_events?:number;input_events?:number} };
type Distribution = {count:number;mean:number;p05:number;p50:number;p95:number;worst:number};
type Group = {planned:number;completed:number;failed:number;return_pct:Distribution;drawdown_pct:Distribution;excess_return_pct:Distribution;loss_frequency?:number;drawdown_breach_frequency?:number;baseline_return_pct?:number;delta_return_pct?:Distribution;compounded_fold_return_pct?:number};
type Suite = {id:number;name:string;status:string;error?:string;plan_sha256:string;config:{mode:string;candidates:{name:string}[];max_agent_decisions:number};plan:Case[];cases:Result[];report:{total:number;completed:number;failed:number;groups:Record<string,Group>}};
const modes = [["out_of_sample","Out-of-sample"],["walk_forward","Walk-forward"],["robustness","Robustness"],["stress","Stress scenarios"],["monte_carlo","Monte Carlo"]] as const;
const input = "w-full mt-1 p-2 rounded border border-border bg-bg-input text-xs";
const button = "border border-border rounded px-2 py-1 text-xs disabled:opacity-50";
const pct = (n?:number) => n == null ? "—" : `${n.toFixed(2)}%`;

export function ValidationControls({sourceId,agent,api,setError}:{sourceId:number;agent:boolean;api:API;setError:(s:string|null)=>void}) {
 const [suites,setSuites]=useState<{id:number;name:string;status:string}[]>([]);
 const [selected,setSelected]=useState<number|null>(null);
 const [suite,setSuite]=useState<Suite|null>(null);
 const [mode,setMode]=useState("walk_forward");
 const [train,setTrain]=useState("");const [test,setTest]=useState("");
 const [seed,setSeed]=useState(42);const [samples,setSamples]=useState(100);
 const [expanding,setExpanding]=useState(false);const [budget,setBudget]=useState(1000);
 const [advanced,setAdvanced]=useState("{}");const [busy,setBusy]=useState(false);
 const [creating,setCreating]=useState(false);
 const load=useCallback(async()=>{const r=await api<{suites:{id:number;name:string;status:string}[]}>("GET","/validations",{source_backtest_id:String(sourceId)});setSuites(r.suites||[]);},[api,sourceId]);
 useEffect(()=>{let active=true;api<{suites:{id:number;name:string;status:string}[]}>("GET","/validations",{source_backtest_id:String(sourceId)}).then(r=>{if(active)setSuites(r.suites||[])}).catch(e=>{if(active)setError(e.message)});return()=>{active=false}},[sourceId,api]);
 useEffect(()=>{
  if(selected==null){setSuite(null);return}
  let active=true;let fetching=false;
  const poll=async()=>{if(fetching)return;fetching=true;try{const r=await api<{suite:Suite}>("GET",`/validations/${selected}`);if(active)setSuite(r.suite)}catch(e){if(active)setError((e as Error).message)}finally{fetching=false}};
  void poll();const timer=window.setInterval(poll,2000);return()=>{active=false;window.clearInterval(timer)};
 },[selected,api]);
 const create=async()=>{
  setBusy(true);try{
   const extra=JSON.parse(advanced);if(!extra||Array.isArray(extra)||typeof extra!=="object")throw new Error("Advanced configuration must be a JSON object");
   const config={...extra,mode,seed,expanding,max_agent_decisions:budget,...(train?{train_steps:Number(train)}:{}),...(test?{test_steps:Number(test)}:{}),...(mode==="monte_carlo"?{samples}:{})};
   const r=await api<{suite:Suite}>("POST","/validations",undefined,{source_backtest_id:sourceId,config});setSelected(r.suite.id);setSuite(r.suite);setCreating(false);await load();setError(null);
  }catch(e){setError((e as Error).message)}finally{setBusy(false)}
 };
 const control=async(action:string)=>{if(!suite)return;setBusy(true);try{const r=await api<{suite:Suite}>("POST",`/validations/${suite.id}/${action}`);setSuite(r.suite);await load();setError(null)}catch(e){setError((e as Error).message)}finally{setBusy(false)}};
 const download=async()=>{if(!suite)return;try{const r=await api<unknown>("GET",`/validations/${suite.id}/artifact`);const url=URL.createObjectURL(new Blob([JSON.stringify(r,null,2)],{type:"application/json"}));const a=document.createElement("a");a.href=url;a.download=`validation-${suite.id}.json`;a.click();window.setTimeout(()=>URL.revokeObjectURL(url),1000)}catch(e){setError((e as Error).message)}};
 return <section className="my-3 p-3 border border-border rounded bg-bg-card space-y-3">
  <div className="flex items-center gap-2"><strong className="text-sm">Validation</strong><button className={`${button} ml-auto`} onClick={()=>setCreating(!creating)}>New validation suite</button></div>
  <p className="text-xs text-text-dim">Compare windows and scenarios using this backtest’s captured dataset. Every child run has its own portfolio and reproducible artifact.</p>
  {creating&&<div className="space-y-3">
   <div className="grid grid-cols-2 gap-3">
    <label className="text-xs">Validation method<select aria-label="Validation method" value={mode} onChange={e=>setMode(e.target.value)} className={input}>{modes.map(([v,n])=><option key={v} value={v}>{n}</option>)}</select></label>
    <label className="text-xs">Seed<input aria-label="Validation seed" type="number" min="1" value={seed} onChange={e=>setSeed(Number(e.target.value))} className={input}/></label>
    {(mode==="walk_forward"||mode==="out_of_sample")&&<><label className="text-xs">Training market steps<input aria-label="Training market steps" type="number" min="2" placeholder="Automatic" value={train} onChange={e=>setTrain(e.target.value)} className={input}/></label><label className="text-xs">Test market steps<input aria-label="Test market steps" type="number" min="2" placeholder="Automatic" value={test} onChange={e=>setTest(e.target.value)} className={input}/></label><label className="text-xs flex items-center gap-2"><input type="checkbox" checked={expanding} onChange={e=>setExpanding(e.target.checked)}/>Expanding training window</label></>}
    {mode==="monte_carlo"&&<label className="text-xs">Sample runs per candidate<input aria-label="Monte Carlo samples" type="number" min="2" max="500" value={samples} onChange={e=>setSamples(Number(e.target.value))} className={input}/></label>}
    {agent&&<label className="text-xs">Agent decisions across the entire suite<input type="number" min="1" max="100000" value={budget} onChange={e=>setBudget(Number(e.target.value))} className={input}/></label>}
   </div>
   {(mode==="walk_forward"||mode==="out_of_sample")&&<p className="text-xs text-text-dim">A market step is one quote timestamp across symbols. With multiple candidates, selection uses only training returns; test windows never overlap. Warmup observes earlier data without trading.</p>}
   {mode==="monte_carlo"&&<p className="text-xs text-text-dim">Samples additional fees, slippage and latency uniformly within configured bounds. These are conditional execution-risk estimates on this historical market path, not probabilities of future market performance.</p>}
   {agent&&<p className="text-xs text-text-dim">Each window or scenario runs fresh agent decisions and can incur model costs. Memory resets between runs. Recorded child artifacts replay without model calls.</p>}
   <details><summary className="cursor-pointer text-xs">Candidates, stress scenarios and sampling bounds</summary><p className="my-2 text-xs text-text-dim">Optional JSON: candidates (name plus strategy definition or agent directive), parameter_grid for strategy parameters, scenarios, warmup_steps, step_steps, selection_metric, max_extra_fee_bps, max_extra_slippage_bps, max_latency_ms, loss_threshold_pct, drawdown_threshold_pct.</p><textarea aria-label="Advanced validation configuration" className={`${input} font-mono`} rows={6} value={advanced} onChange={e=>setAdvanced(e.target.value)}/></details>
   <button disabled={busy} onClick={create} className={`${button} bg-accent text-bg`}>Create queued suite</button>
  </div>}
  {suites.length>0&&<label className="text-xs">Validation suites<select aria-label="Validation suites" className={input} value={selected??""} onChange={e=>{setSuite(null);setSelected(Number(e.target.value)||null)}}><option value="">Select a suite</option>{suites.map(s=><option key={s.id} value={s.id}>{s.name} · {s.status}</option>)}</select></label>}
  {suite&&<>
   <div className="flex flex-wrap items-center gap-2 text-xs"><strong>{suite.status}</strong><span>{suite.report.completed}/{suite.report.total} runs completed · {suite.report.failed} failed</span><span className="flex-1"/>{["queued","paused","failed"].includes(suite.status)&&<button disabled={busy} onClick={()=>control("run")} className={button}>Run / resume suite</button>}{suite.status==="running"&&<button disabled={busy} onClick={()=>control("pause")} className={button}>Pause suite</button>}{!["completed","cancelled"].includes(suite.status)&&<button disabled={busy} onClick={()=>control("cancel")} className={button}>Cancel suite</button>}<button disabled={suite.status==="running"} onClick={download} className={button}>Download validation artifact</button></div>
   <progress aria-label="Validation progress" className="w-full" max={suite.report.total} value={suite.report.completed}/>
   {suite.error&&<p className="text-xs text-red">{suite.error}</p>}
   {Object.entries(suite.report.groups).map(([name,g])=><div key={name} className="border border-border p-2 rounded text-xs"><strong>{name} · {g.completed}/{g.planned} measured runs</strong><div className="grid grid-cols-3 gap-2 mt-2"><span>Mean return {g.return_pct.count?pct(g.return_pct.mean):"—"}</span><span>Return P05 / P50 / P95: {g.return_pct.count?`${pct(g.return_pct.p05)} / ${pct(g.return_pct.p50)} / ${pct(g.return_pct.p95)}`:"—"}</span><span>Worst drawdown {g.drawdown_pct.count?pct(g.drawdown_pct.worst):"—"}</span><span>Loss frequency {g.loss_frequency==null?"—":pct(g.loss_frequency*100)}</span><span>Drawdown breach frequency {g.drawdown_breach_frequency==null?"—":pct(g.drawdown_breach_frequency*100)}</span>{g.baseline_return_pct!=null&&<span>Baseline return {pct(g.baseline_return_pct)} · Mean difference {g.delta_return_pct?.count?pct(g.delta_return_pct.mean):"—"}</span>}{g.compounded_fold_return_pct!=null&&<span>Compounded fold return {pct(g.compounded_fold_return_pct)}</span>}</div></div>)}
   <p className="text-xs text-text-dim">Results reset capital and positions in each window. Compounded fold returns link independent test-period returns and exclude gaps; they are not a continuously held portfolio. Frequencies exclude unfinished/failed runs.</p>
   <div className="overflow-x-auto max-h-80"><table className="w-full text-xs"><thead><tr>{["Window / scenario","Candidate","Status","Return","Drawdown","Backtest"].map(h=><th key={h} className="text-left p-2">{h}</th>)}</tr></thead><tbody>{suite.cases.map(r=>{const c=suite.plan[r.ordinal];if(!c)return null;return <tr key={r.ordinal} className="border-t border-border"><td className="p-2" title={`${c.start} to ${c.end}`}>Fold {c.fold+1} · {c.phase} · {c.shock.name}</td><td>{suite.config.candidates[r.selected_candidate]?.name||"Selected after training"}</td><td title={r.error}>{r.summary?.agent_waiting?"Agent deciding":r.status}{r.status==="running"&&r.summary?.input_events?` · ${r.summary.processed_events??0}/${r.summary.input_events}`:""}</td><td>{pct(r.summary?.metrics?.return_pct)}</td><td>{pct(r.summary?.metrics?.max_drawdown_pct)}</td><td>{r.run_id||"—"}</td></tr>})}</tbody></table></div>
  </>}
 </section>;
}
