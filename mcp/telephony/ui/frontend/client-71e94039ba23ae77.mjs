function Et(t){let h=globalThis.crypto?.randomUUID?.()??`${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;return`${t}-${h}`}function Pt(t){return{recovery_id:Et("recovery"),attempt_id:Et("attempt"),initiating_action:t}}function At(t){return typeof t==="string"&&t.length<=80&&/^(browser|session|recovery|attempt)-[a-f0-9-]{1,64}$/.test(t)?t:void 0}function qt(t){let h=(u,a)=>typeof u==="string"&&u.length<=a&&/^[a-zA-Z0-9_-]*$/.test(u)?u:void 0,e=["WebSocket transport error","WebRTC signaling error","Audio worker failed","WebRTC signaling unavailable"],m=["attach","takeover","automatic_retry","manual_reconnect","audio_device_change","component_recreation"],s=(u,a)=>typeof u==="number"&&Number.isFinite(u)?Math.max(0,Math.min(a,u)):void 0;return{timestamp:Number.isFinite(Date.parse(t.timestamp))?new Date(t.timestamp).toISOString():new Date().toISOString(),action:h(t.action,40)??"diagnostic",outcome:h(t.outcome,40)??"unknown",code:h(t.code,80),detail:t.detail?e.includes(t.detail)?t.detail:"detail_redacted":void 0,recovery_id:At(t.recovery_id),attempt_id:At(t.attempt_id),shared_attempt_id:At(t.shared_attempt_id),session_id:At(t.session_id),previous_session_id:At(t.previous_session_id),initiating_action:m.includes(t.initiating_action)?t.initiating_action:void 0,status:s(t.status,599),remaining_ms:s(t.remaining_ms,3600000),was_clean:typeof t.was_clean==="boolean"?t.was_clean:void 0,duration_ms:s(t.duration_ms,86400000)}}var _t=()=>performance.now();async function pt(t,h,e=5000,m){let s=new AbortController,_,u=new Promise((i,q)=>{_=q}),a=()=>{s.abort(),_(new DOMException("Media request cancelled","AbortError"))};h?.addEventListener("abort",a,{once:!0});let f=setTimeout(a,e);try{if(h?.aborted)return a(),await u;return await Promise.race([t(s.signal).then((i)=>{if(s.signal.aborted)m?.(i);return i}),u])}finally{clearTimeout(f),h?.removeEventListener("abort",a)}}function v(t){let h=t,e=h?.status,m;try{let _=JSON.parse(h?.body??"{}");if(typeof _.code==="string")m=_.code}catch{}let s=m==="media_lease_expired";return{status:e,code:m,expired:s,denied:!s&&[401,403,404,410].includes(e)}}class Ft{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(t,h,e,m,s=_t(),_={now:_t,setTimeout:(u,a)=>setTimeout(u,a),clearTimeout:(u)=>clearTimeout(u)}){this.seconds=t;this.renew=h;this.ended=e;this.event=m;this.clock=_;this.deadline=s+t*1000-1000,this.watchExpiry(),this.schedule(Math.min(t*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(t,h){let{status:e,code:m}=v(h);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:t,status:e,code:m,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(t){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,t))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let t=this.clock.now();try{let h=await Promise.race([this.renew(),new Promise((e,m)=>{this.requestTimer=this.clock.setTimeout(()=>m(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(h?.lease_seconds!==void 0){if(!Number.isFinite(h.lease_seconds)||h.lease_seconds<10||h.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=h.lease_seconds}this.deadline=t+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(h){if(this.stopped)return;let e=v(h);if(e.denied||e.expired){this.finish(e.expired?"expired":"revoked",h);return}this.report("retrying",h),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(t,h){if(this.stopped)return;this.report(t,h),this.stop(),this.ended(t,h)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function Nt(t){let h=new URL(t),e=["localhost","127.0.0.1","[::1]"].includes(h.hostname);if(h.username||h.password||h.search||h.hash||h.protocol!=="https:"&&!(h.protocol==="http:"&&e)||!h.pathname.endsWith("/runtime-browser"))throw Error("Invalid managed Telephony browser endpoint");return h}function ah(t){if(!t.startsWith("/")||t.startsWith("//")||t.includes("..")||t.includes("\\"))throw Error("Invalid Telephony request path")}function ch(t,h){return new Promise((e)=>{let m=()=>{clearTimeout(s),t.removeEventListener("abort",m),e()},s=setTimeout(m,h);if(t.addEventListener("abort",m,{once:!0}),t.aborted)m()})}class kt{proxy;mode;fetchImpl;direct;info;discovery;credential;issuing;proxyOnly=!1;constructor(t,h="auto",e=globalThis.fetch,m){this.proxy=t;this.mode=h;this.fetchImpl=e;this.direct=m;this.fetchImpl=e.bind(globalThis)}get directBase(){return this.info?.browser_api_base}async prepare(){if(this.mode==="proxy"||this.mode==="auto"&&typeof location>"u")return;if(this.direct){Nt(this.direct.baseURL),this.info={managed:!0,browser_api_base:this.direct.baseURL};return}if(this.discovery)return this.discovery;this.discovery=(async()=>{let t;try{t=await this.proxy.get("/runtime-endpoints")}catch(e){if(this.mode==="direct"||![404,405].includes(e.status??0))throw e;this.proxyOnly=!0;return}if(!t?.managed||!t.browser_api_base){if(this.proxyOnly=!0,this.mode==="direct")throw Error("Direct Telephony deployment unavailable");return}if(Nt(t.browser_api_base),t.browser_media_base)Nt(t.browser_media_base);if(t.browser_signaling_base)Nt(t.browser_signaling_base);let h=new URL(this.proxy.mcpURL(),typeof location>"u"?void 0:location.href);if(!["https:","http:"].includes(h.protocol))throw Error("Invalid Telephony management gateway");this.info=t})();try{await this.discovery}catch(t){throw this.discovery=void 0,t}}async grant(){if(this.credential&&this.credential.expires_at*1000>Date.now()+30000)return this.credential;if(this.issuing)return this.issuing;let t=typeof location>"u"?new URL(this.proxy.mcpURL()).origin:location.origin;this.issuing=(async()=>{let h=this.direct?await this.direct.credentialProvider():await this.proxy.post("/runtime-browser-bootstrap",{origin:t});if(!h||typeof h.token!=="string"||!h.token||h.token.length>4096||!Number.isSafeInteger(h.expires_at)||h.expires_at*1000<=Date.now()||h.expires_at*1000>Date.now()+360000||h.endpoint!==this.directBase)throw Error("Invalid managed browser credential");return this.credential=h})();try{return await this.issuing}finally{this.issuing=void 0}}async request(t,h,e,m){if(this.mode==="proxy"||this.proxyOnly||this.mode==="auto"&&typeof location>"u")return t==="GET"?this.proxy.get(h,m):this.proxy.post(h,e,m);if(await this.prepare(),!this.directBase)return t==="GET"?this.proxy.get(h,m):this.proxy.post(h,e,m);ah(h);let s=await this.grant(),_=new URL(this.directBase+h);if(this.proxy.projectId)_.searchParams.set("project_id",this.proxy.projectId);let u=new Headers(m?.headers);u.set("Authorization","Bearer "+s.token),u.set("Content-Type","application/json");let a=await this.fetchImpl(_,{...m,method:t,headers:u,body:e===void 0?void 0:JSON.stringify(e),credentials:"omit",redirect:"error"});if(!a.ok){if(a.status===403)this.credential=void 0;let f=(await a.text()).slice(0,65536);throw Object.assign(Error(`Telephony HTTP ${a.status}`),{status:a.status,body:f})}return await a.json()}mediaURL(t,h){if(!this.directBase)return t;let e=`/api/apps/telephony/_install/${this.proxy.installId}`,m=h==="webrtc"||h==="auto"?this.info?.browser_signaling_base:this.info?.browser_media_base,s=new URL((m||this.directBase)+t.pathname.slice(e.length));return s.search=t.search,s}subscribe(t,h,e){let m=new AbortController,s,_=()=>{m.abort(),s?.close(),e?.signal?.removeEventListener("abort",_)};if(e?.signal?.addEventListener("abort",_,{once:!0}),e?.signal?.aborted)_();return(async()=>{try{if(await this.prepare(),m.signal.aborted)return;if(!this.directBase){s=this.proxy.subscribe(t,h,e);return}ah(t);let u=500;while(!m.signal.aborted){try{let a=await this.grant(),f=new URL(this.directBase+t);if(this.proxy.projectId)f.searchParams.set("project_id",this.proxy.projectId);let i=await this.fetchImpl(f,{headers:{Authorization:"Bearer "+a.token,Accept:"text/event-stream"},credentials:"omit",redirect:"error",signal:m.signal});if(!i.ok||!i.body){if(i.status===403)this.credential=void 0;throw Object.assign(Error(`Telephony stream HTTP ${i.status}`),{status:i.status})}u=500;let q=i.body.getReader(),M=new TextDecoder,g="";try{while(!m.signal.aborted){let{done:P,value:C}=await q.read();if(P)break;if(g+=M.decode(C,{stream:!0}),g=g.replace(/\r\n/g,`
`),g.length>65536)throw Error("Telephony notification frame too large");let w;while((w=g.indexOf(`

`))>=0){let b=g.slice(0,w);g=g.slice(w+2);let y=b.split(`
`).filter((r)=>r.startsWith("data:")).map((r)=>r.slice(5).trimStart()).join(`
`);if(y)try{h(JSON.parse(y))}catch{}}}}finally{await q.cancel().catch(()=>{})}}catch(a){if(m.signal.aborted)return;e?.onError?.(a),u=Math.min(1e4,u*2)}if(!m.signal.aborted)await ch(m.signal,u)}}catch(u){if(!m.signal.aborted)e?.onError?.(u)}})(),{close:_}}}function Dt(t){return t}var xt="0.12.23";var ph=["rtt_ms","queue_ms","target_ms","buffered_bytes","underruns","dropped_ms","capture_sequence_gaps","playback_sequence_gaps","main_thread_max_pause_ms","worker_max_tick_gap_ms","capture_sent_ms","capture_muted_ms","capture_dropped_ms","playback_ingress_ms","playback_transport_dropped_ms","playback_source_dropped_ms","clock_uncertainty_ms","clock_sample_age_ms","stats_errors","stats_duration_ms","send_bitrate_bps","receive_bitrate_bps","receive_gap_ms","capture_age_ms","transit_ms","delivery_excess_ms","server_queue_ms","receiver_concealed_delta_ms","receiver_silent_concealed_delta_ms","receiver_concealed_window_ms","requested_playback_target_ms","requested_upload_bitrate_bps","receiver_ssrc","receiver_loss_delta","receiver_loss_window_ms","remote_receiver_ssrc","remote_receiver_loss_delta","remote_receiver_loss_window_ms","receiver_bytesReceived","receiver_packetsReceived","receiver_packetsLost","receiver_jitter","receiver_packetsDiscarded","receiver_concealedSamples","receiver_silentConcealedSamples","receiver_concealmentEvents","receiver_insertedSamplesForDeceleration","receiver_removedSamplesForAcceleration","receiver_totalSamplesReceived","receiver_audioLevel","receiver_totalAudioEnergy","receiver_totalSamplesDuration","receiver_nackCount","receiver_fecPacketsReceived","receiver_fecPacketsDiscarded","receiver_jitter_buffer_interval_ms","receiver_jitter_target_interval_ms","receiver_jitter_minimum_interval_ms","receiver_processing_interval_ms","receiver_totalProcessingDelay","receiver_totalSamplesDecoded","telemetry_skipped_samples","telemetry_skipped_auxiliary","telemetry_queue_age_ms","receiver_last_packet_age_ms","sender_bytesSent","sender_packetsSent","sender_headerBytesSent","sender_retransmittedPacketsSent","sender_retransmittedBytesSent","sender_nackCount","sender_targetBitrate","sender_send_delay_interval_ms","remote_receiver_packetsLost","remote_receiver_fractionLost","remote_receiver_jitter","remote_receiver_roundTripTime","remote_receiver_totalRoundTripTime","remote_receiver_roundTripTimeMeasurements","remote_sender_packetsSent","remote_sender_bytesSent","pair_availableOutgoingBitrate","pair_availableIncomingBitrate","pair_currentRoundTripTime","pair_totalRoundTripTime","pair_bytesSent","pair_bytesReceived","pair_requestsSent","pair_requestsReceived","pair_responsesSent","pair_responsesReceived","pair_consentRequestsSent","path_revision","codec_clock_rate","codec_channels"],Fh={receiver_processing:["unavailable","per_decoded_sample"],connection:["connected","reconnecting","closed"],context:["running","suspended","interrupted","closed"],muted:["true","false"],device_muted:["true","false"],track:["live","ended"],ice:["new","checking","connected","completed","disconnected","failed","closed"],dtls:["new","connecting","connected","closed","failed"],pair:["frozen","waiting","in-progress","failed","succeeded"],local_candidate:["host","srflx","prflx","relay"],remote_candidate:["host","srflx","prflx","relay"],protocol:["udp","tcp"],relay_protocol:["udp","tcp","tls"],codec:["audio/opus","audio/PCMU","audio/PCMA","pcm16"]};function Kt(t){let h={};for(let e of ph)if(typeof t[e]==="number"&&Number.isFinite(t[e]))h[e]=Math.max(0,Math.min(t[e],1000000000000));return h}function jt(t){let h={};for(let[e,m]of Object.entries(Fh))if(typeof t[e]==="string"&&m.includes(t[e]))h[e]=t[e];return h}class Tt{transport;serial=0;lastEmit=-1/0;previousAt;previous;incident;history=[];pending=[];counters={};constructor(t){this.transport=t}observe(t,h,e=performance.now(),m=new Date().toISOString(),s){if(!Number.isFinite(e)||!Number.isFinite(Date.parse(m)))return;if(this.previousAt!==void 0&&e<this.previousAt)this.lastEmit=-1/0,this.previous=void 0,this.counters={},this.incident=void 0;let _=Kt(t),u=jt(h),a=!1;for(let i of["underruns","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealmentEvents","path_revision","stats_errors"])if(_[i]!==void 0){if(this.counters[i]!==void 0&&_[i]>this.counters[i])a=!0;this.counters[i]=_[i]}let f={id:`s${++this.serial}`,timestamp:m,transport:this.transport,reason:a?"incident":"periodic",window_ms:Math.min(60000,Math.max(0,typeof s==="number"&&Number.isFinite(s)?s:this.previousAt===void 0?0:e-this.previousAt)),metrics:_,states:u};if(this.previousAt=e,a&&!this.incident)this.incident={sample:f,context:this.previous};if(e-this.lastEmit>=5000){if(this.incident){if(this.incident.context)this.enqueue({...this.incident.context,reason:"incident_context"});this.enqueue(this.incident.sample),this.incident=void 0}else this.enqueue(f);this.lastEmit=e}this.previous=f}enqueue(t){if(this.history.push(t),this.history.length>24)this.history.shift();if(this.pending.push(t),this.pending.length>8)this.pending.shift()}recent(){return this.history.map((t)=>({...t,metrics:{...t.metrics},states:{...t.states}}))}drain(){return this.pending.splice(0,2)}}function ih(t){let h={...t,metrics:{},states:{},part_index:0,part_count:16},e=[],m=h;for(let s of["metrics","states"])for(let[_,u]of Object.entries(t[s])){let a={...m,[s]:{...m[s],[_]:u}};if(JSON.stringify(a).length>768)e.push(m),m={...h,metrics:{},states:{},[s]:{[_]:u}};else m=a}if(e.push(m),e.length>16)return[];return e.map((s,_)=>({...s,part_index:_,part_count:e.length}))}var Th=["receiver_loss_delta","remote_receiver_loss_delta","receiver_concealed_delta_ms","receiver_ssrc","remote_receiver_ssrc","rtt_ms","queue_ms","target_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealedSamples","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_jitter","buffered_bytes","main_thread_max_pause_ms","worker_max_tick_gap_ms","receiver_processing_interval_ms","path_revision","stats_errors"];function lh(t){let h={};for(let e of Th)if(t.metrics[e]!==void 0){if(h[e]=t.metrics[e],ih({...t,metrics:h}).length>2)delete h[e]}return{...t,metrics:h}}class wt{send;sendReport;now;auxiliary=[];skippedAuxiliary=0;skippedSamples=0;auxiliaryTurn=!1;pending=[];queuedAt=new Map;lastFull=-1/0;timer;report;reportAt=0;static maxAgeMS=12000;constructor(t,h,e=()=>performance.now()){this.send=t;this.sendReport=h;this.now=e}enqueueReport(t){if(this.report!==void 0)this.skippedAuxiliary++;this.report=t,this.reportAt=this.now(),this.start()}enqueueAuxiliary(t){let h=!!t?.diagnostics?.timing?.runtime;if(h){let e=this.auxiliary.length;this.auxiliary=this.auxiliary.filter((m)=>!m.runtime),this.skippedAuxiliary+=e-this.auxiliary.length}if(this.auxiliary.push({report:t,at:this.now(),runtime:h}),this.auxiliary.length>16)this.auxiliary.shift(),this.skippedAuxiliary++;this.start()}start(){if(!this.timer)this.timer=setInterval(()=>this.tick(),250)}discard(t){this.pending=this.pending.filter((h)=>h.id!==t),this.queuedAt.delete(t),this.skippedSamples++}enqueue(t){let h=this.now(),e=[...t].reverse().find((m)=>m.reason==="incident")??t.at(-1);if(this.skippedSamples+=Math.max(0,t.length-(e?1:0)),e){let m=e.reason==="periodic"&&h-this.lastFull>=30000;if(m)this.lastFull=h;let s=m?e:lh(e);s={...s,metrics:{...s.metrics,telemetry_skipped_samples:this.skippedSamples,telemetry_skipped_auxiliary:this.skippedAuxiliary}};let _=ih(s),u=this.pending[0]?.id;for(let a of this.queuedAt.keys())if(a!==u)this.discard(a);if(this.queuedAt.has(s.id))this.discard(s.id);if(_.length)this.pending.push(..._),this.queuedAt.set(s.id,h)}if(this.pending.length)this.start()}tick(){let t=this.now();for(let[h,e]of this.queuedAt)if(t-e>wt.maxAgeMS)this.discard(h);if(this.auxiliary=this.auxiliary.filter((h)=>{if(t-h.at>wt.maxAgeMS)return this.skippedAuxiliary++,!1;return!0}),this.report!==void 0&&t-this.reportAt>wt.maxAgeMS)this.report=void 0,this.skippedAuxiliary++;try{if(this.report!==void 0&&this.sendReport){if(this.sendReport(this.report,Math.max(0,t-this.reportAt)))this.report=void 0}else if(this.auxiliary.length&&this.sendReport&&(!this.pending.length||this.auxiliaryTurn)){if(this.sendReport(this.auxiliary[0].report,Math.max(0,t-this.auxiliary[0].at)))this.auxiliary.shift(),this.auxiliaryTurn=!1}else if(this.pending.length){let h=this.pending[0],e={...h.metrics,telemetry_queue_age_ms:Math.max(0,t-(this.queuedAt.get(h.id)??t))};if(this.send({...h,metrics:e})){if(this.pending.shift(),h.part_index===h.part_count-1)this.queuedAt.delete(h.id);this.auxiliaryTurn=!0}}}catch{}if(!this.pending.length&&!this.auxiliary.length&&this.report===void 0)clearInterval(this.timer),this.timer=void 0}stop(){clearInterval(this.timer),this.timer=void 0,this.pending=[],this.queuedAt.clear(),this.auxiliary=[],this.report=void 0}}class lt{emit;now;timestamp;lastTick;suspendedAt;contextState;environmentCleanup=[];counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(t,h=()=>performance.now(),e=()=>new Date().toISOString()){this.emit=t;this.now=h;this.timestamp=e}observeEnvironment(){this.stopEnvironment();let t=(h,e,m)=>{if(!h)return;try{h.addEventListener(e,m),this.environmentCleanup.push(()=>h.removeEventListener(e,m))}catch{}};try{if(typeof document<"u"){t(document,"visibilitychange",()=>this.report("tab_visibility",document.visibilityState));for(let h of["freeze","resume"])t(document,h,()=>this.report("page_lifecycle",h));this.report("tab_visibility",document.visibilityState)}if(typeof window<"u")for(let h of["pagehide","pageshow"])t(window,h,()=>this.report("page_lifecycle",h));if(typeof navigator<"u")t(navigator.mediaDevices,"devicechange",()=>this.report("microphone","devices_changed"))}catch{}}stopEnvironment(){for(let t of this.environmentCleanup.splice(0))try{t()}catch{}}tick(){let t=this.now();if(this.lastTick!==void 0){let h=Math.max(0,t-this.lastTick-1000);if(h>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,h),this.report("main_thread","scheduling_gap",h)}this.lastTick=t}context(t){if(t===this.contextState)return;this.contextState=t;let h=this.now();if(t==="suspended"||t==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=h,this.counters.audio_context_suspend_count++;this.report("audio_context",t)}else{let e;if(this.suspendedAt!==void 0)e=Math.max(0,h-this.suspendedAt),this.counters.audio_context_suspended_ms+=e,this.suspendedAt=void 0;this.report("audio_context",t,e)}}report(t,h,e){try{this.emit({timestamp:this.timestamp(),action:t,outcome:h,duration_ms:e===void 0?void 0:Math.round(e)})}catch{}}}async function k(t,h,e){let m=performance.now(),s=(_)=>{try{h?.({timestamp:new Date().toISOString(),action:t,outcome:_,duration_ms:Math.round(Math.max(0,performance.now()-m))})}catch{}};s("started");try{let _=await e();return s("completed"),_}catch(_){throw s("failed"),_}}var Mh=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function Vt(t){let h=(t??"FR").trim().toUpperCase();return Mh[h]??Mh.FR}function Oh(t,h){let e=[];if(!(t.cadence.reduce((_,u)=>_+u,0)>0)||!(h>0))return e;let s=0;while(s<h)for(let _=0;_<t.cadence.length;_+=2){let u=t.cadence[_]??0,a=t.cadence[_+1]??0;if(s>=h)break;if(u>0)e.push({start:s,end:Math.min(s+u,h)});s+=u+a}return e}function $t(t,h,e,m=0.12){let s=t.createGain();s.gain.value=0,s.connect(h);let _=m/Math.max(1,e.frequencies.length),u=e.frequencies.map((P)=>{let C=t.createOscillator();return C.type="sine",C.frequency.value=P,C.connect(s),C.start(),C}),a=t.currentTime+0.05,f=0,i=0.01,q=()=>{let P=t.currentTime-a+10;if(P<=f)return;for(let C of Oh(e,P)){if(C.end<=f)continue;let w=a+Math.max(C.start,f),b=a+C.end;s.gain.setValueAtTime(0,w),s.gain.linearRampToValueAtTime(_,w+i),s.gain.setValueAtTime(_,Math.max(w+i,b-i)),s.gain.linearRampToValueAtTime(0,b)}f=P};q();let M=setInterval(q,4000),g=!1;return()=>{if(g)return;g=!0,clearInterval(M);try{s.gain.cancelScheduledValues(0)}catch{}s.gain.value=0;for(let P of u){try{P.stop()}catch{}P.disconnect()}s.disconnect()}}var ft=24000,Ot=60;async function Sh(t,h){try{await t.audioWorklet.addModule(h)}catch(e){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:e})}}function Qh(t){let h=new Int16Array(t.length);for(let e=0;e<t.length;e++){let m=Math.max(-1,Math.min(1,t[e]));h[e]=m<0?m*32768:m*32767}return h.buffer}class gt{history=new Float32Array(64);phase=0;process(t,h,e){if(h===e)return t;let m=new Float32Array(64+t.length);m.set(this.history),m.set(t,64);let s=h/e,_=Math.min(1,e/h)*0.9,u=[],a=this.phase;for(;a<t.length;a+=s){let f=a+32,i=0,q=0;for(let M=Math.ceil(f-32);M<=Math.floor(f+32);M++){let g=M-f,C=(Math.abs(g)<0.000000001?_:Math.sin(Math.PI*_*g)/(Math.PI*g))*(0.5+0.5*Math.cos(Math.PI*g/32));if(M>=0&&M<m.length)i+=m[M]*C,q+=C}u.push(q?i/q:0)}return this.phase=a-t.length,this.history=m.slice(-64),new Float32Array(u)}}function Gh(t){let h=0;for(let e=0;e<t.length;e++)h+=t[e]*t[e];return Math.sqrt(h/Math.max(1,t.length))}function yh(t,h){let e=new Map(t.map((m)=>[m.id,m]));for(let m of h){let s=e.get(m.id);if(!s||(!s.complete||m.complete)&&(!s.ended_at||!!m.ended_at)&&m.duration_ms>=s.duration_ms)e.set(m.id,{...m})}return[...e.values()].sort((m,s)=>m.started_at.localeCompare(s.started_at)).slice(-100)}function Bh(t,h){return t.map((e)=>e.ended_at?e:{...e,ended_at:e.observed_until,end_reason:h,complete:!1})}var o={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function I(t){if(t.mediaTransport!==void 0&&!["websocket","webrtc","auto"].includes(t.mediaTransport))throw RangeError("Unsupported softphone media transport");if(t.webrtcFec!==void 0&&typeof t.webrtcFec!=="boolean")throw RangeError("webrtcFec must be boolean");let h=t.playbackTargetMs??Ot,e=t.playbackMinMs??Math.min(Ot,h),m=t.playbackMaxMs??280;for(let s of[h,e,m])if(!Number.isFinite(s)||s<40||s>280)throw RangeError("Playback buffers must be between 40 and 280 ms");if(e>h||h>m)throw RangeError("Playback buffers require min <= target <= max");if(t.playbackAdaptive!==void 0&&typeof t.playbackAdaptive!=="boolean")throw RangeError("playbackAdaptive must be boolean");return{initialTargetMs:h,minTargetMs:e,maxTargetMs:m,hardMaxMs:320,adaptiveReserve:t.playbackAdaptive!==!1}}function at(t){return{...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{},echoCancellation:t.echoCancellation,noiseSuppression:t.noiseSuppression,autoGainControl:t.autoGainControl}}function qh(t){let h=t.getSettings();return{deviceLabel:t.label||"Default microphone",sampleRate:typeof h.sampleRate==="number"?h.sampleRate:null,channelCount:typeof h.channelCount==="number"?h.channelCount:null,echoCancellation:typeof h.echoCancellation==="boolean"?h.echoCancellation:null,noiseSuppression:typeof h.noiseSuppression==="boolean"?h.noiseSuppression:null,autoGainControl:typeof h.autoGainControl==="boolean"?h.autoGainControl:null}}function Rt(t){if(!Number.isFinite(t)||t<=0)return null;return 20*Math.log10(t)}function Uh(t,h,e){let m=new ArrayBuffer(44+h*2),s=new DataView(m),_=(a,f)=>{for(let i=0;i<f.length;i++)s.setUint8(a+i,f.charCodeAt(i))};_(0,"RIFF"),s.setUint32(4,36+h*2,!0),_(8,"WAVE"),_(12,"fmt "),s.setUint32(16,16,!0),s.setUint16(20,1,!0),s.setUint16(22,1,!0),s.setUint32(24,e,!0),s.setUint32(28,e*2,!0),s.setUint16(32,2,!0),s.setUint16(34,16,!0),_(36,"data"),s.setUint32(40,h*2,!0);let u=44;for(let a of t)for(let f=0;f<a.length;f++)s.setInt16(u,a[f],!0),u+=2;return new Blob([m],{type:"audio/wav"})}class dt{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new gt;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(t,h=!0){this.onLevel=t;this.recordAudio=h}async start(t,h){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:at(h)}),this.ensureOpen();let e=this.stream.getAudioTracks()[0];if(!e)throw Error("No microphone audio track was returned.");this.settings=qh(e);try{this.ctx=new AudioContext({sampleRate:ft,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await Sh(this.ctx,t),this.ensureOpen();let m=this.ctx.sampleRate,s=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:h.inputGainDB,highpassFilter:h.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(_)=>{if(this.stopped)return;if(!(_.data instanceof Float32Array)){if(_.data.type==="capture.stats")this.peak=Math.max(this.peak,_.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,_.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,_.data.limiter_reduction_db??0);return}let u=_.data;if(m!==ft)u=this.resampler.process(u,m,ft);let a=Gh(u);if(this.onLevel?.(a),this.recordAudio)this.frames.push(new Int16Array(Qh(u)));if(this.samples+=u.length,a>=0.005){for(let f=0;f<u.length;f++)this.activeSquares+=u[f]*u[f];this.activeSamples+=u.length}},s.connect(this.capture),this.settings}catch(e){throw await this.release(),e}}async stop(){this.stopped=!0;let t=this.settings,h=this.frames,e=this.samples,m=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,s=this.peak;if(await this.release(),!t||e===0)throw Error("No microphone audio was captured.");return{audio:Uh(h,e,ft),durationMs:Math.round(e*1000/ft),sampleRate:ft,activeRmsDbfs:Rt(m),peakDbfs:Rt(s),postPeakDbfs:Rt(this.postPeak||s),limiterReductionDb:this.limiterReductionDB,settings:t}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let t of this.stream?.getTracks()??[])t.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class It{callbacks;clientEpoch=crypto.randomUUID();transportTelemetry=new Tt("websocket");transportSender=new wt((t)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[t]}})}),!0},(t)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify(t)}),!0});worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new lt((t)=>this.recordSessionEvent(t));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;sessionReady=!1;mediaPathReady;mediaRevision=0;callAnswered=!1;playbackHeld=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={mediaTransport:"websocket",codec:"pcm16",rttMs:null,queueMs:0,targetMs:Ot,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:ft,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:o.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t={}){this.callbacks=t}get isMuted(){return this.muted}async start(t,h,e,m=o){let s=I(m);this.diagnostics.targetMs=s.initialTargetMs,this.callbacks.onState?.("connecting"),this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+xt.replaceAll(".","_")}),this.runtimeTelemetry.observeEnvironment(),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{this.stream=await k("microphone_device",(f)=>this.recordSessionEvent(f),()=>navigator.mediaDevices.getUserMedia({audio:at(m)})),this.ensureOpen();let _=this.stream.getAudioTracks()[0];if(!_)throw Error("No microphone audio track was returned.");if(_.readyState==="ended")throw Error("Microphone disconnected before audio setup.");_.onmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser.")},_.onunmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),this.callbacks.onNotice?.("Microphone input restored.")},_.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let u=qh(_);this.diagnostics={...this.diagnostics,microphoneSampleRate:u.sampleRate??0,microphoneChannelCount:u.channelCount??0,echoCancellation:u.echoCancellation,noiseSuppression:u.noiseSuppression,autoGainControl:u.autoGainControl,micInputGainDb:m.inputGainDB};try{this.ctx=new AudioContext({sampleRate:ft,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await k("audio_context",(f)=>this.recordSessionEvent(f),()=>this.ctx.resume());if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),m.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(m.outputDeviceId),this.ensureOpen();await k("audio_worklet",(f)=>this.recordSessionEvent(f),()=>Sh(this.ctx,h)),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let a=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:m.inputGainDB,highpassFilter:m.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:{...s,telemetryEpoch:this.clientEpoch,telemetryEnabled:!1}}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),a.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=m.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await k("audio_transport",(f)=>this.recordSessionEvent(f),()=>this.openWorker(t,e)),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.setPlaybackObservation(this.ctx?.state==="running"&&this.microphoneTransportReady&&!this.playbackHeld,"audio_context_paused"),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running")this.updateLiveState()},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(_){if(this.closed)this.teardown();if(!this.closed)this.fail(_ instanceof Error?_.message:"browser audio setup failed");throw _}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){await this.ctx?.resume(),this.updateLiveState()}setOutputVolume(t){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,t))}sendDTMF(t){if(/^[0-9*#]+$/.test(t))this.sendText(JSON.stringify({type:"dtmf",digits:t}))}startRingback(t){if(this.closed||!this.ctx||this.ringback)return;this.ringback=$t(this.ctx,this.ctx.destination,Vt(t))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(t)=>{let h=t.data;if(h?.type!=="capture.stats")return;this.micLevel=this.muted?0:h.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:Rt(h.active_rms??0),micPeakDbfs:Rt(h.pre_peak??0),micPostPeakDbfs:Rt(h.post_peak??0),micInputGainDb:h.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:h.limiter_reduction_db??0}},this.playback.port.onmessage=(t)=>{let h=t.data;if(h?.type==="playback.underrun"){this.diagnostics.underrunEvents=yh(this.diagnostics.underrunEvents??[],[h.event]),this.diagnostics.underrunDurationMs=h.underrun_ms,this.diagnostics.underruns=h.underruns;return}if(h?.type!=="stats")return;this.diagnostics.playbackEvents=h.playback_events,this.playbackTiming={reserve_expanded_ms:h.reserve_expanded_ms,reserve_compressed_ms:h.reserve_compressed_ms,reserve_adjustments:h.reserve_adjustments,reserve_match_rejections:h.reserve_match_rejections,played_ms:h.played_ms,max_residence_ms:h.max_residence_ms,drop_totals_ms:h.drop_totals_ms,coaching:{played_ms:h.whisper_played_ms,dropped_ms:h.whisper_dropped_ms,max_queue_ms:h.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,h.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:h.whisper_played_ms??0,coachingDroppedMs:h.whisper_dropped_ms??0,coachingMaxQueueMs:h.whisper_max_queue_ms??0,queueMs:h.queue_ms??0,targetMs:h.target_ms??Ot,underruns:h.underruns??0,droppedMs:h.dropped_ms??0,underrunDurationMs:h.underrun_ms??0,underrunEvents:yh(this.diagnostics.underrunEvents??[],h.underrun_events??[]),maxQueueMs:h.max_queue_ms??0,playbackSequenceGaps:h.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,h.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(t,h){return new Promise((e,m)=>{let s=new Worker(h);this.worker=s;let _=new MessageChannel,u=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:_.port1},[_.port1]),this.playback?.port.postMessage({type:"transport",port:u.port1},[u.port1]);let a=!1,f=setTimeout(()=>{if(a)return;a=!0,m(Error("audio connection timed out"))},1e4),i=(q)=>{if(a)return;if(a=!0,clearTimeout(f),q)m(q);else e()};this.cancelWorkerStart=()=>i(Error("audio session closed")),s.onmessage=(q)=>{if(this.closed){i(Error("audio session closed"));return}let M=q.data;if(M?.type==="socket.open")this.reportedPlaybackIDs.clear(),this.reportedCaptureQueueIDs.clear(),this.opened=!0,this.mediaSocketConnected=!0,this.mediaPathReady=void 0,this.mediaRevision=0,this.sessionReady=!1,this.startRTTProbe(),i();else if(M?.type==="runtime.event")this.recordSessionEvent(M.event);else if(M?.type==="socket.message")this.handleControl(M.data);else if(M?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL(M.recovery).then((g)=>{if(!this.closed&&this.worker===s)s.postMessage({type:"socket.credentials",id:M.id,mediaURL:g})},(g)=>{if(this.closed||this.worker!==s)return;let P=v(g);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:P.denied?"revoked":"retrying",...M.recovery,status:P.status,code:P.code}),s.postMessage({type:"socket.credentials",id:M.id,denied:P.denied})})}else if(M?.type==="socket.error")this.recordSessionEvent({timestamp:M.timestamp,action:"websocket",outcome:"transport_error"});else if(M?.type==="socket.close")if(this.recordSessionEvent({timestamp:M.timestamp,action:"websocket",outcome:"closed",code:String(M.code),detail:M.reason,was_clean:M.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"transport_disconnected"),this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else i(Error("audio connection closed before it was ready"));else if(M?.type==="socket.failed")this.mediaSocketConnected=!1,i(Error(M.detail||"audio connection lost")),this.fail(M.detail||"audio connection lost",M.recoverable===!0);else if(M?.type==="transport.drop"&&M.event)this.diagnostics.dropEvents=this.mergeDropEvents(M.event);else if(M?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=M.buffered_bytes??0,this.diagnostics.captureQueueEvents=M.capture_queue_events,this.transportTiming=M.timing??{},M.observation)this.transportTelemetry.observe({...this.transportTiming,...M.observation,rtt_ms:M.timing?.rtt_ms,queue_ms:this.diagnostics.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:M.buffered_bytes,underruns:this.diagnostics.underruns,dropped_ms:this.diagnostics.droppedMs,capture_sequence_gaps:this.diagnostics.captureSequenceGaps,playback_sequence_gaps:this.diagnostics.playbackSequenceGaps,main_thread_max_pause_ms:this.runtimeTelemetry.counters.main_thread_max_pause_ms},{connection:this.mediaSocketConnected?"connected":"reconnecting",context:this.ctx?.state,muted:String(this.muted),device_muted:String(this.stream?.getAudioTracks()[0]?.muted),track:this.stream?.getAudioTracks()[0]?.readyState,codec:"pcm16"},performance.now(),M.observation?.timestamp,M.observation?.window_ms);if(this.diagnostics.transportSamples=this.transportTelemetry.recent(),typeof M.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(M.timing.rtt_ms)}},s.onerror=(q)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:q.message?.slice(0,160)}),i(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},s.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:t,contextRate:this.ctx?.sampleRate??ft,muted:this.muted,capturePort:_.port2,playbackPort:u.port2},[_.port2,u.port2])})}mergeDropEvents(t,h){if(t)this.workerDropEvents=[...this.workerDropEvents,t].slice(-100);if(h)this.playbackDropEvents=h.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((e,m)=>e.timestamp.localeCompare(m.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;shutdownIntent="session_cleanup";reportedPlaybackIDs=new Set;reportedCaptureQueueIDs=new Set;handleControl(t){if(this.closed)return;try{let h=JSON.parse(t);if(h.type==="media.ready"||h.type==="media.not_ready"){let e=h;if(!Number.isSafeInteger(e.revision)||e.revision<=this.mediaRevision)return;this.mediaRevision=e.revision,this.mediaPathReady=e.type==="media.ready";try{this.callbacks.onMediaReadiness?.(e)}catch{}if(!this.mediaPathReady&&this.callAnswered)this.callbacks.onState?.("reconnecting","Connecting carrier audio…");this.updateLiveState()}else if(h.type==="ready")this.sessionReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState();else if(h.type==="dtmf.error"||h.type==="dtmf.sent")this.callbacks.onNotice?.(h.type==="dtmf.sent"?"Keypad tone sent":h.detail||"Keypad tone failed");else if(h.type==="pong"){if(this.diagnostics.captureSequenceGaps=h.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof h.nonce==="number"&&h.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-h.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(h.type==="call.ended"||h.type==="session.replaced"){this.setPlaybackObservation(!1,h.type==="call.ended"?"call_ended":"observation_ended"),this.shutdownIntent=h.type==="call.ended"?"call_ended":"session_replaced",this.closed=!0;try{this.callbacks.onState?.("ended",h.type)}finally{this.teardown()}}else if(h.type==="call.status"&&typeof h.call_id==="string"&&typeof h.status==="string"){if(h.hold_state)this.playbackHeld=h.hold_state!=="active",this.setPlaybackObservation(!this.playbackHeld&&this.microphoneTransportReady&&this.ctx?.state==="running",this.playbackHeld?"hold":"observation_resumed");if(h.hold_state&&h.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callAnswered=["answered","in-progress"].includes(h.status),this.updateLiveState(),this.callbacks.onCallStatus?.(h)}else if(h.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:h.detail?.slice(0,160)}),this.fail(h.detail||"The call could not be connected.");else if(h.type==="coach.state")this.callbacks.onNotice?.(h.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(h.type==="audio.health"){let e=h;if(e.state!=="healthy"&&e.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:e.state,reason:e.reason,stages:e.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),e.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&e.state==="healthy"&&e.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(h.type==="media.delivery"){let e=h.state;if(e==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(e==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=e==="stalled"}else if(h.type==="peer.disconnected")this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"carrier_disconnected"),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(h.type==="peer.connected")this.microphoneTransportReady=!0,this.setPlaybackObservation(this.ctx?.state==="running"&&!this.playbackHeld,"carrier_connected"),this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState()}catch{}}updateLiveState(){if(this.sessionReady&&this.callAnswered&&this.microphoneTransportReady&&this.mediaPathReady!==!1&&this.ctx?.state==="running")this.callbacks.onState?.("live")}startRTTProbe(){this.stopRTTProbe();let t=()=>{this.sendDiagnostics()};t(),this.pingTimer=setInterval(t,5000)}sendText(t){this.worker?.postMessage({type:"send.text",data:t})}recordSessionEvent(t){try{t={...this.callbacks.sessionDiagnostics?.(),...t}}catch{}t=qt(t),this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],t].slice(-50);try{this.callbacks.onSessionEvent?.(t)}catch{}try{let h={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,session_events:[t]}};if(new TextEncoder().encode(JSON.stringify(h)).byteLength<=1100)this.transportSender.enqueueAuxiliary(h);else this.transportSender.skippedAuxiliary++}catch{}}sendDiagnostics(){let t=this.diagnostics,h=(s,_)=>{let u=(s??[]).slice(-64),a=u.filter((f)=>!_.has(f.id)).slice(0,8);if(this.mediaSocketConnected){let f=new Set(u.map((i)=>i.id));for(let i of _)if(!f.has(i))_.delete(i);for(let i of a)_.add(i.id)}return a},e=h(t.playbackEvents,this.reportedPlaybackIDs),m=h(t.captureQueueEvents,this.reportedCaptureQueueIDs);if(this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{media_transport:"websocket",codec:"pcm16",client_version:xt,client_epoch:this.clientEpoch,playback_events:e,capture_queue_events:m,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:t.rttMs,playback_queue_ms:t.queueMs,playback_target_ms:t.targetMs,playback_max_queue_ms:t.maxQueueMs,playback_underruns:t.underruns,playback_underrun_ms:t.underrunDurationMs,playback_underrun_events:t.underrunEvents,playback_dropped_ms:t.droppedMs,websocket_buffered_bytes:t.websocketBufferedBytes,audio_context_rate:t.audioContextRate,microphone_sample_rate:t.microphoneSampleRate,microphone_channel_count:t.microphoneChannelCount,echo_cancellation:t.echoCancellation,noise_suppression:t.noiseSuppression,auto_gain_control:t.autoGainControl,mic_active_rms_dbfs:t.micActiveRmsDbfs,mic_peak_dbfs:t.micPeakDbfs,mic_post_peak_dbfs:t.micPostPeakDbfs,mic_input_gain_db:t.micInputGainDb,mic_limiter_reduction_db:t.micLimiterReductionDb,capture_sequence_gaps:t.captureSequenceGaps,playback_sequence_gaps:t.playbackSequenceGaps,drop_events:t.dropEvents}})),this.mediaSocketConnected)this.transportSender.enqueue(this.transportTelemetry.drain());this.callbacks.onDiagnostics?.({...t})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setPlaybackObservation(t,h){try{if(this.playback?.port.postMessage({type:"playback.telemetry.boundary",active:t,reason:h,audio_time_ms:(this.ctx?.currentTime??0)*1000}),!t)this.diagnostics.underrunEvents=Bh(this.diagnostics.underrunEvents??[],h)}catch{}}setMuted(t){let h=this.muted!==t;if(this.muted=t,this.worker?.postMessage({type:"muted",value:t}),this.capture?.port.postMessage({type:"muted",value:t}),t)this.sendText(JSON.stringify({type:"interrupt"}));if(h)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:t?"muted":"unmuted"})}stop(){this.shutdownIntent="user_stop";let t=!this.closed;this.closed=!0;try{if(t)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(t,h=!1){if(this.shutdownIntent="audio_error",!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:t.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",t,{recoverable:h})}finally{this.teardown()}}teardown(){this.runtimeTelemetry.stopEnvironment(),this.transportSender.stop(),this.setPlaybackObservation(!1,"observation_ended"),this.stopRingback(),this.microphoneTransportReady=!1,this.sessionReady=this.callAnswered=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let t=this.worker;if(t?.postMessage({type:"close",reason:this.shutdownIntent}),t){let h=setTimeout(()=>t.terminate(),600);t.onmessage=(e)=>{if(e.data?.type==="socket.shutdown.complete")clearTimeout(h),t.terminate()}}this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((h)=>h.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var Te=ft*Ot/1000;var Xt=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
        const muted = Boolean(event.data.value);
        // Readiness can repeat the current gate. Preserve partial speech and
        // DSP history unless the user actually changes mute state.
        if (muted === this.muted) return;
        this.muted = muted;
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
`;var gh=`// Passive receiver only. Both directions share one rendering clock and delay.
class TelephonyListenerProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    this.targetMS = options.processorOptions?.initialTargetMs ?? 60;
    this.capacityMS = Math.min(320, Math.max(220,this.targetMS+80));
    this.futureMS = Math.min(320, Math.max(200,this.targetMS+60));
    this.stereo = options.processorOptions?.stereo === true;
    this.queues = [[], []];
    this.dropped = [0, 0];
    this.lastPlayAtMS=[null,null];this.rejectedLate=[0,0];this.rejectedFuture=[0,0];
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
      this.lastPlayAtMS[d]=data.playAtMS;
      if (data.playAtMS + duration < now || data.playAtMS > now + this.futureMS) {
        if(data.playAtMS+duration<now)this.rejectedLate[d]+=data.frame.length;else this.rejectedFuture[d]+=data.frame.length;
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
      this.port.postMessage({ type: "listener.diagnostics", render_time_ms:currentTime*1000,last_play_at_ms:this.lastPlayAtMS.slice(),rejected_late_ms:this.rejectedLate.map(n=>n*1000/sampleRate),rejected_future_ms:this.rejectedFuture.map(n=>n*1000/sampleRate), dropped_ms: this.dropped.map(n => n * 1000 / sampleRate), played_ms: this.played.map(n => n * 1000 / sampleRate), max_queue_ms: this.maxQueueMS, max_late_ms: this.maxLateMS });
    }
    return true;
  }
}
registerProcessor("telephony-listener", TelephonyListenerProcessor);
`;var nt=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
    postMessage({ type: "socket.failed", recoverable: true, detail: "audio connection lost after repeated retries" });
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
      if(microphoneReady!==Boolean(message.value)) captureGateOpenedAt=monotonicEpochMS();
      microphoneReady = Boolean(message.value);
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
`;var rh=new WeakMap,Yt=new Map,Vh=8000;async function Qt(t,h){let e=h==="worklet"?Xt:h==="worker"?nt:gh;if(typeof location>"u"||new URL(t.mcpURL(),location.href).origin!==location.origin){let i=[URL.createObjectURL(new Blob([e],{type:"text/javascript"}))];return{urls:i,dispose(){i.splice(0).forEach((q)=>URL.revokeObjectURL(q))}}}if(t.name!=="telephony"||!t.projectId||!Number.isSafeInteger(t.installId)||t.installId<=0)throw Error("Telephony audio requires a project and installation");let m=new URL(t.mcpURL(),location.href),s=`${m.origin}:${m.pathname}:${t.projectId}:${t.installId}`,_=rh.get(t);if(!_||_.scope!==s)_={scope:s,entries:new Map},rh.set(t,_);let u=_.entries,a=h,f=u.get(a);if(!f){f=(async()=>{let q=Yt.get(h);if(!q)q=crypto.subtle.digest("SHA-256",new TextEncoder().encode(e)).then((C)=>Array.from(new Uint8Array(C),(w)=>w.toString(16).padStart(2,"0")).join("")),Yt.set(h,q),q.catch(()=>{if(Yt.get(h)===q)Yt.delete(h)});let M=`/ui/frontend/${h}-${await q}.js`,g=new AbortController,P;try{if(await Promise.race([t.get(M,{cache:"force-cache",redirect:"error",signal:g.signal,headers:{Accept:"text/plain"}}),new Promise((b,y)=>{P=setTimeout(()=>{y(Error("Telephony audio asset preparation timed out")),g.abort()},Vh)})])!==e)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let w=new URL(`/api/apps/telephony/_install/${t.installId}${M}`,m);return w.searchParams.set("project_id",t.projectId),w.searchParams.set("install_id",String(t.installId)),w.href}finally{clearTimeout(P)}})(),u.set(a,f);let i=f;i.catch(()=>{if(u.get(a)===i)u.delete(a)})}return{urls:[await f],dispose(){}}}function Gt(t=!1){let h=(t?[Xt]:[Xt,nt]).map((e)=>URL.createObjectURL(new Blob([e],{type:"text/javascript"})));return{urls:h,dispose(){h.splice(0).forEach((e)=>URL.revokeObjectURL(e))}}}async function rt(t,h=!1){if(typeof location>"u"||new URL(t.mcpURL(),location.href).origin!==location.origin)return Gt(h);let e=await Promise.all(h?[Qt(t,"worklet")]:[Qt(t,"worklet"),Qt(t,"worker")]);return{urls:e.flatMap((m)=>m.urls),dispose(){e.forEach((m)=>m.dispose())}}}class Bt{options;target;bitrate=32000;stable;last=-1/0;initial;maximum;constructor(t={}){this.options=t;this.initial=t.playbackTargetMs??60,this.target=this.initial,this.maximum=Math.max(this.initial,t.playbackMaxMs??160)}observe(t,h=performance.now()){if(h<this.last)this.last=-1/0,this.stable=void 0;if(h-this.last<2000)return;this.last=h;let e=t.receiver_jitter===void 0?void 0:t.receiver_jitter*1000,m=t.receiver_concealed_delta_ms??0;if(this.options.playbackAdaptive!==!1){let u=Math.min(this.maximum,Math.max(this.initial,this.initial+Math.ceil(Math.max(0,(e??0)-10)*2/20)*20,m>0?this.target+20:this.initial));if(u>this.target)this.target=Math.min(u,this.target+20),this.stable=void 0;else if(e!==void 0&&m===0&&e<10){if(this.stable??=h,h-this.stable>=1e4)this.target=Math.max(this.initial,this.target-10),this.stable=h}else this.stable=void 0}let{pair_availableOutgoingBitrate:s,remote_receiver_fractionLost:_}=t;if(s!==void 0&&s>0&&s<64000)this.bitrate=Math.max(16000,Math.min(32000,Math.floor((s-32000)*0.8/1000)*1000));else if(_!==void 0&&_>=0.03)this.bitrate=Math.max(16000,this.bitrate-4000);else if(_!==void 0&&_<0.01)this.bitrate=Math.min(32000,this.bitrate+2000)}}class vt{tokens=1100;at;rate=640;observeAvailableBitrate(t){this.rate=t!==void 0&&t>=96000?2000:640}consume(t,h=performance.now()){if(!Number.isFinite(t)||!Number.isFinite(h)||t<0||t>1100)return!1;if(this.at!==void 0)this.tokens=Math.min(1100,this.tokens+Math.max(0,h-this.at)*this.rate/1000);if(this.at=h,this.tokens<t)return!1;return this.tokens-=t,!0}}function Lt(t){if(t===void 0)return"websocket";if(!["websocket","webrtc","auto"].includes(t))throw RangeError("Unsupported softphone media transport");return t}function ot(t,h){if(h!==void 0&&typeof h!=="boolean")throw RangeError("webrtcFec must be boolean");let e=new URL(t);if(e.searchParams.set("transport","webrtc"),e.searchParams.delete("webrtc_fec"),h!==void 0)e.searchParams.set("webrtc_fec",String(h));return e.toString()}var X=(t)=>{try{t?.()}catch{}},it=(t,h=1000000000000)=>typeof t==="number"&&Number.isFinite(t)?Math.max(0,Math.min(t,h)):0;function $h(t){return t===!0?"relay_configured":t===!1?"no_relay":"unreported"}function Ut(t){let h=v(t);return h.denied||h.code==="media_session_changed"||h.code==="call_ended"||h.code==="call_not_attachable"||h.expired||["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes(t?.name)}function th(t,h){let e=0,m=0,s=0,_=0,u=0,a=0,f=0,i=0,q=0,M=null,g,P,C,w={},b={},y={},r=(S,A)=>typeof S?.[A]==="number"&&Number.isFinite(S[A])?Math.max(0,S[A]):void 0,V=(S,A,R)=>{for(let x of R){let c=r(S,x);if(c!==void 0)b[A+x]=c}},Q=(S,A,R,x,c)=>{let B=r(S,R),F=r(S,x),U=h?.counters?.[A];if(B!==void 0&&F!==void 0){if(w[A]??={},w[A][R]=B,w[A][x]=F,r(S,"ssrc")!==void 0)w[A].ssrc=S.ssrc;if(U&&U.ssrc===r(S,"ssrc")&&B>=U[R]&&F>U[x])b[c]=(B-U[R])/(F-U[x])*1000}},T=(S,A,R)=>{let x=r(S,"packetsLost"),c=r(S,"ssrc"),B=r(S,"timestamp"),F=h?.counters?.[A];if(c!==void 0)b[R+"ssrc"]=c;if(x===void 0||B===void 0)return;if(w[A]??={},w[A].packetsLost=x,w[A].at=B,c!==void 0)w[A].ssrc=c;if(F&&F.ssrc===c&&x>=F.packetsLost&&B>F.at)b[R+"loss_delta"]=(b[R+"loss_delta"]??0)+(x-F.packetsLost),b[R+"loss_window_ms"]=B-F.at},G=(S,A,R)=>{let x=r(S,"concealedSamples"),c=r(S,"silentConcealedSamples"),B=r(S,"timestamp"),F=r(S,"ssrc"),U=h?.counters?.[A];if(x===void 0||B===void 0)return;if(w[A]??={},Object.assign(w[A],{concealedSamples:x,at:B}),F!==void 0)w[A].ssrc=F;if(c!==void 0)w[A].silentConcealedSamples=c;if(U&&U.ssrc===F&&x>=U.concealedSamples&&B>U.at){if(b.receiver_concealed_delta_ms=(b.receiver_concealed_delta_ms??0)+(x-U.concealedSamples)*1000/R,b.receiver_concealed_window_ms=B-U.at,c!==void 0&&c>=U.silentConcealedSamples)b.receiver_silent_concealed_delta_ms=(b.receiver_silent_concealed_delta_ms??0)+(c-U.silentConcealedSamples)*1000/R}},d=!1,Mt=0;t.forEach((S,A)=>{if(q=Math.max(q,it(S.timestamp,1000000000000000)),(S.kind??S.mediaType??t.get(S.localId)?.kind)==="audio"&&Mt++<32){if(S.type==="outbound-rtp")e+=it(S.bytesSent),V(S,"sender_",["bytesSent","packetsSent","headerBytesSent","retransmittedPacketsSent","retransmittedBytesSent","nackCount","targetBitrate"]),Q(S,A,"totalPacketSendDelay","packetsSent","sender_send_delay_interval_ms");if(S.type==="inbound-rtp"){T(S,A,"receiver_"),m+=it(S.bytesReceived),s+=it(S.packetsLost),_=Math.max(_,it(S.jitter)*1000);let x=t.get(S.codecId),c=r(x,"clockRate")??48000;if(G(S,A,c),d||=r(S,"concealedSamples")!==void 0,u+=it(S.concealedSamples)*1000/c,a+=it(S.packetsDiscarded),f+=it(S.jitterBufferDelay),i+=it(S.jitterBufferEmittedCount),V(S,"receiver_",["bytesReceived","packetsReceived","packetsLost","jitter","packetsDiscarded","concealedSamples","silentConcealedSamples","concealmentEvents","insertedSamplesForDeceleration","removedSamplesForAcceleration","totalSamplesReceived","audioLevel","totalAudioEnergy","totalSamplesDuration","nackCount","fecPacketsReceived","fecPacketsDiscarded"]),Q(S,A,"jitterBufferDelay","jitterBufferEmittedCount","receiver_jitter_buffer_interval_ms"),Q(S,A,"jitterBufferTargetDelay","jitterBufferEmittedCount","receiver_jitter_target_interval_ms"),Q(S,A,"jitterBufferMinimumDelay","jitterBufferEmittedCount","receiver_jitter_minimum_interval_ms"),V(S,"receiver_",["totalProcessingDelay","totalSamplesDecoded"]),y.receiver_processing=r(S,"totalSamplesDecoded")===void 0?"unavailable":"per_decoded_sample",Q(S,A,"totalProcessingDelay","totalSamplesDecoded","receiver_processing_interval_ms"),r(S,"lastPacketReceivedTimestamp")!==void 0)b.receiver_last_packet_age_ms=Math.max(0,S.timestamp-S.lastPacketReceivedTimestamp);if(x){if(y.codec=x.mimeType,b.codec_clock_rate=c,r(x,"channels")!==void 0)b.codec_channels=x.channels}}if(S.type==="remote-inbound-rtp")V(S,"remote_receiver_",["packetsLost","fractionLost","jitter","roundTripTime","totalRoundTripTime","roundTripTimeMeasurements"]),T(S,A,"remote_receiver_");if(S.type==="remote-outbound-rtp")V(S,"remote_sender_",["packetsSent","bytesSent"]);let R=S.type==="outbound-rtp"?"bytesSent":S.type==="inbound-rtp"?"bytesReceived":void 0;if(R&&r(S,R)!==void 0)w[A]??={},w[A][R]=S[R],w[A].at=S.timestamp}if(S.type==="transport"&&S.selectedCandidatePairId){g=S.selectedCandidatePairId,y.dtls=S.dtlsState;let R=t.get(g);if(R){M=r(R,"currentRoundTripTime")!==void 0?R.currentRoundTripTime*1000:null,V(R,"pair_",["availableOutgoingBitrate","availableIncomingBitrate","currentRoundTripTime","totalRoundTripTime","bytesSent","bytesReceived","requestsSent","requestsReceived","responsesSent","responsesReceived","consentRequestsSent"]),y.pair=R.state;let x=t.get(R.localCandidateId),c=t.get(R.remoteCandidateId);if(x)P=x.protocol,C=x.candidateType,y.protocol=P,y.local_candidate=C,y.relay_protocol=x.relayProtocol;if(c)y.remote_candidate=c.candidateType}}});let ht=(S,A)=>{if(h?.counters){let x=0;for(let[c,B]of Object.entries(w)){let F=h.counters[c];if(F&&B[S]>=F[S]&&B.at>F.at)x+=(B[S]-F[S])*8000/(B.at-F.at)}return x}let R=h&&q>h.at?(q-h.at)/1000:0;return R?Math.max(0,A-(S==="bytesSent"?h.sent:h.received))*8/R:0},et=(h?.pathRevision??0)+(g&&g!==h?.pairID?1:0);b.path_revision=et;let bt=ht("bytesSent",e),yt=ht("bytesReceived",m);return{previous:{at:q,sent:e,received:m,counters:w,pairID:g,pathRevision:et},rtt:M,queueMs:b.receiver_jitter_buffer_interval_ms??0,metrics:Kt({...b,send_bitrate_bps:bt,receive_bitrate_bps:yt}),states:jt(y),webrtc:{protocol:P,candidateType:C,sendBitrateBps:bt,receiveBitrateBps:yt,packetsLost:s,jitterMs:_,concealedMs:d?u:void 0,packetsDiscarded:a,jitterBufferMs:i>0?f/i*1000:0,jitterBufferIntervalMs:b.receiver_jitter_buffer_interval_ms}}}function Xh(t,h=new Date().toISOString()){let e=[];for(let[m,s]of[["receiver_","carrier_to_operator"],["remote_receiver_","operator_to_carrier"]]){let _=t[m+"loss_delta"];if(_>0)e.push({timestamp:h,direction:s,reason:"webrtc_packet_loss",duration_ms:0,packet_count:_,ssrc:t[m+"ssrc"],window_ms:t[m+"loss_window_ms"]})}if(t.receiver_concealed_delta_ms>0)e.push({timestamp:h,direction:"carrier_to_operator",reason:"webrtc_concealment",duration_ms:Math.round(t.receiver_concealed_delta_ms),ssrc:t.receiver_ssrc,window_ms:t.receiver_concealed_window_ms});return e}class hh{callbacks;stopped=!1;generation=0;socket;pc;stream;context;capture;whisper;destination;speaker;remoteAudio;speakerMeter;carrierStalled=!1;deliveryDegraded=!1;ready=!1;answered=!1;mediaPathReady;mediaRevision=0;reportedLive=!1;peer=!1;muted=!1;whisperEpoch=null;whisperBase=null;whisperResampler=new gt;timer;retry;cancelSetup;recovering=!1;recoveryGeneration=0;recoveryContext;recoveryExpiry;recoveryRequest;ringback;options;workletURL;events=[];previous;sampling=new WeakSet;transportTelemetry=new Tt("webrtc");transportSender=new wt((t)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[t]}})},(t,h=0)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let e=t;if(e?.diagnostics?.webrtc){let m=e.diagnostics.webrtc;return this.sendTelemetry({...e,diagnostics:{...e.diagnostics,webrtc:{...m,jitterBufferSampleAgeMs:(m.jitterBufferSampleAgeMs??0)+h}}})}return this.sendTelemetry(t)});controlBudget=new vt;quality;playbackReceiver;uploadSender;applyingBitrate=!1;appliedBitrate;statsErrors=0;lastStatsSend=-1/0;lastValidStatsAt=-1/0;clientEpoch=crypto.randomUUID();relayConfigured;nativeCounts;completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};runtime=new lt((t)=>this.recordSessionEvent(t));diagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:null,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t){this.callbacks=t}async start(t,h,e){if(this.options=h,this.workletURL=e,!e)throw Error("WebRTC requires the shared Telephony audio processor");if(typeof RTCPeerConnection!=="function")throw Error("WebRTC audio is unavailable in this browser");I(h),await this.connect(t)}current(t){return!this.stopped&&this.generation===t}guard(t){if(!this.current(t))throw Error("Audio session cancelled")}async connect(t){this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+xt.replaceAll(".","_")}),this.runtime.observeEnvironment();let h=++this.generation,e=this.options;for(let M of this.events.slice(-8))this.enqueueRTCEvents({session_events:[M]});for(let M of this.diagnostics.dropEvents.slice(-8))this.enqueueRTCEvents({drop_events:[M]});this.ready=this.peer=this.answered=this.reportedLive=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.previous=void 0,this.quality=new Bt(e),this.appliedBitrate=void 0;let m=await k("microphone_device",(M)=>this.recordSessionEvent(M),()=>navigator.mediaDevices.getUserMedia({audio:at(e)}));if(!this.current(h))throw m.getTracks().forEach((M)=>M.stop()),Error("Audio session cancelled");this.stream=m;let s=m.getAudioTracks()[0];if(!s)throw Error("No microphone audio track");let _=s.getSettings();this.diagnostics={...this.diagnostics,microphoneSampleRate:_.sampleRate??0,microphoneChannelCount:_.channelCount??1,echoCancellation:typeof _.echoCancellation==="boolean"?_.echoCancellation:null,noiseSuppression:_.noiseSuppression??null,autoGainControl:_.autoGainControl??null,micInputGainDb:e.inputGainDB},s.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),s.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),s.onended=()=>{if(this.current(h))this.disconnected(h,"microphone_ended")},this.context=new AudioContext({latencyHint:"interactive"});let u=this.context;if(u.onstatechange=()=>{this.runtime.context(u.state),this.updateLiveState()},this.runtime.context(u.state),await k("audio_context",(M)=>this.recordSessionEvent(M),()=>u.resume()),this.guard(h),e.outputDeviceId&&"setSinkId"in u)await u.setSinkId(e.outputDeviceId);await k("audio_worklet",(M)=>this.recordSessionEvent(M),()=>u.audioWorklet.addModule(this.workletURL)),this.guard(h),this.diagnostics.audioContextRate=u.sampleRate,this.capture=new AudioWorkletNode(u,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:e.inputGainDB,highpassFilter:e.highpassFilter}}),this.destination=u.createMediaStreamDestination(),this.destination.channelCount=1,u.createMediaStreamSource(m).connect(this.capture).connect(this.destination),this.capture.port.onmessage=(M)=>{if(M.data?.type==="capture.stats"){let g=(C)=>C>0?20*Math.log10(C):null;this.diagnostics.micActiveRmsDbfs=g(M.data.active_rms),this.diagnostics.micPeakDbfs=g(M.data.pre_peak),this.diagnostics.micPostPeakDbfs=g(M.data.post_peak),this.diagnostics.micLimiterReductionDb=M.data.limiter_reduction_db??0;let P=0;if(this.speakerMeter){let C=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(C),P=Math.sqrt(C.reduce((w,b)=>w+b*b,0)/C.length)}X(()=>this.callbacks.onLevels?.(it(M.data.active_rms,1),P))}},this.speaker=u.createGain(),this.speaker.gain.value=e.outputVolume??1,this.speaker.connect(u.destination),this.speakerMeter=u.createAnalyser(),this.speakerMeter.fftSize=256;let a=u.createGain();a.gain.value=0,this.speaker.connect(this.speakerMeter).connect(a).connect(u.destination),this.whisper=new AudioWorkletNode(u,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:I(e)}),this.whisper.connect(this.speaker),this.gate();let f=new WebSocket(ot(t,e.webrtcFec));f.binaryType="arraybuffer",this.socket=f;let i=!1,q=!1;await k("audio_transport",(M)=>this.recordSessionEvent(M),()=>new Promise((M,g)=>{let P=(b)=>{if(q)return;q=!0,clearTimeout(C),this.cancelSetup=void 0,b?g(b):M()},C=setTimeout(()=>P(Error("WebRTC audio connection timed out")),18000);this.cancelSetup=()=>P(Error("Audio session cancelled"));let w=()=>{if(this.current(h)&&this.ready&&i)this.gate(),P(),this.updateLiveState()};f.onmessage=(b)=>{if(!this.current(h))return;if(b.data instanceof ArrayBuffer){this.playWhisper(b.data);return}if(typeof b.data!=="string")return;let y;try{y=JSON.parse(b.data)}catch{return}if(y.type==="webrtc.config"){if(this.relayConfigured=typeof y.relay_configured==="boolean"?y.relay_configured:void 0,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"ice_configuration",outcome:$h(y.relay_configured)}),this.pc){P(Error("Repeated WebRTC configuration"));return}(async()=>{let r=new RTCPeerConnection({iceServers:y.ice_servers??[]});this.pc=r,this.destination.stream.getAudioTracks().forEach((Q)=>{let T=r.addTrack(Q,this.destination.stream);this.uploadSender=T,this.applyUploadBitrate(T,32000,h)}),r.ontrack=(Q)=>{if(!this.current(h))return;this.playbackReceiver=Q.receiver,this.applyPlaybackTarget();let T=new MediaStream([Q.track]),G=new Audio;G.muted=!0,G.srcObject=T,this.remoteAudio=G,G.play().catch(()=>X(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction."))),u.createMediaStreamSource(T).connect(this.speaker)},r.onconnectionstatechange=()=>{if(!this.current(h))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:r.connectionState}),r.connectionState==="connected")i=!0,w();if(r.connectionState==="failed"||r.connectionState==="closed")if(!q)P(Error("WebRTC connection failed"));else this.disconnected(h,"webrtc_"+r.connectionState)};let V=await r.createOffer();this.guard(h),await r.setLocalDescription(V),await new Promise((Q,T)=>{if(r.iceGatheringState==="complete"){Q();return}let G=setTimeout(()=>{r.onicegatheringstatechange=null,T(Error("ICE gathering timed out"))},7000);r.onicegatheringstatechange=()=>{if(r.iceGatheringState==="complete")clearTimeout(G),r.onicegatheringstatechange=null,Q()}}),this.guard(h),f.send(JSON.stringify({type:"webrtc.offer",sdp:r.localDescription.sdp}))})().catch((r)=>P(r instanceof Error?r:Error("WebRTC setup failed")));return}if(y.type==="webrtc.answer"){this.pc?.setRemoteDescription({type:"answer",sdp:y.sdp}).catch(()=>P(Error("Invalid WebRTC answer")));return}if(y.type==="media.ready"||y.type==="media.not_ready"){if(!Number.isSafeInteger(y.revision)||y.revision<=this.mediaRevision)return;this.mediaRevision=y.revision,this.mediaPathReady=y.type==="media.ready",X(()=>this.callbacks.onMediaReadiness?.(y)),this.updateLiveState()}if(y.type==="ready")this.ready=!0,this.send({type:"media.capabilities",version:2,versions:[3],whisper:!0}),w();if(y.type==="peer.connected")this.peer=!0,this.gate(),w();if(y.type==="peer.disconnected")this.peer=!1,this.gate(),this.updateLiveState(),X(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));if(y.type==="call.status")this.answered=["answered","in-progress"].includes(y.status),w(),X(()=>this.callbacks.onCallStatus?.(y));if(y.type==="call.error")if(!q)P(Error(y.detail??"Carrier activation failed"));else this.stop(),X(()=>this.callbacks.onState?.("error",y.detail));if(y.type==="call.ended"||y.type==="session.replaced")if(!q)P(Error(y.type));else this.stop(),X(()=>this.callbacks.onState?.("ended",y.type));if(y.type==="audio.health"){if(this.diagnostics.audioHealth={state:y.state,reason:y.reason,stages:y.stages},X(()=>this.callbacks.onAudioHealth?.(y)),y.state==="audio_degraded"&&!this.deliveryDegraded)this.deliveryDegraded=!0,X(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));else if(this.deliveryDegraded&&y.state==="healthy"&&y.stages?.telephony_to_browser?.state==="healthy"&&u.state==="running")this.deliveryDegraded=!1,X(()=>this.callbacks.onNotice?.("Speech delivery restored."))}if(y.type==="media.delivery"){if(y.state==="stalled")X(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));else if(y.state==="flowing"&&this.carrierStalled)X(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));this.carrierStalled=y.state==="stalled"}if(y.type==="call.notice")X(()=>this.callbacks.onNotice?.(y.detail??y.reason??"Audio delivery notice"));if(y.type==="coach.state")this.whisperEpoch=y.talking&&Number.isInteger(y.epoch)?y.epoch:null,this.whisperBase=null,this.whisper?.port.postMessage({type:"whisper.clear"})},f.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"}),f.onclose=(b)=>{if(!this.current(h))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(b.code),was_clean:b.wasClean}),!q){P(Object.assign(Error("WebRTC signaling unavailable"),b.code===1008?{status:403}:{}));return}if(b.code===1008){this.stop(),X(()=>this.callbacks.onState?.("error","Media authorization ended"));return}this.disconnected(h,"signaling_closed")}})),this.guard(h),this.timer=setInterval(()=>{this.runtime.tick(),this.statistics(h)},1000),await this.statistics(h)}send(t){if(this.socket?.readyState===WebSocket.OPEN&&this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(t))}catch{}}updateLiveState(){let t=this.ready&&this.peer&&this.answered&&this.mediaPathReady!==!1&&this.pc?.connectionState==="connected"&&this.context?.state==="running";if(t===this.reportedLive)return;this.reportedLive=t,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"media_path",outcome:t?"ready":"waiting"}),X(()=>this.callbacks.onState?.(t?"live":"reconnecting",t?void 0:"Connecting carrier audio…"))}gate(){let t=!this.stopped&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!t}),this.destination?.stream.getAudioTracks().forEach((h)=>h.enabled=t)}async statistics(t){if(!this.current(t)||!this.pc||this.sampling.has(this.pc))return;let h=this.pc,e=performance.now();this.sampling.add(h);try{let m=th(await h.getStats(),this.previous);if(!this.current(t)||this.pc!==h)return;if(this.previous=m.previous,this.lastValidStatsAt=performance.now(),this.nativeCounts={packetsLost:m.webrtc.packetsLost,packetsDiscarded:m.webrtc.packetsDiscarded,concealedMs:m.webrtc.concealedMs},m.webrtc.packetsLost+=this.completedCounts.packetsLost,m.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded,m.webrtc.concealedMs!==void 0)m.webrtc.concealedMs+=this.completedCounts.concealedMs;if(this.quality?.observe(m.metrics),this.applyPlaybackTarget(),this.uploadSender&&this.quality)this.applyUploadBitrate(this.uploadSender,this.quality.bitrate,t);if(m.metrics.requested_playback_target_ms=this.quality?.target??60,this.appliedBitrate!==void 0)m.metrics.requested_upload_bitrate_bps=this.appliedBitrate;this.controlBudget.observeAvailableBitrate(m.metrics.pair_availableOutgoingBitrate);let s=Xh(m.metrics);for(let u of s)this.enqueueRTCEvents({drop_events:[u]});this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,...s].slice(-100),this.diagnostics={...this.diagnostics,rttMs:m.rtt,queueMs:m.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,m.queueMs),targetMs:this.quality?.target??this.options?.playbackTargetMs??60,webrtc:m.webrtc,playbackSequenceGaps:m.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};let _=this.stream?.getAudioTracks()[0];this.transportTelemetry.observe({...m.metrics,rtt_ms:m.rtt,queue_ms:m.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:this.socket?.bufferedAmount??0,stats_errors:this.statsErrors,stats_duration_ms:performance.now()-e,main_thread_max_pause_ms:this.runtime.counters.main_thread_max_pause_ms},{...m.states,ice:h.iceConnectionState,connection:"connected",context:this.context?.state,muted:String(this.muted),device_muted:String(_?.muted),track:_?.readyState}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),X(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}catch{if(this.current(t)&&this.pc===h)this.statsErrors++,this.transportTelemetry.observe({stats_errors:this.statsErrors,stats_duration_ms:performance.now()-e},{ice:h.iceConnectionState,context:this.context?.state,muted:String(this.muted)}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),X(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}finally{this.sampling.delete(h)}}applyPlaybackTarget(){let t=this.playbackReceiver;if(!t||!this.quality)return;if("jitterBufferTarget"in t)try{t.jitterBufferTarget=this.quality.target}catch{}}async applyUploadBitrate(t,h,e){if(this.applyingBitrate||this.appliedBitrate===h)return;this.applyingBitrate=!0;try{let m=t.getParameters();if(m.encodings?.length){if(m.encodings[0].maxBitrate=h,await t.setParameters(m),this.current(e)&&this.uploadSender===t)this.appliedBitrate=h}}catch{}finally{this.applyingBitrate=!1}}sendTelemetry(t){if(!this.ready||this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let h=JSON.stringify(t),e=new TextEncoder().encode(h).byteLength+58;if(!this.controlBudget.consume(e))return!1;try{return this.socket.send(h),!0}catch{return!1}}enqueueRTCEvents(t){try{let h={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,...t}};if(new TextEncoder().encode(JSON.stringify(h)).byteLength+58>1100){this.transportSender.skippedAuxiliary++;return}this.transportSender.enqueueAuxiliary(h)}catch{this.transportSender.skippedAuxiliary++}}sendStatistics(){let t=this.stream?.getAudioTracks()[0];if(performance.now()-this.lastStatsSend>=5000)this.lastStatsSend=performance.now(),this.transportSender.enqueueReport({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,client_version:xt,relay_configured:this.relayConfigured,media_transport:"webrtc",codec:"opus",webrtc:{...Object.fromEntries(Object.entries(this.diagnostics.webrtc??{}).map(([h,e])=>[h,typeof e==="number"?Math.round(e*10)/10:e])),jitterBufferSampleAgeMs:Number.isFinite(this.lastValidStatsAt)?Math.max(0,performance.now()-this.lastValidStatsAt):void 0},connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:t?.readyState,microphone_device_muted:t?.muted,rtt_ms:this.diagnostics.rttMs===null?null:Math.round(this.diagnostics.rttMs),playback_queue_ms:Math.round(this.diagnostics.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:this.diagnostics.webrtc?.packetsLost}}),this.transportSender.enqueue(this.transportTelemetry.drain()),this.enqueueRTCEvents({timing:{runtime:this.runtime.counters}})}playWhisper(t){if(t.byteLength<17||t.byteLength>176||this.whisperEpoch===null||!this.context)return;let h=new DataView(t);if(h.getUint32(0,!0)!==827805761||h.getUint32(4,!0)!==this.whisperEpoch)return;let e=h.getFloat64(8,!0);if(!Number.isFinite(e))return;let m=performance.timeOrigin+performance.now()-e;this.whisperBase=this.whisperBase===null?m:Math.min(this.whisperBase,m);let s=Math.max(0,m-this.whisperBase);if(s>200)return;let _=new Float32Array(t.byteLength-16);for(let a=0;a<_.length;a++){let f=~h.getUint8(a+16)&255,i=((f&15)<<3)+132<<(f>>4&7);_[a]=(f&128?132-i:i-132)/32768}let u=this.context.sampleRate===8000?_:this.whisperResampler.process(_,8000,this.context.sampleRate);this.whisper?.port.postMessage({type:"whisper",frame:u,received_audio_ms:this.context.currentTime*1000-s},[u.buffer])}disconnected(t,h){if(!this.current(t)||this.recovering)return;this.recovering=!0;let e=Pt("automatic_retry");this.recoveryContext=e,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"started",code:h,...e}),this.cleanup(),X(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));let m=++this.recoveryGeneration,s=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===m;this.recoveryExpiry=setTimeout(()=>{if(!s())return;this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"timed_out",...this.recoveryContext}),++this.recoveryGeneration,this.recovering=!1,clearTimeout(this.retry),this.cancelSetup?.(),this.recoveryRequest?.abort(),this.cleanup(),X(()=>this.callbacks.onState?.("error","Network unavailable; waiting to restore audio to the existing call.",{recoverable:!0}))},30000);let _=async()=>{if(!s())return;let u={...e,attempt_id:Et("attempt")};this.recoveryContext=u,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"attempt_started",code:h,...u});try{if(!this.callbacks.refreshMediaURL)throw Error("Fresh media authorization is required");this.recoveryRequest=new AbortController;let a=await this.callbacks.refreshMediaURL(u,this.recoveryRequest.signal);if(!s())return;if(await this.connect(a),!s())return;if(this.pc?.connectionState!=="connected")throw Error("WebRTC disconnected during recovery");clearTimeout(this.recoveryExpiry),this.recovering=!1,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected",...u})}catch(a){if(!s())return;this.cleanup();let f=v(a);if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:Ut(a)?"revoked":"attempt_failed",...u,status:f.status,code:f.code}),Ut(a)){clearTimeout(this.recoveryExpiry),this.recovering=!1,X(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return}this.retry=setTimeout(()=>void _(),1000)}};_()}recordSessionEvent(t){try{t={...this.callbacks.sessionDiagnostics?.(),...t}}catch{}t=qt(t),this.events=[...this.events,t].slice(-100),this.enqueueRTCEvents({session_events:[t]}),X(()=>this.callbacks.onSessionEvent?.(t))}setMuted(t){if(this.muted!==t)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:t?"muted":"unmuted"});if(this.muted=t,this.gate(),t)this.send({type:"interrupt"})}sendDTMF(t){if(!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");this.send({type:"dtmf",digits:t})}setOutputVolume(t){if(!Number.isFinite(t)||t<0||t>1)throw RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=t;if(this.options)this.options.outputVolume=t}startRingback(t){if(this.stopRingback(),this.context&&this.speaker)this.ringback=$t(this.context,this.speaker,Vt(t))}stopRingback(){this.ringback?.(),this.ringback=void 0}cleanup(){if(this.runtime.stopEnvironment(),this.transportSender.stop(),this.nativeCounts){for(let m of["packetsLost","packetsDiscarded","concealedMs"])this.completedCounts[m]+=this.nativeCounts[m]??0;this.nativeCounts=void 0}++this.generation,this.ready=this.peer=!1,this.stopRingback(),clearInterval(this.timer),this.timer=void 0;let t=this.socket;if(this.socket=void 0,t){if(t.readyState===WebSocket.OPEN)try{t.send(JSON.stringify({type:"media.shutdown",reason:this.stopped?"session_cleanup":"audio_error"}))}catch{}t.onclose=t.onmessage=t.onerror=null,t.close(1000,"client_shutdown")}this.playbackReceiver=void 0,this.uploadSender=void 0;let h=this.pc;if(this.pc=void 0,h)h.onconnectionstatechange=h.ontrack=null,h.close();if(this.remoteAudio)this.remoteAudio.pause(),this.remoteAudio.srcObject=null,this.remoteAudio=void 0;this.stream?.getTracks().forEach((m)=>{m.onended=null,m.stop()}),this.stream=void 0,this.destination?.stream.getTracks().forEach((m)=>m.stop()),this.destination=void 0,this.capture?.disconnect(),this.capture=void 0,this.whisper?.disconnect(),this.whisper=void 0,this.whisperEpoch=null,this.whisperBase=null,this.whisperResampler=new gt;let e=this.context;if(this.context=void 0,e)e.onstatechange=null,e.close().catch(()=>{});this.speaker=void 0,this.speakerMeter=void 0}stop(){if(this.stopped)return;if(this.recovering)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"cancelled",...this.recoveryContext});this.stopped=!0,this.recoveryRequest?.abort(),++this.recoveryGeneration,clearTimeout(this.recoveryExpiry),clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup()}}function bh(t,h,e){let m,s=!1,_=!1,u=[];return{async start(a,f){let i=Lt(f.mediaTransport),q=async(M)=>{if(s)throw Error("Audio session cancelled");let g=M();m=g,g.setMuted(_);try{for(let P of u.splice(0))try{g.recordSessionEvent?.(P)}catch{}if(s)throw Error("Audio session cancelled");if(await g.start(a,f),s)throw Error("Audio session cancelled")}catch(P){if(g.stop(),m===g)m=void 0;throw P}};if(i==="websocket"){await q(h);return}try{await q(e)}catch(M){if(i!=="auto"||s||Ut(M))throw M;X(()=>t.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"})),await q(h)}},stop(){s=!0,u.splice(0),m?.stop(),m=void 0},setMuted(a){_=a,m?.setMuted(a)},setOutputVolume(a){m?.setOutputVolume(a)},sendDTMF(a){m?.sendDTMF(a)},recordSessionEvent(a){if(m)m.recordSessionEvent?.(a);else if(!s){if(u.push(qt(a)),u.length>20)u.shift()}},startRingback(a){m?.startRingback?.(a)},stopRingback(){m?.stopRingback?.()}}}async function Ph(){return(await navigator.mediaDevices.enumerateDevices()).filter((h)=>h.kind==="audioinput").map((h,e)=>({deviceId:h.deviceId,label:h.label||`Microphone ${e+1}`}))}function wh(t,h){let e=new dt(t,!1),m,s=!1,_,u=()=>{return s=!0,_??=(async()=>{try{await e.cancel()}finally{m?.dispose(),m=void 0}})()};return{async start(a={}){if(s)throw Error("Microphone preview is already used");s=!0;try{if(m=h?await rt(h,!0):Gt(!0),_)throw m.dispose(),m=void 0,Error("Microphone preview was cancelled");await e.start(m.urls[0],{...o,...a})}catch(f){throw await u(),f}},stop:u}}function Yh(t){return{async preflight(h,e){await Lh(t,h,e)},create(h){let e=new It(h),m,s=!1,_=()=>{s=!0;try{e.stop()}finally{m?.dispose(),m=void 0}};return{async start(u,a){try{if(m=await k("audio_assets",(f)=>e.recordSessionEvent(f),()=>t?rt(t):Promise.resolve(Gt())),s)throw Error("Audio session was cancelled");await e.start(u,m.urls[0],m.urls[1],a)}catch(f){throw _(),f}},stop:_,recordSessionEvent:(u)=>e.recordSessionEvent(u),setMuted:(u)=>e.setMuted(u),sendDTMF:(u)=>e.sendDTMF(u),setOutputVolume:(u)=>e.setOutputVolume(u),startRingback:(u)=>e.startRingback(u),stopRingback:()=>e.stopRingback()}}}}function Ch(t){let h=Yh(t);return{preflight:(e,m)=>h.preflight(e,m),prepare:(e,m)=>Ah(t,e,m),create(e){return bh(e,()=>h.create(e),()=>{let m=new hh(e),s,_=!1;return{async start(u,a){try{if(s=await k("audio_assets",(f)=>m.recordSessionEvent(f),()=>t?rt(t,!0):Promise.resolve(Gt(!0))),_)throw Error("Audio session cancelled");await m.start(u,a,s.urls[0])}catch(f){throw m.stop(),s?.dispose(),s=void 0,f}},stop(){_=!0,m.stop(),s?.dispose(),s=void 0},setMuted:(u)=>m.setMuted(u),sendDTMF:(u)=>m.sendDTMF(u),setOutputVolume:(u)=>m.setOutputVolume(u),recordSessionEvent:(u)=>m.recordSessionEvent(u),startRingback:(u)=>m.startRingback(u),stopRingback:()=>m.stopRingback()}})}}}async function Ah(t,h,e){if(!t||typeof location>"u"||new URL(t.mcpURL(),location.href).origin!==location.origin)return;await k("audio_assets_prepare",e,async()=>{(await rt(t,h.mediaTransport==="webrtc"||h.mediaTransport==="auto")).dispose()})}async function Lh(t,h,e){await Promise.all([k("microphone_preflight",e,async()=>{(await navigator.mediaDevices.getUserMedia({audio:at(h)})).getTracks().forEach((s)=>s.stop())}),Ah(t,h,e)])}function Eh(t,h){return{create(e){let m={...o,...h},s=I(m),_=new Bt(m),u=[],a,f,i,q,M=!1,g=!1,P=!1,C=!1,w=!1,b=!1,y=0,r,V,Q,T,G,d,Mt=!1,ht,et,bt=m.outputVolume??1,yt={},S=32000,A,R,x,c,B=[],F=(p)=>{if(a?.readyState===WebSocket.OPEN&&a.bufferedAmount<65536)a.send(JSON.stringify(p));else throw Error("Listener connection unavailable")},U=()=>{if(M)throw Error("Listening cancelled")},D=()=>{let p=++y;if(w=b=!1,A?.(p,Error("Coaching cancelled")),c?.(),R=void 0,ht)clearInterval(ht);if(ht=void 0,G)G.onconnectionstatechange=null,G.close(),G=void 0;if(V?.disconnect(),V=void 0,Q?.disconnect(),Q=void 0,T?.disconnect(),T=void 0,r?.getTracks().forEach((E)=>{E.onended=null,E.stop()}),r=void 0,C&&g&&a?.readyState===WebSocket.OPEN)try{F({type:"coach.stop",generation:p})}catch{}e.onTalking?.(!1)},mt=()=>D(),N=()=>{if(document.visibilityState!=="visible")D()},K=()=>{if(M)return;if(D(),M=!0,x?.(),window.removeEventListener("blur",mt),document.removeEventListener("visibilitychange",N),et)clearInterval(et);if(et=void 0,a)a.onmessage=a.onclose=a.onerror=null,a.close(1000,"listener_shutdown");if(f)f.ontrack=f.onconnectionstatechange=null,f.close();for(let p of B)p.pause(),p.srcObject=null;if(q?.disconnect(),i)i.onstatechange=null,i.close().catch(()=>{});d?.dispose(),d=void 0},J=(p)=>{if(!M)K(),e.onClose(p)},St=async(p,E)=>{if(await p.setLocalDescription(await p.createOffer()),await new Promise(($,W)=>{if(p.iceGatheringState==="complete"){$();return}let l=setTimeout(()=>{p.onicegatheringstatechange=null,W(Error("ICE gathering timed out"))},7000);p.onicegatheringstatechange=()=>{if(p.iceGatheringState==="complete"||!E())clearTimeout(l),p.onicegatheringstatechange=null,E()?$():W(Error("Coaching cancelled"))}}),!E())throw Error("Listening cancelled");return p.localDescription.sdp};return{async start(p,E){if(C=E?.coaching===!0,U(),typeof RTCPeerConnection!=="function")throw Error("WebRTC audio unavailable");try{if(i=new AudioContext({latencyHint:"interactive"}),await i.resume(),U(),m.outputDeviceId&&"setSinkId"in i)await i.setSinkId(m.outputDeviceId);if(U(),q=i.createGain(),q.gain.value=bt,q.connect(i.destination),i.onstatechange=()=>{if(i?.state==="suspended"||i?.state==="interrupted")J("listener_audio_paused")},await new Promise((l,O)=>{let H=!1,j=!1,Y=(st)=>{if(H)return;H=!0,clearTimeout(L),x=void 0,st?O(st):l()},L=setTimeout(()=>Y(Error("WebRTC listener timed out")),18000),Wt=()=>{if(P&&j&&!M&&!g)g=!0,Y(),e.onReady("webrtc")};x=()=>Y(Error("Listening cancelled")),a=new WebSocket(ot(p,m.webrtcFec)),a.onmessage=({data:st})=>{if(M)return;if(typeof st!=="string"){Y(Error("Invalid WebRTC listener frame")),J("listener_protocol_error");return}let Z;try{Z=JSON.parse(st)}catch{Y(Error("Invalid listener control")),J("listener_protocol_error");return}if(Z.type==="webrtc.config"){if(f||Z.listener!==!0){Y(Error("Invalid WebRTC listener configuration"));return}yt={iceServers:Z.ice_servers??[]},S=Z.bitrate??32000,f=new RTCPeerConnection(yt);let ut=f;for(let n=0;n<2;n++)ut.addTransceiver("audio",{direction:"recvonly"});ut.ontrack=(n)=>{if(M||!i||!q)return;u.push(n.receiver);let Zt=ut.getTransceivers().findIndex((Ct)=>Ct.receiver===n.receiver);if(Zt<0||Zt>1){J("listener_protocol_error");return}if("jitterBufferTarget"in n.receiver)try{n.receiver.jitterBufferTarget=s.initialTargetMs}catch{}let _h=new MediaStream([n.track]),Ht=new Audio;Ht.muted=!0,Ht.srcObject=_h,B.push(Ht),Ht.play().catch(()=>{});let fh=i.createMediaStreamSource(_h);if(h.stereo){let Ct=i.createStereoPanner();Ct.pan.value=Zt===0?-1:1,fh.connect(Ct).connect(q)}else{let Ct=i.createGain();Ct.gain.value=0.5,fh.connect(Ct).connect(q)}},ut.onconnectionstatechange=()=>{if(M)return;if(ut.connectionState==="connected")P=!0,Wt();if(ut.connectionState==="failed"||ut.connectionState==="closed"){if(Y(Error("WebRTC listener connection failed")),H&&g)J("listener_network_error")}},St(ut,()=>!M).then((n)=>F({type:"webrtc.offer",sdp:n})).catch((n)=>Y(n));return}if(Z.type==="webrtc.answer"){f?.setRemoteDescription({type:"answer",sdp:Z.sdp}).catch((ut)=>Y(ut));return}if(Z.type==="listener.ready"){if(C&&Z.coaching!==!0){Y(Error("Coaching not authorized"));return}j=!0,Wt();return}if(Z.type==="coach.started"){A?.(Z.generation);return}if(Z.type==="coach.rejected"){A?.(y,Error(Z.detail||"Coaching unavailable"));return}if(Z.type==="coach.stopped"&&(Z.generation===void 0||Z.generation===y)){D();return}if(Z.type==="coach.webrtc.answer"){R?.(Z.sdp,Z.generation);return}},a.onerror=()=>{if(Y(Error("Listener connection failed")),g)J("listener_network_error")},a.onclose=(st)=>{if(Y(Error(st.reason||"Listener disconnected")),g)J(st.reason||"listener_disconnected")}}),U(),C)window.addEventListener("blur",mt),document.addEventListener("visibilitychange",N);let $,W=!1;et=setInterval(()=>{if(M||!f||W)return;W=!0,f.getStats().then((l)=>{if(M)return;let O=th(l,$);$=O.previous,_.observe({...O.metrics,receiver_jitter:O.webrtc.jitterMs/1000});for(let H of u)if("jitterBufferTarget"in H)try{H.jitterBufferTarget=_.target}catch{}e.onDiagnostics?.({media_transport:"webrtc",dropped_ms:[],played_ms:[],max_queue_ms:O.queueMs,max_late_ms:0,sequence_gaps:[],network_excess_ms:0,network_dropped_ms:[],webrtc:O.webrtc})}).catch(()=>{}).finally(()=>{W=!1})},1000)}catch($){throw K(),$}},async startTalking(){if(U(),!C||!g||!i)throw Error("Coaching session not ready");if(w)return;w=!0;let p=++y,E=()=>!M&&w&&y===p;try{let $=await navigator.mediaDevices.getUserMedia({audio:at(m)});if(!E()){$.getTracks().forEach((l)=>l.stop());return}if(r=$,r.getTracks().forEach((l)=>{l.onended=()=>D()}),!d){let l=await rt(t,!0);if(!E()){l.dispose();return}d=l}if(!Mt)await i.audioWorklet.addModule(d.urls[0]),Mt=!0;if(!E())return;if(await new Promise((l,O)=>{let H=setTimeout(()=>{A=void 0,O(Error("Coaching activation timed out"))},3000);A=(j,Y)=>{if(Y||j===p)clearTimeout(H),A=void 0,Y?O(Y):l()},F({type:"coach.start",generation:p})}),!E())return;ht=setInterval(()=>{if(E())try{F({type:"coach.keepalive",generation:p})}catch{D()}},1000),V=new AudioWorkletNode(i,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:m.inputGainDB,highpassFilter:m.highpassFilter}}),T=i.createMediaStreamDestination(),T.channelCount=1,Q=i.createMediaStreamSource($),Q.connect(V).connect(T);let W=new RTCPeerConnection(yt);if(G=W,W.addTransceiver(T.stream.getAudioTracks()[0],{direction:"sendonly",streams:[T.stream],sendEncodings:[{maxBitrate:S}]}),await new Promise((l,O)=>{let H=!1,j=(L)=>{if(H)return;H=!0,clearTimeout(Y),c=void 0,R=void 0,L?O(L):l()},Y=setTimeout(()=>j(Error("Coaching WebRTC timed out")),18000);c=()=>j(Error("Coaching cancelled")),R=(L,Wt)=>{if(Wt!==p||!E())return;W.setRemoteDescription({type:"answer",sdp:L}).catch((st)=>j(st))},W.onconnectionstatechange=()=>{if(!E())return;if(W.connectionState==="connected")j();if(W.connectionState==="failed"||W.connectionState==="closed")j(Error("Coaching media disconnected")),D()},St(W,E).then((L)=>{if(E())F({type:"coach.webrtc.offer",generation:p,sdp:L})}).catch((L)=>j(L))}),E())b=!0,e.onTalking?.(!0)}catch($){if(E())throw D(),$}},stopTalking:D,stop:K,setOutputVolume(p){if(!Number.isFinite(p)||p<0||p>1)throw RangeError("Listener volume must be 0–1");if(bt=p,q)q.gain.value=p}}}}}function ct(t={},h={}){let e=["mediaTransport","webrtcFec","inputDeviceId","outputDeviceId","outputVolume","echoCancellation","noiseSuppression","autoGainControl","inputGainDB","highpassFilter","playbackTargetMs","playbackMinMs","playbackMaxMs","playbackAdaptive","stereo"],m={...o};for(let s of[t,h])for(let _ of e){let u=s[_];if(u!==void 0)m[_]=u}if(I(m),m.mediaTransport=Lt(m.mediaTransport),m.webrtcFec??=!0,m.outputVolume!==void 0&&(!Number.isFinite(m.outputVolume)||m.outputVolume<0||m.outputVolume>1))throw RangeError("Listener volume must be 0–1");return m}function zh(t,h,e,m){let s,_=!1,u=!1,a,f={...t,onReady(i){u=!0,t.onReady(i)}};return{async start(i,q){if(_)throw Error("Listening cancelled");s=h==="websocket"?e(f):m(f);try{await s.start(i,q)}catch(M){if(s.stop(),_||u||h!=="auto"||Ut(M))throw M;s=e(f),await s.start(i,q)}if(_)s.stop();else if(a!==void 0)s.setOutputVolume(a)},async startTalking(){if(_||!s?.startTalking)throw Error("Coaching session not ready");await s.startTalking()},stopTalking(){s?.stopTalking?.()},stop(){_=!0,s?.stop()},setOutputVolume(i){if(!Number.isFinite(i)||i<0||i>1)throw RangeError("Listener volume must be 0–1");a=i,s?.setOutputVolume(i)}}}function eh(t,h={}){let e=ct({},h),m=Lt(e.mediaTransport),s=kh(t,e),_=Eh(t,e);return{create(u){return zh(u,m,(a)=>s.create(a),(a)=>_.create(a))}}}function Jh(t){if(t.byteLength<26||t.byteLength>984||t.byteLength%2)throw Error("Invalid listener audio frame");let h=new DataView(t),e=h.getUint32(4,!0);if(h.getUint32(0,!0)!==827085889||e>1)throw Error("Invalid listener audio protocol");let m=Number(h.getBigUint64(8,!0)),s=Number(h.getBigUint64(16,!0));if(!Number.isSafeInteger(m)||!Number.isSafeInteger(s))throw Error("Invalid listener audio timing");let _=new Float32Array((t.byteLength-24)/2);for(let u=0;u<_.length;u++)_[u]=h.getInt16(24+u*2,!0)/32768;return{direction:e,sequence:m,timestampMS:s,frame:_}}function Zh(t,h,e,m){if(t.length<1||t.length>480||!Number.isFinite(m)||m<0||!Number.isInteger(h)||h<1||h>4294967295)throw Error("Invalid coaching capture");let s=new ArrayBuffer(24+t.length*2),_=new DataView(s);_.setUint32(0,826496833,!0),_.setUint32(4,h,!0),_.setUint32(8,e>>>0,!0),_.setFloat64(16,m,!0);for(let u=0;u<t.length;u++){let a=Math.max(-1,Math.min(1,t[u]));_.setInt16(24+u*2,a<0?a*32768:a*32767,!0)}return s}function kh(t,h={}){return{create(e){let m,s,_,u,a=!1,f=[],i=!1,q=0,M=!1,g=!1,P,C,w,b,y,r,V,Q=!1,T,G,d=0,Mt=!1,ht=[0,0],et=[0,0],bt=[0,0],yt=[0,0],S=[void 0,void 0],A=[new gt,new gt],R=h.outputVolume??1,x,c=()=>{let N=++q;if(M=!1,g=!1,V?.(N,Error("Coaching cancelled")),r)clearInterval(r);if(r=void 0,y?.port1.close(),y?.port2.close(),y=void 0,C?.disconnect(),C=void 0,w?.disconnect(),w=void 0,b?.disconnect(),b=void 0,P?.getTracks().forEach((K)=>{K.onended=null,K.stop()}),P=void 0,i&&Mt&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.stop",generation:N}));e.onTalking?.(!1)},B=()=>c(),F=()=>{if(document.visibilityState!=="visible")c()},U=()=>{if(a)return;if(c(),a=!0,x?.(),window.removeEventListener("blur",B),document.removeEventListener("visibilitychange",F),u)u.onmessage=u.onclose=u.onerror=null,u.close(1000,"listener_shutdown");if(s?.disconnect(),_?.disconnect(),m)m.onstatechange=null,m.close().catch(()=>{});for(let N of f.splice(0))N.dispose()},D=(N)=>{if(!a)U(),e.onClose(N)},mt=()=>{if(a)throw Error("Listening cancelled")};return{async start(N,K){i=K?.coaching===!0,mt();try{if(m=new AudioContext({latencyHint:"interactive"}),m.state==="suspended")await m.resume();if(mt(),h.outputDeviceId&&"setSinkId"in m)await m.setSinkId(h.outputDeviceId);mt();let J=await Qt(t,"listener");if(a)J.dispose(),mt();if(f.push(J),await m.audioWorklet.addModule(J.urls[0]),mt(),s=new AudioWorkletNode(m,"telephony-listener",{numberOfInputs:0,outputChannelCount:[h.stereo?2:1],processorOptions:{stereo:h.stereo,...I(h)}}),_=m.createGain(),_.gain.value=R,s.connect(_).connect(m.destination),s.onprocessorerror=()=>D("listener_audio_error"),s.port.onmessage=({data:St})=>{if(St?.type==="listener.diagnostics"&&!a)try{e.onDiagnostics?.({...St,media_transport:"websocket",context_state:m?.state,context_rate:m?.sampleRate,context_time_ms:(m?.currentTime??0)*1000,received_ms:[...et],scheduled_ms:[...bt],sequence_gaps:[...yt],network_excess_ms:d,network_dropped_ms:[...ht]})}catch{}},m.onstatechange=()=>{if(m?.state==="suspended"||m?.state==="interrupted")D("listener_audio_paused")},await new Promise((St,p)=>{let E=!1,$=setTimeout(()=>{W(Error("Listener connection timed out")),D("listener_network_error")},1e4),W=(l)=>{if(E)return;E=!0,clearTimeout($),x=void 0,l?p(l):St()};x=()=>W(Error("Listening cancelled")),u=new WebSocket(N),u.binaryType="arraybuffer",u.onmessage=({data:l})=>{if(a||!m||!s)return;try{if(typeof l==="string"){let L=JSON.parse(l);if(L.type==="listener.ready"){if(i&&L.coaching!==!0)throw Error("Coaching not authorized");Mt=!0,W(),e.onReady("websocket")}else if(L.type==="coach.started")V?.(L.generation);else if(L.type==="coach.rejected")V?.(q,Error(L.detail||"Coaching unavailable"));else if(L.type==="coach.stopped"&&(L.generation===void 0||L.generation===q))c();return}if(!Mt||!(l instanceof ArrayBuffer))return;let O=Jh(l),H=O.direction;if(et[H]+=O.frame.length*1000/24000,S[H]!==void 0&&O.sequence<S[H])return;if(S[H]!==void 0&&O.sequence>S[H])yt[H]+=O.sequence-S[H];S[H]=O.sequence+1;let j=performance.now()-O.timestampMS;if(G=Math.min(G??j,j),d=Math.max(0,j-G),d>200){ht[H]+=O.frame.length*1000/24000;return}T??=m.currentTime*1000+(h.playbackTargetMs??60)-O.timestampMS;let Y=A[H].process(O.frame,24000,m.sampleRate);bt[H]+=O.frame.length*1000/24000,s.port.postMessage({direction:H,frame:Y,playAtMS:O.timestampMS+T},[Y.buffer])}catch{W(Error("Invalid listener media")),D("listener_protocol_error")}},u.onerror=()=>{W(Error("Listener connection failed")),D("listener_network_error")},u.onclose=(l)=>{W(Error(l.reason||"Listener disconnected")),D(l.reason||"listener_disconnected")}}),mt(),i)window.addEventListener("blur",B),document.addEventListener("visibilitychange",F)}catch(J){throw U(),J}},async startTalking(){if(mt(),!i||!Mt||!m||!u||u.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(M)return;M=!0;let N=++q,K=()=>!a&&M&&N===q;try{let J=await navigator.mediaDevices.getUserMedia({audio:at({...o,...h})});if(!K()){J.getTracks().forEach((E)=>E.stop());return}if(P=J,P.getAudioTracks().forEach((E)=>{E.onended=()=>c()}),!Q){let E=await rt(t,!0);if(!K()){E.dispose();return}f.push(E),await m.audioWorklet.addModule(E.urls[0]),Q=!0}if(!K())return;if(await new Promise((E,$)=>{let W=setTimeout(()=>{V=void 0,$(Error("Coaching activation timed out"))},3000);V=(l,O)=>{if(O||l===N)clearTimeout(W),V=void 0,O?$(O):E()},u.send(JSON.stringify({type:"coach.start",generation:N}))}),!K())return;C=new AudioWorkletNode(m,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:h.inputGainDB??0,highpassFilter:h.highpassFilter!==!1}}),w=m.createMediaStreamSource(J),b=m.createGain(),b.gain.value=0,w.connect(C).connect(b).connect(m.destination),y=new MessageChannel;let St=0,p=new gt;y.port1.onmessage=({data:E})=>{if(!K()||!g||E?.type!=="capture"||!(E.frame instanceof Float32Array)||!m||!u)return;if(!Number.isFinite(E.timestamp_ms)||m.currentTime*1000-E.timestamp_ms>150||u.bufferedAmount>2880||u.readyState!==WebSocket.OPEN)return;let $=p.process(E.frame,E.sample_rate,24000);if($.length>0&&$.length<=480)u.send(Zh($,N,St++,E.timestamp_ms))},y.port1.start(),C.port.postMessage({type:"transport",port:y.port2},[y.port2]),g=!0,e.onTalking?.(!0),r=setInterval(()=>{if(K()&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.keepalive",generation:N}))},1000)}catch(J){if(K())throw c(),J}},stopTalking:c,stop:U,setOutputVolume(N){if(!Number.isFinite(N)||N<0||N>1)throw RangeError("Listener volume must be 0–1");if(R=N,_)_.gain.value=N}}}}}class mh{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;requestAbort;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;audioOptions;constructor(t,h={}){this.client=t;this.options=h;this.audioOptions=ct(h.audio,h),this.runtime=h.runtime??eh(t.app,this.audioOptions)}setAudioOptions(t){let h=ct({},t);this.audioOptions=h,this.runtime=this.options.runtime??eh(this.client.app,h)}getAudioOptions=()=>Object.freeze({...this.audioOptions});getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.observers.add(t),()=>{this.observers.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let h of this.observers)try{h(this.snapshot)}catch{}}async listen(t){return this.begin(t,!1)}async coach(t){return this.begin(t,!0)}async begin(t,h){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=h,this.retryUntil=0,this.retryDelay=500;let e=++this.generation;return this.update({state:"connecting",callId:t,detail:void 0,coaching:h,talking:!1,mediaTransport:void 0}),this.connect(t,e)}async connect(t,h){this.requestAbort?.abort(),this.requestAbort=new AbortController;let e,m=++this.attempt,s=()=>h===this.generation&&m===this.attempt&&!this.disposed;try{if(e=this.coaching?await this.client.coachSession(t):await this.client.listenSession(t,this.requestAbort?.signal),!s()){await this.client.stopListening(e).catch(()=>{});return}this.session=e;let _=this.runtime.create({onReady:(u)=>{if(s())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0,mediaTransport:u})},onClose:(u)=>{if(s())this.disconnected(u,t,h)},onTalking:(u,a)=>{if(s())this.update({talking:u,detail:a})},onDiagnostics:(u)=>{if(s())try{this.options.onDiagnostics?.(u)}catch{}}});if(this.audio=_,this.lease=new Ft(e.lease_seconds,()=>this.client.renewListening(e),(u)=>{if(s())this.disconnected(u==="revoked"?"access_revoked":"media_disconnected",t,h,u==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,e.lease_started_ms),await _.start(this.client.listenerMediaURL(e,this.audioOptions.mediaTransport),{coaching:e.coaching===!0}),!s())_.stop()}catch(_){if(!s()||e&&this.session!==e)return;let u=_?.status,a;try{a=JSON.parse(_.body??"{}").code}catch{}let f=a==="call_ended"?"call_ended":u===401||u===403||u===404?"access_revoked":"listener_disconnected";throw this.disconnected(f,t,h,String(_)),_}}cleanup(){this.requestAbort?.abort(),this.requestAbort=void 0,this.lease?.stop(),this.lease=void 0;let t=this.audio;this.audio=void 0,t?.stop();let h=this.session;return this.session=void 0,h?this.client.stopListening(h).catch(()=>{}):Promise.resolve()}disconnected(t,h,e,m=t){if(e!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let s=t==="call_ended",_=t==="access_revoked",u=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(t);if(!this.coaching&&!s&&!_&&u&&this.options.reconnect!==!1){this.retryUntil||=Date.now()+30000;{this.update({state:"reconnecting",detail:m,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,e===this.generation&&!this.disposed)this.connect(h,e).catch(()=>{})},Date.now()<this.retryUntil?this.retryDelay:15000),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:s?"call_ended":_?"access_revoked":"disconnected",detail:m,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(t){this.audio?.setOutputVolume(t)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let t=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1,mediaTransport:void 0}),await t}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}var Dh={set:(t,h)=>setTimeout(t,h),clear:(t)=>clearTimeout(t)};class sh{run;permanent;ended;event;clock;stopped=!1;active;timer;attempt=0;online=()=>this.wake();constructor(t,h,e,m,s=Dh){this.run=t;this.permanent=h;this.ended=e;this.event=m;this.clock=s;globalThis.addEventListener?.("online",this.online),this.schedule(1000)}emit(t){try{this.event?.(t)}catch{}}schedule(t){if(!this.stopped)this.clock.clear(this.timer),this.timer=this.clock.set(()=>void this.tick(),t)}wake(){if(!this.stopped&&!this.active)this.schedule(0)}async tick(){if(this.stopped||this.active)return;let t=new AbortController;this.active=t,this.attempt++,this.emit("attempt_started");let h;try{if(await Promise.race([this.run(t.signal),new Promise((e,m)=>{h=this.clock.set(()=>{t.abort(),m(Error("Audio recovery request timed out"))},30000)})]),this.stopped)return;this.emit("connected"),this.stop()}catch(e){if(this.stopped)return;if(this.permanent(e)){this.emit("revoked"),this.stop(),this.ended(e);return}this.emit("waiting_for_network"),this.schedule(Math.min(15000,1000*2**Math.min(4,this.attempt)))}finally{if(this.clock.clear(h),this.active===t)this.active=void 0}}stop(){if(this.stopped)return;this.stopped=!0,this.clock.clear(this.timer),this.active?.abort(),globalThis.removeEventListener?.("online",this.online)}}function xh(t){if(!t)return"placing";if(zt(t))return"ended";if(t==="ringing")return"ringing";if(t==="answered"||t==="in-progress")return"connected";return"placing"}var tt=(t)=>t instanceof Error?t.message:String(t);class uh{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;hasStatusPush=!1;automaticRecovery;hasLiveAudio=!1;uncertainAttachment;scheduleStatusPoll;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(t,h={}){this.client=t;this.options=h;if(this.runtime=h.audioRuntime??Ch(t.app),this.audioOptions={...o,...h.audio,...h.mediaTransport?{mediaTransport:h.mediaTransport}:{}},I(this.audioOptions),this.interval=h.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}createCallListener(t={}){return this.assertOpen(),this.client.createCallListener({...this.audioOptions,...t.audio,...t})}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.assertOpen(),this.listeners.add(t),()=>{this.listeners.delete(t)}};update(t){let h={...this.snapshot,...t};if(h.callId&&xh(h.carrierStatus)==="connected")h.phase=h.audioState==="live"?"connected":"placing";this.snapshot=Object.freeze(h);for(let e of this.listeners)try{e(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(t){if(this.disposed||t!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.automaticRecovery?.stop(),this.automaticRecovery=void 0,this.uncertainAttachment=void 0,this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(t){return!this.disposed&&t===this.generation}finish(t){if(this.current(t))this.update({busy:!1})}cancellable(t,h){let e=this.cancellation.signal;return new Promise((m,s)=>{let _=()=>{e.removeEventListener("abort",_),s(Error("Softphone operation cancelled"))};if(e.addEventListener("abort",_,{once:!0}),t.then(m,s).finally(()=>e.removeEventListener("abort",_)),!this.current(h)||e.aborted)_()})}begin(t){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||t&&this.session)throw Error("Softphone already has an active operation or call");let h=this.invalidate();return this.update({busy:!0,detail:void 0}),h}async prepareAudio(t,h){let e=[],m=(s)=>{if(this.current(t)&&!this.cancellation.signal.aborted)e.push(qt(s))};try{return await this.cancellable(k(h?"audio_preflight":"audio_prepare",m,()=>h?this.runtime.preflight(this.audioOptions,m):this.runtime.prepare?.(this.audioOptions,m)??Promise.resolve()),t),this.assertCurrent(t),e}catch(s){if(this.current(t))for(let _ of e)this.reportSessionEvent(_);throw s}}async dial(t){let h=this.begin(!0),e,m=!1;try{this.assertCurrent(h);let s={to:t.to.trim(),from:t.from?.trim(),timeout_sec:t.timeout_sec,recording:t.recording},_=JSON.stringify(s);if(!this.intent||this.intent.value!==_||t.idempotency_key&&t.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:_,request:{...s,idempotency_key:t.idempotency_key||crypto.randomUUID()}};let u=await this.prepareAudio(h,!0);return this.assertCurrent(h),e=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(h),m=!0,this.outbound=!0,await this.attachAudio(e,h,u),e.call_id}catch(s){if(e&&(this.current(h)||!m||this.disposed))await this.recoverStartup(e,h,()=>this.client.hangup(e.call_id));if(this.current(h))this.update({detail:tt(s)});throw s}finally{this.finish(h)}}async answer(t,h={}){let e=this.begin(!0),m,s=!1;try{this.assertCurrent(e);let _=await this.prepareAudio(e,!0);m=await this.client.answer(t,h),this.assertCurrent(e),s=!0,this.outbound=!1,await this.attachAudio(m,e,_)}catch(_){if(m&&(this.current(e)||!s||this.disposed))await this.recoverStartup(m,e,()=>this.client.release(m));if(this.current(e))this.update({detail:tt(_)});throw _}finally{this.finish(e)}}async recoverStartup(t,h,e){try{if(await e(),this.current(h))this.clearCall()}catch{if(this.current(h))this.session=t,this.update({callId:t.call_id,audioState:"error"}),this.startPolling()}}async join(t){await this.answer(t,{rejoin:!0})}async attach(t,h="attach"){await this.acquireSession(t,!1,h)}async takeover(t){await this.acquireSession(t,!0)}async acquireSession(t,h,e="takeover"){let m=this.begin(!0);try{let s=await this.prepareAudio(m,!0);this.assertCurrent(m);let _=Pt(h?"takeover":e),u=await(h?this.client.takeover(t,_):this.client.attach(t,_));this.assertCurrent(m),await this.attachAudio(u,m,s),await this.reconcileAttachedCall(u,m)}catch(s){if(this.current(m))this.update({detail:tt(s)});throw s}finally{this.finish(m)}}async reconnect(t,h="manual_reconnect"){if(!this.session)throw Error("No call to reconnect");let e={...this.audioOptions,...t};I(e);let m=this.begin(!1);this.audioOptions=e;let s=this.session.call_id,_=Pt(h),u=this.session.session_generation,a=(f,i={})=>this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:f,..._,previous_session_id:u,...i});a("started");try{let f=await this.prepareAudio(m,!1),i;try{i=await this.acquireRecoverySession(this.session,{..._,previous_session_id:u},this.cancellation.signal)}catch(q){let M=v(q),g;try{g=JSON.parse(q.body??"{}").current_generation}catch{}if(h==="automatic_retry"||M.code!=="media_session_changed"||!g||!/^session-[a-f0-9-]{1,64}$/.test(g))throw q;this.assertCurrent(m),i=await this.client.attach(s,{...Pt(h),previous_session_id:g},{signal:this.cancellation.signal},g)}this.assertCurrent(m),await this.attachAudio(i,m,f),await this.reconcileAttachedCall(i,m),a("connected",{session_id:i.session_generation})}catch(f){if(a(this.current(m)?"failed":"cancelled"),this.current(m))this.update({detail:tt(f)});throw f}finally{this.finish(m)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let t=this.session;if(!t){this.cancellation.abort();return}let h=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let e=(async()=>{try{if(await this.client.hangup(t.call_id),this.current(h))this.clearCall()}catch(m){if(this.current(h))this.update({audioState:"error",detail:tt(m)}),this.startPolling();throw m}finally{this.finish(h)}})();this.hangingUp=e;try{await e}finally{if(this.hangingUp===e)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(t){this.assertOpen();let h=this.session;if(!h)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let e=this.generation;this.update({busy:!0,detail:void 0});let m;try{m=this.client[t](h.call_id)}catch(s){if(this.current(e)&&this.session===h)this.update({busy:!1,detail:tt(s)});throw s}this.controlPending=m;try{let s=await m;if(this.current(e)&&this.session===h)this.update({holdState:s.hold_state,recordingState:s.recording_state,controlError:s.control_error,capabilities:s.capabilities});return s}catch(s){if(this.current(e)&&this.session===h)this.update({detail:tt(s)});throw s}finally{if(this.controlPending===m)this.controlPending=void 0;if(this.current(e)&&this.session===h)this.update({busy:!1})}}configureAudio(t){this.assertOpen();let h={...this.audioOptions,...t};I(h),this.audioOptions=h}setMuted(t){this.assertOpen(),this.audio?.setMuted(t),this.update({muted:t})}setOutputVolume(t){if(this.assertOpen(),!Number.isFinite(t)||t<0||t>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=t,this.audio?.setOutputVolume(t)}sendDTMF(t){if(this.assertOpen(),!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(t)}observeCall(t){if(this.disposed||t.id!==this.session?.call_id)return;if(t.direction)this.outbound=t.direction==="outbound";if(zt(t.status)){this.invalidate(),this.clearCall(t.status),this.update({busy:!1,phase:"ended",termination:t.termination,answeredBy:t.answered_by??this.snapshot.answeredBy,endedAt:t.ended_at});return}this.update({carrierStatus:t.status,phase:xh(t.status),answeredBy:t.answered_by??this.snapshot.answeredBy,holdState:t.hold_state??this.snapshot.holdState,recordingState:t.recording_state??this.snapshot.recordingState,controlError:t.control_error??this.snapshot.controlError,capabilities:t.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let t=this.options.ringback;return typeof t==="object"&&t?t.country:void 0}syncRingback(){let t=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(t&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!t&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(t,h,e=[]){this.hasStatusPush=!1,this.assertCurrent(h),this.stopAudio(),this.session=t,this.update({callId:t.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let m,s=()=>!this.disposed&&m!==void 0&&this.audio===m,_=(f)=>{if(s())try{f()}catch{}},u,a;try{this.assertCurrent(h),m=this.runtime.create({refreshMediaURL:(f,i)=>{let q=f??Pt("automatic_retry");if(u)return this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"coalesced",...q,shared_attempt_id:a?.attempt_id}),u;if(!s()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let M=this.generation,g=(async()=>{let C=t.session_generation,w=await this.acquireRecoverySession(t,{...q,previous_session_id:C},i);if(this.assertCurrent(M),!s())throw Error("Audio connection no longer active");return t=w,this.session=w,this.startLease(w),this.reportSessionEvent({timestamp:new Date().toISOString(),action:"media_session",outcome:"replaced",...q,previous_session_id:C,session_id:w.session_generation}),this.client.mediaURL(w,this.audioOptions.mediaTransport)})();u=g,a=q;let P=()=>{if(u===g)u=void 0,a=void 0};return g.then(P,P),g},sessionDiagnostics:()=>({session_id:t.session_generation}),onSessionEvent:(f)=>_(()=>this.options.onSessionEvent?.(qt({...f,session_id:f.session_id??t.session_generation}))),onState:(f,i,q)=>{if(!s())return;if(f==="ended"&&i==="call.ended"){this.observeCall({id:t.call_id,status:"completed"});return}if(f==="live")this.hasLiveAudio=!0;if(this.update({audioState:f,detail:i}),this.scheduleStatusPoll?.(),s()&&(f==="error"||f==="ended")){if(this.stopAudio(),f==="error"){if(this.reconcileFailedAudio(t,this.generation),q?.recoverable)this.scheduleAudioRecovery()}}},onMediaReadiness:(f)=>_(()=>this.options.onMediaReadiness?.(f)),onLevels:(f,i)=>_(()=>this.options.onLevels?.(f,i)),onAudioHealth:(f)=>_(()=>this.options.onAudioHealth?.(f)),onDiagnostics:(f)=>_(()=>this.options.onDiagnostics?.(f)),onNotice:(f)=>_(()=>this.options.onNotice?.(f)),onCallStatus:(f)=>_(()=>{this.hasStatusPush=!0,this.observeCall({id:f.call_id,status:f.status,direction:f.direction,answered_at:f.answered_at,ended_at:f.ended_at,answered_by:f.answered_by,termination:f.termination,hold_state:f.hold_state,recording_state:f.recording_state,control_error:f.control_error}),this.scheduleStatusPoll?.()})}),this.audio=m,this.assertCurrent(h);for(let f of e)this.reportSessionEvent(f);if(this.assertCurrent(h),this.startPolling(),this.startLease(t),m.setMuted(this.snapshot.muted),await this.cancellable(m.start(this.client.mediaURL(t,this.audioOptions.mediaTransport),this.audioOptions),h),this.assertCurrent(h),!s())throw Error(this.snapshot.detail||"Audio connection ended during setup");m.setMuted(this.snapshot.muted),this.syncRingback()}catch(f){if(s())this.stopAudio();if(this.current(h))this.update({audioState:"error",detail:tt(f)});throw f}}async acquireRecoverySession(t,h,e){let m=this.uncertainAttachment??{diagnostics:h,expected:t.attachment_guard_version===1?t.session_generation??"":void 0};if(e?.aborted)throw new DOMException("Cancelled","AbortError");if(this.uncertainAttachment=m,m.diagnostics.attempt_id!==h.attempt_id)this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"idempotent_retry",...h,shared_attempt_id:m.diagnostics.attempt_id});try{let s=await this.client.attach(t.call_id,m.diagnostics,{signal:e},m.expected);if(e?.aborted)throw new DOMException("Cancelled","AbortError");return this.uncertainAttachment=void 0,s}catch(s){let _=v(s).status;if(_!==void 0&&_>=400&&_<500)this.uncertainAttachment=void 0;throw s}}scheduleAudioRecovery(){if(this.disposed||!this.session||!this.hasLiveAudio||this.automaticRecovery)return;let t=this.session.call_id,h=this.generation;this.update({audioState:"reconnecting",detail:"Network unavailable; waiting to restore audio."});let e=(_)=>{let u=v(_);return u.denied||u.code==="media_session_changed"||u.code==="call_ended"||u.code==="call_not_attachable"||["NotAllowedError","NotFoundError"].includes(_?.name)},m=this.uncertainAttachment?.diagnostics??Pt("automatic_retry"),s=m;this.automaticRecovery=new sh(async(_)=>{this.assertCurrent(h);let u=await pt((q)=>this.client.getCall(t,q),_);if(!u)throw Object.assign(Error("Call unavailable"),{status:404});if(zt(u.status)){this.observeCall(u);return}this.assertCurrent(h);let a=await this.prepareAudio(h,!1);if(_.aborted)throw new DOMException("Cancelled","AbortError");let f=await this.acquireRecoverySession(this.session,s,_);if(this.assertCurrent(h),_.aborted)throw new DOMException("Cancelled","AbortError");let i=()=>{if(this.current(h)&&this.session===f)this.stopAudio()};_.addEventListener("abort",i,{once:!0});try{if(await this.attachAudio(f,h,a),_.aborted)throw new DOMException("Cancelled","AbortError")}finally{_.removeEventListener("abort",i)}this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:"connected",...s}),this.automaticRecovery?.stop(),this.automaticRecovery=void 0},e,(_)=>{if(this.current(h))this.automaticRecovery=void 0,this.update({audioState:"error",detail:tt(_)})},(_)=>{if(_==="attempt_started")s={...m,attempt_id:Et("attempt")};this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:_,...s})})}async reconcileAttachedCall(t,h){try{let e=await this.client.getCall(t.call_id);if(this.current(h)&&this.session===t&&e)this.observeCall(e)}catch{}}async reconcileFailedAudio(t,h){try{let e=await this.client.getCall(t.call_id);if(!this.current(h)||this.session!==t||!e)return;if(e.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(e)}catch{}}startLease(t){if(this.lease?.stop(),!t.lease_seconds)return;this.lease=new Ft(t.lease_seconds,()=>this.client.renew(t),(h,e)=>{if(this.disposed||this.session!==t)return;if(this.stopAudio(),this.update({audioState:"error",detail:h==="expired"?"Audio session expired. Reconnecting audio.":`Audio authorization ended: ${tt(e??h)}`}),h==="expired")this.scheduleAudioRecovery();else this.automaticRecovery?.stop(),this.automaticRecovery=void 0},(h)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(h);else try{this.options.onSessionEvent?.(h)}catch{}},t.lease_started_ms)}reportSessionEvent(t){if(t=qt(t),this.audio?.recordSessionEvent)try{this.audio.recordSessionEvent(t)}catch{}else try{this.options.onSessionEvent?.(t)}catch{}}stopAudio(){this.lease?.stop(),this.lease=void 0;let t=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{t?.stopRingback?.()}catch{}}try{t?.stop()}catch{}if(t)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){this.scheduleStatusPoll=void 0,clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let t=new AbortController;this.polling=t;let h=!1,e=performance.now(),m=()=>this.hasStatusPush&&this.snapshot.audioState==="live"?Math.max(1,Math.max(this.interval,30000)-(performance.now()-e)):this.interval,s=async()=>{if(h)return;let _=this.session?.call_id;if(!_||t.signal.aborted)return;h=!0;try{let u=await this.client.getCall(_,t.signal);if(e=performance.now(),!t.signal.aborted&&u)this.observeCall(u)}catch(u){if(!t.signal.aborted)this.hasStatusPush=!1,this.update({detail:`Call status unavailable: ${tt(u)}`})}finally{h=!1,this.scheduleStatusPoll?.()}};this.scheduleStatusPoll=()=>{if(h||t.signal.aborted||!this.session||this.disposed)return;clearTimeout(this.timer),this.timer=setTimeout(s,m())},this.scheduleStatusPoll()}clearCall(t){this.hasStatusPush=!1,this.stopAudio(),this.stopPolling(),this.session=void 0,this.hasLiveAudio=!1,this.outbound=!1,this.update({callId:void 0,carrierStatus:t,audioState:"idle",muted:!1,detail:void 0,phase:t?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function Kh(t,h="en"){if(t?.reason==="ai_inactivity")return h.toLowerCase().startsWith("fr")?"Fin de l’appel : aucune réponse":"Call ended: no response";if(t?.reason==="ai_policy_failure")return h.toLowerCase().startsWith("fr")?"Échec du rappel d’inactivité IA":"AI inactivity reminder failed";if(t?.reason==="time_limit"||t?.reason==="ai_max_duration")return h.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return t?.reason?.replaceAll("_"," ")??""}class Rh extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function jh(t){return t.direction==="inbound"&&t.status==="pending"&&t.answerable!==!1&&!t.routing_waiting&&(t.peer_kind==="human"||Boolean(t.ring_offers?.some((h)=>h.kind==="browser")))}var zt=(t)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(t);function z(t){if(!t||/[\s/\\?#]/.test(t))throw Error("Invalid call ID");return encodeURIComponent(t)}class Jt{app;options;transport;listMicrophones=Ph;createMicrophonePreview=(t)=>wh(t,this.app);constructor(t,h={}){this.app=t;this.options=h;if(this.transport=new kt(t,h.authProvider?"proxy":h.direct?"direct":h.runtime,globalThis.fetch,h.direct),t.name!=="telephony"||!t.projectId||!t.installId)throw Error("Telephony requires an explicit project and installation")}get(t,h){return this.transport.request("GET",t,void 0,h)}post(t,h,e){return this.transport.request("POST",t,h,e)}subscribe(t,h,e){return this.transport.subscribe(t,h,e)}path(t){if(!this.options.authProvider)return t;return`/user${t}${t.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(t){let h=await this.get(this.path("/calls"),{signal:t});if(!Array.isArray(h?.calls)||h.calls.some((e)=>!e||typeof e.id!=="string"||typeof e.status!=="string"))throw Error("Invalid Telephony calls response");return h.calls}async getCall(t,h){let e=await this.get(this.path(`/calls?call_id=${z(t)}`),{signal:h});if(!Array.isArray(e?.calls))throw Error("Invalid Telephony call response");return e.calls.find((m)=>m.id===t)}incomingCalls(t){return t.filter(jh)}watchCalls(t,h={}){let e=h.intervalMs??2000;if(!Number.isFinite(e)||e<100)throw Error("Call watch interval must be at least 100 ms");let m=new AbortController,s,_,u=!1,a=!1,f=-1/0,i,q,M=performance.now(),g=()=>{let y=performance.now()-f;return y<15000?Math.max(1,Math.min(Math.max(e,30000)-(performance.now()-M),15000-y)):e},P=new Set,C=()=>{clearTimeout(s),m.abort(),_?.close(),h.signal?.removeEventListener("abort",C)},w=(y)=>{try{h.onError?.(y)}catch{C()}};if(h.signal?.addEventListener("abort",C,{once:!0}),h.signal?.aborted)C();let b=async(y)=>{if(m.signal.aborted)return;if(u){a=!0;return}clearTimeout(s),u=!0;let r=performance.now(),V=i;try{let Q=await this.listCalls(m.signal);if(M=performance.now(),!m.signal.aborted){t(Q),q=V;for(let T of Q){if(T.answerable!==!0||T.status!=="pending")continue;for(let G of T.ring_offers??[]){if(G.kind!=="browser"||!G.id||P.has(G.id))continue;P.add(G.id),this.acknowledgeOffer(T.id,G.id).catch((d)=>{if(![404,405].includes(d?.status??0))P.delete(G.id)})}}try{h.onTiming?.({trigger:y,fetchMs:performance.now()-r})}catch{}}}catch(Q){if(f=-1/0,q=void 0,!m.signal.aborted){let T=Q?.status;try{h.onFailure?.({trigger:y,fetchMs:performance.now()-r,status:typeof T==="number"?T:void 0,error:Q})}catch{}w(Q)}}finally{if(u=!1,!m.signal.aborted)if(a)a=!1,b("push");else s=setTimeout(()=>void b("poll"),g())}};if(b("poll"),!m.signal.aborted&&h.push!==!1&&typeof this.app.subscribe==="function")try{_=this.subscribe(this.path("/calls/events"),(y)=>{if(y.type==="heartbeat"||y.type==="calls.changed"){if(f=performance.now(),y.type==="calls.changed"){let r=typeof y.state==="string"&&/^[0-9a-f]{64}$/.test(y.state)?y.state:void 0;if(i=r,!r||r!==q||u)b("push")}else if(!u)clearTimeout(s),s=setTimeout(()=>void b("poll"),g())}else if(y.type==="access.revoked")f=-1/0,_?.close(),b("push")},{transport:"fetch",signal:m.signal,reconnectDelayMs:250,onError:(y)=>{if(f=-1/0,!u&&!m.signal.aborted)clearTimeout(s),s=setTimeout(()=>void b("poll"),e);let r=y?.status;if(r===404||r===405||r===501)_?.close()}})}catch{}return{close:C}}async place(t){if(!/^\+[1-9]\d{7,14}$/.test(t.to)||!t.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let h=_t();return this.session(await this.post(this.path("/softphone/place"),t),void 0,"media",h)}async answer(t,h={}){let e=_t();try{return this.session(await this.post(this.path(`/softphone/answer/${z(t)}`),h),t,"media",e)}catch(m){let s=m;if(s.status===409&&typeof s.body==="string"){let _;try{_=JSON.parse(s.body)}catch{}if(_?.code==="offer_expired")throw new Rh}throw m}}async acknowledgeOffer(t,h){await this.post(this.path(`/softphone/offer/ack/${z(t)}`),{offer_id:h})}async declineOffer(t,h){await this.post(this.path(`/softphone/offer/decline/${z(t)}`),{offer_id:h})}async attach(t,h,e,m){let s=_t();return this.session(await pt((_)=>this.post(this.path(`/softphone/attach/${z(t)}`),{...h?{media_diagnostics:h}:{},...m!==void 0&&h?.attempt_id?{attachment_guard:{id:h.attempt_id,expected_generation:m}}:{}},{...e,signal:_}),e?.signal??void 0),t,"media",s)}async takeover(t,h){let e=_t();return this.session(await this.post(this.path(`/softphone/takeover/${z(t)}`),h?{media_diagnostics:h}:{}),t,"media",e)}createCallListener(t={}){return new mh(this,{...t,...ct({...this.options.audio,...t.audio},t)})}async listenSession(t,h){let e=_t();return this.session(await pt((m)=>this.post(this.path(`/softphone/listen/${z(t)}`),{},{signal:m}),h,5000,(m)=>{try{this.stopListening(this.session(m,t,"listen-media",e)).catch(()=>{})}catch{}}),t,"listen-media",e)}async coachSession(t){let h=_t(),e=this.session(await this.post(this.path(`/softphone/coach/${z(t)}`),{}),t,"listen-media",h);if(e.coaching!==!0)throw Error("Invalid coaching session");return e}async renewListening(t){return this.post(this.path(`/softphone/${t.coaching?"coach-renew":"listen-renew"}/${z(t.call_id)}`),{session_token:t.session_token})}async stopListening(t){await this.post(this.path(`/softphone/${t.coaching?"coach-stop":"listen-stop"}/${z(t.call_id)}`),{session_token:t.session_token})}async listenerAudit(t){return this.get(this.path(`/softphone/listen-audit/${z(t)}`))}outboundTransport(t){return this.post(this.path("/numbers/outbound-transport"),t)}outboundNumbers(t={}){return this.get(this.path(t.fresh?"/softphone/numbers?fresh=true":"/softphone/numbers"))}async renew(t){return this.post(this.path(`/softphone/renew/${z(t.call_id)}`),{session_token:t.session_token})}async release(t){if(!t.session_token)throw Error("Answer session has no release token");await this.post(this.path(`/softphone/release/${z(t.call_id)}`),{session_token:t.session_token})}async hangup(t){await this.post(this.path(`/calls/${z(t)}/hangup`),{})}hold(t){return this.post(this.path(`/calls/${z(t)}/hold`),{})}resume(t){return this.post(this.path(`/calls/${z(t)}/resume`),{})}pauseRecording(t){return this.post(this.path(`/calls/${z(t)}/pause-recording`),{})}resumeRecording(t){return this.post(this.path(`/calls/${z(t)}/resume-recording`),{})}createSoftphone(t={}){return new uh(this,{...t,audio:{...this.options.audio,...t.audio}})}mediaURL(t,h){return this.resolveMediaURL(t,"media",h)}listenerMediaURL(t,h){return this.resolveMediaURL(t,"listen-media",h)}resolveMediaURL(t,h,e){let m=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),s=new URL(t.media_url,m),_=`/api/apps/telephony/_install/${this.app.installId}/softphone/${h}/${z(t.call_id)}/`;if(s.origin!==m.origin||!["http:","https:"].includes(s.protocol)||s.username||s.password||s.search||s.hash||!s.pathname.startsWith(_)||!/^[A-Za-z0-9_-]+$/.test(s.pathname.slice(_.length)))throw Error("Invalid Telephony media endpoint");if(h==="listen-media"&&s.pathname.slice(_.length)!==t.session_token)throw Error("Listener credential mismatch");if(h==="media"&&At(t.session_generation))s.searchParams.set("session_generation",t.session_generation);let u=this.transport.mediaURL(s,e);return u.protocol=u.protocol==="https:"?"wss:":"ws:",u.href}session(t,h,e="media",m=_t()){let s=t;if(!s||typeof s.call_id!=="string"||typeof s.media_url!=="string"||h&&s.call_id!==h||s.session_token!==void 0&&typeof s.session_token!=="string")throw Error("Invalid Telephony session response");if(s.lease_seconds!==void 0&&(!Number.isFinite(s.lease_seconds)||s.lease_seconds<10||s.lease_seconds>3600||!s.session_token))throw Error("Invalid media lease");if(e==="listen-media"&&(!s.session_token||s.lease_seconds===void 0))throw Error("Invalid listener lease");return z(s.call_id),this.resolveMediaURL(s,e),{...s,lease_started_ms:m}}}var T0=Dt({app:"telephony",create:({app:t})=>new Jt(t)});function Q0({app:t},h){return new Jt(t,h)}export{Q0 as createClient,Kh as callTerminationLabel};
