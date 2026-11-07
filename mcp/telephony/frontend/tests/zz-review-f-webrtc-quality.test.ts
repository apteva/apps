import {expect,test} from "bun:test";
import {RTCQualityPolicy,rtcQualityPolicy} from "../src/webrtc-quality";

const report=(loss:number,updated=1)=>({remote_receiver_fractionLost:loss,remote_receiver_report_updated:updated});

test("standard upload control does not oscillate around a 3% loss boundary",()=>{
 // Alternating fresh reports of 3% and 0% loss: the released control lowered
 // 4 kbps then raised 2 kbps every 2 s, a saw-tooth of 15 changes per minute.
 const q=new RTCQualityPolicy();let changes=0,previous=q.bitrate;
 for(let t=0;t<60000;t+=2000){q.observe(report(t%4000?0:.03),t);if(q.bitrate!==previous)changes++;previous=q.bitrate;}
 expect(q.bitrate).toBe(16000);
 expect(changes).toBeLessThanOrEqual(5);
});

test("standard upload control recovers only after a 10 s clean window",()=>{
 const q=new RTCQualityPolicy();
 q.observe(report(.05),0);expect(q.bitrate).toBe(28000);
 for(let t=2000;t<10000;t+=2000){q.observe(report(0),t);expect(q.bitrate).toBe(28000);}
 for(let t=10000;t<=12000;t+=2000)q.observe(report(0),t);
 expect(q.bitrate).toBe(30000);
 // A moderate-loss report restarts the stability window.
 q.observe(report(.02),14000);for(let t=16000;t<24000;t+=2000)q.observe(report(0),t);
 expect(q.bitrate).toBe(30000);
});

test("a cached receiver report is not counted again",()=>{
 const q=new RTCQualityPolicy();
 q.observe(report(.1),0);
 for(let t=2000;t<20000;t+=2000)q.observe(report(.1,0),t);
 expect(q.bitrate).toBe(28000);
 const clean=new RTCQualityPolicy();clean.observe(report(.1),0);
 for(let t=2000;t<60000;t+=2000)clean.observe(report(0,0),t);
 expect(clean.bitrate).toBe(28000);
});

test("an unconfigured host still uses the standard controller",()=>{
 expect(rtcQualityPolicy({}).uploadControl).toBe("standard");
});
