import { afterEach, expect, test } from "bun:test";
import { VoiceAudioClient } from "../src/voiceAudio";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const processors=new Map<string,any>();
runInNewContext(readFileSync(new URL("../../ui/realtime-capture-worklet.js",import.meta.url),"utf8"),{sampleRate:48000,AudioWorkletProcessor:class{port={postMessage:(_:any)=>{},onmessage:null}},registerProcessor:(n:string,p:any)=>processors.set(n,p)});
let clients:VoiceAudioClient[]=[];
const originalGlobals=Object.fromEntries(["AudioContext","AudioWorkletNode","WebSocket","window","navigator"].map(k=>[k,Object.getOwnPropertyDescriptor(globalThis,k)]));
class Context {
 static last:Context;state="running";destination={};audioWorklet={addModule:async()=>{}};
 constructor(){Context.last=this;} createMediaStreamSource(){return {connect(){},disconnect(){}};} async close(){this.state="closed";}
}
class Worklet {
 static all=new Map<string,Worklet>();processor:any;
 port={onmessage:null as any,postMessage:(data:any)=>this.processor.port.onmessage?.({data})};
 constructor(_:any,name:string){Worklet.all.set(name,this);this.processor=new (processors.get(name))();this.processor.port.postMessage=(data:any)=>this.port.onmessage?.({data});}
 tick(ms:number){for(let i=0;i<Math.ceil(ms*48/128);i++)this.processor.process([],[[new Float32Array(128)]]);}
 connect(){}disconnect(){}
}
class Socket {
 static OPEN=1;static all:Socket[]=[];readyState=1;binaryType="";onmessage:any;onclose:any;
 sent:any[]=[];listeners=new Map<string,()=>void>();
 constructor(_:string){Socket.all.push(this);queueMicrotask(()=>this.listeners.get("open")?.());}
 addEventListener(n:string,f:()=>void){this.listeners.set(n,f);}send(data:any){this.sent.push(typeof data==="string"?JSON.parse(data):data);}
 close(){this.readyState=3;this.onclose?.();}
 emit(data:any){this.onmessage?.({data:typeof data==="object"&&!(data instanceof ArrayBuffer)?JSON.stringify(data):data});}
 frame(id:string,ms:number,silence=false){this.emit({type:"audio.frame",item_id:id,audio_end_ms:ms,silence});const p=new Int16Array(ms*24);if(!silence)p.fill(3000);this.emit(p.buffer);}
}
async function fixture(){
 Socket.all=[];Worklet.all.clear();Object.assign(globalThis,{AudioContext:Context,AudioWorkletNode:Worklet,WebSocket:Socket,window:{AudioWorkletNode:Worklet}});
 Object.defineProperty(globalThis,"navigator",{configurable:true,value:{mediaDevices:{getUserMedia:async()=>({getTracks:()=>[],getAudioTracks:()=>[]})}}});
 const states:string[]=[];let disconnected=0;const c=new VoiceAudioClient(s=>states.push(s),()=>disconnected++);clients.push(c);
 await c.prepare("fixture");await c.connect("ws://fixture");
 return {c,s:Socket.all.at(-1)!,player:Worklet.all.get("conversations-pcm-playback")!,mic:Worklet.all.get("conversations-pcm-capture")!,states,disconnected:()=>disconnected};
}
afterEach(()=>{for(const c of clients)c.close();clients=[];for(const [key,descriptor]of Object.entries(originalGlobals)){if(descriptor)Object.defineProperty(globalThis,key,descriptor);else delete(globalThis as any)[key];}});

