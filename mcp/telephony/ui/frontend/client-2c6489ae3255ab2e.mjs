function P(t){return t}var B=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function N(t){let e=(t??"FR").trim().toUpperCase();return B[e]??B.FR}function J(t,e){let a=[];if(!(t.cadence.reduce((h,i)=>h+i,0)>0)||!(e>0))return a;let m=0;while(m<e)for(let h=0;h<t.cadence.length;h+=2){let i=t.cadence[h]??0,c=t.cadence[h+1]??0;if(m>=e)break;if(i>0)a.push({start:m,end:Math.min(m+i,e)});m+=i+c}return a}function V(t,e,a,s=0.12){let m=t.createGain();m.gain.value=0,m.connect(e);let h=s/Math.max(1,a.frequencies.length),i=a.frequencies.map((l)=>{let p=t.createOscillator();return p.type="sine",p.frequency.value=l,p.connect(m),p.start(),p}),c=t.currentTime+0.05,n=0,f=0.01,o=()=>{let l=t.currentTime-c+10;if(l<=n)return;for(let p of J(a,l)){if(p.end<=n)continue;let M=c+Math.max(p.start,n),S=c+p.end;m.gain.setValueAtTime(0,M),m.gain.linearRampToValueAtTime(h,M+f),m.gain.setValueAtTime(h,Math.max(M+f,S-f)),m.gain.linearRampToValueAtTime(0,S)}n=l};o();let r=setInterval(o,4000),u=!1;return()=>{if(u)return;u=!0,clearInterval(r);try{m.gain.cancelScheduledValues(0)}catch{}m.gain.value=0;for(let l of i){try{l.stop()}catch{}l.disconnect()}m.disconnect()}}var k=24000,A=60;async function W(t,e){try{await t.audioWorklet.addModule(e)}catch(a){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:a})}}function L(t){let e=new Int16Array(t.length);for(let a=0;a<t.length;a++){let s=Math.max(-1,Math.min(1,t[a]));e[a]=s<0?s*32768:s*32767}return e.buffer}class X{history=new Float32Array(64);phase=0;process(t,e,a){if(e===a)return t;let s=new Float32Array(64+t.length);s.set(this.history),s.set(t,64);let m=e/a,h=Math.min(1,a/e)*0.9,i=[],c=this.phase;for(;c<t.length;c+=m){let n=c+32,f=0,o=0;for(let r=Math.ceil(n-32);r<=Math.floor(n+32);r++){let u=r-n,p=(Math.abs(u)<0.000000001?h:Math.sin(Math.PI*h*u)/(Math.PI*u))*(0.5+0.5*Math.cos(Math.PI*u/32));if(r>=0&&r<s.length)f+=s[r]*p,o+=p}i.push(o?f/o:0)}return this.phase=c-t.length,this.history=s.slice(-64),new Float32Array(i)}}function Z(t){let e=0;for(let a=0;a<t.length;a++)e+=t[a]*t[a];return Math.sqrt(e/Math.max(1,t.length))}var d={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function q(t){let e=t.playbackTargetMs??A,a=t.playbackMinMs??Math.min(A,e),s=t.playbackMaxMs??160;for(let m of[e,a,s])if(!Number.isFinite(m)||m<40||m>160)throw RangeError("Playback buffers must be between 40 and 160 ms");if(a>e||e>s)throw RangeError("Playback buffers require min <= target <= max");return{initialTargetMs:e,minTargetMs:a,maxTargetMs:s,hardMaxMs:320}}function w(t){return{...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{},echoCancellation:t.echoCancellation,noiseSuppression:t.noiseSuppression,autoGainControl:t.autoGainControl}}function Y(t){let e=t.getSettings();return{deviceLabel:t.label||"Default microphone",sampleRate:typeof e.sampleRate==="number"?e.sampleRate:null,channelCount:typeof e.channelCount==="number"?e.channelCount:null,echoCancellation:typeof e.echoCancellation==="boolean"?e.echoCancellation:null,noiseSuppression:typeof e.noiseSuppression==="boolean"?e.noiseSuppression:null,autoGainControl:typeof e.autoGainControl==="boolean"?e.autoGainControl:null}}function b(t){if(!Number.isFinite(t)||t<=0)return null;return 20*Math.log10(t)}function K(t,e,a){let s=new ArrayBuffer(44+e*2),m=new DataView(s),h=(c,n)=>{for(let f=0;f<n.length;f++)m.setUint8(c+f,n.charCodeAt(f))};h(0,"RIFF"),m.setUint32(4,36+e*2,!0),h(8,"WAVE"),h(12,"fmt "),m.setUint32(16,16,!0),m.setUint16(20,1,!0),m.setUint16(22,1,!0),m.setUint32(24,a,!0),m.setUint32(28,a*2,!0),m.setUint16(32,2,!0),m.setUint16(34,16,!0),h(36,"data"),m.setUint32(40,e*2,!0);let i=44;for(let c of t)for(let n=0;n<c.length;n++)m.setInt16(i,c[n],!0),i+=2;return new Blob([s],{type:"audio/wav"})}class C{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new X;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(t,e=!0){this.onLevel=t;this.recordAudio=e}async start(t,e){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:w(e)}),this.ensureOpen();let a=this.stream.getAudioTracks()[0];if(!a)throw Error("No microphone audio track was returned.");this.settings=Y(a);try{this.ctx=new AudioContext({sampleRate:k,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await W(this.ctx,t),this.ensureOpen();let s=this.ctx.sampleRate,m=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:e.inputGainDB,highpassFilter:e.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(h)=>{if(this.stopped)return;if(!(h.data instanceof Float32Array)){if(h.data.type==="capture.stats")this.peak=Math.max(this.peak,h.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,h.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,h.data.limiter_reduction_db??0);return}let i=h.data;if(s!==k)i=this.resampler.process(i,s,k);let c=Z(i);if(this.onLevel?.(c),this.recordAudio)this.frames.push(new Int16Array(L(i)));if(this.samples+=i.length,c>=0.005){for(let n=0;n<i.length;n++)this.activeSquares+=i[n]*i[n];this.activeSamples+=i.length}},m.connect(this.capture),this.settings}catch(a){throw await this.release(),a}}async stop(){this.stopped=!0;let t=this.settings,e=this.frames,a=this.samples,s=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,m=this.peak;if(await this.release(),!t||a===0)throw Error("No microphone audio was captured.");return{audio:K(e,a,k),durationMs:Math.round(a*1000/k),sampleRate:k,activeRmsDbfs:b(s),peakDbfs:b(m),postPeakDbfs:b(this.postPeak||m),limiterReductionDb:this.limiterReductionDB,settings:t}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let t of this.stream?.getTracks()??[])t.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class R{callbacks;worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;opened=!1;cancelWorkerStart;microphoneTransportReady=!1;ringback=null;transportTiming={};playbackTiming={};diagnostics={rttMs:null,queueMs:0,targetMs:A,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:k,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:d.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t={}){this.callbacks=t}get isMuted(){return this.muted}async start(t,e,a,s=d){let m=q(s);this.diagnostics.targetMs=m.initialTargetMs,this.callbacks.onState?.("connecting");try{this.stream=await navigator.mediaDevices.getUserMedia({audio:w(s)}),this.ensureOpen();let h=this.stream.getAudioTracks()[0];if(!h)throw Error("No microphone audio track was returned.");if(h.readyState==="ended")throw Error("Microphone disconnected before audio setup.");h.onmute=()=>this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser."),h.onunmute=()=>this.callbacks.onNotice?.("Microphone input restored."),h.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let i=Y(h);this.diagnostics={...this.diagnostics,microphoneSampleRate:i.sampleRate??0,microphoneChannelCount:i.channelCount??0,echoCancellation:i.echoCancellation,noiseSuppression:i.noiseSuppression,autoGainControl:i.autoGainControl,micInputGainDb:s.inputGainDB};try{this.ctx=new AudioContext({sampleRate:k,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();if(this.ensureOpen(),s.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(s.outputDeviceId),this.ensureOpen();await W(this.ctx,e),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let c=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:m}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),c.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=s.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await this.openWorker(t,a),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running"&&this.microphoneTransportReady)this.callbacks.onState?.("live")},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(h){if(this.closed)this.teardown();if(!this.closed)this.fail(h instanceof Error?h.message:"browser audio setup failed");throw h}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){if(await this.ctx?.resume(),this.microphoneTransportReady)this.callbacks.onState?.("live")}setOutputVolume(t){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,t))}sendDTMF(t){if(/^[0-9*#]+$/.test(t))this.sendText(JSON.stringify({type:"dtmf",digits:t}))}startRingback(t){if(this.closed||!this.ctx||this.ringback)return;this.ringback=V(this.ctx,this.ctx.destination,N(t))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="capture.stats")return;this.micLevel=this.muted?0:e.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:b(e.active_rms??0),micPeakDbfs:b(e.pre_peak??0),micPostPeakDbfs:b(e.post_peak??0),micInputGainDb:e.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:e.limiter_reduction_db??0}},this.playback.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="stats")return;this.playbackTiming={played_ms:e.played_ms,max_residence_ms:e.max_residence_ms,drop_totals_ms:e.drop_totals_ms},this.speakerLevel=Math.max(this.speakerLevel,e.speaker_level??0),this.diagnostics={...this.diagnostics,queueMs:e.queue_ms??0,targetMs:e.target_ms??A,underruns:e.underruns??0,droppedMs:e.dropped_ms??0,maxQueueMs:e.max_queue_ms??0,playbackSequenceGaps:e.playback_sequence_gaps??0,dropEvents:[...this.diagnostics.dropEvents.filter((a)=>a.direction!=="carrier_to_operator"),...e.drop_events??[]].slice(-100)},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(t,e){return new Promise((a,s)=>{let m=new Worker(e);this.worker=m;let h=new MessageChannel,i=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:h.port1},[h.port1]),this.playback?.port.postMessage({type:"transport",port:i.port1},[i.port1]);let c=!1,n=setTimeout(()=>{if(c)return;c=!0,s(Error("audio connection timed out"))},1e4),f=(o)=>{if(c)return;if(c=!0,clearTimeout(n),o)s(o);else a()};this.cancelWorkerStart=()=>f(Error("audio session closed")),m.onmessage=(o)=>{if(this.closed){f(Error("audio session closed"));return}let r=o.data;if(r?.type==="socket.open")this.opened=!0,this.startRTTProbe(),f();else if(r?.type==="socket.message")this.handleControl(r.data);else if(r?.type==="socket.close")if(this.stopRTTProbe(),this.microphoneTransportReady=!1,this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else f(Error("audio connection closed before it was ready"));else if(r?.type==="socket.failed")f(Error(r.detail||"audio connection lost")),this.fail(r.detail||"audio connection lost");else if(r?.type==="transport.drop"&&r.event)this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,r.event].slice(-100);else if(r?.type==="transport.stats")this.diagnostics.websocketBufferedBytes=r.buffered_bytes??0,this.transportTiming=r.timing??{}},m.onerror=()=>{if(f(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},m.postMessage({type:"init",audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:t,contextRate:this.ctx?.sampleRate??k,muted:this.muted,capturePort:h.port2,playbackPort:i.port2},[h.port2,i.port2])})}handleControl(t){if(this.closed)return;try{let e=JSON.parse(t);if(e.type==="dtmf.error"||e.type==="dtmf.sent")this.callbacks.onNotice?.(e.type==="dtmf.sent"?"Keypad tone sent":e.detail||"Keypad tone failed");else if(e.type==="pong"&&typeof e.nonce==="number"&&e.nonce>=0)this.diagnostics.captureSequenceGaps=e.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-e.nonce)),this.callbacks.onDiagnostics?.({...this.diagnostics});else if(e.type==="call.ended"||e.type==="session.replaced"){this.closed=!0;try{this.callbacks.onState?.("ended",e.type)}finally{this.teardown()}}else if(e.type==="call.status"&&typeof e.call_id==="string"&&typeof e.status==="string"){if(e.hold_state&&e.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callbacks.onCallStatus?.(e)}else if(e.type==="call.error")this.fail(e.detail||"The call could not be connected.");else if(e.type==="peer.disconnected")this.microphoneTransportReady=!1,this.worker?.postMessage({type:"microphone.ready",value:!1}),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(e.type==="peer.connected")this.microphoneTransportReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.callbacks.onState?.("live",this.opened?void 0:"Audio reconnected")}catch{}}startRTTProbe(){this.stopRTTProbe();let t=()=>{this.sendText(JSON.stringify({type:"ping",nonce:performance.now()})),this.sendDiagnostics()};t(),this.pingTimer=setInterval(t,5000)}sendText(t){this.worker?.postMessage({type:"send.text",data:t})}sendDiagnostics(){let t=this.diagnostics;this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{timing:{transport:this.transportTiming,playback:this.playbackTiming},rtt_ms:t.rttMs,playback_queue_ms:t.queueMs,playback_target_ms:t.targetMs,playback_max_queue_ms:t.maxQueueMs,playback_underruns:t.underruns,playback_dropped_ms:t.droppedMs,websocket_buffered_bytes:t.websocketBufferedBytes,audio_context_rate:t.audioContextRate,microphone_sample_rate:t.microphoneSampleRate,microphone_channel_count:t.microphoneChannelCount,echo_cancellation:t.echoCancellation,noise_suppression:t.noiseSuppression,auto_gain_control:t.autoGainControl,mic_active_rms_dbfs:t.micActiveRmsDbfs,mic_peak_dbfs:t.micPeakDbfs,mic_post_peak_dbfs:t.micPostPeakDbfs,mic_input_gain_db:t.micInputGainDb,mic_limiter_reduction_db:t.micLimiterReductionDb,capture_sequence_gaps:t.captureSequenceGaps,playback_sequence_gaps:t.playbackSequenceGaps,drop_events:t.dropEvents}})),this.callbacks.onDiagnostics?.({...t})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setMuted(t){if(this.muted=t,this.worker?.postMessage({type:"muted",value:t}),this.capture?.port.postMessage({type:"muted",value:t}),t)this.sendText(JSON.stringify({type:"interrupt"}))}stop(){let t=!this.closed;this.closed=!0;try{if(t)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(t){this.closed=!0;try{this.callbacks.onState?.("error",t)}finally{this.teardown()}}teardown(){this.stopRingback(),this.microphoneTransportReady=!1,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let t=this.worker;if(t?.postMessage({type:"close"}),t)setTimeout(()=>t.terminate(),100);this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((e)=>e.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var At=k*A/1000;var g=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
`;var T=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
// and consume frames directly so panel rendering cannot stall live speech.
const MAGIC = 0x31545041; // "APT1" in little endian.
const HEADER_BYTES = 16;
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
const timing = { capture_frames:0, capture_sent_ms:0, capture_dropped_ms:0, capture_max_age_ms:0, playback_received_ms:0, playback_sequence_gaps:0, playback_max_transit_ms:0, playback_max_server_queue_ms:0, playback_transport_dropped_ms:0, worker_max_tick_gap_ms:0, drop_totals_ms:{} };
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
    framedV2=false; serverClockOffset=null; serverClockUncertainty=null; serverClockSampleAt=0; playbackExpected=null;
    ws.send(JSON.stringify({type:"media.capabilities",version:2}));
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
        if(control.type==="media.capabilities" && control.version===2) framedV2=true;
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
    let buffer=event.data, sequence=playbackSequence++;
    if(framedV2 && buffer.byteLength>=32 && new DataView(buffer).getUint32(0,true)===MAGIC_V2) {
      const view=new DataView(buffer);
      sequence=view.getUint32(4,true);
      if(playbackExpected!==null && sequence>playbackExpected) timing.playback_sequence_gaps+=sequence-playbackExpected;
      playbackExpected=(sequence+1)>>>0;
      const sent=view.getFloat64(16,true), queued=view.getFloat64(24,true);
      if(Number.isFinite(queued)) timing.playback_max_server_queue_ms=Math.max(timing.playback_max_server_queue_ms,queued);
      if(serverClockOffset!==null && monotonicEpochMS()-serverClockSampleAt<15000 && Number.isFinite(sent)) {
        const transit=Math.max(0,monotonicEpochMS()+serverClockOffset-sent);
        timing.playback_max_transit_ms=Math.max(timing.playback_max_transit_ms,transit);
        // Drop only when even the conservative lower age bound is stale.
        if(transit-serverClockUncertainty>320) {
          const duration=(buffer.byteLength-32)*1000/(SAMPLE_RATE*2);
          timing.playback_transport_dropped_ms+=duration;
          timing.drop_totals_ms.playback_transport_age=(timing.drop_totals_ms.playback_transport_age||0)+duration;
          resamplers.delete(\`\${SAMPLE_RATE}:\${contextRate}\`);
          return;
        }
      }
      buffer=buffer.slice(32);
    }
    timing.playback_received_ms+=buffer.byteLength*1000/(SAMPLE_RATE*2);
    const frame = resample(decodePlayback(buffer), SAMPLE_RATE, contextRate);
    playbackPort?.postMessage({ type: "playback", frame, sequence, timestamp_ms: performance.now(), received_audio_ms:audioClockOffset===null ? null : monotonicEpochMS()-audioClockOffset, clock_uncertainty_ms:audioClockUncertainty }, [frame.buffer]);
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
`;function x(t=!1){let e=(t?[g]:[g,T]).map((a)=>URL.createObjectURL(new Blob([a],{type:"text/javascript"})));return{urls:e,dispose(){e.splice(0).forEach((a)=>URL.revokeObjectURL(a))}}}async function F(t,e=!1){if(typeof location>"u")return x(e);let a=new URL(t.mcpURL(),location.href);if(a.origin!==location.origin)return x(e);if(t.name!=="telephony"||!t.projectId||!Number.isSafeInteger(t.installId)||t.installId<=0)throw Error("Telephony audio requires a project and installation");return{urls:await Promise.all((e?[g]:[g,T]).map(async(h,i)=>{let c=new TextEncoder().encode(h),n=Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",c)),(u)=>u.toString(16).padStart(2,"0")).join(""),f=`/ui/frontend/${i===0?"worklet":"worker"}-${n}.js`;if(await t.get(f,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==h)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let r=new URL(`/api/apps/telephony/_install/${t.installId}${f}`,a);return r.searchParams.set("project_id",t.projectId),r.searchParams.set("install_id",String(t.installId)),r.href})),dispose(){}}}async function H(){return(await navigator.mediaDevices.enumerateDevices()).filter((e)=>e.kind==="audioinput").map((e,a)=>({deviceId:e.deviceId,label:e.label||`Microphone ${a+1}`}))}function U(t,e){let a=new C(t,!1),s,m=!1,h,i=()=>{return m=!0,h??=(async()=>{try{await a.cancel()}finally{s?.dispose(),s=void 0}})()};return{async start(c={}){if(m)throw Error("Microphone preview is already used");m=!0;try{if(s=e?await F(e,!0):x(!0),h)throw s.dispose(),s=void 0,Error("Microphone preview was cancelled");await a.start(s.urls[0],{...d,...c})}catch(n){throw await i(),n}},stop:i}}function $(t){return{async preflight(e){(await navigator.mediaDevices.getUserMedia({audio:w(e)})).getTracks().forEach((s)=>s.stop())},create(e){let a=new R(e),s,m=!1,h=()=>{m=!0;try{a.stop()}finally{s?.dispose(),s=void 0}};return{async start(i,c){try{if(s=t?await F(t):x(),m)throw Error("Audio session was cancelled");await a.start(i,s.urls[0],s.urls[1],c)}catch(n){throw h(),n}},stop:h,setMuted:(i)=>a.setMuted(i),sendDTMF:(i)=>a.sendDTMF(i),setOutputVolume:(i)=>a.setOutputVolume(i),startRingback:(i)=>a.startRingback(i),stopRingback:()=>a.stopRingback()}}}}function j(t){if(!t)return"placing";if(G(t))return"ended";if(t==="ringing")return"ringing";if(t==="answered"||t==="in-progress")return"connected";return"placing"}var y=(t)=>t instanceof Error?t.message:String(t);class O{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;leaseTimer;leaseGeneration=0;timer;polling;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(t,e={}){this.client=t;this.options=e;if(this.runtime=e.audioRuntime??$(t.app),this.audioOptions={...d,...e.audio},q(this.audioOptions),this.interval=e.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.assertOpen(),this.listeners.add(t),()=>{this.listeners.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let e of this.listeners)try{e(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(t){if(this.disposed||t!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(t){return!this.disposed&&t===this.generation}finish(t){if(this.current(t))this.update({busy:!1})}cancellable(t,e){let a=this.cancellation.signal;return new Promise((s,m)=>{let h=()=>{a.removeEventListener("abort",h),m(Error("Softphone operation cancelled"))};if(a.addEventListener("abort",h,{once:!0}),t.then(s,m).finally(()=>a.removeEventListener("abort",h)),!this.current(e)||a.aborted)h()})}begin(t){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||t&&this.session)throw Error("Softphone already has an active operation or call");let e=this.invalidate();return this.update({busy:!0,detail:void 0}),e}async dial(t){let e=this.begin(!0),a,s=!1;try{this.assertCurrent(e);let m={to:t.to.trim(),from:t.from?.trim(),timeout_sec:t.timeout_sec,recording:t.recording},h=JSON.stringify(m);if(!this.intent||this.intent.value!==h||t.idempotency_key&&t.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:h,request:{...m,idempotency_key:t.idempotency_key||crypto.randomUUID()}};return await this.cancellable(this.runtime.preflight(this.audioOptions),e),this.assertCurrent(e),a=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(e),s=!0,this.outbound=!0,await this.attachAudio(a,e),a.call_id}catch(m){if(a&&(this.current(e)||!s||this.disposed))await this.recoverStartup(a,e,()=>this.client.hangup(a.call_id));if(this.current(e))this.update({detail:y(m)});throw m}finally{this.finish(e)}}async answer(t,e={}){let a=this.begin(!0),s,m=!1;try{this.assertCurrent(a),s=await this.client.answer(t,e),this.assertCurrent(a),m=!0,this.outbound=!1,await this.attachAudio(s,a)}catch(h){if(s&&(this.current(a)||!m||this.disposed))await this.recoverStartup(s,a,()=>this.client.release(s));if(this.current(a))this.update({detail:y(h)});throw h}finally{this.finish(a)}}async recoverStartup(t,e,a){try{if(await a(),this.current(e))this.clearCall()}catch{if(this.current(e))this.session=t,this.update({callId:t.call_id,audioState:"error"}),this.startPolling()}}async join(t){await this.answer(t,{rejoin:!0})}async attach(t){await this.acquireSession(t,!1)}async takeover(t){await this.acquireSession(t,!0)}async acquireSession(t,e){let a=this.begin(!0);try{await this.cancellable(this.runtime.preflight(this.audioOptions),a),this.assertCurrent(a);let s=await(e?this.client.takeover(t):this.client.attach(t));this.assertCurrent(a),await this.attachAudio(s,a),await this.reconcileAttachedCall(s,a)}catch(s){if(this.current(a))this.update({detail:y(s)});throw s}finally{this.finish(a)}}async reconnect(t){if(!this.session)throw Error("No call to reconnect");let e={...this.audioOptions,...t};q(e);let a=this.begin(!1);this.audioOptions=e;try{await this.attachAudio(this.session,a)}catch(s){if(this.current(a))this.update({detail:y(s)});throw s}finally{this.finish(a)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let t=this.session;if(!t){this.cancellation.abort();return}let e=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let a=(async()=>{try{if(await this.client.hangup(t.call_id),this.current(e))this.clearCall()}catch(s){if(this.current(e))this.update({audioState:"error",detail:y(s)}),this.startPolling();throw s}finally{this.finish(e)}})();this.hangingUp=a;try{await a}finally{if(this.hangingUp===a)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(t){this.assertOpen();let e=this.session;if(!e)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let a=this.generation;this.update({busy:!0,detail:void 0});let s;try{s=this.client[t](e.call_id)}catch(m){if(this.current(a)&&this.session===e)this.update({busy:!1,detail:y(m)});throw m}this.controlPending=s;try{let m=await s;if(this.current(a)&&this.session===e)this.update({holdState:m.hold_state,recordingState:m.recording_state,controlError:m.control_error,capabilities:m.capabilities});return m}catch(m){if(this.current(a)&&this.session===e)this.update({detail:y(m)});throw m}finally{if(this.controlPending===s)this.controlPending=void 0;if(this.current(a)&&this.session===e)this.update({busy:!1})}}configureAudio(t){this.assertOpen();let e={...this.audioOptions,...t};q(e),this.audioOptions=e}setMuted(t){this.assertOpen(),this.audio?.setMuted(t),this.update({muted:t})}setOutputVolume(t){if(this.assertOpen(),!Number.isFinite(t)||t<0||t>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=t,this.audio?.setOutputVolume(t)}sendDTMF(t){if(this.assertOpen(),!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(t)}observeCall(t){if(this.disposed||t.id!==this.session?.call_id)return;if(t.direction)this.outbound=t.direction==="outbound";if(G(t.status)){this.invalidate(),this.clearCall(t.status),this.update({busy:!1,phase:"ended",termination:t.termination,answeredBy:t.answered_by??this.snapshot.answeredBy,endedAt:t.ended_at});return}this.update({carrierStatus:t.status,phase:j(t.status),answeredBy:t.answered_by??this.snapshot.answeredBy,holdState:t.hold_state??this.snapshot.holdState,recordingState:t.recording_state??this.snapshot.recordingState,controlError:t.control_error??this.snapshot.controlError,capabilities:t.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let t=this.options.ringback;return typeof t==="object"&&t?t.country:void 0}syncRingback(){let t=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(t&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!t&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(t,e){this.assertCurrent(e),this.stopAudio(),this.session=t,this.update({callId:t.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let a,s=()=>!this.disposed&&a!==void 0&&this.audio===a,m=(h)=>{if(s())try{h()}catch{}};try{if(this.assertCurrent(e),a=this.runtime.create({onState:(h,i)=>{if(!s())return;if(h==="ended"&&i==="call.ended"){this.observeCall({id:t.call_id,status:"completed"});return}if(this.update({audioState:h,detail:i}),s()&&(h==="error"||h==="ended")){if(this.stopAudio(),h==="error")this.reconcileFailedAudio(t,e)}},onLevels:(h,i)=>m(()=>this.options.onLevels?.(h,i)),onDiagnostics:(h)=>m(()=>this.options.onDiagnostics?.(h)),onNotice:(h)=>m(()=>this.options.onNotice?.(h)),onCallStatus:(h)=>m(()=>this.observeCall({id:h.call_id,status:h.status,direction:h.direction,answered_at:h.answered_at,ended_at:h.ended_at,answered_by:h.answered_by,termination:h.termination,hold_state:h.hold_state,recording_state:h.recording_state,control_error:h.control_error}))}),this.audio=a,this.assertCurrent(e),this.startPolling(),this.startLease(t),a.setMuted(this.snapshot.muted),await this.cancellable(a.start(this.client.mediaURL(t),this.audioOptions),e),this.assertCurrent(e),!s())throw Error(this.snapshot.detail||"Audio connection ended during setup");a.setMuted(this.snapshot.muted),this.syncRingback()}catch(h){if(s())this.stopAudio();if(this.current(e))this.update({audioState:"error",detail:y(h)});throw h}}async reconcileAttachedCall(t,e){try{let a=await this.client.getCall(t.call_id);if(this.current(e)&&this.session===t&&a)this.observeCall(a)}catch{}}async reconcileFailedAudio(t,e){try{let a=await this.client.getCall(t.call_id);if(!this.current(e)||this.session!==t||!a)return;if(a.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(a)}catch{}}startLease(t){let e=++this.leaseGeneration;if(!t.lease_seconds)return;let a=async()=>{if(this.disposed||this.session!==t||e!==this.leaseGeneration)return;try{if(await this.client.renew(t),e===this.leaseGeneration)this.leaseTimer=setTimeout(a,t.lease_seconds*1000/3)}catch(s){if(e!==this.leaseGeneration)return;this.stopAudio(),this.update({audioState:"error",detail:`Audio authorization ended: ${y(s)}`})}};this.leaseTimer=setTimeout(a,t.lease_seconds*1000/3)}stopAudio(){++this.leaseGeneration,clearTimeout(this.leaseTimer),this.leaseTimer=void 0;let t=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{t?.stopRingback?.()}catch{}}try{t?.stop()}catch{}if(t)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let t=new AbortController;this.polling=t;let e=async()=>{let a=this.session?.call_id;if(!a||t.signal.aborted)return;try{let s=await this.client.getCall(a,t.signal);if(!t.signal.aborted&&s)this.observeCall(s)}catch(s){if(!t.signal.aborted)this.update({detail:`Call status unavailable: ${y(s)}`})}finally{if(!t.signal.aborted&&this.session&&!this.disposed)this.timer=setTimeout(e,this.interval)}};this.timer=setTimeout(e,this.interval)}clearCall(t){this.stopAudio(),this.stopPolling(),this.session=void 0,this.outbound=!1,this.update({callId:void 0,carrierStatus:t,audioState:"idle",muted:!1,detail:void 0,phase:t?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function I(t,e="en"){if(t?.reason==="time_limit")return e.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return t?.reason?.replaceAll("_"," ")??""}class Q extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function tt(t){return t.direction==="inbound"&&t.status==="pending"&&t.answerable!==!1&&!t.routing_waiting&&(t.peer_kind==="human"||Boolean(t.ring_offers?.some((e)=>e.kind==="browser")))}var G=(t)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(t);function _(t){if(!t||/[\s/\\?#]/.test(t))throw Error("Invalid call ID");return encodeURIComponent(t)}class E{app;options;listMicrophones=H;createMicrophonePreview=(t)=>U(t,this.app);constructor(t,e={}){this.app=t;this.options=e;if(t.name!=="telephony"||!t.projectId||!t.installId)throw Error("Telephony requires an explicit project and installation")}path(t){if(!this.options.authProvider)return t;return`/user${t}${t.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(t){let e=await this.app.get(this.path("/calls"),{signal:t});if(!Array.isArray(e?.calls)||e.calls.some((a)=>!a||typeof a.id!=="string"||typeof a.status!=="string"))throw Error("Invalid Telephony calls response");return e.calls}async getCall(t,e){let a=await this.app.get(this.path(`/calls?call_id=${_(t)}`),{signal:e});if(!Array.isArray(a?.calls))throw Error("Invalid Telephony call response");return a.calls.find((s)=>s.id===t)}incomingCalls(t){return t.filter(tt)}watchCalls(t,e={}){let a=e.intervalMs??2000;if(!Number.isFinite(a)||a<100)throw Error("Call watch interval must be at least 100 ms");let s=new AbortController,m,h,i=!1,c=!1,n=new Set,f=()=>{clearTimeout(m),s.abort(),h?.close(),e.signal?.removeEventListener("abort",f)},o=(u)=>{try{e.onError?.(u)}catch{f()}};if(e.signal?.addEventListener("abort",f,{once:!0}),e.signal?.aborted)f();let r=async(u)=>{if(s.signal.aborted)return;if(i){c=!0;return}clearTimeout(m),i=!0;let l=performance.now();try{let p=await this.listCalls(s.signal);if(!s.signal.aborted){t(p);for(let M of p){if(M.answerable!==!0||M.status!=="pending")continue;for(let S of M.ring_offers??[]){if(S.kind!=="browser"||!S.id||n.has(S.id))continue;n.add(S.id),this.acknowledgeOffer(M.id,S.id).catch((v)=>{if(![404,405].includes(v?.status??0))n.delete(S.id)})}}try{e.onTiming?.({trigger:u,fetchMs:performance.now()-l})}catch{}}}catch(p){if(!s.signal.aborted){let M=p?.status;try{e.onFailure?.({trigger:u,fetchMs:performance.now()-l,status:typeof M==="number"?M:void 0,error:p})}catch{}o(p)}}finally{if(i=!1,!s.signal.aborted)if(c)c=!1,r("push");else m=setTimeout(()=>void r("poll"),a)}};if(r("poll"),!s.signal.aborted&&e.push!==!1&&typeof this.app.subscribe==="function")try{h=this.app.subscribe(this.path("/calls/events"),(u)=>{if(u.type==="calls.changed")r("push");else if(u.type==="access.revoked")h?.close(),r("push")},{transport:"fetch",signal:s.signal,reconnectDelayMs:250,onError:(u)=>{let l=u?.status;if(l===404||l===405||l===501)h?.close()}})}catch{}return{close:f}}async place(t){if(!/^\+[1-9]\d{7,14}$/.test(t.to)||!t.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");return this.session(await this.app.post(this.path("/softphone/place"),t))}async answer(t,e={}){try{return this.session(await this.app.post(this.path(`/softphone/answer/${_(t)}`),e),t)}catch(a){let s=a;if(s.status===409&&typeof s.body==="string"){let m;try{m=JSON.parse(s.body)}catch{}if(m?.code==="offer_expired")throw new Q}throw a}}async acknowledgeOffer(t,e){await this.app.post(this.path(`/softphone/offer/ack/${_(t)}`),{offer_id:e})}async declineOffer(t,e){await this.app.post(this.path(`/softphone/offer/decline/${_(t)}`),{offer_id:e})}async attach(t){return this.session(await this.app.post(this.path(`/softphone/attach/${_(t)}`),{}),t)}async takeover(t){return this.session(await this.app.post(this.path(`/softphone/takeover/${_(t)}`),{}),t)}async renew(t){await this.app.post(this.path(`/softphone/renew/${_(t.call_id)}`),{session_token:t.session_token})}async release(t){if(!t.session_token)throw Error("Answer session has no release token");await this.app.post(this.path(`/softphone/release/${_(t.call_id)}`),{session_token:t.session_token})}async hangup(t){await this.app.post(this.path(`/calls/${_(t)}/hangup`),{})}hold(t){return this.app.post(this.path(`/calls/${_(t)}/hold`),{})}resume(t){return this.app.post(this.path(`/calls/${_(t)}/resume`),{})}pauseRecording(t){return this.app.post(this.path(`/calls/${_(t)}/pause-recording`),{})}resumeRecording(t){return this.app.post(this.path(`/calls/${_(t)}/resume-recording`),{})}createSoftphone(t={}){return new O(this,t)}mediaURL(t){let e=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),a=new URL(t.media_url,e),s=`/api/apps/telephony/_install/${this.app.installId}/softphone/media/${_(t.call_id)}/`;if(a.origin!==e.origin||!["http:","https:"].includes(a.protocol)||a.username||a.password||a.search||a.hash||!a.pathname.startsWith(s)||!/^[A-Za-z0-9_-]+$/.test(a.pathname.slice(s.length)))throw Error("Invalid Telephony media endpoint");return a.protocol=a.protocol==="https:"?"wss:":"ws:",a.href}session(t,e){let a=t;if(!a||typeof a.call_id!=="string"||typeof a.media_url!=="string"||e&&a.call_id!==e||a.session_token!==void 0&&typeof a.session_token!=="string")throw Error("Invalid Telephony session response");if(a.lease_seconds!==void 0&&(!Number.isFinite(a.lease_seconds)||a.lease_seconds<10||a.lease_seconds>3600||!a.session_token))throw Error("Invalid media lease");return _(a.call_id),this.mediaURL(a),a}}var Xt=P({app:"telephony",create:({app:t})=>new E(t)});function Ut({app:t},e){return new E(t,e)}export{Ut as createClient,I as callTerminationLabel};
