import {expect, test} from 'bun:test';
import {runInNewContext} from 'node:vm';

const workerSource=await Bun.file(new URL('./clock-observer.js',import.meta.url)).text();
function observer(){
 let clock=0,tick:()=>void=()=>{},result:any;
 const self:any={};
 runInNewContext(workerSource,{self,performance:{timeOrigin:100000,now:()=>clock},
  setInterval:(fn:()=>void)=>{tick=fn;return 1;},clearInterval:()=>{},postMessage:(value:any)=>{result=value;}});
 const attach=(stage:string)=>{
  const requests:any[]=[];
  const port:any={postMessage:(value:any)=>requests.push(value),start:()=>{},onmessage:null};
  self.onmessage({data:{type:'attach',stage,port}});
  return {reply:(audio:number)=>{const request=requests.at(-1);port.onmessage({data:{type:'clock.reply',nonce:request.nonce,audio_ms:audio}});}};
 };
 return {attach,advance:(ms:number)=>{clock+=ms;},tick:()=>tick(),finish:()=>{self.onmessage({data:{type:'finish'}});return result;}};
}
test('independent clocks distinguish source rendering pauses from observer/UI delivery',()=>{
 const o=observer(),source=o.attach('synthetic_microphone'),playback=o.attach('playback:0');
 source.reply(0);playback.reply(0);
 o.advance(2000);o.tick();source.reply(0);playback.reply(2000);
 const result=o.finish();
 expect(result.stages.synthetic_microphone.max_lag_ms).toBe(2000);
 expect(result.stages['playback:0'].max_lag_ms).toBe(0);
 expect(result.samples.at(-1).timestamp).toBe('1970-01-01T00:01:42.000Z');
});
test('late clock-probe replies cannot manufacture an audio clock measurement',()=>{
 const o=observer(),source=o.attach('source');o.advance(51);source.reply(0);
 expect(o.finish().samples).toHaveLength(0);
});
test('a replacement playback context starts a separate clock baseline',()=>{
 const o=observer(),first=o.attach('playback:0');first.reply(1000);
 o.advance(1000);o.tick();first.reply(2000);
 const replacement=o.attach('playback:1');replacement.reply(0);
 o.advance(250);o.tick();replacement.reply(250);
 expect(o.finish().stages['playback:1'].max_lag_ms).toBe(0);
});
test('clock observations stay bounded during long runs',()=>{
 const o=observer(),source=o.attach('source');source.reply(0);
 for(let i=1;i<=1000;i++){o.advance(250);o.tick();source.reply(i*250);}
 expect(o.finish().samples).toHaveLength(640);
});
test('attaching a clock probe does not reset the benchmark microphone waveform',async()=>{
 const source=await Bun.file(new URL('./probe-worklet.js',import.meta.url)).text();
 const processors=new Map<string,any>();
 runInNewContext(source,{AudioWorkletProcessor:class{port:any={};},sampleRate:24000,currentTime:10,
  registerProcessor:(name:string,processor:any)=>processors.set(name,processor)});
 const microphone=new (processors.get('benchmark-source'))();
 microphone.port.onmessage({data:{start:9}});
 const replies:any[]=[];const port:any={postMessage:(x:any)=>replies.push(x),start:()=>{}};
 microphone.port.onmessage({data:{clock_port:port}});
 expect(microphone.start).toBe(9);
 port.onmessage({data:{type:'clock.probe',nonce:100}});
 expect(replies).toEqual([{type:'clock.reply',nonce:100,audio_ms:10000}]);
});
