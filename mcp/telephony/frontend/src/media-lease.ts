/** No credentials or media URLs are included in session diagnostics. */
export type MediaInitiatingAction = "attach" | "takeover" | "automatic_retry" | "manual_reconnect" | "audio_device_change" | "component_recreation";
export interface MediaRecoveryContext {
 recovery_id: string;
 attempt_id: string;
 initiating_action: MediaInitiatingAction;
}
export interface MediaAttachmentDiagnostics extends Partial<MediaRecoveryContext> { previous_session_id?: string }
export function mediaDiagnosticID(kind: "recovery" | "attempt"): string {
 const suffix=globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;
 return `${kind}-${suffix}`;
}
export function mediaRecoveryContext(action:MediaInitiatingAction): MediaRecoveryContext {
 return {recovery_id:mediaDiagnosticID("recovery"),attempt_id:mediaDiagnosticID("attempt"),initiating_action:action};
}
export function safeMediaDiagnosticID(value?:string): string | undefined {
 return typeof value==="string" && value.length<=80 && /^(browser|session|recovery|attempt)-[a-f0-9-]{1,64}$/.test(value) ? value : undefined;
}
export function safeMediaSessionEvent(event:MediaSessionEvent):MediaSessionEvent {
 const label=(v:string|undefined,max:number)=>typeof v==="string" && v.length<=max && /^[a-zA-Z0-9_-]*$/.test(v)?v:undefined;
 const knownDetails=["WebSocket transport error","WebRTC signaling error","Audio worker failed","WebRTC signaling unavailable"];
 const actions:MediaInitiatingAction[]=["attach","takeover","automatic_retry","manual_reconnect","audio_device_change","component_recreation"];
 const numeric=(v:number|undefined,max:number)=>typeof v==="number" && Number.isFinite(v) ? Math.max(0,Math.min(max,v)) : undefined;
 const timestamp=Number.isFinite(Date.parse(event.timestamp)) ? new Date(event.timestamp).toISOString() : new Date().toISOString();
 return {timestamp,action:label(event.action,40)??"diagnostic",outcome:label(event.outcome,40)??"unknown",code:label(event.code,80),
  detail:event.detail ? knownDetails.includes(event.detail) ? event.detail : "detail_redacted" : undefined,
  recovery_id:safeMediaDiagnosticID(event.recovery_id),attempt_id:safeMediaDiagnosticID(event.attempt_id),
  shared_attempt_id:safeMediaDiagnosticID(event.shared_attempt_id),session_id:safeMediaDiagnosticID(event.session_id),previous_session_id:safeMediaDiagnosticID(event.previous_session_id),
  initiating_action:actions.includes(event.initiating_action!) ? event.initiating_action : undefined,
  status:numeric(event.status,599),remaining_ms:numeric(event.remaining_ms,3600000),was_clean:typeof event.was_clean==="boolean" ? event.was_clean : undefined,duration_ms:numeric(event.duration_ms,86400000)};
}
export interface MediaSessionEvent {
  recovery_id?: string;
  attempt_id?: string;
  shared_attempt_id?: string;
  session_id?: string;
  previous_session_id?: string;
  initiating_action?: MediaInitiatingAction;
  timestamp: string;
  action: string;
  outcome: string;
  status?: number;
  code?: string;
  remaining_ms?: number;
  detail?: string;
  was_clean?: boolean;
  duration_ms?: number;
}
export const leaseClock = () => performance.now();
export function mediaFailure(error: unknown): { status?: number; code?: string; denied: boolean; expired: boolean } {
  const value = error as {status?: number; body?: string};
  const status = value?.status;
  let code: string | undefined;
  try { const body = JSON.parse(value?.body ?? "{}"); if (typeof body.code === "string") code = body.code; } catch { /* Plain HTTP responses remain supported. */ }
  const expired = code === "media_lease_expired";
  return { status, code, expired, denied: !expired && [401, 403, 404, 410].includes(status!) };
}

export interface LeaseTimers {
  now(): number;
  setTimeout(callback: () => void, ms: number): ReturnType<typeof setTimeout>;
  clearTimeout(timer?: ReturnType<typeof setTimeout>): void;
}

/** An independent expiry watchdog also bounds a hung renewal request. */
export class MediaLease {
  private renewTimer?: ReturnType<typeof setTimeout>;
  private expiryTimer?: ReturnType<typeof setTimeout>;
  private requestTimer?: ReturnType<typeof setTimeout>;
  private stopped = false;
  private retryMS = 250;
  private deadline: number;
  constructor(private seconds: number, private renew: () => Promise<{lease_seconds?: number} | void>,
    private ended: (reason: "expired" | "revoked", error?: unknown) => void,
    private event?: (event: MediaSessionEvent) => void, startedMS = leaseClock(), private clock: LeaseTimers = {now:leaseClock,setTimeout:(callback,ms)=>setTimeout(callback,ms),clearTimeout:timer=>clearTimeout(timer)}) {
    // The server stores Unix seconds; reserve one second for truncation.
    this.deadline = startedMS + seconds * 1000 - 1000;
    this.watchExpiry(); this.schedule(Math.min(seconds * 1000 / 3, this.remaining()));
  }
  private remaining() { return Math.max(0, this.deadline - this.clock.now()); }
  private report(outcome: string, error?: unknown) {
    const {status,code} = mediaFailure(error);
    try { this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome,
      status,code,remaining_ms:Math.round(this.remaining())}); } catch { /* host observers cannot interrupt media */ }
  }
  private watchExpiry() {
    this.clock.clearTimeout(this.expiryTimer);
    this.expiryTimer = this.clock.setTimeout(() => this.finish("expired"), this.remaining());
  }
  private schedule(ms: number) {
    this.renewTimer = this.clock.setTimeout(() => void this.tick(), Math.max(0, ms));
  }
  private async tick() {
    if (this.stopped) return;
    if (!this.remaining()) { this.finish("expired"); return; }
    const sent = this.clock.now();
    try {
      const result = await Promise.race([this.renew(), new Promise<never>((_, reject) => {
        this.requestTimer = this.clock.setTimeout(() => reject(new Error("Media renewal timed out")), Math.min(5000, this.remaining()));
      })]);
      if (this.stopped) return;
      if (result?.lease_seconds !== undefined) {
        if (!Number.isFinite(result.lease_seconds) || result.lease_seconds < 10 || result.lease_seconds > 3600) throw new Error("Invalid media renewal lease");
        this.seconds = result.lease_seconds;
      }
      this.deadline = sent + this.seconds * 1000 - 1000;
      this.retryMS = 250; this.watchExpiry(); this.report("renewed");
      this.schedule(Math.min(this.seconds * 1000 / 3, this.remaining()));
    } catch (error) {
      if (this.stopped) return;
      const failure = mediaFailure(error);
      if (failure.denied || failure.expired) { this.finish(failure.expired ? "expired" : "revoked", error); return; }
      this.report("retrying", error);
      this.schedule(Math.min(this.retryMS, this.remaining()));
      this.retryMS = Math.min(4000, this.retryMS * 2);
    } finally { this.clock.clearTimeout(this.requestTimer); this.requestTimer = undefined; }
  }
  private finish(reason: "expired" | "revoked", error?: unknown) {
    if (this.stopped) return;
    this.report(reason, error); this.stop(); this.ended(reason, error);
  }
  stop() {
    this.stopped = true;
    this.clock.clearTimeout(this.renewTimer); this.clock.clearTimeout(this.expiryTimer); this.clock.clearTimeout(this.requestTimer);
  }
}
