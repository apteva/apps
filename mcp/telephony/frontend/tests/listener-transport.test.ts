import {expect,test} from "bun:test";
import {selectableListenerAudio,type ListenerAudioCallbacks,type ListenerAudioConnection} from "../src/listener-audio";

for(const choice of ["websocket","webrtc","auto"] as const)test(`listener transport selection ${choice}`,async()=>{
 const starts:string[]=[],stops:string[]=[];
 const runtime=(name:string)=>(callbacks:ListenerAudioCallbacks):ListenerAudioConnection=>({async start(){starts.push(name);if(name==="rtc")throw new Error("ICE unavailable");callbacks.onReady("websocket");},stop(){stops.push(name);},setOutputVolume(){}});
 let transport:string|undefined;
 const connection=selectableListenerAudio({onReady:t=>{transport=t;},onClose(){}},choice,runtime("ws"),runtime("rtc"));
 if(choice==="webrtc")await expect(connection.start("ws://fixture")).rejects.toThrow();else await connection.start("ws://fixture");
 expect(starts).toEqual(choice==="websocket"?["ws"]:choice==="auto"?["rtc","ws"]:["rtc"]);
 if(choice!=="webrtc")expect(transport).toBe("websocket");connection.stop();
});

test("automatic listening never downgrades after ready or authorization/device denial",async()=>{
 for(const failure of [Object.assign(new Error("denied"),{status:403}),Object.assign(new Error("permission"),{name:"NotAllowedError"}),new Error("after-ready")]){
  let fallback=0;
  const connection=selectableListenerAudio({onReady(){},onClose(){}},"auto",()=>{fallback++;throw new Error("unexpected fallback");},callbacks=>({async start(){if(failure.message==="after-ready")callbacks.onReady("webrtc");throw failure;},stop(){},setOutputVolume(){}}));
  await expect(connection.start("ws://fixture")).rejects.toThrow();expect(fallback).toBe(0);connection.stop();
 }
});

test("cancellation while RTC negotiation is pending cannot launch fallback",async()=>{
 let reject!:(error:Error)=>void,fallback=0;
 const connection=selectableListenerAudio({onReady(){},onClose(){}},"auto",()=>{fallback++;throw new Error("unexpected fallback");},()=>({start(){return new Promise((_,r)=>reject=r);},stop(){},setOutputVolume(){}}));
 const pending=connection.start("ws://fixture");connection.stop();reject(new Error("closed"));await expect(pending).rejects.toThrow();expect(fallback).toBe(0);
});
