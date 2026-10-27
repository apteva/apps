import {browserTabID,diagnosticID,requestDiagnostic,type CallWatchDiagnostics,type RequestDiagnostic,type OfferBrowserEvent} from "./telemetry";
import {runNetworkCheck} from "./network-check";
import { boundedMediaRequest } from "./media-lease";
import {RuntimeTelephonyTransport, type TelephonyRuntimeMode, type DirectTelephonyConnection} from "./runtime-transport";
import { type MediaAttachmentDiagnostics, safeMediaDiagnosticID, leaseClock } from "./media-lease";
import { defineAppExtension, type AppHandle } from "@apteva/web-sdk";
import { createMicrophonePreview, listMicrophones } from "./audio";
import { listenerAudioOptions } from "./listener-audio";
import type { SoftphoneAudioOptions } from "../../ui/softphone-audio";
import { HeadlessCallListener, type CallListenerOptions } from "./listener";
import { HeadlessSoftphone, type SoftphoneOptions } from "./softphone";

/** Translate stable termination reasons without presenting duration expiry as a fault. */
export function callTerminationLabel(termination: CallTermination | undefined, locale = "en"): string {
  if (termination?.reason === "ai_inactivity") return locale.toLowerCase().startsWith("fr") ? "Fin de l’appel : aucune réponse" : "Call ended: no response";
  if (termination?.reason === "ai_policy_failure") return locale.toLowerCase().startsWith("fr") ? "Échec du rappel d’inactivité IA" : "AI inactivity reminder failed";
  if (termination?.reason === "time_limit" || termination?.reason === "ai_max_duration") return locale.toLowerCase().startsWith("fr") ? "Durée maximale atteinte" : "Maximum call duration reached";
  return termination?.reason?.replaceAll("_", " ") ?? "";
}

