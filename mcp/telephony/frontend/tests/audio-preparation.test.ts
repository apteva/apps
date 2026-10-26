import {afterEach,expect,spyOn,test} from "bun:test";
import {AudioPreparation,AUDIO_PREFLIGHT_TIMEOUT_MS} from "../../ui/audio-preparation";
import {createBrowserAudio} from "../src/audio";
import {DEFAULT_SOFTPHONE_AUDIO_OPTIONS} from "../../ui/softphone-audio";
const restore:Array<()=>void>=[];
afterEach(()=>{while(restore.length)restore.pop()!();});
function replace(name:string,value:unknown){const old=Object.getOwnPropertyDescriptor(globalThis,name);Object.defineProperty(globalThis,name,{value,configurable:true});restore.push(()=>{if(old)Object.defineProperty(globalThis,name,old);else Reflect.deleteProperty(globalThis,name);});}
function fixture(){
 let opens=0,stops=0,activation=true,resumes=0,closes=0;
 const track={readyState:"live",stop(){stops++;this.readyState="ended";},getSettings:()=>({sampleRate:48000,channelCount:1})};
 const stream={getTracks:()=>[track],getAudioTracks:()=>[track]} as unknown as MediaStream;
 let microphone=()=>Promise.resolve(stream),resume=()=>Promise.resolve();
 class Context{
  state="suspended";sampleRate=48000;
  audioWorklet={addModule:async()=>{throw Error("worklet fixture failure");}};
  resume(){resumes++;if(!activation)throw Error("playback activation lost");return resume().then(()=>{this.state="running";});}
  async close(){closes++;this.state="closed";}
 }
 replace("AudioContext",Context);replace("navigator",{mediaDevices:{getUserMedia:()=>{opens++;return microphone();}}});
 return {stream,track,releaseActivation(){activation=false;},setMicrophone(fn:typeof microphone){microphone=fn;},setResume(fn:typeof resume){resume=fn;},get opens(){return opens;},get stops(){return stops;},get resumes(){return resumes;},get closes(){return closes;}};
}
function fastTimeout(){const original=setTimeout;const timer=spyOn(globalThis,"setTimeout").mockImplementation(((fn:any,ms:number,...args:any[])=>original(fn,ms===AUDIO_PREFLIGHT_TIMEOUT_MS?5:ms,...args)) as typeof setTimeout);restore.push(()=>timer.mockRestore());}

test("playback is unlocked synchronously and the live microphone transfers exactly once",async()=>{
 const f=fixture(),events:any[]=[];const p=new AudioPreparation({}, {latencyHint:"interactive"},e=>events.push(e));
 expect(f.resumes).toBe(1);expect(f.opens).toBe(1);f.releaseActivation();await p.ready;
 const devices=p.take();expect(devices.stream).toBe(f.stream);expect(devices.context.state).toBe("running");
 expect(()=>p.take()).toThrow();p.cancel();expect(f.stops).toBe(0);expect(f.closes).toBe(0);
 devices.stream.getTracks().forEach(t=>t.stop());await devices.context.close();
 expect(f.stops).toBe(1);expect(f.closes).toBe(1);expect(events.some(e=>e.action==="audio_context_preflight"&&e.outcome==="completed")).toBe(true);
});
test("cancelled device permission stops a late microphone and cannot be reused",async()=>{
 const f=fixture();let resolve!:(s:MediaStream)=>void;f.setMicrophone(()=>new Promise(r=>resolve=r));
 const p=new AudioPreparation({},{});const failed=p.ready.catch(e=>e);p.cancel();expect((await failed).message).toContain("cancelled");
 resolve(f.stream);await Bun.sleep(0);expect(f.stops).toBe(1);expect(f.closes).toBe(1);expect(()=>p.take()).toThrow();
});
test("stalled playback permission is bounded and closes already-open microphone",async()=>{
 fastTimeout();const f=fixture();f.setResume(()=>new Promise(()=>{}));const p=new AudioPreparation({},{});
 await expect(p.ready).rejects.toThrow("timed out");expect(f.stops).toBe(1);expect(f.closes).toBe(1);
});
test("stalled microphone is bounded; a late device is stopped after timeout",async()=>{
 fastTimeout();const f=fixture();let resolve!:(s:MediaStream)=>void;f.setMicrophone(()=>new Promise(r=>resolve=r));
 const p=new AudioPreparation({},{});await expect(p.ready).rejects.toThrow("timed out");
 resolve(f.stream);await Bun.sleep(0);expect(f.stops).toBe(1);expect(f.closes).toBe(1);
});
test("permission denial closes playback preparation and does not retain devices",async()=>{
 const f=fixture();f.setMicrophone(()=>Promise.reject(new DOMException("Denied","NotAllowedError")));
 const p=new AudioPreparation({},{});await expect(p.ready).rejects.toThrow("Denied");expect(f.closes).toBe(1);expect(()=>p.take()).toThrow();
});
for(const transport of ["webrtc","websocket"] as const){
 test(`${transport} adopts preflight devices after HTTP without reopening or requesting playback activation`,async()=>{
  const f=fixture();replace("RTCPeerConnection",class {});
  const runtime=createBrowserAudio();const options={...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:transport};
  await runtime.preflight(options);expect(f.stops).toBe(0);f.releaseActivation();
  f.setMicrophone(()=>Promise.reject(Error("A second microphone open must never occur")));
  const connection=runtime.create({});
  await expect(connection.start("ws://fixture/media",options)).rejects.toThrow(transport==="webrtc"?"worklet fixture failure":"audio processor could not load");
  connection.stop();runtime.cancelPreparation?.();
  expect(f.opens).toBe(1);expect(f.resumes).toBe(1);expect(f.stops).toBe(1);expect(f.closes).toBe(1);
 });
}
test("failed answer request can dispose preparation and the next attempt gets fresh devices",async()=>{
 const f=fixture(),runtime=createBrowserAudio();await runtime.preflight(DEFAULT_SOFTPHONE_AUDIO_OPTIONS);
 runtime.cancelPreparation?.();expect(f.stops).toBe(1);expect(f.closes).toBe(1);
 f.track.readyState="live";await runtime.preflight(DEFAULT_SOFTPHONE_AUDIO_OPTIONS);expect(f.opens).toBe(2);
 runtime.cancelPreparation?.();expect(f.stops).toBe(2);expect(f.closes).toBe(2);
});

test("a microphone disconnected during the answer request is never handed to media",async()=>{
 const f=fixture(),p=new AudioPreparation({},{});await p.ready;f.track.readyState="ended";
 expect(()=>p.take()).toThrow("Microphone disconnected");p.cancel();expect(f.closes).toBe(1);
});
