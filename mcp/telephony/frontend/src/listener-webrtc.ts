import type { AppHandle } from "@apteva/web-sdk";
import type { ListenerAudioConnection, ListenerAudioRuntime, ListenerPlaybackOptions } from "./listener-audio";
import { DEFAULT_SOFTPHONE_AUDIO_OPTIONS, microphoneConstraints, playbackBufferOptions } from "../../ui/softphone-audio";
import { loadAudioModules, type AudioModules } from "./audio-assets";
import { RTCQualityPolicy } from "./webrtc-quality";
import { rtcMediaURL, rtcStatistics } from "./webrtc-audio";

/** Passive listening has no microphone/transmitter. Each explicit PTT obtains
 * a new authorized generation and a fresh uplink PC, then releases both. */
export function createListenerRTC(app:AppHandle, options:ListenerPlaybackOptions):ListenerAudioRuntime {
 return {create(callbacks) {
  const audioOptions={...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,...options};
  const buffer=playbackBufferOptions(audioOptions);
  const quality=new RTCQualityPolicy(audioOptions);
  const receivers:RTCRtpReceiver[]=[];
  let socket:WebSocket|undefined, pc:RTCPeerConnection|undefined, context:AudioContext|undefined, speaker:GainNode|undefined;
  let closed=false, ready=false, connected=false, coaching=false, talkWanted=false, talking=false, generation=0;
  let mic:MediaStream|undefined, capture:AudioWorkletNode|undefined, micSource:MediaStreamAudioSourceNode|undefined, destination:MediaStreamAudioDestinationNode|undefined;
  let uplink:RTCPeerConnection|undefined, modules:AudioModules|undefined, captureLoaded=false;
  let keepalive:ReturnType<typeof setInterval>|undefined, sampleTimer:ReturnType<typeof setInterval>|undefined;
  let volume=audioOptions.outputVolume??1;
  let config:RTCConfiguration={}, bitrate=32000;
  let talkAck:((generation:number,error?:Error)=>void)|undefined, talkAnswer:((sdp:string,generation:number)=>void)|undefined;
  let cancelStart:(()=>void)|undefined, cancelTalk:(()=>void)|undefined;
  const elements:HTMLAudioElement[]=[];
  const send=(value:unknown)=>{if(socket?.readyState===WebSocket.OPEN && socket.bufferedAmount<65536)socket.send(JSON.stringify(value));else throw new Error("Listener connection unavailable");};
  const ensure=()=>{if(closed)throw new Error("Listening cancelled");};
  const stopTalking=()=>{
   const stopped=++generation;talkWanted=talking=false;talkAck?.(stopped,new Error("Coaching cancelled"));cancelTalk?.();talkAnswer=undefined;
   if(keepalive)clearInterval(keepalive);keepalive=undefined;
   if(uplink){uplink.onconnectionstatechange=null;uplink.close();uplink=undefined;}
   capture?.disconnect();capture=undefined;micSource?.disconnect();micSource=undefined;destination?.disconnect();destination=undefined;
   mic?.getTracks().forEach(t=>{t.onended=null;t.stop();});mic=undefined;
   if(coaching && ready && socket?.readyState===WebSocket.OPEN)try{send({type:"coach.stop",generation:stopped});}catch{/* socket already closing */}
   callbacks.onTalking?.(false);
  };
  const blur=()=>stopTalking(),visibility=()=>{if(document.visibilityState!=="visible")stopTalking();};
  const stop=()=>{
   if(closed)return;stopTalking();closed=true;cancelStart?.();
   window.removeEventListener("blur",blur);document.removeEventListener("visibilitychange",visibility);
   if(sampleTimer)clearInterval(sampleTimer);sampleTimer=undefined;
   if(socket){socket.onmessage=socket.onclose=socket.onerror=null;socket.close(1000,"listener_shutdown");}
   if(pc){pc.ontrack=pc.onconnectionstatechange=null;pc.close();}
   for(const element of elements){element.pause();element.srcObject=null;}
   speaker?.disconnect();if(context){context.onstatechange=null;void context.close().catch(()=>{});}
   modules?.dispose();modules=undefined;
  };
  const fail=(reason:string)=>{if(!closed){stop();callbacks.onClose(reason);}};
  const offer=async(peer:RTCPeerConnection,current:()=>boolean)=>{
   await peer.setLocalDescription(await peer.createOffer());
   await new Promise<void>((resolve,reject)=>{
    if(peer.iceGatheringState==="complete"){resolve();return;}
    const timeout=setTimeout(()=>{peer.onicegatheringstatechange=null;reject(new Error("ICE gathering timed out"));},7000);
    peer.onicegatheringstatechange=()=>{if(peer.iceGatheringState==="complete" || !current()){clearTimeout(timeout);peer.onicegatheringstatechange=null;current()?resolve():reject(new Error("Coaching cancelled"));}};
   });
   if(!current())throw new Error("Listening cancelled");return peer.localDescription!.sdp;
  };
  return {
   async start(url,settings){
    coaching=settings?.coaching===true;ensure();
    if(typeof RTCPeerConnection!=="function")throw new Error("WebRTC audio unavailable");
    try{
     context=new AudioContext({latencyHint:"interactive"});await context.resume();ensure();
     if(audioOptions.outputDeviceId && "setSinkId" in context)await (context as AudioContext&{setSinkId(id:string):Promise<void>}).setSinkId(audioOptions.outputDeviceId);
     ensure();speaker=context.createGain();speaker.gain.value=volume;speaker.connect(context.destination);
     context.onstatechange=()=>{if(context?.state==="suspended" || (context?.state as string)==="interrupted")fail("listener_audio_paused");};
     await new Promise<void>((resolve,reject)=>{
      let finished=false, serverReady=false;
      const finish=(error?:Error)=>{if(finished)return;finished=true;clearTimeout(timeout);cancelStart=undefined;error?reject(error):resolve();};
      const timeout=setTimeout(()=>finish(new Error("WebRTC listener timed out")),18000);
      const maybeReady=()=>{if(connected&&serverReady&&!closed&&!ready){ready=true;finish();callbacks.onReady("webrtc");}};
      cancelStart=()=>finish(new Error("Listening cancelled"));
      socket=new WebSocket(rtcMediaURL(url,audioOptions.webrtcFec));
      socket.onmessage=({data})=>{
       if(closed)return;
       if(typeof data!=="string"){finish(new Error("Invalid WebRTC listener frame"));fail("listener_protocol_error");return;}
       let event:any;try{event=JSON.parse(data);}catch{finish(new Error("Invalid listener control"));fail("listener_protocol_error");return;}
       if(event.type==="webrtc.config"){
        if(pc || event.listener!==true){finish(new Error("Invalid WebRTC listener configuration"));return;}
        config={iceServers:event.ice_servers??[]};bitrate=event.bitrate??32000;
        pc=new RTCPeerConnection(config);const peer=pc;
        for(let d=0;d<2;d++)peer.addTransceiver("audio",{direction:"recvonly"});
        peer.ontrack=e=>{
         if(closed||!context||!speaker)return;
         receivers.push(e.receiver);
         const d=peer.getTransceivers().findIndex(t=>t.receiver===e.receiver);if(d<0||d>1){fail("listener_protocol_error");return;}
         if("jitterBufferTarget" in e.receiver)try{(e.receiver as RTCRtpReceiver&{jitterBufferTarget:number}).jitterBufferTarget=buffer.initialTargetMs;}catch{/* optional browser hint */}
         const remote=new MediaStream([e.track]);const element=new Audio();element.muted=true;element.srcObject=remote;elements.push(element);void element.play().catch(()=>{});
         const node=context.createMediaStreamSource(remote);
         if(options.stereo){const pan=context.createStereoPanner();pan.pan.value=d===0?-1:1;node.connect(pan).connect(speaker);}else{const gain=context.createGain();gain.gain.value=0.5;node.connect(gain).connect(speaker);}
        };
        peer.onconnectionstatechange=()=>{
         if(closed)return;
         if(peer.connectionState==="connected"){connected=true;maybeReady();}
         if(peer.connectionState==="failed"||peer.connectionState==="closed"){finish(new Error("WebRTC listener connection failed"));if(finished&&ready)fail("listener_network_error");}
        };
        void offer(peer,()=>!closed).then(sdp=>send({type:"webrtc.offer",sdp})).catch(error=>finish(error));return;
       }
       if(event.type==="webrtc.answer"){void pc?.setRemoteDescription({type:"answer",sdp:event.sdp}).catch(error=>finish(error));return;}
       if(event.type==="listener.ready"){if(coaching&&event.coaching!==true){finish(new Error("Coaching not authorized"));return;}serverReady=true;maybeReady();return;}
       if(event.type==="coach.started"){talkAck?.(event.generation);return;}
       if(event.type==="coach.rejected"){talkAck?.(generation,new Error(event.detail||"Coaching unavailable"));return;}
       if(event.type==="coach.stopped" && (event.generation===undefined||event.generation===generation)){stopTalking();return;}
       if(event.type==="coach.webrtc.answer"){talkAnswer?.(event.sdp,event.generation);return;}
      };
      socket.onerror=()=>{finish(new Error("Listener connection failed"));if(ready)fail("listener_network_error");};
      socket.onclose=e=>{finish(new Error(e.reason||"Listener disconnected"));if(ready)fail(e.reason||"listener_disconnected");};
     });
     ensure();if(coaching){window.addEventListener("blur",blur);document.addEventListener("visibilitychange",visibility);}
     let previous:ReturnType<typeof rtcStatistics>["previous"]|undefined,sampling=false;
     sampleTimer=setInterval(()=>{
      if(closed||!pc||sampling)return;sampling=true;
      void pc.getStats().then(report=>{if(closed)return;const value=rtcStatistics(report,previous);previous=value.previous;
       quality.observe({...value.metrics,receiver_jitter:value.webrtc.jitterMs/1000});
       for(const receiver of receivers)if("jitterBufferTarget" in receiver)try{(receiver as RTCRtpReceiver&{jitterBufferTarget:number}).jitterBufferTarget=quality.target;}catch{/* optional browser hint */}
       callbacks.onDiagnostics?.({media_transport:"webrtc",dropped_ms:[],played_ms:[],max_queue_ms:value.queueMs,max_late_ms:0,sequence_gaps:[],network_excess_ms:0,network_dropped_ms:[],webrtc:value.webrtc});
      }).catch(()=>{}).finally(()=>{sampling=false;});
     },1000);
    }catch(error){stop();throw error;}
   },
   async startTalking(){
    ensure();if(!coaching||!ready||!context)throw new Error("Coaching session not ready");if(talkWanted)return;
    talkWanted=true;const gen=++generation,current=()=>!closed&&talkWanted&&generation===gen;
    try{
     const stream=await navigator.mediaDevices.getUserMedia({audio:microphoneConstraints(audioOptions)});
     if(!current()){stream.getTracks().forEach(t=>t.stop());return;}mic=stream;mic.getTracks().forEach(t=>{t.onended=()=>stopTalking();});
     if(!modules){const loaded=await loadAudioModules(app,true);if(!current()){loaded.dispose();return;}modules=loaded;}
     if(!captureLoaded){await context.audioWorklet.addModule(modules!.urls[0]);captureLoaded=true;}
     if(!current())return;
     await new Promise<void>((resolve,reject)=>{const timeout=setTimeout(()=>{talkAck=undefined;reject(new Error("Coaching activation timed out"));},3000);talkAck=(ack,error)=>{if(error||ack===gen){clearTimeout(timeout);talkAck=undefined;error?reject(error):resolve();}};send({type:"coach.start",generation:gen});});
     if(!current())return;
     // Keep the short authority lease alive while the new uplink negotiates.
     keepalive=setInterval(()=>{if(current())try{send({type:"coach.keepalive",generation:gen});}catch{stopTalking();}},1000);
     capture=new AudioWorkletNode(context,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:true,inputGainDB:audioOptions.inputGainDB,highpassFilter:audioOptions.highpassFilter}});
     destination=context.createMediaStreamDestination();destination.channelCount=1;micSource=context.createMediaStreamSource(stream);micSource.connect(capture).connect(destination);
     const peer=new RTCPeerConnection(config);uplink=peer;
     peer.addTransceiver(destination.stream.getAudioTracks()[0],{direction:"sendonly",streams:[destination.stream],sendEncodings:[{maxBitrate:bitrate}]});
     await new Promise<void>((resolve,reject)=>{
      let settled=false;const finish=(error?:Error)=>{if(settled)return;settled=true;clearTimeout(timeout);cancelTalk=undefined;talkAnswer=undefined;error?reject(error):resolve();};
      const timeout=setTimeout(()=>finish(new Error("Coaching WebRTC timed out")),18000);cancelTalk=()=>finish(new Error("Coaching cancelled"));
      talkAnswer=(sdp,ack)=>{if(ack!==gen||!current())return;void peer.setRemoteDescription({type:"answer",sdp}).catch(error=>finish(error));};
      peer.onconnectionstatechange=()=>{if(!current())return;if(peer.connectionState==="connected")finish();if(peer.connectionState==="failed"||peer.connectionState==="closed"){finish(new Error("Coaching media disconnected"));stopTalking();}};
      void offer(peer,current).then(sdp=>{if(current())send({type:"coach.webrtc.offer",generation:gen,sdp});}).catch(error=>finish(error));
     });
     if(current()){talking=true;callbacks.onTalking?.(true);}
    }catch(error){if(current()){stopTalking();throw error;}}
   },
   stopTalking,stop,
   setOutputVolume(value){if(!Number.isFinite(value)||value<0||value>1)throw new RangeError("Listener volume must be 0–1");volume=value;if(speaker)speaker.gain.value=value;},
  } satisfies ListenerAudioConnection;
 }};
}
