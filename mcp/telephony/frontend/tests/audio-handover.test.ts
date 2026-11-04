import {afterEach,beforeEach,describe,expect,test} from "bun:test";
import {AptevaClient} from "@apteva/web-sdk";
import {telephonyExtension} from "../src/client";
import {WebRTCAudioConnection} from "../src/webrtc-audio";
import {DEFAULT_SOFTPHONE_AUDIO_OPTIONS,type SoftphoneCallbacks} from "../../ui/softphone-audio";
import type {AudioRuntime} from "../src/audio";
import {FakePeer,FakeSocket,completeSetup,installFakeBrowser,settle} from "./rtc-fake-browser";

let restore:()=>void;
beforeEach(()=>{restore=installFakeBrowser();});
afterEach(()=>restore());

const options={...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"webrtc" as const};
describe("a connection whose signaling drops",()=>{
 async function live(extra:Record<string,unknown>,startHandover?:SoftphoneCallbacks["startHandover"],age=10){
  const states:any[]=[],events:any[]=[],refreshed:any[]=[];
  const audio=new WebRTCAudioConnection({startHandover,onState:(s:any,d:any)=>states.push([s,d]),onSessionEvent:(e:any)=>events.push(e),refreshMediaURL:async()=>{refreshed.push(1);return "ws://telephony.test/next";}} as SoftphoneCallbacks);
  const running=audio.start("ws://telephony.test/first",options,"worklet.js");
  await settle();
  const socket=FakeSocket.instances[0],peer=await completeSetup(socket,0,extra);
  await running;
  peer.age=age;await (audio as any).statistics((audio as any).generation);
  return {audio,socket,peer,states,events,refreshed};
 }
 const reconnecting=(states:any[])=>states.some(s=>s[0]==="reconnecting");
 test("hands over without touching audio when the server offers it and audio is flowing",async()=>{
  const calls:any[]=[];
  let finish!:(ok:boolean)=>void;
  const c=await live({handover:true},(cause,evidence)=>{calls.push([cause,evidence]);return new Promise<boolean>(r=>finish=r);});
  c.socket.drop();await settle();
  expect(calls).toHaveLength(1);
  expect(calls[0][0]).toBe("signaling_closed");
  expect(calls[0][1].connection_state).toBe("connected");
  expect(calls[0][1].signaling_state).toBe("closed");
  expect(c.peer.closed).toBe(false);
  expect(reconnecting(c.states)).toBe(false);
  expect(c.refreshed).toEqual([]);
  finish(true);await settle();
  // The controller stops this connection after the swap; it must not recover on its own.
  expect(c.refreshed).toEqual([]);
  c.audio.stop();
 });
 test("takes the ordinary path when the server does not offer it",async()=>{
  const calls:any[]=[];
  const c=await live({},()=>{calls.push(1);return Promise.resolve(true);});
  c.socket.drop();await settle();
  expect(calls).toEqual([]);
  expect(reconnecting(c.states)).toBe(true);
  expect(c.peer.closed).toBe(true);
  c.audio.stop();
 });
 test("takes the ordinary path when audio has already stopped",async()=>{
  const calls:any[]=[];
  const c=await live({handover:true},()=>{calls.push(1);return Promise.resolve(true);},9000);
  c.socket.drop();await settle();
  expect(calls).toEqual([]);
  expect(reconnecting(c.states)).toBe(true);
  c.audio.stop();
 });
 test("takes the ordinary path when the controller cannot start one",async()=>{
  const c=await live({handover:true},()=>null);
  c.socket.drop();await settle();
  expect(reconnecting(c.states)).toBe(true);
  c.audio.stop();
 });
 test("a failed handover falls back to ordinary recovery with the original cause",async()=>{
  let finish!:(ok:boolean)=>void;
  const c=await live({handover:true},()=>new Promise<boolean>(r=>finish=r));
  c.socket.drop();await settle();
  expect(c.refreshed).toEqual([]);
  finish(false);await settle();
  expect(c.refreshed).toHaveLength(1);
  const started=c.events.find((e:any)=>e.action==="reconnect"&&e.outcome==="started");
  expect(started.code).toBe("signaling_closed");
  c.audio.stop();
 });
 test("if the old path also dies, the adviser is told and the later cause is recorded",async()=>{
  let finish!:(ok:boolean)=>void;
  const c=await live({handover:true},()=>new Promise<boolean>(r=>finish=r));
  c.socket.drop();await settle();
  c.peer.state("failed");await settle();
  expect(reconnecting(c.states)).toBe(true);
  // Still waiting for the replacement, not starting a second recovery beside it.
  expect(c.refreshed).toEqual([]);
  finish(false);await settle();
  expect(c.events.find((e:any)=>e.action==="reconnect"&&e.outcome==="started").code).toBe("webrtc_failed");
  c.audio.stop();
 });
 test("an authorization close never hands over",async()=>{
  const calls:any[]=[];
  const c=await live({handover:true},()=>{calls.push(1);return Promise.resolve(true);});
  c.socket.drop(1008);await settle();
  expect(calls).toEqual([]);
  c.audio.stop();
 });
});

