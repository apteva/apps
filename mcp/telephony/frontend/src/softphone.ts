import {validateNetworkPolicy,type NetworkPolicy} from "./network-policy";
import {withNetworkAbort,type NetworkCheckResult,type NetworkCheckOptions} from "./network-check";
import { AudioRecoveryLoop } from "./audio-recovery";
import { mediaFailure, mediaDiagnosticID, boundedMediaRequest } from "./media-lease";
import type { CallListenerOptions } from "./listener";
import { MediaLease, mediaRecoveryContext, safeMediaSessionEvent, type MediaInitiatingAction, type MediaRecoveryContext, type MediaSessionEvent } from "./media-lease";
import { DEFAULT_SOFTPHONE_AUDIO_OPTIONS, playbackBufferOptions, type SoftphoneMediaReadiness, type SoftphoneAudioHealth, type SoftphoneAudioOptions, type SoftphoneCallStatus, type SoftphoneDiagnostics, type SoftphoneState } from "../../ui/softphone-audio";
import { createBrowserAudio, type AudioConnection, type AudioRuntime } from "./audio";
import { measureAudioSetup } from "../../ui/audio-setup";
import { isTerminalCall, type AnswerRequest, type Call, type CallControlResult, type CallSession, type CallTermination, type DialRequest, type TelephonyClient } from "./client";

/** Carrier progress; connected is exposed only after browser media is live. */
export type SoftphonePhase = "idle" | "placing" | "ringing" | "connected" | "ended";
export interface RingbackOptions { country?: string }

export function phaseForStatus(status?: string): SoftphonePhase {
  if (!status) return "placing";
  if (isTerminalCall(status)) return "ended";
  if (status === "ringing") return "ringing";
  if (status === "answered" || status === "in-progress") return "connected";
  return "placing";
}

export interface SoftphoneSnapshot {
  readonly callId?: string;
 readonly networkCheck?: NetworkCheckResult;
 readonly networkChecking?:boolean;
  readonly carrierStatus?: string;
  readonly audioState: SoftphoneState | "idle";
  readonly busy: boolean;
  readonly muted: boolean;
  readonly detail?: string;
  readonly phase: SoftphonePhase;
  /** Retained after the call ends until the next dial or answer. */
  readonly termination?: CallTermination;
  readonly answeredBy?: string;
  readonly endedAt?: string;
  readonly holdState?: Call["hold_state"];
  readonly recordingState?: Call["recording_state"];
  readonly controlError?: string;
  readonly capabilities?: Call["capabilities"];
}
export interface SoftphoneOptions {
 /** Idle-only network readiness checks. Enabled by the bundled panel, opt-in for existing hosts. */
 networkCheck?: boolean;
 onNetworkCheck?: (result:NetworkCheckResult)=>void;
  onMediaReadiness?: (event: SoftphoneMediaReadiness) => void;
	/** Applied to the shared browser runtime; defaults to websocket. */
	mediaTransport?: "websocket" | "webrtc" | "auto";
  audio?: Partial<SoftphoneAudioOptions>;
  /** 0 disables automatic status reconciliation; the host then calls observeCall. */
  pollIntervalMs?: number;
  onLevels?: (microphone: number, speaker: number) => void;
  onAudioHealth?: (health: SoftphoneAudioHealth) => void;
  onDiagnostics?: (value: SoftphoneDiagnostics) => void;
  onNotice?: (detail: string) => void;
  onSessionEvent?: (event: MediaSessionEvent) => void;
  /** Alternate device implementation for non-browser hosts or controlled tests. */
  audioRuntime?: AudioRuntime;
  /** Play a locally synthesized ringback while an outbound call rings. Off by default; pass a country code for its cadence (France otherwise). */
  ringback?: boolean | RingbackOptions;
}

const message = (error: unknown) => error instanceof Error ? error.message : String(error);

/** One human operator's call. UI frameworks subscribe to immutable snapshots. */
export class HeadlessSoftphone {
  private snapshot: SoftphoneSnapshot = Object.freeze({ audioState: "idle", busy: false, muted: false, phase: "idle" });
  private listeners = new Set<(state: SoftphoneSnapshot) => void>();
  private audio?: AudioConnection;
  private session?: CallSession;
  private disposed = false;
  private generation = 0;
  private cancellation = new AbortController();
  private hangingUp?: Promise<void>;
  private controlPending?: Promise<CallControlResult>;
  private intent?: { value: string; request: DialRequest };
  private lease?: MediaLease;
  private timer?: ReturnType<typeof setTimeout>;
  private polling?: AbortController;
  private hasStatusPush=false;
  private automaticRecovery?:AudioRecoveryLoop;
  private hasLiveAudio=false;
  private uncertainAttachment?:{diagnostics:MediaRecoveryContext&{previous_session_id?:string};expected?:string};
  private scheduleStatusPoll?: () => void;
  private outbound = false;
  private ringing = false;
  private readonly runtime: AudioRuntime;
  private audioOptions: SoftphoneAudioOptions;
  private preferredPlayback:Pick<SoftphoneAudioOptions,"playbackTargetMs"|"playbackMinMs"|"playbackMaxMs">={};
  private preferredMediaTransport: SoftphoneAudioOptions["mediaTransport"];
 private networkTimer?:ReturnType<typeof setTimeout>;
 private networkRequest?:AbortController;
 private readonly networkChanged=()=>{if(this.disposed)return;if(this.snapshot.networkCheck)this.update({networkCheck:{...this.snapshot.networkCheck,state:"inconclusive"}});this.scheduleNetworkCheck(1000);};
  private readonly interval: number;

