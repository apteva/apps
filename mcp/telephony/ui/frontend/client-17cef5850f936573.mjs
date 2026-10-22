function Ch(h){let t=globalThis.crypto?.randomUUID?.()??`${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;return`${h}-${t}`}function xh(h){return{recovery_id:Ch("recovery"),attempt_id:Ch("attempt"),initiating_action:h}}function Fh(h){return typeof h==="string"&&h.length<=80&&/^(browser|session|recovery|attempt)-[a-f0-9-]{1,64}$/.test(h)?h:void 0}function mh(h){let t=(f,i)=>typeof f==="string"&&f.length<=i&&/^[a-zA-Z0-9_-]*$/.test(f)?f:void 0,m=["WebSocket transport error","WebRTC signaling error","Audio worker failed","WebRTC signaling unavailable"],s=["attach","takeover","automatic_retry","manual_reconnect","audio_device_change","component_recreation"],_=(f,i)=>typeof f==="number"&&Number.isFinite(f)?Math.max(0,Math.min(i,f)):void 0;return{timestamp:Number.isFinite(Date.parse(h.timestamp))?new Date(h.timestamp).toISOString():new Date().toISOString(),action:t(h.action,40)??"diagnostic",outcome:t(h.outcome,40)??"unknown",code:t(h.code,80),detail:h.detail?m.includes(h.detail)?h.detail:"detail_redacted":void 0,recovery_id:Fh(h.recovery_id),attempt_id:Fh(h.attempt_id),shared_attempt_id:Fh(h.shared_attempt_id),session_id:Fh(h.session_id),previous_session_id:Fh(h.previous_session_id),initiating_action:s.includes(h.initiating_action)?h.initiating_action:void 0,status:_(h.status,599),remaining_ms:_(h.remaining_ms,3600000),was_clean:typeof h.was_clean==="boolean"?h.was_clean:void 0,duration_ms:_(h.duration_ms,86400000)}}var ah=()=>performance.now();async function Bh(h,t,m=5000,s){let _=new AbortController,u,f=new Promise((a,y)=>{u=y}),i=()=>{_.abort(),u(new DOMException("Media request cancelled","AbortError"))};t?.addEventListener("abort",i,{once:!0});let M=setTimeout(i,m);try{if(t?.aborted)return i(),await f;return await Promise.race([h(_.signal).then((a)=>{if(_.signal.aborted)s?.(a);return a}),f])}finally{clearTimeout(M),t?.removeEventListener("abort",i)}}function th(h){let t=h,m=t?.status,s;try{let u=JSON.parse(t?.body??"{}");if(typeof u.code==="string")s=u.code}catch{}let _=s==="media_lease_expired";return{status:m,code:s,expired:_,denied:!_&&[401,403,404,410].includes(m)}}class Uh{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(h,t,m,s,_=ah(),u={now:ah,setTimeout:(f,i)=>setTimeout(f,i),clearTimeout:(f)=>clearTimeout(f)}){this.seconds=h;this.renew=t;this.ended=m;this.event=s;this.clock=u;this.deadline=_+h*1000-1000,this.watchExpiry(),this.schedule(Math.min(h*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(h,t){let{status:m,code:s}=th(t);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:h,status:m,code:s,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(h){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,h))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let h=this.clock.now();try{let t=await Promise.race([this.renew(),new Promise((m,s)=>{this.requestTimer=this.clock.setTimeout(()=>s(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(t?.lease_seconds!==void 0){if(!Number.isFinite(t.lease_seconds)||t.lease_seconds<10||t.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=t.lease_seconds}this.deadline=h+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(t){if(this.stopped)return;let m=th(t);if(m.denied||m.expired){this.finish(m.expired?"expired":"revoked",t);return}this.report("retrying",t),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(h,t){if(this.stopped)return;this.report(h,t),this.stop(),this.ended(h,t)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function Dh(h){let t=new URL(h),m=["localhost","127.0.0.1","[::1]"].includes(t.hostname);if(t.username||t.password||t.search||t.hash||t.protocol!=="https:"&&!(t.protocol==="http:"&&m)||!t.pathname.endsWith("/runtime-browser"))throw Error("Invalid managed Telephony browser endpoint");return t}function et(h){if(!h.startsWith("/")||h.startsWith("//")||h.includes("..")||h.includes("\\"))throw Error("Invalid Telephony request path")}function Nt(h,t){return new Promise((m)=>{let s=()=>{clearTimeout(_),h.removeEventListener("abort",s),m()},_=setTimeout(s,t);if(h.addEventListener("abort",s,{once:!0}),h.aborted)s()})}class rh{proxy;mode;fetchImpl;direct;info;discovery;credential;issuing;proxyOnly=!1;constructor(h,t="auto",m=globalThis.fetch,s){this.proxy=h;this.mode=t;this.fetchImpl=m;this.direct=s;this.fetchImpl=m.bind(globalThis)}get directBase(){return this.info?.browser_api_base}async prepare(){if(this.mode==="proxy"||this.mode==="auto"&&typeof location>"u")return;if(this.direct){Dh(this.direct.baseURL),this.info={managed:!0,browser_api_base:this.direct.baseURL};return}if(this.discovery)return this.discovery;this.discovery=(async()=>{let h;try{h=await this.proxy.get("/runtime-endpoints")}catch(m){if(this.mode==="direct"||![404,405].includes(m.status??0))throw m;this.proxyOnly=!0;return}if(!h?.managed||!h.browser_api_base){if(this.proxyOnly=!0,this.mode==="direct")throw Error("Direct Telephony deployment unavailable");return}if(Dh(h.browser_api_base),h.browser_media_base)Dh(h.browser_media_base);if(h.browser_signaling_base)Dh(h.browser_signaling_base);let t=new URL(this.proxy.mcpURL(),typeof location>"u"?void 0:location.href);if(!["https:","http:"].includes(t.protocol))throw Error("Invalid Telephony management gateway");this.info=h})();try{await this.discovery}catch(h){throw this.discovery=void 0,h}}async grant(){if(this.credential&&this.credential.expires_at*1000>Date.now()+30000)return this.credential;if(this.issuing)return this.issuing;let h=typeof location>"u"?new URL(this.proxy.mcpURL()).origin:location.origin;this.issuing=(async()=>{let t=this.direct?await this.direct.credentialProvider():await this.proxy.post("/runtime-browser-bootstrap",{origin:h});if(!t||typeof t.token!=="string"||!t.token||t.token.length>4096||!Number.isSafeInteger(t.expires_at)||t.expires_at*1000<=Date.now()||t.expires_at*1000>Date.now()+360000||t.endpoint!==this.directBase)throw Error("Invalid managed browser credential");return this.credential=t})();try{return await this.issuing}finally{this.issuing=void 0}}async request(h,t,m,s){if(this.mode==="proxy"||this.proxyOnly||this.mode==="auto"&&typeof location>"u")return h==="GET"?this.proxy.get(t,s):this.proxy.post(t,m,s);if(await this.prepare(),!this.directBase)return h==="GET"?this.proxy.get(t,s):this.proxy.post(t,m,s);et(t);let _=await this.grant(),u=new URL(this.directBase+t);if(this.proxy.projectId)u.searchParams.set("project_id",this.proxy.projectId);let f=new Headers(s?.headers);f.set("Authorization","Bearer "+_.token),f.set("Content-Type","application/json");let i=await this.fetchImpl(u,{...s,method:h,headers:f,body:m===void 0?void 0:JSON.stringify(m),credentials:"omit",redirect:"error"});if(!i.ok){if(i.status===403)this.credential=void 0;let M=(await i.text()).slice(0,65536);throw Object.assign(Error(`Telephony HTTP ${i.status}`),{status:i.status,body:M})}return await i.json()}mediaURL(h,t){if(!this.directBase)return h;let m=`/api/apps/telephony/_install/${this.proxy.installId}`,s=t==="webrtc"||t==="auto"?this.info?.browser_signaling_base:this.info?.browser_media_base,_=new URL((s||this.directBase)+h.pathname.slice(m.length));return _.search=h.search,_}subscribe(h,t,m){let s=new AbortController,_,u=()=>{s.abort(),_?.close(),m?.signal?.removeEventListener("abort",u)};if(m?.signal?.addEventListener("abort",u,{once:!0}),m?.signal?.aborted)u();return(async()=>{try{if(await this.prepare(),s.signal.aborted)return;if(!this.directBase){_=this.proxy.subscribe(h,t,m);return}et(h);let f=500;while(!s.signal.aborted){try{let i=await this.grant(),M=new URL(this.directBase+h);if(this.proxy.projectId)M.searchParams.set("project_id",this.proxy.projectId);let a=await this.fetchImpl(M,{headers:{Authorization:"Bearer "+i.token,Accept:"text/event-stream"},credentials:"omit",redirect:"error",signal:s.signal});if(!a.ok||!a.body){if(a.status===403)this.credential=void 0;throw Object.assign(Error(`Telephony stream HTTP ${a.status}`),{status:a.status})}f=500;let y=a.body.getReader(),S=new TextDecoder,g="";try{while(!s.signal.aborted){let{done:A,value:b}=await y.read();if(A)break;if(g+=S.decode(b,{stream:!0}),g=g.replace(/\r\n/g,`
`),g.length>65536)throw Error("Telephony notification frame too large");let w;while((w=g.indexOf(`

`))>=0){let C=g.slice(0,w);g=g.slice(w+2);let P=C.split(`
`).filter((q)=>q.startsWith("data:")).map((q)=>q.slice(5).trimStart()).join(`
`);if(P)try{t(JSON.parse(P))}catch{}}}}finally{await y.cancel().catch(()=>{})}}catch(i){if(s.signal.aborted)return;m?.onError?.(i),f=Math.min(1e4,f*2)}if(!s.signal.aborted)await Nt(s.signal,f)}}catch(f){if(!s.signal.aborted)m?.onError?.(f)}})(),{close:u}}}function nh(h){return h}function vh(h){if(h.turnTransport!==void 0&&!["all","udp","tcp","tls"].includes(h.turnTransport))throw RangeError("Invalid TURN transport");if(h.iceTransportPolicy!==void 0&&!["all","relay"].includes(h.iceTransportPolicy))throw RangeError("Invalid ICE policy");if(h.turnTLSPort!==void 0&&(!Number.isInteger(h.turnTLSPort)||h.turnTLSPort<1||h.turnTLSPort>65535||h.turnTransport!=="tls"))throw RangeError("TURN TLS port requires TLS transport and a valid port")}function Wh(h){let t=h.ice_transport_policy??"all";if(t!=="all"&&t!=="relay")throw Error("Invalid server ICE policy");let m=h.ice_servers??[];if(!Array.isArray(m)||m.length>8)throw Error("Invalid server ICE configuration");for(let s of m){let _=typeof s.urls==="string"?[s.urls]:s.urls;if(!Array.isArray(_)||_.length<1||_.length>16||_.some((u)=>typeof u!=="string"||u.length>1024||/[\r\n@#]/.test(u)||!/^stuns?:|^turns?:/.test(u)))throw Error("Invalid server ICE endpoint")}if(t==="relay"&&!m.some((s)=>(typeof s.urls==="string"?[s.urls]:s.urls).some((_)=>/^turns?:/.test(_))))throw Object.assign(Error("Required TURN relay is unavailable"),{code:"turn_relay_unavailable"});return{iceServers:m,iceTransportPolicy:t}}function Pt(h,t){vh(t);for(let m of["turn_transport","ice_policy","turn_tls_port"])h.searchParams.delete(m);if(t.turnTransport!==void 0)h.searchParams.set("turn_transport",t.turnTransport);if(t.iceTransportPolicy!==void 0)h.searchParams.set("ice_policy",t.iceTransportPolicy);if(t.turnTLSPort!==void 0)h.searchParams.set("turn_tls_port",String(t.turnTLSPort))}class Nh{send;active;observe;configured;work=Promise.resolve();peers=new Map;revision=1;closed=!1;retryTimer;retries=0;constructor(h,t,m=()=>{},s){this.send=h;this.active=t;this.observe=m;this.configured=s}register(h,t,m=()=>!0,s){this.remove(h);let _=h==="coach";this.peers.set(h,{pc:t,current:m,revision:0,generation:s,cancellation:new AbortController,offerType:_?"coach.webrtc.restart_offer":"webrtc.restart_offer",answerType:_?"coach.webrtc.restart_answer":"webrtc.restart_answer"})}remove(h){let t=this.peers.get(h);if(t?.cancellation.abort(),t?.pending)clearTimeout(t.pending.timer),t.pending.reject(Error("ICE peer replaced"));this.peers.delete(h)}report(h,t){try{this.observe(h,t)}catch{}}alive(h){return!this.closed&&this.active()&&(!h||h.current()&&h.pc.connectionState!=="closed")}handle(h){if(!["webrtc.ice_config","webrtc.restart_answer","coach.webrtc.restart_answer","webrtc.ice_status"].includes(h.type))return!1;if(!this.alive())return!0;if(h.type==="webrtc.ice_status"){if(this.report(["credential_refresh_unavailable","restart_failed"].includes(h.state)?h.state:"ice_status"),h.state==="restart_failed"){for(let m of this.peers.values())if(m.generation===h.generation&&m.pending&&m.pending.revision===h.revision)clearTimeout(m.pending.timer),m.pending.reject(Error("ICE restart rejected")),m.pending=void 0}return!0}if(h.type.endsWith("restart_answer")){for(let m of this.peers.values()){if(h.type!==m.answerType||m.generation!==h.generation||m.pending?.revision!==h.revision||!this.alive(m))continue;let s=m.pending;if(!s)continue;m.pc.setRemoteDescription({type:"answer",sdp:h.sdp}).then(()=>{if(m.pending!==s)return;if(clearTimeout(s.timer),m.pending=void 0,this.alive(m))s.resolve();else s.reject(Error("ICE session replaced"))}).catch(()=>{if(m.pending===s)clearTimeout(s.timer),m.pending=void 0,s.reject(Error("ICE answer rejected"))})}return!0}if(!Number.isSafeInteger(h.revision)||h.revision<=this.revision)return!0;let t;try{if(t=Wh(h),!Number.isFinite(h.expires_at)||h.expires_at*1000<=Date.now()+1000)throw Error()}catch{return this.report("invalid_ice_refresh"),!0}this.revision=h.revision;try{this.configured?.(t)}catch{}return this.report("credentials_refreshed",{code:`ice_rev_${h.revision}`,remaining_ms:Math.max(0,Math.min(3600000,Math.round(h.expires_at*1000-Date.now())))}),this.work=this.work.then(async()=>{if(!this.alive()||this.revision!==h.revision)return;let m=await Promise.allSettled([...this.peers.values()].filter((s)=>this.alive(s)).map((s)=>this.restart(s,t,h.revision)));if(!this.alive()||this.revision!==h.revision)return;if(m.some((s)=>s.status==="rejected"))this.retry();else{if(this.retries=0,this.retryTimer)clearTimeout(this.retryTimer);this.retryTimer=void 0}}).catch(()=>{this.report("restart_failed"),this.retry()}),!0}retry(){if(this.retryTimer||this.retries>=3||!this.alive())return;this.retryTimer=setTimeout(()=>{if(this.retryTimer=void 0,this.alive()){this.retries++;try{this.send({type:"webrtc.ice_refresh"})}catch{this.report("refresh_request_failed")}}},15000)}async restart(h,t,m){if(h.pending)clearTimeout(h.pending.timer),h.pending.reject(Error("ICE revision superseded")),h.pending=void 0;let s=()=>this.alive(h)&&this.peersHas(h);if(this.revision!==m||!s())return;let _=Ch("attempt"),u=performance.now(),f=()=>({attempt_id:_,code:`ice_rev_${m}`,duration_ms:Math.round(performance.now()-u)});try{if(await new Promise((M,a)=>{if(h.pc.signalingState==="stable"){M();return}let y=()=>{clearTimeout(A),h.pc.removeEventListener("signalingstatechange",S),h.cancellation.signal.removeEventListener("abort",g)},S=()=>{if(!s()||h.pc.signalingState==="stable")y(),s()?M():a(Error("ICE peer replaced"))},g=()=>{y(),a(Error("ICE peer removed"))},A=setTimeout(()=>{y(),a(Error("ICE signaling busy"))},1e4);h.pc.addEventListener("signalingstatechange",S),h.cancellation.signal.addEventListener("abort",g,{once:!0}),S()}),!s())return;h.pc.setConfiguration({...h.pc.getConfiguration(),...t});let i=await h.pc.createOffer({iceRestart:!0});if(!s())return;if(await h.pc.setLocalDescription(i),!s())return;if(await new Promise((M,a)=>{if(h.pc.iceGatheringState==="complete"){M();return}let y=()=>{clearTimeout(A),h.pc.removeEventListener("icegatheringstatechange",S),h.cancellation.signal.removeEventListener("abort",g)},S=()=>{if(!s()||h.pc.iceGatheringState==="complete")y(),s()?M():a(Error("ICE session replaced"))},g=()=>{y(),a(Error("ICE session stopped"))},A=setTimeout(()=>{y(),a(Error("ICE gathering timed out"))},7000);h.cancellation.signal.addEventListener("abort",g,{once:!0}),h.pc.addEventListener("icegatheringstatechange",S),S()}),!s())return;if(this.report("restart_started",f()),await new Promise((M,a)=>{let y=setTimeout(()=>{if(h.pending?.revision===m)h.pending=void 0;a(Error("ICE restart timed out"))},1e4);h.pending={revision:m,resolve:M,reject:a,timer:y};try{this.send({type:h.offerType,revision:m,generation:h.generation,sdp:h.pc.localDescription.sdp})}catch{clearTimeout(y),h.pending=void 0,a(Error("ICE signaling unavailable"))}}),s())this.report("restart_completed",f())}catch(i){if(s()&&h.pc.signalingState==="have-local-offer")try{await h.pc.setLocalDescription({type:"rollback"})}catch{}if(s())this.report("restart_failed",f());throw i}}peersHas(h){return[...this.peers.values()].includes(h)}stop(){if(this.closed)return;if(this.closed=!0,this.retryTimer)clearTimeout(this.retryTimer);this.retryTimer=void 0;for(let h of[...this.peers.keys()])this.remove(h)}}var Th="0.12.30";var Ht=["rtt_ms","queue_ms","target_ms","buffered_bytes","underruns","dropped_ms","capture_sequence_gaps","playback_sequence_gaps","main_thread_max_pause_ms","worker_max_tick_gap_ms","capture_sent_ms","capture_muted_ms","capture_dropped_ms","playback_ingress_ms","playback_transport_dropped_ms","playback_source_dropped_ms","clock_uncertainty_ms","clock_sample_age_ms","stats_errors","stats_duration_ms","send_bitrate_bps","receive_bitrate_bps","receive_gap_ms","capture_age_ms","transit_ms","delivery_excess_ms","server_queue_ms","receiver_concealed_delta_ms","receiver_silent_concealed_delta_ms","receiver_concealed_window_ms","requested_playback_target_ms","requested_upload_bitrate_bps","receiver_ssrc","receiver_loss_delta","receiver_loss_window_ms","remote_receiver_ssrc","remote_receiver_loss_delta","remote_receiver_loss_window_ms","receiver_bytesReceived","receiver_packetsReceived","receiver_packetsLost","receiver_jitter","receiver_packetsDiscarded","receiver_concealedSamples","receiver_silentConcealedSamples","receiver_concealmentEvents","receiver_insertedSamplesForDeceleration","receiver_removedSamplesForAcceleration","receiver_totalSamplesReceived","receiver_audioLevel","receiver_totalAudioEnergy","receiver_totalSamplesDuration","receiver_nackCount","receiver_fecPacketsReceived","receiver_fecPacketsDiscarded","receiver_jitter_buffer_interval_ms","receiver_jitter_target_interval_ms","receiver_jitter_minimum_interval_ms","receiver_processing_interval_ms","receiver_totalProcessingDelay","receiver_totalSamplesDecoded","telemetry_skipped_samples","telemetry_skipped_auxiliary","telemetry_queue_age_ms","receiver_last_packet_age_ms","sender_bytesSent","sender_packetsSent","sender_headerBytesSent","sender_retransmittedPacketsSent","sender_retransmittedBytesSent","sender_nackCount","sender_targetBitrate","sender_send_delay_interval_ms","remote_receiver_packetsLost","remote_receiver_fractionLost","remote_receiver_jitter","remote_receiver_roundTripTime","remote_receiver_totalRoundTripTime","remote_receiver_roundTripTimeMeasurements","remote_sender_packetsSent","remote_sender_bytesSent","pair_availableOutgoingBitrate","pair_availableIncomingBitrate","pair_currentRoundTripTime","pair_totalRoundTripTime","pair_bytesSent","pair_bytesReceived","pair_requestsSent","pair_requestsReceived","pair_responsesSent","pair_responsesReceived","pair_consentRequestsSent","path_revision","codec_clock_rate","codec_channels"],Vt={receiver_processing:["unavailable","per_decoded_sample"],connection:["connected","reconnecting","closed"],context:["running","suspended","interrupted","closed"],muted:["true","false"],device_muted:["true","false"],track:["live","ended"],ice:["new","checking","connected","completed","disconnected","failed","closed"],dtls:["new","connecting","connected","closed","failed"],pair:["frozen","waiting","in-progress","failed","succeeded"],local_candidate:["host","srflx","prflx","relay"],remote_candidate:["host","srflx","prflx","relay"],protocol:["udp","tcp"],relay_protocol:["udp","tcp","tls"],codec:["audio/opus","audio/PCMU","audio/PCMA","pcm16"]};function oh(h){let t={};for(let m of Ht)if(typeof h[m]==="number"&&Number.isFinite(h[m]))t[m]=Math.max(0,Math.min(h[m],1000000000000));return t}function ht(h){let t={};for(let[m,s]of Object.entries(Vt))if(typeof h[m]==="string"&&s.includes(h[m]))t[m]=h[m];return t}class Hh{transport;serial=0;lastEmit=-1/0;previousAt;previous;incident;history=[];pending=[];counters={};constructor(h){this.transport=h}observe(h,t,m=performance.now(),s=new Date().toISOString(),_){if(!Number.isFinite(m)||!Number.isFinite(Date.parse(s)))return;if(this.previousAt!==void 0&&m<this.previousAt)this.lastEmit=-1/0,this.previous=void 0,this.counters={},this.incident=void 0;let u=oh(h),f=ht(t),i=!1;for(let a of["underruns","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealmentEvents","path_revision","stats_errors"])if(u[a]!==void 0){if(this.counters[a]!==void 0&&u[a]>this.counters[a])i=!0;this.counters[a]=u[a]}let M={id:`s${++this.serial}`,timestamp:s,transport:this.transport,reason:i?"incident":"periodic",window_ms:Math.min(60000,Math.max(0,typeof _==="number"&&Number.isFinite(_)?_:this.previousAt===void 0?0:m-this.previousAt)),metrics:u,states:f};if(this.previousAt=m,i&&!this.incident)this.incident={sample:M,context:this.previous};if(m-this.lastEmit>=5000){if(this.incident){if(this.incident.context)this.enqueue({...this.incident.context,reason:"incident_context"});this.enqueue(this.incident.sample),this.incident=void 0}else this.enqueue(M);this.lastEmit=m}this.previous=M}enqueue(h){if(this.history.push(h),this.history.length>24)this.history.shift();if(this.pending.push(h),this.pending.length>8)this.pending.shift()}recent(){return this.history.map((h)=>({...h,metrics:{...h.metrics},states:{...h.states}}))}drain(){return this.pending.splice(0,2)}}function wt(h){let t={...h,metrics:{},states:{},part_index:0,part_count:16},m=[],s=t;for(let _ of["metrics","states"])for(let[u,f]of Object.entries(h[_])){let i={...s,[_]:{...s[_],[u]:f}};if(JSON.stringify(i).length>768)m.push(s),s={...t,metrics:{},states:{},[_]:{[u]:f}};else s=i}if(m.push(s),m.length>16)return[];return m.map((_,u)=>({..._,part_index:u,part_count:m.length}))}var $t=["receiver_loss_delta","remote_receiver_loss_delta","receiver_concealed_delta_ms","receiver_ssrc","remote_receiver_ssrc","rtt_ms","queue_ms","target_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealedSamples","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_jitter","buffered_bytes","main_thread_max_pause_ms","worker_max_tick_gap_ms","receiver_processing_interval_ms","path_revision","stats_errors"];function Xt(h){let t={};for(let m of $t)if(h.metrics[m]!==void 0){if(t[m]=h.metrics[m],wt({...h,metrics:t}).length>2)delete t[m]}return{...h,metrics:t}}class Eh{send;sendReport;now;auxiliary=[];skippedAuxiliary=0;skippedSamples=0;auxiliaryTurn=!1;pending=[];queuedAt=new Map;lastFull=-1/0;timer;report;reportAt=0;static maxAgeMS=12000;constructor(h,t,m=()=>performance.now()){this.send=h;this.sendReport=t;this.now=m}enqueueReport(h){if(this.report!==void 0)this.skippedAuxiliary++;this.report=h,this.reportAt=this.now(),this.start()}enqueueAuxiliary(h){let t=!!h?.diagnostics?.timing?.runtime;if(t){let m=this.auxiliary.length;this.auxiliary=this.auxiliary.filter((s)=>!s.runtime),this.skippedAuxiliary+=m-this.auxiliary.length}if(this.auxiliary.push({report:h,at:this.now(),runtime:t}),this.auxiliary.length>16)this.auxiliary.shift(),this.skippedAuxiliary++;this.start()}start(){if(!this.timer)this.timer=setInterval(()=>this.tick(),250)}discard(h){this.pending=this.pending.filter((t)=>t.id!==h),this.queuedAt.delete(h),this.skippedSamples++}enqueue(h){let t=this.now(),m=[...h].reverse().find((s)=>s.reason==="incident")??h.at(-1);if(this.skippedSamples+=Math.max(0,h.length-(m?1:0)),m){let s=m.reason==="periodic"&&t-this.lastFull>=30000;if(s)this.lastFull=t;let _=s?m:Xt(m);_={..._,metrics:{..._.metrics,telemetry_skipped_samples:this.skippedSamples,telemetry_skipped_auxiliary:this.skippedAuxiliary}};let u=wt(_),f=this.pending[0]?.id;for(let i of this.queuedAt.keys())if(i!==f)this.discard(i);if(this.queuedAt.has(_.id))this.discard(_.id);if(u.length)this.pending.push(...u),this.queuedAt.set(_.id,t)}if(this.pending.length)this.start()}tick(){let h=this.now();for(let[t,m]of this.queuedAt)if(h-m>Eh.maxAgeMS)this.discard(t);if(this.auxiliary=this.auxiliary.filter((t)=>{if(h-t.at>Eh.maxAgeMS)return this.skippedAuxiliary++,!1;return!0}),this.report!==void 0&&h-this.reportAt>Eh.maxAgeMS)this.report=void 0,this.skippedAuxiliary++;try{if(this.report!==void 0&&this.sendReport){if(this.sendReport(this.report,Math.max(0,h-this.reportAt)))this.report=void 0}else if(this.auxiliary.length&&this.sendReport&&(!this.pending.length||this.auxiliaryTurn)){if(this.sendReport(this.auxiliary[0].report,Math.max(0,h-this.auxiliary[0].at)))this.auxiliary.shift(),this.auxiliaryTurn=!1}else if(this.pending.length){let t=this.pending[0],m={...t.metrics,telemetry_queue_age_ms:Math.max(0,h-(this.queuedAt.get(t.id)??h))};if(this.send({...t,metrics:m})){if(this.pending.shift(),t.part_index===t.part_count-1)this.queuedAt.delete(t.id);this.auxiliaryTurn=!0}}}catch{}if(!this.pending.length&&!this.auxiliary.length&&this.report===void 0)clearInterval(this.timer),this.timer=void 0}stop(){clearInterval(this.timer),this.timer=void 0,this.pending=[],this.queuedAt.clear(),this.auxiliary=[],this.report=void 0}}class Vh{emit;now;timestamp;lastTick;suspendedAt;contextState;environmentCleanup=[];counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(h,t=()=>performance.now(),m=()=>new Date().toISOString()){this.emit=h;this.now=t;this.timestamp=m}observeEnvironment(){this.stopEnvironment();let h=(t,m,s)=>{if(!t)return;try{t.addEventListener(m,s),this.environmentCleanup.push(()=>t.removeEventListener(m,s))}catch{}};try{if(typeof document<"u"){h(document,"visibilitychange",()=>this.report("tab_visibility",document.visibilityState));for(let t of["freeze","resume"])h(document,t,()=>this.report("page_lifecycle",t));this.report("tab_visibility",document.visibilityState)}if(typeof window<"u")for(let t of["pagehide","pageshow"])h(window,t,()=>this.report("page_lifecycle",t));if(typeof navigator<"u")h(navigator.mediaDevices,"devicechange",()=>this.report("microphone","devices_changed"))}catch{}}stopEnvironment(){for(let h of this.environmentCleanup.splice(0))try{h()}catch{}}tick(){let h=this.now();if(this.lastTick!==void 0){let t=Math.max(0,h-this.lastTick-1000);if(t>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,t),this.report("main_thread","scheduling_gap",t)}this.lastTick=h}context(h){if(h===this.contextState)return;this.contextState=h;let t=this.now();if(h==="suspended"||h==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=t,this.counters.audio_context_suspend_count++;this.report("audio_context",h)}else{let m;if(this.suspendedAt!==void 0)m=Math.max(0,t-this.suspendedAt),this.counters.audio_context_suspended_ms+=m,this.suspendedAt=void 0;this.report("audio_context",h,m)}}report(h,t,m){try{this.emit({timestamp:this.timestamp(),action:h,outcome:t,duration_ms:m===void 0?void 0:Math.round(m)})}catch{}}}async function c(h,t,m){let s=performance.now(),_=(u)=>{try{t?.({timestamp:new Date().toISOString(),action:h,outcome:u,duration_ms:Math.round(Math.max(0,performance.now()-s))})}catch{}};_("started");try{let u=await m();return _("completed"),u}catch(u){throw _("failed"),u}}var bt=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function Kh(h){let t=(h??"FR").trim().toUpperCase();return bt[t]??bt.FR}function Yt(h,t){let m=[];if(!(h.cadence.reduce((u,f)=>u+f,0)>0)||!(t>0))return m;let _=0;while(_<t)for(let u=0;u<h.cadence.length;u+=2){let f=h.cadence[u]??0,i=h.cadence[u+1]??0;if(_>=t)break;if(f>0)m.push({start:_,end:Math.min(_+f,t)});_+=f+i}return m}function kh(h,t,m,s=0.12){let _=h.createGain();_.gain.value=0,_.connect(t);let u=s/Math.max(1,m.frequencies.length),f=m.frequencies.map((A)=>{let b=h.createOscillator();return b.type="sine",b.frequency.value=A,b.connect(_),b.start(),b}),i=h.currentTime+0.05,M=0,a=0.01,y=()=>{let A=h.currentTime-i+10;if(A<=M)return;for(let b of Yt(m,A)){if(b.end<=M)continue;let w=i+Math.max(b.start,M),C=i+b.end;_.gain.setValueAtTime(0,w),_.gain.linearRampToValueAtTime(u,w+a),_.gain.setValueAtTime(u,Math.max(w+a,C-a)),_.gain.linearRampToValueAtTime(0,C)}M=A};y();let S=setInterval(y,4000),g=!1;return()=>{if(g)return;g=!0,clearInterval(S);try{_.gain.cancelScheduledValues(0)}catch{}_.gain.value=0;for(let A of f){try{A.stop()}catch{}A.disconnect()}_.disconnect()}}var yh=24000,$h=60;async function Ct(h,t){try{await h.audioWorklet.addModule(t)}catch(m){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:m})}}function Lt(h){let t=new Int16Array(h.length);for(let m=0;m<h.length;m++){let s=Math.max(-1,Math.min(1,h[m]));t[m]=s<0?s*32768:s*32767}return t.buffer}class Ph{history=new Float32Array(64);phase=0;process(h,t,m){if(t===m)return h;let s=new Float32Array(64+h.length);s.set(this.history),s.set(h,64);let _=t/m,u=Math.min(1,m/t)*0.9,f=[],i=this.phase;for(;i<h.length;i+=_){let M=i+32,a=0,y=0;for(let S=Math.ceil(M-32);S<=Math.floor(M+32);S++){let g=S-M,b=(Math.abs(g)<0.000000001?u:Math.sin(Math.PI*u*g)/(Math.PI*g))*(0.5+0.5*Math.cos(Math.PI*g/32));if(S>=0&&S<s.length)a+=s[S]*b,y+=b}f.push(y?a/y:0)}return this.phase=i-h.length,this.history=s.slice(-64),new Float32Array(f)}}function zt(h){let t=0;for(let m=0;m<h.length;m++)t+=h[m]*h[m];return Math.sqrt(t/Math.max(1,h.length))}function At(h,t){let m=new Map(h.map((s)=>[s.id,s]));for(let s of t){let _=m.get(s.id);if(!_||(!_.complete||s.complete)&&(!_.ended_at||!!s.ended_at)&&s.duration_ms>=_.duration_ms)m.set(s.id,{...s})}return[...m.values()].sort((s,_)=>s.started_at.localeCompare(_.started_at)).slice(-100)}function Jt(h,t){return h.map((m)=>m.ended_at?m:{...m,ended_at:m.observed_until,end_reason:t,complete:!1})}var sh={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function v(h){if(h.mediaTransport!==void 0&&!["websocket","webrtc","auto"].includes(h.mediaTransport))throw RangeError("Unsupported softphone media transport");if(vh(h),h.webrtcFec!==void 0&&typeof h.webrtcFec!=="boolean")throw RangeError("webrtcFec must be boolean");let t=h.playbackTargetMs??$h,m=h.playbackMinMs??Math.min($h,t),s=h.playbackMaxMs??280;for(let _ of[t,m,s])if(!Number.isFinite(_)||_<40||_>280)throw RangeError("Playback buffers must be between 40 and 280 ms");if(m>t||t>s)throw RangeError("Playback buffers require min <= target <= max");if(h.playbackAdaptive!==void 0&&typeof h.playbackAdaptive!=="boolean")throw RangeError("playbackAdaptive must be boolean");return{initialTargetMs:t,minTargetMs:m,maxTargetMs:s,hardMaxMs:320,adaptiveReserve:h.playbackAdaptive!==!1}}function Sh(h){return{...h.inputDeviceId?{deviceId:{exact:h.inputDeviceId}}:{},echoCancellation:h.echoCancellation,noiseSuppression:h.noiseSuppression,autoGainControl:h.autoGainControl}}function xt(h){let t=h.getSettings();return{deviceLabel:h.label||"Default microphone",sampleRate:typeof t.sampleRate==="number"?t.sampleRate:null,channelCount:typeof t.channelCount==="number"?t.channelCount:null,echoCancellation:typeof t.echoCancellation==="boolean"?t.echoCancellation:null,noiseSuppression:typeof t.noiseSuppression==="boolean"?t.noiseSuppression:null,autoGainControl:typeof t.autoGainControl==="boolean"?t.autoGainControl:null}}function Oh(h){if(!Number.isFinite(h)||h<=0)return null;return 20*Math.log10(h)}function Zt(h,t,m){let s=new ArrayBuffer(44+t*2),_=new DataView(s),u=(i,M)=>{for(let a=0;a<M.length;a++)_.setUint8(i+a,M.charCodeAt(a))};u(0,"RIFF"),_.setUint32(4,36+t*2,!0),u(8,"WAVE"),u(12,"fmt "),_.setUint32(16,16,!0),_.setUint16(20,1,!0),_.setUint16(22,1,!0),_.setUint32(24,m,!0),_.setUint32(28,m*2,!0),_.setUint16(32,2,!0),_.setUint16(34,16,!0),u(36,"data"),_.setUint32(40,t*2,!0);let f=44;for(let i of h)for(let M=0;M<i.length;M++)_.setInt16(f,i[M],!0),f+=2;return new Blob([s],{type:"audio/wav"})}class tt{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new Ph;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(h,t=!0){this.onLevel=h;this.recordAudio=t}async start(h,t){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:Sh(t)}),this.ensureOpen();let m=this.stream.getAudioTracks()[0];if(!m)throw Error("No microphone audio track was returned.");this.settings=xt(m);try{this.ctx=new AudioContext({sampleRate:yh,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await Ct(this.ctx,h),this.ensureOpen();let s=this.ctx.sampleRate,_=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:t.inputGainDB,highpassFilter:t.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(u)=>{if(this.stopped)return;if(!(u.data instanceof Float32Array)){if(u.data.type==="capture.stats")this.peak=Math.max(this.peak,u.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,u.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,u.data.limiter_reduction_db??0);return}let f=u.data;if(s!==yh)f=this.resampler.process(f,s,yh);let i=zt(f);if(this.onLevel?.(i),this.recordAudio)this.frames.push(new Int16Array(Lt(f)));if(this.samples+=f.length,i>=0.005){for(let M=0;M<f.length;M++)this.activeSquares+=f[M]*f[M];this.activeSamples+=f.length}},_.connect(this.capture),this.settings}catch(m){throw await this.release(),m}}async stop(){this.stopped=!0;let h=this.settings,t=this.frames,m=this.samples,s=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,_=this.peak;if(await this.release(),!h||m===0)throw Error("No microphone audio was captured.");return{audio:Zt(t,m,yh),durationMs:Math.round(m*1000/yh),sampleRate:yh,activeRmsDbfs:Oh(s),peakDbfs:Oh(_),postPeakDbfs:Oh(this.postPeak||_),limiterReductionDb:this.limiterReductionDB,settings:h}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let h of this.stream?.getTracks()??[])h.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class mt{callbacks;clientEpoch=crypto.randomUUID();transportTelemetry=new Hh("websocket");transportSender=new Eh((h)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[h]}})}),!0},(h)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify(h)}),!0});worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new Vh((h)=>this.recordSessionEvent(h));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;sessionReady=!1;mediaPathReady;mediaRevision=0;callAnswered=!1;playbackHeld=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={mediaTransport:"websocket",codec:"pcm16",rttMs:null,queueMs:0,targetMs:$h,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:yh,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:sh.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(h={}){this.callbacks=h}get isMuted(){return this.muted}async start(h,t,m,s=sh){let _=v(s);this.diagnostics.targetMs=_.initialTargetMs,this.callbacks.onState?.("connecting"),this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+Th.replaceAll(".","_")}),this.runtimeTelemetry.observeEnvironment(),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{this.stream=await c("microphone_device",(M)=>this.recordSessionEvent(M),()=>navigator.mediaDevices.getUserMedia({audio:Sh(s)})),this.ensureOpen();let u=this.stream.getAudioTracks()[0];if(!u)throw Error("No microphone audio track was returned.");if(u.readyState==="ended")throw Error("Microphone disconnected before audio setup.");u.onmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser.")},u.onunmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),this.callbacks.onNotice?.("Microphone input restored.")},u.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let f=xt(u);this.diagnostics={...this.diagnostics,microphoneSampleRate:f.sampleRate??0,microphoneChannelCount:f.channelCount??0,echoCancellation:f.echoCancellation,noiseSuppression:f.noiseSuppression,autoGainControl:f.autoGainControl,micInputGainDb:s.inputGainDB};try{this.ctx=new AudioContext({sampleRate:yh,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await c("audio_context",(M)=>this.recordSessionEvent(M),()=>this.ctx.resume());if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),s.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(s.outputDeviceId),this.ensureOpen();await c("audio_worklet",(M)=>this.recordSessionEvent(M),()=>Ct(this.ctx,t)),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let i=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:{..._,telemetryEpoch:this.clientEpoch,telemetryEnabled:!1}}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),i.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=s.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await c("audio_transport",(M)=>this.recordSessionEvent(M),()=>this.openWorker(h,m)),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.setPlaybackObservation(this.ctx?.state==="running"&&this.microphoneTransportReady&&!this.playbackHeld,"audio_context_paused"),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running")this.updateLiveState()},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(u){if(this.closed)this.teardown();if(!this.closed)this.fail(u instanceof Error?u.message:"browser audio setup failed");throw u}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){await this.ctx?.resume(),this.updateLiveState()}setOutputVolume(h){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,h))}sendDTMF(h){if(/^[0-9*#]+$/.test(h))this.sendText(JSON.stringify({type:"dtmf",digits:h}))}startRingback(h){if(this.closed||!this.ctx||this.ringback)return;this.ringback=kh(this.ctx,this.ctx.destination,Kh(h))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(h)=>{let t=h.data;if(t?.type!=="capture.stats")return;this.micLevel=this.muted?0:t.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:Oh(t.active_rms??0),micPeakDbfs:Oh(t.pre_peak??0),micPostPeakDbfs:Oh(t.post_peak??0),micInputGainDb:t.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:t.limiter_reduction_db??0}},this.playback.port.onmessage=(h)=>{let t=h.data;if(t?.type==="playback.underrun"){this.diagnostics.underrunEvents=At(this.diagnostics.underrunEvents??[],[t.event]),this.diagnostics.underrunDurationMs=t.underrun_ms,this.diagnostics.underruns=t.underruns;return}if(t?.type!=="stats")return;this.diagnostics.playbackEvents=t.playback_events,this.playbackTiming={reserve_expanded_ms:t.reserve_expanded_ms,reserve_compressed_ms:t.reserve_compressed_ms,reserve_adjustments:t.reserve_adjustments,reserve_match_rejections:t.reserve_match_rejections,played_ms:t.played_ms,max_residence_ms:t.max_residence_ms,drop_totals_ms:t.drop_totals_ms,coaching:{played_ms:t.whisper_played_ms,dropped_ms:t.whisper_dropped_ms,max_queue_ms:t.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,t.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:t.whisper_played_ms??0,coachingDroppedMs:t.whisper_dropped_ms??0,coachingMaxQueueMs:t.whisper_max_queue_ms??0,queueMs:t.queue_ms??0,targetMs:t.target_ms??$h,underruns:t.underruns??0,droppedMs:t.dropped_ms??0,underrunDurationMs:t.underrun_ms??0,underrunEvents:At(this.diagnostics.underrunEvents??[],t.underrun_events??[]),maxQueueMs:t.max_queue_ms??0,playbackSequenceGaps:t.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,t.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(h,t){return new Promise((m,s)=>{let _=new Worker(t);this.worker=_;let u=new MessageChannel,f=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:u.port1},[u.port1]),this.playback?.port.postMessage({type:"transport",port:f.port1},[f.port1]);let i=!1,M=setTimeout(()=>{if(i)return;i=!0,s(Error("audio connection timed out"))},1e4),a=(y)=>{if(i)return;if(i=!0,clearTimeout(M),y)s(y);else m()};this.cancelWorkerStart=()=>a(Error("audio session closed")),_.onmessage=(y)=>{if(this.closed){a(Error("audio session closed"));return}let S=y.data;if(S?.type==="socket.open")this.reportedPlaybackIDs.clear(),this.reportedCaptureQueueIDs.clear(),this.opened=!0,this.mediaSocketConnected=!0,this.mediaPathReady=void 0,this.mediaRevision=0,this.sessionReady=!1,this.startRTTProbe(),a();else if(S?.type==="runtime.event")this.recordSessionEvent(S.event);else if(S?.type==="socket.message")this.handleControl(S.data);else if(S?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL(S.recovery).then((g)=>{if(!this.closed&&this.worker===_)_.postMessage({type:"socket.credentials",id:S.id,mediaURL:g})},(g)=>{if(this.closed||this.worker!==_)return;let A=th(g);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:A.denied?"revoked":"retrying",...S.recovery,status:A.status,code:A.code}),_.postMessage({type:"socket.credentials",id:S.id,denied:A.denied})})}else if(S?.type==="socket.error")this.recordSessionEvent({timestamp:S.timestamp,action:"websocket",outcome:"transport_error"});else if(S?.type==="socket.close")if(this.recordSessionEvent({timestamp:S.timestamp,action:"websocket",outcome:"closed",code:String(S.code),detail:S.reason,was_clean:S.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"transport_disconnected"),this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else a(Error("audio connection closed before it was ready"));else if(S?.type==="socket.failed")this.mediaSocketConnected=!1,a(Error(S.detail||"audio connection lost")),this.fail(S.detail||"audio connection lost",S.recoverable===!0);else if(S?.type==="transport.drop"&&S.event)this.diagnostics.dropEvents=this.mergeDropEvents(S.event);else if(S?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=S.buffered_bytes??0,this.diagnostics.captureQueueEvents=S.capture_queue_events,this.transportTiming=S.timing??{},S.observation)this.transportTelemetry.observe({...this.transportTiming,...S.observation,rtt_ms:S.timing?.rtt_ms,queue_ms:this.diagnostics.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:S.buffered_bytes,underruns:this.diagnostics.underruns,dropped_ms:this.diagnostics.droppedMs,capture_sequence_gaps:this.diagnostics.captureSequenceGaps,playback_sequence_gaps:this.diagnostics.playbackSequenceGaps,main_thread_max_pause_ms:this.runtimeTelemetry.counters.main_thread_max_pause_ms},{connection:this.mediaSocketConnected?"connected":"reconnecting",context:this.ctx?.state,muted:String(this.muted),device_muted:String(this.stream?.getAudioTracks()[0]?.muted),track:this.stream?.getAudioTracks()[0]?.readyState,codec:"pcm16"},performance.now(),S.observation?.timestamp,S.observation?.window_ms);if(this.diagnostics.transportSamples=this.transportTelemetry.recent(),typeof S.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(S.timing.rtt_ms)}},_.onerror=(y)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:y.message?.slice(0,160)}),a(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},_.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:h,contextRate:this.ctx?.sampleRate??yh,muted:this.muted,capturePort:u.port2,playbackPort:f.port2},[u.port2,f.port2])})}mergeDropEvents(h,t){if(h)this.workerDropEvents=[...this.workerDropEvents,h].slice(-100);if(t)this.playbackDropEvents=t.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((m,s)=>m.timestamp.localeCompare(s.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;shutdownIntent="session_cleanup";reportedPlaybackIDs=new Set;reportedCaptureQueueIDs=new Set;handleControl(h){if(this.closed)return;try{let t=JSON.parse(h);if(t.type==="media.ready"||t.type==="media.not_ready"){let m=t;if(!Number.isSafeInteger(m.revision)||m.revision<=this.mediaRevision)return;this.mediaRevision=m.revision,this.mediaPathReady=m.type==="media.ready";try{this.callbacks.onMediaReadiness?.(m)}catch{}if(!this.mediaPathReady&&this.callAnswered)this.callbacks.onState?.("reconnecting","Connecting carrier audio…");this.updateLiveState()}else if(t.type==="ready")this.sessionReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState();else if(t.type==="dtmf.error"||t.type==="dtmf.sent")this.callbacks.onNotice?.(t.type==="dtmf.sent"?"Keypad tone sent":t.detail||"Keypad tone failed");else if(t.type==="pong"){if(this.diagnostics.captureSequenceGaps=t.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof t.nonce==="number"&&t.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-t.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(t.type==="call.ended"||t.type==="session.replaced"){this.setPlaybackObservation(!1,t.type==="call.ended"?"call_ended":"observation_ended"),this.shutdownIntent=t.type==="call.ended"?"call_ended":"session_replaced",this.closed=!0;try{this.callbacks.onState?.("ended",t.type)}finally{this.teardown()}}else if(t.type==="call.status"&&typeof t.call_id==="string"&&typeof t.status==="string"){if(t.hold_state)this.playbackHeld=t.hold_state!=="active",this.setPlaybackObservation(!this.playbackHeld&&this.microphoneTransportReady&&this.ctx?.state==="running",this.playbackHeld?"hold":"observation_resumed");if(t.hold_state&&t.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callAnswered=["answered","in-progress"].includes(t.status),this.updateLiveState(),this.callbacks.onCallStatus?.(t)}else if(t.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:t.detail?.slice(0,160)}),this.fail(t.detail||"The call could not be connected.");else if(t.type==="coach.state")this.callbacks.onNotice?.(t.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(t.type==="audio.health"){let m=t;if(m.state!=="healthy"&&m.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:m.state,reason:m.reason,stages:m.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),m.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&m.state==="healthy"&&m.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(t.type==="media.delivery"){let m=t.state;if(m==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(m==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=m==="stalled"}else if(t.type==="peer.disconnected")this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"carrier_disconnected"),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(t.type==="peer.connected")this.microphoneTransportReady=!0,this.setPlaybackObservation(this.ctx?.state==="running"&&!this.playbackHeld,"carrier_connected"),this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState()}catch{}}updateLiveState(){if(this.sessionReady&&this.callAnswered&&this.microphoneTransportReady&&this.mediaPathReady!==!1&&this.ctx?.state==="running")this.callbacks.onState?.("live")}startRTTProbe(){this.stopRTTProbe();let h=()=>{this.sendDiagnostics()};h(),this.pingTimer=setInterval(h,5000)}sendText(h){this.worker?.postMessage({type:"send.text",data:h})}recordSessionEvent(h){try{h={...this.callbacks.sessionDiagnostics?.(),...h}}catch{}h=mh(h),this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],h].slice(-50);try{this.callbacks.onSessionEvent?.(h)}catch{}try{let t={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,session_events:[h]}};if(new TextEncoder().encode(JSON.stringify(t)).byteLength<=1100)this.transportSender.enqueueAuxiliary(t);else this.transportSender.skippedAuxiliary++}catch{}}sendDiagnostics(){let h=this.diagnostics,t=(_,u)=>{let f=(_??[]).slice(-64),i=f.filter((M)=>!u.has(M.id)).slice(0,8);if(this.mediaSocketConnected){let M=new Set(f.map((a)=>a.id));for(let a of u)if(!M.has(a))u.delete(a);for(let a of i)u.add(a.id)}return i},m=t(h.playbackEvents,this.reportedPlaybackIDs),s=t(h.captureQueueEvents,this.reportedCaptureQueueIDs);if(this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{media_transport:"websocket",codec:"pcm16",client_version:Th,client_epoch:this.clientEpoch,playback_events:m,capture_queue_events:s,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:h.rttMs,playback_queue_ms:h.queueMs,playback_target_ms:h.targetMs,playback_max_queue_ms:h.maxQueueMs,playback_underruns:h.underruns,playback_underrun_ms:h.underrunDurationMs,playback_underrun_events:h.underrunEvents,playback_dropped_ms:h.droppedMs,websocket_buffered_bytes:h.websocketBufferedBytes,audio_context_rate:h.audioContextRate,microphone_sample_rate:h.microphoneSampleRate,microphone_channel_count:h.microphoneChannelCount,echo_cancellation:h.echoCancellation,noise_suppression:h.noiseSuppression,auto_gain_control:h.autoGainControl,mic_active_rms_dbfs:h.micActiveRmsDbfs,mic_peak_dbfs:h.micPeakDbfs,mic_post_peak_dbfs:h.micPostPeakDbfs,mic_input_gain_db:h.micInputGainDb,mic_limiter_reduction_db:h.micLimiterReductionDb,capture_sequence_gaps:h.captureSequenceGaps,playback_sequence_gaps:h.playbackSequenceGaps,drop_events:h.dropEvents}})),this.mediaSocketConnected)this.transportSender.enqueue(this.transportTelemetry.drain());this.callbacks.onDiagnostics?.({...h})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setPlaybackObservation(h,t){try{if(this.playback?.port.postMessage({type:"playback.telemetry.boundary",active:h,reason:t,audio_time_ms:(this.ctx?.currentTime??0)*1000}),!h)this.diagnostics.underrunEvents=Jt(this.diagnostics.underrunEvents??[],t)}catch{}}setMuted(h){let t=this.muted!==h;if(this.muted=h,this.worker?.postMessage({type:"muted",value:h}),this.capture?.port.postMessage({type:"muted",value:h}),h)this.sendText(JSON.stringify({type:"interrupt"}));if(t)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:h?"muted":"unmuted"})}stop(){this.shutdownIntent="user_stop";let h=!this.closed;this.closed=!0;try{if(h)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(h,t=!1){if(this.shutdownIntent="audio_error",!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:h.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",h,{recoverable:t})}finally{this.teardown()}}teardown(){this.runtimeTelemetry.stopEnvironment(),this.transportSender.stop(),this.setPlaybackObservation(!1,"observation_ended"),this.stopRingback(),this.microphoneTransportReady=!1,this.sessionReady=this.callAnswered=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let h=this.worker;if(h?.postMessage({type:"close",reason:this.shutdownIntent}),h){let t=setTimeout(()=>h.terminate(),600);h.onmessage=(m)=>{if(m.data?.type==="socket.shutdown.complete")clearTimeout(t),h.terminate()}}this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((t)=>t.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var L0=yh*$h/1000;var jh=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
`;var Et=`// Passive receiver only. Both directions share one rendering clock and delay.
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
`;var st=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
`;var Rt=new WeakMap,ch=new Map,jt=8000;async function Xh(h,t){let m=t==="worklet"?jh:t==="worker"?st:Et;if(typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin){let a=[URL.createObjectURL(new Blob([m],{type:"text/javascript"}))];return{urls:a,dispose(){a.splice(0).forEach((y)=>URL.revokeObjectURL(y))}}}if(h.name!=="telephony"||!h.projectId||!Number.isSafeInteger(h.installId)||h.installId<=0)throw Error("Telephony audio requires a project and installation");let s=new URL(h.mcpURL(),location.href),_=`${s.origin}:${s.pathname}:${h.projectId}:${h.installId}`,u=Rt.get(h);if(!u||u.scope!==_)u={scope:_,entries:new Map},Rt.set(h,u);let f=u.entries,i=t,M=f.get(i);if(!M){M=(async()=>{let y=ch.get(t);if(!y)y=crypto.subtle.digest("SHA-256",new TextEncoder().encode(m)).then((b)=>Array.from(new Uint8Array(b),(w)=>w.toString(16).padStart(2,"0")).join("")),ch.set(t,y),y.catch(()=>{if(ch.get(t)===y)ch.delete(t)});let S=`/ui/frontend/${t}-${await y}.js`,g=new AbortController,A;try{if(await Promise.race([h.get(S,{cache:"force-cache",redirect:"error",signal:g.signal,headers:{Accept:"text/plain"}}),new Promise((C,P)=>{A=setTimeout(()=>{P(Error("Telephony audio asset preparation timed out")),g.abort()},jt)})])!==m)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let w=new URL(`/api/apps/telephony/_install/${h.installId}${S}`,s);return w.searchParams.set("project_id",h.projectId),w.searchParams.set("install_id",String(h.installId)),w.href}finally{clearTimeout(A)}})(),f.set(i,M);let a=M;a.catch(()=>{if(f.get(i)===a)f.delete(i)})}return{urls:[await M],dispose(){}}}function Yh(h=!1){let t=(h?[jh]:[jh,st]).map((m)=>URL.createObjectURL(new Blob([m],{type:"text/javascript"})));return{urls:t,dispose(){t.splice(0).forEach((m)=>URL.revokeObjectURL(m))}}}async function wh(h,t=!1){if(typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin)return Yh(t);let m=await Promise.all(t?[Xh(h,"worklet")]:[Xh(h,"worklet"),Xh(h,"worker")]);return{urls:m.flatMap((s)=>s.urls),dispose(){m.forEach((s)=>s.dispose())}}}class Lh{options;target;bitrate=32000;stable;last=-1/0;initial;maximum;constructor(h={}){this.options=h;this.initial=h.playbackTargetMs??60,this.target=this.initial,this.maximum=Math.max(this.initial,h.playbackMaxMs??160)}observe(h,t=performance.now()){if(t<this.last)this.last=-1/0,this.stable=void 0;if(t-this.last<2000)return;this.last=t;let m=h.receiver_jitter===void 0?void 0:h.receiver_jitter*1000,s=h.receiver_concealed_delta_ms??0;if(this.options.playbackAdaptive!==!1){let f=Math.min(this.maximum,Math.max(this.initial,this.initial+Math.ceil(Math.max(0,(m??0)-10)*2/20)*20,s>0?this.target+20:this.initial));if(f>this.target)this.target=Math.min(f,this.target+20),this.stable=void 0;else if(m!==void 0&&s===0&&m<10){if(this.stable??=t,t-this.stable>=1e4)this.target=Math.max(this.initial,this.target-10),this.stable=t}else this.stable=void 0}let{pair_availableOutgoingBitrate:_,remote_receiver_fractionLost:u}=h;if(_!==void 0&&_>0&&_<64000)this.bitrate=Math.max(16000,Math.min(32000,Math.floor((_-32000)*0.8/1000)*1000));else if(u!==void 0&&u>=0.03)this.bitrate=Math.max(16000,this.bitrate-4000);else if(u!==void 0&&u<0.01)this.bitrate=Math.min(32000,this.bitrate+2000)}}class _t{tokens=1100;at;rate=640;observeAvailableBitrate(h){this.rate=h!==void 0&&h>=96000?2000:640}consume(h,t=performance.now()){if(!Number.isFinite(h)||!Number.isFinite(t)||h<0||h>1100)return!1;if(this.at!==void 0)this.tokens=Math.min(1100,this.tokens+Math.max(0,t-this.at)*this.rate/1000);if(this.at=t,this.tokens<h)return!1;return this.tokens-=h,!0}}function Ih(h){if(h===void 0)return"websocket";if(!["websocket","webrtc","auto"].includes(h))throw RangeError("Unsupported softphone media transport");return h}function ft(h,t,m={}){if(t!==void 0&&typeof t!=="boolean")throw RangeError("webrtcFec must be boolean");let s=new URL(h);if(Pt(s,m),s.searchParams.set("transport","webrtc"),s.searchParams.delete("webrtc_fec"),t!==void 0)s.searchParams.set("webrtc_fec",String(t));return s.toString()}var D=(h)=>{try{h?.()}catch{}},qh=(h,t=1000000000000)=>typeof h==="number"&&Number.isFinite(h)?Math.max(0,Math.min(h,t)):0;function ct(h){return h===!0?"relay_configured":h===!1?"no_relay":"unreported"}function zh(h){let t=th(h);return["turn_relay_unavailable","turn_client_update_required","turn_relay_required"].includes(h?.code??t.code)||t.denied||t.code==="media_session_changed"||t.code==="call_ended"||t.code==="call_not_attachable"||t.expired||["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes(h?.name)}function ut(h,t){let m=0,s=0,_=0,u=0,f=0,i=0,M=0,a=0,y=0,S=null,g,A,b,w={},C={},P={},q=(e,x)=>typeof e?.[x]==="number"&&Number.isFinite(e[x])?Math.max(0,e[x]):void 0,R=(e,x,T)=>{for(let F of T){let O=q(e,F);if(O!==void 0)C[x+F]=O}},J=(e,x,T,F,O)=>{let V=q(e,T),H=q(e,F),Z=t?.counters?.[x];if(V!==void 0&&H!==void 0){if(w[x]??={},w[x][T]=V,w[x][F]=H,q(e,"ssrc")!==void 0)w[x].ssrc=e.ssrc;if(Z&&Z.ssrc===q(e,"ssrc")&&V>=Z[T]&&H>Z[F])C[O]=(V-Z[T])/(H-Z[F])*1000}},Q=(e,x,T)=>{let F=q(e,"packetsLost"),O=q(e,"ssrc"),V=q(e,"timestamp"),H=t?.counters?.[x];if(O!==void 0)C[T+"ssrc"]=O;if(F===void 0||V===void 0)return;if(w[x]??={},w[x].packetsLost=F,w[x].at=V,O!==void 0)w[x].ssrc=O;if(H&&H.ssrc===O&&F>=H.packetsLost&&V>H.at)C[T+"loss_delta"]=(C[T+"loss_delta"]??0)+(F-H.packetsLost),C[T+"loss_window_ms"]=V-H.at},B=(e,x,T)=>{let F=q(e,"concealedSamples"),O=q(e,"silentConcealedSamples"),V=q(e,"timestamp"),H=q(e,"ssrc"),Z=t?.counters?.[x];if(F===void 0||V===void 0)return;if(w[x]??={},Object.assign(w[x],{concealedSamples:F,at:V}),H!==void 0)w[x].ssrc=H;if(O!==void 0)w[x].silentConcealedSamples=O;if(Z&&Z.ssrc===H&&F>=Z.concealedSamples&&V>Z.at){if(C.receiver_concealed_delta_ms=(C.receiver_concealed_delta_ms??0)+(F-Z.concealedSamples)*1000/T,C.receiver_concealed_window_ms=V-Z.at,O!==void 0&&O>=Z.silentConcealedSamples)C.receiver_silent_concealed_delta_ms=(C.receiver_silent_concealed_delta_ms??0)+(O-Z.silentConcealedSamples)*1000/T}},X=!1,gh=0;h.forEach((e,x)=>{if(y=Math.max(y,qh(e.timestamp,1000000000000000)),(e.kind??e.mediaType??h.get(e.localId)?.kind)==="audio"&&gh++<32){if(e.type==="outbound-rtp")m+=qh(e.bytesSent),R(e,"sender_",["bytesSent","packetsSent","headerBytesSent","retransmittedPacketsSent","retransmittedBytesSent","nackCount","targetBitrate"]),J(e,x,"totalPacketSendDelay","packetsSent","sender_send_delay_interval_ms");if(e.type==="inbound-rtp"){Q(e,x,"receiver_"),s+=qh(e.bytesReceived),_+=qh(e.packetsLost),u=Math.max(u,qh(e.jitter)*1000);let F=h.get(e.codecId),O=q(F,"clockRate")??48000;if(B(e,x,O),X||=q(e,"concealedSamples")!==void 0,f+=qh(e.concealedSamples)*1000/O,i+=qh(e.packetsDiscarded),M+=qh(e.jitterBufferDelay),a+=qh(e.jitterBufferEmittedCount),R(e,"receiver_",["bytesReceived","packetsReceived","packetsLost","jitter","packetsDiscarded","concealedSamples","silentConcealedSamples","concealmentEvents","insertedSamplesForDeceleration","removedSamplesForAcceleration","totalSamplesReceived","audioLevel","totalAudioEnergy","totalSamplesDuration","nackCount","fecPacketsReceived","fecPacketsDiscarded"]),J(e,x,"jitterBufferDelay","jitterBufferEmittedCount","receiver_jitter_buffer_interval_ms"),J(e,x,"jitterBufferTargetDelay","jitterBufferEmittedCount","receiver_jitter_target_interval_ms"),J(e,x,"jitterBufferMinimumDelay","jitterBufferEmittedCount","receiver_jitter_minimum_interval_ms"),R(e,"receiver_",["totalProcessingDelay","totalSamplesDecoded"]),P.receiver_processing=q(e,"totalSamplesDecoded")===void 0?"unavailable":"per_decoded_sample",J(e,x,"totalProcessingDelay","totalSamplesDecoded","receiver_processing_interval_ms"),q(e,"lastPacketReceivedTimestamp")!==void 0)C.receiver_last_packet_age_ms=Math.max(0,e.timestamp-e.lastPacketReceivedTimestamp);if(F){if(P.codec=F.mimeType,C.codec_clock_rate=O,q(F,"channels")!==void 0)C.codec_channels=F.channels}}if(e.type==="remote-inbound-rtp")R(e,"remote_receiver_",["packetsLost","fractionLost","jitter","roundTripTime","totalRoundTripTime","roundTripTimeMeasurements"]),Q(e,x,"remote_receiver_");if(e.type==="remote-outbound-rtp")R(e,"remote_sender_",["packetsSent","bytesSent"]);let T=e.type==="outbound-rtp"?"bytesSent":e.type==="inbound-rtp"?"bytesReceived":void 0;if(T&&q(e,T)!==void 0)w[x]??={},w[x][T]=e[T],w[x].at=e.timestamp}if(e.type==="transport"&&e.selectedCandidatePairId){g=e.selectedCandidatePairId,P.dtls=e.dtlsState;let T=h.get(g);if(T){S=q(T,"currentRoundTripTime")!==void 0?T.currentRoundTripTime*1000:null,R(T,"pair_",["availableOutgoingBitrate","availableIncomingBitrate","currentRoundTripTime","totalRoundTripTime","bytesSent","bytesReceived","requestsSent","requestsReceived","responsesSent","responsesReceived","consentRequestsSent"]),P.pair=T.state;let F=h.get(T.localCandidateId),O=h.get(T.remoteCandidateId);if(F)A=F.protocol,b=F.candidateType,P.protocol=A,P.local_candidate=b,P.relay_protocol=F.relayProtocol;if(O)P.remote_candidate=O.candidateType}}});let fh=(e,x)=>{if(t?.counters){let F=0;for(let[O,V]of Object.entries(w)){let H=t.counters[O];if(H&&V[e]>=H[e]&&V.at>H.at)F+=(V[e]-H[e])*8000/(V.at-H.at)}return F}let T=t&&y>t.at?(y-t.at)/1000:0;return T?Math.max(0,x-(e==="bytesSent"?t.sent:t.received))*8/T:0},uh=(t?.pathRevision??0)+(g&&g!==t?.pairID?1:0);C.path_revision=uh;let bh=fh("bytesSent",m),Mh=fh("bytesReceived",s);return{previous:{at:y,sent:m,received:s,counters:w,pairID:g,pathRevision:uh},rtt:S,queueMs:C.receiver_jitter_buffer_interval_ms??0,metrics:oh({...C,send_bitrate_bps:bh,receive_bitrate_bps:Mh}),states:ht(P),webrtc:{relayProtocol:P.relay_protocol,protocol:A,candidateType:b,sendBitrateBps:bh,receiveBitrateBps:Mh,packetsLost:_,jitterMs:u,concealedMs:X?f:void 0,packetsDiscarded:i,jitterBufferMs:a>0?M/a*1000:0,jitterBufferIntervalMs:C.receiver_jitter_buffer_interval_ms}}}function It(h,t=new Date().toISOString()){let m=[];for(let[s,_]of[["receiver_","carrier_to_operator"],["remote_receiver_","operator_to_carrier"]]){let u=h[s+"loss_delta"];if(u>0)m.push({timestamp:t,direction:_,reason:"webrtc_packet_loss",duration_ms:0,packet_count:u,ssrc:h[s+"ssrc"],window_ms:h[s+"loss_window_ms"]})}if(h.receiver_concealed_delta_ms>0)m.push({timestamp:t,direction:"carrier_to_operator",reason:"webrtc_concealment",duration_ms:Math.round(h.receiver_concealed_delta_ms),ssrc:h.receiver_ssrc,window_ms:h.receiver_concealed_window_ms});return m}class Mt{callbacks;ice;stopped=!1;generation=0;socket;pc;stream;context;capture;whisper;destination;speaker;remoteAudio;speakerMeter;carrierStalled=!1;deliveryDegraded=!1;ready=!1;answered=!1;mediaPathReady;mediaRevision=0;reportedLive=!1;peer=!1;muted=!1;whisperEpoch=null;whisperBase=null;whisperResampler=new Ph;timer;retry;cancelSetup;recovering=!1;recoveryGeneration=0;recoveryContext;recoveryExpiry;recoveryRequest;ringback;options;workletURL;events=[];previous;sampling=new WeakSet;transportTelemetry=new Hh("webrtc");transportSender=new Eh((h)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[h]}})},(h,t=0)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let m=h;if(m?.diagnostics?.webrtc){let s=m.diagnostics.webrtc;return this.sendTelemetry({...m,diagnostics:{...m.diagnostics,webrtc:{...s,jitterBufferSampleAgeMs:(s.jitterBufferSampleAgeMs??0)+t}}})}return this.sendTelemetry(h)});controlBudget=new _t;quality;playbackReceiver;uploadSender;applyingBitrate=!1;appliedBitrate;statsErrors=0;lastStatsSend=-1/0;lastValidStatsAt=-1/0;clientEpoch=crypto.randomUUID();relayConfigured;nativeCounts;completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};runtime=new Vh((h)=>this.recordSessionEvent(h));diagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:null,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(h){this.callbacks=h}async start(h,t,m){if(this.options=t,this.workletURL=m,!m)throw Error("WebRTC requires the shared Telephony audio processor");if(typeof RTCPeerConnection!=="function")throw Error("WebRTC audio is unavailable in this browser");v(t),await this.connect(h)}current(h){return!this.stopped&&this.generation===h}guard(h){if(!this.current(h))throw Error("Audio session cancelled")}async connect(h){this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+Th.replaceAll(".","_")}),this.runtime.observeEnvironment();let t=++this.generation,m=this.options;for(let g of this.events.slice(-8))this.enqueueRTCEvents({session_events:[g]});for(let g of this.diagnostics.dropEvents.slice(-8))this.enqueueRTCEvents({drop_events:[g]});this.ready=this.peer=this.answered=this.reportedLive=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.previous=void 0,this.quality=new Lh(m),this.appliedBitrate=void 0;let s=await c("microphone_device",(g)=>this.recordSessionEvent(g),()=>navigator.mediaDevices.getUserMedia({audio:Sh(m)}));if(!this.current(t))throw s.getTracks().forEach((g)=>g.stop()),Error("Audio session cancelled");this.stream=s;let _=s.getAudioTracks()[0];if(!_)throw Error("No microphone audio track");let u=_.getSettings();this.diagnostics={...this.diagnostics,microphoneSampleRate:u.sampleRate??0,microphoneChannelCount:u.channelCount??1,echoCancellation:typeof u.echoCancellation==="boolean"?u.echoCancellation:null,noiseSuppression:u.noiseSuppression??null,autoGainControl:u.autoGainControl??null,micInputGainDb:m.inputGainDB},_.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),_.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),_.onended=()=>{if(this.current(t))this.disconnected(t,"microphone_ended")},this.context=new AudioContext({latencyHint:"interactive"});let f=this.context;if(f.onstatechange=()=>{this.runtime.context(f.state),this.updateLiveState()},this.runtime.context(f.state),await c("audio_context",(g)=>this.recordSessionEvent(g),()=>f.resume()),this.guard(t),m.outputDeviceId&&"setSinkId"in f)await f.setSinkId(m.outputDeviceId);await c("audio_worklet",(g)=>this.recordSessionEvent(g),()=>f.audioWorklet.addModule(this.workletURL)),this.guard(t),this.diagnostics.audioContextRate=f.sampleRate,this.capture=new AudioWorkletNode(f,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:m.inputGainDB,highpassFilter:m.highpassFilter}}),this.destination=f.createMediaStreamDestination(),this.destination.channelCount=1,f.createMediaStreamSource(s).connect(this.capture).connect(this.destination),this.capture.port.onmessage=(g)=>{if(g.data?.type==="capture.stats"){let A=(w)=>w>0?20*Math.log10(w):null;this.diagnostics.micActiveRmsDbfs=A(g.data.active_rms),this.diagnostics.micPeakDbfs=A(g.data.pre_peak),this.diagnostics.micPostPeakDbfs=A(g.data.post_peak),this.diagnostics.micLimiterReductionDb=g.data.limiter_reduction_db??0;let b=0;if(this.speakerMeter){let w=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(w),b=Math.sqrt(w.reduce((C,P)=>C+P*P,0)/w.length)}D(()=>this.callbacks.onLevels?.(qh(g.data.active_rms,1),b))}},this.speaker=f.createGain(),this.speaker.gain.value=m.outputVolume??1,this.speaker.connect(f.destination),this.speakerMeter=f.createAnalyser(),this.speakerMeter.fftSize=256;let i=f.createGain();i.gain.value=0,this.speaker.connect(this.speakerMeter).connect(i).connect(f.destination),this.whisper=new AudioWorkletNode(f,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:v(m)}),this.whisper.connect(this.speaker),this.gate();let M=new WebSocket(ft(h,m.webrtcFec,m));M.binaryType="arraybuffer",this.socket=M;let a=!1,y=!1,S=!1;await c("audio_transport",(g)=>this.recordSessionEvent(g),()=>new Promise((g,A)=>{let b=(P)=>{if(y)return;y=!0,clearTimeout(w),this.cancelSetup=void 0,P?A(S?Object.assign(P,{code:"turn_relay_required"}):P):g()},w=setTimeout(()=>b(Error("WebRTC audio connection timed out")),18000);this.cancelSetup=()=>b(Error("Audio session cancelled"));let C=()=>{if(this.current(t)&&this.ready&&a)this.gate(),b(),this.updateLiveState()};M.onmessage=(P)=>{if(!this.current(t))return;if(P.data instanceof ArrayBuffer){this.playWhisper(P.data);return}if(typeof P.data!=="string")return;let q;try{q=JSON.parse(P.data)}catch{return}if(q.type==="webrtc.error"){b(Object.assign(Error("Required TURN relay is unavailable"),{code:q.code}));return}if(this.ice?.handle(q))return;if(q.type==="webrtc.config"){if(S=q.ice_transport_policy==="relay",this.relayConfigured=typeof q.relay_configured==="boolean"?q.relay_configured:void 0,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"ice_configuration",outcome:ct(q.relay_configured)}),this.pc){b(Error("Repeated WebRTC configuration"));return}(async()=>{let R=new RTCPeerConnection(Wh(q));this.pc=R,this.ice?.stop(),this.ice=new Nh((Q)=>this.send(Q),()=>this.current(t),(Q,B)=>this.recordSessionEvent({...B,timestamp:new Date().toISOString(),action:"ice_restart",outcome:Q}),(Q)=>{this.relayConfigured=Q.iceServers?.some((B)=>(typeof B.urls==="string"?[B.urls]:B.urls).some((X)=>/^turns?:/.test(X)))??!1}),this.ice.register("main",R),this.destination.stream.getAudioTracks().forEach((Q)=>{let B=R.addTrack(Q,this.destination.stream);this.uploadSender=B,this.applyUploadBitrate(B,32000,t)}),R.ontrack=(Q)=>{if(!this.current(t))return;this.playbackReceiver=Q.receiver,this.applyPlaybackTarget();let B=new MediaStream([Q.track]),X=new Audio;X.muted=!0,X.srcObject=B,this.remoteAudio=X,X.play().catch(()=>D(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction."))),f.createMediaStreamSource(B).connect(this.speaker)},R.onconnectionstatechange=()=>{if(!this.current(t))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:R.connectionState}),R.connectionState==="connected")a=!0,C();if(R.connectionState==="failed"||R.connectionState==="closed")if(!y)b(Error("WebRTC connection failed"));else this.disconnected(t,"webrtc_"+R.connectionState)};let J=await R.createOffer();this.guard(t),await R.setLocalDescription(J),await new Promise((Q,B)=>{if(R.iceGatheringState==="complete"){Q();return}let X=setTimeout(()=>{R.onicegatheringstatechange=null,B(Error("ICE gathering timed out"))},7000);R.onicegatheringstatechange=()=>{if(R.iceGatheringState==="complete")clearTimeout(X),R.onicegatheringstatechange=null,Q()}}),this.guard(t),M.send(JSON.stringify({type:"webrtc.offer",sdp:R.localDescription.sdp,ice_version:1}))})().catch((R)=>b(R instanceof Error?R:Error("WebRTC setup failed")));return}if(q.type==="webrtc.answer"){this.pc?.setRemoteDescription({type:"answer",sdp:q.sdp}).catch(()=>b(Error("Invalid WebRTC answer")));return}if(q.type==="media.ready"||q.type==="media.not_ready"){if(!Number.isSafeInteger(q.revision)||q.revision<=this.mediaRevision)return;this.mediaRevision=q.revision,this.mediaPathReady=q.type==="media.ready",D(()=>this.callbacks.onMediaReadiness?.(q)),this.updateLiveState()}if(q.type==="ready")this.ready=!0,this.send({type:"media.capabilities",version:2,versions:[3],whisper:!0}),C();if(q.type==="peer.connected")this.peer=!0,this.gate(),C();if(q.type==="peer.disconnected")this.peer=!1,this.gate(),this.updateLiveState(),D(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));if(q.type==="call.status")this.answered=["answered","in-progress"].includes(q.status),C(),D(()=>this.callbacks.onCallStatus?.(q));if(q.type==="call.error")if(!y)b(Error(q.detail??"Carrier activation failed"));else this.stop(),D(()=>this.callbacks.onState?.("error",q.detail));if(q.type==="call.ended"||q.type==="session.replaced")if(!y)b(Error(q.type));else this.stop(),D(()=>this.callbacks.onState?.("ended",q.type));if(q.type==="audio.health"){if(this.diagnostics.audioHealth={state:q.state,reason:q.reason,stages:q.stages},D(()=>this.callbacks.onAudioHealth?.(q)),q.state==="audio_degraded"&&!this.deliveryDegraded)this.deliveryDegraded=!0,D(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));else if(this.deliveryDegraded&&q.state==="healthy"&&q.stages?.telephony_to_browser?.state==="healthy"&&f.state==="running")this.deliveryDegraded=!1,D(()=>this.callbacks.onNotice?.("Speech delivery restored."))}if(q.type==="media.delivery"){if(q.state==="stalled")D(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));else if(q.state==="flowing"&&this.carrierStalled)D(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));this.carrierStalled=q.state==="stalled"}if(q.type==="call.notice")D(()=>this.callbacks.onNotice?.(q.detail??q.reason??"Audio delivery notice"));if(q.type==="coach.state")this.whisperEpoch=q.talking&&Number.isInteger(q.epoch)?q.epoch:null,this.whisperBase=null,this.whisper?.port.postMessage({type:"whisper.clear"})},M.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"}),M.onclose=(P)=>{if(!this.current(t))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(P.code),was_clean:P.wasClean}),!y){b(Object.assign(Error("WebRTC signaling unavailable"),P.code===1008?{status:403}:{}));return}if(P.code===1008){this.stop(),D(()=>this.callbacks.onState?.("error","Media authorization ended"));return}this.disconnected(t,"signaling_closed")}})),this.guard(t),this.timer=setInterval(()=>{this.runtime.tick(),this.statistics(t)},1000),await this.statistics(t)}send(h){if(this.socket?.readyState===WebSocket.OPEN&&this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(h))}catch{}}updateLiveState(){let h=this.ready&&this.peer&&this.answered&&this.mediaPathReady!==!1&&this.pc?.connectionState==="connected"&&this.context?.state==="running";if(h===this.reportedLive)return;this.reportedLive=h,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"media_path",outcome:h?"ready":"waiting"}),D(()=>this.callbacks.onState?.(h?"live":"reconnecting",h?void 0:"Connecting carrier audio…"))}gate(){let h=!this.stopped&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!h}),this.destination?.stream.getAudioTracks().forEach((t)=>t.enabled=h)}async statistics(h){if(!this.current(h)||!this.pc||this.sampling.has(this.pc))return;let t=this.pc,m=performance.now();this.sampling.add(t);try{let s=ut(await t.getStats(),this.previous);if(!this.current(h)||this.pc!==t)return;if(this.previous=s.previous,this.lastValidStatsAt=performance.now(),this.nativeCounts={packetsLost:s.webrtc.packetsLost,packetsDiscarded:s.webrtc.packetsDiscarded,concealedMs:s.webrtc.concealedMs},s.webrtc.packetsLost+=this.completedCounts.packetsLost,s.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded,s.webrtc.concealedMs!==void 0)s.webrtc.concealedMs+=this.completedCounts.concealedMs;if(this.quality?.observe(s.metrics),this.applyPlaybackTarget(),this.uploadSender&&this.quality)this.applyUploadBitrate(this.uploadSender,this.quality.bitrate,h);if(s.metrics.requested_playback_target_ms=this.quality?.target??60,this.appliedBitrate!==void 0)s.metrics.requested_upload_bitrate_bps=this.appliedBitrate;this.controlBudget.observeAvailableBitrate(s.metrics.pair_availableOutgoingBitrate);let _=It(s.metrics);for(let f of _)this.enqueueRTCEvents({drop_events:[f]});this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,..._].slice(-100),this.diagnostics={...this.diagnostics,rttMs:s.rtt,queueMs:s.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,s.queueMs),targetMs:this.quality?.target??this.options?.playbackTargetMs??60,webrtc:s.webrtc,playbackSequenceGaps:s.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};let u=this.stream?.getAudioTracks()[0];this.transportTelemetry.observe({...s.metrics,rtt_ms:s.rtt,queue_ms:s.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:this.socket?.bufferedAmount??0,stats_errors:this.statsErrors,stats_duration_ms:performance.now()-m,main_thread_max_pause_ms:this.runtime.counters.main_thread_max_pause_ms},{...s.states,ice:t.iceConnectionState,connection:"connected",context:this.context?.state,muted:String(this.muted),device_muted:String(u?.muted),track:u?.readyState}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),D(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}catch{if(this.current(h)&&this.pc===t)this.statsErrors++,this.transportTelemetry.observe({stats_errors:this.statsErrors,stats_duration_ms:performance.now()-m},{ice:t.iceConnectionState,context:this.context?.state,muted:String(this.muted)}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),D(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}finally{this.sampling.delete(t)}}applyPlaybackTarget(){let h=this.playbackReceiver;if(!h||!this.quality)return;if("jitterBufferTarget"in h)try{h.jitterBufferTarget=this.quality.target}catch{}}async applyUploadBitrate(h,t,m){if(this.applyingBitrate||this.appliedBitrate===t)return;this.applyingBitrate=!0;try{let s=h.getParameters();if(s.encodings?.length){if(s.encodings[0].maxBitrate=t,await h.setParameters(s),this.current(m)&&this.uploadSender===h)this.appliedBitrate=t}}catch{}finally{this.applyingBitrate=!1}}sendTelemetry(h){if(!this.ready||this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let t=JSON.stringify(h),m=new TextEncoder().encode(t).byteLength+58;if(!this.controlBudget.consume(m))return!1;try{return this.socket.send(t),!0}catch{return!1}}enqueueRTCEvents(h){try{let t={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,...h}};if(new TextEncoder().encode(JSON.stringify(t)).byteLength+58>1100){this.transportSender.skippedAuxiliary++;return}this.transportSender.enqueueAuxiliary(t)}catch{this.transportSender.skippedAuxiliary++}}sendStatistics(){let h=this.stream?.getAudioTracks()[0];if(performance.now()-this.lastStatsSend>=5000)this.lastStatsSend=performance.now(),this.transportSender.enqueueReport({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,client_version:Th,relay_configured:this.relayConfigured,media_transport:"webrtc",codec:"opus",webrtc:{...Object.fromEntries(Object.entries(this.diagnostics.webrtc??{}).map(([t,m])=>[t,typeof m==="number"?Math.round(m*10)/10:m])),jitterBufferSampleAgeMs:Number.isFinite(this.lastValidStatsAt)?Math.max(0,performance.now()-this.lastValidStatsAt):void 0},connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:h?.readyState,microphone_device_muted:h?.muted,rtt_ms:this.diagnostics.rttMs===null?null:Math.round(this.diagnostics.rttMs),playback_queue_ms:Math.round(this.diagnostics.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:this.diagnostics.webrtc?.packetsLost}}),this.transportSender.enqueue(this.transportTelemetry.drain()),this.enqueueRTCEvents({timing:{runtime:this.runtime.counters}})}playWhisper(h){if(h.byteLength<17||h.byteLength>176||this.whisperEpoch===null||!this.context)return;let t=new DataView(h);if(t.getUint32(0,!0)!==827805761||t.getUint32(4,!0)!==this.whisperEpoch)return;let m=t.getFloat64(8,!0);if(!Number.isFinite(m))return;let s=performance.timeOrigin+performance.now()-m;this.whisperBase=this.whisperBase===null?s:Math.min(this.whisperBase,s);let _=Math.max(0,s-this.whisperBase);if(_>200)return;let u=new Float32Array(h.byteLength-16);for(let i=0;i<u.length;i++){let M=~t.getUint8(i+16)&255,a=((M&15)<<3)+132<<(M>>4&7);u[i]=(M&128?132-a:a-132)/32768}let f=this.context.sampleRate===8000?u:this.whisperResampler.process(u,8000,this.context.sampleRate);this.whisper?.port.postMessage({type:"whisper",frame:f,received_audio_ms:this.context.currentTime*1000-_},[f.buffer])}disconnected(h,t){if(!this.current(h)||this.recovering)return;this.recovering=!0;let m=xh("automatic_retry");this.recoveryContext=m,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"started",code:t,...m}),this.cleanup(),D(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));let s=++this.recoveryGeneration,_=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===s;this.recoveryExpiry=setTimeout(()=>{if(!_())return;this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"timed_out",...this.recoveryContext}),++this.recoveryGeneration,this.recovering=!1,clearTimeout(this.retry),this.cancelSetup?.(),this.recoveryRequest?.abort(),this.cleanup(),D(()=>this.callbacks.onState?.("error","Network unavailable; waiting to restore audio to the existing call.",{recoverable:!0}))},30000);let u=async()=>{if(!_())return;let f={...m,attempt_id:Ch("attempt")};this.recoveryContext=f,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"attempt_started",code:t,...f});try{if(!this.callbacks.refreshMediaURL)throw Error("Fresh media authorization is required");this.recoveryRequest=new AbortController;let i=await this.callbacks.refreshMediaURL(f,this.recoveryRequest.signal);if(!_())return;if(await this.connect(i),!_())return;if(this.pc?.connectionState!=="connected")throw Error("WebRTC disconnected during recovery");clearTimeout(this.recoveryExpiry),this.recovering=!1,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected",...f})}catch(i){if(!_())return;this.cleanup();let M=th(i);if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:zh(i)?"revoked":"attempt_failed",...f,status:M.status,code:M.code}),zh(i)){clearTimeout(this.recoveryExpiry),this.recovering=!1,D(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return}this.retry=setTimeout(()=>void u(),1000)}};u()}recordSessionEvent(h){try{h={...this.callbacks.sessionDiagnostics?.(),...h}}catch{}h=mh(h),this.events=[...this.events,h].slice(-100),this.enqueueRTCEvents({session_events:[h]}),D(()=>this.callbacks.onSessionEvent?.(h))}setMuted(h){if(this.muted!==h)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:h?"muted":"unmuted"});if(this.muted=h,this.gate(),h)this.send({type:"interrupt"})}sendDTMF(h){if(!/^[0-9*#]+$/.test(h))throw Error("Invalid DTMF digits");this.send({type:"dtmf",digits:h})}setOutputVolume(h){if(!Number.isFinite(h)||h<0||h>1)throw RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=h;if(this.options)this.options.outputVolume=h}startRingback(h){if(this.stopRingback(),this.context&&this.speaker)this.ringback=kh(this.context,this.speaker,Kh(h))}stopRingback(){this.ringback?.(),this.ringback=void 0}cleanup(){if(this.runtime.stopEnvironment(),this.transportSender.stop(),this.nativeCounts){for(let s of["packetsLost","packetsDiscarded","concealedMs"])this.completedCounts[s]+=this.nativeCounts[s]??0;this.nativeCounts=void 0}++this.generation,this.ready=this.peer=!1,this.stopRingback(),clearInterval(this.timer),this.timer=void 0;let h=this.socket;if(this.socket=void 0,h){if(h.readyState===WebSocket.OPEN)try{h.send(JSON.stringify({type:"media.shutdown",reason:this.stopped?"session_cleanup":"audio_error"}))}catch{}h.onclose=h.onmessage=h.onerror=null,h.close(1000,"client_shutdown")}this.ice?.stop(),this.ice=void 0,this.playbackReceiver=void 0,this.uploadSender=void 0;let t=this.pc;if(this.pc=void 0,t)t.onconnectionstatechange=t.ontrack=null,t.close();if(this.remoteAudio)this.remoteAudio.pause(),this.remoteAudio.srcObject=null,this.remoteAudio=void 0;this.stream?.getTracks().forEach((s)=>{s.onended=null,s.stop()}),this.stream=void 0,this.destination?.stream.getTracks().forEach((s)=>s.stop()),this.destination=void 0,this.capture?.disconnect(),this.capture=void 0,this.whisper?.disconnect(),this.whisper=void 0,this.whisperEpoch=null,this.whisperBase=null,this.whisperResampler=new Ph;let m=this.context;if(this.context=void 0,m)m.onstatechange=null,m.close().catch(()=>{});this.speaker=void 0,this.speakerMeter=void 0}stop(){if(this.stopped)return;if(this.recovering)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"cancelled",...this.recoveryContext});this.stopped=!0,this.recoveryRequest?.abort(),++this.recoveryGeneration,clearTimeout(this.recoveryExpiry),clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup()}}function Ft(h,t,m){let s,_=!1,u=!1,f=[];return{async start(i,M){let a=Ih(M.mediaTransport),y=async(S)=>{if(_)throw Error("Audio session cancelled");let g=S();s=g,g.setMuted(u);try{for(let A of f.splice(0))try{g.recordSessionEvent?.(A)}catch{}if(_)throw Error("Audio session cancelled");if(await g.start(i,M),_)throw Error("Audio session cancelled")}catch(A){if(g.stop(),s===g)s=void 0;throw A}};if(a==="websocket"){await y(t);return}try{await y(m)}catch(S){if(a!=="auto"||_||M.iceTransportPolicy==="relay"||zh(S))throw S;D(()=>h.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"})),await y(t)}},stop(){_=!0,f.splice(0),s?.stop(),s=void 0},setMuted(i){u=i,s?.setMuted(i)},setOutputVolume(i){s?.setOutputVolume(i)},sendDTMF(i){s?.sendDTMF(i)},recordSessionEvent(i){if(s)s.recordSessionEvent?.(i);else if(!_){if(f.push(mh(i)),f.length>20)f.shift()}},startRingback(i){s?.startRingback?.(i)},stopRingback(){s?.stopRingback?.()}}}async function Tt(){return(await navigator.mediaDevices.enumerateDevices()).filter((t)=>t.kind==="audioinput").map((t,m)=>({deviceId:t.deviceId,label:t.label||`Microphone ${m+1}`}))}function Ot(h,t){let m=new tt(h,!1),s,_=!1,u,f=()=>{return _=!0,u??=(async()=>{try{await m.cancel()}finally{s?.dispose(),s=void 0}})()};return{async start(i={}){if(_)throw Error("Microphone preview is already used");_=!0;try{if(s=t?await wh(t,!0):Yh(!0),u)throw s.dispose(),s=void 0,Error("Microphone preview was cancelled");await m.start(s.urls[0],{...sh,...i})}catch(M){throw await f(),M}},stop:f}}function dt(h){return{async preflight(t,m){await lt(h,t,m)},create(t){let m=new mt(t),s,_=!1,u=()=>{_=!0;try{m.stop()}finally{s?.dispose(),s=void 0}};return{async start(f,i){try{if(s=await c("audio_assets",(M)=>m.recordSessionEvent(M),()=>h?wh(h):Promise.resolve(Yh())),_)throw Error("Audio session was cancelled");await m.start(f,s.urls[0],s.urls[1],i)}catch(M){throw u(),M}},stop:u,recordSessionEvent:(f)=>m.recordSessionEvent(f),setMuted:(f)=>m.setMuted(f),sendDTMF:(f)=>m.sendDTMF(f),setOutputVolume:(f)=>m.setOutputVolume(f),startRingback:(f)=>m.startRingback(f),stopRingback:()=>m.stopRingback()}}}}function Qt(h){let t=dt(h);return{preflight:(m,s)=>t.preflight(m,s),prepare:(m,s)=>Gt(h,m,s),create(m){return Ft(m,()=>t.create(m),()=>{let s=new Mt(m),_,u=!1;return{async start(f,i){try{if(_=await c("audio_assets",(M)=>s.recordSessionEvent(M),()=>h?wh(h,!0):Promise.resolve(Yh(!0))),u)throw Error("Audio session cancelled");await s.start(f,i,_.urls[0])}catch(M){throw s.stop(),_?.dispose(),_=void 0,M}},stop(){u=!0,s.stop(),_?.dispose(),_=void 0},setMuted:(f)=>s.setMuted(f),sendDTMF:(f)=>s.sendDTMF(f),setOutputVolume:(f)=>s.setOutputVolume(f),recordSessionEvent:(f)=>s.recordSessionEvent(f),startRingback:(f)=>s.startRingback(f),stopRingback:()=>s.stopRingback()}})}}}async function Gt(h,t,m){if(!h||typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin)return;await c("audio_assets_prepare",m,async()=>{(await wh(h,t.mediaTransport==="webrtc"||t.mediaTransport==="auto")).dispose()})}async function lt(h,t,m){await Promise.all([c("microphone_preflight",m,async()=>{(await navigator.mediaDevices.getUserMedia({audio:Sh(t)})).getTracks().forEach((_)=>_.stop())}),Gt(h,t,m)])}function Bt(h,t){return{create(m){let s={...sh,...t},_=v(s),u=new Lh(s),f=[],i,M,a,y,S=!1,g=!1,A=!1,b=!1,w=!1,C=!1,P=0,q,R,J,Q,B,X,gh=!1,fh,uh,bh=s.outputVolume??1,Mh={},e=32000,x,T=[],F,O,V,H,Z=[],r=(E)=>{if(i?.readyState===WebSocket.OPEN&&i.bufferedAmount<65536)i.send(JSON.stringify(E));else throw Error("Listener connection unavailable")},n=()=>{if(S)throw Error("Listening cancelled")},U=()=>{let E=++P;if(w=C=!1,F?.(E,Error("Coaching cancelled")),H?.(),O=void 0,fh)clearInterval(fh);if(fh=void 0,x?.remove("coach"),B)B.onconnectionstatechange=null,B.close(),B=void 0;if(R?.disconnect(),R=void 0,J?.disconnect(),J=void 0,Q?.disconnect(),Q=void 0,q?.getTracks().forEach((W)=>{W.onended=null,W.stop()}),q=void 0,b&&g&&i?.readyState===WebSocket.OPEN)try{r({type:"coach.stop",generation:E})}catch{}m.onTalking?.(!1)},d=()=>U(),l=()=>{if(document.visibilityState!=="visible")U()},ih=()=>{if(S)return;if(U(),S=!0,x?.stop(),x=void 0,V?.(),window.removeEventListener("blur",d),document.removeEventListener("visibilitychange",l),uh)clearInterval(uh);if(uh=void 0,i)i.onmessage=i.onclose=i.onerror=null,i.close(1000,"listener_shutdown");if(M)M.ontrack=M.onconnectionstatechange=null,M.close();for(let E of Z)E.pause(),E.srcObject=null;if(y?.disconnect(),a)a.onstatechange=null,a.close().catch(()=>{});X?.dispose(),X=void 0},o=(E)=>{if(!S)ih(),m.onClose(E)},Y=async(E,W)=>{if(await E.setLocalDescription(await E.createOffer()),await new Promise(($,G)=>{if(E.iceGatheringState==="complete"){$();return}let N=setTimeout(()=>{E.onicegatheringstatechange=null,G(Error("ICE gathering timed out"))},7000);E.onicegatheringstatechange=()=>{if(E.iceGatheringState==="complete"||!W())clearTimeout(N),E.onicegatheringstatechange=null,W()?$():G(Error("Coaching cancelled"))}}),!W())throw Error("Listening cancelled");return E.localDescription.sdp};return{async start(E,W){if(b=W?.coaching===!0,n(),typeof RTCPeerConnection!=="function")throw Error("WebRTC audio unavailable");try{if(a=new AudioContext({latencyHint:"interactive"}),await a.resume(),n(),s.outputDeviceId&&"setSinkId"in a)await a.setSinkId(s.outputDeviceId);if(n(),y=a.createGain(),y.gain.value=bh,y.connect(a.destination),a.onstatechange=()=>{if(a?.state==="suspended"||a?.state==="interrupted")o("listener_audio_paused")},await new Promise((N,I)=>{let p=!1,K=!1,Ah=!1,L=(eh)=>{if(p)return;p=!0,clearTimeout(ph),V=void 0,eh?I(Ah?Object.assign(eh,{code:"turn_relay_required"}):eh):N()},ph=setTimeout(()=>L(Error("WebRTC listener timed out")),18000),Jh=()=>{if(A&&K&&!S&&!g)g=!0,L(),m.onReady("webrtc")};V=()=>L(Error("Listening cancelled")),i=new WebSocket(ft(E,s.webrtcFec,s)),i.onmessage=({data:eh})=>{if(S)return;if(typeof eh!=="string"){L(Error("Invalid WebRTC listener frame")),o("listener_protocol_error");return}let z;try{z=JSON.parse(eh)}catch{L(Error("Invalid listener control")),o("listener_protocol_error");return}if(z.type==="webrtc.error"){L(Object.assign(Error("Required TURN relay is unavailable"),{code:z.code}));return}if(x?.handle(z))return;if(z.type==="webrtc.config"){if(Ah=z.ice_transport_policy==="relay",M||z.listener!==!0){L(Error("Invalid WebRTC listener configuration"));return}try{Mh=Wh(z)}catch(j){L(j);return}e=z.bitrate??32000,M=new RTCPeerConnection(Mh);let hh=M;x=new Nh(r,()=>!S,(j,Gh)=>{if(T.push(mh({...Gh,timestamp:new Date().toISOString(),action:"ice_restart",outcome:j})),T.length>50)T.shift()},(j)=>{Mh=j}),x.register("main",hh);for(let j=0;j<2;j++)hh.addTransceiver("audio",{direction:"recvonly"});hh.ontrack=(j)=>{if(S||!a||!y)return;f.push(j.receiver);let Gh=hh.getTransceivers().findIndex((Rh)=>Rh.receiver===j.receiver);if(Gh<0||Gh>1){o("listener_protocol_error");return}if("jitterBufferTarget"in j.receiver)try{j.receiver.jitterBufferTarget=_.initialTargetMs}catch{}let qt=new MediaStream([j.track]),Zh=new Audio;Zh.muted=!0,Zh.srcObject=qt,Z.push(Zh),Zh.play().catch(()=>{});let gt=a.createMediaStreamSource(qt);if(t.stereo){let Rh=a.createStereoPanner();Rh.pan.value=Gh===0?-1:1,gt.connect(Rh).connect(y)}else{let Rh=a.createGain();Rh.gain.value=0.5,gt.connect(Rh).connect(y)}},hh.onconnectionstatechange=()=>{if(S)return;if(hh.connectionState==="connected")A=!0,Jh();if(hh.connectionState==="failed"||hh.connectionState==="closed"){if(L(Error("WebRTC listener connection failed")),p&&g)o("listener_network_error")}},Y(hh,()=>!S).then((j)=>r({type:"webrtc.offer",sdp:j,ice_version:1})).catch((j)=>L(j));return}if(z.type==="webrtc.answer"){M?.setRemoteDescription({type:"answer",sdp:z.sdp}).catch((hh)=>L(hh));return}if(z.type==="listener.ready"){if(b&&z.coaching!==!0){L(Error("Coaching not authorized"));return}K=!0,Jh();return}if(z.type==="coach.started"){F?.(z.generation);return}if(z.type==="coach.rejected"){F?.(P,Error(z.detail||"Coaching unavailable"));return}if(z.type==="coach.stopped"&&(z.generation===void 0||z.generation===P)){U();return}if(z.type==="coach.webrtc.answer"){O?.(z.sdp,z.generation);return}},i.onerror=()=>{if(L(Error("Listener connection failed")),g)o("listener_network_error")},i.onclose=(eh)=>{if(L(Error(eh.reason||"Listener disconnected")),g)o(eh.reason||"listener_disconnected")}}),n(),b)window.addEventListener("blur",d),document.addEventListener("visibilitychange",l);let $,G=!1;uh=setInterval(()=>{if(S||!M||G)return;G=!0,M.getStats().then((N)=>{if(S)return;let I=ut(N,$);$=I.previous,u.observe({...I.metrics,receiver_jitter:I.webrtc.jitterMs/1000});for(let p of f)if("jitterBufferTarget"in p)try{p.jitterBufferTarget=u.target}catch{}m.onDiagnostics?.({session_events:T.slice(),media_transport:"webrtc",dropped_ms:[],played_ms:[],max_queue_ms:I.queueMs,max_late_ms:0,sequence_gaps:[],network_excess_ms:0,network_dropped_ms:[],webrtc:I.webrtc})}).catch(()=>{}).finally(()=>{G=!1})},1000)}catch($){throw ih(),$}},async startTalking(){if(n(),!b||!g||!a)throw Error("Coaching session not ready");if(w)return;w=!0;let E=++P,W=()=>!S&&w&&P===E;try{let $=await navigator.mediaDevices.getUserMedia({audio:Sh(s)});if(!W()){$.getTracks().forEach((N)=>N.stop());return}if(q=$,q.getTracks().forEach((N)=>{N.onended=()=>U()}),!X){let N=await wh(h,!0);if(!W()){N.dispose();return}X=N}if(!gh)await a.audioWorklet.addModule(X.urls[0]),gh=!0;if(!W())return;if(await new Promise((N,I)=>{let p=setTimeout(()=>{F=void 0,I(Error("Coaching activation timed out"))},3000);F=(K,Ah)=>{if(Ah||K===E)clearTimeout(p),F=void 0,Ah?I(Ah):N()},r({type:"coach.start",generation:E})}),!W())return;fh=setInterval(()=>{if(W())try{r({type:"coach.keepalive",generation:E})}catch{U()}},1000),R=new AudioWorkletNode(a,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:s.inputGainDB,highpassFilter:s.highpassFilter}}),Q=a.createMediaStreamDestination(),Q.channelCount=1,J=a.createMediaStreamSource($),J.connect(R).connect(Q);let G=new RTCPeerConnection(Mh);if(B=G,x?.register("coach",G,W,E),G.addTransceiver(Q.stream.getAudioTracks()[0],{direction:"sendonly",streams:[Q.stream],sendEncodings:[{maxBitrate:e}]}),await new Promise((N,I)=>{let p=!1,K=(L)=>{if(p)return;p=!0,clearTimeout(Ah),H=void 0,O=void 0,L?I(L):N()},Ah=setTimeout(()=>K(Error("Coaching WebRTC timed out")),18000);H=()=>K(Error("Coaching cancelled")),O=(L,ph)=>{if(ph!==E||!W())return;G.setRemoteDescription({type:"answer",sdp:L}).catch((Jh)=>K(Jh))},G.onconnectionstatechange=()=>{if(!W())return;if(G.connectionState==="connected")K();if(G.connectionState==="failed"||G.connectionState==="closed")K(Error("Coaching media disconnected")),U()},Y(G,W).then((L)=>{if(W())r({type:"coach.webrtc.offer",generation:E,sdp:L})}).catch((L)=>K(L))}),W())C=!0,m.onTalking?.(!0)}catch($){if(W())throw U(),$}},stopTalking:U,stop:ih,setOutputVolume(E){if(!Number.isFinite(E)||E<0||E>1)throw RangeError("Listener volume must be 0–1");if(bh=E,y)y.gain.value=E}}}}}function Qh(h={},t={}){let m=["turnTransport","iceTransportPolicy","turnTLSPort","mediaTransport","webrtcFec","inputDeviceId","outputDeviceId","outputVolume","echoCancellation","noiseSuppression","autoGainControl","inputGainDB","highpassFilter","playbackTargetMs","playbackMinMs","playbackMaxMs","playbackAdaptive","stereo"],s={...sh};for(let _ of[h,t])for(let u of m){let f=_[u];if(f!==void 0)s[u]=f}if(v(s),s.mediaTransport=Ih(s.mediaTransport),s.webrtcFec??=!0,s.outputVolume!==void 0&&(!Number.isFinite(s.outputVolume)||s.outputVolume<0||s.outputVolume>1))throw RangeError("Listener volume must be 0–1");return s}function pt(h,t,m,s,_=!1){let u,f=!1,i=!1,M,a={...h,onReady(y){i=!0,h.onReady(y)}};return{async start(y,S){if(f)throw Error("Listening cancelled");u=t==="websocket"?m(a):s(a);try{await u.start(y,S)}catch(g){if(u.stop(),f||i||t!=="auto"||zh(g)||_)throw g;u=m(a),await u.start(y,S)}if(f)u.stop();else if(M!==void 0)u.setOutputVolume(M)},async startTalking(){if(f||!u?.startTalking)throw Error("Coaching session not ready");await u.startTalking()},stopTalking(){u?.stopTalking?.()},stop(){f=!0,u?.stop()},setOutputVolume(y){if(!Number.isFinite(y)||y<0||y>1)throw RangeError("Listener volume must be 0–1");M=y,u?.setOutputVolume(y)}}}function it(h,t={}){let m=Qh({},t),s=Ih(m.mediaTransport),_=vt(h,m),u=Bt(h,m);return{create(f){return pt(f,s,(i)=>_.create(i),(i)=>u.create(i),m.iceTransportPolicy==="relay")}}}function rt(h){if(h.byteLength<26||h.byteLength>984||h.byteLength%2)throw Error("Invalid listener audio frame");let t=new DataView(h),m=t.getUint32(4,!0);if(t.getUint32(0,!0)!==827085889||m>1)throw Error("Invalid listener audio protocol");let s=Number(t.getBigUint64(8,!0)),_=Number(t.getBigUint64(16,!0));if(!Number.isSafeInteger(s)||!Number.isSafeInteger(_))throw Error("Invalid listener audio timing");let u=new Float32Array((h.byteLength-24)/2);for(let f=0;f<u.length;f++)u[f]=t.getInt16(24+f*2,!0)/32768;return{direction:m,sequence:s,timestampMS:_,frame:u}}function nt(h,t,m,s){if(h.length<1||h.length>480||!Number.isFinite(s)||s<0||!Number.isInteger(t)||t<1||t>4294967295)throw Error("Invalid coaching capture");let _=new ArrayBuffer(24+h.length*2),u=new DataView(_);u.setUint32(0,826496833,!0),u.setUint32(4,t,!0),u.setUint32(8,m>>>0,!0),u.setFloat64(16,s,!0);for(let f=0;f<h.length;f++){let i=Math.max(-1,Math.min(1,h[f]));u.setInt16(24+f*2,i<0?i*32768:i*32767,!0)}return _}function vt(h,t={}){return{create(m){let s,_,u,f,i=!1,M=[],a=!1,y=0,S=!1,g=!1,A,b,w,C,P,q,R,J=!1,Q,B,X=0,gh=!1,fh=[0,0],uh=[0,0],bh=[0,0],Mh=[0,0],e=[void 0,void 0],x=[new Ph,new Ph],T=t.outputVolume??1,F,O=()=>{let U=++y;if(S=!1,g=!1,R?.(U,Error("Coaching cancelled")),q)clearInterval(q);if(q=void 0,P?.port1.close(),P?.port2.close(),P=void 0,b?.disconnect(),b=void 0,w?.disconnect(),w=void 0,C?.disconnect(),C=void 0,A?.getTracks().forEach((d)=>{d.onended=null,d.stop()}),A=void 0,a&&gh&&f?.readyState===WebSocket.OPEN)f.send(JSON.stringify({type:"coach.stop",generation:U}));m.onTalking?.(!1)},V=()=>O(),H=()=>{if(document.visibilityState!=="visible")O()},Z=()=>{if(i)return;if(O(),i=!0,F?.(),window.removeEventListener("blur",V),document.removeEventListener("visibilitychange",H),f)f.onmessage=f.onclose=f.onerror=null,f.close(1000,"listener_shutdown");if(_?.disconnect(),u?.disconnect(),s)s.onstatechange=null,s.close().catch(()=>{});for(let U of M.splice(0))U.dispose()},r=(U)=>{if(!i)Z(),m.onClose(U)},n=()=>{if(i)throw Error("Listening cancelled")};return{async start(U,d){a=d?.coaching===!0,n();try{if(s=new AudioContext({latencyHint:"interactive"}),s.state==="suspended")await s.resume();if(n(),t.outputDeviceId&&"setSinkId"in s)await s.setSinkId(t.outputDeviceId);n();let l=await Xh(h,"listener");if(i)l.dispose(),n();if(M.push(l),await s.audioWorklet.addModule(l.urls[0]),n(),_=new AudioWorkletNode(s,"telephony-listener",{numberOfInputs:0,outputChannelCount:[t.stereo?2:1],processorOptions:{stereo:t.stereo,...v(t)}}),u=s.createGain(),u.gain.value=T,_.connect(u).connect(s.destination),_.onprocessorerror=()=>r("listener_audio_error"),_.port.onmessage=({data:ih})=>{if(ih?.type==="listener.diagnostics"&&!i)try{m.onDiagnostics?.({...ih,media_transport:"websocket",context_state:s?.state,context_rate:s?.sampleRate,context_time_ms:(s?.currentTime??0)*1000,received_ms:[...uh],scheduled_ms:[...bh],sequence_gaps:[...Mh],network_excess_ms:X,network_dropped_ms:[...fh]})}catch{}},s.onstatechange=()=>{if(s?.state==="suspended"||s?.state==="interrupted")r("listener_audio_paused")},await new Promise((ih,o)=>{let Y=!1,E=setTimeout(()=>{W(Error("Listener connection timed out")),r("listener_network_error")},1e4),W=($)=>{if(Y)return;Y=!0,clearTimeout(E),F=void 0,$?o($):ih()};F=()=>W(Error("Listening cancelled")),f=new WebSocket(U),f.binaryType="arraybuffer",f.onmessage=({data:$})=>{if(i||!s||!_)return;try{if(typeof $==="string"){let K=JSON.parse($);if(K.type==="listener.ready"){if(a&&K.coaching!==!0)throw Error("Coaching not authorized");gh=!0,W(),m.onReady("websocket")}else if(K.type==="coach.started")R?.(K.generation);else if(K.type==="coach.rejected")R?.(y,Error(K.detail||"Coaching unavailable"));else if(K.type==="coach.stopped"&&(K.generation===void 0||K.generation===y))O();return}if(!gh||!($ instanceof ArrayBuffer))return;let G=rt($),N=G.direction;if(uh[N]+=G.frame.length*1000/24000,e[N]!==void 0&&G.sequence<e[N])return;if(e[N]!==void 0&&G.sequence>e[N])Mh[N]+=G.sequence-e[N];e[N]=G.sequence+1;let I=performance.now()-G.timestampMS;if(B=Math.min(B??I,I),X=Math.max(0,I-B),X>200){fh[N]+=G.frame.length*1000/24000;return}Q??=s.currentTime*1000+(t.playbackTargetMs??60)-G.timestampMS;let p=x[N].process(G.frame,24000,s.sampleRate);bh[N]+=G.frame.length*1000/24000,_.port.postMessage({direction:N,frame:p,playAtMS:G.timestampMS+Q},[p.buffer])}catch{W(Error("Invalid listener media")),r("listener_protocol_error")}},f.onerror=()=>{W(Error("Listener connection failed")),r("listener_network_error")},f.onclose=($)=>{W(Error($.reason||"Listener disconnected")),r($.reason||"listener_disconnected")}}),n(),a)window.addEventListener("blur",V),document.addEventListener("visibilitychange",H)}catch(l){throw Z(),l}},async startTalking(){if(n(),!a||!gh||!s||!f||f.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(S)return;S=!0;let U=++y,d=()=>!i&&S&&U===y;try{let l=await navigator.mediaDevices.getUserMedia({audio:Sh({...sh,...t})});if(!d()){l.getTracks().forEach((Y)=>Y.stop());return}if(A=l,A.getAudioTracks().forEach((Y)=>{Y.onended=()=>O()}),!J){let Y=await wh(h,!0);if(!d()){Y.dispose();return}M.push(Y),await s.audioWorklet.addModule(Y.urls[0]),J=!0}if(!d())return;if(await new Promise((Y,E)=>{let W=setTimeout(()=>{R=void 0,E(Error("Coaching activation timed out"))},3000);R=($,G)=>{if(G||$===U)clearTimeout(W),R=void 0,G?E(G):Y()},f.send(JSON.stringify({type:"coach.start",generation:U}))}),!d())return;b=new AudioWorkletNode(s,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:t.inputGainDB??0,highpassFilter:t.highpassFilter!==!1}}),w=s.createMediaStreamSource(l),C=s.createGain(),C.gain.value=0,w.connect(b).connect(C).connect(s.destination),P=new MessageChannel;let ih=0,o=new Ph;P.port1.onmessage=({data:Y})=>{if(!d()||!g||Y?.type!=="capture"||!(Y.frame instanceof Float32Array)||!s||!f)return;if(!Number.isFinite(Y.timestamp_ms)||s.currentTime*1000-Y.timestamp_ms>150||f.bufferedAmount>2880||f.readyState!==WebSocket.OPEN)return;let E=o.process(Y.frame,Y.sample_rate,24000);if(E.length>0&&E.length<=480)f.send(nt(E,U,ih++,Y.timestamp_ms))},P.port1.start(),b.port.postMessage({type:"transport",port:P.port2},[P.port2]),g=!0,m.onTalking?.(!0),q=setInterval(()=>{if(d()&&f?.readyState===WebSocket.OPEN)f.send(JSON.stringify({type:"coach.keepalive",generation:U}))},1000)}catch(l){if(d())throw O(),l}},stopTalking:O,stop:Z,setOutputVolume(U){if(!Number.isFinite(U)||U<0||U>1)throw RangeError("Listener volume must be 0–1");if(T=U,u)u.gain.value=U}}}}}class at{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;requestAbort;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;audioOptions;constructor(h,t={}){this.client=h;this.options=t;this.audioOptions=Qh(t.audio,t),this.runtime=t.runtime??it(h.app,this.audioOptions)}setAudioOptions(h){let t=Qh({},h);this.audioOptions=t,this.runtime=this.options.runtime??it(this.client.app,t)}getAudioOptions=()=>Object.freeze({...this.audioOptions});getSnapshot=()=>this.snapshot;subscribe=(h)=>{return this.observers.add(h),()=>{this.observers.delete(h)}};update(h){this.snapshot=Object.freeze({...this.snapshot,...h});for(let t of this.observers)try{t(this.snapshot)}catch{}}async listen(h){return this.begin(h,!1)}async coach(h){return this.begin(h,!0)}async begin(h,t){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=t,this.retryUntil=0,this.retryDelay=500;let m=++this.generation;return this.update({state:"connecting",callId:h,detail:void 0,coaching:t,talking:!1,mediaTransport:void 0}),this.connect(h,m)}async connect(h,t){this.requestAbort?.abort(),this.requestAbort=new AbortController;let m,s=++this.attempt,_=()=>t===this.generation&&s===this.attempt&&!this.disposed;try{if(m=this.coaching?await this.client.coachSession(h):await this.client.listenSession(h,this.requestAbort?.signal),!_()){await this.client.stopListening(m).catch(()=>{});return}this.session=m;let u=this.runtime.create({onReady:(f)=>{if(_())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0,mediaTransport:f})},onClose:(f)=>{if(_())this.disconnected(f,h,t)},onTalking:(f,i)=>{if(_())this.update({talking:f,detail:i})},onDiagnostics:(f)=>{if(_())try{this.options.onDiagnostics?.(f)}catch{}}});if(this.audio=u,this.lease=new Uh(m.lease_seconds,()=>this.client.renewListening(m),(f)=>{if(_())this.disconnected(f==="revoked"?"access_revoked":"media_disconnected",h,t,f==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,m.lease_started_ms),await u.start(this.client.listenerMediaURL(m,this.audioOptions.mediaTransport),{coaching:m.coaching===!0}),!_())u.stop()}catch(u){if(!_()||m&&this.session!==m)return;let f=u?.status,i;try{i=JSON.parse(u.body??"{}").code}catch{}let M=i==="call_ended"?"call_ended":f===401||f===403||f===404?"access_revoked":"listener_disconnected";throw this.disconnected(M,h,t,String(u)),u}}cleanup(){this.requestAbort?.abort(),this.requestAbort=void 0,this.lease?.stop(),this.lease=void 0;let h=this.audio;this.audio=void 0,h?.stop();let t=this.session;return this.session=void 0,t?this.client.stopListening(t).catch(()=>{}):Promise.resolve()}disconnected(h,t,m,s=h){if(m!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let _=h==="call_ended",u=h==="access_revoked",f=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(h);if(!this.coaching&&!_&&!u&&f&&this.options.reconnect!==!1){this.retryUntil||=Date.now()+30000;{this.update({state:"reconnecting",detail:s,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,m===this.generation&&!this.disposed)this.connect(t,m).catch(()=>{})},Date.now()<this.retryUntil?this.retryDelay:15000),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:_?"call_ended":u?"access_revoked":"disconnected",detail:s,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(h){this.audio?.setOutputVolume(h)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let h=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1,mediaTransport:void 0}),await h}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}var ot={set:(h,t)=>setTimeout(h,t),clear:(h)=>clearTimeout(h)};class yt{run;permanent;ended;event;clock;stopped=!1;active;timer;attempt=0;online=()=>this.wake();constructor(h,t,m,s,_=ot){this.run=h;this.permanent=t;this.ended=m;this.event=s;this.clock=_;globalThis.addEventListener?.("online",this.online),this.schedule(1000)}emit(h){try{this.event?.(h)}catch{}}schedule(h){if(!this.stopped)this.clock.clear(this.timer),this.timer=this.clock.set(()=>void this.tick(),h)}wake(){if(!this.stopped&&!this.active)this.schedule(0)}async tick(){if(this.stopped||this.active)return;let h=new AbortController;this.active=h,this.attempt++,this.emit("attempt_started");let t;try{if(await Promise.race([this.run(h.signal),new Promise((m,s)=>{t=this.clock.set(()=>{h.abort(),s(Error("Audio recovery request timed out"))},30000)})]),this.stopped)return;this.emit("connected"),this.stop()}catch(m){if(this.stopped)return;if(this.permanent(m)){this.emit("revoked"),this.stop(),this.ended(m);return}this.emit("waiting_for_network"),this.schedule(Math.min(15000,1000*2**Math.min(4,this.attempt)))}finally{if(this.clock.clear(t),this.active===h)this.active=void 0}}stop(){if(this.stopped)return;this.stopped=!0,this.clock.clear(this.timer),this.active?.abort(),globalThis.removeEventListener?.("online",this.online)}}function Ut(h){if(!h)return"placing";if(dh(h))return"ended";if(h==="ringing")return"ringing";if(h==="answered"||h==="in-progress")return"connected";return"placing"}var _h=(h)=>h instanceof Error?h.message:String(h);class St{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;hasStatusPush=!1;automaticRecovery;hasLiveAudio=!1;uncertainAttachment;scheduleStatusPoll;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(h,t={}){this.client=h;this.options=t;if(this.runtime=t.audioRuntime??Qt(h.app),this.audioOptions={...sh,...t.audio,...t.mediaTransport?{mediaTransport:t.mediaTransport}:{}},v(this.audioOptions),this.interval=t.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}createCallListener(h={}){return this.assertOpen(),this.client.createCallListener({...this.audioOptions,...h.audio,...h})}getSnapshot=()=>this.snapshot;subscribe=(h)=>{return this.assertOpen(),this.listeners.add(h),()=>{this.listeners.delete(h)}};update(h){let t={...this.snapshot,...h};if(t.callId&&Ut(t.carrierStatus)==="connected")t.phase=t.audioState==="live"?"connected":"placing";this.snapshot=Object.freeze(t);for(let m of this.listeners)try{m(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(h){if(this.disposed||h!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.automaticRecovery?.stop(),this.automaticRecovery=void 0,this.uncertainAttachment=void 0,this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(h){return!this.disposed&&h===this.generation}finish(h){if(this.current(h))this.update({busy:!1})}cancellable(h,t){let m=this.cancellation.signal;return new Promise((s,_)=>{let u=()=>{m.removeEventListener("abort",u),_(Error("Softphone operation cancelled"))};if(m.addEventListener("abort",u,{once:!0}),h.then(s,_).finally(()=>m.removeEventListener("abort",u)),!this.current(t)||m.aborted)u()})}begin(h){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||h&&this.session)throw Error("Softphone already has an active operation or call");let t=this.invalidate();return this.update({busy:!0,detail:void 0}),t}async prepareAudio(h,t){let m=[],s=(_)=>{if(this.current(h)&&!this.cancellation.signal.aborted)m.push(mh(_))};try{return await this.cancellable(c(t?"audio_preflight":"audio_prepare",s,()=>t?this.runtime.preflight(this.audioOptions,s):this.runtime.prepare?.(this.audioOptions,s)??Promise.resolve()),h),this.assertCurrent(h),m}catch(_){if(this.current(h))for(let u of m)this.reportSessionEvent(u);throw _}}async dial(h){let t=this.begin(!0),m,s=!1;try{this.assertCurrent(t);let _={to:h.to.trim(),from:h.from?.trim(),timeout_sec:h.timeout_sec,recording:h.recording},u=JSON.stringify(_);if(!this.intent||this.intent.value!==u||h.idempotency_key&&h.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:u,request:{..._,idempotency_key:h.idempotency_key||crypto.randomUUID()}};let f=await this.prepareAudio(t,!0);return this.assertCurrent(t),m=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(t),s=!0,this.outbound=!0,await this.attachAudio(m,t,f),m.call_id}catch(_){if(m&&(this.current(t)||!s||this.disposed))await this.recoverStartup(m,t,()=>this.client.hangup(m.call_id));if(this.current(t))this.update({detail:_h(_)});throw _}finally{this.finish(t)}}async answer(h,t={}){let m=this.begin(!0),s,_=!1;try{this.assertCurrent(m);let u=await this.prepareAudio(m,!0);s=await this.client.answer(h,t),this.assertCurrent(m),_=!0,this.outbound=!1,await this.attachAudio(s,m,u)}catch(u){if(s&&(this.current(m)||!_||this.disposed))await this.recoverStartup(s,m,()=>this.client.release(s));if(this.current(m))this.update({detail:_h(u)});throw u}finally{this.finish(m)}}async recoverStartup(h,t,m){try{if(await m(),this.current(t))this.clearCall()}catch{if(this.current(t))this.session=h,this.update({callId:h.call_id,audioState:"error"}),this.startPolling()}}async join(h){await this.answer(h,{rejoin:!0})}async attach(h,t="attach"){await this.acquireSession(h,!1,t)}async takeover(h){await this.acquireSession(h,!0)}async acquireSession(h,t,m="takeover"){let s=this.begin(!0);try{let _=await this.prepareAudio(s,!0);this.assertCurrent(s);let u=xh(t?"takeover":m),f=await(t?this.client.takeover(h,u):this.client.attach(h,u));this.assertCurrent(s),await this.attachAudio(f,s,_),await this.reconcileAttachedCall(f,s)}catch(_){if(this.current(s))this.update({detail:_h(_)});throw _}finally{this.finish(s)}}async reconnect(h,t="manual_reconnect"){if(!this.session)throw Error("No call to reconnect");let m={...this.audioOptions,...h};v(m);let s=this.begin(!1);this.audioOptions=m;let _=this.session.call_id,u=xh(t),f=this.session.session_generation,i=(M,a={})=>this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:M,...u,previous_session_id:f,...a});i("started");try{let M=await this.prepareAudio(s,!1),a;try{a=await this.acquireRecoverySession(this.session,{...u,previous_session_id:f},this.cancellation.signal)}catch(y){let S=th(y),g;try{g=JSON.parse(y.body??"{}").current_generation}catch{}if(t==="automatic_retry"||S.code!=="media_session_changed"||!g||!/^session-[a-f0-9-]{1,64}$/.test(g))throw y;this.assertCurrent(s),a=await this.client.attach(_,{...xh(t),previous_session_id:g},{signal:this.cancellation.signal},g)}this.assertCurrent(s),await this.attachAudio(a,s,M),await this.reconcileAttachedCall(a,s),i("connected",{session_id:a.session_generation})}catch(M){if(i(this.current(s)?"failed":"cancelled"),this.current(s))this.update({detail:_h(M)});throw M}finally{this.finish(s)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let h=this.session;if(!h){this.cancellation.abort();return}let t=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let m=(async()=>{try{if(await this.client.hangup(h.call_id),this.current(t))this.clearCall()}catch(s){if(this.current(t))this.update({audioState:"error",detail:_h(s)}),this.startPolling();throw s}finally{this.finish(t)}})();this.hangingUp=m;try{await m}finally{if(this.hangingUp===m)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(h){this.assertOpen();let t=this.session;if(!t)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let m=this.generation;this.update({busy:!0,detail:void 0});let s;try{s=this.client[h](t.call_id)}catch(_){if(this.current(m)&&this.session===t)this.update({busy:!1,detail:_h(_)});throw _}this.controlPending=s;try{let _=await s;if(this.current(m)&&this.session===t)this.update({holdState:_.hold_state,recordingState:_.recording_state,controlError:_.control_error,capabilities:_.capabilities});return _}catch(_){if(this.current(m)&&this.session===t)this.update({detail:_h(_)});throw _}finally{if(this.controlPending===s)this.controlPending=void 0;if(this.current(m)&&this.session===t)this.update({busy:!1})}}configureAudio(h){this.assertOpen();let t={...this.audioOptions,...h};v(t),this.audioOptions=t}setMuted(h){this.assertOpen(),this.audio?.setMuted(h),this.update({muted:h})}setOutputVolume(h){if(this.assertOpen(),!Number.isFinite(h)||h<0||h>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=h,this.audio?.setOutputVolume(h)}sendDTMF(h){if(this.assertOpen(),!/^[0-9*#]+$/.test(h))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(h)}observeCall(h){if(this.disposed||h.id!==this.session?.call_id)return;if(h.direction)this.outbound=h.direction==="outbound";if(dh(h.status)){this.invalidate(),this.clearCall(h.status),this.update({busy:!1,phase:"ended",termination:h.termination,answeredBy:h.answered_by??this.snapshot.answeredBy,endedAt:h.ended_at});return}this.update({carrierStatus:h.status,phase:Ut(h.status),answeredBy:h.answered_by??this.snapshot.answeredBy,holdState:h.hold_state??this.snapshot.holdState,recordingState:h.recording_state??this.snapshot.recordingState,controlError:h.control_error??this.snapshot.controlError,capabilities:h.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let h=this.options.ringback;return typeof h==="object"&&h?h.country:void 0}syncRingback(){let h=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(h&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!h&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(h,t,m=[]){this.hasStatusPush=!1,this.assertCurrent(t),this.stopAudio(),this.session=h,this.update({callId:h.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let s,_=()=>!this.disposed&&s!==void 0&&this.audio===s,u=(M)=>{if(_())try{M()}catch{}},f,i;try{this.assertCurrent(t),s=this.runtime.create({refreshMediaURL:(M,a)=>{let y=M??xh("automatic_retry");if(f)return this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"coalesced",...y,shared_attempt_id:i?.attempt_id}),f;if(!_()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let S=this.generation,g=(async()=>{let b=h.session_generation,w=await this.acquireRecoverySession(h,{...y,previous_session_id:b},a);if(this.assertCurrent(S),!_())throw Error("Audio connection no longer active");return h=w,this.session=w,this.startLease(w),this.reportSessionEvent({timestamp:new Date().toISOString(),action:"media_session",outcome:"replaced",...y,previous_session_id:b,session_id:w.session_generation}),this.client.mediaURL(w,this.audioOptions.mediaTransport)})();f=g,i=y;let A=()=>{if(f===g)f=void 0,i=void 0};return g.then(A,A),g},sessionDiagnostics:()=>({session_id:h.session_generation}),onSessionEvent:(M)=>u(()=>this.options.onSessionEvent?.(mh({...M,session_id:M.session_id??h.session_generation}))),onState:(M,a,y)=>{if(!_())return;if(M==="ended"&&a==="call.ended"){this.observeCall({id:h.call_id,status:"completed"});return}if(M==="live")this.hasLiveAudio=!0;if(this.update({audioState:M,detail:a}),this.scheduleStatusPoll?.(),_()&&(M==="error"||M==="ended")){if(this.stopAudio(),M==="error"){if(this.reconcileFailedAudio(h,this.generation),y?.recoverable)this.scheduleAudioRecovery()}}},onMediaReadiness:(M)=>u(()=>this.options.onMediaReadiness?.(M)),onLevels:(M,a)=>u(()=>this.options.onLevels?.(M,a)),onAudioHealth:(M)=>u(()=>this.options.onAudioHealth?.(M)),onDiagnostics:(M)=>u(()=>this.options.onDiagnostics?.(M)),onNotice:(M)=>u(()=>this.options.onNotice?.(M)),onCallStatus:(M)=>u(()=>{this.hasStatusPush=!0,this.observeCall({id:M.call_id,status:M.status,direction:M.direction,answered_at:M.answered_at,ended_at:M.ended_at,answered_by:M.answered_by,termination:M.termination,hold_state:M.hold_state,recording_state:M.recording_state,control_error:M.control_error}),this.scheduleStatusPoll?.()})}),this.audio=s,this.assertCurrent(t);for(let M of m)this.reportSessionEvent(M);if(this.assertCurrent(t),this.startPolling(),this.startLease(h),s.setMuted(this.snapshot.muted),await this.cancellable(s.start(this.client.mediaURL(h,this.audioOptions.mediaTransport),this.audioOptions),t),this.assertCurrent(t),!_())throw Error(this.snapshot.detail||"Audio connection ended during setup");s.setMuted(this.snapshot.muted),this.syncRingback()}catch(M){if(_())this.stopAudio();if(this.current(t))this.update({audioState:"error",detail:_h(M)});throw M}}async acquireRecoverySession(h,t,m){let s=this.uncertainAttachment??{diagnostics:t,expected:h.attachment_guard_version===1?h.session_generation??"":void 0};if(m?.aborted)throw new DOMException("Cancelled","AbortError");if(this.uncertainAttachment=s,s.diagnostics.attempt_id!==t.attempt_id)this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"idempotent_retry",...t,shared_attempt_id:s.diagnostics.attempt_id});try{let _=await this.client.attach(h.call_id,s.diagnostics,{signal:m},s.expected);if(m?.aborted)throw new DOMException("Cancelled","AbortError");return this.uncertainAttachment=void 0,_}catch(_){let u=th(_).status;if(u!==void 0&&u>=400&&u<500)this.uncertainAttachment=void 0;throw _}}scheduleAudioRecovery(){if(this.disposed||!this.session||!this.hasLiveAudio||this.automaticRecovery)return;let h=this.session.call_id,t=this.generation;this.update({audioState:"reconnecting",detail:"Network unavailable; waiting to restore audio."});let m=(u)=>{let f=th(u);return f.denied||f.code==="media_session_changed"||f.code==="call_ended"||f.code==="call_not_attachable"||["NotAllowedError","NotFoundError"].includes(u?.name)},s=this.uncertainAttachment?.diagnostics??xh("automatic_retry"),_=s;this.automaticRecovery=new yt(async(u)=>{this.assertCurrent(t);let f=await Bh((y)=>this.client.getCall(h,y),u);if(!f)throw Object.assign(Error("Call unavailable"),{status:404});if(dh(f.status)){this.observeCall(f);return}this.assertCurrent(t);let i=await this.prepareAudio(t,!1);if(u.aborted)throw new DOMException("Cancelled","AbortError");let M=await this.acquireRecoverySession(this.session,_,u);if(this.assertCurrent(t),u.aborted)throw new DOMException("Cancelled","AbortError");let a=()=>{if(this.current(t)&&this.session===M)this.stopAudio()};u.addEventListener("abort",a,{once:!0});try{if(await this.attachAudio(M,t,i),u.aborted)throw new DOMException("Cancelled","AbortError")}finally{u.removeEventListener("abort",a)}this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:"connected",..._}),this.automaticRecovery?.stop(),this.automaticRecovery=void 0},m,(u)=>{if(this.current(t))this.automaticRecovery=void 0,this.update({audioState:"error",detail:_h(u)})},(u)=>{if(u==="attempt_started")_={...s,attempt_id:Ch("attempt")};this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:u,..._})})}async reconcileAttachedCall(h,t){try{let m=await this.client.getCall(h.call_id);if(this.current(t)&&this.session===h&&m)this.observeCall(m)}catch{}}async reconcileFailedAudio(h,t){try{let m=await this.client.getCall(h.call_id);if(!this.current(t)||this.session!==h||!m)return;if(m.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(m)}catch{}}startLease(h){if(this.lease?.stop(),!h.lease_seconds)return;this.lease=new Uh(h.lease_seconds,()=>this.client.renew(h),(t,m)=>{if(this.disposed||this.session!==h)return;if(this.stopAudio(),this.update({audioState:"error",detail:t==="expired"?"Audio session expired. Reconnecting audio.":`Audio authorization ended: ${_h(m??t)}`}),t==="expired")this.scheduleAudioRecovery();else this.automaticRecovery?.stop(),this.automaticRecovery=void 0},(t)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(t);else try{this.options.onSessionEvent?.(t)}catch{}},h.lease_started_ms)}reportSessionEvent(h){if(h=mh(h),this.audio?.recordSessionEvent)try{this.audio.recordSessionEvent(h)}catch{}else try{this.options.onSessionEvent?.(h)}catch{}}stopAudio(){this.lease?.stop(),this.lease=void 0;let h=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{h?.stopRingback?.()}catch{}}try{h?.stop()}catch{}if(h)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){this.scheduleStatusPoll=void 0,clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let h=new AbortController;this.polling=h;let t=!1,m=performance.now(),s=()=>this.hasStatusPush&&this.snapshot.audioState==="live"?Math.max(1,Math.max(this.interval,30000)-(performance.now()-m)):this.interval,_=async()=>{if(t)return;let u=this.session?.call_id;if(!u||h.signal.aborted)return;t=!0;try{let f=await this.client.getCall(u,h.signal);if(m=performance.now(),!h.signal.aborted&&f)this.observeCall(f)}catch(f){if(!h.signal.aborted)this.hasStatusPush=!1,this.update({detail:`Call status unavailable: ${_h(f)}`})}finally{t=!1,this.scheduleStatusPoll?.()}};this.scheduleStatusPoll=()=>{if(t||h.signal.aborted||!this.session||this.disposed)return;clearTimeout(this.timer),this.timer=setTimeout(_,s())},this.scheduleStatusPoll()}clearCall(h){this.hasStatusPush=!1,this.stopAudio(),this.stopPolling(),this.session=void 0,this.hasLiveAudio=!1,this.outbound=!1,this.update({callId:void 0,carrierStatus:h,audioState:"idle",muted:!1,detail:void 0,phase:h?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function h0(h,t="en"){if(h?.reason==="ai_inactivity")return t.toLowerCase().startsWith("fr")?"Fin de l’appel : aucune réponse":"Call ended: no response";if(h?.reason==="ai_policy_failure")return t.toLowerCase().startsWith("fr")?"Échec du rappel d’inactivité IA":"AI inactivity reminder failed";if(h?.reason==="time_limit"||h?.reason==="ai_max_duration")return t.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return h?.reason?.replaceAll("_"," ")??""}class Wt extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function t0(h){return h.direction==="inbound"&&h.status==="pending"&&h.answerable!==!1&&!h.routing_waiting&&(h.peer_kind==="human"||Boolean(h.ring_offers?.some((t)=>t.kind==="browser")))}var dh=(h)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(h);function k(h){if(!h||/[\s/\\?#]/.test(h))throw Error("Invalid call ID");return encodeURIComponent(h)}class lh{app;options;transport;listMicrophones=Tt;createMicrophonePreview=(h)=>Ot(h,this.app);constructor(h,t={}){this.app=h;this.options=t;if(this.transport=new rh(h,t.authProvider?"proxy":t.direct?"direct":t.runtime,globalThis.fetch,t.direct),h.name!=="telephony"||!h.projectId||!h.installId)throw Error("Telephony requires an explicit project and installation")}get(h,t){return this.transport.request("GET",h,void 0,t)}post(h,t,m){return this.transport.request("POST",h,t,m)}subscribe(h,t,m){return this.transport.subscribe(h,t,m)}path(h){if(!this.options.authProvider)return h;return`/user${h}${h.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(h){let t=await this.get(this.path("/calls"),{signal:h});if(!Array.isArray(t?.calls)||t.calls.some((m)=>!m||typeof m.id!=="string"||typeof m.status!=="string"))throw Error("Invalid Telephony calls response");return t.calls}async getCall(h,t){let m=await this.get(this.path(`/calls?call_id=${k(h)}`),{signal:t});if(!Array.isArray(m?.calls))throw Error("Invalid Telephony call response");return m.calls.find((s)=>s.id===h)}incomingCalls(h){return h.filter(t0)}watchCalls(h,t={}){let m=t.intervalMs??2000;if(!Number.isFinite(m)||m<100)throw Error("Call watch interval must be at least 100 ms");let s=new AbortController,_,u,f=!1,i=!1,M=-1/0,a,y,S=performance.now(),g=()=>{let P=performance.now()-M;return P<15000?Math.max(1,Math.min(Math.max(m,30000)-(performance.now()-S),15000-P)):m},A=new Set,b=()=>{clearTimeout(_),s.abort(),u?.close(),t.signal?.removeEventListener("abort",b)},w=(P)=>{try{t.onError?.(P)}catch{b()}};if(t.signal?.addEventListener("abort",b,{once:!0}),t.signal?.aborted)b();let C=async(P)=>{if(s.signal.aborted)return;if(f){i=!0;return}clearTimeout(_),f=!0;let q=performance.now(),R=a;try{let J=await this.listCalls(s.signal);if(S=performance.now(),!s.signal.aborted){h(J),y=R;for(let Q of J){if(Q.answerable!==!0||Q.status!=="pending")continue;for(let B of Q.ring_offers??[]){if(B.kind!=="browser"||!B.id||A.has(B.id))continue;A.add(B.id),this.acknowledgeOffer(Q.id,B.id).catch((X)=>{if(![404,405].includes(X?.status??0))A.delete(B.id)})}}try{t.onTiming?.({trigger:P,fetchMs:performance.now()-q})}catch{}}}catch(J){if(M=-1/0,y=void 0,!s.signal.aborted){let Q=J?.status;try{t.onFailure?.({trigger:P,fetchMs:performance.now()-q,status:typeof Q==="number"?Q:void 0,error:J})}catch{}w(J)}}finally{if(f=!1,!s.signal.aborted)if(i)i=!1,C("push");else _=setTimeout(()=>void C("poll"),g())}};if(C("poll"),!s.signal.aborted&&t.push!==!1&&typeof this.app.subscribe==="function")try{u=this.subscribe(this.path("/calls/events"),(P)=>{if(P.type==="heartbeat"||P.type==="calls.changed"){if(M=performance.now(),P.type==="calls.changed"){let q=typeof P.state==="string"&&/^[0-9a-f]{64}$/.test(P.state)?P.state:void 0;if(a=q,!q||q!==y||f)C("push")}else if(!f)clearTimeout(_),_=setTimeout(()=>void C("poll"),g())}else if(P.type==="access.revoked")M=-1/0,u?.close(),C("push")},{transport:"fetch",signal:s.signal,reconnectDelayMs:250,onError:(P)=>{if(M=-1/0,!f&&!s.signal.aborted)clearTimeout(_),_=setTimeout(()=>void C("poll"),m);let q=P?.status;if(q===404||q===405||q===501)u?.close()}})}catch{}return{close:b}}async place(h){if(!/^\+[1-9]\d{7,14}$/.test(h.to)||!h.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let t=ah();return this.session(await this.post(this.path("/softphone/place"),h),void 0,"media",t)}async answer(h,t={}){let m=ah();try{return this.session(await this.post(this.path(`/softphone/answer/${k(h)}`),t),h,"media",m)}catch(s){let _=s;if(_.status===409&&typeof _.body==="string"){let u;try{u=JSON.parse(_.body)}catch{}if(u?.code==="offer_expired")throw new Wt}throw s}}async acknowledgeOffer(h,t){await this.post(this.path(`/softphone/offer/ack/${k(h)}`),{offer_id:t})}async declineOffer(h,t){await this.post(this.path(`/softphone/offer/decline/${k(h)}`),{offer_id:t})}async attach(h,t,m,s){let _=ah();return this.session(await Bh((u)=>this.post(this.path(`/softphone/attach/${k(h)}`),{...t?{media_diagnostics:t}:{},...s!==void 0&&t?.attempt_id?{attachment_guard:{id:t.attempt_id,expected_generation:s}}:{}},{...m,signal:u}),m?.signal??void 0),h,"media",_)}async takeover(h,t){let m=ah();return this.session(await this.post(this.path(`/softphone/takeover/${k(h)}`),t?{media_diagnostics:t}:{}),h,"media",m)}createCallListener(h={}){return new at(this,{...h,...Qh({...this.options.audio,...h.audio},h)})}async listenSession(h,t){let m=ah();return this.session(await Bh((s)=>this.post(this.path(`/softphone/listen/${k(h)}`),{},{signal:s}),t,5000,(s)=>{try{this.stopListening(this.session(s,h,"listen-media",m)).catch(()=>{})}catch{}}),h,"listen-media",m)}async coachSession(h){let t=ah(),m=this.session(await this.post(this.path(`/softphone/coach/${k(h)}`),{}),h,"listen-media",t);if(m.coaching!==!0)throw Error("Invalid coaching session");return m}async renewListening(h){return this.post(this.path(`/softphone/${h.coaching?"coach-renew":"listen-renew"}/${k(h.call_id)}`),{session_token:h.session_token})}async stopListening(h){await this.post(this.path(`/softphone/${h.coaching?"coach-stop":"listen-stop"}/${k(h.call_id)}`),{session_token:h.session_token})}async listenerAudit(h){return this.get(this.path(`/softphone/listen-audit/${k(h)}`))}turnPolicy(h={}){return this.post(this.path("/softphone/turn-policy"),h)}configureTURNPolicy(h={}){return this.post(this.path("/numbers/turn-policy"),h)}carrierMediaSettings(h){return this.post(this.path("/numbers/carrier-media"),h)}outboundTransport(h){return this.post(this.path("/numbers/outbound-transport"),h)}outboundNumbers(h={}){return this.get(this.path(h.fresh?"/softphone/numbers?fresh=true":"/softphone/numbers"))}async renew(h){return this.post(this.path(`/softphone/renew/${k(h.call_id)}`),{session_token:h.session_token})}async release(h){if(!h.session_token)throw Error("Answer session has no release token");await this.post(this.path(`/softphone/release/${k(h.call_id)}`),{session_token:h.session_token})}async hangup(h){await this.post(this.path(`/calls/${k(h)}/hangup`),{})}hold(h){return this.post(this.path(`/calls/${k(h)}/hold`),{})}resume(h){return this.post(this.path(`/calls/${k(h)}/resume`),{})}pauseRecording(h){return this.post(this.path(`/calls/${k(h)}/pause-recording`),{})}resumeRecording(h){return this.post(this.path(`/calls/${k(h)}/resume-recording`),{})}createSoftphone(h={}){return new St(this,{...h,audio:{...this.options.audio,...h.audio}})}mediaURL(h,t){return this.resolveMediaURL(h,"media",t)}listenerMediaURL(h,t){return this.resolveMediaURL(h,"listen-media",t)}resolveMediaURL(h,t,m){let s=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),_=new URL(h.media_url,s),u=`/api/apps/telephony/_install/${this.app.installId}/softphone/${t}/${k(h.call_id)}/`;if(_.origin!==s.origin||!["http:","https:"].includes(_.protocol)||_.username||_.password||_.search||_.hash||!_.pathname.startsWith(u)||!/^[A-Za-z0-9_-]+$/.test(_.pathname.slice(u.length)))throw Error("Invalid Telephony media endpoint");if(t==="listen-media"&&_.pathname.slice(u.length)!==h.session_token)throw Error("Listener credential mismatch");if(t==="media"&&Fh(h.session_generation))_.searchParams.set("session_generation",h.session_generation);let f=this.transport.mediaURL(_,m);return f.protocol=f.protocol==="https:"?"wss:":"ws:",f.href}session(h,t,m="media",s=ah()){let _=h;if(!_||typeof _.call_id!=="string"||typeof _.media_url!=="string"||t&&_.call_id!==t||_.session_token!==void 0&&typeof _.session_token!=="string")throw Error("Invalid Telephony session response");if(_.lease_seconds!==void 0&&(!Number.isFinite(_.lease_seconds)||_.lease_seconds<10||_.lease_seconds>3600||!_.session_token))throw Error("Invalid media lease");if(m==="listen-media"&&(!_.session_token||_.lease_seconds===void 0))throw Error("Invalid listener lease");return k(_.call_id),this.resolveMediaURL(_,m),{..._,lease_started_ms:s}}}var Zm=nh({app:"telephony",create:({app:h})=>new lh(h)});function km({app:h},t){return new lh(h,t)}export{km as createClient,h0 as callTerminationLabel};
