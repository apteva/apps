import {expect,test} from "bun:test";
import {AudioRuntimeTelemetry} from "../../ui/audio-runtime-telemetry";

test("main thread scheduling delay is timestamped and does not change audio",()=>{
 let now=0;const events:any[]=[];
 const t=new AudioRuntimeTelemetry(e=>events.push(e),()=>now,()=>"2026-10-06T10:00:00Z");
 t.tick();now=1000;t.tick();now=2500;t.tick();now=3500;t.tick();
 expect(events).toEqual([{timestamp:"2026-10-06T10:00:00Z",action:"main_thread",outcome:"scheduling_gap",duration_ms:500}]);
 expect(t.counters.main_thread_pause_count).toBe(1);expect(t.counters.main_thread_max_pause_ms).toBe(500);
});
test("AudioContext suspension and interruption share a duration and resume once",()=>{
 let now=0;const events:any[]=[];const t=new AudioRuntimeTelemetry(e=>events.push(e),()=>now);
 t.context("running");now=10;t.context("suspended");now=20;t.context("suspended");now=40;t.context("interrupted");now=3010;t.context("running");
 expect(t.counters.audio_context_suspend_count).toBe(1);expect(t.counters.audio_context_suspended_ms).toBe(3000);
 expect(events.map(e=>e.outcome)).toEqual(["running","suspended","interrupted","running"]);
 expect(events.at(-1).duration_ms).toBe(3000);
});
test("background timer delay is an observation and observer errors are isolated",()=>{
 let now=0;const t=new AudioRuntimeTelemetry(()=>{throw Error("host observer")},()=>now);
 t.tick();now=60000;expect(()=>t.tick()).not.toThrow();expect(t.counters.main_thread_max_pause_ms).toBe(59000);
 expect(()=>t.context("suspended")).not.toThrow();
});

test("inactive playback cannot announce restored audio; health never ends the call", async()=>{
 const {SoftphoneSession}=await import("../../ui/softphone-audio");
 const notices:string[]=[],health:any[]=[],states:any[]=[];
 const session:any=new SoftphoneSession({onNotice:s=>notices.push(s),onAudioHealth:h=>health.push(h),onState:s=>states.push(s)});
 session.ctx={state:"running"};
 const report=(state:string,playback:string)=>session.handleControl(JSON.stringify({type:"audio.health",state,reason:state==='audio_degraded'?'audio_degraded':undefined,stages:{telephony_to_browser:{state:playback}}}));
 report("audio_degraded","audio_degraded");session.ctx.state="suspended";report("healthy","inactive");
 expect(notices).toHaveLength(1);expect(states).toHaveLength(0);expect(session.closed).toBe(false);
 session.ctx.state="running";report("healthy","healthy");report("healthy","healthy");
 expect(notices).toEqual(["Speech delivery is interrupted. The call remains connected.","Speech delivery restored."]);
 expect(health).toHaveLength(4);
});
