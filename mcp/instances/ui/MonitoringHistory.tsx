import { useEffect, useState, useRef } from "react";

export interface MonitoringStatus {
 enabled: boolean; state: string; version?: string; error?: string;
 last_seen?: number; budget_bytes?: number; storage_bytes?: number; detail_evicted?: number; budget_evictions?: number;
}
export interface Aggregate {sum:number; observed_ms:number; min:number; max:number; peak_at:number}
export interface HistoryPoint {time:number;step_ms:number;observed_ms:number;values:Record<string,Aggregate>;above_80_ms?:number;above_95_ms?:number}
interface History {points:HistoryPoint[];resolution:string;from:number;to:number;oldest?:number;monitoring:MonitoringStatus}
interface Recording {time:number;cpu:number;core:number;iowait?:number;memory?:number;interval_ms:number}
interface Incident {id:string;updated?:number;start:number;end?:number;peak:number;peak_at:number;core_peak:number;reason:string;detail_expired?:boolean;recordings?:Recording[];processes?:Array<{pid:number;name:string;cpu_pct:number;memory_bytes:number}>}
const API="/api/apps/instances/api";
const ranges=[{name:"5 min",ms:300000},{name:"1 hour",ms:3600000},{name:"24 hours",ms:86400000},{name:"7 days",ms:604800000},{name:"30 days",ms:2592000000},{name:"90 days",ms:7776000000},{name:"1 year",ms:31536000000}];
export function metricAverage(point:HistoryPoint,key:string):number|undefined {
 const v=point.values[key];if(!v)return undefined;const observed=v.observed_ms||point.observed_ms;return observed>0?v.sum/observed:undefined;
}
// Paths break on missing or poorly-covered buckets; disconnected hosts never
// turn into a convincing flat zero line or a bridge across an outage.
export function chartPath(points:HistoryPoint[],key:string,peak:boolean,from:number,to:number,max=100):string {
 let path="",previous:HistoryPoint|undefined;
 for(const p of points){const value=peak?p.values[key]?.max:metricAverage(p,key);if(value===undefined||p.observed_ms<=0){previous=undefined;continue;}
 const x=20+Math.max(0,Math.min(1,(p.time-from)/(to-from)))*760;
 const y=110-Math.max(0,Math.min(max,value))/max*100;const connected=previous&&p.time-previous.time<=p.step_ms*1.5&&previous.observed_ms>=previous.step_ms*0.8&&p.observed_ms>=p.step_ms*0.8;
 path+=`${connected?" L":" M"}${x.toFixed(2)},${y.toFixed(2)}`;previous=p;
 }return path.trim();
}
function IOWaitChart({points,from,to}:{points:HistoryPoint[];from:number;to:number}) {
 const observed=points.filter(p=>p.observed_ms>0&&p.values.iowait);
 const peak=observed.reduce((best,p)=>p.values.iowait.max>(best?.max??-1)?p.values.iowait:best,undefined as Aggregate|undefined);
 const duration=observed.reduce((sum,p)=>sum+(p.values.iowait.observed_ms||p.observed_ms),0);
 const average=duration>0?observed.reduce((sum,p)=>sum+p.values.iowait.sum,0)/duration:undefined;
 const max=Math.max(5,Math.min(100,Math.ceil((peak?.max||0)/5)*5));
 return <div className="space-y-1" aria-label="I/O wait history">
  <p className="text-text">I/O wait history</p>
  {peak&&average!==undefined?<>
   <svg viewBox="0 0 800 135" className="w-full h-40" role="img" aria-label="I/O wait average and recorded peaks over time">
    {[0,max/2,max].map(v=><g key={v}><line x1="20" x2="780" y1={110-v/max*100} y2={110-v/max*100} stroke="currentColor" opacity="0.1"/><text x="1" y={113-v/max*100} fontSize="9" fill="currentColor">{v}%</text></g>)}
    <path d={chartPath(points,"iowait",false,from,to,max)} fill="none" stroke="#22d3ee" strokeWidth="2"/>
    <path d={chartPath(points,"iowait",true,from,to,max)} fill="none" stroke="#fb923c" strokeWidth="1.5"/>
    {observed.map(p=><circle key={p.time} cx={20+Math.max(0,Math.min(1,(p.time-from)/(to-from)))*760} cy={110-Math.max(0,Math.min(max,p.values.iowait.max))/max*100} r={p.values.iowait.max>0?2:1} fill="#fb923c"><title>{`${p.values.iowait.max.toFixed(2)}% I/O wait peak at ${new Date(p.values.iowait.peak_at).toLocaleString()} · bucket average ${metricAverage(p,"iowait")?.toFixed(2)}%`}</title></circle>)}
    <text x="20" y="130" fontSize="9" fill="currentColor">{new Date(from).toLocaleString()}</text><text x="780" y="130" textAnchor="end" fontSize="9" fill="currentColor">{new Date(to).toLocaleString()}</text>
   </svg>
   <div className="flex flex-wrap gap-3 text-[10px]"><span style={{color:"#22d3ee"}}>I/O wait average</span><span style={{color:"#fb923c"}}>I/O wait peak</span></div>
   <p>Average {average.toFixed(2)}% · Peak {peak.max.toFixed(2)}% at {new Date(peak.peak_at).toLocaleString()}</p>
   <p className="text-[10px]">Sampled every 250 ms · chart scale 0–{max}% · gaps indicate missing observations</p>
  </>:<p>No I/O wait observations in this range.</p>}
 </div>;
}
function MonitoringChart({points,from,to}:{points:HistoryPoint[];from:number;to:number}) {
 return <svg viewBox="0 0 800 135" className="w-full h-40" role="img" aria-label="CPU average and recorded peaks, busiest core peaks, and memory history">
  {[0,50,100].map(v=><g key={v}><line x1="20" x2="780" y1={110-v} y2={110-v} stroke="currentColor" opacity="0.1"/><text x="1" y={113-v} fontSize="9" fill="currentColor">{v}</text></g>)}
  <path d={chartPath(points,"cpu",false,from,to)} fill="none" stroke="#60a5fa" strokeWidth="2"/>
  <path d={chartPath(points,"cpu",true,from,to)} fill="none" stroke="#f59e0b" strokeWidth="1.5"/>
  <path d={chartPath(points,"core",true,from,to)} fill="none" stroke="#f87171" strokeWidth="1" strokeDasharray="4 3"/>
  <path d={chartPath(points,"memory",false,from,to)} fill="none" stroke="#a78bfa" strokeWidth="1.5"/>
  {points.map(p=>p.values.cpu?<circle key={p.time} cx={20+Math.max(0,Math.min(1,(p.time-from)/(to-from)))*760} cy={110-Math.min(100,p.values.cpu.max)} r={p.values.cpu.max>=90?2:1} fill="#f59e0b"><title>{`${p.values.cpu.max.toFixed(1)}% CPU at ${new Date(p.values.cpu.peak_at).toLocaleString()}`}</title></circle>:null)}
  {points.map(p=>p.values.core&&p.values.core.max>=95?<circle key={`core-${p.time}`} cx={20+Math.max(0,Math.min(1,(p.time-from)/(to-from)))*760} cy={110-Math.min(100,p.values.core.max)} r="2" fill="#f87171"><title>{`${p.values.core.max.toFixed(1)}% busiest core at ${new Date(p.values.core.peak_at).toLocaleString()}`}</title></circle>:null)}
  <text x="20" y="130" fontSize="9" fill="currentColor">{new Date(from).toLocaleString()}</text><text x="780" y="130" textAnchor="end" fontSize="9" fill="currentColor">{new Date(to).toLocaleString()}</text>
 </svg>;
}
export function MonitoringHistory({id,withParams}:{id:number;withParams:()=>string}) {
 const generation=useRef(0);
 const [range,setRange]=useState(3600000);const [history,setHistory]=useState<History|null>(null);
 const [incidents,setIncidents]=useState<Incident[]>([]);const [selected,setSelected]=useState<Incident|null>(null);
 const [error,setError]=useState("");const [busy,setBusy]=useState(false);const [refresh,setRefresh]=useState(0);
 useEffect(()=>{generation.current++;setBusy(false);let cancelled=false,inFlight=false;const controller=new AbortController();setSelected(null);setHistory(null);
 const load=async()=>{if(inFlight||document.hidden)return;inFlight=true;try{
  const to=Date.now(),from=to-range;
  const query=`${withParams()}&from=${encodeURIComponent(new Date(from).toISOString())}&to=${encodeURIComponent(new Date(to).toISOString())}&max_points=600`;
  const [h,e]=await Promise.all([fetch(`${API}/instances/${id}/metrics/history?${query}`,{credentials:"same-origin",signal:controller.signal}),fetch(`${API}/instances/${id}/metrics/incidents?${withParams()}`,{credentials:"same-origin",signal:controller.signal})]);
  if(!h.ok||!e.ok)throw new Error("Monitoring history could not be loaded");
  const data=await h.json(),events=await e.json();if(cancelled)return;
  setHistory({...data,points:data.points||[],from:data.from||from,to:data.to||to});setIncidents(events.incidents||[]);setError("");
 }catch(e){if(!cancelled&&(e as Error).name!=="AbortError")setError((e as Error).message);}finally{inFlight=false;}};
 load();const timer=setInterval(load,10000);return()=>{generation.current++;cancelled=true;controller.abort();clearInterval(timer);};
 },[id,range,withParams,refresh]);
 const open=async(event:Incident)=>{const current=generation.current;setBusy(true);try{const r=await fetch(`${API}/instances/${id}/metrics/incidents?${withParams()}&incident_id=${encodeURIComponent(event.id)}`,{credentials:"same-origin"});if(!r.ok)throw new Error("Incident recording could not be loaded");const data=await r.json();if(current===generation.current)setSelected(data.incident);}catch(e){if(current===generation.current)setError((e as Error).message);}finally{if(current===generation.current)setBusy(false);}};
 const toggle=async()=>{const current=generation.current;setBusy(true);try{const r=await fetch(`${API}/instances/${id}/monitoring?${withParams()}`,{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({enabled:!history?.monitoring?.enabled})});if(!r.ok)throw new Error("Monitoring setting could not be saved");if(current===generation.current)setRefresh(n=>n+1);}catch(e){if(current===generation.current)setError((e as Error).message);}finally{if(current===generation.current)setBusy(false);}};
 const peak=history?.points.reduce((best,p)=>p.values.cpu&&p.values.cpu.max>(best?.max??-1)?p.values.cpu:best,undefined as Aggregate|undefined);
 const above=history?.points.reduce((sum,p)=>sum+(p.above_80_ms||0),0)||0;
 const recorded=selected?.recordings||[];
 const detailPoints:HistoryPoint[]=recorded.map(p=>({time:p.time,step_ms:p.interval_ms,observed_ms:p.interval_ms,values:Object.fromEntries(([["cpu",p.cpu],["core",p.core],["iowait",p.iowait],["memory",p.memory]] as Array<[string,number|undefined]>).filter(([,value])=>typeof value==="number").map(([key,value])=>[key,{sum:Number(value)*p.interval_ms,observed_ms:p.interval_ms,min:Number(value),max:Number(value),peak_at:p.time}]))}));
 return <div className="space-y-2 text-xs text-text-muted border-t border-border pt-3">
  <div className="flex flex-wrap items-center justify-between gap-2"><span className="text-text">Monitoring history</span><div className="flex gap-2"><select aria-label="History range" value={range} onChange={e=>setRange(Number(e.target.value))} className="bg-bg border border-border rounded px-2 py-1">{ranges.map(r=><option key={r.ms} value={r.ms}>{r.name}</option>)}</select>{history?.monitoring&&<button onClick={toggle} disabled={busy} className="border border-border rounded px-2">{history.monitoring.enabled?"Disable monitoring":"Enable monitoring"}</button>}</div></div>
  {error&&<p role="alert" className="text-red">{error}</p>}
  {history?.monitoring&&<p>Collector: {history.monitoring.state}{history.monitoring.version?` · v${history.monitoring.version}`:""}{history.monitoring.error?` · ${history.monitoring.error}`:""}</p>}
  {history?.points.length?<><MonitoringChart points={history.points} from={history.from} to={history.to}/><div className="flex flex-wrap gap-3 text-[10px]"><span style={{color:"#60a5fa"}}>CPU average</span><span style={{color:"#f59e0b"}}>CPU peak</span><span style={{color:"#f87171"}}>Busiest core peak</span><span style={{color:"#a78bfa"}}>Memory</span></div><p>Resolution {history.resolution} · CPU sampled every 250 ms · gaps indicate missing observations</p>{peak&&<p>Peak {peak.max.toFixed(1)}% at {new Date(peak.peak_at).toLocaleString()} · {(above/1000).toFixed(1)}s observed at ≥80% CPU</p>}</>:<p>{history?"No recorded samples in this range.":"Loading history…"}</p>}
  {!!history?.points.length&&<IOWaitChart points={history.points} from={history.from} to={history.to}/>}
  {!!history?.points.length&&<div className="grid grid-cols-2 gap-2 text-[10px]">{[["network_rx","Network RX"],["network_tx","Network TX"],["disk_read","Disk read (max device)"],["disk_write","Disk write (max device)"],["steal","VM steal"]].map(([key,label])=>{const peak=history.points.reduce((best,p)=>p.values[key]&&p.values[key].max>(best?.max??-1)?p.values[key]:best,undefined as Aggregate|undefined);return peak?<span key={key} title={new Date(peak.peak_at).toLocaleString()}>{label} peak: {key==="steal"?`${peak.max.toFixed(1)}%`:`${(peak.max/1024).toFixed(1)} KiB/s`}</span>:null;})}</div>}
  {!!history?.monitoring?.budget_evictions&&<p>The storage limit has shortened some older history or recordings. Available samples and gaps are shown above.</p>}
  {!!history?.monitoring?.detail_evicted&&<p>Some older incident detail expired under the storage limit. Recorded peaks remain in retained history.</p>}
  <div className="space-y-1"><span className="text-text">Spike recordings</span>{!incidents.length&&<p>No recorded incidents.</p>}{incidents.slice(0,10).map(event=><button key={event.id} disabled={busy} onClick={()=>open(event)} className="block w-full text-left border border-border rounded px-2 py-1 hover:text-text">{new Date(event.start).toLocaleString()} · {event.reason} · CPU {event.peak.toFixed(1)}% / core {event.core_peak.toFixed(1)}%{event.end?"":event.updated&&Date.now()-event.updated>15000?" · awaiting collector":" · recording"}{event.detail_expired?" · summary only":""}</button>)}</div>
  {selected&&<div className="border border-border rounded p-2 space-y-2"><div className="flex justify-between"><span>{selected.reason} · {new Date(selected.start).toLocaleString()}</span><button onClick={()=>setSelected(null)}>Close recording</button></div>{detailPoints.length>0?<><MonitoringChart points={detailPoints} from={recorded[0].time} to={recorded[recorded.length-1].time+250}/><IOWaitChart points={detailPoints} from={recorded[0].time} to={recorded[recorded.length-1].time+250}/></>:<p>Detailed samples have expired; the incident summary is still available.</p>}{!!selected.processes?.length&&<><p>Processes observed for 1s after the trigger (best effort; CPU is per-core %):</p>{selected.processes.map(p=><p key={p.pid}>{p.name} · PID {p.pid} · {p.cpu_pct.toFixed(1)}% CPU</p>)}</>}</div>}
 </div>;
}
