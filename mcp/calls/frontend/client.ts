export interface CallsCapabilities {
  audio?: boolean;
  video?: boolean;
  screen?: boolean;
  chat?: boolean;
  room_control?: boolean;
  transcript_read?: boolean;
  transcript_write?: boolean;
}

export interface CallsParticipant {
  id: number;
  room_id: number;
  display_name?: string;
  kind: "human" | "agent" | "service";
  role: string;
  capabilities?: CallsCapabilities | string;
  muted_audio?: boolean;
  muted_video?: boolean;
}

export interface CallsMessage {
  id: number;
  participant_id?: number;
  speaker_name?: string;
  body: string;
  created_at: string;
}

export interface CallsAdmission {
  participant: CallsParticipant;
  participant_token: string;
  rtc_config: { ice_servers: RTCIceServer[]; heartbeat_interval_seconds?: number };
}

export interface CallsRemoteStream {
  participant: CallsParticipant;
  stream: MediaStream;
}

export interface CallsSnapshot {
  phase: "idle" | "joining" | "joined" | "ended";
  participant?: CallsParticipant;
  capabilities: CallsCapabilities;
  participants: readonly CallsParticipant[];
  remoteStreams: readonly CallsRemoteStream[];
  messages: readonly CallsMessage[];
  localStream?: MediaStream;
  screenStream?: MediaStream;
  microphone: boolean;
  camera: boolean;
  error?: string;
}

export interface CallsSessionOptions {
  /** App base, such as /api/apps/calls. A same-origin proxy can use another prefix. */
  apiBaseURL: string | URL;
  projectId?: string;
  fetch?: typeof fetch;
}

type MediaKind = "audio" | "video" | "screen";
type Signal = { id: number; from_participant_id: number } & (
  | { kind: "offer" | "answer"; payload: RTCSessionDescriptionInit }
  | { kind: "ice"; payload: RTCIceCandidateInit }
);
type Peer = {
  pc: RTCPeerConnection;
  participant: CallsParticipant;
  streams: Map<string, MediaStream>;
  candidates: RTCIceCandidateInit[];
  makingOffer: boolean;
  ignoringOffer: boolean;
  settingAnswer: boolean;
};

class CallsHTTPError extends Error {
  constructor(readonly status: number, message: string) { super(message); }
}

export function parseCallsInvite(invite: string | URL, base = globalThis.location?.href): { token: string; apiBaseURL: URL } {
  const url = new URL(invite, base);
  const match = /^(.*)\/join\/([^/]+)\/?$/.exec(url.pathname);
  if (!match || !["http:", "https:"].includes(url.protocol) || url.username || url.password) {
    throw new Error("Invalid Calls invite link");
  }
  url.pathname = match[1];
  url.hash = "";
  return { token: decodeURIComponent(match[2]), apiBaseURL: url };
}

function emptySnapshot(phase: CallsSnapshot["phase"] = "idle"): CallsSnapshot {
  return { phase, capabilities: {}, participants: [], messages: [], remoteStreams: [], microphone: false, camera: false };
}

function capabilities(raw: CallsParticipant["capabilities"]): CallsCapabilities {
  return typeof raw === "string" ? JSON.parse(raw) : raw ?? {};
}

export class CallsSession {
  private readonly base: URL;
  private readonly fetcher: typeof fetch;
  private snapshot = emptySnapshot();
  private readonly listeners = new Set<(snapshot: CallsSnapshot) => void>();
  private active?: { admission: CallsAdmission; abort: AbortController };
  private readonly peers = new Map<number, Peer>();
  private readonly media = new Map<MediaKind, MediaStream>();
  private local?: MediaStream;
  private readonly pendingMedia = new Set<MediaKind>();
  private readonly timers = new Set<ReturnType<typeof setTimeout>>();
  private joining = false;
  private disposed = false;
  private lastSignal = 0;
  private lastMessage = 0;
  private generation = 0;

  constructor(options: CallsSessionOptions) {
    this.base = new URL(options.apiBaseURL, globalThis.location?.href);
    if (!["http:", "https:"].includes(this.base.protocol) || this.base.username || this.base.password) {
      throw new Error("Invalid Calls API base URL");
    }
    if (options.projectId) this.base.searchParams.set("project_id", options.projectId);
    this.fetcher = options.fetch ?? globalThis.fetch.bind(globalThis);
  }

  getSnapshot(): CallsSnapshot { return this.snapshot; }

  subscribe(listener: (snapshot: CallsSnapshot) => void): () => void {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  }

