import {expect,test} from "bun:test";
import {RTCICEController,rtcICEConfiguration,applyRTCICEOptions,validateRTCICEOptions} from "../src/rtc-ice";
import {permanentRTCFailure,rtcStatistics,selectableAudio} from "../src/webrtc-audio";
import {listenerAudioOptions,selectableListenerAudio} from "../src/listener-audio";
import {resolveAudioOptions} from "../../ui/audio-settings";
import {validateTURNPolicy,turnPolicyUpdate} from "../../ui/TurnSettings";

class FakePeer extends EventTarget {
 connectionState="connected";signalingState="stable";iceGatheringState="complete";localDescription:any;
 sent=100;received=100;pair="pair-1";candidateType="relay";autoResume=true;
 async getStats(){return new Map([["t",{type:"transport",selectedCandidatePairId:this.pair}],[this.pair,{type:"candidate-pair",localCandidateId:"l",remoteCandidateId:"r"}],["l",{candidateType:this.candidateType}],["r",{candidateType:"host"}],["out",{type:"outbound-rtp",packetsSent:this.sent}],["in",{type:"inbound-rtp",packetsReceived:this.received}]]);}
 configuration:RTCConfiguration={iceServers:[]};senders=[{track:{enabled:false,id:"same-microphone"}}];closed=false;offers:any[]=[];answers:any[]=[];
 setConfiguration(c:RTCConfiguration){this.configuration=c;}getConfiguration(){return this.configuration;}
 async createOffer(options:any){this.offers.push(options);return {type:"offer",sdp:"new-ice-offer"};}
 async setLocalDescription(d:any){this.localDescription=d;}async setRemoteDescription(d:any){this.answers.push(d);this.pair=`pair-${this.answers.length+1}`;if(this.autoResume){this.sent++;this.received++;}}
 close(){this.closed=true;}
}
const refresh=(revision=2)=>({type:"webrtc.ice_config",revision,expires_at:Date.now()/1000+3600,ice_servers:[{urls:["turns:relay.example:8443?transport=tcp"],username:"secret-user",credential:"secret-password"}],ice_transport_policy:"relay"});
const tick=()=>new Promise(resolve=>setTimeout(resolve,0));

