import {mediaDiagnosticID, type MediaSessionEvent} from "./media-lease";
/** Shared adviser/listening/coaching ICE control. Credentials exist only in
 * RTCPeerConnection configuration and this attachment's memory, never telemetry. */
export interface RTCICEOptions {
 turnTransport?: "all"|"udp"|"tcp"|"tls";
 iceTransportPolicy?: "all"|"relay";
 turnTLSPort?: number;
}
export function validateRTCICEOptions(value:RTCICEOptions):void {
 if(value.turnTransport!==undefined&&!["all","udp","tcp","tls"].includes(value.turnTransport))throw new RangeError("Invalid TURN transport");
 if(value.iceTransportPolicy!==undefined&&!["all","relay"].includes(value.iceTransportPolicy))throw new RangeError("Invalid ICE policy");
 if(value.turnTLSPort!==undefined&&(!Number.isInteger(value.turnTLSPort)||value.turnTLSPort<1||value.turnTLSPort>65535||value.turnTransport!=="tls"))throw new RangeError("TURN TLS port requires TLS transport and a valid port");
}
export function rtcICEConfiguration(value:any):RTCConfiguration {
 const policy=value.ice_transport_policy??"all";
 if(policy!=="all"&&policy!=="relay")throw new Error("Invalid server ICE policy");
 const servers=value.ice_servers??[];
 if(!Array.isArray(servers)||servers.length>8)throw new Error("Invalid server ICE configuration");
 for(const s of servers){
  const urls=typeof s.urls==="string"?[s.urls]:s.urls;
  if(!Array.isArray(urls)||urls.length<1||urls.length>16||urls.some((u:unknown)=>typeof u!=="string"||u.length>1024||/[\r\n@#]/.test(u)||!/^stuns?:|^turns?:/.test(u)))throw new Error("Invalid server ICE endpoint");
 }
 if(policy==="relay"&&!servers.some((s:any)=>(typeof s.urls==="string"?[s.urls]:s.urls).some((u:string)=>/^turns?:/.test(u))))throw Object.assign(new Error("Required TURN relay is unavailable"),{code:"turn_relay_unavailable"});
 return {iceServers:servers,iceTransportPolicy:policy};
}
export function applyRTCICEOptions(url:URL,options:RTCICEOptions):void {
 validateRTCICEOptions(options);
 for(const key of ["turn_transport","ice_policy","turn_tls_port"])url.searchParams.delete(key);
 if(options.turnTransport!==undefined)url.searchParams.set("turn_transport",options.turnTransport);
 if(options.iceTransportPolicy!==undefined)url.searchParams.set("ice_policy",options.iceTransportPolicy);
 if(options.turnTLSPort!==undefined)url.searchParams.set("turn_tls_port",String(options.turnTLSPort));
}
export interface RTCICEMediaProgress {pairID:string;direct:boolean;sent:number;received:number;send:boolean;receive:boolean}
// Control-plane snapshots only: no sampling timer is added to steady audio.
export function rtcICEMediaProgress(report:RTCStatsReport):RTCICEMediaProgress {
 const v:RTCICEMediaProgress={pairID:"",direct:false,sent:0,received:0,send:false,receive:false};
 report.forEach((s:any)=>{
  if(s.type==="transport"&&s.selectedCandidatePairId)v.pairID=s.selectedCandidatePairId;
  if(s.type==="outbound-rtp"&&!s.isRemote){v.sent+=s.packetsSent??0;v.send=true;}
  if(s.type==="inbound-rtp"&&!s.isRemote){v.received+=s.packetsReceived??0;v.receive=true;}
 });
 if(!v.pairID)report.forEach((s:any)=>{if(s.type==="candidate-pair"&&s.nominated&&s.state==="succeeded")v.pairID=s.id;});
 const pair=report.get(v.pairID),local=pair&&report.get(pair.localCandidateId),remote=pair&&report.get(pair.remoteCandidateId);
 const direct=(c:any)=>c&&["host","srflx","prflx"].includes(c.candidateType);
 v.direct=Boolean(pair&&direct(local)&&direct(remote));return v;
}
export function rtcICEMediaResumed(before:RTCICEMediaProgress,after:RTCICEMediaProgress):boolean {
 return Boolean(after.pairID&&after.pairID!==before.pairID&&(before.send||before.receive)&&(!before.send||after.sent>before.sent)&&(!before.receive||after.received>before.received));
}
interface ICEPeer {
 pc:RTCPeerConnection; current:()=>boolean; offerType:string; answerType:string; generation?:number;
 revision:number; cancellation:AbortController; pending?:{revision:number;resolve:()=>void;reject:(e:Error)=>void;timer:ReturnType<typeof setTimeout>};
}
export class RTCICEController {
 private work:Promise<unknown>=Promise.resolve();
 private peers=new Map<string,ICEPeer>(); private revision=1; private closed=false;
 private retryTimer?:ReturnType<typeof setTimeout>;private retries=0;
 constructor(private send:(value:unknown)=>void,private active:()=>boolean,private observe:(outcome:string,event?:Partial<MediaSessionEvent>)=>void=()=>{},private configured?:(config:RTCConfiguration)=>void){}
 register(key:string,pc:RTCPeerConnection,current:()=>boolean=()=>true,generation?:number){
  this.remove(key);const coach=key==="coach";
  this.peers.set(key,{pc,current,revision:0,generation,cancellation:new AbortController(),offerType:coach?"coach.webrtc.restart_offer":"webrtc.restart_offer",answerType:coach?"coach.webrtc.restart_answer":"webrtc.restart_answer"});
 }
 remove(key:string){const peer=this.peers.get(key);peer?.cancellation.abort();if(peer?.pending){clearTimeout(peer.pending.timer);peer.pending.reject(new Error("ICE peer replaced"));}this.peers.delete(key);}
 private report(outcome:string,event?:Partial<MediaSessionEvent>){try{this.observe(outcome,event);}catch{/* observers cannot interrupt media */}}
 private alive(peer?:ICEPeer){return !this.closed&&this.active()&&(!peer||peer.current()&&peer.pc.connectionState!=="closed");}
 handle(value:any):boolean {
  if(!["webrtc.ice_config","webrtc.restart_answer","coach.webrtc.restart_answer","webrtc.ice_status"].includes(value.type))return false;
  if(!this.alive())return true;
  if(value.type==="webrtc.ice_status"){
   this.report(["credential_refresh_unavailable","restart_failed"].includes(value.state)?value.state:"ice_status");
   if(value.state==="restart_failed")for(const peer of this.peers.values())if(peer.generation===value.generation && peer.pending && peer.pending.revision===value.revision){clearTimeout(peer.pending.timer);peer.pending.reject(new Error("ICE restart rejected"));peer.pending=undefined;}
   return true;
  }
  if(value.type.endsWith("restart_answer")){
   for(const peer of this.peers.values()){
    if(value.type!==peer.answerType||peer.generation!==value.generation||peer.pending?.revision!==value.revision||!this.alive(peer))continue;
    const pending=peer.pending;if(!pending)continue;
    void peer.pc.setRemoteDescription({type:"answer",sdp:value.sdp}).then(()=>{
     if(peer.pending!==pending)return;clearTimeout(pending.timer);peer.pending=undefined;
     if(this.alive(peer)){pending.resolve();}else pending.reject(new Error("ICE session replaced"));
    }).catch(()=>{if(peer.pending===pending){clearTimeout(pending.timer);peer.pending=undefined;pending.reject(new Error("ICE answer rejected"));}});
   }return true;
  }
  if(!Number.isSafeInteger(value.revision)||value.revision<=this.revision)return true;
  let config:RTCConfiguration;
  try{config=rtcICEConfiguration(value);if(!Number.isFinite(value.expires_at)||value.expires_at*1000<=Date.now()+1000)throw new Error();}
  catch{this.report("invalid_ice_refresh");return true;}
  this.revision=value.revision;try{this.configured?.(config);}catch{/* observers cannot interrupt media */}this.report("credentials_refreshed",{code:`ice_rev_${value.revision}`,remaining_ms:Math.max(0,Math.min(3600000,Math.round(value.expires_at*1000-Date.now())))});
  this.work=this.work.then(async()=>{
   if(!this.alive()||this.revision!==value.revision)return;
   const outcomes=await Promise.allSettled([...this.peers.values()].filter(peer=>this.alive(peer)).map(peer=>this.restart(peer,config,value.revision)));
   if(!this.alive()||this.revision!==value.revision)return;
   if(outcomes.some(v=>v.status==="rejected"))this.retry();else{this.retries=0;if(this.retryTimer)clearTimeout(this.retryTimer);this.retryTimer=undefined;}
  }).catch(()=>{this.report("restart_failed");this.retry();});
  return true;
 }
 private mediaStats(peer:ICEPeer,current:()=>boolean):Promise<RTCStatsReport>{
  return new Promise((resolve,reject)=>{
   const cleanup=()=>{clearTimeout(timer);peer.cancellation.signal.removeEventListener("abort",aborted);};
   const aborted=()=>{cleanup();reject(new Error("ICE peer removed"));};
   const timer=setTimeout(()=>{cleanup();reject(new Error("ICE media statistics timed out"));},1500);
   peer.cancellation.signal.addEventListener("abort",aborted,{once:true});
   if(!current()){aborted();return;}
   Promise.resolve().then(()=>peer.pc.getStats()).then(report=>{cleanup();current()?resolve(report):reject(new Error("ICE peer replaced"));},()=>{cleanup();reject(new Error("ICE media statistics unavailable"));});
  });
 }
 private retry(){
  if(this.retryTimer||this.retries>=3||!this.alive())return;
  this.retryTimer=setTimeout(()=>{this.retryTimer=undefined;if(this.alive()){this.retries++;try{this.send({type:"webrtc.ice_refresh"});}catch{this.report("refresh_request_failed");}}},15000);
 }
 private async restart(peer:ICEPeer,config:RTCConfiguration,revision:number){
  if(peer.pending){clearTimeout(peer.pending.timer);peer.pending.reject(new Error("ICE revision superseded"));peer.pending=undefined;}
  const current=()=>this.alive(peer)&&this.peersHas(peer);
  if(this.revision!==revision||!current())return;
  const attempt_id=mediaDiagnosticID("attempt"),start=performance.now();
  const evidence=()=>({attempt_id,code:`ice_rev_${revision}`,duration_ms:Math.round(performance.now()-start)});
  try {
  // A new coaching uplink can still be completing its initial offer/answer.
  // Its initial signaling owns the PC until stable; never overlap negotiations.
  await new Promise<void>((resolve,reject)=>{
   if(peer.pc.signalingState==="stable"){resolve();return;}
   const cleanup=()=>{clearTimeout(timer);peer.pc.removeEventListener("signalingstatechange",changed);peer.cancellation.signal.removeEventListener("abort",aborted);};
   const changed=()=>{if(!current()||peer.pc.signalingState==="stable"){cleanup();current()?resolve():reject(new Error("ICE peer replaced"));}};
   const aborted=()=>{cleanup();reject(new Error("ICE peer removed"));};
   const timer=setTimeout(()=>{cleanup();reject(new Error("ICE signaling busy"));},10000);
   peer.pc.addEventListener("signalingstatechange",changed);peer.cancellation.signal.addEventListener("abort",aborted,{once:true});changed();
  });
  if(!current())return;
  peer.pc.setConfiguration({...peer.pc.getConfiguration(),...config});
  const before=rtcICEMediaProgress(await this.mediaStats(peer,current));
  if(!current())return;
  if(config.iceTransportPolicy!=="relay"&&peer.pc.connectionState==="connected"&&before.direct){
   peer.revision=revision;this.report("credentials_applied_direct",evidence());return;
  }
  // The server keeps its nominated sockets alive while the new pair connects.
  // Track senders/receivers and AudioContext survive this operation.
  const offer=await peer.pc.createOffer({iceRestart:true});if(!current())return;
  await peer.pc.setLocalDescription(offer);if(!current())return;
  await new Promise<void>((resolve,reject)=>{
   if(peer.pc.iceGatheringState==="complete"){resolve();return;}
   const cleanup=()=>{clearTimeout(timer);peer.pc.removeEventListener("icegatheringstatechange",changed);peer.cancellation.signal.removeEventListener("abort",aborted);};
   const changed=()=>{if(!current()||peer.pc.iceGatheringState==="complete"){cleanup();current()?resolve():reject(new Error("ICE session replaced"));}};
   const aborted=()=>{cleanup();reject(new Error("ICE session stopped"));};
   const timer=setTimeout(()=>{cleanup();reject(new Error("ICE gathering timed out"));},7000);
 peer.cancellation.signal.addEventListener("abort",aborted,{once:true});
   peer.pc.addEventListener("icegatheringstatechange",changed);changed();
  });
  if(!current())return;
  this.report("restart_started",evidence());
  await new Promise<void>((resolve,reject)=>{
   const timer=setTimeout(()=>{if(peer.pending?.revision===revision)peer.pending=undefined;reject(new Error("ICE restart timed out"));},10000);
   peer.pending={revision,resolve,reject,timer};
   try{this.send({type:peer.offerType,revision,generation:peer.generation,sdp:peer.pc.localDescription!.sdp});}catch{clearTimeout(timer);peer.pending=undefined;reject(new Error("ICE signaling unavailable"));}
  });
  if(current())this.report("restart_signaled",evidence());
  const deadline=performance.now()+10000;
  while(current()){
   const progress=rtcICEMediaProgress(await this.mediaStats(peer,current));
   if(!current())return;
   if(peer.pc.connectionState==="connected"&&rtcICEMediaResumed(before,progress)){
    peer.revision=revision;this.report("restart_completed",evidence());return;
   }
   if(performance.now()>=deadline)throw new Error("ICE media did not resume");
   await new Promise<void>((resolve,reject)=>{
    const cleanup=()=>{clearTimeout(timer);peer.cancellation.signal.removeEventListener("abort",aborted);};
    const aborted=()=>{cleanup();reject(new Error("ICE peer removed"));};
    const timer=setTimeout(()=>{cleanup();resolve();},100);
    peer.cancellation.signal.addEventListener("abort",aborted,{once:true});
    if(!current())aborted();
   });
  }
  } catch(error) {
   if(!current())return;
   // A failed or superseded exchange must leave the peer able to accept the
   // next refresh without recreating tracks or touching the carrier call.
   if(current()&&peer.pc.signalingState==="have-local-offer")try{await peer.pc.setLocalDescription({type:"rollback"});}catch{/* next refresh remains bounded */}
   if(current())this.report("restart_failed",evidence());
   throw error;
  }
 }
 private peersHas(peer:ICEPeer){return [...this.peers.values()].includes(peer);}
 stop(){if(this.closed)return;this.closed=true;if(this.retryTimer)clearTimeout(this.retryTimer);this.retryTimer=undefined;for(const key of [...this.peers.keys()])this.remove(key);}
}
