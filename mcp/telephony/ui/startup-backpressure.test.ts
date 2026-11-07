import {expect,test} from 'bun:test';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {SoftphoneSession} from './softphone-audio';

function player(rate=24000) {
 const ctors:Record<string,any>={},events:any[]=[];
 const clock=vm.createContext({Float32Array,sampleRate:rate,currentTime:0,AudioWorkletProcessor:class{port={postMessage(e:any){events.push(e)}}},registerProcessor(n:string,c:any){ctors[n]=c;}});
 vm.runInContext(readFileSync(new URL('./softphone-worklet.js',import.meta.url),'utf8'),clock);
 const p=new ctors['softphone-playback']({processorOptions:{}});
 return {p,clock,events,frame(ms=20,seq=0){p.handleMessage({frame:new Float32Array(Math.round(rate*ms/1000)).fill(.25),sequence:seq});},block(){const out=new Float32Array(128);p.process([],[[out]]);clock.currentTime+=128/rate;return out;}};
}
for(const rate of [24000,44100,48000]) {
 test(`resampler startup residue does not require an extra carrier packet (${rate}Hz)`,()=>{
  const r=player(rate);r.frame(59);
  while(r.clock.currentTime<.065)r.block();
  expect(r.p.playedSamples).toBeGreaterThan(0);
  const start=r.p.playbackEvents.find((e:any)=>e.kind==='playback_start');
  expect(start.reason).toBe('render_quantum_ready');
  expect(start.wait_ms).toBeGreaterThanOrEqual(60);
  expect(start.queue_ms).toBeGreaterThanOrEqual(60-128000/rate);
  expect(r.p.playedSamples+r.p.queued+r.p.droppedSamples).toBe(Math.round(rate*.059));
 });
 test(`startup does not bypass minimum reserve (${rate}Hz)`,()=>{
  const r=player(rate);r.frame();
  while(r.clock.currentTime<.085){expect(r.block().every((n:number)=>n===0)).toBe(true);}
  expect(r.p.playedSamples).toBe(0);expect(r.p.underruns).toBe(0);
  r.frame(40,1);r.block();
  // Arrival jitter may raise the full target; timeout is bounded and requires
  // a real minimum reserve, then all supplied samples remain accounted for.
  while(r.p.playedSamples===0&&r.clock.currentTime<.31)r.block();
  const start=r.p.playbackEvents.find((e:any)=>e.kind==='playback_start');
  expect(start.queue_ms).toBeGreaterThanOrEqual(60-1000/rate);expect(start.wait_ms).toBeLessThanOrEqual(305);
  expect(r.p.playedSamples+r.p.queued+r.p.droppedSamples).toBe(Math.round(rate*.02)+Math.round(rate*.04));
 });
 test(`short startup tail is played without a fabricated startup underrun (${rate}Hz)`,()=>{
  const r=player(rate);r.frame();
  while(r.clock.currentTime<.25)r.block();
  expect(r.p.playedSamples).toBe(Math.round(rate*.02));expect(r.p.droppedSamples).toBe(0);expect(r.p.underruns).toBe(0);
  expect(r.p.playbackEvents.find((e:any)=>e.kind==='playback_start').reason).toBe('short_tail');
  expect(r.p.playbackEvents.find((e:any)=>e.kind==='short_tail_drained')).toBeDefined();
 });
 test(`rebuffer events retain a real live-gap duration (${rate}Hz)`,()=>{
  const r=player(rate);r.frame(80);while(r.clock.currentTime<.16)r.block();
  expect(r.p.underruns).toBe(1);expect(r.p.playbackEvents.some((e:any)=>e.kind==='rebuffer')).toBe(true);
  r.frame(20,1);while(r.clock.currentTime<.3)r.block();
  expect(r.p.underrunSamples).toBeGreaterThan(0);
  r.p.reportStats();expect(r.events.at(-1).playback_events.length).toBeLessThanOrEqual(64);
 });
}