type Connection={cb:SoftphoneCallbacks;url?:string;options?:any;stopped:boolean;muted?:boolean;volume?:number;start(url:string,options:any):Promise<void>;stop():void;setMuted(v:boolean):void;setOutputVolume(v:number):void;sendDTMF():void;release:(error?:unknown)=>void;resolveSetup:()=>void};
function controller(attach:(body:any,count:number)=>Response|Promise<Response>){
 const requests:any[]=[],created:Connection[]=[],records:any[]=[];
 const sdk=new AptevaClient({baseURL:"https://gateway.example",accessToken:"token",fetch:(async(url:unknown,init:any)=>{
  const path=new URL(String(url)).pathname;
  const body=init?.body?JSON.parse(String(init.body)):undefined;
  if(path.includes("/softphone/attach/")){requests.push(body);return attach(body,requests.length);}
  if(path.includes("/softphone/recovery/")){records.push(body.record);return new Response(null,{status:202});}
  if(path.includes("/softphone/renew/"))return Response.json({lease_seconds:60});
  if(path.endsWith("/calls"))return Response.json({calls:[{id:"call-1",status:"in-progress"}]});
  return Response.json({});
 }) as typeof fetch});
 const client=sdk.use(telephonyExtension,{projectId:"p1",installId:42});
 const runtime:AudioRuntime={async preflight(){},create(cb){
  const first=created.length===0;
  let settleStart!:(error?:unknown)=>void;
  const held=new Promise<void>((resolve,reject)=>{settleStart=error=>error?reject(error):resolve();});
  held.catch(()=>{});
  const connection:Connection={cb,stopped:false,release:error=>{if(!error)cb.onState?.("live");settleStart(error);},resolveSetup:()=>settleStart(),
   start(url,opts){connection.url=url;connection.options=opts;if(first){cb.onState?.("live");return Promise.resolve();}return held;},
   stop(){connection.stopped=true;settleStart(new Error("stopped"));},
   setMuted(v){connection.muted=v;},setOutputVolume(v){connection.volume=v;},sendDTMF(){}};
  created.push(connection);return connection as any;
 }};
 const phone=client.createSoftphone({audioRuntime:runtime,pollIntervalMs:0});
 return {phone,requests,created,records};
}
const issued=(generation:string,extra:Record<string,unknown>={})=>Response.json({call_id:"call-1",media_url:`/api/apps/telephony/_install/42/softphone/media/call-1/tok${generation.replace(/[^a-z0-9]/g,"")}`,session_token:`tok-${generation}`,session_generation:generation,lease_seconds:60,attachment_guard_version:1,...extra});
const refused=(code:string,current?:string)=>new Response(JSON.stringify({code,...(current?{current_generation:current}:{})}),{status:409,headers:{"content-type":"application/json"}});
const evidence={connection_state:"connected",signaling_state:"closed",last_packet_age_ms:15};
async function attached(attach:(body:any,count:number)=>Response|Promise<Response>){
 const c=controller(attach);
 await c.phone.attach("call-1");
 await settle();
 return c;
}
describe("the softphone controller during a handover",()=>{
 const first="session-aaa",second="session-bbb";
 test("keeps the old connection until the candidate is ready, then swaps with no gap",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):issued(second,{handover_pending:true}));
  const old=c.created[0];
  c.phone.setOutputVolume(0.4);
  const pending=old.cb.startHandover!("signaling_closed",evidence)!;
  await settle();
  const guard=c.requests[1].attachment_guard;
  expect(guard.handover).toBe(true);
  expect(guard.expected_generation).toBe(first);
  const candidate=c.created[1];
  expect(candidate.options.mediaTransport).toBe("webrtc");
  expect(candidate.options.setupLimits).toEqual({socketOpenMs:4000,mediaConnectMs:5000});
  expect(candidate.url).toContain("toksessionbbb");
  expect(old.stopped).toBe(false);
  expect(c.phone.getSnapshot().audioState).toBe("live");
  // Mute changes reach both while the swap is pending.
  c.phone.setMuted(true);
  expect(candidate.muted).toBe(true);
  candidate.release();
  expect(await pending).toBe(true);
  expect(old.stopped).toBe(true);
  expect(candidate.stopped).toBe(false);
  expect(candidate.volume).toBe(0.4);
  expect(c.phone.getSnapshot().audioState).toBe("live");
  await settle();
  expect(c.records.at(-1)).toMatchObject({outcome:"handed_over",trigger:"signaling_closed",attempts_total:1});
  expect(c.records.at(-1).lost_ms).toBeUndefined();
  // Later recoveries start from the new generation.
  c.phone.dispose();
 });
 test("the call stays live on screen while the candidate finishes its last messages",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):issued(second,{handover_pending:true}));
  const old=c.created[0],seen:string[]=[];
  const unsubscribe=c.phone.subscribe(()=>{seen.push(c.phone.getSnapshot().audioState);});
  const pending=old.cb.startHandover!("signaling_closed",evidence)!;
  await settle();
  const candidate=c.created[1];
  // Real order: setup resolves with the path connected, then ready and the call status.
  candidate.cb.onState?.("reconnecting","Connecting carrier audio…");
  candidate.resolveSetup();
  await settle();
  expect(old.stopped).toBe(false);
  candidate.cb.onState?.("live");
  expect(await pending).toBe(true);
  expect(old.stopped).toBe(true);
  expect(seen).not.toContain("reconnecting");
  expect(c.phone.getSnapshot().audioState).toBe("live");
  unsubscribe();
  c.phone.dispose();
 });
 test("a candidate that never goes live is swapped in and shows its real state",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):issued(second,{handover_pending:true}));
  const old=c.created[0];
  const pending=old.cb.startHandover!("signaling_closed",evidence)!;
  await settle();
  const candidate=c.created[1];
  candidate.cb.onState?.("reconnecting","Connecting carrier audio…");
  candidate.resolveSetup();
  expect(await pending).toBe(true);
  expect(old.stopped).toBe(true);
  expect(c.phone.getSnapshot().audioState).toBe("reconnecting");
  c.phone.dispose();
 });
 test("a candidate that fails before going live counts as a failed attempt",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):issued(`session-${n}aa`,{handover_pending:true}));
  const old=c.created[0];
  const pending=old.cb.startHandover!("signaling_closed",evidence)!;
  await settle();
  const candidate=c.created[1];
  candidate.resolveSetup();
  await settle();
  candidate.cb.onState?.("error","candidate problem");
  await settle();
  expect(candidate.stopped).toBe(true);
  expect(old.stopped).toBe(false);
  expect(c.phone.getSnapshot().audioState).toBe("live");
  c.created[2].release(new Error("still no path"));
  expect(await pending).toBe(false);
  c.phone.dispose();
 });
 test("a candidate cannot change call state before it is current",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):issued(second,{handover_pending:true}));
  const old=c.created[0];
  const pending=old.cb.startHandover!("signaling_closed",evidence)!;
  await settle();
  const candidate=c.created[1];
  candidate.cb.onState?.("error","candidate problem");
  candidate.cb.onState?.("ended","call.ended");
  candidate.cb.onCallStatus?.({call_id:"call-1",status:"completed"} as any);
  await settle();
  expect(c.phone.getSnapshot().callId).toBe("call-1");
  expect(c.phone.getSnapshot().audioState).toBe("live");
  expect(old.stopped).toBe(false);
  candidate.stop();
  await settle();
  c.created[2].stop();
  expect(await pending).toBe(false);
  c.phone.dispose();
 });
 test("candidates that fail twice leave the old connection in place and report it",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):issued(`session-${n}aa`,{handover_pending:true}));
  const old=c.created[0];
  const pending=old.cb.startHandover!("signaling_closed",evidence)!;
  await settle();
  c.created[1].release(Object.assign(new Error("slow"),{code:"socket_open_timeout"}));
  await settle();
  expect(c.created[2].options.setupLimits).toEqual({socketOpenMs:6000,mediaConnectMs:8000});
  c.created[2].release(Object.assign(new Error("slow"),{code:"media_connect_timeout"}));
  expect(await pending).toBe(false);
  expect(old.stopped).toBe(false);
  await settle();
  expect(c.records.at(-1)).toMatchObject({outcome:"handover_failed",attempts_total:2});
  expect(c.records.at(-1).attempts.map((a:any)=>a.code)).toEqual(["socket_open_timeout","media_connect_timeout"]);
  c.phone.dispose();
 });
 test("a server with handover off ends it after one request",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):refused("handover_unavailable"));
  expect(await c.created[0].cb.startHandover!("signaling_closed",evidence)!).toBe(false);
  expect(c.requests).toHaveLength(2);
  expect(c.created).toHaveLength(1);
  c.phone.dispose();
 });
 test("a takeover seen as a changed generation is never retried",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):refused("media_session_changed","session-ccc"));
  expect(await c.created[0].cb.startHandover!("signaling_closed",evidence)!).toBe(false);
  expect(c.requests).toHaveLength(2);
  c.phone.dispose();
 });
 test("an unseen commit by its own candidate is recognised, not treated as a takeover",async()=>{
  const c=await attached((body,n)=>{
   if(n===1)return issued(first);
   if(n===2)return issued(second,{handover_pending:true});
   // The first candidate committed on the server, then died before saying so.
   if(body.attachment_guard.expected_generation===first)return refused("media_session_changed",second);
   return issued("session-ddd",{handover_pending:true});
  });
  const pending=c.created[0].cb.startHandover!("signaling_closed",evidence)!;
  await settle();
  c.created[1].release(new Error("died after commit"));
  await settle();
  expect(c.requests[2].attachment_guard.expected_generation).toBe(first);
  // Both attempts are spent, so it reports failure and ordinary recovery continues.
  expect(await pending).toBe(false);
  // Ordinary recovery recognises the generation and replaces its own session.
  const recovered=await attachOrdinary(c);
  expect(recovered).toContain("/softphone/media/call-1/");
  expect(c.requests.at(-1).attachment_guard.expected_generation).toBe(second);
  c.phone.dispose();
 });
 async function attachOrdinary(c:ReturnType<typeof controller>){
  return c.created[0].cb.refreshMediaURL!({recovery_id:"recovery-1",attempt_id:"attempt-1",initiating_action:"automatic_retry"});
 }
 test("hanging up during a handover stops the candidate and reports the cancellation",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):issued(second,{handover_pending:true}));
  const pending=c.created[0].cb.startHandover!("signaling_closed",evidence)!;
  await settle();
  const candidate=c.created[1];
  void c.phone.hangup().catch(()=>{});
  expect(candidate.stopped).toBe(true);
  expect(await pending).toBe(false);
  await settle();
  expect(c.records.at(-1)?.outcome).toBe("cancelled");
  c.phone.dispose();
 });
 test("only one handover runs at a time, and none while audio is not live",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):issued(second,{handover_pending:true}));
  const old=c.created[0];
  const pending=old.cb.startHandover!("signaling_closed",evidence)!;
  expect(old.cb.startHandover!("signaling_closed",evidence)).toBeNull();
  await settle();
  c.created[1].stop();
  await settle();
  c.created[2].stop();
  await pending;
  old.cb.onState?.("reconnecting","x");
  expect(old.cb.startHandover!("signaling_closed",evidence)).toBeNull();
  c.phone.dispose();
 });
 test("a lease that ends during a handover does not stop audio",async()=>{
  const c=await attached((_,n)=>n===1?issued(first):issued(second,{handover_pending:true}));
  const old=c.created[0];
  const pending=old.cb.startHandover!("signaling_closed",evidence)!;
  await settle();
  (c.phone as any).lease.finish("revoked",Object.assign(new Error("replaced"),{status:403}));
  expect(old.stopped).toBe(false);
  expect(c.phone.getSnapshot().audioState).toBe("live");
  c.created[1].release();
  expect(await pending).toBe(true);
  expect((c.phone as any).lease).toBeDefined();
  c.phone.dispose();
 });
});