  constructor(readonly client: TelephonyClient, private readonly options: SoftphoneOptions = {}) {
    this.runtime = options.audioRuntime ?? createBrowserAudio(client.app);
    this.audioOptions = { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS, ...options.audio, ...(options.mediaTransport ? { mediaTransport: options.mediaTransport } : {}) };
    this.preferredMediaTransport=this.audioOptions.mediaTransport;
    this.preferredPlayback={playbackTargetMs:this.audioOptions.playbackTargetMs,playbackMinMs:this.audioOptions.playbackMinMs,playbackMaxMs:this.audioOptions.playbackMaxMs};
    playbackBufferOptions(this.audioOptions);
    this.interval = options.pollIntervalMs ?? 2000;
    if (!Number.isFinite(this.interval) || (this.interval !== 0 && this.interval < 100)) throw new Error("Call poll interval must be 0 or at least 100 ms");
    if(options.networkCheck){globalThis.addEventListener?.("online",this.networkChanged);this.scheduleNetworkCheck(500+Math.random()*15000);}
  }

  private scheduleNetworkCheck(delay=300000+Math.random()*60000){
    if(!this.options.networkCheck||this.disposed)return;
    clearTimeout(this.networkTimer);
    this.networkTimer=setTimeout(()=>{if(!this.session&&!this.snapshot.busy&&(typeof document==="undefined"||document.visibilityState!=="hidden"))void this.checkNetwork().catch(()=>{});else this.scheduleNetworkCheck();},delay);
  }
  private cancelNetworkCheck(){if(this.snapshot.networkChecking)this.update({networkChecking:false});clearTimeout(this.networkTimer);this.networkRequest?.abort();this.networkRequest=undefined;this.client.cancelNetworkChecks();}
  /** Does not alter busy/call state; dial and answer synchronously cancel it. */
  async checkNetwork(options:NetworkCheckOptions={}):Promise<NetworkCheckResult>{
    this.assertOpen();if(this.session||this.snapshot.busy||!this.client.networkChecksAllowed)throw Error("Network checks pause during calls or incoming offers");
    this.cancelNetworkCheck();const controller=new AbortController();this.networkRequest=controller;const unregister=this.client.registerNetworkCheck(controller);this.update({networkChecking:true});
    const cancel=()=>controller.abort();options.signal?.addEventListener("abort",cancel,{once:true});if(options.signal?.aborted)cancel();
    let profileDeadline:ReturnType<typeof setTimeout>|undefined;
    try{
      // Policy discovery is never on the call-admission path. A slow or old
      // server cannot stall answering, microphone preparation or cancellation.
      const profileController=new AbortController(),cancelProfile=()=>profileController.abort();controller.signal.addEventListener("abort",cancelProfile,{once:true});
      profileDeadline=setTimeout(cancelProfile,3000);
      let enabled=true;
      try {const view=await withNetworkAbort(this.client.networkPolicy({},profileController.signal),profileController.signal);const profile=validateNetworkPolicy(view.effective);enabled=profile.background_check;
        if(!controller.signal.aborted&&!this.session&&!this.snapshot.busy)this.applyNetworkPolicy(view.configured===false?undefined:profile);
      }catch { /* optional policy discovery never breaks existing calls */ }
      finally {clearTimeout(profileDeadline);controller.signal.removeEventListener("abort",cancelProfile);}
      if(!enabled && !options.paths)return {state:"unavailable",checkedAt:new Date().toISOString(),paths:[]};
      const result=await this.client.checkNetwork({...options,signal:controller.signal});
      if(this.networkRequest===controller&&!controller.signal.aborted){this.update({networkCheck:result});try{this.options.onNetworkCheck?.(result);}catch{/* observer isolation */}}
      return result;
    }finally{unregister();clearTimeout(profileDeadline);options.signal?.removeEventListener("abort",cancel);if(this.networkRequest===controller){this.networkRequest=undefined;this.update({networkChecking:false});this.scheduleNetworkCheck();}}
  }

