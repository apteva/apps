import {afterEach,beforeEach,describe,expect,spyOn,test} from "bun:test";
import {RecoveryRecorder,recoveryPathCheck,runRecoveryChecks} from "../src/media-recovery-record";
import {WebRTCAudioConnection,rtcRecoveryLimits,permanentRTCFailure} from "../src/webrtc-audio";
import {DEFAULT_SOFTPHONE_AUDIO_OPTIONS} from "../../ui/softphone-audio";
import {FakePeer,FakeSocket,completeSetup,installFakeBrowser,settle} from "./rtc-fake-browser";

test("the record keeps the stage each attempt reached and stays bounded",()=>{
 let now=1000;
 const r=new RecoveryRecorder("recovery-1","signaling_closed",{connection_state:"failed",last_packet_age_ms:7000},()=>now,()=>"2026-11-04T15:02:40.000Z");
 now=1500;r.attempt("attempt-1");r.stage("session");now=5500;r.attemptFailed("socket_open_timeout");
 now=6500;r.attempt("attempt-2");r.stage("session");r.stage("socket_open");r.stage("ready");now=7500;
 const done=r.finish("connected");
 expect(done).toMatchObject({recovery_id:"recovery-1",trigger:"signaling_closed",outcome:"connected",lost_ms:6500,attempts_total:2,last_packet_age_ms:7000,connection_state:"failed"});
 expect(done.attempts).toEqual([
  {attempt_id:"attempt-1",at_ms:500,duration_ms:4000,reached:"session",outcome:"failed",code:"socket_open_timeout"},
  {attempt_id:"attempt-2",at_ms:5500,duration_ms:1000,reached:"ready",outcome:"connected"},
 ]);
 expect(r.finish("timed_out").outcome).toBe("connected");
});
test("many attempts keep the first few and the latest",()=>{
 const r=new RecoveryRecorder("recovery-1","signaling_closed",{},()=>0);
 for(let i=1;i<=20;i++)r.attempt(`attempt-${i}`);
 const done=r.finish("timed_out");
 expect(done.attempts_total).toBe(20);expect(done.attempts).toHaveLength(5);
 expect(done.attempts.map(a=>a.attempt_id)).toEqual(["attempt-1","attempt-2","attempt-3","attempt-4","attempt-20"]);
 expect(new TextEncoder().encode(JSON.stringify(done)).byteLength).toBeLessThan(8192);
 expect(done.lost_ms).toBeUndefined();
});
test("path checks are uncredentialed, bounded and report failure as evidence",async()=>{
 const seen:any[]=[];
 const ok=await recoveryPathCheck("https://telephony.test/health",3000,(async(url:string,init:any)=>{seen.push([url,init]);return new Response();}) as any);
 expect(ok.ok).toBe(true);
 expect(seen[0][1]).toMatchObject({method:"HEAD",mode:"no-cors",cache:"no-store",credentials:"omit"});
 const down=await recoveryPathCheck("https://telephony.test/health",3000,(async()=>{throw new TypeError("network");}) as any);
 expect(down.ok).toBe(false);
 const hung=await recoveryPathCheck("https://telephony.test/health",5,((_:string,init:any)=>new Promise((_,reject)=>init.signal.addEventListener("abort",()=>reject(new Error("aborted"))))) as any);
 expect(hung.ok).toBe(false);
 const urls:string[]=[];
 const r=new RecoveryRecorder("recovery-1","signaling_closed",{});
 await runRecoveryChecks(r,"wss://telephony.test/softphone/media/call/token?session_generation=x","https://crm.test",(async(url:string)=>{urls.push(url);return new Response();}) as any);
 expect(urls.sort()).toEqual(["https://crm.test/","https://telephony.test/health"]);
 expect(r.record.checks?.telephony?.ok).toBe(true);expect(r.record.checks?.page?.ok).toBe(true);
 urls.length=0;
 await runRecoveryChecks(new RecoveryRecorder("recovery-2","x",{}),"wss://same.test/x","https://same.test",(async(url:string)=>{urls.push(url);return new Response();}) as any);
 expect(urls).toEqual(["https://same.test/health"]);
});

