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
const JITTER_TARGET_MS = 60;
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
  onDiagnostics?: (diagnostics: SoftphoneDiagnostics) => void;
}

export interface SoftphoneAudioOptions {
  echoCancellation: boolean;
  noiseSuppression: boolean;
  autoGainControl: boolean;
}

export interface SoftphoneDiagnostics {
  rttMs: number | null;
  queueMs: number;
  targetMs: number;
  underruns: number;
  droppedMs: number;
  maxQueueMs: number;
  audioContextRate: number;
  websocketBufferedBytes: number;
  microphoneSampleRate: number;
  microphoneChannelCount: number;
  echoCancellation: boolean | null;
  noiseSuppression: boolean | null;
  autoGainControl: boolean | null;
  micActiveRmsDbfs: number | null;
  micPeakDbfs: number | null;
}

export const DEFAULT_SOFTPHONE_AUDIO_OPTIONS: SoftphoneAudioOptions = {
  echoCancellation: true,
  noiseSuppression: false,
  autoGainControl: true,
};

function microphoneConstraints(options: SoftphoneAudioOptions): MediaTrackConstraints {
  return {
    echoCancellation: options.echoCancellation,
    noiseSuppression: options.noiseSuppression,
    autoGainControl: options.autoGainControl,
  };
}

export interface MicrophoneAppliedSettings {
  deviceLabel: string;
  sampleRate: number | null;
  channelCount: number | null;
  echoCancellation: boolean | null;
  noiseSuppression: boolean | null;
  autoGainControl: boolean | null;
}

export interface MicrophoneTestResult {
  audio: Blob;
  durationMs: number;
  sampleRate: number;
  activeRmsDbfs: number | null;
  peakDbfs: number | null;
  settings: MicrophoneAppliedSettings;
}

function appliedMicrophoneSettings(track: MediaStreamTrack): MicrophoneAppliedSettings {
  const settings = track.getSettings() as MediaTrackSettings & {
    echoCancellation?: boolean;
    noiseSuppression?: boolean;
    autoGainControl?: boolean;
  };
  return {
    deviceLabel: track.label || "Default microphone",
    sampleRate: typeof settings.sampleRate === "number" ? settings.sampleRate : null,
    channelCount: typeof settings.channelCount === "number" ? settings.channelCount : null,
    echoCancellation: typeof settings.echoCancellation === "boolean" ? settings.echoCancellation : null,
    noiseSuppression: typeof settings.noiseSuppression === "boolean" ? settings.noiseSuppression : null,
    autoGainControl: typeof settings.autoGainControl === "boolean" ? settings.autoGainControl : null,
  };
}

function dbfs(value: number): number | null {
  if (!Number.isFinite(value) || value <= 0) return null;
  return 20 * Math.log10(value);
}

export function pcm16WAV(frames: Int16Array[], samples: number, sampleRate: number): Blob {
  const buffer = new ArrayBuffer(44 + samples * 2);
  const view = new DataView(buffer);
  const ascii = (offset: number, value: string) => {
    for (let i = 0; i < value.length; i++) view.setUint8(offset + i, value.charCodeAt(i));
  };
  ascii(0, "RIFF");
  view.setUint32(4, 36 + samples * 2, true);
  ascii(8, "WAVE");
  ascii(12, "fmt ");
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);
  view.setUint16(22, 1, true);
  view.setUint32(24, sampleRate, true);
  view.setUint32(28, sampleRate * 2, true);
  view.setUint16(32, 2, true);
  view.setUint16(34, 16, true);
  ascii(36, "data");
  view.setUint32(40, samples * 2, true);
  let offset = 44;
  for (const frame of frames) {
    for (let i = 0; i < frame.length; i++) {
      view.setInt16(offset, frame[i], true);
      offset += 2;
    }
  }
  return new Blob([buffer], { type: "audio/wav" });
}

// A local-only capture through the same browser pipeline used by a live human
// call. It deliberately stops before WebSocket transport and the carrier
// bridge, producing a WAV that never leaves the operator's browser.
export class MicrophoneTestSession {
  private ctx: AudioContext | null = null;
  private stream: MediaStream | null = null;
  private capture: AudioWorkletNode | null = null;
  private sink: GainNode | null = null;
  private frames: Int16Array[] = [];
  private samples = 0;
  private activeSquares = 0;
  private activeSamples = 0;
  private peak = 0;
  private settings: MicrophoneAppliedSettings | null = null;
  private stopped = false;

  constructor(private readonly onLevel?: (level: number) => void) {}

