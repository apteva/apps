import { useEffect, useState, useRef, useId } from "react";

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
interface ChartSeries {key:string;label:string;color:string;peak?:boolean;digits:number;dash?:string}
const cpuSeries:ChartSeries[]=[
 {key:"cpu",label:"CPU average",color:"#60a5fa",digits:1},
 {key:"cpu",label:"CPU peak",color:"#f59e0b",peak:true,digits:1},
 {key:"core",label:"Busiest core peak",color:"#f87171",peak:true,digits:1,dash:"4 3"},
 {key:"memory",label:"Memory average",color:"#a78bfa",digits:1},
];
const ioSeries:ChartSeries[]=[
 {key:"iowait",label:"I/O wait average",color:"#22d3ee",digits:2},
 {key:"iowait",label:"I/O wait peak",color:"#fb923c",peak:true,digits:2},
];
function chartValue(point:HistoryPoint,series:ChartSeries):number|undefined {
 if(point.observed_ms<=0)return undefined;
 const value=series.peak?point.values[series.key]?.max:metricAverage(point,series.key);
 return value!==undefined&&Number.isFinite(value)?value:undefined;
}
// SVG's default aspect ratio can letterbox wide/narrow panels. Account for it
// so the crosshair selects the actual plotted timestamp at every panel width.
export function pointerChartTime(clientX:number,rect:{left:number;width:number;height:number},from:number,to:number):number|undefined {
 const scale=Math.min(rect.width/800,rect.height/135);
 if(scale<=0||to<=from)return undefined;
 const x=(clientX-rect.left-(rect.width-800*scale)/2)/scale;
 return x>=20&&x<=780?from+(x-20)/760*(to-from):undefined;
}
export function nearestChartPoint(points:HistoryPoint[],time:number):HistoryPoint|undefined {
 let best:HistoryPoint|undefined;
 for(const point of points){
  if(point.observed_ms<=0||Math.abs(point.time-time)>point.step_ms/2)continue;
  if(!best||Math.abs(point.time-time)<Math.abs(best.time-time))best=point;
 }
 return best;
}
export function MetricHistoryChart({points,from,to,kind,max=100}:{points:HistoryPoint[];from:number;to:number;kind:"cpu"|"iowait";max?:number}) {
 const series=kind==="cpu"?cpuSeries:ioSeries;
 const [selectedTime,setSelectedTime]=useState<number>();
 const tooltipId=useId();
 const observed=points.filter(p=>series.some(s=>chartValue(p,s)!==undefined));
 const selected=observed.find(p=>p.time===selectedTime);
 const xAt=(time:number)=>20+Math.max(0,Math.min(1,(time-from)/(to-from)))*760;
 const yAt=(value:number)=>110-Math.max(0,Math.min(max,value))/max*100;
 const inspect=(e:React.PointerEvent<SVGSVGElement>)=>{
  const time=pointerChartTime(e.clientX,e.currentTarget.getBoundingClientRect(),from,to);
  setSelectedTime(time===undefined?undefined:nearestChartPoint(observed,time)?.time);
 };
 const navigate=(e:React.KeyboardEvent<SVGSVGElement>)=>{
  if(e.key==="Escape"){setSelectedTime(undefined);return;}
  if(!["ArrowLeft","ArrowRight","Home","End"].includes(e.key)||!observed.length)return;
  e.preventDefault();const index=observed.findIndex(p=>p.time===selectedTime);
  const next=e.key==="Home"?0:e.key==="End"?observed.length-1:index<0?0:Math.max(0,Math.min(observed.length-1,index+(e.key==="ArrowRight"?1:-1)));
  setSelectedTime(observed[next].time);
 };
 return <div style={{position:"relative"}}>
  <svg viewBox="0 0 800 135" className="w-full h-40" role="img" tabIndex={0}
   aria-label={kind==="cpu"?"CPU average and recorded peaks, busiest core peaks, and memory history":"I/O wait average and recorded peaks over time"}
   aria-describedby={selected?tooltipId:undefined}
   onPointerMove={inspect} onPointerDown={e=>{e.currentTarget.focus();inspect(e);}} onPointerLeave={e=>{if(e.pointerType!=="touch")setSelectedTime(undefined);}}
   onFocus={()=>setSelectedTime(observed[0]?.time)} onBlur={()=>setSelectedTime(undefined)} onKeyDown={navigate}>
   {[0,max/2,max].map(v=><g key={v}><line x1="20" x2="780" y1={yAt(v)} y2={yAt(v)} stroke="currentColor" opacity="0.1"/><text x="1" y={yAt(v)+3} fontSize="9" fill="currentColor">{v}{kind==="iowait"?"%":""}</text></g>)}
   {series.map(s=><path key={s.label} d={chartPath(points,s.key,!!s.peak,from,to,max)} fill="none" stroke={s.color} strokeWidth={s.peak?1.5:2} strokeDasharray={s.dash}/>)}
   {series.filter(s=>s.peak).flatMap(s=>observed.map(p=>{
    const value=chartValue(p,s);if(value===undefined||(s.key==="core"&&value<95))return null;
    const name=s.key==="cpu"?"CPU":s.key==="core"?"busiest core":"I/O wait peak";
    return <circle key={`${s.key}-${p.time}`} cx={xAt(p.time)} cy={yAt(value)} r={value>=90||s.key==="iowait"&&value>0?2:1} fill={s.color}>
     <title>{`${value.toFixed(s.digits)}% ${name} at ${new Date(p.values[s.key].peak_at).toLocaleString()}${s.key==="iowait"?` · bucket average ${metricAverage(p,s.key)?.toFixed(2)}%`:""}`}</title>
    </circle>;
   }))}
   {selected&&<g pointerEvents="none">
    <line x1={xAt(selected.time)} x2={xAt(selected.time)} y1="10" y2="110" stroke="currentColor" opacity="0.5" strokeDasharray="2 2"/>
    {series.map(s=>{const value=chartValue(selected,s);return value!==undefined?<circle key={s.label} cx={xAt(selected.time)} cy={yAt(value)} r="3" fill={s.color} stroke="#181818"/>:null;})}
   </g>}
   <text x="20" y="130" fontSize="9" fill="currentColor">{new Date(from).toLocaleString()}</text><text x="780" y="130" textAnchor="end" fontSize="9" fill="currentColor">{new Date(to).toLocaleString()}</text>
  </svg>
  <p className="text-[10px]">Hover or tap for values · focus the chart and use arrow keys to inspect samples</p>
  {selected&&<div id={tooltipId} role="tooltip" style={{position:"absolute",top:4,...(selected.time<(from+to)/2?{right:8}:{left:8}),maxWidth:"calc(100% - 16px)",padding:"8px 10px",border:"1px solid #555",borderRadius:6,background:"#181818",color:"#eee",fontSize:11,lineHeight:1.5,pointerEvents:"none",zIndex:1,boxShadow:"0 2px 8px #0008"}}>
   <div>{new Date(selected.time).toLocaleString()} · {(selected.step_ms/1000).toLocaleString()}s bucket</div>
   {series.map(s=>{const value=chartValue(selected,s);return <div key={s.label}>
    <span style={{color:s.color}}>{s.label}</span>: <strong>{value===undefined?"No observation":`${value.toFixed(s.digits)}%`}</strong>
    {s.peak&&value!==undefined&&<span> at {new Date(selected.values[s.key].peak_at).toLocaleString()}</span>}
   </div>;})}
   {selected.observed_ms<selected.step_ms*0.8&&<div>Partial observations: {(selected.observed_ms/selected.step_ms*100).toFixed(0)}% coverage</div>}
  </div>}
 </div>;
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
   <MetricHistoryChart points={points} from={from} to={to} kind="iowait" max={max}/>
   <div className="flex flex-wrap gap-3 text-[10px]"><span style={{color:"#22d3ee"}}>I/O wait average</span><span style={{color:"#fb923c"}}>I/O wait peak</span></div>
   <p>Average {average.toFixed(2)}% · Peak {peak.max.toFixed(2)}% at {new Date(peak.peak_at).toLocaleString()}</p>
   <p className="text-[10px]">Sampled every 250 ms · chart scale 0–{max}% · gaps indicate missing observations</p>
  </>:<p>No I/O wait observations in this range.</p>}
 </div>;
}
function MonitoringChart({points,from,to}:{points:HistoryPoint[];from:number;to:number}) {
 return <MetricHistoryChart points={points} from={from} to={to} kind="cpu"/>;
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
