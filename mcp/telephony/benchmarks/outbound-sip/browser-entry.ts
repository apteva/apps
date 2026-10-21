import {WebRTCAudioConnection} from '../../frontend/src/webrtc-audio';
import {DEFAULT_SOFTPHONE_AUDIO_OPTIONS} from '../../ui/softphone-audio';
import {outputWallTimeMS,waitForAudioClockProgress} from '../softphone/audio-clock';
const w=window as any;
w.startSoak=async(config:any)=>{
 const source=new AudioContext({sampleRate:24000});await source.audioWorklet.addModule('/source-worklet.js');await source.resume();
 const node=new AudioWorkletNode(source,'outbound-soak-source');const sourceMarkers:any[]=[];let sourceMarkerOverflow=0;node.port.onmessage=e=>{sourceMarkers.push({index:e.data.index,at_ms:outputWallTimeMS(source,e.data.context_time,performance.timeOrigin,performance.now())});if(sourceMarkers.length>16){sourceMarkers.shift();sourceMarkerOverflow++;}};node.port.postMessage({speech:config.speech});
 const destination=source.createMediaStreamDestination();destination.channelCount=1;node.connect(destination);
 navigator.mediaDevices.getUserMedia=async()=>destination.stream.clone();
 const NativeSocket=window.WebSocket;
 window.WebSocket=class extends NativeSocket {
  send(data:any){
   if(typeof data==='string'){let message:any;try{message=JSON.parse(data);}catch{}
    if(message?.type==='webrtc.offer') {void fetch(config.fixture+'/fixture/offer',{method:'POST',body:JSON.stringify({id:config.id,sdp:message.sdp})}).then(r=>{if(!r.ok)throw new Error('Local offer proxy unavailable');return r.json();}).then(result=>super.send(JSON.stringify({...message,sdp:result.sdp})));return;}
   }
   super.send(data);
  }
 };
 const Native=window.RTCPeerConnection;
 window.RTCPeerConnection=class extends Native{
  async setRemoteDescription(description:RTCSessionDescriptionInit){
   if(description.type==='answer'){
    const response=await fetch(config.fixture+'/fixture/proxy',{method:'POST',body:JSON.stringify({id:config.id,sdp:description.sdp})});
    if(!response.ok)throw new Error('Local fault proxy unavailable');description={...description,sdp:(await response.json()).sdp};
   }
   return super.setRemoteDescription(description);
  }
 };
 const states:any[]=[];let lastDiagnostics:any;
 const session=new WebRTCAudioConnection({refreshMediaURL:async()=>config.media_url,onState:(state,detail)=>{states.push({state,detail,at:Date.now()});if(states.length>32)states.shift();},onDiagnostics:d=>{lastDiagnostics=d;}});
 await session.start(config.media_url,{...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,outputVolume:0},'/capture-worklet.js');
 session.setMuted(false);await waitForAudioClockProgress([source]);
 const origin=outputWallTimeMS(source,0,performance.timeOrigin,performance.now());
 await fetch(config.fixture+'/fixture/clock',{method:'POST',body:JSON.stringify({id:config.id,source_origin_ms:origin,clock_uncertainty_ms:20})});
 w.soak={session,source,states,beginSampling:()=>{sourceMarkers.length=0;sourceMarkerOverflow=0;},snapshot:()=>({id:config.id,at_ms:Date.now(),states:states.slice(-4),diagnostics:lastDiagnostics?{mediaTransport:lastDiagnostics.mediaTransport,webrtc:lastDiagnostics.webrtc,queueMs:lastDiagnostics.queueMs,micActiveRmsDbfs:lastDiagnostics.micActiveRmsDbfs,audioHealth:lastDiagnostics.audioHealth}:null,context_state:source.state,source_ms:source.currentTime*1000,source_markers:sourceMarkers.splice(0),source_marker_overflow:sourceMarkerOverflow}),stop:()=>{session.stop();return source.close();}};
 return {ready:true};
};