test("only the first two automatic retries get shorter limits, and those failures are retried",()=>{
 expect(rtcRecoveryLimits(1)).toEqual({socketOpenMs:4000,mediaConnectMs:5000});
 expect(rtcRecoveryLimits(2)).toEqual({socketOpenMs:6000,mediaConnectMs:8000});
 expect(rtcRecoveryLimits(3)).toBeUndefined();
 for(const code of ["socket_open_timeout","media_connect_timeout"])expect(permanentRTCFailure(Object.assign(new Error("x"),{code}))).toBe(false);
});

let restore:()=>void;
beforeEach(()=>{restore=installFakeBrowser();});
afterEach(()=>restore());

// setTimeout is real except the delays a test names, which it fires by hand.
function timers(names:number[]){
 const original=setTimeout,originalClear=clearTimeout,held=new Map<object,{ms:number;run:()=>void}>();
 const spy=spyOn(globalThis,"setTimeout").mockImplementation(((fn:any,ms:number,...args:any[])=>{
  if(names.includes(ms)){const id={};held.set(id,{ms,run:()=>fn(...args)});return id as any;}
  return original(fn,ms,...args);
 }) as typeof setTimeout);
 const clear=spyOn(globalThis,"clearTimeout").mockImplementation(((id:any)=>{if(!held.delete(id))originalClear(id);}) as typeof clearTimeout);
 return {fire(ms:number){for(const [id,entry] of [...held])if(entry.ms===ms){held.delete(id);entry.run();}},pending:(ms:number)=>[...held.values()].filter(e=>e.ms===ms).length,restore(){spy.mockRestore();clear.mockRestore();}};
}
const options={...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"webrtc" as const};
function connection(extra:Record<string,unknown>={}){
 const records:any[]=[],states:any[]=[],events:any[]=[];
 const urls=["ws://telephony.test/second","ws://telephony.test/third","ws://telephony.test/fourth"];
 const audio=new WebRTCAudioConnection({onRecoveryRecord:(r:any)=>records.push(JSON.parse(JSON.stringify(r))),onState:(s:any,d:any)=>states.push([s,d]),onSessionEvent:(e:any)=>events.push(e),refreshMediaURL:async()=>urls.shift()!,...extra} as any);
 return {audio,records,states,events};
}
async function live(audio:WebRTCAudioConnection){
 const socket=FakeSocket.instances.length,index=FakePeer.instances.length;
 const started=audio.start("ws://telephony.test/first",options,"worklet.js");
 await settle();
 const peer=await completeSetup(FakeSocket.instances[socket],index);
 await started;
 return peer;
}

test("a stalled socket fails the first retry at 4 s, and a hung path at 5 s after the answer",async()=>{
 const clock=timers([1000,4000,5000,6000,8000,30000]);
 const {audio,records,events}=connection();
 try{
  const peer=await live(audio);
  FakeSocket.instances[0].drop();await settle();
  // Attempt 1: the socket never opens.
  expect(FakeSocket.instances).toHaveLength(2);expect(clock.pending(4000)).toBe(1);
  clock.fire(4000);await settle();
  const failed=events.filter(e=>e.outcome==="attempt_failed");
  expect(failed.at(-1).code).toBe("socket_open_timeout");
  // Attempt 2: the socket opens and is answered, but the path does not connect.
  clock.fire(1000);await settle();
  const second=FakeSocket.instances[2];expect(clock.pending(6000)).toBe(1);
  second.open();second.receive({type:"webrtc.config",ice_servers:[]});await settle();
  second.receive({type:"webrtc.answer",sdp:"v=0"});await settle();
  expect(clock.pending(8000)).toBe(1);
  clock.fire(8000);await settle();
  expect(events.filter(e=>e.outcome==="attempt_failed").at(-1).code).toBe("media_connect_timeout");
  // Attempt 3 has the full limit only.
  clock.fire(1000);await settle();
  const third=FakeSocket.instances[3];expect(clock.pending(4000)+clock.pending(6000)).toBe(0);
  await completeSetup(third,FakePeer.instances.length);
  await settle();
  expect(records).toHaveLength(1);
  const record=records[0];
  expect(record.outcome).toBe("connected");expect(record.trigger).toBe("signaling_closed");expect(record.attempts_total).toBe(3);
  expect(record.attempts.map((a:any)=>[a.reached,a.outcome,a.code])).toEqual([["session","failed","socket_open_timeout"],["answered","failed","media_connect_timeout"],["ready","connected",undefined]]);
  expect(record.connection_state).toBe("connected");expect(record.signaling_state).toBe("closed");expect(record.microphone).toBe("live");expect(record.online).toBe(true);
  expect(peer.closed).toBe(true);
 }finally{audio.stop();clock.restore();}
});

