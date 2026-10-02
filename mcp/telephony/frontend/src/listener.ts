import type { CallSession, TelephonyClient } from "./client";
import { createListenerAudio, type ListenerAudioConnection, type ListenerAudioRuntime, type ListenerDiagnostics, type ListenerPlaybackOptions } from "./listener-audio";

export type ListenerState = "idle" | "connecting" | "listening" | "reconnecting" | "disconnected" | "access_revoked" | "call_ended";
export interface ListenerSnapshot { readonly state: ListenerState; readonly callId?: string; readonly detail?: string; readonly coaching?: boolean; readonly talking?: boolean }
export interface CallListenerOptions extends ListenerPlaybackOptions {
  runtime?: ListenerAudioRuntime;
  onDiagnostics?: (value: ListenerDiagnostics) => void;
  /** Automatically retry transient disconnections for at most 30 seconds. Default true. */
  reconnect?: boolean;
}

/** Independent listen/coach session: microphone capture requires explicit push-to-talk. */
export class HeadlessCallListener {
  private snapshot: ListenerSnapshot = Object.freeze({ state: "idle" });
  private observers = new Set<(snapshot: ListenerSnapshot) => void>();
  private generation = 0;
  private attempt = 0;
  private session?: CallSession;
  private audio?: ListenerAudioConnection;
  private lease?: ReturnType<typeof setInterval>;
  private retry?: ReturnType<typeof setTimeout>;
  private retryUntil = 0;
  private retryDelay = 500;
  private disposed = false;
  private coaching = false;
  private readonly runtime: ListenerAudioRuntime;
  constructor(private readonly client: TelephonyClient, private readonly options: CallListenerOptions = {}) {
    this.runtime = options.runtime ?? createListenerAudio(client.app, options);
  }
  getSnapshot = () => this.snapshot;
  subscribe = (observer: (snapshot: ListenerSnapshot) => void) => { this.observers.add(observer); return () => { this.observers.delete(observer); }; };
  private update(patch: Partial<ListenerSnapshot>) { this.snapshot = Object.freeze({ ...this.snapshot, ...patch }); for (const observer of this.observers) { try { observer(this.snapshot); } catch { /* host isolation */ } } }
  async listen(callId: string): Promise<void> { return this.begin(callId,false); }
  async coach(callId: string): Promise<void> { return this.begin(callId,true); }
  private async begin(callId: string, coaching: boolean): Promise<void> {
    if (this.disposed) throw new Error("Listener disposed");
    await this.stop();
    this.coaching=coaching;
    this.retryUntil = 0; this.retryDelay = 500;
    const generation = ++this.generation;
    this.update({ state: "connecting", callId, detail: undefined, coaching, talking:false });
    return this.connect(callId, generation);
  }
  private async connect(callId: string, generation: number): Promise<void> {
    let session: CallSession | undefined;
    const attempt = ++this.attempt;
    const current = () => generation === this.generation && attempt === this.attempt && !this.disposed;
    try {
      session = this.coaching ? await this.client.coachSession(callId) : await this.client.listenSession(callId);
      if (!current()) { await this.client.stopListening(session).catch(() => {}); return; }
      this.session = session;
      const audio = this.runtime.create({
        onReady: () => { if (current()) { this.retryUntil = 0; this.retryDelay = 500; this.update({ state: "listening", detail: undefined }); } },
        onClose: reason => { if (current()) this.disconnected(reason, callId, generation); },
        onTalking: (talking,detail) => { if(current()) this.update({talking,detail}); },
        onDiagnostics: value => { if (current()) { try { this.options.onDiagnostics?.(value); } catch { /* host isolation */ } } },
      });
      this.audio = audio;
      let renewing = false;
      this.lease = setInterval(() => {
        if (!current() || renewing || this.session !== session) return;
        renewing = true;
        void this.client.renewListening(session!).catch(() => { if (current()) this.disconnected("access_revoked", callId, generation); }).finally(() => { renewing = false; });
      }, 20_000);
      await audio.start(this.client.listenerMediaURL(session),{coaching:session.coaching===true});
      if (!current()) audio.stop();
    } catch (error) {
      if (!current() || (session && this.session !== session)) return;
      const status = (error as { status?: number })?.status;
      let code: string | undefined;
      try { code = JSON.parse((error as { body?: string }).body ?? "{}").code; } catch { /* non-JSON denial */ }
      const reason = code === "call_ended" ? "call_ended" : status === 401 || status === 403 || status === 404 ? "access_revoked" : "listener_disconnected";
      // A denied reconnect must not continue retrying the same credential.
      this.disconnected(reason, callId, generation, String(error));
      throw error;
    }
  }
  private cleanup(): Promise<void> {
    if (this.lease) clearInterval(this.lease); this.lease = undefined;
    const audio = this.audio; this.audio = undefined; audio?.stop();
    const session = this.session; this.session = undefined;
    return session ? this.client.stopListening(session).catch(() => {}) : Promise.resolve();
  }
  private disconnected(reason: string, callId: string, generation: number, detail = reason) {
    if (generation !== this.generation || this.disposed) return;
    void this.cleanup();
    if (this.retry) return;
    const ended = reason === "call_ended", revoked = reason === "access_revoked";
    const transient = ["listener_disconnected", "listener_network_error", "media_disconnected", "media_replaced"].includes(reason);
    if (!this.coaching && !ended && !revoked && transient && this.options.reconnect !== false) {
      this.retryUntil ||= Date.now() + 30_000;
      if (Date.now() < this.retryUntil) {
        this.update({ state: "reconnecting", detail, talking:false });
        this.retry = setTimeout(() => { this.retry = undefined; if (generation === this.generation && !this.disposed) void this.connect(callId, generation).catch(() => {}); }, this.retryDelay);
        this.retryDelay = Math.min(4_000, this.retryDelay * 2); return;
      }
    }
    this.update({ state: ended ? "call_ended" : revoked ? "access_revoked" : "disconnected", detail, talking:false });
  }
  async startTalking(): Promise<void> {
    if(this.snapshot.state!=="listening" || !this.session?.coaching || !this.audio?.startTalking) throw new Error("Join private coaching before talking");
    await this.audio.startTalking();
  }
  stopTalking(): void { this.audio?.stopTalking?.(); this.update({talking:false}); }
  setOutputVolume(value: number) { this.audio?.setOutputVolume(value); }
  async stop(): Promise<void> {
    ++this.generation;
    if (this.retry) clearTimeout(this.retry); this.retry = undefined;
    const cleanup = this.cleanup();
    this.update({ state: "idle", callId: undefined, detail: undefined, coaching:false, talking:false });
    await cleanup;
  }
  async dispose(): Promise<void> { await this.stop(); this.disposed = true; this.observers.clear(); }
}
