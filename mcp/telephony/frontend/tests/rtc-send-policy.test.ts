import {expect,test} from "bun:test";
import {RTCSendPolicy} from "../src/rtc-send-policy";
import {DEFAULT_NETWORK_POLICY,validateNetworkPolicy} from "../src/network-policy";
import {rtcStatistics} from "../src/webrtc-audio";
import {rtcQualityPolicy} from "../src/webrtc-quality";

const feedback=(extra:Record<string,number>={})=>({path_revision:1,remote_receiver_ssrc:42,
 remote_receiver_report_updated:1,remote_receiver_report_age_ms:0,sender_packets_delta:100,sender_window_ms:2000,
 remote_receiver_fractionLost:0,remote_receiver_roundTripTime:.04,...extra});
const make=()=>new RTCSendPolicy(validateNetworkPolicy({upload_bitrate_control:"guarded"}));
test("guarded upload ignores high stable RTT, isolated delay, receive loss and cached reports",()=>{
 for(const extra of [{remote_receiver_roundTripTime:.6},{pair_currentRoundTripTime:2,receiver_loss_delta:100,receiver_concealed_delta_ms:200},
  {remote_receiver_report_updated:0,remote_receiver_fractionLost:.5}, {sender_packets_delta:0,remote_receiver_fractionLost:.5},
  {remote_receiver_report_age_ms:9000,remote_receiver_fractionLost:.5}, {remote_receiver_fractionLost:NaN}] as Record<string,number>[]){
  const p=make();for(let t=0;t<120000;t+=2000)p.observe(feedback(extra),t);expect(p.bitrate).toBe(32000);
 }
 const p=make();p.observe(feedback(),0);p.observe(feedback({remote_receiver_roundTripTime:.4,sender_send_delay_interval_ms:40}),2000);
 for(let t=4000;t<30000;t+=2000)p.observe(feedback(),t);expect(p.bitrate).toBe(32000);
});
test("guarded upload reduces sustained directional congestion, recovers slowly and remains scoped",()=>{
 const p=make();p.observe(feedback(),0);
 for(let t=2000;t<=6000;t+=2000)p.observe(feedback({remote_receiver_roundTripTime:.25,sender_send_delay_interval_ms:30}),t);
 expect(p.bitrate).toBe(24000);expect(p.reason).toBe("sustained_upload_delay");
 for(let t=8000;t<40000;t+=2000)p.observe(feedback({remote_receiver_fractionLost:.1}),t);
 expect(p.bitrate).toBe(16000);
 for(let t=40000;t<50000;t+=2000)p.observe(feedback(),t);expect(p.bitrate).toBe(16000);
 p.observe(feedback(),50000);expect(p.bitrate).toBe(18000);
 for(let t=52000;t<150000;t+=2000)p.observe(feedback(),t);expect(p.bitrate).toBe(32000);
 expect(make().bitrate).toBe(32000);
});
test("new path, SSRC and paused samples do not inherit delay baseline or cause a rate jump",()=>{
 for(const change of [{path_revision:2},{remote_receiver_ssrc:77}] as Record<string,number>[]){
  const p=make();p.observe(feedback(),0);
  for(let t=2000;t<30000;t+=2000)p.observe(feedback({...change,remote_receiver_roundTripTime:.5,sender_send_delay_interval_ms:25}),t);
  expect(p.bitrate).toBe(32000);
 }
 const p=make();p.observe(feedback(),0);p.observe(feedback({remote_receiver_roundTripTime:.3,sender_send_delay_interval_ms:30}),2000);
 p.observe(feedback({remote_receiver_roundTripTime:.3,sender_send_delay_interval_ms:30}),20000);expect(p.bitrate).toBe(32000);
});
test("profile controller keeps both bitrate bounds and playback opt-out",()=>{
 expect(DEFAULT_NETWORK_POLICY.upload_bitrate_control).toBe("standard");
 expect(()=>validateNetworkPolicy({upload_bitrate_control:"rtt-only"})).toThrow();
 const p=rtcQualityPolicy({playbackAdaptive:false,networkProfile:validateNetworkPolicy({upload_bitrate_control:"guarded",initial_bitrate_bps:24000,min_bitrate_bps:20000,max_bitrate_bps:28000})});
 for(let t=0;t<120000;t+=2000)p.observe(feedback({remote_receiver_fractionLost:.5,receiver_jitter:1}),t);
 expect(p.bitrate).toBe(20000);expect(p.target).toBe(60);
 for(let t=120000;t<240000;t+=2000)p.observe(feedback(),t);expect(p.bitrate).toBe(28000);
});
test("native RTCP feedback freshness survives polling and rejects counter reset/replacement",()=>{
 const report=(at:number,remoteAt:number,measurements:number,ssrc=42)=>new Map([
  ["out",{type:"outbound-rtp",kind:"audio",timestamp:at,packetsSent:at/20,bytesSent:at*2,ssrc}],
  ["remote",{type:"remote-inbound-rtp",kind:"audio",timestamp:remoteAt,ssrc,roundTripTimeMeasurements:measurements,fractionLost:.1,packetsLost:5}]
 ]) as any;
 const a=rtcStatistics(report(1000,900,1));
 const b=rtcStatistics(report(2000,900,1),a.previous);
 expect(b.metrics.remote_receiver_report_updated).toBe(0);expect(b.metrics.remote_receiver_report_age_ms).toBe(1100);
 expect(b.metrics.sender_packets_delta).toBe(50);expect(b.metrics.sender_window_ms).toBe(1000);
 const c=rtcStatistics(report(3000,2900,2),b.previous);expect(c.metrics.remote_receiver_report_updated).toBe(1);
 const cached=rtcStatistics(report(4000,2900,2),c.previous);expect(cached.metrics.remote_receiver_report_updated).toBe(0);
 const reset=rtcStatistics(report(5000,4900,0),cached.previous);expect(reset.metrics.remote_receiver_report_updated).toBe(1);
 const moved=rtcStatistics(report(6000,5900,1,88),reset.previous);expect(moved.metrics.remote_receiver_report_updated).toBe(1);
});

