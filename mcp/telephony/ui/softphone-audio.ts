// Browser audio engine for the Telephony softphone.
//
// Wire format on both directions of the media socket is the same one every
// carrier bridge in this app speaks: raw PCM16LE mono at 24 kHz, one binary
// WebSocket frame per 20 ms (480 samples). Text frames carry status events.
//
// The AudioContext is opened at 24 kHz so neither direction needs resampling in
// JS — the browser's own high-quality resampler handles the device rate. If a
// browser refuses that rate we fall back to band-limited resampling rather than
// failing the call.

const SAMPLE_RATE = 24_000;
const JITTER_TARGET_MS = 80;
const MAX_RECONNECT_MS = 30_000;

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

// The local microphone preview uses the same streaming anti-alias filter as
// the worker fallback, with independent history for each recording session.
class PreviewResampler {
  private history = new Float32Array(64);
  private phase = 0;
  process(frame:Float32Array,from:number,to:number):Float32Array {
    if(from===to)return frame;
    const input=new Float32Array(64+frame.length);input.set(this.history);input.set(frame,64);
    const ratio=from/to,cutoff=Math.min(1,to/from)*0.9,values:number[]=[];
    let pos=this.phase;
    for(;pos<frame.length;pos+=ratio){const center=pos+32;let value=0,weight=0;
      for(let j=Math.ceil(center-32);j<=Math.floor(center+32);j++){const x=j-center;const sinc=Math.abs(x)<1e-9?cutoff:Math.sin(Math.PI*cutoff*x)/(Math.PI*x);const w=sinc*(0.5+0.5*Math.cos(Math.PI*x/32));if(j>=0&&j<input.length){value+=input[j]*w;weight+=w;}}
      values.push(weight?value/weight:0);
    }
    this.phase=pos-frame.length;this.history=input.slice(-64);return new Float32Array(values);
  }
}

function rms(frame: Float32Array): number {
  let sum = 0;
  for (let i = 0; i < frame.length; i++) sum += frame[i] * frame[i];
  return Math.sqrt(sum / Math.max(1, frame.length));
}

export type SoftphoneState = "connecting" | "reconnecting" | "live" | "ended" | "error";

export interface SoftphoneCallbacks {
  onState?: (state: SoftphoneState, detail?: string) => void;
  onNotice?: (detail:string) => void;
  onLevels?: (mic: number, speaker: number) => void;
  onDiagnostics?: (diagnostics: SoftphoneDiagnostics) => void;
}

export interface SoftphoneAudioOptions {
  inputDeviceId?: string;
  outputDeviceId?: string;
  outputVolume?: number;
  echoCancellation: boolean;
  noiseSuppression: boolean;
  autoGainControl: boolean;
  inputGainDB: number;
  highpassFilter: boolean;
}

export interface AudioDropEvent {
  timestamp: string;
  direction: string;
  reason: string;
  duration_ms: number;
  queue_before_ms?: number;
  queue_after_ms?: number;
  sequence?: number | null;
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
  micPostPeakDbfs: number | null;
  micInputGainDb: number;
  micLimiterReductionDb: number;
  captureSequenceGaps: number;
  playbackSequenceGaps: number;
  dropEvents: AudioDropEvent[];
}

export const DEFAULT_SOFTPHONE_AUDIO_OPTIONS: SoftphoneAudioOptions = {
  echoCancellation: true,
  noiseSuppression: false,
  autoGainControl: false,
  inputGainDB: -6,
  highpassFilter: true,
};

