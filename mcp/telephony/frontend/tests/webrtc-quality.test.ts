import {expect,test} from "bun:test";
import {RTCQualityPolicy,RTCControlBudget} from "../src/webrtc-quality";
import {rtcStatistics,rtcLossEvents,WebRTCAudioConnection} from "../src/webrtc-audio";
import {audioWidgetMeasurements,audioWidgetProblems} from "../../ui/audio-widget";

test("WebRTC reserve adapts modestly, remains bounded, recovers slowly and respects opt-out",()=>{
 const q=new RTCQualityPolicy();expect(q.target).toBe(60);
 for(let t=0;t<=20000;t+=2000)q.observe({receiver_jitter:.1,receiver_concealed_delta_ms:20},t);
 expect(q.target).toBe(160);
 for(let t=22000;t<=140000;t+=2000)q.observe({receiver_jitter:.001},t);
 expect(q.target).toBe(60);
 const fixed=new RTCQualityPolicy({playbackAdaptive:false,playbackTargetMs:100});
 fixed.observe({receiver_jitter:.2,receiver_concealed_delta_ms:200},0);expect(fixed.target).toBe(100);
});

test("RTC monitoring has a byte budget and cannot grow a large socket write",()=>{
 const b=new RTCControlBudget();expect(b.consume(1000,0)).toBe(true);expect(b.consume(1000,0)).toBe(false);
 expect(b.consume(4000,100000)).toBe(false);expect(b.consume(NaN,100000)).toBe(false);
 expect(b.consume(1000,2000)).toBe(true);
 b.observeAvailableBitrate(128000);expect(b.rate).toBe(2000);b.observeAvailableBitrate(64000);expect(b.rate).toBe(640);
 let bytes=0;for(let t=3000;t<13000;t+=250)if(b.consume(700,t))bytes+=700;
 expect(bytes).toBeLessThanOrEqual(6400+1100);
});
test("upload rate has a floor and cap, ignores missing bandwidth and recovers",()=>{
 const q=new RTCQualityPolicy();q.observe({},0);expect(q.bitrate).toBe(32000);
 q.observe({pair_availableOutgoingBitrate:40000},2000);expect(q.bitrate).toBe(16000);
 for(let t=4000;t<=30000;t+=2000)q.observe({remote_receiver_fractionLost:0},t);
 expect(q.bitrate).toBe(32000);
 q.observe({pair_availableOutgoingBitrate:1},32000);expect(q.bitrate).toBe(16000);
});
test("concealment observations are sampled deltas and survive absent packet-loss counters",()=>{
 const report=(time:number,count?:number,ssrc=42,id="in")=>new Map([[id,{type:"inbound-rtp",kind:"audio",timestamp:time,ssrc,concealedSamples:count,silentConcealedSamples:count===undefined?undefined:count/2}]]) as any;
 const first=rtcStatistics(report(1000,480));expect(first.webrtc.concealedMs).toBe(10);expect(rtcLossEvents(first.metrics)).toEqual([]);
 const next=rtcStatistics(report(2000,1440),first.previous);
 expect(next.metrics.receiver_concealed_delta_ms).toBe(20);expect(next.metrics.receiver_silent_concealed_delta_ms).toBe(10);
 const events=rtcLossEvents(next.metrics,"2026-10-10T10:00:00Z");expect(events).toHaveLength(1);
 expect(events[0]).toMatchObject({reason:"webrtc_concealment",duration_ms:20,ssrc:42,window_ms:1000});
 for(const changed of [report(3000,0),report(3000,1500,99),report(3000,1500,42,"new"),report(1000,1500),report(3000)])expect(rtcStatistics(changed,next.previous).metrics.receiver_concealed_delta_ms).toBeUndefined();
 expect(rtcStatistics(report(3000)).webrtc.concealedMs).toBeUndefined();
});
test("widget displays replacement audio as a separate warning",()=>{
 const report:any={issues:["concealed_audio"],metrics:{webrtc_concealed_ms:120}};
 expect(audioWidgetProblems(report)[0].tone).toBe("warning");
 expect(audioWidgetMeasurements(report)[0]).toMatchObject({key:"webrtc_concealed_ms",value:"120 ms"});
});

test("monitoring never precedes the RTC offer and oversized observers cannot block its queue",()=>{
 const previous=(globalThis as any).WebSocket;(globalThis as any).WebSocket={OPEN:1};const sent:string[]=[];
 const audio:any=new WebRTCAudioConnection({});audio.socket={readyState:1,bufferedAmount:0,send:(x:string)=>sent.push(x)};
 try{
  expect(audio.sendTelemetry({type:"diagnostics"})).toBe(false);expect(sent).toEqual([]);
  audio.ready=true;expect(audio.sendTelemetry({type:"diagnostics"})).toBe(true);expect(sent).toHaveLength(1);
  audio.enqueueRTCEvents({session_events:[{detail:"x".repeat(4000)}]});expect(audio.transportSender.skippedAuxiliary).toBe(1);
 }finally{audio.transportSender.stop();(globalThis as any).WebSocket=previous;}
});