  async join(token: string, displayName?: string): Promise<void> {
    if (this.disposed || this.active || this.joining) throw new Error("Calls session is already in use");
    this.joining = true;
    const generation = this.generation;
    this.update({ ...emptySnapshot("joining"), error: undefined });
    try {
      const admission = await this.request<CallsAdmission>("/api/join", { token, display_name: displayName });
      if (this.generation !== generation) {
        await this.release(admission);
        return;
      }
      this.activate(admission);
    } catch (error) {
      if (this.generation === generation) this.update({ phase: "idle", error: String((error as Error).message ?? error) });
      throw error;
    } finally {
      this.joining = false;
    }
  }

  /** Reattach to an existing participant. A reload does not consume the invite again. */
  async resume(admission: CallsAdmission): Promise<void> {
    if (this.disposed || this.active || this.joining) throw new Error("Calls session is already in use");
    this.joining = true;
    const generation = this.generation;
    try {
      await this.roomRequest<{ participants: CallsParticipant[] }>("participants", undefined, admission);
      if (this.generation === generation) this.activate(admission);
      else await this.release(admission);
    } finally {
      this.joining = false;
    }
  }

  /** Intended for the host's per-tab session storage. Treat the token as a secret. */
  getAdmission(): CallsAdmission | undefined { return this.active?.admission; }

  async setMicrophone(enabled: boolean): Promise<void> { await this.setMedia("audio", enabled); }
  async setCamera(enabled: boolean): Promise<void> { await this.setMedia("video", enabled); }
  async setScreenShare(enabled: boolean): Promise<void> { await this.setMedia("screen", enabled); }

  async sendMessage(body: string): Promise<void> {
    const active = this.active;
    const { message } = await this.roomRequest<{ message: CallsMessage }>("messages", { body });
    if (active === this.active) this.mergeMessages([message]);
  }

  async end(): Promise<void> {
    const active = this.active;
    await this.roomRequest("end", {});
    if (active === this.active) this.reset("ended");
  }

  async leave(): Promise<void> {
    const admission = this.active?.admission;
    this.reset();
    if (admission) await this.release(admission);
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.listeners.clear();
    void this.leave();
  }

  private update(patch: Partial<CallsSnapshot>): void {
    this.snapshot = { ...this.snapshot, ...patch };
    for (const listener of this.listeners) listener(this.snapshot);
  }

  private async request<T>(path: string, body?: object, token?: string, signal?: AbortSignal, keepalive = false): Promise<T> {
    const url = new URL(this.base);
    const [pathname, query] = path.split("?", 2);
    url.pathname = url.pathname.replace(/\/$/, "") + pathname;
    new URLSearchParams(query).forEach((value, key) => url.searchParams.set(key, value));
    const headers = new Headers();
    if (token) headers.set("Authorization", "Bearer " + token);
    if (body) headers.set("Content-Type", "application/json");
    const response = await this.fetcher(url, {
      method: body ? "POST" : "GET", body: body && JSON.stringify(body), headers,
      credentials: "omit", redirect: "error", cache: "no-store", signal, keepalive,
    });
    const data = await response.json().catch(() => undefined);
    if (!response.ok) throw new CallsHTTPError(response.status, data?.error ?? `Calls request failed (${response.status})`);
    return data as T;
  }

  private roomRequest<T>(path: string, body?: object, admission = this.active?.admission, keepalive = false): Promise<T> {
    if (!admission) return Promise.reject(new Error("Join the room first"));
    const signal = !keepalive && this.active?.admission === admission ? this.active.abort.signal : undefined;
    return this.request<T>("/api/rooms/" + admission.participant.room_id + "/" + path, body, admission.participant_token, signal, keepalive);
  }

  private async release(admission: CallsAdmission): Promise<void> {
    await this.roomRequest("leave", {}, admission, true).catch(() => {});
  }

  private activate(admission: CallsAdmission): void {
    if (!admission.participant_token || !admission.participant?.room_id) throw new Error("Invalid Calls admission");
    const allowed = capabilities(admission.participant.capabilities);
    const active = { admission, abort: new AbortController() };
    this.active = active;
    this.lastSignal = 0;
    this.lastMessage = 0;
    this.update({ ...emptySnapshot("joined"), error: undefined, participant: admission.participant, capabilities: allowed });
    this.poll(() => this.loadParticipants(), 3000, active);
    if (allowed.audio || allowed.video || allowed.screen) this.poll(() => this.loadSignals(), 1000, active);
    if (allowed.chat) this.poll(() => this.loadMessages(), 2500, active);
    this.poll(() => this.heartbeat(), (admission.rtc_config.heartbeat_interval_seconds || 15) * 1000, active);
  }

  private poll(run: () => Promise<void>, interval: number, active: NonNullable<CallsSession["active"]>): void {
    const tick = async () => {
      if (this.active !== active) return;
      try {
        await run();
      } catch (error) {
        if (this.active !== active) return;
        if (error instanceof CallsHTTPError && error.status === 401) {
          this.reset("ended");
          return;
        }
        this.update({ error: (error as Error).message });
      }
      if (this.active !== active) return;
      const timer = setTimeout(() => { this.timers.delete(timer); void tick(); }, interval);
      this.timers.add(timer);
    };
    void tick();
  }

