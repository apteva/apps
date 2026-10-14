import {expect,test} from "bun:test";
import {AudioRecoveryLoop, type RecoveryTimers} from "../src/audio-recovery";
class Clock{now=0;next=0;tasks=new Map<number,{at:number;fn:()=>void}>();timers:RecoveryTimers={set:(fn,ms)=>{const n=++this.next;this.tasks.set(n,{at:this.now+ms,fn});return n as any;},clear:id=>{this.tasks.delete(id as any);}};
 async drain(){for(let i=0;i<20;i++)await Promise.resolve();}
 async advance(ms:number){const target=this.now+ms;for(;;){const item=[...this.tasks].sort((a,b)=>a[1].at-b[1].at)[0];if(!item||item[1].at>target)break;this.now=item[1].at;this.tasks.delete(item[0]);item[1].fn();await this.drain();}this.now=target;await this.drain();}}
for(const outage of [40000,90000,120000])test(`recovery survives ${outage/1000}s outage at bounded rate`,async()=>{
 const c=new Clock();let calls=0,peak=0,active=0;const events:string[]=[];
 const loop=new AudioRecoveryLoop(async()=>{active++;peak=Math.max(peak,active);calls++;try{if(c.now<outage)throw Error("network unavailable");}finally{active--;}},()=>false,()=>{throw Error("unexpected denial")},e=>events.push(e),c.timers);
 await c.advance(outage+16000);expect(events.at(-1)).toBe("connected");expect(calls).toBeLessThan(20);expect(peak).toBe(1);expect(c.tasks.size).toBe(0);loop.stop();
});
test("hung attempts abort, online wake coalesces, and stop forbids late recovery",async()=>{
 const c=new Clock();let calls=0,aborts=0;let resolve!:()=>void;const events:string[]=[];
 const loop=new AudioRecoveryLoop(signal=>{calls++;signal.addEventListener("abort",()=>aborts++);return new Promise<void>(r=>resolve=r);},()=>false,()=>{},e=>events.push(e),c.timers);
 await c.advance(1000);loop.wake();loop.wake();await c.advance(1000);expect(calls).toBe(1);
 await c.advance(30000);expect(aborts).toBe(1);loop.stop();resolve();await c.advance(20000);expect(events).not.toContain("connected");expect(calls).toBe(1);expect(c.tasks.size).toBe(0);
});
test("denied access ends recovery and observer failure cannot block attempts",async()=>{
 const c=new Clock();let ended=0,calls=0;const loop=new AudioRecoveryLoop(async()=>{calls++;throw {status:403};},e=>(e as any).status===403,()=>ended++,()=>{throw Error("observer");},c.timers);
 await c.advance(120000);expect(calls).toBe(1);expect(ended).toBe(1);expect(c.tasks.size).toBe(0);loop.stop();
});
