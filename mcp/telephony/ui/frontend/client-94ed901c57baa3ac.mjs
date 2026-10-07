var E=()=>performance.now();function z(e){let t=e,s=t?.status,h;try{let m=JSON.parse(t?.body??"{}");if(typeof m.code==="string")h=m.code}catch{}let a=h==="media_lease_expired";return{status:s,code:h,expired:a,denied:!a&&[401,403,404,410].includes(s)}}class Y{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(e,t,s,h,a=E(),m={now:E,setTimeout:(i,u)=>setTimeout(i,u),clearTimeout:(i)=>clearTimeout(i)}){this.seconds=e;this.renew=t;this.ended=s;this.event=h;this.clock=m;this.deadline=a+e*1000-1000,this.watchExpiry(),this.schedule(Math.min(e*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(e,t){let{status:s,code:h}=z(t);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:e,status:s,code:h,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(e){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,e))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let e=this.clock.now();try{let t=await Promise.race([this.renew(),new Promise((s,h)=>{this.requestTimer=this.clock.setTimeout(()=>h(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(t?.lease_seconds!==void 0){if(!Number.isFinite(t.lease_seconds)||t.lease_seconds<10||t.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=t.lease_seconds}this.deadline=e+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(t){if(this.stopped)return;let s=z(t);if(s.denied||s.expired){this.finish(s.expired?"expired":"revoked",t);return}this.report("retrying",t),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(e,t){if(this.stopped)return;this.report(e,t),this.stop(),this.ended(e,t)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function ie(e){return e}class me{emit;now;timestamp;lastTick;suspendedAt;contextState;counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(e,t=()=>performance.now(),s=()=>new Date().toISOString()){this.emit=e;this.now=t;this.timestamp=s}tick(){let e=this.now();if(this.lastTick!==void 0){let t=Math.max(0,e-this.lastTick-1000);if(t>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,t),this.report("main_thread","scheduling_gap",t)}this.lastTick=e}context(e){if(e===this.contextState)return;this.contextState=e;let t=this.now();if(e==="suspended"||e==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=t,this.counters.audio_context_suspend_count++;this.report("audio_context",e)}else{let s;if(this.suspendedAt!==void 0)s=Math.max(0,t-this.suspendedAt),this.counters.audio_context_suspended_ms+=s,this.suspendedAt=void 0;this.report("audio_context",e,s)}}report(e,t,s){try{this.emit({timestamp:this.timestamp(),action:e,outcome:t,duration_ms:s===void 0?void 0:Math.round(s)})}catch{}}}var we=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function ke(e){let t=(e??"FR").trim().toUpperCase();return we[t]??we.FR}function Fe(e,t){let s=[];if(!(e.cadence.reduce((m,i)=>m+i,0)>0)||!(t>0))return s;let a=0;while(a<t)for(let m=0;m<e.cadence.length;m+=2){let i=e.cadence[m]??0,u=e.cadence[m+1]??0;if(a>=t)break;if(i>0)s.push({start:a,end:Math.min(a+i,t)});a+=i+u}return s}function Ae(e,t,s,h=0.12){let a=e.createGain();a.gain.value=0,a.connect(t);let m=h/Math.max(1,s.frequencies.length),i=s.frequencies.map((f)=>{let l=e.createOscillator();return l.type="sine",l.frequency.value=f,l.connect(a),l.start(),l}),u=e.currentTime+0.05,c=0,p=0.01,_=()=>{let f=e.currentTime-u+10;if(f<=c)return;for(let l of Fe(s,f)){if(l.end<=c)continue;let S=u+Math.max(l.start,c),g=u+l.end;a.gain.setValueAtTime(0,S),a.gain.linearRampToValueAtTime(m,S+p),a.gain.setValueAtTime(m,Math.max(S+p,g-p)),a.gain.linearRampToValueAtTime(0,g)}c=f};_();let r=setInterval(_,4000),n=!1;return()=>{if(n)return;n=!0,clearInterval(r);try{a.gain.cancelScheduledValues(0)}catch{}a.gain.value=0;for(let f of i){try{f.stop()}catch{}f.disconnect()}a.disconnect()}}var T=24000,$=60;async function qe(e,t){try{await e.audioWorklet.addModule(t)}catch(s){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:s})}}function Qe(e){let t=new Int16Array(e.length);for(let s=0;s<e.length;s++){let h=Math.max(-1,Math.min(1,e[s]));t[s]=h<0?h*32768:h*32767}return t.buffer}class U{history=new Float32Array(64);phase=0;process(e,t,s){if(t===s)return e;let h=new Float32Array(64+e.length);h.set(this.history),h.set(e,64);let a=t/s,m=Math.min(1,s/t)*0.9,i=[],u=this.phase;for(;u<e.length;u+=a){let c=u+32,p=0,_=0;for(let r=Math.ceil(c-32);r<=Math.floor(c+32);r++){let n=r-c,l=(Math.abs(n)<0.000000001?m:Math.sin(Math.PI*m*n)/(Math.PI*n))*(0.5+0.5*Math.cos(Math.PI*n/32));if(r>=0&&r<h.length)p+=h[r]*l,_+=l}i.push(_?p/_:0)}return this.phase=u-e.length,this.history=h.slice(-64),new Float32Array(i)}}function Be(e){let t=0;for(let s=0;s<e.length;s++)t+=e[s]*e[s];return Math.sqrt(t/Math.max(1,e.length))}var X={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function H(e){let t=e.playbackTargetMs??$,s=e.playbackMinMs??Math.min($,t),h=e.playbackMaxMs??160;for(let a of[t,s,h])if(!Number.isFinite(a)||a<40||a>160)throw RangeError("Playback buffers must be between 40 and 160 ms");if(s>t||t>h)throw RangeError("Playback buffers require min <= target <= max");return{initialTargetMs:t,minTargetMs:s,maxTargetMs:h,hardMaxMs:320}}function K(e){return{...e.inputDeviceId?{deviceId:{exact:e.inputDeviceId}}:{},echoCancellation:e.echoCancellation,noiseSuppression:e.noiseSuppression,autoGainControl:e.autoGainControl}}function xe(e){let t=e.getSettings();return{deviceLabel:e.label||"Default microphone",sampleRate:typeof t.sampleRate==="number"?t.sampleRate:null,channelCount:typeof t.channelCount==="number"?t.channelCount:null,echoCancellation:typeof t.echoCancellation==="boolean"?t.echoCancellation:null,noiseSuppression:typeof t.noiseSuppression==="boolean"?t.noiseSuppression:null,autoGainControl:typeof t.autoGainControl==="boolean"?t.autoGainControl:null}}function D(e){if(!Number.isFinite(e)||e<=0)return null;return 20*Math.log10(e)}function Ge(e,t,s){let h=new ArrayBuffer(44+t*2),a=new DataView(h),m=(u,c)=>{for(let p=0;p<c.length;p++)a.setUint8(u+p,c.charCodeAt(p))};m(0,"RIFF"),a.setUint32(4,36+t*2,!0),m(8,"WAVE"),m(12,"fmt "),a.setUint32(16,16,!0),a.setUint16(20,1,!0),a.setUint16(22,1,!0),a.setUint32(24,s,!0),a.setUint32(28,s*2,!0),a.setUint16(32,2,!0),a.setUint16(34,16,!0),m(36,"data"),a.setUint32(40,t*2,!0);let i=44;for(let u of e)for(let c=0;c<u.length;c++)a.setInt16(i,u[c],!0),i+=2;return new Blob([h],{type:"audio/wav"})}class ue{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new U;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(e,t=!0){this.onLevel=e;this.recordAudio=t}async start(e,t){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:K(t)}),this.ensureOpen();let s=this.stream.getAudioTracks()[0];if(!s)throw Error("No microphone audio track was returned.");this.settings=xe(s);try{this.ctx=new AudioContext({sampleRate:T,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await qe(this.ctx,e),this.ensureOpen();let h=this.ctx.sampleRate,a=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:t.inputGainDB,highpassFilter:t.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(m)=>{if(this.stopped)return;if(!(m.data instanceof Float32Array)){if(m.data.type==="capture.stats")this.peak=Math.max(this.peak,m.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,m.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,m.data.limiter_reduction_db??0);return}let i=m.data;if(h!==T)i=this.resampler.process(i,h,T);let u=Be(i);if(this.onLevel?.(u),this.recordAudio)this.frames.push(new Int16Array(Qe(i)));if(this.samples+=i.length,u>=0.005){for(let c=0;c<i.length;c++)this.activeSquares+=i[c]*i[c];this.activeSamples+=i.length}},a.connect(this.capture),this.settings}catch(s){throw await this.release(),s}}async stop(){this.stopped=!0;let e=this.settings,t=this.frames,s=this.samples,h=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,a=this.peak;if(await this.release(),!e||s===0)throw Error("No microphone audio was captured.");return{audio:Ge(t,s,T),durationMs:Math.round(s*1000/T),sampleRate:T,activeRmsDbfs:D(h),peakDbfs:D(a),postPeakDbfs:D(this.postPeak||a),limiterReductionDb:this.limiterReductionDB,settings:e}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let e of this.stream?.getTracks()??[])e.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class re{callbacks;clientEpoch=crypto.randomUUID();worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new me((e)=>this.recordSessionEvent(e));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={rttMs:null,queueMs:0,targetMs:$,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:T,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:X.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(e={}){this.callbacks=e}get isMuted(){return this.muted}async start(e,t,s,h=X){let a=H(h);this.diagnostics.targetMs=a.initialTargetMs,this.callbacks.onState?.("connecting"),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{this.stream=await navigator.mediaDevices.getUserMedia({audio:K(h)}),this.ensureOpen();let m=this.stream.getAudioTracks()[0];if(!m)throw Error("No microphone audio track was returned.");if(m.readyState==="ended")throw Error("Microphone disconnected before audio setup.");m.onmute=()=>this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser."),m.onunmute=()=>this.callbacks.onNotice?.("Microphone input restored."),m.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let i=xe(m);this.diagnostics={...this.diagnostics,microphoneSampleRate:i.sampleRate??0,microphoneChannelCount:i.channelCount??0,echoCancellation:i.echoCancellation,noiseSuppression:i.noiseSuppression,autoGainControl:i.autoGainControl,micInputGainDb:h.inputGainDB};try{this.ctx=new AudioContext({sampleRate:T,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await this.ctx.resume();if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),h.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(h.outputDeviceId),this.ensureOpen();await qe(this.ctx,t),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let u=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:h.inputGainDB,highpassFilter:h.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:a}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),u.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=h.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await this.openWorker(e,s),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running"&&this.microphoneTransportReady)this.callbacks.onState?.("live")},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(m){if(this.closed)this.teardown();if(!this.closed)this.fail(m instanceof Error?m.message:"browser audio setup failed");throw m}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){if(await this.ctx?.resume(),this.microphoneTransportReady)this.callbacks.onState?.("live")}setOutputVolume(e){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,e))}sendDTMF(e){if(/^[0-9*#]+$/.test(e))this.sendText(JSON.stringify({type:"dtmf",digits:e}))}startRingback(e){if(this.closed||!this.ctx||this.ringback)return;this.ringback=Ae(this.ctx,this.ctx.destination,ke(e))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(e)=>{let t=e.data;if(t?.type!=="capture.stats")return;this.micLevel=this.muted?0:t.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:D(t.active_rms??0),micPeakDbfs:D(t.pre_peak??0),micPostPeakDbfs:D(t.post_peak??0),micInputGainDb:t.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:t.limiter_reduction_db??0}},this.playback.port.onmessage=(e)=>{let t=e.data;if(t?.type!=="stats")return;this.playbackTiming={played_ms:t.played_ms,max_residence_ms:t.max_residence_ms,drop_totals_ms:t.drop_totals_ms,coaching:{played_ms:t.whisper_played_ms,dropped_ms:t.whisper_dropped_ms,max_queue_ms:t.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,t.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:t.whisper_played_ms??0,coachingDroppedMs:t.whisper_dropped_ms??0,coachingMaxQueueMs:t.whisper_max_queue_ms??0,queueMs:t.queue_ms??0,targetMs:t.target_ms??$,underruns:t.underruns??0,droppedMs:t.dropped_ms??0,maxQueueMs:t.max_queue_ms??0,playbackSequenceGaps:t.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,t.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(e,t){return new Promise((s,h)=>{let a=new Worker(t);this.worker=a;let m=new MessageChannel,i=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:m.port1},[m.port1]),this.playback?.port.postMessage({type:"transport",port:i.port1},[i.port1]);let u=!1,c=setTimeout(()=>{if(u)return;u=!0,h(Error("audio connection timed out"))},1e4),p=(_)=>{if(u)return;if(u=!0,clearTimeout(c),_)h(_);else s()};this.cancelWorkerStart=()=>p(Error("audio session closed")),a.onmessage=(_)=>{if(this.closed){p(Error("audio session closed"));return}let r=_.data;if(r?.type==="socket.open")this.opened=!0,this.mediaSocketConnected=!0,this.startRTTProbe(),p();else if(r?.type==="runtime.event")this.recordSessionEvent(r.event);else if(r?.type==="socket.message")this.handleControl(r.data);else if(r?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL().then((n)=>{if(!this.closed&&this.worker===a)a.postMessage({type:"socket.credentials",id:r.id,mediaURL:n})},(n)=>{if(this.closed||this.worker!==a)return;let f=z(n);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:f.denied?"revoked":"retrying",status:f.status,code:f.code}),a.postMessage({type:"socket.credentials",id:r.id,denied:f.denied})})}else if(r?.type==="socket.error")this.recordSessionEvent({timestamp:r.timestamp,action:"websocket",outcome:"transport_error"});else if(r?.type==="socket.close")if(this.recordSessionEvent({timestamp:r.timestamp,action:"websocket",outcome:"closed",code:String(r.code),detail:r.reason,was_clean:r.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else p(Error("audio connection closed before it was ready"));else if(r?.type==="socket.failed")this.mediaSocketConnected=!1,p(Error(r.detail||"audio connection lost")),this.fail(r.detail||"audio connection lost");else if(r?.type==="transport.drop"&&r.event)this.diagnostics.dropEvents=this.mergeDropEvents(r.event);else if(r?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=r.buffered_bytes??0,this.transportTiming=r.timing??{},typeof r.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(r.timing.rtt_ms)}},a.onerror=(_)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:_.message?.slice(0,160)}),p(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},a.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:e,contextRate:this.ctx?.sampleRate??T,muted:this.muted,capturePort:m.port2,playbackPort:i.port2},[m.port2,i.port2])})}mergeDropEvents(e,t){if(e)this.workerDropEvents=[...this.workerDropEvents,e].slice(-100);if(t)this.playbackDropEvents=t.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((s,h)=>s.timestamp.localeCompare(h.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;handleControl(e){if(this.closed)return;try{let t=JSON.parse(e);if(t.type==="dtmf.error"||t.type==="dtmf.sent")this.callbacks.onNotice?.(t.type==="dtmf.sent"?"Keypad tone sent":t.detail||"Keypad tone failed");else if(t.type==="pong"){if(this.diagnostics.captureSequenceGaps=t.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof t.nonce==="number"&&t.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-t.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(t.type==="call.ended"||t.type==="session.replaced"){this.closed=!0;try{this.callbacks.onState?.("ended",t.type)}finally{this.teardown()}}else if(t.type==="call.status"&&typeof t.call_id==="string"&&typeof t.status==="string"){if(t.hold_state&&t.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callbacks.onCallStatus?.(t)}else if(t.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:t.detail?.slice(0,160)}),this.fail(t.detail||"The call could not be connected.");else if(t.type==="coach.state")this.callbacks.onNotice?.(t.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(t.type==="audio.health"){let s=t;if(s.state!=="healthy"&&s.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:s.state,reason:s.reason,stages:s.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),s.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&s.state==="healthy"&&s.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(t.type==="media.delivery"){let s=t.state;if(s==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(s==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=s==="stalled"}else if(t.type==="peer.disconnected")this.microphoneTransportReady=!1,this.worker?.postMessage({type:"microphone.ready",value:!1}),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(t.type==="peer.connected")this.microphoneTransportReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.callbacks.onState?.("live",this.opened?void 0:"Audio reconnected")}catch{}}startRTTProbe(){this.stopRTTProbe();let e=()=>{this.sendDiagnostics()};e(),this.pingTimer=setInterval(e,5000)}sendText(e){this.worker?.postMessage({type:"send.text",data:e})}recordSessionEvent(e){this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],e].slice(-50);try{this.callbacks.onSessionEvent?.(e)}catch{}try{this.sendDiagnostics()}catch{}}sendDiagnostics(){let e=this.diagnostics;this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,session_events:e.sessionEvents,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:e.rttMs,playback_queue_ms:e.queueMs,playback_target_ms:e.targetMs,playback_max_queue_ms:e.maxQueueMs,playback_underruns:e.underruns,playback_dropped_ms:e.droppedMs,websocket_buffered_bytes:e.websocketBufferedBytes,audio_context_rate:e.audioContextRate,microphone_sample_rate:e.microphoneSampleRate,microphone_channel_count:e.microphoneChannelCount,echo_cancellation:e.echoCancellation,noise_suppression:e.noiseSuppression,auto_gain_control:e.autoGainControl,mic_active_rms_dbfs:e.micActiveRmsDbfs,mic_peak_dbfs:e.micPeakDbfs,mic_post_peak_dbfs:e.micPostPeakDbfs,mic_input_gain_db:e.micInputGainDb,mic_limiter_reduction_db:e.micLimiterReductionDb,capture_sequence_gaps:e.captureSequenceGaps,playback_sequence_gaps:e.playbackSequenceGaps,drop_events:e.dropEvents}})),this.callbacks.onDiagnostics?.({...e})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setMuted(e){let t=this.muted!==e;if(this.muted=e,this.worker?.postMessage({type:"muted",value:e}),this.capture?.port.postMessage({type:"muted",value:e}),e)this.sendText(JSON.stringify({type:"interrupt"}));if(t)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:e?"muted":"unmuted"})}stop(){let e=!this.closed;this.closed=!0;try{if(e)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(e){if(!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:e.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",e)}finally{this.teardown()}}teardown(){this.stopRingback(),this.microphoneTransportReady=!1,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let e=this.worker;if(e?.postMessage({type:"close"}),e)setTimeout(()=>e.terminate(),100);this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((t)=>t.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var yt=T*$/1000;var F=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
`;var ce=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
`;function v(e=!1){let t=(e?[F]:[F,ce]).map((s)=>URL.createObjectURL(new Blob([s],{type:"text/javascript"})));return{urls:t,dispose(){t.splice(0).forEach((s)=>URL.revokeObjectURL(s))}}}async function pe(e,t=!1){if(typeof location>"u")return v(t);let s=new URL(e.mcpURL(),location.href);if(s.origin!==location.origin)return v(t);if(e.name!=="telephony"||!e.projectId||!Number.isSafeInteger(e.installId)||e.installId<=0)throw Error("Telephony audio requires a project and installation");return{urls:await Promise.all((t?[F]:[F,ce]).map(async(m,i)=>{let u=new TextEncoder().encode(m),c=Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",u)),(n)=>n.toString(16).padStart(2,"0")).join(""),p=`/ui/frontend/${i===0?"worklet":"worker"}-${c}.js`;if(await e.get(p,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==m)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let r=new URL(`/api/apps/telephony/_install/${e.installId}${p}`,s);return r.searchParams.set("project_id",e.projectId),r.searchParams.set("install_id",String(e.installId)),r.href})),dispose(){}}}async function Ce(){return(await navigator.mediaDevices.enumerateDevices()).filter((t)=>t.kind==="audioinput").map((t,s)=>({deviceId:t.deviceId,label:t.label||`Microphone ${s+1}`}))}function Pe(e,t){let s=new ue(e,!1),h,a=!1,m,i=()=>{return a=!0,m??=(async()=>{try{await s.cancel()}finally{h?.dispose(),h=void 0}})()};return{async start(u={}){if(a)throw Error("Microphone preview is already used");a=!0;try{if(h=t?await pe(t,!0):v(!0),m)throw h.dispose(),h=void 0,Error("Microphone preview was cancelled");await s.start(h.urls[0],{...X,...u})}catch(c){throw await i(),c}},stop:i}}function Ee(e){return{async preflight(t){(await navigator.mediaDevices.getUserMedia({audio:K(t)})).getTracks().forEach((h)=>h.stop())},create(t){let s=new re(t),h,a=!1,m=()=>{a=!0;try{s.stop()}finally{h?.dispose(),h=void 0}};return{async start(i,u){try{if(h=e?await pe(e):v(),a)throw Error("Audio session was cancelled");await s.start(i,h.urls[0],h.urls[1],u)}catch(c){throw m(),c}},stop:m,recordSessionEvent:(i)=>s.recordSessionEvent(i),setMuted:(i)=>s.setMuted(i),sendDTMF:(i)=>s.sendDTMF(i),setOutputVolume:(i)=>s.setOutputVolume(i),startRingback:(i)=>s.startRingback(i),stopRingback:()=>s.stopRingback()}}}}var I=`// Passive receiver only. Both directions share one rendering clock and delay.
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
`;function De(e){if(e.byteLength<26||e.byteLength>984||e.byteLength%2)throw Error("Invalid listener audio frame");let t=new DataView(e),s=t.getUint32(4,!0);if(t.getUint32(0,!0)!==827085889||s>1)throw Error("Invalid listener audio protocol");let h=Number(t.getBigUint64(8,!0)),a=Number(t.getBigUint64(16,!0));if(!Number.isSafeInteger(h)||!Number.isSafeInteger(a))throw Error("Invalid listener audio timing");let m=new Float32Array((e.byteLength-24)/2);for(let i=0;i<m.length;i++)m[i]=t.getInt16(24+i*2,!0)/32768;return{direction:s,sequence:h,timestampMS:a,frame:m}}function Xe(e,t,s,h){if(e.length<1||e.length>480||!Number.isFinite(h)||h<0||!Number.isInteger(t)||t<1||t>4294967295)throw Error("Invalid coaching capture");let a=new ArrayBuffer(24+e.length*2),m=new DataView(a);m.setUint32(0,826496833,!0),m.setUint32(4,t,!0),m.setUint32(8,s>>>0,!0),m.setFloat64(16,h,!0);for(let i=0;i<e.length;i++){let u=Math.max(-1,Math.min(1,e[i]));m.setInt16(24+i*2,u<0?u*32768:u*32767,!0)}return a}function Te(e,t={}){return{create(s){let h,a,m,i,u=!1,c=[],p=!1,_=0,r=!1,n=!1,f,l,S,g,C,J,N,_e=!1,oe,ee,te=0,L=!1,ye=[0,0],Me=[0,0],V=[void 0,void 0],Oe=[new U,new U],Se=t.outputVolume??1,se,B=()=>{let y=++_;if(r=!1,n=!1,N?.(y,Error("Coaching cancelled")),J)clearInterval(J);if(J=void 0,C?.port1.close(),C?.port2.close(),C=void 0,l?.disconnect(),l=void 0,S?.disconnect(),S=void 0,g?.disconnect(),g=void 0,f?.getTracks().forEach((A)=>{A.onended=null,A.stop()}),f=void 0,p&&L&&i?.readyState===WebSocket.OPEN)i.send(JSON.stringify({type:"coach.stop",generation:y}));s.onTalking?.(!1)},de=()=>B(),ge=()=>{if(document.visibilityState!=="visible")B()},he=()=>{if(u)return;if(B(),u=!0,se?.(),window.removeEventListener("blur",de),document.removeEventListener("visibilitychange",ge),i)i.onmessage=i.onclose=i.onerror=null,i.close();if(a?.disconnect(),m?.disconnect(),h)h.onstatechange=null,h.close().catch(()=>{});for(let y of c)URL.revokeObjectURL(y)},W=(y)=>{if(!u)he(),s.onClose(y)},G=()=>{if(u)throw Error("Listening cancelled")};return{async start(y,A){p=A?.coaching===!0,G();try{if(h=new AudioContext({latencyHint:"interactive"}),h.state==="suspended")await h.resume();if(G(),t.outputDeviceId&&"setSinkId"in h)await h.setSinkId(t.outputDeviceId);G();let x,Z=new URL(e.mcpURL(),location.href);if(Z.origin===location.origin){let o=`/ui/frontend/listener-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(I))),(k)=>k.toString(16).padStart(2,"0")).join("")}.js`,d=await e.get(o,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}});if(G(),d!==I)throw Error("Listener audio asset integrity mismatch");let Q=new URL(`/api/apps/telephony/_install/${e.installId}${o}`,Z);Q.searchParams.set("project_id",e.projectId),Q.searchParams.set("install_id",String(e.installId)),x=Q.href}else{let P=URL.createObjectURL(new Blob([I],{type:"text/javascript"}));c.push(P),x=P}if(await h.audioWorklet.addModule(x),G(),a=new AudioWorkletNode(h,"telephony-listener",{numberOfInputs:0,outputChannelCount:[t.stereo?2:1],processorOptions:{stereo:t.stereo}}),m=h.createGain(),m.gain.value=Se,a.connect(m).connect(h.destination),a.onprocessorerror=()=>W("listener_audio_error"),a.port.onmessage=({data:P})=>{if(P?.type==="listener.diagnostics"&&!u)try{s.onDiagnostics?.({...P,sequence_gaps:[...Me],network_excess_ms:te,network_dropped_ms:[...ye]})}catch{}},h.onstatechange=()=>{if(h?.state==="suspended"||h?.state==="interrupted")W("listener_audio_paused")},await new Promise((P,o)=>{let d=!1,Q=setTimeout(()=>{k(Error("Listener connection timed out")),W("listener_network_error")},1e4),k=(b)=>{if(d)return;d=!0,clearTimeout(Q),se=void 0,b?o(b):P()};se=()=>k(Error("Listening cancelled")),i=new WebSocket(y),i.binaryType="arraybuffer",i.onmessage=({data:b})=>{if(u||!h||!a)return;try{if(typeof b==="string"){let O=JSON.parse(b);if(O.type==="listener.ready"){if(p&&O.coaching!==!0)throw Error("Coaching not authorized");L=!0,k(),s.onReady()}else if(O.type==="coach.started")N?.(O.generation);else if(O.type==="coach.rejected")N?.(_,Error(O.detail||"Coaching unavailable"));else if(O.type==="coach.stopped"&&(O.generation===void 0||O.generation===_))B();return}if(!L||!(b instanceof ArrayBuffer))return;let w=De(b),q=w.direction;if(V[q]!==void 0&&w.sequence<V[q])return;if(V[q]!==void 0&&w.sequence>V[q])Me[q]+=w.sequence-V[q];V[q]=w.sequence+1;let ae=performance.now()-w.timestampMS;if(ee=Math.min(ee??ae,ae),te=Math.max(0,ae-ee),te>200){ye[q]+=w.frame.length*1000/24000;return}oe??=h.currentTime*1000+60-w.timestampMS;let be=Oe[q].process(w.frame,24000,h.sampleRate);a.port.postMessage({direction:q,frame:be,playAtMS:w.timestampMS+oe},[be.buffer])}catch{k(Error("Invalid listener media")),W("listener_protocol_error")}},i.onerror=()=>{k(Error("Listener connection failed")),W("listener_network_error")},i.onclose=(b)=>{k(Error(b.reason||"Listener disconnected")),W(b.reason||"listener_disconnected")}}),G(),p)window.addEventListener("blur",de),document.addEventListener("visibilitychange",ge)}catch(x){throw he(),x}},async startTalking(){if(G(),!p||!L||!h||!i||i.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(r)return;r=!0;let y=++_,A=()=>!u&&r&&y===_;try{let x=await navigator.mediaDevices.getUserMedia({audio:{echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{}}});if(!A()){x.getTracks().forEach((o)=>o.stop());return}if(f=x,f.getAudioTracks().forEach((o)=>{o.onended=()=>B()}),!_e){let o=new URL(e.mcpURL(),location.href),d;if(o.origin===location.origin){let k=`/ui/frontend/worklet-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(F))),(q)=>q.toString(16).padStart(2,"0")).join("")}.js`;if(await e.get(k,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==F)throw Error("Coaching audio asset integrity mismatch");let w=new URL(`/api/apps/telephony/_install/${e.installId}${k}`,o);w.searchParams.set("project_id",e.projectId),w.searchParams.set("install_id",String(e.installId)),d=w.href}else d=URL.createObjectURL(new Blob([F],{type:"text/javascript"})),c.push(d);if(!A())return;await h.audioWorklet.addModule(d),_e=!0}if(!A())return;if(await new Promise((o,d)=>{let Q=setTimeout(()=>{N=void 0,d(Error("Coaching activation timed out"))},3000);N=(k,b)=>{if(b||k===y)clearTimeout(Q),N=void 0,b?d(b):o()},i.send(JSON.stringify({type:"coach.start",generation:y}))}),!A())return;l=new AudioWorkletNode(h,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:0,highpassFilter:!0}}),S=h.createMediaStreamSource(x),g=h.createGain(),g.gain.value=0,S.connect(l).connect(g).connect(h.destination),C=new MessageChannel;let Z=0,P=new U;C.port1.onmessage=({data:o})=>{if(!A()||!n||o?.type!=="capture"||!(o.frame instanceof Float32Array)||!h||!i)return;if(!Number.isFinite(o.timestamp_ms)||h.currentTime*1000-o.timestamp_ms>150||i.bufferedAmount>2880||i.readyState!==WebSocket.OPEN)return;let d=P.process(o.frame,o.sample_rate,24000);if(d.length>0&&d.length<=480)i.send(Xe(d,y,Z++,o.timestamp_ms))},C.port1.start(),l.port.postMessage({type:"transport",port:C.port2},[C.port2]),n=!0,s.onTalking?.(!0),J=setInterval(()=>{if(A()&&i?.readyState===WebSocket.OPEN)i.send(JSON.stringify({type:"coach.keepalive",generation:y}))},1000)}catch(x){if(A())throw B(),x}},stopTalking:B,stop:he,setOutputVolume(y){if(!Number.isFinite(y)||y<0||y>1)throw RangeError("Listener volume must be 0–1");if(Se=y,m)m.gain.value=y}}}}}class ne{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;constructor(e,t={}){this.client=e;this.options=t;this.runtime=t.runtime??Te(e.app,t)}getSnapshot=()=>this.snapshot;subscribe=(e)=>{return this.observers.add(e),()=>{this.observers.delete(e)}};update(e){this.snapshot=Object.freeze({...this.snapshot,...e});for(let t of this.observers)try{t(this.snapshot)}catch{}}async listen(e){return this.begin(e,!1)}async coach(e){return this.begin(e,!0)}async begin(e,t){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=t,this.retryUntil=0,this.retryDelay=500;let s=++this.generation;return this.update({state:"connecting",callId:e,detail:void 0,coaching:t,talking:!1}),this.connect(e,s)}async connect(e,t){let s,h=++this.attempt,a=()=>t===this.generation&&h===this.attempt&&!this.disposed;try{if(s=this.coaching?await this.client.coachSession(e):await this.client.listenSession(e),!a()){await this.client.stopListening(s).catch(()=>{});return}this.session=s;let m=this.runtime.create({onReady:()=>{if(a())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0})},onClose:(i)=>{if(a())this.disconnected(i,e,t)},onTalking:(i,u)=>{if(a())this.update({talking:i,detail:u})},onDiagnostics:(i)=>{if(a())try{this.options.onDiagnostics?.(i)}catch{}}});if(this.audio=m,this.lease=new Y(s.lease_seconds,()=>this.client.renewListening(s),(i)=>{if(a())this.disconnected(i==="revoked"?"access_revoked":"media_disconnected",e,t,i==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,s.lease_started_ms),await m.start(this.client.listenerMediaURL(s),{coaching:s.coaching===!0}),!a())m.stop()}catch(m){if(!a()||s&&this.session!==s)return;let i=m?.status,u;try{u=JSON.parse(m.body??"{}").code}catch{}let c=u==="call_ended"?"call_ended":i===401||i===403||i===404?"access_revoked":"listener_disconnected";throw this.disconnected(c,e,t,String(m)),m}}cleanup(){this.lease?.stop(),this.lease=void 0;let e=this.audio;this.audio=void 0,e?.stop();let t=this.session;return this.session=void 0,t?this.client.stopListening(t).catch(()=>{}):Promise.resolve()}disconnected(e,t,s,h=e){if(s!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let a=e==="call_ended",m=e==="access_revoked",i=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(e);if(!this.coaching&&!a&&!m&&i&&this.options.reconnect!==!1){if(this.retryUntil||=Date.now()+30000,Date.now()<this.retryUntil){this.update({state:"reconnecting",detail:h,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,s===this.generation&&!this.disposed)this.connect(t,s).catch(()=>{})},this.retryDelay),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:a?"call_ended":m?"access_revoked":"disconnected",detail:h,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(e){this.audio?.setOutputVolume(e)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let e=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1}),await e}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}function Ue(e){if(!e)return"placing";if(le(e))return"ended";if(e==="ringing")return"ringing";if(e==="answered"||e==="in-progress")return"connected";return"placing"}var R=(e)=>e instanceof Error?e.message:String(e);class fe{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(e,t={}){this.client=e;this.options=t;if(this.runtime=t.audioRuntime??Ee(e.app),this.audioOptions={...X,...t.audio},H(this.audioOptions),this.interval=t.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}getSnapshot=()=>this.snapshot;subscribe=(e)=>{return this.assertOpen(),this.listeners.add(e),()=>{this.listeners.delete(e)}};update(e){this.snapshot=Object.freeze({...this.snapshot,...e});for(let t of this.listeners)try{t(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(e){if(this.disposed||e!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(e){return!this.disposed&&e===this.generation}finish(e){if(this.current(e))this.update({busy:!1})}cancellable(e,t){let s=this.cancellation.signal;return new Promise((h,a)=>{let m=()=>{s.removeEventListener("abort",m),a(Error("Softphone operation cancelled"))};if(s.addEventListener("abort",m,{once:!0}),e.then(h,a).finally(()=>s.removeEventListener("abort",m)),!this.current(t)||s.aborted)m()})}begin(e){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||e&&this.session)throw Error("Softphone already has an active operation or call");let t=this.invalidate();return this.update({busy:!0,detail:void 0}),t}async dial(e){let t=this.begin(!0),s,h=!1;try{this.assertCurrent(t);let a={to:e.to.trim(),from:e.from?.trim(),timeout_sec:e.timeout_sec,recording:e.recording},m=JSON.stringify(a);if(!this.intent||this.intent.value!==m||e.idempotency_key&&e.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:m,request:{...a,idempotency_key:e.idempotency_key||crypto.randomUUID()}};return await this.cancellable(this.runtime.preflight(this.audioOptions),t),this.assertCurrent(t),s=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(t),h=!0,this.outbound=!0,await this.attachAudio(s,t),s.call_id}catch(a){if(s&&(this.current(t)||!h||this.disposed))await this.recoverStartup(s,t,()=>this.client.hangup(s.call_id));if(this.current(t))this.update({detail:R(a)});throw a}finally{this.finish(t)}}async answer(e,t={}){let s=this.begin(!0),h,a=!1;try{this.assertCurrent(s),h=await this.client.answer(e,t),this.assertCurrent(s),a=!0,this.outbound=!1,await this.attachAudio(h,s)}catch(m){if(h&&(this.current(s)||!a||this.disposed))await this.recoverStartup(h,s,()=>this.client.release(h));if(this.current(s))this.update({detail:R(m)});throw m}finally{this.finish(s)}}async recoverStartup(e,t,s){try{if(await s(),this.current(t))this.clearCall()}catch{if(this.current(t))this.session=e,this.update({callId:e.call_id,audioState:"error"}),this.startPolling()}}async join(e){await this.answer(e,{rejoin:!0})}async attach(e){await this.acquireSession(e,!1)}async takeover(e){await this.acquireSession(e,!0)}async acquireSession(e,t){let s=this.begin(!0);try{await this.cancellable(this.runtime.preflight(this.audioOptions),s),this.assertCurrent(s);let h=await(t?this.client.takeover(e):this.client.attach(e));this.assertCurrent(s),await this.attachAudio(h,s),await this.reconcileAttachedCall(h,s)}catch(h){if(this.current(s))this.update({detail:R(h)});throw h}finally{this.finish(s)}}async reconnect(e){if(!this.session)throw Error("No call to reconnect");let t={...this.audioOptions,...e};H(t);let s=this.begin(!1);this.audioOptions=t;let h=this.session.call_id;try{let a=await this.client.attach(h);this.assertCurrent(s),await this.attachAudio(a,s),await this.reconcileAttachedCall(a,s)}catch(a){if(this.current(s))this.update({detail:R(a)});throw a}finally{this.finish(s)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let e=this.session;if(!e){this.cancellation.abort();return}let t=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let s=(async()=>{try{if(await this.client.hangup(e.call_id),this.current(t))this.clearCall()}catch(h){if(this.current(t))this.update({audioState:"error",detail:R(h)}),this.startPolling();throw h}finally{this.finish(t)}})();this.hangingUp=s;try{await s}finally{if(this.hangingUp===s)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(e){this.assertOpen();let t=this.session;if(!t)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let s=this.generation;this.update({busy:!0,detail:void 0});let h;try{h=this.client[e](t.call_id)}catch(a){if(this.current(s)&&this.session===t)this.update({busy:!1,detail:R(a)});throw a}this.controlPending=h;try{let a=await h;if(this.current(s)&&this.session===t)this.update({holdState:a.hold_state,recordingState:a.recording_state,controlError:a.control_error,capabilities:a.capabilities});return a}catch(a){if(this.current(s)&&this.session===t)this.update({detail:R(a)});throw a}finally{if(this.controlPending===h)this.controlPending=void 0;if(this.current(s)&&this.session===t)this.update({busy:!1})}}configureAudio(e){this.assertOpen();let t={...this.audioOptions,...e};H(t),this.audioOptions=t}setMuted(e){this.assertOpen(),this.audio?.setMuted(e),this.update({muted:e})}setOutputVolume(e){if(this.assertOpen(),!Number.isFinite(e)||e<0||e>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=e,this.audio?.setOutputVolume(e)}sendDTMF(e){if(this.assertOpen(),!/^[0-9*#]+$/.test(e))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(e)}observeCall(e){if(this.disposed||e.id!==this.session?.call_id)return;if(e.direction)this.outbound=e.direction==="outbound";if(le(e.status)){this.invalidate(),this.clearCall(e.status),this.update({busy:!1,phase:"ended",termination:e.termination,answeredBy:e.answered_by??this.snapshot.answeredBy,endedAt:e.ended_at});return}this.update({carrierStatus:e.status,phase:Ue(e.status),answeredBy:e.answered_by??this.snapshot.answeredBy,holdState:e.hold_state??this.snapshot.holdState,recordingState:e.recording_state??this.snapshot.recordingState,controlError:e.control_error??this.snapshot.controlError,capabilities:e.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let e=this.options.ringback;return typeof e==="object"&&e?e.country:void 0}syncRingback(){let e=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(e&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!e&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(e,t){this.assertCurrent(t),this.stopAudio(),this.session=e,this.update({callId:e.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let s,h=()=>!this.disposed&&s!==void 0&&this.audio===s,a=(i)=>{if(h())try{i()}catch{}},m;try{if(this.assertCurrent(t),s=this.runtime.create({refreshMediaURL:()=>{if(m)return m;if(!h()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let i=this.generation,u=(async()=>{let p=await this.client.attach(e.call_id);if(this.assertCurrent(i),!h())throw Error("Audio connection no longer active");return e=p,this.session=p,this.startLease(p),this.client.mediaURL(p)})();m=u;let c=()=>{if(m===u)m=void 0};return u.then(c,c),u},onSessionEvent:(i)=>a(()=>this.options.onSessionEvent?.(i)),onState:(i,u)=>{if(!h())return;if(i==="ended"&&u==="call.ended"){this.observeCall({id:e.call_id,status:"completed"});return}if(this.update({audioState:i,detail:u}),h()&&(i==="error"||i==="ended")){if(this.stopAudio(),i==="error")this.reconcileFailedAudio(e,this.generation)}},onLevels:(i,u)=>a(()=>this.options.onLevels?.(i,u)),onAudioHealth:(i)=>a(()=>this.options.onAudioHealth?.(i)),onDiagnostics:(i)=>a(()=>this.options.onDiagnostics?.(i)),onNotice:(i)=>a(()=>this.options.onNotice?.(i)),onCallStatus:(i)=>a(()=>this.observeCall({id:i.call_id,status:i.status,direction:i.direction,answered_at:i.answered_at,ended_at:i.ended_at,answered_by:i.answered_by,termination:i.termination,hold_state:i.hold_state,recording_state:i.recording_state,control_error:i.control_error}))}),this.audio=s,this.assertCurrent(t),this.startPolling(),this.startLease(e),s.setMuted(this.snapshot.muted),await this.cancellable(s.start(this.client.mediaURL(e),this.audioOptions),t),this.assertCurrent(t),!h())throw Error(this.snapshot.detail||"Audio connection ended during setup");s.setMuted(this.snapshot.muted),this.syncRingback()}catch(i){if(h())this.stopAudio();if(this.current(t))this.update({audioState:"error",detail:R(i)});throw i}}async reconcileAttachedCall(e,t){try{let s=await this.client.getCall(e.call_id);if(this.current(t)&&this.session===e&&s)this.observeCall(s)}catch{}}async reconcileFailedAudio(e,t){try{let s=await this.client.getCall(e.call_id);if(!this.current(t)||this.session!==e||!s)return;if(s.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(s)}catch{}}startLease(e){if(this.lease?.stop(),!e.lease_seconds)return;this.lease=new Y(e.lease_seconds,()=>this.client.renew(e),(t,s)=>{if(this.disposed||this.session!==e)return;this.stopAudio(),this.update({audioState:"error",detail:t==="expired"?"Audio session expired. Reconnect audio.":`Audio authorization ended: ${R(s??t)}`})},(t)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(t);else try{this.options.onSessionEvent?.(t)}catch{}},e.lease_started_ms)}stopAudio(){this.lease?.stop(),this.lease=void 0;let e=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{e?.stopRingback?.()}catch{}}try{e?.stop()}catch{}if(e)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let e=new AbortController;this.polling=e;let t=async()=>{let s=this.session?.call_id;if(!s||e.signal.aborted)return;try{let h=await this.client.getCall(s,e.signal);if(!e.signal.aborted&&h)this.observeCall(h)}catch(h){if(!e.signal.aborted)this.update({detail:`Call status unavailable: ${R(h)}`})}finally{if(!e.signal.aborted&&this.session&&!this.disposed)this.timer=setTimeout(t,this.interval)}};this.timer=setTimeout(t,this.interval)}clearCall(e){this.stopAudio(),this.stopPolling(),this.session=void 0,this.outbound=!1,this.update({callId:void 0,carrierStatus:e,audioState:"idle",muted:!1,detail:void 0,phase:e?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function Ye(e,t="en"){if(e?.reason==="time_limit")return t.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return e?.reason?.replaceAll("_"," ")??""}class Re extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function $e(e){return e.direction==="inbound"&&e.status==="pending"&&e.answerable!==!1&&!e.routing_waiting&&(e.peer_kind==="human"||Boolean(e.ring_offers?.some((t)=>t.kind==="browser")))}var le=(e)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(e);function M(e){if(!e||/[\s/\\?#]/.test(e))throw Error("Invalid call ID");return encodeURIComponent(e)}class j{app;options;listMicrophones=Ce;createMicrophonePreview=(e)=>Pe(e,this.app);constructor(e,t={}){this.app=e;this.options=t;if(e.name!=="telephony"||!e.projectId||!e.installId)throw Error("Telephony requires an explicit project and installation")}path(e){if(!this.options.authProvider)return e;return`/user${e}${e.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(e){let t=await this.app.get(this.path("/calls"),{signal:e});if(!Array.isArray(t?.calls)||t.calls.some((s)=>!s||typeof s.id!=="string"||typeof s.status!=="string"))throw Error("Invalid Telephony calls response");return t.calls}async getCall(e,t){let s=await this.app.get(this.path(`/calls?call_id=${M(e)}`),{signal:t});if(!Array.isArray(s?.calls))throw Error("Invalid Telephony call response");return s.calls.find((h)=>h.id===e)}incomingCalls(e){return e.filter($e)}watchCalls(e,t={}){let s=t.intervalMs??2000;if(!Number.isFinite(s)||s<100)throw Error("Call watch interval must be at least 100 ms");let h=new AbortController,a,m,i=!1,u=!1,c=new Set,p=()=>{clearTimeout(a),h.abort(),m?.close(),t.signal?.removeEventListener("abort",p)},_=(n)=>{try{t.onError?.(n)}catch{p()}};if(t.signal?.addEventListener("abort",p,{once:!0}),t.signal?.aborted)p();let r=async(n)=>{if(h.signal.aborted)return;if(i){u=!0;return}clearTimeout(a),i=!0;let f=performance.now();try{let l=await this.listCalls(h.signal);if(!h.signal.aborted){e(l);for(let S of l){if(S.answerable!==!0||S.status!=="pending")continue;for(let g of S.ring_offers??[]){if(g.kind!=="browser"||!g.id||c.has(g.id))continue;c.add(g.id),this.acknowledgeOffer(S.id,g.id).catch((C)=>{if(![404,405].includes(C?.status??0))c.delete(g.id)})}}try{t.onTiming?.({trigger:n,fetchMs:performance.now()-f})}catch{}}}catch(l){if(!h.signal.aborted){let S=l?.status;try{t.onFailure?.({trigger:n,fetchMs:performance.now()-f,status:typeof S==="number"?S:void 0,error:l})}catch{}_(l)}}finally{if(i=!1,!h.signal.aborted)if(u)u=!1,r("push");else a=setTimeout(()=>void r("poll"),s)}};if(r("poll"),!h.signal.aborted&&t.push!==!1&&typeof this.app.subscribe==="function")try{m=this.app.subscribe(this.path("/calls/events"),(n)=>{if(n.type==="calls.changed")r("push");else if(n.type==="access.revoked")m?.close(),r("push")},{transport:"fetch",signal:h.signal,reconnectDelayMs:250,onError:(n)=>{let f=n?.status;if(f===404||f===405||f===501)m?.close()}})}catch{}return{close:p}}async place(e){if(!/^\+[1-9]\d{7,14}$/.test(e.to)||!e.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let t=E();return this.session(await this.app.post(this.path("/softphone/place"),e),void 0,"media",t)}async answer(e,t={}){let s=E();try{return this.session(await this.app.post(this.path(`/softphone/answer/${M(e)}`),t),e,"media",s)}catch(h){let a=h;if(a.status===409&&typeof a.body==="string"){let m;try{m=JSON.parse(a.body)}catch{}if(m?.code==="offer_expired")throw new Re}throw h}}async acknowledgeOffer(e,t){await this.app.post(this.path(`/softphone/offer/ack/${M(e)}`),{offer_id:t})}async declineOffer(e,t){await this.app.post(this.path(`/softphone/offer/decline/${M(e)}`),{offer_id:t})}async attach(e){let t=E();return this.session(await this.app.post(this.path(`/softphone/attach/${M(e)}`),{}),e,"media",t)}async takeover(e){let t=E();return this.session(await this.app.post(this.path(`/softphone/takeover/${M(e)}`),{}),e,"media",t)}createCallListener(e={}){return new ne(this,e)}async listenSession(e){let t=E();return this.session(await this.app.post(this.path(`/softphone/listen/${M(e)}`),{}),e,"listen-media",t)}async coachSession(e){let t=E(),s=this.session(await this.app.post(this.path(`/softphone/coach/${M(e)}`),{}),e,"listen-media",t);if(s.coaching!==!0)throw Error("Invalid coaching session");return s}async renewListening(e){return this.app.post(this.path(`/softphone/${e.coaching?"coach-renew":"listen-renew"}/${M(e.call_id)}`),{session_token:e.session_token})}async stopListening(e){await this.app.post(this.path(`/softphone/${e.coaching?"coach-stop":"listen-stop"}/${M(e.call_id)}`),{session_token:e.session_token})}async listenerAudit(e){return this.app.get(this.path(`/softphone/listen-audit/${M(e)}`))}async renew(e){return this.app.post(this.path(`/softphone/renew/${M(e.call_id)}`),{session_token:e.session_token})}async release(e){if(!e.session_token)throw Error("Answer session has no release token");await this.app.post(this.path(`/softphone/release/${M(e.call_id)}`),{session_token:e.session_token})}async hangup(e){await this.app.post(this.path(`/calls/${M(e)}/hangup`),{})}hold(e){return this.app.post(this.path(`/calls/${M(e)}/hold`),{})}resume(e){return this.app.post(this.path(`/calls/${M(e)}/resume`),{})}pauseRecording(e){return this.app.post(this.path(`/calls/${M(e)}/pause-recording`),{})}resumeRecording(e){return this.app.post(this.path(`/calls/${M(e)}/resume-recording`),{})}createSoftphone(e={}){return new fe(this,e)}mediaURL(e){return this.resolveMediaURL(e,"media")}listenerMediaURL(e){return this.resolveMediaURL(e,"listen-media")}resolveMediaURL(e,t){let s=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),h=new URL(e.media_url,s),a=`/api/apps/telephony/_install/${this.app.installId}/softphone/${t}/${M(e.call_id)}/`;if(h.origin!==s.origin||!["http:","https:"].includes(h.protocol)||h.username||h.password||h.search||h.hash||!h.pathname.startsWith(a)||!/^[A-Za-z0-9_-]+$/.test(h.pathname.slice(a.length)))throw Error("Invalid Telephony media endpoint");if(t==="listen-media"&&h.pathname.slice(a.length)!==e.session_token)throw Error("Listener credential mismatch");return h.protocol=h.protocol==="https:"?"wss:":"ws:",h.href}session(e,t,s="media",h=E()){let a=e;if(!a||typeof a.call_id!=="string"||typeof a.media_url!=="string"||t&&a.call_id!==t||a.session_token!==void 0&&typeof a.session_token!=="string")throw Error("Invalid Telephony session response");if(a.lease_seconds!==void 0&&(!Number.isFinite(a.lease_seconds)||a.lease_seconds<10||a.lease_seconds>3600||!a.session_token))throw Error("Invalid media lease");if(s==="listen-media"&&(!a.session_token||a.lease_seconds===void 0))throw Error("Invalid listener lease");return M(a.call_id),this.resolveMediaURL(a,s),{...a,lease_started_ms:h}}}var $t=ie({app:"telephony",create:({app:e})=>new j(e)});function Jt({app:e},t){return new j(e,t)}export{Jt as createClient,Ye as callTerminationLabel};