  async start(workletURL: string, options: SoftphoneAudioOptions): Promise<MicrophoneAppliedSettings> {
    try {
      this.stream = await navigator.mediaDevices.getUserMedia({ audio: microphoneConstraints(options) });
      const track = this.stream.getAudioTracks()[0];
      if (!track) throw new Error("No microphone audio track was returned.");
      this.settings = appliedMicrophoneSettings(track);

      this.ctx = new AudioContext({ sampleRate: SAMPLE_RATE, latencyHint: "interactive" });
      if (this.ctx.state === "suspended") await this.ctx.resume();
      await this.ctx.audioWorklet.addModule(workletURL);
      const contextRate = this.ctx.sampleRate;
      const source = this.ctx.createMediaStreamSource(this.stream);
      this.capture = new AudioWorkletNode(this.ctx, "softphone-capture");
      // Keep the capture branch renderable without monitoring the microphone.
      this.sink = this.ctx.createGain();
      this.sink.gain.value = 0;
      this.capture.connect(this.sink).connect(this.ctx.destination);
      this.capture.port.onmessage = (event: MessageEvent<Float32Array>) => {
        if (this.stopped) return;
        let frame = event.data;
        if (contextRate !== SAMPLE_RATE) frame = resample(frame, contextRate, SAMPLE_RATE);
        const level = rms(frame);
        this.onLevel?.(level);
        const pcm = new Int16Array(floatToPCM16(frame));
        this.frames.push(pcm);
        this.samples += pcm.length;
        for (let i = 0; i < frame.length; i++) this.peak = Math.max(this.peak, Math.abs(frame[i]));
        // Exclude silence from the speech-level estimate so a pause before or
        // after speaking does not make a healthy microphone look too quiet.
        if (level >= 0.005) {
          for (let i = 0; i < frame.length; i++) this.activeSquares += frame[i] * frame[i];
          this.activeSamples += frame.length;
        }
      };
      source.connect(this.capture);
      return this.settings;
    } catch (error) {
      await this.release();
      throw error;
    }
  }

  async stop(): Promise<MicrophoneTestResult> {
    this.stopped = true;
    const settings = this.settings;
    const frames = this.frames;
    const samples = this.samples;
    const activeRms = this.activeSamples > 0 ? Math.sqrt(this.activeSquares / this.activeSamples) : 0;
    const peak = this.peak;
    await this.release();
    if (!settings || samples === 0) throw new Error("No microphone audio was captured.");
    return {
      audio: pcm16WAV(frames, samples, SAMPLE_RATE),
      durationMs: Math.round(samples * 1000 / SAMPLE_RATE),
      sampleRate: SAMPLE_RATE,
      activeRmsDbfs: dbfs(activeRms),
      peakDbfs: dbfs(peak),
      settings,
    };
  }

  async cancel(): Promise<void> {
    this.stopped = true;
    await this.release();
  }

  private async release(): Promise<void> {
    if (this.capture) {
      this.capture.port.onmessage = null;
      this.capture.disconnect();
      this.capture = null;
    }
    this.sink?.disconnect();
    this.sink = null;
    for (const track of this.stream?.getTracks() ?? []) track.stop();
    this.stream = null;
    if (this.ctx && this.ctx.state !== "closed") await this.ctx.close();
    this.ctx = null;
    this.onLevel?.(0);
  }
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
  private pingTimer: ReturnType<typeof setInterval> | null = null;
  private micActiveSquares = 0;
  private micActiveSamples = 0;
  private micPeak = 0;
  private diagnostics: SoftphoneDiagnostics = {
    rttMs: null, queueMs: 0, targetMs: JITTER_TARGET_MS, underruns: 0,
    droppedMs: 0, maxQueueMs: 0, audioContextRate: SAMPLE_RATE,
    websocketBufferedBytes: 0, microphoneSampleRate: 0, microphoneChannelCount: 0,
    echoCancellation: null, noiseSuppression: null, autoGainControl: null,
    micActiveRmsDbfs: null, micPeakDbfs: null,
  };

  constructor(private readonly callbacks: SoftphoneCallbacks = {}) {}

  get isMuted(): boolean {
    return this.muted;
  }