export interface CallTermination { reason?: string; cause?: string; code?: string; initiator?: string }
export interface Call {
  id: string;
  status: string;
  answered_at?: string;
  ended_at?: string;
  max_duration_sec?: number;
  duration_started_at?: string;
  connected_deadline_at?: string;
  /** human, machine, fax, silence, or unknown once answering machine detection reports. */
  answered_by?: string;
  termination?: CallTermination;
  direction: string;
  peer_kind: string;
  /** Advisory at list time; Answer checks the current offer again. */
  answerable?: boolean;
  listen_supported?: boolean;
  listenable?: boolean;
  coach_supported?: boolean;
  coachable?: boolean;
  coach_unavailable_reason?: string;
  listen_unavailable_reason?: string;
  from_number: string;
  to_number: string;
  routing_waiting?: boolean;
  ring_offers?: Array<{ id: string; destination_id: string; kind: string }>;
  call_classification?: string;
  callback_opportunity_id?: string;
  routing_resolution?: string;
  hold_state?: "active" | "starting" | "held" | "stopping" | "unknown" | "ended";
  recording_state?: "off" | "active" | "pause_requested" | "paused" | "resume_requested" | "unknown" | "ended";
  control_error?: string;
  capabilities?: { hold_music: boolean; recording_pause: boolean };
}
export interface CallControlResult {
  call_id: string;
  hold_state: NonNullable<Call["hold_state"]>;
  recording_state: NonNullable<Call["recording_state"]>;
  control_error: string;
  capabilities: NonNullable<Call["capabilities"]>;
}
export interface CallSession {
  attachment_guard_version?: number;
  call_id: string;
  media_url: string;
  session_generation?: string;
  session_token?: string;
  lease_seconds?: number;
  /** Local monotonic request start; never supplied by the server. */
  lease_started_ms?: number;
  coaching?: boolean;
}
export interface CarrierJitterBufferPolicy { enabled:boolean; min_ms?:number; max_ms?:number }
export interface CarrierMediaSettingsRequest {
 route_id?:string; carrier_connection_id?:number; phone_number?:string;
 carrier_jitter_buffer?:CarrierJitterBufferPolicy; apply?:boolean; fresh?:boolean;
}
export interface CarrierMediaSettingsResult {
 ok:boolean; carrier_jitter_buffer:{requested:CarrierJitterBufferPolicy;effective?:CarrierJitterBufferPolicy;state:string;verified_at?:string;scope:string;resource_type:string;resource_id:string;shared_connection:boolean;message?:string};
}
export interface OutboundSIPConfig {
 host:string; resource_id:string; resource_type?:"credential_connection"|"ip_connection"|"fqdn_connection"|"elastic_sip_trunk";
 auth_mode:"digest"|"ip"; media_encryption:"srtp_sdes"|"disabled"; codecs?:Array<"G722"|"PCMU"|"PCMA">;
}
export interface OutboundTransport {
 transport:"programmable_websocket"|"sip_direct";scope:"default"|"account"|"number";
 config?:OutboundSIPConfig;credentials_configured:boolean;verified_at?:string;
}
export interface OutboundSIPSetupOptions {
 automatic_supported:boolean;
 profiles:Array<{id:string;name:string;enabled:boolean;destinations:string[];daily_spend_limit_enabled:boolean;daily_spend_limit?:string}>;
 warning?:string;
}
export interface OutboundTransportResult {ok:boolean;outbound_transport?:OutboundTransport;setup_options?:OutboundSIPSetupOptions;applies_to?:"new_calls"}
export interface OutboundTransportRequest {
 carrier_connection_id:number;phone_number?:string;transport?:OutboundTransport["transport"];inherit?:boolean;
 config?:OutboundSIPConfig;username?:string;password?:string;
 /** Provision a dedicated outbound resource with managed, write-only credentials. */
 provision?:boolean;setup_options?:boolean;outbound_profile_id?:string;
 /** Explicit policy when creating a Telnyx outbound profile; USD daily limit. */
 profile_policy?:{destinations:string[];daily_spend_limit:string};
}
export interface OutboundNumberChoice {
 outbound_transport?:OutboundTransport;
 verified_at?: string;
 phone_number: string;
 provider: string;
 carrier_connection_id: number;
 outbound_enabled: boolean;
 outbound: { required: boolean; status: string; message?: string };
}
export interface OutboundNumbersResult {
 numbers: OutboundNumberChoice[];
 inventory_status: "available" | "partial" | "unavailable";
 warnings: Array<{ provider: string; carrier_connection_id: number; code: string; message: string }>;
}
export interface DialRequest {
  to: string;
  from?: string;
  timeout_sec?: number;
  recording?: boolean;
  /** Retain the same key and request when retrying an uncertain response. */
  idempotency_key: string;
}
export interface AnswerRequest { destination_id?: string; rejoin?: boolean; auto_answer_session?:string;auto_answer_warning_completed?:boolean }
export class TelephonyOfferExpiredError extends Error {
  readonly code = "offer_expired";
  readonly status = 409;
  constructor() { super("Call offer expired"); this.name = "TelephonyOfferExpiredError"; }
}
export function isIncomingBrowserCall(call: Pick<Call, "direction" | "status" | "peer_kind" | "answerable" | "routing_waiting" | "ring_offers">): boolean {
  return call.direction === "inbound" && call.status === "pending" && call.answerable !== false && !call.routing_waiting &&
    (call.peer_kind === "human" || Boolean(call.ring_offers?.some(offer => offer.kind === "browser")));
}
export interface WatchCallFailure extends RequestDiagnostic {
  trigger:"poll"|"push";fetchMs:number;operation:"call_list"|"callback";callback?:"onCalls";error:unknown;
}
export interface WatchCallsOptions {
  intervalMs?:number;
  push?:boolean;
  /** Coalesced, best-effort health snapshots while an incoming browser offer is observed. */
  persistDiagnostics?:boolean;
  /** Bounds a list read, independently of the SDK's general request timeout. */
  requestTimeoutMs?:number;
  onTiming?:(sample:{trigger:"poll"|"push";fetchMs:number})=>void;
  /** Callback failures are separate from request/response failures. */
  onFailure?:(sample:WatchCallFailure)=>void;
  onCallbackError?:(sample:WatchCallFailure)=>void;
  onDiagnostics?:(snapshot:Readonly<CallWatchDiagnostics>)=>void;
  signal?:AbortSignal;
  onError?:(error:unknown)=>void;
}
/** Host rendering failures retain their original exception in cause, never an HTTP status. */
export class TelephonyCallbackError extends Error {
  readonly category="callback";readonly code="on_calls_exception";readonly callback="onCalls";
  constructor(cause:unknown){super("Telephony host onCalls callback failed",{cause});this.name="TelephonyCallbackError";}
}
export class TelephonyInvalidResponseError extends Error {
  readonly category="invalid_response";
  readonly code="invalid_call_list";
  constructor(readonly requestId?:string){super("Invalid Telephony calls response");this.name="TelephonyInvalidResponseError";}
}
export const isTerminalCall = (status: string) =>
  ["completed", "failed", "no-answer", "no_answer", "busy", "canceled", "cancelled"].includes(status);

