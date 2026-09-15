import { DEFAULT_SOFTPHONE_AUDIO_OPTIONS, playbackBufferOptions, type SoftphoneAudioOptions, type SoftphoneCallStatus, type SoftphoneDiagnostics, type SoftphoneState } from "../../ui/softphone-audio";
import { createBrowserAudio, type AudioConnection, type AudioRuntime } from "./audio";
import { isTerminalCall, type AnswerRequest, type Call, type CallSession, type CallTermination, type DialRequest, type TelephonyClient } from "./client";

/** Coarse call progress derived from carrier status: idle, placing, ringing, connected, ended. */
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
}
export interface SoftphoneOptions {
  audio?: Partial<SoftphoneAudioOptions>;
  /** 0 disables automatic status reconciliation; the host then calls observeCall. */
  pollIntervalMs?: number;
  onLevels?: (microphone: number, speaker: number) => void;
  onDiagnostics?: (value: SoftphoneDiagnostics) => void;
  onNotice?: (detail: string) => void;
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
  private intent?: { value: string; request: DialRequest };
  private leaseTimer?: ReturnType<typeof setTimeout>;
  private leaseGeneration = 0;
  private timer?: ReturnType<typeof setTimeout>;
  private polling?: AbortController;
  private outbound = false;
  private ringing = false;
  private readonly runtime: AudioRuntime;
  private audioOptions: SoftphoneAudioOptions;
  private readonly interval: number;

  constructor(readonly client: TelephonyClient, private readonly options: SoftphoneOptions = {}) {
    this.runtime = options.audioRuntime ?? createBrowserAudio(client.app);
    this.audioOptions = { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS, ...options.audio };
    playbackBufferOptions(this.audioOptions);
    this.interval = options.pollIntervalMs ?? 2000;
    if (!Number.isFinite(this.interval) || (this.interval !== 0 && this.interval < 100)) throw new Error("Call poll interval must be 0 or at least 100 ms");
  }

  getSnapshot = (): SoftphoneSnapshot => this.snapshot;
  subscribe = (listener: (state: SoftphoneSnapshot) => void): (() => void) => {
    this.assertOpen();
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };

  private update(patch: Partial<SoftphoneSnapshot>) {
    this.snapshot = Object.freeze({ ...this.snapshot, ...patch });
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
    this.cancellation.abort();
    this.cancellation = new AbortController();
    return ++this.generation;
  }
  private current(generation: number) { return !this.disposed && generation === this.generation; }
  private finish(generation: number) { if (this.current(generation)) this.update({ busy: false }); }
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
    this.assertOpen();
    if (this.snapshot.busy || this.hangingUp || (requireIdle && this.session)) throw new Error("Softphone already has an active operation or call");
    const generation = this.invalidate();
    this.update({ busy: true, detail: undefined });
    return generation;
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
      await this.cancellable(this.runtime.preflight(this.audioOptions), generation);
      this.assertCurrent(generation);
      // Do not abort a write whose carrier outcome would then be unknown.
      placed = await this.client.place(this.intent.request);
      this.intent = undefined;
      this.assertCurrent(generation);
      attached = true;
      this.outbound = true;
      await this.attachAudio(placed, generation);
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
      claimed = await this.client.answer(id, request);
      this.assertCurrent(generation);
      attached = true;
      this.outbound = false;
      await this.attachAudio(claimed, generation);
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
  async attach(id: string): Promise<void> { await this.acquireSession(id, false); }

  /** Explicit supervisor action; normal attach/join cannot displace another user. */
  async takeover(id: string): Promise<void> { await this.acquireSession(id, true); }

  private async acquireSession(id: string, takeover: boolean): Promise<void> {
    const generation = this.begin(true);
    try {
      await this.cancellable(this.runtime.preflight(this.audioOptions), generation);
      this.assertCurrent(generation);
      const session = await (takeover ? this.client.takeover(id) : this.client.attach(id));
      this.assertCurrent(generation);
      this.outbound = false;
      await this.attachAudio(session, generation);
    } catch (error) {
      if (this.current(generation)) this.update({ detail: message(error) });
      throw error;
    } finally { this.finish(generation); }
  }

