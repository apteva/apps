import {expect,test} from 'bun:test';
import {simulateAdaptivePlayback} from '../benchmarks/softphone/adaptive-playback';

for(const rate of [24000,44100,48000]) {
 test(`live reserve grows during playback without changing tone pitch (${rate}Hz)`,()=>{
  const r=simulateAdaptivePlayback({name:'growth',rate,seconds:22,gaps:[0],ceiling:280,adaptive:true,signal:'tone',targetStepMS:280,collect:true});
  expect(r.underruns).toBe(0);expect(r.dropped_ms).toBe(0);expect(r.accounted_samples).toBe(r.sent_samples);
  expect(r.expanded_ms).toBeGreaterThan(180);expect(r.max_queue_ms).toBeLessThanOrEqual(320);expect(r.max_residence_ms).toBeLessThan(320);
  expect(r.reserve_samples.filter(s=>s.at_ms>19000).every(s=>s.queue_ms>250)).toBe(true);
  const audio=r.output.slice(rate*5,rate*20);let squares=0,crossings=0,jump=0;
  for(let i=1;i<audio.length;i++){squares+=audio[i]*audio[i];if(audio[i-1]<0 && audio[i]>=0)crossings++;jump=Math.max(jump,Math.abs(audio[i]-audio[i-1]));}
  expect(crossings/15).toBeCloseTo(200,0);expect(Math.sqrt(squares/audio.length)).toBeCloseTo(.2/Math.sqrt(2),3);
  expect(jump).toBeLessThan(.2*2*Math.PI*200/rate*1.15);
  expect(r.expanded_ms).toBeLessThan(22000*.02+20);
 });
 test(`280ms repeats settle without ongoing cuts (${rate}Hz)`,()=>{
  const r=simulateAdaptivePlayback({name:'repeat',rate,seconds:30,gaps:[280],ceiling:280,adaptive:true});
  expect(r.late_underruns).toBe(0);expect(r.dropped_ms).toBe(0);expect(r.p95_source_latency_ms).toBeLessThan(320);
  expect(r.accounted_samples).toBe(r.sent_samples);
 });
}
test('progressively worse jitter reduces missing playback without discarding speech',()=>{
 const scenario={name:'progressive',seconds:45,gaps:[40,80,120,200,280],signal:'voice' as const};
 const legacy=simulateAdaptivePlayback({...scenario,ceiling:160,adaptive:false});
 const candidate=simulateAdaptivePlayback({...scenario,ceiling:280,adaptive:true});
 expect(candidate.underrun_ms).toBeLessThan(legacy.underrun_ms*.6);expect(candidate.underruns).toBeLessThan(legacy.underruns);
 expect(candidate.dropped_ms).toBe(0);expect(candidate.accounted_samples).toBe(candidate.sent_samples);expect(candidate.p95_source_latency_ms).toBeLessThan(320);
});
test('stable audio remains sample-exact and noise is never forced through a poor splice',()=>{
 for(const signal of ['tone','noise','silence'] as const){
  const scenario={name:'steady',seconds:8,gaps:[0],signal,collect:true,ceiling:280};
  const normal=simulateAdaptivePlayback({...scenario,adaptive:false});
  const adaptive=simulateAdaptivePlayback({...scenario,adaptive:true,...(signal==='noise'?{targetStepMS:280}: {})});
  expect(adaptive.output).toEqual(normal.output);expect(adaptive.dropped_ms).toBe(0);expect(adaptive.underruns).toBe(0);
  if(signal==='noise')expect(adaptive.match_rejections).toBeGreaterThan(0);
 }
});
test('missing media and insufficient bandwidth remain honestly classified, not synthesized',()=>{
 const missing=simulateAdaptivePlayback({name:'missing',seconds:20,gaps:[280],ceiling:280,adaptive:true,omit:true});
 expect(missing.omitted_ms).toBeGreaterThan(0);expect(missing.underrun_ms).toBeGreaterThan(1000);expect(missing.expanded_ms).toBeLessThan(missing.duration_ms*.02+20);
 const slow=simulateAdaptivePlayback({name:'slow',seconds:20,gaps:[0],ceiling:280,adaptive:true,bandwidthKbps:256});
 expect(slow.transport_rejected_ms).toBeGreaterThan(0);expect(slow.underrun_ms).toBeGreaterThan(1000);
});
test('stable recovery drains reserve gradually while retaining signal and latency caps',()=>{
 const r=simulateAdaptivePlayback({name:'drain',seconds:55,gaps:[280,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],ceiling:280,adaptive:true,signal:'tone',targetStepMS:60,targetStepAtMS:7000});
 expect(r.compressed_ms).toBeGreaterThan(150);expect(r.late_underruns).toBe(0);expect(r.dropped_ms).toBe(0);expect(r.accounted_samples).toBe(r.sent_samples);
 expect(r.reserve_samples.filter(s=>s.at_ms>50000).every(s=>s.queue_ms<100)).toBe(true);
});
test('ten-minute voiced conversation retains a bounded reserve and source accounting',()=>{
 const r=simulateAdaptivePlayback({name:'long-voice',seconds:600,gaps:[40,80,120,200,280],ceiling:280,adaptive:true});
 expect(r.max_queue_ms).toBeLessThanOrEqual(320);expect(r.max_residence_ms).toBeLessThan(320);
 expect(r.p95_source_latency_ms).toBeLessThan(320);expect(r.accounted_samples).toBe(r.sent_samples);expect(r.dropped_ms).toBe(0);
 expect(r.late_underruns).toBe(0);
},30000);
