function ah(h){let t=globalThis.crypto?.randomUUID?.()??`${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;return`${h}-${t}`}function Gh(h){return{recovery_id:ah("recovery"),attempt_id:ah("attempt"),initiating_action:h}}function Vh(h){return typeof h==="string"&&h.length<=80&&/^(browser|session|recovery|attempt)-[a-f0-9-]{1,64}$/.test(h)?h:void 0}function Mh(h){let t=(u,y)=>typeof u==="string"&&u.length<=y&&/^[a-zA-Z0-9_-]*$/.test(u)?u:void 0,m=["WebSocket transport error","WebRTC signaling error","Audio worker failed","WebRTC signaling unavailable"],_=["attach","takeover","automatic_retry","manual_reconnect","audio_device_change","component_recreation"],M=(u,y)=>typeof u==="number"&&Number.isFinite(u)?Math.max(0,Math.min(y,u)):void 0;return{timestamp:Number.isFinite(Date.parse(h.timestamp))?new Date(h.timestamp).toISOString():new Date().toISOString(),action:t(h.action,40)??"diagnostic",outcome:t(h.outcome,40)??"unknown",code:t(h.code,80),detail:h.detail?m.includes(h.detail)?h.detail:"detail_redacted":void 0,recovery_id:Vh(h.recovery_id),attempt_id:Vh(h.attempt_id),shared_attempt_id:Vh(h.shared_attempt_id),session_id:Vh(h.session_id),previous_session_id:Vh(h.previous_session_id),initiating_action:_.includes(h.initiating_action)?h.initiating_action:void 0,status:M(h.status,599),remaining_ms:M(h.remaining_ms,3600000),was_clean:typeof h.was_clean==="boolean"?h.was_clean:void 0,duration_ms:M(h.duration_ms,86400000)}}var Sh=()=>performance.now();async function zh(h,t,m=5000,_){let M=new AbortController,f,u=new Promise((S,q)=>{f=q}),y=()=>{M.abort(),f(new DOMException("Media request cancelled","AbortError"))};t?.addEventListener("abort",y,{once:!0});let s=setTimeout(y,m);try{if(t?.aborted)return y(),await u;return await Promise.race([h(M.signal).then((S)=>{if(M.signal.aborted)_?.(S);return S}),u])}finally{clearTimeout(s),t?.removeEventListener("abort",y)}}function _h(h){let t=h,m=t?.status,_;try{let f=JSON.parse(t?.body??"{}");if(typeof f.code==="string")_=f.code}catch{}let M=_==="media_lease_expired";return{status:m,code:_,expired:M,denied:!M&&[401,403,404,410].includes(m)}}class Jh{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(h,t,m,_,M=Sh(),f={now:Sh,setTimeout:(u,y)=>setTimeout(u,y),clearTimeout:(u)=>clearTimeout(u)}){this.seconds=h;this.renew=t;this.ended=m;this.event=_;this.clock=f;this.deadline=M+h*1000-1000,this.watchExpiry(),this.schedule(Math.min(h*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(h,t){let{status:m,code:_}=_h(t);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:h,status:m,code:_,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(h){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,h))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let h=this.clock.now();try{let t=await Promise.race([this.renew(),new Promise((m,_)=>{this.requestTimer=this.clock.setTimeout(()=>_(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(t?.lease_seconds!==void 0){if(!Number.isFinite(t.lease_seconds)||t.lease_seconds<10||t.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=t.lease_seconds}this.deadline=h+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(t){if(this.stopped)return;let m=_h(t);if(m.denied||m.expired){this.finish(m.expired?"expired":"revoked",t);return}this.report("retrying",t),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(h,t){if(this.stopped)return;this.report(h,t),this.stop(),this.ended(h,t)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function h0(h){if(h.turnTransport!==void 0&&!["all","udp","tcp","tls"].includes(h.turnTransport))throw RangeError("Invalid TURN transport");if(h.iceTransportPolicy!==void 0&&!["all","relay"].includes(h.iceTransportPolicy))throw RangeError("Invalid ICE policy");if(h.turnTLSPort!==void 0&&(!Number.isInteger(h.turnTLSPort)||h.turnTLSPort<1||h.turnTLSPort>65535||h.turnTransport!=="tls"))throw RangeError("TURN TLS port requires TLS transport and a valid port")}function $h(h){let t=h.ice_transport_policy??"all";if(t!=="all"&&t!=="relay")throw Error("Invalid server ICE policy");let m=h.ice_servers??[];if(!Array.isArray(m)||m.length>8)throw Error("Invalid server ICE configuration");for(let _ of m){let M=typeof _.urls==="string"?[_.urls]:_.urls;if(!Array.isArray(M)||M.length<1||M.length>16||M.some((f)=>typeof f!=="string"||f.length>1024||/[\r\n@#]/.test(f)||!/^stuns?:|^turns?:/.test(f)))throw Error("Invalid server ICE endpoint")}if(t==="relay"&&!m.some((_)=>(typeof _.urls==="string"?[_.urls]:_.urls).some((M)=>/^turns?:/.test(M))))throw Object.assign(Error("Required TURN relay is unavailable"),{code:"turn_relay_unavailable"});return{iceServers:m,iceTransportPolicy:t}}function B0(h,t){h0(t);for(let m of["turn_transport","ice_policy","turn_tls_port"])h.searchParams.delete(m);if(t.turnTransport!==void 0)h.searchParams.set("turn_transport",t.turnTransport);if(t.iceTransportPolicy!==void 0)h.searchParams.set("ice_policy",t.iceTransportPolicy);if(t.turnTLSPort!==void 0)h.searchParams.set("turn_tls_port",String(t.turnTLSPort))}function O0(h){let t={pairID:"",direct:!1,sent:0,received:0,send:!1,receive:!1};if(h.forEach((u)=>{if(u.type==="transport"&&u.selectedCandidatePairId)t.pairID=u.selectedCandidatePairId;if(u.type==="outbound-rtp"&&!u.isRemote)t.sent+=u.packetsSent??0,t.send=!0;if(u.type==="inbound-rtp"&&!u.isRemote)t.received+=u.packetsReceived??0,t.receive=!0}),!t.pairID)h.forEach((u)=>{if(u.type==="candidate-pair"&&u.nominated&&u.state==="succeeded")t.pairID=u.id});let m=h.get(t.pairID),_=m&&h.get(m.localCandidateId),M=m&&h.get(m.remoteCandidateId),f=(u)=>u&&["host","srflx","prflx"].includes(u.candidateType);return t.direct=Boolean(m&&f(_)&&f(M)),t}function d0(h,t){return Boolean(t.pairID&&t.pairID!==h.pairID&&(h.send||h.receive)&&(!h.send||t.sent>h.sent)&&(!h.receive||t.received>h.received))}class Lh{send;active;observe;configured;work=Promise.resolve();peers=new Map;revision=1;closed=!1;retryTimer;retries=0;constructor(h,t,m=()=>{},_){this.send=h;this.active=t;this.observe=m;this.configured=_}register(h,t,m=()=>!0,_){this.remove(h);let M=h==="coach";this.peers.set(h,{pc:t,current:m,revision:0,generation:_,cancellation:new AbortController,offerType:M?"coach.webrtc.restart_offer":"webrtc.restart_offer",answerType:M?"coach.webrtc.restart_answer":"webrtc.restart_answer"})}remove(h){let t=this.peers.get(h);if(t?.cancellation.abort(),t?.pending)clearTimeout(t.pending.timer),t.pending.reject(Error("ICE peer replaced"));this.peers.delete(h)}report(h,t){try{this.observe(h,t)}catch{}}alive(h){return!this.closed&&this.active()&&(!h||h.current()&&h.pc.connectionState!=="closed")}handle(h){if(!["webrtc.ice_config","webrtc.restart_answer","coach.webrtc.restart_answer","webrtc.ice_status"].includes(h.type))return!1;if(!this.alive())return!0;if(h.type==="webrtc.ice_status"){if(this.report(["credential_refresh_unavailable","restart_failed"].includes(h.state)?h.state:"ice_status"),h.state==="restart_failed"){for(let m of this.peers.values())if(m.generation===h.generation&&m.pending&&m.pending.revision===h.revision)clearTimeout(m.pending.timer),m.pending.reject(Error("ICE restart rejected")),m.pending=void 0}return!0}if(h.type.endsWith("restart_answer")){for(let m of this.peers.values()){if(h.type!==m.answerType||m.generation!==h.generation||m.pending?.revision!==h.revision||!this.alive(m))continue;let _=m.pending;if(!_)continue;m.pc.setRemoteDescription({type:"answer",sdp:h.sdp}).then(()=>{if(m.pending!==_)return;if(clearTimeout(_.timer),m.pending=void 0,this.alive(m))_.resolve();else _.reject(Error("ICE session replaced"))}).catch(()=>{if(m.pending===_)clearTimeout(_.timer),m.pending=void 0,_.reject(Error("ICE answer rejected"))})}return!0}if(!Number.isSafeInteger(h.revision)||h.revision<=this.revision)return!0;let t;try{if(t=$h(h),!Number.isFinite(h.expires_at)||h.expires_at*1000<=Date.now()+1000)throw Error()}catch{return this.report("invalid_ice_refresh"),!0}this.revision=h.revision;try{this.configured?.(t)}catch{}return this.report("credentials_refreshed",{code:`ice_rev_${h.revision}`,remaining_ms:Math.max(0,Math.min(3600000,Math.round(h.expires_at*1000-Date.now())))}),this.work=this.work.then(async()=>{if(!this.alive()||this.revision!==h.revision)return;let m=await Promise.allSettled([...this.peers.values()].filter((_)=>this.alive(_)).map((_)=>this.restart(_,t,h.revision)));if(!this.alive()||this.revision!==h.revision)return;if(m.some((_)=>_.status==="rejected"))this.retry();else{if(this.retries=0,this.retryTimer)clearTimeout(this.retryTimer);this.retryTimer=void 0}}).catch(()=>{this.report("restart_failed"),this.retry()}),!0}mediaStats(h,t){return new Promise((m,_)=>{let M=()=>{clearTimeout(u),h.cancellation.signal.removeEventListener("abort",f)},f=()=>{M(),_(Error("ICE peer removed"))},u=setTimeout(()=>{M(),_(Error("ICE media statistics timed out"))},1500);if(h.cancellation.signal.addEventListener("abort",f,{once:!0}),!t()){f();return}Promise.resolve().then(()=>h.pc.getStats()).then((y)=>{M(),t()?m(y):_(Error("ICE peer replaced"))},()=>{M(),_(Error("ICE media statistics unavailable"))})})}retry(){if(this.retryTimer||this.retries>=3||!this.alive())return;this.retryTimer=setTimeout(()=>{if(this.retryTimer=void 0,this.alive()){this.retries++;try{this.send({type:"webrtc.ice_refresh"})}catch{this.report("refresh_request_failed")}}},15000)}async restart(h,t,m){if(h.pending)clearTimeout(h.pending.timer),h.pending.reject(Error("ICE revision superseded")),h.pending=void 0;let _=()=>this.alive(h)&&this.peersHas(h);if(this.revision!==m||!_())return;let M=ah("attempt"),f=performance.now(),u=()=>({attempt_id:M,code:`ice_rev_${m}`,duration_ms:Math.round(performance.now()-f)});try{if(await new Promise((q,b)=>{if(h.pc.signalingState==="stable"){q();return}let A=()=>{clearTimeout(P),h.pc.removeEventListener("signalingstatechange",g),h.cancellation.signal.removeEventListener("abort",R)},g=()=>{if(!_()||h.pc.signalingState==="stable")A(),_()?q():b(Error("ICE peer replaced"))},R=()=>{A(),b(Error("ICE peer removed"))},P=setTimeout(()=>{A(),b(Error("ICE signaling busy"))},1e4);h.pc.addEventListener("signalingstatechange",g),h.cancellation.signal.addEventListener("abort",R,{once:!0}),g()}),!_())return;h.pc.setConfiguration({...h.pc.getConfiguration(),...t});let y=O0(await this.mediaStats(h,_));if(!_())return;if(t.iceTransportPolicy!=="relay"&&h.pc.connectionState==="connected"&&y.direct){h.revision=m,this.report("credentials_applied_direct",u());return}let s=await h.pc.createOffer({iceRestart:!0});if(!_())return;if(await h.pc.setLocalDescription(s),!_())return;if(await new Promise((q,b)=>{if(h.pc.iceGatheringState==="complete"){q();return}let A=()=>{clearTimeout(P),h.pc.removeEventListener("icegatheringstatechange",g),h.cancellation.signal.removeEventListener("abort",R)},g=()=>{if(!_()||h.pc.iceGatheringState==="complete")A(),_()?q():b(Error("ICE session replaced"))},R=()=>{A(),b(Error("ICE session stopped"))},P=setTimeout(()=>{A(),b(Error("ICE gathering timed out"))},7000);h.cancellation.signal.addEventListener("abort",R,{once:!0}),h.pc.addEventListener("icegatheringstatechange",g),g()}),!_())return;if(this.report("restart_started",u()),await new Promise((q,b)=>{let A=setTimeout(()=>{if(h.pending?.revision===m)h.pending=void 0;b(Error("ICE restart timed out"))},1e4);h.pending={revision:m,resolve:q,reject:b,timer:A};try{this.send({type:h.offerType,revision:m,generation:h.generation,sdp:h.pc.localDescription.sdp})}catch{clearTimeout(A),h.pending=void 0,b(Error("ICE signaling unavailable"))}}),_())this.report("restart_signaled",u());let S=performance.now()+1e4;while(_()){let q=O0(await this.mediaStats(h,_));if(!_())return;if(h.pc.connectionState==="connected"&&d0(y,q)){h.revision=m,this.report("restart_completed",u());return}if(performance.now()>=S)throw Error("ICE media did not resume");await new Promise((b,A)=>{let g=()=>{clearTimeout(P),h.cancellation.signal.removeEventListener("abort",R)},R=()=>{g(),A(Error("ICE peer removed"))},P=setTimeout(()=>{g(),b()},100);if(h.cancellation.signal.addEventListener("abort",R,{once:!0}),!_())R()})}}catch(y){if(!_())return;if(_()&&h.pc.signalingState==="have-local-offer")try{await h.pc.setLocalDescription({type:"rollback"})}catch{}if(_())this.report("restart_failed",u());throw y}}peersHas(h){return[...this.peers.values()].includes(h)}stop(){if(this.closed)return;if(this.closed=!0,this.retryTimer)clearTimeout(this.retryTimer);this.retryTimer=void 0;for(let h of[...this.peers.keys()])this.remove(h)}}function Q0(){return new DOMException("Network check cancelled","AbortError")}function r(h,t){return new Promise((m,_)=>{let M=()=>{f(),_(Q0())},f=()=>t.removeEventListener("abort",M);if(t.addEventListener("abort",M,{once:!0}),h.then((u)=>{f(),t.aborted?_(Q0()):m(u)},(u)=>{f(),_(u)}),t.aborted)M()})}class U0{seen=new Set;highest=-1;lastAt;lastSource;uploadCount=0;uploadHighest=-1;upstreamJitter=0;jitter=0;rtts=[];lastEcho=0;sent=0;browserPauseMs=0;lastSend;sentAt(h){if(this.lastSend!==void 0)this.browserPauseMs=Math.max(this.browserPauseMs,h-this.lastSend-20);this.lastSend=h,this.sent++}receive(h,t){if(h.byteLength!==96)return;let m=new DataView(h);if(m.getUint32(0,!0)!==827346516)return;let _=m.getUint32(4,!0);if(_>=1000||this.seen.has(_))return;this.seen.add(_),this.highest=Math.max(this.highest,_);let M=Number(m.getBigUint64(8,!0))/1000;if(this.lastAt!==void 0&&this.lastSource!==void 0){let u=Math.abs(t-this.lastAt-(M-this.lastSource));this.jitter+=(u-this.jitter)/16}this.lastAt=t,this.lastSource=M,this.uploadCount=Math.max(this.uploadCount,m.getUint32(16,!0)),this.uploadHighest=Math.max(this.uploadHighest,m.getUint32(20,!0)),this.upstreamJitter=m.getUint32(32,!0)/1000;let f=Number(m.getBigUint64(24,!0))/1000;if(f>this.lastEcho&&f<=t){if(this.lastEcho=f,this.rtts.push(t-f),this.rtts.length>300)this.rtts.shift()}}result(h,t,m){let _=this.uploadCount>0?this.uploadHighest+1:0,M=this.highest+1,f=_>0?Math.max(0,(_-this.uploadCount)/_*100):0,u=M>0?Math.max(0,(M-this.seen.size)/M*100):0,y=this.rtts.slice().sort((A,g)=>A-g),s=y.length?y[Math.floor((y.length-1)*0.95)]:void 0,S=Math.max(this.jitter,this.upstreamJitter),q=this.browserPauseMs>100?"browser_busy":this.seen.size<100||this.uploadCount<100?"insufficient_samples":void 0,b=q?"inconclusive":Math.max(f,u)>=3||S>30||(s??0)>300?"degraded":"ready";return{path:h,state:b,setupMs:t,durationMs:m,sent:this.sent,received:this.seen.size,uploadLossPct:f,downloadLossPct:u,rttMs:s,jitterMs:S,browserPauseMs:this.browserPauseMs,reason:q}}}async function c0(h,t,m){let _=performance.now(),M,f,u,y=()=>{if(u)clearInterval(u);f?.close()};m.addEventListener("abort",y,{once:!0});try{let s=h({action:"prepare",path:t},m).then((Q)=>{if(m.aborted&&typeof Q?.id==="string")h({action:"cancel",id:Q.id},AbortSignal.timeout(1000)).catch(()=>{});return Q}),S=await r(s,m);if(M=S.id,typeof M!=="string"||!/^session-[a-f0-9-]{1,64}$/.test(M)||S.packet_bytes!==96||S.interval_ms!==20)throw Error("Invalid network test configuration");f=new RTCPeerConnection($h(S));let q=f.createDataChannel("telephony-network-check",{ordered:!1,maxRetransmits:0});q.binaryType="arraybuffer";let b=new U0;q.onmessage=(Q)=>{if(Q.data instanceof ArrayBuffer)b.receive(Q.data,performance.now())};let A=new Promise((Q,H)=>{q.onopen=()=>Q(),q.onerror=()=>H(Error("Network test connection failed"))}),g=await r(f.createOffer(),m);if(await r(f.setLocalDescription(g),m),f.iceGatheringState!=="complete")await r(new Promise((Q)=>{f.onicegatheringstatechange=()=>{if(f.iceGatheringState==="complete")Q()}}),m);let R=await r(h({action:"offer",id:M,sdp:f.localDescription.sdp},m),m);await r(f.setRemoteDescription({type:"answer",sdp:R.sdp}),m),await r(A,m);let P=performance.now()-_,F=performance.now(),a=0;u=setInterval(()=>{if(m.aborted||q.readyState!=="open"||q.bufferedAmount>384||a>=400)return;let Q=new ArrayBuffer(96),H=new DataView(Q),o=performance.now();H.setUint32(0,827346516,!0),H.setUint32(4,a++,!0),H.setBigUint64(8,BigInt(Math.round(o*1000)),!0);try{q.send(Q),b.sentAt(o)}catch{}},20),await r(new Promise((Q)=>{let H=setTimeout(Q,6000);m.addEventListener("abort",()=>clearTimeout(H),{once:!0})}),m);let C=b.result(t,P,performance.now()-F),E=await r(f.getStats(),m),B="";E.forEach((Q)=>{if(Q.type==="transport")B=Q.selectedCandidatePairId??""});let D=E.get(B),$=D&&E.get(D.localCandidateId);return C.candidateType=$?.candidateType,C.relayProtocol=$?.relayProtocol,C}finally{if(m.removeEventListener("abort",y),y(),M){let s=h({action:"cancel",id:M},AbortSignal.timeout(1000)).catch(()=>{});if(!m.aborted)await r(s,AbortSignal.timeout(1000)).catch(()=>{})}}}async function W0(h,t={}){let m=t.paths??["configured"];if(m.length<1||m.length>3||new Set(m).size!==m.length||m.some((f)=>!["configured","direct","relay"].includes(f)))throw Error("Invalid network test paths");let _={state:"unavailable",checkedAt:new Date().toISOString(),paths:[]};for(let f of m){if(t.signal?.aborted)return _.state="cancelled",_;let u=new AbortController,y=()=>u.abort();t.signal?.addEventListener("abort",y,{once:!0});let s=setTimeout(y,18000);try{_.paths.push(await c0(h,f,u.signal))}catch{if(t.signal?.aborted)return _.state="cancelled",_;_.paths.push({path:f,state:"unavailable",setupMs:0,durationMs:0,sent:0,received:0,uploadLossPct:0,downloadLossPct:0,jitterMs:0,browserPauseMs:0,reason:u.signal.aborted?"timeout":"path_unavailable"})}finally{clearTimeout(s),t.signal?.removeEventListener("abort",y)}}let M=_.paths.filter((f)=>f.state==="ready"||f.state==="degraded").sort((f,u)=>(f.uploadLossPct+f.downloadLossPct)*50+f.jitterMs+(f.rttMs??1000)-(u.uploadLossPct+u.downloadLossPct)*50-u.jitterMs-(u.rttMs??1000));if(M.length)_.state=M[0].state,_.recommendedPath=M[0].path;else if(_.paths.some((f)=>f.state==="inconclusive"))_.state="inconclusive";return _}function dh(h){let t=new URL(h),m=["localhost","127.0.0.1","[::1]"].includes(t.hostname);if(t.username||t.password||t.search||t.hash||t.protocol!=="https:"&&!(t.protocol==="http:"&&m)||!t.pathname.endsWith("/runtime-browser"))throw Error("Invalid managed Telephony browser endpoint");return t}function V0(h){if(!h.startsWith("/")||h.startsWith("//")||h.includes("..")||h.includes("\\"))throw Error("Invalid Telephony request path")}function l0(h,t){return new Promise((m)=>{let _=()=>{clearTimeout(M),h.removeEventListener("abort",_),m()},M=setTimeout(_,t);if(h.addEventListener("abort",_,{once:!0}),h.aborted)_()})}class t0{proxy;mode;fetchImpl;direct;info;discovery;credential;issuing;proxyOnly=!1;constructor(h,t="auto",m=globalThis.fetch,_){this.proxy=h;this.mode=t;this.fetchImpl=m;this.direct=_;this.fetchImpl=m.bind(globalThis)}get directBase(){return this.info?.browser_api_base}async prepare(){if(this.mode==="proxy"||this.mode==="auto"&&typeof location>"u")return;if(this.direct){dh(this.direct.baseURL),this.info={managed:!0,browser_api_base:this.direct.baseURL};return}if(this.discovery)return this.discovery;this.discovery=(async()=>{let h;try{h=await this.proxy.get("/runtime-endpoints")}catch(m){if(this.mode==="direct"||![404,405].includes(m.status??0))throw m;this.proxyOnly=!0;return}if(!h?.managed||!h.browser_api_base){if(this.proxyOnly=!0,this.mode==="direct")throw Error("Direct Telephony deployment unavailable");return}if(dh(h.browser_api_base),h.browser_media_base)dh(h.browser_media_base);if(h.browser_signaling_base)dh(h.browser_signaling_base);let t=new URL(this.proxy.mcpURL(),typeof location>"u"?void 0:location.href);if(!["https:","http:"].includes(t.protocol))throw Error("Invalid Telephony management gateway");this.info=h})();try{await this.discovery}catch(h){throw this.discovery=void 0,h}}async grant(){if(this.credential&&this.credential.expires_at*1000>Date.now()+30000)return this.credential;if(this.issuing)return this.issuing;let h=typeof location>"u"?new URL(this.proxy.mcpURL()).origin:location.origin;this.issuing=(async()=>{let t=this.direct?await this.direct.credentialProvider():await this.proxy.post("/runtime-browser-bootstrap",{origin:h});if(!t||typeof t.token!=="string"||!t.token||t.token.length>4096||!Number.isSafeInteger(t.expires_at)||t.expires_at*1000<=Date.now()||t.expires_at*1000>Date.now()+360000||t.endpoint!==this.directBase)throw Error("Invalid managed browser credential");return this.credential=t})();try{return await this.issuing}finally{this.issuing=void 0}}async request(h,t,m,_){if(this.mode!=="direct"&&!this.direct||this.proxyOnly)return h==="GET"?this.proxy.get(t,_):this.proxy.post(t,m,_);if(await this.prepare(),!this.directBase)return h==="GET"?this.proxy.get(t,_):this.proxy.post(t,m,_);return this.requestDirect(h,t,m,_)}async requestDirect(h,t,m,_){V0(t),_?.signal?.throwIfAborted();let M=await this.grant(),f=new URL(this.directBase+t);if(_?.signal?.throwIfAborted(),this.proxy.projectId)f.searchParams.set("project_id",this.proxy.projectId);let u=new Headers(_?.headers);u.set("Authorization","Bearer "+M.token),u.set("Content-Type","application/json");let y=await this.fetchImpl(f,{..._,method:h,headers:u,body:m===void 0?void 0:JSON.stringify(m),credentials:"omit",redirect:"error"});if(!y.ok){if(y.status===403)this.credential=void 0;let s=(await y.text()).slice(0,65536);throw Object.assign(Error(`Telephony HTTP ${y.status}`),{status:y.status,body:s})}return await y.json()}requestMedia(h,t,m){let _=()=>{return m?.signal?.throwIfAborted(),this.directBase&&!this.proxyOnly?this.requestDirect("POST",h,t,m):this.request("POST",h,t,m)};if(this.mode==="auto"&&typeof location<"u"&&!this.proxyOnly)return this.prepare().then(_);return _()}mediaURL(h,t){if(!this.directBase)return h;let m=`/api/apps/telephony/_install/${this.proxy.installId}`,_=t==="webrtc"||t==="auto"?this.info?.browser_signaling_base:this.info?.browser_media_base,M=new URL((_||this.directBase)+h.pathname.slice(m.length));return M.search=h.search,M}subscribe(h,t,m){if(this.mode!=="direct"&&!this.direct||this.proxyOnly)return this.proxy.subscribe(h,t,m);let _=new AbortController,M,f=()=>{_.abort(),M?.close(),m?.signal?.removeEventListener("abort",f)};if(m?.signal?.addEventListener("abort",f,{once:!0}),m?.signal?.aborted)f();return(async()=>{try{if(await this.prepare(),_.signal.aborted)return;if(!this.directBase){M=this.proxy.subscribe(h,t,m);return}V0(h);let u=500;while(!_.signal.aborted){try{let y=await this.grant(),s=new URL(this.directBase+h);if(this.proxy.projectId)s.searchParams.set("project_id",this.proxy.projectId);let S=await this.fetchImpl(s,{headers:{Authorization:"Bearer "+y.token,Accept:"text/event-stream"},credentials:"omit",redirect:"error",signal:_.signal});if(!S.ok||!S.body){if(S.status===403)this.credential=void 0;throw Object.assign(Error(`Telephony stream HTTP ${S.status}`),{status:S.status})}u=500;let q=S.body.getReader(),b=new TextDecoder,A="";try{while(!_.signal.aborted){let{done:g,value:R}=await q.read();if(g)break;if(A+=b.decode(R,{stream:!0}),A=A.replace(/\r\n/g,`
`),A.length>65536)throw Error("Telephony notification frame too large");let P;while((P=A.indexOf(`

`))>=0){let F=A.slice(0,P);A=A.slice(P+2);let a=F.split(`
`).filter((C)=>C.startsWith("data:")).map((C)=>C.slice(5).trimStart()).join(`
`);if(a)try{t(JSON.parse(a))}catch{}}}}finally{await q.cancel().catch(()=>{})}}catch(y){if(_.signal.aborted)return;m?.onError?.(y),u=Math.min(1e4,u*2)}if(!_.signal.aborted)await l0(_.signal,u)}}catch(u){if(!_.signal.aborted)m?.onError?.(u)}})(),{close:f}}}function m0(h){return h}var wh="0.12.53";var p0=["rtt_ms","queue_ms","target_ms","buffered_bytes","underruns","dropped_ms","capture_sequence_gaps","playback_sequence_gaps","main_thread_max_pause_ms","worker_max_tick_gap_ms","capture_sent_ms","capture_muted_ms","capture_dropped_ms","playback_ingress_ms","playback_transport_dropped_ms","playback_source_dropped_ms","clock_uncertainty_ms","clock_sample_age_ms","stats_errors","stats_duration_ms","send_bitrate_bps","receive_bitrate_bps","receive_gap_ms","capture_age_ms","transit_ms","delivery_excess_ms","server_queue_ms","receiver_concealed_delta_ms","receiver_silent_concealed_delta_ms","receiver_concealed_window_ms","requested_playback_target_ms","requested_upload_bitrate_bps","receiver_ssrc","receiver_loss_delta","receiver_loss_window_ms","remote_receiver_ssrc","remote_receiver_loss_delta","remote_receiver_loss_window_ms","receiver_bytesReceived","receiver_packetsReceived","receiver_packetsLost","receiver_jitter","receiver_packetsDiscarded","receiver_concealedSamples","receiver_silentConcealedSamples","receiver_concealmentEvents","receiver_insertedSamplesForDeceleration","receiver_removedSamplesForAcceleration","receiver_totalSamplesReceived","receiver_audioLevel","receiver_totalAudioEnergy","receiver_totalSamplesDuration","receiver_nackCount","receiver_fecPacketsReceived","receiver_fecPacketsDiscarded","receiver_jitter_buffer_interval_ms","receiver_jitter_target_interval_ms","receiver_jitter_minimum_interval_ms","receiver_processing_interval_ms","receiver_totalProcessingDelay","receiver_totalSamplesDecoded","telemetry_skipped_samples","telemetry_skipped_auxiliary","telemetry_queue_age_ms","receiver_last_packet_age_ms","sender_bytesSent","sender_packetsSent","sender_headerBytesSent","sender_retransmittedPacketsSent","sender_retransmittedBytesSent","sender_nackCount","sender_targetBitrate","sender_send_delay_interval_ms","remote_receiver_report_age_ms","remote_receiver_report_updated","sender_packets_delta","sender_window_ms","remote_receiver_packetsLost","remote_receiver_fractionLost","remote_receiver_jitter","remote_receiver_roundTripTime","remote_receiver_totalRoundTripTime","remote_receiver_roundTripTimeMeasurements","remote_sender_packetsSent","remote_sender_bytesSent","pair_availableOutgoingBitrate","pair_availableIncomingBitrate","pair_currentRoundTripTime","pair_totalRoundTripTime","pair_bytesSent","pair_bytesReceived","pair_requestsSent","pair_requestsReceived","pair_responsesSent","pair_responsesReceived","pair_consentRequestsSent","path_revision","codec_clock_rate","codec_channels"],e0={upload_bitrate_control:["standard","guarded"],upload_bitrate_reason:["standard","initial","observation_reset","loss","bandwidth","sustained_upload_delay","stable_recovery"],receiver_processing:["unavailable","per_decoded_sample"],connection:["connected","reconnecting","closed"],context:["running","suspended","interrupted","closed"],muted:["true","false"],device_muted:["true","false"],track:["live","ended"],ice:["new","checking","connected","completed","disconnected","failed","closed"],dtls:["new","connecting","connected","closed","failed"],pair:["frozen","waiting","in-progress","failed","succeeded"],local_candidate:["host","srflx","prflx","relay"],remote_candidate:["host","srflx","prflx","relay"],protocol:["udp","tcp"],relay_protocol:["udp","tcp","tls"],codec:["audio/opus","audio/PCMU","audio/PCMA","pcm16"]};function _0(h){let t={};for(let m of p0)if(typeof h[m]==="number"&&Number.isFinite(h[m]))t[m]=Math.max(0,Math.min(h[m],1000000000000));return t}function M0(h){let t={};for(let[m,_]of Object.entries(e0))if(typeof h[m]==="string"&&_.includes(h[m]))t[m]=h[m];return t}class Th{transport;serial=0;lastEmit=-1/0;previousAt;previous;incident;history=[];pending=[];counters={};constructor(h){this.transport=h}observe(h,t,m=performance.now(),_=new Date().toISOString(),M){if(!Number.isFinite(m)||!Number.isFinite(Date.parse(_)))return;if(this.previousAt!==void 0&&m<this.previousAt)this.lastEmit=-1/0,this.previous=void 0,this.counters={},this.incident=void 0;let f=_0(h),u=M0(t),y=!1;for(let S of["underruns","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealmentEvents","path_revision","stats_errors"])if(f[S]!==void 0){if(this.counters[S]!==void 0&&f[S]>this.counters[S])y=!0;this.counters[S]=f[S]}let s={id:`s${++this.serial}`,timestamp:_,transport:this.transport,reason:y?"incident":"periodic",window_ms:Math.min(60000,Math.max(0,typeof M==="number"&&Number.isFinite(M)?M:this.previousAt===void 0?0:m-this.previousAt)),metrics:f,states:u};if(this.previousAt=m,y&&!this.incident)this.incident={sample:s,context:this.previous};if(m-this.lastEmit>=5000){if(this.incident){if(this.incident.context)this.enqueue({...this.incident.context,reason:"incident_context"});this.enqueue(this.incident.sample),this.incident=void 0}else this.enqueue(s);this.lastEmit=m}this.previous=s}enqueue(h){if(this.history.push(h),this.history.length>24)this.history.shift();if(this.pending.push(h),this.pending.length>8)this.pending.shift()}recent(){return this.history.map((h)=>({...h,metrics:{...h.metrics},states:{...h.states}}))}drain(){return this.pending.splice(0,2)}}function $0(h){let t={...h,metrics:{},states:{},part_index:0,part_count:16},m=[],_=t;for(let M of["metrics","states"])for(let[f,u]of Object.entries(h[M])){let y={..._,[M]:{..._[M],[f]:u}};if(JSON.stringify(y).length>768)m.push(_),_={...t,metrics:{},states:{},[M]:{[f]:u}};else _=y}if(m.push(_),m.length>16)return[];return m.map((M,f)=>({...M,part_index:f,part_count:m.length}))}var n0=["receiver_loss_delta","remote_receiver_loss_delta","receiver_concealed_delta_ms","receiver_ssrc","remote_receiver_ssrc","rtt_ms","queue_ms","target_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealedSamples","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_jitter","buffered_bytes","main_thread_max_pause_ms","worker_max_tick_gap_ms","receiver_processing_interval_ms","path_revision","stats_errors"];function v0(h){let t={};for(let m of n0)if(h.metrics[m]!==void 0){if(t[m]=h.metrics[m],$0({...h,metrics:t}).length>2)delete t[m]}return{...h,metrics:t}}class Oh{send;sendReport;now;auxiliary=[];skippedAuxiliary=0;skippedSamples=0;auxiliaryTurn=!1;pending=[];queuedAt=new Map;lastFull=-1/0;timer;report;reportAt=0;static maxAgeMS=12000;constructor(h,t,m=()=>performance.now()){this.send=h;this.sendReport=t;this.now=m}enqueueReport(h){if(this.report!==void 0)this.skippedAuxiliary++;this.report=h,this.reportAt=this.now(),this.start()}enqueueAuxiliary(h){let t=!!h?.diagnostics?.timing?.runtime;if(t){let m=this.auxiliary.length;this.auxiliary=this.auxiliary.filter((_)=>!_.runtime),this.skippedAuxiliary+=m-this.auxiliary.length}if(this.auxiliary.push({report:h,at:this.now(),runtime:t}),this.auxiliary.length>16)this.auxiliary.shift(),this.skippedAuxiliary++;this.start()}start(){if(!this.timer)this.timer=setInterval(()=>this.tick(),250)}discard(h){this.pending=this.pending.filter((t)=>t.id!==h),this.queuedAt.delete(h),this.skippedSamples++}enqueue(h){let t=this.now(),m=[...h].reverse().find((_)=>_.reason==="incident")??h.at(-1);if(this.skippedSamples+=Math.max(0,h.length-(m?1:0)),m){let _=m.reason==="periodic"&&t-this.lastFull>=30000;if(_)this.lastFull=t;let M=_?m:v0(m);M={...M,metrics:{...M.metrics,telemetry_skipped_samples:this.skippedSamples,telemetry_skipped_auxiliary:this.skippedAuxiliary}};let f=$0(M),u=this.pending[0]?.id;for(let y of this.queuedAt.keys())if(y!==u)this.discard(y);if(this.queuedAt.has(M.id))this.discard(M.id);if(f.length)this.pending.push(...f),this.queuedAt.set(M.id,t)}if(this.pending.length)this.start()}tick(){let h=this.now();for(let[t,m]of this.queuedAt)if(h-m>Oh.maxAgeMS)this.discard(t);if(this.auxiliary=this.auxiliary.filter((t)=>{if(h-t.at>Oh.maxAgeMS)return this.skippedAuxiliary++,!1;return!0}),this.report!==void 0&&h-this.reportAt>Oh.maxAgeMS)this.report=void 0,this.skippedAuxiliary++;try{if(this.report!==void 0&&this.sendReport){if(this.sendReport(this.report,Math.max(0,h-this.reportAt)))this.report=void 0}else if(this.auxiliary.length&&this.sendReport&&(!this.pending.length||this.auxiliaryTurn)){if(this.sendReport(this.auxiliary[0].report,Math.max(0,h-this.auxiliary[0].at)))this.auxiliary.shift(),this.auxiliaryTurn=!1}else if(this.pending.length){let t=this.pending[0],m={...t.metrics,telemetry_queue_age_ms:Math.max(0,h-(this.queuedAt.get(t.id)??h))};if(this.send({...t,metrics:m})){if(this.pending.shift(),t.part_index===t.part_count-1)this.queuedAt.delete(t.id);this.auxiliaryTurn=!0}}}catch{}if(!this.pending.length&&!this.auxiliary.length&&this.report===void 0)clearInterval(this.timer),this.timer=void 0}stop(){clearInterval(this.timer),this.timer=void 0,this.pending=[],this.queuedAt.clear(),this.auxiliary=[],this.report=void 0}}class Zh{emit;now;timestamp;lastTick;suspendedAt;contextState;environmentCleanup=[];counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(h,t=()=>performance.now(),m=()=>new Date().toISOString()){this.emit=h;this.now=t;this.timestamp=m}observeEnvironment(){this.stopEnvironment();let h=(t,m,_)=>{if(!t)return;try{t.addEventListener(m,_),this.environmentCleanup.push(()=>t.removeEventListener(m,_))}catch{}};try{if(typeof document<"u"){h(document,"visibilitychange",()=>this.report("tab_visibility",document.visibilityState));for(let t of["freeze","resume"])h(document,t,()=>this.report("page_lifecycle",t));this.report("tab_visibility",document.visibilityState)}if(typeof window<"u")for(let t of["pagehide","pageshow"])h(window,t,()=>this.report("page_lifecycle",t));if(typeof navigator<"u")h(navigator.mediaDevices,"devicechange",()=>this.report("microphone","devices_changed"))}catch{}}stopEnvironment(){for(let h of this.environmentCleanup.splice(0))try{h()}catch{}}tick(){let h=this.now();if(this.lastTick!==void 0){let t=Math.max(0,h-this.lastTick-1000);if(t>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,t),this.report("main_thread","scheduling_gap",t)}this.lastTick=h}context(h){if(h===this.contextState)return;this.contextState=h;let t=this.now();if(h==="suspended"||h==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=t,this.counters.audio_context_suspend_count++;this.report("audio_context",h)}else{let m;if(this.suspendedAt!==void 0)m=Math.max(0,t-this.suspendedAt),this.counters.audio_context_suspended_ms+=m,this.suspendedAt=void 0;this.report("audio_context",h,m)}}report(h,t,m){try{this.emit({timestamp:this.timestamp(),action:h,outcome:t,duration_ms:m===void 0?void 0:Math.round(m)})}catch{}}}async function l(h,t,m){let _=performance.now(),M=(f)=>{try{t?.({timestamp:new Date().toISOString(),action:h,outcome:f,duration_ms:Math.round(Math.max(0,performance.now()-_))})}catch{}};M("started");try{let f=await m();return M("completed"),f}catch(f){throw M("failed"),f}}var w0=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function ch(h){let t=(h??"FR").trim().toUpperCase();return w0[t]??w0.FR}function r0(h,t){let m=[];if(!(h.cadence.reduce((f,u)=>f+u,0)>0)||!(t>0))return m;let M=0;while(M<t)for(let f=0;f<h.cadence.length;f+=2){let u=h.cadence[f]??0,y=h.cadence[f+1]??0;if(M>=t)break;if(u>0)m.push({start:M,end:Math.min(M+u,t)});M+=u+y}return m}function lh(h,t,m,_=0.12){let M=h.createGain();M.gain.value=0,M.connect(t);let f=_/Math.max(1,m.frequencies.length),u=m.frequencies.map((g)=>{let R=h.createOscillator();return R.type="sine",R.frequency.value=g,R.connect(M),R.start(),R}),y=h.currentTime+0.05,s=0,S=0.01,q=()=>{let g=h.currentTime-y+10;if(g<=s)return;for(let R of r0(m,g)){if(R.end<=s)continue;let P=y+Math.max(R.start,s),F=y+R.end;M.gain.setValueAtTime(0,P),M.gain.linearRampToValueAtTime(f,P+S),M.gain.setValueAtTime(f,Math.max(P+S,F-S)),M.gain.linearRampToValueAtTime(0,F)}s=g};q();let b=setInterval(q,4000),A=!1;return()=>{if(A)return;A=!0,clearInterval(b);try{M.gain.cancelScheduledValues(0)}catch{}M.gain.value=0;for(let g of u){try{g.stop()}catch{}g.disconnect()}M.disconnect()}}var sh=24000,Kh=60;async function H0(h,t){try{await h.audioWorklet.addModule(t)}catch(m){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:m})}}function o0(h){let t=new Int16Array(h.length);for(let m=0;m<h.length;m++){let _=Math.max(-1,Math.min(1,h[m]));t[m]=_<0?_*32768:_*32767}return t.buffer}class Ph{history=new Float32Array(64);phase=0;process(h,t,m){if(t===m)return h;let _=new Float32Array(64+h.length);_.set(this.history),_.set(h,64);let M=t/m,f=Math.min(1,m/t)*0.9,u=[],y=this.phase;for(;y<h.length;y+=M){let s=y+32,S=0,q=0;for(let b=Math.ceil(s-32);b<=Math.floor(s+32);b++){let A=b-s,R=(Math.abs(A)<0.000000001?f:Math.sin(Math.PI*f*A)/(Math.PI*A))*(0.5+0.5*Math.cos(Math.PI*A/32));if(b>=0&&b<_.length)S+=_[b]*R,q+=R}u.push(q?S/q:0)}return this.phase=y-h.length,this.history=_.slice(-64),new Float32Array(u)}}function ht(h){let t=0;for(let m=0;m<h.length;m++)t+=h[m]*h[m];return Math.sqrt(t/Math.max(1,h.length))}function N0(h,t){let m=new Map(h.map((_)=>[_.id,_]));for(let _ of t){let M=m.get(_.id);if(!M||(!M.complete||_.complete)&&(!M.ended_at||!!_.ended_at)&&_.duration_ms>=M.duration_ms)m.set(_.id,{..._})}return[...m.values()].sort((_,M)=>_.started_at.localeCompare(M.started_at)).slice(-100)}function tt(h,t){return h.map((m)=>m.ended_at?m:{...m,ended_at:m.observed_until,end_reason:t,complete:!1})}var fh={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function v(h){if(h.mediaTransport!==void 0&&!["websocket","webrtc","auto"].includes(h.mediaTransport))throw RangeError("Unsupported softphone media transport");if(h0(h),h.webrtcFec!==void 0&&typeof h.webrtcFec!=="boolean")throw RangeError("webrtcFec must be boolean");let t=h.playbackTargetMs??Kh,m=h.playbackMinMs??Math.min(Kh,t),_=h.playbackMaxMs??280;for(let M of[t,m,_])if(!Number.isFinite(M)||M<40||M>280)throw RangeError("Playback buffers must be between 40 and 280 ms");if(m>t||t>_)throw RangeError("Playback buffers require min <= target <= max");if(h.playbackAdaptive!==void 0&&typeof h.playbackAdaptive!=="boolean")throw RangeError("playbackAdaptive must be boolean");return{initialTargetMs:t,minTargetMs:m,maxTargetMs:_,hardMaxMs:320,adaptiveReserve:h.playbackAdaptive!==!1}}function qh(h){return{...h.inputDeviceId?{deviceId:{exact:h.inputDeviceId}}:{},echoCancellation:h.echoCancellation,noiseSuppression:h.noiseSuppression,autoGainControl:h.autoGainControl}}function X0(h){let t=h.getSettings();return{deviceLabel:h.label||"Default microphone",sampleRate:typeof t.sampleRate==="number"?t.sampleRate:null,channelCount:typeof t.channelCount==="number"?t.channelCount:null,echoCancellation:typeof t.echoCancellation==="boolean"?t.echoCancellation:null,noiseSuppression:typeof t.noiseSuppression==="boolean"?t.noiseSuppression:null,autoGainControl:typeof t.autoGainControl==="boolean"?t.autoGainControl:null}}function Nh(h){if(!Number.isFinite(h)||h<=0)return null;return 20*Math.log10(h)}function mt(h,t,m){let _=new ArrayBuffer(44+t*2),M=new DataView(_),f=(y,s)=>{for(let S=0;S<s.length;S++)M.setUint8(y+S,s.charCodeAt(S))};f(0,"RIFF"),M.setUint32(4,36+t*2,!0),f(8,"WAVE"),f(12,"fmt "),M.setUint32(16,16,!0),M.setUint16(20,1,!0),M.setUint16(22,1,!0),M.setUint32(24,m,!0),M.setUint32(28,m*2,!0),M.setUint16(32,2,!0),M.setUint16(34,16,!0),f(36,"data"),M.setUint32(40,t*2,!0);let u=44;for(let y of h)for(let s=0;s<y.length;s++)M.setInt16(u,y[s],!0),u+=2;return new Blob([_],{type:"audio/wav"})}class f0{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new Ph;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(h,t=!0){this.onLevel=h;this.recordAudio=t}async start(h,t){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:qh(t)}),this.ensureOpen();let m=this.stream.getAudioTracks()[0];if(!m)throw Error("No microphone audio track was returned.");this.settings=X0(m);try{this.ctx=new AudioContext({sampleRate:sh,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await H0(this.ctx,h),this.ensureOpen();let _=this.ctx.sampleRate,M=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:t.inputGainDB,highpassFilter:t.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(f)=>{if(this.stopped)return;if(!(f.data instanceof Float32Array)){if(f.data.type==="capture.stats")this.peak=Math.max(this.peak,f.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,f.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,f.data.limiter_reduction_db??0);return}let u=f.data;if(_!==sh)u=this.resampler.process(u,_,sh);let y=ht(u);if(this.onLevel?.(y),this.recordAudio)this.frames.push(new Int16Array(o0(u)));if(this.samples+=u.length,y>=0.005){for(let s=0;s<u.length;s++)this.activeSquares+=u[s]*u[s];this.activeSamples+=u.length}},M.connect(this.capture),this.settings}catch(m){throw await this.release(),m}}async stop(){this.stopped=!0;let h=this.settings,t=this.frames,m=this.samples,_=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,M=this.peak;if(await this.release(),!h||m===0)throw Error("No microphone audio was captured.");return{audio:mt(t,m,sh),durationMs:Math.round(m*1000/sh),sampleRate:sh,activeRmsDbfs:Nh(_),peakDbfs:Nh(M),postPeakDbfs:Nh(this.postPeak||M),limiterReductionDb:this.limiterReductionDB,settings:h}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let h of this.stream?.getTracks()??[])h.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class u0{callbacks;clientEpoch=crypto.randomUUID();transportTelemetry=new Th("websocket");transportSender=new Oh((h)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[h]}})}),!0},(h)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify(h)}),!0});worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new Zh((h)=>this.recordSessionEvent(h));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;sessionReady=!1;mediaPathReady;mediaRevision=0;callAnswered=!1;playbackHeld=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={mediaTransport:"websocket",codec:"pcm16",rttMs:null,queueMs:0,targetMs:Kh,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:sh,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:fh.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(h={}){this.callbacks=h}get isMuted(){return this.muted}async start(h,t,m,_=fh,M){let f=v(_);this.diagnostics.targetMs=f.initialTargetMs,this.callbacks.onState?.("connecting"),this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+wh.replaceAll(".","_")}),this.runtimeTelemetry.observeEnvironment(),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{if(M)this.stream=M.stream,this.ctx=M.context;this.stream=M?.stream??await l("microphone_device",(S)=>this.recordSessionEvent(S),()=>navigator.mediaDevices.getUserMedia({audio:qh(_)})),this.ensureOpen();let u=this.stream.getAudioTracks()[0];if(!u)throw Error("No microphone audio track was returned.");if(u.readyState==="ended")throw Error("Microphone disconnected before audio setup.");u.onmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser.")},u.onunmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),this.callbacks.onNotice?.("Microphone input restored.")},u.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let y=X0(u);if(this.diagnostics={...this.diagnostics,microphoneSampleRate:y.sampleRate??0,microphoneChannelCount:y.channelCount??0,echoCancellation:y.echoCancellation,noiseSuppression:y.noiseSuppression,autoGainControl:y.autoGainControl,micInputGainDb:_.inputGainDB},!this.ctx)try{this.ctx=new AudioContext({sampleRate:sh,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await l("audio_context",(S)=>this.recordSessionEvent(S),()=>this.ctx.resume());if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),_.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(_.outputDeviceId),this.ensureOpen();await l("audio_worklet",(S)=>this.recordSessionEvent(S),()=>H0(this.ctx,t)),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let s=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:_.inputGainDB,highpassFilter:_.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:{...f,telemetryEpoch:this.clientEpoch,telemetryEnabled:!1}}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),s.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=_.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await l("audio_transport",(S)=>this.recordSessionEvent(S),()=>this.openWorker(h,m)),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.setPlaybackObservation(this.ctx?.state==="running"&&this.microphoneTransportReady&&!this.playbackHeld,"audio_context_paused"),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running")this.updateLiveState()},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(u){if(this.closed)this.teardown();if(!this.closed)this.fail(u instanceof Error?u.message:"browser audio setup failed");throw u}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){await this.ctx?.resume(),this.updateLiveState()}setOutputVolume(h){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,h))}sendDTMF(h){if(/^[0-9*#]+$/.test(h))this.sendText(JSON.stringify({type:"dtmf",digits:h}))}startRingback(h){if(this.closed||!this.ctx||this.ringback)return;this.ringback=lh(this.ctx,this.ctx.destination,ch(h))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(h)=>{let t=h.data;if(t?.type!=="capture.stats")return;this.micLevel=this.muted?0:t.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:Nh(t.active_rms??0),micPeakDbfs:Nh(t.pre_peak??0),micPostPeakDbfs:Nh(t.post_peak??0),micInputGainDb:t.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:t.limiter_reduction_db??0}},this.playback.port.onmessage=(h)=>{let t=h.data;if(t?.type==="playback.underrun"){this.diagnostics.underrunEvents=N0(this.diagnostics.underrunEvents??[],[t.event]),this.diagnostics.underrunDurationMs=t.underrun_ms,this.diagnostics.underruns=t.underruns;return}if(t?.type!=="stats")return;this.diagnostics.playbackEvents=t.playback_events,this.playbackTiming={reserve_expanded_ms:t.reserve_expanded_ms,reserve_compressed_ms:t.reserve_compressed_ms,reserve_adjustments:t.reserve_adjustments,reserve_match_rejections:t.reserve_match_rejections,played_ms:t.played_ms,max_residence_ms:t.max_residence_ms,drop_totals_ms:t.drop_totals_ms,coaching:{played_ms:t.whisper_played_ms,dropped_ms:t.whisper_dropped_ms,max_queue_ms:t.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,t.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:t.whisper_played_ms??0,coachingDroppedMs:t.whisper_dropped_ms??0,coachingMaxQueueMs:t.whisper_max_queue_ms??0,queueMs:t.queue_ms??0,targetMs:t.target_ms??Kh,underruns:t.underruns??0,droppedMs:t.dropped_ms??0,underrunDurationMs:t.underrun_ms??0,underrunEvents:N0(this.diagnostics.underrunEvents??[],t.underrun_events??[]),maxQueueMs:t.max_queue_ms??0,playbackSequenceGaps:t.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,t.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(h,t){return new Promise((m,_)=>{let M=new Worker(t);this.worker=M;let f=new MessageChannel,u=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:f.port1},[f.port1]),this.playback?.port.postMessage({type:"transport",port:u.port1},[u.port1]);let y=!1,s=setTimeout(()=>{if(y)return;y=!0,_(Error("audio connection timed out"))},1e4),S=(q)=>{if(y)return;if(y=!0,clearTimeout(s),q)_(q);else m()};this.cancelWorkerStart=()=>S(Error("audio session closed")),M.onmessage=(q)=>{if(this.closed){S(Error("audio session closed"));return}let b=q.data;if(b?.type==="socket.open")this.reportedPlaybackIDs.clear(),this.reportedCaptureQueueIDs.clear(),this.opened=!0,this.mediaSocketConnected=!0,this.mediaPathReady=void 0,this.mediaRevision=0,this.sessionReady=!1,this.startRTTProbe(),S();else if(b?.type==="runtime.event")this.recordSessionEvent(b.event);else if(b?.type==="socket.message")this.handleControl(b.data);else if(b?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL(b.recovery).then((A)=>{if(!this.closed&&this.worker===M)M.postMessage({type:"socket.credentials",id:b.id,mediaURL:A})},(A)=>{if(this.closed||this.worker!==M)return;let g=_h(A);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:g.denied?"revoked":"retrying",...b.recovery,status:g.status,code:g.code}),M.postMessage({type:"socket.credentials",id:b.id,denied:g.denied})})}else if(b?.type==="socket.error")this.recordSessionEvent({timestamp:b.timestamp,action:"websocket",outcome:"transport_error"});else if(b?.type==="socket.close")if(this.recordSessionEvent({timestamp:b.timestamp,action:"websocket",outcome:"closed",code:String(b.code),detail:b.reason,was_clean:b.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"transport_disconnected"),this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else S(Error("audio connection closed before it was ready"));else if(b?.type==="socket.failed")this.mediaSocketConnected=!1,S(Error(b.detail||"audio connection lost")),this.fail(b.detail||"audio connection lost",b.recoverable===!0);else if(b?.type==="transport.drop"&&b.event)this.diagnostics.dropEvents=this.mergeDropEvents(b.event);else if(b?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=b.buffered_bytes??0,this.diagnostics.captureQueueEvents=b.capture_queue_events,this.transportTiming=b.timing??{},b.observation)this.transportTelemetry.observe({...this.transportTiming,...b.observation,rtt_ms:b.timing?.rtt_ms,queue_ms:this.diagnostics.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:b.buffered_bytes,underruns:this.diagnostics.underruns,dropped_ms:this.diagnostics.droppedMs,capture_sequence_gaps:this.diagnostics.captureSequenceGaps,playback_sequence_gaps:this.diagnostics.playbackSequenceGaps,main_thread_max_pause_ms:this.runtimeTelemetry.counters.main_thread_max_pause_ms},{connection:this.mediaSocketConnected?"connected":"reconnecting",context:this.ctx?.state,muted:String(this.muted),device_muted:String(this.stream?.getAudioTracks()[0]?.muted),track:this.stream?.getAudioTracks()[0]?.readyState,codec:"pcm16"},performance.now(),b.observation?.timestamp,b.observation?.window_ms);if(this.diagnostics.transportSamples=this.transportTelemetry.recent(),typeof b.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(b.timing.rtt_ms)}},M.onerror=(q)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:q.message?.slice(0,160)}),S(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},M.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:h,contextRate:this.ctx?.sampleRate??sh,muted:this.muted,capturePort:f.port2,playbackPort:u.port2},[f.port2,u.port2])})}mergeDropEvents(h,t){if(h)this.workerDropEvents=[...this.workerDropEvents,h].slice(-100);if(t)this.playbackDropEvents=t.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((m,_)=>m.timestamp.localeCompare(_.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;shutdownIntent="session_cleanup";reportedPlaybackIDs=new Set;reportedCaptureQueueIDs=new Set;handleControl(h){if(this.closed)return;try{let t=JSON.parse(h);if(t.type==="media.ready"||t.type==="media.not_ready"){let m=t;if(!Number.isSafeInteger(m.revision)||m.revision<=this.mediaRevision)return;this.mediaRevision=m.revision,this.mediaPathReady=m.type==="media.ready";try{this.callbacks.onMediaReadiness?.(m)}catch{}if(!this.mediaPathReady&&this.callAnswered)this.callbacks.onState?.("reconnecting","Connecting carrier audio…");this.updateLiveState()}else if(t.type==="ready")this.sessionReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState();else if(t.type==="dtmf.error"||t.type==="dtmf.sent")this.callbacks.onNotice?.(t.type==="dtmf.sent"?"Keypad tone sent":t.detail||"Keypad tone failed");else if(t.type==="pong"){if(this.diagnostics.captureSequenceGaps=t.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof t.nonce==="number"&&t.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-t.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(t.type==="call.ended"||t.type==="session.replaced"){this.setPlaybackObservation(!1,t.type==="call.ended"?"call_ended":"observation_ended"),this.shutdownIntent=t.type==="call.ended"?"call_ended":"session_replaced",this.closed=!0;try{this.callbacks.onState?.("ended",t.type)}finally{this.teardown()}}else if(t.type==="call.status"&&typeof t.call_id==="string"&&typeof t.status==="string"){if(t.hold_state)this.playbackHeld=t.hold_state!=="active",this.setPlaybackObservation(!this.playbackHeld&&this.microphoneTransportReady&&this.ctx?.state==="running",this.playbackHeld?"hold":"observation_resumed");if(t.hold_state&&t.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callAnswered=["answered","in-progress"].includes(t.status),this.updateLiveState(),this.callbacks.onCallStatus?.(t)}else if(t.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:t.detail?.slice(0,160)}),this.fail(t.detail||"The call could not be connected.");else if(t.type==="coach.state")this.callbacks.onNotice?.(t.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(t.type==="audio.health"){let m=t;if(m.state!=="healthy"&&m.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:m.state,reason:m.reason,stages:m.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),m.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&m.state==="healthy"&&m.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(t.type==="media.delivery"){let m=t.state;if(m==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(m==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=m==="stalled"}else if(t.type==="peer.disconnected")this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"carrier_disconnected"),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(t.type==="peer.connected")this.microphoneTransportReady=!0,this.setPlaybackObservation(this.ctx?.state==="running"&&!this.playbackHeld,"carrier_connected"),this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState()}catch{}}updateLiveState(){if(this.sessionReady&&this.callAnswered&&this.microphoneTransportReady&&this.mediaPathReady!==!1&&this.ctx?.state==="running")this.callbacks.onState?.("live")}startRTTProbe(){this.stopRTTProbe();let h=()=>{this.sendDiagnostics()};h(),this.pingTimer=setInterval(h,5000)}sendText(h){this.worker?.postMessage({type:"send.text",data:h})}recordSessionEvent(h){try{h={...this.callbacks.sessionDiagnostics?.(),...h}}catch{}h=Mh(h),this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],h].slice(-50);try{this.callbacks.onSessionEvent?.(h)}catch{}try{let t={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,session_events:[h]}};if(new TextEncoder().encode(JSON.stringify(t)).byteLength<=1100)this.transportSender.enqueueAuxiliary(t);else this.transportSender.skippedAuxiliary++}catch{}}sendDiagnostics(){let h=this.diagnostics,t=(M,f)=>{let u=(M??[]).slice(-64),y=u.filter((s)=>!f.has(s.id)).slice(0,8);if(this.mediaSocketConnected){let s=new Set(u.map((S)=>S.id));for(let S of f)if(!s.has(S))f.delete(S);for(let S of y)f.add(S.id)}return y},m=t(h.playbackEvents,this.reportedPlaybackIDs),_=t(h.captureQueueEvents,this.reportedCaptureQueueIDs);if(this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{media_transport:"websocket",codec:"pcm16",client_version:wh,client_epoch:this.clientEpoch,playback_events:m,capture_queue_events:_,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:h.rttMs,playback_queue_ms:h.queueMs,playback_target_ms:h.targetMs,playback_max_queue_ms:h.maxQueueMs,playback_underruns:h.underruns,playback_underrun_ms:h.underrunDurationMs,playback_underrun_events:h.underrunEvents,playback_dropped_ms:h.droppedMs,websocket_buffered_bytes:h.websocketBufferedBytes,audio_context_rate:h.audioContextRate,microphone_sample_rate:h.microphoneSampleRate,microphone_channel_count:h.microphoneChannelCount,echo_cancellation:h.echoCancellation,noise_suppression:h.noiseSuppression,auto_gain_control:h.autoGainControl,mic_active_rms_dbfs:h.micActiveRmsDbfs,mic_peak_dbfs:h.micPeakDbfs,mic_post_peak_dbfs:h.micPostPeakDbfs,mic_input_gain_db:h.micInputGainDb,mic_limiter_reduction_db:h.micLimiterReductionDb,capture_sequence_gaps:h.captureSequenceGaps,playback_sequence_gaps:h.playbackSequenceGaps,drop_events:h.dropEvents}})),this.mediaSocketConnected)this.transportSender.enqueue(this.transportTelemetry.drain());this.callbacks.onDiagnostics?.({...h})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setPlaybackObservation(h,t){try{if(this.playback?.port.postMessage({type:"playback.telemetry.boundary",active:h,reason:t,audio_time_ms:(this.ctx?.currentTime??0)*1000}),!h)this.diagnostics.underrunEvents=tt(this.diagnostics.underrunEvents??[],t)}catch{}}setMuted(h){let t=this.muted!==h;if(this.muted=h,this.worker?.postMessage({type:"muted",value:h}),this.capture?.port.postMessage({type:"muted",value:h}),h)this.sendText(JSON.stringify({type:"interrupt"}));if(t)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:h?"muted":"unmuted"})}stop(){this.shutdownIntent="user_stop";let h=!this.closed;this.closed=!0;try{if(h)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(h,t=!1){if(this.shutdownIntent="audio_error",!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:h.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",h,{recoverable:t})}finally{this.teardown()}}teardown(){this.runtimeTelemetry.stopEnvironment(),this.transportSender.stop(),this.setPlaybackObservation(!1,"observation_ended"),this.stopRingback(),this.microphoneTransportReady=!1,this.sessionReady=this.callAnswered=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let h=this.worker;if(h?.postMessage({type:"close",reason:this.shutdownIntent}),h){let t=setTimeout(()=>h.terminate(),600);h.onmessage=(m)=>{if(m.data?.type==="socket.shutdown.complete")clearTimeout(t),h.terminate()}}this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((t)=>t.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var hm=sh*Kh/1000;var ph=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
`;var Y0=`// Passive receiver only. Both directions share one rendering clock and delay.
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
`;var y0=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
`;var z0=new WeakMap,eh=new Map,ut=8000;async function Dh(h,t){let m=t==="worklet"?ph:t==="worker"?y0:Y0;if(typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin){let S=[URL.createObjectURL(new Blob([m],{type:"text/javascript"}))];return{urls:S,dispose(){S.splice(0).forEach((q)=>URL.revokeObjectURL(q))}}}if(h.name!=="telephony"||!h.projectId||!Number.isSafeInteger(h.installId)||h.installId<=0)throw Error("Telephony audio requires a project and installation");let _=new URL(h.mcpURL(),location.href),M=`${_.origin}:${_.pathname}:${h.projectId}:${h.installId}`,f=z0.get(h);if(!f||f.scope!==M)f={scope:M,entries:new Map},z0.set(h,f);let u=f.entries,y=t,s=u.get(y);if(!s){s=(async()=>{let q=eh.get(t);if(!q)q=crypto.subtle.digest("SHA-256",new TextEncoder().encode(m)).then((R)=>Array.from(new Uint8Array(R),(P)=>P.toString(16).padStart(2,"0")).join("")),eh.set(t,q),q.catch(()=>{if(eh.get(t)===q)eh.delete(t)});let b=`/ui/frontend/${t}-${await q}.js`,A=new AbortController,g;try{if(await Promise.race([h.get(b,{cache:"force-cache",redirect:"error",signal:A.signal,headers:{Accept:"text/plain"}}),new Promise((F,a)=>{g=setTimeout(()=>{a(Error("Telephony audio asset preparation timed out")),A.abort()},ut)})])!==m)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let P=new URL(`/api/apps/telephony/_install/${h.installId}${b}`,_);return P.searchParams.set("project_id",h.projectId),P.searchParams.set("install_id",String(h.installId)),P.href}finally{clearTimeout(g)}})(),u.set(y,s);let S=s;S.catch(()=>{if(u.get(y)===S)u.delete(y)})}return{urls:[await s],dispose(){}}}function ih(h=!1){let t=(h?[ph]:[ph,y0]).map((m)=>URL.createObjectURL(new Blob([m],{type:"text/javascript"})));return{urls:t,dispose(){t.splice(0).forEach((m)=>URL.revokeObjectURL(m))}}}async function Eh(h,t=!1){if(typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin)return ih(t);let m=await Promise.all(t?[Dh(h,"worklet")]:[Dh(h,"worklet"),Dh(h,"worker")]);return{urls:m.flatMap((_)=>_.urls),dispose(){m.forEach((_)=>_.dispose())}}}var S0=Object.freeze({upload_bitrate_control:"standard",media_transport:"inherit",initial_bitrate_bps:32000,min_bitrate_bps:16000,max_bitrate_bps:32000,playback_target_ms:60,playback_max_ms:160,quality_warnings:!1,degraded_seconds:6,warning_cooldown_seconds:60,background_check:!1,allow_user_overrides:!1});function Ch(h){if(!h||typeof h!=="object"||Array.isArray(h))throw Error("Invalid network policy");let t={...S0,...h};for(let m of["initial_bitrate_bps","min_bitrate_bps","max_bitrate_bps","playback_target_ms","playback_max_ms","degraded_seconds","warning_cooldown_seconds"])if(!Number.isInteger(t[m]))throw Error("Invalid network policy bounds");for(let m of["quality_warnings","background_check","allow_user_overrides"])if(typeof t[m]!=="boolean")throw Error("Invalid network policy switch");if(!["standard","guarded"].includes(t.upload_bitrate_control)||!["inherit","auto","webrtc","websocket"].includes(t.media_transport)||t.min_bitrate_bps<16000||t.max_bitrate_bps>32000||t.initial_bitrate_bps<t.min_bitrate_bps||t.initial_bitrate_bps>t.max_bitrate_bps||t.playback_target_ms<40||t.playback_target_ms>120||t.playback_max_ms<t.playback_target_ms||t.playback_max_ms>200||t.degraded_seconds<4||t.degraded_seconds>30||t.warning_cooldown_seconds<30||t.warning_cooldown_seconds>300)throw Error("Invalid network policy bounds");return t}class s0{profile;bitrate;reason="initial";last;path;ssrc;baseline;pairResponses;pairRTT;pairAt;congested;stable;changed=-1/0;constructor(h){this.profile=h;this.bitrate=h.initial_bitrate_bps}observe(h,t){if(!Number.isFinite(t))return;if(this.last!==void 0&&(t<=this.last||t-this.last>5000)||this.path!==h.path_revision||this.ssrc!==h.remote_receiver_ssrc)this.baseline=this.congested=this.stable=void 0,this.pairResponses=this.pairRTT=this.pairAt=void 0,this.changed=t,this.reason="observation_reset";this.last=t,this.path=h.path_revision,this.ssrc=h.remote_receiver_ssrc;let{pair_responsesReceived:_,pair_currentRoundTripTime:M}=h;if(Number.isFinite(_)&&_>0){if((this.pairResponses===void 0||_>this.pairResponses)&&Number.isFinite(M)&&M>0&&M<5)this.pairRTT=M,this.pairAt=t;this.pairResponses=_}let f=h.remote_receiver_report_age_ms>=0&&h.remote_receiver_report_age_ms<=5000&&h.sender_packets_delta>0&&h.sender_window_ms>0&&h.sender_window_ms<=5000,u=h.remote_receiver_fractionLost;if(!f||!Number.isFinite(u)||u<0||u>1){this.congested=this.stable=void 0;return}if(h.remote_receiver_report_updated!==1)return;let y=h.remote_receiver_roundTripTime??(this.pairAt!==void 0&&t-this.pairAt<=5000?this.pairRTT:void 0),s=y!==void 0&&Number.isFinite(y)&&y>0&&y<5;if(s)this.baseline=Math.min(this.baseline??y,y);let S=s&&this.baseline!==void 0?y-this.baseline:0,q=h.pair_availableOutgoingBitrate,b=Number.isFinite(q)&&q>0&&q<64000?Math.max(this.profile.min_bitrate_bps,Math.floor((q-32000)*0.8/1000)*1000):void 0,A=u>=0.01||h.sender_send_delay_interval_ms>=20||b!==void 0&&b<this.bitrate;if(s&&S>=0.08&&A)this.congested??=t;else this.congested=void 0;let R=this.congested!==void 0&&t-this.congested>=4000;if(u>=0.03||b!==void 0&&b<this.bitrate||R){if(this.stable=void 0,t-this.changed<(u>=0.03?2000:4000))return;this.bitrate=Math.max(this.profile.min_bitrate_bps,Math.min(R?Math.floor(this.bitrate*0.75/1000)*1000:this.bitrate-4000,b??this.bitrate)),this.changed=t,this.reason=u>=0.03?"loss":R?"sustained_upload_delay":"bandwidth"}else if(u<0.01&&(!s||S<0.04)&&!(h.sender_send_delay_interval_ms>=20)){if(this.stable??=t,t-this.stable>=1e4&&t-this.changed>=1e4)this.bitrate=Math.min(this.profile.max_bitrate_bps,this.bitrate+2000),this.stable=this.changed=t,this.reason="stable_recovery"}else this.stable=void 0}}class q0{options;target;bitrate=32000;get uploadControl(){return"standard"}get uploadReason(){return"standard"}stable;last=-1/0;initial;maximum;constructor(h={}){this.options=h;this.initial=h.playbackTargetMs??60,this.target=this.initial,this.maximum=Math.max(this.initial,h.playbackMaxMs??160)}observe(h,t=performance.now()){if(t<this.last)this.last=-1/0,this.stable=void 0;if(t-this.last<2000)return;this.last=t;let m=h.receiver_jitter===void 0?void 0:h.receiver_jitter*1000,_=h.receiver_concealed_delta_ms??0;if(this.options.playbackAdaptive!==!1){let u=Math.min(this.maximum,Math.max(this.initial,this.initial+Math.ceil(Math.max(0,(m??0)-10)*2/20)*20,_>0?this.target+20:this.initial));if(u>this.target)this.target=Math.min(u,this.target+20),this.stable=void 0;else if(m!==void 0&&_===0&&m<10){if(this.stable??=t,t-this.stable>=1e4)this.target=Math.max(this.initial,this.target-10),this.stable=t}else this.stable=void 0}let{pair_availableOutgoingBitrate:M,remote_receiver_fractionLost:f}=h;if(M!==void 0&&M>0&&M<64000)this.bitrate=Math.max(16000,Math.min(32000,Math.floor((M-32000)*0.8/1000)*1000));else if(f!==void 0&&f>=0.03)this.bitrate=Math.max(16000,this.bitrate-4000);else if(f!==void 0&&f<0.01)this.bitrate=Math.min(32000,this.bitrate+2000)}}class b0{tokens=1100;at;rate=640;observeAvailableBitrate(h){this.rate=h!==void 0&&h>=96000?2000:640}consume(h,t=performance.now()){if(!Number.isFinite(h)||!Number.isFinite(t)||h<0||h>1100)return!1;if(this.at!==void 0)this.tokens=Math.min(1100,this.tokens+Math.max(0,t-this.at)*this.rate/1000);if(this.at=t,this.tokens<h)return!1;return this.tokens-=h,!0}}class J0 extends q0{get uploadControl(){return this.guarded?"guarded":"standard"}get uploadReason(){return this.guarded?.reason??"standard"}minimumRate;maximumRate;guarded;constructor(h){let t=Ch(h.networkProfile);super({...h,playbackTargetMs:t.playback_target_ms,playbackMaxMs:t.playback_max_ms});if(t.upload_bitrate_control==="guarded")this.guarded=new s0(t);this.bitrate=t.initial_bitrate_bps,this.minimumRate=t.min_bitrate_bps,this.maximumRate=t.max_bitrate_bps}observe(h,t=performance.now()){if(super.observe(h,t),this.guarded)this.guarded.observe(h,t),this.bitrate=this.guarded.bitrate;this.bitrate=Math.max(this.minimumRate,Math.min(this.maximumRate,this.bitrate))}}function Hh(h={}){return h.networkProfile?new J0(h):new q0(h)}class Bh{profile;uploadBadAt;path;badSince;last;lastAttempt=-1/0;baselineRTT;attempts=0;constructor(h=S0){this.profile=h}observe(h,t,m){if(!m||!this.profile.quality_warnings||this.last!==void 0&&(t<=this.last||t-this.last>3000)){this.badSince=void 0,this.uploadBadAt=void 0,this.baselineRTT=void 0,this.last=t;return}if(this.last=t,this.path!==h.path_revision)this.path=h.path_revision,this.baselineRTT=void 0,this.badSince=void 0,this.uploadBadAt=void 0;let _=h.pair_currentRoundTripTime;if(_!==void 0&&_>0&&_<5)this.baselineRTT=Math.min(this.baselineRTT??_,_);let M=_!==void 0&&this.baselineRTT!==void 0&&_-this.baselineRTT>=0.08,f=h.receiver_loss_delta??0,u=h.receiver_loss_window_ms,y=u!==void 0&&u>=500&&u<=2500&&f>=3,s=h.receiver_concealed_delta_ms??0,S=h.receiver_jitter??0;if(h.remote_receiver_report_updated===1)this.uploadBadAt=h.remote_receiver_report_age_ms<=5000&&h.sender_packets_delta>0&&h.remote_receiver_fractionLost>=0.03?t:void 0;if(!(this.uploadBadAt!==void 0&&t-this.uploadBadAt<=5000||y&&(M||s>=80)||s>=80&&S>=0.03)){this.badSince=void 0;return}if(this.badSince??=t,this.attempts>=2||t-this.badSince<this.profile.degraded_seconds*1000||t-this.lastAttempt<this.profile.warning_cooldown_seconds*1000)return;return this.attempts++,this.lastAttempt=t,this.badSince=void 0,"degraded"}}function nh(h){if(h===void 0)return"websocket";if(!["websocket","webrtc","auto"].includes(h))throw RangeError("Unsupported softphone media transport");return h}function g0(h,t,m={}){if(t!==void 0&&typeof t!=="boolean")throw RangeError("webrtcFec must be boolean");let _=new URL(h);if(B0(_,m),_.searchParams.set("transport","webrtc"),_.searchParams.delete("webrtc_fec"),t!==void 0)_.searchParams.set("webrtc_fec",String(t));return _.toString()}var i=(h)=>{try{h?.()}catch{}},bh=(h,t=1000000000000)=>typeof h==="number"&&Number.isFinite(h)?Math.max(0,Math.min(h,t)):0;function yt(h){return h===!0?"relay_configured":h===!1?"no_relay":"unreported"}function kh(h){let t=_h(h);return["turn_relay_unavailable","turn_client_update_required","turn_relay_required"].includes(h?.code??t.code)||t.denied||t.code==="media_session_changed"||t.code==="call_ended"||t.code==="call_not_attachable"||t.expired||["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes(h?.name)}function A0(h,t){let m=0,_=0,M=0,f=0,u=0,y=0,s=0,S=0,q=0,b=null,A,g,R,P={},F={},a={},C=(x,G)=>typeof x?.[G]==="number"&&Number.isFinite(x[G])?Math.max(0,x[G]):void 0,E=(x,G,U)=>{for(let O of U){let V=C(x,O);if(V!==void 0)F[G+O]=V}},B=(x,G,U,O,V)=>{let L=C(x,U),N=C(x,O),X=t?.counters?.[G];if(L!==void 0&&N!==void 0){if(P[G]??={},P[G][U]=L,P[G][O]=N,C(x,"ssrc")!==void 0)P[G].ssrc=x.ssrc;if(X&&X.ssrc===C(x,"ssrc")&&L>=X[U]&&N>X[O])F[V]=(L-X[U])/(N-X[O])*1000}},D=(x,G,U)=>{let O=C(x,"packetsLost"),V=C(x,"ssrc"),L=C(x,"timestamp"),N=t?.counters?.[G];if(V!==void 0)F[U+"ssrc"]=V;if(O===void 0||L===void 0)return;if(P[G]??={},P[G].packetsLost=O,P[G].at=L,V!==void 0)P[G].ssrc=V;if(N&&N.ssrc===V&&O>=N.packetsLost&&L>N.at)F[U+"loss_delta"]=(F[U+"loss_delta"]??0)+(O-N.packetsLost),F[U+"loss_window_ms"]=L-N.at},$=(x,G,U)=>{let O=C(x,"concealedSamples"),V=C(x,"silentConcealedSamples"),L=C(x,"timestamp"),N=C(x,"ssrc"),X=t?.counters?.[G];if(O===void 0||L===void 0)return;if(P[G]??={},Object.assign(P[G],{concealedSamples:O,at:L}),N!==void 0)P[G].ssrc=N;if(V!==void 0)P[G].silentConcealedSamples=V;if(X&&X.ssrc===N&&O>=X.concealedSamples&&L>X.at){if(F.receiver_concealed_delta_ms=(F.receiver_concealed_delta_ms??0)+(O-X.concealedSamples)*1000/U,F.receiver_concealed_window_ms=L-X.at,V!==void 0&&V>=X.silentConcealedSamples)F.receiver_silent_concealed_delta_ms=(F.receiver_silent_concealed_delta_ms??0)+(V-X.silentConcealedSamples)*1000/U}},Q,H=!1,o=!1,gh=0;h.forEach((x,G)=>{if(q=Math.max(q,bh(x.timestamp,1000000000000000)),(x.kind??x.mediaType??h.get(x.localId)?.kind)==="audio"&&gh++<32){if(x.type==="outbound-rtp"){m+=bh(x.bytesSent),E(x,"sender_",["bytesSent","packetsSent","headerBytesSent","retransmittedPacketsSent","retransmittedBytesSent","nackCount","targetBitrate"]),B(x,G,"totalPacketSendDelay","packetsSent","sender_send_delay_interval_ms");let O=C(x,"packetsSent"),V=t?.counters?.[G];if(P[G]??={},O!==void 0)P[G].packetsSent=O;if(V&&O!==void 0&&O>=V.packetsSent&&x.timestamp>V.at)F.sender_packets_delta=O-V.packetsSent,F.sender_window_ms=x.timestamp-V.at}if(x.type==="inbound-rtp"){D(x,G,"receiver_"),_+=bh(x.bytesReceived),M+=bh(x.packetsLost),f=Math.max(f,bh(x.jitter)*1000);let O=h.get(x.codecId),V=C(O,"clockRate")??48000;if($(x,G,V),o||=C(x,"concealedSamples")!==void 0,u+=bh(x.concealedSamples)*1000/V,y+=bh(x.packetsDiscarded),s+=bh(x.jitterBufferDelay),S+=bh(x.jitterBufferEmittedCount),E(x,"receiver_",["bytesReceived","packetsReceived","packetsLost","jitter","packetsDiscarded","concealedSamples","silentConcealedSamples","concealmentEvents","insertedSamplesForDeceleration","removedSamplesForAcceleration","totalSamplesReceived","audioLevel","totalAudioEnergy","totalSamplesDuration","nackCount","fecPacketsReceived","fecPacketsDiscarded"]),B(x,G,"jitterBufferDelay","jitterBufferEmittedCount","receiver_jitter_buffer_interval_ms"),B(x,G,"jitterBufferTargetDelay","jitterBufferEmittedCount","receiver_jitter_target_interval_ms"),B(x,G,"jitterBufferMinimumDelay","jitterBufferEmittedCount","receiver_jitter_minimum_interval_ms"),E(x,"receiver_",["totalProcessingDelay","totalSamplesDecoded"]),a.receiver_processing=C(x,"totalSamplesDecoded")===void 0?"unavailable":"per_decoded_sample",B(x,G,"totalProcessingDelay","totalSamplesDecoded","receiver_processing_interval_ms"),C(x,"lastPacketReceivedTimestamp")!==void 0)F.receiver_last_packet_age_ms=Math.max(0,x.timestamp-x.lastPacketReceivedTimestamp);if(O){if(a.codec=O.mimeType,F.codec_clock_rate=V,C(O,"channels")!==void 0)F.codec_channels=O.channels}}if(x.type==="remote-inbound-rtp"){let O=C(x,"timestamp"),V=t?.counters?.[G];if(Q=O,H=O!==void 0&&(!V||V.ssrc!==C(x,"ssrc")||O>V.feedbackAt),P[G]??={},O!==void 0)P[G].feedbackAt=O;E(x,"remote_receiver_",["packetsLost","fractionLost","jitter","roundTripTime","totalRoundTripTime","roundTripTimeMeasurements"]),D(x,G,"remote_receiver_")}if(x.type==="remote-outbound-rtp")E(x,"remote_sender_",["packetsSent","bytesSent"]);let U=x.type==="outbound-rtp"?"bytesSent":x.type==="inbound-rtp"?"bytesReceived":void 0;if(U&&C(x,U)!==void 0)P[G]??={},P[G][U]=x[U],P[G].at=x.timestamp}if(x.type==="transport"&&x.selectedCandidatePairId){A=x.selectedCandidatePairId,a.dtls=x.dtlsState;let U=h.get(A);if(U){b=C(U,"currentRoundTripTime")!==void 0?U.currentRoundTripTime*1000:null,E(U,"pair_",["availableOutgoingBitrate","availableIncomingBitrate","currentRoundTripTime","totalRoundTripTime","bytesSent","bytesReceived","requestsSent","requestsReceived","responsesSent","responsesReceived","consentRequestsSent"]),a.pair=U.state;let O=h.get(U.localCandidateId),V=h.get(U.remoteCandidateId);if(O)g=O.protocol,R=O.candidateType,a.protocol=g,a.local_candidate=R,a.relay_protocol=O.relayProtocol;if(V)a.remote_candidate=V.candidateType}}});let yh=(x,G)=>{if(t?.counters){let O=0;for(let[V,L]of Object.entries(P)){let N=t.counters[V];if(N&&L[x]>=N[x]&&L.at>N.at)O+=(L[x]-N[x])*8000/(L.at-N.at)}return O}let U=t&&q>t.at?(q-t.at)/1000:0;return U?Math.max(0,G-(x==="bytesSent"?t.sent:t.received))*8/U:0};if(Q!==void 0)F.remote_receiver_report_age_ms=Math.max(0,q-Q),F.remote_receiver_report_updated=H?1:0;let Fh=(t?.pathRevision??0)+(A&&A!==t?.pairID?1:0);F.path_revision=Fh;let e=yh("bytesSent",m),Qh=yh("bytesReceived",_);return{previous:{at:q,sent:m,received:_,counters:P,pairID:A,pathRevision:Fh},rtt:b,queueMs:F.receiver_jitter_buffer_interval_ms??0,metrics:_0({...F,send_bitrate_bps:e,receive_bitrate_bps:Qh}),states:M0(a),webrtc:{relayProtocol:a.relay_protocol,protocol:g,candidateType:R,sendBitrateBps:e,receiveBitrateBps:Qh,packetsLost:M,jitterMs:f,concealedMs:o?u:void 0,packetsDiscarded:y,jitterBufferMs:S>0?s/S*1000:0,jitterBufferIntervalMs:F.receiver_jitter_buffer_interval_ms}}}function St(h,t=new Date().toISOString()){let m=[];for(let[_,M]of[["receiver_","carrier_to_operator"],["remote_receiver_","operator_to_carrier"]]){let f=h[_+"loss_delta"];if(f>0)m.push({timestamp:t,direction:M,reason:"webrtc_packet_loss",duration_ms:0,packet_count:f,ssrc:h[_+"ssrc"],window_ms:h[_+"loss_window_ms"]})}if(h.receiver_concealed_delta_ms>0)m.push({timestamp:t,direction:"carrier_to_operator",reason:"webrtc_concealment",duration_ms:Math.round(h.receiver_concealed_delta_ms),ssrc:h.receiver_ssrc,window_ms:h.receiver_concealed_window_ms});return m}class x0{callbacks;ice;stopped=!1;generation=0;socket;pc;stream;context;capture;whisper;destination;speaker;remoteAudio;speakerMeter;carrierStalled=!1;deliveryDegraded=!1;ready=!1;answered=!1;mediaPathReady;mediaRevision=0;reportedLive=!1;peer=!1;muted=!1;whisperEpoch=null;whisperBase=null;whisperResampler=new Ph;timer;retry;cancelSetup;recovering=!1;recoveryGeneration=0;recoveryContext;recoveryExpiry;recoveryRequest;ringback;options;workletURL;events=[];previous;sampling=new WeakSet;transportTelemetry=new Th("webrtc");transportSender=new Oh((h)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[h]}})},(h,t=0)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let m=h;if(m?.diagnostics?.webrtc){let _=m.diagnostics.webrtc;return this.sendTelemetry({...m,diagnostics:{...m.diagnostics,webrtc:{..._,jitterBufferSampleAgeMs:(_.jitterBufferSampleAgeMs??0)+t}}})}return this.sendTelemetry(h)});controlBudget=new b0;quality;pathHealth=new Bh;playbackReceiver;uploadSender;applyingBitrate=!1;appliedBitrate;statsErrors=0;lastStatsSend=-1/0;lastValidStatsAt=-1/0;clientEpoch=crypto.randomUUID();relayConfigured;nativeCounts;completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};runtime=new Zh((h)=>this.recordSessionEvent(h));diagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:null,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(h){this.callbacks=h}async start(h,t,m,_){if(this.options=t,this.workletURL=m,!m)throw Error("WebRTC requires the shared Telephony audio processor");if(typeof RTCPeerConnection!=="function")throw Error("WebRTC audio is unavailable in this browser");v(t),await this.connect(h,_)}current(h){return!this.stopped&&this.generation===h}guard(h){if(!this.current(h))throw Error("Audio session cancelled")}async connect(h,t){this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+wh.replaceAll(".","_")}),this.runtime.observeEnvironment();let m=++this.generation,_=this.options;for(let g of this.events.slice(-8))this.enqueueRTCEvents({session_events:[g]});for(let g of this.diagnostics.dropEvents.slice(-8))this.enqueueRTCEvents({drop_events:[g]});if(this.ready=this.peer=this.answered=this.reportedLive=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.previous=void 0,this.quality=Hh(_),this.pathHealth=new Bh(_.networkProfile),this.appliedBitrate=void 0,t)this.stream=t.stream,this.context=t.context;let M=t?.stream??await l("microphone_device",(g)=>this.recordSessionEvent(g),()=>navigator.mediaDevices.getUserMedia({audio:qh(_)}));if(!this.current(m))throw M.getTracks().forEach((g)=>g.stop()),Error("Audio session cancelled");this.stream=M;let f=M.getAudioTracks()[0];if(!f)throw Error("No microphone audio track");let u=f.getSettings();this.diagnostics={...this.diagnostics,microphoneSampleRate:u.sampleRate??0,microphoneChannelCount:u.channelCount??1,echoCancellation:typeof u.echoCancellation==="boolean"?u.echoCancellation:null,noiseSuppression:u.noiseSuppression??null,autoGainControl:u.autoGainControl??null,micInputGainDb:_.inputGainDB},f.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),f.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),f.onended=()=>{if(this.current(m))this.disconnected(m,"microphone_ended")},this.context=t?.context??new AudioContext({latencyHint:"interactive"});let y=this.context;if(y.onstatechange=()=>{this.runtime.context(y.state),this.updateLiveState()},this.runtime.context(y.state),y.state!=="running")await l("audio_context",(g)=>this.recordSessionEvent(g),()=>y.resume());if(this.guard(m),_.outputDeviceId&&"setSinkId"in y)await y.setSinkId(_.outputDeviceId);await l("audio_worklet",(g)=>this.recordSessionEvent(g),()=>y.audioWorklet.addModule(this.workletURL)),this.guard(m),this.diagnostics.audioContextRate=y.sampleRate,this.capture=new AudioWorkletNode(y,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:_.inputGainDB,highpassFilter:_.highpassFilter}}),this.destination=y.createMediaStreamDestination(),this.destination.channelCount=1,y.createMediaStreamSource(M).connect(this.capture).connect(this.destination),this.capture.port.onmessage=(g)=>{if(g.data?.type==="capture.stats"){let R=(F)=>F>0?20*Math.log10(F):null;this.diagnostics.micActiveRmsDbfs=R(g.data.active_rms),this.diagnostics.micPeakDbfs=R(g.data.pre_peak),this.diagnostics.micPostPeakDbfs=R(g.data.post_peak),this.diagnostics.micLimiterReductionDb=g.data.limiter_reduction_db??0;let P=0;if(this.speakerMeter){let F=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(F),P=Math.sqrt(F.reduce((a,C)=>a+C*C,0)/F.length)}i(()=>this.callbacks.onLevels?.(bh(g.data.active_rms,1),P))}},this.speaker=y.createGain(),this.speaker.gain.value=_.outputVolume??1,this.speaker.connect(y.destination),this.speakerMeter=y.createAnalyser(),this.speakerMeter.fftSize=256;let s=y.createGain();s.gain.value=0,this.speaker.connect(this.speakerMeter).connect(s).connect(y.destination),this.whisper=new AudioWorkletNode(y,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:v(_)}),this.whisper.connect(this.speaker),this.gate();let S=new WebSocket(g0(h,_.webrtcFec,_));S.binaryType="arraybuffer",this.socket=S;let q=!1,b=!1,A=!1;await l("audio_transport",(g)=>this.recordSessionEvent(g),()=>new Promise((g,R)=>{let P=(C)=>{if(b)return;b=!0,clearTimeout(F),this.cancelSetup=void 0,C?R(A?Object.assign(C,{code:"turn_relay_required"}):C):g()},F=setTimeout(()=>P(Error("WebRTC audio connection timed out")),18000);this.cancelSetup=()=>P(Error("Audio session cancelled"));let a=()=>{if(this.current(m)&&this.ready&&q)this.gate(),P(),this.updateLiveState()};S.onmessage=(C)=>{if(!this.current(m))return;if(C.data instanceof ArrayBuffer){this.playWhisper(C.data);return}if(typeof C.data!=="string")return;let E;try{E=JSON.parse(C.data)}catch{return}if(E.type==="webrtc.error"){P(Object.assign(Error("Required TURN relay is unavailable"),{code:E.code}));return}if(this.ice?.handle(E))return;if(E.type==="webrtc.config"){if(A=E.ice_transport_policy==="relay",E.network_policy)try{let B=Ch(E.network_policy);this.quality=Hh({..._,networkProfile:B}),this.pathHealth=new Bh(B)}catch{P(Error("Invalid network policy"));return}if(this.relayConfigured=typeof E.relay_configured==="boolean"?E.relay_configured:void 0,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"ice_configuration",outcome:yt(E.relay_configured)}),this.pc){P(Error("Repeated WebRTC configuration"));return}(async()=>{let B=new RTCPeerConnection($h(E));this.pc=B,this.ice?.stop(),this.ice=new Lh(($)=>this.send($),()=>this.current(m),($,Q)=>this.recordSessionEvent({...Q,timestamp:new Date().toISOString(),action:"ice_restart",outcome:$}),($)=>{this.relayConfigured=$.iceServers?.some((Q)=>(typeof Q.urls==="string"?[Q.urls]:Q.urls).some((H)=>/^turns?:/.test(H)))??!1}),this.ice.register("main",B),this.destination.stream.getAudioTracks().forEach(($)=>{let Q=B.addTrack($,this.destination.stream);this.uploadSender=Q,this.applyUploadBitrate(Q,this.quality?.bitrate??32000,m)}),B.ontrack=($)=>{if(!this.current(m))return;this.playbackReceiver=$.receiver,this.applyPlaybackTarget();let Q=new MediaStream([$.track]),H=new Audio;H.muted=!0,H.srcObject=Q,this.remoteAudio=H,H.play().catch(()=>i(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction."))),y.createMediaStreamSource(Q).connect(this.speaker)},B.onconnectionstatechange=()=>{if(!this.current(m))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:B.connectionState}),B.connectionState==="connected")q=!0,a();if(B.connectionState==="failed"||B.connectionState==="closed")if(!b)P(Error("WebRTC connection failed"));else this.disconnected(m,"webrtc_"+B.connectionState)};let D=await B.createOffer();this.guard(m),await B.setLocalDescription(D),await new Promise(($,Q)=>{if(B.iceGatheringState==="complete"){$();return}let H=setTimeout(()=>{B.onicegatheringstatechange=null,Q(Error("ICE gathering timed out"))},7000);B.onicegatheringstatechange=()=>{if(B.iceGatheringState==="complete")clearTimeout(H),B.onicegatheringstatechange=null,$()}}),this.guard(m),S.send(JSON.stringify({type:"webrtc.offer",sdp:B.localDescription.sdp,ice_version:1}))})().catch((B)=>P(B instanceof Error?B:Error("WebRTC setup failed")));return}if(E.type==="webrtc.answer"){this.pc?.setRemoteDescription({type:"answer",sdp:E.sdp}).catch(()=>P(Error("Invalid WebRTC answer")));return}if(E.type==="media.ready"||E.type==="media.not_ready"){if(!Number.isSafeInteger(E.revision)||E.revision<=this.mediaRevision)return;this.mediaRevision=E.revision,this.mediaPathReady=E.type==="media.ready",i(()=>this.callbacks.onMediaReadiness?.(E)),this.updateLiveState()}if(E.type==="ready")this.ready=!0,this.send({type:"media.capabilities",version:2,versions:[3],whisper:!0}),a();if(E.type==="peer.connected")this.peer=!0,this.gate(),a();if(E.type==="peer.disconnected")this.peer=!1,this.gate(),this.updateLiveState(),i(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));if(E.type==="call.status")this.answered=["answered","in-progress"].includes(E.status),a(),i(()=>this.callbacks.onCallStatus?.(E));if(E.type==="call.error")if(!b)P(Error(E.detail??"Carrier activation failed"));else this.stop(),i(()=>this.callbacks.onState?.("error",E.detail));if(E.type==="call.ended"||E.type==="session.replaced")if(!b)P(Error(E.type));else this.stop(),i(()=>this.callbacks.onState?.("ended",E.type));if(E.type==="audio.health"){if(this.diagnostics.audioHealth={state:E.state,reason:E.reason,stages:E.stages},i(()=>this.callbacks.onAudioHealth?.(E)),E.state==="audio_degraded"&&!this.deliveryDegraded)this.deliveryDegraded=!0,i(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));else if(this.deliveryDegraded&&E.state==="healthy"&&E.stages?.telephony_to_browser?.state==="healthy"&&y.state==="running")this.deliveryDegraded=!1,i(()=>this.callbacks.onNotice?.("Speech delivery restored."))}if(E.type==="media.delivery"){if(E.state==="stalled")i(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));else if(E.state==="flowing"&&this.carrierStalled)i(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));this.carrierStalled=E.state==="stalled"}if(E.type==="call.notice")i(()=>this.callbacks.onNotice?.(E.detail??E.reason??"Audio delivery notice"));if(E.type==="coach.state")this.whisperEpoch=E.talking&&Number.isInteger(E.epoch)?E.epoch:null,this.whisperBase=null,this.whisper?.port.postMessage({type:"whisper.clear"})},S.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"}),S.onclose=(C)=>{if(!this.current(m))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(C.code),was_clean:C.wasClean}),!b){P(Object.assign(Error("WebRTC signaling unavailable"),C.code===1008?{status:403}:{}));return}if(C.code===1008){this.stop(),i(()=>this.callbacks.onState?.("error","Media authorization ended"));return}this.disconnected(m,"signaling_closed")}})),this.guard(m),this.timer=setInterval(()=>{this.runtime.tick(),this.statistics(m)},1000),await this.statistics(m)}send(h){if(this.socket?.readyState===WebSocket.OPEN&&this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(h))}catch{}}updateLiveState(){let h=this.ready&&this.peer&&this.answered&&this.mediaPathReady!==!1&&this.pc?.connectionState==="connected"&&this.context?.state==="running";if(h===this.reportedLive)return;this.reportedLive=h,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"media_path",outcome:h?"ready":"waiting"}),i(()=>this.callbacks.onState?.(h?"live":"reconnecting",h?void 0:"Connecting carrier audio…"))}gate(){let h=!this.stopped&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!h}),this.destination?.stream.getAudioTracks().forEach((t)=>t.enabled=h)}async statistics(h){if(!this.current(h)||!this.pc||this.sampling.has(this.pc))return;let t=this.pc,m=performance.now();this.sampling.add(t);try{let _=A0(await t.getStats(),this.previous);if(!this.current(h)||this.pc!==t)return;if(this.previous=_.previous,this.lastValidStatsAt=performance.now(),this.nativeCounts={packetsLost:_.webrtc.packetsLost,packetsDiscarded:_.webrtc.packetsDiscarded,concealedMs:_.webrtc.concealedMs},_.webrtc.packetsLost+=this.completedCounts.packetsLost,_.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded,_.webrtc.concealedMs!==void 0)_.webrtc.concealedMs+=this.completedCounts.concealedMs;this.quality?.observe(_.metrics),this.applyPlaybackTarget();let M=this.pc.connectionState==="connected"&&this.context?.state==="running"&&this.peer&&this.answered;if(this.pathHealth.observe(_.metrics,performance.now(),M))this.recordSessionEvent({timestamp:new Date().toISOString(),action:"network_quality",outcome:"sustained_degradation"},!1),i(()=>this.callbacks.onNotice?.("The audio connection is unstable. After this call, compare direct and relay paths in Network settings."));if(this.uploadSender&&this.quality)this.applyUploadBitrate(this.uploadSender,this.quality.bitrate,h);if(_.states.upload_bitrate_control=this.quality?.uploadControl??"standard",_.states.upload_bitrate_reason=this.quality?.uploadReason??"standard",_.metrics.requested_playback_target_ms=this.quality?.target??60,this.appliedBitrate!==void 0)_.metrics.requested_upload_bitrate_bps=this.appliedBitrate;this.controlBudget.observeAvailableBitrate(_.metrics.pair_availableOutgoingBitrate);let f=St(_.metrics);for(let y of f)this.enqueueRTCEvents({drop_events:[y]});this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,...f].slice(-100),this.diagnostics={...this.diagnostics,rttMs:_.rtt,queueMs:_.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,_.queueMs),targetMs:this.quality?.target??this.options?.playbackTargetMs??60,webrtc:_.webrtc,playbackSequenceGaps:_.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};let u=this.stream?.getAudioTracks()[0];this.transportTelemetry.observe({..._.metrics,rtt_ms:_.rtt,queue_ms:_.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:this.socket?.bufferedAmount??0,stats_errors:this.statsErrors,stats_duration_ms:performance.now()-m,main_thread_max_pause_ms:this.runtime.counters.main_thread_max_pause_ms},{..._.states,ice:t.iceConnectionState,connection:"connected",context:this.context?.state,muted:String(this.muted),device_muted:String(u?.muted),track:u?.readyState}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),i(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}catch{if(this.current(h)&&this.pc===t)this.statsErrors++,this.transportTelemetry.observe({stats_errors:this.statsErrors,stats_duration_ms:performance.now()-m},{ice:t.iceConnectionState,context:this.context?.state,muted:String(this.muted)}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),i(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}finally{this.sampling.delete(t)}}applyPlaybackTarget(){let h=this.playbackReceiver;if(!h||!this.quality)return;if("jitterBufferTarget"in h)try{h.jitterBufferTarget=this.quality.target}catch{}}async applyUploadBitrate(h,t,m){if(this.applyingBitrate||this.appliedBitrate===t)return;this.applyingBitrate=!0;try{let _=h.getParameters();if(_.encodings?.length){if(_.encodings[0].maxBitrate=t,await h.setParameters(_),this.current(m)&&this.uploadSender===h)this.appliedBitrate=t}}catch{}finally{this.applyingBitrate=!1}}sendTelemetry(h){if(!this.ready||this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let t=JSON.stringify(h),m=new TextEncoder().encode(t).byteLength+58;if(!this.controlBudget.consume(m))return!1;try{return this.socket.send(t),!0}catch{return!1}}enqueueRTCEvents(h){try{let t={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,...h}};if(new TextEncoder().encode(JSON.stringify(t)).byteLength+58>1100){this.transportSender.skippedAuxiliary++;return}this.transportSender.enqueueAuxiliary(t)}catch{this.transportSender.skippedAuxiliary++}}sendStatistics(){let h=this.stream?.getAudioTracks()[0];if(performance.now()-this.lastStatsSend>=5000)this.lastStatsSend=performance.now(),this.transportSender.enqueueReport({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,client_version:wh,relay_configured:this.relayConfigured,media_transport:"webrtc",codec:"opus",webrtc:{...Object.fromEntries(Object.entries(this.diagnostics.webrtc??{}).map(([t,m])=>[t,typeof m==="number"?Math.round(m*10)/10:m])),jitterBufferSampleAgeMs:Number.isFinite(this.lastValidStatsAt)?Math.max(0,performance.now()-this.lastValidStatsAt):void 0},connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:h?.readyState,microphone_device_muted:h?.muted,rtt_ms:this.diagnostics.rttMs===null?null:Math.round(this.diagnostics.rttMs),playback_queue_ms:Math.round(this.diagnostics.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:this.diagnostics.webrtc?.packetsLost}}),this.transportSender.enqueue(this.transportTelemetry.drain()),this.enqueueRTCEvents({timing:{runtime:this.runtime.counters}})}playWhisper(h){if(h.byteLength<17||h.byteLength>176||this.whisperEpoch===null||!this.context)return;let t=new DataView(h);if(t.getUint32(0,!0)!==827805761||t.getUint32(4,!0)!==this.whisperEpoch)return;let m=t.getFloat64(8,!0);if(!Number.isFinite(m))return;let _=performance.timeOrigin+performance.now()-m;this.whisperBase=this.whisperBase===null?_:Math.min(this.whisperBase,_);let M=Math.max(0,_-this.whisperBase);if(M>200)return;let f=new Float32Array(h.byteLength-16);for(let y=0;y<f.length;y++){let s=~t.getUint8(y+16)&255,S=((s&15)<<3)+132<<(s>>4&7);f[y]=(s&128?132-S:S-132)/32768}let u=this.context.sampleRate===8000?f:this.whisperResampler.process(f,8000,this.context.sampleRate);this.whisper?.port.postMessage({type:"whisper",frame:u,received_audio_ms:this.context.currentTime*1000-M},[u.buffer])}disconnected(h,t){if(!this.current(h)||this.recovering)return;this.recovering=!0;let m=Gh("automatic_retry");this.recoveryContext=m,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"started",code:t,...m}),this.cleanup(),i(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));let _=++this.recoveryGeneration,M=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===_;this.recoveryExpiry=setTimeout(()=>{if(!M())return;this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"timed_out",...this.recoveryContext}),++this.recoveryGeneration,this.recovering=!1,clearTimeout(this.retry),this.cancelSetup?.(),this.recoveryRequest?.abort(),this.cleanup(),i(()=>this.callbacks.onState?.("error","Network unavailable; waiting to restore audio to the existing call.",{recoverable:!0}))},30000);let f=async()=>{if(!M())return;let u={...m,attempt_id:ah("attempt")};this.recoveryContext=u,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"attempt_started",code:t,...u});try{if(!this.callbacks.refreshMediaURL)throw Error("Fresh media authorization is required");this.recoveryRequest=new AbortController;let y=await this.callbacks.refreshMediaURL(u,this.recoveryRequest.signal);if(!M())return;if(await this.connect(y),!M())return;if(this.pc?.connectionState!=="connected")throw Error("WebRTC disconnected during recovery");clearTimeout(this.recoveryExpiry),this.recovering=!1,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected",...u})}catch(y){if(!M())return;this.cleanup();let s=_h(y);if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:kh(y)?"revoked":"attempt_failed",...u,status:s.status,code:s.code}),kh(y)){clearTimeout(this.recoveryExpiry),this.recovering=!1,i(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return}this.retry=setTimeout(()=>void f(),1000)}};f()}recordSessionEvent(h,t=!0){try{h={...this.callbacks.sessionDiagnostics?.(),...h}}catch{}if(h=Mh(h),this.events=[...this.events,h].slice(-100),t)this.enqueueRTCEvents({session_events:[h]});i(()=>this.callbacks.onSessionEvent?.(h))}setMuted(h){if(this.muted!==h)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:h?"muted":"unmuted"});if(this.muted=h,this.gate(),h)this.send({type:"interrupt"})}sendDTMF(h){if(!/^[0-9*#]+$/.test(h))throw Error("Invalid DTMF digits");this.send({type:"dtmf",digits:h})}setOutputVolume(h){if(!Number.isFinite(h)||h<0||h>1)throw RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=h;if(this.options)this.options.outputVolume=h}startRingback(h){if(this.stopRingback(),this.context&&this.speaker)this.ringback=lh(this.context,this.speaker,ch(h))}stopRingback(){this.ringback?.(),this.ringback=void 0}cleanup(){if(this.runtime.stopEnvironment(),this.transportSender.stop(),this.nativeCounts){for(let _ of["packetsLost","packetsDiscarded","concealedMs"])this.completedCounts[_]+=this.nativeCounts[_]??0;this.nativeCounts=void 0}++this.generation,this.ready=this.peer=!1,this.stopRingback(),clearInterval(this.timer),this.timer=void 0;let h=this.socket;if(this.socket=void 0,h){if(h.readyState===WebSocket.OPEN)try{h.send(JSON.stringify({type:"media.shutdown",reason:this.stopped?"session_cleanup":"audio_error"}))}catch{}h.onclose=h.onmessage=h.onerror=null,h.close(1000,"client_shutdown")}this.ice?.stop(),this.ice=void 0,this.playbackReceiver=void 0,this.uploadSender=void 0;let t=this.pc;if(this.pc=void 0,t)t.onconnectionstatechange=t.ontrack=null,t.close();if(this.remoteAudio)this.remoteAudio.pause(),this.remoteAudio.srcObject=null,this.remoteAudio=void 0;this.stream?.getTracks().forEach((_)=>{_.onended=null,_.stop()}),this.stream=void 0,this.destination?.stream.getTracks().forEach((_)=>_.stop()),this.destination=void 0,this.capture?.disconnect(),this.capture=void 0,this.whisper?.disconnect(),this.whisper=void 0,this.whisperEpoch=null,this.whisperBase=null,this.whisperResampler=new Ph;let m=this.context;if(this.context=void 0,m)m.onstatechange=null,m.close().catch(()=>{});this.speaker=void 0,this.speakerMeter=void 0}stop(){if(this.stopped)return;if(this.recovering)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"cancelled",...this.recoveryContext});this.stopped=!0,this.recoveryRequest?.abort(),++this.recoveryGeneration,clearTimeout(this.recoveryExpiry),clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup()}}function L0(h,t,m){let _,M=!1,f=!1,u=[],y;return{async start(s,S){let q=nh(S.mediaTransport),b=async(A)=>{if(M)throw Error("Audio session cancelled");let g=A();_=g,g.setMuted(f);let R;try{for(let P of u.splice(0))try{g.recordSessionEvent?.(P)}catch{}if(M)throw Error("Audio session cancelled");if(await Promise.race([g.start(s,S),new Promise((P,F)=>{if(y=()=>F(Error("Audio session cancelled")),R=setTimeout(()=>F(Error("Audio transport setup timed out")),18000),M)y()})]),M)throw Error("Audio session cancelled")}catch(P){if(g.stop(),_===g)_=void 0;throw P}finally{clearTimeout(R),y=void 0}};if(q==="websocket"){await b(t);return}try{await b(m)}catch(A){if(q!=="auto"||M||S.iceTransportPolicy==="relay"||kh(A))throw A;i(()=>h.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"})),await b(t)}},stop(){M=!0,y?.(),u.splice(0),_?.stop(),_=void 0},setMuted(s){f=s,_?.setMuted(s)},setOutputVolume(s){_?.setOutputVolume(s)},sendDTMF(s){_?.sendDTMF(s)},recordSessionEvent(s){if(_)_.recordSessionEvent?.(s);else if(!M){if(u.push(Mh(s)),u.length>20)u.shift()}},startRingback(s){_?.startRingback?.(s)},stopRingback(){_?.stopRingback?.()}}}var st=1e4;class P0{ready;context;stream;cancelled=!1;completed=!1;cancelWait;constructor(h,t,m){this.ready=this.prepare(h,t,m)}async prepare(h,t,m){let _;try{try{this.context=new AudioContext(t)}catch{this.context=new AudioContext({latencyHint:"interactive"})}let M=this.context,f=l("audio_context_preflight",m,()=>M.resume()),u=l("microphone_preflight",m,async()=>{let y=await navigator.mediaDevices.getUserMedia({audio:h});if(this.cancelled)throw y.getTracks().forEach((s)=>s.stop()),Error("Audio preparation cancelled");if(this.stream=y,!y.getAudioTracks().some((s)=>s.readyState==="live"))throw Error("Microphone is unavailable")});if(await Promise.race([Promise.all([f,u]),new Promise((y,s)=>{this.cancelWait=()=>s(Error("Audio preparation cancelled")),_=setTimeout(()=>s(Error("Microphone or playback setup timed out. Check browser permissions and try again.")),st)})]),this.cancelled||M.state!=="running")throw Error("Browser audio is paused. Click Answer again to enable audio.");this.completed=!0}catch(M){throw this.cancel(),M}finally{clearTimeout(_),this.cancelWait=void 0}}take(){if(!this.completed||this.cancelled||!this.stream||!this.context)throw Error("Audio preparation is unavailable");if(!this.stream.getAudioTracks().some((t)=>t.readyState==="live"))throw Error("Microphone disconnected before audio setup. Select a microphone and try again.");let h={stream:this.stream,context:this.context};return this.stream=void 0,this.context=void 0,h}cancel(){this.cancelled=!0,this.cancelWait?.(),this.stream?.getTracks().forEach((t)=>t.stop()),this.stream=void 0;let h=this.context;if(this.context=void 0,h)h.close().catch(()=>{})}}async function Z0(){return(await navigator.mediaDevices.enumerateDevices()).filter((t)=>t.kind==="audioinput").map((t,m)=>({deviceId:t.deviceId,label:t.label||`Microphone ${m+1}`}))}function K0(h,t){let m=new f0(h,!1),_,M=!1,f,u=()=>{return M=!0,f??=(async()=>{try{await m.cancel()}finally{_?.dispose(),_=void 0}})()};return{async start(y={}){if(M)throw Error("Microphone preview is already used");M=!0;try{if(_=t?await Eh(t,!0):ih(!0),f)throw _.dispose(),_=void 0,Error("Microphone preview was cancelled");await m.start(_.urls[0],{...fh,...y})}catch(s){throw await u(),s}},stop:u}}function qt(h,t){return{create(m){let _=new u0(m),M,f=!1,u=()=>{f=!0;try{_.stop()}finally{M?.dispose(),M=void 0}};return{async start(y,s){try{if(M=await l("audio_assets",(S)=>_.recordSessionEvent(S),()=>h?Eh(h):Promise.resolve(ih())),f)throw Error("Audio session was cancelled");await _.start(y,M.urls[0],M.urls[1],s,t())}catch(S){throw u(),S}},stop:u,recordSessionEvent:(y)=>_.recordSessionEvent(y),setMuted:(y)=>_.setMuted(y),sendDTMF:(y)=>_.sendDTMF(y),setOutputVolume:(y)=>_.setOutputVolume(y),startRingback:(y)=>_.startRingback(y),stopRingback:()=>_.stopRingback()}}}}function D0(h){let t,m=()=>{t?.cancel(),t=void 0},_=()=>{if(!t)return;let f=t.take();return t=void 0,f},M=qt(h,_);return{async preflight(f,u){m();let y=new P0(qh(f),f.mediaTransport==="webrtc"||f.mediaTransport==="auto"?{latencyHint:"interactive"}:{sampleRate:24000,latencyHint:"interactive"},u);t=y;try{await Promise.all([y.ready,T0(h,f,u)])}catch(s){if(y.cancel(),t===y)t=void 0;throw s}},cancelPreparation:m,prepare:(f,u)=>T0(h,f,u),create(f){return L0(f,()=>M.create(f),()=>{let u=new x0(f),y,s=!1;return{async start(S,q){try{if(y=await l("audio_assets",(b)=>u.recordSessionEvent(b),()=>h?Eh(h,!0):Promise.resolve(ih(!0))),s)throw Error("Audio session cancelled");await u.start(S,q,y.urls[0],_())}catch(b){throw u.stop(),y?.dispose(),y=void 0,b}},stop(){s=!0,u.stop(),y?.dispose(),y=void 0},setMuted:(S)=>u.setMuted(S),sendDTMF:(S)=>u.sendDTMF(S),setOutputVolume:(S)=>u.setOutputVolume(S),recordSessionEvent:(S)=>u.recordSessionEvent(S),startRingback:(S)=>u.startRingback(S),stopRingback:()=>u.stopRingback()}})}}}async function T0(h,t,m){if(!h||typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin)return;await l("audio_assets_prepare",m,async()=>{(await Eh(h,t.mediaTransport==="webrtc"||t.mediaTransport==="auto")).dispose()})}function i0(h,t){return{create(m){let _={...fh,...t},M=v(_),f=Hh(_),u=new Bh(_.networkProfile),y=[],s,S,q,b,A=!1,g=!1,R=!1,P=!1,F=!1,a=!1,C=0,E,B,D,$,Q,H,o=!1,gh,yh,Fh=_.outputVolume??1,e={},Qh=32000,x,G=[],U,O,V,L,N=[],X=(W)=>{if(s?.readyState===WebSocket.OPEN&&s.bufferedAmount<65536)s.send(JSON.stringify(W));else throw Error("Listener connection unavailable")},T=()=>{if(A)throw Error("Listening cancelled")},k=()=>{let W=++C;if(F=a=!1,U?.(W,Error("Coaching cancelled")),L?.(),O=void 0,gh)clearInterval(gh);if(gh=void 0,x?.remove("coach"),Q)Q.onconnectionstatechange=null,Q.close(),Q=void 0;if(B?.disconnect(),B=void 0,D?.disconnect(),D=void 0,$?.disconnect(),$=void 0,E?.getTracks().forEach((w)=>{w.onended=null,w.stop()}),E=void 0,P&&g&&s?.readyState===WebSocket.OPEN)try{X({type:"coach.stop",generation:W})}catch{}m.onTalking?.(!1)},n=()=>k(),Ah=()=>{if(document.visibilityState!=="visible")k()},Uh=()=>{if(A)return;if(k(),A=!0,x?.stop(),x=void 0,V?.(),window.removeEventListener("blur",n),document.removeEventListener("visibilitychange",Ah),yh)clearInterval(yh);if(yh=void 0,s)s.onmessage=s.onclose=s.onerror=null,s.close(1000,"listener_shutdown");if(S)S.ontrack=S.onconnectionstatechange=null,S.close();for(let W of N)W.pause(),W.srcObject=null;if(b?.disconnect(),q)q.onstatechange=null,q.close().catch(()=>{});H?.dispose(),H=void 0},J=(W)=>{if(!A)Uh(),m.onClose(W)},hh=async(W,w)=>{if(await W.setLocalDescription(await W.createOffer()),await new Promise((Y,z)=>{if(W.iceGatheringState==="complete"){Y();return}let d=setTimeout(()=>{W.onicegatheringstatechange=null,z(Error("ICE gathering timed out"))},7000);W.onicegatheringstatechange=()=>{if(W.iceGatheringState==="complete"||!w())clearTimeout(d),W.onicegatheringstatechange=null,w()?Y():z(Error("Coaching cancelled"))}}),!w())throw Error("Listening cancelled");return W.localDescription.sdp};return{async start(W,w){if(P=w?.coaching===!0,T(),typeof RTCPeerConnection!=="function")throw Error("WebRTC audio unavailable");try{if(q=new AudioContext({latencyHint:"interactive"}),await q.resume(),T(),_.outputDeviceId&&"setSinkId"in q)await q.setSinkId(_.outputDeviceId);if(T(),b=q.createGain(),b.gain.value=Fh,b.connect(q.destination),q.onstatechange=()=>{if(q?.state==="suspended"||q?.state==="interrupted")J("listener_audio_paused")},await new Promise((d,p)=>{let I=!1,th=!1,Rh=!1,K=(xh)=>{if(I)return;I=!0,clearTimeout(oh),V=void 0,xh?p(Rh?Object.assign(xh,{code:"turn_relay_required"}):xh):d()},oh=setTimeout(()=>K(Error("WebRTC listener timed out")),18000),Ih=()=>{if(R&&th&&!A&&!g)g=!0,K(),m.onReady("webrtc")};V=()=>K(Error("Listening cancelled")),s=new WebSocket(g0(W,_.webrtcFec,_)),s.onmessage=({data:xh})=>{if(A)return;if(typeof xh!=="string"){K(Error("Invalid WebRTC listener frame")),J("listener_protocol_error");return}let Z;try{Z=JSON.parse(xh)}catch{K(Error("Invalid listener control")),J("listener_protocol_error");return}if(Z.type==="webrtc.error"){K(Object.assign(Error("Required TURN relay is unavailable"),{code:Z.code}));return}if(x?.handle(Z))return;if(Z.type==="webrtc.config"){if(Rh=Z.ice_transport_policy==="relay",Z.network_policy)try{let j=Ch(Z.network_policy);f=Hh({..._,networkProfile:j}),u=new Bh(j)}catch{K(Error("Invalid network policy"));return}if(S||Z.listener!==!0){K(Error("Invalid WebRTC listener configuration"));return}try{e=$h(Z)}catch(j){K(j);return}Qh=Z.bitrate??32000,S=new RTCPeerConnection(e);let mh=S;x=new Lh(X,()=>!A,(j,Yh)=>{if(G.push(Mh({...Yh,timestamp:new Date().toISOString(),action:"ice_restart",outcome:j})),G.length>50)G.shift()},(j)=>{e=j}),x.register("main",mh);for(let j=0;j<2;j++)mh.addTransceiver("audio",{direction:"recvonly"});mh.ontrack=(j)=>{if(A||!q||!b)return;y.push(j.receiver);let Yh=mh.getTransceivers().findIndex((Wh)=>Wh.receiver===j.receiver);if(Yh<0||Yh>1){J("listener_protocol_error");return}if("jitterBufferTarget"in j.receiver)try{j.receiver.jitterBufferTarget=M.initialTargetMs}catch{}let a0=new MediaStream([j.track]),jh=new Audio;jh.muted=!0,jh.srcObject=a0,N.push(jh),jh.play().catch(()=>{});let G0=q.createMediaStreamSource(a0);if(t.stereo){let Wh=q.createStereoPanner();Wh.pan.value=Yh===0?-1:1,G0.connect(Wh).connect(b)}else{let Wh=q.createGain();Wh.gain.value=0.5,G0.connect(Wh).connect(b)}},mh.onconnectionstatechange=()=>{if(A)return;if(mh.connectionState==="connected")R=!0,Ih();if(mh.connectionState==="failed"||mh.connectionState==="closed"){if(K(Error("WebRTC listener connection failed")),I&&g)J("listener_network_error")}},hh(mh,()=>!A).then((j)=>X({type:"webrtc.offer",sdp:j,ice_version:1})).catch((j)=>K(j));return}if(Z.type==="webrtc.answer"){S?.setRemoteDescription({type:"answer",sdp:Z.sdp}).catch((mh)=>K(mh));return}if(Z.type==="listener.ready"){if(P&&Z.coaching!==!0){K(Error("Coaching not authorized"));return}th=!0,Ih();return}if(Z.type==="coach.started"){U?.(Z.generation);return}if(Z.type==="coach.rejected"){U?.(C,Error(Z.detail||"Coaching unavailable"));return}if(Z.type==="coach.stopped"&&(Z.generation===void 0||Z.generation===C)){k();return}if(Z.type==="coach.webrtc.answer"){O?.(Z.sdp,Z.generation);return}},s.onerror=()=>{if(K(Error("Listener connection failed")),g)J("listener_network_error")},s.onclose=(xh)=>{if(K(Error(xh.reason||"Listener disconnected")),g)J(xh.reason||"listener_disconnected")}}),T(),P)window.addEventListener("blur",n),document.addEventListener("visibilitychange",Ah);let Y,z=!1;yh=setInterval(()=>{if(A||!S||z)return;z=!0,S.getStats().then((d)=>{if(A)return;let p=A0(d,Y);if(Y=p.previous,f.observe({...p.metrics,receiver_jitter:p.webrtc.jitterMs/1000}),u.observe(p.metrics,performance.now(),S?.connectionState==="connected"&&q?.state==="running")){if(G.push({timestamp:new Date().toISOString(),action:"network_quality",outcome:"sustained_degradation"}),G.length>50)G.shift()}for(let I of y)if("jitterBufferTarget"in I)try{I.jitterBufferTarget=f.target}catch{}m.onDiagnostics?.({session_events:G.slice(),media_transport:"webrtc",dropped_ms:[],played_ms:[],max_queue_ms:p.queueMs,max_late_ms:0,sequence_gaps:[],network_excess_ms:0,network_dropped_ms:[],webrtc:p.webrtc})}).catch(()=>{}).finally(()=>{z=!1})},1000)}catch(Y){throw Uh(),Y}},async startTalking(){if(T(),!P||!g||!q)throw Error("Coaching session not ready");if(F)return;F=!0;let W=++C,w=()=>!A&&F&&C===W;try{let Y=await navigator.mediaDevices.getUserMedia({audio:qh(_)});if(!w()){Y.getTracks().forEach((d)=>d.stop());return}if(E=Y,E.getTracks().forEach((d)=>{d.onended=()=>k()}),!H){let d=await Eh(h,!0);if(!w()){d.dispose();return}H=d}if(!o)await q.audioWorklet.addModule(H.urls[0]),o=!0;if(!w())return;if(await new Promise((d,p)=>{let I=setTimeout(()=>{U=void 0,p(Error("Coaching activation timed out"))},3000);U=(th,Rh)=>{if(Rh||th===W)clearTimeout(I),U=void 0,Rh?p(Rh):d()},X({type:"coach.start",generation:W})}),!w())return;gh=setInterval(()=>{if(w())try{X({type:"coach.keepalive",generation:W})}catch{k()}},1000),B=new AudioWorkletNode(q,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:_.inputGainDB,highpassFilter:_.highpassFilter}}),$=q.createMediaStreamDestination(),$.channelCount=1,D=q.createMediaStreamSource(Y),D.connect(B).connect($);let z=new RTCPeerConnection(e);if(Q=z,x?.register("coach",z,w,W),z.addTransceiver($.stream.getAudioTracks()[0],{direction:"sendonly",streams:[$.stream],sendEncodings:[{maxBitrate:Qh}]}),await new Promise((d,p)=>{let I=!1,th=(K)=>{if(I)return;I=!0,clearTimeout(Rh),L=void 0,O=void 0,K?p(K):d()},Rh=setTimeout(()=>th(Error("Coaching WebRTC timed out")),18000);L=()=>th(Error("Coaching cancelled")),O=(K,oh)=>{if(oh!==W||!w())return;z.setRemoteDescription({type:"answer",sdp:K}).catch((Ih)=>th(Ih))},z.onconnectionstatechange=()=>{if(!w())return;if(z.connectionState==="connected")th();if(z.connectionState==="failed"||z.connectionState==="closed")th(Error("Coaching media disconnected")),k()},hh(z,w).then((K)=>{if(w())X({type:"coach.webrtc.offer",generation:W,sdp:K})}).catch((K)=>th(K))}),w())a=!0,m.onTalking?.(!0)}catch(Y){if(w())throw k(),Y}},stopTalking:k,stop:Uh,setOutputVolume(W){if(!Number.isFinite(W)||W<0||W>1)throw RangeError("Listener volume must be 0–1");if(Fh=W,b)b.gain.value=W}}}}}function Xh(h={},t={}){let m=["turnTransport","iceTransportPolicy","turnTLSPort","mediaTransport","webrtcFec","inputDeviceId","outputDeviceId","outputVolume","echoCancellation","noiseSuppression","autoGainControl","inputGainDB","highpassFilter","playbackTargetMs","playbackMinMs","playbackMaxMs","playbackAdaptive","stereo"],_={...fh};for(let M of[h,t])for(let f of m){let u=M[f];if(u!==void 0)_[f]=u}if(v(_),_.mediaTransport=nh(_.mediaTransport),_.webrtcFec??=!0,_.outputVolume!==void 0&&(!Number.isFinite(_.outputVolume)||_.outputVolume<0||_.outputVolume>1))throw RangeError("Listener volume must be 0–1");return _}function bt(h,t,m,_,M=!1){let f,u=!1,y=!1,s,S={...h,onReady(q){y=!0,h.onReady(q)}};return{async start(q,b){if(u)throw Error("Listening cancelled");f=t==="websocket"?m(S):_(S);try{await f.start(q,b)}catch(A){if(f.stop(),u||y||t!=="auto"||kh(A)||M)throw A;f=m(S),await f.start(q,b)}if(u)f.stop();else if(s!==void 0)f.setOutputVolume(s)},async startTalking(){if(u||!f?.startTalking)throw Error("Coaching session not ready");await f.startTalking()},stopTalking(){f?.stopTalking?.()},stop(){u=!0,f?.stop()},setOutputVolume(q){if(!Number.isFinite(q)||q<0||q>1)throw RangeError("Listener volume must be 0–1");s=q,f?.setOutputVolume(q)}}}function E0(h,t={}){let m=Xh({},t),_=nh(m.mediaTransport),M=xt(h,m),f=i0(h,m);return{create(u){return bt(u,_,(y)=>M.create(y),(y)=>f.create(y),m.iceTransportPolicy==="relay")}}}function gt(h){if(h.byteLength<26||h.byteLength>984||h.byteLength%2)throw Error("Invalid listener audio frame");let t=new DataView(h),m=t.getUint32(4,!0);if(t.getUint32(0,!0)!==827085889||m>1)throw Error("Invalid listener audio protocol");let _=Number(t.getBigUint64(8,!0)),M=Number(t.getBigUint64(16,!0));if(!Number.isSafeInteger(_)||!Number.isSafeInteger(M))throw Error("Invalid listener audio timing");let f=new Float32Array((h.byteLength-24)/2);for(let u=0;u<f.length;u++)f[u]=t.getInt16(24+u*2,!0)/32768;return{direction:m,sequence:_,timestampMS:M,frame:f}}function At(h,t,m,_){if(h.length<1||h.length>480||!Number.isFinite(_)||_<0||!Number.isInteger(t)||t<1||t>4294967295)throw Error("Invalid coaching capture");let M=new ArrayBuffer(24+h.length*2),f=new DataView(M);f.setUint32(0,826496833,!0),f.setUint32(4,t,!0),f.setUint32(8,m>>>0,!0),f.setFloat64(16,_,!0);for(let u=0;u<h.length;u++){let y=Math.max(-1,Math.min(1,h[u]));f.setInt16(24+u*2,y<0?y*32768:y*32767,!0)}return M}function xt(h,t={}){return{create(m){let _,M,f,u,y=!1,s=[],S=!1,q=0,b=!1,A=!1,g,R,P,F,a,C,E,B=!1,D,$,Q=0,H=!1,o=[0,0],gh=[0,0],yh=[0,0],Fh=[0,0],e=[void 0,void 0],Qh=[new Ph,new Ph],x=t.outputVolume??1,G,U=()=>{let T=++q;if(b=!1,A=!1,E?.(T,Error("Coaching cancelled")),C)clearInterval(C);if(C=void 0,a?.port1.close(),a?.port2.close(),a=void 0,R?.disconnect(),R=void 0,P?.disconnect(),P=void 0,F?.disconnect(),F=void 0,g?.getTracks().forEach((k)=>{k.onended=null,k.stop()}),g=void 0,S&&H&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.stop",generation:T}));m.onTalking?.(!1)},O=()=>U(),V=()=>{if(document.visibilityState!=="visible")U()},L=()=>{if(y)return;if(U(),y=!0,G?.(),window.removeEventListener("blur",O),document.removeEventListener("visibilitychange",V),u)u.onmessage=u.onclose=u.onerror=null,u.close(1000,"listener_shutdown");if(M?.disconnect(),f?.disconnect(),_)_.onstatechange=null,_.close().catch(()=>{});for(let T of s.splice(0))T.dispose()},N=(T)=>{if(!y)L(),m.onClose(T)},X=()=>{if(y)throw Error("Listening cancelled")};return{async start(T,k){S=k?.coaching===!0,X();try{if(_=new AudioContext({latencyHint:"interactive"}),_.state==="suspended")await _.resume();if(X(),t.outputDeviceId&&"setSinkId"in _)await _.setSinkId(t.outputDeviceId);X();let n=await Dh(h,"listener");if(y)n.dispose(),X();if(s.push(n),await _.audioWorklet.addModule(n.urls[0]),X(),M=new AudioWorkletNode(_,"telephony-listener",{numberOfInputs:0,outputChannelCount:[t.stereo?2:1],processorOptions:{stereo:t.stereo,...v(t)}}),f=_.createGain(),f.gain.value=x,M.connect(f).connect(_.destination),M.onprocessorerror=()=>N("listener_audio_error"),M.port.onmessage=({data:Ah})=>{if(Ah?.type==="listener.diagnostics"&&!y)try{m.onDiagnostics?.({...Ah,media_transport:"websocket",context_state:_?.state,context_rate:_?.sampleRate,context_time_ms:(_?.currentTime??0)*1000,received_ms:[...gh],scheduled_ms:[...yh],sequence_gaps:[...Fh],network_excess_ms:Q,network_dropped_ms:[...o]})}catch{}},_.onstatechange=()=>{if(_?.state==="suspended"||_?.state==="interrupted")N("listener_audio_paused")},await new Promise((Ah,Uh)=>{let J=!1,hh=setTimeout(()=>{W(Error("Listener connection timed out")),N("listener_network_error")},1e4),W=(w)=>{if(J)return;J=!0,clearTimeout(hh),G=void 0,w?Uh(w):Ah()};G=()=>W(Error("Listening cancelled")),u=new WebSocket(T),u.binaryType="arraybuffer",u.onmessage=({data:w})=>{if(y||!_||!M)return;try{if(typeof w==="string"){let I=JSON.parse(w);if(I.type==="listener.ready"){if(S&&I.coaching!==!0)throw Error("Coaching not authorized");H=!0,W(),m.onReady("websocket")}else if(I.type==="coach.started")E?.(I.generation);else if(I.type==="coach.rejected")E?.(q,Error(I.detail||"Coaching unavailable"));else if(I.type==="coach.stopped"&&(I.generation===void 0||I.generation===q))U();return}if(!H||!(w instanceof ArrayBuffer))return;let Y=gt(w),z=Y.direction;if(gh[z]+=Y.frame.length*1000/24000,e[z]!==void 0&&Y.sequence<e[z])return;if(e[z]!==void 0&&Y.sequence>e[z])Fh[z]+=Y.sequence-e[z];e[z]=Y.sequence+1;let d=performance.now()-Y.timestampMS;if($=Math.min($??d,d),Q=Math.max(0,d-$),Q>200){o[z]+=Y.frame.length*1000/24000;return}D??=_.currentTime*1000+(t.playbackTargetMs??60)-Y.timestampMS;let p=Qh[z].process(Y.frame,24000,_.sampleRate);yh[z]+=Y.frame.length*1000/24000,M.port.postMessage({direction:z,frame:p,playAtMS:Y.timestampMS+D},[p.buffer])}catch{W(Error("Invalid listener media")),N("listener_protocol_error")}},u.onerror=()=>{W(Error("Listener connection failed")),N("listener_network_error")},u.onclose=(w)=>{W(Error(w.reason||"Listener disconnected")),N(w.reason||"listener_disconnected")}}),X(),S)window.addEventListener("blur",O),document.addEventListener("visibilitychange",V)}catch(n){throw L(),n}},async startTalking(){if(X(),!S||!H||!_||!u||u.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(b)return;b=!0;let T=++q,k=()=>!y&&b&&T===q;try{let n=await navigator.mediaDevices.getUserMedia({audio:qh({...fh,...t})});if(!k()){n.getTracks().forEach((J)=>J.stop());return}if(g=n,g.getAudioTracks().forEach((J)=>{J.onended=()=>U()}),!B){let J=await Eh(h,!0);if(!k()){J.dispose();return}s.push(J),await _.audioWorklet.addModule(J.urls[0]),B=!0}if(!k())return;if(await new Promise((J,hh)=>{let W=setTimeout(()=>{E=void 0,hh(Error("Coaching activation timed out"))},3000);E=(w,Y)=>{if(Y||w===T)clearTimeout(W),E=void 0,Y?hh(Y):J()},u.send(JSON.stringify({type:"coach.start",generation:T}))}),!k())return;R=new AudioWorkletNode(_,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:t.inputGainDB??0,highpassFilter:t.highpassFilter!==!1}}),P=_.createMediaStreamSource(n),F=_.createGain(),F.gain.value=0,P.connect(R).connect(F).connect(_.destination),a=new MessageChannel;let Ah=0,Uh=new Ph;a.port1.onmessage=({data:J})=>{if(!k()||!A||J?.type!=="capture"||!(J.frame instanceof Float32Array)||!_||!u)return;if(!Number.isFinite(J.timestamp_ms)||_.currentTime*1000-J.timestamp_ms>150||u.bufferedAmount>2880||u.readyState!==WebSocket.OPEN)return;let hh=Uh.process(J.frame,J.sample_rate,24000);if(hh.length>0&&hh.length<=480)u.send(At(hh,T,Ah++,J.timestamp_ms))},a.port1.start(),R.port.postMessage({type:"transport",port:a.port2},[a.port2]),A=!0,m.onTalking?.(!0),C=setInterval(()=>{if(k()&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.keepalive",generation:T}))},1000)}catch(n){if(k())throw U(),n}},stopTalking:U,stop:L,setOutputVolume(T){if(!Number.isFinite(T)||T<0||T>1)throw RangeError("Listener volume must be 0–1");if(x=T,f)f.gain.value=T}}}}}class C0{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;requestAbort;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;audioOptions;constructor(h,t={}){this.client=h;this.options=t;this.audioOptions=Xh(t.audio,t),this.runtime=t.runtime??E0(h.app,this.audioOptions)}setAudioOptions(h){let t=Xh({},h);this.audioOptions=t,this.runtime=this.options.runtime??E0(this.client.app,t)}getAudioOptions=()=>Object.freeze({...this.audioOptions});getSnapshot=()=>this.snapshot;subscribe=(h)=>{return this.observers.add(h),()=>{this.observers.delete(h)}};update(h){this.snapshot=Object.freeze({...this.snapshot,...h});for(let t of this.observers)try{t(this.snapshot)}catch{}}async listen(h){return this.begin(h,!1)}async coach(h){return this.begin(h,!0)}async begin(h,t){if(this.client.cancelNetworkChecks(),this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=t,this.retryUntil=0,this.retryDelay=500;let m=++this.generation;return this.update({state:"connecting",callId:h,detail:void 0,coaching:t,talking:!1,mediaTransport:void 0}),this.connect(h,m)}async connect(h,t){this.requestAbort?.abort(),this.requestAbort=new AbortController;let m,_=++this.attempt,M=()=>t===this.generation&&_===this.attempt&&!this.disposed;try{if(m=this.coaching?await this.client.coachSession(h):await this.client.listenSession(h,this.requestAbort?.signal),!M()){await this.client.stopListening(m).catch(()=>{});return}this.session=m;let f=this.runtime.create({onReady:(u)=>{if(M())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0,mediaTransport:u})},onClose:(u)=>{if(M())this.disconnected(u,h,t)},onTalking:(u,y)=>{if(M())this.update({talking:u,detail:y})},onDiagnostics:(u)=>{if(M())try{this.options.onDiagnostics?.(u)}catch{}}});if(this.audio=f,this.lease=new Jh(m.lease_seconds,()=>this.client.renewListening(m),(u)=>{if(M())this.disconnected(u==="revoked"?"access_revoked":"media_disconnected",h,t,u==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,m.lease_started_ms),await f.start(this.client.listenerMediaURL(m,this.audioOptions.mediaTransport),{coaching:m.coaching===!0}),!M())f.stop()}catch(f){if(!M()||m&&this.session!==m)return;let u=f?.status,y;try{y=JSON.parse(f.body??"{}").code}catch{}let s=y==="call_ended"?"call_ended":u===401||u===403||u===404?"access_revoked":"listener_disconnected";throw this.disconnected(s,h,t,String(f)),f}}cleanup(){this.requestAbort?.abort(),this.requestAbort=void 0,this.lease?.stop(),this.lease=void 0;let h=this.audio;this.audio=void 0,h?.stop();let t=this.session;return this.session=void 0,t?this.client.stopListening(t).catch(()=>{}):Promise.resolve()}disconnected(h,t,m,_=h){if(m!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let M=h==="call_ended",f=h==="access_revoked",u=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(h);if(!this.coaching&&!M&&!f&&u&&this.options.reconnect!==!1){this.retryUntil||=Date.now()+30000;{this.update({state:"reconnecting",detail:_,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,m===this.generation&&!this.disposed)this.connect(t,m).catch(()=>{})},Date.now()<this.retryUntil?this.retryDelay:15000),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:M?"call_ended":f?"access_revoked":"disconnected",detail:_,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(h){this.audio?.setOutputVolume(h)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let h=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1,mediaTransport:void 0}),await h}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}var Pt={set:(h,t)=>setTimeout(h,t),clear:(h)=>clearTimeout(h)};class F0{run;permanent;ended;event;clock;stopped=!1;active;timer;attempt=0;online=()=>this.wake();constructor(h,t,m,_,M=Pt){this.run=h;this.permanent=t;this.ended=m;this.event=_;this.clock=M;globalThis.addEventListener?.("online",this.online),this.schedule(1000)}emit(h){try{this.event?.(h)}catch{}}schedule(h){if(!this.stopped)this.clock.clear(this.timer),this.timer=this.clock.set(()=>void this.tick(),h)}wake(){if(!this.stopped&&!this.active)this.schedule(0)}async tick(){if(this.stopped||this.active)return;let h=new AbortController;this.active=h,this.attempt++,this.emit("attempt_started");let t;try{if(await Promise.race([this.run(h.signal),new Promise((m,_)=>{t=this.clock.set(()=>{h.abort(),_(Error("Audio recovery request timed out"))},30000)})]),this.stopped)return;this.emit("connected"),this.stop()}catch(m){if(this.stopped)return;if(this.permanent(m)){this.emit("revoked"),this.stop(),this.ended(m);return}this.emit("waiting_for_network"),this.schedule(Math.min(15000,1000*2**Math.min(4,this.attempt)))}finally{if(this.clock.clear(t),this.active===h)this.active=void 0}}stop(){if(this.stopped)return;this.stopped=!0,this.clock.clear(this.timer),this.active?.abort(),globalThis.removeEventListener?.("online",this.online)}}function k0(h){if(!h)return"placing";if(vh(h))return"ended";if(h==="ringing")return"ringing";if(h==="answered"||h==="in-progress")return"connected";return"placing"}var uh=(h)=>h instanceof Error?h.message:String(h);class R0{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;hasStatusPush=!1;automaticRecovery;hasLiveAudio=!1;uncertainAttachment;scheduleStatusPoll;outbound=!1;ringing=!1;runtime;audioOptions;preferredPlayback={};preferredMediaTransport;networkTimer;networkRequest;networkChanged=()=>{if(this.disposed)return;if(this.snapshot.networkCheck)this.update({networkCheck:{...this.snapshot.networkCheck,state:"inconclusive"}});this.scheduleNetworkCheck(1000)};interval;constructor(h,t={}){this.client=h;this.options=t;if(this.runtime=t.audioRuntime??D0(h.app),this.audioOptions={...fh,...t.audio,...t.mediaTransport?{mediaTransport:t.mediaTransport}:{}},this.preferredMediaTransport=this.audioOptions.mediaTransport,this.preferredPlayback={playbackTargetMs:this.audioOptions.playbackTargetMs,playbackMinMs:this.audioOptions.playbackMinMs,playbackMaxMs:this.audioOptions.playbackMaxMs},v(this.audioOptions),this.interval=t.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms");if(t.networkCheck)globalThis.addEventListener?.("online",this.networkChanged),this.scheduleNetworkCheck(500+Math.random()*15000)}scheduleNetworkCheck(h=300000+Math.random()*60000){if(!this.options.networkCheck||this.disposed)return;clearTimeout(this.networkTimer),this.networkTimer=setTimeout(()=>{if(!this.session&&!this.snapshot.busy&&(typeof document>"u"||document.visibilityState!=="hidden"))this.checkNetwork().catch(()=>{});else this.scheduleNetworkCheck()},h)}cancelNetworkCheck(){if(this.snapshot.networkChecking)this.update({networkChecking:!1});clearTimeout(this.networkTimer),this.networkRequest?.abort(),this.networkRequest=void 0,this.client.cancelNetworkChecks()}async checkNetwork(h={}){if(this.assertOpen(),this.session||this.snapshot.busy||!this.client.networkChecksAllowed)throw Error("Network checks pause during calls or incoming offers");this.cancelNetworkCheck();let t=new AbortController;this.networkRequest=t;let m=this.client.registerNetworkCheck(t);this.update({networkChecking:!0});let _=()=>t.abort();if(h.signal?.addEventListener("abort",_,{once:!0}),h.signal?.aborted)_();let M;try{let f=new AbortController,u=()=>f.abort();t.signal.addEventListener("abort",u,{once:!0}),M=setTimeout(u,3000);let y=!0;try{let S=await r(this.client.networkPolicy({},f.signal),f.signal),q=Ch(S.effective);if(y=q.background_check,!t.signal.aborted&&!this.session&&!this.snapshot.busy)this.applyNetworkPolicy(S.configured===!1?void 0:q)}catch{}finally{clearTimeout(M),t.signal.removeEventListener("abort",u)}if(!y&&!h.paths)return{state:"unavailable",checkedAt:new Date().toISOString(),paths:[]};let s=await this.client.checkNetwork({...h,signal:t.signal});if(this.networkRequest===t&&!t.signal.aborted){this.update({networkCheck:s});try{this.options.onNetworkCheck?.(s)}catch{}}return s}finally{if(m(),clearTimeout(M),h.signal?.removeEventListener("abort",_),this.networkRequest===t)this.networkRequest=void 0,this.update({networkChecking:!1}),this.scheduleNetworkCheck()}}applyNetworkPolicy(h){if(this.assertOpen(),h===void 0){if(this.audioOptions.networkProfile)this.audioOptions={...this.audioOptions,...this.preferredPlayback,networkProfile:void 0,mediaTransport:this.preferredMediaTransport};return}let t=Ch(h);this.audioOptions={...this.audioOptions,networkProfile:t,playbackTargetMs:t.playback_target_ms,playbackMinMs:Math.min(this.preferredPlayback.playbackMinMs??60,t.playback_target_ms),playbackMaxMs:t.playback_max_ms,mediaTransport:t.media_transport==="inherit"?this.preferredMediaTransport:t.media_transport}}createCallListener(h={}){return this.assertOpen(),this.client.createCallListener({...this.audioOptions,...h.audio,...h})}getSnapshot=()=>this.snapshot;subscribe=(h)=>{return this.assertOpen(),this.listeners.add(h),()=>{this.listeners.delete(h)}};update(h){let t={...this.snapshot,...h};if(t.callId&&k0(t.carrierStatus)==="connected")t.phase=t.audioState==="live"?"connected":"placing";this.snapshot=Object.freeze(t);for(let m of this.listeners)try{m(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(h){if(this.disposed||h!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(){return this.runtime.cancelPreparation?.(),this.automaticRecovery?.stop(),this.automaticRecovery=void 0,this.uncertainAttachment=void 0,this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(h){return!this.disposed&&h===this.generation}finish(h){if(this.current(h))this.runtime.cancelPreparation?.(),this.update({busy:!1})}cancellable(h,t){let m=this.cancellation.signal;return new Promise((_,M)=>{let f=()=>{m.removeEventListener("abort",f),M(Error("Softphone operation cancelled"))};if(m.addEventListener("abort",f,{once:!0}),h.then(_,M).finally(()=>m.removeEventListener("abort",f)),!this.current(t)||m.aborted)f()})}begin(h){if(this.cancelNetworkCheck(),this.assertOpen(),this.snapshot.busy||this.hangingUp||h&&this.session)throw Error("Softphone already has an active operation or call");let t=this.invalidate();return this.update({busy:!0,detail:void 0}),t}async prepareAudio(h,t){let m=[],_=(M)=>{if(this.current(h)&&!this.cancellation.signal.aborted)m.push(Mh(M))};try{return await this.cancellable(l(t?"audio_preflight":"audio_prepare",_,()=>t?this.runtime.preflight(this.audioOptions,_):this.runtime.prepare?.(this.audioOptions,_)??Promise.resolve()),h),this.assertCurrent(h),m}catch(M){if(this.current(h))for(let f of m)this.reportSessionEvent(f);throw M}}async dial(h){let t=this.begin(!0),m,_=!1;try{this.assertCurrent(t);let M={to:h.to.trim(),from:h.from?.trim(),timeout_sec:h.timeout_sec,recording:h.recording},f=JSON.stringify(M);if(!this.intent||this.intent.value!==f||h.idempotency_key&&h.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:f,request:{...M,idempotency_key:h.idempotency_key||crypto.randomUUID()}};let u=await this.prepareAudio(t,!0);return this.assertCurrent(t),m=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(t),_=!0,this.outbound=!0,await this.attachAudio(m,t,u),m.call_id}catch(M){if(m&&(this.current(t)||!_||this.disposed))await this.recoverStartup(m,t,()=>this.client.hangup(m.call_id));if(this.current(t))this.update({detail:uh(M)});throw M}finally{this.finish(t)}}async answer(h,t={}){let m=this.begin(!0),_,M=!1;try{this.assertCurrent(m);let f=await this.prepareAudio(m,!0);_=await this.client.answer(h,t),this.assertCurrent(m),M=!0,this.outbound=!1,await this.attachAudio(_,m,f)}catch(f){if(_&&(this.current(m)||!M||this.disposed))await this.recoverStartup(_,m,()=>this.client.release(_));if(this.current(m))this.update({detail:uh(f)});throw f}finally{this.finish(m)}}async recoverStartup(h,t,m){try{if(await m(),this.current(t))this.clearCall()}catch{if(this.current(t))this.session=h,this.update({callId:h.call_id,audioState:"error"}),this.startPolling()}}async join(h){await this.answer(h,{rejoin:!0})}async attach(h,t="attach"){await this.acquireSession(h,!1,t)}async takeover(h){await this.acquireSession(h,!0)}async acquireSession(h,t,m="takeover"){let _=this.begin(!0);try{let M=await this.prepareAudio(_,!0);this.assertCurrent(_);let f=Gh(t?"takeover":m),u=await(t?this.client.takeover(h,f):this.client.attach(h,f));this.assertCurrent(_),await this.attachAudio(u,_,M),await this.reconcileAttachedCall(u,_)}catch(M){if(this.current(_))this.update({detail:uh(M)});throw M}finally{this.finish(_)}}async reconnect(h,t="manual_reconnect"){if(!this.session)throw Error("No call to reconnect");let m={...this.audioOptions,...h};v(m);let _=this.begin(!1);this.configureAudio(h??{});let M=this.session.call_id,f=Gh(t),u=this.session.session_generation,y=(s,S={})=>this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:s,...f,previous_session_id:u,...S});y("started");try{let s=await this.prepareAudio(_,!1),S;try{S=await this.acquireRecoverySession(this.session,{...f,previous_session_id:u},this.cancellation.signal)}catch(q){let b=_h(q),A;try{A=JSON.parse(q.body??"{}").current_generation}catch{}if(t==="automatic_retry"||b.code!=="media_session_changed"||!A||!/^session-[a-f0-9-]{1,64}$/.test(A))throw q;this.assertCurrent(_),S=await this.client.attach(M,{...Gh(t),previous_session_id:A},{signal:this.cancellation.signal},A)}this.assertCurrent(_),await this.attachAudio(S,_,s),await this.reconcileAttachedCall(S,_),y("connected",{session_id:S.session_generation})}catch(s){if(y(this.current(_)?"failed":"cancelled"),this.current(_))this.update({detail:uh(s)});throw s}finally{this.finish(_)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let h=this.session;if(!h){this.cancellation.abort(),this.runtime.cancelPreparation?.();return}let t=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let m=(async()=>{try{if(await this.client.hangup(h.call_id),this.current(t))this.clearCall()}catch(_){if(this.current(t))this.update({audioState:"error",detail:uh(_)}),this.startPolling();throw _}finally{this.finish(t)}})();this.hangingUp=m;try{await m}finally{if(this.hangingUp===m)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(h){this.assertOpen();let t=this.session;if(!t)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let m=this.generation;this.update({busy:!0,detail:void 0});let _;try{_=this.client[h](t.call_id)}catch(M){if(this.current(m)&&this.session===t)this.update({busy:!1,detail:uh(M)});throw M}this.controlPending=_;try{let M=await _;if(this.current(m)&&this.session===t)this.update({holdState:M.hold_state,recordingState:M.recording_state,controlError:M.control_error,capabilities:M.capabilities});return M}catch(M){if(this.current(m)&&this.session===t)this.update({detail:uh(M)});throw M}finally{if(this.controlPending===_)this.controlPending=void 0;if(this.current(m)&&this.session===t)this.update({busy:!1})}}configureAudio(h){this.assertOpen();let t={...this.audioOptions,...h};if(v(t),h.mediaTransport!==void 0)this.preferredMediaTransport=h.mediaTransport;for(let m of["playbackTargetMs","playbackMinMs","playbackMaxMs"])if(m in h)this.preferredPlayback[m]=h[m];if(this.audioOptions=t,t.networkProfile)this.applyNetworkPolicy(t.networkProfile)}setMuted(h){this.assertOpen(),this.audio?.setMuted(h),this.update({muted:h})}setOutputVolume(h){if(this.assertOpen(),!Number.isFinite(h)||h<0||h>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=h,this.audio?.setOutputVolume(h)}sendDTMF(h){if(this.assertOpen(),!/^[0-9*#]+$/.test(h))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(h)}observeCall(h){if(this.disposed||h.id!==this.session?.call_id)return;if(h.direction)this.outbound=h.direction==="outbound";if(vh(h.status)){this.invalidate(),this.clearCall(h.status),this.update({busy:!1,phase:"ended",termination:h.termination,answeredBy:h.answered_by??this.snapshot.answeredBy,endedAt:h.ended_at});return}this.update({carrierStatus:h.status,phase:k0(h.status),answeredBy:h.answered_by??this.snapshot.answeredBy,holdState:h.hold_state??this.snapshot.holdState,recordingState:h.recording_state??this.snapshot.recordingState,controlError:h.control_error??this.snapshot.controlError,capabilities:h.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let h=this.options.ringback;return typeof h==="object"&&h?h.country:void 0}syncRingback(){let h=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(h&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!h&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(h,t,m=[]){this.hasStatusPush=!1,this.assertCurrent(t),this.stopAudio(),this.session=h,this.update({callId:h.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let _,M=()=>!this.disposed&&_!==void 0&&this.audio===_,f=(s)=>{if(M())try{s()}catch{}},u,y;try{this.assertCurrent(t),_=this.runtime.create({refreshMediaURL:(S,q)=>{let b=S??Gh("automatic_retry");if(u)return this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"coalesced",...b,shared_attempt_id:y?.attempt_id}),u;if(!M()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let A=this.generation,g=(async()=>{let P=h.session_generation,F=await this.acquireRecoverySession(h,{...b,previous_session_id:P},q);if(this.assertCurrent(A),!M())throw Error("Audio connection no longer active");return h=F,this.session=F,this.startLease(F),this.reportSessionEvent({timestamp:new Date().toISOString(),action:"media_session",outcome:"replaced",...b,previous_session_id:P,session_id:F.session_generation}),this.client.mediaURL(F,this.audioOptions.mediaTransport)})();u=g,y=b;let R=()=>{if(u===g)u=void 0,y=void 0};return g.then(R,R),g},sessionDiagnostics:()=>({session_id:h.session_generation}),onSessionEvent:(S)=>f(()=>{let q={audio_assets:"Loading audio resources…",microphone_device:"Opening microphone…",audio_context:"Enabling browser playback…",audio_worklet:"Loading audio processor…",audio_transport:"Connecting audio transport…"};if(this.snapshot.busy&&S.outcome==="started"&&q[S.action])this.update({detail:q[S.action]});this.options.onSessionEvent?.(Mh({...S,session_id:S.session_id??h.session_generation}))}),onState:(S,q,b)=>{if(!M())return;if(S==="ended"&&q==="call.ended"){this.observeCall({id:h.call_id,status:"completed"});return}if(S==="live")this.hasLiveAudio=!0;if(this.update({audioState:S,detail:q}),this.scheduleStatusPoll?.(),M()&&(S==="error"||S==="ended")){if(this.stopAudio(),S==="error"){if(this.reconcileFailedAudio(h,this.generation),b?.recoverable)this.scheduleAudioRecovery()}}},onMediaReadiness:(S)=>f(()=>this.options.onMediaReadiness?.(S)),onLevels:(S,q)=>f(()=>this.options.onLevels?.(S,q)),onAudioHealth:(S)=>f(()=>this.options.onAudioHealth?.(S)),onDiagnostics:(S)=>f(()=>this.options.onDiagnostics?.(S)),onNotice:(S)=>f(()=>this.options.onNotice?.(S)),onCallStatus:(S)=>f(()=>{this.hasStatusPush=!0,this.observeCall({id:S.call_id,status:S.status,direction:S.direction,answered_at:S.answered_at,ended_at:S.ended_at,answered_by:S.answered_by,termination:S.termination,hold_state:S.hold_state,recording_state:S.recording_state,control_error:S.control_error}),this.scheduleStatusPoll?.()})}),this.audio=_,this.assertCurrent(t);for(let S of m)this.reportSessionEvent(S);this.assertCurrent(t),this.startPolling(),this.startLease(h),_.setMuted(this.snapshot.muted);let s;try{await this.cancellable(Promise.race([_.start(this.client.mediaURL(h,this.audioOptions.mediaTransport),this.audioOptions),new Promise((S,q)=>{s=setTimeout(()=>q(Error("Audio setup timed out. Check browser permissions and the network, then try again.")),this.audioOptions.mediaTransport==="auto"?40000:20000)})]),t)}finally{clearTimeout(s)}if(this.assertCurrent(t),!M())throw Error(this.snapshot.detail||"Audio connection ended during setup");_.setMuted(this.snapshot.muted),this.syncRingback()}catch(s){if(M())this.stopAudio();if(this.current(t))this.update({audioState:"error",detail:uh(s)});throw s}}async acquireRecoverySession(h,t,m){let _=this.uncertainAttachment??{diagnostics:t,expected:h.attachment_guard_version===1?h.session_generation??"":void 0};if(m?.aborted)throw new DOMException("Cancelled","AbortError");if(this.uncertainAttachment=_,_.diagnostics.attempt_id!==t.attempt_id)this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"idempotent_retry",...t,shared_attempt_id:_.diagnostics.attempt_id});try{let M=await this.client.attach(h.call_id,_.diagnostics,{signal:m},_.expected);if(m?.aborted)throw new DOMException("Cancelled","AbortError");return this.uncertainAttachment=void 0,M}catch(M){let f=_h(M).status;if(f!==void 0&&f>=400&&f<500)this.uncertainAttachment=void 0;throw M}}scheduleAudioRecovery(){if(this.disposed||!this.session||!this.hasLiveAudio||this.automaticRecovery)return;let h=this.session.call_id,t=this.generation;this.update({audioState:"reconnecting",detail:"Network unavailable; waiting to restore audio."});let m=(f)=>{let u=_h(f);return u.denied||u.code==="media_session_changed"||u.code==="call_ended"||u.code==="call_not_attachable"||["NotAllowedError","NotFoundError"].includes(f?.name)},_=this.uncertainAttachment?.diagnostics??Gh("automatic_retry"),M=_;this.automaticRecovery=new F0(async(f)=>{this.assertCurrent(t);let u=await zh((q)=>this.client.getCall(h,q),f);if(!u)throw Object.assign(Error("Call unavailable"),{status:404});if(vh(u.status)){this.observeCall(u);return}this.assertCurrent(t);let y=await this.prepareAudio(t,!1);if(f.aborted)throw new DOMException("Cancelled","AbortError");let s=await this.acquireRecoverySession(this.session,M,f);if(this.assertCurrent(t),f.aborted)throw new DOMException("Cancelled","AbortError");let S=()=>{if(this.current(t)&&this.session===s)this.stopAudio()};f.addEventListener("abort",S,{once:!0});try{if(await this.attachAudio(s,t,y),f.aborted)throw new DOMException("Cancelled","AbortError")}finally{f.removeEventListener("abort",S)}this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:"connected",...M}),this.automaticRecovery?.stop(),this.automaticRecovery=void 0},m,(f)=>{if(this.current(t))this.automaticRecovery=void 0,this.update({audioState:"error",detail:uh(f)})},(f)=>{if(f==="attempt_started")M={..._,attempt_id:ah("attempt")};this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:f,...M})})}async reconcileAttachedCall(h,t){try{let m=await this.client.getCall(h.call_id);if(this.current(t)&&this.session===h&&m)this.observeCall(m)}catch{}}async reconcileFailedAudio(h,t){try{let m=await this.client.getCall(h.call_id);if(!this.current(t)||this.session!==h||!m)return;if(m.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(m)}catch{}}startLease(h){if(this.lease?.stop(),!h.lease_seconds)return;this.lease=new Jh(h.lease_seconds,()=>this.client.renew(h),(t,m)=>{if(this.disposed||this.session!==h)return;if(this.stopAudio(),this.update({audioState:"error",detail:t==="expired"?"Audio session expired. Reconnecting audio.":`Audio authorization ended: ${uh(m??t)}`}),t==="expired")this.scheduleAudioRecovery();else this.automaticRecovery?.stop(),this.automaticRecovery=void 0},(t)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(t);else try{this.options.onSessionEvent?.(t)}catch{}},h.lease_started_ms)}reportSessionEvent(h){if(h=Mh(h),this.audio?.recordSessionEvent)try{this.audio.recordSessionEvent(h)}catch{}else try{this.options.onSessionEvent?.(h)}catch{}}stopAudio(){this.lease?.stop(),this.lease=void 0;let h=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{h?.stopRingback?.()}catch{}}try{h?.stop()}catch{}if(h)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){this.scheduleStatusPoll=void 0,clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let h=new AbortController;this.polling=h;let t=!1,m=performance.now(),_=()=>this.hasStatusPush&&this.snapshot.audioState==="live"?Math.max(1,Math.max(this.interval,30000)-(performance.now()-m)):this.interval,M=async()=>{if(t)return;let f=this.session?.call_id;if(!f||h.signal.aborted)return;t=!0;try{let u=await this.client.getCall(f,h.signal);if(m=performance.now(),!h.signal.aborted&&u)this.observeCall(u)}catch(u){if(!h.signal.aborted)this.hasStatusPush=!1,this.update({detail:`Call status unavailable: ${uh(u)}`})}finally{t=!1,this.scheduleStatusPoll?.()}};this.scheduleStatusPoll=()=>{if(t||h.signal.aborted||!this.session||this.disposed)return;clearTimeout(this.timer),this.timer=setTimeout(M,_())},this.scheduleStatusPoll()}clearCall(h){this.hasStatusPush=!1,this.stopAudio(),this.stopPolling(),this.session=void 0,this.hasLiveAudio=!1,this.scheduleNetworkCheck(),this.outbound=!1,this.update({callId:void 0,carrierStatus:h,audioState:"idle",muted:!1,detail:void 0,phase:h?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.cancelNetworkCheck(),globalThis.removeEventListener?.("online",this.networkChanged),this.disposed)return;this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function Et(h,t="en"){if(h?.reason==="ai_inactivity")return t.toLowerCase().startsWith("fr")?"Fin de l’appel : aucune réponse":"Call ended: no response";if(h?.reason==="ai_policy_failure")return t.toLowerCase().startsWith("fr")?"Échec du rappel d’inactivité IA":"AI inactivity reminder failed";if(h?.reason==="time_limit"||h?.reason==="ai_max_duration")return t.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return h?.reason?.replaceAll("_"," ")??""}class j0 extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function I0(h){return h.direction==="inbound"&&h.status==="pending"&&h.answerable!==!1&&!h.routing_waiting&&(h.peer_kind==="human"||Boolean(h.ring_offers?.some((t)=>t.kind==="browser")))}var vh=(h)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(h);function c(h){if(!h||/[\s/\\?#]/.test(h))throw Error("Invalid call ID");return encodeURIComponent(h)}class rh{app;options;transport;networkCheckControllers=new Set;incomingPending=!1;listMicrophones=Z0;createMicrophonePreview=(h)=>K0(h,this.app);constructor(h,t={}){this.app=h;this.options=t;if(this.transport=new t0(h,t.authProvider?"proxy":t.direct?"direct":t.runtime,globalThis.fetch,t.direct),h.name!=="telephony"||!h.projectId||!h.installId)throw Error("Telephony requires an explicit project and installation")}get(h,t){return this.transport.request("GET",h,void 0,t)}post(h,t,m){return this.transport.request("POST",h,t,m)}mediaPost(h,t,m){return this.transport.requestMedia(h,t,m)}subscribe(h,t,m){return this.transport.subscribe(h,t,m)}path(h){if(!this.options.authProvider)return h;return`/user${h}${h.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(h){let t=await this.get(this.path("/calls"),{signal:h});if(!Array.isArray(t?.calls)||t.calls.some((m)=>!m||typeof m.id!=="string"||typeof m.status!=="string"))throw Error("Invalid Telephony calls response");if(this.incomingPending=t.calls.some(I0),this.incomingPending)this.cancelNetworkChecks();return t.calls}async getCall(h,t){let m=await this.get(this.path(`/calls?call_id=${c(h)}`),{signal:t});if(!Array.isArray(m?.calls))throw Error("Invalid Telephony call response");return m.calls.find((_)=>_.id===h)}incomingCalls(h){return h.filter(I0)}watchCalls(h,t={}){let m=t.intervalMs??2000;if(!Number.isFinite(m)||m<100)throw Error("Call watch interval must be at least 100 ms");let _=new AbortController,M,f,u=!1,y=!1,s=-1/0,S,q,b=performance.now(),A=()=>{let a=performance.now()-s;return a<15000?Math.max(1,Math.min(Math.max(m,30000)-(performance.now()-b),15000-a)):m},g=new Set,R=()=>{clearTimeout(M),_.abort(),f?.close(),t.signal?.removeEventListener("abort",R)},P=(a)=>{try{t.onError?.(a)}catch{R()}};if(t.signal?.addEventListener("abort",R,{once:!0}),t.signal?.aborted)R();let F=async(a)=>{if(_.signal.aborted)return;if(u){y=!0;return}clearTimeout(M),u=!0;let C=performance.now(),E=S;try{let B=await this.listCalls(_.signal);if(b=performance.now(),!_.signal.aborted){h(B),q=E;for(let D of B){if(D.answerable!==!0||D.status!=="pending")continue;for(let $ of D.ring_offers??[]){if($.kind!=="browser"||!$.id||g.has($.id))continue;g.add($.id),this.acknowledgeOffer(D.id,$.id).catch((Q)=>{if(![404,405].includes(Q?.status??0))g.delete($.id)})}}try{t.onTiming?.({trigger:a,fetchMs:performance.now()-C})}catch{}}}catch(B){if(s=-1/0,q=void 0,!_.signal.aborted){let D=B?.status;try{t.onFailure?.({trigger:a,fetchMs:performance.now()-C,status:typeof D==="number"?D:void 0,error:B})}catch{}P(B)}}finally{if(u=!1,!_.signal.aborted)if(y)y=!1,F("push");else M=setTimeout(()=>void F("poll"),A())}};if(F("poll"),!_.signal.aborted&&t.push!==!1&&typeof this.app.subscribe==="function")try{f=this.subscribe(this.path("/calls/events"),(a)=>{if(a.type==="heartbeat"||a.type==="calls.changed"){if(s=performance.now(),a.type==="calls.changed"){let C=typeof a.state==="string"&&/^[0-9a-f]{64}$/.test(a.state)?a.state:void 0;if(S=C,!C||C!==q||u)F("push")}else if(!u)clearTimeout(M),M=setTimeout(()=>void F("poll"),A())}else if(a.type==="access.revoked")s=-1/0,f?.close(),F("push")},{transport:"fetch",signal:_.signal,reconnectDelayMs:250,onError:(a)=>{if(s=-1/0,!u&&!_.signal.aborted)clearTimeout(M),M=setTimeout(()=>void F("poll"),m);let C=a?.status;if(C===404||C===405||C===501)f?.close()}})}catch{}return{close:R}}async place(h){if(this.cancelNetworkChecks(),!/^\+[1-9]\d{7,14}$/.test(h.to)||!h.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let t=Sh();return this.session(await this.mediaPost(this.path("/softphone/place"),h),void 0,"media",t)}async answer(h,t={}){this.cancelNetworkChecks();let m=Sh();try{return this.session(await this.mediaPost(this.path(`/softphone/answer/${c(h)}`),t),h,"media",m)}catch(_){let M=_;if(M.status===409&&typeof M.body==="string"){let f;try{f=JSON.parse(M.body)}catch{}if(f?.code==="offer_expired")throw new j0}throw _}}async acknowledgeOffer(h,t){await this.post(this.path(`/softphone/offer/ack/${c(h)}`),{offer_id:t})}async declineOffer(h,t){await this.post(this.path(`/softphone/offer/decline/${c(h)}`),{offer_id:t})}async attach(h,t,m,_){let M=Sh();return this.session(await zh((f)=>this.mediaPost(this.path(`/softphone/attach/${c(h)}`),{...t?{media_diagnostics:t}:{},..._!==void 0&&t?.attempt_id?{attachment_guard:{id:t.attempt_id,expected_generation:_}}:{}},{...m,signal:f}),m?.signal??void 0),h,"media",M)}async takeover(h,t){let m=Sh();return this.session(await this.mediaPost(this.path(`/softphone/takeover/${c(h)}`),t?{media_diagnostics:t}:{}),h,"media",m)}createCallListener(h={}){return new C0(this,{...h,...Xh({...this.options.audio,...h.audio},h)})}async listenSession(h,t){let m=Sh();return this.session(await zh((_)=>this.mediaPost(this.path(`/softphone/listen/${c(h)}`),{},{signal:_}),t,5000,(_)=>{try{this.stopListening(this.session(_,h,"listen-media",m)).catch(()=>{})}catch{}}),h,"listen-media",m)}async coachSession(h){let t=Sh(),m=this.session(await this.mediaPost(this.path(`/softphone/coach/${c(h)}`),{}),h,"listen-media",t);if(m.coaching!==!0)throw Error("Invalid coaching session");return m}async renewListening(h){return this.mediaPost(this.path(`/softphone/${h.coaching?"coach-renew":"listen-renew"}/${c(h.call_id)}`),{session_token:h.session_token})}async stopListening(h){await this.mediaPost(this.path(`/softphone/${h.coaching?"coach-stop":"listen-stop"}/${c(h.call_id)}`),{session_token:h.session_token})}async listenerAudit(h){return this.get(this.path(`/softphone/listen-audit/${c(h)}`))}networkPolicy(h={},t){return this.post(this.path("/softphone/network-policy"),h,{signal:t})}configureNetworkPolicy(h={}){return this.post(this.path("/numbers/network-policy"),h)}registerNetworkCheck(h){return this.networkCheckControllers.add(h),()=>this.networkCheckControllers.delete(h)}cancelNetworkChecks(h){for(let t of this.networkCheckControllers)if(t.signal!==h)t.abort(),this.networkCheckControllers.delete(t)}get networkChecksAllowed(){return!this.incomingPending}async checkNetwork(h={}){if(this.incomingPending)return{state:"cancelled",checkedAt:new Date().toISOString(),paths:[]};this.cancelNetworkChecks(h.signal);let t=new AbortController;this.networkCheckControllers.add(t);let m=()=>t.abort();if(h.signal?.addEventListener("abort",m,{once:!0}),h.signal?.aborted)m();try{return await W0((_,M)=>this.post(this.path("/softphone/network-check"),_,{signal:M}),{...h,signal:t.signal})}finally{h.signal?.removeEventListener("abort",m),this.networkCheckControllers.delete(t)}}turnPolicy(h={}){return this.post(this.path("/softphone/turn-policy"),h)}configureTURNPolicy(h={}){return this.post(this.path("/numbers/turn-policy"),h)}carrierMediaSettings(h){return this.post(this.path("/numbers/carrier-media"),h)}outboundTransport(h){return this.post(this.path("/numbers/outbound-transport"),h)}outboundNumbers(h={}){return this.get(this.path(h.fresh?"/softphone/numbers?fresh=true":"/softphone/numbers"))}async renew(h){return this.mediaPost(this.path(`/softphone/renew/${c(h.call_id)}`),{session_token:h.session_token})}async release(h){if(!h.session_token)throw Error("Answer session has no release token");await this.mediaPost(this.path(`/softphone/release/${c(h.call_id)}`),{session_token:h.session_token})}async hangup(h){await this.post(this.path(`/calls/${c(h)}/hangup`),{})}hold(h){return this.post(this.path(`/calls/${c(h)}/hold`),{})}resume(h){return this.post(this.path(`/calls/${c(h)}/resume`),{})}pauseRecording(h){return this.post(this.path(`/calls/${c(h)}/pause-recording`),{})}resumeRecording(h){return this.post(this.path(`/calls/${c(h)}/resume-recording`),{})}createSoftphone(h={}){return new R0(this,{...h,audio:{...this.options.audio,...h.audio}})}mediaURL(h,t){return this.resolveMediaURL(h,"media",t)}listenerMediaURL(h,t){return this.resolveMediaURL(h,"listen-media",t)}resolveMediaURL(h,t,m){let _=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),M=new URL(h.media_url,_),f=`/api/apps/telephony/_install/${this.app.installId}/softphone/${t}/${c(h.call_id)}/`;if(M.origin!==_.origin||!["http:","https:"].includes(M.protocol)||M.username||M.password||M.search||M.hash||!M.pathname.startsWith(f)||!/^[A-Za-z0-9_-]+$/.test(M.pathname.slice(f.length)))throw Error("Invalid Telephony media endpoint");if(t==="listen-media"&&M.pathname.slice(f.length)!==h.session_token)throw Error("Listener credential mismatch");if(t==="media"&&Vh(h.session_generation))M.searchParams.set("session_generation",h.session_generation);let u=this.transport.mediaURL(M,m);return u.protocol=u.protocol==="https:"?"wss:":"ws:",u.href}session(h,t,m="media",_=Sh()){let M=h;if(!M||typeof M.call_id!=="string"||typeof M.media_url!=="string"||t&&M.call_id!==t||M.session_token!==void 0&&typeof M.session_token!=="string")throw Error("Invalid Telephony session response");if(M.lease_seconds!==void 0&&(!Number.isFinite(M.lease_seconds)||M.lease_seconds<10||M.lease_seconds>3600||!M.session_token))throw Error("Invalid media lease");if(m==="listen-media"&&(!M.session_token||M.lease_seconds===void 0))throw Error("Invalid listener lease");return c(M.call_id),this.resolveMediaURL(M,m),{...M,lease_started_ms:_}}}var C_=m0({app:"telephony",create:({app:h})=>new rh(h)});function a_({app:h},t){return new rh(h,t)}export{a_ as createClient,Et as callTerminationLabel};
