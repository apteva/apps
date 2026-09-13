import { test, expect } from "bun:test";
import { AptevaClient } from "@apteva/web-sdk";
import { TelephonyClient } from "../src/client";

function fixture(authProvider?: string) {
  let state = "pending", active = 0, peak = 0, listCount = 0, cancelCount = 0;
  let beforeList = async () => {};
  let streamStatus = 200;
  const controllers: ReadableStreamDefaultController<Uint8Array>[] = [];
  const requests: Array<{url: URL; headers: Headers}> = [];
  const sdk = new AptevaClient({baseURL:"https://gateway.example",accessToken:"first",fetch: (async (url,init) => {
    const parsed=new URL(String(url));requests.push({url:parsed,headers:new Headers(init?.headers)});
    if(parsed.pathname.endsWith("/calls/events")) {
      if(streamStatus!==200)return new Response("unavailable",{status:streamStatus});
      const body=new ReadableStream<Uint8Array>({start(controller){controllers.push(controller);},cancel(){cancelCount++;}});
      return new Response(body,{headers:{"Content-Type":"text/event-stream"}});
    }
    listCount++;active++;peak=Math.max(peak,active);
    try{await beforeList();return Response.json({calls:[{id:"one",direction:"inbound",status:state,peer_kind:"human",from_number:"",to_number:""}]});}finally{active--;}
  }) as typeof fetch});
  const client=new TelephonyClient(sdk.app("telephony",{projectId:"p1",installId:42}),{authProvider});
  return {sdk,client,requests,controllers,setState(value:string){state=value;},setBeforeList(value:()=>Promise<void>){beforeList=value;},setStreamStatus(value:number){streamStatus=value;},
    emit(type="calls.changed"){controllers.at(-1)!.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({type})}\n\n`));},
    get listCount(){return listCount;},get peak(){return peak;},get cancelCount(){return cancelCount;}};
}
async function until(predicate:()=>boolean,timeout=600){const end=performance.now()+timeout;while(!predicate()&&performance.now()<end)await Bun.sleep(1);expect(predicate()).toBe(true);}

test("incoming push refreshes immediately, coalesces bursts, and cleans up",async()=>{
 const f=fixture();const seen:string[]=[];const timings:string[]=[];
 const watch=f.client.watchCalls(calls=>seen.push(calls[0].status),{intervalMs:10000,onTiming:s=>timings.push(s.trigger)});
 try{
  await until(()=>seen.length===1&&f.controllers.length===1);
  let release!:()=>void;const blocked=new Promise<void>(r=>release=r);f.setBeforeList(()=>blocked);f.setState("answering");
  const start=performance.now();f.emit();await until(()=>f.listCount===2);for(let i=0;i<100;i++)f.emit();await Bun.sleep(10);
  expect(f.listCount).toBe(2);release();await until(()=>seen.length===3);
  expect(performance.now()-start).toBeLessThan(500);expect(f.peak).toBe(1);expect(timings).toEqual(["poll","push","push"]);
 }finally{watch.close();}
 await until(()=>f.cancelCount===1);const count=f.listCount;await Bun.sleep(30);expect(f.listCount).toBe(count);
});

test("reconnect uses fresh auth and installation/project scope then reconciles",async()=>{
 const f=fixture("company login");let seen=0;const watch=f.client.watchCalls(()=>seen++,{intervalMs:10000});
 try{
  await until(()=>seen===1&&f.controllers.length===1);f.sdk.setAccessToken("renewed");f.controllers[0].close();
  await until(()=>f.controllers.length===2);f.emit();await until(()=>seen===2);
  const streams=f.requests.filter(r=>r.url.pathname.endsWith("/calls/events"));expect(streams).toHaveLength(2);
  expect(streams[1].headers.get("Authorization")).toBe("Bearer renewed");
  for(const r of f.requests){expect(r.url.searchParams.get("install_id")).toBe("42");expect(r.url.searchParams.get("project_id")).toBe("p1");expect(r.url.searchParams.get("auth_provider")).toBe("company login");expect(r.url.searchParams.has("api_key")).toBe(false);expect(r.url.pathname).toContain("/user/calls");}
 }finally{watch.close();}
});

test("old servers retain polling without repeated missing-endpoint requests",async()=>{
 const f=fixture();f.setStreamStatus(404);let seen=0;const watch=f.client.watchCalls(()=>seen++,{intervalMs:100});
 try{await until(()=>seen>=3);expect(f.requests.filter(r=>r.url.pathname.endsWith("/calls/events"))).toHaveLength(1);}finally{watch.close();}
});

test("push can be disabled and abort suppresses late responses",async()=>{
 const f=fixture();let release!:()=>void;f.setBeforeList(()=>new Promise<void>(r=>release=r));const abort=new AbortController();let seen=0;
 const watch=f.client.watchCalls(()=>seen++,{push:false,intervalMs:500,signal:abort.signal});abort.abort();release();await Bun.sleep(20);watch.close();expect(seen).toBe(0);expect(f.controllers).toHaveLength(0);
});

test("revocation closes the stream and forces an authorized refresh",async()=>{
 const f=fixture();let seen=0;const watch=f.client.watchCalls(()=>seen++,{intervalMs:10000});
 try{await until(()=>seen===1&&f.controllers.length===1);f.emit("access.revoked");await until(()=>f.cancelCount===1&&seen===2);await Bun.sleep(300);expect(f.controllers).toHaveLength(1);}finally{watch.close();}
});
