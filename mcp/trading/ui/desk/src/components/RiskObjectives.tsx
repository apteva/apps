import { useEffect, useState } from "react";
import { archiveObjective, createObjective, updateRisk, updateUniversePolicy } from "../api/risk.ts";
import type { PortfolioObjective, PortfolioUniversePolicy, RiskPolicy, RiskState } from "../api/types.ts";

export function RiskObjectives({ portfolioId, policy: inputPolicy, state, objectives, universe: inputUniverse, allowedClasses, onRefresh }: {
  portfolioId: number; policy?: RiskPolicy; state?: RiskState; objectives: PortfolioObjective[];
  universe?: PortfolioUniversePolicy; allowedClasses: string[]; onRefresh: () => void;
}) {
  const [policy, setPolicy] = useState<RiskPolicy | undefined>(inputPolicy);
  const [name, setName] = useState("Growth target");
  const [target, setTarget] = useState(10);
  const [metric, setMetric] = useState("period_return_pct");
  const [universe, setUniverse] = useState<PortfolioUniversePolicy | undefined>(inputUniverse);
  const [includes, setIncludes] = useState("");
  const [excludes, setExcludes] = useState("");
  useEffect(() => setPolicy(inputPolicy), [inputPolicy]);
  useEffect(() => { setUniverse(inputUniverse); setIncludes((inputUniverse?.include_symbols ?? []).join(", ")); setExcludes((inputUniverse?.exclude_symbols ?? []).join(", ")); }, [inputUniverse]);
  const savePreset = async (risk_level: RiskPolicy["risk_level"]) => { await updateRisk(portfolioId, { risk_level }); onRefresh(); };
  const saveCustom = async () => { if (policy) { await updateRisk(portfolioId, { ...policy, risk_level: "custom" }); onRefresh(); } };
  const saveUniverse = async () => { if (universe) { await updateUniversePolicy(portfolioId, { ...universe, include_symbols: splitSymbols(includes), exclude_symbols: splitSymbols(excludes) }); onRefresh(); } };
  return <div className="grid lg:grid-cols-2 gap-3 p-2">
    <div className="glass rounded-xl p-4">
      <div className="text-xs uppercase tracking-wider t-tertiary mb-3">Enforced risk policy</div>
      <div className="flex gap-2 mb-4">{(["conservative", "balanced", "aggressive"] as const).map((level) => <button key={level} onClick={() => savePreset(level)} className={`tab capitalize ${policy?.risk_level === level ? "active" : ""}`}>{level}</button>)}</div>
      {policy && <><div className="grid grid-cols-2 gap-3">{([ ["Daily loss", "max_daily_loss_pct"], ["Max drawdown", "max_drawdown_pct"], ["Position", "max_position_pct"], ["Gross exposure", "max_gross_exposure_pct"], ["Order", "max_order_pct"] ] as const).map(([label, key]) => <label key={key} className="text-[11px] t-secondary">{label} %<input className="field mt-1 w-full mono" type="number" min="0.01" step="0.25" value={policy[key]} onChange={(e) => setPolicy({ ...policy, [key]: Number(e.target.value), risk_level: "custom" })}/></label>)}</div>
      <button className="btn-primary mt-3" onClick={saveCustom}>Save custom limits</button></>}
      <div className="grid grid-cols-2 gap-3 mt-4 border-t border-[var(--border)] pt-3"><div><div className="text-[10px] t-tertiary">HIGH WATER</div><div className="mono">${(state?.high_water_equity ?? 0).toLocaleString()}</div></div><div><div className="text-[10px] t-tertiary">DRAWDOWN</div><div className="mono t-down">{(state?.current_drawdown_pct ?? 0).toFixed(2)}%</div></div></div>
      <p className="text-[11px] t-tertiary mt-3">One gate for manual, agent, strategy, simulation, Alpaca paper, and armed live orders.</p>
    </div>
    <div className="glass rounded-xl p-4">
      <div className="text-xs uppercase tracking-wider t-tertiary mb-3">Trading objectives</div>
      <div className="grid grid-cols-2 gap-2 mb-3"><input className="field" value={name} onChange={(e) => setName(e.target.value)}/><input className="field mono" type="number" value={target} onChange={(e) => setTarget(Number(e.target.value))}/><select className="field col-span-2" value={metric} onChange={(e) => setMetric(e.target.value)}><option value="period_return_pct">Return from now</option><option value="total_return_pct">Total return</option><option value="day_return_pct">Day return</option><option value="drawdown_pct">Drawdown floor</option></select><button className="btn-primary col-span-2" onClick={async () => { await createObjective(portfolioId, { name, metric, target_pct: target, direction: metric === "drawdown_pct" ? "at_most" : "at_least" }); onRefresh(); }}>Create objective</button></div>
      <div className="space-y-2">{objectives.length === 0 ? <div className="text-xs t-tertiary py-6 text-center">No percentage objectives yet.</div> : objectives.map((o) => <div key={o.id} className="rounded-lg border border-[var(--border)] p-3"><div className="flex justify-between text-xs"><strong>{o.name}</strong><span className={o.achieved ? "t-up" : "t-secondary"}>{o.achieved ? "achieved" : o.period_state}</span></div><div className="text-[11px] t-tertiary mt-1 mono">{o.actual_pct?.toFixed(2) ?? "—"}% / {o.target_pct.toFixed(2)}%</div><div className="h-1 rounded bg-[var(--surface-2)] mt-2 overflow-hidden"><div className="h-full bg-[var(--accent)]" style={{ width: `${Math.max(0, Math.min(100, o.progress_pct ?? 0))}%` }}/></div><button className="text-[10px] t-tertiary mt-2" onClick={async () => { await archiveObjective(portfolioId, o.id); onRefresh(); }}>Archive</button></div>)}</div>
    </div>
    <div className="glass rounded-xl p-4 lg:col-span-2">
      <div className="text-xs uppercase tracking-wider t-tertiary mb-3">Tradable universe · hard order gate</div>
      {universe && <div className="grid md:grid-cols-3 gap-3">
        <label className="text-[11px] t-secondary">Selection mode<select className="field mt-1 w-full" value={universe.selection_mode} onChange={(e) => setUniverse({ ...universe, selection_mode: e.target.value as PortfolioUniversePolicy["selection_mode"] })}><option value="all_allowed_classes">All allowed classes</option><option value="symbol_allowlist">Symbol allowlist</option><option value="reference_universe">Reference universe</option></select></label>
        <label className="text-[11px] t-secondary">Included symbols<input className="field mt-1 w-full mono" value={includes} onChange={(e) => setIncludes(e.target.value)} placeholder="SPY, QQQ" /></label>
        <label className="text-[11px] t-secondary">Excluded symbols<input className="field mt-1 w-full mono" value={excludes} onChange={(e) => setExcludes(e.target.value)} placeholder="GME" /></label>
        <label className="text-[11px] t-secondary">Reference universe<input className="field mt-1 w-full mono" value={universe.reference_universe_id ?? ""} onChange={(e) => setUniverse({ ...universe, reference_universe_id: e.target.value })} placeholder="alpaca-active-assets" /></label>
        <label className="text-[11px] t-secondary flex items-center gap-2 mt-5"><input type="checkbox" checked={universe.require_active_listing} onChange={(e) => setUniverse({ ...universe, require_active_listing: e.target.checked })} /> Require active listing</label>
        <label className="text-[11px] t-secondary flex items-center gap-2 mt-5"><input type="checkbox" checked={universe.enforcement_enabled} onChange={(e) => setUniverse({ ...universe, enforcement_enabled: e.target.checked })} /> Enforce on every order</label>
        <div className="md:col-span-3 flex items-center gap-3"><span className="text-[11px] t-tertiary">Asset classes: {allowedClasses.join(", ")}</span><button className="btn-primary ml-auto" onClick={saveUniverse}>Save universe policy</button></div>
      </div>}
    </div>
  </div>;
}

function splitSymbols(value: string) { return [...new Set(value.split(/[\s,]+/).map((v) => v.trim().toUpperCase()).filter(Boolean))]; }
