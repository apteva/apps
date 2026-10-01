function J(t){return t}var at=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function st(t){let e=(t??"FR").trim().toUpperCase();return at[e]??at.FR}function ft(t,e){let a=[];if(!(t.cadence.reduce((m,i)=>m+i,0)>0)||!(e>0))return a;let h=0;while(h<e)for(let m=0;m<t.cadence.length;m+=2){let i=t.cadence[m]??0,c=t.cadence[m+1]??0;if(h>=e)break;if(i>0)a.push({start:h,end:Math.min(h+i,e)});h+=i+c}return a}function ht(t,e,a,s=0.12){let h=t.createGain();h.gain.value=0,h.connect(e);let m=s/Math.max(1,a.frequencies.length),i=a.frequencies.map((n)=>{let f=t.createOscillator();return f.type="sine",f.frequency.value=n,f.connect(h),f.start(),f}),c=t.currentTime+0.05,u=0,l=0.01,y=()=>{let n=t.currentTime-c+10;if(n<=u)return;for(let f of ft(a,n)){if(f.end<=u)continue;let o=c+Math.max(f.start,u),M=c+f.end;h.gain.setValueAtTime(0,o),h.gain.linearRampToValueAtTime(m,o+l),h.gain.setValueAtTime(m,Math.max(o+l,M-l)),h.gain.linearRampToValueAtTime(0,M)}u=n};y();let r=setInterval(y,4000),p=!1;return()=>{if(p)return;p=!0,clearInterval(r);try{h.gain.cancelScheduledValues(0)}catch{}h.gain.value=0;for(let n of i){try{n.stop()}catch{}n.disconnect()}h.disconnect()}}var A=24000,F=60;async function mt(t,e){try{await t.audioWorklet.addModule(e)}catch(a){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:a})}}function nt(t){let e=new Int16Array(t.length);for(let a=0;a<t.length;a++){let s=Math.max(-1,Math.min(1,t[a]));e[a]=s<0?s*32768:s*32767}return e.buffer}class O{history=new Float32Array(64);phase=0;process(t,e,a){if(e===a)return t;let s=new Float32Array(64+t.length);s.set(this.history),s.set(t,64);let h=e/a,m=Math.min(1,a/e)*0.9,i=[],c=this.phase;for(;c<t.length;c+=h){let u=c+32,l=0,y=0;for(let r=Math.ceil(u-32);r<=Math.floor(u+32);r++){let p=r-u,f=(Math.abs(p)<0.000000001?m:Math.sin(Math.PI*m*p)/(Math.PI*p))*(0.5+0.5*Math.cos(Math.PI*p/32));if(r>=0&&r<s.length)l+=s[r]*f,y+=f}i.push(y?l/y:0)}return this.phase=c-t.length,this.history=s.slice(-64),new Float32Array(i)}}function _t(t){let e=0;for(let a=0;a<t.length;a++)e+=t[a]*t[a];return Math.sqrt(e/Math.max(1,t.length))}var P={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function G(t){let e=t.playbackTargetMs??F,a=t.playbackMinMs??Math.min(F,e),s=t.playbackMaxMs??160;for(let h of[e,a,s])if(!Number.isFinite(h)||h<40||h>160)throw RangeError("Playback buffers must be between 40 and 160 ms");if(a>e||e>s)throw RangeError("Playback buffers require min <= target <= max");return{initialTargetMs:e,minTargetMs:a,maxTargetMs:s,hardMaxMs:320}}function Y(t){return{...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{},echoCancellation:t.echoCancellation,noiseSuppression:t.noiseSuppression,autoGainControl:t.autoGainControl}}function it(t){let e=t.getSettings();return{deviceLabel:t.label||"Default microphone",sampleRate:typeof e.sampleRate==="number"?e.sampleRate:null,channelCount:typeof e.channelCount==="number"?e.channelCount:null,echoCancellation:typeof e.echoCancellation==="boolean"?e.echoCancellation:null,noiseSuppression:typeof e.noiseSuppression==="boolean"?e.noiseSuppression:null,autoGainControl:typeof e.autoGainControl==="boolean"?e.autoGainControl:null}}function E(t){if(!Number.isFinite(t)||t<=0)return null;return 20*Math.log10(t)}function yt(t,e,a){let s=new ArrayBuffer(44+e*2),h=new DataView(s),m=(c,u)=>{for(let l=0;l<u.length;l++)h.setUint8(c+l,u.charCodeAt(l))};m(0,"RIFF"),h.setUint32(4,36+e*2,!0),m(8,"WAVE"),m(12,"fmt "),h.setUint32(16,16,!0),h.setUint16(20,1,!0),h.setUint16(22,1,!0),h.setUint32(24,a,!0),h.setUint32(28,a*2,!0),h.setUint16(32,2,!0),h.setUint16(34,16,!0),m(36,"data"),h.setUint32(40,e*2,!0);let i=44;for(let c of t)for(let u=0;u<c.length;u++)h.setInt16(i,c[u],!0),i+=2;return new Blob([s],{type:"audio/wav"})}class L{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new O;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(t,e=!0){this.onLevel=t;this.recordAudio=e}async start(t,e){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:Y(e)}),this.ensureOpen();let a=this.stream.getAudioTracks()[0];if(!a)throw Error("No microphone audio track was returned.");this.settings=it(a);try{this.ctx=new AudioContext({sampleRate:A,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await mt(this.ctx,t),this.ensureOpen();let s=this.ctx.sampleRate,h=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:e.inputGainDB,highpassFilter:e.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(m)=>{if(this.stopped)return;if(!(m.data instanceof Float32Array)){if(m.data.type==="capture.stats")this.peak=Math.max(this.peak,m.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,m.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,m.data.limiter_reduction_db??0);return}let i=m.data;if(s!==A)i=this.resampler.process(i,s,A);let c=_t(i);if(this.onLevel?.(c),this.recordAudio)this.frames.push(new Int16Array(nt(i)));if(this.samples+=i.length,c>=0.005){for(let u=0;u<i.length;u++)this.activeSquares+=i[u]*i[u];this.activeSamples+=i.length}},h.connect(this.capture),this.settings}catch(a){throw await this.release(),a}}async stop(){this.stopped=!0;let t=this.settings,e=this.frames,a=this.samples,s=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,h=this.peak;if(await this.release(),!t||a===0)throw Error("No microphone audio was captured.");return{audio:yt(e,a,A),durationMs:Math.round(a*1000/A),sampleRate:A,activeRmsDbfs:E(s),peakDbfs:E(h),postPeakDbfs:E(this.postPeak||h),limiterReductionDb:this.limiterReductionDB,settings:t}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let t of this.stream?.getTracks()??[])t.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class Z{callbacks;worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;opened=!1;cancelWorkerStart;microphoneTransportReady=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};diagnostics={rttMs:null,queueMs:0,targetMs:F,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:A,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:P.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t={}){this.callbacks=t}get isMuted(){return this.muted}async start(t,e,a,s=P){let h=G(s);this.diagnostics.targetMs=h.initialTargetMs,this.callbacks.onState?.("connecting");try{this.stream=await navigator.mediaDevices.getUserMedia({audio:Y(s)}),this.ensureOpen();let m=this.stream.getAudioTracks()[0];if(!m)throw Error("No microphone audio track was returned.");if(m.readyState==="ended")throw Error("Microphone disconnected before audio setup.");m.onmute=()=>this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser."),m.onunmute=()=>this.callbacks.onNotice?.("Microphone input restored."),m.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let i=it(m);this.diagnostics={...this.diagnostics,microphoneSampleRate:i.sampleRate??0,microphoneChannelCount:i.channelCount??0,echoCancellation:i.echoCancellation,noiseSuppression:i.noiseSuppression,autoGainControl:i.autoGainControl,micInputGainDb:s.inputGainDB};try{this.ctx=new AudioContext({sampleRate:A,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();if(this.ensureOpen(),s.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(s.outputDeviceId),this.ensureOpen();await mt(this.ctx,e),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let c=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:h}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),c.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=s.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await this.openWorker(t,a),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running"&&this.microphoneTransportReady)this.callbacks.onState?.("live")},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(m){if(this.closed)this.teardown();if(!this.closed)this.fail(m instanceof Error?m.message:"browser audio setup failed");throw m}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){if(await this.ctx?.resume(),this.microphoneTransportReady)this.callbacks.onState?.("live")}setOutputVolume(t){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,t))}sendDTMF(t){if(/^[0-9*#]+$/.test(t))this.sendText(JSON.stringify({type:"dtmf",digits:t}))}startRingback(t){if(this.closed||!this.ctx||this.ringback)return;this.ringback=ht(this.ctx,this.ctx.destination,st(t))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="capture.stats")return;this.micLevel=this.muted?0:e.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:E(e.active_rms??0),micPeakDbfs:E(e.pre_peak??0),micPostPeakDbfs:E(e.post_peak??0),micInputGainDb:e.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:e.limiter_reduction_db??0}},this.playback.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="stats")return;this.playbackTiming={played_ms:e.played_ms,max_residence_ms:e.max_residence_ms,drop_totals_ms:e.drop_totals_ms},this.speakerLevel=Math.max(this.speakerLevel,e.speaker_level??0),this.diagnostics={...this.diagnostics,queueMs:e.queue_ms??0,targetMs:e.target_ms??F,underruns:e.underruns??0,droppedMs:e.dropped_ms??0,maxQueueMs:e.max_queue_ms??0,playbackSequenceGaps:e.playback_sequence_gaps??0,dropEvents:[...this.diagnostics.dropEvents.filter((a)=>a.direction!=="carrier_to_operator"),...e.drop_events??[]].slice(-100)},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(t,e){return new Promise((a,s)=>{let h=new Worker(e);this.worker=h;let m=new MessageChannel,i=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:m.port1},[m.port1]),this.playback?.port.postMessage({type:"transport",port:i.port1},[i.port1]);let c=!1,u=setTimeout(()=>{if(c)return;c=!0,s(Error("audio connection timed out"))},1e4),l=(y)=>{if(c)return;if(c=!0,clearTimeout(u),y)s(y);else a()};this.cancelWorkerStart=()=>l(Error("audio session closed")),h.onmessage=(y)=>{if(this.closed){l(Error("audio session closed"));return}let r=y.data;if(r?.type==="socket.open")this.opened=!0,this.mediaSocketConnected=!0,this.startRTTProbe(),l();else if(r?.type==="socket.message")this.handleControl(r.data);else if(r?.type==="socket.close")if(this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else l(Error("audio connection closed before it was ready"));else if(r?.type==="socket.failed")this.mediaSocketConnected=!1,l(Error(r.detail||"audio connection lost")),this.fail(r.detail||"audio connection lost");else if(r?.type==="transport.drop"&&r.event)this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,r.event].slice(-100);else if(r?.type==="transport.stats")this.diagnostics.websocketBufferedBytes=r.buffered_bytes??0,this.transportTiming=r.timing??{}},h.onerror=()=>{if(l(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},h.postMessage({type:"init",audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:t,contextRate:this.ctx?.sampleRate??A,muted:this.muted,capturePort:m.port2,playbackPort:i.port2},[m.port2,i.port2])})}carrierDeliveryStalled=!1;handleControl(t){if(this.closed)return;try{let e=JSON.parse(t);if(e.type==="dtmf.error"||e.type==="dtmf.sent")this.callbacks.onNotice?.(e.type==="dtmf.sent"?"Keypad tone sent":e.detail||"Keypad tone failed");else if(e.type==="pong"&&typeof e.nonce==="number"&&e.nonce>=0)this.diagnostics.captureSequenceGaps=e.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-e.nonce)),this.callbacks.onDiagnostics?.({...this.diagnostics});else if(e.type==="call.ended"||e.type==="session.replaced"){this.closed=!0;try{this.callbacks.onState?.("ended",e.type)}finally{this.teardown()}}else if(e.type==="call.status"&&typeof e.call_id==="string"&&typeof e.status==="string"){if(e.hold_state&&e.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callbacks.onCallStatus?.(e)}else if(e.type==="call.error")this.fail(e.detail||"The call could not be connected.");else if(e.type==="media.delivery"){let a=e.state;if(a==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(a==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=a==="stalled"}else if(e.type==="peer.disconnected")this.microphoneTransportReady=!1,this.worker?.postMessage({type:"microphone.ready",value:!1}),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(e.type==="peer.connected")this.microphoneTransportReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.callbacks.onState?.("live",this.opened?void 0:"Audio reconnected")}catch{}}startRTTProbe(){this.stopRTTProbe();let t=()=>{this.sendText(JSON.stringify({type:"ping",nonce:performance.now()})),this.sendDiagnostics()};t(),this.pingTimer=setInterval(t,5000)}sendText(t){this.worker?.postMessage({type:"send.text",data:t})}sendDiagnostics(){let t=this.diagnostics;this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{timing:{transport:this.transportTiming,playback:this.playbackTiming},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:t.rttMs,playback_queue_ms:t.queueMs,playback_target_ms:t.targetMs,playback_max_queue_ms:t.maxQueueMs,playback_underruns:t.underruns,playback_dropped_ms:t.droppedMs,websocket_buffered_bytes:t.websocketBufferedBytes,audio_context_rate:t.audioContextRate,microphone_sample_rate:t.microphoneSampleRate,microphone_channel_count:t.microphoneChannelCount,echo_cancellation:t.echoCancellation,noise_suppression:t.noiseSuppression,auto_gain_control:t.autoGainControl,mic_active_rms_dbfs:t.micActiveRmsDbfs,mic_peak_dbfs:t.micPeakDbfs,mic_post_peak_dbfs:t.micPostPeakDbfs,mic_input_gain_db:t.micInputGainDb,mic_limiter_reduction_db:t.micLimiterReductionDb,capture_sequence_gaps:t.captureSequenceGaps,playback_sequence_gaps:t.playbackSequenceGaps,drop_events:t.dropEvents}})),this.callbacks.onDiagnostics?.({...t})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setMuted(t){if(this.muted=t,this.worker?.postMessage({type:"muted",value:t}),this.capture?.port.postMessage({type:"muted",value:t}),t)this.sendText(JSON.stringify({type:"interrupt"}))}stop(){let t=!this.closed;this.closed=!0;try{if(t)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(t){this.closed=!0;try{this.callbacks.onState?.("error",t)}finally{this.teardown()}}teardown(){this.stopRingback(),this.microphoneTransportReady=!1,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let t=this.worker;if(t?.postMessage({type:"close"}),t)setTimeout(()=>t.terminate(),100);this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((e)=>e.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var vt=A*F/1000;var B=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
    if (message === "flush" || message?.type === "flush") {
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
    this.processedSinceStats += out.length;
    if (this.processedSinceStats >= sampleRate) { this.processedSinceStats -= sampleRate; this.reportStats(); }
    return true;
  }
}

registerProcessor("softphone-capture", SoftphoneCaptureProcessor);
registerProcessor("softphone-playback", SoftphonePlaybackProcessor);
`;var K=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
function resample(frame, from, to) {
  if (from === to) return frame;
  const key = \`\${from}:\${to}\`;
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
    framedV2=false; serverClockOffset=null; serverClockUncertainty=null; serverClockSampleAt=0; playbackExpected=null; playbackDeliveryBase=null;
    ws.send(JSON.stringify({type:"media.capabilities",version:2,versions:[3,2]}));
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
    if (!(event.data instanceof ArrayBuffer) || event.data.byteLength % 2 !== 0) return;
    let buffer=event.data, sequence=playbackSequence++, sourceAudioMS=null;
    const magic=buffer.byteLength>=4 ? new DataView(buffer).getUint32(0,true) : 0;
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
`;function N(t=!1){let e=(t?[B]:[B,K]).map((a)=>URL.createObjectURL(new Blob([a],{type:"text/javascript"})));return{urls:e,dispose(){e.splice(0).forEach((a)=>URL.revokeObjectURL(a))}}}async function z(t,e=!1){if(typeof location>"u")return N(e);let a=new URL(t.mcpURL(),location.href);if(a.origin!==location.origin)return N(e);if(t.name!=="telephony"||!t.projectId||!Number.isSafeInteger(t.installId)||t.installId<=0)throw Error("Telephony audio requires a project and installation");return{urls:await Promise.all((e?[B]:[B,K]).map(async(m,i)=>{let c=new TextEncoder().encode(m),u=Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",c)),(p)=>p.toString(16).padStart(2,"0")).join(""),l=`/ui/frontend/${i===0?"worklet":"worker"}-${u}.js`;if(await t.get(l,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==m)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let r=new URL(`/api/apps/telephony/_install/${t.installId}${l}`,a);return r.searchParams.set("project_id",t.projectId),r.searchParams.set("install_id",String(t.installId)),r.href})),dispose(){}}}async function ct(){return(await navigator.mediaDevices.enumerateDevices()).filter((e)=>e.kind==="audioinput").map((e,a)=>({deviceId:e.deviceId,label:e.label||`Microphone ${a+1}`}))}function ut(t,e){let a=new L(t,!1),s,h=!1,m,i=()=>{return h=!0,m??=(async()=>{try{await a.cancel()}finally{s?.dispose(),s=void 0}})()};return{async start(c={}){if(h)throw Error("Microphone preview is already used");h=!0;try{if(s=e?await z(e,!0):N(!0),m)throw s.dispose(),s=void 0,Error("Microphone preview was cancelled");await a.start(s.urls[0],{...P,...c})}catch(u){throw await i(),u}},stop:i}}function rt(t){return{async preflight(e){(await navigator.mediaDevices.getUserMedia({audio:Y(e)})).getTracks().forEach((s)=>s.stop())},create(e){let a=new Z(e),s,h=!1,m=()=>{h=!0;try{a.stop()}finally{s?.dispose(),s=void 0}};return{async start(i,c){try{if(s=t?await z(t):N(),h)throw Error("Audio session was cancelled");await a.start(i,s.urls[0],s.urls[1],c)}catch(u){throw m(),u}},stop:m,setMuted:(i)=>a.setMuted(i),sendDTMF:(i)=>a.sendDTMF(i),setOutputVolume:(i)=>a.setOutputVolume(i),startRingback:(i)=>a.startRingback(i),stopRingback:()=>a.stopRingback()}}}}var U=`// Passive receiver only. Both directions share one rendering clock and delay.
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
`;function St(t){if(t.byteLength<26||t.byteLength>984||t.byteLength%2)throw Error("Invalid listener audio frame");let e=new DataView(t),a=e.getUint32(4,!0);if(e.getUint32(0,!0)!==827085889||a>1)throw Error("Invalid listener audio protocol");let s=Number(e.getBigUint64(8,!0)),h=Number(e.getBigUint64(16,!0));if(!Number.isSafeInteger(s)||!Number.isSafeInteger(h))throw Error("Invalid listener audio timing");let m=new Float32Array((t.byteLength-24)/2);for(let i=0;i<m.length;i++)m[i]=e.getInt16(24+i*2,!0)/32768;return{direction:a,sequence:s,timestampMS:h,frame:m}}function lt(t,e={}){return{create(a){let s,h,m,i,c,u=!1,l,y,r=0,p=!1,n=[0,0],f=[0,0],o=[void 0,void 0],M=[new O,new O],V=e.outputVolume??1,Q,$=()=>{if(u)return;if(u=!0,Q?.(),i)i.onmessage=i.onclose=i.onerror=null,i.close();if(h?.disconnect(),m?.disconnect(),s)s.onstatechange=null,s.close().catch(()=>{});if(c)URL.revokeObjectURL(c)},x=(d)=>{if(!u)$(),a.onClose(d)},C=()=>{if(u)throw Error("Listening cancelled")};return{async start(d){C();try{if(s=new AudioContext({latencyHint:"interactive"}),s.state==="suspended")await s.resume();if(C(),e.outputDeviceId&&"setSinkId"in s)await s.setSinkId(e.outputDeviceId);C();let w,tt=new URL(t.mcpURL(),location.href);if(tt.origin===location.origin){let W=`/ui/frontend/listener-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(U))),(q)=>q.toString(16).padStart(2,"0")).join("")}.js`,X=await t.get(W,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}});if(C(),X!==U)throw Error("Listener audio asset integrity mismatch");let T=new URL(`/api/apps/telephony/_install/${t.installId}${W}`,tt);T.searchParams.set("project_id",t.projectId),T.searchParams.set("install_id",String(t.installId)),w=T.href}else c=URL.createObjectURL(new Blob([U],{type:"text/javascript"})),w=c;await s.audioWorklet.addModule(w),C(),h=new AudioWorkletNode(s,"telephony-listener",{numberOfInputs:0,outputChannelCount:[e.stereo?2:1],processorOptions:{stereo:e.stereo}}),m=s.createGain(),m.gain.value=V,h.connect(m).connect(s.destination),h.onprocessorerror=()=>x("listener_audio_error"),h.port.onmessage=({data:R})=>{if(R?.type==="listener.diagnostics"&&!u)try{a.onDiagnostics?.({...R,sequence_gaps:[...f],network_excess_ms:r,network_dropped_ms:[...n]})}catch{}},s.onstatechange=()=>{if(s?.state==="suspended"||s?.state==="interrupted")x("listener_audio_paused")},await new Promise((R,W)=>{let X=!1,T=setTimeout(()=>{q(Error("Listener connection timed out")),x("listener_network_error")},1e4),q=(k)=>{if(X)return;X=!0,clearTimeout(T),Q=void 0,k?W(k):R()};Q=()=>q(Error("Listening cancelled")),i=new WebSocket(d),i.binaryType="arraybuffer",i.onmessage=({data:k})=>{if(u||!s||!h)return;try{if(typeof k==="string"){if(JSON.parse(k).type==="listener.ready")p=!0,q(),a.onReady();return}if(!p||!(k instanceof ArrayBuffer))return;let S=St(k),b=S.direction;if(o[b]!==void 0&&S.sequence<o[b])return;if(o[b]!==void 0&&S.sequence>o[b])f[b]+=S.sequence-o[b];o[b]=S.sequence+1;let v=performance.now()-S.timestampMS;if(y=Math.min(y??v,v),r=Math.max(0,v-y),r>200){n[b]+=S.frame.length*1000/24000;return}l??=s.currentTime*1000+60-S.timestampMS;let et=M[b].process(S.frame,24000,s.sampleRate);h.port.postMessage({direction:b,frame:et,playAtMS:S.timestampMS+l},[et.buffer])}catch{q(Error("Invalid listener media")),x("listener_protocol_error")}},i.onerror=()=>{q(Error("Listener connection failed")),x("listener_network_error")},i.onclose=(k)=>{q(Error(k.reason||"Listener disconnected")),x(k.reason||"listener_disconnected")}}),C()}catch(w){throw $(),w}},stop:$,setOutputVolume(d){if(!Number.isFinite(d)||d<0||d>1)throw RangeError("Listener volume must be 0–1");if(V=d,m)m.gain.value=d}}}}}class D{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;retry;retryUntil=0;retryDelay=500;disposed=!1;runtime;constructor(t,e={}){this.client=t;this.options=e;this.runtime=e.runtime??lt(t.app,e)}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.observers.add(t),()=>{this.observers.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let e of this.observers)try{e(this.snapshot)}catch{}}async listen(t){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.retryUntil=0,this.retryDelay=500;let e=++this.generation;return this.update({state:"connecting",callId:t,detail:void 0}),this.connect(t,e)}async connect(t,e){let a,s=++this.attempt,h=()=>e===this.generation&&s===this.attempt&&!this.disposed;try{if(a=await this.client.listenSession(t),!h()){await this.client.stopListening(a).catch(()=>{});return}this.session=a;let m=this.runtime.create({onReady:()=>{if(h())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0})},onClose:(c)=>{if(h())this.disconnected(c,t,e)},onDiagnostics:(c)=>{if(h())try{this.options.onDiagnostics?.(c)}catch{}}});this.audio=m;let i=!1;if(this.lease=setInterval(()=>{if(!h()||i||this.session!==a)return;i=!0,this.client.renewListening(a).catch(()=>{if(h())this.disconnected("access_revoked",t,e)}).finally(()=>{i=!1})},20000),await m.start(this.client.listenerMediaURL(a)),!h())m.stop()}catch(m){if(!h()||a&&this.session!==a)return;let i=m?.status,c;try{c=JSON.parse(m.body??"{}").code}catch{}let u=c==="call_ended"?"call_ended":i===401||i===403||i===404?"access_revoked":"listener_disconnected";throw this.disconnected(u,t,e,String(m)),m}}cleanup(){if(this.lease)clearInterval(this.lease);this.lease=void 0;let t=this.audio;this.audio=void 0,t?.stop();let e=this.session;return this.session=void 0,e?this.client.stopListening(e).catch(()=>{}):Promise.resolve()}disconnected(t,e,a,s=t){if(a!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let h=t==="call_ended",m=t==="access_revoked",i=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(t);if(!h&&!m&&i&&this.options.reconnect!==!1){if(this.retryUntil||=Date.now()+30000,Date.now()<this.retryUntil){this.update({state:"reconnecting",detail:s}),this.retry=setTimeout(()=>{if(this.retry=void 0,a===this.generation&&!this.disposed)this.connect(e,a).catch(()=>{})},this.retryDelay),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:h?"call_ended":m?"access_revoked":"disconnected",detail:s})}setOutputVolume(t){this.audio?.setOutputVolume(t)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let t=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0}),await t}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}function bt(t){if(!t)return"placing";if(I(t))return"ended";if(t==="ringing")return"ringing";if(t==="answered"||t==="in-progress")return"connected";return"placing"}var g=(t)=>t instanceof Error?t.message:String(t);class j{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;leaseTimer;leaseGeneration=0;timer;polling;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(t,e={}){this.client=t;this.options=e;if(this.runtime=e.audioRuntime??rt(t.app),this.audioOptions={...P,...e.audio},G(this.audioOptions),this.interval=e.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.assertOpen(),this.listeners.add(t),()=>{this.listeners.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let e of this.listeners)try{e(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(t){if(this.disposed||t!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(t){return!this.disposed&&t===this.generation}finish(t){if(this.current(t))this.update({busy:!1})}cancellable(t,e){let a=this.cancellation.signal;return new Promise((s,h)=>{let m=()=>{a.removeEventListener("abort",m),h(Error("Softphone operation cancelled"))};if(a.addEventListener("abort",m,{once:!0}),t.then(s,h).finally(()=>a.removeEventListener("abort",m)),!this.current(e)||a.aborted)m()})}begin(t){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||t&&this.session)throw Error("Softphone already has an active operation or call");let e=this.invalidate();return this.update({busy:!0,detail:void 0}),e}async dial(t){let e=this.begin(!0),a,s=!1;try{this.assertCurrent(e);let h={to:t.to.trim(),from:t.from?.trim(),timeout_sec:t.timeout_sec,recording:t.recording},m=JSON.stringify(h);if(!this.intent||this.intent.value!==m||t.idempotency_key&&t.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:m,request:{...h,idempotency_key:t.idempotency_key||crypto.randomUUID()}};return await this.cancellable(this.runtime.preflight(this.audioOptions),e),this.assertCurrent(e),a=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(e),s=!0,this.outbound=!0,await this.attachAudio(a,e),a.call_id}catch(h){if(a&&(this.current(e)||!s||this.disposed))await this.recoverStartup(a,e,()=>this.client.hangup(a.call_id));if(this.current(e))this.update({detail:g(h)});throw h}finally{this.finish(e)}}async answer(t,e={}){let a=this.begin(!0),s,h=!1;try{this.assertCurrent(a),s=await this.client.answer(t,e),this.assertCurrent(a),h=!0,this.outbound=!1,await this.attachAudio(s,a)}catch(m){if(s&&(this.current(a)||!h||this.disposed))await this.recoverStartup(s,a,()=>this.client.release(s));if(this.current(a))this.update({detail:g(m)});throw m}finally{this.finish(a)}}async recoverStartup(t,e,a){try{if(await a(),this.current(e))this.clearCall()}catch{if(this.current(e))this.session=t,this.update({callId:t.call_id,audioState:"error"}),this.startPolling()}}async join(t){await this.answer(t,{rejoin:!0})}async attach(t){await this.acquireSession(t,!1)}async takeover(t){await this.acquireSession(t,!0)}async acquireSession(t,e){let a=this.begin(!0);try{await this.cancellable(this.runtime.preflight(this.audioOptions),a),this.assertCurrent(a);let s=await(e?this.client.takeover(t):this.client.attach(t));this.assertCurrent(a),await this.attachAudio(s,a),await this.reconcileAttachedCall(s,a)}catch(s){if(this.current(a))this.update({detail:g(s)});throw s}finally{this.finish(a)}}async reconnect(t){if(!this.session)throw Error("No call to reconnect");let e={...this.audioOptions,...t};G(e);let a=this.begin(!1);this.audioOptions=e;try{await this.attachAudio(this.session,a)}catch(s){if(this.current(a))this.update({detail:g(s)});throw s}finally{this.finish(a)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let t=this.session;if(!t){this.cancellation.abort();return}let e=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let a=(async()=>{try{if(await this.client.hangup(t.call_id),this.current(e))this.clearCall()}catch(s){if(this.current(e))this.update({audioState:"error",detail:g(s)}),this.startPolling();throw s}finally{this.finish(e)}})();this.hangingUp=a;try{await a}finally{if(this.hangingUp===a)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(t){this.assertOpen();let e=this.session;if(!e)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let a=this.generation;this.update({busy:!0,detail:void 0});let s;try{s=this.client[t](e.call_id)}catch(h){if(this.current(a)&&this.session===e)this.update({busy:!1,detail:g(h)});throw h}this.controlPending=s;try{let h=await s;if(this.current(a)&&this.session===e)this.update({holdState:h.hold_state,recordingState:h.recording_state,controlError:h.control_error,capabilities:h.capabilities});return h}catch(h){if(this.current(a)&&this.session===e)this.update({detail:g(h)});throw h}finally{if(this.controlPending===s)this.controlPending=void 0;if(this.current(a)&&this.session===e)this.update({busy:!1})}}configureAudio(t){this.assertOpen();let e={...this.audioOptions,...t};G(e),this.audioOptions=e}setMuted(t){this.assertOpen(),this.audio?.setMuted(t),this.update({muted:t})}setOutputVolume(t){if(this.assertOpen(),!Number.isFinite(t)||t<0||t>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=t,this.audio?.setOutputVolume(t)}sendDTMF(t){if(this.assertOpen(),!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(t)}observeCall(t){if(this.disposed||t.id!==this.session?.call_id)return;if(t.direction)this.outbound=t.direction==="outbound";if(I(t.status)){this.invalidate(),this.clearCall(t.status),this.update({busy:!1,phase:"ended",termination:t.termination,answeredBy:t.answered_by??this.snapshot.answeredBy,endedAt:t.ended_at});return}this.update({carrierStatus:t.status,phase:bt(t.status),answeredBy:t.answered_by??this.snapshot.answeredBy,holdState:t.hold_state??this.snapshot.holdState,recordingState:t.recording_state??this.snapshot.recordingState,controlError:t.control_error??this.snapshot.controlError,capabilities:t.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let t=this.options.ringback;return typeof t==="object"&&t?t.country:void 0}syncRingback(){let t=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(t&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!t&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(t,e){this.assertCurrent(e),this.stopAudio(),this.session=t,this.update({callId:t.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let a,s=()=>!this.disposed&&a!==void 0&&this.audio===a,h=(m)=>{if(s())try{m()}catch{}};try{if(this.assertCurrent(e),a=this.runtime.create({onState:(m,i)=>{if(!s())return;if(m==="ended"&&i==="call.ended"){this.observeCall({id:t.call_id,status:"completed"});return}if(this.update({audioState:m,detail:i}),s()&&(m==="error"||m==="ended")){if(this.stopAudio(),m==="error")this.reconcileFailedAudio(t,e)}},onLevels:(m,i)=>h(()=>this.options.onLevels?.(m,i)),onDiagnostics:(m)=>h(()=>this.options.onDiagnostics?.(m)),onNotice:(m)=>h(()=>this.options.onNotice?.(m)),onCallStatus:(m)=>h(()=>this.observeCall({id:m.call_id,status:m.status,direction:m.direction,answered_at:m.answered_at,ended_at:m.ended_at,answered_by:m.answered_by,termination:m.termination,hold_state:m.hold_state,recording_state:m.recording_state,control_error:m.control_error}))}),this.audio=a,this.assertCurrent(e),this.startPolling(),this.startLease(t),a.setMuted(this.snapshot.muted),await this.cancellable(a.start(this.client.mediaURL(t),this.audioOptions),e),this.assertCurrent(e),!s())throw Error(this.snapshot.detail||"Audio connection ended during setup");a.setMuted(this.snapshot.muted),this.syncRingback()}catch(m){if(s())this.stopAudio();if(this.current(e))this.update({audioState:"error",detail:g(m)});throw m}}async reconcileAttachedCall(t,e){try{let a=await this.client.getCall(t.call_id);if(this.current(e)&&this.session===t&&a)this.observeCall(a)}catch{}}async reconcileFailedAudio(t,e){try{let a=await this.client.getCall(t.call_id);if(!this.current(e)||this.session!==t||!a)return;if(a.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(a)}catch{}}startLease(t){let e=++this.leaseGeneration;if(!t.lease_seconds)return;let a=async()=>{if(this.disposed||this.session!==t||e!==this.leaseGeneration)return;try{if(await this.client.renew(t),e===this.leaseGeneration)this.leaseTimer=setTimeout(a,t.lease_seconds*1000/3)}catch(s){if(e!==this.leaseGeneration)return;this.stopAudio(),this.update({audioState:"error",detail:`Audio authorization ended: ${g(s)}`})}};this.leaseTimer=setTimeout(a,t.lease_seconds*1000/3)}stopAudio(){++this.leaseGeneration,clearTimeout(this.leaseTimer),this.leaseTimer=void 0;let t=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{t?.stopRingback?.()}catch{}}try{t?.stop()}catch{}if(t)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let t=new AbortController;this.polling=t;let e=async()=>{let a=this.session?.call_id;if(!a||t.signal.aborted)return;try{let s=await this.client.getCall(a,t.signal);if(!t.signal.aborted&&s)this.observeCall(s)}catch(s){if(!t.signal.aborted)this.update({detail:`Call status unavailable: ${g(s)}`})}finally{if(!t.signal.aborted&&this.session&&!this.disposed)this.timer=setTimeout(e,this.interval)}};this.timer=setTimeout(e,this.interval)}clearCall(t){this.stopAudio(),this.stopPolling(),this.session=void 0,this.outbound=!1,this.update({callId:void 0,carrierStatus:t,audioState:"idle",muted:!1,detail:void 0,phase:t?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function At(t,e="en"){if(t?.reason==="time_limit")return e.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return t?.reason?.replaceAll("_"," ")??""}class pt extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function gt(t){return t.direction==="inbound"&&t.status==="pending"&&t.answerable!==!1&&!t.routing_waiting&&(t.peer_kind==="human"||Boolean(t.ring_offers?.some((e)=>e.kind==="browser")))}var I=(t)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(t);function _(t){if(!t||/[\s/\\?#]/.test(t))throw Error("Invalid call ID");return encodeURIComponent(t)}class H{app;options;listMicrophones=ct;createMicrophonePreview=(t)=>ut(t,this.app);constructor(t,e={}){this.app=t;this.options=e;if(t.name!=="telephony"||!t.projectId||!t.installId)throw Error("Telephony requires an explicit project and installation")}path(t){if(!this.options.authProvider)return t;return`/user${t}${t.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(t){let e=await this.app.get(this.path("/calls"),{signal:t});if(!Array.isArray(e?.calls)||e.calls.some((a)=>!a||typeof a.id!=="string"||typeof a.status!=="string"))throw Error("Invalid Telephony calls response");return e.calls}async getCall(t,e){let a=await this.app.get(this.path(`/calls?call_id=${_(t)}`),{signal:e});if(!Array.isArray(a?.calls))throw Error("Invalid Telephony call response");return a.calls.find((s)=>s.id===t)}incomingCalls(t){return t.filter(gt)}watchCalls(t,e={}){let a=e.intervalMs??2000;if(!Number.isFinite(a)||a<100)throw Error("Call watch interval must be at least 100 ms");let s=new AbortController,h,m,i=!1,c=!1,u=new Set,l=()=>{clearTimeout(h),s.abort(),m?.close(),e.signal?.removeEventListener("abort",l)},y=(p)=>{try{e.onError?.(p)}catch{l()}};if(e.signal?.addEventListener("abort",l,{once:!0}),e.signal?.aborted)l();let r=async(p)=>{if(s.signal.aborted)return;if(i){c=!0;return}clearTimeout(h),i=!0;let n=performance.now();try{let f=await this.listCalls(s.signal);if(!s.signal.aborted){t(f);for(let o of f){if(o.answerable!==!0||o.status!=="pending")continue;for(let M of o.ring_offers??[]){if(M.kind!=="browser"||!M.id||u.has(M.id))continue;u.add(M.id),this.acknowledgeOffer(o.id,M.id).catch((V)=>{if(![404,405].includes(V?.status??0))u.delete(M.id)})}}try{e.onTiming?.({trigger:p,fetchMs:performance.now()-n})}catch{}}}catch(f){if(!s.signal.aborted){let o=f?.status;try{e.onFailure?.({trigger:p,fetchMs:performance.now()-n,status:typeof o==="number"?o:void 0,error:f})}catch{}y(f)}}finally{if(i=!1,!s.signal.aborted)if(c)c=!1,r("push");else h=setTimeout(()=>void r("poll"),a)}};if(r("poll"),!s.signal.aborted&&e.push!==!1&&typeof this.app.subscribe==="function")try{m=this.app.subscribe(this.path("/calls/events"),(p)=>{if(p.type==="calls.changed")r("push");else if(p.type==="access.revoked")m?.close(),r("push")},{transport:"fetch",signal:s.signal,reconnectDelayMs:250,onError:(p)=>{let n=p?.status;if(n===404||n===405||n===501)m?.close()}})}catch{}return{close:l}}async place(t){if(!/^\+[1-9]\d{7,14}$/.test(t.to)||!t.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");return this.session(await this.app.post(this.path("/softphone/place"),t))}async answer(t,e={}){try{return this.session(await this.app.post(this.path(`/softphone/answer/${_(t)}`),e),t)}catch(a){let s=a;if(s.status===409&&typeof s.body==="string"){let h;try{h=JSON.parse(s.body)}catch{}if(h?.code==="offer_expired")throw new pt}throw a}}async acknowledgeOffer(t,e){await this.app.post(this.path(`/softphone/offer/ack/${_(t)}`),{offer_id:e})}async declineOffer(t,e){await this.app.post(this.path(`/softphone/offer/decline/${_(t)}`),{offer_id:e})}async attach(t){return this.session(await this.app.post(this.path(`/softphone/attach/${_(t)}`),{}),t)}async takeover(t){return this.session(await this.app.post(this.path(`/softphone/takeover/${_(t)}`),{}),t)}createCallListener(t={}){return new D(this,t)}async listenSession(t){return this.session(await this.app.post(this.path(`/softphone/listen/${_(t)}`),{}),t,"listen-media")}async renewListening(t){await this.app.post(this.path(`/softphone/listen-renew/${_(t.call_id)}`),{session_token:t.session_token})}async stopListening(t){await this.app.post(this.path(`/softphone/listen-stop/${_(t.call_id)}`),{session_token:t.session_token})}async listenerAudit(t){return this.app.get(this.path(`/softphone/listen-audit/${_(t)}`))}async renew(t){await this.app.post(this.path(`/softphone/renew/${_(t.call_id)}`),{session_token:t.session_token})}async release(t){if(!t.session_token)throw Error("Answer session has no release token");await this.app.post(this.path(`/softphone/release/${_(t.call_id)}`),{session_token:t.session_token})}async hangup(t){await this.app.post(this.path(`/calls/${_(t)}/hangup`),{})}hold(t){return this.app.post(this.path(`/calls/${_(t)}/hold`),{})}resume(t){return this.app.post(this.path(`/calls/${_(t)}/resume`),{})}pauseRecording(t){return this.app.post(this.path(`/calls/${_(t)}/pause-recording`),{})}resumeRecording(t){return this.app.post(this.path(`/calls/${_(t)}/resume-recording`),{})}createSoftphone(t={}){return new j(this,t)}mediaURL(t){return this.resolveMediaURL(t,"media")}listenerMediaURL(t){return this.resolveMediaURL(t,"listen-media")}resolveMediaURL(t,e){let a=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),s=new URL(t.media_url,a),h=`/api/apps/telephony/_install/${this.app.installId}/softphone/${e}/${_(t.call_id)}/`;if(s.origin!==a.origin||!["http:","https:"].includes(s.protocol)||s.username||s.password||s.search||s.hash||!s.pathname.startsWith(h)||!/^[A-Za-z0-9_-]+$/.test(s.pathname.slice(h.length)))throw Error("Invalid Telephony media endpoint");if(e==="listen-media"&&s.pathname.slice(h.length)!==t.session_token)throw Error("Listener credential mismatch");return s.protocol=s.protocol==="https:"?"wss:":"ws:",s.href}session(t,e,a="media"){let s=t;if(!s||typeof s.call_id!=="string"||typeof s.media_url!=="string"||e&&s.call_id!==e||s.session_token!==void 0&&typeof s.session_token!=="string")throw Error("Invalid Telephony session response");if(s.lease_seconds!==void 0&&(!Number.isFinite(s.lease_seconds)||s.lease_seconds<10||s.lease_seconds>3600||!s.session_token))throw Error("Invalid media lease");if(a==="listen-media"&&(!s.session_token||s.lease_seconds===void 0))throw Error("Invalid listener lease");return _(s.call_id),this.resolveMediaURL(s,a),s}}var ye=J({app:"telephony",create:({app:t})=>new H(t)});function ke({app:t},e){return new H(t,e)}export{ke as createClient,At as callTerminationLabel};
