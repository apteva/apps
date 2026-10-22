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
export interface AnswerRequest { destination_id?: string; rejoin?: boolean }
export class TelephonyOfferExpiredError extends Error {
  readonly code = "offer_expired";
  readonly status = 409;
  constructor() { super("Call offer expired"); this.name = "TelephonyOfferExpiredError"; }
}
export function isIncomingBrowserCall(call: Pick<Call, "direction" | "status" | "peer_kind" | "answerable" | "routing_waiting" | "ring_offers">): boolean {
  return call.direction === "inbound" && call.status === "pending" && call.answerable !== false && !call.routing_waiting &&
    (call.peer_kind === "human" || Boolean(call.ring_offers?.some(offer => offer.kind === "browser")));
}
export interface WatchCallsOptions {
  /** Recovery polling interval; 500 ms is supported as an interim tuning option. */
  intervalMs?: number;
  /** Live, permission-filtered hints with polling recovery. Default true. */
  push?: boolean;
  /** Local request timing, without assumptions about server/client clock skew. */
  onTiming?: (sample: { trigger: "poll" | "push"; fetchMs: number }) => void;
  /** Failed list request, including its HTTP status when one was received. */
  onFailure?: (sample: { trigger: "poll" | "push"; fetchMs: number; status?: number; error: unknown }) => void;
  signal?: AbortSignal;
  onError?: (error: unknown) => void;
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
 private transport: RuntimeTelephonyTransport;
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
  private subscribe<E>(path:string,onEvent:(event:E)=>void,opts?:Parameters<AppHandle["subscribe"]>[2]){return this.transport.subscribe(path,onEvent,opts);}

  private path(path: string): string {
    if (!this.options.authProvider) return path;
    return `/user${path}${path.includes("?") ? "&" : "?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`;
  }