test("optional ICE configuration and validated per-session overrides",()=>{
 expect(rtcICEConfiguration({})).toEqual({iceServers:[],iceTransportPolicy:"all"});
 const url=new URL("wss://app.example/media/call/token?project_id=auth&ice_policy=old");applyRTCICEOptions(url,{turnTransport:"tls",turnTLSPort:8443,iceTransportPolicy:"relay"});
 expect(url.searchParams.getAll("ice_policy")).toEqual(["relay"]);expect(url.searchParams.get("turn_tls_port")).toBe("8443");expect(url.searchParams.get("project_id")).toBe("auth");
 expect(()=>validateRTCICEOptions({turnTransport:"udp",turnTLSPort:5349})).toThrow();expect(()=>rtcICEConfiguration({ice_transport_policy:"relay"})).toThrow();
 expect(()=>rtcICEConfiguration({ice_servers:[{urls:["https://user:password@example"]}]})).toThrow();
 expect(permanentRTCFailure(Object.assign(new Error("Relay required"),{code:"turn_relay_unavailable"}))).toBe(true);
});
test("credential renewal keeps peer, microphone, mute and stream senders",async()=>{
 const p=new FakePeer(),senders=p.senders,events:string[]=[],messages:any[]=[];
 const c=new RTCICEController(v=>messages.push(v),()=>true,v=>events.push(v));c.register("main",p as any);
 c.handle(refresh());await tick();expect(messages).toHaveLength(1);expect(messages[0].type).toBe("webrtc.restart_offer");expect(p.offers[0]).toEqual({iceRestart:true});
 c.handle({type:"webrtc.restart_answer",revision:1,sdp:"stale"});await tick();expect(p.answers).toHaveLength(0);
 c.handle({type:"webrtc.restart_answer",revision:2,sdp:"fresh"});await tick();
 expect(p.answers[0].sdp).toBe("fresh");expect(p.senders).toBe(senders);expect(p.senders[0].track.enabled).toBe(false);expect(p.closed).toBe(false);
 expect(events).toContain("restart_completed");expect(JSON.stringify(events)).not.toContain("secret");c.stop();
});
test("listening and coaching refresh distinct peers and pin coaching generation",async()=>{
 const messages:any[]=[],main=new FakePeer(),coach=new FakePeer();let current=true;
 const c=new RTCICEController(v=>messages.push(v),()=>true);c.register("main",main as any);c.register("coach",coach as any,()=>current,42);
 c.handle(refresh());await tick();expect(messages.map(m=>m.type).sort()).toEqual(["coach.webrtc.restart_offer","webrtc.restart_offer"]);
 c.handle({type:"coach.webrtc.restart_answer",revision:2,generation:41,sdp:"wrong-generation"});await tick();expect(coach.answers).toHaveLength(0);
 c.handle({type:"webrtc.restart_answer",revision:2,sdp:"listen"});c.handle({type:"coach.webrtc.restart_answer",revision:2,generation:42,sdp:"coach"});await tick();expect(coach.answers[0].sdp).toBe("coach");
 current=false;c.handle(refresh(3));await tick();expect(coach.offers).toHaveLength(1);c.stop();
});
test("superseded, malformed and canceled refreshes never resurrect audio",async()=>{
 let active=true;const p=new FakePeer(),messages:any[]=[];const c=new RTCICEController(v=>messages.push(v),()=>active);c.register("main",p as any);
 c.handle({...refresh(),expires_at:0});await tick();expect(p.offers).toHaveLength(0);
 c.handle(refresh());await tick();c.handle(refresh());await tick();expect(messages).toHaveLength(1);
 active=false;c.handle({type:"webrtc.restart_answer",revision:2,sdp:"late"});c.stop();await tick();expect(p.answers).toHaveLength(0);expect(p.closed).toBe(false);
});
test("listener inherits session options but shared-browser persistence drops them",()=>{
 const options=listenerAudioOptions({turnTransport:"tls",turnTLSPort:8443,iceTransportPolicy:"relay"});expect(options.turnTLSPort).toBe(8443);
 const stored=resolveAudioOptions(JSON.stringify(options),null,null);expect(stored.turnTLSPort).toBeUndefined();expect(stored.iceTransportPolicy).toBeUndefined();
 expect(()=>validateTURNPolicy({source:"app",transport:"udp",ice_transport_policy:"relay",ttl_seconds:3600,tls_port:8443})).toThrow();
});
test("relay transport is distinct from RTP candidate UDP protocol",()=>{
 const report=new Map<any,any>([["t",{type:"transport",selectedCandidatePairId:"p"}],["p",{type:"candidate-pair",localCandidateId:"l",remoteCandidateId:"r"}],["l",{candidateType:"relay",protocol:"udp",relayProtocol:"tls"}],["r",{candidateType:"host"}]]);
 const result=rtcStatistics(report as any);expect(result.webrtc.protocol).toBe("udp");expect(result.webrtc.relayProtocol).toBe("tls");expect(result.webrtc.candidateType).toBe("relay");
});

test("coaching renewal waits for initial negotiation and cancels removed peers",async()=>{
 const p=new FakePeer(),messages:any[]=[];p.signalingState="have-local-offer";
 const c=new RTCICEController(v=>messages.push(v),()=>true);c.register("coach",p as any,()=>true,8);
 c.handle(refresh());await tick();expect(p.offers).toHaveLength(0);
 p.signalingState="stable";p.dispatchEvent(new Event("signalingstatechange"));await tick();expect(p.offers).toHaveLength(1);
 c.remove("coach");await tick();c.handle({type:"coach.webrtc.restart_answer",revision:2,generation:8,sdp:"late"});await tick();expect(p.answers).toHaveLength(0);c.stop();
});

test("overlapping refreshes are serialized and throwing observers cannot break media",async()=>{
 const p=new FakePeer(),messages:any[]=[],events:any[]=[];
 const c=new RTCICEController(v=>messages.push(v),()=>true,(outcome,evidence)=>{events.push({outcome,...evidence});throw new Error("observer");},()=>{throw new Error("observer");});c.register("main",p as any);
 c.handle(refresh());await tick();c.handle(refresh(3));await tick();expect(p.offers).toHaveLength(1);
 c.handle({type:"webrtc.restart_answer",revision:2,sdp:"older"});await tick();expect(p.offers).toHaveLength(2);
 c.handle({type:"webrtc.restart_answer",revision:3,sdp:"newer"});await tick();
 expect(events.filter(e=>e.outcome==="restart_completed")).toHaveLength(2);
 const starts=events.filter(e=>e.outcome==="restart_started");expect(starts[0].attempt_id).toMatch(/^attempt-/);expect(starts[0].code).toBe("ice_rev_2");
 expect(events.find(e=>e.outcome==="restart_completed")?.attempt_id).toBe(starts[0].attempt_id);
 expect(JSON.stringify(events)).not.toContain("secret");c.stop();
});

