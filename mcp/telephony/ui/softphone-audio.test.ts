import { describe, expect, test } from "bun:test";
import { pcm16WAV } from "./softphone-audio";

describe("local microphone WAV", () => {
  test("writes mono PCM16 with an exact duration and payload", async () => {
    const samples = new Int16Array([0, 1234, -2345, 32767, -32768]);
    const wav = pcm16WAV([samples.subarray(0, 2), samples.subarray(2)], samples.length, 24000);
    const bytes = new Uint8Array(await wav.arrayBuffer());
    const view = new DataView(bytes.buffer);

    expect(wav.type).toBe("audio/wav");
    expect(new TextDecoder().decode(bytes.subarray(0, 4))).toBe("RIFF");
    expect(new TextDecoder().decode(bytes.subarray(8, 12))).toBe("WAVE");
    expect(view.getUint16(22, true)).toBe(1);
    expect(view.getUint32(24, true)).toBe(24000);
    expect(view.getUint16(34, true)).toBe(16);
    expect(view.getUint32(40, true)).toBe(samples.length * 2);
    expect(Array.from({ length: samples.length }, (_, index) => view.getInt16(44 + index * 2, true))).toEqual(Array.from(samples));
  });
});

test('carrier delivery notices preserve the healthy microphone and report recovery once', async()=>{
 const {SoftphoneSession}=await import('./softphone-audio');
 const notices:string[]=[];const states:unknown[]=[];
 const session:any=new SoftphoneSession({onNotice:n=>notices.push(n),onState:s=>states.push(s)});
 session.microphoneTransportReady=true;
 session.handleControl(JSON.stringify({type:'media.delivery',direction:'carrier_to_operator',state:'stalled'}));
 expect(session.microphoneTransportReady).toBe(true);
 session.handleControl(JSON.stringify({type:'media.delivery',state:'flowing'}));
 session.handleControl(JSON.stringify({type:'media.delivery',state:'flowing'}));
 expect(notices).toHaveLength(2);expect(states).toHaveLength(0);
});

test('mute and unmute events show the updated state and ignore duplicate commands',async()=>{
 const {SoftphoneSession}=await import('./softphone-audio');
 const events:any[]=[];
 const session:any=new SoftphoneSession({onSessionEvent:e=>{events.push({event:e,muted:session.isMuted});throw Error('observer');}});
 session.setMuted(true);session.setMuted(true);session.setMuted(false);
 expect(events.map(e=>[e.event.action,e.event.outcome,e.muted])).toEqual([['microphone','muted',true],['microphone','unmuted',false]]);
 expect(events.every(e=>Number.isFinite(Date.parse(e.event.timestamp)))).toBe(true);
 expect(session.closed).toBe(false);
});

test('playback reports retain timestamped Worker rejection events without duplication', async()=>{
 const {SoftphoneSession}=await import('./softphone-audio');
 const descriptor=Object.getOwnPropertyDescriptor(globalThis,'Worker');
 let worker:any;
 Object.defineProperty(globalThis,'Worker',{configurable:true,value:class {
  onmessage:any;onerror:any;constructor(){worker=this;}postMessage(){}terminate(){}
 }});
 const session:any=new SoftphoneSession();
 session.capture={port:{postMessage(){}}};session.playback={port:{postMessage(){}}};
 try {
  session.installWorkletDiagnostics();
  const started=session.openWorker('ws://unused','worker.js');
  worker.onmessage({data:{type:'socket.open'}});await started;
  const transport={timestamp:'2026-10-07T10:00:00.000Z',direction:'carrier_to_operator',reason:'playback_delivery_excess',duration_ms:20,sequence:1};
  const rendered={...transport,timestamp:'2026-10-07T10:00:00.020Z',reason:'playback_hard_limit',sequence:2};
  worker.onmessage({data:{type:'transport.drop',event:transport}});
  const report=()=>session.playback.port.onmessage({data:{type:'stats',drop_events:[rendered]}});
  report();report();
  expect(session.diagnostics.dropEvents).toEqual([transport,rendered]);
  session.playback.port.onmessage({data:{type:'stats',drop_events:[]}});
  expect(session.diagnostics.dropEvents).toEqual([transport]);
  for(let i=0;i<351;i++)worker.onmessage({data:{type:'transport.drop',event:{...transport,sequence:i+3}}});
  report();expect(session.diagnostics.dropEvents.length).toBeLessThanOrEqual(100);
 } finally {
  session.capture=null;session.playback=null;session.stop();
  if(descriptor)Object.defineProperty(globalThis,'Worker',descriptor);else Reflect.deleteProperty(globalThis,'Worker');
 }
});
