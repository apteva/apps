import { afterEach, expect, test } from "bun:test";
import { VoiceAudioClient } from "../src/voiceAudio";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

let clients: VoiceAudioClient[] = [];
const originalGlobals = Object.fromEntries(["AudioContext","AudioWorkletNode","WebSocket","window","navigator"].map(k=>[k,Object.getOwnPropertyDescriptor(globalThis,k)]));
class Source {
  onended: (() => void) | null = null; stopped = false; startAt = 0;
  buffer: any; connect() {} disconnect() {} start(at:number) { this.startAt=at; }
  stop() { this.stopped=true; this.onended?.(); }
}
class Context {
  static last: Context; currentTime=0; state="running"; destination={}; sources:Source[]=[];
  audioWorklet={addModule:async()=>{}};
  constructor(){Context.last=this;}
  createMediaStreamSource(){return {connect(){},disconnect(){}};}
  createBuffer(_:number,n:number,rate:number){return {duration:n/rate,getChannelData:()=>new Float32Array(n)};}
  createBufferSource(){const s=new Source();this.sources.push(s);return s;}
  async close(){this.state="closed";}
}
class Worklet {
  static last:Worklet; port={onmessage:null as any,postMessage:(_:any)=>{}};
  constructor(){Worklet.last=this;} connect(){} disconnect(){}
}
class Socket {
  static OPEN=1; static all:Socket[]=[]; readyState=1; binaryType=""; onmessage:any; onclose:any;
  sent:any[]=[]; listeners=new Map<string,()=>void>();
  constructor(_:string){Socket.all.push(this);queueMicrotask(()=>this.listeners.get("open")?.());}
  addEventListener(n:string,f:()=>void){this.listeners.set(n,f);}
  send(data:any){this.sent.push(typeof data==="string"?JSON.parse(data):data);}
  close(){this.readyState=3;this.onclose?.();}
  emit(data:any){this.onmessage?.({data:typeof data==="object"&&!(data instanceof ArrayBuffer)?JSON.stringify(data):data});}
  frame(id:string,ms:number,silence=false){this.emit({type:"audio.frame",item_id:id,audio_end_ms:ms,silence});const p=new Int16Array(ms*24);if(!silence)p.fill(3000);this.emit(p.buffer);}
}
async function fixture(){
  Socket.all=[];
  Object.assign(globalThis,{AudioContext:Context,AudioWorkletNode:Worklet,WebSocket:Socket,window:{AudioWorkletNode:Worklet}});
  Object.defineProperty(globalThis,"navigator",{configurable:true,value:{mediaDevices:{getUserMedia:async()=>({getTracks:()=>[],getAudioTracks:()=>[]})}}});
  const states:string[]=[];let disconnected=0;
  const c=new VoiceAudioClient(s=>states.push(s),()=>disconnected++);clients.push(c);
  await c.prepare("fixture");await c.connect("ws://fixture");
  return {c,s:Socket.all.at(-1)!,ctx:Context.last,states,disconnected:()=>disconnected};
}
afterEach(()=>{for(const c of clients)c.close();clients=[]; for(const [key, descriptor] of Object.entries(originalGlobals)) { if(descriptor) Object.defineProperty(globalThis,key,descriptor); else delete (globalThis as any)[key]; }});

