import {expect, test} from "bun:test";
import {readFileSync} from "node:fs";
import vm from "node:vm";
import {endPlaybackObservation, mergePlaybackUnderruns, SoftphoneSession, type PlaybackUnderrunEvent} from "./softphone-audio";

function renderer(rate=24000, enabled=true, source=readFileSync(new URL('./softphone-worklet.js',import.meta.url),'utf8')) {
  const processors:Record<string,any>={}, messages:any[]=[];
  let wall=Date.parse('2026-10-08T10:00:00Z');
  class ClockDate extends Date {static now(){return wall;}}
  const clock=vm.createContext({Date:ClockDate,Float32Array,sampleRate:rate,currentTime:0,AudioWorkletProcessor:class{port={postMessage(v:any){messages.push(v);}}},registerProcessor(n:string,c:any){processors[n]=c;}});
  vm.runInContext(source,clock);
  const p=new processors['softphone-playback']({processorOptions:{telemetryEnabled:enabled,telemetryEpoch:'test'}});
  return {p,clock,messages,jumpWall(n:number){wall+=n;},block(n=128){const out=new Float32Array(n);p.process([],[[out]]);clock.currentTime+=n/rate;return out;}};
}
for (const rate of [24000,44100,48000]) {
  test(`underrun ${rate} Hz: first missing sample, rebuffering and recovery are exact`,()=>{
    const r=renderer(rate);
    r.p.handleMessage({frame:new Float32Array(100).fill(.25),sequence:7});r.p.playing=true;
    const out=r.block();expect(Array.from(out.slice(0,100))).toEqual(Array(100).fill(.25));
    r.block();r.block();
    r.p.reportStats();const ongoing=r.messages.at(-1).underrun_events[0];
    expect(ongoing.missing_samples).toBe(284);expect(ongoing.duration_ms).toBeCloseTo(284000/rate,9);
    expect(ongoing.start_audio_ms).toBeCloseTo(100000/rate,9);expect(ongoing.last_sequence).toBe(7);
    expect(Date.parse(ongoing.started_at)).toBe(Date.parse('2026-10-08T10:00:00Z')+Math.floor(100000/rate));
    r.p.handleMessage({frame:new Float32Array(Math.round(rate*.08)).fill(.5),sequence:8});r.block();
    const end=r.messages.findLast(v=>v.type==='playback.underrun').event;
    expect(end.end_reason).toBe('recovered');expect(end.complete).toBe(true);expect(end.resume_sequence).toBe(8);
    expect(end.duration_ms).toBeCloseTo(284000/rate,9);expect(end.end_audio_ms).toBeCloseTo(384000/rate,9);
    expect(r.messages.filter(v=>v.type==='playback.underrun')).toHaveLength(2);
  });
  test(`underrun ${rate} Hz: startup and supplied silence produce no gap`,()=>{
    const r=renderer(rate);for(let i=0;i<20;i++)r.block();expect(r.p.underrunEvents).toHaveLength(0);
    // Keep supplying silence-valued audio: silence is data, not missing media.
    for(let i=0;i<100;i++){r.p.handleMessage({frame:new Float32Array(128),sequence:i});r.block();}
    expect(r.p.underrunEvents).toHaveLength(0);
  });
}
test('wall clock jumps cannot inflate missing sample duration; pauses/flush do not create gaps',()=>{
  const r=renderer();r.p.handleMessage({frame:new Float32Array(100),sequence:1});r.p.playing=true;r.block();
  r.jumpWall(3600000);r.block();r.p.reportStats();
  const gap=r.messages.at(-1).underrun_events[0];expect(gap.duration_ms).toBe(6.5);
  expect(Date.parse(gap.observed_until)-Date.parse(gap.started_at)).toBeLessThan(10);
  r.p.handleMessage({type:'playback.telemetry.boundary',active:false,reason:'audio_context_paused',audio_time_ms:r.clock.currentTime*1000});
  const samples=r.p.underrunSamples;for(let i=0;i<50;i++)r.block();expect(r.p.underrunSamples).toBe(samples);
  expect(r.p.underrunEvents[0].end_reason).toBe('audio_context_paused');
  r.p.handleMessage({type:'playback.telemetry.boundary',active:true});r.block();
  expect(r.p.underrunEvents).toHaveLength(2);expect(r.p.underrunEvents[1].id).not.toBe(r.p.underrunEvents[0].id);
  expect(r.p.underrunSamples).toBe(samples+128);
  r.p.handleMessage({type:'flush'});
  for(let i=0;i<20;i++)r.block();expect(r.p.underrunEvents).toHaveLength(2);
});
test('underrun history is capped at 100 and completed snapshots deduplicate',()=>{
  const r=renderer();for(let i=0;i<105;i++){r.p.handleMessage({frame:new Float32Array(100),sequence:i});r.p.playing=true;r.block();r.p.handleMessage({type:'flush'});}
  expect(r.p.underrunEvents).toHaveLength(100);expect(r.p.underrunSamples).toBe(105*28);r.p.reportStats();
  const events=r.messages.at(-1).underrun_events;expect(mergePlaybackUnderruns(events,events)).toHaveLength(100);
  const incomplete=endPlaybackObservation([{...events[0],ended_at:undefined,complete:false}], 'observation_ended')[0];
  expect(incomplete.ended_at).toBe(incomplete.observed_until);expect(incomplete.duration_ms).toBe(events[0].duration_ms);expect(incomplete.complete).toBe(false);
  const stale={...incomplete,ended_at:undefined,end_reason:'ongoing'};
  expect(mergePlaybackUnderruns([incomplete],[stale])[0].ended_at).toBe(incomplete.ended_at);
});
test('enabling telemetry produces bit-exact playback under jitter and long stalls',()=>{
  for(const rate of [24000,44100,48000]){
    const instrumented=renderer(rate), baseline=renderer(rate,false);
    let next=0,sequence=0;
    for(let block=0;block<12000;block++){
      const now=block*128/rate;
      while(next<=now && !(now>1 && now<2) && !(now>4 && now<4.08)){
        const frame=new Float32Array(Math.round(rate*.02));for(let k=0;k<frame.length;k++)frame[k]=Math.sin((sequence*frame.length+k)*.1)*.2;
        instrumented.p.handleMessage({frame:frame.slice(),sequence});baseline.p.handleMessage({frame:frame.slice(),sequence});next+=.02;sequence++;
      }
      expect(instrumented.block()).toEqual(baseline.block());
    }
    expect(instrumented.p.underruns).toBe(baseline.p.underruns);expect(instrumented.p.droppedSamples).toBe(baseline.p.droppedSamples);expect(instrumented.p.targetMs).toBe(baseline.p.targetMs);
  }
});
test('headless diagnostics forward gap events and hold never changes microphone readiness',()=>{
  const session:any=new SoftphoneSession();const wire:any[]=[], boundaries:any[]=[];
  session.capture={port:{postMessage(){}}};session.playback={port:{postMessage(v:any){boundaries.push(v);}}};session.sendText=(v:string)=>wire.push(JSON.parse(v));
  session.ctx={state:'running',currentTime:1};session.microphoneTransportReady=true;
  session.installWorkletDiagnostics();
  const event:PlaybackUnderrunEvent={id:'epoch:1',started_at:'2026-10-08T10:00:00Z',observed_until:'2026-10-08T10:00:00.020Z',duration_ms:20,missing_samples:480,sample_rate:24000,start_audio_ms:0,end_audio_ms:20,end_reason:'ongoing',complete:false,timestamp_basis:'browser_wall_audio_clock'};
  session.playback.port.onmessage({data:{type:'playback.underrun',event,underruns:1,underrun_ms:20}});session.sendDiagnostics();
  expect(wire.at(-1).diagnostics.playback_underrun_events).toEqual([event]);expect(wire.at(-1).diagnostics.playback_underrun_ms).toBe(20);
  session.handleControl(JSON.stringify({type:'call.status',call_id:'call',status:'answered',hold_state:'held'}));
  expect(session.microphoneTransportReady).toBe(true);expect(boundaries.at(-1).active).toBe(false);expect(session.diagnostics.underrunEvents[0].complete).toBe(false);
  session.handleControl(JSON.stringify({type:'peer.connected'}));expect(boundaries.at(-1).active).toBe(false);
  session.handleControl(JSON.stringify({type:'call.status',call_id:'call',status:'answered',hold_state:'active'}));expect(boundaries.at(-1).active).toBe(true);
});

test('call ending is distinct from a live gap and does not invent a recovery',()=>{
 const session:any=new SoftphoneSession();const r=renderer();
 r.p.handleMessage({frame:new Float32Array(100),sequence:1});r.p.playing=true;r.block();
 r.p.reportStats();session.diagnostics.underrunEvents=r.messages.at(-1).underrun_events;
 session.teardown=()=>{};
 session.handleControl(JSON.stringify({type:'call.ended',call_id:'call'}));
 expect(session.diagnostics.underrunEvents[0].end_reason).toBe('call_ended');
 expect(session.diagnostics.underrunEvents[0].complete).toBe(false);
});
