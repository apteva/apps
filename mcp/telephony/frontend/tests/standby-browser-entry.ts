import {WebRTCAudioConnection,rtcStatistics} from "../src/webrtc-audio";
import {DEFAULT_SOFTPHONE_AUDIO_OPTIONS} from "../../ui/softphone-audio";

// Test-only: a ring-time connection in real Chromium, then the claim's devices
// on the same peer connection. Counts peer connections to prove no second one.
(window as any).runStandbyTest=async(standbyURL:string)=>{
 let peers=0;const Native=RTCPeerConnection;
 (window as any).RTCPeerConnection=class extends Native{constructor(config?:RTCConfiguration){super(config);peers++;}};
 const states:string[]=[],events:any[]=[];
 const options={...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"webrtc" as const};
 const connection=new WebRTCAudioConnection({onState:s=>states.push(s),onSessionEvent:e=>events.push(e)});
 const sleep=(ms:number)=>new Promise(r=>setTimeout(r,ms));
 const until=async(test:()=>boolean|Promise<boolean>,ms=15000)=>{const end=performance.now()+ms;while(!await test()){if(performance.now()>end)throw new Error("standby browser acceptance timed out");await sleep(50);}};
 const counters=(report:RTCStatsReport)=>{let sent=0,received=0;report.forEach((s:any)=>{if(s.type==="outbound-rtp")sent+=s.packetsSent??0;if(s.type==="inbound-rtp")received+=s.packetsReceived??0;});return{sent,received};};
 try{
  const standbyStarted=performance.now();
  await connection.standby(standbyURL,options);
  const standbyMs=performance.now()-standbyStarted;
  if(!connection.standing())throw new Error("ring-time connection not standing");
  const waiting=counters(await (connection as any).pc.getStats());
  await fetch("/parked",{method:"POST"});
  let claim:any;
  await until(async()=>{claim=await(await fetch("/claim")).json();return claim.ready===true;},20000);
  const promoteStarted=performance.now();
  await connection.start(claim.media_url,options,"/worklet.js");
  const promoteMs=performance.now()-promoteStarted;
  const pc:RTCPeerConnection=(connection as any).pc;
  await until(async()=>{const v=counters(await pc.getStats());return v.sent>waiting.sent+10&&v.received>10;});
  await until(()=>states.includes("live"));
  const report=await pc.getStats();
  const phases=events.filter(e=>e.duration_ms!==undefined).map(e=>`${e.action}:${e.outcome}:${Math.round(e.duration_ms)}`);
  return {standby_ms:standbyMs,promote_ms:promoteMs,phases,peers,states,waiting_sent:waiting.sent,...counters(report),path:rtcStatistics(report).webrtc,reconnects:events.filter(e=>e.action==="reconnect").length};
 }catch(error){const pc=(connection as any).pc;throw new Error(JSON.stringify({error:String(error),states,events:events.slice(-20),state:pc?.connectionState}));}
 finally{connection.stop();}
};
