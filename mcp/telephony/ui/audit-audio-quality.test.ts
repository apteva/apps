// Audit-only: expected to fail on the unmodified 0.3.7 release.
import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { SoftphoneSession } from "./softphone-audio";

function worklet(rate = 24000) {
  const constructors: Record<string, any> = {};
  const context = vm.createContext({ sampleRate: rate, currentTime: 0,
    AudioWorkletProcessor: class { port = { messages: [] as any[], onmessage: null, postMessage(x: any) { this.messages.push(x); } }; },
    registerProcessor(name: string, ctor: any) { constructors[name] = ctor; },
  });
  vm.runInContext(readFileSync(new URL("./softphone-worklet.js", import.meta.url), "utf8"), context);
  return constructors;
}

test("audit: live microphone statistics must return to silence", () => {
  const C = worklet()["softphone-capture"];
  const p = new C({processorOptions:{inputGainDB:0,highpassFilter:false}});
  for (let n=0;n<375;n++) p.process([[new Float32Array(128).fill(0.1)]],[[new Float32Array(128)]]);
  for (let n=0;n<750;n++) p.process([[new Float32Array(128)]],[[new Float32Array(128)]]);
  p.reportStats();
  expect(p.port.messages.at(-1).active_rms).toBeLessThan(0.001);
});

test("audit: mute must clear unsent microphone samples", () => {
  const C = worklet()["softphone-capture"];
  const p = new C({processorOptions:{inputGainDB:0,highpassFilter:false}});
  // There is 256/480 of a frame waiting at the moment the operator mutes.
  for (let n=0;n<2;n++) p.process([[new Float32Array(128).fill(0.1)]],[[new Float32Array(128)]]);
  p.port.onmessage({data:{type:"muted",value:true}});
  for (let n=0;n<4;n++) p.process([[new Float32Array(128)]],[[new Float32Array(128)]]);
  const frame = p.port.messages.find((x: any) => typeof x.length === "number");
  expect(Array.from(frame).some((x: any) => Math.abs(x)>0.001)).toBe(false);
});

test("audit: unmute must not replay the pre-mute limiter delay", () => {
  const C = worklet()["softphone-capture"];
  const p = new C({processorOptions:{inputGainDB:0,highpassFilter:false}});
  p.process([[new Float32Array(128).fill(0.1)]],[[new Float32Array(128)]]);
  p.port.onmessage({data:{type:"muted",value:true}});
  for (let n=0;n<50;n++) p.process([[new Float32Array(128)]],[[new Float32Array(128)]]);
  p.port.onmessage({data:{type:"muted",value:false}});
  expect(Math.abs(p.filterAndLimit(0))).toBeLessThan(0.001);
});

test("audit: short playback bursts should not stay buffered forever", () => {
  const C = worklet()["softphone-playback"];
  const p = new C({processorOptions:{initialTargetMs:80}});
  p.handleMessage({frame:new Float32Array(480).fill(0.2),sequence:0,timestamp_ms:0});
  let heard = 0;
  for (let n=0;n<375;n++) { const output=new Float32Array(128); p.process([],[[output]]); for (const x of output) heard+=Math.abs(x); }
  expect(heard).toBeGreaterThan(0);
});

test("audit: a running worker failure must report an audio error", async () => {
  const original=Object.getOwnPropertyDescriptor(globalThis,"Worker");
  let worker:any;
  Object.defineProperty(globalThis,"Worker",{configurable:true,value:class {
    onmessage:any; onerror:any; constructor(){worker=this;} postMessage(){} terminate(){}
  }});
  const states:string[]=[];
  const session:any=new SoftphoneSession({onState:(state)=>states.push(state)});
  try {
    const pending=session.openWorker("ws://unused","worker.js");
    worker.onmessage({data:{type:"socket.open"}});
    await pending;
    session.handleControl(JSON.stringify({type:"peer.connected"}));
    worker.onerror({message:"worker crashed after startup"});
    expect(states).toContain("error");
    // A queued event from the failed worker must not revive its session or
    // overwrite the controls belonging to a later replacement session.
    worker.onmessage({data:{type:"socket.message",data:JSON.stringify({type:"peer.connected"})}});
    session.handleControl(JSON.stringify({type:"peer.connected"}));
    expect(states.at(-1)).toBe("error");
  } finally {
    session.stop();
    if(original) Object.defineProperty(globalThis,"Worker",original);
    else Reflect.deleteProperty(globalThis,"Worker");
  }
});