test("the first connection keeps its full limit, and a required relay never gets the short media limit",async()=>{
 const clock=timers([1000,4000,5000,6000,8000,18000,30000]);
 const {audio}=connection();
 try{
  const started=audio.start("ws://telephony.test/first",options,"worklet.js");await settle();
  expect(clock.pending(18000)).toBe(1);expect(clock.pending(4000)).toBe(0);
  await completeSetup(FakeSocket.instances[0],0);await started;
  FakeSocket.instances[0].drop();await settle();
  const retry=FakeSocket.instances[1];retry.open();
  retry.receive({type:"webrtc.config",ice_servers:[{urls:"turn:relay.test"}],ice_transport_policy:"relay"});await settle();
  retry.receive({type:"webrtc.answer",sdp:"v=0"});await settle();
  expect(clock.pending(5000)).toBe(0);
 }finally{audio.stop();clock.restore();}
});

test("timed out, revoked and cancelled recoveries each send one record",async()=>{
 const clock=timers([1000,4000,30000]);
 try{
  const timedOut=connection();
  await live(timedOut.audio);FakeSocket.instances.at(-1)!.drop();await settle();
  clock.fire(30000);await settle();
  expect(timedOut.records.map(r=>r.outcome)).toEqual(["timed_out"]);
  expect(timedOut.states.at(-1)[0]).toBe("error");
  timedOut.audio.stop();await settle();expect(timedOut.records).toHaveLength(1);

  const revoked=connection({refreshMediaURL:async()=>{throw Object.assign(new Error("gone"),{status:403});}});
  await live(revoked.audio);FakeSocket.instances.at(-1)!.drop();await settle();
  expect(revoked.records.map(r=>r.outcome)).toEqual(["revoked"]);
  revoked.audio.stop();

  const cancelled=connection();
  await live(cancelled.audio);FakeSocket.instances.at(-1)!.drop();await settle();
  cancelled.audio.stop();await settle();
  expect(cancelled.records.map(r=>r.outcome)).toEqual(["cancelled"]);
 }finally{clock.restore();}
});

test("a healthy call sends no record and runs no path checks",async()=>{
 const previous=Object.getOwnPropertyDescriptor(globalThis,"location");
 Object.defineProperty(globalThis,"location",{configurable:true,value:{origin:"https://crm.test",href:"https://crm.test/"}});
 const fetches=spyOn(globalThis,"fetch").mockImplementation((async()=>new Response()) as any);
 const {audio,records}=connection();
 try{
  await live(audio);await settle();
  expect(fetches).toHaveBeenCalledTimes(0);expect(records).toEqual([]);
  // Interrupted: exactly one round, for this recovery.
  FakeSocket.instances[0].drop();await settle();
  expect(fetches).toHaveBeenCalledTimes(2);
 }finally{audio.stop();fetches.mockRestore();if(previous)Object.defineProperty(globalThis,"location",previous);else Reflect.deleteProperty(globalThis,"location");}
});

