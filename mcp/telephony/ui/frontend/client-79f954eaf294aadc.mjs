function st(t){return t}var St=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function wt(t){let e=(t??"FR").trim().toUpperCase();return St[e]??St.FR}function Ct(t,e){let s=[];if(!(t.cadence.reduce((i,m)=>i+m,0)>0)||!(e>0))return s;let a=0;while(a<e)for(let i=0;i<t.cadence.length;i+=2){let m=t.cadence[i]??0,u=t.cadence[i+1]??0;if(a>=e)break;if(m>0)s.push({start:a,end:Math.min(a+m,e)});a+=m+u}return s}function dt(t,e,s,h=0.12){let a=t.createGain();a.gain.value=0,a.connect(e);let i=h/Math.max(1,s.frequencies.length),m=s.frequencies.map((_)=>{let f=t.createOscillator();return f.type="sine",f.frequency.value=_,f.connect(a),f.start(),f}),u=t.currentTime+0.05,r=0,c=0.01,y=()=>{let _=t.currentTime-u+10;if(_<=r)return;for(let f of Ct(s,_)){if(f.end<=r)continue;let S=u+Math.max(f.start,r),d=u+f.end;a.gain.setValueAtTime(0,S),a.gain.linearRampToValueAtTime(i,S+c),a.gain.setValueAtTime(i,Math.max(S+c,d-c)),a.gain.linearRampToValueAtTime(0,d)}r=_};y();let p=setInterval(y,4000),l=!1;return()=>{if(l)return;l=!0,clearInterval(p);try{a.gain.cancelScheduledValues(0)}catch{}a.gain.value=0;for(let _ of m){try{_.stop()}catch{}_.disconnect()}a.disconnect()}}var C=24000,Y=60;async function bt(t,e){try{await t.audioWorklet.addModule(e)}catch(s){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:s})}}function Rt(t){let e=new Int16Array(t.length);for(let s=0;s<t.length;s++){let h=Math.max(-1,Math.min(1,t[s]));e[s]=h<0?h*32768:h*32767}return e.buffer}class X{history=new Float32Array(64);phase=0;process(t,e,s){if(e===s)return t;let h=new Float32Array(64+t.length);h.set(this.history),h.set(t,64);let a=e/s,i=Math.min(1,s/e)*0.9,m=[],u=this.phase;for(;u<t.length;u+=a){let r=u+32,c=0,y=0;for(let p=Math.ceil(r-32);p<=Math.floor(r+32);p++){let l=p-r,f=(Math.abs(l)<0.000000001?i:Math.sin(Math.PI*i*l)/(Math.PI*l))*(0.5+0.5*Math.cos(Math.PI*l/32));if(p>=0&&p<h.length)c+=h[p]*f,y+=f}m.push(y?c/y:0)}return this.phase=u-t.length,this.history=h.slice(-64),new Float32Array(m)}}function Ot(t){let e=0;for(let s=0;s<t.length;s++)e+=t[s]*t[s];return Math.sqrt(e/Math.max(1,t.length))}var U={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function $(t){let e=t.playbackTargetMs??Y,s=t.playbackMinMs??Math.min(Y,e),h=t.playbackMaxMs??160;for(let a of[e,s,h])if(!Number.isFinite(a)||a<40||a>160)throw RangeError("Playback buffers must be between 40 and 160 ms");if(s>e||e>h)throw RangeError("Playback buffers require min <= target <= max");return{initialTargetMs:e,minTargetMs:s,maxTargetMs:h,hardMaxMs:320}}function L(t){return{...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{},echoCancellation:t.echoCancellation,noiseSuppression:t.noiseSuppression,autoGainControl:t.autoGainControl}}function kt(t){let e=t.getSettings();return{deviceLabel:t.label||"Default microphone",sampleRate:typeof e.sampleRate==="number"?e.sampleRate:null,channelCount:typeof e.channelCount==="number"?e.channelCount:null,echoCancellation:typeof e.echoCancellation==="boolean"?e.echoCancellation:null,noiseSuppression:typeof e.noiseSuppression==="boolean"?e.noiseSuppression:null,autoGainControl:typeof e.autoGainControl==="boolean"?e.autoGainControl:null}}function W(t){if(!Number.isFinite(t)||t<=0)return null;return 20*Math.log10(t)}function Ft(t,e,s){let h=new ArrayBuffer(44+e*2),a=new DataView(h),i=(u,r)=>{for(let c=0;c<r.length;c++)a.setUint8(u+c,r.charCodeAt(c))};i(0,"RIFF"),a.setUint32(4,36+e*2,!0),i(8,"WAVE"),i(12,"fmt "),a.setUint32(16,16,!0),a.setUint16(20,1,!0),a.setUint16(22,1,!0),a.setUint32(24,s,!0),a.setUint32(28,s*2,!0),a.setUint16(32,2,!0),a.setUint16(34,16,!0),i(36,"data"),a.setUint32(40,e*2,!0);let m=44;for(let u of t)for(let r=0;r<u.length;r++)a.setInt16(m,u[r],!0),m+=2;return new Blob([h],{type:"audio/wav"})}class ht{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new X;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(t,e=!0){this.onLevel=t;this.recordAudio=e}async start(t,e){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:L(e)}),this.ensureOpen();let s=this.stream.getAudioTracks()[0];if(!s)throw Error("No microphone audio track was returned.");this.settings=kt(s);try{this.ctx=new AudioContext({sampleRate:C,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await bt(this.ctx,t),this.ensureOpen();let h=this.ctx.sampleRate,a=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:e.inputGainDB,highpassFilter:e.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(i)=>{if(this.stopped)return;if(!(i.data instanceof Float32Array)){if(i.data.type==="capture.stats")this.peak=Math.max(this.peak,i.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,i.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,i.data.limiter_reduction_db??0);return}let m=i.data;if(h!==C)m=this.resampler.process(m,h,C);let u=Ot(m);if(this.onLevel?.(u),this.recordAudio)this.frames.push(new Int16Array(Rt(m)));if(this.samples+=m.length,u>=0.005){for(let r=0;r<m.length;r++)this.activeSquares+=m[r]*m[r];this.activeSamples+=m.length}},a.connect(this.capture),this.settings}catch(s){throw await this.release(),s}}async stop(){this.stopped=!0;let t=this.settings,e=this.frames,s=this.samples,h=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,a=this.peak;if(await this.release(),!t||s===0)throw Error("No microphone audio was captured.");return{audio:Ft(e,s,C),durationMs:Math.round(s*1000/C),sampleRate:C,activeRmsDbfs:W(h),peakDbfs:W(a),postPeakDbfs:W(this.postPeak||a),limiterReductionDb:this.limiterReductionDB,settings:t}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let t of this.stream?.getTracks()??[])t.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class at{callbacks;worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;opened=!1;cancelWorkerStart;microphoneTransportReady=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};diagnostics={rttMs:null,queueMs:0,targetMs:Y,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:C,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:U.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t={}){this.callbacks=t}get isMuted(){return this.muted}async start(t,e,s,h=U){let a=$(h);this.diagnostics.targetMs=a.initialTargetMs,this.callbacks.onState?.("connecting");try{this.stream=await navigator.mediaDevices.getUserMedia({audio:L(h)}),this.ensureOpen();let i=this.stream.getAudioTracks()[0];if(!i)throw Error("No microphone audio track was returned.");if(i.readyState==="ended")throw Error("Microphone disconnected before audio setup.");i.onmute=()=>this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser."),i.onunmute=()=>this.callbacks.onNotice?.("Microphone input restored."),i.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let m=kt(i);this.diagnostics={...this.diagnostics,microphoneSampleRate:m.sampleRate??0,microphoneChannelCount:m.channelCount??0,echoCancellation:m.echoCancellation,noiseSuppression:m.noiseSuppression,autoGainControl:m.autoGainControl,micInputGainDb:h.inputGainDB};try{this.ctx=new AudioContext({sampleRate:C,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();if(this.ensureOpen(),h.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(h.outputDeviceId),this.ensureOpen();await bt(this.ctx,e),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let u=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:h.inputGainDB,highpassFilter:h.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:a}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),u.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=h.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await this.openWorker(t,s),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running"&&this.microphoneTransportReady)this.callbacks.onState?.("live")},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(i){if(this.closed)this.teardown();if(!this.closed)this.fail(i instanceof Error?i.message:"browser audio setup failed");throw i}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){if(await this.ctx?.resume(),this.microphoneTransportReady)this.callbacks.onState?.("live")}setOutputVolume(t){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,t))}sendDTMF(t){if(/^[0-9*#]+$/.test(t))this.sendText(JSON.stringify({type:"dtmf",digits:t}))}startRingback(t){if(this.closed||!this.ctx||this.ringback)return;this.ringback=dt(this.ctx,this.ctx.destination,wt(t))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="capture.stats")return;this.micLevel=this.muted?0:e.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:W(e.active_rms??0),micPeakDbfs:W(e.pre_peak??0),micPostPeakDbfs:W(e.post_peak??0),micInputGainDb:e.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:e.limiter_reduction_db??0}},this.playback.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="stats")return;this.playbackTiming={played_ms:e.played_ms,max_residence_ms:e.max_residence_ms,drop_totals_ms:e.drop_totals_ms,coaching:{played_ms:e.whisper_played_ms,dropped_ms:e.whisper_dropped_ms,max_queue_ms:e.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,e.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:e.whisper_played_ms??0,coachingDroppedMs:e.whisper_dropped_ms??0,coachingMaxQueueMs:e.whisper_max_queue_ms??0,queueMs:e.queue_ms??0,targetMs:e.target_ms??Y,underruns:e.underruns??0,droppedMs:e.dropped_ms??0,maxQueueMs:e.max_queue_ms??0,playbackSequenceGaps:e.playback_sequence_gaps??0,dropEvents:[...this.diagnostics.dropEvents.filter((s)=>s.direction!=="carrier_to_operator"),...e.drop_events??[]].slice(-100)},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(t,e){return new Promise((s,h)=>{let a=new Worker(e);this.worker=a;let i=new MessageChannel,m=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:i.port1},[i.port1]),this.playback?.port.postMessage({type:"transport",port:m.port1},[m.port1]);let u=!1,r=setTimeout(()=>{if(u)return;u=!0,h(Error("audio connection timed out"))},1e4),c=(y)=>{if(u)return;if(u=!0,clearTimeout(r),y)h(y);else s()};this.cancelWorkerStart=()=>c(Error("audio session closed")),a.onmessage=(y)=>{if(this.closed){c(Error("audio session closed"));return}let p=y.data;if(p?.type==="socket.open")this.opened=!0,this.mediaSocketConnected=!0,this.startRTTProbe(),c();else if(p?.type==="socket.message")this.handleControl(p.data);else if(p?.type==="socket.close")if(this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else c(Error("audio connection closed before it was ready"));else if(p?.type==="socket.failed")this.mediaSocketConnected=!1,c(Error(p.detail||"audio connection lost")),this.fail(p.detail||"audio connection lost");else if(p?.type==="transport.drop"&&p.event)this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,p.event].slice(-100);else if(p?.type==="transport.stats")this.diagnostics.websocketBufferedBytes=p.buffered_bytes??0,this.transportTiming=p.timing??{}},a.onerror=()=>{if(c(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},a.postMessage({type:"init",audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:t,contextRate:this.ctx?.sampleRate??C,muted:this.muted,capturePort:i.port2,playbackPort:m.port2},[i.port2,m.port2])})}carrierDeliveryStalled=!1;handleControl(t){if(this.closed)return;try{let e=JSON.parse(t);if(e.type==="dtmf.error"||e.type==="dtmf.sent")this.callbacks.onNotice?.(e.type==="dtmf.sent"?"Keypad tone sent":e.detail||"Keypad tone failed");else if(e.type==="pong"&&typeof e.nonce==="number"&&e.nonce>=0)this.diagnostics.captureSequenceGaps=e.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-e.nonce)),this.callbacks.onDiagnostics?.({...this.diagnostics});else if(e.type==="call.ended"||e.type==="session.replaced"){this.closed=!0;try{this.callbacks.onState?.("ended",e.type)}finally{this.teardown()}}else if(e.type==="call.status"&&typeof e.call_id==="string"&&typeof e.status==="string"){if(e.hold_state&&e.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callbacks.onCallStatus?.(e)}else if(e.type==="call.error")this.fail(e.detail||"The call could not be connected.");else if(e.type==="coach.state")this.callbacks.onNotice?.(e.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(e.type==="media.delivery"){let s=e.state;if(s==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(s==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=s==="stalled"}else if(e.type==="peer.disconnected")this.microphoneTransportReady=!1,this.worker?.postMessage({type:"microphone.ready",value:!1}),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(e.type==="peer.connected")this.microphoneTransportReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.callbacks.onState?.("live",this.opened?void 0:"Audio reconnected")}catch{}}startRTTProbe(){this.stopRTTProbe();let t=()=>{this.sendText(JSON.stringify({type:"ping",nonce:performance.now()})),this.sendDiagnostics()};t(),this.pingTimer=setInterval(t,5000)}sendText(t){this.worker?.postMessage({type:"send.text",data:t})}sendDiagnostics(){let t=this.diagnostics;this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{timing:{transport:this.transportTiming,playback:this.playbackTiming},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:t.rttMs,playback_queue_ms:t.queueMs,playback_target_ms:t.targetMs,playback_max_queue_ms:t.maxQueueMs,playback_underruns:t.underruns,playback_dropped_ms:t.droppedMs,websocket_buffered_bytes:t.websocketBufferedBytes,audio_context_rate:t.audioContextRate,microphone_sample_rate:t.microphoneSampleRate,microphone_channel_count:t.microphoneChannelCount,echo_cancellation:t.echoCancellation,noise_suppression:t.noiseSuppression,auto_gain_control:t.autoGainControl,mic_active_rms_dbfs:t.micActiveRmsDbfs,mic_peak_dbfs:t.micPeakDbfs,mic_post_peak_dbfs:t.micPostPeakDbfs,mic_input_gain_db:t.micInputGainDb,mic_limiter_reduction_db:t.micLimiterReductionDb,capture_sequence_gaps:t.captureSequenceGaps,playback_sequence_gaps:t.playbackSequenceGaps,drop_events:t.dropEvents}})),this.callbacks.onDiagnostics?.({...t})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setMuted(t){if(this.muted=t,this.worker?.postMessage({type:"muted",value:t}),this.capture?.port.postMessage({type:"muted",value:t}),t)this.sendText(JSON.stringify({type:"interrupt"}))}stop(){let t=!this.closed;this.closed=!0;try{if(t)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(t){this.closed=!0;try{this.callbacks.onState?.("error",t)}finally{this.teardown()}}teardown(){this.stopRingback(),this.microphoneTransportReady=!1,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let t=this.worker;if(t?.postMessage({type:"close"}),t)setTimeout(()=>t.terminate(),100);this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((e)=>e.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var re=C*Y/1000;var F=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
const CROSSFADE_MS = 5;

class SoftphoneCaptureProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const config = options.processorOptions || {};
    this.inputGain = 10 ** ((config.inputGainDB ?? 0) / 20);
    this.inputGainDB = config.inputGainDB ?? 0;
    this.highpass = config.highpassFilter !== false;
    this.ceiling = 10 ** (-3 / 20);
    this.lookaheadSamples = Math.max(1, Math.round(sampleRate * 0.005));
    this.delay = new Float32Array(this.lookaheadSamples);
    this.delayOffset = 0;
    this.peakValues = new Float32Array(this.lookaheadSamples + 1);
    this.peakIndices = new Float64Array(this.lookaheadSamples + 1);
    this.peakHead = this.peakTail = this.sampleIndex = 0;
    this.limiterGain = 1;
    this.release = Math.exp(-1 / (sampleRate * 0.1));
    this.hpAlpha = (1 / (2 * Math.PI * 80)) / ((1 / (2 * Math.PI * 80)) + (1 / sampleRate));
    this.hpX = 0;
    this.hpY = 0;
    this.buffer = new Float32Array(FRAME_SAMPLES);
    this.filled = 0;
    this.sequence = 0;
    this.transport = null;
    this.muted = false;
    this.statsSamples = 0;
    this.activeSquares = 0;
    this.activeSamples = 0;
    this.prePeak = 0;
    this.postPeak = 0;
    this.maxReductionDB = 0;
    this.port.onmessage = (event) => {
      if (event.data?.type === "transport" && event.data.port) {
        this.transport = event.data.port;
        this.transport.onmessage = (event) => {
          if (event.data?.type === "clock.probe") this.transport.postMessage({type:"clock.reply", nonce:event.data.nonce, audio_ms:currentTime*1000});
        };
        this.transport.start?.();
      } else if (event.data?.type === "muted") {
        this.muted = Boolean(event.data.value);
        this.buffer.fill(0); this.filled = 0;
        this.delay.fill(0); this.delayOffset = 0; this.peakHead = this.peakTail = this.sampleIndex = 0;
        this.hpX = this.hpY = 0; this.limiterGain = 1;
        this.activeSquares = this.activeSamples = this.prePeak = this.postPeak = this.maxReductionDB = 0;
      }
    };
  }

  filterAndLimit(value) {
    this.prePeak = Math.max(this.prePeak, Math.abs(value));
    let filtered = value;
    if (this.highpass) {
      filtered = this.hpAlpha * (this.hpY + value - this.hpX);
      this.hpX = value;
      this.hpY = filtered;
    }
    filtered *= this.inputGain;
    this.delay[this.delayOffset] = filtered;
    const length=this.peakValues.length, index=this.sampleIndex++, amplitude=Math.abs(filtered);
    while (this.peakHead!==this.peakTail && this.peakIndices[this.peakHead]<=index-this.lookaheadSamples) this.peakHead=(this.peakHead+1)%length;
    while (this.peakHead!==this.peakTail) { const last=(this.peakTail-1+length)%length; if(this.peakValues[last]>amplitude) break; this.peakTail=last; }
    this.peakValues[this.peakTail]=amplitude; this.peakIndices[this.peakTail]=index; this.peakTail=(this.peakTail+1)%length;
    const peak=this.peakValues[this.peakHead];
    const wanted = peak > this.ceiling ? this.ceiling / peak : 1;
    if (wanted < this.limiterGain) this.limiterGain = wanted;
    else this.limiterGain = 1 - (1 - this.limiterGain) * this.release;
    const readOffset = (this.delayOffset + 1) % this.delay.length;
    let output = this.delay[readOffset] * this.limiterGain;
    this.delayOffset = readOffset;
    output = Math.max(-this.ceiling, Math.min(this.ceiling, output));
    this.postPeak = Math.max(this.postPeak, Math.abs(output));
    this.maxReductionDB = Math.max(this.maxReductionDB, -20 * Math.log10(Math.max(1e-6, this.limiterGain)));
    return output;
  }

  emitFrame() {
    const frame = this.buffer.slice(0);
    const sequence = this.sequence++;
    const timestampMS = currentTime * 1000;
    if (this.transport) this.transport.postMessage({ type: "capture", frame, sequence, timestamp_ms: timestampMS, sample_rate: sampleRate }, [frame.buffer]);
    else this.port.postMessage(frame, [frame.buffer]);
  }

  reportStats() {
    const activeRms = this.activeSamples > 0 ? Math.sqrt(this.activeSquares / this.activeSamples) : 0;
    this.port.postMessage({
      type: "capture.stats", active_rms: activeRms, pre_peak: this.prePeak, post_peak: this.postPeak,
      input_gain_db: this.inputGainDB, limiter_reduction_db: this.maxReductionDB,
    });
    this.activeSquares = this.activeSamples = this.prePeak = this.postPeak = this.maxReductionDB = 0;
  }

  process(inputs, outputs) {
    const channel = inputs[0] && inputs[0][0];
    const out = outputs[0]?.[0];
    if (out) out.fill(0);
    if (!channel) return true;
    for (let i = 0; i < channel.length; i += 1) {
      const processed = this.muted ? 0 : this.filterAndLimit(channel[i]);
      this.buffer[this.filled++] = processed;
      if (Math.abs(processed) >= 0.005) { this.activeSquares += processed * processed; this.activeSamples += 1; }
      if (this.filled === this.buffer.length) { this.emitFrame(); this.filled = 0; }
    }
    this.statsSamples += channel.length;
    if (this.statsSamples >= sampleRate / 10) { this.statsSamples -= sampleRate / 10; this.reportStats(); }
    return true;
  }
}

class SoftphonePlaybackProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const config = options.processorOptions || {};
    this.initialTargetMs = config.initialTargetMs || 60;
    this.minTargetMs = config.minTargetMs || 60;
    this.maxTargetMs = config.maxTargetMs || 160;
    this.hardMaxMs = config.hardMaxMs || 320;
    this.targetMs = this.initialTargetMs;
    this.queue = [];
    this.queued = 0;
    this.offset = 0;
    this.playing = false;
    this.startupWaitSamples = 0;
    this.underruns = 0;
    this.droppedSamples = 0;
    this.maxQueuedSamples = 0;
    this.processedSinceStats = 0;
    this.stableSamples = 0;
    this.dropEvents = [];
    this.sequenceGaps = 0;
    this.lastArrivalMS = null;
    this.lastFrameMS = 20;
    this.maxResidenceMS = 0;
    this.playedSamples = 0;
    this.dropTotals = {};
    this.expectedSequence = null;
    this.needsCrossfade = false;
    this.lastOutputTail = new Float32Array(Math.max(1, Math.round(sampleRate * CROSSFADE_MS / 1000)));
    this.whisperQueue = []; this.whisperOffset = 0; this.whisperQueued = 0;
    this.whisperPlayed = 0; this.whisperDropped = 0; this.whisperMaxQueued = 0;
    this.transport = null;
    this.speakerPeak = 0;
    this.port.onmessage = (event) => {
      if (event.data?.type === "transport" && event.data.port) {
        this.transport = event.data.port;
        this.transport.onmessage = (transportEvent) => this.handleMessage(transportEvent.data);
        this.transport.start?.();
      } else this.handleMessage(event.data);
    };
  }

  handleMessage(message) {
    if(message?.type==="whisper.clear") {
      this.whisperDropped+=this.whisperQueued; this.whisperQueue=[]; this.whisperOffset=0; this.whisperQueued=0; return;
    }
    if(message?.type==="whisper") {
      if(!(message.frame instanceof Float32Array) || message.frame.length>this.msToSamples(20)+1) return;
      const arrived=Number.isFinite(message.received_audio_ms) ? Math.min(currentTime*1000,message.received_audio_ms) : currentTime*1000;
      this.whisperQueue.push({frame:message.frame,arrived});this.whisperQueued+=message.frame.length;
      while(this.whisperQueue.length>6) { const old=this.whisperQueue.shift(); const n=old.frame.length-this.whisperOffset;this.whisperQueued-=n;this.whisperDropped+=n;this.whisperOffset=0; }
      this.whisperMaxQueued=Math.max(this.whisperMaxQueued,this.whisperQueued);return;
    }
    if (message === "flush" || message?.type === "flush") {
      this.whisperDropped+=this.whisperQueued;this.whisperQueue=[];this.whisperQueued=0;this.whisperOffset=0;
      this.dropOldest(this.queued, "playback_flush");
      this.queue = []; this.queued = 0; this.offset = 0; this.playing = false;
      this.startupWaitSamples = this.stableSamples = 0;
      this.expectedSequence = null; this.lastArrivalMS = null;
      this.targetMs = this.initialTargetMs;
      this.needsCrossfade = false; this.lastOutputTail.fill(0);
      return;
    }
    const chunk = message?.frame ?? message;
    if (!chunk || typeof chunk.length !== "number") return;
    const sequence = Number.isInteger(message?.sequence) ? message.sequence : null;
    if (sequence !== null) {
      if (this.expectedSequence !== null && sequence > this.expectedSequence) this.sequenceGaps += sequence - this.expectedSequence;
      this.expectedSequence = sequence + 1;
    }
    const now = currentTime * 1000;
    // Arrival variation is measured on the render clock, never by subtracting
    // worker performance.now() from AudioContext.currentTime.
    if (this.lastArrivalMS !== null) {
      const excess = now - this.lastArrivalMS - this.lastFrameMS;
      if (excess > 20) {
        this.targetMs = Math.min(this.maxTargetMs, Math.max(this.targetMs, Math.ceil((excess + this.minTargetMs) / 10) * 10));
        this.stableSamples = 0;
      }
    }
    this.lastArrivalMS = now;
    this.lastFrameMS = chunk.length * 1000 / sampleRate;
    const received = Number.isFinite(message?.received_audio_ms) ? Math.min(now, message.received_audio_ms + (message.clock_uncertainty_ms || 0)) : now;
    this.queue.push({ frame: chunk, sequence, arrived_ms: received });
    this.queued += chunk.length;
    this.maxQueuedSamples = Math.max(this.maxQueuedSamples, this.queued);
    // The target controls startup/rebuffering, not permission to delete speech.
    // Moderate catch-up bursts can drain naturally below the hard latency cap.
    const hardMax = this.msToSamples(this.hardMaxMs);
    if (this.queued > hardMax) this.dropOldest(this.queued - this.msToSamples(this.maxTargetMs), "playback_hard_limit");
  }

  msToSamples(ms) { return Math.round(sampleRate * ms / 1000); }

  dropOldest(samples, reason) {
    const before = this.queued;
    let remaining = Math.max(0, samples);
    let sequence = null;
    while (remaining > 0 && this.queue.length > 0) {
      const item = this.queue[0];
      if (sequence === null) sequence = item.sequence;
      const available = item.frame.length - this.offset;
      const take = Math.min(available, remaining);
      this.offset += take; this.queued -= take; this.droppedSamples += take; remaining -= take;
      if (this.offset >= item.frame.length) { this.queue.shift(); this.offset = 0; }
    }
    const dropped = before - this.queued;
    if (dropped > 0) {
      this.stableSamples = 0;
      this.dropTotals[reason] = (this.dropTotals[reason] || 0) + dropped;
      this.needsCrossfade = true;
      this.dropEvents.push({
        timestamp: new Date().toISOString(), direction: "carrier_to_operator", reason,
        duration_ms: Math.round(dropped * 1000 / sampleRate),
        queue_before_ms: Math.round(before * 1000 / sampleRate), queue_after_ms: Math.round(this.queued * 1000 / sampleRate),
        sequence,
      });
      if (this.dropEvents.length > 100) this.dropEvents.shift();
    }
  }

  applyCrossfade(item) {
    if (!this.needsCrossfade) return;
    const n = Math.min(this.lastOutputTail.length, item.frame.length - this.offset);
    for (let i = 0; i < n; i += 1) {
      const t = (i + 1) / (n + 1);
      item.frame[this.offset + i] = this.lastOutputTail[this.lastOutputTail.length - n + i] * (1 - t) + item.frame[this.offset + i] * t;
    }
    this.needsCrossfade = false;
  }

  reportStats() {
    this.port.postMessage({
      whisper_played_ms:this.whisperPlayed*1000/sampleRate,whisper_dropped_ms:this.whisperDropped*1000/sampleRate,whisper_max_queue_ms:this.whisperMaxQueued*1000/sampleRate,
      type: "stats", queue_ms: Math.round(this.queued * 1000 / sampleRate), target_ms: this.targetMs,
      underruns: this.underruns, dropped_ms: Math.round(this.droppedSamples * 1000 / sampleRate),
      max_queue_ms: Math.round(this.maxQueuedSamples * 1000 / sampleRate),
      playback_sequence_gaps: this.sequenceGaps, drop_events: this.dropEvents.slice(-100),
      speaker_level: this.speakerPeak, played_ms: this.playedSamples * 1000 / sampleRate,
      max_residence_ms: this.maxResidenceMS,
      drop_totals_ms: Object.fromEntries(Object.entries(this.dropTotals).map(([reason, samples]) => [reason, samples * 1000 / sampleRate])),
    });
    this.speakerPeak = 0;
  }

  process(_inputs, outputs) {
    const out = outputs[0][0];
    if (!out) return true;
    out.fill(0);
    while (this.queue.length && currentTime * 1000 - this.queue[0].arrived_ms >= this.hardMaxMs) {
      this.dropOldest(this.queue[0].frame.length - this.offset, "playback_age_limit");
    }
    if (!this.playing && this.queued > 0) this.startupWaitSamples += out.length;
    if (!this.playing && this.queued > 0 && (this.queued >= this.msToSamples(this.targetMs) || this.startupWaitSamples >= this.msToSamples(this.targetMs))) { this.playing = true; this.startupWaitSamples = 0; }
    if (this.queued === 0) this.startupWaitSamples = 0;
    let written = 0;
    if (this.playing) {
      while (written < out.length && this.queue.length > 0) {
        const item = this.queue[0];
        this.maxResidenceMS = Math.max(this.maxResidenceMS, currentTime * 1000 - item.arrived_ms);
        this.applyCrossfade(item);
        const take = Math.min(out.length - written, item.frame.length - this.offset);
        out.set(item.frame.subarray(this.offset, this.offset + take), written);
        written += take; this.offset += take; this.queued -= take;
        if (this.offset >= item.frame.length) { this.queue.shift(); this.offset = 0; }
      }
      if (written < out.length) {
        this.underruns += 1; this.targetMs = Math.min(this.maxTargetMs, this.targetMs + 20);
        this.playing = false; this.stableSamples = 0;
      } else {
        this.stableSamples += out.length;
        if (this.stableSamples >= sampleRate * 30 && this.targetMs > this.minTargetMs) {
          this.targetMs = Math.max(this.minTargetMs, this.targetMs - 10); this.stableSamples = 0;
        }
      }
    }
    this.playedSamples += written;
    const tail = Math.min(out.length, this.lastOutputTail.length);
    for (let i = 0; i < written; i += 1) this.speakerPeak = Math.max(this.speakerPeak, Math.abs(out[i]));
    if (tail < this.lastOutputTail.length) this.lastOutputTail.copyWithin(0, tail);
    this.lastOutputTail.set(out.subarray(out.length - tail), this.lastOutputTail.length - tail);
    // Independent adviser-only overlay. It never feeds the microphone graph,
    // changes caller adaptation, or evicts caller samples.
    let whisperWritten=0;
    while(whisperWritten<out.length && this.whisperQueue.length) {
      const item=this.whisperQueue[0];
      if(currentTime*1000-item.arrived>=200) { const n=item.frame.length-this.whisperOffset;this.whisperDropped+=n;this.whisperQueued-=n;this.whisperQueue.shift();this.whisperOffset=0;continue; }
      const take=Math.min(out.length-whisperWritten,item.frame.length-this.whisperOffset);
      for(let i=0;i<take;i++) { const at=whisperWritten+i,headroom=Math.max(0,1-Math.abs(out[at]));const voice=item.frame[this.whisperOffset+i]*0.5;out[at]+=Math.max(-headroom,Math.min(headroom,voice)); }
      this.whisperOffset+=take;this.whisperQueued-=take;this.whisperPlayed+=take;whisperWritten+=take;
      if(this.whisperOffset>=item.frame.length) { this.whisperQueue.shift();this.whisperOffset=0; }
    }
    this.processedSinceStats += out.length;
    if (this.processedSinceStats >= sampleRate) { this.processedSinceStats -= sampleRate; this.reportStats(); }
    return true;
  }
}

