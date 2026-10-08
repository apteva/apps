var G=()=>performance.now();function z(t){let e=t,h=e?.status,s;try{let a=JSON.parse(e?.body??"{}");if(typeof a.code==="string")s=a.code}catch{}let i=s==="media_lease_expired";return{status:h,code:s,expired:i,denied:!i&&[401,403,404,410].includes(h)}}class v{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(t,e,h,s,i=G(),a={now:G,setTimeout:(m,u)=>setTimeout(m,u),clearTimeout:(m)=>clearTimeout(m)}){this.seconds=t;this.renew=e;this.ended=h;this.event=s;this.clock=a;this.deadline=i+t*1000-1000,this.watchExpiry(),this.schedule(Math.min(t*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(t,e){let{status:h,code:s}=z(e);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:t,status:h,code:s,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(t){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,t))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let t=this.clock.now();try{let e=await Promise.race([this.renew(),new Promise((h,s)=>{this.requestTimer=this.clock.setTimeout(()=>s(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(e?.lease_seconds!==void 0){if(!Number.isFinite(e.lease_seconds)||e.lease_seconds<10||e.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=e.lease_seconds}this.deadline=t+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(e){if(this.stopped)return;let h=z(e);if(h.denied||h.expired){this.finish(h.expired?"expired":"revoked",e);return}this.report("retrying",e),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(t,e){if(this.stopped)return;this.report(t,e),this.stop(),this.ended(t,e)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function Mt(t){return t}class tt{emit;now;timestamp;lastTick;suspendedAt;contextState;counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(t,e=()=>performance.now(),h=()=>new Date().toISOString()){this.emit=t;this.now=e;this.timestamp=h}tick(){let t=this.now();if(this.lastTick!==void 0){let e=Math.max(0,t-this.lastTick-1000);if(e>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,e),this.report("main_thread","scheduling_gap",e)}this.lastTick=t}context(t){if(t===this.contextState)return;this.contextState=t;let e=this.now();if(t==="suspended"||t==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=e,this.counters.audio_context_suspend_count++;this.report("audio_context",t)}else{let h;if(this.suspendedAt!==void 0)h=Math.max(0,e-this.suspendedAt),this.counters.audio_context_suspended_ms+=h,this.suspendedAt=void 0;this.report("audio_context",t,h)}}report(t,e,h){try{this.emit({timestamp:this.timestamp(),action:t,outcome:e,duration_ms:h===void 0?void 0:Math.round(h)})}catch{}}}var xt=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function mt(t){let e=(t??"FR").trim().toUpperCase();return xt[e]??xt.FR}function Vt(t,e){let h=[];if(!(t.cadence.reduce((a,m)=>a+m,0)>0)||!(e>0))return h;let i=0;while(i<e)for(let a=0;a<t.cadence.length;a+=2){let m=t.cadence[a]??0,u=t.cadence[a+1]??0;if(i>=e)break;if(m>0)h.push({start:i,end:Math.min(i+m,e)});i+=m+u}return h}function it(t,e,h,s=0.12){let i=t.createGain();i.gain.value=0,i.connect(e);let a=s/Math.max(1,h.frequencies.length),m=h.frequencies.map((r)=>{let y=t.createOscillator();return y.type="sine",y.frequency.value=r,y.connect(i),y.start(),y}),u=t.currentTime+0.05,p=0,_=0.01,S=()=>{let r=t.currentTime-u+10;if(r<=p)return;for(let y of Vt(h,r)){if(y.end<=p)continue;let M=u+Math.max(y.start,p),l=u+y.end;i.gain.setValueAtTime(0,M),i.gain.linearRampToValueAtTime(a,M+_),i.gain.setValueAtTime(a,Math.max(M+_,l-_)),i.gain.linearRampToValueAtTime(0,l)}p=r};S();let f=setInterval(S,4000),n=!1;return()=>{if(n)return;n=!0,clearInterval(f);try{i.gain.cancelScheduledValues(0)}catch{}i.gain.value=0;for(let r of m){try{r.stop()}catch{}r.disconnect()}i.disconnect()}}var N=24000,et=60;async function Ot(t,e){try{await t.audioWorklet.addModule(e)}catch(h){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:h})}}function Wt(t){let e=new Int16Array(t.length);for(let h=0;h<t.length;h++){let s=Math.max(-1,Math.min(1,t[h]));e[h]=s<0?s*32768:s*32767}return e.buffer}class W{history=new Float32Array(64);phase=0;process(t,e,h){if(e===h)return t;let s=new Float32Array(64+t.length);s.set(this.history),s.set(t,64);let i=e/h,a=Math.min(1,h/e)*0.9,m=[],u=this.phase;for(;u<t.length;u+=i){let p=u+32,_=0,S=0;for(let f=Math.ceil(p-32);f<=Math.floor(p+32);f++){let n=f-p,y=(Math.abs(n)<0.000000001?a:Math.sin(Math.PI*a*n)/(Math.PI*n))*(0.5+0.5*Math.cos(Math.PI*n/32));if(f>=0&&f<s.length)_+=s[f]*y,S+=y}m.push(S?_/S:0)}return this.phase=u-t.length,this.history=s.slice(-64),new Float32Array(m)}}function Dt(t){let e=0;for(let h=0;h<t.length;h++)e+=t[h]*t[h];return Math.sqrt(e/Math.max(1,t.length))}var L={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function $(t){if(t.mediaTransport!==void 0&&!["websocket","webrtc","auto"].includes(t.mediaTransport))throw RangeError("Unsupported softphone media transport");let e=t.playbackTargetMs??et,h=t.playbackMinMs??Math.min(et,e),s=t.playbackMaxMs??160;for(let i of[e,h,s])if(!Number.isFinite(i)||i<40||i>160)throw RangeError("Playback buffers must be between 40 and 160 ms");if(h>e||e>s)throw RangeError("Playback buffers require min <= target <= max");return{initialTargetMs:e,minTargetMs:h,maxTargetMs:s,hardMaxMs:320}}function I(t){return{...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{},echoCancellation:t.echoCancellation,noiseSuppression:t.noiseSuppression,autoGainControl:t.autoGainControl}}function dt(t){let e=t.getSettings();return{deviceLabel:t.label||"Default microphone",sampleRate:typeof e.sampleRate==="number"?e.sampleRate:null,channelCount:typeof e.channelCount==="number"?e.channelCount:null,echoCancellation:typeof e.echoCancellation==="boolean"?e.echoCancellation:null,noiseSuppression:typeof e.noiseSuppression==="boolean"?e.noiseSuppression:null,autoGainControl:typeof e.autoGainControl==="boolean"?e.autoGainControl:null}}function K(t){if(!Number.isFinite(t)||t<=0)return null;return 20*Math.log10(t)}function Xt(t,e,h){let s=new ArrayBuffer(44+e*2),i=new DataView(s),a=(u,p)=>{for(let _=0;_<p.length;_++)i.setUint8(u+_,p.charCodeAt(_))};a(0,"RIFF"),i.setUint32(4,36+e*2,!0),a(8,"WAVE"),a(12,"fmt "),i.setUint32(16,16,!0),i.setUint16(20,1,!0),i.setUint16(22,1,!0),i.setUint32(24,h,!0),i.setUint32(28,h*2,!0),i.setUint16(32,2,!0),i.setUint16(34,16,!0),a(36,"data"),i.setUint32(40,e*2,!0);let m=44;for(let u of t)for(let p=0;p<u.length;p++)i.setInt16(m,u[p],!0),m+=2;return new Blob([s],{type:"audio/wav"})}class yt{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new W;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(t,e=!0){this.onLevel=t;this.recordAudio=e}async start(t,e){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:I(e)}),this.ensureOpen();let h=this.stream.getAudioTracks()[0];if(!h)throw Error("No microphone audio track was returned.");this.settings=dt(h);try{this.ctx=new AudioContext({sampleRate:N,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await Ot(this.ctx,t),this.ensureOpen();let s=this.ctx.sampleRate,i=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:e.inputGainDB,highpassFilter:e.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(a)=>{if(this.stopped)return;if(!(a.data instanceof Float32Array)){if(a.data.type==="capture.stats")this.peak=Math.max(this.peak,a.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,a.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,a.data.limiter_reduction_db??0);return}let m=a.data;if(s!==N)m=this.resampler.process(m,s,N);let u=Dt(m);if(this.onLevel?.(u),this.recordAudio)this.frames.push(new Int16Array(Wt(m)));if(this.samples+=m.length,u>=0.005){for(let p=0;p<m.length;p++)this.activeSquares+=m[p]*m[p];this.activeSamples+=m.length}},i.connect(this.capture),this.settings}catch(h){throw await this.release(),h}}async stop(){this.stopped=!0;let t=this.settings,e=this.frames,h=this.samples,s=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,i=this.peak;if(await this.release(),!t||h===0)throw Error("No microphone audio was captured.");return{audio:Xt(e,h,N),durationMs:Math.round(h*1000/N),sampleRate:N,activeRmsDbfs:K(s),peakDbfs:K(i),postPeakDbfs:K(this.postPeak||i),limiterReductionDb:this.limiterReductionDB,settings:t}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let t of this.stream?.getTracks()??[])t.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class nt{callbacks;clientEpoch=crypto.randomUUID();worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new tt((t)=>this.recordSessionEvent(t));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={mediaTransport:"websocket",codec:"pcm16",rttMs:null,queueMs:0,targetMs:et,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:N,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:L.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t={}){this.callbacks=t}get isMuted(){return this.muted}async start(t,e,h,s=L){let i=$(s);this.diagnostics.targetMs=i.initialTargetMs,this.callbacks.onState?.("connecting"),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{this.stream=await navigator.mediaDevices.getUserMedia({audio:I(s)}),this.ensureOpen();let a=this.stream.getAudioTracks()[0];if(!a)throw Error("No microphone audio track was returned.");if(a.readyState==="ended")throw Error("Microphone disconnected before audio setup.");a.onmute=()=>this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser."),a.onunmute=()=>this.callbacks.onNotice?.("Microphone input restored."),a.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let m=dt(a);this.diagnostics={...this.diagnostics,microphoneSampleRate:m.sampleRate??0,microphoneChannelCount:m.channelCount??0,echoCancellation:m.echoCancellation,noiseSuppression:m.noiseSuppression,autoGainControl:m.autoGainControl,micInputGainDb:s.inputGainDB};try{this.ctx=new AudioContext({sampleRate:N,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await this.ctx.resume();if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),s.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(s.outputDeviceId),this.ensureOpen();await Ot(this.ctx,e),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let u=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:i}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),u.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=s.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await this.openWorker(t,h),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running"&&this.microphoneTransportReady)this.callbacks.onState?.("live")},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(a){if(this.closed)this.teardown();if(!this.closed)this.fail(a instanceof Error?a.message:"browser audio setup failed");throw a}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){if(await this.ctx?.resume(),this.microphoneTransportReady)this.callbacks.onState?.("live")}setOutputVolume(t){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,t))}sendDTMF(t){if(/^[0-9*#]+$/.test(t))this.sendText(JSON.stringify({type:"dtmf",digits:t}))}startRingback(t){if(this.closed||!this.ctx||this.ringback)return;this.ringback=it(this.ctx,this.ctx.destination,mt(t))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="capture.stats")return;this.micLevel=this.muted?0:e.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:K(e.active_rms??0),micPeakDbfs:K(e.pre_peak??0),micPostPeakDbfs:K(e.post_peak??0),micInputGainDb:e.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:e.limiter_reduction_db??0}},this.playback.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="stats")return;this.playbackTiming={played_ms:e.played_ms,max_residence_ms:e.max_residence_ms,drop_totals_ms:e.drop_totals_ms,coaching:{played_ms:e.whisper_played_ms,dropped_ms:e.whisper_dropped_ms,max_queue_ms:e.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,e.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:e.whisper_played_ms??0,coachingDroppedMs:e.whisper_dropped_ms??0,coachingMaxQueueMs:e.whisper_max_queue_ms??0,queueMs:e.queue_ms??0,targetMs:e.target_ms??et,underruns:e.underruns??0,droppedMs:e.dropped_ms??0,maxQueueMs:e.max_queue_ms??0,playbackSequenceGaps:e.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,e.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(t,e){return new Promise((h,s)=>{let i=new Worker(e);this.worker=i;let a=new MessageChannel,m=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:a.port1},[a.port1]),this.playback?.port.postMessage({type:"transport",port:m.port1},[m.port1]);let u=!1,p=setTimeout(()=>{if(u)return;u=!0,s(Error("audio connection timed out"))},1e4),_=(S)=>{if(u)return;if(u=!0,clearTimeout(p),S)s(S);else h()};this.cancelWorkerStart=()=>_(Error("audio session closed")),i.onmessage=(S)=>{if(this.closed){_(Error("audio session closed"));return}let f=S.data;if(f?.type==="socket.open")this.opened=!0,this.mediaSocketConnected=!0,this.startRTTProbe(),_();else if(f?.type==="runtime.event")this.recordSessionEvent(f.event);else if(f?.type==="socket.message")this.handleControl(f.data);else if(f?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL().then((n)=>{if(!this.closed&&this.worker===i)i.postMessage({type:"socket.credentials",id:f.id,mediaURL:n})},(n)=>{if(this.closed||this.worker!==i)return;let r=z(n);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:r.denied?"revoked":"retrying",status:r.status,code:r.code}),i.postMessage({type:"socket.credentials",id:f.id,denied:r.denied})})}else if(f?.type==="socket.error")this.recordSessionEvent({timestamp:f.timestamp,action:"websocket",outcome:"transport_error"});else if(f?.type==="socket.close")if(this.recordSessionEvent({timestamp:f.timestamp,action:"websocket",outcome:"closed",code:String(f.code),detail:f.reason,was_clean:f.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else _(Error("audio connection closed before it was ready"));else if(f?.type==="socket.failed")this.mediaSocketConnected=!1,_(Error(f.detail||"audio connection lost")),this.fail(f.detail||"audio connection lost");else if(f?.type==="transport.drop"&&f.event)this.diagnostics.dropEvents=this.mergeDropEvents(f.event);else if(f?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=f.buffered_bytes??0,this.transportTiming=f.timing??{},typeof f.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(f.timing.rtt_ms)}},i.onerror=(S)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:S.message?.slice(0,160)}),_(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},i.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:t,contextRate:this.ctx?.sampleRate??N,muted:this.muted,capturePort:a.port2,playbackPort:m.port2},[a.port2,m.port2])})}mergeDropEvents(t,e){if(t)this.workerDropEvents=[...this.workerDropEvents,t].slice(-100);if(e)this.playbackDropEvents=e.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((h,s)=>h.timestamp.localeCompare(s.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;handleControl(t){if(this.closed)return;try{let e=JSON.parse(t);if(e.type==="dtmf.error"||e.type==="dtmf.sent")this.callbacks.onNotice?.(e.type==="dtmf.sent"?"Keypad tone sent":e.detail||"Keypad tone failed");else if(e.type==="pong"){if(this.diagnostics.captureSequenceGaps=e.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof e.nonce==="number"&&e.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-e.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(e.type==="call.ended"||e.type==="session.replaced"){this.closed=!0;try{this.callbacks.onState?.("ended",e.type)}finally{this.teardown()}}else if(e.type==="call.status"&&typeof e.call_id==="string"&&typeof e.status==="string"){if(e.hold_state&&e.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callbacks.onCallStatus?.(e)}else if(e.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:e.detail?.slice(0,160)}),this.fail(e.detail||"The call could not be connected.");else if(e.type==="coach.state")this.callbacks.onNotice?.(e.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(e.type==="audio.health"){let h=e;if(h.state!=="healthy"&&h.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:h.state,reason:h.reason,stages:h.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),h.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&h.state==="healthy"&&h.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(e.type==="media.delivery"){let h=e.state;if(h==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(h==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=h==="stalled"}else if(e.type==="peer.disconnected")this.microphoneTransportReady=!1,this.worker?.postMessage({type:"microphone.ready",value:!1}),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(e.type==="peer.connected")this.microphoneTransportReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.callbacks.onState?.("live",this.opened?void 0:"Audio reconnected")}catch{}}startRTTProbe(){this.stopRTTProbe();let t=()=>{this.sendDiagnostics()};t(),this.pingTimer=setInterval(t,5000)}sendText(t){this.worker?.postMessage({type:"send.text",data:t})}recordSessionEvent(t){this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],t].slice(-50);try{this.callbacks.onSessionEvent?.(t)}catch{}try{this.sendDiagnostics()}catch{}}sendDiagnostics(){let t=this.diagnostics;this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{media_transport:"websocket",codec:"pcm16",client_epoch:this.clientEpoch,session_events:t.sessionEvents,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:t.rttMs,playback_queue_ms:t.queueMs,playback_target_ms:t.targetMs,playback_max_queue_ms:t.maxQueueMs,playback_underruns:t.underruns,playback_dropped_ms:t.droppedMs,websocket_buffered_bytes:t.websocketBufferedBytes,audio_context_rate:t.audioContextRate,microphone_sample_rate:t.microphoneSampleRate,microphone_channel_count:t.microphoneChannelCount,echo_cancellation:t.echoCancellation,noise_suppression:t.noiseSuppression,auto_gain_control:t.autoGainControl,mic_active_rms_dbfs:t.micActiveRmsDbfs,mic_peak_dbfs:t.micPeakDbfs,mic_post_peak_dbfs:t.micPostPeakDbfs,mic_input_gain_db:t.micInputGainDb,mic_limiter_reduction_db:t.micLimiterReductionDb,capture_sequence_gaps:t.captureSequenceGaps,playback_sequence_gaps:t.playbackSequenceGaps,drop_events:t.dropEvents}})),this.callbacks.onDiagnostics?.({...t})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setMuted(t){let e=this.muted!==t;if(this.muted=t,this.worker?.postMessage({type:"muted",value:t}),this.capture?.port.postMessage({type:"muted",value:t}),t)this.sendText(JSON.stringify({type:"interrupt"}));if(e)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:t?"muted":"unmuted"})}stop(){let t=!this.closed;this.closed=!0;try{if(t)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(t){if(!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:t.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",t)}finally{this.teardown()}}teardown(){this.stopRingback(),this.microphoneTransportReady=!1,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let t=this.worker;if(t?.postMessage({type:"close"}),t)setTimeout(()=>t.terminate(),100);this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((e)=>e.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var Pe=N*et/1000;var D=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
const CROSSFADE_MS = 5;

class SoftphoneCaptureProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const config = options.processorOptions || {};
    this.nativeOutput = config.nativeOutput === true;
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
      if (this.nativeOutput && out) out[i] = processed;
      this.buffer[this.filled++] = processed;
      if (Math.abs(processed) >= 0.005) { this.activeSquares += processed * processed; this.activeSamples += 1; }
      if (this.filled === this.buffer.length) { if (!this.nativeOutput) this.emitFrame(); this.filled = 0; }
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
`;var St=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
let audioClockSampleAt = 0;
let capturePaused = false;
let refreshCredentials = false;
let refreshID = 0;
let lastRTTPing = null;
let pendingRecovery = false;
let reconnectCause = "transport_closed";
let serverClockOffset = null;
let serverClockUncertainty = null;
let serverClockSampleAt = 0;
let captureGateOpenedAt = 0;
let playbackExpected = null;
let playbackDeliveryBase = null;
let pendingMutedCapture = null;
let whisperEpoch = null;
let whisperDeliveryBase = null;
const timing = { capture_frames:0, capture_muted_frames:0, capture_muted_ms:0, capture_sent_ms:0, capture_dropped_ms:0, capture_max_age_ms:0, playback_received_ms:0, playback_ingress_ms:0, playback_sequence_gaps:0, playback_max_transit_ms:0, playback_max_server_queue_ms:0, playback_transport_dropped_ms:0, playback_source_dropped_ms:0, playback_max_source_age_ms:0, playback_max_delivery_excess_ms:0, playback_source_timestamp_ms:0, playback_source_sequence:0, playback_source_epoch:0, worker_max_tick_gap_ms:0, worker_pause_count:0, reconnect_attempts:0, reconnect_successes:0, rtt_ms:null, rtt_max_ms:0, rtt_samples:[], websocket_max_buffered_bytes:0, drop_totals_ms:{} };
function runtimeEvent(action,outcome,duration_ms) {
  postMessage({type:"runtime.event",event:{timestamp:new Date().toISOString(),action,outcome,duration_ms:duration_ms===undefined ? undefined : Math.round(duration_ms)}});
}
function monotonicEpochMS() { return performance.timeOrigin + performance.now(); }
function stats() {
  flushMutedCapture();
  timing.websocket_max_buffered_bytes=Math.max(timing.websocket_max_buffered_bytes,socket?.bufferedAmount ?? 0); postMessage({type:"transport.stats", buffered_bytes:socket?.bufferedAmount||0, timing:{...timing, clock_uncertainty_ms:serverClockUncertainty, clock_sample_age_ms:serverClockSampleAt ? monotonicEpochMS()-serverClockSampleAt : null}}); }
function dropCapture(message,reason,age=0) {
 const duration = message.frame.length*1000/(message.sample_rate||SAMPLE_RATE);
 timing.capture_dropped_ms+=duration;
 timing.drop_totals_ms[reason]=(timing.drop_totals_ms[reason]||0)+duration;
 resamplers.delete(\`\${message.sample_rate || SAMPLE_RATE}:\${SAMPLE_RATE}\`);
 postMessage({type:"transport.drop",event:{timestamp:new Date().toISOString(),direction:"operator_to_carrier",reason,duration_ms:duration,queue_before_ms:age,sequence:message.sequence}});
}

// Metadata shares the ordered media socket. The server can exclude only these
// exact source sequences from loss counts; no media/state decision uses it.
function flushMutedCapture() {
 const range=pendingMutedCapture;
 if(!range) return;
 pendingMutedCapture=null;
 if(socket?.readyState===WebSocket.OPEN) {
  try { socket.send(JSON.stringify({type:"capture.omitted",reason:"muted",...range})); }
  catch { /* Observations must never interrupt microphone or playback audio. */ }
 }
}
function observeMutedCapture(message) {
 const duration=message.frame.length*1000/(message.sample_rate||SAMPLE_RATE);
 timing.capture_muted_frames++;
 timing.capture_muted_ms+=duration;
 if(!Number.isInteger(message.sequence) || !socket || socket.readyState!==WebSocket.OPEN) return;
 const sequence=message.sequence>>>0;
 if(pendingMutedCapture && (sequence!==pendingMutedCapture.last_sequence+1)) flushMutedCapture();
 if(!pendingMutedCapture) pendingMutedCapture={first_sequence:sequence,last_sequence:sequence,frames:0};
 pendingMutedCapture.last_sequence=sequence;pendingMutedCapture.frames++;
}


function dropPlayback(buffer, header, sequence, reason, age) {
 const duration=(buffer.byteLength-header)*1000/(SAMPLE_RATE*2);
 if(reason==="playback_source_age") timing.playback_source_dropped_ms+=duration; else timing.playback_transport_dropped_ms+=duration;
 timing.drop_totals_ms[reason]=(timing.drop_totals_ms[reason]||0)+duration;
 resamplers.delete(\`\${SAMPLE_RATE}:\${contextRate}\`);
 postMessage({type:"transport.drop",event:{timestamp:new Date().toISOString(),direction:"carrier_to_operator",reason,duration_ms:duration,queue_before_ms:age,sequence}});
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
      audioClockSampleAt = now;
      audioClockUncertainty = rtt/2 + 10; // Audio render quantum and timestamp sampling.
    }
    return;
  }
  if (message?.type !== "capture") return;
  if(muted) { observeMutedCapture(message); return; }
  if(!microphoneReady || !socket || socket.readyState !== WebSocket.OPEN) return;
  const now = monotonicEpochMS();
  if(capturePaused || audioClockOffset===null || now-audioClockSampleAt>5000) {
    capturePort?.postMessage({type:"clock.probe",nonce:now});
    dropCapture(message,"capture_clock_unavailable"); return;
  }
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
  flushMutedCapture();
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
    const tickGap=tick-lastTickAt;
    timing.worker_max_tick_gap_ms=Math.max(timing.worker_max_tick_gap_ms,tickGap);
    if(tickGap>=1250) { timing.worker_pause_count++; runtimeEvent("worker","scheduling_gap",tickGap-1000); }
    lastTickAt=tick;
    capturePort?.postMessage({type:"clock.probe",nonce:tick});
    stats();
    const now = Date.now();
    if (now - lastReceivedAt >= (openedAt ? 15000 : 10000)) {
      detach({code:0,reason:"heartbeat_timeout",wasClean:false});
      ws.close();
      return;
    }
    if (ws.readyState === WebSocket.OPEN && now - lastPingAt >= 5000) {
      lastPingAt = now;
      lastRTTPing=monotonicEpochMS();
      ws.send(JSON.stringify({ type: "ping", nonce: lastRTTPing }));
      ws.send(JSON.stringify({ type: "media.clock", nonce:monotonicEpochMS() }));
    }
  }, 1000);
  ws.onopen = () => {
    if (socket !== ws || closed) { ws.close(); return; }
    if(pendingRecovery) { timing.reconnect_successes++; pendingRecovery=false; runtimeEvent("reconnect","connected"); }
    lastRTTPing=null;
    pendingMutedCapture=null;
    openedAt = Date.now();
    lastReceivedAt = openedAt;
    framedV2=false; serverClockOffset=null; serverClockUncertainty=null; serverClockSampleAt=0; playbackExpected=null; playbackDeliveryBase=null; whisperEpoch=null; whisperDeliveryBase=null; playbackPort?.postMessage({type:"whisper.clear"});
    capturePort?.postMessage({type:"clock.probe",nonce:monotonicEpochMS()});
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
        if(control.type==="pong" && lastRTTPing!==null && control.nonce===lastRTTPing) {
          const rtt=monotonicEpochMS()-lastRTTPing;
          if(Number.isFinite(rtt)&&rtt>=0) { timing.rtt_ms=rtt;timing.rtt_max_ms=Math.max(timing.rtt_max_ms,rtt);timing.rtt_samples.push({at:new Date().toISOString(),rtt_ms:rtt});if(timing.rtt_samples.length>32)timing.rtt_samples.shift(); }
          lastRTTPing=null; stats();
          // The pong also carries server capture sequence gaps.
          postMessage({type:"socket.message",data:JSON.stringify({...control,nonce:undefined})});
          return;
        }
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
      timing.playback_ingress_ms+=(buffer.byteLength-header)*1000/(SAMPLE_RATE*2);
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
      // Measure every received frame before selecting any rejection reason.
      let transit=null, sourceAge=null;
      if(serverClockOffset!==null && monotonicEpochMS()-serverClockSampleAt<15000 && Number.isFinite(sent)) {
        transit=Math.max(0,monotonicEpochMS()+serverClockOffset-sent);
        timing.playback_max_transit_ms=Math.max(timing.playback_max_transit_ms,transit);
        sourceAge=source!==null && Number.isFinite(source) ? Math.max(0,monotonicEpochMS()+serverClockOffset-source) : null;
        if(sourceAge!==null) {
          timing.playback_max_source_age_ms=Math.max(timing.playback_max_source_age_ms,sourceAge);
          if(audioClockOffset!==null) sourceAudioMS=Math.min(sourceAudioMS ?? Infinity,source-serverClockOffset-audioClockOffset+serverClockUncertainty+audioClockUncertainty);
        }
      }
      if(deliveryExcess>320) { dropPlayback(buffer,header,sequence,"playback_delivery_excess",sourceAge ?? deliveryExcess); return; }
      const sourceStale=sourceAge!==null && sourceAge-serverClockUncertainty>320;
      if(sourceStale || (transit!==null && transit-serverClockUncertainty>320)) {
        dropPlayback(buffer,header,sequence,sourceStale ? "playback_source_age" : "playback_transport_age",sourceStale ? sourceAge : transit); return;
      }
      buffer=buffer.slice(header);
    }
    if(!(framedV2 && (magic===MAGIC_V2 || magic===MAGIC_V3))) timing.playback_ingress_ms+=buffer.byteLength*1000/(SAMPLE_RATE*2);
    timing.playback_received_ms+=buffer.byteLength*1000/(SAMPLE_RATE*2);
    const frame = resample(decodePlayback(buffer), SAMPLE_RATE, contextRate);
    playbackPort?.postMessage({ type: "playback", frame, sequence, timestamp_ms: performance.now(), received_audio_ms:sourceAudioMS ?? (audioClockOffset===null ? null : monotonicEpochMS()-audioClockOffset), clock_uncertainty_ms:audioClockUncertainty }, [frame.buffer]);
  };
  ws.onerror = () => postMessage({ type: "socket.error", timestamp:new Date().toISOString(), detail:"WebSocket transport error" });
  function detach(event) {
    if (socket !== ws) return;
    if (healthTimer !== null) clearInterval(healthTimer);
    healthTimer = null;
    socket = null;
    microphoneReady = false;
    resamplers.clear();
    playbackPort?.postMessage({ type: "flush" });
    pendingRecovery=true;
    reconnectCause=event?.reason==="heartbeat_timeout" ? "heartbeat_timeout" : \`websocket_close_\${event?.code ?? 0}\`;
    postMessage({ type: "socket.close", timestamp:new Date().toISOString(), code:event?.code ?? 0, reason:(event?.reason || "").slice(0,160), wasClean:Boolean(event?.wasClean) });
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
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    timing.reconnect_attempts++; runtimeEvent("reconnect",reconnectCause);
    if(!refreshCredentials) { connect(); return; }
    const id=++refreshID;
    postMessage({type:"socket.reconnect",id,cause:reconnectCause});
    reconnectTimer=setTimeout(()=>{reconnectTimer=null;refreshID++;scheduleReconnect();},5000);
  }, delay);
}

self.onmessage = (event) => {
  const message = event.data;
  switch (message?.type) {
    case "init":
      muted = Boolean(message.muted);
      mediaURL = message.mediaURL;
      refreshCredentials = Boolean(message.refreshCredentials);
      contextRate = message.contextRate || SAMPLE_RATE;
      if(Number.isFinite(message.audioClockMS)&&Number.isFinite(message.monotonicEpochMS)) { audioClockOffset=message.monotonicEpochMS-message.audioClockMS; audioClockUncertainty=20; audioClockSampleAt=monotonicEpochMS(); }
      capturePort = message.capturePort;
      playbackPort = message.playbackPort;
      capturePort.onmessage = (captureEvent) => capture(captureEvent.data);
      capturePort.start?.();
      playbackPort.start?.();
      capturePort.postMessage({type:"clock.probe",nonce:monotonicEpochMS()});
      connect();
      break;
    case "socket.credentials":
      if(closed || message.id!==refreshID) break;
      refreshID++;
      clearTimeout(reconnectTimer); reconnectTimer=null;
      if(message.mediaURL) { mediaURL=message.mediaURL; connect(); }
      else if(message.denied) postMessage({type:"socket.failed",detail:"Audio authorization ended. Reconnect after signing in."});
      else scheduleReconnect();
      break;
    case "clock.reset":
      capturePaused=Boolean(message.paused);
      audioClockOffset=null; audioClockSampleAt=0;
      resamplers.clear();
      captureGateOpenedAt=monotonicEpochMS();
      playbackPort?.postMessage({type:"flush"});
      if(!capturePaused) capturePort?.postMessage({type:"clock.probe",nonce:monotonicEpochMS()});
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
      flushMutedCapture();
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
`;function j(t=!1){let e=(t?[D]:[D,St]).map((h)=>URL.createObjectURL(new Blob([h],{type:"text/javascript"})));return{urls:e,dispose(){e.splice(0).forEach((h)=>URL.revokeObjectURL(h))}}}async function at(t,e=!1){if(typeof location>"u")return j(e);let h=new URL(t.mcpURL(),location.href);if(h.origin!==location.origin)return j(e);if(t.name!=="telephony"||!t.projectId||!Number.isSafeInteger(t.installId)||t.installId<=0)throw Error("Telephony audio requires a project and installation");return{urls:await Promise.all((e?[D]:[D,St]).map(async(a,m)=>{let u=new TextEncoder().encode(a),p=Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",u)),(n)=>n.toString(16).padStart(2,"0")).join(""),_=`/ui/frontend/${m===0?"worklet":"worker"}-${p}.js`;if(await t.get(_,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==a)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let f=new URL(`/api/apps/telephony/_install/${t.installId}${_}`,h);return f.searchParams.set("project_id",t.projectId),f.searchParams.set("install_id",String(t.installId)),f.href})),dispose(){}}}function Ht(t){if(t===void 0)return"websocket";if(!["websocket","webrtc","auto"].includes(t))throw RangeError("Unsupported softphone media transport");return t}function Ut(t){let e=new URL(t);return e.searchParams.set("transport","webrtc"),e.toString()}var A=(t)=>{try{t?.()}catch{}},o=(t,e=1000000000000)=>typeof t==="number"&&Number.isFinite(t)?Math.max(0,Math.min(t,e)):0;function Tt(t){let e=z(t);return e.denied||e.expired||["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes(t?.name)}function Jt(t,e){let h=0,s=0,i=0,a=0,m=0,u=0,p=0,_=0,S=null,f,n,r=0;t.forEach((M)=>{if(r=Math.max(r,o(M.timestamp,1000000000000000)),M.type==="outbound-rtp"&&(M.kind??M.mediaType)==="audio")h+=o(M.bytesSent);if(M.type==="inbound-rtp"&&(M.kind??M.mediaType)==="audio")s+=o(M.bytesReceived),i+=o(M.packetsLost),a=Math.max(a,o(M.jitter)*1000),m+=o(M.concealedSamples)*1000/48000,u+=o(M.packetsDiscarded),p+=o(M.jitterBufferDelay),_+=o(M.jitterBufferEmittedCount);if(M.type==="transport"&&M.selectedCandidatePairId){let l=t.get(M.selectedCandidatePairId);if(l){S=typeof l.currentRoundTripTime==="number"?o(l.currentRoundTripTime)*1000:null;let c=t.get(l.localCandidateId);if(c)f=c.protocol,n=c.candidateType}}});let y=e&&r>e.at?(r-e.at)/1000:0;return{previous:{at:r,sent:h,received:s},rtt:S,queueMs:_>0?p/_*1000:0,webrtc:{protocol:f,candidateType:n,sendBitrateBps:y?Math.max(0,h-(e?.sent??0))*8/y:0,receiveBitrateBps:y?Math.max(0,s-(e?.received??0))*8/y:0,packetsLost:i,jitterMs:a,concealedMs:m,packetsDiscarded:u,jitterBufferMs:_>0?p/_*1000:0}}}class lt{callbacks;stopped=!1;generation=0;socket;pc;stream;context;capture;whisper;destination;speaker;remoteAudio;speakerMeter;carrierStalled=!1;deliveryDegraded=!1;ready=!1;peer=!1;muted=!1;whisperEpoch=null;whisperBase=null;whisperResampler=new W;timer;retry;cancelSetup;recovering=!1;recoveryGeneration=0;recoveryExpiry;ringback;options;workletURL;events=[];previous;reportedLoss=0;clientEpoch=crypto.randomUUID();nativeCounts;completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};runtime=new tt((t)=>this.recordSessionEvent(t));diagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t){this.callbacks=t}async start(t,e,h){if(this.options=e,this.workletURL=h,!h)throw Error("WebRTC requires the shared Telephony audio processor");if(typeof RTCPeerConnection!=="function")throw Error("WebRTC audio is unavailable in this browser");$(e),await this.connect(t)}current(t){return!this.stopped&&this.generation===t}guard(t){if(!this.current(t))throw Error("Audio session cancelled")}async connect(t){let e=++this.generation,h=this.options;this.ready=this.peer=!1,this.previous=void 0,this.reportedLoss=this.completedCounts.packetsLost;let s=await navigator.mediaDevices.getUserMedia({audio:I(h)});if(!this.current(e))throw s.getTracks().forEach((f)=>f.stop()),Error("Audio session cancelled");this.stream=s;let i=s.getAudioTracks()[0];if(!i)throw Error("No microphone audio track");let a=i.getSettings();this.diagnostics={...this.diagnostics,microphoneSampleRate:a.sampleRate??0,microphoneChannelCount:a.channelCount??1,echoCancellation:typeof a.echoCancellation==="boolean"?a.echoCancellation:null,noiseSuppression:a.noiseSuppression??null,autoGainControl:a.autoGainControl??null,micInputGainDb:h.inputGainDB},i.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),i.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),i.onended=()=>{if(this.current(e))this.disconnected(e,"microphone_ended")},this.context=new AudioContext({latencyHint:"interactive"});let m=this.context;if(m.onstatechange=()=>this.runtime.context(m.state),this.runtime.context(m.state),await m.resume(),this.guard(e),h.outputDeviceId&&"setSinkId"in m)await m.setSinkId(h.outputDeviceId);await m.audioWorklet.addModule(this.workletURL),this.guard(e),this.diagnostics.audioContextRate=m.sampleRate,this.capture=new AudioWorkletNode(m,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:h.inputGainDB,highpassFilter:h.highpassFilter}}),this.destination=m.createMediaStreamDestination(),this.destination.channelCount=1,m.createMediaStreamSource(s).connect(this.capture).connect(this.destination),this.capture.port.onmessage=(f)=>{if(f.data?.type==="capture.stats"){let n=(y)=>y>0?20*Math.log10(y):null;this.diagnostics.micActiveRmsDbfs=n(f.data.active_rms),this.diagnostics.micPeakDbfs=n(f.data.pre_peak),this.diagnostics.micPostPeakDbfs=n(f.data.post_peak),this.diagnostics.micLimiterReductionDb=f.data.limiter_reduction_db??0;let r=0;if(this.speakerMeter){let y=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(y),r=Math.sqrt(y.reduce((M,l)=>M+l*l,0)/y.length)}A(()=>this.callbacks.onLevels?.(o(f.data.active_rms,1),r))}},this.speaker=m.createGain(),this.speaker.gain.value=h.outputVolume??1,this.speaker.connect(m.destination),this.speakerMeter=m.createAnalyser(),this.speakerMeter.fftSize=256;let u=m.createGain();u.gain.value=0,this.speaker.connect(this.speakerMeter).connect(u).connect(m.destination),this.whisper=new AudioWorkletNode(m,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:$(h)}),this.whisper.connect(this.speaker),this.gate();let p=new WebSocket(Ut(t));p.binaryType="arraybuffer",this.socket=p;let _=!1,S=!1;await new Promise((f,n)=>{let r=(l)=>{if(S)return;S=!0,clearTimeout(y),this.cancelSetup=void 0,l?n(l):f()},y=setTimeout(()=>r(Error("WebRTC audio connection timed out")),18000);this.cancelSetup=()=>r(Error("Audio session cancelled"));let M=()=>{if(this.current(e)&&this.ready&&_)this.gate(),r(),A(()=>this.callbacks.onState?.("live"))};p.onmessage=(l)=>{if(!this.current(e))return;if(l.data instanceof ArrayBuffer){this.playWhisper(l.data);return}if(typeof l.data!=="string")return;let c;try{c=JSON.parse(l.data)}catch{return}if(c.type==="webrtc.config"){if(this.pc){r(Error("Repeated WebRTC configuration"));return}(async()=>{let w=new RTCPeerConnection({iceServers:c.ice_servers??[]});this.pc=w,this.destination.stream.getAudioTracks().forEach((T)=>{let F=w.addTrack(T,this.destination.stream),P=F.getParameters();if(P.encodings?.length)P.encodings[0].maxBitrate=32000,F.setParameters(P).catch(()=>{})}),w.ontrack=(T)=>{if(!this.current(e))return;if("jitterBufferTarget"in T.receiver)try{T.receiver.jitterBufferTarget=h.playbackTargetMs??60}catch{}let F=new MediaStream([T.track]),P=new Audio;P.muted=!0,P.srcObject=F,this.remoteAudio=P,P.play().catch(()=>A(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction."))),m.createMediaStreamSource(F).connect(this.speaker)},w.onconnectionstatechange=()=>{if(!this.current(e))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:w.connectionState}),w.connectionState==="connected")_=!0,M();if(w.connectionState==="failed"||w.connectionState==="closed")if(!S)r(Error("WebRTC connection failed"));else this.disconnected(e,"webrtc_"+w.connectionState)};let X=await w.createOffer();this.guard(e),await w.setLocalDescription(X),await new Promise((T,F)=>{if(w.iceGatheringState==="complete"){T();return}let P=setTimeout(()=>{w.onicegatheringstatechange=null,F(Error("ICE gathering timed out"))},7000);w.onicegatheringstatechange=()=>{if(w.iceGatheringState==="complete")clearTimeout(P),w.onicegatheringstatechange=null,T()}}),this.guard(e),p.send(JSON.stringify({type:"webrtc.offer",sdp:w.localDescription.sdp}))})().catch((w)=>r(w instanceof Error?w:Error("WebRTC setup failed")));return}if(c.type==="webrtc.answer"){this.pc?.setRemoteDescription({type:"answer",sdp:c.sdp}).catch(()=>r(Error("Invalid WebRTC answer")));return}if(c.type==="ready")this.ready=!0,this.send({type:"media.capabilities",version:2,versions:[3],whisper:!0}),M();if(c.type==="peer.connected")this.peer=!0,this.gate();if(c.type==="peer.disconnected")this.peer=!1,this.gate(),A(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));if(c.type==="call.status")A(()=>this.callbacks.onCallStatus?.(c));if(c.type==="call.error")if(!S)r(Error(c.detail??"Carrier activation failed"));else this.stop(),A(()=>this.callbacks.onState?.("error",c.detail));if(c.type==="call.ended"||c.type==="session.replaced")if(!S)r(Error(c.type));else this.stop(),A(()=>this.callbacks.onState?.("ended",c.type));if(c.type==="audio.health"){if(this.diagnostics.audioHealth={state:c.state,reason:c.reason,stages:c.stages},A(()=>this.callbacks.onAudioHealth?.(c)),c.state==="audio_degraded"&&!this.deliveryDegraded)this.deliveryDegraded=!0,A(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));else if(this.deliveryDegraded&&c.state==="healthy"&&c.stages?.telephony_to_browser?.state==="healthy"&&m.state==="running")this.deliveryDegraded=!1,A(()=>this.callbacks.onNotice?.("Speech delivery restored."))}if(c.type==="media.delivery"){if(c.state==="stalled")A(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));else if(c.state==="flowing"&&this.carrierStalled)A(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));this.carrierStalled=c.state==="stalled"}if(c.type==="call.notice")A(()=>this.callbacks.onNotice?.(c.detail??c.reason??"Audio delivery notice"));if(c.type==="coach.state")this.whisperEpoch=c.talking&&Number.isInteger(c.epoch)?c.epoch:null,this.whisperBase=null,this.whisper?.port.postMessage({type:"whisper.clear"})},p.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"}),p.onclose=(l)=>{if(!this.current(e))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(l.code),was_clean:l.wasClean}),!S){r(Object.assign(Error("WebRTC signaling unavailable"),l.code===1008?{status:403}:{}));return}if(l.code===1008){this.stop(),A(()=>this.callbacks.onState?.("error","Media authorization ended"));return}this.disconnected(e,"signaling_closed")}}),this.guard(e),this.timer=setInterval(()=>{this.runtime.tick(),this.statistics(e)},1000),await this.statistics(e)}send(t){if(this.socket?.readyState===WebSocket.OPEN&&this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(t))}catch{}}gate(){let t=this.ready&&this.peer&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!t}),this.destination?.stream.getAudioTracks().forEach((e)=>e.enabled=t)}async statistics(t){if(!this.current(t)||!this.pc)return;try{let e=Jt(await this.pc.getStats(),this.previous);if(!this.current(t))return;if(this.previous=e.previous,this.nativeCounts={packetsLost:e.webrtc.packetsLost,packetsDiscarded:e.webrtc.packetsDiscarded,concealedMs:e.webrtc.concealedMs},e.webrtc.packetsLost+=this.completedCounts.packetsLost,e.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded,e.webrtc.concealedMs+=this.completedCounts.concealedMs,e.webrtc.packetsLost>this.reportedLoss)this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,{timestamp:new Date().toISOString(),direction:"carrier_to_operator",reason:"webrtc_packet_loss",duration_ms:0}].slice(-100),this.reportedLoss=e.webrtc.packetsLost;this.diagnostics={...this.diagnostics,rttMs:e.rtt,queueMs:e.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,e.queueMs),targetMs:this.options?.playbackTargetMs??60,webrtc:e.webrtc,playbackSequenceGaps:e.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};let h=this.stream?.getAudioTracks()[0];this.send({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,media_transport:"webrtc",codec:"opus",webrtc:e.webrtc,connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:h?.readyState,microphone_device_muted:h?.muted,session_events:this.events,rtt_ms:e.rtt===null?null:Math.round(e.rtt),playback_queue_ms:Math.round(e.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:e.webrtc.packetsLost,drop_events:this.diagnostics.dropEvents,timing:{runtime:this.runtime.counters}}}),A(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}catch{}}playWhisper(t){if(t.byteLength<17||t.byteLength>176||this.whisperEpoch===null||!this.context)return;let e=new DataView(t);if(e.getUint32(0,!0)!==827805761||e.getUint32(4,!0)!==this.whisperEpoch)return;let h=e.getFloat64(8,!0);if(!Number.isFinite(h))return;let s=performance.timeOrigin+performance.now()-h;this.whisperBase=this.whisperBase===null?s:Math.min(this.whisperBase,s);let i=Math.max(0,s-this.whisperBase);if(i>200)return;let a=new Float32Array(t.byteLength-16);for(let u=0;u<a.length;u++){let p=~e.getUint8(u+16)&255,_=((p&15)<<3)+132<<(p>>4&7);a[u]=(p&128?132-_:_-132)/32768}let m=this.context.sampleRate===8000?a:this.whisperResampler.process(a,8000,this.context.sampleRate);this.whisper?.port.postMessage({type:"whisper",frame:m,received_audio_ms:this.context.currentTime*1000-i},[m.buffer])}disconnected(t,e){if(!this.current(t)||this.recovering)return;this.recovering=!0,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:e}),this.cleanup(),A(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));let h=++this.recoveryGeneration,s=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===h;this.recoveryExpiry=setTimeout(()=>{if(!s())return;++this.recoveryGeneration,this.recovering=!1,clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup(),A(()=>this.callbacks.onState?.("error","WebRTC audio recovery timed out; reconnect audio to the existing call."))},30000);let i=async()=>{if(!s())return;try{if(!this.callbacks.refreshMediaURL)throw Error("Fresh media authorization is required");let a=await this.callbacks.refreshMediaURL();if(!s())return;if(await this.connect(a),!s())return;if(this.pc?.connectionState!=="connected")throw Error("WebRTC disconnected during recovery");clearTimeout(this.recoveryExpiry),this.recovering=!1,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected"})}catch(a){if(!s())return;if(this.cleanup(),Tt(a)){clearTimeout(this.recoveryExpiry),this.recovering=!1,A(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return}this.retry=setTimeout(()=>void i(),1000)}};i()}recordSessionEvent(t){this.events=[...this.events,t].slice(-100),A(()=>this.callbacks.onSessionEvent?.(t))}setMuted(t){if(this.muted!==t)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:t?"muted":"unmuted"});if(this.muted=t,this.gate(),t)this.send({type:"interrupt"})}sendDTMF(t){if(!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");this.send({type:"dtmf",digits:t})}setOutputVolume(t){if(!Number.isFinite(t)||t<0||t>1)throw RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=t;if(this.options)this.options.outputVolume=t}startRingback(t){if(this.stopRingback(),this.context&&this.speaker)this.ringback=it(this.context,this.speaker,mt(t))}stopRingback(){this.ringback?.(),this.ringback=void 0}cleanup(){if(this.nativeCounts){for(let s of["packetsLost","packetsDiscarded","concealedMs"])this.completedCounts[s]+=this.nativeCounts[s];this.nativeCounts=void 0}++this.generation,this.ready=this.peer=!1,this.stopRingback(),clearInterval(this.timer),this.timer=void 0;let t=this.socket;if(this.socket=void 0,t)t.onclose=t.onmessage=t.onerror=null,t.close();let e=this.pc;if(this.pc=void 0,e)e.onconnectionstatechange=e.ontrack=null,e.close();if(this.remoteAudio)this.remoteAudio.pause(),this.remoteAudio.srcObject=null,this.remoteAudio=void 0;this.stream?.getTracks().forEach((s)=>{s.onended=null,s.stop()}),this.stream=void 0,this.destination?.stream.getTracks().forEach((s)=>s.stop()),this.destination=void 0,this.capture?.disconnect(),this.capture=void 0,this.whisper?.disconnect(),this.whisper=void 0,this.whisperEpoch=null,this.whisperBase=null,this.whisperResampler=new W;let h=this.context;if(this.context=void 0,h)h.onstatechange=null,h.close().catch(()=>{});this.speaker=void 0,this.speakerMeter=void 0}stop(){if(this.stopped)return;this.stopped=!0,++this.recoveryGeneration,clearTimeout(this.recoveryExpiry),clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup()}}function Rt(t,e,h){let s,i=!1,a=!1;return{async start(m,u){let p=Ht(u.mediaTransport),_=async(S)=>{if(i)throw Error("Audio session cancelled");let f=S();s=f,f.setMuted(a);try{if(await f.start(m,u),i)throw Error("Audio session cancelled")}catch(n){if(f.stop(),s===f)s=void 0;throw n}};if(p==="websocket"){await _(e);return}try{await _(h)}catch(S){if(p!=="auto"||i||Tt(S))throw S;A(()=>t.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"})),await _(e)}},stop(){i=!0,s?.stop(),s=void 0},setMuted(m){a=m,s?.setMuted(m)},setOutputVolume(m){s?.setOutputVolume(m)},sendDTMF(m){s?.sendDTMF(m)},recordSessionEvent(m){s?.recordSessionEvent?.(m)},startRingback(m){s?.startRingback?.(m)},stopRingback(){s?.stopRingback?.()}}}async function ot(){return(await navigator.mediaDevices.enumerateDevices()).filter((e)=>e.kind==="audioinput").map((e,h)=>({deviceId:e.deviceId,label:e.label||`Microphone ${h+1}`}))}function Ft(t,e){let h=new yt(t,!1),s,i=!1,a,m=()=>{return i=!0,a??=(async()=>{try{await h.cancel()}finally{s?.dispose(),s=void 0}})()};return{async start(u={}){if(i)throw Error("Microphone preview is already used");i=!0;try{if(s=e?await at(e,!0):j(!0),a)throw s.dispose(),s=void 0,Error("Microphone preview was cancelled");await h.start(s.urls[0],{...L,...u})}catch(p){throw await m(),p}},stop:m}}function Zt(t){return{async preflight(e){(await navigator.mediaDevices.getUserMedia({audio:I(e)})).getTracks().forEach((s)=>s.stop())},create(e){let h=new nt(e),s,i=!1,a=()=>{i=!0;try{h.stop()}finally{s?.dispose(),s=void 0}};return{async start(m,u){try{if(s=t?await at(t):j(),i)throw Error("Audio session was cancelled");await h.start(m,s.urls[0],s.urls[1],u)}catch(p){throw a(),p}},stop:a,recordSessionEvent:(m)=>h.recordSessionEvent(m),setMuted:(m)=>h.setMuted(m),sendDTMF:(m)=>h.sendDTMF(m),setOutputVolume:(m)=>h.setOutputVolume(m),startRingback:(m)=>h.startRingback(m),stopRingback:()=>h.stopRingback()}}}}function Qt(t){let e=Zt(t);return{preflight:(h)=>e.preflight(h),create(h){return Rt(h,()=>e.create(h),()=>{let s=new lt(h),i,a=!1;return{async start(m,u){if(i=t?await at(t):j(),a)throw i.dispose(),i=void 0,Error("Audio session cancelled");await s.start(m,u,i.urls[0])},stop(){a=!0,s.stop(),i?.dispose(),i=void 0},setMuted:(m)=>s.setMuted(m),sendDTMF:(m)=>s.sendDTMF(m),setOutputVolume:(m)=>s.setOutputVolume(m),recordSessionEvent:(m)=>s.recordSessionEvent(m),startRingback:(m)=>s.startRingback(m),stopRingback:()=>s.stopRingback()}})}}}var ut=`// Passive receiver only. Both directions share one rendering clock and delay.
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
`;function Kt(t){if(t.byteLength<26||t.byteLength>984||t.byteLength%2)throw Error("Invalid listener audio frame");let e=new DataView(t),h=e.getUint32(4,!0);if(e.getUint32(0,!0)!==827085889||h>1)throw Error("Invalid listener audio protocol");let s=Number(e.getBigUint64(8,!0)),i=Number(e.getBigUint64(16,!0));if(!Number.isSafeInteger(s)||!Number.isSafeInteger(i))throw Error("Invalid listener audio timing");let a=new Float32Array((t.byteLength-24)/2);for(let m=0;m<a.length;m++)a[m]=e.getInt16(24+m*2,!0)/32768;return{direction:h,sequence:s,timestampMS:i,frame:a}}function Lt(t,e,h,s){if(t.length<1||t.length>480||!Number.isFinite(s)||s<0||!Number.isInteger(e)||e<1||e>4294967295)throw Error("Invalid coaching capture");let i=new ArrayBuffer(24+t.length*2),a=new DataView(i);a.setUint32(0,826496833,!0),a.setUint32(4,e,!0),a.setUint32(8,h>>>0,!0),a.setFloat64(16,s,!0);for(let m=0;m<t.length;m++){let u=Math.max(-1,Math.min(1,t[m]));a.setInt16(24+m*2,u<0?u*32768:u*32767,!0)}return i}function Gt(t,e={}){return{create(h){let s,i,a,m,u=!1,p=[],_=!1,S=0,f=!1,n=!1,r,y,M,l,c,w,X,T=!1,F,P,pt=0,ht=!1,At=[0,0],qt=[0,0],J=[void 0,void 0],Bt=[new W,new W],kt=e.outputVolume??1,_t,H=()=>{let b=++S;if(f=!1,n=!1,X?.(b,Error("Coaching cancelled")),w)clearInterval(w);if(w=void 0,c?.port1.close(),c?.port2.close(),c=void 0,y?.disconnect(),y=void 0,M?.disconnect(),M=void 0,l?.disconnect(),l=void 0,r?.getTracks().forEach((O)=>{O.onended=null,O.stop()}),r=void 0,_&&ht&&m?.readyState===WebSocket.OPEN)m.send(JSON.stringify({type:"coach.stop",generation:b}));h.onTalking?.(!1)},Ct=()=>H(),Pt=()=>{if(document.visibilityState!=="visible")H()},ct=()=>{if(u)return;if(H(),u=!0,_t?.(),window.removeEventListener("blur",Ct),document.removeEventListener("visibilitychange",Pt),m)m.onmessage=m.onclose=m.onerror=null,m.close();if(i?.disconnect(),a?.disconnect(),s)s.onstatechange=null,s.close().catch(()=>{});for(let b of p)URL.revokeObjectURL(b)},Z=(b)=>{if(!u)ct(),h.onClose(b)},U=()=>{if(u)throw Error("Listening cancelled")};return{async start(b,O){_=O?.coaching===!0,U();try{if(s=new AudioContext({latencyHint:"interactive"}),s.state==="suspended")await s.resume();if(U(),e.outputDeviceId&&"setSinkId"in s)await s.setSinkId(e.outputDeviceId);U();let R,st=new URL(t.mcpURL(),location.href);if(st.origin===location.origin){let g=`/ui/frontend/listener-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(ut))),(x)=>x.toString(16).padStart(2,"0")).join("")}.js`,k=await t.get(g,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}});if(U(),k!==ut)throw Error("Listener audio asset integrity mismatch");let Y=new URL(`/api/apps/telephony/_install/${t.installId}${g}`,st);Y.searchParams.set("project_id",t.projectId),Y.searchParams.set("install_id",String(t.installId)),R=Y.href}else{let Q=URL.createObjectURL(new Blob([ut],{type:"text/javascript"}));p.push(Q),R=Q}if(await s.audioWorklet.addModule(R),U(),i=new AudioWorkletNode(s,"telephony-listener",{numberOfInputs:0,outputChannelCount:[e.stereo?2:1],processorOptions:{stereo:e.stereo}}),a=s.createGain(),a.gain.value=kt,i.connect(a).connect(s.destination),i.onprocessorerror=()=>Z("listener_audio_error"),i.port.onmessage=({data:Q})=>{if(Q?.type==="listener.diagnostics"&&!u)try{h.onDiagnostics?.({...Q,sequence_gaps:[...qt],network_excess_ms:pt,network_dropped_ms:[...At]})}catch{}},s.onstatechange=()=>{if(s?.state==="suspended"||s?.state==="interrupted")Z("listener_audio_paused")},await new Promise((Q,g)=>{let k=!1,Y=setTimeout(()=>{x(Error("Listener connection timed out")),Z("listener_network_error")},1e4),x=(C)=>{if(k)return;k=!0,clearTimeout(Y),_t=void 0,C?g(C):Q()};_t=()=>x(Error("Listening cancelled")),m=new WebSocket(b),m.binaryType="arraybuffer",m.onmessage=({data:C})=>{if(u||!s||!i)return;try{if(typeof C==="string"){let V=JSON.parse(C);if(V.type==="listener.ready"){if(_&&V.coaching!==!0)throw Error("Coaching not authorized");ht=!0,x(),h.onReady()}else if(V.type==="coach.started")X?.(V.generation);else if(V.type==="coach.rejected")X?.(S,Error(V.detail||"Coaching unavailable"));else if(V.type==="coach.stopped"&&(V.generation===void 0||V.generation===S))H();return}if(!ht||!(C instanceof ArrayBuffer))return;let E=Kt(C),d=E.direction;if(J[d]!==void 0&&E.sequence<J[d])return;if(J[d]!==void 0&&E.sequence>J[d])qt[d]+=E.sequence-J[d];J[d]=E.sequence+1;let rt=performance.now()-E.timestampMS;if(P=Math.min(P??rt,rt),pt=Math.max(0,rt-P),pt>200){At[d]+=E.frame.length*1000/24000;return}F??=s.currentTime*1000+60-E.timestampMS;let Et=Bt[d].process(E.frame,24000,s.sampleRate);i.port.postMessage({direction:d,frame:Et,playAtMS:E.timestampMS+F},[Et.buffer])}catch{x(Error("Invalid listener media")),Z("listener_protocol_error")}},m.onerror=()=>{x(Error("Listener connection failed")),Z("listener_network_error")},m.onclose=(C)=>{x(Error(C.reason||"Listener disconnected")),Z(C.reason||"listener_disconnected")}}),U(),_)window.addEventListener("blur",Ct),document.addEventListener("visibilitychange",Pt)}catch(R){throw ct(),R}},async startTalking(){if(U(),!_||!ht||!s||!m||m.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(f)return;f=!0;let b=++S,O=()=>!u&&f&&b===S;try{let R=await navigator.mediaDevices.getUserMedia({audio:{echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,...e.inputDeviceId?{deviceId:{exact:e.inputDeviceId}}:{}}});if(!O()){R.getTracks().forEach((g)=>g.stop());return}if(r=R,r.getAudioTracks().forEach((g)=>{g.onended=()=>H()}),!T){let g=new URL(t.mcpURL(),location.href),k;if(g.origin===location.origin){let x=`/ui/frontend/worklet-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(D))),(d)=>d.toString(16).padStart(2,"0")).join("")}.js`;if(await t.get(x,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==D)throw Error("Coaching audio asset integrity mismatch");let E=new URL(`/api/apps/telephony/_install/${t.installId}${x}`,g);E.searchParams.set("project_id",t.projectId),E.searchParams.set("install_id",String(t.installId)),k=E.href}else k=URL.createObjectURL(new Blob([D],{type:"text/javascript"})),p.push(k);if(!O())return;await s.audioWorklet.addModule(k),T=!0}if(!O())return;if(await new Promise((g,k)=>{let Y=setTimeout(()=>{X=void 0,k(Error("Coaching activation timed out"))},3000);X=(x,C)=>{if(C||x===b)clearTimeout(Y),X=void 0,C?k(C):g()},m.send(JSON.stringify({type:"coach.start",generation:b}))}),!O())return;y=new AudioWorkletNode(s,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:0,highpassFilter:!0}}),M=s.createMediaStreamSource(R),l=s.createGain(),l.gain.value=0,M.connect(y).connect(l).connect(s.destination),c=new MessageChannel;let st=0,Q=new W;c.port1.onmessage=({data:g})=>{if(!O()||!n||g?.type!=="capture"||!(g.frame instanceof Float32Array)||!s||!m)return;if(!Number.isFinite(g.timestamp_ms)||s.currentTime*1000-g.timestamp_ms>150||m.bufferedAmount>2880||m.readyState!==WebSocket.OPEN)return;let k=Q.process(g.frame,g.sample_rate,24000);if(k.length>0&&k.length<=480)m.send(Lt(k,b,st++,g.timestamp_ms))},c.port1.start(),y.port.postMessage({type:"transport",port:c.port2},[c.port2]),n=!0,h.onTalking?.(!0),w=setInterval(()=>{if(O()&&m?.readyState===WebSocket.OPEN)m.send(JSON.stringify({type:"coach.keepalive",generation:b}))},1000)}catch(R){if(O())throw H(),R}},stopTalking:H,stop:ct,setOutputVolume(b){if(!Number.isFinite(b)||b<0||b>1)throw RangeError("Listener volume must be 0–1");if(kt=b,a)a.gain.value=b}}}}}class wt{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;constructor(t,e={}){this.client=t;this.options=e;this.runtime=e.runtime??Gt(t.app,e)}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.observers.add(t),()=>{this.observers.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let e of this.observers)try{e(this.snapshot)}catch{}}async listen(t){return this.begin(t,!1)}async coach(t){return this.begin(t,!0)}async begin(t,e){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=e,this.retryUntil=0,this.retryDelay=500;let h=++this.generation;return this.update({state:"connecting",callId:t,detail:void 0,coaching:e,talking:!1}),this.connect(t,h)}async connect(t,e){let h,s=++this.attempt,i=()=>e===this.generation&&s===this.attempt&&!this.disposed;try{if(h=this.coaching?await this.client.coachSession(t):await this.client.listenSession(t),!i()){await this.client.stopListening(h).catch(()=>{});return}this.session=h;let a=this.runtime.create({onReady:()=>{if(i())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0})},onClose:(m)=>{if(i())this.disconnected(m,t,e)},onTalking:(m,u)=>{if(i())this.update({talking:m,detail:u})},onDiagnostics:(m)=>{if(i())try{this.options.onDiagnostics?.(m)}catch{}}});if(this.audio=a,this.lease=new v(h.lease_seconds,()=>this.client.renewListening(h),(m)=>{if(i())this.disconnected(m==="revoked"?"access_revoked":"media_disconnected",t,e,m==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,h.lease_started_ms),await a.start(this.client.listenerMediaURL(h),{coaching:h.coaching===!0}),!i())a.stop()}catch(a){if(!i()||h&&this.session!==h)return;let m=a?.status,u;try{u=JSON.parse(a.body??"{}").code}catch{}let p=u==="call_ended"?"call_ended":m===401||m===403||m===404?"access_revoked":"listener_disconnected";throw this.disconnected(p,t,e,String(a)),a}}cleanup(){this.lease?.stop(),this.lease=void 0;let t=this.audio;this.audio=void 0,t?.stop();let e=this.session;return this.session=void 0,e?this.client.stopListening(e).catch(()=>{}):Promise.resolve()}disconnected(t,e,h,s=t){if(h!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let i=t==="call_ended",a=t==="access_revoked",m=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(t);if(!this.coaching&&!i&&!a&&m&&this.options.reconnect!==!1){if(this.retryUntil||=Date.now()+30000,Date.now()<this.retryUntil){this.update({state:"reconnecting",detail:s,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,h===this.generation&&!this.disposed)this.connect(e,h).catch(()=>{})},this.retryDelay),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:i?"call_ended":a?"access_revoked":"disconnected",detail:s,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(t){this.audio?.setOutputVolume(t)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let t=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1}),await t}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}function It(t){if(!t)return"placing";if(bt(t))return"ended";if(t==="ringing")return"ringing";if(t==="answered"||t==="in-progress")return"connected";return"placing"}var B=(t)=>t instanceof Error?t.message:String(t);class gt{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(t,e={}){this.client=t;this.options=e;if(this.runtime=e.audioRuntime??Qt(t.app),this.audioOptions={...L,...e.audio,...e.mediaTransport?{mediaTransport:e.mediaTransport}:{}},$(this.audioOptions),this.interval=e.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.assertOpen(),this.listeners.add(t),()=>{this.listeners.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let e of this.listeners)try{e(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(t){if(this.disposed||t!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(t){return!this.disposed&&t===this.generation}finish(t){if(this.current(t))this.update({busy:!1})}cancellable(t,e){let h=this.cancellation.signal;return new Promise((s,i)=>{let a=()=>{h.removeEventListener("abort",a),i(Error("Softphone operation cancelled"))};if(h.addEventListener("abort",a,{once:!0}),t.then(s,i).finally(()=>h.removeEventListener("abort",a)),!this.current(e)||h.aborted)a()})}begin(t){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||t&&this.session)throw Error("Softphone already has an active operation or call");let e=this.invalidate();return this.update({busy:!0,detail:void 0}),e}async dial(t){let e=this.begin(!0),h,s=!1;try{this.assertCurrent(e);let i={to:t.to.trim(),from:t.from?.trim(),timeout_sec:t.timeout_sec,recording:t.recording},a=JSON.stringify(i);if(!this.intent||this.intent.value!==a||t.idempotency_key&&t.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:a,request:{...i,idempotency_key:t.idempotency_key||crypto.randomUUID()}};return await this.cancellable(this.runtime.preflight(this.audioOptions),e),this.assertCurrent(e),h=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(e),s=!0,this.outbound=!0,await this.attachAudio(h,e),h.call_id}catch(i){if(h&&(this.current(e)||!s||this.disposed))await this.recoverStartup(h,e,()=>this.client.hangup(h.call_id));if(this.current(e))this.update({detail:B(i)});throw i}finally{this.finish(e)}}async answer(t,e={}){let h=this.begin(!0),s,i=!1;try{this.assertCurrent(h),s=await this.client.answer(t,e),this.assertCurrent(h),i=!0,this.outbound=!1,await this.attachAudio(s,h)}catch(a){if(s&&(this.current(h)||!i||this.disposed))await this.recoverStartup(s,h,()=>this.client.release(s));if(this.current(h))this.update({detail:B(a)});throw a}finally{this.finish(h)}}async recoverStartup(t,e,h){try{if(await h(),this.current(e))this.clearCall()}catch{if(this.current(e))this.session=t,this.update({callId:t.call_id,audioState:"error"}),this.startPolling()}}async join(t){await this.answer(t,{rejoin:!0})}async attach(t){await this.acquireSession(t,!1)}async takeover(t){await this.acquireSession(t,!0)}async acquireSession(t,e){let h=this.begin(!0);try{await this.cancellable(this.runtime.preflight(this.audioOptions),h),this.assertCurrent(h);let s=await(e?this.client.takeover(t):this.client.attach(t));this.assertCurrent(h),await this.attachAudio(s,h),await this.reconcileAttachedCall(s,h)}catch(s){if(this.current(h))this.update({detail:B(s)});throw s}finally{this.finish(h)}}async reconnect(t){if(!this.session)throw Error("No call to reconnect");let e={...this.audioOptions,...t};$(e);let h=this.begin(!1);this.audioOptions=e;let s=this.session.call_id;try{let i=await this.client.attach(s);this.assertCurrent(h),await this.attachAudio(i,h),await this.reconcileAttachedCall(i,h)}catch(i){if(this.current(h))this.update({detail:B(i)});throw i}finally{this.finish(h)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let t=this.session;if(!t){this.cancellation.abort();return}let e=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let h=(async()=>{try{if(await this.client.hangup(t.call_id),this.current(e))this.clearCall()}catch(s){if(this.current(e))this.update({audioState:"error",detail:B(s)}),this.startPolling();throw s}finally{this.finish(e)}})();this.hangingUp=h;try{await h}finally{if(this.hangingUp===h)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(t){this.assertOpen();let e=this.session;if(!e)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let h=this.generation;this.update({busy:!0,detail:void 0});let s;try{s=this.client[t](e.call_id)}catch(i){if(this.current(h)&&this.session===e)this.update({busy:!1,detail:B(i)});throw i}this.controlPending=s;try{let i=await s;if(this.current(h)&&this.session===e)this.update({holdState:i.hold_state,recordingState:i.recording_state,controlError:i.control_error,capabilities:i.capabilities});return i}catch(i){if(this.current(h)&&this.session===e)this.update({detail:B(i)});throw i}finally{if(this.controlPending===s)this.controlPending=void 0;if(this.current(h)&&this.session===e)this.update({busy:!1})}}configureAudio(t){this.assertOpen();let e={...this.audioOptions,...t};$(e),this.audioOptions=e}setMuted(t){this.assertOpen(),this.audio?.setMuted(t),this.update({muted:t})}setOutputVolume(t){if(this.assertOpen(),!Number.isFinite(t)||t<0||t>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=t,this.audio?.setOutputVolume(t)}sendDTMF(t){if(this.assertOpen(),!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(t)}observeCall(t){if(this.disposed||t.id!==this.session?.call_id)return;if(t.direction)this.outbound=t.direction==="outbound";if(bt(t.status)){this.invalidate(),this.clearCall(t.status),this.update({busy:!1,phase:"ended",termination:t.termination,answeredBy:t.answered_by??this.snapshot.answeredBy,endedAt:t.ended_at});return}this.update({carrierStatus:t.status,phase:It(t.status),answeredBy:t.answered_by??this.snapshot.answeredBy,holdState:t.hold_state??this.snapshot.holdState,recordingState:t.recording_state??this.snapshot.recordingState,controlError:t.control_error??this.snapshot.controlError,capabilities:t.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let t=this.options.ringback;return typeof t==="object"&&t?t.country:void 0}syncRingback(){let t=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(t&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!t&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(t,e){this.assertCurrent(e),this.stopAudio(),this.session=t,this.update({callId:t.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let h,s=()=>!this.disposed&&h!==void 0&&this.audio===h,i=(m)=>{if(s())try{m()}catch{}},a;try{if(this.assertCurrent(e),h=this.runtime.create({refreshMediaURL:()=>{if(a)return a;if(!s()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let m=this.generation,u=(async()=>{let _=await this.client.attach(t.call_id);if(this.assertCurrent(m),!s())throw Error("Audio connection no longer active");return t=_,this.session=_,this.startLease(_),this.client.mediaURL(_)})();a=u;let p=()=>{if(a===u)a=void 0};return u.then(p,p),u},onSessionEvent:(m)=>i(()=>this.options.onSessionEvent?.(m)),onState:(m,u)=>{if(!s())return;if(m==="ended"&&u==="call.ended"){this.observeCall({id:t.call_id,status:"completed"});return}if(this.update({audioState:m,detail:u}),s()&&(m==="error"||m==="ended")){if(this.stopAudio(),m==="error")this.reconcileFailedAudio(t,this.generation)}},onLevels:(m,u)=>i(()=>this.options.onLevels?.(m,u)),onAudioHealth:(m)=>i(()=>this.options.onAudioHealth?.(m)),onDiagnostics:(m)=>i(()=>this.options.onDiagnostics?.(m)),onNotice:(m)=>i(()=>this.options.onNotice?.(m)),onCallStatus:(m)=>i(()=>this.observeCall({id:m.call_id,status:m.status,direction:m.direction,answered_at:m.answered_at,ended_at:m.ended_at,answered_by:m.answered_by,termination:m.termination,hold_state:m.hold_state,recording_state:m.recording_state,control_error:m.control_error}))}),this.audio=h,this.assertCurrent(e),this.startPolling(),this.startLease(t),h.setMuted(this.snapshot.muted),await this.cancellable(h.start(this.client.mediaURL(t),this.audioOptions),e),this.assertCurrent(e),!s())throw Error(this.snapshot.detail||"Audio connection ended during setup");h.setMuted(this.snapshot.muted),this.syncRingback()}catch(m){if(s())this.stopAudio();if(this.current(e))this.update({audioState:"error",detail:B(m)});throw m}}async reconcileAttachedCall(t,e){try{let h=await this.client.getCall(t.call_id);if(this.current(e)&&this.session===t&&h)this.observeCall(h)}catch{}}async reconcileFailedAudio(t,e){try{let h=await this.client.getCall(t.call_id);if(!this.current(e)||this.session!==t||!h)return;if(h.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(h)}catch{}}startLease(t){if(this.lease?.stop(),!t.lease_seconds)return;this.lease=new v(t.lease_seconds,()=>this.client.renew(t),(e,h)=>{if(this.disposed||this.session!==t)return;this.stopAudio(),this.update({audioState:"error",detail:e==="expired"?"Audio session expired. Reconnect audio.":`Audio authorization ended: ${B(h??e)}`})},(e)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(e);else try{this.options.onSessionEvent?.(e)}catch{}},t.lease_started_ms)}stopAudio(){this.lease?.stop(),this.lease=void 0;let t=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{t?.stopRingback?.()}catch{}}try{t?.stop()}catch{}if(t)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let t=new AbortController;this.polling=t;let e=async()=>{let h=this.session?.call_id;if(!h||t.signal.aborted)return;try{let s=await this.client.getCall(h,t.signal);if(!t.signal.aborted&&s)this.observeCall(s)}catch(s){if(!t.signal.aborted)this.update({detail:`Call status unavailable: ${B(s)}`})}finally{if(!t.signal.aborted&&this.session&&!this.disposed)this.timer=setTimeout(e,this.interval)}};this.timer=setTimeout(e,this.interval)}clearCall(t){this.stopAudio(),this.stopPolling(),this.session=void 0,this.outbound=!1,this.update({callId:void 0,carrierStatus:t,audioState:"idle",muted:!1,detail:void 0,phase:t?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function jt(t,e="en"){if(t?.reason==="time_limit")return e.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return t?.reason?.replaceAll("_"," ")??""}class Nt extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function vt(t){return t.direction==="inbound"&&t.status==="pending"&&t.answerable!==!1&&!t.routing_waiting&&(t.peer_kind==="human"||Boolean(t.ring_offers?.some((e)=>e.kind==="browser")))}var bt=(t)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(t);function q(t){if(!t||/[\s/\\?#]/.test(t))throw Error("Invalid call ID");return encodeURIComponent(t)}class ft{app;options;listMicrophones=ot;createMicrophonePreview=(t)=>Ft(t,this.app);constructor(t,e={}){this.app=t;this.options=e;if(t.name!=="telephony"||!t.projectId||!t.installId)throw Error("Telephony requires an explicit project and installation")}path(t){if(!this.options.authProvider)return t;return`/user${t}${t.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(t){let e=await this.app.get(this.path("/calls"),{signal:t});if(!Array.isArray(e?.calls)||e.calls.some((h)=>!h||typeof h.id!=="string"||typeof h.status!=="string"))throw Error("Invalid Telephony calls response");return e.calls}async getCall(t,e){let h=await this.app.get(this.path(`/calls?call_id=${q(t)}`),{signal:e});if(!Array.isArray(h?.calls))throw Error("Invalid Telephony call response");return h.calls.find((s)=>s.id===t)}incomingCalls(t){return t.filter(vt)}watchCalls(t,e={}){let h=e.intervalMs??2000;if(!Number.isFinite(h)||h<100)throw Error("Call watch interval must be at least 100 ms");let s=new AbortController,i,a,m=!1,u=!1,p=new Set,_=()=>{clearTimeout(i),s.abort(),a?.close(),e.signal?.removeEventListener("abort",_)},S=(n)=>{try{e.onError?.(n)}catch{_()}};if(e.signal?.addEventListener("abort",_,{once:!0}),e.signal?.aborted)_();let f=async(n)=>{if(s.signal.aborted)return;if(m){u=!0;return}clearTimeout(i),m=!0;let r=performance.now();try{let y=await this.listCalls(s.signal);if(!s.signal.aborted){t(y);for(let M of y){if(M.answerable!==!0||M.status!=="pending")continue;for(let l of M.ring_offers??[]){if(l.kind!=="browser"||!l.id||p.has(l.id))continue;p.add(l.id),this.acknowledgeOffer(M.id,l.id).catch((c)=>{if(![404,405].includes(c?.status??0))p.delete(l.id)})}}try{e.onTiming?.({trigger:n,fetchMs:performance.now()-r})}catch{}}}catch(y){if(!s.signal.aborted){let M=y?.status;try{e.onFailure?.({trigger:n,fetchMs:performance.now()-r,status:typeof M==="number"?M:void 0,error:y})}catch{}S(y)}}finally{if(m=!1,!s.signal.aborted)if(u)u=!1,f("push");else i=setTimeout(()=>void f("poll"),h)}};if(f("poll"),!s.signal.aborted&&e.push!==!1&&typeof this.app.subscribe==="function")try{a=this.app.subscribe(this.path("/calls/events"),(n)=>{if(n.type==="calls.changed")f("push");else if(n.type==="access.revoked")a?.close(),f("push")},{transport:"fetch",signal:s.signal,reconnectDelayMs:250,onError:(n)=>{let r=n?.status;if(r===404||r===405||r===501)a?.close()}})}catch{}return{close:_}}async place(t){if(!/^\+[1-9]\d{7,14}$/.test(t.to)||!t.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let e=G();return this.session(await this.app.post(this.path("/softphone/place"),t),void 0,"media",e)}async answer(t,e={}){let h=G();try{return this.session(await this.app.post(this.path(`/softphone/answer/${q(t)}`),e),t,"media",h)}catch(s){let i=s;if(i.status===409&&typeof i.body==="string"){let a;try{a=JSON.parse(i.body)}catch{}if(a?.code==="offer_expired")throw new Nt}throw s}}async acknowledgeOffer(t,e){await this.app.post(this.path(`/softphone/offer/ack/${q(t)}`),{offer_id:e})}async declineOffer(t,e){await this.app.post(this.path(`/softphone/offer/decline/${q(t)}`),{offer_id:e})}async attach(t){let e=G();return this.session(await this.app.post(this.path(`/softphone/attach/${q(t)}`),{}),t,"media",e)}async takeover(t){let e=G();return this.session(await this.app.post(this.path(`/softphone/takeover/${q(t)}`),{}),t,"media",e)}createCallListener(t={}){return new wt(this,t)}async listenSession(t){let e=G();return this.session(await this.app.post(this.path(`/softphone/listen/${q(t)}`),{}),t,"listen-media",e)}async coachSession(t){let e=G(),h=this.session(await this.app.post(this.path(`/softphone/coach/${q(t)}`),{}),t,"listen-media",e);if(h.coaching!==!0)throw Error("Invalid coaching session");return h}async renewListening(t){return this.app.post(this.path(`/softphone/${t.coaching?"coach-renew":"listen-renew"}/${q(t.call_id)}`),{session_token:t.session_token})}async stopListening(t){await this.app.post(this.path(`/softphone/${t.coaching?"coach-stop":"listen-stop"}/${q(t.call_id)}`),{session_token:t.session_token})}async listenerAudit(t){return this.app.get(this.path(`/softphone/listen-audit/${q(t)}`))}outboundNumbers(){return this.app.get(this.path("/softphone/numbers"))}async renew(t){return this.app.post(this.path(`/softphone/renew/${q(t.call_id)}`),{session_token:t.session_token})}async release(t){if(!t.session_token)throw Error("Answer session has no release token");await this.app.post(this.path(`/softphone/release/${q(t.call_id)}`),{session_token:t.session_token})}async hangup(t){await this.app.post(this.path(`/calls/${q(t)}/hangup`),{})}hold(t){return this.app.post(this.path(`/calls/${q(t)}/hold`),{})}resume(t){return this.app.post(this.path(`/calls/${q(t)}/resume`),{})}pauseRecording(t){return this.app.post(this.path(`/calls/${q(t)}/pause-recording`),{})}resumeRecording(t){return this.app.post(this.path(`/calls/${q(t)}/resume-recording`),{})}createSoftphone(t={}){return new gt(this,t)}mediaURL(t){return this.resolveMediaURL(t,"media")}listenerMediaURL(t){return this.resolveMediaURL(t,"listen-media")}resolveMediaURL(t,e){let h=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),s=new URL(t.media_url,h),i=`/api/apps/telephony/_install/${this.app.installId}/softphone/${e}/${q(t.call_id)}/`;if(s.origin!==h.origin||!["http:","https:"].includes(s.protocol)||s.username||s.password||s.search||s.hash||!s.pathname.startsWith(i)||!/^[A-Za-z0-9_-]+$/.test(s.pathname.slice(i.length)))throw Error("Invalid Telephony media endpoint");if(e==="listen-media"&&s.pathname.slice(i.length)!==t.session_token)throw Error("Listener credential mismatch");return s.protocol=s.protocol==="https:"?"wss:":"ws:",s.href}session(t,e,h="media",s=G()){let i=t;if(!i||typeof i.call_id!=="string"||typeof i.media_url!=="string"||e&&i.call_id!==e||i.session_token!==void 0&&typeof i.session_token!=="string")throw Error("Invalid Telephony session response");if(i.lease_seconds!==void 0&&(!Number.isFinite(i.lease_seconds)||i.lease_seconds<10||i.lease_seconds>3600||!i.session_token))throw Error("Invalid media lease");if(h==="listen-media"&&(!i.session_token||i.lease_seconds===void 0))throw Error("Invalid listener lease");return q(i.call_id),this.resolveMediaURL(i,h),{...i,lease_started_ms:s}}}var ih=Mt({app:"telephony",create:({app:t})=>new ft(t)});function fh({app:t},e){return new ft(t,e)}export{fh as createClient,jt as callTerminationLabel};