function callID(id: string): string {
  if (!id || /[\s/\\?#]/.test(id)) throw new Error("Invalid call ID");
  return encodeURIComponent(id);
}

export interface TelephonyClientOptions {
 /** Managed deployments use direct authenticated browser ingress when advertised. */
 runtime?: TelephonyRuntimeMode;
 direct?: DirectTelephonyConnection;
  /** Shared defaults for new softphones, listeners and coaching sessions. */
  audio?: Partial<SoftphoneAudioOptions>;
  /** Online provider configured by the installation administrator. */
  authProvider?: string;
}

/** Human-call API. Does not invoke the AI-call MCP tools or open a microphone. */
export class TelephonyClient {
  readonly browserTabId=browserTabID();
  readonly browserSessionId=diagnosticID();
  private latestWatch?:CallWatchDiagnostics;
 private transport: RuntimeTelephonyTransport;
 private networkCheckControllers = new Set<AbortController>();
 private incomingPending=false;
  listMicrophones = listMicrophones;
  createMicrophonePreview = (onLevel?: (level: number) => void) => createMicrophonePreview(onLevel, this.app);
  constructor(readonly app: AppHandle, private readonly options: TelephonyClientOptions = {}) {
    this.transport=new RuntimeTelephonyTransport(app,options.authProvider ? "proxy" : options.direct ? "direct" : options.runtime,globalThis.fetch,options.direct);
    if (app.name !== "telephony" || !app.projectId || !app.installId) {
      throw new Error("Telephony requires an explicit project and installation");
    }
  }

  private get<R>(path:string,init?:RequestInit):Promise<R>{return this.transport.request<R>("GET",path,undefined,init);}
  private post<R=any>(path:string,body?:unknown,init?:RequestInit):Promise<R>{return this.transport.request<R>("POST",path,body,init);}
  private mediaPost<R=any>(path:string,body?:unknown,init?:RequestInit):Promise<R>{return this.transport.requestMedia<R>(path,body,init);}
  private subscribe<E>(path:string,onEvent:(event:E)=>void,opts?:Parameters<AppHandle["subscribe"]>[2]){return this.transport.subscribe(path,onEvent,opts);}

  private path(path: string): string {
    if (!this.options.authProvider) return path;
    return `/user${path}${path.includes("?") ? "&" : "?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`;
  }

  async listCalls(signal?: AbortSignal,requestID=`tel-${diagnosticID()}`): Promise<Call[]> {
    const result = await this.get<{ calls: Call[] }>(this.path("/calls"), { signal,headers:{"X-Request-ID":requestID} });
    if (!Array.isArray(result?.calls) || result.calls.some(c => !c || typeof c.id !== "string" || typeof c.status !== "string" || (c.ring_offers != null && (!Array.isArray(c.ring_offers) || c.ring_offers.some(o=>!o || typeof o.id!=="string" || typeof o.kind!=="string" || typeof o.destination_id!=="string"))))) {
      throw new TelephonyInvalidResponseError(requestID);
    }
    signal?.throwIfAborted();
    this.incomingPending=result.calls.some(isIncomingBrowserCall);
 if(this.incomingPending)this.cancelNetworkChecks();
    return result.calls;
  }

  async getCall(id: string, signal?: AbortSignal): Promise<Call | undefined> {
    const result = await this.get<{ calls: Call[] }>(this.path(`/calls?call_id=${callID(id)}`), { signal });
    if (!Array.isArray(result?.calls)) throw new Error("Invalid Telephony call response");
    return result.calls.find(call => call.id === id);
  }

  incomingCalls(calls: readonly Call[]): Call[] {
    return calls.filter(isIncomingBrowserCall);
  }

  /** Push is a hint to refetch through the same authorized handle. Coalesce
   * bursts, never overlap requests, and retain periodic/reconnect recovery. */
  watchCalls(onCalls:(calls:Call[])=>unknown,options:WatchCallsOptions={}):{close():void;getDiagnostics():Readonly<CallWatchDiagnostics>} {
    const interval=options.intervalMs??2000,timeout=options.requestTimeoutMs??10000;
    if(!Number.isFinite(interval)||interval<100)throw Error("Call watch interval must be at least 100 ms");
    if(!Number.isFinite(timeout)||timeout<100||timeout>60000)throw Error("Call request timeout must be 100–60000 ms");
    const controller=new AbortController();let timer:ReturnType<typeof setTimeout>|undefined,stream:{close():void}|undefined;
    let running=false,queued=false,lastPush=-Infinity,hintedState:string|undefined,appliedState:string|undefined,lastRefresh=performance.now();
    let diagnostic:CallWatchDiagnostics={browser_tab_id:this.browserTabId,browser_session_id:this.browserSessionId,watch_id:diagnosticID(),state:options.push===false?"disabled":"connecting",connections:0,disconnects:0,reconnects:0,consecutive_list_failures:0};
    const snapshot=()=>Object.freeze({...diagnostic,last_failure:diagnostic.last_failure?{...diagnostic.last_failure}:undefined});
    const update=(patch:Partial<CallWatchDiagnostics>)=>{diagnostic={...diagnostic,...patch};this.latestWatch=snapshot();try{options.onDiagnostics?.(snapshot())}catch{/* diagnostics cannot break delivery */}persistHealth(Boolean(patch.last_failure)||patch.state==="reconnecting"||patch.state==="closed");};
    let evidenceUntil=0,lastHealthReport=-Infinity,healthReporting=false;
    const persistHealth=(urgent=false)=>{
      if(options.persistDiagnostics===false||Date.now()>evidenceUntil||healthReporting||performance.now()-lastHealthReport<(urgent?1000:10000))return;
      healthReporting=true;lastHealthReport=performance.now();
      void boundedMediaRequest(signal=>this.post(this.path("/softphone/telemetry"),{browser_tab_id:this.browserTabId,browser_session_id:this.browserSessionId,events:[],watch:snapshot()},{signal})).catch(()=>{}).finally(()=>{healthReporting=false;});
    };
    const acknowledged=new Set<string>();
    const nextPollDelay=()=>{const age=performance.now()-lastPush;return age<15000?Math.max(1,Math.min(Math.max(interval,30000)-(performance.now()-lastRefresh),15000-age)):interval;};
    const close=()=>{if(controller.signal.aborted)return;clearTimeout(timer);controller.abort();stream?.close();options.signal?.removeEventListener("abort",close);update({state:"closed"});};
    const report=(error:unknown)=>{try{options.onError?.(error)}catch{/* host error observers cannot stop call delivery */}};
    const callbackFailure=(cause:unknown,trigger:"poll"|"push",fetchMs:number,requestId:string)=>{
      if(controller.signal.aborted)return;
      const error=new TelephonyCallbackError(cause);
      const failure:WatchCallFailure={trigger,fetchMs,operation:"callback",callback:"onCalls",category:"callback",code:"on_calls_exception",requestId,clientRequestId:requestId,error};
      update({last_failure:{category:"callback",code:"on_calls_exception",requestId}});
      try{options.onFailure?.(failure)}catch{}try{options.onCallbackError?.(failure)}catch{}report(error);
    };
    options.signal?.addEventListener("abort",close,{once:true});if(options.signal?.aborted)close();
    const refresh=async(trigger:"poll"|"push")=>{
      if(controller.signal.aborted)return;if(running){queued=true;return;}clearTimeout(timer);running=true;
      const started=performance.now(),stateAtStart=hintedState,requestID=`tel-${diagnosticID()}`,read=new AbortController();
      const cancel=()=>read.abort(controller.signal.reason);controller.signal.addEventListener("abort",cancel,{once:true});
      const deadline=setTimeout(()=>read.abort(new DOMException("Call-list read timed out","TimeoutError")),timeout);
      let calls:Call[]|undefined;
      // Only the HTTP operation and response validation belong to this catch.
      let rejectRead:(()=>void)|undefined;
      try{calls=await Promise.race([this.listCalls(read.signal,requestID),new Promise<never>((_,reject)=>{rejectRead=()=>reject(read.signal.reason);read.signal.addEventListener("abort",rejectRead,{once:true});if(read.signal.aborted)rejectRead();})]);}
      catch(error){
        lastPush=-Infinity;appliedState=undefined;
        if(!controller.signal.aborted){const failure=requestDiagnostic(error,read.signal);failure.clientRequestId??=requestID;failure.requestId??=requestID;update({last_failure:failure,consecutive_list_failures:diagnostic.consecutive_list_failures+1,last_request_id:failure.requestId??requestID});
          try{options.onFailure?.({...failure,trigger,fetchMs:performance.now()-started,operation:"call_list",error})}catch{}report(error);}
      }finally{clearTimeout(deadline);controller.signal.removeEventListener("abort",cancel);if(rejectRead)read.signal.removeEventListener("abort",rejectRead);}
      if(calls&&!controller.signal.aborted&&!read.signal.aborted){
        if(calls.some(c=>isIncomingBrowserCall(c)&&Boolean(c.ring_offers?.length)))evidenceUntil=Date.now()+30000;
        const fetchMs=performance.now()-started;lastRefresh=performance.now();appliedState=stateAtStart;
        update({last_successful_call_list_at:new Date().toISOString(),last_request_id:requestID,consecutive_list_failures:0});
        // Receipt is independent of whether the host callback renders successfully.
        for(const call of calls){if(call.answerable!==true||call.status!=="pending")continue;for(const offer of call.ring_offers??[]){
          if(offer.kind!=="browser"||!offer.id||acknowledged.has(offer.id))continue;acknowledged.add(offer.id);
          void this.acknowledgeOffer(call.id,offer.id).catch(error=>{if(![404,405].includes((error as {status?:number})?.status??0))acknowledged.delete(offer.id);});
        }}
        try{const result=onCalls(calls);if(result&&typeof (result as PromiseLike<unknown>).then==="function")void Promise.resolve(result).catch(error=>callbackFailure(error,trigger,fetchMs,requestID));}
        catch(error){callbackFailure(error,trigger,fetchMs,requestID);}
        try{options.onTiming?.({trigger,fetchMs})}catch{/* observers are isolated */}
      }
      running=false;
      if(!controller.signal.aborted){if(queued){queued=false;void refresh("push")}else timer=setTimeout(()=>void refresh("poll"),nextPollDelay());}
    };
    update({});void refresh("poll");
    if(!controller.signal.aborted&&options.push!==false&&typeof this.app.subscribe==="function"){
      try{
        const q=new URLSearchParams({browser_tab_id:this.browserTabId,browser_session_id:this.browserSessionId,request_id:`tel-${diagnosticID()}`});
        stream=this.subscribe<{type?:string;state?:string;request_id?:string;reason?:string}>(this.path(`/calls/events?${q}`),event=>{
          if(controller.signal.aborted)return;
          if(event.type==="stream.open"){update({last_request_id:event.request_id});return;}
          if(event.type==="stream.closed"){update({state:"reconnecting",last_disconnect_at:new Date().toISOString()});return;}
          if(event.type==="heartbeat"||event.type==="calls.changed"){
            lastPush=performance.now();update({state:"connected",last_stream_event_at:new Date().toISOString(),...(event.type==="heartbeat"?{last_heartbeat_at:new Date().toISOString()}:{})});
            if(event.type==="calls.changed"){const state=typeof event.state==="string"&&/^[0-9a-f]{64}$/.test(event.state)?event.state:undefined;hintedState=state;if(!state||state!==appliedState||running)void refresh("push");}
            else if(!running){clearTimeout(timer);timer=setTimeout(()=>void refresh("poll"),nextPollDelay());}
          }else if(event.type==="access.revoked"){lastPush=-Infinity;stream?.close();update({state:"closed"});void refresh("push");}
        },{transport:"fetch",signal:controller.signal,reconnectDelayMs:250,
          onOpen:()=>{if(controller.signal.aborted)return;update({state:"connected",connections:diagnostic.connections+1,reconnects:Math.max(0,diagnostic.connections),last_connect_at:new Date().toISOString()});},
          onError:error=>{
            if(controller.signal.aborted)return;lastPush=-Infinity;
            const failure=requestDiagnostic(error);failure.requestId??=q.get("request_id")??undefined;update({state:"reconnecting",disconnects:diagnostic.disconnects+1,last_disconnect_at:new Date().toISOString(),last_reconnect_attempt_at:new Date().toISOString(),last_failure:failure});
            if(!running){clearTimeout(timer);timer=setTimeout(()=>void refresh("poll"),interval);}
            if([404,405,501].includes((error as {status?:number})?.status??0)){stream?.close();update({state:"disabled"});}
          },
        });
      }catch(error){update({state:"disabled",last_failure:requestDiagnostic(error)});}
    }
    return {close,getDiagnostics:snapshot};
  }

  async place(request: DialRequest): Promise<CallSession> {
    this.cancelNetworkChecks();
    if (!/^\+[1-9]\d{7,14}$/.test(request.to) || !request.idempotency_key?.trim()) {
      throw new Error("Dial requires an E.164 number and an idempotency key");
    }
    const started = leaseClock();
    return this.session(await this.mediaPost(this.path("/softphone/place"), request), undefined, "media", started);
  }

  async answer(id: string, request: AnswerRequest = {}): Promise<CallSession> {
    this.cancelNetworkChecks();
    const started = leaseClock();
    try {
      return this.session(await this.mediaPost(this.path(`/softphone/answer/${callID(id)}`), request), id, "media", started);
    } catch (error) {
      const response = error as { status?: number; body?: string };
      if (response.status === 409 && typeof response.body === "string") {
        let payload: { code?: string } | undefined;
        try { payload = JSON.parse(response.body); } catch { /* Other 409 responses retain their original error. */ }
        if (payload?.code === "offer_expired") throw new TelephonyOfferExpiredError();
      }
      throw error;
    }
  }

  async acknowledgeOffer(callIDValue: string, offerID: string): Promise<void> {
    await this.post(this.path(`/softphone/offer/ack/${callID(callIDValue)}`), { offer_id: offerID,browser_tab_id:this.browserTabId,browser_session_id:this.browserSessionId,...(this.latestWatch?{watch:this.latestWatch}:{}) });
  }

  /** Host UI/audio evidence; receipt ACKs never imply these stages. */
  reportOfferEvent(event:OfferBrowserEvent):Promise<{accepted:boolean}>{
    const record={...event,event_id:event.event_id??diagnosticID(),occurred_at:event.occurred_at??new Date().toISOString()};
    return boundedMediaRequest(signal=>this.post(this.path("/softphone/telemetry"),{browser_tab_id:this.browserTabId,browser_session_id:this.browserSessionId,events:[record],...(this.latestWatch?{watch:this.latestWatch}:{})},{signal}));
  }
  offerDiagnostics(id:string):Promise<Record<string,unknown>>{return this.get(this.path(`/softphone/offer-telemetry/${callID(id)}`));}
  getWatchDiagnostics():Readonly<CallWatchDiagnostics>|undefined{return this.latestWatch?Object.freeze({...this.latestWatch,last_failure:this.latestWatch.last_failure?{...this.latestWatch.last_failure}:undefined}):undefined;}

  async declineOffer(callIDValue: string, offerID: string): Promise<void> {
    await this.post(this.path(`/softphone/offer/decline/${callID(callIDValue)}`), { offer_id: offerID });
  }

  /** Attach an assigned human call; never dials or takes another user's call. */
  async attach(id: string, diagnostics?: MediaAttachmentDiagnostics, init?:RequestInit, expectedGeneration?:string): Promise<CallSession> {
    const started = leaseClock();
    return this.session(await boundedMediaRequest(signal=>this.mediaPost(this.path(`/softphone/attach/${callID(id)}`), {...(diagnostics ? {media_diagnostics:diagnostics} : {}),...(expectedGeneration!==undefined && diagnostics?.attempt_id ? {attachment_guard:{id:diagnostics.attempt_id,expected_generation:expectedGeneration}} : {})},{...init,signal}),init?.signal??undefined), id, "media", started);
  }

  async takeover(id: string, diagnostics?: MediaAttachmentDiagnostics): Promise<CallSession> {
    const started = leaseClock();
    return this.session(await this.mediaPost(this.path(`/softphone/takeover/${callID(id)}`), diagnostics ? {media_diagnostics:diagnostics} : {}), id, "media", started);
  }

  createCallListener(options: CallListenerOptions = {}): HeadlessCallListener { return new HeadlessCallListener(this, {...options,...listenerAudioOptions({...this.options.audio,...options.audio},options)}); }

  async listenSession(id: string, signal?:AbortSignal): Promise<CallSession> { const started = leaseClock(); return this.session(await boundedMediaRequest(s=>this.mediaPost(this.path(`/softphone/listen/${callID(id)}`), {},{signal:s}),signal,5000,value=>{try{void this.stopListening(this.session(value,id,"listen-media",started)).catch(()=>{});}catch{/* invalid late credentials cannot be used */}}), id, "listen-media", started); }
  async coachSession(id: string): Promise<CallSession> { const started = leaseClock(); const s=this.session(await this.mediaPost(this.path(`/softphone/coach/${callID(id)}`), {}),id,"listen-media",started); if(s.coaching!==true) throw new Error("Invalid coaching session");return s; }
  async renewListening(session: CallSession): Promise<{lease_seconds?: number}> { return this.mediaPost(this.path(`/softphone/${session.coaching ? "coach-renew" : "listen-renew"}/${callID(session.call_id)}`), { session_token: session.session_token }); }
  async stopListening(session: CallSession): Promise<void> { await this.mediaPost(this.path(`/softphone/${session.coaching ? "coach-stop" : "listen-stop"}/${callID(session.call_id)}`), { session_token: session.session_token }); }
  async listenerAudit(id: string): Promise<{ listeners: Array<{ id: string; principal: unknown; joined_at: string; left_at: string; reason: string; diagnostics: unknown; mode: "listen"|"coach" }>; coaching: Array<{id:string;listener_audit_id:string;principal:unknown;started_at:string;ended_at:string;reason:string}> }> { return this.get(this.path(`/softphone/listen-audit/${callID(id)}`)); }

  /** Operator-only configuration. Never available through adviser app-user grants.
   * Transport changes affect new calls; credentials are write-only. */
  /** Operator-only optional carrier buffer. Off by default; shared resource scope.
   * apply defers while calls are active. Does not change local SIP recovery. */
  /** The backend resolves project and adviser identity from authentication. */
  networkPolicy(request:{policy?:Partial<import("./network-policy").NetworkPolicy>;inherit?:boolean}={},signal?:AbortSignal):Promise<import("./network-policy").NetworkPolicyView>{return this.post(this.path("/softphone/network-policy"),request,{signal});}
  configureNetworkPolicy(request:{identity?:import("./turn-policy").TURNIdentity;policy?:Partial<import("./network-policy").NetworkPolicy>;inherit?:boolean}={}):Promise<import("./network-policy").NetworkPolicyView>{return this.post(this.path("/numbers/network-policy"),request);}
  registerNetworkCheck(controller:AbortController):()=>void {this.networkCheckControllers.add(controller);return()=>this.networkCheckControllers.delete(controller);}
  cancelNetworkChecks(preserve?:AbortSignal):void {for(const controller of this.networkCheckControllers)if(controller.signal!==preserve){controller.abort();this.networkCheckControllers.delete(controller);}}
  get networkChecksAllowed():boolean{return !this.incomingPending;}
  async checkNetwork(options:import("./network-check").NetworkCheckOptions={}):Promise<import("./network-check").NetworkCheckResult>{
    if(this.incomingPending)return {state:"cancelled",checkedAt:new Date().toISOString(),paths:[]};
    this.cancelNetworkChecks(options.signal);const controller=new AbortController();this.networkCheckControllers.add(controller);
    const cancel=()=>controller.abort();options.signal?.addEventListener("abort",cancel,{once:true});if(options.signal?.aborted)cancel();
    try{return await runNetworkCheck((body,signal)=>this.post(this.path("/softphone/network-check"),body,{signal}),{...options,signal:controller.signal});}
    finally{options.signal?.removeEventListener("abort",cancel);this.networkCheckControllers.delete(controller);}
  }
  autoAnswerPolicy(request:import("./auto-answer").AutoAnswerPolicyRequest={}):Promise<import("./auto-answer").AutoAnswerPolicyView>{return this.post(this.path("/softphone/auto-answer-policy"),request);}
  autoAnswerSession(request:{session_id:string;available:boolean;takeover?:boolean;warning_tone_supported?:boolean}):Promise<{designated:boolean;lease_ms?:number}>{return boundedMediaRequest(signal=>this.post(this.path("/softphone/auto-answer-session"),request,{signal}));}
  autoAnswerOffer(request:{session_id:string;call_id:string;destination_id?:string;cancel?:boolean}):Promise<{eligible:boolean;destination_id?:string;offer_id?:string;offer_ttl_ms?:number;effective:import("./auto-answer").AutoAnswerEffective}>{return boundedMediaRequest(signal=>this.post(this.path("/softphone/auto-answer-offer"),request,{signal}));}
  turnPolicy(request:{policy?:import("./turn-policy").TURNPolicy;inherit?:boolean} = {}):Promise<import("./turn-policy").TURNPolicyView>{return this.post(this.path("/softphone/turn-policy"),request);}
  /** Operator-only project/adviser relay configuration. */
  configureTURNPolicy(request:{identity?:import("./turn-policy").TURNIdentity;policy?:import("./turn-policy").TURNPolicy;inherit?:boolean} = {}):Promise<import("./turn-policy").TURNPolicyView>{return this.post(this.path("/numbers/turn-policy"),request);}
  carrierMediaSettings(request:CarrierMediaSettingsRequest):Promise<CarrierMediaSettingsResult> {
    return this.post(this.path("/numbers/carrier-media"),request);
  }
  outboundTransport(request:OutboundTransportRequest):Promise<OutboundTransportResult> {
    return this.post(this.path("/numbers/outbound-transport"),request);
  }
  /** Authorized choices for new calls; disabling never changes existing media grants. */
  outboundNumbers(options: { fresh?: boolean } = {}): Promise<OutboundNumbersResult> { return this.get(this.path(options.fresh ? "/softphone/numbers?fresh=true" : "/softphone/numbers")); }

  async renew(session: CallSession): Promise<{lease_seconds?: number}> {
    return this.mediaPost(this.path(`/softphone/renew/${callID(session.call_id)}`), { session_token: session.session_token });
  }

  async release(session: CallSession): Promise<void> {
    if (!session.session_token) throw new Error("Answer session has no release token");
    await this.mediaPost(this.path(`/softphone/release/${callID(session.call_id)}`), { session_token: session.session_token });
  }

  async hangup(id: string): Promise<void> {
    await this.post(this.path(`/calls/${callID(id)}/hangup`), {});
  }

  /** Keep the same carrier call connected. Requires configured hold music. */
  hold(id: string): Promise<CallControlResult> {
    return this.post(this.path(`/calls/${callID(id)}/hold`), {});
  }

  resume(id: string): Promise<CallControlResult> {
    return this.post(this.path(`/calls/${callID(id)}/resume`), {});
  }

  /** Resolves only after Telephony receives Telnyx's successful pause result. */
  pauseRecording(id: string): Promise<CallControlResult> {
    return this.post(this.path(`/calls/${callID(id)}/pause-recording`), {});
  }

  resumeRecording(id: string): Promise<CallControlResult> {
    return this.post(this.path(`/calls/${callID(id)}/resume-recording`), {});
  }

  createSoftphone(options: SoftphoneOptions = {}): HeadlessSoftphone {
    return new HeadlessSoftphone(this, {...options,audio:{...this.options.audio,...options.audio}});
  }

  /** Resolve only the selected installation's media path on the SDK gateway. */
  mediaURL(session: CallSession, transport?: string): string { return this.resolveMediaURL(session, "media", transport); }
  listenerMediaURL(session: CallSession, transport?: string): string { return this.resolveMediaURL(session, "listen-media", transport); }
  private resolveMediaURL(session: CallSession, kind: "media" | "listen-media", transport?: string): string {
    const gateway = new URL(this.app.mcpURL(), typeof location === "undefined" ? undefined : location.href);
    const url = new URL(session.media_url, gateway);
    const prefix = `/api/apps/telephony/_install/${this.app.installId}/softphone/${kind}/${callID(session.call_id)}/`;
    if (url.origin !== gateway.origin || !["http:", "https:"].includes(url.protocol) ||
        url.username || url.password || url.search || url.hash || !url.pathname.startsWith(prefix) ||
        !/^[A-Za-z0-9_-]+$/.test(url.pathname.slice(prefix.length))) {
      throw new Error("Invalid Telephony media endpoint");
    }
    if (kind === "listen-media" && url.pathname.slice(prefix.length) !== session.session_token) throw new Error("Listener credential mismatch");
    if(kind==="media" && safeMediaDiagnosticID(session.session_generation)) url.searchParams.set("session_generation",session.session_generation!);
    const media=this.transport.mediaURL(url, transport);
    media.protocol = media.protocol === "https:" ? "wss:" : "ws:";
    return media.href;
  }

  private session(value: unknown, expectedID?: string, kind: "media" | "listen-media" = "media", startedMS = leaseClock()): CallSession {
    const session = value as CallSession;
    if (!session || typeof session.call_id !== "string" || typeof session.media_url !== "string" ||
        (expectedID && session.call_id !== expectedID) ||
        (session.session_token !== undefined && typeof session.session_token !== "string")) {
      throw new Error("Invalid Telephony session response");
    }
    if (session.lease_seconds !== undefined && (!Number.isFinite(session.lease_seconds) || session.lease_seconds < 10 || session.lease_seconds > 3600 || !session.session_token)) throw new Error("Invalid media lease");
    if (kind === "listen-media" && (!session.session_token || session.lease_seconds === undefined)) throw new Error("Invalid listener lease");
    callID(session.call_id);
    this.resolveMediaURL(session, kind);
    return { ...session, lease_started_ms: startedMS };
  }
}

export const telephonyExtension = defineAppExtension({
  app: "telephony",
  create: ({ app }) => new TelephonyClient(app),
});
