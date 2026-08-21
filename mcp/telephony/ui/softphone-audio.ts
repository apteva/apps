// Browser audio engine for the Telephony softphone.
//
// Wire format on both directions of the media socket is the same one every
// carrier bridge in this app speaks: raw PCM16LE mono at 24 kHz, one binary
// WebSocket frame per 20 ms (480 samples). Text frames carry status events.
//
// The AudioContext is opened at 24 kHz so neither direction needs resampling in
// JS — the browser's own high-quality resampler handles the device rate. If a
// browser refuses that rate we fall back to linear resampling rather than
// failing the call.

const SAMPLE_RATE = 24_000;
// ~80 ms of slack absorbs network jitter without adding audible latency.
const JITTER_TARGET_SAMPLES = SAMPLE_RATE * 0.08;
const MAX_RECONNECT_MS = 30_000;
const MAX_RECONNECT_DELAY_MS = 4_000;

function floatToPCM16(input: Float32Array): ArrayBuffer {
  const out = new Int16Array(input.length);
  for (let i = 0; i < input.length; i++) {
    const clamped = Math.max(-1, Math.min(1, input[i]));
    out[i] = clamped < 0 ? clamped * 0x8000 : clamped * 0x7fff;
  }
  return out.buffer;
}

function pcm16ToFloat(buffer: ArrayBuffer): Float32Array {
  const input = new Int16Array(buffer);
  const out = new Float32Array(input.length);
  for (let i = 0; i < input.length; i++) out[i] = input[i] / 0x8000;
  return out;
}

// Linear resampling fallback, only used when a browser refuses a 24 kHz context.
function resample(input: Float32Array, from: number, to: number): Float32Array {
  if (from === to) return input;
  const ratio = from / to;
  const out = new Float32Array(Math.floor(input.length / ratio));
  for (let i = 0; i < out.length; i++) {
    const pos = i * ratio;
    const low = Math.floor(pos);
    const high = Math.min(low + 1, input.length - 1);
    out[i] = input[low] + (input[high] - input[low]) * (pos - low);
  }
  return out;
}

function rms(frame: Float32Array): number {
  let sum = 0;
  for (let i = 0; i < frame.length; i++) sum += frame[i] * frame[i];
  return Math.sqrt(sum / Math.max(1, frame.length));
}

export type SoftphoneState = "connecting" | "reconnecting" | "live" | "ended" | "error";

export interface SoftphoneCallbacks {
  onState?: (state: SoftphoneState, detail?: string) => void;
  onLevels?: (mic: number, speaker: number) => void;
}

export class SoftphoneSession {
  private ws: WebSocket | null = null;
  private ctx: AudioContext | null = null;
  private stream: MediaStream | null = null;
  private capture: AudioWorkletNode | null = null;
  private playback: AudioWorkletNode | null = null;
  private muted = false;
  private closed = false;
  private micLevel = 0;
  private speakerLevel = 0;
  private levelTimer: ReturnType<typeof setInterval> | null = null;
  private mediaURL = "";
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectStartedAt = 0;
  private reconnectAttempt = 0;

  constructor(private readonly callbacks: SoftphoneCallbacks = {}) {}

  get isMuted(): boolean {
    return this.muted;
  }

  async start(mediaURL: string, workletURL: string): Promise<void> {
    this.callbacks.onState?.("connecting");
    this.mediaURL = mediaURL;
    try {
      // Echo cancellation matters more than anything else here: without it the
      // caller hears themselves through the operator's speakers.
      this.stream = await navigator.mediaDevices.getUserMedia({
        audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
      });
    } catch (err) {
      this.fail("microphone permission denied");
      throw err;
    }

    let deviceRate = SAMPLE_RATE;
    try {
      // Constructed from the operator's Answer click. getUserMedia may display
      // a permission prompt, but the resumed context remains associated with
      // that explicit interaction.
      this.ctx = new AudioContext({ sampleRate: SAMPLE_RATE, latencyHint: "interactive" });
      if (this.ctx.state === "suspended") await this.ctx.resume();
      await this.ctx.audioWorklet.addModule(workletURL);

      deviceRate = this.ctx.sampleRate;
      const source = this.ctx.createMediaStreamSource(this.stream);
      this.capture = new AudioWorkletNode(this.ctx, "softphone-capture");
      this.playback = new AudioWorkletNode(this.ctx, "softphone-playback", {
        numberOfInputs: 0,
        outputChannelCount: [1],
      });
      source.connect(this.capture);
      this.playback.connect(this.ctx.destination);

      await this.openSocket(mediaURL);
    } catch (err) {
      if (!this.closed) {
        const detail = err instanceof Error && err.message ? err.message : "browser audio setup failed";
        this.fail(detail);
      }
      throw err;
    }

    this.capture.port.onmessage = (event: MessageEvent<Float32Array>) => {
      if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
      let frame = event.data;
      if (deviceRate !== SAMPLE_RATE) frame = resample(frame, deviceRate, SAMPLE_RATE);
      this.micLevel = this.muted ? 0 : rms(frame);
      // Muting sends silence rather than nothing: a continuous stream keeps the
      // carrier-side pacer's timing stable.
      if (this.muted) frame = new Float32Array(frame.length);
      this.ws.send(floatToPCM16(frame));
    };

    this.levelTimer = setInterval(() => {
      this.callbacks.onLevels?.(this.micLevel, this.speakerLevel);
      // Decay so the meters fall back to zero when a side goes quiet.
      this.speakerLevel *= 0.6;
    }, 100);
  }

