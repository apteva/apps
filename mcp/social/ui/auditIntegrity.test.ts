import {afterEach, expect, test} from "bun:test";
import {createRequestGate} from "./requestIdentity";
import {localScheduleInput,scheduleInstant} from "./scheduleTime";
import {uploadResumable} from "./uploadResumable";
const originalFetch=globalThis.fetch;
afterEach(()=>{globalThis.fetch=originalFetch});

test("older conversation response cannot replace newer selection, including away and back",async()=>{
 const gate=createRequestGate();let display="";
 let finishOld!:(s:string)=>void;
 const old=new Promise<string>(r=>finishOld=r);const isOld=gate.begin();
 const oldRequest=old.then(value=>{if(isOld())display=value});
 const isNew=gate.begin();if(isNew())display="B";
 gate.invalidate();const back=gate.begin();if(back())display="A fresh";
 finishOld("A stale");await oldRequest;expect(display).toBe("A fresh");
});
test("local schedule round trips with explicit instant",()=>{
 const local="2026-09-10T14:30";
 const instant=scheduleInstant(local);expect(instant.endsWith("Z")).toBe(true);
 expect(localScheduleInput(instant)).toBe(local);
 expect(()=>scheduleInstant("invalid")).toThrow();
});
test("chunk upload respects server concurrency and abort cleans up promptly",async()=>{
 const controller=new AbortController();let active=0,maxActive=0,deleted=false;
 globalThis.fetch=(async (url:any,init:any)=>{
  const path=String(url);
  if(init.method==="DELETE"){deleted=true;return new Response("{}")}
  if(path.includes("/parts/")){active++;maxActive=Math.max(active,maxActive);controller.abort();active--;throw new DOMException("aborted","AbortError")}
  return Response.json({upload_id:"upload",part_size:1024*1024,max_parallel:2,max_parts:100});
 }) as typeof fetch;
 const file=new File([new Uint8Array(26*1024*1024)],"video.mp4");
 await expect(uploadResumable(file,{parallel:12,signal:controller.signal})).rejects.toThrow("abort");
 expect(maxActive).toBeLessThanOrEqual(2);expect(deleted).toBe(true);
});
test("abort interrupts part retry backoff without completing upload",async()=>{
 const controller=new AbortController();let deleted=false,completed=false;
 globalThis.fetch=(async (url:any,init:any)=>{
  const path=String(url);
  if(init.method==="DELETE"){deleted=true;return Response.json({})}
  if(path.includes("/complete")){completed=true;return Response.json({file:{id:1}})}
  if(path.includes("/parts/")){setTimeout(()=>controller.abort(),10);return new Response("temporary",{status:503})}
  return Response.json({upload_id:"upload",part_size:30*1024*1024,max_parallel:1});
 }) as typeof fetch;
 const started=Date.now();
 await expect(uploadResumable(new File([new Uint8Array(26*1024*1024)],"v.mp4"),{signal:controller.signal})).rejects.toThrow("abort");
 expect(Date.now()-started).toBeLessThan(1000);expect(deleted).toBe(true);expect(completed).toBe(false);
});
test("schedule conversion rejects DST gaps and preserves the local UTC offset",()=>{
 const previous=process.env.TZ;
 try{
  process.env.TZ="Europe/Madrid";
  expect(scheduleInstant("2026-09-10T14:30")).toBe("2026-09-10T12:30:00.000Z");
  expect(()=>scheduleInstant("2026-03-29T02:30")).toThrow("does not exist");
 }finally{if(previous===undefined)delete process.env.TZ;else process.env.TZ=previous}
});
