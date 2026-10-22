import {WebRTCAudioConnection,rtcStatistics} from "../src/webrtc-audio";
import {DEFAULT_SOFTPHONE_AUDIO_OPTIONS} from "../../ui/softphone-audio";

// Test-only access to the attachment proves that renewal keeps its actual peer
// and sender. No production API exposes credentials or test infrastructure.
(window as any).runTURNTest=async(url:string)=>{
 const events:any[]=[];
 const connection=new WebRTCAudioConnection({onState(){},onSessionEvent:e=>events.push(e)});
 const until=async(test:()=>boolean|Promise<boolean>,ms=15000)=>{
  const end=performance.now()+ms;
  while(!await test()){if(performance.now()>end)throw new Error("TURN browser acceptance timed out");await new Promise(r=>setTimeout(r,50));}
 };
 const counters=(report:RTCStatsReport)=>{let sent=0,received=0;report.forEach((s:any)=>{if(s.type==="outbound-rtp")sent+=s.packetsSent??0;if(s.type==="inbound-rtp")received+=s.packetsReceived??0;});return{sent,received};};
 try {
  await connection.start(url,{...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"webrtc"},"/worklet.js");
  const peer:RTCPeerConnection=(connection as any).pc;
  const sender=peer.getSenders()[0];
  await until(async()=>{const v=counters(await peer.getStats());return v.sent>3&&v.received>3;});
  const before=counters(await peer.getStats());connection.setMuted(true);
  (connection as any).send({type:"webrtc.ice_refresh"});
  await until(()=>events.some(e=>e.action==="ice_restart"&&e.outcome==="restart_completed"&&e.code==="ice_rev_2"));
  await until(()=>peer.iceConnectionState==="connected"||peer.iceConnectionState==="completed");
  await until(async()=>counters(await peer.getStats()).received>before.received+5);
  const report=await peer.getStats(),path=rtcStatistics(report).webrtc;
  if(path.candidateType!=="relay")throw new Error("Browser bypassed required TURN relay");
  if(peer!==(connection as any).pc||peer.getSenders()[0]!==sender||sender.track?.enabled!==false)throw new Error("Renewal replaced the peer/sender or lost mute");
  if(events.some(e=>e.action==="reconnect"))throw new Error("Renewal recreated the attachment");
  return {before,after:counters(report),path,events:events.filter(e=>e.action==="ice_restart"),peer_preserved:true,mute_preserved:true};
 } finally {connection.stop();}
};
