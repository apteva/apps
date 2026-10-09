import { microphoneConstraints, playbackBufferOptions, PreviewResampler, type SoftphoneAudioOptions, type SoftphoneCallbacks, type SoftphoneDiagnostics } from "../../ui/softphone-audio";
import { TransportTelemetry, TransportTelemetrySender, transportMetrics, transportStates } from "../../ui/transport-telemetry";
import { AudioRuntimeTelemetry } from "../../ui/audio-runtime-telemetry";
import { playRingback, ringbackPattern } from "../../ui/ringback";
import { mediaFailure, type MediaSessionEvent } from "./media-lease";
import type { AudioConnection } from "./audio";

export type MediaTransport = "websocket" | "webrtc" | "auto";
export function mediaTransport(value?: MediaTransport): MediaTransport {
  if (value === undefined) return "websocket";
  if (!["websocket", "webrtc", "auto"].includes(value)) throw new RangeError("Unsupported softphone media transport");
  return value;
}
export function rtcMediaURL(url: string): string {
  const parsed = new URL(url); parsed.searchParams.set("transport", "webrtc"); return parsed.toString();
}

const safe = (callback: (() => void) | undefined) => { try { callback?.(); } catch { /* observers never gate media */ } };
const finite = (value: unknown, limit = 1e12) => typeof value === "number" && Number.isFinite(value) ? Math.max(0, Math.min(value, limit)) : 0;
export function permanentRTCFailure(error: unknown): boolean {
  const failure=mediaFailure(error);
  return failure.denied || failure.expired || ["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes((error as Error)?.name);
}

type RTCPrevious = { at:number;sent:number;received:number; counters?:Record<string,Record<string,number>>; pairID?:string;pathRevision?:number };
/** RTP counters are directional. Missing native fields stay absent. Delta
 * calculations require the same stats identity and a nondecreasing counter. */
export function rtcStatistics(report: RTCStatsReport, previous?: RTCPrevious) {
  let sent=0, received=0, lost=0, jitter=0, concealed=0, discarded=0, delay=0, emitted=0, at=0;
  let rtt:number|null=null, pairID:string|undefined, protocol:string|undefined, candidateType:string|undefined;
  const counters:Record<string,Record<string,number>>={}, metrics:Record<string,number>={}, states:Record<string,string>={};
  const numeric=(s:any,k:string)=>typeof s?.[k]==="number" && Number.isFinite(s[k]) ? Math.max(0,s[k]) : undefined;
  const copy=(s:any,prefix:string,keys:string[])=>{for(const k of keys){const n=numeric(s,k);if(n!==undefined)metrics[prefix+k]=n;}};
  const interval=(s:any,id:string,numerator:string,denominator:string,key:string)=>{
    const n=numeric(s,numerator), d=numeric(s,denominator), old=previous?.counters?.[id];
    if(n!==undefined && d!==undefined){counters[id]??={};counters[id][numerator]=n;counters[id][denominator]=d;
      if(old && n>=old[numerator] && d>old[denominator]) metrics[key]=(n-old[numerator])/(d-old[denominator])*1000;}
  };
  let identities=0;
  report.forEach((s:any,id:string)=>{
    at=Math.max(at,finite(s.timestamp,1e15));
    if((s.kind??s.mediaType??report.get(s.localId)?.kind)==="audio" && identities++<32){
      if(s.type==="outbound-rtp"){
        sent+=finite(s.bytesSent);copy(s,"sender_",["bytesSent","packetsSent","headerBytesSent","retransmittedPacketsSent","retransmittedBytesSent","nackCount","targetBitrate"]);
        interval(s,id,"totalPacketSendDelay","packetsSent","sender_send_delay_interval_ms");
      }
      if(s.type==="inbound-rtp"){
        received+=finite(s.bytesReceived);lost+=finite(s.packetsLost);jitter=Math.max(jitter,finite(s.jitter)*1000);
        const codec=report.get(s.codecId);const rate=numeric(codec,"clockRate")??48000;
        concealed+=finite(s.concealedSamples)*1000/rate;discarded+=finite(s.packetsDiscarded);delay+=finite(s.jitterBufferDelay);emitted+=finite(s.jitterBufferEmittedCount);
        copy(s,"receiver_",["bytesReceived","packetsReceived","packetsLost","jitter","packetsDiscarded","concealedSamples","silentConcealedSamples","concealmentEvents","insertedSamplesForDeceleration","removedSamplesForAcceleration","totalSamplesReceived","audioLevel","totalAudioEnergy","totalSamplesDuration","nackCount","fecPacketsReceived","fecPacketsDiscarded"]);
        interval(s,id,"jitterBufferDelay","jitterBufferEmittedCount","receiver_jitter_buffer_interval_ms");
        interval(s,id,"jitterBufferTargetDelay","jitterBufferEmittedCount","receiver_jitter_target_interval_ms");
        interval(s,id,"jitterBufferMinimumDelay","jitterBufferEmittedCount","receiver_jitter_minimum_interval_ms");
        interval(s,id,"totalProcessingDelay","packetsReceived","receiver_processing_interval_ms");
        if(numeric(s,"lastPacketReceivedTimestamp")!==undefined)metrics.receiver_last_packet_age_ms=Math.max(0,s.timestamp-s.lastPacketReceivedTimestamp);
        if(codec){states.codec=codec.mimeType;metrics.codec_clock_rate=rate;if(numeric(codec,"channels")!==undefined)metrics.codec_channels=codec.channels;}
      }
      if(s.type==="remote-inbound-rtp")copy(s,"remote_receiver_",["packetsLost","fractionLost","jitter","roundTripTime","totalRoundTripTime","roundTripTimeMeasurements"]);
      if(s.type==="remote-outbound-rtp")copy(s,"remote_sender_",["packetsSent","bytesSent"]);
      const bytesKey=s.type==="outbound-rtp"?"bytesSent":s.type==="inbound-rtp"?"bytesReceived":undefined;
      if(bytesKey && numeric(s,bytesKey)!==undefined){counters[id]??={};counters[id][bytesKey]=s[bytesKey];counters[id].at=s.timestamp;}
    }
    if(s.type==="transport" && s.selectedCandidatePairId){
      pairID=s.selectedCandidatePairId;states.dtls=s.dtlsState;
      const pair=report.get(pairID!);if(pair){
        rtt=numeric(pair,"currentRoundTripTime")!==undefined?pair.currentRoundTripTime*1000:null;
        copy(pair,"pair_",["availableOutgoingBitrate","availableIncomingBitrate","currentRoundTripTime","totalRoundTripTime","bytesSent","bytesReceived","requestsSent","requestsReceived","responsesSent","responsesReceived","consentRequestsSent"]);states.pair=pair.state;
        const local=report.get(pair.localCandidateId),remote=report.get(pair.remoteCandidateId);
        if(local){protocol=local.protocol;candidateType=local.candidateType;states.protocol=protocol!;states.local_candidate=candidateType!;states.relay_protocol=local.relayProtocol;}
        if(remote)states.remote_candidate=remote.candidateType;
      }
    }
  });
  const bitrate=(key:string,fallback:number)=>{
    if(previous?.counters){let total=0;for(const [id,c] of Object.entries(counters)){const old=previous.counters[id];if(old && c[key]>=old[key] && c.at>old.at)total+=(c[key]-old[key])*8000/(c.at-old.at);}return total;}
    const seconds=previous && at>previous.at?(at-previous.at)/1000:0;return seconds?Math.max(0,fallback-(key==="bytesSent"?previous!.sent:previous!.received))*8/seconds:0;
  };
  const pathRevision=(previous?.pathRevision??0)+(pairID && pairID!==previous?.pairID?1:0);
  metrics.path_revision=pathRevision;
  const sendBitrateBps=bitrate("bytesSent",sent),receiveBitrateBps=bitrate("bytesReceived",received);
  return {previous:{at,sent,received,counters,pairID,pathRevision},rtt:rtt as number|null,queueMs:emitted>0?delay/emitted*1000:0,
    metrics:transportMetrics({...metrics,send_bitrate_bps:sendBitrateBps,receive_bitrate_bps:receiveBitrateBps}),states:transportStates(states),
    webrtc:{protocol,candidateType,sendBitrateBps,receiveBitrateBps,packetsLost:lost,jitterMs:jitter,concealedMs:concealed,packetsDiscarded:discarded,jitterBufferMs:emitted>0?delay/emitted*1000:0}};
}

/** Native WebRTC media; the socket carries authenticated signaling/control only. */
export class WebRTCAudioConnection implements AudioConnection {
  private stopped=false;
  private generation=0;
  private socket?: WebSocket;
  private pc?: RTCPeerConnection;
  private stream?: MediaStream;
  private context?: AudioContext;
  private capture?: AudioWorkletNode;
  private whisper?: AudioWorkletNode;
  private destination?: MediaStreamAudioDestinationNode;
  private speaker?: GainNode;
  private remoteAudio?: HTMLAudioElement;
  private speakerMeter?: AnalyserNode;
  private carrierStalled=false;
  private deliveryDegraded=false;
  private ready=false;
  private peer=false;
  private muted=false;
  private whisperEpoch: number | null=null;
  private whisperBase: number | null=null;
  private whisperResampler=new PreviewResampler();
  private timer?: ReturnType<typeof setInterval>;
  private retry?: ReturnType<typeof setTimeout>;
  private cancelSetup?: () => void;
  private recovering=false;
  private recoveryGeneration=0;
  private recoveryExpiry?: ReturnType<typeof setTimeout>;
  private ringback?: () => void;
  private options?: SoftphoneAudioOptions;
  private workletURL?: string;
  private events: MediaSessionEvent[]=[];
  private previous?: RTCPrevious;
  private sampling=new WeakSet<RTCPeerConnection>();
  private transportTelemetry=new TransportTelemetry("webrtc");
  private transportSender=new TransportTelemetrySender(sample=>{
    if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return false;
    this.send({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[sample]}});return true;
  },report=>{
    if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return false;
    this.send(report);return true;
  });
  private statsErrors=0;
  private lastStatsSend=-Infinity;
  private reportedLoss=0;
  private clientEpoch=crypto.randomUUID();
  private nativeCounts?: {packetsLost:number;packetsDiscarded:number;concealedMs:number};
  private completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};
  private runtime=new AudioRuntimeTelemetry(e=>this.recordSessionEvent(e));
  private diagnostics: SoftphoneDiagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};

  constructor(private callbacks: SoftphoneCallbacks) {}
  async start(url:string,options:SoftphoneAudioOptions,workletURL?:string):Promise<void> {
    this.options=options;this.workletURL=workletURL;
    if (!workletURL) throw new Error("WebRTC requires the shared Telephony audio processor");
    if (typeof RTCPeerConnection!=="function") throw new Error("WebRTC audio is unavailable in this browser");
    playbackBufferOptions(options);
    await this.connect(url);
  }
  private current(generation:number){return !this.stopped && this.generation===generation;}
  private guard(generation:number){if(!this.current(generation)) throw new Error("Audio session cancelled");}
  private async connect(url:string):Promise<void> {
    const generation=++this.generation,options=this.options!;
    this.ready=this.peer=false;this.previous=undefined;this.reportedLoss=this.completedCounts.packetsLost;
    const stream=await navigator.mediaDevices.getUserMedia({audio:microphoneConstraints(options)});
    if(!this.current(generation)){stream.getTracks().forEach(t=>t.stop());throw new Error("Audio session cancelled");}
    this.stream=stream;
    const mic=stream.getAudioTracks()[0];if(!mic) throw new Error("No microphone audio track");
    const settings=mic.getSettings();
    this.diagnostics={...this.diagnostics,microphoneSampleRate:settings.sampleRate??0,microphoneChannelCount:settings.channelCount??1,
      echoCancellation:typeof settings.echoCancellation==="boolean"?settings.echoCancellation:null,noiseSuppression:settings.noiseSuppression??null,autoGainControl:settings.autoGainControl??null,micInputGainDb:options.inputGainDB};
    mic.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"});
    mic.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"});
    mic.onended=()=>{if(this.current(generation))this.disconnected(generation,"microphone_ended");};
    this.context=new AudioContext({latencyHint:"interactive"});const context=this.context;
    context.onstatechange=()=>this.runtime.context(context.state);
    this.runtime.context(context.state);await context.resume();this.guard(generation);
    if(options.outputDeviceId && "setSinkId" in context) await (context as AudioContext & {setSinkId(id:string):Promise<void>}).setSinkId(options.outputDeviceId);
    await context.audioWorklet.addModule(this.workletURL!);this.guard(generation);
    this.diagnostics.audioContextRate=context.sampleRate;
    this.capture=new AudioWorkletNode(context,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:true,inputGainDB:options.inputGainDB,highpassFilter:options.highpassFilter}});
    this.destination=context.createMediaStreamDestination();
    // Keep the processed mono signal mono. A stereo destination with a silent
    // second channel halves its level when the native encoder downmixes it.
    this.destination.channelCount=1;
    context.createMediaStreamSource(stream).connect(this.capture).connect(this.destination);
    this.capture.port.onmessage=e=>{if(e.data?.type==="capture.stats") {
      const db=(v:number)=>v>0?20*Math.log10(v):null;
      this.diagnostics.micActiveRmsDbfs=db(e.data.active_rms);this.diagnostics.micPeakDbfs=db(e.data.pre_peak);
      this.diagnostics.micPostPeakDbfs=db(e.data.post_peak);this.diagnostics.micLimiterReductionDb=e.data.limiter_reduction_db??0;
      let speaker=0;if(this.speakerMeter){const samples=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(samples);speaker=Math.sqrt(samples.reduce((n,v)=>n+v*v,0)/samples.length);}
      safe(()=>this.callbacks.onLevels?.(finite(e.data.active_rms,1),speaker));
    }};
    this.speaker=context.createGain();this.speaker.gain.value=options.outputVolume??1;this.speaker.connect(context.destination);
    this.speakerMeter=context.createAnalyser();this.speakerMeter.fftSize=256;
    const silentMeter=context.createGain();silentMeter.gain.value=0;this.speaker.connect(this.speakerMeter).connect(silentMeter).connect(context.destination);
    this.whisper=new AudioWorkletNode(context,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:playbackBufferOptions(options)});
    this.whisper.connect(this.speaker);
    this.gate();
    const socket=new WebSocket(rtcMediaURL(url));socket.binaryType="arraybuffer";this.socket=socket;
    let connected=false,finished=false;
    await new Promise<void>((resolve,reject)=>{
      const finish=(err?:Error)=>{if(finished)return;finished=true;clearTimeout(timeout);this.cancelSetup=undefined;err?reject(err):resolve();};
      const timeout=setTimeout(()=>finish(new Error("WebRTC audio connection timed out")),18000);
      this.cancelSetup=()=>finish(new Error("Audio session cancelled"));
      const maybeReady=()=>{if(this.current(generation)&&this.ready&&connected){this.gate();finish();safe(()=>this.callbacks.onState?.("live"));}};
      socket.onmessage=event=>{
        if(!this.current(generation))return;
        if(event.data instanceof ArrayBuffer){this.playWhisper(event.data);return;}
        if(typeof event.data!=="string")return;
        let value:any;try{value=JSON.parse(event.data);}catch{return;}
        if(value.type==="webrtc.config"){
          if(this.pc){finish(new Error("Repeated WebRTC configuration"));return;}
          void (async()=>{
            const pc=new RTCPeerConnection({iceServers:value.ice_servers??[]});this.pc=pc;
            this.destination!.stream.getAudioTracks().forEach(track=>{
              const sender=pc.addTrack(track,this.destination!.stream);
              const parameters=sender.getParameters();if(parameters.encodings?.length){parameters.encodings[0].maxBitrate=32000;void sender.setParameters(parameters).catch(()=>{});}
            });
            pc.ontrack=e=>{if(!this.current(generation))return;
              if("jitterBufferTarget" in e.receiver) try{(e.receiver as RTCRtpReceiver & {jitterBufferTarget:number}).jitterBufferTarget=options.playbackTargetMs??60;}catch{ /* browser hint only */ }
              const remote=new MediaStream([e.track]);
              // Chromium requires a media-element consumer to start the RTP
              // playout clock. Its output is muted: the AudioContext graph is
              // the sole audible path and follows the selected output device.
              const element=new Audio();element.muted=true;element.srcObject=remote;this.remoteAudio=element;
              void element.play().catch(()=>safe(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction.")));
              context.createMediaStreamSource(remote).connect(this.speaker!);
            };
            pc.onconnectionstatechange=()=>{
              if(!this.current(generation))return;
              this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:pc.connectionState});
              if(pc.connectionState==="connected"){connected=true;maybeReady();}
              if(pc.connectionState==="failed"||pc.connectionState==="closed"){
                if(!finished)finish(new Error("WebRTC connection failed"));else this.disconnected(generation,"webrtc_"+pc.connectionState);
              }
            };
            const offer=await pc.createOffer();this.guard(generation);await pc.setLocalDescription(offer);
            await new Promise<void>((resolve,reject)=>{
              if(pc.iceGatheringState==="complete"){resolve();return;}
              const timer=setTimeout(()=>{pc.onicegatheringstatechange=null;reject(new Error("ICE gathering timed out"));},7000);
              pc.onicegatheringstatechange=()=>{if(pc.iceGatheringState==="complete"){clearTimeout(timer);pc.onicegatheringstatechange=null;resolve();}};
            });
            this.guard(generation);socket.send(JSON.stringify({type:"webrtc.offer",sdp:pc.localDescription!.sdp}));
          })().catch(error=>finish(error instanceof Error?error:new Error("WebRTC setup failed")));
          return;
        }
        if(value.type==="webrtc.answer") {void this.pc?.setRemoteDescription({type:"answer",sdp:value.sdp}).catch(()=>finish(new Error("Invalid WebRTC answer")));return;}
        if(value.type==="ready"){this.ready=true;this.send({type:"media.capabilities",version:2,versions:[3],whisper:true});maybeReady();}
        if(value.type==="peer.connected"){this.peer=true;this.gate();}
        if(value.type==="peer.disconnected"){this.peer=false;this.gate();safe(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));}
        if(value.type==="call.status")safe(()=>this.callbacks.onCallStatus?.(value));
        if(value.type==="call.error"){if(!finished)finish(new Error(value.detail??"Carrier activation failed"));else {this.stop();safe(()=>this.callbacks.onState?.("error",value.detail));}}
        if(value.type==="call.ended" || value.type==="session.replaced"){
          if(!finished)finish(new Error(value.type));else {this.stop();safe(()=>this.callbacks.onState?.("ended",value.type));}
        }
        if(value.type==="audio.health"){
          this.diagnostics.audioHealth={state:value.state,reason:value.reason,stages:value.stages};
          safe(()=>this.callbacks.onAudioHealth?.(value));
          if(value.state==="audio_degraded"&&!this.deliveryDegraded){this.deliveryDegraded=true;safe(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));}
          else if(this.deliveryDegraded&&value.state==="healthy"&&value.stages?.telephony_to_browser?.state==="healthy"&&context.state==="running"){this.deliveryDegraded=false;safe(()=>this.callbacks.onNotice?.("Speech delivery restored."));}
        }
        if(value.type==="media.delivery"){
          if(value.state==="stalled")safe(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));
          else if(value.state==="flowing"&&this.carrierStalled)safe(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));
          this.carrierStalled=value.state==="stalled";
        }
        if(value.type==="call.notice")safe(()=>this.callbacks.onNotice?.(value.detail??value.reason??"Audio delivery notice"));
        if(value.type==="coach.state"){this.whisperEpoch=value.talking && Number.isInteger(value.epoch)?value.epoch:null;this.whisperBase=null;this.whisper?.port.postMessage({type:"whisper.clear"});}
      };
      socket.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"});
      socket.onclose=e=>{
        if(!this.current(generation))return;
        this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(e.code),was_clean:e.wasClean});
        if(!finished){finish(Object.assign(new Error("WebRTC signaling unavailable"),e.code===1008?{status:403}:{}));return;}
        if(e.code===1008){this.stop();safe(()=>this.callbacks.onState?.("error","Media authorization ended"));return;}
        this.disconnected(generation,"signaling_closed");
      };
    });
    this.guard(generation);
    this.timer=setInterval(()=>{this.runtime.tick();void this.statistics(generation);},1000);
    await this.statistics(generation);
  }
  private send(value:unknown){if(this.socket?.readyState===WebSocket.OPEN && this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(value));}catch{/* reconnect handles failure */}}
  private gate(){const open=this.ready&&this.peer&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!open});this.destination?.stream.getAudioTracks().forEach(t=>t.enabled=open);}
  private async statistics(generation:number){
    if(!this.current(generation)||!this.pc||this.sampling.has(this.pc))return;
    const pc=this.pc,started=performance.now();this.sampling.add(pc);
    try{
      const result=rtcStatistics(await pc.getStats(),this.previous);if(!this.current(generation)||this.pc!==pc)return;
      this.previous=result.previous;
      this.nativeCounts={packetsLost:result.webrtc.packetsLost,packetsDiscarded:result.webrtc.packetsDiscarded,concealedMs:result.webrtc.concealedMs};
      result.webrtc.packetsLost+=this.completedCounts.packetsLost;
      result.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded;
      result.webrtc.concealedMs+=this.completedCounts.concealedMs;
      if(result.webrtc.packetsLost>this.reportedLoss){this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,{timestamp:new Date().toISOString(),direction:"carrier_to_operator",reason:"webrtc_packet_loss",duration_ms:0}].slice(-100);this.reportedLoss=result.webrtc.packetsLost;}
      this.diagnostics={...this.diagnostics,rttMs:result.rtt,queueMs:result.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,result.queueMs),targetMs:this.options?.playbackTargetMs??60,webrtc:result.webrtc,playbackSequenceGaps:result.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};
      const track=this.stream?.getAudioTracks()[0];
      this.transportTelemetry.observe({...result.metrics,rtt_ms:result.rtt,queue_ms:result.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:this.socket?.bufferedAmount??0,stats_errors:this.statsErrors,stats_duration_ms:performance.now()-started,main_thread_max_pause_ms:this.runtime.counters.main_thread_max_pause_ms},
        {...result.states,ice:pc.iceConnectionState,connection:"connected",context:this.context?.state,muted:String(this.muted),device_muted:String(track?.muted),track:track?.readyState});
      this.diagnostics.transportSamples=this.transportTelemetry.recent();
      this.sendStatistics();
      safe(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}));
    }catch{
      if(this.current(generation)&&this.pc===pc){
        this.statsErrors++;
        this.transportTelemetry.observe({stats_errors:this.statsErrors,stats_duration_ms:performance.now()-started},{ice:pc.iceConnectionState,context:this.context?.state,muted:String(this.muted)});
        this.diagnostics.transportSamples=this.transportTelemetry.recent();this.sendStatistics();
        safe(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}));
      }
    }
    finally{this.sampling.delete(pc);}
  }
  private sendStatistics(){
    const track=this.stream?.getAudioTracks()[0];
      if(performance.now()-this.lastStatsSend>=5000){this.lastStatsSend=performance.now();
      this.transportSender.enqueueReport({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,media_transport:"webrtc",codec:"opus",webrtc:this.diagnostics.webrtc,connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:track?.readyState,microphone_device_muted:track?.muted,session_events:this.events,rtt_ms:this.diagnostics.rttMs===null?null:Math.round(this.diagnostics.rttMs),playback_queue_ms:Math.round(this.diagnostics.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:this.diagnostics.webrtc?.packetsLost,drop_events:this.diagnostics.dropEvents,timing:{runtime:this.runtime.counters}}});this.transportSender.enqueue(this.transportTelemetry.drain());}
  }
  private playWhisper(data:ArrayBuffer){
    if(data.byteLength<17||data.byteLength>176||this.whisperEpoch===null||!this.context)return;
    const view=new DataView(data);if(view.getUint32(0,true)!==0x31575041||view.getUint32(4,true)!==this.whisperEpoch)return;
    const source=view.getFloat64(8,true);if(!Number.isFinite(source))return;
    const delta=performance.timeOrigin+performance.now()-source;this.whisperBase=this.whisperBase===null?delta:Math.min(this.whisperBase,delta);const excess=Math.max(0,delta-this.whisperBase);if(excess>200)return;
    const pcm=new Float32Array(data.byteLength-16);for(let i=0;i<pcm.length;i++){const u=(~view.getUint8(i+16))&255;const v=(((u&15)<<3)+132)<<(u>>4&7);pcm[i]=((u&128)?132-v:v-132)/32768;}
    const frame=this.context.sampleRate===8000?pcm:this.whisperResampler.process(pcm,8000,this.context.sampleRate);
    this.whisper?.port.postMessage({type:"whisper",frame,received_audio_ms:this.context.currentTime*1000-excess},[frame.buffer]);
  }
  private disconnected(generation:number,cause:string){
    if(!this.current(generation)||this.recovering)return;
    this.recovering=true;this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:cause});this.cleanup();
    safe(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));
    const recovery=++this.recoveryGeneration;
    const recovering=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===recovery;
    this.recoveryExpiry=setTimeout(()=>{
      if(!recovering())return;
      ++this.recoveryGeneration;this.recovering=false;clearTimeout(this.retry);this.cancelSetup?.();this.cleanup();
      safe(()=>this.callbacks.onState?.("error","WebRTC audio recovery timed out; reconnect audio to the existing call."));
    },30000);
    const attempt=async()=>{
      if(!recovering())return;
      try{
        if(!this.callbacks.refreshMediaURL)throw new Error("Fresh media authorization is required");
        const url=await this.callbacks.refreshMediaURL();if(!recovering())return;await this.connect(url);if(!recovering())return;
        if(this.pc?.connectionState!=="connected")throw new Error("WebRTC disconnected during recovery");
        clearTimeout(this.recoveryExpiry);
        this.recovering=false;this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected"});
      }catch(error){
        if(!recovering())return;this.cleanup();
        if(permanentRTCFailure(error)){clearTimeout(this.recoveryExpiry);this.recovering=false;safe(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return;}
        this.retry=setTimeout(()=>void attempt(),1000);
      }
    };void attempt();
  }
  recordSessionEvent(event:MediaSessionEvent){this.events=[...this.events,event].slice(-100);safe(()=>this.callbacks.onSessionEvent?.(event));}
  setMuted(value:boolean){if(this.muted!==value)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:value?"muted":"unmuted"});this.muted=value;this.gate();if(value)this.send({type:"interrupt"});}
  sendDTMF(digits:string){if(!/^[0-9*#]+$/.test(digits))throw new Error("Invalid DTMF digits");this.send({type:"dtmf",digits});}
  setOutputVolume(value:number){if(!Number.isFinite(value)||value<0||value>1)throw new RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=value;if(this.options)this.options.outputVolume=value;}
  startRingback(country?:string){this.stopRingback();if(this.context&&this.speaker)this.ringback=playRingback(this.context,this.speaker,ringbackPattern(country));}
  stopRingback(){this.ringback?.();this.ringback=undefined;}
  private cleanup(){
    this.transportSender.stop();
    if(this.nativeCounts){for(const key of ["packetsLost","packetsDiscarded","concealedMs"] as const)this.completedCounts[key]+=this.nativeCounts[key];this.nativeCounts=undefined;}
    ++this.generation;this.ready=this.peer=false;this.stopRingback();clearInterval(this.timer);this.timer=undefined;
    const socket=this.socket;this.socket=undefined;if(socket){socket.onclose=socket.onmessage=socket.onerror=null;socket.close();}
    const pc=this.pc;this.pc=undefined;if(pc){pc.onconnectionstatechange=pc.ontrack=null;pc.close();}
    if(this.remoteAudio){this.remoteAudio.pause();this.remoteAudio.srcObject=null;this.remoteAudio=undefined;}
    this.stream?.getTracks().forEach(t=>{t.onended=null;t.stop();});this.stream=undefined;
    this.destination?.stream.getTracks().forEach(t=>t.stop());this.destination=undefined;
    this.capture?.disconnect();this.capture=undefined;this.whisper?.disconnect();this.whisper=undefined;this.whisperEpoch=null;this.whisperBase=null;this.whisperResampler=new PreviewResampler();
    const context=this.context;this.context=undefined;if(context){context.onstatechange=null;void context.close().catch(()=>{});}this.speaker=undefined;this.speakerMeter=undefined;
  }
  stop(){if(this.stopped)return;this.stopped=true;++this.recoveryGeneration;clearTimeout(this.recoveryExpiry);clearTimeout(this.retry);this.cancelSetup?.();this.cleanup();}
}

/** Try one transport at a time; failed setup releases all resources first. */
export function selectableAudio(callbacks:SoftphoneCallbacks, websocket:()=>AudioConnection, rtc:()=>AudioConnection):AudioConnection {
  let selected:AudioConnection|undefined,stopped=false,muted=false;
  return {
    async start(url,options){
      const choice=mediaTransport(options.mediaTransport);
      const start=async(factory:()=>AudioConnection)=>{if(stopped)throw new Error("Audio session cancelled");const connection=factory();selected=connection;connection.setMuted(muted);try{await connection.start(url,options);if(stopped)throw new Error("Audio session cancelled");}catch(error){connection.stop();if(selected===connection)selected=undefined;throw error;}};
      if(choice==="websocket"){await start(websocket);return;}
      try{await start(rtc);}catch(error){if(choice!=="auto"||stopped||permanentRTCFailure(error))throw error;safe(()=>callbacks.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"}));await start(websocket);}
    },
    stop(){stopped=true;selected?.stop();selected=undefined;},setMuted(value){muted=value;selected?.setMuted(value);},
    setOutputVolume(value){selected?.setOutputVolume(value);},sendDTMF(digits){selected?.sendDTMF(digits);},
    recordSessionEvent(event){selected?.recordSessionEvent?.(event);},startRingback(country){selected?.startRingback?.(country);},stopRingback(){selected?.stopRingback?.();},
  };
}