registerProcessor("softphone-capture", SoftphoneCaptureProcessor);
registerProcessor("softphone-playback", SoftphonePlaybackProcessor);
`;var it=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
// and consume frames directly so panel rendering cannot stall live speech.
const MAGIC = 0x31545041; // "APT1" in little endian.
const HEADER_BYTES = 16;
const MAGIC_V3 = 0x33545041;
const MAGIC_V2 = 0x32545041;
const MAX_CAPTURE_AGE_MS = 250;
const SAMPLE_RATE = 24_000;
const MAX_RECONNECT_MS = 30_000;
const MAX_RECONNECT_DELAY_MS = 4_000;
const MAX_CAPTURE_BUFFERED_BYTES = SAMPLE_RATE * 2 * 0.12;

let socket = null;
let mediaURL = "";
let capturePort = null;
let playbackPort = null;
let closed = false;
let microphoneReady = false;
let muted = false;
let healthTimer = null;
let reconnectStartedAt = 0;
let reconnectAttempt = 0;
let playbackSequence = 0;
let contextRate = SAMPLE_RATE;
let reconnectTimer = null;
let framedV2 = false;
let audioClockOffset = null;
let audioClockUncertainty = 0;
let serverClockOffset = null;
let serverClockUncertainty = null;
let serverClockSampleAt = 0;
let captureGateOpenedAt = 0;
let playbackExpected = null;
let playbackDeliveryBase = null;
let whisperEpoch = null;
let whisperDeliveryBase = null;
const timing = { capture_frames:0, capture_sent_ms:0, capture_dropped_ms:0, capture_max_age_ms:0, playback_received_ms:0, playback_sequence_gaps:0, playback_max_transit_ms:0, playback_max_server_queue_ms:0, playback_transport_dropped_ms:0, playback_source_dropped_ms:0, playback_max_source_age_ms:0, playback_max_delivery_excess_ms:0, playback_source_timestamp_ms:0, playback_source_sequence:0, playback_source_epoch:0, worker_max_tick_gap_ms:0, drop_totals_ms:{} };
function monotonicEpochMS() { return performance.timeOrigin + performance.now(); }
function stats() { postMessage({type:"transport.stats", buffered_bytes:socket?.bufferedAmount||0, timing:{...timing, clock_uncertainty_ms:serverClockUncertainty, clock_sample_age_ms:serverClockSampleAt ? monotonicEpochMS()-serverClockSampleAt : null}}); }
function dropCapture(message,reason,age=0) {
 const duration = message.frame.length*1000/(message.sample_rate||SAMPLE_RATE);
 timing.capture_dropped_ms+=duration;
 timing.drop_totals_ms[reason]=(timing.drop_totals_ms[reason]||0)+duration;
 resamplers.delete(\`\${message.sample_rate || SAMPLE_RATE}:\${SAMPLE_RATE}\`);
 postMessage({type:"transport.drop",event:{timestamp:new Date().toISOString(),direction:"operator_to_carrier",reason,duration_ms:duration,queue_before_ms:age,sequence:message.sequence}});
}


function pcm16(floatFrame) {
  const out = new Int16Array(floatFrame.length);
  for (let i = 0; i < floatFrame.length; i += 1) {
    const value = Math.max(-1, Math.min(1, floatFrame[i]));
    out[i] = value < 0 ? value * 0x8000 : value * 0x7fff;
  }
  return out;
}

// Streaming, windowed-sinc low-pass conversion. History and fractional phase
// carry across frames; the fixed delay supplies future taps without edge resets.
const resamplers = new Map();
function resample(frame, from, to, namespace = "") {
  if (from === to) return frame;
  const key = \`\${namespace}\${from}:\${to}\`;
  let state = resamplers.get(key);
  if (!state) { state = {history:new Float32Array(64), phase:0}; resamplers.set(key,state); }
  const input = new Float32Array(state.history.length + frame.length);
  input.set(state.history); input.set(frame, state.history.length);
  const ratio = from / to, cutoff = Math.min(1,to/from) * 0.90, taps = 64;
  const values = [];
  let pos = state.phase;
  for (; pos < frame.length; pos += ratio) {
    const center = pos + 32;
    let value=0, weight=0;
    for (let j=Math.ceil(center-32); j<=Math.floor(center+32); j++) {
      const x=j-center;
      const sinc = Math.abs(x)<1e-9 ? cutoff : Math.sin(Math.PI*cutoff*x)/(Math.PI*x);
      const w=sinc*(0.5+0.5*Math.cos(Math.PI*x/32));
      if (j>=0 && j<input.length) { value+=input[j]*w; weight+=w; }
    }
    values.push(weight ? value/weight : 0);
  }
  state.phase = pos-frame.length;
  state.history = input.slice(-taps);
  return new Float32Array(values);
}

function framedCapture(frame, sequence, timestampMS, sourceRate, ageMS = 0) {
  const pcm = pcm16(resample(frame, sourceRate || SAMPLE_RATE, SAMPLE_RATE));
  const header = framedV2 ? 32 : HEADER_BYTES;
  const out = new ArrayBuffer(header + pcm.byteLength);
  const view = new DataView(out);
  view.setUint32(0, framedV2 ? MAGIC_V2 : MAGIC, true);
  view.setUint32(4, sequence >>> 0, true);
  view.setFloat64(8, timestampMS, true);
  if (framedV2) { view.setFloat64(16, monotonicEpochMS(), true); view.setFloat64(24, ageMS, true); }
  new Uint8Array(out, header).set(new Uint8Array(pcm.buffer));
  return out;
}

function decodePlayback(buffer) {
  const input = new Int16Array(buffer);
  const out = new Float32Array(input.length);
  for (let i = 0; i < input.length; i += 1) out[i] = input[i] / 0x8000;
  return out;
}

function capture(message) {
  if (message?.type === "clock.reply") {
    const now = monotonicEpochMS(), rtt = now-message.nonce;
    if (rtt>=0 && rtt<=50 && Number.isFinite(message.audio_ms)) {
      audioClockOffset = (now+message.nonce)/2-message.audio_ms;
      audioClockUncertainty = rtt/2 + 10; // Audio render quantum and timestamp sampling.
    }
    return;
  }
  if (message?.type !== "capture" || muted || !microphoneReady || !socket || socket.readyState !== WebSocket.OPEN) return;
  const now = monotonicEpochMS();
  const mapped = Number.isFinite(message.timestamp_ms) && audioClockOffset!==null ? message.timestamp_ms+audioClockOffset : null;
  const age = mapped===null ? 0 : Math.max(0,now-mapped);
  timing.capture_frames++;
  timing.capture_max_age_ms=Math.max(timing.capture_max_age_ms,age);
  // Use a conservative lower bound, not an assumed exact clock offset.
  if (mapped!==null && (age-audioClockUncertainty>MAX_CAPTURE_AGE_MS || mapped+audioClockUncertainty<captureGateOpenedAt)) {
    dropCapture(message,"capture_age_limit",age); return;
  }
  if (socket.bufferedAmount > MAX_CAPTURE_BUFFERED_BYTES) {
    dropCapture(message,"websocket_backpressure",socket.bufferedAmount*1000/(SAMPLE_RATE*2)); return;
  }
  socket.send(framedCapture(message.frame, message.sequence, message.timestamp_ms, message.sample_rate, age));
  timing.capture_sent_ms+=message.frame.length*1000/(message.sample_rate||SAMPLE_RATE);
}

function connect() {
  if (closed || !mediaURL) return;
  const ws = new WebSocket(mediaURL);
  ws.binaryType = "arraybuffer";
  socket = ws;
  let openedAt = 0;
  let lastReceivedAt = Date.now();
  let lastPingAt = 0;
  let lastTickAt = monotonicEpochMS();
  // CONNECTING sockets and half-open TCP connections do not always emit close.
  // Run the heartbeat in the worker so a busy UI thread cannot fake an outage.
  healthTimer = setInterval(() => {
    if (closed || socket !== ws) return;
    const tick = monotonicEpochMS();
    timing.worker_max_tick_gap_ms=Math.max(timing.worker_max_tick_gap_ms,tick-lastTickAt);
    lastTickAt=tick;
    capturePort?.postMessage({type:"clock.probe",nonce:tick});
    stats();
    const now = Date.now();
    if (now - lastReceivedAt >= (openedAt ? 15000 : 10000)) {
      detach();
      ws.close();
      return;
    }
    if (ws.readyState === WebSocket.OPEN && now - lastPingAt >= 5000) {
      lastPingAt = now;
      ws.send(JSON.stringify({ type: "ping", nonce: -1 }));
      ws.send(JSON.stringify({ type: "media.clock", nonce:monotonicEpochMS() }));
    }
  }, 1000);
  ws.onopen = () => {
    if (socket !== ws || closed) { ws.close(); return; }
    openedAt = Date.now();
    lastReceivedAt = openedAt;
    framedV2=false; serverClockOffset=null; serverClockUncertainty=null; serverClockSampleAt=0; playbackExpected=null; playbackDeliveryBase=null; whisperEpoch=null; whisperDeliveryBase=null; playbackPort?.postMessage({type:"whisper.clear"});
    ws.send(JSON.stringify({type:"media.capabilities",version:2,versions:[3,2],whisper:true}));
    ws.send(JSON.stringify({type:"media.clock",nonce:monotonicEpochMS()}));
    postMessage({ type: "socket.open" });
  };
  ws.onmessage = (event) => {
    if (closed || socket !== ws) return;
    lastReceivedAt = Date.now();
    // A responsive transport is healthy even while the callee is ringing.
    // Handshakes alone must not reset the retry budget.
    if (openedAt && lastReceivedAt - openedAt >= 10000) { reconnectStartedAt = 0; reconnectAttempt = 0; }
    if (typeof event.data === "string") {
      try {
        const control=JSON.parse(event.data);
        if(control.type==="media.capabilities" && (control.version===2 || control.version===3)) framedV2=true;
        if(control.type==="coach.state") {
          whisperEpoch=control.talking && Number.isInteger(control.epoch) ? control.epoch : null;
          whisperDeliveryBase=null;
          resamplers.delete(\`whisper:8000:\${contextRate}\`);
          playbackPort?.postMessage({type:"whisper.clear"});
        }
        if(control.type==="media.clock") {
          const now=monotonicEpochMS(), rtt=now-control.nonce-(control.sent_ms-control.received_ms);
          if(Number.isFinite(rtt)&&rtt>=0&&rtt<1000) {
            serverClockOffset=((control.received_ms-control.nonce)+(control.sent_ms-now))/2;
            serverClockUncertainty=rtt/2; serverClockSampleAt=now;
          }
          return;
        }
      } catch { /* Other status frames are handled by the session. */ }
      postMessage({ type: "socket.message", data: event.data });
      return;
    }
    if (!(event.data instanceof ArrayBuffer)) return;
    let buffer=event.data, sourceAudioMS=null;
    const magic=buffer.byteLength>=4 ? new DataView(buffer).getUint32(0,true) : 0;
    if(magic===0x31575041) {
      if(buffer.byteLength<17 || buffer.byteLength>176 || whisperEpoch===null) return;
      const view=new DataView(buffer),epoch=view.getUint32(4,true),source=view.getFloat64(8,true);
      if(epoch!==whisperEpoch || !Number.isFinite(source)) return;
      const delta=monotonicEpochMS()-source;
      whisperDeliveryBase=whisperDeliveryBase===null ? delta : Math.min(whisperDeliveryBase,delta);
      const excess=Math.max(0,delta-whisperDeliveryBase);
      if(excess>200) return;
      const pcm=new Float32Array(buffer.byteLength-16);
      for(let i=0;i<pcm.length;i++) { const u=(~view.getUint8(16+i))&255;const value=(((u&15)<<3)+132)<<(u>>4&7);pcm[i]=((u&128)?132-value:value-132)/32768; }
      const frame=resample(pcm,8000,contextRate,"whisper:");
      const received=audioClockOffset===null ? null : monotonicEpochMS()-audioClockOffset-excess+audioClockUncertainty;
      playbackPort?.postMessage({type:"whisper",frame,received_audio_ms:received},[frame.buffer]);
      return;
    }
    if(buffer.byteLength%2!==0) return;
    let sequence=playbackSequence++;
    const header=magic===MAGIC_V3 ? 64 : 32;
    if(framedV2 && buffer.byteLength>=header && (magic===MAGIC_V2 || magic===MAGIC_V3)) {
      const view=new DataView(buffer);
      sequence=view.getUint32(4,true);
      if(playbackExpected!==null && sequence>playbackExpected) timing.playback_sequence_gaps+=sequence-playbackExpected;
      playbackExpected=(sequence+1)>>>0;
      const sent=view.getFloat64(16,true), queued=view.getFloat64(24,true);
      const source=magic===MAGIC_V3 ? view.getFloat64(48,true) : null;
      if(magic===MAGIC_V3) {
        timing.playback_source_timestamp_ms=view.getFloat64(32,true);
        timing.playback_source_sequence=Number(view.getBigUint64(40,true));
        timing.playback_source_epoch=view.getUint32(56,true);
      }
      if(Number.isFinite(queued)) timing.playback_max_server_queue_ms=Math.max(timing.playback_max_server_queue_ms,queued);
      // Relative clocks need no synchronized offset to detect GROWING delay.
      // When clock probes cannot cross an undersized link, still reject audio
      // over 320ms behind its fastest delivery in this socket epoch. This does
      // not estimate the unknown fixed latency of its very first packet.
      const ageClock=source!==null && Number.isFinite(source) ? source : sent;
      let deliveryExcess=0;
      if(Number.isFinite(ageClock) && ageClock>0) {
        const delta=monotonicEpochMS()-ageClock;
        playbackDeliveryBase=playbackDeliveryBase===null ? delta : Math.min(playbackDeliveryBase,delta);
        deliveryExcess=Math.max(0,delta-playbackDeliveryBase);
        timing.playback_max_delivery_excess_ms=Math.max(timing.playback_max_delivery_excess_ms,deliveryExcess);
        if(audioClockOffset!==null) sourceAudioMS=monotonicEpochMS()-audioClockOffset-deliveryExcess+audioClockUncertainty;
      }
      if(deliveryExcess>320) {
        const duration=(buffer.byteLength-header)*1000/(SAMPLE_RATE*2);
        timing.playback_transport_dropped_ms+=duration;
        timing.drop_totals_ms.playback_delivery_excess=(timing.drop_totals_ms.playback_delivery_excess||0)+duration;
        resamplers.delete(\`\${SAMPLE_RATE}:\${contextRate}\`);return;
      }
      if(serverClockOffset!==null && monotonicEpochMS()-serverClockSampleAt<15000 && Number.isFinite(sent)) {
        const transit=Math.max(0,monotonicEpochMS()+serverClockOffset-sent);
        timing.playback_max_transit_ms=Math.max(timing.playback_max_transit_ms,transit);
        // Use a single source-to-playback excess-age budget across stages.
        // Source mapping is relative to fastest carrier delivery, not UTC.
        const sourceAge=source!==null && Number.isFinite(source) ? Math.max(0,monotonicEpochMS()+serverClockOffset-source) : null;
        if(sourceAge!==null) {
          timing.playback_max_source_age_ms=Math.max(timing.playback_max_source_age_ms,sourceAge);
          if(audioClockOffset!==null) sourceAudioMS=Math.min(sourceAudioMS ?? Infinity,source-serverClockOffset-audioClockOffset+serverClockUncertainty+audioClockUncertainty);
        }
        const sourceStale=sourceAge!==null && sourceAge-serverClockUncertainty>320;
        if(sourceStale || transit-serverClockUncertainty>320) {
          const duration=(buffer.byteLength-header)*1000/(SAMPLE_RATE*2);
          const reason=sourceStale ? "playback_source_age" : "playback_transport_age";
          if(sourceStale) timing.playback_source_dropped_ms+=duration; else timing.playback_transport_dropped_ms+=duration;
          timing.drop_totals_ms[reason]=(timing.drop_totals_ms[reason]||0)+duration;
          resamplers.delete(\`\${SAMPLE_RATE}:\${contextRate}\`);
          return;
        }
      }
      buffer=buffer.slice(header);
    }
    timing.playback_received_ms+=buffer.byteLength*1000/(SAMPLE_RATE*2);
    const frame = resample(decodePlayback(buffer), SAMPLE_RATE, contextRate);
    playbackPort?.postMessage({ type: "playback", frame, sequence, timestamp_ms: performance.now(), received_audio_ms:sourceAudioMS ?? (audioClockOffset===null ? null : monotonicEpochMS()-audioClockOffset), clock_uncertainty_ms:audioClockUncertainty }, [frame.buffer]);
  };
  ws.onerror = () => postMessage({ type: "socket.error" });
  function detach() {
    if (socket !== ws) return;
    if (healthTimer !== null) clearInterval(healthTimer);
    healthTimer = null;
    socket = null;
    microphoneReady = false;
    resamplers.clear();
    playbackPort?.postMessage({ type: "flush" });
    postMessage({ type: "socket.close" });
    scheduleReconnect();
  }
  ws.onclose = detach;
}

function scheduleReconnect() {
  if (closed || reconnectTimer !== null) return;
  const now = Date.now();
  if (reconnectStartedAt === 0) reconnectStartedAt = now;
  if (now - reconnectStartedAt >= MAX_RECONNECT_MS) {
    postMessage({ type: "socket.failed", detail: "audio connection lost after repeated retries" });
    return;
  }
  const delay = Math.min(250 * (2 ** reconnectAttempt), MAX_RECONNECT_DELAY_MS);
  reconnectAttempt += 1;
  reconnectTimer = setTimeout(() => { reconnectTimer = null; connect(); }, delay);
}

self.onmessage = (event) => {
  const message = event.data;
  switch (message?.type) {
    case "init":
      muted = Boolean(message.muted);
      mediaURL = message.mediaURL;
      contextRate = message.contextRate || SAMPLE_RATE;
      if(Number.isFinite(message.audioClockMS)&&Number.isFinite(message.monotonicEpochMS)) { audioClockOffset=message.monotonicEpochMS-message.audioClockMS; audioClockUncertainty=20; }
      capturePort = message.capturePort;
      playbackPort = message.playbackPort;
      capturePort.onmessage = (captureEvent) => capture(captureEvent.data);
      capturePort.start?.();
      playbackPort.start?.();
      capturePort.postMessage({type:"clock.probe",nonce:monotonicEpochMS()});
      connect();
      break;
    case "muted":
      muted = Boolean(message.value);
      captureGateOpenedAt=monotonicEpochMS();
      resamplers.clear();
      break;
    case "microphone.ready":
      if (!message.value) resamplers.clear();
      microphoneReady = Boolean(message.value);
      captureGateOpenedAt=monotonicEpochMS();
      break;
    case "send.text":
      if (socket?.readyState === WebSocket.OPEN) {
        socket.send(message.data);
        stats();
      }
      break;
    case "flush":
      playbackPort?.postMessage({ type: "flush" });
      break;
    case "close":
      closed = true;
      if (healthTimer !== null) clearInterval(healthTimer);
      healthTimer = null;
      if (reconnectTimer !== null) clearTimeout(reconnectTimer);
      reconnectTimer = null;
      socket?.close();
      socket = null;
      capturePort?.close();
      playbackPort?.close();
      close();
      break;
  }
};
`;function v(t=!1){let e=(t?[F]:[F,it]).map((s)=>URL.createObjectURL(new Blob([s],{type:"text/javascript"})));return{urls:e,dispose(){e.splice(0).forEach((s)=>URL.revokeObjectURL(s))}}}async function mt(t,e=!1){if(typeof location>"u")return v(e);let s=new URL(t.mcpURL(),location.href);if(s.origin!==location.origin)return v(e);if(t.name!=="telephony"||!t.projectId||!Number.isSafeInteger(t.installId)||t.installId<=0)throw Error("Telephony audio requires a project and installation");return{urls:await Promise.all((e?[F]:[F,it]).map(async(i,m)=>{let u=new TextEncoder().encode(i),r=Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",u)),(l)=>l.toString(16).padStart(2,"0")).join(""),c=`/ui/frontend/${m===0?"worklet":"worker"}-${r}.js`;if(await t.get(c,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==i)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let p=new URL(`/api/apps/telephony/_install/${t.installId}${c}`,s);return p.searchParams.set("project_id",t.projectId),p.searchParams.set("install_id",String(t.installId)),p.href})),dispose(){}}}async function At(){return(await navigator.mediaDevices.enumerateDevices()).filter((e)=>e.kind==="audioinput").map((e,s)=>({deviceId:e.deviceId,label:e.label||`Microphone ${s+1}`}))}function gt(t,e){let s=new ht(t,!1),h,a=!1,i,m=()=>{return a=!0,i??=(async()=>{try{await s.cancel()}finally{h?.dispose(),h=void 0}})()};return{async start(u={}){if(a)throw Error("Microphone preview is already used");a=!0;try{if(h=e?await mt(e,!0):v(!0),i)throw h.dispose(),h=void 0,Error("Microphone preview was cancelled");await s.start(h.urls[0],{...U,...u})}catch(r){throw await m(),r}},stop:m}}function qt(t){return{async preflight(e){(await navigator.mediaDevices.getUserMedia({audio:L(e)})).getTracks().forEach((h)=>h.stop())},create(e){let s=new at(e),h,a=!1,i=()=>{a=!0;try{s.stop()}finally{h?.dispose(),h=void 0}};return{async start(m,u){try{if(h=t?await mt(t):v(),a)throw Error("Audio session was cancelled");await s.start(m,h.urls[0],h.urls[1],u)}catch(r){throw i(),r}},stop:i,setMuted:(m)=>s.setMuted(m),sendDTMF:(m)=>s.sendDTMF(m),setOutputVolume:(m)=>s.setOutputVolume(m),startRingback:(m)=>s.startRingback(m),stopRingback:()=>s.stopRingback()}}}}var Z=`// Passive receiver only. Both directions share one rendering clock and delay.
class TelephonyListenerProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    this.stereo = options.processorOptions?.stereo === true;
    this.queues = [[], []];
    this.dropped = [0, 0];
    this.played = [0, 0];
    this.maxQueueMS = 0;
    this.maxLateMS = 0;
    this.reportSamples = 0;
    this.port.onmessage = ({ data }) => {
      const d = data?.direction;
      if ((d !== 0 && d !== 1) || !(data.frame instanceof Float32Array) || !Number.isFinite(data.playAtMS)) return;
      const now = currentTime * 1000;
      const duration = data.frame.length * 1000 / sampleRate;
      // Bound future scheduling and stale audio independently of queue length.
      if (data.playAtMS + duration < now || data.playAtMS > now + 200) {
        this.dropped[d] += data.frame.length; return;
      }
      const queue = this.queues[d];
      let start = Math.round(data.playAtMS * sampleRate / 1000);
      const previous = queue[queue.length - 1];
      // Buffered carriers can transmit several adjacent packets together.
      // Preserve their sample order rather than overlapping arrival timestamps.
      if (previous) start = Math.max(start, previous.start + previous.frame.length);
      queue.push({ frame: data.frame, start });
      let trimmed = false;
      while (queue.length > 8) {
        const old = queue.shift(); this.dropped[d] += old.frame.length - (old.played || 0); trimmed = true;
      }
      const last = queue[queue.length - 1];
      if (trimmed || (last.start + last.frame.length) * 1000 / sampleRate > now + 220) {
        // Rebase only this listener's backlog. Keep a small cushion and a hard
        // upper bound instead of letting repeated catch-up bursts build delay.
        let next = Math.round((now + 40) * sampleRate / 1000);
        for (const item of queue) {
          if (item.played) { item.frame = item.frame.subarray(item.played); item.played = 0; }
          item.start = next; next += item.frame.length;
        }
      }
      this.maxQueueMS = Math.max(this.maxQueueMS, queue.reduce((sum, x) => sum + x.frame.length, 0) * 1000 / sampleRate);
    };
  }
  process(_inputs, outputs) {
    const out = outputs[0];
    if (!out?.[0]) return true;
    out.forEach(channel => channel.fill(0));
    const start = Math.round(currentTime * sampleRate);
    for (let d = 0; d < 2; d++) {
      const queue = this.queues[d];
      for (let i = 0; i < out[0].length; i++) {
        const now = start + i;
        while (queue.length && queue[0].start + queue[0].frame.length <= now) {
          const old = queue.shift();
          this.dropped[d] += Math.max(0, old.frame.length - (old.played || 0));
        }
        const item = queue[0];
        if (!item || now < item.start) continue;
        const offset = now - item.start;
        if (!item.played) {
          this.dropped[d] += offset;
          this.maxLateMS = Math.max(this.maxLateMS, offset * 1000 / sampleRate);
        }
        item.played = offset + 1;
        this.played[d]++;
        const channel = this.stereo ? out[d] : out[0];
        if (channel) channel[i] += item.frame[offset] * (this.stereo ? 1 : 0.5);
      }
    }
    this.reportSamples += out[0].length;
    if (this.reportSamples >= sampleRate) {
      this.reportSamples = 0;
      this.port.postMessage({ type: "listener.diagnostics", dropped_ms: this.dropped.map(n => n * 1000 / sampleRate), played_ms: this.played.map(n => n * 1000 / sampleRate), max_queue_ms: this.maxQueueMS, max_late_ms: this.maxLateMS });
    }
    return true;
  }
}
registerProcessor("telephony-listener", TelephonyListenerProcessor);
`;function Nt(t){if(t.byteLength<26||t.byteLength>984||t.byteLength%2)throw Error("Invalid listener audio frame");let e=new DataView(t),s=e.getUint32(4,!0);if(e.getUint32(0,!0)!==827085889||s>1)throw Error("Invalid listener audio protocol");let h=Number(e.getBigUint64(8,!0)),a=Number(e.getBigUint64(16,!0));if(!Number.isSafeInteger(h)||!Number.isSafeInteger(a))throw Error("Invalid listener audio timing");let i=new Float32Array((t.byteLength-24)/2);for(let m=0;m<i.length;m++)i[m]=e.getInt16(24+m*2,!0)/32768;return{direction:s,sequence:h,timestampMS:a,frame:i}}function Gt(t,e,s,h){if(t.length<1||t.length>480||!Number.isFinite(h)||h<0||!Number.isInteger(e)||e<1||e>4294967295)throw Error("Invalid coaching capture");let a=new ArrayBuffer(24+t.length*2),i=new DataView(a);i.setUint32(0,826496833,!0),i.setUint32(4,e,!0),i.setUint32(8,s>>>0,!0),i.setFloat64(16,h,!0);for(let m=0;m<t.length;m++){let u=Math.max(-1,Math.min(1,t[m]));i.setInt16(24+m*2,u<0?u*32768:u*32767,!0)}return a}function xt(t,e={}){return{create(s){let h,a,i,m,u=!1,r=[],c=!1,y=0,p=!1,l=!1,_,f,S,d,E,H,N,ct=!1,ft,z,j=0,D=!1,lt=[0,0],_t=[0,0],G=[void 0,void 0],Pt=[new X,new X],nt=e.outputVolume??1,I,Q=()=>{let M=++y;if(p=!1,l=!1,N?.(M,Error("Coaching cancelled")),H)clearInterval(H);if(H=void 0,E?.port1.close(),E?.port2.close(),E=void 0,f?.disconnect(),f=void 0,S?.disconnect(),S=void 0,d?.disconnect(),d=void 0,_?.getTracks().forEach((g)=>{g.onended=null,g.stop()}),_=void 0,c&&D&&m?.readyState===WebSocket.OPEN)m.send(JSON.stringify({type:"coach.stop",generation:M}));s.onTalking?.(!1)},yt=()=>Q(),Mt=()=>{if(document.visibilityState!=="visible")Q()},tt=()=>{if(u)return;if(Q(),u=!0,I?.(),window.removeEventListener("blur",yt),document.removeEventListener("visibilitychange",Mt),m)m.onmessage=m.onclose=m.onerror=null,m.close();if(a?.disconnect(),i?.disconnect(),h)h.onstatechange=null,h.close().catch(()=>{});for(let M of r)URL.revokeObjectURL(M)},V=(M)=>{if(!u)tt(),s.onClose(M)},B=()=>{if(u)throw Error("Listening cancelled")};return{async start(M,g){c=g?.coaching===!0,B();try{if(h=new AudioContext({latencyHint:"interactive"}),h.state==="suspended")await h.resume();if(B(),e.outputDeviceId&&"setSinkId"in h)await h.setSinkId(e.outputDeviceId);B();let x,J=new URL(t.mcpURL(),location.href);if(J.origin===location.origin){let n=`/ui/frontend/listener-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(Z))),(A)=>A.toString(16).padStart(2,"0")).join("")}.js`,w=await t.get(n,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}});if(B(),w!==Z)throw Error("Listener audio asset integrity mismatch");let T=new URL(`/api/apps/telephony/_install/${t.installId}${n}`,J);T.searchParams.set("project_id",t.projectId),T.searchParams.set("install_id",String(t.installId)),x=T.href}else{let P=URL.createObjectURL(new Blob([Z],{type:"text/javascript"}));r.push(P),x=P}if(await h.audioWorklet.addModule(x),B(),a=new AudioWorkletNode(h,"telephony-listener",{numberOfInputs:0,outputChannelCount:[e.stereo?2:1],processorOptions:{stereo:e.stereo}}),i=h.createGain(),i.gain.value=nt,a.connect(i).connect(h.destination),a.onprocessorerror=()=>V("listener_audio_error"),a.port.onmessage=({data:P})=>{if(P?.type==="listener.diagnostics"&&!u)try{s.onDiagnostics?.({...P,sequence_gaps:[..._t],network_excess_ms:j,network_dropped_ms:[...lt]})}catch{}},h.onstatechange=()=>{if(h?.state==="suspended"||h?.state==="interrupted")V("listener_audio_paused")},await new Promise((P,n)=>{let w=!1,T=setTimeout(()=>{A(Error("Listener connection timed out")),V("listener_network_error")},1e4),A=(b)=>{if(w)return;w=!0,clearTimeout(T),I=void 0,b?n(b):P()};I=()=>A(Error("Listening cancelled")),m=new WebSocket(M),m.binaryType="arraybuffer",m.onmessage=({data:b})=>{if(u||!h||!a)return;try{if(typeof b==="string"){let O=JSON.parse(b);if(O.type==="listener.ready"){if(c&&O.coaching!==!0)throw Error("Coaching not authorized");D=!0,A(),s.onReady()}else if(O.type==="coach.started")N?.(O.generation);else if(O.type==="coach.rejected")N?.(y,Error(O.detail||"Coaching unavailable"));else if(O.type==="coach.stopped"&&(O.generation===void 0||O.generation===y))Q();return}if(!D||!(b instanceof ArrayBuffer))return;let k=Nt(b),q=k.direction;if(G[q]!==void 0&&k.sequence<G[q])return;if(G[q]!==void 0&&k.sequence>G[q])_t[q]+=k.sequence-G[q];G[q]=k.sequence+1;let et=performance.now()-k.timestampMS;if(z=Math.min(z??et,et),j=Math.max(0,et-z),j>200){lt[q]+=k.frame.length*1000/24000;return}ft??=h.currentTime*1000+60-k.timestampMS;let ot=Pt[q].process(k.frame,24000,h.sampleRate);a.port.postMessage({direction:q,frame:ot,playAtMS:k.timestampMS+ft},[ot.buffer])}catch{A(Error("Invalid listener media")),V("listener_protocol_error")}},m.onerror=()=>{A(Error("Listener connection failed")),V("listener_network_error")},m.onclose=(b)=>{A(Error(b.reason||"Listener disconnected")),V(b.reason||"listener_disconnected")}}),B(),c)window.addEventListener("blur",yt),document.addEventListener("visibilitychange",Mt)}catch(x){throw tt(),x}},async startTalking(){if(B(),!c||!D||!h||!m||m.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(p)return;p=!0;let M=++y,g=()=>!u&&p&&M===y;try{let x=await navigator.mediaDevices.getUserMedia({audio:{echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,...e.inputDeviceId?{deviceId:{exact:e.inputDeviceId}}:{}}});if(!g()){x.getTracks().forEach((n)=>n.stop());return}if(_=x,_.getAudioTracks().forEach((n)=>{n.onended=()=>Q()}),!ct){let n=new URL(t.mcpURL(),location.href),w;if(n.origin===location.origin){let A=`/ui/frontend/worklet-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(F))),(q)=>q.toString(16).padStart(2,"0")).join("")}.js`;if(await t.get(A,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==F)throw Error("Coaching audio asset integrity mismatch");let k=new URL(`/api/apps/telephony/_install/${t.installId}${A}`,n);k.searchParams.set("project_id",t.projectId),k.searchParams.set("install_id",String(t.installId)),w=k.href}else w=URL.createObjectURL(new Blob([F],{type:"text/javascript"})),r.push(w);if(!g())return;await h.audioWorklet.addModule(w),ct=!0}if(!g())return;if(await new Promise((n,w)=>{let T=setTimeout(()=>{N=void 0,w(Error("Coaching activation timed out"))},3000);N=(A,b)=>{if(b||A===M)clearTimeout(T),N=void 0,b?w(b):n()},m.send(JSON.stringify({type:"coach.start",generation:M}))}),!g())return;f=new AudioWorkletNode(h,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:0,highpassFilter:!0}}),S=h.createMediaStreamSource(x),d=h.createGain(),d.gain.value=0,S.connect(f).connect(d).connect(h.destination),E=new MessageChannel;let J=0,P=new X;E.port1.onmessage=({data:n})=>{if(!g()||!l||n?.type!=="capture"||!(n.frame instanceof Float32Array)||!h||!m)return;if(!Number.isFinite(n.timestamp_ms)||h.currentTime*1000-n.timestamp_ms>150||m.bufferedAmount>2880||m.readyState!==WebSocket.OPEN)return;let w=P.process(n.frame,n.sample_rate,24000);if(w.length>0&&w.length<=480)m.send(Gt(w,M,J++,n.timestamp_ms))},E.port1.start(),f.port.postMessage({type:"transport",port:E.port2},[E.port2]),l=!0,s.onTalking?.(!0),H=setInterval(()=>{if(g()&&m?.readyState===WebSocket.OPEN)m.send(JSON.stringify({type:"coach.keepalive",generation:M}))},1000)}catch(x){if(g())throw Q(),x}},stopTalking:Q,stop:tt,setOutputVolume(M){if(!Number.isFinite(M)||M<0||M>1)throw RangeError("Listener volume must be 0–1");if(nt=M,i)i.gain.value=M}}}}}class ut{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;constructor(t,e={}){this.client=t;this.options=e;this.runtime=e.runtime??xt(t.app,e)}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.observers.add(t),()=>{this.observers.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let e of this.observers)try{e(this.snapshot)}catch{}}async listen(t){return this.begin(t,!1)}async coach(t){return this.begin(t,!0)}async begin(t,e){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=e,this.retryUntil=0,this.retryDelay=500;let s=++this.generation;return this.update({state:"connecting",callId:t,detail:void 0,coaching:e,talking:!1}),this.connect(t,s)}async connect(t,e){let s,h=++this.attempt,a=()=>e===this.generation&&h===this.attempt&&!this.disposed;try{if(s=this.coaching?await this.client.coachSession(t):await this.client.listenSession(t),!a()){await this.client.stopListening(s).catch(()=>{});return}this.session=s;let i=this.runtime.create({onReady:()=>{if(a())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0})},onClose:(u)=>{if(a())this.disconnected(u,t,e)},onTalking:(u,r)=>{if(a())this.update({talking:u,detail:r})},onDiagnostics:(u)=>{if(a())try{this.options.onDiagnostics?.(u)}catch{}}});this.audio=i;let m=!1;if(this.lease=setInterval(()=>{if(!a()||m||this.session!==s)return;m=!0,this.client.renewListening(s).catch(()=>{if(a())this.disconnected("access_revoked",t,e)}).finally(()=>{m=!1})},20000),await i.start(this.client.listenerMediaURL(s),{coaching:s.coaching===!0}),!a())i.stop()}catch(i){if(!a()||s&&this.session!==s)return;let m=i?.status,u;try{u=JSON.parse(i.body??"{}").code}catch{}let r=u==="call_ended"?"call_ended":m===401||m===403||m===404?"access_revoked":"listener_disconnected";throw this.disconnected(r,t,e,String(i)),i}}cleanup(){if(this.lease)clearInterval(this.lease);this.lease=void 0;let t=this.audio;this.audio=void 0,t?.stop();let e=this.session;return this.session=void 0,e?this.client.stopListening(e).catch(()=>{}):Promise.resolve()}disconnected(t,e,s,h=t){if(s!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let a=t==="call_ended",i=t==="access_revoked",m=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(t);if(!this.coaching&&!a&&!i&&m&&this.options.reconnect!==!1){if(this.retryUntil||=Date.now()+30000,Date.now()<this.retryUntil){this.update({state:"reconnecting",detail:h,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,s===this.generation&&!this.disposed)this.connect(e,s).catch(()=>{})},this.retryDelay),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:a?"call_ended":i?"access_revoked":"disconnected",detail:h,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(t){this.audio?.setOutputVolume(t)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let t=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1}),await t}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}function Vt(t){if(!t)return"placing";if(pt(t))return"ended";if(t==="ringing")return"ringing";if(t==="answered"||t==="in-progress")return"connected";return"placing"}var R=(t)=>t instanceof Error?t.message:String(t);class rt{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;leaseTimer;leaseGeneration=0;timer;polling;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(t,e={}){this.client=t;this.options=e;if(this.runtime=e.audioRuntime??qt(t.app),this.audioOptions={...U,...e.audio},$(this.audioOptions),this.interval=e.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.assertOpen(),this.listeners.add(t),()=>{this.listeners.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let e of this.listeners)try{e(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(t){if(this.disposed||t!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(t){return!this.disposed&&t===this.generation}finish(t){if(this.current(t))this.update({busy:!1})}cancellable(t,e){let s=this.cancellation.signal;return new Promise((h,a)=>{let i=()=>{s.removeEventListener("abort",i),a(Error("Softphone operation cancelled"))};if(s.addEventListener("abort",i,{once:!0}),t.then(h,a).finally(()=>s.removeEventListener("abort",i)),!this.current(e)||s.aborted)i()})}begin(t){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||t&&this.session)throw Error("Softphone already has an active operation or call");let e=this.invalidate();return this.update({busy:!0,detail:void 0}),e}async dial(t){let e=this.begin(!0),s,h=!1;try{this.assertCurrent(e);let a={to:t.to.trim(),from:t.from?.trim(),timeout_sec:t.timeout_sec,recording:t.recording},i=JSON.stringify(a);if(!this.intent||this.intent.value!==i||t.idempotency_key&&t.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:i,request:{...a,idempotency_key:t.idempotency_key||crypto.randomUUID()}};return await this.cancellable(this.runtime.preflight(this.audioOptions),e),this.assertCurrent(e),s=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(e),h=!0,this.outbound=!0,await this.attachAudio(s,e),s.call_id}catch(a){if(s&&(this.current(e)||!h||this.disposed))await this.recoverStartup(s,e,()=>this.client.hangup(s.call_id));if(this.current(e))this.update({detail:R(a)});throw a}finally{this.finish(e)}}async answer(t,e={}){let s=this.begin(!0),h,a=!1;try{this.assertCurrent(s),h=await this.client.answer(t,e),this.assertCurrent(s),a=!0,this.outbound=!1,await this.attachAudio(h,s)}catch(i){if(h&&(this.current(s)||!a||this.disposed))await this.recoverStartup(h,s,()=>this.client.release(h));if(this.current(s))this.update({detail:R(i)});throw i}finally{this.finish(s)}}async recoverStartup(t,e,s){try{if(await s(),this.current(e))this.clearCall()}catch{if(this.current(e))this.session=t,this.update({callId:t.call_id,audioState:"error"}),this.startPolling()}}async join(t){await this.answer(t,{rejoin:!0})}async attach(t){await this.acquireSession(t,!1)}async takeover(t){await this.acquireSession(t,!0)}async acquireSession(t,e){let s=this.begin(!0);try{await this.cancellable(this.runtime.preflight(this.audioOptions),s),this.assertCurrent(s);let h=await(e?this.client.takeover(t):this.client.attach(t));this.assertCurrent(s),await this.attachAudio(h,s),await this.reconcileAttachedCall(h,s)}catch(h){if(this.current(s))this.update({detail:R(h)});throw h}finally{this.finish(s)}}async reconnect(t){if(!this.session)throw Error("No call to reconnect");let e={...this.audioOptions,...t};$(e);let s=this.begin(!1);this.audioOptions=e;try{await this.attachAudio(this.session,s)}catch(h){if(this.current(s))this.update({detail:R(h)});throw h}finally{this.finish(s)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let t=this.session;if(!t){this.cancellation.abort();return}let e=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let s=(async()=>{try{if(await this.client.hangup(t.call_id),this.current(e))this.clearCall()}catch(h){if(this.current(e))this.update({audioState:"error",detail:R(h)}),this.startPolling();throw h}finally{this.finish(e)}})();this.hangingUp=s;try{await s}finally{if(this.hangingUp===s)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(t){this.assertOpen();let e=this.session;if(!e)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let s=this.generation;this.update({busy:!0,detail:void 0});let h;try{h=this.client[t](e.call_id)}catch(a){if(this.current(s)&&this.session===e)this.update({busy:!1,detail:R(a)});throw a}this.controlPending=h;try{let a=await h;if(this.current(s)&&this.session===e)this.update({holdState:a.hold_state,recordingState:a.recording_state,controlError:a.control_error,capabilities:a.capabilities});return a}catch(a){if(this.current(s)&&this.session===e)this.update({detail:R(a)});throw a}finally{if(this.controlPending===h)this.controlPending=void 0;if(this.current(s)&&this.session===e)this.update({busy:!1})}}configureAudio(t){this.assertOpen();let e={...this.audioOptions,...t};$(e),this.audioOptions=e}setMuted(t){this.assertOpen(),this.audio?.setMuted(t),this.update({muted:t})}setOutputVolume(t){if(this.assertOpen(),!Number.isFinite(t)||t<0||t>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=t,this.audio?.setOutputVolume(t)}sendDTMF(t){if(this.assertOpen(),!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(t)}observeCall(t){if(this.disposed||t.id!==this.session?.call_id)return;if(t.direction)this.outbound=t.direction==="outbound";if(pt(t.status)){this.invalidate(),this.clearCall(t.status),this.update({busy:!1,phase:"ended",termination:t.termination,answeredBy:t.answered_by??this.snapshot.answeredBy,endedAt:t.ended_at});return}this.update({carrierStatus:t.status,phase:Vt(t.status),answeredBy:t.answered_by??this.snapshot.answeredBy,holdState:t.hold_state??this.snapshot.holdState,recordingState:t.recording_state??this.snapshot.recordingState,controlError:t.control_error??this.snapshot.controlError,capabilities:t.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let t=this.options.ringback;return typeof t==="object"&&t?t.country:void 0}syncRingback(){let t=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(t&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!t&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(t,e){this.assertCurrent(e),this.stopAudio(),this.session=t,this.update({callId:t.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let s,h=()=>!this.disposed&&s!==void 0&&this.audio===s,a=(i)=>{if(h())try{i()}catch{}};try{if(this.assertCurrent(e),s=this.runtime.create({onState:(i,m)=>{if(!h())return;if(i==="ended"&&m==="call.ended"){this.observeCall({id:t.call_id,status:"completed"});return}if(this.update({audioState:i,detail:m}),h()&&(i==="error"||i==="ended")){if(this.stopAudio(),i==="error")this.reconcileFailedAudio(t,e)}},onLevels:(i,m)=>a(()=>this.options.onLevels?.(i,m)),onDiagnostics:(i)=>a(()=>this.options.onDiagnostics?.(i)),onNotice:(i)=>a(()=>this.options.onNotice?.(i)),onCallStatus:(i)=>a(()=>this.observeCall({id:i.call_id,status:i.status,direction:i.direction,answered_at:i.answered_at,ended_at:i.ended_at,answered_by:i.answered_by,termination:i.termination,hold_state:i.hold_state,recording_state:i.recording_state,control_error:i.control_error}))}),this.audio=s,this.assertCurrent(e),this.startPolling(),this.startLease(t),s.setMuted(this.snapshot.muted),await this.cancellable(s.start(this.client.mediaURL(t),this.audioOptions),e),this.assertCurrent(e),!h())throw Error(this.snapshot.detail||"Audio connection ended during setup");s.setMuted(this.snapshot.muted),this.syncRingback()}catch(i){if(h())this.stopAudio();if(this.current(e))this.update({audioState:"error",detail:R(i)});throw i}}async reconcileAttachedCall(t,e){try{let s=await this.client.getCall(t.call_id);if(this.current(e)&&this.session===t&&s)this.observeCall(s)}catch{}}async reconcileFailedAudio(t,e){try{let s=await this.client.getCall(t.call_id);if(!this.current(e)||this.session!==t||!s)return;if(s.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(s)}catch{}}startLease(t){let e=++this.leaseGeneration;if(!t.lease_seconds)return;let s=async()=>{if(this.disposed||this.session!==t||e!==this.leaseGeneration)return;try{if(await this.client.renew(t),e===this.leaseGeneration)this.leaseTimer=setTimeout(s,t.lease_seconds*1000/3)}catch(h){if(e!==this.leaseGeneration)return;this.stopAudio(),this.update({audioState:"error",detail:`Audio authorization ended: ${R(h)}`})}};this.leaseTimer=setTimeout(s,t.lease_seconds*1000/3)}stopAudio(){++this.leaseGeneration,clearTimeout(this.leaseTimer),this.leaseTimer=void 0;let t=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{t?.stopRingback?.()}catch{}}try{t?.stop()}catch{}if(t)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let t=new AbortController;this.polling=t;let e=async()=>{let s=this.session?.call_id;if(!s||t.signal.aborted)return;try{let h=await this.client.getCall(s,t.signal);if(!t.signal.aborted&&h)this.observeCall(h)}catch(h){if(!t.signal.aborted)this.update({detail:`Call status unavailable: ${R(h)}`})}finally{if(!t.signal.aborted&&this.session&&!this.disposed)this.timer=setTimeout(e,this.interval)}};this.timer=setTimeout(e,this.interval)}clearCall(t){this.stopAudio(),this.stopPolling(),this.session=void 0,this.outbound=!1,this.update({callId:void 0,carrierStatus:t,audioState:"idle",muted:!1,detail:void 0,phase:t?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function Wt(t,e="en"){if(t?.reason==="time_limit")return e.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return t?.reason?.replaceAll("_"," ")??""}class Et extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function Ut(t){return t.direction==="inbound"&&t.status==="pending"&&t.answerable!==!1&&!t.routing_waiting&&(t.peer_kind==="human"||Boolean(t.ring_offers?.some((e)=>e.kind==="browser")))}var pt=(t)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(t);function o(t){if(!t||/[\s/\\?#]/.test(t))throw Error("Invalid call ID");return encodeURIComponent(t)}class K{app;options;listMicrophones=At;createMicrophonePreview=(t)=>gt(t,this.app);constructor(t,e={}){this.app=t;this.options=e;if(t.name!=="telephony"||!t.projectId||!t.installId)throw Error("Telephony requires an explicit project and installation")}path(t){if(!this.options.authProvider)return t;return`/user${t}${t.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(t){let e=await this.app.get(this.path("/calls"),{signal:t});if(!Array.isArray(e?.calls)||e.calls.some((s)=>!s||typeof s.id!=="string"||typeof s.status!=="string"))throw Error("Invalid Telephony calls response");return e.calls}async getCall(t,e){let s=await this.app.get(this.path(`/calls?call_id=${o(t)}`),{signal:e});if(!Array.isArray(s?.calls))throw Error("Invalid Telephony call response");return s.calls.find((h)=>h.id===t)}incomingCalls(t){return t.filter(Ut)}watchCalls(t,e={}){let s=e.intervalMs??2000;if(!Number.isFinite(s)||s<100)throw Error("Call watch interval must be at least 100 ms");let h=new AbortController,a,i,m=!1,u=!1,r=new Set,c=()=>{clearTimeout(a),h.abort(),i?.close(),e.signal?.removeEventListener("abort",c)},y=(l)=>{try{e.onError?.(l)}catch{c()}};if(e.signal?.addEventListener("abort",c,{once:!0}),e.signal?.aborted)c();let p=async(l)=>{if(h.signal.aborted)return;if(m){u=!0;return}clearTimeout(a),m=!0;let _=performance.now();try{let f=await this.listCalls(h.signal);if(!h.signal.aborted){t(f);for(let S of f){if(S.answerable!==!0||S.status!=="pending")continue;for(let d of S.ring_offers??[]){if(d.kind!=="browser"||!d.id||r.has(d.id))continue;r.add(d.id),this.acknowledgeOffer(S.id,d.id).catch((E)=>{if(![404,405].includes(E?.status??0))r.delete(d.id)})}}try{e.onTiming?.({trigger:l,fetchMs:performance.now()-_})}catch{}}}catch(f){if(!h.signal.aborted){let S=f?.status;try{e.onFailure?.({trigger:l,fetchMs:performance.now()-_,status:typeof S==="number"?S:void 0,error:f})}catch{}y(f)}}finally{if(m=!1,!h.signal.aborted)if(u)u=!1,p("push");else a=setTimeout(()=>void p("poll"),s)}};if(p("poll"),!h.signal.aborted&&e.push!==!1&&typeof this.app.subscribe==="function")try{i=this.app.subscribe(this.path("/calls/events"),(l)=>{if(l.type==="calls.changed")p("push");else if(l.type==="access.revoked")i?.close(),p("push")},{transport:"fetch",signal:h.signal,reconnectDelayMs:250,onError:(l)=>{let _=l?.status;if(_===404||_===405||_===501)i?.close()}})}catch{}return{close:c}}async place(t){if(!/^\+[1-9]\d{7,14}$/.test(t.to)||!t.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");return this.session(await this.app.post(this.path("/softphone/place"),t))}async answer(t,e={}){try{return this.session(await this.app.post(this.path(`/softphone/answer/${o(t)}`),e),t)}catch(s){let h=s;if(h.status===409&&typeof h.body==="string"){let a;try{a=JSON.parse(h.body)}catch{}if(a?.code==="offer_expired")throw new Et}throw s}}async acknowledgeOffer(t,e){await this.app.post(this.path(`/softphone/offer/ack/${o(t)}`),{offer_id:e})}async declineOffer(t,e){await this.app.post(this.path(`/softphone/offer/decline/${o(t)}`),{offer_id:e})}async attach(t){return this.session(await this.app.post(this.path(`/softphone/attach/${o(t)}`),{}),t)}async takeover(t){return this.session(await this.app.post(this.path(`/softphone/takeover/${o(t)}`),{}),t)}createCallListener(t={}){return new ut(this,t)}async listenSession(t){return this.session(await this.app.post(this.path(`/softphone/listen/${o(t)}`),{}),t,"listen-media")}async coachSession(t){let e=this.session(await this.app.post(this.path(`/softphone/coach/${o(t)}`),{}),t,"listen-media");if(e.coaching!==!0)throw Error("Invalid coaching session");return e}async renewListening(t){await this.app.post(this.path(`/softphone/${t.coaching?"coach-renew":"listen-renew"}/${o(t.call_id)}`),{session_token:t.session_token})}async stopListening(t){await this.app.post(this.path(`/softphone/${t.coaching?"coach-stop":"listen-stop"}/${o(t.call_id)}`),{session_token:t.session_token})}async listenerAudit(t){return this.app.get(this.path(`/softphone/listen-audit/${o(t)}`))}async renew(t){await this.app.post(this.path(`/softphone/renew/${o(t.call_id)}`),{session_token:t.session_token})}async release(t){if(!t.session_token)throw Error("Answer session has no release token");await this.app.post(this.path(`/softphone/release/${o(t.call_id)}`),{session_token:t.session_token})}async hangup(t){await this.app.post(this.path(`/calls/${o(t)}/hangup`),{})}hold(t){return this.app.post(this.path(`/calls/${o(t)}/hold`),{})}resume(t){return this.app.post(this.path(`/calls/${o(t)}/resume`),{})}pauseRecording(t){return this.app.post(this.path(`/calls/${o(t)}/pause-recording`),{})}resumeRecording(t){return this.app.post(this.path(`/calls/${o(t)}/resume-recording`),{})}createSoftphone(t={}){return new rt(this,t)}mediaURL(t){return this.resolveMediaURL(t,"media")}listenerMediaURL(t){return this.resolveMediaURL(t,"listen-media")}resolveMediaURL(t,e){let s=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),h=new URL(t.media_url,s),a=`/api/apps/telephony/_install/${this.app.installId}/softphone/${e}/${o(t.call_id)}/`;if(h.origin!==s.origin||!["http:","https:"].includes(h.protocol)||h.username||h.password||h.search||h.hash||!h.pathname.startsWith(a)||!/^[A-Za-z0-9_-]+$/.test(h.pathname.slice(a.length)))throw Error("Invalid Telephony media endpoint");if(e==="listen-media"&&h.pathname.slice(a.length)!==t.session_token)throw Error("Listener credential mismatch");return h.protocol=h.protocol==="https:"?"wss:":"ws:",h.href}session(t,e,s="media"){let h=t;if(!h||typeof h.call_id!=="string"||typeof h.media_url!=="string"||e&&h.call_id!==e||h.session_token!==void 0&&typeof h.session_token!=="string")throw Error("Invalid Telephony session response");if(h.lease_seconds!==void 0&&(!Number.isFinite(h.lease_seconds)||h.lease_seconds<10||h.lease_seconds>3600||!h.session_token))throw Error("Invalid media lease");if(s==="listen-media"&&(!h.session_token||h.lease_seconds===void 0))throw Error("Invalid listener lease");return o(h.call_id),this.resolveMediaURL(h,s),h}}var Te=st({app:"telephony",create:({app:t})=>new K(t)});function Ne({app:t},e){return new K(t,e)}export{Ne as createClient,Wt as callTerminationLabel};