  /** Applies to the next attachment, without restarting an existing call. */
  applyNetworkPolicy(value?:NetworkPolicy):void {
    this.assertOpen();
    if(value===undefined){if(this.audioOptions.networkProfile)this.audioOptions={...this.audioOptions,...this.preferredPlayback,networkProfile:undefined,mediaTransport:this.preferredMediaTransport};return;}
    const profile=validateNetworkPolicy(value);
    this.audioOptions={...this.audioOptions,networkProfile:profile,playbackTargetMs:profile.playback_target_ms,playbackMinMs:Math.min(this.preferredPlayback.playbackMinMs??60,profile.playback_target_ms),playbackMaxMs:profile.playback_max_ms,mediaTransport:profile.media_transport==="inherit"?this.preferredMediaTransport:profile.media_transport};
  }

  /** New listener/coach sessions inherit this softphone's current audio profile. */
  createCallListener(options:CallListenerOptions = {}) { this.assertOpen(); return this.client.createCallListener({...this.audioOptions,...options.audio,...options}); }

  getSnapshot = (): SoftphoneSnapshot => this.snapshot;
  subscribe = (listener: (state: SoftphoneSnapshot) => void): (() => void) => {
    this.assertOpen();
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };

  private update(patch: Partial<SoftphoneSnapshot>) {
    const next={...this.snapshot,...patch};
    if(next.callId && phaseForStatus(next.carrierStatus)==="connected") next.phase=next.audioState==="live"?"connected":"placing";
    this.snapshot = Object.freeze(next);
    for (const listener of this.listeners) {
      // Host rendering must not interrupt call cleanup or start another dial.
      try { listener(this.snapshot); } catch { /* isolate observer failures */ }
    }
  }
  private assertOpen() { if (this.disposed) throw new Error("Softphone has been disposed"); }
  private assertCurrent(generation: number) {
    if (this.disposed || generation !== this.generation || this.cancellation.signal.aborted) throw new Error("Softphone operation cancelled");
  }
  private invalidate(): number {
    this.runtime.cancelPreparation?.();
    this.automaticRecovery?.stop();this.automaticRecovery=undefined;this.uncertainAttachment=undefined;
    this.cancellation.abort();
    this.cancellation = new AbortController();
    return ++this.generation;
  }
  private current(generation: number) { return !this.disposed && generation === this.generation; }
  private finish(generation: number) {
    if (this.current(generation)) { this.runtime.cancelPreparation?.(); this.update({ busy: false }); }
  }
  // Device permission and audio startup may never settle. Cancellation must
  // free controls immediately; the runtime still owns cleanup of late devices.
  private cancellable<T>(pending: Promise<T>, generation: number): Promise<T> {
    const signal = this.cancellation.signal;
    return new Promise<T>((resolve, reject) => {
      const cancel = () => { signal.removeEventListener("abort", cancel); reject(new Error("Softphone operation cancelled")); };
      signal.addEventListener("abort", cancel, { once: true });
      pending.then(resolve, reject).finally(() => signal.removeEventListener("abort", cancel));
      if (!this.current(generation) || signal.aborted) cancel();
    });
  }
  private begin(requireIdle: boolean): number {
 this.cancelNetworkCheck();
    this.assertOpen();
    if (this.snapshot.busy || this.hangingUp || (requireIdle && this.session)) throw new Error("Softphone already has an active operation or call");
    const generation = this.invalidate();
    this.update({ busy: true, detail: undefined });
    return generation;
  }

  private async prepareAudio(generation: number, microphone: boolean): Promise<MediaSessionEvent[]> {
    const events: MediaSessionEvent[] = [];
    const emit = (event: MediaSessionEvent) => { if (this.current(generation) && !this.cancellation.signal.aborted) events.push(safeMediaSessionEvent(event)); };
    try {
      await this.cancellable(measureAudioSetup(microphone ? "audio_preflight" : "audio_prepare", emit, () => microphone
        ? this.runtime.preflight(this.audioOptions, emit)
        : this.runtime.prepare?.(this.audioOptions, emit) ?? Promise.resolve()), generation);
      this.assertCurrent(generation);
      return events;
    } catch (error) {
      if (this.current(generation)) for (const event of events) this.reportSessionEvent(event);
      throw error;
    }
  }

  async dial(input: Omit<DialRequest, "idempotency_key"> & { idempotency_key?: string }): Promise<string> {
    const generation = this.begin(true);
    let placed: CallSession | undefined;
    let attached = false;
    try {
      this.assertCurrent(generation);
      const normalized = { to: input.to.trim(), from: input.from?.trim(), timeout_sec: input.timeout_sec, recording: input.recording };
      const value = JSON.stringify(normalized);
      if (!this.intent || this.intent.value !== value || (input.idempotency_key && input.idempotency_key !== this.intent.request.idempotency_key)) {
        this.intent = { value, request: { ...normalized, idempotency_key: input.idempotency_key || crypto.randomUUID() } };
      }
      const setupEvents = await this.prepareAudio(generation, true);
      this.assertCurrent(generation);
      // Do not abort a write whose carrier outcome would then be unknown.
      placed = await this.client.place(this.intent.request);
      this.intent = undefined;
      this.assertCurrent(generation);
      attached = true;
      this.outbound = true;
      await this.attachAudio(placed, generation, setupEvents);
      return placed.call_id;
    } catch (error) {
      if (placed && (this.current(generation) || !attached || this.disposed)) {
        await this.recoverStartup(placed, generation, () => this.client.hangup(placed!.call_id));
      }
      if (this.current(generation)) this.update({ detail: message(error) });
      throw error;
    } finally { this.finish(generation); }
  }

