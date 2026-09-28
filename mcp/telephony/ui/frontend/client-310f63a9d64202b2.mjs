function Y(t){return t}var N=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function $(t){let h=(t??"FR").trim().toUpperCase();return N[h]??N.FR}function j(t,h){let e=[];if(!(t.cadence.reduce((a,f)=>a+f,0)>0)||!(h>0))return e;let m=0;while(m<h)for(let a=0;a<t.cadence.length;a+=2){let f=t.cadence[a]??0,u=t.cadence[a+1]??0;if(m>=h)break;if(f>0)e.push({start:m,end:Math.min(m+f,h)});m+=f+u}return e}function V(t,h,e,s=0.12){let m=t.createGain();m.gain.value=0,m.connect(h);let a=s/Math.max(1,e.frequencies.length),f=e.frequencies.map((S)=>{let k=t.createOscillator();return k.type="sine",k.frequency.value=S,k.connect(m),k.start(),k}),u=t.currentTime+0.05,M=0,y=0.01,q=()=>{let S=t.currentTime-u+10;if(S<=M)return;for(let k of j(e,S)){if(k.end<=M)continue;let P=u+Math.max(k.start,M),b=u+k.end;m.gain.setValueAtTime(0,P),m.gain.linearRampToValueAtTime(a,P+y),m.gain.setValueAtTime(a,Math.max(P+y,b-y)),m.gain.linearRampToValueAtTime(0,b)}M=S};q();let i=setInterval(q,4000),c=!1;return()=>{if(c)return;c=!0,clearInterval(i);try{m.gain.cancelScheduledValues(0)}catch{}m.gain.value=0;for(let S of f){try{S.stop()}catch{}S.disconnect()}m.disconnect()}}var _=24000,E=60;async function Z(t,h){try{await t.audioWorklet.addModule(h)}catch(e){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:e})}}function l(t){let h=new Int16Array(t.length);for(let e=0;e<t.length;e++){let s=Math.max(-1,Math.min(1,t[e]));h[e]=s<0?s*32768:s*32767}return h.buffer}class K{history=new Float32Array(64);phase=0;process(t,h,e){if(h===e)return t;let s=new Float32Array(64+t.length);s.set(this.history),s.set(t,64);let m=h/e,a=Math.min(1,e/h)*0.9,f=[],u=this.phase;for(;u<t.length;u+=m){let M=u+32,y=0,q=0;for(let i=Math.ceil(M-32);i<=Math.floor(M+32);i++){let c=i-M,k=(Math.abs(c)<0.000000001?a:Math.sin(Math.PI*a*c)/(Math.PI*c))*(0.5+0.5*Math.cos(Math.PI*c/32));if(i>=0&&i<s.length)y+=s[i]*k,q+=k}f.push(q?y/q:0)}return this.phase=u-t.length,this.history=s.slice(-64),new Float32Array(f)}}function g(t){let h=0;for(let e=0;e<t.length;e++)h+=t[e]*t[e];return Math.sqrt(h/Math.max(1,t.length))}var x={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function w(t){let h=t.playbackTargetMs??E,e=t.playbackMinMs??Math.min(E,h),s=t.playbackMaxMs??160;for(let m of[h,e,s])if(!Number.isFinite(m)||m<40||m>160)throw RangeError("Playback buffers must be between 40 and 160 ms");if(e>h||h>s)throw RangeError("Playback buffers require min <= target <= max");return{initialTargetMs:h,minTargetMs:e,maxTargetMs:s,hardMaxMs:320}}function T(t){return{...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{},echoCancellation:t.echoCancellation,noiseSuppression:t.noiseSuppression,autoGainControl:t.autoGainControl}}function J(t){let h=t.getSettings();return{deviceLabel:t.label||"Default microphone",sampleRate:typeof h.sampleRate==="number"?h.sampleRate:null,channelCount:typeof h.channelCount==="number"?h.channelCount:null,echoCancellation:typeof h.echoCancellation==="boolean"?h.echoCancellation:null,noiseSuppression:typeof h.noiseSuppression==="boolean"?h.noiseSuppression:null,autoGainControl:typeof h.autoGainControl==="boolean"?h.autoGainControl:null}}function R(t){if(!Number.isFinite(t)||t<=0)return null;return 20*Math.log10(t)}function I(t,h,e){let s=new ArrayBuffer(44+h*2),m=new DataView(s),a=(u,M)=>{for(let y=0;y<M.length;y++)m.setUint8(u+y,M.charCodeAt(y))};a(0,"RIFF"),m.setUint32(4,36+h*2,!0),a(8,"WAVE"),a(12,"fmt "),m.setUint32(16,16,!0),m.setUint16(20,1,!0),m.setUint16(22,1,!0),m.setUint32(24,e,!0),m.setUint32(28,e*2,!0),m.setUint16(32,2,!0),m.setUint16(34,16,!0),a(36,"data"),m.setUint32(40,h*2,!0);let f=44;for(let u of t)for(let M=0;M<u.length;M++)m.setInt16(f,u[M],!0),f+=2;return new Blob([s],{type:"audio/wav"})}class G{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new K;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(t,h=!0){this.onLevel=t;this.recordAudio=h}async start(t,h){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:T(h)}),this.ensureOpen();let e=this.stream.getAudioTracks()[0];if(!e)throw Error("No microphone audio track was returned.");this.settings=J(e);try{this.ctx=new AudioContext({sampleRate:_,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await Z(this.ctx,t),this.ensureOpen();let s=this.ctx.sampleRate,m=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:h.inputGainDB,highpassFilter:h.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(a)=>{if(this.stopped)return;if(!(a.data instanceof Float32Array)){if(a.data.type==="capture.stats")this.peak=Math.max(this.peak,a.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,a.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,a.data.limiter_reduction_db??0);return}let f=a.data;if(s!==_)f=this.resampler.process(f,s,_);let u=g(f);if(this.onLevel?.(u),this.recordAudio)this.frames.push(new Int16Array(l(f)));if(this.samples+=f.length,u>=0.005){for(let M=0;M<f.length;M++)this.activeSquares+=f[M]*f[M];this.activeSamples+=f.length}},m.connect(this.capture),this.settings}catch(e){throw await this.release(),e}}async stop(){this.stopped=!0;let t=this.settings,h=this.frames,e=this.samples,s=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,m=this.peak;if(await this.release(),!t||e===0)throw Error("No microphone audio was captured.");return{audio:I(h,e,_),durationMs:Math.round(e*1000/_),sampleRate:_,activeRmsDbfs:R(s),peakDbfs:R(m),postPeakDbfs:R(this.postPeak||m),limiterReductionDb:this.limiterReductionDB,settings:t}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let t of this.stream?.getTracks()??[])t.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class H{callbacks;worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;opened=!1;cancelWorkerStart;microphoneTransportReady=!1;ringback=null;diagnostics={rttMs:null,queueMs:0,targetMs:E,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:_,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:x.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t={}){this.callbacks=t}get isMuted(){return this.muted}async start(t,h,e,s=x){let m=w(s);this.diagnostics.targetMs=m.initialTargetMs,this.callbacks.onState?.("connecting");try{this.stream=await navigator.mediaDevices.getUserMedia({audio:T(s)}),this.ensureOpen();let a=this.stream.getAudioTracks()[0];if(!a)throw Error("No microphone audio track was returned.");if(a.readyState==="ended")throw Error("Microphone disconnected before audio setup.");a.onmute=()=>this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser."),a.onunmute=()=>this.callbacks.onNotice?.("Microphone input restored."),a.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let f=J(a);this.diagnostics={...this.diagnostics,microphoneSampleRate:f.sampleRate??0,microphoneChannelCount:f.channelCount??0,echoCancellation:f.echoCancellation,noiseSuppression:f.noiseSuppression,autoGainControl:f.autoGainControl,micInputGainDb:s.inputGainDB};try{this.ctx=new AudioContext({sampleRate:_,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();if(this.ensureOpen(),s.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(s.outputDeviceId),this.ensureOpen();await Z(this.ctx,h),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let u=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:m}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),u.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=s.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await this.openWorker(t,e),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running"&&this.microphoneTransportReady)this.callbacks.onState?.("live")},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(a){if(this.closed)this.teardown();if(!this.closed)this.fail(a instanceof Error?a.message:"browser audio setup failed");throw a}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){if(await this.ctx?.resume(),this.microphoneTransportReady)this.callbacks.onState?.("live")}setOutputVolume(t){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,t))}sendDTMF(t){if(/^[0-9*#]+$/.test(t))this.sendText(JSON.stringify({type:"dtmf",digits:t}))}startRingback(t){if(this.closed||!this.ctx||this.ringback)return;this.ringback=V(this.ctx,this.ctx.destination,$(t))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(t)=>{let h=t.data;if(h?.type!=="capture.stats")return;this.micLevel=this.muted?0:h.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:R(h.active_rms??0),micPeakDbfs:R(h.pre_peak??0),micPostPeakDbfs:R(h.post_peak??0),micInputGainDb:h.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:h.limiter_reduction_db??0}},this.playback.port.onmessage=(t)=>{let h=t.data;if(h?.type!=="stats")return;this.speakerLevel=Math.max(this.speakerLevel,h.speaker_level??0),this.diagnostics={...this.diagnostics,queueMs:h.queue_ms??0,targetMs:h.target_ms??E,underruns:h.underruns??0,droppedMs:h.dropped_ms??0,maxQueueMs:h.max_queue_ms??0,playbackSequenceGaps:h.playback_sequence_gaps??0,dropEvents:[...this.diagnostics.dropEvents.filter((e)=>e.direction!=="carrier_to_operator"),...h.drop_events??[]].slice(-100)},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(t,h){return new Promise((e,s)=>{let m=new Worker(h);this.worker=m;let a=new MessageChannel,f=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:a.port1},[a.port1]),this.playback?.port.postMessage({type:"transport",port:f.port1},[f.port1]);let u=!1,M=setTimeout(()=>{if(u)return;u=!0,s(Error("audio connection timed out"))},1e4),y=(q)=>{if(u)return;if(u=!0,clearTimeout(M),q)s(q);else e()};this.cancelWorkerStart=()=>y(Error("audio session closed")),m.onmessage=(q)=>{if(this.closed){y(Error("audio session closed"));return}let i=q.data;if(i?.type==="socket.open")this.opened=!0,this.startRTTProbe(),y();else if(i?.type==="socket.message")this.handleControl(i.data);else if(i?.type==="socket.close")if(this.stopRTTProbe(),this.microphoneTransportReady=!1,this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else y(Error("audio connection closed before it was ready"));else if(i?.type==="socket.failed")y(Error(i.detail||"audio connection lost")),this.fail(i.detail||"audio connection lost");else if(i?.type==="transport.drop"&&i.event)this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,i.event].slice(-100);else if(i?.type==="transport.stats")this.diagnostics.websocketBufferedBytes=i.buffered_bytes??0},m.onerror=()=>{if(y(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},m.postMessage({type:"init",mediaURL:t,contextRate:this.ctx?.sampleRate??_,muted:this.muted,capturePort:a.port2,playbackPort:f.port2},[a.port2,f.port2])})}handleControl(t){if(this.closed)return;try{let h=JSON.parse(t);if(h.type==="dtmf.error"||h.type==="dtmf.sent")this.callbacks.onNotice?.(h.type==="dtmf.sent"?"Keypad tone sent":h.detail||"Keypad tone failed");else if(h.type==="pong"&&typeof h.nonce==="number"&&h.nonce>=0)this.diagnostics.captureSequenceGaps=h.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-h.nonce)),this.callbacks.onDiagnostics?.({...this.diagnostics});else if(h.type==="call.ended"||h.type==="session.replaced"){this.closed=!0;try{this.callbacks.onState?.("ended",h.type)}finally{this.teardown()}}else if(h.type==="call.status"&&typeof h.call_id==="string"&&typeof h.status==="string"){if(h.hold_state&&h.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callbacks.onCallStatus?.(h)}else if(h.type==="call.error")this.fail(h.detail||"The call could not be connected.");else if(h.type==="peer.disconnected")this.microphoneTransportReady=!1,this.worker?.postMessage({type:"microphone.ready",value:!1}),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(h.type==="peer.connected")this.microphoneTransportReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.callbacks.onState?.("live",this.opened?void 0:"Audio reconnected")}catch{}}startRTTProbe(){this.stopRTTProbe();let t=()=>{this.sendText(JSON.stringify({type:"ping",nonce:performance.now()})),this.sendDiagnostics()};t(),this.pingTimer=setInterval(t,5000)}sendText(t){this.worker?.postMessage({type:"send.text",data:t})}sendDiagnostics(){let t=this.diagnostics;this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{rtt_ms:t.rttMs,playback_queue_ms:t.queueMs,playback_target_ms:t.targetMs,playback_max_queue_ms:t.maxQueueMs,playback_underruns:t.underruns,playback_dropped_ms:t.droppedMs,websocket_buffered_bytes:t.websocketBufferedBytes,audio_context_rate:t.audioContextRate,microphone_sample_rate:t.microphoneSampleRate,microphone_channel_count:t.microphoneChannelCount,echo_cancellation:t.echoCancellation,noise_suppression:t.noiseSuppression,auto_gain_control:t.autoGainControl,mic_active_rms_dbfs:t.micActiveRmsDbfs,mic_peak_dbfs:t.micPeakDbfs,mic_post_peak_dbfs:t.micPostPeakDbfs,mic_input_gain_db:t.micInputGainDb,mic_limiter_reduction_db:t.micLimiterReductionDb,capture_sequence_gaps:t.captureSequenceGaps,playback_sequence_gaps:t.playbackSequenceGaps,drop_events:t.dropEvents}})),this.callbacks.onDiagnostics?.({...t})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setMuted(t){if(this.muted=t,this.worker?.postMessage({type:"muted",value:t}),this.capture?.port.postMessage({type:"muted",value:t}),t)this.sendText(JSON.stringify({type:"interrupt"}))}stop(){let t=!this.closed;this.closed=!0;try{if(t)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(t){this.closed=!0;try{this.callbacks.onState?.("error",t)}finally{this.teardown()}}teardown(){this.stopRingback(),this.microphoneTransportReady=!1,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let t=this.worker;if(t?.postMessage({type:"close"}),t)setTimeout(()=>t.terminate(),100);this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((h)=>h.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var xt=_*E/1000;var F=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
      this.queue = []; this.queued = 0; this.offset = 0; this.playing = false;
      return;
    }
    const chunk = message?.frame ?? message;
    if (!chunk || typeof chunk.length !== "number") return;
    const sequence = Number.isInteger(message?.sequence) ? message.sequence : null;
    if (sequence !== null) {
      if (this.expectedSequence !== null && sequence > this.expectedSequence) this.sequenceGaps += sequence - this.expectedSequence;
      this.expectedSequence = sequence + 1;
    }
    this.queue.push({ frame: chunk, sequence, timestamp_ms: message?.timestamp_ms ?? currentTime * 1000 });
    this.queued += chunk.length;
    this.maxQueuedSamples = Math.max(this.maxQueuedSamples, this.queued);
    const softMax = this.msToSamples(this.targetMs + 80);
    if (this.playing && this.queued > softMax) this.dropOldest(this.queued - this.msToSamples(this.targetMs + 30), "playback_soft_limit");
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
      speaker_level: this.speakerPeak,
    });
    this.speakerPeak = 0;
  }

  process(_inputs, outputs) {
    const out = outputs[0][0];
    if (!out) return true;
    out.fill(0);
    if (!this.playing && this.queued > 0) this.startupWaitSamples += out.length;
    if (!this.playing && this.queued > 0 && (this.queued >= this.msToSamples(this.targetMs) || this.startupWaitSamples >= this.msToSamples(this.targetMs))) { this.playing = true; this.startupWaitSamples = 0; }
    if (this.queued === 0) this.startupWaitSamples = 0;
    let written = 0;
    if (this.playing) {
      while (written < out.length && this.queue.length > 0) {
        const item = this.queue[0];
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
`;var X=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
// and consume frames directly so panel rendering cannot stall live speech.
const MAGIC = 0x31545041; // "APT1" in little endian.
const HEADER_BYTES = 16;
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

function framedCapture(frame, sequence, timestampMS, sourceRate) {
  const pcm = pcm16(resample(frame, sourceRate || SAMPLE_RATE, SAMPLE_RATE));
  const out = new ArrayBuffer(HEADER_BYTES + pcm.byteLength);
  const view = new DataView(out);
  view.setUint32(0, MAGIC, true);
  view.setUint32(4, sequence >>> 0, true);
  view.setFloat64(8, timestampMS, true);
  new Uint8Array(out, HEADER_BYTES).set(new Uint8Array(pcm.buffer));
  return out;
}

function decodePlayback(buffer) {
  const input = new Int16Array(buffer);
  const out = new Float32Array(input.length);
  for (let i = 0; i < input.length; i += 1) out[i] = input[i] / 0x8000;
  return out;
}

function capture(message) {
  if (message?.type !== "capture" || muted || !microphoneReady || !socket || socket.readyState !== WebSocket.OPEN) return;
  if (socket.bufferedAmount > MAX_CAPTURE_BUFFERED_BYTES) {
    postMessage({
      type: "transport.drop",
      event: {
        timestamp: new Date().toISOString(), direction: "operator_to_carrier", reason: "websocket_backpressure",
        duration_ms: Math.round(message.frame.length * 1000 / (message.sample_rate || SAMPLE_RATE)),
        queue_before_ms: Math.round(socket.bufferedAmount * 1000 / (SAMPLE_RATE * 2)),
        queue_after_ms: Math.round(socket.bufferedAmount * 1000 / (SAMPLE_RATE * 2)), sequence: message.sequence,
      },
    });
    return;
  }
  socket.send(framedCapture(message.frame, message.sequence, message.timestamp_ms, message.sample_rate));
}

function connect() {
  if (closed || !mediaURL) return;
  const ws = new WebSocket(mediaURL);
  ws.binaryType = "arraybuffer";
  socket = ws;
  let openedAt = 0;
  let lastReceivedAt = Date.now();
  let lastPingAt = 0;
  // CONNECTING sockets and half-open TCP connections do not always emit close.
  // Run the heartbeat in the worker so a busy UI thread cannot fake an outage.
  healthTimer = setInterval(() => {
    if (closed || socket !== ws) return;
    const now = Date.now();
    if (now - lastReceivedAt >= (openedAt ? 15000 : 10000)) {
      detach();
      ws.close();
      return;
    }
    if (ws.readyState === WebSocket.OPEN && now - lastPingAt >= 5000) {
      lastPingAt = now;
      ws.send(JSON.stringify({ type: "ping", nonce: -1 }));
    }
  }, 1000);
  ws.onopen = () => {
    if (socket !== ws || closed) { ws.close(); return; }
    openedAt = Date.now();
    lastReceivedAt = openedAt;
    postMessage({ type: "socket.open" });
  };
  ws.onmessage = (event) => {
    if (closed || socket !== ws) return;
    lastReceivedAt = Date.now();
    // A responsive transport is healthy even while the callee is ringing.
    // Handshakes alone must not reset the retry budget.
    if (openedAt && lastReceivedAt - openedAt >= 10000) { reconnectStartedAt = 0; reconnectAttempt = 0; }
    if (typeof event.data === "string") {
      postMessage({ type: "socket.message", data: event.data });
      return;
    }
    if (!(event.data instanceof ArrayBuffer) || event.data.byteLength % 2 !== 0) return;
    const frame = resample(decodePlayback(event.data), SAMPLE_RATE, contextRate);
    const sequence = playbackSequence++;
    playbackPort?.postMessage({ type: "playback", frame, sequence, timestamp_ms: performance.now() }, [frame.buffer]);
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
      capturePort = message.capturePort;
      playbackPort = message.playbackPort;
      capturePort.onmessage = (captureEvent) => capture(captureEvent.data);
      capturePort.start?.();
      playbackPort.start?.();
      connect();
      break;
    case "muted":
      muted = Boolean(message.value);
      resamplers.clear();
      break;
    case "microphone.ready":
      if (!message.value) resamplers.clear();
      microphoneReady = Boolean(message.value);
      break;
    case "send.text":
      if (socket?.readyState === WebSocket.OPEN) {
        socket.send(message.data);
        postMessage({ type: "transport.stats", buffered_bytes: socket.bufferedAmount });
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
`;function B(t=!1){let h=(t?[F]:[F,X]).map((e)=>URL.createObjectURL(new Blob([e],{type:"text/javascript"})));return{urls:h,dispose(){h.splice(0).forEach((e)=>URL.revokeObjectURL(e))}}}async function C(t,h=!1){if(typeof location>"u")return B(h);let e=new URL(t.mcpURL(),location.href);if(e.origin!==location.origin)return B(h);if(t.name!=="telephony"||!t.projectId||!Number.isSafeInteger(t.installId)||t.installId<=0)throw Error("Telephony audio requires a project and installation");return{urls:await Promise.all((h?[F]:[F,X]).map(async(a,f)=>{let u=new TextEncoder().encode(a),M=Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",u)),(c)=>c.toString(16).padStart(2,"0")).join(""),y=`/ui/frontend/${f===0?"worklet":"worker"}-${M}.js`;if(await t.get(y,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==a)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let i=new URL(`/api/apps/telephony/_install/${t.installId}${y}`,e);return i.searchParams.set("project_id",t.projectId),i.searchParams.set("install_id",String(t.installId)),i.href})),dispose(){}}}async function z(){return(await navigator.mediaDevices.enumerateDevices()).filter((h)=>h.kind==="audioinput").map((h,e)=>({deviceId:h.deviceId,label:h.label||`Microphone ${e+1}`}))}function U(t,h){let e=new G(t,!1),s,m=!1,a,f=()=>{return m=!0,a??=(async()=>{try{await e.cancel()}finally{s?.dispose(),s=void 0}})()};return{async start(u={}){if(m)throw Error("Microphone preview is already used");m=!0;try{if(s=h?await C(h,!0):B(!0),a)throw s.dispose(),s=void 0,Error("Microphone preview was cancelled");await e.start(s.urls[0],{...x,...u})}catch(M){throw await f(),M}},stop:f}}function D(t){return{async preflight(h){(await navigator.mediaDevices.getUserMedia({audio:T(h)})).getTracks().forEach((s)=>s.stop())},create(h){let e=new H(h),s,m=!1,a=()=>{m=!0;try{e.stop()}finally{s?.dispose(),s=void 0}};return{async start(f,u){try{if(s=t?await C(t):B(),m)throw Error("Audio session was cancelled");await e.start(f,s.urls[0],s.urls[1],u)}catch(M){throw a(),M}},stop:a,setMuted:(f)=>e.setMuted(f),sendDTMF:(f)=>e.sendDTMF(f),setOutputVolume:(f)=>e.setOutputVolume(f),startRingback:(f)=>e.startRingback(f),stopRingback:()=>e.stopRingback()}}}}function v(t){if(!t)return"placing";if(Q(t))return"ended";if(t==="ringing")return"ringing";if(t==="answered"||t==="in-progress")return"connected";return"placing"}var A=(t)=>t instanceof Error?t.message:String(t);class O{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;leaseTimer;leaseGeneration=0;timer;polling;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(t,h={}){this.client=t;this.options=h;if(this.runtime=h.audioRuntime??D(t.app),this.audioOptions={...x,...h.audio},w(this.audioOptions),this.interval=h.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.assertOpen(),this.listeners.add(t),()=>{this.listeners.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let h of this.listeners)try{h(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(t){if(this.disposed||t!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(t){return!this.disposed&&t===this.generation}finish(t){if(this.current(t))this.update({busy:!1})}cancellable(t,h){let e=this.cancellation.signal;return new Promise((s,m)=>{let a=()=>{e.removeEventListener("abort",a),m(Error("Softphone operation cancelled"))};if(e.addEventListener("abort",a,{once:!0}),t.then(s,m).finally(()=>e.removeEventListener("abort",a)),!this.current(h)||e.aborted)a()})}begin(t){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||t&&this.session)throw Error("Softphone already has an active operation or call");let h=this.invalidate();return this.update({busy:!0,detail:void 0}),h}async dial(t){let h=this.begin(!0),e,s=!1;try{this.assertCurrent(h);let m={to:t.to.trim(),from:t.from?.trim(),timeout_sec:t.timeout_sec,recording:t.recording},a=JSON.stringify(m);if(!this.intent||this.intent.value!==a||t.idempotency_key&&t.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:a,request:{...m,idempotency_key:t.idempotency_key||crypto.randomUUID()}};return await this.cancellable(this.runtime.preflight(this.audioOptions),h),this.assertCurrent(h),e=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(h),s=!0,this.outbound=!0,await this.attachAudio(e,h),e.call_id}catch(m){if(e&&(this.current(h)||!s||this.disposed))await this.recoverStartup(e,h,()=>this.client.hangup(e.call_id));if(this.current(h))this.update({detail:A(m)});throw m}finally{this.finish(h)}}async answer(t,h={}){let e=this.begin(!0),s,m=!1;try{this.assertCurrent(e),s=await this.client.answer(t,h),this.assertCurrent(e),m=!0,this.outbound=!1,await this.attachAudio(s,e)}catch(a){if(s&&(this.current(e)||!m||this.disposed))await this.recoverStartup(s,e,()=>this.client.release(s));if(this.current(e))this.update({detail:A(a)});throw a}finally{this.finish(e)}}async recoverStartup(t,h,e){try{if(await e(),this.current(h))this.clearCall()}catch{if(this.current(h))this.session=t,this.update({callId:t.call_id,audioState:"error"}),this.startPolling()}}async join(t){await this.answer(t,{rejoin:!0})}async attach(t){await this.acquireSession(t,!1)}async takeover(t){await this.acquireSession(t,!0)}async acquireSession(t,h){let e=this.begin(!0);try{await this.cancellable(this.runtime.preflight(this.audioOptions),e),this.assertCurrent(e);let s=await(h?this.client.takeover(t):this.client.attach(t));this.assertCurrent(e),await this.attachAudio(s,e),await this.reconcileAttachedCall(s,e)}catch(s){if(this.current(e))this.update({detail:A(s)});throw s}finally{this.finish(e)}}async reconnect(t){if(!this.session)throw Error("No call to reconnect");let h={...this.audioOptions,...t};w(h);let e=this.begin(!1);this.audioOptions=h;try{await this.attachAudio(this.session,e)}catch(s){if(this.current(e))this.update({detail:A(s)});throw s}finally{this.finish(e)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let t=this.session;if(!t){this.cancellation.abort();return}let h=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let e=(async()=>{try{if(await this.client.hangup(t.call_id),this.current(h))this.clearCall()}catch(s){if(this.current(h))this.update({audioState:"error",detail:A(s)}),this.startPolling();throw s}finally{this.finish(h)}})();this.hangingUp=e;try{await e}finally{if(this.hangingUp===e)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(t){this.assertOpen();let h=this.session;if(!h)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let e=this.generation;this.update({busy:!0,detail:void 0});let s;try{s=this.client[t](h.call_id)}catch(m){if(this.current(e)&&this.session===h)this.update({busy:!1,detail:A(m)});throw m}this.controlPending=s;try{let m=await s;if(this.current(e)&&this.session===h)this.update({holdState:m.hold_state,recordingState:m.recording_state,controlError:m.control_error,capabilities:m.capabilities});return m}catch(m){if(this.current(e)&&this.session===h)this.update({detail:A(m)});throw m}finally{if(this.controlPending===s)this.controlPending=void 0;if(this.current(e)&&this.session===h)this.update({busy:!1})}}configureAudio(t){this.assertOpen();let h={...this.audioOptions,...t};w(h),this.audioOptions=h}setMuted(t){this.assertOpen(),this.audio?.setMuted(t),this.update({muted:t})}setOutputVolume(t){if(this.assertOpen(),!Number.isFinite(t)||t<0||t>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=t,this.audio?.setOutputVolume(t)}sendDTMF(t){if(this.assertOpen(),!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(t)}observeCall(t){if(this.disposed||t.id!==this.session?.call_id)return;if(t.direction)this.outbound=t.direction==="outbound";if(Q(t.status)){this.invalidate(),this.clearCall(t.status),this.update({busy:!1,phase:"ended",termination:t.termination,answeredBy:t.answered_by??this.snapshot.answeredBy,endedAt:t.ended_at});return}this.update({carrierStatus:t.status,phase:v(t.status),answeredBy:t.answered_by??this.snapshot.answeredBy,holdState:t.hold_state??this.snapshot.holdState,recordingState:t.recording_state??this.snapshot.recordingState,controlError:t.control_error??this.snapshot.controlError,capabilities:t.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let t=this.options.ringback;return typeof t==="object"&&t?t.country:void 0}syncRingback(){let t=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(t&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!t&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(t,h){this.assertCurrent(h),this.stopAudio(),this.session=t,this.update({callId:t.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let e,s=()=>!this.disposed&&e!==void 0&&this.audio===e,m=(a)=>{if(s())try{a()}catch{}};try{if(this.assertCurrent(h),e=this.runtime.create({onState:(a,f)=>{if(!s())return;if(a==="ended"&&f==="call.ended"){this.observeCall({id:t.call_id,status:"completed"});return}if(this.update({audioState:a,detail:f}),s()&&(a==="error"||a==="ended")){if(this.stopAudio(),a==="error")this.reconcileFailedAudio(t,h)}},onLevels:(a,f)=>m(()=>this.options.onLevels?.(a,f)),onDiagnostics:(a)=>m(()=>this.options.onDiagnostics?.(a)),onNotice:(a)=>m(()=>this.options.onNotice?.(a)),onCallStatus:(a)=>m(()=>this.observeCall({id:a.call_id,status:a.status,direction:a.direction,answered_at:a.answered_at,ended_at:a.ended_at,answered_by:a.answered_by,termination:a.termination,hold_state:a.hold_state,recording_state:a.recording_state,control_error:a.control_error}))}),this.audio=e,this.assertCurrent(h),this.startPolling(),this.startLease(t),e.setMuted(this.snapshot.muted),await this.cancellable(e.start(this.client.mediaURL(t),this.audioOptions),h),this.assertCurrent(h),!s())throw Error(this.snapshot.detail||"Audio connection ended during setup");e.setMuted(this.snapshot.muted),this.syncRingback()}catch(a){if(s())this.stopAudio();if(this.current(h))this.update({audioState:"error",detail:A(a)});throw a}}async reconcileAttachedCall(t,h){try{let e=await this.client.getCall(t.call_id);if(this.current(h)&&this.session===t&&e)this.observeCall(e)}catch{}}async reconcileFailedAudio(t,h){try{let e=await this.client.getCall(t.call_id);if(!this.current(h)||this.session!==t||!e)return;if(e.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(e)}catch{}}startLease(t){let h=++this.leaseGeneration;if(!t.lease_seconds)return;let e=async()=>{if(this.disposed||this.session!==t||h!==this.leaseGeneration)return;try{if(await this.client.renew(t),h===this.leaseGeneration)this.leaseTimer=setTimeout(e,t.lease_seconds*1000/3)}catch(s){if(h!==this.leaseGeneration)return;this.stopAudio(),this.update({audioState:"error",detail:`Audio authorization ended: ${A(s)}`})}};this.leaseTimer=setTimeout(e,t.lease_seconds*1000/3)}stopAudio(){++this.leaseGeneration,clearTimeout(this.leaseTimer),this.leaseTimer=void 0;let t=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{t?.stopRingback?.()}catch{}}try{t?.stop()}catch{}if(t)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let t=new AbortController;this.polling=t;let h=async()=>{let e=this.session?.call_id;if(!e||t.signal.aborted)return;try{let s=await this.client.getCall(e,t.signal);if(!t.signal.aborted&&s)this.observeCall(s)}catch(s){if(!t.signal.aborted)this.update({detail:`Call status unavailable: ${A(s)}`})}finally{if(!t.signal.aborted&&this.session&&!this.disposed)this.timer=setTimeout(h,this.interval)}};this.timer=setTimeout(h,this.interval)}clearCall(t){this.stopAudio(),this.stopPolling(),this.session=void 0,this.outbound=!1,this.update({callId:void 0,carrierStatus:t,audioState:"idle",muted:!1,detail:void 0,phase:t?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}class L extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function o(t){return t.direction==="inbound"&&t.status==="pending"&&t.answerable!==!1&&!t.routing_waiting&&(t.peer_kind==="human"||Boolean(t.ring_offers?.some((h)=>h.kind==="browser")))}var Q=(t)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(t);function p(t){if(!t||/[\s/\\?#]/.test(t))throw Error("Invalid call ID");return encodeURIComponent(t)}class W{app;options;listMicrophones=z;createMicrophonePreview=(t)=>U(t,this.app);constructor(t,h={}){this.app=t;this.options=h;if(t.name!=="telephony"||!t.projectId||!t.installId)throw Error("Telephony requires an explicit project and installation")}path(t){if(!this.options.authProvider)return t;return`/user${t}${t.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(t){let h=await this.app.get(this.path("/calls"),{signal:t});if(!Array.isArray(h?.calls)||h.calls.some((e)=>!e||typeof e.id!=="string"||typeof e.status!=="string"))throw Error("Invalid Telephony calls response");return h.calls}async getCall(t,h){let e=await this.app.get(this.path(`/calls?call_id=${p(t)}`),{signal:h});if(!Array.isArray(e?.calls))throw Error("Invalid Telephony call response");return e.calls.find((s)=>s.id===t)}incomingCalls(t){return t.filter(o)}watchCalls(t,h={}){let e=h.intervalMs??2000;if(!Number.isFinite(e)||e<100)throw Error("Call watch interval must be at least 100 ms");let s=new AbortController,m,a,f=!1,u=!1,M=new Set,y=()=>{clearTimeout(m),s.abort(),a?.close(),h.signal?.removeEventListener("abort",y)},q=(c)=>{try{h.onError?.(c)}catch{y()}};if(h.signal?.addEventListener("abort",y,{once:!0}),h.signal?.aborted)y();let i=async(c)=>{if(s.signal.aborted)return;if(f){u=!0;return}clearTimeout(m),f=!0;let S=performance.now();try{let k=await this.listCalls(s.signal);if(!s.signal.aborted){t(k);for(let P of k){if(P.answerable!==!0||P.status!=="pending")continue;for(let b of P.ring_offers??[]){if(b.kind!=="browser"||!b.id||M.has(b.id))continue;M.add(b.id),this.acknowledgeOffer(P.id,b.id).catch((n)=>{if(![404,405].includes(n?.status??0))M.delete(b.id)})}}try{h.onTiming?.({trigger:c,fetchMs:performance.now()-S})}catch{}}}catch(k){if(!s.signal.aborted){let P=k?.status;try{h.onFailure?.({trigger:c,fetchMs:performance.now()-S,status:typeof P==="number"?P:void 0,error:k})}catch{}q(k)}}finally{if(f=!1,!s.signal.aborted)if(u)u=!1,i("push");else m=setTimeout(()=>void i("poll"),e)}};if(i("poll"),!s.signal.aborted&&h.push!==!1&&typeof this.app.subscribe==="function")try{a=this.app.subscribe(this.path("/calls/events"),(c)=>{if(c.type==="calls.changed")i("push");else if(c.type==="access.revoked")a?.close(),i("push")},{transport:"fetch",signal:s.signal,reconnectDelayMs:250,onError:(c)=>{let S=c?.status;if(S===404||S===405||S===501)a?.close()}})}catch{}return{close:y}}async place(t){if(!/^\+[1-9]\d{7,14}$/.test(t.to)||!t.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");return this.session(await this.app.post(this.path("/softphone/place"),t))}async answer(t,h={}){try{return this.session(await this.app.post(this.path(`/softphone/answer/${p(t)}`),h),t)}catch(e){let s=e;if(s.status===409&&typeof s.body==="string"){let m;try{m=JSON.parse(s.body)}catch{}if(m?.code==="offer_expired")throw new L}throw e}}async acknowledgeOffer(t,h){await this.app.post(this.path(`/softphone/offer/ack/${p(t)}`),{offer_id:h})}async declineOffer(t,h){await this.app.post(this.path(`/softphone/offer/decline/${p(t)}`),{offer_id:h})}async attach(t){return this.session(await this.app.post(this.path(`/softphone/attach/${p(t)}`),{}),t)}async takeover(t){return this.session(await this.app.post(this.path(`/softphone/takeover/${p(t)}`),{}),t)}async renew(t){await this.app.post(this.path(`/softphone/renew/${p(t.call_id)}`),{session_token:t.session_token})}async release(t){if(!t.session_token)throw Error("Answer session has no release token");await this.app.post(this.path(`/softphone/release/${p(t.call_id)}`),{session_token:t.session_token})}async hangup(t){await this.app.post(this.path(`/calls/${p(t)}/hangup`),{})}hold(t){return this.app.post(this.path(`/calls/${p(t)}/hold`),{})}resume(t){return this.app.post(this.path(`/calls/${p(t)}/resume`),{})}pauseRecording(t){return this.app.post(this.path(`/calls/${p(t)}/pause-recording`),{})}resumeRecording(t){return this.app.post(this.path(`/calls/${p(t)}/resume-recording`),{})}createSoftphone(t={}){return new O(this,t)}mediaURL(t){let h=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),e=new URL(t.media_url,h),s=`/api/apps/telephony/_install/${this.app.installId}/softphone/media/${p(t.call_id)}/`;if(e.origin!==h.origin||!["http:","https:"].includes(e.protocol)||e.username||e.password||e.search||e.hash||!e.pathname.startsWith(s)||!/^[A-Za-z0-9_-]+$/.test(e.pathname.slice(s.length)))throw Error("Invalid Telephony media endpoint");return e.protocol=e.protocol==="https:"?"wss:":"ws:",e.href}session(t,h){let e=t;if(!e||typeof e.call_id!=="string"||typeof e.media_url!=="string"||h&&e.call_id!==h||e.session_token!==void 0&&typeof e.session_token!=="string")throw Error("Invalid Telephony session response");if(e.lease_seconds!==void 0&&(!Number.isFinite(e.lease_seconds)||e.lease_seconds<10||e.lease_seconds>3600||!e.session_token))throw Error("Invalid media lease");return p(e.call_id),this.mediaURL(e),e}}var Zt=Y({app:"telephony",create:({app:t})=>new W(t)});function zt({app:t},h){return new W(t,h)}export{zt as createClient};
