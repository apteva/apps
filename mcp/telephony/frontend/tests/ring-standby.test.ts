import {afterEach,beforeEach,describe,expect,test} from "bun:test";
import {AptevaClient} from "@apteva/web-sdk";
import {telephonyExtension,type Call} from "../src/client";
import {WebRTCAudioConnection,selectableAudio} from "../src/webrtc-audio";
import {DEFAULT_SOFTPHONE_AUDIO_OPTIONS,type SoftphoneCallbacks} from "../../ui/softphone-audio";
import type {AudioConnection,AudioRuntime} from "../src/audio";
import {FakePeer,FakeSocket,fakeDevices,installFakeBrowser,settle} from "./rtc-fake-browser";

let restore:()=>void;
beforeEach(()=>{restore=installFakeBrowser();});
afterEach(()=>restore());

const options={...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"webrtc" as const};
const standbyURL="ws://telephony.test/api/apps/telephony/_install/42/softphone/media/call-1/standbytoken";

describe("a ring-time connection",()=>{
 async function parked(){
  const states:any[]=[],refreshed:any[]=[];
  const audio=new WebRTCAudioConnection({onState:(s:any,d:any)=>states.push([s,d]),refreshMediaURL:async()=>{refreshed.push(1);return "ws://telephony.test/next";}} as SoftphoneCallbacks);
  const pending=audio.standby(standbyURL,options);
  await settle();
  const socket=FakeSocket.instances[0];
  socket.open();socket.receive({type:"webrtc.config",ice_servers:[]});await settle();
  socket.receive({type:"webrtc.answer",sdp:"v=0 answer"});await settle();
  const peer=FakePeer.instances[0];peer.state("connected");
  await pending;
  return {audio,socket,peer,states,refreshed};
 }
 test("connects with no microphone or playback, and without the server's ready",async()=>{
  const c=await parked();
  expect(fakeDevices.opened).toBe(0);
  expect(fakeDevices.contexts).toBe(0);
  expect(c.peer.tracks).toBe(0);
  expect(c.peer.transceivers.map(t=>[t.kind,t.init.direction])).toEqual([["audio","sendrecv"]]);
  expect(c.socket.url).toContain("transport=webrtc");
  expect(c.audio.standing()).toBe(true);
  expect(c.states).toEqual([]);
  c.audio.stop();
  expect(c.audio.standing()).toBe(false);
 });
 test("start attaches the microphone to it, arms it and finishes on ready",async()=>{
  const c=await parked();
  let finished=false;
  const started=c.audio.start("ws://telephony.test/ignored",options,"worklet.js").then(()=>{finished=true;});
  await settle();
  expect(fakeDevices.opened).toBe(1);
  expect(c.peer.transceivers[0].sender.replaced).toHaveLength(1);
  expect(c.socket.sentOfType("standby.armed")).toHaveLength(1);
  expect(FakeSocket.instances).toHaveLength(1);
  expect(FakePeer.instances).toHaveLength(1);
  // The carrier is answered by the server after the arm; nothing is live before.
  expect(finished).toBe(false);
  expect(c.states).toEqual([]);
  c.socket.receive({type:"ready"});c.socket.receive({type:"peer.connected"});c.socket.receive({type:"call.status",status:"answered"});
  await started;
  expect(c.states.at(-1)?.[0]).toBe("live");
  expect(c.audio.standing()).toBe(false);
  c.audio.stop();
 });
 test("one that ends while waiting is simply gone, without recovery",async()=>{
  const c=await parked();
  c.socket.drop();await settle();
  expect(c.audio.standing()).toBe(false);
  expect(c.peer.closed).toBe(true);
  expect(c.states).toEqual([]);
  expect(c.refreshed).toEqual([]);
 });
 test("a carrier failure after arming fails the start",async()=>{
  const c=await parked();
  const started=c.audio.start(standbyURL,options,"worklet.js");
  await settle();
  c.socket.receive({type:"call.error",detail:"The carrier could not answer the call."});
  await expect(started).rejects.toThrow("carrier could not answer");
  c.audio.stop();
 });
 test("the server closing it while armed fails the start, without recovery",async()=>{
  const c=await parked();
  const started=c.audio.start(standbyURL,options,"worklet.js");
  await settle();
  c.socket.drop();
  await expect(started).rejects.toThrow();
  await settle();
  expect(c.refreshed).toEqual([]);
  c.audio.stop();
 });
 test("lost while the microphone is attached, it reports the loss so the answer connects anew",async()=>{
  const c=await parked();
  const started=c.audio.start(standbyURL,options,"worklet.js");
  c.socket.drop();
  const error=await started.then(()=>undefined,e=>e);
  expect(error?.code).toBe("standby_lost");
  expect(c.refreshed).toEqual([]);
  c.audio.stop();
 });
 test("a failed path while waiting ends it without recovery",async()=>{
  const c=await parked();
  c.peer.state("failed");await settle();
  expect(c.audio.standing()).toBe(false);
  expect(c.states).toEqual([]);
  expect(c.refreshed).toEqual([]);
 });
});

