import {test,expect} from "bun:test";
import {AptevaClient} from "@apteva/web-sdk";
import {telephonyExtension,type WatchCallFailure} from "../src/client";
const calls={calls:[{id:"call-a",status:"pending",direction:"inbound",peer_kind:"human",answerable:true,ring_offers:[{id:"offer-a",kind:"browser",destination_id:"sales"}]}]};
function fixture(){let read=async(_init?:RequestInit):Promise<Response>=>Response.json(calls),reads=0;const requests:Array<{path:string;body:any;headers:Headers}>=[];
 const sdk=new AptevaClient({baseURL:"https://gateway.test",apiKey:"fixture",fetch:(async(url,init)=>{const path=new URL(String(url)).pathname;requests.push({path,body:init?.body?JSON.parse(String(init.body)):undefined,headers:new Headers(init?.headers)});if(path.endsWith("/calls")){reads++;return read(init)}return Response.json({acknowledged:true})}) as typeof fetch});const client=sdk.use(telephonyExtension,{projectId:"p",installId:42});
 return {client,requests,get reads(){return reads},setRead(fn:typeof read){read=fn}};
}
async function until(fn:()=>boolean,timeout=600){const end=Date.now()+timeout;while(!fn()&&Date.now()<end)await Bun.sleep(1);expect(fn()).toBe(true)}
test("synchronous host callback exception is never classified as HTTP, and receipt ACK remains independent",async()=>{
 const f=fixture(),failures:WatchCallFailure[]=[],callbackFailures:WatchCallFailure[]=[],timings:any[]=[],legacyErrors:any[]=[];
 const error=Object.assign(new Error("host renderer failed"),{status:503});
 const watch=f.client.watchCalls(()=>{throw error},{push:false,intervalMs:100,onFailure:e=>failures.push(e),onCallbackError:e=>callbackFailures.push(e),onTiming:e=>timings.push(e),onError:e=>legacyErrors.push(e)});
 try{await until(()=>failures.length>=1);expect(failures[0]).toMatchObject({operation:"callback",callback:"onCalls",category:"callback",code:"on_calls_exception"});expect(failures[0].status).toBeUndefined();expect(legacyErrors[0]).toMatchObject({category:"callback",callback:"onCalls"});expect(legacyErrors[0].status).toBeUndefined();expect(legacyErrors[0].cause).toBe(error);expect(callbackFailures).toHaveLength(1);expect(f.requests.filter(r=>r.path.includes("/offer/ack/"))).toHaveLength(1);expect(timings).toHaveLength(1);expect(watch.getDiagnostics().last_successful_call_list_at).toBeDefined();expect(watch.getDiagnostics().consecutive_list_failures).toBe(0);await until(()=>f.reads>=2);expect(f.requests.filter(r=>r.path.includes("/offer/ack/"))).toHaveLength(1)}finally{watch.close()}
});
test("async host rejection is reported separately and does not hold the next list read",async()=>{
 const f=fixture(),failures:WatchCallFailure[]=[];let reject!:(e:unknown)=>void;
 const pending=new Promise<void>((_,r)=>{reject=r});const watch=f.client.watchCalls(()=>pending,{push:false,intervalMs:100,onFailure:e=>failures.push(e)});
 try{await until(()=>f.reads>=2);expect(failures).toHaveLength(0);reject(Error("async renderer"));await until(()=>failures.length>=1);expect(failures.every(e=>e.category==="callback"&&e.operation==="callback")).toBe(true);expect(watch.getDiagnostics().consecutive_list_failures).toBe(0)}finally{watch.close()}
});
test("HTTP failure preserves safe status, server code and both request IDs",async()=>{
 const f=fixture(),failures:WatchCallFailure[]=[];f.setRead(async()=>Response.json({code:"runtime_not_admitting",request_id:"server-request-12"},{status:503}));
 const watch=f.client.watchCalls(()=>{throw Error("must not run")},{push:false,onFailure:e=>failures.push(e)});
 try{await until(()=>failures.length===1);expect(failures[0]).toMatchObject({operation:"call_list",category:"http",status:503,code:"runtime_not_admitting",requestId:"server-request-12"});expect(failures[0].clientRequestId).toBe(f.requests[0].headers.get("X-Request-ID")!);expect(watch.getDiagnostics().consecutive_list_failures).toBe(1)}finally{watch.close()}
});
for(const [name,response] of [["shape",()=>Response.json({calls:"bad"})],["nested offers",()=>Response.json({calls:[{id:"x",status:"pending",ring_offers:{invalid:true}}]})],["JSON",()=>new Response('{broken',{headers:{"Content-Type":"application/json"}})]] as const){test(`invalid ${name} response does not run the host callback or send a receipt`,async()=>{
 const f=fixture(),failures:WatchCallFailure[]=[];let delivered=0;f.setRead(async()=>response());const watch=f.client.watchCalls(()=>delivered++,{push:false,onFailure:e=>failures.push(e)});
 try{await until(()=>failures.length===1);expect(failures[0].category).toBe("invalid_response");expect(failures[0].operation).toBe("call_list");expect(delivered).toBe(0);expect(f.requests.some(r=>r.path.includes("/offer/ack/"))).toBe(false)}finally{watch.close()}
})}
test("network failures and explicit per-read timeouts have distinct categories",async()=>{
 for(const category of ["network","timeout"] as const){const f=fixture(),failures:WatchCallFailure[]=[];f.setRead(category==="network"?async()=>{throw new TypeError("Failed to fetch")}:async()=>new Promise<Response>(()=>{}));const watch=f.client.watchCalls(()=>{},{push:false,requestTimeoutMs:100,onFailure:e=>failures.push(e)});
 try{await until(()=>failures.length===1);expect(failures[0].category).toBe(category);expect(failures[0].operation).toBe("call_list");expect(failures[0].requestId).toBeDefined();expect(failures[0].clientRequestId).toBe(f.requests[0].headers.get("X-Request-ID")!)}finally{watch.close()}}
});
test("closing a watcher cancels a pending read without reporting a connection failure",async()=>{
 const f=fixture(),failures:WatchCallFailure[]=[];let resolve!:(r:Response)=>void;f.setRead(async()=>new Promise(r=>{resolve=r}));let delivered=0;const watch=f.client.watchCalls(()=>delivered++,{push:false,onFailure:e=>failures.push(e)});await until(()=>Boolean(resolve));watch.close();resolve(Response.json(calls));await Bun.sleep(5);expect(failures).toHaveLength(0);expect(delivered).toBe(0);expect(watch.getDiagnostics().state).toBe("closed");expect(f.requests.filter(r=>r.path.includes("/offer/ack/"))).toHaveLength(0);
});
test("diagnostic observers throwing cannot become request failures or stop call delivery",async()=>{
 const f=fixture();let delivered=0;const watch=f.client.watchCalls(()=>delivered++,{push:false,intervalMs:100,onTiming:()=>{throw Error("observer")},onDiagnostics:()=>{throw Error("observer")},onFailure:()=>{throw Error("observer")},onError:()=>{throw Error("observer")}});try{await until(()=>delivered>=2);expect(watch.getDiagnostics().consecutive_list_failures).toBe(0)}finally{watch.close()}
});
test("browser evidence reports use the same session as receipt ACKs and never fabricate a playback event",async()=>{
 const f=fixture();const watch=f.client.watchCalls(()=>{},{push:false,persistDiagnostics:false});
 try{await until(()=>f.requests.some(r=>r.path.includes("/offer/ack/")));const ack=f.requests.find(r=>r.path.includes("/offer/ack/"))!.body;
  expect(f.requests.some(r=>r.body?.events?.some((e:any)=>e.stage==="ringtone_started"))).toBe(false);
  await f.client.reportOfferEvent({call_id:"call-a",offer_id:"offer-a",stage:"ui_displayed",event_id:"display-once"});const report=f.requests.find(r=>r.path.endsWith("/telemetry"))!.body;
  expect(report.browser_tab_id).toBe(ack.browser_tab_id);expect(report.browser_session_id).toBe(ack.browser_session_id);expect(report.events[0]).toMatchObject({stage:"ui_displayed",event_id:"display-once"});expect(report.watch.last_successful_call_list_at).toBeDefined();
 }finally{watch.close()}
});
test("separate clients in one tab share a tab ID but retain independent session IDs",()=>{const first=fixture(),second=fixture();expect(first.client.browserTabId).toBe(second.client.browserTabId);expect(first.client.browserSessionId).not.toBe(second.client.browserSessionId)});

