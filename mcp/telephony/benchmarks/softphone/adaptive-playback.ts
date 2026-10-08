import vm from 'node:vm';
import {readFileSync} from 'node:fs';
import {resolve} from 'node:path';

export interface AdaptiveScenario {
  name:string; rate?:number; seconds?:number; ceiling:number; adaptive:boolean;
  gaps:number[]; periodMS?:number; firstGapMS?:number; signal?:'voice'|'tone'|'silence'|'noise';
  omit?:boolean; bandwidthKbps?:number; baseLatencyMS?:number; collect?:boolean; targetStepMS?:number; targetStepAtMS?:number;
}
export function simulateAdaptivePlayback(s:AdaptiveScenario) {
  const rate=s.rate??24000, duration=(s.seconds??45)*1000, period=s.periodMS??3000;
  const ctors:Record<string,any>={}, messages:any[]=[];
  const context=vm.createContext({Float32Array,sampleRate:rate,currentTime:0,AudioWorkletProcessor:class{port={postMessage(v:any){messages.push(v);}}},registerProcessor(n:string,c:any){ctors[n]=c;}});
  vm.runInContext(readFileSync(new URL('../../ui/softphone-worklet.js',import.meta.url),'utf8'),context);
  const p=new ctors['softphone-playback']({processorOptions:{maxTargetMs:s.ceiling,adaptiveReserve:s.adaptive}});
  const sample=(index:number)=>{
    const t=index/rate;
    if(s.signal==='silence')return 0;
    if(s.signal==='noise'){let x=index+1;x=Math.imul(x^(x>>>16),0x7feb352d);x=Math.imul(x^(x>>>15),0x846ca68b);return ((x^(x>>>16))>>>0)/4294967296*.36-.18;}
    if(s.signal==='tone')return .2*Math.sin(2*Math.PI*200*t);
    // Speech-like voiced harmonics, changing envelope and quiet intervals.
    const envelope=t%1.2<.85 ? .15*(.5+.5*Math.sin(2*Math.PI*3*t)**2) : 0;
    return envelope*(Math.sin(2*Math.PI*125*t)+.3*Math.sin(2*Math.PI*250*t)+.15*Math.sin(2*Math.PI*375*t));
  };
  const first=s.firstGapMS??2000;
  const gapAt=(sourceMS:number)=>{
    const interval=Math.floor((sourceMS-first)/period);
    const start=first+interval*period, length=interval>=0 ? s.gaps[interval%s.gaps.length]??0 : 0;
    return length>0 && sourceMS>=start && sourceMS<start+length ? {start,end:start+length} : undefined;
  };
  let next=0, sequence=0, generated=0, omitted=0, lastDelivery=0, sent=0, rejected=0, deliveredEnd=0;
  let maxQueue=0, maxLatency=0, renderBlocks=0, maxRenderUS=0, renderElapsedMS=0;
  const renderCosts:number[]=[];
  const output:number[]=[], reserveSamples:Array<{at_ms:number;queue_ms:number;target_ms:number}>=[], latencies:number[]=[];
  const block=new Float32Array(128), frameSamples=Math.round(rate*.02), startCPU=performance.now();
  for(let n=0;n<duration*rate/1000;n+=128){
    const now=n*1000/rate;context.currentTime=n/rate;
    if(s.targetStepMS && now>=(s.targetStepAtMS??4000))p.targetMs=s.targetStepMS;
    while(true){
      const sourceMS=next*1000/rate;if(sourceMS>now)break;
      const gap=gapAt(sourceMS);
      if(gap && s.omit){omitted+=frameSamples;generated+=frameSamples;next+=frameSamples;sequence++;continue;}
      let delivery=Math.max(sourceMS+(s.baseLatencyMS??0), gap?.end??0);
      if(s.bandwidthKbps) delivery=Math.max(delivery,lastDelivery+(frameSamples*2+32)*8/s.bandwidthKbps);
      if(delivery>now)break;
      lastDelivery=delivery;
      // The real worker also rejects seconds-old sources before playback.
      if(now-sourceMS>320){rejected+=frameSamples;}else{
        const frame=Float32Array.from({length:frameSamples},(_,k)=>sample(next+k));
        (frame as any).sourceStart=next;
        p.handleMessage({frame,sequence,received_audio_ms:sourceMS});sent+=frameSamples;deliveredEnd=next+frameSamples;
      }
      generated+=frameSamples;next+=frameSamples;sequence++;
    }
    maxQueue=Math.max(maxQueue,p.queued*1000/rate);
    const consumedBefore=p.playedSamples, expandedBefore=p.reserveExpandedSamples;
    const cpu=performance.now();p.process([],[[block]]);const elapsed=performance.now()-cpu;renderElapsedMS+=elapsed;renderCosts.push(elapsed*1000);maxRenderUS=Math.max(maxRenderUS,elapsed*1000);renderBlocks++;
    if(s.collect) output.push(...block);
    if(p.playedSamples>consumedBefore || p.reserveExpandedSamples>expandedBefore){
      // Source cursor includes controlled compression; expansion emits without
      // advancing it. This measures source-to-render age, not queue age alone.
      const sourceCursor=(p.queue.length ? p.queue[0].frame.sourceStart+p.offset : deliveredEnd)*1000/rate;
      const age=Math.max(0,now+128000/rate-sourceCursor);maxLatency=Math.max(maxLatency,age);
      if(n% (128*16)===0)latencies.push(age);
    }
    if(n%(128*64)===0)reserveSamples.push({at_ms:now,queue_ms:p.queued*1000/rate,target_ms:p.targetMs});
  }
  p.reportStats();
  latencies.sort((a,b)=>a-b);renderCosts.sort((a,b)=>a-b);
  const gaps=messages.filter(v=>v.type==='stats').at(-1)?.underrun_events??[];
  return {name:s.name,rate,ceiling_ms:s.ceiling,adaptive:s.adaptive,signal:s.signal??'voice',duration_ms:duration,
    underruns:p.underruns,underrun_ms:p.underrunSamples*1000/rate,late_underruns:gaps.filter((e:any)=>e.start_audio_ms>duration/2).length,
    dropped_ms:p.droppedSamples*1000/rate,transport_rejected_ms:rejected*1000/rate,omitted_ms:omitted*1000/rate,
    expanded_ms:p.reserveExpandedSamples*1000/rate,compressed_ms:p.reserveCompressedSamples*1000/rate,
    match_rejections:p.reserveMatchRejections,adjustments:p.reserveAdjustments,max_queue_ms:maxQueue,max_residence_ms:p.maxResidenceMS,
    p95_source_latency_ms:latencies[Math.floor(latencies.length*.95)]??0,max_source_latency_ms:maxLatency,
    accounted_samples:p.playedSamples+p.droppedSamples+p.queued,sent_samples:sent,generated_samples:generated,
    simulation_elapsed_ms:performance.now()-startCPU,render_elapsed_ms:renderElapsedMS,p95_render_us:renderCosts[Math.floor(renderCosts.length*.95)]??0,max_render_us:maxRenderUS,render_blocks:renderBlocks,
    output, reserve_samples:reserveSamples};
}
export function adaptiveMatrix() {
  const results=[];
  for(const signal of ['voice','tone'] as const)for(const gaps of [[0],[80],[280],[40,80,120,200,280]])for(const ceiling of [160,220,280])for(const adaptive of [false,true]){
    results.push(simulateAdaptivePlayback({name:`${signal}-${gaps.join('_')}-${ceiling}-${adaptive?'adaptive':'legacy'}`,signal,gaps,ceiling,adaptive}));
  }
  for(const s of [
    {name:'missing-280',gaps:[280],omit:true},
    {name:'seven-second-catchup',gaps:[7000],periodMS:20000},
    {name:'512k',gaps:[40,80],bandwidthKbps:512,baseLatencyMS:30},
    {name:'256k',gaps:[0],bandwidthKbps:256,baseLatencyMS:30},
  ])results.push(simulateAdaptivePlayback({...s,ceiling:280,adaptive:true}));
  return results;
}
if(import.meta.main){
  const destination=resolve(process.argv[2]??'/tmp/telephony-adaptive-playback.json');
  const results=adaptiveMatrix();
  await Bun.write(destination,JSON.stringify({schema:'telephony-adaptive-playback/v1',created_at:new Date().toISOString(),scope:'Local actual AudioWorklet in VM; synthetic voiced audio and transport timing; no live carrier or perceptual quality claim',results},null,2));
  for(const r of results)console.log(`${r.name}: ${r.underruns} gaps / ${r.underrun_ms.toFixed(1)}ms; drops=${r.dropped_ms.toFixed(1)}ms; p95=${r.p95_source_latency_ms.toFixed(1)}ms; expand=${r.expanded_ms.toFixed(1)}ms; compress=${r.compressed_ms.toFixed(1)}ms`);
  console.log(destination);
}
