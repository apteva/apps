import {expect, test} from "bun:test";
import {readFileSync} from "node:fs";
import vm from "node:vm";

function playback(rate:number) {
 const ctors:Record<string,any>={};
 const clock=vm.createContext({sampleRate:rate,currentTime:0,AudioWorkletProcessor:class{port={postMessage(){}}},registerProcessor(n:string,c:any){ctors[n]=c;}});
 vm.runInContext(readFileSync(new URL('./softphone-worklet.js',import.meta.url),'utf8'),clock);
 return {clock,p:new ctors['softphone-playback']({processorOptions:{}})};
}

// Real worklet, ordered transport catch-up, exact sample accounting. Frame
// markers identify source age independently of the worklet's queue counters.
function simulate(rate:number,stall:number,duration=180000,variable=false) {
 const {p,clock}=playback(rate);
 let sent=0,frames=0,maxAge=0,maxQueue=0,rendered=0;
 const out=new Float32Array(128);
 for(let samples=0;samples<duration*rate/1000;samples+=128) {
  const now=samples*1000/rate; clock.currentTime=samples/rate;
  while(sent*1000/rate<=now) {
   const sourceMS=sent*1000/rate;
   const base=Math.floor(sourceMS/30000)*30000;
   // Repeat moderate jitter every 3s; long outage every 30s.
   const start=stall<500 ? Math.floor(sourceMS/3000)*3000+1000 : base+1000;
   if(sourceMS>=start && sourceMS<start+stall && now<start+stall) break;
   const size=Math.round(rate*(variable?[10,20,30][frames%3]:20)/1000);
   p.handleMessage({frame:new Float32Array(size).fill(sourceMS+1),sequence:frames++});
   sent+=size;
  }
  maxQueue=Math.max(maxQueue,p.queued*1000/rate);
  p.process([],[[out]]);
  for(const v of out) if(v!==0) {rendered++;maxAge=Math.max(maxAge,now-(v-1));}
 }
 expect(p.playedSamples+p.droppedSamples+p.queued).toBe(sent);
 expect(maxQueue).toBeLessThanOrEqual(320);
 expect(p.maxResidenceMS).toBeLessThanOrEqual(320);
 return {dropped:p.droppedSamples*1000/rate,underruns:p.underruns,maxAge,maxQueue,rendered,target:p.targetMs};
}

for(const rate of [24000,44100,48000]) {
 test(`steady ${rate} Hz and variable packets preserve every sample`,()=>{
  for(const variable of [false,true]) {
   const s=simulate(rate,0,60000,variable);
   expect(s.dropped).toBe(0);expect(s.underruns).toBe(0);expect(s.maxAge).toBeLessThan(100);
  }
 });
 test(`repeated jitter ${rate} Hz stays bounded without trimming moderate bursts`,()=>{
  for(const stall of [40,80,120,200,250]) {
   const s=simulate(rate,stall);
   expect(s.dropped).toBe(0);
   expect(s.maxAge).toBeLessThan(320);
   expect(s.underruns).toBeLessThanOrEqual(5);
  }
 });
 test(`500ms and 6.7s outage ${rate} Hz recover to live audio`,()=>{
  for(const stall of [500,6700]) {
   const s=simulate(rate,stall,90000);
   expect(s.dropped).toBeGreaterThan(0);
   // Crossfade includes 5ms of the previous tail, whose source time can
   // predate the outage; inspect queue residence and sample conservation.
   expect(s.target).toBeLessThanOrEqual(160);
  }
 });
 test(`steady PCM is bit-exact at ${rate} Hz`,()=>{
  const {clock,p}=playback(rate);
  let inputIndex=0,outputIndex=0;
  for(let sample=0;sample<rate*3;sample+=128) {
   clock.currentTime=sample/rate;
   while(inputIndex<=sample) {
    const frame=Float32Array.from({length:Math.round(rate/50)},(_,i)=>0.25*Math.sin((inputIndex+i)*2*Math.PI*440/rate));
    p.handleMessage({frame});inputIndex+=frame.length;
   }
   const out=new Float32Array(128); const before=p.playedSamples;
   p.process([],[[out]]);
   if(p.playedSamples>before) for(const value of out) {
    expect(value).toBe(Math.fround(0.25*Math.sin(outputIndex++*2*Math.PI*440/rate)));
   }
  }
  expect(p.droppedSamples).toBe(0);expect(p.underruns).toBe(0);
 });
}

test('30-minute conversation does not accumulate delay',()=>{
 const s=simulate(24000,200,30*60*1000,true);
 expect(s.dropped).toBe(0);expect(s.maxAge).toBeLessThan(320);expect(s.underruns).toBeLessThanOrEqual(5);
 console.log('30-minute simulated call:',JSON.stringify(s));
},30000);

test('delayed playback MessagePort frames expire and flush clears adaptation epoch',()=>{
 const {clock,p}=playback(24000);
 clock.currentTime=10;
 p.handleMessage({frame:new Float32Array(480).fill(.5),sequence:1,received_audio_ms:1000,clock_uncertainty_ms:10});
 const out=new Float32Array(128);p.process([],[[out]]);
 expect(out.every(x=>x===0)).toBe(true);
 expect(p.dropTotals.playback_age_limit).toBe(480);
 p.stableSamples=24000*29;p.targetMs=160;
 p.handleMessage({type:'flush'});
 expect(p.targetMs).toBe(60);expect(p.stableSamples).toBe(0);expect(p.expectedSequence).toBe(null);
});