test("cached RTCP polls do not prevent a confirmed later report from driving slow recovery",()=>{
 const p=make();p.observe(feedback(),0);
 p.observe(feedback({remote_receiver_fractionLost:.1}),4000);expect(p.bitrate).toBe(28000);
 for(let t=5000;t<=20000;t+=1000)p.observe(feedback({remote_receiver_report_updated:t%4000===0?1:0}),t);
 expect(p.bitrate).toBe(30000);
});

// Closed-loop wire queue model: exactly the same capacity and fixed isolated
// packet losses. Feedback comes from each queue's result, not a desired rate.
function wireReplay(guarded:boolean,capacity:number){
 const q=rtcQualityPolicy(guarded?{networkProfile:validateNetworkPolicy({upload_bitrate_control:"guarded"})}:{});
 let queue=0,dropped=0,windowLost=0;const delay:number[]=[];
 for(let i=0;i<6000;i++){
  const now=i*20,link=now<10000||now>=70000?96000:capacity;
  queue=Math.max(0,queue-link/8/50);
  const bytes=q.bitrate/8/50+64;
  if((queue+bytes)*8000/link>1600||i%100===75){dropped++;windowLost++;}else{queue+=bytes;delay.push(queue*8000/link);}
  if(i%100===0){q.observe(feedback({remote_receiver_fractionLost:windowLost/100,remote_receiver_roundTripTime:.04+2*queue*8/link}),now);windowLost=0;}
 }
 delay.sort((a,b)=>a-b);
 return {dropped,p95_queue_ms:delay[Math.floor(delay.length*.95)],final_bitrate:q.bitrate};
}
test("identical-network replay compares conservative upload policy with released control",async()=>{
 const results=[96000,48000,44000].map(capacity=>({capacity,standard:wireReplay(false,capacity),guarded:wireReplay(true,capacity)}));
 if(process.env.TELEPHONY_GUARDED_MODEL_RESULTS)await Bun.write(process.env.TELEPHONY_GUARDED_MODEL_RESULTS,JSON.stringify({scope:"wire queue model; not native BWE or audible speech",results},null,2));
 expect(results[0].guarded).toEqual(results[0].standard);
 for(const r of results.slice(1)){
  expect(r.guarded.dropped).toBeLessThan(r.standard.dropped);
  expect(r.guarded.p95_queue_ms).toBeLessThan(r.standard.p95_queue_ms);
 }
});


test("fresh RTCP loss does not require optional RTT measurements, and STUN RTT must advance",()=>{
 const report=(at:number)=>new Map([["remote",{type:"remote-inbound-rtp",kind:"audio",timestamp:at,ssrc:42,roundTripTimeMeasurements:0,packetsLost:10,fractionLost:.1}]]) as any;
 const a=rtcStatistics(report(1000));expect(rtcStatistics(report(2000),a.previous).metrics.remote_receiver_report_updated).toBe(1);
 const p=make();const m=feedback({pair_responsesReceived:1,pair_currentRoundTripTime:.04});delete (m as Record<string,number>).remote_receiver_roundTripTime;
 p.observe(m,0);
 for(let t=2000;t<=6000;t+=2000)p.observe({...m,pair_responsesReceived:t/2000+1,pair_currentRoundTripTime:.25,sender_send_delay_interval_ms:30},t);
 expect(p.bitrate).toBe(24000);
 const stale=make();stale.observe(m,0);
 for(let t=2000;t<20000;t+=2000)stale.observe({...m,pair_currentRoundTripTime:.25,sender_send_delay_interval_ms:30},t);
 expect(stale.bitrate).toBe(32000);
});
