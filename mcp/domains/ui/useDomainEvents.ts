import {useEffect,useRef} from 'react';
export type DomainEvent = {topic:string;project_id:string;install_id:number;seq:number;data:Record<string,unknown>};
export function useDomainEvents(projectId:string,installId:number,onEvent:(event:DomainEvent)=>void){
 const handler=useRef(onEvent);handler.current=onEvent;
 useEffect(()=>{
  const receive=(event:DomainEvent)=>{if(event.project_id===projectId&&event.install_id===installId)handler.current(event)};
  const bridge=(window as unknown as {__aptevaAppEvents?:{subscribe:(app:string,project:string,fn:(event:DomainEvent)=>void)=>()=>void}}).__aptevaAppEvents;
  if(bridge)return bridge.subscribe('domains',projectId,receive);
  let last=0;const seen=new Set<string>();
  const source=new EventSource(`/api/app-events/domains?project_id=${encodeURIComponent(projectId)}`,{withCredentials:true});
  source.onmessage=e=>{try{const event=JSON.parse(e.data) as DomainEvent;const id=String(event.data?.event_id||'');if(event.seq<=last||id&&seen.has(id))return;last=event.seq;if(id){seen.add(id);if(seen.size>256)seen.delete(seen.values().next().value!)}receive(event)}catch{}};
  return()=>source.close();
 },[projectId,installId]);
}