export function microphoneConstraints(options: SoftphoneAudioOptions): MediaTrackConstraints {
  return {
    ...(options.inputDeviceId ? {deviceId:{exact:options.inputDeviceId}} : {}),
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
  postPeakDbfs: number | null;
  limiterReductionDb: number;
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
  private postPeak = 0;
  private limiterReductionDB = 0;
  private settings: MicrophoneAppliedSettings | null = null;
  private stopped = false;
  private resampler = new PreviewResampler();
  private ensureOpen(): void { if (this.stopped) { void this.release(); throw new Error("Microphone test cancelled."); } }

  constructor(private readonly onLevel?: (level: number) => void) {}

  async start(workletURL: string, options: SoftphoneAudioOptions): Promise<MicrophoneAppliedSettings> {
    try {
      this.stream = await navigator.mediaDevices.getUserMedia({ audio: microphoneConstraints(options) });
      this.ensureOpen();
      const track = this.stream.getAudioTracks()[0];
      if (!track) throw new Error("No microphone audio track was returned.");
      this.settings = appliedMicrophoneSettings(track);

      try { this.ctx = new AudioContext({ sampleRate: SAMPLE_RATE, latencyHint: "interactive" }); }
      catch { this.ctx = new AudioContext({ latencyHint: "interactive" }); }
      if (this.ctx.state === "suspended") await this.ctx.resume();
      this.ensureOpen();
      await this.ctx.audioWorklet.addModule(workletURL);
      this.ensureOpen();
      const contextRate = this.ctx.sampleRate;
      const source = this.ctx.createMediaStreamSource(this.stream);
      this.capture = new AudioWorkletNode(this.ctx, "softphone-capture", {
        processorOptions: { inputGainDB: options.inputGainDB, highpassFilter: options.highpassFilter },
      });
      // Keep the capture branch renderable without monitoring the microphone.
      this.sink = this.ctx.createGain();
      this.sink.gain.value = 0;
      this.capture.connect(this.sink).connect(this.ctx.destination);
      this.capture.port.onmessage = (event: MessageEvent<Float32Array | { type?: string; post_peak?: number; limiter_reduction_db?: number }>) => {
        if (this.stopped) return;
        if (!(event.data instanceof Float32Array)) {
          if (event.data.type === "capture.stats") {
            this.peak = Math.max(this.peak, (event.data as { pre_peak?: number }).pre_peak ?? 0);
            this.postPeak = Math.max(this.postPeak, event.data.post_peak ?? 0);
            this.limiterReductionDB = Math.max(this.limiterReductionDB, event.data.limiter_reduction_db ?? 0);
          }
          return;
        }
        let frame = event.data;
        if (contextRate !== SAMPLE_RATE) frame = this.resampler.process(frame, contextRate, SAMPLE_RATE);
        const level = rms(frame);
        this.onLevel?.(level);
        const pcm = new Int16Array(floatToPCM16(frame));
        this.frames.push(pcm);
        this.samples += pcm.length;
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
      postPeakDbfs: dbfs(this.postPeak || peak),
      limiterReductionDb: this.limiterReductionDB,
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
  private worker: Worker | null = null;
  private ctx: AudioContext | null = null;
  private stream: MediaStream | null = null;
  private capture: AudioWorkletNode | null = null;
  private playback: AudioWorkletNode | null = null;
  private sink: GainNode | null = null;
  private output: GainNode | null = null;
  private muted = false;
  private closed = false;
  private micLevel = 0;
  private speakerLevel = 0;
  private levelTimer: ReturnType<typeof setInterval> | null = null;
  private pingTimer: ReturnType<typeof setInterval> | null = null;
  private opened = false;
  private microphoneTransportReady = false;
  private diagnostics: SoftphoneDiagnostics = {
    rttMs: null, queueMs: 0, targetMs: JITTER_TARGET_MS, underruns: 0,
    droppedMs: 0, maxQueueMs: 0, audioContextRate: SAMPLE_RATE,
    websocketBufferedBytes: 0, microphoneSampleRate: 0, microphoneChannelCount: 0,
    echoCancellation: null, noiseSuppression: null, autoGainControl: null,
    micActiveRmsDbfs: null, micPeakDbfs: null, micPostPeakDbfs: null,
    micInputGainDb: DEFAULT_SOFTPHONE_AUDIO_OPTIONS.inputGainDB, micLimiterReductionDb: 0,
    captureSequenceGaps: 0, playbackSequenceGaps: 0, dropEvents: [],
  };

  constructor(private readonly callbacks: SoftphoneCallbacks = {}) {}

  get isMuted(): boolean { return this.muted; }

  async start(
    mediaURL: string,
    workletURL: string,
    workerURL: string,
    options: SoftphoneAudioOptions = DEFAULT_SOFTPHONE_AUDIO_OPTIONS,
  ): Promise<void> {
    this.callbacks.onState?.("connecting");
    try {
      this.stream = await navigator.mediaDevices.getUserMedia({ audio: microphoneConstraints(options) });
      this.ensureOpen();
      const track = this.stream.getAudioTracks()[0];
      if (!track) throw new Error("No microphone audio track was returned.");
      const applied = appliedMicrophoneSettings(track);
      this.diagnostics = {
        ...this.diagnostics, microphoneSampleRate: applied.sampleRate ?? 0,
        microphoneChannelCount: applied.channelCount ?? 0, echoCancellation: applied.echoCancellation,
        noiseSuppression: applied.noiseSuppression, autoGainControl: applied.autoGainControl,
        micInputGainDb: options.inputGainDB,
      };
      try { this.ctx = new AudioContext({ sampleRate:SAMPLE_RATE, latencyHint:"interactive" }); }
      catch { this.ctx = new AudioContext({latencyHint:"interactive"}); }
      if (this.ctx.state === "suspended") await this.ctx.resume();
      this.ensureOpen();
      if (options.outputDeviceId && "setSinkId" in this.ctx) { await (this.ctx as AudioContext & {setSinkId(id:string):Promise<void>}).setSinkId(options.outputDeviceId); this.ensureOpen(); }
      await this.ctx.audioWorklet.addModule(workletURL);
      this.ensureOpen();
      this.diagnostics.audioContextRate = this.ctx.sampleRate;
      const source = this.ctx.createMediaStreamSource(this.stream);
      this.capture = new AudioWorkletNode(this.ctx, "softphone-capture", {
        processorOptions: { inputGainDB: options.inputGainDB, highpassFilter: options.highpassFilter },
      });
      this.playback = new AudioWorkletNode(this.ctx, "softphone-playback", {
        numberOfInputs: 0, outputChannelCount: [1],
        processorOptions: { initialTargetMs: JITTER_TARGET_MS, minTargetMs: 60, maxTargetMs: 160, hardMaxMs: 320 },
      });
      this.installWorkletDiagnostics();
      this.sink = this.ctx.createGain();
      this.sink.gain.value = 0;
      this.capture.connect(this.sink).connect(this.ctx.destination);
      source.connect(this.capture);
      this.output = this.ctx.createGain(); this.output.gain.value = options.outputVolume ?? 1;
      this.playback.connect(this.output).connect(this.ctx.destination);
      await this.openWorker(mediaURL, workerURL);
      this.ensureOpen();
      track.onmute = () => this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser.");
      track.onunmute = () => this.callbacks.onNotice?.("Microphone input restored.");
      track.onended = () => { if (!this.closed) this.fail("Microphone disconnected. Select a microphone and reconnect audio."); };
      this.capture.onprocessorerror = this.playback.onprocessorerror = () => this.fail("Audio processing stopped. Reconnect audio.");
      this.ctx.onstatechange = () => { if (!this.closed && this.ctx?.state === "suspended") this.callbacks.onState?.("reconnecting", "Browser paused audio. Resume audio to continue."); };
      this.levelTimer = setInterval(() => {
        this.callbacks.onLevels?.(this.micLevel, this.speakerLevel);
        this.micLevel *= 0.65;
        this.speakerLevel *= 0.65;
      }, 100);
    } catch (error) {
      if (this.closed) this.teardown();
      if (!this.closed) this.fail(error instanceof Error ? error.message : "browser audio setup failed");
      throw error;
    }
  }

  private ensureOpen(): void { if (this.closed) { this.teardown(); throw new Error("Audio session was cancelled."); } }

  async resumeAudio(): Promise<void> { await this.ctx?.resume(); if (this.microphoneTransportReady) this.callbacks.onState?.("live"); }
  setOutputVolume(value:number): void { if (this.output) this.output.gain.value=Math.max(0,Math.min(1,value)); }
  sendDTMF(digits: string): void { if (/^[0-9*#]+$/.test(digits)) this.sendText(JSON.stringify({type:"dtmf",digits})); }

  private installWorkletDiagnostics(): void {
    if (!this.capture || !this.playback) return;
    this.capture.port.onmessage = (event: MessageEvent) => {
      const stats = event.data;
      if (stats?.type !== "capture.stats") return;
      this.micLevel = this.muted ? 0 : (stats.active_rms ?? 0);
      this.diagnostics = {
        ...this.diagnostics, micActiveRmsDbfs: dbfs(stats.active_rms ?? 0),
        micPeakDbfs: dbfs(stats.pre_peak ?? 0), micPostPeakDbfs: dbfs(stats.post_peak ?? 0),
        micInputGainDb: stats.input_gain_db ?? this.diagnostics.micInputGainDb,
        micLimiterReductionDb: stats.limiter_reduction_db ?? 0,
      };
    };
    this.playback.port.onmessage = (event: MessageEvent) => {
      const stats = event.data;
      if (stats?.type !== "stats") return;
      this.speakerLevel = Math.max(this.speakerLevel, stats.speaker_level ?? 0);
      this.diagnostics = {
        ...this.diagnostics, queueMs: stats.queue_ms ?? 0, targetMs: stats.target_ms ?? JITTER_TARGET_MS,
        underruns: stats.underruns ?? 0, droppedMs: stats.dropped_ms ?? 0,
        maxQueueMs: stats.max_queue_ms ?? 0, playbackSequenceGaps: stats.playback_sequence_gaps ?? 0,
        dropEvents: [...this.diagnostics.dropEvents.filter((item) => item.direction !== "carrier_to_operator"), ...(stats.drop_events ?? [])].slice(-100),
      };
      this.callbacks.onDiagnostics?.({ ...this.diagnostics });
    };
  }

  private openWorker(mediaURL: string, workerURL: string): Promise<void> {
    return new Promise((resolve, reject) => {
      const worker = new Worker(workerURL);
      this.worker = worker;
      const captureChannel = new MessageChannel();
      const playbackChannel = new MessageChannel();
      this.capture?.port.postMessage({ type: "transport", port: captureChannel.port1 }, [captureChannel.port1]);
      this.playback?.port.postMessage({ type: "transport", port: playbackChannel.port1 }, [playbackChannel.port1]);
      let settled = false;
      const timeout = setTimeout(() => {
        if (settled) return;
        settled = true;
        reject(new Error("audio connection timed out"));
      }, 10_000);
      const finish = (error?: Error) => {
        if (settled) return;
        settled = true;
        clearTimeout(timeout);
        if (error) reject(error); else resolve();
      };
      worker.onmessage = (event: MessageEvent) => {
        if (this.closed) { finish(new Error("audio session closed")); return; }
        const message = event.data;
        if (message?.type === "socket.open") {
          this.opened = true;
          this.startRTTProbe();
          finish();
        } else if (message?.type === "socket.message") {
          this.handleControl(message.data);
        } else if (message?.type === "socket.close") {
          this.stopRTTProbe();
          this.microphoneTransportReady = false;
          if (this.opened && !this.closed) this.callbacks.onState?.("reconnecting", "Connection interrupted; retrying…");
          else finish(new Error("audio connection closed before it was ready"));
        } else if (message?.type === "socket.failed") {
          this.fail(message.detail || "audio connection lost");
        } else if (message?.type === "transport.drop" && message.event) {
          this.diagnostics.dropEvents = [...this.diagnostics.dropEvents, message.event].slice(-100);
        } else if (message?.type === "transport.stats") {
          this.diagnostics.websocketBufferedBytes = message.buffered_bytes ?? 0;
        }
      };
      worker.onerror = () => { finish(new Error("audio worker failed")); if (!this.closed) this.fail("Audio worker failed. Reconnect audio."); };
      worker.postMessage({
        type: "init", mediaURL, contextRate: this.ctx?.sampleRate ?? SAMPLE_RATE,
        capturePort: captureChannel.port2, playbackPort: playbackChannel.port2,
      }, [captureChannel.port2, playbackChannel.port2]);
    });
  }

  private handleControl(data: string): void {
    if (this.closed) return;
    try {
      const parsed = JSON.parse(data) as { type?: string; detail?: string; nonce?: number; capture_sequence_gaps?:number };
      if (parsed.type === "dtmf.error" || parsed.type === "dtmf.sent") { this.callbacks.onNotice?.(parsed.type === "dtmf.sent" ? "Keypad tone sent" : parsed.detail || "Keypad tone failed");
      } else if (parsed.type === "pong" && typeof parsed.nonce === "number") {
        this.diagnostics.captureSequenceGaps = parsed.capture_sequence_gaps ?? this.diagnostics.captureSequenceGaps;
        this.diagnostics.rttMs = Math.max(0, Math.round(performance.now() - parsed.nonce));
        this.callbacks.onDiagnostics?.({ ...this.diagnostics });
      } else if (parsed.type === "call.ended" || parsed.type === "session.replaced") {
        this.closed = true; this.callbacks.onState?.("ended", parsed.type); this.teardown();
      } else if (parsed.type === "call.error") {
        this.fail(parsed.detail || "The call could not be connected.");
      } else if (parsed.type === "peer.disconnected") {
        this.microphoneTransportReady = false;
        this.worker?.postMessage({ type: "microphone.ready", value: false });
        this.worker?.postMessage({ type: "flush" });
        this.callbacks.onState?.("reconnecting", "Carrier audio interrupted; reconnecting…");
      } else if (parsed.type === "peer.connected") {
        this.microphoneTransportReady = true;
        this.worker?.postMessage({ type: "microphone.ready", value: true });
        this.callbacks.onState?.("live", this.opened ? undefined : "Audio reconnected");
      }
    } catch { /* Status frames are advisory. */ }
  }

  private startRTTProbe(): void {
    this.stopRTTProbe();
    const ping = () => {
      this.sendText(JSON.stringify({ type: "ping", nonce: performance.now() }));
      this.sendDiagnostics();
    };
    ping();
    this.pingTimer = setInterval(ping, 5_000);
  }

  private sendText(data: string): void { this.worker?.postMessage({ type: "send.text", data }); }

  private sendDiagnostics(): void {
    const value = this.diagnostics;
    this.sendText(JSON.stringify({ type: "diagnostics", diagnostics: {
      rtt_ms: value.rttMs, playback_queue_ms: value.queueMs, playback_target_ms: value.targetMs,
      playback_max_queue_ms: value.maxQueueMs, playback_underruns: value.underruns,
      playback_dropped_ms: value.droppedMs, websocket_buffered_bytes: value.websocketBufferedBytes,
      audio_context_rate: value.audioContextRate, microphone_sample_rate: value.microphoneSampleRate,
      microphone_channel_count: value.microphoneChannelCount, echo_cancellation: value.echoCancellation,
      noise_suppression: value.noiseSuppression, auto_gain_control: value.autoGainControl,
      mic_active_rms_dbfs: value.micActiveRmsDbfs, mic_peak_dbfs: value.micPeakDbfs,
      mic_post_peak_dbfs: value.micPostPeakDbfs, mic_input_gain_db: value.micInputGainDb,
      mic_limiter_reduction_db: value.micLimiterReductionDb, capture_sequence_gaps: value.captureSequenceGaps,
      playback_sequence_gaps: value.playbackSequenceGaps, drop_events: value.dropEvents,
    }}));
    this.callbacks.onDiagnostics?.({ ...value });
  }

  private stopRTTProbe(): void {
    if (this.pingTimer !== null) clearInterval(this.pingTimer);
    this.pingTimer = null;
  }

  setMuted(muted: boolean): void {
    this.muted = muted;
    this.worker?.postMessage({type:"muted",value:muted});
    this.capture?.port.postMessage({ type: "muted", value: muted });
    if (muted) this.sendText(JSON.stringify({ type: "interrupt" }));
  }

  stop(): void {
    if (!this.closed) this.callbacks.onState?.("ended");
    this.closed = true; this.teardown();
  }

  private fail(detail: string): void {
    this.closed = true; this.callbacks.onState?.("error", detail); this.teardown();
  }

  private teardown(): void {
    this.microphoneTransportReady = false;
    this.sendDiagnostics();
    this.stopRTTProbe();
    if (this.levelTimer !== null) clearInterval(this.levelTimer);
    this.levelTimer = null;
    const worker = this.worker;
    worker?.postMessage({ type: "close" });
    if (worker) setTimeout(() => worker.terminate(), 100);
    this.worker = null;
    this.capture?.disconnect();
    this.playback?.disconnect();
    this.sink?.disconnect(); this.output?.disconnect(); this.output=null;
    this.stream?.getTracks().forEach((track) => track.stop());
    this.stream = null;
    void this.ctx?.close().catch(() => undefined);
    this.ctx = null; this.capture = null; this.playback = null; this.sink = null;
  }
}

export const SOFTPHONE_JITTER_TARGET_SAMPLES = SAMPLE_RATE * JITTER_TARGET_MS / 1000;
export const SOFTPHONE_MAX_RECONNECT_MS = MAX_RECONNECT_MS;