  async reconnect(audio?: Partial<SoftphoneAudioOptions>): Promise<void> {
    if (!this.session) throw new Error("No call to reconnect");
    const nextOptions = { ...this.audioOptions, ...audio };
    playbackBufferOptions(nextOptions);
    const generation = this.begin(false);
    this.audioOptions = nextOptions;
    try { await this.attachAudio(this.session, generation); }
    catch (error) { if (this.current(generation)) this.update({ detail: message(error) }); throw error; }
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

  /** Device/processing changes apply on the next dial, answer, or reconnect. */
  configureAudio(options: Partial<SoftphoneAudioOptions>): void {
    this.assertOpen();
    const nextOptions = { ...this.audioOptions, ...options };
    playbackBufferOptions(nextOptions);
    this.audioOptions = nextOptions;
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
  observeCall(call: Pick<Call, "id" | "status"> & Partial<Pick<Call, "answered_at" | "ended_at" | "answered_by" | "termination">>): void {
    if (this.disposed || call.id !== this.session?.call_id) return;
    if (isTerminalCall(call.status)) {
      this.invalidate();
      this.clearCall(call.status);
      this.update({ busy: false, phase: "ended", termination: call.termination, answeredBy: call.answered_by ?? this.snapshot.answeredBy, endedAt: call.ended_at });
      return;
    }
    this.update({ carrierStatus: call.status, phase: phaseForStatus(call.status), answeredBy: call.answered_by ?? this.snapshot.answeredBy });
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

  private async attachAudio(session: CallSession, generation: number) {
    this.assertCurrent(generation);
    this.stopAudio();
    this.session = session;
    this.update({ callId: session.call_id, carrierStatus: undefined, audioState: "connecting", phase: "placing", termination: undefined, answeredBy: undefined, endedAt: undefined });
    let audio: AudioConnection | undefined;
    const current = () => !this.disposed && audio !== undefined && this.audio === audio;
    const notify = (callback: () => void) => { if (current()) { try { callback(); } catch { /* isolate host callbacks */ } } };
    try {
      this.assertCurrent(generation);
      audio = this.runtime.create({
        onState: (audioState, detail) => {
          if (!current()) return;
          if (audioState === "ended" && detail === "call.ended") {
            this.observeCall({ id: session.call_id, status: "completed" });
            return;
          }
          this.update({ audioState, detail });
          if (current() && (audioState === "error" || audioState === "ended")) {
            this.stopAudio();
            if (audioState === "error") void this.reconcileFailedAudio(session, generation);
          }
        },
        onLevels: (mic, speaker) => notify(() => this.options.onLevels?.(mic, speaker)),
        onDiagnostics: diagnostics => notify(() => this.options.onDiagnostics?.(diagnostics)),
        onNotice: detail => notify(() => this.options.onNotice?.(detail)),
        onCallStatus: status => notify(() => this.observeCall({
          id: status.call_id, status: status.status, answered_at: status.answered_at, ended_at: status.ended_at,
          answered_by: status.answered_by, termination: status.termination,
        })),
      });
      this.audio = audio;
      this.assertCurrent(generation);
      this.startPolling();
      this.startLease(session);
      audio.setMuted(this.snapshot.muted);
      await this.cancellable(audio.start(this.client.mediaURL(session), this.audioOptions), generation);
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
    const leaseGeneration = ++this.leaseGeneration;
    if (!session.lease_seconds) return;
    const renew = async () => {
      if (this.disposed || this.session !== session || leaseGeneration !== this.leaseGeneration) return;
      try {
        await this.client.renew(session);
        if (leaseGeneration === this.leaseGeneration) this.leaseTimer = setTimeout(renew, session.lease_seconds! * 1000 / 3);
      } catch (error) {
        if (leaseGeneration !== this.leaseGeneration) return;
        this.stopAudio();
        this.update({ audioState: "error", detail: `Audio authorization ended: ${message(error)}` });
      }
    };
    this.leaseTimer = setTimeout(renew, session.lease_seconds * 1000 / 3);
  }

  private stopAudio() {
    ++this.leaseGeneration;
    clearTimeout(this.leaseTimer);
    this.leaseTimer = undefined;
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
    const poll = async () => {
      const id = this.session?.call_id;
      if (!id || controller.signal.aborted) return;
      try {
        const call = await this.client.getCall(id, controller.signal);
        if (!controller.signal.aborted && call) this.observeCall(call);
      } catch (error) {
        if (!controller.signal.aborted) this.update({ detail: `Call status unavailable: ${message(error)}` });
      } finally {
        if (!controller.signal.aborted && this.session && !this.disposed) this.timer = setTimeout(poll, this.interval);
      }
    };
    this.timer = setTimeout(poll, this.interval);
  }
  private clearCall(carrierStatus?: string) {
    this.stopAudio();
    this.stopPolling();
    this.session = undefined;
    this.outbound = false;
    this.update({ callId: undefined, carrierStatus, audioState: "idle", muted: false, detail: undefined, phase: carrierStatus ? "ended" : "idle" });
  }

  /** Releases local devices and monitoring. An established carrier call stays up. */
  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.invalidate();
    this.listeners.clear();
    this.clearCall();
    this.update({ busy: false });
  }
}
