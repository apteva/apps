import { expect, test } from "bun:test";
import { MediaLease, type LeaseTimers, type MediaSessionEvent } from "../src/media-lease";

function clock() {
 let now=0, id=0;
 const tasks=new Map<number,{at:number;run:()=>void}>();
 const timers:LeaseTimers={now:()=>now,setTimeout:(run,ms)=>{const n=++id;tasks.set(n,{at:now+ms,run});return n as any;},clearTimeout:n=>{tasks.delete(n as any);}};
 const drain=async()=>{for(let i=0;i<10;i++)await Promise.resolve();};
 return {timers,async advance(ms:number){now+=ms;for(let i=0;i<30;i++){const next=[...tasks].filter(([,t])=>t.at<=now).sort((a,b)=>a[1].at-b[1].at)[0];if(!next)break;tasks.delete(next[0]);next[1].run();await drain();}},drain,tasks};
}

test("network and server failures retry while audio authorization remains valid",async()=>{
 for(const failure of [new TypeError("Failed to fetch"),{status:503},{status:429}]) {
  const c=clock();let calls=0;const ended:string[]=[],events:MediaSessionEvent[]=[];
  const lease=new MediaLease(60,async()=>{if(++calls===1)throw failure;return {lease_seconds:60};},r=>ended.push(r),e=>events.push(e),0,c.timers);
  await c.advance(20000);expect(ended).toEqual([]);expect(events.at(-1)?.outcome).toBe("retrying");
  await c.advance(250);expect(calls).toBe(2);expect(events.at(-1)?.outcome).toBe("renewed");
  await c.advance(38000);expect(ended).toEqual([]);lease.stop();expect(c.tasks.size).toBe(0);
 }
});
test("confirmed denial, expiry and caller termination never extend a lease",async()=>{
 for(const status of [401,403,404,410]){
  const c=clock(),ended:string[]=[];
  const lease=new MediaLease(60,async()=>{throw {status};},r=>ended.push(r),undefined,0,c.timers);
  await c.advance(20000);expect(ended).toEqual(["revoked"]);expect(c.tasks.size).toBe(0);lease.stop();
 }
 const c=clock(),ended:string[]=[];
 new MediaLease(60,async()=>{throw {status:403,body:'{"code":"media_lease_expired"}'};},r=>ended.push(r),undefined,0,c.timers);
 await c.advance(20000);expect(ended).toEqual(["expired"]);
});
test("background timer delay and hung requests cannot keep expired audio alive",async()=>{
 const c=clock(),ended:string[]=[];let calls=0;
 new MediaLease(60,async()=>{calls++;return new Promise(()=>{});},r=>ended.push(r),undefined,0,c.timers);
 await c.advance(20000);expect(calls).toBe(1);
 await c.advance(5000);expect(ended).toEqual([]);
 await c.advance(34000);expect(ended).toEqual(["expired"]);expect(c.tasks.size).toBe(0);
 const b=clock();let resumedRequests=0;const resumedEnded:string[]=[];
 new MediaLease(60,async()=>{resumedRequests++;},r=>resumedEnded.push(r),undefined,0,b.timers);
 await b.advance(65000);expect(resumedRequests).toBe(0);expect(resumedEnded).toEqual(["expired"]);
});
test("stop and expiry ignore late success; slow responses consume lease time",async()=>{
 for(const stop of [true,false]){
  const c=clock();let resolve!:(v:any)=>void;const ended:string[]=[];
  const lease=new MediaLease(60,()=>new Promise(r=>{resolve=r;}),r=>ended.push(r),undefined,0,c.timers);
  await c.advance(20000);if(stop)lease.stop();else await c.advance(39000);
  resolve({lease_seconds:60});await c.drain();expect(c.tasks.size).toBe(0);expect(ended).toEqual(stop?[]:["expired"]);
 }
 const c=clock(),ended:string[]=[];let resolve!:(v:any)=>void;
 const lease=new MediaLease(60,()=>new Promise(r=>{resolve=r;}),r=>ended.push(r),undefined,0,c.timers);
 await c.advance(20000);await c.advance(4000);resolve({lease_seconds:10});await c.drain();
 await c.advance(5000);expect(ended).toEqual(["expired"]);lease.stop();
});
