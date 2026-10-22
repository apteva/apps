import { useEffect, useState } from "react";

export interface CarrierBufferPolicy { enabled: boolean; min_ms: number; max_ms: number }
export interface CarrierBufferView {
 requested: CarrierBufferPolicy; effective?: CarrierBufferPolicy; state: string;
 verified_at?: string; resource_id: string; shared_connection: boolean; message?: string;
}
export type CarrierBufferRequest = (body: Record<string, unknown>) => Promise<{carrier_jitter_buffer?: CarrierBufferView}>;

export function carrierBufferInput(value: CarrierBufferPolicy): CarrierBufferPolicy {
 if (!Number.isInteger(value.min_ms) || !Number.isInteger(value.max_ms) || value.min_ms<40 || value.max_ms>400 || value.min_ms>value.max_ms)
  throw new Error("Choose 40–400 ms, with minimum no greater than maximum.");
 return {...value};
}
export default function CarrierJitterBufferControls({target,request}:{target:Record<string,unknown>;request:CarrierBufferRequest}) {
 const [view,setView]=useState<CarrierBufferView>();
 const [policy,setPolicy]=useState<CarrierBufferPolicy>({enabled:false,min_ms:60,max_ms:200});
 const [busy,setBusy]=useState(false),[error,setError]=useState("");
 const targetKey=JSON.stringify(target);
 useEffect(()=>{
  let active=true;setBusy(true);setView(undefined);setError("");
  request({...JSON.parse(targetKey),fresh:true}).then(r=>{if(active&&r.carrier_jitter_buffer){setView(r.carrier_jitter_buffer);setPolicy(r.carrier_jitter_buffer.requested)}})
   .catch(e=>{if(active)setError(e.message)}).finally(()=>{if(active)setBusy(false)});
  return ()=>{active=false};
 },[request,targetKey]);
 const save=async(apply:boolean)=>{
  setError("");setBusy(true);
  try {const r=await request({...target,carrier_jitter_buffer:carrierBufferInput(policy),apply});setView(r.carrier_jitter_buffer)}
  catch(e){setError((e as Error).message)}finally{setBusy(false)}
 };
 if(view?.state==="unsupported")return <p className="text-xs text-text-muted">This carrier does not expose optional jitter buffering.</p>;
 return <fieldset className="space-y-2 rounded border border-border p-3" disabled={busy||!view}>
  <legend className="px-1 text-sm font-medium">Carrier jitter buffer</legend>
  <p className="text-xs text-text-muted">Optional; off by default. Affects every number and route sharing this carrier connection. Adds buffering latency. Telephony’s local SIP recovery remains automatic.</p>
  <label className="flex gap-2 text-sm"><input type="checkbox" checked={policy.enabled} onChange={e=>setPolicy({...policy,enabled:e.target.checked})}/>Enable carrier jitter buffering</label>
  <div className="grid grid-cols-2 gap-2">{(["min_ms","max_ms"] as const).map(key=><label className="text-xs" key={key}>{key==="min_ms"?"Minimum (ms)":"Maximum (ms)"}<input type="number" min={40} max={400} step={1} className="mt-1 w-full rounded border border-border bg-bg p-2" value={policy[key]} onChange={e=>setPolicy({...policy,[key]:Number(e.target.value)})}/></label>)}</div>
  {view&&<p className="text-xs">Status: {view.state.replaceAll("_"," ")}. Last verified carrier value: {view.effective?`${view.effective.enabled?"on":"off"}, ${view.effective.min_ms}–${view.effective.max_ms} ms`:"unavailable"}{view.verified_at?` · ${new Date(view.verified_at).toLocaleString()}`:""}.</p>}
  {view?.message&&<p className="text-xs text-text-muted">{view.message}</p>}
  {error&&<p role="alert" className="text-sm text-error">{error}</p>}
  <div className="flex gap-2"><button type="button" className="rounded border border-border px-2 py-1 text-xs" onClick={()=>void save(false)}>Save policy</button><button type="button" className="rounded border border-border px-2 py-1 text-xs" onClick={()=>void save(true)}>Apply to idle connection</button></div>
 </fieldset>;
}