describe("stalled audio path recovery",()=>{
 const STALL=3000;
 const advertised={ice_recover_supported:true};
 async function started(extra:Record<string,unknown>=advertised){
  const c=connection();
  const socket=FakeSocket.instances.length,index=FakePeer.instances.length;
  const running=c.audio.start("ws://telephony.test/first",options,"worklet.js");
  await settle();
  const peer=await completeSetup(FakeSocket.instances[socket],index,extra);
  await running;
  return {...c,peer,socket:FakeSocket.instances[socket]};
 }
 test("a short disconnection that clears by itself sends nothing",async()=>{
  const clock=timers([STALL]);
  try{
   const {audio,peer,socket}=await started();
   peer.state("disconnected");expect(clock.pending(STALL)).toBe(1);
   peer.state("connected");expect(clock.pending(STALL)).toBe(0);
   expect(socket.sentOfType("webrtc.ice_recover")).toEqual([]);
   audio.stop();
  }finally{clock.restore();}
 });
 test("off by default: a stall waits for the browser, as it does today",async()=>{
  const clock=timers([STALL]);
  try{
   const {audio,peer,socket}=await started({});
   peer.state("disconnected");
   expect(clock.pending(STALL)).toBe(0);
   expect(socket.sentOfType("webrtc.ice_recover")).toEqual([]);
   audio.stop();
  }finally{clock.restore();}
 });
 test("a sustained stall restarts ICE on the same connection and keeps the microphone",async()=>{
  const clock=timers([STALL]);
  try{
   const {audio,peer,socket,states}=await started();
   const peers=FakePeer.instances.length,sockets=FakeSocket.instances.length;
   peer.state("disconnected");clock.fire(STALL);
   expect(socket.sentOfType("webrtc.ice_recover")).toHaveLength(1);
   socket.receive({type:"webrtc.ice_config",revision:2,recovery:true,ice_servers:[],ice_transport_policy:"all",expires_at:0});
   expect(clock.pending(STALL)).toBe(0);
   await settle();
   expect(peer.offers.at(-1)).toEqual({iceRestart:true});
   const offer=socket.sentOfType("webrtc.restart_offer")[0];
   expect(offer.revision).toBe(2);
   expect([FakePeer.instances.length,FakeSocket.instances.length]).toEqual([peers,sockets]);
   expect(states.map(s=>s[0])).not.toContain("reconnecting");
   audio.stop();
  }finally{clock.restore();}
 });
 test("no answer with audio still arriving leaves the call alone",async()=>{
  const clock=timers([STALL]);
  try{
   const {audio,peer,records,states}=await started();
   peer.age=10;await (audio as any).statistics((audio as any).generation);
   peer.state("disconnected");clock.fire(STALL);clock.fire(STALL);
   await settle();
   expect(FakePeer.instances).toHaveLength(1);
   expect(records).toEqual([]);
   expect(states.map(s=>s[0])).not.toContain("reconnecting");
   audio.stop();
  }finally{clock.restore();}
 });
 test("no answer with audio stopped moves to reconnection without waiting for the browser",async()=>{
  const clock=timers([STALL]);
  try{
   const {audio,peer,states}=await started();
   peer.age=9000;await (audio as any).statistics((audio as any).generation);
   peer.state("disconnected");clock.fire(STALL);clock.fire(STALL);
   await settle();
   expect(states.map(s=>s[0])).toContain("reconnecting");
   expect(peer.closed).toBe(true);
   audio.stop();
  }finally{clock.restore();}
 });
 test("a server that refuses a repeat request counts as signaling alive",async()=>{
  const clock=timers([STALL]);
  try{
   const {audio,peer,socket,states}=await started();
   peer.age=9000;await (audio as any).statistics((audio as any).generation);
   peer.state("disconnected");clock.fire(STALL);
   socket.receive({type:"webrtc.ice_status",state:"recovery_busy"});
   expect(clock.pending(STALL)).toBe(0);
   expect(states.map(s=>s[0])).not.toContain("reconnecting");
   audio.stop();
  }finally{clock.restore();}
 });
 test("a server-closed path still takes the existing route to reconnection",async()=>{
  const clock=timers([STALL]);
  try{
   const {audio,peer,states}=await started();
   peer.state("disconnected");
   peer.state("failed");
   expect(clock.pending(STALL)).toBe(0);
   expect(states.map(s=>s[0])).toContain("reconnecting");
   audio.stop();
  }finally{clock.restore();}
 });
});
