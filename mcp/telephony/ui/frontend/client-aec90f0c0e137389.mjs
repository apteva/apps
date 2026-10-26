function Gh(h){let m=globalThis.crypto?.randomUUID?.()??`${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;return`${h}-${m}`}function Oh(h){return{recovery_id:Gh("recovery"),attempt_id:Gh("attempt"),initiating_action:h}}function $h(h){return typeof h==="string"&&h.length<=80&&/^(browser|session|recovery|attempt)-[a-f0-9-]{1,64}$/.test(h)?h:void 0}function Mh(h){let m=(u,y)=>typeof u==="string"&&u.length<=y&&/^[a-zA-Z0-9_-]*$/.test(u)?u:void 0,t=["WebSocket transport error","WebRTC signaling error","Audio worker failed","WebRTC signaling unavailable"],_=["attach","takeover","automatic_retry","manual_reconnect","audio_device_change","component_recreation"],M=(u,y)=>typeof u==="number"&&Number.isFinite(u)?Math.max(0,Math.min(y,u)):void 0;return{timestamp:Number.isFinite(Date.parse(h.timestamp))?new Date(h.timestamp).toISOString():new Date().toISOString(),action:m(h.action,40)??"diagnostic",outcome:m(h.outcome,40)??"unknown",code:m(h.code,80),detail:h.detail?t.includes(h.detail)?h.detail:"detail_redacted":void 0,recovery_id:$h(h.recovery_id),attempt_id:$h(h.attempt_id),shared_attempt_id:$h(h.shared_attempt_id),session_id:$h(h.session_id),previous_session_id:$h(h.previous_session_id),initiating_action:_.includes(h.initiating_action)?h.initiating_action:void 0,status:M(h.status,599),remaining_ms:M(h.remaining_ms,3600000),was_clean:typeof h.was_clean==="boolean"?h.was_clean:void 0,duration_ms:M(h.duration_ms,86400000)}}var Sh=()=>performance.now();async function Nh(h,m,t=5000,_){let M=new AbortController,f,u=new Promise((S,s)=>{f=s}),y=()=>{M.abort(),f(new DOMException("Media request cancelled","AbortError"))};m?.addEventListener("abort",y,{once:!0});let q=setTimeout(y,t);try{if(m?.aborted)return y(),await u;return await Promise.race([h(M.signal).then((S)=>{if(M.signal.aborted)_?.(S);return S}),u])}finally{clearTimeout(q),m?.removeEventListener("abort",y)}}function th(h){let m=h,t=m?.status,_;try{let f=JSON.parse(m?.body??"{}");if(typeof f.code==="string")_=f.code}catch{}let M=_==="media_lease_expired";return{status:t,code:_,expired:M,denied:!M&&[401,403,404,410].includes(t)}}class Lh{seconds;renew;ended;event;clock;renewTimer;expiryTimer;requestTimer;stopped=!1;retryMS=250;deadline;constructor(h,m,t,_,M=Sh(),f={now:Sh,setTimeout:(u,y)=>setTimeout(u,y),clearTimeout:(u)=>clearTimeout(u)}){this.seconds=h;this.renew=m;this.ended=t;this.event=_;this.clock=f;this.deadline=M+h*1000-1000,this.watchExpiry(),this.schedule(Math.min(h*1000/3,this.remaining()))}remaining(){return Math.max(0,this.deadline-this.clock.now())}report(h,m){let{status:t,code:_}=th(m);try{this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome:h,status:t,code:_,remaining_ms:Math.round(this.remaining())})}catch{}}watchExpiry(){this.clock.clearTimeout(this.expiryTimer),this.expiryTimer=this.clock.setTimeout(()=>this.finish("expired"),this.remaining())}schedule(h){this.renewTimer=this.clock.setTimeout(()=>void this.tick(),Math.max(0,h))}async tick(){if(this.stopped)return;if(!this.remaining()){this.finish("expired");return}let h=this.clock.now();try{let m=await Promise.race([this.renew(),new Promise((t,_)=>{this.requestTimer=this.clock.setTimeout(()=>_(Error("Media renewal timed out")),Math.min(5000,this.remaining()))})]);if(this.stopped)return;if(m?.lease_seconds!==void 0){if(!Number.isFinite(m.lease_seconds)||m.lease_seconds<10||m.lease_seconds>3600)throw Error("Invalid media renewal lease");this.seconds=m.lease_seconds}this.deadline=h+this.seconds*1000-1000,this.retryMS=250,this.watchExpiry(),this.report("renewed"),this.schedule(Math.min(this.seconds*1000/3,this.remaining()))}catch(m){if(this.stopped)return;let t=th(m);if(t.denied||t.expired){this.finish(t.expired?"expired":"revoked",m);return}this.report("retrying",m),this.schedule(Math.min(this.retryMS,this.remaining())),this.retryMS=Math.min(4000,this.retryMS*2)}finally{this.clock.clearTimeout(this.requestTimer),this.requestTimer=void 0}}finish(h,m){if(this.stopped)return;this.report(h,m),this.stop(),this.ended(h,m)}stop(){this.stopped=!0,this.clock.clearTimeout(this.renewTimer),this.clock.clearTimeout(this.expiryTimer),this.clock.clearTimeout(this.requestTimer)}}function m0(h){if(h.turnTransport!==void 0&&!["all","udp","tcp","tls"].includes(h.turnTransport))throw RangeError("Invalid TURN transport");if(h.iceTransportPolicy!==void 0&&!["all","relay"].includes(h.iceTransportPolicy))throw RangeError("Invalid ICE policy");if(h.turnTLSPort!==void 0&&(!Number.isInteger(h.turnTLSPort)||h.turnTLSPort<1||h.turnTLSPort>65535||h.turnTransport!=="tls"))throw RangeError("TURN TLS port requires TLS transport and a valid port")}function Hh(h){let m=h.ice_transport_policy??"all";if(m!=="all"&&m!=="relay")throw Error("Invalid server ICE policy");let t=h.ice_servers??[];if(!Array.isArray(t)||t.length>8)throw Error("Invalid server ICE configuration");for(let _ of t){let M=typeof _.urls==="string"?[_.urls]:_.urls;if(!Array.isArray(M)||M.length<1||M.length>16||M.some((f)=>typeof f!=="string"||f.length>1024||/[\r\n@#]/.test(f)||!/^stuns?:|^turns?:/.test(f)))throw Error("Invalid server ICE endpoint")}if(m==="relay"&&!t.some((_)=>(typeof _.urls==="string"?[_.urls]:_.urls).some((M)=>/^turns?:/.test(M))))throw Object.assign(Error("Required TURN relay is unavailable"),{code:"turn_relay_unavailable"});return{iceServers:t,iceTransportPolicy:m}}function W0(h,m){m0(m);for(let t of["turn_transport","ice_policy","turn_tls_port"])h.searchParams.delete(t);if(m.turnTransport!==void 0)h.searchParams.set("turn_transport",m.turnTransport);if(m.iceTransportPolicy!==void 0)h.searchParams.set("ice_policy",m.iceTransportPolicy);if(m.turnTLSPort!==void 0)h.searchParams.set("turn_tls_port",String(m.turnTLSPort))}function U0(h){let m={pairID:"",direct:!1,sent:0,received:0,send:!1,receive:!1};if(h.forEach((u)=>{if(u.type==="transport"&&u.selectedCandidatePairId)m.pairID=u.selectedCandidatePairId;if(u.type==="outbound-rtp"&&!u.isRemote)m.sent+=u.packetsSent??0,m.send=!0;if(u.type==="inbound-rtp"&&!u.isRemote)m.received+=u.packetsReceived??0,m.receive=!0}),!m.pairID)h.forEach((u)=>{if(u.type==="candidate-pair"&&u.nominated&&u.state==="succeeded")m.pairID=u.id});let t=h.get(m.pairID),_=t&&h.get(t.localCandidateId),M=t&&h.get(t.remoteCandidateId),f=(u)=>u&&["host","srflx","prflx"].includes(u.candidateType);return m.direct=Boolean(t&&f(_)&&f(M)),m}function c0(h,m){return Boolean(m.pairID&&m.pairID!==h.pairID&&(h.send||h.receive)&&(!h.send||m.sent>h.sent)&&(!h.receive||m.received>h.received))}class Th{send;active;observe;configured;work=Promise.resolve();peers=new Map;revision=1;closed=!1;retryTimer;retries=0;constructor(h,m,t=()=>{},_){this.send=h;this.active=m;this.observe=t;this.configured=_}register(h,m,t=()=>!0,_){this.remove(h);let M=h==="coach";this.peers.set(h,{pc:m,current:t,revision:0,generation:_,cancellation:new AbortController,offerType:M?"coach.webrtc.restart_offer":"webrtc.restart_offer",answerType:M?"coach.webrtc.restart_answer":"webrtc.restart_answer"})}remove(h){let m=this.peers.get(h);if(m?.cancellation.abort(),m?.pending)clearTimeout(m.pending.timer),m.pending.reject(Error("ICE peer replaced"));this.peers.delete(h)}report(h,m){try{this.observe(h,m)}catch{}}alive(h){return!this.closed&&this.active()&&(!h||h.current()&&h.pc.connectionState!=="closed")}handle(h){if(!["webrtc.ice_config","webrtc.restart_answer","coach.webrtc.restart_answer","webrtc.ice_status"].includes(h.type))return!1;if(!this.alive())return!0;if(h.type==="webrtc.ice_status"){if(this.report(["credential_refresh_unavailable","restart_failed"].includes(h.state)?h.state:"ice_status"),h.state==="restart_failed"){for(let t of this.peers.values())if(t.generation===h.generation&&t.pending&&t.pending.revision===h.revision)clearTimeout(t.pending.timer),t.pending.reject(Error("ICE restart rejected")),t.pending=void 0}return!0}if(h.type.endsWith("restart_answer")){for(let t of this.peers.values()){if(h.type!==t.answerType||t.generation!==h.generation||t.pending?.revision!==h.revision||!this.alive(t))continue;let _=t.pending;if(!_)continue;t.pc.setRemoteDescription({type:"answer",sdp:h.sdp}).then(()=>{if(t.pending!==_)return;if(clearTimeout(_.timer),t.pending=void 0,this.alive(t))_.resolve();else _.reject(Error("ICE session replaced"))}).catch(()=>{if(t.pending===_)clearTimeout(_.timer),t.pending=void 0,_.reject(Error("ICE answer rejected"))})}return!0}if(!Number.isSafeInteger(h.revision)||h.revision<=this.revision)return!0;let m;try{if(m=Hh(h),!Number.isFinite(h.expires_at)||h.expires_at*1000<=Date.now()+1000)throw Error()}catch{return this.report("invalid_ice_refresh"),!0}this.revision=h.revision;try{this.configured?.(m)}catch{}return this.report("credentials_refreshed",{code:`ice_rev_${h.revision}`,remaining_ms:Math.max(0,Math.min(3600000,Math.round(h.expires_at*1000-Date.now())))}),this.work=this.work.then(async()=>{if(!this.alive()||this.revision!==h.revision)return;let t=await Promise.allSettled([...this.peers.values()].filter((_)=>this.alive(_)).map((_)=>this.restart(_,m,h.revision)));if(!this.alive()||this.revision!==h.revision)return;if(t.some((_)=>_.status==="rejected"))this.retry();else{if(this.retries=0,this.retryTimer)clearTimeout(this.retryTimer);this.retryTimer=void 0}}).catch(()=>{this.report("restart_failed"),this.retry()}),!0}mediaStats(h,m){return new Promise((t,_)=>{let M=()=>{clearTimeout(u),h.cancellation.signal.removeEventListener("abort",f)},f=()=>{M(),_(Error("ICE peer removed"))},u=setTimeout(()=>{M(),_(Error("ICE media statistics timed out"))},1500);if(h.cancellation.signal.addEventListener("abort",f,{once:!0}),!m()){f();return}Promise.resolve().then(()=>h.pc.getStats()).then((y)=>{M(),m()?t(y):_(Error("ICE peer replaced"))},()=>{M(),_(Error("ICE media statistics unavailable"))})})}retry(){if(this.retryTimer||this.retries>=3||!this.alive())return;this.retryTimer=setTimeout(()=>{if(this.retryTimer=void 0,this.alive()){this.retries++;try{this.send({type:"webrtc.ice_refresh"})}catch{this.report("refresh_request_failed")}}},15000)}async restart(h,m,t){if(h.pending)clearTimeout(h.pending.timer),h.pending.reject(Error("ICE revision superseded")),h.pending=void 0;let _=()=>this.alive(h)&&this.peersHas(h);if(this.revision!==t||!_())return;let M=Gh("attempt"),f=performance.now(),u=()=>({attempt_id:M,code:`ice_rev_${t}`,duration_ms:Math.round(performance.now()-f)});try{if(await new Promise((s,b)=>{if(h.pc.signalingState==="stable"){s();return}let A=()=>{clearTimeout(g),h.pc.removeEventListener("signalingstatechange",x),h.cancellation.signal.removeEventListener("abort",R)},x=()=>{if(!_()||h.pc.signalingState==="stable")A(),_()?s():b(Error("ICE peer replaced"))},R=()=>{A(),b(Error("ICE peer removed"))},g=setTimeout(()=>{A(),b(Error("ICE signaling busy"))},1e4);h.pc.addEventListener("signalingstatechange",x),h.cancellation.signal.addEventListener("abort",R,{once:!0}),x()}),!_())return;h.pc.setConfiguration({...h.pc.getConfiguration(),...m});let y=U0(await this.mediaStats(h,_));if(!_())return;if(m.iceTransportPolicy!=="relay"&&h.pc.connectionState==="connected"&&y.direct){h.revision=t,this.report("credentials_applied_direct",u());return}let q=await h.pc.createOffer({iceRestart:!0});if(!_())return;if(await h.pc.setLocalDescription(q),!_())return;if(await new Promise((s,b)=>{if(h.pc.iceGatheringState==="complete"){s();return}let A=()=>{clearTimeout(g),h.pc.removeEventListener("icegatheringstatechange",x),h.cancellation.signal.removeEventListener("abort",R)},x=()=>{if(!_()||h.pc.iceGatheringState==="complete")A(),_()?s():b(Error("ICE session replaced"))},R=()=>{A(),b(Error("ICE session stopped"))},g=setTimeout(()=>{A(),b(Error("ICE gathering timed out"))},7000);h.cancellation.signal.addEventListener("abort",R,{once:!0}),h.pc.addEventListener("icegatheringstatechange",x),x()}),!_())return;if(this.report("restart_started",u()),await new Promise((s,b)=>{let A=setTimeout(()=>{if(h.pending?.revision===t)h.pending=void 0;b(Error("ICE restart timed out"))},1e4);h.pending={revision:t,resolve:s,reject:b,timer:A};try{this.send({type:h.offerType,revision:t,generation:h.generation,sdp:h.pc.localDescription.sdp})}catch{clearTimeout(A),h.pending=void 0,b(Error("ICE signaling unavailable"))}}),_())this.report("restart_signaled",u());let S=performance.now()+1e4;while(_()){let s=U0(await this.mediaStats(h,_));if(!_())return;if(h.pc.connectionState==="connected"&&c0(y,s)){h.revision=t,this.report("restart_completed",u());return}if(performance.now()>=S)throw Error("ICE media did not resume");await new Promise((b,A)=>{let x=()=>{clearTimeout(g),h.cancellation.signal.removeEventListener("abort",R)},R=()=>{x(),A(Error("ICE peer removed"))},g=setTimeout(()=>{x(),b()},100);if(h.cancellation.signal.addEventListener("abort",R,{once:!0}),!_())R()})}}catch(y){if(!_())return;if(_()&&h.pc.signalingState==="have-local-offer")try{await h.pc.setLocalDescription({type:"rollback"})}catch{}if(_())this.report("restart_failed",u());throw y}}peersHas(h){return[...this.peers.values()].includes(h)}stop(){if(this.closed)return;if(this.closed=!0,this.retryTimer)clearTimeout(this.retryTimer);this.retryTimer=void 0;for(let h of[...this.peers.keys()])this.remove(h)}}function V0(){return new DOMException("Network check cancelled","AbortError")}function r(h,m){return new Promise((t,_)=>{let M=()=>{f(),_(V0())},f=()=>m.removeEventListener("abort",M);if(m.addEventListener("abort",M,{once:!0}),h.then((u)=>{f(),m.aborted?_(V0()):t(u)},(u)=>{f(),_(u)}),m.aborted)M()})}class $0{seen=new Set;highest=-1;lastAt;lastSource;uploadCount=0;uploadHighest=-1;upstreamJitter=0;jitter=0;rtts=[];lastEcho=0;sent=0;browserPauseMs=0;lastSend;sentAt(h){if(this.lastSend!==void 0)this.browserPauseMs=Math.max(this.browserPauseMs,h-this.lastSend-20);this.lastSend=h,this.sent++}receive(h,m){if(h.byteLength!==96)return;let t=new DataView(h);if(t.getUint32(0,!0)!==827346516)return;let _=t.getUint32(4,!0);if(_>=1000||this.seen.has(_))return;this.seen.add(_),this.highest=Math.max(this.highest,_);let M=Number(t.getBigUint64(8,!0))/1000;if(this.lastAt!==void 0&&this.lastSource!==void 0){let u=Math.abs(m-this.lastAt-(M-this.lastSource));this.jitter+=(u-this.jitter)/16}this.lastAt=m,this.lastSource=M,this.uploadCount=Math.max(this.uploadCount,t.getUint32(16,!0)),this.uploadHighest=Math.max(this.uploadHighest,t.getUint32(20,!0)),this.upstreamJitter=t.getUint32(32,!0)/1000;let f=Number(t.getBigUint64(24,!0))/1000;if(f>this.lastEcho&&f<=m){if(this.lastEcho=f,this.rtts.push(m-f),this.rtts.length>300)this.rtts.shift()}}result(h,m,t){let _=this.uploadCount>0?this.uploadHighest+1:0,M=this.highest+1,f=_>0?Math.max(0,(_-this.uploadCount)/_*100):0,u=M>0?Math.max(0,(M-this.seen.size)/M*100):0,y=this.rtts.slice().sort((A,x)=>A-x),q=y.length?y[Math.floor((y.length-1)*0.95)]:void 0,S=Math.max(this.jitter,this.upstreamJitter),s=this.browserPauseMs>100?"browser_busy":this.seen.size<100||this.uploadCount<100?"insufficient_samples":void 0,b=s?"inconclusive":Math.max(f,u)>=3||S>30||(q??0)>300?"degraded":"ready";return{path:h,state:b,setupMs:m,durationMs:t,sent:this.sent,received:this.seen.size,uploadLossPct:f,downloadLossPct:u,rttMs:q,jitterMs:S,browserPauseMs:this.browserPauseMs,reason:s}}}async function p0(h,m,t){let _=performance.now(),M,f,u,y=()=>{if(u)clearInterval(u);f?.close()};t.addEventListener("abort",y,{once:!0});try{let q=h({action:"prepare",path:m},t).then((U)=>{if(t.aborted&&typeof U?.id==="string")h({action:"cancel",id:U.id},AbortSignal.timeout(1000)).catch(()=>{});return U}),S=await r(q,t);if(M=S.id,typeof M!=="string"||!/^session-[a-f0-9-]{1,64}$/.test(M)||S.packet_bytes!==96||S.interval_ms!==20)throw Error("Invalid network test configuration");f=new RTCPeerConnection(Hh(S));let s=f.createDataChannel("telephony-network-check",{ordered:!1,maxRetransmits:0});s.binaryType="arraybuffer";let b=new $0;s.onmessage=(U)=>{if(U.data instanceof ArrayBuffer)b.receive(U.data,performance.now())};let A=new Promise((U,Y)=>{s.onopen=()=>U(),s.onerror=()=>Y(Error("Network test connection failed"))}),x=await r(f.createOffer(),t);if(await r(f.setLocalDescription(x),t),f.iceGatheringState!=="complete")await r(new Promise((U)=>{f.onicegatheringstatechange=()=>{if(f.iceGatheringState==="complete")U()}}),t);let R=await r(h({action:"offer",id:M,sdp:f.localDescription.sdp},t),t);await r(f.setRemoteDescription({type:"answer",sdp:R.sdp}),t),await r(A,t);let g=performance.now()-_,F=performance.now(),G=0;u=setInterval(()=>{if(t.aborted||s.readyState!=="open"||s.bufferedAmount>384||G>=400)return;let U=new ArrayBuffer(96),Y=new DataView(U),o=performance.now();Y.setUint32(0,827346516,!0),Y.setUint32(4,G++,!0),Y.setBigUint64(8,BigInt(Math.round(o*1000)),!0);try{s.send(U),b.sentAt(o)}catch{}},20),await r(new Promise((U)=>{let Y=setTimeout(U,6000);t.addEventListener("abort",()=>clearTimeout(Y),{once:!0})}),t);let C=b.result(m,g,performance.now()-F),E=await r(f.getStats(),t),Q="";E.forEach((U)=>{if(U.type==="transport")Q=U.selectedCandidatePairId??""});let k=E.get(Q),N=k&&E.get(k.localCandidateId);return C.candidateType=N?.candidateType,C.relayProtocol=N?.relayProtocol,C}finally{if(t.removeEventListener("abort",y),y(),M){let q=h({action:"cancel",id:M},AbortSignal.timeout(1000)).catch(()=>{});if(!t.aborted)await r(q,AbortSignal.timeout(1000)).catch(()=>{})}}}async function N0(h,m={}){let t=m.paths??["configured"];if(t.length<1||t.length>3||new Set(t).size!==t.length||t.some((f)=>!["configured","direct","relay"].includes(f)))throw Error("Invalid network test paths");let _={state:"unavailable",checkedAt:new Date().toISOString(),paths:[]};for(let f of t){if(m.signal?.aborted)return _.state="cancelled",_;let u=new AbortController,y=()=>u.abort();m.signal?.addEventListener("abort",y,{once:!0});let q=setTimeout(y,18000);try{_.paths.push(await p0(h,f,u.signal))}catch{if(m.signal?.aborted)return _.state="cancelled",_;_.paths.push({path:f,state:"unavailable",setupMs:0,durationMs:0,sent:0,received:0,uploadLossPct:0,downloadLossPct:0,jitterMs:0,browserPauseMs:0,reason:u.signal.aborted?"timeout":"path_unavailable"})}finally{clearTimeout(q),m.signal?.removeEventListener("abort",y)}}let M=_.paths.filter((f)=>f.state==="ready"||f.state==="degraded").sort((f,u)=>(f.uploadLossPct+f.downloadLossPct)*50+f.jitterMs+(f.rttMs??1000)-(u.uploadLossPct+u.downloadLossPct)*50-u.jitterMs-(u.rttMs??1000));if(M.length)_.state=M[0].state,_.recommendedPath=M[0].path;else if(_.paths.some((f)=>f.state==="inconclusive"))_.state="inconclusive";return _}function dh(h){let m=new URL(h),t=["localhost","127.0.0.1","[::1]"].includes(m.hostname);if(m.username||m.password||m.search||m.hash||m.protocol!=="https:"&&!(m.protocol==="http:"&&t)||!m.pathname.endsWith("/runtime-browser"))throw Error("Invalid managed Telephony browser endpoint");return m}function H0(h){if(!h.startsWith("/")||h.startsWith("//")||h.includes("..")||h.includes("\\"))throw Error("Invalid Telephony request path")}function l0(h,m){return new Promise((t)=>{let _=()=>{clearTimeout(M),h.removeEventListener("abort",_),t()},M=setTimeout(_,m);if(h.addEventListener("abort",_,{once:!0}),h.aborted)_()})}class _0{proxy;mode;fetchImpl;direct;info;discovery;credential;issuing;proxyOnly=!1;constructor(h,m="auto",t=globalThis.fetch,_){this.proxy=h;this.mode=m;this.fetchImpl=t;this.direct=_;this.fetchImpl=t.bind(globalThis)}get directBase(){return this.info?.browser_api_base}async prepare(){if(this.mode==="proxy"||this.mode==="auto"&&typeof location>"u")return;if(this.direct){dh(this.direct.baseURL),this.info={managed:!0,browser_api_base:this.direct.baseURL};return}if(this.discovery)return this.discovery;this.discovery=(async()=>{let h;try{h=await this.proxy.get("/runtime-endpoints")}catch(t){if(this.mode==="direct"||![404,405].includes(t.status??0))throw t;this.proxyOnly=!0;return}if(!h?.managed||!h.browser_api_base){if(this.proxyOnly=!0,this.mode==="direct")throw Error("Direct Telephony deployment unavailable");return}if(dh(h.browser_api_base),h.browser_media_base)dh(h.browser_media_base);if(h.browser_signaling_base)dh(h.browser_signaling_base);let m=new URL(this.proxy.mcpURL(),typeof location>"u"?void 0:location.href);if(!["https:","http:"].includes(m.protocol))throw Error("Invalid Telephony management gateway");this.info=h})();try{await this.discovery}catch(h){throw this.discovery=void 0,h}}async grant(){if(this.credential&&this.credential.expires_at*1000>Date.now()+30000)return this.credential;if(this.issuing)return this.issuing;let h=typeof location>"u"?new URL(this.proxy.mcpURL()).origin:location.origin;this.issuing=(async()=>{let m=this.direct?await this.direct.credentialProvider():await this.proxy.post("/runtime-browser-bootstrap",{origin:h});if(!m||typeof m.token!=="string"||!m.token||m.token.length>4096||!Number.isSafeInteger(m.expires_at)||m.expires_at*1000<=Date.now()||m.expires_at*1000>Date.now()+360000||m.endpoint!==this.directBase)throw Error("Invalid managed browser credential");return this.credential=m})();try{return await this.issuing}finally{this.issuing=void 0}}async request(h,m,t,_){if(this.mode!=="direct"&&!this.direct||this.proxyOnly)return h==="GET"?this.proxy.get(m,_):this.proxy.post(m,t,_);if(await this.prepare(),!this.directBase)return h==="GET"?this.proxy.get(m,_):this.proxy.post(m,t,_);return this.requestDirect(h,m,t,_)}async requestDirect(h,m,t,_){H0(m),_?.signal?.throwIfAborted();let M=await this.grant(),f=new URL(this.directBase+m);if(_?.signal?.throwIfAborted(),this.proxy.projectId)f.searchParams.set("project_id",this.proxy.projectId);let u=new Headers(_?.headers);u.set("Authorization","Bearer "+M.token),u.set("Content-Type","application/json");let y=await this.fetchImpl(f,{..._,method:h,headers:u,body:t===void 0?void 0:JSON.stringify(t),credentials:"omit",redirect:"error"});if(!y.ok){if(y.status===403)this.credential=void 0;let q=(await y.text()).slice(0,65536);throw Object.assign(Error(`Telephony HTTP ${y.status}`),{status:y.status,body:q})}return await y.json()}requestMedia(h,m,t){let _=()=>{return t?.signal?.throwIfAborted(),this.directBase&&!this.proxyOnly?this.requestDirect("POST",h,m,t):this.request("POST",h,m,t)};if(this.mode==="auto"&&typeof location<"u"&&!this.proxyOnly)return this.prepare().then(_);return _()}mediaURL(h,m){if(!this.directBase)return h;let t=`/api/apps/telephony/_install/${this.proxy.installId}`,_=m==="webrtc"||m==="auto"?this.info?.browser_signaling_base:this.info?.browser_media_base,M=new URL((_||this.directBase)+h.pathname.slice(t.length));return M.search=h.search,M}subscribe(h,m,t){if(this.mode!=="direct"&&!this.direct||this.proxyOnly)return this.proxy.subscribe(h,m,t);let _=new AbortController,M,f=()=>{_.abort(),M?.close(),t?.signal?.removeEventListener("abort",f)};if(t?.signal?.addEventListener("abort",f,{once:!0}),t?.signal?.aborted)f();return(async()=>{try{if(await this.prepare(),_.signal.aborted)return;if(!this.directBase){M=this.proxy.subscribe(h,m,t);return}H0(h);let u=500;while(!_.signal.aborted){try{let y=await this.grant(),q=new URL(this.directBase+h);if(this.proxy.projectId)q.searchParams.set("project_id",this.proxy.projectId);let S=await this.fetchImpl(q,{headers:{Authorization:"Bearer "+y.token,Accept:"text/event-stream"},credentials:"omit",redirect:"error",signal:_.signal});if(!S.ok||!S.body){if(S.status===403)this.credential=void 0;throw Object.assign(Error(`Telephony stream HTTP ${S.status}`),{status:S.status})}u=500;let s=S.body.getReader(),b=new TextDecoder,A="";try{while(!_.signal.aborted){let{done:x,value:R}=await s.read();if(x)break;if(A+=b.decode(R,{stream:!0}),A=A.replace(/\r\n/g,`
`),A.length>65536)throw Error("Telephony notification frame too large");let g;while((g=A.indexOf(`

`))>=0){let F=A.slice(0,g);A=A.slice(g+2);let G=F.split(`
`).filter((C)=>C.startsWith("data:")).map((C)=>C.slice(5).trimStart()).join(`
`);if(G)try{m(JSON.parse(G))}catch{}}}}finally{await s.cancel().catch(()=>{})}}catch(y){if(_.signal.aborted)return;t?.onError?.(y),u=Math.min(1e4,u*2)}if(!_.signal.aborted)await l0(_.signal,u)}}catch(u){if(!_.signal.aborted)t?.onError?.(u)}})(),{close:f}}}function t0(h){return h}var Xh="0.12.55";var v0=["rtt_ms","queue_ms","target_ms","buffered_bytes","underruns","dropped_ms","capture_sequence_gaps","playback_sequence_gaps","main_thread_max_pause_ms","worker_max_tick_gap_ms","capture_sent_ms","capture_muted_ms","capture_dropped_ms","playback_ingress_ms","playback_transport_dropped_ms","playback_source_dropped_ms","clock_uncertainty_ms","clock_sample_age_ms","stats_errors","stats_duration_ms","send_bitrate_bps","receive_bitrate_bps","receive_gap_ms","capture_age_ms","transit_ms","delivery_excess_ms","server_queue_ms","receiver_concealed_delta_ms","receiver_silent_concealed_delta_ms","receiver_concealed_window_ms","requested_playback_target_ms","requested_upload_bitrate_bps","receiver_ssrc","receiver_loss_delta","receiver_loss_window_ms","remote_receiver_ssrc","remote_receiver_loss_delta","remote_receiver_loss_window_ms","receiver_bytesReceived","receiver_packetsReceived","receiver_packetsLost","receiver_jitter","receiver_packetsDiscarded","receiver_concealedSamples","receiver_silentConcealedSamples","receiver_concealmentEvents","receiver_insertedSamplesForDeceleration","receiver_removedSamplesForAcceleration","receiver_totalSamplesReceived","receiver_audioLevel","receiver_totalAudioEnergy","receiver_totalSamplesDuration","receiver_nackCount","receiver_fecPacketsReceived","receiver_fecPacketsDiscarded","receiver_jitter_buffer_interval_ms","receiver_jitter_target_interval_ms","receiver_jitter_minimum_interval_ms","receiver_processing_interval_ms","receiver_totalProcessingDelay","receiver_totalSamplesDecoded","telemetry_skipped_samples","telemetry_skipped_auxiliary","telemetry_queue_age_ms","receiver_last_packet_age_ms","sender_bytesSent","sender_packetsSent","sender_headerBytesSent","sender_retransmittedPacketsSent","sender_retransmittedBytesSent","sender_nackCount","sender_targetBitrate","sender_send_delay_interval_ms","remote_receiver_report_age_ms","remote_receiver_report_updated","sender_packets_delta","sender_window_ms","remote_receiver_packetsLost","remote_receiver_fractionLost","remote_receiver_jitter","remote_receiver_roundTripTime","remote_receiver_totalRoundTripTime","remote_receiver_roundTripTimeMeasurements","remote_sender_packetsSent","remote_sender_bytesSent","pair_availableOutgoingBitrate","pair_availableIncomingBitrate","pair_currentRoundTripTime","pair_totalRoundTripTime","pair_bytesSent","pair_bytesReceived","pair_requestsSent","pair_requestsReceived","pair_responsesSent","pair_responsesReceived","pair_consentRequestsSent","path_revision","codec_clock_rate","codec_channels"],n0={upload_bitrate_control:["standard","guarded"],upload_bitrate_reason:["standard","initial","observation_reset","loss","bandwidth","sustained_upload_delay","stable_recovery"],receiver_processing:["unavailable","per_decoded_sample"],connection:["connected","reconnecting","closed"],context:["running","suspended","interrupted","closed"],muted:["true","false"],device_muted:["true","false"],track:["live","ended"],ice:["new","checking","connected","completed","disconnected","failed","closed"],dtls:["new","connecting","connected","closed","failed"],pair:["frozen","waiting","in-progress","failed","succeeded"],local_candidate:["host","srflx","prflx","relay"],remote_candidate:["host","srflx","prflx","relay"],protocol:["udp","tcp"],relay_protocol:["udp","tcp","tls"],codec:["audio/opus","audio/PCMU","audio/PCMA","pcm16"]};function M0(h){let m={};for(let t of v0)if(typeof h[t]==="number"&&Number.isFinite(h[t]))m[t]=Math.max(0,Math.min(h[t],1000000000000));return m}function f0(h){let m={};for(let[t,_]of Object.entries(n0))if(typeof h[t]==="string"&&_.includes(h[t]))m[t]=h[t];return m}class Zh{transport;serial=0;lastEmit=-1/0;previousAt;previous;incident;history=[];pending=[];counters={};constructor(h){this.transport=h}observe(h,m,t=performance.now(),_=new Date().toISOString(),M){if(!Number.isFinite(t)||!Number.isFinite(Date.parse(_)))return;if(this.previousAt!==void 0&&t<this.previousAt)this.lastEmit=-1/0,this.previous=void 0,this.counters={},this.incident=void 0;let f=M0(h),u=f0(m),y=!1;for(let S of["underruns","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealmentEvents","path_revision","stats_errors"])if(f[S]!==void 0){if(this.counters[S]!==void 0&&f[S]>this.counters[S])y=!0;this.counters[S]=f[S]}let q={id:`s${++this.serial}`,timestamp:_,transport:this.transport,reason:y?"incident":"periodic",window_ms:Math.min(60000,Math.max(0,typeof M==="number"&&Number.isFinite(M)?M:this.previousAt===void 0?0:t-this.previousAt)),metrics:f,states:u};if(this.previousAt=t,y&&!this.incident)this.incident={sample:q,context:this.previous};if(t-this.lastEmit>=5000){if(this.incident){if(this.incident.context)this.enqueue({...this.incident.context,reason:"incident_context"});this.enqueue(this.incident.sample),this.incident=void 0}else this.enqueue(q);this.lastEmit=t}this.previous=q}enqueue(h){if(this.history.push(h),this.history.length>24)this.history.shift();if(this.pending.push(h),this.pending.length>8)this.pending.shift()}recent(){return this.history.map((h)=>({...h,metrics:{...h.metrics},states:{...h.states}}))}drain(){return this.pending.splice(0,2)}}function X0(h){let m={...h,metrics:{},states:{},part_index:0,part_count:16},t=[],_=m;for(let M of["metrics","states"])for(let[f,u]of Object.entries(h[M])){let y={..._,[M]:{..._[M],[f]:u}};if(JSON.stringify(y).length>768)t.push(_),_={...m,metrics:{},states:{},[M]:{[f]:u}};else _=y}if(t.push(_),t.length>16)return[];return t.map((M,f)=>({...M,part_index:f,part_count:t.length}))}var e0=["receiver_loss_delta","remote_receiver_loss_delta","receiver_concealed_delta_ms","receiver_ssrc","remote_receiver_ssrc","rtt_ms","queue_ms","target_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealedSamples","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_jitter","buffered_bytes","main_thread_max_pause_ms","worker_max_tick_gap_ms","receiver_processing_interval_ms","path_revision","stats_errors"];function r0(h){let m={};for(let t of e0)if(h.metrics[t]!==void 0){if(m[t]=h.metrics[t],X0({...h,metrics:m}).length>2)delete m[t]}return{...h,metrics:m}}class Bh{send;sendReport;now;auxiliary=[];skippedAuxiliary=0;skippedSamples=0;auxiliaryTurn=!1;pending=[];queuedAt=new Map;lastFull=-1/0;timer;report;reportAt=0;static maxAgeMS=12000;constructor(h,m,t=()=>performance.now()){this.send=h;this.sendReport=m;this.now=t}enqueueReport(h){if(this.report!==void 0)this.skippedAuxiliary++;this.report=h,this.reportAt=this.now(),this.start()}enqueueAuxiliary(h){let m=!!h?.diagnostics?.timing?.runtime;if(m){let t=this.auxiliary.length;this.auxiliary=this.auxiliary.filter((_)=>!_.runtime),this.skippedAuxiliary+=t-this.auxiliary.length}if(this.auxiliary.push({report:h,at:this.now(),runtime:m}),this.auxiliary.length>16)this.auxiliary.shift(),this.skippedAuxiliary++;this.start()}start(){if(!this.timer)this.timer=setInterval(()=>this.tick(),250)}discard(h){this.pending=this.pending.filter((m)=>m.id!==h),this.queuedAt.delete(h),this.skippedSamples++}enqueue(h){let m=this.now(),t=[...h].reverse().find((_)=>_.reason==="incident")??h.at(-1);if(this.skippedSamples+=Math.max(0,h.length-(t?1:0)),t){let _=t.reason==="periodic"&&m-this.lastFull>=30000;if(_)this.lastFull=m;let M=_?t:r0(t);M={...M,metrics:{...M.metrics,telemetry_skipped_samples:this.skippedSamples,telemetry_skipped_auxiliary:this.skippedAuxiliary}};let f=X0(M),u=this.pending[0]?.id;for(let y of this.queuedAt.keys())if(y!==u)this.discard(y);if(this.queuedAt.has(M.id))this.discard(M.id);if(f.length)this.pending.push(...f),this.queuedAt.set(M.id,m)}if(this.pending.length)this.start()}tick(){let h=this.now();for(let[m,t]of this.queuedAt)if(h-t>Bh.maxAgeMS)this.discard(m);if(this.auxiliary=this.auxiliary.filter((m)=>{if(h-m.at>Bh.maxAgeMS)return this.skippedAuxiliary++,!1;return!0}),this.report!==void 0&&h-this.reportAt>Bh.maxAgeMS)this.report=void 0,this.skippedAuxiliary++;try{if(this.report!==void 0&&this.sendReport){if(this.sendReport(this.report,Math.max(0,h-this.reportAt)))this.report=void 0}else if(this.auxiliary.length&&this.sendReport&&(!this.pending.length||this.auxiliaryTurn)){if(this.sendReport(this.auxiliary[0].report,Math.max(0,h-this.auxiliary[0].at)))this.auxiliary.shift(),this.auxiliaryTurn=!1}else if(this.pending.length){let m=this.pending[0],t={...m.metrics,telemetry_queue_age_ms:Math.max(0,h-(this.queuedAt.get(m.id)??h))};if(this.send({...m,metrics:t})){if(this.pending.shift(),m.part_index===m.part_count-1)this.queuedAt.delete(m.id);this.auxiliaryTurn=!0}}}catch{}if(!this.pending.length&&!this.auxiliary.length&&this.report===void 0)clearInterval(this.timer),this.timer=void 0}stop(){clearInterval(this.timer),this.timer=void 0,this.pending=[],this.queuedAt.clear(),this.auxiliary=[],this.report=void 0}}class Kh{emit;now;timestamp;lastTick;suspendedAt;contextState;environmentCleanup=[];counters={main_thread_pause_count:0,main_thread_max_pause_ms:0,audio_context_suspend_count:0,audio_context_suspended_ms:0};constructor(h,m=()=>performance.now(),t=()=>new Date().toISOString()){this.emit=h;this.now=m;this.timestamp=t}observeEnvironment(){this.stopEnvironment();let h=(m,t,_)=>{if(!m)return;try{m.addEventListener(t,_),this.environmentCleanup.push(()=>m.removeEventListener(t,_))}catch{}};try{if(typeof document<"u"){h(document,"visibilitychange",()=>this.report("tab_visibility",document.visibilityState));for(let m of["freeze","resume"])h(document,m,()=>this.report("page_lifecycle",m));this.report("tab_visibility",document.visibilityState)}if(typeof window<"u")for(let m of["pagehide","pageshow"])h(window,m,()=>this.report("page_lifecycle",m));if(typeof navigator<"u")h(navigator.mediaDevices,"devicechange",()=>this.report("microphone","devices_changed"))}catch{}}stopEnvironment(){for(let h of this.environmentCleanup.splice(0))try{h()}catch{}}tick(){let h=this.now();if(this.lastTick!==void 0){let m=Math.max(0,h-this.lastTick-1000);if(m>=250)this.counters.main_thread_pause_count++,this.counters.main_thread_max_pause_ms=Math.max(this.counters.main_thread_max_pause_ms,m),this.report("main_thread","scheduling_gap",m)}this.lastTick=h}context(h){if(h===this.contextState)return;this.contextState=h;let m=this.now();if(h==="suspended"||h==="interrupted"){if(this.suspendedAt===void 0)this.suspendedAt=m,this.counters.audio_context_suspend_count++;this.report("audio_context",h)}else{let t;if(this.suspendedAt!==void 0)t=Math.max(0,m-this.suspendedAt),this.counters.audio_context_suspended_ms+=t,this.suspendedAt=void 0;this.report("audio_context",h,t)}}report(h,m,t){try{this.emit({timestamp:this.timestamp(),action:h,outcome:m,duration_ms:t===void 0?void 0:Math.round(t)})}catch{}}}async function p(h,m,t){let _=performance.now(),M=(f)=>{try{m?.({timestamp:new Date().toISOString(),action:h,outcome:f,duration_ms:Math.round(Math.max(0,performance.now()-_))})}catch{}};M("started");try{let f=await t();return M("completed"),f}catch(f){throw M("failed"),f}}var Y0=Object.freeze({FR:{country:"FR",frequencies:[440],cadence:[1.5,3.5]},BE:{country:"BE",frequencies:[425],cadence:[1,3]},CH:{country:"CH",frequencies:[425],cadence:[1,4]},DE:{country:"DE",frequencies:[425],cadence:[1,4]},AT:{country:"AT",frequencies:[425],cadence:[1,5]},NL:{country:"NL",frequencies:[425],cadence:[1,4]},ES:{country:"ES",frequencies:[425],cadence:[1.5,3]},IT:{country:"IT",frequencies:[425],cadence:[1,4]},PT:{country:"PT",frequencies:[425],cadence:[1,5]},GB:{country:"GB",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},IE:{country:"IE",frequencies:[400,450],cadence:[0.4,0.2,0.4,2]},AU:{country:"AU",frequencies:[400,425],cadence:[0.4,0.2,0.4,2]},US:{country:"US",frequencies:[440,480],cadence:[2,4]},CA:{country:"CA",frequencies:[440,480],cadence:[2,4]},JP:{country:"JP",frequencies:[400],cadence:[1,2]}});function ch(h){let m=(h??"FR").trim().toUpperCase();return Y0[m]??Y0.FR}function o0(h,m){let t=[];if(!(h.cadence.reduce((f,u)=>f+u,0)>0)||!(m>0))return t;let M=0;while(M<m)for(let f=0;f<h.cadence.length;f+=2){let u=h.cadence[f]??0,y=h.cadence[f+1]??0;if(M>=m)break;if(u>0)t.push({start:M,end:Math.min(M+u,m)});M+=u+y}return t}function ph(h,m,t,_=0.12){let M=h.createGain();M.gain.value=0,M.connect(m);let f=_/Math.max(1,t.frequencies.length),u=t.frequencies.map((x)=>{let R=h.createOscillator();return R.type="sine",R.frequency.value=x,R.connect(M),R.start(),R}),y=h.currentTime+0.05,q=0,S=0.01,s=()=>{let x=h.currentTime-y+10;if(x<=q)return;for(let R of o0(t,x)){if(R.end<=q)continue;let g=y+Math.max(R.start,q),F=y+R.end;M.gain.setValueAtTime(0,g),M.gain.linearRampToValueAtTime(f,g+S),M.gain.setValueAtTime(f,Math.max(g+S,F-S)),M.gain.linearRampToValueAtTime(0,F)}q=x};s();let b=setInterval(s,4000),A=!1;return()=>{if(A)return;A=!0,clearInterval(b);try{M.gain.cancelScheduledValues(0)}catch{}M.gain.value=0;for(let x of u){try{x.stop()}catch{}x.disconnect()}M.disconnect()}}var qh=24000,Dh=60;async function z0(h,m){try{await h.audioWorklet.addModule(m)}catch(t){throw Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.",{cause:t})}}function hm(h){let m=new Int16Array(h.length);for(let t=0;t<h.length;t++){let _=Math.max(-1,Math.min(1,h[t]));m[t]=_<0?_*32768:_*32767}return m.buffer}class gh{history=new Float32Array(64);phase=0;process(h,m,t){if(m===t)return h;let _=new Float32Array(64+h.length);_.set(this.history),_.set(h,64);let M=m/t,f=Math.min(1,t/m)*0.9,u=[],y=this.phase;for(;y<h.length;y+=M){let q=y+32,S=0,s=0;for(let b=Math.ceil(q-32);b<=Math.floor(q+32);b++){let A=b-q,R=(Math.abs(A)<0.000000001?f:Math.sin(Math.PI*f*A)/(Math.PI*A))*(0.5+0.5*Math.cos(Math.PI*A/32));if(b>=0&&b<_.length)S+=_[b]*R,s+=R}u.push(s?S/s:0)}return this.phase=y-h.length,this.history=_.slice(-64),new Float32Array(u)}}function mm(h){let m=0;for(let t=0;t<h.length;t++)m+=h[t]*h[t];return Math.sqrt(m/Math.max(1,h.length))}function w0(h,m){let t=new Map(h.map((_)=>[_.id,_]));for(let _ of m){let M=t.get(_.id);if(!M||(!M.complete||_.complete)&&(!M.ended_at||!!_.ended_at)&&_.duration_ms>=M.duration_ms)t.set(_.id,{..._})}return[...t.values()].sort((_,M)=>_.started_at.localeCompare(M.started_at)).slice(-100)}function _m(h,m){return h.map((t)=>t.ended_at?t:{...t,ended_at:t.observed_until,end_reason:m,complete:!1})}var fh={echoCancellation:!0,noiseSuppression:!1,autoGainControl:!1,inputGainDB:0,highpassFilter:!0};function e(h){if(h.mediaTransport!==void 0&&!["websocket","webrtc","auto"].includes(h.mediaTransport))throw RangeError("Unsupported softphone media transport");if(m0(h),h.webrtcFec!==void 0&&typeof h.webrtcFec!=="boolean")throw RangeError("webrtcFec must be boolean");let m=h.playbackTargetMs??Dh,t=h.playbackMinMs??Math.min(Dh,m),_=h.playbackMaxMs??280;for(let M of[m,t,_])if(!Number.isFinite(M)||M<40||M>280)throw RangeError("Playback buffers must be between 40 and 280 ms");if(t>m||m>_)throw RangeError("Playback buffers require min <= target <= max");if(h.playbackAdaptive!==void 0&&typeof h.playbackAdaptive!=="boolean")throw RangeError("playbackAdaptive must be boolean");return{initialTargetMs:m,minTargetMs:t,maxTargetMs:_,hardMaxMs:320,adaptiveReserve:h.playbackAdaptive!==!1}}function sh(h){return{...h.inputDeviceId?{deviceId:{exact:h.inputDeviceId}}:{},echoCancellation:h.echoCancellation,noiseSuppression:h.noiseSuppression,autoGainControl:h.autoGainControl}}function J0(h){let m=h.getSettings();return{deviceLabel:h.label||"Default microphone",sampleRate:typeof m.sampleRate==="number"?m.sampleRate:null,channelCount:typeof m.channelCount==="number"?m.channelCount:null,echoCancellation:typeof m.echoCancellation==="boolean"?m.echoCancellation:null,noiseSuppression:typeof m.noiseSuppression==="boolean"?m.noiseSuppression:null,autoGainControl:typeof m.autoGainControl==="boolean"?m.autoGainControl:null}}function Yh(h){if(!Number.isFinite(h)||h<=0)return null;return 20*Math.log10(h)}function tm(h,m,t){let _=new ArrayBuffer(44+m*2),M=new DataView(_),f=(y,q)=>{for(let S=0;S<q.length;S++)M.setUint8(y+S,q.charCodeAt(S))};f(0,"RIFF"),M.setUint32(4,36+m*2,!0),f(8,"WAVE"),f(12,"fmt "),M.setUint32(16,16,!0),M.setUint16(20,1,!0),M.setUint16(22,1,!0),M.setUint32(24,t,!0),M.setUint32(28,t*2,!0),M.setUint16(32,2,!0),M.setUint16(34,16,!0),f(36,"data"),M.setUint32(40,m*2,!0);let u=44;for(let y of h)for(let q=0;q<y.length;q++)M.setInt16(u,y[q],!0),u+=2;return new Blob([_],{type:"audio/wav"})}class u0{onLevel;recordAudio;ctx=null;stream=null;capture=null;sink=null;frames=[];samples=0;activeSquares=0;activeSamples=0;peak=0;postPeak=0;limiterReductionDB=0;settings=null;stopped=!1;resampler=new gh;ensureOpen(){if(this.stopped)throw this.release(),Error("Microphone test cancelled.")}constructor(h,m=!0){this.onLevel=h;this.recordAudio=m}async start(h,m){try{this.stream=await navigator.mediaDevices.getUserMedia({audio:sh(m)}),this.ensureOpen();let t=this.stream.getAudioTracks()[0];if(!t)throw Error("No microphone audio track was returned.");this.settings=J0(t);try{this.ctx=new AudioContext({sampleRate:qh,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.ctx.state==="suspended")await this.ctx.resume();this.ensureOpen(),await z0(this.ctx,h),this.ensureOpen();let _=this.ctx.sampleRate,M=this.ctx.createMediaStreamSource(this.stream);return this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:m.inputGainDB,highpassFilter:m.highpassFilter}}),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),this.capture.port.onmessage=(f)=>{if(this.stopped)return;if(!(f.data instanceof Float32Array)){if(f.data.type==="capture.stats")this.peak=Math.max(this.peak,f.data.pre_peak??0),this.postPeak=Math.max(this.postPeak,f.data.post_peak??0),this.limiterReductionDB=Math.max(this.limiterReductionDB,f.data.limiter_reduction_db??0);return}let u=f.data;if(_!==qh)u=this.resampler.process(u,_,qh);let y=mm(u);if(this.onLevel?.(y),this.recordAudio)this.frames.push(new Int16Array(hm(u)));if(this.samples+=u.length,y>=0.005){for(let q=0;q<u.length;q++)this.activeSquares+=u[q]*u[q];this.activeSamples+=u.length}},M.connect(this.capture),this.settings}catch(t){throw await this.release(),t}}async stop(){this.stopped=!0;let h=this.settings,m=this.frames,t=this.samples,_=this.activeSamples>0?Math.sqrt(this.activeSquares/this.activeSamples):0,M=this.peak;if(await this.release(),!h||t===0)throw Error("No microphone audio was captured.");return{audio:tm(m,t,qh),durationMs:Math.round(t*1000/qh),sampleRate:qh,activeRmsDbfs:Yh(_),peakDbfs:Yh(M),postPeakDbfs:Yh(this.postPeak||M),limiterReductionDb:this.limiterReductionDB,settings:h}}async cancel(){this.stopped=!0,await this.release()}async release(){if(this.capture)this.capture.port.onmessage=null,this.capture.disconnect(),this.capture=null;this.sink?.disconnect(),this.sink=null;for(let h of this.stream?.getTracks()??[])h.stop();if(this.stream=null,this.ctx&&this.ctx.state!=="closed")await this.ctx.close();this.ctx=null,this.onLevel?.(0)}}class y0{callbacks;clientEpoch=crypto.randomUUID();transportTelemetry=new Zh("websocket");transportSender=new Bh((h)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[h]}})}),!0},(h)=>{if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return!1;return this.worker.postMessage({type:"send.telemetry",data:JSON.stringify(h)}),!0});worker=null;ctx=null;stream=null;capture=null;playback=null;sink=null;output=null;muted=!1;closed=!1;micLevel=0;speakerLevel=0;levelTimer=null;pingTimer=null;telemetryTimer=null;runtimeTelemetry=new Kh((h)=>this.recordSessionEvent(h));opened=!1;cancelWorkerStart;microphoneTransportReady=!1;sessionReady=!1;mediaPathReady;mediaRevision=0;callAnswered=!1;playbackHeld=!1;mediaSocketConnected=!1;ringback=null;transportTiming={};playbackTiming={};workerDropEvents=[];playbackDropEvents=[];diagnostics={mediaTransport:"websocket",codec:"pcm16",rttMs:null,queueMs:0,targetMs:Dh,underruns:0,droppedMs:0,maxQueueMs:0,audioContextRate:qh,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:fh.inputGainDB,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(h={}){this.callbacks=h}get isMuted(){return this.muted}async start(h,m,t,_=fh,M){let f=e(_);this.diagnostics.targetMs=f.initialTargetMs,this.callbacks.onState?.("connecting"),this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+Xh.replaceAll(".","_")}),this.runtimeTelemetry.observeEnvironment(),this.runtimeTelemetry.tick(),this.telemetryTimer=setInterval(()=>this.runtimeTelemetry.tick(),1000);try{if(M)this.stream=M.stream,this.ctx=M.context;this.stream=M?.stream??await p("microphone_device",(S)=>this.recordSessionEvent(S),()=>navigator.mediaDevices.getUserMedia({audio:sh(_)})),this.ensureOpen();let u=this.stream.getAudioTracks()[0];if(!u)throw Error("No microphone audio track was returned.");if(u.readyState==="ended")throw Error("Microphone disconnected before audio setup.");u.onmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser.")},u.onunmute=()=>{this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),this.callbacks.onNotice?.("Microphone input restored.")},u.onended=()=>{if(!this.closed)this.fail("Microphone disconnected. Select a microphone and reconnect audio.")};let y=J0(u);if(this.diagnostics={...this.diagnostics,microphoneSampleRate:y.sampleRate??0,microphoneChannelCount:y.channelCount??0,echoCancellation:y.echoCancellation,noiseSuppression:y.noiseSuppression,autoGainControl:y.autoGainControl,micInputGainDb:_.inputGainDB},!this.ctx)try{this.ctx=new AudioContext({sampleRate:qh,latencyHint:"interactive"})}catch{this.ctx=new AudioContext({latencyHint:"interactive"})}if(this.runtimeTelemetry.context(this.ctx.state),this.ctx.state==="suspended")await p("audio_context",(S)=>this.recordSessionEvent(S),()=>this.ctx.resume());if(this.runtimeTelemetry.context(this.ctx.state),this.ensureOpen(),_.outputDeviceId&&"setSinkId"in this.ctx)await this.ctx.setSinkId(_.outputDeviceId),this.ensureOpen();await p("audio_worklet",(S)=>this.recordSessionEvent(S),()=>z0(this.ctx,m)),this.ensureOpen(),this.diagnostics.audioContextRate=this.ctx.sampleRate;let q=this.ctx.createMediaStreamSource(this.stream);this.capture=new AudioWorkletNode(this.ctx,"softphone-capture",{processorOptions:{inputGainDB:_.inputGainDB,highpassFilter:_.highpassFilter}}),this.playback=new AudioWorkletNode(this.ctx,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:{...f,telemetryEpoch:this.clientEpoch,telemetryEnabled:!1}}),this.capture.port.postMessage({type:"muted",value:this.muted}),this.installWorkletDiagnostics(),this.sink=this.ctx.createGain(),this.sink.gain.value=0,this.capture.connect(this.sink).connect(this.ctx.destination),q.connect(this.capture),this.output=this.ctx.createGain(),this.output.gain.value=_.outputVolume??1,this.playback.connect(this.output).connect(this.ctx.destination),await p("audio_transport",(S)=>this.recordSessionEvent(S),()=>this.openWorker(h,t)),this.ensureOpen(),this.capture.onprocessorerror=this.playback.onprocessorerror=()=>this.fail("Audio processing stopped. Reconnect audio."),this.runtimeTelemetry.context(this.ctx.state),this.ctx.onstatechange=()=>{if(this.closed)return;if(this.runtimeTelemetry.context(this.ctx?.state??"closed"),this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state!=="running"}),this.setPlaybackObservation(this.ctx?.state==="running"&&this.microphoneTransportReady&&!this.playbackHeld,"audio_context_paused"),this.ctx?.state==="suspended"||this.ctx?.state==="interrupted")this.callbacks.onState?.("reconnecting","Browser paused audio. Reconnect audio to continue.");else if(this.ctx?.state==="running")this.updateLiveState()},this.levelTimer=setInterval(()=>{this.callbacks.onLevels?.(this.micLevel,this.speakerLevel),this.micLevel*=0.65,this.speakerLevel*=0.65},100)}catch(u){if(this.closed)this.teardown();if(!this.closed)this.fail(u instanceof Error?u.message:"browser audio setup failed");throw u}}ensureOpen(){if(this.closed)throw this.teardown(),Error("Audio session was cancelled.")}async resumeAudio(){await this.ctx?.resume(),this.updateLiveState()}setOutputVolume(h){if(this.output)this.output.gain.value=Math.max(0,Math.min(1,h))}sendDTMF(h){if(/^[0-9*#]+$/.test(h))this.sendText(JSON.stringify({type:"dtmf",digits:h}))}startRingback(h){if(this.closed||!this.ctx||this.ringback)return;this.ringback=ph(this.ctx,this.ctx.destination,ch(h))}stopRingback(){this.ringback?.(),this.ringback=null}installWorkletDiagnostics(){if(!this.capture||!this.playback)return;this.capture.port.onmessage=(h)=>{let m=h.data;if(m?.type!=="capture.stats")return;this.micLevel=this.muted?0:m.active_rms??0,this.diagnostics={...this.diagnostics,micActiveRmsDbfs:Yh(m.active_rms??0),micPeakDbfs:Yh(m.pre_peak??0),micPostPeakDbfs:Yh(m.post_peak??0),micInputGainDb:m.input_gain_db??this.diagnostics.micInputGainDb,micLimiterReductionDb:m.limiter_reduction_db??0}},this.playback.port.onmessage=(h)=>{let m=h.data;if(m?.type==="playback.underrun"){this.diagnostics.underrunEvents=w0(this.diagnostics.underrunEvents??[],[m.event]),this.diagnostics.underrunDurationMs=m.underrun_ms,this.diagnostics.underruns=m.underruns;return}if(m?.type!=="stats")return;this.diagnostics.playbackEvents=m.playback_events,this.playbackTiming={reserve_expanded_ms:m.reserve_expanded_ms,reserve_compressed_ms:m.reserve_compressed_ms,reserve_adjustments:m.reserve_adjustments,reserve_match_rejections:m.reserve_match_rejections,played_ms:m.played_ms,max_residence_ms:m.max_residence_ms,drop_totals_ms:m.drop_totals_ms,coaching:{played_ms:m.whisper_played_ms,dropped_ms:m.whisper_dropped_ms,max_queue_ms:m.whisper_max_queue_ms}},this.speakerLevel=Math.max(this.speakerLevel,m.speaker_level??0),this.diagnostics={...this.diagnostics,coachingPlayedMs:m.whisper_played_ms??0,coachingDroppedMs:m.whisper_dropped_ms??0,coachingMaxQueueMs:m.whisper_max_queue_ms??0,queueMs:m.queue_ms??0,targetMs:m.target_ms??Dh,underruns:m.underruns??0,droppedMs:m.dropped_ms??0,underrunDurationMs:m.underrun_ms??0,underrunEvents:w0(this.diagnostics.underrunEvents??[],m.underrun_events??[]),maxQueueMs:m.max_queue_ms??0,playbackSequenceGaps:m.playback_sequence_gaps??0,dropEvents:this.mergeDropEvents(void 0,m.drop_events??[])},this.callbacks.onDiagnostics?.({...this.diagnostics})}}openWorker(h,m){return new Promise((t,_)=>{let M=new Worker(m);this.worker=M;let f=new MessageChannel,u=new MessageChannel;this.capture?.port.postMessage({type:"transport",port:f.port1},[f.port1]),this.playback?.port.postMessage({type:"transport",port:u.port1},[u.port1]);let y=!1,q=setTimeout(()=>{if(y)return;y=!0,_(Error("audio connection timed out"))},1e4),S=(s)=>{if(y)return;if(y=!0,clearTimeout(q),s)_(s);else t()};this.cancelWorkerStart=()=>S(Error("audio session closed")),M.onmessage=(s)=>{if(this.closed){S(Error("audio session closed"));return}let b=s.data;if(b?.type==="socket.open")this.reportedPlaybackIDs.clear(),this.reportedCaptureQueueIDs.clear(),this.opened=!0,this.mediaSocketConnected=!0,this.mediaPathReady=void 0,this.mediaRevision=0,this.sessionReady=!1,this.startRTTProbe(),S();else if(b?.type==="runtime.event")this.recordSessionEvent(b.event);else if(b?.type==="socket.message")this.handleControl(b.data);else if(b?.type==="socket.reconnect"){if(this.callbacks.refreshMediaURL)this.callbacks.refreshMediaURL(b.recovery).then((A)=>{if(!this.closed&&this.worker===M)M.postMessage({type:"socket.credentials",id:b.id,mediaURL:A})},(A)=>{if(this.closed||this.worker!==M)return;let x=th(A);this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:x.denied?"revoked":"retrying",...b.recovery,status:x.status,code:x.code}),M.postMessage({type:"socket.credentials",id:b.id,denied:x.denied})})}else if(b?.type==="socket.error")this.recordSessionEvent({timestamp:b.timestamp,action:"websocket",outcome:"transport_error"});else if(b?.type==="socket.close")if(this.recordSessionEvent({timestamp:b.timestamp,action:"websocket",outcome:"closed",code:String(b.code),detail:b.reason,was_clean:b.wasClean}),this.stopRTTProbe(),this.mediaSocketConnected=!1,this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"transport_disconnected"),this.opened&&!this.closed)this.callbacks.onState?.("reconnecting","Connection interrupted; retrying…");else S(Error("audio connection closed before it was ready"));else if(b?.type==="socket.failed")this.mediaSocketConnected=!1,S(Error(b.detail||"audio connection lost")),this.fail(b.detail||"audio connection lost",b.recoverable===!0);else if(b?.type==="transport.drop"&&b.event)this.diagnostics.dropEvents=this.mergeDropEvents(b.event);else if(b?.type==="transport.stats"){if(this.diagnostics.websocketBufferedBytes=b.buffered_bytes??0,this.diagnostics.captureQueueEvents=b.capture_queue_events,this.transportTiming=b.timing??{},b.observation)this.transportTelemetry.observe({...this.transportTiming,...b.observation,rtt_ms:b.timing?.rtt_ms,queue_ms:this.diagnostics.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:b.buffered_bytes,underruns:this.diagnostics.underruns,dropped_ms:this.diagnostics.droppedMs,capture_sequence_gaps:this.diagnostics.captureSequenceGaps,playback_sequence_gaps:this.diagnostics.playbackSequenceGaps,main_thread_max_pause_ms:this.runtimeTelemetry.counters.main_thread_max_pause_ms},{connection:this.mediaSocketConnected?"connected":"reconnecting",context:this.ctx?.state,muted:String(this.muted),device_muted:String(this.stream?.getAudioTracks()[0]?.muted),track:this.stream?.getAudioTracks()[0]?.readyState,codec:"pcm16"},performance.now(),b.observation?.timestamp,b.observation?.window_ms);if(this.diagnostics.transportSamples=this.transportTelemetry.recent(),typeof b.timing?.rtt_ms==="number")this.diagnostics.rttMs=Math.round(b.timing.rtt_ms)}},M.onerror=(s)=>{if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:s.message?.slice(0,160)}),S(Error("audio worker failed")),!this.closed)this.fail("Audio worker failed. Reconnect audio.")},M.postMessage({type:"init",refreshCredentials:Boolean(this.callbacks.refreshMediaURL),audioClockMS:(this.ctx?.currentTime??0)*1000,monotonicEpochMS:performance.timeOrigin+performance.now(),mediaURL:h,contextRate:this.ctx?.sampleRate??qh,muted:this.muted,capturePort:f.port2,playbackPort:u.port2},[f.port2,u.port2])})}mergeDropEvents(h,m){if(h)this.workerDropEvents=[...this.workerDropEvents,h].slice(-100);if(m)this.playbackDropEvents=m.slice(-100);return[...this.workerDropEvents,...this.playbackDropEvents].sort((t,_)=>t.timestamp.localeCompare(_.timestamp)).slice(-100)}carrierDeliveryStalled=!1;deliveryDegradedNotice=!1;shutdownIntent="session_cleanup";reportedPlaybackIDs=new Set;reportedCaptureQueueIDs=new Set;handleControl(h){if(this.closed)return;try{let m=JSON.parse(h);if(m.type==="media.ready"||m.type==="media.not_ready"){let t=m;if(!Number.isSafeInteger(t.revision)||t.revision<=this.mediaRevision)return;this.mediaRevision=t.revision,this.mediaPathReady=t.type==="media.ready";try{this.callbacks.onMediaReadiness?.(t)}catch{}if(!this.mediaPathReady&&this.callAnswered)this.callbacks.onState?.("reconnecting","Connecting carrier audio…");this.updateLiveState()}else if(m.type==="ready")this.sessionReady=!0,this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState();else if(m.type==="dtmf.error"||m.type==="dtmf.sent")this.callbacks.onNotice?.(m.type==="dtmf.sent"?"Keypad tone sent":m.detail||"Keypad tone failed");else if(m.type==="pong"){if(this.diagnostics.captureSequenceGaps=m.capture_sequence_gaps??this.diagnostics.captureSequenceGaps,typeof m.nonce==="number"&&m.nonce>=0)this.diagnostics.rttMs=Math.max(0,Math.round(performance.now()-m.nonce));this.callbacks.onDiagnostics?.({...this.diagnostics})}else if(m.type==="call.ended"||m.type==="session.replaced"){this.setPlaybackObservation(!1,m.type==="call.ended"?"call_ended":"observation_ended"),this.shutdownIntent=m.type==="call.ended"?"call_ended":"session_replaced",this.closed=!0;try{this.callbacks.onState?.("ended",m.type)}finally{this.teardown()}}else if(m.type==="call.status"&&typeof m.call_id==="string"&&typeof m.status==="string"){if(m.hold_state)this.playbackHeld=m.hold_state!=="active",this.setPlaybackObservation(!this.playbackHeld&&this.microphoneTransportReady&&this.ctx?.state==="running",this.playbackHeld?"hold":"observation_resumed");if(m.hold_state&&m.hold_state!=="active")this.worker?.postMessage({type:"flush"});this.callAnswered=["answered","in-progress"].includes(m.status),this.updateLiveState(),this.callbacks.onCallStatus?.(m)}else if(m.type==="call.error")this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:m.detail?.slice(0,160)}),this.fail(m.detail||"The call could not be connected.");else if(m.type==="coach.state")this.callbacks.onNotice?.(m.talking?"Private coaching connected. Only you hear the supervisor.":"Private coaching stopped.");else if(m.type==="audio.health"){let t=m;if(t.state!=="healthy"&&t.state!=="audio_degraded")return;if(this.diagnostics.audioHealth={state:t.state,reason:t.reason,stages:t.stages},this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth),this.callbacks.onDiagnostics?.({...this.diagnostics}),t.state==="audio_degraded"&&!this.deliveryDegradedNotice)this.deliveryDegradedNotice=!0,this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");else if(this.deliveryDegradedNotice&&t.state==="healthy"&&t.stages?.telephony_to_browser?.state==="healthy"&&this.ctx?.state==="running")this.deliveryDegradedNotice=!1,this.callbacks.onNotice?.("Speech delivery restored.")}else if(m.type==="media.delivery"){let t=m.state;if(t==="stalled")this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");else if(t==="flowing"&&this.carrierDeliveryStalled)this.callbacks.onNotice?.("Caller audio delivery restored.");this.carrierDeliveryStalled=t==="stalled"}else if(m.type==="peer.disconnected")this.microphoneTransportReady=!1,this.setPlaybackObservation(!1,"carrier_disconnected"),this.worker?.postMessage({type:"flush"}),this.callbacks.onState?.("reconnecting","Carrier audio interrupted; reconnecting…");else if(m.type==="peer.connected")this.microphoneTransportReady=!0,this.setPlaybackObservation(this.ctx?.state==="running"&&!this.playbackHeld,"carrier_connected"),this.worker?.postMessage({type:"microphone.ready",value:!0}),this.updateLiveState()}catch{}}updateLiveState(){if(this.sessionReady&&this.callAnswered&&this.microphoneTransportReady&&this.mediaPathReady!==!1&&this.ctx?.state==="running")this.callbacks.onState?.("live")}startRTTProbe(){this.stopRTTProbe();let h=()=>{this.sendDiagnostics()};h(),this.pingTimer=setInterval(h,5000)}sendText(h){this.worker?.postMessage({type:"send.text",data:h})}recordSessionEvent(h){try{h={...this.callbacks.sessionDiagnostics?.(),...h}}catch{}h=Mh(h),this.diagnostics.sessionEvents=[...this.diagnostics.sessionEvents??[],h].slice(-50);try{this.callbacks.onSessionEvent?.(h)}catch{}try{let m={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,session_events:[h]}};if(new TextEncoder().encode(JSON.stringify(m)).byteLength<=1100)this.transportSender.enqueueAuxiliary(m);else this.transportSender.skippedAuxiliary++}catch{}}sendDiagnostics(){let h=this.diagnostics,m=(M,f)=>{let u=(M??[]).slice(-64),y=u.filter((q)=>!f.has(q.id)).slice(0,8);if(this.mediaSocketConnected){let q=new Set(u.map((S)=>S.id));for(let S of f)if(!q.has(S))f.delete(S);for(let S of y)f.add(S.id)}return y},t=m(h.playbackEvents,this.reportedPlaybackIDs),_=m(h.captureQueueEvents,this.reportedCaptureQueueIDs);if(this.sendText(JSON.stringify({type:"diagnostics",diagnostics:{media_transport:"websocket",codec:"pcm16",client_version:Xh,client_epoch:this.clientEpoch,playback_events:t,capture_queue_events:_,timing:{transport:this.transportTiming,playback:this.playbackTiming,runtime:this.runtimeTelemetry.counters},connection_state:this.mediaSocketConnected?"connected":this.closed?"closed":"reconnecting",carrier_peer_connected:this.microphoneTransportReady,audio_context_state:this.ctx?.state??"closed",microphone_muted:this.muted,microphone_track_state:this.stream?.getAudioTracks()[0]?.readyState??"ended",microphone_device_muted:this.stream?.getAudioTracks()[0]?.muted??!1,rtt_ms:h.rttMs,playback_queue_ms:h.queueMs,playback_target_ms:h.targetMs,playback_max_queue_ms:h.maxQueueMs,playback_underruns:h.underruns,playback_underrun_ms:h.underrunDurationMs,playback_underrun_events:h.underrunEvents,playback_dropped_ms:h.droppedMs,websocket_buffered_bytes:h.websocketBufferedBytes,audio_context_rate:h.audioContextRate,microphone_sample_rate:h.microphoneSampleRate,microphone_channel_count:h.microphoneChannelCount,echo_cancellation:h.echoCancellation,noise_suppression:h.noiseSuppression,auto_gain_control:h.autoGainControl,mic_active_rms_dbfs:h.micActiveRmsDbfs,mic_peak_dbfs:h.micPeakDbfs,mic_post_peak_dbfs:h.micPostPeakDbfs,mic_input_gain_db:h.micInputGainDb,mic_limiter_reduction_db:h.micLimiterReductionDb,capture_sequence_gaps:h.captureSequenceGaps,playback_sequence_gaps:h.playbackSequenceGaps,drop_events:h.dropEvents}})),this.mediaSocketConnected)this.transportSender.enqueue(this.transportTelemetry.drain());this.callbacks.onDiagnostics?.({...h})}stopRTTProbe(){if(this.pingTimer!==null)clearInterval(this.pingTimer);this.pingTimer=null}setPlaybackObservation(h,m){try{if(this.playback?.port.postMessage({type:"playback.telemetry.boundary",active:h,reason:m,audio_time_ms:(this.ctx?.currentTime??0)*1000}),!h)this.diagnostics.underrunEvents=_m(this.diagnostics.underrunEvents??[],m)}catch{}}setMuted(h){let m=this.muted!==h;if(this.muted=h,this.worker?.postMessage({type:"muted",value:h}),this.capture?.port.postMessage({type:"muted",value:h}),h)this.sendText(JSON.stringify({type:"interrupt"}));if(m)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:h?"muted":"unmuted"})}stop(){this.shutdownIntent="user_stop";let h=!this.closed;this.closed=!0;try{if(h)this.callbacks.onState?.("ended")}finally{this.teardown()}}fail(h,m=!1){if(this.shutdownIntent="audio_error",!this.closed)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:h.slice(0,160)});this.closed=!0;try{this.callbacks.onState?.("error",h,{recoverable:m})}finally{this.teardown()}}teardown(){this.runtimeTelemetry.stopEnvironment(),this.transportSender.stop(),this.setPlaybackObservation(!1,"observation_ended"),this.stopRingback(),this.microphoneTransportReady=!1,this.sessionReady=this.callAnswered=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.cancelWorkerStart?.(),this.cancelWorkerStart=void 0;try{this.sendDiagnostics()}catch{}if(this.stopRTTProbe(),this.telemetryTimer!==null)clearInterval(this.telemetryTimer);if(this.telemetryTimer=null,this.ctx)this.ctx.onstatechange=null,this.runtimeTelemetry.context("closed");if(this.levelTimer!==null)clearInterval(this.levelTimer);this.levelTimer=null;let h=this.worker;if(h?.postMessage({type:"close",reason:this.shutdownIntent}),h){let m=setTimeout(()=>h.terminate(),600);h.onmessage=(t)=>{if(t.data?.type==="socket.shutdown.complete")clearTimeout(m),h.terminate()}}this.worker=null,this.capture?.disconnect(),this.playback?.disconnect(),this.sink?.disconnect(),this.output?.disconnect(),this.output=null,this.stream?.getTracks().forEach((m)=>m.stop()),this.stream=null,this.ctx?.close().catch(()=>{return}),this.ctx=null,this.capture=null,this.playback=null,this.sink=null}}var m_=qh*Dh/1000;var lh=`const FRAME_SAMPLES = Math.round(sampleRate / 50);
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
`;var L0=`// Passive receiver only. Both directions share one rendering clock and delay.
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
`;var S0=`// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
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
`;var T0=new WeakMap,vh=new Map,ym=8000;async function kh(h,m){let t=m==="worklet"?lh:m==="worker"?S0:L0;if(typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin){let S=[URL.createObjectURL(new Blob([t],{type:"text/javascript"}))];return{urls:S,dispose(){S.splice(0).forEach((s)=>URL.revokeObjectURL(s))}}}if(h.name!=="telephony"||!h.projectId||!Number.isSafeInteger(h.installId)||h.installId<=0)throw Error("Telephony audio requires a project and installation");let _=new URL(h.mcpURL(),location.href),M=`${_.origin}:${_.pathname}:${h.projectId}:${h.installId}`,f=T0.get(h);if(!f||f.scope!==M)f={scope:M,entries:new Map},T0.set(h,f);let u=f.entries,y=m,q=u.get(y);if(!q){q=(async()=>{let s=vh.get(m);if(!s)s=crypto.subtle.digest("SHA-256",new TextEncoder().encode(t)).then((R)=>Array.from(new Uint8Array(R),(g)=>g.toString(16).padStart(2,"0")).join("")),vh.set(m,s),s.catch(()=>{if(vh.get(m)===s)vh.delete(m)});let b=`/ui/frontend/${m}-${await s}.js`,A=new AbortController,x;try{if(await Promise.race([h.get(b,{cache:"force-cache",redirect:"error",signal:A.signal,headers:{Accept:"text/plain"}}),new Promise((F,G)=>{x=setTimeout(()=>{G(Error("Telephony audio asset preparation timed out")),A.abort()},ym)})])!==t)throw Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");let g=new URL(`/api/apps/telephony/_install/${h.installId}${b}`,_);return g.searchParams.set("project_id",h.projectId),g.searchParams.set("install_id",String(h.installId)),g.href}finally{clearTimeout(x)}})(),u.set(y,q);let S=q;S.catch(()=>{if(u.get(y)===S)u.delete(y)})}return{urls:[await q],dispose(){}}}function Ih(h=!1){let m=(h?[lh]:[lh,S0]).map((t)=>URL.createObjectURL(new Blob([t],{type:"text/javascript"})));return{urls:m,dispose(){m.splice(0).forEach((t)=>URL.revokeObjectURL(t))}}}async function Eh(h,m=!1){if(typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin)return Ih(m);let t=await Promise.all(m?[kh(h,"worklet")]:[kh(h,"worklet"),kh(h,"worker")]);return{urls:t.flatMap((_)=>_.urls),dispose(){t.forEach((_)=>_.dispose())}}}var q0=Object.freeze({upload_bitrate_control:"standard",media_transport:"inherit",initial_bitrate_bps:32000,min_bitrate_bps:16000,max_bitrate_bps:32000,playback_target_ms:60,playback_max_ms:160,quality_warnings:!1,degraded_seconds:6,warning_cooldown_seconds:60,background_check:!1,allow_user_overrides:!1});function Ch(h){if(!h||typeof h!=="object"||Array.isArray(h))throw Error("Invalid network policy");let m={...q0,...h};for(let t of["initial_bitrate_bps","min_bitrate_bps","max_bitrate_bps","playback_target_ms","playback_max_ms","degraded_seconds","warning_cooldown_seconds"])if(!Number.isInteger(m[t]))throw Error("Invalid network policy bounds");for(let t of["quality_warnings","background_check","allow_user_overrides"])if(typeof m[t]!=="boolean")throw Error("Invalid network policy switch");if(!["standard","guarded"].includes(m.upload_bitrate_control)||!["inherit","auto","webrtc","websocket"].includes(m.media_transport)||m.min_bitrate_bps<16000||m.max_bitrate_bps>32000||m.initial_bitrate_bps<m.min_bitrate_bps||m.initial_bitrate_bps>m.max_bitrate_bps||m.playback_target_ms<40||m.playback_target_ms>120||m.playback_max_ms<m.playback_target_ms||m.playback_max_ms>200||m.degraded_seconds<4||m.degraded_seconds>30||m.warning_cooldown_seconds<30||m.warning_cooldown_seconds>300)throw Error("Invalid network policy bounds");return m}class s0{profile;bitrate;reason="initial";last;path;ssrc;baseline;pairResponses;pairRTT;pairAt;congested;stable;changed=-1/0;constructor(h){this.profile=h;this.bitrate=h.initial_bitrate_bps}observe(h,m){if(!Number.isFinite(m))return;if(this.last!==void 0&&(m<=this.last||m-this.last>5000)||this.path!==h.path_revision||this.ssrc!==h.remote_receiver_ssrc)this.baseline=this.congested=this.stable=void 0,this.pairResponses=this.pairRTT=this.pairAt=void 0,this.changed=m,this.reason="observation_reset";this.last=m,this.path=h.path_revision,this.ssrc=h.remote_receiver_ssrc;let{pair_responsesReceived:_,pair_currentRoundTripTime:M}=h;if(Number.isFinite(_)&&_>0){if((this.pairResponses===void 0||_>this.pairResponses)&&Number.isFinite(M)&&M>0&&M<5)this.pairRTT=M,this.pairAt=m;this.pairResponses=_}let f=h.remote_receiver_report_age_ms>=0&&h.remote_receiver_report_age_ms<=5000&&h.sender_packets_delta>0&&h.sender_window_ms>0&&h.sender_window_ms<=5000,u=h.remote_receiver_fractionLost;if(!f||!Number.isFinite(u)||u<0||u>1){this.congested=this.stable=void 0;return}if(h.remote_receiver_report_updated!==1)return;let y=h.remote_receiver_roundTripTime??(this.pairAt!==void 0&&m-this.pairAt<=5000?this.pairRTT:void 0),q=y!==void 0&&Number.isFinite(y)&&y>0&&y<5;if(q)this.baseline=Math.min(this.baseline??y,y);let S=q&&this.baseline!==void 0?y-this.baseline:0,s=h.pair_availableOutgoingBitrate,b=Number.isFinite(s)&&s>0&&s<64000?Math.max(this.profile.min_bitrate_bps,Math.floor((s-32000)*0.8/1000)*1000):void 0,A=u>=0.01||h.sender_send_delay_interval_ms>=20||b!==void 0&&b<this.bitrate;if(q&&S>=0.08&&A)this.congested??=m;else this.congested=void 0;let R=this.congested!==void 0&&m-this.congested>=4000;if(u>=0.03||b!==void 0&&b<this.bitrate||R){if(this.stable=void 0,m-this.changed<(u>=0.03?2000:4000))return;this.bitrate=Math.max(this.profile.min_bitrate_bps,Math.min(R?Math.floor(this.bitrate*0.75/1000)*1000:this.bitrate-4000,b??this.bitrate)),this.changed=m,this.reason=u>=0.03?"loss":R?"sustained_upload_delay":"bandwidth"}else if(u<0.01&&(!q||S<0.04)&&!(h.sender_send_delay_interval_ms>=20)){if(this.stable??=m,m-this.stable>=1e4&&m-this.changed>=1e4)this.bitrate=Math.min(this.profile.max_bitrate_bps,this.bitrate+2000),this.stable=this.changed=m,this.reason="stable_recovery"}else this.stable=void 0}}class b0{options;target;bitrate=32000;get uploadControl(){return"standard"}get uploadReason(){return"standard"}stable;last=-1/0;initial;maximum;constructor(h={}){this.options=h;this.initial=h.playbackTargetMs??60,this.target=this.initial,this.maximum=Math.max(this.initial,h.playbackMaxMs??160)}observe(h,m=performance.now()){if(m<this.last)this.last=-1/0,this.stable=void 0;if(m-this.last<2000)return;this.last=m;let t=h.receiver_jitter===void 0?void 0:h.receiver_jitter*1000,_=h.receiver_concealed_delta_ms??0;if(this.options.playbackAdaptive!==!1){let u=Math.min(this.maximum,Math.max(this.initial,this.initial+Math.ceil(Math.max(0,(t??0)-10)*2/20)*20,_>0?this.target+20:this.initial));if(u>this.target)this.target=Math.min(u,this.target+20),this.stable=void 0;else if(t!==void 0&&_===0&&t<10){if(this.stable??=m,m-this.stable>=1e4)this.target=Math.max(this.initial,this.target-10),this.stable=m}else this.stable=void 0}let{pair_availableOutgoingBitrate:M,remote_receiver_fractionLost:f}=h;if(M!==void 0&&M>0&&M<64000)this.bitrate=Math.max(16000,Math.min(32000,Math.floor((M-32000)*0.8/1000)*1000));else if(f!==void 0&&f>=0.03)this.bitrate=Math.max(16000,this.bitrate-4000);else if(f!==void 0&&f<0.01)this.bitrate=Math.min(32000,this.bitrate+2000)}}class x0{tokens=1100;at;rate=640;observeAvailableBitrate(h){this.rate=h!==void 0&&h>=96000?2000:640}consume(h,m=performance.now()){if(!Number.isFinite(h)||!Number.isFinite(m)||h<0||h>1100)return!1;if(this.at!==void 0)this.tokens=Math.min(1100,this.tokens+Math.max(0,m-this.at)*this.rate/1000);if(this.at=m,this.tokens<h)return!1;return this.tokens-=h,!0}}class Z0 extends b0{get uploadControl(){return this.guarded?"guarded":"standard"}get uploadReason(){return this.guarded?.reason??"standard"}minimumRate;maximumRate;guarded;constructor(h){let m=Ch(h.networkProfile);super({...h,playbackTargetMs:m.playback_target_ms,playbackMaxMs:m.playback_max_ms});if(m.upload_bitrate_control==="guarded")this.guarded=new s0(m);this.bitrate=m.initial_bitrate_bps,this.minimumRate=m.min_bitrate_bps,this.maximumRate=m.max_bitrate_bps}observe(h,m=performance.now()){if(super.observe(h,m),this.guarded)this.guarded.observe(h,m),this.bitrate=this.guarded.bitrate;this.bitrate=Math.max(this.minimumRate,Math.min(this.maximumRate,this.bitrate))}}function wh(h={}){return h.networkProfile?new Z0(h):new b0(h)}class Qh{profile;uploadBadAt;path;badSince;last;lastAttempt=-1/0;baselineRTT;attempts=0;constructor(h=q0){this.profile=h}observe(h,m,t){if(!t||!this.profile.quality_warnings||this.last!==void 0&&(m<=this.last||m-this.last>3000)){this.badSince=void 0,this.uploadBadAt=void 0,this.baselineRTT=void 0,this.last=m;return}if(this.last=m,this.path!==h.path_revision)this.path=h.path_revision,this.baselineRTT=void 0,this.badSince=void 0,this.uploadBadAt=void 0;let _=h.pair_currentRoundTripTime;if(_!==void 0&&_>0&&_<5)this.baselineRTT=Math.min(this.baselineRTT??_,_);let M=_!==void 0&&this.baselineRTT!==void 0&&_-this.baselineRTT>=0.08,f=h.receiver_loss_delta??0,u=h.receiver_loss_window_ms,y=u!==void 0&&u>=500&&u<=2500&&f>=3,q=h.receiver_concealed_delta_ms??0,S=h.receiver_jitter??0;if(h.remote_receiver_report_updated===1)this.uploadBadAt=h.remote_receiver_report_age_ms<=5000&&h.sender_packets_delta>0&&h.remote_receiver_fractionLost>=0.03?m:void 0;if(!(this.uploadBadAt!==void 0&&m-this.uploadBadAt<=5000||y&&(M||q>=80)||q>=80&&S>=0.03)){this.badSince=void 0;return}if(this.badSince??=m,this.attempts>=2||m-this.badSince<this.profile.degraded_seconds*1000||m-this.lastAttempt<this.profile.warning_cooldown_seconds*1000)return;return this.attempts++,this.lastAttempt=m,this.badSince=void 0,"degraded"}}function nh(h){if(h===void 0)return"websocket";if(!["websocket","webrtc","auto"].includes(h))throw RangeError("Unsupported softphone media transport");return h}function A0(h,m,t={}){if(m!==void 0&&typeof m!=="boolean")throw RangeError("webrtcFec must be boolean");let _=new URL(h);if(W0(_,t),_.searchParams.set("transport","webrtc"),_.searchParams.delete("webrtc_fec"),m!==void 0)_.searchParams.set("webrtc_fec",String(m));return _.toString()}var I=(h)=>{try{h?.()}catch{}},bh=(h,m=1000000000000)=>typeof h==="number"&&Number.isFinite(h)?Math.max(0,Math.min(h,m)):0;function Sm(h){return h===!0?"relay_configured":h===!1?"no_relay":"unreported"}function ah(h){let m=th(h);return["turn_relay_unavailable","turn_client_update_required","turn_relay_required"].includes(h?.code??m.code)||m.denied||m.code==="media_session_changed"||m.code==="call_ended"||m.code==="call_not_attachable"||m.expired||["NotAllowedError","NotFoundError","SecurityError","OverconstrainedError"].includes(h?.name)}function P0(h,m){let t=0,_=0,M=0,f=0,u=0,y=0,q=0,S=0,s=0,b=null,A,x,R,g={},F={},G={},C=(P,O)=>typeof P?.[O]==="number"&&Number.isFinite(P[O])?Math.max(0,P[O]):void 0,E=(P,O,W)=>{for(let B of W){let $=C(P,B);if($!==void 0)F[O+B]=$}},Q=(P,O,W,B,$)=>{let T=C(P,W),X=C(P,B),w=m?.counters?.[O];if(T!==void 0&&X!==void 0){if(g[O]??={},g[O][W]=T,g[O][B]=X,C(P,"ssrc")!==void 0)g[O].ssrc=P.ssrc;if(w&&w.ssrc===C(P,"ssrc")&&T>=w[W]&&X>w[B])F[$]=(T-w[W])/(X-w[B])*1000}},k=(P,O,W)=>{let B=C(P,"packetsLost"),$=C(P,"ssrc"),T=C(P,"timestamp"),X=m?.counters?.[O];if($!==void 0)F[W+"ssrc"]=$;if(B===void 0||T===void 0)return;if(g[O]??={},g[O].packetsLost=B,g[O].at=T,$!==void 0)g[O].ssrc=$;if(X&&X.ssrc===$&&B>=X.packetsLost&&T>X.at)F[W+"loss_delta"]=(F[W+"loss_delta"]??0)+(B-X.packetsLost),F[W+"loss_window_ms"]=T-X.at},N=(P,O,W)=>{let B=C(P,"concealedSamples"),$=C(P,"silentConcealedSamples"),T=C(P,"timestamp"),X=C(P,"ssrc"),w=m?.counters?.[O];if(B===void 0||T===void 0)return;if(g[O]??={},Object.assign(g[O],{concealedSamples:B,at:T}),X!==void 0)g[O].ssrc=X;if($!==void 0)g[O].silentConcealedSamples=$;if(w&&w.ssrc===X&&B>=w.concealedSamples&&T>w.at){if(F.receiver_concealed_delta_ms=(F.receiver_concealed_delta_ms??0)+(B-w.concealedSamples)*1000/W,F.receiver_concealed_window_ms=T-w.at,$!==void 0&&$>=w.silentConcealedSamples)F.receiver_silent_concealed_delta_ms=(F.receiver_silent_concealed_delta_ms??0)+($-w.silentConcealedSamples)*1000/W}},U,Y=!1,o=!1,xh=0;h.forEach((P,O)=>{if(s=Math.max(s,bh(P.timestamp,1000000000000000)),(P.kind??P.mediaType??h.get(P.localId)?.kind)==="audio"&&xh++<32){if(P.type==="outbound-rtp"){t+=bh(P.bytesSent),E(P,"sender_",["bytesSent","packetsSent","headerBytesSent","retransmittedPacketsSent","retransmittedBytesSent","nackCount","targetBitrate"]),Q(P,O,"totalPacketSendDelay","packetsSent","sender_send_delay_interval_ms");let B=C(P,"packetsSent"),$=m?.counters?.[O];if(g[O]??={},B!==void 0)g[O].packetsSent=B;if($&&B!==void 0&&B>=$.packetsSent&&P.timestamp>$.at)F.sender_packets_delta=B-$.packetsSent,F.sender_window_ms=P.timestamp-$.at}if(P.type==="inbound-rtp"){k(P,O,"receiver_"),_+=bh(P.bytesReceived),M+=bh(P.packetsLost),f=Math.max(f,bh(P.jitter)*1000);let B=h.get(P.codecId),$=C(B,"clockRate")??48000;if(N(P,O,$),o||=C(P,"concealedSamples")!==void 0,u+=bh(P.concealedSamples)*1000/$,y+=bh(P.packetsDiscarded),q+=bh(P.jitterBufferDelay),S+=bh(P.jitterBufferEmittedCount),E(P,"receiver_",["bytesReceived","packetsReceived","packetsLost","jitter","packetsDiscarded","concealedSamples","silentConcealedSamples","concealmentEvents","insertedSamplesForDeceleration","removedSamplesForAcceleration","totalSamplesReceived","audioLevel","totalAudioEnergy","totalSamplesDuration","nackCount","fecPacketsReceived","fecPacketsDiscarded"]),Q(P,O,"jitterBufferDelay","jitterBufferEmittedCount","receiver_jitter_buffer_interval_ms"),Q(P,O,"jitterBufferTargetDelay","jitterBufferEmittedCount","receiver_jitter_target_interval_ms"),Q(P,O,"jitterBufferMinimumDelay","jitterBufferEmittedCount","receiver_jitter_minimum_interval_ms"),E(P,"receiver_",["totalProcessingDelay","totalSamplesDecoded"]),G.receiver_processing=C(P,"totalSamplesDecoded")===void 0?"unavailable":"per_decoded_sample",Q(P,O,"totalProcessingDelay","totalSamplesDecoded","receiver_processing_interval_ms"),C(P,"lastPacketReceivedTimestamp")!==void 0)F.receiver_last_packet_age_ms=Math.max(0,P.timestamp-P.lastPacketReceivedTimestamp);if(B){if(G.codec=B.mimeType,F.codec_clock_rate=$,C(B,"channels")!==void 0)F.codec_channels=B.channels}}if(P.type==="remote-inbound-rtp"){let B=C(P,"timestamp"),$=m?.counters?.[O];if(U=B,Y=B!==void 0&&(!$||$.ssrc!==C(P,"ssrc")||B>$.feedbackAt),g[O]??={},B!==void 0)g[O].feedbackAt=B;E(P,"remote_receiver_",["packetsLost","fractionLost","jitter","roundTripTime","totalRoundTripTime","roundTripTimeMeasurements"]),k(P,O,"remote_receiver_")}if(P.type==="remote-outbound-rtp")E(P,"remote_sender_",["packetsSent","bytesSent"]);let W=P.type==="outbound-rtp"?"bytesSent":P.type==="inbound-rtp"?"bytesReceived":void 0;if(W&&C(P,W)!==void 0)g[O]??={},g[O][W]=P[W],g[O].at=P.timestamp}if(P.type==="transport"&&P.selectedCandidatePairId){A=P.selectedCandidatePairId,G.dtls=P.dtlsState;let W=h.get(A);if(W){b=C(W,"currentRoundTripTime")!==void 0?W.currentRoundTripTime*1000:null,E(W,"pair_",["availableOutgoingBitrate","availableIncomingBitrate","currentRoundTripTime","totalRoundTripTime","bytesSent","bytesReceived","requestsSent","requestsReceived","responsesSent","responsesReceived","consentRequestsSent"]),G.pair=W.state;let B=h.get(W.localCandidateId),$=h.get(W.remoteCandidateId);if(B)x=B.protocol,R=B.candidateType,G.protocol=x,G.local_candidate=R,G.relay_protocol=B.relayProtocol;if($)G.remote_candidate=$.candidateType}}});let yh=(P,O)=>{if(m?.counters){let B=0;for(let[$,T]of Object.entries(g)){let X=m.counters[$];if(X&&T[P]>=X[P]&&T.at>X.at)B+=(T[P]-X[P])*8000/(T.at-X.at)}return B}let W=m&&s>m.at?(s-m.at)/1000:0;return W?Math.max(0,O-(P==="bytesSent"?m.sent:m.received))*8/W:0};if(U!==void 0)F.remote_receiver_report_age_ms=Math.max(0,s-U),F.remote_receiver_report_updated=Y?1:0;let Fh=(m?.pathRevision??0)+(A&&A!==m?.pairID?1:0);F.path_revision=Fh;let v=yh("bytesSent",t),Uh=yh("bytesReceived",_);return{previous:{at:s,sent:t,received:_,counters:g,pairID:A,pathRevision:Fh},rtt:b,queueMs:F.receiver_jitter_buffer_interval_ms??0,metrics:M0({...F,send_bitrate_bps:v,receive_bitrate_bps:Uh}),states:f0(G),webrtc:{relayProtocol:G.relay_protocol,protocol:x,candidateType:R,sendBitrateBps:v,receiveBitrateBps:Uh,packetsLost:M,jitterMs:f,concealedMs:o?u:void 0,packetsDiscarded:y,jitterBufferMs:S>0?q/S*1000:0,jitterBufferIntervalMs:F.receiver_jitter_buffer_interval_ms}}}function qm(h,m=new Date().toISOString()){let t=[];for(let[_,M]of[["receiver_","carrier_to_operator"],["remote_receiver_","operator_to_carrier"]]){let f=h[_+"loss_delta"];if(f>0)t.push({timestamp:m,direction:M,reason:"webrtc_packet_loss",duration_ms:0,packet_count:f,ssrc:h[_+"ssrc"],window_ms:h[_+"loss_window_ms"]})}if(h.receiver_concealed_delta_ms>0)t.push({timestamp:m,direction:"carrier_to_operator",reason:"webrtc_concealment",duration_ms:Math.round(h.receiver_concealed_delta_ms),ssrc:h.receiver_ssrc,window_ms:h.receiver_concealed_window_ms});return t}class g0{callbacks;ice;stopped=!1;generation=0;socket;pc;stream;context;capture;whisper;destination;speaker;remoteAudio;speakerMeter;carrierStalled=!1;deliveryDegraded=!1;ready=!1;answered=!1;mediaPathReady;mediaRevision=0;reportedLive=!1;peer=!1;muted=!1;whisperEpoch=null;whisperBase=null;whisperResampler=new gh;timer;retry;cancelSetup;recovering=!1;recoveryGeneration=0;recoveryContext;recoveryExpiry;recoveryRequest;ringback;options;workletURL;events=[];previous;sampling=new WeakSet;transportTelemetry=new Zh("webrtc");transportSender=new Bh((h)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;return this.sendTelemetry({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[h]}})},(h,m=0)=>{if(this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let t=h;if(t?.diagnostics?.webrtc){let _=t.diagnostics.webrtc;return this.sendTelemetry({...t,diagnostics:{...t.diagnostics,webrtc:{..._,jitterBufferSampleAgeMs:(_.jitterBufferSampleAgeMs??0)+m}}})}return this.sendTelemetry(h)});controlBudget=new x0;quality;pathHealth=new Qh;playbackReceiver;uploadSender;applyingBitrate=!1;appliedBitrate;statsErrors=0;lastStatsSend=-1/0;lastValidStatsAt=-1/0;clientEpoch=crypto.randomUUID();relayConfigured;nativeCounts;completedCounts={packetsLost:0,packetsDiscarded:0,concealedMs:0};runtime=new Kh((h)=>this.recordSessionEvent(h));diagnostics={mediaTransport:"webrtc",codec:"opus",rttMs:null,queueMs:0,targetMs:60,underruns:null,droppedMs:0,maxQueueMs:0,audioContextRate:48000,websocketBufferedBytes:0,microphoneSampleRate:0,microphoneChannelCount:0,echoCancellation:null,noiseSuppression:null,autoGainControl:null,micActiveRmsDbfs:null,micPeakDbfs:null,micPostPeakDbfs:null,micInputGainDb:0,micLimiterReductionDb:0,captureSequenceGaps:0,playbackSequenceGaps:0,dropEvents:[]};constructor(h){this.callbacks=h}async start(h,m,t,_){if(this.options=m,this.workletURL=t,!t)throw Error("WebRTC requires the shared Telephony audio processor");if(typeof RTCPeerConnection!=="function")throw Error("WebRTC audio is unavailable in this browser");e(m),await this.connect(h,_)}current(h){return!this.stopped&&this.generation===h}guard(h){if(!this.current(h))throw Error("Audio session cancelled")}async connect(h,m){this.recordSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+Xh.replaceAll(".","_")}),this.runtime.observeEnvironment();let t=++this.generation,_=this.options;for(let x of this.events.slice(-8))this.enqueueRTCEvents({session_events:[x]});for(let x of this.diagnostics.dropEvents.slice(-8))this.enqueueRTCEvents({drop_events:[x]});if(this.ready=this.peer=this.answered=this.reportedLive=!1,this.mediaPathReady=void 0,this.mediaRevision=0,this.previous=void 0,this.quality=wh(_),this.pathHealth=new Qh(_.networkProfile),this.appliedBitrate=void 0,m)this.stream=m.stream,this.context=m.context;let M=m?.stream??await p("microphone_device",(x)=>this.recordSessionEvent(x),()=>navigator.mediaDevices.getUserMedia({audio:sh(_)}));if(!this.current(t))throw M.getTracks().forEach((x)=>x.stop()),Error("Audio session cancelled");this.stream=M;let f=M.getAudioTracks()[0];if(!f)throw Error("No microphone audio track");let u=f.getSettings();this.diagnostics={...this.diagnostics,microphoneSampleRate:u.sampleRate??0,microphoneChannelCount:u.channelCount??1,echoCancellation:typeof u.echoCancellation==="boolean"?u.echoCancellation:null,noiseSuppression:u.noiseSuppression??null,autoGainControl:u.autoGainControl??null,micInputGainDb:_.inputGainDB},f.onmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}),f.onunmute=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}),f.onended=()=>{if(this.current(t))this.disconnected(t,"microphone_ended")},this.context=m?.context??new AudioContext({latencyHint:"interactive"});let y=this.context;if(y.onstatechange=()=>{this.runtime.context(y.state),this.updateLiveState()},this.runtime.context(y.state),y.state!=="running")await p("audio_context",(x)=>this.recordSessionEvent(x),()=>y.resume());if(this.guard(t),_.outputDeviceId&&"setSinkId"in y)await y.setSinkId(_.outputDeviceId);await p("audio_worklet",(x)=>this.recordSessionEvent(x),()=>y.audioWorklet.addModule(this.workletURL)),this.guard(t),this.diagnostics.audioContextRate=y.sampleRate,this.capture=new AudioWorkletNode(y,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:_.inputGainDB,highpassFilter:_.highpassFilter}}),this.destination=y.createMediaStreamDestination(),this.destination.channelCount=1,y.createMediaStreamSource(M).connect(this.capture).connect(this.destination),this.capture.port.onmessage=(x)=>{if(x.data?.type==="capture.stats"){let R=(F)=>F>0?20*Math.log10(F):null;this.diagnostics.micActiveRmsDbfs=R(x.data.active_rms),this.diagnostics.micPeakDbfs=R(x.data.pre_peak),this.diagnostics.micPostPeakDbfs=R(x.data.post_peak),this.diagnostics.micLimiterReductionDb=x.data.limiter_reduction_db??0;let g=0;if(this.speakerMeter){let F=new Float32Array(this.speakerMeter.fftSize);this.speakerMeter.getFloatTimeDomainData(F),g=Math.sqrt(F.reduce((G,C)=>G+C*C,0)/F.length)}I(()=>this.callbacks.onLevels?.(bh(x.data.active_rms,1),g))}},this.speaker=y.createGain(),this.speaker.gain.value=_.outputVolume??1,this.speaker.connect(y.destination),this.speakerMeter=y.createAnalyser(),this.speakerMeter.fftSize=256;let q=y.createGain();q.gain.value=0,this.speaker.connect(this.speakerMeter).connect(q).connect(y.destination),this.whisper=new AudioWorkletNode(y,"softphone-playback",{numberOfInputs:0,outputChannelCount:[1],processorOptions:e(_)}),this.whisper.connect(this.speaker),this.gate();let S=new WebSocket(A0(h,_.webrtcFec,_));S.binaryType="arraybuffer",this.socket=S;let s=!1,b=!1,A=!1;await p("audio_transport",(x)=>this.recordSessionEvent(x),()=>new Promise((x,R)=>{let g=(C)=>{if(b)return;b=!0,clearTimeout(F),this.cancelSetup=void 0,C?R(A?Object.assign(C,{code:"turn_relay_required"}):C):x()},F=setTimeout(()=>g(Error("WebRTC audio connection timed out")),18000);this.cancelSetup=()=>g(Error("Audio session cancelled"));let G=()=>{if(this.current(t)&&this.ready&&s)this.gate(),g(),this.updateLiveState()};S.onmessage=(C)=>{if(!this.current(t))return;if(C.data instanceof ArrayBuffer){this.playWhisper(C.data);return}if(typeof C.data!=="string")return;let E;try{E=JSON.parse(C.data)}catch{return}if(E.type==="webrtc.error"){g(Object.assign(Error("Required TURN relay is unavailable"),{code:E.code}));return}if(this.ice?.handle(E))return;if(E.type==="webrtc.config"){if(A=E.ice_transport_policy==="relay",E.network_policy)try{let Q=Ch(E.network_policy);this.quality=wh({..._,networkProfile:Q}),this.pathHealth=new Qh(Q)}catch{g(Error("Invalid network policy"));return}if(this.relayConfigured=typeof E.relay_configured==="boolean"?E.relay_configured:void 0,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"ice_configuration",outcome:Sm(E.relay_configured)}),this.pc){g(Error("Repeated WebRTC configuration"));return}(async()=>{let Q=new RTCPeerConnection(Hh(E));this.pc=Q,this.ice?.stop(),this.ice=new Th((N)=>this.send(N),()=>this.current(t),(N,U)=>this.recordSessionEvent({...U,timestamp:new Date().toISOString(),action:"ice_restart",outcome:N}),(N)=>{this.relayConfigured=N.iceServers?.some((U)=>(typeof U.urls==="string"?[U.urls]:U.urls).some((Y)=>/^turns?:/.test(Y)))??!1}),this.ice.register("main",Q),this.destination.stream.getAudioTracks().forEach((N)=>{let U=Q.addTrack(N,this.destination.stream);this.uploadSender=U,this.applyUploadBitrate(U,this.quality?.bitrate??32000,t)}),Q.ontrack=(N)=>{if(!this.current(t))return;this.playbackReceiver=N.receiver,this.applyPlaybackTarget();let U=new MediaStream([N.track]),Y=new Audio;Y.muted=!0,Y.srcObject=U,this.remoteAudio=Y,Y.play().catch(()=>I(()=>this.callbacks.onNotice?.("Browser audio playback requires a user interaction."))),y.createMediaStreamSource(U).connect(this.speaker)},Q.onconnectionstatechange=()=>{if(!this.current(t))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"webrtc",outcome:Q.connectionState}),Q.connectionState==="connected")s=!0,G();if(Q.connectionState==="failed"||Q.connectionState==="closed")if(!b)g(Error("WebRTC connection failed"));else this.disconnected(t,"webrtc_"+Q.connectionState)};let k=await Q.createOffer();this.guard(t),await Q.setLocalDescription(k),await new Promise((N,U)=>{if(Q.iceGatheringState==="complete"){N();return}let Y=setTimeout(()=>{Q.onicegatheringstatechange=null,U(Error("ICE gathering timed out"))},7000);Q.onicegatheringstatechange=()=>{if(Q.iceGatheringState==="complete")clearTimeout(Y),Q.onicegatheringstatechange=null,N()}}),this.guard(t),S.send(JSON.stringify({type:"webrtc.offer",sdp:Q.localDescription.sdp,ice_version:1}))})().catch((Q)=>g(Q instanceof Error?Q:Error("WebRTC setup failed")));return}if(E.type==="webrtc.answer"){this.pc?.setRemoteDescription({type:"answer",sdp:E.sdp}).catch(()=>g(Error("Invalid WebRTC answer")));return}if(E.type==="media.ready"||E.type==="media.not_ready"){if(!Number.isSafeInteger(E.revision)||E.revision<=this.mediaRevision)return;this.mediaRevision=E.revision,this.mediaPathReady=E.type==="media.ready",I(()=>this.callbacks.onMediaReadiness?.(E)),this.updateLiveState()}if(E.type==="ready")this.ready=!0,this.send({type:"media.capabilities",version:2,versions:[3],whisper:!0}),G();if(E.type==="peer.connected")this.peer=!0,this.gate(),G();if(E.type==="peer.disconnected")this.peer=!1,this.gate(),this.updateLiveState(),I(()=>this.callbacks.onNotice?.("Carrier audio interrupted; the call remains connected."));if(E.type==="call.status")this.answered=["answered","in-progress"].includes(E.status),G(),I(()=>this.callbacks.onCallStatus?.(E));if(E.type==="call.error")if(!b)g(Error(E.detail??"Carrier activation failed"));else this.stop(),I(()=>this.callbacks.onState?.("error",E.detail));if(E.type==="call.ended"||E.type==="session.replaced")if(!b)g(Error(E.type));else this.stop(),I(()=>this.callbacks.onState?.("ended",E.type));if(E.type==="audio.health"){if(this.diagnostics.audioHealth={state:E.state,reason:E.reason,stages:E.stages},I(()=>this.callbacks.onAudioHealth?.(E)),E.state==="audio_degraded"&&!this.deliveryDegraded)this.deliveryDegraded=!0,I(()=>this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected."));else if(this.deliveryDegraded&&E.state==="healthy"&&E.stages?.telephony_to_browser?.state==="healthy"&&y.state==="running")this.deliveryDegraded=!1,I(()=>this.callbacks.onNotice?.("Speech delivery restored."))}if(E.type==="media.delivery"){if(E.state==="stalled")I(()=>this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected."));else if(E.state==="flowing"&&this.carrierStalled)I(()=>this.callbacks.onNotice?.("Caller audio delivery restored."));this.carrierStalled=E.state==="stalled"}if(E.type==="call.notice")I(()=>this.callbacks.onNotice?.(E.detail??E.reason??"Audio delivery notice"));if(E.type==="coach.state")this.whisperEpoch=E.talking&&Number.isInteger(E.epoch)?E.epoch:null,this.whisperBase=null,this.whisper?.port.postMessage({type:"whisper.clear"})},S.onerror=()=>this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"error",detail:"WebRTC signaling error"}),S.onclose=(C)=>{if(!this.current(t))return;if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"websocket",outcome:"closed",code:String(C.code),was_clean:C.wasClean}),!b){g(Object.assign(Error("WebRTC signaling unavailable"),C.code===1008?{status:403}:{}));return}if(C.code===1008){this.stop(),I(()=>this.callbacks.onState?.("error","Media authorization ended"));return}this.disconnected(t,"signaling_closed")}})),this.guard(t),this.timer=setInterval(()=>{this.runtime.tick(),this.statistics(t)},1000),await this.statistics(t)}send(h){if(this.socket?.readyState===WebSocket.OPEN&&this.socket.bufferedAmount<65536)try{this.socket.send(JSON.stringify(h))}catch{}}updateLiveState(){let h=this.ready&&this.peer&&this.answered&&this.mediaPathReady!==!1&&this.pc?.connectionState==="connected"&&this.context?.state==="running";if(h===this.reportedLive)return;this.reportedLive=h,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"media_path",outcome:h?"ready":"waiting"}),I(()=>this.callbacks.onState?.(h?"live":"reconnecting",h?void 0:"Connecting carrier audio…"))}gate(){let h=!this.stopped&&!this.muted;this.capture?.port.postMessage({type:"muted",value:!h}),this.destination?.stream.getAudioTracks().forEach((m)=>m.enabled=h)}async statistics(h){if(!this.current(h)||!this.pc||this.sampling.has(this.pc))return;let m=this.pc,t=performance.now();this.sampling.add(m);try{let _=P0(await m.getStats(),this.previous);if(!this.current(h)||this.pc!==m)return;if(this.previous=_.previous,this.lastValidStatsAt=performance.now(),this.nativeCounts={packetsLost:_.webrtc.packetsLost,packetsDiscarded:_.webrtc.packetsDiscarded,concealedMs:_.webrtc.concealedMs},_.webrtc.packetsLost+=this.completedCounts.packetsLost,_.webrtc.packetsDiscarded+=this.completedCounts.packetsDiscarded,_.webrtc.concealedMs!==void 0)_.webrtc.concealedMs+=this.completedCounts.concealedMs;this.quality?.observe(_.metrics),this.applyPlaybackTarget();let M=this.pc.connectionState==="connected"&&this.context?.state==="running"&&this.peer&&this.answered;if(this.pathHealth.observe(_.metrics,performance.now(),M))this.recordSessionEvent({timestamp:new Date().toISOString(),action:"network_quality",outcome:"sustained_degradation"},!1),I(()=>this.callbacks.onNotice?.("The audio connection is unstable. After this call, compare direct and relay paths in Network settings."));if(this.uploadSender&&this.quality)this.applyUploadBitrate(this.uploadSender,this.quality.bitrate,h);if(_.states.upload_bitrate_control=this.quality?.uploadControl??"standard",_.states.upload_bitrate_reason=this.quality?.uploadReason??"standard",_.metrics.requested_playback_target_ms=this.quality?.target??60,this.appliedBitrate!==void 0)_.metrics.requested_upload_bitrate_bps=this.appliedBitrate;this.controlBudget.observeAvailableBitrate(_.metrics.pair_availableOutgoingBitrate);let f=qm(_.metrics);for(let y of f)this.enqueueRTCEvents({drop_events:[y]});this.diagnostics.dropEvents=[...this.diagnostics.dropEvents,...f].slice(-100),this.diagnostics={...this.diagnostics,rttMs:_.rtt,queueMs:_.queueMs,maxQueueMs:Math.max(this.diagnostics.maxQueueMs,_.queueMs),targetMs:this.quality?.target??this.options?.playbackTargetMs??60,webrtc:_.webrtc,playbackSequenceGaps:_.webrtc.packetsLost,sessionEvents:this.events.slice(),websocketBufferedBytes:this.socket?.bufferedAmount??0};let u=this.stream?.getAudioTracks()[0];this.transportTelemetry.observe({..._.metrics,rtt_ms:_.rtt,queue_ms:_.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:this.socket?.bufferedAmount??0,stats_errors:this.statsErrors,stats_duration_ms:performance.now()-t,main_thread_max_pause_ms:this.runtime.counters.main_thread_max_pause_ms},{..._.states,ice:m.iceConnectionState,connection:"connected",context:this.context?.state,muted:String(this.muted),device_muted:String(u?.muted),track:u?.readyState}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),I(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}catch{if(this.current(h)&&this.pc===m)this.statsErrors++,this.transportTelemetry.observe({stats_errors:this.statsErrors,stats_duration_ms:performance.now()-t},{ice:m.iceConnectionState,context:this.context?.state,muted:String(this.muted)}),this.diagnostics.transportSamples=this.transportTelemetry.recent(),this.sendStatistics(),I(()=>this.callbacks.onDiagnostics?.({...this.diagnostics}))}finally{this.sampling.delete(m)}}applyPlaybackTarget(){let h=this.playbackReceiver;if(!h||!this.quality)return;if("jitterBufferTarget"in h)try{h.jitterBufferTarget=this.quality.target}catch{}}async applyUploadBitrate(h,m,t){if(this.applyingBitrate||this.appliedBitrate===m)return;this.applyingBitrate=!0;try{let _=h.getParameters();if(_.encodings?.length){if(_.encodings[0].maxBitrate=m,await h.setParameters(_),this.current(t)&&this.uploadSender===h)this.appliedBitrate=m}}catch{}finally{this.applyingBitrate=!1}}sendTelemetry(h){if(!this.ready||this.socket?.readyState!==WebSocket.OPEN||this.socket.bufferedAmount>1024)return!1;let m=JSON.stringify(h),t=new TextEncoder().encode(m).byteLength+58;if(!this.controlBudget.consume(t))return!1;try{return this.socket.send(m),!0}catch{return!1}}enqueueRTCEvents(h){try{let m={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,...h}};if(new TextEncoder().encode(JSON.stringify(m)).byteLength+58>1100){this.transportSender.skippedAuxiliary++;return}this.transportSender.enqueueAuxiliary(m)}catch{this.transportSender.skippedAuxiliary++}}sendStatistics(){let h=this.stream?.getAudioTracks()[0];if(performance.now()-this.lastStatsSend>=5000)this.lastStatsSend=performance.now(),this.transportSender.enqueueReport({type:"diagnostics",diagnostics:{client_epoch:this.clientEpoch,client_version:Xh,relay_configured:this.relayConfigured,media_transport:"webrtc",codec:"opus",webrtc:{...Object.fromEntries(Object.entries(this.diagnostics.webrtc??{}).map(([m,t])=>[m,typeof t==="number"?Math.round(t*10)/10:t])),jitterBufferSampleAgeMs:Number.isFinite(this.lastValidStatsAt)?Math.max(0,performance.now()-this.lastValidStatsAt):void 0},connection_state:"connected",carrier_peer_connected:this.peer,audio_context_state:this.context?.state,microphone_muted:this.muted,microphone_track_state:h?.readyState,microphone_device_muted:h?.muted,rtt_ms:this.diagnostics.rttMs===null?null:Math.round(this.diagnostics.rttMs),playback_queue_ms:Math.round(this.diagnostics.queueMs),playback_target_ms:this.diagnostics.targetMs,audio_context_rate:this.context?.sampleRate,playback_sequence_gaps:this.diagnostics.webrtc?.packetsLost}}),this.transportSender.enqueue(this.transportTelemetry.drain()),this.enqueueRTCEvents({timing:{runtime:this.runtime.counters}})}playWhisper(h){if(h.byteLength<17||h.byteLength>176||this.whisperEpoch===null||!this.context)return;let m=new DataView(h);if(m.getUint32(0,!0)!==827805761||m.getUint32(4,!0)!==this.whisperEpoch)return;let t=m.getFloat64(8,!0);if(!Number.isFinite(t))return;let _=performance.timeOrigin+performance.now()-t;this.whisperBase=this.whisperBase===null?_:Math.min(this.whisperBase,_);let M=Math.max(0,_-this.whisperBase);if(M>200)return;let f=new Float32Array(h.byteLength-16);for(let y=0;y<f.length;y++){let q=~m.getUint8(y+16)&255,S=((q&15)<<3)+132<<(q>>4&7);f[y]=(q&128?132-S:S-132)/32768}let u=this.context.sampleRate===8000?f:this.whisperResampler.process(f,8000,this.context.sampleRate);this.whisper?.port.postMessage({type:"whisper",frame:u,received_audio_ms:this.context.currentTime*1000-M},[u.buffer])}disconnected(h,m){if(!this.current(h)||this.recovering)return;this.recovering=!0;let t=Oh("automatic_retry");this.recoveryContext=t,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"started",code:m,...t}),this.cleanup(),I(()=>this.callbacks.onState?.("reconnecting","Reconnecting WebRTC audio; the carrier call stays connected."));let _=++this.recoveryGeneration,M=()=>!this.stopped&&this.recovering&&this.recoveryGeneration===_;this.recoveryExpiry=setTimeout(()=>{if(!M())return;this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"timed_out",...this.recoveryContext}),++this.recoveryGeneration,this.recovering=!1,clearTimeout(this.retry),this.cancelSetup?.(),this.recoveryRequest?.abort(),this.cleanup(),I(()=>this.callbacks.onState?.("error","Network unavailable; waiting to restore audio to the existing call.",{recoverable:!0}))},30000);let f=async()=>{if(!M())return;let u={...t,attempt_id:Gh("attempt")};this.recoveryContext=u,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"attempt_started",code:m,...u});try{if(!this.callbacks.refreshMediaURL)throw Error("Fresh media authorization is required");this.recoveryRequest=new AbortController;let y=await this.callbacks.refreshMediaURL(u,this.recoveryRequest.signal);if(!M())return;if(await this.connect(y),!M())return;if(this.pc?.connectionState!=="connected")throw Error("WebRTC disconnected during recovery");clearTimeout(this.recoveryExpiry),this.recovering=!1,this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"connected",...u})}catch(y){if(!M())return;this.cleanup();let q=th(y);if(this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:ah(y)?"revoked":"attempt_failed",...u,status:q.status,code:q.code}),ah(y)){clearTimeout(this.recoveryExpiry),this.recovering=!1,I(()=>this.callbacks.onState?.("error","Media authorization or microphone access ended"));return}this.retry=setTimeout(()=>void f(),1000)}};f()}recordSessionEvent(h,m=!0){try{h={...this.callbacks.sessionDiagnostics?.(),...h}}catch{}if(h=Mh(h),this.events=[...this.events,h].slice(-100),m)this.enqueueRTCEvents({session_events:[h]});I(()=>this.callbacks.onSessionEvent?.(h))}setMuted(h){if(this.muted!==h)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:h?"muted":"unmuted"});if(this.muted=h,this.gate(),h)this.send({type:"interrupt"})}sendDTMF(h){if(!/^[0-9*#]+$/.test(h))throw Error("Invalid DTMF digits");this.send({type:"dtmf",digits:h})}setOutputVolume(h){if(!Number.isFinite(h)||h<0||h>1)throw RangeError("Volume must be between 0 and 1");if(this.speaker)this.speaker.gain.value=h;if(this.options)this.options.outputVolume=h}startRingback(h){if(this.stopRingback(),this.context&&this.speaker)this.ringback=ph(this.context,this.speaker,ch(h))}stopRingback(){this.ringback?.(),this.ringback=void 0}cleanup(){if(this.runtime.stopEnvironment(),this.transportSender.stop(),this.nativeCounts){for(let _ of["packetsLost","packetsDiscarded","concealedMs"])this.completedCounts[_]+=this.nativeCounts[_]??0;this.nativeCounts=void 0}++this.generation,this.ready=this.peer=!1,this.stopRingback(),clearInterval(this.timer),this.timer=void 0;let h=this.socket;if(this.socket=void 0,h){if(h.readyState===WebSocket.OPEN)try{h.send(JSON.stringify({type:"media.shutdown",reason:this.stopped?"session_cleanup":"audio_error"}))}catch{}h.onclose=h.onmessage=h.onerror=null,h.close(1000,"client_shutdown")}this.ice?.stop(),this.ice=void 0,this.playbackReceiver=void 0,this.uploadSender=void 0;let m=this.pc;if(this.pc=void 0,m)m.onconnectionstatechange=m.ontrack=null,m.close();if(this.remoteAudio)this.remoteAudio.pause(),this.remoteAudio.srcObject=null,this.remoteAudio=void 0;this.stream?.getTracks().forEach((_)=>{_.onended=null,_.stop()}),this.stream=void 0,this.destination?.stream.getTracks().forEach((_)=>_.stop()),this.destination=void 0,this.capture?.disconnect(),this.capture=void 0,this.whisper?.disconnect(),this.whisper=void 0,this.whisperEpoch=null,this.whisperBase=null,this.whisperResampler=new gh;let t=this.context;if(this.context=void 0,t)t.onstatechange=null,t.close().catch(()=>{});this.speaker=void 0,this.speakerMeter=void 0}stop(){if(this.stopped)return;if(this.recovering)this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"cancelled",...this.recoveryContext});this.stopped=!0,this.recoveryRequest?.abort(),++this.recoveryGeneration,clearTimeout(this.recoveryExpiry),clearTimeout(this.retry),this.cancelSetup?.(),this.cleanup()}}function K0(h,m,t){let _,M=!1,f=!1,u=[],y;return{async start(q,S){let s=nh(S.mediaTransport),b=async(A)=>{if(M)throw Error("Audio session cancelled");let x=A();_=x,x.setMuted(f);let R;try{for(let g of u.splice(0))try{x.recordSessionEvent?.(g)}catch{}if(M)throw Error("Audio session cancelled");if(await Promise.race([x.start(q,S),new Promise((g,F)=>{if(y=()=>F(Error("Audio session cancelled")),R=setTimeout(()=>F(Error("Audio transport setup timed out")),18000),M)y()})]),M)throw Error("Audio session cancelled")}catch(g){if(x.stop(),_===x)_=void 0;throw g}finally{clearTimeout(R),y=void 0}};if(s==="websocket"){await b(m);return}try{await b(t)}catch(A){if(s!=="auto"||M||S.iceTransportPolicy==="relay"||ah(A))throw A;I(()=>h.onSessionEvent?.({timestamp:new Date().toISOString(),action:"transport",outcome:"websocket_fallback"})),await b(m)}},stop(){M=!0,y?.(),u.splice(0),_?.stop(),_=void 0},setMuted(q){f=q,_?.setMuted(q)},setOutputVolume(q){_?.setOutputVolume(q)},sendDTMF(q){_?.sendDTMF(q)},recordSessionEvent(q){if(_)_.recordSessionEvent?.(q);else if(!M){if(u.push(Mh(q)),u.length>20)u.shift()}},startRingback(q){_?.startRingback?.(q)},stopRingback(){_?.stopRingback?.()}}}var sm=1e4;class E0{ready;context;stream;cancelled=!1;completed=!1;cancelWait;constructor(h,m,t){this.ready=this.prepare(h,m,t)}async prepare(h,m,t){let _;try{try{this.context=new AudioContext(m)}catch{this.context=new AudioContext({latencyHint:"interactive"})}let M=this.context,f=p("audio_context_preflight",t,()=>M.resume()),u=p("microphone_preflight",t,async()=>{let y=await navigator.mediaDevices.getUserMedia({audio:h});if(this.cancelled)throw y.getTracks().forEach((q)=>q.stop()),Error("Audio preparation cancelled");if(this.stream=y,!y.getAudioTracks().some((q)=>q.readyState==="live"))throw Error("Microphone is unavailable")});if(await Promise.race([Promise.all([f,u]),new Promise((y,q)=>{this.cancelWait=()=>q(Error("Audio preparation cancelled")),_=setTimeout(()=>q(Error("Microphone or playback setup timed out. Check browser permissions and try again.")),sm)})]),this.cancelled||M.state!=="running")throw Error("Browser audio is paused. Click Answer again to enable audio.");this.completed=!0}catch(M){throw this.cancel(),M}finally{clearTimeout(_),this.cancelWait=void 0}}isReady(){return this.completed&&!this.cancelled&&this.context?.state==="running"&&Boolean(this.stream?.getAudioTracks().some((h)=>h.readyState==="live"))}take(){if(!this.completed||this.cancelled||!this.stream||!this.context)throw Error("Audio preparation is unavailable");if(!this.stream.getAudioTracks().some((m)=>m.readyState==="live"))throw Error("Microphone disconnected before audio setup. Select a microphone and try again.");let h={stream:this.stream,context:this.context};return this.stream=void 0,this.context=void 0,h}cancel(){this.cancelled=!0,this.cancelWait?.(),this.stream?.getTracks().forEach((m)=>m.stop()),this.stream=void 0;let h=this.context;if(this.context=void 0,h)h.close().catch(()=>{})}}async function k0(){return(await navigator.mediaDevices.enumerateDevices()).filter((m)=>m.kind==="audioinput").map((m,t)=>({deviceId:m.deviceId,label:m.label||`Microphone ${t+1}`}))}function I0(h,m){let t=new u0(h,!1),_,M=!1,f,u=()=>{return M=!0,f??=(async()=>{try{await t.cancel()}finally{_?.dispose(),_=void 0}})()};return{async start(y={}){if(M)throw Error("Microphone preview is already used");M=!0;try{if(_=m?await Eh(m,!0):Ih(!0),f)throw _.dispose(),_=void 0,Error("Microphone preview was cancelled");await t.start(_.urls[0],{...fh,...y})}catch(q){throw await u(),q}},stop:u}}function bm(h,m){return{create(t){let _=new y0(t),M,f=!1,u=()=>{f=!0;try{_.stop()}finally{M?.dispose(),M=void 0}};return{async start(y,q){try{if(M=await p("audio_assets",(S)=>_.recordSessionEvent(S),()=>h?Eh(h):Promise.resolve(Ih())),f)throw Error("Audio session was cancelled");await _.start(y,M.urls[0],M.urls[1],q,m())}catch(S){throw u(),S}},stop:u,recordSessionEvent:(y)=>_.recordSessionEvent(y),setMuted:(y)=>_.setMuted(y),sendDTMF:(y)=>_.sendDTMF(y),setOutputVolume:(y)=>_.setOutputVolume(y),startRingback:(y)=>_.startRingback(y),stopRingback:()=>_.stopRingback()}}}}function a0(h){let m,t=()=>{m?.cancel(),m=void 0},_=()=>{if(!m)return;let f=m.take();return m=void 0,f},M=bm(h,_);return{async preflight(f,u){t();let y=new E0(sh(f),f.mediaTransport==="webrtc"||f.mediaTransport==="auto"?{latencyHint:"interactive"}:{sampleRate:24000,latencyHint:"interactive"},u);m=y;try{await Promise.all([y.ready,D0(h,f,u)])}catch(q){if(y.cancel(),m===y)m=void 0;throw q}},cancelPreparation:t,isPrepared:()=>m?.isReady()??!1,prepare:(f,u)=>D0(h,f,u),create(f){return K0(f,()=>M.create(f),()=>{let u=new g0(f),y,q=!1;return{async start(S,s){try{if(y=await p("audio_assets",(b)=>u.recordSessionEvent(b),()=>h?Eh(h,!0):Promise.resolve(Ih(!0))),q)throw Error("Audio session cancelled");await u.start(S,s,y.urls[0],_())}catch(b){throw u.stop(),y?.dispose(),y=void 0,b}},stop(){q=!0,u.stop(),y?.dispose(),y=void 0},setMuted:(S)=>u.setMuted(S),sendDTMF:(S)=>u.sendDTMF(S),setOutputVolume:(S)=>u.setOutputVolume(S),recordSessionEvent:(S)=>u.recordSessionEvent(S),startRingback:(S)=>u.startRingback(S),stopRingback:()=>u.stopRingback()}})}}}async function D0(h,m,t){if(!h||typeof location>"u"||new URL(h.mcpURL(),location.href).origin!==location.origin)return;await p("audio_assets_prepare",t,async()=>{(await Eh(h,m.mediaTransport==="webrtc"||m.mediaTransport==="auto")).dispose()})}function j0(h,m){return{create(t){let _={...fh,...m},M=e(_),f=wh(_),u=new Qh(_.networkProfile),y=[],q,S,s,b,A=!1,x=!1,R=!1,g=!1,F=!1,G=!1,C=0,E,Q,k,N,U,Y,o=!1,xh,yh,Fh=_.outputVolume??1,v={},Uh=32000,P,O=[],W,B,$,T,X=[],w=(V)=>{if(q?.readyState===WebSocket.OPEN&&q.bufferedAmount<65536)q.send(JSON.stringify(V));else throw Error("Listener connection unavailable")},Z=()=>{if(A)throw Error("Listening cancelled")},a=()=>{let V=++C;if(F=G=!1,W?.(V,Error("Coaching cancelled")),T?.(),B=void 0,xh)clearInterval(xh);if(xh=void 0,P?.remove("coach"),U)U.onconnectionstatechange=null,U.close(),U=void 0;if(Q?.disconnect(),Q=void 0,k?.disconnect(),k=void 0,N?.disconnect(),N=void 0,E?.getTracks().forEach((H)=>{H.onended=null,H.stop()}),E=void 0,g&&x&&q?.readyState===WebSocket.OPEN)try{w({type:"coach.stop",generation:V})}catch{}t.onTalking?.(!1)},n=()=>a(),Ah=()=>{if(document.visibilityState!=="visible")a()},Wh=()=>{if(A)return;if(a(),A=!0,P?.stop(),P=void 0,$?.(),window.removeEventListener("blur",n),document.removeEventListener("visibilitychange",Ah),yh)clearInterval(yh);if(yh=void 0,q)q.onmessage=q.onclose=q.onerror=null,q.close(1000,"listener_shutdown");if(S)S.ontrack=S.onconnectionstatechange=null,S.close();for(let V of X)V.pause(),V.srcObject=null;if(b?.disconnect(),s)s.onstatechange=null,s.close().catch(()=>{});Y?.dispose(),Y=void 0},L=(V)=>{if(!A)Wh(),t.onClose(V)},hh=async(V,H)=>{if(await V.setLocalDescription(await V.createOffer()),await new Promise((z,J)=>{if(V.iceGatheringState==="complete"){z();return}let d=setTimeout(()=>{V.onicegatheringstatechange=null,J(Error("ICE gathering timed out"))},7000);V.onicegatheringstatechange=()=>{if(V.iceGatheringState==="complete"||!H())clearTimeout(d),V.onicegatheringstatechange=null,H()?z():J(Error("Coaching cancelled"))}}),!H())throw Error("Listening cancelled");return V.localDescription.sdp};return{async start(V,H){if(g=H?.coaching===!0,Z(),typeof RTCPeerConnection!=="function")throw Error("WebRTC audio unavailable");try{if(s=new AudioContext({latencyHint:"interactive"}),await s.resume(),Z(),_.outputDeviceId&&"setSinkId"in s)await s.setSinkId(_.outputDeviceId);if(Z(),b=s.createGain(),b.gain.value=Fh,b.connect(s.destination),s.onstatechange=()=>{if(s?.state==="suspended"||s?.state==="interrupted")L("listener_audio_paused")},await new Promise((d,l)=>{let j=!1,mh=!1,Rh=!1,D=(Ph)=>{if(j)return;j=!0,clearTimeout(h0),$=void 0,Ph?l(Rh?Object.assign(Ph,{code:"turn_relay_required"}):Ph):d()},h0=setTimeout(()=>D(Error("WebRTC listener timed out")),18000),jh=()=>{if(R&&mh&&!A&&!x)x=!0,D(),t.onReady("webrtc")};$=()=>D(Error("Listening cancelled")),q=new WebSocket(A0(V,_.webrtcFec,_)),q.onmessage=({data:Ph})=>{if(A)return;if(typeof Ph!=="string"){D(Error("Invalid WebRTC listener frame")),L("listener_protocol_error");return}let K;try{K=JSON.parse(Ph)}catch{D(Error("Invalid listener control")),L("listener_protocol_error");return}if(K.type==="webrtc.error"){D(Object.assign(Error("Required TURN relay is unavailable"),{code:K.code}));return}if(P?.handle(K))return;if(K.type==="webrtc.config"){if(Rh=K.ice_transport_policy==="relay",K.network_policy)try{let i=Ch(K.network_policy);f=wh({..._,networkProfile:i}),u=new Qh(i)}catch{D(Error("Invalid network policy"));return}if(S||K.listener!==!0){D(Error("Invalid WebRTC listener configuration"));return}try{v=Hh(K)}catch(i){D(i);return}Uh=K.bitrate??32000,S=new RTCPeerConnection(v);let _h=S;P=new Th(w,()=>!A,(i,Jh)=>{if(O.push(Mh({...Jh,timestamp:new Date().toISOString(),action:"ice_restart",outcome:i})),O.length>50)O.shift()},(i)=>{v=i}),P.register("main",_h);for(let i=0;i<2;i++)_h.addTransceiver("audio",{direction:"recvonly"});_h.ontrack=(i)=>{if(A||!s||!b)return;y.push(i.receiver);let Jh=_h.getTransceivers().findIndex((Vh)=>Vh.receiver===i.receiver);if(Jh<0||Jh>1){L("listener_protocol_error");return}if("jitterBufferTarget"in i.receiver)try{i.receiver.jitterBufferTarget=M.initialTargetMs}catch{}let B0=new MediaStream([i.track]),ih=new Audio;ih.muted=!0,ih.srcObject=B0,X.push(ih),ih.play().catch(()=>{});let Q0=s.createMediaStreamSource(B0);if(m.stereo){let Vh=s.createStereoPanner();Vh.pan.value=Jh===0?-1:1,Q0.connect(Vh).connect(b)}else{let Vh=s.createGain();Vh.gain.value=0.5,Q0.connect(Vh).connect(b)}},_h.onconnectionstatechange=()=>{if(A)return;if(_h.connectionState==="connected")R=!0,jh();if(_h.connectionState==="failed"||_h.connectionState==="closed"){if(D(Error("WebRTC listener connection failed")),j&&x)L("listener_network_error")}},hh(_h,()=>!A).then((i)=>w({type:"webrtc.offer",sdp:i,ice_version:1})).catch((i)=>D(i));return}if(K.type==="webrtc.answer"){S?.setRemoteDescription({type:"answer",sdp:K.sdp}).catch((_h)=>D(_h));return}if(K.type==="listener.ready"){if(g&&K.coaching!==!0){D(Error("Coaching not authorized"));return}mh=!0,jh();return}if(K.type==="coach.started"){W?.(K.generation);return}if(K.type==="coach.rejected"){W?.(C,Error(K.detail||"Coaching unavailable"));return}if(K.type==="coach.stopped"&&(K.generation===void 0||K.generation===C)){a();return}if(K.type==="coach.webrtc.answer"){B?.(K.sdp,K.generation);return}},q.onerror=()=>{if(D(Error("Listener connection failed")),x)L("listener_network_error")},q.onclose=(Ph)=>{if(D(Error(Ph.reason||"Listener disconnected")),x)L(Ph.reason||"listener_disconnected")}}),Z(),g)window.addEventListener("blur",n),document.addEventListener("visibilitychange",Ah);let z,J=!1;yh=setInterval(()=>{if(A||!S||J)return;J=!0,S.getStats().then((d)=>{if(A)return;let l=P0(d,z);if(z=l.previous,f.observe({...l.metrics,receiver_jitter:l.webrtc.jitterMs/1000}),u.observe(l.metrics,performance.now(),S?.connectionState==="connected"&&s?.state==="running")){if(O.push({timestamp:new Date().toISOString(),action:"network_quality",outcome:"sustained_degradation"}),O.length>50)O.shift()}for(let j of y)if("jitterBufferTarget"in j)try{j.jitterBufferTarget=f.target}catch{}t.onDiagnostics?.({session_events:O.slice(),media_transport:"webrtc",dropped_ms:[],played_ms:[],max_queue_ms:l.queueMs,max_late_ms:0,sequence_gaps:[],network_excess_ms:0,network_dropped_ms:[],webrtc:l.webrtc})}).catch(()=>{}).finally(()=>{J=!1})},1000)}catch(z){throw Wh(),z}},async startTalking(){if(Z(),!g||!x||!s)throw Error("Coaching session not ready");if(F)return;F=!0;let V=++C,H=()=>!A&&F&&C===V;try{let z=await navigator.mediaDevices.getUserMedia({audio:sh(_)});if(!H()){z.getTracks().forEach((d)=>d.stop());return}if(E=z,E.getTracks().forEach((d)=>{d.onended=()=>a()}),!Y){let d=await Eh(h,!0);if(!H()){d.dispose();return}Y=d}if(!o)await s.audioWorklet.addModule(Y.urls[0]),o=!0;if(!H())return;if(await new Promise((d,l)=>{let j=setTimeout(()=>{W=void 0,l(Error("Coaching activation timed out"))},3000);W=(mh,Rh)=>{if(Rh||mh===V)clearTimeout(j),W=void 0,Rh?l(Rh):d()},w({type:"coach.start",generation:V})}),!H())return;xh=setInterval(()=>{if(H())try{w({type:"coach.keepalive",generation:V})}catch{a()}},1000),Q=new AudioWorkletNode(s,"softphone-capture",{outputChannelCount:[1],processorOptions:{nativeOutput:!0,inputGainDB:_.inputGainDB,highpassFilter:_.highpassFilter}}),N=s.createMediaStreamDestination(),N.channelCount=1,k=s.createMediaStreamSource(z),k.connect(Q).connect(N);let J=new RTCPeerConnection(v);if(U=J,P?.register("coach",J,H,V),J.addTransceiver(N.stream.getAudioTracks()[0],{direction:"sendonly",streams:[N.stream],sendEncodings:[{maxBitrate:Uh}]}),await new Promise((d,l)=>{let j=!1,mh=(D)=>{if(j)return;j=!0,clearTimeout(Rh),T=void 0,B=void 0,D?l(D):d()},Rh=setTimeout(()=>mh(Error("Coaching WebRTC timed out")),18000);T=()=>mh(Error("Coaching cancelled")),B=(D,h0)=>{if(h0!==V||!H())return;J.setRemoteDescription({type:"answer",sdp:D}).catch((jh)=>mh(jh))},J.onconnectionstatechange=()=>{if(!H())return;if(J.connectionState==="connected")mh();if(J.connectionState==="failed"||J.connectionState==="closed")mh(Error("Coaching media disconnected")),a()},hh(J,H).then((D)=>{if(H())w({type:"coach.webrtc.offer",generation:V,sdp:D})}).catch((D)=>mh(D))}),H())G=!0,t.onTalking?.(!0)}catch(z){if(H())throw a(),z}},stopTalking:a,stop:Wh,setOutputVolume(V){if(!Number.isFinite(V)||V<0||V>1)throw RangeError("Listener volume must be 0–1");if(Fh=V,b)b.gain.value=V}}}}}function zh(h={},m={}){let t=["turnTransport","iceTransportPolicy","turnTLSPort","mediaTransport","webrtcFec","inputDeviceId","outputDeviceId","outputVolume","echoCancellation","noiseSuppression","autoGainControl","inputGainDB","highpassFilter","playbackTargetMs","playbackMinMs","playbackMaxMs","playbackAdaptive","stereo"],_={...fh};for(let M of[h,m])for(let f of t){let u=M[f];if(u!==void 0)_[f]=u}if(e(_),_.mediaTransport=nh(_.mediaTransport),_.webrtcFec??=!0,_.outputVolume!==void 0&&(!Number.isFinite(_.outputVolume)||_.outputVolume<0||_.outputVolume>1))throw RangeError("Listener volume must be 0–1");return _}function xm(h,m,t,_,M=!1){let f,u=!1,y=!1,q,S={...h,onReady(s){y=!0,h.onReady(s)}};return{async start(s,b){if(u)throw Error("Listening cancelled");f=m==="websocket"?t(S):_(S);try{await f.start(s,b)}catch(A){if(f.stop(),u||y||m!=="auto"||ah(A)||M)throw A;f=t(S),await f.start(s,b)}if(u)f.stop();else if(q!==void 0)f.setOutputVolume(q)},async startTalking(){if(u||!f?.startTalking)throw Error("Coaching session not ready");await f.startTalking()},stopTalking(){f?.stopTalking?.()},stop(){u=!0,f?.stop()},setOutputVolume(s){if(!Number.isFinite(s)||s<0||s>1)throw RangeError("Listener volume must be 0–1");q=s,f?.setOutputVolume(s)}}}function C0(h,m={}){let t=zh({},m),_=nh(t.mediaTransport),M=gm(h,t),f=j0(h,t);return{create(u){return xm(u,_,(y)=>M.create(y),(y)=>f.create(y),t.iceTransportPolicy==="relay")}}}function Am(h){if(h.byteLength<26||h.byteLength>984||h.byteLength%2)throw Error("Invalid listener audio frame");let m=new DataView(h),t=m.getUint32(4,!0);if(m.getUint32(0,!0)!==827085889||t>1)throw Error("Invalid listener audio protocol");let _=Number(m.getBigUint64(8,!0)),M=Number(m.getBigUint64(16,!0));if(!Number.isSafeInteger(_)||!Number.isSafeInteger(M))throw Error("Invalid listener audio timing");let f=new Float32Array((h.byteLength-24)/2);for(let u=0;u<f.length;u++)f[u]=m.getInt16(24+u*2,!0)/32768;return{direction:t,sequence:_,timestampMS:M,frame:f}}function Pm(h,m,t,_){if(h.length<1||h.length>480||!Number.isFinite(_)||_<0||!Number.isInteger(m)||m<1||m>4294967295)throw Error("Invalid coaching capture");let M=new ArrayBuffer(24+h.length*2),f=new DataView(M);f.setUint32(0,826496833,!0),f.setUint32(4,m,!0),f.setUint32(8,t>>>0,!0),f.setFloat64(16,_,!0);for(let u=0;u<h.length;u++){let y=Math.max(-1,Math.min(1,h[u]));f.setInt16(24+u*2,y<0?y*32768:y*32767,!0)}return M}function gm(h,m={}){return{create(t){let _,M,f,u,y=!1,q=[],S=!1,s=0,b=!1,A=!1,x,R,g,F,G,C,E,Q=!1,k,N,U=0,Y=!1,o=[0,0],xh=[0,0],yh=[0,0],Fh=[0,0],v=[void 0,void 0],Uh=[new gh,new gh],P=m.outputVolume??1,O,W=()=>{let Z=++s;if(b=!1,A=!1,E?.(Z,Error("Coaching cancelled")),C)clearInterval(C);if(C=void 0,G?.port1.close(),G?.port2.close(),G=void 0,R?.disconnect(),R=void 0,g?.disconnect(),g=void 0,F?.disconnect(),F=void 0,x?.getTracks().forEach((a)=>{a.onended=null,a.stop()}),x=void 0,S&&Y&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.stop",generation:Z}));t.onTalking?.(!1)},B=()=>W(),$=()=>{if(document.visibilityState!=="visible")W()},T=()=>{if(y)return;if(W(),y=!0,O?.(),window.removeEventListener("blur",B),document.removeEventListener("visibilitychange",$),u)u.onmessage=u.onclose=u.onerror=null,u.close(1000,"listener_shutdown");if(M?.disconnect(),f?.disconnect(),_)_.onstatechange=null,_.close().catch(()=>{});for(let Z of q.splice(0))Z.dispose()},X=(Z)=>{if(!y)T(),t.onClose(Z)},w=()=>{if(y)throw Error("Listening cancelled")};return{async start(Z,a){S=a?.coaching===!0,w();try{if(_=new AudioContext({latencyHint:"interactive"}),_.state==="suspended")await _.resume();if(w(),m.outputDeviceId&&"setSinkId"in _)await _.setSinkId(m.outputDeviceId);w();let n=await kh(h,"listener");if(y)n.dispose(),w();if(q.push(n),await _.audioWorklet.addModule(n.urls[0]),w(),M=new AudioWorkletNode(_,"telephony-listener",{numberOfInputs:0,outputChannelCount:[m.stereo?2:1],processorOptions:{stereo:m.stereo,...e(m)}}),f=_.createGain(),f.gain.value=P,M.connect(f).connect(_.destination),M.onprocessorerror=()=>X("listener_audio_error"),M.port.onmessage=({data:Ah})=>{if(Ah?.type==="listener.diagnostics"&&!y)try{t.onDiagnostics?.({...Ah,media_transport:"websocket",context_state:_?.state,context_rate:_?.sampleRate,context_time_ms:(_?.currentTime??0)*1000,received_ms:[...xh],scheduled_ms:[...yh],sequence_gaps:[...Fh],network_excess_ms:U,network_dropped_ms:[...o]})}catch{}},_.onstatechange=()=>{if(_?.state==="suspended"||_?.state==="interrupted")X("listener_audio_paused")},await new Promise((Ah,Wh)=>{let L=!1,hh=setTimeout(()=>{V(Error("Listener connection timed out")),X("listener_network_error")},1e4),V=(H)=>{if(L)return;L=!0,clearTimeout(hh),O=void 0,H?Wh(H):Ah()};O=()=>V(Error("Listening cancelled")),u=new WebSocket(Z),u.binaryType="arraybuffer",u.onmessage=({data:H})=>{if(y||!_||!M)return;try{if(typeof H==="string"){let j=JSON.parse(H);if(j.type==="listener.ready"){if(S&&j.coaching!==!0)throw Error("Coaching not authorized");Y=!0,V(),t.onReady("websocket")}else if(j.type==="coach.started")E?.(j.generation);else if(j.type==="coach.rejected")E?.(s,Error(j.detail||"Coaching unavailable"));else if(j.type==="coach.stopped"&&(j.generation===void 0||j.generation===s))W();return}if(!Y||!(H instanceof ArrayBuffer))return;let z=Am(H),J=z.direction;if(xh[J]+=z.frame.length*1000/24000,v[J]!==void 0&&z.sequence<v[J])return;if(v[J]!==void 0&&z.sequence>v[J])Fh[J]+=z.sequence-v[J];v[J]=z.sequence+1;let d=performance.now()-z.timestampMS;if(N=Math.min(N??d,d),U=Math.max(0,d-N),U>200){o[J]+=z.frame.length*1000/24000;return}k??=_.currentTime*1000+(m.playbackTargetMs??60)-z.timestampMS;let l=Uh[J].process(z.frame,24000,_.sampleRate);yh[J]+=z.frame.length*1000/24000,M.port.postMessage({direction:J,frame:l,playAtMS:z.timestampMS+k},[l.buffer])}catch{V(Error("Invalid listener media")),X("listener_protocol_error")}},u.onerror=()=>{V(Error("Listener connection failed")),X("listener_network_error")},u.onclose=(H)=>{V(Error(H.reason||"Listener disconnected")),X(H.reason||"listener_disconnected")}}),w(),S)window.addEventListener("blur",B),document.addEventListener("visibilitychange",$)}catch(n){throw T(),n}},async startTalking(){if(w(),!S||!Y||!_||!u||u.readyState!==WebSocket.OPEN)throw Error("Coaching session not ready");if(b)return;b=!0;let Z=++s,a=()=>!y&&b&&Z===s;try{let n=await navigator.mediaDevices.getUserMedia({audio:sh({...fh,...m})});if(!a()){n.getTracks().forEach((L)=>L.stop());return}if(x=n,x.getAudioTracks().forEach((L)=>{L.onended=()=>W()}),!Q){let L=await Eh(h,!0);if(!a()){L.dispose();return}q.push(L),await _.audioWorklet.addModule(L.urls[0]),Q=!0}if(!a())return;if(await new Promise((L,hh)=>{let V=setTimeout(()=>{E=void 0,hh(Error("Coaching activation timed out"))},3000);E=(H,z)=>{if(z||H===Z)clearTimeout(V),E=void 0,z?hh(z):L()},u.send(JSON.stringify({type:"coach.start",generation:Z}))}),!a())return;R=new AudioWorkletNode(_,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:m.inputGainDB??0,highpassFilter:m.highpassFilter!==!1}}),g=_.createMediaStreamSource(n),F=_.createGain(),F.gain.value=0,g.connect(R).connect(F).connect(_.destination),G=new MessageChannel;let Ah=0,Wh=new gh;G.port1.onmessage=({data:L})=>{if(!a()||!A||L?.type!=="capture"||!(L.frame instanceof Float32Array)||!_||!u)return;if(!Number.isFinite(L.timestamp_ms)||_.currentTime*1000-L.timestamp_ms>150||u.bufferedAmount>2880||u.readyState!==WebSocket.OPEN)return;let hh=Wh.process(L.frame,L.sample_rate,24000);if(hh.length>0&&hh.length<=480)u.send(Pm(hh,Z,Ah++,L.timestamp_ms))},G.port1.start(),R.port.postMessage({type:"transport",port:G.port2},[G.port2]),A=!0,t.onTalking?.(!0),C=setInterval(()=>{if(a()&&u?.readyState===WebSocket.OPEN)u.send(JSON.stringify({type:"coach.keepalive",generation:Z}))},1000)}catch(n){if(a())throw W(),n}},stopTalking:W,stop:T,setOutputVolume(Z){if(!Number.isFinite(Z)||Z<0||Z>1)throw RangeError("Listener volume must be 0–1");if(P=Z,f)f.gain.value=Z}}}}}class F0{client;options;snapshot=Object.freeze({state:"idle"});observers=new Set;generation=0;attempt=0;session;audio;lease;requestAbort;retry;retryUntil=0;retryDelay=500;disposed=!1;coaching=!1;runtime;audioOptions;constructor(h,m={}){this.client=h;this.options=m;this.audioOptions=zh(m.audio,m),this.runtime=m.runtime??C0(h.app,this.audioOptions)}setAudioOptions(h){let m=zh({},h);this.audioOptions=m,this.runtime=this.options.runtime??C0(this.client.app,m)}getAudioOptions=()=>Object.freeze({...this.audioOptions});getSnapshot=()=>this.snapshot;subscribe=(h)=>{return this.observers.add(h),()=>{this.observers.delete(h)}};update(h){this.snapshot=Object.freeze({...this.snapshot,...h});for(let m of this.observers)try{m(this.snapshot)}catch{}}async listen(h){return this.begin(h,!1)}async coach(h){return this.begin(h,!0)}async begin(h,m){if(this.client.cancelNetworkChecks(),this.disposed)throw Error("Listener disposed");await this.stop(),this.coaching=m,this.retryUntil=0,this.retryDelay=500;let t=++this.generation;return this.update({state:"connecting",callId:h,detail:void 0,coaching:m,talking:!1,mediaTransport:void 0}),this.connect(h,t)}async connect(h,m){this.requestAbort?.abort(),this.requestAbort=new AbortController;let t,_=++this.attempt,M=()=>m===this.generation&&_===this.attempt&&!this.disposed;try{if(t=this.coaching?await this.client.coachSession(h):await this.client.listenSession(h,this.requestAbort?.signal),!M()){await this.client.stopListening(t).catch(()=>{});return}this.session=t;let f=this.runtime.create({onReady:(u)=>{if(M())this.retryUntil=0,this.retryDelay=500,this.update({state:"listening",detail:void 0,mediaTransport:u})},onClose:(u)=>{if(M())this.disconnected(u,h,m)},onTalking:(u,y)=>{if(M())this.update({talking:u,detail:y})},onDiagnostics:(u)=>{if(M())try{this.options.onDiagnostics?.(u)}catch{}}});if(this.audio=f,this.lease=new Lh(t.lease_seconds,()=>this.client.renewListening(t),(u)=>{if(M())this.disconnected(u==="revoked"?"access_revoked":"media_disconnected",h,m,u==="expired"?"Listener session expired":"Listener access revoked")},this.options.onSessionEvent,t.lease_started_ms),await f.start(this.client.listenerMediaURL(t,this.audioOptions.mediaTransport),{coaching:t.coaching===!0}),!M())f.stop()}catch(f){if(!M()||t&&this.session!==t)return;let u=f?.status,y;try{y=JSON.parse(f.body??"{}").code}catch{}let q=y==="call_ended"?"call_ended":u===401||u===403||u===404?"access_revoked":"listener_disconnected";throw this.disconnected(q,h,m,String(f)),f}}cleanup(){this.requestAbort?.abort(),this.requestAbort=void 0,this.lease?.stop(),this.lease=void 0;let h=this.audio;this.audio=void 0,h?.stop();let m=this.session;return this.session=void 0,m?this.client.stopListening(m).catch(()=>{}):Promise.resolve()}disconnected(h,m,t,_=h){if(t!==this.generation||this.disposed)return;if(this.cleanup(),this.retry)return;let M=h==="call_ended",f=h==="access_revoked",u=["listener_disconnected","listener_network_error","media_disconnected","media_replaced"].includes(h);if(!this.coaching&&!M&&!f&&u&&this.options.reconnect!==!1){this.retryUntil||=Date.now()+30000;{this.update({state:"reconnecting",detail:_,talking:!1}),this.retry=setTimeout(()=>{if(this.retry=void 0,t===this.generation&&!this.disposed)this.connect(m,t).catch(()=>{})},Date.now()<this.retryUntil?this.retryDelay:15000),this.retryDelay=Math.min(4000,this.retryDelay*2);return}}this.update({state:M?"call_ended":f?"access_revoked":"disconnected",detail:_,talking:!1})}async startTalking(){if(this.snapshot.state!=="listening"||!this.session?.coaching||!this.audio?.startTalking)throw Error("Join private coaching before talking");await this.audio.startTalking()}stopTalking(){this.audio?.stopTalking?.(),this.update({talking:!1})}setOutputVolume(h){this.audio?.setOutputVolume(h)}async stop(){if(++this.generation,this.retry)clearTimeout(this.retry);this.retry=void 0;let h=this.cleanup();this.update({state:"idle",callId:void 0,detail:void 0,coaching:!1,talking:!1,mediaTransport:void 0}),await h}async dispose(){await this.stop(),this.disposed=!0,this.observers.clear()}}class R0{client;host;watch;session=crypto.randomUUID();generation=0;disposed=!1;state={available:!1,designated:!1,state:"off"};heartbeat;countdown;watcher;offered=new Map;ignored=new Set;reserved=new Set;pending=!1;answerNowAction;constructor(h,m,t){this.client=h;this.host=m;this.watch=t}update(h){this.state={...this.state,...h},this.host.changed({...this.state})}async setAvailable(h,m=!1){if(this.disposed)throw Error("Softphone has been disposed");if(this.host.busy()&&h)throw Error("Finish the current call before enabling automatic answering");let t=++this.generation;if(clearTimeout(this.heartbeat),this.watcher?.close(),this.watcher=void 0,this.cancel(),!h){this.host.releasePreparation(),this.update({available:!1,designated:!1,state:"off",detail:void 0}),await this.client.autoAnswerSession({session_id:this.session,available:!1});return}this.update({available:!1,designated:!1,state:"preparing",detail:void 0});try{if(await this.host.prepare(),t!==this.generation||this.disposed){if(!this.state.available)this.host.releasePreparation();return}let _=await this.client.autoAnswerSession({session_id:this.session,available:!0,takeover:m});if(t!==this.generation||this.disposed){await this.client.autoAnswerSession({session_id:this.session,available:!1});return}if(!_.designated)throw Error("Another browser is available for this adviser. Choose Use this browser to move automatic answering here.");if(this.update({available:!0,designated:!0,state:"available"}),this.scheduleHeartbeat(t),this.watch)this.watcher=this.client.watchCalls((M)=>this.observe(M),{onError:(M)=>this.update({detail:String(M)})});this.consider()}catch(_){if(t===this.generation)this.host.releasePreparation(),this.update({available:!1,designated:!1,state:"blocked",detail:_ instanceof Error?_.message:String(_)});throw _}}scheduleHeartbeat(h){clearTimeout(this.heartbeat),this.heartbeat=setTimeout(()=>{this.client.autoAnswerSession({session_id:this.session,available:!0}).then((m)=>{if(h!==this.generation||this.disposed)return;if(!m.designated)throw Error("Automatic answering moved to another browser");this.scheduleHeartbeat(h)}).catch((m)=>{if(h!==this.generation||this.disposed)return;this.generation++,this.cancel(),this.watcher?.close(),this.watcher=void 0,this.host.releasePreparation(),this.update({available:!1,designated:!1,state:"blocked",detail:String(m)})})},1e4)}observe(h){this.offered=new Map(h.filter(eh).map((m)=>[m.id,m]));for(let m of this.ignored)if(!h.some((t)=>t.id===m))this.ignored.delete(m);if(this.state.callId&&!this.offered.has(this.state.callId)&&this.state.state!=="answering")this.cancel();this.consider()}cancel(h=this.state.callId){if(clearTimeout(this.countdown),this.countdown=void 0,this.answerNowAction=void 0,h){if(this.ignored.add(h),this.reserved.delete(h))this.client.autoAnswerOffer({session_id:this.session,call_id:h,cancel:!0}).catch(()=>{})}this.update({callId:void 0,answerAt:void 0,source:void 0,state:this.state.available?"available":"off"})}answerNow(){if(this.state.state!=="countdown")return;clearTimeout(this.countdown),this.answerNowAction?.(),this.answerNowAction=void 0}consider(){if(this.disposed||!this.state.available||!this.state.designated||this.pending||this.state.callId||this.host.busy())return;let h=[...this.offered.values()].find((_)=>!this.ignored.has(_.id));if(!h)return;let m=this.generation,t=Date.now();this.pending=!0,this.client.autoAnswerOffer({session_id:this.session,call_id:h.id,destination_id:h.ring_offers?.find((_)=>_.kind==="browser")?.destination_id}).then((_)=>{if(m!==this.generation||this.disposed||!this.state.available)return;if(!_.eligible)return;if(this.reserved.add(h.id),this.ignored.has(h.id)){this.cancel(h.id);return}if(!this.offered.has(h.id)||this.host.busy()){this.cancel(h.id);return}this.ignored.add(h.id),this.update({callId:h.id,source:_.effective.source,offerReceivedAt:t,detail:void 0,state:_.effective.delay_ms?"countdown":"answering",answerAt:Date.now()+_.effective.delay_ms});let M=()=>{if(this.answerNowAction=void 0,m!==this.generation||this.disposed||!this.offered.has(h.id)||this.host.busy()||this.state.callId!==h.id){this.cancel(h.id);return}this.update({state:"answering",answerAt:void 0,answerStartedAt:Date.now()}),this.host.answer(h.id,_.destination_id??"",this.session).then(()=>{if(m===this.generation)this.reserved.delete(h.id),this.update({callId:void 0,state:"available"})}).catch((f)=>{if(this.cancel(h.id),m===this.generation)this.update({detail:f instanceof Error?f.message:String(f)})})};if(this.answerNowAction=M,_.effective.delay_ms===0)M();else this.countdown=setTimeout(M,_.effective.delay_ms)}).catch((_)=>{if(m===this.generation)this.ignored.add(h.id),this.update({detail:_ instanceof Error?_.message:String(_)})}).finally(()=>{this.pending=!1})}dispose(){if(this.disposed)return;if(this.disposed=!0,this.generation++,clearTimeout(this.heartbeat),this.watcher?.close(),this.cancel(),this.state.available)this.client.autoAnswerSession({session_id:this.session,available:!1}).catch(()=>{});this.host.releasePreparation()}}var Em={set:(h,m)=>setTimeout(h,m),clear:(h)=>clearTimeout(h)};class G0{run;permanent;ended;event;clock;stopped=!1;active;timer;attempt=0;online=()=>this.wake();constructor(h,m,t,_,M=Em){this.run=h;this.permanent=m;this.ended=t;this.event=_;this.clock=M;globalThis.addEventListener?.("online",this.online),this.schedule(1000)}emit(h){try{this.event?.(h)}catch{}}schedule(h){if(!this.stopped)this.clock.clear(this.timer),this.timer=this.clock.set(()=>void this.tick(),h)}wake(){if(!this.stopped&&!this.active)this.schedule(0)}async tick(){if(this.stopped||this.active)return;let h=new AbortController;this.active=h,this.attempt++,this.emit("attempt_started");let m;try{if(await Promise.race([this.run(h.signal),new Promise((t,_)=>{m=this.clock.set(()=>{h.abort(),_(Error("Audio recovery request timed out"))},30000)})]),this.stopped)return;this.emit("connected"),this.stop()}catch(t){if(this.stopped)return;if(this.permanent(t)){this.emit("revoked"),this.stop(),this.ended(t);return}this.emit("waiting_for_network"),this.schedule(Math.min(15000,1000*2**Math.min(4,this.attempt)))}finally{if(this.clock.clear(m),this.active===h)this.active=void 0}}stop(){if(this.stopped)return;this.stopped=!0,this.clock.clear(this.timer),this.active?.abort(),globalThis.removeEventListener?.("online",this.online)}}function i0(h){if(!h)return"placing";if(rh(h))return"ended";if(h==="ringing")return"ringing";if(h==="answered"||h==="in-progress")return"connected";return"placing"}var uh=(h)=>h instanceof Error?h.message:String(h);class O0{client;options;snapshot=Object.freeze({audioState:"idle",busy:!1,muted:!1,phase:"idle"});listeners=new Set;audio;session;disposed=!1;generation=0;cancellation=new AbortController;hangingUp;controlPending;intent;lease;timer;polling;hasStatusPush=!1;automaticRecovery;hasLiveAudio=!1;uncertainAttachment;scheduleStatusPoll;outbound=!1;ringing=!1;runtime;audioOptions;preferredPlayback={};preferredMediaTransport;networkTimer;networkRequest;networkChanged=()=>{if(this.disposed)return;if(this.snapshot.networkCheck)this.update({networkCheck:{...this.snapshot.networkCheck,state:"inconclusive"}});this.scheduleNetworkCheck(1000)};interval;autoAnswer;autoPrepared=!1;automaticAnswerStartedAt;constructor(h,m={}){this.client=h;this.options=m;if(this.runtime=m.audioRuntime??a0(h.app),this.audioOptions={...fh,...m.audio,...m.mediaTransport?{mediaTransport:m.mediaTransport}:{}},this.preferredMediaTransport=this.audioOptions.mediaTransport,this.preferredPlayback={playbackTargetMs:this.audioOptions.playbackTargetMs,playbackMinMs:this.audioOptions.playbackMinMs,playbackMaxMs:this.audioOptions.playbackMaxMs},e(this.audioOptions),this.interval=m.pollIntervalMs??2000,!Number.isFinite(this.interval)||this.interval!==0&&this.interval<100)throw Error("Call poll interval must be 0 or at least 100 ms");if(this.autoAnswer=new R0(h,{busy:()=>this.disposed||this.snapshot.busy||Boolean(this.session)||Boolean(this.hangingUp),prepare:async()=>{this.cancelNetworkCheck(),await this.runtime.preflight(this.audioOptions),this.autoPrepared=!0},releasePreparation:()=>{if(this.autoPrepared)this.runtime.cancelPreparation?.(),this.autoPrepared=!1},answer:(t,_,M)=>this.answer(t,{destination_id:_,auto_answer_session:M}),changed:(t)=>{this.update({autoAnswer:t});try{this.options.onAutoAnswer?.(t)}catch{}}},this.interval!==0),m.networkCheck)globalThis.addEventListener?.("online",this.networkChanged),this.scheduleNetworkCheck(500+Math.random()*15000)}scheduleNetworkCheck(h=300000+Math.random()*60000){if(!this.options.networkCheck||this.disposed)return;clearTimeout(this.networkTimer),this.networkTimer=setTimeout(()=>{if(!this.session&&!this.snapshot.busy&&(typeof document>"u"||document.visibilityState!=="hidden"))this.checkNetwork().catch(()=>{});else this.scheduleNetworkCheck()},h)}cancelNetworkCheck(){if(this.snapshot.networkChecking)this.update({networkChecking:!1});clearTimeout(this.networkTimer),this.networkRequest?.abort(),this.networkRequest=void 0,this.client.cancelNetworkChecks()}async checkNetwork(h={}){if(this.assertOpen(),this.session||this.snapshot.busy||!this.client.networkChecksAllowed)throw Error("Network checks pause during calls or incoming offers");this.cancelNetworkCheck();let m=new AbortController;this.networkRequest=m;let t=this.client.registerNetworkCheck(m);this.update({networkChecking:!0});let _=()=>m.abort();if(h.signal?.addEventListener("abort",_,{once:!0}),h.signal?.aborted)_();let M;try{let f=new AbortController,u=()=>f.abort();m.signal.addEventListener("abort",u,{once:!0}),M=setTimeout(u,3000);let y=!0;try{let S=await r(this.client.networkPolicy({},f.signal),f.signal),s=Ch(S.effective);if(y=s.background_check,!m.signal.aborted&&!this.session&&!this.snapshot.busy)this.applyNetworkPolicy(S.configured===!1?void 0:s)}catch{}finally{clearTimeout(M),m.signal.removeEventListener("abort",u)}if(!y&&!h.paths)return{state:"unavailable",checkedAt:new Date().toISOString(),paths:[]};let q=await this.client.checkNetwork({...h,signal:m.signal});if(this.networkRequest===m&&!m.signal.aborted){this.update({networkCheck:q});try{this.options.onNetworkCheck?.(q)}catch{}}return q}finally{if(t(),clearTimeout(M),h.signal?.removeEventListener("abort",_),this.networkRequest===m)this.networkRequest=void 0,this.update({networkChecking:!1}),this.scheduleNetworkCheck()}}applyNetworkPolicy(h){if(this.assertOpen(),h===void 0){if(this.audioOptions.networkProfile)this.audioOptions={...this.audioOptions,...this.preferredPlayback,networkProfile:void 0,mediaTransport:this.preferredMediaTransport};return}let m=Ch(h);this.audioOptions={...this.audioOptions,networkProfile:m,playbackTargetMs:m.playback_target_ms,playbackMinMs:Math.min(this.preferredPlayback.playbackMinMs??60,m.playback_target_ms),playbackMaxMs:m.playback_max_ms,mediaTransport:m.media_transport==="inherit"?this.preferredMediaTransport:m.media_transport}}createCallListener(h={}){return this.assertOpen(),this.client.createCallListener({...this.audioOptions,...h.audio,...h})}getSnapshot=()=>this.snapshot;subscribe=(h)=>{return this.assertOpen(),this.listeners.add(h),()=>{this.listeners.delete(h)}};update(h){let m={...this.snapshot,...h};if(m.callId&&i0(m.carrierStatus)==="connected")m.phase=m.audioState==="live"?"connected":"placing";this.snapshot=Object.freeze(m);for(let t of this.listeners)try{t(this.snapshot)}catch{}}assertOpen(){if(this.disposed)throw Error("Softphone has been disposed")}assertCurrent(h){if(this.disposed||h!==this.generation||this.cancellation.signal.aborted)throw Error("Softphone operation cancelled")}invalidate(h=!1){if(!h)this.runtime.cancelPreparation?.(),this.autoPrepared=!1;return this.automaticRecovery?.stop(),this.automaticRecovery=void 0,this.uncertainAttachment=void 0,this.cancellation.abort(),this.cancellation=new AbortController,++this.generation}current(h){return!this.disposed&&h===this.generation}finish(h){if(this.current(h))this.runtime.cancelPreparation?.(),this.update({busy:!1})}cancellable(h,m){let t=this.cancellation.signal;return new Promise((_,M)=>{let f=()=>{t.removeEventListener("abort",f),M(Error("Softphone operation cancelled"))};if(t.addEventListener("abort",f,{once:!0}),h.then(_,M).finally(()=>t.removeEventListener("abort",f)),!this.current(m)||t.aborted)f()})}begin(h,m=!1){if(this.cancelNetworkCheck(),this.assertOpen(),this.snapshot.busy||this.hangingUp||h&&this.session)throw Error("Softphone already has an active operation or call");let t=this.invalidate(m);return this.update({busy:!0,detail:void 0}),t}async prepareAudio(h,m){let t=[],_=(M)=>{if(this.current(h)&&!this.cancellation.signal.aborted)t.push(Mh(M))};try{return await this.cancellable(p(m?"audio_preflight":"audio_prepare",_,()=>m?this.runtime.preflight(this.audioOptions,_):this.runtime.prepare?.(this.audioOptions,_)??Promise.resolve()),h),this.assertCurrent(h),t}catch(M){if(this.current(h))for(let f of t)this.reportSessionEvent(f);throw M}}async dial(h){let m=this.begin(!0),t,_=!1;try{this.assertCurrent(m);let M={to:h.to.trim(),from:h.from?.trim(),timeout_sec:h.timeout_sec,recording:h.recording},f=JSON.stringify(M);if(!this.intent||this.intent.value!==f||h.idempotency_key&&h.idempotency_key!==this.intent.request.idempotency_key)this.intent={value:f,request:{...M,idempotency_key:h.idempotency_key||crypto.randomUUID()}};let u=await this.prepareAudio(m,!0);return this.assertCurrent(m),t=await this.client.place(this.intent.request),this.intent=void 0,this.assertCurrent(m),_=!0,this.outbound=!0,await this.attachAudio(t,m,u),t.call_id}catch(M){if(t&&(this.current(m)||!_||this.disposed))await this.recoverStartup(t,m,()=>this.client.hangup(t.call_id));if(this.current(m))this.update({detail:uh(M)});throw M}finally{this.finish(m)}}setAutoAnswerAvailable(h,m={}){return this.autoAnswer.setAvailable(h,m.takeover)}answerAutoAnswerNow(){this.autoAnswer.answerNow()}cancelAutoAnswer(h){this.autoAnswer.cancel(h)}observeCalls(h){for(let m of h)this.observeCall(m);this.autoAnswer.observe(h)}async answer(h,m={}){if(!m.auto_answer_session)this.autoAnswer.cancel(h);let t=Boolean(m.auto_answer_session&&this.autoPrepared&&this.runtime.isPrepared?.()!==!1),_=this.begin(!0,t),M,f=!1;try{this.assertCurrent(_);let u=t?[]:await this.prepareAudio(_,!0);if(this.autoPrepared=!1,this.automaticAnswerStartedAt=m.auto_answer_session?Date.now():void 0,this.automaticAnswerStartedAt){let y=this.snapshot.autoAnswer?.offerReceivedAt;if(y)u.push({timestamp:new Date(y).toISOString(),action:"automatic_answer",outcome:"offer_received"});u.push({timestamp:new Date(this.automaticAnswerStartedAt).toISOString(),action:"automatic_answer",outcome:"answer_started",code:this.snapshot.autoAnswer?.source,duration_ms:y?this.automaticAnswerStartedAt-y:void 0})}M=await this.client.answer(h,m),this.assertCurrent(_),f=!0,this.outbound=!1,await this.attachAudio(M,_,u)}catch(u){if(M&&(this.current(_)||!f||this.disposed))await this.recoverStartup(M,_,()=>this.client.release(M));if(this.current(_))this.update({detail:uh(u)});throw u}finally{this.finish(_)}}async recoverStartup(h,m,t){try{if(await t(),this.current(m))this.clearCall()}catch{if(this.current(m))this.session=h,this.update({callId:h.call_id,audioState:"error"}),this.startPolling()}}async join(h){await this.answer(h,{rejoin:!0})}async attach(h,m="attach"){await this.acquireSession(h,!1,m)}async takeover(h){await this.acquireSession(h,!0)}async acquireSession(h,m,t="takeover"){let _=this.begin(!0);try{let M=await this.prepareAudio(_,!0);this.assertCurrent(_);let f=Oh(m?"takeover":t),u=await(m?this.client.takeover(h,f):this.client.attach(h,f));this.assertCurrent(_),await this.attachAudio(u,_,M),await this.reconcileAttachedCall(u,_)}catch(M){if(this.current(_))this.update({detail:uh(M)});throw M}finally{this.finish(_)}}async reconnect(h,m="manual_reconnect"){if(!this.session)throw Error("No call to reconnect");let t={...this.audioOptions,...h};e(t);let _=this.begin(!1);this.configureAudio(h??{});let M=this.session.call_id,f=Oh(m),u=this.session.session_generation,y=(q,S={})=>this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:q,...f,previous_session_id:u,...S});y("started");try{let q=await this.prepareAudio(_,!1),S;try{S=await this.acquireRecoverySession(this.session,{...f,previous_session_id:u},this.cancellation.signal)}catch(s){let b=th(s),A;try{A=JSON.parse(s.body??"{}").current_generation}catch{}if(m==="automatic_retry"||b.code!=="media_session_changed"||!A||!/^session-[a-f0-9-]{1,64}$/.test(A))throw s;this.assertCurrent(_),S=await this.client.attach(M,{...Oh(m),previous_session_id:A},{signal:this.cancellation.signal},A)}this.assertCurrent(_),await this.attachAudio(S,_,q),await this.reconcileAttachedCall(S,_),y("connected",{session_id:S.session_generation})}catch(q){if(y(this.current(_)?"failed":"cancelled"),this.current(_))this.update({detail:uh(q)});throw q}finally{this.finish(_)}}async hangup(){if(this.assertOpen(),this.hangingUp)return this.hangingUp;let h=this.session;if(!h){this.cancellation.abort(),this.runtime.cancelPreparation?.();return}let m=this.invalidate();this.stopAudio(),this.update({busy:!0,audioState:"ended"});let t=(async()=>{try{if(await this.client.hangup(h.call_id),this.current(m))this.clearCall()}catch(_){if(this.current(m))this.update({audioState:"error",detail:uh(_)}),this.startPolling();throw _}finally{this.finish(m)}})();this.hangingUp=t;try{await t}finally{if(this.hangingUp===t)this.hangingUp=void 0}}hold(){return this.runCallControl("hold")}resume(){return this.runCallControl("resume")}pauseRecording(){return this.runCallControl("pauseRecording")}resumeRecording(){return this.runCallControl("resumeRecording")}async runCallControl(h){this.assertOpen();let m=this.session;if(!m)throw Error("No active call to control");if(this.snapshot.busy||this.hangingUp||this.controlPending)throw Error("Softphone already has an active operation");let t=this.generation;this.update({busy:!0,detail:void 0});let _;try{_=this.client[h](m.call_id)}catch(M){if(this.current(t)&&this.session===m)this.update({busy:!1,detail:uh(M)});throw M}this.controlPending=_;try{let M=await _;if(this.current(t)&&this.session===m)this.update({holdState:M.hold_state,recordingState:M.recording_state,controlError:M.control_error,capabilities:M.capabilities});return M}catch(M){if(this.current(t)&&this.session===m)this.update({detail:uh(M)});throw M}finally{if(this.controlPending===_)this.controlPending=void 0;if(this.current(t)&&this.session===m)this.update({busy:!1})}}configureAudio(h){this.assertOpen();let m={...this.audioOptions,...h};if(e(m),h.mediaTransport!==void 0)this.preferredMediaTransport=h.mediaTransport;for(let t of["playbackTargetMs","playbackMinMs","playbackMaxMs"])if(t in h)this.preferredPlayback[t]=h[t];if(this.audioOptions=m,m.networkProfile)this.applyNetworkPolicy(m.networkProfile)}setMuted(h){this.assertOpen(),this.audio?.setMuted(h),this.update({muted:h})}setOutputVolume(h){if(this.assertOpen(),!Number.isFinite(h)||h<0||h>1)throw Error("Volume must be between 0 and 1");this.audioOptions.outputVolume=h,this.audio?.setOutputVolume(h)}sendDTMF(h){if(this.assertOpen(),!/^[0-9*#]+$/.test(h))throw Error("Invalid DTMF digits");if(this.snapshot.audioState!=="live")throw Error("Audio is not connected");this.audio?.sendDTMF(h)}observeCall(h){if(this.disposed||h.id!==this.session?.call_id)return;if(h.direction)this.outbound=h.direction==="outbound";if(rh(h.status)){this.invalidate(),this.clearCall(h.status),this.update({busy:!1,phase:"ended",termination:h.termination,answeredBy:h.answered_by??this.snapshot.answeredBy,endedAt:h.ended_at});return}this.update({carrierStatus:h.status,phase:i0(h.status),answeredBy:h.answered_by??this.snapshot.answeredBy,holdState:h.hold_state??this.snapshot.holdState,recordingState:h.recording_state??this.snapshot.recordingState,controlError:h.control_error??this.snapshot.controlError,capabilities:h.capabilities??this.snapshot.capabilities}),this.syncRingback()}ringbackCountry(){let h=this.options.ringback;return typeof h==="object"&&h?h.country:void 0}syncRingback(){let h=this.outbound&&Boolean(this.options.ringback)&&this.snapshot.phase==="ringing"&&this.audio!==void 0;if(h&&!this.ringing){this.ringing=!0;try{this.audio?.startRingback?.(this.ringbackCountry())}catch{this.ringing=!1}}else if(!h&&this.ringing){this.ringing=!1;try{this.audio?.stopRingback?.()}catch{}}}async attachAudio(h,m,t=[]){this.hasStatusPush=!1,this.assertCurrent(m),this.stopAudio(),this.session=h,this.update({callId:h.call_id,carrierStatus:void 0,audioState:"connecting",phase:"placing",termination:void 0,answeredBy:void 0,endedAt:void 0,holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0});let _,M=()=>!this.disposed&&_!==void 0&&this.audio===_,f=(q)=>{if(M())try{q()}catch{}},u,y;try{this.assertCurrent(m),_=this.runtime.create({refreshMediaURL:(S,s)=>{let b=S??Oh("automatic_retry");if(u)return this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"coalesced",...b,shared_attempt_id:y?.attempt_id}),u;if(!M()||this.snapshot.busy)return Promise.reject(Error("Audio recovery is not currently available"));let A=this.generation,x=(async()=>{let g=h.session_generation,F=await this.acquireRecoverySession(h,{...b,previous_session_id:g},s);if(this.assertCurrent(A),!M())throw Error("Audio connection no longer active");return h=F,this.session=F,this.startLease(F),this.reportSessionEvent({timestamp:new Date().toISOString(),action:"media_session",outcome:"replaced",...b,previous_session_id:g,session_id:F.session_generation}),this.client.mediaURL(F,this.audioOptions.mediaTransport)})();u=x,y=b;let R=()=>{if(u===x)u=void 0,y=void 0};return x.then(R,R),x},sessionDiagnostics:()=>({session_id:h.session_generation}),onSessionEvent:(S)=>f(()=>{let s={audio_assets:"Loading audio resources…",microphone_device:"Opening microphone…",audio_context:"Enabling browser playback…",audio_worklet:"Loading audio processor…",audio_transport:"Connecting audio transport…"};if(this.snapshot.busy&&S.outcome==="started"&&s[S.action])this.update({detail:s[S.action]});this.options.onSessionEvent?.(Mh({...S,session_id:S.session_id??h.session_generation}))}),onState:(S,s,b)=>{if(!M())return;if(S==="ended"&&s==="call.ended"){this.observeCall({id:h.call_id,status:"completed"});return}if(S==="live"){if(this.hasLiveAudio=!0,this.automaticAnswerStartedAt)this.reportSessionEvent({timestamp:new Date().toISOString(),action:"automatic_answer",outcome:"audio_connected",duration_ms:Date.now()-this.automaticAnswerStartedAt}),this.automaticAnswerStartedAt=void 0}if(this.update({audioState:S,detail:s}),this.scheduleStatusPoll?.(),M()&&(S==="error"||S==="ended")){if(this.stopAudio(),S==="error"){if(this.reconcileFailedAudio(h,this.generation),b?.recoverable)this.scheduleAudioRecovery()}}},onMediaReadiness:(S)=>f(()=>this.options.onMediaReadiness?.(S)),onLevels:(S,s)=>f(()=>this.options.onLevels?.(S,s)),onAudioHealth:(S)=>f(()=>this.options.onAudioHealth?.(S)),onDiagnostics:(S)=>f(()=>this.options.onDiagnostics?.(S)),onNotice:(S)=>f(()=>this.options.onNotice?.(S)),onCallStatus:(S)=>f(()=>{this.hasStatusPush=!0,this.observeCall({id:S.call_id,status:S.status,direction:S.direction,answered_at:S.answered_at,ended_at:S.ended_at,answered_by:S.answered_by,termination:S.termination,hold_state:S.hold_state,recording_state:S.recording_state,control_error:S.control_error}),this.scheduleStatusPoll?.()})}),this.audio=_,this.assertCurrent(m);for(let S of t)this.reportSessionEvent(S);this.assertCurrent(m),this.startPolling(),this.startLease(h),_.setMuted(this.snapshot.muted);let q;try{await this.cancellable(Promise.race([_.start(this.client.mediaURL(h,this.audioOptions.mediaTransport),this.audioOptions),new Promise((S,s)=>{q=setTimeout(()=>s(Error("Audio setup timed out. Check browser permissions and the network, then try again.")),this.audioOptions.mediaTransport==="auto"?40000:20000)})]),m)}finally{clearTimeout(q)}if(this.assertCurrent(m),!M())throw Error(this.snapshot.detail||"Audio connection ended during setup");_.setMuted(this.snapshot.muted),this.syncRingback()}catch(q){if(M())this.stopAudio();if(this.current(m))this.update({audioState:"error",detail:uh(q)});throw q}}async acquireRecoverySession(h,m,t){let _=this.uncertainAttachment??{diagnostics:m,expected:h.attachment_guard_version===1?h.session_generation??"":void 0};if(t?.aborted)throw new DOMException("Cancelled","AbortError");if(this.uncertainAttachment=_,_.diagnostics.attempt_id!==m.attempt_id)this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"idempotent_retry",...m,shared_attempt_id:_.diagnostics.attempt_id});try{let M=await this.client.attach(h.call_id,_.diagnostics,{signal:t},_.expected);if(t?.aborted)throw new DOMException("Cancelled","AbortError");return this.uncertainAttachment=void 0,M}catch(M){let f=th(M).status;if(f!==void 0&&f>=400&&f<500)this.uncertainAttachment=void 0;throw M}}scheduleAudioRecovery(){if(this.disposed||!this.session||!this.hasLiveAudio||this.automaticRecovery)return;let h=this.session.call_id,m=this.generation;this.update({audioState:"reconnecting",detail:"Network unavailable; waiting to restore audio."});let t=(f)=>{let u=th(f);return u.denied||u.code==="media_session_changed"||u.code==="call_ended"||u.code==="call_not_attachable"||["NotAllowedError","NotFoundError"].includes(f?.name)},_=this.uncertainAttachment?.diagnostics??Oh("automatic_retry"),M=_;this.automaticRecovery=new G0(async(f)=>{this.assertCurrent(m);let u=await Nh((s)=>this.client.getCall(h,s),f);if(!u)throw Object.assign(Error("Call unavailable"),{status:404});if(rh(u.status)){this.observeCall(u);return}this.assertCurrent(m);let y=await this.prepareAudio(m,!1);if(f.aborted)throw new DOMException("Cancelled","AbortError");let q=await this.acquireRecoverySession(this.session,M,f);if(this.assertCurrent(m),f.aborted)throw new DOMException("Cancelled","AbortError");let S=()=>{if(this.current(m)&&this.session===q)this.stopAudio()};f.addEventListener("abort",S,{once:!0});try{if(await this.attachAudio(q,m,y),f.aborted)throw new DOMException("Cancelled","AbortError")}finally{f.removeEventListener("abort",S)}this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:"connected",...M}),this.automaticRecovery?.stop(),this.automaticRecovery=void 0},t,(f)=>{if(this.current(m))this.automaticRecovery=void 0,this.update({audioState:"error",detail:uh(f)})},(f)=>{if(f==="attempt_started")M={..._,attempt_id:Gh("attempt")};this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:f,...M})})}async reconcileAttachedCall(h,m){try{let t=await this.client.getCall(h.call_id);if(this.current(m)&&this.session===h&&t)this.observeCall(t)}catch{}}async reconcileFailedAudio(h,m){try{let t=await this.client.getCall(h.call_id);if(!this.current(m)||this.session!==h||!t)return;if(t.status==="pending")this.invalidate(),this.clearCall("pending"),this.update({busy:!1,detail:"The call was not connected. Answer again to retry."});else this.observeCall(t)}catch{}}startLease(h){if(this.lease?.stop(),!h.lease_seconds)return;this.lease=new Lh(h.lease_seconds,()=>this.client.renew(h),(m,t)=>{if(this.disposed||this.session!==h)return;if(this.stopAudio(),this.update({audioState:"error",detail:m==="expired"?"Audio session expired. Reconnecting audio.":`Audio authorization ended: ${uh(t??m)}`}),m==="expired")this.scheduleAudioRecovery();else this.automaticRecovery?.stop(),this.automaticRecovery=void 0},(m)=>{if(this.audio?.recordSessionEvent)this.audio.recordSessionEvent(m);else try{this.options.onSessionEvent?.(m)}catch{}},h.lease_started_ms)}reportSessionEvent(h){if(h=Mh(h),this.audio?.recordSessionEvent)try{this.audio.recordSessionEvent(h)}catch{}else try{this.options.onSessionEvent?.(h)}catch{}}stopAudio(){this.lease?.stop(),this.lease=void 0;let h=this.audio;if(this.audio=void 0,this.ringing){this.ringing=!1;try{h?.stopRingback?.()}catch{}}try{h?.stop()}catch{}if(h)try{this.options.onLevels?.(0,0)}catch{}}stopPolling(){this.scheduleStatusPoll=void 0,clearTimeout(this.timer),this.timer=void 0,this.polling?.abort(),this.polling=void 0}startPolling(){if(this.stopPolling(),!this.interval)return;let h=new AbortController;this.polling=h;let m=!1,t=performance.now(),_=()=>this.hasStatusPush&&this.snapshot.audioState==="live"?Math.max(1,Math.max(this.interval,30000)-(performance.now()-t)):this.interval,M=async()=>{if(m)return;let f=this.session?.call_id;if(!f||h.signal.aborted)return;m=!0;try{let u=await this.client.getCall(f,h.signal);if(t=performance.now(),!h.signal.aborted&&u)this.observeCall(u)}catch(u){if(!h.signal.aborted)this.hasStatusPush=!1,this.update({detail:`Call status unavailable: ${uh(u)}`})}finally{m=!1,this.scheduleStatusPoll?.()}};this.scheduleStatusPoll=()=>{if(m||h.signal.aborted||!this.session||this.disposed)return;clearTimeout(this.timer),this.timer=setTimeout(M,_())},this.scheduleStatusPoll()}clearCall(h){this.hasStatusPush=!1,this.stopAudio(),this.stopPolling(),this.session=void 0,this.hasLiveAudio=!1,this.scheduleNetworkCheck(),this.outbound=!1,this.update({callId:void 0,carrierStatus:h,audioState:"idle",muted:!1,detail:void 0,phase:h?"ended":"idle",holdState:void 0,recordingState:void 0,controlError:void 0,capabilities:void 0})}dispose(){if(this.cancelNetworkCheck(),globalThis.removeEventListener?.("online",this.networkChanged),this.disposed)return;this.autoAnswer.dispose(),this.disposed=!0,this.invalidate(),this.listeners.clear(),this.clearCall(),this.update({busy:!1})}}function Cm(h,m="en"){if(h?.reason==="ai_inactivity")return m.toLowerCase().startsWith("fr")?"Fin de l’appel : aucune réponse":"Call ended: no response";if(h?.reason==="ai_policy_failure")return m.toLowerCase().startsWith("fr")?"Échec du rappel d’inactivité IA":"AI inactivity reminder failed";if(h?.reason==="time_limit"||h?.reason==="ai_max_duration")return m.toLowerCase().startsWith("fr")?"Durée maximale atteinte":"Maximum call duration reached";return h?.reason?.replaceAll("_"," ")??""}class d0 extends Error{code="offer_expired";status=409;constructor(){super("Call offer expired");this.name="TelephonyOfferExpiredError"}}function eh(h){return h.direction==="inbound"&&h.status==="pending"&&h.answerable!==!1&&!h.routing_waiting&&(h.peer_kind==="human"||Boolean(h.ring_offers?.some((m)=>m.kind==="browser")))}var rh=(h)=>["completed","failed","no-answer","no_answer","busy","canceled","cancelled"].includes(h);function c(h){if(!h||/[\s/\\?#]/.test(h))throw Error("Invalid call ID");return encodeURIComponent(h)}class oh{app;options;transport;networkCheckControllers=new Set;incomingPending=!1;listMicrophones=k0;createMicrophonePreview=(h)=>I0(h,this.app);constructor(h,m={}){this.app=h;this.options=m;if(this.transport=new _0(h,m.authProvider?"proxy":m.direct?"direct":m.runtime,globalThis.fetch,m.direct),h.name!=="telephony"||!h.projectId||!h.installId)throw Error("Telephony requires an explicit project and installation")}get(h,m){return this.transport.request("GET",h,void 0,m)}post(h,m,t){return this.transport.request("POST",h,m,t)}mediaPost(h,m,t){return this.transport.requestMedia(h,m,t)}subscribe(h,m,t){return this.transport.subscribe(h,m,t)}path(h){if(!this.options.authProvider)return h;return`/user${h}${h.includes("?")?"&":"?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`}async listCalls(h){let m=await this.get(this.path("/calls"),{signal:h});if(!Array.isArray(m?.calls)||m.calls.some((t)=>!t||typeof t.id!=="string"||typeof t.status!=="string"))throw Error("Invalid Telephony calls response");if(this.incomingPending=m.calls.some(eh),this.incomingPending)this.cancelNetworkChecks();return m.calls}async getCall(h,m){let t=await this.get(this.path(`/calls?call_id=${c(h)}`),{signal:m});if(!Array.isArray(t?.calls))throw Error("Invalid Telephony call response");return t.calls.find((_)=>_.id===h)}incomingCalls(h){return h.filter(eh)}watchCalls(h,m={}){let t=m.intervalMs??2000;if(!Number.isFinite(t)||t<100)throw Error("Call watch interval must be at least 100 ms");let _=new AbortController,M,f,u=!1,y=!1,q=-1/0,S,s,b=performance.now(),A=()=>{let G=performance.now()-q;return G<15000?Math.max(1,Math.min(Math.max(t,30000)-(performance.now()-b),15000-G)):t},x=new Set,R=()=>{clearTimeout(M),_.abort(),f?.close(),m.signal?.removeEventListener("abort",R)},g=(G)=>{try{m.onError?.(G)}catch{R()}};if(m.signal?.addEventListener("abort",R,{once:!0}),m.signal?.aborted)R();let F=async(G)=>{if(_.signal.aborted)return;if(u){y=!0;return}clearTimeout(M),u=!0;let C=performance.now(),E=S;try{let Q=await this.listCalls(_.signal);if(b=performance.now(),!_.signal.aborted){h(Q),s=E;for(let k of Q){if(k.answerable!==!0||k.status!=="pending")continue;for(let N of k.ring_offers??[]){if(N.kind!=="browser"||!N.id||x.has(N.id))continue;x.add(N.id),this.acknowledgeOffer(k.id,N.id).catch((U)=>{if(![404,405].includes(U?.status??0))x.delete(N.id)})}}try{m.onTiming?.({trigger:G,fetchMs:performance.now()-C})}catch{}}}catch(Q){if(q=-1/0,s=void 0,!_.signal.aborted){let k=Q?.status;try{m.onFailure?.({trigger:G,fetchMs:performance.now()-C,status:typeof k==="number"?k:void 0,error:Q})}catch{}g(Q)}}finally{if(u=!1,!_.signal.aborted)if(y)y=!1,F("push");else M=setTimeout(()=>void F("poll"),A())}};if(F("poll"),!_.signal.aborted&&m.push!==!1&&typeof this.app.subscribe==="function")try{f=this.subscribe(this.path("/calls/events"),(G)=>{if(G.type==="heartbeat"||G.type==="calls.changed"){if(q=performance.now(),G.type==="calls.changed"){let C=typeof G.state==="string"&&/^[0-9a-f]{64}$/.test(G.state)?G.state:void 0;if(S=C,!C||C!==s||u)F("push")}else if(!u)clearTimeout(M),M=setTimeout(()=>void F("poll"),A())}else if(G.type==="access.revoked")q=-1/0,f?.close(),F("push")},{transport:"fetch",signal:_.signal,reconnectDelayMs:250,onError:(G)=>{if(q=-1/0,!u&&!_.signal.aborted)clearTimeout(M),M=setTimeout(()=>void F("poll"),t);let C=G?.status;if(C===404||C===405||C===501)f?.close()}})}catch{}return{close:R}}async place(h){if(this.cancelNetworkChecks(),!/^\+[1-9]\d{7,14}$/.test(h.to)||!h.idempotency_key?.trim())throw Error("Dial requires an E.164 number and an idempotency key");let m=Sh();return this.session(await this.mediaPost(this.path("/softphone/place"),h),void 0,"media",m)}async answer(h,m={}){this.cancelNetworkChecks();let t=Sh();try{return this.session(await this.mediaPost(this.path(`/softphone/answer/${c(h)}`),m),h,"media",t)}catch(_){let M=_;if(M.status===409&&typeof M.body==="string"){let f;try{f=JSON.parse(M.body)}catch{}if(f?.code==="offer_expired")throw new d0}throw _}}async acknowledgeOffer(h,m){await this.post(this.path(`/softphone/offer/ack/${c(h)}`),{offer_id:m})}async declineOffer(h,m){await this.post(this.path(`/softphone/offer/decline/${c(h)}`),{offer_id:m})}async attach(h,m,t,_){let M=Sh();return this.session(await Nh((f)=>this.mediaPost(this.path(`/softphone/attach/${c(h)}`),{...m?{media_diagnostics:m}:{},..._!==void 0&&m?.attempt_id?{attachment_guard:{id:m.attempt_id,expected_generation:_}}:{}},{...t,signal:f}),t?.signal??void 0),h,"media",M)}async takeover(h,m){let t=Sh();return this.session(await this.mediaPost(this.path(`/softphone/takeover/${c(h)}`),m?{media_diagnostics:m}:{}),h,"media",t)}createCallListener(h={}){return new F0(this,{...h,...zh({...this.options.audio,...h.audio},h)})}async listenSession(h,m){let t=Sh();return this.session(await Nh((_)=>this.mediaPost(this.path(`/softphone/listen/${c(h)}`),{},{signal:_}),m,5000,(_)=>{try{this.stopListening(this.session(_,h,"listen-media",t)).catch(()=>{})}catch{}}),h,"listen-media",t)}async coachSession(h){let m=Sh(),t=this.session(await this.mediaPost(this.path(`/softphone/coach/${c(h)}`),{}),h,"listen-media",m);if(t.coaching!==!0)throw Error("Invalid coaching session");return t}async renewListening(h){return this.mediaPost(this.path(`/softphone/${h.coaching?"coach-renew":"listen-renew"}/${c(h.call_id)}`),{session_token:h.session_token})}async stopListening(h){await this.mediaPost(this.path(`/softphone/${h.coaching?"coach-stop":"listen-stop"}/${c(h.call_id)}`),{session_token:h.session_token})}async listenerAudit(h){return this.get(this.path(`/softphone/listen-audit/${c(h)}`))}networkPolicy(h={},m){return this.post(this.path("/softphone/network-policy"),h,{signal:m})}configureNetworkPolicy(h={}){return this.post(this.path("/numbers/network-policy"),h)}registerNetworkCheck(h){return this.networkCheckControllers.add(h),()=>this.networkCheckControllers.delete(h)}cancelNetworkChecks(h){for(let m of this.networkCheckControllers)if(m.signal!==h)m.abort(),this.networkCheckControllers.delete(m)}get networkChecksAllowed(){return!this.incomingPending}async checkNetwork(h={}){if(this.incomingPending)return{state:"cancelled",checkedAt:new Date().toISOString(),paths:[]};this.cancelNetworkChecks(h.signal);let m=new AbortController;this.networkCheckControllers.add(m);let t=()=>m.abort();if(h.signal?.addEventListener("abort",t,{once:!0}),h.signal?.aborted)t();try{return await N0((_,M)=>this.post(this.path("/softphone/network-check"),_,{signal:M}),{...h,signal:m.signal})}finally{h.signal?.removeEventListener("abort",t),this.networkCheckControllers.delete(m)}}autoAnswerPolicy(h={}){return this.post(this.path("/softphone/auto-answer-policy"),h)}autoAnswerSession(h){return Nh((m)=>this.post(this.path("/softphone/auto-answer-session"),h,{signal:m}))}autoAnswerOffer(h){return Nh((m)=>this.post(this.path("/softphone/auto-answer-offer"),h,{signal:m}))}turnPolicy(h={}){return this.post(this.path("/softphone/turn-policy"),h)}configureTURNPolicy(h={}){return this.post(this.path("/numbers/turn-policy"),h)}carrierMediaSettings(h){return this.post(this.path("/numbers/carrier-media"),h)}outboundTransport(h){return this.post(this.path("/numbers/outbound-transport"),h)}outboundNumbers(h={}){return this.get(this.path(h.fresh?"/softphone/numbers?fresh=true":"/softphone/numbers"))}async renew(h){return this.mediaPost(this.path(`/softphone/renew/${c(h.call_id)}`),{session_token:h.session_token})}async release(h){if(!h.session_token)throw Error("Answer session has no release token");await this.mediaPost(this.path(`/softphone/release/${c(h.call_id)}`),{session_token:h.session_token})}async hangup(h){await this.post(this.path(`/calls/${c(h)}/hangup`),{})}hold(h){return this.post(this.path(`/calls/${c(h)}/hold`),{})}resume(h){return this.post(this.path(`/calls/${c(h)}/resume`),{})}pauseRecording(h){return this.post(this.path(`/calls/${c(h)}/pause-recording`),{})}resumeRecording(h){return this.post(this.path(`/calls/${c(h)}/resume-recording`),{})}createSoftphone(h={}){return new O0(this,{...h,audio:{...this.options.audio,...h.audio}})}mediaURL(h,m){return this.resolveMediaURL(h,"media",m)}listenerMediaURL(h,m){return this.resolveMediaURL(h,"listen-media",m)}resolveMediaURL(h,m,t){let _=new URL(this.app.mcpURL(),typeof location>"u"?void 0:location.href),M=new URL(h.media_url,_),f=`/api/apps/telephony/_install/${this.app.installId}/softphone/${m}/${c(h.call_id)}/`;if(M.origin!==_.origin||!["http:","https:"].includes(M.protocol)||M.username||M.password||M.search||M.hash||!M.pathname.startsWith(f)||!/^[A-Za-z0-9_-]+$/.test(M.pathname.slice(f.length)))throw Error("Invalid Telephony media endpoint");if(m==="listen-media"&&M.pathname.slice(f.length)!==h.session_token)throw Error("Listener credential mismatch");if(m==="media"&&$h(h.session_generation))M.searchParams.set("session_generation",h.session_generation);let u=this.transport.mediaURL(M,t);return u.protocol=u.protocol==="https:"?"wss:":"ws:",u.href}session(h,m,t="media",_=Sh()){let M=h;if(!M||typeof M.call_id!=="string"||typeof M.media_url!=="string"||m&&M.call_id!==m||M.session_token!==void 0&&typeof M.session_token!=="string")throw Error("Invalid Telephony session response");if(M.lease_seconds!==void 0&&(!Number.isFinite(M.lease_seconds)||M.lease_seconds<10||M.lease_seconds>3600||!M.session_token))throw Error("Invalid media lease");if(t==="listen-media"&&(!M.session_token||M.lease_seconds===void 0))throw Error("Invalid listener lease");return c(M.call_id),this.resolveMediaURL(M,t),{...M,lease_started_ms:_}}}var Ot=t0({app:"telephony",create:({app:h})=>new oh(h)});function Ut({app:h},m){return new oh(h,m)}export{Ut as createClient,Cm as callTerminationLabel};