test("HTTP errors mentioning JSON parsing remain HTTP failures",async()=>{
 const f=fixture(),failures:WatchCallFailure[]=[];f.setRead(async()=>new Response('Failed to parse JSON',{status:503}));const watch=f.client.watchCalls(()=>{},{push:false,onFailure:e=>failures.push(e)});
 try{await until(()=>failures.length===1);expect(failures[0]).toMatchObject({category:"http",status:503})}finally{watch.close()}
});
test("stream reconnects and heartbeats remain separate from successful list reads",async()=>{
 const f=fixture();let event!:(e:any)=>void,options:any;
 (f.client as any).app.subscribe=(_path:string,onEvent:(e:any)=>void,opts:any)=>{event=onEvent;options=opts;return {close(){}}};
 const watch=f.client.watchCalls(()=>{});
 try{await until(()=>Boolean(options)&&Boolean(watch.getDiagnostics().last_successful_call_list_at));options.onOpen();event({type:"heartbeat"});
  expect(watch.getDiagnostics()).toMatchObject({state:"connected",connections:1,reconnects:0});expect(watch.getDiagnostics().last_heartbeat_at).toBeDefined();
  const successfulRead=watch.getDiagnostics().last_successful_call_list_at;
  options.onError(new TypeError("Failed to fetch"));expect(watch.getDiagnostics()).toMatchObject({state:"reconnecting",disconnects:1,last_failure:{category:"network"}});
  expect(watch.getDiagnostics().last_successful_call_list_at).toBe(successfulRead);options.onOpen();expect(watch.getDiagnostics()).toMatchObject({state:"connected",connections:2,reconnects:1});
 }finally{watch.close()}
});
test("diagnostic snapshots coalesce and never retain arbitrary server text",async()=>{
 const f=fixture();let event!:(e:any)=>void,options:any;
 (f.client as any).app.subscribe=(_path:string,onEvent:(e:any)=>void,opts:any)=>{event=onEvent;options=opts;return {close(){}}};
 const watch=f.client.watchCalls(()=>{});
 try{await until(()=>f.requests.some(r=>r.path.endsWith('/telemetry')));for(let i=0;i<30;i++)event({type:"heartbeat"});
  options.onError(Object.assign(Error("private message"),{status:503,body:JSON.stringify({code:"secret token with spaces",request_id:"unsafe secret"})}));
  expect(watch.getDiagnostics().last_failure).toMatchObject({category:"http",status:503});expect(watch.getDiagnostics().last_failure?.code).toBeUndefined();
  expect(JSON.stringify(watch.getDiagnostics())).not.toContain("private message");expect(JSON.stringify(watch.getDiagnostics())).not.toContain("unsafe secret");
  expect(f.requests.filter(r=>r.path.endsWith('/telemetry')).length).toBeLessThanOrEqual(1);
 }finally{watch.close()}
});

test("throwing failure observers cannot stop a watcher after a host callback failure",async()=>{
 const f=fixture();let delivered=0;const watch=f.client.watchCalls(()=>{delivered++;throw Error("renderer")},{push:false,intervalMs:100,onCallbackError:()=>{throw Error("observer")},onError:()=>{throw Error("observer")}});
 try{await until(()=>delivered>=2);expect(watch.getDiagnostics().state).not.toBe("closed");expect(watch.getDiagnostics().consecutive_list_failures).toBe(0)}finally{watch.close()}
});
