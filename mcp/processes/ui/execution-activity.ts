import {useEffect, useState} from "react";
export type TelemetryEvent = {id?: string; seq?: number; instance_id?: number; thread_id?: string; type: string; time: string; data?: Record<string, any>};
const time = (event: TelemetryEvent) => Date.parse(event.time);
export function eventsForStep(events: TelemetryEvent[], executionID: string, stepID?: string, completedAt?: string) {
  const claims = events.filter(e => e.type === "tool.call" && e.data?.name?.endsWith("step_claim") && e.data?.args?.step_id === stepID);
  const start = claims.length ? Math.min(...claims.map(time)) : -Infinity;
  const end = completedAt ? Date.parse(completedAt) : Infinity;
  return events.filter(event => {
    if (time(event) < start || time(event) > end) return false;
    const data = event.data || {};
    const ids = data.execution_ids;
    if (Array.isArray(ids)) return ids.some(id => String(id) === executionID);
    if (data.execution_id) return String(data.execution_id) === executionID;
    // Older runtimes omit execution IDs on model events. The exact worker
    // thread and claimed-step lifetime constrain those events instead.
    return !!stepID && claims.length > 0 && ["llm.start", "llm.thinking", "llm.chunk", "llm.done", "llm.error"].includes(event.type);
  }).sort((a,b) => time(a)-time(b));
}
export function thoughtsFromEvents(events: TelemetryEvent[]) {
  const turns = new Map<string, {id:string; time:string; reasoning:string; response:string; status:string}>();
  for (const event of events) {
    if (!["llm.start", "llm.thinking", "llm.chunk", "llm.done", "llm.error"].includes(event.type)) continue;
    const data = event.data || {};
    const id = String(data.iteration ?? data.request_id ?? event.id ?? event.time);
    const turn = turns.get(id) || {id, time:event.time, reasoning:"", response:"", status:"running"};
    if (event.type === "llm.thinking") turn.reasoning += String(data.text || data.chunk || "");
    if (event.type === "llm.chunk") turn.response += String(data.text || data.chunk || "");
    if (event.type === "llm.done") {turn.status = "completed"; if (data.message) turn.response = String(data.message);}
    if (event.type === "llm.error") {turn.status = "failed"; turn.response = String(data.error || data.message || "Model error");}
    turns.set(id,turn);
  }
  return [...turns.values()];
}

type Snapshot = {events:TelemetryEvent[]; loading:boolean; error:string};
type Feed = {snapshot:Snapshot; listeners:Map<(s:Snapshot)=>void,boolean>; stop:()=>void; refresh:()=>void; refreshedAt?:number; cleanupTimer?:ReturnType<typeof setTimeout>};
const feeds = new Map<string, Feed>();
export function subscribeWorker(agentID:number, threadID:string, live:boolean, listener:(s:Snapshot)=>void) {
  const key = `${agentID}:${threadID}`;
  let feed = feeds.get(key);
  if (!feed) {
    const controller = new AbortController();
    let active = true, fetching = false, latestStored = 0;
    let records = new Map<string,TelemetryEvent>();
    feed = {snapshot:{events:[], loading:true,error:""}, listeners:new Map(),stop:()=>{},refresh:()=>{}};
    const own = feed;
    const publish = () => own.listeners.forEach((_, fn) => fn(own.snapshot));
    const ingest = (events:TelemetryEvent[]) => {
      for (const event of events) {
        if (event.thread_id && event.thread_id !== threadID) continue;
        const id = event.id || (event.seq ? `seq:${event.seq}` : JSON.stringify([event.type,event.time,event.data]));
        records.set(id,event);
      }
      const ordered = [...records.entries()].sort((a,b)=>time(a[1])-time(b[1]));
      records = new Map(ordered.slice(-2000));
      own.snapshot = {...own.snapshot, events:[...records.values()]};
    };
    own.refresh = async () => {
      if (!active || fetching) return;
      fetching = true;
      try {
        const query = new URLSearchParams({agent_id:String(agentID),thread_id:threadID,limit:"1000"});
        if (latestStored) query.set("since",new Date(Math.max(0,latestStored-2000)).toISOString());
        const response = await fetch(`/api/telemetry?${query}`,{credentials:"same-origin",signal:controller.signal});
        if (!response.ok) throw new Error(`Worker activity unavailable (${response.status})`);
        const rows = await response.json();
        if (!active) return;
        const events:TelemetryEvent[] = Array.isArray(rows) ? rows : [];
        ingest(events);
        for (const event of events) if (Number.isFinite(time(event))) latestStored = Math.max(latestStored,time(event));
        own.snapshot = {...own.snapshot,loading:false,error:""};
      } catch (error) {
        if (active && !controller.signal.aborted) own.snapshot = {...own.snapshot,loading:false,error:error instanceof Error ? error.message : String(error)};
      } finally {fetching=false;own.refreshedAt=Date.now();if(active)publish();}
    };
    const bus = (window as unknown as {__aptevaTelemetryBus?:{subscribe:(id:number,fn:(event:TelemetryEvent)=>void)=>()=>void}}).__aptevaTelemetryBus;
    const unsubscribe = bus?.subscribe(agentID,event => {
      if (!active || event.thread_id !== threadID) return;
      ingest([event]);publish();
    });
    const recover = () => {if(document.visibilityState !== "hidden")own.refresh();};
    const timer = setInterval(()=> {if([...own.listeners.values()].some(Boolean))recover();},5000);
    for (const event of ["online","apteva.telemetry.reconnected","apteva.telemetry.gap"]) window.addEventListener(event,recover);
    document.addEventListener("visibilitychange",recover);
    own.stop = () => {
      active=false;controller.abort();clearInterval(timer);unsubscribe?.();
      for (const event of ["online","apteva.telemetry.reconnected","apteva.telemetry.gap"]) window.removeEventListener(event,recover);
      document.removeEventListener("visibilitychange",recover);
    };
    feeds.set(key,own);
  }
  clearTimeout(feed.cleanupTimer);
  feed.listeners.set(listener,live);listener(feed.snapshot);if(!feed.refreshedAt || Date.now()-feed.refreshedAt>1000)feed.refresh();
  return () => {feed!.listeners.delete(listener);if(!feed!.listeners.size){feed!.cleanupTimer=setTimeout(()=>{if(!feed!.listeners.size){feed!.stop();feeds.delete(key);}},0);}};
}
export function useWorkerActivity(agentID?:number,threadID?:string,enabled=false,live=false) {
  const key = `${agentID}:${threadID}`;
  const [state,setState] = useState<{key:string;snapshot:Snapshot}>({key:"",snapshot:{events:[],loading:false,error:""}});
  useEffect(()=> {
    if(!enabled || !agentID || !threadID)return;
    return subscribeWorker(agentID,threadID,live,snapshot=>setState({key,snapshot}));
  },[key,enabled,live]);
  return state.key===key ? state.snapshot : {events:[],loading:enabled,error:""};
}
