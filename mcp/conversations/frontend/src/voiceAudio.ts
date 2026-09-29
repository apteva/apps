export type VoiceAudioState = "connecting" | "listening" | "speaking" | "closed";

interface AudioFrame { type: "audio.frame"; item_id: string; audio_end_ms: number }

// PCM bridge protocol is shared with the dashboard, but this capture module
// and its URL are packaged by Conversations, not served by the dashboard.
export class VoiceAudioClient {
  private media: MediaStream | null = null;
  private context: AudioContext | null = null;
  private source: MediaStreamAudioSourceNode | null = null;
  private capture: AudioWorkletNode | null = null;
  private socket: WebSocket | null = null;
  private muted = false;
  private closed = false;
  private playbackCursor = 0;
  private pendingFrame: AudioFrame | null = null;
  private playback = new Map<AudioBufferSourceNode, AudioFrame | null>();

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
      this.capture.port.onmessage = (event: MessageEvent<ArrayBuffer>) => {
        if (!this.muted && this.socket?.readyState === WebSocket.OPEN) this.socket.send(event.data);
      };
      this.source.connect(this.capture);
      this.capture.connect(this.context.destination);
    } catch (error) { this.close(); throw error; }
  }

  async connect(url: string) {
    if (!this.media || !this.context) throw new Error("Microphone not prepared.");
    this.closed = false;
    this.onState("connecting");
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    this.socket = socket;
    await new Promise<void>((resolve, reject) => {
      socket.addEventListener("open", () => { this.onState("listening"); resolve(); }, { once: true });
      socket.addEventListener("error", () => reject(new Error("Audio bridge connection failed.")), { once: true });
      socket.addEventListener("close", () => reject(new Error("Audio bridge closed before connection.")), { once: true });
    });
    socket.onmessage = event => {
      if (typeof event.data === "string") {
        try {
          const control = JSON.parse(event.data);
          if (control?.type === "interrupt") { this.pendingFrame = null; this.clearPlayback(); this.onState("listening"); }
          else if (control?.type === "audio.frame" && typeof control.item_id === "string" && Number.isFinite(control.audio_end_ms)) this.pendingFrame = control as AudioFrame;
        } catch { /* Forward-compatible unknown control. */ }
      } else if (event.data instanceof ArrayBuffer) {
        const frame = this.pendingFrame;
        this.pendingFrame = null;
        this.playPCM(event.data, frame);
      }
    };
    socket.onclose = () => { if (this.socket === socket) this.socket = null; this.pendingFrame = null; this.clearPlayback(); if (!this.closed) this.onDisconnect(); };
  }

  setMuted(value: boolean) {
    this.muted = value;
    for (const track of this.media?.getAudioTracks() ?? []) track.enabled = !value;
  }

  close() {
    this.closed = true;
    this.socket?.close(1000, "voice ended");
    this.socket = null;
    this.clearPlayback();
    this.capture?.disconnect();
    this.source?.disconnect();
    for (const track of this.media?.getTracks() ?? []) track.stop();
    this.media = null; this.capture = null; this.source = null;
    if (this.context) void this.context.close();
    this.context = null;
    this.onState("closed");
  }

  private playPCM(buffer: ArrayBuffer, frame: AudioFrame | null) {
    const context = this.context;
    if (!context || context.state === "closed") return;
    const pcm = new Int16Array(buffer);
    if (!pcm.length) return;
    const audio = context.createBuffer(1, pcm.length, 24000);
    const channel = audio.getChannelData(0);
    for (let i = 0; i < pcm.length; i++) channel[i] = pcm[i] / 32768;
    const source = context.createBufferSource();
    source.buffer = audio; source.connect(context.destination);
    const start = Math.max(context.currentTime + 0.015, this.playbackCursor);
    this.playbackCursor = start + audio.duration;
    this.playback.set(source, frame);
    source.onended = () => {
      const played = this.playback.get(source);
      this.playback.delete(source);
      if (played && this.socket?.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify({ type: "playback.progress", item_id: played.item_id, audio_end_ms: played.audio_end_ms }));
      if (!this.playback.size && !this.closed) this.onState("listening");
    };
    this.onState("speaking");
    source.start(start);
  }

  private clearPlayback() {
    const sources = [...this.playback.keys()];
    this.playback.clear();
    for (const source of sources) { try { source.stop(); } catch { /* already ended */ } }
    this.playbackCursor = this.context?.currentTime ?? 0;
  }
}
