function Ut(t){let h=new URL(t),e=["localhost","127.0.0.1","[::1]"].includes(h.hostname);if(h.username||h.password||h.search||h.hash||h.protocol!=="https:"&&!(h.protocol==="http:"&&e)||!h.pathname.endsWith("/runtime-browser"))throw Error("Invalid managed Telephony browser endpoint");return h}function ih(t){if(!t.startsWith("/")||t.startsWith("//")||t.includes("..")||t.includes("\\"))throw Error("Invalid Telephony request path")}function Ah(t,h){return new Promise((e)=>{let s=()=>{clearTimeout(m),t.removeEventListener("abort",s),e()},m=setTimeout(s,h);if(t.addEventListener("abort",s,{once:!0}),t.aborted)s()})}class Lt{proxy;mode;fetchImpl;direct;info;discovery;credential;issuing;proxyOnly=!1;constructor(t,h="auto",e=globalThis.fetch,s){this.proxy=t;this.mode=h;this.fetchImpl=e;this.direct=s;this.fetchImpl=e.bind(globalThis)}get directBase(){return this.info?.browser_api_base}async prepare(){if(this.mode==="proxy"||this.mode==="auto"&&typeof location>"u")return;if(this.direct){Ut(this.direct.baseURL),this.info={managed:!0,browser_api_base:this.direct.baseURL};return}if(this.discovery)return this.discovery;this.discovery=(async()=>{let t;try{t=await this.proxy.get("/runtime-endpoints")}catch(e){if(this.mode==="direct"||![404,405].includes(e.status??0))throw e;this.proxyOnly=!0;return}if(!t?.managed||!t.browser_api_base){if(this.proxyOnly=!0,this.mode==="direct")throw Error("Direct Telephony deployment unavailable");return}if(Ut(t.browser_api_base),t.browser_media_base)Ut(t.browser_media_base);if(t.browser_signaling_base)Ut(t.browser_signaling_base);let h=new URL(this.proxy.mcpURL(),typeof location>"u"?void 0:location.href);if(!["https:","http:"].includes(h.protocol))throw Error("Invalid Telephony management gateway");this.info=t})();try{await this.discovery}catch(t){throw this.discovery=void 0,t}}async grant(){if(this.credential&&this.credential.expires_at*1000>Date.now()+30000)return this.credential;if(this.issuing)return this.issuing;let t=typeof location>"u"?new URL(this.proxy.mcpURL()).origin:location.origin;this.issuing=(async()=>{let h=this.direct?await this.direct.credentialProvider():await this.proxy.post("/runtime-browser-bootstrap",{origin:t});if(!h||typeof h.token!=="string"||!h.token||h.token.length>4096||!Number.isSafeInteger(h.expires_at)||h.expires_at*1000<=Date.now()||h.expires_at*1000>Date.now()+360000||h.endpoint!==this.directBase)throw Error("Invalid managed browser credential");return this.credential=h})();try{return await this.issuing}finally{this.issuing=void 0}}async request(t,h,e,s){if(this.mode==="proxy"||this.proxyOnly||this.mode==="auto"&&typeof location>"u")return t==="GET"?this.proxy.get(h,s):this.proxy.post(h,e,s);if(await this.prepare(),!this.directBase)return t==="GET"?this.proxy.get(h,s):this.proxy.post(h,e,s);ih(h);let m=await this.grant(),i=new URL(this.directBase+h);if(this.proxy.projectId)i.searchParams.set("project_id",this.proxy.projectId);let u=new Headers(s?.headers);u.set("Authorization","Bearer "+m.token),u.set("Content-Type","application/json");let _=await this.fetchImpl(i,{...s,method:t,headers:u,body:e===void 0?void 0:JSON.stringify(e),credentials:"omit",redirect:"error"});if(!_.ok){if(_.status===403)this.credential=void 0;let a=(await _.text()).slice(0,65536);throw Object.assign(Error(`Telephony HTTP ${_.status}`),{status:_.status,body:a})}return await _.json()}mediaURL(t,h){if(!this.directBase)return t;let e=`/api/apps/telephony/_install/${this.proxy.installId}`,s=h==="webrtc"||h==="auto"?this.info?.browser_signaling_base:this.info?.browser_media_base,m=new URL((s||this.directBase)+t.pathname.slice(e.length));return m.search=t.search,m}subscribe(t,h,e){let s=new AbortController,m,i=()=>{s.abort(),m?.close(),e?.signal?.removeEventListener("abort",i)};if(e?.signal?.addEventListener("abort",i,{once:!0}),e?.signal?.aborted)i();return(async()=>{try{if(await this.prepare(),s.signal.aborted)return;if(!this.directBase){m=this.proxy.subscribe(t,h,e);return}ih(t);let u=500;while(!s.signal.aborted){try{let _=await this.grant(),a=new URL(this.directBase+t);if(this.proxy.projectId)a.searchParams.set("project_id",this.proxy.projectId);let f=await this.fetchImpl(a,{headers:{Authorization:"Bearer "+_.token,Accept:"text/event-stream"},credentials:"omit",redirect:"error",signal:s.signal});if(!f.ok||!f.body){if(f.status===403)this.credential=void 0;throw Object.assign(Error(`Telephony stream HTTP ${f.status}`),{status:f.status})}u=500;let S=f.body.getReader(),M=new TextDecoder,g="";try{while(!s.signal.aborted){let{done:P,value:c}=await S.read();if(P)break;if(g+=M.decode(c,{stream:!0}),g=g.replace(/\r\n/g,`
`),g.length>65536)throw Error("Telephony notification frame too large");let q;while((q=g.indexOf(`

`))>=0){let w=g.slice(0,q);g=g.slice(q+2);let y=w.split(`
`).filter((b)=>b.startsWith("data:")).map((b)=>b.slice(5).trimStart()).join(`
`);if(y)try{h(JSON.parse(y))}catch{}}}}finally{await S.cancel().catch(()=>{})}}catch(_){if(s.signal.aborted)return;e?.onError?.(_),u=Math.min(1e4,u*2)}if(!s.signal.aborted)await Ah(s.signal,u)}}catch(u){if(!s.signal.aborted)e?.onError?.(u)}})(),{close:i}}}function Wt(t){let h=globalThis.crypto?.randomUUID?.()??`${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;return`${t}-${h}`}function Ct(t){return{recovery_id:Wt("recovery"),attempt_id:Wt("attempt"),initiating_action:t}}function Pt(t){return typeof t==="string"&&t.length<=80&&/^(browser|session|recovery|attempt)-[a-f0-9-]{1,64}$/.test(t)?t:void 0}function rt(t){let h=(u,_)=>typeof u==="string"&&u.length<=_&&/^[a-zA-Z0-9_-]*$/.test(u)?u:void 0,e=["WebSocket transport error","WebRTC signaling error","Audio worker failed","WebRTC signaling unavailable"],s=["attach","takeover","automatic_retry","manual_reconnect","audio_device_change","component_recreation"],m=(u,_)=>typeof u==="number"&&Number.isFinite(u)?Math.max(0,Math.min(_,u)):void 0;return{timestamp:Number.isFinite(Date.parse(t.timestamp))?new Date(t.timestamp).toISOString():new Date().toISOString(),action:h(t.action,40)??"diagnostic",outcome:h(t.outcome,40)??"unknown",code:h(t.code,80),detail:t.detail?e.includes(t.detail)?t.detail:"detail_redacted":void 0,recovery_id:Pt(t.recovery_id),attempt_id:Pt(t.attempt_id),shared_attempt_id:Pt(t.shared_attempt_id),session_id:Pt(t.session_id),previous_session_id:Pt(t.previous_session_id),initiating_action:s.includes(t.initiating_action)?t.initiating_action:void 0,status:m(t.status,599),remaining_ms:m(t.remaining_ms,3600000),was_clean:typeof t.was_clean==="boolean"?t.was_clean:void 0,duration_ms:m(t.duration_ms,86400000)}}var it=()=>performance.now();function wt(t){let h=t,e=h?.status,s;try{let i=JSON.parse(h?.body??"{}");if(typeof i.code==="string")s=i.code}catch{}let m=s==="media_lease_expired";return{status:e,code:s,expired:m,denied:!m&&[401,403,404,410].includes(e)}}class xt{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(t,h,e,s,m=it(),i={now:it,setTimeout:(u,_)=>setTimeout(u,_),clearTimeout:(u)=>clearTimeout(u)}){this.seconds=t;this.renew=h;this.ended=e;this.event=s;this.clock=i;this.deadline=m+t*1000-1000,this.watchExpiry(),this.schedule(Math.min(t*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(t,h){let{status:e,code:s}=wt(h);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:t,status:e,code:s,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(t){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,t))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let t=this.clock.now();try{let h=await Promise.race([this.renew(),new Promise((e,s)=>{this.requestTimer=this.clock.setTimeout(()=>s(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(h?.lease_seconds!==void 0){if(!Number.isFinite(h.lease_seconds)||h.lease_seconds<10||h.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=h.lease_seconds}this.deadline=t+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(h){if(this.stopped)return;let e=wt(h);if(e.denied||e.expired){this.finish(e.expired?"expired":"revoked",h);return}this.report("retrying",h),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(t,h){if(this.stopped)return;this.report(t,h),this.stop(),this.ended(t,h)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function zt(t){return t}var lt="0.12.20";var Eh=["rtt_ms","queue_ms","target_ms","buffered_bytes","underruns","dropped_ms","capture_sequence_gaps","playback_sequence_gaps","main_thread_max_pause_ms","worker_max_tick_gap_ms","capture_sent_ms","capture_muted_ms","capture_dropped_ms","playback_ingress_ms","playback_transport_dropped_ms","playback_source_dropped_ms","clock_uncertainty_ms","clock_sample_age_ms","stats_errors","stats_duration_ms","send_bitrate_bps","receive_bitrate_bps","receive_gap_ms","capture_age_ms","transit_ms","delivery_excess_ms","server_queue_ms","receiver_concealed_delta_ms","receiver_silent_concealed_delta_ms","receiver_concealed_window_ms","requested_playback_target_ms","requested_upload_bitrate_bps","receiver_ssrc","receiver_loss_delta","receiver_loss_window_ms","remote_receiver_ssrc","remote_receiver_loss_delta","remote_receiver_loss_window_ms","receiver_bytesReceived","receiver_packetsReceived","receiver_packetsLost","receiver_jitter","receiver_packetsDiscarded","receiver_concealedSamples","receiver_silentConcealedSamples","receiver_concealmentEvents","receiver_insertedSamplesForDeceleration","receiver_removedSamplesForAcceleration","receiver_totalSamplesReceived","receiver_audioLevel","receiver_totalAudioEnergy","receiver_totalSamplesDuration","receiver_nackCount","receiver_fecPacketsReceived","receiver_fecPacketsDiscarded","receiver_jitter_buffer_interval_ms","receiver_jitter_target_interval_ms","receiver_jitter_minimum_interval_ms","receiver_processing_interval_ms","receiver_totalProcessingDelay","receiver_totalSamplesDecoded","telemetry_skipped_samples","telemetry_skipped_auxiliary","telemetry_queue_age_ms","receiver_last_packet_age_ms","sender_bytesSent","sender_packetsSent","sender_headerBytesSent","sender_retransmittedPacketsSent","sender_retransmittedBytesSent","sender_nackCount","sender_targetBitrate","sender_send_delay_interval_ms","remote_receiver_packetsLost","remote_receiver_fractionLost","remote_receiver_jitter","remote_receiver_roundTripTime","remote_receiver_totalRoundTripTime","remote_receiver_roundTripTimeMeasurements","remote_sender_packetsSent","remote_sender_bytesSent","pair_availableOutgoingBitrate","pair_availableIncomingBitrate","pair_currentRoundTripTime","pair_totalRoundTripTime","pair_bytesSent","pair_bytesReceived","pair_requestsSent","pair_requestsReceived","pair_responsesSent","pair_responsesReceived","pair_consentRequestsSent","path_revision","codec_clock_rate","codec_channels"],xh={receiver_processing:["unavailable","per_decoded_sample"],connection:["connected","reconnecting","closed"],context:["running","suspended","interrupted","closed"],muted:["true","false"],device_muted:["true","false"],track:["live","ended"],ice:["new","checking","connected","completed","disconnected","failed","closed"],dtls:["new","connecting","connected","closed","failed"],pair:["frozen","waiting","in-progress","failed","succeeded"],local_candidate:["host","srflx","prflx","relay"],remote_candidate:["host","srflx","prflx","relay"],protocol:["udp","tcp"],relay_protocol:["udp","tcp","tls"],codec:["audio/opus","audio/PCMU","audio/PCMA","pcm16"]};function Jt(t){let h={};for(let e of Eh)if(typeof t[e]==="number"&&Number.isFinite(t[e]))h[e]=Math.max(0,Math.min(t[e],1000000000000));return h}function Zt(t){let h={};for(let[e,s]of Object.entries(xh))if(typeof t[e]==="string"&&s.includes(t[e]))h[e]=t[e];return h}class nt{transport;serial=0;lastEmit=-1/0;previousAt;previous;incident;history=[];pending=[];counters={};constructor(t){this.transport=t}observe(t,h,e=performance.now(),s=new Date().toISOString(),m){if(!Number.isFinite(e)||!Number.isFinite(Date.parse(s)))return;if(this.previousAt!==void 0&&e<this.previousAt)this.lastEmit=-1/0,this.previous=void 0,this.counters={},this.incident=void 0;let i=Jt(t),u=Zt(h),_=!1;for(let f of["underruns","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealmentEvents","path_revision","stats_errors"])if(i[f]!==void 0){if(this.counters[f]!==void 0&&i[f]>this.counters[f])_=!0;this.counters[f]=i[f]}let a={id:`s${++this.serial}`,timestamp:s,transport:this.transport,reason:_?"incident":"periodic",window_ms:Math.min(60000,Math.max(0,typeof m==="number"&&Number.isFinite(m)?m:this.previousAt===void 0?0:e-this.previousAt)),metrics:i,states:u};if(this.previousAt=e,_&&!this.incident)this.incident={sample:a,context:this.previous};if(e-this.lastEmit>=5000){if(this.incident){if(this.incident.context)this.enqueue({...this.incident.context,reason:"incident_context"});this.enqueue(this.incident.sample),this.incident=void 0}else this.enqueue(a);this.lastEmit=e}this.previous=a}enqueue(t){if(this.history.push(t),this.history.length>24)this.history.shift();if(this.pending.push(t),this.pending.length>8)this.pending.shift()}recent(){return this.history.map((t)=>({...t,metrics:{...t.metrics},states:{...t.states}}))}drain(){return this.pending.splice(0,2)}}function ah(t){let h={...t,metrics:{},states:{},part_index:0,part_count:16},e=[],s=h;for(let m of["metrics","states"])for(let[i,u]of Object.entries(t[m])){let _={...s,[m]:{...s[m],[i]:u}};if(JSON.stringify(_).length>768)e.push(s),s={...h,metrics:{},states:{},[m]:{[i]:u}};else s=_}if(e.push(s),e.length>16)return[];return e.map((m,i)=>({...m,part_index:i,part_count:e.length}))}var nh=["receiver_loss_delta","remote_receiver_loss_delta","receiver_concealed_delta_ms","receiver_ssrc","remote_receiver_ssrc","rtt_ms","queue_ms","target_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealedSamples","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_jitter","buffered_bytes","main_thread_max_pause_ms","worker_max_tick_gap_ms","receiver_processing_interval_ms","path_revision","stats_errors"];function Rh(t){let h={};for(let e of nh)if(t.metrics[e]!==void 0){if(h[e]=t.metrics[e],ah({...t,metrics:h}).length>2)delete h[e]}return{...t,metrics:h}}class bt{send;sendReport;now;auxiliary=[];skippedAuxiliary=0;skippedSamples=0;auxiliaryTurn=!1;pending=[];queuedAt=new Map;lastFull=-1/0;timer;report;reportAt=0;static maxAgeMS=12000;constructor(t,h,e=()=>performance.now()){this.send=t;this.sendReport=h;this.now=e}enqueueReport(t){if(this.report!==void 0)this.skippedAuxiliary++;this.report=t,this.reportAt=this.now(),this.start()}enqueueAuxiliary(t){let h=!!t?.diagnostics?.timing?.runtime;if(h){let e=this.auxiliary.length;this.auxiliary=this.auxiliary.filter((s)=>!s.runtime),this.skippedAuxiliary+=e-this.auxiliary.length}if(this.auxiliary.push({report:t,at:this.now(),runtime:h}),this.auxiliary.length>16)this.auxiliary.shift(),this.skippedAuxiliary++;this.start()}start(){if(!this.timer)this.timer=setInterval(()=>this.tick(),250)}discard(t){this.pending=this.pending.filter((h)=>h.id!==t),this.queuedAt.delete(t),this.skippedSamples++}enqueue(t){let h=this.now(),e=[...t].reverse().find((s)=>s.reason==="incident")??t.at(-1);if(this.skippedSamples+=Math.max(0,t.length-(e?1:0)),e){let s=e.reason==="periodic"&&h-this.lastFull>=30000;if(s)this.lastFull=h;let m=s?e:Rh(e);m={...m,metrics:{...m.metrics,telemetry_skipped_samples:this.skippedSamples,telemetry_skipped_auxiliary:this.skippedAuxiliary}};let i=ah(m),u=this.pending[0]?.id;for(let _ of this.queuedAt.keys())if(_!==u)this.discard(_);if(this.queuedAt.has(m.id))this.discard(m.id);if(i.length)this.pending.push(...i),this.queuedAt.set(m.id,h)}if(this.pending.length)this.start()}tick(){let t=this.now();for(let[h,e]of this.queuedAt)if(t-e>bt.maxAgeMS)this.discard(h);if(this.auxiliary=this.auxiliary.filter((h)=>{if(t-h.at>bt.maxAgeMS)return this.skippedAuxiliary++,!1;return!0}),this.report!==void 0&&t-this.reportAt>bt.maxAgeMS)this.report=void 0,this.skippedAuxiliary++;try{if(this.report!==void 0&&this.sendReport){if(this.sendReport(this.report))this.report=void 0}else if(this.auxiliary.length&&this.sendReport&&(!this.pending.length||this.auxiliaryTurn)){if(this.sendReport(this.auxiliary[0].report))this.auxiliary.shift(),this.auxiliaryTurn=!1}else if(this.pending.length){let h=this.pending[0],e={...h.metrics,telemetry_queue_age_ms:Math.max(0,t-(this.queuedAt.get(h.id)??t))};if(this.send({...h,metrics:e})){if(this.pending.shift(),h.part_index===h.part_count-1)this.queuedAt.delete(h.id);this.auxiliaryTurn=!0}}}catch{}if(!this.pending.length&&!this.auxiliary.length&&this.report===void 0)clearInterval(this.timer),this.timer=void 0}stop(){clearInterval(this.timer),this.timer=void 0,this.pending=[],this.queuedAt.clear(),this.auxiliary=[],this.report=void 0}}class Rt{emit;now;timestamp;lastTick;suspendedAt;contextState;environmentCleanup=[];counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(t,h=()=>performance.now(),e=()=>new Date().toISOString()){this.emit=t;this.now=h;this.timestamp=e}observeEnvironment(){this.stopEnvironment();let t=(h,e,s)=>{if(!h)return;try{h.addEventListener(e,s),this.environmentCleanup.push(()=>h.removeEventListener(e,s))}catch{}};try{if(typeof document<"u"){t(document,"visibilitychange",()=>this.report("tab_visibility",document.visibilityState));for(let h of["freeze","resume"])t(document,h,()=>this.report("page_lifecycle",h));this.report("tab_visibility",document.visibilityState)}if(typeof window<"u")for(let h of["pagehide","pageshow"])t(window,h,()=>this.report("page_lifecycle",h));if(typeof navigator<"u")t(navigator.mediaDevices,"devicechange",()=>this.report("microphone","devices_changed"))}catch{}}stopEnvironment(){for(let t of this.environmentCleanup.splice(0))try{t()}catch{}}tick(){let t=this.now();if(this.lastTick!==void 0){let h=Math.max(0,t-this.lastTick-1000);if(h>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,h),this.report("main_thread","scheduling_gap",h)}this.lastTick=t}context(t){if(t===this.contextState)return;this.contextState=t;let h=this.now();if(t==="suspended"||t==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=h,this.counters.audio_context_suspend_count++;this.report("audio_context",t)}else{let e;if(this.suspendedAt!==void 0)e=Math.max(0,h-this.suspendedAt),this.counters.audio_context_suspended_ms+=e,this.suspendedAt=void 0;this.report("audio_context",t,e)}}report(t,h,e){try{this.emit({timestamp:this.timestamp(),action:t,outcome:h,duration_ms:e===void 0?void 0:Math.round(e)})}catch{}}}async function L(t,h,e){let s=performance.now(),m=(i)=>{try{h?.({timestamp:new Date().toISOString(),action:t,outcome:i,duration_ms:Math.round(Math.max(0,performance.now()-s))})}catch{}};m("started");try{let i=await e();return m("completed"),i}catch(i){throw m("failed"),i}}var _h=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function Ht(t){let h=(t??"FR").trim().toUpperCase();return _h[h]??_h.FR}function ph(t,h){let e=[];if(!(t.cadence.reduce((i,u)=>i+u,0)>0)||!(h>0))return e;let m=0;while(m<h)for(let i=0;i<t.cadence.length;i+=2){let u=t.cadence[i]??0,_=t.cadence[i+1]??0;if(m>=h)break;if(u>0)e.push({start:m,end:Math.min(m+u,h)});m+=u+_}return e}function Nt(t,h,e,s=0.12){let m=t.createGain();m.gain.value=0,m.connect(h);let i=s/Math.max(1,e.frequencies.length),u=e.frequencies.map((P)=>{let c=t.createOscillator();return c.type="sine",c.frequency.value=P,c.connect(m),c.start(),c}),_=t.currentTime+0.05,a=0,f=0.01,S=()=>{let P=t.currentTime-_+10;if(P<=a)return;for(let c of ph(e,P)){if(c.end<=a)continue;let q=_+Math.max(c.start,a),w=_+c.end;m.gain.setValueAtTime(0,q),m.gain.linearRampToValueAtTime(i,q+f),m.gain.setValueAtTime(i,Math.max(q+f,w-f)),m.gain.linearRampToValueAtTime(0,w)}a=P};S();let M=setInterval(S,4000),g=!1;return()=>{if(g)return;g=!0,clearInterval(M);try{m.gain.cancelScheduledValues(0)}catch{}m.gain.value=0;for(let P of u){try{P.stop()}catch{}P.disconnect()}m.disconnect()}}var at=24000,pt=60;async function Mh(t,h){try{await t.audioWorklet.addModule(h)}catch(e){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:e})}}function Fh(t){let h=new Int16Array(t.length);for(let e=0;e<t.length;e++){let s=Math.max(-1,Math.min(1,t[e]));h[e]=s<0?s*32768:s*32767}return h.buffer}class St{history=new Float32Array(64);phase=0;process(t,h,e){if(h===e)return t;let s=new Float32Array(64+t.length);s.set(this.history),s.set(t,64);let m=h/e,i=Math.min(1,e/h)*0.9,u=[],_=this.phase;for(;_<t.length;_+=m){let a=_+32,f=0,S=0;for(let M=Math.ceil(a-32);M<=Math.floor(a+32);M++){let g=M-a,c=(Math.abs(g)<0.000000001?i:Math.sin(Math.PI*i*g)/(Math.PI*g))*(0.5+0.5*Math.cos(Math.PI*g/32));if(M>=0&&M<s.length)f+=s[M]*c,S+=c}u.push(S?f/S:0)}return this.phase=_-t.length,this.history=s.slice(-64),new Float32Array(u)}}function Th(t){let h=0;for(let e=0;e<t.length;e++)h+=t[e]*t[e];return Math.sqrt(h/Math.max(1,t.length))}function fh(t,h){let e=new Map(t.map((s)=>[s.id,s]));for(let s of h){let m=e.get(s.id);if(!m||(!m.complete||s.complete)&&(!m.ended_at||!!s.ended_at)&&s.duration_ms>=m.duration_ms)e.set(s.id,{...s})}return[...e.values()].sort((s,m)=>s.started_at.localeCompare(m.started_at)).slice(-100)}function Oh(t,h){return t.map((e)=>e.ended_at?e:{...e,ended_at:e.observed_until,end_reason:h,complete:!1})}var o={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function D(t){if(t.mediaTransport!==void 0&&!["websocket","webrtc","auto"].includes(t.mediaTransport))throw RangeError("Unsupported softphone media transport");if(t.webrtcFec!==void 0&&typeof t.webrtcFec!=="boolean")throw RangeError("webrtcFec must be boolean");let h=t.playbackTargetMs??pt,e=t.playbackMinMs??Math.min(pt,h),s=t.playbackMaxMs??280;for(let m of[h,e,s])if(!Number.isFinite(m)||m<40||m>280)throw RangeError("Playback buffers must be between 40 and 280 ms");if(e>h||h>s)throw RangeError("Playback buffers require min <= target <= max");if(t.playbackAdaptive!==void 0&&typeof t.playbackAdaptive!=="boolean")throw RangeError("playbackAdaptive must be boolean");return{initialTargetMs:h,minTargetMs:e,maxTargetMs:s,hardMaxMs:320,adaptiveReserve:t.playbackAdaptive!==!1}}function _t(t){return{...t.inputDeviceId?{deviceId:{exact:t.inputDeviceId}}:{},echoCancellation:t.echoCancellation,noiseSuppression:t.noiseSuppression,autoGainControl:t.autoGainControl}}function yh(t){let h=t.getSettings();return{deviceLabel:t.label||"Default microphone",sampleRate:typeof h.sampleRate==="number"?h.sampleRate:null,channelCount:typeof h.channelCount==="number"?h.channelCount:null,echoCancellation:typeof h.echoCancellation==="boolean"?h.echoCancellation:null,noiseSuppression:typeof h.noiseSuppression==="boolean"?h.noiseSuppression:null,autoGainControl:typeof h.autoGainControl==="boolean"?h.autoGainControl:null}}function At(t){if(!Number.isFinite(t)||t<=0)return null;return 20*Math.log10(t)}function Gh(t,h,e){let s=new ArrayBuffer(44+h*2),m=new DataView(s),i=(_,a)=>{for(let f=0;f<a.length;f++)m.setUint8(_+f,a.charCodeAt(f))};i(0,"RIFF"),m.setUint32(4,36+h*2,!0),i(8,"WAVE"),i(12,"fmt "),m.setUint32(16,16,!0),m.setUint16(20,1,!0),m.setUint16(22,1,!0),m.setUint32(24,e,!0),m.setUint32(28,e*2,!0),m.setUint16(32,2,!0),m.setUint16(34,16,!0),i(36,"data"),m.setUint32(40,h*2,!0);let u=44;for(let _ of t)for(let a=0;a<_.length;a++)m.setInt16(u,_[a],!0),u+=2;return new Blob([s],{type:"audio/wav"})}class kt{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new St;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(t,h=!0){this.onLevel=t;this.recordAudio=h}async start(t,h){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:_t(h)}),this.ensureOpen();let e=this.stream.getAudioTracks()[0];if(!e)throw Error("No microphone audio track was returned.");this.settings=yh(e);try{this.ctx=new AudioContext({sampleRate:at,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await Mh(this.ctx,t),this.ensureOpen();let s=this.ctx.sampleRate,m=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:h.inputGainDB,highpassFilter:h.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(i)=>{if(this.stopped)return;if(!(i.data instanceof Float32Array)){if(i.data.type==="capture.stats")this.peak=Math.max(this.peak,i.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,i.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,i.data.limiter_reduction_db??0);return}let u=i.data;if(s!==at)u=this.resampler.process(u,s,at);let _=Th(u);if(this.onLevel?.(_),this.recordAudio)this.frames.push(new Int16Array(Fh(u)));if(this.samples+=u.length,_>=0.005){for(let a=0;a<u.length;a++)this.activeSquares+=u[a]*u[a];this.activeSamples+=u.length}},m.connect(this.capture),this.settings}catch(e){throw await this.release(),e}}async stop(){this.stopped=!0;let t=this.settings,h=this.frames,e=this.samples,s=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,m=this.peak;if(await this.release(),!t||e===0)throw Error("No microphone audio was captured.");return{audio:Gh(h,e,at),durationMs:Math.round(e*1000/at),sampleRate:at,activeRmsDbfs:At(s),peakDbfs:At(m),postPeakDbfs:At(this.postPeak||m),limiterReductionDb:this.limiterReductionDB,settings:t}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let t of this.stream?.getTracks()??[])t.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class Dt{callbacks;clientEpoch=crypto.randomUUID();transportTelemetry=new nt("websocket");transportSender=new bt((t)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[t]}})}),!0},(t)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify(t)}),!0});worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new Rt((t)=>this.recordSessionEvent(t));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;sessionReady=!1;mediaPathReady;mediaRevision=0;callAnswered=!1;playbackHeld=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={mediaTransport:"websocket",codec:"pcm16",rttMs:null,queueMs:0,targetMs:pt,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:at,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:o.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t={}){this.callbacks=t}get isMuted(){return this.muted}async start(t,h,e,s=o){let m=D(s);this.diagnostics.targetMs=m.initialTargetMs,this.callbacks.onState?.("connecting"),this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+lt.replaceAll(".","_")}),this.runtimeTelemetry.observeEnvironment(),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{this.stream=await L("microphone_device",(a)=>this.recordSessionEvent(a),()=>navigator.mediaDevices.getUserMedia({audio:_t(s)})),this.ensureOpen();let i=this.stream.getAudioTracks()[0];if(!i)throw Error("No microphone audio track was returned.");if(i.readyState==="ended")throw Error("Microphone disconnected before audio setup.");i.onmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser.")},i.onunmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),this.callbacks.onNotice?.("Microphone input restored.")},i.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let u=yh(i);this.diagnostics={...this.diagnostics,microphoneSampleRate:u.sampleRate??0,microphoneChannelCount:u.channelCount??0,echoCancellation:u.echoCancellation,noiseSuppression:u.noiseSuppression,autoGainControl:u.autoGainControl,micInputGainDb:s.inputGainDB};try{this.ctx=new AudioContext({sampleRate:at,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await L("audio_context",(a)=>this.recordSessionEvent(a),()=>this.ctx.resume());if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),s.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(s.outputDeviceId),this.ensureOpen();await L("audio_worklet",(a)=>this.recordSessionEvent(a),()=>Mh(this.ctx,h)),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let _=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:{...m,telemetryEpoch:this.clientEpoch,telemetryEnabled:!1}}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),_.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=s.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await L("audio_transport",(a)=>this.recordSessionEvent(a),()=>this.openWorker(t,e)),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.setPlaybackObservation(this.ctx?.state==="running"&&this.microphoneTransportReady&&!this.playbackHeld,"audio_context_paused"),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running")this.updateLiveState()},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(i){if(this.closed)this.teardown();if(!this.closed)this.fail(i instanceof Error?i.message:"browser audio setup failed");throw i}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){await this.ctx?.resume(),this.updateLiveState()}setOutputVolume(t){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,t))}sendDTMF(t){if(/^[0-9*#]+$/.test(t))this.sendText(JSON.stringify({type:"dtmf",digits:t}))}startRingback(t){if(this.closed||!this.ctx||this.ringback)return;this.ringback=Nt(this.ctx,this.ctx.destination,Ht(t))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(t)=>{let h=t.data;if(h?.type!=="capture.stats")return;this.micLevel=this.muted?0:h.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:At(h.active_rms??0),micPeakDbfs:At(h.pre_peak??0),micPostPeakDbfs:At(h.post_peak??0),micInputGainDb:h.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:h.limiter_reduction_db??0}},this.playback.port.onmessage=(t)=>{let h=t.data;if(h?.type==="playback.underrun"){this.diagnostics.underrunEvents=fh(this.diagnostics.underrunEvents??[],[h.event]),this.diagnostics.underrunDurationMs=h.underrun_ms,this.diagnostics.underruns=h.underruns;return}if(h?.type!=="stats")return;this.diagnostics.playbackEvents=h.playback_events,this.playbackTiming={reserve_expanded_ms:h.reserve_expanded_ms,reserve_compressed_ms:h.reserve_compressed_ms,reserve_adjustments:h.reserve_adjustments,reserve_match_rejections:h.reserve_match_rejections,played_ms:h.played_ms,max_residence_ms:h.max_residence_ms,drop_totals_ms:h.drop_totals_ms,coaching:{played_ms:h.whisper_played_ms,dropped_ms:h.whisper_dropped_ms,max_queue_ms:h.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,h.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:h.whisper_played_ms??0,coachingDroppedMs:h.whisper_dropped_ms??0,coachingMaxQueueMs:h.whisper_max_queue_ms??0,queueMs:h.queue_ms??0,targetMs:h.target_ms??pt,underruns:h.underruns??0,droppedMs:h.dropped_ms??0,underrunDurationMs:h.underrun_ms??0,underrunEvents:fh(this.diagnostics.underrunEvents??[],h.underrun_events??[]),maxQueueMs:h.max_queue_ms??0,playbackSequenceGaps:h.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,h.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(t,h){return new Promise((e,s)=>{let m=new Worker(h);this.worker=m;let i=new MessageChannel,u=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:i.port1},[i.port1]),this.playback?.port.postMessage({type:"transport",port:u.port1},[u.port1]);let _=!1,a=setTimeout(()=>{if(_)return;_=!0,s(Error("audio connection timed out"))},1e4),f=(S)=>{if(_)return;if(_=!0,clearTimeout(a),S)s(S);else e()};this.cancelWorkerStart=()=>f(Error("audio session closed")),m.onmessage=(S)=>{if(this.closed){f(Error("audio session closed"));return}let M=S.data;if(M?.type==="socket.open")this.reportedPlaybackIDs.clear(),this.reportedCaptureQueueIDs.clear(),this.opened=!0,this.mediaSocketConnected=!0,this.mediaPathReady=void 0,this.mediaRevision=0,this.sessionReady=!1,this.startRTTProbe(),f();else if(M?.type==="runtime.event")this.recordSessionEvent(M.event);else if(M?.type==="socket.message")this.handleControl(M.data);else if(M?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL(M.recovery).then((g)=>{if(!this.closed&&this.worker===m)m.postMessage({type:"socket.credentials",id:M.id,mediaURL:g})},(g)=>{if(this.closed||this.worker!==m)return;let P=wt(g);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:P.denied?"revoked":"retrying",...M.recovery,status:P.status,code:P.code}),m.postMessage({type:"socket.credentials",id:M.id,denied:P.denied})})}else if(M?.type==="socket.error")this.recordSessionEvent({timestamp:M.timestamp,action:"websocket",outcome:"transport_error"});else if(M?.type==="socket.close")if(this.recordSessionEvent({timestamp:M.timestamp,action:"websocket",outcome:"closed",code:String(M.code),detail:M.reason,was_clean:M.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"transport_disconnected"),this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else f(Error("audio connection closed before it was ready"));else if(M?.type==="socket.failed")this.mediaSocketConnected=!1,f(Error(M.detail||"audio connection lost")),this.fail(M.detail||"audio connection lost");else if(M?.type==="transport.drop"&&M.event)this.diagnostics.dropEvents=this.mergeDropEvents(M.event);else if(M?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=M.buffered_bytes??0,this.diagnostics.captureQueueEvents=M.capture_queue_events,this.transportTiming=M.timing??{},M.observation)this.transportTelemetry.observe({...this.transportTiming,...M.observation,rtt_ms:M.timing?.rtt_ms,queue_ms:this.diagnostics.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:M.buffered_bytes,underruns:this.diagnostics.underruns,dropped_ms:this.diagnostics.droppedMs,capture_sequence_gaps:this.diagnostics.captureSequenceGaps,playback_sequence_gaps:this.diagnostics.playbackSequenceGaps,main_thread_max_pause_ms:this.runtimeTelemetry.counters.main_thread_max_pause_ms},{connection:this.mediaSocketConnected?"connected":"reconnecting",context:this.ctx?.state,muted:String(this.muted),device_muted:String(this.stream?.getAudioTracks()[0]?.muted),track:this.stream?.getAudioTracks()[0]?.readyState,codec:"pcm16"},performance.now(),M.observation?.timestamp,M.observation?.window_ms);if(this.diagnostics.transportSamples=this.transportTelemetry.recent(),typeof M.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(M.timing.rtt_ms)}},m.onerror=(S)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:S.message?.slice(0,160)}),f(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},m.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:t,contextRate:this.ctx?.sampleRate??at,muted:this.muted,capturePort:i.port2,playbackPort:u.port2},[i.port2,u.port2])})}mergeDropEvents(t,h){if(t)this.workerDropEvents=[...this.workerDropEvents,t].slice(-100);if(h)this.playbackDropEvents=h.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((e,s)=>e.timestamp.localeCompare(s.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;shutdownIntent="session_cleanup";reportedPlaybackIDs=new Set;reportedCaptureQueueIDs=new Set;handleControl(t){if(this.closed)return;try{let h=JSON.parse(t);if(h.type==="media.ready"||h.type==="media.not_ready"){let e=h;if(!Number.isSafeInteger(e.revision)||e.revision<=this.mediaRevision)return;this.mediaRevision=e.revision,this.mediaPathReady=e.type==="media.ready";try{this.callbacks.onMediaReadiness?.(e)}catch{}if(!this.mediaPathReady&&this.callAnswered)this.callbacks.onState?.("reconnecting","Connecting carrier audio…");this.updateLiveState()}else if(h.type==="ready")this.sessionReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState();else if(h.type==="dtmf.error"||h.type==="dtmf.sent")this.callbacks.onNotice?.(h.type==="dtmf.sent"?"Keypad tone sent":h.detail||"Keypad tone failed");else if(h.type==="pong"){if(this.diagnostics.captureSequenceGaps=h.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof h.nonce==="number"&&h.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-h.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(h.type==="call.ended"||h.type==="session.replaced"){this.setPlaybackObservation(!1,h.type==="call.ended"?"call_ended":"observation_ended"),this.shutdownIntent=h.type==="call.ended"?"call_ended":"session_replaced",this.closed=!0;try{this.callbacks.onState?.("ended",h.type)}finally{this.teardown()}}else if(h.type==="call.status"&&typeof h.call_id==="string"&&typeof h.status==="string"){if(h.hold_state)this.playbackHeld=h.hold_state!=="active",this.setPlaybackObservation(!this.playbackHeld&&this.microphoneTransportReady&&this.ctx?.state==="running",this.playbackHeld?"hold":"observation_resumed");if(h.hold_state&&h.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callAnswered=["answered","in-progress"].includes(h.status),this.updateLiveState(),this.callbacks.onCallStatus?.(h)}else if(h.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:h.detail?.slice(0,160)}),this.fail(h.detail||"The call could not be connected.");else if(h.type==="coach.state")this.callbacks.onNotice?.(h.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(h.type==="audio.health"){let e=h;if(e.state!=="healthy"&&e.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:e.state,reason:e.reason,stages:e.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),e.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&e.state==="healthy"&&e.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(h.type==="media.delivery"){let e=h.state;if(e==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(e==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=e==="stalled"}else if(h.type==="peer.disconnected")this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"carrier_disconnected"),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(h.type==="peer.connected")this.microphoneTransportReady=!0,this.setPlaybackObservation(this.ctx?.state==="running"&&!this.playbackHeld,"carrier_connected"),this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState()}catch{}}updateLiveState(){if(this.sessionReady&&this.callAnswered&&this.microphoneTransportReady&&this.mediaPathReady!==!1&&this.ctx?.state==="running")this.callbacks.onState?.("live")}startRTTProbe(){this.stopRTTProbe();let t=()=>{this.sendDiagnostics()};t(),this.pingTimer=setInterval(t,5000)}sendText(t){this.worker?.postMessage({type:"send.text",data:t})}recordSessionEvent(t){try{t={...this.callbacks.sessionDiagnostics?.(),...t}}catch{}t=rt(t),this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],t].slice(-50);try{this.callbacks.onSessionEvent?.(t)}catch{}try{let h={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,session_events:[t]}};if(new TextEncoder().encode(JSON.stringify(h)).byteLength<=1100)this.transportSender.enqueueAuxiliary(h);else this.transportSender.skippedAuxiliary++}catch{}}sendDiagnostics(){let t=this.diagnostics,h=(m,i)=>{let u=(m??[]).slice(-64),_=u.filter((a)=>!i.has(a.id)).slice(0,8);if(this.mediaSocketConnected){let a=new Set(u.map((f)=>f.id));for(let f of i)if(!a.has(f))i.delete(f);for(let f of _)i.add(f.id)}return _},e=h(t.playbackEvents,this.reportedPlaybackIDs),s=h(t.captureQueueEvents,this.reportedCaptureQueueIDs);if(this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{media_transport:"websocket",codec:"pcm16",client_version:lt,client_epoch:this.clientEpoch,playback_events:e,capture_queue_events:s,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:t.rttMs,playback_queue_ms:t.queueMs,playback_target_ms:t.targetMs,playback_max_queue_ms:t.maxQueueMs,playback_underruns:t.underruns,playback_underrun_ms:t.underrunDurationMs,playback_underrun_events:t.underrunEvents,playback_dropped_ms:t.droppedMs,websocket_buffered_bytes:t.websocketBufferedBytes,audio_context_rate:t.audioContextRate,microphone_sample_rate:t.microphoneSampleRate,microphone_channel_count:t.microphoneChannelCount,echo_cancellation:t.echoCancellation,noise_suppression:t.noiseSuppression,auto_gain_control:t.autoGainControl,mic_active_rms_dbfs:t.micActiveRmsDbfs,mic_peak_dbfs:t.micPeakDbfs,mic_post_peak_dbfs:t.micPostPeakDbfs,mic_input_gain_db:t.micInputGainDb,mic_limiter_reduction_db:t.micLimiterReductionDb,capture_sequence_gaps:t.captureSequenceGaps,playback_sequence_gaps:t.playbackSequenceGaps,drop_events:t.dropEvents}})),this.mediaSocketConnected)this.transportSender.enqueue(this.transportTelemetry.drain());this.callbacks.onDiagnostics?.({...t})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setPlaybackObservation(t,h){try{if(this.playback?.port.postMessage({type:"playback.telemetry.boundary",active:t,reason:h,audio_time_ms:(this.ctx?.currentTime??0)*1000}),!t)this.diagnostics.underrunEvents=Oh(this.diagnostics.underrunEvents??[],h)}catch{}}setMuted(t){let h=this.muted!==t;if(this.muted=t,this.worker?.postMessage({type:"muted",value:t}),this.capture?.port.postMessage({type:"muted",value:t}),t)this.sendText(JSON.stringify({type:"interrupt"}));if(h)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:t?"muted":"unmuted"})}stop(){this.shutdownIntent="user_stop";let t=!this.closed;this.closed=!0;try{if(t)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(t){if(this.shutdownIntent="audio_error",!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:t.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",t)}finally{this.teardown()}}teardown(){this.runtimeTelemetry.stopEnvironment(),this.transportSender.stop(),this.setPlaybackObservation(!1,"observation_ended"),this.stopRingback(),this.microphoneTransportReady=!1,this.sessionReady=this.callAnswered=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let t=this.worker;if(t?.postMessage({type:"close",reason:this.shutdownIntent}),t){let h=setTimeout(()=>t.terminate(),600);t.onmessage=(e)=>{if(e.data?.type==="socket.shutdown.complete")clearTimeout(h),t.terminate()}}this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((h)=>h.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var xe=at*pt/1000;var Vt=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
`;var rh=`// Passive receiver only. Both directions share one rendering clock and delay.
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
`;var Kt=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
`;var Sh=new WeakMap,$t=new Map,Wh=8000;async function Ft(t,h){let e=h==="worklet"?Vt:h==="worker"?Kt:rh;if(typeof location>"u"||new URL(t.mcpURL(),location.href).origin!==location.origin){let f=[URL.createObjectURL(new Blob([e],{type:"text/javascript"}))];return{urls:f,dispose(){f.splice(0).forEach((S)=>URL.revokeObjectURL(S))}}}if(t.name!=="telephony"||!t.projectId||!Number.isSafeInteger(t.installId)||t.installId<=0)throw Error("Telephony audio requires a project and installation");let s=new URL(t.mcpURL(),location.href),m=`${s.origin}:${s.pathname}:${t.projectId}:${t.installId}`,i=Sh.get(t);if(!i||i.scope!==m)i={scope:m,entries:new Map},Sh.set(t,i);let u=i.entries,_=h,a=u.get(_);if(!a){a=(async()=>{let S=$t.get(h);if(!S)S=crypto.subtle.digest("SHA-256",new TextEncoder().encode(e)).then((c)=>Array.from(new Uint8Array(c),(q)=>q.toString(16).padStart(2,"0")).join("")),$t.set(h,S),S.catch(()=>{if($t.get(h)===S)$t.delete(h)});let M=`/ui/frontend/${h}-${await S}.js`,g=new AbortController,P;try{if(await Promise.race([t.get(M,{cache:"force-cache",redirect:"error",signal:g.signal,headers:{Accept:"text/plain"}}),new Promise((w,y)=>{P=setTimeout(()=>{y(Error("Telephony audio asset preparation timed out")),g.abort()},Wh)})])!==e)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let q=new URL(`/api/apps/telephony/_install/${t.installId}${M}`,s);return q.searchParams.set("project_id",t.projectId),q.searchParams.set("install_id",String(t.installId)),q.href}finally{clearTimeout(P)}})(),u.set(_,a);let f=a;f.catch(()=>{if(u.get(_)===f)u.delete(_)})}return{urls:[await a],dispose(){}}}function Tt(t=!1){let h=(t?[Vt]:[Vt,Kt]).map((e)=>URL.createObjectURL(new Blob([e],{type:"text/javascript"})));return{urls:h,dispose(){h.splice(0).forEach((e)=>URL.revokeObjectURL(e))}}}async function gt(t,h=!1){if(typeof location>"u"||new URL(t.mcpURL(),location.href).origin!==location.origin)return Tt(h);let e=await Promise.all(h?[Ft(t,"worklet")]:[Ft(t,"worklet"),Ft(t,"worker")]);return{urls:e.flatMap((s)=>s.urls),dispose(){e.forEach((s)=>s.dispose())}}}class Ot{options;target;bitrate=32000;stable;last=-1/0;initial;maximum;constructor(t={}){this.options=t;this.initial=t.playbackTargetMs??60,this.target=this.initial,this.maximum=Math.max(this.initial,t.playbackMaxMs??160)}observe(t,h=performance.now()){if(h<this.last)this.last=-1/0,this.stable=void 0;if(h-this.last<2000)return;this.last=h;let e=t.receiver_jitter===void 0?void 0:t.receiver_jitter*1000,s=t.receiver_concealed_delta_ms??0;if(this.options.playbackAdaptive!==!1){let u=Math.min(this.maximum,Math.max(this.initial,this.initial+Math.ceil(Math.max(0,(e??0)-10)*2/20)*20,s>0?this.target+20:this.initial));if(u>this.target)this.target=Math.min(u,this.target+20),this.stable=void 0;else if(e!==void 0&&s===0&&e<10){if(this.stable??=h,h-this.stable>=1e4)this.target=Math.max(this.initial,this.target-10),this.stable=h}else this.stable=void 0}let{pair_availableOutgoingBitrate:m,remote_receiver_fractionLost:i}=t;if(m!==void 0&&m>0&&m<64000)this.bitrate=Math.max(16000,Math.min(32000,Math.floor((m-32000)*0.8/1000)*1000));else if(i!==void 0&&i>=0.03)this.bitrate=Math.max(16000,this.bitrate-4000);else if(i!==void 0&&i<0.01)this.bitrate=Math.min(32000,this.bitrate+2000)}}class jt{tokens=1100;at;rate=640;observeAvailableBitrate(t){this.rate=t!==void 0&&t>=96000?2000:640}consume(t,h=performance.now()){if(!Number.isFinite(t)||!Number.isFinite(h)||t<0||t>1100)return!1;if(this.at!==void 0)this.tokens=Math.min(1100,this.tokens+Math.max(0,h-this.at)*this.rate/1000);if(this.at=h,this.tokens<t)return!1;return this.tokens-=t,!0}}function dt(t){if(t===void 0)return"websocket";if(!["websocket","webrtc","auto"].includes(t))throw RangeError("Unsupported softphone media transport");return t}function It(t,h){if(h!==void 0&&typeof h!=="boolean")throw RangeError("webrtcFec must be boolean");let e=new URL(t);if(e.searchParams.set("transport","webrtc"),e.searchParams.delete("webrtc_fec"),h!==void 0)e.searchParams.set("webrtc_fec",String(h));return e.toString()}var H=(t)=>{try{t?.()}catch{}},ft=(t,h=1000000000000)=>typeof t==="number"&&Number.isFinite(t)?Math.max(0,Math.min(t,h)):0;function Hh(t){return t===!0?"relay_configured":t===!1?"no_relay":"unreported"}function Gt(t){let h=wt(t);return h.denied||h.expired||["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes(t?.name)}function vt(t,h){let e=0,s=0,m=0,i=0,u=0,_=0,a=0,f=0,S=0,M=null,g,P,c,q={},w={},y={},b=(r,C)=>typeof r?.[C]==="number"&&Number.isFinite(r[C])?Math.max(0,r[C]):void 0,O=(r,C,l)=>{for(let E of l){let n=b(r,E);if(n!==void 0)w[C+E]=n}},U=(r,C,l,E,n)=>{let Q=b(r,l),x=b(r,E),F=h?.counters?.[C];if(Q!==void 0&&x!==void 0){if(q[C]??={},q[C][l]=Q,q[C][E]=x,b(r,"ssrc")!==void 0)q[C].ssrc=r.ssrc;if(F&&F.ssrc===b(r,"ssrc")&&Q>=F[l]&&x>F[E])w[n]=(Q-F[l])/(x-F[E])*1000}},N=(r,C,l)=>{let E=b(r,"packetsLost"),n=b(r,"ssrc"),Q=b(r,"timestamp"),x=h?.counters?.[C];if(n!==void 0)w[l+"ssrc"]=n;if(E===void 0||Q===void 0)return;if(q[C]??={},q[C].packetsLost=E,q[C].at=Q,n!==void 0)q[C].ssrc=n;if(x&&x.ssrc===n&&E>=x.packetsLost&&Q>x.at)w[l+"loss_delta"]=(w[l+"loss_delta"]??0)+(E-x.packetsLost),w[l+"loss_window_ms"]=Q-x.at},X=(r,C,l)=>{let E=b(r,"concealedSamples"),n=b(r,"silentConcealedSamples"),Q=b(r,"timestamp"),x=b(r,"ssrc"),F=h?.counters?.[C];if(E===void 0||Q===void 0)return;if(q[C]??={},Object.assign(q[C],{concealedSamples:E,at:Q}),x!==void 0)q[C].ssrc=x;if(n!==void 0)q[C].silentConcealedSamples=n;if(F&&F.ssrc===x&&E>=F.concealedSamples&&Q>F.at){if(w.receiver_concealed_delta_ms=(w.receiver_concealed_delta_ms??0)+(E-F.concealedSamples)*1000/l,w.receiver_concealed_window_ms=Q-F.at,n!==void 0&&n>=F.silentConcealedSamples)w.receiver_silent_concealed_delta_ms=(w.receiver_silent_concealed_delta_ms??0)+(n-F.silentConcealedSamples)*1000/l}},K=!1,yt=0;t.forEach((r,C)=>{if(S=Math.max(S,ft(r.timestamp,1000000000000000)),(r.kind??r.mediaType??t.get(r.localId)?.kind)==="audio"&&yt++<32){if(r.type==="outbound-rtp")e+=ft(r.bytesSent),O(r,"sender_",["bytesSent","packetsSent","headerBytesSent","retransmittedPacketsSent","retransmittedBytesSent","nackCount","targetBitrate"]),U(r,C,"totalPacketSendDelay","packetsSent","sender_send_delay_interval_ms");if(r.type==="inbound-rtp"){N(r,C,"receiver_"),s+=ft(r.bytesReceived),m+=ft(r.packetsLost),i=Math.max(i,ft(r.jitter)*1000);let E=t.get(r.codecId),n=b(E,"clockRate")??48000;if(X(r,C,n),K||=b(r,"concealedSamples")!==void 0,u+=ft(r.concealedSamples)*1000/n,_+=ft(r.packetsDiscarded),a+=ft(r.jitterBufferDelay),f+=ft(r.jitterBufferEmittedCount),O(r,"receiver_",["bytesReceived","packetsReceived","packetsLost","jitter","packetsDiscarded","concealedSamples","silentConcealedSamples","concealmentEvents","insertedSamplesForDeceleration","removedSamplesForAcceleration","totalSamplesReceived","audioLevel","totalAudioEnergy","totalSamplesDuration","nackCount","fecPacketsReceived","fecPacketsDiscarded"]),U(r,C,"jitterBufferDelay","jitterBufferEmittedCount","receiver_jitter_buffer_interval_ms"),U(r,C,"jitterBufferTargetDelay","jitterBufferEmittedCount","receiver_jitter_target_interval_ms"),U(r,C,"jitterBufferMinimumDelay","jitterBufferEmittedCount","receiver_jitter_minimum_interval_ms"),O(r,"receiver_",["totalProcessingDelay","totalSamplesDecoded"]),y.receiver_processing=b(r,"totalSamplesDecoded")===void 0?"unavailable":"per_decoded_sample",U(r,C,"totalProcessingDelay","totalSamplesDecoded","receiver_processing_interval_ms"),b(r,"lastPacketReceivedTimestamp")!==void 0)w.receiver_last_packet_age_ms=Math.max(0,r.timestamp-r.lastPacketReceivedTimestamp);if(E){if(y.codec=E.mimeType,w.codec_clock_rate=n,b(E,"channels")!==void 0)w.codec_channels=E.channels}}if(r.type==="remote-inbound-rtp")O(r,"remote_receiver_",["packetsLost","fractionLost","jitter","roundTripTime","totalRoundTripTime","roundTripTimeMeasurements"]),N(r,C,"remote_receiver_");if(r.type==="remote-outbound-rtp")O(r,"remote_sender_",["packetsSent","bytesSent"]);let l=r.type==="outbound-rtp"?"bytesSent":r.type==="inbound-rtp"?"bytesReceived":void 0;if(l&&b(r,l)!==void 0)q[C]??={},q[C][l]=r[l],q[C].at=r.timestamp}if(r.type==="transport"&&r.selectedCandidatePairId){g=r.selectedCandidatePairId,y.dtls=r.dtlsState;let l=t.get(g);if(l){M=b(l,"currentRoundTripTime")!==void 0?l.currentRoundTripTime*1000:null,O(l,"pair_",["availableOutgoingBitrate","availableIncomingBitrate","currentRoundTripTime","totalRoundTripTime","bytesSent","bytesReceived","requestsSent","requestsReceived","responsesSent","responsesReceived","consentRequestsSent"]),y.pair=l.state;let E=t.get(l.localCandidateId),n=t.get(l.remoteCandidateId);if(E)P=E.protocol,c=E.candidateType,y.protocol=P,y.local_candidate=c,y.relay_protocol=E.relayProtocol;if(n)y.remote_candidate=n.candidateType}}});let tt=(r,C)=>{if(h?.counters){let E=0;for(let[n,Q]of Object.entries(q)){let x=h.counters[n];if(x&&Q[r]>=x[r]&&Q.at>x.at)E+=(Q[r]-x[r])*8000/(Q.at-x.at)}return E}let l=h&&S>h.at?(S-h.at)/1000:0;return l?Math.max(0,C-(r==="bytesSent"?h.sent:h.received))*8/l:0},ht=(h?.pathRevision??0)+(g&&g!==h?.pairID?1:0);w.path_revision=ht;let j=tt("bytesSent",e),qt=tt("bytesReceived",s);return{previous:{at:S,sent:e,received:s,counters:q,pairID:g,pathRevision:ht},rtt:M,queueMs:f>0?a/f*1000:0,metrics:Jt({...w,send_bitrate_bps:j,receive_bitrate_bps:qt}),states:Zt(y),webrtc:{protocol:P,candidateType:c,sendBitrateBps:j,receiveBitrateBps:qt,packetsLost:m,jitterMs:i,concealedMs:K?u:void 0,packetsDiscarded:_,jitterBufferMs:f>0?a/f*1000:0}}}function Nh(t,h=new Date().toISOString()){let e=[];for(let[s,m]of[["receiver_","carrier_to_operator"],["remote_receiver_","operator_to_carrier"]]){let i=t[s+"loss_delta"];if(i>0)e.push({timestamp:h,direction:m,reason:"webrtc_packet_loss",duration_ms:0,packet_count:i,ssrc:t[s+"ssrc"],window_ms:t[s+"loss_window_ms"]})}if(t.receiver_concealed_delta_ms>0)e.push({timestamp:h,direction:"carrier_to_operator",reason:"webrtc_concealment",duration_ms:Math.round(t.receiver_concealed_delta_ms),ssrc:t.receiver_ssrc,window_ms:t.receiver_concealed_window_ms});return e}class ot{callbacks;stopped=!1;generation=0;socket;pc;stream;context;capture;whisper;destination;speaker;remoteAudio;speakerMeter;carrierStalled=!1;deliveryDegraded=!1;ready=!1;answered=!1;mediaPathReady;mediaRevision=0;reportedLive=!1;peer=!1;muted=!1;whisperEpoch=null;whisperBase=null;whisperResampler=new St;timer;retry;cancelSetup;recovering=!1;recoveryGeneration=0;recoveryContext;recoveryExpiry;ringback;options;workletURL;events=[];previous;sampling=new WeakSet;transportTelemetry=new nt("webrtc");transportSender=new bt((t)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[t]}})},(t)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry(t)});controlBudget=new jt;quality;playbackReceiver;uploadSender;applyingBitrate=!1;appliedBitrate;statsErrors=0;lastStatsSend=-1/0;clientEpoch=crypto.randomUUID();relayConfigured;nativeCounts;completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};runtime=new Rt((t)=>this.recordSessionEvent(t));diagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:null,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(t){this.callbacks=t}async start(t,h,e){if(this.options=h,this.workletURL=e,!e)throw Error("WebRTC requires the shared Telephony audio processor");if(typeof RTCPeerConnection!=="function")throw Error("WebRTC audio is unavailable in this browser");D(h),await this.connect(t)}current(t){return!this.stopped&&this.generation===t}guard(t){if(!this.current(t))throw Error("Audio session cancelled")}async connect(t){this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+lt.replaceAll(".","_")}),this.runtime.observeEnvironment();let h=++this.generation,e=this.options;for(let M of this.events.slice(-8))this.enqueueRTCEvents({session_events:[M]});for(let M of this.diagnostics.dropEvents.slice(-8))this.enqueueRTCEvents({drop_events:[M]});this.ready=this.peer=this.answered=this.reportedLive=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.previous=void 0,this.quality=new Ot(e),this.appliedBitrate=void 0;let s=await L("microphone_device",(M)=>this.recordSessionEvent(M),()=>navigator.mediaDevices.getUserMedia({audio:_t(e)}));if(!this.current(h))throw s.getTracks().forEach((M)=>M.stop()),Error("Audio session cancelled");this.stream=s;let m=s.getAudioTracks()[0];if(!m)throw Error("No microphone audio track");let i=m.getSettings();this.diagnostics={...this.diagnostics,microphoneSampleRate:i.sampleRate??0,microphoneChannelCount:i.channelCount??1,echoCancellation:typeof i.echoCancellation==="boolean"?i.echoCancellation:null,noiseSuppression:i.noiseSuppression??null,autoGainControl:i.autoGainControl??null,micInputGainDb:e.inputGainDB},m.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),m.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),m.onended=()=>{if(this.current(h))this.disconnected(h,"microphone_ended")},this.context=new AudioContext({latencyHint:"interactive"});let u=this.context;if(u.onstatechange=()=>{this.runtime.context(u.state),this.updateLiveState()},this.runtime.context(u.state),await L("audio_context",(M)=>this.recordSessionEvent(M),()=>u.resume()),this.guard(h),e.outputDeviceId&&"setSinkId"in u)await u.setSinkId(e.outputDeviceId);await L("audio_worklet",(M)=>this.recordSessionEvent(M),()=>u.audioWorklet.addModule(this.workletURL)),this.guard(h),this.diagnostics.audioContextRate=u.sampleRate,this.capture=new AudioWorkletNode(u,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:e.inputGainDB,highpassFilter:e.highpassFilter}}),this.destination=u.createMediaStreamDestination(),this.destination.channelCount=1,u.createMediaStreamSource(s).connect(this.capture).connect(this.destination),this.capture.port.onmessage=(M)=>{if(M.data?.type==="capture.stats"){let g=(c)=>c>0?20*Math.log10(c):null;this.diagnostics.micActiveRmsDbfs=g(M.data.active_rms),this.diagnostics.micPeakDbfs=g(M.data.pre_peak),this.diagnostics.micPostPeakDbfs=g(M.data.post_peak),this.diagnostics.micLimiterReductionDb=M.data.limiter_reduction_db??0;let P=0;if(this.speakerMeter){let c=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(c),P=Math.sqrt(c.reduce((q,w)=>q+w*w,0)/c.length)}H(()=>this.callbacks.onLevels?.(ft(M.data.active_rms,1),P))}},this.speaker=u.createGain(),this.speaker.gain.value=e.outputVolume??1,this.speaker.connect(u.destination),this.speakerMeter=u.createAnalyser(),this.speakerMeter.fftSize=256;let _=u.createGain();_.gain.value=0,this.speaker.connect(this.speakerMeter).connect(_).connect(u.destination),this.whisper=new AudioWorkletNode(u,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:D(e)}),this.whisper.connect(this.speaker),this.gate();let a=new WebSocket(It(t,e.webrtcFec));a.binaryType="arraybuffer",this.socket=a;let f=!1,S=!1;await L("audio_transport",(M)=>this.recordSessionEvent(M),()=>new Promise((M,g)=>{let P=(w)=>{if(S)return;S=!0,clearTimeout(c),this.cancelSetup=void 0,w?g(w):M()},c=setTimeout(()=>P(Error("WebRTC audio connection timed out")),18000);this.cancelSetup=()=>P(Error("Audio session cancelled"));let q=()=>{if(this.current(h)&&this.ready&&f)this.gate(),P(),this.updateLiveState()};a.onmessage=(w)=>{if(!this.current(h))return;if(w.data instanceof ArrayBuffer){this.playWhisper(w.data);return}if(typeof w.data!=="string")return;let y;try{y=JSON.parse(w.data)}catch{return}if(y.type==="webrtc.config"){if(this.relayConfigured=typeof y.relay_configured==="boolean"?y.relay_configured:void 0,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"ice_configuration",outcome:Hh(y.relay_configured)}),this.pc){P(Error("Repeated WebRTC configuration"));return}(async()=>{let b=new RTCPeerConnection({iceServers:y.ice_servers??[]});this.pc=b,this.destination.stream.getAudioTracks().forEach((U)=>{let N=b.addTrack(U,this.destination.stream);this.uploadSender=N,this.applyUploadBitrate(N,32000,h)}),b.ontrack=(U)=>{if(!this.current(h))return;this.playbackReceiver=U.receiver,this.applyPlaybackTarget();let N=new MediaStream([U.track]),X=new Audio;X.muted=!0,X.srcObject=N,this.remoteAudio=X,X.play().catch(()=>H(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction."))),u.createMediaStreamSource(N).connect(this.speaker)},b.onconnectionstatechange=()=>{if(!this.current(h))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:b.connectionState}),b.connectionState==="connected")f=!0,q();if(b.connectionState==="failed"||b.connectionState==="closed")if(!S)P(Error("WebRTC connection failed"));else this.disconnected(h,"webrtc_"+b.connectionState)};let O=await b.createOffer();this.guard(h),await b.setLocalDescription(O),await new Promise((U,N)=>{if(b.iceGatheringState==="complete"){U();return}let X=setTimeout(()=>{b.onicegatheringstatechange=null,N(Error("ICE gathering timed out"))},7000);b.onicegatheringstatechange=()=>{if(b.iceGatheringState==="complete")clearTimeout(X),b.onicegatheringstatechange=null,U()}}),this.guard(h),a.send(JSON.stringify({type:"webrtc.offer",sdp:b.localDescription.sdp}))})().catch((b)=>P(b instanceof Error?b:Error("WebRTC setup failed")));return}if(y.type==="webrtc.answer"){this.pc?.setRemoteDescription({type:"answer",sdp:y.sdp}).catch(()=>P(Error("Invalid WebRTC answer")));return}if(y.type==="media.ready"||y.type==="media.not_ready"){if(!Number.isSafeInteger(y.revision)||y.revision<=this.mediaRevision)return;this.mediaRevision=y.revision,this.mediaPathReady=y.type==="media.ready",H(()=>this.callbacks.onMediaReadiness?.(y)),this.updateLiveState()}if(y.type==="ready")this.ready=!0,this.send({type:"media.capabilities",version:2,versions:[3],whisper:!0}),q();if(y.type==="peer.connected")this.peer=!0,this.gate(),q();if(y.type==="peer.disconnected")this.peer=!1,this.gate(),this.updateLiveState(),H(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));if(y.type==="call.status")this.answered=["answered","in-progress"].includes(y.status),q(),H(()=>this.callbacks.onCallStatus?.(y));if(y.type==="call.error")if(!S)P(Error(y.detail??"Carrier activation failed"));else this.stop(),H(()=>this.callbacks.onState?.("error",y.detail));if(y.type==="call.ended"||y.type==="session.replaced")if(!S)P(Error(y.type));else this.stop(),H(()=>this.callbacks.onState?.("ended",y.type));if(y.type==="audio.health"){if(this.diagnostics.audioHealth={state:y.state,reason:y.reason,stages:y.stages},H(()=>this.callbacks.onAudioHealth?.(y)),y.state==="audio_degraded"&&!this.deliveryDegraded)this.deliveryDegraded=!0,H(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));else if(this.deliveryDegraded&&y.state==="healthy"&&y.stages?.telephony_to_browser?.state==="healthy"&&u.state==="running")this.deliveryDegraded=!1,H(()=>this.callbacks.onNotice?.("Speech delivery restored."))}if(y.type==="media.delivery"){if(y.state==="stalled")H(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));else if(y.state==="flowing"&&this.carrierStalled)H(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));this.carrierStalled=y.state==="stalled"}if(y.type==="call.notice")H(()=>this.callbacks.onNotice?.(y.detail??y.reason??"Audio delivery notice"));if(y.type==="coach.state")this.whisperEpoch=y.talking&&Number.isInteger(y.epoch)?y.epoch:null,this.whisperBase=null,this.whisper?.port.postMessage({type:"whisper.clear"})},a.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"}),a.onclose=(w)=>{if(!this.current(h))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(w.code),was_clean:w.wasClean}),!S){P(Object.assign(Error("WebRTC signaling unavailable"),w.code===1008?{status:403}:{}));return}if(w.code===1008){this.stop(),H(()=>this.callbacks.onState?.("error","Media authorization ended"));return}this.disconnected(h,"signaling_closed")}})),this.guard(h),this.timer=setInterval(()=>{this.runtime.tick(),this.statistics(h)},1000),await this.statistics(h)}send(t){if(this.socket?.readyState===WebSocket.OPEN&&this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(t))}catch{}}updateLiveState(){let t=this.ready&&this.peer&&this.answered&&this.mediaPathReady!==!1&&this.pc?.connectionState==="connected"&&this.context?.state==="running";if(t===this.reportedLive)return;this.reportedLive=t,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"media_path",outcome:t?"ready":"waiting"}),H(()=>this.callbacks.onState?.(t?"live":"reconnecting",t?void 0:"Connecting carrier audio…"))}gate(){let t=!this.stopped&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!t}),this.destination?.stream.getAudioTracks().forEach((h)=>h.enabled=t)}async statistics(t){if(!this.current(t)||!this.pc||this.sampling.has(this.pc))return;let h=this.pc,e=performance.now();this.sampling.add(h);try{let s=vt(await h.getStats(),this.previous);if(!this.current(t)||this.pc!==h)return;if(this.previous=s.previous,this.nativeCounts={packetsLost:s.webrtc.packetsLost,packetsDiscarded:s.webrtc.packetsDiscarded,concealedMs:s.webrtc.concealedMs},s.webrtc.packetsLost+=this.completedCounts.packetsLost,s.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded,s.webrtc.concealedMs!==void 0)s.webrtc.concealedMs+=this.completedCounts.concealedMs;if(this.quality?.observe(s.metrics),this.applyPlaybackTarget(),this.uploadSender&&this.quality)this.applyUploadBitrate(this.uploadSender,this.quality.bitrate,t);if(s.metrics.requested_playback_target_ms=this.quality?.target??60,this.appliedBitrate!==void 0)s.metrics.requested_upload_bitrate_bps=this.appliedBitrate;this.controlBudget.observeAvailableBitrate(s.metrics.pair_availableOutgoingBitrate);let m=Nh(s.metrics);for(let u of m)this.enqueueRTCEvents({drop_events:[u]});this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,...m].slice(-100),this.diagnostics={...this.diagnostics,rttMs:s.rtt,queueMs:s.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,s.queueMs),targetMs:this.quality?.target??this.options?.playbackTargetMs??60,webrtc:s.webrtc,playbackSequenceGaps:s.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};let i=this.stream?.getAudioTracks()[0];this.transportTelemetry.observe({...s.metrics,rtt_ms:s.rtt,queue_ms:s.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:this.socket?.bufferedAmount??0,stats_errors:this.statsErrors,stats_duration_ms:performance.now()-e,main_thread_max_pause_ms:this.runtime.counters.main_thread_max_pause_ms},{...s.states,ice:h.iceConnectionState,connection:"connected",context:this.context?.state,muted:String(this.muted),device_muted:String(i?.muted),track:i?.readyState}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),H(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}catch{if(this.current(t)&&this.pc===h)this.statsErrors++,this.transportTelemetry.observe({stats_errors:this.statsErrors,stats_duration_ms:performance.now()-e},{ice:h.iceConnectionState,context:this.context?.state,muted:String(this.muted)}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),H(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}finally{this.sampling.delete(h)}}applyPlaybackTarget(){let t=this.playbackReceiver;if(!t||!this.quality)return;if("jitterBufferTarget"in t)try{t.jitterBufferTarget=this.quality.target}catch{}}async applyUploadBitrate(t,h,e){if(this.applyingBitrate||this.appliedBitrate===h)return;this.applyingBitrate=!0;try{let s=t.getParameters();if(s.encodings?.length){if(s.encodings[0].maxBitrate=h,await t.setParameters(s),this.current(e)&&this.uploadSender===t)this.appliedBitrate=h}}catch{}finally{this.applyingBitrate=!1}}sendTelemetry(t){if(!this.ready||this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let h=JSON.stringify(t),e=new TextEncoder().encode(h).byteLength+58;if(!this.controlBudget.consume(e))return!1;try{return this.socket.send(h),!0}catch{return!1}}enqueueRTCEvents(t){try{let h={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,...t}};if(new TextEncoder().encode(JSON.stringify(h)).byteLength+58>1100){this.transportSender.skippedAuxiliary++;return}this.transportSender.enqueueAuxiliary(h)}catch{this.transportSender.skippedAuxiliary++}}sendStatistics(){let t=this.stream?.getAudioTracks()[0];if(performance.now()-this.lastStatsSend>=5000)this.lastStatsSend=performance.now(),this.transportSender.enqueueReport({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,client_version:lt,relay_configured:this.relayConfigured,media_transport:"webrtc",codec:"opus",webrtc:Object.fromEntries(Object.entries(this.diagnostics.webrtc??{}).map(([h,e])=>[h,typeof e==="number"?Math.round(e*10)/10:e])),connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:t?.readyState,microphone_device_muted:t?.muted,rtt_ms:this.diagnostics.rttMs===null?null:Math.round(this.diagnostics.rttMs),playback_queue_ms:Math.round(this.diagnostics.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:this.diagnostics.webrtc?.packetsLost}}),this.transportSender.enqueue(this.transportTelemetry.drain()),this.enqueueRTCEvents({timing:{runtime:this.runtime.counters}})}playWhisper(t){if(t.byteLength<17||t.byteLength>176||this.whisperEpoch===null||!this.context)return;let h=new DataView(t);if(h.getUint32(0,!0)!==827805761||h.getUint32(4,!0)!==this.whisperEpoch)return;let e=h.getFloat64(8,!0);if(!Number.isFinite(e))return;let s=performance.timeOrigin+performance.now()-e;this.whisperBase=this.whisperBase===null?s:Math.min(this.whisperBase,s);let m=Math.max(0,s-this.whisperBase);if(m>200)return;let i=new Float32Array(t.byteLength-16);for(let _=0;_<i.length;_++){let a=~h.getUint8(_+16)&255,f=((a&15)<<3)+132<<(a>>4&7);i[_]=(a&128?132-f:f-132)/32768}let u=this.context.sampleRate===8000?i:this.whisperResampler.process(i,8000,this.context.sampleRate);this.whisper?.port.postMessage({type:"whisper",frame:u,received_audio_ms:this.context.currentTime*1000-m},[u.buffer])}disconnected(t,h){if(!this.current(t)||this.recovering)return;this.recovering=!0;let e=Ct("automatic_retry");this.recoveryContext=e,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"started",code:h,...e}),this.cleanup(),H(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));let s=++this.recoveryGeneration,m=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===s;this.recoveryExpiry=setTimeout(()=>{if(!m())return;this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"timed_out",...this.recoveryContext}),++this.recoveryGeneration,this.recovering=!1,clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup(),H(()=>this.callbacks.onState?.("error","WebRTC audio recovery timed out; reconnect audio to the existing call."))},30000);let i=async()=>{if(!m())return;let u={...e,attempt_id:Wt("attempt")};this.recoveryContext=u,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"attempt_started",code:h,...u});try{if(!this.callbacks.refreshMediaURL)throw Error("Fresh media authorization is required");let _=await this.callbacks.refreshMediaURL(u);if(!m())return;if(await this.connect(_),!m())return;if(this.pc?.connectionState!=="connected")throw Error("WebRTC disconnected during recovery");clearTimeout(this.recoveryExpiry),this.recovering=!1,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected",...u})}catch(_){if(!m())return;this.cleanup();let a=wt(_);if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:Gt(_)?"revoked":"attempt_failed",...u,status:a.status,code:a.code}),Gt(_)){clearTimeout(this.recoveryExpiry),this.recovering=!1,H(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return}this.retry=setTimeout(()=>void i(),1000)}};i()}recordSessionEvent(t){try{t={...this.callbacks.sessionDiagnostics?.(),...t}}catch{}t=rt(t),this.events=[...this.events,t].slice(-100),this.enqueueRTCEvents({session_events:[t]}),H(()=>this.callbacks.onSessionEvent?.(t))}setMuted(t){if(this.muted!==t)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:t?"muted":"unmuted"});if(this.muted=t,this.gate(),t)this.send({type:"interrupt"})}sendDTMF(t){if(!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");this.send({type:"dtmf",digits:t})}setOutputVolume(t){if(!Number.isFinite(t)||t<0||t>1)throw RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=t;if(this.options)this.options.outputVolume=t}startRingback(t){if(this.stopRingback(),this.context&&this.speaker)this.ringback=Nt(this.context,this.speaker,Ht(t))}stopRingback(){this.ringback?.(),this.ringback=void 0}cleanup(){if(this.runtime.stopEnvironment(),this.transportSender.stop(),this.nativeCounts){for(let s of["packetsLost","packetsDiscarded","concealedMs"])this.completedCounts[s]+=this.nativeCounts[s]??0;this.nativeCounts=void 0}++this.generation,this.ready=this.peer=!1,this.stopRingback(),clearInterval(this.timer),this.timer=void 0;let t=this.socket;if(this.socket=void 0,t){if(t.readyState===WebSocket.OPEN)try{t.send(JSON.stringify({type:"media.shutdown",reason:this.stopped?"session_cleanup":"audio_error"}))}catch{}t.onclose=t.onmessage=t.onerror=null,t.close(1000,"client_shutdown")}this.playbackReceiver=void 0,this.uploadSender=void 0;let h=this.pc;if(this.pc=void 0,h)h.onconnectionstatechange=h.ontrack=null,h.close();if(this.remoteAudio)this.remoteAudio.pause(),this.remoteAudio.srcObject=null,this.remoteAudio=void 0;this.stream?.getTracks().forEach((s)=>{s.onended=null,s.stop()}),this.stream=void 0,this.destination?.stream.getTracks().forEach((s)=>s.stop()),this.destination=void 0,this.capture?.disconnect(),this.capture=void 0,this.whisper?.disconnect(),this.whisper=void 0,this.whisperEpoch=null,this.whisperBase=null,this.whisperResampler=new St;let e=this.context;if(this.context=void 0,e)e.onstatechange=null,e.close().catch(()=>{});this.speaker=void 0,this.speakerMeter=void 0}stop(){if(this.stopped)return;if(this.recovering)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"cancelled",...this.recoveryContext});this.stopped=!0,++this.recoveryGeneration,clearTimeout(this.recoveryExpiry),clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup()}}function gh(t,h,e){let s,m=!1,i=!1,u=[];return{async start(_,a){let f=dt(a.mediaTransport),S=async(M)=>{if(m)throw Error("Audio session cancelled");let g=M();s=g,g.setMuted(i);try{for(let P of u.splice(0))try{g.recordSessionEvent?.(P)}catch{}if(m)throw Error("Audio session cancelled");if(await g.start(_,a),m)throw Error("Audio session cancelled")}catch(P){if(g.stop(),s===g)s=void 0;throw P}};if(f==="websocket"){await S(h);return}try{await S(e)}catch(M){if(f!=="auto"||m||Gt(M))throw M;H(()=>t.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"})),await S(h)}},stop(){m=!0,u.splice(0),s?.stop(),s=void 0},setMuted(_){i=_,s?.setMuted(_)},setOutputVolume(_){s?.setOutputVolume(_)},sendDTMF(_){s?.sendDTMF(_)},recordSessionEvent(_){if(s)s.recordSessionEvent?.(_);else if(!m){if(u.push(rt(_)),u.length>20)u.shift()}},startRingback(_){s?.startRingback?.(_)},stopRingback(){s?.stopRingback?.()}}}async function qh(){return(await navigator.mediaDevices.enumerateDevices()).filter((h)=>h.kind==="audioinput").map((h,e)=>({deviceId:h.deviceId,label:h.label||`Microphone ${e+1}`}))}function bh(t,h){let e=new kt(t,!1),s,m=!1,i,u=()=>{return m=!0,i??=(async()=>{try{await e.cancel()}finally{s?.dispose(),s=void 0}})()};return{async start(_={}){if(m)throw Error("Microphone preview is already used");m=!0;try{if(s=h?await gt(h,!0):Tt(!0),i)throw s.dispose(),s=void 0,Error("Microphone preview was cancelled");await e.start(s.urls[0],{...o,..._})}catch(a){throw await u(),a}},stop:u}}function Vh(t){return{async preflight(h,e){await $h(t,h,e)},create(h){let e=new Dt(h),s,m=!1,i=()=>{m=!0;try{e.stop()}finally{s?.dispose(),s=void 0}};return{async start(u,_){try{if(s=await L("audio_assets",(a)=>e.recordSessionEvent(a),()=>t?gt(t):Promise.resolve(Tt())),m)throw Error("Audio session was cancelled");await e.start(u,s.urls[0],s.urls[1],_)}catch(a){throw i(),a}},stop:i,recordSessionEvent:(u)=>e.recordSessionEvent(u),setMuted:(u)=>e.setMuted(u),sendDTMF:(u)=>e.sendDTMF(u),setOutputVolume:(u)=>e.setOutputVolume(u),startRingback:(u)=>e.startRingback(u),stopRingback:()=>e.stopRingback()}}}}function ch(t){let h=Vh(t);return{preflight:(e,s)=>h.preflight(e,s),prepare:(e,s)=>Ph(t,e,s),create(e){return gh(e,()=>h.create(e),()=>{let s=new ot(e),m,i=!1;return{async start(u,_){try{if(m=await L("audio_assets",(a)=>s.recordSessionEvent(a),()=>t?gt(t,!0):Promise.resolve(Tt(!0))),i)throw Error("Audio session cancelled");await s.start(u,_,m.urls[0])}catch(a){throw s.stop(),m?.dispose(),m=void 0,a}},stop(){i=!0,s.stop(),m?.dispose(),m=void 0},setMuted:(u)=>s.setMuted(u),sendDTMF:(u)=>s.sendDTMF(u),setOutputVolume:(u)=>s.setOutputVolume(u),recordSessionEvent:(u)=>s.recordSessionEvent(u),startRingback:(u)=>s.startRingback(u),stopRingback:()=>s.stopRingback()}})}}}async function Ph(t,h,e){if(!t||typeof location>"u"||new URL(t.mcpURL(),location.href).origin!==location.origin)return;await L("audio_assets_prepare",e,async()=>{(await gt(t,h.mediaTransport==="webrtc"||h.mediaTransport==="auto")).dispose()})}async function $h(t,h,e){await Promise.all([L("microphone_preflight",e,async()=>{(await navigator.mediaDevices.getUserMedia({audio:_t(h)})).getTracks().forEach((m)=>m.stop())}),Ph(t,h,e)])}function wh(t,h){return{create(e){let s={...o,...h},m=D(s),i=new Ot(s),u=[],_,a,f,S,M=!1,g=!1,P=!1,c=!1,q=!1,w=!1,y=0,b,O,U,N,X,K,yt=!1,tt,ht,j=s.outputVolume??1,qt={},r=32000,C,l,E,n,Q=[],x=(A)=>{if(_?.readyState===WebSocket.OPEN&&_.bufferedAmount<65536)_.send(JSON.stringify(A));else throw Error("Listener connection unavailable")},F=()=>{if(M)throw Error("Listening cancelled")},R=()=>{let A=++y;if(q=w=!1,C?.(A,Error("Coaching cancelled")),n?.(),l=void 0,tt)clearInterval(tt);if(tt=void 0,X)X.onconnectionstatechange=null,X.close(),X=void 0;if(O?.disconnect(),O=void 0,U?.disconnect(),U=void 0,N?.disconnect(),N=void 0,b?.getTracks().forEach((T)=>{T.onended=null,T.stop()}),b=void 0,c&&g&&_?.readyState===WebSocket.OPEN)try{x({type:"coach.stop",generation:A})}catch{}e.onTalking?.(!1)},J=()=>R(),Z=()=>{if(document.visibilityState!=="visible")R()},et=()=>{if(M)return;if(R(),M=!0,E?.(),window.removeEventListener("blur",J),document.removeEventListener("visibilitychange",Z),ht)clearInterval(ht);if(ht=void 0,_)_.onmessage=_.onclose=_.onerror=null,_.close(1000,"listener_shutdown");if(a)a.ontrack=a.onconnectionstatechange=null,a.close();for(let A of Q)A.pause(),A.srcObject=null;if(S?.disconnect(),f)f.onstatechange=null,f.close().catch(()=>{});K?.dispose(),K=void 0},v=(A)=>{if(!M)et(),e.onClose(A)},W=async(A,T)=>{if(await A.setLocalDescription(await A.createOffer()),await new Promise((B,p)=>{if(A.iceGatheringState==="complete"){B();return}let G=setTimeout(()=>{A.onicegatheringstatechange=null,p(Error("ICE gathering timed out"))},7000);A.onicegatheringstatechange=()=>{if(A.iceGatheringState==="complete"||!T())clearTimeout(G),A.onicegatheringstatechange=null,T()?B():p(Error("Coaching cancelled"))}}),!T())throw Error("Listening cancelled");return A.localDescription.sdp};return{async start(A,T){if(c=T?.coaching===!0,F(),typeof RTCPeerConnection!=="function")throw Error("WebRTC audio unavailable");try{if(f=new AudioContext({latencyHint:"interactive"}),await f.resume(),F(),s.outputDeviceId&&"setSinkId"in f)await f.setSinkId(s.outputDeviceId);if(F(),S=f.createGain(),S.gain.value=j,S.connect(f.destination),f.onstatechange=()=>{if(f?.state==="suspended"||f?.state==="interrupted")v("listener_audio_paused")},await new Promise((G,z)=>{let k=!1,V=!1,Y=(mt)=>{if(k)return;k=!0,clearTimeout(st),E=void 0,mt?z(mt):G()},st=setTimeout(()=>Y(Error("WebRTC listener timed out")),18000),Qt=()=>{if(P&&V&&!M&&!g)g=!0,Y(),e.onReady("webrtc")};E=()=>Y(Error("Listening cancelled")),_=new WebSocket(It(A,s.webrtcFec)),_.onmessage=({data:mt})=>{if(M)return;if(typeof mt!=="string"){Y(Error("Invalid WebRTC listener frame")),v("listener_protocol_error");return}let d;try{d=JSON.parse(mt)}catch{Y(Error("Invalid listener control")),v("listener_protocol_error");return}if(d.type==="webrtc.config"){if(a||d.listener!==!0){Y(Error("Invalid WebRTC listener configuration"));return}qt={iceServers:d.ice_servers??[]},r=d.bitrate??32000,a=new RTCPeerConnection(qt);let ut=a;for(let I=0;I<2;I++)ut.addTransceiver("audio",{direction:"recvonly"});ut.ontrack=(I)=>{if(M||!f||!S)return;u.push(I.receiver);let Yt=ut.getTransceivers().findIndex((ct)=>ct.receiver===I.receiver);if(Yt<0||Yt>1){v("listener_protocol_error");return}if("jitterBufferTarget"in I.receiver)try{I.receiver.jitterBufferTarget=m.initialTargetMs}catch{}let mh=new MediaStream([I.track]),Bt=new Audio;Bt.muted=!0,Bt.srcObject=mh,Q.push(Bt),Bt.play().catch(()=>{});let uh=f.createMediaStreamSource(mh);if(h.stereo){let ct=f.createStereoPanner();ct.pan.value=Yt===0?-1:1,uh.connect(ct).connect(S)}else{let ct=f.createGain();ct.gain.value=0.5,uh.connect(ct).connect(S)}},ut.onconnectionstatechange=()=>{if(M)return;if(ut.connectionState==="connected")P=!0,Qt();if(ut.connectionState==="failed"||ut.connectionState==="closed"){if(Y(Error("WebRTC listener connection failed")),k&&g)v("listener_network_error")}},W(ut,()=>!M).then((I)=>x({type:"webrtc.offer",sdp:I})).catch((I)=>Y(I));return}if(d.type==="webrtc.answer"){a?.setRemoteDescription({type:"answer",sdp:d.sdp}).catch((ut)=>Y(ut));return}if(d.type==="listener.ready"){if(c&&d.coaching!==!0){Y(Error("Coaching not authorized"));return}V=!0,Qt();return}if(d.type==="coach.started"){C?.(d.generation);return}if(d.type==="coach.rejected"){C?.(y,Error(d.detail||"Coaching unavailable"));return}if(d.type==="coach.stopped"&&(d.generation===void 0||d.generation===y)){R();return}if(d.type==="coach.webrtc.answer"){l?.(d.sdp,d.generation);return}},_.onerror=()=>{if(Y(Error("Listener connection failed")),g)v("listener_network_error")},_.onclose=(mt)=>{if(Y(Error(mt.reason||"Listener disconnected")),g)v(mt.reason||"listener_disconnected")}}),F(),c)window.addEventListener("blur",J),document.addEventListener("visibilitychange",Z);let B,p=!1;ht=setInterval(()=>{if(M||!a||p)return;p=!0,a.getStats().then((G)=>{if(M)return;let z=vt(G,B);B=z.previous,i.observe({...z.metrics,receiver_jitter:z.webrtc.jitterMs/1000});for(let k of u)if("jitterBufferTarget"in k)try{k.jitterBufferTarget=i.target}catch{}e.onDiagnostics?.({media_transport:"webrtc",dropped_ms:[],played_ms:[],max_queue_ms:z.queueMs,max_late_ms:0,sequence_gaps:[],network_excess_ms:0,network_dropped_ms:[],webrtc:z.webrtc})}).catch(()=>{}).finally(()=>{p=!1})},1000)}catch(B){throw et(),B}},async startTalking(){if(F(),!c||!g||!f)throw Error("Coaching session not ready");if(q)return;q=!0;let A=++y,T=()=>!M&&q&&y===A;try{let B=await navigator.mediaDevices.getUserMedia({audio:_t(s)});if(!T()){B.getTracks().forEach((G)=>G.stop());return}if(b=B,b.getTracks().forEach((G)=>{G.onended=()=>R()}),!K){let G=await gt(t,!0);if(!T()){G.dispose();return}K=G}if(!yt)await f.audioWorklet.addModule(K.urls[0]),yt=!0;if(!T())return;if(await new Promise((G,z)=>{let k=setTimeout(()=>{C=void 0,z(Error("Coaching activation timed out"))},3000);C=(V,Y)=>{if(Y||V===A)clearTimeout(k),C=void 0,Y?z(Y):G()},x({type:"coach.start",generation:A})}),!T())return;tt=setInterval(()=>{if(T())try{x({type:"coach.keepalive",generation:A})}catch{R()}},1000),O=new AudioWorkletNode(f,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),N=f.createMediaStreamDestination(),N.channelCount=1,U=f.createMediaStreamSource(B),U.connect(O).connect(N);let p=new RTCPeerConnection(qt);if(X=p,p.addTransceiver(N.stream.getAudioTracks()[0],{direction:"sendonly",streams:[N.stream],sendEncodings:[{maxBitrate:r}]}),await new Promise((G,z)=>{let k=!1,V=(st)=>{if(k)return;k=!0,clearTimeout(Y),n=void 0,l=void 0,st?z(st):G()},Y=setTimeout(()=>V(Error("Coaching WebRTC timed out")),18000);n=()=>V(Error("Coaching cancelled")),l=(st,Qt)=>{if(Qt!==A||!T())return;p.setRemoteDescription({type:"answer",sdp:st}).catch((mt)=>V(mt))},p.onconnectionstatechange=()=>{if(!T())return;if(p.connectionState==="connected")V();if(p.connectionState==="failed"||p.connectionState==="closed")V(Error("Coaching media disconnected")),R()},W(p,T).then((st)=>{if(T())x({type:"coach.webrtc.offer",generation:A,sdp:st})}).catch((st)=>V(st))}),T())w=!0,e.onTalking?.(!0)}catch(B){if(T())throw R(),B}},stopTalking:R,stop:et,setOutputVolume(A){if(!Number.isFinite(A)||A<0||A>1)throw RangeError("Listener volume must be 0–1");if(j=A,S)S.gain.value=A}}}}}function Et(t={},h={}){let e=["mediaTransport","webrtcFec","inputDeviceId","outputDeviceId","outputVolume","echoCancellation","noiseSuppression","autoGainControl","inputGainDB","highpassFilter","playbackTargetMs","playbackMinMs","playbackMaxMs","playbackAdaptive","stereo"],s={...o};for(let m of[t,h])for(let i of e){let u=m[i];if(u!==void 0)s[i]=u}if(D(s),s.mediaTransport=dt(s.mediaTransport),s.webrtcFec??=!0,s.outputVolume!==void 0&&(!Number.isFinite(s.outputVolume)||s.outputVolume<0||s.outputVolume>1))throw RangeError("Listener volume must be 0–1");return s}function dh(t,h,e,s){let m,i=!1,u=!1,_,a={...t,onReady(f){u=!0,t.onReady(f)}};return{async start(f,S){if(i)throw Error("Listening cancelled");m=h==="websocket"?e(a):s(a);try{await m.start(f,S)}catch(M){if(m.stop(),i||u||h!=="auto"||Gt(M))throw M;m=e(a),await m.start(f,S)}if(i)m.stop();else if(_!==void 0)m.setOutputVolume(_)},async startTalking(){if(i||!m?.startTalking)throw Error("Coaching session not ready");await m.startTalking()},stopTalking(){m?.stopTalking?.()},stop(){i=!0,m?.stop()},setOutputVolume(f){if(!Number.isFinite(f)||f<0||f>1)throw RangeError("Listener volume must be 0–1");_=f,m?.setOutputVolume(f)}}}function th(t,h={}){let e=Et({},h),s=dt(e.mediaTransport),m=Lh(t,e),i=wh(t,e);return{create(u){return dh(u,s,(_)=>m.create(_),(_)=>i.create(_))}}}function Xh(t){if(t.byteLength<26||t.byteLength>984||t.byteLength%2)throw Error("Invalid listener audio frame");let h=new DataView(t),e=h.getUint32(4,!0);if(h.getUint32(0,!0)!==827085889||e>1)throw Error("Invalid listener audio protocol");let s=Number(h.getBigUint64(8,!0)),m=Number(h.getBigUint64(16,!0));if(!Number.isSafeInteger(s)||!Number.isSafeInteger(m))throw Error("Invalid listener audio timing");let i=new Float32Array((t.byteLength-24)/2);for(let u=0;u<i.length;u++)i[u]=h.getInt16(24+u*2,!0)/32768;return{direction:e,sequence:s,timestampMS:m,frame:i}}function Yh(t,h,e,s){if(t.length<1||t.length>480||!Number.isFinite(s)||s<0||!Number.isInteger(h)||h<1||h>4294967295)throw Error("Invalid coaching capture");let m=new ArrayBuffer(24+t.length*2),i=new DataView(m);i.setUint32(0,826496833,!0),i.setUint32(4,h,!0),i.setUint32(8,e>>>0,!0),i.setFloat64(16,s,!0);for(let u=0;u<t.length;u++){let _=Math.max(-1,Math.min(1,t[u]));i.setInt16(24+u*2,_<0?_*32768:_*32767,!0)}return m}function Lh(t,h={}){return{create(e){let s,m,i,u,_=!1,a=[],f=!1,S=0,M=!1,g=!1,P,c,q,w,y,b,O,U=!1,N,X,K=0,yt=!1,tt=[0,0],ht=[0,0],j=[void 0,void 0],qt=[new St,new St],r=h.outputVolume??1,C,l=()=>{let R=++S;if(M=!1,g=!1,O?.(R,Error("Coaching cancelled")),b)clearInterval(b);if(b=void 0,y?.port1.close(),y?.port2.close(),y=void 0,c?.disconnect(),c=void 0,q?.disconnect(),q=void 0,w?.disconnect(),w=void 0,P?.getTracks().forEach((J)=>{J.onended=null,J.stop()}),P=void 0,f&&yt&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.stop",generation:R}));e.onTalking?.(!1)},E=()=>l(),n=()=>{if(document.visibilityState!=="visible")l()},Q=()=>{if(_)return;if(l(),_=!0,C?.(),window.removeEventListener("blur",E),document.removeEventListener("visibilitychange",n),u)u.onmessage=u.onclose=u.onerror=null,u.close(1000,"listener_shutdown");if(m?.disconnect(),i?.disconnect(),s)s.onstatechange=null,s.close().catch(()=>{});for(let R of a.splice(0))R.dispose()},x=(R)=>{if(!_)Q(),e.onClose(R)},F=()=>{if(_)throw Error("Listening cancelled")};return{async start(R,J){f=J?.coaching===!0,F();try{if(s=new AudioContext({latencyHint:"interactive"}),s.state==="suspended")await s.resume();if(F(),h.outputDeviceId&&"setSinkId"in s)await s.setSinkId(h.outputDeviceId);F();let Z=await Ft(t,"listener");if(_)Z.dispose(),F();if(a.push(Z),await s.audioWorklet.addModule(Z.urls[0]),F(),m=new AudioWorkletNode(s,"telephony-listener",{numberOfInputs:0,outputChannelCount:[h.stereo?2:1],processorOptions:{stereo:h.stereo,...D(h)}}),i=s.createGain(),i.gain.value=r,m.connect(i).connect(s.destination),m.onprocessorerror=()=>x("listener_audio_error"),m.port.onmessage=({data:et})=>{if(et?.type==="listener.diagnostics"&&!_)try{e.onDiagnostics?.({...et,media_transport:"websocket",sequence_gaps:[...ht],network_excess_ms:K,network_dropped_ms:[...tt]})}catch{}},s.onstatechange=()=>{if(s?.state==="suspended"||s?.state==="interrupted")x("listener_audio_paused")},await new Promise((et,v)=>{let W=!1,A=setTimeout(()=>{T(Error("Listener connection timed out")),x("listener_network_error")},1e4),T=(B)=>{if(W)return;W=!0,clearTimeout(A),C=void 0,B?v(B):et()};C=()=>T(Error("Listening cancelled")),u=new WebSocket(R),u.binaryType="arraybuffer",u.onmessage=({data:B})=>{if(_||!s||!m)return;try{if(typeof B==="string"){let V=JSON.parse(B);if(V.type==="listener.ready"){if(f&&V.coaching!==!0)throw Error("Coaching not authorized");yt=!0,T(),e.onReady("websocket")}else if(V.type==="coach.started")O?.(V.generation);else if(V.type==="coach.rejected")O?.(S,Error(V.detail||"Coaching unavailable"));else if(V.type==="coach.stopped"&&(V.generation===void 0||V.generation===S))l();return}if(!yt||!(B instanceof ArrayBuffer))return;let p=Xh(B),G=p.direction;if(j[G]!==void 0&&p.sequence<j[G])return;if(j[G]!==void 0&&p.sequence>j[G])ht[G]+=p.sequence-j[G];j[G]=p.sequence+1;let z=performance.now()-p.timestampMS;if(X=Math.min(X??z,z),K=Math.max(0,z-X),K>200){tt[G]+=p.frame.length*1000/24000;return}N??=s.currentTime*1000+(h.playbackTargetMs??60)-p.timestampMS;let k=qt[G].process(p.frame,24000,s.sampleRate);m.port.postMessage({direction:G,frame:k,playAtMS:p.timestampMS+N},[k.buffer])}catch{T(Error("Invalid listener media")),x("listener_protocol_error")}},u.onerror=()=>{T(Error("Listener connection failed")),x("listener_network_error")},u.onclose=(B)=>{T(Error(B.reason||"Listener disconnected")),x(B.reason||"listener_disconnected")}}),F(),f)window.addEventListener("blur",E),document.addEventListener("visibilitychange",n)}catch(Z){throw Q(),Z}},async startTalking(){if(F(),!f||!yt||!s||!u||u.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(M)return;M=!0;let R=++S,J=()=>!_&&M&&R===S;try{let Z=await navigator.mediaDevices.getUserMedia({audio:_t({...o,...h})});if(!J()){Z.getTracks().forEach((W)=>W.stop());return}if(P=Z,P.getAudioTracks().forEach((W)=>{W.onended=()=>l()}),!U){let W=await gt(t,!0);if(!J()){W.dispose();return}a.push(W),await s.audioWorklet.addModule(W.urls[0]),U=!0}if(!J())return;if(await new Promise((W,A)=>{let T=setTimeout(()=>{O=void 0,A(Error("Coaching activation timed out"))},3000);O=(B,p)=>{if(p||B===R)clearTimeout(T),O=void 0,p?A(p):W()},u.send(JSON.stringify({type:"coach.start",generation:R}))}),!J())return;c=new AudioWorkletNode(s,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:h.inputGainDB??0,highpassFilter:h.highpassFilter!==!1}}),q=s.createMediaStreamSource(Z),w=s.createGain(),w.gain.value=0,q.connect(c).connect(w).connect(s.destination),y=new MessageChannel;let et=0,v=new St;y.port1.onmessage=({data:W})=>{if(!J()||!g||W?.type!=="capture"||!(W.frame instanceof Float32Array)||!s||!u)return;if(!Number.isFinite(W.timestamp_ms)||s.currentTime*1000-W.timestamp_ms>150||u.bufferedAmount>2880||u.readyState!==WebSocket.OPEN)return;let A=v.process(W.frame,W.sample_rate,24000);if(A.length>0&&A.length<=480)u.send(Yh(A,R,et++,W.timestamp_ms))},y.port1.start(),c.port.postMessage({type:"transport",port:y.port2},[y.port2]),g=!0,e.onTalking?.(!0),b=setInterval(()=>{if(J()&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.keepalive",generation:R}))},1000)}catch(Z){if(J())throw l(),Z}},stopTalking:l,stop:Q,setOutputVolume(R){if(!Number.isFinite(R)||R<0||R>1)throw RangeError("Listener volume must be 0–1");if(r=R,i)i.gain.value=R}}}}}class hh{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;audioOptions;constructor(t,h={}){this.client=t;this.options=h;this.audioOptions=Et(h.audio,h),this.runtime=h.runtime??th(t.app,this.audioOptions)}setAudioOptions(t){let h=Et({},t);this.audioOptions=h,this.runtime=this.options.runtime??th(this.client.app,h)}getAudioOptions=()=>Object.freeze({...this.audioOptions});getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.observers.add(t),()=>{this.observers.delete(t)}};update(t){this.snapshot=Object.freeze({...this.snapshot,...t});for(let h of this.observers)try{h(this.snapshot)}catch{}}async listen(t){return this.begin(t,!1)}async coach(t){return this.begin(t,!0)}async begin(t,h){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=h,this.retryUntil=0,this.retryDelay=500;let e=++this.generation;return this.update({state:"connecting",callId:t,detail:void 0,coaching:h,talking:!1,mediaTransport:void 0}),this.connect(t,e)}async connect(t,h){let e,s=++this.attempt,m=()=>h===this.generation&&s===this.attempt&&!this.disposed;try{if(e=this.coaching?await this.client.coachSession(t):await this.client.listenSession(t),!m()){await this.client.stopListening(e).catch(()=>{});return}this.session=e;let i=this.runtime.create({onReady:(u)=>{if(m())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0,mediaTransport:u})},onClose:(u)=>{if(m())this.disconnected(u,t,h)},onTalking:(u,_)=>{if(m())this.update({talking:u,detail:_})},onDiagnostics:(u)=>{if(m())try{this.options.onDiagnostics?.(u)}catch{}}});if(this.audio=i,this.lease=new xt(e.lease_seconds,()=>this.client.renewListening(e),(u)=>{if(m())this.disconnected(u==="revoked"?"access_revoked":"media_disconnected",t,h,u==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,e.lease_started_ms),await i.start(this.client.listenerMediaURL(e,this.audioOptions.mediaTransport),{coaching:e.coaching===!0}),!m())i.stop()}catch(i){if(!m()||e&&this.session!==e)return;let u=i?.status,_;try{_=JSON.parse(i.body??"{}").code}catch{}let a=_==="call_ended"?"call_ended":u===401||u===403||u===404?"access_revoked":"listener_disconnected";throw this.disconnected(a,t,h,String(i)),i}}cleanup(){this.lease?.stop(),this.lease=void 0;let t=this.audio;this.audio=void 0,t?.stop();let h=this.session;return this.session=void 0,h?this.client.stopListening(h).catch(()=>{}):Promise.resolve()}disconnected(t,h,e,s=t){if(e!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let m=t==="call_ended",i=t==="access_revoked",u=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(t);if(!this.coaching&&!m&&!i&&u&&this.options.reconnect!==!1){if(this.retryUntil||=Date.now()+30000,Date.now()<this.retryUntil){this.update({state:"reconnecting",detail:s,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,e===this.generation&&!this.disposed)this.connect(h,e).catch(()=>{})},this.retryDelay),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:m?"call_ended":i?"access_revoked":"disconnected",detail:s,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(t){this.audio?.setOutputVolume(t)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let t=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1,mediaTransport:void 0}),await t}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}function Ch(t){if(!t)return"placing";if(sh(t))return"ended";if(t==="ringing")return"ringing";if(t==="answered"||t==="in-progress")return"connected";return"placing"}var Mt=(t)=>t instanceof Error?t.message:String(t);class eh{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;hasStatusPush=!1;scheduleStatusPoll;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(t,h={}){this.client=t;this.options=h;if(this.runtime=h.audioRuntime??ch(t.app),this.audioOptions={...o,...h.audio,...h.mediaTransport?{mediaTransport:h.mediaTransport}:{}},D(this.audioOptions),this.interval=h.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}createCallListener(t={}){return this.assertOpen(),this.client.createCallListener({...this.audioOptions,...t.audio,...t})}getSnapshot=()=>this.snapshot;subscribe=(t)=>{return this.assertOpen(),this.listeners.add(t),()=>{this.listeners.delete(t)}};update(t){let h={...this.snapshot,...t};if(h.callId&&Ch(h.carrierStatus)==="connected")h.phase=h.audioState==="live"?"connected":"placing";this.snapshot=Object.freeze(h);for(let e of this.listeners)try{e(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(t){if(this.disposed||t!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(t){return!this.disposed&&t===this.generation}finish(t){if(this.current(t))this.update({busy:!1})}cancellable(t,h){let e=this.cancellation.signal;return new Promise((s,m)=>{let i=()=>{e.removeEventListener("abort",i),m(Error("Softphone operation cancelled"))};if(e.addEventListener("abort",i,{once:!0}),t.then(s,m).finally(()=>e.removeEventListener("abort",i)),!this.current(h)||e.aborted)i()})}begin(t){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||t&&this.session)throw Error("Softphone already has an active operation or call");let h=this.invalidate();return this.update({busy:!0,detail:void 0}),h}async prepareAudio(t,h){let e=[],s=(m)=>{if(this.current(t)&&!this.cancellation.signal.aborted)e.push(rt(m))};try{return await this.cancellable(L(h?"audio_preflight":"audio_prepare",s,()=>h?this.runtime.preflight(this.audioOptions,s):this.runtime.prepare?.(this.audioOptions,s)??Promise.resolve()),t),this.assertCurrent(t),e}catch(m){if(this.current(t))for(let i of e)this.reportSessionEvent(i);throw m}}async dial(t){let h=this.begin(!0),e,s=!1;try{this.assertCurrent(h);let m={to:t.to.trim(),from:t.from?.trim(),timeout_sec:t.timeout_sec,recording:t.recording},i=JSON.stringify(m);if(!this.intent||this.intent.value!==i||t.idempotency_key&&t.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:i,request:{...m,idempotency_key:t.idempotency_key||crypto.randomUUID()}};let u=await this.prepareAudio(h,!0);return this.assertCurrent(h),e=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(h),s=!0,this.outbound=!0,await this.attachAudio(e,h,u),e.call_id}catch(m){if(e&&(this.current(h)||!s||this.disposed))await this.recoverStartup(e,h,()=>this.client.hangup(e.call_id));if(this.current(h))this.update({detail:Mt(m)});throw m}finally{this.finish(h)}}async answer(t,h={}){let e=this.begin(!0),s,m=!1;try{this.assertCurrent(e);let i=await this.prepareAudio(e,!0);s=await this.client.answer(t,h),this.assertCurrent(e),m=!0,this.outbound=!1,await this.attachAudio(s,e,i)}catch(i){if(s&&(this.current(e)||!m||this.disposed))await this.recoverStartup(s,e,()=>this.client.release(s));if(this.current(e))this.update({detail:Mt(i)});throw i}finally{this.finish(e)}}async recoverStartup(t,h,e){try{if(await e(),this.current(h))this.clearCall()}catch{if(this.current(h))this.session=t,this.update({callId:t.call_id,audioState:"error"}),this.startPolling()}}async join(t){await this.answer(t,{rejoin:!0})}async attach(t,h="attach"){await this.acquireSession(t,!1,h)}async takeover(t){await this.acquireSession(t,!0)}async acquireSession(t,h,e="takeover"){let s=this.begin(!0);try{let m=await this.prepareAudio(s,!0);this.assertCurrent(s);let i=Ct(h?"takeover":e),u=await(h?this.client.takeover(t,i):this.client.attach(t,i));this.assertCurrent(s),await this.attachAudio(u,s,m),await this.reconcileAttachedCall(u,s)}catch(m){if(this.current(s))this.update({detail:Mt(m)});throw m}finally{this.finish(s)}}async reconnect(t,h="manual_reconnect"){if(!this.session)throw Error("No call to reconnect");let e={...this.audioOptions,...t};D(e);let s=this.begin(!1);this.audioOptions=e;let m=this.session.call_id,i=Ct(h),u=this.session.session_generation,_=(a,f={})=>this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:a,...i,previous_session_id:u,...f});_("started");try{let a=await this.prepareAudio(s,!1),f=await this.client.attach(m,{...i,previous_session_id:u});this.assertCurrent(s),await this.attachAudio(f,s,a),await this.reconcileAttachedCall(f,s),_("connected",{session_id:f.session_generation})}catch(a){if(_(this.current(s)?"failed":"cancelled"),this.current(s))this.update({detail:Mt(a)});throw a}finally{this.finish(s)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let t=this.session;if(!t){this.cancellation.abort();return}let h=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let e=(async()=>{try{if(await this.client.hangup(t.call_id),this.current(h))this.clearCall()}catch(s){if(this.current(h))this.update({audioState:"error",detail:Mt(s)}),this.startPolling();throw s}finally{this.finish(h)}})();this.hangingUp=e;try{await e}finally{if(this.hangingUp===e)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(t){this.assertOpen();let h=this.session;if(!h)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let e=this.generation;this.update({busy:!0,detail:void 0});let s;try{s=this.client[t](h.call_id)}catch(m){if(this.current(e)&&this.session===h)this.update({busy:!1,detail:Mt(m)});throw m}this.controlPending=s;try{let m=await s;if(this.current(e)&&this.session===h)this.update({holdState:m.hold_state,recordingState:m.recording_state,controlError:m.control_error,capabilities:m.capabilities});return m}catch(m){if(this.current(e)&&this.session===h)this.update({detail:Mt(m)});throw m}finally{if(this.controlPending===s)this.controlPending=void 0;if(this.current(e)&&this.session===h)this.update({busy:!1})}}configureAudio(t){this.assertOpen();let h={...this.audioOptions,...t};D(h),this.audioOptions=h}setMuted(t){this.assertOpen(),this.audio?.setMuted(t),this.update({muted:t})}setOutputVolume(t){if(this.assertOpen(),!Number.isFinite(t)||t<0||t>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=t,this.audio?.setOutputVolume(t)}sendDTMF(t){if(this.assertOpen(),!/^[0-9*#]+$/.test(t))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(t)}observeCall(t){if(this.disposed||t.id!==this.session?.call_id)return;if(t.direction)this.outbound=t.direction==="outbound";if(sh(t.status)){this.invalidate(),this.clearCall(t.status),this.update({busy:!1,phase:"ended",termination:t.termination,answeredBy:t.answered_by??this.snapshot.answeredBy,endedAt:t.ended_at});return}this.update({carrierStatus:t.status,phase:Ch(t.status),answeredBy:t.answered_by??this.snapshot.answeredBy,holdState:t.hold_state??this.snapshot.holdState,recordingState:t.recording_state??this.snapshot.recordingState,controlError:t.control_error??this.snapshot.controlError,capabilities:t.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let t=this.options.ringback;return typeof t==="object"&&t?t.country:void 0}syncRingback(){let t=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(t&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!t&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(t,h,e=[]){this.hasStatusPush=!1,this.assertCurrent(h),this.stopAudio(),this.session=t,this.update({callId:t.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let s,m=()=>!this.disposed&&s!==void 0&&this.audio===s,i=(a)=>{if(m())try{a()}catch{}},u,_;try{this.assertCurrent(h),s=this.runtime.create({refreshMediaURL:(a)=>{let f=a??Ct("automatic_retry");if(u)return this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"coalesced",...f,shared_attempt_id:_?.attempt_id}),u;if(!m()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let S=this.generation,M=(async()=>{let P=t.session_generation,c=await this.client.attach(t.call_id,{...f,previous_session_id:P});if(this.assertCurrent(S),!m())throw Error("Audio connection no longer active");return t=c,this.session=c,this.startLease(c),this.reportSessionEvent({timestamp:new Date().toISOString(),action:"media_session",outcome:"replaced",...f,previous_session_id:P,session_id:c.session_generation}),this.client.mediaURL(c,this.audioOptions.mediaTransport)})();u=M,_=f;let g=()=>{if(u===M)u=void 0,_=void 0};return M.then(g,g),M},sessionDiagnostics:()=>({session_id:t.session_generation}),onSessionEvent:(a)=>i(()=>this.options.onSessionEvent?.(rt({...a,session_id:a.session_id??t.session_generation}))),onState:(a,f)=>{if(!m())return;if(a==="ended"&&f==="call.ended"){this.observeCall({id:t.call_id,status:"completed"});return}if(this.update({audioState:a,detail:f}),this.scheduleStatusPoll?.(),m()&&(a==="error"||a==="ended")){if(this.stopAudio(),a==="error")this.reconcileFailedAudio(t,this.generation)}},onMediaReadiness:(a)=>i(()=>this.options.onMediaReadiness?.(a)),onLevels:(a,f)=>i(()=>this.options.onLevels?.(a,f)),onAudioHealth:(a)=>i(()=>this.options.onAudioHealth?.(a)),onDiagnostics:(a)=>i(()=>this.options.onDiagnostics?.(a)),onNotice:(a)=>i(()=>this.options.onNotice?.(a)),onCallStatus:(a)=>i(()=>{this.hasStatusPush=!0,this.observeCall({id:a.call_id,status:a.status,direction:a.direction,answered_at:a.answered_at,ended_at:a.ended_at,answered_by:a.answered_by,termination:a.termination,hold_state:a.hold_state,recording_state:a.recording_state,control_error:a.control_error}),this.scheduleStatusPoll?.()})}),this.audio=s,this.assertCurrent(h);for(let a of e)this.reportSessionEvent(a);if(this.assertCurrent(h),this.startPolling(),this.startLease(t),s.setMuted(this.snapshot.muted),await this.cancellable(s.start(this.client.mediaURL(t,this.audioOptions.mediaTransport),this.audioOptions),h),this.assertCurrent(h),!m())throw Error(this.snapshot.detail||"Audio connection ended during setup");s.setMuted(this.snapshot.muted),this.syncRingback()}catch(a){if(m())this.stopAudio();if(this.current(h))this.update({audioState:"error",detail:Mt(a)});throw a}}async reconcileAttachedCall(t,h){try{let e=await this.client.getCall(t.call_id);if(this.current(h)&&this.session===t&&e)this.observeCall(e)}catch{}}async reconcileFailedAudio(t,h){try{let e=await this.client.getCall(t.call_id);if(!this.current(h)||this.session!==t||!e)return;if(e.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(e)}catch{}}startLease(t){if(this.lease?.stop(),!t.lease_seconds)return;this.lease=new xt(t.lease_seconds,()=>this.client.renew(t),(h,e)=>{if(this.disposed||this.session!==t)return;this.stopAudio(),this.update({audioState:"error",detail:h==="expired"?"Audio session expired. Reconnect audio.":`Audio authorization ended: ${Mt(e??h)}`})},(h)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(h);else try{this.options.onSessionEvent?.(h)}catch{}},t.lease_started_ms)}reportSessionEvent(t){if(t=rt(t),this.audio?.recordSessionEvent)try{this.audio.recordSessionEvent(t)}catch{}else try{this.options.onSessionEvent?.(t)}catch{}}stopAudio(){this.lease?.stop(),this.lease=void 0;let t=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{t?.stopRingback?.()}catch{}}try{t?.stop()}catch{}if(t)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){this.scheduleStatusPoll=void 0,clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let t=new AbortController;this.polling=t;let h=!1,e=performance.now(),s=()=>this.hasStatusPush&&this.snapshot.audioState==="live"?Math.max(1,Math.max(this.interval,30000)-(performance.now()-e)):this.interval,m=async()=>{if(h)return;let i=this.session?.call_id;if(!i||t.signal.aborted)return;h=!0;try{let u=await this.client.getCall(i,t.signal);if(e=performance.now(),!t.signal.aborted&&u)this.observeCall(u)}catch(u){if(!t.signal.aborted)this.update({detail:`Call status unavailable: ${Mt(u)}`})}finally{h=!1,this.scheduleStatusPoll?.()}};this.scheduleStatusPoll=()=>{if(h||t.signal.aborted||!this.session||this.disposed)return;clearTimeout(this.timer),this.timer=setTimeout(m,s())},this.scheduleStatusPoll()}clearCall(t){this.hasStatusPush=!1,this.stopAudio(),this.stopPolling(),this.session=void 0,this.outbound=!1,this.update({callId:void 0,carrierStatus:t,audioState:"idle",muted:!1,detail:void 0,phase:t?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function zh(t,h="en"){if(t?.reason==="ai_inactivity")return h.toLowerCase().startsWith("fr")?"Fin de l’appel : aucune réponse":"Call ended: no response";if(t?.reason==="ai_policy_failure")return h.toLowerCase().startsWith("fr")?"Échec du rappel d’inactivité IA":"AI inactivity reminder failed";if(t?.reason==="time_limit"||t?.reason==="ai_max_duration")return h.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return t?.reason?.replaceAll("_"," ")??""}class lh extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function Jh(t){return t.direction==="inbound"&&t.status==="pending"&&t.answerable!==!1&&!t.routing_waiting&&(t.peer_kind==="human"||Boolean(t.ring_offers?.some((h)=>h.kind==="browser")))}var sh=(t)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(t);function $(t){if(!t||/[\s/\\?#]/.test(t))throw Error("Invalid call ID");return encodeURIComponent(t)}class Xt{app;options;transport;listMicrophones=qh;createMicrophonePreview=(t)=>bh(t,this.app);constructor(t,h={}){this.app=t;this.options=h;if(this.transport=new Lt(t,h.authProvider?"proxy":h.direct?"direct":h.runtime,globalThis.fetch,h.direct),t.name!=="telephony"||!t.projectId||!t.installId)throw Error("Telephony requires an explicit project and installation")}get(t,h){return this.transport.request("GET",t,void 0,h)}post(t,h,e){return this.transport.request("POST",t,h,e)}subscribe(t,h,e){return this.transport.subscribe(t,h,e)}path(t){if(!this.options.authProvider)return t;return`/user${t}${t.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(t){let h=await this.get(this.path("/calls"),{signal:t});if(!Array.isArray(h?.calls)||h.calls.some((e)=>!e||typeof e.id!=="string"||typeof e.status!=="string"))throw Error("Invalid Telephony calls response");return h.calls}async getCall(t,h){let e=await this.get(this.path(`/calls?call_id=${$(t)}`),{signal:h});if(!Array.isArray(e?.calls))throw Error("Invalid Telephony call response");return e.calls.find((s)=>s.id===t)}incomingCalls(t){return t.filter(Jh)}watchCalls(t,h={}){let e=h.intervalMs??2000;if(!Number.isFinite(e)||e<100)throw Error("Call watch interval must be at least 100 ms");let s=new AbortController,m,i,u=!1,_=!1,a=-1/0,f=performance.now(),S=()=>{let q=performance.now()-a;return q<15000?Math.max(1,Math.min(Math.max(e,30000)-(performance.now()-f),15000-q)):e},M=new Set,g=()=>{clearTimeout(m),s.abort(),i?.close(),h.signal?.removeEventListener("abort",g)},P=(q)=>{try{h.onError?.(q)}catch{g()}};if(h.signal?.addEventListener("abort",g,{once:!0}),h.signal?.aborted)g();let c=async(q)=>{if(s.signal.aborted)return;if(u){_=!0;return}clearTimeout(m),u=!0;let w=performance.now();try{let y=await this.listCalls(s.signal);if(f=performance.now(),!s.signal.aborted){t(y);for(let b of y){if(b.answerable!==!0||b.status!=="pending")continue;for(let O of b.ring_offers??[]){if(O.kind!=="browser"||!O.id||M.has(O.id))continue;M.add(O.id),this.acknowledgeOffer(b.id,O.id).catch((U)=>{if(![404,405].includes(U?.status??0))M.delete(O.id)})}}try{h.onTiming?.({trigger:q,fetchMs:performance.now()-w})}catch{}}}catch(y){if(!s.signal.aborted){let b=y?.status;try{h.onFailure?.({trigger:q,fetchMs:performance.now()-w,status:typeof b==="number"?b:void 0,error:y})}catch{}P(y)}}finally{if(u=!1,!s.signal.aborted)if(_)_=!1,c("push");else m=setTimeout(()=>void c("poll"),S())}};if(c("poll"),!s.signal.aborted&&h.push!==!1&&typeof this.app.subscribe==="function")try{i=this.subscribe(this.path("/calls/events"),(q)=>{if(q.type==="heartbeat"||q.type==="calls.changed"){if(a=performance.now(),q.type==="calls.changed")c("push");else if(!u)clearTimeout(m),m=setTimeout(()=>void c("poll"),S())}else if(q.type==="access.revoked")a=-1/0,i?.close(),c("push")},{transport:"fetch",signal:s.signal,reconnectDelayMs:250,onError:(q)=>{if(a=-1/0,!u&&!s.signal.aborted)clearTimeout(m),m=setTimeout(()=>void c("poll"),e);let w=q?.status;if(w===404||w===405||w===501)i?.close()}})}catch{}return{close:g}}async place(t){if(!/^\+[1-9]\d{7,14}$/.test(t.to)||!t.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let h=it();return this.session(await this.post(this.path("/softphone/place"),t),void 0,"media",h)}async answer(t,h={}){let e=it();try{return this.session(await this.post(this.path(`/softphone/answer/${$(t)}`),h),t,"media",e)}catch(s){let m=s;if(m.status===409&&typeof m.body==="string"){let i;try{i=JSON.parse(m.body)}catch{}if(i?.code==="offer_expired")throw new lh}throw s}}async acknowledgeOffer(t,h){await this.post(this.path(`/softphone/offer/ack/${$(t)}`),{offer_id:h})}async declineOffer(t,h){await this.post(this.path(`/softphone/offer/decline/${$(t)}`),{offer_id:h})}async attach(t,h){let e=it();return this.session(await this.post(this.path(`/softphone/attach/${$(t)}`),h?{media_diagnostics:h}:{}),t,"media",e)}async takeover(t,h){let e=it();return this.session(await this.post(this.path(`/softphone/takeover/${$(t)}`),h?{media_diagnostics:h}:{}),t,"media",e)}createCallListener(t={}){return new hh(this,{...t,...Et({...this.options.audio,...t.audio},t)})}async listenSession(t){let h=it();return this.session(await this.post(this.path(`/softphone/listen/${$(t)}`),{}),t,"listen-media",h)}async coachSession(t){let h=it(),e=this.session(await this.post(this.path(`/softphone/coach/${$(t)}`),{}),t,"listen-media",h);if(e.coaching!==!0)throw Error("Invalid coaching session");return e}async renewListening(t){return this.post(this.path(`/softphone/${t.coaching?"coach-renew":"listen-renew"}/${$(t.call_id)}`),{session_token:t.session_token})}async stopListening(t){await this.post(this.path(`/softphone/${t.coaching?"coach-stop":"listen-stop"}/${$(t.call_id)}`),{session_token:t.session_token})}async listenerAudit(t){return this.get(this.path(`/softphone/listen-audit/${$(t)}`))}outboundTransport(t){return this.post(this.path("/numbers/outbound-transport"),t)}outboundNumbers(t={}){return this.get(this.path(t.fresh?"/softphone/numbers?fresh=true":"/softphone/numbers"))}async renew(t){return this.post(this.path(`/softphone/renew/${$(t.call_id)}`),{session_token:t.session_token})}async release(t){if(!t.session_token)throw Error("Answer session has no release token");await this.post(this.path(`/softphone/release/${$(t.call_id)}`),{session_token:t.session_token})}async hangup(t){await this.post(this.path(`/calls/${$(t)}/hangup`),{})}hold(t){return this.post(this.path(`/calls/${$(t)}/hold`),{})}resume(t){return this.post(this.path(`/calls/${$(t)}/resume`),{})}pauseRecording(t){return this.post(this.path(`/calls/${$(t)}/pause-recording`),{})}resumeRecording(t){return this.post(this.path(`/calls/${$(t)}/resume-recording`),{})}createSoftphone(t={}){return new eh(this,{...t,audio:{...this.options.audio,...t.audio}})}mediaURL(t,h){return this.resolveMediaURL(t,"media",h)}listenerMediaURL(t,h){return this.resolveMediaURL(t,"listen-media",h)}resolveMediaURL(t,h,e){let s=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),m=new URL(t.media_url,s),i=`/api/apps/telephony/_install/${this.app.installId}/softphone/${h}/${$(t.call_id)}/`;if(m.origin!==s.origin||!["http:","https:"].includes(m.protocol)||m.username||m.password||m.search||m.hash||!m.pathname.startsWith(i)||!/^[A-Za-z0-9_-]+$/.test(m.pathname.slice(i.length)))throw Error("Invalid Telephony media endpoint");if(h==="listen-media"&&m.pathname.slice(i.length)!==t.session_token)throw Error("Listener credential mismatch");if(h==="media"&&Pt(t.session_generation))m.searchParams.set("session_generation",t.session_generation);let u=this.transport.mediaURL(m,e);return u.protocol=u.protocol==="https:"?"wss:":"ws:",u.href}session(t,h,e="media",s=it()){let m=t;if(!m||typeof m.call_id!=="string"||typeof m.media_url!=="string"||h&&m.call_id!==h||m.session_token!==void 0&&typeof m.session_token!=="string")throw Error("Invalid Telephony session response");if(m.lease_seconds!==void 0&&(!Number.isFinite(m.lease_seconds)||m.lease_seconds<10||m.lease_seconds>3600||!m.session_token))throw Error("Invalid media lease");if(e==="listen-media"&&(!m.session_token||m.lease_seconds===void 0))throw Error("Invalid listener lease");return $(m.call_id),this.resolveMediaURL(m,e),{...m,lease_started_ms:s}}}var Cs=zt({app:"telephony",create:({app:t})=>new Xt(t)});function Es({app:t},h){return new Xt(t,h)}export{Es as createClient,zh as callTerminationLabel};
