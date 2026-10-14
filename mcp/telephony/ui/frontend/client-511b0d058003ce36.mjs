function Eh(h){let t=globalThis.crypto?.randomUUID?.()??`${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;return`${h}-${t}`}function Ph(h){return{recovery_id:Eh("recovery"),attempt_id:Eh("attempt"),initiating_action:h}}function Ah(h){return typeof h==="string"&&h.length<=80&&/^(browser|session|recovery|attempt)-[a-f0-9-]{1,64}$/.test(h)?h:void 0}function qh(h){let t=(u,i)=>typeof u==="string"&&u.length<=i&&/^[a-zA-Z0-9_-]*$/.test(u)?u:void 0,e=["WebSocket transport error","WebRTC signaling error","Audio worker failed","WebRTC signaling unavailable"],m=["attach","takeover","automatic_retry","manual_reconnect","audio_device_change","component_recreation"],s=(u,i)=>typeof u==="number"&&Number.isFinite(u)?Math.max(0,Math.min(i,u)):void 0;return{timestamp:Number.isFinite(Date.parse(h.timestamp))?new Date(h.timestamp).toISOString():new Date().toISOString(),action:t(h.action,40)??"diagnostic",outcome:t(h.outcome,40)??"unknown",code:t(h.code,80),detail:h.detail?e.includes(h.detail)?h.detail:"detail_redacted":void 0,recovery_id:Ah(h.recovery_id),attempt_id:Ah(h.attempt_id),shared_attempt_id:Ah(h.shared_attempt_id),session_id:Ah(h.session_id),previous_session_id:Ah(h.previous_session_id),initiating_action:m.includes(h.initiating_action)?h.initiating_action:void 0,status:s(h.status,599),remaining_ms:s(h.remaining_ms,3600000),was_clean:typeof h.was_clean==="boolean"?h.was_clean:void 0,duration_ms:s(h.duration_ms,86400000)}}var ih=()=>performance.now();async function Fh(h,t,e=5000,m){let s=new AbortController,_,u=new Promise((a,S)=>{_=S}),i=()=>{s.abort(),_(new DOMException("Media request cancelled","AbortError"))};t?.addEventListener("abort",i,{once:!0});let f=setTimeout(i,e);try{if(t?.aborted)return i(),await u;return await Promise.race([h(s.signal).then((a)=>{if(s.signal.aborted)m?.(a);return a}),u])}finally{clearTimeout(f),t?.removeEventListener("abort",i)}}function o(h){let t=h,e=t?.status,m;try{let _=JSON.parse(t?.body??"{}");if(typeof _.code==="string")m=_.code}catch{}let s=m==="media_lease_expired";return{status:e,code:m,expired:s,denied:!s&&[401,403,404,410].includes(e)}}class Th{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(h,t,e,m,s=ih(),_={now:ih,setTimeout:(u,i)=>setTimeout(u,i),clearTimeout:(u)=>clearTimeout(u)}){this.seconds=h;this.renew=t;this.ended=e;this.event=m;this.clock=_;this.deadline=s+h*1000-1000,this.watchExpiry(),this.schedule(Math.min(h*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(h,t){let{status:e,code:m}=o(t);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:h,status:e,code:m,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(h){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,h))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let h=this.clock.now();try{let t=await Promise.race([this.renew(),new Promise((e,m)=>{this.requestTimer=this.clock.setTimeout(()=>m(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(t?.lease_seconds!==void 0){if(!Number.isFinite(t.lease_seconds)||t.lease_seconds<10||t.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=t.lease_seconds}this.deadline=h+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(t){if(this.stopped)return;let e=o(t);if(e.denied||e.expired){this.finish(e.expired?"expired":"revoked",t);return}this.report("retrying",t),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(h,t){if(this.stopped)return;this.report(h,t),this.stop(),this.ended(h,t)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function lh(h){let t=new URL(h),e=["localhost","127.0.0.1","[::1]"].includes(t.hostname);if(t.username||t.password||t.search||t.hash||t.protocol!=="https:"&&!(t.protocol==="http:"&&e)||!t.pathname.endsWith("/runtime-browser"))throw Error("Invalid managed Telephony browser endpoint");return t}function it(h){if(!h.startsWith("/")||h.startsWith("//")||h.includes("..")||h.includes("\\"))throw Error("Invalid Telephony request path")}function ct(h,t){return new Promise((e)=>{let m=()=>{clearTimeout(s),h.removeEventListener("abort",m),e()},s=setTimeout(m,t);if(h.addEventListener("abort",m,{once:!0}),h.aborted)m()})}class kh{proxy;mode;fetchImpl;direct;info;discovery;credential;issuing;proxyOnly=!1;constructor(h,t="auto",e=globalThis.fetch,m){this.proxy=h;this.mode=t;this.fetchImpl=e;this.direct=m;this.fetchImpl=e.bind(globalThis)}get directBase(){return this.info?.browser_api_base}async prepare(){if(this.mode==="proxy"||this.mode==="auto"&&typeof location>"u")return;if(this.direct){lh(this.direct.baseURL),this.info={managed:!0,browser_api_base:this.direct.baseURL};return}if(this.discovery)return this.discovery;this.discovery=(async()=>{let h;try{h=await this.proxy.get("/runtime-endpoints")}catch(e){if(this.mode==="direct"||![404,405].includes(e.status??0))throw e;this.proxyOnly=!0;return}if(!h?.managed||!h.browser_api_base){if(this.proxyOnly=!0,this.mode==="direct")throw Error("Direct Telephony deployment unavailable");return}if(lh(h.browser_api_base),h.browser_media_base)lh(h.browser_media_base);if(h.browser_signaling_base)lh(h.browser_signaling_base);let t=new URL(this.proxy.mcpURL(),typeof location>"u"?void 0:location.href);if(!["https:","http:"].includes(t.protocol))throw Error("Invalid Telephony management gateway");this.info=h})();try{await this.discovery}catch(h){throw this.discovery=void 0,h}}async grant(){if(this.credential&&this.credential.expires_at*1000>Date.now()+30000)return this.credential;if(this.issuing)return this.issuing;let h=typeof location>"u"?new URL(this.proxy.mcpURL()).origin:location.origin;this.issuing=(async()=>{let t=this.direct?await this.direct.credentialProvider():await this.proxy.post("/runtime-browser-bootstrap",{origin:h});if(!t||typeof t.token!=="string"||!t.token||t.token.length>4096||!Number.isSafeInteger(t.expires_at)||t.expires_at*1000<=Date.now()||t.expires_at*1000>Date.now()+360000||t.endpoint!==this.directBase)throw Error("Invalid managed browser credential");return this.credential=t})();try{return await this.issuing}finally{this.issuing=void 0}}async request(h,t,e,m){if(this.mode==="proxy"||this.proxyOnly||this.mode==="auto"&&typeof location>"u")return h==="GET"?this.proxy.get(t,m):this.proxy.post(t,e,m);if(await this.prepare(),!this.directBase)return h==="GET"?this.proxy.get(t,m):this.proxy.post(t,e,m);it(t);let s=await this.grant(),_=new URL(this.directBase+t);if(this.proxy.projectId)_.searchParams.set("project_id",this.proxy.projectId);let u=new Headers(m?.headers);u.set("Authorization","Bearer "+s.token),u.set("Content-Type","application/json");let i=await this.fetchImpl(_,{...m,method:h,headers:u,body:e===void 0?void 0:JSON.stringify(e),credentials:"omit",redirect:"error"});if(!i.ok){if(i.status===403)this.credential=void 0;let f=(await i.text()).slice(0,65536);throw Object.assign(Error(`Telephony HTTP ${i.status}`),{status:i.status,body:f})}return await i.json()}mediaURL(h,t){if(!this.directBase)return h;let e=`/api/apps/telephony/_install/${this.proxy.installId}`,m=t==="webrtc"||t==="auto"?this.info?.browser_signaling_base:this.info?.browser_media_base,s=new URL((m||this.directBase)+h.pathname.slice(e.length));return s.search=h.search,s}subscribe(h,t,e){let m=new AbortController,s,_=()=>{m.abort(),s?.close(),e?.signal?.removeEventListener("abort",_)};if(e?.signal?.addEventListener("abort",_,{once:!0}),e?.signal?.aborted)_();return(async()=>{try{if(await this.prepare(),m.signal.aborted)return;if(!this.directBase){s=this.proxy.subscribe(h,t,e);return}it(h);let u=500;while(!m.signal.aborted){try{let i=await this.grant(),f=new URL(this.directBase+h);if(this.proxy.projectId)f.searchParams.set("project_id",this.proxy.projectId);let a=await this.fetchImpl(f,{headers:{Authorization:"Bearer "+i.token,Accept:"text/event-stream"},credentials:"omit",redirect:"error",signal:m.signal});if(!a.ok||!a.body){if(a.status===403)this.credential=void 0;throw Object.assign(Error(`Telephony stream HTTP ${a.status}`),{status:a.status})}u=500;let S=a.body.getReader(),M=new TextDecoder,g="";try{while(!m.signal.aborted){let{done:P,value:C}=await S.read();if(P)break;if(g+=M.decode(C,{stream:!0}),g=g.replace(/\r\n/g,`
`),g.length>65536)throw Error("Telephony notification frame too large");let w;while((w=g.indexOf(`

`))>=0){let b=g.slice(0,w);g=g.slice(w+2);let y=b.split(`
`).filter((r)=>r.startsWith("data:")).map((r)=>r.slice(5).trimStart()).join(`
`);if(y)try{t(JSON.parse(y))}catch{}}}}finally{await S.cancel().catch(()=>{})}}catch(i){if(m.signal.aborted)return;e?.onError?.(i),u=Math.min(1e4,u*2)}if(!m.signal.aborted)await ct(m.signal,u)}}catch(u){if(!m.signal.aborted)e?.onError?.(u)}})(),{close:_}}}function Dh(h){return h}var xh="0.12.23";var Ft=["rtt_ms","queue_ms","target_ms","buffered_bytes","underruns","dropped_ms","capture_sequence_gaps","playback_sequence_gaps","main_thread_max_pause_ms","worker_max_tick_gap_ms","capture_sent_ms","capture_muted_ms","capture_dropped_ms","playback_ingress_ms","playback_transport_dropped_ms","playback_source_dropped_ms","clock_uncertainty_ms","clock_sample_age_ms","stats_errors","stats_duration_ms","send_bitrate_bps","receive_bitrate_bps","receive_gap_ms","capture_age_ms","transit_ms","delivery_excess_ms","server_queue_ms","receiver_concealed_delta_ms","receiver_silent_concealed_delta_ms","receiver_concealed_window_ms","requested_playback_target_ms","requested_upload_bitrate_bps","receiver_ssrc","receiver_loss_delta","receiver_loss_window_ms","remote_receiver_ssrc","remote_receiver_loss_delta","remote_receiver_loss_window_ms","receiver_bytesReceived","receiver_packetsReceived","receiver_packetsLost","receiver_jitter","receiver_packetsDiscarded","receiver_concealedSamples","receiver_silentConcealedSamples","receiver_concealmentEvents","receiver_insertedSamplesForDeceleration","receiver_removedSamplesForAcceleration","receiver_totalSamplesReceived","receiver_audioLevel","receiver_totalAudioEnergy","receiver_totalSamplesDuration","receiver_nackCount","receiver_fecPacketsReceived","receiver_fecPacketsDiscarded","receiver_jitter_buffer_interval_ms","receiver_jitter_target_interval_ms","receiver_jitter_minimum_interval_ms","receiver_processing_interval_ms","receiver_totalProcessingDelay","receiver_totalSamplesDecoded","telemetry_skipped_samples","telemetry_skipped_auxiliary","telemetry_queue_age_ms","receiver_last_packet_age_ms","sender_bytesSent","sender_packetsSent","sender_headerBytesSent","sender_retransmittedPacketsSent","sender_retransmittedBytesSent","sender_nackCount","sender_targetBitrate","sender_send_delay_interval_ms","remote_receiver_packetsLost","remote_receiver_fractionLost","remote_receiver_jitter","remote_receiver_roundTripTime","remote_receiver_totalRoundTripTime","remote_receiver_roundTripTimeMeasurements","remote_sender_packetsSent","remote_sender_bytesSent","pair_availableOutgoingBitrate","pair_availableIncomingBitrate","pair_currentRoundTripTime","pair_totalRoundTripTime","pair_bytesSent","pair_bytesReceived","pair_requestsSent","pair_requestsReceived","pair_responsesSent","pair_responsesReceived","pair_consentRequestsSent","path_revision","codec_clock_rate","codec_channels"],Tt={receiver_processing:["unavailable","per_decoded_sample"],connection:["connected","reconnecting","closed"],context:["running","suspended","interrupted","closed"],muted:["true","false"],device_muted:["true","false"],track:["live","ended"],ice:["new","checking","connected","completed","disconnected","failed","closed"],dtls:["new","connecting","connected","closed","failed"],pair:["frozen","waiting","in-progress","failed","succeeded"],local_candidate:["host","srflx","prflx","relay"],remote_candidate:["host","srflx","prflx","relay"],protocol:["udp","tcp"],relay_protocol:["udp","tcp","tls"],codec:["audio/opus","audio/PCMU","audio/PCMA","pcm16"]};function Kh(h){let t={};for(let e of Ft)if(typeof h[e]==="number"&&Number.isFinite(h[e]))t[e]=Math.max(0,Math.min(h[e],1000000000000));return t}function jh(h){let t={};for(let[e,m]of Object.entries(Tt))if(typeof h[e]==="string"&&m.includes(h[e]))t[e]=h[e];return t}class ph{transport;serial=0;lastEmit=-1/0;previousAt;previous;incident;history=[];pending=[];counters={};constructor(h){this.transport=h}observe(h,t,e=performance.now(),m=new Date().toISOString(),s){if(!Number.isFinite(e)||!Number.isFinite(Date.parse(m)))return;if(this.previousAt!==void 0&&e<this.previousAt)this.lastEmit=-1/0,this.previous=void 0,this.counters={},this.incident=void 0;let _=Kh(h),u=jh(t),i=!1;for(let a of["underruns","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealmentEvents","path_revision","stats_errors"])if(_[a]!==void 0){if(this.counters[a]!==void 0&&_[a]>this.counters[a])i=!0;this.counters[a]=_[a]}let f={id:`s${++this.serial}`,timestamp:m,transport:this.transport,reason:i?"incident":"periodic",window_ms:Math.min(60000,Math.max(0,typeof s==="number"&&Number.isFinite(s)?s:this.previousAt===void 0?0:e-this.previousAt)),metrics:_,states:u};if(this.previousAt=e,i&&!this.incident)this.incident={sample:f,context:this.previous};if(e-this.lastEmit>=5000){if(this.incident){if(this.incident.context)this.enqueue({...this.incident.context,reason:"incident_context"});this.enqueue(this.incident.sample),this.incident=void 0}else this.enqueue(f);this.lastEmit=e}this.previous=f}enqueue(h){if(this.history.push(h),this.history.length>24)this.history.shift();if(this.pending.push(h),this.pending.length>8)this.pending.shift()}recent(){return this.history.map((h)=>({...h,metrics:{...h.metrics},states:{...h.states}}))}drain(){return this.pending.splice(0,2)}}function at(h){let t={...h,metrics:{},states:{},part_index:0,part_count:16},e=[],m=t;for(let s of["metrics","states"])for(let[_,u]of Object.entries(h[s])){let i={...m,[s]:{...m[s],[_]:u}};if(JSON.stringify(i).length>768)e.push(m),m={...t,metrics:{},states:{},[s]:{[_]:u}};else m=i}if(e.push(m),e.length>16)return[];return e.map((s,_)=>({...s,part_index:_,part_count:e.length}))}var pt=["receiver_loss_delta","remote_receiver_loss_delta","receiver_concealed_delta_ms","receiver_ssrc","remote_receiver_ssrc","rtt_ms","queue_ms","target_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealedSamples","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_jitter","buffered_bytes","main_thread_max_pause_ms","worker_max_tick_gap_ms","receiver_processing_interval_ms","path_revision","stats_errors"];function Ot(h){let t={};for(let e of pt)if(h.metrics[e]!==void 0){if(t[e]=h.metrics[e],at({...h,metrics:t}).length>2)delete t[e]}return{...h,metrics:t}}class wh{send;sendReport;now;auxiliary=[];skippedAuxiliary=0;skippedSamples=0;auxiliaryTurn=!1;pending=[];queuedAt=new Map;lastFull=-1/0;timer;report;reportAt=0;static maxAgeMS=12000;constructor(h,t,e=()=>performance.now()){this.send=h;this.sendReport=t;this.now=e}enqueueReport(h){if(this.report!==void 0)this.skippedAuxiliary++;this.report=h,this.reportAt=this.now(),this.start()}enqueueAuxiliary(h){let t=!!h?.diagnostics?.timing?.runtime;if(t){let e=this.auxiliary.length;this.auxiliary=this.auxiliary.filter((m)=>!m.runtime),this.skippedAuxiliary+=e-this.auxiliary.length}if(this.auxiliary.push({report:h,at:this.now(),runtime:t}),this.auxiliary.length>16)this.auxiliary.shift(),this.skippedAuxiliary++;this.start()}start(){if(!this.timer)this.timer=setInterval(()=>this.tick(),250)}discard(h){this.pending=this.pending.filter((t)=>t.id!==h),this.queuedAt.delete(h),this.skippedSamples++}enqueue(h){let t=this.now(),e=[...h].reverse().find((m)=>m.reason==="incident")??h.at(-1);if(this.skippedSamples+=Math.max(0,h.length-(e?1:0)),e){let m=e.reason==="periodic"&&t-this.lastFull>=30000;if(m)this.lastFull=t;let s=m?e:Ot(e);s={...s,metrics:{...s.metrics,telemetry_skipped_samples:this.skippedSamples,telemetry_skipped_auxiliary:this.skippedAuxiliary}};let _=at(s),u=this.pending[0]?.id;for(let i of this.queuedAt.keys())if(i!==u)this.discard(i);if(this.queuedAt.has(s.id))this.discard(s.id);if(_.length)this.pending.push(..._),this.queuedAt.set(s.id,t)}if(this.pending.length)this.start()}tick(){let h=this.now();for(let[t,e]of this.queuedAt)if(h-e>wh.maxAgeMS)this.discard(t);if(this.auxiliary=this.auxiliary.filter((t)=>{if(h-t.at>wh.maxAgeMS)return this.skippedAuxiliary++,!1;return!0}),this.report!==void 0&&h-this.reportAt>wh.maxAgeMS)this.report=void 0,this.skippedAuxiliary++;try{if(this.report!==void 0&&this.sendReport){if(this.sendReport(this.report,Math.max(0,h-this.reportAt)))this.report=void 0}else if(this.auxiliary.length&&this.sendReport&&(!this.pending.length||this.auxiliaryTurn)){if(this.sendReport(this.auxiliary[0].report,Math.max(0,h-this.auxiliary[0].at)))this.auxiliary.shift(),this.auxiliaryTurn=!1}else if(this.pending.length){let t=this.pending[0],e={...t.metrics,telemetry_queue_age_ms:Math.max(0,h-(this.queuedAt.get(t.id)??h))};if(this.send({...t,metrics:e})){if(this.pending.shift(),t.part_index===t.part_count-1)this.queuedAt.delete(t.id);this.auxiliaryTurn=!0}}}catch{}if(!this.pending.length&&!this.auxiliary.length&&this.report===void 0)clearInterval(this.timer),this.timer=void 0}stop(){clearInterval(this.timer),this.timer=void 0,this.pending=[],this.queuedAt.clear(),this.auxiliary=[],this.report=void 0}}class Oh{emit;now;timestamp;lastTick;suspendedAt;contextState;environmentCleanup=[];counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(h,t=()=>performance.now(),e=()=>new Date().toISOString()){this.emit=h;this.now=t;this.timestamp=e}observeEnvironment(){this.stopEnvironment();let h=(t,e,m)=>{if(!t)return;try{t.addEventListener(e,m),this.environmentCleanup.push(()=>t.removeEventListener(e,m))}catch{}};try{if(typeof document<"u"){h(document,"visibilitychange",()=>this.report("tab_visibility",document.visibilityState));for(let t of["freeze","resume"])h(document,t,()=>this.report("page_lifecycle",t));this.report("tab_visibility",document.visibilityState)}if(typeof window<"u")for(let t of["pagehide","pageshow"])h(window,t,()=>this.report("page_lifecycle",t));if(typeof navigator<"u")h(navigator.mediaDevices,"devicechange",()=>this.report("microphone","devices_changed"))}catch{}}stopEnvironment(){for(let h of this.environmentCleanup.splice(0))try{h()}catch{}}tick(){let h=this.now();if(this.lastTick!==void 0){let t=Math.max(0,h-this.lastTick-1000);if(t>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,t),this.report("main_thread","scheduling_gap",t)}this.lastTick=h}context(h){if(h===this.contextState)return;this.contextState=h;let t=this.now();if(h==="suspended"||h==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=t,this.counters.audio_context_suspend_count++;this.report("audio_context",h)}else{let e;if(this.suspendedAt!==void 0)e=Math.max(0,t-this.suspendedAt),this.counters.audio_context_suspended_ms+=e,this.suspendedAt=void 0;this.report("audio_context",h,e)}}report(h,t,e){try{this.emit({timestamp:this.timestamp(),action:h,outcome:t,duration_ms:e===void 0?void 0:Math.round(e)})}catch{}}}async function J(h,t,e){let m=performance.now(),s=(_)=>{try{t?.({timestamp:new Date().toISOString(),action:h,outcome:_,duration_ms:Math.round(Math.max(0,performance.now()-m))})}catch{}};s("started");try{let _=await e();return s("completed"),_}catch(_){throw s("failed"),_}}var Mt=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function Vh(h){let t=(h??"FR").trim().toUpperCase();return Mt[t]??Mt.FR}function Qt(h,t){let e=[];if(!(h.cadence.reduce((_,u)=>_+u,0)>0)||!(t>0))return e;let s=0;while(s<t)for(let _=0;_<h.cadence.length;_+=2){let u=h.cadence[_]??0,i=h.cadence[_+1]??0;if(s>=t)break;if(u>0)e.push({start:s,end:Math.min(s+u,t)});s+=u+i}return e}function $h(h,t,e,m=0.12){let s=h.createGain();s.gain.value=0,s.connect(t);let _=m/Math.max(1,e.frequencies.length),u=e.frequencies.map((P)=>{let C=h.createOscillator();return C.type="sine",C.frequency.value=P,C.connect(s),C.start(),C}),i=h.currentTime+0.05,f=0,a=0.01,S=()=>{let P=h.currentTime-i+10;if(P<=f)return;for(let C of Qt(e,P)){if(C.end<=f)continue;let w=i+Math.max(C.start,f),b=i+C.end;s.gain.setValueAtTime(0,w),s.gain.linearRampToValueAtTime(_,w+a),s.gain.setValueAtTime(_,Math.max(w+a,b-a)),s.gain.linearRampToValueAtTime(0,b)}f=P};S();let M=setInterval(S,4000),g=!1;return()=>{if(g)return;g=!0,clearInterval(M);try{s.gain.cancelScheduledValues(0)}catch{}s.gain.value=0;for(let P of u){try{P.stop()}catch{}P.disconnect()}s.disconnect()}}var ah=24000,Qh=60;async function St(h,t){try{await h.audioWorklet.addModule(t)}catch(e){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:e})}}function Gt(h){let t=new Int16Array(h.length);for(let e=0;e<h.length;e++){let m=Math.max(-1,Math.min(1,h[e]));t[e]=m<0?m*32768:m*32767}return t.buffer}class gh{history=new Float32Array(64);phase=0;process(h,t,e){if(t===e)return h;let m=new Float32Array(64+h.length);m.set(this.history),m.set(h,64);let s=t/e,_=Math.min(1,e/t)*0.9,u=[],i=this.phase;for(;i<h.length;i+=s){let f=i+32,a=0,S=0;for(let M=Math.ceil(f-32);M<=Math.floor(f+32);M++){let g=M-f,C=(Math.abs(g)<0.000000001?_:Math.sin(Math.PI*_*g)/(Math.PI*g))*(0.5+0.5*Math.cos(Math.PI*g/32));if(M>=0&&M<m.length)a+=m[M]*C,S+=C}u.push(S?a/S:0)}return this.phase=i-h.length,this.history=m.slice(-64),new Float32Array(u)}}function Bt(h){let t=0;for(let e=0;e<h.length;e++)t+=h[e]*h[e];return Math.sqrt(t/Math.max(1,h.length))}function yt(h,t){let e=new Map(h.map((m)=>[m.id,m]));for(let m of t){let s=e.get(m.id);if(!s||(!s.complete||m.complete)&&(!s.ended_at||!!m.ended_at)&&m.duration_ms>=s.duration_ms)e.set(m.id,{...m})}return[...e.values()].sort((m,s)=>m.started_at.localeCompare(s.started_at)).slice(-100)}function Ut(h,t){return h.map((e)=>e.ended_at?e:{...e,ended_at:e.observed_until,end_reason:t,complete:!1})}var hh={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function I(h){if(h.mediaTransport!==void 0&&!["websocket","webrtc","auto"].includes(h.mediaTransport))throw RangeError("Unsupported softphone media transport");if(h.webrtcFec!==void 0&&typeof h.webrtcFec!=="boolean")throw RangeError("webrtcFec must be boolean");let t=h.playbackTargetMs??Qh,e=h.playbackMinMs??Math.min(Qh,t),m=h.playbackMaxMs??280;for(let s of[t,e,m])if(!Number.isFinite(s)||s<40||s>280)throw RangeError("Playback buffers must be between 40 and 280 ms");if(e>t||t>m)throw RangeError("Playback buffers require min <= target <= max");if(h.playbackAdaptive!==void 0&&typeof h.playbackAdaptive!=="boolean")throw RangeError("playbackAdaptive must be boolean");return{initialTargetMs:t,minTargetMs:e,maxTargetMs:m,hardMaxMs:320,adaptiveReserve:h.playbackAdaptive!==!1}}function Mh(h){return{...h.inputDeviceId?{deviceId:{exact:h.inputDeviceId}}:{},echoCancellation:h.echoCancellation,noiseSuppression:h.noiseSuppression,autoGainControl:h.autoGainControl}}function qt(h){let t=h.getSettings();return{deviceLabel:h.label||"Default microphone",sampleRate:typeof t.sampleRate==="number"?t.sampleRate:null,channelCount:typeof t.channelCount==="number"?t.channelCount:null,echoCancellation:typeof t.echoCancellation==="boolean"?t.echoCancellation:null,noiseSuppression:typeof t.noiseSuppression==="boolean"?t.noiseSuppression:null,autoGainControl:typeof t.autoGainControl==="boolean"?t.autoGainControl:null}}function Rh(h){if(!Number.isFinite(h)||h<=0)return null;return 20*Math.log10(h)}function Wt(h,t,e){let m=new ArrayBuffer(44+t*2),s=new DataView(m),_=(i,f)=>{for(let a=0;a<f.length;a++)s.setUint8(i+a,f.charCodeAt(a))};_(0,"RIFF"),s.setUint32(4,36+t*2,!0),_(8,"WAVE"),_(12,"fmt "),s.setUint32(16,16,!0),s.setUint16(20,1,!0),s.setUint16(22,1,!0),s.setUint32(24,e,!0),s.setUint32(28,e*2,!0),s.setUint16(32,2,!0),s.setUint16(34,16,!0),_(36,"data"),s.setUint32(40,t*2,!0);let u=44;for(let i of h)for(let f=0;f<i.length;f++)s.setInt16(u,i[f],!0),u+=2;return new Blob([m],{type:"audio/wav"})}class Ih{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new gh;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(h,t=!0){this.onLevel=h;this.recordAudio=t}async start(h,t){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:Mh(t)}),this.ensureOpen();let e=this.stream.getAudioTracks()[0];if(!e)throw Error("No microphone audio track was returned.");this.settings=qt(e);try{this.ctx=new AudioContext({sampleRate:ah,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await St(this.ctx,h),this.ensureOpen();let m=this.ctx.sampleRate,s=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:t.inputGainDB,highpassFilter:t.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(_)=>{if(this.stopped)return;if(!(_.data instanceof Float32Array)){if(_.data.type==="capture.stats")this.peak=Math.max(this.peak,_.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,_.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,_.data.limiter_reduction_db??0);return}let u=_.data;if(m!==ah)u=this.resampler.process(u,m,ah);let i=Bt(u);if(this.onLevel?.(i),this.recordAudio)this.frames.push(new Int16Array(Gt(u)));if(this.samples+=u.length,i>=0.005){for(let f=0;f<u.length;f++)this.activeSquares+=u[f]*u[f];this.activeSamples+=u.length}},s.connect(this.capture),this.settings}catch(e){throw await this.release(),e}}async stop(){this.stopped=!0;let h=this.settings,t=this.frames,e=this.samples,m=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,s=this.peak;if(await this.release(),!h||e===0)throw Error("No microphone audio was captured.");return{audio:Wt(t,e,ah),durationMs:Math.round(e*1000/ah),sampleRate:ah,activeRmsDbfs:Rh(m),peakDbfs:Rh(s),postPeakDbfs:Rh(this.postPeak||s),limiterReductionDb:this.limiterReductionDB,settings:h}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let h of this.stream?.getTracks()??[])h.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class dh{callbacks;clientEpoch=crypto.randomUUID();transportTelemetry=new ph("websocket");transportSender=new wh((h)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[h]}})}),!0},(h)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify(h)}),!0});worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new Oh((h)=>this.recordSessionEvent(h));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;sessionReady=!1;mediaPathReady;mediaRevision=0;callAnswered=!1;playbackHeld=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={mediaTransport:"websocket",codec:"pcm16",rttMs:null,queueMs:0,targetMs:Qh,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:ah,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:hh.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(h={}){this.callbacks=h}get isMuted(){return this.muted}async start(h,t,e,m=hh){let s=I(m);this.diagnostics.targetMs=s.initialTargetMs,this.callbacks.onState?.("connecting"),this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+xh.replaceAll(".","_")}),this.runtimeTelemetry.observeEnvironment(),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{this.stream=await J("microphone_device",(f)=>this.recordSessionEvent(f),()=>navigator.mediaDevices.getUserMedia({audio:Mh(m)})),this.ensureOpen();let _=this.stream.getAudioTracks()[0];if(!_)throw Error("No microphone audio track was returned.");if(_.readyState==="ended")throw Error("Microphone disconnected before audio setup.");_.onmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser.")},_.onunmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),this.callbacks.onNotice?.("Microphone input restored.")},_.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let u=qt(_);this.diagnostics={...this.diagnostics,microphoneSampleRate:u.sampleRate??0,microphoneChannelCount:u.channelCount??0,echoCancellation:u.echoCancellation,noiseSuppression:u.noiseSuppression,autoGainControl:u.autoGainControl,micInputGainDb:m.inputGainDB};try{this.ctx=new AudioContext({sampleRate:ah,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await J("audio_context",(f)=>this.recordSessionEvent(f),()=>this.ctx.resume());if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),m.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(m.outputDeviceId),this.ensureOpen();await J("audio_worklet",(f)=>this.recordSessionEvent(f),()=>St(this.ctx,t)),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let i=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:m.inputGainDB,highpassFilter:m.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:{...s,telemetryEpoch:this.clientEpoch,telemetryEnabled:!1}}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),i.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=m.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await J("audio_transport",(f)=>this.recordSessionEvent(f),()=>this.openWorker(h,e)),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.setPlaybackObservation(this.ctx?.state==="running"&&this.microphoneTransportReady&&!this.playbackHeld,"audio_context_paused"),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running")this.updateLiveState()},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(_){if(this.closed)this.teardown();if(!this.closed)this.fail(_ instanceof Error?_.message:"browser audio setup failed");throw _}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){await this.ctx?.resume(),this.updateLiveState()}setOutputVolume(h){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,h))}sendDTMF(h){if(/^[0-9*#]+$/.test(h))this.sendText(JSON.stringify({type:"dtmf",digits:h}))}startRingback(h){if(this.closed||!this.ctx||this.ringback)return;this.ringback=$h(this.ctx,this.ctx.destination,Vh(h))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(h)=>{let t=h.data;if(t?.type!=="capture.stats")return;this.micLevel=this.muted?0:t.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:Rh(t.active_rms??0),micPeakDbfs:Rh(t.pre_peak??0),micPostPeakDbfs:Rh(t.post_peak??0),micInputGainDb:t.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:t.limiter_reduction_db??0}},this.playback.port.onmessage=(h)=>{let t=h.data;if(t?.type==="playback.underrun"){this.diagnostics.underrunEvents=yt(this.diagnostics.underrunEvents??[],[t.event]),this.diagnostics.underrunDurationMs=t.underrun_ms,this.diagnostics.underruns=t.underruns;return}if(t?.type!=="stats")return;this.diagnostics.playbackEvents=t.playback_events,this.playbackTiming={reserve_expanded_ms:t.reserve_expanded_ms,reserve_compressed_ms:t.reserve_compressed_ms,reserve_adjustments:t.reserve_adjustments,reserve_match_rejections:t.reserve_match_rejections,played_ms:t.played_ms,max_residence_ms:t.max_residence_ms,drop_totals_ms:t.drop_totals_ms,coaching:{played_ms:t.whisper_played_ms,dropped_ms:t.whisper_dropped_ms,max_queue_ms:t.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,t.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:t.whisper_played_ms??0,coachingDroppedMs:t.whisper_dropped_ms??0,coachingMaxQueueMs:t.whisper_max_queue_ms??0,queueMs:t.queue_ms??0,targetMs:t.target_ms??Qh,underruns:t.underruns??0,droppedMs:t.dropped_ms??0,underrunDurationMs:t.underrun_ms??0,underrunEvents:yt(this.diagnostics.underrunEvents??[],t.underrun_events??[]),maxQueueMs:t.max_queue_ms??0,playbackSequenceGaps:t.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,t.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(h,t){return new Promise((e,m)=>{let s=new Worker(t);this.worker=s;let _=new MessageChannel,u=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:_.port1},[_.port1]),this.playback?.port.postMessage({type:"transport",port:u.port1},[u.port1]);let i=!1,f=setTimeout(()=>{if(i)return;i=!0,m(Error("audio connection timed out"))},1e4),a=(S)=>{if(i)return;if(i=!0,clearTimeout(f),S)m(S);else e()};this.cancelWorkerStart=()=>a(Error("audio session closed")),s.onmessage=(S)=>{if(this.closed){a(Error("audio session closed"));return}let M=S.data;if(M?.type==="socket.open")this.reportedPlaybackIDs.clear(),this.reportedCaptureQueueIDs.clear(),this.opened=!0,this.mediaSocketConnected=!0,this.mediaPathReady=void 0,this.mediaRevision=0,this.sessionReady=!1,this.startRTTProbe(),a();else if(M?.type==="runtime.event")this.recordSessionEvent(M.event);else if(M?.type==="socket.message")this.handleControl(M.data);else if(M?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL(M.recovery).then((g)=>{if(!this.closed&&this.worker===s)s.postMessage({type:"socket.credentials",id:M.id,mediaURL:g})},(g)=>{if(this.closed||this.worker!==s)return;let P=o(g);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:P.denied?"revoked":"retrying",...M.recovery,status:P.status,code:P.code}),s.postMessage({type:"socket.credentials",id:M.id,denied:P.denied})})}else if(M?.type==="socket.error")this.recordSessionEvent({timestamp:M.timestamp,action:"websocket",outcome:"transport_error"});else if(M?.type==="socket.close")if(this.recordSessionEvent({timestamp:M.timestamp,action:"websocket",outcome:"closed",code:String(M.code),detail:M.reason,was_clean:M.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"transport_disconnected"),this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else a(Error("audio connection closed before it was ready"));else if(M?.type==="socket.failed")this.mediaSocketConnected=!1,a(Error(M.detail||"audio connection lost")),this.fail(M.detail||"audio connection lost",M.recoverable===!0);else if(M?.type==="transport.drop"&&M.event)this.diagnostics.dropEvents=this.mergeDropEvents(M.event);else if(M?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=M.buffered_bytes??0,this.diagnostics.captureQueueEvents=M.capture_queue_events,this.transportTiming=M.timing??{},M.observation)this.transportTelemetry.observe({...this.transportTiming,...M.observation,rtt_ms:M.timing?.rtt_ms,queue_ms:this.diagnostics.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:M.buffered_bytes,underruns:this.diagnostics.underruns,dropped_ms:this.diagnostics.droppedMs,capture_sequence_gaps:this.diagnostics.captureSequenceGaps,playback_sequence_gaps:this.diagnostics.playbackSequenceGaps,main_thread_max_pause_ms:this.runtimeTelemetry.counters.main_thread_max_pause_ms},{connection:this.mediaSocketConnected?"connected":"reconnecting",context:this.ctx?.state,muted:String(this.muted),device_muted:String(this.stream?.getAudioTracks()[0]?.muted),track:this.stream?.getAudioTracks()[0]?.readyState,codec:"pcm16"},performance.now(),M.observation?.timestamp,M.observation?.window_ms);if(this.diagnostics.transportSamples=this.transportTelemetry.recent(),typeof M.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(M.timing.rtt_ms)}},s.onerror=(S)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:S.message?.slice(0,160)}),a(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},s.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:h,contextRate:this.ctx?.sampleRate??ah,muted:this.muted,capturePort:_.port2,playbackPort:u.port2},[_.port2,u.port2])})}mergeDropEvents(h,t){if(h)this.workerDropEvents=[...this.workerDropEvents,h].slice(-100);if(t)this.playbackDropEvents=t.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((e,m)=>e.timestamp.localeCompare(m.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;shutdownIntent="session_cleanup";reportedPlaybackIDs=new Set;reportedCaptureQueueIDs=new Set;handleControl(h){if(this.closed)return;try{let t=JSON.parse(h);if(t.type==="media.ready"||t.type==="media.not_ready"){let e=t;if(!Number.isSafeInteger(e.revision)||e.revision<=this.mediaRevision)return;this.mediaRevision=e.revision,this.mediaPathReady=e.type==="media.ready";try{this.callbacks.onMediaReadiness?.(e)}catch{}if(!this.mediaPathReady&&this.callAnswered)this.callbacks.onState?.("reconnecting","Connecting carrier audio…");this.updateLiveState()}else if(t.type==="ready")this.sessionReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState();else if(t.type==="dtmf.error"||t.type==="dtmf.sent")this.callbacks.onNotice?.(t.type==="dtmf.sent"?"Keypad tone sent":t.detail||"Keypad tone failed");else if(t.type==="pong"){if(this.diagnostics.captureSequenceGaps=t.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof t.nonce==="number"&&t.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-t.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(t.type==="call.ended"||t.type==="session.replaced"){this.setPlaybackObservation(!1,t.type==="call.ended"?"call_ended":"observation_ended"),this.shutdownIntent=t.type==="call.ended"?"call_ended":"session_replaced",this.closed=!0;try{this.callbacks.onState?.("ended",t.type)}finally{this.teardown()}}else if(t.type==="call.status"&&typeof t.call_id==="string"&&typeof t.status==="string"){if(t.hold_state)this.playbackHeld=t.hold_state!=="active",this.setPlaybackObservation(!this.playbackHeld&&this.microphoneTransportReady&&this.ctx?.state==="running",this.playbackHeld?"hold":"observation_resumed");if(t.hold_state&&t.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callAnswered=["answered","in-progress"].includes(t.status),this.updateLiveState(),this.callbacks.onCallStatus?.(t)}else if(t.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:t.detail?.slice(0,160)}),this.fail(t.detail||"The call could not be connected.");else if(t.type==="coach.state")this.callbacks.onNotice?.(t.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(t.type==="audio.health"){let e=t;if(e.state!=="healthy"&&e.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:e.state,reason:e.reason,stages:e.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),e.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&e.state==="healthy"&&e.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(t.type==="media.delivery"){let e=t.state;if(e==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(e==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=e==="stalled"}else if(t.type==="peer.disconnected")this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"carrier_disconnected"),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(t.type==="peer.connected")this.microphoneTransportReady=!0,this.setPlaybackObservation(this.ctx?.state==="running"&&!this.playbackHeld,"carrier_connected"),this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState()}catch{}}updateLiveState(){if(this.sessionReady&&this.callAnswered&&this.microphoneTransportReady&&this.mediaPathReady!==!1&&this.ctx?.state==="running")this.callbacks.onState?.("live")}startRTTProbe(){this.stopRTTProbe();let h=()=>{this.sendDiagnostics()};h(),this.pingTimer=setInterval(h,5000)}sendText(h){this.worker?.postMessage({type:"send.text",data:h})}recordSessionEvent(h){try{h={...this.callbacks.sessionDiagnostics?.(),...h}}catch{}h=qh(h),this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],h].slice(-50);try{this.callbacks.onSessionEvent?.(h)}catch{}try{let t={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,session_events:[h]}};if(new TextEncoder().encode(JSON.stringify(t)).byteLength<=1100)this.transportSender.enqueueAuxiliary(t);else this.transportSender.skippedAuxiliary++}catch{}}sendDiagnostics(){let h=this.diagnostics,t=(s,_)=>{let u=(s??[]).slice(-64),i=u.filter((f)=>!_.has(f.id)).slice(0,8);if(this.mediaSocketConnected){let f=new Set(u.map((a)=>a.id));for(let a of _)if(!f.has(a))_.delete(a);for(let a of i)_.add(a.id)}return i},e=t(h.playbackEvents,this.reportedPlaybackIDs),m=t(h.captureQueueEvents,this.reportedCaptureQueueIDs);if(this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{media_transport:"websocket",codec:"pcm16",client_version:xh,client_epoch:this.clientEpoch,playback_events:e,capture_queue_events:m,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:h.rttMs,playback_queue_ms:h.queueMs,playback_target_ms:h.targetMs,playback_max_queue_ms:h.maxQueueMs,playback_underruns:h.underruns,playback_underrun_ms:h.underrunDurationMs,playback_underrun_events:h.underrunEvents,playback_dropped_ms:h.droppedMs,websocket_buffered_bytes:h.websocketBufferedBytes,audio_context_rate:h.audioContextRate,microphone_sample_rate:h.microphoneSampleRate,microphone_channel_count:h.microphoneChannelCount,echo_cancellation:h.echoCancellation,noise_suppression:h.noiseSuppression,auto_gain_control:h.autoGainControl,mic_active_rms_dbfs:h.micActiveRmsDbfs,mic_peak_dbfs:h.micPeakDbfs,mic_post_peak_dbfs:h.micPostPeakDbfs,mic_input_gain_db:h.micInputGainDb,mic_limiter_reduction_db:h.micLimiterReductionDb,capture_sequence_gaps:h.captureSequenceGaps,playback_sequence_gaps:h.playbackSequenceGaps,drop_events:h.dropEvents}})),this.mediaSocketConnected)this.transportSender.enqueue(this.transportTelemetry.drain());this.callbacks.onDiagnostics?.({...h})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setPlaybackObservation(h,t){try{if(this.playback?.port.postMessage({type:"playback.telemetry.boundary",active:h,reason:t,audio_time_ms:(this.ctx?.currentTime??0)*1000}),!h)this.diagnostics.underrunEvents=Ut(this.diagnostics.underrunEvents??[],t)}catch{}}setMuted(h){let t=this.muted!==h;if(this.muted=h,this.worker?.postMessage({type:"muted",value:h}),this.capture?.port.postMessage({type:"muted",value:h}),h)this.sendText(JSON.stringify({type:"interrupt"}));if(t)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:h?"muted":"unmuted"})}stop(){this.shutdownIntent="user_stop";let h=!this.closed;this.closed=!0;try{if(h)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(h,t=!1){if(this.shutdownIntent="audio_error",!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:h.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",h,{recoverable:t})}finally{this.teardown()}}teardown(){this.runtimeTelemetry.stopEnvironment(),this.transportSender.stop(),this.setPlaybackObservation(!1,"observation_ended"),this.stopRingback(),this.microphoneTransportReady=!1,this.sessionReady=this.callAnswered=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let h=this.worker;if(h?.postMessage({type:"close",reason:this.shutdownIntent}),h){let t=setTimeout(()=>h.terminate(),600);h.onmessage=(e)=>{if(e.data?.type==="socket.shutdown.complete")clearTimeout(t),h.terminate()}}this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((t)=>t.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var p0=ah*Qh/1000;var Xh=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
`;var gt=`// Passive receiver only. Both directions share one rendering clock and delay.
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
`;var nh=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
`;var rt=new WeakMap,Yh=new Map,Vt=8000;async function Gh(h,t){let e=t==="worklet"?Xh:t==="worker"?nh:gt;if(typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin){let a=[URL.createObjectURL(new Blob([e],{type:"text/javascript"}))];return{urls:a,dispose(){a.splice(0).forEach((S)=>URL.revokeObjectURL(S))}}}if(h.name!=="telephony"||!h.projectId||!Number.isSafeInteger(h.installId)||h.installId<=0)throw Error("Telephony audio requires a project and installation");let m=new URL(h.mcpURL(),location.href),s=`${m.origin}:${m.pathname}:${h.projectId}:${h.installId}`,_=rt.get(h);if(!_||_.scope!==s)_={scope:s,entries:new Map},rt.set(h,_);let u=_.entries,i=t,f=u.get(i);if(!f){f=(async()=>{let S=Yh.get(t);if(!S)S=crypto.subtle.digest("SHA-256",new TextEncoder().encode(e)).then((C)=>Array.from(new Uint8Array(C),(w)=>w.toString(16).padStart(2,"0")).join("")),Yh.set(t,S),S.catch(()=>{if(Yh.get(t)===S)Yh.delete(t)});let M=`/ui/frontend/${t}-${await S}.js`,g=new AbortController,P;try{if(await Promise.race([h.get(M,{cache:"force-cache",redirect:"error",signal:g.signal,headers:{Accept:"text/plain"}}),new Promise((b,y)=>{P=setTimeout(()=>{y(Error("Telephony audio asset preparation timed out")),g.abort()},Vt)})])!==e)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let w=new URL(`/api/apps/telephony/_install/${h.installId}${M}`,m);return w.searchParams.set("project_id",h.projectId),w.searchParams.set("install_id",String(h.installId)),w.href}finally{clearTimeout(P)}})(),u.set(i,f);let a=f;a.catch(()=>{if(u.get(i)===a)u.delete(i)})}return{urls:[await f],dispose(){}}}function Bh(h=!1){let t=(h?[Xh]:[Xh,nh]).map((e)=>URL.createObjectURL(new Blob([e],{type:"text/javascript"})));return{urls:t,dispose(){t.splice(0).forEach((e)=>URL.revokeObjectURL(e))}}}async function rh(h,t=!1){if(typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin)return Bh(t);let e=await Promise.all(t?[Gh(h,"worklet")]:[Gh(h,"worklet"),Gh(h,"worker")]);return{urls:e.flatMap((m)=>m.urls),dispose(){e.forEach((m)=>m.dispose())}}}class Uh{options;target;bitrate=32000;stable;last=-1/0;initial;maximum;constructor(h={}){this.options=h;this.initial=h.playbackTargetMs??60,this.target=this.initial,this.maximum=Math.max(this.initial,h.playbackMaxMs??160)}observe(h,t=performance.now()){if(t<this.last)this.last=-1/0,this.stable=void 0;if(t-this.last<2000)return;this.last=t;let e=h.receiver_jitter===void 0?void 0:h.receiver_jitter*1000,m=h.receiver_concealed_delta_ms??0;if(this.options.playbackAdaptive!==!1){let u=Math.min(this.maximum,Math.max(this.initial,this.initial+Math.ceil(Math.max(0,(e??0)-10)*2/20)*20,m>0?this.target+20:this.initial));if(u>this.target)this.target=Math.min(u,this.target+20),this.stable=void 0;else if(e!==void 0&&m===0&&e<10){if(this.stable??=t,t-this.stable>=1e4)this.target=Math.max(this.initial,this.target-10),this.stable=t}else this.stable=void 0}let{pair_availableOutgoingBitrate:s,remote_receiver_fractionLost:_}=h;if(s!==void 0&&s>0&&s<64000)this.bitrate=Math.max(16000,Math.min(32000,Math.floor((s-32000)*0.8/1000)*1000));else if(_!==void 0&&_>=0.03)this.bitrate=Math.max(16000,this.bitrate-4000);else if(_!==void 0&&_<0.01)this.bitrate=Math.min(32000,this.bitrate+2000)}}class vh{tokens=1100;at;rate=640;observeAvailableBitrate(h){this.rate=h!==void 0&&h>=96000?2000:640}consume(h,t=performance.now()){if(!Number.isFinite(h)||!Number.isFinite(t)||h<0||h>1100)return!1;if(this.at!==void 0)this.tokens=Math.min(1100,this.tokens+Math.max(0,t-this.at)*this.rate/1000);if(this.at=t,this.tokens<h)return!1;return this.tokens-=h,!0}}function zh(h){if(h===void 0)return"websocket";if(!["websocket","webrtc","auto"].includes(h))throw RangeError("Unsupported softphone media transport");return h}function oh(h,t){if(t!==void 0&&typeof t!=="boolean")throw RangeError("webrtcFec must be boolean");let e=new URL(h);if(e.searchParams.set("transport","webrtc"),e.searchParams.delete("webrtc_fec"),t!==void 0)e.searchParams.set("webrtc_fec",String(t));return e.toString()}var $=(h)=>{try{h?.()}catch{}},yh=(h,t=1000000000000)=>typeof h==="number"&&Number.isFinite(h)?Math.max(0,Math.min(h,t)):0;function $t(h){return h===!0?"relay_configured":h===!1?"no_relay":"unreported"}function Wh(h){let t=o(h);return t.denied||t.code==="media_session_changed"||t.code==="call_ended"||t.code==="call_not_attachable"||t.expired||["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes(h?.name)}function ht(h,t){let e=0,m=0,s=0,_=0,u=0,i=0,f=0,a=0,S=0,M=null,g,P,C,w={},b={},y={},r=(q,A)=>typeof q?.[A]==="number"&&Number.isFinite(q[A])?Math.max(0,q[A]):void 0,V=(q,A,E)=>{for(let R of E){let F=r(q,R);if(F!==void 0)b[A+R]=F}},B=(q,A,E,R,F)=>{let W=r(q,E),c=r(q,R),Q=t?.counters?.[A];if(W!==void 0&&c!==void 0){if(w[A]??={},w[A][E]=W,w[A][R]=c,r(q,"ssrc")!==void 0)w[A].ssrc=q.ssrc;if(Q&&Q.ssrc===r(q,"ssrc")&&W>=Q[E]&&c>Q[R])b[F]=(W-Q[E])/(c-Q[R])*1000}},O=(q,A,E)=>{let R=r(q,"packetsLost"),F=r(q,"ssrc"),W=r(q,"timestamp"),c=t?.counters?.[A];if(F!==void 0)b[E+"ssrc"]=F;if(R===void 0||W===void 0)return;if(w[A]??={},w[A].packetsLost=R,w[A].at=W,F!==void 0)w[A].ssrc=F;if(c&&c.ssrc===F&&R>=c.packetsLost&&W>c.at)b[E+"loss_delta"]=(b[E+"loss_delta"]??0)+(R-c.packetsLost),b[E+"loss_window_ms"]=W-c.at},N=(q,A,E)=>{let R=r(q,"concealedSamples"),F=r(q,"silentConcealedSamples"),W=r(q,"timestamp"),c=r(q,"ssrc"),Q=t?.counters?.[A];if(R===void 0||W===void 0)return;if(w[A]??={},Object.assign(w[A],{concealedSamples:R,at:W}),c!==void 0)w[A].ssrc=c;if(F!==void 0)w[A].silentConcealedSamples=F;if(Q&&Q.ssrc===c&&R>=Q.concealedSamples&&W>Q.at){if(b.receiver_concealed_delta_ms=(b.receiver_concealed_delta_ms??0)+(R-Q.concealedSamples)*1000/E,b.receiver_concealed_window_ms=W-Q.at,F!==void 0&&F>=Q.silentConcealedSamples)b.receiver_silent_concealed_delta_ms=(b.receiver_silent_concealed_delta_ms??0)+(F-Q.silentConcealedSamples)*1000/E}},D=!1,Sh=0;h.forEach((q,A)=>{if(S=Math.max(S,yh(q.timestamp,1000000000000000)),(q.kind??q.mediaType??h.get(q.localId)?.kind)==="audio"&&Sh++<32){if(q.type==="outbound-rtp")e+=yh(q.bytesSent),V(q,"sender_",["bytesSent","packetsSent","headerBytesSent","retransmittedPacketsSent","retransmittedBytesSent","nackCount","targetBitrate"]),B(q,A,"totalPacketSendDelay","packetsSent","sender_send_delay_interval_ms");if(q.type==="inbound-rtp"){O(q,A,"receiver_"),m+=yh(q.bytesReceived),s+=yh(q.packetsLost),_=Math.max(_,yh(q.jitter)*1000);let R=h.get(q.codecId),F=r(R,"clockRate")??48000;if(N(q,A,F),D||=r(q,"concealedSamples")!==void 0,u+=yh(q.concealedSamples)*1000/F,i+=yh(q.packetsDiscarded),f+=yh(q.jitterBufferDelay),a+=yh(q.jitterBufferEmittedCount),V(q,"receiver_",["bytesReceived","packetsReceived","packetsLost","jitter","packetsDiscarded","concealedSamples","silentConcealedSamples","concealmentEvents","insertedSamplesForDeceleration","removedSamplesForAcceleration","totalSamplesReceived","audioLevel","totalAudioEnergy","totalSamplesDuration","nackCount","fecPacketsReceived","fecPacketsDiscarded"]),B(q,A,"jitterBufferDelay","jitterBufferEmittedCount","receiver_jitter_buffer_interval_ms"),B(q,A,"jitterBufferTargetDelay","jitterBufferEmittedCount","receiver_jitter_target_interval_ms"),B(q,A,"jitterBufferMinimumDelay","jitterBufferEmittedCount","receiver_jitter_minimum_interval_ms"),V(q,"receiver_",["totalProcessingDelay","totalSamplesDecoded"]),y.receiver_processing=r(q,"totalSamplesDecoded")===void 0?"unavailable":"per_decoded_sample",B(q,A,"totalProcessingDelay","totalSamplesDecoded","receiver_processing_interval_ms"),r(q,"lastPacketReceivedTimestamp")!==void 0)b.receiver_last_packet_age_ms=Math.max(0,q.timestamp-q.lastPacketReceivedTimestamp);if(R){if(y.codec=R.mimeType,b.codec_clock_rate=F,r(R,"channels")!==void 0)b.codec_channels=R.channels}}if(q.type==="remote-inbound-rtp")V(q,"remote_receiver_",["packetsLost","fractionLost","jitter","roundTripTime","totalRoundTripTime","roundTripTimeMeasurements"]),O(q,A,"remote_receiver_");if(q.type==="remote-outbound-rtp")V(q,"remote_sender_",["packetsSent","bytesSent"]);let E=q.type==="outbound-rtp"?"bytesSent":q.type==="inbound-rtp"?"bytesReceived":void 0;if(E&&r(q,E)!==void 0)w[A]??={},w[A][E]=q[E],w[A].at=q.timestamp}if(q.type==="transport"&&q.selectedCandidatePairId){g=q.selectedCandidatePairId,y.dtls=q.dtlsState;let E=h.get(g);if(E){M=r(E,"currentRoundTripTime")!==void 0?E.currentRoundTripTime*1000:null,V(E,"pair_",["availableOutgoingBitrate","availableIncomingBitrate","currentRoundTripTime","totalRoundTripTime","bytesSent","bytesReceived","requestsSent","requestsReceived","responsesSent","responsesReceived","consentRequestsSent"]),y.pair=E.state;let R=h.get(E.localCandidateId),F=h.get(E.remoteCandidateId);if(R)P=R.protocol,C=R.candidateType,y.protocol=P,y.local_candidate=C,y.relay_protocol=R.relayProtocol;if(F)y.remote_candidate=F.candidateType}}});let eh=(q,A)=>{if(t?.counters){let R=0;for(let[F,W]of Object.entries(w)){let c=t.counters[F];if(c&&W[q]>=c[q]&&W.at>c.at)R+=(W[q]-c[q])*8000/(W.at-c.at)}return R}let E=t&&S>t.at?(S-t.at)/1000:0;return E?Math.max(0,A-(q==="bytesSent"?t.sent:t.received))*8/E:0},mh=(t?.pathRevision??0)+(g&&g!==t?.pairID?1:0);b.path_revision=mh;let d=eh("bytesSent",e),bh=eh("bytesReceived",m);return{previous:{at:S,sent:e,received:m,counters:w,pairID:g,pathRevision:mh},rtt:M,queueMs:b.receiver_jitter_buffer_interval_ms??0,metrics:Kh({...b,send_bitrate_bps:d,receive_bitrate_bps:bh}),states:jh(y),webrtc:{protocol:P,candidateType:C,sendBitrateBps:d,receiveBitrateBps:bh,packetsLost:s,jitterMs:_,concealedMs:D?u:void 0,packetsDiscarded:i,jitterBufferMs:a>0?f/a*1000:0,jitterBufferIntervalMs:b.receiver_jitter_buffer_interval_ms}}}function Xt(h,t=new Date().toISOString()){let e=[];for(let[m,s]of[["receiver_","carrier_to_operator"],["remote_receiver_","operator_to_carrier"]]){let _=h[m+"loss_delta"];if(_>0)e.push({timestamp:t,direction:s,reason:"webrtc_packet_loss",duration_ms:0,packet_count:_,ssrc:h[m+"ssrc"],window_ms:h[m+"loss_window_ms"]})}if(h.receiver_concealed_delta_ms>0)e.push({timestamp:t,direction:"carrier_to_operator",reason:"webrtc_concealment",duration_ms:Math.round(h.receiver_concealed_delta_ms),ssrc:h.receiver_ssrc,window_ms:h.receiver_concealed_window_ms});return e}class tt{callbacks;stopped=!1;generation=0;socket;pc;stream;context;capture;whisper;destination;speaker;remoteAudio;speakerMeter;carrierStalled=!1;deliveryDegraded=!1;ready=!1;answered=!1;mediaPathReady;mediaRevision=0;reportedLive=!1;peer=!1;muted=!1;whisperEpoch=null;whisperBase=null;whisperResampler=new gh;timer;retry;cancelSetup;recovering=!1;recoveryGeneration=0;recoveryContext;recoveryExpiry;recoveryRequest;ringback;options;workletURL;events=[];previous;sampling=new WeakSet;transportTelemetry=new ph("webrtc");transportSender=new wh((h)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[h]}})},(h,t=0)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let e=h;if(e?.diagnostics?.webrtc){let m=e.diagnostics.webrtc;return this.sendTelemetry({...e,diagnostics:{...e.diagnostics,webrtc:{...m,jitterBufferSampleAgeMs:(m.jitterBufferSampleAgeMs??0)+t}}})}return this.sendTelemetry(h)});controlBudget=new vh;quality;playbackReceiver;uploadSender;applyingBitrate=!1;appliedBitrate;statsErrors=0;lastStatsSend=-1/0;lastValidStatsAt=-1/0;clientEpoch=crypto.randomUUID();relayConfigured;nativeCounts;completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};runtime=new Oh((h)=>this.recordSessionEvent(h));diagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:null,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(h){this.callbacks=h}async start(h,t,e){if(this.options=t,this.workletURL=e,!e)throw Error("WebRTC requires the shared Telephony audio processor");if(typeof RTCPeerConnection!=="function")throw Error("WebRTC audio is unavailable in this browser");I(t),await this.connect(h)}current(h){return!this.stopped&&this.generation===h}guard(h){if(!this.current(h))throw Error("Audio session cancelled")}async connect(h){this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+xh.replaceAll(".","_")}),this.runtime.observeEnvironment();let t=++this.generation,e=this.options;for(let M of this.events.slice(-8))this.enqueueRTCEvents({session_events:[M]});for(let M of this.diagnostics.dropEvents.slice(-8))this.enqueueRTCEvents({drop_events:[M]});this.ready=this.peer=this.answered=this.reportedLive=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.previous=void 0,this.quality=new Uh(e),this.appliedBitrate=void 0;let m=await J("microphone_device",(M)=>this.recordSessionEvent(M),()=>navigator.mediaDevices.getUserMedia({audio:Mh(e)}));if(!this.current(t))throw m.getTracks().forEach((M)=>M.stop()),Error("Audio session cancelled");this.stream=m;let s=m.getAudioTracks()[0];if(!s)throw Error("No microphone audio track");let _=s.getSettings();this.diagnostics={...this.diagnostics,microphoneSampleRate:_.sampleRate??0,microphoneChannelCount:_.channelCount??1,echoCancellation:typeof _.echoCancellation==="boolean"?_.echoCancellation:null,noiseSuppression:_.noiseSuppression??null,autoGainControl:_.autoGainControl??null,micInputGainDb:e.inputGainDB},s.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),s.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),s.onended=()=>{if(this.current(t))this.disconnected(t,"microphone_ended")},this.context=new AudioContext({latencyHint:"interactive"});let u=this.context;if(u.onstatechange=()=>{this.runtime.context(u.state),this.updateLiveState()},this.runtime.context(u.state),await J("audio_context",(M)=>this.recordSessionEvent(M),()=>u.resume()),this.guard(t),e.outputDeviceId&&"setSinkId"in u)await u.setSinkId(e.outputDeviceId);await J("audio_worklet",(M)=>this.recordSessionEvent(M),()=>u.audioWorklet.addModule(this.workletURL)),this.guard(t),this.diagnostics.audioContextRate=u.sampleRate,this.capture=new AudioWorkletNode(u,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:e.inputGainDB,highpassFilter:e.highpassFilter}}),this.destination=u.createMediaStreamDestination(),this.destination.channelCount=1,u.createMediaStreamSource(m).connect(this.capture).connect(this.destination),this.capture.port.onmessage=(M)=>{if(M.data?.type==="capture.stats"){let g=(C)=>C>0?20*Math.log10(C):null;this.diagnostics.micActiveRmsDbfs=g(M.data.active_rms),this.diagnostics.micPeakDbfs=g(M.data.pre_peak),this.diagnostics.micPostPeakDbfs=g(M.data.post_peak),this.diagnostics.micLimiterReductionDb=M.data.limiter_reduction_db??0;let P=0;if(this.speakerMeter){let C=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(C),P=Math.sqrt(C.reduce((w,b)=>w+b*b,0)/C.length)}$(()=>this.callbacks.onLevels?.(yh(M.data.active_rms,1),P))}},this.speaker=u.createGain(),this.speaker.gain.value=e.outputVolume??1,this.speaker.connect(u.destination),this.speakerMeter=u.createAnalyser(),this.speakerMeter.fftSize=256;let i=u.createGain();i.gain.value=0,this.speaker.connect(this.speakerMeter).connect(i).connect(u.destination),this.whisper=new AudioWorkletNode(u,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:I(e)}),this.whisper.connect(this.speaker),this.gate();let f=new WebSocket(oh(h,e.webrtcFec));f.binaryType="arraybuffer",this.socket=f;let a=!1,S=!1;await J("audio_transport",(M)=>this.recordSessionEvent(M),()=>new Promise((M,g)=>{let P=(b)=>{if(S)return;S=!0,clearTimeout(C),this.cancelSetup=void 0,b?g(b):M()},C=setTimeout(()=>P(Error("WebRTC audio connection timed out")),18000);this.cancelSetup=()=>P(Error("Audio session cancelled"));let w=()=>{if(this.current(t)&&this.ready&&a)this.gate(),P(),this.updateLiveState()};f.onmessage=(b)=>{if(!this.current(t))return;if(b.data instanceof ArrayBuffer){this.playWhisper(b.data);return}if(typeof b.data!=="string")return;let y;try{y=JSON.parse(b.data)}catch{return}if(y.type==="webrtc.config"){if(this.relayConfigured=typeof y.relay_configured==="boolean"?y.relay_configured:void 0,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"ice_configuration",outcome:$t(y.relay_configured)}),this.pc){P(Error("Repeated WebRTC configuration"));return}(async()=>{let r=new RTCPeerConnection({iceServers:y.ice_servers??[]});this.pc=r,this.destination.stream.getAudioTracks().forEach((B)=>{let O=r.addTrack(B,this.destination.stream);this.uploadSender=O,this.applyUploadBitrate(O,32000,t)}),r.ontrack=(B)=>{if(!this.current(t))return;this.playbackReceiver=B.receiver,this.applyPlaybackTarget();let O=new MediaStream([B.track]),N=new Audio;N.muted=!0,N.srcObject=O,this.remoteAudio=N,N.play().catch(()=>$(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction."))),u.createMediaStreamSource(O).connect(this.speaker)},r.onconnectionstatechange=()=>{if(!this.current(t))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:r.connectionState}),r.connectionState==="connected")a=!0,w();if(r.connectionState==="failed"||r.connectionState==="closed")if(!S)P(Error("WebRTC connection failed"));else this.disconnected(t,"webrtc_"+r.connectionState)};let V=await r.createOffer();this.guard(t),await r.setLocalDescription(V),await new Promise((B,O)=>{if(r.iceGatheringState==="complete"){B();return}let N=setTimeout(()=>{r.onicegatheringstatechange=null,O(Error("ICE gathering timed out"))},7000);r.onicegatheringstatechange=()=>{if(r.iceGatheringState==="complete")clearTimeout(N),r.onicegatheringstatechange=null,B()}}),this.guard(t),f.send(JSON.stringify({type:"webrtc.offer",sdp:r.localDescription.sdp}))})().catch((r)=>P(r instanceof Error?r:Error("WebRTC setup failed")));return}if(y.type==="webrtc.answer"){this.pc?.setRemoteDescription({type:"answer",sdp:y.sdp}).catch(()=>P(Error("Invalid WebRTC answer")));return}if(y.type==="media.ready"||y.type==="media.not_ready"){if(!Number.isSafeInteger(y.revision)||y.revision<=this.mediaRevision)return;this.mediaRevision=y.revision,this.mediaPathReady=y.type==="media.ready",$(()=>this.callbacks.onMediaReadiness?.(y)),this.updateLiveState()}if(y.type==="ready")this.ready=!0,this.send({type:"media.capabilities",version:2,versions:[3],whisper:!0}),w();if(y.type==="peer.connected")this.peer=!0,this.gate(),w();if(y.type==="peer.disconnected")this.peer=!1,this.gate(),this.updateLiveState(),$(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));if(y.type==="call.status")this.answered=["answered","in-progress"].includes(y.status),w(),$(()=>this.callbacks.onCallStatus?.(y));if(y.type==="call.error")if(!S)P(Error(y.detail??"Carrier activation failed"));else this.stop(),$(()=>this.callbacks.onState?.("error",y.detail));if(y.type==="call.ended"||y.type==="session.replaced")if(!S)P(Error(y.type));else this.stop(),$(()=>this.callbacks.onState?.("ended",y.type));if(y.type==="audio.health"){if(this.diagnostics.audioHealth={state:y.state,reason:y.reason,stages:y.stages},$(()=>this.callbacks.onAudioHealth?.(y)),y.state==="audio_degraded"&&!this.deliveryDegraded)this.deliveryDegraded=!0,$(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));else if(this.deliveryDegraded&&y.state==="healthy"&&y.stages?.telephony_to_browser?.state==="healthy"&&u.state==="running")this.deliveryDegraded=!1,$(()=>this.callbacks.onNotice?.("Speech delivery restored."))}if(y.type==="media.delivery"){if(y.state==="stalled")$(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));else if(y.state==="flowing"&&this.carrierStalled)$(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));this.carrierStalled=y.state==="stalled"}if(y.type==="call.notice")$(()=>this.callbacks.onNotice?.(y.detail??y.reason??"Audio delivery notice"));if(y.type==="coach.state")this.whisperEpoch=y.talking&&Number.isInteger(y.epoch)?y.epoch:null,this.whisperBase=null,this.whisper?.port.postMessage({type:"whisper.clear"})},f.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"}),f.onclose=(b)=>{if(!this.current(t))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(b.code),was_clean:b.wasClean}),!S){P(Object.assign(Error("WebRTC signaling unavailable"),b.code===1008?{status:403}:{}));return}if(b.code===1008){this.stop(),$(()=>this.callbacks.onState?.("error","Media authorization ended"));return}this.disconnected(t,"signaling_closed")}})),this.guard(t),this.timer=setInterval(()=>{this.runtime.tick(),this.statistics(t)},1000),await this.statistics(t)}send(h){if(this.socket?.readyState===WebSocket.OPEN&&this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(h))}catch{}}updateLiveState(){let h=this.ready&&this.peer&&this.answered&&this.mediaPathReady!==!1&&this.pc?.connectionState==="connected"&&this.context?.state==="running";if(h===this.reportedLive)return;this.reportedLive=h,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"media_path",outcome:h?"ready":"waiting"}),$(()=>this.callbacks.onState?.(h?"live":"reconnecting",h?void 0:"Connecting carrier audio…"))}gate(){let h=!this.stopped&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!h}),this.destination?.stream.getAudioTracks().forEach((t)=>t.enabled=h)}async statistics(h){if(!this.current(h)||!this.pc||this.sampling.has(this.pc))return;let t=this.pc,e=performance.now();this.sampling.add(t);try{let m=ht(await t.getStats(),this.previous);if(!this.current(h)||this.pc!==t)return;if(this.previous=m.previous,this.lastValidStatsAt=performance.now(),this.nativeCounts={packetsLost:m.webrtc.packetsLost,packetsDiscarded:m.webrtc.packetsDiscarded,concealedMs:m.webrtc.concealedMs},m.webrtc.packetsLost+=this.completedCounts.packetsLost,m.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded,m.webrtc.concealedMs!==void 0)m.webrtc.concealedMs+=this.completedCounts.concealedMs;if(this.quality?.observe(m.metrics),this.applyPlaybackTarget(),this.uploadSender&&this.quality)this.applyUploadBitrate(this.uploadSender,this.quality.bitrate,h);if(m.metrics.requested_playback_target_ms=this.quality?.target??60,this.appliedBitrate!==void 0)m.metrics.requested_upload_bitrate_bps=this.appliedBitrate;this.controlBudget.observeAvailableBitrate(m.metrics.pair_availableOutgoingBitrate);let s=Xt(m.metrics);for(let u of s)this.enqueueRTCEvents({drop_events:[u]});this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,...s].slice(-100),this.diagnostics={...this.diagnostics,rttMs:m.rtt,queueMs:m.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,m.queueMs),targetMs:this.quality?.target??this.options?.playbackTargetMs??60,webrtc:m.webrtc,playbackSequenceGaps:m.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};let _=this.stream?.getAudioTracks()[0];this.transportTelemetry.observe({...m.metrics,rtt_ms:m.rtt,queue_ms:m.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:this.socket?.bufferedAmount??0,stats_errors:this.statsErrors,stats_duration_ms:performance.now()-e,main_thread_max_pause_ms:this.runtime.counters.main_thread_max_pause_ms},{...m.states,ice:t.iceConnectionState,connection:"connected",context:this.context?.state,muted:String(this.muted),device_muted:String(_?.muted),track:_?.readyState}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),$(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}catch{if(this.current(h)&&this.pc===t)this.statsErrors++,this.transportTelemetry.observe({stats_errors:this.statsErrors,stats_duration_ms:performance.now()-e},{ice:t.iceConnectionState,context:this.context?.state,muted:String(this.muted)}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),$(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}finally{this.sampling.delete(t)}}applyPlaybackTarget(){let h=this.playbackReceiver;if(!h||!this.quality)return;if("jitterBufferTarget"in h)try{h.jitterBufferTarget=this.quality.target}catch{}}async applyUploadBitrate(h,t,e){if(this.applyingBitrate||this.appliedBitrate===t)return;this.applyingBitrate=!0;try{let m=h.getParameters();if(m.encodings?.length){if(m.encodings[0].maxBitrate=t,await h.setParameters(m),this.current(e)&&this.uploadSender===h)this.appliedBitrate=t}}catch{}finally{this.applyingBitrate=!1}}sendTelemetry(h){if(!this.ready||this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let t=JSON.stringify(h),e=new TextEncoder().encode(t).byteLength+58;if(!this.controlBudget.consume(e))return!1;try{return this.socket.send(t),!0}catch{return!1}}enqueueRTCEvents(h){try{let t={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,...h}};if(new TextEncoder().encode(JSON.stringify(t)).byteLength+58>1100){this.transportSender.skippedAuxiliary++;return}this.transportSender.enqueueAuxiliary(t)}catch{this.transportSender.skippedAuxiliary++}}sendStatistics(){let h=this.stream?.getAudioTracks()[0];if(performance.now()-this.lastStatsSend>=5000)this.lastStatsSend=performance.now(),this.transportSender.enqueueReport({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,client_version:xh,relay_configured:this.relayConfigured,media_transport:"webrtc",codec:"opus",webrtc:{...Object.fromEntries(Object.entries(this.diagnostics.webrtc??{}).map(([t,e])=>[t,typeof e==="number"?Math.round(e*10)/10:e])),jitterBufferSampleAgeMs:Number.isFinite(this.lastValidStatsAt)?Math.max(0,performance.now()-this.lastValidStatsAt):void 0},connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:h?.readyState,microphone_device_muted:h?.muted,rtt_ms:this.diagnostics.rttMs===null?null:Math.round(this.diagnostics.rttMs),playback_queue_ms:Math.round(this.diagnostics.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:this.diagnostics.webrtc?.packetsLost}}),this.transportSender.enqueue(this.transportTelemetry.drain()),this.enqueueRTCEvents({timing:{runtime:this.runtime.counters}})}playWhisper(h){if(h.byteLength<17||h.byteLength>176||this.whisperEpoch===null||!this.context)return;let t=new DataView(h);if(t.getUint32(0,!0)!==827805761||t.getUint32(4,!0)!==this.whisperEpoch)return;let e=t.getFloat64(8,!0);if(!Number.isFinite(e))return;let m=performance.timeOrigin+performance.now()-e;this.whisperBase=this.whisperBase===null?m:Math.min(this.whisperBase,m);let s=Math.max(0,m-this.whisperBase);if(s>200)return;let _=new Float32Array(h.byteLength-16);for(let i=0;i<_.length;i++){let f=~t.getUint8(i+16)&255,a=((f&15)<<3)+132<<(f>>4&7);_[i]=(f&128?132-a:a-132)/32768}let u=this.context.sampleRate===8000?_:this.whisperResampler.process(_,8000,this.context.sampleRate);this.whisper?.port.postMessage({type:"whisper",frame:u,received_audio_ms:this.context.currentTime*1000-s},[u.buffer])}disconnected(h,t){if(!this.current(h)||this.recovering)return;this.recovering=!0;let e=Ph("automatic_retry");this.recoveryContext=e,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"started",code:t,...e}),this.cleanup(),$(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));let m=++this.recoveryGeneration,s=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===m;this.recoveryExpiry=setTimeout(()=>{if(!s())return;this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"timed_out",...this.recoveryContext}),++this.recoveryGeneration,this.recovering=!1,clearTimeout(this.retry),this.cancelSetup?.(),this.recoveryRequest?.abort(),this.cleanup(),$(()=>this.callbacks.onState?.("error","Network unavailable; waiting to restore audio to the existing call.",{recoverable:!0}))},30000);let _=async()=>{if(!s())return;let u={...e,attempt_id:Eh("attempt")};this.recoveryContext=u,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"attempt_started",code:t,...u});try{if(!this.callbacks.refreshMediaURL)throw Error("Fresh media authorization is required");this.recoveryRequest=new AbortController;let i=await this.callbacks.refreshMediaURL(u,this.recoveryRequest.signal);if(!s())return;if(await this.connect(i),!s())return;if(this.pc?.connectionState!=="connected")throw Error("WebRTC disconnected during recovery");clearTimeout(this.recoveryExpiry),this.recovering=!1,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected",...u})}catch(i){if(!s())return;this.cleanup();let f=o(i);if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:Wh(i)?"revoked":"attempt_failed",...u,status:f.status,code:f.code}),Wh(i)){clearTimeout(this.recoveryExpiry),this.recovering=!1,$(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return}this.retry=setTimeout(()=>void _(),1000)}};_()}recordSessionEvent(h){try{h={...this.callbacks.sessionDiagnostics?.(),...h}}catch{}h=qh(h),this.events=[...this.events,h].slice(-100),this.enqueueRTCEvents({session_events:[h]}),$(()=>this.callbacks.onSessionEvent?.(h))}setMuted(h){if(this.muted!==h)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:h?"muted":"unmuted"});if(this.muted=h,this.gate(),h)this.send({type:"interrupt"})}sendDTMF(h){if(!/^[0-9*#]+$/.test(h))throw Error("Invalid DTMF digits");this.send({type:"dtmf",digits:h})}setOutputVolume(h){if(!Number.isFinite(h)||h<0||h>1)throw RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=h;if(this.options)this.options.outputVolume=h}startRingback(h){if(this.stopRingback(),this.context&&this.speaker)this.ringback=$h(this.context,this.speaker,Vh(h))}stopRingback(){this.ringback?.(),this.ringback=void 0}cleanup(){if(this.runtime.stopEnvironment(),this.transportSender.stop(),this.nativeCounts){for(let m of["packetsLost","packetsDiscarded","concealedMs"])this.completedCounts[m]+=this.nativeCounts[m]??0;this.nativeCounts=void 0}++this.generation,this.ready=this.peer=!1,this.stopRingback(),clearInterval(this.timer),this.timer=void 0;let h=this.socket;if(this.socket=void 0,h){if(h.readyState===WebSocket.OPEN)try{h.send(JSON.stringify({type:"media.shutdown",reason:this.stopped?"session_cleanup":"audio_error"}))}catch{}h.onclose=h.onmessage=h.onerror=null,h.close(1000,"client_shutdown")}this.playbackReceiver=void 0,this.uploadSender=void 0;let t=this.pc;if(this.pc=void 0,t)t.onconnectionstatechange=t.ontrack=null,t.close();if(this.remoteAudio)this.remoteAudio.pause(),this.remoteAudio.srcObject=null,this.remoteAudio=void 0;this.stream?.getTracks().forEach((m)=>{m.onended=null,m.stop()}),this.stream=void 0,this.destination?.stream.getTracks().forEach((m)=>m.stop()),this.destination=void 0,this.capture?.disconnect(),this.capture=void 0,this.whisper?.disconnect(),this.whisper=void 0,this.whisperEpoch=null,this.whisperBase=null,this.whisperResampler=new gh;let e=this.context;if(this.context=void 0,e)e.onstatechange=null,e.close().catch(()=>{});this.speaker=void 0,this.speakerMeter=void 0}stop(){if(this.stopped)return;if(this.recovering)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"cancelled",...this.recoveryContext});this.stopped=!0,this.recoveryRequest?.abort(),++this.recoveryGeneration,clearTimeout(this.recoveryExpiry),clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup()}}function bt(h,t,e){let m,s=!1,_=!1,u=[];return{async start(i,f){let a=zh(f.mediaTransport),S=async(M)=>{if(s)throw Error("Audio session cancelled");let g=M();m=g,g.setMuted(_);try{for(let P of u.splice(0))try{g.recordSessionEvent?.(P)}catch{}if(s)throw Error("Audio session cancelled");if(await g.start(i,f),s)throw Error("Audio session cancelled")}catch(P){if(g.stop(),m===g)m=void 0;throw P}};if(a==="websocket"){await S(t);return}try{await S(e)}catch(M){if(a!=="auto"||s||Wh(M))throw M;$(()=>h.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"})),await S(t)}},stop(){s=!0,u.splice(0),m?.stop(),m=void 0},setMuted(i){_=i,m?.setMuted(i)},setOutputVolume(i){m?.setOutputVolume(i)},sendDTMF(i){m?.sendDTMF(i)},recordSessionEvent(i){if(m)m.recordSessionEvent?.(i);else if(!s){if(u.push(qh(i)),u.length>20)u.shift()}},startRingback(i){m?.startRingback?.(i)},stopRingback(){m?.stopRingback?.()}}}async function Pt(){return(await navigator.mediaDevices.enumerateDevices()).filter((t)=>t.kind==="audioinput").map((t,e)=>({deviceId:t.deviceId,label:t.label||`Microphone ${e+1}`}))}function wt(h,t){let e=new Ih(h,!1),m,s=!1,_,u=()=>{return s=!0,_??=(async()=>{try{await e.cancel()}finally{m?.dispose(),m=void 0}})()};return{async start(i={}){if(s)throw Error("Microphone preview is already used");s=!0;try{if(m=t?await rh(t,!0):Bh(!0),_)throw m.dispose(),m=void 0,Error("Microphone preview was cancelled");await e.start(m.urls[0],{...hh,...i})}catch(f){throw await u(),f}},stop:u}}function Yt(h){return{async preflight(t,e){await zt(h,t,e)},create(t){let e=new dh(t),m,s=!1,_=()=>{s=!0;try{e.stop()}finally{m?.dispose(),m=void 0}};return{async start(u,i){try{if(m=await J("audio_assets",(f)=>e.recordSessionEvent(f),()=>h?rh(h):Promise.resolve(Bh())),s)throw Error("Audio session was cancelled");await e.start(u,m.urls[0],m.urls[1],i)}catch(f){throw _(),f}},stop:_,recordSessionEvent:(u)=>e.recordSessionEvent(u),setMuted:(u)=>e.setMuted(u),sendDTMF:(u)=>e.sendDTMF(u),setOutputVolume:(u)=>e.setOutputVolume(u),startRingback:(u)=>e.startRingback(u),stopRingback:()=>e.stopRingback()}}}}function Ct(h){let t=Yt(h);return{preflight:(e,m)=>t.preflight(e,m),prepare:(e,m)=>At(h,e,m),create(e){return bt(e,()=>t.create(e),()=>{let m=new tt(e),s,_=!1;return{async start(u,i){try{if(s=await J("audio_assets",(f)=>m.recordSessionEvent(f),()=>h?rh(h,!0):Promise.resolve(Bh(!0))),_)throw Error("Audio session cancelled");await m.start(u,i,s.urls[0])}catch(f){throw m.stop(),s?.dispose(),s=void 0,f}},stop(){_=!0,m.stop(),s?.dispose(),s=void 0},setMuted:(u)=>m.setMuted(u),sendDTMF:(u)=>m.sendDTMF(u),setOutputVolume:(u)=>m.setOutputVolume(u),recordSessionEvent:(u)=>m.recordSessionEvent(u),startRingback:(u)=>m.startRingback(u),stopRingback:()=>m.stopRingback()}})}}}async function At(h,t,e){if(!h||typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin)return;await J("audio_assets_prepare",e,async()=>{(await rh(h,t.mediaTransport==="webrtc"||t.mediaTransport==="auto")).dispose()})}async function zt(h,t,e){await Promise.all([J("microphone_preflight",e,async()=>{(await navigator.mediaDevices.getUserMedia({audio:Mh(t)})).getTracks().forEach((s)=>s.stop())}),At(h,t,e)])}function Et(h,t){return{create(e){let m={...hh,...t},s=I(m),_=new Uh(m),u=[],i,f,a,S,M=!1,g=!1,P=!1,C=!1,w=!1,b=!1,y=0,r,V,B,O,N,D,Sh=!1,eh,mh,d=m.outputVolume??1,bh={},q=32000,A,E,R,F,W=[],c=(x)=>{if(i?.readyState===WebSocket.OPEN&&i.bufferedAmount<65536)i.send(JSON.stringify(x));else throw Error("Listener connection unavailable")},Q=()=>{if(M)throw Error("Listening cancelled")},T=()=>{let x=++y;if(w=b=!1,A?.(x,Error("Coaching cancelled")),F?.(),E=void 0,eh)clearInterval(eh);if(eh=void 0,N)N.onconnectionstatechange=null,N.close(),N=void 0;if(V?.disconnect(),V=void 0,B?.disconnect(),B=void 0,O?.disconnect(),O=void 0,r?.getTracks().forEach((G)=>{G.onended=null,G.stop()}),r=void 0,C&&g&&i?.readyState===WebSocket.OPEN)try{c({type:"coach.stop",generation:x})}catch{}e.onTalking?.(!1)},k=()=>T(),K=()=>{if(document.visibilityState!=="visible")T()},sh=()=>{if(M)return;if(T(),M=!0,R?.(),window.removeEventListener("blur",k),document.removeEventListener("visibilitychange",K),mh)clearInterval(mh);if(mh=void 0,i)i.onmessage=i.onclose=i.onerror=null,i.close(1000,"listener_shutdown");if(f)f.ontrack=f.onconnectionstatechange=null,f.close();for(let x of W)x.pause(),x.srcObject=null;if(S?.disconnect(),a)a.onstatechange=null,a.close().catch(()=>{});D?.dispose(),D=void 0},v=(x)=>{if(!M)sh(),e.onClose(x)},l=async(x,G)=>{if(await x.setLocalDescription(await x.createOffer()),await new Promise((H,p)=>{if(x.iceGatheringState==="complete"){H();return}let U=setTimeout(()=>{x.onicegatheringstatechange=null,p(Error("ICE gathering timed out"))},7000);x.onicegatheringstatechange=()=>{if(x.iceGatheringState==="complete"||!G())clearTimeout(U),x.onicegatheringstatechange=null,G()?H():p(Error("Coaching cancelled"))}}),!G())throw Error("Listening cancelled");return x.localDescription.sdp};return{async start(x,G){if(C=G?.coaching===!0,Q(),typeof RTCPeerConnection!=="function")throw Error("WebRTC audio unavailable");try{if(a=new AudioContext({latencyHint:"interactive"}),await a.resume(),Q(),m.outputDeviceId&&"setSinkId"in a)await a.setSinkId(m.outputDeviceId);if(Q(),S=a.createGain(),S.gain.value=d,S.connect(a.destination),a.onstatechange=()=>{if(a?.state==="suspended"||a?.state==="interrupted")v("listener_audio_paused")},await new Promise((U,Z)=>{let j=!1,X=!1,L=(_h)=>{if(j)return;j=!0,clearTimeout(uh),R=void 0,_h?Z(_h):U()},uh=setTimeout(()=>L(Error("WebRTC listener timed out")),18000),Hh=()=>{if(P&&X&&!M&&!g)g=!0,L(),e.onReady("webrtc")};R=()=>L(Error("Listening cancelled")),i=new WebSocket(oh(x,m.webrtcFec)),i.onmessage=({data:_h})=>{if(M)return;if(typeof _h!=="string"){L(Error("Invalid WebRTC listener frame")),v("listener_protocol_error");return}let z;try{z=JSON.parse(_h)}catch{L(Error("Invalid listener control")),v("listener_protocol_error");return}if(z.type==="webrtc.config"){if(f||z.listener!==!0){L(Error("Invalid WebRTC listener configuration"));return}bh={iceServers:z.ice_servers??[]},q=z.bitrate??32000,f=new RTCPeerConnection(bh);let fh=f;for(let n=0;n<2;n++)fh.addTransceiver("audio",{direction:"recvonly"});fh.ontrack=(n)=>{if(M||!a||!S)return;u.push(n.receiver);let Zh=fh.getTransceivers().findIndex((Ch)=>Ch.receiver===n.receiver);if(Zh<0||Zh>1){v("listener_protocol_error");return}if("jitterBufferTarget"in n.receiver)try{n.receiver.jitterBufferTarget=s.initialTargetMs}catch{}let _t=new MediaStream([n.track]),Nh=new Audio;Nh.muted=!0,Nh.srcObject=_t,W.push(Nh),Nh.play().catch(()=>{});let ft=a.createMediaStreamSource(_t);if(t.stereo){let Ch=a.createStereoPanner();Ch.pan.value=Zh===0?-1:1,ft.connect(Ch).connect(S)}else{let Ch=a.createGain();Ch.gain.value=0.5,ft.connect(Ch).connect(S)}},fh.onconnectionstatechange=()=>{if(M)return;if(fh.connectionState==="connected")P=!0,Hh();if(fh.connectionState==="failed"||fh.connectionState==="closed"){if(L(Error("WebRTC listener connection failed")),j&&g)v("listener_network_error")}},l(fh,()=>!M).then((n)=>c({type:"webrtc.offer",sdp:n})).catch((n)=>L(n));return}if(z.type==="webrtc.answer"){f?.setRemoteDescription({type:"answer",sdp:z.sdp}).catch((fh)=>L(fh));return}if(z.type==="listener.ready"){if(C&&z.coaching!==!0){L(Error("Coaching not authorized"));return}X=!0,Hh();return}if(z.type==="coach.started"){A?.(z.generation);return}if(z.type==="coach.rejected"){A?.(y,Error(z.detail||"Coaching unavailable"));return}if(z.type==="coach.stopped"&&(z.generation===void 0||z.generation===y)){T();return}if(z.type==="coach.webrtc.answer"){E?.(z.sdp,z.generation);return}},i.onerror=()=>{if(L(Error("Listener connection failed")),g)v("listener_network_error")},i.onclose=(_h)=>{if(L(Error(_h.reason||"Listener disconnected")),g)v(_h.reason||"listener_disconnected")}}),Q(),C)window.addEventListener("blur",k),document.addEventListener("visibilitychange",K);let H,p=!1;mh=setInterval(()=>{if(M||!f||p)return;p=!0,f.getStats().then((U)=>{if(M)return;let Z=ht(U,H);H=Z.previous,_.observe({...Z.metrics,receiver_jitter:Z.webrtc.jitterMs/1000});for(let j of u)if("jitterBufferTarget"in j)try{j.jitterBufferTarget=_.target}catch{}e.onDiagnostics?.({media_transport:"webrtc",dropped_ms:[],played_ms:[],max_queue_ms:Z.queueMs,max_late_ms:0,sequence_gaps:[],network_excess_ms:0,network_dropped_ms:[],webrtc:Z.webrtc})}).catch(()=>{}).finally(()=>{p=!1})},1000)}catch(H){throw sh(),H}},async startTalking(){if(Q(),!C||!g||!a)throw Error("Coaching session not ready");if(w)return;w=!0;let x=++y,G=()=>!M&&w&&y===x;try{let H=await navigator.mediaDevices.getUserMedia({audio:Mh(m)});if(!G()){H.getTracks().forEach((U)=>U.stop());return}if(r=H,r.getTracks().forEach((U)=>{U.onended=()=>T()}),!D){let U=await rh(h,!0);if(!G()){U.dispose();return}D=U}if(!Sh)await a.audioWorklet.addModule(D.urls[0]),Sh=!0;if(!G())return;if(await new Promise((U,Z)=>{let j=setTimeout(()=>{A=void 0,Z(Error("Coaching activation timed out"))},3000);A=(X,L)=>{if(L||X===x)clearTimeout(j),A=void 0,L?Z(L):U()},c({type:"coach.start",generation:x})}),!G())return;eh=setInterval(()=>{if(G())try{c({type:"coach.keepalive",generation:x})}catch{T()}},1000),V=new AudioWorkletNode(a,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:m.inputGainDB,highpassFilter:m.highpassFilter}}),O=a.createMediaStreamDestination(),O.channelCount=1,B=a.createMediaStreamSource(H),B.connect(V).connect(O);let p=new RTCPeerConnection(bh);if(N=p,p.addTransceiver(O.stream.getAudioTracks()[0],{direction:"sendonly",streams:[O.stream],sendEncodings:[{maxBitrate:q}]}),await new Promise((U,Z)=>{let j=!1,X=(uh)=>{if(j)return;j=!0,clearTimeout(L),F=void 0,E=void 0,uh?Z(uh):U()},L=setTimeout(()=>X(Error("Coaching WebRTC timed out")),18000);F=()=>X(Error("Coaching cancelled")),E=(uh,Hh)=>{if(Hh!==x||!G())return;p.setRemoteDescription({type:"answer",sdp:uh}).catch((_h)=>X(_h))},p.onconnectionstatechange=()=>{if(!G())return;if(p.connectionState==="connected")X();if(p.connectionState==="failed"||p.connectionState==="closed")X(Error("Coaching media disconnected")),T()},l(p,G).then((uh)=>{if(G())c({type:"coach.webrtc.offer",generation:x,sdp:uh})}).catch((uh)=>X(uh))}),G())b=!0,e.onTalking?.(!0)}catch(H){if(G())throw T(),H}},stopTalking:T,stop:sh,setOutputVolume(x){if(!Number.isFinite(x)||x<0||x>1)throw RangeError("Listener volume must be 0–1");if(d=x,S)S.gain.value=x}}}}}function ch(h={},t={}){let e=["mediaTransport","webrtcFec","inputDeviceId","outputDeviceId","outputVolume","echoCancellation","noiseSuppression","autoGainControl","inputGainDB","highpassFilter","playbackTargetMs","playbackMinMs","playbackMaxMs","playbackAdaptive","stereo"],m={...hh};for(let s of[h,t])for(let _ of e){let u=s[_];if(u!==void 0)m[_]=u}if(I(m),m.mediaTransport=zh(m.mediaTransport),m.webrtcFec??=!0,m.outputVolume!==void 0&&(!Number.isFinite(m.outputVolume)||m.outputVolume<0||m.outputVolume>1))throw RangeError("Listener volume must be 0–1");return m}function Lt(h,t,e,m){let s,_=!1,u=!1,i,f={...h,onReady(a){u=!0,h.onReady(a)}};return{async start(a,S){if(_)throw Error("Listening cancelled");s=t==="websocket"?e(f):m(f);try{await s.start(a,S)}catch(M){if(s.stop(),_||u||t!=="auto"||Wh(M))throw M;s=e(f),await s.start(a,S)}if(_)s.stop();else if(i!==void 0)s.setOutputVolume(i)},async startTalking(){if(_||!s?.startTalking)throw Error("Coaching session not ready");await s.startTalking()},stopTalking(){s?.stopTalking?.()},stop(){_=!0,s?.stop()},setOutputVolume(a){if(!Number.isFinite(a)||a<0||a>1)throw RangeError("Listener volume must be 0–1");i=a,s?.setOutputVolume(a)}}}function et(h,t={}){let e=ch({},t),m=zh(e.mediaTransport),s=kt(h,e),_=Et(h,e);return{create(u){return Lt(u,m,(i)=>s.create(i),(i)=>_.create(i))}}}function Jt(h){if(h.byteLength<26||h.byteLength>984||h.byteLength%2)throw Error("Invalid listener audio frame");let t=new DataView(h),e=t.getUint32(4,!0);if(t.getUint32(0,!0)!==827085889||e>1)throw Error("Invalid listener audio protocol");let m=Number(t.getBigUint64(8,!0)),s=Number(t.getBigUint64(16,!0));if(!Number.isSafeInteger(m)||!Number.isSafeInteger(s))throw Error("Invalid listener audio timing");let _=new Float32Array((h.byteLength-24)/2);for(let u=0;u<_.length;u++)_[u]=t.getInt16(24+u*2,!0)/32768;return{direction:e,sequence:m,timestampMS:s,frame:_}}function Zt(h,t,e,m){if(h.length<1||h.length>480||!Number.isFinite(m)||m<0||!Number.isInteger(t)||t<1||t>4294967295)throw Error("Invalid coaching capture");let s=new ArrayBuffer(24+h.length*2),_=new DataView(s);_.setUint32(0,826496833,!0),_.setUint32(4,t,!0),_.setUint32(8,e>>>0,!0),_.setFloat64(16,m,!0);for(let u=0;u<h.length;u++){let i=Math.max(-1,Math.min(1,h[u]));_.setInt16(24+u*2,i<0?i*32768:i*32767,!0)}return s}function kt(h,t={}){return{create(e){let m,s,_,u,i=!1,f=[],a=!1,S=0,M=!1,g=!1,P,C,w,b,y,r,V,B=!1,O,N,D=0,Sh=!1,eh=[0,0],mh=[0,0],d=[void 0,void 0],bh=[new gh,new gh],q=t.outputVolume??1,A,E=()=>{let T=++S;if(M=!1,g=!1,V?.(T,Error("Coaching cancelled")),r)clearInterval(r);if(r=void 0,y?.port1.close(),y?.port2.close(),y=void 0,C?.disconnect(),C=void 0,w?.disconnect(),w=void 0,b?.disconnect(),b=void 0,P?.getTracks().forEach((k)=>{k.onended=null,k.stop()}),P=void 0,a&&Sh&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.stop",generation:T}));e.onTalking?.(!1)},R=()=>E(),F=()=>{if(document.visibilityState!=="visible")E()},W=()=>{if(i)return;if(E(),i=!0,A?.(),window.removeEventListener("blur",R),document.removeEventListener("visibilitychange",F),u)u.onmessage=u.onclose=u.onerror=null,u.close(1000,"listener_shutdown");if(s?.disconnect(),_?.disconnect(),m)m.onstatechange=null,m.close().catch(()=>{});for(let T of f.splice(0))T.dispose()},c=(T)=>{if(!i)W(),e.onClose(T)},Q=()=>{if(i)throw Error("Listening cancelled")};return{async start(T,k){a=k?.coaching===!0,Q();try{if(m=new AudioContext({latencyHint:"interactive"}),m.state==="suspended")await m.resume();if(Q(),t.outputDeviceId&&"setSinkId"in m)await m.setSinkId(t.outputDeviceId);Q();let K=await Gh(h,"listener");if(i)K.dispose(),Q();if(f.push(K),await m.audioWorklet.addModule(K.urls[0]),Q(),s=new AudioWorkletNode(m,"telephony-listener",{numberOfInputs:0,outputChannelCount:[t.stereo?2:1],processorOptions:{stereo:t.stereo,...I(t)}}),_=m.createGain(),_.gain.value=q,s.connect(_).connect(m.destination),s.onprocessorerror=()=>c("listener_audio_error"),s.port.onmessage=({data:sh})=>{if(sh?.type==="listener.diagnostics"&&!i)try{e.onDiagnostics?.({...sh,media_transport:"websocket",sequence_gaps:[...mh],network_excess_ms:D,network_dropped_ms:[...eh]})}catch{}},m.onstatechange=()=>{if(m?.state==="suspended"||m?.state==="interrupted")c("listener_audio_paused")},await new Promise((sh,v)=>{let l=!1,x=setTimeout(()=>{G(Error("Listener connection timed out")),c("listener_network_error")},1e4),G=(H)=>{if(l)return;l=!0,clearTimeout(x),A=void 0,H?v(H):sh()};A=()=>G(Error("Listening cancelled")),u=new WebSocket(T),u.binaryType="arraybuffer",u.onmessage=({data:H})=>{if(i||!m||!s)return;try{if(typeof H==="string"){let X=JSON.parse(H);if(X.type==="listener.ready"){if(a&&X.coaching!==!0)throw Error("Coaching not authorized");Sh=!0,G(),e.onReady("websocket")}else if(X.type==="coach.started")V?.(X.generation);else if(X.type==="coach.rejected")V?.(S,Error(X.detail||"Coaching unavailable"));else if(X.type==="coach.stopped"&&(X.generation===void 0||X.generation===S))E();return}if(!Sh||!(H instanceof ArrayBuffer))return;let p=Jt(H),U=p.direction;if(d[U]!==void 0&&p.sequence<d[U])return;if(d[U]!==void 0&&p.sequence>d[U])mh[U]+=p.sequence-d[U];d[U]=p.sequence+1;let Z=performance.now()-p.timestampMS;if(N=Math.min(N??Z,Z),D=Math.max(0,Z-N),D>200){eh[U]+=p.frame.length*1000/24000;return}O??=m.currentTime*1000+(t.playbackTargetMs??60)-p.timestampMS;let j=bh[U].process(p.frame,24000,m.sampleRate);s.port.postMessage({direction:U,frame:j,playAtMS:p.timestampMS+O},[j.buffer])}catch{G(Error("Invalid listener media")),c("listener_protocol_error")}},u.onerror=()=>{G(Error("Listener connection failed")),c("listener_network_error")},u.onclose=(H)=>{G(Error(H.reason||"Listener disconnected")),c(H.reason||"listener_disconnected")}}),Q(),a)window.addEventListener("blur",R),document.addEventListener("visibilitychange",F)}catch(K){throw W(),K}},async startTalking(){if(Q(),!a||!Sh||!m||!u||u.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(M)return;M=!0;let T=++S,k=()=>!i&&M&&T===S;try{let K=await navigator.mediaDevices.getUserMedia({audio:Mh({...hh,...t})});if(!k()){K.getTracks().forEach((l)=>l.stop());return}if(P=K,P.getAudioTracks().forEach((l)=>{l.onended=()=>E()}),!B){let l=await rh(h,!0);if(!k()){l.dispose();return}f.push(l),await m.audioWorklet.addModule(l.urls[0]),B=!0}if(!k())return;if(await new Promise((l,x)=>{let G=setTimeout(()=>{V=void 0,x(Error("Coaching activation timed out"))},3000);V=(H,p)=>{if(p||H===T)clearTimeout(G),V=void 0,p?x(p):l()},u.send(JSON.stringify({type:"coach.start",generation:T}))}),!k())return;C=new AudioWorkletNode(m,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:t.inputGainDB??0,highpassFilter:t.highpassFilter!==!1}}),w=m.createMediaStreamSource(K),b=m.createGain(),b.gain.value=0,w.connect(C).connect(b).connect(m.destination),y=new MessageChannel;let sh=0,v=new gh;y.port1.onmessage=({data:l})=>{if(!k()||!g||l?.type!=="capture"||!(l.frame instanceof Float32Array)||!m||!u)return;if(!Number.isFinite(l.timestamp_ms)||m.currentTime*1000-l.timestamp_ms>150||u.bufferedAmount>2880||u.readyState!==WebSocket.OPEN)return;let x=v.process(l.frame,l.sample_rate,24000);if(x.length>0&&x.length<=480)u.send(Zt(x,T,sh++,l.timestamp_ms))},y.port1.start(),C.port.postMessage({type:"transport",port:y.port2},[y.port2]),g=!0,e.onTalking?.(!0),r=setInterval(()=>{if(k()&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.keepalive",generation:T}))},1000)}catch(K){if(k())throw E(),K}},stopTalking:E,stop:W,setOutputVolume(T){if(!Number.isFinite(T)||T<0||T>1)throw RangeError("Listener volume must be 0–1");if(q=T,_)_.gain.value=T}}}}}class mt{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;requestAbort;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;audioOptions;constructor(h,t={}){this.client=h;this.options=t;this.audioOptions=ch(t.audio,t),this.runtime=t.runtime??et(h.app,this.audioOptions)}setAudioOptions(h){let t=ch({},h);this.audioOptions=t,this.runtime=this.options.runtime??et(this.client.app,t)}getAudioOptions=()=>Object.freeze({...this.audioOptions});getSnapshot=()=>this.snapshot;subscribe=(h)=>{return this.observers.add(h),()=>{this.observers.delete(h)}};update(h){this.snapshot=Object.freeze({...this.snapshot,...h});for(let t of this.observers)try{t(this.snapshot)}catch{}}async listen(h){return this.begin(h,!1)}async coach(h){return this.begin(h,!0)}async begin(h,t){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=t,this.retryUntil=0,this.retryDelay=500;let e=++this.generation;return this.update({state:"connecting",callId:h,detail:void 0,coaching:t,talking:!1,mediaTransport:void 0}),this.connect(h,e)}async connect(h,t){this.requestAbort?.abort(),this.requestAbort=new AbortController;let e,m=++this.attempt,s=()=>t===this.generation&&m===this.attempt&&!this.disposed;try{if(e=this.coaching?await this.client.coachSession(h):await this.client.listenSession(h,this.requestAbort?.signal),!s()){await this.client.stopListening(e).catch(()=>{});return}this.session=e;let _=this.runtime.create({onReady:(u)=>{if(s())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0,mediaTransport:u})},onClose:(u)=>{if(s())this.disconnected(u,h,t)},onTalking:(u,i)=>{if(s())this.update({talking:u,detail:i})},onDiagnostics:(u)=>{if(s())try{this.options.onDiagnostics?.(u)}catch{}}});if(this.audio=_,this.lease=new Th(e.lease_seconds,()=>this.client.renewListening(e),(u)=>{if(s())this.disconnected(u==="revoked"?"access_revoked":"media_disconnected",h,t,u==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,e.lease_started_ms),await _.start(this.client.listenerMediaURL(e,this.audioOptions.mediaTransport),{coaching:e.coaching===!0}),!s())_.stop()}catch(_){if(!s()||e&&this.session!==e)return;let u=_?.status,i;try{i=JSON.parse(_.body??"{}").code}catch{}let f=i==="call_ended"?"call_ended":u===401||u===403||u===404?"access_revoked":"listener_disconnected";throw this.disconnected(f,h,t,String(_)),_}}cleanup(){this.requestAbort?.abort(),this.requestAbort=void 0,this.lease?.stop(),this.lease=void 0;let h=this.audio;this.audio=void 0,h?.stop();let t=this.session;return this.session=void 0,t?this.client.stopListening(t).catch(()=>{}):Promise.resolve()}disconnected(h,t,e,m=h){if(e!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let s=h==="call_ended",_=h==="access_revoked",u=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(h);if(!this.coaching&&!s&&!_&&u&&this.options.reconnect!==!1){this.retryUntil||=Date.now()+30000;{this.update({state:"reconnecting",detail:m,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,e===this.generation&&!this.disposed)this.connect(t,e).catch(()=>{})},Date.now()<this.retryUntil?this.retryDelay:15000),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:s?"call_ended":_?"access_revoked":"disconnected",detail:m,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(h){this.audio?.setOutputVolume(h)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let h=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1,mediaTransport:void 0}),await h}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}var Dt={set:(h,t)=>setTimeout(h,t),clear:(h)=>clearTimeout(h)};class st{run;permanent;ended;event;clock;stopped=!1;active;timer;attempt=0;online=()=>this.wake();constructor(h,t,e,m,s=Dt){this.run=h;this.permanent=t;this.ended=e;this.event=m;this.clock=s;globalThis.addEventListener?.("online",this.online),this.schedule(1000)}emit(h){try{this.event?.(h)}catch{}}schedule(h){if(!this.stopped)this.clock.clear(this.timer),this.timer=this.clock.set(()=>void this.tick(),h)}wake(){if(!this.stopped&&!this.active)this.schedule(0)}async tick(){if(this.stopped||this.active)return;let h=new AbortController;this.active=h,this.attempt++,this.emit("attempt_started");let t;try{if(await Promise.race([this.run(h.signal),new Promise((e,m)=>{t=this.clock.set(()=>{h.abort(),m(Error("Audio recovery request timed out"))},30000)})]),this.stopped)return;this.emit("connected"),this.stop()}catch(e){if(this.stopped)return;if(this.permanent(e)){this.emit("revoked"),this.stop(),this.ended(e);return}this.emit("waiting_for_network"),this.schedule(Math.min(15000,1000*2**Math.min(4,this.attempt)))}finally{if(this.clock.clear(t),this.active===h)this.active=void 0}}stop(){if(this.stopped)return;this.stopped=!0,this.clock.clear(this.timer),this.active?.abort(),globalThis.removeEventListener?.("online",this.online)}}function xt(h){if(!h)return"placing";if(Lh(h))return"ended";if(h==="ringing")return"ringing";if(h==="answered"||h==="in-progress")return"connected";return"placing"}var th=(h)=>h instanceof Error?h.message:String(h);class ut{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;hasStatusPush=!1;automaticRecovery;hasLiveAudio=!1;uncertainAttachment;scheduleStatusPoll;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(h,t={}){this.client=h;this.options=t;if(this.runtime=t.audioRuntime??Ct(h.app),this.audioOptions={...hh,...t.audio,...t.mediaTransport?{mediaTransport:t.mediaTransport}:{}},I(this.audioOptions),this.interval=t.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}createCallListener(h={}){return this.assertOpen(),this.client.createCallListener({...this.audioOptions,...h.audio,...h})}getSnapshot=()=>this.snapshot;subscribe=(h)=>{return this.assertOpen(),this.listeners.add(h),()=>{this.listeners.delete(h)}};update(h){let t={...this.snapshot,...h};if(t.callId&&xt(t.carrierStatus)==="connected")t.phase=t.audioState==="live"?"connected":"placing";this.snapshot=Object.freeze(t);for(let e of this.listeners)try{e(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(h){if(this.disposed||h!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.automaticRecovery?.stop(),this.automaticRecovery=void 0,this.uncertainAttachment=void 0,this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(h){return!this.disposed&&h===this.generation}finish(h){if(this.current(h))this.update({busy:!1})}cancellable(h,t){let e=this.cancellation.signal;return new Promise((m,s)=>{let _=()=>{e.removeEventListener("abort",_),s(Error("Softphone operation cancelled"))};if(e.addEventListener("abort",_,{once:!0}),h.then(m,s).finally(()=>e.removeEventListener("abort",_)),!this.current(t)||e.aborted)_()})}begin(h){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||h&&this.session)throw Error("Softphone already has an active operation or call");let t=this.invalidate();return this.update({busy:!0,detail:void 0}),t}async prepareAudio(h,t){let e=[],m=(s)=>{if(this.current(h)&&!this.cancellation.signal.aborted)e.push(qh(s))};try{return await this.cancellable(J(t?"audio_preflight":"audio_prepare",m,()=>t?this.runtime.preflight(this.audioOptions,m):this.runtime.prepare?.(this.audioOptions,m)??Promise.resolve()),h),this.assertCurrent(h),e}catch(s){if(this.current(h))for(let _ of e)this.reportSessionEvent(_);throw s}}async dial(h){let t=this.begin(!0),e,m=!1;try{this.assertCurrent(t);let s={to:h.to.trim(),from:h.from?.trim(),timeout_sec:h.timeout_sec,recording:h.recording},_=JSON.stringify(s);if(!this.intent||this.intent.value!==_||h.idempotency_key&&h.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:_,request:{...s,idempotency_key:h.idempotency_key||crypto.randomUUID()}};let u=await this.prepareAudio(t,!0);return this.assertCurrent(t),e=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(t),m=!0,this.outbound=!0,await this.attachAudio(e,t,u),e.call_id}catch(s){if(e&&(this.current(t)||!m||this.disposed))await this.recoverStartup(e,t,()=>this.client.hangup(e.call_id));if(this.current(t))this.update({detail:th(s)});throw s}finally{this.finish(t)}}async answer(h,t={}){let e=this.begin(!0),m,s=!1;try{this.assertCurrent(e);let _=await this.prepareAudio(e,!0);m=await this.client.answer(h,t),this.assertCurrent(e),s=!0,this.outbound=!1,await this.attachAudio(m,e,_)}catch(_){if(m&&(this.current(e)||!s||this.disposed))await this.recoverStartup(m,e,()=>this.client.release(m));if(this.current(e))this.update({detail:th(_)});throw _}finally{this.finish(e)}}async recoverStartup(h,t,e){try{if(await e(),this.current(t))this.clearCall()}catch{if(this.current(t))this.session=h,this.update({callId:h.call_id,audioState:"error"}),this.startPolling()}}async join(h){await this.answer(h,{rejoin:!0})}async attach(h,t="attach"){await this.acquireSession(h,!1,t)}async takeover(h){await this.acquireSession(h,!0)}async acquireSession(h,t,e="takeover"){let m=this.begin(!0);try{let s=await this.prepareAudio(m,!0);this.assertCurrent(m);let _=Ph(t?"takeover":e),u=await(t?this.client.takeover(h,_):this.client.attach(h,_));this.assertCurrent(m),await this.attachAudio(u,m,s),await this.reconcileAttachedCall(u,m)}catch(s){if(this.current(m))this.update({detail:th(s)});throw s}finally{this.finish(m)}}async reconnect(h,t="manual_reconnect"){if(!this.session)throw Error("No call to reconnect");let e={...this.audioOptions,...h};I(e);let m=this.begin(!1);this.audioOptions=e;let s=this.session.call_id,_=Ph(t),u=this.session.session_generation,i=(f,a={})=>this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:f,..._,previous_session_id:u,...a});i("started");try{let f=await this.prepareAudio(m,!1),a;try{a=await this.acquireRecoverySession(this.session,{..._,previous_session_id:u},this.cancellation.signal)}catch(S){let M=o(S),g;try{g=JSON.parse(S.body??"{}").current_generation}catch{}if(t==="automatic_retry"||M.code!=="media_session_changed"||!g||!/^session-[a-f0-9-]{1,64}$/.test(g))throw S;this.assertCurrent(m),a=await this.client.attach(s,{...Ph(t),previous_session_id:g},{signal:this.cancellation.signal},g)}this.assertCurrent(m),await this.attachAudio(a,m,f),await this.reconcileAttachedCall(a,m),i("connected",{session_id:a.session_generation})}catch(f){if(i(this.current(m)?"failed":"cancelled"),this.current(m))this.update({detail:th(f)});throw f}finally{this.finish(m)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let h=this.session;if(!h){this.cancellation.abort();return}let t=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let e=(async()=>{try{if(await this.client.hangup(h.call_id),this.current(t))this.clearCall()}catch(m){if(this.current(t))this.update({audioState:"error",detail:th(m)}),this.startPolling();throw m}finally{this.finish(t)}})();this.hangingUp=e;try{await e}finally{if(this.hangingUp===e)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(h){this.assertOpen();let t=this.session;if(!t)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let e=this.generation;this.update({busy:!0,detail:void 0});let m;try{m=this.client[h](t.call_id)}catch(s){if(this.current(e)&&this.session===t)this.update({busy:!1,detail:th(s)});throw s}this.controlPending=m;try{let s=await m;if(this.current(e)&&this.session===t)this.update({holdState:s.hold_state,recordingState:s.recording_state,controlError:s.control_error,capabilities:s.capabilities});return s}catch(s){if(this.current(e)&&this.session===t)this.update({detail:th(s)});throw s}finally{if(this.controlPending===m)this.controlPending=void 0;if(this.current(e)&&this.session===t)this.update({busy:!1})}}configureAudio(h){this.assertOpen();let t={...this.audioOptions,...h};I(t),this.audioOptions=t}setMuted(h){this.assertOpen(),this.audio?.setMuted(h),this.update({muted:h})}setOutputVolume(h){if(this.assertOpen(),!Number.isFinite(h)||h<0||h>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=h,this.audio?.setOutputVolume(h)}sendDTMF(h){if(this.assertOpen(),!/^[0-9*#]+$/.test(h))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(h)}observeCall(h){if(this.disposed||h.id!==this.session?.call_id)return;if(h.direction)this.outbound=h.direction==="outbound";if(Lh(h.status)){this.invalidate(),this.clearCall(h.status),this.update({busy:!1,phase:"ended",termination:h.termination,answeredBy:h.answered_by??this.snapshot.answeredBy,endedAt:h.ended_at});return}this.update({carrierStatus:h.status,phase:xt(h.status),answeredBy:h.answered_by??this.snapshot.answeredBy,holdState:h.hold_state??this.snapshot.holdState,recordingState:h.recording_state??this.snapshot.recordingState,controlError:h.control_error??this.snapshot.controlError,capabilities:h.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let h=this.options.ringback;return typeof h==="object"&&h?h.country:void 0}syncRingback(){let h=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(h&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!h&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(h,t,e=[]){this.hasStatusPush=!1,this.assertCurrent(t),this.stopAudio(),this.session=h,this.update({callId:h.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let m,s=()=>!this.disposed&&m!==void 0&&this.audio===m,_=(f)=>{if(s())try{f()}catch{}},u,i;try{this.assertCurrent(t),m=this.runtime.create({refreshMediaURL:(f,a)=>{let S=f??Ph("automatic_retry");if(u)return this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"coalesced",...S,shared_attempt_id:i?.attempt_id}),u;if(!s()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let M=this.generation,g=(async()=>{let C=h.session_generation,w=await this.acquireRecoverySession(h,{...S,previous_session_id:C},a);if(this.assertCurrent(M),!s())throw Error("Audio connection no longer active");return h=w,this.session=w,this.startLease(w),this.reportSessionEvent({timestamp:new Date().toISOString(),action:"media_session",outcome:"replaced",...S,previous_session_id:C,session_id:w.session_generation}),this.client.mediaURL(w,this.audioOptions.mediaTransport)})();u=g,i=S;let P=()=>{if(u===g)u=void 0,i=void 0};return g.then(P,P),g},sessionDiagnostics:()=>({session_id:h.session_generation}),onSessionEvent:(f)=>_(()=>this.options.onSessionEvent?.(qh({...f,session_id:f.session_id??h.session_generation}))),onState:(f,a,S)=>{if(!s())return;if(f==="ended"&&a==="call.ended"){this.observeCall({id:h.call_id,status:"completed"});return}if(f==="live")this.hasLiveAudio=!0;if(this.update({audioState:f,detail:a}),this.scheduleStatusPoll?.(),s()&&(f==="error"||f==="ended")){if(this.stopAudio(),f==="error"){if(this.reconcileFailedAudio(h,this.generation),S?.recoverable)this.scheduleAudioRecovery()}}},onMediaReadiness:(f)=>_(()=>this.options.onMediaReadiness?.(f)),onLevels:(f,a)=>_(()=>this.options.onLevels?.(f,a)),onAudioHealth:(f)=>_(()=>this.options.onAudioHealth?.(f)),onDiagnostics:(f)=>_(()=>this.options.onDiagnostics?.(f)),onNotice:(f)=>_(()=>this.options.onNotice?.(f)),onCallStatus:(f)=>_(()=>{this.hasStatusPush=!0,this.observeCall({id:f.call_id,status:f.status,direction:f.direction,answered_at:f.answered_at,ended_at:f.ended_at,answered_by:f.answered_by,termination:f.termination,hold_state:f.hold_state,recording_state:f.recording_state,control_error:f.control_error}),this.scheduleStatusPoll?.()})}),this.audio=m,this.assertCurrent(t);for(let f of e)this.reportSessionEvent(f);if(this.assertCurrent(t),this.startPolling(),this.startLease(h),m.setMuted(this.snapshot.muted),await this.cancellable(m.start(this.client.mediaURL(h,this.audioOptions.mediaTransport),this.audioOptions),t),this.assertCurrent(t),!s())throw Error(this.snapshot.detail||"Audio connection ended during setup");m.setMuted(this.snapshot.muted),this.syncRingback()}catch(f){if(s())this.stopAudio();if(this.current(t))this.update({audioState:"error",detail:th(f)});throw f}}async acquireRecoverySession(h,t,e){let m=this.uncertainAttachment??{diagnostics:t,expected:h.attachment_guard_version===1?h.session_generation??"":void 0};if(e?.aborted)throw new DOMException("Cancelled","AbortError");if(this.uncertainAttachment=m,m.diagnostics.attempt_id!==t.attempt_id)this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"idempotent_retry",...t,shared_attempt_id:m.diagnostics.attempt_id});try{let s=await this.client.attach(h.call_id,m.diagnostics,{signal:e},m.expected);if(e?.aborted)throw new DOMException("Cancelled","AbortError");return this.uncertainAttachment=void 0,s}catch(s){let _=o(s).status;if(_!==void 0&&_>=400&&_<500)this.uncertainAttachment=void 0;throw s}}scheduleAudioRecovery(){if(this.disposed||!this.session||!this.hasLiveAudio||this.automaticRecovery)return;let h=this.session.call_id,t=this.generation;this.update({audioState:"reconnecting",detail:"Network unavailable; waiting to restore audio."});let e=(_)=>{let u=o(_);return u.denied||u.code==="media_session_changed"||u.code==="call_ended"||u.code==="call_not_attachable"||["NotAllowedError","NotFoundError"].includes(_?.name)},m=this.uncertainAttachment?.diagnostics??Ph("automatic_retry"),s=m;this.automaticRecovery=new st(async(_)=>{this.assertCurrent(t);let u=await Fh((S)=>this.client.getCall(h,S),_);if(!u)throw Object.assign(Error("Call unavailable"),{status:404});if(Lh(u.status)){this.observeCall(u);return}this.assertCurrent(t);let i=await this.prepareAudio(t,!1);if(_.aborted)throw new DOMException("Cancelled","AbortError");let f=await this.acquireRecoverySession(this.session,s,_);if(this.assertCurrent(t),_.aborted)throw new DOMException("Cancelled","AbortError");let a=()=>{if(this.current(t)&&this.session===f)this.stopAudio()};_.addEventListener("abort",a,{once:!0});try{if(await this.attachAudio(f,t,i),_.aborted)throw new DOMException("Cancelled","AbortError")}finally{_.removeEventListener("abort",a)}this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:"connected",...s}),this.automaticRecovery?.stop(),this.automaticRecovery=void 0},e,(_)=>{if(this.current(t))this.automaticRecovery=void 0,this.update({audioState:"error",detail:th(_)})},(_)=>{if(_==="attempt_started")s={...m,attempt_id:Eh("attempt")};this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:_,...s})})}async reconcileAttachedCall(h,t){try{let e=await this.client.getCall(h.call_id);if(this.current(t)&&this.session===h&&e)this.observeCall(e)}catch{}}async reconcileFailedAudio(h,t){try{let e=await this.client.getCall(h.call_id);if(!this.current(t)||this.session!==h||!e)return;if(e.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(e)}catch{}}startLease(h){if(this.lease?.stop(),!h.lease_seconds)return;this.lease=new Th(h.lease_seconds,()=>this.client.renew(h),(t,e)=>{if(this.disposed||this.session!==h)return;if(this.stopAudio(),this.update({audioState:"error",detail:t==="expired"?"Audio session expired. Reconnecting audio.":`Audio authorization ended: ${th(e??t)}`}),t==="expired")this.scheduleAudioRecovery();else this.automaticRecovery?.stop(),this.automaticRecovery=void 0},(t)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(t);else try{this.options.onSessionEvent?.(t)}catch{}},h.lease_started_ms)}reportSessionEvent(h){if(h=qh(h),this.audio?.recordSessionEvent)try{this.audio.recordSessionEvent(h)}catch{}else try{this.options.onSessionEvent?.(h)}catch{}}stopAudio(){this.lease?.stop(),this.lease=void 0;let h=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{h?.stopRingback?.()}catch{}}try{h?.stop()}catch{}if(h)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){this.scheduleStatusPoll=void 0,clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let h=new AbortController;this.polling=h;let t=!1,e=performance.now(),m=()=>this.hasStatusPush&&this.snapshot.audioState==="live"?Math.max(1,Math.max(this.interval,30000)-(performance.now()-e)):this.interval,s=async()=>{if(t)return;let _=this.session?.call_id;if(!_||h.signal.aborted)return;t=!0;try{let u=await this.client.getCall(_,h.signal);if(e=performance.now(),!h.signal.aborted&&u)this.observeCall(u)}catch(u){if(!h.signal.aborted)this.hasStatusPush=!1,this.update({detail:`Call status unavailable: ${th(u)}`})}finally{t=!1,this.scheduleStatusPoll?.()}};this.scheduleStatusPoll=()=>{if(t||h.signal.aborted||!this.session||this.disposed)return;clearTimeout(this.timer),this.timer=setTimeout(s,m())},this.scheduleStatusPoll()}clearCall(h){this.hasStatusPush=!1,this.stopAudio(),this.stopPolling(),this.session=void 0,this.hasLiveAudio=!1,this.outbound=!1,this.update({callId:void 0,carrierStatus:h,audioState:"idle",muted:!1,detail:void 0,phase:h?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function Kt(h,t="en"){if(h?.reason==="ai_inactivity")return t.toLowerCase().startsWith("fr")?"Fin de l’appel : aucune réponse":"Call ended: no response";if(h?.reason==="ai_policy_failure")return t.toLowerCase().startsWith("fr")?"Échec du rappel d’inactivité IA":"AI inactivity reminder failed";if(h?.reason==="time_limit"||h?.reason==="ai_max_duration")return t.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return h?.reason?.replaceAll("_"," ")??""}class Rt extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function jt(h){return h.direction==="inbound"&&h.status==="pending"&&h.answerable!==!1&&!h.routing_waiting&&(h.peer_kind==="human"||Boolean(h.ring_offers?.some((t)=>t.kind==="browser")))}var Lh=(h)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(h);function Y(h){if(!h||/[\s/\\?#]/.test(h))throw Error("Invalid call ID");return encodeURIComponent(h)}class Jh{app;options;transport;listMicrophones=Pt;createMicrophonePreview=(h)=>wt(h,this.app);constructor(h,t={}){this.app=h;this.options=t;if(this.transport=new kh(h,t.authProvider?"proxy":t.direct?"direct":t.runtime,globalThis.fetch,t.direct),h.name!=="telephony"||!h.projectId||!h.installId)throw Error("Telephony requires an explicit project and installation")}get(h,t){return this.transport.request("GET",h,void 0,t)}post(h,t,e){return this.transport.request("POST",h,t,e)}subscribe(h,t,e){return this.transport.subscribe(h,t,e)}path(h){if(!this.options.authProvider)return h;return`/user${h}${h.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(h){let t=await this.get(this.path("/calls"),{signal:h});if(!Array.isArray(t?.calls)||t.calls.some((e)=>!e||typeof e.id!=="string"||typeof e.status!=="string"))throw Error("Invalid Telephony calls response");return t.calls}async getCall(h,t){let e=await this.get(this.path(`/calls?call_id=${Y(h)}`),{signal:t});if(!Array.isArray(e?.calls))throw Error("Invalid Telephony call response");return e.calls.find((m)=>m.id===h)}incomingCalls(h){return h.filter(jt)}watchCalls(h,t={}){let e=t.intervalMs??2000;if(!Number.isFinite(e)||e<100)throw Error("Call watch interval must be at least 100 ms");let m=new AbortController,s,_,u=!1,i=!1,f=-1/0,a,S,M=performance.now(),g=()=>{let y=performance.now()-f;return y<15000?Math.max(1,Math.min(Math.max(e,30000)-(performance.now()-M),15000-y)):e},P=new Set,C=()=>{clearTimeout(s),m.abort(),_?.close(),t.signal?.removeEventListener("abort",C)},w=(y)=>{try{t.onError?.(y)}catch{C()}};if(t.signal?.addEventListener("abort",C,{once:!0}),t.signal?.aborted)C();let b=async(y)=>{if(m.signal.aborted)return;if(u){i=!0;return}clearTimeout(s),u=!0;let r=performance.now(),V=a;try{let B=await this.listCalls(m.signal);if(M=performance.now(),!m.signal.aborted){h(B),S=V;for(let O of B){if(O.answerable!==!0||O.status!=="pending")continue;for(let N of O.ring_offers??[]){if(N.kind!=="browser"||!N.id||P.has(N.id))continue;P.add(N.id),this.acknowledgeOffer(O.id,N.id).catch((D)=>{if(![404,405].includes(D?.status??0))P.delete(N.id)})}}try{t.onTiming?.({trigger:y,fetchMs:performance.now()-r})}catch{}}}catch(B){if(f=-1/0,S=void 0,!m.signal.aborted){let O=B?.status;try{t.onFailure?.({trigger:y,fetchMs:performance.now()-r,status:typeof O==="number"?O:void 0,error:B})}catch{}w(B)}}finally{if(u=!1,!m.signal.aborted)if(i)i=!1,b("push");else s=setTimeout(()=>void b("poll"),g())}};if(b("poll"),!m.signal.aborted&&t.push!==!1&&typeof this.app.subscribe==="function")try{_=this.subscribe(this.path("/calls/events"),(y)=>{if(y.type==="heartbeat"||y.type==="calls.changed"){if(f=performance.now(),y.type==="calls.changed"){let r=typeof y.state==="string"&&/^[0-9a-f]{64}$/.test(y.state)?y.state:void 0;if(a=r,!r||r!==S||u)b("push")}else if(!u)clearTimeout(s),s=setTimeout(()=>void b("poll"),g())}else if(y.type==="access.revoked")f=-1/0,_?.close(),b("push")},{transport:"fetch",signal:m.signal,reconnectDelayMs:250,onError:(y)=>{if(f=-1/0,!u&&!m.signal.aborted)clearTimeout(s),s=setTimeout(()=>void b("poll"),e);let r=y?.status;if(r===404||r===405||r===501)_?.close()}})}catch{}return{close:C}}async place(h){if(!/^\+[1-9]\d{7,14}$/.test(h.to)||!h.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let t=ih();return this.session(await this.post(this.path("/softphone/place"),h),void 0,"media",t)}async answer(h,t={}){let e=ih();try{return this.session(await this.post(this.path(`/softphone/answer/${Y(h)}`),t),h,"media",e)}catch(m){let s=m;if(s.status===409&&typeof s.body==="string"){let _;try{_=JSON.parse(s.body)}catch{}if(_?.code==="offer_expired")throw new Rt}throw m}}async acknowledgeOffer(h,t){await this.post(this.path(`/softphone/offer/ack/${Y(h)}`),{offer_id:t})}async declineOffer(h,t){await this.post(this.path(`/softphone/offer/decline/${Y(h)}`),{offer_id:t})}async attach(h,t,e,m){let s=ih();return this.session(await Fh((_)=>this.post(this.path(`/softphone/attach/${Y(h)}`),{...t?{media_diagnostics:t}:{},...m!==void 0&&t?.attempt_id?{attachment_guard:{id:t.attempt_id,expected_generation:m}}:{}},{...e,signal:_}),e?.signal??void 0),h,"media",s)}async takeover(h,t){let e=ih();return this.session(await this.post(this.path(`/softphone/takeover/${Y(h)}`),t?{media_diagnostics:t}:{}),h,"media",e)}createCallListener(h={}){return new mt(this,{...h,...ch({...this.options.audio,...h.audio},h)})}async listenSession(h,t){let e=ih();return this.session(await Fh((m)=>this.post(this.path(`/softphone/listen/${Y(h)}`),{},{signal:m}),t,5000,(m)=>{try{this.stopListening(this.session(m,h,"listen-media",e)).catch(()=>{})}catch{}}),h,"listen-media",e)}async coachSession(h){let t=ih(),e=this.session(await this.post(this.path(`/softphone/coach/${Y(h)}`),{}),h,"listen-media",t);if(e.coaching!==!0)throw Error("Invalid coaching session");return e}async renewListening(h){return this.post(this.path(`/softphone/${h.coaching?"coach-renew":"listen-renew"}/${Y(h.call_id)}`),{session_token:h.session_token})}async stopListening(h){await this.post(this.path(`/softphone/${h.coaching?"coach-stop":"listen-stop"}/${Y(h.call_id)}`),{session_token:h.session_token})}async listenerAudit(h){return this.get(this.path(`/softphone/listen-audit/${Y(h)}`))}outboundTransport(h){return this.post(this.path("/numbers/outbound-transport"),h)}outboundNumbers(h={}){return this.get(this.path(h.fresh?"/softphone/numbers?fresh=true":"/softphone/numbers"))}async renew(h){return this.post(this.path(`/softphone/renew/${Y(h.call_id)}`),{session_token:h.session_token})}async release(h){if(!h.session_token)throw Error("Answer session has no release token");await this.post(this.path(`/softphone/release/${Y(h.call_id)}`),{session_token:h.session_token})}async hangup(h){await this.post(this.path(`/calls/${Y(h)}/hangup`),{})}hold(h){return this.post(this.path(`/calls/${Y(h)}/hold`),{})}resume(h){return this.post(this.path(`/calls/${Y(h)}/resume`),{})}pauseRecording(h){return this.post(this.path(`/calls/${Y(h)}/pause-recording`),{})}resumeRecording(h){return this.post(this.path(`/calls/${Y(h)}/resume-recording`),{})}createSoftphone(h={}){return new ut(this,{...h,audio:{...this.options.audio,...h.audio}})}mediaURL(h,t){return this.resolveMediaURL(h,"media",t)}listenerMediaURL(h,t){return this.resolveMediaURL(h,"listen-media",t)}resolveMediaURL(h,t,e){let m=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),s=new URL(h.media_url,m),_=`/api/apps/telephony/_install/${this.app.installId}/softphone/${t}/${Y(h.call_id)}/`;if(s.origin!==m.origin||!["http:","https:"].includes(s.protocol)||s.username||s.password||s.search||s.hash||!s.pathname.startsWith(_)||!/^[A-Za-z0-9_-]+$/.test(s.pathname.slice(_.length)))throw Error("Invalid Telephony media endpoint");if(t==="listen-media"&&s.pathname.slice(_.length)!==h.session_token)throw Error("Listener credential mismatch");if(t==="media"&&Ah(h.session_generation))s.searchParams.set("session_generation",h.session_generation);let u=this.transport.mediaURL(s,e);return u.protocol=u.protocol==="https:"?"wss:":"ws:",u.href}session(h,t,e="media",m=ih()){let s=h;if(!s||typeof s.call_id!=="string"||typeof s.media_url!=="string"||t&&s.call_id!==t||s.session_token!==void 0&&typeof s.session_token!=="string")throw Error("Invalid Telephony session response");if(s.lease_seconds!==void 0&&(!Number.isFinite(s.lease_seconds)||s.lease_seconds<10||s.lease_seconds>3600||!s.session_token))throw Error("Invalid media lease");if(e==="listen-media"&&(!s.session_token||s.lease_seconds===void 0))throw Error("Invalid listener lease");return Y(s.call_id),this.resolveMediaURL(s,e),{...s,lease_started_ms:m}}}var pe=Dh({app:"telephony",create:({app:h})=>new Jh(h)});function Ge({app:h},t){return new Jh(h,t)}export{Ge as createClient,Kt as callTerminationLabel};
