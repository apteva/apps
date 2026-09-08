function k(t){return t}function x(t){let e=new Int16Array(t.length);for(let s=0;s<t.length;s++){let a=Math.max(-1,Math.min(1,t[s]));e[s]=a<0?a*32768:a*32767}return e.buffer}class q{history=new Float32Array(64);phase=0;process(t,e,s){if(e===s)return t;let a=new Float32Array(64+t.length);a.set(this.history),a.set(t,64);let h=e/s,i=Math.min(1,s/e)*0.9,u=[],n=this.phase;for(;n<t.length;n+=h){let r=n+32,m=0,c=0;for(let p=Math.ceil(r-32);p<=Math.floor(r+32);p++){let M=p-r,T=(Math.abs(M)<0.000000001?i:Math.sin(Math.PI*i*M)/(Math.PI*M))*(0.5+0.5*Math.cos(Math.PI*M/32));if(p>=0&&p<a.length)m+=a[p]*T,c+=T}u.push(c?m/c:0)}return this.phase=n-t.length,this.history=a.slice(-64),new Float32Array(u)}}function w(t){let e=0;for(let s=0;s<t.length;s++)e+=t[s]*t[s];return Math.sqrt(e/Math.max(1,t.length))}var d={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:-6,highpassFilter:!0};function y(t){return{...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{},echoCancellation:t.echoCancellation,noiseSuppression:t.noiseSuppression,autoGainControl:t.autoGainControl}}function P(t){let e=t.getSettings();return{deviceLabel:t.label||"Default microphone",sampleRate:typeof e.sampleRate==="number"?e.sampleRate:null,channelCount:typeof e.channelCount==="number"?e.channelCount:null,echoCancellation:typeof e.echoCancellation==="boolean"?e.echoCancellation:null,noiseSuppression:typeof e.noiseSuppression==="boolean"?e.noiseSuppression:null,autoGainControl:typeof e.autoGainControl==="boolean"?e.autoGainControl:null}}function f(t){if(!Number.isFinite(t)||t<=0)return null;return 20*Math.log10(t)}function B(t,e,s){let a=new ArrayBuffer(44+e*2),h=new DataView(a),i=(n,r)=>{for(let m=0;m<r.length;m++)h.setUint8(n+m,r.charCodeAt(m))};i(0,"RIFF"),h.setUint32(4,36+e*2,!0),i(8,"WAVE"),i(12,"fmt "),h.setUint32(16,16,!0),h.setUint16(20,1,!0),h.setUint16(22,1,!0),h.setUint32(24,s,!0),h.setUint32(28,s*2,!0),h.setUint16(32,2,!0),h.setUint16(34,16,!0),i(36,"data"),h.setUint32(40,e*2,!0);let u=44;for(let n of t)for(let r=0;r<n.length;r++)h.setInt16(u,n[r],!0),u+=2;return new Blob([a],{type:"audio/wav"})}class O{onLevel;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new q;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(t){this.onLevel=t}async start(t,e){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:y(e)}),this.ensureOpen();let s=this.stream.getAudioTracks()[0];if(!s)throw Error("No microphone audio track was returned.");this.settings=P(s);try{this.ctx=new AudioContext({sampleRate:24000,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await this.ctx.audioWorklet.addModule(t),this.ensureOpen();let a=this.ctx.sampleRate,h=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:e.inputGainDB,highpassFilter:e.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(i)=>{if(this.stopped)return;if(!(i.data instanceof Float32Array)){if(i.data.type==="capture.stats")this.peak=Math.max(this.peak,i.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,i.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,i.data.limiter_reduction_db??0);return}let u=i.data;if(a!==24000)u=this.resampler.process(u,a,24000);let n=w(u);this.onLevel?.(n);let r=new Int16Array(x(u));if(this.frames.push(r),this.samples+=r.length,n>=0.005){for(let m=0;m<u.length;m++)this.activeSquares+=u[m]*u[m];this.activeSamples+=u.length}},h.connect(this.capture),this.settings}catch(s){throw await this.release(),s}}async stop(){this.stopped=!0;let t=this.settings,e=this.frames,s=this.samples,a=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,h=this.peak;if(await this.release(),!t||s===0)throw Error("No microphone audio was captured.");return{audio:B(e,s,24000),durationMs:Math.round(s*1000/24000),sampleRate:24000,activeRmsDbfs:f(a),peakDbfs:f(h),postPeakDbfs:f(this.postPeak||h),limiterReductionDb:this.limiterReductionDB,settings:t}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let t of this.stream?.getTracks()??[])t.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class A{callbacks;worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;opened=!1;cancelWorkerStart;microphoneTransportReady=!1;diagnostics={rttMs:null,queueMs:0,targetMs:80,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:24000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:d.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t={}){this.callbacks=t}get isMuted(){return this.muted}async start(t,e,s,a=d){this.callbacks.onState?.("connecting");try{this.stream=await navigator.mediaDevices.getUserMedia({audio:y(a)}),this.ensureOpen();let h=this.stream.getAudioTracks()[0];if(!h)throw Error("No microphone audio track was returned.");if(h.readyState==="ended")throw Error("Microphone disconnected before audio setup.");h.onmute=()=>this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser."),h.onunmute=()=>this.callbacks.onNotice?.("Microphone input restored."),h.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let i=P(h);this.diagnostics={...this.diagnostics,microphoneSampleRate:i.sampleRate??0,microphoneChannelCount:i.channelCount??0,echoCancellation:i.echoCancellation,noiseSuppression:i.noiseSuppression,autoGainControl:i.autoGainControl,micInputGainDb:a.inputGainDB};try{this.ctx=new AudioContext({sampleRate:24000,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();if(this.ensureOpen(),a.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(a.outputDeviceId),this.ensureOpen();await this.ctx.audioWorklet.addModule(e),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let u=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:a.inputGainDB,highpassFilter:a.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:{initialTargetMs:80,minTargetMs:60,maxTargetMs:160,hardMaxMs:320}}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),u.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=a.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await this.openWorker(t,s),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running"&&this.microphoneTransportReady)this.callbacks.onState?.("live")},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(h){if(this.closed)this.teardown();if(!this.closed)this.fail(h instanceof Error?h.message:"browser audio setup failed");throw h}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){if(await this.ctx?.resume(),this.microphoneTransportReady)this.callbacks.onState?.("live")}setOutputVolume(t){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,t))}sendDTMF(t){if(/^[0-9*#]+$/.test(t))this.sendText(JSON.stringify({type:"dtmf",digits:t}))}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="capture.stats")return;this.micLevel=this.muted?0:e.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:f(e.active_rms??0),micPeakDbfs:f(e.pre_peak??0),micPostPeakDbfs:f(e.post_peak??0),micInputGainDb:e.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:e.limiter_reduction_db??0}},this.playback.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="stats")return;this.speakerLevel=Math.max(this.speakerLevel,e.speaker_level??0),this.diagnostics={...this.diagnostics,queueMs:e.queue_ms??0,targetMs:e.target_ms??80,underruns:e.underruns??0,droppedMs:e.dropped_ms??0,maxQueueMs:e.max_queue_ms??0,playbackSequenceGaps:e.playback_sequence_gaps??0,dropEvents:[...this.diagnostics.dropEvents.filter((s)=>s.direction!=="carrier_to_operator"),...e.drop_events??[]].slice(-100)},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(t,e){return new Promise((s,a)=>{let h=new Worker(e);this.worker=h;let i=new MessageChannel,u=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:i.port1},[i.port1]),this.playback?.port.postMessage({type:"transport",port:u.port1},[u.port1]);let n=!1,r=setTimeout(()=>{if(n)return;n=!0,a(Error("audio connection timed out"))},1e4),m=(c)=>{if(n)return;if(n=!0,clearTimeout(r),c)a(c);else s()};this.cancelWorkerStart=()=>m(Error("audio session closed")),h.onmessage=(c)=>{if(this.closed){m(Error("audio session closed"));return}let p=c.data;if(p?.type==="socket.open")this.opened=!0,this.startRTTProbe(),m();else if(p?.type==="socket.message")this.handleControl(p.data);else if(p?.type==="socket.close")if(this.stopRTTProbe(),this.microphoneTransportReady=!1,this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else m(Error("audio connection closed before it was ready"));else if(p?.type==="socket.failed")m(Error(p.detail||"audio connection lost")),this.fail(p.detail||"audio connection lost");else if(p?.type==="transport.drop"&&p.event)this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,p.event].slice(-100);else if(p?.type==="transport.stats")this.diagnostics.websocketBufferedBytes=p.buffered_bytes??0},h.onerror=()=>{if(m(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},h.postMessage({type:"init",mediaURL:t,contextRate:this.ctx?.sampleRate??24000,muted:this.muted,capturePort:i.port2,playbackPort:u.port2},[i.port2,u.port2])})}handleControl(t){if(this.closed)return;try{let e=JSON.parse(t);if(e.type==="dtmf.error"||e.type==="dtmf.sent")this.callbacks.onNotice?.(e.type==="dtmf.sent"?"Keypad tone sent":e.detail||"Keypad tone failed");else if(e.type==="pong"&&typeof e.nonce==="number"&&e.nonce>=0)this.diagnostics.captureSequenceGaps=e.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-e.nonce)),this.callbacks.onDiagnostics?.({...this.diagnostics});else if(e.type==="call.ended"||e.type==="session.replaced"){this.closed=!0;try{this.callbacks.onState?.("ended",e.type)}finally{this.teardown()}}else if(e.type==="call.error")this.fail(e.detail||"The call could not be connected.");else if(e.type==="peer.disconnected")this.microphoneTransportReady=!1,this.worker?.postMessage({type:"microphone.ready",value:!1}),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(e.type==="peer.connected")this.microphoneTransportReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.callbacks.onState?.("live",this.opened?void 0:"Audio reconnected")}catch{}}startRTTProbe(){this.stopRTTProbe();let t=()=>{this.sendText(JSON.stringify({type:"ping",nonce:performance.now()})),this.sendDiagnostics()};t(),this.pingTimer=setInterval(t,5000)}sendText(t){this.worker?.postMessage({type:"send.text",data:t})}sendDiagnostics(){let t=this.diagnostics;this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{rtt_ms:t.rttMs,playback_queue_ms:t.queueMs,playback_target_ms:t.targetMs,playback_max_queue_ms:t.maxQueueMs,playback_underruns:t.underruns,playback_dropped_ms:t.droppedMs,websocket_buffered_bytes:t.websocketBufferedBytes,audio_context_rate:t.audioContextRate,microphone_sample_rate:t.microphoneSampleRate,microphone_channel_count:t.microphoneChannelCount,echo_cancellation:t.echoCancellation,noise_suppression:t.noiseSuppression,auto_gain_control:t.autoGainControl,mic_active_rms_dbfs:t.micActiveRmsDbfs,mic_peak_dbfs:t.micPeakDbfs,mic_post_peak_dbfs:t.micPostPeakDbfs,mic_input_gain_db:t.micInputGainDb,mic_limiter_reduction_db:t.micLimiterReductionDb,capture_sequence_gaps:t.captureSequenceGaps,playback_sequence_gaps:t.playbackSequenceGaps,drop_events:t.dropEvents}})),this.callbacks.onDiagnostics?.({...t})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setMuted(t){if(this.muted=t,this.worker?.postMessage({type:"muted",value:t}),this.capture?.port.postMessage({type:"muted",value:t}),t)this.sendText(JSON.stringify({type:"interrupt"}))}stop(){let t=!this.closed;this.closed=!0;try{if(t)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(t){this.closed=!0;try{this.callbacks.onState?.("error",t)}finally{this.teardown()}}teardown(){this.microphoneTransportReady=!1,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let t=this.worker;if(t?.postMessage({type:"close"}),t)setTimeout(()=>t.terminate(),100);this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((e)=>e.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var b=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
const CROSSFADE_MS = 5;

class SoftphoneCaptureProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const config = options.processorOptions || {};
    this.inputGain = 10 ** ((config.inputGainDB ?? -6) / 20);
    this.inputGainDB = config.inputGainDB ?? -6;
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
    this.initialTargetMs = config.initialTargetMs || 80;
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
`;var E=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
`;var g={async preflight(t){(await navigator.mediaDevices.getUserMedia({audio:y(t)})).getTracks().forEach((s)=>s.stop())},create(t){let e=new A(t),s=[],a=()=>{try{e.stop()}finally{s.splice(0).forEach((h)=>URL.revokeObjectURL(h))}};return{async start(h,i){try{for(let u of[b,E])s.push(URL.createObjectURL(new Blob([u],{type:"text/javascript"})));await e.start(h,s[0],s[1],i)}catch(u){throw a(),u}},stop:a,setMuted:(h)=>e.setMuted(h),sendDTMF:(h)=>e.sendDTMF(h),setOutputVolume:(h)=>e.setOutputVolume(h)}}};var l=(t)=>t instanceof Error?t.message:String(t);class _{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;intent;timer;polling;runtime;audioOptions;interval;constructor(t,e={}){this.client=t;this.options=e;if(this.runtime=e.audioRuntime??g,this.audioOptions={...d,...e.audio},this.interval=e.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.assertOpen(),this.listeners.add(t),()=>{this.listeners.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let e of this.listeners)try{e(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(t){if(this.disposed||t!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(t){return!this.disposed&&t===this.generation}finish(t){if(this.current(t))this.update({busy:!1})}cancellable(t,e){let s=this.cancellation.signal;return new Promise((a,h)=>{let i=()=>{s.removeEventListener("abort",i),h(Error("Softphone operation cancelled"))};if(s.addEventListener("abort",i,{once:!0}),t.then(a,h).finally(()=>s.removeEventListener("abort",i)),!this.current(e)||s.aborted)i()})}begin(t){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||t&&this.session)throw Error("Softphone already has an active operation or call");let e=this.invalidate();return this.update({busy:!0,detail:void 0}),e}async dial(t){let e=this.begin(!0),s,a=!1;try{this.assertCurrent(e);let h={to:t.to.trim(),from:t.from?.trim(),timeout_sec:t.timeout_sec,recording:t.recording},i=JSON.stringify(h);if(!this.intent||this.intent.value!==i||t.idempotency_key&&t.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:i,request:{...h,idempotency_key:t.idempotency_key||crypto.randomUUID()}};return await this.cancellable(this.runtime.preflight(this.audioOptions),e),this.assertCurrent(e),s=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(e),a=!0,await this.attach(s,e),s.call_id}catch(h){if(s&&(this.current(e)||!a||this.disposed))await this.recoverStartup(s,e,()=>this.client.hangup(s.call_id));if(this.current(e))this.update({detail:l(h)});throw h}finally{this.finish(e)}}async answer(t,e={}){let s=this.begin(!0),a,h=!1;try{this.assertCurrent(s),a=await this.client.answer(t,e),this.assertCurrent(s),h=!0,await this.attach(a,s)}catch(i){if(a&&(this.current(s)||!h||this.disposed))await this.recoverStartup(a,s,()=>this.client.release(a));if(this.current(s))this.update({detail:l(i)});throw i}finally{this.finish(s)}}async recoverStartup(t,e,s){try{if(await s(),this.current(e))this.clearCall()}catch{if(this.current(e))this.session=t,this.update({callId:t.call_id,audioState:"error"}),this.startPolling()}}async join(t){await this.answer(t,{rejoin:!0})}async reconnect(t){if(!this.session)throw Error("No call to reconnect");let e=this.begin(!1);if(t)this.audioOptions={...this.audioOptions,...t};try{await this.attach(this.session,e)}catch(s){if(this.current(e))this.update({detail:l(s)});throw s}finally{this.finish(e)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let t=this.session;if(!t){this.cancellation.abort();return}let e=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let s=(async()=>{try{if(await this.client.hangup(t.call_id),this.current(e))this.clearCall()}catch(a){if(this.current(e))this.update({audioState:"error",detail:l(a)}),this.startPolling();throw a}finally{this.finish(e)}})();this.hangingUp=s;try{await s}finally{if(this.hangingUp===s)this.hangingUp=void 0}}configureAudio(t){this.assertOpen(),this.audioOptions={...this.audioOptions,...t}}setMuted(t){this.assertOpen(),this.audio?.setMuted(t),this.update({muted:t})}setOutputVolume(t){if(this.assertOpen(),!Number.isFinite(t)||t<0||t>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=t,this.audio?.setOutputVolume(t)}sendDTMF(t){if(this.assertOpen(),!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(t)}observeCall(t){if(this.disposed||t.id!==this.session?.call_id)return;if(R(t.status))this.invalidate(),this.clearCall(t.status),this.update({busy:!1});else this.update({carrierStatus:t.status})}async attach(t,e){this.assertCurrent(e),this.stopAudio(),this.session=t,this.update({callId:t.call_id,carrierStatus:void 0,audioState:"connecting"});let s,a=()=>!this.disposed&&s!==void 0&&this.audio===s,h=(i)=>{if(a())try{i()}catch{}};try{if(this.assertCurrent(e),s=this.runtime.create({onState:(i,u)=>{if(!a())return;if(i==="ended"&&u==="call.ended"){this.observeCall({id:t.call_id,status:"completed"});return}if(this.update({audioState:i,detail:u}),a()&&(i==="error"||i==="ended")){if(this.stopAudio(),i==="error")this.reconcileFailedAudio(t,e)}},onLevels:(i,u)=>h(()=>this.options.onLevels?.(i,u)),onDiagnostics:(i)=>h(()=>this.options.onDiagnostics?.(i)),onNotice:(i)=>h(()=>this.options.onNotice?.(i))}),this.audio=s,this.assertCurrent(e),this.startPolling(),s.setMuted(this.snapshot.muted),await this.cancellable(s.start(this.client.mediaURL(t),this.audioOptions),e),this.assertCurrent(e),!a())throw Error(this.snapshot.detail||"Audio connection ended during setup");s.setMuted(this.snapshot.muted)}catch(i){if(a())this.stopAudio();if(this.current(e))this.update({audioState:"error",detail:l(i)});throw i}}async reconcileFailedAudio(t,e){try{let s=await this.client.getCall(t.call_id);if(!this.current(e)||this.session!==t||!s)return;if(s.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(s)}catch{}}stopAudio(){let t=this.audio;this.audio=void 0;try{t?.stop()}catch{}if(t)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let t=new AbortController;this.polling=t;let e=async()=>{let s=this.session?.call_id;if(!s||t.signal.aborted)return;try{let a=await this.client.getCall(s,t.signal);if(!t.signal.aborted&&a)this.observeCall(a)}catch(a){if(!t.signal.aborted)this.update({detail:`Call status unavailable: ${l(a)}`})}finally{if(!t.signal.aborted&&this.session&&!this.disposed)this.timer=setTimeout(e,this.interval)}};this.timer=setTimeout(e,this.interval)}clearCall(t){this.stopAudio(),this.stopPolling(),this.session=void 0,this.update({callId:void 0,carrierStatus:t,audioState:"idle",muted:!1,detail:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function W(t){return t.direction==="inbound"&&t.status==="pending"&&!t.routing_waiting&&(t.peer_kind==="human"||Boolean(t.ring_offers?.some((e)=>e.kind==="browser")))}var R=(t)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(t);function o(t){if(!t||/[\s/\\?#]/.test(t))throw Error("Invalid call ID");return encodeURIComponent(t)}class S{app;constructor(t){this.app=t;if(t.name!=="telephony"||!t.projectId||!t.installId)throw Error("Telephony requires an explicit project and installation")}async listCalls(t){let e=await this.app.get("/calls",{signal:t});if(!Array.isArray(e?.calls)||e.calls.some((s)=>!s||typeof s.id!=="string"||typeof s.status!=="string"))throw Error("Invalid Telephony calls response");return e.calls}async getCall(t,e){let s=await this.app.get(`/calls?call_id=${o(t)}`,{signal:e});if(!Array.isArray(s?.calls))throw Error("Invalid Telephony call response");return s.calls.find((a)=>a.id===t)}incomingCalls(t){return t.filter(W)}watchCalls(t,e={}){let s=e.intervalMs??2000;if(!Number.isFinite(s)||s<100)throw Error("Call watch interval must be at least 100 ms");let a=new AbortController,h,i=()=>{clearTimeout(h),a.abort(),e.signal?.removeEventListener("abort",i)};if(e.signal?.addEventListener("abort",i,{once:!0}),e.signal?.aborted)i();let u=async()=>{if(a.signal.aborted)return;try{let n=await this.listCalls(a.signal);if(!a.signal.aborted)t(n)}catch(n){if(!a.signal.aborted)try{e.onError?.(n)}catch{i()}}finally{if(!a.signal.aborted)h=setTimeout(u,s)}};return u(),{close:i}}async place(t){if(!/^\+[1-9]\d{7,14}$/.test(t.to)||!t.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");return this.session(await this.app.post("/softphone/place",t))}async answer(t,e={}){return this.session(await this.app.post(`/softphone/answer/${o(t)}`,e),t)}async release(t){if(!t.session_token)throw Error("Answer session has no release token");await this.app.post(`/softphone/release/${o(t.call_id)}`,{session_token:t.session_token})}async hangup(t){await this.app.post(`/calls/${o(t)}/hangup`,{})}createSoftphone(t={}){return new _(this,t)}mediaURL(t){let e=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),s=new URL(t.media_url,e),a=`/api/apps/telephony/_install/${this.app.installId}/softphone/media/${o(t.call_id)}/`;if(s.origin!==e.origin||!["http:","https:"].includes(s.protocol)||s.username||s.password||s.search||s.hash||!s.pathname.startsWith(a)||!/^[A-Za-z0-9_-]+$/.test(s.pathname.slice(a.length)))throw Error("Invalid Telephony media endpoint");return s.protocol=s.protocol==="https:"?"wss:":"ws:",s.href}session(t,e){let s=t;if(!s||typeof s.call_id!=="string"||typeof s.media_url!=="string"||e&&s.call_id!==e||s.session_token!==void 0&&typeof s.session_token!=="string")throw Error("Invalid Telephony session response");return o(s.call_id),this.mediaURL(s),s}}var Mt=k({app:"telephony",create:({app:t})=>new S(t)});function St({app:t}){return new S(t)}export{St as createClient};