  async listCalls(signal?: AbortSignal): Promise<Call[]> {
    const result = await this.get<{ calls: Call[] }>(this.path("/calls"), { signal });
    if (!Array.isArray(result?.calls) || result.calls.some(c => !c || typeof c.id !== "string" || typeof c.status !== "string")) {
      throw new Error("Invalid Telephony calls response");
    }
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
  watchCalls(onCalls: (calls: Call[]) => void, options: WatchCallsOptions = {}): { close(): void } {
    const interval = options.intervalMs ?? 2000;
    if (!Number.isFinite(interval) || interval < 100) throw new Error("Call watch interval must be at least 100 ms");
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stream: { close(): void } | undefined;
    let running = false;
    let queued = false;
    let lastPush = -Infinity;
    let hintedState: string | undefined, appliedState: string | undefined;
    let lastRefresh=performance.now();
    const nextPollDelay = () => {
      const age=performance.now()-lastPush;
      return age<15000 ? Math.max(1,Math.min(Math.max(interval,30000)-(performance.now()-lastRefresh),15000-age)) : interval;
    };
    const acknowledged = new Set<string>();
    const close = () => {
      clearTimeout(timer); controller.abort(); stream?.close();
      options.signal?.removeEventListener("abort", close);
    };
    const report = (error: unknown) => { try { options.onError?.(error); } catch { close(); } };
    options.signal?.addEventListener("abort", close, { once: true });
    if (options.signal?.aborted) close();
    const refresh = async (trigger: "poll" | "push") => {
      if (controller.signal.aborted) return;
      if (running) { queued = true; return; }
      clearTimeout(timer); running = true;
      const started = performance.now();
      const stateAtStart = hintedState;
      try {
        const calls = await this.listCalls(controller.signal);
        lastRefresh=performance.now();
        if (!controller.signal.aborted) {
          onCalls(calls);
          appliedState = stateAtStart;
          for (const call of calls) {
            if (call.answerable !== true || call.status !== "pending") continue;
            for (const offer of call.ring_offers ?? []) {
              if (offer.kind !== "browser" || !offer.id || acknowledged.has(offer.id)) continue;
              acknowledged.add(offer.id);
              void this.acknowledgeOffer(call.id, offer.id).catch(error => {
                if (![404, 405].includes((error as { status?: number })?.status ?? 0)) acknowledged.delete(offer.id);
              });
            }
          }
          // Diagnostic observers cannot interrupt call detection.
          try { options.onTiming?.({ trigger, fetchMs: performance.now() - started }); } catch {}
        }
      } catch (error) {
        lastPush=-Infinity; appliedState=undefined;
        if (!controller.signal.aborted) {
          const status = (error as { status?: unknown })?.status;
          try { options.onFailure?.({ trigger, fetchMs: performance.now() - started, status: typeof status === "number" ? status : undefined, error }); } catch {}
          report(error);
        }
      } finally {
        running = false;
        if (!controller.signal.aborted) {
          if (queued) { queued = false; void refresh("push"); }
          else timer = setTimeout(() => void refresh("poll"), nextPollDelay());
        }
      }
    };
    void refresh("poll");
    if (!controller.signal.aborted && options.push !== false && typeof this.app.subscribe === "function") {
      try {
        stream = this.subscribe<{ type?: string; state?: string }>(this.path("/calls/events"), event => {
          if (event.type === "heartbeat" || event.type === "calls.changed") {
            lastPush=performance.now();
            if(event.type==="calls.changed") {
              const state = typeof event.state === "string" && /^[0-9a-f]{64}$/.test(event.state) ? event.state : undefined;
              hintedState = state;
              if (!state || state !== appliedState || running) void refresh("push");
            }
            else if(!running) {clearTimeout(timer);timer=setTimeout(()=>void refresh("poll"),nextPollDelay());}
          }
          else if (event.type === "access.revoked") { lastPush=-Infinity; stream?.close(); void refresh("push"); }
        }, {
          transport: "fetch", signal: controller.signal, reconnectDelayMs: 250,
          // Every stream authenticates. Equal visible-state hints reuse the last
          // successful read; errors and periodic polls retain recovery.
          onError: error => {
            lastPush=-Infinity;
            if(!running&&!controller.signal.aborted) {clearTimeout(timer);timer=setTimeout(()=>void refresh("poll"),interval);}
            const status = (error as { status?: number })?.status;
            if (status === 404 || status === 405 || status === 501) stream?.close();
            // Streaming is optional; the normal authenticated poll reports
            // actionable authentication errors and keeps old servers working.
          },
        });
      } catch { /* Older hosts can continue using polling. */ }
    }
    return { close };
  }

  async place(request: DialRequest): Promise<CallSession> {
    if (!/^\+[1-9]\d{7,14}$/.test(request.to) || !request.idempotency_key?.trim()) {
      throw new Error("Dial requires an E.164 number and an idempotency key");
    }
    const started = leaseClock();
    return this.session(await this.post(this.path("/softphone/place"), request), undefined, "media", started);
  }

  async answer(id: string, request: AnswerRequest = {}): Promise<CallSession> {
    const started = leaseClock();
    try {
      return this.session(await this.post(this.path(`/softphone/answer/${callID(id)}`), request), id, "media", started);
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
    await this.post(this.path(`/softphone/offer/ack/${callID(callIDValue)}`), { offer_id: offerID });
  }

  async declineOffer(callIDValue: string, offerID: string): Promise<void> {
    await this.post(this.path(`/softphone/offer/decline/${callID(callIDValue)}`), { offer_id: offerID });
  }

  /** Attach an assigned human call; never dials or takes another user's call. */
  async attach(id: string, diagnostics?: MediaAttachmentDiagnostics, init?:RequestInit, expectedGeneration?:string): Promise<CallSession> {
    const started = leaseClock();
    return this.session(await boundedMediaRequest(signal=>this.post(this.path(`/softphone/attach/${callID(id)}`), {...(diagnostics ? {media_diagnostics:diagnostics} : {}),...(expectedGeneration!==undefined && diagnostics?.attempt_id ? {attachment_guard:{id:diagnostics.attempt_id,expected_generation:expectedGeneration}} : {})},{...init,signal}),init?.signal??undefined), id, "media", started);
  }

  async takeover(id: string, diagnostics?: MediaAttachmentDiagnostics): Promise<CallSession> {
    const started = leaseClock();
    return this.session(await this.post(this.path(`/softphone/takeover/${callID(id)}`), diagnostics ? {media_diagnostics:diagnostics} : {}), id, "media", started);
  }

  createCallListener(options: CallListenerOptions = {}): HeadlessCallListener { return new HeadlessCallListener(this, {...options,...listenerAudioOptions({...this.options.audio,...options.audio},options)}); }

  async listenSession(id: string, signal?:AbortSignal): Promise<CallSession> { const started = leaseClock(); return this.session(await boundedMediaRequest(s=>this.post(this.path(`/softphone/listen/${callID(id)}`), {},{signal:s}),signal,5000,value=>{try{void this.stopListening(this.session(value,id,"listen-media",started)).catch(()=>{});}catch{/* invalid late credentials cannot be used */}}), id, "listen-media", started); }
  async coachSession(id: string): Promise<CallSession> { const started = leaseClock(); const s=this.session(await this.post(this.path(`/softphone/coach/${callID(id)}`), {}),id,"listen-media",started); if(s.coaching!==true) throw new Error("Invalid coaching session");return s; }
  async renewListening(session: CallSession): Promise<{lease_seconds?: number}> { return this.post(this.path(`/softphone/${session.coaching ? "coach-renew" : "listen-renew"}/${callID(session.call_id)}`), { session_token: session.session_token }); }
  async stopListening(session: CallSession): Promise<void> { await this.post(this.path(`/softphone/${session.coaching ? "coach-stop" : "listen-stop"}/${callID(session.call_id)}`), { session_token: session.session_token }); }
  async listenerAudit(id: string): Promise<{ listeners: Array<{ id: string; principal: unknown; joined_at: string; left_at: string; reason: string; diagnostics: unknown; mode: "listen"|"coach" }>; coaching: Array<{id:string;listener_audit_id:string;principal:unknown;started_at:string;ended_at:string;reason:string}> }> { return this.get(this.path(`/softphone/listen-audit/${callID(id)}`)); }

  /** Operator-only configuration. Never available through adviser app-user grants.
   * Transport changes affect new calls; credentials are write-only. */
  /** Operator-only optional carrier buffer. Off by default; shared resource scope.
   * apply defers while calls are active. Does not change local SIP recovery. */
  carrierMediaSettings(request:CarrierMediaSettingsRequest):Promise<CarrierMediaSettingsResult> {
    return this.post(this.path("/numbers/carrier-media"),request);
  }
  outboundTransport(request:OutboundTransportRequest):Promise<OutboundTransportResult> {
    return this.post(this.path("/numbers/outbound-transport"),request);
  }
  /** Authorized choices for new calls; disabling never changes existing media grants. */
  outboundNumbers(options: { fresh?: boolean } = {}): Promise<OutboundNumbersResult> { return this.get(this.path(options.fresh ? "/softphone/numbers?fresh=true" : "/softphone/numbers")); }

  async renew(session: CallSession): Promise<{lease_seconds?: number}> {
    return this.post(this.path(`/softphone/renew/${callID(session.call_id)}`), { session_token: session.session_token });
  }

  async release(session: CallSession): Promise<void> {
    if (!session.session_token) throw new Error("Answer session has no release token");
    await this.post(this.path(`/softphone/release/${callID(session.call_id)}`), { session_token: session.session_token });
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
