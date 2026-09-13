import { useEffect, useState } from "react";

type Props = { appName?: string; projectId?: string; installId?: number; eventRevision?: number; widgetSettings?: { recent_limit?: number } };
type Item = { id:string; process_id:string; process_name:string; assignment_id?:string; state:string; progress?:number; current_step?:string; backend?:string; created_at?:string; scheduled_for?:string; error?:string; delivery_warning?:string };
type Data = { running:Item[]; upcoming:Item[]; recent:Item[]; processes:number };

export default function ProcessOverviewWidget({ projectId, installId, eventRevision, widgetSettings }: Props) {
  const [data,setData] = useState<Data>({running:[],upcoming:[],recent:[],processes:0}); const [error,setError]=useState("");
  const load=()=>{ const q=new URLSearchParams({project_id:projectId||"", ...(installId?{install_id:String(installId)}:{})}); return fetch(`/api/apps/processes/overview?${q}`,{credentials:"same-origin"}).then(r=>{if(!r.ok) throw new Error(`Overview unavailable (${r.status})`); return r.json()}).then(setData).catch(e=>setError(String(e.message||e))); };
  useEffect(()=>{load()},[projectId,installId,eventRevision]);
  const limit=Math.max(1,Math.min(12,widgetSettings?.recent_limit||5));
  const row=(x:Item)=><div className="ap-process-overview-row" key={x.id}><div><strong>{x.process_name}</strong><span className={`pill ${x.state}`}>{x.state}</span><div className="small muted">{x.current_step||x.backend||"Procedure run"}{x.progress!=null?` · ${x.progress}%`:""}</div></div><a href={`?process_id=${encodeURIComponent(x.process_id)}&run_id=${encodeURIComponent(x.id)}`}>Open</a></div>;
  return <section aria-label="Processes overview" className="ap-process-overview"><div className="row between"><div><h2>Processes</h2><p className="small muted">{data.processes} procedures · live execution overview</p></div><button onClick={load}>Refresh</button></div>{error&&<p role="alert" className="notice">{error}</p>}<div className="ap-process-overview-stats"><span><b>{data.running.length}</b> Running</span><span><b>{data.upcoming.length}</b> Scheduled</span><span><b>{data.recent.filter(x=>x.state==="failed").length}</b> Failed</span></div><h3>Active</h3>{data.running.length?data.running.slice(0,limit).map(row):<p className="small muted">Nothing running.</p>}<h3>Recent</h3>{data.recent.length?data.recent.slice(0,limit).map(row):<p className="small muted">No recent executions.</p>}</section>;
}