type Fake={standbyURL?:string;url?:string;alive:boolean;stopped:boolean;startError?:Error;cb:SoftphoneCallbacks};
function fakeConnection(created:Fake[],cb:SoftphoneCallbacks={} as SoftphoneCallbacks):AudioConnection{
 const state:Fake={alive:false,stopped:false,cb};created.push(state);
 return {
  async standby(url){state.standbyURL=url;state.alive=true;},
  standing:()=>state.alive&&!state.stopped,
  async start(url){state.url=url;if(state.startError)throw state.startError;state.alive=false;cb.onState?.("live");},
  stop(){state.stopped=true;state.alive=false;},setMuted(){},sendDTMF(){},setOutputVolume(){},
 };
}

describe("choosing a transport with a ring-time connection",()=>{
 test("starts on the ring-time connection when it is still open",async()=>{
  const created:Fake[]=[];
  const audio=selectableAudio({} as SoftphoneCallbacks,()=>fakeConnection(created),()=>fakeConnection(created));
  await audio.standby!(standbyURL,{...options,mediaTransport:"auto"});
  expect(audio.standing!()).toBe(true);
  await audio.start("ws://telephony.test/session",{...options,mediaTransport:"auto"});
  expect(created).toHaveLength(1);
  expect(created[0].url).toBe("ws://telephony.test/session");
 });
 test("connects anew when it ended before the answer",async()=>{
  const created:Fake[]=[];
  const audio=selectableAudio({} as SoftphoneCallbacks,()=>fakeConnection(created),()=>fakeConnection(created));
  await audio.standby!(standbyURL,options);
  created[0].alive=false;
  await audio.start("ws://telephony.test/session",options);
  expect(created).toHaveLength(2);
  expect(created[0].stopped).toBe(true);
  expect(created[1].url).toBe("ws://telephony.test/session");
 });
 test("connects anew when it ends while the answer attaches devices",async()=>{
  const created:Fake[]=[];
  const audio=selectableAudio({} as SoftphoneCallbacks,()=>fakeConnection(created),()=>fakeConnection(created));
  await audio.standby!(standbyURL,options);
  created[0].startError=Object.assign(new Error("Ring-time audio connection ended"),{code:"standby_lost"});
  await audio.start("ws://telephony.test/session",options);
  expect(created).toHaveLength(2);
  expect(created[0].stopped).toBe(true);
  expect(created[1].url).toBe("ws://telephony.test/session");
 });
 test("other failures on it are the answer's failure",async()=>{
  const created:Fake[]=[];
  const audio=selectableAudio({} as SoftphoneCallbacks,()=>fakeConnection(created),()=>fakeConnection(created));
  await audio.standby!(standbyURL,{...options,mediaTransport:"auto"});
  created[0].startError=new Error("The carrier could not answer the call.");
  await expect(audio.start("ws://telephony.test/session",{...options,mediaTransport:"auto"})).rejects.toThrow("carrier");
  expect(created).toHaveLength(1);
 });
 test("is never opened for WebSocket audio",async()=>{
  const audio=selectableAudio({} as SoftphoneCallbacks,()=>fakeConnection([]),()=>fakeConnection([]));
  await expect(audio.standby!(standbyURL,{...options,mediaTransport:"websocket"})).rejects.toThrow();
 });
});