  async answer(id: string, request: AnswerRequest = {}): Promise<void> {
    const generation = this.begin(true);
    let claimed: CallSession | undefined;
    let attached = false;
    try {
      this.assertCurrent(generation);
      const setupEvents = await this.prepareAudio(generation, true);
      claimed = await this.client.answer(id, request);
      this.assertCurrent(generation);
      attached = true;
      this.outbound = false;
      await this.attachAudio(claimed, generation, setupEvents);
    } catch (error) {
      if (claimed && (this.current(generation) || !attached || this.disposed)) {
        await this.recoverStartup(claimed, generation, () => this.client.release(claimed!));
      }
      if (this.current(generation)) this.update({ detail: message(error) });
      throw error;
    } finally { this.finish(generation); }
  }

  private async recoverStartup(session: CallSession, generation: number, cleanup: () => Promise<void>) {
    try {
      await cleanup();
      if (this.current(generation)) this.clearCall();
    } catch {
      if (this.current(generation)) {
        this.session = session;
        this.update({ callId: session.call_id, audioState: "error" });
        this.startPolling();
      }
    }
  }

  /** Explicitly rejoin a known call after a reload; server permissions still apply. */
  async join(id: string): Promise<void> { await this.answer(id, { rejoin: true }); }

  /** Resume a backend-assigned call without placing a new carrier leg. */
  async attach(id: string, action: MediaInitiatingAction = "attach"): Promise<void> { await this.acquireSession(id, false, action); }

  /** Explicit supervisor action; normal attach/join cannot displace another user. */
  async takeover(id: string): Promise<void> { await this.acquireSession(id, true); }

  private async acquireSession(id: string, takeover: boolean, action: MediaInitiatingAction = "takeover"): Promise<void> {
    const generation = this.begin(true);
    try {
      const setupEvents = await this.prepareAudio(generation, true);
      this.assertCurrent(generation);
      const recovery=mediaRecoveryContext(takeover ? "takeover" : action);
      const session = await (takeover ? this.client.takeover(id,recovery) : this.client.attach(id,recovery));
      this.assertCurrent(generation);
      await this.attachAudio(session, generation, setupEvents);
      // The call was placed elsewhere, so its direction and current status are
      // unknown here. One read settles both instead of waiting for polling.
      await this.reconcileAttachedCall(session, generation);
    } catch (error) {
      if (this.current(generation)) this.update({ detail: message(error) });
      throw error;
    } finally { this.finish(generation); }
  }

