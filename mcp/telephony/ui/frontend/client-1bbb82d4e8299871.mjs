var L=()=>performance.now();function te(e){let t=e,s=t?.status,h;try{let r=JSON.parse(t?.body??"{}");if(typeof r.code==="string")h=r.code}catch{}let a=h==="media_lease_expired";return{status:s,code:h,expired:a,denied:!a&&[401,403,404,410].includes(s)}}class me{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(e,t,s,h,a=L(),r={now:L,setTimeout:(i,u)=>setTimeout(i,u),clearTimeout:(i)=>clearTimeout(i)}){this.seconds=e;this.renew=t;this.ended=s;this.event=h;this.clock=r;this.deadline=a+e*1000-1000,this.watchExpiry(),this.schedule(Math.min(e*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(e,t){let{status:s,code:h}=te(t);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:e,status:s,code:h,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(e){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,e))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let e=this.clock.now();try{let t=await Promise.race([this.renew(),new Promise((s,h)=>{this.requestTimer=this.clock.setTimeout(()=>h(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(t?.lease_seconds!==void 0){if(!Number.isFinite(t.lease_seconds)||t.lease_seconds<10||t.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=t.lease_seconds}this.deadline=e+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(t){if(this.stopped)return;let s=te(t);if(s.denied||s.expired){this.finish(s.expired?"expired":"revoked",t);return}this.report("retrying",t),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(e,t){if(this.stopped)return;this.report(e,t),this.stop(),this.ended(e,t)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function be(e){return e}var $e=["rtt_ms","queue_ms","target_ms","buffered_bytes","underruns","dropped_ms","capture_sequence_gaps","playback_sequence_gaps","main_thread_max_pause_ms","worker_max_tick_gap_ms","capture_sent_ms","capture_muted_ms","capture_dropped_ms","playback_ingress_ms","playback_transport_dropped_ms","playback_source_dropped_ms","clock_uncertainty_ms","clock_sample_age_ms","stats_errors","stats_duration_ms","send_bitrate_bps","receive_bitrate_bps","receive_gap_ms","capture_age_ms","transit_ms","delivery_excess_ms","server_queue_ms","receiver_concealed_delta_ms","receiver_silent_concealed_delta_ms","receiver_concealed_window_ms","requested_playback_target_ms","requested_upload_bitrate_bps","receiver_ssrc","receiver_loss_delta","receiver_loss_window_ms","remote_receiver_ssrc","remote_receiver_loss_delta","remote_receiver_loss_window_ms","receiver_bytesReceived","receiver_packetsReceived","receiver_packetsLost","receiver_jitter","receiver_packetsDiscarded","receiver_concealedSamples","receiver_silentConcealedSamples","receiver_concealmentEvents","receiver_insertedSamplesForDeceleration","receiver_removedSamplesForAcceleration","receiver_totalSamplesReceived","receiver_audioLevel","receiver_totalAudioEnergy","receiver_totalSamplesDuration","receiver_nackCount","receiver_fecPacketsReceived","receiver_fecPacketsDiscarded","receiver_jitter_buffer_interval_ms","receiver_jitter_target_interval_ms","receiver_jitter_minimum_interval_ms","receiver_processing_interval_ms","receiver_last_packet_age_ms","sender_bytesSent","sender_packetsSent","sender_headerBytesSent","sender_retransmittedPacketsSent","sender_retransmittedBytesSent","sender_nackCount","sender_targetBitrate","sender_send_delay_interval_ms","remote_receiver_packetsLost","remote_receiver_fractionLost","remote_receiver_jitter","remote_receiver_roundTripTime","remote_receiver_totalRoundTripTime","remote_receiver_roundTripTimeMeasurements","remote_sender_packetsSent","remote_sender_bytesSent","pair_availableOutgoingBitrate","pair_availableIncomingBitrate","pair_currentRoundTripTime","pair_totalRoundTripTime","pair_bytesSent","pair_bytesReceived","pair_requestsSent","pair_requestsReceived","pair_responsesSent","pair_responsesReceived","pair_consentRequestsSent","path_revision","codec_clock_rate","codec_channels"],Xe={connection:["connected","reconnecting","closed"],context:["running","suspended","interrupted","closed"],muted:["true","false"],device_muted:["true","false"],track:["live","ended"],ice:["new","checking","connected","completed","disconnected","failed","closed"],dtls:["new","connecting","connected","closed","failed"],pair:["frozen","waiting","in-progress","failed","succeeded"],local_candidate:["host","srflx","prflx","relay"],remote_candidate:["host","srflx","prflx","relay"],protocol:["udp","tcp"],relay_protocol:["udp","tcp","tls"],codec:["audio/opus","audio/PCMU","audio/PCMA","pcm16"]};function qe(e){let t={};for(let s of $e)if(typeof e[s]==="number"&&Number.isFinite(e[s]))t[s]=Math.max(0,Math.min(e[s],1000000000000));return t}function Ae(e){let t={};for(let[s,h]of Object.entries(Xe))if(typeof e[s]==="string"&&h.includes(e[s]))t[s]=e[s];return t}class ne{transport;serial=0;lastEmit=-1/0;previousAt;previous;incident;history=[];pending=[];counters={};constructor(e){this.transport=e}observe(e,t,s=performance.now(),h=new Date().toISOString(),a){if(!Number.isFinite(s)||!Number.isFinite(Date.parse(h)))return;if(this.previousAt!==void 0&&s<this.previousAt)this.lastEmit=-1/0,this.previous=void 0,this.counters={},this.incident=void 0;let r=qe(e),i=Ae(t),u=!1;for(let f of["underruns","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealmentEvents","path_revision","stats_errors"])if(r[f]!==void 0){if(this.counters[f]!==void 0&&r[f]>this.counters[f])u=!0;this.counters[f]=r[f]}let p={id:`s${++this.serial}`,timestamp:h,transport:this.transport,reason:u?"incident":"periodic",window_ms:Math.min(60000,Math.max(0,typeof a==="number"&&Number.isFinite(a)?a:this.previousAt===void 0?0:s-this.previousAt)),metrics:r,states:i};if(this.previousAt=s,u&&!this.incident)this.incident={sample:p,context:this.previous};if(s-this.lastEmit>=5000){if(this.incident){if(this.incident.context)this.enqueue({...this.incident.context,reason:"incident_context"});this.enqueue(this.incident.sample),this.incident=void 0}else this.enqueue(p);this.lastEmit=s}this.previous=p}enqueue(e){if(this.history.push(e),this.history.length>24)this.history.shift();if(this.pending.push(e),this.pending.length>8)this.pending.shift()}recent(){return this.history.map((e)=>({...e,metrics:{...e.metrics},states:{...e.states}}))}drain(){return this.pending.splice(0,2)}}function Ye(e){let t={...e,metrics:{},states:{},part_index:0,part_count:16},s=[],h=t;for(let a of["metrics","states"])for(let[r,i]of Object.entries(e[a])){let u={...h,[a]:{...h[a],[r]:i}};if(JSON.stringify(u).length>768)s.push(h),h={...t,metrics:{},states:{},[a]:{[r]:i}};else h=u}if(s.push(h),s.length>16)return[];return s.map((a,r)=>({...a,part_index:r,part_count:s.length}))}class fe{send;sendReport;auxiliary=[];skippedAuxiliary=0;skippedSamples=0;auxiliaryTurn=!1;pending=[];timer;report;constructor(e,t){this.send=e;this.sendReport=t}enqueueReport(e){this.report=e,this.start()}enqueueAuxiliary(e){if(this.auxiliary.push(e),this.auxiliary.length>32)this.auxiliary.shift(),this.skippedAuxiliary++;this.start()}start(){if(!this.timer)this.timer=setInterval(()=>this.tick(),250)}enqueue(e){for(let t of e){let s=Ye(t);while(this.pending.length+s.length>32){let h=this.pending.at(-1);if(!h||h.id===this.pending[0]?.id)break;this.pending=this.pending.filter((a)=>a.id!==h.id),this.skippedSamples++}if(this.pending.length+s.length<=32)this.pending.push(...s);else this.skippedSamples++}if(this.pending.length)this.start()}tick(){try{if(this.report!==void 0&&this.sendReport){if(this.sendReport(this.report))this.report=void 0}else if(this.auxiliary.length&&this.sendReport&&(!this.pending.length||this.auxiliaryTurn)){if(this.sendReport(this.auxiliary[0]))this.auxiliary.shift(),this.auxiliaryTurn=!1}else if(this.pending.length&&this.send(this.pending[0]))this.pending.shift(),this.auxiliaryTurn=!0}catch{}if(!this.pending.length&&!this.auxiliary.length&&this.report===void 0)clearInterval(this.timer),this.timer=void 0}stop(){clearInterval(this.timer),this.timer=void 0,this.pending=[],this.auxiliary=[],this.report=void 0}}class pe{emit;now;timestamp;lastTick;suspendedAt;contextState;environmentCleanup=[];counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(e,t=()=>performance.now(),s=()=>new Date().toISOString()){this.emit=e;this.now=t;this.timestamp=s}observeEnvironment(){this.stopEnvironment();let e=(t,s,h)=>{if(!t)return;try{t.addEventListener(s,h),this.environmentCleanup.push(()=>t.removeEventListener(s,h))}catch{}};try{if(typeof document<"u"){e(document,"visibilitychange",()=>this.report("tab_visibility",document.visibilityState));for(let t of["freeze","resume"])e(document,t,()=>this.report("page_lifecycle",t));this.report("tab_visibility",document.visibilityState)}if(typeof window<"u")for(let t of["pagehide","pageshow"])e(window,t,()=>this.report("page_lifecycle",t));if(typeof navigator<"u")e(navigator.mediaDevices,"devicechange",()=>this.report("microphone","devices_changed"))}catch{}}stopEnvironment(){for(let e of this.environmentCleanup.splice(0))try{e()}catch{}}tick(){let e=this.now();if(this.lastTick!==void 0){let t=Math.max(0,e-this.lastTick-1000);if(t>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,t),this.report("main_thread","scheduling_gap",t)}this.lastTick=e}context(e){if(e===this.contextState)return;this.contextState=e;let t=this.now();if(e==="suspended"||e==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=t,this.counters.audio_context_suspend_count++;this.report("audio_context",e)}else{let s;if(this.suspendedAt!==void 0)s=Math.max(0,t-this.suspendedAt),this.counters.audio_context_suspended_ms+=s,this.suspendedAt=void 0;this.report("audio_context",e,s)}}report(e,t,s){try{this.emit({timestamp:this.timestamp(),action:e,outcome:t,duration_ms:s===void 0?void 0:Math.round(s)})}catch{}}}var Ne=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function Me(e){let t=(e??"FR").trim().toUpperCase();return Ne[t]??Ne.FR}function ze(e,t){let s=[];if(!(e.cadence.reduce((r,i)=>r+i,0)>0)||!(t>0))return s;let a=0;while(a<t)for(let r=0;r<e.cadence.length;r+=2){let i=e.cadence[r]??0,u=e.cadence[r+1]??0;if(a>=t)break;if(i>0)s.push({start:a,end:Math.min(a+i,t)});a+=i+u}return s}function ye(e,t,s,h=0.12){let a=e.createGain();a.gain.value=0,a.connect(t);let r=h/Math.max(1,s.frequencies.length),i=s.frequencies.map((c)=>{let S=e.createOscillator();return S.type="sine",S.frequency.value=c,S.connect(a),S.start(),S}),u=e.currentTime+0.05,p=0,f=0.01,d=()=>{let c=e.currentTime-u+10;if(c<=p)return;for(let S of ze(s,c)){if(S.end<=p)continue;let o=u+Math.max(S.start,p),_=u+S.end;a.gain.setValueAtTime(0,o),a.gain.linearRampToValueAtTime(r,o+f),a.gain.setValueAtTime(r,Math.max(o+f,_-f)),a.gain.linearRampToValueAtTime(0,_)}p=c};d();let m=setInterval(d,4000),y=!1;return()=>{if(y)return;y=!0,clearInterval(m);try{a.gain.cancelScheduledValues(0)}catch{}a.gain.value=0;for(let c of i){try{c.stop()}catch{}c.disconnect()}a.disconnect()}}var D=24000,le=60;async function Ge(e,t){try{await e.audioWorklet.addModule(t)}catch(s){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:s})}}function Je(e){let t=new Int16Array(e.length);for(let s=0;s<e.length;s++){let h=Math.max(-1,Math.min(1,e[s]));t[s]=h<0?h*32768:h*32767}return t.buffer}class J{history=new Float32Array(64);phase=0;process(e,t,s){if(t===s)return e;let h=new Float32Array(64+e.length);h.set(this.history),h.set(e,64);let a=t/s,r=Math.min(1,s/t)*0.9,i=[],u=this.phase;for(;u<e.length;u+=a){let p=u+32,f=0,d=0;for(let m=Math.ceil(p-32);m<=Math.floor(p+32);m++){let y=m-p,S=(Math.abs(y)<0.000000001?r:Math.sin(Math.PI*r*y)/(Math.PI*y))*(0.5+0.5*Math.cos(Math.PI*y/32));if(m>=0&&m<h.length)f+=h[m]*S,d+=S}i.push(d?f/d:0)}return this.phase=u-e.length,this.history=h.slice(-64),new Float32Array(i)}}function Ze(e){let t=0;for(let s=0;s<e.length;s++)t+=e[s]*e[s];return Math.sqrt(t/Math.max(1,e.length))}function Be(e,t){let s=new Map(e.map((h)=>[h.id,h]));for(let h of t){let a=s.get(h.id);if(!a||(!a.complete||h.complete)&&(!a.ended_at||!!h.ended_at)&&h.duration_ms>=a.duration_ms)s.set(h.id,{...h})}return[...s.values()].sort((h,a)=>h.started_at.localeCompare(a.started_at)).slice(-100)}function Ke(e,t){return e.map((s)=>s.ended_at?s:{...s,ended_at:s.observed_until,end_reason:t,complete:!1})}var he={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function j(e){if(e.mediaTransport!==void 0&&!["websocket","webrtc","auto"].includes(e.mediaTransport))throw RangeError("Unsupported softphone media transport");let t=e.playbackTargetMs??le,s=e.playbackMinMs??Math.min(le,t),h=e.playbackMaxMs??280;for(let a of[t,s,h])if(!Number.isFinite(a)||a<40||a>280)throw RangeError("Playback buffers must be between 40 and 280 ms");if(s>t||t>h)throw RangeError("Playback buffers require min <= target <= max");if(e.playbackAdaptive!==void 0&&typeof e.playbackAdaptive!=="boolean")throw RangeError("playbackAdaptive must be boolean");return{initialTargetMs:t,minTargetMs:s,maxTargetMs:h,hardMaxMs:320,adaptiveReserve:e.playbackAdaptive!==!1}}function ae(e){return{...e.inputDeviceId?{deviceId:{exact:e.inputDeviceId}}:{},echoCancellation:e.echoCancellation,noiseSuppression:e.noiseSuppression,autoGainControl:e.autoGainControl}}function Qe(e){let t=e.getSettings();return{deviceLabel:e.label||"Default microphone",sampleRate:typeof t.sampleRate==="number"?t.sampleRate:null,channelCount:typeof t.channelCount==="number"?t.channelCount:null,echoCancellation:typeof t.echoCancellation==="boolean"?t.echoCancellation:null,noiseSuppression:typeof t.noiseSuppression==="boolean"?t.noiseSuppression:null,autoGainControl:typeof t.autoGainControl==="boolean"?t.autoGainControl:null}}function se(e){if(!Number.isFinite(e)||e<=0)return null;return 20*Math.log10(e)}function je(e,t,s){let h=new ArrayBuffer(44+t*2),a=new DataView(h),r=(u,p)=>{for(let f=0;f<p.length;f++)a.setUint8(u+f,p.charCodeAt(f))};r(0,"RIFF"),a.setUint32(4,36+t*2,!0),r(8,"WAVE"),r(12,"fmt "),a.setUint32(16,16,!0),a.setUint16(20,1,!0),a.setUint16(22,1,!0),a.setUint32(24,s,!0),a.setUint32(28,s*2,!0),a.setUint16(32,2,!0),a.setUint16(34,16,!0),r(36,"data"),a.setUint32(40,t*2,!0);let i=44;for(let u of e)for(let p=0;p<u.length;p++)a.setInt16(i,u[p],!0),i+=2;return new Blob([h],{type:"audio/wav"})}class we{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new J;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(e,t=!0){this.onLevel=e;this.recordAudio=t}async start(e,t){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:ae(t)}),this.ensureOpen();let s=this.stream.getAudioTracks()[0];if(!s)throw Error("No microphone audio track was returned.");this.settings=Qe(s);try{this.ctx=new AudioContext({sampleRate:D,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await Ge(this.ctx,e),this.ensureOpen();let h=this.ctx.sampleRate,a=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:t.inputGainDB,highpassFilter:t.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(r)=>{if(this.stopped)return;if(!(r.data instanceof Float32Array)){if(r.data.type==="capture.stats")this.peak=Math.max(this.peak,r.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,r.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,r.data.limiter_reduction_db??0);return}let i=r.data;if(h!==D)i=this.resampler.process(i,h,D);let u=Ze(i);if(this.onLevel?.(u),this.recordAudio)this.frames.push(new Int16Array(Je(i)));if(this.samples+=i.length,u>=0.005){for(let p=0;p<i.length;p++)this.activeSquares+=i[p]*i[p];this.activeSamples+=i.length}},a.connect(this.capture),this.settings}catch(s){throw await this.release(),s}}async stop(){this.stopped=!0;let e=this.settings,t=this.frames,s=this.samples,h=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,a=this.peak;if(await this.release(),!e||s===0)throw Error("No microphone audio was captured.");return{audio:je(t,s,D),durationMs:Math.round(s*1000/D),sampleRate:D,activeRmsDbfs:se(h),peakDbfs:se(a),postPeakDbfs:se(this.postPeak||a),limiterReductionDb:this.limiterReductionDB,settings:e}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let e of this.stream?.getTracks()??[])e.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class Pe{callbacks;clientEpoch=crypto.randomUUID();transportTelemetry=new ne("websocket");transportSender=new fe((e)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[e]}})}),!0});worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new pe((e)=>this.recordSessionEvent(e));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;playbackHeld=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={mediaTransport:"websocket",codec:"pcm16",rttMs:null,queueMs:0,targetMs:le,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:D,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:he.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(e={}){this.callbacks=e}get isMuted(){return this.muted}async start(e,t,s,h=he){let a=j(h);this.diagnostics.targetMs=a.initialTargetMs,this.callbacks.onState?.("connecting"),this.runtimeTelemetry.observeEnvironment(),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{this.stream=await navigator.mediaDevices.getUserMedia({audio:ae(h)}),this.ensureOpen();let r=this.stream.getAudioTracks()[0];if(!r)throw Error("No microphone audio track was returned.");if(r.readyState==="ended")throw Error("Microphone disconnected before audio setup.");r.onmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser.")},r.onunmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),this.callbacks.onNotice?.("Microphone input restored.")},r.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let i=Qe(r);this.diagnostics={...this.diagnostics,microphoneSampleRate:i.sampleRate??0,microphoneChannelCount:i.channelCount??0,echoCancellation:i.echoCancellation,noiseSuppression:i.noiseSuppression,autoGainControl:i.autoGainControl,micInputGainDb:h.inputGainDB};try{this.ctx=new AudioContext({sampleRate:D,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await this.ctx.resume();if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),h.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(h.outputDeviceId),this.ensureOpen();await Ge(this.ctx,t),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let u=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:h.inputGainDB,highpassFilter:h.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:{...a,telemetryEpoch:this.clientEpoch,telemetryEnabled:!1}}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),u.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=h.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await this.openWorker(e,s),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.setPlaybackObservation(this.ctx?.state==="running"&&this.microphoneTransportReady&&!this.playbackHeld,"audio_context_paused"),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running"&&this.microphoneTransportReady)this.callbacks.onState?.("live")},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(r){if(this.closed)this.teardown();if(!this.closed)this.fail(r instanceof Error?r.message:"browser audio setup failed");throw r}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){if(await this.ctx?.resume(),this.microphoneTransportReady)this.callbacks.onState?.("live")}setOutputVolume(e){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,e))}sendDTMF(e){if(/^[0-9*#]+$/.test(e))this.sendText(JSON.stringify({type:"dtmf",digits:e}))}startRingback(e){if(this.closed||!this.ctx||this.ringback)return;this.ringback=ye(this.ctx,this.ctx.destination,Me(e))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(e)=>{let t=e.data;if(t?.type!=="capture.stats")return;this.micLevel=this.muted?0:t.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:se(t.active_rms??0),micPeakDbfs:se(t.pre_peak??0),micPostPeakDbfs:se(t.post_peak??0),micInputGainDb:t.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:t.limiter_reduction_db??0}},this.playback.port.onmessage=(e)=>{let t=e.data;if(t?.type==="playback.underrun"){this.diagnostics.underrunEvents=Be(this.diagnostics.underrunEvents??[],[t.event]),this.diagnostics.underrunDurationMs=t.underrun_ms,this.diagnostics.underruns=t.underruns;return}if(t?.type!=="stats")return;this.diagnostics.playbackEvents=t.playback_events,this.playbackTiming={reserve_expanded_ms:t.reserve_expanded_ms,reserve_compressed_ms:t.reserve_compressed_ms,reserve_adjustments:t.reserve_adjustments,reserve_match_rejections:t.reserve_match_rejections,played_ms:t.played_ms,max_residence_ms:t.max_residence_ms,drop_totals_ms:t.drop_totals_ms,coaching:{played_ms:t.whisper_played_ms,dropped_ms:t.whisper_dropped_ms,max_queue_ms:t.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,t.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:t.whisper_played_ms??0,coachingDroppedMs:t.whisper_dropped_ms??0,coachingMaxQueueMs:t.whisper_max_queue_ms??0,queueMs:t.queue_ms??0,targetMs:t.target_ms??le,underruns:t.underruns??0,droppedMs:t.dropped_ms??0,underrunDurationMs:t.underrun_ms??0,underrunEvents:Be(this.diagnostics.underrunEvents??[],t.underrun_events??[]),maxQueueMs:t.max_queue_ms??0,playbackSequenceGaps:t.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,t.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(e,t){return new Promise((s,h)=>{let a=new Worker(t);this.worker=a;let r=new MessageChannel,i=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:r.port1},[r.port1]),this.playback?.port.postMessage({type:"transport",port:i.port1},[i.port1]);let u=!1,p=setTimeout(()=>{if(u)return;u=!0,h(Error("audio connection timed out"))},1e4),f=(d)=>{if(u)return;if(u=!0,clearTimeout(p),d)h(d);else s()};this.cancelWorkerStart=()=>f(Error("audio session closed")),a.onmessage=(d)=>{if(this.closed){f(Error("audio session closed"));return}let m=d.data;if(m?.type==="socket.open")this.reportedPlaybackIDs.clear(),this.reportedCaptureQueueIDs.clear(),this.opened=!0,this.mediaSocketConnected=!0,this.startRTTProbe(),f();else if(m?.type==="runtime.event")this.recordSessionEvent(m.event);else if(m?.type==="socket.message")this.handleControl(m.data);else if(m?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL().then((y)=>{if(!this.closed&&this.worker===a)a.postMessage({type:"socket.credentials",id:m.id,mediaURL:y})},(y)=>{if(this.closed||this.worker!==a)return;let c=te(y);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:c.denied?"revoked":"retrying",status:c.status,code:c.code}),a.postMessage({type:"socket.credentials",id:m.id,denied:c.denied})})}else if(m?.type==="socket.error")this.recordSessionEvent({timestamp:m.timestamp,action:"websocket",outcome:"transport_error"});else if(m?.type==="socket.close")if(this.recordSessionEvent({timestamp:m.timestamp,action:"websocket",outcome:"closed",code:String(m.code),detail:m.reason,was_clean:m.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"transport_disconnected"),this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else f(Error("audio connection closed before it was ready"));else if(m?.type==="socket.failed")this.mediaSocketConnected=!1,f(Error(m.detail||"audio connection lost")),this.fail(m.detail||"audio connection lost");else if(m?.type==="transport.drop"&&m.event)this.diagnostics.dropEvents=this.mergeDropEvents(m.event);else if(m?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=m.buffered_bytes??0,this.diagnostics.captureQueueEvents=m.capture_queue_events,this.transportTiming=m.timing??{},m.observation)this.transportTelemetry.observe({...this.transportTiming,...m.observation,rtt_ms:m.timing?.rtt_ms,queue_ms:this.diagnostics.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:m.buffered_bytes,underruns:this.diagnostics.underruns,dropped_ms:this.diagnostics.droppedMs,capture_sequence_gaps:this.diagnostics.captureSequenceGaps,playback_sequence_gaps:this.diagnostics.playbackSequenceGaps,main_thread_max_pause_ms:this.runtimeTelemetry.counters.main_thread_max_pause_ms},{connection:this.mediaSocketConnected?"connected":"reconnecting",context:this.ctx?.state,muted:String(this.muted),device_muted:String(this.stream?.getAudioTracks()[0]?.muted),track:this.stream?.getAudioTracks()[0]?.readyState,codec:"pcm16"},performance.now(),m.observation?.timestamp,m.observation?.window_ms);if(this.diagnostics.transportSamples=this.transportTelemetry.recent(),typeof m.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(m.timing.rtt_ms)}},a.onerror=(d)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:d.message?.slice(0,160)}),f(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},a.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:e,contextRate:this.ctx?.sampleRate??D,muted:this.muted,capturePort:r.port2,playbackPort:i.port2},[r.port2,i.port2])})}mergeDropEvents(e,t){if(e)this.workerDropEvents=[...this.workerDropEvents,e].slice(-100);if(t)this.playbackDropEvents=t.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((s,h)=>s.timestamp.localeCompare(h.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;shutdownIntent="session_cleanup";reportedPlaybackIDs=new Set;reportedCaptureQueueIDs=new Set;handleControl(e){if(this.closed)return;try{let t=JSON.parse(e);if(t.type==="dtmf.error"||t.type==="dtmf.sent")this.callbacks.onNotice?.(t.type==="dtmf.sent"?"Keypad tone sent":t.detail||"Keypad tone failed");else if(t.type==="pong"){if(this.diagnostics.captureSequenceGaps=t.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof t.nonce==="number"&&t.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-t.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(t.type==="call.ended"||t.type==="session.replaced"){this.setPlaybackObservation(!1,t.type==="call.ended"?"call_ended":"observation_ended"),this.shutdownIntent=t.type==="call.ended"?"call_ended":"session_replaced",this.closed=!0;try{this.callbacks.onState?.("ended",t.type)}finally{this.teardown()}}else if(t.type==="call.status"&&typeof t.call_id==="string"&&typeof t.status==="string"){if(t.hold_state)this.playbackHeld=t.hold_state!=="active",this.setPlaybackObservation(!this.playbackHeld&&this.microphoneTransportReady&&this.ctx?.state==="running",this.playbackHeld?"hold":"observation_resumed");if(t.hold_state&&t.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callbacks.onCallStatus?.(t)}else if(t.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:t.detail?.slice(0,160)}),this.fail(t.detail||"The call could not be connected.");else if(t.type==="coach.state")this.callbacks.onNotice?.(t.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(t.type==="audio.health"){let s=t;if(s.state!=="healthy"&&s.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:s.state,reason:s.reason,stages:s.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),s.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&s.state==="healthy"&&s.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(t.type==="media.delivery"){let s=t.state;if(s==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(s==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=s==="stalled"}else if(t.type==="peer.disconnected")this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"carrier_disconnected"),this.worker?.postMessage({type:"microphone.ready",value:!1}),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(t.type==="peer.connected")this.microphoneTransportReady=!0,this.setPlaybackObservation(this.ctx?.state==="running"&&!this.playbackHeld,"carrier_connected"),this.worker?.postMessage({type:"microphone.ready",value:!0}),this.callbacks.onState?.("live",this.opened?void 0:"Audio reconnected")}catch{}}startRTTProbe(){this.stopRTTProbe();let e=()=>{this.sendDiagnostics()};e(),this.pingTimer=setInterval(e,5000)}sendText(e){this.worker?.postMessage({type:"send.text",data:e})}recordSessionEvent(e){this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],e].slice(-50);try{this.callbacks.onSessionEvent?.(e)}catch{}try{this.sendDiagnostics()}catch{}}sendDiagnostics(){let e=this.diagnostics,t=(a,r)=>{let i=(a??[]).slice(-64),u=i.filter((p)=>!r.has(p.id)).slice(0,8);if(this.mediaSocketConnected){let p=new Set(i.map((f)=>f.id));for(let f of r)if(!p.has(f))r.delete(f);for(let f of u)r.add(f.id)}return u},s=t(e.playbackEvents,this.reportedPlaybackIDs),h=t(e.captureQueueEvents,this.reportedCaptureQueueIDs);if(this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{media_transport:"websocket",codec:"pcm16",client_epoch:this.clientEpoch,session_events:e.sessionEvents,playback_events:s,capture_queue_events:h,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:e.rttMs,playback_queue_ms:e.queueMs,playback_target_ms:e.targetMs,playback_max_queue_ms:e.maxQueueMs,playback_underruns:e.underruns,playback_underrun_ms:e.underrunDurationMs,playback_underrun_events:e.underrunEvents,playback_dropped_ms:e.droppedMs,websocket_buffered_bytes:e.websocketBufferedBytes,audio_context_rate:e.audioContextRate,microphone_sample_rate:e.microphoneSampleRate,microphone_channel_count:e.microphoneChannelCount,echo_cancellation:e.echoCancellation,noise_suppression:e.noiseSuppression,auto_gain_control:e.autoGainControl,mic_active_rms_dbfs:e.micActiveRmsDbfs,mic_peak_dbfs:e.micPeakDbfs,mic_post_peak_dbfs:e.micPostPeakDbfs,mic_input_gain_db:e.micInputGainDb,mic_limiter_reduction_db:e.micLimiterReductionDb,capture_sequence_gaps:e.captureSequenceGaps,playback_sequence_gaps:e.playbackSequenceGaps,drop_events:e.dropEvents}})),this.mediaSocketConnected)this.transportSender.enqueue(this.transportTelemetry.drain());this.callbacks.onDiagnostics?.({...e})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setPlaybackObservation(e,t){try{if(this.playback?.port.postMessage({type:"playback.telemetry.boundary",active:e,reason:t,audio_time_ms:(this.ctx?.currentTime??0)*1000}),!e)this.diagnostics.underrunEvents=Ke(this.diagnostics.underrunEvents??[],t)}catch{}}setMuted(e){let t=this.muted!==e;if(this.muted=e,this.worker?.postMessage({type:"muted",value:e}),this.capture?.port.postMessage({type:"muted",value:e}),e)this.sendText(JSON.stringify({type:"interrupt"}));if(t)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:e?"muted":"unmuted"})}stop(){this.shutdownIntent="user_stop";let e=!this.closed;this.closed=!0;try{if(e)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(e){if(this.shutdownIntent="audio_error",!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:e.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",e)}finally{this.teardown()}}teardown(){this.runtimeTelemetry.stopEnvironment(),this.transportSender.stop(),this.setPlaybackObservation(!1,"observation_ended"),this.stopRingback(),this.microphoneTransportReady=!1,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let e=this.worker;if(e?.postMessage({type:"close",reason:this.shutdownIntent}),e){let t=setTimeout(()=>e.terminate(),600);e.onmessage=(s)=>{if(s.data?.type==="socket.shutdown.complete")clearTimeout(t),e.terminate()}}this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((t)=>t.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var Ut=D*le/1000;var Z=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
    const safeMS = (value, fallback, max) => Number.isFinite(value) ? Math.max(40,Math.min(max,value)) : fallback;
    this.maxTargetMs = safeMS(config.maxTargetMs,280,280);
    this.initialTargetMs = safeMS(config.initialTargetMs,Math.min(60,this.maxTargetMs),this.maxTargetMs);
    this.minTargetMs = safeMS(config.minTargetMs,Math.min(60,this.initialTargetMs),this.initialTargetMs);
    this.hardMaxMs = 320;
    this.targetMs = this.initialTargetMs;
    this.adaptiveReserve = config.adaptiveReserve !== false;
    this.reserveHistory = new Float32Array(this.msToSamples(40));
    this.reservePreview = new Float32Array(this.msToSamples(22));
    this.reservePending = new Float32Array(this.msToSamples(20));
    this.reserveFade = new Float32Array(this.msToSamples(2));
    this.reserveExpandedSamples = this.reserveCompressedSamples = 0;
    this.reserveAdjustments = this.reserveMatchRejections = 0;
    this.reserveLastSearchMS = -Infinity;
    this.resetReserveAdjustment();
    this.queue = [];
    this.queued = 0;
    this.offset = 0;
    this.playing = false;
    this.startupWaitSamples = 0;
    this.hasPlayed = false;
    this.shortTailStartup = false;
    this.shortTailRemaining = null;
    this.playbackEvents = [];
    this.playbackEventSerial = 0;
    this.lastAdjustmentEventMS = -Infinity;
    this.underruns = 0;
    this.underrunSamples = 0;
    this.underrunEvents = [];
    this.openUnderrun = null;
    this.underrunSerial = 0;
    this.underrunPending = false;
    this.underrunEpoch = config.telemetryEpoch || "playback";
    this.underrunTelemetryEnabled = config.telemetryEnabled !== false;
    this.lastPlayedSequence = null;
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
    if (message?.type === "playback.telemetry.boundary") {
      // Observation only: do not flush, retime or change playback adaptation.
      if (message.active !== true) this.endUnderrun(message.reason || "observation_ended", Number.isFinite(message.audio_time_ms) ? message.audio_time_ms : currentTime * 1000);
      this.underrunTelemetryEnabled = message.active === true;
      return;
    }
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
      this.endUnderrun("flush", currentTime * 1000);
      this.whisperDropped+=this.whisperQueued;this.whisperQueue=[];this.whisperQueued=0;this.whisperOffset=0;
      this.dropOldest(this.queued, "playback_flush");
      this.queue = []; this.queued = 0; this.offset = 0; this.playing = false;
      this.underrunPending = false;
      this.resetReserveAdjustment();
      this.startupWaitSamples = this.stableSamples = 0;
      this.shortTailStartup = false;this.shortTailRemaining=null;
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
        const prior = this.targetMs;
        this.targetMs = Math.min(this.maxTargetMs, Math.max(this.targetMs, Math.ceil((excess + this.minTargetMs) / 10) * 10));
        if (prior !== this.targetMs) this.playbackEvent("target_changed", "arrival_jitter");
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

  playbackEvent(kind, reason, extra = {}) {
    this.playbackEvents.push({id:\`\${this.underrunEpoch}:playback:\${++this.playbackEventSerial}\`,
      timestamp:new Date().toISOString(), audio_time_ms:currentTime*1000, kind, reason,
      phase:this.playing ? "playing" : this.hasPlayed ? "rebuffer" : "startup", queue_ms:this.queued*1000/sampleRate,
      target_ms:this.targetMs, wait_ms:this.startupWaitSamples*1000/sampleRate, ...extra});
    if (this.playbackEvents.length > 64) this.playbackEvents.shift();
  }

  resetReserveAdjustment() {
    this.reserveHistoryCount = this.reserveHistoryPosition = 0;
    this.reserveCredit = 0;
    this.reservePendingLength = this.reservePendingOffset = 0;
    this.reserveFadeLength = this.reserveFadeOffset = 0;
  }

  rememberCallerSamples(out, count) {
    for (let i = 0; i < count; i++) {
      this.reserveHistory[this.reserveHistoryPosition] = out[i];
      this.reserveHistoryPosition = (this.reserveHistoryPosition + 1) % this.reserveHistory.length;
    }
    this.reserveHistoryCount = Math.min(this.reserveHistory.length, this.reserveHistoryCount + count);
  }

  // Pitch-preserving expansion/compression only at closely matching waveform
  // boundaries. The bounded search never runs on every frame, changes the
  // microphone/coaching path, or synthesizes missing speech during an outage.
  adjustLiveReserve(outputSamples) {
    if (!this.adaptiveReserve || !this.playing || this.reservePendingLength || this.needsCrossfade) return 0;
    const reserve = this.queued - outputSamples;
    const desired = Math.min(this.msToSamples(this.targetMs)+outputSamples, this.msToSamples(this.hardMaxMs-Math.max(20,this.lastFrameMS))-outputSamples*2);
    const expand = this.targetMs > this.initialTargetMs && reserve < desired - this.msToSamples(2);
    const compress = this.stableSamples >= sampleRate*10 && reserve > desired + this.msToSamples(30);
    // Never stretch into a delivery outage. Reserve is grown only while fresh
    // packets are flowing; repeated jitter keeps the learned cushion intact.
    if (this.lastArrivalMS === null || currentTime*1000-this.lastArrivalMS > this.lastFrameMS+outputSamples*1000/sampleRate) { this.reserveCredit=0; return 0; }
    if (!expand && !compress) return 0;
    // At most 2% of rendered time, with no stored-up adjustment after an outage.
    this.reserveCredit = Math.min(this.msToSamples(20), this.reserveCredit + outputSamples * .02);
    const compare = this.msToSamples(2);
    const minimum = this.msToSamples(3);
    let maximum = Math.min(this.msToSamples(20), Math.floor(this.reserveCredit), Math.abs(desired-reserve));
    maximum = Math.min(maximum, expand ? this.reserveHistoryCount : this.queued - outputSamples - compare);
    if (expand && this.queue.length) {
      const sourceAge=Math.max(0,currentTime*1000-this.queue[0].arrived_ms);
      maximum=Math.min(maximum,Math.floor(this.msToSamples(Math.max(0,this.hardMaxMs-sourceAge))/2)-outputSamples);
    }
    if (maximum < minimum || this.queued < outputSamples + compare || this.reserveHistoryCount < compare) return 0;
    // Do not search more than once per 100ms when the audio does not match.
    if (currentTime * 1000 - this.reserveLastSearchMS < 100) return 0;
    this.reserveLastSearchMS = currentTime * 1000;
    const needed = compress ? maximum + compare : compare;
    let copied = 0;
    for (let q = 0; q < this.queue.length && copied < needed; q++) {
      const item = this.queue[q];
      const offset = q === 0 ? this.offset : 0;
      const take = Math.min(needed-copied, item.frame.length-offset);
      this.reservePreview.set(item.frame.subarray(offset, offset+take), copied);
      copied += take;
    }
    if (copied < needed) return 0;
    const size = this.reserveHistory.length;
    const score = lag => {
      let error = 0, energy = 0;
      for (let i = 0; i < compare; i++) {
        const a = this.reservePreview[i];
        const b = expand ? this.reserveHistory[(this.reserveHistoryPosition-lag+i+size)%size] : this.reservePreview[lag+i];
        if (!Number.isFinite(a) || !Number.isFinite(b) || Math.abs(a)>1 || Math.abs(b)>1) return Infinity;
        const delta=a-b; error+=delta*delta; energy+=a*a+b*b;
      }
      return error / Math.max(1e-9, energy);
    };
    const step = Math.max(1, Math.floor(sampleRate/12000));
    let best = minimum, bestScore = Infinity;
    for (let lag=minimum; lag<=maximum; lag+=step) {
      const value=score(lag); if(value<bestScore) {bestScore=value;best=lag;}
    }
    const coarse=best;
    for(let lag=Math.max(minimum,coarse-step+1);lag<=Math.min(maximum,coarse+step-1);lag++) {
      const value=score(lag);if(value<bestScore){bestScore=value;best=lag;}
    }
    if (bestScore > .005) { this.reserveMatchRejections++; return 0; }
    this.reserveCredit -= best;
    this.reserveAdjustments++;
    if (currentTime*1000-this.lastAdjustmentEventMS >= 250) {
      this.lastAdjustmentEventMS = currentTime*1000;
      this.playbackEvent("reserve_adjustment", expand ? "expansion" : "compression", {phase:"playing",duration_ms:best*1000/sampleRate,match_score:bestScore});
    }
    if (expand) {
      for(let i=0;i<best;i++) this.reservePending[i]=this.reserveHistory[(this.reserveHistoryPosition-best+i+size)%size];
      for(let i=0;i<compare;i++) {
        const blend=(i+1)/compare;
        this.reservePending[i]=this.reservePreview[i]*(1-blend)+this.reservePending[i]*blend;
        const end=best-compare+i;
        this.reservePending[end]=this.reservePending[end]*(1-blend)+this.reserveHistory[(this.reserveHistoryPosition-compare+i+size)%size]*blend;
      }
      this.reservePendingLength=best;this.reservePendingOffset=0;
      return 0;
    }
    this.reserveFade.set(this.reservePreview.subarray(0,compare));
    this.reserveFadeLength=compare;this.reserveFadeOffset=0;
    let remaining=best;
    while(remaining>0 && this.queue.length) {
      const item=this.queue[0], take=Math.min(remaining,item.frame.length-this.offset);
      this.offset+=take;this.queued-=take;remaining-=take;
      if(this.offset===item.frame.length){this.queue.shift();this.offset=0;}
    }
    this.reserveCompressedSamples+=best;
    return best;
  }

  beginUnderrun(audioMS) {
    if (!this.underrunTelemetryEnabled || this.openUnderrun) return;
    const event = {
      id: \`\${this.underrunEpoch}:\${++this.underrunSerial}\`,
      started_at: new Date(Date.now() + audioMS - currentTime * 1000).toISOString(),
      observed_until: "", duration_ms: 0, missing_samples: 0, sample_rate: sampleRate,
      start_audio_ms: audioMS, end_audio_ms: audioMS,
      last_sequence: this.lastPlayedSequence, end_reason: "ongoing", complete: false,
      timestamp_basis: "browser_wall_audio_clock",
    };
    this.underrunEvents.push(event);
    if (this.underrunEvents.length > 100) this.underrunEvents.shift();
    this.openUnderrun = event;
  }

  observeUnderrun(samples, audioEndMS) {
    const event = this.openUnderrun;
    if (!event || samples <= 0) return;
    event.missing_samples += samples;
    this.underrunSamples += samples;
    event.duration_ms = event.missing_samples * 1000 / sampleRate;
    event.end_audio_ms = Math.max(event.start_audio_ms, audioEndMS);
  }

  underrunSnapshot(event) {
    if (event.ended_at) return {...event};
    // Correlate against the original wall/audio anchor, regardless of later
    // clock changes. Format only the ongoing event at snapshot time.
    return {...event, observed_until: new Date(Date.parse(event.started_at) + event.end_audio_ms - event.start_audio_ms).toISOString()};
  }

  reportUnderrun(event) {
    this.port.postMessage({type: "playback.underrun", event: this.underrunSnapshot(event), underruns: this.underruns, underrun_ms: this.underrunSamples * 1000 / sampleRate});
  }

  endUnderrun(reason, audioMS, resumeSequence = null) {
    const event = this.openUnderrun;
    if (!event) return;
    event.end_audio_ms = Math.max(event.end_audio_ms, audioMS);
    event.ended_at = new Date(Date.parse(event.started_at) + event.end_audio_ms - event.start_audio_ms).toISOString();
    event.observed_until = event.ended_at;
    event.resume_sequence = resumeSequence;
    event.end_reason = reason;
    event.complete = true;
    this.openUnderrun = null;
    this.reportUnderrun(event);
  }

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
      this.resetReserveAdjustment();
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
      reserve_expanded_ms:this.reserveExpandedSamples*1000/sampleRate, reserve_compressed_ms:this.reserveCompressedSamples*1000/sampleRate, reserve_adjustments:this.reserveAdjustments, reserve_match_rejections:this.reserveMatchRejections,
      playback_events:this.playbackEvents.slice(),
      underrun_ms: this.underrunSamples * 1000 / sampleRate, underrun_events: this.underrunEvents.map(event => this.underrunSnapshot(event)),
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
    if (!this.playing && this.queued > 0) {
      const waitMS=this.startupWaitSamples*1000/sampleRate;
      const timeoutMS=Math.min(300,Math.max(120,this.targetMs+40));
      const reserveMS=Math.min(this.targetMs,Math.max(40,this.minTargetMs));
      const full=this.queued >= this.msToSamples(this.targetMs);
      const minimum=this.queued >= this.msToSamples(reserveMS);
      // Resampler startup and render-block boundaries can leave a near-full
      // reserve slightly below the target. After the target wait, tolerate one
      // render block rather than requiring another entire carrier packet.
      const roundedReserve=waitMS>=this.targetMs && this.queued>=Math.max(this.msToSamples(40),this.msToSamples(this.targetMs)-out.length);
      // Timeout cannot start continuous playback with an underfilled packet.
      // A bounded short-tail drain preserves short utterances without assuming
      // more speech exists or manufacturing an initial startup underrun.
      const tail=waitMS>=timeoutMS && !minimum && currentTime*1000-this.lastArrivalMS>=60;
      if (full || roundedReserve || (waitMS>=timeoutMS && minimum) || tail) {
        this.shortTailStartup=tail && !this.hasPlayed;
        this.shortTailRemaining=tail ? this.queued : null;
        this.playbackEvent("playback_start", full ? "target_ready" : roundedReserve ? "render_quantum_ready" : tail ? "short_tail" : "minimum_after_timeout", {minimum_ms:reserveMS});
        this.playing=true;this.startupWaitSamples=0;
      }
    }
    if (this.queued === 0) this.startupWaitSamples = 0;
    let written = 0, consumed = 0;
    if (this.playing) {
      if (this.queue.length > 0) {
        this.endUnderrun("recovered", currentTime * 1000, this.queue[0].sequence);
        this.underrunPending = false;
      }
      consumed += this.shortTailRemaining===null ? this.adjustLiveReserve(out.length) : 0;
      if (this.reservePendingLength > 0 && this.queue.length > 0) {
        const take=Math.min(out.length, this.reservePendingLength-this.reservePendingOffset);
        out.set(this.reservePending.subarray(this.reservePendingOffset,this.reservePendingOffset+take));
        written+=take;this.reserveExpandedSamples+=take;this.reservePendingOffset+=take;
        if(this.reservePendingOffset===this.reservePendingLength) this.reservePendingLength=this.reservePendingOffset=0;
      }
      while (written < out.length && this.queue.length > 0) {
        const item = this.queue[0];
        this.lastPlayedSequence = item.sequence;
        this.maxResidenceMS = Math.max(this.maxResidenceMS, currentTime * 1000 - item.arrived_ms);
        this.applyCrossfade(item);
        const take = Math.min(out.length - written, item.frame.length - this.offset,this.shortTailRemaining ?? Infinity);
        out.set(item.frame.subarray(this.offset, this.offset + take), written);
        const fade=Math.min(take,this.reserveFadeLength-this.reserveFadeOffset);
        for(let i=0;i<fade;i++) {
          const blend=(this.reserveFadeOffset+i+1)/this.reserveFadeLength;
          out[written+i]=this.reserveFade[this.reserveFadeOffset+i]*(1-blend)+out[written+i]*blend;
        }
        this.reserveFadeOffset+=fade;
        written += take; consumed += take; this.offset += take; this.queued -= take;
        if (this.offset >= item.frame.length) { this.queue.shift(); this.offset = 0; }
        if(this.shortTailRemaining!==null) {this.shortTailRemaining-=take;if(this.shortTailRemaining===0)break;}
      }
      if (written > 0 && !this.shortTailStartup) this.hasPlayed = true;
      if ((written < out.length || this.shortTailRemaining===0) && this.shortTailStartup) {
        this.playing=false;this.shortTailStartup=false;this.shortTailRemaining=null;this.resetReserveAdjustment();
        this.playbackEvent("short_tail_drained", "insufficient_continuation");
      } else if (written < out.length || this.shortTailRemaining===0) {
        this.shortTailRemaining=null;
        this.underruns += 1; this.targetMs = Math.min(this.maxTargetMs, this.targetMs + 20);
        this.playbackEvent("rebuffer", "underrun");
        this.playing = false; this.stableSamples = 0;
        this.resetReserveAdjustment();
        this.underrunPending = true;
        this.beginUnderrun(currentTime * 1000 + written * 1000 / sampleRate);
      } else {
        this.stableSamples += out.length;
        if (this.stableSamples >= sampleRate * 30 && this.targetMs > this.minTargetMs) {
          this.targetMs = Math.max(this.minTargetMs, this.targetMs - 10); this.stableSamples = 0;
          this.playbackEvent("target_changed", "stable_playback", {phase:"playing"});
        }
      }
    }
    if (this.underrunPending && !this.openUnderrun) this.beginUnderrun(currentTime * 1000 + written * 1000 / sampleRate);
    if (this.openUnderrun) {
      const first = this.openUnderrun.missing_samples === 0;
      this.observeUnderrun(out.length - written, (currentTime + out.length / sampleRate) * 1000);
      if (first) this.reportUnderrun(this.openUnderrun);
    }
    this.playedSamples += consumed;
    if (written === out.length) this.rememberCallerSamples(out, written);
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
`;var Ee=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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

let queueEvents = [], queueEventSerial = 0, queueBuilding = false, queueStartedMS = 0, queueLastEventMS = -Infinity;
let lastCaptureAge = null;
function observeCaptureQueue(message, now, age, reason) {
  const bytes=socket?.bufferedAmount ?? 0, queueMS=bytes*1000/(SAMPLE_RATE*2);
  let kind=reason;
  if (!queueBuilding && queueMS>=40) {queueBuilding=true;queueStartedMS=now;kind=kind || "queue_buildup";}
  else if (queueBuilding && queueMS<20) {queueBuilding=false;kind=kind || "queue_drained";}
  else if (queueBuilding && now-queueLastEventMS>=250) kind=kind || "queue_sample";
  if (!kind || (kind==="backpressure_drop" && now-queueLastEventMS<250)) return;
  queueLastEventMS=now;
  const duration=message?.frame?.length*1000/(message?.sample_rate||SAMPLE_RATE);
  queueEvents.push({id:\`capture-queue:\${++queueEventSerial}\`,timestamp:new Date().toISOString(),kind,reason:kind,
    queue_bytes:bytes,queue_ms:queueMS,duration_ms:queueStartedMS ? Math.max(0,now-queueStartedMS) : 0,
    frame_age_ms:Number.isFinite(age) ? age+(duration||0) : undefined,
    worker_delay_ms:Number.isFinite(age) ? Math.max(0,age-audioClockUncertainty) : undefined,
    clock_uncertainty_ms:audioClockUncertainty,sequence:message?.sequence});
  if(queueEvents.length>64)queueEvents.shift();
}
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
let sampleAt=performance.now(), captureWireBytes=0, playbackWireBytes=0, lastPlaybackReceipt=null;
let receiveGap=null, captureAge=null, transitMax=null, deliveryExcessMax=null, queueMax=null;
function stats() {
  flushMutedCapture();
  observeCaptureQueue(null,monotonicEpochMS(),null);
  const now=performance.now(), elapsed=Math.max(1,now-sampleAt);
  const observation=elapsed>=500 ? {window_ms:elapsed,timestamp:new Date().toISOString(),send_bitrate_bps:captureWireBytes*8000/elapsed,receive_bitrate_bps:playbackWireBytes*8000/elapsed,
    receive_gap_ms:receiveGap,capture_age_ms:captureAge,transit_ms:transitMax,delivery_excess_ms:deliveryExcessMax,server_queue_ms:queueMax} : undefined;
  if(observation){sampleAt=now;captureWireBytes=playbackWireBytes=0;receiveGap=captureAge=transitMax=deliveryExcessMax=queueMax=null;}
  timing.websocket_max_buffered_bytes=Math.max(timing.websocket_max_buffered_bytes,socket?.bufferedAmount ?? 0); postMessage({type:"transport.stats", observation, buffered_bytes:socket?.bufferedAmount||0, capture_queue_events:queueEvents.slice(), timing:{...timing, clock_uncertainty_ms:serverClockUncertainty, clock_sample_age_ms:serverClockSampleAt ? monotonicEpochMS()-serverClockSampleAt : null}}); }
function dropCapture(message,reason,age=0) {
 const duration = message.frame.length*1000/(message.sample_rate||SAMPLE_RATE);
 timing.capture_dropped_ms+=duration;
 timing.drop_totals_ms[reason]=(timing.drop_totals_ms[reason]||0)+duration;
 resamplers.delete(\`\${message.sample_rate || SAMPLE_RATE}:\${SAMPLE_RATE}\`);
 postMessage({type:"transport.drop",event:{timestamp:new Date().toISOString(),direction:"operator_to_carrier",reason,duration_ms:duration,queue_before_ms:age,sequence:message.sequence,
   frame_age_ms:lastCaptureAge===null ? undefined : lastCaptureAge+duration,
   worker_delay_ms:lastCaptureAge===null ? undefined : Math.max(0,lastCaptureAge-audioClockUncertainty),
   clock_uncertainty_ms:audioClockUncertainty,queue_bytes:socket?.bufferedAmount ?? 0}});
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
  lastCaptureAge=null;
  if(capturePaused || audioClockOffset===null || now-audioClockSampleAt>5000) {
    capturePort?.postMessage({type:"clock.probe",nonce:now});
    dropCapture(message,"capture_clock_unavailable"); return;
  }
  const mapped = Number.isFinite(message.timestamp_ms) && audioClockOffset!==null ? message.timestamp_ms+audioClockOffset : null;
  const age = mapped===null ? 0 : Math.max(0,now-mapped);
  lastCaptureAge=age;
  observeCaptureQueue(message,now,age);
  timing.capture_frames++;
  captureAge=Math.max(captureAge,age);
  timing.capture_max_age_ms=Math.max(timing.capture_max_age_ms,age);
  // Use a conservative lower bound, not an assumed exact clock offset.
  if (mapped!==null && (age-audioClockUncertainty>MAX_CAPTURE_AGE_MS || mapped+audioClockUncertainty<captureGateOpenedAt)) {
    dropCapture(message,"capture_age_limit",age); return;
  }
  if (socket.bufferedAmount > MAX_CAPTURE_BUFFERED_BYTES) {
    observeCaptureQueue(message,now,age,"backpressure_drop");
    dropCapture(message,"websocket_backpressure",socket.bufferedAmount*1000/(SAMPLE_RATE*2)); return;
  }
  flushMutedCapture();
  const wire=framedCapture(message.frame, message.sequence, message.timestamp_ms, message.sample_rate, age);
  socket.send(wire);captureWireBytes+=wire.byteLength;
  timing.capture_sent_ms+=message.frame.length*1000/(message.sample_rate||SAMPLE_RATE);
}

function connect() {
  if (closed || !mediaURL) return;
  lastPlaybackReceipt=null;sampleAt=performance.now();captureWireBytes=playbackWireBytes=0;receiveGap=captureAge=transitMax=deliveryExcessMax=queueMax=null;
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
    queueBuilding=false;queueStartedMS=0;lastCaptureAge=null;
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
    const receipt=performance.now();if(lastPlaybackReceipt!==null)receiveGap=Math.max(receiveGap,receipt-lastPlaybackReceipt);lastPlaybackReceipt=receipt;playbackWireBytes+=buffer.byteLength;
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
      if(Number.isFinite(queued)){queueMax=Math.max(queueMax,queued);timing.playback_max_server_queue_ms=Math.max(timing.playback_max_server_queue_ms,queued);}
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
        deliveryExcessMax=Math.max(deliveryExcessMax,deliveryExcess);
        timing.playback_max_delivery_excess_ms=Math.max(timing.playback_max_delivery_excess_ms,deliveryExcess);
        if(audioClockOffset!==null) sourceAudioMS=monotonicEpochMS()-audioClockOffset-deliveryExcess+audioClockUncertainty;
      }
      // Measure every received frame before selecting any rejection reason.
      let transit=null, sourceAge=null;
      if(serverClockOffset!==null && monotonicEpochMS()-serverClockSampleAt<15000 && Number.isFinite(sent)) {
        transit=Math.max(0,monotonicEpochMS()+serverClockOffset-sent);
        transitMax=Math.max(transitMax,transit);
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
    case "send.telemetry":
      // Monitoring parts may be lost under pressure; microphone audio is
      // authoritative and must never wait behind a diagnostic backlog.
      if(socket?.readyState===WebSocket.OPEN && socket.bufferedAmount<=1920) {
        try{socket.send(message.data);}catch{/* observational only */}
      }
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
      const closing=socket;
      socket=null;
      if (closing?.readyState===WebSocket.OPEN) {
        // This metadata only describes browser shutdown. It never hangs up the
        // carrier, changes authorization or disables recovery on another tab.
        try {closing.send(JSON.stringify({type:"media.shutdown",reason:message.reason || "session_cleanup"}));} catch { /* close still proceeds if metadata cannot be sent */ }
      }
      let finished=false;
      const finishClose=()=>{if(finished)return;finished=true;postMessage({type:"socket.shutdown.complete"});close();};
      if (closing) {
        closing.onclose=finishClose;
        try {closing.close(1000,"client_shutdown");} catch {finishClose();}
        setTimeout(finishClose,500);
      } else finishClose();
      capturePort?.close();
      playbackPort?.close();
      break;
  }
};
`;function ie(e=!1){let t=(e?[Z]:[Z,Ee]).map((s)=>URL.createObjectURL(new Blob([s],{type:"text/javascript"})));return{urls:t,dispose(){t.splice(0).forEach((s)=>URL.revokeObjectURL(s))}}}async function de(e,t=!1){if(typeof location>"u")return ie(t);let s=new URL(e.mcpURL(),location.href);if(s.origin!==location.origin)return ie(t);if(e.name!=="telephony"||!e.projectId||!Number.isSafeInteger(e.installId)||e.installId<=0)throw Error("Telephony audio requires a project and installation");return{urls:await Promise.all((t?[Z]:[Z,Ee]).map(async(r,i)=>{let u=new TextEncoder().encode(r),p=Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",u)),(y)=>y.toString(16).padStart(2,"0")).join(""),f=`/ui/frontend/${i===0?"worklet":"worker"}-${p}.js`;if(await e.get(f,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==r)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let m=new URL(`/api/apps/telephony/_install/${e.installId}${f}`,s);return m.searchParams.set("project_id",e.projectId),m.searchParams.set("install_id",String(e.installId)),m.href})),dispose(){}}}class ke{options;target;bitrate=32000;stable;last=-1/0;initial;maximum;constructor(e={}){this.options=e;this.initial=e.playbackTargetMs??60,this.target=this.initial,this.maximum=Math.max(this.initial,e.playbackMaxMs??160)}observe(e,t=performance.now()){if(t<this.last)this.last=-1/0,this.stable=void 0;if(t-this.last<2000)return;this.last=t;let s=e.receiver_jitter===void 0?void 0:e.receiver_jitter*1000,h=e.receiver_concealed_delta_ms??0;if(this.options.playbackAdaptive!==!1){let i=Math.min(this.maximum,Math.max(this.initial,this.initial+Math.ceil(Math.max(0,(s??0)-10)*2/20)*20,h>0?this.target+20:this.initial));if(i>this.target)this.target=Math.min(i,this.target+20),this.stable=void 0;else if(s!==void 0&&h===0&&s<10){if(this.stable??=t,t-this.stable>=1e4)this.target=Math.max(this.initial,this.target-10),this.stable=t}else this.stable=void 0}let{pair_availableOutgoingBitrate:a,remote_receiver_fractionLost:r}=e;if(a!==void 0&&a>0&&a<64000)this.bitrate=Math.max(16000,Math.min(32000,Math.floor((a-32000)*0.8/1000)*1000));else if(r!==void 0&&r>=0.03)this.bitrate=Math.max(16000,this.bitrate-4000);else if(r!==void 0&&r<0.01)this.bitrate=Math.min(32000,this.bitrate+2000)}}class xe{tokens=1100;at;rate=640;observeAvailableBitrate(e){this.rate=e!==void 0&&e>=96000?2000:640}consume(e,t=performance.now()){if(!Number.isFinite(e)||!Number.isFinite(t)||e<0||e>1100)return!1;if(this.at!==void 0)this.tokens=Math.min(1100,this.tokens+Math.max(0,t-this.at)*this.rate/1000);if(this.at=t,this.tokens<e)return!1;return this.tokens-=e,!0}}function tt(e){if(e===void 0)return"websocket";if(!["websocket","webrtc","auto"].includes(e))throw RangeError("Unsupported softphone media transport");return e}function st(e){let t=new URL(e);return t.searchParams.set("transport","webrtc"),t.toString()}var x=(e)=>{try{e?.()}catch{}},$=(e,t=1000000000000)=>typeof e==="number"&&Number.isFinite(e)?Math.max(0,Math.min(e,t)):0;function We(e){let t=te(e);return t.denied||t.expired||["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes(e?.name)}function ht(e,t){let s=0,h=0,a=0,r=0,i=0,u=0,p=0,f=0,d=0,m=null,y,c,S,o={},_={},l={},M=(n,g)=>typeof n?.[g]==="number"&&Number.isFinite(n[g])?Math.max(0,n[g]):void 0,N=(n,g,b)=>{for(let q of b){let A=M(n,q);if(A!==void 0)_[g+q]=A}},R=(n,g,b,q,A)=>{let P=M(n,b),w=M(n,q),E=t?.counters?.[g];if(P!==void 0&&w!==void 0){if(o[g]??={},o[g][b]=P,o[g][q]=w,E&&P>=E[b]&&w>E[q])_[A]=(P-E[b])/(w-E[q])*1000}},G=(n,g,b)=>{let q=M(n,"packetsLost"),A=M(n,"ssrc"),P=M(n,"timestamp"),w=t?.counters?.[g];if(A!==void 0)_[b+"ssrc"]=A;if(q===void 0||P===void 0)return;if(o[g]??={},o[g].packetsLost=q,o[g].at=P,A!==void 0)o[g].ssrc=A;if(w&&w.ssrc===A&&q>=w.packetsLost&&P>w.at)_[b+"loss_delta"]=(_[b+"loss_delta"]??0)+(q-w.packetsLost),_[b+"loss_window_ms"]=P-w.at},W=(n,g,b)=>{let q=M(n,"concealedSamples"),A=M(n,"silentConcealedSamples"),P=M(n,"timestamp"),w=M(n,"ssrc"),E=t?.counters?.[g];if(q===void 0||P===void 0)return;if(o[g]??={},Object.assign(o[g],{concealedSamples:q,at:P}),w!==void 0)o[g].ssrc=w;if(A!==void 0)o[g].silentConcealedSamples=A;if(E&&E.ssrc===w&&q>=E.concealedSamples&&P>E.at){if(_.receiver_concealed_delta_ms=(_.receiver_concealed_delta_ms??0)+(q-E.concealedSamples)*1000/b,_.receiver_concealed_window_ms=P-E.at,A!==void 0&&A>=E.silentConcealedSamples)_.receiver_silent_concealed_delta_ms=(_.receiver_silent_concealed_delta_ms??0)+(A-E.silentConcealedSamples)*1000/b}},I=!1,ee=0;e.forEach((n,g)=>{if(d=Math.max(d,$(n.timestamp,1000000000000000)),(n.kind??n.mediaType??e.get(n.localId)?.kind)==="audio"&&ee++<32){if(n.type==="outbound-rtp")s+=$(n.bytesSent),N(n,"sender_",["bytesSent","packetsSent","headerBytesSent","retransmittedPacketsSent","retransmittedBytesSent","nackCount","targetBitrate"]),R(n,g,"totalPacketSendDelay","packetsSent","sender_send_delay_interval_ms");if(n.type==="inbound-rtp"){G(n,g,"receiver_"),h+=$(n.bytesReceived),a+=$(n.packetsLost),r=Math.max(r,$(n.jitter)*1000);let q=e.get(n.codecId),A=M(q,"clockRate")??48000;if(W(n,g,A),I||=M(n,"concealedSamples")!==void 0,i+=$(n.concealedSamples)*1000/A,u+=$(n.packetsDiscarded),p+=$(n.jitterBufferDelay),f+=$(n.jitterBufferEmittedCount),N(n,"receiver_",["bytesReceived","packetsReceived","packetsLost","jitter","packetsDiscarded","concealedSamples","silentConcealedSamples","concealmentEvents","insertedSamplesForDeceleration","removedSamplesForAcceleration","totalSamplesReceived","audioLevel","totalAudioEnergy","totalSamplesDuration","nackCount","fecPacketsReceived","fecPacketsDiscarded"]),R(n,g,"jitterBufferDelay","jitterBufferEmittedCount","receiver_jitter_buffer_interval_ms"),R(n,g,"jitterBufferTargetDelay","jitterBufferEmittedCount","receiver_jitter_target_interval_ms"),R(n,g,"jitterBufferMinimumDelay","jitterBufferEmittedCount","receiver_jitter_minimum_interval_ms"),R(n,g,"totalProcessingDelay","packetsReceived","receiver_processing_interval_ms"),M(n,"lastPacketReceivedTimestamp")!==void 0)_.receiver_last_packet_age_ms=Math.max(0,n.timestamp-n.lastPacketReceivedTimestamp);if(q){if(l.codec=q.mimeType,_.codec_clock_rate=A,M(q,"channels")!==void 0)_.codec_channels=q.channels}}if(n.type==="remote-inbound-rtp")N(n,"remote_receiver_",["packetsLost","fractionLost","jitter","roundTripTime","totalRoundTripTime","roundTripTimeMeasurements"]),G(n,g,"remote_receiver_");if(n.type==="remote-outbound-rtp")N(n,"remote_sender_",["packetsSent","bytesSent"]);let b=n.type==="outbound-rtp"?"bytesSent":n.type==="inbound-rtp"?"bytesReceived":void 0;if(b&&M(n,b)!==void 0)o[g]??={},o[g][b]=n[b],o[g].at=n.timestamp}if(n.type==="transport"&&n.selectedCandidatePairId){y=n.selectedCandidatePairId,l.dtls=n.dtlsState;let b=e.get(y);if(b){m=M(b,"currentRoundTripTime")!==void 0?b.currentRoundTripTime*1000:null,N(b,"pair_",["availableOutgoingBitrate","availableIncomingBitrate","currentRoundTripTime","totalRoundTripTime","bytesSent","bytesReceived","requestsSent","requestsReceived","responsesSent","responsesReceived","consentRequestsSent"]),l.pair=b.state;let q=e.get(b.localCandidateId),A=e.get(b.remoteCandidateId);if(q)c=q.protocol,S=q.candidateType,l.protocol=c,l.local_candidate=S,l.relay_protocol=q.relayProtocol;if(A)l.remote_candidate=A.candidateType}}});let re=(n,g)=>{if(t?.counters){let q=0;for(let[A,P]of Object.entries(o)){let w=t.counters[A];if(w&&P[n]>=w[n]&&P.at>w.at)q+=(P[n]-w[n])*8000/(P.at-w.at)}return q}let b=t&&d>t.at?(d-t.at)/1000:0;return b?Math.max(0,g-(n==="bytesSent"?t.sent:t.received))*8/b:0},ue=(t?.pathRevision??0)+(y&&y!==t?.pairID?1:0);_.path_revision=ue;let Y=re("bytesSent",s),_e=re("bytesReceived",h);return{previous:{at:d,sent:s,received:h,counters:o,pairID:y,pathRevision:ue},rtt:m,queueMs:f>0?p/f*1000:0,metrics:qe({..._,send_bitrate_bps:Y,receive_bitrate_bps:_e}),states:Ae(l),webrtc:{protocol:c,candidateType:S,sendBitrateBps:Y,receiveBitrateBps:_e,packetsLost:a,jitterMs:r,concealedMs:I?i:void 0,packetsDiscarded:u,jitterBufferMs:f>0?p/f*1000:0}}}function at(e,t=new Date().toISOString()){let s=[];for(let[h,a]of[["receiver_","carrier_to_operator"],["remote_receiver_","operator_to_carrier"]]){let r=e[h+"loss_delta"];if(r>0)s.push({timestamp:t,direction:a,reason:"webrtc_packet_loss",duration_ms:0,packet_count:r,ssrc:e[h+"ssrc"],window_ms:e[h+"loss_window_ms"]})}if(e.receiver_concealed_delta_ms>0)s.push({timestamp:t,direction:"carrier_to_operator",reason:"webrtc_concealment",duration_ms:Math.round(e.receiver_concealed_delta_ms),ssrc:e.receiver_ssrc,window_ms:e.receiver_concealed_window_ms});return s}class Ce{callbacks;stopped=!1;generation=0;socket;pc;stream;context;capture;whisper;destination;speaker;remoteAudio;speakerMeter;carrierStalled=!1;deliveryDegraded=!1;ready=!1;peer=!1;muted=!1;whisperEpoch=null;whisperBase=null;whisperResampler=new J;timer;retry;cancelSetup;recovering=!1;recoveryGeneration=0;recoveryExpiry;ringback;options;workletURL;events=[];previous;sampling=new WeakSet;transportTelemetry=new ne("webrtc");transportSender=new fe((e)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[e]}})},(e)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry(e)});controlBudget=new xe;quality;playbackReceiver;uploadSender;applyingBitrate=!1;appliedBitrate;statsErrors=0;lastStatsSend=-1/0;clientEpoch=crypto.randomUUID();nativeCounts;completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};runtime=new pe((e)=>this.recordSessionEvent(e));diagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:null,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(e){this.callbacks=e}async start(e,t,s){if(this.options=t,this.workletURL=s,!s)throw Error("WebRTC requires the shared Telephony audio processor");if(typeof RTCPeerConnection!=="function")throw Error("WebRTC audio is unavailable in this browser");j(t),await this.connect(e)}current(e){return!this.stopped&&this.generation===e}guard(e){if(!this.current(e))throw Error("Audio session cancelled")}async connect(e){this.runtime.observeEnvironment();let t=++this.generation,s=this.options;for(let m of this.events.slice(-8))this.enqueueRTCEvents({session_events:[m]});for(let m of this.diagnostics.dropEvents.slice(-8))this.enqueueRTCEvents({drop_events:[m]});this.ready=this.peer=!1,this.previous=void 0,this.quality=new ke(s),this.appliedBitrate=void 0;let h=await navigator.mediaDevices.getUserMedia({audio:ae(s)});if(!this.current(t))throw h.getTracks().forEach((m)=>m.stop()),Error("Audio session cancelled");this.stream=h;let a=h.getAudioTracks()[0];if(!a)throw Error("No microphone audio track");let r=a.getSettings();this.diagnostics={...this.diagnostics,microphoneSampleRate:r.sampleRate??0,microphoneChannelCount:r.channelCount??1,echoCancellation:typeof r.echoCancellation==="boolean"?r.echoCancellation:null,noiseSuppression:r.noiseSuppression??null,autoGainControl:r.autoGainControl??null,micInputGainDb:s.inputGainDB},a.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),a.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),a.onended=()=>{if(this.current(t))this.disconnected(t,"microphone_ended")},this.context=new AudioContext({latencyHint:"interactive"});let i=this.context;if(i.onstatechange=()=>this.runtime.context(i.state),this.runtime.context(i.state),await i.resume(),this.guard(t),s.outputDeviceId&&"setSinkId"in i)await i.setSinkId(s.outputDeviceId);await i.audioWorklet.addModule(this.workletURL),this.guard(t),this.diagnostics.audioContextRate=i.sampleRate,this.capture=new AudioWorkletNode(i,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),this.destination=i.createMediaStreamDestination(),this.destination.channelCount=1,i.createMediaStreamSource(h).connect(this.capture).connect(this.destination),this.capture.port.onmessage=(m)=>{if(m.data?.type==="capture.stats"){let y=(S)=>S>0?20*Math.log10(S):null;this.diagnostics.micActiveRmsDbfs=y(m.data.active_rms),this.diagnostics.micPeakDbfs=y(m.data.pre_peak),this.diagnostics.micPostPeakDbfs=y(m.data.post_peak),this.diagnostics.micLimiterReductionDb=m.data.limiter_reduction_db??0;let c=0;if(this.speakerMeter){let S=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(S),c=Math.sqrt(S.reduce((o,_)=>o+_*_,0)/S.length)}x(()=>this.callbacks.onLevels?.($(m.data.active_rms,1),c))}},this.speaker=i.createGain(),this.speaker.gain.value=s.outputVolume??1,this.speaker.connect(i.destination),this.speakerMeter=i.createAnalyser(),this.speakerMeter.fftSize=256;let u=i.createGain();u.gain.value=0,this.speaker.connect(this.speakerMeter).connect(u).connect(i.destination),this.whisper=new AudioWorkletNode(i,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:j(s)}),this.whisper.connect(this.speaker),this.gate();let p=new WebSocket(st(e));p.binaryType="arraybuffer",this.socket=p;let f=!1,d=!1;await new Promise((m,y)=>{let c=(_)=>{if(d)return;d=!0,clearTimeout(S),this.cancelSetup=void 0,_?y(_):m()},S=setTimeout(()=>c(Error("WebRTC audio connection timed out")),18000);this.cancelSetup=()=>c(Error("Audio session cancelled"));let o=()=>{if(this.current(t)&&this.ready&&f)this.gate(),c(),x(()=>this.callbacks.onState?.("live"))};p.onmessage=(_)=>{if(!this.current(t))return;if(_.data instanceof ArrayBuffer){this.playWhisper(_.data);return}if(typeof _.data!=="string")return;let l;try{l=JSON.parse(_.data)}catch{return}if(l.type==="webrtc.config"){if(this.pc){c(Error("Repeated WebRTC configuration"));return}(async()=>{let M=new RTCPeerConnection({iceServers:l.ice_servers??[]});this.pc=M,this.destination.stream.getAudioTracks().forEach((R)=>{let G=M.addTrack(R,this.destination.stream);this.uploadSender=G,this.applyUploadBitrate(G,32000,t)}),M.ontrack=(R)=>{if(!this.current(t))return;this.playbackReceiver=R.receiver,this.applyPlaybackTarget();let G=new MediaStream([R.track]),W=new Audio;W.muted=!0,W.srcObject=G,this.remoteAudio=W,W.play().catch(()=>x(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction."))),i.createMediaStreamSource(G).connect(this.speaker)},M.onconnectionstatechange=()=>{if(!this.current(t))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:M.connectionState}),M.connectionState==="connected")f=!0,o();if(M.connectionState==="failed"||M.connectionState==="closed")if(!d)c(Error("WebRTC connection failed"));else this.disconnected(t,"webrtc_"+M.connectionState)};let N=await M.createOffer();this.guard(t),await M.setLocalDescription(N),await new Promise((R,G)=>{if(M.iceGatheringState==="complete"){R();return}let W=setTimeout(()=>{M.onicegatheringstatechange=null,G(Error("ICE gathering timed out"))},7000);M.onicegatheringstatechange=()=>{if(M.iceGatheringState==="complete")clearTimeout(W),M.onicegatheringstatechange=null,R()}}),this.guard(t),p.send(JSON.stringify({type:"webrtc.offer",sdp:M.localDescription.sdp}))})().catch((M)=>c(M instanceof Error?M:Error("WebRTC setup failed")));return}if(l.type==="webrtc.answer"){this.pc?.setRemoteDescription({type:"answer",sdp:l.sdp}).catch(()=>c(Error("Invalid WebRTC answer")));return}if(l.type==="ready")this.ready=!0,this.send({type:"media.capabilities",version:2,versions:[3],whisper:!0}),o();if(l.type==="peer.connected")this.peer=!0,this.gate();if(l.type==="peer.disconnected")this.peer=!1,this.gate(),x(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));if(l.type==="call.status")x(()=>this.callbacks.onCallStatus?.(l));if(l.type==="call.error")if(!d)c(Error(l.detail??"Carrier activation failed"));else this.stop(),x(()=>this.callbacks.onState?.("error",l.detail));if(l.type==="call.ended"||l.type==="session.replaced")if(!d)c(Error(l.type));else this.stop(),x(()=>this.callbacks.onState?.("ended",l.type));if(l.type==="audio.health"){if(this.diagnostics.audioHealth={state:l.state,reason:l.reason,stages:l.stages},x(()=>this.callbacks.onAudioHealth?.(l)),l.state==="audio_degraded"&&!this.deliveryDegraded)this.deliveryDegraded=!0,x(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));else if(this.deliveryDegraded&&l.state==="healthy"&&l.stages?.telephony_to_browser?.state==="healthy"&&i.state==="running")this.deliveryDegraded=!1,x(()=>this.callbacks.onNotice?.("Speech delivery restored."))}if(l.type==="media.delivery"){if(l.state==="stalled")x(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));else if(l.state==="flowing"&&this.carrierStalled)x(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));this.carrierStalled=l.state==="stalled"}if(l.type==="call.notice")x(()=>this.callbacks.onNotice?.(l.detail??l.reason??"Audio delivery notice"));if(l.type==="coach.state")this.whisperEpoch=l.talking&&Number.isInteger(l.epoch)?l.epoch:null,this.whisperBase=null,this.whisper?.port.postMessage({type:"whisper.clear"})},p.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"}),p.onclose=(_)=>{if(!this.current(t))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(_.code),was_clean:_.wasClean}),!d){c(Object.assign(Error("WebRTC signaling unavailable"),_.code===1008?{status:403}:{}));return}if(_.code===1008){this.stop(),x(()=>this.callbacks.onState?.("error","Media authorization ended"));return}this.disconnected(t,"signaling_closed")}}),this.guard(t),this.timer=setInterval(()=>{this.runtime.tick(),this.statistics(t)},1000),await this.statistics(t)}send(e){if(this.socket?.readyState===WebSocket.OPEN&&this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(e))}catch{}}gate(){let e=this.ready&&this.peer&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!e}),this.destination?.stream.getAudioTracks().forEach((t)=>t.enabled=e)}async statistics(e){if(!this.current(e)||!this.pc||this.sampling.has(this.pc))return;let t=this.pc,s=performance.now();this.sampling.add(t);try{let h=ht(await t.getStats(),this.previous);if(!this.current(e)||this.pc!==t)return;if(this.previous=h.previous,this.nativeCounts={packetsLost:h.webrtc.packetsLost,packetsDiscarded:h.webrtc.packetsDiscarded,concealedMs:h.webrtc.concealedMs},h.webrtc.packetsLost+=this.completedCounts.packetsLost,h.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded,h.webrtc.concealedMs!==void 0)h.webrtc.concealedMs+=this.completedCounts.concealedMs;if(this.quality?.observe(h.metrics),this.applyPlaybackTarget(),this.uploadSender&&this.quality)this.applyUploadBitrate(this.uploadSender,this.quality.bitrate,e);if(h.metrics.requested_playback_target_ms=this.quality?.target??60,this.appliedBitrate!==void 0)h.metrics.requested_upload_bitrate_bps=this.appliedBitrate;this.controlBudget.observeAvailableBitrate(h.metrics.pair_availableOutgoingBitrate);let a=at(h.metrics);for(let i of a)this.enqueueRTCEvents({drop_events:[i]});this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,...a].slice(-100),this.diagnostics={...this.diagnostics,rttMs:h.rtt,queueMs:h.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,h.queueMs),targetMs:this.quality?.target??this.options?.playbackTargetMs??60,webrtc:h.webrtc,playbackSequenceGaps:h.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};let r=this.stream?.getAudioTracks()[0];this.transportTelemetry.observe({...h.metrics,rtt_ms:h.rtt,queue_ms:h.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:this.socket?.bufferedAmount??0,stats_errors:this.statsErrors,stats_duration_ms:performance.now()-s,main_thread_max_pause_ms:this.runtime.counters.main_thread_max_pause_ms},{...h.states,ice:t.iceConnectionState,connection:"connected",context:this.context?.state,muted:String(this.muted),device_muted:String(r?.muted),track:r?.readyState}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),x(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}catch{if(this.current(e)&&this.pc===t)this.statsErrors++,this.transportTelemetry.observe({stats_errors:this.statsErrors,stats_duration_ms:performance.now()-s},{ice:t.iceConnectionState,context:this.context?.state,muted:String(this.muted)}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),x(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}finally{this.sampling.delete(t)}}applyPlaybackTarget(){let e=this.playbackReceiver;if(!e||!this.quality)return;if("jitterBufferTarget"in e)try{e.jitterBufferTarget=this.quality.target}catch{}}async applyUploadBitrate(e,t,s){if(this.applyingBitrate||this.appliedBitrate===t)return;this.applyingBitrate=!0;try{let h=e.getParameters();if(h.encodings?.length){if(h.encodings[0].maxBitrate=t,await e.setParameters(h),this.current(s)&&this.uploadSender===e)this.appliedBitrate=t}}catch{}finally{this.applyingBitrate=!1}}sendTelemetry(e){if(!this.ready||this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let t=JSON.stringify(e),s=new TextEncoder().encode(t).byteLength+58;if(!this.controlBudget.consume(s))return!1;try{return this.socket.send(t),!0}catch{return!1}}enqueueRTCEvents(e){try{let t={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,...e}};if(new TextEncoder().encode(JSON.stringify(t)).byteLength+58>1100){this.transportSender.skippedAuxiliary++;return}this.transportSender.enqueueAuxiliary(t)}catch{this.transportSender.skippedAuxiliary++}}sendStatistics(){let e=this.stream?.getAudioTracks()[0];if(performance.now()-this.lastStatsSend>=5000)this.lastStatsSend=performance.now(),this.transportSender.enqueueReport({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,media_transport:"webrtc",codec:"opus",webrtc:Object.fromEntries(Object.entries(this.diagnostics.webrtc??{}).map(([t,s])=>[t,typeof s==="number"?Math.round(s*10)/10:s])),connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:e?.readyState,microphone_device_muted:e?.muted,rtt_ms:this.diagnostics.rttMs===null?null:Math.round(this.diagnostics.rttMs),playback_queue_ms:Math.round(this.diagnostics.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:this.diagnostics.webrtc?.packetsLost}}),this.transportSender.enqueue(this.transportTelemetry.drain()),this.enqueueRTCEvents({timing:{runtime:this.runtime.counters}})}playWhisper(e){if(e.byteLength<17||e.byteLength>176||this.whisperEpoch===null||!this.context)return;let t=new DataView(e);if(t.getUint32(0,!0)!==827805761||t.getUint32(4,!0)!==this.whisperEpoch)return;let s=t.getFloat64(8,!0);if(!Number.isFinite(s))return;let h=performance.timeOrigin+performance.now()-s;this.whisperBase=this.whisperBase===null?h:Math.min(this.whisperBase,h);let a=Math.max(0,h-this.whisperBase);if(a>200)return;let r=new Float32Array(e.byteLength-16);for(let u=0;u<r.length;u++){let p=~t.getUint8(u+16)&255,f=((p&15)<<3)+132<<(p>>4&7);r[u]=(p&128?132-f:f-132)/32768}let i=this.context.sampleRate===8000?r:this.whisperResampler.process(r,8000,this.context.sampleRate);this.whisper?.port.postMessage({type:"whisper",frame:i,received_audio_ms:this.context.currentTime*1000-a},[i.buffer])}disconnected(e,t){if(!this.current(e)||this.recovering)return;this.recovering=!0,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:t}),this.cleanup(),x(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));let s=++this.recoveryGeneration,h=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===s;this.recoveryExpiry=setTimeout(()=>{if(!h())return;++this.recoveryGeneration,this.recovering=!1,clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup(),x(()=>this.callbacks.onState?.("error","WebRTC audio recovery timed out; reconnect audio to the existing call."))},30000);let a=async()=>{if(!h())return;try{if(!this.callbacks.refreshMediaURL)throw Error("Fresh media authorization is required");let r=await this.callbacks.refreshMediaURL();if(!h())return;if(await this.connect(r),!h())return;if(this.pc?.connectionState!=="connected")throw Error("WebRTC disconnected during recovery");clearTimeout(this.recoveryExpiry),this.recovering=!1,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected"})}catch(r){if(!h())return;if(this.cleanup(),We(r)){clearTimeout(this.recoveryExpiry),this.recovering=!1,x(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return}this.retry=setTimeout(()=>void a(),1000)}};a()}recordSessionEvent(e){this.events=[...this.events,e].slice(-100),this.enqueueRTCEvents({session_events:[e]}),x(()=>this.callbacks.onSessionEvent?.(e))}setMuted(e){if(this.muted!==e)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:e?"muted":"unmuted"});if(this.muted=e,this.gate(),e)this.send({type:"interrupt"})}sendDTMF(e){if(!/^[0-9*#]+$/.test(e))throw Error("Invalid DTMF digits");this.send({type:"dtmf",digits:e})}setOutputVolume(e){if(!Number.isFinite(e)||e<0||e>1)throw RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=e;if(this.options)this.options.outputVolume=e}startRingback(e){if(this.stopRingback(),this.context&&this.speaker)this.ringback=ye(this.context,this.speaker,Me(e))}stopRingback(){this.ringback?.(),this.ringback=void 0}cleanup(){if(this.runtime.stopEnvironment(),this.transportSender.stop(),this.nativeCounts){for(let h of["packetsLost","packetsDiscarded","concealedMs"])this.completedCounts[h]+=this.nativeCounts[h]??0;this.nativeCounts=void 0}++this.generation,this.ready=this.peer=!1,this.stopRingback(),clearInterval(this.timer),this.timer=void 0;let e=this.socket;if(this.socket=void 0,e)e.onclose=e.onmessage=e.onerror=null,e.close();this.playbackReceiver=void 0,this.uploadSender=void 0;let t=this.pc;if(this.pc=void 0,t)t.onconnectionstatechange=t.ontrack=null,t.close();if(this.remoteAudio)this.remoteAudio.pause(),this.remoteAudio.srcObject=null,this.remoteAudio=void 0;this.stream?.getTracks().forEach((h)=>{h.onended=null,h.stop()}),this.stream=void 0,this.destination?.stream.getTracks().forEach((h)=>h.stop()),this.destination=void 0,this.capture?.disconnect(),this.capture=void 0,this.whisper?.disconnect(),this.whisper=void 0,this.whisperEpoch=null,this.whisperBase=null,this.whisperResampler=new J;let s=this.context;if(this.context=void 0,s)s.onstatechange=null,s.close().catch(()=>{});this.speaker=void 0,this.speakerMeter=void 0}stop(){if(this.stopped)return;this.stopped=!0,++this.recoveryGeneration,clearTimeout(this.recoveryExpiry),clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup()}}function Ue(e,t,s){let h,a=!1,r=!1;return{async start(i,u){let p=tt(u.mediaTransport),f=async(d)=>{if(a)throw Error("Audio session cancelled");let m=d();h=m,m.setMuted(r);try{if(await m.start(i,u),a)throw Error("Audio session cancelled")}catch(y){if(m.stop(),h===m)h=void 0;throw y}};if(p==="websocket"){await f(t);return}try{await f(s)}catch(d){if(p!=="auto"||a||We(d))throw d;x(()=>e.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"})),await f(t)}},stop(){a=!0,h?.stop(),h=void 0},setMuted(i){r=i,h?.setMuted(i)},setOutputVolume(i){h?.setOutputVolume(i)},sendDTMF(i){h?.sendDTMF(i)},recordSessionEvent(i){h?.recordSessionEvent?.(i)},startRingback(i){h?.startRingback?.(i)},stopRingback(){h?.stopRingback?.()}}}async function He(){return(await navigator.mediaDevices.enumerateDevices()).filter((t)=>t.kind==="audioinput").map((t,s)=>({deviceId:t.deviceId,label:t.label||`Microphone ${s+1}`}))}function ve(e,t){let s=new we(e,!1),h,a=!1,r,i=()=>{return a=!0,r??=(async()=>{try{await s.cancel()}finally{h?.dispose(),h=void 0}})()};return{async start(u={}){if(a)throw Error("Microphone preview is already used");a=!0;try{if(h=t?await de(t,!0):ie(!0),r)throw h.dispose(),h=void 0,Error("Microphone preview was cancelled");await s.start(h.urls[0],{...he,...u})}catch(p){throw await i(),p}},stop:i}}function it(e){return{async preflight(t){(await navigator.mediaDevices.getUserMedia({audio:ae(t)})).getTracks().forEach((h)=>h.stop())},create(t){let s=new Pe(t),h,a=!1,r=()=>{a=!0;try{s.stop()}finally{h?.dispose(),h=void 0}};return{async start(i,u){try{if(h=e?await de(e):ie(),a)throw Error("Audio session was cancelled");await s.start(i,h.urls[0],h.urls[1],u)}catch(p){throw r(),p}},stop:r,recordSessionEvent:(i)=>s.recordSessionEvent(i),setMuted:(i)=>s.setMuted(i),sendDTMF:(i)=>s.sendDTMF(i),setOutputVolume:(i)=>s.setOutputVolume(i),startRingback:(i)=>s.startRingback(i),stopRingback:()=>s.stopRingback()}}}}function Ve(e){let t=it(e);return{preflight:(s)=>t.preflight(s),create(s){return Ue(s,()=>t.create(s),()=>{let h=new Ce(s),a,r=!1;return{async start(i,u){if(a=e?await de(e):ie(),r)throw a.dispose(),a=void 0,Error("Audio session cancelled");await h.start(i,u,a.urls[0])},stop(){r=!0,h.stop(),a?.dispose(),a=void 0},setMuted:(i)=>h.setMuted(i),sendDTMF:(i)=>h.sendDTMF(i),setOutputVolume:(i)=>h.setOutputVolume(i),recordSessionEvent:(i)=>h.recordSessionEvent(i),startRingback:(i)=>h.startRingback(i),stopRingback:()=>h.stopRingback()}})}}}var ge=`// Passive receiver only. Both directions share one rendering clock and delay.
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
`;function ut(e){if(e.byteLength<26||e.byteLength>984||e.byteLength%2)throw Error("Invalid listener audio frame");let t=new DataView(e),s=t.getUint32(4,!0);if(t.getUint32(0,!0)!==827085889||s>1)throw Error("Invalid listener audio protocol");let h=Number(t.getBigUint64(8,!0)),a=Number(t.getBigUint64(16,!0));if(!Number.isSafeInteger(h)||!Number.isSafeInteger(a))throw Error("Invalid listener audio timing");let r=new Float32Array((e.byteLength-24)/2);for(let i=0;i<r.length;i++)r[i]=t.getInt16(24+i*2,!0)/32768;return{direction:s,sequence:h,timestampMS:a,frame:r}}function mt(e,t,s,h){if(e.length<1||e.length>480||!Number.isFinite(h)||h<0||!Number.isInteger(t)||t<1||t>4294967295)throw Error("Invalid coaching capture");let a=new ArrayBuffer(24+e.length*2),r=new DataView(a);r.setUint32(0,826496833,!0),r.setUint32(4,t,!0),r.setUint32(8,s>>>0,!0),r.setFloat64(16,h,!0);for(let i=0;i<e.length;i++){let u=Math.max(-1,Math.min(1,e[i]));r.setInt16(24+i*2,u<0?u*32768:u*32767,!0)}return a}function Le(e,t={}){return{create(s){let h,a,r,i,u=!1,p=[],f=!1,d=0,m=!1,y=!1,c,S,o,_,l,M,N,R=!1,G,W,I=0,ee=!1,re=[0,0],ue=[0,0],Y=[void 0,void 0],_e=[new J,new J],n=t.outputVolume??1,g,b=()=>{let C=++d;if(m=!1,y=!1,N?.(C,Error("Coaching cancelled")),M)clearInterval(M);if(M=void 0,l?.port1.close(),l?.port2.close(),l=void 0,S?.disconnect(),S=void 0,o?.disconnect(),o=void 0,_?.disconnect(),_=void 0,c?.getTracks().forEach((U)=>{U.onended=null,U.stop()}),c=void 0,f&&ee&&i?.readyState===WebSocket.OPEN)i.send(JSON.stringify({type:"coach.stop",generation:C}));s.onTalking?.(!1)},q=()=>b(),A=()=>{if(document.visibilityState!=="visible")b()},P=()=>{if(u)return;if(b(),u=!0,g?.(),window.removeEventListener("blur",q),document.removeEventListener("visibilitychange",A),i)i.onmessage=i.onclose=i.onerror=null,i.close();if(a?.disconnect(),r?.disconnect(),h)h.onstatechange=null,h.close().catch(()=>{});for(let C of p)URL.revokeObjectURL(C)},w=(C)=>{if(!u)P(),s.onClose(C)},E=()=>{if(u)throw Error("Listening cancelled")};return{async start(C,U){f=U?.coaching===!0,E();try{if(h=new AudioContext({latencyHint:"interactive"}),h.state==="suspended")await h.resume();if(E(),t.outputDeviceId&&"setSinkId"in h)await h.setSinkId(t.outputDeviceId);E();let v,ce=new URL(e.mcpURL(),location.href);if(ce.origin===location.origin){let k=`/ui/frontend/listener-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(ge))),(Q)=>Q.toString(16).padStart(2,"0")).join("")}.js`,O=await e.get(k,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}});if(E(),O!==ge)throw Error("Listener audio asset integrity mismatch");let K=new URL(`/api/apps/telephony/_install/${e.installId}${k}`,ce);K.searchParams.set("project_id",e.projectId),K.searchParams.set("install_id",String(e.installId)),v=K.href}else{let V=URL.createObjectURL(new Blob([ge],{type:"text/javascript"}));p.push(V),v=V}if(await h.audioWorklet.addModule(v),E(),a=new AudioWorkletNode(h,"telephony-listener",{numberOfInputs:0,outputChannelCount:[t.stereo?2:1],processorOptions:{stereo:t.stereo}}),r=h.createGain(),r.gain.value=n,a.connect(r).connect(h.destination),a.onprocessorerror=()=>w("listener_audio_error"),a.port.onmessage=({data:V})=>{if(V?.type==="listener.diagnostics"&&!u)try{s.onDiagnostics?.({...V,sequence_gaps:[...ue],network_excess_ms:I,network_dropped_ms:[...re]})}catch{}},h.onstatechange=()=>{if(h?.state==="suspended"||h?.state==="interrupted")w("listener_audio_paused")},await new Promise((V,k)=>{let O=!1,K=setTimeout(()=>{Q(Error("Listener connection timed out")),w("listener_network_error")},1e4),Q=(F)=>{if(O)return;O=!0,clearTimeout(K),g=void 0,F?k(F):V()};g=()=>Q(Error("Listening cancelled")),i=new WebSocket(C),i.binaryType="arraybuffer",i.onmessage=({data:F})=>{if(u||!h||!a)return;try{if(typeof F==="string"){let z=JSON.parse(F);if(z.type==="listener.ready"){if(f&&z.coaching!==!0)throw Error("Coaching not authorized");ee=!0,Q(),s.onReady()}else if(z.type==="coach.started")N?.(z.generation);else if(z.type==="coach.rejected")N?.(d,Error(z.detail||"Coaching unavailable"));else if(z.type==="coach.stopped"&&(z.generation===void 0||z.generation===d))b();return}if(!ee||!(F instanceof ArrayBuffer))return;let B=ut(F),H=B.direction;if(Y[H]!==void 0&&B.sequence<Y[H])return;if(Y[H]!==void 0&&B.sequence>Y[H])ue[H]+=B.sequence-Y[H];Y[H]=B.sequence+1;let oe=performance.now()-B.timestampMS;if(W=Math.min(W??oe,oe),I=Math.max(0,oe-W),I>200){re[H]+=B.frame.length*1000/24000;return}G??=h.currentTime*1000+60-B.timestampMS;let Fe=_e[H].process(B.frame,24000,h.sampleRate);a.port.postMessage({direction:H,frame:Fe,playAtMS:B.timestampMS+G},[Fe.buffer])}catch{Q(Error("Invalid listener media")),w("listener_protocol_error")}},i.onerror=()=>{Q(Error("Listener connection failed")),w("listener_network_error")},i.onclose=(F)=>{Q(Error(F.reason||"Listener disconnected")),w(F.reason||"listener_disconnected")}}),E(),f)window.addEventListener("blur",q),document.addEventListener("visibilitychange",A)}catch(v){throw P(),v}},async startTalking(){if(E(),!f||!ee||!h||!i||i.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(m)return;m=!0;let C=++d,U=()=>!u&&m&&C===d;try{let v=await navigator.mediaDevices.getUserMedia({audio:{echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{}}});if(!U()){v.getTracks().forEach((k)=>k.stop());return}if(c=v,c.getAudioTracks().forEach((k)=>{k.onended=()=>b()}),!R){let k=new URL(e.mcpURL(),location.href),O;if(k.origin===location.origin){let Q=`/ui/frontend/worklet-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(Z))),(H)=>H.toString(16).padStart(2,"0")).join("")}.js`;if(await e.get(Q,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==Z)throw Error("Coaching audio asset integrity mismatch");let B=new URL(`/api/apps/telephony/_install/${e.installId}${Q}`,k);B.searchParams.set("project_id",e.projectId),B.searchParams.set("install_id",String(e.installId)),O=B.href}else O=URL.createObjectURL(new Blob([Z],{type:"text/javascript"})),p.push(O);if(!U())return;await h.audioWorklet.addModule(O),R=!0}if(!U())return;if(await new Promise((k,O)=>{let K=setTimeout(()=>{N=void 0,O(Error("Coaching activation timed out"))},3000);N=(Q,F)=>{if(F||Q===C)clearTimeout(K),N=void 0,F?O(F):k()},i.send(JSON.stringify({type:"coach.start",generation:C}))}),!U())return;S=new AudioWorkletNode(h,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:0,highpassFilter:!0}}),o=h.createMediaStreamSource(v),_=h.createGain(),_.gain.value=0,o.connect(S).connect(_).connect(h.destination),l=new MessageChannel;let ce=0,V=new J;l.port1.onmessage=({data:k})=>{if(!U()||!y||k?.type!=="capture"||!(k.frame instanceof Float32Array)||!h||!i)return;if(!Number.isFinite(k.timestamp_ms)||h.currentTime*1000-k.timestamp_ms>150||i.bufferedAmount>2880||i.readyState!==WebSocket.OPEN)return;let O=V.process(k.frame,k.sample_rate,24000);if(O.length>0&&O.length<=480)i.send(mt(O,C,ce++,k.timestamp_ms))},l.port1.start(),S.port.postMessage({type:"transport",port:l.port2},[l.port2]),y=!0,s.onTalking?.(!0),M=setInterval(()=>{if(U()&&i?.readyState===WebSocket.OPEN)i.send(JSON.stringify({type:"coach.keepalive",generation:C}))},1000)}catch(v){if(U())throw b(),v}},stopTalking:b,stop:P,setOutputVolume(C){if(!Number.isFinite(C)||C<0||C>1)throw RangeError("Listener volume must be 0–1");if(n=C,r)r.gain.value=C}}}}}class Te{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;constructor(e,t={}){this.client=e;this.options=t;this.runtime=t.runtime??Le(e.app,t)}getSnapshot=()=>this.snapshot;subscribe=(e)=>{return this.observers.add(e),()=>{this.observers.delete(e)}};update(e){this.snapshot=Object.freeze({...this.snapshot,...e});for(let t of this.observers)try{t(this.snapshot)}catch{}}async listen(e){return this.begin(e,!1)}async coach(e){return this.begin(e,!0)}async begin(e,t){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=t,this.retryUntil=0,this.retryDelay=500;let s=++this.generation;return this.update({state:"connecting",callId:e,detail:void 0,coaching:t,talking:!1}),this.connect(e,s)}async connect(e,t){let s,h=++this.attempt,a=()=>t===this.generation&&h===this.attempt&&!this.disposed;try{if(s=this.coaching?await this.client.coachSession(e):await this.client.listenSession(e),!a()){await this.client.stopListening(s).catch(()=>{});return}this.session=s;let r=this.runtime.create({onReady:()=>{if(a())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0})},onClose:(i)=>{if(a())this.disconnected(i,e,t)},onTalking:(i,u)=>{if(a())this.update({talking:i,detail:u})},onDiagnostics:(i)=>{if(a())try{this.options.onDiagnostics?.(i)}catch{}}});if(this.audio=r,this.lease=new me(s.lease_seconds,()=>this.client.renewListening(s),(i)=>{if(a())this.disconnected(i==="revoked"?"access_revoked":"media_disconnected",e,t,i==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,s.lease_started_ms),await r.start(this.client.listenerMediaURL(s),{coaching:s.coaching===!0}),!a())r.stop()}catch(r){if(!a()||s&&this.session!==s)return;let i=r?.status,u;try{u=JSON.parse(r.body??"{}").code}catch{}let p=u==="call_ended"?"call_ended":i===401||i===403||i===404?"access_revoked":"listener_disconnected";throw this.disconnected(p,e,t,String(r)),r}}cleanup(){this.lease?.stop(),this.lease=void 0;let e=this.audio;this.audio=void 0,e?.stop();let t=this.session;return this.session=void 0,t?this.client.stopListening(t).catch(()=>{}):Promise.resolve()}disconnected(e,t,s,h=e){if(s!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let a=e==="call_ended",r=e==="access_revoked",i=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(e);if(!this.coaching&&!a&&!r&&i&&this.options.reconnect!==!1){if(this.retryUntil||=Date.now()+30000,Date.now()<this.retryUntil){this.update({state:"reconnecting",detail:h,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,s===this.generation&&!this.disposed)this.connect(t,s).catch(()=>{})},this.retryDelay),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:a?"call_ended":r?"access_revoked":"disconnected",detail:h,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(e){this.audio?.setOutputVolume(e)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let e=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1}),await e}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}function nt(e){if(!e)return"placing";if(Oe(e))return"ended";if(e==="ringing")return"ringing";if(e==="answered"||e==="in-progress")return"connected";return"placing"}var X=(e)=>e instanceof Error?e.message:String(e);class Re{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(e,t={}){this.client=e;this.options=t;if(this.runtime=t.audioRuntime??Ve(e.app),this.audioOptions={...he,...t.audio,...t.mediaTransport?{mediaTransport:t.mediaTransport}:{}},j(this.audioOptions),this.interval=t.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}getSnapshot=()=>this.snapshot;subscribe=(e)=>{return this.assertOpen(),this.listeners.add(e),()=>{this.listeners.delete(e)}};update(e){this.snapshot=Object.freeze({...this.snapshot,...e});for(let t of this.listeners)try{t(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(e){if(this.disposed||e!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(e){return!this.disposed&&e===this.generation}finish(e){if(this.current(e))this.update({busy:!1})}cancellable(e,t){let s=this.cancellation.signal;return new Promise((h,a)=>{let r=()=>{s.removeEventListener("abort",r),a(Error("Softphone operation cancelled"))};if(s.addEventListener("abort",r,{once:!0}),e.then(h,a).finally(()=>s.removeEventListener("abort",r)),!this.current(t)||s.aborted)r()})}begin(e){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||e&&this.session)throw Error("Softphone already has an active operation or call");let t=this.invalidate();return this.update({busy:!0,detail:void 0}),t}async dial(e){let t=this.begin(!0),s,h=!1;try{this.assertCurrent(t);let a={to:e.to.trim(),from:e.from?.trim(),timeout_sec:e.timeout_sec,recording:e.recording},r=JSON.stringify(a);if(!this.intent||this.intent.value!==r||e.idempotency_key&&e.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:r,request:{...a,idempotency_key:e.idempotency_key||crypto.randomUUID()}};return await this.cancellable(this.runtime.preflight(this.audioOptions),t),this.assertCurrent(t),s=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(t),h=!0,this.outbound=!0,await this.attachAudio(s,t),s.call_id}catch(a){if(s&&(this.current(t)||!h||this.disposed))await this.recoverStartup(s,t,()=>this.client.hangup(s.call_id));if(this.current(t))this.update({detail:X(a)});throw a}finally{this.finish(t)}}async answer(e,t={}){let s=this.begin(!0),h,a=!1;try{this.assertCurrent(s),h=await this.client.answer(e,t),this.assertCurrent(s),a=!0,this.outbound=!1,await this.attachAudio(h,s)}catch(r){if(h&&(this.current(s)||!a||this.disposed))await this.recoverStartup(h,s,()=>this.client.release(h));if(this.current(s))this.update({detail:X(r)});throw r}finally{this.finish(s)}}async recoverStartup(e,t,s){try{if(await s(),this.current(t))this.clearCall()}catch{if(this.current(t))this.session=e,this.update({callId:e.call_id,audioState:"error"}),this.startPolling()}}async join(e){await this.answer(e,{rejoin:!0})}async attach(e){await this.acquireSession(e,!1)}async takeover(e){await this.acquireSession(e,!0)}async acquireSession(e,t){let s=this.begin(!0);try{await this.cancellable(this.runtime.preflight(this.audioOptions),s),this.assertCurrent(s);let h=await(t?this.client.takeover(e):this.client.attach(e));this.assertCurrent(s),await this.attachAudio(h,s),await this.reconcileAttachedCall(h,s)}catch(h){if(this.current(s))this.update({detail:X(h)});throw h}finally{this.finish(s)}}async reconnect(e){if(!this.session)throw Error("No call to reconnect");let t={...this.audioOptions,...e};j(t);let s=this.begin(!1);this.audioOptions=t;let h=this.session.call_id;try{let a=await this.client.attach(h);this.assertCurrent(s),await this.attachAudio(a,s),await this.reconcileAttachedCall(a,s)}catch(a){if(this.current(s))this.update({detail:X(a)});throw a}finally{this.finish(s)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let e=this.session;if(!e){this.cancellation.abort();return}let t=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let s=(async()=>{try{if(await this.client.hangup(e.call_id),this.current(t))this.clearCall()}catch(h){if(this.current(t))this.update({audioState:"error",detail:X(h)}),this.startPolling();throw h}finally{this.finish(t)}})();this.hangingUp=s;try{await s}finally{if(this.hangingUp===s)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(e){this.assertOpen();let t=this.session;if(!t)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let s=this.generation;this.update({busy:!0,detail:void 0});let h;try{h=this.client[e](t.call_id)}catch(a){if(this.current(s)&&this.session===t)this.update({busy:!1,detail:X(a)});throw a}this.controlPending=h;try{let a=await h;if(this.current(s)&&this.session===t)this.update({holdState:a.hold_state,recordingState:a.recording_state,controlError:a.control_error,capabilities:a.capabilities});return a}catch(a){if(this.current(s)&&this.session===t)this.update({detail:X(a)});throw a}finally{if(this.controlPending===h)this.controlPending=void 0;if(this.current(s)&&this.session===t)this.update({busy:!1})}}configureAudio(e){this.assertOpen();let t={...this.audioOptions,...e};j(t),this.audioOptions=t}setMuted(e){this.assertOpen(),this.audio?.setMuted(e),this.update({muted:e})}setOutputVolume(e){if(this.assertOpen(),!Number.isFinite(e)||e<0||e>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=e,this.audio?.setOutputVolume(e)}sendDTMF(e){if(this.assertOpen(),!/^[0-9*#]+$/.test(e))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(e)}observeCall(e){if(this.disposed||e.id!==this.session?.call_id)return;if(e.direction)this.outbound=e.direction==="outbound";if(Oe(e.status)){this.invalidate(),this.clearCall(e.status),this.update({busy:!1,phase:"ended",termination:e.termination,answeredBy:e.answered_by??this.snapshot.answeredBy,endedAt:e.ended_at});return}this.update({carrierStatus:e.status,phase:nt(e.status),answeredBy:e.answered_by??this.snapshot.answeredBy,holdState:e.hold_state??this.snapshot.holdState,recordingState:e.recording_state??this.snapshot.recordingState,controlError:e.control_error??this.snapshot.controlError,capabilities:e.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let e=this.options.ringback;return typeof e==="object"&&e?e.country:void 0}syncRingback(){let e=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(e&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!e&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(e,t){this.assertCurrent(t),this.stopAudio(),this.session=e,this.update({callId:e.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let s,h=()=>!this.disposed&&s!==void 0&&this.audio===s,a=(i)=>{if(h())try{i()}catch{}},r;try{if(this.assertCurrent(t),s=this.runtime.create({refreshMediaURL:()=>{if(r)return r;if(!h()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let i=this.generation,u=(async()=>{let f=await this.client.attach(e.call_id);if(this.assertCurrent(i),!h())throw Error("Audio connection no longer active");return e=f,this.session=f,this.startLease(f),this.client.mediaURL(f)})();r=u;let p=()=>{if(r===u)r=void 0};return u.then(p,p),u},onSessionEvent:(i)=>a(()=>this.options.onSessionEvent?.(i)),onState:(i,u)=>{if(!h())return;if(i==="ended"&&u==="call.ended"){this.observeCall({id:e.call_id,status:"completed"});return}if(this.update({audioState:i,detail:u}),h()&&(i==="error"||i==="ended")){if(this.stopAudio(),i==="error")this.reconcileFailedAudio(e,this.generation)}},onLevels:(i,u)=>a(()=>this.options.onLevels?.(i,u)),onAudioHealth:(i)=>a(()=>this.options.onAudioHealth?.(i)),onDiagnostics:(i)=>a(()=>this.options.onDiagnostics?.(i)),onNotice:(i)=>a(()=>this.options.onNotice?.(i)),onCallStatus:(i)=>a(()=>this.observeCall({id:i.call_id,status:i.status,direction:i.direction,answered_at:i.answered_at,ended_at:i.ended_at,answered_by:i.answered_by,termination:i.termination,hold_state:i.hold_state,recording_state:i.recording_state,control_error:i.control_error}))}),this.audio=s,this.assertCurrent(t),this.startPolling(),this.startLease(e),s.setMuted(this.snapshot.muted),await this.cancellable(s.start(this.client.mediaURL(e),this.audioOptions),t),this.assertCurrent(t),!h())throw Error(this.snapshot.detail||"Audio connection ended during setup");s.setMuted(this.snapshot.muted),this.syncRingback()}catch(i){if(h())this.stopAudio();if(this.current(t))this.update({audioState:"error",detail:X(i)});throw i}}async reconcileAttachedCall(e,t){try{let s=await this.client.getCall(e.call_id);if(this.current(t)&&this.session===e&&s)this.observeCall(s)}catch{}}async reconcileFailedAudio(e,t){try{let s=await this.client.getCall(e.call_id);if(!this.current(t)||this.session!==e||!s)return;if(s.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(s)}catch{}}startLease(e){if(this.lease?.stop(),!e.lease_seconds)return;this.lease=new me(e.lease_seconds,()=>this.client.renew(e),(t,s)=>{if(this.disposed||this.session!==e)return;this.stopAudio(),this.update({audioState:"error",detail:t==="expired"?"Audio session expired. Reconnect audio.":`Audio authorization ended: ${X(s??t)}`})},(t)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(t);else try{this.options.onSessionEvent?.(t)}catch{}},e.lease_started_ms)}stopAudio(){this.lease?.stop(),this.lease=void 0;let e=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{e?.stopRingback?.()}catch{}}try{e?.stop()}catch{}if(e)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let e=new AbortController;this.polling=e;let t=async()=>{let s=this.session?.call_id;if(!s||e.signal.aborted)return;try{let h=await this.client.getCall(s,e.signal);if(!e.signal.aborted&&h)this.observeCall(h)}catch(h){if(!e.signal.aborted)this.update({detail:`Call status unavailable: ${X(h)}`})}finally{if(!e.signal.aborted&&this.session&&!this.disposed)this.timer=setTimeout(t,this.interval)}};this.timer=setTimeout(t,this.interval)}clearCall(e){this.stopAudio(),this.stopPolling(),this.session=void 0,this.outbound=!1,this.update({callId:void 0,carrierStatus:e,audioState:"idle",muted:!1,detail:void 0,phase:e?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function ft(e,t="en"){if(e?.reason==="ai_inactivity")return t.toLowerCase().startsWith("fr")?"Fin de l’appel : aucune réponse":"Call ended: no response";if(e?.reason==="ai_policy_failure")return t.toLowerCase().startsWith("fr")?"Échec du rappel d’inactivité IA":"AI inactivity reminder failed";if(e?.reason==="time_limit"||e?.reason==="ai_max_duration")return t.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return e?.reason?.replaceAll("_"," ")??""}class De extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function pt(e){return e.direction==="inbound"&&e.status==="pending"&&e.answerable!==!1&&!e.routing_waiting&&(e.peer_kind==="human"||Boolean(e.ring_offers?.some((t)=>t.kind==="browser")))}var Oe=(e)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(e);function T(e){if(!e||/[\s/\\?#]/.test(e))throw Error("Invalid call ID");return encodeURIComponent(e)}class Se{app;options;listMicrophones=He;createMicrophonePreview=(e)=>ve(e,this.app);constructor(e,t={}){this.app=e;this.options=t;if(e.name!=="telephony"||!e.projectId||!e.installId)throw Error("Telephony requires an explicit project and installation")}path(e){if(!this.options.authProvider)return e;return`/user${e}${e.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(e){let t=await this.app.get(this.path("/calls"),{signal:e});if(!Array.isArray(t?.calls)||t.calls.some((s)=>!s||typeof s.id!=="string"||typeof s.status!=="string"))throw Error("Invalid Telephony calls response");return t.calls}async getCall(e,t){let s=await this.app.get(this.path(`/calls?call_id=${T(e)}`),{signal:t});if(!Array.isArray(s?.calls))throw Error("Invalid Telephony call response");return s.calls.find((h)=>h.id===e)}incomingCalls(e){return e.filter(pt)}watchCalls(e,t={}){let s=t.intervalMs??2000;if(!Number.isFinite(s)||s<100)throw Error("Call watch interval must be at least 100 ms");let h=new AbortController,a,r,i=!1,u=!1,p=new Set,f=()=>{clearTimeout(a),h.abort(),r?.close(),t.signal?.removeEventListener("abort",f)},d=(y)=>{try{t.onError?.(y)}catch{f()}};if(t.signal?.addEventListener("abort",f,{once:!0}),t.signal?.aborted)f();let m=async(y)=>{if(h.signal.aborted)return;if(i){u=!0;return}clearTimeout(a),i=!0;let c=performance.now();try{let S=await this.listCalls(h.signal);if(!h.signal.aborted){e(S);for(let o of S){if(o.answerable!==!0||o.status!=="pending")continue;for(let _ of o.ring_offers??[]){if(_.kind!=="browser"||!_.id||p.has(_.id))continue;p.add(_.id),this.acknowledgeOffer(o.id,_.id).catch((l)=>{if(![404,405].includes(l?.status??0))p.delete(_.id)})}}try{t.onTiming?.({trigger:y,fetchMs:performance.now()-c})}catch{}}}catch(S){if(!h.signal.aborted){let o=S?.status;try{t.onFailure?.({trigger:y,fetchMs:performance.now()-c,status:typeof o==="number"?o:void 0,error:S})}catch{}d(S)}}finally{if(i=!1,!h.signal.aborted)if(u)u=!1,m("push");else a=setTimeout(()=>void m("poll"),s)}};if(m("poll"),!h.signal.aborted&&t.push!==!1&&typeof this.app.subscribe==="function")try{r=this.app.subscribe(this.path("/calls/events"),(y)=>{if(y.type==="calls.changed")m("push");else if(y.type==="access.revoked")r?.close(),m("push")},{transport:"fetch",signal:h.signal,reconnectDelayMs:250,onError:(y)=>{let c=y?.status;if(c===404||c===405||c===501)r?.close()}})}catch{}return{close:f}}async place(e){if(!/^\+[1-9]\d{7,14}$/.test(e.to)||!e.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let t=L();return this.session(await this.app.post(this.path("/softphone/place"),e),void 0,"media",t)}async answer(e,t={}){let s=L();try{return this.session(await this.app.post(this.path(`/softphone/answer/${T(e)}`),t),e,"media",s)}catch(h){let a=h;if(a.status===409&&typeof a.body==="string"){let r;try{r=JSON.parse(a.body)}catch{}if(r?.code==="offer_expired")throw new De}throw h}}async acknowledgeOffer(e,t){await this.app.post(this.path(`/softphone/offer/ack/${T(e)}`),{offer_id:t})}async declineOffer(e,t){await this.app.post(this.path(`/softphone/offer/decline/${T(e)}`),{offer_id:t})}async attach(e){let t=L();return this.session(await this.app.post(this.path(`/softphone/attach/${T(e)}`),{}),e,"media",t)}async takeover(e){let t=L();return this.session(await this.app.post(this.path(`/softphone/takeover/${T(e)}`),{}),e,"media",t)}createCallListener(e={}){return new Te(this,e)}async listenSession(e){let t=L();return this.session(await this.app.post(this.path(`/softphone/listen/${T(e)}`),{}),e,"listen-media",t)}async coachSession(e){let t=L(),s=this.session(await this.app.post(this.path(`/softphone/coach/${T(e)}`),{}),e,"listen-media",t);if(s.coaching!==!0)throw Error("Invalid coaching session");return s}async renewListening(e){return this.app.post(this.path(`/softphone/${e.coaching?"coach-renew":"listen-renew"}/${T(e.call_id)}`),{session_token:e.session_token})}async stopListening(e){await this.app.post(this.path(`/softphone/${e.coaching?"coach-stop":"listen-stop"}/${T(e.call_id)}`),{session_token:e.session_token})}async listenerAudit(e){return this.app.get(this.path(`/softphone/listen-audit/${T(e)}`))}outboundNumbers(e={}){return this.app.get(this.path(e.fresh?"/softphone/numbers?fresh=true":"/softphone/numbers"))}async renew(e){return this.app.post(this.path(`/softphone/renew/${T(e.call_id)}`),{session_token:e.session_token})}async release(e){if(!e.session_token)throw Error("Answer session has no release token");await this.app.post(this.path(`/softphone/release/${T(e.call_id)}`),{session_token:e.session_token})}async hangup(e){await this.app.post(this.path(`/calls/${T(e)}/hangup`),{})}hold(e){return this.app.post(this.path(`/calls/${T(e)}/hold`),{})}resume(e){return this.app.post(this.path(`/calls/${T(e)}/resume`),{})}pauseRecording(e){return this.app.post(this.path(`/calls/${T(e)}/pause-recording`),{})}resumeRecording(e){return this.app.post(this.path(`/calls/${T(e)}/resume-recording`),{})}createSoftphone(e={}){return new Re(this,e)}mediaURL(e){return this.resolveMediaURL(e,"media")}listenerMediaURL(e){return this.resolveMediaURL(e,"listen-media")}resolveMediaURL(e,t){let s=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),h=new URL(e.media_url,s),a=`/api/apps/telephony/_install/${this.app.installId}/softphone/${t}/${T(e.call_id)}/`;if(h.origin!==s.origin||!["http:","https:"].includes(h.protocol)||h.username||h.password||h.search||h.hash||!h.pathname.startsWith(a)||!/^[A-Za-z0-9_-]+$/.test(h.pathname.slice(a.length)))throw Error("Invalid Telephony media endpoint");if(t==="listen-media"&&h.pathname.slice(a.length)!==e.session_token)throw Error("Listener credential mismatch");return h.protocol=h.protocol==="https:"?"wss:":"ws:",h.href}session(e,t,s="media",h=L()){let a=e;if(!a||typeof a.call_id!=="string"||typeof a.media_url!=="string"||t&&a.call_id!==t||a.session_token!==void 0&&typeof a.session_token!=="string")throw Error("Invalid Telephony session response");if(a.lease_seconds!==void 0&&(!Number.isFinite(a.lease_seconds)||a.lease_seconds<10||a.lease_seconds>3600||!a.session_token))throw Error("Invalid media lease");if(s==="listen-media"&&(!a.session_token||a.lease_seconds===void 0))throw Error("Invalid listener lease");return T(a.call_id),this.resolveMediaURL(a,s),{...a,lease_started_ms:h}}}var qs=be({app:"telephony",create:({app:e})=>new Se(e)});function Ps({app:e},t){return new Se(e,t)}export{Ps as createClient,ft as callTerminationLabel};