const offered=(id="call-1"):Call=>({id,status:"pending",direction:"inbound",peer_kind:"human",answerable:true,ring_offers:[{id:"offer-"+id,kind:"browser",destination_id:"sales"}]} as unknown as Call);
const standbyID="s".repeat(64);
function controller(answer:(body:any)=>Record<string,unknown>,standby:()=>Response=()=>Response.json({standby_id:standbyID,call_id:"call-1",media_url:"/api/apps/telephony/_install/42/softphone/media/call-1/standbytok",expires_in_ms:20000}),softphone:Record<string,unknown>={}){
 const standbys:any[]=[],answers:any[]=[],created:Fake[]=[];
 const sdk=new AptevaClient({baseURL:"https://gateway.example",accessToken:"token",fetch:(async(url:unknown,init:any)=>{
  const path=new URL(String(url)).pathname;
  const body=init?.body?JSON.parse(String(init.body)):undefined;
  if(path.includes("/softphone/standby/")){standbys.push({path,body});return standby();}
  if(path.includes("/softphone/answer/")){answers.push(body);return Response.json({call_id:"call-1",media_url:"/api/apps/telephony/_install/42/softphone/media/call-1/tokclaim",session_token:"tok-claim",session_generation:"session-claim",lease_seconds:60,attachment_guard_version:1,...answer(body)});}
  if(path.includes("/softphone/renew/"))return Response.json({lease_seconds:60});
  return Response.json({});
 }) as typeof fetch});
 const client=sdk.use(telephonyExtension,{projectId:"p1",installId:42});
 const runtime:AudioRuntime={async preflight(){},create(cb){return fakeConnection(created,cb);}};
 const phone=client.createSoftphone({audioRuntime:runtime,pollIntervalMs:0,mediaTransport:"auto",...softphone});
 return {phone,standbys,answers,created};
}

describe("the softphone while a call rings",()=>{
 test("opens one ring-time connection and answers on it",async()=>{
  const c=controller(body=>body.standby_id===standbyID?{standby:"promoted"}:{});
  c.phone.observeCalls([offered(),offered("call-2")]);
  await settle();
  expect(c.standbys).toHaveLength(1);
  expect(c.standbys[0].body).toEqual({destination_id:"sales"});
  expect(c.created).toHaveLength(1);
  expect(c.created[0].standbyURL).toContain("/softphone/media/call-1/standbytok");
  // Repeated call lists change nothing.
  c.phone.observeCalls([offered(),offered("call-2")]);
  await settle();
  expect(c.standbys).toHaveLength(1);
  await c.phone.answer("call-1",{destination_id:"sales"});
  expect(c.answers[0].standby_id).toBe(standbyID);
  expect(c.created).toHaveLength(1);
  expect(c.created[0].url).toContain("tokclaim");
  expect(c.phone.getSnapshot().audioState).toBe("live");
  c.phone.dispose();
 });
 test("connects anew when the server did not promote it",async()=>{
  const c=controller(()=>({standby:"unavailable"}));
  c.phone.observeCalls([offered()]);
  await settle();
  await c.phone.answer("call-1",{destination_id:"sales"});
  expect(c.created).toHaveLength(2);
  expect(c.created[0].stopped).toBe(true);
  expect(c.created[1].url).toContain("tokclaim");
  c.phone.dispose();
 });
 test("keeps it while the answer claims the call, even when the call list moves on",async()=>{
  const c=controller(()=>({standby:"promoted"}));
  c.phone.observeCalls([offered()]);
  await settle();
  const answering=c.phone.answer("call-1",{destination_id:"sales"});
  c.phone.observeCalls([{...offered(),status:"answering"} as Call]);
  await answering;
  expect(c.created).toHaveLength(1);
  expect(c.created[0].stopped).toBe(false);
  c.phone.dispose();
 });
 test("closes it when the offer goes away, and does not reopen it for that call",async()=>{
  const c=controller(()=>({}));
  c.phone.observeCalls([offered()]);
  await settle();
  c.phone.observeCalls([]);
  expect(c.created[0].stopped).toBe(true);
  c.phone.observeCalls([offered("call-3")]);
  await settle();
  expect(c.standbys).toHaveLength(2);
  c.phone.dispose();
  expect(c.created[1].stopped).toBe(true);
 });
 test("a rejoin does not use it and closes it",async()=>{
  const c=controller(()=>({}));
  c.phone.observeCalls([offered(),offered("call-2")]);
  await settle();
  await c.phone.answer("call-1",{rejoin:true}).catch(()=>{});
  expect(c.answers[0]?.standby_id).toBeUndefined();
  expect(c.created[0].stopped).toBe(true);
  c.phone.dispose();
 });
 test("is not used when it is off, or for WebSocket audio",async()=>{
  for(const softphone of [{ringStandby:false},{mediaTransport:"websocket"}]){
   const c=controller(()=>({}),undefined,softphone);
   c.phone.observeCalls([offered()]);
   await settle();
   expect(c.standbys).toHaveLength(0);
   c.phone.dispose();
  }
 });
 test("an install without it is not asked again for each call",async()=>{
  const c=controller(()=>({}),()=>new Response("not found",{status:404}));
  c.phone.observeCalls([offered()]);
  await settle();
  c.phone.observeCalls([offered(),offered("call-2")]);
  await settle();
  expect(c.standbys).toHaveLength(1);
  c.phone.dispose();
 });
});