function worker() {
 let now=10000;const messages:any[]=[];const sockets:any[]=[];const intervals:Function[]=[];
 const port=()=>({postMessage(){},start(){},close(){},onmessage:null as any});
 const capturePort=port(),playbackPort=port();const received:any[]=[];
 playbackPort.postMessage=(x:any)=>received.push(x);
 class Socket { static OPEN=1;readyState=1;bufferedAmount=0;sent:any[]=[];onopen:any;onmessage:any;onclose:any;constructor(){sockets.push(this)}send(x:any){this.sent.push(x)}close(){} }
 const context=vm.createContext({ArrayBuffer,DataView,Uint8Array,Int16Array,Float32Array,performance:{timeOrigin:0,now:()=>now},WebSocket:Socket,postMessage:(x:any)=>messages.push(x),self:{},setInterval:(f:Function)=>{intervals.push(f);return intervals.length;},clearInterval(){},setTimeout(){},clearTimeout(){},close(){}});
 vm.runInContext(readFileSync(new URL('./softphone-worker.js',import.meta.url),'utf8'),context);
 const command=(x:any)=>context.self.onmessage({data:x});
 command({type:'init',mediaURL:'ws://local',capturePort,playbackPort,contextRate:24000,audioClockMS:0,monotonicEpochMS:now});
 sockets[0].onopen();command({type:'microphone.ready',value:true});
 const frame=(timestamp:number,sequence=1)=>capturePort.onmessage({data:{type:'capture',frame:new Float32Array(480).fill(.2),timestamp_ms:timestamp,sequence,sample_rate:24000}});
 return {context,command,frame,socket:sockets[0],messages,received,intervals,setNow:(n:number)=>{now=n;}};
}

test('worker discards stale capture after a pause and retains current frames',()=>{
 const w=worker();w.setNow(11000);w.frame(0);
 expect(w.messages.at(-1).event.reason).toBe('capture_age_limit');
 expect(w.socket.sent.filter((x:any)=>x instanceof ArrayBuffer)).toHaveLength(0);
 w.frame(1000,2);
 expect(w.socket.sent.filter((x:any)=>x instanceof ArrayBuffer)).toHaveLength(1);
 w.socket.bufferedAmount=6000;w.frame(1020,3);
 expect(w.messages.at(-1).event.reason).toBe('websocket_backpressure');
});

test('new worker falls back to APT1 on old server; negotiates timing on new server',()=>{
 const w=worker();w.frame(0);
 expect(new DataView(w.socket.sent.at(-1)).getUint32(0,true)).toBe(0x31545041);
 w.socket.onmessage({data:JSON.stringify({type:'media.capabilities',version:2})});w.frame(20);
 const capture=new DataView(w.socket.sent.at(-1));
 expect(capture.getUint32(0,true)).toBe(0x32545041);expect(capture.getFloat64(8,true)).toBe(20);
 const makeFrame=(seq:number)=>{const f=new ArrayBuffer(32+960);const v=new DataView(f);v.setUint32(0,0x32545041,true);v.setUint32(4,seq,true);return f;};
 w.socket.onmessage({data:makeFrame(5)});w.socket.onmessage({data:makeFrame(8)});
 expect(w.received.at(-1).sequence).toBe(8);
 w.intervals[0]();expect(w.messages.findLast((m:any)=>m.type==='transport.stats').timing.playback_sequence_gaps).toBe(2);
});

test('mute and reconnect gates do not replay earlier capture',()=>{
 const w=worker();w.command({type:'muted',value:true});w.frame(0);
 expect(w.socket.sent.filter((x:any)=>x instanceof ArrayBuffer)).toHaveLength(0);
 w.setNow(10100);w.command({type:'muted',value:false});w.frame(0);
 expect(w.messages.at(-1).event.reason).toBe('capture_age_limit');
 w.frame(100);expect(w.socket.sent.filter((x:any)=>x instanceof ArrayBuffer)).toHaveLength(1);
});

function timedPlayback(sent:number,sequence=1) {
 const frame=new ArrayBuffer(32+960), view=new DataView(frame);
 view.setUint32(0,0x32545041,true);view.setUint32(4,sequence,true);view.setFloat64(16,sent,true);
 new Int16Array(frame,32).fill(1234);
 return frame;
}
test('network age guard uses clock uncertainty, never RTT/2 as exact one-way delay',()=>{
 const w=worker();w.socket.onmessage({data:JSON.stringify({type:'media.capabilities',version:2})});
 w.setNow(10020);w.socket.onmessage({data:JSON.stringify({type:'media.clock',nonce:10000,received_ms:100,sent_ms:100})});
 w.setNow(11000);w.socket.onmessage({data:timedPlayback(110)});
 expect(w.received).toHaveLength(0); // 980ms estimate minus 10ms uncertainty is stale.
 w.socket.onmessage({data:timedPlayback(1080,2)});
 expect(w.received).toHaveLength(1); // Current speech survives.
 expect(w.received[0].frame[0]).toBeCloseTo(1234/32768,6);
 const uncertain=worker();uncertain.socket.onmessage({data:JSON.stringify({type:'media.capabilities',version:2})});
 uncertain.setNow(10800);uncertain.socket.onmessage({data:JSON.stringify({type:'media.clock',nonce:10000,received_ms:100,sent_ms:100})});
 uncertain.setNow(11000);uncertain.socket.onmessage({data:timedPlayback(200)});
 expect(uncertain.received).toHaveLength(1); // 500ms estimate ±400ms cannot establish staleness.
});
test('expired clock estimates do not cause false network-age drops',()=>{
 const w=worker();w.socket.onmessage({data:JSON.stringify({type:'media.capabilities',version:2})});
 w.setNow(10020);w.socket.onmessage({data:JSON.stringify({type:'media.clock',nonce:10000,received_ms:100,sent_ms:100})});
 w.setNow(30000);w.socket.onmessage({data:timedPlayback(110)});
 expect(w.received).toHaveLength(1);
});