function transport() {
 let now=10000,exited=0;const events:any[]=[],timers:Function[]=[],intervals:Function[]=[],closed:any[]=[];
 class Socket{static OPEN=1;readyState=1;bufferedAmount=0;sent:any[]=[];onopen:any;onmessage:any;onclose:any;send(v:any){this.sent.push(v)}close(...args:any[]){closed.push(args)}}
 const ctx=vm.createContext({Float32Array,ArrayBuffer,DataView,Uint8Array,Int16Array,performance:{timeOrigin:0,now:()=>now},WebSocket:Socket,self:{},postMessage(v:any){events.push(v)},setTimeout(f:Function){timers.push(f);return timers.length;},clearTimeout(){},setInterval(f:Function){intervals.push(f);return 1;},clearInterval(){},close(){exited++}});
 vm.runInContext(readFileSync(new URL('./softphone-worker.js',import.meta.url),'utf8'),ctx);
 const capture:any={postMessage(){},start(){},close(){}},playback={postMessage(){},start(){},close(){}};
 const cmd=(m:any)=>ctx.self.onmessage({data:m});cmd({type:'init',mediaURL:'ws://local',capturePort:capture,playbackPort:playback,contextRate:24000,audioClockMS:0,monotonicEpochMS:now});
 const socket:any=vm.runInContext('socket',ctx);socket.onopen();cmd({type:'microphone.ready',value:true});
 return {events,timers,intervals,closed,socket,cmd,exited:()=>exited,setNow(n:number){now=n},frame(t=0,sequence=1){capture.onmessage({data:{type:'capture',timestamp_ms:t,sequence,sample_rate:24000,frame:new Float32Array(480).fill(.2)}})}};
}
test('intentional shutdown advertises intent and waits for a normal close handshake',()=>{
 const t=transport();t.cmd({type:'close',reason:'user_stop'});
 expect(t.closed).toEqual([[1000,'client_shutdown']]);
 expect(JSON.parse(t.socket.sent.at(-1))).toEqual({type:'media.shutdown',reason:'user_stop'});
 expect(t.exited()).toBe(0);t.socket.onclose({code:1000});expect(t.exited()).toBe(1);
 t.timers.at(-1)!();expect(t.exited()).toBe(1);
 expect(t.events.some(e=>e.type==='socket.reconnect')).toBe(false);
});
test('backpressure keeps the limit and records queue buildup, loss and drain with independent age',()=>{
 const t=transport();t.socket.bufferedAmount=7440;t.frame();
 const drop=t.events.find(e=>e.type==='transport.drop').event;
 expect(drop.duration_ms).toBe(20);expect(drop.queue_before_ms).toBe(155);
 expect(drop.frame_age_ms).toBe(20);expect(drop.worker_delay_ms).toBe(0);expect(drop.queue_bytes).toBe(7440);
 expect(t.socket.sent.filter((e:any)=>e instanceof ArrayBuffer)).toHaveLength(0);
 t.setNow(10040);t.socket.bufferedAmount=0;t.frame(40,2);t.intervals[0]();
 const stats=t.events.findLast(e=>e.type==='transport.stats');
 expect(stats.capture_queue_events.map((e:any)=>e.kind)).toContain('queue_buildup');
 expect(stats.capture_queue_events.map((e:any)=>e.kind)).toContain('queue_drained');
 expect(stats.timing.capture_dropped_ms).toBe(20);expect(t.socket.sent.filter((e:any)=>e instanceof ArrayBuffer)).toHaveLength(1);
 for(let i=0;i<500;i++){t.setNow(10040+i*300);t.socket.bufferedAmount=i%2?0:7440;t.frame(i*300+40,i+3)}
 t.intervals[0]();expect(t.events.findLast(e=>e.type==='transport.stats').capture_queue_events.length).toBeLessThanOrEqual(64);
});
test('shared headless cleanup retains its bounded handshake and observation callbacks',()=>{
 const session:any=new SoftphoneSession(),sent:any[]=[];let terminated=0;
 const worker:any={postMessage(v:any){sent.push(v)},terminate(){terminated++}};session.worker=worker;session.stop();
 expect(sent.find(e=>e.type==='close').reason).toBe('user_stop');expect(terminated).toBe(0);
 worker.onmessage({data:{type:'socket.shutdown.complete'}});expect(terminated).toBe(1);
});
test('headless observations send bounded deltas while preserving local callback history',()=>{
 const s:any=new SoftphoneSession(),wire:any[]=[];s.mediaSocketConnected=true;s.sendText=(v:string)=>wire.push(JSON.parse(v));
 const e={id:'playback:1',timestamp:new Date().toISOString(),kind:'playback_start',reason:'target_ready',queue_ms:60};
 s.diagnostics.playbackEvents=[e];s.sendDiagnostics();s.sendDiagnostics();
 expect(wire[0].diagnostics.playback_events).toEqual([e]);expect(wire[1].diagnostics.playback_events).toEqual([]);
 expect(s.diagnostics.playbackEvents).toEqual([e]);
 s.reportedPlaybackIDs.clear();s.sendDiagnostics();expect(wire[2].diagnostics.playback_events).toEqual([e]);
 s.diagnostics.playbackEvents=Array.from({length:64},(_,i)=>({...e,id:`p:${i}`}));
 s.sendDiagnostics();s.sendDiagnostics();
 expect(wire[3].diagnostics.playback_events).toHaveLength(8);expect(wire[4].diagnostics.playback_events).toHaveLength(8);
 expect(wire[3].diagnostics.playback_events[0].id).not.toBe(wire[4].diagnostics.playback_events[0].id);
});
test('short-tail completion cannot suppress a later real live underrun',()=>{
 const r=player();r.frame(20);while(r.clock.currentTime<.12)r.block();
 r.frame(80,1);while(r.clock.currentTime<.5)r.block();
 expect(r.p.underruns).toBe(1);expect(r.p.underrunSamples).toBeGreaterThan(0);
 expect(r.p.playedSamples+r.p.queued+r.p.droppedSamples).toBe(2400);
});

