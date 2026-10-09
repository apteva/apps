import {expect,test,spyOn} from "bun:test";
import {mediaTransport,rtcMediaURL,rtcStatistics,selectableAudio,permanentRTCFailure,WebRTCAudioConnection} from "../src/webrtc-audio";
import {DEFAULT_SOFTPHONE_AUDIO_OPTIONS,playbackBufferOptions} from "../../ui/softphone-audio";
import type {AudioConnection} from "../src/audio";

function connection(name:string,events:string[],error?:Error):AudioConnection {
 return {async start(){events.push(name+":start");if(error)throw error;},stop(){events.push(name+":stop");},setMuted(value){events.push(name+":mute:"+value);},sendDTMF(digits){events.push(name+":dtmf:"+digits);},setOutputVolume(){}};
}
test("existing WebSocket remains the default, invalid options fail before media",()=>{
 expect(mediaTransport()).toBe("websocket");expect(()=>mediaTransport("bad" as any)).toThrow();expect(()=>playbackBufferOptions({mediaTransport:"bad" as any})).toThrow();
 expect(rtcMediaURL("wss://example.test/softphone/media/id/token?project_id=p")).toBe("wss://example.test/softphone/media/id/token?project_id=p&transport=webrtc");
});
test("default does not create a peer connection; explicit RTC never silently falls back",async()=>{
 const events:string[]=[];
 const normal=selectableAudio({},()=>connection("ws",events),()=>{throw Error("RTC must not run");});
 await normal.start("ws://test",DEFAULT_SOFTPHONE_AUDIO_OPTIONS);normal.stop();expect(events).toContain("ws:start");
 events.length=0;const explicit=selectableAudio({},()=>connection("ws",events),()=>connection("rtc",events,Error("failed")));
 await expect(explicit.start("ws://test",{...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"webrtc"})).rejects.toThrow("failed");
 expect(events).toEqual(["rtc:mute:false","rtc:start","rtc:stop"]);
});
test("auto releases failed RTC before starting fallback and retains mute",async()=>{
 const events:string[]=[],notices:any[]=[];
 const audio=selectableAudio({onSessionEvent:e=>notices.push(e)},()=>connection("ws",events),()=>connection("rtc",events,Error("failed")));
 audio.setMuted(true);await audio.start("ws://test",{...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"auto"});audio.sendDTMF("1");audio.stop();
 expect(events).toEqual(["rtc:mute:true","rtc:start","rtc:stop","ws:mute:true","ws:start","ws:dtmf:1","ws:stop"]);
 expect(notices[0].outcome).toBe("websocket_fallback");expect(Date.parse(notices[0].timestamp)).toBeFinite();
});
test("cancelled RTC setup cannot start fallback or revive media",async()=>{
 const events:string[]=[];let reject!:(e:Error)=>void;
 const rtc={...connection("rtc",events),start:()=>new Promise<void>((_,r)=>reject=r)};
 const audio=selectableAudio({},()=>connection("ws",events),()=>rtc);
 const pending=audio.start("ws://test",{...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"auto"});audio.stop();reject(Error("cancelled"));
 await expect(pending).rejects.toThrow();expect(events).not.toContain("ws:start");
});
test("auto and recovery never retry revoked authorization or denied microphone access",async()=>{
 for(const error of [Object.assign(Error("denied"),{status:403}),Object.assign(Error("expired"),{status:410}),new DOMException("denied","NotAllowedError"),new DOMException("no microphone","NotFoundError")]){
  expect(permanentRTCFailure(error)).toBe(true);
  const events:string[]=[];
  const audio=selectableAudio({},()=>connection("ws",events),()=>connection("rtc",events,error));
  await expect(audio.start("ws://test",{...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"auto"})).rejects.toThrow();
  expect(events).not.toContain("ws:start");expect(events).toContain("rtc:stop");
 }
 expect(permanentRTCFailure(Object.assign(Error("temporary"),{status:503}))).toBe(false);
});
test("RTP statistics retain transport and separate concealment from dropped PCM",()=>{
 const epoch=Date.UTC(2026,9,7);
 const report=new Map<string,any>([
 ["out",{type:"outbound-rtp",kind:"audio",timestamp:epoch+2000,bytesSent:5000}],
 ["in",{type:"inbound-rtp",kind:"audio",timestamp:epoch+2000,bytesReceived:7000,packetsLost:3,jitter:.02,concealedSamples:960,packetsDiscarded:1,jitterBufferDelay:2,jitterBufferEmittedCount:100}],
 ["transport",{type:"transport",selectedCandidatePairId:"pair"}],
 ["pair",{type:"candidate-pair",currentRoundTripTime:.04,localCandidateId:"local"}],
 ["local",{type:"local-candidate",protocol:"udp",candidateType:"host",address:"sensitive-ip"}],
 ]) as unknown as RTCStatsReport;
 const value=rtcStatistics(report,{at:epoch+1000,sent:1000,received:2000});
 expect(value.rtt).toBe(40);expect(value.webrtc.sendBitrateBps).toBe(32000);expect(value.webrtc.receiveBitrateBps).toBe(40000);
 expect(value.webrtc.concealedMs).toBe(20);expect(value.webrtc.jitterMs).toBe(20);expect(value.queueMs).toBe(20);
 expect(value.webrtc.protocol).toBe("udp");expect(JSON.stringify(value)).not.toContain("sensitive-ip");
});
test("a hung fresh-authorization request is bounded and late completion cannot revive audio",async()=>{
 const states:string[]=[];let resolve!:(url:string)=>void,expire!:()=>void,connections=0;
 const audio:any=new WebRTCAudioConnection({onState:state=>states.push(state),refreshMediaURL:()=>new Promise(r=>resolve=r)});
 audio.connect=async()=>{connections++;};
 const original=setTimeout;
 const timer=spyOn(globalThis,"setTimeout").mockImplementation(((fn:any,ms:any,...args:any[])=>{
  if(ms===30000)expire=fn;return original(fn,ms,...args);
 }) as typeof setTimeout);
 try{
  audio.disconnected(0,"signaling_closed");expect(states).toEqual(["reconnecting"]);
  expire();expect(states).toEqual(["reconnecting","error"]);
  resolve("ws://local/new-session");await Promise.resolve();await Promise.resolve();
  expect(connections).toBe(0);audio.stop();
 }finally{timer.mockRestore();}
});