  private reset(phase: CallsSnapshot["phase"] = "idle"): void {
    this.generation++;
    this.active?.abort.abort();
    this.active = undefined;
    for (const timer of this.timers) clearTimeout(timer);
    this.timers.clear();
    for (const { pc } of this.peers.values()) pc.close();
    this.peers.clear();
    for (const stream of this.media.values()) for (const track of stream.getTracks()) track.stop();
    this.media.clear();
    this.local = undefined;
    this.pendingMedia.clear();
    this.snapshot = emptySnapshot(phase);
    this.update({});
  }

  private async setMedia(kind: MediaKind, enabled: boolean): Promise<void> {
    const active = this.active;
    if (!active) throw new Error("Join the room first");
    if (!this.snapshot.capabilities[kind]) throw new Error("Media permission denied");
    if (this.pendingMedia.has(kind)) throw new Error("Media permission is already pending");
    const current = this.media.get(kind);
    if (current && kind !== "screen") {
      for (const track of current.getTracks()) track.enabled = enabled;
    } else if (current && !enabled) {
      this.removeMedia(kind, current);
      return;
    } else if (!current && enabled) {
      this.pendingMedia.add(kind);
      try {
        const stream = kind === "screen"
          ? await navigator.mediaDevices.getDisplayMedia({ video: true, audio: false })
          : await navigator.mediaDevices.getUserMedia({ audio: kind === "audio", video: kind === "video" });
        if (this.active !== active) {
          for (const track of stream.getTracks()) track.stop();
          return;
        }
        this.media.set(kind, stream);
        const outbound = kind === "screen" ? stream : (this.local ??= new MediaStream());
        for (const track of stream.getTracks()) {
          if (kind !== "screen") outbound.addTrack(track);
          track.addEventListener("ended", () => this.removeMedia(kind, stream), { once: true });
          for (const { pc } of this.peers.values()) pc.addTrack(track, outbound);
        }
      } finally {
        if (this.active === active) this.pendingMedia.delete(kind);
      }
    }
    this.publishMedia();
  }

  private removeMedia(kind: MediaKind, stream: MediaStream): void {
    if (this.media.get(kind) !== stream) return;
    this.media.delete(kind);
    for (const track of stream.getTracks()) {
      for (const { pc } of this.peers.values()) {
        const sender = pc.getSenders().find(sender => sender.track === track);
        if (sender) pc.removeTrack(sender);
      }
      if (kind !== "screen") this.local?.removeTrack(track);
      track.stop();
    }
    if (this.local && !this.local.getTracks().length) this.local = undefined;
    this.publishMedia();
  }

  private publishMedia(): void {
    this.update({
      localStream: this.local,
      screenStream: this.media.get("screen"),
      camera: this.media.get("video")?.getVideoTracks().some(track => track.enabled) ?? false,
      microphone: this.media.get("audio")?.getAudioTracks().some(track => track.enabled) ?? false,
    });
  }

  private signal(to: number, kind: Signal["kind"], payload: object): Promise<unknown> {
    return this.roomRequest("signal/" + kind, { to_participant_id: to, payload });
  }

  private ensurePeer(participant: CallsParticipant): Peer {
    const existing = this.peers.get(participant.id);
    if (existing) {
      existing.participant = participant;
      return existing;
    }
    const pc = new RTCPeerConnection({ iceServers: this.active?.admission.rtc_config.ice_servers });
    const peer: Peer = {
      pc, participant, streams: new Map(), candidates: [], makingOffer: false, ignoringOffer: false, settingAnswer: false,
    };
    this.peers.set(participant.id, peer);
    const current = () => this.peers.get(participant.id) === peer;
    const report = (error: Error) => { if (current()) this.update({ error: error.message }); };
    pc.onicecandidate = ({ candidate }) => {
      if (candidate && current()) void this.signal(participant.id, "ice", candidate.toJSON()).catch(report);
    };
    pc.onnegotiationneeded = async () => {
      try {
        peer.makingOffer = true;
        await pc.setLocalDescription();
        if (current() && pc.localDescription) await this.signal(participant.id, "offer", pc.localDescription.toJSON());
      } catch (error) { report(error as Error); }
      finally { peer.makingOffer = false; }
    };
    pc.ontrack = ({ track, streams }) => {
      if (!current()) return;
      const stream = streams[0] ?? new MediaStream([track]);
      peer.streams.set(stream.id, stream);
      this.publishRemoteStreams();
      track.addEventListener("ended", () => {
        if (stream.getTracks().every(item => item.readyState === "ended")) peer.streams.delete(stream.id);
        if (current()) this.publishRemoteStreams();
      }, { once: true });
      stream.addEventListener("removetrack", () => {
        if (!stream.getTracks().length) peer.streams.delete(stream.id);
        if (current()) this.publishRemoteStreams();
      });
    };
    pc.onconnectionstatechange = () => {
      if (current() && ["failed", "closed"].includes(pc.connectionState)) this.closePeer(participant.id);
    };
    for (const stream of [this.local, this.media.get("screen")]) {
      if (stream) for (const track of stream.getTracks()) pc.addTrack(track, stream);
    }
    return peer;
  }

