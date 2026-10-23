export type VoiceAudioState = "connecting" | "listening" | "speaking" | "closed";
interface AudioFrame { type: "audio.frame"; item_id: string; audio_end_ms: number; silence?: boolean }
interface Playback { frame: AudioFrame | null; start: number; end: number; socket: WebSocket; epoch: number; silent: boolean }

// App-owned PCM renderer. Provider transcripts never control audio delivery.
export class VoiceAudioClient {
  private media: MediaStream | null = null;
  private context: AudioContext | null = null;
  private source: MediaStreamAudioSourceNode | null = null;
  private capture: AudioWorkletNode | null = null;
  private socket: WebSocket | null = null;
  private muted = false;
  private closed = false;
  private epoch = 0;
  private rejectConnect: ((reason: Error) => void) | null = null;
  private playbackCursor = 0;
  private pendingFrame: AudioFrame | null = null;
  private playback = new Map<AudioBufferSourceNode, Playback>();
  private cancelled = new Set<string>();
  private reported = new Map<string, number>();
  private progressTimer: ReturnType<typeof setInterval> | null = null;
  constructor(private onState: (state: VoiceAudioState) => void, private onDisconnect: () => void) {}

  async prepare(workletURL: string) {
    if (!navigator.mediaDevices?.getUserMedia || !window.AudioWorkletNode) throw new Error("This browser cannot capture realtime audio.");
    this.media = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
    if (this.closed) { for (const track of this.media.getTracks()) track.stop(); this.media = null; throw new Error("Voice view closed before microphone was ready."); }
    try {
      this.context = new AudioContext({ latencyHint: "interactive" });
      if (this.context.state === "suspended") await this.context.resume();
      await this.context.audioWorklet.addModule(workletURL);
      if (this.closed) throw new Error("Voice view closed before capture was ready.");
      this.source = this.context.createMediaStreamSource(this.media);
      this.capture = new AudioWorkletNode(this.context, "conversations-pcm-capture", { numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1], channelCount: 1 });
      this.capture.port.onmessage = (event: MessageEvent<ArrayBuffer | { type: string }>) => {
        const socket = this.socket;
        if (socket?.readyState !== WebSocket.OPEN) return;
        if (event.data instanceof ArrayBuffer) socket.send(this.muted ? new ArrayBuffer(event.data.byteLength) : event.data);
        else if (!this.muted && (event.data.type === "input.speech_started" || event.data.type === "input.speech_stopped")) socket.send(JSON.stringify(event.data));
      };
      this.source.connect(this.capture); this.capture.connect(this.context.destination);
    } catch (error) { this.close(); throw error; }
  }

  async connect(url: string) {
    if (!this.media || !this.context || this.closed) throw new Error("Microphone not prepared.");
    this.rejectConnect?.(new Error("Voice connection superseded.")); this.rejectConnect = null;
    const epoch = ++this.epoch;
    const previous = this.socket;
    this.socket = null; previous?.close(1000, "bridge replaced");
    this.clearPlayback(); this.pendingFrame = null; this.cancelled.clear(); this.reported.clear();
    this.onState("connecting");
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer"; this.socket = socket;
    const current = () => !this.closed && this.socket === socket && this.epoch === epoch;
    await new Promise<void>((resolve, reject) => {
      let opened = false;
      this.rejectConnect = reject;
      // Install handlers before advertising playback receipt support.
      socket.onmessage = event => {
        if (!current()) return;
        if (typeof event.data === "string") {
          let control;
          try { control = JSON.parse(event.data); } catch { return; }
          if (control?.type === "interrupt") {
            const item = typeof control.item_id === "string" && control.item_id ? control.item_id : undefined;
            this.reportProgress();
            if (item) this.cancelled.add(item);
            else {
              for (const p of this.playback.values()) if (p.frame) this.cancelled.add(p.frame.item_id);
              if (this.pendingFrame) this.cancelled.add(this.pendingFrame.item_id);
            }
            this.clearPlayback(item);
            // Retain metadata until its binary partner arrives, including canceled IDs.
            if (item) this.send(socket, { type: "playback.cleared", item_id: item });
            this.updateState();
          } else if (control?.type === "audio.frame" && typeof control.item_id === "string" && Number.isFinite(control.audio_end_ms) && control.audio_end_ms >= 0) this.pendingFrame = control as AudioFrame;
        } else if (event.data instanceof ArrayBuffer) {
          const frame = this.pendingFrame; this.pendingFrame = null;
          if (!frame || !this.cancelled.has(frame.item_id)) this.playPCM(event.data, frame, socket, epoch);
        }
      };
      socket.onclose = () => {
        if (!current()) return;
        this.socket = null; this.pendingFrame = null; this.rejectConnect = null; this.clearPlayback();
        if (!opened) reject(new Error("Audio bridge closed before connection.")); else this.onDisconnect();
      };
      socket.addEventListener("error", () => { if (!opened) reject(new Error("Audio bridge connection failed.")); }, { once: true });
      socket.addEventListener("open", () => {
        if (!current()) { socket.close(); reject(new Error("Voice connection superseded.")); return; }
        opened = true; this.rejectConnect = null; this.capture?.port.postMessage({ type: "reset-speech" }); this.send(socket, { type: "playback.ready" }); this.onState("listening"); resolve();
      }, { once: true });
    });
  }

  setMuted(value: boolean) {
    this.muted = value; this.capture?.port.postMessage({ type: "reset-speech" });
    for (const track of this.media?.getAudioTracks() ?? []) track.enabled = !value;
  }
  close() {
    this.rejectConnect?.(new Error("Voice connection closed.")); this.rejectConnect = null;
    this.closed = true; ++this.epoch;
    this.socket?.close(1000, "voice ended"); this.socket = null;
    this.pendingFrame = null; this.clearPlayback(); this.cancelled.clear(); this.reported.clear();
    this.capture?.disconnect(); this.source?.disconnect();
    for (const track of this.media?.getTracks() ?? []) track.stop();
    this.media = null; this.capture = null; this.source = null;
    if (this.context) void this.context.close();
    this.context = null; this.onState("closed");
  }
  private send(socket: WebSocket, value: object) {
    if (socket === this.socket && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(value));
  }
  private playPCM(buffer: ArrayBuffer, frame: AudioFrame | null, socket: WebSocket, epoch: number) {
    const context = this.context;
    if (!context || context.state === "closed" || buffer.byteLength % 2) return;
    const pcm = new Int16Array(buffer);
    if (!pcm.length) return;
    const audio = context.createBuffer(1, pcm.length, 24000);
    const channel = audio.getChannelData(0);
    let voiced = false;
    for (let i = 0; i < pcm.length; i++) { channel[i] = pcm[i] / 32768; if (Math.abs(pcm[i]) > 128) voiced = true; }
    const source = context.createBufferSource(); source.buffer = audio; source.connect(context.destination);
    const start = Math.max(context.currentTime + 0.015, this.playbackCursor);
    const entry: Playback = { frame, start, end: start + audio.duration, socket, epoch, silent: frame?.silence ?? !voiced };
    this.playbackCursor = entry.end; this.playback.set(source, entry);
    source.onended = () => {
      if (this.playback.get(source) !== entry || epoch !== this.epoch || socket !== this.socket) return;
      this.reportEntry(entry); this.playback.delete(source); source.disconnect(); this.updateState();
      if (!this.playback.size) this.stopProgress();
    };
    source.start(start);
    if (!this.progressTimer) this.progressTimer = setInterval(() => { this.reportProgress(); this.updateState(); }, 50);
    this.updateState();
  }
  private reportEntry(entry: Playback) {
    const frame = entry.frame;
    if (!frame || entry.epoch !== this.epoch || entry.socket !== this.socket || this.cancelled.has(frame.item_id) || !this.context) return;
    const elapsed = Math.max(0, Math.min(entry.end - entry.start, this.context.currentTime - entry.start));
    if (!elapsed) return;
    const end = Math.max(0, Math.min(frame.audio_end_ms, Math.floor(frame.audio_end_ms - (entry.end - entry.start - elapsed) * 1000 + 0.001)));
    if (end <= (this.reported.get(frame.item_id) ?? 0)) return;
    this.reported.set(frame.item_id, end); this.send(entry.socket, { type: "playback.progress", item_id: frame.item_id, audio_end_ms: end });
  }
  private reportProgress() { for (const entry of this.playback.values()) this.reportEntry(entry); }
  private updateState() {
    if (!this.closed) this.onState([...this.playback.values()].some(p => !p.silent && p.end > (this.context?.currentTime ?? 0)) ? "speaking" : "listening");
  }
  private stopProgress() { if (this.progressTimer) clearInterval(this.progressTimer); this.progressTimer = null; }
  private clearPlayback(item?: string) {
    for (const [source, entry] of this.playback) {
      if (item && entry.frame?.item_id !== item) continue;
      // Remove before stop(): canceled onended must never acknowledge full audio.
      this.playback.delete(source); source.onended = null;
      try { source.stop(); } catch { /* already ended */ }
      source.disconnect();
    }
    this.playbackCursor = Math.max(this.context?.currentTime ?? 0, ...[...this.playback.values()].map(p => p.end));
    if (!this.playback.size) this.stopProgress();
  }
}