function richReport(time:number,inboundID="in",pairID="pair",base=0):RTCStatsReport {
 return new Map<string,any>([
 ["out",{type:"outbound-rtp",kind:"audio",timestamp:time,bytesSent:base+1000,packetsSent:base+100,totalPacketSendDelay:base+1,retransmittedPacketsSent:2,nackCount:1}],
 [inboundID,{type:"inbound-rtp",kind:"audio",timestamp:time,bytesReceived:base+2000,packetsReceived:base+100,packetsLost:2,jitter:.01,codecId:"codec",jitterBufferDelay:base+2,jitterBufferTargetDelay:base+3,jitterBufferMinimumDelay:base+1,jitterBufferEmittedCount:base+100,totalProcessingDelay:base+.5,lastPacketReceivedTimestamp:time-30,concealmentEvents:3,silentConcealedSamples:100,insertedSamplesForDeceleration:50}],
 ["remote-in",{type:"remote-inbound-rtp",kind:"audio",packetsLost:7,fractionLost:.04,jitter:.02,roundTripTime:.1}],
 ["remote-out",{type:"remote-outbound-rtp",kind:"audio",packetsSent:400,bytesSent:3000}],
 ["transport",{type:"transport",selectedCandidatePairId:pairID,dtlsState:"connected"}],
 [pairID,{type:"candidate-pair",state:"succeeded",localCandidateId:"local",remoteCandidateId:"remote",availableOutgoingBitrate:128000,availableIncomingBitrate:256000,currentRoundTripTime:.04,requestsSent:100}],
 ["local",{type:"local-candidate",protocol:"udp",candidateType:"relay",relayProtocol:"tls",address:"SECRET-IP",url:"SECRET-TURN"}],
 ["remote",{type:"remote-candidate",candidateType:"srflx",address:"SECRET-REMOTE"}],
 ["codec",{type:"codec",mimeType:"audio/opus",clockRate:48000,channels:1,sdpFmtpLine:"SECRET-SDP"}],
 ]) as unknown as RTCStatsReport;
}
test("rich native stats separate remote receiver loss, expose bandwidth and interval delays without raw identifiers",()=>{
 const at=Date.UTC(2026,9,9), first=rtcStatistics(richReport(at));
 expect(first.metrics.receiver_jitter_buffer_interval_ms).toBeUndefined();
 const next=rtcStatistics(richReport(at+1000,"in","pair",100),first.previous);
 expect(next.metrics.remote_receiver_packetsLost).toBe(7);expect(next.metrics.receiver_packetsLost).toBe(2);
 expect(next.metrics.receiver_jitter_buffer_interval_ms).toBe(1000);expect(next.metrics.sender_send_delay_interval_ms).toBe(1000);
 expect(next.metrics.pair_availableOutgoingBitrate).toBe(128000);expect(next.metrics.receiver_last_packet_age_ms).toBe(30);
 expect(next.states.remote_candidate).toBe("srflx");expect(next.states.relay_protocol).toBe("tls");expect(next.states.codec).toBe("audio/opus");
 expect(JSON.stringify({metrics:next.metrics,states:next.states,webrtc:next.webrtc})).not.toContain("SECRET");
 const moved=rtcStatistics(richReport(at+2000,"in","pair2",200),next.previous);expect(moved.metrics.path_revision).toBe(2);
});
test("missing stats, counter resets and new RTP identities do not fabricate rates or delay deltas",()=>{
 const at=Date.UTC(2026,9,9), first=rtcStatistics(richReport(at,"in","pair",1000));
 const reset=rtcStatistics(richReport(at+1000),first.previous);
 expect(reset.webrtc.sendBitrateBps).toBe(0);expect(reset.webrtc.receiveBitrateBps).toBe(0);expect(reset.metrics.receiver_jitter_buffer_interval_ms).toBeUndefined();
 const replaced=rtcStatistics(richReport(at+2000,"new-ssrc","pair",2000),first.previous);
 expect(replaced.webrtc.receiveBitrateBps).toBe(0);expect(replaced.metrics.receiver_jitter_buffer_interval_ms).toBeUndefined();
 const absent=rtcStatistics(new Map() as any);expect(absent.rtt).toBeNull();expect(absent.metrics.pair_availableOutgoingBitrate).toBeUndefined();expect(absent.metrics.remote_receiver_packetsLost).toBeUndefined();
});
test("overlapping getStats, failed sampling and late completions never stop or revive media",async()=>{
 let resolve!:(v:RTCStatsReport)=>void,reads=0;const published:any[]=[];
 const audio:any=new WebRTCAudioConnection({onDiagnostics:d=>published.push(d)});
 const pc={getStats:()=>{reads++;return new Promise<RTCStatsReport>(r=>resolve=r);},iceConnectionState:"connected",connectionState:"connected"};
 audio.pc=pc;audio.generation=1;
 const pending=audio.statistics(1);await audio.statistics(1);expect(reads).toBe(1);
 resolve(richReport(Date.now()));await pending;expect(published).toHaveLength(1);
 pc.getStats=async()=>{throw new Error("stats unavailable")};await audio.statistics(1);expect(audio.statsErrors).toBe(1);expect(audio.pc).toBe(pc);expect(audio.stopped).toBe(false);
 pc.getStats=()=>new Promise<RTCStatsReport>(r=>resolve=r);
 const late=audio.statistics(1);audio.generation=2;resolve(richReport(Date.now()));await late;expect(published).toHaveLength(2);audio.transportSender.stop();
});
