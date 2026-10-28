// Regressions for long-call clock skew, render-clock probe starvation and
// playback target recovery after a single delivery stall.
import {expect,test} from 'bun:test';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {simulateAdaptivePlayback} from '../benchmarks/softphone/adaptive-playback';

function worker() {
 let now=10000;const messages:any[]=[];const sockets:any[]=[];const intervals:Function[]=[];
 const port=()=>({postMessage(){},start(){},close(){},onmessage:null as any});
 const capturePort=port(),playbackPort=port();const received:any[]=[];
 playbackPort.postMessage=(x:any)=>received.push(x);
 class Socket { static OPEN=1;readyState=1;bufferedAmount=0;sent:any[]=[];onopen:any;onmessage:any;onclose:any;constructor(public url:string){sockets.push(this)}send(x:any){this.sent.push(x)}close(){} }
 const context=vm.createContext({ArrayBuffer,DataView,Uint8Array,Int16Array,Float32Array,performance:{timeOrigin:0,now:()=>now},WebSocket:Socket,postMessage:(x:any)=>messages.push(x),self:{},setInterval:(f:Function)=>{intervals.push(f);return intervals.length;},clearInterval(){},setTimeout(){return 0;},clearTimeout(){},close(){}});
 vm.runInContext(readFileSync(new URL('./softphone-worker.js',import.meta.url),'utf8'),context);
 const command=(x:any)=>context.self.onmessage({data:x});
 command({type:'init',mediaURL:'ws://local',capturePort,playbackPort,contextRate:24000,audioClockMS:0,monotonicEpochMS:now});
 sockets[0].onopen();received.length=0;command({type:'microphone.ready',value:true});
 const frame=(timestamp:number,sequence=1)=>capturePort.onmessage({data:{type:'capture',frame:new Float32Array(480).fill(.2),timestamp_ms:timestamp,sequence,sample_rate:24000}});
 const reply=(nonce:number,audio:number)=>capturePort.onmessage({data:{type:'clock.reply',nonce,audio_ms:audio}});
 const sent=()=>sockets[0].sent.filter((x:any)=>x instanceof ArrayBuffer).length;
 return {command,frame,reply,sent,socket:sockets[0],messages,received,intervals,setNow:(n:number)=>{now=n;}};
}
function sourcePlayback(sourceClock:number,sequence:number) {
 const f=new ArrayBuffer(64+960),v=new DataView(f);
 v.setUint32(0,0x33545041,true);v.setUint32(4,sequence,true);
 v.setFloat64(16,sourceClock,true);v.setFloat64(32,sourceClock,true);
 v.setBigUint64(40,BigInt(sequence),true);v.setFloat64(48,sourceClock,true);v.setUint32(56,1,true);
 return f;
}

test('100ppm server/browser skew over two hours never turns into playback delivery excess',()=>{
 const w=worker();w.socket.onmessage({data:JSON.stringify({type:'media.capabilities',version:3})});
 // The browser clock runs 100ppm fast: 720ms of accumulated skew after 2h.
 // One frame per 200ms keeps the simulation small; the floor is time-based.
 let sequence=0;
 for(let source=100;source<=2*3600*1000;source+=200) {w.setNow(10000+source*1.0001);w.socket.onmessage({data:sourcePlayback(source,sequence++)});}
 w.intervals[0]();const timing=w.messages.findLast((m:any)=>m.type==='transport.stats').timing;
 expect(w.messages.filter((m:any)=>m.type==='transport.drop')).toHaveLength(0);
 expect(w.received).toHaveLength(sequence);
 expect(timing.playback_max_delivery_excess_ms).toBeLessThan(10);
 // A real sustained delay late in the call is still detected and rejected.
 const late=7200000+400;w.setNow(10000+late*1.0001+400);w.socket.onmessage({data:sourcePlayback(late,sequence++)});
 expect(w.messages.findLast((m:any)=>m.type==='transport.drop').event.reason).toBe('playback_delivery_excess');
},30000);

