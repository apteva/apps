import { useEffect, useState } from "react";
import type { ExecutionCost, VenueProfile } from "../api/execution.ts";
import { updateVenueProfile } from "../api/execution.ts";

export function ExecutionIntelligence({ profiles, costs, totals, onRefresh }: {
  profiles: VenueProfile[]; costs: ExecutionCost[]; totals: Record<string, number>; onRefresh: () => void;
}) {
  return <div className="h-full overflow-auto p-3 grid gap-3 lg:grid-cols-[minmax(0,1.25fr)_minmax(340px,.75fr)]">
    <section className="glass-inset rounded-xl overflow-hidden min-w-0">
      <div className="px-3 py-2 border-b border-[var(--border)]"><div className="text-xs font-semibold">Venue execution profiles</div><div className="text-[10px] t-tertiary">Effective fees, sessions, precision, minimums, funding, and outage policy</div></div>
      <div className="grid gap-2 p-2 md:grid-cols-2">{profiles.map(p => <Profile key={`${p.venue_slug}:${p.asset_class}:${p.symbol}`} profile={p} onRefresh={onRefresh} />)}</div>
    </section>
    <div className="space-y-3 min-w-0">
      <section className="glass-inset rounded-xl p-3">
        <div className="text-xs font-semibold mb-2">Execution cost attribution</div>
        <div className="grid grid-cols-2 gap-2">{["fee","spread","slippage","funding"].map(kind => <div key={kind} className="rounded-lg border border-[var(--border)] p-2"><div className="text-[10px] uppercase t-tertiary">{kind}</div><div className="mono text-sm">{(totals[kind] ?? 0).toFixed(4)}</div></div>)}</div>
      </section>
      <section className="glass-inset rounded-xl overflow-hidden">
        <div className="px-3 py-2 border-b border-[var(--border)] text-xs font-semibold">Cost ledger</div>
        <div className="max-h-[420px] overflow-auto">{costs.length === 0 ? <div className="p-6 text-center text-[11px] t-tertiary">Costs appear here as orders fill or funding events arrive.</div> : costs.map(c => <div key={c.id} className="px-3 py-2 border-b border-[var(--border)] text-[11px] flex justify-between gap-3"><div><div className="font-semibold">{c.symbol || "portfolio"} · {c.kind}</div><div className="t-tertiary">{c.venue_slug} · {c.liquidity_role || "n/a"} · {new Date(c.occurred_at).toLocaleString()}</div></div><div className="mono text-right">{c.amount.toFixed(6)}<div className="t-tertiary">{c.currency}</div></div></div>)}</div>
      </section>
    </div>
  </div>;
}

function Profile({profile,onRefresh}:{profile:VenueProfile;onRefresh:()=>void}) {
  const [draft,setDraft]=useState(profile); const [saving,setSaving]=useState(false); const [error,setError]=useState("");
  useEffect(()=>setDraft(profile),[profile]);
  const num=(key:keyof VenueProfile,label:string)=><label className="text-[10px] t-tertiary">{label}<input className="field mt-1 w-full mono" type="number" step="any" value={String(draft[key]??0)} onChange={e=>setDraft({...draft,[key]:Number(e.target.value)})}/></label>;
  return <div className="rounded-lg border border-[var(--border)] p-3">
    <div className="flex justify-between gap-2 mb-2"><div><strong className="text-xs">{draft.venue_slug}</strong><span className="text-[10px] t-tertiary"> · {draft.asset_class}</span></div><span className={`text-[10px] uppercase ${draft.status==="outage"||draft.runtime?.status==="outage"?"t-down":"t-up"}`}>{draft.status}/{draft.runtime?.status||"healthy"}</span></div>
    <div className="grid grid-cols-3 gap-2 mb-2"><select className="field text-[10px]" value={draft.status} onChange={e=>setDraft({...draft,status:e.target.value as VenueProfile["status"]})}>{["active","degraded","maintenance","outage"].map(v=><option key={v}>{v}</option>)}</select><select className="field text-[10px]" value={draft.session_policy} onChange={e=>setDraft({...draft,session_policy:e.target.value as VenueProfile["session_policy"]})}>{["continuous","regular_only","venue_managed"].map(v=><option key={v}>{v}</option>)}</select><select className="field text-[10px]" value={draft.spread_model} onChange={e=>setDraft({...draft,spread_model:e.target.value as VenueProfile["spread_model"]})}>{["quote","fixed_bps","none"].map(v=><option key={v}>{v}</option>)}</select></div>
    <div className="grid grid-cols-4 gap-2">{num("maker_fee_bps","Maker bp")}{num("taker_fee_bps","Taker bp")}{num("slippage_bps","Slip bp")}{num("fallback_spread_bps","Spread bp")}{num("min_qty","Min qty")}{num("min_notional","Min value")}{num("qty_step","Qty step")}{num("price_tick","Tick")}</div>
    {error&&<div className="text-[10px] t-down mt-2">{error}</div>}
    <button className="btn-primary w-full mt-2" disabled={saving} onClick={async()=>{setSaving(true);setError("");try{await updateVenueProfile(draft);onRefresh()}catch(e){setError(e instanceof Error?e.message:String(e))}finally{setSaving(false)}}}>{saving?"Saving…":"Save profile"}</button>
  </div>;
}
