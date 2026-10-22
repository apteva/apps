import {WebRTCAudioConnection,rtcStatistics} from "../src/webrtc-audio";
import {DEFAULT_SOFTPHONE_AUDIO_OPTIONS} from "../../ui/softphone-audio";

// Test-only inspection. Delay applying each renewed answer by 400 ms: media
// must keep flowing on the original nominated path while signaling is pending.
(window as any).runTURNTest=async(url:string,mode:string,seconds=16)=>{
 const events:any[]=[];
 const connection=new WebRTCAudioConnection({onState(){},onSessionEvent:e=>events.push(e)});
 const sleep=(ms:number)=>new Promise(r=>setTimeout(r,ms));
 const until=async(test:()=>boolean|Promise<boolean>,ms=15000)=>{
  const end=performance.now()+ms;
  while(!await test()){if(performance.now()>end)throw new Error("TURN browser acceptance timed out");await sleep(50);}
 };
 const counters=(report:RTCStatsReport)=>{let sent=0,received=0,lost=0,remoteLost=0,remoteReports=0;report.forEach((s:any)=>{if(s.type==="outbound-rtp")sent+=s.packetsSent??0;if(s.type==="inbound-rtp"){received+=s.packetsReceived??0;lost+=s.packetsLost??0;}if(s.type==="remote-inbound-rtp"){remoteLost+=s.packetsLost??0;remoteReports++;}});return{sent,received,lost,remoteLost,remoteReports};};
 let monitor:ReturnType<typeof setInterval>|undefined;
 try {
  await connection.start(url,{...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"webrtc"},"/worklet.js");
  const peer:RTCPeerConnection=(connection as any).pc,sender=peer.getSenders()[0];
  await until(async()=>{const v=counters(await peer.getStats());return v.sent>10&&v.received>10;});
  const before=counters(await peer.getStats()),start=performance.now();
  let delayedAnswers=0,maxGap=0,lastReceived=before.received,lastProgress=performance.now(),sampling=false;
  const original=peer.setRemoteDescription.bind(peer);
  peer.setRemoteDescription=async(description)=>{delayedAnswers++;await sleep(400);return original(description);};
  monitor=setInterval(async()=>{if(sampling)return;sampling=true;try{const v=counters(await peer.getStats()),now=performance.now();if(v.received>lastReceived){maxGap=Math.max(maxGap,now-lastProgress);lastReceived=v.received;lastProgress=now;}}finally{sampling=false;}},50);
  for(let revision=2;revision<=3;revision++){
   if(revision===3)await sleep(11000); // credential-service request debounce
   connection.setMuted(true);
   (connection as any).send({type:"webrtc.ice_refresh"});
   await until(()=>events.some(e=>e.action==="ice_restart"&&e.outcome===(mode==="direct"?"credentials_applied_direct":"restart_completed")&&e.code===`ice_rev_${revision}`));
   if(peer!==(connection as any).pc||peer.getSenders()[0]!==sender||sender.track?.enabled!==false)throw new Error("Renewal replaced peer/sender or lost mute");
   connection.setMuted(false);
   const target=lastReceived+5;await until(async()=>counters(await peer.getStats()).received>target);
  }
  await sleep(Math.max(2000,seconds*1000-(performance.now()-start))); // includes remote RTCP evidence and post-cleanup continuity
  clearInterval(monitor);monitor=undefined;
  const report=await peer.getStats(),after=counters(report),path=rtcStatistics(report).webrtc;
  if(mode!=="direct"&&path.candidateType!=="relay")throw new Error("Browser bypassed required TURN relay");
  if(mode==="direct"&&(path.candidateType==="relay"||delayedAnswers!==0))throw new Error("Direct media restarted during credential refresh");
  if(mode!=="direct"&&delayedAnswers!==2)throw new Error("Renewal did not exercise both delayed answers");
  if(after.remoteReports<1)throw new Error("Remote RTP loss evidence unavailable");
  const loss=after.lost-before.lost,remoteLoss=after.remoteLost-before.remoteLost;
  if(loss>0||remoteLoss>0||maxGap>180)throw new Error(`Renewal interrupted media: lost=${loss} remote_lost=${remoteLoss} observed_gap_ms=${maxGap}`);
  if(events.some(e=>e.action==="reconnect"))throw new Error("Renewal recreated attachment");
  return {before,after,path,packet_loss_delta:loss,remote_packet_loss_delta:remoteLoss,max_receive_observation_gap_ms:maxGap,delayed_answers:delayedAnswers,events:events.filter(e=>e.action==="ice_restart"),peer_preserved:true,mute_preserved:true};
 } finally {if(monitor)clearInterval(monitor);connection.stop();}
};