  private openSocket(mediaURL: string): Promise<void> {
    return new Promise((resolve, reject) => {
      const ws = new WebSocket(mediaURL);
      ws.binaryType = "arraybuffer";
      this.ws = ws;
      let settled = false;
      let opened = false;
      const finish = (error?: Error) => {
        if (settled) return;
        settled = true;
        clearTimeout(timeout);
        if (error) reject(error);
        else resolve();
      };
      const timeout = setTimeout(() => {
        try {
          ws.close();
        } catch {
          // The browser may already have discarded the failed socket.
        }
        finish(new Error("audio connection timed out"));
      }, 10_000);

      ws.onopen = () => {
        if (this.closed || this.ws !== ws) {
          ws.close();
          return;
        }
        opened = true;
        finish();
      };
      ws.onerror = () => {
        finish(new Error("audio connection failed"));
      };
      ws.onclose = () => {
        if (this.ws !== ws) return;
        this.ws = null;
        finish(new Error("audio connection closed before it was ready"));
        if (!this.closed && opened) {
          this.scheduleReconnect();
        }
      };
      ws.onmessage = (event: MessageEvent) => {
        if (this.ws !== ws) return;
        if (typeof event.data === "string") {
          try {
            const parsed = JSON.parse(event.data) as { type?: string; detail?: string };
            if (parsed.type === "call.ended" || parsed.type === "session.replaced") {
              this.closed = true;
              this.callbacks.onState?.("ended", parsed.type);
              this.teardown();
            } else if (parsed.type === "call.error") {
              this.fail(parsed.detail || "The call could not be connected.");
            } else if (parsed.type === "peer.disconnected") {
              this.playback?.port.postMessage("flush");
              this.callbacks.onState?.("reconnecting", "Carrier audio interrupted; reconnecting…");
            } else if (parsed.type === "ready" || parsed.type === "peer.connected") {
              this.markLive();
            }
          } catch {
            // Status frames are advisory; a malformed one must not drop audio.
          }
          return;
        }
        let frame = pcm16ToFloat(event.data as ArrayBuffer);
        this.speakerLevel = Math.max(this.speakerLevel, rms(frame));
        if (this.ctx && this.ctx.sampleRate !== SAMPLE_RATE) {
          frame = resample(frame, SAMPLE_RATE, this.ctx.sampleRate);
        }
        this.playback?.port.postMessage(frame);
      };
    });
  }

  private markLive(): void {
    const recovered = this.reconnectStartedAt > 0 || this.reconnectAttempt > 0;
    this.reconnectStartedAt = 0;
    this.reconnectAttempt = 0;
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.callbacks.onState?.("live", recovered ? "Audio reconnected" : undefined);
  }

  private scheduleReconnect(): void {
    if (this.closed || this.reconnectTimer !== null || !this.mediaURL) return;
    const now = Date.now();
    if (this.reconnectStartedAt === 0) this.reconnectStartedAt = now;
    if (now - this.reconnectStartedAt >= MAX_RECONNECT_MS) {
      this.fail("audio connection lost after repeated retries");
      return;
    }
    this.playback?.port.postMessage("flush");
    this.callbacks.onState?.("reconnecting", "Connection interrupted; retrying…");
    const delay = Math.min(250 * (2 ** this.reconnectAttempt), MAX_RECONNECT_DELAY_MS);
    this.reconnectAttempt += 1;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      if (this.closed) return;
      void this.openSocket(this.mediaURL).catch(() => this.scheduleReconnect());
    }, delay);
  }

  setMuted(muted: boolean): void {
    this.muted = muted;
    if (muted) {
      // Drop anything already queued so unmuting resumes at the live edge.
      this.playback?.port.postMessage("flush");
      if (this.ws?.readyState === WebSocket.OPEN) {
        this.ws.send(JSON.stringify({ type: "interrupt" }));
      }
    }
  }

  stop(): void {
    if (this.closed) return;
    this.closed = true;
    this.callbacks.onState?.("ended");
    this.teardown();
  }

  private fail(detail: string): void {
    this.closed = true;
    this.callbacks.onState?.("error", detail);
    this.teardown();
  }

  private teardown(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.levelTimer !== null) {
      clearInterval(this.levelTimer);
      this.levelTimer = null;
    }
    try {
      this.ws?.close();
    } catch {
      // Already closing.
    }
    this.ws = null;
    this.capture?.port.close();
    this.stream?.getTracks().forEach((track) => track.stop());
    this.stream = null;
    void this.ctx?.close().catch(() => undefined);
    this.ctx = null;
    this.capture = null;
    this.playback = null;
  }
}

export const SOFTPHONE_JITTER_TARGET_SAMPLES = JITTER_TARGET_SAMPLES;
export const SOFTPHONE_MAX_RECONNECT_MS = MAX_RECONNECT_MS;