test('render-clock probes slower than 50ms still calibrate capture instead of muting the microphone',()=>{
 const w=worker();w.command({type:'clock.reset',paused:false});
 w.setNow(10100);w.frame(100);expect(w.messages.at(-1).event.reason).toBe('capture_clock_unavailable');
 // Every probe takes 120ms to return; previously none was ever accepted.
 for(let i=0;i<10;i++) {
  const at=11000+i*1000;w.setNow(at);w.reply(at-120,at-60-10000);w.frame(at-10000,2+i);
 }
 expect(w.sent()).toBe(10);
 expect(w.messages.filter((m:any)=>m.type==='transport.drop'&&m.event.reason==='capture_clock_unavailable')).toHaveLength(1);
 // The age cap still applies with the probe's measured uncertainty (70ms).
 w.setNow(21000);w.frame(21000-10000-400,20);expect(w.messages.at(-1).event.reason).toBe('capture_age_limit');
});

test('a minute without any render-clock reply keeps capture flowing and reports the stale clock',()=>{
 const w=worker();
 for(let i=0;i<3000;i++) {w.setNow(10000+i*20);w.frame(i*20,i+1);}
 expect(w.sent()).toBe(3000);
 w.intervals[0]();const timing=w.messages.findLast((m:any)=>m.type==='transport.stats').timing;
 expect(timing.capture_clock_stale_ms).toBeGreaterThan(50000);expect(timing.drop_totals_ms.capture_clock_unavailable).toBeUndefined();
 expect(w.messages.filter((m:any)=>m.type==='runtime.event'&&m.event.action==='audio_clock'&&m.event.outcome==='stale')).toHaveLength(1);
 // The uncertainty grows, but grossly stale audio is still discarded.
 w.setNow(70000);w.frame(70000-10000-1000,5000);expect(w.messages.at(-1).event.reason).toBe('capture_age_limit');
});

const targetAt=(r:any,ms:number)=>r.reserve_samples.find((s:any)=>s.at_ms>=ms);
const schedule=(gap:(i:number)=>number)=>Array.from({length:600},(_,i)=>gap(i));

test('one 250ms stall returns playback latency near baseline within a minute',()=>{
 // Stall at 5s; one-second periods; nothing else late.
 const r=simulateAdaptivePlayback({name:'single-stall',seconds:120,gaps:schedule(i=>i===0?250:0),periodMS:1000,firstGapMS:5000,ceiling:280,adaptive:true});
 expect(targetAt(r,10000).target_ms).toBe(280);
 expect(targetAt(r,65000).target_ms).toBeLessThanOrEqual(80);
 expect(targetAt(r,65000).queue_ms).toBeLessThan(120);
 expect(r.late_underruns).toBe(0);expect(r.dropped_ms).toBe(0);expect(r.accounted_samples).toBe(r.sent_samples);
});

test('a stall plus 30ms late frames every 22s no longer pins the target at 280ms',()=>{
 // Previously the target stayed at 280ms for the full ten minutes.
 const r=simulateAdaptivePlayback({name:'stall-jitter',seconds:600,gaps:schedule(i=>i===0?250:i%22===0?30:0),periodMS:1000,firstGapMS:5000,ceiling:280,adaptive:true});
 expect(r.reserve_samples.filter((s:any)=>s.at_ms>65000).every((s:any)=>s.target_ms<=110)).toBe(true);
 expect(r.late_underruns).toBe(0);expect(r.dropped_ms).toBe(0);
},30000);

test('recurring real jitter keeps the learned cushion',()=>{
 const r=simulateAdaptivePlayback({name:'recurring-80',seconds:300,gaps:schedule(i=>i%5===0?80:0),periodMS:1000,firstGapMS:5000,ceiling:280,adaptive:true});
 expect(r.reserve_samples.filter((s:any)=>s.at_ms>20000).every((s:any)=>s.target_ms>=140)).toBe(true);
 expect(r.late_underruns).toBe(0);expect(r.dropped_ms).toBe(0);
},30000);
