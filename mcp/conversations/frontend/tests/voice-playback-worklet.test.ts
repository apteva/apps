import { expect,test } from "bun:test";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
const code=readFileSync(new URL("../../ui/realtime-capture-worklet.js",import.meta.url),"utf8");
function renderer(rate=48000){
 let Processor:any;const events:any[]=[];
 runInNewContext(code,{sampleRate:rate,AudioWorkletProcessor:class{port={postMessage:(v:any)=>events.push(v),onmessage:null}},registerProcessor:(name:string,p:any)=>{if(name==="conversations-pcm-playback")Processor=p;}});
 const p=new Processor(),send=(data:any)=>p.port.onmessage({data:{...data,epoch:1}});send({type:"reset"});
 return {p,events,send,render(){const output=new Float32Array(128);p.process([],[[output]]);return [...output];}};
}
for(const rate of [48000,44100])test(`continuous tone survives jitter and packet resampling at ${rate} Hz`,()=>{
 const r=renderer(rate),count=24000,pcm=Int16Array.from({length:count},(_,i)=>Math.round(Math.sin(i*2*Math.PI*440/24000)*16000));
 const output:number[]=[];let packet=0;
 // 20ms packets, deliberately bunched/delayed by up to 25ms. The one-time
 // 120ms jitter reserve must bridge this without per-packet gaps or clicks.
 const arrivals=Array.from({length:50},(_,i)=>i*0.02+([0,0.025,0.010,0.018][i%4]));
 // Reproduce the previous player's actual scheduling formula on the same
 // packet arrivals: it inserts a 25ms hole into otherwise continuous PCM.
 let legacyEnd=0,largestGap=0;
 for(const arrived of arrivals){const start=Math.max(arrived+0.015,legacyEnd);if(legacyEnd)largestGap=Math.max(largestGap,start-legacyEnd);legacyEnd=start+0.02;}
 expect(largestGap).toBeGreaterThan(0.02);
 for(let quantum=0;quantum<Math.ceil(1.3*rate/128);quantum++){
  const now=quantum*128/rate;
  while(packet<50&&arrivals[packet]<=now){const part=pcm.slice(packet*480,(packet+1)*480);r.send({type:"pcm",pcm:part.buffer,item_id:"tone",audio_end_ms:(packet+1)*20,silence:false});packet++;}
  output.push(...r.render());
 }
 const first=output.findIndex(x=>Math.abs(x)>1e-6);
 expect(first/rate).toBeGreaterThan(0.115);expect(first/rate).toBeLessThan(0.135);
 // Compare the actual samples with one continuous linear resampling of the
 // original tone. Exclude only the deliberate 3ms fade at stream start/end.
 const origin=Math.round(rate*0.12);let maxError=0;
 for(let i=Math.ceil(rate*0.005);i<Math.floor(rate*0.995);i++){
  const position=i*24000/rate,index=Math.floor(position),f=position-index;
  const expected=(pcm[index]*(1-f)+pcm[index+1]*f)/32768;
  maxError=Math.max(maxError,Math.abs(output[origin+i]-expected));
 }
 expect(maxError).toBeLessThan(0.00001);
 expect(r.events.filter(x=>x.type==="progress").at(-1).audio_end_ms).toBe(1000);
});

test("partial clear receipts precede clear acknowledgment; unplayed A is never consumed and B survives",()=>{
 const r=renderer();for(const id of ["A","B"])r.send({type:"pcm",pcm:new Int16Array(24000).fill(2000).buffer,item_id:id,audio_end_ms:1000});
 for(let i=0;i<100;i++)r.render();r.send({type:"clear",item_id:"A"});
 const cleared=r.events.findIndex(x=>x.type==="cleared");expect(r.events[cleared-1]).toMatchObject({type:"progress",item_id:"A"});expect(r.events[cleared-1].audio_end_ms).toBeLessThan(1000);
 for(let i=0;i<450;i++)r.render();expect(r.events.some(x=>x.type==="progress"&&x.item_id==="A"&&x.audio_end_ms===1000)).toBe(false);expect(r.events.some(x=>x.type==="progress"&&x.item_id==="B"&&x.audio_end_ms===1000)).toBe(true);
});