test("ready immediately; partial playback receipts follow clock, not scheduling",async()=>{
  const {s,ctx}=await fixture();expect(s.sent).toEqual([{type:"playback.ready"}]);
  s.frame("A",1000);expect(s.sent).toHaveLength(1);
  ctx.currentTime=0.335;await Bun.sleep(60);
  const receipt=s.sent.find(x=>x.type==="playback.progress");expect(receipt.audio_end_ms).toBe(320);
  ctx.currentTime=1.0149;ctx.sources[0].onended?.();expect(s.sent.at(-1).audio_end_ms).toBe(1000);
});
test("scoped clear preserves B and scheduling; rejects late A and canceled callbacks",async()=>{
  const {s,ctx,states}=await fixture();s.frame("A",500);s.frame("B",300);
  const ended=ctx.sources[0].onended!;ctx.currentTime=0.215;
  s.emit({type:"interrupt",item_id:"A"});ended();
  expect(ctx.sources[0].stopped).toBe(true);expect(ctx.sources[1].stopped).toBe(false);
  expect(s.sent.some(x=>x.item_id==="A"&&x.audio_end_ms===500)).toBe(false);
  expect(s.sent.at(-1)).toEqual({type:"playback.cleared",item_id:"A"});expect(states.at(-1)).toBe("speaking");
  s.frame("A",100);expect(ctx.sources).toHaveLength(2);
  s.frame("C",100);expect(ctx.sources[2].startAt).toBeCloseTo(0.815);
});
test("metadata/binary pair stays canceled even when interrupt arrives between frames",async()=>{
  const {s,ctx}=await fixture();s.emit({type:"audio.frame",item_id:"A",audio_end_ms:100});s.emit({type:"interrupt",item_id:"A"});s.emit(new Int16Array(2400).buffer);expect(ctx.sources).toHaveLength(0);
});
test("old callbacks cannot report or clear replacement connection; every connection is ready",async()=>{
  const {c,s,ctx,disconnected}=await fixture();s.frame("A",1000);
  const ended=ctx.sources[0].onended!,closed=s.onclose,message=s.onmessage;
  await c.connect("ws://replacement");const next=Socket.all.at(-1)!;next.frame("B",200);
  ended();closed();message({data:JSON.stringify({type:"interrupt"})});
  expect(next.sent).toEqual([{type:"playback.ready"}]);expect(ctx.sources[1].stopped).toBe(false);expect(disconnected()).toBe(0);
});
test("silence preserves PCM and receipts but does not show speaking; mic remains duplex",async()=>{
  const {c,s,ctx,states}=await fixture();s.frame("silence",500,true);expect(ctx.sources).toHaveLength(1);expect(states.at(-1)).toBe("listening");
  s.frame("speech",500);const mic=new Int16Array([100,200]).buffer;Worklet.last.port.onmessage({data:mic});expect(s.sent.at(-1)).toBe(mic);
  Worklet.last.port.onmessage({data:{type:"input.speech_started"}});expect(s.sent.at(-1).type).toBe("input.speech_started");
  c.setMuted(true);Worklet.last.port.onmessage({data:mic});expect([...new Int16Array(s.sent.at(-1))]).toEqual([0,0]);
  s.emit({type:"interrupt"});expect(ctx.sources.every(x=>x.stopped)).toBe(true);expect(states.at(-1)).toBe("listening");
});
test("capture VAD debounces noise, reports onset/end once, and never pauses PCM",()=>{
  let Processor:any;const events:any[]=[];
  runInNewContext(readFileSync(new URL("../../ui/realtime-capture-worklet.js",import.meta.url),"utf8"),{sampleRate:48000,AudioWorkletProcessor:class{port={postMessage:(v:any)=>events.push(v),onmessage:null}},registerProcessor:(_:string,p:any)=>Processor=p});
  const p=new Processor();const frame=(value:number,n:number)=>{for(let i=0;i<n;i++)p.process([[new Float32Array(128).fill(value)]],[[new Float32Array(128)]]);};
  frame(0.001,200);frame(0.2,5);frame(0,20);expect(events.filter(x=>x.type)).toHaveLength(0);
  frame(0.1,100);frame(0.1,100);frame(0,170);
  expect(events.filter(x=>x.type).map(x=>x.type)).toEqual(["input.speech_started","input.speech_stopped"]);
  expect(events.filter(x=>x instanceof ArrayBuffer||Object.prototype.toString.call(x)==="[object ArrayBuffer]").length).toBeGreaterThan(20);
});

test("closing during a pending reconnect settles the connection promise",async()=>{
  const {c}=await fixture();
  const pending=c.connect("ws://pending");
  // close before FakeSocket's queued open event runs.
  c.close();
  await expect(pending).rejects.toThrow("Voice connection closed");
});

test("legacy global interrupt also cancels a pending metadata/binary pair",async()=>{
  const {s,ctx}=await fixture();
  s.emit({type:"audio.frame",item_id:"legacy",audio_end_ms:100});s.emit({type:"interrupt"});s.emit(new Int16Array(2400).buffer);
  expect(ctx.sources).toHaveLength(0);
  s.frame("fresh",100);expect(ctx.sources).toHaveLength(1);
});