test("a coaching restart failure does not reject the listening restart",async()=>{
 const messages:any[]=[],main=new FakePeer(),coach=new FakePeer();
 const c=new RTCICEController(v=>messages.push(v),()=>true);c.register("main",main as any);c.register("coach",coach as any,()=>true,9);
 c.handle(refresh());await tick();
 c.handle({type:"webrtc.ice_status",state:"restart_failed",revision:2,generation:9});await tick();
 c.handle({type:"webrtc.restart_answer",revision:2,sdp:"listen"});await tick();
 expect(main.answers).toHaveLength(1);expect(coach.answers).toHaveLength(0);c.stop();
});

test("stopping during ICE gathering removes callbacks and suppresses signaling",async()=>{
 const p=new FakePeer(),messages:any[]=[];p.iceGatheringState="gathering";
 const c=new RTCICEController(v=>messages.push(v),()=>true);c.register("main",p as any);
 c.handle(refresh());await tick();c.stop();p.iceGatheringState="complete";p.dispatchEvent(new Event("icegatheringstatechange"));await tick();expect(messages).toHaveLength(0);
});


test("forced-relay session never falls back to PCM on a generic network timeout",async()=>{
 let fallbacks=0;
 const failure=()=>({async start(){throw new Error("ICE gathering timed out");},stop(){},setMuted(){},setOutputVolume(){},sendDTMF(){}});
 const audio=selectableAudio({onState(){}},()=>{fallbacks++;return failure();},failure);
 await expect(audio.start("wss://app.example",{...resolveAudioOptions(null,null,null),mediaTransport:"auto",iceTransportPolicy:"relay"})).rejects.toThrow();expect(fallbacks).toBe(0);audio.stop();
 const listener=selectableListenerAudio({onReady(){},onClose(){}},"auto",()=>{fallbacks++;return failure();},failure,true);
 await expect(listener.start("wss://app.example")).rejects.toThrow();expect(fallbacks).toBe(0);listener.stop();
});


test("editing an inherited adviser policy cannot grant override permissions",()=>{
 const inherited={source:"app",transport:"all",ice_transport_policy:"all",ttl_seconds:3600,allow_user_overrides:true} as const;
 expect(turnPolicyUpdate(inherited,"adviser").allow_user_overrides).toBeUndefined();
 expect(turnPolicyUpdate(inherited,"project").allow_user_overrides).toBe(true);
 expect(inherited.allow_user_overrides).toBe(true);
});


test("healthy direct paths refresh configuration without ICE restart",async()=>{
 const p=new FakePeer(),messages:any[]=[],events:string[]=[];p.candidateType="host";
 const c=new RTCICEController(v=>messages.push(v),()=>true,v=>events.push(v));c.register("main",p as any);
 c.handle({...refresh(),ice_transport_policy:"all"});await tick();
 expect(messages).toHaveLength(0);expect(p.offers).toHaveLength(0);
 expect(p.configuration.iceServers?.[0].username).toBe("secret-user");
 expect(events).toContain("credentials_applied_direct");expect(events).not.toContain("restart_completed");c.stop();
});
test("signaling and a connected peer cannot report renewal success without resumed RTP",async()=>{
 const p=new FakePeer(),events:string[]=[];p.autoResume=false;
 const c=new RTCICEController(()=>{},()=>true,v=>events.push(v));c.register("main",p as any);
 c.handle(refresh());await tick();c.handle({type:"webrtc.restart_answer",revision:2,sdp:"answer"});await tick();
 expect(events).toContain("restart_signaled");expect(events).not.toContain("restart_completed");
 p.sent++;await new Promise(r=>setTimeout(r,120));expect(events).not.toContain("restart_completed");
 p.received++;await new Promise(r=>setTimeout(r,120));expect(events).toContain("restart_completed");c.stop();
});
test("relay policy changes still restart a direct path",async()=>{
 const p=new FakePeer(),messages:any[]=[];p.candidateType="host";
 const c=new RTCICEController(v=>messages.push(v),()=>true);c.register("main",p as any);
 c.handle(refresh());await tick();expect(messages).toHaveLength(1);expect(p.offers).toHaveLength(1);c.stop();
});
