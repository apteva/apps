export type VoiceAudioState = "connecting" | "listening" | "speaking" | "closed";
interface AudioFrame { type: "audio.frame"; item_id: string; audio_end_ms: number; silence?: boolean }

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
  private renderer: AudioWorkletNode | null = null;
  private cleared = new Set<string>();
  private items = new Set<string>();
  private pendingFrame: AudioFrame | null = null;
  private cancelled = new Set<string>();
  private reported = new Map<string, number>();
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
      this.renderer = new AudioWorkletNode(this.context, "conversations-pcm-playback", { numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [1] });
      this.renderer.port.onmessage = ({data}) => {
        const socket=this.socket;
        if (data.epoch!==this.epoch || !socket || socket.readyState!==WebSocket.OPEN || this.closed) return;
        if (data.type==="progress" && !this.cleared.has(data.item_id)) {
          const end=data.audio_end_ms;
          if (end>(this.reported.get(data.item_id)??0)) {this.reported.set(data.item_id,end);this.send(socket,{type:"playback.progress",item_id:data.item_id,audio_end_ms:end});}
        } else if (data.type==="cleared") {
          this.cleared.add(data.item_id);this.send(socket,{type:"playback.cleared",item_id:data.item_id});
        } else if (data.type==="state") this.onState(data.speaking?"speaking":"listening");
      };
      this.renderer.connect(this.context.destination);
      this.source.connect(this.capture); this.capture.connect(this.context.destination);
    } catch (error) { this.close(); throw error; }
  }

  async connect(url: string) {
    if (!this.media || !this.context || this.closed) throw new Error("Microphone not prepared.");
    this.rejectConnect?.(new Error("Voice connection superseded.")); this.rejectConnect = null;
    const epoch = ++this.epoch;
    const previous = this.socket;
    this.socket = null; previous?.close(1000, "bridge replaced");
    this.renderer?.port.postMessage({type:"reset",epoch}); this.pendingFrame = null; this.cancelled.clear(); this.cleared.clear(); this.items.clear(); this.reported.clear();
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
            if (item) this.cancelled.add(item);
            else {
              for (const id of this.items) this.cancelled.add(id);
              if (this.pendingFrame) this.cancelled.add(this.pendingFrame.item_id);
            }
            this.clearPlayback(item);
            // Retain metadata until its binary partner arrives, including canceled IDs.
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
    this.pendingFrame = null; this.clearPlayback(); this.cancelled.clear(); this.cleared.clear(); this.items.clear(); this.reported.clear();
    this.capture?.disconnect(); this.renderer?.disconnect(); this.renderer = null; this.source?.disconnect();
    for (const track of this.media?.getTracks() ?? []) track.stop();
    this.media = null; this.capture = null; this.source = null;
    if (this.context) void this.context.close();
    this.context = null; this.onState("closed");
  }
  private send(socket: WebSocket, value: object) {
    if (socket === this.socket && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(value));
  }
  private playPCM(buffer: ArrayBuffer, frame: AudioFrame | null, _socket: WebSocket, epoch: number) {
    if (!this.context || this.context.state==="closed" || buffer.byteLength%2 || !buffer.byteLength) return;
    let voiced=false;
    for (const sample of new Int16Array(buffer)) if (Math.abs(sample)>128) {voiced=true;break;}
    if (frame) this.items.add(frame.item_id);
    this.renderer?.port.postMessage({type:"pcm",epoch,pcm:buffer,item_id:frame?.item_id,
      audio_end_ms:frame?.audio_end_ms,silence:frame?.silence??!voiced},[buffer]);
  }
  private clearPlayback(item?: string) {
    this.renderer?.port.postMessage({type:"clear",epoch:this.epoch,item_id:item});
  }
}