  private closePeer(id: number): void {
    const peer = this.peers.get(id);
    this.peers.delete(id);
    peer?.pc.close();
    this.publishRemoteStreams();
  }

  private publishRemoteStreams(): void {
    this.update({ remoteStreams: [...this.peers.values()].flatMap(peer =>
      [...peer.streams.values()].map(stream => ({ participant: peer.participant, stream }))) });
  }

  private async loadParticipants(): Promise<void> {
    const active = this.active;
    const { participants } = await this.roomRequest<{ participants: CallsParticipant[] }>("participants");
    if (!active || this.active !== active) return;
    const ids = new Set(participants.map(participant => participant.id));
    for (const id of this.peers.keys()) if (!ids.has(id)) this.closePeer(id);
    const allowed = this.snapshot.capabilities;
    if (allowed.audio || allowed.video || allowed.screen) {
      for (const participant of participants) {
        if (participant.id !== active.admission.participant.id) this.ensurePeer(participant);
      }
    }
    this.update({ participants });
  }

  private async loadSignals(): Promise<void> {
    const active = this.active;
    const { signals } = await this.roomRequest<{ signals: Signal[] }>("signal?since_id=" + this.lastSignal);
    if (!active || this.active !== active) return;
    for (const signal of signals) {
      if (this.active !== active) return;
      this.lastSignal = signal.id;
      const peer = this.peers.get(signal.from_participant_id);
      if (!peer) {
        await this.loadParticipants();
        if (this.active !== active) return;
      }
      const sender = this.peers.get(signal.from_participant_id);
      if (!sender) continue;
      try { await this.handleSignal(sender, signal, active.admission.participant.id); }
      catch (error) {
        if (this.active === active) this.update({ error: (error as Error).message });
      }
    }
  }

  private async handleSignal(peer: Peer, signal: Signal, self: number): Promise<void> {
    const { pc } = peer;
    if (signal.kind === "ice") {
      if (peer.ignoringOffer) return;
      if (pc.remoteDescription) await pc.addIceCandidate(signal.payload);
      else peer.candidates.push(signal.payload);
      return;
    }
    const collision = signal.kind === "offer" && (peer.makingOffer || (pc.signalingState !== "stable" && !peer.settingAnswer));
    peer.ignoringOffer = collision && self < signal.from_participant_id;
    if (peer.ignoringOffer) return;
    peer.settingAnswer = signal.kind === "answer";
    try { await pc.setRemoteDescription(signal.payload); }
    finally { peer.settingAnswer = false; }
    for (const candidate of peer.candidates.splice(0)) await pc.addIceCandidate(candidate);
    if (signal.kind === "offer") {
      await pc.setLocalDescription();
      if (this.peers.get(peer.participant.id) === peer && pc.localDescription) {
        await this.signal(peer.participant.id, "answer", pc.localDescription.toJSON());
      }
    }
  }

  private async heartbeat(): Promise<void> {
    const active = this.active;
    const states = [...this.peers.values()].map(({ pc }) => pc.connectionState);
    const connection_state = states.includes("connected") ? "connected" : states.includes("connecting") ? "connecting" : "new";
    const { room_status } = await this.roomRequest<{ room_status: string }>("heartbeat", {
      connection_state, muted_audio: !this.snapshot.microphone, muted_video: !this.snapshot.camera,
      tracks: [...this.media].flatMap(([kind, stream]) => stream.getTracks().map(track => ({
        id: track.id, kind, source: kind === "video" ? "camera" : kind === "audio" ? "microphone" : kind,
        label: track.label, enabled: track.enabled,
      }))),
    });
    if (this.active === active && room_status === "ended") this.reset("ended");
  }

  private async loadMessages(): Promise<void> {
    const active = this.active;
    const { messages } = await this.roomRequest<{ messages: CallsMessage[] }>("messages?since_id=" + this.lastMessage);
    if (this.active !== active || !messages.length) return;
    this.lastMessage = messages[messages.length - 1].id;
    this.mergeMessages(messages);
  }

  private mergeMessages(messages: CallsMessage[]): void {
    const all = new Map([...this.snapshot.messages, ...messages].map(message => [message.id, message]));
    this.update({ messages: [...all.values()].sort((a, b) => a.id - b.id) });
  }
}
