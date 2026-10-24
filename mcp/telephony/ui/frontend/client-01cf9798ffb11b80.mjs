function xh(h){let t=globalThis.crypto?.randomUUID?.()??`${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;return`${h}-${t}`}function Eh(h){return{recovery_id:xh("recovery"),attempt_id:xh("attempt"),initiating_action:h}}function Th(h){return typeof h==="string"&&h.length<=80&&/^(browser|session|recovery|attempt)-[a-f0-9-]{1,64}$/.test(h)?h:void 0}function mh(h){let t=(u,M)=>typeof u==="string"&&u.length<=M&&/^[a-zA-Z0-9_-]*$/.test(u)?u:void 0,m=["WebSocket transport error","WebRTC signaling error","Audio worker failed","WebRTC signaling unavailable"],_=["attach","takeover","automatic_retry","manual_reconnect","audio_device_change","component_recreation"],s=(u,M)=>typeof u==="number"&&Number.isFinite(u)?Math.max(0,Math.min(M,u)):void 0;return{timestamp:Number.isFinite(Date.parse(h.timestamp))?new Date(h.timestamp).toISOString():new Date().toISOString(),action:t(h.action,40)??"diagnostic",outcome:t(h.outcome,40)??"unknown",code:t(h.code,80),detail:h.detail?m.includes(h.detail)?h.detail:"detail_redacted":void 0,recovery_id:Th(h.recovery_id),attempt_id:Th(h.attempt_id),shared_attempt_id:Th(h.shared_attempt_id),session_id:Th(h.session_id),previous_session_id:Th(h.previous_session_id),initiating_action:_.includes(h.initiating_action)?h.initiating_action:void 0,status:s(h.status,599),remaining_ms:s(h.remaining_ms,3600000),was_clean:typeof h.was_clean==="boolean"?h.was_clean:void 0,duration_ms:s(h.duration_ms,86400000)}}var ih=()=>performance.now();async function Uh(h,t,m=5000,_){let s=new AbortController,f,u=new Promise((i,q)=>{f=q}),M=()=>{s.abort(),f(new DOMException("Media request cancelled","AbortError"))};t?.addEventListener("abort",M,{once:!0});let a=setTimeout(M,m);try{if(t?.aborted)return M(),await u;return await Promise.race([h(s.signal).then((i)=>{if(s.signal.aborted)_?.(i);return i}),u])}finally{clearTimeout(a),t?.removeEventListener("abort",M)}}function th(h){let t=h,m=t?.status,_;try{let f=JSON.parse(t?.body??"{}");if(typeof f.code==="string")_=f.code}catch{}let s=_==="media_lease_expired";return{status:m,code:_,expired:s,denied:!s&&[401,403,404,410].includes(m)}}class Nh{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(h,t,m,_,s=ih(),f={now:ih,setTimeout:(u,M)=>setTimeout(u,M),clearTimeout:(u)=>clearTimeout(u)}){this.seconds=h;this.renew=t;this.ended=m;this.event=_;this.clock=f;this.deadline=s+h*1000-1000,this.watchExpiry(),this.schedule(Math.min(h*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(h,t){let{status:m,code:_}=th(t);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:h,status:m,code:_,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(h){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,h))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let h=this.clock.now();try{let t=await Promise.race([this.renew(),new Promise((m,_)=>{this.requestTimer=this.clock.setTimeout(()=>_(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(t?.lease_seconds!==void 0){if(!Number.isFinite(t.lease_seconds)||t.lease_seconds<10||t.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=t.lease_seconds}this.deadline=h+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(t){if(this.stopped)return;let m=th(t);if(m.denied||m.expired){this.finish(m.expired?"expired":"revoked",t);return}this.report("retrying",t),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(h,t){if(this.stopped)return;this.report(h,t),this.stop(),this.ended(h,t)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function Dh(h){let t=new URL(h),m=["localhost","127.0.0.1","[::1]"].includes(t.hostname);if(t.username||t.password||t.search||t.hash||t.protocol!=="https:"&&!(t.protocol==="http:"&&m)||!t.pathname.endsWith("/runtime-browser"))throw Error("Invalid managed Telephony browser endpoint");return t}function Pt(h){if(!h.startsWith("/")||h.startsWith("//")||h.includes("..")||h.includes("\\"))throw Error("Invalid Telephony request path")}function Vt(h,t){return new Promise((m)=>{let _=()=>{clearTimeout(s),h.removeEventListener("abort",_),m()},s=setTimeout(_,t);if(h.addEventListener("abort",_,{once:!0}),h.aborted)_()})}class nh{proxy;mode;fetchImpl;direct;info;discovery;credential;issuing;proxyOnly=!1;constructor(h,t="auto",m=globalThis.fetch,_){this.proxy=h;this.mode=t;this.fetchImpl=m;this.direct=_;this.fetchImpl=m.bind(globalThis)}get directBase(){return this.info?.browser_api_base}async prepare(){if(this.mode==="proxy"||this.mode==="auto"&&typeof location>"u")return;if(this.direct){Dh(this.direct.baseURL),this.info={managed:!0,browser_api_base:this.direct.baseURL};return}if(this.discovery)return this.discovery;this.discovery=(async()=>{let h;try{h=await this.proxy.get("/runtime-endpoints")}catch(m){if(this.mode==="direct"||![404,405].includes(m.status??0))throw m;this.proxyOnly=!0;return}if(!h?.managed||!h.browser_api_base){if(this.proxyOnly=!0,this.mode==="direct")throw Error("Direct Telephony deployment unavailable");return}if(Dh(h.browser_api_base),h.browser_media_base)Dh(h.browser_media_base);if(h.browser_signaling_base)Dh(h.browser_signaling_base);let t=new URL(this.proxy.mcpURL(),typeof location>"u"?void 0:location.href);if(!["https:","http:"].includes(t.protocol))throw Error("Invalid Telephony management gateway");this.info=h})();try{await this.discovery}catch(h){throw this.discovery=void 0,h}}async grant(){if(this.credential&&this.credential.expires_at*1000>Date.now()+30000)return this.credential;if(this.issuing)return this.issuing;let h=typeof location>"u"?new URL(this.proxy.mcpURL()).origin:location.origin;this.issuing=(async()=>{let t=this.direct?await this.direct.credentialProvider():await this.proxy.post("/runtime-browser-bootstrap",{origin:h});if(!t||typeof t.token!=="string"||!t.token||t.token.length>4096||!Number.isSafeInteger(t.expires_at)||t.expires_at*1000<=Date.now()||t.expires_at*1000>Date.now()+360000||t.endpoint!==this.directBase)throw Error("Invalid managed browser credential");return this.credential=t})();try{return await this.issuing}finally{this.issuing=void 0}}async request(h,t,m,_){if(this.mode==="proxy"||this.proxyOnly||this.mode==="auto"&&typeof location>"u")return h==="GET"?this.proxy.get(t,_):this.proxy.post(t,m,_);if(await this.prepare(),!this.directBase)return h==="GET"?this.proxy.get(t,_):this.proxy.post(t,m,_);Pt(t);let s=await this.grant(),f=new URL(this.directBase+t);if(this.proxy.projectId)f.searchParams.set("project_id",this.proxy.projectId);let u=new Headers(_?.headers);u.set("Authorization","Bearer "+s.token),u.set("Content-Type","application/json");let M=await this.fetchImpl(f,{..._,method:h,headers:u,body:m===void 0?void 0:JSON.stringify(m),credentials:"omit",redirect:"error"});if(!M.ok){if(M.status===403)this.credential=void 0;let a=(await M.text()).slice(0,65536);throw Object.assign(Error(`Telephony HTTP ${M.status}`),{status:M.status,body:a})}return await M.json()}mediaURL(h,t){if(!this.directBase)return h;let m=`/api/apps/telephony/_install/${this.proxy.installId}`,_=t==="webrtc"||t==="auto"?this.info?.browser_signaling_base:this.info?.browser_media_base,s=new URL((_||this.directBase)+h.pathname.slice(m.length));return s.search=h.search,s}subscribe(h,t,m){let _=new AbortController,s,f=()=>{_.abort(),s?.close(),m?.signal?.removeEventListener("abort",f)};if(m?.signal?.addEventListener("abort",f,{once:!0}),m?.signal?.aborted)f();return(async()=>{try{if(await this.prepare(),_.signal.aborted)return;if(!this.directBase){s=this.proxy.subscribe(h,t,m);return}Pt(h);let u=500;while(!_.signal.aborted){try{let M=await this.grant(),a=new URL(this.directBase+h);if(this.proxy.projectId)a.searchParams.set("project_id",this.proxy.projectId);let i=await this.fetchImpl(a,{headers:{Authorization:"Bearer "+M.token,Accept:"text/event-stream"},credentials:"omit",redirect:"error",signal:_.signal});if(!i.ok||!i.body){if(i.status===403)this.credential=void 0;throw Object.assign(Error(`Telephony stream HTTP ${i.status}`),{status:i.status})}u=500;let q=i.body.getReader(),y=new TextDecoder,S="";try{while(!_.signal.aborted){let{done:C,value:w}=await q.read();if(C)break;if(S+=y.decode(w,{stream:!0}),S=S.replace(/\r\n/g,`
`),S.length>65536)throw Error("Telephony notification frame too large");let b;while((b=S.indexOf(`

`))>=0){let x=S.slice(0,b);S=S.slice(b+2);let A=x.split(`
`).filter((g)=>g.startsWith("data:")).map((g)=>g.slice(5).trimStart()).join(`
`);if(A)try{t(JSON.parse(A))}catch{}}}}finally{await q.cancel().catch(()=>{})}}catch(M){if(_.signal.aborted)return;m?.onError?.(M),u=Math.min(1e4,u*2)}if(!_.signal.aborted)await Vt(_.signal,u)}}catch(u){if(!_.signal.aborted)m?.onError?.(u)}})(),{close:f}}}function rh(h){return h}function vh(h){if(h.turnTransport!==void 0&&!["all","udp","tcp","tls"].includes(h.turnTransport))throw RangeError("Invalid TURN transport");if(h.iceTransportPolicy!==void 0&&!["all","relay"].includes(h.iceTransportPolicy))throw RangeError("Invalid ICE policy");if(h.turnTLSPort!==void 0&&(!Number.isInteger(h.turnTLSPort)||h.turnTLSPort<1||h.turnTLSPort>65535||h.turnTransport!=="tls"))throw RangeError("TURN TLS port requires TLS transport and a valid port")}function Wh(h){let t=h.ice_transport_policy??"all";if(t!=="all"&&t!=="relay")throw Error("Invalid server ICE policy");let m=h.ice_servers??[];if(!Array.isArray(m)||m.length>8)throw Error("Invalid server ICE configuration");for(let _ of m){let s=typeof _.urls==="string"?[_.urls]:_.urls;if(!Array.isArray(s)||s.length<1||s.length>16||s.some((f)=>typeof f!=="string"||f.length>1024||/[\r\n@#]/.test(f)||!/^stuns?:|^turns?:/.test(f)))throw Error("Invalid server ICE endpoint")}if(t==="relay"&&!m.some((_)=>(typeof _.urls==="string"?[_.urls]:_.urls).some((s)=>/^turns?:/.test(s))))throw Object.assign(Error("Required TURN relay is unavailable"),{code:"turn_relay_unavailable"});return{iceServers:m,iceTransportPolicy:t}}function At(h,t){vh(t);for(let m of["turn_transport","ice_policy","turn_tls_port"])h.searchParams.delete(m);if(t.turnTransport!==void 0)h.searchParams.set("turn_transport",t.turnTransport);if(t.iceTransportPolicy!==void 0)h.searchParams.set("ice_policy",t.iceTransportPolicy);if(t.turnTLSPort!==void 0)h.searchParams.set("turn_tls_port",String(t.turnTLSPort))}function wt(h){let t={pairID:"",direct:!1,sent:0,received:0,send:!1,receive:!1};if(h.forEach((u)=>{if(u.type==="transport"&&u.selectedCandidatePairId)t.pairID=u.selectedCandidatePairId;if(u.type==="outbound-rtp"&&!u.isRemote)t.sent+=u.packetsSent??0,t.send=!0;if(u.type==="inbound-rtp"&&!u.isRemote)t.received+=u.packetsReceived??0,t.receive=!0}),!t.pairID)h.forEach((u)=>{if(u.type==="candidate-pair"&&u.nominated&&u.state==="succeeded")t.pairID=u.id});let m=h.get(t.pairID),_=m&&h.get(m.localCandidateId),s=m&&h.get(m.remoteCandidateId),f=(u)=>u&&["host","srflx","prflx"].includes(u.candidateType);return t.direct=Boolean(m&&f(_)&&f(s)),t}function $t(h,t){return Boolean(t.pairID&&t.pairID!==h.pairID&&(h.send||h.receive)&&(!h.send||t.sent>h.sent)&&(!h.receive||t.received>h.received))}class Hh{send;active;observe;configured;work=Promise.resolve();peers=new Map;revision=1;closed=!1;retryTimer;retries=0;constructor(h,t,m=()=>{},_){this.send=h;this.active=t;this.observe=m;this.configured=_}register(h,t,m=()=>!0,_){this.remove(h);let s=h==="coach";this.peers.set(h,{pc:t,current:m,revision:0,generation:_,cancellation:new AbortController,offerType:s?"coach.webrtc.restart_offer":"webrtc.restart_offer",answerType:s?"coach.webrtc.restart_answer":"webrtc.restart_answer"})}remove(h){let t=this.peers.get(h);if(t?.cancellation.abort(),t?.pending)clearTimeout(t.pending.timer),t.pending.reject(Error("ICE peer replaced"));this.peers.delete(h)}report(h,t){try{this.observe(h,t)}catch{}}alive(h){return!this.closed&&this.active()&&(!h||h.current()&&h.pc.connectionState!=="closed")}handle(h){if(!["webrtc.ice_config","webrtc.restart_answer","coach.webrtc.restart_answer","webrtc.ice_status"].includes(h.type))return!1;if(!this.alive())return!0;if(h.type==="webrtc.ice_status"){if(this.report(["credential_refresh_unavailable","restart_failed"].includes(h.state)?h.state:"ice_status"),h.state==="restart_failed"){for(let m of this.peers.values())if(m.generation===h.generation&&m.pending&&m.pending.revision===h.revision)clearTimeout(m.pending.timer),m.pending.reject(Error("ICE restart rejected")),m.pending=void 0}return!0}if(h.type.endsWith("restart_answer")){for(let m of this.peers.values()){if(h.type!==m.answerType||m.generation!==h.generation||m.pending?.revision!==h.revision||!this.alive(m))continue;let _=m.pending;if(!_)continue;m.pc.setRemoteDescription({type:"answer",sdp:h.sdp}).then(()=>{if(m.pending!==_)return;if(clearTimeout(_.timer),m.pending=void 0,this.alive(m))_.resolve();else _.reject(Error("ICE session replaced"))}).catch(()=>{if(m.pending===_)clearTimeout(_.timer),m.pending=void 0,_.reject(Error("ICE answer rejected"))})}return!0}if(!Number.isSafeInteger(h.revision)||h.revision<=this.revision)return!0;let t;try{if(t=Wh(h),!Number.isFinite(h.expires_at)||h.expires_at*1000<=Date.now()+1000)throw Error()}catch{return this.report("invalid_ice_refresh"),!0}this.revision=h.revision;try{this.configured?.(t)}catch{}return this.report("credentials_refreshed",{code:`ice_rev_${h.revision}`,remaining_ms:Math.max(0,Math.min(3600000,Math.round(h.expires_at*1000-Date.now())))}),this.work=this.work.then(async()=>{if(!this.alive()||this.revision!==h.revision)return;let m=await Promise.allSettled([...this.peers.values()].filter((_)=>this.alive(_)).map((_)=>this.restart(_,t,h.revision)));if(!this.alive()||this.revision!==h.revision)return;if(m.some((_)=>_.status==="rejected"))this.retry();else{if(this.retries=0,this.retryTimer)clearTimeout(this.retryTimer);this.retryTimer=void 0}}).catch(()=>{this.report("restart_failed"),this.retry()}),!0}mediaStats(h,t){return new Promise((m,_)=>{let s=()=>{clearTimeout(u),h.cancellation.signal.removeEventListener("abort",f)},f=()=>{s(),_(Error("ICE peer removed"))},u=setTimeout(()=>{s(),_(Error("ICE media statistics timed out"))},1500);if(h.cancellation.signal.addEventListener("abort",f,{once:!0}),!t()){f();return}Promise.resolve().then(()=>h.pc.getStats()).then((M)=>{s(),t()?m(M):_(Error("ICE peer replaced"))},()=>{s(),_(Error("ICE media statistics unavailable"))})})}retry(){if(this.retryTimer||this.retries>=3||!this.alive())return;this.retryTimer=setTimeout(()=>{if(this.retryTimer=void 0,this.alive()){this.retries++;try{this.send({type:"webrtc.ice_refresh"})}catch{this.report("refresh_request_failed")}}},15000)}async restart(h,t,m){if(h.pending)clearTimeout(h.pending.timer),h.pending.reject(Error("ICE revision superseded")),h.pending=void 0;let _=()=>this.alive(h)&&this.peersHas(h);if(this.revision!==m||!_())return;let s=xh("attempt"),f=performance.now(),u=()=>({attempt_id:s,code:`ice_rev_${m}`,duration_ms:Math.round(performance.now()-f)});try{if(await new Promise((q,y)=>{if(h.pc.signalingState==="stable"){q();return}let S=()=>{clearTimeout(b),h.pc.removeEventListener("signalingstatechange",C),h.cancellation.signal.removeEventListener("abort",w)},C=()=>{if(!_()||h.pc.signalingState==="stable")S(),_()?q():y(Error("ICE peer replaced"))},w=()=>{S(),y(Error("ICE peer removed"))},b=setTimeout(()=>{S(),y(Error("ICE signaling busy"))},1e4);h.pc.addEventListener("signalingstatechange",C),h.cancellation.signal.addEventListener("abort",w,{once:!0}),C()}),!_())return;h.pc.setConfiguration({...h.pc.getConfiguration(),...t});let M=wt(await this.mediaStats(h,_));if(!_())return;if(t.iceTransportPolicy!=="relay"&&h.pc.connectionState==="connected"&&M.direct){h.revision=m,this.report("credentials_applied_direct",u());return}let a=await h.pc.createOffer({iceRestart:!0});if(!_())return;if(await h.pc.setLocalDescription(a),!_())return;if(await new Promise((q,y)=>{if(h.pc.iceGatheringState==="complete"){q();return}let S=()=>{clearTimeout(b),h.pc.removeEventListener("icegatheringstatechange",C),h.cancellation.signal.removeEventListener("abort",w)},C=()=>{if(!_()||h.pc.iceGatheringState==="complete")S(),_()?q():y(Error("ICE session replaced"))},w=()=>{S(),y(Error("ICE session stopped"))},b=setTimeout(()=>{S(),y(Error("ICE gathering timed out"))},7000);h.cancellation.signal.addEventListener("abort",w,{once:!0}),h.pc.addEventListener("icegatheringstatechange",C),C()}),!_())return;if(this.report("restart_started",u()),await new Promise((q,y)=>{let S=setTimeout(()=>{if(h.pending?.revision===m)h.pending=void 0;y(Error("ICE restart timed out"))},1e4);h.pending={revision:m,resolve:q,reject:y,timer:S};try{this.send({type:h.offerType,revision:m,generation:h.generation,sdp:h.pc.localDescription.sdp})}catch{clearTimeout(S),h.pending=void 0,y(Error("ICE signaling unavailable"))}}),_())this.report("restart_signaled",u());let i=performance.now()+1e4;while(_()){let q=wt(await this.mediaStats(h,_));if(!_())return;if(h.pc.connectionState==="connected"&&$t(M,q)){h.revision=m,this.report("restart_completed",u());return}if(performance.now()>=i)throw Error("ICE media did not resume");await new Promise((y,S)=>{let C=()=>{clearTimeout(b),h.cancellation.signal.removeEventListener("abort",w)},w=()=>{C(),S(Error("ICE peer removed"))},b=setTimeout(()=>{C(),y()},100);if(h.cancellation.signal.addEventListener("abort",w,{once:!0}),!_())w()})}}catch(M){if(!_())return;if(_()&&h.pc.signalingState==="have-local-offer")try{await h.pc.setLocalDescription({type:"rollback"})}catch{}if(_())this.report("restart_failed",u());throw M}}peersHas(h){return[...this.peers.values()].includes(h)}stop(){if(this.closed)return;if(this.closed=!0,this.retryTimer)clearTimeout(this.retryTimer);this.retryTimer=void 0;for(let h of[...this.peers.keys()])this.remove(h)}}var Oh="0.12.35";var Xt=["rtt_ms","queue_ms","target_ms","buffered_bytes","underruns","dropped_ms","capture_sequence_gaps","playback_sequence_gaps","main_thread_max_pause_ms","worker_max_tick_gap_ms","capture_sent_ms","capture_muted_ms","capture_dropped_ms","playback_ingress_ms","playback_transport_dropped_ms","playback_source_dropped_ms","clock_uncertainty_ms","clock_sample_age_ms","stats_errors","stats_duration_ms","send_bitrate_bps","receive_bitrate_bps","receive_gap_ms","capture_age_ms","transit_ms","delivery_excess_ms","server_queue_ms","receiver_concealed_delta_ms","receiver_silent_concealed_delta_ms","receiver_concealed_window_ms","requested_playback_target_ms","requested_upload_bitrate_bps","receiver_ssrc","receiver_loss_delta","receiver_loss_window_ms","remote_receiver_ssrc","remote_receiver_loss_delta","remote_receiver_loss_window_ms","receiver_bytesReceived","receiver_packetsReceived","receiver_packetsLost","receiver_jitter","receiver_packetsDiscarded","receiver_concealedSamples","receiver_silentConcealedSamples","receiver_concealmentEvents","receiver_insertedSamplesForDeceleration","receiver_removedSamplesForAcceleration","receiver_totalSamplesReceived","receiver_audioLevel","receiver_totalAudioEnergy","receiver_totalSamplesDuration","receiver_nackCount","receiver_fecPacketsReceived","receiver_fecPacketsDiscarded","receiver_jitter_buffer_interval_ms","receiver_jitter_target_interval_ms","receiver_jitter_minimum_interval_ms","receiver_processing_interval_ms","receiver_totalProcessingDelay","receiver_totalSamplesDecoded","telemetry_skipped_samples","telemetry_skipped_auxiliary","telemetry_queue_age_ms","receiver_last_packet_age_ms","sender_bytesSent","sender_packetsSent","sender_headerBytesSent","sender_retransmittedPacketsSent","sender_retransmittedBytesSent","sender_nackCount","sender_targetBitrate","sender_send_delay_interval_ms","remote_receiver_packetsLost","remote_receiver_fractionLost","remote_receiver_jitter","remote_receiver_roundTripTime","remote_receiver_totalRoundTripTime","remote_receiver_roundTripTimeMeasurements","remote_sender_packetsSent","remote_sender_bytesSent","pair_availableOutgoingBitrate","pair_availableIncomingBitrate","pair_currentRoundTripTime","pair_totalRoundTripTime","pair_bytesSent","pair_bytesReceived","pair_requestsSent","pair_requestsReceived","pair_responsesSent","pair_responsesReceived","pair_consentRequestsSent","path_revision","codec_clock_rate","codec_channels"],Yt={receiver_processing:["unavailable","per_decoded_sample"],connection:["connected","reconnecting","closed"],context:["running","suspended","interrupted","closed"],muted:["true","false"],device_muted:["true","false"],track:["live","ended"],ice:["new","checking","connected","completed","disconnected","failed","closed"],dtls:["new","connecting","connected","closed","failed"],pair:["frozen","waiting","in-progress","failed","succeeded"],local_candidate:["host","srflx","prflx","relay"],remote_candidate:["host","srflx","prflx","relay"],protocol:["udp","tcp"],relay_protocol:["udp","tcp","tls"],codec:["audio/opus","audio/PCMU","audio/PCMA","pcm16"]};function oh(h){let t={};for(let m of Xt)if(typeof h[m]==="number"&&Number.isFinite(h[m]))t[m]=Math.max(0,Math.min(h[m],1000000000000));return t}function ht(h){let t={};for(let[m,_]of Object.entries(Yt))if(typeof h[m]==="string"&&_.includes(h[m]))t[m]=h[m];return t}class Vh{transport;serial=0;lastEmit=-1/0;previousAt;previous;incident;history=[];pending=[];counters={};constructor(h){this.transport=h}observe(h,t,m=performance.now(),_=new Date().toISOString(),s){if(!Number.isFinite(m)||!Number.isFinite(Date.parse(_)))return;if(this.previousAt!==void 0&&m<this.previousAt)this.lastEmit=-1/0,this.previous=void 0,this.counters={},this.incident=void 0;let f=oh(h),u=ht(t),M=!1;for(let i of["underruns","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealmentEvents","path_revision","stats_errors"])if(f[i]!==void 0){if(this.counters[i]!==void 0&&f[i]>this.counters[i])M=!0;this.counters[i]=f[i]}let a={id:`s${++this.serial}`,timestamp:_,transport:this.transport,reason:M?"incident":"periodic",window_ms:Math.min(60000,Math.max(0,typeof s==="number"&&Number.isFinite(s)?s:this.previousAt===void 0?0:m-this.previousAt)),metrics:f,states:u};if(this.previousAt=m,M&&!this.incident)this.incident={sample:a,context:this.previous};if(m-this.lastEmit>=5000){if(this.incident){if(this.incident.context)this.enqueue({...this.incident.context,reason:"incident_context"});this.enqueue(this.incident.sample),this.incident=void 0}else this.enqueue(a);this.lastEmit=m}this.previous=a}enqueue(h){if(this.history.push(h),this.history.length>24)this.history.shift();if(this.pending.push(h),this.pending.length>8)this.pending.shift()}recent(){return this.history.map((h)=>({...h,metrics:{...h.metrics},states:{...h.states}}))}drain(){return this.pending.splice(0,2)}}function bt(h){let t={...h,metrics:{},states:{},part_index:0,part_count:16},m=[],_=t;for(let s of["metrics","states"])for(let[f,u]of Object.entries(h[s])){let M={..._,[s]:{..._[s],[f]:u}};if(JSON.stringify(M).length>768)m.push(_),_={...t,metrics:{},states:{},[s]:{[f]:u}};else _=M}if(m.push(_),m.length>16)return[];return m.map((s,f)=>({...s,part_index:f,part_count:m.length}))}var Lt=["receiver_loss_delta","remote_receiver_loss_delta","receiver_concealed_delta_ms","receiver_ssrc","remote_receiver_ssrc","rtt_ms","queue_ms","target_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealedSamples","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_jitter","buffered_bytes","main_thread_max_pause_ms","worker_max_tick_gap_ms","receiver_processing_interval_ms","path_revision","stats_errors"];function zt(h){let t={};for(let m of Lt)if(h.metrics[m]!==void 0){if(t[m]=h.metrics[m],bt({...h,metrics:t}).length>2)delete t[m]}return{...h,metrics:t}}class Rh{send;sendReport;now;auxiliary=[];skippedAuxiliary=0;skippedSamples=0;auxiliaryTurn=!1;pending=[];queuedAt=new Map;lastFull=-1/0;timer;report;reportAt=0;static maxAgeMS=12000;constructor(h,t,m=()=>performance.now()){this.send=h;this.sendReport=t;this.now=m}enqueueReport(h){if(this.report!==void 0)this.skippedAuxiliary++;this.report=h,this.reportAt=this.now(),this.start()}enqueueAuxiliary(h){let t=!!h?.diagnostics?.timing?.runtime;if(t){let m=this.auxiliary.length;this.auxiliary=this.auxiliary.filter((_)=>!_.runtime),this.skippedAuxiliary+=m-this.auxiliary.length}if(this.auxiliary.push({report:h,at:this.now(),runtime:t}),this.auxiliary.length>16)this.auxiliary.shift(),this.skippedAuxiliary++;this.start()}start(){if(!this.timer)this.timer=setInterval(()=>this.tick(),250)}discard(h){this.pending=this.pending.filter((t)=>t.id!==h),this.queuedAt.delete(h),this.skippedSamples++}enqueue(h){let t=this.now(),m=[...h].reverse().find((_)=>_.reason==="incident")??h.at(-1);if(this.skippedSamples+=Math.max(0,h.length-(m?1:0)),m){let _=m.reason==="periodic"&&t-this.lastFull>=30000;if(_)this.lastFull=t;let s=_?m:zt(m);s={...s,metrics:{...s.metrics,telemetry_skipped_samples:this.skippedSamples,telemetry_skipped_auxiliary:this.skippedAuxiliary}};let f=bt(s),u=this.pending[0]?.id;for(let M of this.queuedAt.keys())if(M!==u)this.discard(M);if(this.queuedAt.has(s.id))this.discard(s.id);if(f.length)this.pending.push(...f),this.queuedAt.set(s.id,t)}if(this.pending.length)this.start()}tick(){let h=this.now();for(let[t,m]of this.queuedAt)if(h-m>Rh.maxAgeMS)this.discard(t);if(this.auxiliary=this.auxiliary.filter((t)=>{if(h-t.at>Rh.maxAgeMS)return this.skippedAuxiliary++,!1;return!0}),this.report!==void 0&&h-this.reportAt>Rh.maxAgeMS)this.report=void 0,this.skippedAuxiliary++;try{if(this.report!==void 0&&this.sendReport){if(this.sendReport(this.report,Math.max(0,h-this.reportAt)))this.report=void 0}else if(this.auxiliary.length&&this.sendReport&&(!this.pending.length||this.auxiliaryTurn)){if(this.sendReport(this.auxiliary[0].report,Math.max(0,h-this.auxiliary[0].at)))this.auxiliary.shift(),this.auxiliaryTurn=!1}else if(this.pending.length){let t=this.pending[0],m={...t.metrics,telemetry_queue_age_ms:Math.max(0,h-(this.queuedAt.get(t.id)??h))};if(this.send({...t,metrics:m})){if(this.pending.shift(),t.part_index===t.part_count-1)this.queuedAt.delete(t.id);this.auxiliaryTurn=!0}}}catch{}if(!this.pending.length&&!this.auxiliary.length&&this.report===void 0)clearInterval(this.timer),this.timer=void 0}stop(){clearInterval(this.timer),this.timer=void 0,this.pending=[],this.queuedAt.clear(),this.auxiliary=[],this.report=void 0}}class $h{emit;now;timestamp;lastTick;suspendedAt;contextState;environmentCleanup=[];counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(h,t=()=>performance.now(),m=()=>new Date().toISOString()){this.emit=h;this.now=t;this.timestamp=m}observeEnvironment(){this.stopEnvironment();let h=(t,m,_)=>{if(!t)return;try{t.addEventListener(m,_),this.environmentCleanup.push(()=>t.removeEventListener(m,_))}catch{}};try{if(typeof document<"u"){h(document,"visibilitychange",()=>this.report("tab_visibility",document.visibilityState));for(let t of["freeze","resume"])h(document,t,()=>this.report("page_lifecycle",t));this.report("tab_visibility",document.visibilityState)}if(typeof window<"u")for(let t of["pagehide","pageshow"])h(window,t,()=>this.report("page_lifecycle",t));if(typeof navigator<"u")h(navigator.mediaDevices,"devicechange",()=>this.report("microphone","devices_changed"))}catch{}}stopEnvironment(){for(let h of this.environmentCleanup.splice(0))try{h()}catch{}}tick(){let h=this.now();if(this.lastTick!==void 0){let t=Math.max(0,h-this.lastTick-1000);if(t>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,t),this.report("main_thread","scheduling_gap",t)}this.lastTick=h}context(h){if(h===this.contextState)return;this.contextState=h;let t=this.now();if(h==="suspended"||h==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=t,this.counters.audio_context_suspend_count++;this.report("audio_context",h)}else{let m;if(this.suspendedAt!==void 0)m=Math.max(0,t-this.suspendedAt),this.counters.audio_context_suspended_ms+=m,this.suspendedAt=void 0;this.report("audio_context",h,m)}}report(h,t,m){try{this.emit({timestamp:this.timestamp(),action:h,outcome:t,duration_ms:m===void 0?void 0:Math.round(m)})}catch{}}}async function j(h,t,m){let _=performance.now(),s=(f)=>{try{t?.({timestamp:new Date().toISOString(),action:h,outcome:f,duration_ms:Math.round(Math.max(0,performance.now()-_))})}catch{}};s("started");try{let f=await m();return s("completed"),f}catch(f){throw s("failed"),f}}var Ct=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function Kh(h){let t=(h??"FR").trim().toUpperCase();return Ct[t]??Ct.FR}function et(h,t){let m=[];if(!(h.cadence.reduce((f,u)=>f+u,0)>0)||!(t>0))return m;let s=0;while(s<t)for(let f=0;f<h.cadence.length;f+=2){let u=h.cadence[f]??0,M=h.cadence[f+1]??0;if(s>=t)break;if(u>0)m.push({start:s,end:Math.min(s+u,t)});s+=u+M}return m}function kh(h,t,m,_=0.12){let s=h.createGain();s.gain.value=0,s.connect(t);let f=_/Math.max(1,m.frequencies.length),u=m.frequencies.map((C)=>{let w=h.createOscillator();return w.type="sine",w.frequency.value=C,w.connect(s),w.start(),w}),M=h.currentTime+0.05,a=0,i=0.01,q=()=>{let C=h.currentTime-M+10;if(C<=a)return;for(let w of et(m,C)){if(w.end<=a)continue;let b=M+Math.max(w.start,a),x=M+w.end;s.gain.setValueAtTime(0,b),s.gain.linearRampToValueAtTime(f,b+i),s.gain.setValueAtTime(f,Math.max(b+i,x-i)),s.gain.linearRampToValueAtTime(0,x)}a=C};q();let y=setInterval(q,4000),S=!1;return()=>{if(S)return;S=!0,clearInterval(y);try{s.gain.cancelScheduledValues(0)}catch{}s.gain.value=0;for(let C of u){try{C.stop()}catch{}C.disconnect()}s.disconnect()}}var yh=24000,Xh=60;async function Et(h,t){try{await h.audioWorklet.addModule(t)}catch(m){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:m})}}function Jt(h){let t=new Int16Array(h.length);for(let m=0;m<h.length;m++){let _=Math.max(-1,Math.min(1,h[m]));t[m]=_<0?_*32768:_*32767}return t.buffer}class wh{history=new Float32Array(64);phase=0;process(h,t,m){if(t===m)return h;let _=new Float32Array(64+h.length);_.set(this.history),_.set(h,64);let s=t/m,f=Math.min(1,m/t)*0.9,u=[],M=this.phase;for(;M<h.length;M+=s){let a=M+32,i=0,q=0;for(let y=Math.ceil(a-32);y<=Math.floor(a+32);y++){let S=y-a,w=(Math.abs(S)<0.000000001?f:Math.sin(Math.PI*f*S)/(Math.PI*S))*(0.5+0.5*Math.cos(Math.PI*S/32));if(y>=0&&y<_.length)i+=_[y]*w,q+=w}u.push(q?i/q:0)}return this.phase=M-h.length,this.history=_.slice(-64),new Float32Array(u)}}function Zt(h){let t=0;for(let m=0;m<h.length;m++)t+=h[m]*h[m];return Math.sqrt(t/Math.max(1,h.length))}function xt(h,t){let m=new Map(h.map((_)=>[_.id,_]));for(let _ of t){let s=m.get(_.id);if(!s||(!s.complete||_.complete)&&(!s.ended_at||!!_.ended_at)&&_.duration_ms>=s.duration_ms)m.set(_.id,{..._})}return[...m.values()].sort((_,s)=>_.started_at.localeCompare(s.started_at)).slice(-100)}function Dt(h,t){return h.map((m)=>m.ended_at?m:{...m,ended_at:m.observed_until,end_reason:t,complete:!1})}var _h={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function v(h){if(h.mediaTransport!==void 0&&!["websocket","webrtc","auto"].includes(h.mediaTransport))throw RangeError("Unsupported softphone media transport");if(vh(h),h.webrtcFec!==void 0&&typeof h.webrtcFec!=="boolean")throw RangeError("webrtcFec must be boolean");let t=h.playbackTargetMs??Xh,m=h.playbackMinMs??Math.min(Xh,t),_=h.playbackMaxMs??280;for(let s of[t,m,_])if(!Number.isFinite(s)||s<40||s>280)throw RangeError("Playback buffers must be between 40 and 280 ms");if(m>t||t>_)throw RangeError("Playback buffers require min <= target <= max");if(h.playbackAdaptive!==void 0&&typeof h.playbackAdaptive!=="boolean")throw RangeError("playbackAdaptive must be boolean");return{initialTargetMs:t,minTargetMs:m,maxTargetMs:_,hardMaxMs:320,adaptiveReserve:h.playbackAdaptive!==!1}}function Sh(h){return{...h.inputDeviceId?{deviceId:{exact:h.inputDeviceId}}:{},echoCancellation:h.echoCancellation,noiseSuppression:h.noiseSuppression,autoGainControl:h.autoGainControl}}function Rt(h){let t=h.getSettings();return{deviceLabel:h.label||"Default microphone",sampleRate:typeof t.sampleRate==="number"?t.sampleRate:null,channelCount:typeof t.channelCount==="number"?t.channelCount:null,echoCancellation:typeof t.echoCancellation==="boolean"?t.echoCancellation:null,noiseSuppression:typeof t.noiseSuppression==="boolean"?t.noiseSuppression:null,autoGainControl:typeof t.autoGainControl==="boolean"?t.autoGainControl:null}}function Qh(h){if(!Number.isFinite(h)||h<=0)return null;return 20*Math.log10(h)}function Kt(h,t,m){let _=new ArrayBuffer(44+t*2),s=new DataView(_),f=(M,a)=>{for(let i=0;i<a.length;i++)s.setUint8(M+i,a.charCodeAt(i))};f(0,"RIFF"),s.setUint32(4,36+t*2,!0),f(8,"WAVE"),f(12,"fmt "),s.setUint32(16,16,!0),s.setUint16(20,1,!0),s.setUint16(22,1,!0),s.setUint32(24,m,!0),s.setUint32(28,m*2,!0),s.setUint16(32,2,!0),s.setUint16(34,16,!0),f(36,"data"),s.setUint32(40,t*2,!0);let u=44;for(let M of h)for(let a=0;a<M.length;a++)s.setInt16(u,M[a],!0),u+=2;return new Blob([_],{type:"audio/wav"})}class tt{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new wh;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(h,t=!0){this.onLevel=h;this.recordAudio=t}async start(h,t){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:Sh(t)}),this.ensureOpen();let m=this.stream.getAudioTracks()[0];if(!m)throw Error("No microphone audio track was returned.");this.settings=Rt(m);try{this.ctx=new AudioContext({sampleRate:yh,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await Et(this.ctx,h),this.ensureOpen();let _=this.ctx.sampleRate,s=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:t.inputGainDB,highpassFilter:t.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(f)=>{if(this.stopped)return;if(!(f.data instanceof Float32Array)){if(f.data.type==="capture.stats")this.peak=Math.max(this.peak,f.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,f.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,f.data.limiter_reduction_db??0);return}let u=f.data;if(_!==yh)u=this.resampler.process(u,_,yh);let M=Zt(u);if(this.onLevel?.(M),this.recordAudio)this.frames.push(new Int16Array(Jt(u)));if(this.samples+=u.length,M>=0.005){for(let a=0;a<u.length;a++)this.activeSquares+=u[a]*u[a];this.activeSamples+=u.length}},s.connect(this.capture),this.settings}catch(m){throw await this.release(),m}}async stop(){this.stopped=!0;let h=this.settings,t=this.frames,m=this.samples,_=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,s=this.peak;if(await this.release(),!h||m===0)throw Error("No microphone audio was captured.");return{audio:Kt(t,m,yh),durationMs:Math.round(m*1000/yh),sampleRate:yh,activeRmsDbfs:Qh(_),peakDbfs:Qh(s),postPeakDbfs:Qh(this.postPeak||s),limiterReductionDb:this.limiterReductionDB,settings:h}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let h of this.stream?.getTracks()??[])h.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class mt{callbacks;clientEpoch=crypto.randomUUID();transportTelemetry=new Vh("websocket");transportSender=new Rh((h)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[h]}})}),!0},(h)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify(h)}),!0});worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new $h((h)=>this.recordSessionEvent(h));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;sessionReady=!1;mediaPathReady;mediaRevision=0;callAnswered=!1;playbackHeld=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={mediaTransport:"websocket",codec:"pcm16",rttMs:null,queueMs:0,targetMs:Xh,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:yh,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:_h.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(h={}){this.callbacks=h}get isMuted(){return this.muted}async start(h,t,m,_=_h){let s=v(_);this.diagnostics.targetMs=s.initialTargetMs,this.callbacks.onState?.("connecting"),this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+Oh.replaceAll(".","_")}),this.runtimeTelemetry.observeEnvironment(),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{this.stream=await j("microphone_device",(a)=>this.recordSessionEvent(a),()=>navigator.mediaDevices.getUserMedia({audio:Sh(_)})),this.ensureOpen();let f=this.stream.getAudioTracks()[0];if(!f)throw Error("No microphone audio track was returned.");if(f.readyState==="ended")throw Error("Microphone disconnected before audio setup.");f.onmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser.")},f.onunmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),this.callbacks.onNotice?.("Microphone input restored.")},f.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let u=Rt(f);this.diagnostics={...this.diagnostics,microphoneSampleRate:u.sampleRate??0,microphoneChannelCount:u.channelCount??0,echoCancellation:u.echoCancellation,noiseSuppression:u.noiseSuppression,autoGainControl:u.autoGainControl,micInputGainDb:_.inputGainDB};try{this.ctx=new AudioContext({sampleRate:yh,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await j("audio_context",(a)=>this.recordSessionEvent(a),()=>this.ctx.resume());if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),_.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(_.outputDeviceId),this.ensureOpen();await j("audio_worklet",(a)=>this.recordSessionEvent(a),()=>Et(this.ctx,t)),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let M=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:_.inputGainDB,highpassFilter:_.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:{...s,telemetryEpoch:this.clientEpoch,telemetryEnabled:!1}}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),M.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=_.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await j("audio_transport",(a)=>this.recordSessionEvent(a),()=>this.openWorker(h,m)),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.setPlaybackObservation(this.ctx?.state==="running"&&this.microphoneTransportReady&&!this.playbackHeld,"audio_context_paused"),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running")this.updateLiveState()},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(f){if(this.closed)this.teardown();if(!this.closed)this.fail(f instanceof Error?f.message:"browser audio setup failed");throw f}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){await this.ctx?.resume(),this.updateLiveState()}setOutputVolume(h){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,h))}sendDTMF(h){if(/^[0-9*#]+$/.test(h))this.sendText(JSON.stringify({type:"dtmf",digits:h}))}startRingback(h){if(this.closed||!this.ctx||this.ringback)return;this.ringback=kh(this.ctx,this.ctx.destination,Kh(h))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(h)=>{let t=h.data;if(t?.type!=="capture.stats")return;this.micLevel=this.muted?0:t.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:Qh(t.active_rms??0),micPeakDbfs:Qh(t.pre_peak??0),micPostPeakDbfs:Qh(t.post_peak??0),micInputGainDb:t.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:t.limiter_reduction_db??0}},this.playback.port.onmessage=(h)=>{let t=h.data;if(t?.type==="playback.underrun"){this.diagnostics.underrunEvents=xt(this.diagnostics.underrunEvents??[],[t.event]),this.diagnostics.underrunDurationMs=t.underrun_ms,this.diagnostics.underruns=t.underruns;return}if(t?.type!=="stats")return;this.diagnostics.playbackEvents=t.playback_events,this.playbackTiming={reserve_expanded_ms:t.reserve_expanded_ms,reserve_compressed_ms:t.reserve_compressed_ms,reserve_adjustments:t.reserve_adjustments,reserve_match_rejections:t.reserve_match_rejections,played_ms:t.played_ms,max_residence_ms:t.max_residence_ms,drop_totals_ms:t.drop_totals_ms,coaching:{played_ms:t.whisper_played_ms,dropped_ms:t.whisper_dropped_ms,max_queue_ms:t.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,t.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:t.whisper_played_ms??0,coachingDroppedMs:t.whisper_dropped_ms??0,coachingMaxQueueMs:t.whisper_max_queue_ms??0,queueMs:t.queue_ms??0,targetMs:t.target_ms??Xh,underruns:t.underruns??0,droppedMs:t.dropped_ms??0,underrunDurationMs:t.underrun_ms??0,underrunEvents:xt(this.diagnostics.underrunEvents??[],t.underrun_events??[]),maxQueueMs:t.max_queue_ms??0,playbackSequenceGaps:t.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,t.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(h,t){return new Promise((m,_)=>{let s=new Worker(t);this.worker=s;let f=new MessageChannel,u=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:f.port1},[f.port1]),this.playback?.port.postMessage({type:"transport",port:u.port1},[u.port1]);let M=!1,a=setTimeout(()=>{if(M)return;M=!0,_(Error("audio connection timed out"))},1e4),i=(q)=>{if(M)return;if(M=!0,clearTimeout(a),q)_(q);else m()};this.cancelWorkerStart=()=>i(Error("audio session closed")),s.onmessage=(q)=>{if(this.closed){i(Error("audio session closed"));return}let y=q.data;if(y?.type==="socket.open")this.reportedPlaybackIDs.clear(),this.reportedCaptureQueueIDs.clear(),this.opened=!0,this.mediaSocketConnected=!0,this.mediaPathReady=void 0,this.mediaRevision=0,this.sessionReady=!1,this.startRTTProbe(),i();else if(y?.type==="runtime.event")this.recordSessionEvent(y.event);else if(y?.type==="socket.message")this.handleControl(y.data);else if(y?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL(y.recovery).then((S)=>{if(!this.closed&&this.worker===s)s.postMessage({type:"socket.credentials",id:y.id,mediaURL:S})},(S)=>{if(this.closed||this.worker!==s)return;let C=th(S);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:C.denied?"revoked":"retrying",...y.recovery,status:C.status,code:C.code}),s.postMessage({type:"socket.credentials",id:y.id,denied:C.denied})})}else if(y?.type==="socket.error")this.recordSessionEvent({timestamp:y.timestamp,action:"websocket",outcome:"transport_error"});else if(y?.type==="socket.close")if(this.recordSessionEvent({timestamp:y.timestamp,action:"websocket",outcome:"closed",code:String(y.code),detail:y.reason,was_clean:y.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"transport_disconnected"),this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else i(Error("audio connection closed before it was ready"));else if(y?.type==="socket.failed")this.mediaSocketConnected=!1,i(Error(y.detail||"audio connection lost")),this.fail(y.detail||"audio connection lost",y.recoverable===!0);else if(y?.type==="transport.drop"&&y.event)this.diagnostics.dropEvents=this.mergeDropEvents(y.event);else if(y?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=y.buffered_bytes??0,this.diagnostics.captureQueueEvents=y.capture_queue_events,this.transportTiming=y.timing??{},y.observation)this.transportTelemetry.observe({...this.transportTiming,...y.observation,rtt_ms:y.timing?.rtt_ms,queue_ms:this.diagnostics.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:y.buffered_bytes,underruns:this.diagnostics.underruns,dropped_ms:this.diagnostics.droppedMs,capture_sequence_gaps:this.diagnostics.captureSequenceGaps,playback_sequence_gaps:this.diagnostics.playbackSequenceGaps,main_thread_max_pause_ms:this.runtimeTelemetry.counters.main_thread_max_pause_ms},{connection:this.mediaSocketConnected?"connected":"reconnecting",context:this.ctx?.state,muted:String(this.muted),device_muted:String(this.stream?.getAudioTracks()[0]?.muted),track:this.stream?.getAudioTracks()[0]?.readyState,codec:"pcm16"},performance.now(),y.observation?.timestamp,y.observation?.window_ms);if(this.diagnostics.transportSamples=this.transportTelemetry.recent(),typeof y.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(y.timing.rtt_ms)}},s.onerror=(q)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:q.message?.slice(0,160)}),i(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},s.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:h,contextRate:this.ctx?.sampleRate??yh,muted:this.muted,capturePort:f.port2,playbackPort:u.port2},[f.port2,u.port2])})}mergeDropEvents(h,t){if(h)this.workerDropEvents=[...this.workerDropEvents,h].slice(-100);if(t)this.playbackDropEvents=t.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((m,_)=>m.timestamp.localeCompare(_.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;shutdownIntent="session_cleanup";reportedPlaybackIDs=new Set;reportedCaptureQueueIDs=new Set;handleControl(h){if(this.closed)return;try{let t=JSON.parse(h);if(t.type==="media.ready"||t.type==="media.not_ready"){let m=t;if(!Number.isSafeInteger(m.revision)||m.revision<=this.mediaRevision)return;this.mediaRevision=m.revision,this.mediaPathReady=m.type==="media.ready";try{this.callbacks.onMediaReadiness?.(m)}catch{}if(!this.mediaPathReady&&this.callAnswered)this.callbacks.onState?.("reconnecting","Connecting carrier audio…");this.updateLiveState()}else if(t.type==="ready")this.sessionReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState();else if(t.type==="dtmf.error"||t.type==="dtmf.sent")this.callbacks.onNotice?.(t.type==="dtmf.sent"?"Keypad tone sent":t.detail||"Keypad tone failed");else if(t.type==="pong"){if(this.diagnostics.captureSequenceGaps=t.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof t.nonce==="number"&&t.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-t.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(t.type==="call.ended"||t.type==="session.replaced"){this.setPlaybackObservation(!1,t.type==="call.ended"?"call_ended":"observation_ended"),this.shutdownIntent=t.type==="call.ended"?"call_ended":"session_replaced",this.closed=!0;try{this.callbacks.onState?.("ended",t.type)}finally{this.teardown()}}else if(t.type==="call.status"&&typeof t.call_id==="string"&&typeof t.status==="string"){if(t.hold_state)this.playbackHeld=t.hold_state!=="active",this.setPlaybackObservation(!this.playbackHeld&&this.microphoneTransportReady&&this.ctx?.state==="running",this.playbackHeld?"hold":"observation_resumed");if(t.hold_state&&t.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callAnswered=["answered","in-progress"].includes(t.status),this.updateLiveState(),this.callbacks.onCallStatus?.(t)}else if(t.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:t.detail?.slice(0,160)}),this.fail(t.detail||"The call could not be connected.");else if(t.type==="coach.state")this.callbacks.onNotice?.(t.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(t.type==="audio.health"){let m=t;if(m.state!=="healthy"&&m.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:m.state,reason:m.reason,stages:m.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),m.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&m.state==="healthy"&&m.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(t.type==="media.delivery"){let m=t.state;if(m==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(m==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=m==="stalled"}else if(t.type==="peer.disconnected")this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"carrier_disconnected"),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(t.type==="peer.connected")this.microphoneTransportReady=!0,this.setPlaybackObservation(this.ctx?.state==="running"&&!this.playbackHeld,"carrier_connected"),this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState()}catch{}}updateLiveState(){if(this.sessionReady&&this.callAnswered&&this.microphoneTransportReady&&this.mediaPathReady!==!1&&this.ctx?.state==="running")this.callbacks.onState?.("live")}startRTTProbe(){this.stopRTTProbe();let h=()=>{this.sendDiagnostics()};h(),this.pingTimer=setInterval(h,5000)}sendText(h){this.worker?.postMessage({type:"send.text",data:h})}recordSessionEvent(h){try{h={...this.callbacks.sessionDiagnostics?.(),...h}}catch{}h=mh(h),this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],h].slice(-50);try{this.callbacks.onSessionEvent?.(h)}catch{}try{let t={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,session_events:[h]}};if(new TextEncoder().encode(JSON.stringify(t)).byteLength<=1100)this.transportSender.enqueueAuxiliary(t);else this.transportSender.skippedAuxiliary++}catch{}}sendDiagnostics(){let h=this.diagnostics,t=(s,f)=>{let u=(s??[]).slice(-64),M=u.filter((a)=>!f.has(a.id)).slice(0,8);if(this.mediaSocketConnected){let a=new Set(u.map((i)=>i.id));for(let i of f)if(!a.has(i))f.delete(i);for(let i of M)f.add(i.id)}return M},m=t(h.playbackEvents,this.reportedPlaybackIDs),_=t(h.captureQueueEvents,this.reportedCaptureQueueIDs);if(this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{media_transport:"websocket",codec:"pcm16",client_version:Oh,client_epoch:this.clientEpoch,playback_events:m,capture_queue_events:_,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:h.rttMs,playback_queue_ms:h.queueMs,playback_target_ms:h.targetMs,playback_max_queue_ms:h.maxQueueMs,playback_underruns:h.underruns,playback_underrun_ms:h.underrunDurationMs,playback_underrun_events:h.underrunEvents,playback_dropped_ms:h.droppedMs,websocket_buffered_bytes:h.websocketBufferedBytes,audio_context_rate:h.audioContextRate,microphone_sample_rate:h.microphoneSampleRate,microphone_channel_count:h.microphoneChannelCount,echo_cancellation:h.echoCancellation,noise_suppression:h.noiseSuppression,auto_gain_control:h.autoGainControl,mic_active_rms_dbfs:h.micActiveRmsDbfs,mic_peak_dbfs:h.micPeakDbfs,mic_post_peak_dbfs:h.micPostPeakDbfs,mic_input_gain_db:h.micInputGainDb,mic_limiter_reduction_db:h.micLimiterReductionDb,capture_sequence_gaps:h.captureSequenceGaps,playback_sequence_gaps:h.playbackSequenceGaps,drop_events:h.dropEvents}})),this.mediaSocketConnected)this.transportSender.enqueue(this.transportTelemetry.drain());this.callbacks.onDiagnostics?.({...h})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setPlaybackObservation(h,t){try{if(this.playback?.port.postMessage({type:"playback.telemetry.boundary",active:h,reason:t,audio_time_ms:(this.ctx?.currentTime??0)*1000}),!h)this.diagnostics.underrunEvents=Dt(this.diagnostics.underrunEvents??[],t)}catch{}}setMuted(h){let t=this.muted!==h;if(this.muted=h,this.worker?.postMessage({type:"muted",value:h}),this.capture?.port.postMessage({type:"muted",value:h}),h)this.sendText(JSON.stringify({type:"interrupt"}));if(t)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:h?"muted":"unmuted"})}stop(){this.shutdownIntent="user_stop";let h=!this.closed;this.closed=!0;try{if(h)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(h,t=!1){if(this.shutdownIntent="audio_error",!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:h.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",h,{recoverable:t})}finally{this.teardown()}}teardown(){this.runtimeTelemetry.stopEnvironment(),this.transportSender.stop(),this.setPlaybackObservation(!1,"observation_ended"),this.stopRingback(),this.microphoneTransportReady=!1,this.sessionReady=this.callAnswered=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let h=this.worker;if(h?.postMessage({type:"close",reason:this.shutdownIntent}),h){let t=setTimeout(()=>h.terminate(),600);h.onmessage=(m)=>{if(m.data?.type==="socket.shutdown.complete")clearTimeout(t),h.terminate()}}this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((t)=>t.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var J0=yh*Xh/1000;var Ih=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
`;var Ft=`// Passive receiver only. Both directions share one rendering clock and delay.
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
        // Overflow must make progress immediately. Repeated arrivals must not
        // re-prime the queue into the future before its first sample can play.
        // Never postpone an already scheduled head, including a partially
        // played frame when only the tail exceeded the scheduling bound.
        const head = queue[0];
        let next = Math.min(head.start + (head.played || 0), Math.round((now + (trimmed ? 0 : Math.max(40, this.targetMS - 20))) * sampleRate / 1000));
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
`;var _t=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
`;var Tt=new WeakMap,jh=new Map,ct=8000;async function Yh(h,t){let m=t==="worklet"?Ih:t==="worker"?_t:Ft;if(typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin){let i=[URL.createObjectURL(new Blob([m],{type:"text/javascript"}))];return{urls:i,dispose(){i.splice(0).forEach((q)=>URL.revokeObjectURL(q))}}}if(h.name!=="telephony"||!h.projectId||!Number.isSafeInteger(h.installId)||h.installId<=0)throw Error("Telephony audio requires a project and installation");let _=new URL(h.mcpURL(),location.href),s=`${_.origin}:${_.pathname}:${h.projectId}:${h.installId}`,f=Tt.get(h);if(!f||f.scope!==s)f={scope:s,entries:new Map},Tt.set(h,f);let u=f.entries,M=t,a=u.get(M);if(!a){a=(async()=>{let q=jh.get(t);if(!q)q=crypto.subtle.digest("SHA-256",new TextEncoder().encode(m)).then((w)=>Array.from(new Uint8Array(w),(b)=>b.toString(16).padStart(2,"0")).join("")),jh.set(t,q),q.catch(()=>{if(jh.get(t)===q)jh.delete(t)});let y=`/ui/frontend/${t}-${await q}.js`,S=new AbortController,C;try{if(await Promise.race([h.get(y,{cache:"force-cache",redirect:"error",signal:S.signal,headers:{Accept:"text/plain"}}),new Promise((x,A)=>{C=setTimeout(()=>{A(Error("Telephony audio asset preparation timed out")),S.abort()},ct)})])!==m)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let b=new URL(`/api/apps/telephony/_install/${h.installId}${y}`,_);return b.searchParams.set("project_id",h.projectId),b.searchParams.set("install_id",String(h.installId)),b.href}finally{clearTimeout(C)}})(),u.set(M,a);let i=a;i.catch(()=>{if(u.get(M)===i)u.delete(M)})}return{urls:[await a],dispose(){}}}function Lh(h=!1){let t=(h?[Ih]:[Ih,_t]).map((m)=>URL.createObjectURL(new Blob([m],{type:"text/javascript"})));return{urls:t,dispose(){t.splice(0).forEach((m)=>URL.revokeObjectURL(m))}}}async function Ah(h,t=!1){if(typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin)return Lh(t);let m=await Promise.all(t?[Yh(h,"worklet")]:[Yh(h,"worklet"),Yh(h,"worker")]);return{urls:m.flatMap((_)=>_.urls),dispose(){m.forEach((_)=>_.dispose())}}}class zh{options;target;bitrate=32000;stable;last=-1/0;initial;maximum;constructor(h={}){this.options=h;this.initial=h.playbackTargetMs??60,this.target=this.initial,this.maximum=Math.max(this.initial,h.playbackMaxMs??160)}observe(h,t=performance.now()){if(t<this.last)this.last=-1/0,this.stable=void 0;if(t-this.last<2000)return;this.last=t;let m=h.receiver_jitter===void 0?void 0:h.receiver_jitter*1000,_=h.receiver_concealed_delta_ms??0;if(this.options.playbackAdaptive!==!1){let u=Math.min(this.maximum,Math.max(this.initial,this.initial+Math.ceil(Math.max(0,(m??0)-10)*2/20)*20,_>0?this.target+20:this.initial));if(u>this.target)this.target=Math.min(u,this.target+20),this.stable=void 0;else if(m!==void 0&&_===0&&m<10){if(this.stable??=t,t-this.stable>=1e4)this.target=Math.max(this.initial,this.target-10),this.stable=t}else this.stable=void 0}let{pair_availableOutgoingBitrate:s,remote_receiver_fractionLost:f}=h;if(s!==void 0&&s>0&&s<64000)this.bitrate=Math.max(16000,Math.min(32000,Math.floor((s-32000)*0.8/1000)*1000));else if(f!==void 0&&f>=0.03)this.bitrate=Math.max(16000,this.bitrate-4000);else if(f!==void 0&&f<0.01)this.bitrate=Math.min(32000,this.bitrate+2000)}}class st{tokens=1100;at;rate=640;observeAvailableBitrate(h){this.rate=h!==void 0&&h>=96000?2000:640}consume(h,t=performance.now()){if(!Number.isFinite(h)||!Number.isFinite(t)||h<0||h>1100)return!1;if(this.at!==void 0)this.tokens=Math.min(1100,this.tokens+Math.max(0,t-this.at)*this.rate/1000);if(this.at=t,this.tokens<h)return!1;return this.tokens-=h,!0}}function ch(h){if(h===void 0)return"websocket";if(!["websocket","webrtc","auto"].includes(h))throw RangeError("Unsupported softphone media transport");return h}function ut(h,t,m={}){if(t!==void 0&&typeof t!=="boolean")throw RangeError("webrtcFec must be boolean");let _=new URL(h);if(At(_,m),_.searchParams.set("transport","webrtc"),_.searchParams.delete("webrtc_fec"),t!==void 0)_.searchParams.set("webrtc_fec",String(t));return _.toString()}var D=(h)=>{try{h?.()}catch{}},qh=(h,t=1000000000000)=>typeof h==="number"&&Number.isFinite(h)?Math.max(0,Math.min(h,t)):0;function dt(h){return h===!0?"relay_configured":h===!1?"no_relay":"unreported"}function eh(h){let t=th(h);return["turn_relay_unavailable","turn_client_update_required","turn_relay_required"].includes(h?.code??t.code)||t.denied||t.code==="media_session_changed"||t.code==="call_ended"||t.code==="call_not_attachable"||t.expired||["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes(h?.name)}function ft(h,t){let m=0,_=0,s=0,f=0,u=0,M=0,a=0,i=0,q=0,y=null,S,C,w,b={},x={},A={},g=(P,E)=>typeof P?.[E]==="number"&&Number.isFinite(P[E])?Math.max(0,P[E]):void 0,F=(P,E,O)=>{for(let T of O){let Q=g(P,T);if(Q!==void 0)x[E+T]=Q}},J=(P,E,O,T,Q)=>{let $=g(P,O),V=g(P,T),Z=t?.counters?.[E];if($!==void 0&&V!==void 0){if(b[E]??={},b[E][O]=$,b[E][T]=V,g(P,"ssrc")!==void 0)b[E].ssrc=P.ssrc;if(Z&&Z.ssrc===g(P,"ssrc")&&$>=Z[O]&&V>Z[T])x[Q]=($-Z[O])/(V-Z[T])*1000}},G=(P,E,O)=>{let T=g(P,"packetsLost"),Q=g(P,"ssrc"),$=g(P,"timestamp"),V=t?.counters?.[E];if(Q!==void 0)x[O+"ssrc"]=Q;if(T===void 0||$===void 0)return;if(b[E]??={},b[E].packetsLost=T,b[E].at=$,Q!==void 0)b[E].ssrc=Q;if(V&&V.ssrc===Q&&T>=V.packetsLost&&$>V.at)x[O+"loss_delta"]=(x[O+"loss_delta"]??0)+(T-V.packetsLost),x[O+"loss_window_ms"]=$-V.at},U=(P,E,O)=>{let T=g(P,"concealedSamples"),Q=g(P,"silentConcealedSamples"),$=g(P,"timestamp"),V=g(P,"ssrc"),Z=t?.counters?.[E];if(T===void 0||$===void 0)return;if(b[E]??={},Object.assign(b[E],{concealedSamples:T,at:$}),V!==void 0)b[E].ssrc=V;if(Q!==void 0)b[E].silentConcealedSamples=Q;if(Z&&Z.ssrc===V&&T>=Z.concealedSamples&&$>Z.at){if(x.receiver_concealed_delta_ms=(x.receiver_concealed_delta_ms??0)+(T-Z.concealedSamples)*1000/O,x.receiver_concealed_window_ms=$-Z.at,Q!==void 0&&Q>=Z.silentConcealedSamples)x.receiver_silent_concealed_delta_ms=(x.receiver_silent_concealed_delta_ms??0)+(Q-Z.silentConcealedSamples)*1000/O}},Y=!1,gh=0;h.forEach((P,E)=>{if(q=Math.max(q,qh(P.timestamp,1000000000000000)),(P.kind??P.mediaType??h.get(P.localId)?.kind)==="audio"&&gh++<32){if(P.type==="outbound-rtp")m+=qh(P.bytesSent),F(P,"sender_",["bytesSent","packetsSent","headerBytesSent","retransmittedPacketsSent","retransmittedBytesSent","nackCount","targetBitrate"]),J(P,E,"totalPacketSendDelay","packetsSent","sender_send_delay_interval_ms");if(P.type==="inbound-rtp"){G(P,E,"receiver_"),_+=qh(P.bytesReceived),s+=qh(P.packetsLost),f=Math.max(f,qh(P.jitter)*1000);let T=h.get(P.codecId),Q=g(T,"clockRate")??48000;if(U(P,E,Q),Y||=g(P,"concealedSamples")!==void 0,u+=qh(P.concealedSamples)*1000/Q,M+=qh(P.packetsDiscarded),a+=qh(P.jitterBufferDelay),i+=qh(P.jitterBufferEmittedCount),F(P,"receiver_",["bytesReceived","packetsReceived","packetsLost","jitter","packetsDiscarded","concealedSamples","silentConcealedSamples","concealmentEvents","insertedSamplesForDeceleration","removedSamplesForAcceleration","totalSamplesReceived","audioLevel","totalAudioEnergy","totalSamplesDuration","nackCount","fecPacketsReceived","fecPacketsDiscarded"]),J(P,E,"jitterBufferDelay","jitterBufferEmittedCount","receiver_jitter_buffer_interval_ms"),J(P,E,"jitterBufferTargetDelay","jitterBufferEmittedCount","receiver_jitter_target_interval_ms"),J(P,E,"jitterBufferMinimumDelay","jitterBufferEmittedCount","receiver_jitter_minimum_interval_ms"),F(P,"receiver_",["totalProcessingDelay","totalSamplesDecoded"]),A.receiver_processing=g(P,"totalSamplesDecoded")===void 0?"unavailable":"per_decoded_sample",J(P,E,"totalProcessingDelay","totalSamplesDecoded","receiver_processing_interval_ms"),g(P,"lastPacketReceivedTimestamp")!==void 0)x.receiver_last_packet_age_ms=Math.max(0,P.timestamp-P.lastPacketReceivedTimestamp);if(T){if(A.codec=T.mimeType,x.codec_clock_rate=Q,g(T,"channels")!==void 0)x.codec_channels=T.channels}}if(P.type==="remote-inbound-rtp")F(P,"remote_receiver_",["packetsLost","fractionLost","jitter","roundTripTime","totalRoundTripTime","roundTripTimeMeasurements"]),G(P,E,"remote_receiver_");if(P.type==="remote-outbound-rtp")F(P,"remote_sender_",["packetsSent","bytesSent"]);let O=P.type==="outbound-rtp"?"bytesSent":P.type==="inbound-rtp"?"bytesReceived":void 0;if(O&&g(P,O)!==void 0)b[E]??={},b[E][O]=P[O],b[E].at=P.timestamp}if(P.type==="transport"&&P.selectedCandidatePairId){S=P.selectedCandidatePairId,A.dtls=P.dtlsState;let O=h.get(S);if(O){y=g(O,"currentRoundTripTime")!==void 0?O.currentRoundTripTime*1000:null,F(O,"pair_",["availableOutgoingBitrate","availableIncomingBitrate","currentRoundTripTime","totalRoundTripTime","bytesSent","bytesReceived","requestsSent","requestsReceived","responsesSent","responsesReceived","consentRequestsSent"]),A.pair=O.state;let T=h.get(O.localCandidateId),Q=h.get(O.remoteCandidateId);if(T)C=T.protocol,w=T.candidateType,A.protocol=C,A.local_candidate=w,A.relay_protocol=T.relayProtocol;if(Q)A.remote_candidate=Q.candidateType}}});let uh=(P,E)=>{if(t?.counters){let T=0;for(let[Q,$]of Object.entries(b)){let V=t.counters[Q];if(V&&$[P]>=V[P]&&$.at>V.at)T+=($[P]-V[P])*8000/($.at-V.at)}return T}let O=t&&q>t.at?(q-t.at)/1000:0;return O?Math.max(0,E-(P==="bytesSent"?t.sent:t.received))*8/O:0},fh=(t?.pathRevision??0)+(S&&S!==t?.pairID?1:0);x.path_revision=fh;let bh=uh("bytesSent",m),Mh=uh("bytesReceived",_);return{previous:{at:q,sent:m,received:_,counters:b,pairID:S,pathRevision:fh},rtt:y,queueMs:x.receiver_jitter_buffer_interval_ms??0,metrics:oh({...x,send_bitrate_bps:bh,receive_bitrate_bps:Mh}),states:ht(A),webrtc:{relayProtocol:A.relay_protocol,protocol:C,candidateType:w,sendBitrateBps:bh,receiveBitrateBps:Mh,packetsLost:s,jitterMs:f,concealedMs:Y?u:void 0,packetsDiscarded:M,jitterBufferMs:i>0?a/i*1000:0,jitterBufferIntervalMs:x.receiver_jitter_buffer_interval_ms}}}function lt(h,t=new Date().toISOString()){let m=[];for(let[_,s]of[["receiver_","carrier_to_operator"],["remote_receiver_","operator_to_carrier"]]){let f=h[_+"loss_delta"];if(f>0)m.push({timestamp:t,direction:s,reason:"webrtc_packet_loss",duration_ms:0,packet_count:f,ssrc:h[_+"ssrc"],window_ms:h[_+"loss_window_ms"]})}if(h.receiver_concealed_delta_ms>0)m.push({timestamp:t,direction:"carrier_to_operator",reason:"webrtc_concealment",duration_ms:Math.round(h.receiver_concealed_delta_ms),ssrc:h.receiver_ssrc,window_ms:h.receiver_concealed_window_ms});return m}class Mt{callbacks;ice;stopped=!1;generation=0;socket;pc;stream;context;capture;whisper;destination;speaker;remoteAudio;speakerMeter;carrierStalled=!1;deliveryDegraded=!1;ready=!1;answered=!1;mediaPathReady;mediaRevision=0;reportedLive=!1;peer=!1;muted=!1;whisperEpoch=null;whisperBase=null;whisperResampler=new wh;timer;retry;cancelSetup;recovering=!1;recoveryGeneration=0;recoveryContext;recoveryExpiry;recoveryRequest;ringback;options;workletURL;events=[];previous;sampling=new WeakSet;transportTelemetry=new Vh("webrtc");transportSender=new Rh((h)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[h]}})},(h,t=0)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let m=h;if(m?.diagnostics?.webrtc){let _=m.diagnostics.webrtc;return this.sendTelemetry({...m,diagnostics:{...m.diagnostics,webrtc:{..._,jitterBufferSampleAgeMs:(_.jitterBufferSampleAgeMs??0)+t}}})}return this.sendTelemetry(h)});controlBudget=new st;quality;playbackReceiver;uploadSender;applyingBitrate=!1;appliedBitrate;statsErrors=0;lastStatsSend=-1/0;lastValidStatsAt=-1/0;clientEpoch=crypto.randomUUID();relayConfigured;nativeCounts;completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};runtime=new $h((h)=>this.recordSessionEvent(h));diagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:null,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(h){this.callbacks=h}async start(h,t,m){if(this.options=t,this.workletURL=m,!m)throw Error("WebRTC requires the shared Telephony audio processor");if(typeof RTCPeerConnection!=="function")throw Error("WebRTC audio is unavailable in this browser");v(t),await this.connect(h)}current(h){return!this.stopped&&this.generation===h}guard(h){if(!this.current(h))throw Error("Audio session cancelled")}async connect(h){this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+Oh.replaceAll(".","_")}),this.runtime.observeEnvironment();let t=++this.generation,m=this.options;for(let S of this.events.slice(-8))this.enqueueRTCEvents({session_events:[S]});for(let S of this.diagnostics.dropEvents.slice(-8))this.enqueueRTCEvents({drop_events:[S]});this.ready=this.peer=this.answered=this.reportedLive=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.previous=void 0,this.quality=new zh(m),this.appliedBitrate=void 0;let _=await j("microphone_device",(S)=>this.recordSessionEvent(S),()=>navigator.mediaDevices.getUserMedia({audio:Sh(m)}));if(!this.current(t))throw _.getTracks().forEach((S)=>S.stop()),Error("Audio session cancelled");this.stream=_;let s=_.getAudioTracks()[0];if(!s)throw Error("No microphone audio track");let f=s.getSettings();this.diagnostics={...this.diagnostics,microphoneSampleRate:f.sampleRate??0,microphoneChannelCount:f.channelCount??1,echoCancellation:typeof f.echoCancellation==="boolean"?f.echoCancellation:null,noiseSuppression:f.noiseSuppression??null,autoGainControl:f.autoGainControl??null,micInputGainDb:m.inputGainDB},s.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),s.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),s.onended=()=>{if(this.current(t))this.disconnected(t,"microphone_ended")},this.context=new AudioContext({latencyHint:"interactive"});let u=this.context;if(u.onstatechange=()=>{this.runtime.context(u.state),this.updateLiveState()},this.runtime.context(u.state),await j("audio_context",(S)=>this.recordSessionEvent(S),()=>u.resume()),this.guard(t),m.outputDeviceId&&"setSinkId"in u)await u.setSinkId(m.outputDeviceId);await j("audio_worklet",(S)=>this.recordSessionEvent(S),()=>u.audioWorklet.addModule(this.workletURL)),this.guard(t),this.diagnostics.audioContextRate=u.sampleRate,this.capture=new AudioWorkletNode(u,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:m.inputGainDB,highpassFilter:m.highpassFilter}}),this.destination=u.createMediaStreamDestination(),this.destination.channelCount=1,u.createMediaStreamSource(_).connect(this.capture).connect(this.destination),this.capture.port.onmessage=(S)=>{if(S.data?.type==="capture.stats"){let C=(b)=>b>0?20*Math.log10(b):null;this.diagnostics.micActiveRmsDbfs=C(S.data.active_rms),this.diagnostics.micPeakDbfs=C(S.data.pre_peak),this.diagnostics.micPostPeakDbfs=C(S.data.post_peak),this.diagnostics.micLimiterReductionDb=S.data.limiter_reduction_db??0;let w=0;if(this.speakerMeter){let b=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(b),w=Math.sqrt(b.reduce((x,A)=>x+A*A,0)/b.length)}D(()=>this.callbacks.onLevels?.(qh(S.data.active_rms,1),w))}},this.speaker=u.createGain(),this.speaker.gain.value=m.outputVolume??1,this.speaker.connect(u.destination),this.speakerMeter=u.createAnalyser(),this.speakerMeter.fftSize=256;let M=u.createGain();M.gain.value=0,this.speaker.connect(this.speakerMeter).connect(M).connect(u.destination),this.whisper=new AudioWorkletNode(u,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:v(m)}),this.whisper.connect(this.speaker),this.gate();let a=new WebSocket(ut(h,m.webrtcFec,m));a.binaryType="arraybuffer",this.socket=a;let i=!1,q=!1,y=!1;await j("audio_transport",(S)=>this.recordSessionEvent(S),()=>new Promise((S,C)=>{let w=(A)=>{if(q)return;q=!0,clearTimeout(b),this.cancelSetup=void 0,A?C(y?Object.assign(A,{code:"turn_relay_required"}):A):S()},b=setTimeout(()=>w(Error("WebRTC audio connection timed out")),18000);this.cancelSetup=()=>w(Error("Audio session cancelled"));let x=()=>{if(this.current(t)&&this.ready&&i)this.gate(),w(),this.updateLiveState()};a.onmessage=(A)=>{if(!this.current(t))return;if(A.data instanceof ArrayBuffer){this.playWhisper(A.data);return}if(typeof A.data!=="string")return;let g;try{g=JSON.parse(A.data)}catch{return}if(g.type==="webrtc.error"){w(Object.assign(Error("Required TURN relay is unavailable"),{code:g.code}));return}if(this.ice?.handle(g))return;if(g.type==="webrtc.config"){if(y=g.ice_transport_policy==="relay",this.relayConfigured=typeof g.relay_configured==="boolean"?g.relay_configured:void 0,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"ice_configuration",outcome:dt(g.relay_configured)}),this.pc){w(Error("Repeated WebRTC configuration"));return}(async()=>{let F=new RTCPeerConnection(Wh(g));this.pc=F,this.ice?.stop(),this.ice=new Hh((G)=>this.send(G),()=>this.current(t),(G,U)=>this.recordSessionEvent({...U,timestamp:new Date().toISOString(),action:"ice_restart",outcome:G}),(G)=>{this.relayConfigured=G.iceServers?.some((U)=>(typeof U.urls==="string"?[U.urls]:U.urls).some((Y)=>/^turns?:/.test(Y)))??!1}),this.ice.register("main",F),this.destination.stream.getAudioTracks().forEach((G)=>{let U=F.addTrack(G,this.destination.stream);this.uploadSender=U,this.applyUploadBitrate(U,32000,t)}),F.ontrack=(G)=>{if(!this.current(t))return;this.playbackReceiver=G.receiver,this.applyPlaybackTarget();let U=new MediaStream([G.track]),Y=new Audio;Y.muted=!0,Y.srcObject=U,this.remoteAudio=Y,Y.play().catch(()=>D(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction."))),u.createMediaStreamSource(U).connect(this.speaker)},F.onconnectionstatechange=()=>{if(!this.current(t))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:F.connectionState}),F.connectionState==="connected")i=!0,x();if(F.connectionState==="failed"||F.connectionState==="closed")if(!q)w(Error("WebRTC connection failed"));else this.disconnected(t,"webrtc_"+F.connectionState)};let J=await F.createOffer();this.guard(t),await F.setLocalDescription(J),await new Promise((G,U)=>{if(F.iceGatheringState==="complete"){G();return}let Y=setTimeout(()=>{F.onicegatheringstatechange=null,U(Error("ICE gathering timed out"))},7000);F.onicegatheringstatechange=()=>{if(F.iceGatheringState==="complete")clearTimeout(Y),F.onicegatheringstatechange=null,G()}}),this.guard(t),a.send(JSON.stringify({type:"webrtc.offer",sdp:F.localDescription.sdp,ice_version:1}))})().catch((F)=>w(F instanceof Error?F:Error("WebRTC setup failed")));return}if(g.type==="webrtc.answer"){this.pc?.setRemoteDescription({type:"answer",sdp:g.sdp}).catch(()=>w(Error("Invalid WebRTC answer")));return}if(g.type==="media.ready"||g.type==="media.not_ready"){if(!Number.isSafeInteger(g.revision)||g.revision<=this.mediaRevision)return;this.mediaRevision=g.revision,this.mediaPathReady=g.type==="media.ready",D(()=>this.callbacks.onMediaReadiness?.(g)),this.updateLiveState()}if(g.type==="ready")this.ready=!0,this.send({type:"media.capabilities",version:2,versions:[3],whisper:!0}),x();if(g.type==="peer.connected")this.peer=!0,this.gate(),x();if(g.type==="peer.disconnected")this.peer=!1,this.gate(),this.updateLiveState(),D(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));if(g.type==="call.status")this.answered=["answered","in-progress"].includes(g.status),x(),D(()=>this.callbacks.onCallStatus?.(g));if(g.type==="call.error")if(!q)w(Error(g.detail??"Carrier activation failed"));else this.stop(),D(()=>this.callbacks.onState?.("error",g.detail));if(g.type==="call.ended"||g.type==="session.replaced")if(!q)w(Error(g.type));else this.stop(),D(()=>this.callbacks.onState?.("ended",g.type));if(g.type==="audio.health"){if(this.diagnostics.audioHealth={state:g.state,reason:g.reason,stages:g.stages},D(()=>this.callbacks.onAudioHealth?.(g)),g.state==="audio_degraded"&&!this.deliveryDegraded)this.deliveryDegraded=!0,D(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));else if(this.deliveryDegraded&&g.state==="healthy"&&g.stages?.telephony_to_browser?.state==="healthy"&&u.state==="running")this.deliveryDegraded=!1,D(()=>this.callbacks.onNotice?.("Speech delivery restored."))}if(g.type==="media.delivery"){if(g.state==="stalled")D(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));else if(g.state==="flowing"&&this.carrierStalled)D(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));this.carrierStalled=g.state==="stalled"}if(g.type==="call.notice")D(()=>this.callbacks.onNotice?.(g.detail??g.reason??"Audio delivery notice"));if(g.type==="coach.state")this.whisperEpoch=g.talking&&Number.isInteger(g.epoch)?g.epoch:null,this.whisperBase=null,this.whisper?.port.postMessage({type:"whisper.clear"})},a.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"}),a.onclose=(A)=>{if(!this.current(t))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(A.code),was_clean:A.wasClean}),!q){w(Object.assign(Error("WebRTC signaling unavailable"),A.code===1008?{status:403}:{}));return}if(A.code===1008){this.stop(),D(()=>this.callbacks.onState?.("error","Media authorization ended"));return}this.disconnected(t,"signaling_closed")}})),this.guard(t),this.timer=setInterval(()=>{this.runtime.tick(),this.statistics(t)},1000),await this.statistics(t)}send(h){if(this.socket?.readyState===WebSocket.OPEN&&this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(h))}catch{}}updateLiveState(){let h=this.ready&&this.peer&&this.answered&&this.mediaPathReady!==!1&&this.pc?.connectionState==="connected"&&this.context?.state==="running";if(h===this.reportedLive)return;this.reportedLive=h,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"media_path",outcome:h?"ready":"waiting"}),D(()=>this.callbacks.onState?.(h?"live":"reconnecting",h?void 0:"Connecting carrier audio…"))}gate(){let h=!this.stopped&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!h}),this.destination?.stream.getAudioTracks().forEach((t)=>t.enabled=h)}async statistics(h){if(!this.current(h)||!this.pc||this.sampling.has(this.pc))return;let t=this.pc,m=performance.now();this.sampling.add(t);try{let _=ft(await t.getStats(),this.previous);if(!this.current(h)||this.pc!==t)return;if(this.previous=_.previous,this.lastValidStatsAt=performance.now(),this.nativeCounts={packetsLost:_.webrtc.packetsLost,packetsDiscarded:_.webrtc.packetsDiscarded,concealedMs:_.webrtc.concealedMs},_.webrtc.packetsLost+=this.completedCounts.packetsLost,_.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded,_.webrtc.concealedMs!==void 0)_.webrtc.concealedMs+=this.completedCounts.concealedMs;if(this.quality?.observe(_.metrics),this.applyPlaybackTarget(),this.uploadSender&&this.quality)this.applyUploadBitrate(this.uploadSender,this.quality.bitrate,h);if(_.metrics.requested_playback_target_ms=this.quality?.target??60,this.appliedBitrate!==void 0)_.metrics.requested_upload_bitrate_bps=this.appliedBitrate;this.controlBudget.observeAvailableBitrate(_.metrics.pair_availableOutgoingBitrate);let s=lt(_.metrics);for(let u of s)this.enqueueRTCEvents({drop_events:[u]});this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,...s].slice(-100),this.diagnostics={...this.diagnostics,rttMs:_.rtt,queueMs:_.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,_.queueMs),targetMs:this.quality?.target??this.options?.playbackTargetMs??60,webrtc:_.webrtc,playbackSequenceGaps:_.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};let f=this.stream?.getAudioTracks()[0];this.transportTelemetry.observe({..._.metrics,rtt_ms:_.rtt,queue_ms:_.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:this.socket?.bufferedAmount??0,stats_errors:this.statsErrors,stats_duration_ms:performance.now()-m,main_thread_max_pause_ms:this.runtime.counters.main_thread_max_pause_ms},{..._.states,ice:t.iceConnectionState,connection:"connected",context:this.context?.state,muted:String(this.muted),device_muted:String(f?.muted),track:f?.readyState}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),D(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}catch{if(this.current(h)&&this.pc===t)this.statsErrors++,this.transportTelemetry.observe({stats_errors:this.statsErrors,stats_duration_ms:performance.now()-m},{ice:t.iceConnectionState,context:this.context?.state,muted:String(this.muted)}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),D(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}finally{this.sampling.delete(t)}}applyPlaybackTarget(){let h=this.playbackReceiver;if(!h||!this.quality)return;if("jitterBufferTarget"in h)try{h.jitterBufferTarget=this.quality.target}catch{}}async applyUploadBitrate(h,t,m){if(this.applyingBitrate||this.appliedBitrate===t)return;this.applyingBitrate=!0;try{let _=h.getParameters();if(_.encodings?.length){if(_.encodings[0].maxBitrate=t,await h.setParameters(_),this.current(m)&&this.uploadSender===h)this.appliedBitrate=t}}catch{}finally{this.applyingBitrate=!1}}sendTelemetry(h){if(!this.ready||this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let t=JSON.stringify(h),m=new TextEncoder().encode(t).byteLength+58;if(!this.controlBudget.consume(m))return!1;try{return this.socket.send(t),!0}catch{return!1}}enqueueRTCEvents(h){try{let t={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,...h}};if(new TextEncoder().encode(JSON.stringify(t)).byteLength+58>1100){this.transportSender.skippedAuxiliary++;return}this.transportSender.enqueueAuxiliary(t)}catch{this.transportSender.skippedAuxiliary++}}sendStatistics(){let h=this.stream?.getAudioTracks()[0];if(performance.now()-this.lastStatsSend>=5000)this.lastStatsSend=performance.now(),this.transportSender.enqueueReport({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,client_version:Oh,relay_configured:this.relayConfigured,media_transport:"webrtc",codec:"opus",webrtc:{...Object.fromEntries(Object.entries(this.diagnostics.webrtc??{}).map(([t,m])=>[t,typeof m==="number"?Math.round(m*10)/10:m])),jitterBufferSampleAgeMs:Number.isFinite(this.lastValidStatsAt)?Math.max(0,performance.now()-this.lastValidStatsAt):void 0},connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:h?.readyState,microphone_device_muted:h?.muted,rtt_ms:this.diagnostics.rttMs===null?null:Math.round(this.diagnostics.rttMs),playback_queue_ms:Math.round(this.diagnostics.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:this.diagnostics.webrtc?.packetsLost}}),this.transportSender.enqueue(this.transportTelemetry.drain()),this.enqueueRTCEvents({timing:{runtime:this.runtime.counters}})}playWhisper(h){if(h.byteLength<17||h.byteLength>176||this.whisperEpoch===null||!this.context)return;let t=new DataView(h);if(t.getUint32(0,!0)!==827805761||t.getUint32(4,!0)!==this.whisperEpoch)return;let m=t.getFloat64(8,!0);if(!Number.isFinite(m))return;let _=performance.timeOrigin+performance.now()-m;this.whisperBase=this.whisperBase===null?_:Math.min(this.whisperBase,_);let s=Math.max(0,_-this.whisperBase);if(s>200)return;let f=new Float32Array(h.byteLength-16);for(let M=0;M<f.length;M++){let a=~t.getUint8(M+16)&255,i=((a&15)<<3)+132<<(a>>4&7);f[M]=(a&128?132-i:i-132)/32768}let u=this.context.sampleRate===8000?f:this.whisperResampler.process(f,8000,this.context.sampleRate);this.whisper?.port.postMessage({type:"whisper",frame:u,received_audio_ms:this.context.currentTime*1000-s},[u.buffer])}disconnected(h,t){if(!this.current(h)||this.recovering)return;this.recovering=!0;let m=Eh("automatic_retry");this.recoveryContext=m,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"started",code:t,...m}),this.cleanup(),D(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));let _=++this.recoveryGeneration,s=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===_;this.recoveryExpiry=setTimeout(()=>{if(!s())return;this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"timed_out",...this.recoveryContext}),++this.recoveryGeneration,this.recovering=!1,clearTimeout(this.retry),this.cancelSetup?.(),this.recoveryRequest?.abort(),this.cleanup(),D(()=>this.callbacks.onState?.("error","Network unavailable; waiting to restore audio to the existing call.",{recoverable:!0}))},30000);let f=async()=>{if(!s())return;let u={...m,attempt_id:xh("attempt")};this.recoveryContext=u,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"attempt_started",code:t,...u});try{if(!this.callbacks.refreshMediaURL)throw Error("Fresh media authorization is required");this.recoveryRequest=new AbortController;let M=await this.callbacks.refreshMediaURL(u,this.recoveryRequest.signal);if(!s())return;if(await this.connect(M),!s())return;if(this.pc?.connectionState!=="connected")throw Error("WebRTC disconnected during recovery");clearTimeout(this.recoveryExpiry),this.recovering=!1,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected",...u})}catch(M){if(!s())return;this.cleanup();let a=th(M);if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:eh(M)?"revoked":"attempt_failed",...u,status:a.status,code:a.code}),eh(M)){clearTimeout(this.recoveryExpiry),this.recovering=!1,D(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return}this.retry=setTimeout(()=>void f(),1000)}};f()}recordSessionEvent(h){try{h={...this.callbacks.sessionDiagnostics?.(),...h}}catch{}h=mh(h),this.events=[...this.events,h].slice(-100),this.enqueueRTCEvents({session_events:[h]}),D(()=>this.callbacks.onSessionEvent?.(h))}setMuted(h){if(this.muted!==h)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:h?"muted":"unmuted"});if(this.muted=h,this.gate(),h)this.send({type:"interrupt"})}sendDTMF(h){if(!/^[0-9*#]+$/.test(h))throw Error("Invalid DTMF digits");this.send({type:"dtmf",digits:h})}setOutputVolume(h){if(!Number.isFinite(h)||h<0||h>1)throw RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=h;if(this.options)this.options.outputVolume=h}startRingback(h){if(this.stopRingback(),this.context&&this.speaker)this.ringback=kh(this.context,this.speaker,Kh(h))}stopRingback(){this.ringback?.(),this.ringback=void 0}cleanup(){if(this.runtime.stopEnvironment(),this.transportSender.stop(),this.nativeCounts){for(let _ of["packetsLost","packetsDiscarded","concealedMs"])this.completedCounts[_]+=this.nativeCounts[_]??0;this.nativeCounts=void 0}++this.generation,this.ready=this.peer=!1,this.stopRingback(),clearInterval(this.timer),this.timer=void 0;let h=this.socket;if(this.socket=void 0,h){if(h.readyState===WebSocket.OPEN)try{h.send(JSON.stringify({type:"media.shutdown",reason:this.stopped?"session_cleanup":"audio_error"}))}catch{}h.onclose=h.onmessage=h.onerror=null,h.close(1000,"client_shutdown")}this.ice?.stop(),this.ice=void 0,this.playbackReceiver=void 0,this.uploadSender=void 0;let t=this.pc;if(this.pc=void 0,t)t.onconnectionstatechange=t.ontrack=null,t.close();if(this.remoteAudio)this.remoteAudio.pause(),this.remoteAudio.srcObject=null,this.remoteAudio=void 0;this.stream?.getTracks().forEach((_)=>{_.onended=null,_.stop()}),this.stream=void 0,this.destination?.stream.getTracks().forEach((_)=>_.stop()),this.destination=void 0,this.capture?.disconnect(),this.capture=void 0,this.whisper?.disconnect(),this.whisper=void 0,this.whisperEpoch=null,this.whisperBase=null,this.whisperResampler=new wh;let m=this.context;if(this.context=void 0,m)m.onstatechange=null,m.close().catch(()=>{});this.speaker=void 0,this.speakerMeter=void 0}stop(){if(this.stopped)return;if(this.recovering)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"cancelled",...this.recoveryContext});this.stopped=!0,this.recoveryRequest?.abort(),++this.recoveryGeneration,clearTimeout(this.recoveryExpiry),clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup()}}function Ot(h,t,m){let _,s=!1,f=!1,u=[];return{async start(M,a){let i=ch(a.mediaTransport),q=async(y)=>{if(s)throw Error("Audio session cancelled");let S=y();_=S,S.setMuted(f);try{for(let C of u.splice(0))try{S.recordSessionEvent?.(C)}catch{}if(s)throw Error("Audio session cancelled");if(await S.start(M,a),s)throw Error("Audio session cancelled")}catch(C){if(S.stop(),_===S)_=void 0;throw C}};if(i==="websocket"){await q(t);return}try{await q(m)}catch(y){if(i!=="auto"||s||a.iceTransportPolicy==="relay"||eh(y))throw y;D(()=>h.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"})),await q(t)}},stop(){s=!0,u.splice(0),_?.stop(),_=void 0},setMuted(M){f=M,_?.setMuted(M)},setOutputVolume(M){_?.setOutputVolume(M)},sendDTMF(M){_?.sendDTMF(M)},recordSessionEvent(M){if(_)_.recordSessionEvent?.(M);else if(!s){if(u.push(mh(M)),u.length>20)u.shift()}},startRingback(M){_?.startRingback?.(M)},stopRingback(){_?.stopRingback?.()}}}async function Qt(){return(await navigator.mediaDevices.enumerateDevices()).filter((t)=>t.kind==="audioinput").map((t,m)=>({deviceId:t.deviceId,label:t.label||`Microphone ${m+1}`}))}function Gt(h,t){let m=new tt(h,!1),_,s=!1,f,u=()=>{return s=!0,f??=(async()=>{try{await m.cancel()}finally{_?.dispose(),_=void 0}})()};return{async start(M={}){if(s)throw Error("Microphone preview is already used");s=!0;try{if(_=t?await Ah(t,!0):Lh(!0),f)throw _.dispose(),_=void 0,Error("Microphone preview was cancelled");await m.start(_.urls[0],{..._h,...M})}catch(a){throw await u(),a}},stop:u}}function pt(h){return{async preflight(t,m){await nt(h,t,m)},create(t){let m=new mt(t),_,s=!1,f=()=>{s=!0;try{m.stop()}finally{_?.dispose(),_=void 0}};return{async start(u,M){try{if(_=await j("audio_assets",(a)=>m.recordSessionEvent(a),()=>h?Ah(h):Promise.resolve(Lh())),s)throw Error("Audio session was cancelled");await m.start(u,_.urls[0],_.urls[1],M)}catch(a){throw f(),a}},stop:f,recordSessionEvent:(u)=>m.recordSessionEvent(u),setMuted:(u)=>m.setMuted(u),sendDTMF:(u)=>m.sendDTMF(u),setOutputVolume:(u)=>m.setOutputVolume(u),startRingback:(u)=>m.startRingback(u),stopRingback:()=>m.stopRingback()}}}}function Bt(h){let t=pt(h);return{preflight:(m,_)=>t.preflight(m,_),prepare:(m,_)=>Ut(h,m,_),create(m){return Ot(m,()=>t.create(m),()=>{let _=new Mt(m),s,f=!1;return{async start(u,M){try{if(s=await j("audio_assets",(a)=>_.recordSessionEvent(a),()=>h?Ah(h,!0):Promise.resolve(Lh(!0))),f)throw Error("Audio session cancelled");await _.start(u,M,s.urls[0])}catch(a){throw _.stop(),s?.dispose(),s=void 0,a}},stop(){f=!0,_.stop(),s?.dispose(),s=void 0},setMuted:(u)=>_.setMuted(u),sendDTMF:(u)=>_.sendDTMF(u),setOutputVolume:(u)=>_.setOutputVolume(u),recordSessionEvent:(u)=>_.recordSessionEvent(u),startRingback:(u)=>_.startRingback(u),stopRingback:()=>_.stopRingback()}})}}}async function Ut(h,t,m){if(!h||typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin)return;await j("audio_assets_prepare",m,async()=>{(await Ah(h,t.mediaTransport==="webrtc"||t.mediaTransport==="auto")).dispose()})}async function nt(h,t,m){await Promise.all([j("microphone_preflight",m,async()=>{(await navigator.mediaDevices.getUserMedia({audio:Sh(t)})).getTracks().forEach((s)=>s.stop())}),Ut(h,t,m)])}function Nt(h,t){return{create(m){let _={..._h,...t},s=v(_),f=new zh(_),u=[],M,a,i,q,y=!1,S=!1,C=!1,w=!1,b=!1,x=!1,A=0,g,F,J,G,U,Y,gh=!1,uh,fh,bh=_.outputVolume??1,Mh={},P=32000,E,O=[],T,Q,$,V,Z=[],n=(R)=>{if(M?.readyState===WebSocket.OPEN&&M.bufferedAmount<65536)M.send(JSON.stringify(R));else throw Error("Listener connection unavailable")},r=()=>{if(y)throw Error("Listening cancelled")},N=()=>{let R=++A;if(b=x=!1,T?.(R,Error("Coaching cancelled")),V?.(),Q=void 0,uh)clearInterval(uh);if(uh=void 0,E?.remove("coach"),U)U.onconnectionstatechange=null,U.close(),U=void 0;if(F?.disconnect(),F=void 0,J?.disconnect(),J=void 0,G?.disconnect(),G=void 0,g?.getTracks().forEach((W)=>{W.onended=null,W.stop()}),g=void 0,w&&S&&M?.readyState===WebSocket.OPEN)try{n({type:"coach.stop",generation:R})}catch{}m.onTalking?.(!1)},d=()=>N(),l=()=>{if(document.visibilityState!=="visible")N()},ah=()=>{if(y)return;if(N(),y=!0,E?.stop(),E=void 0,$?.(),window.removeEventListener("blur",d),document.removeEventListener("visibilitychange",l),fh)clearInterval(fh);if(fh=void 0,M)M.onmessage=M.onclose=M.onerror=null,M.close(1000,"listener_shutdown");if(a)a.ontrack=a.onconnectionstatechange=null,a.close();for(let R of Z)R.pause(),R.srcObject=null;if(q?.disconnect(),i)i.onstatechange=null,i.close().catch(()=>{});Y?.dispose(),Y=void 0},o=(R)=>{if(!y)ah(),m.onClose(R)},L=async(R,W)=>{if(await R.setLocalDescription(await R.createOffer()),await new Promise((X,B)=>{if(R.iceGatheringState==="complete"){X();return}let H=setTimeout(()=>{R.onicegatheringstatechange=null,B(Error("ICE gathering timed out"))},7000);R.onicegatheringstatechange=()=>{if(R.iceGatheringState==="complete"||!W())clearTimeout(H),R.onicegatheringstatechange=null,W()?X():B(Error("Coaching cancelled"))}}),!W())throw Error("Listening cancelled");return R.localDescription.sdp};return{async start(R,W){if(w=W?.coaching===!0,r(),typeof RTCPeerConnection!=="function")throw Error("WebRTC audio unavailable");try{if(i=new AudioContext({latencyHint:"interactive"}),await i.resume(),r(),_.outputDeviceId&&"setSinkId"in i)await i.setSinkId(_.outputDeviceId);if(r(),q=i.createGain(),q.gain.value=bh,q.connect(i.destination),i.onstatechange=()=>{if(i?.state==="suspended"||i?.state==="interrupted")o("listener_audio_paused")},await new Promise((H,c)=>{let p=!1,K=!1,Ch=!1,z=(Ph)=>{if(p)return;p=!0,clearTimeout(ph),$=void 0,Ph?c(Ch?Object.assign(Ph,{code:"turn_relay_required"}):Ph):H()},ph=setTimeout(()=>z(Error("WebRTC listener timed out")),18000),Jh=()=>{if(C&&K&&!y&&!S)S=!0,z(),m.onReady("webrtc")};$=()=>z(Error("Listening cancelled")),M=new WebSocket(ut(R,_.webrtcFec,_)),M.onmessage=({data:Ph})=>{if(y)return;if(typeof Ph!=="string"){z(Error("Invalid WebRTC listener frame")),o("listener_protocol_error");return}let e;try{e=JSON.parse(Ph)}catch{z(Error("Invalid listener control")),o("listener_protocol_error");return}if(e.type==="webrtc.error"){z(Object.assign(Error("Required TURN relay is unavailable"),{code:e.code}));return}if(E?.handle(e))return;if(e.type==="webrtc.config"){if(Ch=e.ice_transport_policy==="relay",a||e.listener!==!0){z(Error("Invalid WebRTC listener configuration"));return}try{Mh=Wh(e)}catch(I){z(I);return}P=e.bitrate??32000,a=new RTCPeerConnection(Mh);let hh=a;E=new Hh(n,()=>!y,(I,Bh)=>{if(O.push(mh({...Bh,timestamp:new Date().toISOString(),action:"ice_restart",outcome:I})),O.length>50)O.shift()},(I)=>{Mh=I}),E.register("main",hh);for(let I=0;I<2;I++)hh.addTransceiver("audio",{direction:"recvonly"});hh.ontrack=(I)=>{if(y||!i||!q)return;u.push(I.receiver);let Bh=hh.getTransceivers().findIndex((Fh)=>Fh.receiver===I.receiver);if(Bh<0||Bh>1){o("listener_protocol_error");return}if("jitterBufferTarget"in I.receiver)try{I.receiver.jitterBufferTarget=s.initialTargetMs}catch{}let qt=new MediaStream([I.track]),Zh=new Audio;Zh.muted=!0,Zh.srcObject=qt,Z.push(Zh),Zh.play().catch(()=>{});let gt=i.createMediaStreamSource(qt);if(t.stereo){let Fh=i.createStereoPanner();Fh.pan.value=Bh===0?-1:1,gt.connect(Fh).connect(q)}else{let Fh=i.createGain();Fh.gain.value=0.5,gt.connect(Fh).connect(q)}},hh.onconnectionstatechange=()=>{if(y)return;if(hh.connectionState==="connected")C=!0,Jh();if(hh.connectionState==="failed"||hh.connectionState==="closed"){if(z(Error("WebRTC listener connection failed")),p&&S)o("listener_network_error")}},L(hh,()=>!y).then((I)=>n({type:"webrtc.offer",sdp:I,ice_version:1})).catch((I)=>z(I));return}if(e.type==="webrtc.answer"){a?.setRemoteDescription({type:"answer",sdp:e.sdp}).catch((hh)=>z(hh));return}if(e.type==="listener.ready"){if(w&&e.coaching!==!0){z(Error("Coaching not authorized"));return}K=!0,Jh();return}if(e.type==="coach.started"){T?.(e.generation);return}if(e.type==="coach.rejected"){T?.(A,Error(e.detail||"Coaching unavailable"));return}if(e.type==="coach.stopped"&&(e.generation===void 0||e.generation===A)){N();return}if(e.type==="coach.webrtc.answer"){Q?.(e.sdp,e.generation);return}},M.onerror=()=>{if(z(Error("Listener connection failed")),S)o("listener_network_error")},M.onclose=(Ph)=>{if(z(Error(Ph.reason||"Listener disconnected")),S)o(Ph.reason||"listener_disconnected")}}),r(),w)window.addEventListener("blur",d),document.addEventListener("visibilitychange",l);let X,B=!1;fh=setInterval(()=>{if(y||!a||B)return;B=!0,a.getStats().then((H)=>{if(y)return;let c=ft(H,X);X=c.previous,f.observe({...c.metrics,receiver_jitter:c.webrtc.jitterMs/1000});for(let p of u)if("jitterBufferTarget"in p)try{p.jitterBufferTarget=f.target}catch{}m.onDiagnostics?.({session_events:O.slice(),media_transport:"webrtc",dropped_ms:[],played_ms:[],max_queue_ms:c.queueMs,max_late_ms:0,sequence_gaps:[],network_excess_ms:0,network_dropped_ms:[],webrtc:c.webrtc})}).catch(()=>{}).finally(()=>{B=!1})},1000)}catch(X){throw ah(),X}},async startTalking(){if(r(),!w||!S||!i)throw Error("Coaching session not ready");if(b)return;b=!0;let R=++A,W=()=>!y&&b&&A===R;try{let X=await navigator.mediaDevices.getUserMedia({audio:Sh(_)});if(!W()){X.getTracks().forEach((H)=>H.stop());return}if(g=X,g.getTracks().forEach((H)=>{H.onended=()=>N()}),!Y){let H=await Ah(h,!0);if(!W()){H.dispose();return}Y=H}if(!gh)await i.audioWorklet.addModule(Y.urls[0]),gh=!0;if(!W())return;if(await new Promise((H,c)=>{let p=setTimeout(()=>{T=void 0,c(Error("Coaching activation timed out"))},3000);T=(K,Ch)=>{if(Ch||K===R)clearTimeout(p),T=void 0,Ch?c(Ch):H()},n({type:"coach.start",generation:R})}),!W())return;uh=setInterval(()=>{if(W())try{n({type:"coach.keepalive",generation:R})}catch{N()}},1000),F=new AudioWorkletNode(i,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:_.inputGainDB,highpassFilter:_.highpassFilter}}),G=i.createMediaStreamDestination(),G.channelCount=1,J=i.createMediaStreamSource(X),J.connect(F).connect(G);let B=new RTCPeerConnection(Mh);if(U=B,E?.register("coach",B,W,R),B.addTransceiver(G.stream.getAudioTracks()[0],{direction:"sendonly",streams:[G.stream],sendEncodings:[{maxBitrate:P}]}),await new Promise((H,c)=>{let p=!1,K=(z)=>{if(p)return;p=!0,clearTimeout(Ch),V=void 0,Q=void 0,z?c(z):H()},Ch=setTimeout(()=>K(Error("Coaching WebRTC timed out")),18000);V=()=>K(Error("Coaching cancelled")),Q=(z,ph)=>{if(ph!==R||!W())return;B.setRemoteDescription({type:"answer",sdp:z}).catch((Jh)=>K(Jh))},B.onconnectionstatechange=()=>{if(!W())return;if(B.connectionState==="connected")K();if(B.connectionState==="failed"||B.connectionState==="closed")K(Error("Coaching media disconnected")),N()},L(B,W).then((z)=>{if(W())n({type:"coach.webrtc.offer",generation:R,sdp:z})}).catch((z)=>K(z))}),W())x=!0,m.onTalking?.(!0)}catch(X){if(W())throw N(),X}},stopTalking:N,stop:ah,setOutputVolume(R){if(!Number.isFinite(R)||R<0||R>1)throw RangeError("Listener volume must be 0–1");if(bh=R,q)q.gain.value=R}}}}}function Gh(h={},t={}){let m=["turnTransport","iceTransportPolicy","turnTLSPort","mediaTransport","webrtcFec","inputDeviceId","outputDeviceId","outputVolume","echoCancellation","noiseSuppression","autoGainControl","inputGainDB","highpassFilter","playbackTargetMs","playbackMinMs","playbackMaxMs","playbackAdaptive","stereo"],_={..._h};for(let s of[h,t])for(let f of m){let u=s[f];if(u!==void 0)_[f]=u}if(v(_),_.mediaTransport=ch(_.mediaTransport),_.webrtcFec??=!0,_.outputVolume!==void 0&&(!Number.isFinite(_.outputVolume)||_.outputVolume<0||_.outputVolume>1))throw RangeError("Listener volume must be 0–1");return _}function rt(h,t,m,_,s=!1){let f,u=!1,M=!1,a,i={...h,onReady(q){M=!0,h.onReady(q)}};return{async start(q,y){if(u)throw Error("Listening cancelled");f=t==="websocket"?m(i):_(i);try{await f.start(q,y)}catch(S){if(f.stop(),u||M||t!=="auto"||eh(S)||s)throw S;f=m(i),await f.start(q,y)}if(u)f.stop();else if(a!==void 0)f.setOutputVolume(a)},async startTalking(){if(u||!f?.startTalking)throw Error("Coaching session not ready");await f.startTalking()},stopTalking(){f?.stopTalking?.()},stop(){u=!0,f?.stop()},setOutputVolume(q){if(!Number.isFinite(q)||q<0||q>1)throw RangeError("Listener volume must be 0–1");a=q,f?.setOutputVolume(q)}}}function at(h,t={}){let m=Gh({},t),_=ch(m.mediaTransport),s=h0(h,m),f=Nt(h,m);return{create(u){return rt(u,_,(M)=>s.create(M),(M)=>f.create(M),m.iceTransportPolicy==="relay")}}}function vt(h){if(h.byteLength<26||h.byteLength>984||h.byteLength%2)throw Error("Invalid listener audio frame");let t=new DataView(h),m=t.getUint32(4,!0);if(t.getUint32(0,!0)!==827085889||m>1)throw Error("Invalid listener audio protocol");let _=Number(t.getBigUint64(8,!0)),s=Number(t.getBigUint64(16,!0));if(!Number.isSafeInteger(_)||!Number.isSafeInteger(s))throw Error("Invalid listener audio timing");let f=new Float32Array((h.byteLength-24)/2);for(let u=0;u<f.length;u++)f[u]=t.getInt16(24+u*2,!0)/32768;return{direction:m,sequence:_,timestampMS:s,frame:f}}function ot(h,t,m,_){if(h.length<1||h.length>480||!Number.isFinite(_)||_<0||!Number.isInteger(t)||t<1||t>4294967295)throw Error("Invalid coaching capture");let s=new ArrayBuffer(24+h.length*2),f=new DataView(s);f.setUint32(0,826496833,!0),f.setUint32(4,t,!0),f.setUint32(8,m>>>0,!0),f.setFloat64(16,_,!0);for(let u=0;u<h.length;u++){let M=Math.max(-1,Math.min(1,h[u]));f.setInt16(24+u*2,M<0?M*32768:M*32767,!0)}return s}function h0(h,t={}){return{create(m){let _,s,f,u,M=!1,a=[],i=!1,q=0,y=!1,S=!1,C,w,b,x,A,g,F,J=!1,G,U,Y=0,gh=!1,uh=[0,0],fh=[0,0],bh=[0,0],Mh=[0,0],P=[void 0,void 0],E=[new wh,new wh],O=t.outputVolume??1,T,Q=()=>{let N=++q;if(y=!1,S=!1,F?.(N,Error("Coaching cancelled")),g)clearInterval(g);if(g=void 0,A?.port1.close(),A?.port2.close(),A=void 0,w?.disconnect(),w=void 0,b?.disconnect(),b=void 0,x?.disconnect(),x=void 0,C?.getTracks().forEach((d)=>{d.onended=null,d.stop()}),C=void 0,i&&gh&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.stop",generation:N}));m.onTalking?.(!1)},$=()=>Q(),V=()=>{if(document.visibilityState!=="visible")Q()},Z=()=>{if(M)return;if(Q(),M=!0,T?.(),window.removeEventListener("blur",$),document.removeEventListener("visibilitychange",V),u)u.onmessage=u.onclose=u.onerror=null,u.close(1000,"listener_shutdown");if(s?.disconnect(),f?.disconnect(),_)_.onstatechange=null,_.close().catch(()=>{});for(let N of a.splice(0))N.dispose()},n=(N)=>{if(!M)Z(),m.onClose(N)},r=()=>{if(M)throw Error("Listening cancelled")};return{async start(N,d){i=d?.coaching===!0,r();try{if(_=new AudioContext({latencyHint:"interactive"}),_.state==="suspended")await _.resume();if(r(),t.outputDeviceId&&"setSinkId"in _)await _.setSinkId(t.outputDeviceId);r();let l=await Yh(h,"listener");if(M)l.dispose(),r();if(a.push(l),await _.audioWorklet.addModule(l.urls[0]),r(),s=new AudioWorkletNode(_,"telephony-listener",{numberOfInputs:0,outputChannelCount:[t.stereo?2:1],processorOptions:{stereo:t.stereo,...v(t)}}),f=_.createGain(),f.gain.value=O,s.connect(f).connect(_.destination),s.onprocessorerror=()=>n("listener_audio_error"),s.port.onmessage=({data:ah})=>{if(ah?.type==="listener.diagnostics"&&!M)try{m.onDiagnostics?.({...ah,media_transport:"websocket",context_state:_?.state,context_rate:_?.sampleRate,context_time_ms:(_?.currentTime??0)*1000,received_ms:[...fh],scheduled_ms:[...bh],sequence_gaps:[...Mh],network_excess_ms:Y,network_dropped_ms:[...uh]})}catch{}},_.onstatechange=()=>{if(_?.state==="suspended"||_?.state==="interrupted")n("listener_audio_paused")},await new Promise((ah,o)=>{let L=!1,R=setTimeout(()=>{W(Error("Listener connection timed out")),n("listener_network_error")},1e4),W=(X)=>{if(L)return;L=!0,clearTimeout(R),T=void 0,X?o(X):ah()};T=()=>W(Error("Listening cancelled")),u=new WebSocket(N),u.binaryType="arraybuffer",u.onmessage=({data:X})=>{if(M||!_||!s)return;try{if(typeof X==="string"){let K=JSON.parse(X);if(K.type==="listener.ready"){if(i&&K.coaching!==!0)throw Error("Coaching not authorized");gh=!0,W(),m.onReady("websocket")}else if(K.type==="coach.started")F?.(K.generation);else if(K.type==="coach.rejected")F?.(q,Error(K.detail||"Coaching unavailable"));else if(K.type==="coach.stopped"&&(K.generation===void 0||K.generation===q))Q();return}if(!gh||!(X instanceof ArrayBuffer))return;let B=vt(X),H=B.direction;if(fh[H]+=B.frame.length*1000/24000,P[H]!==void 0&&B.sequence<P[H])return;if(P[H]!==void 0&&B.sequence>P[H])Mh[H]+=B.sequence-P[H];P[H]=B.sequence+1;let c=performance.now()-B.timestampMS;if(U=Math.min(U??c,c),Y=Math.max(0,c-U),Y>200){uh[H]+=B.frame.length*1000/24000;return}G??=_.currentTime*1000+(t.playbackTargetMs??60)-B.timestampMS;let p=E[H].process(B.frame,24000,_.sampleRate);bh[H]+=B.frame.length*1000/24000,s.port.postMessage({direction:H,frame:p,playAtMS:B.timestampMS+G},[p.buffer])}catch{W(Error("Invalid listener media")),n("listener_protocol_error")}},u.onerror=()=>{W(Error("Listener connection failed")),n("listener_network_error")},u.onclose=(X)=>{W(Error(X.reason||"Listener disconnected")),n(X.reason||"listener_disconnected")}}),r(),i)window.addEventListener("blur",$),document.addEventListener("visibilitychange",V)}catch(l){throw Z(),l}},async startTalking(){if(r(),!i||!gh||!_||!u||u.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(y)return;y=!0;let N=++q,d=()=>!M&&y&&N===q;try{let l=await navigator.mediaDevices.getUserMedia({audio:Sh({..._h,...t})});if(!d()){l.getTracks().forEach((L)=>L.stop());return}if(C=l,C.getAudioTracks().forEach((L)=>{L.onended=()=>Q()}),!J){let L=await Ah(h,!0);if(!d()){L.dispose();return}a.push(L),await _.audioWorklet.addModule(L.urls[0]),J=!0}if(!d())return;if(await new Promise((L,R)=>{let W=setTimeout(()=>{F=void 0,R(Error("Coaching activation timed out"))},3000);F=(X,B)=>{if(B||X===N)clearTimeout(W),F=void 0,B?R(B):L()},u.send(JSON.stringify({type:"coach.start",generation:N}))}),!d())return;w=new AudioWorkletNode(_,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:t.inputGainDB??0,highpassFilter:t.highpassFilter!==!1}}),b=_.createMediaStreamSource(l),x=_.createGain(),x.gain.value=0,b.connect(w).connect(x).connect(_.destination),A=new MessageChannel;let ah=0,o=new wh;A.port1.onmessage=({data:L})=>{if(!d()||!S||L?.type!=="capture"||!(L.frame instanceof Float32Array)||!_||!u)return;if(!Number.isFinite(L.timestamp_ms)||_.currentTime*1000-L.timestamp_ms>150||u.bufferedAmount>2880||u.readyState!==WebSocket.OPEN)return;let R=o.process(L.frame,L.sample_rate,24000);if(R.length>0&&R.length<=480)u.send(ot(R,N,ah++,L.timestamp_ms))},A.port1.start(),w.port.postMessage({type:"transport",port:A.port2},[A.port2]),S=!0,m.onTalking?.(!0),g=setInterval(()=>{if(d()&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.keepalive",generation:N}))},1000)}catch(l){if(d())throw Q(),l}},stopTalking:Q,stop:Z,setOutputVolume(N){if(!Number.isFinite(N)||N<0||N>1)throw RangeError("Listener volume must be 0–1");if(O=N,f)f.gain.value=N}}}}}class it{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;requestAbort;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;audioOptions;constructor(h,t={}){this.client=h;this.options=t;this.audioOptions=Gh(t.audio,t),this.runtime=t.runtime??at(h.app,this.audioOptions)}setAudioOptions(h){let t=Gh({},h);this.audioOptions=t,this.runtime=this.options.runtime??at(this.client.app,t)}getAudioOptions=()=>Object.freeze({...this.audioOptions});getSnapshot=()=>this.snapshot;subscribe=(h)=>{return this.observers.add(h),()=>{this.observers.delete(h)}};update(h){this.snapshot=Object.freeze({...this.snapshot,...h});for(let t of this.observers)try{t(this.snapshot)}catch{}}async listen(h){return this.begin(h,!1)}async coach(h){return this.begin(h,!0)}async begin(h,t){if(this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=t,this.retryUntil=0,this.retryDelay=500;let m=++this.generation;return this.update({state:"connecting",callId:h,detail:void 0,coaching:t,talking:!1,mediaTransport:void 0}),this.connect(h,m)}async connect(h,t){this.requestAbort?.abort(),this.requestAbort=new AbortController;let m,_=++this.attempt,s=()=>t===this.generation&&_===this.attempt&&!this.disposed;try{if(m=this.coaching?await this.client.coachSession(h):await this.client.listenSession(h,this.requestAbort?.signal),!s()){await this.client.stopListening(m).catch(()=>{});return}this.session=m;let f=this.runtime.create({onReady:(u)=>{if(s())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0,mediaTransport:u})},onClose:(u)=>{if(s())this.disconnected(u,h,t)},onTalking:(u,M)=>{if(s())this.update({talking:u,detail:M})},onDiagnostics:(u)=>{if(s())try{this.options.onDiagnostics?.(u)}catch{}}});if(this.audio=f,this.lease=new Nh(m.lease_seconds,()=>this.client.renewListening(m),(u)=>{if(s())this.disconnected(u==="revoked"?"access_revoked":"media_disconnected",h,t,u==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,m.lease_started_ms),await f.start(this.client.listenerMediaURL(m,this.audioOptions.mediaTransport),{coaching:m.coaching===!0}),!s())f.stop()}catch(f){if(!s()||m&&this.session!==m)return;let u=f?.status,M;try{M=JSON.parse(f.body??"{}").code}catch{}let a=M==="call_ended"?"call_ended":u===401||u===403||u===404?"access_revoked":"listener_disconnected";throw this.disconnected(a,h,t,String(f)),f}}cleanup(){this.requestAbort?.abort(),this.requestAbort=void 0,this.lease?.stop(),this.lease=void 0;let h=this.audio;this.audio=void 0,h?.stop();let t=this.session;return this.session=void 0,t?this.client.stopListening(t).catch(()=>{}):Promise.resolve()}disconnected(h,t,m,_=h){if(m!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let s=h==="call_ended",f=h==="access_revoked",u=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(h);if(!this.coaching&&!s&&!f&&u&&this.options.reconnect!==!1){this.retryUntil||=Date.now()+30000;{this.update({state:"reconnecting",detail:_,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,m===this.generation&&!this.disposed)this.connect(t,m).catch(()=>{})},Date.now()<this.retryUntil?this.retryDelay:15000),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:s?"call_ended":f?"access_revoked":"disconnected",detail:_,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(h){this.audio?.setOutputVolume(h)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let h=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1,mediaTransport:void 0}),await h}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}var t0={set:(h,t)=>setTimeout(h,t),clear:(h)=>clearTimeout(h)};class yt{run;permanent;ended;event;clock;stopped=!1;active;timer;attempt=0;online=()=>this.wake();constructor(h,t,m,_,s=t0){this.run=h;this.permanent=t;this.ended=m;this.event=_;this.clock=s;globalThis.addEventListener?.("online",this.online),this.schedule(1000)}emit(h){try{this.event?.(h)}catch{}}schedule(h){if(!this.stopped)this.clock.clear(this.timer),this.timer=this.clock.set(()=>void this.tick(),h)}wake(){if(!this.stopped&&!this.active)this.schedule(0)}async tick(){if(this.stopped||this.active)return;let h=new AbortController;this.active=h,this.attempt++,this.emit("attempt_started");let t;try{if(await Promise.race([this.run(h.signal),new Promise((m,_)=>{t=this.clock.set(()=>{h.abort(),_(Error("Audio recovery request timed out"))},30000)})]),this.stopped)return;this.emit("connected"),this.stop()}catch(m){if(this.stopped)return;if(this.permanent(m)){this.emit("revoked"),this.stop(),this.ended(m);return}this.emit("waiting_for_network"),this.schedule(Math.min(15000,1000*2**Math.min(4,this.attempt)))}finally{if(this.clock.clear(t),this.active===h)this.active=void 0}}stop(){if(this.stopped)return;this.stopped=!0,this.clock.clear(this.timer),this.active?.abort(),globalThis.removeEventListener?.("online",this.online)}}function Wt(h){if(!h)return"placing";if(dh(h))return"ended";if(h==="ringing")return"ringing";if(h==="answered"||h==="in-progress")return"connected";return"placing"}var sh=(h)=>h instanceof Error?h.message:String(h);class St{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;hasStatusPush=!1;automaticRecovery;hasLiveAudio=!1;uncertainAttachment;scheduleStatusPoll;outbound=!1;ringing=!1;runtime;audioOptions;interval;constructor(h,t={}){this.client=h;this.options=t;if(this.runtime=t.audioRuntime??Bt(h.app),this.audioOptions={..._h,...t.audio,...t.mediaTransport?{mediaTransport:t.mediaTransport}:{}},v(this.audioOptions),this.interval=t.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms")}createCallListener(h={}){return this.assertOpen(),this.client.createCallListener({...this.audioOptions,...h.audio,...h})}getSnapshot=()=>this.snapshot;subscribe=(h)=>{return this.assertOpen(),this.listeners.add(h),()=>{this.listeners.delete(h)}};update(h){let t={...this.snapshot,...h};if(t.callId&&Wt(t.carrierStatus)==="connected")t.phase=t.audioState==="live"?"connected":"placing";this.snapshot=Object.freeze(t);for(let m of this.listeners)try{m(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(h){if(this.disposed||h!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.automaticRecovery?.stop(),this.automaticRecovery=void 0,this.uncertainAttachment=void 0,this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(h){return!this.disposed&&h===this.generation}finish(h){if(this.current(h))this.update({busy:!1})}cancellable(h,t){let m=this.cancellation.signal;return new Promise((_,s)=>{let f=()=>{m.removeEventListener("abort",f),s(Error("Softphone operation cancelled"))};if(m.addEventListener("abort",f,{once:!0}),h.then(_,s).finally(()=>m.removeEventListener("abort",f)),!this.current(t)||m.aborted)f()})}begin(h){if(this.assertOpen(),this.snapshot.busy||this.hangingUp||h&&this.session)throw Error("Softphone already has an active operation or call");let t=this.invalidate();return this.update({busy:!0,detail:void 0}),t}async prepareAudio(h,t){let m=[],_=(s)=>{if(this.current(h)&&!this.cancellation.signal.aborted)m.push(mh(s))};try{return await this.cancellable(j(t?"audio_preflight":"audio_prepare",_,()=>t?this.runtime.preflight(this.audioOptions,_):this.runtime.prepare?.(this.audioOptions,_)??Promise.resolve()),h),this.assertCurrent(h),m}catch(s){if(this.current(h))for(let f of m)this.reportSessionEvent(f);throw s}}async dial(h){let t=this.begin(!0),m,_=!1;try{this.assertCurrent(t);let s={to:h.to.trim(),from:h.from?.trim(),timeout_sec:h.timeout_sec,recording:h.recording},f=JSON.stringify(s);if(!this.intent||this.intent.value!==f||h.idempotency_key&&h.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:f,request:{...s,idempotency_key:h.idempotency_key||crypto.randomUUID()}};let u=await this.prepareAudio(t,!0);return this.assertCurrent(t),m=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(t),_=!0,this.outbound=!0,await this.attachAudio(m,t,u),m.call_id}catch(s){if(m&&(this.current(t)||!_||this.disposed))await this.recoverStartup(m,t,()=>this.client.hangup(m.call_id));if(this.current(t))this.update({detail:sh(s)});throw s}finally{this.finish(t)}}async answer(h,t={}){let m=this.begin(!0),_,s=!1;try{this.assertCurrent(m);let f=await this.prepareAudio(m,!0);_=await this.client.answer(h,t),this.assertCurrent(m),s=!0,this.outbound=!1,await this.attachAudio(_,m,f)}catch(f){if(_&&(this.current(m)||!s||this.disposed))await this.recoverStartup(_,m,()=>this.client.release(_));if(this.current(m))this.update({detail:sh(f)});throw f}finally{this.finish(m)}}async recoverStartup(h,t,m){try{if(await m(),this.current(t))this.clearCall()}catch{if(this.current(t))this.session=h,this.update({callId:h.call_id,audioState:"error"}),this.startPolling()}}async join(h){await this.answer(h,{rejoin:!0})}async attach(h,t="attach"){await this.acquireSession(h,!1,t)}async takeover(h){await this.acquireSession(h,!0)}async acquireSession(h,t,m="takeover"){let _=this.begin(!0);try{let s=await this.prepareAudio(_,!0);this.assertCurrent(_);let f=Eh(t?"takeover":m),u=await(t?this.client.takeover(h,f):this.client.attach(h,f));this.assertCurrent(_),await this.attachAudio(u,_,s),await this.reconcileAttachedCall(u,_)}catch(s){if(this.current(_))this.update({detail:sh(s)});throw s}finally{this.finish(_)}}async reconnect(h,t="manual_reconnect"){if(!this.session)throw Error("No call to reconnect");let m={...this.audioOptions,...h};v(m);let _=this.begin(!1);this.audioOptions=m;let s=this.session.call_id,f=Eh(t),u=this.session.session_generation,M=(a,i={})=>this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:a,...f,previous_session_id:u,...i});M("started");try{let a=await this.prepareAudio(_,!1),i;try{i=await this.acquireRecoverySession(this.session,{...f,previous_session_id:u},this.cancellation.signal)}catch(q){let y=th(q),S;try{S=JSON.parse(q.body??"{}").current_generation}catch{}if(t==="automatic_retry"||y.code!=="media_session_changed"||!S||!/^session-[a-f0-9-]{1,64}$/.test(S))throw q;this.assertCurrent(_),i=await this.client.attach(s,{...Eh(t),previous_session_id:S},{signal:this.cancellation.signal},S)}this.assertCurrent(_),await this.attachAudio(i,_,a),await this.reconcileAttachedCall(i,_),M("connected",{session_id:i.session_generation})}catch(a){if(M(this.current(_)?"failed":"cancelled"),this.current(_))this.update({detail:sh(a)});throw a}finally{this.finish(_)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let h=this.session;if(!h){this.cancellation.abort();return}let t=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let m=(async()=>{try{if(await this.client.hangup(h.call_id),this.current(t))this.clearCall()}catch(_){if(this.current(t))this.update({audioState:"error",detail:sh(_)}),this.startPolling();throw _}finally{this.finish(t)}})();this.hangingUp=m;try{await m}finally{if(this.hangingUp===m)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(h){this.assertOpen();let t=this.session;if(!t)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let m=this.generation;this.update({busy:!0,detail:void 0});let _;try{_=this.client[h](t.call_id)}catch(s){if(this.current(m)&&this.session===t)this.update({busy:!1,detail:sh(s)});throw s}this.controlPending=_;try{let s=await _;if(this.current(m)&&this.session===t)this.update({holdState:s.hold_state,recordingState:s.recording_state,controlError:s.control_error,capabilities:s.capabilities});return s}catch(s){if(this.current(m)&&this.session===t)this.update({detail:sh(s)});throw s}finally{if(this.controlPending===_)this.controlPending=void 0;if(this.current(m)&&this.session===t)this.update({busy:!1})}}configureAudio(h){this.assertOpen();let t={...this.audioOptions,...h};v(t),this.audioOptions=t}setMuted(h){this.assertOpen(),this.audio?.setMuted(h),this.update({muted:h})}setOutputVolume(h){if(this.assertOpen(),!Number.isFinite(h)||h<0||h>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=h,this.audio?.setOutputVolume(h)}sendDTMF(h){if(this.assertOpen(),!/^[0-9*#]+$/.test(h))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(h)}observeCall(h){if(this.disposed||h.id!==this.session?.call_id)return;if(h.direction)this.outbound=h.direction==="outbound";if(dh(h.status)){this.invalidate(),this.clearCall(h.status),this.update({busy:!1,phase:"ended",termination:h.termination,answeredBy:h.answered_by??this.snapshot.answeredBy,endedAt:h.ended_at});return}this.update({carrierStatus:h.status,phase:Wt(h.status),answeredBy:h.answered_by??this.snapshot.answeredBy,holdState:h.hold_state??this.snapshot.holdState,recordingState:h.recording_state??this.snapshot.recordingState,controlError:h.control_error??this.snapshot.controlError,capabilities:h.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let h=this.options.ringback;return typeof h==="object"&&h?h.country:void 0}syncRingback(){let h=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(h&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!h&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(h,t,m=[]){this.hasStatusPush=!1,this.assertCurrent(t),this.stopAudio(),this.session=h,this.update({callId:h.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let _,s=()=>!this.disposed&&_!==void 0&&this.audio===_,f=(a)=>{if(s())try{a()}catch{}},u,M;try{this.assertCurrent(t),_=this.runtime.create({refreshMediaURL:(a,i)=>{let q=a??Eh("automatic_retry");if(u)return this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"coalesced",...q,shared_attempt_id:M?.attempt_id}),u;if(!s()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let y=this.generation,S=(async()=>{let w=h.session_generation,b=await this.acquireRecoverySession(h,{...q,previous_session_id:w},i);if(this.assertCurrent(y),!s())throw Error("Audio connection no longer active");return h=b,this.session=b,this.startLease(b),this.reportSessionEvent({timestamp:new Date().toISOString(),action:"media_session",outcome:"replaced",...q,previous_session_id:w,session_id:b.session_generation}),this.client.mediaURL(b,this.audioOptions.mediaTransport)})();u=S,M=q;let C=()=>{if(u===S)u=void 0,M=void 0};return S.then(C,C),S},sessionDiagnostics:()=>({session_id:h.session_generation}),onSessionEvent:(a)=>f(()=>this.options.onSessionEvent?.(mh({...a,session_id:a.session_id??h.session_generation}))),onState:(a,i,q)=>{if(!s())return;if(a==="ended"&&i==="call.ended"){this.observeCall({id:h.call_id,status:"completed"});return}if(a==="live")this.hasLiveAudio=!0;if(this.update({audioState:a,detail:i}),this.scheduleStatusPoll?.(),s()&&(a==="error"||a==="ended")){if(this.stopAudio(),a==="error"){if(this.reconcileFailedAudio(h,this.generation),q?.recoverable)this.scheduleAudioRecovery()}}},onMediaReadiness:(a)=>f(()=>this.options.onMediaReadiness?.(a)),onLevels:(a,i)=>f(()=>this.options.onLevels?.(a,i)),onAudioHealth:(a)=>f(()=>this.options.onAudioHealth?.(a)),onDiagnostics:(a)=>f(()=>this.options.onDiagnostics?.(a)),onNotice:(a)=>f(()=>this.options.onNotice?.(a)),onCallStatus:(a)=>f(()=>{this.hasStatusPush=!0,this.observeCall({id:a.call_id,status:a.status,direction:a.direction,answered_at:a.answered_at,ended_at:a.ended_at,answered_by:a.answered_by,termination:a.termination,hold_state:a.hold_state,recording_state:a.recording_state,control_error:a.control_error}),this.scheduleStatusPoll?.()})}),this.audio=_,this.assertCurrent(t);for(let a of m)this.reportSessionEvent(a);if(this.assertCurrent(t),this.startPolling(),this.startLease(h),_.setMuted(this.snapshot.muted),await this.cancellable(_.start(this.client.mediaURL(h,this.audioOptions.mediaTransport),this.audioOptions),t),this.assertCurrent(t),!s())throw Error(this.snapshot.detail||"Audio connection ended during setup");_.setMuted(this.snapshot.muted),this.syncRingback()}catch(a){if(s())this.stopAudio();if(this.current(t))this.update({audioState:"error",detail:sh(a)});throw a}}async acquireRecoverySession(h,t,m){let _=this.uncertainAttachment??{diagnostics:t,expected:h.attachment_guard_version===1?h.session_generation??"":void 0};if(m?.aborted)throw new DOMException("Cancelled","AbortError");if(this.uncertainAttachment=_,_.diagnostics.attempt_id!==t.attempt_id)this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"idempotent_retry",...t,shared_attempt_id:_.diagnostics.attempt_id});try{let s=await this.client.attach(h.call_id,_.diagnostics,{signal:m},_.expected);if(m?.aborted)throw new DOMException("Cancelled","AbortError");return this.uncertainAttachment=void 0,s}catch(s){let f=th(s).status;if(f!==void 0&&f>=400&&f<500)this.uncertainAttachment=void 0;throw s}}scheduleAudioRecovery(){if(this.disposed||!this.session||!this.hasLiveAudio||this.automaticRecovery)return;let h=this.session.call_id,t=this.generation;this.update({audioState:"reconnecting",detail:"Network unavailable; waiting to restore audio."});let m=(f)=>{let u=th(f);return u.denied||u.code==="media_session_changed"||u.code==="call_ended"||u.code==="call_not_attachable"||["NotAllowedError","NotFoundError"].includes(f?.name)},_=this.uncertainAttachment?.diagnostics??Eh("automatic_retry"),s=_;this.automaticRecovery=new yt(async(f)=>{this.assertCurrent(t);let u=await Uh((q)=>this.client.getCall(h,q),f);if(!u)throw Object.assign(Error("Call unavailable"),{status:404});if(dh(u.status)){this.observeCall(u);return}this.assertCurrent(t);let M=await this.prepareAudio(t,!1);if(f.aborted)throw new DOMException("Cancelled","AbortError");let a=await this.acquireRecoverySession(this.session,s,f);if(this.assertCurrent(t),f.aborted)throw new DOMException("Cancelled","AbortError");let i=()=>{if(this.current(t)&&this.session===a)this.stopAudio()};f.addEventListener("abort",i,{once:!0});try{if(await this.attachAudio(a,t,M),f.aborted)throw new DOMException("Cancelled","AbortError")}finally{f.removeEventListener("abort",i)}this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:"connected",...s}),this.automaticRecovery?.stop(),this.automaticRecovery=void 0},m,(f)=>{if(this.current(t))this.automaticRecovery=void 0,this.update({audioState:"error",detail:sh(f)})},(f)=>{if(f==="attempt_started")s={..._,attempt_id:xh("attempt")};this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:f,...s})})}async reconcileAttachedCall(h,t){try{let m=await this.client.getCall(h.call_id);if(this.current(t)&&this.session===h&&m)this.observeCall(m)}catch{}}async reconcileFailedAudio(h,t){try{let m=await this.client.getCall(h.call_id);if(!this.current(t)||this.session!==h||!m)return;if(m.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(m)}catch{}}startLease(h){if(this.lease?.stop(),!h.lease_seconds)return;this.lease=new Nh(h.lease_seconds,()=>this.client.renew(h),(t,m)=>{if(this.disposed||this.session!==h)return;if(this.stopAudio(),this.update({audioState:"error",detail:t==="expired"?"Audio session expired. Reconnecting audio.":`Audio authorization ended: ${sh(m??t)}`}),t==="expired")this.scheduleAudioRecovery();else this.automaticRecovery?.stop(),this.automaticRecovery=void 0},(t)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(t);else try{this.options.onSessionEvent?.(t)}catch{}},h.lease_started_ms)}reportSessionEvent(h){if(h=mh(h),this.audio?.recordSessionEvent)try{this.audio.recordSessionEvent(h)}catch{}else try{this.options.onSessionEvent?.(h)}catch{}}stopAudio(){this.lease?.stop(),this.lease=void 0;let h=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{h?.stopRingback?.()}catch{}}try{h?.stop()}catch{}if(h)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){this.scheduleStatusPoll=void 0,clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let h=new AbortController;this.polling=h;let t=!1,m=performance.now(),_=()=>this.hasStatusPush&&this.snapshot.audioState==="live"?Math.max(1,Math.max(this.interval,30000)-(performance.now()-m)):this.interval,s=async()=>{if(t)return;let f=this.session?.call_id;if(!f||h.signal.aborted)return;t=!0;try{let u=await this.client.getCall(f,h.signal);if(m=performance.now(),!h.signal.aborted&&u)this.observeCall(u)}catch(u){if(!h.signal.aborted)this.hasStatusPush=!1,this.update({detail:`Call status unavailable: ${sh(u)}`})}finally{t=!1,this.scheduleStatusPoll?.()}};this.scheduleStatusPoll=()=>{if(t||h.signal.aborted||!this.session||this.disposed)return;clearTimeout(this.timer),this.timer=setTimeout(s,_())},this.scheduleStatusPoll()}clearCall(h){this.hasStatusPush=!1,this.stopAudio(),this.stopPolling(),this.session=void 0,this.hasLiveAudio=!1,this.outbound=!1,this.update({callId:void 0,carrierStatus:h,audioState:"idle",muted:!1,detail:void 0,phase:h?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function m0(h,t="en"){if(h?.reason==="ai_inactivity")return t.toLowerCase().startsWith("fr")?"Fin de l’appel : aucune réponse":"Call ended: no response";if(h?.reason==="ai_policy_failure")return t.toLowerCase().startsWith("fr")?"Échec du rappel d’inactivité IA":"AI inactivity reminder failed";if(h?.reason==="time_limit"||h?.reason==="ai_max_duration")return t.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return h?.reason?.replaceAll("_"," ")??""}class Ht extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function _0(h){return h.direction==="inbound"&&h.status==="pending"&&h.answerable!==!1&&!h.routing_waiting&&(h.peer_kind==="human"||Boolean(h.ring_offers?.some((t)=>t.kind==="browser")))}var dh=(h)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(h);function k(h){if(!h||/[\s/\\?#]/.test(h))throw Error("Invalid call ID");return encodeURIComponent(h)}class lh{app;options;transport;listMicrophones=Qt;createMicrophonePreview=(h)=>Gt(h,this.app);constructor(h,t={}){this.app=h;this.options=t;if(this.transport=new nh(h,t.authProvider?"proxy":t.direct?"direct":t.runtime,globalThis.fetch,t.direct),h.name!=="telephony"||!h.projectId||!h.installId)throw Error("Telephony requires an explicit project and installation")}get(h,t){return this.transport.request("GET",h,void 0,t)}post(h,t,m){return this.transport.request("POST",h,t,m)}subscribe(h,t,m){return this.transport.subscribe(h,t,m)}path(h){if(!this.options.authProvider)return h;return`/user${h}${h.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(h){let t=await this.get(this.path("/calls"),{signal:h});if(!Array.isArray(t?.calls)||t.calls.some((m)=>!m||typeof m.id!=="string"||typeof m.status!=="string"))throw Error("Invalid Telephony calls response");return t.calls}async getCall(h,t){let m=await this.get(this.path(`/calls?call_id=${k(h)}`),{signal:t});if(!Array.isArray(m?.calls))throw Error("Invalid Telephony call response");return m.calls.find((_)=>_.id===h)}incomingCalls(h){return h.filter(_0)}watchCalls(h,t={}){let m=t.intervalMs??2000;if(!Number.isFinite(m)||m<100)throw Error("Call watch interval must be at least 100 ms");let _=new AbortController,s,f,u=!1,M=!1,a=-1/0,i,q,y=performance.now(),S=()=>{let A=performance.now()-a;return A<15000?Math.max(1,Math.min(Math.max(m,30000)-(performance.now()-y),15000-A)):m},C=new Set,w=()=>{clearTimeout(s),_.abort(),f?.close(),t.signal?.removeEventListener("abort",w)},b=(A)=>{try{t.onError?.(A)}catch{w()}};if(t.signal?.addEventListener("abort",w,{once:!0}),t.signal?.aborted)w();let x=async(A)=>{if(_.signal.aborted)return;if(u){M=!0;return}clearTimeout(s),u=!0;let g=performance.now(),F=i;try{let J=await this.listCalls(_.signal);if(y=performance.now(),!_.signal.aborted){h(J),q=F;for(let G of J){if(G.answerable!==!0||G.status!=="pending")continue;for(let U of G.ring_offers??[]){if(U.kind!=="browser"||!U.id||C.has(U.id))continue;C.add(U.id),this.acknowledgeOffer(G.id,U.id).catch((Y)=>{if(![404,405].includes(Y?.status??0))C.delete(U.id)})}}try{t.onTiming?.({trigger:A,fetchMs:performance.now()-g})}catch{}}}catch(J){if(a=-1/0,q=void 0,!_.signal.aborted){let G=J?.status;try{t.onFailure?.({trigger:A,fetchMs:performance.now()-g,status:typeof G==="number"?G:void 0,error:J})}catch{}b(J)}}finally{if(u=!1,!_.signal.aborted)if(M)M=!1,x("push");else s=setTimeout(()=>void x("poll"),S())}};if(x("poll"),!_.signal.aborted&&t.push!==!1&&typeof this.app.subscribe==="function")try{f=this.subscribe(this.path("/calls/events"),(A)=>{if(A.type==="heartbeat"||A.type==="calls.changed"){if(a=performance.now(),A.type==="calls.changed"){let g=typeof A.state==="string"&&/^[0-9a-f]{64}$/.test(A.state)?A.state:void 0;if(i=g,!g||g!==q||u)x("push")}else if(!u)clearTimeout(s),s=setTimeout(()=>void x("poll"),S())}else if(A.type==="access.revoked")a=-1/0,f?.close(),x("push")},{transport:"fetch",signal:_.signal,reconnectDelayMs:250,onError:(A)=>{if(a=-1/0,!u&&!_.signal.aborted)clearTimeout(s),s=setTimeout(()=>void x("poll"),m);let g=A?.status;if(g===404||g===405||g===501)f?.close()}})}catch{}return{close:w}}async place(h){if(!/^\+[1-9]\d{7,14}$/.test(h.to)||!h.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let t=ih();return this.session(await this.post(this.path("/softphone/place"),h),void 0,"media",t)}async answer(h,t={}){let m=ih();try{return this.session(await this.post(this.path(`/softphone/answer/${k(h)}`),t),h,"media",m)}catch(_){let s=_;if(s.status===409&&typeof s.body==="string"){let f;try{f=JSON.parse(s.body)}catch{}if(f?.code==="offer_expired")throw new Ht}throw _}}async acknowledgeOffer(h,t){await this.post(this.path(`/softphone/offer/ack/${k(h)}`),{offer_id:t})}async declineOffer(h,t){await this.post(this.path(`/softphone/offer/decline/${k(h)}`),{offer_id:t})}async attach(h,t,m,_){let s=ih();return this.session(await Uh((f)=>this.post(this.path(`/softphone/attach/${k(h)}`),{...t?{media_diagnostics:t}:{},..._!==void 0&&t?.attempt_id?{attachment_guard:{id:t.attempt_id,expected_generation:_}}:{}},{...m,signal:f}),m?.signal??void 0),h,"media",s)}async takeover(h,t){let m=ih();return this.session(await this.post(this.path(`/softphone/takeover/${k(h)}`),t?{media_diagnostics:t}:{}),h,"media",m)}createCallListener(h={}){return new it(this,{...h,...Gh({...this.options.audio,...h.audio},h)})}async listenSession(h,t){let m=ih();return this.session(await Uh((_)=>this.post(this.path(`/softphone/listen/${k(h)}`),{},{signal:_}),t,5000,(_)=>{try{this.stopListening(this.session(_,h,"listen-media",m)).catch(()=>{})}catch{}}),h,"listen-media",m)}async coachSession(h){let t=ih(),m=this.session(await this.post(this.path(`/softphone/coach/${k(h)}`),{}),h,"listen-media",t);if(m.coaching!==!0)throw Error("Invalid coaching session");return m}async renewListening(h){return this.post(this.path(`/softphone/${h.coaching?"coach-renew":"listen-renew"}/${k(h.call_id)}`),{session_token:h.session_token})}async stopListening(h){await this.post(this.path(`/softphone/${h.coaching?"coach-stop":"listen-stop"}/${k(h.call_id)}`),{session_token:h.session_token})}async listenerAudit(h){return this.get(this.path(`/softphone/listen-audit/${k(h)}`))}turnPolicy(h={}){return this.post(this.path("/softphone/turn-policy"),h)}configureTURNPolicy(h={}){return this.post(this.path("/numbers/turn-policy"),h)}carrierMediaSettings(h){return this.post(this.path("/numbers/carrier-media"),h)}outboundTransport(h){return this.post(this.path("/numbers/outbound-transport"),h)}outboundNumbers(h={}){return this.get(this.path(h.fresh?"/softphone/numbers?fresh=true":"/softphone/numbers"))}async renew(h){return this.post(this.path(`/softphone/renew/${k(h.call_id)}`),{session_token:h.session_token})}async release(h){if(!h.session_token)throw Error("Answer session has no release token");await this.post(this.path(`/softphone/release/${k(h.call_id)}`),{session_token:h.session_token})}async hangup(h){await this.post(this.path(`/calls/${k(h)}/hangup`),{})}hold(h){return this.post(this.path(`/calls/${k(h)}/hold`),{})}resume(h){return this.post(this.path(`/calls/${k(h)}/resume`),{})}pauseRecording(h){return this.post(this.path(`/calls/${k(h)}/pause-recording`),{})}resumeRecording(h){return this.post(this.path(`/calls/${k(h)}/resume-recording`),{})}createSoftphone(h={}){return new St(this,{...h,audio:{...this.options.audio,...h.audio}})}mediaURL(h,t){return this.resolveMediaURL(h,"media",t)}listenerMediaURL(h,t){return this.resolveMediaURL(h,"listen-media",t)}resolveMediaURL(h,t,m){let _=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),s=new URL(h.media_url,_),f=`/api/apps/telephony/_install/${this.app.installId}/softphone/${t}/${k(h.call_id)}/`;if(s.origin!==_.origin||!["http:","https:"].includes(s.protocol)||s.username||s.password||s.search||s.hash||!s.pathname.startsWith(f)||!/^[A-Za-z0-9_-]+$/.test(s.pathname.slice(f.length)))throw Error("Invalid Telephony media endpoint");if(t==="listen-media"&&s.pathname.slice(f.length)!==h.session_token)throw Error("Listener credential mismatch");if(t==="media"&&Th(h.session_generation))s.searchParams.set("session_generation",h.session_generation);let u=this.transport.mediaURL(s,m);return u.protocol=u.protocol==="https:"?"wss:":"ws:",u.href}session(h,t,m="media",_=ih()){let s=h;if(!s||typeof s.call_id!=="string"||typeof s.media_url!=="string"||t&&s.call_id!==t||s.session_token!==void 0&&typeof s.session_token!=="string")throw Error("Invalid Telephony session response");if(s.lease_seconds!==void 0&&(!Number.isFinite(s.lease_seconds)||s.lease_seconds<10||s.lease_seconds>3600||!s.session_token))throw Error("Invalid media lease");if(m==="listen-media"&&(!s.session_token||s.lease_seconds===void 0))throw Error("Invalid listener lease");return k(s.call_id),this.resolveMediaURL(s,m),{...s,lease_started_ms:_}}}var Km=rh({app:"telephony",create:({app:h})=>new lh(h)});function jm({app:h},t){return new lh(h,t)}export{jm as createClient,m0 as callTerminationLabel};
