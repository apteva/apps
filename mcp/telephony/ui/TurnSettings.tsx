import {useEffect,useRef,useState} from "react";
import {turnPolicyUpdate,type TURNIdentity,type TURNPolicy,type TURNPolicyRequest,type TURNPolicyView} from "../frontend/src/turn-policy";
export * from "../frontend/src/turn-policy";
export default function TurnSettings({request,admin=false}:{request:TURNPolicyRequest;admin?:boolean}){
 const [open,setOpen]=useState(false),[identity,setIdentity]=useState("");
 const [view,setView]=useState<TURNPolicyView>(),[policy,setPolicy]=useState<TURNPolicy>();
 const [users,setUsers]=useState<TURNIdentity[]>([]),[busy,setBusy]=useState(false),[error,setError]=useState("");
 const generation=useRef(0);
 const target=()=>identity?{identity:JSON.parse(identity)}:{};
 useEffect(()=>{
  if(!open)return;const gen=++generation.current;setBusy(true);setError("");setView(undefined);
  request(identity?{identity:JSON.parse(identity)}:{}).then(v=>{if(gen!==generation.current)return;setView(v);setPolicy(v.effective);if(v.advisers)setUsers(v.advisers);}).catch(e=>{if(gen===generation.current)setError(e.message)}).finally(()=>{if(gen===generation.current)setBusy(false)});
  return ()=>{generation.current++};
 },[request,open,identity]);
 const save=async(inherit=false)=>{
  if(!policy)return;const gen=++generation.current;setBusy(true);setError("");
  try{const v=await request({...target(),...(inherit?{inherit:true}:{policy:turnPolicyUpdate(policy,view?.scope??"adviser")})});if(gen===generation.current){setView(v);setPolicy(v.effective);}}
  catch(e){if(gen===generation.current)setError((e as Error).message)}finally{if(gen===generation.current)setBusy(false)}
 };
 const canEdit=admin||view?.scope==="project"||view?.allow_user_overrides;
 return <details className="rounded border border-border p-3 text-sm" open={open} onToggle={e=>setOpen(e.currentTarget.open)}>
  <summary className="cursor-pointer">WebRTC relay / TURN settings</summary>
  <div className="mt-3 space-y-3">
   {admin&&<label className="block">Applies to<select className="mt-1 w-full rounded border border-border bg-bg p-2" value={identity} onChange={e=>setIdentity(e.target.value)} disabled={busy}><option value="">Project defaults</option>{users.map(u=><option key={JSON.stringify(u)} value={JSON.stringify(u)}>{u.subject_id} · {u.issuer_app} #{u.issuer_install_id}</option>)}</select></label>}
   <p className="text-xs text-text-muted">Use WebRTC or Auto media transport for relay policies. Optional. Bind the TURN app in Telephony integrations to use managed credentials. Domain, listener ports and TLS are configured in TURN. Changes apply to the next connection; use the softphone’s reconnect action for an active call.</p>
   {view&&<p className="text-xs">TURN integration: {view.turn_bound?"bound":"not bound"}. {view.scope==="adviser"?(view.override?"Adviser override":"Using project defaults"):"Project defaults"}.</p>}
   {!canEdit&&view&&<p className="text-xs">Your administrator controls these relay settings.</p>}
   {policy&&<fieldset disabled={busy||!view||!canEdit} className="space-y-3">
    <label className="block">Credential source<select className="mt-1 w-full rounded border border-border bg-bg p-2" value={policy.source} onChange={e=>setPolicy({...policy,source:e.target.value as TURNPolicy["source"]})}><option value="existing">Existing ICE configuration (default)</option><option value="app" disabled={!view?.turn_bound}>Optional TURN app</option></select></label>
    <label className="block">Connection policy<select className="mt-1 w-full rounded border border-border bg-bg p-2" value={policy.ice_transport_policy} onChange={e=>setPolicy({...policy,ice_transport_policy:e.target.value as TURNPolicy["ice_transport_policy"]})}><option value="all">Direct preferred; relay available</option><option value="relay">Force relay (controlled comparison)</option></select></label>
    <label className="block">TURN transport<select className="mt-1 w-full rounded border border-border bg-bg p-2" value={policy.transport} onChange={e=>setPolicy({...policy,transport:e.target.value as TURNPolicy["transport"],tls_port:undefined})}>{["all","udp","tcp","tls"].map(v=><option key={v} value={v}>{v.toUpperCase()}</option>)}</select></label>
    {policy.transport==="tls"&&<label className="block">Configured TURN TLS port (optional)<input className="mt-1 w-full rounded border border-border bg-bg p-2" type="number" min={1} max={65535} placeholder="All configured TLS listeners" value={policy.tls_port??""} onChange={e=>setPolicy({...policy,tls_port:e.target.value?Number(e.target.value):undefined})}/></label>}
    {policy.source==="app"&&<label className="block">Temporary credential lifetime (seconds)<input className="mt-1 w-full rounded border border-border bg-bg p-2" type="number" min={60} max={86400} value={policy.ttl_seconds} onChange={e=>setPolicy({...policy,ttl_seconds:Number(e.target.value)})}/></label>}
    {view?.scope==="project"&&<label className="flex gap-2"><input type="checkbox" checked={policy.allow_user_overrides??false} onChange={e=>setPolicy({...policy,allow_user_overrides:e.target.checked})}/>Allow advisers to change their own relay policy and session test options</label>}
    <div className="flex gap-2"><button type="button" className="rounded border border-border px-3 py-2" onClick={()=>void save()}>Save relay settings</button>{view?.scope==="adviser"&&<button type="button" className="rounded border border-border px-3 py-2" onClick={()=>void save(true)}>Use project defaults</button>}</div>
   </fieldset>}
   {error&&<p role="alert" className="text-error">{error}</p>}
  </div>
 </details>;
}