  async reconnect(audio?: Partial<SoftphoneAudioOptions>, action: MediaInitiatingAction = "manual_reconnect"): Promise<void> {
    if (!this.session) throw new Error("No call to reconnect");
    const nextOptions = { ...this.audioOptions, ...audio };
    playbackBufferOptions(nextOptions);
    const generation = this.begin(false);
    this.configureAudio(audio??{});
    const id = this.session.call_id;
    const recovery=mediaRecoveryContext(action),previous_session_id=this.session.session_generation;
    const report=(outcome:string,extra:Partial<MediaSessionEvent>={})=>this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome,...recovery,previous_session_id,...extra});
    report("started");
    try {
      // Keep the current audio running while scripts prepare. Acquire a new
      // server generation only after preparation, preserving token ownership.
      const setupEvents = await this.prepareAudio(generation, false);
      let session:CallSession;
      try{session=await this.acquireRecoverySession(this.session!,{...recovery,previous_session_id},this.cancellation.signal);}
      catch(error){
        const f=mediaFailure(error);let current:string|undefined;
        try{current=JSON.parse((error as {body?:string}).body??"{}").current_generation;}catch{}
        if(action==="automatic_retry" || f.code!=="media_session_changed" || !current || !/^session-[a-f0-9-]{1,64}$/.test(current))throw error;
        // An explicit user action may conditionally replace the observed current
        // generation. Automatic retry never displaces a newer attachment.
        this.assertCurrent(generation);
        session=await this.client.attach(id,{...mediaRecoveryContext(action),previous_session_id:current},{signal:this.cancellation.signal},current);
      }
      this.assertCurrent(generation);
      await this.attachAudio(session, generation, setupEvents);
      await this.reconcileAttachedCall(session, generation);
      report("connected",{session_id:session.session_generation});
    }
    catch (error) { report(this.current(generation)?"failed":"cancelled"); if (this.current(generation)) this.update({ detail: message(error) }); throw error; }
    finally { this.finish(generation); }
  }

  async hangup(): Promise<void> {
    this.assertOpen();
    if (this.hangingUp) return this.hangingUp;
    const session = this.session;
    if (!session) {
      // With a write in flight, retain the busy lock until its outcome is
      // known and the cancelled operation cleans up any accepted call.
      this.cancellation.abort();
      this.runtime.cancelPreparation?.();
      return;
    }
    const generation = this.invalidate();
    this.stopAudio();
    this.update({ busy: true, audioState: "ended" });
    const pending = (async () => {
      try {
        await this.client.hangup(session.call_id);
        if (this.current(generation)) this.clearCall();
      } catch (error) {
        if (this.current(generation)) {
          this.update({ audioState: "error", detail: message(error) });
          this.startPolling();
        }
        throw error;
      } finally { this.finish(generation); }
    })();
    this.hangingUp = pending;
    try { await pending; }
    finally { if (this.hangingUp === pending) this.hangingUp = undefined; }
  }

  /** Call controls act on this controller's current call, never dial a new leg. */
  hold(): Promise<CallControlResult> { return this.runCallControl("hold"); }
  resume(): Promise<CallControlResult> { return this.runCallControl("resume"); }
  pauseRecording(): Promise<CallControlResult> { return this.runCallControl("pauseRecording"); }
  resumeRecording(): Promise<CallControlResult> { return this.runCallControl("resumeRecording"); }

  private async runCallControl(action: "hold" | "resume" | "pauseRecording" | "resumeRecording"): Promise<CallControlResult> {
    this.assertOpen();
    const session = this.session;
    if (!session) throw new Error("No active call to control");
    if (this.snapshot.busy || this.hangingUp || this.controlPending) throw new Error("Softphone already has an active operation");
    const generation = this.generation;
    this.update({ busy: true, detail: undefined });
    let pending: Promise<CallControlResult>;
    try { pending = this.client[action](session.call_id); }
    catch (error) {
      if (this.current(generation) && this.session === session) this.update({ busy: false, detail: message(error) });
      throw error;
    }
    this.controlPending = pending;
    try {
      const result = await pending;
      if (this.current(generation) && this.session === session) {
        this.update({ holdState: result.hold_state, recordingState: result.recording_state,
          controlError: result.control_error, capabilities: result.capabilities });
      }
      return result;
    } catch (error) {
      if (this.current(generation) && this.session === session) this.update({ detail: message(error) });
      throw error;
    } finally {
      if (this.controlPending === pending) this.controlPending = undefined;
      if (this.current(generation) && this.session === session) this.update({ busy: false });
    }
  }

  /** Device/processing changes apply on the next dial, answer, or reconnect. */
  configureAudio(options: Partial<SoftphoneAudioOptions>): void {
    this.assertOpen();
    const nextOptions = { ...this.audioOptions, ...options };
    playbackBufferOptions(nextOptions);
    if(options.mediaTransport!==undefined)this.preferredMediaTransport=options.mediaTransport;
    for(const key of ["playbackTargetMs","playbackMinMs","playbackMaxMs"] as const)if(key in options)this.preferredPlayback[key]=options[key];
    this.audioOptions = nextOptions;
    if(nextOptions.networkProfile)this.applyNetworkPolicy(nextOptions.networkProfile);
  }

  setMuted(muted: boolean): void {
    this.assertOpen();
    this.audio?.setMuted(muted);
    this.update({ muted });
  }
  setOutputVolume(volume: number): void {
    this.assertOpen();
    if (!Number.isFinite(volume) || volume < 0 || volume > 1) throw new Error("Volume must be between 0 and 1");
    this.audioOptions.outputVolume = volume;
    this.audio?.setOutputVolume(volume);
  }
  sendDTMF(digits: string): void {
    this.assertOpen();
    if (!/^[0-9*#]+$/.test(digits)) throw new Error("Invalid DTMF digits");
    if (this.snapshot.audioState !== "live") throw new Error("Audio is not connected");
    this.audio?.sendDTMF(digits);
  }

  /** Call completion is authoritative; transient media errors keep the call controls. */
  observeCall(call: Pick<Call, "id" | "status"> & Partial<Pick<Call, "direction" | "answered_at" | "ended_at" | "answered_by" | "termination" | "hold_state" | "recording_state" | "control_error" | "capabilities">>): void {
    if (this.disposed || call.id !== this.session?.call_id) return;
    // Ringback depends on the call's direction, not on which method opened
    // it: a call placed by a backend and attached here is still outbound.
    if (call.direction) this.outbound = call.direction === "outbound";
    if (isTerminalCall(call.status)) {
      this.invalidate();
      this.clearCall(call.status);
      this.update({ busy: false, phase: "ended", termination: call.termination, answeredBy: call.answered_by ?? this.snapshot.answeredBy, endedAt: call.ended_at });
      return;
    }
    this.update({ carrierStatus: call.status, phase: phaseForStatus(call.status), answeredBy: call.answered_by ?? this.snapshot.answeredBy,
      holdState: call.hold_state ?? this.snapshot.holdState, recordingState: call.recording_state ?? this.snapshot.recordingState,
      controlError: call.control_error ?? this.snapshot.controlError, capabilities: call.capabilities ?? this.snapshot.capabilities });
    this.syncRingback();
  }

  private ringbackCountry(): string | undefined {
    const value = this.options.ringback;
    return typeof value === "object" && value ? value.country : undefined;
  }
  // Ringback is a local courtesy tone for outbound dials only; it starts when
  // the carrier reports ringing and stops on answer, hangup, or media loss.
  private syncRingback() {
    const wanted = this.outbound && Boolean(this.options.ringback) && this.snapshot.phase === "ringing" && this.audio !== undefined;
    if (wanted && !this.ringing) {
      this.ringing = true;
      try { this.audio?.startRingback?.(this.ringbackCountry()); } catch { this.ringing = false; }
    } else if (!wanted && this.ringing) {
      this.ringing = false;
      try { this.audio?.stopRingback?.(); } catch { /* audio may already be gone */ }
    }
  }

  private async attachAudio(session: CallSession, generation: number, setupEvents: MediaSessionEvent[] = []) {
    this.hasStatusPush=false;
    this.assertCurrent(generation);
    this.stopAudio();
    this.session = session;
    this.update({ callId: session.call_id, carrierStatus: undefined, audioState: "connecting", phase: "placing", termination: undefined, answeredBy: undefined, endedAt: undefined,
      holdState: undefined, recordingState: undefined, controlError: undefined, capabilities: undefined });
    let audio: AudioConnection | undefined;
    const current = () => !this.disposed && audio !== undefined && this.audio === audio;
    const notify = (callback: () => void) => { if (current()) { try { callback(); } catch { /* isolate host callbacks */ } } };
    let refreshing: Promise<string> | undefined;
    let refreshingContext: MediaRecoveryContext | undefined;
    try {
      this.assertCurrent(generation);
      audio = this.runtime.create({
        refreshMediaURL: (requested,signal) => {
          const recovery=requested ?? mediaRecoveryContext("automatic_retry");
          if (refreshing) {
            this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"coalesced",...recovery,shared_attempt_id:refreshingContext?.attempt_id});
            return refreshing;
          }
          if (!current() || this.snapshot.busy) return Promise.reject(new Error("Audio recovery is not currently available"));
          const refreshGeneration = this.generation;
          const pending = (async () => {
            const previous_session_id=session.session_generation;
            const fresh = await this.acquireRecoverySession(session,{...recovery,previous_session_id},signal);
            this.assertCurrent(refreshGeneration);
            if (!current()) throw new Error("Audio connection no longer active");
            session = fresh; this.session = fresh; this.startLease(fresh);
            this.reportSessionEvent({timestamp:new Date().toISOString(),action:"media_session",outcome:"replaced",...recovery,previous_session_id,session_id:fresh.session_generation});
            return this.client.mediaURL(fresh, this.audioOptions.mediaTransport);
          })();
          refreshing = pending; refreshingContext=recovery;
          const reset = () => { if (refreshing === pending) {refreshing = undefined;refreshingContext=undefined;} };
          void pending.then(reset, reset);
          return pending;
        },
        sessionDiagnostics: () => ({session_id:session.session_generation}),
        onSessionEvent: event => notify(() => {
          const steps: Record<string,string> = {
            audio_assets: "Loading audio resources…", microphone_device: "Opening microphone…",
            audio_context: "Enabling browser playback…", audio_worklet: "Loading audio processor…",
            audio_transport: "Connecting audio transport…",
          };
          if (this.snapshot.busy && event.outcome === "started" && steps[event.action]) this.update({detail:steps[event.action]});
          this.options.onSessionEvent?.(safeMediaSessionEvent({...event,session_id:event.session_id??session.session_generation}));
        }),
        onState: (audioState, detail,failure) => {
          if (!current()) return;
          if (audioState === "ended" && detail === "call.ended") {
            this.observeCall({ id: session.call_id, status: "completed" });
            return;
          }
          if(audioState==="live")this.hasLiveAudio=true;
          this.update({ audioState, detail });
          this.scheduleStatusPoll?.();
          if (current() && (audioState === "error" || audioState === "ended")) {
            this.stopAudio();
            if (audioState === "error") {void this.reconcileFailedAudio(session, this.generation);if(failure?.recoverable)this.scheduleAudioRecovery();}
          }
        },
        onMediaReadiness: event => notify(() => this.options.onMediaReadiness?.(event)),
        onLevels: (mic, speaker) => notify(() => this.options.onLevels?.(mic, speaker)),
        onAudioHealth: health => notify(() => this.options.onAudioHealth?.(health)),
        onDiagnostics: diagnostics => notify(() => this.options.onDiagnostics?.(diagnostics)),
        onNotice: detail => notify(() => this.options.onNotice?.(detail)),
        onCallStatus: status => notify(() => {this.hasStatusPush=true;this.observeCall({
          id: status.call_id, status: status.status, direction: status.direction, answered_at: status.answered_at, ended_at: status.ended_at,
          answered_by: status.answered_by, termination: status.termination, hold_state: status.hold_state,
          recording_state: status.recording_state, control_error: status.control_error,
        });this.scheduleStatusPoll?.();}),
      });
      this.audio = audio;
      this.assertCurrent(generation);
      // These stages preceded session acquisition. Queue them in the chosen
      // runtime before startup so startup failures retain their evidence too.
      for (const event of setupEvents) this.reportSessionEvent(event);
      this.assertCurrent(generation);
      this.startPolling();
      this.startLease(session);
      audio.setMuted(this.snapshot.muted);
      // The transport's socket timeout starts after device/worklet setup.
      // Bound the complete operation too: those browser promises can stall.
      let startupTimer: ReturnType<typeof setTimeout> | undefined;
      try {
        await this.cancellable(Promise.race([
          audio.start(this.client.mediaURL(session, this.audioOptions.mediaTransport), this.audioOptions),
          new Promise<never>((_, reject) => {
            startupTimer = setTimeout(() => reject(new Error("Audio setup timed out. Check browser permissions and the network, then try again.")), this.audioOptions.mediaTransport === "auto" ? 40000 : 20000);
          }),
        ]), generation);
      } finally { clearTimeout(startupTimer); }
      this.assertCurrent(generation);
      if (!current()) throw new Error(this.snapshot.detail || "Audio connection ended during setup");
      audio.setMuted(this.snapshot.muted);
      this.syncRingback();
    } catch (error) {
      // An older permission/startup result must never stop a replacement call.
      if (current()) this.stopAudio();
      if (this.current(generation)) this.update({ audioState: "error", detail: message(error) });
      throw error;
    }
  }

  private async acquireRecoverySession(session:CallSession,diagnostics:MediaRecoveryContext&{previous_session_id?:string},signal?:AbortSignal):Promise<CallSession>{
    // An unknown POST outcome retries the same guarded mutation. A manual action
    // invalidates the loop and still uses a generation precondition, so a late
    // response cannot rotate a newer session behind it.
    const chosen=this.uncertainAttachment??{diagnostics,expected:session.attachment_guard_version===1?(session.session_generation??""):undefined};
    if(signal?.aborted)throw new DOMException("Cancelled","AbortError");
    this.uncertainAttachment=chosen;
    if(chosen.diagnostics.attempt_id!==diagnostics.attempt_id)this.reportSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"idempotent_retry",...diagnostics,shared_attempt_id:chosen.diagnostics.attempt_id});
    try{
      const value=await this.client.attach(session.call_id,chosen.diagnostics,{signal},chosen.expected);
      if(signal?.aborted)throw new DOMException("Cancelled","AbortError");
      this.uncertainAttachment=undefined;return value;
    }catch(error){const status=mediaFailure(error).status;if(status!==undefined&&status>=400&&status<500)this.uncertainAttachment=undefined;throw error;}
  }
  private scheduleAudioRecovery(){
    if(this.disposed||!this.session||!this.hasLiveAudio||this.automaticRecovery)return;
    const id=this.session.call_id,generation=this.generation;
    this.update({audioState:"reconnecting",detail:"Network unavailable; waiting to restore audio."});
    const permanent=(error:unknown)=>{const f=mediaFailure(error);return f.denied||f.code==="media_session_changed"||f.code==="call_ended"||f.code==="call_not_attachable"||["NotAllowedError","NotFoundError"].includes((error as Error)?.name);};
    const episode=this.uncertainAttachment?.diagnostics??mediaRecoveryContext("automatic_retry");
    let attempt:MediaRecoveryContext=episode;
    this.automaticRecovery=new AudioRecoveryLoop(async signal=>{
      this.assertCurrent(generation);
      const call=await boundedMediaRequest(s=>this.client.getCall(id,s),signal);
      if(!call)throw Object.assign(new Error("Call unavailable"),{status:404});
      if(isTerminalCall(call.status)){this.observeCall(call);return;}
      this.assertCurrent(generation);
      const events=await this.prepareAudio(generation,false);if(signal.aborted)throw new DOMException("Cancelled","AbortError");
      const session=await this.acquireRecoverySession(this.session!,attempt,signal);
      this.assertCurrent(generation);if(signal.aborted)throw new DOMException("Cancelled","AbortError");
      const cancel=()=>{if(this.current(generation)&&this.session===session)this.stopAudio();};
      signal.addEventListener("abort",cancel,{once:true});
      try{await this.attachAudio(session,generation,events);if(signal.aborted)throw new DOMException("Cancelled","AbortError");}
      finally{signal.removeEventListener("abort",cancel);}
      this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome:"connected",...attempt});
      this.automaticRecovery?.stop();this.automaticRecovery=undefined;
    },permanent,error=>{if(this.current(generation)){this.automaticRecovery=undefined;this.update({audioState:"error",detail:message(error)});}},outcome=>{if(outcome==="attempt_started")attempt={...episode,attempt_id:mediaDiagnosticID("attempt")};this.reportSessionEvent({timestamp:new Date().toISOString(),action:"network_recovery",outcome,...attempt});});
  }

  private async reconcileAttachedCall(session: CallSession, generation: number) {
    try {
      const call = await this.client.getCall(session.call_id);
      if (this.current(generation) && this.session === session && call) this.observeCall(call);
    } catch { /* pushed call.status frames or polling catch up */ }
  }

  private async reconcileFailedAudio(session: CallSession, generation: number) {
    // Carrier answer failure can roll the server claim back to pending. A
    // fresh read is essential: host list polls may predate the answer request.
    try {
      const call = await this.client.getCall(session.call_id);
      if (!this.current(generation) || this.session !== session || !call) return;
      if (call.status === "pending") {
        this.invalidate();
        this.clearCall("pending");
        this.update({ busy: false, detail: "The call was not connected. Answer again to retry." });
      } else this.observeCall(call);
    } catch { /* normal monitoring or explicit recovery can retry */ }
  }

  private startLease(session: CallSession) {
    this.lease?.stop();
    if (!session.lease_seconds) return;
    this.lease = new MediaLease(session.lease_seconds, () => this.client.renew(session), (reason, error) => {
      if (this.disposed || this.session !== session) return;
      this.stopAudio();
      this.update({ audioState: "error", detail: reason === "expired" ? "Audio session expired. Reconnecting audio." : `Audio authorization ended: ${message(error ?? reason)}` });
      if(reason==="expired")this.scheduleAudioRecovery();else{this.automaticRecovery?.stop();this.automaticRecovery=undefined;}
    }, event => {
      if (this.audio?.recordSessionEvent) this.audio.recordSessionEvent(event);
      else { try { this.options.onSessionEvent?.(event); } catch { /* host isolation */ } }
    }, session.lease_started_ms);
  }

  private reportSessionEvent(event:MediaSessionEvent) {
    event=safeMediaSessionEvent(event);
    if(this.audio?.recordSessionEvent) {try{this.audio.recordSessionEvent(event);}catch{/* observer isolation */}}
    else {try{this.options.onSessionEvent?.(event);}catch{/* observer isolation */}}
  }
  private stopAudio() {
    this.lease?.stop(); this.lease = undefined;
    const audio = this.audio;
    this.audio = undefined;
    if (this.ringing) {
      this.ringing = false;
      try { audio?.stopRingback?.(); } catch { /* adapters must not block call cleanup */ }
    }
    try { audio?.stop(); } catch { /* adapters must not block call cleanup */ }
    if (audio) { try { this.options.onLevels?.(0, 0); } catch { /* host meters cannot interrupt cleanup */ } }
  }
  private stopPolling() {
    this.scheduleStatusPoll=undefined;
    clearTimeout(this.timer);
    this.timer = undefined;
    this.polling?.abort();
    this.polling = undefined;
  }
  private startPolling() {
    this.stopPolling();
    if (!this.interval) return;
    const controller = new AbortController();
    this.polling = controller;
    let running=false;
    let lastRead=performance.now();
    const delay=()=>this.hasStatusPush && this.snapshot.audioState==="live" ? Math.max(1,Math.max(this.interval,30000)-(performance.now()-lastRead)) : this.interval;
    const poll = async () => {
      if(running) return;
      const id = this.session?.call_id;
      if (!id || controller.signal.aborted) return;
      running=true;
      try {
        const call = await this.client.getCall(id, controller.signal);
        lastRead=performance.now();
        if (!controller.signal.aborted && call) this.observeCall(call);
      } catch (error) {
        if (!controller.signal.aborted) {
          this.hasStatusPush=false;
          this.update({ detail: `Call status unavailable: ${message(error)}` });
        }
      } finally {
        running=false;
        this.scheduleStatusPoll?.();
      }
    };
    this.scheduleStatusPoll=()=>{
      if(running||controller.signal.aborted||!this.session||this.disposed)return;
      clearTimeout(this.timer);this.timer=setTimeout(poll,delay());
    };
    this.scheduleStatusPoll();
  }
  private clearCall(carrierStatus?: string) {
    this.hasStatusPush=false;
    this.stopAudio();
    this.stopPolling();
    this.session = undefined;this.hasLiveAudio=false;this.scheduleNetworkCheck();
    this.outbound = false;
    this.update({ callId: undefined, carrierStatus, audioState: "idle", muted: false, detail: undefined, phase: carrierStatus ? "ended" : "idle",
      holdState: undefined, recordingState: undefined, controlError: undefined, capabilities: undefined });
  }

  /** Releases local devices and monitoring. An established carrier call stays up. */
  dispose(): void {
 this.cancelNetworkCheck();globalThis.removeEventListener?.("online",this.networkChanged);
    if (this.disposed) return;
    this.disposed = true;
    this.invalidate();
    this.listeners.clear();
    this.clearCall();
    this.update({ busy: false });
  }
}