  async start(
    mediaURL: string,
    workletURL: string,
    options: SoftphoneAudioOptions = DEFAULT_SOFTPHONE_AUDIO_OPTIONS,
  ): Promise<void> {
    this.callbacks.onState?.("connecting");
    this.mediaURL = mediaURL;
    try {
      // Echo cancellation matters more than anything else here: without it the
      // caller hears themselves through the operator's speakers.
      this.stream = await navigator.mediaDevices.getUserMedia({
        audio: microphoneConstraints(options),
      });
      const track = this.stream.getAudioTracks()[0];
      if (track) {
        const applied = appliedMicrophoneSettings(track);
        this.diagnostics = {
          ...this.diagnostics,
          microphoneSampleRate: applied.sampleRate ?? 0,
          microphoneChannelCount: applied.channelCount ?? 0,
          echoCancellation: applied.echoCancellation,
          noiseSuppression: applied.noiseSuppression,
          autoGainControl: applied.autoGainControl,
        };
      }
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
        processorOptions: {
          initialTargetMs: JITTER_TARGET_MS,
          minTargetMs: 40,
          maxTargetMs: 140,
          hardMaxMs: 250,
        },
      });
      this.diagnostics.audioContextRate = deviceRate;
      this.playback.port.onmessage = (event: MessageEvent) => {
        const stats = event.data as {
          type?: string; queue_ms?: number; target_ms?: number; underruns?: number;
          dropped_ms?: number; max_queue_ms?: number;
        };
        if (stats.type !== "stats") return;
        this.diagnostics = {
          ...this.diagnostics,
          queueMs: stats.queue_ms ?? 0,
          targetMs: stats.target_ms ?? JITTER_TARGET_MS,
          underruns: stats.underruns ?? 0,
          droppedMs: stats.dropped_ms ?? 0,
          maxQueueMs: stats.max_queue_ms ?? 0,
        };
        this.callbacks.onDiagnostics?.({ ...this.diagnostics });
      };
      // Install the capture consumer before connecting the graph. MessagePort
      // otherwise retains frames produced during WebSocket setup and releases
      // them as a burst, which a carrier interprets as seconds of queued audio.
      this.capture.port.onmessage = (event: MessageEvent<Float32Array>) => {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
        let frame = event.data;
        if (deviceRate !== SAMPLE_RATE) frame = resample(frame, deviceRate, SAMPLE_RATE);
        this.micLevel = this.muted ? 0 : rms(frame);
        if (!this.muted) {
          for (let i = 0; i < frame.length; i++) this.micPeak = Math.max(this.micPeak, Math.abs(frame[i]));
          if (this.micLevel >= 0.005) {
            for (let i = 0; i < frame.length; i++) this.micActiveSquares += frame[i] * frame[i];
            this.micActiveSamples += frame.length;
          }
        }
        // Muting sends silence rather than nothing: a continuous stream keeps
        // the carrier-side live pacer's timing stable after answer.
        if (this.muted) frame = new Float32Array(frame.length);
        this.ws.send(floatToPCM16(frame));
      };
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

    this.levelTimer = setInterval(() => {
      this.callbacks.onLevels?.(this.micLevel, this.speakerLevel);
      this.refreshMicrophoneDiagnostics();
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
        this.startRTTProbe(ws);
        finish();
      };
      ws.onerror = () => {
        finish(new Error("audio connection failed"));
      };
      ws.onclose = () => {
        if (this.ws !== ws) return;
        this.ws = null;
        this.stopRTTProbe();
        finish(new Error("audio connection closed before it was ready"));
        if (!this.closed && opened) {
          this.scheduleReconnect();
        }
      };
      ws.onmessage = (event: MessageEvent) => {
        if (this.ws !== ws) return;
        if (typeof event.data === "string") {
          try {
            const parsed = JSON.parse(event.data) as { type?: string; detail?: string; nonce?: number };
            if (parsed.type === "pong" && typeof parsed.nonce === "number") {
              this.diagnostics.rttMs = Math.max(0, Math.round(performance.now() - parsed.nonce));
              this.callbacks.onDiagnostics?.({ ...this.diagnostics });
              return;
            }
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

  private startRTTProbe(ws: WebSocket): void {
    this.stopRTTProbe();
    const ping = () => {
      if (this.ws === ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "ping", nonce: performance.now() }));
        this.sendDiagnostics(ws);
      }
    };
    ping();
    this.pingTimer = setInterval(ping, 5_000);
  }

  private refreshMicrophoneDiagnostics(): void {
    const activeRms = this.micActiveSamples > 0 ? Math.sqrt(this.micActiveSquares / this.micActiveSamples) : 0;
    this.diagnostics.micActiveRmsDbfs = dbfs(activeRms);
    this.diagnostics.micPeakDbfs = dbfs(this.micPeak);
    this.diagnostics.websocketBufferedBytes = this.ws?.bufferedAmount ?? 0;
  }

  private sendDiagnostics(ws: WebSocket): void {
    if (this.ws !== ws || ws.readyState !== WebSocket.OPEN) return;
    this.refreshMicrophoneDiagnostics();
    const value = this.diagnostics;
    ws.send(JSON.stringify({
      type: "diagnostics",
      diagnostics: {
        rtt_ms: value.rttMs,
        playback_queue_ms: value.queueMs,
        playback_target_ms: value.targetMs,
        playback_max_queue_ms: value.maxQueueMs,
        playback_underruns: value.underruns,
        playback_dropped_ms: value.droppedMs,
        websocket_buffered_bytes: value.websocketBufferedBytes,
        audio_context_rate: value.audioContextRate,
        microphone_sample_rate: value.microphoneSampleRate,
        microphone_channel_count: value.microphoneChannelCount,
        echo_cancellation: value.echoCancellation,
        noise_suppression: value.noiseSuppression,
        auto_gain_control: value.autoGainControl,
        mic_active_rms_dbfs: value.micActiveRmsDbfs,
        mic_peak_dbfs: value.micPeakDbfs,
      },
    }));
    this.callbacks.onDiagnostics?.({ ...value });
  }

  private stopRTTProbe(): void {
    if (this.pingTimer !== null) {
      clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
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
    if (this.ws?.readyState === WebSocket.OPEN) this.sendDiagnostics(this.ws);
    this.stopRTTProbe();
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

export const SOFTPHONE_JITTER_TARGET_SAMPLES = SAMPLE_RATE * JITTER_TARGET_MS / 1000;
export const SOFTPHONE_MAX_RECONNECT_MS = MAX_RECONNECT_MS;