test("ready immediately; partial receipts follow audio-thread consumption, not scheduling",async()=>{
 const {s,player}=await fixture();expect(s.sent).toEqual([{type:"playback.ready"}]);s.frame("A",1000);expect(s.sent).toHaveLength(1);
 player.tick(440);const progress=s.sent.filter(x=>x.type==="playback.progress");expect(progress.at(-1).audio_end_ms).toBeGreaterThan(250);expect(progress.at(-1).audio_end_ms).toBeLessThan(350);
 player.tick(800);expect(s.sent.at(-1).audio_end_ms).toBe(1000);
});
test("audio-thread clear removes only A, preserves B and rejects late A",async()=>{
 const {s,player,states}=await fixture();s.frame("A",500);s.frame("B",300);player.tick(250);s.emit({type:"interrupt",item_id:"A"});
 expect(player.processor.queue.map((x:any)=>x.item_id)).toEqual(["B"]);expect(s.sent.at(-1)).toEqual({type:"playback.cleared",item_id:"A"});expect(states.at(-1)).toBe("speaking");
 s.frame("A",100);expect(player.processor.queue).toHaveLength(1);player.tick(400);
 expect(s.sent.some(x=>x.item_id==="A"&&x.audio_end_ms===500)).toBe(false);expect(s.sent.some(x=>x.item_id==="B"&&x.audio_end_ms===300)).toBe(true);
});
test("metadata/binary pair stays canceled when interrupt arrives between messages",async()=>{
 const {s,player}=await fixture();s.emit({type:"audio.frame",item_id:"A",audio_end_ms:100});s.emit({type:"interrupt",item_id:"A"});s.emit(new Int16Array(2400).buffer);expect(player.processor.queue).toHaveLength(0);
});
test("stale callbacks cannot acknowledge or clear a replacement connection",async()=>{
 const {c,s,player,disconnected}=await fixture();s.frame("A",1000);const oldEpoch=player.processor.epoch,closed=s.onclose,message=s.onmessage;
 await c.connect("ws://replacement");const next=Socket.all.at(-1)!;next.frame("B",200);
 player.port.onmessage({data:{type:"progress",epoch:oldEpoch,item_id:"A",audio_end_ms:1000}});player.port.onmessage({data:{type:"cleared",epoch:oldEpoch,item_id:"B"}});closed();message({data:JSON.stringify({type:"interrupt"})});
 expect(next.sent).toEqual([{type:"playback.ready"}]);expect(player.processor.queue.map((x:any)=>x.item_id)).toEqual(["B"]);expect(disconnected()).toBe(0);
});
test("silent PCM is consumed without speaking; microphone stays duplex and mute sends zeros",async()=>{
 const {c,s,player,mic,states}=await fixture();s.frame("silence",500,true);expect(states.at(-1)).toBe("listening");player.tick(700);expect(s.sent.at(-1).audio_end_ms).toBe(500);
 s.frame("speech",500);const audio=new Int16Array([100,200]).buffer;mic.port.onmessage({data:audio});expect(s.sent.at(-1)).toBe(audio);
 mic.port.onmessage({data:{type:"input.speech_started"}});expect(s.sent.at(-1).type).toBe("input.speech_started");c.setMuted(true);mic.port.onmessage({data:audio});expect([...new Int16Array(s.sent.at(-1))]).toEqual([0,0]);
 s.emit({type:"interrupt"});expect(player.processor.queue).toHaveLength(0);expect(states.at(-1)).toBe("listening");
});
test("capture VAD debounces noise, reports onset/end once, and keeps capturing",()=>{
 const p=new(processors.get("conversations-pcm-capture"))(),events:any[]=[];p.port.postMessage=(v:any)=>events.push(v);
 const frame=(value:number,n:number)=>{for(let i=0;i<n;i++)p.process([[new Float32Array(128).fill(value)]],[[new Float32Array(128)]]);};
 frame(0.001,200);frame(0.2,5);frame(0,20);expect(events.filter(x=>x.type)).toHaveLength(0);frame(0.1,200);frame(0,170);
 expect(events.filter(x=>x.type).map(x=>x.type)).toEqual(["input.speech_started","input.speech_stopped"]);expect(events.filter(x=>Object.prototype.toString.call(x)==="[object ArrayBuffer]").length).toBeGreaterThan(20);
});
test("closing settles a pending reconnect",async()=>{const {c}=await fixture();const pending=c.connect("ws://pending");c.close();await expect(pending).rejects.toThrow("Voice connection closed");});
test("legacy global clear also cancels pending metadata, next item still plays",async()=>{
 const {s,player}=await fixture();s.emit({type:"audio.frame",item_id:"legacy",audio_end_ms:100});s.emit({type:"interrupt"});s.emit(new Int16Array(2400).buffer);expect(player.processor.queue).toHaveLength(0);s.frame("fresh",100);expect(player.processor.queue).toHaveLength(1);
});
