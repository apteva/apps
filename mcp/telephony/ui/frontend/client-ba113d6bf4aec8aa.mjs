function Gt(t){let e=globalThis.crypto?.randomUUID?.()??`${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;return`${t}-${e}`}function ct(t){return{recovery_id:Gt("recovery"),attempt_id:Gt("attempt"),initiating_action:t}}function pt(t){return typeof t==="string"&&t.length<=80&&/^(browser|session|recovery|attempt)-[a-f0-9-]{1,64}$/.test(t)?t:void 0}function lt(t){let e=(m,u)=>typeof m==="string"&&m.length<=u&&/^[a-zA-Z0-9_-]*$/.test(m)?m:void 0,h=["WebSocket transport error","WebRTC signaling error","Audio worker failed","WebRTC signaling unavailable"],s=["attach","takeover","automatic_retry","manual_reconnect","audio_device_change","component_recreation"],a=(m,u)=>typeof m==="number"&&Number.isFinite(m)?Math.max(0,Math.min(u,m)):void 0;return{timestamp:Number.isFinite(Date.parse(t.timestamp))?new Date(t.timestamp).toISOString():new Date().toISOString(),action:e(t.action,40)??"diagnostic",outcome:e(t.outcome,40)??"unknown",code:e(t.code,80),detail:t.detail?h.includes(t.detail)?t.detail:"detail_redacted":void 0,recovery_id:pt(t.recovery_id),attempt_id:pt(t.attempt_id),shared_attempt_id:pt(t.shared_attempt_id),session_id:pt(t.session_id),previous_session_id:pt(t.previous_session_id),initiating_action:s.includes(t.initiating_action)?t.initiating_action:void 0,status:a(t.status,599),remaining_ms:a(t.remaining_ms,3600000),was_clean:typeof t.was_clean==="boolean"?t.was_clean:void 0,duration_ms:a(t.duration_ms,86400000)}}var at=()=>performance.now();function gt(t){let e=t,h=e?.status,s;try{let i=JSON.parse(e?.body??"{}");if(typeof i.code==="string")s=i.code}catch{}let a=s==="media_lease_expired";return{status:h,code:s,expired:a,denied:!a&&[401,403,404,410].includes(h)}}class wt{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(t,e,h,s,a=at(),i={now:at,setTimeout:(m,u)=>setTimeout(m,u),clearTimeout:(m)=>clearTimeout(m)}){this.seconds=t;this.renew=e;this.ended=h;this.event=s;this.clock=i;this.deadline=a+t*1000-1000,this.watchExpiry(),this.schedule(Math.min(t*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(t,e){let{status:h,code:s}=gt(e);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:t,status:h,code:s,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(t){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,t))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let t=this.clock.now();try{let e=await Promise.race([this.renew(),new Promise((h,s)=>{this.requestTimer=this.clock.setTimeout(()=>s(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(e?.lease_seconds!==void 0){if(!Number.isFinite(e.lease_seconds)||e.lease_seconds<10||e.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=e.lease_seconds}this.deadline=t+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(e){if(this.stopped)return;let h=gt(e);if(h.denied||h.expired){this.finish(h.expired?"expired":"revoked",e);return}this.report("retrying",e),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(t,e){if(this.stopped)return;this.report(t,e),this.stop(),this.ended(t,e)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function Wt(t){return t}var ne=["rtt_ms","queue_ms","target_ms","buffered_bytes","underruns","dropped_ms","capture_sequence_gaps","playback_sequence_gaps","main_thread_max_pause_ms","worker_max_tick_gap_ms","capture_sent_ms","capture_muted_ms","capture_dropped_ms","playback_ingress_ms","playback_transport_dropped_ms","playback_source_dropped_ms","clock_uncertainty_ms","clock_sample_age_ms","stats_errors","stats_duration_ms","send_bitrate_bps","receive_bitrate_bps","receive_gap_ms","capture_age_ms","transit_ms","delivery_excess_ms","server_queue_ms","receiver_concealed_delta_ms","receiver_silent_concealed_delta_ms","receiver_concealed_window_ms","requested_playback_target_ms","requested_upload_bitrate_bps","receiver_ssrc","receiver_loss_delta","receiver_loss_window_ms","remote_receiver_ssrc","remote_receiver_loss_delta","remote_receiver_loss_window_ms","receiver_bytesReceived","receiver_packetsReceived","receiver_packetsLost","receiver_jitter","receiver_packetsDiscarded","receiver_concealedSamples","receiver_silentConcealedSamples","receiver_concealmentEvents","receiver_insertedSamplesForDeceleration","receiver_removedSamplesForAcceleration","receiver_totalSamplesReceived","receiver_audioLevel","receiver_totalAudioEnergy","receiver_totalSamplesDuration","receiver_nackCount","receiver_fecPacketsReceived","receiver_fecPacketsDiscarded","receiver_jitter_buffer_interval_ms","receiver_jitter_target_interval_ms","receiver_jitter_minimum_interval_ms","receiver_processing_interval_ms","receiver_last_packet_age_ms","sender_bytesSent","sender_packetsSent","sender_headerBytesSent","sender_retransmittedPacketsSent","sender_retransmittedBytesSent","sender_nackCount","sender_targetBitrate","sender_send_delay_interval_ms","remote_receiver_packetsLost","remote_receiver_fractionLost","remote_receiver_jitter","remote_receiver_roundTripTime","remote_receiver_totalRoundTripTime","remote_receiver_roundTripTimeMeasurements","remote_sender_packetsSent","remote_sender_bytesSent","pair_availableOutgoingBitrate","pair_availableIncomingBitrate","pair_currentRoundTripTime","pair_totalRoundTripTime","pair_bytesSent","pair_bytesReceived","pair_requestsSent","pair_requestsReceived","pair_responsesSent","pair_responsesReceived","pair_consentRequestsSent","path_revision","codec_clock_rate","codec_channels"],Me={connection:["connected","reconnecting","closed"],context:["running","suspended","interrupted","closed"],muted:["true","false"],device_muted:["true","false"],track:["live","ended"],ice:["new","checking","connected","completed","disconnected","failed","closed"],dtls:["new","connecting","connected","closed","failed"],pair:["frozen","waiting","in-progress","failed","succeeded"],local_candidate:["host","srflx","prflx","relay"],remote_candidate:["host","srflx","prflx","relay"],protocol:["udp","tcp"],relay_protocol:["udp","tcp","tls"],codec:["audio/opus","audio/PCMU","audio/PCMA","pcm16"]};function Vt(t){let e={};for(let h of ne)if(typeof t[h]==="number"&&Number.isFinite(t[h]))e[h]=Math.max(0,Math.min(t[h],1000000000000));return e}function $t(t){let e={};for(let[h,s]of Object.entries(Me))if(typeof t[h]==="string"&&s.includes(t[h]))e[h]=t[h];return e}class Pt{transport;serial=0;lastEmit=-1/0;previousAt;previous;incident;history=[];pending=[];counters={};constructor(t){this.transport=t}observe(t,e,h=performance.now(),s=new Date().toISOString(),a){if(!Number.isFinite(h)||!Number.isFinite(Date.parse(s)))return;if(this.previousAt!==void 0&&h<this.previousAt)this.lastEmit=-1/0,this.previous=void 0,this.counters={},this.incident=void 0;let i=Vt(t),m=$t(e),u=!1;for(let _ of["underruns","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealmentEvents","path_revision","stats_errors"])if(i[_]!==void 0){if(this.counters[_]!==void 0&&i[_]>this.counters[_])u=!0;this.counters[_]=i[_]}let f={id:`s${++this.serial}`,timestamp:s,transport:this.transport,reason:u?"incident":"periodic",window_ms:Math.min(60000,Math.max(0,typeof a==="number"&&Number.isFinite(a)?a:this.previousAt===void 0?0:h-this.previousAt)),metrics:i,states:m};if(this.previousAt=h,u&&!this.incident)this.incident={sample:f,context:this.previous};if(h-this.lastEmit>=5000){if(this.incident){if(this.incident.context)this.enqueue({...this.incident.context,reason:"incident_context"});this.enqueue(this.incident.sample),this.incident=void 0}else this.enqueue(f);this.lastEmit=h}this.previous=f}enqueue(t){if(this.history.push(t),this.history.length>24)this.history.shift();if(this.pending.push(t),this.pending.length>8)this.pending.shift()}recent(){return this.history.map((t)=>({...t,metrics:{...t.metrics},states:{...t.states}}))}drain(){return this.pending.splice(0,2)}}function ye(t){let e={...t,metrics:{},states:{},part_index:0,part_count:16},h=[],s=e;for(let a of["metrics","states"])for(let[i,m]of Object.entries(t[a])){let u={...s,[a]:{...s[a],[i]:m}};if(JSON.stringify(u).length>768)h.push(s),s={...e,metrics:{},states:{},[a]:{[i]:m}};else s=u}if(h.push(s),h.length>16)return[];return h.map((a,i)=>({...a,part_index:i,part_count:h.length}))}class Et{send;sendReport;auxiliary=[];skippedAuxiliary=0;skippedSamples=0;auxiliaryTurn=!1;pending=[];timer;report;constructor(t,e){this.send=t;this.sendReport=e}enqueueReport(t){this.report=t,this.start()}enqueueAuxiliary(t){if(this.auxiliary.push(t),this.auxiliary.length>32)this.auxiliary.shift(),this.skippedAuxiliary++;this.start()}start(){if(!this.timer)this.timer=setInterval(()=>this.tick(),250)}enqueue(t){for(let e of t){let h=ye(e);while(this.pending.length+h.length>32){let s=this.pending.at(-1);if(!s||s.id===this.pending[0]?.id)break;this.pending=this.pending.filter((a)=>a.id!==s.id),this.skippedSamples++}if(this.pending.length+h.length<=32)this.pending.push(...h);else this.skippedSamples++}if(this.pending.length)this.start()}tick(){try{if(this.report!==void 0&&this.sendReport){if(this.sendReport(this.report))this.report=void 0}else if(this.auxiliary.length&&this.sendReport&&(!this.pending.length||this.auxiliaryTurn)){if(this.sendReport(this.auxiliary[0]))this.auxiliary.shift(),this.auxiliaryTurn=!1}else if(this.pending.length&&this.send(this.pending[0]))this.pending.shift(),this.auxiliaryTurn=!0}catch{}if(!this.pending.length&&!this.auxiliary.length&&this.report===void 0)clearInterval(this.timer),this.timer=void 0}stop(){clearInterval(this.timer),this.timer=void 0,this.pending=[],this.auxiliary=[],this.report=void 0}}class Ct{emit;now;timestamp;lastTick;suspendedAt;contextState;environmentCleanup=[];counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(t,e=()=>performance.now(),h=()=>new Date().toISOString()){this.emit=t;this.now=e;this.timestamp=h}observeEnvironment(){this.stopEnvironment();let t=(e,h,s)=>{if(!e)return;try{e.addEventListener(h,s),this.environmentCleanup.push(()=>e.removeEventListener(h,s))}catch{}};try{if(typeof document<"u"){t(document,"visibilitychange",()=>this.report("tab_visibility",document.visibilityState));for(let e of["freeze","resume"])t(document,e,()=>this.report("page_lifecycle",e));this.report("tab_visibility",document.visibilityState)}if(typeof window<"u")for(let e of["pagehide","pageshow"])t(window,e,()=>this.report("page_lifecycle",e));if(typeof navigator<"u")t(navigator.mediaDevices,"devicechange",()=>this.report("microphone","devices_changed"))}catch{}}stopEnvironment(){for(let t of this.environmentCleanup.splice(0))try{t()}catch{}}tick(){let t=this.now();if(this.lastTick!==void 0){let e=Math.max(0,t-this.lastTick-1000);if(e>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,e),this.report("main_thread","scheduling_gap",e)}this.lastTick=t}context(t){if(t===this.contextState)return;this.contextState=t;let e=this.now();if(t==="suspended"||t==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=e,this.counters.audio_context_suspend_count++;this.report("audio_context",t)}else{let h;if(this.suspendedAt!==void 0)h=Math.max(0,e-this.suspendedAt),this.counters.audio_context_suspended_ms+=h,this.suspendedAt=void 0;this.report("audio_context",t,h)}}report(t,e,h){try{this.emit({timestamp:this.timestamp(),action:t,outcome:e,duration_ms:h===void 0?void 0:Math.round(h)})}catch{}}}var ee=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function Qt(t){let e=(t??"FR").trim().toUpperCase();return ee[e]??ee.FR}function Se(t,e){let h=[];if(!(t.cadence.reduce((i,m)=>i+m,0)>0)||!(e>0))return h;let a=0;while(a<e)for(let i=0;i<t.cadence.length;i+=2){let m=t.cadence[i]??0,u=t.cadence[i+1]??0;if(a>=e)break;if(m>0)h.push({start:a,end:Math.min(a+m,e)});a+=m+u}return h}function Bt(t,e,h,s=0.12){let a=t.createGain();a.gain.value=0,a.connect(e);let i=s/Math.max(1,h.frequencies.length),m=h.frequencies.map((S)=>{let d=t.createOscillator();return d.type="sine",d.frequency.value=S,d.connect(a),d.start(),d}),u=t.currentTime+0.05,f=0,_=0.01,y=()=>{let S=t.currentTime-u+10;if(S<=f)return;for(let d of Se(h,S)){if(d.end<=f)continue;let b=u+Math.max(d.start,f),g=u+d.end;a.gain.setValueAtTime(0,b),a.gain.linearRampToValueAtTime(i,b+_),a.gain.setValueAtTime(i,Math.max(b+_,g-_)),a.gain.linearRampToValueAtTime(0,g)}f=S};y();let r=setInterval(y,4000),p=!1;return()=>{if(p)return;p=!0,clearInterval(r);try{a.gain.cancelScheduledValues(0)}catch{}a.gain.value=0;for(let S of m){try{S.stop()}catch{}S.disconnect()}a.disconnect()}}var mt=24000,xt=60;async function se(t,e){try{await t.audioWorklet.addModule(e)}catch(h){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:h})}}function pe(t){let e=new Int16Array(t.length);for(let h=0;h<t.length;h++){let s=Math.max(-1,Math.min(1,t[h]));e[h]=s<0?s*32768:s*32767}return e.buffer}class _t{history=new Float32Array(64);phase=0;process(t,e,h){if(e===h)return t;let s=new Float32Array(64+t.length);s.set(this.history),s.set(t,64);let a=e/h,i=Math.min(1,h/e)*0.9,m=[],u=this.phase;for(;u<t.length;u+=a){let f=u+32,_=0,y=0;for(let r=Math.ceil(f-32);r<=Math.floor(f+32);r++){let p=r-f,d=(Math.abs(p)<0.000000001?i:Math.sin(Math.PI*i*p)/(Math.PI*p))*(0.5+0.5*Math.cos(Math.PI*p/32));if(r>=0&&r<s.length)_+=s[r]*d,y+=d}m.push(y?_/y:0)}return this.phase=u-t.length,this.history=s.slice(-64),new Float32Array(m)}}function ge(t){let e=0;for(let h=0;h<t.length;h++)e+=t[h]*t[h];return Math.sqrt(e/Math.max(1,t.length))}function he(t,e){let h=new Map(t.map((s)=>[s.id,s]));for(let s of e){let a=h.get(s.id);if(!a||(!a.complete||s.complete)&&(!a.ended_at||!!s.ended_at)&&s.duration_ms>=a.duration_ms)h.set(s.id,{...s})}return[...h.values()].sort((s,a)=>s.started_at.localeCompare(a.started_at)).slice(-100)}function le(t,e){return t.map((h)=>h.ended_at?h:{...h,ended_at:h.observed_until,end_reason:e,complete:!1})}var o={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function D(t){if(t.mediaTransport!==void 0&&!["websocket","webrtc","auto"].includes(t.mediaTransport))throw RangeError("Unsupported softphone media transport");if(t.webrtcFec!==void 0&&typeof t.webrtcFec!=="boolean")throw RangeError("webrtcFec must be boolean");let e=t.playbackTargetMs??xt,h=t.playbackMinMs??Math.min(xt,e),s=t.playbackMaxMs??280;for(let a of[e,h,s])if(!Number.isFinite(a)||a<40||a>280)throw RangeError("Playback buffers must be between 40 and 280 ms");if(h>e||e>s)throw RangeError("Playback buffers require min <= target <= max");if(t.playbackAdaptive!==void 0&&typeof t.playbackAdaptive!=="boolean")throw RangeError("playbackAdaptive must be boolean");return{initialTargetMs:e,minTargetMs:h,maxTargetMs:s,hardMaxMs:320,adaptiveReserve:t.playbackAdaptive!==!1}}function it(t){return{...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{},echoCancellation:t.echoCancellation,noiseSuppression:t.noiseSuppression,autoGainControl:t.autoGainControl}}function ae(t){let e=t.getSettings();return{deviceLabel:t.label||"Default microphone",sampleRate:typeof e.sampleRate==="number"?e.sampleRate:null,channelCount:typeof e.channelCount==="number"?e.channelCount:null,echoCancellation:typeof e.echoCancellation==="boolean"?e.echoCancellation:null,noiseSuppression:typeof e.noiseSuppression==="boolean"?e.noiseSuppression:null,autoGainControl:typeof e.autoGainControl==="boolean"?e.autoGainControl:null}}function qt(t){if(!Number.isFinite(t)||t<=0)return null;return 20*Math.log10(t)}function ce(t,e,h){let s=new ArrayBuffer(44+e*2),a=new DataView(s),i=(u,f)=>{for(let _=0;_<f.length;_++)a.setUint8(u+_,f.charCodeAt(_))};i(0,"RIFF"),a.setUint32(4,36+e*2,!0),i(8,"WAVE"),i(12,"fmt "),a.setUint32(16,16,!0),a.setUint16(20,1,!0),a.setUint16(22,1,!0),a.setUint32(24,h,!0),a.setUint32(28,h*2,!0),a.setUint16(32,2,!0),a.setUint16(34,16,!0),i(36,"data"),a.setUint32(40,e*2,!0);let m=44;for(let u of t)for(let f=0;f<u.length;f++)a.setInt16(m,u[f],!0),m+=2;return new Blob([s],{type:"audio/wav"})}class Lt{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new _t;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(t,e=!0){this.onLevel=t;this.recordAudio=e}async start(t,e){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:it(e)}),this.ensureOpen();let h=this.stream.getAudioTracks()[0];if(!h)throw Error("No microphone audio track was returned.");this.settings=ae(h);try{this.ctx=new AudioContext({sampleRate:mt,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await se(this.ctx,t),this.ensureOpen();let s=this.ctx.sampleRate,a=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:e.inputGainDB,highpassFilter:e.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(i)=>{if(this.stopped)return;if(!(i.data instanceof Float32Array)){if(i.data.type==="capture.stats")this.peak=Math.max(this.peak,i.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,i.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,i.data.limiter_reduction_db??0);return}let m=i.data;if(s!==mt)m=this.resampler.process(m,s,mt);let u=ge(m);if(this.onLevel?.(u),this.recordAudio)this.frames.push(new Int16Array(pe(m)));if(this.samples+=m.length,u>=0.005){for(let f=0;f<m.length;f++)this.activeSquares+=m[f]*m[f];this.activeSamples+=m.length}},a.connect(this.capture),this.settings}catch(h){throw await this.release(),h}}async stop(){this.stopped=!0;let t=this.settings,e=this.frames,h=this.samples,s=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,a=this.peak;if(await this.release(),!t||h===0)throw Error("No microphone audio was captured.");return{audio:ce(e,h,mt),durationMs:Math.round(h*1000/mt),sampleRate:mt,activeRmsDbfs:qt(s),peakDbfs:qt(a),postPeakDbfs:qt(this.postPeak||a),limiterReductionDb:this.limiterReductionDB,settings:t}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let t of this.stream?.getTracks()??[])t.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class Xt{callbacks;clientEpoch=crypto.randomUUID();transportTelemetry=new Pt("websocket");transportSender=new Et((t)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[t]}})}),!0},(t)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify(t)}),!0});worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new Ct((t)=>this.recordSessionEvent(t));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;playbackHeld=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={mediaTransport:"websocket",codec:"pcm16",rttMs:null,queueMs:0,targetMs:xt,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:mt,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:o.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t={}){this.callbacks=t}get isMuted(){return this.muted}async start(t,e,h,s=o){let a=D(s);this.diagnostics.targetMs=a.initialTargetMs,this.callbacks.onState?.("connecting"),this.runtimeTelemetry.observeEnvironment(),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{this.stream=await navigator.mediaDevices.getUserMedia({audio:it(s)}),this.ensureOpen();let i=this.stream.getAudioTracks()[0];if(!i)throw Error("No microphone audio track was returned.");if(i.readyState==="ended")throw Error("Microphone disconnected before audio setup.");i.onmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser.")},i.onunmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),this.callbacks.onNotice?.("Microphone input restored.")},i.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let m=ae(i);this.diagnostics={...this.diagnostics,microphoneSampleRate:m.sampleRate??0,microphoneChannelCount:m.channelCount??0,echoCancellation:m.echoCancellation,noiseSuppression:m.noiseSuppression,autoGainControl:m.autoGainControl,micInputGainDb:s.inputGainDB};try{this.ctx=new AudioContext({sampleRate:mt,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await this.ctx.resume();if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),s.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(s.outputDeviceId),this.ensureOpen();await se(this.ctx,e),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let u=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:{...a,telemetryEpoch:this.clientEpoch,telemetryEnabled:!1}}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),u.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=s.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await this.openWorker(t,h),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.setPlaybackObservation(this.ctx?.state==="running"&&this.microphoneTransportReady&&!this.playbackHeld,"audio_context_paused"),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running"&&this.microphoneTransportReady)this.callbacks.onState?.("live")},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(i){if(this.closed)this.teardown();if(!this.closed)this.fail(i instanceof Error?i.message:"browser audio setup failed");throw i}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){if(await this.ctx?.resume(),this.microphoneTransportReady)this.callbacks.onState?.("live")}setOutputVolume(t){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,t))}sendDTMF(t){if(/^[0-9*#]+$/.test(t))this.sendText(JSON.stringify({type:"dtmf",digits:t}))}startRingback(t){if(this.closed||!this.ctx||this.ringback)return;this.ringback=Bt(this.ctx,this.ctx.destination,Qt(t))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(t)=>{let e=t.data;if(e?.type!=="capture.stats")return;this.micLevel=this.muted?0:e.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:qt(e.active_rms??0),micPeakDbfs:qt(e.pre_peak??0),micPostPeakDbfs:qt(e.post_peak??0),micInputGainDb:e.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:e.limiter_reduction_db??0}},this.playback.port.onmessage=(t)=>{let e=t.data;if(e?.type==="playback.underrun"){this.diagnostics.underrunEvents=he(this.diagnostics.underrunEvents??[],[e.event]),this.diagnostics.underrunDurationMs=e.underrun_ms,this.diagnostics.underruns=e.underruns;return}if(e?.type!=="stats")return;this.diagnostics.playbackEvents=e.playback_events,this.playbackTiming={reserve_expanded_ms:e.reserve_expanded_ms,reserve_compressed_ms:e.reserve_compressed_ms,reserve_adjustments:e.reserve_adjustments,reserve_match_rejections:e.reserve_match_rejections,played_ms:e.played_ms,max_residence_ms:e.max_residence_ms,drop_totals_ms:e.drop_totals_ms,coaching:{played_ms:e.whisper_played_ms,dropped_ms:e.whisper_dropped_ms,max_queue_ms:e.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,e.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:e.whisper_played_ms??0,coachingDroppedMs:e.whisper_dropped_ms??0,coachingMaxQueueMs:e.whisper_max_queue_ms??0,queueMs:e.queue_ms??0,targetMs:e.target_ms??xt,underruns:e.underruns??0,droppedMs:e.dropped_ms??0,underrunDurationMs:e.underrun_ms??0,underrunEvents:he(this.diagnostics.underrunEvents??[],e.underrun_events??[]),maxQueueMs:e.max_queue_ms??0,playbackSequenceGaps:e.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,e.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(t,e){return new Promise((h,s)=>{let a=new Worker(e);this.worker=a;let i=new MessageChannel,m=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:i.port1},[i.port1]),this.playback?.port.postMessage({type:"transport",port:m.port1},[m.port1]);let u=!1,f=setTimeout(()=>{if(u)return;u=!0,s(Error("audio connection timed out"))},1e4),_=(y)=>{if(u)return;if(u=!0,clearTimeout(f),y)s(y);else h()};this.cancelWorkerStart=()=>_(Error("audio session closed")),a.onmessage=(y)=>{if(this.closed){_(Error("audio session closed"));return}let r=y.data;if(r?.type==="socket.open")this.reportedPlaybackIDs.clear(),this.reportedCaptureQueueIDs.clear(),this.opened=!0,this.mediaSocketConnected=!0,this.startRTTProbe(),_();else if(r?.type==="runtime.event")this.recordSessionEvent(r.event);else if(r?.type==="socket.message")this.handleControl(r.data);else if(r?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL(r.recovery).then((p)=>{if(!this.closed&&this.worker===a)a.postMessage({type:"socket.credentials",id:r.id,mediaURL:p})},(p)=>{if(this.closed||this.worker!==a)return;let S=gt(p);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:S.denied?"revoked":"retrying",...r.recovery,status:S.status,code:S.code}),a.postMessage({type:"socket.credentials",id:r.id,denied:S.denied})})}else if(r?.type==="socket.error")this.recordSessionEvent({timestamp:r.timestamp,action:"websocket",outcome:"transport_error"});else if(r?.type==="socket.close")if(this.recordSessionEvent({timestamp:r.timestamp,action:"websocket",outcome:"closed",code:String(r.code),detail:r.reason,was_clean:r.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"transport_disconnected"),this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else _(Error("audio connection closed before it was ready"));else if(r?.type==="socket.failed")this.mediaSocketConnected=!1,_(Error(r.detail||"audio connection lost")),this.fail(r.detail||"audio connection lost");else if(r?.type==="transport.drop"&&r.event)this.diagnostics.dropEvents=this.mergeDropEvents(r.event);else if(r?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=r.buffered_bytes??0,this.diagnostics.captureQueueEvents=r.capture_queue_events,this.transportTiming=r.timing??{},r.observation)this.transportTelemetry.observe({...this.transportTiming,...r.observation,rtt_ms:r.timing?.rtt_ms,queue_ms:this.diagnostics.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:r.buffered_bytes,underruns:this.diagnostics.underruns,dropped_ms:this.diagnostics.droppedMs,capture_sequence_gaps:this.diagnostics.captureSequenceGaps,playback_sequence_gaps:this.diagnostics.playbackSequenceGaps,main_thread_max_pause_ms:this.runtimeTelemetry.counters.main_thread_max_pause_ms},{connection:this.mediaSocketConnected?"connected":"reconnecting",context:this.ctx?.state,muted:String(this.muted),device_muted:String(this.stream?.getAudioTracks()[0]?.muted),track:this.stream?.getAudioTracks()[0]?.readyState,codec:"pcm16"},performance.now(),r.observation?.timestamp,r.observation?.window_ms);if(this.diagnostics.transportSamples=this.transportTelemetry.recent(),typeof r.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(r.timing.rtt_ms)}},a.onerror=(y)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:y.message?.slice(0,160)}),_(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},a.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:t,contextRate:this.ctx?.sampleRate??mt,muted:this.muted,capturePort:i.port2,playbackPort:m.port2},[i.port2,m.port2])})}mergeDropEvents(t,e){if(t)this.workerDropEvents=[...this.workerDropEvents,t].slice(-100);if(e)this.playbackDropEvents=e.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((h,s)=>h.timestamp.localeCompare(s.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;shutdownIntent="session_cleanup";reportedPlaybackIDs=new Set;reportedCaptureQueueIDs=new Set;handleControl(t){if(this.closed)return;try{let e=JSON.parse(t);if(e.type==="dtmf.error"||e.type==="dtmf.sent")this.callbacks.onNotice?.(e.type==="dtmf.sent"?"Keypad tone sent":e.detail||"Keypad tone failed");else if(e.type==="pong"){if(this.diagnostics.captureSequenceGaps=e.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof e.nonce==="number"&&e.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-e.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(e.type==="call.ended"||e.type==="session.replaced"){this.setPlaybackObservation(!1,e.type==="call.ended"?"call_ended":"observation_ended"),this.shutdownIntent=e.type==="call.ended"?"call_ended":"session_replaced",this.closed=!0;try{this.callbacks.onState?.("ended",e.type)}finally{this.teardown()}}else if(e.type==="call.status"&&typeof e.call_id==="string"&&typeof e.status==="string"){if(e.hold_state)this.playbackHeld=e.hold_state!=="active",this.setPlaybackObservation(!this.playbackHeld&&this.microphoneTransportReady&&this.ctx?.state==="running",this.playbackHeld?"hold":"observation_resumed");if(e.hold_state&&e.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callbacks.onCallStatus?.(e)}else if(e.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:e.detail?.slice(0,160)}),this.fail(e.detail||"The call could not be connected.");else if(e.type==="coach.state")this.callbacks.onNotice?.(e.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(e.type==="audio.health"){let h=e;if(h.state!=="healthy"&&h.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:h.state,reason:h.reason,stages:h.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),h.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&h.state==="healthy"&&h.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(e.type==="media.delivery"){let h=e.state;if(h==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(h==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=h==="stalled"}else if(e.type==="peer.disconnected")this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"carrier_disconnected"),this.worker?.postMessage({type:"microphone.ready",value:!1}),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(e.type==="peer.connected")this.microphoneTransportReady=!0,this.setPlaybackObservation(this.ctx?.state==="running"&&!this.playbackHeld,"carrier_connected"),this.worker?.postMessage({type:"microphone.ready",value:!0}),this.callbacks.onState?.("live",this.opened?void 0:"Audio reconnected")}catch{}}startRTTProbe(){this.stopRTTProbe();let t=()=>{this.sendDiagnostics()};t(),this.pingTimer=setInterval(t,5000)}sendText(t){this.worker?.postMessage({type:"send.text",data:t})}recordSessionEvent(t){try{t={...this.callbacks.sessionDiagnostics?.(),...t}}catch{}t=lt(t),this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],t].slice(-50);try{this.callbacks.onSessionEvent?.(t)}catch{}try{let e={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,session_events:[t]}};if(new TextEncoder().encode(JSON.stringify(e)).byteLength<=1100)this.transportSender.enqueueAuxiliary(e);else this.transportSender.skippedAuxiliary++}catch{}}sendDiagnostics(){let t=this.diagnostics,e=(a,i)=>{let m=(a??[]).slice(-64),u=m.filter((f)=>!i.has(f.id)).slice(0,8);if(this.mediaSocketConnected){let f=new Set(m.map((_)=>_.id));for(let _ of i)if(!f.has(_))i.delete(_);for(let _ of u)i.add(_.id)}return u},h=e(t.playbackEvents,this.reportedPlaybackIDs),s=e(t.captureQueueEvents,this.reportedCaptureQueueIDs);if(this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{media_transport:"websocket",codec:"pcm16",client_epoch:this.clientEpoch,playback_events:h,capture_queue_events:s,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:t.rttMs,playback_queue_ms:t.queueMs,playback_target_ms:t.targetMs,playback_max_queue_ms:t.maxQueueMs,playback_underruns:t.underruns,playback_underrun_ms:t.underrunDurationMs,playback_underrun_events:t.underrunEvents,playback_dropped_ms:t.droppedMs,websocket_buffered_bytes:t.websocketBufferedBytes,audio_context_rate:t.audioContextRate,microphone_sample_rate:t.microphoneSampleRate,microphone_channel_count:t.microphoneChannelCount,echo_cancellation:t.echoCancellation,noise_suppression:t.noiseSuppression,auto_gain_control:t.autoGainControl,mic_active_rms_dbfs:t.micActiveRmsDbfs,mic_peak_dbfs:t.micPeakDbfs,mic_post_peak_dbfs:t.micPostPeakDbfs,mic_input_gain_db:t.micInputGainDb,mic_limiter_reduction_db:t.micLimiterReductionDb,capture_sequence_gaps:t.captureSequenceGaps,playback_sequence_gaps:t.playbackSequenceGaps,drop_events:t.dropEvents}})),this.mediaSocketConnected)this.transportSender.enqueue(this.transportTelemetry.drain());this.callbacks.onDiagnostics?.({...t})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setPlaybackObservation(t,e){try{if(this.playback?.port.postMessage({type:"playback.telemetry.boundary",active:t,reason:e,audio_time_ms:(this.ctx?.currentTime??0)*1000}),!t)this.diagnostics.underrunEvents=le(this.diagnostics.underrunEvents??[],e)}catch{}}setMuted(t){let e=this.muted!==t;if(this.muted=t,this.worker?.postMessage({type:"muted",value:t}),this.capture?.port.postMessage({type:"muted",value:t}),t)this.sendText(JSON.stringify({type:"interrupt"}));if(e)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:t?"muted":"unmuted"})}stop(){this.shutdownIntent="user_stop";let t=!this.closed;this.closed=!0;try{if(t)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(t){if(this.shutdownIntent="audio_error",!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:t.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",t)}finally{this.teardown()}}teardown(){this.runtimeTelemetry.stopEnvironment(),this.transportSender.stop(),this.setPlaybackObservation(!1,"observation_ended"),this.stopRingback(),this.microphoneTransportReady=!1,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let t=this.worker;if(t?.postMessage({type:"close",reason:this.shutdownIntent}),t){let e=setTimeout(()=>t.terminate(),600);t.onmessage=(h)=>{if(h.data?.type==="socket.shutdown.complete")clearTimeout(e),t.terminate()}}this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((e)=>e.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var ah=mt*xt/1000;var nt=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
`;var Yt=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
let recoveryContext = null;
function recoveryID(kind) {return \`\${kind}-\${self.crypto?.randomUUID?.() ?? Date.now().toString(16)+"-"+Math.random().toString(16).slice(2)}\`; }
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
  let session_id;
  try { const value=new URL(mediaURL).searchParams.get("session_generation");if(value && /^session-[a-f0-9-]{1,64}$/.test(value))session_id=value; } catch {/* legacy URLs have no generation */}
  postMessage({type:"runtime.event",event:{timestamp:new Date().toISOString(),action,outcome,session_id,...(action==="reconnect" ? {...recoveryContext,code:reconnectCause} : {}),duration_ms:duration_ms===undefined ? undefined : Math.round(duration_ms)}});
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
    if (openedAt && lastReceivedAt - openedAt >= 10000) { reconnectStartedAt = 0; reconnectAttempt = 0; recoveryContext=null; }
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
    if(recoveryContext && pendingRecovery) runtimeEvent("reconnect","attempt_failed");
    pendingRecovery=true;
    if(!recoveryContext) recoveryContext={recovery_id:recoveryID("recovery"),initiating_action:"automatic_retry"};
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
    runtimeEvent("reconnect","timed_out");
    postMessage({ type: "socket.failed", detail: "audio connection lost after repeated retries" });
    return;
  }
  const delay = Math.min(250 * (2 ** reconnectAttempt), MAX_RECONNECT_DELAY_MS);
  reconnectAttempt += 1;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    timing.reconnect_attempts++; recoveryContext={...recoveryContext,attempt_id:recoveryID("attempt")}; runtimeEvent("reconnect","attempt_started");
    if(!refreshCredentials) { connect(); return; }
    const id=++refreshID;
    postMessage({type:"socket.reconnect",id,cause:reconnectCause,recovery:{...recoveryContext}});
    reconnectTimer=setTimeout(()=>{runtimeEvent("reconnect","authorization_timed_out");reconnectTimer=null;refreshID++;scheduleReconnect();},5000);
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
      if(recoveryContext && pendingRecovery) runtimeEvent("reconnect","cancelled");
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
`;function dt(t=!1){let e=(t?[nt]:[nt,Yt]).map((h)=>URL.createObjectURL(new Blob([h],{type:"text/javascript"})));return{urls:e,dispose(){e.splice(0).forEach((h)=>URL.revokeObjectURL(h))}}}async function bt(t,e=!1){if(typeof location>"u")return dt(e);let h=new URL(t.mcpURL(),location.href);if(h.origin!==location.origin)return dt(e);if(t.name!=="telephony"||!t.projectId||!Number.isSafeInteger(t.installId)||t.installId<=0)throw Error("Telephony audio requires a project and installation");return{urls:await Promise.all((e?[nt]:[nt,Yt]).map(async(i,m)=>{let u=new TextEncoder().encode(i),f=Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",u)),(p)=>p.toString(16).padStart(2,"0")).join(""),_=`/ui/frontend/${m===0?"worklet":"worker"}-${f}.js`;if(await t.get(_,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==i)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let r=new URL(`/api/apps/telephony/_install/${t.installId}${_}`,h);return r.searchParams.set("project_id",t.projectId),r.searchParams.set("install_id",String(t.installId)),r.href})),dispose(){}}}class Rt{options;target;bitrate=32000;stable;last=-1/0;initial;maximum;constructor(t={}){this.options=t;this.initial=t.playbackTargetMs??60,this.target=this.initial,this.maximum=Math.max(this.initial,t.playbackMaxMs??160)}observe(t,e=performance.now()){if(e<this.last)this.last=-1/0,this.stable=void 0;if(e-this.last<2000)return;this.last=e;let h=t.receiver_jitter===void 0?void 0:t.receiver_jitter*1000,s=t.receiver_concealed_delta_ms??0;if(this.options.playbackAdaptive!==!1){let m=Math.min(this.maximum,Math.max(this.initial,this.initial+Math.ceil(Math.max(0,(h??0)-10)*2/20)*20,s>0?this.target+20:this.initial));if(m>this.target)this.target=Math.min(m,this.target+20),this.stable=void 0;else if(h!==void 0&&s===0&&h<10){if(this.stable??=e,e-this.stable>=1e4)this.target=Math.max(this.initial,this.target-10),this.stable=e}else this.stable=void 0}let{pair_availableOutgoingBitrate:a,remote_receiver_fractionLost:i}=t;if(a!==void 0&&a>0&&a<64000)this.bitrate=Math.max(16000,Math.min(32000,Math.floor((a-32000)*0.8/1000)*1000));else if(i!==void 0&&i>=0.03)this.bitrate=Math.max(16000,this.bitrate-4000);else if(i!==void 0&&i<0.01)this.bitrate=Math.min(32000,this.bitrate+2000)}}class zt{tokens=1100;at;rate=640;observeAvailableBitrate(t){this.rate=t!==void 0&&t>=96000?2000:640}consume(t,e=performance.now()){if(!Number.isFinite(t)||!Number.isFinite(e)||t<0||t>1100)return!1;if(this.at!==void 0)this.tokens=Math.min(1100,this.tokens+Math.max(0,e-this.at)*this.rate/1000);if(this.at=e,this.tokens<t)return!1;return this.tokens-=t,!0}}function Nt(t){if(t===void 0)return"websocket";if(!["websocket","webrtc","auto"].includes(t))throw RangeError("Unsupported softphone media transport");return t}function Jt(t,e){if(e!==void 0&&typeof e!=="boolean")throw RangeError("webrtcFec must be boolean");let h=new URL(t);if(h.searchParams.set("transport","webrtc"),h.searchParams.delete("webrtc_fec"),e!==void 0)h.searchParams.set("webrtc_fec",String(e));return h.toString()}var H=(t)=>{try{t?.()}catch{}},ut=(t,e=1000000000000)=>typeof t==="number"&&Number.isFinite(t)?Math.max(0,Math.min(t,e)):0;function Tt(t){let e=gt(t);return e.denied||e.expired||["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes(t?.name)}function Zt(t,e){let h=0,s=0,a=0,i=0,m=0,u=0,f=0,_=0,y=0,r=null,p,S,d,b={},g={},M={},c=(n,q)=>typeof n?.[q]==="number"&&Number.isFinite(n[q])?Math.max(0,n[q]):void 0,V=(n,q,A)=>{for(let w of A){let E=c(n,w);if(E!==void 0)g[q+w]=E}},U=(n,q,A,w,E)=>{let Q=c(n,A),P=c(n,w),F=e?.counters?.[q];if(Q!==void 0&&P!==void 0){if(b[q]??={},b[q][A]=Q,b[q][w]=P,F&&Q>=F[A]&&P>F[w])g[E]=(Q-F[A])/(P-F[w])*1000}},k=(n,q,A)=>{let w=c(n,"packetsLost"),E=c(n,"ssrc"),Q=c(n,"timestamp"),P=e?.counters?.[q];if(E!==void 0)g[A+"ssrc"]=E;if(w===void 0||Q===void 0)return;if(b[q]??={},b[q].packetsLost=w,b[q].at=Q,E!==void 0)b[q].ssrc=E;if(P&&P.ssrc===E&&w>=P.packetsLost&&Q>P.at)g[A+"loss_delta"]=(g[A+"loss_delta"]??0)+(w-P.packetsLost),g[A+"loss_window_ms"]=Q-P.at},L=(n,q,A)=>{let w=c(n,"concealedSamples"),E=c(n,"silentConcealedSamples"),Q=c(n,"timestamp"),P=c(n,"ssrc"),F=e?.counters?.[q];if(w===void 0||Q===void 0)return;if(b[q]??={},Object.assign(b[q],{concealedSamples:w,at:Q}),P!==void 0)b[q].ssrc=P;if(E!==void 0)b[q].silentConcealedSamples=E;if(F&&F.ssrc===P&&w>=F.concealedSamples&&Q>F.at){if(g.receiver_concealed_delta_ms=(g.receiver_concealed_delta_ms??0)+(w-F.concealedSamples)*1000/A,g.receiver_concealed_window_ms=Q-F.at,E!==void 0&&E>=F.silentConcealedSamples)g.receiver_silent_concealed_delta_ms=(g.receiver_silent_concealed_delta_ms??0)+(E-F.silentConcealedSamples)*1000/A}},K=!1,ft=0;t.forEach((n,q)=>{if(y=Math.max(y,ut(n.timestamp,1000000000000000)),(n.kind??n.mediaType??t.get(n.localId)?.kind)==="audio"&&ft++<32){if(n.type==="outbound-rtp")h+=ut(n.bytesSent),V(n,"sender_",["bytesSent","packetsSent","headerBytesSent","retransmittedPacketsSent","retransmittedBytesSent","nackCount","targetBitrate"]),U(n,q,"totalPacketSendDelay","packetsSent","sender_send_delay_interval_ms");if(n.type==="inbound-rtp"){k(n,q,"receiver_"),s+=ut(n.bytesReceived),a+=ut(n.packetsLost),i=Math.max(i,ut(n.jitter)*1000);let w=t.get(n.codecId),E=c(w,"clockRate")??48000;if(L(n,q,E),K||=c(n,"concealedSamples")!==void 0,m+=ut(n.concealedSamples)*1000/E,u+=ut(n.packetsDiscarded),f+=ut(n.jitterBufferDelay),_+=ut(n.jitterBufferEmittedCount),V(n,"receiver_",["bytesReceived","packetsReceived","packetsLost","jitter","packetsDiscarded","concealedSamples","silentConcealedSamples","concealmentEvents","insertedSamplesForDeceleration","removedSamplesForAcceleration","totalSamplesReceived","audioLevel","totalAudioEnergy","totalSamplesDuration","nackCount","fecPacketsReceived","fecPacketsDiscarded"]),U(n,q,"jitterBufferDelay","jitterBufferEmittedCount","receiver_jitter_buffer_interval_ms"),U(n,q,"jitterBufferTargetDelay","jitterBufferEmittedCount","receiver_jitter_target_interval_ms"),U(n,q,"jitterBufferMinimumDelay","jitterBufferEmittedCount","receiver_jitter_minimum_interval_ms"),U(n,q,"totalProcessingDelay","packetsReceived","receiver_processing_interval_ms"),c(n,"lastPacketReceivedTimestamp")!==void 0)g.receiver_last_packet_age_ms=Math.max(0,n.timestamp-n.lastPacketReceivedTimestamp);if(w){if(M.codec=w.mimeType,g.codec_clock_rate=E,c(w,"channels")!==void 0)g.codec_channels=w.channels}}if(n.type==="remote-inbound-rtp")V(n,"remote_receiver_",["packetsLost","fractionLost","jitter","roundTripTime","totalRoundTripTime","roundTripTimeMeasurements"]),k(n,q,"remote_receiver_");if(n.type==="remote-outbound-rtp")V(n,"remote_sender_",["packetsSent","bytesSent"]);let A=n.type==="outbound-rtp"?"bytesSent":n.type==="inbound-rtp"?"bytesReceived":void 0;if(A&&c(n,A)!==void 0)b[q]??={},b[q][A]=n[A],b[q].at=n.timestamp}if(n.type==="transport"&&n.selectedCandidatePairId){p=n.selectedCandidatePairId,M.dtls=n.dtlsState;let A=t.get(p);if(A){r=c(A,"currentRoundTripTime")!==void 0?A.currentRoundTripTime*1000:null,V(A,"pair_",["availableOutgoingBitrate","availableIncomingBitrate","currentRoundTripTime","totalRoundTripTime","bytesSent","bytesReceived","requestsSent","requestsReceived","responsesSent","responsesReceived","consentRequestsSent"]),M.pair=A.state;let w=t.get(A.localCandidateId),E=t.get(A.remoteCandidateId);if(w)S=w.protocol,d=w.candidateType,M.protocol=S,M.local_candidate=d,M.relay_protocol=w.relayProtocol;if(E)M.remote_candidate=E.candidateType}}});let v=(n,q)=>{if(e?.counters){let w=0;for(let[E,Q]of Object.entries(b)){let P=e.counters[E];if(P&&Q[n]>=P[n]&&Q.at>P.at)w+=(Q[n]-P[n])*8000/(Q.at-P.at)}return w}let A=e&&y>e.at?(y-e.at)/1000:0;return A?Math.max(0,q-(n==="bytesSent"?e.sent:e.received))*8/A:0},tt=(e?.pathRevision??0)+(p&&p!==e?.pairID?1:0);g.path_revision=tt;let j=v("bytesSent",h),Mt=v("bytesReceived",s);return{previous:{at:y,sent:h,received:s,counters:b,pairID:p,pathRevision:tt},rtt:r,queueMs:_>0?f/_*1000:0,metrics:Vt({...g,send_bitrate_bps:j,receive_bitrate_bps:Mt}),states:$t(M),webrtc:{protocol:S,candidateType:d,sendBitrateBps:j,receiveBitrateBps:Mt,packetsLost:a,jitterMs:i,concealedMs:K?m:void 0,packetsDiscarded:u,jitterBufferMs:_>0?f/_*1000:0}}}function be(t,e=new Date().toISOString()){let h=[];for(let[s,a]of[["receiver_","carrier_to_operator"],["remote_receiver_","operator_to_carrier"]]){let i=t[s+"loss_delta"];if(i>0)h.push({timestamp:e,direction:a,reason:"webrtc_packet_loss",duration_ms:0,packet_count:i,ssrc:t[s+"ssrc"],window_ms:t[s+"loss_window_ms"]})}if(t.receiver_concealed_delta_ms>0)h.push({timestamp:e,direction:"carrier_to_operator",reason:"webrtc_concealment",duration_ms:Math.round(t.receiver_concealed_delta_ms),ssrc:t.receiver_ssrc,window_ms:t.receiver_concealed_window_ms});return h}class Dt{callbacks;stopped=!1;generation=0;socket;pc;stream;context;capture;whisper;destination;speaker;remoteAudio;speakerMeter;carrierStalled=!1;deliveryDegraded=!1;ready=!1;peer=!1;muted=!1;whisperEpoch=null;whisperBase=null;whisperResampler=new _t;timer;retry;cancelSetup;recovering=!1;recoveryGeneration=0;recoveryContext;recoveryExpiry;ringback;options;workletURL;events=[];previous;sampling=new WeakSet;transportTelemetry=new Pt("webrtc");transportSender=new Et((t)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[t]}})},(t)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry(t)});controlBudget=new zt;quality;playbackReceiver;uploadSender;applyingBitrate=!1;appliedBitrate;statsErrors=0;lastStatsSend=-1/0;clientEpoch=crypto.randomUUID();nativeCounts;completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};runtime=new Ct((t)=>this.recordSessionEvent(t));diagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:null,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t){this.callbacks=t}async start(t,e,h){if(this.options=e,this.workletURL=h,!h)throw Error("WebRTC requires the shared Telephony audio processor");if(typeof RTCPeerConnection!=="function")throw Error("WebRTC audio is unavailable in this browser");D(e),await this.connect(t)}current(t){return!this.stopped&&this.generation===t}guard(t){if(!this.current(t))throw Error("Audio session cancelled")}async connect(t){this.runtime.observeEnvironment();let e=++this.generation,h=this.options;for(let r of this.events.slice(-8))this.enqueueRTCEvents({session_events:[r]});for(let r of this.diagnostics.dropEvents.slice(-8))this.enqueueRTCEvents({drop_events:[r]});this.ready=this.peer=!1,this.previous=void 0,this.quality=new Rt(h),this.appliedBitrate=void 0;let s=await navigator.mediaDevices.getUserMedia({audio:it(h)});if(!this.current(e))throw s.getTracks().forEach((r)=>r.stop()),Error("Audio session cancelled");this.stream=s;let a=s.getAudioTracks()[0];if(!a)throw Error("No microphone audio track");let i=a.getSettings();this.diagnostics={...this.diagnostics,microphoneSampleRate:i.sampleRate??0,microphoneChannelCount:i.channelCount??1,echoCancellation:typeof i.echoCancellation==="boolean"?i.echoCancellation:null,noiseSuppression:i.noiseSuppression??null,autoGainControl:i.autoGainControl??null,micInputGainDb:h.inputGainDB},a.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),a.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),a.onended=()=>{if(this.current(e))this.disconnected(e,"microphone_ended")},this.context=new AudioContext({latencyHint:"interactive"});let m=this.context;if(m.onstatechange=()=>this.runtime.context(m.state),this.runtime.context(m.state),await m.resume(),this.guard(e),h.outputDeviceId&&"setSinkId"in m)await m.setSinkId(h.outputDeviceId);await m.audioWorklet.addModule(this.workletURL),this.guard(e),this.diagnostics.audioContextRate=m.sampleRate,this.capture=new AudioWorkletNode(m,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:h.inputGainDB,highpassFilter:h.highpassFilter}}),this.destination=m.createMediaStreamDestination(),this.destination.channelCount=1,m.createMediaStreamSource(s).connect(this.capture).connect(this.destination),this.capture.port.onmessage=(r)=>{if(r.data?.type==="capture.stats"){let p=(d)=>d>0?20*Math.log10(d):null;this.diagnostics.micActiveRmsDbfs=p(r.data.active_rms),this.diagnostics.micPeakDbfs=p(r.data.pre_peak),this.diagnostics.micPostPeakDbfs=p(r.data.post_peak),this.diagnostics.micLimiterReductionDb=r.data.limiter_reduction_db??0;let S=0;if(this.speakerMeter){let d=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(d),S=Math.sqrt(d.reduce((b,g)=>b+g*g,0)/d.length)}H(()=>this.callbacks.onLevels?.(ut(r.data.active_rms,1),S))}},this.speaker=m.createGain(),this.speaker.gain.value=h.outputVolume??1,this.speaker.connect(m.destination),this.speakerMeter=m.createAnalyser(),this.speakerMeter.fftSize=256;let u=m.createGain();u.gain.value=0,this.speaker.connect(this.speakerMeter).connect(u).connect(m.destination),this.whisper=new AudioWorkletNode(m,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:D(h)}),this.whisper.connect(this.speaker),this.gate();let f=new WebSocket(Jt(t,h.webrtcFec));f.binaryType="arraybuffer",this.socket=f;let _=!1,y=!1;await new Promise((r,p)=>{let S=(g)=>{if(y)return;y=!0,clearTimeout(d),this.cancelSetup=void 0,g?p(g):r()},d=setTimeout(()=>S(Error("WebRTC audio connection timed out")),18000);this.cancelSetup=()=>S(Error("Audio session cancelled"));let b=()=>{if(this.current(e)&&this.ready&&_)this.gate(),S(),H(()=>this.callbacks.onState?.("live"))};f.onmessage=(g)=>{if(!this.current(e))return;if(g.data instanceof ArrayBuffer){this.playWhisper(g.data);return}if(typeof g.data!=="string")return;let M;try{M=JSON.parse(g.data)}catch{return}if(M.type==="webrtc.config"){if(this.pc){S(Error("Repeated WebRTC configuration"));return}(async()=>{let c=new RTCPeerConnection({iceServers:M.ice_servers??[]});this.pc=c,this.destination.stream.getAudioTracks().forEach((U)=>{let k=c.addTrack(U,this.destination.stream);this.uploadSender=k,this.applyUploadBitrate(k,32000,e)}),c.ontrack=(U)=>{if(!this.current(e))return;this.playbackReceiver=U.receiver,this.applyPlaybackTarget();let k=new MediaStream([U.track]),L=new Audio;L.muted=!0,L.srcObject=k,this.remoteAudio=L,L.play().catch(()=>H(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction."))),m.createMediaStreamSource(k).connect(this.speaker)},c.onconnectionstatechange=()=>{if(!this.current(e))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:c.connectionState}),c.connectionState==="connected")_=!0,b();if(c.connectionState==="failed"||c.connectionState==="closed")if(!y)S(Error("WebRTC connection failed"));else this.disconnected(e,"webrtc_"+c.connectionState)};let V=await c.createOffer();this.guard(e),await c.setLocalDescription(V),await new Promise((U,k)=>{if(c.iceGatheringState==="complete"){U();return}let L=setTimeout(()=>{c.onicegatheringstatechange=null,k(Error("ICE gathering timed out"))},7000);c.onicegatheringstatechange=()=>{if(c.iceGatheringState==="complete")clearTimeout(L),c.onicegatheringstatechange=null,U()}}),this.guard(e),f.send(JSON.stringify({type:"webrtc.offer",sdp:c.localDescription.sdp}))})().catch((c)=>S(c instanceof Error?c:Error("WebRTC setup failed")));return}if(M.type==="webrtc.answer"){this.pc?.setRemoteDescription({type:"answer",sdp:M.sdp}).catch(()=>S(Error("Invalid WebRTC answer")));return}if(M.type==="ready")this.ready=!0,this.send({type:"media.capabilities",version:2,versions:[3],whisper:!0}),b();if(M.type==="peer.connected")this.peer=!0,this.gate();if(M.type==="peer.disconnected")this.peer=!1,this.gate(),H(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));if(M.type==="call.status")H(()=>this.callbacks.onCallStatus?.(M));if(M.type==="call.error")if(!y)S(Error(M.detail??"Carrier activation failed"));else this.stop(),H(()=>this.callbacks.onState?.("error",M.detail));if(M.type==="call.ended"||M.type==="session.replaced")if(!y)S(Error(M.type));else this.stop(),H(()=>this.callbacks.onState?.("ended",M.type));if(M.type==="audio.health"){if(this.diagnostics.audioHealth={state:M.state,reason:M.reason,stages:M.stages},H(()=>this.callbacks.onAudioHealth?.(M)),M.state==="audio_degraded"&&!this.deliveryDegraded)this.deliveryDegraded=!0,H(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));else if(this.deliveryDegraded&&M.state==="healthy"&&M.stages?.telephony_to_browser?.state==="healthy"&&m.state==="running")this.deliveryDegraded=!1,H(()=>this.callbacks.onNotice?.("Speech delivery restored."))}if(M.type==="media.delivery"){if(M.state==="stalled")H(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));else if(M.state==="flowing"&&this.carrierStalled)H(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));this.carrierStalled=M.state==="stalled"}if(M.type==="call.notice")H(()=>this.callbacks.onNotice?.(M.detail??M.reason??"Audio delivery notice"));if(M.type==="coach.state")this.whisperEpoch=M.talking&&Number.isInteger(M.epoch)?M.epoch:null,this.whisperBase=null,this.whisper?.port.postMessage({type:"whisper.clear"})},f.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"}),f.onclose=(g)=>{if(!this.current(e))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(g.code),was_clean:g.wasClean}),!y){S(Object.assign(Error("WebRTC signaling unavailable"),g.code===1008?{status:403}:{}));return}if(g.code===1008){this.stop(),H(()=>this.callbacks.onState?.("error","Media authorization ended"));return}this.disconnected(e,"signaling_closed")}}),this.guard(e),this.timer=setInterval(()=>{this.runtime.tick(),this.statistics(e)},1000),await this.statistics(e)}send(t){if(this.socket?.readyState===WebSocket.OPEN&&this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(t))}catch{}}gate(){let t=this.ready&&this.peer&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!t}),this.destination?.stream.getAudioTracks().forEach((e)=>e.enabled=t)}async statistics(t){if(!this.current(t)||!this.pc||this.sampling.has(this.pc))return;let e=this.pc,h=performance.now();this.sampling.add(e);try{let s=Zt(await e.getStats(),this.previous);if(!this.current(t)||this.pc!==e)return;if(this.previous=s.previous,this.nativeCounts={packetsLost:s.webrtc.packetsLost,packetsDiscarded:s.webrtc.packetsDiscarded,concealedMs:s.webrtc.concealedMs},s.webrtc.packetsLost+=this.completedCounts.packetsLost,s.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded,s.webrtc.concealedMs!==void 0)s.webrtc.concealedMs+=this.completedCounts.concealedMs;if(this.quality?.observe(s.metrics),this.applyPlaybackTarget(),this.uploadSender&&this.quality)this.applyUploadBitrate(this.uploadSender,this.quality.bitrate,t);if(s.metrics.requested_playback_target_ms=this.quality?.target??60,this.appliedBitrate!==void 0)s.metrics.requested_upload_bitrate_bps=this.appliedBitrate;this.controlBudget.observeAvailableBitrate(s.metrics.pair_availableOutgoingBitrate);let a=be(s.metrics);for(let m of a)this.enqueueRTCEvents({drop_events:[m]});this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,...a].slice(-100),this.diagnostics={...this.diagnostics,rttMs:s.rtt,queueMs:s.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,s.queueMs),targetMs:this.quality?.target??this.options?.playbackTargetMs??60,webrtc:s.webrtc,playbackSequenceGaps:s.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};let i=this.stream?.getAudioTracks()[0];this.transportTelemetry.observe({...s.metrics,rtt_ms:s.rtt,queue_ms:s.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:this.socket?.bufferedAmount??0,stats_errors:this.statsErrors,stats_duration_ms:performance.now()-h,main_thread_max_pause_ms:this.runtime.counters.main_thread_max_pause_ms},{...s.states,ice:e.iceConnectionState,connection:"connected",context:this.context?.state,muted:String(this.muted),device_muted:String(i?.muted),track:i?.readyState}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),H(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}catch{if(this.current(t)&&this.pc===e)this.statsErrors++,this.transportTelemetry.observe({stats_errors:this.statsErrors,stats_duration_ms:performance.now()-h},{ice:e.iceConnectionState,context:this.context?.state,muted:String(this.muted)}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),H(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}finally{this.sampling.delete(e)}}applyPlaybackTarget(){let t=this.playbackReceiver;if(!t||!this.quality)return;if("jitterBufferTarget"in t)try{t.jitterBufferTarget=this.quality.target}catch{}}async applyUploadBitrate(t,e,h){if(this.applyingBitrate||this.appliedBitrate===e)return;this.applyingBitrate=!0;try{let s=t.getParameters();if(s.encodings?.length){if(s.encodings[0].maxBitrate=e,await t.setParameters(s),this.current(h)&&this.uploadSender===t)this.appliedBitrate=e}}catch{}finally{this.applyingBitrate=!1}}sendTelemetry(t){if(!this.ready||this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let e=JSON.stringify(t),h=new TextEncoder().encode(e).byteLength+58;if(!this.controlBudget.consume(h))return!1;try{return this.socket.send(e),!0}catch{return!1}}enqueueRTCEvents(t){try{let e={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,...t}};if(new TextEncoder().encode(JSON.stringify(e)).byteLength+58>1100){this.transportSender.skippedAuxiliary++;return}this.transportSender.enqueueAuxiliary(e)}catch{this.transportSender.skippedAuxiliary++}}sendStatistics(){let t=this.stream?.getAudioTracks()[0];if(performance.now()-this.lastStatsSend>=5000)this.lastStatsSend=performance.now(),this.transportSender.enqueueReport({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,media_transport:"webrtc",codec:"opus",webrtc:Object.fromEntries(Object.entries(this.diagnostics.webrtc??{}).map(([e,h])=>[e,typeof h==="number"?Math.round(h*10)/10:h])),connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:t?.readyState,microphone_device_muted:t?.muted,rtt_ms:this.diagnostics.rttMs===null?null:Math.round(this.diagnostics.rttMs),playback_queue_ms:Math.round(this.diagnostics.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:this.diagnostics.webrtc?.packetsLost}}),this.transportSender.enqueue(this.transportTelemetry.drain()),this.enqueueRTCEvents({timing:{runtime:this.runtime.counters}})}playWhisper(t){if(t.byteLength<17||t.byteLength>176||this.whisperEpoch===null||!this.context)return;let e=new DataView(t);if(e.getUint32(0,!0)!==827805761||e.getUint32(4,!0)!==this.whisperEpoch)return;let h=e.getFloat64(8,!0);if(!Number.isFinite(h))return;let s=performance.timeOrigin+performance.now()-h;this.whisperBase=this.whisperBase===null?s:Math.min(this.whisperBase,s);let a=Math.max(0,s-this.whisperBase);if(a>200)return;let i=new Float32Array(t.byteLength-16);for(let u=0;u<i.length;u++){let f=~e.getUint8(u+16)&255,_=((f&15)<<3)+132<<(f>>4&7);i[u]=(f&128?132-_:_-132)/32768}let m=this.context.sampleRate===8000?i:this.whisperResampler.process(i,8000,this.context.sampleRate);this.whisper?.port.postMessage({type:"whisper",frame:m,received_audio_ms:this.context.currentTime*1000-a},[m.buffer])}disconnected(t,e){if(!this.current(t)||this.recovering)return;this.recovering=!0;let h=ct("automatic_retry");this.recoveryContext=h,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"started",code:e,...h}),this.cleanup(),H(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));let s=++this.recoveryGeneration,a=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===s;this.recoveryExpiry=setTimeout(()=>{if(!a())return;this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"timed_out",...this.recoveryContext}),++this.recoveryGeneration,this.recovering=!1,clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup(),H(()=>this.callbacks.onState?.("error","WebRTC audio recovery timed out; reconnect audio to the existing call."))},30000);let i=async()=>{if(!a())return;let m={...h,attempt_id:Gt("attempt")};this.recoveryContext=m,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"attempt_started",code:e,...m});try{if(!this.callbacks.refreshMediaURL)throw Error("Fresh media authorization is required");let u=await this.callbacks.refreshMediaURL(m);if(!a())return;if(await this.connect(u),!a())return;if(this.pc?.connectionState!=="connected")throw Error("WebRTC disconnected during recovery");clearTimeout(this.recoveryExpiry),this.recovering=!1,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected",...m})}catch(u){if(!a())return;this.cleanup();let f=gt(u);if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:Tt(u)?"revoked":"attempt_failed",...m,status:f.status,code:f.code}),Tt(u)){clearTimeout(this.recoveryExpiry),this.recovering=!1,H(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return}this.retry=setTimeout(()=>void i(),1000)}};i()}recordSessionEvent(t){try{t={...this.callbacks.sessionDiagnostics?.(),...t}}catch{}t=lt(t),this.events=[...this.events,t].slice(-100),this.enqueueRTCEvents({session_events:[t]}),H(()=>this.callbacks.onSessionEvent?.(t))}setMuted(t){if(this.muted!==t)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:t?"muted":"unmuted"});if(this.muted=t,this.gate(),t)this.send({type:"interrupt"})}sendDTMF(t){if(!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");this.send({type:"dtmf",digits:t})}setOutputVolume(t){if(!Number.isFinite(t)||t<0||t>1)throw RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=t;if(this.options)this.options.outputVolume=t}startRingback(t){if(this.stopRingback(),this.context&&this.speaker)this.ringback=Bt(this.context,this.speaker,Qt(t))}stopRingback(){this.ringback?.(),this.ringback=void 0}cleanup(){if(this.runtime.stopEnvironment(),this.transportSender.stop(),this.nativeCounts){for(let s of["packetsLost","packetsDiscarded","concealedMs"])this.completedCounts[s]+=this.nativeCounts[s]??0;this.nativeCounts=void 0}++this.generation,this.ready=this.peer=!1,this.stopRingback(),clearInterval(this.timer),this.timer=void 0;let t=this.socket;if(this.socket=void 0,t){if(t.readyState===WebSocket.OPEN)try{t.send(JSON.stringify({type:"media.shutdown",reason:this.stopped?"session_cleanup":"audio_error"}))}catch{}t.onclose=t.onmessage=t.onerror=null,t.close(1000,"client_shutdown")}this.playbackReceiver=void 0,this.uploadSender=void 0;let e=this.pc;if(this.pc=void 0,e)e.onconnectionstatechange=e.ontrack=null,e.close();if(this.remoteAudio)this.remoteAudio.pause(),this.remoteAudio.srcObject=null,this.remoteAudio=void 0;this.stream?.getTracks().forEach((s)=>{s.onended=null,s.stop()}),this.stream=void 0,this.destination?.stream.getTracks().forEach((s)=>s.stop()),this.destination=void 0,this.capture?.disconnect(),this.capture=void 0,this.whisper?.disconnect(),this.whisper=void 0,this.whisperEpoch=null,this.whisperBase=null,this.whisperResampler=new _t;let h=this.context;if(this.context=void 0,h)h.onstatechange=null,h.close().catch(()=>{});this.speaker=void 0,this.speakerMeter=void 0}stop(){if(this.stopped)return;if(this.recovering)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"cancelled",...this.recoveryContext});this.stopped=!0,++this.recoveryGeneration,clearTimeout(this.recoveryExpiry),clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup()}}function me(t,e,h){let s,a=!1,i=!1;return{async start(m,u){let f=Nt(u.mediaTransport),_=async(y)=>{if(a)throw Error("Audio session cancelled");let r=y();s=r,r.setMuted(i);try{if(await r.start(m,u),a)throw Error("Audio session cancelled")}catch(p){if(r.stop(),s===r)s=void 0;throw p}};if(f==="websocket"){await _(e);return}try{await _(h)}catch(y){if(f!=="auto"||a||Tt(y))throw y;H(()=>t.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"})),await _(e)}},stop(){a=!0,s?.stop(),s=void 0},setMuted(m){i=m,s?.setMuted(m)},setOutputVolume(m){s?.setOutputVolume(m)},sendDTMF(m){s?.sendDTMF(m)},recordSessionEvent(m){s?.recordSessionEvent?.(m)},startRingback(m){s?.startRingback?.(m)},stopRingback(){s?.stopRingback?.()}}}async function ie(){return(await navigator.mediaDevices.enumerateDevices()).filter((e)=>e.kind==="audioinput").map((e,h)=>({deviceId:e.deviceId,label:e.label||`Microphone ${h+1}`}))}function ue(t,e){let h=new Lt(t,!1),s,a=!1,i,m=()=>{return a=!0,i??=(async()=>{try{await h.cancel()}finally{s?.dispose(),s=void 0}})()};return{async start(u={}){if(a)throw Error("Microphone preview is already used");a=!0;try{if(s=e?await bt(e,!0):dt(!0),i)throw s.dispose(),s=void 0,Error("Microphone preview was cancelled");await h.start(s.urls[0],{...o,...u})}catch(f){throw await m(),f}},stop:m}}function Ae(t){return{async preflight(e){(await navigator.mediaDevices.getUserMedia({audio:it(e)})).getTracks().forEach((s)=>s.stop())},create(e){let h=new Xt(e),s,a=!1,i=()=>{a=!0;try{h.stop()}finally{s?.dispose(),s=void 0}};return{async start(m,u){try{if(s=t?await bt(t):dt(),a)throw Error("Audio session was cancelled");await h.start(m,s.urls[0],s.urls[1],u)}catch(f){throw i(),f}},stop:i,recordSessionEvent:(m)=>h.recordSessionEvent(m),setMuted:(m)=>h.setMuted(m),sendDTMF:(m)=>h.sendDTMF(m),setOutputVolume:(m)=>h.setOutputVolume(m),startRingback:(m)=>h.startRingback(m),stopRingback:()=>h.stopRingback()}}}}function re(t){let e=Ae(t);return{preflight:(h)=>e.preflight(h),create(h){return me(h,()=>e.create(h),()=>{let s=new Dt(h),a,i=!1;return{async start(m,u){if(a=t?await bt(t):dt(),i)throw a.dispose(),a=void 0,Error("Audio session cancelled");await s.start(m,u,a.urls[0])},stop(){i=!0,s.stop(),a?.dispose(),a=void 0},setMuted:(m)=>s.setMuted(m),sendDTMF:(m)=>s.sendDTMF(m),setOutputVolume:(m)=>s.setOutputVolume(m),recordSessionEvent:(m)=>s.recordSessionEvent(m),startRingback:(m)=>s.startRingback(m),stopRingback:()=>s.stopRingback()}})}}}function fe(t,e){return{create(h){let s={...o,...e},a=D(s),i=new Rt(s),m=[],u,f,_,y,r=!1,p=!1,S=!1,d=!1,b=!1,g=!1,M=0,c,V,U,k,L,K,ft=!1,v,tt,j=s.outputVolume??1,Mt={},n=32000,q,A,w,E,Q=[],P=(l)=>{if(u?.readyState===WebSocket.OPEN&&u.bufferedAmount<65536)u.send(JSON.stringify(l));else throw Error("Listener connection unavailable")},F=()=>{if(r)throw Error("Listening cancelled")},R=()=>{let l=++M;if(b=g=!1,q?.(l,Error("Coaching cancelled")),E?.(),A=void 0,v)clearInterval(v);if(v=void 0,L)L.onconnectionstatechange=null,L.close(),L=void 0;if(V?.disconnect(),V=void 0,U?.disconnect(),U=void 0,k?.disconnect(),k=void 0,c?.getTracks().forEach((T)=>{T.onended=null,T.stop()}),c=void 0,d&&p&&u?.readyState===WebSocket.OPEN)try{P({type:"coach.stop",generation:l})}catch{}h.onTalking?.(!1)},Y=()=>R(),J=()=>{if(document.visibilityState!=="visible")R()},yt=()=>{if(r)return;if(R(),r=!0,w?.(),window.removeEventListener("blur",Y),document.removeEventListener("visibilitychange",J),tt)clearInterval(tt);if(tt=void 0,u)u.onmessage=u.onclose=u.onerror=null,u.close(1000,"listener_shutdown");if(f)f.ontrack=f.onconnectionstatechange=null,f.close();for(let l of Q)l.pause(),l.srcObject=null;if(y?.disconnect(),_)_.onstatechange=null,_.close().catch(()=>{});K?.dispose(),K=void 0},X=(l)=>{if(!r)yt(),h.onClose(l)},N=async(l,T)=>{if(await l.setLocalDescription(await l.createOffer()),await new Promise((G,C)=>{if(l.iceGatheringState==="complete"){G();return}let x=setTimeout(()=>{l.onicegatheringstatechange=null,C(Error("ICE gathering timed out"))},7000);l.onicegatheringstatechange=()=>{if(l.iceGatheringState==="complete"||!T())clearTimeout(x),l.onicegatheringstatechange=null,T()?G():C(Error("Coaching cancelled"))}}),!T())throw Error("Listening cancelled");return l.localDescription.sdp};return{async start(l,T){if(d=T?.coaching===!0,F(),typeof RTCPeerConnection!=="function")throw Error("WebRTC audio unavailable");try{if(_=new AudioContext({latencyHint:"interactive"}),await _.resume(),F(),s.outputDeviceId&&"setSinkId"in _)await _.setSinkId(s.outputDeviceId);if(F(),y=_.createGain(),y.gain.value=j,y.connect(_.destination),_.onstatechange=()=>{if(_?.state==="suspended"||_?.state==="interrupted")X("listener_audio_paused")},await new Promise((x,B)=>{let z=!1,Z=!1,O=(ht)=>{if(z)return;z=!0,clearTimeout(et),w=void 0,ht?B(ht):x()},et=setTimeout(()=>O(Error("WebRTC listener timed out")),18000),Ft=()=>{if(S&&Z&&!r&&!p)p=!0,O(),h.onReady("webrtc")};w=()=>O(Error("Listening cancelled")),u=new WebSocket(Jt(l,s.webrtcFec)),u.onmessage=({data:ht})=>{if(r)return;if(typeof ht!=="string"){O(Error("Invalid WebRTC listener frame")),X("listener_protocol_error");return}let $;try{$=JSON.parse(ht)}catch{O(Error("Invalid listener control")),X("listener_protocol_error");return}if($.type==="webrtc.config"){if(f||$.listener!==!0){O(Error("Invalid WebRTC listener configuration"));return}Mt={iceServers:$.ice_servers??[]},n=$.bitrate??32000,f=new RTCPeerConnection(Mt);let st=f;for(let I=0;I<2;I++)st.addTransceiver("audio",{direction:"recvonly"});st.ontrack=(I)=>{if(r||!_||!y)return;m.push(I.receiver);let Ht=st.getTransceivers().findIndex((St)=>St.receiver===I.receiver);if(Ht<0||Ht>1){X("listener_protocol_error");return}if("jitterBufferTarget"in I.receiver)try{I.receiver.jitterBufferTarget=a.initialTargetMs}catch{}let vt=new MediaStream([I.track]),Ot=new Audio;Ot.muted=!0,Ot.srcObject=vt,Q.push(Ot),Ot.play().catch(()=>{});let te=_.createMediaStreamSource(vt);if(e.stereo){let St=_.createStereoPanner();St.pan.value=Ht===0?-1:1,te.connect(St).connect(y)}else{let St=_.createGain();St.gain.value=0.5,te.connect(St).connect(y)}},st.onconnectionstatechange=()=>{if(r)return;if(st.connectionState==="connected")S=!0,Ft();if(st.connectionState==="failed"||st.connectionState==="closed"){if(O(Error("WebRTC listener connection failed")),z&&p)X("listener_network_error")}},N(st,()=>!r).then((I)=>P({type:"webrtc.offer",sdp:I})).catch((I)=>O(I));return}if($.type==="webrtc.answer"){f?.setRemoteDescription({type:"answer",sdp:$.sdp}).catch((st)=>O(st));return}if($.type==="listener.ready"){if(d&&$.coaching!==!0){O(Error("Coaching not authorized"));return}Z=!0,Ft();return}if($.type==="coach.started"){q?.($.generation);return}if($.type==="coach.rejected"){q?.(M,Error($.detail||"Coaching unavailable"));return}if($.type==="coach.stopped"&&($.generation===void 0||$.generation===M)){R();return}if($.type==="coach.webrtc.answer"){A?.($.sdp,$.generation);return}},u.onerror=()=>{if(O(Error("Listener connection failed")),p)X("listener_network_error")},u.onclose=(ht)=>{if(O(Error(ht.reason||"Listener disconnected")),p)X(ht.reason||"listener_disconnected")}}),F(),d)window.addEventListener("blur",Y),document.addEventListener("visibilitychange",J);let G,C=!1;tt=setInterval(()=>{if(r||!f||C)return;C=!0,f.getStats().then((x)=>{if(r)return;let B=Zt(x,G);G=B.previous,i.observe({...B.metrics,receiver_jitter:B.webrtc.jitterMs/1000});for(let z of m)if("jitterBufferTarget"in z)try{z.jitterBufferTarget=i.target}catch{}h.onDiagnostics?.({media_transport:"webrtc",dropped_ms:[],played_ms:[],max_queue_ms:B.queueMs,max_late_ms:0,sequence_gaps:[],network_excess_ms:0,network_dropped_ms:[],webrtc:B.webrtc})}).catch(()=>{}).finally(()=>{C=!1})},1000)}catch(G){throw yt(),G}},async startTalking(){if(F(),!d||!p||!_)throw Error("Coaching session not ready");if(b)return;b=!0;let l=++M,T=()=>!r&&b&&M===l;try{let G=await navigator.mediaDevices.getUserMedia({audio:it(s)});if(!T()){G.getTracks().forEach((x)=>x.stop());return}if(c=G,c.getTracks().forEach((x)=>{x.onended=()=>R()}),!K){let x=await bt(t,!0);if(!T()){x.dispose();return}K=x}if(!ft)await _.audioWorklet.addModule(K.urls[0]),ft=!0;if(!T())return;if(await new Promise((x,B)=>{let z=setTimeout(()=>{q=void 0,B(Error("Coaching activation timed out"))},3000);q=(Z,O)=>{if(O||Z===l)clearTimeout(z),q=void 0,O?B(O):x()},P({type:"coach.start",generation:l})}),!T())return;v=setInterval(()=>{if(T())try{P({type:"coach.keepalive",generation:l})}catch{R()}},1000),V=new AudioWorkletNode(_,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),k=_.createMediaStreamDestination(),k.channelCount=1,U=_.createMediaStreamSource(G),U.connect(V).connect(k);let C=new RTCPeerConnection(Mt);if(L=C,C.addTransceiver(k.stream.getAudioTracks()[0],{direction:"sendonly",streams:[k.stream],sendEncodings:[{maxBitrate:n}]}),await new Promise((x,B)=>{let z=!1,Z=(et)=>{if(z)return;z=!0,clearTimeout(O),E=void 0,A=void 0,et?B(et):x()},O=setTimeout(()=>Z(Error("Coaching WebRTC timed out")),18000);E=()=>Z(Error("Coaching cancelled")),A=(et,Ft)=>{if(Ft!==l||!T())return;C.setRemoteDescription({type:"answer",sdp:et}).catch((ht)=>Z(ht))},C.onconnectionstatechange=()=>{if(!T())return;if(C.connectionState==="connected")Z();if(C.connectionState==="failed"||C.connectionState==="closed")Z(Error("Coaching media disconnected")),R()},N(C,T).then((et)=>{if(T())P({type:"coach.webrtc.offer",generation:l,sdp:et})}).catch((et)=>Z(et))}),T())g=!0,h.onTalking?.(!0)}catch(G){if(T())throw R(),G}},stopTalking:R,stop:yt,setOutputVolume(l){if(!Number.isFinite(l)||l<0||l>1)throw RangeError("Listener volume must be 0–1");if(j=l,y)y.gain.value=l}}}}}var Ut=`// Passive receiver only. Both directions share one rendering clock and delay.
class TelephonyListenerProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    this.targetMS = options.processorOptions?.initialTargetMs ?? 60;
    this.capacityMS = Math.min(320, Math.max(220,this.targetMS+80));
    this.futureMS = Math.min(320, Math.max(200,this.targetMS+60));
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
      if (data.playAtMS + duration < now || data.playAtMS > now + this.futureMS) {
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
      while (queue.length > Math.ceil((this.capacityMS - 60) / 20)) {
        const old = queue.shift(); this.dropped[d] += old.frame.length - (old.played || 0); trimmed = true;
      }
      const last = queue[queue.length - 1];
      if (trimmed || (last.start + last.frame.length) * 1000 / sampleRate > now + this.capacityMS) {
        // Rebase only this listener's backlog. Keep a small cushion and a hard
        // upper bound instead of letting repeated catch-up bursts build delay.
        let next = Math.round((now + Math.max(40, this.targetMS - 20)) * sampleRate / 1000);
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
`;function At(t={},e={}){let h=["mediaTransport","webrtcFec","inputDeviceId","outputDeviceId","outputVolume","echoCancellation","noiseSuppression","autoGainControl","inputGainDB","highpassFilter","playbackTargetMs","playbackMinMs","playbackMaxMs","playbackAdaptive","stereo"],s={...o};for(let a of[t,e])for(let i of h){let m=a[i];if(m!==void 0)s[i]=m}if(D(s),s.mediaTransport=Nt(s.mediaTransport),s.webrtcFec??=!0,s.outputVolume!==void 0&&(!Number.isFinite(s.outputVolume)||s.outputVolume<0||s.outputVolume>1))throw RangeError("Listener volume must be 0–1");return s}function Pe(t,e,h,s){let a,i=!1,m=!1,u,f={...t,onReady(_){m=!0,t.onReady(_)}};return{async start(_,y){if(i)throw Error("Listening cancelled");a=e==="websocket"?h(f):s(f);try{await a.start(_,y)}catch(r){if(a.stop(),i||m||e!=="auto"||Tt(r))throw r;a=h(f),await a.start(_,y)}if(i)a.stop();else if(u!==void 0)a.setOutputVolume(u)},async startTalking(){if(i||!a?.startTalking)throw Error("Coaching session not ready");await a.startTalking()},stopTalking(){a?.stopTalking?.()},stop(){i=!0,a?.stop()},setOutputVolume(_){if(!Number.isFinite(_)||_<0||_>1)throw RangeError("Listener volume must be 0–1");u=_,a?.setOutputVolume(_)}}}function Kt(t,e={}){let h=At({},e),s=Nt(h.mediaTransport),a=xe(t,h),i=fe(t,h);return{create(m){return Pe(m,s,(u)=>a.create(u),(u)=>i.create(u))}}}function Ee(t){if(t.byteLength<26||t.byteLength>984||t.byteLength%2)throw Error("Invalid listener audio frame");let e=new DataView(t),h=e.getUint32(4,!0);if(e.getUint32(0,!0)!==827085889||h>1)throw Error("Invalid listener audio protocol");let s=Number(e.getBigUint64(8,!0)),a=Number(e.getBigUint64(16,!0));if(!Number.isSafeInteger(s)||!Number.isSafeInteger(a))throw Error("Invalid listener audio timing");let i=new Float32Array((t.byteLength-24)/2);for(let m=0;m<i.length;m++)i[m]=e.getInt16(24+m*2,!0)/32768;return{direction:h,sequence:s,timestampMS:a,frame:i}}function Ce(t,e,h,s){if(t.length<1||t.length>480||!Number.isFinite(s)||s<0||!Number.isInteger(e)||e<1||e>4294967295)throw Error("Invalid coaching capture");let a=new ArrayBuffer(24+t.length*2),i=new DataView(a);i.setUint32(0,826496833,!0),i.setUint32(4,e,!0),i.setUint32(8,h>>>0,!0),i.setFloat64(16,s,!0);for(let m=0;m<t.length;m++){let u=Math.max(-1,Math.min(1,t[m]));i.setInt16(24+m*2,u<0?u*32768:u*32767,!0)}return a}function xe(t,e={}){return{create(h){let s,a,i,m,u=!1,f=[],_=!1,y=0,r=!1,p=!1,S,d,b,g,M,c,V,U=!1,k,L,K=0,ft=!1,v=[0,0],tt=[0,0],j=[void 0,void 0],Mt=[new _t,new _t],n=e.outputVolume??1,q,A=()=>{let R=++y;if(r=!1,p=!1,V?.(R,Error("Coaching cancelled")),c)clearInterval(c);if(c=void 0,M?.port1.close(),M?.port2.close(),M=void 0,d?.disconnect(),d=void 0,b?.disconnect(),b=void 0,g?.disconnect(),g=void 0,S?.getTracks().forEach((Y)=>{Y.onended=null,Y.stop()}),S=void 0,_&&ft&&m?.readyState===WebSocket.OPEN)m.send(JSON.stringify({type:"coach.stop",generation:R}));h.onTalking?.(!1)},w=()=>A(),E=()=>{if(document.visibilityState!=="visible")A()},Q=()=>{if(u)return;if(A(),u=!0,q?.(),window.removeEventListener("blur",w),document.removeEventListener("visibilitychange",E),m)m.onmessage=m.onclose=m.onerror=null,m.close(1000,"listener_shutdown");if(a?.disconnect(),i?.disconnect(),s)s.onstatechange=null,s.close().catch(()=>{});for(let R of f)URL.revokeObjectURL(R)},P=(R)=>{if(!u)Q(),h.onClose(R)},F=()=>{if(u)throw Error("Listening cancelled")};return{async start(R,Y){_=Y?.coaching===!0,F();try{if(s=new AudioContext({latencyHint:"interactive"}),s.state==="suspended")await s.resume();if(F(),e.outputDeviceId&&"setSinkId"in s)await s.setSinkId(e.outputDeviceId);F();let J,yt=new URL(t.mcpURL(),location.href);if(yt.origin===location.origin){let N=`/ui/frontend/listener-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(Ut))),(G)=>G.toString(16).padStart(2,"0")).join("")}.js`,l=await t.get(N,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}});if(F(),l!==Ut)throw Error("Listener audio asset integrity mismatch");let T=new URL(`/api/apps/telephony/_install/${t.installId}${N}`,yt);T.searchParams.set("project_id",t.projectId),T.searchParams.set("install_id",String(t.installId)),J=T.href}else{let X=URL.createObjectURL(new Blob([Ut],{type:"text/javascript"}));f.push(X),J=X}if(await s.audioWorklet.addModule(J),F(),a=new AudioWorkletNode(s,"telephony-listener",{numberOfInputs:0,outputChannelCount:[e.stereo?2:1],processorOptions:{stereo:e.stereo,...D(e)}}),i=s.createGain(),i.gain.value=n,a.connect(i).connect(s.destination),a.onprocessorerror=()=>P("listener_audio_error"),a.port.onmessage=({data:X})=>{if(X?.type==="listener.diagnostics"&&!u)try{h.onDiagnostics?.({...X,media_transport:"websocket",sequence_gaps:[...tt],network_excess_ms:K,network_dropped_ms:[...v]})}catch{}},s.onstatechange=()=>{if(s?.state==="suspended"||s?.state==="interrupted")P("listener_audio_paused")},await new Promise((X,N)=>{let l=!1,T=setTimeout(()=>{G(Error("Listener connection timed out")),P("listener_network_error")},1e4),G=(C)=>{if(l)return;l=!0,clearTimeout(T),q=void 0,C?N(C):X()};q=()=>G(Error("Listening cancelled")),m=new WebSocket(R),m.binaryType="arraybuffer",m.onmessage=({data:C})=>{if(u||!s||!a)return;try{if(typeof C==="string"){let O=JSON.parse(C);if(O.type==="listener.ready"){if(_&&O.coaching!==!0)throw Error("Coaching not authorized");ft=!0,G(),h.onReady("websocket")}else if(O.type==="coach.started")V?.(O.generation);else if(O.type==="coach.rejected")V?.(y,Error(O.detail||"Coaching unavailable"));else if(O.type==="coach.stopped"&&(O.generation===void 0||O.generation===y))A();return}if(!ft||!(C instanceof ArrayBuffer))return;let x=Ee(C),B=x.direction;if(j[B]!==void 0&&x.sequence<j[B])return;if(j[B]!==void 0&&x.sequence>j[B])tt[B]+=x.sequence-j[B];j[B]=x.sequence+1;let z=performance.now()-x.timestampMS;if(L=Math.min(L??z,z),K=Math.max(0,z-L),K>200){v[B]+=x.frame.length*1000/24000;return}k??=s.currentTime*1000+(e.playbackTargetMs??60)-x.timestampMS;let Z=Mt[B].process(x.frame,24000,s.sampleRate);a.port.postMessage({direction:B,frame:Z,playAtMS:x.timestampMS+k},[Z.buffer])}catch{G(Error("Invalid listener media")),P("listener_protocol_error")}},m.onerror=()=>{G(Error("Listener connection failed")),P("listener_network_error")},m.onclose=(C)=>{G(Error(C.reason||"Listener disconnected")),P(C.reason||"listener_disconnected")}}),F(),_)window.addEventListener("blur",w),document.addEventListener("visibilitychange",E)}catch(J){throw Q(),J}},async startTalking(){if(F(),!_||!ft||!s||!m||m.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(r)return;r=!0;let R=++y,Y=()=>!u&&r&&R===y;try{let J=await navigator.mediaDevices.getUserMedia({audio:it({...o,...e})});if(!Y()){J.getTracks().forEach((N)=>N.stop());return}if(S=J,S.getAudioTracks().forEach((N)=>{N.onended=()=>A()}),!U){let N=new URL(t.mcpURL(),location.href),l;if(N.origin===location.origin){let G=`/ui/frontend/worklet-${Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(nt))),(B)=>B.toString(16).padStart(2,"0")).join("")}.js`;if(await t.get(G,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}})!==nt)throw Error("Coaching audio asset integrity mismatch");let x=new URL(`/api/apps/telephony/_install/${t.installId}${G}`,N);x.searchParams.set("project_id",t.projectId),x.searchParams.set("install_id",String(t.installId)),l=x.href}else l=URL.createObjectURL(new Blob([nt],{type:"text/javascript"})),f.push(l);if(!Y())return;await s.audioWorklet.addModule(l),U=!0}if(!Y())return;if(await new Promise((N,l)=>{let T=setTimeout(()=>{V=void 0,l(Error("Coaching activation timed out"))},3000);V=(G,C)=>{if(C||G===R)clearTimeout(T),V=void 0,C?l(C):N()},m.send(JSON.stringify({type:"coach.start",generation:R}))}),!Y())return;d=new AudioWorkletNode(s,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:e.inputGainDB??0,highpassFilter:e.highpassFilter!==!1}}),b=s.createMediaStreamSource(J),g=s.createGain(),g.gain.value=0,b.connect(d).connect(g).connect(s.destination),M=new MessageChannel;let yt=0,X=new _t;M.port1.onmessage=({data:N})=>{if(!Y()||!p||N?.type!=="capture"||!(N.frame instanceof Float32Array)||!s||!m)return;if(!Number.isFinite(N.timestamp_ms)||s.currentTime*1000-N.timestamp_ms>150||m.bufferedAmount>2880||m.readyState!==WebSocket.OPEN)return;let l=X.process(N.frame,N.sample_rate,24000);if(l.length>0&&l.length<=480)m.send(Ce(l,R,yt++,N.timestamp_ms))},M.port1.start(),d.port.postMessage({type:"transport",port:M.port2},[M.port2]),p=!0,h.onTalking?.(!0),c=setInterval(()=>{if(Y()&&m?.readyState===WebSocket.OPEN)m.send(JSON.stringify({type:"coach.keepalive",generation:R}))},1000)}catch(J){if(Y())throw A(),J}},stopTalking:A,stop:Q,setOutputVolume(R){if(!Number.isFinite(R)||R<0||R>1)throw RangeError("Listener volume must be 0–1");if(n=R,i)i.gain.value=R}}}}}class jt{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;audioOptions;constructor(t,e={}){this.client=t;this.options=e;this.audioOptions=At(e.audio,e),this.runtime=e.runtime??Kt(t.app,this.audioOptions)}setAudioOptions(t){let e=At({},t);this.audioOptions=e,this.runtime=this.options.runtime??Kt(this.client.app,e)}getAudioOptions=()=>Object.freeze({...this.audioOptions});getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.observers.add(t),()=>{this.observers.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let e of this.observers)try{e(this.snapshot)}catch{}}async listen(t){return this.begin(t,!1)}async coach(t){return this.begin(t,!0)}async begin(t,e){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=e,this.retryUntil=0,this.retryDelay=500;let h=++this.generation;return this.update({state:"connecting",callId:t,detail:void 0,coaching:e,talking:!1,mediaTransport:void 0}),this.connect(t,h)}async connect(t,e){let h,s=++this.attempt,a=()=>e===this.generation&&s===this.attempt&&!this.disposed;try{if(h=this.coaching?await this.client.coachSession(t):await this.client.listenSession(t),!a()){await this.client.stopListening(h).catch(()=>{});return}this.session=h;let i=this.runtime.create({onReady:(m)=>{if(a())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0,mediaTransport:m})},onClose:(m)=>{if(a())this.disconnected(m,t,e)},onTalking:(m,u)=>{if(a())this.update({talking:m,detail:u})},onDiagnostics:(m)=>{if(a())try{this.options.onDiagnostics?.(m)}catch{}}});if(this.audio=i,this.lease=new wt(h.lease_seconds,()=>this.client.renewListening(h),(m)=>{if(a())this.disconnected(m==="revoked"?"access_revoked":"media_disconnected",t,e,m==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,h.lease_started_ms),await i.start(this.client.listenerMediaURL(h),{coaching:h.coaching===!0}),!a())i.stop()}catch(i){if(!a()||h&&this.session!==h)return;let m=i?.status,u;try{u=JSON.parse(i.body??"{}").code}catch{}let f=u==="call_ended"?"call_ended":m===401||m===403||m===404?"access_revoked":"listener_disconnected";throw this.disconnected(f,t,e,String(i)),i}}cleanup(){this.lease?.stop(),this.lease=void 0;let t=this.audio;this.audio=void 0,t?.stop();let e=this.session;return this.session=void 0,e?this.client.stopListening(e).catch(()=>{}):Promise.resolve()}disconnected(t,e,h,s=t){if(h!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let a=t==="call_ended",i=t==="access_revoked",m=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(t);if(!this.coaching&&!a&&!i&&m&&this.options.reconnect!==!1){if(this.retryUntil||=Date.now()+30000,Date.now()<this.retryUntil){this.update({state:"reconnecting",detail:s,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,h===this.generation&&!this.disposed)this.connect(e,h).catch(()=>{})},this.retryDelay),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:a?"call_ended":i?"access_revoked":"disconnected",detail:s,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(t){this.audio?.setOutputVolume(t)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let t=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1,mediaTransport:void 0}),await t}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}function Re(t){if(!t)return"placing";if(ot(t))return"ended";if(t==="ringing")return"ringing";if(t==="answered"||t==="in-progress")return"connected";return"placing"}var rt=(t)=>t instanceof Error?t.message:String(t);class It{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(t,e={}){this.client=t;this.options=e;if(this.runtime=e.audioRuntime??re(t.app),this.audioOptions={...o,...e.audio,...e.mediaTransport?{mediaTransport:e.mediaTransport}:{}},D(this.audioOptions),this.interval=e.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}createCallListener(t={}){return this.assertOpen(),this.client.createCallListener({...this.audioOptions,...t.audio,...t})}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.assertOpen(),this.listeners.add(t),()=>{this.listeners.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let e of this.listeners)try{e(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(t){if(this.disposed||t!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(t){return!this.disposed&&t===this.generation}finish(t){if(this.current(t))this.update({busy:!1})}cancellable(t,e){let h=this.cancellation.signal;return new Promise((s,a)=>{let i=()=>{h.removeEventListener("abort",i),a(Error("Softphone operation cancelled"))};if(h.addEventListener("abort",i,{once:!0}),t.then(s,a).finally(()=>h.removeEventListener("abort",i)),!this.current(e)||h.aborted)i()})}begin(t){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||t&&this.session)throw Error("Softphone already has an active operation or call");let e=this.invalidate();return this.update({busy:!0,detail:void 0}),e}async dial(t){let e=this.begin(!0),h,s=!1;try{this.assertCurrent(e);let a={to:t.to.trim(),from:t.from?.trim(),timeout_sec:t.timeout_sec,recording:t.recording},i=JSON.stringify(a);if(!this.intent||this.intent.value!==i||t.idempotency_key&&t.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:i,request:{...a,idempotency_key:t.idempotency_key||crypto.randomUUID()}};return await this.cancellable(this.runtime.preflight(this.audioOptions),e),this.assertCurrent(e),h=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(e),s=!0,this.outbound=!0,await this.attachAudio(h,e),h.call_id}catch(a){if(h&&(this.current(e)||!s||this.disposed))await this.recoverStartup(h,e,()=>this.client.hangup(h.call_id));if(this.current(e))this.update({detail:rt(a)});throw a}finally{this.finish(e)}}async answer(t,e={}){let h=this.begin(!0),s,a=!1;try{this.assertCurrent(h),s=await this.client.answer(t,e),this.assertCurrent(h),a=!0,this.outbound=!1,await this.attachAudio(s,h)}catch(i){if(s&&(this.current(h)||!a||this.disposed))await this.recoverStartup(s,h,()=>this.client.release(s));if(this.current(h))this.update({detail:rt(i)});throw i}finally{this.finish(h)}}async recoverStartup(t,e,h){try{if(await h(),this.current(e))this.clearCall()}catch{if(this.current(e))this.session=t,this.update({callId:t.call_id,audioState:"error"}),this.startPolling()}}async join(t){await this.answer(t,{rejoin:!0})}async attach(t,e="attach"){await this.acquireSession(t,!1,e)}async takeover(t){await this.acquireSession(t,!0)}async acquireSession(t,e,h="takeover"){let s=this.begin(!0);try{await this.cancellable(this.runtime.preflight(this.audioOptions),s),this.assertCurrent(s);let a=ct(e?"takeover":h),i=await(e?this.client.takeover(t,a):this.client.attach(t,a));this.assertCurrent(s),await this.attachAudio(i,s),await this.reconcileAttachedCall(i,s)}catch(a){if(this.current(s))this.update({detail:rt(a)});throw a}finally{this.finish(s)}}async reconnect(t,e="manual_reconnect"){if(!this.session)throw Error("No call to reconnect");let h={...this.audioOptions,...t};D(h);let s=this.begin(!1);this.audioOptions=h;let a=this.session.call_id,i=ct(e),m=this.session.session_generation,u=(f,_={})=>this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:f,...i,previous_session_id:m,..._});u("started");try{let f=await this.client.attach(a,{...i,previous_session_id:m});this.assertCurrent(s),await this.attachAudio(f,s),await this.reconcileAttachedCall(f,s),u("connected",{session_id:f.session_generation})}catch(f){if(u(this.current(s)?"failed":"cancelled"),this.current(s))this.update({detail:rt(f)});throw f}finally{this.finish(s)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let t=this.session;if(!t){this.cancellation.abort();return}let e=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let h=(async()=>{try{if(await this.client.hangup(t.call_id),this.current(e))this.clearCall()}catch(s){if(this.current(e))this.update({audioState:"error",detail:rt(s)}),this.startPolling();throw s}finally{this.finish(e)}})();this.hangingUp=h;try{await h}finally{if(this.hangingUp===h)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(t){this.assertOpen();let e=this.session;if(!e)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let h=this.generation;this.update({busy:!0,detail:void 0});let s;try{s=this.client[t](e.call_id)}catch(a){if(this.current(h)&&this.session===e)this.update({busy:!1,detail:rt(a)});throw a}this.controlPending=s;try{let a=await s;if(this.current(h)&&this.session===e)this.update({holdState:a.hold_state,recordingState:a.recording_state,controlError:a.control_error,capabilities:a.capabilities});return a}catch(a){if(this.current(h)&&this.session===e)this.update({detail:rt(a)});throw a}finally{if(this.controlPending===s)this.controlPending=void 0;if(this.current(h)&&this.session===e)this.update({busy:!1})}}configureAudio(t){this.assertOpen();let e={...this.audioOptions,...t};D(e),this.audioOptions=e}setMuted(t){this.assertOpen(),this.audio?.setMuted(t),this.update({muted:t})}setOutputVolume(t){if(this.assertOpen(),!Number.isFinite(t)||t<0||t>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=t,this.audio?.setOutputVolume(t)}sendDTMF(t){if(this.assertOpen(),!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(t)}observeCall(t){if(this.disposed||t.id!==this.session?.call_id)return;if(t.direction)this.outbound=t.direction==="outbound";if(ot(t.status)){this.invalidate(),this.clearCall(t.status),this.update({busy:!1,phase:"ended",termination:t.termination,answeredBy:t.answered_by??this.snapshot.answeredBy,endedAt:t.ended_at});return}this.update({carrierStatus:t.status,phase:Re(t.status),answeredBy:t.answered_by??this.snapshot.answeredBy,holdState:t.hold_state??this.snapshot.holdState,recordingState:t.recording_state??this.snapshot.recordingState,controlError:t.control_error??this.snapshot.controlError,capabilities:t.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let t=this.options.ringback;return typeof t==="object"&&t?t.country:void 0}syncRingback(){let t=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(t&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!t&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(t,e){this.assertCurrent(e),this.stopAudio(),this.session=t,this.update({callId:t.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let h,s=()=>!this.disposed&&h!==void 0&&this.audio===h,a=(u)=>{if(s())try{u()}catch{}},i,m;try{if(this.assertCurrent(e),h=this.runtime.create({refreshMediaURL:(u)=>{let f=u??ct("automatic_retry");if(i)return this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"coalesced",...f,shared_attempt_id:m?.attempt_id}),i;if(!s()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let _=this.generation,y=(async()=>{let p=t.session_generation,S=await this.client.attach(t.call_id,{...f,previous_session_id:p});if(this.assertCurrent(_),!s())throw Error("Audio connection no longer active");return t=S,this.session=S,this.startLease(S),this.reportSessionEvent({timestamp:new Date().toISOString(),action:"media_session",outcome:"replaced",...f,previous_session_id:p,session_id:S.session_generation}),this.client.mediaURL(S)})();i=y,m=f;let r=()=>{if(i===y)i=void 0,m=void 0};return y.then(r,r),y},sessionDiagnostics:()=>({session_id:t.session_generation}),onSessionEvent:(u)=>a(()=>this.options.onSessionEvent?.(lt({...u,session_id:u.session_id??t.session_generation}))),onState:(u,f)=>{if(!s())return;if(u==="ended"&&f==="call.ended"){this.observeCall({id:t.call_id,status:"completed"});return}if(this.update({audioState:u,detail:f}),s()&&(u==="error"||u==="ended")){if(this.stopAudio(),u==="error")this.reconcileFailedAudio(t,this.generation)}},onLevels:(u,f)=>a(()=>this.options.onLevels?.(u,f)),onAudioHealth:(u)=>a(()=>this.options.onAudioHealth?.(u)),onDiagnostics:(u)=>a(()=>this.options.onDiagnostics?.(u)),onNotice:(u)=>a(()=>this.options.onNotice?.(u)),onCallStatus:(u)=>a(()=>this.observeCall({id:u.call_id,status:u.status,direction:u.direction,answered_at:u.answered_at,ended_at:u.ended_at,answered_by:u.answered_by,termination:u.termination,hold_state:u.hold_state,recording_state:u.recording_state,control_error:u.control_error}))}),this.audio=h,this.assertCurrent(e),this.startPolling(),this.startLease(t),h.setMuted(this.snapshot.muted),await this.cancellable(h.start(this.client.mediaURL(t),this.audioOptions),e),this.assertCurrent(e),!s())throw Error(this.snapshot.detail||"Audio connection ended during setup");h.setMuted(this.snapshot.muted),this.syncRingback()}catch(u){if(s())this.stopAudio();if(this.current(e))this.update({audioState:"error",detail:rt(u)});throw u}}async reconcileAttachedCall(t,e){try{let h=await this.client.getCall(t.call_id);if(this.current(e)&&this.session===t&&h)this.observeCall(h)}catch{}}async reconcileFailedAudio(t,e){try{let h=await this.client.getCall(t.call_id);if(!this.current(e)||this.session!==t||!h)return;if(h.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(h)}catch{}}startLease(t){if(this.lease?.stop(),!t.lease_seconds)return;this.lease=new wt(t.lease_seconds,()=>this.client.renew(t),(e,h)=>{if(this.disposed||this.session!==t)return;this.stopAudio(),this.update({audioState:"error",detail:e==="expired"?"Audio session expired. Reconnect audio.":`Audio authorization ended: ${rt(h??e)}`})},(e)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(e);else try{this.options.onSessionEvent?.(e)}catch{}},t.lease_started_ms)}reportSessionEvent(t){if(t=lt(t),this.audio?.recordSessionEvent)try{this.audio.recordSessionEvent(t)}catch{}else try{this.options.onSessionEvent?.(t)}catch{}}stopAudio(){this.lease?.stop(),this.lease=void 0;let t=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{t?.stopRingback?.()}catch{}}try{t?.stop()}catch{}if(t)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let t=new AbortController;this.polling=t;let e=async()=>{let h=this.session?.call_id;if(!h||t.signal.aborted)return;try{let s=await this.client.getCall(h,t.signal);if(!t.signal.aborted&&s)this.observeCall(s)}catch(s){if(!t.signal.aborted)this.update({detail:`Call status unavailable: ${rt(s)}`})}finally{if(!t.signal.aborted&&this.session&&!this.disposed)this.timer=setTimeout(e,this.interval)}};this.timer=setTimeout(e,this.interval)}clearCall(t){this.stopAudio(),this.stopPolling(),this.session=void 0,this.outbound=!1,this.update({callId:void 0,carrierStatus:t,audioState:"idle",muted:!1,detail:void 0,phase:t?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function Te(t,e="en"){if(t?.reason==="ai_inactivity")return e.toLowerCase().startsWith("fr")?"Fin de l’appel : aucune réponse":"Call ended: no response";if(t?.reason==="ai_policy_failure")return e.toLowerCase().startsWith("fr")?"Échec du rappel d’inactivité IA":"AI inactivity reminder failed";if(t?.reason==="time_limit"||t?.reason==="ai_max_duration")return e.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return t?.reason?.replaceAll("_"," ")??""}class _e extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function Fe(t){return t.direction==="inbound"&&t.status==="pending"&&t.answerable!==!1&&!t.routing_waiting&&(t.peer_kind==="human"||Boolean(t.ring_offers?.some((e)=>e.kind==="browser")))}var ot=(t)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(t);function W(t){if(!t||/[\s/\\?#]/.test(t))throw Error("Invalid call ID");return encodeURIComponent(t)}class kt{app;options;listMicrophones=ie;createMicrophonePreview=(t)=>ue(t,this.app);constructor(t,e={}){this.app=t;this.options=e;if(t.name!=="telephony"||!t.projectId||!t.installId)throw Error("Telephony requires an explicit project and installation")}path(t){if(!this.options.authProvider)return t;return`/user${t}${t.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(t){let e=await this.app.get(this.path("/calls"),{signal:t});if(!Array.isArray(e?.calls)||e.calls.some((h)=>!h||typeof h.id!=="string"||typeof h.status!=="string"))throw Error("Invalid Telephony calls response");return e.calls}async getCall(t,e){let h=await this.app.get(this.path(`/calls?call_id=${W(t)}`),{signal:e});if(!Array.isArray(h?.calls))throw Error("Invalid Telephony call response");return h.calls.find((s)=>s.id===t)}incomingCalls(t){return t.filter(Fe)}watchCalls(t,e={}){let h=e.intervalMs??2000;if(!Number.isFinite(h)||h<100)throw Error("Call watch interval must be at least 100 ms");let s=new AbortController,a,i,m=!1,u=!1,f=new Set,_=()=>{clearTimeout(a),s.abort(),i?.close(),e.signal?.removeEventListener("abort",_)},y=(p)=>{try{e.onError?.(p)}catch{_()}};if(e.signal?.addEventListener("abort",_,{once:!0}),e.signal?.aborted)_();let r=async(p)=>{if(s.signal.aborted)return;if(m){u=!0;return}clearTimeout(a),m=!0;let S=performance.now();try{let d=await this.listCalls(s.signal);if(!s.signal.aborted){t(d);for(let b of d){if(b.answerable!==!0||b.status!=="pending")continue;for(let g of b.ring_offers??[]){if(g.kind!=="browser"||!g.id||f.has(g.id))continue;f.add(g.id),this.acknowledgeOffer(b.id,g.id).catch((M)=>{if(![404,405].includes(M?.status??0))f.delete(g.id)})}}try{e.onTiming?.({trigger:p,fetchMs:performance.now()-S})}catch{}}}catch(d){if(!s.signal.aborted){let b=d?.status;try{e.onFailure?.({trigger:p,fetchMs:performance.now()-S,status:typeof b==="number"?b:void 0,error:d})}catch{}y(d)}}finally{if(m=!1,!s.signal.aborted)if(u)u=!1,r("push");else a=setTimeout(()=>void r("poll"),h)}};if(r("poll"),!s.signal.aborted&&e.push!==!1&&typeof this.app.subscribe==="function")try{i=this.app.subscribe(this.path("/calls/events"),(p)=>{if(p.type==="calls.changed")r("push");else if(p.type==="access.revoked")i?.close(),r("push")},{transport:"fetch",signal:s.signal,reconnectDelayMs:250,onError:(p)=>{let S=p?.status;if(S===404||S===405||S===501)i?.close()}})}catch{}return{close:_}}async place(t){if(!/^\+[1-9]\d{7,14}$/.test(t.to)||!t.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let e=at();return this.session(await this.app.post(this.path("/softphone/place"),t),void 0,"media",e)}async answer(t,e={}){let h=at();try{return this.session(await this.app.post(this.path(`/softphone/answer/${W(t)}`),e),t,"media",h)}catch(s){let a=s;if(a.status===409&&typeof a.body==="string"){let i;try{i=JSON.parse(a.body)}catch{}if(i?.code==="offer_expired")throw new _e}throw s}}async acknowledgeOffer(t,e){await this.app.post(this.path(`/softphone/offer/ack/${W(t)}`),{offer_id:e})}async declineOffer(t,e){await this.app.post(this.path(`/softphone/offer/decline/${W(t)}`),{offer_id:e})}async attach(t,e){let h=at();return this.session(await this.app.post(this.path(`/softphone/attach/${W(t)}`),e?{media_diagnostics:e}:{}),t,"media",h)}async takeover(t,e){let h=at();return this.session(await this.app.post(this.path(`/softphone/takeover/${W(t)}`),e?{media_diagnostics:e}:{}),t,"media",h)}createCallListener(t={}){return new jt(this,{...t,...At({...this.options.audio,...t.audio},t)})}async listenSession(t){let e=at();return this.session(await this.app.post(this.path(`/softphone/listen/${W(t)}`),{}),t,"listen-media",e)}async coachSession(t){let e=at(),h=this.session(await this.app.post(this.path(`/softphone/coach/${W(t)}`),{}),t,"listen-media",e);if(h.coaching!==!0)throw Error("Invalid coaching session");return h}async renewListening(t){return this.app.post(this.path(`/softphone/${t.coaching?"coach-renew":"listen-renew"}/${W(t.call_id)}`),{session_token:t.session_token})}async stopListening(t){await this.app.post(this.path(`/softphone/${t.coaching?"coach-stop":"listen-stop"}/${W(t.call_id)}`),{session_token:t.session_token})}async listenerAudit(t){return this.app.get(this.path(`/softphone/listen-audit/${W(t)}`))}outboundTransport(t){return this.app.post(this.path("/numbers/outbound-transport"),t)}outboundNumbers(t={}){return this.app.get(this.path(t.fresh?"/softphone/numbers?fresh=true":"/softphone/numbers"))}async renew(t){return this.app.post(this.path(`/softphone/renew/${W(t.call_id)}`),{session_token:t.session_token})}async release(t){if(!t.session_token)throw Error("Answer session has no release token");await this.app.post(this.path(`/softphone/release/${W(t.call_id)}`),{session_token:t.session_token})}async hangup(t){await this.app.post(this.path(`/calls/${W(t)}/hangup`),{})}hold(t){return this.app.post(this.path(`/calls/${W(t)}/hold`),{})}resume(t){return this.app.post(this.path(`/calls/${W(t)}/resume`),{})}pauseRecording(t){return this.app.post(this.path(`/calls/${W(t)}/pause-recording`),{})}resumeRecording(t){return this.app.post(this.path(`/calls/${W(t)}/resume-recording`),{})}createSoftphone(t={}){return new It(this,{...t,audio:{...this.options.audio,...t.audio}})}mediaURL(t){return this.resolveMediaURL(t,"media")}listenerMediaURL(t){return this.resolveMediaURL(t,"listen-media")}resolveMediaURL(t,e){let h=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),s=new URL(t.media_url,h),a=`/api/apps/telephony/_install/${this.app.installId}/softphone/${e}/${W(t.call_id)}/`;if(s.origin!==h.origin||!["http:","https:"].includes(s.protocol)||s.username||s.password||s.search||s.hash||!s.pathname.startsWith(a)||!/^[A-Za-z0-9_-]+$/.test(s.pathname.slice(a.length)))throw Error("Invalid Telephony media endpoint");if(e==="listen-media"&&s.pathname.slice(a.length)!==t.session_token)throw Error("Listener credential mismatch");if(e==="media"&&pt(t.session_generation))s.searchParams.set("session_generation",t.session_generation);return s.protocol=s.protocol==="https:"?"wss:":"ws:",s.href}session(t,e,h="media",s=at()){let a=t;if(!a||typeof a.call_id!=="string"||typeof a.media_url!=="string"||e&&a.call_id!==e||a.session_token!==void 0&&typeof a.session_token!=="string")throw Error("Invalid Telephony session response");if(a.lease_seconds!==void 0&&(!Number.isFinite(a.lease_seconds)||a.lease_seconds<10||a.lease_seconds>3600||!a.session_token))throw Error("Invalid media lease");if(h==="listen-media"&&(!a.session_token||a.lease_seconds===void 0))throw Error("Invalid listener lease");return W(a.call_id),this.resolveMediaURL(a,h),{...a,lease_started_ms:s}}}var Kh=Wt({app:"telephony",create:({app:t})=>new kt(t)});function oh({app:t},e){return new kt(t,e)}export{oh as createClient,Te as callTerminationLabel};