test('interval transport observations measure wire rates and rejected-frame delay before discard without changing PCM',()=>{
 const t=transport();t.socket.onmessage({data:JSON.stringify({type:'media.capabilities',version:3})});
 function packet(seq:number,sent:number){const data=new ArrayBuffer(64+960),v=new DataView(data);v.setUint32(0,0x33545041,true);v.setUint32(4,seq,true);v.setFloat64(16,sent,true);v.setFloat64(24,5,true);v.setFloat64(48,sent,true);return data;}
 t.socket.onmessage({data:packet(1,10000)});
 t.setNow(17230);t.socket.onmessage({data:packet(2,10020)});
 t.frame(7230,1);t.intervals[0]();
 const stats=t.events.findLast(e=>e.type==='transport.stats');
 expect(stats.observation.window_ms).toBe(7230);expect(stats.observation.receive_gap_ms).toBe(7230);expect(stats.observation.delivery_excess_ms).toBe(7210);
 expect(stats.observation.server_queue_ms).toBe(5);expect(stats.observation.receive_bitrate_bps).toBeCloseTo(2048*8000/7230,4);
 expect(stats.timing.playback_transport_dropped_ms+stats.timing.playback_source_dropped_ms).toBe(20);
 // Repeat reports within the same interval must not create inflated rates.
 t.cmd({type:'send.text',data:'{}'});expect(t.events.findLast(e=>e.type==='transport.stats').observation).toBeUndefined();
 t.setNow(18230);t.intervals[0]();const empty=t.events.findLast(e=>e.type==='transport.stats').observation;
 expect(empty.receive_bitrate_bps).toBe(0);expect(empty.delivery_excess_ms).toBeNull();
});

 test('monitoring pieces skip a backed-up socket without delaying capture or changing its limits',()=>{
  const t=transport();t.socket.bufferedAmount=2000;t.cmd({type:'send.telemetry',data:'diagnostic-part'});
  expect(t.socket.sent).not.toContain('diagnostic-part');t.frame();expect(t.socket.sent.some((v:any)=>v instanceof ArrayBuffer)).toBe(true);
  t.socket.bufferedAmount=0;t.cmd({type:'send.telemetry',data:'diagnostic-part'});expect(t.socket.sent).toContain('diagnostic-part');
 });
