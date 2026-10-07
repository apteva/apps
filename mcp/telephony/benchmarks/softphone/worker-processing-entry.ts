// Local benchmark fixtures, never included in the app's browser bundle.
const realNow=performance.now.bind(performance);
let fixtureNow=10000;
const sockets:any[]=[],intervals:Function[]=[];
let sent=0,received=0,dropped=0;
class FixtureSocket {
 static OPEN=1;readyState=1;bufferedAmount=0;onopen:any;onmessage:any;
 constructor(){sockets.push(this);}close(){}
 send(message:unknown){if(message instanceof ArrayBuffer)sent++;}
}
const originalPost=postMessage.bind(globalThis);
Object.defineProperty(globalThis,'performance',{value:{timeOrigin:0,now:()=>fixtureNow},configurable:true});
Object.assign(globalThis,{WebSocket:FixtureSocket,setInterval(f:Function){intervals.push(f);return intervals.length;},clearInterval(){},close(){},postMessage(message:any){if(message.type==='transport.drop')dropped++;}});
(self as any).importScripts('/production-worker.js');
const results=[];
for(const rate of [24000,44100,48000]) {
 fixtureNow=10000;sent=received=dropped=0;
 const capture:any={postMessage(){},start(){},close(){}};
 const playback:any={postMessage(m:any){if(m.type==='playback')received++;},start(){},close(){}};
 const command=(data:any)=>(self.onmessage as Function)({data});
 command({type:'init',mediaURL:'ws://loopback-fixture',capturePort:capture,playbackPort:playback,contextRate:rate,audioClockMS:0,monotonicEpochMS:fixtureNow});
 const socket=sockets.at(-1);socket.onopen();command({type:'microphone.ready',value:true});
 socket.onmessage({data:JSON.stringify({type:'media.capabilities',version:3})});
 const durations:number[]=[];
 for(let i=0;i<1100;i++) {
  fixtureNow=10000+i*20;
  const microphone=new Float32Array(rate/50).fill(.125);
  const data=new ArrayBuffer(64+960),v=new DataView(data);
  v.setUint32(0,0x33545041,true);v.setUint32(4,i,true);
  v.setFloat64(16,100+i*20,true);v.setFloat64(48,100+i*20,true);
  new Int16Array(data,64).fill(4096);
  const start=realNow();
  if(i%50===0) {
   capture.onmessage({data:{type:'clock.reply',nonce:fixtureNow,audio_ms:i*20}});
   socket.onmessage({data:JSON.stringify({type:'media.clock',nonce:fixtureNow,received_ms:100+i*20,sent_ms:100+i*20})});
   intervals.at(-1)!();
  }
  capture.onmessage({data:{type:'capture',frame:microphone,sample_rate:rate,timestamp_ms:i*20,sequence:i}});
  socket.onmessage({data});
  if(i>=100)durations.push(realNow()-start);
 }
 durations.sort((a,b)=>a-b);
 if(sent!==1100||received!==1100||dropped!==0)throw new Error(`Unexpected loss at ${rate}Hz: ${JSON.stringify({sent,received,dropped})}`);
 const mean=durations.reduce((a,b)=>a+b,0)/durations.length;
 results.push({rate,frames:1100,observed_drops:dropped,mean_ms:mean,p95_ms:durations[Math.ceil(.95*durations.length)-1],max_ms:durations.at(-1),cpu_percent_of_one_core_at_50fps:mean/20*100});
}
originalPost({type:'benchmark.complete',results});
